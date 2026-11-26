package hostsvc

// The write-session journal's tests: the interval that makes a crashed session
// recoverable, the sweep that recovers it, and the inventory that reports what
// the sweep could not remove (design §5.10, scenario 9's tail and scenario 13).
//
// The seam is the same one write_test.go uses — the ops through Call, real files
// in a directory the test owns — because what a coordinator can act on is the
// answer on the wire, not the state of a struct. The two places these tests
// reach inside are deliberate and named: `crashForTest` releases a daemon's lock
// without cleaning up (which is exactly what SIGKILL does to a process), and the
// sweep is asked to run by constructing the NEXT service rather than by calling
// it directly.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/proto"
)

const (
	// crashedInstance and successorInstance name two runs of one daemon on one
	// journal root. They are spelled out rather than generated so a failure
	// names the directory it is about.
	crashedInstance   = "11111111111111111111111111111111"
	successorInstance = "22222222222222222222222222222222"
	successorAgainIns = "33333333333333333333333333333333"
)

// journalRooted builds a service over provider on an explicit journal root and
// instance, which is how a test arranges a predecessor and a successor on the
// same disk.
func journalRooted(t *testing.T, provider *local.Provider, root, instance string) *Service {
	t.Helper()
	svc, err := New(provider, JournalConfig{Root: root, Hostname: "test-host", InstanceID: instance})
	if err != nil {
		t.Fatalf("new fs service (instance %s): %v", instance, err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// crashForTest releases a daemon's journal lock without removing anything —
// precisely what SIGKILL leaves behind: the kernel closes the descriptors, the
// flock goes with them, and every byte on disk stays exactly as it was.
func crashForTest(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.journal.lock.Close(); err != nil {
		t.Fatalf("releasing the journal lock: %v", err)
	}
	svc.journal.lock = nil
}

// journalEntries lists the entry files in dir, which is what says whether a
// session was recorded and whether it still is.
func journalEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		t.Fatalf("reading the journal directory: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), journalEntrySuffix) {
			names = append(names, e.Name())
		}
	}
	return names
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s still exists (err = %v)", path, err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s is gone: %v", path, err)
	}
}

// inventoryOf asks the service the way a coordinator does and decodes the
// answer, so the assertions are about what is on the wire.
func inventoryOf(t *testing.T, svc *Service) proto.FSInventoryResult {
	t.Helper()
	answer, err := callAs[proto.FSInventoryResult](t, svc, proto.OpInventory, proto.FSInventoryParams{})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	return answer
}

// TestWriteJournalEntryIsOnDiskBeforeTheTemporary is the invariant the whole
// recovery rests on, asserted at the only moment it can be observed: the seam
// that creates the temporary runs AFTER the entry was written and synced, and
// before the temporary exists.
func TestWriteJournalEntryIsOnDiskBeforeTheTemporary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)

	realCreate := svc.seams.createTemp
	svc.seams.createTemp = func(dir, name string) (*os.File, error) {
		// The temporary does not exist yet: this is the seam.
		if _, err := os.Lstat(temp); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the temporary existed before the journal entry was checked (err = %v)", err)
		}
		entries := journalEntries(t, svc.journal.dir)
		if len(entries) != 1 || entries[0] != id+journalEntrySuffix {
			t.Errorf("journal entries at temporary creation = %v, want just %q", entries, id+journalEntrySuffix)
		}
		body, err := os.ReadFile(filepath.Join(svc.journal.dir, id+journalEntrySuffix)) // #nosec G304 — the fixture this test built.
		if err != nil {
			t.Fatalf("reading the journal entry: %v", err)
		}
		var rec journalRecord
		if err := json.Unmarshal(body, &rec); err != nil {
			t.Fatalf("the journal entry is not readable: %v", err)
		}
		if rec.ID != id || rec.Temp != temp {
			t.Errorf("journal entry = %+v, want id %q naming %q", rec, id, temp)
		}
		if realCreate != nil {
			return realCreate(dir, name)
		}
		return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 — the test's own directory.
	}

	writeOpenOK(t, svc, id, target)
	writeOK(t, svc, id, 0, "half")
	mustExist(t, temp)
	if got := journalEntries(t, svc.journal.dir); len(got) != 1 {
		t.Fatalf("journal entries after a write = %v, want one", got)
	}
}

