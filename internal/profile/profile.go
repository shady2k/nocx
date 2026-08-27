package profile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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

// DesiredMode is the FIRST of the three delivery axes (spec §3.5, nocx-mlm7):
// what the user wants nocx to do with this destination, resolved by the
// existing cascade (profile → group → global → hardcoded default). It
// replaces the old auto|ask|off launch policy outright (N1): raw adds
// nothing, script runs the shell tiers we ship, relay deploys the Tier-B
// binary. N3 makes script the default — wrap and install automatically,
// consent asked only for the relay.
type DesiredMode string

const (
	DesiredRaw    DesiredMode = "raw"    // nothing added: no rewrite, no remote write
	DesiredScript DesiredMode = "script" // the shell tiers we ship — no compiled artifact
	DesiredRelay  DesiredMode = "relay"  // Tier B, a deployed binary — consent-gated
)

// validDesiredMode reports whether v is a value this build recognises.
// An unrecognised stored value is not an error at decode time — it falls back
// to the default at resolution (see ResolveEffectiveProfile) so an explicit
// user choice never becomes a silent no-op.
func validDesiredMode(v DesiredMode) bool {
	switch v {
	case DesiredRaw, DesiredScript, DesiredRelay:
		return true
	default:
		return false
	}
}

// RelayConsent is the THIRD of the three delivery axes (spec §3.5): whether
// the user has consented to nocx deploying the relay binary on this
// destination. Persisted per destination and NEVER inherited — a group
// cannot express consent, and script mode never reads it. Relay mode without
// consent=granted behaves as raw.
type RelayConsent string

const (
	ConsentUnknown RelayConsent = "unknown"
	ConsentGranted RelayConsent = "granted"
	ConsentDenied  RelayConsent = "denied"
)

// validRelayConsent reports whether v is a value this build recognises.
// An unrecognised stored value falls back to unknown at resolution so it
// never reaches the wire dressed as a real choice.
func validRelayConsent(v RelayConsent) bool {
	switch v {
	case ConsentUnknown, ConsentGranted, ConsentDenied:
		return true
	default:
		return false
	}
}

// PortDiscoveryMode controls whether nocx periodically samples the remote
// host's listening ports (spec D3). PortDiscoveryAuto (the default) samples
// once the connection settles and on prompt debounce; PortDiscoveryAsk
// probes nothing until accepted; PortDiscoveryOff never probes. It is its
// own field — NOT folded into desiredMode — because a user may trust
// prompt hooks and not periodic remote exec, or the reverse (spec §6).
type PortDiscoveryMode string

const (
	PortDiscoveryAuto PortDiscoveryMode = "auto"
	PortDiscoveryAsk  PortDiscoveryMode = "ask"
	PortDiscoveryOff  PortDiscoveryMode = "off"
)

// validPortDiscovery reports whether v is a value this build recognises.
// An unrecognised stored value is not an error at decode time — it falls
// back to the default at resolution (see ResolveEffectiveProfile), exactly
// like desiredMode (nocx-mlm7): a user's explicit choice must never
// become a silent no-op.
func validPortDiscovery(v PortDiscoveryMode) bool {
	switch v {
	case PortDiscoveryAuto, PortDiscoveryAsk, PortDiscoveryOff:
		return true
	default:
		return false
	}
}

// ForwardSpec is one stored forward on a connection profile (spec §8, D5):
// topology and policy only — never credentials. It mirrors the static half
// of tunnel.Spec; the connect-time replay maps it onto the tunnel model.
// All three directions are first-class from day one (spec D4): local is the
// only implemented strategy today, and a stored remote or dynamic forward
// is preserved, never dropped or coerced to local.
type ForwardSpec struct {
	Direction   string `json:"direction"`             // local | remote | dynamic
	BindHost    string `json:"bindHost,omitempty"`    // "" = 127.0.0.1 (the tunnel layer's default)
	BindPort    int    `json:"bindPort,omitempty"`    // 0 = ephemeral; the OS answers at start
	Destination string `json:"destination,omitempty"` // "host:port"; empty for dynamic
}

// ValidForwards is the single validation authority for a stored forward
// list. It mirrors what tunnel.New accepts — local/remote require a valid
// "host:port" destination, dynamic carries none — with the destination port
// required to be a number in range (a service name like "host:ssh" is not a
// forward target) and the bind port in range, so a list that passes here
// can always be replayed. The connection editor and any transport-side gate
// ask the same question.
func ValidForwards(fs []ForwardSpec) error {
	for i, f := range fs {
		switch f.Direction {
		case "local", "remote":
			if f.Destination == "" {
				return fmt.Errorf("forward %d: %s destination is required", i, f.Direction)
			}
			_, portStr, err := net.SplitHostPort(f.Destination)
			if err != nil {
				return fmt.Errorf("forward %d: invalid %s destination %q: %w", i, f.Direction, f.Destination, err)
			}
			p, err := strconv.Atoi(portStr)
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("forward %d: %s destination port %q out of range", i, f.Direction, portStr)
			}
		case "dynamic":
			// no destination
		case "":
			return fmt.Errorf("forward %d: direction is required", i)
		default:
			return fmt.Errorf("forward %d: unknown direction %q", i, f.Direction)
		}
		if f.BindPort < 0 || f.BindPort > 65535 {
			return fmt.Errorf("forward %d: bind port %d out of range", i, f.BindPort)
		}
	}
	return nil
}

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
}

