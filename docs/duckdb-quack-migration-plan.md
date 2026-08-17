# Plan: Migrate excursion-funnel to DuckDB + Quack shared ledger

## Context

`excursion-funnel` today is a single-user local proxy that meters Claude Code / Codex
token usage into **SQLite** (`modernc.org/sqlite`, pure-Go/cgo-free). Reporting
(`ef usage|tools|inspect`, `/ui`) runs analytical `GROUP BY/SUM` queries, and because
SQLite is row-oriented the project carries a hand-built **materialized-aggregate layer**
(`usage_daily_model`, `usage_model_histogram`, `RefreshUsageAggregates`,
`jobs.AggregateScheduler`) plus string-based date handling (`substr(started_at,1,10)`).

We are evolving it toward a **shared/centralized ledger**: many developers' local
proxies feed one common analytical store with a live team dashboard. That makes a
columnar engine worthwhile and introduces a genuine **multi-writer** need — the exact
case DuckDB's **Quack** protocol (client-server over HTTP+Arrow, core in DuckDB
v1.5.3) exists for, and one SQLite cannot serve.

**Decisions locked with the user:**
- **DuckDB fully replaces SQLite as the analytical store** (standalone local ledger *and*
  hub). Accept cgo. Deletes the materialized-aggregate layer.
- **Distributed local proxy keeps a minimal SQLite outbox** as a durable send-buffer
  (survives hub downtime); `modernc.org/sqlite` is demoted from "the store" to "the spool".
- **Local proxy forwards to the hub over Quack** (DuckDB Quack client → `quack://hub`).

**Outcome:** three roles from one codebase — standalone DuckDB ledger, central DuckDB
hub (`quack_serve` + team dashboard), and a lightweight distributed proxy that spools to
SQLite and pushes to the hub via Quack.

## Key architectural facts (already true in the code)
- `queue.Writer` is already an interface (`InsertBatch(ctx, []UsageEvent)`,
  `internal/queue/queue.go:150`). It is the seam between the **queue's writer goroutine**
  and the store (`queue.go:260`) — the proxy itself writes through the `Enqueue` sink
  (`internal/proxy/proxy.go:70`). **This is the swap seam** — SQLite store, DuckDB store,
  and SQLite-outbox all implement it; the proxy and queue are untouched.
- The queue already batches (50 events / 250 ms, `queue.go:168-175`) — ideal for DuckDB's
  Appender bulk-insert path.
- **DuckDB single-writer constraint bites the CLI even standalone:** `ef usage|inspect|tools`
  open the DB in a *separate process* (`report.Open` → `store.OpenExisting`,
  `store.go:62`). DuckDB forbids a second opener while the daemon holds the file RW. The
  fix (route CLI reads through the daemon via Quack; direct read-only file open as fallback
  when the daemon is down) is designed into Phase 1.

## Dependencies / versions
- Add `github.com/duckdb/duckdb-go` (the maintained path; `marcboeker/go-duckdb` was
  archived Oct 2025). **cgo required** — `CGO_ENABLED=1` + a working C linker
  (mingw-w64 gcc on Windows). The driver ships prebuilt static DuckDB libs, so a full
  C++ toolchain is only needed if we later build a custom DuckDB lib (see extension
  strategy below).
- **Pin `v2.10505.0` (bundles DuckDB 1.5.5)**. duckdb-go encodes the DuckDB version in
  its second semver component (`v2.MAJOR_MINOR_PATCH.x`); 1.5.3 (`v2.10503.x`) is the
  *minimum* — that's when Quack became a signed core extension — but 1.5.5 is current.
- **Extension-loading strategy (important):** Quack is a *core extension*, **not**
  compiled into the engine, and duckdb-go's prebuilt libs statically bundle only
  ICU/JSON/Parquet/Autocomplete. Quack (and the `sqlite` extension used by `ef migrate`)
  is fetched from the DuckDB extension repo over the network on first use
  (autoinstall + autoload). Decision: run explicit `INSTALL quack; LOAD quack` boot
  queries via `duckdb.NewConnector` so failures surface early and clearly; document
  that first run needs network access. Offline/air-gapped support would require a
  custom static DuckDB build (`BUILD_EXTENSIONS="…;quack"` +
  `-tags=duckdb_use_static_lib`) — out of scope unless it becomes a requirement.
