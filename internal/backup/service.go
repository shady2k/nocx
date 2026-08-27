package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/snippet"
	"github.com/shady2k/nocx/internal/storage"
)

// ── Service ──────────────────────────────────────────────────────────────

// Service implements the Backup & Restore workflow (ADR-0027).
type Service struct {
	connections ConnectionSnapshotStore
	settings    SettingsSnapshotStore
	doc         storage.DocumentStore
	snippets    SnippetStore // nil → the backup has no snippets section
	notes       NoteStore    // nil → the backup has no notes section
}

// NewService wires the backup service from its three dependencies.
// NewService wires the backup service from its dependencies. snippets may be
// nil: the backup then simply carries no snippets section, and restore
// leaves the current library alone.
func NewService(
	connections ConnectionSnapshotStore,
	settingsStore SettingsSnapshotStore,
	doc storage.DocumentStore,
	snippets SnippetStore,
	notes NoteStore,
) *Service {
	return &Service{
		connections: connections,
		settings:    settingsStore,
		doc:         doc,
		snippets:    snippets,
		notes:       notes,
	}
}

// ── Result types ─────────────────────────────────────────────────────────

// CreateResult is the JSON-RPC result for backup.create.
type CreateResult struct {
	FileName string        `json:"fileName"`
	Contents string        `json:"contents"`
	Summary  CreateSummary `json:"summary"`
}

// CreateSummary carries integer counts from the creation pass.
type CreateSummary struct {
	Settings                       int `json:"settings"`
	Connections                    int `json:"connections"`
	Groups                         int `json:"groups"`
	Snippets                       int `json:"snippets"`
	Notes                          int `json:"notes"`
	CredentialBindingsRemoved      int `json:"credentialBindingsRemoved"`
	GroupCredentialBindingsRemoved int `json:"groupCredentialBindingsRemoved"`
	GroupDefaultKeysOmitted        int `json:"groupDefaultKeysOmitted"`
}

// RestorePreview is the JSON-RPC result for backup.preview.
type RestorePreview struct {
	PreviewToken                   string             `json:"previewToken"`
	CreatedAt                      string             `json:"createdAt"`
	Strategy                       RestoreStrategy    `json:"strategy"`
	Settings                       SettingsPreview    `json:"settings"`
	Connections                    ConnectionsPreview `json:"connections"`
	Groups                         GroupsPreview      `json:"groups"`
	Snippets                       SnippetsPreview    `json:"snippets"`
	Notes                          NotesPreview       `json:"notes"`
	ConnectionsRequiringCredential []ProfileRef       `json:"connectionsRequiringCredential"`
	Omissions                      RestoreOmissions   `json:"omissions"`
}

// SnippetsPreview carries the snippet count the preview reports.
type SnippetsPreview struct {
	Included int `json:"included"`
}

// NotesPreview carries the note count the preview reports — the number a
// person reads before deciding to restore over what they have.
type NotesPreview struct {
	Included int `json:"included"`
}

// SettingsPreview carries setting-level diff counts.
type SettingsPreview struct {
	Included int `json:"included"`
	Changed  int `json:"changed"`
	Reset    int `json:"reset"`
}

// ConnectionsPreview carries connection-level diff counts.
type ConnectionsPreview struct {
	Included int `json:"included"`
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Removed  int `json:"removed"`
}

// GroupsPreview carries group-level diff counts.
type GroupsPreview struct {
	Included int `json:"included"`
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Removed  int `json:"removed"`
}

// ProfileRef is a minimal connection reference for the UI.
type ProfileRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RestoreOmissions carries counts of items deliberately excluded.
type RestoreOmissions struct {
	CredentialBindingsRemoved      int `json:"credentialBindingsRemoved"`
	GroupCredentialBindingsRemoved int `json:"groupCredentialBindingsRemoved"`
	GroupDefaultKeysOmitted        int `json:"groupDefaultKeysOmitted"`
}

// RestoreResult is the JSON-RPC result for backup.restore.
type RestoreResult struct {
	Strategy                       RestoreStrategy  `json:"strategy"`
	SettingsChanged                int              `json:"settingsChanged"`
	SettingsReset                  int              `json:"settingsReset"`
	ConnectionsAdded               int              `json:"connectionsAdded"`
	ConnectionsUpdated             int              `json:"connectionsUpdated"`
	ConnectionsRemoved             int              `json:"connectionsRemoved"`
	GroupsAdded                    int              `json:"groupsAdded"`
	GroupsUpdated                  int              `json:"groupsUpdated"`
	GroupsRemoved                  int              `json:"groupsRemoved"`
	GroupCredentialBindingsRemoved int              `json:"groupCredentialBindingsRemoved"`
	ConnectionsRequiringCredential []ProfileRef     `json:"connectionsRequiringCredential"`
	Omissions                      RestoreOmissions `json:"omissions,omitempty"`
}

// loadNotes reads the library, or nothing when no store is wired. A read
// FAILURE is an error the caller reports: a backup that quietly omitted the
// notes it could not read would be a backup somebody trusted.
func (s *Service) loadNotes() ([]note.Note, error) {
	if s.notes == nil {
		return nil, nil
	}
	return s.notes.LoadAllNotes()
}

// ── Create ────────────────────────────────────────────────────────────────

// Create builds a backup document from the current live state.
func (s *Service) Create() (*CreateResult, error) {
	snap, err := s.connections.LoadConnectionSnapshot()
	if err != nil {
		return nil, fmt.Errorf("load connection snapshot: %w", err)
	}
	overrides := s.settings.NonSecretOverrides()
	snips, err := s.loadSnippets()
	if err != nil {
		return nil, fmt.Errorf("load snippets: %w", err)
	}
	notes, err := s.loadNotes()
	if err != nil {
		return nil, fmt.Errorf("load notes: %w", err)
	}

	doc, summary := buildDocument(snap, overrides, snips, notes)
	doc.CreatedAt = time.Now().UTC()

	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal backup: %w", err)
	}
	contents := string(raw) + "\n"

	if len(contents) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}

	filename := "nocx-backup-" + doc.CreatedAt.Format("20060102T150405Z") + ".json"

	return &CreateResult{
		FileName: filename,
		Contents: contents,
		Summary:  summary,
	}, nil
}

// ── Preview ───────────────────────────────────────────────────────────────

// Preview parses a backup file, computes the diff against current state,
// and returns a preview with a binding token.
func (s *Service) Preview(contents string, strategy RestoreStrategy) (*RestorePreview, error) {
	doc, omissions, err := parseAndValidate(contents, s.settings)
	if err != nil {
		return nil, err
	}
	if strategy != RestoreMerge && strategy != RestoreReplace {
		return nil, fmt.Errorf("%w: unknown restore strategy %q", ErrInvalidDocument, strategy)
	}

	snap, err := s.connections.LoadConnectionSnapshot()
	if err != nil {
		return nil, fmt.Errorf("load connection snapshot: %w", err)
	}
	overrides := s.settings.NonSecretOverrides()

	preview := computePreview(doc, snap, overrides, strategy, omissions)
	preview.Strategy = strategy
	preview.CreatedAt = doc.CreatedAt.UTC().Format(time.RFC3339)

	token := computePreviewToken(contents, strategy, snap, overrides)
	preview.PreviewToken = token

	return preview, nil
}

// ── Restore ───────────────────────────────────────────────────────────────

