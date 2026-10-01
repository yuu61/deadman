package prober

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsProbeJobEndsDescendants(t *testing.T) {
	const (
		stageEnv = "DEADMAN_TEST_JOB_STAGE"
		pidEnv   = "DEADMAN_TEST_JOB_PID_DIR"
	)

	stage := os.Getenv(stageEnv)
	if stage == "descendant" {
		<-time.After(30 * time.Second)

		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	argv := []string{executable, "-test.run=^TestWindowsProbeJobEndsDescendants$"}
	if stage == "relay" {
		runWindowsTestRelay(t, argv, pidEnv, stageEnv)

		return
	}

	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%v", canceled), func(t *testing.T) {
			dir := t.TempDir()
			env := append(os.Environ(), stageEnv+"=relay", pidEnv+"="+dir)

			ctx := t.Context()
			if canceled {
				deadline, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()

				ctx = deadline

				env = append(env, "DEADMAN_TEST_JOB_WAIT=1")
			}

			out, runErr := runCapped(ctx, argv, env)
			if !canceled && runErr != nil {
				t.Fatalf("relay failed: %v, %+v", runErr, out)
			}

			data, err := fs.ReadFile(os.DirFS(dir), "child.pid")
			if err != nil {
				t.Fatal(err)
			}

			pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			if err != nil {
				t.Fatal(err)
			}

			process, err := windows.OpenProcess(
				windows.SYNCHRONIZE|windows.PROCESS_TERMINATE,
				false,
				uint32(pid),
			)
			if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				discard(windows.TerminateProcess(process, 1))
				discard(windows.CloseHandle(process))
			})

			status, err := windows.WaitForSingleObject(process, 1000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatalf("descendant alive after probe: status=%d err=%v", status, err)
			}
		})
	}
}

func runWindowsTestRelay(t *testing.T, argv []string, pidEnv, stageEnv string) {
	t.Helper()
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), argv[0], argv[1:]...)

	cmd.Env = append(os.Environ(), stageEnv+"=descendant")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr

	err := cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(os.Getenv(pidEnv))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	err = root.WriteFile("child.pid", []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if os.Getenv("DEADMAN_TEST_JOB_WAIT") == "1" {
		<-time.After(30 * time.Second)
	}
}
