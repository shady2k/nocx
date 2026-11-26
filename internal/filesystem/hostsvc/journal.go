package hostsvc

// The write-session journal: the durable half of "a helper that dies mid-write
// leaves no file behind" (design §5.10, C24, scenario 9's tail and scenario 13).
//
// # Why a journal at all
//
// A write session's temporary lives beside its target — `.nocx-write-<id>` in a
// directory the helper does not own — so a process that is killed between
// `write-open` and the end of the session leaves a file nothing in this product
// will ever name again. The name carries the session id, which the CALLER holds
// and the next daemon does not: after a restart there is nobody to ask. The
// journal is what closes that: `<root>/<hostname>-<instanceID>/<id>.json` holds
// the absolute temporary path, so a later generation can remove exactly the
// files its predecessors left.
//
// # The ordering is the invariant
//
// The entry is created, its file synced, and the directory entry synced BEFORE
// the temporary is created. That single order is what makes every failure the
// journal has to survive decidable:
//
//   - a crash before the entry: no temporary exists, nothing to find;
//   - a crash after the entry, before the temporary: the sweep finds an entry
//     naming a file that is not there (ENOENT) and removes the entry;
//   - a crash after the temporary: the sweep finds both and removes both.
//
// Without the fsyncs the entry could be a rename that never reached the disk
// while the temporary did, which is the one state nobody can recover from: a
// file whose name is known to the caller that has exited and to no one else.
//
// # One directory per daemon run, and a lock that says "still alive"
//
// The directory name carries the host AND the run's instance id, so a daemon
// never shares a journal with a predecessor. That is what lets a start-up sweep
// be safe on a HOME shared over NFS between machines: it only ever reads
// directories whose name starts with THIS host's, and it takes the `lock` file
// inside one non-blocking before touching a byte of it. A live daemon holds
// that flock for its whole life, so a generation that finds the lock taken
// leaves the directory — and its temporaries — alone. A process that died
// released the lock with its file descriptors, which is the evidence the sweep
// acts on.
//
// # What it refuses to delete
//
// A journal entry is a file this product wrote, but the sweep still treats it
// as untrusted input: it deletes a regular, non-symlink file only when its base
// name is exactly `.nocx-write-<id>` for the id the entry itself names. Anything
// else — a symlink, a directory, an ordinary file with another name, an id that
// is not 128 bits of lower-case hex — is left untouched and its entry kept,
// because a directory whose permissions changed under us must not turn into a
// mechanism for removing whatever a hostile entry spells.
//
// An entry whose BYTES were read and whose JSON does not parse IS removed, and
// the ordering above is the proof that this is safe: a torn entry means the
// crash happened before the temporary existed. Keeping it would leave a
// directory that no start can ever finish with. An entry that could not be READ
// at all is a different state — the temporary it names may well exist — so it
// is kept, counted, and its directory survives with it. The same holds for an
// entry that is not a plain file.
import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// JournalConfig is where one daemon keeps its write-session journal.
//
// Root is the account-wide root (`~/.nocx/fs-writes` in the shipped helper);
// Hostname and InstanceID name THIS run's directory inside it. They are
// parameters rather than reads of the process environment so a test can own a
// journal under its own temporary directory — a package that called
// os.Hostname and os.UserHomeDir itself would make every journal test touch the
// machine it runs on.
type JournalConfig struct {
	Root       string
	Hostname   string
	InstanceID string
}

const (
	// journalLockName is the file whose flock says "this generation is still
	// running". It is created 0600 inside the daemon's own directory.
	journalLockName = "lock"
	// journalEntrySuffix names one session's entry. The id is the rest of the
	// name, and the id is checked to be hex before it is ever used as a path
	// component, so the name cannot escape the directory.
	journalEntrySuffix = ".json"
)

