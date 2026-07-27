// Package settings implements the typed settings registry that replaces
// internal/config's untyped Get/Set bag. Every setting is a typed declaration
// carrying key, default, label, description, section, validation, data class
// and control kind, exposed through a non-generic Descriptor interface for
// enumeration and typed keys for get/set.
//
// Adding a setting requires touching exactly one declaration site: a
// package-level var created via MustRegisterBool, MustRegisterString, etc.
// The declaration auto-registers; there is no separate switch, serializer,
// or enum entry.
//
// ADR-0011 §2: secret-class settings expose set, delete, and exists —
// never get. Secret values live in credential.SecretStore behind an opaque
// SecretID; the document carries only the reference. No registry method
// returns plaintext for a secret-class key.
//
// ADR-0011 §6: the settings document owns its own monotonic schema version
// through the shared storage.Module protocol, independent of every other
// module's version.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
)

// ── Data classification and control kinds ──────────────────────────────

// DataClass drives the generated UI, export eligibility and log handling.
// It does NOT route storage (ADR-0011 §3).
type DataClass string

const (
	PublicConfig        DataClass = "publicConfig"
	PrivateMetadata     DataClass = "privateMetadata"
	PrivateContent      DataClass = "privateContent"
	SecretAuthenticator DataClass = "secretAuthenticator"
)

// ControlKind describes the UI control to render for a setting.
type ControlKind string

const (
	ControlToggle ControlKind = "toggle"
	ControlText   ControlKind = "text"
	ControlNumber ControlKind = "number"
	ControlSelect ControlKind = "select"
	ControlSecret ControlKind = "secret"
)

// ── Errors ──────────────────────────────────────────────────────────────

// ErrValidation is a typed error returned when a setting value fails its
// declaration's validation rules. The transport layer maps it to a JSON-RPC
// error.
var ErrValidation = errors.New("settings: validation failed")

// ValidationError wraps ErrValidation with context.
type ValidationError struct {
	SettingKey string
	Value      any
	Message    string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("settings: %q validation failed: %s", e.SettingKey, e.Message)
}

func (e *ValidationError) Unwrap() error { return ErrValidation }

// ── Select option ──────────────────────────────────────────────────────

// SelectOption is a single choice in a select control.
type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ── Descriptor — non-generic interface for enumeration ─────────────────

// Descriptor is the non-generic interface every setting type implements.
// It exposes all declaration metadata for enumeration (settings.describe)
// without requiring callers to know the concrete type.
type Descriptor interface {
	Key() string
	Section() string
	Label() string
	Description() string
	Control() ControlKind
	DataClass() DataClass
	Default() any // nil for secrets
	Options() []SelectOption
	Min() *float64
	Max() *float64
}

// ── Wire declaration ───────────────────────────────────────────────────

