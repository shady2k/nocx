package hostsvc

// A write session holds the daemon's admission for its whole life
// (nocx-oalo1.12.5): while it is open, the last hosted session closing drains
// the daemon only after the write has committed or aborted. These tests assert
// the interval at both ends — an admission is taken before the temporary exists
// and given back on EVERY way a session can end — because a single missing
// release is a helper that never retires, which is the failure the admission
// exists to prevent in the other direction.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// recordingAdmission is the daemon's side of the contract, as a test sees it:
// how many reservations are outstanding and whether one was ever released
// twice (which sync.Once in the service makes impossible to observe directly,
// so the test asserts the count instead).
type recordingAdmission struct {
	mu        sync.Mutex
	refuse    error
	granted   int
	released  int
	onRelease func()
}

func (a *recordingAdmission) Admit() (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refuse != nil {
		return nil, a.refuse
	}
	a.granted++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.released++
			a.mu.Unlock()
			if a.onRelease != nil {
				a.onRelease()
			}
		})
	}, nil
}

func (a *recordingAdmission) counts() (granted, released int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.granted, a.released
}

func (a *recordingAdmission) outstanding(t *testing.T) int {
	t.Helper()
	granted, released := a.counts()
	if granted < released {
		t.Fatalf("admission released %d times after being granted %d", released, granted)
	}
	return granted - released
}

// newAdmissionService builds the shipped service over root with an admission
// the test owns.
func newAdmissionService(t *testing.T, root string, a Admission) *Service {
	t.Helper()
	svc, err := New(local.New(local.WithRoot(root)), journalFor(t), WithAdmission(a))
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

// TestAWriteSessionHoldsAnAdmissionUntilItEnds walks every way a session ends
// and asserts the admission is given back exactly then: a commit installs the
// bytes, an abort removes the temporary, a connection closing aborts what is
// still open, and the service closing ends what is left. The session's life is
// the admission's life, with no third state.
func TestAWriteSessionHoldsAnAdmissionUntilItEnds(t *testing.T) {
	for _, ending := range []string{"commit", "abort", "connection-close", "service-close"} {
		t.Run(ending, func(t *testing.T) {
			dir := tempDir(t)
			target := filepath.Join(dir, "note.txt")
			mustWrite(t, target, []byte("old\n"))
			admission := &recordingAdmission{}
			svc := newAdmissionService(t, dir, admission)
			conn := host.New(nil, io.Discard, "hash", "instance", discardLog())
			ctx := host.WithConnection(context.Background(), conn)

			id := writeID(t.Name())
			if _, err := svc.Call(ctx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: id, Path: target})); err != nil {
				t.Fatalf("write-open: %v", err)
			}
			if _, err := svc.Call(ctx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("new\n")})); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := admission.outstanding(t); got != 1 {
				t.Fatalf("an open write session holds %d admissions, want 1", got)
			}

			switch ending {
			case "commit":
				commitOK(t, svc, proto.FSWriteCommitParams{ID: id, Path: target, Size: int64(len("new\n"))})
			case "abort":
				abortOK(t, svc, id)
			case "connection-close":
				svc.ConnectionClosed(conn)
			case "service-close":
				if err := svc.Close(); err != nil {
					t.Fatalf("closing the service: %v", err)
				}
			}
			if got := admission.outstanding(t); got != 0 {
				t.Fatalf("after %s %d admissions are still held, want 0", ending, got)
			}

			// A second ending — the repeat commit scenario 9 exists for, or a
			// close after a connection close — must not give the admission
			// back twice either.
			if _, err := commitTry(t, svc, proto.FSWriteCommitParams{ID: id, Path: target, Size: int64(len("new\n"))}); err != nil {
				t.Fatalf("repeating the commit: %v", err)
			}
			if _, released := admission.counts(); released != 1 {
				t.Fatalf("the admission was released %d times for one session, want 1", released)
			}
		})
	}
}

// TestAWriteOpenThatNeverOpensReleasesItsAdmission: the reservation is taken
// before the temporary exists, so every refusal on the way to it — a directory,
// a dangling symlink, a journal that cannot be written — must give it back. A
// refactored early return is exactly how this leaks.
func TestAWriteOpenThatNeverOpensReleasesItsAdmission(t *testing.T) {
	dir := tempDir(t)
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)

	// A directory is not a regular file, so resolveWriteTarget refuses it
	// after the admission is taken.
	_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen,
		proto.FSWriteOpenParams{ID: writeID(t.Name()), Path: dir})
	if err == nil {
		t.Fatal("write-open on a directory reported success")
	}
	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("a refused write-open holds %d admissions, want 0", got)
	}
	if granted, _ := admission.counts(); granted != 1 {
		t.Fatalf("a refused write-open took %d admissions, want 1 (the reservation precedes the refusal)", granted)
	}
}