// beforeLock is the seam for the window between creating this run's lock file
// and taking its flock (openJournal). A test uses it to let another starting
// daemon sweep the still-empty directory in exactly that window; in the shipped
// helper it is nil.
var beforeLock func(dir string)

// readJournalEntryBytes reads one entry file. It is a variable so a test can
// make a read fail for a file that exists and is a plain file — the state that
// must keep the entry — without relying on permission bits, which a test
// running as root cannot use to refuse a read.
var readJournalEntryBytes = os.ReadFile

// journal is one daemon's journal directory and the lock that proves it is
// alive. It is constructed once, at start, and closed once, at shutdown.
type journal struct {
	dir  string
	lock *os.File
}

// journalRecord is one session's entry. ID and Temp are redundant with the
// file's own name and path on purpose: the sweep decides what to delete from
// the RECORD, not from the name of the file it was written in, so a record that
// was copied into the directory by hand still cannot name anything outside the
// form the record itself has to satisfy.
type journalRecord struct {
	ID   string `json:"id"`
	Temp string `json:"temp"`
}

// journalOpenAttempts bounds how many times openJournal recreates its directory
// and retries the lock. A retry happens only when a sweep removed a directory
// that was still empty (see openLockedJournalDir); one sweep is the ordinary
// case, so three attempts is already beyond what a start should ever need, and
// an unbounded loop would turn a lock held by a live daemon into a busy wait.
const journalOpenAttempts = 3

// openJournal creates this run's directory, takes its lock, and sweeps the
// directories a previous generation left behind.
//
// THE LOCK IS TAKEN BEFORE THE SWEEP, and that order is load-bearing: another
// generation sweeping the same root treats a directory whose lock it can take
// as abandoned, so a directory that has been created but not yet locked would
// be swept out from under the daemon that is about to use it. That window
// cannot be closed by ordering alone — the directory has to exist before the
// lock file does — so openLockedJournalDir proves after taking the lock that
// the file it holds is still the one on disk, and recreates the directory when
// it is not.
func openJournal(cfg JournalConfig) (*journal, []proto.FSStrandedTemp, error) {
	if cfg.Root == "" {
		return nil, nil, errors.New("hostsvc: the write-session journal needs a root directory")
	}
	if !validWriteID(cfg.InstanceID) {
		return nil, nil, fmt.Errorf("hostsvc: the journal's instance id %q is not 32 lower-case hex characters", cfg.InstanceID)
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		return nil, nil, err
	}
	name := journalDirName(cfg.Hostname, cfg.InstanceID)
	dir := filepath.Join(cfg.Root, name)
	lock, err := openLockedJournalDir(dir)
	if err != nil {
		return nil, nil, err
	}
	j := &journal{dir: dir, lock: lock}
	return j, sweepJournal(cfg.Root, cfg.Hostname, name), nil
}

// openLockedJournalDir creates dir, takes its lock, and answers only once that
// lock belongs to the file still on disk.
//
// THE PROOF IS THE REASON THIS LOOPS. Creating the lock file and taking its
// flock are two steps, and a daemon starting at the same moment sweeps what
// looks like an abandoned directory between them: it finds the file, takes the
// still-free flock, sees no entries, and removes the lock and the directory.
// The flock this daemon then holds is on an unlinked inode, so the directory it
// believes it owns does not exist — and every write-open fails with ENOENT
// until the daemon restarts. os.SameFile is what makes that state observable;
// recreating the directory and locking it again is the answer.
//
// A flock that is HELD is not retried: "this instance id is in use" is the
// ordinary answer for a second daemon of the same generation, and the caller
// reports it as such.
func openLockedJournalDir(dir string) (*os.File, error) {
	lockPath := filepath.Join(dir, journalLockName)
	var lastErr error
	for attempt := 0; attempt < journalOpenAttempts; attempt++ {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		// MkdirAll applies the umask, and the journal holds paths of files this
		// account is writing: nothing else on the machine has any business
		// reading them.
		// #nosec G302 — a directory's mode, and 0700 is the tightest one that is
		// still usable: this process has to create files in it.
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
		// #nosec G304 — dir is this run's own directory under the caller's root.
		lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		if beforeLock != nil {
			beforeLock(dir)
		}
		if err := lockExclusive(lock); err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("hostsvc: the write-session journal %s is in use: %w", dir, err)
		}
		if err := lockBelongsTo(lock, lockPath); err != nil {
			_ = lock.Close()
			lastErr = err
			continue
		}
		return lock, nil
	}
	return nil, fmt.Errorf("hostsvc: the write-session journal %s kept disappearing while it was being locked: %w", dir, lastErr)
}

