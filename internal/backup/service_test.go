package backup_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/snippet"
)

type fakeDocStore struct {
	data        map[string][]byte
	writes      int
	failOnWrite int // 1-based: fail when the next write would be the Nth
}

func (f *fakeDocStore) Read(name string, into any) (bool, error) {
	b, ok := f.data[name]
	if !ok || b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, into)
}

func (f *fakeDocStore) Write(name string, doc any) error {
	f.writes++
	if f.failOnWrite > 0 && f.writes == f.failOnWrite {
		return errors.New("injected journal write failure")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if f.data == nil {
		f.data = make(map[string][]byte)
	}
	f.data[name] = b
	return nil
}

func (f *fakeDocStore) Delete(name string) error {
	if f.data != nil {
		delete(f.data, name)
	}
	return nil
}

type fakeConnStore struct {
	snap profile.ConnectionSnapshot
}

func (f *fakeConnStore) LoadConnectionSnapshot() (profile.ConnectionSnapshot, error) {
	return f.snap, nil
}

func (f *fakeConnStore) ReplaceConnectionSnapshot(s profile.ConnectionSnapshot) error {
	f.snap = s
	return nil
}

type fakeSettingsStore struct {
	overrides   map[string]any
	publishLog  []bool
	validateErr error
}

func (f *fakeSettingsStore) NonSecretOverrides() map[string]any {
	out := make(map[string]any, len(f.overrides))
	for k, v := range f.overrides {
		out[k] = v
	}
	return out
}

func (f *fakeSettingsStore) ReplaceNonSecretOverrides(v map[string]any) (settings.PendingNotification, error) {
	f.overrides = make(map[string]any, len(v))
	for k, val := range v {
		f.overrides[k] = val
	}
	return settings.PendingNotification{}, nil
}

func (f *fakeSettingsStore) Publish(settings.PendingNotification) {
	f.publishLog = append(f.publishLog, true)
}

func (f *fakeSettingsStore) ValidateSetting(key string, value any) error {
	return f.validateErr
}

// fakeSnippetStore is an in-memory snippet.Store for the backup tests.
type fakeSnippetStore struct {
	list []snippet.Snippet
}

func (f *fakeSnippetStore) LoadAll() ([]snippet.Snippet, error) {
	return append([]snippet.Snippet(nil), f.list...), nil
}

func (f *fakeSnippetStore) SaveAll(s []snippet.Snippet) error {
	f.list = append([]snippet.Snippet(nil), s...)
	return nil
}

// fakeNoteStore is an in-memory notes library for the backup tests. The
// error switches are what the "every external call has a failing test" rule
// needs: a backup that quietly omitted the notes it could not read would be
// a backup somebody trusted.
type fakeNoteStore struct {
	list     []note.Note
	loadErr  error
	writeErr error
}

func (f *fakeNoteStore) LoadAllNotes() ([]note.Note, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return append([]note.Note(nil), f.list...), nil
}

func (f *fakeNoteStore) ReplaceNotes(notes []note.Note) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.list = append([]note.Note(nil), notes...)
	return nil
}

func newFakeServiceWithNotes() (*backup.Service, *fakeSnippetStore, *fakeNoteStore) {
	conn := &fakeConnStore{
		snap: profile.ConnectionSnapshot{
			Profiles: []profile.SSHProfile{},
			Groups:   []profile.ProfileGroup{},
		},
	}
	sett := &fakeSettingsStore{overrides: map[string]any{}}
	doc := &fakeDocStore{}
	snips := &fakeSnippetStore{}
	notes := &fakeNoteStore{}
	return backup.NewService(conn, sett, doc, snips, notes), snips, notes
}

func newFakeService() (*backup.Service, *fakeConnStore, *fakeSettingsStore, *fakeDocStore, *fakeSnippetStore) {
	conn := &fakeConnStore{
		snap: profile.ConnectionSnapshot{
			Profiles: []profile.SSHProfile{},
			Groups:   []profile.ProfileGroup{},
		},
	}
	sett := &fakeSettingsStore{overrides: map[string]any{}}
	doc := &fakeDocStore{}
	snips := &fakeSnippetStore{}
	svc := backup.NewService(conn, sett, doc, snips, nil)
	return svc, conn, sett, doc, snips
}

// ── Helpers ──────────────────────────────────────────────────────────────

func mustCreate(t *testing.T, svc *backup.Service) *backup.CreateResult {
	t.Helper()
	r, err := svc.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return r
}

func mustPreview(t *testing.T, svc *backup.Service, contents string, strategy backup.RestoreStrategy) *backup.RestorePreview {
	t.Helper()
	r, err := svc.Preview(contents, strategy)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	return r
}

func mustRestore(t *testing.T, svc *backup.Service, contents string, strategy backup.RestoreStrategy, token string) *backup.RestoreResult {
	t.Helper()
	r, err := svc.Restore(contents, strategy, token)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return r
}

// ── Tests ────────────────────────────────────────────────────────────────

