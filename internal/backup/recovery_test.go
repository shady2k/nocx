package backup_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/waittest"
)

// THE RESTORE INTERVAL, FROM THE INSIDE (ADR-8eej3e66 §5).
//
// A restore writes five stores between one "prepared" journal entry and one
// "committed" entry. Every one of those writes can fail, and the invariant the
// whole design rests on is the interval, not any single moment: from the
// prepared entry until the stores are back at the old generation (rollback) or
// the journal says committed (forward completion), NO reader may observe one
// store from each generation.
//
// These tests inject the failure at each boundary and assert the interval's
// two ends — the old generation everywhere, or a RecoveryError that tells the
// caller to quarantine. The capability layer's test proves the second end.

// ── Stores with a controllable failure ─────────────────────────────────

type switchConnStore struct {
	snap    profile.ConnectionSnapshot
	calls   int
	failAt  int
	blockAt int
	entered chan struct{}
	release chan struct{}
	left    chan struct{}
}

func (s *switchConnStore) LoadConnectionSnapshot() (profile.ConnectionSnapshot, error) {
	return s.snap, nil
}

func (s *switchConnStore) ReplaceConnectionSnapshot(v profile.ConnectionSnapshot) error {
	s.calls++
	if s.blockAt > 0 && s.calls == s.blockAt {
		close(s.entered)
		<-s.release
		if s.left != nil {
			close(s.left)
		}
	}
	if s.failAt > 0 && s.calls == s.failAt {
		return errors.New("injected connections write failure")
	}
	s.snap = v
	return nil
}

type switchSettingsStore struct {
	overrides map[string]any
	calls     int
	failAt    int
}

func (s *switchSettingsStore) NonSecretOverrides() map[string]any {
	out := make(map[string]any, len(s.overrides))
	for k, v := range s.overrides {
		out[k] = v
	}
	return out
}

func (s *switchSettingsStore) ReplaceNonSecretOverrides(v map[string]any) (settings.PendingNotification, error) {
	s.calls++
	if s.failAt > 0 && s.calls == s.failAt {
		return settings.PendingNotification{}, errors.New("injected settings write failure")
	}
	s.overrides = make(map[string]any, len(v))
	for k, val := range v {
		s.overrides[k] = val
	}
	return settings.PendingNotification{}, nil
}

func (s *switchSettingsStore) Publish(settings.PendingNotification) {}

func (s *switchSettingsStore) ValidateSetting(string, any) error { return nil }

type switchSnippetStore struct {
	list   []snippet.Snippet
	calls  int
	failAt int
}

func (s *switchSnippetStore) LoadAll() ([]snippet.Snippet, error) {
	return append([]snippet.Snippet(nil), s.list...), nil
}

func (s *switchSnippetStore) SaveAll(v []snippet.Snippet) error {
	s.calls++
	if s.failAt > 0 && s.calls == s.failAt {
		return errors.New("injected snippets write failure")
	}
	s.list = append([]snippet.Snippet(nil), v...)
	return nil
}

type switchNoteStore struct {
	list   []note.Note
	calls  int
	failAt int
}

func (s *switchNoteStore) LoadAllNotes() ([]note.Note, error) {
	return append([]note.Note(nil), s.list...), nil
}

func (s *switchNoteStore) ReplaceNotes(v []note.Note) error {
	s.calls++
	if s.failAt > 0 && s.calls == s.failAt {
		return errors.New("injected notes write failure")
	}
	s.list = append([]note.Note(nil), v...)
	return nil
}

// ── Fixture ────────────────────────────────────────────────────────────

const journalDoc = "backup-restore-journal.json"

type restoreFixture struct {
	svc   *backup.Service
	conn  *switchConnStore
	sett  *switchSettingsStore
	doc   *fakeDocStore
	snips *switchSnippetStore
	notes *switchNoteStore
}

func newRestoreFixture(budget time.Duration) *restoreFixture {
	f := &restoreFixture{
		conn: &switchConnStore{snap: profile.ConnectionSnapshot{
			Profiles: []profile.SSHProfile{{Base: profile.Base{ID: "old", Type: "ssh", Name: "old"}, Options: profile.StoredSSHProfileOptions{Host: "old.example"}}},
			Groups:   []profile.ProfileGroup{},
		}},
		sett:  &switchSettingsStore{overrides: map[string]any{"tab.placement": "old"}},
		doc:   &fakeDocStore{},
		snips: &switchSnippetStore{list: []snippet.Snippet{{ID: "s-old", Title: "old", Body: "old"}}},
		notes: &switchNoteStore{list: []note.Note{{ID: "n-old", Body: "old"}}},
	}
	f.svc = backup.NewService(f.conn, f.sett, f.doc, f.snips, f.notes)
	f.svc.SetRecoveryBudget(budget)
	return f
}