// lockBelongsTo answers whether the file the flock was taken on is still the
// one at path. A sweep of an empty directory unlinks the lock and leaves the
// flock on the unlinked inode, which is exactly the difference this reports.
func lockBelongsTo(lock *os.File, path string) error {
	held, err := lock.Stat()
	if err != nil {
		return err
	}
	onDisk, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(held, onDisk) {
		return fmt.Errorf("%s was replaced while it was being locked", path)
	}
	return nil
}

// record writes one session's entry and makes it durable BEFORE the caller
// creates the temporary it names. See the file doc for why this order is the
// whole mechanism.
func (j *journal) record(id, temp string) error {
	body, err := json.Marshal(journalRecord{ID: id, Temp: temp})
	if err != nil {
		return err
	}
	path := filepath.Join(j.dir, id+journalEntrySuffix)
	// #nosec G304 — the id was checked to be 128 bits of hex, so the name is a
	// filename component and nothing else.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		// A half-written entry names a temporary that was never created, so
		// removing it here loses nothing — and the sweep would remove it as a
		// torn entry anyway.
		_ = os.Remove(path)
		return err
	}
	// The file's CONTENTS are durable; this makes its NAME durable, which is
	// what a sweep of a later generation looks for.
	if err := syncDir(j.dir); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// forget removes one session's entry. It is called once the temporary it names
