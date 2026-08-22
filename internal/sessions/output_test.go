package sessions

import (
	"bytes"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func TestPrintJSONEmptyRowsIsArray(t *testing.T) {
	var output bytes.Buffer
	if err := printJSON(&output, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "[]\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestPrintRowsEmptyRowsKeepsMessage(t *testing.T) {
	var output bytes.Buffer
	if err := printRows(&output, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "no session rows\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestPrintRowsRendersCounts(t *testing.T) {
	var output bytes.Buffer
	if err := printRows(&output, []report.SessionRow{{
		Period: "2026-08-03", Provider: "openai", Started: 2, Used: 3,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"PERIOD", "PROVIDER", "STARTED", "USED", "2026-08-03", "openai", "2", "3"} {
		if !bytes.Contains(output.Bytes(), []byte(marker)) {
			t.Fatalf("output missing %q:\n%s", marker, output.String())
		}
	}
}
