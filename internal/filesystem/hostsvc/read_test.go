package hostsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// tempDir is t.TempDir with its symlinks resolved. On macOS $TMPDIR lives
// under /var, a symlink to /private/var, so a lexical join onto t.TempDir() is
// not the identity the provider answers with. Resolving once at the root keeps
// "canonical" and "lexical" distinguishable without resolving at every
// assertion — the rule AGENTS.md states for this repository.
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

func callParams(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

func readThrough(t *testing.T, svc *Service, p proto.FSReadParams) (proto.FSReadResult, error) {
	t.Helper()
	result, err := svc.Call(context.Background(), proto.OpRead, callParams(t, p))
	if err != nil {
		return proto.FSReadResult{}, err
	}
	r, ok := result.(proto.FSReadResult)
	if !ok {
		t.Fatalf("read result is %T, want proto.FSReadResult", result)
	}
	return r, nil
}

func refusalCode(t *testing.T, svc *Service, err error) (string, json.RawMessage) {
	t.Helper()
	code, details := svc.Refusal(err)
	if code == "" {
		t.Fatalf("error %v has no refusal code", err)
	}
	return code, details
}

// TestReadWholeReturnsTheVersionOfTheReturnedBytes is the quiet half of
// scenario 12: an untouched file reads once, reports no retry, and its version
// is the digest of exactly the bytes in Data.
func TestReadWholeReturnsTheVersionOfTheReturnedBytes(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "quiet.txt")
	content := []byte("a quiet file\n")
	mustWrite(t, path, content)

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	r, err := readThrough(t, svc, proto.FSReadParams{Path: path, Offset: 0, Length: 1 << 20, Whole: true})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(r.Data) != string(content) {
		t.Fatalf("data = %q, want %q", r.Data, content)
	}
	if r.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", r.Size, len(content))
	}
	if r.Changed {
		t.Error("changed = true for an untouched file")
	}
	if r.Version == nil {
		t.Fatal("a whole-file read carries no version")
	}
	sum := sha256.Sum256(content)
	if r.Version.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("version sha256 = %q, want the digest of the returned bytes", r.Version.SHA256)
	}
	if r.Version.Size != int64(len(content)) || r.Version.MtimeNs != r.MtimeNs {
		t.Fatalf("version = %+v, want size %d and the answer's mtime %d", r.Version, len(content), r.MtimeNs)
	}
	if r.Identity == "" {
		t.Error("identity is empty on a unix build; an append could not be told from a replacement")
	}
	if r.Canonical != path {
		t.Fatalf("canonical = %q, want %q", r.Canonical, path)
	}
}

// TestReadWholeRefusesAFileBiggerThanTheBound: whole means whole. A prefix is
// not the file, and a version over a prefix would be a statement about bytes
// the caller never saw.
func TestReadWholeRefusesAFileBiggerThanTheBound(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "big.txt")
	mustWrite(t, path, []byte("0123456789"))

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	_, err := readThrough(t, svc, proto.FSReadParams{Path: path, Offset: 0, Length: 4, Whole: true})
	if err == nil {
		t.Fatal("a whole-file read of a file larger than the bound succeeded")
	}
	code, details := refusalCode(t, svc, err)
	if code != proto.FSCodeTooLarge {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeTooLarge)
	}
	var d struct {
		Observed int64 `json:"observed"`
		Limit    int64 `json:"limit"`
	}
	if err := json.Unmarshal(details, &d); err != nil {
		t.Fatalf("details: %v", err)
	}
	if d.Observed != 10 || d.Limit != 4 {
		t.Fatalf("details = %+v, want observed 10 and limit 4", d)
	}
}

// TestReadRefusesPagesItCannotRead: every malformed page is the plan's single
// invalid-page code. A negative offset or a non-positive length would
// otherwise reach a slice or a ReadAt with a value nothing defines.
func TestReadRefusesPagesItCannotRead(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "file.txt")
	mustWrite(t, path, []byte("data"))

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	for _, tc := range []struct {
		name string
		p    proto.FSReadParams
	}{
		{name: "negative offset", p: proto.FSReadParams{Path: path, Offset: -1, Length: 4}},
		{name: "zero length", p: proto.FSReadParams{Path: path, Offset: 0, Length: 0}},
		{name: "length beyond the ceiling", p: proto.FSReadParams{Path: path, Offset: 0, Length: maxReadLength + 1}},
		{name: "whole read from a non-zero offset", p: proto.FSReadParams{Path: path, Offset: 1, Length: 4, Whole: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readThrough(t, svc, tc.p)
			if err == nil {
				t.Fatal("the read succeeded")
			}
			if code, _ := refusalCode(t, svc, err); code != proto.FSCodeInvalidPage {
				t.Fatalf("code = %q, want %q", code, proto.FSCodeInvalidPage)
			}
		})
	}
}

