package probe

import (
	"fmt"
	"strings"
)

// checkNUL rejects text that cannot cross an OS string boundary. Do not include the
// value in the error: a credential or a private key path may be what is being checked.
func checkNUL(what, value string) error {
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must not contain NUL", what)
	}

	return nil
}
