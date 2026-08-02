package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

// ── Service ──────────────────────────────────────────────────────────────

// Service implements the Backup & Restore workflow (ADR-0018).
type Service struct {
	connections ConnectionSnapshotStore
	settings    SettingsSnapshotStore
	doc         storage.DocumentStore
}

// NewService wires the backup service from its three dependencies.
func NewService(
	connections ConnectionSnapshotStore,
	settingsStore SettingsSnapshotStore,
	doc storage.DocumentStore,
) *Service {
	return &Service{
		connections: connections,
		settings:    settingsStore,
		doc:         doc,
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
	ConnectionsRequiringCredential []ProfileRef       `json:"connectionsRequiringCredential"`
	Omissions                      RestoreOmissions   `json:"omissions"`
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

// ── Create ────────────────────────────────────────────────────────────────

// Create builds a backup document from the current live state.
func (s *Service) Create() (*CreateResult, error) {
	snap, err := s.connections.LoadConnectionSnapshot()
	if err != nil {
		return nil, fmt.Errorf("load connection snapshot: %w", err)
	}
	overrides := s.settings.NonSecretOverrides()

	doc, summary := buildDocument(snap, overrides)
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
	doc, omissions, err := parseAndValidate(contents)
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
	doc, omissions, err := parseAndValidate(contents)
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

	expectedToken := computePreviewToken(contents, strategy, snap, overrides)
	if previewToken != expectedToken {
		return nil, fmt.Errorf("%w: preview is stale", ErrInvalidDocument)
	}

	result, targetSnap, targetOverrides := computeRestore(doc, snap, overrides, strategy, omissions)

	beforeSnap := snap
	beforeOverrides := overrides

	if jerr := writeJournal(s.doc, "prepared", &beforeSnap, &beforeOverrides); jerr != nil {
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

	if werr := writeJournal(s.doc, "committed", &beforeSnap, &beforeOverrides); werr != nil {
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
		if _, err := s.settings.ReplaceNonSecretOverrides(*js.settings); err != nil {
			return fmt.Errorf("%w: rollback settings: %w", ErrRecoveryRequired, err)
		}
		cleanupJournal(s.doc)
		return nil
	case "committed":
		cleanupJournal(s.doc)
		return nil
	default:
		return fmt.Errorf("%w: unknown journal state %q", ErrRecoveryRequired, js.state)
	}
}

// ── Internal: document building ──────────────────────────────────────────

func buildDocument(snap profile.ConnectionSnapshot, overrides map[string]any) (Document, CreateSummary) {
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

func profileToBackup(p profile.SSHProfile) BackupProfile {
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
		Options: BackupSSHOptions{
			Host:              p.Options.Host,
			Port:              intVal(p.Options.Port),
			User:              strVal(p.Options.User),
			Auth:              authVal(p.Options.Auth),
			KeepaliveInterval: intVal(p.Options.KeepaliveInterval),
			KeepaliveCountMax: intVal(p.Options.KeepaliveCountMax),
			ReadyTimeout:      intVal(p.Options.ReadyTimeout),
			JumpHost:          strVal(p.Options.JumpHost),
			AgentForward:      boolVal(p.Options.AgentForward),
			CanBeJumpServer:   boolVal(p.Options.CanBeJumpServer),
		},
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
		}

		if groupHasSecretRefs(g) {
			bg.CredentialBindingRemoved = true
		}

		// Secret references, key material paths and unknown keys are
		// deliberately not carried by a backup (ADR-0018): secrets are
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

func parseAndValidate(contents string) (Document, RestoreOmissions, error) {
	if len(contents) > MaxDocumentBytes {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}

	var doc Document
	dec := json.NewDecoder(strings.NewReader(contents))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Document{}, RestoreOmissions{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}

	// Detect duplicate keys: re-parse into RawMessage to capture raw bytes,
	// compact both sides, and compare lengths. If original had duplicates, the
	// compacted raw (which preserves them) will be longer than the doc's
	// compact marshal (last value wins).
	var raw json.RawMessage
	if json.Unmarshal([]byte(contents), &raw) == nil {
		var buf bytes.Buffer
		if json.Compact(&buf, raw) == nil {
			check, _ := json.Marshal(doc)
			if len(check) < buf.Len() {
				return Document{}, RestoreOmissions{}, fmt.Errorf("%w: duplicate keys detected", ErrInvalidDocument)
			}
		}
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
	}

	// TODO(ADR-0018): validate setting keys against the registry to keep Preview and Restore consistent.

	omissions := RestoreOmissions{}
	for _, g := range doc.Connections.Groups {
		if g.CredentialBindingRemoved {
			omissions.GroupCredentialBindingsRemoved++
		}
		omissions.GroupDefaultKeysOmitted += len(g.OmittedDefaultKeys)
	}
	return doc, omissions, nil
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
			if cv, ok := overrides[key]; ok && cv == doc.Settings.Overrides[key] {
				continue
			}
			if _, ok := overrides[key]; ok {
				p.Settings.Changed++
			}
		}
		for key := range overrides {
			if _, inDoc := doc.Settings.Overrides[key]; !inDoc {
				p.Settings.Reset++
			}
		}
	} else {
		for key, val := range doc.Settings.Overrides {
			if cv, ok := overrides[key]; ok && cv == val {
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

func computeRestore(doc Document, snap profile.ConnectionSnapshot, overrides map[string]any, strategy RestoreStrategy, omissions RestoreOmissions) (*RestoreResult, profile.ConnectionSnapshot, map[string]any) {
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
			if cv, ok := overrides[key]; ok && cv == doc.Settings.Overrides[key] {
				continue
			}
			if _, ok := overrides[key]; ok {
				result.SettingsChanged++
			}
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
			if cv, ok := targetOverrides[k]; !ok || cv != v {
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

	return result, targetSnap, targetOverrides
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
			Host:              bp.Options.Host,
			Port:              optInt(bp.Options.Port),
			User:              optStr(bp.Options.User),
			Auth:              optAuth(bp.Options.Auth),
			KeepaliveInterval: optInt(bp.Options.KeepaliveInterval),
			KeepaliveCountMax: optInt(bp.Options.KeepaliveCountMax),
			ReadyTimeout:      optInt(bp.Options.ReadyTimeout),
			JumpHost:          optStr(bp.Options.JumpHost),
			AgentForward:      optBool(bp.Options.AgentForward),
			CanBeJumpServer:   optBool(bp.Options.CanBeJumpServer),
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
	mp.Options.Auth = optAuth(bp.Options.Auth)
	mp.Options.KeepaliveInterval = optInt(bp.Options.KeepaliveInterval)
	mp.Options.KeepaliveCountMax = optInt(bp.Options.KeepaliveCountMax)
	mp.Options.ReadyTimeout = optInt(bp.Options.ReadyTimeout)
	mp.Options.JumpHost = optStr(bp.Options.JumpHost)
	mp.Options.AgentForward = optBool(bp.Options.AgentForward)
	mp.Options.CanBeJumpServer = optBool(bp.Options.CanBeJumpServer)

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
		// are never carried and never kept (ADR-0018).
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
		bp.Options.KeepaliveInterval != intVal(cp.Options.KeepaliveInterval) ||
		bp.Options.KeepaliveCountMax != intVal(cp.Options.KeepaliveCountMax) ||
		bp.Options.ReadyTimeout != intVal(cp.Options.ReadyTimeout) ||
		bp.Options.JumpHost != strVal(cp.Options.JumpHost) ||
		bp.Options.AgentForward != boolVal(cp.Options.AgentForward) ||
		bp.Options.CanBeJumpServer != boolVal(cp.Options.CanBeJumpServer) {
		return false
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
