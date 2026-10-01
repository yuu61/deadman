package logfile

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

// One mutex per directory serializes cooperating stores without retaining target-file
// handles. Directory IDs also match path aliases.
type logLock struct{ mutex windows.Handle }

func newLogLock(root *os.Root) (*logLock, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}

	var info windows.ByHandleFileInformation

	infoErr := windows.GetFileInformationByHandle(windows.Handle(directory.Fd()), &info)

	err = errors.Join(infoErr, directory.Close())
	if err != nil {
		return nil, fmt.Errorf("identify log directory: %w", err)
	}

	name, err := windows.UTF16PtrFromString(fmt.Sprintf("Global\\deadman-log-%x-%x-%x",
		info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow))
	if err != nil {
		return nil, fmt.Errorf("name log mutex: %w", err)
	}

	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, fmt.Errorf("create log mutex: %w", err)
	}

	return &logLock{mutex: mutex}, nil
}

func (lock *logLock) acquire(_ *os.File) (func() error, error) {
	// Win32 mutex ownership belongs to the OS thread, not the goroutine.
	runtime.LockOSThread()

	status, err := windows.WaitForSingleObject(lock.mutex, windows.INFINITE)
	if err != nil {
		runtime.UnlockOSThread()

		return nil, fmt.Errorf("wait for log mutex: %w", err)
	}

	if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
		runtime.UnlockOSThread()

		return nil, fmt.Errorf("unexpected log mutex wait status %d", status)
	}

	return func() error {
		unlockErr := windows.ReleaseMutex(lock.mutex)

		runtime.UnlockOSThread()

		if unlockErr != nil {
			return fmt.Errorf("release log mutex: %w", unlockErr)
		}

		return nil
	}, nil
}

func (lock *logLock) close() error {
	err := windows.CloseHandle(lock.mutex)
	if err != nil {
		return fmt.Errorf("close log mutex: %w", err)
	}

	return nil
}
