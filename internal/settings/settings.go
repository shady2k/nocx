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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

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
	ControlPaths  ControlKind = "paths"
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
	Unit        string         `json:"unit,omitempty"`
	ZeroLabel   string         `json:"zeroLabel,omitempty"`
}

// ── Group catalogue ────────────────────────────────────────────────────

// SettingsGroup is one group in the settings rail's catalogue: a titled,
// ordered bucket that rail pages are sorted into. The catalogue is declared
// in Go and ships beside the declarations in settings.describe — the
// settings rail renders from it and contains no lookup table of its own
// (design spec 2026-07-26 "Bucket 2": nested categories arrive as
// declaration schema, never as frontend exceptions).
//
// A group carries no settings. Membership is per SECTION (SectionGroups),
// never per declaration: a per-declaration group field would let two
// declarations in one section name different groups — a contradiction the
// type would permit, so the type does not have the field.
//
// The wire type IS the declaration site — there is no separate GroupSpec.
// The Bool/String/Number/Select specs exist because their spec shape differs
// from the wire shape (bounds, data class, defaults are transformed in
// toDeclaration); a group has none of that, so a tag-less twin would
// duplicate (id, title, order) with only the json tags as the difference.
//
// Reading the catalogue and the mapping goes through the Registry methods
// (Groups, SectionGroups) — the path capability/config.go takes to the wire.
// There are no package-level read accessors: a second reader of one fact is
// a second answer waiting to drift (the deadcode ratchet caught exactly
// that). Registration is package-level, like MustRegister*; reading is not.
type SettingsGroup struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Order int    `json:"order"`
}

// groupCatalogue is the declared rail group catalogue, in registration
// order. Initialised non-nil so an empty catalogue marshals as [] rather
// than null on the wire.
var groupCatalogue = []SettingsGroup{}

// groupIndex maps a group id to its position in groupCatalogue.
var groupIndex = map[string]int{}

// sectionGroups maps a section name to the group id it belongs to — the
// "which group each section belongs to" half of the catalogue. One section,
// one group: keyed by section, never by declaration.
var sectionGroups = map[string]string{}

// RegisterGroup declares a rail group and appends it to the catalogue.
// Panics on an empty or duplicate id. Called at package init, beside the
// setting declarations.
func RegisterGroup(group SettingsGroup) {
	if group.ID == "" {
		panic("settings: group id must not be empty")
	}
	if _, dup := groupIndex[group.ID]; dup {
		panic("settings: duplicate group id " + group.ID)
	}
	groupIndex[group.ID] = len(groupCatalogue)
	groupCatalogue = append(groupCatalogue, group)
}

