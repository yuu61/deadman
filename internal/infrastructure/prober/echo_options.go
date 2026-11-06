package prober

import (
	"strconv"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func (p *subprocessPinger) echoArgs() []string {
	var args []string

	size := p.echo.Size.Bytes()
	bsdIPv6 := p.family == probe.FamilyIPv6 && (p.os == probe.OSDarwin || p.os == probe.OSFreeBSD)
	// BSD IPv6 ping defaults to 8 bytes, while Compile resolves omission to 56.
	if size != probe.RelayPayloadSize || bsdIPv6 {
		args = append(args, "-s", strconv.Itoa(size))
	}

	// -b sets both buffers. Grow the 8 KiB send buffer while retaining ping's
	// default 48 KiB receive capacity, including ancillary data and packet bursts.
	const (
		defaultBSDSendBuffer    = 8 * 1024
		defaultBSDReceiveBuffer = 48 * 1024
	)
	if bsdIPv6 && echoReceiveSize(size) > defaultBSDSendBuffer {
		buffer := max(defaultBSDReceiveBuffer, echoSocketBufferSize(size))
		args = append(args, "-b", strconv.Itoa(buffer))
	}

	if p.echo.DF {
		if p.os == probe.OSLinux {
			args = append(args, "-M", "do")
		} else {
			args = append(args, "-D")
		}
	}

	return args
}

// JUNOS runs an operational CLI, so env and POSIX shell quoting cannot precede ping.
// Compile limits all values to literal CLI words.
func (p *subprocessPinger) junosCommand() []string {
	family := "inet"
	if p.family == probe.FamilyIPv6 {
		family = "inet6"
	}

	args := []string{"ping", p.addr, "count", "1", family, "wait", "1"}
	if size := p.echo.Size.Bytes(); size != probe.RelayPayloadSize {
		args = append(args, "size", strconv.Itoa(size))
	}

	if p.echo.DF && p.family == probe.FamilyIPv4 {
		args = append(args, "do-not-fragment")
	}

	if p.source.IsSet() {
		args = append(args, "source", p.source.String())
	}

	if p.iface.IsSet() {
		args = append(args, "interface", p.iface.String())
	}

	if p.instance.IsSet() {
		args = append(args, "routing-instance", p.instance.String())
	}

	return args
}
