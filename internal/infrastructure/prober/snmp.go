package prober

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// The DISMAN-PING-MIB (RFC 4560) objects snmpPinger writes and reads, numbered as
// Net-SNMP's `snmptranslate -On DISMAN-PING-MIB::<name>` gives them.
const (
	pingCtlEntry     = ".1.3.6.1.2.1.80.1.2.1"
	pingResultsEntry = ".1.3.6.1.2.1.80.1.3.1"
	pingIcmpEcho     = ".1.3.6.1.2.1.80.3.1"

	ctlTargetAddressType = 3
	ctlTargetAddress     = 4
	ctlAdminStatus       = 8
	ctlStorageType       = 12
	ctlType              = 16
	ctlRowStatus         = 23

	resultsOperStatus     = 1
	resultsMinRtt         = 4
	resultsProbeResponses = 7
	resultsSentProbes     = 8
)

// Values of those objects: InetAddressType, pingCtlAdminStatus, StorageType, RowStatus
// and pingResultsOperStatus.
const (
	inetAddressIPv4 = 1
	inetAddressIPv6 = 2
	adminEnabled    = 1
	storageVolatile = 2
	rowCreateAndGo  = 4
	rowDestroy      = 6
	operDisabled    = 2
	operCompleted   = 3
)

const (
	// snmpPort is the agent's port.
	snmpPort = 161
	// snmpOwner is the pingCtlOwnerIndex of every entry deadman creates, which lets the
	// agent's operator tell them from other managers' entries.
	snmpOwner = "deadman"
	// snmpRetries is enough resends that the probe's deadline, not the count, ends them.
	snmpRetries = 5
)

// The bounds of one probe: the whole exchange, each request (resent within the whole),
// the removal of the entry, which outlives a canceled probe, and the polls of its
// results, which back off from the first interval to the longest.
const (
	snmpTimeout        = 5 * time.Second
	snmpRequestTimeout = time.Second
	snmpCleanupTimeout = time.Second
	snmpPollFirst      = 50 * time.Millisecond
	snmpPollMax        = 500 * time.Millisecond
)

// snmpTiming holds those bounds, so a test can shorten them.
type snmpTiming struct {
	total, request, cleanup time.Duration
	pollFirst, pollMax      time.Duration
}

// resultColumns are the pingResultsEntry columns each poll reads, in the order outcome
// takes them.
var resultColumns = []int{
	resultsOperStatus,
	resultsMinRtt,
	resultsProbeResponses,
	resultsSentProbes,
}

// snmpNumberTypes are the types an agent may encode those columns in.
var snmpNumberTypes = []gosnmp.Asn1BER{
	gosnmp.Integer,
	gosnmp.Gauge32,
	gosnmp.Uinteger32,
	gosnmp.Counter32,
}

// snmpPinger asks an SNMP agent to ping the target by the remote ping of RFC 4560
// (DISMAN-PING-MIB) over SNMPv2c. Each probe creates a pingCtlEntry, polls its
// pingResultsEntry until the test ends and destroys the entry, as Net-SNMP's snmpping
// does. It writes the columns snmpping writes (the target, enabled, volatile,
// createAndGo; one probe of no extra data by the MIB's defaults) and pingCtlType, which
// the MIB defaults to pingIcmpEcho but some agents (H3C, Huawei) require.
type snmpPinger struct {
	dest      probe.Destination // a name is resolved on this host each probe.
	agent     string            // relay=: the agent's host.
	port      uint16
	community string
	// index is the entry's pingCtlTable index (see snmpTestName): the same for the row
	// across reloads within this process, so a probe clears the entry an earlier one left.
	index  string
	lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
	timing snmpTiming
}

func newSNMPPinger(dest probe.Destination, s probe.SNMP, row string) (probe.Pinger, error) {
	return &snmpPinger{
		dest:      dest,
		agent:     s.Host,
		port:      snmpPort,
		community: s.Community,
		index:     snmpIndex(snmpOwner, snmpTestName(snmpInstance, row)),
		lookup:    net.DefaultResolver.LookupNetIP,
		timing: snmpTiming{
			total:     snmpTimeout,
			request:   snmpRequestTimeout,
			cleanup:   snmpCleanupTimeout,
			pollFirst: snmpPollFirst,
			pollMax:   snmpPollMax,
		},
	}, nil
}

