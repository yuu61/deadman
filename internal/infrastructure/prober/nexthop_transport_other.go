//go:build !linux

package prober

// newLinkTransport fails because next-hop forcing is a Linux-only feature: the AF_PACKET
// L2 injection it relies on has no portable equivalent. A forced target is therefore not
// built here, and its build error tells the operator why.
func newLinkTransport() (linkTransport, error) { return nil, errNoTransport }
