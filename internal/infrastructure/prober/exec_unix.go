//go:build unix

package prober

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// A dedicated process group owns relay helpers, including ssh ProxyCommand children.
// Kill it on cancellation and after Wait: closing inherited pipes alone leaves helpers
// alive, and even a successful relay may have left descendants behind.
func runProbeCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		err := killProbeGroup(cmd.Process.Pid)
		if err != nil {
			return err
		}

		return os.ErrProcessDone
	}

	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("start probe command: %w", err)
	}

	err = waitProbeCommand(cmd)

	return errors.Join(err, killProbeGroup(cmd.Process.Pid))
}

func killProbeGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("kill probe process group %d: %w", pid, err)
	}

	return nil
}
