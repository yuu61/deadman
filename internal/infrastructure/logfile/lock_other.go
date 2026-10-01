//go:build !unix && !windows

package logfile

import "os"

type logLock struct{}

func newLogLock(_ *os.Root) (*logLock, error) { return &logLock{}, nil }

func (*logLock) acquire(
	_ *os.File,
) (func() error, error) {
	return func() error { return nil }, nil
}
func (*logLock) close() error { return nil }
