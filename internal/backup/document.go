package backup

import (
	"errors"
	"time"

	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/settings"
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

// ── Domain interfaces (ADR-0015) ─────────────────────────────────────────

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
}

// ── Document envelope ────────────────────────────────────────────────────

// Document is the on-disk/wire shape of a nocx backup file.
type Document struct {
	Format      string             `json:"format"`
	Version     int                `json:"version"`
	CreatedAt   time.Time          `json:"createdAt"`
	Settings    SettingsSection    `json:"settings"`
	Connections ConnectionsSection `json:"connections"`
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
// credentialId is deliberately absent (ADR-0015).
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
	IsTemplate           bool                         `json:"isTemplate,omitempty"`
	Options              BackupSSHOptions             `json:"options"`
	RequiresCredential   bool                         `json:"requiresCredential,omitempty"`
}

// BackupSSHOptions mirrors SSHProfileOptions without credentialId.
type BackupSSHOptions struct {
	Host              string           `json:"host,omitempty"`
	Port              int              `json:"port,omitempty"`
	User              string           `json:"user,omitempty"`
	Auth              profile.AuthMode `json:"auth,omitempty"`
	KeepaliveInterval int              `json:"keepaliveInterval,omitempty"`
	KeepaliveCountMax int              `json:"keepaliveCountMax,omitempty"`
	ReadyTimeout      int              `json:"readyTimeout,omitempty"`
	JumpHost          string           `json:"jumpHost,omitempty"`
	AgentForward      bool             `json:"agentForward,omitempty"`
	CanBeJumpServer   bool             `json:"canBeJumpServer,omitempty"`
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
