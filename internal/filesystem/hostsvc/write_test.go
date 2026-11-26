package hostsvc

// The write session's tests, at the seam the panel will use: the four ops
// through Call, with real files in a temporary directory this test owns.
//
// The failure cases are asserted through the SAME seam a caller reaches, never
// by reaching into the session: what a coordinator can do about a refusal is
// the contract, and a test that inspected internal state would pass while the
// wire answered something else. The one place a seam is injected is the step
// that must fail — temp creation, a chunk write, the fsync, the rename — because
// a filesystem that runs out of space on demand does not exist and a sleeping
// writer would make the test a race.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestKeepBothTreatsDirectoriesAndSymlinksAsOccupied(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(external, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "target.txt")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "target (1).txt")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	writeOpenOK(t, svc, id, filepath.Join(dir, "staging-target"))
	writeOK(t, svc, id, 0, "replacement")
	absent := true
	params := proto.FSWriteCommitParams{ID: id, Size: int64(len("replacement")), Expect: &proto.FSWriteExpect{Absent: &absent}}
	for _, occupied := range []string{directory, link} {
		params.Path = occupied
		_, err := commitTry(t, svc, params)
		var collision *targetExists
		if !errors.As(err, &collision) {
			t.Fatalf("commit for %s: %v; want collision", occupied, err)
		}
	}
	params.Path = filepath.Join(dir, "target (2).txt")
	if got := commitOK(t, svc, params); got.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("commit after collisions = %+v", got)
	}
	bytes, err := os.ReadFile(external) // #nosec G304 -- test-owned path.
	if err != nil || string(bytes) != "original" {
		t.Fatalf("existing symlink target = %q, %v", bytes, err)
	}
}

func newWriteService(t *testing.T, root string) *Service {
	t.Helper()
	return newTestService(t, local.New(local.WithRoot(root)))
}

// writeID is a valid session id derived from the test's name, so a failure
// names its own session.
func writeID(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:16])
}

func writeOpenOK(t *testing.T, svc *Service, id, path string) proto.FSWriteOpenResult {
	t.Helper()
	out, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: id, Path: path})
	if err != nil {
		t.Fatalf("write-open: %v", err)
	}
	return out
}

func writeOK(t *testing.T, svc *Service, id string, offset int64, data string) proto.FSWriteResult {
	t.Helper()
	out, err := callAs[proto.FSWriteResult](t, svc, proto.OpWrite, proto.FSWriteParams{ID: id, Offset: offset, Data: []byte(data)})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return out
}

func commitTry(t *testing.T, svc *Service, p proto.FSWriteCommitParams) (proto.FSWriteCommitResult, error) {
	t.Helper()
	return callAs[proto.FSWriteCommitResult](t, svc, proto.OpWriteCommit, p)
}

func commitOK(t *testing.T, svc *Service, p proto.FSWriteCommitParams) proto.FSWriteCommitResult {
	t.Helper()
	out, err := commitTry(t, svc, p)
	if err != nil {
		t.Fatalf("write-commit: %v", err)
	}
	return out
}

func abortOK(t *testing.T, svc *Service, id string) proto.FSWriteAbortResult {
	t.Helper()
	out, err := callAs[proto.FSWriteAbortResult](t, svc, proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id})
	if err != nil {
		t.Fatalf("write-abort: %v", err)
	}
	return out
}

func callAs[T any](t *testing.T, svc *Service, op string, params any) (T, error) {
	t.Helper()
	var zero T
	result, err := svc.Call(context.Background(), op, callParams(t, params))
	if err != nil {
		return zero, err
	}
	v, ok := result.(T)
	if !ok {
		t.Fatalf("%s result is %T, want %T", op, result, zero)
	}
	return v, nil
}

// versionOf is what a whole-file read would have answered before the write:
// the expectation a commit is checked against, built from the same three facts
// the read op reports.
func versionOf(t *testing.T, path string) proto.FSVersion {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 — the fixture this test created.
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return proto.FSVersion{Size: info.Size(), MtimeNs: info.ModTime().UnixNano(), SHA256: hex.EncodeToString(sum[:])}
}

// temps lists the session temporaries in dir, which is how "no temp was left"
// is asserted: the file's absence, not the session's memory of it.
func temps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), writeTempPrefix) {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	return found
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 — the fixture this test created.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestWriteSessionCommitsAnUnchangedFile is the quiet half of scenario 9: the
// file is still what the caller read, so the temporary replaces it, the version
// the commit answers describes the bytes that landed, and the temporary is
// gone.
func TestWriteSessionCommitsAnUnchangedFile(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("the old bytes\n"))
	before := versionOf(t, file)
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	open := writeOpenOK(t, svc, id, file)
	if open.Resolved != file {
		t.Fatalf("resolved = %q, want %q", open.Resolved, file)
	}
	if len(temps(t, dir)) != 1 {
		t.Fatalf("write-open left %d temporaries, want 1", len(temps(t, dir)))
	}
	next := writeOK(t, svc, id, 0, "the new bytes\n")
	if next.Size != int64(len("the new bytes\n")) {
		t.Fatalf("write size = %d, want the chunk's length", next.Size)
	}

	expect := &proto.FSWriteExpect{Version: &before}
	result := commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: next.Size, Expect: expect})
	if result.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("outcome = %q, want %q", result.Outcome, proto.FSOutcomeCommitted)
	}
	if got := mustRead(t, file); got != "the new bytes\n" {
		t.Fatalf("target holds %q, want the committed bytes", got)
	}
	if result.Version == nil {
		t.Fatal("a committed outcome carries no version")
	}
	sum := sha256.Sum256([]byte("the new bytes\n"))
	if result.Version.SHA256 != hex.EncodeToString(sum[:]) || result.Version.Size != next.Size {
		t.Fatalf("committed version = %+v, want the digest of the new bytes", *result.Version)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("a committed session left temporaries %v", left)
	}
}

