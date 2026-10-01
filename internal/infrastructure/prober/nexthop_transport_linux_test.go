//go:build linux

package prober

import (
	"encoding/binary"
	"testing"
)

// TestHtonsLaysOutNetworkOrderBytes checks htons by the bytes it puts in memory, which
// are the same on every host: the most significant byte first.
func TestHtonsLaysOutNetworkOrderBytes(t *testing.T) {
	var b [2]byte

	binary.NativeEndian.PutUint16(b[:], htons(0x0800))

	if want := [2]byte{0x08, 0x00}; b != want {
		t.Fatalf("htons(0x0800) in memory = % x, want % x", b, want)
	}

	binary.NativeEndian.PutUint16(b[:], htons(0x86DD))

	if want := [2]byte{0x86, 0xDD}; b != want {
		t.Fatalf("htons(0x86DD) in memory = % x, want % x", b, want)
	}
}
