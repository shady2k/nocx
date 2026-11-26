package hostsvc

// The write half of the `fs` service: one session per file, a temporary beside
// the target, and a commit that installs it only over the version the caller
// expected (ADR-727g63tr, design §5.10, C27).
//
// # Why a session rather than one request carrying the file
//
// A frame's payload is bounded (proto.MaxFrameBytes), so a file of any useful
// size crosses in chunks; the bytes must go SOMEWHERE before the commit knows
// whether it may install them, and the only safe somewhere is a file the caller
// cannot see and this process can delete. The temporary is that file: created
// beside the target — same filesystem, so the install is a rename and never a
// copy — with mode 0600 and O_EXCL, so no path in this code ever reads a
// partial write and no second party can open it.
//
// # Why the caller chooses the session id
//
// The commit's hard problem is not the write, it is the answer that never
// arrives: a caller that retries must not install the bytes twice, and a caller
// that gives up must not leave a file nobody will finish. Both are decided by
// an identity the CALLER holds across the retry, so the id comes from the
// caller — 128 bits of hex, because it is also a filename component. A repeat
// of a commit answers the first outcome without touching the target at all; an
// id this process never held is answered `unknown-outcome` with the target's
// current version, which is the honest answer to "did my write land" after a
// restart.
//
// # What the state machine guarantees
//
// opening → open → committing → committed is the only forward path. Every
// refusal that leaves the temporary usable — `size-mismatch`,
// `changed-since-read`, `exists`, a failed rename — returns to open with the
// temporary still on disk, so the caller may fix the condition (another name
// for KeepBoth, a fresh read for a changed file) and commit again WITHOUT
// sending the bytes a second time. The two transitions that cannot fail back
// are the rename, after which a session answers its first outcome forever, and
// a failed fsync, after which the session is poisoned: the descriptor's
// writeback error is reported once, a retry would read as healthy, and only
// `write-abort` can end such a session.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// writeTempPrefix names a session's temporary beside its target. The name
// carries the session id and nothing else: a helper that finds one of these
// after a restart knows which session left it, and a sweep can decide what it
// is without a journal (which is the leaf that adds one).
const writeTempPrefix = ".nocx-write-"

// maxWriteChunk is the bound fs.write accepts for one chunk. It matches
// transfer.DefaultChunk (256 KiB) — the size the coordinator's own upload path
// already moves — and it is a TRANSPORT bound, not a product one: base64 costs
// four bytes for every three, so 256 KiB arrives as about 350 KiB inside a
// frame whose payload limit is 1 MiB (proto.MaxFrameBytes). The constant is
// repeated rather than imported because §5.10's table keeps this package off
// the transfer package, and a chunk bound is not worth a second dependency in
// the artifact that runs on hosts we do not own.
const maxWriteChunk = 256 << 10

// writeIDHexLen is the length of a 128-bit identifier spelled as hex.
const writeIDHexLen = 32

// writeState is a session's place in open → committing → committed, plus the
// two ends a session can leave by: aborted, and the committed outcome it keeps
// for the repeat that must not write twice.
type writeState int32

const (
	// writeOpening: the session is registered but its temporary is still being
	// created. Nothing may be written or committed yet — those answer
	// `bad-session` rather than waiting — and either end may still be taken by
	// the caller or by its connection going away.
	writeOpening writeState = iota
	// writeOpen: the temporary accepts chunks and either end may still be
	// taken by the caller or by its connection going away.
	writeOpen
	// writeCommitting: the rename is being prepared or has happened. A
	// connection closing here does NOT abort — the answer is what the next
	// connection asks for.
	writeCommitting
	// writeCommitted: the bytes are installed, and outcome is the answer
	// every later commit returns untouched.
	writeCommitted
	// writeAborted: the temporary is gone and the target was never touched.
	writeAborted
	// writeBroken: the commit's fsync reported a writeback failure. The
	// session keeps its temporary and its bytes, but it can no longer be
	// written or committed: the kernel reports a descriptor's writeback error
	// ONCE, so a second fsync on this descriptor answers clean over pages that
	// may never have reached the disk. Only abort can end it.
	writeBroken
)

// writeSession is one caller's write in progress. Its fields are guarded by mu
// — a chunk and a commit can arrive concurrently — except state, which is
// atomic so that a connection ending can decide whether a session is still
// open without waiting behind a commit that is legitimately slow.
type writeSession struct {
	// conn is the host.Host the session was opened on, nil outside a host.
	// Only its identity is used: a connection ending aborts what is still
	// open on it. It is atomic because a connection's teardown asks about it
	// without waiting behind a commit that is legitimately slow, and the
	// commit's failure path may move ownership to the caller's own connection
	// while that teardown is deciding (review M3).
	conn atomic.Pointer[host.Host]
	// dir is the resolved directory the target lives in. A commit may name
	// another file in it (KeepBoth); it may not leave it.
	dir    string
	temp   string
	file   *os.File
	size   int64
	digest hash.Hash
	// journalEntry is the file name of this session's journal record, relative
	// to the daemon's journal directory, or "" once the record is gone (see
	// journal.go). It is cleared by the same transitions that remove the
	// temporary, so "is there an entry" and "is there a file" cannot drift.
	journalEntry string

	// admission is the daemon reservation this session holds for its whole
	// life: taken before its temporary exists, given back when it reaches
	// committed or aborted, or when its connection or the service ends it. It
	// is nil for a service built without one — a test's, or a `fs` service
	// compiled into something that is not a daemon.
	admission     func()
	admissionOnce sync.Once

	// ownerClosed records that the connection which opened this session has
	// ENDED, whatever state the session was in when it did. A commit in flight
	// deliberately survives its opener, so the session can outlive the owner it
	// still names; without this flag nothing would ever notice that the
	// connection in `conn` is dead, and a failed install would leave the
	// admission held behind an owner no teardown will look for again (review
	// M3). It is atomic for the same reason conn is.
	ownerClosed atomic.Bool

	state atomic.Int32

	mu sync.Mutex
	// outcome is the first answer a commit gave, kept so a repeat answers
	// identically. It is stored before state moves to writeCommitted.
	outcome proto.FSWriteCommitResult
}

