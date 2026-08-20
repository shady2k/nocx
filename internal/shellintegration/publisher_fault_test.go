package shellintegration

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// errInjected is the fault the table injects at each boundary.
var errInjected = errors.New("injected fault")

// faultFS wraps an FS and can fail or record calls by kind. Every publish
// boundary is a kind: mkdir, create (each file write), sync (each file
// fsync), syncdir (each directory fsync), rename, remove (lock release and
// cleanup) and lock acquire (mkdir of the lock dir).
type faultFS struct {
	FS
	mu       sync.Mutex
	failOn   map[string]int    // kind -> 1-based call number to fail
	failPath map[string]string // kind -> path that always fails
	failErr  error
	counts   map[string]int
	ops      []string // ordered op log, "kind:path"
}

func newFaultFS(inner FS) *faultFS {
	return &faultFS{
		FS:       inner,
		failOn:   map[string]int{},
		failPath: map[string]string{},
		counts:   map[string]int{},
	}
}

// setFault makes the n-th call of kind return err (1-based). n <= 0 clears.
func (f *faultFS) setFault(kind string, n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n <= 0 {
		delete(f.failOn, kind)
		return
	}
	f.failOn[kind] = n
	f.failErr = err
}

// resetCounts zeroes the per-kind counters and the op log so a subsequent
// publish can be faulted and counted in isolation from the baseline publish
// that precedes it.
func (f *faultFS) resetCounts() {
	f.mu.Lock()
	f.counts = map[string]int{}
	f.ops = nil
	f.mu.Unlock()
}

// setFaultPath makes every call of kind against exactly path fail. An
// ordinal says WHEN a fault fires and is what an enumeration needs; a path
// says WHERE, which is what a test about one boundary — this residue entry,
// this directory — should say instead of counting calls to reach it. An
// empty path clears.
func (f *faultFS) setFaultPath(kind, path string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == "" {
		delete(f.failPath, kind)
		return
	}
	f.failPath[kind] = path
	f.failErr = err
}

func (f *faultFS) hit(kind, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[kind]++
	f.ops = append(f.ops, kind+":"+path)
	if n, ok := f.failOn[kind]; ok && f.counts[kind] == n {
		return f.failErr
	}
	if p, ok := f.failPath[kind]; ok && p == path {
		return f.failErr
	}
	return nil
}

func (f *faultFS) Lstat(path string) (fs.FileInfo, error) {
	if err := f.hit("lstat", path); err != nil {
		return nil, err
	}
	return f.FS.Lstat(path)
}

func (f *faultFS) Mkdir(path string, mode os.FileMode) error {
	if err := f.hit("mkdir", path); err != nil {
		return err
	}
	return f.FS.Mkdir(path, mode)
}

func (f *faultFS) Create(path string, mode os.FileMode) (File, error) {
	if err := f.hit("create", path); err != nil {
		return nil, err
	}
	file, err := f.FS.Create(path, mode)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, fs: f, path: path}, nil
}

func (f *faultFS) SyncDir(path string) error {
	if err := f.hit("syncdir", path); err != nil {
		return err
	}
	return f.FS.SyncDir(path)
}

func (f *faultFS) Rename(src, dst string) error {
	if err := f.hit("rename", src+" -> "+dst); err != nil {
		return err
	}
	return f.FS.Rename(src, dst)
}

func (f *faultFS) Remove(path string) error {
	if err := f.hit("remove", path); err != nil {
		return err
	}
	return f.FS.Remove(path)
}

func (f *faultFS) ReadDir(path string) ([]fs.FileInfo, error) {
	if err := f.hit("readdir", path); err != nil {
		return nil, err
	}
	return f.FS.ReadDir(path)
}

func (f *faultFS) ReadFile(path string) ([]byte, error) {
	if err := f.hit("readfile", path); err != nil {
		return nil, err
	}
	return f.FS.ReadFile(path)
}

// faultFile makes each step of the write boundary — Write, Sync, Close —
// its own fault-injectable kind, as publish_fs.go says they are ("the
// returned File is the write boundary: Write, Sync and Close are separate
// fault-injectable steps"). Sync was injectable from the start; the other
// two became so for the P3 measurement, which enumerates every boundary.
type faultFile struct {
	File
	fs   *faultFS
	path string
}

