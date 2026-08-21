package shellintegration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is the P3 measurement gate (design §7: "N, the count of remote
// operations, is a decision gate, not a blank"). It measures — it does not
// bound. Nothing here changes the publisher's behaviour and nothing here
// asserts a ceiling the source must respect; the ratchet of assertion 30
// ("the measured maximum equals the constant in the source") belongs to the
// implementation dispatch that receives the approved number.
//
// What it does assert is that the numbers reported to that gate are still
// the numbers: measuredMax* below are the maxima observed when the report
// was written, and the final subtest fails the moment either moves.

// measuredMaxPublishCalls / measuredMaxPublishBytes are the maximum FS-seam
// calls and bytes written observed on ANY publish path measured here, with
// the production bundle (four files), an uncontended lock and no
// pre-existing residue. They are the FIXED part of the footprint: the
// scaling terms (bundle file count, entries already present in tmp/ and
// integration/, lock polls) are measured separately by
// TestMeasurePublishScaling and TestMeasureLockLoopCost and are NOT folded
// into these constants.
// measuredMaxPublishBytes has moved twice, and both moves are the ratchet
// working rather than failing. Every byte of both is the launch carrier,
// which is the only bundle file either change touched.
//
// 2026-08-21, first move: it was 57,496 when measured against a tree that had
// the carrier but not stage-1; merging the two grew the launch carrier from
// 7,170 to 14,384 bytes, because it now also emits the terminal bootstrap
// outcome and reads the capability from the inherited descriptor. Neither
// figure could be observed on either branch alone, which is why only the
// merged gate reported it.
//
// 2026-08-21, second move (nocx-m8jwn.9, startup fidelity): 64,710 -> 67,211,
// and the +2,501 is the carrier again, 14,384 -> 16,885. Three things grew
// it: the login banner sshd skips is now emulated in the carrier itself (the
// one point every tier passes through, so it is printed once per bootstrap
// and ~/.hushlogin is honoured); the bash tier's rcfile, which the carrier
// embeds, now sources the system profile a login shell would have read; and
// the zsh tier's transient ZDOTDIR, which the carrier also embeds, now writes
// a .zshenv and a .zprofile beside its .zshrc so the user's own login-phase
// files run in their native phases. The bundle's three generation scripts did
// not move at all: 27,161 / 21,924 / 661.
//
// 2026-08-21, third move (nocx-m8jwn.6, command discovery): 67,211 ->
// 67,208, and this one SHRANK. The shell tiers stopped enumerating the PATH:
// bash asks `compgen -abkA function` where it asked `compgen -c`, and zsh
// dropped `${(k)commands}` from its table union, because that half is now
// computed once per host by internal/commandnames and shared across tabs
// instead of re-run in every session. Three bytes of stripped code, net, in
// the two generation scripts.
//
// 2026-08-21, fourth move (nocx-tyyo, the refused nested ssh): 67,208 ->
// 67,252, +44 across the two generation scripts that have a nested launch.
// Both grew by the same guard: a grant whose bootstrap is EMPTY is the
// protocol's refusal echo, and it is now checked BEFORE the parent suspends
// itself, so the user's own line runs conventionally and exactly once. The
// dead branch each script carried for that case below the suspend was
// removed in the same edit, which is why 44 bytes buys a fix in two tiers.
//
// The CALL counts did not move on any of the four occasions —
// 57/17/49/58/58/63 on every path — so N = 90 is untouched: the bundle
// changed size, not the work. B = 256 KiB still holds, now at 3.81x
// headroom.
const (
	measuredMaxPublishCalls = 63
	measuredMaxPublishBytes = 67252

	// measuredMaxBoundedResidue is the same figure for the worst attempt
	// that is still inside the residue bounds the design asks P3 to enforce
	// (one staging slot, one uncommitted generation, one generation swept).
	// It excludes the lock probes, which are the other scaling term.
	measuredMaxBoundedResidue = 83
)

// countingFS decorates any FS and records, per method, the call count, the
// bytes written and the full ordered trace. It is a decorator, not a second
// harness: wrapped OUTSIDE the existing faultFS
// (newCountingFS(newFaultFS(NewOSFS()))) it counts every call the publisher
// issues, including the one faultFS then fails, and the fault-injection
// tests keep working through it unchanged.
//
// Write, Sync and Close are counted separately from Create: each is its own
// publish boundary (publish_fs.go: "the returned File is the write
// boundary"), and a carrier pays for each one separately on the wire.
type countingFS struct {
	FS
	mu    sync.Mutex
	calls map[string]int
	bytes int64 // bytes written through the seam
	read  int64 // bytes read back through the seam (Verify, manifest, uninstall)
	trace []string

	// onCall, when set, is called with the ordinal of each seam call as it
	// is made. It is how a test makes something happen AT a boundary — a
	// remote host consuming the whole of T inside one operation — without
	// waiting for a duration to pass.
	onCall func(n int)
}

func newCountingFS(inner FS) *countingFS {
	return &countingFS{FS: inner, calls: map[string]int{}}
}

func (c *countingFS) record(kind, path string, n int) {
	c.mu.Lock()
	c.calls[kind]++
	c.bytes += int64(n)
	c.trace = append(c.trace, kind+":"+path)
	ordinal, hook := len(c.trace), c.onCall
	c.mu.Unlock()
	if hook != nil {
		hook(ordinal)
	}
}

// reset zeroes the counters and the trace so a baseline publish that only
// sets the scene is not counted against the operation being measured.
func (c *countingFS) reset() {
	c.mu.Lock()
	c.calls = map[string]int{}
	c.bytes, c.read = 0, 0
	c.trace = nil
	c.mu.Unlock()
}

func (c *countingFS) Lstat(path string) (os.FileInfo, error) {
	c.record("lstat", path, 0)
	return c.FS.Lstat(path)
}

func (c *countingFS) Mkdir(path string, mode os.FileMode) error {
	c.record("mkdir", path, 0)
	return c.FS.Mkdir(path, mode)
}

func (c *countingFS) Create(path string, mode os.FileMode) (File, error) {
	c.record("create", path, 0)
	f, err := c.FS.Create(path, mode)
	if err != nil {
		return nil, err
	}
	return &countingFile{File: f, c: c, path: path}, nil
}

func (c *countingFS) SyncDir(path string) error {
	c.record("syncdir", path, 0)
	return c.FS.SyncDir(path)
}

func (c *countingFS) Rename(src, dst string) error {
	c.record("rename", src+" -> "+dst, 0)
	return c.FS.Rename(src, dst)
}

func (c *countingFS) Remove(path string) error {
	c.record("remove", path, 0)
	return c.FS.Remove(path)
}

func (c *countingFS) ReadDir(path string) ([]os.FileInfo, error) {
	c.record("readdir", path, 0)
	return c.FS.ReadDir(path)
}

func (c *countingFS) ReadFile(path string) ([]byte, error) {
	c.record("readfile", path, 0)
	data, err := c.FS.ReadFile(path)
	c.mu.Lock()
	c.read += int64(len(data))
	c.mu.Unlock()
	return data, err
}

type countingFile struct {
	File
	c    *countingFS
	path string
}

// Write counts the bytes as issued, not as accepted: a carrier pays for a
// write it attempts even when the far side refuses it.
func (w *countingFile) Write(p []byte) (int, error) {
	w.c.record("write", w.path, len(p))
	return w.File.Write(p)
}

func (w *countingFile) Sync() error {
	w.c.record("sync", w.path, 0)
	return w.File.Sync()
}

func (w *countingFile) Close() error {
	w.c.record("close", w.path, 0)
	return w.File.Close()
}

// measuredTrace is one measurement: what a single publisher operation cost
// at the FS seam.
type measuredTrace struct {
	total int
	calls map[string]int
	bytes int64
	read  int64
	ops   []string
}

func (c *countingFS) snapshot() measuredTrace {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := measuredTrace{calls: map[string]int{}, bytes: c.bytes, read: c.read, ops: append([]string(nil), c.trace...)}
	for k, v := range c.calls {
		m.calls[k] = v
		m.total += v
	}
	return m
}

// measuredKinds is every method of FS plus the three File boundaries, in
// the order the report tabulates them.
var measuredKinds = []string{"lstat", "mkdir", "create", "write", "sync", "close", "syncdir", "rename", "remove", "readdir", "readfile"}

func (m measuredTrace) String() string {
	parts := make([]string, 0, len(measuredKinds)+2)
	parts = append(parts, fmt.Sprintf("total=%d", m.total), fmt.Sprintf("bytes=%d", m.bytes))
	if m.read != 0 {
		parts = append(parts, fmt.Sprintf("read=%d", m.read))
	}
	for _, k := range measuredKinds {
		if m.calls[k] != 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, m.calls[k]))
		}
	}
	return strings.Join(parts, " ")
}