// Restore applies a backup file under the given strategy, gated by a
// preview token that must match current state.
func (s *Service) Restore(contents string, strategy RestoreStrategy, previewToken string) (*RestoreResult, error) {
	doc, omissions, err := parseAndValidate(contents, s.settings)
	if err != nil {
		return nil, err
	}
	if strategy != RestoreMerge && strategy != RestoreReplace {
		return nil, fmt.Errorf("%w: unknown restore strategy %q", ErrInvalidDocument, strategy)
	}

	snap, err := s.connections.LoadConnectionSnapshot()
	if err != nil {
		return nil, fmt.Errorf("load connection snapshot: %w", err)
	}
	overrides := s.settings.NonSecretOverrides()
	beforeSnips, err := s.loadSnippets()
	if err != nil {
		return nil, fmt.Errorf("load snippets: %w", err)
	}
	beforeNotesLib, err := s.loadNotes()
	if err != nil {
		return nil, fmt.Errorf("load notes: %w", err)
	}

	expectedToken := computePreviewToken(contents, strategy, snap, overrides)
	if previewToken != expectedToken {
		return nil, fmt.Errorf("%w: preview is stale", ErrInvalidDocument)
	}

	result, targetSnap, targetOverrides, targetSnips, writeSnips := computeRestore(doc, snap, overrides, beforeSnips, strategy, omissions)
	targetNotes, writeNotes := computeRestoreNotes(doc, beforeNotesLib, strategy)

	// A document that carries snippets cannot be applied by a service wired
	// without the snippet store (the transport's test servers are). Refuse
	// before the journal is written so nothing is half-applied.
	if writeSnips && s.snippets == nil {
		return nil, fmt.Errorf("restore: backup carries snippets but no snippet store is wired")
	}
	// Same refusal for notes, and it matters more: a snippet can be retyped
	// from the thing it automates, and a note cannot be retyped from
	// anything.
	if writeNotes && s.notes == nil {
		return nil, fmt.Errorf("restore: backup carries notes but no notes store is wired")
	}

	beforeSnap := snap
	beforeOverrides := overrides
	beforeSnippets := toBackupSnippets(beforeSnips)
	beforeNotes := toBackupNotes(beforeNotesLib)

	if jerr := writeJournal(s.doc, "prepared", &beforeSnap, &beforeOverrides, beforeSnippets, beforeNotes); jerr != nil {
		return nil, fmt.Errorf("journal prepared: %w", jerr)
	}

	if cerr := s.connections.ReplaceConnectionSnapshot(targetSnap); cerr != nil {
		if recErr := s.Recover(); recErr != nil {
			return nil, fmt.Errorf("replace connections: %w; recovery failed: %w", cerr, recErr)
		}
		return nil, fmt.Errorf("replace connections: %w", cerr)
	}

	var pn settings.PendingNotification
	pn, err = s.settings.ReplaceNonSecretOverrides(targetOverrides)
	if err != nil {
		if recErr := s.Recover(); recErr != nil {
			return nil, fmt.Errorf("replace settings: %w; recovery failed: %w", err, recErr)
		}
		return nil, fmt.Errorf("replace settings: %w", err)
	}

	// The snippet write lives INSIDE the prepared/committed interval: a
	// failure after it must take the library back with connections and
	// settings, or the restore looks successful everywhere it was not.
	if writeSnips {
		if serr := s.snippets.SaveAll(targetSnips); serr != nil {
			if recErr := s.Recover(); recErr != nil {
				return nil, fmt.Errorf("replace snippets: %w; recovery failed: %w", serr, recErr)
			}
			return nil, fmt.Errorf("replace snippets: %w", serr)
		}
	}

	// The notes write is inside the same interval, for the same reason: a
	// failure after it must take the library back with everything else, or
	// the restore looks successful everywhere it was not.
	if writeNotes {
		if nerr := s.notes.ReplaceNotes(targetNotes); nerr != nil {
			if recErr := s.Recover(); recErr != nil {
				return nil, fmt.Errorf("replace notes: %w; recovery failed: %w", nerr, recErr)
			}
			return nil, fmt.Errorf("replace notes: %w", nerr)
		}
	}

	if werr := writeJournal(s.doc, "committed", &beforeSnap, &beforeOverrides, beforeSnippets, beforeNotes); werr != nil {
		if recErr := s.Recover(); recErr != nil {
			return nil, fmt.Errorf("journal committed: %w; recovery failed: %w", werr, recErr)
		}
		return nil, fmt.Errorf("journal committed: %w", werr)
	}

	cleanupJournal(s.doc)
	s.settings.Publish(pn)

	return result, nil
}

// ── Recover ───────────────────────────────────────────────────────────────

// Recover resolves any in-flight journal state on startup or after a crash.
func (s *Service) Recover() error {
	js, err := readJournal(s.doc)
	if err != nil {
		return err
	}

	switch js.state {
	case "idle":
		return nil
	case "prepared":
		if err := s.connections.ReplaceConnectionSnapshot(*js.connections); err != nil {
			return fmt.Errorf("%w: rollback connections: %w", ErrRecoveryRequired, err)
		}
		pn, err := s.settings.ReplaceNonSecretOverrides(*js.settings)
		if err != nil {
			return fmt.Errorf("%w: rollback settings: %w", ErrRecoveryRequired, err)
		}
		// The library was written inside the same interval, so it rolls back
		// with everything else. A journal from before this field existed (or
		// from a service wired without the snippet store) carries none and
		// has nothing to restore.
		if js.snippets != nil && s.snippets != nil {
			if err := s.snippets.SaveAll(fromBackupSnippets(*js.snippets)); err != nil {
				return fmt.Errorf("%w: rollback snippets: %w", ErrRecoveryRequired, err)
			}
		}
		if js.notes != nil && s.notes != nil {
			if err := s.notes.ReplaceNotes(fromBackupNotes(*js.notes)); err != nil {
				return fmt.Errorf("%w: rollback notes: %w", ErrRecoveryRequired, err)
			}
		}
		cleanupJournal(s.doc)
		s.settings.Publish(pn)
		return nil
	case "committed":
		cleanupJournal(s.doc)
		return nil
	default:
		return fmt.Errorf("%w: unknown journal state %q", ErrRecoveryRequired, js.state)
	}
}

// ── Internal: document building ──────────────────────────────────────────