func TestCreate_RoundTrip(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{
			Base:    profile.Base{ID: "ssh:custom:p1:0001", Type: "ssh", Name: "p1"},
			Options: profile.StoredSSHProfileOptions{Host: "h.example.com", Port: new(22), User: new("u")},
		},
	}
	conn.snap.Groups = []profile.ProfileGroup{
		{ID: "g1", Name: "G1"},
	}

	r := mustCreate(t, svc)

	// Validate JSON structure.
	var doc backup.Document
	if err := json.Unmarshal([]byte(r.Contents), &doc); err != nil {
		t.Fatalf("unmarshal created backup: %v", err)
	}
	if doc.Format != backup.Format {
		t.Errorf("format = %q, want %q", doc.Format, backup.Format)
	}
	if doc.Version != backup.Version {
		t.Errorf("version = %d, want %d", doc.Version, backup.Version)
	}
	if doc.CreatedAt.IsZero() {
		t.Error("createdAt is zero")
	}
	if len(doc.Connections.Profiles) != 1 {
		t.Errorf("profiles = %d, want 1", len(doc.Connections.Profiles))
	}
	if len(doc.Connections.Groups) != 1 {
		t.Errorf("groups = %d, want 1", len(doc.Connections.Groups))
	}

	// Preview round-trip.
	preview := mustPreview(t, svc, r.Contents, backup.RestoreMerge)
	if preview.PreviewToken == "" {
		t.Error("preview token is empty")
	}
	if preview.Strategy != backup.RestoreMerge {
		t.Errorf("strategy = %q", preview.Strategy)
	}
}

func TestCreate_NoCredentialKeys(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{
			Base:    profile.Base{ID: "ssh:custom:p1:0001", Type: "ssh", Name: "p1"},
			Options: profile.StoredSSHProfileOptions{Host: "h", PasswordSecret: "cred:xyz:abc"},
		},
	}
	conn.snap.Groups = []profile.ProfileGroup{
		{
			ID:   "g1",
			Name: "G1",
			Defaults: &profile.ProfileDefaults{
				SparseSSHOptions: profile.SparseSSHOptions{
					PasswordSecret: new("cred:group:def"),
					Port:           new(2222),
				},
			},
		},
	}

	r := mustCreate(t, svc)

	// The JSON must not contain credentialId, secretId, passphraseSecretId anywhere.
	for _, forbidden := range []string{"credentialId", "passwordSecret", "keySecret", "keyPassphraseSecret"} {
		if strings.Contains(r.Contents, forbidden) {
			t.Errorf("backup JSON contains forbidden key %q", forbidden)
		}
	}
	// But requiresCredential should be present.
	if !strings.Contains(r.Contents, "requiresCredential") {
		t.Error("backup should contain requiresCredential")
	}
}

func TestCreate_SettingsOverridesOnly(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()

	sett.overrides["tab.placement"] = "vertical"
	sett.overrides["clipboard.osc52Suppressed"] = true

	r := mustCreate(t, svc)

	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	if v, ok := doc.Settings.Overrides["tab.placement"]; !ok || v != "vertical" {
		t.Errorf("settings overrides = %v", doc.Settings.Overrides)
	}
	if len(doc.Settings.Overrides) != 2 {
		t.Errorf("overrides count = %d, want 2", len(doc.Settings.Overrides))
	}
}

func TestParse_RejectsInvalidFormat(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	_, err := svc.Preview(`{"format":"bad","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}`, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for bad format")
	}
}

func TestParse_RejectsInvalidVersion(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	_, err := svc.Preview(`{"format":"nocx-backup","version":99,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}`, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for bad version")
	}
}

func TestParse_RejectsTrailingJSON(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	_, err := svc.Preview(`{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}{}`, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for trailing JSON")
	}
}

func TestParse_RejectsDuplicateProfileID(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h"}},{"id":"p1","type":"ssh","name":"b","options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for duplicate profile id")
	}
}

func TestParse_RejectsEmptyProfileName(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"","options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestParse_RejectsObsoleteNeedsReviewField(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"host","needsReview":true,"options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil || !strings.Contains(err.Error(), "needsReview") {
		t.Fatalf("Preview error = %v, want obsolete needsReview field refusal", err)
	}
}

func TestParse_RejectsNonSSHType(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"telnet","name":"a","options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for non-ssh type")
	}
}

func TestMerge_PreservesExtraConnections(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	// Existing: two profiles.
	conn.snap.Profiles = []profile.SSHProfile{
		{Base: profile.Base{ID: "p1", Type: "ssh", Name: "keep"}, Options: profile.StoredSSHProfileOptions{Host: "h1"}},
		{Base: profile.Base{ID: "p2", Type: "ssh", Name: "extra"}, Options: profile.StoredSSHProfileOptions{Host: "h2"}},
	}

	// Backup: only p1 (updated name), plus p3 (new).
	r := mustCreate(t, svc)
	// Modify created backup to simulate what a user would import.
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Connections.Profiles = []backup.BackupProfile{
		{ID: "p1", Type: "ssh", Name: "updated", Options: backup.BackupSSHOptions{Host: "h1"}},
		{ID: "p3", Type: "ssh", Name: "new", Options: backup.BackupSSHOptions{Host: "h3"}},
	}
	doc.Connections.Groups = []backup.BackupGroup{}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	if preview.Connections.Updated != 1 {
		t.Errorf("updated = %d, want 1", preview.Connections.Updated)
	}
	if preview.Connections.Added != 1 {
		t.Errorf("added = %d, want 1", preview.Connections.Added)
	}
	if preview.Connections.Removed != 0 {
		t.Errorf("removed = %d, want 0 (merge never removes)", preview.Connections.Removed)
	}

	mustRestore(t, svc, contents, backup.RestoreMerge, preview.PreviewToken)

	snap, _ := conn.LoadConnectionSnapshot()
	if len(snap.Profiles) != 3 {
		t.Fatalf("after merge: %d profiles, want 3", len(snap.Profiles))
	}
	names := map[string]string{}
	for _, p := range snap.Profiles {
		names[p.ID] = p.Name
	}
	if names["p1"] != "updated" {
		t.Errorf("p1 name = %q, want 'updated'", names["p1"])
	}
	if names["p2"] != "extra" {
		t.Errorf("p2 name = %q, want 'extra' (preserved)", names["p2"])
	}
	if names["p3"] != "new" {
		t.Errorf("p3 name = %q, want 'new'", names["p3"])
	}
}