// row is the report line for one measured path.
type row struct {
	path string
	m    measuredTrace
}

func logRows(t *testing.T, title string, rows []row) {
	t.Helper()
	t.Logf("--- %s", title)
	for _, r := range rows {
		t.Logf("    %-46s %s", r.path, r.m)
	}
}

// newMeasuredPublisher wires a Publisher over countingFS -> faultFS -> the
// real filesystem, so one stand both counts and injects.
func newMeasuredPublisher(t *testing.T) (*Publisher, string, *countingFS, *faultFS) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	ffs := newFaultFS(NewOSFS())
	cfs := newCountingFS(ffs)
	pub := NewPublisher(testLogger(), cfs, root)
	// Every measurement runs on the injected clock: the lock's K probes
	// must not cost 1.55 s of anybody's afternoon, and no number here may
	// depend on how long a machine took.
	newFakeClock().install(pub)
	return pub, root, cfs, ffs
}

// prodBundle is the bundle the product actually publishes, at a chosen
// version. Measuring the test bundle would understate the byte figure by
// three orders of magnitude.
func prodBundle(version string) Bundle {
	b := launchBundle()
	b.Version = version
	return b
}

// TestMeasurePublishHappyPaths measures the three publish paths that carry
// no fault: first contact, the no-op re-publish, and a real replacement.
func TestMeasurePublishHappyPaths(t *testing.T) {
	rows := measureHappyPaths(t)
	logRows(t, "happy paths (production bundle: 3 generation files + launch carrier)", rows)
}