// TestWriteCommitRefusesAFileChangedSinceItWasRead is scenario 9's adversarial
// half: somebody writes between the read and the commit, the commit is refused
// with `changed-since-read`, their bytes survive, and the caller's temporary is
// still there so nothing has to be sent again unless the caller aborts.
func TestWriteCommitRefusesAFileChangedSinceItWasRead(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("the bytes the caller read\n"))
	// An executable target, so the mode the commit WOULD install is visibly
	// different from the temporary's own 0600 — and the refusal below has
	// something to put back.
	// #nosec G302 — the fixture needs a mode the commit would have to
	// preserve; nothing here is a secret.
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	read := versionOf(t, file)
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "the caller's replacement\n")

	// The adversarial writer, between the read and the commit.
	mustWrite(t, file, []byte("somebody else's edit\n"))

	_, err := commitTry(t, svc, proto.FSWriteCommitParams{
		ID: id, Path: file, Size: wrote.Size, Expect: &proto.FSWriteExpect{Version: &read},
	})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeChangedSinceRead {
		t.Fatalf("refusal = %q, want %q", code, proto.FSCodeChangedSinceRead)
	}
	if got := mustRead(t, file); got != "somebody else's edit\n" {
		t.Fatalf("the other writer's bytes were replaced: %q", got)
	}
	left := temps(t, dir)
	if len(left) != 1 {
		t.Fatalf("a refused commit left %d temporaries, want the session's one", len(left))
	}
	private, err := os.Stat(left[0])
	if err != nil {
		t.Fatal(err)
	}
	if private.Mode().Perm() != 0o600 {
		t.Fatalf("a refused commit left its temporary at %v, want 0600 — the caller's bytes are not public", private.Mode().Perm())
	}

	// The caller gives up: the temporary goes, the refused file stays.
	abort := abortOK(t, svc, id)
	if abort.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("abort outcome = %q, want %q", abort.Outcome, proto.FSOutcomeAborted)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("write-abort left temporaries %v", left)
	}
	if got := mustRead(t, file); got != "somebody else's edit\n" {
		t.Fatalf("write-abort touched the target: %q", got)
	}
}

// TestWriteCommitComparesTheDigestWhenSizeAndMtimeAgree is the reason a version
// has three fields and not two: a rewrite of the same length inside one
// timestamp granule is invisible to size and mtime, and the digest is what
// refuses it.
func TestWriteCommitComparesTheDigestWhenSizeAndMtimeAgree(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("aaaa\n"))
	read := versionOf(t, file)
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "bbbb\n")

	// Same length, and the timestamp restored to the one the read saw.
	mustWrite(t, file, []byte("cccc\n"))
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if chtimesErr := os.Chtimes(file, info.ModTime(), time.Unix(0, read.MtimeNs)); chtimesErr != nil {
		t.Fatal(chtimesErr)
	}
	again := versionOf(t, file)
	if again.Size != read.Size || again.MtimeNs != read.MtimeNs {
		t.Fatalf("the fixture did not reproduce the size and mtime: %+v vs %+v", again, read)
	}
	if again.SHA256 == read.SHA256 {
		t.Fatal("the fixture did not change the content")
	}

	_, err = commitTry(t, svc, proto.FSWriteCommitParams{
		ID: id, Path: file, Size: wrote.Size, Expect: &proto.FSWriteExpect{Version: &read},
	})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeChangedSinceRead {
		t.Fatalf("refusal = %q, want %q — the digest is what caught it", code, proto.FSCodeChangedSinceRead)
	}
	if got := mustRead(t, file); got != "cccc\n" {
		t.Fatalf("the write landed over a same-size rewrite: %q", got)
	}
}

// TestWriteCommitRepeatedAnswersTheFirstOutcomeAndKeepsTheInode is the "never
// twice" half of the plan: the caller lost its reply, asks again with the same
// id, and learns what happened without a second install — the target's inode
// does not move, which is the only evidence that nothing was rewritten.
func TestWriteCommitRepeatedAnswersTheFirstOutcomeAndKeepsTheInode(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "new\n")
	params := proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size}
	first := commitOK(t, svc, params)
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	second := commitOK(t, svc, params)
	if second.Outcome != first.Outcome || second.Path != first.Path {
		t.Fatalf("the repeat answered %+v, want the first outcome %+v", second, first)
	}
	if first.Version == nil || second.Version == nil || *first.Version != *second.Version {
		t.Fatalf("the repeat answered version %+v, want the first %+v", second.Version, first.Version)
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("a repeated commit replaced the target's inode")
	}
	if got := mustRead(t, file); got != "new\n" {
		t.Fatalf("target holds %q after the repeat", got)
	}
}

