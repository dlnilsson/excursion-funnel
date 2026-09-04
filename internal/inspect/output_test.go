package inspect

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func TestPrintRowsUsesProvidedWriter(t *testing.T) {
	var output bytes.Buffer
	printRows(&output, []report.InspectRow{{
		ID:          "req-1",
		Source:      "test",
		StartedAt:   time.Now(),
		Provider:    "openai",
		Method:      "POST",
		Path:        "/v1/responses",
		UpstreamURL: "https://example.test",
		Effort:      "xhigh",
		Input:       sql.NullInt64{Int64: 10, Valid: true},
		Cached:      sql.NullInt64{Int64: 3, Valid: true},
	}})
	if text := output.String(); !strings.Contains(text, "id: req-1") || !strings.Contains(text, "effort: xhigh") || !strings.Contains(text, "tokens: input=7") {
		t.Fatalf("unexpected output: %s", text)
	}
}