func measureHappyPaths(t *testing.T) []row {
	t.Helper()
	var rows []row

	// First contact: nothing installed, no ~/.nocx at all.
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("first contact: %v", err)
		}
		rows = append(rows, row{"first contact (nothing installed)", cfs.snapshot()})
	}

	// No-op: the same generation is already committed.
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		cfs.reset()
		res, err := pub.Publish(prodBundle("39"))
		if err != nil {
			t.Fatalf("no-op: %v", err)
		}
		if res.Published {
			t.Fatalf("re-publishing the same version must skip, got %+v", res)
		}
		rows = append(rows, row{"no-op (matching generation committed)", cfs.snapshot()})
	}

	// Replacement: a different generation is committed.
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		cfs.reset()
		res, err := pub.Publish(prodBundle("40"))
		if err != nil {
			t.Fatalf("replacement: %v", err)
		}
		if !res.Published {
			t.Fatalf("a newer version must publish, got %+v", res)
		}
		rows = append(rows, row{"replacement (one older generation kept)", cfs.snapshot()})
	}

	// Replacement over two generations: the third is swept, which is the
	// first path on which cleanup does real removal work.
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		for _, v := range []string{"39", "40"} {
			if _, err := pub.Publish(prodBundle(v)); err != nil {
				t.Fatalf("baseline %s: %v", v, err)
			}
		}
		cfs.reset()
		if _, err := pub.Publish(prodBundle("41")); err != nil {
			t.Fatalf("replacement: %v", err)
		}
		rows = append(rows, row{"replacement (one generation swept)", cfs.snapshot()})
	}
	return rows
}

// TestMeasureTraceOfCanonicalPaths prints the full ordered trace of the two
// canonical publishes, root-relative. The counts above say how much; this
// says of what, which is what a formula for N has to be read off.
func TestMeasureTraceOfCanonicalPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage []string
		want  string
	}{
		{"first contact", nil, "39"},
		{"replacement (one generation swept, launch absent)", []string{"39", "40"}, "41"},
	} {
		pub, root, cfs, _ := newMeasuredPublisher(t)
		for _, v := range tc.stage {
			if _, err := pub.Publish(prodBundle(v)); err != nil {
				t.Fatalf("stage %s: %v", v, err)
			}
		}
		if len(tc.stage) > 0 {
			if err := os.Remove(filepath.Join(root, launchName)); err != nil {
				t.Fatalf("remove launch: %v", err)
			}
		}
		cfs.reset()
		if _, err := pub.Publish(prodBundle(tc.want)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		m := cfs.snapshot()
		t.Logf("--- ordered trace: %s (%s)", tc.name, m)
		for i, op := range m.ops {
			kind, path, _ := strings.Cut(op, ":")
			t.Logf("    %2d  %-8s %s", i+1, kind, strings.ReplaceAll(path, root, "~/.nocx"))
		}
	}
}

// TestMeasurePublishFaultPaths injects a failure at every boundary of every
// happy path, in turn, and measures what the failed attempt cost. The
// enumeration is over the fault-free maximum per method, so every Mkdir,
// Create, Write, Sync, Close, Rename, SyncDir, Remove, Lstat, ReadDir and
// ReadFile of each path fails once.
func TestMeasurePublishFaultPaths(t *testing.T) {
	rows := measureFaultPaths(t)
	logRows(t, "worst fault position per path (of every boundary enumerated)", rows)
}