// TestWriteCommitOnAFreshServiceAnswersUnknownOutcome is the restart: a new
// service never held the session, so it cannot claim the write did not happen —
// it answers what is at the target now. The digest is offered only when the
// size agrees, because two different sizes have nothing to compare.
func TestWriteCommitOnAFreshServiceAnswersUnknownOutcome(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("landed elsewhere\n"))
	restarted := newWriteService(t, dir)

	same := commitOK(t, restarted, proto.FSWriteCommitParams{
		ID: writeID(t.Name()), Path: file, Size: int64(len("landed elsewhere\n")),
	})
	if same.Outcome != proto.FSOutcomeUnknown {
		t.Fatalf("outcome = %q, want %q", same.Outcome, proto.FSOutcomeUnknown)
	}
	if same.Current == nil || same.Current.SHA256 == "" {
		t.Fatalf("current = %+v, want a digest when the sizes agree", same.Current)
	}
	want := versionOf(t, file)
	if same.Current.SHA256 != want.SHA256 || same.Current.Size != want.Size || same.Current.MtimeNs != want.MtimeNs {
		t.Fatalf("current = %+v, want the target's version %+v", *same.Current, want)
	}

	other := commitOK(t, restarted, proto.FSWriteCommitParams{
		ID: writeID(t.Name() + "-other"), Path: file, Size: 3,
	})
	if other.Outcome != proto.FSOutcomeUnknown || other.Current == nil {
		t.Fatalf("outcome = %+v, want unknown-outcome with the target's metadata", other)
	}
	if other.Current.SHA256 != "" {
		t.Fatalf("current carries a digest of a file whose size the caller did not state: %+v", *other.Current)
	}

	missing := commitOK(t, restarted, proto.FSWriteCommitParams{
		ID: writeID(t.Name() + "-missing"), Path: filepath.Join(dir, "absent.txt"), Size: 1,
	})
	if missing.Outcome != proto.FSOutcomeUnknown || missing.Current != nil {
		t.Fatalf("unknown-outcome for a missing target = %+v, want no current", missing)
	}
}

// TestConnectionCloseAbortsAnOpenSessionAndSparesACommittingOne is the policy
// the observer exists for: a caller that goes away before committing leaves no
// temporary, and one whose rename is already under way is NOT stopped — its
// answer is what the next connection asks for.
func TestConnectionCloseAbortsAnOpenSessionAndSparesACommittingOne(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)
	conn := host.New(strings.NewReader(""), io.Discard, "hash", "instance", discardLog())

	// Closed before the commit.
	openCtx := host.WithConnection(context.Background(), conn)
	early := writeID(t.Name() + "-early")
	if _, err := svc.Call(openCtx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: early, Path: file})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call(openCtx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: early, Offset: 0, Data: []byte("early\n")})); err != nil {
		t.Fatal(err)
	}
	svc.ConnectionClosed(conn)
	result, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: early, Path: file, Size: int64(len("early\n"))})
	if err != nil {
		t.Fatalf("commit of a connection-aborted session: %v", err)
	}
	if result.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("outcome = %q, want %q", result.Outcome, proto.FSOutcomeAborted)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("the closed connection left temporaries %v", left)
	}

	// Closed during the commit.
	during := writeID(t.Name() + "-during")
	if _, err := svc.Call(openCtx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: during, Path: file})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call(openCtx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: during, Offset: 0, Data: []byte("during\n")})); err != nil {
		t.Fatal(err)
	}
	svc.seams.beforeRename = func() { svc.ConnectionClosed(conn) }
	t.Cleanup(func() { svc.seams.beforeRename = nil })

	first := commitOK(t, svc, proto.FSWriteCommitParams{ID: during, Path: file, Size: int64(len("during\n"))})
	if first.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("a commit interrupted by its connection answered %q, want %q", first.Outcome, proto.FSOutcomeCommitted)
	}
	// The next connection asks the same question and hears the same answer.
	svc.seams.beforeRename = nil
	second := commitOK(t, svc, proto.FSWriteCommitParams{ID: during, Path: file, Size: int64(len("during\n"))})
	if second.Outcome != proto.FSOutcomeCommitted || second.Version == nil || first.Version == nil || *second.Version != *first.Version {
		t.Fatalf("the next connection heard %+v, want %+v", second, first)
	}
	if got := mustRead(t, file); got != "during\n" {
		t.Fatalf("target holds %q, want the committed bytes", got)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("a finished commit left temporaries %v", left)
	}
}

// TestConnectionCloseDiscardsABrokenSessionsTemporary is the other half of the
// observer's policy: a session whose fsync reported a writeback failure can no
// longer be written or committed, so only an abort can end it — and its
// connection going away IS that abort. Left alone, its temporary and its open
// descriptor outlive every caller the daemon will ever have.
func TestConnectionCloseDiscardsABrokenSessionsTemporary(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)
	conn := host.New(strings.NewReader(""), io.Discard, "hash", "instance", discardLog())
	ctx := host.WithConnection(context.Background(), conn)

	id := writeID(t.Name())
	if _, err := svc.Call(ctx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: id, Path: file})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Call(ctx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("new\n")})); err != nil {
		t.Fatal(err)
	}
	svc.seams.syncTemp = func(*os.File) error { return syscall.EIO }
	t.Cleanup(func() { svc.seams.syncTemp = nil })
	_, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: int64(len("new\n"))})
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("commit = %v, want the injected fsync failure", err)
	}
	if left := temps(t, dir); len(left) != 1 {
		t.Fatalf("a failed fsync left %d temporaries, want the session's one", len(left))
	}

	svc.ConnectionClosed(conn)

	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("the closed connection left the broken session's temporary %v", left)
	}
	if got := mustRead(t, file); got != "old\n" {
		t.Fatalf("target holds %q, want the bytes it had before the write", got)
	}
	result := commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: int64(len("new\n"))})
	if result.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("a commit after the connection closed answered %q, want %q", result.Outcome, proto.FSOutcomeAborted)
	}
}

// TestCancelPolicyRefusesTheTwoEndsOfASession: a half-applied commit or abort
// is worse than a caller waiting, so both refuse cancellation while the reads
// and the bytes stay cancellable.
func TestCancelPolicyRefusesTheTwoEndsOfASession(t *testing.T) {
	svc := newWriteService(t, tempDir(t))
	for _, op := range []string{proto.OpWriteCommit, proto.OpWriteAbort} {
		if !svc.RefusesCancel(op) {
			t.Fatalf("%s is cancellable, want it refused", op)
		}
	}
	for _, op := range []string{proto.OpWriteOpen, proto.OpWrite, proto.OpRead, proto.OpList} {
		if svc.RefusesCancel(op) {
			t.Fatalf("%s refuses cancellation, want it cancellable", op)
		}
	}
}

