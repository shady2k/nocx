package hostsvc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// journalInstanceSeq gives every service this package constructs its own
// journal instance id: two services in one test binary must not contend for one
// directory, and only a test that WANTS them to share one arranges it.
var journalInstanceSeq atomic.Uint64

// newTestService builds the shipped service over provider with a journal under
// this test's own temporary directory — never the machine's home — and closes it
// after the test, which is what releases the journal's lock.
func newTestService(t *testing.T, provider *local.Provider) *hostsvc.Service {
	t.Helper()
	svc, err := hostsvc.New(provider, hostsvc.JournalConfig{
		Root:       t.TempDir(),
		Hostname:   "test-host",
		InstanceID: fmt.Sprintf("%032x", journalInstanceSeq.Add(1)),
	})
	if err != nil {
		t.Fatalf("new fs service: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("closing the fs service: %v", err)
		}
	})
	return svc
}

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

func params(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

func call[P any](t *testing.T, svc *hostsvc.Service, op string, in P) (any, error) {
	t.Helper()
	return svc.Call(context.Background(), op, params(t, in))
}

func rootThrough(t *testing.T, svc *hostsvc.Service, in proto.FSRootParams) proto.FSRootResult {
	t.Helper()
	result, err := call(t, svc, proto.OpRoot, in)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	r, ok := result.(proto.FSRootResult)
	if !ok {
		t.Fatalf("root result is %T, want proto.FSRootResult", result)
	}
	return r
}

func statThrough(t *testing.T, svc *hostsvc.Service, path string) proto.FSStatResult {
	t.Helper()
	result, err := call(t, svc, proto.OpStat, proto.FSPathParams{Path: path})
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	r, ok := result.(proto.FSStatResult)
	if !ok {
		t.Fatalf("stat result is %T, want proto.FSStatResult", result)
	}
	return r
}

func listThrough(t *testing.T, svc *hostsvc.Service, path string, offset, limit int) proto.FSListResult {
	t.Helper()
	result, err := call(t, svc, proto.OpList, proto.FSListParams{Path: path, Offset: offset, Limit: limit})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	r, ok := result.(proto.FSListResult)
	if !ok {
		t.Fatalf("list result is %T, want proto.FSListResult", result)
	}
	return r
}

func refusal(t *testing.T, svc *hostsvc.Service, err error) (string, json.RawMessage) {
	t.Helper()
	if err == nil {
		t.Fatal("the op succeeded, want a refusal")
	}
	code, details := svc.Refusal(err)
	if code == "" {
		t.Fatalf("error %v has no wire code", err)
	}
	return code, details
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root; a directory's permission bits do not refuse it")
	}
}

// TestRootAnswersTheVerifiedPath: a caller that verified a working directory
// gets that directory, not inferred; with nothing verified the provider's own
// root answers, which is where the composition root's override lives.
func TestRootAnswersTheVerifiedPath(t *testing.T) {
	base := tempDir(t)
	verified := filepath.Join(base, "project")
	mustMkdir(t, verified)

	svc := newTestService(t, local.New(local.WithRoot(verified)))
	got := rootThrough(t, svc, proto.FSRootParams{})
	if got.Path != verified {
		t.Fatalf("root path = %q, want the provider's verified override %q", got.Path, verified)
	}
	if got.Inferred {
		t.Fatal("a verified override was reported as inferred")
	}

	other := filepath.Join(base, "elsewhere")
	mustMkdir(t, other)
	got = rootThrough(t, svc, proto.FSRootParams{Path: other})
	if got.Path != other || got.Inferred {
		t.Fatalf("root for an explicit path = %+v, want %q not inferred", got, other)
	}
}

// TestRootFallsBackToHomeWithAReason is the other end of the same interval: a
// path that is gone by the time the panel opens is a labelled inference, not a
// root that does not exist and not an error.
func TestRootFallsBackToHomeWithAReason(t *testing.T) {
	base := tempDir(t)
	svc := newTestService(t, local.New())
	got := rootThrough(t, svc, proto.FSRootParams{Path: filepath.Join(base, "vanished")})
	if !got.Inferred {
		t.Fatal("an unusable path was reported as a verified root")
	}
	if got.InferredReason == "" {
		t.Fatal("an inferred root carried no reason")
	}
}

