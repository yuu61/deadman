package prober

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// pingResults is one poll's answer: pingResultsOperStatus, MinRtt, ProbeResponses and
// SentProbes.
type pingResults struct{ oper, minRTT, responses, sent int }

const operEnabled = 1

// agentScript is how a fakeAgent answers.
type agentScript struct {
	refuseDestroy gosnmp.SNMPError // stale row cannot be cleared.
	silent        bool             // answer nothing.
	refuse        gosnmp.SNMPError // the error-status of the answer to the create.
	noMIB         bool             // polls find no such object.
	// results answers poll i with results[min(i, len-1)]; nil is no such instance.
	results []*pingResults
	// pollDelay holds back the answer to each poll, but not to a SET: a slow agent.
	pollDelay time.Duration
}

// fakeAgent is an SNMPv2c agent on the loopback. It records every SET and answers the
// SETs and the polls of an entry's results as scripted.
type fakeAgent struct {
	conn   net.PacketConn
	script agentScript

	mu        sync.Mutex
	sets      [][]gosnmp.SnmpPDU
	polls     int
	community string
}

func startAgent(t *testing.T, script agentScript) *fakeAgent {
	t.Helper()

	conn, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("loopback UDP is unavailable in this environment: %v", err)
		}

		t.Fatal(err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	a := &fakeAgent{conn: conn, script: script}
	go a.serve()

	return a
}

func (a *fakeAgent) serve() {
	buf := make([]byte, 1<<16)
	dec := &gosnmp.GoSNMP{Version: gosnmp.Version2c}

	for {
		n, from, err := a.conn.ReadFrom(buf)
		if err != nil {
			return
		}

		// The decoded values alias the packet, so each gets its own copy.
		req, err := dec.SnmpDecodePacket(slices.Clone(buf[:n]))
		if err != nil {
			continue
		}

		reply := a.answer(req)
		if reply == nil {
			continue
		}

		out, err := reply.MarshalMsg()
		if err != nil {
			continue
		}

		if req.PDUType != gosnmp.SetRequest && a.script.pollDelay > 0 {
			time.AfterFunc(a.script.pollDelay, func() { _, _ = a.conn.WriteTo(out, from) })

			continue
		}

		_, _ = a.conn.WriteTo(out, from)
	}
}

// polled reports how many polls the agent has received.
func (a *fakeAgent) polled() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.polls
}

func (a *fakeAgent) answer(req *gosnmp.SnmpPacket) *gosnmp.SnmpPacket {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.community = req.Community
	reply := &gosnmp.SnmpPacket{
		Version:   gosnmp.Version2c,
		Community: req.Community,
		PDUType:   gosnmp.GetResponse,
		RequestID: req.RequestID,
		Variables: req.Variables,
	}

	if req.PDUType == gosnmp.SetRequest {
		a.sets = append(a.sets, req.Variables)

		reply.Error = a.setError(len(req.Variables))
		if reply.Error != gosnmp.NoError {
			reply.ErrorIndex = 1
		}
	} else {
		reply.Variables = a.poll(req.Variables)
		a.polls++
	}

	if a.script.silent {
		return nil
	}

	return reply
}

func (a *fakeAgent) setError(count int) gosnmp.SNMPError {
	if count == 1 {
		return a.script.refuseDestroy
	}

	return a.script.refuse
}

// poll answers a GET of an entry's results columns.
func (a *fakeAgent) poll(vars []gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
	r := a.script.results[min(a.polls, len(a.script.results)-1)]
	out := make([]gosnmp.SnmpPDU, 0, len(vars))

	for _, v := range vars {
		column, _, _ := strings.Cut(strings.TrimPrefix(v.Name, pingResultsEntry+"."), ".")
		answer := gosnmp.SnmpPDU{Name: v.Name, Type: gosnmp.Gauge32}

		switch {
		case a.script.noMIB:
			answer.Type = gosnmp.NoSuchObject
		case r == nil:
			answer.Type = gosnmp.NoSuchInstance
		case column == strconv.Itoa(resultsOperStatus):
			answer.Type, answer.Value = gosnmp.Integer, r.oper
		case column == strconv.Itoa(resultsMinRtt):
			answer.Value = uint(r.minRTT)
		case column == strconv.Itoa(resultsProbeResponses):
			answer.Value = uint(r.responses)
		default:
			answer.Value = uint(r.sent)
		}

		out = append(out, answer)
	}

	return out
}