// SSHProfileOptions is the SSH-specific options block on an SSHProfile.
// PasswordSecret, KeySecret and KeyPassphraseSecret reference the secrets
// (ADR-0011 §2) the connection authenticates with: the stored password, the
// stored private key and the stored key passphrase. They are BACKEND-OWNED
// references (sec:v1:...); on the wire the transport replaces them with the
// renderer's row handles (secrow:...), so a reference never crosses the
// boundary (ADR-0017 §1).
type SSHProfileOptions struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	// PasswordSecret is the stored password the connection authenticates with.
	PasswordSecret string `json:"passwordSecret,omitempty"`
	// KeySecret is the stored private key. Mutually exclusive with KeyPath:
	// storing key material clears KeyPath, and setting KeyPath clears KeySecret.
	KeySecret string `json:"keySecret,omitempty"`
	// KeyPassphraseSecret is the stored passphrase for the private key, when
	// the key is encrypted. Bound only alongside a key.
	KeyPassphraseSecret string `json:"keyPassphraseSecret,omitempty"`
	// Inline fields. User and Auth are always the profile's own (ADR-0017);
	// KeyPath is the file-based alternative to KeySecret.
	KeyPath           string            `json:"keyPath,omitempty"`
	User              string            `json:"user,omitempty"`
	Auth              AuthMode          `json:"auth,omitempty"`
	KeepaliveInterval int               `json:"keepaliveInterval,omitempty"`
	KeepaliveCountMax int               `json:"keepaliveCountMax,omitempty"`
	ReadyTimeout      int               `json:"readyTimeout,omitempty"`
	JumpHost          string            `json:"jumpHost,omitempty"` // Profile name or ID of the jump server
	AgentForward      bool              `json:"agentForward,omitempty"`
	DesiredMode       DesiredMode       `json:"desiredMode,omitempty"`
	RelayConsent      RelayConsent      `json:"relayConsent,omitempty"`
	PortDiscovery     PortDiscoveryMode `json:"portDiscovery,omitempty"`
	Forwards          []ForwardSpec     `json:"forwards,omitempty"`
	CanBeJumpServer   bool              `json:"canBeJumpServer,omitempty"` // Whether this profile can be used as a jump server
}

// SSHProfile is a connection profile for an SSH host. It holds only
// *identity* (host/port/user) and configuration — never secrets.
// Secret references live in the vault, addressed by reference (ADR-0017).
// Options uses the presence-aware StoredSSHProfileOptions so nil
// pointer fields distinguish "not set" from "explicitly zero/false".
type SSHProfile struct {
	Base
	Options StoredSSHProfileOptions `json:"options"`
}

// StoredSSHProfileOptions is the presence-aware options type used for JSON
// storage. Pointer fields distinguish "not set" (nil) from "explicitly set
// to zero/false" (non-nil), which the dense SSHProfileOptions cannot do
// with omitempty JSON tags.
type StoredSSHProfileOptions struct {
	Host                 string                `json:"host"`
	Port                 *int                  `json:"port,omitempty"`
	PasswordSecret       string                `json:"passwordSecret,omitempty"`
	KeySecret            string                `json:"keySecret,omitempty"`
	KeyPassphraseSecret  string                `json:"keyPassphraseSecret,omitempty"`
	User                 *string               `json:"user,omitempty"`
	Auth                 *AuthMode             `json:"auth,omitempty"`
	KeepaliveInterval    *int                  `json:"keepaliveInterval,omitempty"`
	KeepaliveCountMax    *int                  `json:"keepaliveCountMax,omitempty"`
	ReadyTimeout         *int                  `json:"readyTimeout,omitempty"`
	KeyPath              *string               `json:"keyPath,omitempty"`
	JumpHost             *string               `json:"jumpHost,omitempty"`
	AgentForward         *bool                 `json:"agentForward,omitempty"`
	DesiredMode          *DesiredMode          `json:"desiredMode,omitempty"`
	RelayConsent         *RelayConsent         `json:"relayConsent,omitempty"`
	PortDiscovery        *PortDiscoveryMode    `json:"portDiscovery,omitempty"`
	Forwards             *[]ForwardSpec        `json:"forwards,omitempty"` // nil = unset; &[] = explicitly none (omitempty drops an empty slice)
	CanBeJumpServer      *bool                 `json:"canBeJumpServer,omitempty"`
	BehaviorOnSessionEnd *BehaviorOnSessionEnd `json:"behaviorOnSessionEnd,omitempty"`
}

// ToDense converts stored options to dense SSHProfileOptions. Pointer fields
// that are nil become the zero value (unset), which the caller interprets as
// "inherit" in the resolution engine.
func (s StoredSSHProfileOptions) ToDense() SSHProfileOptions {
	o := SSHProfileOptions{Host: s.Host}
	if s.PasswordSecret != "" {
		o.PasswordSecret = s.PasswordSecret
	}
	if s.KeySecret != "" {
		o.KeySecret = s.KeySecret
	}
	if s.KeyPassphraseSecret != "" {
		o.KeyPassphraseSecret = s.KeyPassphraseSecret
	}
	if s.KeyPath != nil {
		o.KeyPath = *s.KeyPath
	}
	if s.Port != nil {
		o.Port = *s.Port
	}
	if s.User != nil {
		o.User = *s.User
	}
	if s.Auth != nil {
		o.Auth = *s.Auth
	}
	if s.KeepaliveInterval != nil {
		o.KeepaliveInterval = *s.KeepaliveInterval
	}
	if s.KeepaliveCountMax != nil {
		o.KeepaliveCountMax = *s.KeepaliveCountMax
	}
	if s.ReadyTimeout != nil {
		o.ReadyTimeout = *s.ReadyTimeout
	}
	if s.JumpHost != nil {
		o.JumpHost = *s.JumpHost
	}
	if s.AgentForward != nil {
		o.AgentForward = *s.AgentForward
	}
	if s.CanBeJumpServer != nil {
		o.CanBeJumpServer = *s.CanBeJumpServer
	}
	if s.DesiredMode != nil {
		o.DesiredMode = *s.DesiredMode
	}
	if s.RelayConsent != nil {
		o.RelayConsent = *s.RelayConsent
	}
	if s.PortDiscovery != nil {
		o.PortDiscovery = *s.PortDiscovery
	}
	if s.Forwards != nil {
		o.Forwards = *s.Forwards
	}
	return o
}

