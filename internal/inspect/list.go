package inspect

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

const (
	defaultListWidth  = 100
	defaultListHeight = 24
	shortIDLength     = 12
)

type terminalDescriptor interface {
	Fd() uintptr
}

type requestItem struct {
	row report.RecentRequestRow
}

func (i requestItem) FilterValue() string {
	return strings.Join([]string{
		i.row.ID,
		i.row.ResponseID,
		i.row.Provider,
		i.row.Client,
		i.row.Model,
		i.row.Method,
		i.row.Path,
		requestStatus(i.row.HTTPStatus.Valid, i.row.HTTPStatus.Int64),
	}, " ")
}

func (i requestItem) Title() string {
	return displayValue(i.row.Model) + " · " + displayClient(i.row.Client)
}

func (i requestItem) Description() string {
	return strings.Join([]string{
		reporting.LocalTimestamp(i.row.StartedAt),
		displayValue(i.row.Provider),
		"status=" + requestStatus(i.row.HTTPStatus.Valid, i.row.HTTPStatus.Int64),
		strings.TrimSpace(i.row.Method + " " + compactValue(i.row.Path)),
		"id=" + shortRequestID(i.row.ID),
	}, "  •  ")
}

type requestListModel struct {
	list       list.Model
	inspectKey key.Binding
	selectedID string
}

func newRequestListModel(rows []report.RecentRequestRow) requestListModel {
	items := make([]list.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, requestItem{row: row})
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true
	delegate.SetSpacing(0)
	inspectKey := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "inspect"),
	)
	requestList := list.New(items, delegate, defaultListWidth, defaultListHeight)
	requestList.Title = "Recent requests"
	requestList.SetStatusBarItemName("request", "requests")
	requestList.KeyMap.Quit = key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)
	requestList.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{inspectKey}
	}

	return requestListModel{list: requestList, inspectKey: inspectKey}
}

func (m requestListModel) Init() tea.Cmd { return nil }

func (m requestListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		if key.Matches(msg, m.inspectKey) && m.list.FilterState() != list.Filtering {
			item, ok := m.list.SelectedItem().(requestItem)
			if ok {
				m.selectedID = item.row.ID
				return m, tea.Quit
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m requestListModel) View() tea.View {
	view := tea.NewView(m.list.View())
	view.AltScreen = true
	return view
}

func isTerminalReader(in io.Reader) bool {
	descriptor, ok := in.(terminalDescriptor)
	return ok && term.IsTerminal(descriptor.Fd())
}

func isTerminalWriter(out io.Writer) bool {
	descriptor, ok := out.(terminalDescriptor)
	return ok && term.IsTerminal(descriptor.Fd())
}

func shouldUseRequestList(stdinTerminal, stdoutTerminal bool) bool {
	return stdinTerminal && stdoutTerminal
}

func runRequestList(ctx context.Context, in io.Reader, out io.Writer, rows []report.RecentRequestRow) (string, error) {
	program := tea.NewProgram(
		newRequestListModel(rows),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	)
	model, err := program.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("run request picker: %w", err)
	}
	finalModel, ok := model.(requestListModel)
	if !ok {
		return "", fmt.Errorf("request picker returned model %T", model)
	}
	return finalModel.selectedID, nil
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func displayClient(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "unknown") {
		return "-"
	}
	return displayValue(value)
}

func requestStatus(valid bool, status int64) string {
	if !valid {
		return "-"
	}
	return strconv.FormatInt(status, 10)
}

func shortRequestID(id string) string {
	if len(id) <= shortIDLength {
		return id
	}
	return id[:shortIDLength] + "…"
}

func compactValue(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ↩ ")
	value = strings.ReplaceAll(value, "\n", " ↩ ")
	return strings.ReplaceAll(value, "\r", " ↩ ")
}