func buildDocument(snap profile.ConnectionSnapshot, overrides map[string]any, snips []snippet.Snippet, notes []note.Note) (Document, CreateSummary) {
	doc := Document{
		Format:  Format,
		Version: Version,
		Settings: SettingsSection{
			Overrides: overrides,
		},
		Connections: ConnectionsSection{
			Profiles: []BackupProfile{},
			Groups:   []BackupGroup{},
		},
	}
	sum := CreateSummary{
		Settings:    len(overrides),
		Connections: len(snap.Profiles),
		Groups:      len(snap.Groups),
		Snippets:    len(snips),
		Notes:       len(notes),
	}
	// An empty library is omitted entirely (the omitempty on Document
	// Snippets): a backup without the key predates the section, and restore
	// treats a missing section as "say nothing about snippets".
	if len(snips) > 0 {
		doc.Snippets = make([]BackupSnippet, 0, len(snips))
		for _, s := range snips {
			doc.Snippets = append(doc.Snippets, BackupSnippet{ID: s.ID, Title: s.Title, Body: s.Body})
		}
	}
	// Notes follow the same rule: an empty library is omitted entirely, and
	// a document without the key says nothing about notes rather than
	// saying "you had none".
	if len(notes) > 0 {
		doc.Notes = make([]BackupNote, 0, len(notes))
		for _, n := range notes {
			doc.Notes = append(doc.Notes, BackupNote{
				ID:        n.ID,
				Body:      n.Body,
				CreatedAt: n.CreatedAt,
				UpdatedAt: n.UpdatedAt,
			})
		}
	}

	groupMap := make(map[string]profile.ProfileGroup, len(snap.Groups))
	for _, g := range snap.Groups {
		groupMap[g.ID] = g
	}

	for _, p := range snap.Profiles {
		bp := profileToBackup(p)

		requires := profileHasSecretRefs(p)
		if !requires && p.Group != "" {
			if g, ok := groupMap[p.Group]; ok {
				if groupHasSecretRefs(g) {
					requires = true
				}
			}
		}
		if requires {
			bp.RequiresCredential = true
			sum.CredentialBindingsRemoved++
		}

		doc.Connections.Profiles = append(doc.Connections.Profiles, bp)
	}

	for _, g := range snap.Groups {
		bg, groupOmitted := groupToBackup(g)
		doc.Connections.Groups = append(doc.Connections.Groups, bg)
		sum.GroupDefaultKeysOmitted += groupOmitted
		if bg.CredentialBindingRemoved {
			sum.GroupCredentialBindingsRemoved++
		}
	}

	return doc, sum
}

// loadSnippets reads the library for the backup document. A service wired
// without the snippet store backs up no snippets and reports none.
func (s *Service) loadSnippets() ([]snippet.Snippet, error) {
	if s.snippets == nil {
		return nil, nil
	}
	return s.snippets.LoadAll()
}

// computeRestoreNotes decides what the notes library becomes. A document
// without the section says NOTHING about notes — restore leaves what is
// there alone, under either strategy — and that is what makes a backup
// written before this feature safe to restore.
//
// Merge keeps what the machine has and adds what it does not, matched on
// id: a restore is not a way to lose a note that only exists here.
func computeRestoreNotes(doc Document, current []note.Note, strategy RestoreStrategy) ([]note.Note, bool) {
	if len(doc.Notes) == 0 {
		return nil, false
	}
	if strategy == RestoreReplace {
		return fromBackupNotes(doc.Notes), true
	}
	byID := make(map[string]struct{}, len(current))
	merged := make([]note.Note, 0, len(current)+len(doc.Notes))
	for _, n := range current {
		byID[n.ID] = struct{}{}
		merged = append(merged, n)
	}
	for _, bn := range doc.Notes {
		if _, exists := byID[bn.ID]; exists {
			continue
		}
		merged = append(merged, note.Note{
			ID:        bn.ID,
			Body:      bn.Body,
			CreatedAt: bn.CreatedAt,
			UpdatedAt: bn.UpdatedAt,
		})
	}
	return merged, true
}

// toBackupNotes converts the library to the journal's wire form. nil stays
// nil — "no notes carried" is a state distinct from "an empty library".
func toBackupNotes(notes []note.Note) *[]BackupNote {
	if notes == nil {
		return nil
	}
	out := make([]BackupNote, 0, len(notes))
	for _, n := range notes {
		out = append(out, BackupNote{ID: n.ID, Body: n.Body, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt})
	}
	return &out
}

