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
// open → committing → committed is the only forward path. Every refusal —
// `size-mismatch`, `changed-since-read`, `exists`, a failed fsync or rename —
// returns to open with the temporary still on disk, so the caller may fix the
// condition (another name for KeepBoth, a fresh read for a changed file) and
// commit again WITHOUT sending the bytes a second time. The one transition that
// cannot fail back is the rename itself, and a session that has made it answers
// its first outcome forever after.
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
	// writeOpen: the temporary accepts chunks and either end may still be
	// taken by the caller or by its connection going away.
	writeOpen writeState = iota
	// writeCommitting: the rename is being prepared or has happened. A
	// connection closing here does NOT abort — the answer is what the next
	// connection asks for.
	writeCommitting
	// writeCommitted: the bytes are installed, and outcome is the answer
	// every later commit returns untouched.
	writeCommitted
	// writeAborted: the temporary is gone and the target was never touched.
	writeAborted
)

// writeSession is one caller's write in progress. Its fields are guarded by mu
// — a chunk and a commit can arrive concurrently — except state, which is
// atomic so that a connection ending can decide whether a session is still
// open without waiting behind a commit that is legitimately slow.
type writeSession struct {
	// conn is the host.Host the session was opened on, nil outside a host.
	// Only its identity is used: a connection ending aborts what is still
	// open on it.
	conn any
	// dir is the resolved directory the target lives in. A commit may name
	// another file in it (KeepBoth); it may not leave it.
	dir    string
	temp   string
	file   *os.File
	size   int64
	digest hash.Hash

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
	writeChunk   func(f *os.File, data []byte) (int, error)
	syncTemp     func(f *os.File) error
	rename       func(oldpath, newpath string, noReplace bool) error
	beforeRename func()
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
// file, and creates the temporary beside it.
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
	resolved, err := resolveWriteTarget(p.Path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(resolved)

	// The map lock is held across the creation on purpose: the temporary and
	// its registration are ONE step, so a connection cannot end between them
	// and leave a file whose session nobody recorded — the closure would have
	// nothing to abort. It costs a create under a lock, which is a syscall on
	// a directory the caller already named.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]*writeSession{}
	}
	if _, busy := s.sessions[p.ID]; busy {
		return nil, &badSession{reason: "write session id is already in use"}
	}
	// #nosec G304 — the name is derived from the caller's id after it was
	// checked to be hex, and the directory is the target's own, resolved above.
	f, err := s.createTemp(dir, writeTempPrefix+p.ID)
	if err != nil {
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
		_ = os.Remove(filepath.Join(dir, writeTempPrefix+p.ID))
		return nil, err
	}
	// A request the caller withdrew — the connection went away — must not
	// leave its temporary behind. The registration below and this check are
	// the two orderings of one race: cancellation first is caught here, and a
	// closure first is caught by ConnectionClosed, which sees whatever the
	// map holds when it runs.
	if err := ctx.Err(); err != nil {
		_ = f.Close()
		_ = os.Remove(filepath.Join(dir, writeTempPrefix+p.ID))
		return nil, err
	}
	s.sessions[p.ID] = &writeSession{
		conn:   host.ConnectionFrom(ctx),
		dir:    dir,
		temp:   filepath.Join(dir, writeTempPrefix+p.ID),
		file:   f,
		digest: sha256.New(),
	}
	return proto.FSWriteOpenResult{Resolved: resolved}, nil
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
	n, err := s.writeTo(sess.file, p.Data)
	if err != nil {
		return nil, err
	}
	if n != len(p.Data) {
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
	}
	if p.Size != sess.size {
		// The session stays open and its temporary stays alive: the caller's
		// count disagrees with ours, which is a fact about the request, not
		// about the file, and re-sending what is missing is the caller's call.
		return nil, &sizeMismatch{expected: sess.size, got: p.Size}
	}
	candidate, err := commitTarget(sess, p.Path)
	if err != nil {
		return nil, err
	}
	sess.state.Store(int32(writeCommitting))
	result, err := s.install(sess, candidate, p)
	if err != nil {
		// Every refusal before the rename leaves the session exactly as it
		// was: open, with its temporary and its bytes.
		sess.state.Store(int32(writeOpen))
		return nil, err
	}
	sess.outcome = result
	sess.state.Store(int32(writeCommitted))
	return result, nil
}

