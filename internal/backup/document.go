package backup

import (
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/note"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/snippet"
)

// ── Format constants ────────────────────────────────────────────────────

const (
	Format           = "nocx-backup"
	Version          = 1
	MaxDocumentBytes = 8 << 20 // 8 MiB UTF-8 JSON
)

// ── Errors ───────────────────────────────────────────────────────────────

var (
	ErrInvalidDocument  = errors.New("invalid backup document")
	ErrRecoveryRequired = errors.New("configuration recovery required")
)

// ── Restore strategy ─────────────────────────────────────────────────────

// RestoreStrategy selects how a backup is applied.
type RestoreStrategy string

const (
	RestoreMerge   RestoreStrategy = "merge"
	RestoreReplace RestoreStrategy = "replace"
)

// ── Domain interfaces (ADR-0027) ─────────────────────────────────────────

// ConnectionSnapshotStore reads and replaces the connection portion of the
// profile store atomically, preserving credential metadata.
type ConnectionSnapshotStore interface {
	LoadConnectionSnapshot() (profile.ConnectionSnapshot, error)
	ReplaceConnectionSnapshot(profile.ConnectionSnapshot) error
}

// SettingsSnapshotStore reads and replaces non-secret settings overrides
// with deferred notification. PendingNotification is an opaque token
// from the settings package.
type SettingsSnapshotStore interface {
	NonSecretOverrides() map[string]any
	ReplaceNonSecretOverrides(map[string]any) (settings.PendingNotification, error)
	Publish(settings.PendingNotification)

	// ValidateSetting checks a key-value pair against the registry without
	// persisting anything. It returns an error for unknown, secret-class,
	// wrong-typed, object, or array values.
	ValidateSetting(key string, value any) error
}

// SnippetStore reads and replaces the snippet library document
// (internal/snippet's store satisfies it). A backup service wired without
// one (nil) simply omits the snippets section: the backup still covers
// everything the service was given.
type SnippetStore interface {
	LoadAll() ([]snippet.Snippet, error)
	SaveAll([]snippet.Snippet) error
}

// NoteStore reads and replaces the notes library (internal/note's store
// satisfies it, through a thin adapter that supplies the context). A backup
// service wired without one (nil) simply omits the notes section.
//
// Notes are the section where "the backup carries it, restore refuses to
// half-apply it" matters most: a snippet can be retyped from the thing it
// automates, and a note cannot be retyped from anything.
type NoteStore interface {
	LoadAllNotes() ([]note.Note, error)
	ReplaceNotes([]note.Note) error
}

// ── Document envelope ────────────────────────────────────────────────────

// Document is the on-disk/wire shape of a nocx backup file.
type Document struct {
	Format      string             `json:"format"`
	Version     int                `json:"version"`
	CreatedAt   time.Time          `json:"createdAt"`
	Settings    SettingsSection    `json:"settings"`
	Connections ConnectionsSection `json:"connections"`
	// Snippets is the library at backup time, in display order. Absent for
	// a backup written before this section existed — which restore must
	// accept and leave the current library alone under merge.
	Snippets []BackupSnippet `json:"snippets,omitempty"`
	// Notes is the notes library at backup time, oldest first. Absent for a
	// backup written before this section existed, with the same rule.
	Notes []BackupNote `json:"notes,omitempty"`
}

// BackupNote is the wire shape of one note. The title is deliberately
// absent: it is derived from the body wherever it is read, and a stored one
// here would be a second owner of the name that could arrive disagreeing
// with the text under it.
type BackupNote struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// SettingsSection holds only saved non-secret overrides.
type SettingsSection struct {
	Overrides map[string]any `json:"overrides"`
}

// ConnectionsSection holds profiles and groups without credential data.
type ConnectionsSection struct {
	Profiles []BackupProfile `json:"profiles"`
	Groups   []BackupGroup   `json:"groups"`
}

// ── Profile DTO ──────────────────────────────────────────────────────────

// BackupProfile is the wire shape of an SSH profile in a backup.
// credentialId is deliberately absent (ADR-0027).
type BackupProfile struct {
	ID                   string                       `json:"id"`
	Type                 string                       `json:"type"`
	Name                 string                       `json:"name"`
	Group                string                       `json:"group,omitempty"`
	Icon                 string                       `json:"icon,omitempty"`
	Color                string                       `json:"color,omitempty"`
	DisableDynamicTitle  bool                         `json:"disableDynamicTitle,omitempty"`
	BehaviorOnSessionEnd profile.BehaviorOnSessionEnd `json:"behaviorOnSessionEnd,omitempty"`
	Weight               int                          `json:"weight,omitempty"`
	IsBuiltin            bool                         `json:"isBuiltin,omitempty"`
	Options              BackupSSHOptions             `json:"options"`
	RequiresCredential   bool                         `json:"requiresCredential,omitempty"`
}

// BackupSSHOptions mirrors the current presence-aware SSH options without
// credential references or secret material.
type BackupSSHOptions struct {
	Host              string                    `json:"host,omitempty"`
	Port              int                       `json:"port,omitempty"`
	User              string                    `json:"user,omitempty"`
	Auth              profile.AuthMode          `json:"auth,omitempty"`
	KeyPath           string                    `json:"keyPath,omitempty"`
	KeepaliveInterval int                       `json:"keepaliveInterval,omitempty"`
	KeepaliveCountMax int                       `json:"keepaliveCountMax,omitempty"`
	ReadyTimeout      int                       `json:"readyTimeout,omitempty"`
	JumpHost          string                    `json:"jumpHost,omitempty"`
	AgentForward      bool                      `json:"agentForward,omitempty"`
	DesiredMode       profile.DesiredMode       `json:"desiredMode,omitempty"`
	RelayConsent      profile.RelayConsent      `json:"relayConsent,omitempty"`
	PortDiscovery     profile.PortDiscoveryMode `json:"portDiscovery,omitempty"`
	Forwards          []profile.ForwardSpec     `json:"forwards,omitempty"`
	CanBeJumpServer   bool                      `json:"canBeJumpServer,omitempty"`
}

// ── Group DTO ─────────────────────────────────────────────────────────────

// BackupGroup is the wire shape of a profile group in a backup.
type BackupGroup struct {
	ID                       string               `json:"id"`
	ParentGroupID            string               `json:"parentGroupId,omitempty"`
	Name                     string               `json:"name"`
	Icon                     string               `json:"icon,omitempty"`
	Color                    string               `json:"color,omitempty"`
	Defaults                 *BackupGroupDefaults `json:"defaults,omitempty"`
	Editable                 bool                 `json:"editable,omitempty"`
	CredentialBindingRemoved bool                 `json:"credentialBindingRemoved,omitempty"`
	OmittedDefaultKeys       []string             `json:"omittedDefaultKeys,omitempty"`
}

// BackupGroupDefaults carries only typed safe defaults per provider.
type BackupGroupDefaults struct {
	SSH *BackupSSHDefaults `json:"ssh,omitempty"`
}

// BackupSSHDefaults wraps the ten safe SSH option fields.
type BackupSSHDefaults struct {
	Options BackupSSHOptions `json:"options"`
}

// ── Snippet DTO ──────────────────────────────────────────────────────────

// BackupSnippet is the wire shape of one snippet in a backup. It mirrors
// snippet.Snippet exactly; the library's own type is not used here so the
// backup document format stays independent of the store's domain type.
type BackupSnippet struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}