// backupFrom builds the document the restore under test will apply: a full
// generation different from the fixture's current one in every store.
func backupFrom(t *testing.T) string {
	t.Helper()
	src := newRestoreFixture(time.Minute)
	src.conn.snap = profile.ConnectionSnapshot{
		Profiles: []profile.SSHProfile{{Base: profile.Base{ID: "new", Type: "ssh", Name: "new"}, Options: profile.StoredSSHProfileOptions{Host: "new.example"}}},
		Groups:   []profile.ProfileGroup{},
	}
	src.sett.overrides = map[string]any{"tab.placement": "new"}
	src.snips.list = []snippet.Snippet{{ID: "s-new", Title: "new", Body: "new"}}
	src.notes.list = []note.Note{{ID: "n-new", Body: "new"}}
	created, err := src.svc.Create()
	if err != nil {
		t.Fatalf("create the backup document: %v", err)
	}
	return created.Contents
}

func restoreTo(t *testing.T, f *restoreFixture, contents string) error {
	t.Helper()
	preview, err := f.svc.Preview(contents, backup.RestoreReplace)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	_, err = f.svc.Restore(contents, backup.RestoreReplace, preview.PreviewToken)
	return err
}

// assertOldGeneration is the other end of the interval: every store, not just
// the ones the table row touched.
func assertOldGeneration(t *testing.T, f *restoreFixture) {
	t.Helper()
	if len(f.conn.snap.Profiles) != 1 || f.conn.snap.Profiles[0].ID != "old" {
		t.Fatalf("connections = %+v, want the old generation", f.conn.snap.Profiles)
	}
	if got := f.sett.overrides["tab.placement"]; got != "old" {
		t.Fatalf("settings tab.placement = %v, want old", got)
	}
	if len(f.snips.list) != 1 || f.snips.list[0].ID != "s-old" {
		t.Fatalf("snippets = %+v, want the old generation", f.snips.list)
	}
	if len(f.notes.list) != 1 || f.notes.list[0].ID != "n-old" {
		t.Fatalf("notes = %+v, want the old generation", f.notes.list)
	}
	var journal struct {
		State string `json:"state"`
	}
	raw, ok := f.doc.data[journalDoc]
	if ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &journal); err != nil {
			t.Fatalf("read journal: %v", err)
		}
		if journal.State != "idle" {
			t.Fatalf("journal state = %q after a completed rollback, want idle", journal.State)
		}
	}
}

// ── Every boundary ─────────────────────────────────────────────────────

func TestRestore_FailureAtEveryBoundaryLeavesTheOldGeneration(t *testing.T) {
	contents := backupFrom(t)

	cases := []struct {
		name  string
		arm   func(f *restoreFixture)
		state string
	}{
		{"connections", func(f *restoreFixture) { f.conn.failAt = 1 }, "the first store write"},
		{"settings", func(f *restoreFixture) { f.sett.failAt = 1 }, "the settings document"},
		{"snippets", func(f *restoreFixture) { f.snips.failAt = 1 }, "the snippet library"},
		{"notes", func(f *restoreFixture) { f.notes.failAt = 1 }, "the notes library"},
		{"committed-journal", func(f *restoreFixture) { f.doc.failOnWrite = 2 }, "the committed journal entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestoreFixture(time.Minute)
			tc.arm(f)
			err := restoreTo(t, f, contents)
			if err == nil {
				t.Fatalf("a failure at %s did not fail the restore", tc.state)
			}
			var recovery *backup.RecoveryError
			if errors.As(err, &recovery) {
				t.Fatalf("a transient failure at %s ended in a recovery error: %v", tc.state, err)
			}
			assertOldGeneration(t, f)
		})
	}
}

// A rollback that fails too is the one case the old generation cannot be
// promised, and the error must SAY so rather than return an ordinary failure:
// the caller turns it into a quarantine. The state is mixed at that moment,
// which is exactly why the stores must stop answering.
func TestRestore_ARollbackFailureIsARecoveryError(t *testing.T) {
	f := newRestoreFixture(time.Minute)
	// The apply writes connections, then its settings write fails and starts
	// the rollback; the rollback's own first write — connections again — is
	// the one that fails.
	f.sett.failAt = 1
	f.conn.failAt = 2

	err := restoreTo(t, f, backupFrom(t))
	if err == nil {
		t.Fatal("the restore succeeded despite a failing rollback")
	}
	var recovery *backup.RecoveryError
	if !errors.As(err, &recovery) {
		t.Fatalf("error = %v, want a *backup.RecoveryError", err)
	}
	if recovery.TimedOut {
		t.Fatal("a failed rollback was reported as a timeout")
	}
	if !strings.Contains(err.Error(), "must not be read") {
		t.Fatalf("the recovery error does not say the stores are unreadable: %v", err)
	}
	// The generation is mixed: connections are new, settings old.
	if len(f.conn.snap.Profiles) != 1 || f.conn.snap.Profiles[0].ID != "new" {
		t.Fatalf("connections = %+v, want the new generation at the failure point", f.conn.snap.Profiles)
	}
	if got := f.sett.overrides["tab.placement"]; got != "old" {
		t.Fatalf("settings tab.placement = %v, want old", got)
	}
}