// is provably gone — removed, or renamed onto the target — and a failure is
// reported rather than hidden so the caller can say what it could not do.
func (j *journal) forget(id string) error {
	err := os.Remove(filepath.Join(j.dir, id+journalEntrySuffix))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// close removes this run's directory when nothing is left in it, and releases
// the lock.
//
// The directory and its lock file go BEFORE the release, while the flock is
// still held: releasing first would let a daemon starting right now take a lock
// that is about to be unlinked underneath it — the race openJournal detects and
// retries, but one an orderly shutdown has no reason to create. Unlinking a
// locked file does not release the lock, so this stays single-writer until the
// close below.
//
// A directory that still holds an entry SURVIVES with its lock file: the next
// generation must be able to take that lock to report the entry, and removing
// the lock would leave a directory the sweep skips forever.
func (j *journal) close() error {
	if j.lock == nil {
		return nil
	}
	if entries, readErr := os.ReadDir(j.dir); readErr == nil {
		empty := true
		for _, e := range entries {
			if e.Name() != journalLockName {
				empty = false
				break
			}
		}
		if empty {
			_ = os.Remove(filepath.Join(j.dir, journalLockName))
			_ = os.Remove(j.dir)
		}
	}
	err := j.lock.Close()
	j.lock = nil
	return err
}

// sweepJournal reaps the directories of earlier generations of THIS host.
//
// A directory is a candidate when its name is exactly `<host>-` followed by an
// instance id — the form journalDirName writes — and not merely when it carries
// the prefix. The difference is a home shared over NFS: `build-` is a prefix of
// `build-arm-<id>`, so a prefix test lets the `build` host reap the entries and
// temporaries of a live `build-arm`, and on a filesystem whose locks do not
// cross machines even delete them. The id after the separator is what tells two
// machines apart; a directory whose remainder is not 128 bits of hex is not one
// this product wrote, so it is left alone. The daemon's own directory is
// skipped because there is nothing in it to reap.
func sweepJournal(root, hostname, ownName string) []proto.FSStrandedTemp {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	prefix := journalDirPrefix(hostname)
	var stranded []proto.FSStrandedTemp
	for _, d := range dirs {
		name := d.Name()
		if name == ownName || !d.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		if !validWriteID(strings.TrimPrefix(name, prefix)) {
			continue
		}
		stranded = append(stranded, reapJournalDir(filepath.Join(root, name))...)
	}
	return stranded
}

// reapJournalDir cleans one abandoned generation's directory and answers the
// temporaries it could not remove.
//
// A lock that cannot be taken means a daemon of that generation is still
// running: its directory, its entries and its temporaries are exactly what a
// live process needs, so nothing here is touched. On a platform without
// advisory locks, the lock call answers success and this treats every directory
// as abandoned — the shipped helper runs on unix, and this is the same
// limitation write_other.go states for the rest of the write path.
func reapJournalDir(dir string) []proto.FSStrandedTemp {
	// #nosec G304 — dir came from ReadDir of the journal root; the name is a
	// constant.
	lock, err := os.OpenFile(filepath.Join(dir, journalLockName), os.O_RDWR, 0)
	if err != nil {
		// No lock file: not a directory this product can reason about. Its
		// contents are left exactly as they are.
		return nil
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		return nil
	}
	stranded, remaining := reapJournalRecords(dir)
	if remaining == 0 {
		// The lock and the directory go BEFORE the release, while the flock is
		// still held: releasing first would let a daemon starting right now
		// take a lock that is about to be unlinked underneath it.
		_ = os.Remove(filepath.Join(dir, journalLockName))
		_ = os.Remove(dir)
	}
	_ = lock.Close()
	return stranded
}

// reapJournalRecords deletes what one abandoned directory's entries name, and
// answers both the refusals and how many entries are left behind.
//
// "Left behind" is what decides whether the directory may be removed, so every
// path that keeps an entry — an unreadable entry file, a record naming
// something this product did not write, a removal that failed — counts.
func reapJournalRecords(dir string) (stranded []proto.FSStrandedTemp, remaining int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// An unreadable directory is not evidence that it is empty. Keeping it
		// costs a directory; removing one that still holds entries would leave
		// temporaries nobody can name again.
		return nil, 1
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, journalEntrySuffix) {
			continue
		}
		path := filepath.Join(dir, name)
		rec, entry := readJournalRecord(path)
		switch entry {
		case entryTornJSON:
			// The bytes were read and are not JSON, which can only be an entry
			// this product tore BEFORE creating the temporary it names:
			// removing it is safe, and keeping it would leave a directory no
			// start could ever finish with.
			_ = os.Remove(path)
			continue
		case entryUnreadable, entryNotRegular:
			// The entry's own state is unknown — the disk refused a read, or
			// the file is a symlink, a directory or a device. What it names may
			// exist, and a symlink is not something to follow or delete on the
			// strength of its name, so nothing is deleted and the directory is
			// kept with it.
			remaining++
			continue
		}
		temporary, verdict, refusal := temporaryOf(rec)
		switch verdict {
		case tempMissing:
			_ = os.Remove(path)
		case tempNotOurs:
			// The record names something outside the form this service
			// creates. Deletes nothing — not the file, not the record: a
			// directory of entries is evidence somebody should look at.
			remaining++
		case tempRemoved:
			_ = os.Remove(path)
		case tempStranded, tempUnknown:
			remaining++
			stranded = append(stranded, proto.FSStrandedTemp{Path: temporary, Reason: refusal})
		}
	}
	return stranded, remaining
}

// tempVerdict is what the sweep concluded about one record's temporary.
type tempVerdict int

