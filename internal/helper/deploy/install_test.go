package deploy_test

// The deploy package's install and prune acceptance tests (plan Task 9):
// every D7/D6 property is asserted against a fault-injectable in-memory
// RemoteFS, never by timing. Ensure resolves its bytes through the
// ArtifactSource seam it is given, so these tests inject a synthetic
// artifact — the D7 semantics need bytes, not a real helper — and run
// identically whether or not `make helpers` has run. Exactly one test in
// this package (platform_test.go) needs the real embedded binaries, and
// only it is gated on them.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// syntheticSource is an ArtifactSource serving a fixed stand-in artifact:
// real gzip bytes whose decompressed content hashes to syntheticHash. The
// install semantics under test are transport- and size-independent; the
// payload is over a kilobyte so failAfterBytes(1024) interrupts the write
// midway.
type syntheticSource struct{}

func (syntheticSource) Artifact(_ deploy.Platform) ([]byte, string, error) {
	return syntheticCompressed, syntheticHash, nil
}

var syntheticPayload = bytes.Repeat([]byte("deploy test helper payload\n"), 512)

var (
	syntheticCompressed []byte
	syntheticHash       string
)

func init() {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(syntheticPayload); err != nil {
		panic("synthetic: " + err.Error())
	}
	if err := zw.Close(); err != nil {
		panic("synthetic: " + err.Error())
	}
	sum := sha256.Sum256(syntheticPayload)
	syntheticCompressed = buf.Bytes()
	syntheticHash = hex.EncodeToString(sum[:])
}

// TestAnInterruptedInstallIsNeverMistakenForComplete is plan Task 9 step 1,
// verbatim: an upload interrupted midway must leave nothing that a later
// Ensure mistakes for complete, tested by a RemoteFS fake that fails at a
// byte bound rather than by timing.
func TestAnInterruptedInstallIsNeverMistakenForComplete(t *testing.T) {
	fs := newFakeFS()
	fs.failAfterBytes(1024)
	_, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err == nil {
		t.Fatal("want the interrupted upload to fail")
	}
	fs.failAfterBytes(0) // heal
	path, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("second attempt must succeed: %v", err)
	}
	if !fs.hasMarker(filepath.Dir(path)) {
		t.Fatal("the completed install must carry .install-complete")
	}
}

// TestEnsureUploadsNothingWhenComplete is D7: an already-complete directory
// must not upload a single byte — asserted by counting uploads, never by
// timing.
func TestEnsureUploadsNothingWhenComplete(t *testing.T) {
	fs := newFakeFS()
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before := fs.binaryCreateCount()
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if got := fs.binaryCreateCount() - before; got != 0 {
		t.Fatalf("Ensure on a complete directory performed %d uploads, want 0", got)
	}
}

// TestEnsureIncompleteDirectoryIsRemovedAndReinstalled is D7: a directory
// WITHOUT the marker is removed and reinstalled, never used.
func TestEnsureIncompleteDirectoryIsRemovedAndReinstalled(t *testing.T) {
	fs := newFakeFS()
	installed, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// Delete the marker: the directory is now partial, whatever it holds.
	fs.remove(filepath.Join(filepath.Dir(installed), ".install-complete"))
	before := fs.binaryCreateCount()
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}); err != nil {
		t.Fatalf("ensure over an incomplete directory must reinstall: %v", err)
	}
	if got := fs.binaryCreateCount() - before; got != 1 {
		t.Fatalf("ensure over an incomplete directory performed %d uploads, want exactly 1 reinstall", got)
	}
}

// TestEnsureReinstallsExactlyOnceOnHashMismatch is D6: a complete directory
// whose binary does not hash to its name is removed and reinstalled exactly
// once, and the fresh install is returned. A second mismatch on the same
// call is ErrHashMismatch and never loops — the next test.
func TestEnsureReinstallsExactlyOnceOnHashMismatch(t *testing.T) {
	fs := newFakeFS()
	installed, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// The directory looks complete but its binary no longer hashes to its
	// name — the D6 mismatch.
	fs.corruptFile(installed)
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}); err != nil {
		t.Fatalf("ensure over a mismatched complete directory must reinstall once: %v", err)
	}
	if got := fs.binaryCreateCount(); got != 2 {
		t.Fatalf("a hash mismatch triggered %d uploads, want exactly 2 (install + one reinstall)", got)
	}
}

