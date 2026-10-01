//go:build unix

package logfile

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type logLock struct{}

func newLogLock(_ *os.Root) (*logLock, error) { return &logLock{}, nil }

func (*logLock) acquire(file *os.File) (func() error, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
	if err != nil {
		return nil, fmt.Errorf("lock the log file: %w", err)
	}

	return func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		if unlockErr != nil {
			return fmt.Errorf("unlock the log file: %w", unlockErr)
		}

		return nil
	}, nil
}

func (*logLock) close() error { return nil }