func (w *faultFile) Write(p []byte) (int, error) {
	if err := w.fs.hit("write", w.path); err != nil {
		return 0, err
	}
	return w.File.Write(p)
}

func (w *faultFile) Sync() error {
	if err := w.fs.hit("sync", w.path); err != nil {
		return err
	}
	return w.File.Sync()
}

func (w *faultFile) Close() error {
	if err := w.fs.hit("close", w.path); err != nil {
		return err
	}
	return w.File.Close()
}

// recordingFS logs every operation in order (used by the rename-last test).
type recordingFS struct {
	FS
	mu  sync.Mutex
	ops []string
}

func (r *recordingFS) hit(kind, path string) {
	r.mu.Lock()
	r.ops = append(r.ops, kind+":"+path)
	r.mu.Unlock()
}

func (r *recordingFS) Lstat(path string) (fs.FileInfo, error) {
	r.hit("lstat", path)
	return r.FS.Lstat(path)
}

func (r *recordingFS) Mkdir(path string, mode os.FileMode) error {
	r.hit("mkdir", path)
	return r.FS.Mkdir(path, mode)
}

func (r *recordingFS) Create(path string, mode os.FileMode) (File, error) {
	r.hit("create", path)
	return r.FS.Create(path, mode)
}

func (r *recordingFS) SyncDir(path string) error {
	r.hit("syncdir", path)
	return r.FS.SyncDir(path)
}

func (r *recordingFS) Rename(src, dst string) error {
	r.hit("rename", src+" -> "+dst)
	return r.FS.Rename(src, dst)
}

func (r *recordingFS) Remove(path string) error {
	r.hit("remove", path)
	return r.FS.Remove(path)
}

func (r *recordingFS) ReadDir(path string) ([]fs.FileInfo, error) {
	r.hit("readdir", path)
	return r.FS.ReadDir(path)
}

func (r *recordingFS) ReadFile(path string) ([]byte, error) {
	r.hit("readfile", path)
	return r.FS.ReadFile(path)
}

