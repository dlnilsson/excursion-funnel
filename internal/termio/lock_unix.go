//go:build darwin || linux

package termio

import "golang.org/x/sys/unix"

// lockEcho clears ECHO on the given fd's termios, leaving canonical mode and
// signal generation (Ctrl-C, Ctrl-Z) untouched. It returns a func that
// restores the original termios.
func lockEcho(fd uintptr) (func(), error) {
	old, err := unix.IoctlGetTermios(int(fd), ioctlReadTermios)
	if err != nil {
		return nil, err
	}
	noecho := *old
	noecho.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(fd), ioctlWriteTermios, &noecho); err != nil {
		return nil, err
	}
	return func() {
		_ = unix.IoctlSetTermios(int(fd), ioctlWriteTermios, old)
	}, nil
}
