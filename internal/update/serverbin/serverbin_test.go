package serverbin_test

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/update/serverbin"
)

// ---------------------------------------------------------------------------
// A filesystem that can be told to fail
// ---------------------------------------------------------------------------

// faultFS wraps the real filesystem and fails ONE named operation. The
// happy path underneath stays genuine — a fully in-memory double would let
// this package pass while doing something the real os package refuses —
// and every external call gets a test in which it fails without the test
// depending on who is running it. In a container that is root, and root
// makes every chmod-based refusal test pass for the wrong reason.
type faultFS struct {
	serverbin.FS
	op    string // "stat", "mkdirall", "open", "create", "rename", "remove", "readdir", "syncdir"
	match string // only fail when the path contains this; empty matches any
	err   error
	// truncate drops this many bytes from every write, producing a copy
	// that is short rather than absent — the failure a hash check exists
	// to catch and a size check would miss on a same-length corruption.
	truncate int
}

func (f *faultFS) fails(op, path string) bool {
	return f.op == op && (f.match == "" || strings.Contains(path, f.match))
}

func (f *faultFS) Stat(p string) (iofs.FileInfo, error) {
	if f.fails("stat", p) {
		return nil, f.err
	}
	return f.FS.Stat(p)
}

func (f *faultFS) MkdirAll(p string, m os.FileMode) error {
	if f.fails("mkdirall", p) {
		return f.err
	}
	return f.FS.MkdirAll(p, m)
}

func (f *faultFS) Open(p string) (io.ReadCloser, error) {
	if f.fails("open", p) {
		return nil, f.err
	}
	return f.FS.Open(p)
}

func (f *faultFS) Create(p string, m os.FileMode) (serverbin.File, error) {
	if f.fails("create", p) {
		return nil, f.err
	}
	h, err := f.FS.Create(p, m)
	if err != nil || f.truncate == 0 {
		return h, err
	}
	return &shortFile{File: h, drop: f.truncate}, nil
}

func (f *faultFS) Rename(a, b string) error {
	if f.fails("rename", b) || f.fails("rename", a) {
		return f.err
	}
	return f.FS.Rename(a, b)
}

func (f *faultFS) Remove(p string) error {
	if f.fails("remove", p) {
		return f.err
	}
	return f.FS.Remove(p)
}

func (f *faultFS) ReadDir(p string) ([]iofs.DirEntry, error) {
	if f.fails("readdir", p) {
		return nil, f.err
	}
	return f.FS.ReadDir(p)
}

func (f *faultFS) SyncDir(p string) error {
	if f.fails("syncdir", p) {
		return f.err
	}
	return f.FS.SyncDir(p)
}

// shortFile writes everything but the last drop bytes of each write.
type shortFile struct {
	serverbin.File
	drop int
}

func (s *shortFile) Write(p []byte) (int, error) {
	n := len(p) - s.drop
	if n < 0 {
		n = 0
	}
	written, err := s.File.Write(p[:n])
	if err != nil {
		return written, err
	}
	// Report the full length: a short write the caller is TOLD about is
	// an ordinary error, and the interesting corruption is the one that
	// claims to have succeeded.
	return len(p), nil
}

func newFault(op, match string, err error) *faultFS {
	return &faultFS{FS: serverbin.NewOSFS(), op: op, match: match, err: err}
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

var errInjected = errors.New("injected failure")

// writeSource puts a fake coordinator binary on disk and returns its path.
func writeSource(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "nocx-server")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil { //nolint:gosec // fixture must be executable
		t.Fatal(err)
	}
	return p
}

func newInstaller(fs serverbin.FS) *serverbin.Installer {
	return serverbin.New(fs, nil)
}

// ---------------------------------------------------------------------------
// The versioned name
// ---------------------------------------------------------------------------