// writeSeams are the injection points that let a test fail ONE step of the
// sequence deterministically. Every nil field is the real call; a test sets
// exactly the step whose failure it is asserting, which is the only way to
// check "created nothing", "left the temporary alive" and "the target is
// untouched" without a race or a sleep.
type writeSeams struct {
	createTemp   func(dir, name string) (*os.File, error)
	writeChunk   func(f *os.File, data []byte, offset int64) (int, error)
	applyMode    func(temp, candidate string) error
	syncTemp     func(f *os.File) error
	rename       func(oldpath, newpath string, noReplace bool) error
	beforeRename func()
	removeTemp   func(path string) error
}

// pathLocks serialises the installs that land on one resolved path, so two
// sessions committing the same `expect` cannot both pass the check. The entry
// is reference-counted and dropped when the last holder releases it: a helper
// runs for months, and a map that kept a mutex per path ever written would grow
// for no answer.
type pathLocks struct {
	mu   sync.Mutex
	held map[string]*pathLock
}

type pathLock struct {
	mu   sync.Mutex
	refs int
}

func newPathLocks() *pathLocks { return &pathLocks{held: map[string]*pathLock{}} }

// lock blocks until this caller holds path's lock and returns its release.
func (l *pathLocks) lock(path string) func() {
	l.mu.Lock()
	entry := l.held[path]
	if entry == nil {
		entry = &pathLock{}
		l.held[path] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		// The count is decremented under the MAP lock while the entry's own
		// lock is still held: a caller that arrives between the decrement and
		// the unlock must either find the same entry (refs was not 0, so
		// somebody else is still inside) or create a new one for a mutex
		// nobody holds any more. Releasing the entry first would let the two
		// overlap — the deleted entry still locked, a fresh one taken by
		// another caller — which is two writers in one critical section,
		// exactly what this exists to prevent.
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.held, path)
		}
		l.mu.Unlock()
		entry.mu.Unlock()
	}
}