const (
	// tempMissing: the temporary is gone, so the entry is spent.
	tempMissing tempVerdict = iota
	// tempRemoved: the temporary was a file this service created and it was
	// removed with this sweep.
	tempRemoved
	// tempNotOurs: the record does not name a `.nocx-write-<id>` this service
	// could have created. Nothing is deleted.
	tempNotOurs
	// tempStranded: the temporary is ours and removing it failed.
	tempStranded
	// tempUnknown: the temporary's state could not be read at all (an
	// unreadable directory, say), so the entry survives and is reported.
	tempUnknown
)

// temporaryOf classifies and, where it may, removes the file one record names.
// The record is untrusted input: the form check is what confines the one
// deletion this function performs to files this service itself creates. refusal
// is what the caller reports when the temporary outlives the sweep.
func temporaryOf(rec journalRecord) (path string, verdict tempVerdict, refusal string) {
	if !validWriteID(rec.ID) || !filepath.IsAbs(rec.Temp) {
		return rec.Temp, tempNotOurs, ""
	}
	if filepath.Base(rec.Temp) != writeTempPrefix+rec.ID {
		return rec.Temp, tempNotOurs, ""
	}
	info, err := os.Lstat(rec.Temp)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return rec.Temp, tempMissing, ""
	case err != nil:
		return rec.Temp, tempUnknown, fmt.Sprint(err)
	case !info.Mode().IsRegular():
		// A symlink is never followed and never removed: the temporary this
		// service creates is a regular file, so this is something else wearing
		// its name. A directory or a device is the same answer for the same
		// reason.
		return rec.Temp, tempNotOurs, ""
	}
	if err := os.Remove(rec.Temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rec.Temp, tempStranded, fmt.Sprint(err)
	}
	return rec.Temp, tempRemoved, ""
}

// entryVerdict is what reading one entry file answered.
type entryVerdict int

const (
	// entryRecord: the bytes were read and parsed.
	entryRecord entryVerdict = iota
	// entryTornJSON: the bytes were read and are not the JSON this product
	// writes. That is an entry this product tore while writing it, which the
	// ordering makes proof that the temporary it names was never created.
	entryTornJSON
	// entryUnreadable: the entry exists but could not be read, so nothing
	// about the temporary it names is known — and it may exist.
	entryUnreadable
	// entryNotRegular: the entry is not a plain file (a symlink, a directory),
	// so it is never followed, read, or deleted on the strength of its name.
	entryNotRegular
)

// readJournalRecord reads and parses one entry file and answers which of the
// four states it is in. The distinction between entryTornJSON and
// entryUnreadable is the whole point: only the first proves the temporary was
// never created, and only that proof licenses a deletion.
func readJournalRecord(path string) (journalRecord, entryVerdict) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return journalRecord{}, entryNotRegular
	}
	body, err := readJournalEntryBytes(path)
	if err != nil {
		return journalRecord{}, entryUnreadable
	}
	var rec journalRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return journalRecord{}, entryTornJSON
	}
	return rec, entryRecord
}

// journalDirPrefix is the part of a directory name every daemon of this host
// shares: the normalised hostname and the separator before the instance id.
func journalDirPrefix(hostname string) string { return normalizeJournalHostname(hostname) + "-" }

// journalDirName is this run's directory inside the journal root.
func journalDirName(hostname, instanceID string) string {
	return journalDirPrefix(hostname) + instanceID
}