// TestWriteOpenWithNoTemporaryLeavesNoJournalEntry is the other end of the same
// interval: a failure between the entry and the temporary rolls the entry back,
// so a helper that cannot create a file does not accumulate a record per attempt
// for the next start to read.
func TestWriteOpenWithNoTemporaryLeavesNoJournalEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	boom := errors.New("temporary creation refused")
	svc.seams.createTemp = func(string, string) (*os.File, error) { return nil, boom }

	_, err := callAs[proto.FSWriteOpenResult](t, svc, proto.OpWriteOpen, proto.FSWriteOpenParams{ID: id, Path: target})
	if err == nil {
		t.Fatal("write-open answered a temporary it never created")
	}
	mustNotExist(t, filepath.Join(dir, writeTempPrefix+id))
	if got := journalEntries(t, svc.journal.dir); len(got) != 0 {
		t.Fatalf("journal entries after a failed open = %v, want none", got)
	}
}

// TestAbortRemovesTheTemporaryAndItsEntry is the orderly end of a session the
// caller gave up on: nothing is left for the next start to find.
func TestAbortRemovesTheTemporaryAndItsEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)
	writeOpenOK(t, svc, id, target)
	writeOK(t, svc, id, 0, "half")
	mustExist(t, temp)

	aborted := writeAbortOK(t, svc, id)
	if aborted.Outcome != proto.FSOutcomeAborted {
		t.Fatalf("abort outcome = %q, want %q", aborted.Outcome, proto.FSOutcomeAborted)
	}
	mustNotExist(t, temp)
	if got := journalEntries(t, svc.journal.dir); len(got) != 0 {
		t.Fatalf("journal entries after an abort = %v, want none", got)
	}
	landed, err := os.ReadFile(target) // #nosec G304 — the fixture this test wrote.
	if err != nil || string(landed) != "original\n" {
		t.Fatalf("the abort changed the target: %q (%v)", landed, err)
	}
}

// TestCommitRemovesTheEntryItInstalled is the interval's closing event: the
// entry is spent the moment the bytes are at the path the caller named.
func TestCommitRemovesTheEntryItInstalled(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, dir)
	id := writeID(t.Name())
	writeOpenOK(t, svc, id, target)
	writeOK(t, svc, id, 0, "replacement\n")
	if _, err := callAs[proto.FSWriteCommitResult](t, svc, proto.OpWriteCommit, proto.FSWriteCommitParams{
		ID: id, Path: target, Size: int64(len("replacement\n")),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := journalEntries(t, svc.journal.dir); len(got) != 0 {
		t.Fatalf("journal entries after a commit = %v, want none", got)
	}
}

// TestCloseAbortsOpenSessionsAndRemovesTheRunDirectory is the orderly shutdown:
// a session still open when the daemon stops loses its temporary and its entry,
// and the run's directory goes with the last of them.
func TestCloseAbortsOpenSessionsAndRemovesTheRunDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	svc := journalRooted(t, local.New(local.WithRoot(dir)), root, crashedInstance)
	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)
	writeOpenOK(t, svc, id, target)
	writeOK(t, svc, id, 0, "half")
	journalDir := svc.journal.dir
	mustExist(t, temp)

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mustNotExist(t, temp)
	mustNotExist(t, journalDir)
}

// TestSweepRemovesTheTemporaryOfACrashedDaemon is scenario 9's tail: a helper
// killed between open and commit leaves a temporary and an entry, and the next
// helper removes both before it serves anything.
func TestSweepRemovesTheTemporaryOfACrashedDaemon(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	crashed := journalRooted(t, local.New(local.WithRoot(dir)), root, crashedInstance)
	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)
	writeOpenOK(t, crashed, id, target)
	writeOK(t, crashed, id, 0, "half")
	crashedDir := crashed.journal.dir
	crashForTest(t, crashed)

	successor := journalRooted(t, local.New(local.WithRoot(dir)), root, successorInstance)
	mustNotExist(t, temp)
	mustNotExist(t, crashedDir)
	if got := inventoryOf(t, successor); len(got.Stranded) != 0 {
		t.Fatalf("inventory after a clean sweep = %+v, want nothing stranded", got.Stranded)
	}
}

// TestSweepLeavesALiveDaemonsTemporariesAlone proves the flock's half: the
// generation before this one is still holding its lock, so its temporary is a
// file somebody is about to finish, not litter.
func TestSweepLeavesALiveDaemonsTemporariesAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	live := journalRooted(t, local.New(local.WithRoot(dir)), root, crashedInstance)
	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)
	writeOpenOK(t, live, id, target)
	writeOK(t, live, id, 0, "half")

	successor := journalRooted(t, local.New(local.WithRoot(dir)), root, successorInstance)
	mustExist(t, temp)
	if got := journalEntries(t, live.journal.dir); len(got) != 1 {
		t.Fatalf("the live daemon's journal lost its entry: %v", got)
	}
	if got := inventoryOf(t, successor); len(got.Stranded) != 0 {
		t.Fatalf("a live daemon's temporary was reported as stranded: %+v", got.Stranded)
	}
}