// TestEnsureHashMismatchDoesNotLoop is D6's terminal half: when the
// reinstall STILL does not hash, Ensure returns ErrHashMismatch and stops —
// exactly one reinstall, never a retry loop.
func TestEnsureHashMismatchDoesNotLoop(t *testing.T) {
	fs := newFakeFS()
	installed, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	fs.corruptFile(installed)
	fs.corruptWrites = true // the reinstall will be corrupt too
	_, _, err = deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if !errors.Is(err, deploy.ErrHashMismatch) {
		t.Fatalf("second mismatch error = %v, want ErrHashMismatch", err)
	}
	if got := fs.binaryCreateCount(); got != 2 {
		t.Fatalf("Ensure attempted %d uploads before ErrHashMismatch, want 2 (install + one reinstall, no loop)", got)
	}
}

// TestEnsureTwoPlatformsNeverCollide is D7's reflexion-pass fix: the
// directory name carries goos-goarch, so one account on an arm64 and an
// amd64 machine — NFS, or the same login on both — resolves to two
// directories, each holding the binary for its own platform.
func TestEnsureTwoPlatformsNeverCollide(t *testing.T) {
	fs := newFakeFS()
	amd64, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("amd64 install: %v", err)
	}
	arm64, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "arm64"})
	if err != nil {
		t.Fatalf("arm64 install: %v", err)
	}
	if filepath.Dir(amd64) == filepath.Dir(arm64) {
		t.Fatal("two platforms resolved to one install directory")
	}
	if !fs.hasMarker(filepath.Dir(amd64)) || !fs.hasMarker(filepath.Dir(arm64)) {
		t.Fatal("both platforms' installs must be complete")
	}
}

// TestPruneKeepsTheNamedDirectory is D25's pruning half: older or
// superseded installs go, the one named as keep — the version in use —
// never.
func TestPruneKeepsTheNamedDirectory(t *testing.T) {
	fs := newFakeFS()
	installed, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	keep := filepath.Base(filepath.Dir(installed))
	// A stale sibling: a directory that matches our naming pattern but is
	// not the version in use (a superseded content hash).
	stale := filepath.Join("/home/u", ".nocx", "helper", "1-linux-amd64-"+strings.Repeat("0", 64))
	fs.mkdirAll(stale)
	fs.touch(filepath.Join(stale, ".install-complete"))
	// A foreign directory that is not ours: left strictly alone.
	fs.mkdirAll(filepath.Join("/home/u", ".nocx", "helper", "notes"))
	fs.touch(filepath.Join("/home/u", ".nocx", "helper", "notes", "todo.txt"))

	if err := deploy.Prune(context.Background(), fs, "/home/u", keep); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if fs.exists(installed) != nil {
		t.Fatalf("prune removed the directory passed as keep (%s)", keep)
	}
	if fs.exists(filepath.Join("/home/u", ".nocx", "helper", "1-linux-amd64-"+strings.Repeat("0", 64))) == nil {
		t.Fatal("prune left a superseded install directory behind")
	}
	if fs.exists(filepath.Join("/home/u", ".nocx", "helper", "notes", "todo.txt")) != nil {
		t.Fatal("prune touched a directory that does not match our naming pattern")
	}
}