// rewrite moves the file's size and mtime, which is what the fence observes.
// The mtime is set explicitly because a rewrite of the same length inside one
// filesystem timestamp granule is a change no stat can see, and a test that
// depended on the granule would pass on one filesystem and fail on another.
func rewrite(t *testing.T, path string, data []byte, tick int) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	when := time.Now().Add(time.Duration(tick) * time.Second)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// TestReadWholeRefusesAFileRewrittenOnEveryAttempt is scenario 12's refusal:
// the file moves during every attempt, so after three attempts no version can
// be named and the answer says so instead of returning a mixture.
func TestReadWholeRefusesAFileRewrittenOnEveryAttempt(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "moving.txt")
	mustWrite(t, path, []byte("attempt zero\n"))

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	attempts := 0
	svc.afterRead = func(attempt int) {
		attempts++
		rewrite(t, path, []byte("attempt rewritten\n"), attempt+1)
	}
	_, err := readThrough(t, svc, proto.FSReadParams{Path: path, Offset: 0, Length: 1 << 20, Whole: true})
	if err == nil {
		t.Fatal("a file rewritten during every read attempt was read as if stable")
	}
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeChangedDuringRead {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeChangedDuringRead)
	}
	if attempts != wholeReadAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, wholeReadAttempts)
	}
}

// TestReadWholeSucceedsOnceTheFileSettles is scenario 12's bounded success: a
// file that moves on the first attempt and settles on the second is read, its
// Changed flag says a retry happened, and the version is of the settled bytes
// — not of what the first attempt saw.
func TestReadWholeSucceedsOnceTheFileSettles(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "settles.txt")
	mustWrite(t, path, []byte("first state\n"))
	settled := []byte("second, settled state\n")

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	attempts := 0
	svc.afterRead = func(attempt int) {
		attempts++
		if attempt == 0 {
			rewrite(t, path, settled, 1)
		}
	}
	r, err := readThrough(t, svc, proto.FSReadParams{Path: path, Offset: 0, Length: 1 << 20, Whole: true})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if !r.Changed {
		t.Error("changed = false, want true: the answer came from a retry")
	}
	if string(r.Data) != string(settled) {
		t.Fatalf("data = %q, want the settled content %q", r.Data, settled)
	}
	sum := sha256.Sum256(settled)
	if r.Version == nil || r.Version.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("version = %+v, want the digest of the settled bytes", r.Version)
	}
}

// TestReadRangeKeepsIdentityAcrossAnAppendAndLosesItOnAReplacement is the
// range half of the contract: dev:ino is what tells "the file grew" from "this
// is a different file now", and neither size, mtime nor content can.
func TestReadRangeKeepsIdentityAcrossAnAppendAndLosesItOnAReplacement(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "log.txt")
	mustWrite(t, path, []byte("one\n"))

	svc := newTestService(t, local.New(local.WithRoot(dir)))
	read := func() proto.FSReadResult {
		t.Helper()
		r, err := readThrough(t, svc, proto.FSReadParams{Path: path, Offset: 0, Length: 1 << 20})
		if err != nil {
			t.Fatalf("range read: %v", err)
		}
		if r.Version != nil {
			t.Fatal("a range read carried a version; a window's version is not the file's")
		}
		return r
	}

	first := read()
	// #nosec G304 — the seam appends to the exact fixture under test, a path
	// built from tempDir(t).
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("two\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	appended := read()
	if first.Identity == "" || appended.Identity == "" {
		t.Fatal("identity is empty on a unix build")
	}
	if appended.Identity != first.Identity {
		t.Fatalf("identity changed on an append: %q then %q", first.Identity, appended.Identity)
	}
	if string(appended.Data) != "one\ntwo\n" {
		t.Fatalf("appended data = %q", appended.Data)
	}

	replacement := filepath.Join(dir, "replacement.txt")
	mustWrite(t, replacement, []byte("new\n"))
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	replaced := read()
	if replaced.Identity == first.Identity {
		t.Fatalf("identity survived a replacement: %q", replaced.Identity)
	}
	if string(replaced.Data) != "new\n" {
		t.Fatalf("replaced data = %q", replaced.Data)
	}
}

// TestReadRefusesASpecialFileWithoutBlocking: a FIFO opened for reading blocks
// forever when it is opened without O_NONBLOCK, which is the hang the
// openability table exists to prevent — and this service opens files itself,
// so it has its own window to close. The wait is a failure detector, not a
// synchronisation primitive: a passing read returns at once.
func TestReadRefusesASpecialFileWithoutBlocking(t *testing.T) {
	dir := tempDir(t)
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	assertNotRegular(t, svc, fifo, "fifo")
}

