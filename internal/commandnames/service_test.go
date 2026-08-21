package commandnames_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/commandnames"
)

// fakeSource is a Source whose probe and scan are scripted. It counts both,
// because "a cache hit starts no second scan" is a claim about a COUNT and
// cannot be asserted any other way.
type fakeSource struct {
	mu       sync.Mutex
	identity commandnames.Identity
	probe    commandnames.Probe
	probeErr error
	scan     commandnames.Scan
	scanErr  error
	probes   int
	scans    int
	// scanGate, when non-nil, blocks every scan until it is closed. It is
	// how the singleflight assertion observes two callers inside one scan
	// without depending on a duration.
	scanGate chan struct{}
	scanning chan struct{}
}

func (f *fakeSource) Identity() commandnames.Identity { return f.identity }

func (f *fakeSource) Probe(context.Context) (commandnames.Probe, error) {
	f.mu.Lock()
	f.probes++
	p, err := f.probe, f.probeErr
	f.mu.Unlock()
	return p, err
}

func (f *fakeSource) Scan(_ context.Context, _ commandnames.Probe) (commandnames.Scan, error) {
	f.mu.Lock()
	f.scans++
	gate, scanning := f.scanGate, f.scanning
	s, err := f.scan, f.scanErr
	f.mu.Unlock()
	if scanning != nil {
		select {
		case scanning <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	return s, err
}

func (f *fakeSource) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes, f.scans
}

func (f *fakeSource) setScan(s commandnames.Scan, err error) {
	f.mu.Lock()
	f.scan, f.scanErr = s, err
	f.mu.Unlock()
}

func (f *fakeSource) setProbe(p commandnames.Probe) {
	f.mu.Lock()
	f.probe = p
	f.mu.Unlock()
}

func stamps(pairs ...string) []commandnames.DirStamp {
	out := make([]commandnames.DirStamp, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, commandnames.DirStamp{Dir: pairs[i], Stamp: pairs[i+1]})
	}
	return out
}

func newSource() *fakeSource {
	return &fakeSource{
		identity: commandnames.Identity{Route: "ssh:user@host:22", Generation: "v39"},
		probe: commandnames.Probe{
			User:        "user",
			ShellFamily: "bash",
			Path:        "/usr/bin:/bin",
			Stamps:      stamps("/usr/bin", "1000", "/bin", "2000"),
			Stamped:     true,
		},
		scan: commandnames.Scan{Names: []string{"ls", "grep"}},
	}
}

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newService(now func() time.Time) *commandnames.Service {
	return commandnames.New(now, nil)
}

// ── assertion 35, first clause ────────────────────────────────────────────
//
// A cache hit starts NO second scan. Ten tabs to one host is one scan, and
// the count is what says so.
func TestNames_TenSessionsToOneHostRunOneScan(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()

	for i := 0; i < 10; i++ {
		res := svc.Names(context.Background(), src)
		if res.State != commandnames.StateReady {
			t.Fatalf("session %d: state = %q, want ready", i, res.State)
		}
		if strings.Join(res.Names, ",") != "grep,ls" {
			t.Fatalf("session %d: names = %v", i, res.Names)
		}
	}
	probes, scans := src.counts()
	if scans != 1 {
		t.Fatalf("ten sessions ran %d scans; the shared enumeration must run once", scans)
	}
	if probes != 10 {
		t.Fatalf("probes = %d, want one per session (the invalidation probe is the per-session cost)", probes)
	}
}

// Any change to a PATH directory's stamp rescans — that is the invalidation
// mechanism, and it is an event and not a clock.
func TestNames_AChangedDirectoryStampRescans(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()

	svc.Names(context.Background(), src)
	src.setProbe(commandnames.Probe{
		User: "user", ShellFamily: "bash", Path: "/usr/bin:/bin",
		Stamps:  stamps("/usr/bin", "1001", "/bin", "2000"), // a package was installed
		Stamped: true,
	})
	src.setScan(commandnames.Scan{Names: []string{"ls", "grep", "newtool"}}, nil)

	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateReady {
		t.Fatalf("state = %q", res.State)
	}
	if _, scans := src.counts(); scans != 2 {
		t.Fatalf("scans = %d, want 2 — a changed stamp must rescan", scans)
	}
	if strings.Join(res.Names, ",") != "grep,ls,newtool" {
		t.Fatalf("names = %v", res.Names)
	}
}

