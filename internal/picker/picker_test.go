package picker

import (
	"strconv"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

type row struct {
	id   string
	name string
}

func testOptions(onSelect func(row) tea.Cmd) Options[row] {
	return Options[row]{
		Title:           "Rows",
		ItemName:        "row",
		ItemPlural:      "rows",
		ActionHelp:      "choose",
		ShowDescription: true,
		Render: func(r row) (string, string, string) {
			return r.name, "id=" + r.id, r.id + " " + r.name
		},
		OnSelect: onSelect,
	}
}

func TestEnterSelectsRowAndQuits(t *testing.T) {
	m := newModel([]row{{id: "req-1", name: "first"}}, testOptions(nil))

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(model[row])
	if !got.hasSelected || got.selected.id != "req-1" {
		t.Fatalf("selected = %+v (hasSelected=%t), want req-1", got.selected, got.hasSelected)
	}
	if cmd == nil {
		t.Fatal("Enter did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Enter command message = %T, want tea.QuitMsg", cmd())
	}
}

func TestEnterRunsOnSelectInsteadOfQuittingDirectly(t *testing.T) {
	var chosen string
	m := newModel([]row{{id: "req-1", name: "first"}}, testOptions(func(r row) tea.Cmd {
		chosen = r.id
		return tea.Quit
	}))

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if chosen != "req-1" {
		t.Fatalf("OnSelect saw %q, want req-1", chosen)
	}
	if cmd == nil {
		t.Fatal("Enter discarded the OnSelect command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("OnSelect command message = %T, want tea.QuitMsg", cmd())
	}
}

func TestQuitDoesNotSelect(t *testing.T) {
	selected := false
	m := newModel([]row{{id: "req-1"}}, testOptions(func(row) tea.Cmd {
		selected = true
		return nil
	}))

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if selected || updated.(model[row]).hasSelected {
		t.Fatal("quit selected the highlighted row")
	}
	if cmd == nil {
		t.Fatal("q did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q command message = %T, want tea.QuitMsg", cmd())
	}
}

func TestEnterAppliesActiveFilterWithoutSelecting(t *testing.T) {
	m := newModel([]row{{id: "req-1", name: "gpt"}}, testOptions(nil))
	m.list.SetFilterText("gpt")
	m.list.SetFilterState(list.Filtering)

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := updated.(model[row])
	if got.hasSelected {
		t.Fatalf("filter confirmation selected %+v", got.selected)
	}
	if got.list.FilterState() == list.Filtering {
		t.Fatal("Enter did not apply the active filter")
	}
}

func TestResizesAndUsesAlternateScreen(t *testing.T) {
	m := newModel([]row{{id: "req-1"}}, testOptions(nil))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	resized := updated.(model[row])
	if resized.list.Width() != 100 || resized.list.Height() != 40 {
		t.Fatalf("list size = %dx%d, want 100x40", resized.list.Width(), resized.list.Height())
	}
	if !resized.View().AltScreen {
		t.Fatal("picker does not request the alternate screen")
	}
}

func TestRenderProjectsEveryRow(t *testing.T) {
	rows := make([]row, 0, 3)
	for i := range 3 {
		rows = append(rows, row{id: strconv.Itoa(i), name: "row-" + strconv.Itoa(i)})
	}
	m := newModel(rows, testOptions(nil))
	items := m.list.Items()
	if len(items) != len(rows) {
		t.Fatalf("items = %d, want %d", len(items), len(rows))
	}
	for i, listItem := range items {
		got, ok := listItem.(item[row])
		if !ok {
			t.Fatalf("item %d type = %T", i, listItem)
		}
		if got.Title() != rows[i].name || got.Description() != "id="+rows[i].id {
			t.Fatalf("item %d rendered as %q/%q", i, got.Title(), got.Description())
		}
		if got.FilterValue() != rows[i].id+" "+rows[i].name {
			t.Fatalf("item %d filter value = %q", i, got.FilterValue())
		}
	}
}
