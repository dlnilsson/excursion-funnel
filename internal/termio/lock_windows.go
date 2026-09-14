//go:build windows

package termio

import "golang.org/x/sys/windows"

// lockEcho clears ENABLE_ECHO_INPUT on the given handle's console mode,
// leaving line buffering and Ctrl-C processing untouched. It returns a func
// that restores the original console mode.
func lockEcho(fd uintptr) (func(), error) {
	handle := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(handle, &old); err != nil {
		return nil, err
	}
	noecho := old &^ windows.ENABLE_ECHO_INPUT
	if err := windows.SetConsoleMode(handle, noecho); err != nil {
		return nil, err
	}
	return func() {
		_ = windows.SetConsoleMode(handle, old)
	}, nil
}