// TestReadRefusesASocket is the third special object, and the one open(2)
// cannot serve at all: a unix socket answers ENXIO before a descriptor
// exists, so the descriptor check never runs. The refusal must still name the
// object — §5.10, the package README and kindName all promise `socket` —
// because `internal` tells a person the helper broke rather than that they
// clicked a socket.
func TestReadRefusesASocket(t *testing.T) {
	// A SHORT directory, not t.TempDir(): a unix socket's path lives in
	// sockaddr_un's 104-byte sun_path, and on macOS $TMPDIR is long enough
	// that t.TempDir() plus the test's own name overflows it. The test then
	// read "cannot create a socket here" and skipped — in ci-mac, the one
	// platform whose open(2) errno this test exists to name.
	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// EvalSymlinks: on macOS $TMPDIR lives under /var, a symlink to
	// /private/var, and the provider answers the resolved spelling.
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("Go's net package creates no unix sockets on Windows")
		}
		t.Fatalf("listen unix %s: %v", socket, err)
	}
	defer func() { _ = listener.Close() }()
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	assertNotRegular(t, svc, socket, "socket")
}

// TestReadRefusesADeviceWithoutBlocking is the same refusal for a character
// device. os.DevNull is the portable one; a machine that has none skips
// rather than fails, because the claim is about what this service answers when
// one exists, not about the machine.
func TestReadRefusesADeviceWithoutBlocking(t *testing.T) {
	devNull := os.DevNull
	info, err := os.Stat(devNull)
	if err != nil || info.Mode()&os.ModeDevice == 0 {
		t.Skipf("no device at %s", devNull)
	}
	dir := tempDir(t)
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	assertNotRegular(t, svc, devNull, "device")
}

func assertNotRegular(t *testing.T, svc *Service, path, wantKind string) {
	t.Helper()
	type answer struct {
		err error
	}
	raw, err := json.Marshal(proto.FSReadParams{Path: path, Offset: 0, Length: 1 << 20, Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan answer, 1)
	go func() {
		// No *testing.T calls here: this goroutine exists to detect a blocking
		// read, and a Fatalf from it would end a goroutine the test is waiting
		// on. The answer travels back to the test goroutine instead.
		_, callErr := svc.Call(context.Background(), proto.OpRead, raw)
		done <- answer{err: callErr}
	}()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("the read succeeded")
		}
		code, details := refusalCode(t, svc, got.err)
		if code != proto.FSCodeNotRegular {
			t.Fatalf("code = %q, want %q", code, proto.FSCodeNotRegular)
		}
		var d struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(details, &d); err != nil {
			t.Fatalf("details: %v", err)
		}
		if d.Kind != wantKind {
			t.Fatalf("details.kind = %q, want %q", d.Kind, wantKind)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read blocked on a special file; O_NONBLOCK or the descriptor check is missing")
	}
}

// TestReadRefusesADirectory is the same not-regular answer for a directory,
// whose open succeeds and whose fstat is what classifies it.
func TestReadRefusesADirectory(t *testing.T) {
	dir := tempDir(t)
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	_, err := readThrough(t, svc, proto.FSReadParams{Path: dir, Offset: 0, Length: 1 << 20, Whole: true})
	if err == nil {
		t.Fatal("reading a directory succeeded")
	}
	code, details := refusalCode(t, svc, err)
	if code != proto.FSCodeNotRegular {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeNotRegular)
	}
	var d struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(details, &d); err != nil {
		t.Fatalf("details: %v", err)
	}
	if d.Kind != "dir" {
		t.Fatalf("details.kind = %q, want dir", d.Kind)
	}
}

// TestReadEmptyFileIsAnEmptyStringOnTheWire: a nil []byte marshals to null,
// and the frozen shape says data is a string. An empty file is the case a
// hand-built fixture always gets right and a real encoder gets wrong.
func TestReadEmptyFileIsAnEmptyStringOnTheWire(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "empty.txt")
	mustWrite(t, path, nil)
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	r, err := readThrough(t, svc, proto.FSReadParams{Path: path, Offset: 0, Length: 16, Whole: true})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"data":""`) {
		t.Fatalf("an empty read marshals as %s", raw)
	}
	if r.Version == nil || r.Version.Size != 0 {
		t.Fatalf("version = %+v, want an empty size", r.Version)
	}
}

// TestReadIsRefusedByACancelledContext: every op runs under the request
// context, and a caller that withdrew gets the plan's timed-out code rather
// than a read it is no longer waiting for.
func TestReadIsRefusedByACancelledContext(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "file.txt")
	mustWrite(t, path, []byte("data"))
	svc := newTestService(t, local.New(local.WithRoot(dir)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Call(ctx, proto.OpRead, callParams(t, proto.FSReadParams{Path: path, Offset: 0, Length: 4}))
	if err == nil {
		t.Fatal("a cancelled read succeeded")
	}
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeTimedOut {
		t.Fatalf("code = %q, want %q", code, proto.FSCodeTimedOut)
	}
}
