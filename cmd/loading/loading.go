// Package loading provides terminal loading animations for CLI commands.
package loading

import (
	"context"
	"fmt"
	"io"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type descriptorWriter interface {
	Fd() uintptr
}

type finishedMsg[T any] struct {
	value T
	err   error
}

type model[T any] struct {
	spinner   spinner.Model
	statusOut io.Writer
	text      string
	task      tea.Cmd
	value     T
	err       error
	done      bool
}

func newModel[T any](statusOut io.Writer, text string, task tea.Cmd) model[T] {
	return model[T]{
		spinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Magenta)),
		),
		statusOut: statusOut,
		text:      text,
		task:      task,
	}
}

func (m model[T]) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.task)
}

func (m model[T]) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case finishedMsg[T]:
		m.value = msg.value
		m.err = msg.err
		m.done = true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if err := renderLine(m.statusOut, m.View().Content); err != nil {
			m.err = err
			m.done = true
			return m, tea.Quit
		}
		return m, cmd
	default:
		return m, nil
	}
}

func (m model[T]) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	return tea.NewView(m.spinner.View() + m.text)
}

// ShouldAnimate reports whether an interactive, human-readable command should
// display a loading animation.
func ShouldAnimate(json, stdoutTerminal, stderrTerminal bool) bool {
	return !json && stdoutTerminal && stderrTerminal
}

// IsTerminalWriter reports whether writer is attached to a terminal.
func IsTerminalWriter(writer io.Writer) bool {
	descriptor, ok := writer.(descriptorWriter)
	return ok && term.IsTerminal(descriptor.Fd())
}

func renderLine(out io.Writer, view string) error {
	_, err := io.WriteString(out, "\r"+ansi.EraseEntireLine+view)
	return err
}

func clearLine(out io.Writer) error {
	_, err := io.WriteString(out, "\r"+ansi.EraseEntireLine)
	return err
}

// Run displays a loading animation while task executes and returns its result.
func Run[T any](ctx context.Context, statusOut io.Writer, text string, task func() (T, error)) (T, error) {
	var zero T
	cmd := func() tea.Msg {
		value, err := task()
		return finishedMsg[T]{value: value, err: err}
	}
	loadingModel := newModel[T](statusOut, text, cmd)
	if err := renderLine(statusOut, loadingModel.View().Content); err != nil {
		return zero, err
	}
	program := tea.NewProgram(
		loadingModel,
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
	clearErr := clearLine(statusOut)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}
		return zero, fmt.Errorf("run loading animation: %w", err)
	}
	loadingModel, ok := finalModel.(model[T])
	if !ok {
		return zero, fmt.Errorf("run loading animation: unexpected model %T", finalModel)
	}
	if loadingModel.err != nil {
		return zero, loadingModel.err
	}
	if clearErr != nil {
		return zero, clearErr
	}
	return loadingModel.value, nil
}