func TestReplace_RemovesExtraConnections(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{Base: profile.Base{ID: "p1", Type: "ssh", Name: "keep"}, Options: profile.StoredSSHProfileOptions{Host: "h1"}},
		{Base: profile.Base{ID: "p2", Type: "ssh", Name: "extra"}, Options: profile.StoredSSHProfileOptions{Host: "h2"}},
	}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	// Only p1 in backup.
	doc.Connections.Profiles = []backup.BackupProfile{
		{ID: "p1", Type: "ssh", Name: "updated", Options: backup.BackupSSHOptions{Host: "h1"}},
	}
	doc.Connections.Groups = []backup.BackupGroup{}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreReplace)
	if preview.Connections.Removed != 1 {
		t.Errorf("removed = %d, want 1", preview.Connections.Removed)
	}

	mustRestore(t, svc, contents, backup.RestoreReplace, preview.PreviewToken)

	snap, _ := conn.LoadConnectionSnapshot()
	if len(snap.Profiles) != 1 {
		t.Fatalf("after replace: %d profiles, want 1", len(snap.Profiles))
	}
	if snap.Profiles[0].Name != "updated" {
		t.Errorf("name = %q", snap.Profiles[0].Name)
	}
}

func TestMerge_PreservesCredentialBinding_UnchangedHost(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{
			Base:    profile.Base{ID: "p1", Type: "ssh", Name: "p1"},
			Options: profile.StoredSSHProfileOptions{Host: "h", Port: new(22), PasswordSecret: "cred:keep:abc"},
		},
	}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Connections.Profiles = []backup.BackupProfile{
		{ID: "p1", Type: "ssh", Name: "p1-renamed", Options: backup.BackupSSHOptions{Host: "h", Port: 22}},
	}
	doc.Connections.Groups = []backup.BackupGroup{}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	mustRestore(t, svc, contents, backup.RestoreMerge, preview.PreviewToken)

	snap, _ := conn.LoadConnectionSnapshot()
	if snap.Profiles[0].Options.PasswordSecret != "cred:keep:abc" {
		t.Errorf("passwordSecret = %q, want 'cred:keep:abc' (preserved)", snap.Profiles[0].Options.PasswordSecret)
	}
}

func TestMerge_ClearsCredentialBinding_ChangedHost(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{
			Base: profile.Base{ID: "p1", Type: "ssh", Name: "p1"},
			Options: profile.StoredSSHProfileOptions{
				Host:                "old",
				Port:                new(22),
				PasswordSecret:      "cred:drop:abc",
				KeySecret:           "cred:key:abc",
				KeyPassphraseSecret: "cred:pass:abc",
			},
		},
	}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Connections.Profiles = []backup.BackupProfile{
		{ID: "p1", Type: "ssh", Name: "p1", Options: backup.BackupSSHOptions{Host: "new", Port: 22}},
	}
	doc.Connections.Groups = []backup.BackupGroup{}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	mustRestore(t, svc, contents, backup.RestoreMerge, preview.PreviewToken)

	snap, _ := conn.LoadConnectionSnapshot()
	if snap.Profiles[0].Options.PasswordSecret != "" {
		t.Errorf("passwordSecret = %q, want empty (cleared on host change)", snap.Profiles[0].Options.PasswordSecret)
	}
	if snap.Profiles[0].Options.KeySecret != "" {
		t.Errorf("keySecret = %q, want empty (cleared on host change)", snap.Profiles[0].Options.KeySecret)
	}
	if snap.Profiles[0].Options.KeyPassphraseSecret != "" {
		t.Errorf("keyPassphraseSecret = %q, want empty (cleared on host change)", snap.Profiles[0].Options.KeyPassphraseSecret)
	}
}

func TestPreviewToken_StaleAfterMutation(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{Base: profile.Base{ID: "p1", Type: "ssh", Name: "p1"}, Options: profile.StoredSSHProfileOptions{Host: "h"}},
	}

	r := mustCreate(t, svc)
	preview := mustPreview(t, svc, r.Contents, backup.RestoreMerge)

	// Mutate state after preview.
	conn.snap.Profiles = append(conn.snap.Profiles, profile.SSHProfile{
		Base: profile.Base{ID: "p2", Type: "ssh", Name: "p2"}, Options: profile.StoredSSHProfileOptions{Host: "h2"},
	})

	_, err := svc.Restore(r.Contents, backup.RestoreMerge, preview.PreviewToken)
	if err == nil {
		t.Fatal("expected stale preview token error")
	}
}

func TestRestore_MergeSettings(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()

	sett.overrides["clipboard.osc52Suppressed"] = true

	r := mustCreate(t, svc)
	preview := mustPreview(t, svc, r.Contents, backup.RestoreMerge)
	mustRestore(t, svc, r.Contents, backup.RestoreMerge, preview.PreviewToken)

	if sett.overrides["clipboard.osc52Suppressed"] != true {
		t.Error("setting override lost after merge restore")
	}
}