// A change to PATH itself is already in the key: it is a different cache
// entry, not an invalidation.
func TestNames_AChangedPathIsADifferentKey(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	svc.Names(context.Background(), src)

	src.setProbe(commandnames.Probe{
		User: "user", ShellFamily: "bash", Path: "/opt/bin:/usr/bin:/bin",
		Stamps:  stamps("/opt/bin", "5", "/usr/bin", "1000", "/bin", "2000"),
		Stamped: true,
	})
	svc.Names(context.Background(), src)
	if _, scans := src.counts(); scans != 2 {
		t.Fatalf("scans = %d, want 2 — a different PATH is a different key", scans)
	}

	// The first PATH is still cached: going back does not rescan.
	src.setProbe(commandnames.Probe{
		User: "user", ShellFamily: "bash", Path: "/usr/bin:/bin",
		Stamps:  stamps("/usr/bin", "1000", "/bin", "2000"),
		Stamped: true,
	})
	svc.Names(context.Background(), src)
	if _, scans := src.counts(); scans != 2 {
		t.Fatalf("scans = %d, want still 2 — the first key's entry survives", scans)
	}
}

// Every component of the key defends against a specific confusion. Each one
// alone must be enough to separate two entries.
func TestNames_EveryKeyComponentSeparatesTheCache(t *testing.T) {
	base := newSource()
	cases := []struct {
		what  string
		apply func(*fakeSource)
	}{
		{"route", func(s *fakeSource) { s.identity.Route = "ssh:user@other:22" }},
		{"generation", func(s *fakeSource) { s.identity.Generation = "v40" }},
		{"remote user", func(s *fakeSource) {
			p := s.probe
			p.User = "root"
			s.probe = p
		}},
		{"shell family", func(s *fakeSource) {
			p := s.probe
			p.ShellFamily = "zsh"
			s.probe = p
		}},
		{"PATH", func(s *fakeSource) {
			p := s.probe
			p.Path = "/usr/bin"
			p.Stamps = stamps("/usr/bin", "1000")
			s.probe = p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
			svc := newService(clock.now)
			first := newSource()
			svc.Names(context.Background(), first)

			second := newSource()
			tc.apply(second)
			svc.Names(context.Background(), second)
			if _, scans := second.counts(); scans != 1 {
				t.Fatalf("a differing %s was served from the first entry's cache", tc.what)
			}
		})
	}
	_ = base
}

// ── assertion 35, second clause ───────────────────────────────────────────
//
// A timeout publishes NO partial snapshot. Nothing is cached, and the next
// session scans again rather than being served a half-enumeration.
func TestNames_ATimeoutPublishesNoPartialSnapshot(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	src.setScan(commandnames.Scan{Names: []string{"partial"}}, commandnames.ErrScanDeadline)

	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateTimedOut {
		t.Fatalf("state = %q, want timed-out", res.State)
	}
	if len(res.Names) != 0 {
		t.Fatalf("a timed-out scan published %v", res.Names)
	}

	src.setScan(commandnames.Scan{Names: []string{"ls"}}, nil)
	res = svc.Names(context.Background(), src)
	if res.State != commandnames.StateReady {
		t.Fatalf("state = %q after a good scan", res.State)
	}
	if _, scans := src.counts(); scans != 2 {
		t.Fatalf("scans = %d — a timeout must leave nothing cached", scans)
	}
}

// A scan that fails for any other reason is `failed`, and it too caches
// nothing. The two are distinct states because they are distinct facts.
func TestNames_AFailedScanIsDistinctFromATimeout(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	src.setScan(commandnames.Scan{}, errors.New("remote host refused the exec"))

	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	if res.Reason == "" {
		t.Fatalf("a failed scan named no reason")
	}
	if len(res.Names) != 0 {
		t.Fatalf("a failed scan published %v", res.Names)
	}
}

