//go:build linux

package prober

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// listenReplies opens the raw ICMP socket a forced echo's reply comes back on, as the
// direct prober opens its own (icmp_linux.go): the kernel drops ICMP other than echo
// replies carrying id, and stamps each reply on arrival, so a forced RTT ends at the same
// instant a direct one does. A non-nil src binds the socket, so a source that is no longer
// assigned fails here.
func listenReplies(family probe.Family, src net.IP, id int) (replyWaiter, error) {
	fam := icmpFamilyFor(family)

	fd, err := openICMPSocket(fam, unix.SOCK_RAW)
	if err != nil {
		return nil, fmt.Errorf("listen for ICMP replies: %w", err)
	}

	err = filterICMPSocket(fd, fam.proto, id)
	if err == nil && src != nil {
		err = bindReplySource(fd, src)
	}

	if err != nil {
		_ = unix.Close(fd)

		return nil, err
	}

	// os.NewFile registers the nonblocking socket with the runtime poller and takes
	// ownership of the fd, so from here it is closed via f.Close(), never unix.Close.
	f := os.NewFile(uintptr(fd), "icmp")

	rc, err := f.SyscallConn()
	if err != nil {
		discard(f.Close())

		return nil, fmt.Errorf("listen for ICMP replies: %w", err)
	}

	return &kernelReplyWaiter{f: f, rc: rc, fam: fam}, nil
}

func bindReplySource(fd int, src net.IP) error {
	sa, err := ipSockaddr(src, 0)
	if err != nil {
		return err
	}

	err = unix.Bind(fd, sa)
	if err != nil {
		return fmt.Errorf("bind the reply socket to %s: %w", src, err)
	}

	return nil
}

// kernelReplyWaiter reads a forced echo's reply through the direct prober's receive loop
// (icmpProbe.awaitReply): the IPv4 header stripped, the peer, id, seq and token matched,
// and the RTT taken from the kernel's receive timestamp.
type kernelReplyWaiter struct {
	f   *os.File
	rc  syscall.RawConn
	fam icmpFamily
}

func (w *kernelReplyWaiter) close() error {
	err := w.f.Close()
	if err != nil {
		return fmt.Errorf("close the ICMP socket: %w", err)
	}

	return nil
}

func (w *kernelReplyWaiter) wait(
	ctx context.Context,
	echo sentEcho,
	deadline time.Time,
) (float64, bool, error) {
	err := w.f.SetReadDeadline(deadline)
	if err != nil {
		return 0, false, fmt.Errorf("set the read deadline: %w", err)
	}

	stop := interruptOnDone(ctx, w.f.SetReadDeadline)
	defer stop()

	pr := icmpProbe{fam: w.fam, privileged: true, id: echo.id, seq: echo.seq, token: echo.token}

	rtt, err := pr.awaitReply(w.rc, echo.peer, echo.at)
	if err != nil {
		return 0, false, echoReadError(ctx, err)
	}

	return rtt, true, nil
}

// echoReadError treats the probe's response deadline as loss (nil), while preserving
// caller cancellation and socket failures as unobserved results.
func echoReadError(ctx context.Context, readErr error) error {
	timedOut := isTimeout(readErr)
	if timedOut {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			// The socket timer can fire before the context timer. Wait for the
			// already-expired context to publish whether this was our deadline.
			<-ctx.Done()
		}
	}

	if ctx.Err() != nil && !errors.Is(context.Cause(ctx), errNexthopTimeout) {
		return ctx.Err()
	}

	if timedOut {
		return nil
	}

	return fmt.Errorf("read an ICMP reply: %w", readErr)
}