// recorded returns the SETs the agent received and the community they carried.
func (a *fakeAgent) recorded() ([][]gosnmp.SnmpPDU, string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.sets, a.community
}

// agentPinger is the pinger of an snmp plan for addr through a, with short timeouts.
func agentPinger(t *testing.T, a *fakeAgent, addr string) *snmpPinger {
	t.Helper()

	sp := testSNMPPinger(t, addr, "row#1")
	sp.port = netip.MustParseAddrPort(a.conn.LocalAddr().String()).Port()
	sp.timing = snmpTiming{
		total:     400 * time.Millisecond,
		request:   100 * time.Millisecond,
		cleanup:   100 * time.Millisecond,
		pollFirst: 5 * time.Millisecond,
		pollMax:   20 * time.Millisecond,
	}

	return sp
}

func testSNMPPinger(t *testing.T, addr, row string) *snmpPinger {
	t.Helper()

	p, err := New(compiled(t, probe.Spec{
		Addr:   addr,
		Params: probe.SNMP{Host: "127.0.0.1", Community: "public"},
	}), row)
	if err != nil {
		t.Fatal(err)
	}

	sp, ok := p.(*snmpPinger)
	if !ok {
		t.Fatalf("New returned %T, want *snmpPinger", p)
	}

	return sp
}

// pdus prints varbinds for comparison.
func pdus(vars []gosnmp.SnmpPDU) string {
	var b strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&b, "%s %v %v\n", v.Name, v.Type, v.Value)
	}

	return b.String()
}

// A probe clears the entry, creates it and starts its test in one SET of what
// snmpping sends plus pingCtlType, and destroys it after the test. The agent receives
// the target as InetAddress bytes of its family.
func TestSNMPProbeCreatesAndDestroysPingEntry(t *testing.T) {
	for addr, want := range map[string]struct {
		addrType int
		bytes    []byte
	}{
		"192.0.2.10":   {inetAddressIPv4, []byte{192, 0, 2, 10}},
		"2001:db8::10": {inetAddressIPv6, netip.MustParseAddr("2001:db8::10").AsSlice()},
	} {
		a := startAgent(t, agentScript{results: []*pingResults{
			{oper: operEnabled},
			{oper: operCompleted, minRTT: 7, responses: 1, sent: 1},
		}})
		p := agentPinger(t, a, addr)

		if got := p.Send(t.Context()); got != probe.SuccessResult(7) {
			t.Errorf("%s: Send = %+v, want a 7 ms success", addr, got)
		}

		sets, community := a.recorded()
		if community != "public" {
			t.Errorf("%s: community %q, want public", addr, community)
		}

		col := func(c int) string { return pingCtlEntry + "." + strconv.Itoa(c) + p.index }
		destroy := pdus(
			[]gosnmp.SnmpPDU{{Name: col(ctlRowStatus), Type: gosnmp.Integer, Value: rowDestroy}},
		)
		create := pdus([]gosnmp.SnmpPDU{
			{Name: col(ctlTargetAddressType), Type: gosnmp.Integer, Value: want.addrType},
			{Name: col(ctlTargetAddress), Type: gosnmp.OctetString, Value: want.bytes},
			{Name: col(ctlAdminStatus), Type: gosnmp.Integer, Value: adminEnabled},
			{Name: col(ctlStorageType), Type: gosnmp.Integer, Value: storageVolatile},
			{Name: col(ctlType), Type: gosnmp.ObjectIdentifier, Value: pingIcmpEcho},
			{Name: col(ctlRowStatus), Type: gosnmp.Integer, Value: rowCreateAndGo},
		})

		if len(sets) != 3 || pdus(sets[0]) != destroy || pdus(sets[1]) != create ||
			pdus(sets[2]) != destroy {
			got := make([]string, 0, len(sets))
			for _, s := range sets {
				got = append(got, pdus(s))
			}

			t.Errorf("%s: SETs\n%s\nwant destroy, create, destroy:\n%s%s", addr,
				strings.Join(got, "--\n"), create, destroy)
		}
	}
}

