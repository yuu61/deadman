package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuu61/deadman/internal/application/monitoring"
	"github.com/yuu61/deadman/internal/domain/monitor"
	"github.com/yuu61/deadman/internal/domain/probe"
	"github.com/yuu61/deadman/internal/presentation/resultbar"
)

// writeConfig writes a config file into a fresh temporary directory and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "deadman.conf")

	err := os.WriteFile(path, []byte(body), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

// newService is the only place the real adapters meet the use cases; the layers' own
// tests run on fakes. So check the wiring end to end: the session opens on the real
// config file, a target the real prober factory rejects degrades to a rejected line
// carrying the factory's reason, a reload rereads the same file (and reports it
// missing), and without -l there is no writer.
func TestNewServiceWiresRealAdapters(t *testing.T) {
	// probe=snmp without community= cannot be built: Compile requires the community.
	path := writeConfig(t, "bad 192.0.2.1 probe=snmp relay=h\n")

	svc, logWriter := newService(path, "")
	if logWriter != nil {
		t.Fatal("newService without a log directory returned a LogWriter")
	}

	session, loaded, err := svc.Open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(session.Close)

	rows := monitoredRows(session.Table())

	table := session.Table()
	if _, ok := table[0].(monitoring.Rejected); len(rows) != 0 || len(table) != 1 ||
		!ok {
		t.Fatalf(
			"an unbuildable target should become a rejected line, got %+v / %+v",
			rows,
			table,
		)
	}

	if w := loaded.Warnings; len(w) != 1 || !strings.Contains(buildReason(w[0]), "community") {
		t.Errorf("want the snmp mode's reason (community) in one warning, got %v", w)
	}

	// An ssh-relay target: built without opening any socket, whatever the runner's privilege.
	err = os.WriteFile(
		path,
		[]byte("columns MIN=off\nh 192.0.2.1 probe=ssh relay=jump os=Linux\n"),
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}

	out := session.Update(mustEvent(t, session.Reload()), time.Now())
	if out.Reload == nil || !out.Reload.Applied {
		t.Fatalf("reload of the edited file = %+v, want it applied", out.Reload)
	}

	rows = monitoredRows(session.Table())
	if len(rows) != 1 || rows[0].Target.Name != "h" ||
		rows[0].Plan.Method() != probe.MethodSSH {
		t.Errorf("reloaded rows = %+v, want the ssh-relay target h", rows)
	}

	if v, set := out.Reload.Display.Columns["MIN"]; !set || v {
		t.Errorf("reloaded Columns = %v, want MIN=off from the file", out.Reload.Display.Columns)
	}

	err = os.Remove(path)
	if err != nil {
		t.Fatal(err)
	}

	out = session.Update(mustEvent(t, session.Reload()), time.Now())
	if out.Reload == nil || out.Reload.Applied || len(out.Reload.Warnings) != 1 {
		t.Fatalf("reload of a missing file = %+v, want a 'reload failed' warning", out.Reload)
	}

	if _, ok := out.Reload.Warnings[0].(monitoring.ReloadFailed); !ok {
		t.Errorf("reload of a missing file = %+v, want a 'reload failed' warning", out.Reload)
	}
}

// A config file that cannot be read starts no session: the error names the file.
func TestNewServiceOpenMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.conf")
	svc, _ := newService(path, "")

	s, _, err := svc.Open(t.Context(), false)
	if err == nil || s != nil || !strings.Contains(err.Error(), "missing.conf") {
		t.Errorf("Open of a missing config = %v, %v; want the open error and no session", s, err)
	}
}

// With -l, newService logs through the LogWriter it returns.
func TestNewServiceLogsThroughWriter(t *testing.T) {
	dir := t.TempDir()

	ports, logWriter := newPorts(sshTargetConfig(t), dir)
	if logWriter == nil || ports.Log == nil {
		t.Fatal("newPorts with a log directory returned no LogWriter")
	}

	recordThrough(t, ports, probe.SuccessResult(3))

	err := logWriter.Close()
	if err != nil {
		t.Fatal(err)
	}

	b := readOnlyLog(t, dir)

	if fields := strings.Fields(string(b)); len(fields) != 6 || fields[2] != "up" ||
		fields[3] != "3.000" {
		t.Errorf("log line = %q, want an up line with RTT 3.000", b)
	}
}