// TestStatReportsTheMetadataARowShows: kind, size, mtime and mode come from
// the object the path names, with symlinks followed.
func TestStatReportsTheMetadataARowShows(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "note.txt")
	mustWrite(t, path, []byte("three")) // five bytes, so a size of 3 would be a defect
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	got := statThrough(t, svc, path)
	if got.Kind != "regular" || got.Size != 5 || got.Mode != uint32(info.Mode()) {
		t.Fatalf("stat = %+v, want a regular 5-byte file with mode %#o", got, info.Mode())
	}
	if got.MtimeNs != info.ModTime().UnixNano() {
		t.Fatalf("mtimeNs = %d, want %d", got.MtimeNs, info.ModTime().UnixNano())
	}

	link := filepath.Join(dir, "note-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if linked := statThrough(t, svc, link); linked.Kind != got.Kind || linked.Size != got.Size {
		t.Fatalf("stat through a symlink = %+v, want the target's %+v", linked, got)
	}
}

// TestListPagesAndCarriesTheWholeDirectoryDigest: total and rev describe the
// complete directory, while the page carries only what was asked for.
func TestListPagesAndCarriesTheWholeDirectoryDigest(t *testing.T) {
	dir := tempDir(t)
	mustMkdir(t, filepath.Join(dir, "adir"))
	mustWrite(t, filepath.Join(dir, "b.txt"), nil)
	mustWrite(t, filepath.Join(dir, "c.txt"), nil)

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	first := listThrough(t, svc, dir, 0, 1)
	if len(first.Entries) != 1 || first.Total != 3 || !first.HasMore {
		t.Fatalf("first page = %d entries, total %d, hasMore %v; want 1 of 3 with more", len(first.Entries), first.Total, first.HasMore)
	}
	if first.Entries[0].Kind != "dir" {
		t.Fatalf("first row is %+v, want the directory first", first.Entries[0])
	}
	if len(first.Rev) != 64 || strings.Trim(first.Rev, "0123456789abcdef") != "" {
		t.Fatalf("rev = %q, want a sha256 hex digest", first.Rev)
	}
	if first.Canonical != dir {
		t.Fatalf("canonical = %q, want %q", first.Canonical, dir)
	}

	last := listThrough(t, svc, dir, first.Total, 10)
	if last.HasMore || len(last.Entries) != 0 || last.Entries == nil {
		t.Fatalf("page at the end = %+v, want an empty non-nil page", last)
	}
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("an empty page marshals as %s", raw)
	}
}

// TestListKeepsLexicalEntryPathsUnderASymlinkedParent is the canonicalisation
// rule AGENTS.md states: the directory listed has an identity (Canonical), but
// the rows keep the spelling the caller asked for, or the tree a client draws
// would move under it. The parent is behind a symlink in every run, so Linux
// fails too if the two are confused.
func TestListKeepsLexicalEntryPathsUnderASymlinkedParent(t *testing.T) {
	base := tempDir(t)
	dir := filepath.Join(base, "real")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "file.txt"), nil)
	link := filepath.Join(base, "through-link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	svc := newTestService(t, local.New(local.WithRoot(base)))
	got := listThrough(t, svc, link, 0, 10)
	if got.Canonical != dir {
		t.Fatalf("canonical = %q, want the resolved %q", got.Canonical, dir)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	if want := filepath.Join(link, "file.txt"); got.Entries[0].Path != want {
		t.Fatalf("entry path = %q, want the lexical %q", got.Entries[0].Path, want)
	}
}

// TestListPastTheEndIsAnEmptyPage: an offset past the directory's total is
// answered the way local.List answers it — an empty, non-nil page carrying the
// real total and rev — because deleting an entry between two pages produces
// exactly that page, and the total a caller already holds is what resolves the
// race. A refusal here would make this service disagree with the provider on
// the same tree.
func TestListPastTheEndIsAnEmptyPage(t *testing.T) {
	dir := tempDir(t)
	mustMkdir(t, filepath.Join(dir, "adir"))
	mustWrite(t, filepath.Join(dir, "b.txt"), nil)

	provider := local.New(local.WithRoot(dir))
	want, err := provider.List(context.Background(), dir, filesystem.Page{Offset: 5, Limit: 10})
	if err != nil {
		t.Fatalf("local.List: %v", err)
	}
	got := listThrough(t, newTestService(t, provider), dir, 5, 10)
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want an empty non-nil slice", got.Entries)
	}
	if got.HasMore {
		t.Fatal("a page past the end reported more")
	}
	if got.Total != want.Total || got.Offset != want.Offset || got.Rev != want.Rev {
		t.Fatalf("total %d offset %d rev %q; local.List answers total %d offset %d rev %q",
			got.Total, got.Offset, got.Rev, want.Total, want.Offset, want.Rev)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("an empty page marshals as %s", raw)
	}
}