- `modernc.org/sqlite` stays in `go.mod`, scoped to the Phase 3 outbox package only.

---

## Phase 1 — DuckDB replaces SQLite as the standalone store (no distribution yet)

Goal: `ef serve` runs on DuckDB, reporting is live, CLI works against a running daemon.

- **`internal/store/store.go` — rewrite for DuckDB** (keep the `database/sql` shape via
  `sql.Open("duckdb", path)`):
  - Schema with real types: `started_at TIMESTAMPTZ`, `completed_at TIMESTAMPTZ`,
    `duration_ms BIGINT`, token columns `BIGINT`, `id VARCHAR PRIMARY KEY`. Keep the
    `tool_calls` child table.
  - **Timezone semantics (decided):** today rows store RFC3339 text *with offset*
    (`store.go:520`) and day-bucketing is `substr(started_at,1,10)` — "day" means the
    day in the stamped offset. Use `TIMESTAMPTZ` (preserves the instant) and set the
    connection `TimeZone` to the local zone (ICU is bundled) so
    `date_trunc('day', …)` keeps today's local-day semantics for `ef usage today`.
    Verify `localTimestamp` (`main.go:571`) after the type switch.
  - **Delete the materialized-aggregate machinery**: drop tables `usage_daily_model` &
    `usage_model_histogram`, `RefreshUsageAggregates`, `insertDailyAggregateSQL`,
    `insertModelHistogramSQL`, and the `validateSchema` references to them. Make
    `usage_by_day_model` a **live view** using `date_trunc('day', started_at)`.
  - Keep `ProviderSQL`/`ClientSQL` CASE expressions (portable, still the single source of
    truth for classification).
  - **`InsertBatch` via multi-row `INSERT … ON CONFLICT (id) DO NOTHING`** (prepared,
    batched), *not* the Appender. Rationale: the Appender cannot express `ON CONFLICT`
    (a duplicate id becomes a hard batch failure) and bulk-appending into an
    ART-indexed (PK) table is a known DuckDB perf caveat — while our batches are only
    50 rows / 250 ms, so the Appender's bulk advantage is marginal. Idempotent inserts
    also cover queue re-delivery and (Phase 3) forwarder retries. Pass `time.Time`
    directly instead of formatting to RFC3339 text.
  - Remove `SetMaxOpenConns(1)` WAL/pragma logic (SQLite-specific); configure DuckDB
    threads/memory as needed.
- **Delete `internal/jobs/jobs.go`** (`AggregateScheduler`) and its wiring in
  `cmd/ef/main.go:287-289` — no longer needed with live aggregation.
- **Daemon read path (`cmd/ef/main.go` `runServe`)**: after opening the store, start
  `quack_serve` on a local address (new `EF_QUACK_ADDR`, default `127.0.0.1:9494`) so the
  CLI can query the live DB. `/ui` keeps querying the in-process DuckDB directly via
  `report.New(st)` (same process — no Quack needed).
- **`internal/report/report.go` — DuckDB dialect + Quack-aware open**:
  - Live aggregation queries (replace `substr(...)` with `date_trunc`; verify param
    placeholders — duckdb-go accepts `?`). `freshInput` logic in `cmd/ef/main.go` is pure
    Go and unaffected.
  - `report.Open(target)` connects as a **Quack client** to a running daemon
    (`quack://…`); **fall back to a read-only DuckDB file open** when no daemon is running
    (RO open is allowed only when there is no writer). Preserves today's
    "read an existing ledger, fail loudly if none" semantics (`store.OpenExisting`).