// normalizeJournalHostname makes a hostname usable as a filename component
// without losing which machine it names. A hostname is minted by the machine
// and may carry anything the platform allowed (`_`, `.`, `-` are ordinary, but
// a stray `/` would make the path a directory traversal), so every character
// outside the conservative set becomes `_`. Two machines whose names differ
// only in such characters would share a prefix, which is the same trust domain
// already: the journal is per-account, and the sweep's flock is what decides
// whether a directory belongs to a live run.
func normalizeJournalHostname(hostname string) string {
	if hostname == "" {
		return "unknown-host"
	}
	var b strings.Builder
	b.Grow(len(hostname))
	for _, r := range hostname {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ── the service's half ────────────────────────────────────────────────

// errServiceClosed refuses a session a service that is shutting down could not
// finish. It is not a refusal the wire names: a helper that is closing is
// detected by the connection ending, and this only closes the window between
// the shutdown signal and the endpoint stopping.
var errServiceClosed = errors.New("hostsvc: the service is closed")

// Close ends every session still in progress and then releases the journal.
//
// A session that was writing loses its temporary and its entry: the person who
// started it kept the id, but no coordinator is going to ask this process again,
// and a temporary nobody finishes is exactly what the journal exists to prevent.
// A session already INSIDE its commit is waited for rather than aborted — the
// rename is the point of no return, its lock is held by the commit, and taking
// that lock here is what makes "finished" rather than "interrupted" the answer
// it gets. A session whose temporary cannot be removed keeps its journal entry
// so the next generation reports it instead of a person finding it.
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	var firstErr error
	for _, sess := range s.allSessions() {
		sess.mu.Lock()
		// writeCommitting cannot be observed here: the state is only ever set
		// and cleared under this same lock, so acquiring it means the commit
		// that held it has finished.
		switch writeState(sess.state.Load()) {
		case writeOpening:
			// The write-open that owns the temporary's name removes it on its way
			// out, exactly as a connection closing mid-open does.
			sess.state.Store(int32(writeAborted))
		case writeOpen, writeBroken:
			if err := s.discard(sess); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else {
				sess.state.Store(int32(writeAborted))
			}
		}
		sess.mu.Unlock()
	}
	if err := s.journal.close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// recordStranded remembers a temporary this daemon could not remove. The path
// is the key: the same file reported by the sweep and then by a failed abort is
// one warning, not two, and a path this daemon removed itself is forgotten so it
// stops being reported.
func (s *Service) recordStranded(path, reason string) {
	if path == "" {
		return
	}
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()
	if s.strandedPaths == nil {
		s.strandedPaths = map[string]bool{}
	}
	if s.strandedPaths[path] {
		return
	}
	s.strandedPaths[path] = true
	s.stranded = append(s.stranded, proto.FSStrandedTemp{Path: path, Reason: reason})
}

// forgetStranded drops one path from the inventory snapshot, once the temporary
// it names is provably gone. The snapshot is not a log of everything that ever
// failed: it answers "what can a person still find and remove", and a file this
// daemon removed is no longer part of that answer.
func (s *Service) forgetStranded(path string) {
	if path == "" {
		return
	}
	s.strandedMu.Lock()
	defer s.strandedMu.Unlock()
	if !s.strandedPaths[path] {
		return
	}
	delete(s.strandedPaths, path)
	kept := s.stranded[:0]
	for _, temp := range s.stranded {
		if temp.Path != path {
			kept = append(kept, temp)
		}
	}
	s.stranded = kept
}

// startJournal records one write session's temporary BEFORE the temporary
// exists. The order is the guarantee journal.go's file doc explains.
func (s *Service) startJournal(sess *writeSession, id, temp string) error {
	if err := s.journal.record(id, temp); err != nil {
		return err
	}
	sess.journalEntry = id + journalEntrySuffix
	return nil
}

// dropJournalEntry forgets one session's entry once its temporary is provably
// gone — removed, or renamed onto the target. A failure is NOT escalated to the
// caller: what it would cost is an entry the next start finds naming a file that
// is not there (ENOENT) and removes, which is exactly the state the ordering
// makes safe. It is reported through the session's own end instead.
func (s *Service) dropJournalEntry(sess *writeSession) {
	if sess.journalEntry == "" {
		return
	}
	id := strings.TrimSuffix(sess.journalEntry, journalEntrySuffix)
	if err := s.journal.forget(id); err == nil {
		sess.journalEntry = ""
	}
}