// TestARefusedAdmissionRefusesTheWriteOpen: a daemon that is draining has
// closed admission, and the fs service must not open a temporary it would then
// be shut down under. The refusal reaches the caller as the admission's own
// error, and nothing is created.
func TestARefusedAdmissionRefusesTheWriteOpen(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "note.txt")
	mustWrite(t, target, []byte("old\n"))
	refusal := errors.New("session: helper generation is draining")
	admission := &recordingAdmission{refuse: refusal}
	svc := newAdmissionService(t, dir, admission)

	id := writeID(t.Name())
	_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen,
		proto.FSWriteOpenParams{ID: id, Path: target})
	if !errors.Is(err, refusal) {
		t.Fatalf("write-open = %v, want the admission's refusal", err)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("a refused write-open created %v", left)
	}
	if _, err := callAs[proto.FSWriteResult](t, svc, proto.OpWrite,
		proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("x")}); err == nil {
		t.Fatal("a refused write-open left a session behind")
	}
	if granted, _ := admission.counts(); granted != 0 {
		t.Fatalf("a refused admission counted %d grants, want 0", granted)
	}
}

// TestAWriteSessionWhoseTemporarySurvivesItsConnectionStillReleases: a
// temporary that cannot be removed outlives the session — the journal reports
// it through fs.inventory — but the daemon must not be pinned by a session no
// connection can finish. The admission's interval ends with the caller, not
// with a file somebody else has to remove.
func TestAWriteSessionWhoseTemporarySurvivesItsConnectionStillReleases(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "note.txt")
	mustWrite(t, target, []byte("old\n"))
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)
	conn := host.New(nil, io.Discard, "hash", "instance", discardLog())
	ctx := host.WithConnection(context.Background(), conn)

	id := writeID(t.Name())
	if _, err := svc.Call(ctx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: id, Path: target})); err != nil {
		t.Fatalf("write-open: %v", err)
	}
	if _, err := svc.Call(ctx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("new\n")})); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc.seams.removeTemp = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { svc.seams.removeTemp = nil })

	svc.ConnectionClosed(conn)

	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("a session whose temporary survived its connection holds %d admissions, want 0", got)
	}
	if left := temps(t, dir); len(left) != 1 {
		t.Fatalf("the undeletable temporary = %v, want it reported rather than gone", left)
	}
	if got := inventory(t, svc); len(got) != 1 {
		t.Fatalf("inventory = %v, want the temporary nobody could remove", got)
	}
	// The descriptor went with the failed removal, so the session is no longer
	// one that accepts bytes; its state says so rather than leaving a session
	// that only fails because the file it would write to is gone.
	if sess := svc.session(id); sess == nil || writeState(sess.state.Load()) != writeBroken {
		t.Fatal("a session whose connection ended and whose temporary survived is not broken")
	}
	if _, err := callAs[proto.FSWriteResult](t, svc, proto.OpWrite,
		proto.FSWriteParams{ID: id, Offset: 4, Data: []byte("x")}); err == nil {
		t.Fatal("a session whose connection ended and whose temporary survived still accepted bytes")
	}
}

// inventory asks fs.inventory for the stranded snapshot, through the seam a
// coordinator reaches.
func inventory(t *testing.T, svc *Service) []proto.FSStrandedTemp {
	t.Helper()
	out, err := callAs[proto.FSInventoryResult](t, svc, proto.OpInventory, proto.FSInventoryParams{})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	return out.Stranded
}

// TestEveryOpenWriteSessionHoldsItsOwnAdmission: the reservation is per
// session, not per service. Two sessions open at once are two reasons for the
// daemon to keep serving, and each release takes one away.
func TestEveryOpenWriteSessionHoldsItsOwnAdmission(t *testing.T) {
	dir := tempDir(t)
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)

	var ids []string
	for i := 0; i < 2; i++ {
		id := writeID(fmt.Sprintf("%s-%d", t.Name(), i))
		ids = append(ids, id)
		writeOpenOK(t, svc, id, filepath.Join(dir, fmt.Sprintf("note-%d.txt", i)))
	}
	if got := admission.outstanding(t); got != 2 {
		t.Fatalf("two open sessions hold %d admissions, want 2", got)
	}
	abortOK(t, svc, ids[0])
	if got := admission.outstanding(t); got != 1 {
		t.Fatalf("one session left holds %d admissions, want 1", got)
	}
	abortOK(t, svc, ids[1])
	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("no session left holds %d admissions, want 0", got)
	}
}

