package probe

import (
	"errors"
	"fmt"
	"strings"
)

// compileOptions checks conditions whose availability depends on the relay dialect.
func (s SSH) compileOptions(dest Destination) (SSH, error) {
	if s.OS != OSJUNOS && (s.Interface != "" || s.Instance != "") {
		return s, errors.New("interface and instance require os=JUNOS")
	}

	err := s.checkCLIWords(dest)
	if err != nil {
		return s, err
	}

	echo, err := s.Echo.normalizeRelay(s.OS, s.Family)
	if err != nil {
		return s, err
	}

	if s.OS == OSJUNOS && s.Family == FamilyIPv6 {
		echo.DF = false
	}

	timeout, err := s.Timeout.normalize(s.DefaultTimeout())
	if err != nil {
		return s, err
	}

	s.Echo, s.Timeout = echo, timeout

	return s, nil
}

func (s SSH) checkCLIWords(dest Destination) error {
	if s.OS != OSJUNOS {
		return nil
	}

	for _, word := range []string{dest.String(), s.Source.String(), s.Interface, s.Instance} {
		if strings.IndexFunc(word, junosSpecial) >= 0 {
			return fmt.Errorf("invalid JUNOS ping value %q: expected a CLI word", word)
		}
	}

	return nil
}

// JUNOS receives an operational CLI command instead of a POSIX shell command. Its
// values must be single words without pipes, commands, quotes or expansions.
func junosSpecial(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return false
	default:
		return !strings.ContainsRune("-_./:%", r)
	}
}