func fromBackupNotes(rows []BackupNote) []note.Note {
	out := make([]note.Note, 0, len(rows))
	for _, r := range rows {
		out = append(out, note.Note{ID: r.ID, Body: r.Body, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return out
}

// toBackupSnippets converts the library to the document's wire form. nil
// stays nil — the journal and the document use "no snippets carried" to
// distinguish a service without the store from an empty library.
func toBackupSnippets(snips []snippet.Snippet) *[]BackupSnippet {
	if snips == nil {
		return nil
	}
	out := make([]BackupSnippet, 0, len(snips))
	for _, s := range snips {
		out = append(out, BackupSnippet{ID: s.ID, Title: s.Title, Body: s.Body})
	}
	return &out
}

func fromBackupSnippets(bs []BackupSnippet) []snippet.Snippet {
	if bs == nil {
		return nil
	}
	out := make([]snippet.Snippet, 0, len(bs))
	for _, b := range bs {
		out = append(out, snippet.Snippet{ID: b.ID, Title: b.Title, Body: b.Body})
	}
	return out
}

// mergeSnippets merges the backup's snippets into the current library by
// id: a record present in both keeps its current position and takes the
// backup's field values (the backup is newer); a record only in the backup
// is appended in backup order.
func mergeSnippets(current []snippet.Snippet, backupSnips []BackupSnippet) []snippet.Snippet {
	byID := make(map[string]BackupSnippet, len(backupSnips))
	for _, b := range backupSnips {
		byID[b.ID] = b
	}
	out := make([]snippet.Snippet, 0, len(current)+len(backupSnips))
	seen := make(map[string]struct{}, len(backupSnips))
	for _, c := range current {
		if b, ok := byID[c.ID]; ok {
			c.ID, c.Title, c.Body = b.ID, b.Title, b.Body
			seen[c.ID] = struct{}{}
		}
		out = append(out, c)
	}
	for _, b := range backupSnips {
		if _, dup := seen[b.ID]; dup {
			continue
		}
		seen[b.ID] = struct{}{}
		out = append(out, snippet.Snippet{ID: b.ID, Title: b.Title, Body: b.Body})
	}
	return out
}

func profileToBackup(p profile.SSHProfile) BackupProfile {
	o := BackupSSHOptions{
		Host:              p.Options.Host,
		Port:              intVal(p.Options.Port),
		User:              strVal(p.Options.User),
		Auth:              authVal(p.Options.Auth),
		KeyPath:           strVal(p.Options.KeyPath),
		KeepaliveInterval: intVal(p.Options.KeepaliveInterval),
		KeepaliveCountMax: intVal(p.Options.KeepaliveCountMax),
		ReadyTimeout:      intVal(p.Options.ReadyTimeout),
		JumpHost:          strVal(p.Options.JumpHost),
		AgentForward:      boolVal(p.Options.AgentForward),
		DesiredMode:       desiredVal(p.Options.DesiredMode),
		RelayConsent:      consentVal(p.Options.RelayConsent),
		PortDiscovery:     discoveryVal(p.Options.PortDiscovery),
		CanBeJumpServer:   boolVal(p.Options.CanBeJumpServer),
	}
	if p.Options.Forwards != nil {
		o.Forwards = append([]profile.ForwardSpec(nil), (*p.Options.Forwards)...)
	}
	return BackupProfile{
		ID:                   p.ID,
		Type:                 p.Type,
		Name:                 p.Name,
		Group:                p.Group,
		Icon:                 p.Icon,
		Color:                p.Color,
		DisableDynamicTitle:  p.DisableDynamicTitle,
		BehaviorOnSessionEnd: p.BehaviorOnSessionEnd,
		Weight:               p.Weight,
		IsBuiltin:            p.IsBuiltin,
		Options:              o,
	}
}

// profileHasSecretRefs reports whether the profile carries any backend-owned
// secret reference (ADR-0016/0017). Backups deliberately carry no secrets, so
// such references are stripped and the restore summary counts them.
func profileHasSecretRefs(p profile.SSHProfile) bool {
	return p.Options.PasswordSecret != "" || p.Options.KeySecret != "" || p.Options.KeyPassphraseSecret != ""
}

// groupHasSecretRefs reports whether the group defaults carry secret refs.
func groupHasSecretRefs(g profile.ProfileGroup) bool {
	if g.Defaults == nil {
		return false
	}
	return g.Defaults.PasswordSecret != nil || g.Defaults.KeySecret != nil || g.Defaults.KeyPassphraseSecret != nil
}

// --- presence-aware deref helpers (StoredSSHProfileOptions) ---------------

func intVal(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolVal(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func authVal(p *profile.AuthMode) profile.AuthMode {
	if p == nil {
		return ""
	}
	return *p
}

func desiredVal(p *profile.DesiredMode) profile.DesiredMode {
	if p == nil {
		return ""
	}
	return *p
}

func consentVal(p *profile.RelayConsent) profile.RelayConsent {
	if p == nil {
		return ""
	}
	return *p
}

func discoveryVal(p *profile.PortDiscoveryMode) profile.PortDiscoveryMode {
	if p == nil {
		return ""
	}
	return *p
}

func optInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func optBool(v bool) *bool {
	if !v {
		return nil
	}
	return &v
}

func optAuth(v profile.AuthMode) *profile.AuthMode {
	if v == "" {
		return nil
	}
	return &v
}

func optDesired(v profile.DesiredMode) *profile.DesiredMode {
	if v == "" {
		return nil
	}
	return &v
}

func optConsent(v profile.RelayConsent) *profile.RelayConsent {
	if v == "" {
		return nil
	}
	return &v
}

func optDiscovery(v profile.PortDiscoveryMode) *profile.PortDiscoveryMode {
	if v == "" {
		return nil
	}
	return &v
}

func optForwards(v []profile.ForwardSpec) *[]profile.ForwardSpec {
	if len(v) == 0 {
		return nil
	}
	cp := append([]profile.ForwardSpec(nil), v...)
	return &cp
}

func optBeh(v profile.BehaviorOnSessionEnd) *profile.BehaviorOnSessionEnd {
	if v == "" {
		return nil
	}
	return &v
}

// getGroupCredentialID extracts credentialId from an untyped group defaults map.
func getGroupCredentialID(g profile.ProfileGroup) string {
	if g.Defaults == nil {
		return ""
	}
	if g.Defaults.PasswordSecret != nil {
		return *g.Defaults.PasswordSecret
	}
	if g.Defaults.KeySecret != nil {
		return *g.Defaults.KeySecret
	}
	if g.Defaults.KeyPassphraseSecret != nil {
		return *g.Defaults.KeyPassphraseSecret
	}
	return ""
}

func groupToBackup(g profile.ProfileGroup) (BackupGroup, int) {
	bg := BackupGroup{
		ID:            g.ID,
		ParentGroupID: g.ParentGroupID,
		Name:          g.Name,
		Icon:          g.Icon,
		Color:         g.Color,
		Editable:      g.Editable,
	}

	omittedCount := 0

	if g.Defaults != nil {
		bg.Defaults = &BackupGroupDefaults{}
		bg.Defaults.SSH = &BackupSSHDefaults{}
		d := g.Defaults
		bg.Defaults.SSH.Options = BackupSSHOptions{
			Port:              intVal(d.Port),
			User:              strVal(d.User),
			Auth:              authVal(d.Auth),
			KeepaliveInterval: intVal(d.KeepaliveInterval),
			KeepaliveCountMax: intVal(d.KeepaliveCountMax),
			ReadyTimeout:      intVal(d.ReadyTimeout),
			JumpHost:          strVal(d.JumpHost),
			AgentForward:      boolVal(d.AgentForward),
			DesiredMode:       desiredVal(d.DesiredMode),
			PortDiscovery:     discoveryVal(d.PortDiscovery),
		}

		if groupHasSecretRefs(g) {
			bg.CredentialBindingRemoved = true
		}

		// Secret references, key material paths and unknown keys are
		// deliberately not carried by a backup (ADR-0027): secrets are
		// never serialized, and unknown keys may be provider-specific.
		// Secret references are counted via CredentialBindingRemoved, not
		// listed by name — a backup must not name a secret key at all.
		omitted := map[string]bool{
			"defaults.keyPath":              d.KeyPath != nil,
			"defaults.behaviorOnSessionEnd": d.BehaviorOnSessionEnd != nil,
		}
		for _, k := range d.UnknownKeys() {
			omitted["defaults."+k] = true
		}
		for k, present := range omitted {
			if !present {
				continue
			}
			bg.OmittedDefaultKeys = append(bg.OmittedDefaultKeys, k)
			omittedCount++
		}
		sort.Strings(bg.OmittedDefaultKeys)
	}

	return bg, omittedCount
}

// ── Internal: parse & validate ───────────────────────────────────────────

func parseAndValidate(contents string, settings SettingsSnapshotStore) (Document, RestoreOmissions, error) {
	if len(contents) > MaxDocumentBytes {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}

	// Detect duplicate keys before the main decode so the token-level walk
	// sees the raw JSON before encoding/json's "last value wins" consumes it.
	if err := detectDuplicateKeys([]byte(contents)); err != nil {
		return Document{}, RestoreOmissions{}, err
	}

	var doc Document
	dec := json.NewDecoder(strings.NewReader(contents))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}

	if dec.More() {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: trailing content after JSON", ErrInvalidDocument)
	}

	if doc.Format != Format {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: expected format %q, got %q", ErrInvalidDocument, Format, doc.Format)
	}
	if doc.Version != Version {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: expected version %d, got %d", ErrInvalidDocument, Version, doc.Version)
	}
	if doc.CreatedAt.IsZero() {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: createdAt is required", ErrInvalidDocument)
	}
	if doc.Settings.Overrides == nil {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: settings.overrides is required", ErrInvalidDocument)
	}
	// Nil slices are valid — treat as empty.
	if doc.Connections.Profiles == nil {
		doc.Connections.Profiles = []BackupProfile{}
	}
	if doc.Connections.Groups == nil {
		doc.Connections.Groups = []BackupGroup{}
	}

	seenProfIDs := make(map[string]bool)
	for _, p := range doc.Connections.Profiles {
		if p.ID == "" {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile with empty id", ErrInvalidDocument)
		}
		if seenProfIDs[p.ID] {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: duplicate profile id %q", ErrInvalidDocument, p.ID)
		}
		seenProfIDs[p.ID] = true
		if p.Type != "ssh" {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q type must be 'ssh', got %q", ErrInvalidDocument, p.ID, p.Type)
		}
		if p.Name == "" {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has empty name", ErrInvalidDocument, p.ID)
		}
		if p.Options.Host == "" {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has empty host", ErrInvalidDocument, p.ID)
		}
		if p.Options.Port != 0 && (p.Options.Port < 1 || p.Options.Port > 65535) {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has invalid port %d", ErrInvalidDocument, p.ID, p.Options.Port)
		}
		if p.Options.KeepaliveInterval < 0 {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has negative keepaliveInterval", ErrInvalidDocument, p.ID)
		}
		if p.Options.KeepaliveCountMax < 0 {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has negative keepaliveCountMax", ErrInvalidDocument, p.ID)
		}
		if p.Options.ReadyTimeout < 0 {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has negative readyTimeout", ErrInvalidDocument, p.ID)
		}
		validAuth := map[profile.AuthMode]bool{"": true, "password": true, "publicKey": true, "agent": true, "keyboardInteractive": true}
		if !validAuth[p.Options.Auth] {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has invalid auth mode %q", ErrInvalidDocument, p.ID, p.Options.Auth)
		}
		validBeh := map[profile.BehaviorOnSessionEnd]bool{"": true, "auto": true, "keep": true, "reconnect": true, "close": true}
		if !validBeh[p.BehaviorOnSessionEnd] {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q has invalid behaviorOnSessionEnd %q", ErrInvalidDocument, p.ID, p.BehaviorOnSessionEnd)
		}
	}

	seenGroupIDs := make(map[string]bool)
	for _, g := range doc.Connections.Groups {
		if g.ID == "" {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group with empty id", ErrInvalidDocument)
		}
		if seenGroupIDs[g.ID] {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: duplicate group id %q", ErrInvalidDocument, g.ID)
		}
		seenGroupIDs[g.ID] = true
		if g.Name == "" {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q has empty name", ErrInvalidDocument, g.ID)
		}
		// Group default SSH options get the same validation as profile options:
		// restore writes them into the store, and invalid values would reach
		// profiles inheriting from the group.
		if g.Defaults != nil && g.Defaults.SSH != nil {
			o := g.Defaults.SSH.Options
			if o.Port != 0 && (o.Port < 1 || o.Port > 65535) {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q has invalid default port %d", ErrInvalidDocument, g.ID, o.Port)
			}
			if o.KeepaliveInterval < 0 {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q has negative default keepaliveInterval", ErrInvalidDocument, g.ID)
			}
			if o.KeepaliveCountMax < 0 {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q has negative default keepaliveCountMax", ErrInvalidDocument, g.ID)
			}
			if o.ReadyTimeout < 0 {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q has negative default readyTimeout", ErrInvalidDocument, g.ID)
			}
			validAuth := map[profile.AuthMode]bool{"": true, "password": true, "publicKey": true, "agent": true, "keyboardInteractive": true}
			if !validAuth[o.Auth] {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q has invalid default auth mode %q", ErrInvalidDocument, g.ID, o.Auth)
			}
		}
	}

	// Validate every profile's stored forward list through the single authority.
	for _, p := range doc.Connections.Profiles {
		if len(p.Options.Forwards) > 0 {
			if err := profile.ValidForwards(p.Options.Forwards); err != nil {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q: %v", ErrInvalidDocument, p.ID, err)
			}
		}
	}

	// Validate forward lists in group defaults through the same authority.
	for _, g := range doc.Connections.Groups {
		if g.Defaults != nil && g.Defaults.SSH != nil && len(g.Defaults.SSH.Options.Forwards) > 0 {
			if err := profile.ValidForwards(g.Defaults.SSH.Options.Forwards); err != nil {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group %q defaults: %v", ErrInvalidDocument, g.ID, err)
			}
		}
	}

	// Validate the group tree (parent references, cycles, depth) before
	// mutation. Convert backup groups to profile groups for validation.
	docGroups := make([]profile.ProfileGroup, 0, len(doc.Connections.Groups))
	for _, bg := range doc.Connections.Groups {
		docGroups = append(docGroups, backupToGroup(bg))
	}
	if err := profile.ValidateGroupTree(docGroups); err != nil {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: group tree: %v", ErrInvalidDocument, err)
	}

	// Reject any profile whose group ID is non-empty and not present in the
	// backup's group set. Orphaned profiles would silently lose their group
	// anchor on restore, leaving them outside the intended hierarchy.
	for _, p := range doc.Connections.Profiles {
		if p.Group != "" && !seenGroupIDs[p.Group] {
			return Document{}, RestoreOmissions{}, fmt.Errorf("%w: profile %q references unknown group %q", ErrInvalidDocument, p.ID, p.Group)
		}
	}
	// Validate setting keys against the registry to keep Preview and Restore consistent (ADR-0027).
	if settings != nil {
		for key, val := range doc.Settings.Overrides {
			if err := settings.ValidateSetting(key, val); err != nil {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: setting %q: %v", ErrInvalidDocument, key, err)
			}
		}
	}

	omissions := RestoreOmissions{}
	for _, g := range doc.Connections.Groups {
		if g.CredentialBindingRemoved {
			omissions.GroupCredentialBindingsRemoved++
		}
		omissions.GroupDefaultKeysOmitted += len(g.OmittedDefaultKeys)
	}
	return doc, omissions, nil
}

// detectDuplicateKeys walks the JSON tokens of raw and returns an error if
// any object contains duplicate keys (decoded-equivalent after Unicode escape
// resolution). This is stricter than the encoding/json behavior of "last
// value wins" and catches tampered files.
func detectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := checkObjectDuplicates(dec); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if _, err := dec.Token(); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	return fmt.Errorf("%w: trailing content after JSON", ErrInvalidDocument)
}

func checkObjectDuplicates(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected string key, got %T", keyTok)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := checkObjectDuplicates(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("expected object end, got %v", end)
		}
	case '[':
		for dec.More() {
			if err := checkObjectDuplicates(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("expected array end, got %v", end)
		}
	}
	return nil
}

// valuesEqual reports whether a and b are semantically equal.
// It handles types that == cannot compare (slices, maps) without panicking.
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !valuesEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !valuesEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		// Fall back to JSON canonical comparison for unknown types.
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		return string(aj) == string(bj)
	}
}