// snmpInstance separates this process's entries from every other monitor, including
// ones with the same hostname or PID (for example in separate PID namespaces). Keep it
// for the process's lifetime so reloads can reuse and clean up the same entries.
var snmpInstance = rand.Text()

// snmpTestName is the pingCtlTestName of a row's entry: a digest of the process's
// instance ID and the row's ID, within the 32 octets the MIB allows. It is stable across
// probes and reloads, so a later probe clears an entry this process could not destroy.
// A new process uses new names and never destroys another process's tests.
// Entries left by an unclean exit therefore need cleanup on the agent: a new process
// cannot reclaim them without risking deletion of another live process's tests.
func snmpTestName(instance, row string) string {
	sum := sha256.Sum256([]byte(instance + "\x00" + row))

	return hex.EncodeToString(sum[:snmpTestNameBytes])
}

// snmpTestNameBytes is how much of the digest names a test: its hex spelling is 32
// octets, the most pingCtlTestName holds.
const snmpTestNameBytes = 16

// snmpIndex is the pingCtlTable index of an entry: pingCtlOwnerIndex and then
// pingCtlTestName, each a string prefixed by its length (neither is IMPLIED).
func snmpIndex(owner, test string) string {
	var b []byte

	for _, s := range []string{owner, test} {
		b = fmt.Appendf(b, ".%d", len(s))

		for _, c := range []byte(s) {
			b = fmt.Appendf(b, ".%d", c)
		}
	}

	return string(b)
}

// Send runs one remote ping. Failures of the agent are relay results: t when it does not
// answer in time, s when it cannot be reached or refuses or fails the test.
func (p *snmpPinger) Send(ctx context.Context) probe.Result {
	ctx, cancel := context.WithTimeout(ctx, p.timing.total)
	defer cancel()

	// Released last, after the entry is destroyed (defers run in reverse).
	release, ok := snmpEntries.acquire(ctx, p.entryKey())
	if !ok {
		// An earlier probe of the entry has not finished with it in this probe's time.
		return probe.RelayTimeoutResult()
	}
	defer release()

	target, err := p.target(ctx)
	if err != nil {
		// Nothing reached the agent: this host could not resolve the target.
		return probe.UnavailableResult()
	}

	agent := p.session(ctx)

	err = agent.Connect()
	if err != nil {
		return unanswered(ctx, err)
	}

	defer func() { _ = agent.Close() }()

	// A stale entry must be removed before a duplicate create can safely be adopted.
	// RowStatus destroy of an absent row succeeds too (RFC 2579).
	cleared, err := agent.Set([]gosnmp.SnmpPDU{p.ctl(ctlRowStatus, gosnmp.Integer, rowDestroy)})
	if err != nil {
		return unanswered(ctx, err)
	}

	if cleared.Error != gosnmp.NoError {
		return probe.RelayFailedResult()
	}

	created, err := agent.Set(p.create(target))
	// Destroy the entry whatever the answer: the agent may have made it and lost its
	// answer, or refused a resent create as a duplicate of one it made.
	defer p.destroy(ctx, agent)

	if err != nil {
		return unanswered(ctx, err)
	}

	// createAndGo is not idempotent: when the answer to it is lost, gosnmp resends it,
	// and the agent refuses the resend as inconsistentValue because the entry it made
	// exists (RFC 2579). The entry was cleared above, so an existing one is this probe's
	// test, running: its results tell. Any other refusal is the agent's.
	if created.Error != gosnmp.NoError && created.Error != gosnmp.InconsistentValue {
		return probe.RelayFailedResult()
	}

	return p.await(ctx, agent)
}

// entryKey names the agent entry the pinger's probes use: the agent's, and the entry's
// index there.
func (p *snmpPinger) entryKey() string {
	return net.JoinHostPort(p.agent, strconv.Itoa(int(p.port))) + p.index
}