// ── assertion 35, third clause ────────────────────────────────────────────
//
// A cache older than its bound is NOT served as current. Within the bound a
// snapshot whose rescan failed is served as `stale`, with its age; past the
// bound it is not served at all.
func TestNames_AStaleCacheIsServedWithinItsBoundAndNotBeyond(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	if res := svc.Names(context.Background(), src); res.State != commandnames.StateReady {
		t.Fatalf("first: %q", res.State)
	}

	// A package changed, so the next session rescans — and the rescan fails.
	src.setProbe(commandnames.Probe{
		User: "user", ShellFamily: "bash", Path: "/usr/bin:/bin",
		Stamps:  stamps("/usr/bin", "1001", "/bin", "2000"),
		Stamped: true,
	})
	src.setScan(commandnames.Scan{}, commandnames.ErrScanDeadline)
	clock.advance(30 * time.Minute)

	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateStale {
		t.Fatalf("state = %q, want stale — a usable snapshot inside its bound is served", res.State)
	}
	if strings.Join(res.Names, ",") != "grep,ls" {
		t.Fatalf("stale names = %v", res.Names)
	}
	if res.Age != 30*time.Minute {
		t.Fatalf("age = %v, want 30m — the age is what bounds the claim", res.Age)
	}

	// Past the backstop the same snapshot is no longer claimed as current.
	clock.advance(31 * time.Minute)
	res = svc.Names(context.Background(), src)
	if res.State != commandnames.StateTimedOut {
		t.Fatalf("state = %q past the backstop, want the failure's own state", res.State)
	}
	if len(res.Names) != 0 {
		t.Fatalf("a snapshot past its backstop was still served: %v", res.Names)
	}
}

// The backstop is a backstop, not the mechanism: an entry whose stamps have
// not moved is still rescanned once it is older than the bound — that is
// what defends against a filesystem reporting mtime unreliably.
func TestNames_AnEntryPastTheBackstopIsRescannedEvenWithUnchangedStamps(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	svc.Names(context.Background(), src)

	clock.advance(59 * time.Minute)
	svc.Names(context.Background(), src)
	if _, scans := src.counts(); scans != 1 {
		t.Fatalf("scans = %d inside the backstop; unchanged stamps must not rescan", scans)
	}

	clock.advance(2 * time.Minute)
	if res := svc.Names(context.Background(), src); res.State != commandnames.StateReady {
		t.Fatalf("state = %q", res.State)
	}
	if _, scans := src.counts(); scans != 2 {
		t.Fatalf("scans = %d past the backstop; the age bound must force one rescan", scans)
	}
}

// Concurrent sessions arriving on a cold cache share ONE scan. Without this
// the "ten tabs, one scan" claim holds only when the tabs are opened slowly.
func TestNames_ConcurrentColdSessionsShareOneScan(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	src.scanGate = make(chan struct{})
	src.scanning = make(chan struct{}, 1)

	const n = 8
	results := make(chan commandnames.Result, n)
	for i := 0; i < n; i++ {
		go func() { results <- svc.Names(context.Background(), src) }()
	}
	<-src.scanning // a scan is in flight; every other caller must join it
	// Every caller has to be inside Names before the gate opens, or the
	// test proves nothing about the ones that had not arrived yet.
	waitUntil(t, "all callers to have probed", func() bool {
		probes, _ := src.counts()
		return probes == n
	})
	close(src.scanGate)

	for i := 0; i < n; i++ {
		res := <-results
		if res.State != commandnames.StateReady {
			t.Fatalf("state = %q", res.State)
		}
	}
	if _, scans := src.counts(); scans != 1 {
		t.Fatalf("scans = %d, want 1 — concurrent cold callers must singleflight", scans)
	}
}

