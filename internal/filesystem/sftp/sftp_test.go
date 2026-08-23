package sftp

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transfer"
)

// tempDir is t.TempDir() with its symlinks already resolved, and everything in
// this package — the tests and the fake's served root — builds its paths on it.
//
// On macOS $TMPDIR lives under /var, which is a symlink to /private/var. A real
// SFTP server answers RealPath with a canonicalised path, so the fake must too
// (fsfake_test.go); if the served root were the unresolved path instead, the
// fake's RealPath(".") and its RealPath(abs) would answer in two different
// vocabularies, and Root would compare a canonical override against a lexical
// home and fail to abbreviate it under ~. Resolving once at the root keeps the
// fake faithful to the protocol it stands in for. Linux has no such symlink, so
// this is a no-op there and the tests read identically on both.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// skipIfRoot: permission tests are meaningless when the test process can
// read anything.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks do not apply")
	}
}

// TestSSHLeaseSatisfiesSeam pins the wiring the package depends on: the
// committed ssh.FSConn satisfies the read half of the seam, so a real lease
// can serve every listing call. A change to the lease that drops a method
// breaks this test with a clear compile error.
//
// It pins the read half only. The write half is transfer.RemoteFS, whose
// Create returns transfer.RemoteFile where the lease returns ssh.FSFile —
// two names for one shape, and Go matches result types by identity — so the
// lease reaches this provider through an adapter built at the composition
// root, which is also the only place allowed to know both vocabularies.
func TestSSHLeaseSatisfiesSeam(t *testing.T) {
	var _ fsReader = ssh.FSConn(nil)
}

// TestProviderImplementsContract pins the package's reason to exist: the same
// Provider contract as local, satisfied by the sftp provider.
func TestProviderImplementsContract(t *testing.T) {
	var _ filesystem.Provider = (*Provider)(nil)
}

// uploaderSeam is the compile-time half of rule R1's positive direction: a
// remote provider carries the write seam.
var uploaderSeam filesystem.Uploader = (*Provider)(nil)

// TestRemoteProviderIsAnUploader states the positive direction at runtime
// too, so the assertion above cannot be deleted silently.
func TestRemoteProviderIsAnUploader(t *testing.T) {
	if _, ok := any(New(newFakeFS(t))).(filesystem.Uploader); !ok {
		t.Fatal("the sftp provider must implement Uploader — a remote tab is what can be uploaded to")
	}
	_ = uploaderSeam
}

// TestBothProvidersAreUploaders is the other half of the pair, and it is
// here — beside the remote one — because the two are the same claim seen
// from two sides: whoever this backend can list files for, it can write a
// file for.
//
// It asserted the OPPOSITE until D7 was corrected: that local must NOT
// implement Uploader, because R1 was read as "only a remote tab may be
// written to". That was reasoned from the desktop build, where a drop on a
// local tab yields an absolute path and inserting it is the whole gesture.
// A browser drop yields bytes and no path, and the machine those bytes
// belong on is the backend's own — which IS the machine that tab's shell is
// on, so R1 is satisfied rather than bent. R1's structural expression moved
// with it: it is now "a provider that cannot write implements no Uploader
// and its binding holds a nil sink", asserted in
// internal/filesystem/upload_test.go, not "local is that provider".
func TestBothProvidersAreUploaders(t *testing.T) {
	if _, ok := any(local.New()).(filesystem.Uploader); !ok {
		t.Error("local must implement Uploader — a browser drop on a local tab has bytes and no path, so the upload is the only thing the gesture can mean")
	}
	if _, ok := any(New(newFakeFS(t))).(filesystem.Uploader); !ok {
		t.Error("sftp must implement Uploader — the remote path is unchanged by the local one landing")
	}
}