// publishScenario prepares a stand at some pre-state and returns the
// operation whose trace is measured.
type publishScenario struct {
	name  string
	stage func(t *testing.T, pub *Publisher, root string) // runs before the measurement, uncounted
	run   func(pub *Publisher) error
}

func publishScenarios() []publishScenario {
	pubv := func(v string) func(*Publisher) error {
		return func(p *Publisher) error { _, err := p.Publish(prodBundle(v)); return err }
	}
	stagev := func(vs ...string) func(*testing.T, *Publisher, string) {
		return func(t *testing.T, p *Publisher, _ string) {
			t.Helper()
			for _, v := range vs {
				if _, err := p.Publish(prodBundle(v)); err != nil {
					t.Fatalf("stage %s: %v", v, err)
				}
			}
		}
	}
	return []publishScenario{
		{name: "first contact", stage: func(*testing.T, *Publisher, string) {}, run: pubv("39")},
		{name: "no-op", stage: stagev("39"), run: pubv("39")},
		{name: "replacement", stage: stagev("39"), run: pubv("40")},
		{name: "replacement+sweep", stage: stagev("39", "40"), run: pubv("41")},
		{
			// An interrupted earlier attempt at the SAME version left an
			// uncommitted integration/v40 behind: commitGeneration removes
			// it before the rename, so this path pays a tree removal the
			// others do not.
			name: "replacement over uncommitted garbage",
			stage: func(t *testing.T, p *Publisher, root string) {
				t.Helper()
				stagev("39")(t, p, root)
				plantGeneration(t, filepath.Join(root, integrationDir), "v40")
			},
			run: pubv("40"),
		},
		{
			// The launch carrier was removed from a host that still has its
			// generations: the replacement reinstalls it, which is the most
			// expensive publish shape there is — a full sweep AND a carrier
			// write in one attempt.
			name: "replacement+sweep, launch carrier absent",
			stage: func(t *testing.T, p *Publisher, root string) {
				t.Helper()
				stagev("39", "40")(t, p, root)
				if err := os.Remove(filepath.Join(root, launchName)); err != nil {
					t.Fatalf("remove launch: %v", err)
				}
			},
			run: pubv("41"),
		},
		{
			// A crashed first publish leaves an empty root; the next attempt
			// accepts it rather than stranding on it.
			name: "first contact over an empty root",
			stage: func(t *testing.T, _ *Publisher, root string) {
				t.Helper()
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatalf("plant empty root: %v", err)
				}
			},
			run: pubv("39"),
		},
	}
}

func measureFaultPaths(t *testing.T) []row {
	t.Helper()
	var rows []row
	for _, sc := range publishScenarios() {
		// Fault-free run of this scenario: how many calls of each kind are
		// there to fail?
		pub, root, cfs, _ := newMeasuredPublisher(t)
		sc.stage(t, pub, root)
		cfs.reset()
		if err := sc.run(pub); err != nil {
			t.Fatalf("%s: fault-free run: %v", sc.name, err)
		}
		base := cfs.snapshot()

		worst := row{path: sc.name + ": (no boundary to fail)"}
		for _, kind := range measuredKinds {
			for n := 1; n <= base.calls[kind]; n++ {
				fpub, froot, fcfs, ffs := newMeasuredPublisher(t)
				sc.stage(t, fpub, froot)
				fcfs.reset()
				ffs.resetCounts()
				ffs.setFault(kind, n, errInjected)
				_ = sc.run(fpub)
				m := fcfs.snapshot()
				if m.total > worst.m.total {
					label := fmt.Sprintf("%s: fault at %s#%d (%s)", sc.name, kind, n, phaseOf(m.ops, kind, n))
					worst = row{label, m}
				}
			}
		}
		rows = append(rows, worst)
		t.Logf("%s: fault-free %s; enumerated %d boundary positions", sc.name, base, base.total)
	}
	return rows
}

// phaseOf names the operation the n-th call of kind actually was, so a
// worst-case row in the report says WHICH boundary it is, not only which
// ordinal.
func phaseOf(ops []string, kind string, n int) string {
	seen := 0
	for _, op := range ops {
		k, path, _ := strings.Cut(op, ":")
		if k != kind {
			continue
		}
		seen++
		if seen == n {
			return filepath.Base(path)
		}
	}
	return "not reached"
}