// The agent's answers map to the result codes: a reply is a success, a probe the agent
// sent but got no reply to is the target's failure, and the agent failing or not
// answering is a relay failure (s) or timeout (t), which says nothing about the target.
func TestSNMPProbeOutcome(t *testing.T) {
	for name, c := range map[string]struct {
		script agentScript
		want   probe.Result
	}{
		"reply": {
			agentScript{results: []*pingResults{{oper: operCompleted, minRTT: 3, responses: 1, sent: 1}}},
			probe.SuccessResult(3),
		},
		"reply before the test ends": {
			agentScript{results: []*pingResults{{oper: operEnabled, minRTT: 3, responses: 1, sent: 1}}},
			probe.SuccessResult(3),
		},
		"no reply": {
			agentScript{results: []*pingResults{{oper: operEnabled, sent: 1}, {oper: operCompleted, sent: 1}}},
			probe.FailedResult(),
		},
		"no reply, the test left disabled": {
			agentScript{results: []*pingResults{{oper: operDisabled}, {oper: operDisabled, sent: 1}}},
			probe.FailedResult(),
		},
		"test ended unsent": {
			agentScript{results: []*pingResults{{oper: operCompleted}}},
			probe.RelayFailedResult(),
		},
		"create refused": {
			agentScript{refuse: gosnmp.NotWritable, results: []*pingResults{nil}},
			probe.RelayFailedResult(),
		},
		// A resent create is refused because the first one made the entry, whose test
		// then runs as asked (RFC 2579): its results are the probe's.
		"resent create refused as a duplicate": {
			agentScript{
				refuse:  gosnmp.InconsistentValue,
				results: []*pingResults{{oper: operCompleted, minRTT: 3, responses: 1, sent: 1}},
			},
			probe.SuccessResult(3),
		},
		"no ping MIB": {
			agentScript{noMIB: true, results: []*pingResults{nil}},
			probe.RelayFailedResult(),
		},
		"entry never appears": {
			agentScript{results: []*pingResults{nil}},
			probe.RelayFailedResult(),
		},
		"test outlasts the probe": {
			agentScript{results: []*pingResults{{oper: operEnabled, sent: 1}}},
			probe.RelayTimeoutResult(),
		},
		"agent silent": {
			agentScript{silent: true, results: []*pingResults{nil}},
			probe.RelayTimeoutResult(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := agentPinger(t, startAgent(t, c.script), "192.0.2.1")
			if got := p.Send(t.Context()); got != c.want {
				t.Errorf("Send = %+v, want %+v", got, c.want)
			}
		})
	}
}

// A refused create is followed by a destroy too: the refusal may be of a resent create
// whose first send made the entry.
func TestSNMPProbeDestroysEntryAfterRefusedCreate(t *testing.T) {
	a := startAgent(t, agentScript{refuse: gosnmp.InconsistentValue, results: []*pingResults{nil}})
	p := agentPinger(t, a, "192.0.2.1")
	p.Send(t.Context())

	sets, _ := a.recorded()
	last := pdus([]gosnmp.SnmpPDU{{
		Name:  pingCtlEntry + "." + strconv.Itoa(ctlRowStatus) + p.index,
		Type:  gosnmp.Integer,
		Value: rowDestroy,
	}})

	if len(sets) != 3 || pdus(sets[2]) != last {
		t.Errorf("sent %d SETs, want the entry destroyed after the create", len(sets))
	}
}

// A probe canceled mid-test (a reload or exit) still destroys its entry: one left
// behind holds one of the agent's concurrent tests.
func TestSNMPProbeDestroysEntryWhenCancelled(t *testing.T) {
	a := startAgent(t, agentScript{results: []*pingResults{{oper: operEnabled, sent: 1}}})
	p := agentPinger(t, a, "192.0.2.1")

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	p.Send(ctx)

	sets, _ := a.recorded()
	last := pdus([]gosnmp.SnmpPDU{{
		Name:  pingCtlEntry + "." + strconv.Itoa(ctlRowStatus) + p.index,
		Type:  gosnmp.Integer,
		Value: rowDestroy,
	}})

	if len(sets) != 3 || pdus(sets[2]) != last {
		t.Errorf("sent %d SETs, want the entry destroyed last", len(sets))
	}
}