// Every observable consumer must distinguish target failure from unavailable
// measurement, including unrecognized future codes.
func TestOutcomeAgreesAcrossStatisticsLogAndDisplay(t *testing.T) {
	for _, code := range []probe.ResultCode{
		probe.Failed, probe.Success, probe.RelayTimeout, probe.RelayFailed, probe.Unavailable, 99,
	} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			dir := t.TempDir()
			ports, writer := newPorts(sshTargetConfig(t), dir)
			result := probe.Result{Code: code, RTT: 3}
			target := recordThrough(t, ports, result)

			err := writer.Close()
			if err != nil {
				t.Fatal(err)
			}

			data := readOnlyLog(t, dir)

			fields := strings.Fields(string(data))
			if len(fields) != 6 {
				t.Fatalf("invalid log line: %q", data)
			}

			wantState, wantStatus := monitor.Unknown, "unknown"
			wantSnt, wantLoss := 0, 0

			switch code {
			case probe.Success:
				wantState, wantStatus = monitor.Up, "up"
				wantSnt = 1
			case probe.Failed:
				wantState, wantStatus = monitor.Down, "down"
				wantSnt, wantLoss = 1, 1
			case probe.RelayTimeout, probe.RelayFailed, probe.Unavailable:
				// No target observation.
			default:
				if code != 99 {
					t.Fatalf("unhandled result code %v", code)
				}
			}

			if target.State != wantState || target.Snt != wantSnt ||
				target.Loss != wantLoss || fields[2] != wantStatus {
				t.Fatalf("statistics/log disagree for %v: %+v %q", code, target, data)
			}

			success := code == probe.Success
			if !success && fields[3] != "0.000" {
				t.Fatalf("failure logged a stale RTT: %q", data)
			}

			glyph := resultbar.Glyph(result, 10, 0, resultbar.BarBlock)

			level := resultbar.Level(result, 10, 0, resultbar.BarBlock)
			if resultbar.IsFailGlyph(glyph) == success || (level == resultbar.NoLevel) == success {
				t.Fatalf("display disagrees for %v: glyph=%q level=%d", code, glyph, level)
			}
		})
	}
}

// readOnlyLog reads the one row's log in dir, besides the index of the rows.
func readOnlyLog(t *testing.T, dir string) []byte {
	t.Helper()

	logs, err := fs.Glob(os.DirFS(dir), "target-*.log")
	if err != nil || len(logs) != 1 {
		t.Fatalf("log files = %v, err = %v; want one file", logs, err)
	}

	data, err := fs.ReadFile(os.DirFS(dir), logs[0])
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// sshTargetConfig writes a config with a single ssh-relay target "h", which the real
// prober factory builds without opening a socket.
func sshTargetConfig(t *testing.T) string {
	t.Helper()

	return writeConfig(t, "h 192.0.2.1 probe=ssh relay=jump os=Linux\n")
}

// fixedPinger answers every probe with res.
type fixedPinger struct{ res probe.Result }

func (p fixedPinger) Send(context.Context) probe.Result { return p.res }

// recordThrough runs one round of a session over ports' single target, whose probe
// answers res, and returns the target as it recorded it: a session is the only path by
// which a result reaches the statistics and the log. Only the prober is faked, and the
// waits are over at once.
func recordThrough(t *testing.T, ports monitoring.Ports, res probe.Result) monitor.Reading {
	t.Helper()

	ports.NewPinger = func(probe.Plan, string) (probe.Pinger, error) { return fixedPinger{res}, nil }
	ports.Wait = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }

	s, _, err := monitoring.NewService(ports).Open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(s.Close)

	// The round starts, then the probe starts, then its result is recorded.
	tasks := []monitoring.Task{s.Start()}
	for range 3 {
		if len(tasks) != 1 {
			t.Fatalf("a one-target round ran %d tasks at once", len(tasks))
		}

		out := s.Update(mustEvent(t, tasks[0]), time.Now())
		if out.Recorded != nil {
			return out.Recorded.Reading
		}

		tasks = out.Tasks
	}

	t.Fatal("the session did not record the result")

	return monitor.Reading{}
}

// mustEvent runs task and returns its event, failing the test when it reports none.
func mustEvent(t *testing.T, task monitoring.Task) monitoring.Event {
	t.Helper()

	event, ok := task()
	if !ok {
		t.Fatal("task reported no event")
	}

	return event
}

// routerOSLine is a routeros target line with relay= written as relay, and credentials.
func routerOSLine(relay string) string {
	return "r 192.0.2.1 probe=routeros username=u password=p relay=" + relay
}