// TestSinkWritesThroughTheLease is the paired success of the seam: the sink
// the provider hands out actually writes a file on the machine the lease
// views, through the lease and nothing else.
func TestSinkWritesThroughTheLease(t *testing.T) {
	f := newFakeFS(t)
	p := New(f)

	out, err := p.Sink().Put(context.Background(),
		transfer.Upload{DestDir: f.root, Name: "landed.txt", Size: 5, OnExists: transfer.Overwrite},
		strings.NewReader("hello"), func(int64) {})
	if err != nil {
		t.Fatalf("Put through the provider's sink: %v", err)
	}
	if out.State != transfer.StateWritten || out.FinalName != "landed.txt" {
		t.Fatalf("outcome %+v, want landed.txt written", out)
	}
	if len(out.Stranded) != 0 {
		t.Errorf("stranded %v, want nothing left behind", out.Stranded)
	}
	got, err := os.ReadFile(filepath.Join(f.root, "landed.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("file holds %q, want %q", got, "hello")
	}
}

// TestSinkReportsALeaseFailure is the failure path of that same external
// call: a lease that cannot write is reported, and the provider does not
// invent a success.
func TestSinkReportsALeaseFailure(t *testing.T) {
	f := newFakeFS(t)
	_ = f.Close() // a released lease: every call answers errLeaseClosed

	_, err := New(f).Sink().Put(context.Background(),
		transfer.Upload{DestDir: f.root, Name: "landed.txt", Size: 5, OnExists: transfer.Overwrite},
		strings.NewReader("hello"), nil)
	if !errors.Is(err, errLeaseClosed) {
		t.Fatalf("Put on a closed lease: %v, want the lease's own error", err)
	}
}

// ---------------------------------------------------------------------------
// Root
// ---------------------------------------------------------------------------

func TestRootDefaultIsInferredHome(t *testing.T) {
	f := newFakeFS(t)
	root, err := New(f).Root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if root.Path != f.root {
		t.Errorf("Path = %q, want the served home %q", root.Path, f.root)
	}
	if !root.Inferred || root.InferredReason == "" {
		t.Errorf("default root not labelled inferred: %+v", root)
	}
	if root.Display != "~" {
		t.Errorf("Display = %q, want ~", root.Display)
	}
}

func TestRootWithOverride(t *testing.T) {
	ctx := context.Background()
	f := newFakeFS(t)
	sub := filepath.Join(f.root, "project")
	mustMkdir(t, sub)
	r, err := New(f, WithRoot(sub)).Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != sub || r.Inferred || r.InferredReason != "" {
		t.Errorf("verified root = %+v, want %q not inferred", r, sub)
	}
	if r.Display != "~/"+filepath.Base(sub) {
		t.Errorf("Display = %q, want ~/%s (abbreviated under the remote home)", r.Display, filepath.Base(sub))
	}
	// The override is checked at call time: an unusable root falls back,
	// labelled — never silently served.
	gone := filepath.Join(f.root, "gone")
	r, err = New(f, WithRoot(gone)).Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Inferred || r.InferredReason == "" {
		t.Errorf("unusable override not labelled inferred: %+v", r)
	}
	// A file is not a usable root.
	file := filepath.Join(f.root, "f")
	mustWrite(t, file, nil)
	r, err = New(f, WithRoot(file)).Root(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Inferred {
		t.Errorf("file override served as verified root: %+v", r)
	}
}

// TestRootHomeResolutionFailure is the paired failure of Root's external
// call: when the server cannot canonicalise ".", Root reports the error
// rather than fabricating a home.
func TestRootHomeResolutionFailure(t *testing.T) {
	f := newFakeFS(t)
	f.realPathErr = errors.New("sftp: connection lost")
	_, err := New(f).Root(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sftp: connection lost") {
		t.Fatalf("Root() = %v, want the transport error wrapped", err)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestListOrdinaryDirectory(t *testing.T) {
	dir := tempDir(t)
	mustMkdir(t, filepath.Join(dir, "zdir"))
	mustMkdir(t, filepath.Join(dir, "Adir"))
	mustWrite(t, filepath.Join(dir, "a.txt"), nil)
	mustWrite(t, filepath.Join(dir, "B.txt"), nil)

	p := New(newFakeFS(t))
	l, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Directories first, then files, each by UTF-8 byte order, case-sensitive
	// ("Adir" < "zdir" because 'A' = 0x41 < 'z' = 0x7a; "B.txt" < "a.txt"
	// because 'B' = 0x42 < 'a' = 0x61 — a case-insensitive sort would order
	// those two the other way round, which is what makes the pair the whole
	// assertion). There is deliberately no "b.txt" beside "B.txt": macOS ships
	// a case-insensitive filesystem, where creating both leaves one file, and
	// the case-sensitivity of the *ordering* cannot be stated with names that
	// the filesystem refuses to keep apart.
	want := []string{"Adir", "zdir", "B.txt", "a.txt"}
	if len(l.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(l.Entries), len(want), l.Entries)
	}
	for i, w := range want {
		if l.Entries[i].Name != w {
			t.Errorf("entry %d = %q, want %q", i, l.Entries[i].Name, w)
		}
	}
	if l.Path != dir {
		t.Errorf("Listing.Path = %q, want %q", l.Path, dir)
	}
	if l.Canonical != dir {
		t.Errorf("Listing.Canonical = %q, want %q", l.Canonical, dir)
	}
	if l.Total != len(want) || l.Offset != 0 || l.HasMore {
		t.Errorf("page metadata = %+v", l)
	}
	if l.Rev == "" {
		t.Error("Rev empty")
	}
	for _, e := range l.Entries {
		if e.Path != filepath.Join(dir, e.Name) {
			t.Errorf("Entry.Path = %q, want lexical join %q", e.Path, filepath.Join(dir, e.Name))
		}
	}
}

// TestListEmptyDirectoryIsEmptySlice pins "Listing.Entries is never nil": an
// empty directory marshals as [], never null.
func TestListEmptyDirectoryIsEmptySlice(t *testing.T) {
	dir := tempDir(t)
	l, err := New(newFakeFS(t)).List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l.Entries == nil || len(l.Entries) != 0 {
		t.Fatalf("Entries = %#v, want non-nil empty slice", l.Entries)
	}
	if l.Total != 0 || l.HasMore {
		t.Errorf("empty listing metadata = %+v", l)
	}
}

func TestListNotFound(t *testing.T) {
	_, err := New(newFakeFS(t)).List(context.Background(), filepath.Join(tempDir(t), "missing"), filesystem.Page{Offset: 0, Limit: 10})
	var nf *filesystem.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err does not unwrap to fs.ErrNotExist: %v", err)
	}
}

func TestListPermissionDenied(t *testing.T) {
	skipIfRoot(t)
	parent := tempDir(t)
	locked := filepath.Join(parent, "locked")
	mustMkdir(t, locked)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 — restoring the fixture directory's mode; a directory needs 0o755 for traversal, and TempDir cleanup depends on it
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, err := New(newFakeFS(t)).List(context.Background(), locked, filesystem.Page{Offset: 0, Limit: 10})
	var pe *filesystem.ErrPermission
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ErrPermission (never a silently empty listing)", err)
	}
}

func TestListNotADirectory(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "f.txt")
	mustWrite(t, f, []byte("x"))
	_, err := New(newFakeFS(t)).List(context.Background(), f, filesystem.Page{Offset: 0, Limit: 10})
	var nd *filesystem.ErrNotDir
	if !errors.As(err, &nd) {
		t.Fatalf("err = %v, want ErrNotDir", err)
	}
}