// TestACommitOverAHostKeepsItsAdmissionUntilItsAnswerIsWritten is review M2.
// The release used to happen inside the handler, BEFORE the host wrote the
// answer: the drain it starts closes every connection, the one carrying the
// answer included, and a caller could be told nothing about a file that had
// already landed. Over a host the reservation now rides the answer to
// ResponseWritten.
func TestACommitOverAHostKeepsItsAdmissionUntilItsAnswerIsWritten(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "note.txt")
	mustWrite(t, target, []byte("old\n"))
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)
	conn := host.New(nil, io.Discard, "hash", "instance", discardLog())
	ctx := host.WithConnection(context.Background(), conn)

	id := writeID(t.Name())
	if _, err := svc.Call(ctx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: id, Path: target})); err != nil {
		t.Fatalf("write-open: %v", err)
	}
	if _, err := svc.Call(ctx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("new\n")})); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := svc.Call(ctx, proto.OpWriteCommit, callParams(t, proto.FSWriteCommitParams{
		ID: id, Path: target, Size: int64(len("new\n")),
	}))
	if err != nil {
		t.Fatalf("write-commit: %v", err)
	}
	if _, ok := result.(committedOverHost); !ok {
		t.Fatalf("a commit over a host returned %T, want its answer deferred to ResponseWritten", result)
	}
	if got := admission.outstanding(t); got != 1 {
		t.Fatalf("the commit's answer is not written yet and %d admissions are held, want 1", got)
	}

	svc.ResponseWritten(ctx, proto.OpWriteCommit, result, host.AnswerDelivery{})
	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("the written answer left %d admissions held, want 0", got)
	}
}

// TestAnAbortOverAHostKeepsItsAdmissionUntilItsAnswerIsWritten is the same
// contract for the other end of a session, including the case the answer
// reached nobody: a withdrawn frame still ended the session it answered, and a
// reservation kept for it would leak in the other direction.
func TestAnAbortOverAHostKeepsItsAdmissionUntilItsAnswerIsWritten(t *testing.T) {
	for _, withdrawn := range []bool{false, true} {
		t.Run(fmt.Sprintf("withdrawn=%t", withdrawn), func(t *testing.T) {
			dir := tempDir(t)
			target := filepath.Join(dir, "note.txt")
			mustWrite(t, target, []byte("old\n"))
			admission := &recordingAdmission{}
			svc := newAdmissionService(t, dir, admission)
			conn := host.New(nil, io.Discard, "hash", "instance", discardLog())
			ctx := host.WithConnection(context.Background(), conn)

			id := writeID(t.Name())
			if _, err := svc.Call(ctx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: id, Path: target})); err != nil {
				t.Fatalf("write-open: %v", err)
			}
			result, err := svc.Call(ctx, proto.OpWriteAbort, callParams(t, proto.FSWriteAbortParams{ID: id}))
			if err != nil {
				t.Fatalf("write-abort: %v", err)
			}
			if _, ok := result.(abortedOverHost); !ok {
				t.Fatalf("an abort over a host returned %T, want its answer deferred to ResponseWritten", result)
			}
			if got := admission.outstanding(t); got != 1 {
				t.Fatalf("the abort's answer is not written yet and %d admissions are held, want 1", got)
			}
			svc.ResponseWritten(ctx, proto.OpWriteAbort, result, host.AnswerDelivery{Withdrawn: withdrawn})
			if got := admission.outstanding(t); got != 0 {
				t.Fatalf("the answer (withdrawn=%t) left %d admissions held, want 0", withdrawn, got)
			}
		})
	}
}