// ── Internal: preview token ──────────────────────────────────────────────

func computePreviewToken(contents string, strategy RestoreStrategy, snap profile.ConnectionSnapshot, overrides map[string]any) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:", len(contents))
	h.Write([]byte(contents))
	_, _ = fmt.Fprintf(h, ":%s:", strategy)
	canonicalize(h, snap)
	canonicalize(h, overrides)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func canonicalize(h hash.Hash, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		_, _ = fmt.Fprintf(h, "err:%v", err)
		return
	}
	_, _ = fmt.Fprintf(h, "%d:", len(b))
	h.Write(b)
}

// ── Internal: diff computation ───────────────────────────────────────────

func computePreview(doc Document, snap profile.ConnectionSnapshot, overrides map[string]any, strategy RestoreStrategy, omissions RestoreOmissions) *RestorePreview {
	p := &RestorePreview{Omissions: omissions}
	p.Settings.Included = len(doc.Settings.Overrides)
	p.Connections.Included = len(doc.Connections.Profiles)
	p.Groups.Included = len(doc.Connections.Groups)
	p.Snippets.Included = len(doc.Snippets)
	// What a person reads before deciding to restore over what they have.
	p.Notes.Included = len(doc.Notes)

	currentProfiles := snap.Profiles
	currentGroups := snap.Groups

	byID := make(map[string]profile.SSHProfile, len(currentProfiles))
	for _, cp := range currentProfiles {
		byID[cp.ID] = cp
	}

	if strategy == RestoreReplace {
		for _, bp := range doc.Connections.Profiles {
			if cp, exists := byID[bp.ID]; exists {
				if !profileEqual(bp, cp) {
					p.Connections.Updated++
				}
				delete(byID, bp.ID)
			} else {
				p.Connections.Added++
			}
		}
		p.Connections.Removed = len(byID)
	} else {
		for _, bp := range doc.Connections.Profiles {
			if cp, exists := byID[bp.ID]; exists {
				if !profileEqual(bp, cp) {
					p.Connections.Updated++
				}
			} else {
				p.Connections.Added++
			}
		}
	}

	gByID := make(map[string]profile.ProfileGroup, len(currentGroups))
	for _, cg := range currentGroups {
		gByID[cg.ID] = cg
	}
	if strategy == RestoreReplace {
		for _, bg := range doc.Connections.Groups {
			if cg, exists := gByID[bg.ID]; exists {
				if !groupFieldsEqual(bg, cg) {
					p.Groups.Updated++
				}
				delete(gByID, bg.ID)
			} else {
				p.Groups.Added++
			}
		}
		p.Groups.Removed = len(gByID)
	} else {
		for _, bg := range doc.Connections.Groups {
			if cg, exists := gByID[bg.ID]; exists {
				if !groupFieldsEqual(bg, cg) {
					p.Groups.Updated++
				}
			} else {
				p.Groups.Added++
			}
		}
	}

	if strategy == RestoreReplace {
		for key := range doc.Settings.Overrides {
			if cv, ok := overrides[key]; ok && valuesEqual(cv, doc.Settings.Overrides[key]) {
				continue
			}
			p.Settings.Changed++
		}
		for key := range overrides {
			if _, inDoc := doc.Settings.Overrides[key]; !inDoc {
				p.Settings.Reset++
			}
		}
	} else {
		for key, val := range doc.Settings.Overrides {
			if cv, ok := overrides[key]; ok && valuesEqual(cv, val) {
				continue
			}
			p.Settings.Changed++
		}
	}

	// Compute target profiles for credential-requiring check.
	var targetProfiles []profile.SSHProfile
	if strategy == RestoreReplace {
		targetProfiles = make([]profile.SSHProfile, 0, len(doc.Connections.Profiles))
		for _, bp := range doc.Connections.Profiles {
			targetProfiles = append(targetProfiles, backupToProfile(bp))
		}
	} else {
		tpByID := make(map[string]profile.SSHProfile, len(currentProfiles))
		for _, cp := range currentProfiles {
			tpByID[cp.ID] = cp
		}
		for _, bp := range doc.Connections.Profiles {
			if cp, exists := tpByID[bp.ID]; exists {
				tpByID[bp.ID] = mergeProfile(bp, cp)
			} else {
				tpByID[bp.ID] = backupToProfile(bp)
			}
		}
		targetProfiles = make([]profile.SSHProfile, 0, len(tpByID))
		for _, tp := range tpByID {
			targetProfiles = append(targetProfiles, tp)
		}
	}
	p.ConnectionsRequiringCredential = computeRequiringCredential(doc, targetProfiles)
	return p
}