// TestMeasureFailedCleanup measures the paths where the sweep that bounds
// the footprint is itself the thing that fails, and says which of them may
// fail the publish and which may not. The generation sweep runs after the
// manifest is committed and tolerates its own failures — the activation has
// already moved, and the next attempt retries under the lock. The staging
// clear runs BEFORE anything is written and does not tolerate its own
// failure: residue that cannot be cleared refuses the write (bound 1).
func TestMeasureFailedCleanup(t *testing.T) {
	var rows []row
	for _, tc := range []struct {
		name    string
		kind    string
		path    func(root string) string
		refuses bool
	}{
		{
			name:    "staging clear: readdir(tmp) fails",
			kind:    "readdir",
			path:    func(root string) string { return filepath.Join(root, tmpName) },
			refuses: true,
		},
		{
			name:    "staging clear: remove of the residue fails",
			kind:    "remove",
			path:    func(root string) string { return filepath.Join(root, tmpName, "orphan00", "f0") },
			refuses: true,
		},
		{
			name: "generation sweep: readdir(integration) fails",
			kind: "readdir",
			path: func(root string) string { return filepath.Join(root, integrationDir) },
		},
		{
			name: "generation sweep: remove of a swept generation fails",
			kind: "remove",
			path: func(root string) string { return filepath.Join(root, integrationDir, "v39", "nocx.bash") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, root, cfs, ffs := newMeasuredPublisher(t)
			for _, v := range []string{"39", "40"} {
				if _, err := pub.Publish(prodBundle(v)); err != nil {
					t.Fatalf("baseline %s: %v", v, err)
				}
			}
			before := readFileT(t, filepath.Join(root, manifestName))
			plantOrphans(t, filepath.Join(root, tmpName), 1, 2)

			cfs.reset()
			ffs.resetCounts()
			ffs.setFaultPath(tc.kind, tc.path(root), errInjected)
			_, err := pub.Publish(prodBundle("41"))
			ffs.setFaultPath(tc.kind, "", nil)

			m := cfs.snapshot()
			rows = append(rows, row{tc.name, m})
			if m.total > maxPublishFSOps {
				t.Errorf("the failing path cost %d FS-seam calls, over N = %d", m.total, maxPublishFSOps)
			}
			if tc.refuses {
				if err == nil {
					t.Fatal("residue that cannot be cleared must refuse the write")
				}
				if got := readFileT(t, filepath.Join(root, manifestName)); string(got) != string(before) {
					t.Fatal("a refused attempt changed the committed manifest")
				}
				return
			}
			if err != nil {
				t.Fatalf("a sweep failure after the commit must not fail the publish: %v", err)
			}
			// The activation still moved: a failed sweep is residue, never
			// a failed publish.
			if got := readManifestT(t, root).Generation; got != "v41" {
				t.Fatalf("manifest names %s after a failed sweep, want v41", got)
			}
		})
	}
	logRows(t, "cleanup that itself fails", rows)
}

// TestMeasureLockCost measures what a contended lock costs a waiter now
// that K probes on a fixed schedule have replaced the 25 ms poll loop.
// Nothing here is timed: the probes run on the injected clock, and what is
// read off is the SHAPE — how many FS calls the wait issues and which.
func TestMeasureLockCost(t *testing.T) {
	pub, root, cfs, _ := newMeasuredPublisher(t)
	if _, err := pub.Publish(prodBundle("39")); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	lockDir := plantLock(t, root)

	cfs.reset()
	if _, err := pub.Publish(prodBundle("40")); err != nil {
		t.Fatalf("publish under a stale lock: %v", err)
	}
	m := cfs.snapshot()

	// From the first Lstat of the lock directory until the nonce is
	// created, the ONLY operations are repeats of that Lstat plus the
	// stale break (two Removes) and the acquiring Mkdir. One probe costs
	// exactly one FS call, whatever the machine.
	probes, extra := 0, []string{}
	inWait := false
	for _, op := range m.ops {
		k, p, _ := strings.Cut(op, ":")
		if k == "lstat" && p == lockDir {
			inWait = true
			probes++
			continue
		}
		if !inWait {
			continue
		}
		if (k == "remove" && (p == lockDir || p == filepath.Join(lockDir, lockNonceFile))) ||
			(k == "mkdir" && p == lockDir) {
			extra = append(extra, op)
			continue
		}
		if k == "lstat" && p == filepath.Join(lockDir, lockNonceFile) {
			break // the acquisition's nonce write ends the wait
		}
		t.Fatalf("unexpected operation inside the lock wait: %s", op)
	}
	if probes != lockProbes+1 {
		t.Errorf("the waiter probed %d times, want K = %d plus the acquisition after the break", probes, lockProbes)
	}
	t.Logf("--- lock cost, K = %d probes on %v", lockProbes, lockProbeSchedule)
	t.Logf("    contended acquire: %s", m)
	t.Logf("    probes: %d at one FS call each; the break adds %v", probes, extra)
	t.Logf("    retired loop, same outcome: %d Lstat calls (5 s bound / 25 ms poll) + 2 removes",
		int(5*time.Second/(25*time.Millisecond)))
	t.Logf("    retired loop, re-contended after the break: %d Lstat calls, and nothing published",
		2*int(5*time.Second/(25*time.Millisecond)))
}