// TestACommitThatFailsAfterItsOpenerLeftDoesNotLeakItsAdmission is review
// M3(a). The commit deliberately outlives the connection that opened the
// session, so the opener can end while the install runs and ConnectionClosed
// skips a committing session on purpose. A failed install then leaves the
// session open under an owner that is already gone, and the caller's own
// teardown matches on its connection instead — nobody ever releases it.
func TestACommitThatFailsAfterItsOpenerLeftDoesNotLeakItsAdmission(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "note.txt")
	mustWrite(t, target, []byte("old\n"))
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)
	opener := host.New(nil, io.Discard, "hash", "instance", discardLog())
	caller := host.New(nil, io.Discard, "hash", "instance", discardLog())
	openerCtx := host.WithConnection(context.Background(), opener)
	callerCtx := host.WithConnection(context.Background(), caller)

	id := writeID(t.Name())
	if _, err := svc.Call(openerCtx, proto.OpWriteOpen, callParams(t, proto.FSWriteOpenParams{ID: id, Path: target})); err != nil {
		t.Fatalf("write-open: %v", err)
	}
	if _, err := svc.Call(openerCtx, proto.OpWrite, callParams(t, proto.FSWriteParams{ID: id, Offset: 0, Data: []byte("new\n")})); err != nil {
		t.Fatalf("write: %v", err)
	}
	refused := errors.New("rename refused")
	svc.seams.beforeRename = func() {
		// The opener ends while the commit is in flight. This is exactly the
		// window ConnectionClosed skips a committing session in, and it does
		// not wait for the session's lock.
		svc.ConnectionClosed(opener)
	}
	svc.seams.rename = func(string, string, bool) error { return refused }
	t.Cleanup(func() {
		svc.seams.beforeRename = nil
		svc.seams.rename = nil
	})

	if _, err := svc.Call(callerCtx, proto.OpWriteCommit, callParams(t, proto.FSWriteCommitParams{
		ID: id, Path: target, Size: int64(len("new\n")),
	})); !errors.Is(err, refused) {
		t.Fatalf("write-commit = %v, want the injected rename failure", err)
	}
	if got := admission.outstanding(t); got != 1 {
		t.Fatalf("the failed commit released the session: %d admissions held, want 1 (the caller may retry)", got)
	}
	// The caller is now the session's only live owner, so its teardown is what
	// ends the session and gives the reservation back.
	svc.ConnectionClosed(caller)
	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("the closed caller left %d admissions held, want 0", got)
	}
	if left := temps(t, dir); len(left) != 0 {
		t.Fatalf("the session's temporary survived its last owner: %v", left)
	}
}

// TestAnAbortThatCannotRemoveItsTemporaryStillReleasesTheDaemon is review
// M3(b). The shipped uploader attempts exactly one abort and reports its error,
// so a session that kept its reservation after a failed removal pinned the
// daemon until the panel's connection died — with a descriptor that was already
// closed, and no way to write or commit it.
func TestAnAbortThatCannotRemoveItsTemporaryStillReleasesTheDaemon(t *testing.T) {
	dir := tempDir(t)
	target := filepath.Join(dir, "note.txt")
	mustWrite(t, target, []byte("old\n"))
	admission := &recordingAdmission{}
	svc := newAdmissionService(t, dir, admission)

	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)
	writeOpenOK(t, svc, id, target)
	writeOK(t, svc, id, 0, "half")

	refused := errors.New("removal refused")
	svc.seams.removeTemp = func(string) error { return refused }
	t.Cleanup(func() { svc.seams.removeTemp = nil })
	if _, err := callAs[proto.FSWriteAbortResult](t, svc, proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id}); err == nil {
		t.Fatal("abort answered success for a temporary it could not remove")
	}
	if got := admission.outstanding(t); got != 0 {
		t.Fatalf("a failed abort holds %d admissions, want 0", got)
	}
	mustExist(t, temp)
	if got := inventory(t, svc); len(got) != 1 || got[0].Path != temp {
		t.Fatalf("inventory = %+v, want the temporary the abort could not remove", got)
	}
	// The descriptor is gone with the removal attempt, so the session must
	// refuse everything but the abort that can still finish the cleanup.
	if _, err := callAs[proto.FSWriteResult](t, svc, proto.OpWrite,
		proto.FSWriteParams{ID: id, Offset: 4, Data: []byte("x")}); err == nil {
		t.Fatal("a session whose temporary could not be removed still accepted bytes")
	}
	if _, err := callAs[proto.FSWriteCommitResult](t, svc, proto.OpWriteCommit,
		proto.FSWriteCommitParams{ID: id, Path: target, Size: 4}); err == nil {
		t.Fatal("a session whose temporary could not be removed still committed")
	}

	svc.seams.removeTemp = nil
	aborted := abortOK(t, svc, id)
	if aborted.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("repeated abort outcome = %q, want %q", aborted.Outcome, proto.FSOutcomeAborted)
	}
	mustNotExist(t, temp)
	if got := inventory(t, svc); len(got) != 0 {
		t.Fatalf("inventory after the repeated abort = %+v, want nothing stranded", got)
	}
}
