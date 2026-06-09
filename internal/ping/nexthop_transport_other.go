//go:build !linux

package ping

// newLinkTransport reports that next-hop forcing is unsupported on this platform
// by returning nil; the v4 path then degrades to a failure glyph (X), matching the
// "missing relay degrades gracefully" design. macOS/BSD (BPF) and Windows (Npcap)
// implementations can be added as nexthop_transport_<os>.go without touching the
// core or the address-family code.
func newLinkTransport() linkTransport { return nil }