// TestConcurrentCommitsInstallExactlyOne is the plan's serialisation: two
// sessions, the same expectation, one target — the per-path lock plus the
// check decide one winner, whichever order they arrive in.
func TestConcurrentCommitsInstallExactlyOne(t *testing.T) {
	for _, absent := range []bool{false, true} {
		name := "version"
		if absent {
			name = "absent"
		}
		t.Run(name, func(t *testing.T) {
			dir := tempDir(t)
			file := filepath.Join(dir, "note.txt")
			if !absent {
				mustWrite(t, file, []byte("old\n"))
			}
			read := proto.FSVersion{}
			if !absent {
				read = versionOf(t, file)
			}
			svc := newWriteService(t, dir)

			var ids []string
			var expect *proto.FSWriteExpect
			if absent {
				yes := true
				expect = &proto.FSWriteExpect{Absent: &yes}
			} else {
				expect = &proto.FSWriteExpect{Version: &read}
			}
			for i := 0; i < 2; i++ {
				id := writeID(t.Name() + string(rune('a'+i)))
				ids = append(ids, id)
				writeOpenOK(t, svc, id, file)
				writeOK(t, svc, id, 0, "same\n")
			}

			type outcome struct {
				result proto.FSWriteCommitResult
				err    error
			}
			results := make([]outcome, len(ids))
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i, id := range ids {
				wg.Add(1)
				go func(i int, id string) {
					defer wg.Done()
					<-start
					out, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: int64(len("same\n")), Expect: expect})
					results[i] = outcome{result: out, err: err}
				}(i, id)
			}
			close(start)
			wg.Wait()

			committed := 0
			for _, r := range results {
				if r.err == nil {
					if r.result.Outcome != proto.FSOutcomeCommitted {
						t.Fatalf("outcome = %q, want committed", r.result.Outcome)
					}
					committed++
					continue
				}
				want := proto.FSCodeChangedSinceRead
				if absent {
					want = proto.FSCodeExists
				}
				if code, _ := refusalCode(t, svc, r.err); code != want {
					t.Fatalf("the losing commit refused %q, want %q", code, want)
				}
			}
			if committed != 1 {
				t.Fatalf("%d commits installed, want exactly 1", committed)
			}
			if got := mustRead(t, file); got != "same\n" {
				t.Fatalf("target holds %q, want the winner's bytes", got)
			}
		})
	}
}

// TestWriteThroughASymlinkReplacesTheResolvedTarget: the link is what the
// caller named, the file is what gets replaced, and the link survives — the
// resolution happens once, at open, and the commit follows it.
func TestWriteThroughASymlinkReplacesTheResolvedTarget(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "real.txt")
	mustWrite(t, target, []byte("old\n"))
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	open := writeOpenOK(t, svc, id, link)
	if open.Resolved != target {
		t.Fatalf("resolved = %q, want the link's target %q", open.Resolved, target)
	}
	wrote := writeOK(t, svc, id, 0, "new\n")
	result := commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: link, Size: wrote.Size})
	if result.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("outcome = %q, want committed", result.Outcome)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the commit replaced the symlink instead of its target")
	}
	if got := mustRead(t, target); got != "new\n" {
		t.Fatalf("the resolved target holds %q, want the committed bytes", got)
	}
}

// TestWriteCommitKeepsTheTargetsModeAndTheTemporaryIsPrivate: an existing
// file's mode is preserved, a new file gets 0666 less the umask, and while the
// session is open nobody else can read its temporary.
func TestWriteCommitKeepsTheTargetsModeAndTheTemporaryIsPrivate(t *testing.T) {
	dir := tempDir(t)
	for _, mode := range []os.FileMode{0o600, 0o755} {
		file := filepath.Join(dir, "mode-"+mode.String()+".txt")
		if err := os.WriteFile(file, []byte("old\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		svc := newWriteService(t, dir)
		id := writeID(t.Name() + mode.String())
		writeOpenOK(t, svc, id, file)

		open := temps(t, dir)
		if len(open) != 1 {
			t.Fatalf("%s: %d temporaries, want 1", mode, len(open))
		}
		info, err := os.Stat(open[0])
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("the temporary's mode is %v, want 0600", info.Mode().Perm())
		}

		wrote := writeOK(t, svc, id, 0, "new\n")
		commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
		after, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if after.Mode().Perm() != mode {
			t.Fatalf("mode %v became %v, want it preserved", mode, after.Mode().Perm())
		}
	}

	newFile := filepath.Join(dir, "brand-new.txt")
	svc := newWriteService(t, dir)
	id := writeID(t.Name() + "-new")
	writeOpenOK(t, svc, id, newFile)
	wrote := writeOK(t, svc, id, 0, "fresh\n")
	commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: newFile, Size: wrote.Size})
	info, err := os.Stat(newFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != newFileMode() {
		t.Fatalf("a new file's mode is %v, want 0666 less the umask (%v)", info.Mode().Perm(), newFileMode())
	}
}

