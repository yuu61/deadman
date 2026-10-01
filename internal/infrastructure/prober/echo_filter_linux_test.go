//go:build linux

package prober

import (
	"bytes"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestEchoFilterAcceptsOnlyOwnReplies(t *testing.T) {
	const id = 1234
	for _, proto := range []int{ianaProtocolICMP, ianaProtocolICMPv6} {
		filter, err := echoFilter(proto, id)
		if err != nil {
			t.Fatal(err)
		}

		instructions, ok := bpf.Disassemble(filter)
		if !ok {
			t.Fatal("could not disassemble filter")
		}

		vm, err := bpf.NewVM(instructions)
		if err != nil {
			t.Fatal(err)
		}

		var replyType icmp.Type = ipv6.ICMPTypeEchoReply
		if proto == ianaProtocolICMP {
			replyType = ipv4.ICMPTypeEchoReply
		}

		for _, ihl := range []uint8{ipv4.HeaderLen, ipv4.HeaderLen + 4} {
			msg := icmp.Message{
				Type: replyType,
				Body: &icmp.Echo{ID: id, Seq: 1, Data: []byte("token")},
			}

			payload, err := msg.Marshal(nil)
			if err != nil {
				t.Fatal(err)
			}

			headerLen := 0
			if proto == ianaProtocolICMP {
				headerLen = int(ihl)
			}

			own := make([]byte, headerLen+len(payload))
			copy(own[headerLen:], payload)

			if headerLen != 0 {
				own[0] = 0x40 | ihl/ipv4IHLWordSize
			}

			foreign := bytes.Clone(own)
			foreign[headerLen+echoIDOffset] ^= 1
			request := bytes.Clone(own)

			request[headerLen] = byte(ipv6.ICMPTypeEchoRequest)
			if proto == ianaProtocolICMP {
				request[headerLen] = byte(ipv4.ICMPTypeEcho)
			}

			for _, c := range []struct {
				name   string
				packet []byte
				accept bool
			}{
				{"own", own, true},
				{"foreign id", foreign, false},
				{"request", request, false},
				{"short", own[:headerLen+echoIDOffset], false},
				{"empty", nil, false},
			} {
				accepted, err := vm.Run(c.packet)
				if err != nil || (accepted > 0) != c.accept {
					t.Fatalf(
						"proto=%d ihl=%d %s: accepted=%d error=%v",
						proto,
						ihl,
						c.name,
						accepted,
						err,
					)
				}
			}
		}
	}
}
