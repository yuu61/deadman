package probe

import (
	"errors"
	"fmt"
	"strings"
)

// CLIWord is an optional value the JUNOS CLI takes as one word (interface=, instance=). The
// zero value is an omitted one. A word written empty is a set one, which Compile refuses
// instead of reading it as an omission.
type CLIWord struct {
	text string
	set  bool
}

// CLIWordOf is an explicitly configured word.
func CLIWordOf(text string) CLIWord { return CLIWord{text: text, set: true} }

// IsSet reports whether the word was given.
func (w CLIWord) IsSet() bool { return w.set }

// String returns the word as written, or "" when omitted.
func (w CLIWord) String() string { return w.text }

// compileOptions checks conditions whose availability depends on the relay dialect.
func (s SSH) compileOptions(dest Destination) (SSH, error) {
	if s.OS != OSJUNOS && (s.Interface.IsSet() || s.Instance.IsSet()) {
		return s, errors.New("interface and instance require os=JUNOS")
	}

	err := s.checkCLIWords(dest)
	if err != nil {
		return s, err
	}

	echo, err := s.Echo.normalizeRelay(s.OS)
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

	for _, word := range []string{
		dest.String(), s.Source.String(), s.Interface.String(), s.Instance.String(),
	} {
		if strings.IndexFunc(word, junosSpecial) >= 0 {
			return fmt.Errorf("invalid JUNOS ping value %q: expected a CLI word", word)
		}
	}

	for _, word := range []CLIWord{s.Interface, s.Instance} {
		if word.set && word.text == "" {
			return errors.New("invalid JUNOS ping value \"\": expected a CLI word")
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