// TestMeasureLockContentionOutcomes measures the three contention shapes,
// each reached through an observable state change rather than a duration:
// the holder releases when the waiter asks to wait, the holder never
// releases, or the lock is re-taken inside the very Remove that frees it.
func TestMeasureLockContentionOutcomes(t *testing.T) {
	var rows []row

	// Held by another writer, then released before the probes run out.
	{
		pub, root, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		lockDir := plantLock(t, root)
		// The holder releases the moment the waiter asks to wait: an
		// observable state change driven by the code under test.
		pub.after = func(time.Duration) <-chan time.Time {
			_ = os.Remove(filepath.Join(lockDir, lockNonceFile))
			_ = os.Remove(lockDir)
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		}
		cfs.reset()
		if _, err := pub.Publish(prodBundle("40")); err != nil {
			t.Fatalf("publish after the holder released: %v", err)
		}
		rows = append(rows, row{"lock held, released after the first probe", cfs.snapshot()})
	}

	// Held and never released: the stale rule breaks it after K probes.
	{
		pub, root, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		plantLock(t, root)
		cfs.reset()
		if _, err := pub.Publish(prodBundle("40")); err != nil {
			t.Fatalf("publish under a stale lock: %v", err)
		}
		rows = append(rows, row{"lock held and stale (broken, then acquired)", cfs.snapshot()})
	}

	// Broken, then re-contended: the only path that fails, and §11
	// assertion 33 wants it named.
	{
		home := t.TempDir()
		root := filepath.Join(home, dirName)
		if err := os.MkdirAll(filepath.Join(root, tmpName), 0o700); err != nil {
			t.Fatalf("plant root: %v", err)
		}
		cfs := newCountingFS(&squatterFS{FS: NewOSFS(), lockDir: filepath.Join(root, lockName)})
		pub := NewPublisher(testLogger(), cfs, root)
		newFakeClock().install(pub)
		plantLock(t, root)
		cfs.reset()
		res, err := pub.Publish(prodBundle("39"))
		if err == nil {
			t.Fatal("a lock re-contended after the stale break must fail the publish")
		}
		var ce *ContendedError
		if !errors.As(err, &ce) {
			t.Fatalf("unnamed outcome for re-contention: %T %v", err, err)
		}
		if res.Reason != ReasonContended {
			t.Fatalf("outcome reason = %q, want %q", res.Reason, ReasonContended)
		}
		m := cfs.snapshot()
		if m.bytes != 0 {
			t.Errorf("a contended attempt wrote %d bytes", m.bytes)
		}
		rows = append(rows, row{"lock broken then re-contended (publish fails, named)", m})
	}
	logRows(t, "lock contention (probes on the injected clock)", rows)
}