// StoredOptionsFromDense converts dense SSHProfileOptions to the presence-aware
// stored representation. Zero/false values become nil (inherit), matching the
// storedOptsToSparse semantics. For explicit zero/false from patch operations,
// construct StoredSSHProfileOptions directly with the desired pointer values.
func StoredOptionsFromDense(o SSHProfileOptions) StoredSSHProfileOptions {
	s := StoredSSHProfileOptions{Host: o.Host}
	if o.PasswordSecret != "" {
		s.PasswordSecret = o.PasswordSecret
	}
	if o.KeySecret != "" {
		s.KeySecret = o.KeySecret
	}
	if o.KeyPassphraseSecret != "" {
		s.KeyPassphraseSecret = o.KeyPassphraseSecret
	}
	if o.KeyPath != "" {
		v := o.KeyPath
		s.KeyPath = &v
	}
	if o.Port != 0 {
		v := o.Port
		s.Port = &v
	}
	if o.User != "" {
		v := o.User
		s.User = &v
	}
	if o.Auth != "" {
		v := o.Auth
		s.Auth = &v
	}
	if o.KeepaliveInterval != 0 {
		v := o.KeepaliveInterval
		s.KeepaliveInterval = &v
	}
	if o.KeepaliveCountMax != 0 {
		v := o.KeepaliveCountMax
		s.KeepaliveCountMax = &v
	}
	if o.ReadyTimeout != 0 {
		v := o.ReadyTimeout
		s.ReadyTimeout = &v
	}
	if o.JumpHost != "" {
		v := o.JumpHost
		s.JumpHost = &v
	}
	if o.AgentForward {
		v := true
		s.AgentForward = &v
	}
	if o.CanBeJumpServer {
		v := true
		s.CanBeJumpServer = &v
	}
	if o.DesiredMode != "" {
		v := o.DesiredMode
		s.DesiredMode = &v
	}
	if o.RelayConsent != "" {
		v := o.RelayConsent
		s.RelayConsent = &v
	}
	if o.PortDiscovery != "" {
		v := o.PortDiscovery
		s.PortDiscovery = &v
	}
	if o.Forwards != nil {
		fwds := o.Forwards
		s.Forwards = &fwds
	}
	return s
}

// storedOptsToSparse converts StoredSSHProfileOptions (pointer-based,
// presence-aware) directly to SparseSSHOptions, preserving nil vs non-nil
// for every field including bools. This is lossless: *false stays *false,
// *0 stays *0.
func storedOptsToSparse(o StoredSSHProfileOptions) SparseSSHOptions {
	s := SparseSSHOptions{}
	if o.PasswordSecret != "" {
		v := o.PasswordSecret
		s.PasswordSecret = &v
	}
	if o.KeySecret != "" {
		v := o.KeySecret
		s.KeySecret = &v
	}
	if o.KeyPassphraseSecret != "" {
		v := o.KeyPassphraseSecret
		s.KeyPassphraseSecret = &v
	}
	if o.KeyPath != nil {
		s.KeyPath = o.KeyPath
	}
	s.Port = o.Port
	s.User = o.User
	s.Auth = o.Auth
	s.JumpHost = o.JumpHost
	s.KeepaliveInterval = o.KeepaliveInterval
	s.KeepaliveCountMax = o.KeepaliveCountMax
	s.ReadyTimeout = o.ReadyTimeout
	s.AgentForward = o.AgentForward
	s.DesiredMode = o.DesiredMode
	// RelayConsent is deliberately NOT mapped into the sparse layer: consent
	// is per-destination (spec §3.5), never inherited — merging consents
	// across cascade layers would invent an owner that does not exist. It
	// rides profile storage exactly like Forwards.
	s.PortDiscovery = o.PortDiscovery
	// Forwards are deliberately NOT mapped: a forward list is profile-owned
	// (spec §8), never inherited — merging lists across cascade layers would
	// invent semantics nobody decided.
	s.BehaviorOnSessionEnd = o.BehaviorOnSessionEnd
	return s
}

type ProfileGroup struct {
	ID            string           `json:"id"`
	ParentGroupID string           `json:"parentGroupId,omitempty"`
	Name          string           `json:"name"`
	Icon          string           `json:"icon,omitempty"`
	Color         string           `json:"color,omitempty"`
	Defaults      *ProfileDefaults `json:"defaults,omitempty"`
	Editable      bool             `json:"editable,omitempty"`
}

// ---------------------------------------------------------------------------
// Sparse options — presence-aware typed defaults for groups and globals
// ---------------------------------------------------------------------------

// SparseSSHOptions is a presence-aware counterpart to SSHProfileOptions where
// every inheritable field can be absent (nil pointer = not set, inherit).
// This is what GROUPS and GLOBAL defaults carry, and what the merge accumulates.
// The stored SSHProfile.Options uses the pointer-based StoredSSHProfileOptions.
// Only groups/globals use this sparse type for inheritance.
type SparseSSHOptions struct {
	PasswordSecret       *string               `json:"passwordSecret,omitempty"`
	KeySecret            *string               `json:"keySecret,omitempty"`
	KeyPassphraseSecret  *string               `json:"keyPassphraseSecret,omitempty"`
	Port                 *int                  `json:"port,omitempty"`
	User                 *string               `json:"user,omitempty"`
	KeyPath              *string               `json:"keyPath,omitempty"`
	Auth                 *AuthMode             `json:"auth,omitempty"`
	JumpHost             *string               `json:"jumpHost,omitempty"`
	KeepaliveInterval    *int                  `json:"keepaliveInterval,omitempty"`
	KeepaliveCountMax    *int                  `json:"keepaliveCountMax,omitempty"`
	ReadyTimeout         *int                  `json:"readyTimeout,omitempty"`
	AgentForward         *bool                 `json:"agentForward,omitempty"`
	DesiredMode          *DesiredMode          `json:"desiredMode,omitempty"`
	PortDiscovery        *PortDiscoveryMode    `json:"portDiscovery,omitempty"`
	BehaviorOnSessionEnd *BehaviorOnSessionEnd `json:"behaviorOnSessionEnd,omitempty"`
}

// ProfileDefaults is the typed defaults block on a ProfileGroup (or global
// defaults). It wraps SparseSSHOptions with custom JSON handling that records
// unknown keys instead of rejecting the document. Unknown keys are preserved
// on write so they round-trip without data loss, but they are reported by
// Validate() at write and resolution time.
type ProfileDefaults struct {
	SparseSSHOptions

	unknown map[string]json.RawMessage // unknown keys encountered during unmarshal
}