// writeOpen resolves the target ONCE, refuses anything that is not a regular
// file, and creates the temporary beside it. It also takes the daemon's
// admission before anything reaches the disk: the session holds it until it
// commits, aborts, or is ended by its connection or the service
// (nocx-oalo1.12.5).
//
// The resolution happens here and not at commit because the caller shows this
// answer: a path that crossed a symlink writes to the file the link points at,
// and the link itself survives. A dangling link, a directory, a FIFO, a device
// or a socket is `not-regular` with its finer kind — the same refusal a read
// gives, because it is the same question asked before writing.
func (s *Service) writeOpen(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSWriteOpenParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if !validWriteID(p.ID) {
		return nil, &badSession{reason: fmt.Sprintf("id %q is not 32 lower-case hex characters", p.ID)}
	}
	// THE DAEMON IS TOLD BEFORE ANYTHING EXISTS ON DISK. An open write session
	// is work the last hosted session's drain must wait for (nocx-oalo1.12.5),
	// and the reservation precedes the session it covers: a temporary created
	// first would leave a window in which the daemon drained under a write that
	// already had bytes on disk. The refusal a draining daemon gives is passed
	// through unchanged, which is how a coordinator learns to re-acquire.
	var admission func()
	if s.admissions != nil {
		release, err := s.admissions.Admit()
		if err != nil {
			return nil, err
		}
		admission = release
	}
	sess := &writeSession{admission: admission}
	sess.conn.Store(connectionOf(ctx))
	// Every refusal on the way to an open session gives the reservation back:
	// the session does not exist yet, and a leak here is a helper that never
	// retires even though nobody is writing to it.
	opened := false
	defer func() {
		if !opened {
			sess.releaseAdmission()
		}
	}()

	resolved, err := resolveWriteTarget(p.Path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(resolved)
	sess.dir = dir

	// Registration and creation are two critical sections, and the order is
	// deliberate. The session is registered FIRST: a connection cannot end
	// between the two and leave a temporary whose session nobody recorded —
	// the closure would have nothing to abort — and while it is writeOpening a
	// chunk or a commit of it is refused rather than made to wait. The
	// creation itself is file I/O on the target's directory, and a directory
	// on a hung mount does not answer: holding the service's lock across it
	// would stall every other session, every other connection's teardown and
	// the daemon's own ConnectionClosed.
	sess.state.Store(int32(writeOpening))
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errServiceClosed
	}
	if s.sessions == nil {
		s.sessions = map[string]*writeSession{}
	}
	if _, busy := s.sessions[p.ID]; busy {
		s.mu.Unlock()
		return nil, &badSession{reason: "write session id is already in use"}
	}
	s.sessions[p.ID] = sess
	s.mu.Unlock()

	temp := filepath.Join(dir, writeTempPrefix+p.ID)
	// THE JOURNAL ENTRY IS WRITTEN AND SYNCED BEFORE THE TEMPORARY EXISTS, and
	// that order is the whole of what makes a crashed session recoverable
	// (journal.go). A failure here leaves nothing on disk to find: no entry,
	// and the temporary was never created.
	if journalErr := s.startJournal(sess, p.ID, temp); journalErr != nil {
		s.forget(p.ID, sess)
		return nil, journalErr
	}
	// #nosec G304 — the name is derived from the caller's id after it was
	// checked to be hex, and the directory is the target's own, resolved above.
	f, err := s.createTemp(dir, writeTempPrefix+p.ID)
	if err != nil {
		s.forget(p.ID, sess)
		// The temporary was never created, so its entry names nothing that
		// could be left behind; removing it keeps the directory from growing a
		// record per failed open.
		s.dropJournalEntry(sess)
		if errors.Is(err, fs.ErrExist) {
			// A leftover from an earlier generation, or another session that
			// chose the same id. Either way the caller must not write into it.
			return nil, &badSession{reason: "a temporary file for this id already exists in the target's directory"}
		}
		return nil, err
	}
	// The creation mode was already 0600 masked by the umask; fchmod makes it
	// exact rather than dependent on a setting this process did not choose.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(temp)
		s.dropJournalEntry(sess)
		s.forget(p.ID, sess)
		return nil, err
	}

	sess.mu.Lock()
	ctxErr := ctx.Err()
	if ctxErr == nil && writeState(sess.state.Load()) == writeOpening {
		sess.temp = temp
		sess.file = f
		sess.digest = sha256.New()
		sess.state.Store(int32(writeOpen))
		sess.mu.Unlock()
		opened = true
		return proto.FSWriteOpenResult{Resolved: resolved}, nil
	}
	// The request ended while its temporary was being created: the context was
	// cancelled, or the connection closed. This call is the only one that knows
	// the file's name yet, so removing it is its job too — and the mutex is
	// released BEFORE the removal, so a connection closing elsewhere never
	// waits behind an unlink on a directory that has stopped answering.
	sess.state.Store(int32(writeAborted))
	sess.mu.Unlock()
	_ = f.Close()
	if err := os.Remove(temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// The temporary outlived the request that created it. Its entry is the
		// only record of the file's name, so it stays and the file is reported.
		s.recordStranded(temp, fmt.Sprint(err))
	} else {
		s.dropJournalEntry(sess)
	}
	if ctxErr != nil {
		return nil, ctxErr
	}
	return nil, &badSession{reason: "the write session ended before its temporary was ready"}
}

// releaseAdmission gives the daemon back the reservation this session's
// existence holds. Every way a session ends calls it, and the first call wins:
// a session can be committed and then asked again, a connection can close
// under a commit that is finishing, and the daemon must be released once and
// only once (nocx-oalo1.12.5).
func (sess *writeSession) releaseAdmission() {
	if sess.admission == nil {
		return
	}
	sess.admissionOnce.Do(sess.admission)
}

// forget removes a session that never became usable. The identity check keeps
// a slow creation from deleting a session that reused its id afterwards.
func (s *Service) forget(id string, sess *writeSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[id] == sess {
		delete(s.sessions, id)
	}
}

// writeChunk appends one chunk to an open session. Only the next sequential
// offset is accepted: a gap would be a hole in a file whose only purpose is to
// become the target in one rename, and the caller's own position is the size
// this op answers, so a caller cannot lose it.
func (s *Service) writeChunk(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSWriteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Data) > maxWriteChunk {
		return nil, &tooLarge{observed: int64(len(p.Data)), limit: maxWriteChunk}
	}
	sess := s.session(p.ID)
	if sess == nil {
		return nil, &badSession{reason: "no such write session"}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if writeState(sess.state.Load()) != writeOpen {
		return nil, &badSession{reason: "the write session is no longer open"}
	}
	if p.Offset != sess.size {
		return nil, &badOffset{expected: sess.size, got: p.Offset}
	}
	n, err := s.writeTo(sess.file, p.Data, sess.size)
	if err != nil || n != len(p.Data) {
		// Either the write failed after it had already moved bytes, or it moved
		// fewer than asked — ENOSPC arrives WITH a partial count on the
		// ordinary filesystems, and the answer the caller receives is the size
		// from before this chunk. The caller will send the same chunk again at
		// the offset the session still reports, and a retry that landed behind
		// the abandoned half would leave a temporary longer than the size this
		// session acknowledges — which the commit would either install as a
		// corrupted file or refuse forever. Cutting back to the acknowledged
		// size is the one place that tail can be removed.
		if truncErr := sess.file.Truncate(sess.size); truncErr != nil {
			// The temporary no longer matches what this session counted, so
			// nothing may be committed from it. Abort still removes it.
			sess.state.Store(int32(writeBroken))
			if err != nil {
				return nil, err
			}
			return nil, truncErr
		}
		if err != nil {
			return nil, err
		}
		return nil, io.ErrShortWrite
	}
	sess.size += int64(n)
	sess.digest.Write(p.Data)
	return proto.FSWriteResult{Size: sess.size}, nil
}

