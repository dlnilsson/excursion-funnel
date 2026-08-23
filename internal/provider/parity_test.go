package provider_test

import (
	"database/sql"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/provider"
)

// Each classification in this package exists twice: a Go predicate the proxy
// applies while recording, and a SQL expression the reporting queries apply
// while reading. These tests run one set of fixtures through both forms so the
// pair cannot silently drift — which is exactly what happened while the two
// halves lived in different packages.

type pathFixture struct {
	path string
	want string
}

var pathFixtures = []pathFixture{
	{path: "/v1/messages", want: provider.Anthropic},
	{path: "/v1/messages?beta=true", want: provider.Anthropic},
	{path: "/messages", want: provider.Anthropic},
	{path: "/v1/responses", want: provider.OpenAI},
	{path: "/backend-api/codex/responses", want: provider.OpenAI},
	{path: "/v1/chat/completions", want: provider.OpenAI},
	{path: "/v1/models", want: provider.Unknown},
	{path: "/healthz", want: provider.Unknown},
	{path: "", want: provider.Unknown},
}

type clientFixture struct {
	name       string
	userAgent  string
	originator string
	want       string
	// sqlDiverges marks the one case the SQL ladder cannot reproduce: the
	// Agent SDK is matched by a regexp with no LIKE equivalent, so a legacy row
	// with no client_name falls through to its raw user agent instead.
	sqlDiverges bool
	sqlWant     string
}

var clientFixtures = []clientFixture{
	{name: "codex cli", userAgent: "codex-tui/0.146.0", want: "Codex CLI"},
	{name: "zed originator", userAgent: "codex-tui/0.146.0", originator: "zed", want: "Zed"},
	{name: "codex generic", userAgent: "codex-acp/1.0", want: "Codex"},
	{name: "claude code", userAgent: "claude-cli/2.1.218", want: "Claude Code"},
	{
		name: "claude sdk", userAgent: "claude-cli/2.1.232 (external, sdk-ts, agent-sdk/0.3.232)",
		want: "ACP/sdk", sqlDiverges: true, sqlWant: "Claude Code",
	},
	{
		name: "claude sdk version", userAgent: "claude-cli/2.1.232 sdk-ts agent-sdk/1.2.3",
		want: "ACP/sdk", sqlDiverges: true, sqlWant: "Claude Code",
	},
	{name: "originator fallback", originator: "custom-editor", want: "custom-editor"},
	{name: "user-agent fallback", userAgent: "curl/8.21.0", want: "curl/8.21.0"},
	{name: "unknown", want: "Unknown"},
}

type freshInputFixture struct {
	name       string
	provider   string
	input      int64
	cached     int64
	cacheWrite int64
	want       int64
}

var freshInputFixtures = []freshInputFixture{
	{name: "openai ignores cache writes", provider: provider.OpenAI, input: 1000, cached: 400, cacheWrite: 300, want: 600},
	{name: "anthropic subtracts cache writes", provider: provider.Anthropic, input: 1000, cached: 400, cacheWrite: 300, want: 300},
	{name: "no cache activity", provider: provider.OpenAI, input: 500, want: 500},
	{name: "never negative", provider: provider.Anthropic, input: 100, cached: 200, cacheWrite: 300, want: 0},
	{name: "unknown provider keeps cache writes", provider: provider.Unknown, input: 800, cached: 100, cacheWrite: 200, want: 700},
}

