package prober

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// echoReplyKind selects the address family for matchEcho: the IP protocol number
// passed to icmp.ParseMessage and the ICMP type a reply must carry. It is the only
// difference between the families' reply waiting.
type echoReplyKind struct {
	proto     int
	replyType icmp.Type
}

var (
	echoKindV4 = echoReplyKind{ianaProtocolICMP, ipv4.ICMPTypeEchoReply}
	echoKindV6 = echoReplyKind{ianaProtocolICMPv6, ipv6.ICMPTypeEchoReply}
)

// echoWaiter reads one family's ICMP replies to a forced probe. A raw ICMP socket sees
// every host's ICMP (fan-out), so wait keeps reading until its own reply arrives or the
// deadline passes, filtering by source, id, seq and token.
type echoWaiter struct {
	conn *icmp.PacketConn
	kind echoReplyKind
}

func (w *echoWaiter) close() error {
	err := w.conn.Close()
	if err != nil {
		return fmt.Errorf("close the ICMP socket: %w", err)
	}

	return nil
}

func (w *echoWaiter) wait(
	ctx context.Context,
	peer net.IP,
	id, seq int,
	token []byte,
	deadline time.Time,
) (bool, error) {
	buf := make([]byte, recvBufSize)

	err := w.conn.SetReadDeadline(deadline)
	if err != nil {
		return false, fmt.Errorf("set the read deadline: %w", err)
	}

	stop := interruptOnDone(ctx, w.conn.SetReadDeadline)
	defer stop()

	for {
		n, src, readErr := w.conn.ReadFrom(buf)
		if readErr != nil {
			return false, echoReadError(ctx, readErr)
		}

		if matchEcho(buf[:n], src, peer, id, seq, token, w.kind) {
			return true, nil
		}
	}
}

// echoReadError treats the probe's response deadline as loss, while preserving
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

// matchEcho reports whether b is this probe's echo reply: from peer, of kind's family,
// carrying our id, seq and echoed token. The token guards against accepting another
// listener's reply on the fan-out raw socket.
func matchEcho(
	b []byte,
	src net.Addr,
	peer net.IP,
	id, seq int,
	token []byte,
	kind echoReplyKind,
) bool {
	if !addrIP(src).Equal(peer) {
		return false
	}

	msg, err := icmp.ParseMessage(kind.proto, b)
	if err != nil || msg.Type != kind.replyType {
		return false
	}

	echo, ok := msg.Body.(*icmp.Echo)

	return ok && echo.ID == id && echo.Seq == seq && bytes.Equal(echo.Data, token)
}