// A rollback that does not finish inside its budget must return a
// RecoveryError with TimedOut set, and it must return it WITHOUT waiting for
// the blocked write: the store set is what protects a reader, not the caller's
// patience. The late write is still allowed to complete — the quarantine, not
// cancellation, is what makes it unobservable.
func TestRestore_AHungRollbackExpiresTheRecoveryBudget(t *testing.T) {
	f := newRestoreFixture(150 * time.Millisecond)
	// The settings write fails and starts the rollback; the rollback's first
	// write — connections again — hangs.
	f.sett.failAt = 1
	f.conn.blockAt = 2
	f.conn.entered = make(chan struct{})
	f.conn.release = make(chan struct{})
	f.conn.left = make(chan struct{})

	contents := backupFrom(t)
	preview, err := f.svc.Preview(contents, backup.RestoreReplace)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}

	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, rerr := f.svc.Restore(contents, backup.RestoreReplace, preview.PreviewToken)
		done <- outcome{err: rerr}
	}()

	waittest.WaitFor(t, "the rollback to reach the blocked write", func() bool {
		select {
		case <-f.conn.entered:
			return true
		default:
			return false
		}
	})

	var got outcome
	waittest.WaitFor(t, "the restore to return when the rollback budget expires", func() bool {
		select {
		case got = <-done:
			return true
		default:
			return false
		}
	})
	var recovery *backup.RecoveryError
	if !errors.As(got.err, &recovery) || !recovery.TimedOut {
		t.Fatalf("error = %v, want a timed-out *backup.RecoveryError", got.err)
	}

	// The late write completes; the caller has already been told.
	close(f.conn.release)
	waittest.WaitFor(t, "the abandoned rollback write to return", func() bool {
		select {
		case <-f.conn.left:
			return true
		default:
			return false
		}
	})
}

// The restart half: a journal left "prepared" by a process that died mid-restore
// is completed before anything serves, and the result is the old generation
// everywhere — never the new connections beside the old settings the crash left.
func TestRecover_CompletesAnInterruptedRestoreAtStartup(t *testing.T) {
	f := newRestoreFixture(time.Minute)

	// The crash: the prepared journal is on disk and the first store was
	// already rewritten, exactly as Restore leaves it when it dies between
	// writes.
	oldSnap := profile.ConnectionSnapshot{
		Profiles: []profile.SSHProfile{{Base: profile.Base{ID: "old", Type: "ssh", Name: "old"}, Options: profile.StoredSSHProfileOptions{Host: "old.example"}}},
		Groups:   []profile.ProfileGroup{},
	}
	oldOverrides := map[string]any{"tab.placement": "old"}
	journal, err := json.Marshal(map[string]any{
		"version": 1, "state": "prepared", "connections": oldSnap, "settings": oldOverrides,
	})
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	f.doc.data = map[string][]byte{journalDoc: journal}
	f.conn.snap = profile.ConnectionSnapshot{
		Profiles: []profile.SSHProfile{{Base: profile.Base{ID: "new", Type: "ssh", Name: "new"}, Options: profile.StoredSSHProfileOptions{Host: "new.example"}}},
		Groups:   []profile.ProfileGroup{},
	}

	// The restart: a fresh service over the same stores, recovering before it
	// answers anything.
	restarted := backup.NewService(f.conn, f.sett, f.doc, f.snips, f.notes)
	if err := restarted.RecoverWithin(context.Background(), 0); err != nil {
		t.Fatalf("startup recovery failed: %v", err)
	}
	assertOldGeneration(t, f)
}

// The journal is the record that makes quarantine safe: while it says
// "prepared", a restart can complete the rollback even though the process that
// wrote it is gone.
func TestRecover_AnUnknownJournalStateIsARefusal(t *testing.T) {
	f := newRestoreFixture(time.Minute)
	f.doc.data = map[string][]byte{journalDoc: []byte(fmt.Sprintf(`{"version":1,"state":%q}`, "half-applied"))}
	err := f.svc.RecoverWithin(context.Background(), 0)
	if !errors.Is(err, backup.ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
}