func TestListBrokenSymlink(t *testing.T) {
	dir := tempDir(t)
	link := filepath.Join(dir, "broken")
	mustSymlink(t, filepath.Join(dir, "nowhere"), link)
	_, err := New(newFakeFS(t)).List(context.Background(), link, filesystem.Page{Offset: 0, Limit: 10})
	var nf *filesystem.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ErrNotFound for a broken symlink", err)
	}
}

// TestListSymlinkToDirResolvesCanonicalAndListsThat pins the spec's order:
// the canonical directory is resolved and then listed — identity and entries
// cannot come from two different directories. Entry paths use the lexical
// parent the caller asked about.
func TestListSymlinkToDirResolvesCanonicalAndListsThat(t *testing.T) {
	dir := tempDir(t)
	real := filepath.Join(dir, "real")
	mustMkdir(t, real)
	mustWrite(t, filepath.Join(real, "inner.txt"), []byte("x"))
	link := filepath.Join(dir, "link")
	mustSymlink(t, real, link)

	l, err := New(newFakeFS(t)).List(context.Background(), link, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l.Canonical != real {
		t.Errorf("Canonical = %q, want resolved %q", l.Canonical, real)
	}
	if len(l.Entries) != 1 || l.Entries[0].Name != "inner.txt" {
		t.Fatalf("entries = %v, want [inner.txt] from the resolved directory", l.Entries)
	}
	if got := l.Entries[0].Path; got != filepath.Join(link, "inner.txt") {
		t.Errorf("Entry.Path = %q, want lexical parent %q", got, filepath.Join(link, "inner.txt"))
	}
}

// TestListSymlinkRetargetedBetweenLists: a retargeted parent must return the
// identity AND the entries of the new target — never the identity of A with
// the entries of B (spec §5.1).
func TestListSymlinkRetargetedBetweenLists(t *testing.T) {
	dir := tempDir(t)
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	mustMkdir(t, a)
	mustMkdir(t, b)
	mustWrite(t, filepath.Join(a, "a.txt"), nil)
	mustWrite(t, filepath.Join(b, "b.txt"), nil)
	link := filepath.Join(dir, "link")
	mustSymlink(t, a, link)

	p := New(newFakeFS(t))
	ctx := context.Background()
	l1, err := p.List(ctx, link, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l1.Canonical != a || len(l1.Entries) != 1 || l1.Entries[0].Name != "a.txt" {
		t.Fatalf("first list = canonical %q entries %v", l1.Canonical, l1.Entries)
	}
	_ = os.Remove(link)
	mustSymlink(t, b, link)
	l2, err := p.List(ctx, link, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l2.Canonical != b || len(l2.Entries) != 1 || l2.Entries[0].Name != "b.txt" {
		t.Fatalf("second list = canonical %q entries %v; identity and entries must move together", l2.Canonical, l2.Entries)
	}
}

// TestSymlinkEntryMetadata: LinkTarget and LinkKind are populated for
// symlinks — regular, dir, and broken (LinkKind other).
func TestSymlinkEntryMetadata(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "f.txt")
	realDir := filepath.Join(dir, "d")
	mustWrite(t, file, []byte("x"))
	mustMkdir(t, realDir)
	toFile := filepath.Join(dir, "to-file")
	toDir := filepath.Join(dir, "to-dir")
	broken := filepath.Join(dir, "to-broken")
	mustSymlink(t, file, toFile)
	mustSymlink(t, realDir, toDir)
	mustSymlink(t, filepath.Join(dir, "gone"), broken)

	l, err := New(newFakeFS(t)).List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]filesystem.Entry{}
	for _, e := range l.Entries {
		byName[e.Name] = e
	}
	if e := byName["to-file"]; e.Kind != filesystem.KindSymlink || e.LinkKind != filesystem.KindRegular || e.LinkTarget != file {
		t.Errorf("to-file entry = %+v, want symlink→regular at %q", e, file)
	}
	if e := byName["to-dir"]; e.Kind != filesystem.KindSymlink || e.LinkKind != filesystem.KindDir || e.LinkTarget != realDir {
		t.Errorf("to-dir entry = %+v, want symlink→dir at %q", e, realDir)
	}
	if e := byName["to-broken"]; e.Kind != filesystem.KindSymlink || e.LinkKind != filesystem.KindOther {
		t.Errorf("broken entry = %+v, want symlink→other", e)
	}
	// A relative symlink target resolves from the link's directory, not the
	// process cwd.
	rel := filepath.Join(dir, "rel")
	mustSymlink(t, "f.txt", rel)
	l, err = New(newFakeFS(t)).List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Entries {
		if e.Name == "rel" && e.LinkKind != filesystem.KindRegular {
			t.Errorf("relative symlink resolved to %q, want regular", e.LinkKind)
		}
	}
}

// TestListOrderingBeforePagination: pages come out of the same deterministic
// order, so "show next N" can neither duplicate nor skip a row.
func TestListOrderingBeforePagination(t *testing.T) {
	dir := tempDir(t)
	for _, name := range []string{"d", "c", "b", "a", "e"} {
		mustWrite(t, filepath.Join(dir, name+".txt"), nil)
	}
	p := New(newFakeFS(t))
	ctx := context.Background()
	var got []string
	for off := 0; ; off += 2 {
		l, err := p.List(ctx, dir, filesystem.Page{Offset: off, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range l.Entries {
			got = append(got, e.Name)
		}
		if !l.HasMore {
			break
		}
	}
	want := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
	if len(got) != len(want) {
		t.Fatalf("paged names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paged names = %v, want %v", got, want)
		}
	}
	// An offset past the end is an empty page, not an error.
	l, err := p.List(ctx, dir, filesystem.Page{Offset: 99, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if l.Entries == nil || len(l.Entries) != 0 || l.HasMore {
		t.Fatalf("past-end page = %+v, want empty non-nil, no more", l)
	}
}

func TestListEntryCap(t *testing.T) {
	dir := tempDir(t)
	for i := 0; i < 3; i++ {
		mustWrite(t, filepath.Join(dir, "f"+string(rune('a'+i))), nil)
	}
	p := New(newFakeFS(t), WithEntryCap(2))
	_, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	var tl *filesystem.ErrTooLarge
	if !errors.As(err, &tl) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	// The enumeration was complete — the sftp readdir enumerates the whole
	// directory before returning — so the observed count is exact, never
	// "more than N".
	if tl.ObservedCount != 3 || tl.Limit != 2 {
		t.Errorf("ErrTooLarge = %+v, want {3, 2}", tl)
	}
	// Boundary: exactly at the cap is a listing, not a refusal.
	ok := New(newFakeFS(t), WithEntryCap(3))
	if _, err := ok.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("listing at the cap boundary failed: %v", err)
	}
}

// TestListTimedOut is the never-replying-server shape at the provider level:
// a listing the server never answers can only be released by its context, so
// the D14 elapsed-time cap — riding the context into the lease's ReadDir —
// is what returns ErrTimedOut. Partial results are discarded, never returned
// as if complete.
func TestListTimedOut(t *testing.T) {
	dir := tempDir(t)
	mustWrite(t, filepath.Join(dir, "f"), nil)
	f := newFakeFS(t)
	f.blockedReadDir = true
	p := New(f, WithListTimeout(time.Nanosecond))
	_, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	var to *filesystem.ErrTimedOut
	if !errors.As(err, &to) {
		t.Fatalf("err = %v, want ErrTimedOut", err)
	}
	if to.Timeout != time.Nanosecond {
		t.Errorf("ErrTimedOut = %+v, want the configured cap", to)
	}
	// And on a normal machine, with the default cap, the same directory
	// succeeds — the paired success.
	if _, err := New(newFakeFS(t)).List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("default-cap listing failed: %v", err)
	}
}

func TestListInvalidPage(t *testing.T) {
	dir := tempDir(t)
	for _, page := range []filesystem.Page{{Offset: -1, Limit: 10}, {Offset: 0, Limit: 0}, {Offset: 0, Limit: -5}} {
		_, err := New(newFakeFS(t)).List(context.Background(), dir, page)
		var ip *filesystem.ErrInvalidPage
		if !errors.As(err, &ip) {
			t.Errorf("page %+v = %v, want ErrInvalidPage", page, err)
		}
	}
}

func TestListInvalidPath(t *testing.T) {
	dir := tempDir(t)
	_, err := New(newFakeFS(t)).List(context.Background(), "relative/path", filesystem.Page{Offset: 0, Limit: 10})
	var ixp *filesystem.ErrInvalidPath
	if !errors.As(err, &ixp) {
		t.Errorf("relative path = %v, want ErrInvalidPath", err)
	}
	_, err = New(newFakeFS(t)).List(context.Background(), dir+"/./", filesystem.Page{Offset: 0, Limit: 10})
	if !errors.As(err, &ixp) {
		t.Errorf("unclean path = %v, want ErrInvalidPath", err)
	}
}

// TestListTimesOutDuringEntryBuilding: the elapsed-time cap must bound the
// whole listing, not just the readdir. Here ReadDir is fast and the per-entry
// link work is slow; the in-loop deadline check refuses instead of holding
// the slot for the full enumeration.
func TestListTimesOutDuringEntryBuilding(t *testing.T) {
	dir := tempDir(t)
	for i := range 6 {
		mustWrite(t, filepath.Join(dir, "f"+string(rune('a'+i))), nil)
	}
	p := New(newFakeFS(t), WithListTimeout(25*time.Millisecond))
	p.beforeEntry = func() { time.Sleep(10 * time.Millisecond) }
	_, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	var to *filesystem.ErrTimedOut
	if !errors.As(err, &to) {
		t.Fatalf("err = %v, want ErrTimedOut from per-entry work", err)
	}
	// Paired: the same slow per-entry work under a generous cap is a listing.
	p2 := New(newFakeFS(t), WithListTimeout(time.Second))
	p2.beforeEntry = func() { time.Sleep(10 * time.Millisecond) }
	if _, err := p2.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("generous-cap listing failed: %v", err)
	}
}

// TestListKeepsCoherenceWhenParentRetargetedMidOperation: link facts are read
// through the canonical directory actually listed, never the lexical parent.
// A lexical parent retargeted between the readdir and the entry processing
// must not swap LinkTarget and LinkKind for entries that came from the old
// target — Rev is computed over the mixture, so the mixture is the defect.
func TestListKeepsCoherenceWhenParentRetargetedMidOperation(t *testing.T) {
	dir := tempDir(t)
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	mustMkdir(t, a)
	mustMkdir(t, b)
	mustWrite(t, filepath.Join(a, "target-in-a"), nil) // the relative target a/s resolves to
	mustSymlink(t, "target-in-a", filepath.Join(a, "s"))
	link := filepath.Join(dir, "link")
	mustSymlink(t, a, link)
	p := New(newFakeFS(t))
	p.afterReadDir = func() {
		_ = os.Remove(link)
		mustSymlink(t, b, link) // b has no entry named "s"
	}
	l, err := p.List(context.Background(), link, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l.Canonical != a {
		t.Fatalf("Canonical = %q, want the directory actually listed %q", l.Canonical, a)
	}
	var e filesystem.Entry
	for _, en := range l.Entries {
		if en.Name == "s" {
			e = en
		}
	}
	if e.Name == "" {
		t.Fatalf("entries = %v, want [s] from directory a", l.Entries)
	}
	if e.LinkTarget != "target-in-a" || e.LinkKind != filesystem.KindRegular {
		t.Fatalf("entry = %+v, want the link target of directory a's entry, not the retargeted lexical parent's", e)
	}
}

// TestListResponseSizeCeiling: the third D14 bound guards bytes, because
// equal entry counts do not cost equal bytes. A few short names pass a small
// cap; the same count of long names is refused.
func TestListResponseSizeCeiling(t *testing.T) {
	short := tempDir(t)
	for i := range 3 {
		mustWrite(t, filepath.Join(short, "f"+string(rune('a'+i))), nil)
	}
	p := New(newFakeFS(t), WithSizeCap(1024))
	if _, err := p.List(context.Background(), short, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("small listing refused by the size cap: %v", err)
	}
	long := tempDir(t)
	for i := range 5 {
		mustWrite(t, filepath.Join(long, strings.Repeat("n", 150)+string(rune('a'+i))), nil)
	}
	_, err := p.List(context.Background(), long, filesystem.Page{Offset: 0, Limit: 10})
	var ts *filesystem.ErrTooLargeSize
	if !errors.As(err, &ts) {
		t.Fatalf("err = %v, want ErrTooLargeSize", err)
	}
	// The same long-name directory under the default cap is a listing, and a
	// disabled cap (non-positive) refuses nothing.
	if _, err := New(newFakeFS(t)).List(context.Background(), long, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("default-cap listing failed: %v", err)
	}
	if _, err := New(newFakeFS(t), WithSizeCap(0)).List(context.Background(), long, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("disabled-cap listing failed: %v", err)
	}
}

// TestListHugePageLimitDoesNotPanic: a Limit near MaxInt with a nonzero
// Offset wraps negative in the naive lo+Limit arithmetic and panics on the
// slice bounds. The pagination must saturate.
func TestListHugePageLimitDoesNotPanic(t *testing.T) {
	dir := tempDir(t)
	for _, n := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(dir, n), nil)
	}
	p := New(newFakeFS(t))
	ctx := context.Background()
	l, err := p.List(ctx, dir, filesystem.Page{Offset: 0, Limit: math.MaxInt})
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 3 || len(l.Entries) != 3 || l.HasMore {
		t.Fatalf("max-limit page = %+v, want all 3 entries and no more", l)
	}
	l, err = p.List(ctx, dir, filesystem.Page{Offset: 2, Limit: math.MaxInt})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 1 || l.HasMore {
		t.Fatalf("offset-2 max-limit page = %+v, want 1 entry", l)
	}
	l, err = p.List(ctx, dir, filesystem.Page{Offset: math.MaxInt / 2, Limit: math.MaxInt})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 0 || l.HasMore {
		t.Fatalf("far-offset page = %+v, want empty and no more", l)
	}
}