// RegisterSectionGroup places a section under a declared group. This is the
// additive "adding a section to a group" change: one Go-side call, and the
// settings.describe payload carries it with no frontend edit. Panics when
// the group is not declared — a mapping must never name a group the
// catalogue lacks — or when the section already belongs to a group.
func RegisterSectionGroup(section, groupID string) {
	if _, ok := groupIndex[groupID]; !ok {
		panic("settings: section " + strconv.Quote(section) + " names undeclared group " + strconv.Quote(groupID))
	}
	if _, taken := sectionGroups[section]; taken {
		panic("settings: section " + strconv.Quote(section) + " already belongs to a group")
	}
	sectionGroups[section] = groupID
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
	// Unit is what the number is measured in ("days", "MiB"). Declared here,
	// beside Min/Max, so every consumer — the settings screen, an export, a
	// future CLI — renders the same suffix. The unit never lives in prose:
	// a description says what the setting means, not what the number counts.
	Unit string
	// ZeroLabel is what the value 0 MEANS when 0 is a sentinel rather than a
	// quantity — "Kept until the size limit is reached", not "0 days". A
	// sentinel that is explained only in prose is a sentinel nobody reads:
	// the owner's verdict on `Keep history for = 0` was "совсем неочевидно",
	// and the fourth sentence of the description had been explaining it.
	// Empty means 0 is an ordinary number and needs no explanation.
	ZeroLabel string
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

// PathListSpec is the declaration site for a path-list setting. The value is
// always a canonical list of existing directories; there is no Default field
// because the default is always the empty list.
type PathListSpec struct {
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
	unit        string
	zeroLabel   string
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
		Unit:        n.unit,
		ZeroLabel:   n.zeroLabel,
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

// PathList is a typed key for a path-list setting. The stored and wire value
// is a canonical []string of existing directories.
type PathList struct {
	key         string
	section     string
	label       string
	description string
	dataClass   DataClass
}

func (p *PathList) Key() string             { return p.key }
func (p *PathList) Section() string         { return p.section }
func (p *PathList) Label() string           { return p.label }
func (p *PathList) Description() string     { return p.description }
func (p *PathList) Control() ControlKind    { return ControlPaths }
func (p *PathList) DataClass() DataClass    { return p.dataClass }
func (p *PathList) Default() any            { return []string{} }
func (p *PathList) Options() []SelectOption { return nil }
func (p *PathList) Min() *float64           { return nil }
func (p *PathList) Max() *float64           { return nil }

func (p *PathList) toDeclaration() Declaration {
	return Declaration{
		Key:         p.key,
		Section:     p.section,
		Label:       p.label,
		Description: p.description,
		Control:     ControlPaths,
		DataClass:   p.dataClass,
		Default:     p.Default(),
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
		unit:        spec.Unit,
		zeroLabel:   spec.ZeroLabel,
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

// MustRegisterPathList declares a path-list setting and registers it.
func MustRegisterPathList(spec PathListSpec) *PathList {
	p := &PathList{
		key:         spec.Key,
		section:     spec.Section,
		label:       spec.Label,
		description: spec.Description,
		dataClass:   spec.DataClass,
	}
	assertValidKey(p.key)
	allDecls = append(allDecls, p)
	return p
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

// fp is a pointer-to-float64 helper for NumberSpec bounds.
func fp(v float64) *float64 { return &v }

// HistoryEnabled is the "keep history at all" decision. Off means the store
// is not written to: a command runs and no row appears.
var HistoryEnabled = MustRegisterBool(BoolSpec{
	Key:         "history.enabled",
	Section:     "History",
	Label:       "Keep command history",
	Description: "Record commands for recall after a restart. When off, new commands are not stored and the recall panel shows only the current session; existing history stays until its retention limit.",
	DataClass:   PublicConfig,
	Default:     true,
})

// HistoryRetentionDays is the age-based retention limit. The label is honest
// by design (internal/content's package doc): ordinary DELETE leaves rows in
// WAL pages and free space, so the wording says "removed from nocx", never
// "securely erased".
var HistoryRetentionDays = MustRegisterNumber(NumberSpec{
	Key:         "history.retentionDays",
	Section:     "History",
	Label:       "Keep history for",
	Description: "How long a completed command is kept. Older commands are removed from nocx — removal is not secure erasure, deleted rows can remain in the database file's free space.",
	DataClass:   PublicConfig,
	Default:     0,
	Min:         fp(0),
	Max:         fp(3650),
	Unit:        "days",
	// The default. Age expiry is opt-in: a terminal that quietly forgets
	// commands after N days surprises you exactly when you are looking for
	// something old, so growth is bounded by the size budget instead. That
	// makes 0 the value most people see, which is precisely why it has to
	// say what it means on the screen rather than in the fourth sentence of
	// the description.
	ZeroLabel: "Kept until the size limit is reached",
})

// HistoryRetentionMiB is the logical retained-content budget (nocx-rtg0.11):
// the number the user reasons about, what eviction acts on. Measured
// amplification is ~3.2x (256 MiB of content ≈ 811 MiB on disk), so the
// label states the real footprint rather than promising it away.
var HistoryRetentionMiB = MustRegisterNumber(NumberSpec{
	Key:         "history.retentionMiB",
	Section:     "History",
	Label:       "Command history size",
	Description: "How much command text to keep. When it is reached the oldest commands are removed from nocx (again, not securely erased). The on-disk footprint is larger than this number: measured ~3.2x — 256 MiB of content is about 811 MiB on disk — because of the search index and encrypted pages.",
	DataClass:   PublicConfig,
	Default:     4096,
	Min:         fp(64),
	Max:         fp(1 << 20),
	Unit:        "MiB",
})

// HistoryDiskCeilingMiB is the physical ceiling over the main database plus
// its WAL — the second number of the two-number budget. It is separate from
// the content size on purpose: deleting content shrinks what you keep, not
// necessarily the file.
var HistoryDiskCeilingMiB = MustRegisterNumber(NumberSpec{
	Key:         "history.diskCeilingMiB",
	Section:     "History",
	Label:       "Disk space limit",
	Description: "Physical ceiling for the history database plus its write-ahead log. A separate number from the content size: deleting content shrinks what you keep, not necessarily the file. When this is reached the store compacts rather than deleting more.",
	DataClass:   PublicConfig,
	Default:     8192,
	Min:         fp(128),
	Max:         fp(2 << 20),
	Unit:        "MiB",
})

// HistoryOutputEnabled is the "keep command output separately" decision —
// a different switch from keeping the commands, because output has very
// different privacy and size consequences. Wired to the store's policy; the
// capture path that produces output is nocx-de7's epic.
var HistoryOutputEnabled = MustRegisterBool(BoolSpec{
	Key:         "history.outputEnabled",
	Section:     "History",
	Label:       "Keep command output",
	Description: "Whether the text commands printed is kept alongside the commands. Output is larger and more sensitive than the command line; commands are kept without output when this is off.",
	DataClass:   PublicConfig,
	Default:     true,
})

// HistoryOutputCapKB bounds how much of ONE command's output is kept.
//
// The head and the tail are kept and the middle is dropped (design §4.3):
// errors live in the tail, the invocation and its first diagnostics in the
// head, and a million lines of progress bar between them are of no value to
// anyone. A cap on BYTES rather than on lines is what bounds the budget
// almost independently of what the user runs — a line cap is generous to a
// program printing long lines and mean to one printing short ones, for no
// reason either of them can see.
//
// It is not the size budget and it is not the age: those are store-wide and
// belong to nocx-rtg0.30's knobs. This one is per command, and it is what
// keeps one `cat` of a large file from spending the whole budget.
var HistoryOutputCapKB = MustRegisterNumber(NumberSpec{
	Key:         "history.outputCapKB",
	Section:     "History",
	Label:       "Keep per command, at most",
	Description: "How much of one command's output is kept. Past this the beginning and the end are kept, the middle is dropped, and the block says so.",
	DataClass:   PublicConfig,
	Default:     256,
	Min:         fp(16),
	Max:         fp(4096),
	Unit:        "KB",
})

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

// RestoreOnStartup decides whether the application reopens on what was left
// (nocx-l21ib): the workspaces, their tabs, the panes with their directories
// and the blocks those panes printed.
//
// It restores a TAB, never a process. The shell died with the backend (D5)
// and a local pane comes back with a fresh one in the same directory; nothing
// here resurrects anything, and nothing in the product may suggest otherwise
// (ADR-0019 §3).
//
// OFF MARKS THE LAST SESSION'S TABS CLOSED, and deletes nothing (the
// composition root's clearWindowOnCleanStart, nocx-l21ib.4). It is still not
// an instruction to forget — every row stays, with every block still anchored
// to the pane that printed it, and a person who wants the history gone has
// the History settings for that. What turning it back on reopens is the LAST
// session, which is the whole reason the sweep exists: while a clean start
// merely left the rows open and unshown, the next launch with the setting
// back on reopened the session BEFORE the clean one, and every clean start
// added another layer to the pile.
//
// In Interface rather than a section of its own: it is a decision about what
// the window looks like when it opens, beside where the tabs are, and a
// section holding one setting is a heading rather than a grouping.
var RestoreOnStartup = MustRegisterBool(BoolSpec{
	Key:         "restore.onStartup",
	Section:     "Interface",
	Label:       "Reopen tabs and panes on startup",
	Description: "Reopen the workspaces, tabs and panes you left, each with the commands it ran. The shells themselves are new: a local pane starts a fresh shell in the same directory, and nothing that was running comes back. Off opens on an empty window: the tabs you had are closed, not deleted, and the commands they ran stay in your history — but they are not waiting for you if you turn this back on.",
	DataClass:   PublicConfig,
	Default:     true,
})

// OutputWrap is the DEFAULT wrap for a command block's output. The per-block
// ⋮ menu override (nocx-ex636) stays what it always was — the exception the
// kind cannot know about — and a block somebody has overridden ignores this
// value entirely; this decides what an untouched block does.
//
// It governs command output only, which is the one kind with a genuine
// choice: an answer is prose and a horizontal scrollbar under prose is a
// defect rather than a preference, and a fenced code block keeps its
// alignment (nocx-juau) either way.
var OutputWrap = MustRegisterBool(BoolSpec{
	Key:         "terminal.wrapOutput",
	Section:     "Interface",
	Label:       "Wrap long output lines",
	Description: "Wrap a command's output at the width of the block instead of scrolling it sideways. Any block can be switched the other way from its own ⋮ menu; this is what a block does until you do.",
	DataClass:   PublicConfig,
	Default:     true,
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
		{Value: "ayu-dark", Label: "Ayu Dark"},
		{Value: "catppuccin-latte", Label: "Catppuccin Latte"},
		{Value: "catppuccin-mocha", Label: "Catppuccin Mocha"},
		{Value: "dracula", Label: "Dracula"},
		{Value: "gruvbox-dark", Label: "Gruvbox Dark"},
		{Value: "nord", Label: "Nord"},
		{Value: "one-dark", Label: "One Dark"},
		{Value: "rose-pine", Label: "Rosé Pine"},
		{Value: "solarized-dark", Label: "Solarized Dark"},
		{Value: "solarized-light", Label: "Solarized Light"},
	},
})

// The sidebar's width used to be registered here as `sidebar.width`. It is
// not a setting and never was: a setting is something a user DELIBERATELY
// CHOOSES, and a width produced by dragging a panel edge is what the app must
// remember without being asked. Registering it put a "Sidebar width" row on
// Settings → Interface reading 206.3828125 px and badged the section
// "Modified" the moment anybody dragged the edge — two symptoms of the one
// wrong owner (nocx-mqie.3). It now lives in internal/uistate, beside the
// window geometry, as a whole number of CSS pixels. See ADR-0033 for the line
// between the two stores, and check a new key against it before adding one
// here.

// ── Declared groups ────────────────────────────────────────────────────
// The settings rail's group catalogue (nocx-dgsp): declared here, shipped
// beside the declarations in settings.describe, rendered by the frontend
// without a lookup table of its own. Component pages (Endpoints, Protection,
// Secrets, Backup & Restore, Connections) name these ids in their registry
// entries in settings.tsx; generated sections arrive through the
// RegisterSectionGroup calls below.
func init() {
	RegisterGroup(SettingsGroup{ID: "assistant", Title: "Assistant", Order: 0})
	RegisterGroup(SettingsGroup{ID: "vault", Title: "Vault", Order: 1})
	RegisterGroup(SettingsGroup{ID: "application", Title: "Application", Order: 2})
	RegisterGroup(SettingsGroup{ID: "developer", Title: "Developer", Order: 3})
	RegisterSectionGroup("Interface", "application")
	RegisterSectionGroup("Clipboard", "application")
	RegisterSectionGroup("History", "application")
	RegisterSectionGroup("Experimental", "developer")
	// Test is the fixture section the test binaries declare settings in; it
	// is grouped here so the rail shows it under Developer in every build
	// that carries it (criterion 7).
	RegisterSectionGroup("Test", "developer")
}

// SandboxEnabled gates the sidebar shield action (ADR-0043).
// It is a capability/visibility gate, not "sandbox every tab": it exposes
// conversion of an eligible active local tab, and the backend rejects a
// sandbox request while the flag is off.
var SandboxEnabled = MustRegisterBool(BoolSpec{
	Key:         "sandbox.enabled",
	Section:     "Experimental",
	Label:       "Filesystem sandbox",
	Description: "Expose the sidebar shield action that converts the active local tab into a filesystem-isolated sandbox (experimental). The action requires a verified current folder; the flag alone never sandboxes anything.",
	DataClass:   PublicConfig,
	Default:     false,
})

// SandboxAllowedWritablePaths is the persisted global baseline of additional
// directories made read-write in each future sandbox conversion (ADR-0037
// §3.1, ADR-0039 §3.1). The workspace is always writable; changes affect
// future conversions only.
var SandboxAllowedWritablePaths = MustRegisterPathList(PathListSpec{
	Key:         "sandbox.allowedWritablePaths",
	Section:     "Experimental",
	Label:       "Sandbox read & write folders",
	Description: "Additional folders available read/write in each future sandbox conversion. A folder strictly below host HOME also appears at its usual ~/… path; HOME and ancestor grants stay absolute-only. Projected folders can contain credentials and receive exactly this read/write authority. The workspace is always read/write; changes affect future conversions only.",
	DataClass:   PrivateMetadata,
})

// SandboxAllowedReadOnlyPaths is the persisted global baseline of additional
// directories made read-only in each future sandbox conversion (ADR-0039
// §3.1): their contents may be read and traversed, never created, removed,
// renamed, or modified. The workspace is always read/write; changes affect
// future conversions only.
var SandboxAllowedReadOnlyPaths = MustRegisterPathList(PathListSpec{
	Key:         "sandbox.allowedReadOnlyPaths",
	Section:     "Experimental",
	Label:       "Sandbox read-only folders",
	Description: "Additional folders available read-only in each future sandbox conversion (their contents may be read, never created, removed, renamed, or modified). A folder strictly below host HOME also appears at its usual ~/… path; HOME and ancestor grants stay absolute-only. Projected folders can contain credentials and remain read-only. The workspace is always read/write; changes affect future conversions only.",
	DataClass:   PrivateMetadata,
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
	mu        sync.Mutex
	doc       storage.DocumentStore
	secrets   credential.SecretStore
	values    map[string]any
	refs      map[string]credential.SecretID // secret key → SecretID
	revision  int                            // monotonic, in-memory, bumps only on successful mutation
	notifiers []ChangeNotifier
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
		d := descriptorByKey(k)
		if d == nil {
			continue
		}
		if d.Control() == ControlPaths {
			// A recognized path list is preserved raw (no re-stat: a
			// directory that disappeared after a valid save stays visible and
			// makes a sandbox launch fail closed). A type-corrupted value is
			// preserved unchanged so it stays observable as an invalid
			// snapshot, never silently coerced to the default (ADR-0037 §3.2).
			r.values[k] = normalizeLoadedPathList(v)
			continue
		}
		r.values[k] = v
	}
	for k, id := range stored.SecretRefs {
		if descriptorByKey(k) != nil {
			r.refs[k] = credential.SecretID(id)
		}
	}
	return r
}

// SetNotifier registers a callback invoked after every successful mutation.
// Multiple listeners are allowed (each mutation invokes all of them); the
// transport uses one for the settings.changed broadcast, and the composition
// root may register another to keep the ContentDB policy in sync.
func (r *Registry) SetNotifier(n ChangeNotifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifiers = append(r.notifiers, n)
}

// AddNotifier is SetNotifier under its honest name: appends a listener.
func (r *Registry) AddNotifier(n ChangeNotifier) {
	r.SetNotifier(n)
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

// Groups returns the rail group catalogue for settings.describe: the
// (id, title, order) catalogue declared by RegisterGroup, in registration
// order.
func (r *Registry) Groups() []SettingsGroup {
	return groupCatalogue
}

// SectionGroups returns the section→group mapping for settings.describe:
// the group a generated rail page belongs to. A section absent from the map
// is ungrouped and renders at top level. The map is copied so a caller can
// never mutate the catalogue through it.
func (r *Registry) SectionGroups() map[string]string {
	m := make(map[string]string, len(sectionGroups))
	for section, gid := range sectionGroups {
		m[section] = gid
	}
	return m
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
	case *PathList:
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

// GetPaths returns the current value of a path-list setting, or its default.
// The returned slice is a fresh copy — mutating it never mutates registry
// state. A stored value that is not a recognized string list is reported as a
// corruption error, never silently coerced to the default (ADR-0037 §3.2).
func (r *Registry) GetPaths(p *PathList) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[p.key]
	if !ok {
		return []string{}, nil
	}
	paths, ok := v.([]string)
	if !ok {
		return nil, &ValidationError{SettingKey: p.key, Message: "stored path list is corrupted"}
	}
	return copyStrings(paths), nil
}

// SetPaths validates, canonicalizes, and persists a path-list setting. The
// entire candidate is validated before one commit: non-empty absolute paths
// with no control runes that resolve to existing directories, at most
// pathListMaxEntries entries, canonical duplicates collapsing first-wins.
func (r *Registry) SetPaths(p *PathList, value []string) error {
	r.mu.Lock()
	canonical, err := canonicalPaths(p.key, value)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	newValues := copyValues(r.values)
	newValues[p.key] = canonical
	if sandboxPathKeys[p.key] {
		if err := checkSandboxPathConflict(p.key, newValues); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	ch, commitErr := r.commitLocked(newValues, r.refs, []string{p.key})
	r.mu.Unlock()
	if commitErr == nil {
		r.finishCommit(ch)
	}
	return commitErr
}

// AppendSandboxPath canonicalizes one directory and appends it to a sandbox
// baseline in the same locked persistence transaction that observes the
// current list. Concurrent Settings edits therefore cannot be lost. Existing
// canonical entries are idempotent and do not advance the revision.
func (r *Registry) AppendSandboxPath(p *PathList, path string) (int, error) {
	if p != SandboxAllowedWritablePaths && p != SandboxAllowedReadOnlyPaths {
		return 0, &ValidationError{SettingKey: p.key, Message: "not a sandbox path baseline"}
	}
	r.mu.Lock()
	canonical, err := canonicalPaths(p.key, []string{path})
	if err != nil {
		r.mu.Unlock()
		return 0, err
	}
	if len(canonical) != 1 {
		r.mu.Unlock()
		return 0, &ValidationError{SettingKey: p.key, Message: "path did not resolve to one directory"}
	}
	existing, ok := r.values[p.key].([]string)
	if !ok && r.values[p.key] != nil {
		r.mu.Unlock()
		return 0, &ValidationError{SettingKey: p.key, Message: "stored path list is corrupted"}
	}
	for _, current := range existing {
		if current == canonical[0] {
			revision := r.revision
			r.mu.Unlock()
			return revision, nil
		}
	}
	if len(existing) >= pathListMaxEntries {
		r.mu.Unlock()
		return 0, &ValidationError{SettingKey: p.key, Message: fmt.Sprintf("at most %d paths are allowed", pathListMaxEntries)}
	}
	newValues := copyValues(r.values)
	newValues[p.key] = append(copyStrings(existing), canonical[0])
	if err := checkSandboxPathConflict(p.key, newValues); err != nil {
		r.mu.Unlock()
		return 0, err
	}
	ch, commitErr := r.commitLocked(newValues, r.refs, []string{p.key})
	revision := r.revision
	r.mu.Unlock()
	if commitErr != nil {
		return 0, commitErr
	}
	r.finishCommit(ch)
	return revision, nil
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
			if d.Control() == ControlPaths {
				values[d.Key()] = copyPathsValue(v)
			} else {
				values[d.Key()] = v
			}
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

// PendingNotification is an opaque notification produced by a successful
// bulk replacement. Publish it only after an external transaction commits.
type PendingNotification struct {
	ch change
}

// NonSecretOverrides returns only persisted overrides for non-secret settings.
func (r *Registry) NonSecretOverrides() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]any, len(r.values))
	for key, value := range r.values {
		d := descriptorByKey(key)
		if d == nil || d.Control() == ControlSecret || d.DataClass() == SecretAuthenticator {
			continue
		}
		if d.Control() == ControlPaths {
			out[key] = copyPathsValue(value)
			continue
		}
		out[key] = value
	}
	return out
}

// ReplaceNonSecretOverrides validates all supplied values, atomically replaces
// eligible overrides, and defers notifications until Publish is called.
func (r *Registry) ReplaceNonSecretOverrides(values map[string]any) (PendingNotification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	newValues := copyValues(r.values)
	typed := make(map[string]any, len(values))
	for key, value := range values {
		d := descriptorByKey(key)
		if d == nil {
			return PendingNotification{}, &ValidationError{SettingKey: key, Message: "unknown setting key"}
		}
		if d.Control() == ControlSecret || d.DataClass() == SecretAuthenticator {
			return PendingNotification{}, &ValidationError{SettingKey: key, Message: "secret-class settings cannot be bulk-replaced"}
		}
		if d.Control() == ControlPaths {
			canonical, err := canonicalPaths(key, value)
			if err != nil {
				return PendingNotification{}, err
			}
			typed[key] = canonical
			continue
		}
		if err := validateValue(d, value); err != nil {
			return PendingNotification{}, err
		}
		typed[key] = value
	}
	var changedKeys []string
	for key, value := range typed {
		existing, had := newValues[key]
		if !had || !reflect.DeepEqual(existing, value) {
			changedKeys = append(changedKeys, key)
		}
		newValues[key] = value
	}
	for key := range newValues {
		d := descriptorByKey(key)
		if d == nil || d.Control() == ControlSecret || d.DataClass() == SecretAuthenticator {
			continue
		}
		if _, kept := typed[key]; !kept {
			changedKeys = append(changedKeys, key)
			delete(newValues, key)
		}
	}
	sort.Strings(changedKeys)
	// Check sandbox cross-class conflicts on the final combined state.
	// The error is keyed to the read-only setting — the one whose value is
	// constrained by the writable setting.
	if _, roChanged := typed[SandboxAllowedReadOnlyPaths.Key()]; roChanged {
		if err := checkSandboxPathConflict(SandboxAllowedReadOnlyPaths.Key(), newValues); err != nil {
			return PendingNotification{}, err
		}
	}
	if _, rwChanged := typed[SandboxAllowedWritablePaths.Key()]; rwChanged {
		if err := checkSandboxPathConflict(SandboxAllowedWritablePaths.Key(), newValues); err != nil {
			return PendingNotification{}, err
		}
	}
	ch, err := r.commitLocked(newValues, r.refs, changedKeys)
	if err != nil {
		return PendingNotification{}, err
	}
	return PendingNotification{ch: ch}, nil
}

// Publish delivers the deferred notification after the external transaction.
func (r *Registry) Publish(n PendingNotification) {
	r.finishCommit(n.ch)
}

// ValidateSetting checks whether a key-value pair would be accepted by the
// registry without persisting anything. It returns an error for unknown,
// secret-class, wrong-typed, object, or array values.
func (r *Registry) ValidateSetting(key string, value any) error {
	d := descriptorByKey(key)
	if d == nil {
		return fmt.Errorf("unknown setting key %q", key)
	}
	if d.Control() == ControlSecret || d.DataClass() == SecretAuthenticator {
		return fmt.Errorf("secret-class setting %q cannot be validated as non-secret", key)
	}
	_, err := coerceValue(d, value)
	return err
}

func validateValue(d Descriptor, value any) error {
	switch d.Control() {
	case ControlToggle:
		if _, ok := value.(bool); !ok {
			return &ValidationError{SettingKey: d.Key(), Message: "expected boolean"}
		}
	case ControlText:
		s, ok := value.(string)
		if !ok {
			return &ValidationError{SettingKey: d.Key(), Message: "expected string"}
		}
		if def, ok := d.Default().(string); ok && def != "" && s == "" {
			return &ValidationError{SettingKey: d.Key(), Message: "cannot be empty"}
		}
	case ControlNumber:
		f, ok := toFloat64(value)
		if !ok {
			return &ValidationError{SettingKey: d.Key(), Message: "expected number"}
		}
		if min := d.Min(); min != nil && f < *min {
			return &ValidationError{SettingKey: d.Key(), Message: fmt.Sprintf("minimum is %v", *min)}
		}
		if max := d.Max(); max != nil && f > *max {
			return &ValidationError{SettingKey: d.Key(), Message: fmt.Sprintf("maximum is %v", *max)}
		}
	case ControlSelect:
		s, ok := value.(string)
		if !ok {
			return &ValidationError{SettingKey: d.Key(), Message: "expected string"}
		}
		for _, option := range d.Options() {
			if option.Value == s {
				return nil
			}
		}
		return &ValidationError{SettingKey: d.Key(), Message: fmt.Sprintf("invalid option %q", s)}
	case ControlPaths:
		if _, err := canonicalPaths(d.Key(), value); err != nil {
			return err
		}
	default:
		return &ValidationError{SettingKey: d.Key(), Message: "unsupported control kind"}
	}
	return nil
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
	if sandboxPathKeys[d.Key()] {
		if err := checkSandboxPathConflict(d.Key(), newValues); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	ch, err := r.commitLocked(newValues, r.refs, []string{d.Key()})
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// ── Import-time restore ────────────────────────────────────────────────

// ApplyValues restores a snapshot of non-secret setting values, validating
// each through its declaration. It is the import-time counterpart of
// GetSnapshot: whatever the snapshot exported, ApplyValues restores, and
// nothing else. Unknown keys and secret-class keys are rejected — a value
// the receiving build cannot restore must not be silently dropped, and
// import never resolves or invents a secret (ADR-0011 §2).
//
// Every value is validated before anything is committed: an invalid value
// leaves the registry unchanged, so a restore cannot half-apply.
func (r *Registry) ApplyValues(values map[string]any) error {
	if len(values) == 0 {
		return nil
	}

	r.mu.Lock()

	newValues := copyValues(r.values)
	var changed []string

	for key, value := range values {
		d := descriptorByKey(key)
		if d == nil {
			r.mu.Unlock()
			return &ValidationError{SettingKey: key, Message: "unknown setting cannot be restored"}
		}
		if d.Control() == ControlSecret {
			r.mu.Unlock()
			return &ValidationError{SettingKey: key, Message: "secret-class setting cannot be restored by import"}
		}
		typed, err := coerceValue(d, value)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		if current, ok := r.values[key]; ok && reflect.DeepEqual(current, typed) {
			continue
		}
		newValues[key] = typed
		changed = append(changed, key)
	}

	if len(changed) == 0 {
		r.mu.Unlock()
		return nil
	}

	// Check sandbox cross-class conflicts on the final combined state.
	if _, roChanged := values[SandboxAllowedReadOnlyPaths.Key()]; roChanged {
		if err := checkSandboxPathConflict(SandboxAllowedReadOnlyPaths.Key(), newValues); err != nil {
			r.mu.Unlock()
			return err
		}
	}
	if _, rwChanged := values[SandboxAllowedWritablePaths.Key()]; rwChanged {
		if err := checkSandboxPathConflict(SandboxAllowedWritablePaths.Key(), newValues); err != nil {
			r.mu.Unlock()
			return err
		}
	}

	ch, err := r.commitLocked(newValues, r.refs, changed)
	r.mu.Unlock()
	if err == nil {
		r.finishCommit(ch)
	}
	return err
}

// coerceValue validates a JSON-decoded snapshot value against a setting's
// declaration and returns the typed value the registry stores. The checks
// mirror the typed setters' — the two paths validate the same values.
func coerceValue(d Descriptor, value any) (any, error) {
	switch d.Control() {
	case ControlToggle:
		b, ok := value.(bool)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Value: value, Message: "expected a boolean"}
		}
		return b, nil
	case ControlText:
		s, ok := value.(string)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Value: value, Message: "expected a string"}
		}
		str, ok := d.(*String)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Message: "declared as text but is not a String key"}
		}
		if s == "" && str.default_ != "" {
			return nil, &ValidationError{SettingKey: d.Key(), Value: s, Message: "value must not be empty"}
		}
		return s, nil
	case ControlSelect:
		s, ok := value.(string)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Value: value, Message: "expected a string"}
		}
		sel, ok := d.(*Select)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Message: "declared as select but is not a Select key"}
		}
		for _, opt := range sel.options {
			if opt.Value == s {
				return s, nil
			}
		}
		return nil, &ValidationError{
			SettingKey: d.Key(), Value: s,
			Message: fmt.Sprintf("value %q is not a valid option", s),
		}
	case ControlNumber:
		f, ok := toFloat64(value)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Value: value, Message: "expected a number"}
		}
		n, ok := d.(*Number)
		if !ok {
			return nil, &ValidationError{SettingKey: d.Key(), Message: "declared as number but is not a Number key"}
		}
		if n.min != nil && f < *n.min {
			return nil, &ValidationError{
				SettingKey: d.Key(), Value: f,
				Message: fmt.Sprintf("value %v below minimum %v", f, *n.min),
			}
		}
		if n.max != nil && f > *n.max {
			return nil, &ValidationError{
				SettingKey: d.Key(), Value: f,
				Message: fmt.Sprintf("value %v above maximum %v", f, *n.max),
			}
		}
		return f, nil
	case ControlPaths:
		paths, err := canonicalPaths(d.Key(), value)
		if err != nil {
			return nil, err
		}
		return paths, nil
	}
	return nil, &ValidationError{SettingKey: d.Key(), Value: value, Message: "unsupported control kind"}
}

