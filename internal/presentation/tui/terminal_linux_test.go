//go:build linux

package tui

import (
	"errors"
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// The hang-up check rests on the kernel's answer: a pseudo-terminal whose master closed
// (sshd's side of a dropped session) fails the size request with EIO, while it answers
// it as long as the master is open.
func TestHungUpPseudoTerminalFailsTheSizeRequest(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}

	mfd := int(master.Fd())

	err = unix.IoctlSetPointerInt(mfd, unix.TIOCSPTLCK, 0)
	if err != nil {
		t.Fatal(err)
	}

	n, err := unix.IoctlGetInt(mfd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}

	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()

	sfd := int(slave.Fd())

	err = ttyProbe(sfd)
	if err != nil {
		t.Fatalf("live terminal: %v", err)
	}

	_ = master.Close()

	err = ttyProbe(sfd)
	if !errors.Is(err, unix.EIO) {
		t.Fatalf("hung-up terminal: %v, want EIO", err)
	}
}
