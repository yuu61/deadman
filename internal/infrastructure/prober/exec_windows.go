package prober

import (
	"errors"
	"fmt"
	"math"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Start suspended so the process cannot create helpers before joining its job. Closing
// the job kills every member, including descendants holding inherited output pipes.
func runProbeCommand(cmd *exec.Cmd) error {
	job, err := newProbeJob()
	if err != nil {
		return err
	}

	err = runInProbeJob(cmd, job)

	closeErr := windows.CloseHandle(job)
	if closeErr != nil {
		closeErr = fmt.Errorf("close probe job: %w", closeErr)
	}

	return errors.Join(err, closeErr)
}

func runInProbeJob(cmd *exec.Cmd, job windows.Handle) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	cmd.Cancel = func() error {
		return errors.Join(windows.TerminateJobObject(job, 1), cmd.Process.Kill())
	}

	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("start probe command: %w", err)
	}

	err = attachProbeJob(job, cmd.Process.Pid)
	if err != nil {
		return errors.Join(err, cmd.Process.Kill(), cmd.Wait())
	}

	return waitProbeCommand(cmd)
}

func newProbeJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create probe job: %w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return 0, errors.Join(err, windows.CloseHandle(job))
	}

	return job, nil
}

func attachProbeJob(job windows.Handle, pid int) error {
	if pid < 0 || int64(pid) > math.MaxUint32 {
		return fmt.Errorf("invalid probe pid %d", pid)
	}

	processID := uint32(pid)

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		processID,
	)
	if err != nil {
		return fmt.Errorf("open probe process: %w", err)
	}

	defer func() { discard(windows.CloseHandle(process)) }()

	err = windows.AssignProcessToJobObject(job, process)
	if err != nil {
		return fmt.Errorf("assign probe job: %w", err)
	}

	return resumeProbeThread(processID)
}

// exec does not retain CreateProcess's initial thread handle. A suspended process has
// exactly one thread; find and resume it only after job assignment succeeds.
func resumeProbeThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("enumerate probe threads: %w", err)
	}
	defer func() { discard(windows.CloseHandle(snapshot)) }()

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}

		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return fmt.Errorf("open suspended probe thread: %w", openErr)
		}

		_, resumeErr := windows.ResumeThread(thread)

		return errors.Join(resumeErr, windows.CloseHandle(thread))
	}

	return fmt.Errorf("find suspended probe thread: %w", err)
}