// Declaration is the JSON-RPC wire shape matching the frozen contract at
// .internal/plans/settings-rpc-contract.md.
type Declaration struct {
	Key         string         `json:"key"`
	Section     string         `json:"section"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Control     ControlKind    `json:"control"`
	DataClass   DataClass      `json:"dataClass"`
	Default     any            `json:"default,omitempty"`
	Options     []SelectOption `json:"options,omitempty"`
	Min         *float64       `json:"min,omitempty"`
	Max         *float64       `json:"max,omitempty"`
}

// ── Typed setting spec types ───────────────────────────────────────────

// BoolSpec is the declaration site for a boolean toggle setting.
type BoolSpec struct {
	Key         string
	Section     string
	Label       string
	Description string
	DataClass   DataClass
	Default     bool
}

// StringSpec is the declaration site for a text-input setting.
type StringSpec struct {
	Key         string
	Section     string
	Label       string
	Description string
	DataClass   DataClass
	Default     string
}

// NumberSpec is the declaration site for a numeric setting.
type NumberSpec struct {
	Key         string
	Section     string
	Label       string
	Description string
	DataClass   DataClass
	Default     float64
	Min         *float64
	Max         *float64
}

// SelectSpec is the declaration site for a dropdown setting.
type SelectSpec struct {
	Key         string
	Section     string
	Label       string
	Description string
	DataClass   DataClass
	Default     string
	Options     []SelectOption
}

// SecretSpec is the declaration site for a secret-class setting.
// There is deliberately no Default field — secrets have no default.
type SecretSpec struct {
	Key         string
	Section     string
	Label       string
	Description string
	DataClass   DataClass
}

// ── Typed setting keys (unexported fields, exported via Descriptor) ─────

// Bool is a typed key for a boolean toggle setting.
type Bool struct {
	key         string
	section     string
	label       string
	description string
	dataClass   DataClass
	default_    bool
}

func (b *Bool) Key() string             { return b.key }
func (b *Bool) Section() string         { return b.section }
func (b *Bool) Label() string           { return b.label }
func (b *Bool) Description() string     { return b.description }
func (b *Bool) Control() ControlKind    { return ControlToggle }
func (b *Bool) DataClass() DataClass    { return b.dataClass }
func (b *Bool) Default() any            { return b.default_ }
func (b *Bool) Options() []SelectOption { return nil }
func (b *Bool) Min() *float64           { return nil }
func (b *Bool) Max() *float64           { return nil }

func (b *Bool) toDeclaration() Declaration {
	return Declaration{
		Key:         b.key,
		Section:     b.section,
		Label:       b.label,
		Description: b.description,
		Control:     ControlToggle,
		DataClass:   b.dataClass,
		Default:     b.default_,
	}
}

// String is a typed key for a text-input setting.
type String struct {
	key         string
	section     string
	label       string
	description string
	dataClass   DataClass
	default_    string
}

func (s *String) Key() string             { return s.key }
func (s *String) Section() string         { return s.section }
func (s *String) Label() string           { return s.label }
func (s *String) Description() string     { return s.description }
func (s *String) Control() ControlKind    { return ControlText }
func (s *String) DataClass() DataClass    { return s.dataClass }
func (s *String) Default() any            { return s.default_ }
func (s *String) Options() []SelectOption { return nil }
func (s *String) Min() *float64           { return nil }
func (s *String) Max() *float64           { return nil }

func (s *String) toDeclaration() Declaration {
	return Declaration{
		Key:         s.key,
		Section:     s.section,
		Label:       s.label,
		Description: s.description,
		Control:     ControlText,
		DataClass:   s.dataClass,
		Default:     s.default_,
	}
}

// Number is a typed key for a numeric setting with optional min/max.
type Number struct {
	key         string
	section     string
	label       string
	description string
	dataClass   DataClass
	default_    float64
	min         *float64
	max         *float64
}

func (n *Number) Key() string             { return n.key }
func (n *Number) Section() string         { return n.section }
func (n *Number) Label() string           { return n.label }
func (n *Number) Description() string     { return n.description }
func (n *Number) Control() ControlKind    { return ControlNumber }
func (n *Number) DataClass() DataClass    { return n.dataClass }
func (n *Number) Default() any            { return n.default_ }
func (n *Number) Options() []SelectOption { return nil }
func (n *Number) Min() *float64           { return n.min }
func (n *Number) Max() *float64           { return n.max }

func (n *Number) toDeclaration() Declaration {
	return Declaration{
		Key:         n.key,
		Section:     n.section,
		Label:       n.label,
		Description: n.description,
		Control:     ControlNumber,
		DataClass:   n.dataClass,
		Default:     n.default_,
		Min:         n.min,
		Max:         n.max,
	}
}

// Select is a typed key for a dropdown setting.
type Select struct {
	key         string
	section     string
	label       string
	description string
	dataClass   DataClass
	default_    string
	options     []SelectOption
}

func (s *Select) Key() string             { return s.key }
func (s *Select) Section() string         { return s.section }
func (s *Select) Label() string           { return s.label }
func (s *Select) Description() string     { return s.description }
func (s *Select) Control() ControlKind    { return ControlSelect }
func (s *Select) DataClass() DataClass    { return s.dataClass }
func (s *Select) Default() any            { return s.default_ }
func (s *Select) Options() []SelectOption { return s.options }
func (s *Select) Min() *float64           { return nil }
func (s *Select) Max() *float64           { return nil }

func (s *Select) toDeclaration() Declaration {
	return Declaration{
		Key:         s.key,
		Section:     s.section,
		Label:       s.label,
		Description: s.description,
		Control:     ControlSelect,
		DataClass:   s.dataClass,
		Default:     s.default_,
		Options:     s.options,
	}
}

// Secret is a typed key for a secret-class setting: set, delete, exists —
// never get. There is deliberately no Default.
type Secret struct {
	key         string
	section     string
	label       string
	description string
	dataClass   DataClass
}

func (s *Secret) Key() string             { return s.key }
func (s *Secret) Section() string         { return s.section }
func (s *Secret) Label() string           { return s.label }
func (s *Secret) Description() string     { return s.description }
func (s *Secret) Control() ControlKind    { return ControlSecret }
func (s *Secret) DataClass() DataClass    { return s.dataClass }
func (s *Secret) Default() any            { return nil }
func (s *Secret) Options() []SelectOption { return nil }
func (s *Secret) Min() *float64           { return nil }
func (s *Secret) Max() *float64           { return nil }

func (s *Secret) toDeclaration() Declaration {
	return Declaration{
		Key:         s.key,
		Section:     s.section,
		Label:       s.label,
		Description: s.description,
		Control:     ControlSecret,
		DataClass:   s.dataClass,
	}
}

// ── Declaration auto-registration ──────────────────────────────────────

// allDecls holds every declared setting, populated at package init time
// via MustRegister* calls. Read-only after init.
var allDecls []Descriptor

// Descriptors returns every declared setting. This is the enumeration
// backing settings.describe; the UI renders from this list and contains
// no hand-maintained list of settings.
func Descriptors() []Descriptor {
	return allDecls
}

// MustRegisterBool declares a bool setting and registers it for enumeration.
// Panics on an empty or duplicate key. Returns the typed key for get/set.
func MustRegisterBool(spec BoolSpec) *Bool {
	b := &Bool{
		key:         spec.Key,
		section:     spec.Section,
		label:       spec.Label,
		description: spec.Description,
		dataClass:   spec.DataClass,
		default_:    spec.Default,
	}
	assertValidKey(b.key)
	allDecls = append(allDecls, b)
	return b
}

// MustRegisterString declares a string setting and registers it.
func MustRegisterString(spec StringSpec) *String {
	s := &String{
		key:         spec.Key,
		section:     spec.Section,
		label:       spec.Label,
		description: spec.Description,
		dataClass:   spec.DataClass,
		default_:    spec.Default,
	}
	assertValidKey(s.key)
	allDecls = append(allDecls, s)
	return s
}

// MustRegisterNumber declares a number setting and registers it.
func MustRegisterNumber(spec NumberSpec) *Number {
	n := &Number{
		key:         spec.Key,
		section:     spec.Section,
		label:       spec.Label,
		description: spec.Description,
		dataClass:   spec.DataClass,
		default_:    spec.Default,
		min:         spec.Min,
		max:         spec.Max,
	}
	assertValidKey(n.key)
	allDecls = append(allDecls, n)
	return n
}

// MustRegisterSelect declares a select setting and registers it.
func MustRegisterSelect(spec SelectSpec) *Select {
	if len(spec.Options) == 0 {
		panic("settings: Select " + spec.Key + " must have at least one option")
	}
	s := &Select{
		key:         spec.Key,
		section:     spec.Section,
		label:       spec.Label,
		description: spec.Description,
		dataClass:   spec.DataClass,
		default_:    spec.Default,
		options:     spec.Options,
	}
	assertValidKey(s.key)
	allDecls = append(allDecls, s)
	return s
}

// MustRegisterSecret declares a secret-class setting and registers it.
func MustRegisterSecret(spec SecretSpec) *Secret {
	s := &Secret{
		key:         spec.Key,
		section:     spec.Section,
		label:       spec.Label,
		description: spec.Description,
		dataClass:   spec.DataClass,
	}
	assertValidKey(s.key)
	allDecls = append(allDecls, s)
	return s
}

func assertValidKey(key string) {
	if key == "" {
		panic("settings: key must not be empty")
	}
	for _, d := range allDecls {
		if d.Key() == key {
			panic("settings: duplicate key " + key)
		}
	}
}

// ── Declared settings ──────────────────────────────────────────────────

// ClipboardOSC52Suppressed persists the "Don't show again" decision on the
// OSC 52 clipboard permission banner. Currently in-memory only
// (ClipboardGate._suppressed in frontend/src/clipboard.ts); making it
// stick across restarts is a stated acceptance criterion of bead nocx-9m5.
var ClipboardOSC52Suppressed = MustRegisterBool(BoolSpec{
	Key:         "clipboard.osc52Suppressed",
	Section:     "Clipboard",
	Label:       "Suppress OSC 52 clipboard warnings",
	Description: "When enabled, the permission banner for OSC 52 clipboard writes is permanently suppressed. The clipboard stays protected — this only hides the repeated prompt.",
	DataClass:   PrivateMetadata,
	Default:     false,
})

// TabPlacement controls whether tabs render as a horizontal bar at the top
// of the window or as a vertical list on the side. Horizontal is the default;
// vertical matches the layout of the nocx-d3q epic.
var TabPlacement = MustRegisterSelect(SelectSpec{
	Key:         "tab.placement",
	Section:     "Interface",
	Label:       "Tab placement",
	Description: "Horizontal displays tabs across the top of the window, as in a traditional terminal. Vertical lists tabs on the side, as in an IDE.",
	DataClass:   PublicConfig,
	Default:     "horizontal",
	Options: []SelectOption{
		{Value: "horizontal", Label: "Horizontal"},
		{Value: "vertical", Label: "Vertical"},
	},
})

// UITheme controls which colour theme the UI and terminals use.
// The frontend resolves it to a theme file matching the id; adding a new theme
// requires a new theme CSS file and an option here.
var UITheme = MustRegisterSelect(SelectSpec{
	Key:         "ui.theme",
	Section:     "Interface",
	Label:       "Theme",
	Description: "The colour theme applied to the UI and terminal panes. Changing the theme repaints all open terminals in place without restarting them.",
	DataClass:   PublicConfig,
	Default:     "tokyo-night",
	Options: []SelectOption{
		{Value: "tokyo-night", Label: "Tokyo Night"},
		{Value: "light", Label: "Light"},
	},
})

// ── Document shape ─────────────────────────────────────────────────────

const settingsDocName = "settings.json"

// settingsSchemaVersion is this module's current schema version.
const settingsSchemaVersion storage.SchemaVersion = 1

// settingsModule is the storage.Module for the settings document.
var settingsModule = storage.Module{
	Name:    "settings",
	Current: settingsSchemaVersion,
}

// settingsDoc is the on-disk shape of the settings document.
type settingsDoc struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Values        map[string]interface{} `json:"values"`
	SecretRefs    map[string]string      `json:"secretRefs"`
}

// ── Registry ───────────────────────────────────────────────────────────

// ChangeNotifier is called after a successful mutation with the new revision
// and the keys that changed. The callback receives a copy of the keys slice.
// It is invoked after the registry lock is released so it can safely call
// back into the registry without deadlocking.
type ChangeNotifier func(revision int, keys []string)

// Registry holds the runtime state of all settings: persisted values
// (non-secret) in a DocumentStore and secret values in a SecretStore.
type Registry struct {
	mu       sync.Mutex
	doc      storage.DocumentStore
	secrets  credential.SecretStore
	values   map[string]any
	refs     map[string]credential.SecretID // secret key → SecretID
	revision int                            // monotonic, in-memory, bumps only on successful mutation
	notifier ChangeNotifier
}

// SettingsSnapshot is the wire contract for settings.getSnapshot.
// Secret-class keys are absent from both Values and Overridden.
type SettingsSnapshot struct {
	Values     map[string]any `json:"values"`
	Overridden []string       `json:"overridden"`
	Revision   int            `json:"revision"`
}

// New creates a Registry backed by doc for non-secret values and secrets
// for secret-class values. It loads any previously persisted state.
func New(doc storage.DocumentStore, secrets credential.SecretStore) *Registry {
	r := &Registry{
		doc:     doc,
		secrets: secrets,
		values:  make(map[string]any),
		refs:    make(map[string]credential.SecretID),
	}

	var stored settingsDoc
	found, err := doc.Read(settingsDocName, &stored)
	if err != nil {
		slog.Warn("settings: failed to read persisted state, starting fresh", "error", err)
		return r
	}
	if !found {
		return r
	}

	// Migrate on read if needed.
	raw, err := json.Marshal(stored)
	if err == nil {
		migrated, migrateErr := settingsModule.Migrate(raw, storage.SchemaVersion(stored.SchemaVersion))
		if migrateErr != nil {
			slog.Warn("settings: migration failed, starting fresh", "error", migrateErr)
			return r
		}
		var migratedDoc settingsDoc
		if err := json.Unmarshal(migrated, &migratedDoc); err == nil {
			stored = migratedDoc
		}
	}

	for k, v := range stored.Values {
		if descriptorByKey(k) != nil {
			r.values[k] = v
		}
	}
	for k, id := range stored.SecretRefs {
		if descriptorByKey(k) != nil {
			r.refs[k] = credential.SecretID(id)
		}
	}
	return r
}

// SetNotifier registers a callback invoked after every successful mutation.
// Must be called before the registry is used concurrently (setup only).
func (r *Registry) SetNotifier(n ChangeNotifier) {
	r.notifier = n
}

func descriptorByKey(key string) Descriptor {
	for _, d := range allDecls {
		if d.Key() == key {
			return d
		}
	}
	return nil
}

// ── Enumeration ────────────────────────────────────────────────────────

// Descriptors returns every declared setting descriptor.
func (r *Registry) Descriptors() []Descriptor {
	return allDecls
}

// Declarations returns the wire-ready Declaration list for settings.describe.
func (r *Registry) Declarations() []Declaration {
	decls := make([]Declaration, len(allDecls))
	for i, d := range allDecls {
		decls[i] = descriptorToDeclaration(d)
	}
	return decls
}

func descriptorToDeclaration(d Descriptor) Declaration {
	switch t := d.(type) {
	case *Bool:
		return t.toDeclaration()
	case *String:
		return t.toDeclaration()
	case *Number:
		return t.toDeclaration()
	case *Select:
		return t.toDeclaration()
	case *Secret:
		return t.toDeclaration()
	default:
		return Declaration{
			Key:         d.Key(),
			Section:     d.Section(),
			Label:       d.Label(),
			Description: d.Description(),
			Control:     d.Control(),
			DataClass:   d.DataClass(),
		}
	}
}

// ── Non-secret get/set ─────────────────────────────────────────────────

// GetBool returns the current value of a bool setting, or its default.
func (r *Registry) GetBool(b *Bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.values[b.key]; ok {
		bv, isBool := v.(bool)
		if !isBool {
			return b.default_, nil
		}
		return bv, nil
	}
	return b.default_, nil
}

// SetBool persists a bool setting value.
func (r *Registry) SetBool(b *Bool, value bool) error {
	r.mu.Lock()
	newValues := copyValues(r.values)
	newValues[b.key] = value
	ch, err := r.commitLocked(newValues, r.refs, []string{b.key})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// GetString returns the current value of a string setting, or its default.
func (r *Registry) GetString(s *String) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.values[s.key]; ok {
		str, isStr := v.(string)
		if !isStr {
			return s.default_, nil
		}
		return str, nil
	}
	return s.default_, nil
}

// SetString validates and persists a string setting. Rejects empty strings
// for settings that have a non-empty default.
func (r *Registry) SetString(s *String, value string) error {
	r.mu.Lock()
	if value == "" && s.default_ != "" {
		r.mu.Unlock()
		return &ValidationError{SettingKey: s.key, Value: value, Message: "value must not be empty"}
	}
	newValues := copyValues(r.values)
	newValues[s.key] = value
	ch, err := r.commitLocked(newValues, r.refs, []string{s.key})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// GetNumber returns the current value of a number setting, or its default.
func (r *Registry) GetNumber(n *Number) (float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.values[n.key]; ok {
		f, isNum := toFloat64(v)
		if !isNum {
			return n.default_, nil
		}
		return f, nil
	}
	return n.default_, nil
}

// SetNumber validates and persists a number setting.
func (r *Registry) SetNumber(n *Number, value float64) error {
	r.mu.Lock()
	if n.min != nil && value < *n.min {
		r.mu.Unlock()
		return &ValidationError{
			SettingKey: n.key, Value: value,
			Message: fmt.Sprintf("value %v below minimum %v", value, *n.min),
		}
	}
	if n.max != nil && value > *n.max {
		r.mu.Unlock()
		return &ValidationError{
			SettingKey: n.key, Value: value,
			Message: fmt.Sprintf("value %v above maximum %v", value, *n.max),
		}
	}
	newValues := copyValues(r.values)
	newValues[n.key] = value
	ch, err := r.commitLocked(newValues, r.refs, []string{n.key})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// GetSelect returns the current value of a select setting, or its default.
func (r *Registry) GetSelect(s *Select) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.values[s.key]; ok {
		str, isStr := v.(string)
		if !isStr {
			return s.default_, nil
		}
		return str, nil
	}
	return s.default_, nil
}

// SetSelect validates and persists a select setting.
func (r *Registry) SetSelect(s *Select, value string) error {
	r.mu.Lock()
	for _, opt := range s.options {
		if opt.Value == value {
			newValues := copyValues(r.values)
			newValues[s.key] = value
			ch, err := r.commitLocked(newValues, r.refs, []string{s.key})
			r.mu.Unlock()
			if err == nil {
				r.finishCommit(ch)
			}
			return err
		}
	}
	r.mu.Unlock()
	return &ValidationError{
		SettingKey: s.key, Value: value,
		Message: fmt.Sprintf("value %q is not a valid option", value),
	}
}

// ── getSnapshot ─────────────────────────────────────────────────────────

// GetSnapshot returns the current snapshot of all non-secret settings:
// effective values, which keys have stored overrides, and the current
// revision. Secret-class keys are absent from both Values and Overridden.
func (r *Registry) GetSnapshot() (SettingsSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make(map[string]any, len(allDecls))
	var overridden []string
	for _, d := range allDecls {
		if d.Control() == ControlSecret {
			continue
		}
		if v, ok := r.values[d.Key()]; ok {
			values[d.Key()] = v
			overridden = append(overridden, d.Key())
		} else {
			values[d.Key()] = d.Default()
		}
	}
	if overridden == nil {
		overridden = []string{}
	}
	return SettingsSnapshot{
		Values:     values,
		Overridden: overridden,
		Revision:   r.revision,
	}, nil
}

// ── Reset ──────────────────────────────────────────────────────────────

// Reset restores a setting to its declared default. Secret-class settings
// cannot be reset — they have no default.
func (r *Registry) Reset(d Descriptor) error {
	r.mu.Lock()
	if d.Control() == ControlSecret {
		r.mu.Unlock()
		return &ValidationError{SettingKey: d.Key(), Message: "cannot reset a secret-class setting"}
	}
	newValues := copyValues(r.values)
	delete(newValues, d.Key())
	ch, err := r.commitLocked(newValues, r.refs, []string{d.Key()})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// ── Secret-class methods ───────────────────────────────────────────────

// SecretSet stores a secret-class setting value in the SecretStore. Mints
// an opaque SecretID on first set and persists only the reference in the
// settings document — the plaintext never leaves the SecretStore.
func (r *Registry) SecretSet(s *Secret, value string) error {
	r.mu.Lock()
	id, ok := r.refs[s.key]
	if !ok {
		id = credential.NewSecretID()
	}
	secret := credential.NewSecret(value)
	if err := r.secrets.Set(id, secret); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("settings: secret set %q: %w", s.key, err)
	}
	newRefs := copyRefs(r.refs)
	newRefs[s.key] = id
	ch, err := r.commitLocked(r.values, newRefs, []string{s.key})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// SecretDelete removes a secret-class setting value from the SecretStore
// and drops the reference from the settings document. Best-effort on the
// keychain delete — an orphaned keychain entry is safer than a dangling ref.
func (r *Registry) SecretDelete(s *Secret) error {
	r.mu.Lock()
	refID, hasRef := r.refs[s.key]
	if !hasRef {
		r.mu.Unlock()
		return nil
	}
	if err := r.secrets.Delete(refID); err != nil {
		slog.Warn("settings: secret delete failed (may be orphaned)", "key", s.key, "error", err)
	}
	newRefs := copyRefs(r.refs)
	delete(newRefs, s.key)
	ch, err := r.commitLocked(r.values, newRefs, []string{s.key})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// SecretExists reports whether a secret-class setting has a stored value.
func (r *Registry) SecretExists(s *Secret) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.refs[s.key]
	if !ok {
		return false, nil
	}
	return r.secrets.Exists(id)
}

// ── Persistence ────────────────────────────────────────────────────────

// change captures notification state from a successful commit. The caller
// MUST release r.mu and then call finishCommit.
type change struct {
	rev      int
	notifier ChangeNotifier
	keys     []string
}

// commitLocked persists values and refs, commits the new state, and bumps
// revision. Caller MUST hold r.mu. Returns a change record for notification;
// caller MUST release r.mu before calling finishCommit.
func (r *Registry) commitLocked(values map[string]any, refs map[string]credential.SecretID, keys []string) (change, error) {
	if err := r.writeDoc(values, refs); err != nil {
		return change{}, err
	}
	r.values = values
	r.refs = refs
	r.revision++
	ck := make([]string, len(keys))
	copy(ck, keys)
	return change{rev: r.revision, notifier: r.notifier, keys: ck}, nil
}

// finishCommit invokes the notifier if one is set. Must be called after r.mu
// is released.
func (r *Registry) finishCommit(ch change) {
	if ch.notifier != nil {
		ch.notifier(ch.rev, ch.keys)
	}
}

// writeDoc serialises and writes the settings document without mutating
// the registry. Caller MUST hold r.mu.
func (r *Registry) writeDoc(values map[string]any, refs map[string]credential.SecretID) error {
	doc := settingsDoc{
		SchemaVersion: int(settingsSchemaVersion),
		Values:        values,
		SecretRefs:    make(map[string]string, len(refs)),
	}
	for k, id := range refs {
		doc.SecretRefs[k] = string(id)
	}
	return r.doc.Write(settingsDocName, doc)
}

// ── Helpers ────────────────────────────────────────────────────────────

func copyValues(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyRefs(src map[string]credential.SecretID) map[string]credential.SecretID {
	dst := make(map[string]credential.SecretID, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
