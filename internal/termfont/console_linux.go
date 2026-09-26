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
	var buf []unipair

	for range maxUnimapTries {
		n, errno := getUnimap(fd, buf)

		switch {
		case errno == 0:
			return buf[:min(n, len(buf))], true
		case errno == unix.ENOMEM && n > len(buf):
			buf = make([]unipair, n)
		default:
			return nil, false
		}
	}

	return nil, false
}

// getUnimap issues one GIO_UNIMAP with buf as the output capacity and returns the
// kernel-reported entry count and the ioctl errno.
func getUnimap(fd uintptr, buf []unipair) (int, syscall.Errno) {
	// len(buf) came from a previous uint16 entryCt, so it always fits.
	desc := unimapdesc{entryCt: uint16(len(buf))} //nolint:gosec // bounded by a prior uint16 count.
	if len(buf) > 0 {
		desc.entries = &buf[0]
	}

	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		fd,
		gioUnimap,
		uintptr(unsafe.Pointer(&desc)), //nolint:gosec // ioctl ABI: pass the kernel a *unimapdesc.
	)
	// desc.entries is a Go pointer the kernel writes through; keep its array alive
	// until the syscall has returned.
	runtime.KeepAlive(buf)

	return int(desc.entryCt), errno
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
