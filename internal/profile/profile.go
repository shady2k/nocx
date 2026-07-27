package profile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"fmt"
	"strings"
)

// AuthMode controls which auth buckets are tried for an SSH connection.
// AuthAuto (null) means try the full fallback-chain; a specific value
// restricts which buckets are attempted.
type AuthMode string

const (
	AuthAuto                AuthMode = ""
	AuthPassword            AuthMode = "password"
	AuthPublicKey           AuthMode = "publicKey"
	AuthAgent               AuthMode = "agent"
	AuthKeyboardInteractive AuthMode = "keyboardInteractive"
)

// BehaviorOnSessionEnd controls what happens to a tab when its session ends.
type BehaviorOnSessionEnd string

const (
	BehaviorAuto      BehaviorOnSessionEnd = "auto"
	BehaviorKeep      BehaviorOnSessionEnd = "keep"
	BehaviorReconnect BehaviorOnSessionEnd = "reconnect"
	BehaviorClose     BehaviorOnSessionEnd = "close"
)

// Base holds the generic profile fields shared by all profile types
// (SSH now, future types later). Mirrors Tabby's Profile interface.
type Base struct {
	ID                   string               `json:"id"`
	Type                 string               `json:"type"`
	Name                 string               `json:"name"`
	Group                string               `json:"group,omitempty"`
	Icon                 string               `json:"icon,omitempty"`
	Color                string               `json:"color,omitempty"`
	DisableDynamicTitle  bool                 `json:"disableDynamicTitle,omitempty"`
	BehaviorOnSessionEnd BehaviorOnSessionEnd `json:"behaviorOnSessionEnd,omitempty"`
	Weight               int                  `json:"weight,omitempty"`
	IsBuiltin            bool                 `json:"isBuiltin,omitempty"`
	IsTemplate           bool                 `json:"isTemplate,omitempty"`
}

// SSHProfileOptions is the SSH-specific options block on an SSHProfile.
// CredentialID references a reusable Credential (УЗ) by ID. If set,
// username/auth/keyPath are resolved from the credential at connect time.
// If empty, inline User/Auth/PrivateKeys are used (legacy mode).
type SSHProfileOptions struct {
	Host         string `json:"host"`
	Port         int    `json:"port,omitempty"`
	CredentialID string `json:"credentialId,omitempty"` // Link to Credential.ID
	// Inline fields (used only if CredentialID is empty)
	User              string   `json:"user,omitempty"`
	Auth              AuthMode `json:"auth,omitempty"`
	KeepaliveInterval int      `json:"keepaliveInterval,omitempty"`
	KeepaliveCountMax int      `json:"keepaliveCountMax,omitempty"`
	ReadyTimeout      int      `json:"readyTimeout,omitempty"`
	JumpHost          string   `json:"jumpHost,omitempty"` // Profile name or ID of the jump server
	AgentForward      bool     `json:"agentForward,omitempty"`
	CanBeJumpServer   bool     `json:"canBeJumpServer,omitempty"` // Whether this profile can be used as a jump server
	// RequiresReview is set by ADR-0013 migration when legacy credential binding
	// (Host/Port) cannot be automatically converted to a TrustedEndpoint grant.
	// The user must save the connection again to authorize the endpoint.
	RequiresReview bool `json:"requiresReview,omitempty"`
}

// SSHProfile is a connection profile for an SSH host. It holds only
// *identity* (host/port/user) and configuration — never secrets.
// Credentials live in the CredentialStore, addressed by identity.
type SSHProfile struct {
	Base
	Options SSHProfileOptions `json:"options"`
}

// ToPartial returns a sparse representation suitable for persistence:
// only non-zero fields are written. The JSON encoder handles omitempty.
func (p SSHProfile) ToPartial() SSHProfile {
	return p
}

// ProfileGroup is a folder that groups profiles. Groups form a tree via
// ParentGroupID. Defaults carries per-provider defaults inherited by
// profiles in this group.
type ProfileGroup struct {
	ID            string         `json:"id"`
	ParentGroupID string         `json:"parentGroupId,omitempty"`
	Name          string         `json:"name"`
	Icon          string         `json:"icon,omitempty"`
	Color         string         `json:"color,omitempty"`
	Defaults      map[string]any `json:"defaults,omitempty"`
	Editable      bool           `json:"editable,omitempty"`
}

// CredentialTrustedEndpoint represents a grant allowing this credential
// to be used for a specific saved connection profile at a resolved endpoint.
// ADR-0013: replaces the old single Host/Port binding with a set of
// profile-scoped grants.
type CredentialTrustedEndpoint struct {
	ProfileID string `json:"profileId"` // Provenance: the saved connection that granted access
	Host      string `json:"host"`      // Canonical host after SSH config resolution
	Port      uint16 `json:"port"`      // Effective port; always explicit (1-65535)
}

