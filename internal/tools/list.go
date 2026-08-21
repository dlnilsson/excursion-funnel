package tools

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/dlnilsson/excursion-funnel/internal/report"
	"github.com/dlnilsson/excursion-funnel/internal/reporting"
)

const (
	defaultListWidth  = 80
	defaultListHeight = 24
)

type terminalDescriptor interface {
	Fd() uintptr
}

type commandItem struct {
	command     string
	description string
}

func (i commandItem) FilterValue() string { return i.command }

func (i commandItem) Title() string { return compactValue(i.command) }

func (i commandItem) Description() string { return i.description }

type commandListModel struct {
	list         list.Model
	copyKey      key.Binding
	setClipboard func(string) tea.Cmd
}

func newCommandListModel(rows []report.ToolCallRow, verbose bool) commandListModel {
	items := make([]list.Item, 0, len(rows))
	for _, row := range rows {
		description := ""
		if verbose {
			description = commandMetadata(row)
		}
		items = append(items, commandItem{command: row.Command, description: description})
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = verbose
	delegate.SetSpacing(0)
	copyKey := key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "copy"),
	)
	commandList := list.New(items, delegate, defaultListWidth, defaultListHeight)
	commandList.Title = "Commands today"
	commandList.SetStatusBarItemName("command", "commands")
	commandList.KeyMap.Quit = key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)
	commandList.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{copyKey}
	}

	return commandListModel{
		list:         commandList,
		copyKey:      copyKey,
		setClipboard: tea.SetClipboard,
	}
}

func (m commandListModel) Init() tea.Cmd { return nil }

func (m commandListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		if key.Matches(msg, m.copyKey) && m.list.FilterState() != list.Filtering {
			item, ok := m.list.SelectedItem().(commandItem)
			if ok {
				return m, tea.Sequence(m.setClipboard(item.command), tea.Quit)
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m commandListModel) View() tea.View {
	view := tea.NewView(m.list.View())
	view.AltScreen = true
	return view
}

func commandMetadata(row report.ToolCallRow) string {
	parts := []string{
		"started=" + reporting.LocalTimestamp(row.StartedAt),
		"client=" + reporting.EmptyAsDash(row.Client),
		"model=" + reporting.EmptyAsDash(row.Model),
		"tool=" + reporting.EmptyAsDash(row.Name),
		"description=" + reporting.EmptyAsDash(compactValue(row.Description)),
	}
	return strings.Join(parts, "  •  ")
}

func isTerminalReader(in io.Reader) bool {
	streamDescriptor, ok := in.(terminalDescriptor)
	return ok && term.IsTerminal(streamDescriptor.Fd())
}

func isTerminalWriter(out io.Writer) bool {
	streamDescriptor, ok := out.(terminalDescriptor)
	return ok && term.IsTerminal(streamDescriptor.Fd())
}

func shouldUseCommandList(json, stdinTerminal, stdoutTerminal bool) bool {
	return !json && stdinTerminal && stdoutTerminal
}

func runCommandList(ctx context.Context, in io.Reader, out io.Writer, rows []report.ToolCallRow, verbose bool) error {
	program := tea.NewProgram(
		newCommandListModel(rows, verbose),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	)
	if _, err := program.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("run command picker: %w", err)
	}
	return nil
}
