package probe

import (
	"fmt"
	"strconv"
	"strings"
)

// PayloadSize distinguishes an omitted size from an explicit byte count, including 0.
type PayloadSize struct {
	n   int
	set bool
}

// PayloadBytes is an explicitly configured ICMP payload length, excluding headers.
func PayloadBytes(n int) PayloadSize { return PayloadSize{n: n, set: true} }

// Bytes returns the byte count. A compiled plan always carries a resolved size.
func (s PayloadSize) Bytes() int { return s.n }

// EchoOptions are the portable conditions of a direct or subprocess ICMP echo.
type EchoOptions struct {
	Size PayloadSize
	DF   bool
}

// DirectPayloadSize is the native echo's default payload, including its reply token.
const DirectPayloadSize = 24

// RelayPayloadSize is the default payload length Compile resolves for relays.
const RelayPayloadSize = 56

// RelayMinPayload is the smallest payload a relay's ping can time. The ping puts its send
// time in the payload, and prints a reply's RTT only when the payload holds it:
// iputils needs sizeof(struct timeval), 16 bytes on a 64-bit host, and the BSDs' struct tv32
// needs 8. A smaller payload would give replies with no RTT, which cannot be told from 0 ms.
const RelayMinPayload = 16

const maxEchoPayload = 65507 // An IPv4 packet minus its IP and ICMP headers.

func (e EchoOptions) normalize(minimum, maximum, def int) (EchoOptions, error) {
	n := def
	if e.Size.set {
		n = e.Size.n
	}

	if n < minimum || n > maximum {
		return EchoOptions{}, fmt.Errorf("invalid size %d (want %d-%d)", n, minimum, maximum)
	}

	e.Size = PayloadBytes(n)

	return e, nil
}

func (e EchoOptions) normalizeRelay(os OS) (EchoOptions, error) {
	minimum, maximum := RelayMinPayload, maxEchoPayload

	if os == OSJUNOS {
		maximum = 65468
	}

	return e.normalize(minimum, maximum, RelayPayloadSize)
}

// identity extends the existing key only for non-default conditions, preserving old
// log filenames and making an explicitly written default equal its omission.
func (e EchoOptions) identity(b *strings.Builder, def int) {
	if n := e.Size.Bytes(); n != def {
		writeKey(b, "size="+strconv.Itoa(n))
	}

	if e.DF {
		writeKey(b, "df")
	}
}
