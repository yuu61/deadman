package config

import (
	"errors"
	"strings"
)

// ErrUnknownBool reports a token that is neither a truthy nor a falsy spelling.
var ErrUnknownBool = errors.New("config: unrecognized boolean token")

// ParseBoolToken parses a case-insensitive boolean token from a directive value,
// mirroring strconv.ParseBool but with the spellings deadman's config accepts: truthy
// on/true/yes/1, falsy off/false/no/0. Any other spelling (including "") returns
// ErrUnknownBool, so callers can keep their own default and distinguish "explicitly set"
// from "absent". This is the single source of the boolean vocabulary shared by the
// "columns" directive and the relay "verify=" key.
func ParseBoolToken(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, ErrUnknownBool
	}
}