// TestUninstallRemovesTheWholeHelperTree is D25's removal half: the whole
// ~/.nocx/helper tree goes — every installed version AND a directory left
// incomplete by an interrupted install (no .install-complete marker), which
// is exactly the kind a user cannot otherwise get rid of — and nothing else
// under the home is touched: the shell bundle's files and unrelated files
// survive. (The channel-close half of D25 is the CALLER's ordering: no
// helper may be running out of a directory being deleted, so the caller
// closes the helper's channel before calling Uninstall. The deploy package
// states that contract and removes the tree.)
func TestUninstallRemovesTheWholeHelperTree(t *testing.T) {
	fs := newFakeFS()
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}); err != nil {
		t.Fatalf("amd64 install: %v", err)
	}
	if _, _, err := deploy.Ensure(context.Background(), fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "arm64"}); err != nil {
		t.Fatalf("arm64 install: %v", err)
	}
	// An interrupted install: a versioned directory with no
	// .install-complete marker. Uninstall must take it with the rest — a
	// markerless directory is the one a later Ensure removes and
	// reinstalls, and a user cannot remove it any other way from the
	// surface (D25).
	incomplete := filepath.Join("/home/u", ".nocx", "helper", "1-linux-amd64-"+strings.Repeat("1", 64))
	fs.mkdirAll(incomplete)
	fs.touch(filepath.Join(incomplete, "nocx-helper"))
	// The shell bundle and unrelated files live alongside the helper tree
	// and must survive an uninstall.
	fs.touch("/home/u/.nocx/manifest.json")
	fs.touch("/home/u/notes.txt")

	if err := deploy.Uninstall(context.Background(), fs, "/home/u"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if fs.exists("/home/u/.nocx/helper") == nil {
		t.Fatal("uninstall left ~/.nocx/helper behind")
	}
	if fs.exists(incomplete) == nil {
		t.Fatal("uninstall left an incomplete install directory behind")
	}
	if fs.exists("/home/u/.nocx/manifest.json") != nil {
		t.Fatal("uninstall removed a shell-bundle file it does not own")
	}
	if fs.exists("/home/u/notes.txt") != nil {
		t.Fatal("uninstall removed an unrelated file outside the helper tree")
	}
}

// TestUninstallIsIdempotent: removing an absent tree is not an error — a
// host with nothing installed uninstalls cleanly, and a user clicking
// remove twice never sees a failure.
func TestUninstallIsIdempotent(t *testing.T) {
	fs := newFakeFS()
	if err := deploy.Uninstall(context.Background(), fs, "/home/u"); err != nil {
		t.Fatalf("uninstall on a bare host: %v", err)
	}
}

// TestEnsureHonoursContextCancellation: a cancelled install stops between
// phases instead of ploughing on.
func TestEnsureHonoursContextCancellation(t *testing.T) {
	fs := newFakeFS()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := deploy.Ensure(ctx, fs, syntheticSource{}, "/home/u", deploy.Platform{"linux", "amd64"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ensure error = %v, want context.Canceled", err)
	}
}

// ─── the fake RemoteFS ─────────────────────────────────────────────────────

// fakeFS is an in-memory RemoteFS with the fault knobs the acceptance
// criteria need: failAfterBytes interrupts an upload midway, corruptWrites
// makes every written binary fail its hash, binaryCreateCount counts
// uploads, and hasMarker proves a completed install. Modes are recorded but
// not enforced — the seam's mode discipline is the real adapter's, proven
// in internal/ssh.
type fakeFS struct {
	mu sync.Mutex

	dirs  map[string]bool
	files map[string][]byte

	creates       int
	binaryCreates int
	written       int  // bytes written across all binary Creates
	failAt        int  // fail the write that crosses this byte; 0 = never
	corruptWrites bool // corrupt every binary written
}

func newFakeFS() *fakeFS {
	return &fakeFS{dirs: map[string]bool{"/": true}, files: map[string][]byte{}}
}

func (f *fakeFS) failAfterBytes(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = n
}

func (f *fakeFS) binaryCreateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.binaryCreates
}

func (f *fakeFS) hasMarker(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.files[filepath.Join(dir, ".install-complete")]
	return ok
}

// exists returns nil when path exists, else the error a caller can compare
// with fs.ErrNotExist.
func (f *fakeFS) exists(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirs[path] {
		return nil
	}
	if _, ok := f.files[path]; ok {
		return nil
	}
	return fs.ErrNotExist
}

func (f *fakeFS) touch(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if parent := filepath.Dir(path); !f.dirs[parent] {
		panic("fakefs: touch under a missing parent " + parent)
	}
	f.files[path] = []byte{}
}

func (f *fakeFS) mkdirAll(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := "/"
	for _, part := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		f.dirs[cur] = true
	}
}

func (f *fakeFS) remove(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, path)
	delete(f.dirs, path)
}

func (f *fakeFS) corruptFile(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[path]
	if !ok {
		panic("fakefs: corrupt an absent file " + path)
	}
	if len(data) == 0 {
		data = []byte{0}
	}
	data[0] ^= 0xFF
	f.files[path] = data
}

