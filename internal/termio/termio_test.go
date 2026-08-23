package termio_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dlnilsson/excursion-funnel/internal/termio"
)

func TestIsTerminalRejectsStreamsWithoutDescriptor(t *testing.T) {
	if termio.IsTerminal(&bytes.Buffer{}) {
		t.Fatal("buffer reported as a terminal")
	}
	if termio.IsTerminal(strings.NewReader("")) {
		t.Fatal("string reader reported as a terminal")
	}
	if termio.IsTerminal(nil) {
		t.Fatal("nil reported as a terminal")
	}
}

type fakeDescriptor struct{ fd uintptr }

func (f fakeDescriptor) Fd() uintptr { return f.fd }

func TestIsTerminalRejectsNonTerminalDescriptor(t *testing.T) {
	// An implausible descriptor is not an open terminal, so this exercises the
	// descriptor branch without depending on the test process having a tty.
	if termio.IsTerminal(fakeDescriptor{fd: ^uintptr(0)}) {
		t.Fatal("invalid descriptor reported as a terminal")
	}
}