func TestRestore_ReplaceSettings(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()

	sett.overrides["clipboard.osc52Suppressed"] = true
	sett.overrides["tab.placement"] = "vertical"

	r := mustCreate(t, svc)

	// Modify backup to retain one existing override and add one that is new locally.
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Settings.Overrides = map[string]any{
		"clipboard.osc52Suppressed": false,
		"backup.newOverride":        true,
	}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreReplace)
	if preview.Settings.Changed != 2 {
		t.Errorf("settings changed = %d, want 2", preview.Settings.Changed)
	}
	if preview.Settings.Reset != 1 {
		t.Errorf("settings reset = %d, want 1 (tab.placement should reset)", preview.Settings.Reset)
	}

	result := mustRestore(t, svc, contents, backup.RestoreReplace, preview.PreviewToken)
	if result.SettingsChanged != 2 {
		t.Errorf("result settings changed = %d, want 2", result.SettingsChanged)
	}
	if sett.overrides["clipboard.osc52Suppressed"] != false {
		t.Errorf("clipboard = %v, want false", sett.overrides["clipboard.osc52Suppressed"])
	}
	if sett.overrides["backup.newOverride"] != true {
		t.Errorf("backup.newOverride = %v, want true", sett.overrides["backup.newOverride"])
	}
	if _, ok := sett.overrides["tab.placement"]; ok {
		t.Error("tab.placement should be reset (absent after replace)")
	}
}

func TestRecover_Idle(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	if err := svc.Recover(); err != nil {
		t.Fatalf("Recover idle: %v", err)
	}
}

func TestRecover_Prepared_RollsBack(t *testing.T) {
	svc, conn, sett, doc, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{Base: profile.Base{ID: "p1", Type: "ssh", Name: "original"}, Options: profile.StoredSSHProfileOptions{Host: "h"}},
	}
	sett.overrides["clipboard.osc52Suppressed"] = true

	// Simulate a prepared journal.
	beforeConn := conn.snap
	beforeSett := sett.NonSecretOverrides()
	writeTestJournal(t, doc, "prepared", &beforeConn, &beforeSett)

	// Mutate state (simulating partial write).
	conn.snap.Profiles = []profile.SSHProfile{
		{Base: profile.Base{ID: "p2", Type: "ssh", Name: "corrupted"}, Options: profile.StoredSSHProfileOptions{Host: "x"}},
	}
	sett.overrides["clipboard.osc52Suppressed"] = false

	if err := svc.Recover(); err != nil {
		t.Fatalf("Recover prepared: %v", err)
	}

	snap, _ := conn.LoadConnectionSnapshot()
	if snap.Profiles[0].Name != "original" {
		t.Errorf("name = %q, want 'original' (rolled back)", snap.Profiles[0].Name)
	}
	if sett.overrides["clipboard.osc52Suppressed"] != true {
		t.Error("setting should be rolled back")
	}

	// Recovery must publish the rolled-back notification so live observers see the change.
	if len(sett.publishLog) < 1 {
		t.Error("recovery should publish the rolled-back notification")
	}

	// Journal should be cleaned.
	if js, _ := readTestJournal(doc); js.state != "idle" {
		t.Errorf("journal state = %q, want idle", js.state)
	}
}

func TestRecover_Committed_KeepsState(t *testing.T) {
	svc, conn, sett, doc, _ := newFakeService()

	conn.snap.Profiles = []profile.SSHProfile{
		{Base: profile.Base{ID: "p1", Type: "ssh", Name: "committed-state"}, Options: profile.StoredSSHProfileOptions{Host: "h"}},
	}
	sett.overrides["clipboard.osc52Suppressed"] = true

	beforeConn := conn.snap
	beforeSett := sett.NonSecretOverrides()
	writeTestJournal(t, doc, "committed", &beforeConn, &beforeSett)

	if err := svc.Recover(); err != nil {
		t.Fatalf("Recover committed: %v", err)
	}

	snap, _ := conn.LoadConnectionSnapshot()
	if snap.Profiles[0].Name != "committed-state" {
		t.Errorf("name = %q, want 'committed-state' (kept)", snap.Profiles[0].Name)
	}

	js, _ := readTestJournal(doc)
	if js.state != "idle" {
		t.Errorf("journal state = %q, want idle", js.state)
	}
}

func TestSizeLimit(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	big := strings.Repeat("x", backup.MaxDocumentBytes+1)
	_, err := svc.Preview(big, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for oversized document")
	}
}

// ── Journal helpers ──────────────────────────────────────────────────────

func writeTestJournal(t *testing.T, doc *fakeDocStore, state string, conn *profile.ConnectionSnapshot, sett *map[string]any) {
	t.Helper()
	// Use the public journal interface via a real write.
	j := map[string]any{
		"version": 1,
		"state":   state,
	}
	if state == "prepared" || state == "committed" {
		j["connections"] = *conn
		j["settings"] = *sett
	}
	b, _ := json.Marshal(j)
	// Write as raw JSON to avoid unmarshal/remarshal roundtrip.
	var raw json.RawMessage = b
	if err := doc.Write("backup-restore-journal.json", raw); err != nil {
		t.Fatalf("writeTestJournal: %v", err)
	}
}

type testJournal struct {
	state       string
	connections *profile.ConnectionSnapshot
	settings    *map[string]any
}

func readTestJournal(doc *fakeDocStore) (testJournal, error) {
	var j struct {
		Version     int                         `json:"version"`
		State       string                      `json:"state"`
		Connections *profile.ConnectionSnapshot `json:"connections,omitempty"`
		Settings    *map[string]any             `json:"settings,omitempty"`
	}
	found, err := doc.Read("backup-restore-journal.json", &j)
	if err != nil {
		return testJournal{}, err
	}
	if !found {
		return testJournal{state: "idle"}, nil
	}
	return testJournal{
		state:       j.State,
		connections: j.Connections,
		settings:    j.Settings,
	}, nil
}