// TestListOnAFileIsNotDir: the provider answers ENOTDIR and the service keeps
// that answer rather than inventing an empty listing.
func TestListOnAFileIsNotDir(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "file.txt")
	mustWrite(t, path, nil)
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	_, err := call(t, svc, proto.OpList, proto.FSListParams{Path: path, Offset: 0, Limit: 10})
	if code, _ := refusal(t, svc, err); code != proto.FSCodeNotDir {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeNotDir)
	}
}

// TestCanonicalResolvesThePath: the answer is the provider's canonical
// identity, with every symlink resolved.
func TestCanonicalResolvesThePath(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "target.txt")
	mustWrite(t, target, nil)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	result, err := call(t, svc, proto.OpCanonical, proto.FSPathParams{Path: link})
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	got, ok := result.(proto.FSCanonicalResult)
	if !ok {
		t.Fatalf("canonical result is %T", result)
	}
	if got.Canonical != target {
		t.Fatalf("canonical = %q, want %q", got.Canonical, target)
	}
}

// TestPermissionDeniedLeavesTheServiceServing is scenario 14's host half: a
// directory nobody may read answers with the typed refusal, and the next
// request on the same service is answered normally. The connection outlives a
// refusal because the host answers one request's error, not the process.
func TestPermissionDeniedLeavesTheServiceServing(t *testing.T) {
	skipIfRoot(t)
	dir := tempDir(t)
	closed := filepath.Join(dir, "closed")
	mustMkdir(t, closed)
	mustWrite(t, filepath.Join(closed, "inside.txt"), nil)
	open := filepath.Join(dir, "open")
	mustMkdir(t, open)
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 — restoring the fixture directory's mode; a directory needs
	// its traversal bits back for cleanup to remove what is inside it.
	t.Cleanup(func() { _ = os.Chmod(closed, 0o750) })

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	_, err := call(t, svc, proto.OpList, proto.FSListParams{Path: closed, Offset: 0, Limit: 10})
	if code, _ := refusal(t, svc, err); code != proto.FSCodePermissionDenied {
		t.Fatalf("code = %q, want %q", code, proto.FSCodePermissionDenied)
	}
	if got := rootThrough(t, svc, proto.FSRootParams{}); got.Path != dir {
		t.Fatalf("the service stopped answering after a refusal: root = %q, want %q", got.Path, dir)
	}
	if got := listThrough(t, svc, open, 0, 10); got.Total != 0 {
		t.Fatalf("a readable empty directory listed %d entries", got.Total)
	}
}

