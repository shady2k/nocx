package assistant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
)

func writeVersionTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func canonicalOf(t *testing.T, path string) string {
	t.Helper()
	// A temporary directory is reached through a symlink on macOS, and the
	// pane answers the RESOLVED spelling; comparing with EvalSymlinks keeps
	// this assertion right on both platforms.
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return canonical
}

func TestFileVersion_CapturesThePanesVersion(t *testing.T) {
	path := writeVersionTestFile(t, "small.txt", "hello")
	source := newLocalFileSource(t)

	version, err := CaptureFileVersion(t.Context(), source, path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	if version.Path != path || version.Canonical != canonicalOf(t, path) {
		t.Fatalf("version = %+v, want path %q and its canonical identity", version, path)
	}
	if version.Version.Size != int64(len("hello")) || version.Version.MtimeNs == 0 || version.Version.SHA256 == "" {
		t.Fatalf("version = %+v, want size, modification time and sha256 from the pane", version.Version)
	}
	if err := VerifyFileVersion(t.Context(), source, version); err != nil {
		t.Fatalf("VerifyFileVersion of unchanged bytes: %v", err)
	}
}

func TestFileVersion_RejectsChangedFileAndNamesIt(t *testing.T) {
	path := writeVersionTestFile(t, "changed.txt", "before")
	source := newLocalFileSource(t)
	version, err := CaptureFileVersion(t.Context(), source, path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	err = os.WriteFile(path, []byte("after"), 0o600)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = VerifyFileVersion(t.Context(), source, version)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("VerifyFileVersion error = %v, want a refusal naming %q", err, path)
	}
}

func TestFileVersion_RejectsRenameOverOnRealFilesystem(t *testing.T) {
	path := writeVersionTestFile(t, "replaced.txt", "old")
	source := newLocalFileSource(t)
	version, err := CaptureFileVersion(t.Context(), source, path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement.tmp")
	if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile replacement: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if err := VerifyFileVersion(t.Context(), source, version); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("VerifyFileVersion error = %v, want rename-over refusal naming %q", err, path)
	}
}

// The path stops naming the object the person was shown, while the object it
// used to name is still on disk with exactly the same bytes. A comparison of
// sizes and digests alone would pass; the canonical identity is what catches
// it.
func TestFileVersion_RejectsAPathThatNowNamesAnotherObject(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("same bytes"), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	source := newLocalFileSource(t)
	version, err := CaptureFileVersion(t.Context(), source, link)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	if version.Canonical != canonicalOf(t, target) {
		t.Fatalf("canonical = %q, want the symlink's target %q", version.Canonical, target)
	}
	err = os.Remove(link)
	if err != nil {
		t.Fatalf("Remove link: %v", err)
	}
	err = os.WriteFile(link, []byte("same bytes"), 0o600)
	if err != nil {
		t.Fatalf("WriteFile link: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	err = os.Chtimes(link, info.ModTime(), info.ModTime())
	if err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	err = VerifyFileVersion(t.Context(), source, version)
	var mismatch *FileVersionMismatchError
	if !errors.As(err, &mismatch) || !strings.Contains(err.Error(), link) {
		t.Fatalf("VerifyFileVersion error = %v, want a mismatch naming %q", err, link)
	}
}

func TestFileVersion_CaptureWithoutABindingFails(t *testing.T) {
	path := writeVersionTestFile(t, "unbound.txt", "content")
	if _, err := CaptureFileVersion(t.Context(), nil, path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("CaptureFileVersion(nil source) error = %v, want a refusal naming %q", err, path)
	}
}

func TestFileVersion_VerificationWithoutABindingFails(t *testing.T) {
	path := writeVersionTestFile(t, "unbound-verify.txt", "content")
	source := newLocalFileSource(t)
	version, err := CaptureFileVersion(t.Context(), source, path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	if err := VerifyFileVersion(t.Context(), nil, version); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("VerifyFileVersion(nil source) error = %v, want a refusal naming %q", err, path)
	}
}

func TestFileVersion_CaptureOfAMissingPathIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")
	_, err := CaptureFileVersion(t.Context(), newLocalFileSource(t), path)
	if err == nil {
		t.Fatal("CaptureFileVersion of a missing path succeeded")
	}
	if !isNotFound(err) {
		t.Fatalf("CaptureFileVersion error = %v, want the pane's ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("CaptureFileVersion error = %v, want it to name %q", err, path)
	}
}

// A file over the shared read ceiling has no version: a version of a prefix is
// not the file's version, and binding one would authorise bytes nobody read.
func TestFileVersion_CaptureRefusesAFileOverTheReadCeiling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.bin")
	if err := os.WriteFile(path, make([]byte, filesystem.MaxReadBytes+1), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := CaptureFileVersion(t.Context(), newLocalFileSource(t), path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("CaptureFileVersion error = %v, want a refusal naming %q", err, path)
	}
}

func TestFileVersion_AcquireFailureIsVisible(t *testing.T) {
	path := writeVersionTestFile(t, "acquire-fails.txt", "content")
	_, err := CaptureFileVersion(t.Context(), failingFileSource{err: errors.New("binding refused")}, path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "binding refused") {
		t.Fatalf("CaptureFileVersion error = %v, want a named acquire failure", err)
	}
}

// The handle lives for one read and is released on every path. Closing the
// binding after the reads is what observes it: a use-guard still held would
// make Registry.Close abandon its drain and say so (ErrCloseAbandoned), so a
// clean close is the assertion that no handle outlived its call.
func TestFileVersion_EveryCaptureAndVerificationReleasesItsHandle(t *testing.T) {
	path := writeVersionTestFile(t, "released.txt", "content")
	source, provider := newForeignFileSource(t, filesystem.Version{Size: 7, MtimeNs: 1_700_000_000_000_000_000, SHA256: strings.Repeat("ab", 32)})

	version, err := CaptureFileVersion(t.Context(), source, path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	if err := VerifyFileVersion(t.Context(), source, version); err != nil {
		t.Fatalf("VerifyFileVersion: %v", err)
	}
	if err := source.closeAll(); err != nil {
		t.Fatalf("closing the binding after two reads: %v", err)
	}
	if got := provider.state(); got.closed != 1 || got.reads != 2 {
		t.Fatalf("closed = %d, reads = %d; want two reads and one binding torn down", got.closed, got.reads)
	}
}

func TestApprovalStore_CarriesAndChecksEveryApprovedFileVersion(t *testing.T) {
	first := writeVersionTestFile(t, "first.txt", "first")
	second := writeVersionTestFile(t, "second.txt", "second")
	source := newLocalFileSource(t)
	firstVersion, err := CaptureFileVersion(t.Context(), source, first)
	if err != nil {
		t.Fatalf("CaptureFileVersion first: %v", err)
	}
	secondVersion, err := CaptureFileVersion(t.Context(), source, second)
	if err != nil {
		t.Fatalf("CaptureFileVersion second: %v", err)
	}
	proposal := Approval{
		RunID: "run-1", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingCaptured,
		FileVersions:     []FileVersion{firstVersion, secondVersion},
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(Approval{RunID: proposal.RunID, Attempt: proposal.Attempt, Tool: proposal.Tool, CallID: proposal.CallID, ArgHash: proposal.ArgHash}) {
		t.Fatal("Approve returned false")
	}
	versions, ok := store.ApprovedFileVersions(proposal)
	if !ok || len(versions) != 2 || versions[0].Path != first || versions[1].Path != second {
		t.Fatalf("ApprovedFileVersions = %v, %v; want both approved paths", versions, ok)
	}
	if err := store.VerifyApprovedFileVersions(t.Context(), source, proposal); err != nil {
		t.Fatalf("VerifyApprovedFileVersions: %v", err)
	}
	if err := os.WriteFile(second, []byte("changed"), 0o600); err != nil {
		t.Fatalf("WriteFile second: %v", err)
	}
	if err := store.VerifyApprovedFileVersions(t.Context(), source, proposal); err == nil || !strings.Contains(err.Error(), second) {
		t.Fatalf("verification error = %v, want changed second path", err)
	}
}

func TestApprovalStore_AllowsExplicitNoFileBinding(t *testing.T) {
	proposal := Approval{
		RunID: "run-no-files", Attempt: 1, Tool: "notes.search", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingNotApplicable,
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(proposal) {
		t.Fatal("Approve returned false")
	}
	if err := store.VerifyApprovedFileVersions(t.Context(), nil, proposal); err != nil {
		t.Fatalf("VerifyApprovedFileVersions: %v, want no-file approval to be allowed", err)
	}
}

func TestApprovalStore_RefusesFileBindingWithoutCapturedVersions(t *testing.T) {
	proposal := Approval{
		RunID: "run-missing-files", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingRequired,
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(proposal) {
		t.Fatal("Approve returned false")
	}
	err := store.VerifyApprovedFileVersions(t.Context(), newLocalFileSource(t), proposal)
	if err == nil || !strings.Contains(err.Error(), "file version") {
		t.Fatalf("VerifyApprovedFileVersions error = %v, want missing file-version refusal", err)
	}
}

// A captured approval with no source to re-read it through cannot prove the
// bytes are the ones the person saw. "Could not check" must not read as
// "unchanged".
func TestApprovalStore_RefusesCapturedBindingWithNoSource(t *testing.T) {
	path := writeVersionTestFile(t, "captured.txt", "content")
	version, err := CaptureFileVersion(t.Context(), newLocalFileSource(t), path)
	if err != nil {
		t.Fatalf("CaptureFileVersion: %v", err)
	}
	proposal := Approval{
		RunID: "run-captured", Attempt: 1, Tool: "files.read", CallID: "call-1", ArgHash: "args",
		FileVersionState: FileVersionBindingCaptured,
		FileVersions:     []FileVersion{version},
	}
	store := NewApprovalStore()
	store.Request(proposal)
	if !store.Approve(proposal) {
		t.Fatal("Approve returned false")
	}
	if err := store.VerifyApprovedFileVersions(t.Context(), nil, proposal); err == nil {
		t.Fatal("a captured approval verified with no source to read it through")
	}
}

// The evidence path must not reach this machine's disk at all: every byte it
// sees comes from the pane's binding. This is a source-level ratchet, because
// the failure it guards is a line of code that compiles and passes every
// functional test on a local pane — it only misbehaves on somebody else's host.
func TestFileVersion_HasNoDirectDiskAccess(t *testing.T) {
	data, err := os.ReadFile("fileversion.go")
	if err != nil {
		t.Fatalf("read fileversion.go: %v", err)
	}
	for _, forbidden := range []string{`"os"`, "os.Stat(", "os.ReadFile(", "os.Lstat(", "statIdentity", "osFileVersionSource"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("fileversion.go still contains %q: approval evidence must come from the pane's Handle, never from this machine's disk", forbidden)
		}
	}
}

// failingFileSource is a run whose binding cannot be acquired at all.
type failingFileSource struct{ err error }

func (s failingFileSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return nil, nil, s.err
}

// foreignProvider is a pane that is not this machine: it answers the version it
// was given, for any path, and never touches this one's disk. It counts the
// reads and the closes so a test can assert which route the evidence took.
type foreignProvider struct {
	canonical string
	version   filesystem.Version

	mu     sync.Mutex
	reads  int
	closed int
}

type foreignProviderState struct {
	reads  int
	closed int
}

func (p *foreignProvider) Root(context.Context) (filesystem.Root, error) {
	return filesystem.Root{Path: "/"}, nil
}

func (p *foreignProvider) List(context.Context, string, filesystem.Page) (filesystem.Listing, error) {
	return filesystem.Listing{}, nil
}

func (p *foreignProvider) Read(context.Context, string, int64) (filesystem.Content, error) {
	return filesystem.Content{}, errors.New("foreign provider: no read through this seam")
}

func (p *foreignProvider) Stat(context.Context, string) (filesystem.Stat, error) {
	return filesystem.Stat{Kind: filesystem.KindRegular}, nil
}

func (p *foreignProvider) Canonical(_ context.Context, path string) (string, error) {
	if p.canonical == "" {
		return path, nil
	}
	return p.canonical, nil
}

func (p *foreignProvider) Watch(context.Context, string) (filesystem.Watch, error) {
	return nil, errors.New("foreign provider: no watches")
}

func (p *foreignProvider) Close() error {
	p.mu.Lock()
	p.closed++
	p.mu.Unlock()
	return nil
}

func (p *foreignProvider) Versioned() filesystem.VersionedFiles { return p }

func (p *foreignProvider) ReadVersion(context.Context, string, int64) ([]byte, filesystem.Version, error) {
	p.mu.Lock()
	p.reads++
	p.mu.Unlock()
	return []byte("remote bytes"), p.version, nil
}

func (p *foreignProvider) Commit(context.Context, string, []byte, filesystem.Expect) (filesystem.Version, error) {
	return filesystem.Version{}, errors.New("foreign provider: writes are not part of this fixture")
}

func (p *foreignProvider) state() foreignProviderState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return foreignProviderState{reads: p.reads, closed: p.closed}
}

// newForeignFileSource is a real HandleSource over a pane that is not this
// machine, which is what the transport's factory hands a remote session.
func newForeignFileSource(t *testing.T, version filesystem.Version) (*foreignFileSource, *foreignProvider) {
	t.Helper()
	provider := &foreignProvider{version: version}
	reg := filesystem.New()
	id, err := reg.Register(provider, testFilesSession, "", filesystem.Capabilities{Versioned: provider.Versioned()})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(id) })
	return &foreignFileSource{reg: reg, id: id}, provider
}

type foreignFileSource struct {
	reg *filesystem.Registry
	id  string
}

func (s *foreignFileSource) Acquire(context.Context) (filesystem.Handle, func(), error) {
	return s.reg.Acquire(s.id, testOwns{})
}

// closeAll tears the binding down. A use-guard still held makes Close answer
// ErrCloseAbandoned instead of waiting forever, which is what a test asserts
// on when it claims every handle was released.
func (s *foreignFileSource) closeAll() error { return s.reg.Close(s.id) }