func TestRestore_GroupDefaultsRoundTrip(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()

	conn.snap.Groups = []profile.ProfileGroup{
		{
			ID:   "g1",
			Name: "G1",
			Defaults: &profile.ProfileDefaults{
				SparseSSHOptions: profile.SparseSSHOptions{
					Port: new(2222),
					User: new("alice"),
				},
			},
		},
	}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	if doc.Connections.Groups[0].Defaults == nil || doc.Connections.Groups[0].Defaults.SSH == nil {
		t.Fatal("group defaults must be carried in the backup")
	}
	if doc.Connections.Groups[0].Defaults.SSH.Options.Port != 2222 {
		t.Errorf("backup group default port = %d, want 2222", doc.Connections.Groups[0].Defaults.SSH.Options.Port)
	}

	// Restore (replace) into an empty store: defaults must come back.
	conn.snap.Groups = []profile.ProfileGroup{}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"
	preview := mustPreview(t, svc, contents, backup.RestoreReplace)
	mustRestore(t, svc, contents, backup.RestoreReplace, preview.PreviewToken)

	snap, _ := conn.LoadConnectionSnapshot()
	if len(snap.Groups) != 1 {
		t.Fatalf("groups after restore = %d, want 1", len(snap.Groups))
	}
	g := snap.Groups[0]
	if g.Defaults == nil || g.Defaults.Port == nil || *g.Defaults.Port != 2222 {
		t.Errorf("restored group defaults port = %+v, want 2222", g.Defaults)
	}
	if g.Defaults.User == nil || *g.Defaults.User != "alice" {
		t.Errorf("restored group defaults user = %+v, want alice", g.Defaults)
	}
}

// ── Duplicate key detection ─────────────────────────────────────────────

func TestParse_RejectsDuplicateTopLevelKey(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for duplicate top-level key")
	}
}

func TestParse_RejectsDuplicateNestedKey(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h","host":"h2"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for duplicate nested key")
	}
}

func TestParse_AcceptsUnicodeEscapedKeys(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	// "\u0061" = "a", "\u0062" = "b" — unique decoded keys in overrides map.
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{"\u0061":"val1","\u0062":"val2"}},"connections":{"profiles":[],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err != nil {
		t.Fatalf("expected no error for valid unicode escapes, got: %v", err)
	}
}

func TestParse_RejectsDecodedEquivalentDuplicate(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	// "\u006Bey" decodes to "key" — same as the literal "key" in overrides.
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{"\u006Bey":"val1","key":"val2"}},"connections":{"profiles":[],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for decoded-equivalent duplicate keys")
	}
}

func TestParse_AcceptsDecimalNumberWithoutDuplicateKeys(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{"test.number":1.0}},"connections":{"profiles":[],"groups":[]}}`
	if _, err := svc.Preview(contents, backup.RestoreMerge); err != nil {
		t.Fatalf("valid decimal number rejected: %v", err)
	}
}

func TestParse_RejectsSettingValidationError(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()
	sett.validateErr = errors.New("expected string")
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{"ui.theme":{"mode":"dark"}}},"connections":{"profiles":[],"groups":[]}}`
	if _, err := svc.Preview(contents, backup.RestoreMerge); !errors.Is(err, backup.ErrInvalidDocument) {
		t.Fatalf("Preview error = %v, want ErrInvalidDocument", err)
	}
}

// ── valuesEqual safety ──────────────────────────────────────────────────

func TestValuesEqual_NestedMaps(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()
	sett.overrides["test.key"] = map[string]any{"nested": "value"}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Settings.Overrides["test.key"] = map[string]any{"nested": "value"}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	// Same nested map value — should not count as changed.
	if preview.Settings.Changed != 0 {
		t.Errorf("settings changed = %d, want 0 (same nested map)", preview.Settings.Changed)
	}
}

func TestValuesEqual_NestedArrays(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()
	sett.overrides["test.key"] = []any{"a", "b"}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Settings.Overrides["test.key"] = []any{"a", "b"}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	if preview.Settings.Changed != 0 {
		t.Errorf("settings changed = %d, want 0 (same nested array)", preview.Settings.Changed)
	}
}

func TestPreview_SettingsValuesEqualDoesNotPanic(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()
	// Store complex types that would panic with == comparison.
	sett.overrides["test.key"] = map[string]any{"deep": map[string]any{"v": float64(1)}}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Settings.Overrides["test.key"] = map[string]any{"deep": map[string]any{"v": float64(1)}}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	// Must not panic.
	_ = mustPreview(t, svc, contents, backup.RestoreMerge)
}

func TestRestore_SettingsValuesEqualDoesNotPanic(t *testing.T) {
	svc, _, sett, _, _ := newFakeService()
	sett.overrides["test.key"] = []any{map[string]any{"id": float64(1)}, map[string]any{"id": float64(2)}}

	r := mustCreate(t, svc)
	var doc backup.Document
	_ = json.Unmarshal([]byte(r.Contents), &doc)
	doc.Settings.Overrides["test.key"] = []any{map[string]any{"id": float64(1)}, map[string]any{"id": float64(2)}}
	b, _ := json.Marshal(doc)
	contents := string(b) + "\n"

	preview := mustPreview(t, svc, contents, backup.RestoreReplace)
	// Must not panic.
	mustRestore(t, svc, contents, backup.RestoreReplace, preview.PreviewToken)
}

