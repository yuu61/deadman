deadman
=======

deadman is an observation software for host status using ping.

deadman does not have rich functionalities. It only checks host
statuses using ICMP echo. We recomend using deadman for building
temporary networks such as conference and event networks. This
software was originally designed and implemented for Interop Tokyo
ShowNet.

This repository is a **Go rewrite** of the original Python implementation.
It keeps the configuration-file format and command-line flags compatible,
while adding cross-platform support (Windows, Linux, macOS) and shipping as a
single static binary.

![demo](https://github.com/upa/deadman/raw/master/img/deadman-demo.gif)

Build
=====

Requires Go 1.24 or later.

	$ git clone https://github.com/yuu61/deadman
	$ cd deadman
	$ go build -o deadman ./cmd/deadman      # Windows: go build -o deadman.exe ./cmd/deadman

You can also run it directly or install it:

	$ go run ./cmd/deadman deadman.conf
	$ go install github.com/yuu61/deadman/cmd/deadman@latest

How to use
==========

	$ ./deadman deadman.conf                 # Windows: deadman.exe deadman.conf

To change the targets, modify or create a config file. The format is unchanged
from the original deadman:

	$ cat deadman.conf
	google          173.194.117.176
	googleDNS       8.8.8.8
	---
	kame            203.178.141.194
	kame6           2001:200:dff:fff1:216:3eff:feb1:44d7

Each line in the config file indicates a target host. A line of dashes (`---`)
renders a separator, which is useful for grouping targets.

Relay and probing options are written as `key=value` attributes after the
address. For example, ping via a remote host through ssh:

	google-via-ssh  173.194.117.176 relay=X.X.X.X os=Linux

This sends ping to a google server via the remote server X.X.X.X. The ssh
username and key can be specified by `user=USER`, `key=KEYPATH`. Other relay
modes are documented in `deadman.conf`:

| Mode        | Example                                                                 |
|-------------|-------------------------------------------------------------------------|
| direct ICMP | `googleDNS 8.8.8.8`                                                      |
| ssh relay   | `name ADDR relay=SSHHOST os=Linux user=USER key=KEY`                    |
| snmp        | `name ADDR relay=SNMPHOST via=snmp community=COMMUNITY`                 |
| netns       | `name ADDR relay=NETNSNAME via=netns` (Linux, root)                     |
| vrf         | `name ADDR relay=VRFNAME via=vrf` (Linux, root)                         |
| routeros    | `name ADDR relay=ROS via=routeros_api username=U password=P method=https verify=false` |
| tcp/hping3  | `name ADDR tcp=dstport:80` (Linux, root)                                |

The optional `source=...` attribute binds the probe to a source address: an IP
address (any mode), or — for direct ICMP and the ssh/netns/vrf relays on
Linux/macOS — a network interface name such as `source=eth0`.

Options
=======

	-s, --scale N       scale of the RTT bar graph in ms (default 10)
	-a, --async-mode    send pings to all targets concurrently
	-b, --blink-arrow   blink the arrow in async mode
	-l, --logging DIR   write a per-target log file under DIR

Controls
========

	r           reset the statistics of all targets (keeps the program running)
	R           reload the configuration file (Windows; on Unix use SIGHUP)
	q / Ctrl-C  quit

On Unix you can send deadman a SIGHUP to reload its configuration file. When
this happens, existing entries keep their history (matched by name/address and
relay attributes). Terminal resizing is handled automatically.

Privileges and platform notes
=============================

Direct ICMP uses a native socket (no `ping` binary required):

- **Windows**: works without administrator elevation.
- **Linux**: unprivileged ICMP needs
  `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"`, or run with
  `setcap cap_net_raw+ep ./deadman` or as root.
- **macOS**: works out of the box.

The relay modes shell out to external commands and only work where those exist:
`ssh` (ssh relay), `snmpping` (snmp), `ip` (netns/vrf), `hping3` (tcp). netns,
vrf and hping3 are Linux + root. The RouterOS API mode uses HTTP and is
OS-independent. Where a required command is unavailable (e.g. on Windows), that
target simply shows as failed (`X`) rather than crashing.

A Unicode- and color-capable terminal is recommended (Windows Terminal on
Windows) so the block-character RTT bars (`▁▂▃▄▅▆▇█`) render correctly.

License
=======

MIT


Contact
=======

upa@haeena.net
