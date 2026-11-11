//go:build !linux

package prober

import (
	"net"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// newLinkTransport fails because next-hop forcing is a Linux-only feature: the AF_PACKET
// L2 injection it relies on has no portable equivalent. A forced target is therefore not
// built here, and its build error tells the operator why.
func newLinkTransport() (linkTransport, error) { return nil, errNoTransport }

// listenReplies fails for the same reason: a forced target is never built here, and the
// kernel-timestamped reply socket it reads on is Linux's.
func listenReplies(probe.Family, net.IP, int) (replyWaiter, error) { return nil, errNoTransport }