// allowedDefaultKeys returns the set of JSON field names ProfileDefaults
// accepts. Used in custom unmarshaling and DecodeDefaults.
var allowedFields = map[string]bool{
	"passwordSecret":       true,
	"keySecret":            true,
	"keyPassphraseSecret":  true,
	"port":                 true,
	"user":                 true,
	"keyPath":              true,
	"jumpHost":             true,
	"keepaliveInterval":    true,
	"keepaliveCountMax":    true,
	"readyTimeout":         true,
	"agentForward":         true,
	"portDiscovery":        true,
	"desiredMode":          true,
	"auth":                 true,
	"behaviorOnSessionEnd": true,
}

// UnmarshalJSON decodes known fields into SparseSSHOptions and records unknown
// keys with their raw values. It never returns an error for syntactically valid
// JSON — unknown keys are preserved for round-trip safety.
func (d *ProfileDefaults) UnmarshalJSON(data []byte) error {
	raw := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &d.SparseSSHOptions); err != nil {
		return err
	}
	d.unknown = make(map[string]json.RawMessage, len(raw))
	for key, val := range raw {
		if !allowedFields[key] {
			d.unknown[key] = val
		}
	}
	if len(d.unknown) == 0 {
		d.unknown = nil
	}
	return nil
}

// MarshalJSON serializes known fields and appends any unknown keys that were
// recorded during unmarshal, preserving round-trip fidelity.
func (d ProfileDefaults) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(d.SparseSSHOptions)
	if err != nil {
		return nil, err
	}
	if len(d.unknown) == 0 {
		return b, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range d.unknown {
		m[k] = v
	}
	return json.Marshal(m)
}

// UnknownKeys returns the list of JSON field names that were present during
// unmarshal but are not in the allowed set. The returned slice is sorted for
// deterministic output.
func (d *ProfileDefaults) UnknownKeys() []string {
	if d == nil || len(d.unknown) == 0 {
		return nil
	}
	keys := make([]string, 0, len(d.unknown))
	for k := range d.unknown {
		keys = append(keys, k)
	}
	return keys
}

// Validate returns an error when unknown keys exist, listing them. It returns
// nil for a nil receiver or clean defaults.
func (d *ProfileDefaults) Validate() error {
	if d == nil {
		return nil
	}
	if keys := d.UnknownKeys(); len(keys) > 0 {
		return fmt.Errorf("unknown keys in defaults: %s", strings.Join(keys, ", "))
	}
	return nil
}

// hardcodedDefaults returns the base-layer defaults that always apply when
// no group, global, or profile overrides them.
func hardcodedDefaults() SparseSSHOptions {
	port := 22
	user := currentUser()
	beh := BehaviorAuto
	mode := DesiredScript
	pd := PortDiscoveryAuto
	return SparseSSHOptions{
		Port:                 &port,
		User:                 &user,
		BehaviorOnSessionEnd: &beh,
		DesiredMode:          &mode,
		PortDiscovery:        &pd,
	}
}

// FieldSource identifies where an effective field value came from.
type FieldSource string

const (
	FieldSourceProfile FieldSource = "profile" // explicitly set on the stored profile
	FieldSourceGroup   FieldSource = "group:"  // prefix — actual source is "group:<id>"
	FieldSourceGlobal  FieldSource = "global"  // global defaults (Wave 2a: not yet wired to a store)
	FieldSourceDefault FieldSource = "default" // hardcoded application default
)

// EffectiveProfile holds the resolved values for a single profile plus
// per-field provenance. An inherited value is NEVER written back into the
// stored profile — the caller can distinguish "inherited 2222" from
// "overridden here to 2222" forever by comparing Profile against the stored
// version through Source.
// EffectiveProfile holds the resolved values for a single profile plus
// per-field provenance. Profile carries the original (stored) profile;
// ResolvedOptions holds the fully-resolved dense values. An inherited value
// is NEVER written back into the stored profile — the caller can distinguish
// "inherited 2222" from "overridden here to 2222" forever by comparing
// Profile against the stored version through Source.
type EffectiveProfile struct {
	Profile         SSHProfile             // original stored profile
	ResolvedOptions SSHProfileOptions      // resolved values (dense, all-specified)
	Source          map[string]FieldSource // field name -> winning provenance
}

// ---------------------------------------------------------------------------
// Group graph validation
// ---------------------------------------------------------------------------

var (
	ErrCycleDetected = errors.New("group cycle detected")
	ErrMissingParent = errors.New("group parent not found")
	ErrDepthExceeded = errors.New("group depth exceeds maximum")
)

const maxGroupDepth = 32

// ValidateGroupTree checks every group for valid parent references, cycles,
// and depth > 32. It returns the first error found, naming the offending
// group ID in the error message.
func ValidateGroupTree(groups []ProfileGroup) error {
	byID := make(map[string]ProfileGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}

	for _, g := range groups {
		if err := validateGroup(byID, g.ID); err != nil {
			return fmt.Errorf("group %s: %w", g.ID, err)
		}
	}
	return nil
}

// validateGroup checks a single group's parent chain for existence, cycles,
// and depth.
func validateGroup(byID map[string]ProfileGroup, startID string) error {
	current := startID
	seen := map[string]bool{startID: true}
	for range maxGroupDepth {
		g, ok := byID[current]
		if !ok {
			// root with no parent
			return nil
		}
		if g.ParentGroupID == "" {
			// reached a root — valid
			return nil
		}
		if _, ok := byID[g.ParentGroupID]; !ok {
			return fmt.Errorf("parent %q: %w", g.ParentGroupID, ErrMissingParent)
		}
		if seen[g.ParentGroupID] {
			return fmt.Errorf("parent %q: %w", g.ParentGroupID, ErrCycleDetected)
		}
		seen[g.ParentGroupID] = true
		current = g.ParentGroupID
	}
	return fmt.Errorf("%w (max %d)", ErrDepthExceeded, maxGroupDepth)
}

// ResolveGroupChain walks the parent chain from the given leaf group ID up
// to a root, returning groups ordered from nearest ancestor to root.
// Returns an error if a parent is missing or a cycle is detected.
func ResolveGroupChain(groups []ProfileGroup, leafGroupID string) ([]ProfileGroup, error) {
	byID := make(map[string]ProfileGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}

	if err := validateGroup(byID, leafGroupID); err != nil {
		return nil, err
	}

	var chain []ProfileGroup
	current := leafGroupID
	for range maxGroupDepth {
		g, ok := byID[current]
		if !ok || g.ParentGroupID == "" {
			break
		}
		parent, ok := byID[g.ParentGroupID]
		if !ok {
			break
		}
		chain = append(chain, parent)
		current = g.ParentGroupID
	}
	return chain, nil
}