// TestEntryWithUnreadableLinkMetadataIsDistinguishable: a symlink whose link
// text can no longer be read (vanished between the readdir and the entry
// processing, forced by the seam) is KindSymlink with an unreadable link,
// distinguishable from a genuinely broken link, and refused by the
// openability table like any other unreadable object.
func TestEntryWithUnreadableLinkMetadataIsDistinguishable(t *testing.T) {
	dir := tempDir(t)
	link := filepath.Join(dir, "x")
	mustSymlink(t, filepath.Join(dir, "gone"), link)
	p := New(newFakeFS(t))
	p.beforeEntry = func() { _ = os.Remove(link) }
	l, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 1 {
		t.Fatalf("entries = %v, want the vanished link still counted", l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != filesystem.KindSymlink || e.LinkKind != filesystem.KindUnreadable {
		t.Fatalf("entry = %+v, want symlink with unreadable link, not a broken one", e)
	}
	if filesystem.CanOpen(e.Kind, e.LinkKind) || filesystem.CanExpand(e.Kind, e.LinkKind) {
		t.Error("unreadable entry is openable or expandable")
	}
}

// TestSymlinkInaccessibleTargetIsNotBroken: a symlink whose target exists
// but cannot be statted (permission denied) is distinguishable from a
// genuinely broken link, which has no target at all.
func TestSymlinkInaccessibleTargetIsNotBroken(t *testing.T) {
	skipIfRoot(t)
	dir := tempDir(t)
	locked := filepath.Join(dir, "locked")
	mustMkdir(t, locked)
	target := filepath.Join(locked, "target")
	mustWrite(t, target, []byte("x"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 — restoring the fixture directory's mode; a directory needs 0o755 for traversal, and TempDir cleanup depends on it
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	link := filepath.Join(dir, "link")
	mustSymlink(t, target, link)

	l, err := New(newFakeFS(t)).List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var e filesystem.Entry
	for _, en := range l.Entries {
		if en.Name == "link" {
			e = en
		}
	}
	if e.Name == "" {
		t.Fatalf("entries = %v, want the link", l.Entries)
	}
	if e.Kind != filesystem.KindSymlink || e.LinkKind != filesystem.KindUnreadable {
		t.Fatalf("entry = %+v, want symlink with an unreadable target, not a broken one", e)
	}
	if e.LinkTarget != target {
		t.Errorf("LinkTarget = %q, want %q", e.LinkTarget, target)
	}
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

func TestReadOrdinaryFile(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "f.txt")
	body := []byte("hello world")
	mustWrite(t, f, body)
	before := time.Now()
	// ensure mtime is not "now" by construction; filesystems round
	_ = os.Chtimes(f, before.Add(-time.Hour), before.Add(-time.Hour))

	c, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "hello world" {
		t.Errorf("Text = %q", c.Text)
	}
	if c.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", c.Size, len(body))
	}
	if c.Canonical != f {
		t.Errorf("Canonical = %q, want %q", c.Canonical, f)
	}
	if c.Truncated || c.Binary || c.Lossy || c.Changed {
		t.Errorf("flags = %+v, all false expected", c)
	}
	if !c.ModTime.Equal(before.Add(-time.Hour)) {
		t.Errorf("ModTime = %v, want the file's mtime", c.ModTime)
	}
}

// TestReadRespectsMaxBytes: the parameter can only lower the ceiling.
func TestReadRespectsMaxBytes(t *testing.T) {
	dir := tempDir(t)
	big := filepath.Join(dir, "big.txt")
	mustWrite(t, big, []byte(strings.Repeat("x", 200)))
	c, err := New(newFakeFS(t)).Read(context.Background(), big, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.Size != 100 || c.Text != strings.Repeat("x", 100) {
		t.Errorf("capped read = %+v, want 100 bytes truncated", c)
	}
	small := filepath.Join(dir, "small.txt")
	mustWrite(t, small, []byte("tiny"))
	c, err = New(newFakeFS(t)).Read(context.Background(), small, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Truncated || c.Size != 4 || c.Text != "tiny" {
		t.Errorf("small read = %+v, want 4 bytes untruncated", c)
	}
}

// TestReadDefaultLimitIs2MiB: maxBytes <= 0 means the 2 MiB server ceiling,
// and the read is truncated at exactly it.
func TestReadDefaultLimitIs2MiB(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "big")
	mustWrite(t, f, []byte(strings.Repeat("a", 2<<20+100)))
	for _, maxBytes := range []int64{0, -1, 1 << 30} { // default, negative, huge → all 2 MiB
		c, err := New(newFakeFS(t)).Read(context.Background(), f, maxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !c.Truncated || c.Size != 2<<20 {
			t.Errorf("maxBytes=%d: size %d truncated %v, want 2 MiB truncated", maxBytes, c.Size, c.Truncated)
		}
	}
}

// TestReadHugeFileNeverWhole: the memory guard holds for a file far larger
// than the limit — the read must not pull the whole file.
func TestReadHugeFileNeverWhole(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "huge")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(f, 4<<30); err != nil { // 4 GiB sparse — instant
		t.Fatal(err)
	}
	start := time.Now()
	c, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.Size != 2<<20 {
		t.Fatalf("size %d truncated %v, want 2 MiB truncated", c.Size, c.Truncated)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("read of a sparse 4 GiB file took far too long")
	}
}

func TestReadNotFound(t *testing.T) {
	_, err := New(newFakeFS(t)).Read(context.Background(), filepath.Join(tempDir(t), "missing"), 0)
	var nf *filesystem.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestReadPermissionDenied(t *testing.T) {
	skipIfRoot(t)
	dir := tempDir(t)
	f := filepath.Join(dir, "secret")
	mustWrite(t, f, []byte("x"))
	if err := os.Chmod(f, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f, 0o600) })
	_, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	var pe *filesystem.ErrPermission
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
}

// TestReadDirectoryRefused pins the openability table enforced from call-time
// metadata: a directory is not readable, whatever a previous list said.
func TestReadDirectoryRefused(t *testing.T) {
	dir := tempDir(t)
	_, err := New(newFakeFS(t)).Read(context.Background(), dir, 0)
	var nr *filesystem.ErrNotRegular
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v, want ErrNotRegular", err)
	}
	if nr.Kind != filesystem.KindDir {
		t.Errorf("ErrNotRegular.Kind = %q, want dir", nr.Kind)
	}
}

// TestReadFIFORefused: a FIFO blocks forever on open — the only guard is the
// openability check, and it must fire before the read.
func TestReadFIFORefused(t *testing.T) {
	dir := tempDir(t)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(newFakeFS(t)).Read(context.Background(), fifo, 0)
	var nr *filesystem.ErrNotRegular
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v, want ErrNotRegular", err)
	}
	if nr.Kind != filesystem.KindOther {
		t.Errorf("ErrNotRegular.Kind = %q, want other", nr.Kind)
	}
}

func TestReadSymlinkResolves(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "target.txt")
	mustWrite(t, target, []byte("through the link"))
	link := filepath.Join(dir, "link.txt")
	mustSymlink(t, target, link)
	c, err := New(newFakeFS(t)).Read(context.Background(), link, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "through the link" {
		t.Errorf("Text = %q, want the target's content", c.Text)
	}
	if c.Canonical != target {
		t.Errorf("Canonical = %q, want resolved %q", c.Canonical, target)
	}
}

func TestReadBrokenSymlink(t *testing.T) {
	dir := tempDir(t)
	link := filepath.Join(dir, "broken")
	mustSymlink(t, filepath.Join(dir, "gone"), link)
	_, err := New(newFakeFS(t)).Read(context.Background(), link, 0)
	var nf *filesystem.ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestReadRetargetedSymlink: a symlink retargeted between the list and the
// read is read at its NEW target — call-time metadata, never the listed kind.
func TestReadRetargetedSymlink(t *testing.T) {
	dir := tempDir(t)
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	mustWrite(t, a, []byte("AAA"))
	mustWrite(t, b, []byte("BBB"))
	link := filepath.Join(dir, "link.txt")
	mustSymlink(t, a, link)

	p := New(newFakeFS(t))
	ctx := context.Background()
	l, err := p.List(ctx, dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var entry filesystem.Entry
	for _, e := range l.Entries {
		if e.Name == "link.txt" {
			entry = e
		}
	}
	if entry.LinkKind != filesystem.KindRegular {
		t.Fatalf("listed link kind = %q, want regular", entry.LinkKind)
	}
	_ = os.Remove(link)
	mustSymlink(t, b, link)
	c, err := p.Read(ctx, link, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "BBB" || c.Canonical != b {
		t.Errorf("read = %+v, want the new target %q", c, b)
	}
}

// TestReadSwappedToFIFOAfterList: the entry was a regular file when listed;
// the path is a FIFO when read. The openability check must be enforced from
// metadata read at call time, and refuse — never open the FIFO.
func TestReadSwappedToFIFOAfterList(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "f.txt")
	mustWrite(t, f, []byte("innocent"))
	p := New(newFakeFS(t))
	ctx := context.Background()
	l, err := p.List(ctx, dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range l.Entries {
		if e.Name == "f.txt" && e.Kind == filesystem.KindRegular {
			found = true
		}
	}
	if !found {
		t.Fatal("f.txt not listed as regular")
	}
	_ = os.Remove(f)
	if fifoErr := syscall.Mkfifo(f, 0o600); fifoErr != nil {
		t.Fatal(fifoErr)
	}
	_, err = p.Read(ctx, f, 0)
	var nr *filesystem.ErrNotRegular
	if !errors.As(err, &nr) {
		t.Fatalf("err = %v, want ErrNotRegular from call-time metadata", err)
	}
}

// TestReadNULAtByte9000: the NUL heuristic is over the bytes actually read.
func TestReadNULAtByte9000(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "mixed")
	mustWrite(t, f, append([]byte(strings.Repeat("a", 9000)), []byte{0, 'x'}...))
	c, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Binary {
		t.Error("Binary = false, want true (NUL among the bytes read)")
	}
	if c.Text != "" {
		t.Errorf("Text = %q, want empty for a binary", c.Text)
	}
}

// TestReadNULBeyondWindowIsText: "A binary whose first bytes are NUL-free
// reads as text; accepted" — the heuristic is over what was read.
func TestReadNULBeyondWindowIsText(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "front-text")
	mustWrite(t, f, append([]byte(strings.Repeat("a", 5000)), 0))
	c, err := New(newFakeFS(t)).Read(context.Background(), f, 100)
	if err != nil {
		t.Fatal(err)
	}
	if c.Binary {
		t.Error("Binary = true for a NUL beyond the read window")
	}
	if c.Text != strings.Repeat("a", 100) {
		t.Errorf("Text = %q, want the first 100 bytes", c.Text)
	}
}

func TestReadInvalidUTF8IsLossy(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "latin1")
	mustWrite(t, f, []byte{'a', 0xFF, 0xFE, 'b'})
	c, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Lossy {
		t.Error("Lossy = false, want true for invalid UTF-8")
	}
	if !utf8.ValidString(c.Text) {
		t.Error("Text is not valid UTF-8")
	}
	if c.Text != "a\uFFFDb" {
		t.Errorf("Text = %q, want one replacement for the contiguous invalid run", c.Text)
	}
}

// TestReadLiteralReplacementCharIsNotLossy: a valid U+FFFD in the source is
// not a replacement — Lossy means sequences were replaced.
func TestReadLiteralReplacementCharIsNotLossy(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "replacement")
	mustWrite(t, f, []byte("\uFFFD literal"))
	c, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Lossy {
		t.Error("Lossy = true for a literal valid U+FFFD")
	}
	if c.Text != "\uFFFD literal" {
		t.Errorf("Text = %q", c.Text)
	}
}