// A target name is resolved on this host and the agent receives its first address, an
// IPv4-mapped one as IPv4. When it cannot be resolved nothing reaches the agent.
func TestSNMPProbeResolvesTargetHere(t *testing.T) {
	for _, c := range []struct {
		first    string
		addrType int
	}{
		{"2001:db8::5", inetAddressIPv6},
		{"::ffff:192.0.2.5", inetAddressIPv4},
	} {
		a := startAgent(
			t,
			agentScript{results: []*pingResults{{oper: operCompleted, responses: 1, sent: 1}}},
		)
		p := agentPinger(t, a, "target.example")
		p.lookup = func(_ context.Context, network, host string) ([]netip.Addr, error) {
			if network != networkAny || host != "target.example" {
				t.Errorf("lookup(%q, %q)", network, host)
			}

			return []netip.Addr{netip.MustParseAddr(c.first), netip.MustParseAddr("192.0.2.9")}, nil
		}

		p.Send(t.Context())

		want := netip.MustParseAddr(c.first).Unmap()

		sets, _ := a.recorded()
		if len(sets) < 2 {
			t.Fatalf("%s: the agent received %d SETs, want the create", c.first, len(sets))
		}

		addr, ok := sets[1][1].Value.([]byte)
		if !ok || sets[1][0].Value != c.addrType || !bytes.Equal(addr, want.AsSlice()) {
			t.Errorf("%s: the agent was not given %s", c.first, want)
		}
	}

	a := startAgent(t, agentScript{results: []*pingResults{nil}})
	p := agentPinger(t, a, "target.example")
	p.lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("no such host")
	}

	if got := p.Send(t.Context()); got != probe.UnavailableResult() {
		t.Errorf("unresolved target: Send = %+v, want unavailable", got)
	}

	if sets, _ := a.recorded(); len(sets) != 0 {
		t.Errorf("unresolved target: the agent received %d SETs", len(sets))
	}
}

// The entry's index is the owner "deadman" then a test name of at most 32 octets, each
// prefixed by its length. The test name is the row's: the same for its pingers across a
// reload (so a probe clears the entry an earlier one left), and different for another
// row or another process.
func TestSNMPIndex(t *testing.T) {
	got := snmpIndex("deadman", "t1")
	if want := ".7.100.101.97.100.109.97.110.2.116.49"; got != want {
		t.Errorf("snmpIndex = %s, want %s", got, want)
	}

	a := startAgent(t, agentScript{results: []*pingResults{nil}})
	p, again := agentPinger(t, a, "192.0.2.1"), agentPinger(t, a, "192.0.2.1")

	plan := compiled(t, probe.Spec{
		Addr:   "192.0.2.1",
		Params: probe.SNMP{Host: "127.0.0.1", Community: "public"},
	})

	other, err := New(plan, "row#2")
	if err != nil {
		t.Fatal(err)
	}

	q, ok := other.(*snmpPinger)
	if !ok {
		t.Fatalf("New returned %T", other)
	}

	owner := snmpIndex(snmpOwner, "")
	owner = owner[:len(owner)-len(".0")]

	test, ok := strings.CutPrefix(p.index, owner+".")
	n, _, _ := strings.Cut(test, ".")

	length, err := strconv.Atoi(n)
	if !ok || err != nil || length > 32 {
		t.Errorf("index %s: want owner %q and a test name of at most 32 octets", p.index, snmpOwner)
	}

	if p.index != again.index {
		t.Error("the row's pingers name different entries")
	}

	if p.index == q.index ||
		snmpTestName("instance-a", "row#1") == snmpTestName("instance-b", "row#1") {
		t.Error("two rows, or two processes, share an entry")
	}
}

