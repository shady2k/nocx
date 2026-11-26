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
// An entry whose JSON cannot be read at all IS removed, and the ordering above
// is the proof that this is safe: a torn entry means the crash happened before
// the temporary existed. Keeping it would leave a directory that no start can
// ever finish with.
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

// openJournal creates this run's directory, takes its lock, and sweeps the
// directories a previous generation left behind.
//
// THE LOCK IS TAKEN BEFORE THE SWEEP, and that order is load-bearing: another
// generation sweeping the same root treats a directory whose lock it can take
// as abandoned, so a directory that has been created but not yet locked would
// be swept out from under the daemon that is about to use it.
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	// MkdirAll applies the umask, and the journal holds paths of files this
	// account is writing: nothing else on the machine has any business
	// reading them.
	// #nosec G302 — a directory's mode, and 0700 is the tightest one that is
	// still usable: this process has to create files in it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, err
	}
	// #nosec G304 — dir is this run's own directory under the caller's root.
	lock, err := os.OpenFile(filepath.Join(dir, journalLockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("hostsvc: the write-session journal %s is in use: %w", dir, err)
	}
	j := &journal{dir: dir, lock: lock}
	return j, sweepJournal(cfg.Root, cfg.Hostname, name), nil
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
		// removing it here loses nothing — and the sweep would remove it as
		// unreadable anyway.
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

// close releases the lock and removes this run's directory when nothing is
// left in it. A directory that still holds an entry SURVIVES with its lock
// file: the next generation must be able to take that lock to report the entry,
// and removing the lock would leave a directory the sweep skips forever.
func (j *journal) close() error {
	if j.lock == nil {
		return nil
	}
	err := j.lock.Close()
	j.lock = nil
	if err != nil {
		return err
	}
	entries, readErr := os.ReadDir(j.dir)
	if readErr != nil {
		return nil
	}
	for _, e := range entries {
		if e.Name() != journalLockName {
			return nil
		}
	}
	_ = os.Remove(filepath.Join(j.dir, journalLockName))
	_ = os.Remove(j.dir)
	return nil
}

// sweepJournal reaps the directories of earlier generations of THIS host.
//
// A directory is a candidate when its name starts with `<host>-`, which is the
// prefix every daemon of this host writes; another host's directories on a
// shared home are never opened at all. The daemon's own directory is skipped
// because there is nothing in it to reap.
func sweepJournal(root, hostname, ownName string) []proto.FSStrandedTemp {
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	prefix := journalDirPrefix(hostname)
	var stranded []proto.FSStrandedTemp
	for _, d := range dirs {
		name := d.Name()
		if name == ownName || !strings.HasPrefix(name, prefix) || !d.IsDir() {
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
	_ = lock.Close()
	if remaining == 0 {
		_ = os.Remove(filepath.Join(dir, journalLockName))
		_ = os.Remove(dir)
	}
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
		rec, ok := readJournalRecord(path)
		if !ok {
			// A record this product could not have finished writing, or one
			// that is not a plain file at all. The first is provably
			// temp-free (the entry is synced before the temporary exists); the
			// second is left alone and counted, because a symlink in the
			// journal is not something to follow or delete on the strength of
			// its name.
			if readableEntryFile(path) {
				_ = os.Remove(path)
				continue
			}
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

// readJournalRecord parses one entry file. ok is false for anything this
// product did not finish writing.
func readJournalRecord(path string) (journalRecord, bool) {
	if !readableEntryFile(path) {
		return journalRecord{}, false
	}
	body, err := os.ReadFile(path) // #nosec G304 — the journal directory is this daemon's own.
	if err != nil {
		return journalRecord{}, false
	}
	var rec journalRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return journalRecord{}, false
	}
	return rec, true
}

// readableEntryFile answers whether the entry itself is a plain file this
// product could have written — never a symlink, never a directory.
func readableEntryFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
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
// one warning, not two.
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
