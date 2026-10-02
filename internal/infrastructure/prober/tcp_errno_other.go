//go:build !windows

package prober

import "syscall"

const (
	errTCPRefused = syscall.ECONNREFUSED
	errTCPTimeout = syscall.ETIMEDOUT
)
