package prober

import (
	"testing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// A row takes the native echo path only when it must: the default path is the one most
// users run, and the native one is for what it cannot send.
func TestNeedsNativeEcho(t *testing.T) {
	// The largest payload whose echo, with the headers echoReceiveSize allows for, still
	// fits a BSD raw socket's space.
	const fits = bsdRawSpace - 68

	for _, tc := range []struct {
		name string
		echo probe.EchoOptions
		bsd  bool
		want bool
	}{
		{"default size", probe.EchoOptions{Size: probe.PayloadBytes(probe.DirectPayloadSize)}, true, false},
		{"default size elsewhere", probe.EchoOptions{Size: probe.PayloadBytes(probe.DirectPayloadSize)}, false, false},
		{"zero payload", probe.EchoOptions{Size: probe.PayloadBytes(0)}, true, false},
		{"largest that fits", probe.EchoOptions{Size: probe.PayloadBytes(fits)}, true, false},
		{"one byte over", probe.EchoOptions{Size: probe.PayloadBytes(fits + 1)}, true, true},
		{"jumbo", probe.EchoOptions{Size: probe.PayloadBytes(8972)}, true, true},
		{"jumbo where pro-bing is fine", probe.EchoOptions{Size: probe.PayloadBytes(8972)}, false, false},
		{"df", probe.EchoOptions{Size: probe.PayloadBytes(probe.DirectPayloadSize), DF: true}, false, true},
		{"df on a BSD", probe.EchoOptions{Size: probe.PayloadBytes(probe.DirectPayloadSize), DF: true}, true, true},
	} {
		if got := needsNativeEcho(tc.echo, tc.bsd); got != tc.want {
			t.Errorf("%s: needsNativeEcho = %v, want %v", tc.name, got, tc.want)
		}
	}
}