// writeCommit installs the session's temporary over the target, in the order
// the plan fixes: verify the caller's size, fsync the temporary, take the
// target's lock, give the temporary the target's mode and owner, check the
// expectation, rename, fsync the directory.
//
// The order is not cosmetic. The fsync comes BEFORE the rename because a
// rename of unsynced bytes can be durable while its contents are not, and the
// mode is applied to the TEMPORARY because a rename replaces the target rather
// than modifying it — the mode of the file that lands is the mode the file that
// arrived carries.
func (s *Service) writeCommit(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSWriteCommitParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Size < 0 {
		return nil, &badParams{reason: "size is a byte count and cannot be negative"}
	}
	if p.Expect != nil && p.Expect.Version != nil && p.Expect.Absent != nil {
		return nil, &badParams{reason: "expect.version and expect.absent are alternatives, not a conjunction"}
	}
	if p.Expect != nil && p.Expect.Absent != nil && !*p.Expect.Absent {
		return nil, &badParams{reason: "expect.absent may only be true; false states no condition at all"}
	}

	sess := s.session(p.ID)
	if sess == nil {
		// This daemon never held the session: a restart, or another
		// generation answered the open. The target as it stands is the only
		// evidence the caller can act on.
		return proto.FSWriteCommitResult{
			Outcome: proto.FSOutcomeUnknown,
			Path:    p.Path,
			Current: currentTarget(p.Path, p.Size),
		}, nil
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	switch writeState(sess.state.Load()) {
	case writeCommitted:
		return sess.outcome, nil
	case writeAborted:
		return proto.FSWriteCommitResult{Outcome: proto.FSOutcomeAborted, Path: p.Path}, nil
	case writeOpening:
		return nil, &badSession{reason: "the write session's temporary is not ready yet"}
	case writeBroken:
		return nil, &badSession{reason: "the write session's temporary failed on disk and cannot be committed"}
	}
	if p.Size != sess.size {
		// The session stays open and its temporary stays alive: the caller's
		// count disagrees with ours, which is a fact about the request, not
		// about the file, and re-sending what is missing is the caller's call.
		return nil, &sizeMismatch{expected: sess.size, got: p.Size}
	}
	candidate, err := commitTarget(sess, p.Path, expectsAbsent(p.Expect))
	if err != nil {
		return nil, err
	}
	sess.state.Store(int32(writeCommitting))
	result, err := s.install(sess, candidate, p)
	if err != nil {
		s.afterFailedInstallLocked(ctx, sess)
		return nil, err
	}
	sess.outcome = result
	sess.state.Store(int32(writeCommitted))
	// The bytes are at the path the caller named, so the work this reservation
	// covered is finished (nocx-oalo1.12.5). A repeat of this commit answers the
	// stored outcome and finds the reservation already given back. Over a host
	// the reservation rides the answer to ResponseWritten: the daemon's drain
	// closes every connection, the one carrying this answer included, and a
	// release fired here would race the host's write (review M2).
	if sess.admission != nil && connectionOf(ctx) != nil {
		return committedOverHost{FSWriteCommitResult: result, release: sess.releaseAdmission}, nil
	}
	sess.releaseAdmission()
	return result, nil
}

// afterFailedInstallLocked puts a session whose install failed back where its
// caller can finish it, and checks who still owns it (review M3).
//
// A commit deliberately outlives the connection that opened it — the rename is
// the point of no return and the answer belongs to whoever asks next — so the
// opener can end while the install is running, and ConnectionClosed skips a
// committing session on purpose. If the install then FAILS, the session is
// back to open (or broken) under a connection that is already gone: the caller's
// own teardown matches on the new connection and would never look at it, so the
// admission would be held for the daemon's life. The caller of the commit is
// the live owner when there is one; otherwise nothing can ever finish this
// session and it is ended here.
func (s *Service) afterFailedInstallLocked(ctx context.Context, sess *writeSession) {
	broken := writeState(sess.state.Load()) == writeBroken
	if !broken {
		// The refusal left the temporary usable, which is what a caller's retry
		// needs. Publish this before checking ownerClosed so a simultaneous
		// teardown either sees writeOpen or is seen by this handler.
		sess.state.Store(int32(writeOpen))
	}
	if !sess.ownerClosed.Load() {
		return
	}
	if caller := connectionOf(ctx); caller != nil && caller != sess.conn.Load() {
		// Host.Serve waits for its in-flight requests before it closes the
		// connection, so this caller cannot end before this handler returns.
		sess.conn.Store(caller)
		sess.ownerClosed.Store(false)
		return
	}
	// Nobody is left to ask about these bytes. A removal that succeeds ends the
	// session; one that fails leaves the temporary to fs.inventory and the
	// session in the state that refuses writes and commits, so only a later
	// abort may touch it. Either way the daemon is not held for it.
	if err := s.discard(sess); err == nil {
		sess.state.Store(int32(writeAborted))
	} else {
		sess.state.Store(int32(writeBroken))
	}
	sess.releaseAdmission()
}

// install is the commit's body, run under the session's lock with the state
// already at writeCommitting.
func (s *Service) install(sess *writeSession, candidate string, p proto.FSWriteCommitParams) (proto.FSWriteCommitResult, error) {
	if err := s.syncFile(sess.file); err != nil {
		// A writeback failure — EIO, or ENOSPC/EDQUOT reported at fsync — is not
		// the error the caller may retry. On Linux and NFS the kernel clears the
		// descriptor's error flag after the first report, so a second fsync on
		// this descriptor answers 0 and would install a file with pages that
		// never reached the disk. The session is poisoned instead, and only
		// write-abort can end it.
		sess.state.Store(int32(writeBroken))
		return proto.FSWriteCommitResult{}, err
	}
	info, err := sess.file.Stat()
	if err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	if info.Size() != sess.size {
		// Second line: the temporary holds something this session never
		// acknowledged — a partially written chunk on a filesystem that does
		// not shorten it, say. Installing it would land a file whose length and
		// digest disagree with the version this commit answers, so the commit
		// refuses and leaves the session open for the caller to abort.
		return proto.FSWriteCommitResult{}, fmt.Errorf(
			"hostsvc: the temporary holds %d bytes, the session acknowledges %d", info.Size(), sess.size)
	}
	mtimeNs := info.ModTime().UnixNano()

	unlock := s.locks.lock(candidate)
	defer unlock()

	// The mode the file will LAND with is applied to the temporary, so it is
	// registered as private BEFORE the widening: a failure inside applyMode —
	// a chmod that half succeeded, an owner step that fails after the mode
	// already moved — would otherwise leave the temporary at the target's 0755
	// with no defer in place to put it back.
	stillPrivate := true
	defer func() {
		if stillPrivate && sess.temp != "" {
			_ = os.Chmod(sess.temp, 0o600)
		}
	}()
	noReplace := expectsAbsent(p.Expect)
	if noReplace {
		if err := os.Chmod(sess.temp, newFileMode()); err != nil {
			return proto.FSWriteCommitResult{}, err
		}
	} else if err := s.applyMode(sess.temp, candidate); err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	if err := checkExpectation(candidate, p.Expect); err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	if s.seams.beforeRename != nil {
		s.seams.beforeRename()
	}
	if err := s.renameFile(sess.temp, candidate, noReplace); err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	stillPrivate = false
	// The descriptor stays open until the install is done: a refusal before
	// the rename leaves the session writable, so the caller may finish a file
	// it under-sized rather than start over. Its close cannot fail the
	// OUTCOME — the rename already installed the bytes — so a refusal here is
	// swallowed rather than reported as a write that did not happen.
	_ = sess.file.Close()
	sess.file = nil
	sess.temp = ""
	// The bytes are at the path the caller named, so the temporary this entry
	// named no longer exists: the entry is spent. A failure to remove it is not
	// reported as a failed write — the next start finds the entry naming a file
	// that is not there and removes it (journal.go).
	s.dropJournalEntry(sess)
	// A directory sync failure does not change what happened: the rename has
	// already installed the file. It costs durability across a crash, which is
	// reported rather than pretended, but the answer stays `committed` because
	// the bytes ARE at the path the caller named.
	_ = syncDir(sess.dir)
	return proto.FSWriteCommitResult{
		Outcome: proto.FSOutcomeCommitted,
		Path:    candidate,
		Version: &proto.FSVersion{
			Size:    sess.size,
			MtimeNs: mtimeNs,
			SHA256:  hex.EncodeToString(sess.digest.Sum(nil)),
		},
	}, nil
}

// writeAbort discards an open session's temporary. An abort of a session that
// already committed answers `committed` and touches nothing: the caller asked
// what happened to its bytes, and "they landed" is the answer it must not
// mistake for "they were removed". An unknown id answers `aborted` — this
// daemon holds no temporary of that name, which is the state the caller asked
// for.
func (s *Service) writeAbort(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSWriteAbortParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	sess := s.session(p.ID)
	if sess == nil {
		return proto.FSWriteAbortResult{Outcome: proto.FSOutcomeAborted}, nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	switch writeState(sess.state.Load()) {
	case writeCommitted:
		return s.answerAbortLocked(ctx, sess, proto.FSWriteAbortResult{Outcome: proto.FSOutcomeCommitted}), nil
	case writeAborted:
		return s.answerAbortLocked(ctx, sess, proto.FSWriteAbortResult{Outcome: proto.FSOutcomeAborted}), nil
	}
	if err := s.discard(sess); err != nil {
		// The temporary survived — or a descriptor that reported a failed close
		// still ended — so this session has no file to write and no commit
		// left; its state says so rather than leaving a half-session that a
		// chunk would find. A later abort may still take the removal the
		// failure left behind, and fs.inventory reports the temporary until it
		// does (review M3).
		sess.state.Store(int32(writeBroken))
		// Hold the admission through the refusal frame. Its release can close
		// the connection carrying the error when the daemon is draining.
		if sess.admission != nil && connectionOf(ctx) != nil {
			return nil, &abortRefusal{err: err, release: sess.releaseAdmission}
		}
		sess.releaseAdmission()
		return nil, err
	}
	sess.state.Store(int32(writeAborted))
	return s.answerAbortLocked(ctx, sess, proto.FSWriteAbortResult{Outcome: proto.FSOutcomeAborted}), nil
}

// answerAbortLocked is a successful write-abort's answer. Like a commit's, its
// reservation is given back in ResponseWritten when the answer crosses a host,
// because the drain it releases closes the connection that carries it (review
// M2). `opening`'s refusal and a repeat abort find the reservation already
// given back, so the release is a no-op either way.
func (s *Service) answerAbortLocked(ctx context.Context, sess *writeSession, result proto.FSWriteAbortResult) any {
	if sess.admission != nil && connectionOf(ctx) != nil {
		return abortedOverHost{FSWriteAbortResult: result, release: sess.releaseAdmission}
	}
	sess.releaseAdmission()
	return result
}

// committedOverHost and abortedOverHost are successful write answers whose
// daemon reservation is still held. They marshal exactly as the plain results —
// the unexported field never reaches the wire — and ResponseWritten gives the
// reservation back once the frame is written, so a helper that is draining
// cannot cut off the very answer that says the bytes landed (review M2).
type committedOverHost struct {
	proto.FSWriteCommitResult
	release func()
}

type abortedOverHost struct {
	proto.FSWriteAbortResult
	release func()
}

type abortRefusal struct {
	err     error
	release func()
}

func (e *abortRefusal) Error() string { return e.err.Error() }
func (e *abortRefusal) Unwrap() error { return e.err }

func (s *Service) RefusalWritten(_ context.Context, _ string, err error) {
	if refusal, ok := err.(*abortRefusal); ok {
		refusal.release()
	}
}

// ResponseWritten is host.ResponseObserver: the answer is on the wire, so the
// work it reported is finished and the daemon may retire. It runs for a
// withdrawn answer too — a frame that reached nobody still ended the session it
// answered, and a reservation kept for it would be a leak in the other
// direction.
func (s *Service) ResponseWritten(_ context.Context, _ string, result any, _ host.AnswerDelivery) {
	switch answer := result.(type) {
	case committedOverHost:
		if answer.release != nil {
			answer.release()
		}
	case abortedOverHost:
		if answer.release != nil {
			answer.release()
		}
	}
}

// ConnectionClosed aborts every session still open or broken on the connection
// that just ended, and leaves a committing one alone: the rename is the point of
// no return, and its answer belongs to whoever asks next. It is the
// host.ConnectionObserver capability — the host calls it once, after Serve, and
// a temporary nobody will finish must not outlive the caller that started it.
//
// The state is read twice, and the second read is the one that decides. The
// first is a filter that keeps a settled session off the lock, and a session can
// move between the two — a temporary that became ready a moment ago is a
// session that must still be aborted. Deciding on the unlocked read would skip
// it and leak its temporary for the daemon's lifetime, so the switch runs inside
// the session's lock against the state it will act on.
func (s *Service) ConnectionClosed(h *host.Host) {
	if h == nil {
		return
	}
	for _, sess := range s.allSessions() {
		if sess.conn.Load() != h {
			continue
		}
		// The owner is gone from this moment, WHATEVER state the session is in
		// (review M3). The switch below deliberately leaves a committing
		// session alone — its answer belongs to whoever asks next — but a commit
		// that fails afterwards must be able to see that its opener has ended
		// and hand the session to its caller instead of leaving it behind a
		// connection no teardown will look at again.
		sess.ownerClosed.Store(true)
		if st := writeState(sess.state.Load()); st != writeOpening && st != writeOpen && st != writeBroken {
			continue
		}
		sess.mu.Lock()
		if sess.conn.Load() != h {
			// The failed commit handed the session to its live caller while
			// this teardown was waiting for the lock.
			sess.mu.Unlock()
			continue
		}
		switch writeState(sess.state.Load()) {
		case writeOpening:
			// The temporary is still being created. Marking the session over
			// is all this can do — the write-open that owns the file's name
			// removes it on its way out, and the mark is the only evidence it
			// needs.
			sess.state.Store(int32(writeAborted))
		case writeOpen, writeBroken:
			// A broken session is aborted here for the same reason an open one
			// is: its fsync already failed, so no commit will ever end it, and
			// only this removes its temporary and closes its descriptor.
			if err := s.discard(sess); err == nil {
				sess.state.Store(int32(writeAborted))
			} else {
				// The descriptor went with the removal attempt, so this session
				// can no longer be written or committed; a later abort may still
				// take the removal this one could not. Leaving it open would be a
				// session that accepts a chunk into no file at all.
				sess.state.Store(int32(writeBroken))
			}
		default:
			// The state moved to committing while this was waiting for the
			// lock: the answer belongs to whoever asks next, and the owner
			// flag above is what a failed install reads.
			sess.mu.Unlock()
			continue
		}
		sess.mu.Unlock()
		// The caller is gone and this session can never be finished by anyone:
		// whatever became of its temporary, the daemon must not be held for a
		// file nobody will commit. A removal that failed leaves the temporary
		// to fs.inventory, which is a report rather than work in progress
		// (nocx-oalo1.12.5).
		sess.releaseAdmission()
	}
}

// connectionOf is the host.Host this request arrived on, or nil for a direct
// caller (a unit test's svc.Call, or a service that is not behind a daemon).
func connectionOf(ctx context.Context) *host.Host {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	return conn
}

// RefusesCancel declares the two ends of a session uncancellable (D11): a
// commit that is half way through the rename would either land untracked or
// report an outcome nobody can act on, and an abort that lost its race with a
// commit would answer a caller that no longer exists. Reads, `write-open` and
// `write` stay cancellable — abandoning them costs a temporary, which the
// connection's own closure removes.
func (s *Service) RefusesCancel(op string) bool {
	return op == proto.OpWriteCommit || op == proto.OpWriteAbort
}

// session looks one session up. The service lock is held only for the map.
func (s *Service) session(id string) *writeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// allSessions is a snapshot, so a connection's teardown never holds the map
// lock while removing a file.
func (s *Service) allSessions() []*writeSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions := make([]*writeSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	return sessions
}

// discard closes and removes a session's temporary. A missing temporary is not
// a failure: the only caller that can see one gone is a session that already
// installed it, and that path never reaches here.
//
// A removal that FAILS is the one case the journal exists for after the fact: the
// temporary outlives the session, so its entry stays on disk and the file is
// reported through fs.inventory. Deleting the entry anyway would erase the only
// record of a file this product created and cannot remove.
//
// A removal that SUCCEEDS forgets the path in the inventory too. The snapshot is
// what a coordinator dials this helper for, so a temporary that was reported by
// a failed abort and removed by a later one — or by the connection closing —
// must stop being reported: a warning about a file that is not there is worse
// than no warning.
func (s *Service) discard(sess *writeSession) error {
	var closeErr error
	if sess.file != nil {
		// The descriptor is dropped from the session BEFORE the close is asked:
		// a close that reports a failure has still ended the descriptor, and a
		// session that kept it would refuse every later abort and leave the
		// temporary on disk for ever (review M3).
		f := sess.file
		sess.file = nil
		closeErr = f.Close()
	}
	if sess.temp == "" {
		return closeErr
	}
	if err := s.removeTemp(sess.temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.recordStranded(sess.temp, fmt.Sprint(err))
		return errors.Join(closeErr, err)
	}
	s.forgetStranded(sess.temp)
	sess.temp = ""
	s.dropJournalEntry(sess)
	return closeErr
}

// ── the seams, defaulted ───────────────────────────────────────────────

func (s *Service) removeTemp(path string) error {
	if s.seams.removeTemp != nil {
		return s.seams.removeTemp(path)
	}
	return os.Remove(path)
}

func (s *Service) createTemp(dir, name string) (*os.File, error) {
	if s.seams.createTemp != nil {
		return s.seams.createTemp(dir, name)
	}
	// #nosec G304 — the caller checked the id is hex and the directory is the
	// target's own, resolved by writeOpen.
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func (s *Service) writeTo(f *os.File, data []byte, offset int64) (int, error) {
	if s.seams.writeChunk != nil {
		return s.seams.writeChunk(f, data, offset)
	}
	n, err := f.WriteAt(data, offset)
	if err != nil {
		return n, diskFull(err)
	}
	return n, nil
}

func (s *Service) syncFile(f *os.File) error {
	if s.seams.syncTemp != nil {
		return s.seams.syncTemp(f)
	}
	if err := f.Sync(); err != nil {
		return diskFull(err)
	}
	return nil
}

func (s *Service) renameFile(oldpath, newpath string, noReplace bool) error {
	if s.seams.rename != nil {
		return s.seams.rename(oldpath, newpath, noReplace)
	}
	if !noReplace {
		return os.Rename(oldpath, newpath)
	}
	err := renameNoReplace(oldpath, newpath)
	if errors.Is(err, fs.ErrExist) {
		return &targetExists{path: newpath}
	}
	return err
}

// applyMode gives the temporary the mode and the owner the file that lands
// should have: an existing target's own mode and ownership, and for a new file
// 0666 masked by the umask this process was started with — sampled once, at
// package initialisation, because reading a umask means SETTING it for a
// moment and a helper serves several connections at a time.
func (s *Service) applyMode(temp, candidate string) error {
	if s.seams.applyMode != nil {
		return s.seams.applyMode(temp, candidate)
	}
	return applyTargetMode(temp, candidate)
}

// applyTargetMode is applyMode's real body, one call away from the seam.
func applyTargetMode(temp, candidate string) error {
	info, err := os.Lstat(candidate)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return &notRegular{path: candidate, kind: kindName(info.Mode())}
		}
		if chmodErr := os.Chmod(temp, info.Mode().Perm()); chmodErr != nil {
			return chmodErr
		}
		return chownToTarget(temp, info)
	case errors.Is(err, fs.ErrNotExist):
		return os.Chmod(temp, newFileMode())
	default:
		return err
	}
}

func expectsAbsent(expect *proto.FSWriteExpect) bool {
	return expect != nil && expect.Absent != nil && *expect.Absent
}

// checkExpectation is the conditional half of the commit: the target must
// still be the file the caller read, or must not be there at all.
func checkExpectation(candidate string, expect *proto.FSWriteExpect) error {
	if expect == nil {
		return nil
	}
	if expect.Absent != nil {
		if _, err := os.Lstat(candidate); err == nil {
			return &targetExists{path: candidate}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if expect.Version == nil {
		return nil
	}
	want := *expect.Version
	info, err := os.Stat(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &changedSinceRead{path: candidate, reason: "the file is gone"}
		}
		return err
	}
	if info.Size() != want.Size || info.ModTime().UnixNano() != want.MtimeNs {
		return &changedSinceRead{path: candidate, reason: "size or mtime moved"}
	}
	digest, err := hashRegular(candidate)
	if err != nil {
		return err
	}
	if digest != want.SHA256 {
		return &changedSinceRead{path: candidate, reason: "the contents differ"}
	}
	// The last look before the rename, because the hash above took time and
	// the point of the fence is the instant of the install.
	again, err := os.Stat(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &changedSinceRead{path: candidate, reason: "the file is gone"}
		}
		return err
	}
	if again.Size() != want.Size || again.ModTime().UnixNano() != want.MtimeNs {
		return &changedSinceRead{path: candidate, reason: "size or mtime moved during the check"}
	}
	return nil
}

// hashRegular opens the path, classifies the DESCRIPTOR — a FIFO swapped in
// for the file would otherwise block the daemon — and hashes the object it
// fstat'ed, so the digest and the metadata describe one object.
func hashRegular(path string) (string, error) {
	// #nosec G304 — the path came from a session's own resolved directory.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", &notRegular{path: path, kind: kindName(info.Mode())}
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// currentTarget is what an unknown-outcome answer reports: the target's size
// and mtime always, its digest only when the size is the one the caller asked
// about — comparing digests of two different sizes answers nothing. No target
// at all answers nil, which the wire omits.
func currentTarget(path string, size int64) *proto.FSWriteCurrent {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	current := &proto.FSWriteCurrent{Size: info.Size(), MtimeNs: info.ModTime().UnixNano()}
	if info.Size() != size {
		return current
	}
	if digest, err := hashRegular(path); err == nil {
		current.SHA256 = digest
	}
	return current
}

// commitTarget answers where a commit installs, insisting on the directory
// write-open resolved. The NAME may differ — that is how a KeepBoth collision
// is answered without sending the bytes again — but a path that resolves into
// another directory is a caller writing somewhere it never opened a session
// for, and it is refused before anything is touched.
func commitTarget(sess *writeSession, path string, noReplace bool) (string, error) {
	resolve := resolveWriteTarget
	if noReplace {
		// Any existing directory entry is a collision, including a symlink
		// whose target is absent or sits outside the opened directory.
		resolve = resolveWriteName
	}
	candidate, err := resolve(path)
	if err != nil {
		return "", err
	}
	if dir := filepath.Dir(candidate); dir != sess.dir {
		return "", &filesystem.ErrInvalidPath{Path: path, Reason: fmt.Sprintf(
			"%s is outside %s, the directory this session was opened in", dir, sess.dir)}
	}
	return candidate, nil
}

// resolveWriteTarget resolves a path ONCE: every symlink on it, including a
// final component that is one, so the write lands where the link points and the
// link survives. The final component may legitimately not exist yet; the
// directory it would be created in must.
func resolveWriteTarget(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", &filesystem.ErrInvalidPath{Path: path, Reason: "a write target must be an absolute path"}
	}
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// A dangling symlink would resolve to nothing and a rename onto it
		// would replace the LINK; that is not the file the caller saw.
		if info, lerr := os.Lstat(clean); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", &notRegular{path: path, kind: "symlink"}
		}
		return resolveWriteName(clean)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", &notRegular{path: path, kind: kindName(info.Mode())}
	}
	return resolved, nil
}

// resolveWriteName resolves the directory but does not follow the final name.
func resolveWriteName(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return "", &filesystem.ErrInvalidPath{Path: path, Reason: "a write target must be an absolute file path"}
	}
	clean := filepath.Clean(path)
	dir, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(clean)), nil
}

// validWriteID accepts 128 bits spelled as lower-case hex and nothing else.
// The id is a filename component, so the alphabet is the check: `..`, a
// separator and a leading dot are all unspellable in hex.
func validWriteID(id string) bool {
	if len(id) != writeIDHexLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// renameNoReplaceFallback installs the temporary only if the name is free,
// using the one portable pair that has that meaning: a hard link refuses an
// existing name, and removing the temporary afterwards leaves the target with
// a single link.
func renameNoReplaceFallback(oldpath, newpath string) error {
	if err := os.Link(oldpath, newpath); err != nil {
		return err
	}
	// The data is installed; a temporary this process could not remove is a
	// leftover for the sweep to report, not a reason to tell the caller the
	// write failed.
	_ = os.Remove(oldpath)
	return nil
}
