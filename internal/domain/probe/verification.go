package probe

import "fmt"

// Verification distinguishes omitted verification from an explicit choice.
type Verification int

// Verification policies accepted by Compile.
const (
	VerifyDefault Verification = iota
	VerifyEnabled
	VerifyDisabled
)

func (v Verification) resolve(def Verification) (Verification, error) {
	switch v {
	case VerifyDefault:
		return def, nil
	case VerifyEnabled, VerifyDisabled:
		return v, nil
	default:
		return 0, fmt.Errorf("invalid verification policy %d", v)
	}
}

// resolveSpelling is the stable identity of a resolved policy, independent of
// configuration syntax: switching it changes which replies count as a success.
func (v Verification) resolveSpelling() string {
	if v == VerifyEnabled {
		return "verify"
	}

	return "noverify"
}