// TestEntryCapIsTooLargeWithTheObservedCount: the provider's cap is the
// service's too; Observed and Limit travel so the caller can say how far past
// it the directory was.
func TestEntryCapIsTooLargeWithTheObservedCount(t *testing.T) {
	dir := tempDir(t)
	for _, name := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(dir, name), nil)
	}
	svc := newTestService(t, local.New(local.WithRoot(dir), local.WithEntryCap(2)))
	_, err := call(t, svc, proto.OpList, proto.FSListParams{Path: dir, Offset: 0, Limit: 10})
	code, details := refusal(t, svc, err)
	if code != proto.FSCodeTooLarge {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeTooLarge)
	}
	var d struct {
		Observed int `json:"observed"`
		Limit    int `json:"limit"`
	}
	if err := json.Unmarshal(details, &d); err != nil {
		t.Fatalf("details: %v", err)
	}
	if d.Observed != 3 || d.Limit != 2 {
		t.Fatalf("details = %+v, want observed 3 and limit 2", d)
	}
}

// TestSizeCapIsTooLargeSizeWithTheObservedBytes: the listing's BYTE cap is a
// different limit from the entry cap and keeps a different code, because
// whoever recovers the error type behind the refusal — the helperfs leaf does
// it with errors.As — cannot tell the two apart under one word. observed is not
// asserted exactly: the provider measures the wire cost of the whole listing
// and this test does not re-derive it, which would be a second implementation
// of the cap.
func TestSizeCapIsTooLargeSizeWithTheObservedBytes(t *testing.T) {
	dir := tempDir(t)
	mustWrite(t, filepath.Join(dir, "a.txt"), nil)
	svc := newTestService(t, local.New(local.WithRoot(dir), local.WithSizeCap(1)))
	_, err := call(t, svc, proto.OpList, proto.FSListParams{Path: dir, Offset: 0, Limit: 10})
	code, details := refusal(t, svc, err)
	if code != proto.FSCodeTooLargeSize {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeTooLargeSize)
	}
	var d struct {
		Observed int64 `json:"observed"`
		Limit    int64 `json:"limit"`
	}
	if err := json.Unmarshal(details, &d); err != nil {
		t.Fatalf("details: %v", err)
	}
	if d.Limit != 1 || d.Observed <= 1 {
		t.Fatalf("details = %+v, want an observed above the 1-byte limit", d)
	}
}

// TestListElapsedCapIsTimedOut: the provider's operational cap reaches the
// wire as the plan's timed-out code, not as an internal failure.
func TestListElapsedCapIsTimedOut(t *testing.T) {
	dir := tempDir(t)
	mustWrite(t, filepath.Join(dir, "a"), nil)
	svc := newTestService(t, local.New(local.WithRoot(dir), local.WithListTimeout(time.Nanosecond)))
	_, err := call(t, svc, proto.OpList, proto.FSListParams{Path: dir, Offset: 0, Limit: 10})
	if code, _ := refusal(t, svc, err); code != proto.FSCodeTimedOut {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeTimedOut)
	}
}

// TestInvalidAndMissingPathsKeepTheirOwnCodes: a relative path is the
// provider's syntax refusal with its reason, and a path that does not exist is
// not-found — two facts a caller acts on differently.
func TestInvalidAndMissingPathsKeepTheirOwnCodes(t *testing.T) {
	dir := tempDir(t)
	svc := newTestService(t, local.New(local.WithRoot(dir)))

	_, err := call(t, svc, proto.OpStat, proto.FSPathParams{Path: "relative/path"})
	code, details := refusal(t, svc, err)
	if code != proto.FSCodeInvalidPath {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeInvalidPath)
	}
	var d struct {
		Reason string `json:"reason"`
	}
	if unmarshalErr := json.Unmarshal(details, &d); unmarshalErr != nil {
		t.Fatalf("details: %v", unmarshalErr)
	}
	if d.Reason == "" {
		t.Fatal("an invalid path carried no reason")
	}

	_, err = call(t, svc, proto.OpStat, proto.FSPathParams{Path: filepath.Join(dir, "absent")})
	if got, _ := refusal(t, svc, err); got != proto.FSCodeNotFound {
		t.Fatalf("code = %q, want %q", got, proto.FSCodeNotFound)
	}
}