func computeRestore(doc Document, snap profile.ConnectionSnapshot, overrides map[string]any, currentSnips []snippet.Snippet, strategy RestoreStrategy, omissions RestoreOmissions) (*RestoreResult, profile.ConnectionSnapshot, map[string]any, []snippet.Snippet, bool) {
	result := &RestoreResult{Strategy: strategy, Omissions: omissions}

	var targetSnap profile.ConnectionSnapshot
	var targetOverrides map[string]any

	currentProfiles := snap.Profiles
	currentGroups := snap.Groups

	if strategy == RestoreReplace {
		targetOverrides = make(map[string]any, len(doc.Settings.Overrides))
		for k, v := range doc.Settings.Overrides {
			targetOverrides[k] = v
		}
		for key := range doc.Settings.Overrides {
			if cv, ok := overrides[key]; ok && valuesEqual(cv, doc.Settings.Overrides[key]) {
				continue
			}
			result.SettingsChanged++
		}
		for key := range overrides {
			if _, inDoc := doc.Settings.Overrides[key]; !inDoc {
				result.SettingsReset++
			}
		}

		targetSnap.Profiles = make([]profile.SSHProfile, 0, len(doc.Connections.Profiles))
		for _, bp := range doc.Connections.Profiles {
			targetSnap.Profiles = append(targetSnap.Profiles, backupToProfile(bp))
		}
		targetSnap.Groups = make([]profile.ProfileGroup, 0, len(doc.Connections.Groups))
		for _, bg := range doc.Connections.Groups {
			targetSnap.Groups = append(targetSnap.Groups, backupToGroup(bg))
		}

		byID := make(map[string]profile.SSHProfile, len(currentProfiles))
		for _, cp := range currentProfiles {
			byID[cp.ID] = cp
		}
		for _, bp := range doc.Connections.Profiles {
			if cp, exists := byID[bp.ID]; exists {
				if !profileEqual(bp, cp) {
					result.ConnectionsUpdated++
				}
				delete(byID, bp.ID)
			} else {
				result.ConnectionsAdded++
			}
		}
		result.ConnectionsRemoved = len(byID)

		gByID := make(map[string]profile.ProfileGroup, len(currentGroups))
		for _, cg := range currentGroups {
			gByID[cg.ID] = cg
		}
		for _, bg := range doc.Connections.Groups {
			if cg, exists := gByID[bg.ID]; exists {
				if !groupFieldsEqual(bg, cg) {
					result.GroupsUpdated++
				}
				delete(gByID, bg.ID)
			} else {
				result.GroupsAdded++
			}
		}
		result.GroupsRemoved = len(gByID)
	} else {
		targetOverrides = make(map[string]any, len(overrides)+len(doc.Settings.Overrides))
		for k, v := range overrides {
			targetOverrides[k] = v
		}
		for k, v := range doc.Settings.Overrides {
			if cv, ok := targetOverrides[k]; !ok || !valuesEqual(cv, v) {
				result.SettingsChanged++
			}
			targetOverrides[k] = v
		}

		byID := make(map[string]profile.SSHProfile, len(currentProfiles))
		order := make([]string, 0, len(currentProfiles))
		for _, cp := range currentProfiles {
			byID[cp.ID] = cp
			order = append(order, cp.ID)
		}
		for _, bp := range doc.Connections.Profiles {
			if cp, exists := byID[bp.ID]; exists {
				merged := mergeProfile(bp, cp)
				if !profileEqual(bp, cp) {
					result.ConnectionsUpdated++
				}
				byID[bp.ID] = merged
			} else {
				result.ConnectionsAdded++
				byID[bp.ID] = backupToProfile(bp)
				order = append(order, bp.ID)
			}
		}
		targetSnap.Profiles = make([]profile.SSHProfile, 0, len(order))
		for _, id := range order {
			targetSnap.Profiles = append(targetSnap.Profiles, byID[id])
		}

		gByID := make(map[string]profile.ProfileGroup, len(currentGroups))
		gOrder := make([]string, 0, len(currentGroups))
		for _, cg := range currentGroups {
			gByID[cg.ID] = cg
			gOrder = append(gOrder, cg.ID)
		}
		for _, bg := range doc.Connections.Groups {
			if cg, exists := gByID[bg.ID]; exists {
				merged := mergeGroup(bg, cg)
				if !groupFieldsEqual(bg, cg) {
					result.GroupsUpdated++
				}
				gByID[bg.ID] = merged
			} else {
				result.GroupsAdded++
				gByID[bg.ID] = backupToGroup(bg)
				gOrder = append(gOrder, bg.ID)
			}
		}
		targetSnap.Groups = make([]profile.ProfileGroup, 0, len(gOrder))
		for _, id := range gOrder {
			targetSnap.Groups = append(targetSnap.Groups, gByID[id])
		}
	}

	result.ConnectionsRequiringCredential = computeRequiringCredential(doc, targetSnap.Profiles)
	// Count groups whose credential binding was removed.
	currentGByID := make(map[string]profile.ProfileGroup, len(currentGroups))
	for _, cg := range currentGroups {
		currentGByID[cg.ID] = cg
	}
	for _, tg := range targetSnap.Groups {
		if cg, ok := currentGByID[tg.ID]; ok {
			if getGroupCredentialID(cg) != "" && getGroupCredentialID(tg) == "" {
				result.GroupCredentialBindingsRemoved++
			}
		}
	}

	// Snippets: a document that carries the section replaces (or merges)
	// the library; a document without it — written before the section
	// existed — says nothing about snippets, and restore leaves the current
	// library alone under either strategy. The write therefore happens only
	// when the backup mentions snippets.
	var targetSnips []snippet.Snippet
	if len(doc.Snippets) > 0 {
		if strategy == RestoreReplace {
			targetSnips = fromBackupSnippets(doc.Snippets)
		} else {
			targetSnips = mergeSnippets(currentSnips, doc.Snippets)
		}
	}
	writeSnips := len(doc.Snippets) > 0

	return result, targetSnap, targetOverrides, targetSnips, writeSnips
}