// ---------------------------------------------------------------------------
// Effective profile resolution
// ---------------------------------------------------------------------------

// fieldSourceForGroup builds the provenance string for a group-source field.
func fieldSourceForGroup(id string) FieldSource {
	return FieldSource("group:" + id)
}

// ResolveEffectiveProfile produces the resolved profile with per-field
// provenance. Precedence (highest to lowest):
//
//	profile → nearest ancestor group → … → root group → global → hardcoded default
//
// Host is never inherited and is always required.
func ResolveEffectiveProfile(
	profile SSHProfile,
	groups []ProfileGroup,
	globalDefaults SparseSSHOptions,
) (EffectiveProfile, error) {
	if strings.TrimSpace(profile.Options.Host) == "" {
		return EffectiveProfile{}, errors.New("profile host is required and cannot be inherited")
	}

	// Build group chain (nearest ancestor first, then parent, up to root).
	groupChain := make([]ProfileGroup, 0)
	if profile.Group != "" {
		var err error
		groupChain, err = ResolveGroupChain(groups, profile.Group)
		if err != nil {
			return EffectiveProfile{}, fmt.Errorf("resolve group chain from %s: %w", profile.Group, err)
		}
	}

	// Convert profile's stored options to sparse — pointer-based means
	// nil = inherit, non-nil = explicitly set (even for false/zero). This
	// is lossless unlike the old dense conversion.
	profileSparse := storedOptsToSparse(profile.Options)

	// Also extract the profile's own BehaviorOnSessionEnd from Base.
	if profile.BehaviorOnSessionEnd != "" {
		beh := profile.BehaviorOnSessionEnd
		profileSparse.BehaviorOnSessionEnd = &beh
	}

	// Start with hardcoded defaults (lowest priority).
	acc := hardcodedDefaults()
	source := map[string]FieldSource{}
	source["port"] = FieldSourceDefault
	source["user"] = FieldSourceDefault
	source["behaviorOnSessionEnd"] = FieldSourceDefault
	source["desiredMode"] = FieldSourceDefault
	source["portDiscovery"] = FieldSourceDefault

	// Apply global defaults.
	applySparseLayer(&acc, &source, globalDefaults, FieldSourceGlobal)

	// Apply group defaults: root first, nearest last (so nearest wins).
	for i := len(groupChain) - 1; i >= 0; i-- {
		g := groupChain[i]
		if g.Defaults != nil {
			if err := g.Defaults.Validate(); err != nil {
				return EffectiveProfile{}, fmt.Errorf("group %q defaults: %w", g.ID, err)
			}
			applySparseLayer(&acc, &source, g.Defaults.SparseSSHOptions, fieldSourceForGroup(g.ID))
		}
	}

	// Apply the profile's own group defaults (leaf group), which are not
	// included in the chain returned by ResolveGroupChain.
	if profile.Group != "" {
		for _, g := range groups {
			if g.ID == profile.Group && g.Defaults != nil {
				if err := g.Defaults.Validate(); err != nil {
					return EffectiveProfile{}, fmt.Errorf("group %q defaults: %w", g.ID, err)
				}
				applySparseLayer(&acc, &source, g.Defaults.SparseSSHOptions, fieldSourceForGroup(g.ID))
			}
		}
	}

	// Apply profile's own options (highest priority).
	applySparseLayer(&acc, &source, profileSparse, FieldSourceProfile)

	// A desiredMode value this build does not recognise falls back to the
	// default (script — N3 wraps and installs automatically) rather than
	// being treated as a silent no-op: script is the safe behaviour for an
	// unrecognised choice, and the provenance records "default" so the
	// effective view shows the fallback instead of a value that never
	// takes effect.
	if acc.DesiredMode != nil && !validDesiredMode(*acc.DesiredMode) {
		mode := DesiredScript
		acc.DesiredMode = &mode
		source["desiredMode"] = FieldSourceDefault
	}

	// RelayConsent never enters the cascade, so the same unrecognised-value
	// rule applies to the profile's own stored options: an unknown consent
	// falls back to unknown rather than reaching the wire dressed as a real
	// choice. Normalised on `result` (below) so the effective profile and
	// its DTO agree.

	// The same rule for portDiscovery (spec D3): an unrecognised stored
	// value falls back to auto with provenance "default" rather than being
	// treated as a silent no-op.
	if acc.PortDiscovery != nil && !validPortDiscovery(*acc.PortDiscovery) {
		pd := PortDiscoveryAuto
		acc.PortDiscovery = &pd
		source["portDiscovery"] = FieldSourceDefault
	}

	result := profile
	if result.Options.RelayConsent != nil && !validRelayConsent(*result.Options.RelayConsent) {
		c := ConsentUnknown
		result.Options.RelayConsent = &c
	}
	resolvedOpts := sparseToOptions(acc)

	// Apply BehaviorOnSessionEnd from accumulator to the result's Base.
	if acc.BehaviorOnSessionEnd != nil {
		result.BehaviorOnSessionEnd = *acc.BehaviorOnSessionEnd
	}

	return EffectiveProfile{Profile: result, ResolvedOptions: resolvedOpts, Source: source}, nil
}