func TestSQLForPathAgreesWithForPath(t *testing.T) {
	db := openDuckDB(t)
	// SQLForPath repeats its column reference, so evaluate it against a real
	// column rather than a placeholder.
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE requests (path VARCHAR)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	for _, tt := range pathFixtures {
		t.Run(tt.path, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), `DELETE FROM requests`); err != nil {
				t.Fatalf("reset fixture table: %v", err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO requests VALUES (?)`, tt.path); err != nil {
				t.Fatalf("insert fixture row: %v", err)
			}
			var got string
			if err := db.QueryRowContext(t.Context(),
				`SELECT `+provider.SQLForPath("path")+` FROM requests`).Scan(&got); err != nil {
				t.Fatalf("evaluate SQLForPath: %v", err)
			}
			if want := provider.ForPath(tt.path); got != want {
				t.Fatalf("SQLForPath(%q) = %q, but ForPath = %q", tt.path, got, want)
			}
			if got != tt.want {
				t.Fatalf("SQLForPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestClientSQLAgreesWithClientName(t *testing.T) {
	db := openDuckDB(t)
	// The SQL ladder reads the ledger columns, so exercise it against a real
	// row with client_name absent — the legacy shape the fallback branches exist
	// for. Rows written by the current proxy take the first branch instead.
	if _, err := db.ExecContext(t.Context(),
		`CREATE TABLE requests (client_name VARCHAR, user_agent VARCHAR, originator VARCHAR)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	for _, tt := range clientFixtures {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), `DELETE FROM requests`); err != nil {
				t.Fatalf("reset fixture table: %v", err)
			}
			if _, err := db.ExecContext(t.Context(),
				`INSERT INTO requests VALUES (NULL, ?, ?)`,
				nullIfEmpty(tt.userAgent), nullIfEmpty(tt.originator)); err != nil {
				t.Fatalf("insert fixture row: %v", err)
			}
			var got string
			if err := db.QueryRowContext(t.Context(),
				`SELECT `+provider.ClientSQL()+` FROM requests`).Scan(&got); err != nil {
				t.Fatalf("evaluate ClientSQL: %v", err)
			}
			want := tt.want
			if tt.sqlDiverges {
				want = tt.sqlWant
			}
			if got != want {
				t.Fatalf("ClientSQL = %q, want %q", got, want)
			}
			if !tt.sqlDiverges && got != provider.ClientName(headerFor(tt)) {
				t.Fatalf("ClientSQL = %q but ClientName = %q", got, provider.ClientName(headerFor(tt)))
			}
		})
	}
}

func TestClientSQLPrefersRecordedClientName(t *testing.T) {
	db := openDuckDB(t)
	if _, err := db.ExecContext(t.Context(),
		`CREATE TABLE requests (client_name VARCHAR, user_agent VARCHAR, originator VARCHAR);
		 INSERT INTO requests VALUES ('ACP/sdk', 'claude-cli/2.1.232 sdk-ts agent-sdk/1.2.3', NULL)`); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	var got string
	if err := db.QueryRowContext(t.Context(), `SELECT `+provider.ClientSQL()+` FROM requests`).Scan(&got); err != nil {
		t.Fatalf("evaluate ClientSQL: %v", err)
	}
	// The proxy already resolved this row, including the Agent SDK rule the SQL
	// ladder cannot express, so the stored label must win.
	if got != "ACP/sdk" {
		t.Fatalf("ClientSQL = %q, want the recorded ACP/sdk", got)
	}
}

func TestFreshInputSQLAgreesWithFreshInput(t *testing.T) {
	db := openDuckDB(t)
	if _, err := db.ExecContext(t.Context(),
		`CREATE TABLE requests (path VARCHAR, input_tokens BIGINT, cached_input_tokens BIGINT, cache_write_tokens BIGINT)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	for _, tt := range freshInputFixtures {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), `DELETE FROM requests`); err != nil {
				t.Fatalf("reset fixture table: %v", err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO requests VALUES (?, ?, ?, ?)`,
				pathFor(tt.provider), tt.input, tt.cached, tt.cacheWrite); err != nil {
				t.Fatalf("insert fixture row: %v", err)
			}
			var got int64
			if err := db.QueryRowContext(t.Context(),
				`SELECT `+provider.FreshInputSQL("path")+` FROM requests`).Scan(&got); err != nil {
				t.Fatalf("evaluate FreshInputSQL: %v", err)
			}
			want := provider.FreshInput(tt.provider, tt.input, tt.cached, tt.cacheWrite)
			if got != want {
				t.Fatalf("FreshInputSQL = %d, but FreshInput = %d", got, want)
			}
		})
	}
}

func openDuckDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open in-memory DuckDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping in-memory DuckDB: %v", err)
	}
	return db
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// pathFor returns an endpoint that ForPath classifies as the given provider, so
// the SQL expression (which derives the provider from the path column) sees the
// same input the Go helper is given directly.
func pathFor(name string) string {
	switch name {
	case provider.Anthropic:
		return "/v1/messages"
	case provider.OpenAI:
		return "/v1/responses"
	default:
		return "/v1/models"
	}
}