func backupToProfile(bp BackupProfile) profile.SSHProfile {
	return profile.SSHProfile{
		Base: profile.Base{
			ID:                   bp.ID,
			Type:                 bp.Type,
			Name:                 bp.Name,
			Group:                bp.Group,
			Icon:                 bp.Icon,
			Color:                bp.Color,
			DisableDynamicTitle:  bp.DisableDynamicTitle,
			BehaviorOnSessionEnd: bp.BehaviorOnSessionEnd,
			Weight:               bp.Weight,
			IsBuiltin:            bp.IsBuiltin,
		},
		Options: profile.StoredSSHProfileOptions{
			Host:                 bp.Options.Host,
			Port:                 optInt(bp.Options.Port),
			User:                 optStr(bp.Options.User),
			Auth:                 optAuth(bp.Options.Auth),
			KeyPath:              optStr(bp.Options.KeyPath),
			KeepaliveInterval:    optInt(bp.Options.KeepaliveInterval),
			KeepaliveCountMax:    optInt(bp.Options.KeepaliveCountMax),
			ReadyTimeout:         optInt(bp.Options.ReadyTimeout),
			JumpHost:             optStr(bp.Options.JumpHost),
			AgentForward:         optBool(bp.Options.AgentForward),
			DesiredMode:          optDesired(bp.Options.DesiredMode),
			RelayConsent:         optConsent(bp.Options.RelayConsent),
			PortDiscovery:        optDiscovery(bp.Options.PortDiscovery),
			Forwards:             optForwards(bp.Options.Forwards),
			CanBeJumpServer:      optBool(bp.Options.CanBeJumpServer),
			BehaviorOnSessionEnd: optBeh(bp.BehaviorOnSessionEnd),
		},
	}
}

func backupToGroup(bg BackupGroup) profile.ProfileGroup {
	g := profile.ProfileGroup{
		ID:            bg.ID,
		ParentGroupID: bg.ParentGroupID,
		Name:          bg.Name,
		Icon:          bg.Icon,
		Color:         bg.Color,
		Editable:      bg.Editable,
	}
	if bg.Defaults != nil && bg.Defaults.SSH != nil {
		o := bg.Defaults.SSH.Options
		g.Defaults = &profile.ProfileDefaults{}
		g.Defaults.Port = optInt(o.Port)
		g.Defaults.User = optStr(o.User)
		g.Defaults.Auth = optAuth(o.Auth)
		g.Defaults.KeepaliveInterval = optInt(o.KeepaliveInterval)
		g.Defaults.KeepaliveCountMax = optInt(o.KeepaliveCountMax)
		g.Defaults.ReadyTimeout = optInt(o.ReadyTimeout)
		g.Defaults.JumpHost = optStr(o.JumpHost)
		g.Defaults.AgentForward = optBool(o.AgentForward)
		g.Defaults.DesiredMode = optDesired(o.DesiredMode)
		g.Defaults.PortDiscovery = optDiscovery(o.PortDiscovery)
	}
	return g
}

func mergeProfile(bp BackupProfile, cp profile.SSHProfile) profile.SSHProfile {
	mp := cp
	mp.Name = bp.Name
	mp.Group = bp.Group
	mp.Icon = bp.Icon
	mp.Color = bp.Color
	mp.DisableDynamicTitle = bp.DisableDynamicTitle
	mp.BehaviorOnSessionEnd = bp.BehaviorOnSessionEnd
	mp.Weight = bp.Weight
	mp.IsBuiltin = bp.IsBuiltin
	// Copy the options block so pointer fields are not shared with the caller's
	// snapshot before the merged values replace them.
	mp.Options = cp.Options
	mp.Options.Host = bp.Options.Host
	mp.Options.Port = optInt(bp.Options.Port)
	mp.Options.User = optStr(bp.Options.User)
	mp.Options.KeyPath = optStr(bp.Options.KeyPath)
	mp.Options.Auth = optAuth(bp.Options.Auth)
	mp.Options.KeepaliveInterval = optInt(bp.Options.KeepaliveInterval)
	mp.Options.KeepaliveCountMax = optInt(bp.Options.KeepaliveCountMax)
	mp.Options.ReadyTimeout = optInt(bp.Options.ReadyTimeout)
	mp.Options.JumpHost = optStr(bp.Options.JumpHost)
	mp.Options.DesiredMode = optDesired(bp.Options.DesiredMode)
	mp.Options.RelayConsent = optConsent(bp.Options.RelayConsent)
	mp.Options.PortDiscovery = optDiscovery(bp.Options.PortDiscovery)
	mp.Options.Forwards = optForwards(bp.Options.Forwards)
	mp.Options.AgentForward = optBool(bp.Options.AgentForward)
	mp.Options.CanBeJumpServer = optBool(bp.Options.CanBeJumpServer)
	mp.Options.BehaviorOnSessionEnd = optBeh(bp.BehaviorOnSessionEnd)

	effPort := bp.Options.Port
	if effPort == 0 {
		effPort = 22
	}
	cpEffPort := intVal(cp.Options.Port)
	if cpEffPort == 0 {
		cpEffPort = 22
	}
	hostChanged := strings.TrimSpace(bp.Options.Host) != strings.TrimSpace(cp.Options.Host) || effPort != cpEffPort
	if hostChanged {
		// The identity changed: any secret reference the current profile holds
		// belonged to the old target (ADR-0016) and must not follow it.
		mp.Options.PasswordSecret = ""
		mp.Options.KeySecret = ""
		mp.Options.KeyPassphraseSecret = ""
	}

	return mp
}