// TestSweepNeverTouchesAnotherHostsJournal is the shared-home case: two
// machines' directories sit in one root, and a start on this host opens — let
// alone deletes — nothing under the other host's prefix.
func TestSweepNeverTouchesAnotherHostsJournal(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	otherHost, err := New(local.New(local.WithRoot(dir)), JournalConfig{
		Root: root, Hostname: "other-host", InstanceID: crashedInstance,
	})
	if err != nil {
		t.Fatalf("new fs service for the other host: %v", err)
	}
	t.Cleanup(func() { _ = otherHost.Close() })
	id := writeID(t.Name())
	temp := filepath.Join(dir, writeTempPrefix+id)
	writeOpenOK(t, otherHost, id, target)
	writeOK(t, otherHost, id, 0, "half")
	otherDir := otherHost.journal.dir
	crashForTest(t, otherHost)

	ours := journalRooted(t, local.New(local.WithRoot(dir)), root, successorInstance)
	mustExist(t, temp)
	mustExist(t, otherDir)
	if got := inventoryOf(t, ours); len(got.Stranded) != 0 {
		t.Fatalf("another host's temporary was reported: %+v", got.Stranded)
	}
}

// TestSweepDeletesOnlyTheTemporariesItCouldHaveWritten is the safety half: an
// entry naming anything other than a regular `.nocx-write-<id>` of the id it
// declares deletes nothing at all — not the file, and not the entry that named
// it, because a journal directory a person has to look at is better than a
// mechanism that removes whatever a record spells.
func TestSweepDeletesOnlyTheTemporariesItCouldHaveWritten(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	journalDir := filepath.Join(root, journalDirName("test-host", crashedInstance))
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, journalLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	validID := strings.Repeat("a", 32)
	validTemp := filepath.Join(dir, writeTempPrefix+validID)
	ordinary := filepath.Join(dir, "ordinary.txt")
	linkTarget := filepath.Join(dir, "link-target")
	linkedName := strings.Repeat("b", 32)
	linkedTemp := filepath.Join(dir, writeTempPrefix+linkedName)
	dirID := strings.Repeat("c", 32)
	dirTemp := filepath.Join(dir, writeTempPrefix+dirID)
	invalidID := "not-hex"
	invalidTemp := filepath.Join(dir, writeTempPrefix+invalidID)

	for _, path := range []string{validTemp, ordinary, linkTarget, invalidTemp} {
		if err := os.WriteFile(path, []byte("bytes\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(linkTarget, linkedTemp); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dirTemp, 0o700); err != nil {
		t.Fatal(err)
	}

	records := map[string]journalRecord{
		"valid":     {ID: validID, Temp: validTemp},
		"ordinary":  {ID: validID, Temp: ordinary},
		"symlinked": {ID: linkedName, Temp: linkedTemp},
		"directory": {ID: dirID, Temp: dirTemp},
		"invalid":   {ID: invalidID, Temp: invalidTemp},
	}
	for name, rec := range records {
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(journalDir, name+journalEntrySuffix), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	svc := journalRooted(t, local.New(local.WithRoot(dir)), root, successorInstance)

	// The one entry this product could have written: both the temporary and its
	// record are gone.
	mustNotExist(t, validTemp)
	mustNotExist(t, filepath.Join(journalDir, "valid"+journalEntrySuffix))
	// Everything else, and the record that named it, is exactly as it was.
	mustExist(t, ordinary)
	mustExist(t, linkTarget)
	mustExist(t, linkedTemp)
	mustExist(t, dirTemp)
	mustExist(t, invalidTemp)
	mustExist(t, filepath.Join(journalDir, "ordinary"+journalEntrySuffix))
	mustExist(t, filepath.Join(journalDir, "symlinked"+journalEntrySuffix))
	mustExist(t, filepath.Join(journalDir, "directory"+journalEntrySuffix))
	mustExist(t, filepath.Join(journalDir, "invalid"+journalEntrySuffix))
	if got := inventoryOf(t, svc); len(got.Stranded) != 0 {
		t.Fatalf("files this service did not write were reported as stranded: %+v", got.Stranded)
	}
}

// TestUndeletableTemporaryIsReportedAndSurvivesTheNextStart is scenario 13: a
// temporary the sweep cannot remove is named in fs.inventory with the
// operating system's reason, its journal entry stays on disk, and the start
// after that finds and reports the same file again — the record is what
// outlives the failure, not the daemon that met it.
func TestUndeletableTemporaryIsReportedAndSurvivesTheNextStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; a directory's permission bits do not refuse a removal")
	}
	parent := t.TempDir()
	protected := filepath.Join(parent, "read-only")
	if err := os.Mkdir(protected, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(protected, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	crashed := journalRooted(t, local.New(local.WithRoot(protected)), root, crashedInstance)
	id := writeID(t.Name())
	temp := filepath.Join(protected, writeTempPrefix+id)
	writeOpenOK(t, crashed, id, target)
	writeOK(t, crashed, id, 0, "half")
	crashedDir := crashed.journal.dir
	crashForTest(t, crashed)

	// #nosec G302 — the fixture's whole point is a directory that refuses a
	// removal; the cleanup below restores it so the test can be tidied away.
	if err := os.Chmod(protected, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(protected, 0o750) }) // #nosec G302 — restoring the fixture's own mode.

	successor := journalRooted(t, local.New(local.WithRoot(protected)), root, successorInstance)
	mustExist(t, temp)
	if got := journalEntries(t, crashedDir); len(got) != 1 || got[0] != id+journalEntrySuffix {
		t.Fatalf("journal entries after a failed removal = %v, want the session's own", got)
	}
	stranded := inventoryOf(t, successor)
	if len(stranded.Stranded) != 1 {
		t.Fatalf("inventory = %+v, want the one temporary that could not be removed", stranded.Stranded)
	}
	if stranded.Stranded[0].Path != temp {
		t.Fatalf("stranded path = %q, want %q", stranded.Stranded[0].Path, temp)
	}
	if stranded.Stranded[0].Reason == "" {
		t.Fatal("a stranded temporary carried no reason, which is the only thing a person can act on")
	}

	// The start after that: the same file, still undeletable, still named.
	again := journalRooted(t, local.New(local.WithRoot(protected)), root, successorAgainIns)
	second := inventoryOf(t, again)
	if len(second.Stranded) != 1 || second.Stranded[0].Path != temp {
		t.Fatalf("the next start reported %+v, want the same stranded temporary", second.Stranded)
	}
	if second.Stranded[0].Reason == "" {
		t.Fatal("the reason was lost between two starts")
	}
}

// TestAbortThatCannotRemoveTheTemporaryKeepsItStranded is the other way a
// temporary survives its session: the caller aborted, this daemon tried, and the
// directory refused. The answer is the error — the file IS still there — and the
// file is reported, which is the only way a person learns about it.
func TestAbortThatCannotRemoveTheTemporaryKeepsItStranded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; a directory's permission bits do not refuse a removal")
	}
	parent := t.TempDir()
	protected := filepath.Join(parent, "read-only")
	if err := os.Mkdir(protected, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(protected, "note.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := newWriteService(t, protected)
	id := writeID(t.Name())
	temp := filepath.Join(protected, writeTempPrefix+id)
	writeOpenOK(t, svc, id, target)
	writeOK(t, svc, id, 0, "half")

	// #nosec G302 — the fixture's whole point is a directory that refuses a
	// removal; the cleanup below restores it so the test can be tidied away.
	if err := os.Chmod(protected, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(protected, 0o750) }) // #nosec G302 — restoring the fixture's own mode.

	if _, err := callAs[proto.FSWriteAbortResult](t, svc, proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id}); err == nil {
		t.Fatal("abort answered success for a temporary it could not remove")
	}
	mustExist(t, temp)
	if got := journalEntries(t, svc.journal.dir); len(got) != 1 {
		t.Fatalf("journal entries after a failed abort = %v, want the session's own", got)
	}
	stranded := inventoryOf(t, svc)
	if len(stranded.Stranded) != 1 || stranded.Stranded[0].Path != temp || stranded.Stranded[0].Reason == "" {
		t.Fatalf("inventory = %+v, want the temporary the abort could not remove", stranded.Stranded)
	}
}

// TestInventoryAnswersAnEmptyListAndNeverNull is the shape a coordinator decodes:
// "nothing stranded" is [], because a decoder forced to tell it from an absent
// answer would one day stay silent about something.
func TestInventoryAnswersAnEmptyListAndNeverNull(t *testing.T) {
	dir := t.TempDir()
	svc := newWriteService(t, dir)
	answer := inventoryOf(t, svc)
	if answer.Stranded == nil {
		t.Fatal("inventory answered a nil slice; an empty answer must marshal as []")
	}
	body, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"stranded":[]}` {
		t.Fatalf("an empty inventory is %s, want {\"stranded\":[]}", body)
	}
}

// writeAbortOK is write_test.go's helper for the four ops, restated here because
// that file's is named for its own assertions; both go through Call.
func writeAbortOK(t *testing.T, svc *Service, id string) proto.FSWriteAbortResult {
	t.Helper()
	out, err := callAs[proto.FSWriteAbortResult](t, svc, proto.OpWriteAbort, proto.FSWriteAbortParams{ID: id})
	if err != nil {
		t.Fatalf("write-abort: %v", err)
	}
	return out
}