// TestExpectAbsentRefusesAnOccupiedNameAndLetsKeepBothRetry: the collision is
// answered, not resolved, and the same session commits under another name
// without a second trip for the bytes — which is the whole reason the
// temporary survives a refusal.
func TestExpectAbsentRefusesAnOccupiedNameAndLetsKeepBothRetry(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("taken\n"))
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "the caller's bytes\n")
	yes := true
	_, err := commitTry(t, svc, proto.FSWriteCommitParams{
		ID: id, Path: file, Size: wrote.Size, Expect: &proto.FSWriteExpect{Absent: &yes},
	})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeExists {
		t.Fatalf("refusal = %q, want %q", code, proto.FSCodeExists)
	}
	if got := mustRead(t, file); got != "taken\n" {
		t.Fatalf("the occupied name holds %q, want it untouched", got)
	}
	if left := temps(t, dir); len(left) != 1 {
		t.Fatalf("the refusal left %d temporaries, want the session's one", len(left))
	}

	// KeepBoth: the same session, another name, no bytes re-sent.
	other := filepath.Join(dir, "note (1).txt")
	result := commitOK(t, svc, proto.FSWriteCommitParams{
		ID: id, Path: other, Size: wrote.Size, Expect: &proto.FSWriteExpect{Absent: &yes},
	})
	if result.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("the KeepBoth retry answered %q, want committed", result.Outcome)
	}
	if got := mustRead(t, other); got != "the caller's bytes\n" {
		t.Fatalf("the suffixed name holds %q, want the session's bytes", got)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("the retried commit left temporaries %v", left)
	}
}

// TestWriteCommitCannotLeaveTheDirectoryItOpened: a name may differ, a
// directory may not — the temporary belongs to one directory and the caller's
// session is the only authority over what lands in it.
func TestWriteCommitCannotLeaveTheDirectoryItOpened(t *testing.T) {
	dir := tempDir(t)
	outside := filepath.Join(filepath.Dir(dir), "elsewhere.txt")
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)

	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "new\n")
	_, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: outside, Size: wrote.Size})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeInvalidPath {
		t.Fatalf("refusal = %q, want %q", code, proto.FSCodeInvalidPath)
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("the commit created %s outside the session's directory", outside)
	}
	if got := mustRead(t, file); got != "old\n" {
		t.Fatalf("the target holds %q, want it untouched", got)
	}
}

// TestWriteSessionRefusesWhatItCannotWrite is the set of requests whose answer
// is known before anything is touched: an id that cannot be a filename, a
// second session on a taken id, a chunk out of order, a size the session does
// not hold, and an expectation that asks for two conditions at once.
func TestWriteSessionRefusesWhatItCannotWrite(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)

	for _, id := range []string{"", "abc", strings.Repeat("A", 32), strings.Repeat("z", 32), strings.Repeat("a", 31), "../" + strings.Repeat("a", 29)} {
		_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: id, Path: file})
		if code, _ := refusalCode(t, svc, err); code != proto.FSCodeBadSession {
			t.Fatalf("write-open with id %q refused %q, want %q", id, code, proto.FSCodeBadSession)
		}
	}

	taken := writeID(t.Name() + "-taken")
	writeOpenOK(t, svc, taken, file)
	_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: taken, Path: file})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeBadSession {
		t.Fatalf("a second session on a taken id refused %q, want %q", code, proto.FSCodeBadSession)
	}

	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	_, err = callAs[proto.FSWriteResult](t, svc, proto.OpWrite, proto.FSWriteParams{ID: id, Offset: 4, Data: []byte("x")})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeBadOffset {
		t.Fatalf("an out-of-order chunk refused %q, want %q", code, proto.FSCodeBadOffset)
	}
	_, err = callAs[proto.FSWriteResult](t, svc, proto.OpWrite, proto.FSWriteParams{ID: writeID(t.Name() + "-unknown"), Offset: 0, Data: []byte("x")})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeBadSession {
		t.Fatalf("a chunk for an unknown session refused %q, want %q", code, proto.FSCodeBadSession)
	}

	wrote := writeOK(t, svc, id, 0, "new\n")
	_, err = commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size + 1})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeSizeMismatch {
		t.Fatalf("a commit of the wrong size refused %q, want %q", code, proto.FSCodeSizeMismatch)
	}
	if left := temps(t, dir); len(left) != 2 {
		t.Fatalf("a size-mismatched commit left %d temporaries, want both sessions'", len(left))
	}
	// The session survived the refusal: the caller sends the rest.
	if out := writeOK(t, svc, id, wrote.Size, "!"); out.Size != wrote.Size+1 {
		t.Fatalf("a chunk after a refused commit left the session at %d, want %d", out.Size, wrote.Size+1)
	}

	both := &proto.FSWriteExpect{Version: &proto.FSVersion{Size: 1}, Absent: boolPtr(true)}
	_, err = commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size + 1, Expect: both})
	if code, _ := refusalCode(t, svc, err); code != proto.ErrCodeBadParams {
		t.Fatalf("two expectations at once refused %q, want %q", code, proto.ErrCodeBadParams)
	}
	no := false
	_, err = commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size + 1, Expect: &proto.FSWriteExpect{Absent: &no}})
	if code, _ := refusalCode(t, svc, err); code != proto.ErrCodeBadParams {
		t.Fatalf("expect.absent false refused %q, want %q", code, proto.ErrCodeBadParams)
	}
}

// TestWriteSessionRefusesWhatIsNotARegularFile covers the objects a write must
// not be pointed at. A FIFO is the one that matters: opening it for writing
// would block the whole helper until somebody read, so it is classified before
// any descriptor exists.
func TestWriteSessionRefusesWhatIsNotARegularFile(t *testing.T) {
	dir := tempDir(t)
	svc := newWriteService(t, dir)

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "no-such-file"), dangling); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, path, kind string }{
		{name: "directory", path: sub, kind: "dir"},
		{name: "fifo", path: fifo, kind: "fifo"},
		{name: "dangling link", path: dangling, kind: "symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: writeID(t.Name()), Path: tc.path})
			if code, _ := refusalCode(t, svc, err); code != proto.FSCodeNotRegular {
				t.Fatalf("refusal = %q, want %q", code, proto.FSCodeNotRegular)
			}
			code, details := svc.Refusal(err)
			if code != proto.FSCodeNotRegular {
				t.Fatalf("code = %q", code)
			}
			var d kindDetails
			if err := jsonUnmarshalDetails(details, &d); err != nil {
				t.Fatalf("details %s: %v", details, err)
			}
			if d.Kind != tc.kind {
				t.Fatalf("details kind = %q, want %q", d.Kind, tc.kind)
			}
		})
	}
}

