//go:build linux

package termio

import "golang.org/x/sys/unix"

const (
	ioctlReadTermios  = unix.TCGETS
	ioctlWriteTermios = unix.TCSETS
)
