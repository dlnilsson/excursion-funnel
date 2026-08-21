package usage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestShouldAnimateUsage(t *testing.T) {
	tests := []struct {
		name           string
		json           bool
		stdoutTerminal bool
		stderrTerminal bool
		want           bool
	}{
		{name: "interactive table", stdoutTerminal: true, stderrTerminal: true, want: true},
		{name: "JSON", json: true, stdoutTerminal: true, stderrTerminal: true},
		{name: "redirected stdout", stderrTerminal: true},
		{name: "redirected stderr", stdoutTerminal: true},
		{name: "redirected streams"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := shouldAnimateUsage(test.json, test.stdoutTerminal, test.stderrTerminal)
			if got != test.want {
				t.Fatalf("shouldAnimateUsage() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestIsTerminalWriterRejectsBufferedOutput(t *testing.T) {
	if isTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("buffered output detected as a terminal")
	}
}

func TestLoadingModelAnimatesAndClearsOnCompletion(t *testing.T) {
	model := newLoadingModel(io.Discard, func() tea.Msg { return usageFinishedMsg{} })
	initialFrame := model.spinner.View()
	if view := model.View().Content; !strings.Contains(view, loadingUsageText) {
		t.Fatalf("loading view = %q, want text %q", view, loadingUsageText)
	}

	updated, next := model.Update(model.spinner.Tick())
	model = updated.(loadingModel)
	if next == nil {
		t.Fatal("spinner tick did not schedule the next frame")
	}
	if frame := model.spinner.View(); frame == initialFrame {
		t.Fatalf("spinner frame did not advance from %q", initialFrame)
	}

	wantOutput := []byte("usage report\n")
	updated, quit := model.Update(usageFinishedMsg{output: wantOutput})
	model = updated.(loadingModel)
	if quit == nil {
		t.Fatal("completion did not request program exit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("completion command returned %T, want tea.QuitMsg", quit())
	}
	if view := model.View().Content; view != "" {
		t.Fatalf("completed view = %q, want blank view", view)
	}
	if string(model.output) != string(wantOutput) {
		t.Fatalf("completed output = %q, want %q", model.output, wantOutput)
	}
}

func TestRunWithSpinnerWritesReportAfterTaskCompletes(t *testing.T) {
	var (
		out       bytes.Buffer
		statusOut bytes.Buffer
	)
	err := runWithSpinner(t.Context(), &out, &statusOut, func(writer io.Writer) error {
		_, writeErr := io.WriteString(writer, "usage report\n")
		return writeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "usage report\n" {
		t.Fatalf("output = %q, want usage report", got)
	}
	if strings.Contains(statusOut.String(), "usage report") {
		t.Fatalf("spinner stream contains report output: %q", statusOut.String())
	}
	if !strings.Contains(statusOut.String(), loadingUsageText) {
		t.Fatalf("spinner stream does not contain loading text: %q", statusOut.String())
	}
	for _, query := range []string{"\x1b[?2026$p", "\x1b[?2027$p"} {
		if strings.Contains(statusOut.String(), query) {
			t.Fatalf("spinner stream contains terminal capability query %q: %q", query, statusOut.String())
		}
	}
	if !strings.HasSuffix(statusOut.String(), "\r"+ansi.EraseEntireLine) {
		t.Fatalf("spinner stream was not cleared: %q", statusOut.String())
	}
}

func TestRunWithSpinnerReturnsTaskErrorWithoutPartialOutput(t *testing.T) {
	var (
		out       bytes.Buffer
		statusOut bytes.Buffer
	)
	errTask := errors.New("query failed")
	err := runWithSpinner(t.Context(), &out, &statusOut, func(writer io.Writer) error {
		_, _ = io.WriteString(writer, "partial report")
		return errTask
	})
	if !errors.Is(err, errTask) {
		t.Fatalf("runWithSpinner() error = %v, want %v", err, errTask)
	}
	if out.Len() != 0 {
		t.Fatalf("failed task emitted partial output %q", out.String())
	}
}

func TestRunWithSpinnerPropagatesOutputError(t *testing.T) {
	errWrite := errors.New("write failed")
	err := runWithSpinner(t.Context(), errorWriter{err: errWrite}, io.Discard, func(writer io.Writer) error {
		_, writeErr := io.WriteString(writer, "usage report\n")
		return writeErr
	})
	if !errors.Is(err, errWrite) {
		t.Fatalf("runWithSpinner() error = %v, want %v", err, errWrite)
	}
}

func TestRunWithSpinnerReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := runWithSpinner(ctx, io.Discard, io.Discard, func(io.Writer) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runWithSpinner() error = %v, want context.Canceled", err)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}