// ─── RemoteFS implementation ───────────────────────────────────────────────

func (f *fakeFS) Lstat(path string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirs[path] {
		return fakeFileInfo{name: filepath.Base(path), dir: true}, nil
	}
	if data, ok := f.files[path]; ok {
		return fakeFileInfo{name: filepath.Base(path), size: int64(len(data))}, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeFS) Mkdir(path string, mode os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dirs[path] {
		return fs.ErrExist
	}
	if _, ok := f.files[path]; ok {
		return errors.New("fakefs: path exists and is not a directory")
	}
	parent := filepath.Dir(path)
	if parent != path && !f.dirs[parent] {
		return fs.ErrNotExist
	}
	f.dirs[path] = true
	return nil
}

func (f *fakeFS) Create(path string, mode os.FileMode) (deploy.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirs[filepath.Dir(path)] {
		return nil, fs.ErrNotExist
	}
	f.creates++
	if filepath.Base(path) != ".install-complete" {
		f.binaryCreates++
	}
	return &fakeFile{fs: f, path: path}, nil
}

func (f *fakeFS) SyncDir(string) error { return nil }

func (f *fakeFS) Rename(src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.files[src]; ok {
		delete(f.files, src)
		f.files[dst] = data
		return nil
	}
	if f.dirs[src] {
		delete(f.dirs, src)
		f.dirs[dst] = true
		return nil
	}
	return fs.ErrNotExist
}

func (f *fakeFS) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[path]; ok {
		delete(f.files, path)
		return nil
	}
	if f.dirs[path] {
		delete(f.dirs, path)
		return nil
	}
	return fs.ErrNotExist
}

func (f *fakeFS) ReadDir(dir string) ([]fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirs[dir] {
		return nil, fs.ErrNotExist
	}
	var out []fs.FileInfo
	for p := range f.dirs {
		if filepath.Dir(p) == dir && p != dir {
			out = append(out, fakeFileInfo{name: filepath.Base(p), dir: true})
		}
	}
	for p := range f.files {
		if filepath.Dir(p) == dir {
			out = append(out, fakeFileInfo{name: filepath.Base(p), size: int64(len(f.files[p]))})
		}
	}
	return out, nil
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

// fakeFile is a RemoteFS File handle. The payload becomes visible on Close,
// like the real adapter's write boundary.
type fakeFile struct {
	fs   *fakeFS
	path string
	buf  []byte
}

func (ff *fakeFile) Write(p []byte) (int, error) {
	ff.fs.mu.Lock()
	defer ff.fs.mu.Unlock()
	if ff.fs.failAt > 0 && ff.fs.written+len(p) > ff.fs.failAt {
		// The upload is interrupted midway: deliver the partial prefix,
		// then fail.
		n := ff.fs.failAt - ff.fs.written
		if n < 0 {
			n = 0
		}
		ff.buf = append(ff.buf, p[:n]...)
		ff.fs.written += n
		return n, errors.New("fakefs: write interrupted at byte bound")
	}
	ff.buf = append(ff.buf, p...)
	ff.fs.written += len(p)
	return len(p), nil
}

func (ff *fakeFile) Sync() error { return nil }

func (ff *fakeFile) Close() error {
	ff.fs.mu.Lock()
	defer ff.fs.mu.Unlock()
	if ff.fs.corruptWrites && len(ff.buf) > 0 {
		ff.buf[0] ^= 0xFF
	}
	ff.fs.files[ff.path] = ff.buf
	return nil
}

var _ deploy.File = (*fakeFile)(nil)

// fakeFileInfo implements fs.FileInfo for the fake.
type fakeFileInfo struct {
	name string
	size int64
	dir  bool
}

func (fi fakeFileInfo) Name() string       { return fi.name }
func (fi fakeFileInfo) Size() int64        { return fi.size }
func (fi fakeFileInfo) Mode() os.FileMode  { return 0o600 }
func (fi fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fi fakeFileInfo) IsDir() bool        { return fi.dir }
func (fi fakeFileInfo) Sys() any           { return nil }

var _ io.Writer = (*fakeFile)(nil)