func TestParse_RejectsMalformedForwardDirection(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h","forwards":[{"direction":"bogus","destination":"localhost:8080"}]}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for malformed forward direction")
	}
	if !strings.Contains(err.Error(), "forward") && !strings.Contains(err.Error(), "direction") {
		t.Errorf("error should mention forward/direction, got: %v", err)
	}
}

func TestParse_RejectsInvalidForwardDestination(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h","forwards":[{"direction":"local","destination":"missing-port"}]}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for invalid forward destination")
	}
}

func TestParse_RejectsInvalidForwardPort(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h","forwards":[{"direction":"local","bindPort":99999,"destination":"localhost:8080"}]}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for invalid forward port")
	}
}

func TestParse_RejectsOrphanedGroupReference(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h"},"group":"nonexistent"}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for orphaned group reference")
	}
	if !strings.Contains(err.Error(), "unknown group") {
		t.Errorf("error should mention unknown group, got: %v", err)
	}
}

func TestParse_RejectsCyclicGroupTree(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[{"id":"g1","name":"G1","parentGroupId":"g2"},{"id":"g2","name":"G2","parentGroupId":"g1"}]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for cyclic group tree")
	}
	if !strings.Contains(err.Error(), "group tree") && !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention group tree or cycle, got: %v", err)
	}
}

func TestParse_RejectsGroupForwardDefaultsInvalid(t *testing.T) {
	svc, _, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[{"id":"g1","name":"G1","defaults":{"ssh":{"options":{"forwards":[{"direction":"bogus","destination":"host:80"}]}}}}]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for invalid group forward defaults")
	}
}

func TestRestoreMergeNormalizesBehaviorOnSessionEnd(t *testing.T) {
	svc, conn, _, _, _ := newFakeService()
	conn.snap.Profiles = []profile.SSHProfile{{
		Base: profile.Base{
			ID:                   "p1",
			Type:                 "ssh",
			Name:                 "current",
			BehaviorOnSessionEnd: profile.BehaviorKeep,
		},
		Options: profile.StoredSSHProfileOptions{
			Host:                 "host",
			BehaviorOnSessionEnd: profile.Ptr(profile.BehaviorKeep),
		},
	}}

	created := mustCreate(t, svc)
	var doc backup.Document
	if err := json.Unmarshal([]byte(created.Contents), &doc); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	doc.Connections.Profiles[0].BehaviorOnSessionEnd = profile.BehaviorClose
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode backup: %v", err)
	}
	contents := string(raw)
	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	mustRestore(t, svc, contents, backup.RestoreMerge, preview.PreviewToken)

	got := conn.snap.Profiles[0]
	if got.BehaviorOnSessionEnd != profile.BehaviorClose {
		t.Errorf("base behavior = %q, want %q", got.BehaviorOnSessionEnd, profile.BehaviorClose)
	}
	if got.Options.BehaviorOnSessionEnd == nil || *got.Options.BehaviorOnSessionEnd != profile.BehaviorClose {
		t.Errorf("options behavior = %v, want %q", got.Options.BehaviorOnSessionEnd, profile.BehaviorClose)
	}
}

// ── Snippets section ─────────────────────────────────────────────────────