// applySparseLayer overlays src into acc for non-nil fields, recording
// provenance. acc is updated in place ONLY for fields that are nil in acc
// or that src explicitly sets (including explicit false for bools).
func applySparseLayer(acc *SparseSSHOptions, source *map[string]FieldSource, src SparseSSHOptions, layer FieldSource) {
	if src.PasswordSecret != nil {
		acc.PasswordSecret = src.PasswordSecret
		setSource(source, "passwordSecret", layer)
	}
	if src.KeySecret != nil {
		acc.KeySecret = src.KeySecret
		setSource(source, "keySecret", layer)
	}
	if src.KeyPassphraseSecret != nil {
		acc.KeyPassphraseSecret = src.KeyPassphraseSecret
		setSource(source, "keyPassphraseSecret", layer)
	}
	if src.Port != nil {
		acc.Port = src.Port
		setSource(source, "port", layer)
	}
	if src.User != nil {
		acc.User = src.User
		setSource(source, "user", layer)
	}
	if src.Auth != nil {
		acc.Auth = src.Auth
		setSource(source, "auth", layer)
	}
	if src.KeyPath != nil {
		acc.KeyPath = src.KeyPath
		setSource(source, "keyPath", layer)
	}
	if src.JumpHost != nil {
		acc.JumpHost = src.JumpHost
		setSource(source, "jumpHost", layer)
	}
	if src.KeepaliveInterval != nil {
		acc.KeepaliveInterval = src.KeepaliveInterval
		setSource(source, "keepaliveInterval", layer)
	}
	if src.KeepaliveCountMax != nil {
		acc.KeepaliveCountMax = src.KeepaliveCountMax
		setSource(source, "keepaliveCountMax", layer)
	}
	if src.ReadyTimeout != nil {
		acc.ReadyTimeout = src.ReadyTimeout
		setSource(source, "readyTimeout", layer)
	}
	if src.AgentForward != nil {
		acc.AgentForward = src.AgentForward
		setSource(source, "agentForward", layer)
	}
	if src.DesiredMode != nil {
		acc.DesiredMode = src.DesiredMode
		setSource(source, "desiredMode", layer)
	}
	if src.PortDiscovery != nil {
		acc.PortDiscovery = src.PortDiscovery
		setSource(source, "portDiscovery", layer)
	}
	if src.BehaviorOnSessionEnd != nil {
		acc.BehaviorOnSessionEnd = src.BehaviorOnSessionEnd
		setSource(source, "behaviorOnSessionEnd", layer)
	}
}

// setSource records field <- layer in source, overwriting any previous entry.
func setSource(source *map[string]FieldSource, field string, layer FieldSource) {
	if *source == nil {
		*source = map[string]FieldSource{}
	}
	(*source)[field] = layer
}

// sparseToOptions converts a sparse representation back to dense SSHProfileOptions.
// BehaviorOnSessionEnd is NOT handled here — it lives on Base, not Options.
// The caller applies it to the result's Base separately.
func sparseToOptions(s SparseSSHOptions) SSHProfileOptions {
	o := SSHProfileOptions{}
	if s.PasswordSecret != nil {
		o.PasswordSecret = *s.PasswordSecret
	}
	if s.KeySecret != nil {
		o.KeySecret = *s.KeySecret
	}
	if s.KeyPassphraseSecret != nil {
		o.KeyPassphraseSecret = *s.KeyPassphraseSecret
	}
	if s.KeyPath != nil {
		o.KeyPath = *s.KeyPath
	}
	if s.Port != nil {
		o.Port = *s.Port
	}
	if s.User != nil {
		o.User = *s.User
	}
	if s.Auth != nil {
		o.Auth = *s.Auth
	}
	if s.JumpHost != nil {
		o.JumpHost = *s.JumpHost
	}
	if s.KeepaliveInterval != nil {
		o.KeepaliveInterval = *s.KeepaliveInterval
	}
	if s.KeepaliveCountMax != nil {
		o.KeepaliveCountMax = *s.KeepaliveCountMax
	}
	if s.ReadyTimeout != nil {
		o.ReadyTimeout = *s.ReadyTimeout
	}
	if s.AgentForward != nil {
		o.AgentForward = *s.AgentForward
	}
	if s.DesiredMode != nil {
		o.DesiredMode = *s.DesiredMode
	}
	if s.PortDiscovery != nil {
		o.PortDiscovery = *s.PortDiscovery
	}
	return o
}

// ---------------------------------------------------------------------------
// Legacy map decode
// ---------------------------------------------------------------------------

// DecodeDefaults decodes a map[string]any (the old ProfileGroup.Defaults
// format) into a ProfileDefaults. Unknown keys are recorded (not rejected),
// so they round-trip safely and are reported by Validate() at write or
// resolution time.
func DecodeDefaults(m map[string]any) (ProfileDefaults, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return ProfileDefaults{}, fmt.Errorf("re-encode defaults: %w", err)
	}
	var d ProfileDefaults
	if err := d.UnmarshalJSON(data); err != nil {
		return ProfileDefaults{}, err
	}
	return d, nil
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
// MaxIDRunes is the shared upper bound for backend-minted profile-domain ids
// and renderer-supplied ids accepted by the transport.
const MaxIDRunes = 128

const (
	uuidHexRunes        = 32
	mintedIDFixedRunes  = len(":custom::") + uuidHexRunes
	maxIDNamespaceRunes = MaxIDRunes - mintedIDFixedRunes
)