// A probe that fails cannot compute a key, so it cannot look anything up.
// It still must not throw away a snapshot the identity already has: within
// the backstop the last one is served as stale, past it the failure stands.
func TestNames_AFailedProbeServesTheLastSnapshotAsStale(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	svc.Names(context.Background(), src)

	src.mu.Lock()
	src.probeErr = errors.New("connection lost")
	src.mu.Unlock()
	clock.advance(10 * time.Minute)

	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateStale {
		t.Fatalf("state = %q, want stale", res.State)
	}
	if strings.Join(res.Names, ",") != "grep,ls" {
		t.Fatalf("names = %v", res.Names)
	}

	clock.advance(51 * time.Minute)
	res = svc.Names(context.Background(), src)
	if res.State != commandnames.StateFailed {
		t.Fatalf("state = %q past the backstop, want failed", res.State)
	}
	if len(res.Names) != 0 {
		t.Fatalf("served %v past the backstop", res.Names)
	}
}

// The shared result is bounded — 8,192 names and 64 KiB encoded — and says
// so when it had to cut, rather than presenting a prefix as the whole set.
func TestNames_TheSharedResultIsBounded(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	many := make([]string, 20000)
	for i := range many {
		many[i] = fmt.Sprintf("cmd%06d", i)
	}
	src.setScan(commandnames.Scan{Names: many}, nil)

	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateReady {
		t.Fatalf("state = %q", res.State)
	}
	if len(res.Names) > commandnames.MaxSharedNames {
		t.Fatalf("names = %d, over the %d bound", len(res.Names), commandnames.MaxSharedNames)
	}
	total := 0
	for _, n := range res.Names {
		total += len(n) + 1
	}
	if total > commandnames.MaxSharedBytes {
		t.Fatalf("encoded %d bytes, over the %d bound", total, commandnames.MaxSharedBytes)
	}
	if !res.Truncated {
		t.Fatalf("a cut result did not report itself cut")
	}
}

// The invalidation probe is itself bounded: at most 32 directory stamps ride
// in the key, however long the PATH is.
func TestNames_TheProbeIsBoundedToThirtyTwoDirectories(t *testing.T) {
	if commandnames.MaxPathDirs != 32 {
		t.Fatalf("MaxPathDirs = %d, want 32", commandnames.MaxPathDirs)
	}
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	over := make([]string, 0, 200)
	for i := 0; i < 100; i++ {
		over = append(over, fmt.Sprintf("/d%d", i), fmt.Sprintf("%d", i))
	}
	src.setProbe(commandnames.Probe{User: "u", ShellFamily: "bash", Path: "p", Stamps: stamps(over...), Stamped: true})
	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateReady {
		t.Fatalf("state = %q", res.State)
	}
	// A 33rd directory changing beyond the bound cannot invalidate, and the
	// service must not pretend otherwise: it keeps only what it compares.
	if got := svc.StampsHeld(src.Identity()); got > commandnames.MaxPathDirs {
		t.Fatalf("held %d stamps, over the %d bound", got, commandnames.MaxPathDirs)
	}
}

func waitUntil(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("never observed: %s", what)
}

// A far side that cannot stamp its PATH directories gets a snapshot that is
// never reported current. Two `unstamped` stamps compare equal forever, so
// calling such an entry `ready` would be a claim nothing could ever falsify;
// it is served as `stale`, with its age, which a person can act on.
func TestNames_AnUnstampableFarSideIsServedStaleRatherThanReady(t *testing.T) {
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	svc := newService(clock.now)
	src := newSource()
	src.setProbe(commandnames.Probe{
		User: "user", ShellFamily: "bash", Path: "/usr/bin:/bin",
		Stamps:  stamps("/usr/bin", "unstamped", "/bin", "unstamped"),
		Stamped: false,
	})

	if res := svc.Names(context.Background(), src); res.State != commandnames.StateReady {
		t.Fatalf("the first scan is a real scan: %q", res.State)
	}
	clock.advance(5 * time.Minute)
	res := svc.Names(context.Background(), src)
	if res.State != commandnames.StateStale {
		t.Fatalf("state = %q, want stale — nothing can invalidate an unstamped entry", res.State)
	}
	if res.Age != 5*time.Minute {
		t.Fatalf("age = %v", res.Age)
	}
	if _, scans := src.counts(); scans != 1 {
		t.Fatalf("scans = %d — a stale hit still starts no second scan", scans)
	}
}