// TestWriteCommitFailureLeavesTheSessionWhole is the paired-failure half of the
// plan, one subtest per step: temp creation, the chunk write, the fsync, the
// rename. Each fails exactly once, deterministically, and then the real call
// runs — which is what proves the refusal left the session reusable rather than
// merely reported.
func TestWriteCommitFailureLeavesTheSessionWhole(t *testing.T) {
	t.Run("temp create", func(t *testing.T) {
		dir := tempDir(t)
		file := filepath.Join(dir, "note.txt")
		svc := newWriteService(t, dir)
		boom := errors.New("boom")
		svc.seams.createTemp = func(string, string) (*os.File, error) { return nil, &os.PathError{Op: "open", Path: file, Err: boom} }
		_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: writeID(t.Name()), Path: file})
		if !errors.Is(err, boom) {
			t.Fatalf("write-open = %v, want the injected failure", err)
		}
		if left := temps(t, dir); len(left) != 0 {
			t.Fatalf("a failed write-open left temporaries %v", left)
		}
	})

	t.Run("write reports no-space", func(t *testing.T) {
		dir := tempDir(t)
		file := filepath.Join(dir, "note.txt")
		svc := newWriteService(t, dir)
		id := writeID(t.Name())
		writeOpenOK(t, svc, id, file)
		svc.seams.writeChunk = func(*os.File, []byte, int64) (int, error) {
			return 0, &os.PathError{Op: "write", Path: file, Err: syscall.ENOSPC}
		}
		_, err := callAs[proto.FSWriteResult](t, svc, proto.OpWrite, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("bytes")})
		if code, _ := refusalCode(t, svc, err); code != proto.FSCodeNoSpace {
			t.Fatalf("refusal = %q, want %q", code, proto.FSCodeNoSpace)
		}
		svc.seams.writeChunk = nil
		if out := writeOK(t, svc, id, 0, "bytes"); out.Size != 5 {
			t.Fatalf("size after the failed chunk = %d, want 5", out.Size)
		}
	})

	t.Run("fsync", func(t *testing.T) {
		dir := tempDir(t)
		file := filepath.Join(dir, "note.txt")
		mustWrite(t, file, []byte("old\n"))
		svc := newWriteService(t, dir)
		id := writeID(t.Name())
		writeOpenOK(t, svc, id, file)
		wrote := writeOK(t, svc, id, 0, "new\n")
		svc.seams.syncTemp = func(*os.File) error { return syscall.EIO }
		_, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("commit = %v, want the injected fsync failure", err)
		}
		if got := mustRead(t, file); got != "old\n" {
			t.Fatalf("a failed fsync installed the file: %q", got)
		}
		if left := temps(t, dir); len(left) != 1 {
			t.Fatalf("a failed fsync left %d temporaries, want the session's one", len(left))
		}
		// A retry must not be accepted: on Linux and NFS the kernel clears the
		// error flag on the descriptor after the first report, so the SECOND
		// fsync would answer 0 and install pages that never reached the disk.
		svc.seams.syncTemp = nil
		_, err = commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
		if code, _ := refusalCode(t, svc, err); code != proto.FSCodeBadSession {
			t.Fatalf("a commit after a failed fsync refused %q, want %q", code, proto.FSCodeBadSession)
		}
		_, err = callAs[proto.FSWriteResult](t, svc, proto.OpWrite, proto.FSWriteParams{ID: id, Offset: wrote.Size, Data: []byte("!")})
		if code, _ := refusalCode(t, svc, err); code != proto.FSCodeBadSession {
			t.Fatalf("a chunk after a failed fsync refused %q, want %q", code, proto.FSCodeBadSession)
		}
		if got := mustRead(t, file); got != "old\n" {
			t.Fatalf("a session poisoned by a failed fsync installed the file: %q", got)
		}
		// abort is the way out: the temporary goes and the target stays.
		if abort := abortOK(t, svc, id); abort.Outcome != proto.FSOutcomeAborted {
			t.Fatalf("abort of a poisoned session answered %q, want %q", abort.Outcome, proto.FSOutcomeAborted)
		}
		if left := temps(t, dir); len(left) != 0 {
			t.Fatalf("abort of a poisoned session left temporaries %v", left)
		}
		if got := mustRead(t, file); got != "old\n" {
			t.Fatalf("target holds %q after aborting the poisoned session", got)
		}
	})

	t.Run("rename", func(t *testing.T) {
		dir := tempDir(t)
		file := filepath.Join(dir, "note.txt")
		mustWrite(t, file, []byte("old\n"))
		svc := newWriteService(t, dir)
		id := writeID(t.Name())
		writeOpenOK(t, svc, id, file)
		wrote := writeOK(t, svc, id, 0, "new\n")
		svc.seams.rename = func(string, string, bool) error { return syscall.EIO }
		_, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("commit = %v, want the injected rename failure", err)
		}
		if got := mustRead(t, file); got != "old\n" {
			t.Fatalf("a failed rename installed the file: %q", got)
		}
		if left := temps(t, dir); len(left) != 1 {
			t.Fatalf("a failed rename left %d temporaries, want the session's one", len(left))
		}
		svc.seams.rename = nil
		result := commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
		if result.Outcome != proto.FSOutcomeCommitted {
			t.Fatalf("the retried commit answered %q, want committed", result.Outcome)
		}
		if got := mustRead(t, file); got != "new\n" {
			t.Fatalf("the retried commit left %q, want the new bytes", got)
		}
	})
}

