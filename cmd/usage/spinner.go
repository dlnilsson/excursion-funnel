package usage

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

const loadingUsageText = "Loading usage…"

type descriptorWriter interface {
	Fd() uintptr
}

type usageFinishedMsg struct {
	output []byte
	err    error
}

type loadingModel struct {
	spinner   spinner.Model
	statusOut io.Writer
	task      tea.Cmd
	output    []byte
	err       error
	done      bool
}

func newLoadingModel(statusOut io.Writer, task tea.Cmd) loadingModel {
	return loadingModel{
		spinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Magenta)),
		),
		statusOut: statusOut,
		task:      task,
	}
}

func (m loadingModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.task)
}

func (m loadingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case usageFinishedMsg:
		m.output = msg.output
		m.err = msg.err
		m.done = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if err := renderLoadingLine(m.statusOut, m.View().Content); err != nil {
			m.err = err
			m.done = true
			return m, tea.Quit
		}
		return m, cmd
	default:
		return m, nil
	}
}

func (m loadingModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	return tea.NewView(m.spinner.View() + loadingUsageText)
}

func shouldAnimateUsage(json, stdoutTerminal, stderrTerminal bool) bool {
	return !json && stdoutTerminal && stderrTerminal
}

func isTerminalWriter(writer io.Writer) bool {
	descriptor, ok := writer.(descriptorWriter)
	return ok && term.IsTerminal(descriptor.Fd())
}

func renderLoadingLine(out io.Writer, view string) error {
	_, err := io.WriteString(out, "\r"+ansi.EraseEntireLine+view)
	return err
}

func clearLoadingLine(out io.Writer) error {
	_, err := io.WriteString(out, "\r"+ansi.EraseEntireLine)
	return err
}

func runWithSpinner(
	ctx context.Context,
	out io.Writer,
	statusOut io.Writer,
	run func(io.Writer) error,
) error {
	task := func() tea.Msg {
		var buffered bytes.Buffer
		err := run(&buffered)
		return usageFinishedMsg{output: buffered.Bytes(), err: err}
	}
	model := newLoadingModel(statusOut, task)
	if err := renderLoadingLine(statusOut, model.View().Content); err != nil {
		return err
	}
	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(statusOut),
		tea.WithoutSignalHandler(),
		// Bubble Tea's renderer queries terminal modes 2026 and 2027. With
		// input disabled, Windows Terminal's replies would be left for the
		// shell to read after ef exits, so render this one-line view ourselves.
		tea.WithoutRenderer(),
	)
	finalModel, err := program.Run()
	clearErr := clearLoadingLine(statusOut)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("run usage loading animation: %w", err)
	}
	model, ok := finalModel.(loadingModel)
	if !ok {
		return fmt.Errorf("run usage loading animation: unexpected model %T", finalModel)
	}
	if model.err != nil {
		return model.err
	}
	if clearErr != nil {
		return clearErr
	}
	_, err = io.Copy(out, bytes.NewReader(model.output))
	return err
}
