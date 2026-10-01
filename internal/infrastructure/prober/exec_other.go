//go:build !unix && !windows

package prober

import (
	"fmt"
	"os/exec"
)

func runProbeCommand(cmd *exec.Cmd) error {
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("start probe command: %w", err)
	}
	return waitProbeCommand(cmd)
}