// waitDone is a guard against a hang, not a timing assertion: it fails the
// test when the work it waits for never finishes. The assertions above it are
// what the test is about; this exists so a lock held across the temporary's
// creation reports itself as a failure instead of a timeout of the whole
// package.
func waitDone(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s waited for the temporary's creation to return", what)
	}
}

// TestWriteChunkRetriedAfterAPartialWriteLeavesNoTail is the ENOSPC hole:
// write(2) may put some of a chunk on disk AND report no space, while the
// answer the caller receives is the size from BEFORE the chunk. Retrying that
// chunk at the offset the session still reports must produce exactly the
// caller's bytes — a temporary carrying the abandoned half behind them would
// be installed as a file longer than the version the commit answers.
func TestWriteChunkRetriedAfterAPartialWriteLeavesNoTail(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)

	const chunk = "0123456789"
	svc.seams.writeChunk = func(f *os.File, data []byte, offset int64) (int, error) {
		// Half the chunk lands — through the same position-moving call the
		// real write makes — and then the filesystem says there is no room.
		n, err := f.Write(data[:len(data)/2])
		if err != nil {
			return n, err
		}
		return n, syscall.ENOSPC
	}
	_, err := callAs[proto.FSWriteResult](t, svc, proto.OpWrite, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte(chunk)})
	if code, _ := refusalCode(t, svc, err); code != proto.FSCodeNoSpace {
		t.Fatalf("refusal = %q, want %q", code, proto.FSCodeNoSpace)
	}
	svc.seams.writeChunk = nil

	// The caller retries the same chunk at the same offset, as the answer it
	// received says it should.
	wrote := writeOK(t, svc, id, 0, chunk)
	result := commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})

	if got := mustRead(t, file); got != chunk {
		t.Fatalf("target holds %q, want exactly the chunk the caller sent", got)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(chunk)) {
		t.Fatalf("the installed file is %d bytes, want %d — a partial write left a tail", info.Size(), len(chunk))
	}
	sum := sha256.Sum256([]byte(chunk))
	if result.Version == nil || result.Version.Size != int64(len(chunk)) || result.Version.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("committed version = %+v, want the digest of the chunk", result.Version)
	}
}

// TestWriteCommitRefusesATemporaryLongerThanTheSession is the second line: even
// if bytes reach the temporary through a path the chunk op did not count, the
// commit must not install a file whose size and digest would then disagree with
// the version it reports.
func TestWriteCommitRefusesATemporaryLongerThanTheSession(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)

	svc.seams.writeChunk = func(f *os.File, data []byte, offset int64) (int, error) {
		n, err := f.WriteAt(append(append([]byte{}, data...), "tail"...), offset)
		if err != nil {
			return n, err
		}
		return len(data), nil
	}
	wrote := writeOK(t, svc, id, 0, "new\n")
	svc.seams.writeChunk = nil

	if _, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size}); err == nil {
		t.Fatal("a commit installed a temporary longer than the size the session acknowledges")
	}
	if got := mustRead(t, file); got != "old\n" {
		t.Fatalf("a commit of a longer temporary replaced the target: %q", got)
	}
	if left := temps(t, dir); len(left) != 1 {
		t.Fatalf("the refusal left %d temporaries, want the session's one", len(left))
	}
}

// TestWriteCommitPutsTheTemporariesPrivacyBackWhenTheModeStepFails: the
// temporary receives the target's mode before the commit knows whether it may
// land, so a failure anywhere after that widening must return the temporary to
// 0600. Otherwise a write that never happened leaves the caller's bytes
// readable by the rest of the account.
func TestWriteCommitPutsTheTemporariesPrivacyBackWhenTheModeStepFails(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	// #nosec G302 — the fixture needs a mode the commit would have to
	// preserve; nothing here is a secret.
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "new\n")

	boom := errors.New("mode step failed after widening the temporary")
	svc.seams.applyMode = func(temp, _ string) error {
		// The real order is the mode first, the owner second: this is a
		// failure after the widening, which is the case that must not leak.
		// #nosec G302 — widening is exactly what this test is about: the
		// commit gives the temporary the target's mode before it may land.
		if err := os.Chmod(temp, 0o755); err != nil {
			return err
		}
		return boom
	}
	_, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
	if !errors.Is(err, boom) {
		t.Fatalf("commit = %v, want the injected mode failure", err)
	}
	svc.seams.applyMode = nil

	left := temps(t, dir)
	if len(left) != 1 {
		t.Fatalf("the refusal left %d temporaries, want the session's one", len(left))
	}
	info, err := os.Stat(left[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("a refused commit left its temporary at %v, want 0600", info.Mode().Perm())
	}
	if got := mustRead(t, file); got != "old\n" {
		t.Fatalf("the failed commit replaced the target: %q", got)
	}

	// The session is still whole: the same bytes commit once the step works.
	result := commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
	if result.Outcome != proto.FSOutcomeCommitted || mustRead(t, file) != "new\n" {
		t.Fatalf("the retried commit answered %+v, want a commit of the session's bytes", result)
	}
}