// snmpEntries serializes the probes of one agent entry within this process. A row's
// pingers share its entry (see snmpTestName), so across a reload the retired
// generation's canceled probe may still be destroying the entry as the new generation's
// first probe creates it; each probe takes the entry first, so that destroy can never
// remove the new probe's test.
var snmpEntries = entryLocks{held: make(map[string]*entryLock)}

// entryLocks is a lock per key, which exists while someone holds or waits for it.
type entryLocks struct {
	mu   sync.Mutex
	held map[string]*entryLock
}

type entryLock struct {
	sem   chan struct{}
	users int // holders and waiters.
}

// acquire takes key's lock, or reports false when ctx ends first. The returned func
// releases it.
func (l *entryLocks) acquire(ctx context.Context, key string) (func(), bool) {
	l.mu.Lock()

	e := l.held[key]
	if e == nil {
		e = &entryLock{sem: make(chan struct{}, 1)}
		l.held[key] = e
	}

	e.users++
	l.mu.Unlock()

	select {
	case e.sem <- struct{}{}:
		return func() {
			<-e.sem
			l.leave(key, e)
		}, true
	case <-ctx.Done():
		l.leave(key, e)

		return nil, false
	}
}

// leave drops one user of key's lock, forgetting the lock when none is left.
func (l *entryLocks) leave(key string, e *entryLock) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e.users--
	if e.users == 0 {
		delete(l.held, key)
	}
}

// target is the address the agent pings: the target's, or the first one its name
// resolves to here. The agent receives only the address bytes, so an IPv4-mapped
// address goes as IPv4.
func (p *snmpPinger) target(ctx context.Context) (netip.Addr, error) {
	if a, ok := p.dest.IP(); ok {
		return a, nil
	}

	name, _ := p.dest.Name()

	addrs, err := p.lookup(ctx, networkAny, name)
	if err != nil {
		return netip.Addr{}, err
	}

	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("snmp: no addresses for %s", name)
	}

	return addrs[0].Unmap(), nil
}

func (p *snmpPinger) session(ctx context.Context) *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{
		Target:    p.agent,
		Port:      p.port,
		Transport: "udp",
		Community: p.community,
		Version:   gosnmp.Version2c,
		Timeout:   p.timing.request,
		Retries:   snmpRetries,
		Context:   ctx,
		MaxOids:   gosnmp.MaxOids,
	}
}

// create is the SET that makes the entry and starts its test in one PDU.
func (p *snmpPinger) create(target netip.Addr) []gosnmp.SnmpPDU {
	addrType := inetAddressIPv6
	if target.Is4() {
		addrType = inetAddressIPv4
	}

	return []gosnmp.SnmpPDU{
		p.ctl(ctlTargetAddressType, gosnmp.Integer, addrType),
		p.ctl(ctlTargetAddress, gosnmp.OctetString, target.AsSlice()),
		p.ctl(ctlAdminStatus, gosnmp.Integer, adminEnabled),
		p.ctl(ctlStorageType, gosnmp.Integer, storageVolatile),
		p.ctl(ctlType, gosnmp.ObjectIdentifier, pingIcmpEcho),
		p.ctl(ctlRowStatus, gosnmp.Integer, rowCreateAndGo),
	}
}

// ctl is a varbind of the column of the entry's pingCtlEntry.
func (p *snmpPinger) ctl(column int, typ gosnmp.Asn1BER, value any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{
		Name:  pingCtlEntry + "." + strconv.Itoa(column) + p.index,
		Type:  typ,
		Value: value,
	}
}

// destroy removes the entry, even after ctx ended (a reload or exit cancels the probe):
// an entry left behind holds one of the agent's concurrent tests.
func (p *snmpPinger) destroy(ctx context.Context, agent *gosnmp.GoSNMP) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.timing.cleanup)
	defer cancel()

	agent.Context = ctx
	// Best-effort: a failed destroy leaves nothing to retry, and the agent ages the entry out.
	_, err := agent.Set([]gosnmp.SnmpPDU{p.ctl(ctlRowStatus, gosnmp.Integer, rowDestroy)})
	discard(err)
}