// Credential is a reusable authentication identity (УЗ).
// Stored separately from connections so multiple connections can share it.
// Secrets (passwords, key passphrases) are stored in the OS keychain / vault,
// keyed by Credential.ID.
// Credential is a reusable authentication identity (УЗ).
// Stored separately from connections so multiple connections can share it.
// Secrets (passwords, key passphrases) are stored in the SecretStore,
// reachable by opaque SecretID references (ADR-0011 §2).
type Credential struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`              // Display name (e.g. "work-github")
	Username string   `json:"username"`          // SSH username
	Auth     AuthMode `json:"auth"`              // Auth method: password, publicKey, agent, keyboardInteractive
	KeyPath  string   `json:"keyPath,omitempty"` // Private key path (only for publicKey auth)
	// TrustedEndpoints is the set of (profile, host, port) grants allowing
	// this credential to be used. ADR-0013: replaces Host/Port binding.
	// Empty set means "no remote endpoint authorized".
	// Backend-owned: renderer cannot submit or overwrite these.
	TrustedEndpoints []CredentialTrustedEndpoint `json:"trustedEndpoints,omitempty"`
	// SecretID is the opaque reference to the stored password in the
	// SecretStore. Never transmitted to the renderer (ADR-0011 §2).
	SecretID string `json:"secretId,omitempty"`
	// PassphraseSecretID is the opaque reference to the stored key
	// passphrase in the SecretStore. Never transmitted to the renderer.
	PassphraseSecretID string `json:"passphraseSecretId,omitempty"`
	// Legacy fields (deprecated, removed after migration):
	// Host binds this credential to a single target host. ADR-0013: replaced
	// by TrustedEndpoints. Retained for migration only.
	Host string `json:"host,omitempty"`
	// Port pins the port when set; 0 means "this host, any port".
	// ADR-0013: replaced by TrustedEndpoints. Retained for migration only.
	Port int `json:"port,omitempty"`
}

// CredentialUpdateDTO is the renderer-submitted credential update payload.
// ADR-0013: excludes backend-owned fields (TrustedEndpoints, SecretID, PassphraseSecretID).
// The presence of these fields in the JSON payload is rejected to prevent accidental or
// malicious attempts to modify backend-owned state.
type CredentialUpdateDTO struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`              // Display name (e.g. "work-github")
	Username string   `json:"username"`          // SSH username
	Auth     AuthMode `json:"auth"`              // Auth method: password, publicKey, agent, keyboardInteractive
	KeyPath  string   `json:"keyPath,omitempty"` // Private key path (only for publicKey auth)
}

// CredentialCreateDTO is the renderer-submitted credential creation payload.
// ADR-0013: excludes backend-owned fields (TrustedEndpoints, SecretID, PassphraseSecretID).
// The presence of these fields in the JSON payload is rejected.
type CredentialCreateDTO struct {
	Name     string   `json:"name"`              // Display name (e.g. "work-github")
	Username string   `json:"username"`          // SSH username
	Auth     AuthMode `json:"auth"`              // Auth method: password, publicKey, agent, keyboardInteractive
	KeyPath  string   `json:"keyPath,omitempty"` // Private key path (only for publicKey auth)
}

// NewCredentialID generates a credential id: "cred:name:uuid".
func NewCredentialID(name string) string {
	return "cred:" + slugify(name) + ":" + newUUID()
}

// Validate reports whether the credential identity fields are valid.
// ADR-0013: TrustedEndpoints are backend-owned and validated separately.
// An empty grant set is valid — it means "no remote endpoint authorized".
func (c Credential) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("credential name is required")
	}
	if strings.TrimSpace(c.Username) == "" {
		return errors.New("credential username is required")
	}
	return nil
}

// applyDefaults fills zero-valued fields on a profile with sensible defaults.
// This is layer 1 (hardcoded) of the 4-layer defaults merge; the full merge
// (provider → global → group → profile) is performed by mergeSSHOptions.
func applyDefaults(p *SSHProfile) {
	if p.Options.Port == 0 {
		p.Options.Port = 22
	}
	if p.Options.Auth == "" {
		p.Options.Auth = AuthAuto
	}
	if p.Options.User == "" {
		p.Options.User = currentUser()
	}
	if p.BehaviorOnSessionEnd == "" {
		p.BehaviorOnSessionEnd = BehaviorAuto
	}
}

// mergeSSHOptions applies the 4-layer defaults merge for SSH profile options.
// Precedence (last wins): hardcoded → providerDefaults → globalDefaults →
// groupDefaults → profile. Each layer fills fields that are zero in the
// accumulator; a non-zero value at a higher layer is preserved.
func mergeSSHOptions(
	hardcoded, providerDefaults, globalDefaults, groupDefaults, profile SSHProfileOptions,
) SSHProfileOptions {
	acc := hardcoded
	mergeInto(&acc, providerDefaults)
	mergeInto(&acc, globalDefaults)
	mergeInto(&acc, groupDefaults)
	mergeInto(&acc, profile)
	return acc
}

