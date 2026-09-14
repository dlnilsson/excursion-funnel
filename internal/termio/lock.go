package termio

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// LockEcho disables echo on stdin so keystrokes typed while a loading
// animation runs aren't shown or interleaved with its redraws. It returns an
// unlock func that restores the original terminal state; unlock is safe to
// call more than once and is also triggered automatically if the process
// receives an interrupt or termination signal while locked, so the shell
// doesn't lose echo when a user hits Ctrl-C mid-animation. If stdin isn't a
// terminal, LockEcho is a no-op.
func LockEcho() (unlock func()) {
	restore, err := lockEcho(os.Stdin.Fd())
	if err != nil {
		return func() {}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case s := <-sig:
			restore()
			signal.Stop(sig)
			code := 1
			if sysSig, ok := s.(syscall.Signal); ok {
				code = 128 + int(sysSig)
			}
			os.Exit(code)
		case <-done:
			signal.Stop(sig)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			restore()
		})
	}
}