// TestMeasurePublishScaling separates the fixed cost from the parts that
// scale, and names what each scales with. The design forbids unbounded
// directory traversal and wants inspected and removed entries bounded
// separately: this is the measurement that says which is which.
func TestMeasurePublishScaling(t *testing.T) {
	t.Run("bundle file count", func(t *testing.T) {
		var rows []row
		for _, n := range []int{1, 3, 6, 12} {
			pub, _, cfs, _ := newMeasuredPublisher(t)
			if _, err := pub.Publish(syntheticBundle("39", n)); err != nil {
				t.Fatalf("publish with %d files: %v", n, err)
			}
			rows = append(rows, row{fmt.Sprintf("first contact, %2d generation files", n), cfs.snapshot()})
		}
		logRows(t, "scaling with bundle file count F (1 KiB files, no launch carrier)", rows)
	})

	// The two terms that USED to scale with residue no longer do: bound 1
	// clears at most one staging slot's worth per attempt and bound 3
	// sweeps at most one generation, so a host with nine of either costs
	// what a host with one costs and converges over attempts instead.
	t.Run("tmp orphans", func(t *testing.T) {
		var rows []row
		var flat int
		for _, n := range []int{0, 1, 3, 9} {
			pub, root, cfs, _ := newMeasuredPublisher(t)
			if _, err := pub.Publish(prodBundle("39")); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			plantOrphans(t, filepath.Join(root, tmpName), n, 2)
			cfs.reset()
			if _, err := pub.Publish(prodBundle("40")); err != nil {
				t.Fatalf("publish over %d orphans: %v", n, err)
			}
			m := cfs.snapshot()
			rows = append(rows, row{fmt.Sprintf("replacement, %d tmp orphan dirs of 2 files", n), m})
			if n >= maxStagingSlotEntries {
				if flat == 0 {
					flat = m.total
				} else if m.total != flat {
					t.Errorf("%d orphans cost %d FS-seam calls; %d cost %d — the clear is not bounded",
						n, m.total, maxStagingSlotEntries, flat)
				}
			}
			if m.total > maxPublishFSOps {
				t.Errorf("%d orphans cost %d FS-seam calls, over N = %d", n, m.total, maxPublishFSOps)
			}
		}
		logRows(t, "entries already present in tmp/ (bounded: at most one slot cleared per attempt)", rows)
	})

	t.Run("stale generations", func(t *testing.T) {
		var rows []row
		var flat int
		for _, n := range []int{0, 1, 3, 9} {
			pub, root, cfs, _ := newMeasuredPublisher(t)
			if _, err := pub.Publish(prodBundle("39")); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			plantGenerations(t, filepath.Join(root, integrationDir), n)
			cfs.reset()
			if _, err := pub.Publish(prodBundle("40")); err != nil {
				t.Fatalf("publish over %d stale generations: %v", n, err)
			}
			m := cfs.snapshot()
			rows = append(rows, row{fmt.Sprintf("replacement, %d stale generations of 3 files", n), m})
			if n >= 1 {
				if flat == 0 {
					flat = m.total
				} else if m.total != flat {
					t.Errorf("%d stale generations cost %d FS-seam calls; one costs %d — the sweep is not bounded",
						n, m.total, flat)
				}
			}
			if m.total > maxPublishFSOps {
				t.Errorf("%d stale generations cost %d FS-seam calls, over N = %d", n, m.total, maxPublishFSOps)
			}
		}
		logRows(t, "entries already present in integration/ (bounded: at most one swept per attempt)", rows)
	})
}

// syntheticBundle is n generation files of 1 KiB each and no launch
// carrier: the shape that isolates the per-file cost.
func syntheticBundle(version string, n int) Bundle {
	b := Bundle{Protocol: ProtocolVersion, Version: version}
	for i := range n {
		b.Files = append(b.Files, BundleFile{
			Name: fmt.Sprintf("nocx.f%d", i),
			Mode: 0o600,
			Data: []byte(strings.Repeat("x", 1024)),
		})
	}
	return b
}

func plantOrphans(t *testing.T, tmpDir string, dirs, filesEach int) {
	t.Helper()
	for i := range dirs {
		d := filepath.Join(tmpDir, fmt.Sprintf("orphan%02d", i))
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatalf("plant orphan: %v", err)
		}
		for j := range filesEach {
			if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("f%d", j)), []byte("x"), 0o600); err != nil {
				t.Fatalf("plant orphan file: %v", err)
			}
		}
	}
}

func plantGenerations(t *testing.T, integration string, n int) {
	t.Helper()
	for i := range n {
		plantGeneration(t, integration, fmt.Sprintf("v%d", i+1))
	}
}

// plantGeneration writes a generation directory nothing points at: the
// residue an interrupted publish leaves behind.
func plantGeneration(t *testing.T, integration, name string) {
	t.Helper()
	d := filepath.Join(integration, name)
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatalf("plant generation: %v", err)
	}
	for _, f := range []string{"nocx.bash", "nocx.zsh", "nocx.posix"} {
		if err := os.WriteFile(filepath.Join(d, f), []byte("x"), 0o600); err != nil {
			t.Fatalf("plant generation file: %v", err)
		}
	}
}

// TestMeasureBundleBytes reports the payload the design sizes B against.
func TestMeasureBundleBytes(t *testing.T) {
	b := launchBundle()
	total := 0
	names := make([]string, 0, len(b.Files))
	sizes := map[string]int{}
	for _, f := range b.Files {
		total += len(f.Data)
		names = append(names, f.Name)
		sizes[f.Name] = len(f.Data)
	}
	sort.Strings(names)
	t.Logf("--- shipped bundle (version %s)", b.Version)
	for _, n := range names {
		t.Logf("    %-12s %7d bytes (%.1f KiB)", n, sizes[n], float64(sizes[n])/1024)
	}
	t.Logf("    %-12s %7d bytes (%.1f KiB)", "TOTAL", total, float64(total)/1024)

	// And the bytes a first-contact publish actually pushes through the
	// seam, which is the bundle plus the manifest and the lock nonce, and
	// counts the generation files once each.
	pub, _, cfs, _ := newMeasuredPublisher(t)
	if _, err := pub.Publish(b); err != nil {
		t.Fatalf("publish: %v", err)
	}
	m := cfs.snapshot()
	t.Logf("    first-contact bytes through the seam: %d (%.1f KiB) in %d writes",
		m.bytes, float64(m.bytes)/1024, m.calls["write"])
}