// mergeInto copies non-zero fields from src into dst. For slices, a non-empty
// src replaces dst (matching Tabby's arrayMerge = replace semantics).
func mergeInto(dst *SSHProfileOptions, src SSHProfileOptions) {
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Port != 0 {
		dst.Port = src.Port
	}
	if src.CredentialID != "" {
		dst.CredentialID = src.CredentialID
	}
	if src.User != "" {
		dst.User = src.User
	}
	if src.Auth != "" {
		dst.Auth = src.Auth
	}
	if src.KeepaliveInterval != 0 {
		dst.KeepaliveInterval = src.KeepaliveInterval
	}
	if src.KeepaliveCountMax != 0 {
		dst.KeepaliveCountMax = src.KeepaliveCountMax
	}
	if src.ReadyTimeout != 0 {
		dst.ReadyTimeout = src.ReadyTimeout
	}
	if src.JumpHost != "" {
		dst.JumpHost = src.JumpHost
	}
	if src.AgentForward {
		dst.AgentForward = src.AgentForward
	}
}

// ---------------------------------------------------------------------------
// ID generation and parsing
// ---------------------------------------------------------------------------

// namespacedIDParts is the parsed form of a profile id: "type:custom:name:uuid".
type namespacedIDParts struct {
	Type string
	Name string
	UUID string
}

// NewProfileID generates a namespaced profile id: "type:custom:slug:name".
// The name is slugified for filesystem/URL safety.
func NewProfileID(typ, name string) string {
	return typ + ":custom:" + slugify(name) + ":" + newUUID()
}

// isNamespacedID checks whether id has the "type:custom:..." shape.
func isNamespacedID(id string) bool {
	_, ok := parseNamespacedID(id)
	return ok
}

// parseNamespacedID splits "type:custom:name:uuid" into its parts.
func parseNamespacedID(id string) (namespacedIDParts, bool) {
	parts := strings.SplitN(id, ":", 4)
	if len(parts) < 4 || parts[1] != "custom" {
		return namespacedIDParts{}, false
	}
	return namespacedIDParts{Type: parts[0], Name: parts[2], UUID: parts[3]}, true
}

// slugify lowercases and replaces spaces/special chars with hyphens.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Group tree
// ---------------------------------------------------------------------------

// treeNode is a ProfileGroup with its children resolved — the output of
// BuildGroupTree.
type treeNode struct {
	ProfileGroup
	Children []treeNode `json:"children,omitempty"`
}

// BuildGroupTree turns a flat group list into a nested tree via ParentGroupID.
// Orphaned groups (parent not found) become roots. Cycle-safe by construction:
// a group whose parent chain loops will appear at most once because it is only
// attached when its parent is found in the map.
func BuildGroupTree(groups []ProfileGroup) []treeNode {
	m := make(map[string]*treeNode, len(groups))
	for i := range groups {
		m[groups[i].ID] = &treeNode{ProfileGroup: groups[i]}
	}

	var roots []treeNode
	for i := range groups {
		g := &groups[i]
		if g.ParentGroupID == "" {
			roots = append(roots, expandFromMap(m, g.ID))
			continue
		}
		if _, parentExists := m[g.ParentGroupID]; !parentExists {
			roots = append(roots, expandFromMap(m, g.ID))
		}
	}
	return roots
}

// expandFromMap recursively builds a treeNode with children from the map.
func expandFromMap(m map[string]*treeNode, id string) treeNode {
	node := *m[id]
	node.Children = nil
	for _, g := range m {
		if g.ParentGroupID == id {
			node.Children = append(node.Children, expandFromMap(m, g.ID))
		}
	}
	return node
}

// ResolveGroupPath walks the parent chain from the given group id up to a root,
// returning the breadcrumb path of group names (root first, leaf last).
// Cycle-guarded at 32 levels to prevent infinite loops on corrupted data.
func ResolveGroupPath(groups []ProfileGroup, id string) []string {
	m := make(map[string]ProfileGroup, len(groups))
	for _, g := range groups {
		m[g.ID] = g
	}

	var path []string
	current := id
	for depth := 0; current != "" && depth < 32; depth++ {
		g, ok := m[current]
		if !ok {
			path = append([]string{current}, path...)
			break
		}
		if g.Name != "" {
			path = append([]string{g.Name}, path...)
		}
		current = g.ParentGroupID
	}
	return path
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func currentUser() string {
	u := os.Getenv("USER")
	if u == "" {
		u = os.Getenv("LOGNAME")
	}
	if u == "" {
		u = "root"
	}
	return u
}

// newUUID generates a random hex string suitable for profile id suffixes.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Suppress unused import safety — uuid helper.
var _ = hex.EncodeToString

// CheckGrant verifies that a credential has a grant for the given profile and endpoint.
// ADR-0013: credentials are authorized for a finite set of exact endpoints.
// Returns nil if grant exists, error otherwise.
func CheckGrant(cred Credential, profileID string, endpoint CredentialTrustedEndpoint) error {
	for _, grant := range cred.TrustedEndpoints {
		if grant.ProfileID == profileID && grant.Host == endpoint.Host && grant.Port == endpoint.Port {
			return nil
		}
	}
	return fmt.Errorf("credential %s has no grant for profile %s at %s:%d",
		cred.ID, profileID, endpoint.Host, endpoint.Port)
}
