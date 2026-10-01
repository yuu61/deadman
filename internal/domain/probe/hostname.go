package probe

import (
	"fmt"
	"unicode"
)

// isHostName reports whether name is spelled as a host name: the letters, digits,
// hyphens, underscores and dots of a DNS or hosts-file name. A relay's host is dialed as
// written (and RouterOS's is put in the host of a URL), so any other character would fail
// every probe or end or escape the URL's host; Compile refuses it up front instead.
func isHostName(name string) bool {
	if name == "" {
		return false
	}

	for _, c := range []byte(name) {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}

	return true
}

// checkName refuses a name no resolver can look up and no relay can take as one word:
// one with whitespace or a control character in it (a quoted "web 1" written where the
// address goes, say), which would fail every probe. Any other spelling may be a name
// somewhere the probe resolves it (a hosts file, a relay's resolver, an internationalized
// name), so it is left to the resolver. what names the name in the error.
func checkName(what, name string) error {
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%s %q is not a host name: it contains %q", what, name, r)
		}
	}

	return nil
}
