package prober

import "golang.org/x/sys/windows"

// Winsock errors differ from syscall's POSIX errno constants on Windows.
const (
	errTCPRefused = windows.WSAECONNREFUSED
	errTCPTimeout = windows.WSAETIMEDOUT
)
