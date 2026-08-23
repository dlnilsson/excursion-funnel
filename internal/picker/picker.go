// Package picker renders an interactive, filterable list over an arbitrary row
// type and returns the row the user chose. The tool-command and request
// pickers are the same program with different payloads, so the Bubble Tea
// wiring lives here once and each caller supplies only how to render a row and
// what to do on selection.
package picker

import (
	"context"
	"fmt"
	"io"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

const (
	defaultWidth  = 100
	defaultHeight = 24
)

// Options configures one picker run.
type Options[T any] struct {
	// Title heads the list.
	Title string
	// ItemName and ItemPlural label the status bar ("3 commands").
	ItemName   string
	ItemPlural string
	// ActionHelp labels the enter key in the short help ("copy", "inspect").
	ActionHelp string
	// ShowDescription draws each row's second line.
	ShowDescription bool
	// Width and Height size the list before the first WindowSizeMsg arrives.
	// Zero uses the package defaults.
	Width  int
	Height int
	// Render projects a row onto its list presentation. filterValue is the text
	// the user's filter query is matched against.
	Render func(T) (title, description, filterValue string)
	// OnSelect runs when the user presses enter. Returning a command lets the
	// caller act on the row (copying it, for example) and is responsible for
	// quitting. A nil OnSelect quits and reports the row through Run's return.
	OnSelect func(T) tea.Cmd
}

type item[T any] struct {
	value                      T
	title, description, filter string
}

func (i item[T]) Title() string       { return i.title }
func (i item[T]) Description() string { return i.description }
func (i item[T]) FilterValue() string { return i.filter }

type model[T any] struct {
	list        list.Model
	actionKey   key.Binding
	onSelect    func(T) tea.Cmd
	selected    T
	hasSelected bool
}

func (m model[T]) Init() tea.Cmd { return nil }

func (m model[T]) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		// Enter belongs to the filter editor while filtering, so the action key
		// only fires outside that mode.
		if key.Matches(msg, m.actionKey) && m.list.FilterState() != list.Filtering {
			if chosen, ok := m.list.SelectedItem().(item[T]); ok {
				m.selected, m.hasSelected = chosen.value, true
				if m.onSelect != nil {
					return m, m.onSelect(chosen.value)
				}
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model[T]) View() tea.View {
	view := tea.NewView(m.list.View())
	view.AltScreen = true
	return view
}

// Run displays rows and blocks until the user selects one or quits. It reports
// the selected row and whether a selection was made.
func Run[T any](ctx context.Context, in io.Reader, out io.Writer, rows []T, opts Options[T]) (T, bool, error) {
	var zero T
	program := tea.NewProgram(
		newModel(rows, opts),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	)
	final, err := program.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, false, ctxErr
		}
		return zero, false, fmt.Errorf("run %s picker: %w", opts.ItemName, err)
	}
	finished, ok := final.(model[T])
	if !ok {
		return zero, false, fmt.Errorf("%s picker returned model %T", opts.ItemName, final)
	}
	return finished.selected, finished.hasSelected, nil
}

func newModel[T any](rows []T, opts Options[T]) model[T] {
	items := make([]list.Item, 0, len(rows))
	for _, row := range rows {
		title, description, filter := opts.Render(row)
		items = append(items, item[T]{value: row, title: title, description: description, filter: filter})
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = opts.ShowDescription
	delegate.SetSpacing(0)
	actionKey := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", opts.ActionHelp),
	)
	width, height := opts.Width, opts.Height
	if width <= 0 {
		width = defaultWidth
	}
	if height <= 0 {
		height = defaultHeight
	}
	rendered := list.New(items, delegate, width, height)
	rendered.Title = opts.Title
	rendered.SetStatusBarItemName(opts.ItemName, opts.ItemPlural)
	rendered.KeyMap.Quit = key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)
	rendered.AdditionalShortHelpKeys = func() []key.Binding { return []key.Binding{actionKey} }

	return model[T]{list: rendered, actionKey: actionKey, onSelect: opts.OnSelect}
}