// ── Secret-class methods ───────────────────────────────────────────────
// SecretSet stores a secret-class setting value in the SecretStore. Mints
// an opaque SecretID on first set and persists only the reference in the
// settings document — the plaintext never leaves the SecretStore.
//
// No production context available: this is called from an RPC handler path
// that has no request scoping. The vault caps waiting via its own deadline.
func (r *Registry) SecretSet(s *Secret, value string) error {
	r.mu.Lock()
	ctx := context.Background()
	id, ok := r.refs[s.key]
	if !ok {
		var err error
		id, err = r.secrets.Create(ctx, credential.NewSecret(value))
		if err != nil {
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

	// Existing ref: Create a new secret, swap the ref on success, then
	// best-effort delete the old. Commit failure leaves the old ref intact
	// and the new secret is an orphan — safer than a dangling ref.
	newID, err := r.secrets.Create(ctx, credential.NewSecret(value))
	if err != nil {
		r.mu.Unlock()
		return fmt.Errorf("settings: secret set %q: %w", s.key, err)
	}
	newRefs := copyRefs(r.refs)
	newRefs[s.key] = newID
	ch, commitErr := r.commitLocked(r.values, newRefs, []string{s.key})
	r.mu.Unlock()
	if commitErr == nil {
		r.finishCommit(ch)
		// Best-effort delete of the old secret — only after the new ref
		// is persisted, so a crash between the two leaves the old value
		// reachable (ADR-0011 §4: orphan beats dangling ref).
		_ = r.secrets.Delete(ctx, id)
	}
	return commitErr
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
	_ = r.secrets.Delete(context.Background(), refID)
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
	return r.secrets.Exists(context.Background(), id)
}

// ── Persistence ────────────────────────────────────────────────────────

// change captures notification state from a successful commit. The caller
// MUST release r.mu and then call finishCommit.
type change struct {
	rev       int
	notifiers []ChangeNotifier
	keys      []string
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
	return change{rev: r.revision, notifiers: append([]ChangeNotifier(nil), r.notifiers...), keys: ck}, nil
}

// finishCommit invokes every registered notifier. Must be called after r.mu
// is released.
func (r *Registry) finishCommit(ch change) {
	for _, n := range ch.notifiers {
		if n != nil {
			n(ch.rev, ch.keys)
		}
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

// ── Path-list validation ───────────────────────────────────────────────

// pathListMaxEntries bounds a path-list setting (design spec §3.1).
const pathListMaxEntries = 32

// canonicalPaths validates a path-list candidate and returns the canonical,
// deduplicated slice. It accepts []string (the typed setter / in-memory
// snapshot) and []any (JSON-decoded values). Every entry must be a non-empty
// absolute path with no control runes that resolves (Abs → EvalSymlinks →
// Stat) to an existing directory. Canonical duplicates collapse first-wins.
// The returned slice is freshly allocated and never aliases the input.
// Error messages deliberately name no path (AD-11: user paths never enter
// wire errors or logs).
func canonicalPaths(key string, value any) ([]string, error) {
	strs, err := pathStrings(value)
	if err != nil {
		return nil, &ValidationError{SettingKey: key, Value: value, Message: err.Error()}
	}
	if len(strs) > pathListMaxEntries {
		return nil, &ValidationError{SettingKey: key, Value: value, Message: fmt.Sprintf("at most %d paths allowed", pathListMaxEntries)}
	}
	out := make([]string, 0, len(strs))
	for _, p := range strs {
		if p == "" {
			return nil, &ValidationError{SettingKey: key, Value: value, Message: "paths must be non-empty"}
		}
		if !filepath.IsAbs(p) {
			return nil, &ValidationError{SettingKey: key, Value: value, Message: "paths must be absolute"}
		}
		if hasControlRune(p) {
			return nil, &ValidationError{SettingKey: key, Value: value, Message: "paths must not contain control characters"}
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, &ValidationError{SettingKey: key, Value: value, Message: "invalid path"}
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, &ValidationError{SettingKey: key, Value: value, Message: "path does not resolve"}
		}
		fi, err := os.Stat(resolved)
		if err != nil || !fi.IsDir() {
			return nil, &ValidationError{SettingKey: key, Value: value, Message: "path is not an existing directory"}
		}
		dup := false
		for _, seen := range out {
			if sameDir(resolved, seen) {
				dup = true
				break
			}
		}
		if dup {
			continue // canonical first-wins
		}
		out = append(out, resolved)
	}
	return out, nil
}

// pathStrings converts a path-list candidate to []string. It accepts []string
// (the typed setter) and []any (a JSON-decoded array of strings); anything
// else is rejected as "expected an array of strings".
func pathStrings(value any) ([]string, error) {
	switch v := value.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, len(v))
		for i, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, errors.New("expected an array of strings")
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, errors.New("expected an array of strings")
	}
}

// hasControlRune reports whether p contains a Unicode control rune (which
// includes NUL). Such bytes cannot appear in a path handed to a native
// backend.
func hasControlRune(p string) bool {
	for _, r := range p {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// copyStrings returns a fresh copy of src; the result never aliases the input.
func copyStrings(src []string) []string {
	if src == nil {
		return []string{}
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

// copyPathsValue returns the snapshot/override representation of a stored
// path-list value: a deep-copied []string for a recognized list, or the raw
// stored value (deep-copied when it is a slice) so startup type corruption
// stays observable for fail-closed handling instead of being coerced to [].
func copyPathsValue(v any) any {
	switch t := v.(type) {
	case []string:
		return copyStrings(t)
	case []any:
		out := make([]any, len(t))
		copy(out, t)
		return out
	default:
		return v
	}
}

// normalizeLoadedPathList converts a persisted path-list value into registry
// state. A recognized value — an array of strings with at most
// pathListMaxEntries entries — becomes []string, preserved raw (no re-stat).
// Any other value is type corruption and is returned unchanged so it remains
// observable as an invalid snapshot.
func normalizeLoadedPathList(v any) any {
	switch t := v.(type) {
	case []string:
		if len(t) > pathListMaxEntries {
			return v
		}
		return copyStrings(t)
	case []any:
		if len(t) > pathListMaxEntries {
			return v
		}
		out := make([]string, len(t))
		for i, e := range t {
			s, ok := e.(string)
			if !ok {
				return v
			}
			out[i] = s
		}
		return out
	default:
		return v
	}
}

// ── Sandbox path-class conflict detection ────────────────────────────────

// sameDir reports whether two canonical paths refer to the same directory,
// falling back to os.SameFile for case-insensitive/case-normalizing
// filesystems where lexical comparison is not identity. Fails closed: a stat
// failure that would let the caller widen permissions returns false, so the
// caller conservatively treats the paths as distinct.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	fiA, errA := os.Stat(a)
	fiB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(fiA, fiB)
}

// pathWithinOrEqual reports whether path equals root or is a descendant of
// root. The cheap lexical fast path (filepath.Rel) is followed by os.SameFile
// while walking parents, so case/normalization aliases of the same directory
// are recognised on case-insensitive filesystems. Fails closed: a stat
// failure that would make a path appear "within" returns false.
func pathWithinOrEqual(root, path string) bool {
	if sameDir(root, path) {
		return true
	}
	// Cheap lexical fast path.
	rel, err := filepath.Rel(root, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return true
	}
	// Walk up from path, checking each ancestor with os.SameFile.
	for p := path; p != "/" && p != "."; {
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		if sameDir(parent, root) {
			return true
		}
		p = parent
	}
	return false
}

// checkSandboxPathConflict returns a ValidationError if any read-only path
// equals or is a descendant of any writable path — a configuration that
// would make the read-only classification meaningless because a writable
// ancestor overrides it. RW child under RO parent is allowed: the child is
// specifically writable inside a broader read-only tree.
// The error is keyed to the setting being changed and carries no path.
func checkSandboxPathConflict(key string, values map[string]any) error {
	roAny, roOK := values[SandboxAllowedReadOnlyPaths.Key()]
	rwAny, rwOK := values[SandboxAllowedWritablePaths.Key()]
	if !roOK || !rwOK {
		return nil
	}
	ro, ok := roAny.([]string)
	if !ok || len(ro) == 0 {
		return nil
	}
	rw, ok := rwAny.([]string)
	if !ok || len(rw) == 0 {
		return nil
	}
	for _, r := range ro {
		for _, w := range rw {
			if pathWithinOrEqual(w, r) {
				return &ValidationError{
					SettingKey: key,
					Message:    "a read-only path cannot be equal to or below a writable path",
				}
			}
		}
	}
	return nil
}

// sandboxPathKeys is the set of keys whose values participate in the
// cross-class conflict check — the two sandbox path-list settings.
var sandboxPathKeys = map[string]bool{
	"sandbox.allowedWritablePaths": true,
	"sandbox.allowedReadOnlyPaths": true,
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