func mintID(namespace, name string) string {
	namespace = truncateRunes(namespace, maxIDNamespaceRunes)
	slugBudget := maxIDNamespaceRunes - utf8.RuneCountInString(namespace)
	return namespace + ":custom:" + slugify(name, slugBudget) + ":" + newUUID()
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

func NewProfileID(typ, name string) string {
	return mintID(typ, name)
}

// NewGroupID generates a namespaced group id: "group:custom:slug:uuid".
//
// Group ids are minted here rather than in the renderer for the same reason
// profile ids are: an id is identity, and a display layer that invents one has
// to know the uniqueness rule the store enforces. CreateGroup refuses an empty
// id, so something must fill it — this is that something.
func NewGroupID(name string) string {
	return mintID("group", name)
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
func slugify(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(min(len(s), maxRunes))
	for _, r := range s {
		if b.Len() == maxRunes {
			break
		}
		r = unicode.ToLower(r)
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
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

// ConnectionSnapshot is an atomic projection of profiles and groups suitable
// for backup/restore. Credential references remain metadata and no secret
// values are included (ADR-0018).
type ConnectionSnapshot struct {
	Profiles []SSHProfile   `json:"profiles"`
	Groups   []ProfileGroup `json:"groups"`
}

// ---------------------------------------------------------------------------
// Effective profile DTO — wire format with closed-enum source kinds
// ---------------------------------------------------------------------------

// EffectiveSourceKind is a closed enum for the renderer to switch on.
type EffectiveSourceKind string

const (
	EffectiveSourceProfile   EffectiveSourceKind = "profile"
	EffectiveSourceGroup     EffectiveSourceKind = "group"
	EffectiveSourceSSHConfig EffectiveSourceKind = "sshConfig"
	EffectiveSourceGlobal    EffectiveSourceKind = "global"
	EffectiveSourceDefault   EffectiveSourceKind = "default"
)

// EffectiveFieldDTO is the per-field wire representation.
type EffectiveFieldDTO struct {
	Value  any            `json:"value"`
	Source FieldSourceDTO `json:"source"`
}

// FieldSourceDTO is the provenance source in the wire format.
type FieldSourceDTO struct {
	Kind  EffectiveSourceKind `json:"kind"`
	ID    string              `json:"id"`
	Label string              `json:"label"`
}

// EffectiveProfileDTO is the per-profile wire representation.
type EffectiveProfileDTO struct {
	ID           string                       `json:"id"`
	Fields       map[string]EffectiveFieldDTO `json:"fields"`
	RelayConsent RelayConsent                 `json:"relayConsent"`
}

// EffectiveBatchResponse is the response to profiles.effective.
type EffectiveBatchResponse struct {
	Profiles []EffectiveProfileDTO `json:"profiles"`
}

// ToEffectiveDTO converts the resolved EffectiveProfile with supporting data
// into the wire format. Returned fields include every inheritable option
// even when zero/false/empty — omission would make "false" indistinguishable
// from "unresolved".
func ToEffectiveDTO(eff EffectiveProfile, groupByID map[string]ProfileGroup) EffectiveProfileDTO {
	fields := make(map[string]EffectiveFieldDTO)
	p := eff.ResolvedOptions

	// Helper to add a field with provenance.
	addField := func(name string, value any, source FieldSource) {
		kind, id, label := parseFieldSource(source, groupByID)
		fields[name] = EffectiveFieldDTO{
			Value:  value,
			Source: FieldSourceDTO{Kind: kind, ID: id, Label: label},
		}
	}

	// Options fields.
	addField("host", p.Host, eff.Source["host"])
	addField("port", p.Port, eff.Source["port"])
	addField("passwordSecret", p.PasswordSecret, eff.Source["passwordSecret"])
	addField("keySecret", p.KeySecret, eff.Source["keySecret"])
	addField("keyPassphraseSecret", p.KeyPassphraseSecret, eff.Source["keyPassphraseSecret"])
	addField("user", p.User, eff.Source["user"])
	addField("auth", string(p.Auth), eff.Source["auth"])
	addField("keepaliveInterval", p.KeepaliveInterval, eff.Source["keepaliveInterval"])
	addField("keepaliveCountMax", p.KeepaliveCountMax, eff.Source["keepaliveCountMax"])
	addField("readyTimeout", p.ReadyTimeout, eff.Source["readyTimeout"])
	addField("jumpHost", p.JumpHost, eff.Source["jumpHost"])
	addField("agentForward", p.AgentForward, eff.Source["agentForward"])
	addField("canBeJumpServer", p.CanBeJumpServer, eff.Source["canBeJumpServer"])
	addField("desiredMode", string(p.DesiredMode), eff.Source["desiredMode"])
	addField("portDiscovery", string(p.PortDiscovery), eff.Source["portDiscovery"])
	addField("behaviorOnSessionEnd", string(eff.Profile.BehaviorOnSessionEnd), eff.Source["behaviorOnSessionEnd"])

	consent := ConsentUnknown
	if eff.Profile.Options.RelayConsent != nil {
		consent = *eff.Profile.Options.RelayConsent
	}
	return EffectiveProfileDTO{ID: eff.Profile.ID, Fields: fields, RelayConsent: consent}
}

// parseFieldSource converts an internal FieldSource string into the closed-enum
// wire format with resolved group labels.
func parseFieldSource(src FieldSource, groupByID map[string]ProfileGroup) (kind EffectiveSourceKind, id, label string) {
	if src == "" {
		return EffectiveSourceDefault, "", ""
	}
	s := string(src)

	if src == FieldSourceProfile {
		return EffectiveSourceProfile, "", ""
	}
	if src == FieldSourceGlobal {
		return EffectiveSourceGlobal, "", ""
	}
	if src == FieldSourceDefault {
		return EffectiveSourceDefault, "", ""
	}
	if strings.HasPrefix(s, "group:") {
		gid := s[6:]
		kind = EffectiveSourceGroup
		id = gid
		if g, ok := groupByID[gid]; ok {
			label = g.Name
		}
		return
	}

	// Unknown — treat as default.
	return EffectiveSourceDefault, "", ""
}

// fieldNames is the closed allowlist of patchable field paths.
// Each entry maps a JSON path to a mutator that applies the value
// to StoredSSHProfileOptions.
type patchPath string

const (
	patchPort                 patchPath = "options.port"
	patchUser                 patchPath = "options.user"
	patchAuth                 patchPath = "options.auth"
	patchPasswordSecret       patchPath = "options.passwordSecret"
	patchKeySecret            patchPath = "options.keySecret" //nolint:gosec // a JSON patch path naming the key-secret field, not a credential
	patchKeyPassphraseSecret  patchPath = "options.keyPassphraseSecret"
	patchKeepaliveInterval    patchPath = "options.keepaliveInterval"
	patchKeepaliveCountMax    patchPath = "options.keepaliveCountMax"
	patchReadyTimeout         patchPath = "options.readyTimeout"
	patchJumpHost             patchPath = "options.jumpHost"
	patchAgentForward         patchPath = "options.agentForward"
	patchDesiredMode          patchPath = "options.desiredMode"
	patchRelayConsent         patchPath = "options.relayConsent"
	patchPortDiscovery        patchPath = "options.portDiscovery"
	patchForwards             patchPath = "options.forwards"
	patchCanBeJumpServer      patchPath = "options.canBeJumpServer"
	patchBehaviorOnSessionEnd patchPath = "options.behaviorOnSessionEnd"
)

// PatchPathAllowed reports whether a path may be named by profiles.patch.
//
// The allowlist lives here, beside the mutators it guards, and is exported so
// the transport asks rather than keeps its own copy. Two lists that must agree
// are two lists that will eventually not.
func PatchPathAllowed(path string) bool {
	return allowedPatchPaths()[patchPath(path)]
}

// allowedPatchPaths returns the set of valid patch paths.
func allowedPatchPaths() map[patchPath]bool {
	return map[patchPath]bool{
		patchPort:                 true,
		patchUser:                 true,
		patchAuth:                 true,
		patchPasswordSecret:       true,
		patchKeySecret:            true,
		patchKeyPassphraseSecret:  true,
		patchKeepaliveInterval:    true,
		patchKeepaliveCountMax:    true,
		patchReadyTimeout:         true,
		patchJumpHost:             true,
		patchAgentForward:         true,
		patchDesiredMode:          true,
		patchRelayConsent:         true,
		patchPortDiscovery:        true,
		patchForwards:             true,
		patchCanBeJumpServer:      true,
		patchBehaviorOnSessionEnd: true,
	}
}

// ApplyPatchSet applies a set operation to stored options, returning true if
// the field was recognized. Accepted path values: string, float64 (from JSON
// number), bool.
func ApplyPatchSet(opts *StoredSSHProfileOptions, path string, value any) bool {
	pp := patchPath(path)
	switch pp {
	case patchPort:
		v := toInt(value)
		opts.Port = &v
	case patchUser:
		v := toString(value)
		opts.User = &v
	case patchAuth:
		v := toString(value)
		am := AuthMode(v)
		opts.Auth = &am
	case patchPasswordSecret:
		v := toString(value)
		opts.PasswordSecret = v
	case patchKeySecret:
		v := toString(value)
		opts.KeySecret = v
	case patchKeyPassphraseSecret:
		v := toString(value)
		opts.KeyPassphraseSecret = v
	case patchKeepaliveInterval:
		v := toInt(value)
		opts.KeepaliveInterval = &v
	case patchKeepaliveCountMax:
		v := toInt(value)
		opts.KeepaliveCountMax = &v
	case patchReadyTimeout:
		v := toInt(value)
		opts.ReadyTimeout = &v
	case patchJumpHost:
		v := toString(value)
		opts.JumpHost = &v
	case patchAgentForward:
		v := toBool(value)
		opts.AgentForward = &v
	case patchDesiredMode:
		v := toString(value)
		mode := DesiredMode(v)
		opts.DesiredMode = &mode
	case patchRelayConsent:
		v := toString(value)
		c := RelayConsent(v)
		opts.RelayConsent = &c
	case patchPortDiscovery:
		v := toString(value)
		pd := PortDiscoveryMode(v)
		opts.PortDiscovery = &pd
	case patchForwards:
		fwds, ok := toForwardSpecs(value)
		if !ok {
			return false
		}
		opts.Forwards = &fwds
	case patchCanBeJumpServer:
		v := toBool(value)
		opts.CanBeJumpServer = &v
	case patchBehaviorOnSessionEnd:
		v := toString(value)
		bh := BehaviorOnSessionEnd(v)
		opts.BehaviorOnSessionEnd = &bh
	default:
		return false
	}
	return true
}

// ApplyPatchUnset applies an unset operation to stored options, setting the
// field to nil (inherit). Returns true if the field was recognized.
func ApplyPatchUnset(opts *StoredSSHProfileOptions, path string) bool {
	pp := patchPath(path)
	switch pp {
	case patchPort:
		opts.Port = nil
	case patchUser:
		opts.User = nil
	case patchAuth:
		opts.Auth = nil
	case patchPasswordSecret:
		opts.PasswordSecret = ""
	case patchKeySecret:
		opts.KeySecret = ""
	case patchKeyPassphraseSecret:
		opts.KeyPassphraseSecret = ""
	case patchKeepaliveInterval:
		opts.KeepaliveInterval = nil
	case patchKeepaliveCountMax:
		opts.KeepaliveCountMax = nil
	case patchReadyTimeout:
		opts.ReadyTimeout = nil
	case patchJumpHost:
		opts.JumpHost = nil
	case patchAgentForward:
		opts.AgentForward = nil
	case patchDesiredMode:
		opts.DesiredMode = nil
	case patchRelayConsent:
		opts.RelayConsent = nil
	case patchPortDiscovery:
		opts.PortDiscovery = nil
	case patchForwards:
		opts.Forwards = nil
	case patchCanBeJumpServer:
		opts.CanBeJumpServer = nil
	case patchBehaviorOnSessionEnd:
		opts.BehaviorOnSessionEnd = nil
	default:
		return false
	}
	return true
}

// toForwardSpecs decodes the JSON-decoded value of options.forwards — an
// []any of map[string]any — into []ForwardSpec. The decode is strict on
// purpose: a malformed row (wrong type, unknown key, invalid direction or
// destination) is rejected whole, because a silently dropped patch value is
// exactly the failure class that looks like a working field until a user's
// forward stops sticking. ApplyPatchSet returns false and the stored value
// stays untouched.
func toForwardSpecs(v any) ([]ForwardSpec, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	fs := make([]ForwardSpec, 0, len(arr))
	for _, row := range arr {
		m, ok := row.(map[string]any)
		if !ok {
			return nil, false
		}
		var f ForwardSpec
		for k, raw := range m {
			switch k {
			case "direction":
				s, ok := raw.(string)
				if !ok {
					return nil, false
				}
				f.Direction = s
			case "bindHost":
				s, ok := raw.(string)
				if !ok {
					return nil, false
				}
				f.BindHost = s
			case "bindPort":
				n, ok := raw.(float64)
				if !ok {
					return nil, false
				}
				f.BindPort = int(n)
			case "destination":
				s, ok := raw.(string)
				if !ok {
					return nil, false
				}
				f.Destination = s
			default:
				return nil, false
			}
		}
		fs = append(fs, f)
	}
	if err := ValidForwards(fs); err != nil {
		return nil, false
	}
	return fs, true
}

// toInt converts a JSON number (float64) or int to int.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// toString converts a JSON string to string.
func toString(v any) string {
	s, _ := v.(string)
	return s
}

// toBool converts a JSON bool to bool.
func toBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// Ptr returns a pointer to v for constructing StoredSSHProfileOptions fields.
// Example: Ptr(2222) returns *int(2222); Ptr("bob") returns *string("bob").
func Ptr[T any](v T) *T { return &v }