// await polls the entry's results until its test ends. A probe that runs out of time,
// between polls or during one, reports what the last answered poll showed.
func (p *snmpPinger) await(ctx context.Context, agent *gosnmp.GoSNMP) probe.Result {
	oids := make([]string, 0, len(resultColumns))
	for _, c := range resultColumns {
		oids = append(oids, pingResultsEntry+"."+strconv.Itoa(c)+p.index)
	}

	pending := probe.RelayTimeoutResult()

	for wait := p.timing.pollFirst; ; wait = min(2*wait, p.timing.pollMax) {
		res, err := agent.Get(oids)
		if err != nil {
			// gosnmp reads until ctx's deadline and may report it before ctx does.
			if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				return pending
			}

			return unanswered(ctx, err)
		}

		r, ended := outcome(res)
		if ended {
			return r
		}

		pending = r

		select {
		case <-ctx.Done():
			return pending
		case <-time.After(wait):
		}
	}
}

// outcome reads one poll of the results. Once the test has ended, or the agent failed
// the poll, it returns the probe's result and true. Until then it returns what a probe
// ending now reports: t while the test runs, s while the entry the agent accepted is
// missing.
func outcome(res *gosnmp.SnmpPacket) (probe.Result, bool) {
	row, missing, err := readResults(res)

	switch {
	case err != nil:
		return probe.RelayFailedResult(), true
	case missing:
		return probe.RelayFailedResult(), false
	default:
		return row.outcome()
	}
}

// errBadResults is a poll the agent failed or answered with something other than the
// entry's results columns (no such object when it lacks the MIB, say).
var errBadResults = errors.New("snmp: the agent did not answer with the ping results")

// resultsRow is one poll of the columns of resultColumns.
type resultsRow struct{ oper, minRTT, responses, sent int64 }

// readResults reads a poll of the entry's results, or reports that the entry is missing:
// the agent has not made its results yet.
func readResults(res *gosnmp.SnmpPacket) (resultsRow, bool, error) {
	if res.Error != gosnmp.NoError || len(res.Variables) != len(resultColumns) {
		return resultsRow{}, false, errBadResults
	}

	values := make([]int64, 0, len(resultColumns))

	for _, v := range res.Variables {
		if v.Type == gosnmp.NoSuchInstance {
			return resultsRow{}, true, nil
		}

		n, ok := snmpNumber(v)
		if !ok {
			return resultsRow{}, false, errBadResults
		}

		values = append(values, n)
	}

	return resultsRow{
		oper:      values[0],
		minRTT:    values[1],
		responses: values[2],
		sent:      values[3],
	}, false, nil
}

// outcome is the probe's result once the test has ended, with true; until then t.
func (r resultsRow) outcome() (probe.Result, bool) {
	switch {
	case r.responses > 0:
		return probe.SuccessResult(float64(r.minRTT)), true
	case r.oper != operCompleted && (r.oper != operDisabled || r.sent == 0):
		// snmpping's rule: a test may stay disabled until it starts, and some agents end
		// one as disabled rather than completed.
		return probe.RelayTimeoutResult(), false
	case r.sent == 0:
		// The agent ended the test without sending a probe.
		return probe.RelayFailedResult(), true
	default:
		return probe.FailedResult(), true
	}
}

// snmpNumber reads a column whatever integer type the agent encoded it in.
func snmpNumber(v gosnmp.SnmpPDU) (int64, bool) {
	if !slices.Contains(snmpNumberTypes, v.Type) {
		return 0, false
	}

	n := gosnmp.ToBigInt(v.Value)

	return n.Int64(), n.IsInt64()
}

// unanswered classifies a request the agent did not answer: t when it timed out, s when
// it could not be sent or the agent's host refused it (an ICMP port unreachable, say).
// gosnmp tells running out of resends from other errors only by its message.
func unanswered(ctx context.Context, err error) probe.Result {
	var ne net.Error

	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &ne) && ne.Timeout() || strings.Contains(err.Error(), "timeout") {
		return probe.RelayTimeoutResult()
	}

	return probe.RelayFailedResult()
}
