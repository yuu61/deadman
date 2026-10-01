package probe

import (
	"fmt"
	"net/netip"
	"strings"
)

// ParseIP reads an IP literal, allowing leading zeros in each decimal IPv4 octet,
// including the dotted tail of IPv6. IPv6 zones are preserved; unmapping and the
// validity of a zone for a method remain Compile's decisions.
func ParseIP(raw string) (netip.Addr, error) {
	host, zone, zoned := strings.Cut(raw, "%")
	start := strings.LastIndex(host, ":") + 1
	prefix, tail := host[:start], host[start:]

	host = prefix + decimalIPv4(tail)
	if zoned {
		host += "%" + zone
	}

	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q: %w", raw, err)
	}

	return a, nil
}

// decimalIPv4 removes leading zeros only from a complete dotted IPv4 tail.
// netip still checks digits and ranges, so malformed input cannot become an address.
func decimalIPv4(tail string) string {
	parts := strings.Split(tail, ".")

	const ipv4Octets = 4

	if len(parts) != ipv4Octets {
		return tail
	}

	for i, part := range parts {
		if part == "" {
			return tail
		}

		parts[i] = strings.TrimLeft(part, "0")
		if parts[i] == "" {
			parts[i] = "0"
		}
	}

	return strings.Join(parts, ".")
}
