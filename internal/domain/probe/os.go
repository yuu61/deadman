package probe

// OS names an operating system by its `uname -s` spelling, as the os= attribute writes
// it. It is the one OS vocabulary: a target's OS (which ping syntax a relayed probe runs)
// and this host's platform (which privilege remedy a startup warning offers) both use it.
type OS string

// The operating systems deadman tells apart. Another name (an unknown GOOS, or an os=
// value outside these) passes through as its own OS and takes each caller's default.
const (
	OSLinux   OS = "Linux"
	OSDarwin  OS = "Darwin"
	OSFreeBSD OS = "FreeBSD"
	OSWindows OS = "Windows"
)