// Every mistake a target line can be judged by without I/O is a rejected line with its
// reason, from the config text through the real adapters — never a row that is built and
// then fails (or sends from somewhere else) every round without saying why.
func TestStaticMistakesAreRejectedWithTheirReason(t *testing.T) {
	for line, reason := range map[string]string{
		"gw fe80::2 probe=nexthop nexthop=fe80::1":                          "link-local gateway",
		"gw 192.0.2.1 probe=nexthop nexthop=192.0.2.254 source=2001:db8::9": "IPv6",
		"s 192.0.2.1 source=2001:db8::9":                                    "IPv6",
		"s ::1 source=127.0.0.1":                                            "IPv4",
		"s 192.0.2.1 probe=ssh relay=jump os=Darwin source=en0":             "Darwin",
		"s 192.0.2.1 probe=ssh relay=jump os=FreeBSD source=192.0.2.9":      "FreeBSD",
		"t 192.0.2.1 probe=tcp":                                             "tcp requires port",
		"q 192.0.2.1 probe=quic port=0":                                     "invalid port 0",
		"q example.com probe=quic alpn=":                                    "needs a value",
		routerOSLine("r scheme="):                                           "needs a value",
		"t 2001:db8::1 probe=tcp port=80":                                   "IPv4 only",
		"n fe80::1%eth0 probe=snmp relay=agent community=public":            "zone",
		"n 192.0.2.1 probe=snmp relay=udp6:[::1]:161 community=public":      "host name or IP address",
		"m ::ffff:192.0.2.1%eth0":                                           "cannot have a zone",
		"m 192.0.2.1 source=::ffff:192.0.2.9%eth0":                          "cannot have a zone",
		"g fe80::2%eth9 probe=nexthop nexthop=fe80::1 source=eth1":          "must not have a zone",
		routerOSLine("router|1"):                                            "host name or IP address",
		routerOSLine(":8443"):                                               "expected a host",
		routerOSLine("fe80::1%eth0:8443"):                                   "in brackets",
		"n 192.0.2.1 probe=snmp relay=agent|1 community=public":             "host name or IP address",
	} {
		svc, _ := newService(writeConfig(t, line+"\n"), "")

		session, _, err := svc.Open(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}

		table := session.Table()

		rejected, ok := table[0].(monitoring.Rejected)
		if !ok || len(monitoredRows(session.Table())) != 0 ||
			!strings.Contains(rejected.Reason, reason) {
			t.Errorf("%s: lines = %+v, want rejected for %q", line, table, reason)
		}

		session.Close()
	}
}

// A zoned IPv6 source is an address everywhere: the relay's ping gets it as one, where it
// used to be taken for an interface name and refused on a Darwin relay.
func TestZonedSourceIsAnAddress(t *testing.T) {
	svc, _ := newService(
		writeConfig(t, "s 2001:db8::1 probe=ssh relay=jump os=Darwin source=fe80::9%en0\n"),
		"",
	)

	session, _, err := svc.Open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(session.Close)

	if len(monitoredRows(session.Table())) != 1 {
		t.Fatalf("zoned source address refused: %+v", session.Table())
	}
}

// buildReason is the reason a TargetBuildFailed diagnostic gives; "" for another kind.
func buildReason(d monitoring.Diagnostic) string {
	failed, ok := d.(monitoring.TargetBuildFailed)
	if !ok {
		return ""
	}

	return failed.Reason
}

// An IPv4-mapped target is probed as the IPv4 address it maps, while the row still shows
// the address as written.
func TestMappedTargetIsProbedAsIPv4(t *testing.T) {
	svc, _ := newService(
		writeConfig(t, "m ::ffff:192.0.2.1 probe=ssh relay=jump os=Linux\n"),
		"",
	)

	session, _, err := svc.Open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(session.Close)

	rows := monitoredRows(session.Table())
	if len(rows) != 1 {
		t.Fatalf("mapped target refused: %+v", session.Table())
	}

	if rows[0].Plan.Destination().String() != "192.0.2.1" ||
		rows[0].Target.Addr != "::ffff:192.0.2.1" {
		t.Errorf("plan address %q, shown %q", rows[0].Plan.Destination(), rows[0].Target.Addr)
	}
}

// monitoredRows lists the monitored rows of a table, in config order.
func monitoredRows(lines []monitoring.Line) []monitoring.Monitored {
	var out []monitoring.Monitored

	for _, l := range lines {
		if m, ok := l.(monitoring.Monitored); ok {
			out = append(out, m)
		}
	}

	return out
}