func TestEnsure_NamesTheCopyByVersionAndContentHash(t *testing.T) {
	src := writeSource(t, t.TempDir(), "coordinator v0.2.0")
	binDir := filepath.Join(t.TempDir(), "bin")

	got, err := newInstaller(serverbin.NewOSFS()).Ensure(context.Background(), src, binDir, "0.2.0")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	want := "nocx-server-0.2.0-" + got.Hash
	if got.Name != want {
		t.Errorf("name: got %q, want %q", got.Name, want)
	}
	if got.Path != filepath.Join(binDir, want) {
		t.Errorf("path: got %q, want %q", got.Path, filepath.Join(binDir, want))
	}
	if len(got.Hash) != 64 {
		t.Errorf("hash is not a sha256 hex digest: %q", got.Hash)
	}
	if !got.Fresh {
		t.Error("the first install must report itself fresh")
	}

	fi, err := os.Stat(got.Path)
	if err != nil {
		t.Fatalf("the copy is not on disk: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("the copy is not executable by its owner: mode %v", fi.Mode().Perm())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("the copy is readable beyond its owner: mode %v", fi.Mode().Perm())
	}
}

// Two different builds of ONE version must not collide on one name — the
// hash is what makes the name a claim about content.
func TestEnsure_SameVersionDifferentContentGetsADifferentName(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	inst := newInstaller(serverbin.NewOSFS())

	a, err := inst.Ensure(context.Background(), writeSource(t, t.TempDir(), "build A"), binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := inst.Ensure(context.Background(), writeSource(t, t.TempDir(), "build B"), binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name == b.Name {
		t.Fatalf("two different binaries share one name %q — the old one would go on running", a.Name)
	}
	for _, p := range []string{a.Path, b.Path} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is missing: %v", p, err)
		}
	}
}

func TestEnsure_SecondCallWritesNothing(t *testing.T) {
	src := writeSource(t, t.TempDir(), "coordinator")
	binDir := filepath.Join(t.TempDir(), "bin")
	inst := newInstaller(serverbin.NewOSFS())

	first, err := inst.Ensure(context.Background(), src, binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}

	second, err := inst.Ensure(context.Background(), src, binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if second.Fresh {
		t.Error("an already-installed copy was reinstalled")
	}
	after, err := os.Stat(second.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("the copy was rewritten when an identical one was already installed")
	}
}

// A file sitting under the right name whose content is wrong is not an
// install. It is repaired, because a daemon spawned from it would be
// neither the version its name claims nor anything anyone can name.
func TestEnsure_RepairsACopyThatDoesNotMatchItsOwnName(t *testing.T) {
	src := writeSource(t, t.TempDir(), "coordinator")
	binDir := filepath.Join(t.TempDir(), "bin")
	inst := newInstaller(serverbin.NewOSFS())

	first, err := inst.Ensure(context.Background(), src, binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(first.Path, []byte("tampered"), 0o700); werr != nil { //nolint:gosec // the fixture must be executable to stand for an installed copy
		t.Fatal(werr)
	}

	again, err := inst.Ensure(context.Background(), src, binDir, "0.2.0")
	if err != nil {
		t.Fatalf("Ensure over a corrupted copy: %v", err)
	}
	if !again.Fresh {
		t.Error("a corrupted copy was accepted as already installed")
	}
	data, err := os.ReadFile(again.Path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "coordinator" {
		t.Errorf("the copy was not repaired: %q", data)
	}
}

func TestEnsure_RefusesAnEmptyVersion(t *testing.T) {
	src := writeSource(t, t.TempDir(), "coordinator")
	_, err := newInstaller(serverbin.NewOSFS()).Ensure(context.Background(), src, t.TempDir(), "")
	if err == nil {
		t.Fatal("a copy with no version in its name cannot be superseded — Ensure must refuse")
	}
}

func TestSiblingPath_IsTheBinaryBesideTheApplication(t *testing.T) {
	cases := map[string]string{
		"/Applications/nocx.app/Contents/MacOS/nocx": "/Applications/nocx.app/Contents/MacOS/nocx-server",
		"/tmp/.mount_nocxAB/usr/bin/nocx":            "/tmp/.mount_nocxAB/usr/bin/nocx-server",
	}
	for exe, want := range cases {
		if got := serverbin.SiblingPath(exe); got != want {
			t.Errorf("SiblingPath(%q) = %q, want %q", exe, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// The hash gate — acceptance 5
// ---------------------------------------------------------------------------

// A copy that is written short must fail its hash check and must not be
// promoted: nothing appears under the final name, and no debris is left
// behind under the temporary one.
func TestEnsure_TruncatedCopyIsNotPromoted(t *testing.T) {
	src := writeSource(t, t.TempDir(), "a coordinator binary long enough to lose its tail")
	binDir := filepath.Join(t.TempDir(), "bin")

	fs := &faultFS{FS: serverbin.NewOSFS(), truncate: 8}
	_, err := newInstaller(fs).Ensure(context.Background(), src, binDir, "0.2.0")
	if !errors.Is(err, serverbin.ErrCorruptCopy) {
		t.Fatalf("a truncated copy must fail its hash check, got: %v", err)
	}

	entries, rerr := os.ReadDir(binDir)
	if rerr != nil {
		t.Fatalf("read %s: %v", binDir, rerr)
	}
	for _, e := range entries {
		t.Errorf("the failed install left %q behind", e.Name())
	}
}

// The same shape with a corruption that keeps the length: a size check
// would pass it and a content hash does not.
func TestEnsure_SameLengthCorruptionIsNotPromoted(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "AAAA")
	binDir := filepath.Join(dir, "bin")

	fs := &corruptingFS{FS: serverbin.NewOSFS()}
	_, err := newInstaller(fs).Ensure(context.Background(), src, binDir, "0.2.0")
	if !errors.Is(err, serverbin.ErrCorruptCopy) {
		t.Fatalf("a same-length corruption must fail the hash check, got: %v", err)
	}
	if entries, _ := os.ReadDir(binDir); len(entries) != 0 {
		t.Errorf("the failed install left %d files behind", len(entries))
	}
}

type corruptingFS struct{ serverbin.FS }

func (c *corruptingFS) Create(p string, m os.FileMode) (serverbin.File, error) {
	h, err := c.FS.Create(p, m)
	if err != nil {
		return nil, err
	}
	return &flipFile{File: h}, nil
}

type flipFile struct{ serverbin.File }

func (f *flipFile) Write(p []byte) (int, error) {
	q := make([]byte, len(p))
	copy(q, p)
	for i := range q {
		q[i] ^= 0xFF
	}
	return f.File.Write(q)
}

// ---------------------------------------------------------------------------
// Failure paths — one per external call, each paired with a success above
// ---------------------------------------------------------------------------

func TestEnsure_EveryExternalCallHasAFailure(t *testing.T) {
	cases := []struct {
		name  string
		op    string
		match string
	}{
		// The source is inside the AppImage; a FUSE mount can go away
		// between the launcher deciding and this call reading.
		{"source unreadable", "open", "nocx-server"},
		// ~/.local/share may be unwritable, or a file may sit where the
		// directory should be.
		{"bin directory cannot be created", "mkdirall", ""},
		// A full disk refuses the create rather than the write.
		{"temporary copy cannot be created", "create", ""},
		// The promote step. Its failure must leave nothing behind.
		{"promotion cannot rename", "rename", ""},
		// The durability step, after the file is in place.
		{"directory cannot be synced", "syncdir", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeSource(t, dir, "coordinator")
			binDir := filepath.Join(dir, "bin")

			fs := newFault(tc.op, tc.match, errInjected)
			_, err := newInstaller(fs).Ensure(context.Background(), src, binDir, "0.2.0")
			if !errors.Is(err, errInjected) {
				t.Fatalf("the injected %s failure must reach the caller, got: %v", tc.op, err)
			}
			// Nothing may be promoted by a failed install. syncdir is the
			// one step that runs AFTER the rename, so it is allowed to
			// leave the copy in place — the file is complete, only its
			// durability is unproven, and the caller is told.
			if tc.op == "syncdir" {
				return
			}
			entries, _ := os.ReadDir(binDir)
			for _, e := range entries {
				t.Errorf("a failed install left %q behind", e.Name())
			}
		})
	}
}

// The two calls on the handle itself. A Sync that fails means the bytes may
// not be on the platter; a Close that fails means the write may not have
// been flushed at all. Both must abort the install rather than promote a
// copy whose durability nobody can vouch for.
func TestEnsure_HandleFailuresAbortTheInstall(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(serverbin.File) serverbin.File
	}{
		{"sync fails", func(f serverbin.File) serverbin.File { return &failingHandle{File: f, onSync: errInjected} }},
		{"close fails", func(f serverbin.File) serverbin.File { return &failingHandle{File: f, onClose: errInjected} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeSource(t, dir, "coordinator")
			binDir := filepath.Join(dir, "bin")

			fs := &wrappingFS{FS: serverbin.NewOSFS(), wrap: tc.wrap}
			if _, err := newInstaller(fs).Ensure(context.Background(), src, binDir, "0.2.0"); !errors.Is(err, errInjected) {
				t.Fatalf("the injected handle failure must reach the caller, got: %v", err)
			}
			entries, _ := os.ReadDir(binDir)
			for _, e := range entries {
				t.Errorf("a failed install left %q behind", e.Name())
			}
		})
	}
}

type wrappingFS struct {
	serverbin.FS
	wrap func(serverbin.File) serverbin.File
}

func (w *wrappingFS) Create(p string, m os.FileMode) (serverbin.File, error) {
	f, err := w.FS.Create(p, m)
	if err != nil {
		return nil, err
	}
	return w.wrap(f), nil
}

type failingHandle struct {
	serverbin.File
	onSync  error
	onClose error
}

func (h *failingHandle) Sync() error {
	if h.onSync != nil {
		return h.onSync
	}
	return h.File.Sync()
}

func (h *failingHandle) Close() error {
	cerr := h.File.Close()
	if h.onClose != nil {
		return h.onClose
	}
	return cerr
}

// A Stat that fails for a reason other than absence is not "not
// installed": treating it as absence would reinstall over a copy a running
// daemon may be executing from.
func TestEnsure_StatFailureIsNotReadAsAbsence(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "coordinator")
	binDir := filepath.Join(dir, "bin")

	fs := newFault("stat", "nocx-server-0.2.0-", errInjected)
	if _, err := newInstaller(fs).Ensure(context.Background(), src, binDir, "0.2.0"); !errors.Is(err, errInjected) {
		t.Fatalf("a stat failure must be reported, got: %v", err)
	}
}

func TestEnsure_HonoursACancelledContext(t *testing.T) {
	dir := t.TempDir()
	src := writeSource(t, dir, "coordinator")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newInstaller(serverbin.NewOSFS()).Ensure(ctx, src, filepath.Join(dir, "bin"), "0.2.0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled install must stop, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Prune — acceptance 4
// ---------------------------------------------------------------------------

func TestPrune_RemovesSupersededCopiesAndNeverTheOneInUse(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	inst := newInstaller(serverbin.NewOSFS())
	ctx := context.Background()

	old, err := inst.Ensure(ctx, writeSource(t, t.TempDir(), "v1"), binDir, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	older, err := inst.Ensure(ctx, writeSource(t, t.TempDir(), "v0"), binDir, "0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	current, err := inst.Ensure(ctx, writeSource(t, t.TempDir(), "v2"), binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}

	// A file this package does not own must survive untouched.
	foreign := filepath.Join(binDir, "nocx-server-backup-by-hand")
	if err := os.WriteFile(foreign, []byte("mine"), 0o700); err != nil { //nolint:gosec // ditto
		t.Fatal(err)
	}

	if err := inst.Prune(ctx, binDir, current.Name); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if _, err := os.Stat(current.Path); err != nil {
		t.Errorf("the copy in use was pruned: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file this package does not own was deleted: %v", err)
	}
	for _, gone := range []serverbin.Install{old, older} {
		if _, err := os.Stat(gone.Path); !os.IsNotExist(err) {
			t.Errorf("superseded copy %s survived pruning", gone.Name)
		}
	}
}

func TestPrune_RefusesToKeepNothing(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	inst := newInstaller(serverbin.NewOSFS())

	in, err := inst.Ensure(context.Background(), writeSource(t, t.TempDir(), "v1"), binDir, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Prune(context.Background(), binDir, ""); err == nil {
		t.Fatal("Prune with no copy to keep must refuse rather than empty the directory")
	}
	if _, err := os.Stat(in.Path); err != nil {
		t.Errorf("the refused prune deleted something anyway: %v", err)
	}
}

func TestPrune_MissingDirectoryIsNotAnError(t *testing.T) {
	err := newInstaller(serverbin.NewOSFS()).
		Prune(context.Background(), filepath.Join(t.TempDir(), "never-created"), "nocx-server-0.1.0-"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("nothing installed is nothing to prune: %v", err)
	}
}

func TestPrune_ReportsAReadFailure(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	inst := newInstaller(serverbin.NewOSFS())
	in, err := inst.Ensure(context.Background(), writeSource(t, t.TempDir(), "v1"), binDir, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}

	fs := newFault("readdir", "", errInjected)
	if err := newInstaller(fs).Prune(context.Background(), binDir, in.Name); !errors.Is(err, errInjected) {
		t.Fatalf("a directory that cannot be read must be reported, got: %v", err)
	}
}

func TestPrune_ReportsARemoveFailure(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	inst := newInstaller(serverbin.NewOSFS())
	old, err := inst.Ensure(context.Background(), writeSource(t, t.TempDir(), "v1"), binDir, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	current, err := inst.Ensure(context.Background(), writeSource(t, t.TempDir(), "v2"), binDir, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}

	fs := newFault("remove", old.Name, errInjected)
	if err := newInstaller(fs).Prune(context.Background(), binDir, current.Name); !errors.Is(err, errInjected) {
		t.Fatalf("a copy that cannot be removed must be reported, got: %v", err)
	}
}