- **`internal/config/config.go`**: default DB path → `…/excursion-funnel/usage.duckdb`
  (`defaultDBPath`, :172); add `QuackAddr` field + `EF_QUACK_ADDR`/`-quack-addr`. Rename
  the `-db`/`EF_DB` help text away from "sqlite".
- **`ef migrate` (new command, `cmd/ef/main.go`)**: one-time import of an existing
  `usage.sqlite` using DuckDB's sqlite extension —
  `INSTALL sqlite; LOAD sqlite; ATTACH 'usage.sqlite' AS old (TYPE sqlite);
  INSERT INTO requests SELECT … FROM old.requests; …`. The SELECT must **explicitly
  cast** the RFC3339 text timestamps to `TIMESTAMPTZ` (`started_at::TIMESTAMPTZ`).
  Documented, optional. Requires network for the sqlite extension autoinstall
  (see extension strategy).
- **Docs/build**: README build section must state `CGO_ENABLED=1` + C++ toolchain; note
  Windows needs a mingw-w64 gcc (the user's primary env is win32 — call this out).

**Runnable at end of Phase 1:** single-user DuckDB ledger, live dashboard (no refresh
lag), CLI via local Quack. Phase 1 already bets on Quack (the CLI read path), so the
`quack_serve`/`ATTACH 'quack://…'` round trip gets smoke-tested here, not in Phase 2.

## Phase 2 — Central hub (`ef hub`) with Quack multi-writer

Goal: a shared server many proxies can write to, with a team dashboard.

- **`ef hub` (new command)**: opens the DuckDB store (reuse `internal/store`), runs
  `quack_serve` on a **network** address with an **auth token**, serves the team `/ui`
  (reuse `internal/ui` + `report.New`), and runs retention
  (`DeleteRequestsStartedBefore`, already present).
- **Security model (understand before exposing on a network):** Quack binds
  localhost-only by default — a network hub needs explicit
  `allow_other_hostname => true`. The token grants the **full SQL surface** of the
  instance (any client can `DROP TABLE`); the default authorization callback permits
  every query. Transport is plain HTTP, so the token crosses the network in cleartext
  unless fronted by a TLS reverse proxy (the client supports `EXTRA_HTTP_HEADERS` for
  proxy auth). Acceptable on a trusted team network — documented as such; front with
  TLS + consider the authorization hook if that ever changes.
- **Identity columns for multi-user**: add `source` (machine/user id) — and optionally
  `host` — to the `requests` schema and `queue.UsageEvent`. The proxy stamps it from
  config; the hub stores and groups by it so the dashboard can break usage down per
  developer. Extend `ProviderSQL`/`ClientSQL` grouping and `report.SummaryOptions` /
  group-by options (`model|provider|day|source`).
- **Config**: `EF_HUB_ADDR`, `EF_HUB_TOKEN`, `EF_SOURCE` (`config.go`).

## Phase 3 — Distributed local proxy: SQLite outbox + Quack forwarder

Goal: `ef serve` in distributed mode spools durably and pushes to the hub.

- **`internal/store/outbox.go` (new, SQLite via `modernc.org/sqlite`)**: a `queue.Writer`
  that appends events to a tiny local SQLite outbox table (durable spool). This is where
  modernc returns — scoped to buffering only, no analytics.
- **`internal/forward/forward.go` (new)**: a background forwarder (mirrors the
  `jobs`/scheduler shape we deleted) that reads un-forwarded outbox rows, connects as a
  **DuckDB Quack client** to `quack://<EF_HUB_ADDR>` (with `EF_HUB_TOKEN`), `INSERT`s into
  the hub's `requests`/`tool_calls`, then marks rows forwarded / deletes them. Retries with
  backoff so hub downtime never loses data. **Hub inserts must be idempotent**
  (`INSERT … ON CONFLICT (id) DO NOTHING` — already the store's insert shape from
  Phase 1): a crash between the hub INSERT and marking outbox rows forwarded replays
  events on restart, which must not duplicate rows.
- **Mode selection in `runServe`**: when `EF_HUB_ADDR` is set → distributed mode
  (outbox `Writer` + forwarder, no local DuckDB store; CLI/`/ui` target the hub). Otherwise
  → standalone DuckDB (Phase 1). Single branch in `cmd/ef/main.go`.
- **Known limitation (accepted):** in distributed mode there is no local analytical
  store — when the hub is unreachable, `ef usage`/`/ui` are unavailable and data sits
  in the (analytically unreadable) spool until the hub returns. Document this
  trade-off.

---

## Critical files
- `internal/store/store.go` — DuckDB rewrite, Appender, drop aggregate layer (Phase 1)
- `internal/store/outbox.go` *(new)* — SQLite outbox writer (Phase 3)
- `internal/forward/forward.go` *(new)* — Quack forwarder (Phase 3)
- `internal/report/report.go` — DuckDB dialect, live aggregation, Quack-client open (Phase 1)
- `internal/jobs/jobs.go` — **delete** (Phase 1)
- `internal/config/config.go` — DuckDB path, `QuackAddr`, `HubAddr`, `HubToken`, `Source`
- `cmd/ef/main.go` — `hub` + `migrate` commands, serve-mode branch, Quack read path,
  remove aggregate wiring
- `go.mod` / `go.sum` — add `duckdb/duckdb-go` (pin `v2.10505.0`, DuckDB 1.5.5), keep modernc for outbox
- `README.md` — cgo build notes, hub setup, migration, per-source dashboard

## Risks / call-outs
- **cgo on Windows** (user's primary OS): duckdb-go ships prebuilt static libs, so a
  mingw-w64 gcc is needed to *link* (not to build DuckDB); CI must set `CGO_ENABLED=1`.
  Cross-compilation gets harder.
- **Quack is experimental until DuckDB 2.0 (~Sept 2026)**: core extension since v1.5.3,
  but the protocol, function names, and implementation are all documented as subject to
  change. Pin exact duckdb-go versions and expect to absorb breaking changes on
  upgrade; the round trip is smoke-tested in Phase 1 (CLI read path), not deferred to
  Phase 2.
- **Extension autoinstall needs network**: quack and sqlite extensions are downloaded
  from the DuckDB extension repo on first use — first run fails offline. Boot queries
  (`INSTALL`/`LOAD` via `NewConnector`) make the failure early and explicit.
- **Tests need porting to DuckDB + cgo**: `store_test.go`, `report_test.go`,
  `store_bench_test.go` assume SQLite; update fixtures and enable cgo in test CI.
- **Timestamp handling**: `TIMESTAMPTZ` columns change how `report`/`main.go`
  (`localTimestamp`, :571) read values — verify formatting after the type switch, and
  verify day-bucketing matches today's local-day semantics (see Phase 1 decision).

## Verification
- **Phase 1**: `go build ./...` with `CGO_ENABLED=1`; run `ef serve`, drive a few
  proxied requests, confirm rows land in `usage.duckdb`; `ef usage today`, `ef tools today`,
  `ef inspect <id>` return correct data via the local Quack path; stop the daemon and
  confirm CLI falls back to read-only file open; open `/ui/` and confirm live totals with
  **no** aggregate-refresh lag. Run `ef migrate` against a copied legacy `usage.sqlite` and
  diff counts. Port and run `go test ./internal/store/... ./internal/report/...`.
- **Phase 2**: start `ef hub`; from a second machine/process run a DuckDB Quack client with
  the token and `INSERT` a row; confirm the hub `/ui` and `ef usage --group-by source` show
  it; verify auth rejects a bad token.
- **Phase 3**: run `ef serve` with `EF_HUB_ADDR`/`EF_HUB_TOKEN`/`EF_SOURCE` set; drive
  requests; confirm rows appear on the hub. **Stop the hub**, drive more requests, confirm
  they accumulate in the local SQLite outbox; **restart the hub** and confirm the forwarder
  drains the backlog with no loss.