// TestWriteOpenDoesNotHoldTheServiceLockWhileItCreatesTheTemporary: creating
// the temporary is a file I/O call on the target's directory, and a directory
// on a hung mount does not answer. Holding the service's lock across it would
// stall every other session and every other connection's teardown, so the lock
// covers the registration and nothing else.
func TestWriteOpenDoesNotHoldTheServiceLockWhileItCreatesTheTemporary(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)

	// Another connection with an open session of its own.
	other := host.New(strings.NewReader(""), io.Discard, "hash", "instance", discardLog())
	otherCtx := host.WithConnection(context.Background(), other)
	elsewhere := writeID(t.Name() + "-elsewhere")
	if _, err := svc.Call(otherCtx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: elsewhere, Path: file})); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	defer release()
	svc.seams.createTemp = func(dir, name string) (*os.File, error) {
		close(entered)
		<-releaseCh
		// #nosec G304 — the test's own directory and the service's own name.
		return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	t.Cleanup(func() { svc.seams.createTemp = nil })

	slow := writeID(t.Name() + "-slow")
	slowParams := callParams(t, proto.FSWriteOpenParams{ID: slow, Path: file})
	slowAgain := callParams(t, proto.FSWriteOpenParams{ID: slow, Path: file})
	abortParams := callParams(t, proto.FSWriteAbortParams{ID: elsewhere})
	opened := make(chan error, 1)
	go func() {
		_, err := svc.Call(context.Background(), proto.OpWriteOpen, slowParams)
		opened <- err
	}()
	<-entered

	// Everything below runs while the temporary is still being created.
	var (
		duplicate     error
		abortedResult any
		abortedErr    error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, duplicate = svc.Call(context.Background(), proto.OpWriteOpen, slowAgain)
		svc.ConnectionClosed(other)
		abortedResult, abortedErr = svc.Call(context.Background(), proto.OpWriteAbort, abortParams)
	}()
	waitDone(t, done, "a second write-open, an abort of another session and a foreign connection's close")

	release()
	if err := <-opened; err != nil {
		t.Fatalf("the slow write-open failed: %v", err)
	}

	if code, _ := refusalCode(t, svc, duplicate); code != proto.FSCodeBadSession {
		t.Fatalf("a duplicate id during the creation refused %q, want %q", code, proto.FSCodeBadSession)
	}
	if abortedErr != nil {
		t.Fatalf("abort of another session during the creation: %v", abortedErr)
	}
	if aborted, ok := abortedResult.(proto.FSWriteAbortResult); !ok || aborted.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("abort of another session during the creation = %+v, want %q", abortedResult, proto.FSOutcomeAborted)
	}

	// The slow session survived all of it and is the only one left.
	if abort := abortOK(t, svc, slow); abort.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("abort of the slow session answered %q, want %q", abort.Outcome, proto.FSOutcomeAborted)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("the sessions left temporaries %v", left)
	}
}

// TestWriteOpenCleansUpWhenItIsAbandonedWhileCreatingTheTemporary: the caller
// may go away — its context is cancelled, or its connection closes — while the
// temporary is still being created. The file must not outlive the request, and
// the session must not stay half-registered.
func TestWriteOpenCleansUpWhenItIsAbandonedWhileCreatingTheTemporary(t *testing.T) {
	for _, name := range []string{"connection closed", "context cancelled"} {
		t.Run(name, func(t *testing.T) {
			dir := tempDir(t)
			file := filepath.Join(dir, "note.txt")
			mustWrite(t, file, []byte("old\n"))
			svc := newWriteService(t, dir)

			conn := host.New(strings.NewReader(""), io.Discard, "hash", "instance", discardLog())
			ctx, cancel := context.WithCancel(host.WithConnection(context.Background(), conn))
			t.Cleanup(cancel)

			entered := make(chan struct{})
			releaseCh := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
			defer release()
			svc.seams.createTemp = func(dir, name string) (*os.File, error) {
				close(entered)
				<-releaseCh
				// #nosec G304 — the test's own directory and the service's own name.
				return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			}
			t.Cleanup(func() { svc.seams.createTemp = nil })

			params := callParams(t, proto.FSWriteOpenParams{ID: writeID(t.Name()), Path: file})
			opened := make(chan error, 1)
			go func() {
				_, err := svc.Call(ctx, proto.OpWriteOpen, params)
				opened <- err
			}()
			<-entered

			abandoned := make(chan struct{})
			go func() {
				defer close(abandoned)
				if name == "connection closed" {
					svc.ConnectionClosed(conn)
					return
				}
				cancel()
			}()
			waitDone(t, abandoned, "the abandonment of a write-open in flight")
			release()

			if err := <-opened; err == nil {
				t.Fatal("an abandoned write-open answered success")
			}
			if left := temps(t, dir); len(left) != 0 {
				t.Fatalf("an abandoned write-open left temporaries %v", left)
			}
		})
	}
}

// TestWriteAbortOfACommittedSessionReportsTheCommit: the caller that aborts
// after a lost reply must be told its bytes landed, not that they were
// discarded — and the target must not move.
func TestWriteAbortOfACommittedSessionReportsTheCommit(t *testing.T) {
	dir := tempDir(t)
	file := filepath.Join(dir, "note.txt")
	mustWrite(t, file, []byte("old\n"))
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	writeOpenOK(t, svc, id, file)
	wrote := writeOK(t, svc, id, 0, "new\n")
	commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: file, Size: wrote.Size})
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	abort := abortOK(t, svc, id)
	if abort.Outcome != proto.FSOutcomeCommitted {
		t.Fatalf("abort of a committed session answered %q, want %q", abort.Outcome, proto.FSOutcomeCommitted)
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || mustRead(t, file) != "new\n" {
		t.Fatal("write-abort touched a committed target")
	}

	unknown := abortOK(t, svc, writeID(t.Name()+"-unknown"))
	if unknown.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("abort of an unknown session answered %q, want %q", unknown.Outcome, proto.FSOutcomeAborted)
	}
}

func boolPtr(v bool) *bool { return &v }

func jsonUnmarshalDetails(raw []byte, v any) error {
	if len(raw) == 0 {
		return errors.New("no details")
	}
	return json.Unmarshal(raw, v)
}
