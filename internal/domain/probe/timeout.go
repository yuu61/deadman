package probe

import (
	"fmt"
	"time"
)

// Timeout is an optional overall SSH probe deadline, distinct from its ping wait.
type Timeout struct {
	duration time.Duration
	set      bool
}

// TimeoutAfter is an explicitly configured deadline.
func TimeoutAfter(d time.Duration) Timeout { return Timeout{duration: d, set: true} }

// Duration returns the deadline. A compiled plan always carries a resolved timeout.
func (t Timeout) Duration() time.Duration { return t.duration }

func (t Timeout) normalize(def time.Duration) (Timeout, error) {
	d := def
	if t.set {
		d = t.duration
	}

	if d <= 0 {
		return Timeout{}, fmt.Errorf("invalid timeout %s (want positive seconds)", d)
	}

	return TimeoutAfter(d), nil
}

// DefaultTimeout is the overall SSH deadline, including login and ping startup.
func (s SSH) DefaultTimeout() time.Duration {
	if s.OS == OSDarwin && s.Family == FamilyIPv6 {
		return darwinIPv6Timeout
	}

	return sshTimeout
}

const (
	// Darwin ping6 waits about 10 seconds for an unanswered echo, with no wait flag.
	// Allow its summary to arrive so target loss is not mistaken for a relay timeout.
	darwinIPv6Timeout = 15 * time.Second
	sshTimeout        = 5 * time.Second
)