// TestFaultAtEveryBoundaryConverges is the enumerable fault-injection test
// the seam exists for (design §4: failure at each boundary is injectable
// rather than argued about). For every publish boundary of a version bump —
// each mkdir, each file write, each file fsync, each directory fsync, each
// rename, lock acquire (mkdir of the lock dir) and lock release (removes) —
// inject a failure, assert the previous activation is untouched, then
// assert the next attempt converges with no manual cleanup.
func TestFaultAtEveryBoundaryConverges(t *testing.T) {
	// Enumerate every boundary position from a clean version-bump publish:
	// v1 installed, then v2 published, on a fault-free recording FS.
	enumHome := t.TempDir()
	enumFS := newFaultFS(NewOSFS())
	enumPub := NewPublisher(testLogger(), enumFS, filepath.Join(enumHome, dirName))
	newFakeClock().install(enumPub)
	if _, err := enumPub.Publish(testBundle("1")); err != nil {
		t.Fatalf("baseline publish v1: %v", err)
	}
	enumFS.resetCounts() // count only the version-bump publish, not the baseline
	if _, err := enumPub.Publish(testBundle("2")); err != nil {
		t.Fatalf("baseline publish v2: %v", err)
	}
	enumFS.mu.Lock()
	max := map[string]int{}
	for _, op := range enumFS.ops {
		kind := strings.SplitN(op, ":", 2)[0]
		max[kind]++
	}
	enumFS.mu.Unlock()

	type position struct {
		kind string
		n    int
	}
	var positions []position
	// Every boundary publish_fs.go names, not only the six that were once
	// enumerable: Write and Close became injectable for the measurement,
	// and Lstat, ReadDir and ReadFile are boundaries a carrier pays for
	// too (§11 assertion 27 says "every FS-seam boundary").
	for _, kind := range []string{"lstat", "mkdir", "create", "write", "sync", "close", "syncdir", "rename", "remove", "readdir", "readfile"} {
		for n := 1; n <= max[kind]; n++ {
			positions = append(positions, position{kind, n})
		}
	}
	t.Logf("enumerated %d boundary positions across %d kinds", len(positions), len(max))

	for _, pos := range positions {
		t.Run(fmt.Sprintf("%s#%d", pos.kind, pos.n), func(t *testing.T) {
			home := t.TempDir()
			fsys := newFaultFS(NewOSFS())
			pub := NewPublisher(testLogger(), fsys, filepath.Join(home, dirName))
			newFakeClock().install(pub)
			root := filepath.Join(home, dirName)

			if _, err := pub.Publish(testBundle("1")); err != nil {
				t.Fatalf("baseline publish v1: %v", err)
			}
			before := readFileT(t, filepath.Join(root, manifestName))
			fsys.resetCounts() // fault positions are relative to the v2 publish alone
			fsys.setFault(pos.kind, pos.n, errInjected)
			_, err := pub.Publish(testBundle("2"))

			// The outcome is read from the state, not predicted from the
			// ordinal: a fault before the manifest rename leaves the
			// previous activation byte-identical, one after it (the root
			// fsync, the lock release, the generation sweep) leaves the new
			// manifest committed. Both are legitimate; a torn state in
			// between is not, and neither is residue beyond one slot.
			after := readFileT(t, filepath.Join(root, manifestName))
			switch {
			case string(after) == string(before):
				if err == nil {
					t.Fatalf("fault at %s#%d reported success without moving the activation", pos.kind, pos.n)
				}
				vr, verr := pub.Verify()
				if verr != nil {
					t.Fatalf("Verify after fault: %v", verr)
				}
				if !vr.Installed || vr.Generation != "v1" {
					t.Fatalf("previous activation not intact after fault: %+v", vr)
				}
			default:
				m, perr := parseManifest(after)
				if perr != nil {
					t.Fatalf("fault at %s#%d left an unparseable manifest: %v", pos.kind, pos.n, perr)
				}
				if m.Generation != "v2" {
					t.Fatalf("fault at %s#%d left the manifest naming %s", pos.kind, pos.n, m.Generation)
				}
			}
			assertResidueWithinOneSlot(t, root)

			// The next attempt converges with no manual cleanup.
			fsys.setFault(pos.kind, 0, nil)
			if _, retryErr := pub.Publish(testBundle("2")); retryErr != nil {
				t.Fatalf("retry after fault at %s#%d: %v", pos.kind, pos.n, retryErr)
			}
			vr, err := pub.Verify()
			if err != nil {
				t.Fatalf("Verify after retry: %v", err)
			}
			if !vr.Installed || vr.Generation != "v2" {
				t.Fatalf("retry did not converge: %+v", vr)
			}
			assertBoundedFootprint(t, root, "v2")
		})
	}
}