// TestReadChangedOnMidReadGrowth pins the Changed interval: size and mtime
// are sampled before and after; a difference sets Changed. The test seam runs
// between the read and the after-stat, so the growth is deterministic — no
// timing window.
func TestReadChangedOnMidReadGrowth(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "growing.txt")
	mustWrite(t, f, []byte("hello"))
	p := New(newFakeFS(t))
	p.afterRead = func() {
		// #nosec G304 — the seam appends to the exact fixture under test, a path built from tempDir(t)
		fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Errorf("append open: %v", err)
			return
		}
		defer func() { _ = fh.Close() }()
		if _, werr := fh.Write([]byte(" world")); werr != nil {
			t.Errorf("append write: %v", werr)
		}
	}
	c, err := p.Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Changed {
		t.Error("Changed = false, want true: the file grew during the read")
	}
	if c.Text != "hello" || c.Size != 5 {
		t.Errorf("content = %q size %d, want the pre-change bytes", c.Text, c.Size)
	}
}

// TestReadChangedOnMidReadMtime: the mtime leg of the same interval.
func TestReadChangedOnMidReadMtime(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "touched.txt")
	mustWrite(t, f, []byte("steady"))
	base := time.Now().Add(-time.Hour)
	_ = os.Chtimes(f, base, base)
	p := New(newFakeFS(t))
	p.afterRead = func() {
		_ = os.Chtimes(f, base.Add(time.Minute), base.Add(time.Minute))
	}
	c, err := p.Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Changed {
		t.Error("Changed = false, want true: the mtime moved during the read")
	}
}

