package loading

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

func TestShouldAnimate(t *testing.T) {
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
			got := ShouldAnimate(test.json, test.stdoutTerminal, test.stderrTerminal)
			if got != test.want {
				t.Fatalf("ShouldAnimate() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestIsTerminalWriterRejectsBufferedOutput(t *testing.T) {
	if IsTerminalWriter(&bytes.Buffer{}) {
		t.Fatal("buffered output detected as a terminal")
	}
}

func TestModelAnimatesAndClearsOnCompletion(t *testing.T) {
	loadingModel := newModel[string](io.Discard, "Loading report…", func() tea.Msg { return finishedMsg[string]{} })
	initialFrame := loadingModel.spinner.View()
	if view := loadingModel.View().Content; !strings.Contains(view, "Loading report…") {
		t.Fatalf("loading view = %q, want loading text", view)
	}

	updated, next := loadingModel.Update(loadingModel.spinner.Tick())
	loadingModel = updated.(model[string])
	if next == nil {
		t.Fatal("spinner tick did not schedule the next frame")
	}
	if frame := loadingModel.spinner.View(); frame == initialFrame {
		t.Fatalf("spinner frame did not advance from %q", initialFrame)
	}

	updated, quit := loadingModel.Update(finishedMsg[string]{value: "report"})
	loadingModel = updated.(model[string])
	if quit == nil {
		t.Fatal("completion did not request program exit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("completion command returned %T, want tea.QuitMsg", quit())
	}
	if view := loadingModel.View().Content; view != "" {
		t.Fatalf("completed view = %q, want blank view", view)
	}
	if loadingModel.value != "report" {
		t.Fatalf("completed value = %q, want report", loadingModel.value)
	}
}

func TestRunReturnsTaskResultAndClearsAnimation(t *testing.T) {
	var statusOut bytes.Buffer
	result, err := Run(t.Context(), &statusOut, "Loading report…", func() (string, error) {
		return "report", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "report" {
		t.Fatalf("result = %q, want report", result)
	}
	if !strings.Contains(statusOut.String(), "Loading report…") {
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

func TestRunReturnsTaskError(t *testing.T) {
	var statusOut bytes.Buffer
	errTask := errors.New("query failed")
	result, err := Run(t.Context(), &statusOut, "Loading report…", func() (string, error) {
		return "partial report", errTask
	})
	if !errors.Is(err, errTask) {
		t.Fatalf("Run() error = %v, want %v", err, errTask)
	}
	if result != "" {
		t.Fatalf("failed task result = %q, want zero value", result)
	}
}

func TestRunPropagatesStatusOutputError(t *testing.T) {
	errWrite := errors.New("write failed")
	_, err := Run(t.Context(), errorWriter{err: errWrite}, "Loading report…", func() (string, error) {
		return "report", nil
	})
	if !errors.Is(err, errWrite) {
		t.Fatalf("Run() error = %v, want %v", err, errWrite)
	}
}

func TestRunReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Run(ctx, io.Discard, "Loading report…", func() (string, error) { return "", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}