// A separate process monitoring the same row must never create or destroy this
// process's entry. A subprocess also catches an instance ID generated per host.
func TestSNMPProcessesUseSeparateEntries(t *testing.T) {
	const (
		childEnv    = "DEADMAN_TEST_SNMP_CHILD"
		indexPrefix = "SNMP_INDEX="
	)

	p := testSNMPPinger(t, "192.0.2.1", "row#1")
	if os.Getenv(childEnv) == "1" {
		t.Log(indexPrefix + p.index)

		return
	}

	t.Setenv(childEnv, "1")

	// Reuse this binary so the child exercises the same build and needs neither a Go
	// installation nor access to the module's source directory (including WSL shares).
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		executable,
		"-test.count=1",
		"-test.run=^TestSNMPProcessesUseSeparateEntries$",
		"-test.v=true",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child process: %v\n%s", err, out)
	}

	_, suffix, found := strings.Cut(string(out), indexPrefix)
	index, _, _ := strings.Cut(suffix, "\n")
	index = strings.TrimSpace(index)

	if !found || index == "" || index == p.index {
		t.Fatalf(
			"child process index = %q, want a nonempty index different from %q",
			index,
			p.index,
		)
	}
}

// A row's probes share its agent entry, so one probe waits for the last one to be done
// with it: across a reload, the canceled probe of the old generation destroys the entry
// before the new generation's first probe creates it again, never after. gosnmp waits
// out a request in flight whatever its context, so against a slow agent the canceled
// probe's destroy would otherwise land after the new probe's create.
func TestSNMPProbesOfOneEntryTakeTurns(t *testing.T) {
	a := startAgent(t, agentScript{
		results:   []*pingResults{{oper: operEnabled, sent: 1}},
		pollDelay: 60 * time.Millisecond,
	})
	retired, fresh := agentPinger(t, a, "192.0.2.1"), agentPinger(t, a, "192.0.2.1")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		retired.Send(ctx)
	}()

	// Cancel the retired probe while its first poll waits for the agent.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	for a.polled() == 0 {
		select {
		case <-timer.C:
			t.Fatal("the first probe never polled its entry")
		case <-ticker.C:
		}
	}

	cancel()
	fresh.Send(t.Context())
	<-done

	sets, _ := a.recorded()

	// destroy (clear), create, destroy (cleanup) of the retired probe, then the same of
	// the fresh one: a create is the only SET of more than one varbind.
	kinds := make([]string, len(sets))
	for i, set := range sets {
		kinds[i] = "destroy"
		if len(set) > 1 {
			kinds[i] = "create"
		}
	}

	want := []string{"destroy", "create", "destroy", "destroy", "create", "destroy"}
	if !slices.Equal(kinds, want) {
		t.Errorf("SETs = %v, want the two probes' %v in turn", kinds, want)
	}
}

// An entry's lock is held by one probe at a time, gives up with the waiter's context, and
// is forgotten once nobody holds or waits for it.
func TestEntryLocks(t *testing.T) {
	locks := entryLocks{held: make(map[string]*entryLock)}

	release, ok := locks.acquire(t.Context(), "a")
	if !ok {
		t.Fatal("a free lock was not taken")
	}

	short, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	if _, taken := locks.acquire(short, "a"); taken {
		t.Fatal("a held lock was taken again")
	}

	other, ok := locks.acquire(t.Context(), "b")
	if !ok {
		t.Fatal("another key's lock was held up")
	}

	other()
	release()

	again, ok := locks.acquire(t.Context(), "a")
	if !ok {
		t.Fatal("a released lock was not taken")
	}

	again()

	if len(locks.held) != 0 {
		t.Errorf("locks left behind: %v", locks.held)
	}
}

func TestSNMPRefusedClearCannotReuseStaleSuccess(t *testing.T) {
	agent := startAgent(t, agentScript{
		refuseDestroy: gosnmp.NotWritable,
		refuse:        gosnmp.InconsistentValue,
		results:       []*pingResults{{oper: operCompleted, minRTT: 3, responses: 1, sent: 1}},
	})

	result := agentPinger(t, agent, "192.0.2.1").Send(t.Context())
	if result.Code != probe.RelayFailed {
		t.Fatalf("stale entry result = %+v", result)
	}

	sets, _ := agent.recorded()
	if len(sets) != 1 || agent.polled() != 0 {
		t.Fatal("probe continued after clear refusal")
	}
}