func mergeGroup(bg BackupGroup, cg profile.ProfileGroup) profile.ProfileGroup {
	mg := cg
	mg.Name = bg.Name
	mg.Icon = bg.Icon
	mg.Color = bg.Color
	mg.Editable = bg.Editable
	mg.ParentGroupID = bg.ParentGroupID

	if bg.Defaults != nil && bg.Defaults.SSH != nil {
		// The backup's defaults replace the safe fields; secret references
		// are never carried and never kept (ADR-0027).
		if mg.Defaults == nil {
			mg.Defaults = &profile.ProfileDefaults{}
		} else {
			cp := *mg.Defaults
			mg.Defaults = &cp
		}
		o := bg.Defaults.SSH.Options
		mg.Defaults.Port = optInt(o.Port)
		mg.Defaults.User = optStr(o.User)
		mg.Defaults.Auth = optAuth(o.Auth)
		mg.Defaults.KeepaliveInterval = optInt(o.KeepaliveInterval)
		mg.Defaults.KeepaliveCountMax = optInt(o.KeepaliveCountMax)
		mg.Defaults.ReadyTimeout = optInt(o.ReadyTimeout)
		mg.Defaults.JumpHost = optStr(o.JumpHost)
		mg.Defaults.AgentForward = optBool(o.AgentForward)
		mg.Defaults.DesiredMode = optDesired(o.DesiredMode)
		mg.Defaults.PortDiscovery = optDiscovery(o.PortDiscovery)
	} else if mg.Defaults != nil {
		cp := *mg.Defaults
		mg.Defaults = &cp
	}
	if mg.Defaults != nil {
		// Strip secret references from the merged group defaults either way:
		// a group backup never carries them, and stale refs must not survive.
		mg.Defaults.PasswordSecret = nil
		mg.Defaults.KeySecret = nil
		mg.Defaults.KeyPassphraseSecret = nil
	}
	return mg
}

func profileEqual(bp BackupProfile, cp profile.SSHProfile) bool {
	if bp.Name != cp.Name || bp.Group != cp.Group || bp.Icon != cp.Icon ||
		bp.Color != cp.Color || bp.DisableDynamicTitle != cp.DisableDynamicTitle ||
		bp.BehaviorOnSessionEnd != cp.BehaviorOnSessionEnd || bp.Weight != cp.Weight ||
		bp.IsBuiltin != cp.IsBuiltin {
		return false
	}
	if bp.Options.Host != cp.Options.Host || bp.Options.Port != intVal(cp.Options.Port) ||
		bp.Options.User != strVal(cp.Options.User) || bp.Options.Auth != authVal(cp.Options.Auth) ||
		bp.Options.KeyPath != strVal(cp.Options.KeyPath) ||
		bp.Options.KeepaliveInterval != intVal(cp.Options.KeepaliveInterval) ||
		bp.Options.KeepaliveCountMax != intVal(cp.Options.KeepaliveCountMax) ||
		bp.Options.ReadyTimeout != intVal(cp.Options.ReadyTimeout) ||
		bp.Options.JumpHost != strVal(cp.Options.JumpHost) ||
		bp.Options.AgentForward != boolVal(cp.Options.AgentForward) ||
		bp.Options.DesiredMode != desiredVal(cp.Options.DesiredMode) ||
		bp.Options.RelayConsent != consentVal(cp.Options.RelayConsent) ||
		bp.Options.PortDiscovery != discoveryVal(cp.Options.PortDiscovery) ||
		bp.Options.CanBeJumpServer != boolVal(cp.Options.CanBeJumpServer) ||
		!forwardValuesEqual(bp.Options.Forwards, cp.Options.Forwards) {
		return false
	}
	return true
}

func forwardValuesEqual(a []profile.ForwardSpec, b *[]profile.ForwardSpec) bool {
	if len(a) == 0 && (b == nil || len(*b) == 0) {
		return true
	}
	if b == nil || len(a) != len(*b) {
		return false
	}
	for i := range a {
		if a[i] != (*b)[i] {
			return false
		}
	}
	return true
}

func groupFieldsEqual(bg BackupGroup, cg profile.ProfileGroup) bool {
	if bg.Name != cg.Name || bg.Icon != cg.Icon ||
		bg.Color != cg.Color || bg.Editable != cg.Editable ||
		bg.ParentGroupID != cg.ParentGroupID {
		return false
	}
	// Compare only the safe SSH defaults subset — secret references and
	// unknown keys are out of scope for backup equality.
	bgSafe := map[string]any{}
	if bg.Defaults != nil && bg.Defaults.SSH != nil {
		bgSafe = backupSSHToSafeMap(bg.Defaults.SSH.Options)
	}
	cgSafe := map[string]any{}
	if cg.Defaults != nil {
		cgSafe = sparseDefaultsToSafeMap(cg.Defaults)
	}
	bgJSON, _ := json.Marshal(bgSafe)
	cgJSON, _ := json.Marshal(cgSafe)
	return string(bgJSON) == string(cgJSON)
}

func backupSSHToSafeMap(o BackupSSHOptions) map[string]any {
	m := map[string]any{}
	if o.Port != 0 {
		m["port"] = o.Port
	}
	if o.User != "" {
		m["user"] = o.User
	}
	if o.Auth != "" {
		m["auth"] = string(o.Auth)
	}
	if o.KeepaliveInterval != 0 {
		m["keepaliveInterval"] = o.KeepaliveInterval
	}
	if o.KeepaliveCountMax != 0 {
		m["keepaliveCountMax"] = o.KeepaliveCountMax
	}
	if o.ReadyTimeout != 0 {
		m["readyTimeout"] = o.ReadyTimeout
	}
	if o.JumpHost != "" {
		m["jumpHost"] = o.JumpHost
	}
	if o.AgentForward {
		m["agentForward"] = true
	}
	if o.DesiredMode != "" {
		m["desiredMode"] = string(o.DesiredMode)
	}
	if o.PortDiscovery != "" {
		m["portDiscovery"] = string(o.PortDiscovery)
	}
	return m
}

func sparseDefaultsToSafeMap(d *profile.ProfileDefaults) map[string]any {
	m := map[string]any{}
	if d.Port != nil {
		m["port"] = *d.Port
	}
	if d.User != nil {
		m["user"] = *d.User
	}
	if d.Auth != nil {
		m["auth"] = string(*d.Auth)
	}
	if d.KeepaliveInterval != nil {
		m["keepaliveInterval"] = *d.KeepaliveInterval
	}
	if d.KeepaliveCountMax != nil {
		m["keepaliveCountMax"] = *d.KeepaliveCountMax
	}
	if d.ReadyTimeout != nil {
		m["readyTimeout"] = *d.ReadyTimeout
	}
	if d.DesiredMode != nil {
		m["desiredMode"] = string(*d.DesiredMode)
	}
	if d.PortDiscovery != nil {
		m["portDiscovery"] = string(*d.PortDiscovery)
	}
	if d.JumpHost != nil {
		m["jumpHost"] = *d.JumpHost
	}
	if d.AgentForward != nil {
		m["agentForward"] = *d.AgentForward
	}
	return m
}

func computeRequiringCredential(doc Document, targetProfiles []profile.SSHProfile) []ProfileRef {
	refs := make([]ProfileRef, 0)
	for _, bp := range doc.Connections.Profiles {
		if !bp.RequiresCredential {
			continue
		}
		for _, tp := range targetProfiles {
			if tp.ID == bp.ID {
				if !profileHasSecretRefs(tp) {
					refs = append(refs, ProfileRef{ID: bp.ID, Name: bp.Name})
				}
				break
			}
		}
	}
	return refs
}