// TestMeasureVerifyAndUninstall measures the other two operations a carrier
// performs against the same seam; N is a budget per attempt, and an attempt
// may be a Verify rather than a Publish.
func TestMeasureVerifyAndUninstall(t *testing.T) {
	var rows []row
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		cfs.reset()
		vr, err := pub.Verify()
		if err != nil || !vr.Installed {
			t.Fatalf("Verify = %+v, %v", vr, err)
		}
		rows = append(rows, row{"Verify (installed, 3 generation files)", cfs.snapshot()})
	}
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		cfs.reset()
		vr, err := pub.Verify()
		if err != nil || vr.Installed {
			t.Fatalf("Verify on a bare host = %+v, %v", vr, err)
		}
		rows = append(rows, row{"Verify (nothing installed)", cfs.snapshot()})
	}
	{
		pub, _, cfs, _ := newMeasuredPublisher(t)
		if _, err := pub.Publish(prodBundle("39")); err != nil {
			t.Fatalf("baseline: %v", err)
		}
		cfs.reset()
		if _, err := pub.Uninstall(); err != nil {
			t.Fatalf("Uninstall: %v", err)
		}
		rows = append(rows, row{"Uninstall (3 generation files)", cfs.snapshot()})
	}
	logRows(t, "the other seam operations", rows)
}

// TestMeasureWorstBoundedAttempt measures the worst attempt that is still
// inside the residue bounds the design asks P3 to enforce: one staging slot
// plus its manifest temp in tmp/, one uncommitted generation at the target
// version, one stale generation to sweep, the launch carrier absent, and a
// manifest to read. It is the arithmetic of the per-entry coefficients
// turned back into a measurement, so the recommended N is a number that was
// observed rather than one that was added up.
func TestMeasureWorstBoundedAttempt(t *testing.T) {
	pub, root, cfs, _ := newMeasuredPublisher(t)
	for _, v := range []string{"39", "40"} {
		if _, err := pub.Publish(prodBundle(v)); err != nil {
			t.Fatalf("stage %s: %v", v, err)
		}
	}
	if err := os.Remove(filepath.Join(root, launchName)); err != nil {
		t.Fatalf("remove launch: %v", err)
	}
	// One staging slot's worth of residue: a staging dir of F files and the
	// manifest temp of the attempt that died beside it.
	plantOrphans(t, filepath.Join(root, tmpName), 1, 3)
	if err := os.WriteFile(filepath.Join(root, tmpName, "manifest-dead.tmp"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("plant manifest temp: %v", err)
	}
	// An interrupted attempt at the version we are about to publish.
	plantGeneration(t, filepath.Join(root, integrationDir), "v41")

	cfs.reset()
	if _, err := pub.Publish(prodBundle("41")); err != nil {
		t.Fatalf("worst bounded attempt: %v", err)
	}
	m := cfs.snapshot()
	t.Logf("--- worst attempt inside the design's residue bounds")
	t.Logf("    %s", m)
	if m.total != measuredMaxBoundedResidue {
		t.Errorf("worst bounded attempt = %d FS calls, recorded %d — update REPORT-p3-measure.md and this constant together",
			m.total, measuredMaxBoundedResidue)
	}
}

// TestMeasuredMaximumIsStill records the gate's answer: the maximum over
// every publish path measured here — happy, faulted and failed-cleanup —
// with the production bundle, an uncontended lock and no pre-existing
// residue. It fails the moment the measurement moves, which is what makes
// the number in REPORT-p3-measure.md quotable.
func TestMeasuredMaximumIsStill(t *testing.T) {
	rows := append(measureHappyPaths(t), measureFaultPaths(t)...)
	worst := row{}
	maxBytes := int64(0)
	for _, r := range rows {
		if r.m.total > worst.m.total {
			worst = r
		}
		if r.m.bytes > maxBytes {
			maxBytes = r.m.bytes
		}
	}
	t.Logf("worst measured publish path: %s -> %s", worst.path, worst.m)
	if worst.m.total != measuredMaxPublishCalls {
		t.Errorf("maximum FS-seam calls per publish = %d (%s), recorded %d — update REPORT-p3-measure.md and this constant together",
			worst.m.total, worst.path, measuredMaxPublishCalls)
	}
	if maxBytes != measuredMaxPublishBytes {
		t.Errorf("maximum bytes written per publish = %d, recorded %d — update REPORT-p3-measure.md and this constant together",
			maxBytes, measuredMaxPublishBytes)
	}
}
