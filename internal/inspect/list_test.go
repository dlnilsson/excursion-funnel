package inspect

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/dlnilsson/excursion-funnel/internal/report"
)

func TestShouldUseRequestList(t *testing.T) {
	tests := []struct {
		name           string
		stdinTerminal  bool
		stdoutTerminal bool
		want           bool
	}{
		{name: "interactive", stdinTerminal: true, stdoutTerminal: true, want: true},
		{name: "redirected input", stdoutTerminal: true},
		{name: "redirected output", stdinTerminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseRequestList(test.stdinTerminal, test.stdoutTerminal); got != test.want {
				t.Fatalf("shouldUseRequestList() = %t, want %t", got, test.want)
			}
		})
	}
	if isTerminalReader(strings.NewReader("")) || isTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("buffered streams detected as terminals")
	}
}

func TestRequestListItemEmphasizesModelAndMetadata(t *testing.T) {
	row := report.RecentRequestRow{
		ID:         "req-1234567890abcdef",
		ResponseID: "resp-full-value",
		StartedAt:  time.Date(2026, time.August, 20, 12, 34, 56, 0, time.Local),
		Provider:   "openai",
		Client:     "Codex CLI",
		Model:      "gpt-5.6-sol",
		HTTPStatus: sql.NullInt64{Int64: 200, Valid: true},
		Method:     "POST",
		Path:       "/v1/responses",
	}
	model := newRequestListModel([]report.RecentRequestRow{row})
	item, ok := model.list.Items()[0].(requestItem)
	if !ok {
		t.Fatalf("item type = %T, want requestItem", model.list.Items()[0])
	}
	if item.Title() != "gpt-5.6-sol · Codex CLI" {
		t.Fatalf("title = %q", item.Title())
	}
	for _, want := range []string{"openai", "status=200", "POST /v1/responses", "id=req-12345678…"} {
		if !strings.Contains(item.Description(), want) {
			t.Fatalf("description missing %q: %s", want, item.Description())
		}
	}
	for _, want := range []string{row.ID, row.ResponseID, row.Model, row.Client, row.Path} {
		if !strings.Contains(item.FilterValue(), want) {
			t.Fatalf("filter value missing %q: %s", want, item.FilterValue())
		}
	}
}

func TestRequestListEnterSelectsRequestAndQuits(t *testing.T) {
	model := newRequestListModel([]report.RecentRequestRow{{ID: "req-1"}})
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(requestListModel)
	if got.selectedID != "req-1" {
		t.Fatalf("selected ID = %q, want req-1", got.selectedID)
	}
	if cmd == nil {
		t.Fatal("Enter did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Enter command message = %T, want tea.QuitMsg", cmd())
	}
}

func TestRequestListQuitDoesNotSelect(t *testing.T) {
	model := newRequestListModel([]report.RecentRequestRow{{ID: "req-1"}})
	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if updated.(requestListModel).selectedID != "" {
		t.Fatalf("quit selected %q", updated.(requestListModel).selectedID)
	}
	if cmd == nil {
		t.Fatal("q did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q command message = %T, want tea.QuitMsg", cmd())
	}
}

func TestRequestListEnterAppliesActiveFilterWithoutSelecting(t *testing.T) {
	model := newRequestListModel([]report.RecentRequestRow{{ID: "req-1", Model: "gpt"}})
	model.list.SetFilterText("gpt")
	model.list.SetFilterState(list.Filtering)

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(requestListModel)
	if got.selectedID != "" {
		t.Fatalf("filter confirmation selected %q", got.selectedID)
	}
	if got.list.FilterState() == list.Filtering {
		t.Fatal("Enter did not apply the active filter")
	}
}

func TestRequestListResizesAndUsesAlternateScreen(t *testing.T) {
	model := newRequestListModel([]report.RecentRequestRow{{ID: "req-1"}})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	resized := updated.(requestListModel)
	if resized.list.Width() != 100 || resized.list.Height() != 40 {
		t.Fatalf("list size = %dx%d, want 100x40", resized.list.Width(), resized.list.Height())
	}
	if !resized.View().AltScreen {
		t.Fatal("request list does not request the alternate screen")
	}
}