// install is the commit's body, run under the session's lock with the state
// already at writeCommitting.
func (s *Service) install(sess *writeSession, candidate string, p proto.FSWriteCommitParams) (proto.FSWriteCommitResult, error) {
	if err := s.syncFile(sess.file); err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	info, err := sess.file.Stat()
	if err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	mtimeNs := info.ModTime().UnixNano()

	unlock := s.locks.lock(candidate)
	defer unlock()

	if err := s.applyMode(sess.temp, candidate); err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	// The mode the file will LAND with is applied to the temporary, so a
	// refusal below must put the session's privacy back: the bytes are the
	// caller's until they are installed, and a temporary left at the target's
	// 0755 would let the rest of the account read a write that never happened.
	stillPrivate := true
	defer func() {
		if stillPrivate && sess.temp != "" {
			_ = os.Chmod(sess.temp, 0o600)
		}
	}()
	if err := checkExpectation(candidate, p.Expect); err != nil {
		return proto.FSWriteCommitResult{}, err
	}
	if s.seams.beforeRename != nil {
		s.seams.beforeRename()
	}
	noReplace := p.Expect != nil && p.Expect.Absent != nil && *p.Expect.Absent
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
		return proto.FSWriteAbortResult{Outcome: proto.FSOutcomeCommitted}, nil
	case writeAborted:
		return proto.FSWriteAbortResult{Outcome: proto.FSOutcomeAborted}, nil
	}
	if err := s.discard(sess); err != nil {
		return nil, err
	}
	sess.state.Store(int32(writeAborted))
	return proto.FSWriteAbortResult{Outcome: proto.FSOutcomeAborted}, nil
}

// ConnectionClosed aborts every session still OPEN on the connection that just
// ended, and leaves a committing one alone: the rename is the point of no
// return, and its answer belongs to whoever asks next. It is the
// host.ConnectionObserver capability — the host calls it once, after Serve, and
// a temporary nobody will finish must not outlive the caller that started it.
func (s *Service) ConnectionClosed(h *host.Host) {
	if h == nil {
		return
	}
	for _, sess := range s.allSessions() {
		if sess.conn != any(h) {
			continue
		}
		if writeState(sess.state.Load()) != writeOpen {
			continue
		}
		sess.mu.Lock()
		if writeState(sess.state.Load()) == writeOpen {
			if err := s.discard(sess); err == nil {
				sess.state.Store(int32(writeAborted))
			}
		}
		sess.mu.Unlock()
	}
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
func (s *Service) discard(sess *writeSession) error {
	if sess.file != nil {
		if err := sess.file.Close(); err != nil {
			return err
		}
		sess.file = nil
	}
	if sess.temp == "" {
		return nil
	}
	if err := os.Remove(sess.temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	sess.temp = ""
	return nil
}

// ── the seams, defaulted ───────────────────────────────────────────────

func (s *Service) createTemp(dir, name string) (*os.File, error) {
	if s.seams.createTemp != nil {
		return s.seams.createTemp(dir, name)
	}
	// #nosec G304 — the caller checked the id is hex and the directory is the
	// target's own, resolved by writeOpen.
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func (s *Service) writeTo(f *os.File, data []byte) (int, error) {
	if s.seams.writeChunk != nil {
		return s.seams.writeChunk(f, data)
	}
	n, err := f.Write(data)
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
func commitTarget(sess *writeSession, path string) (string, error) {
	candidate, err := resolveWriteTarget(path)
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
		dir, dirErr := filepath.EvalSymlinks(filepath.Dir(clean))
		if dirErr != nil {
			return "", dirErr
		}
		return filepath.Join(dir, filepath.Base(clean)), nil
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
