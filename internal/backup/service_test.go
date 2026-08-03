package backup_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/backup"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
)

type fakeDocStore struct {
	data map[string][]byte
}

func (f *fakeDocStore) Read(name string, into any) (bool, error) {
	b, ok := f.data[name]
	if !ok || b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, into)
}

func (f *fakeDocStore) Write(name string, doc any) error {
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
	overrides  map[string]any
	publishLog []bool
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

func newFakeService() (*backup.Service, *fakeConnStore, *fakeSettingsStore, *fakeDocStore) {
	conn := &fakeConnStore{
		snap: profile.ConnectionSnapshot{
			Profiles: []profile.SSHProfile{},
			Groups:   []profile.ProfileGroup{},
		},
	}
	sett := &fakeSettingsStore{overrides: map[string]any{}}
	doc := &fakeDocStore{}
	svc := backup.NewService(conn, sett, doc)
	return svc, conn, sett, doc
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
	svc, conn, _, _ := newFakeService()

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
	svc, conn, _, _ := newFakeService()

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
	svc, _, sett, _ := newFakeService()

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
	svc, _, _, _ := newFakeService()
	_, err := svc.Preview(`{"format":"bad","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}`, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for bad format")
	}
}

func TestParse_RejectsInvalidVersion(t *testing.T) {
	svc, _, _, _ := newFakeService()
	_, err := svc.Preview(`{"format":"nocx-backup","version":99,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}`, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for bad version")
	}
}

func TestParse_RejectsTrailingJSON(t *testing.T) {
	svc, _, _, _ := newFakeService()
	_, err := svc.Preview(`{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[],"groups":[]}}{}`, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for trailing JSON")
	}
}

func TestParse_RejectsDuplicateProfileID(t *testing.T) {
	svc, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"a","options":{"host":"h"}},{"id":"p1","type":"ssh","name":"b","options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for duplicate profile id")
	}
}

func TestParse_RejectsEmptyProfileName(t *testing.T) {
	svc, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"ssh","name":"","options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestParse_RejectsNonSSHType(t *testing.T) {
	svc, _, _, _ := newFakeService()
	contents := `{"format":"nocx-backup","version":1,"createdAt":"2026-01-01T00:00:00Z","settings":{"overrides":{}},"connections":{"profiles":[{"id":"p1","type":"telnet","name":"a","options":{"host":"h"}}],"groups":[]}}`
	_, err := svc.Preview(contents, backup.RestoreMerge)
	if err == nil {
		t.Fatal("expected error for non-ssh type")
	}
}

func TestMerge_PreservesExtraConnections(t *testing.T) {
	svc, conn, _, _ := newFakeService()

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
	svc, conn, _, _ := newFakeService()

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
	svc, conn, _, _ := newFakeService()

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
	svc, conn, _, _ := newFakeService()

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
	svc, conn, _, _ := newFakeService()

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
	svc, _, sett, _ := newFakeService()

	sett.overrides["clipboard.osc52Suppressed"] = true

	r := mustCreate(t, svc)
	preview := mustPreview(t, svc, r.Contents, backup.RestoreMerge)
	mustRestore(t, svc, r.Contents, backup.RestoreMerge, preview.PreviewToken)

	if sett.overrides["clipboard.osc52Suppressed"] != true {
		t.Error("setting override lost after merge restore")
	}
}

func TestRestore_ReplaceSettings(t *testing.T) {
	svc, _, sett, _ := newFakeService()

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
	svc, _, _, _ := newFakeService()
	if err := svc.Recover(); err != nil {
		t.Fatalf("Recover idle: %v", err)
	}
}

func TestRecover_Prepared_RollsBack(t *testing.T) {
	svc, conn, sett, doc := newFakeService()

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

	// Journal should be cleaned.
	js, _ := readTestJournal(doc)
	if js.state != "idle" {
		t.Errorf("journal state = %q, want idle", js.state)
	}
}

func TestRecover_Committed_KeepsState(t *testing.T) {
	svc, conn, sett, doc := newFakeService()

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
	svc, _, _, _ := newFakeService()
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
	svc, conn, _, _ := newFakeService()

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