// TestReadNotChangedOnSteadyRead is the paired ordinary case: the same file,
// untouched, reports Changed=false.
func TestReadNotChangedOnSteadyRead(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "steady.txt")
	mustWrite(t, f, []byte("steady"))
	c, err := New(newFakeFS(t)).Read(context.Background(), f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Changed {
		t.Error("Changed = true for an untouched file")
	}
}

// TestReadTransportErrorPassesThrough: a transport-level ReadFile failure —
// not fs-shaped — is returned as-is, never reclassified into a filesystem
// marker it is not.
func TestReadTransportErrorPassesThrough(t *testing.T) {
	dir := tempDir(t)
	f := filepath.Join(dir, "f.txt")
	mustWrite(t, f, []byte("x"))
	fs := newFakeFS(t)
	fs.readFileErr = errors.New("sftp: lease dead: hard timeout exceeded")
	_, err := New(fs).Read(context.Background(), f, 0)
	if err == nil || !strings.Contains(err.Error(), "lease dead") {
		t.Fatalf("Read = %v, want the transport error as-is", err)
	}
}

// ---------------------------------------------------------------------------
// Watch, Canonical, Close, Rev
// ---------------------------------------------------------------------------

func TestWatchUnavailableUntilTheWatchingWave(t *testing.T) {
	_, err := New(newFakeFS(t)).Watch(context.Background(), tempDir(t))
	var wu *filesystem.ErrWatchUnavailable
	if !errors.As(err, &wu) {
		t.Fatalf("err = %v, want ErrWatchUnavailable", err)
	}
}