// assertResidueWithinOneSlot is the residue half of §11 assertion 27: after
// a failed attempt, what we own is at most ONE staging slot — one staging
// directory and one manifest temp — and the generation directory holds at
// most the keep-two footprint plus the uncommitted generation of the
// attempt that just failed.
func assertResidueWithinOneSlot(t *testing.T, root string) {
	t.Helper()
	tmpEntries, err := os.ReadDir(filepath.Join(root, tmpName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readdir tmp: %v", err)
	}
	if len(tmpEntries) > maxStagingSlotEntries {
		t.Errorf("tmp/ holds %d entries after a failed attempt, want at most one slot (%d): %v",
			len(tmpEntries), maxStagingSlotEntries, names(tmpEntries))
	}
	dirs := 0
	for _, e := range tmpEntries {
		if e.IsDir() {
			dirs++
		}
	}
	if dirs > maxStagingSlots {
		t.Errorf("tmp/ holds %d staging directories, want at most %d: %v", dirs, maxStagingSlots, names(tmpEntries))
	}
	gens, err := os.ReadDir(filepath.Join(root, integrationDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readdir integration: %v", err)
	}
	if len(gens) > 3 {
		t.Errorf("integration/ holds %d generations after a failed attempt, want at most keep-two plus the uncommitted one: %v",
			len(gens), names(gens))
	}
}

// TestFirstPublishFaultLeavesNothingActive: a fault during the very first
// publish leaves no activation pointer and no committed generation reachable
// from one — torn publication is unrepresentable, and the retry converges.
func TestFirstPublishFaultLeavesNothingActive(t *testing.T) {
	for _, pos := range []struct {
		kind string
		n    int
	}{
		{"mkdir", 2},  // tmp mkdir
		{"create", 2}, // first generation file write
		{"sync", 3},   // a generation file fsync
		{"rename", 1}, // generation rename
		{"rename", 2}, // manifest rename
	} {
		t.Run(fmt.Sprintf("%s#%d", pos.kind, pos.n), func(t *testing.T) {
			home := t.TempDir()
			fsys := newFaultFS(NewOSFS())
			pub := NewPublisher(testLogger(), fsys, filepath.Join(home, dirName))
			newFakeClock().install(pub)
			root := filepath.Join(home, dirName)

			fsys.setFault(pos.kind, pos.n, errInjected)
			if _, err := pub.Publish(testBundle("1")); err == nil {
				t.Fatalf("fault at %s#%d did not fail the first publish", pos.kind, pos.n)
			}
			// Never an activation pointer: whatever fault fired, the
			// manifest must not exist.
			if _, err := os.Stat(filepath.Join(root, manifestName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a manifest exists after a failed first publish: %v", err)
			}
			// A fault after the generation rename (rename#2, the manifest
			// rename) legitimately leaves the uncommitted generation on
			// disk; it is unreachable without the manifest and the retry
			// converges. Earlier faults must leave nothing committed.
			if pos.kind == "rename" && pos.n == 2 {
				if _, err := os.Stat(filepath.Join(root, integrationDir, "v1")); err != nil {
					t.Fatalf("generation missing after manifest-rename fault: %v", err)
				}
			} else if _, err := os.Stat(filepath.Join(root, integrationDir, "v1")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a committed generation exists after a failed first publish: %v", err)
			}

			fsys.setFault(pos.kind, 0, nil)
			if _, err := pub.Publish(testBundle("1")); err != nil {
				t.Fatalf("retry: %v", err)
			}
			assertBoundedFootprint(t, root, "v1")
		})
	}
}

// TestPublishFaultSurfacesErrorAndSweepsRoot pins the two behaviours the
// shadowed error branches in Publish must keep: a faulted first publish
// reaches the caller with the injected cause (never a successful result),
// and a failure after this invocation created the root sweeps the
// still-empty root back. The sweep works because every error branch returns
// explicitly, and an explicit return assigns the named err before the
// deferred sweep runs — a bare return or a swallowed error would leave
// either a phantom ~/.nocx or a false Published result.
func TestPublishFaultSurfacesErrorAndSweepsRoot(t *testing.T) {
	for _, pos := range []struct {
		kind      string
		n         int
		rootSwept bool // the fault fires before anything is written under the fresh root
	}{
		{"mkdir", 2, true},   // tmp mkdir: root exists and is still empty
		{"mkdir", 3, false},  // integration mkdir: tmp/ already written
		{"create", 2, false}, // first generation file write
	} {
		t.Run(fmt.Sprintf("%s#%d", pos.kind, pos.n), func(t *testing.T) {
			home := t.TempDir()
			fsys := newFaultFS(NewOSFS())
			pub := NewPublisher(testLogger(), fsys, filepath.Join(home, dirName))
			newFakeClock().install(pub)
			root := filepath.Join(home, dirName)

			fsys.setFault(pos.kind, pos.n, errInjected)
			res, err := pub.Publish(testBundle("1"))
			if err == nil {
				t.Fatalf("fault at %s#%d reported a successful publish: %+v", pos.kind, pos.n, res)
			}
			if !errors.Is(err, errInjected) {
				t.Fatalf("fault at %s#%d: caller must see the injected fault, got %v", pos.kind, pos.n, err)
			}
			if res.Published {
				t.Fatalf("fault at %s#%d reported Published=true", pos.kind, pos.n)
			}
			_, statErr := os.Stat(root)
			if pos.rootSwept {
				if !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("empty root created by this publish must be swept back, stat: %v", statErr)
				}
			} else if errors.Is(statErr, fs.ErrNotExist) {
				t.Fatal("root was swept although content was written under it")
			}

			fsys.setFault(pos.kind, 0, nil)
			if _, err = pub.Publish(testBundle("1")); err != nil {
				t.Fatalf("retry after fault at %s#%d: %v", pos.kind, pos.n, err)
			}
			assertBoundedFootprint(t, root, "v1")
		})
	}
}

// assertBoundedFootprint checks the invariants that must hold after any
// successful publish: the manifest names exactly the active generation,
// every file verifies, at most two generations and no tmp/ leftovers.
func assertBoundedFootprint(t *testing.T, root, active string) {
	t.Helper()
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.Installed || vr.Generation != active {
		t.Fatalf("Verify = %+v, want generation %s installed", vr, active)
	}
	gens, err := os.ReadDir(filepath.Join(root, integrationDir))
	if err != nil {
		t.Fatalf("readdir integration: %v", err)
	}
	if len(gens) > 2 {
		t.Errorf("integration/ has %d generations after publish", len(gens))
	}
	tmpEntries, err := os.ReadDir(filepath.Join(root, tmpName))
	if err != nil {
		t.Fatalf("readdir tmp: %v", err)
	}
	if len(tmpEntries) != 0 {
		t.Errorf("tmp/ has %d leftovers after publish", len(tmpEntries))
	}
}

// TestConcurrentPublishSameVersion: two concurrent publishes of the same
// version produce ONE remote publish, no duplicated work and no lost bytes.
// Both callers are told the same thing, because local singleflight joined
// them to one attempt: "exactly one caller sees Published" was a property
// of the lock serialising two remote attempts, and the remote attempt one
// of them made is precisely what is now not made at all. What must stay
// exactly one is the number of remote publishes. Run under -race.
func TestConcurrentPublishSameVersion(t *testing.T) {
	home := t.TempDir()
	fsys := newFaultFS(NewOSFS())
	root := filepath.Join(home, dirName)
	b := testBundle("10")

	const workers = 2
	results := make([]PublishResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pub := NewPublisher(testLogger(), fsys, root)
			results[i], errs[i] = pub.Publish(b)
		}(i)
	}
	wg.Wait()

	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if !results[i].Published || results[i].Generation != "v10" {
			t.Fatalf("worker %d did not receive the published result: %+v", i, results[i])
		}
	}

	// One remote publish: one generation rename and one manifest rename,
	// and the generation files and manifest temp written once each. A
	// second attempt — joined or serialised — would double both.
	fsys.mu.Lock()
	creates, renames := fsys.counts["create"], fsys.counts["rename"]
	fsys.mu.Unlock()
	if wantCreates := 1 + len(b.Files) + 1; creates != wantCreates {
		t.Errorf("create calls = %d, want %d (one lock nonce, one bundle, one manifest temp)", creates, wantCreates)
	}
	if renames != 2 {
		t.Errorf("rename calls = %d, want 2 (one generation, one manifest)", renames)
	}

	// No lost bytes: the active generation verifies against the bundle.
	vr, err := NewPublisher(testLogger(), NewOSFS(), root).Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vr.Installed || vr.Generation != "v10" {
		t.Fatalf("Verify = %+v", vr)
	}
	for _, f := range b.Files {
		if got := readFileT(t, filepath.Join(root, integrationDir, "v10", f.Name)); string(got) != string(f.Data) {
			t.Errorf("%s lost or corrupted bytes", f.Name)
		}
	}
	assertBoundedFootprint(t, root, "v10")
}

// TestConcurrentPublishDifferentVersions: when two versions race, the newer
// one wins and the older one is refused, never downgrading the result.
func TestConcurrentPublishDifferentVersions(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, dirName)
	fsys := newFaultFS(NewOSFS())

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, v := range []string{"1", "2"} {
		wg.Add(1)
		go func(i int, v string) {
			defer wg.Done()
			pub := NewPublisher(testLogger(), fsys, root)
			_, errs[i] = pub.Publish(testBundle(v))
		}(i, v)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	m := readManifestT(t, root)
	if m.Generation != "v2" {
		t.Fatalf("manifest names %s after a v1/v2 race, want v2", m.Generation)
	}
	assertBoundedFootprint(t, root, "v2")
}
