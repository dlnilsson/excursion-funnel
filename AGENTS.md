# AGENTS.md

Guidance for AI coding agents working in this repository.

## Project overview

Excursion Funnel (`ef`) is a local usage-telemetry proxy for Claude Code and
Codex, written in Go. AI tools point at it instead of the Anthropic/OpenAI
APIs; it forwards every request upstream, records usage in DuckDB, and serves
a web dashboard. It runs standalone (`ef serve`) or in team mode, where each
client forwards records to a shared hub (`ef hub`) authenticated with Ed25519
SSH keys.

Module path: `github.com/dlnilsson/excursion-funnel`. Single binary: `ef`
(entry point `cmd/ef/main.go`).

## Build and test

CGO is required (the DuckDB driver is a C library):

```sh
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go test ./...
go vet ./...
staticcheck ./...   # keep the tree staticcheck-clean
go fix ./...
go fmt ./...
```

- **Windows:** needs the MSYS2 UCRT64 GCC toolchain on `PATH`
  (`C:\msys64\ucrt64\bin`). GCC 16 breaks the DuckDB driver — use GCC 15.
- First run of `ef serve`/`ef hub` downloads a DuckDB extension, so it needs
  network access once; tests that open a store may too.
- Cross-compile a Linux binary from Windows: `.\scripts\build-linux-amd64.ps1
  -Distro Ubuntu` (uses WSL, outputs `dist\ef-linux-amd64`).
- Dashboard vendor assets (Chart.js, highlight.js) are committed under
  `internal/ui/assets/vendor/` and refreshed with `node scripts/vendor-ui.mjs`
  — never hand-edit them, and don't fetch UI assets at runtime (offline builds
  must work).

There is no Makefile and no CI config; run the commands above yourself before
declaring work done.

## Repository layout

```
cmd/ef/            main() — wires root command into execute
cmd/root/          assembles the Cobra command tree
cmd/execute/       Fang-wrapped execution, signal handling
cmd/<name>/        one package per subcommand: serve, hub, usage, tools,
                   requests, inspect, version
cmd/configflags/   shared flag/env plumbing for daemon config
cmd/reportflags/   shared flag/env plumbing for reporting commands
internal/proxy/    the HTTP proxy that intercepts Claude Code / Codex traffic
internal/sse/      SSE stream parser for streaming responses
internal/anthropic/, internal/openai/  provider-specific usage extraction
internal/store/    DuckDB usage ledger (schema, queries, Quack server)
internal/queue/    async write queue between proxy and store
internal/forward/, internal/store/outbox.go  team-mode forwarding + SQLite outbox
internal/hub/, internal/hubauth/  hub server and SSH-key authentication
internal/serve/, internal/daemon/  standalone daemon assembly, retention
internal/usage/, internal/tools/, internal/requests/, internal/inspect/
                   reporting logic + terminal output (lipgloss) per command
internal/report/, internal/reporting/  shared reporting helpers
internal/ui/       embedded web dashboard (assets/index.html + vendor JS)
internal/config/   configuration model
internal/safelog/  logging that redacts secrets
internal/version/  build/version info
```

Conventions: each CLI subcommand is a thin `cmd/<name>` package delegating to
a matching `internal/<name>` package; reporting commands query a running
daemon/hub over Quack and fall back to opening the DuckDB file read-only.

## Domain notes

- Upstream providers report **tokens only, never cost**. The app currently
  tracks tokens; any cost-estimation feature must compute dollar amounts from
  a locally maintained per-model price table — there is no cost field to read
  from the API responses.
- The proxy must never break the tools flowing through it: on any recording
  failure, pass the upstream response through untouched. Telemetry is
  best-effort; proxying is not.
- Never log request/response bodies or credentials — use `internal/safelog`.
  API keys, hub tokens, and private keys must not appear in logs or errors.
- The hub token grants full database access and is internal to the hub; it is
  never configured on clients. Client auth is Ed25519 SSH keys only.
- Working directory and git branch come from client context headers
  (`X-EF-Cwd`, `X-EF-Git-Branch`) and may be unknown — reporting code must
  handle their absence.

## Code style

Standard `gofmt` Go plus these repo preferences:

- Start every package with a `// Package <name> ...` doc comment.
- Group multiple local variable declarations in a single `var (...)` block
  instead of repeating `:=`.
- `errors.New` for static messages; `fmt.Errorf` only when interpolating
  (wrap causes with `%w`).
- In tests, get contexts from `t.Context()`, never `context.Background()` or
  `context.TODO()`.
- Pre-size slices with `make([]T, 0, n)` when the count is known or bounded.
- Build strings with `strings.Builder` (call `Grow` when the size is
  computable) or `strings.Join`; never `+=` in loops. Use
  `fmt.Fprintf(w, ...)` instead of `w.Write([]byte(fmt.Sprintf(...)))`.
- Prefer generics over `interface{}`/`any` parameters; keep APIs concrete.
- Avoid allocating closures or large temporary buffers in hot paths (the
  proxy and SSE parser are hot paths); reuse via `sync.Pool` where safe.
- Tests live next to the code (`*_test.go`); benchmarks exist for the SSE
  parser and store — keep them passing (`go test -bench . ./internal/sse
  ./internal/store`).

## Git

- Main branch: `main`. Do not commit generated artifacts (`dist/`,
  `usage.duckdb`, `outbox.sqlite`).
- Commit messages are short imperative summaries (see `git log`).