func TestCanonicalMethod(t *testing.T) {
	dir := tempDir(t)
	real := filepath.Join(dir, "real")
	mustMkdir(t, real)
	link := filepath.Join(dir, "link")
	mustSymlink(t, real, link)
	got, err := New(newFakeFS(t)).Canonical(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Errorf("Canonical = %q, want %q", got, real)
	}
	var ixp2 *filesystem.ErrInvalidPath
	if _, err := New(newFakeFS(t)).Canonical(context.Background(), "rel"); !errors.As(err, &ixp2) {
		t.Errorf("relative canonical = %v, want ErrInvalidPath", err)
	}
}

// TestProviderClose pins the interval with both ends named: from New until
// Close returns, calls reach the connection; after Close returns, the lease
// is released and calls fail with the lease-closed error.
func TestProviderClose(t *testing.T) {
	f := newFakeFS(t)
	dir := tempDir(t)
	mustWrite(t, filepath.Join(dir, "f"), nil)
	p := New(f)
	if _, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("pre-close call failed: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if !f.closed {
		t.Error("lease not released by Close")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil (idempotent)", err)
	}
	_, err := p.List(context.Background(), dir, filesystem.Page{Offset: 0, Limit: 10})
	if !errors.Is(err, errLeaseClosed) {
		t.Fatalf("post-close List = %v, want the lease-closed error", err)
	}
}

// TestRevChangesOnAnyChange drives the digest through the real provider: an
// unchanged directory keeps its Rev, and a symlink retargeted to a file of
// the same size and kind still moves it.
func TestRevChangesOnAnyChange(t *testing.T) {
	dir := tempDir(t)
	mustWrite(t, filepath.Join(dir, "f.txt"), []byte("AAAA"))
	link := filepath.Join(dir, "link")
	mustSymlink(t, filepath.Join(dir, "f.txt"), link)

	p := New(newFakeFS(t))
	ctx := context.Background()
	l1, err := p.List(ctx, dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	l2, err := p.List(ctx, dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l1.Rev != l2.Rev {
		t.Fatal("Rev changed for an unchanged directory")
	}
	// Same size, same kind, different target: only LinkTarget moved.
	other := filepath.Join(dir, "other.txt")
	mustWrite(t, other, []byte("BBBB")) // same length as AAAA
	_ = os.Remove(link)
	mustSymlink(t, other, link)
	l3, err := p.List(ctx, dir, filesystem.Page{Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if l3.Rev == l1.Rev {
		t.Fatal("Rev unchanged after a same-size same-kind symlink retarget")
	}
}