// legacyBackupContents is a valid backup document written before the
// snippets section existed: no "snippets" key at all.
func legacyBackupContents(t *testing.T) string {
	t.Helper()
	doc := backup.Document{
		Format:    backup.Format,
		Version:   backup.Version,
		CreatedAt: time.Now().UTC(),
		Settings:  backup.SettingsSection{Overrides: map[string]any{}},
		Connections: backup.ConnectionsSection{
			Profiles: []backup.BackupProfile{},
			Groups:   []backup.BackupGroup{},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal legacy document: %v", err)
	}
	return string(b)
}

// A backup that silently omits a section is worse than no backup: the user
// believes they have one (spec §5.4). The library must survive create then
// wipe then restore under REPLACE with order intact.
func TestCreate_RoundTripsSnippetsUnderReplace(t *testing.T) {
	svc, _, _, _, snips := newFakeService()
	snips.list = []snippet.Snippet{
		{ID: "b", Title: "second", Body: "two"},
		{ID: "a", Title: "first", Body: "one"},
	}

	r := mustCreate(t, svc)
	var doc backup.Document
	if err := json.Unmarshal([]byte(r.Contents), &doc); err != nil {
		t.Fatalf("unmarshal created backup: %v", err)
	}
	if len(doc.Snippets) != 2 || doc.Snippets[0].ID != "b" || doc.Snippets[1].ID != "a" {
		t.Fatalf("backup snippets wrong or out of order: %+v", doc.Snippets)
	}
	if r.Summary.Snippets != 2 {
		t.Fatalf("summary snippets = %d, want 2", r.Summary.Snippets)
	}

	// The machine is wiped: the library on disk is empty now.
	snips.list = nil

	preview := mustPreview(t, svc, r.Contents, backup.RestoreReplace)
	if preview.Snippets.Included != 2 {
		t.Fatalf("preview snippets included = %d, want 2", preview.Snippets.Included)
	}
	mustRestore(t, svc, r.Contents, backup.RestoreReplace, preview.PreviewToken)

	if len(snips.list) != 2 || snips.list[0].ID != "b" || snips.list[1].ID != "a" {
		t.Fatalf("restore lost the library or its order: %+v", snips.list)
	}
}

// Merge must not erase what the backup does not mention: an unconditional
// SaveAll would turn "the backup has no snippets" into "the library is now
// empty".
func TestRestore_MergeWithNoSnippetsSectionLeavesTheLibraryAlone(t *testing.T) {
	svc, _, _, _, snips := newFakeService()
	snips.list = []snippet.Snippet{{ID: "mine", Title: "mine", Body: "keep me"}}

	contents := legacyBackupContents(t)

	preview := mustPreview(t, svc, contents, backup.RestoreMerge)
	mustRestore(t, svc, contents, backup.RestoreMerge, preview.PreviewToken)

	if len(snips.list) != 1 || snips.list[0].ID != "mine" {
		t.Fatalf("merge without a snippets section changed the library: %+v", snips.list)
	}
}

// A document written before this section existed is still a valid backup.
func TestRestore_LegacyDocumentWithoutSnippetsSucceeds(t *testing.T) {
	svc, _, _, _, snips := newFakeService()
	snips.list = []snippet.Snippet{{ID: "mine", Title: "mine", Body: "keep me"}}

	contents := legacyBackupContents(t)

	preview := mustPreview(t, svc, contents, backup.RestoreReplace)
	mustRestore(t, svc, contents, backup.RestoreReplace, preview.PreviewToken)

	if len(snips.list) != 1 || snips.list[0].ID != "mine" {
		t.Fatalf("replace without a snippets section changed the library: %+v", snips.list)
	}
}

// The journal is an interval, not a moment: a failure after the snippet
// write must not leave snippets restored while connections rolled back —
// that would look like a successful restore of everything else.
func TestRestore_SnippetWriteIsRolledBackWithEverythingElse(t *testing.T) {
	svc, conn, sett, doc, snips := newFakeService()
	snips.list = []snippet.Snippet{{ID: "old", Title: "before", Body: "before"}}
	conn.snap.Profiles = []profile.SSHProfile{{
		Base:    profile.Base{ID: "ssh:custom:p1:0001", Type: "ssh", Name: "p1"},
		Options: profile.StoredSSHProfileOptions{Host: "h.example.com"},
	}}
	sett.overrides = map[string]any{"theme": "dark"}

	r := mustCreate(t, svc)
	preview := mustPreview(t, svc, r.Contents, backup.RestoreReplace)

	// The snippet write happens inside the prepared/committed interval. Make
	// the COMMITTED journal write fail — the step after snippets were
	// written — so Recover must take everything back, snippets included.
	doc.failOnWrite = 2
	if _, err := svc.Restore(r.Contents, backup.RestoreReplace, preview.PreviewToken); err == nil {
		t.Fatal("want a restore error")
	}
	doc.failOnWrite = 0

	if len(snips.list) != 1 || snips.list[0].ID != "old" {
		t.Fatalf("snippets not rolled back with everything else: %+v", snips.list)
	}
	if len(conn.snap.Profiles) != 1 || conn.snap.Profiles[0].ID != "ssh:custom:p1:0001" {
		t.Fatalf("connections not rolled back: %+v", conn.snap.Profiles)
	}
	if sett.overrides["theme"] != "dark" {
		t.Fatalf("settings not rolled back: %+v", sett.overrides)
	}
}

// backupWithSnippets is a valid backup document with a snippets section.
func backupWithSnippets(t *testing.T) string {
	t.Helper()
	doc := backup.Document{
		Format:    backup.Format,
		Version:   backup.Version,
		CreatedAt: time.Now().UTC(),
		Settings:  backup.SettingsSection{Overrides: map[string]any{}},
		Connections: backup.ConnectionsSection{
			Profiles: []backup.BackupProfile{},
			Groups:   []backup.BackupGroup{},
		},
		Snippets: []backup.BackupSnippet{{ID: "a", Title: "t", Body: "b"}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return string(b)
}

// A service wired without the snippet store (the transport's test servers
// are) must refuse a backup that carries snippets rather than panic: the
// document mentions a section this build cannot apply, and applying
// everything else would half-restore it.
func TestRestore_WithoutSnippetStoreRefusesASnippetsBackup(t *testing.T) {
	conn := &fakeConnStore{
		snap: profile.ConnectionSnapshot{
			Profiles: []profile.SSHProfile{},
			Groups:   []profile.ProfileGroup{},
		},
	}
	sett := &fakeSettingsStore{overrides: map[string]any{}}
	doc := &fakeDocStore{}
	svc := backup.NewService(conn, sett, doc, nil, nil)

	contents := backupWithSnippets(t)

	preview := mustPreview(t, svc, contents, backup.RestoreReplace)
	if _, err := svc.Restore(contents, backup.RestoreReplace, preview.PreviewToken); err == nil {
		t.Fatal("want an error: the backup carries snippets this service cannot apply")
	}
}

// ── Notes (nocx-z56hq.6) ─────────────────────────────────────────────────

func TestCreate_RoundTripsNotesUnderReplace(t *testing.T) {
	svc, _, notes := newFakeServiceWithNotes()
	notes.list = []note.Note{
		{ID: "n1", Body: "# Deploy\n\nkubectl rollout\n", CreatedAt: 10, UpdatedAt: 11},
		{ID: "n2", Body: "второй\n", CreatedAt: 20, UpdatedAt: 21},
	}

	r := mustCreate(t, svc)
	var doc backup.Document
	if err := json.Unmarshal([]byte(r.Contents), &doc); err != nil {
		t.Fatalf("unmarshal created backup: %v", err)
	}
	if len(doc.Notes) != 2 {
		t.Fatalf("backup carries %d notes, want 2", len(doc.Notes))
	}
	if r.Summary.Notes != 2 {
		t.Fatalf("summary notes = %d, want 2", r.Summary.Notes)
	}

	// The machine is wiped.
	notes.list = nil

	preview := mustPreview(t, svc, r.Contents, backup.RestoreReplace)
	if preview.Notes.Included != 2 {
		t.Fatalf("preview notes included = %d, want 2", preview.Notes.Included)
	}
	mustRestore(t, svc, r.Contents, backup.RestoreReplace, preview.PreviewToken)

	if len(notes.list) != 2 {
		t.Fatalf("restore lost the library: %+v", notes.list)
	}
	// Bodies and timestamps, exactly: a note that came back with a different
	// body is a note somebody lost, and one that came back with a new
	// createdAt has lost when it was written.
	if notes.list[0].Body != "# Deploy\n\nkubectl rollout\n" || notes.list[1].Body != "второй\n" {
		t.Fatalf("bodies did not survive: %+v", notes.list)
	}
	if notes.list[0].CreatedAt != 10 || notes.list[0].UpdatedAt != 11 {
		t.Fatalf("timestamps did not survive: %+v", notes.list[0])
	}
}

func TestRestore_MergeWithNoNotesSectionLeavesTheLibraryAlone(t *testing.T) {
	// A backup written before this section existed says NOTHING about
	// notes. An unconditional write would turn that into "your notes are
	// gone", which is the one outcome this feature cannot have.
	svc, _, notes := newFakeServiceWithNotes()
	r := mustCreate(t, svc) // no notes at backup time → no section

	notes.list = []note.Note{{ID: "kept", Body: "written since", CreatedAt: 1, UpdatedAt: 1}}
	preview := mustPreview(t, svc, r.Contents, backup.RestoreMerge)
	mustRestore(t, svc, r.Contents, backup.RestoreMerge, preview.PreviewToken)

	if len(notes.list) != 1 || notes.list[0].ID != "kept" {
		t.Fatalf("merge erased a note the backup never mentioned: %+v", notes.list)
	}
}

func TestRestore_MergeAddsWhatIsMissingAndKeepsWhatIsHere(t *testing.T) {
	svc, _, notes := newFakeServiceWithNotes()
	notes.list = []note.Note{{ID: "shared", Body: "from the backup", CreatedAt: 1, UpdatedAt: 1}}
	r := mustCreate(t, svc)

	// Since the backup: one note edited here, one written here.
	notes.list = []note.Note{
		{ID: "shared", Body: "edited here", CreatedAt: 1, UpdatedAt: 5},
		{ID: "local", Body: "written here", CreatedAt: 2, UpdatedAt: 2},
	}
	preview := mustPreview(t, svc, r.Contents, backup.RestoreMerge)
	mustRestore(t, svc, r.Contents, backup.RestoreMerge, preview.PreviewToken)

	if len(notes.list) != 2 {
		t.Fatalf("merge changed the count: %+v", notes.list)
	}
	byID := map[string]note.Note{}
	for _, n := range notes.list {
		byID[n.ID] = n
	}
	// A merge is not a way to lose what only exists here, and not a way to
	// have an old copy overwrite a newer edit.
	if byID["local"].Body != "written here" {
		t.Fatalf("merge lost the note that only existed here: %+v", notes.list)
	}
	if byID["shared"].Body != "edited here" {
		t.Fatalf("merge overwrote a local edit with the backup's older copy: %+v", byID["shared"])
	}
}

func TestRestore_NoteWriteIsRolledBackWithEverythingElse(t *testing.T) {
	svc, _, notes := newFakeServiceWithNotes()
	notes.list = []note.Note{{ID: "before", Body: "before the restore", CreatedAt: 1, UpdatedAt: 1}}
	r := mustCreate(t, svc)

	notes.writeErr = errors.New("disk is gone")
	preview := mustPreview(t, svc, r.Contents, backup.RestoreReplace)
	if _, err := svc.Restore(r.Contents, backup.RestoreReplace, preview.PreviewToken); err == nil {
		t.Fatal("a failing notes write must fail the restore")
	}
	notes.writeErr = nil

	// The rollback ran inside the same interval as connections and settings:
	// the library is what it was before the restore started.
	if len(notes.list) != 1 || notes.list[0].ID != "before" {
		t.Fatalf("the library was left in a state nobody asked for: %+v", notes.list)
	}
}

func TestCreate_ReportsANotesReadItCouldNotDo(t *testing.T) {
	svc, _, notes := newFakeServiceWithNotes()
	notes.loadErr = errors.New("database is locked")
	if _, err := svc.Create(); err == nil {
		t.Fatal("a backup that cannot read the notes must fail, not omit them silently")
	}
}

func TestRestore_WithoutANoteStoreRefusesANotesBackup(t *testing.T) {
	// The refusal happens BEFORE the journal is written, so nothing is
	// half-applied by a build that cannot finish the job.
	withNotes, _, notes := newFakeServiceWithNotes()
	notes.list = []note.Note{{ID: "n1", Body: "text", CreatedAt: 1, UpdatedAt: 1}}
	r := mustCreate(t, withNotes)

	svc, _, _, _, _ := newFakeService() // wired without a notes store
	preview := mustPreview(t, svc, r.Contents, backup.RestoreReplace)
	if _, err := svc.Restore(r.Contents, backup.RestoreReplace, preview.PreviewToken); err == nil {
		t.Fatal("a service with no notes store must refuse a backup that carries notes")
	}
}
