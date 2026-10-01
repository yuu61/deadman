//go:build !linux

package termfont

import "os"

// consoleLacks inspects a Linux virtual console's font (GIO_UNIMAP); other platforms
// have no such query, so it never vetoes and CanRender relies on the locale alone.
func consoleLacks(_ *os.File, _ string) bool { return false }
