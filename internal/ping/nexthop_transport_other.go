//go:build !linux

package ping

// newLinkTransport returns nil because next-hop forcing is a Linux-only feature: the
// AF_PACKET L2 injection it relies on has no portable equivalent. Returning nil makes
// the probe degrade to a failure glyph (X), matching how the other Linux-bound via
// modes (netns/vrf, tcp) fail gracefully on non-Linux when their tooling is absent.
// The startup check warns the operator that nexthop targets cannot be honored here.
func newLinkTransport() linkTransport { return nil }
