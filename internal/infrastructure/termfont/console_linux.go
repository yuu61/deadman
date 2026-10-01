//go:build linux

package termfont

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// gioUnimap is GIO_UNIMAP from <linux/kd.h>: read a virtual console font's
// Unicode-to-glyph map. golang.org/x/sys/unix does not export it.
const gioUnimap = 0x4B66

// unipair and unimapdesc mirror <linux/kd.h>'s struct unipair and struct unimapdesc.
// Go lays unimapdesc out as C does (the pointer aligned after the uint16 count).
type unipair struct {
	ucs     uint16 // Unicode code point (the map is BMP-only).
	fontpos uint16 // glyph index in the loaded font.
}

type unimapdesc struct {
	entryCt uint16   // in: capacity of entries; out: the map's total entry count.
	entries *unipair // out: up to the in-capacity entries.
}

// maxUnimapTries bounds the size-then-fetch loop, which only repeats if the font is
// swapped (setfont) between the sizing call and the fetch.
const maxUnimapTries = 3

// consoleLacks reports whether f is a Linux virtual console whose loaded font has no
// glyph for some rune of s. It is false whenever that cannot be established — f is not
// a virtual console (a pty, pipe or file answers the ioctl with ENOTTY/EINVAL) or the
// font map cannot be read — leaving the decision to the locale check.
func consoleLacks(f *os.File, s string) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}

	var (
		m  []unipair
		ok bool
	)

	err = rc.Control(func(fd uintptr) { m, ok = readUnimap(fd) })

	return err == nil && ok && !mapCovers(m, s)
}

// readUnimap reads the console font's Unicode map. The kernel fills at most the given
// capacity, reports the full size back in entryCt, and fails with ENOMEM when the
// capacity was short, so the first call (capacity 0) sizes the buffer for the second.
func readUnimap(fd uintptr) ([]unipair, bool) {
	var capacity uint16

	for range maxUnimapTries {
		buf, n, errno := getUnimap(fd, capacity)

		switch {
		case errno == 0:
			return buf[:min(int(n), len(buf))], true
		case errno == unix.ENOMEM && n > capacity:
			capacity = n
		default:
			return nil, false
		}
	}

	return nil, false
}

// getUnimap issues one GIO_UNIMAP with room for capacity entries and returns the entries,
// the kernel-reported total entry count and the ioctl errno. The count is entryCt's
// uint16 both ways, so a capacity is always one the kernel can be told.
func getUnimap(fd uintptr, capacity uint16) ([]unipair, uint16, syscall.Errno) {
	buf := make([]unipair, capacity)

	desc := unimapdesc{entryCt: capacity}
	if len(buf) > 0 {
		desc.entries = &buf[0]
	}

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		fd,
		gioUnimap,
		// The ioctl takes a pointer to the kernel's struct unimapdesc, which only
		// unsafe.Pointer can pass.
		uintptr(unsafe.Pointer(&desc)), //nolint:gosec // G103: the ioctl ABI requires it.
	)
	// desc.entries is a Go pointer the kernel writes through; keep its array alive
	// until the syscall has returned.
	runtime.KeepAlive(buf)

	return buf, desc.entryCt, errno
}

// mapCovers reports whether every rune of s has an entry in the Unicode map m. A rune
// outside the BMP can never match, as the kernel map is 16-bit.
func mapCovers(m []unipair, s string) bool {
	have := make(map[rune]bool, len(m))
	for _, p := range m {
		have[rune(p.ucs)] = true
	}

	for _, r := range s {
		if !have[r] {
			return false
		}
	}

	return true
}
