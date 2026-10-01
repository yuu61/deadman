package prober

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSubprocessEndsDescendants(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%v", canceled), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")

			body := "exit 0"
			if canceled {
				body = "wait"
			}

			script := scriptFixture(t, fmt.Sprintf("sleep 30 &\necho $! > %q\n%s\n", pidFile, body))

			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()

			if !canceled {
				ctx = t.Context()
			}

			_, runErr := runCapped(ctx, []string{"sh", script}, os.Environ())
			if !canceled && runErr != nil {
				t.Fatal(runErr)
			}

			data, err := fs.ReadFile(os.DirFS(filepath.Dir(pidFile)), "child.pid")
			if err != nil {
				t.Fatal(err)
			}

			var pid int

			_, err = fmt.Sscan(string(data), &pid)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				err := syscall.Kill(pid, syscall.SIGKILL)
				if err != nil && !errors.Is(err, syscall.ESRCH) {
					t.Log(err)
				}
			})

			deadline := time.Now().Add(time.Second)

			for {
				stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				// Exit between opening and reading procfs can return ESRCH instead of ENOENT.
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
					return
				}

				if err != nil {
					t.Fatal(err)
				}
				// A killed orphan may await the namespace's init reaper; it holds no
				// sockets, pipes or running work while in zombie state.
				_, tail, ok := strings.Cut(string(stat), ") ")
				if ok && strings.HasPrefix(tail, "Z ") {
					return
				}

				if time.Now().After(deadline) {
					t.Fatalf("descendant %d still running: %s", pid, stat)
				}

				<-time.After(10 * time.Millisecond)
			}
		})
	}
}
