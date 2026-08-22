package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

// fakeDoc is an in-memory DocumentStore for testing.
type fakeDoc struct {
	data     map[string][]byte
	writeErr error
}

func (f *fakeDoc) Read(name string, into any) (bool, error) {
	b, ok := f.data[name]
	if !ok || b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, into)
}

func (f *fakeDoc) Write(name string, doc any) error {
	if f.writeErr != nil {
		return f.writeErr
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

func (f *fakeDoc) Delete(name string) error {
	delete(f.data, name)
	return nil
}

// fakeSecretStore implements credential.SecretStore in memory.
type fakeSecretStore struct {
	data    map[credential.SecretID]string
	counter atomic.Int64
}

func (f *fakeSecretStore) Get(ctx context.Context, id credential.SecretID) (credential.Secret, error) {
	v, ok := f.data[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return credential.NewSecret(v), nil
}

func (f *fakeSecretStore) Create(ctx context.Context, value credential.Secret) (credential.SecretID, error) {
	var plaintext string
	if err := value.Use(func(b []byte) error { plaintext = string(b); return nil }); err != nil {
		return "", err
	}
	if f.data == nil {
		f.data = make(map[credential.SecretID]string)
	}
	n := f.counter.Add(1)
	id := credential.SecretID(fmt.Sprintf("ss%016x", n))
	f.data[id] = plaintext
	return id, nil
}

func (f *fakeSecretStore) Delete(ctx context.Context, id credential.SecretID) error {
	delete(f.data, id)
	return nil
}

func (f *fakeSecretStore) Exists(ctx context.Context, id credential.SecretID) (bool, error) {
	_, ok := f.data[id]
	return ok, nil
}

// ── inline declarations for types not yet in production ────────────────
// These exist so the test suite exercises every control kind and data path.
// They are not production settings; when equivalent real settings are
// declared, these can be removed and the tests pointed at the real ones.

var _ = settings.MustRegisterSecret(settings.SecretSpec{
	Key:         "test.secretExample",
	Section:     "Test",
	Label:       "Example Secret",
	Description: "A test-only secret-class setting for exercising the infrastructure.",
	DataClass:   settings.SecretAuthenticator,
})

var _ = settings.MustRegisterString(settings.StringSpec{
	Key:         "test.stringExample",
	Section:     "Test",
	Label:       "Example String",
	Description: "A test-only string setting.",
	DataClass:   settings.PrivateMetadata,
	Default:     "hello",
})

var _ = settings.MustRegisterNumber(settings.NumberSpec{
	Key:         "test.numberExample",
	Section:     "Test",
	Label:       "Example Number",
	Description: "A test-only number setting.",
	DataClass:   settings.PrivateMetadata,
	Default:     42,
	Min:         func() *float64 { v := 0.0; return &v }(),
	Max:         func() *float64 { v := 100.0; return &v }(),
})

var _ = settings.MustRegisterSelect(settings.SelectSpec{
	Key:         "test.selectExample",
	Section:     "Test",
	Label:       "Example Select",
	Description: "A test-only select setting.",
	DataClass:   settings.PrivateMetadata,
	Default:     "a",
	Options: []settings.SelectOption{
		{Value: "a", Label: "Option A"},
		{Value: "b", Label: "Option B"},
	},
})

// ── Declaration tests ──────────────────────────────────────────────────

func TestDeclarations(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	descs := reg.Descriptors()

	if len(descs) == 0 {
		t.Fatal("expected at least one declared setting, got 0")
	}

	// Production declaration must be present.
	foundOSC52 := false
	for _, d := range descs {
		if d.Key() == "" {
			t.Errorf("declaration has empty key: %+v", d)
		}
		if d.Label() == "" {
			t.Errorf("declaration %q has empty label", d.Key())
		}
		if d.Control() == "" {
			t.Errorf("declaration %q has empty control kind", d.Key())
		}
		if d.Key() == "clipboard.osc52Suppressed" {
			foundOSC52 = true
		}
	}
	if !foundOSC52 {
		t.Error("clipboard.osc52Suppressed not found in declarations")
	}

	// Wire Declaration conversion.
	decls := reg.Declarations()
	if len(decls) != len(descs) {
		t.Fatalf("Declarations() returned %d, Descriptors() returned %d", len(decls), len(descs))
	}
	for _, d := range decls {
		if d.Key == "" || d.Control == "" || d.Label == "" {
			t.Errorf("wire declaration %q has empty fields", d.Key)
		}
	}
}

// The wire declaration carries the unit a number setting is measured in, and
// the three History settings declare the units the owner reads (nocx-w7h.7).
func TestNumberUnitOnTheWire(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	byKey := map[string]settings.Declaration{}
	for _, d := range reg.Declarations() {
		byKey[d.Key] = d
	}
	for key, want := range map[string]string{
		"history.retentionDays":  "days",
		"history.retentionMiB":   "MiB",
		"history.diskCeilingMiB": "MiB",
	} {
		got := byKey[key].Unit
		if got != want {
			t.Errorf("%s: wire unit = %q, want %q", key, got, want)
		}
	}
	// A number without a unit declares none — the field renders no suffix.
	if got := byKey["test.numberExample"].Unit; got != "" {
		t.Errorf("test.numberExample: wire unit = %q, want empty", got)
	}
}

// ── Bool get/set/reset ─────────────────────────────────────────────────

func TestBoolGetSet(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	osc52 := findBool(t, reg, "clipboard.osc52Suppressed")

	v, err := reg.GetBool(osc52)
	if err != nil {
		t.Fatalf("GetBool: %v", err)
	}
	if v != false {
		t.Errorf("expected default false, got %v", v)
	}

	if err = reg.SetBool(osc52, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}

	v, err = reg.GetBool(osc52)
	if err != nil {
		t.Fatalf("GetBool after set: %v", err)
	}
	if v != true {
		t.Errorf("expected true after set, got %v", v)
	}

	// Reset to default.
	if err = reg.Reset(osc52); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	v, err = reg.GetBool(osc52)
	if err != nil {
		t.Fatalf("GetBool after reset: %v", err)
	}
	if v != false {
		t.Errorf("expected false after reset, got %v", v)
	}
}

// ── getSnapshot ─────────────────────────────────────────────────────────

func TestGetSnapshot_ExcludesSecrets(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	snap, err := reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	// Secret keys must be absent from both values and overridden.
	for _, d := range reg.Descriptors() {
		if d.Control() == settings.ControlSecret {
			if _, ok := snap.Values[d.Key()]; ok {
				t.Errorf("secret key %q present in snapshot values", d.Key())
			}
			for _, k := range snap.Overridden {
				if k == d.Key() {
					t.Errorf("secret key %q present in snapshot overridden", d.Key())
				}
			}
		}
	}

	// Non-secret declarations must be present in values.
	if _, ok := snap.Values["clipboard.osc52Suppressed"]; !ok {
		t.Error("clipboard.osc52Suppressed missing from snapshot values")
	}
}

func TestGetSnapshot_OverriddenTracksStoredOverrides(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	snap, err := reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if len(snap.Overridden) != 0 {
		t.Errorf("fresh registry should have zero overridden, got %v", snap.Overridden)
	}

	b := findBool(t, reg, "clipboard.osc52Suppressed")
	if err = reg.SetBool(b, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}

	snap, err = reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after set: %v", err)
	}

	found := false
	for _, k := range snap.Overridden {
		if k == "clipboard.osc52Suppressed" {
			found = true
			break
		}
	}
	if !found {
		t.Error("clipboard.osc52Suppressed missing from overridden after set")
	}
	if snap.Values["clipboard.osc52Suppressed"] != true {
		t.Errorf("expected values[clipboard.osc52Suppressed]=true, got %v", snap.Values["clipboard.osc52Suppressed"])
	}
}

func TestGetSnapshot_RevisionBumpsAfterMutation(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	snap, err := reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if snap.Revision != 0 {
		t.Errorf("fresh registry revision should be 0, got %d", snap.Revision)
	}

	b := findBool(t, reg, "clipboard.osc52Suppressed")
	if err = reg.SetBool(b, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}

	snap, err = reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after set: %v", err)
	}
	if snap.Revision != 1 {
		t.Errorf("revision should be 1 after one mutation, got %d", snap.Revision)
	}

	if err = reg.Reset(b); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	snap, err = reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after reset: %v", err)
	}
	if snap.Revision != 2 {
		t.Errorf("revision should be 2 after two mutations, got %d", snap.Revision)
	}
}

func TestGetSnapshot_RevisionBumpsAfterSecretMutation(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	s := findSecret(t, reg, "test.secretExample")
	if err := reg.SecretSet(s, "value"); err != nil {
		t.Fatalf("SecretSet: %v", err)
	}

	snap, err := reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after secretSet: %v", err)
	}
	if snap.Revision != 1 {
		t.Errorf("revision should be 1 after secret set, got %d", snap.Revision)
	}

	// Secret keys stay absent from values and overridden even after set.
	if _, ok := snap.Values[s.Key()]; ok {
		t.Errorf("secret key %q present in values after set", s.Key())
	}
	for _, k := range snap.Overridden {
		if k == s.Key() {
			t.Errorf("secret key %q present in overridden after set", s.Key())
		}
	}

	if err = reg.SecretDelete(s); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}

	snap, err = reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after secretDelete: %v", err)
	}
	if snap.Revision != 2 {
		t.Errorf("revision should be 2 after secret delete, got %d", snap.Revision)
	}
}

func TestGetSnapshot_DefaultsPresent(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	snap, err := reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	// A declared non-secret with no stored override should have its default.
	str := findString(t, reg, "test.stringExample")
	if snap.Values[str.Key()] != "hello" {
		t.Errorf("expected default 'hello' for test.stringExample, got %v", snap.Values[str.Key()])
	}
}

// ── Secret set/delete/exists (no get) ──────────────────────────────────

func TestSecretSetDeleteExists(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	s := findSecret(t, reg, "test.secretExample")

	exists, err := reg.SecretExists(s)
	if err != nil {
		t.Fatalf("SecretExists: %v", err)
	}
	if exists {
		t.Error("secret should not exist initially")
	}

	if err = reg.SecretSet(s, "super-secret-value"); err != nil {
		t.Fatalf("SecretSet: %v", err)
	}

	exists, err = reg.SecretExists(s)
	if err != nil {
		t.Fatalf("SecretExists after set: %v", err)
	}
	if !exists {
		t.Error("secret should exist after set")
	}

	if err = reg.SecretDelete(s); err != nil {
		t.Fatalf("SecretDelete: %v", err)
	}

	exists, err = reg.SecretExists(s)
	if err != nil {
		t.Fatalf("SecretExists after delete: %v", err)
	}
	if exists {
		t.Error("secret should not exist after delete")
	}
}

// ── Validation ─────────────────────────────────────────────────────────

func TestStringValidation(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	s := findString(t, reg, "test.stringExample")

	err := reg.SetString(s, "")
	if err == nil {
		t.Error("expected validation error for empty string")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("expected ErrValidation, got %v", err)
	}
}

// ── Number validation ──────────────────────────────────────────────────

func TestNumberValidation(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	n := findNumber(t, reg, "test.numberExample")

	err := reg.SetNumber(n, *n.Min()-1)
	if err == nil {
		t.Error("expected validation error for value below min")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("expected ErrValidation, got %v", err)
	}
}

// ── The sidebar's width is NOT a setting (nocx-mqie.3) ─────────────────

// A width produced by dragging a panel edge is not a decision, so the
// registry must not declare it: a declaration is what puts a row on
// Settings → Interface and what makes a drag count toward the section's
// "Modified" badge. Both symptoms had one cause, and this is the assertion
// that the cause is gone — the width lives in internal/uistate now.
func TestSidebarWidthIsNotDeclaredAsASetting(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	for _, d := range reg.Declarations() {
		if d.Key == "sidebar.width" {
			t.Fatalf("sidebar.width is still declared (section %q, label %q): "+
				"a drag is not a decision, so it must not appear on a Settings page",
				d.Section, d.Label)
		}
	}
}

// ── Select validation ──────────────────────────────────────────────────

func TestSelectValidation(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	s := findSelect(t, reg, "test.selectExample")

	err := reg.SetSelect(s, "not-an-option-value")
	if err == nil {
		t.Error("expected validation error for unknown option")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("expected ErrValidation, got %v", err)
	}
}

// ── Persistence round-trip: OSC 52 suppressed survives restart ─────────

func TestPersistenceRoundTrip(t *testing.T) {
	doc := &fakeDoc{}

	reg1 := settings.New(doc, &fakeSecretStore{})
	osc52 := findBool(t, reg1, "clipboard.osc52Suppressed")

	if err := reg1.SetBool(osc52, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}

	reg2 := settings.New(doc, &fakeSecretStore{})
	osc52 = findBool(t, reg2, "clipboard.osc52Suppressed")

	v, err := reg2.GetBool(osc52)
	if err != nil {
		t.Fatalf("GetBool after round-trip: %v", err)
	}
	if v != true {
		t.Errorf("expected true after persistence round-trip, got %v", v)
	}
}

// ── Schema version ─────────────────────────────────────────────────────

func TestSchemaVersion(t *testing.T) {
	doc := &fakeDoc{}
	reg := settings.New(doc, &fakeSecretStore{})

	osc52 := findBool(t, reg, "clipboard.osc52Suppressed")
	if err := reg.SetBool(osc52, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}

	var docData struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Values        map[string]interface{} `json:"values"`
	}
	found, err := doc.Read("settings.json", &docData)
	if err != nil {
		t.Fatalf("read settings doc: %v", err)
	}
	if !found {
		t.Fatal("settings.json not found after write")
	}
	if docData.SchemaVersion <= 0 {
		t.Errorf("expected positive schema version, got %d", docData.SchemaVersion)
	}
}

// ── Declaration wire-shape tests ───────────────────────────────────────

func TestBoolDefaultNotNil(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	for _, d := range reg.Declarations() {
		if d.Control == "toggle" {
			if d.Default == nil {
				t.Errorf("toggle %q has nil default, want explicit value", d.Key)
			}
		}
	}
}

func TestSelectHasOptions(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	for _, d := range reg.Declarations() {
		if d.Control == "select" {
			if len(d.Options) == 0 {
				t.Errorf("select %q has no options", d.Key)
			}
		}
	}
}

func TestNumberHasBounds(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	for _, d := range reg.Declarations() {
		if d.Control == "number" {
			if d.Min == nil && d.Max == nil {
				t.Errorf("number %q has neither min nor max", d.Key)
			}
		}
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

func findBool(t *testing.T, reg *settings.Registry, key string) *settings.Bool {
	t.Helper()
	for _, d := range reg.Descriptors() {
		if d.Key() == key {
			if b, ok := d.(*settings.Bool); ok {
				return b
			}
		}
	}
	t.Fatalf("bool setting %q not found", key)
	return nil
}

func findString(t *testing.T, reg *settings.Registry, key string) *settings.String {
	t.Helper()
	for _, d := range reg.Descriptors() {
		if d.Key() == key {
			if s, ok := d.(*settings.String); ok {
				return s
			}
		}
	}
	t.Fatalf("string setting %q not found", key)
	return nil
}

func findNumber(t *testing.T, reg *settings.Registry, key string) *settings.Number {
	t.Helper()
	for _, d := range reg.Descriptors() {
		if d.Key() == key {
			if n, ok := d.(*settings.Number); ok {
				return n
			}
		}
	}
	t.Fatalf("number setting %q not found", key)
	return nil
}

func findSelect(t *testing.T, reg *settings.Registry, key string) *settings.Select {
	t.Helper()
	for _, d := range reg.Descriptors() {
		if d.Key() == key {
			if s, ok := d.(*settings.Select); ok {
				return s
			}
		}
	}
	t.Fatalf("select setting %q not found", key)
	return nil
}

func findSecret(t *testing.T, reg *settings.Registry, key string) *settings.Secret {
	t.Helper()
	for _, d := range reg.Descriptors() {
		if d.Key() == key {
			if s, ok := d.(*settings.Secret); ok {
				return s
			}
		}
	}
	t.Fatalf("secret setting %q not found", key)
	return nil
}

// ── History section (the user's decisions) ───────────────────────────────

// The History section renders the decisions a user actually has: keep or
// not, how long, how much disk (two numbers), output separately. The
// retention label says "removed from nocx", never "securely erased"
// (internal/content's package doc: ordinary DELETE leaves data in WAL pages
// and free space).
func TestHistorySectionDeclaresUserDecisions(t *testing.T) {
	rendered := map[string]settings.Declaration{}
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{data: map[credential.SecretID]string{}})
	for _, d := range reg.Declarations() {
		if d.Section == "History" {
			rendered[d.Key] = d
		}
	}
	for _, want := range []string{
		"history.enabled", "history.retentionDays",
		"history.retentionMiB", "history.diskCeilingMiB", "history.outputEnabled",
	} {
		if _, ok := rendered[want]; !ok {
			t.Errorf("History section missing %q — a promise the screen does not make", want)
		}
	}
	retention := rendered["history.retentionDays"].Description
	if !strings.Contains(retention, "removed from nocx") {
		t.Errorf("retention label does not use the honest wording: %q", retention)
	}
	if strings.Contains(retention, "securely erased") {
		t.Errorf("retention label promises secure erasure: %q", retention)
	}
}

// A sentinel that is explained only in prose is a sentinel nobody reads. The
// meaning of 0 travels on the wire beside the unit and the bounds, so the
// screen has nothing to infer (nocx-w7h.12).
func TestNumberZeroLabelOnTheWire(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	byKey := map[string]settings.Declaration{}
	for _, d := range reg.Declarations() {
		byKey[d.Key] = d
	}
	got := byKey["history.retentionDays"]
	if got.ZeroLabel == "" {
		t.Error("history.retentionDays: no ZeroLabel on the wire; 0 is its default and means 'no age limit'")
	}
	// The setting whose 0 is an ordinary number declares none.
	if z := byKey["history.retentionMiB"].ZeroLabel; z != "" {
		t.Errorf("history.retentionMiB: ZeroLabel = %q, want empty", z)
	}
	// And the sentinel's meaning is no longer duplicated in the prose that
	// used to carry it — one statement, one place.
	if strings.Contains(got.Description, "0 keeps everything") {
		t.Error("history.retentionDays: the description still explains 0; ZeroLabel owns that now")
	}
}

// ── ApplyValues (import-time restore) ──────────────────────────────────

// Whatever GetSnapshot exports, ApplyValues restores: the two are inverse
// operations over the non-secret settings (nocx-ojxa — import used to drop
// settings the export carried).
func TestApplyValues_RestoresSnapshot(t *testing.T) {
	src := settings.New(&fakeDoc{}, &fakeSecretStore{})
	if err := src.SetBool(settings.HistoryEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	if err := src.SetNumber(settings.HistoryRetentionDays, 90); err != nil {
		t.Fatalf("SetNumber: %v", err)
	}
	if err := src.SetSelect(settings.TabPlacement, "vertical"); err != nil {
		t.Fatalf("SetSelect: %v", err)
	}

	snap, err := src.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	dst := settings.New(&fakeDoc{}, &fakeSecretStore{})
	if applyErr := dst.ApplyValues(snap.Values); applyErr != nil {
		t.Fatalf("ApplyValues: %v", applyErr)
	}

	got, err := dst.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	for key, want := range snap.Values {
		if !reflect.DeepEqual(got.Values[key], want) {
			t.Errorf("%s after restore = %v, want %v", key, got.Values[key], want)
		}
	}
	for _, key := range snap.Overridden {
		found := false
		for _, k := range got.Overridden {
			if k == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is not marked overridden after restore", key)
		}
	}
}

func TestApplyValues_UnknownKeyRejected(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	err := reg.ApplyValues(map[string]any{"no.such.setting": true})
	if err == nil {
		t.Fatal("unknown key applied, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error is %T, want ValidationError wrapping ErrValidation", err)
	}
}

// Import never resolves or invents a secret (ADR-0011 §2): a snapshot key
// that names a secret-class setting is refused, not applied.
func TestApplyValues_SecretClassKeyRejected(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	err := reg.ApplyValues(map[string]any{"test.secretExample": "value"})
	if err == nil {
		t.Fatal("secret-class key applied, want error")
	}
}

// Every value is validated before anything is committed: one invalid value
// leaves the whole restore undone, never half-applied.
func TestApplyValues_InvalidValueRejectedAtomically(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	// First value is valid; the second is not. The valid one must not land.
	// clipboard.osc52Suppressed defaults to false, so "did not land" is
	// observable (history.enabled defaults to true and could not prove it).
	err := reg.ApplyValues(map[string]any{
		"clipboard.osc52Suppressed": true,
		"test.selectExample":        "not-an-option",
	})
	if err == nil {
		t.Fatal("invalid select value applied, want error")
	}
	got, gerr := reg.GetBool(settings.ClipboardOSC52Suppressed)
	if gerr != nil {
		t.Fatalf("GetBool: %v", gerr)
	}
	if got {
		t.Error("clipboard.osc52Suppressed became true despite the map being rejected as a whole")
	}

	if err := reg.ApplyValues(map[string]any{"test.numberExample": float64(500)}); err == nil {
		t.Fatal("out-of-bounds number applied, want error")
	}
	if err := reg.ApplyValues(map[string]any{"clipboard.osc52Suppressed": "yes"}); err == nil {
		t.Fatal("string where a boolean is declared applied, want error")
	}
	if err := reg.ApplyValues(map[string]any{"test.stringExample": ""}); err == nil {
		t.Fatal("empty string for a non-empty-default setting applied, want error")
	}
}

func TestApplyValues_EmptyMapIsNoop(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	if err := reg.ApplyValues(nil); err != nil {
		t.Fatalf("ApplyValues(nil): %v", err)
	}
	if err := reg.ApplyValues(map[string]any{}); err != nil {
		t.Fatalf("ApplyValues(empty): %v", err)
	}
}

func TestValidateSettingMatchesBulkRestoreValidation(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	if err := reg.ValidateSetting("test.numberExample", float64(1)); err != nil {
		t.Fatalf("valid number rejected: %v", err)
	}
	for name, tc := range map[string]struct {
		key   string
		value any
	}{
		"unknown key":  {key: "no.such.setting", value: true},
		"secret key":   {key: "test.secretExample", value: "secret"},
		"object value": {key: "test.stringExample", value: map[string]any{"mode": "dark"}},
		"array value":  {key: "test.stringExample", value: []any{"dark"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := reg.ValidateSetting(tc.key, tc.value); err == nil {
				t.Fatal("invalid setting accepted")
			}
		})
	}
}

// ── Group catalogue (nocx-dgsp) ────────────────────────────────────────
// The rail's group catalogue is declared in Go and ships beside the
// declarations in settings.describe; these tests pin the declared catalogue
// and the registration API that grows it.

// Test-only group + section mapping, registered through the same API
// production uses — the "adding a section to a group is a Go-side change"
// flow (nocx-dgsp criterion 2), exercised without touching the production
// registrations.
func init() {
	settings.RegisterGroup(settings.SettingsGroup{ID: "testgroup", Title: "Test Group", Order: 99})
	settings.RegisterSectionGroup("Experiments", "testgroup")
}

func TestGroups_DeclaredCatalogue(t *testing.T) {
	// Read through a Registry — the same path capability/config.go takes to
	// the wire — never through a package-level accessor that could drift
	// from it (the deadcode ratchet is how the duplicate was caught).
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	groups := reg.Groups()
	byID := make(map[string]settings.SettingsGroup, len(groups))
	orders := make(map[int]string, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
		if other, dup := orders[g.Order]; dup {
			t.Errorf("groups %q and %q share order %d — the rail cannot sort by it", other, g.ID, g.Order)
		}
		orders[g.Order] = g.ID
	}
	for _, want := range []struct {
		id, title string
		order     int
	}{
		{"assistant", "Assistant", 0},
		{"vault", "Vault", 1},
		{"application", "Application", 2},
		{"developer", "Developer", 3},
	} {
		g, ok := byID[want.id]
		if !ok {
			t.Errorf("group %q missing from the catalogue", want.id)
			continue
		}
		if g.Title != want.title {
			t.Errorf("group %q title = %q, want %q", want.id, g.Title, want.title)
		}
		if g.Order != want.order {
			t.Errorf("group %q order = %d, want %d", want.id, g.Order, want.order)
		}
	}
}

func TestSectionGroups_ProductionMappings(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	sg := reg.SectionGroups()
	for section, want := range map[string]string{
		"Interface": "application",
		"Clipboard": "application",
		"History":   "application",
		"Test":      "developer",
	} {
		got, ok := sg[section]
		if !ok {
			t.Errorf("section %q belongs to no group", section)
			continue
		}
		if got != want {
			t.Errorf("section %q → group %q, want %q", section, got, want)
		}
	}
	// A section nobody placed is ungrouped, not an error — it renders at top
	// level (criterion 1's Connections is the component-page instance; this is
	// the generated-section one).
	if _, ok := sg["No.Such.Section"]; ok {
		t.Error("an unplaced section resolved to a group")
	}
}

func TestSectionGroup_AddedThroughRegistry(t *testing.T) {
	// The init() registration above went through RegisterSectionGroup — the
	// same one-line API a real Go-side change uses. The describe payload
	// carries the result with no frontend edit (criterion 2, Go half); read
	// it back through a Registry, the wire path.
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	got, ok := reg.SectionGroups()["Experiments"]
	if !ok || got != "testgroup" {
		t.Fatalf("Experiments → %q (ok=%v), want testgroup", got, ok)
	}
}

func TestRegisterGroup_DuplicateIDPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterGroup with a duplicate id did not panic")
		}
	}()
	settings.RegisterGroup(settings.SettingsGroup{ID: "assistant", Title: "Assistant Again", Order: 50})
}

func TestRegisterSectionGroup_UnknownGroupPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterSectionGroup with an undeclared group id did not panic")
		}
	}()
	settings.RegisterSectionGroup("No.Such.Section", "no-such-group")
}

func TestRegisterSectionGroup_DuplicateSectionPanics(t *testing.T) {
	// One section, one group — the same contradiction a per-declaration field
	// would permit, refused at registration instead (criterion 3).
	defer func() {
		if recover() == nil {
			t.Error("RegisterSectionGroup for an already-placed section did not panic")
		}
	}()
	settings.RegisterSectionGroup("Test", "testgroup")
}

func TestDeclaration_HasNoPerDeclarationGroupField(t *testing.T) {
	// Criterion 3, asserted by construction: the section→group mapping is the
	// one owner, so a Declaration can never disagree with a sibling in its
	// section. This test fails the moment a per-declaration group field is
	// added to the wire type.
	declType := reflect.TypeOf(settings.Declaration{})
	for _, name := range []string{"Group", "GroupID", "GroupId", "GroupName"} {
		if _, found := declType.FieldByName(name); found {
			t.Errorf("Declaration gained per-declaration field %q — the section→group mapping must stay the one owner", name)
		}
	}
}

func TestSectionGroups_SameSectionSameGroup(t *testing.T) {
	// Every declaration resolves through the same section-keyed mapping, so
	// two declarations in one section cannot disagree.
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	sg := reg.SectionGroups()
	bySection := make(map[string]string)
	for _, d := range reg.Declarations() {
		gid, ok := sg[d.Section]
		if !ok {
			continue
		}
		if prev, seen := bySection[d.Section]; seen && prev != gid {
			t.Errorf("section %q resolved to both %q and %q", d.Section, prev, gid)
		}
		bySection[d.Section] = gid
	}
	for _, section := range []string{"Interface", "Clipboard", "History"} {
		if _, ok := sg[section]; !ok {
			t.Errorf("production section %q is not placed in any group", section)
		}
	}
}

func TestRegistry_GroupsAndSectionGroups(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	if len(reg.Groups()) == 0 {
		t.Error("registry reports an empty group catalogue")
	}
	sg := reg.SectionGroups()
	if sg["History"] != "application" {
		t.Errorf("registry SectionGroups[History] = %q, want application", sg["History"])
	}
	// The registry hands out a copy: mutating it must not corrupt the
	// catalogue the describe payload reads from.
	sg["History"] = "mutated"
	if reg.SectionGroups()["History"] != "application" {
		t.Error("mutating the returned SectionGroups map corrupted the catalogue")
	}
}

// ── Contract conformance (nocx-dgsp addendum 2) ────────────────────────
// The settings.describe result shape is declared once, in
// contracts/settings.describe.schema.json; the renderer's types are
// generated from it and the Go side is validated against it. The DTO test
// below pins the marshalled envelope (field tags, omitempty, nil-slice
// behaviour); the over-the-wire test lives in internal/app where the real
// composition root answers the real method over a real socket.

// contractDir holds the wire schemas; from internal/settings the repo root
// is two levels up. The loader mirrors the one in
// internal/transport/ws_contract_test.go, which this package cannot import.
const contractDir = "../../contracts"

func loadSettingsContractSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	path := filepath.Join(contractDir, name)
	f, openErr := os.Open(path) //nolint:gosec // test-only path under contracts/
	if openErr != nil {
		t.Fatalf("open %s: %v", path, openErr)
	}
	defer func() { _ = f.Close() }()
	doc, parseErr := jsonschema.UnmarshalJSON(f)
	if parseErr != nil {
		t.Fatalf("parse %s: %v", path, parseErr)
	}
	if addErr := c.AddResource("https://nocx.local/contracts/"+name, doc); addErr != nil {
		t.Fatalf("add %s: %v", name, addErr)
	}
	s, err := c.Compile("https://nocx.local/contracts/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

func validateSettingsContract(t *testing.T, s *jsonschema.Schema, raw []byte, what string) {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: unmarshal: %v", what, err)
	}
	if err := s.Validate(doc); err != nil {
		t.Errorf("%s does not satisfy its contract:\n%v\n\npayload was:\n%s", what, err, raw)
	}
}

func TestSettingsDescribe_DTOConformsToContract(t *testing.T) {
	schema := loadSettingsContractSchema(t, "settings.describe.schema.json")
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	cases := map[string]map[string]any{
		// Everything populated: the real registry's declarations and group
		// catalogue, marshalled exactly as the transport builds the
		// settings.describe envelope. Covers the fields omitempty hides —
		// unit/zeroLabel on numbers, options on selects, absent default on
		// secrets.
		"populated": {
			"declarations":  reg.Declarations(),
			"groups":        reg.Groups(),
			"sectionGroups": reg.SectionGroups(),
		},
		// Empty arrays must marshal as [] and an empty mapping as {} —
		// never null, which the renderer's .map / Object.entries would
		// throw on.
		"empty": {
			"declarations":  []settings.Declaration{},
			"groups":        []settings.SettingsGroup{},
			"sectionGroups": map[string]string{},
		},
	}
	for name, envelope := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateSettingsContract(t, schema, raw, "settings.describe DTO ("+name+")")
		})
	}
}

// The per-command output cap (nocx-2f0f): declared, bounded, and the SAME
// number the store falls back to. Two defaults for one quantity is how a
// store opened without a registry ends up bounding a command by an amount
// the settings page never showed.
func TestHistoryOutputCap_IsDeclaredBoundedAndAgreesWithTheStore(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})

	v, err := reg.GetNumber(settings.HistoryOutputCapKB)
	if err != nil {
		t.Fatalf("GetNumber: %v", err)
	}
	if v != 256 {
		t.Fatalf("default = %v, want 256", v)
	}
	if int(v)<<10 != content.DefaultOutputCapBytes {
		t.Fatalf("settings default %d KB != content.DefaultOutputCapBytes %d bytes",
			int(v), content.DefaultOutputCapBytes)
	}
	if err := reg.SetNumber(settings.HistoryOutputCapKB, 8); err == nil {
		t.Fatal("8 KB is below the declared floor and must be refused")
	}
	if err := reg.SetNumber(settings.HistoryOutputCapKB, 8192); err == nil {
		t.Fatal("8192 KB is above the declared ceiling and must be refused")
	}
	if err := reg.SetNumber(settings.HistoryOutputCapKB, 1024); err != nil {
		t.Fatalf("a value inside the bounds was refused: %v", err)
	}
}

func findPathList(t *testing.T, reg *settings.Registry, key string) *settings.PathList {
	t.Helper()
	for _, descriptor := range reg.Descriptors() {
		if descriptor.Key() != key {
			continue
		}
		if paths, ok := descriptor.(*settings.PathList); ok {
			return paths
		}
	}
	t.Fatalf("path-list setting %q not found", key)
	return nil
}

// ── Path-list settings (ADR-0037 §3, ADR-0039 §3.1) ─────────────────────

// tmpDirs returns n fresh existing directories under one t.TempDir() base.
func tmpDirs(t *testing.T, n int) []string {
	t.Helper()
	base := t.TempDir()
	out := make([]string, 0, n)
	for i := range n {
		d := filepath.Join(base, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		out = append(out, d)
	}
	return out
}

// pathListKeys lists every declared path-list setting. The shared behavior is
// proven for each declaration, never just the first (ADR-0039 adds the
// read-only class beside the writable one).
func pathListKeys() []string {
	return []string{"sandbox.allowedWritablePaths", "sandbox.allowedReadOnlyPaths"}
}

func TestPathListDeclaration(t *testing.T) {
	reg := settings.New(&fakeDoc{}, &fakeSecretStore{})
	byKey := map[string]settings.Declaration{}
	for _, d := range reg.Declarations() {
		byKey[d.Key] = d
	}
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			decl, ok := byKey[key]
			if !ok {
				t.Fatalf("%s not declared", key)
			}
			if decl.Section != "Experimental" {
				t.Errorf("section = %q, want Experimental", decl.Section)
			}
			if decl.Control != settings.ControlPaths {
				t.Errorf("control = %q, want paths", decl.Control)
			}
			if decl.DataClass != settings.PrivateMetadata {
				t.Errorf("dataClass = %q, want privateMetadata", decl.DataClass)
			}
			if decl.Label == "" || decl.Description == "" {
				t.Error("declaration missing label or description")
			}
			if _, ok := decl.Default.([]string); !ok {
				t.Errorf("default is %T, want []string", decl.Default)
			}
		})
	}
}

func TestSetPaths_CanonicalizesAndDedupes(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			p := findPathList(t, reg, key)

			target := tmpDirs(t, 1)[0]
			link := filepath.Join(filepath.Dir(target), "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("Symlink: %v", err)
			}

			// The symlink and the duplicate both collapse to the canonical
			// target, first-wins.
			if err := reg.SetPaths(p, []string{target, link, target}); err != nil {
				t.Fatalf("SetPaths: %v", err)
			}
			got, err := reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if len(got) != 1 || got[0] != target {
				t.Errorf("got %v, want [%s]", got, target)
			}
		})
	}
}

func TestSetPaths_RejectsInvalid(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			p := findPathList(t, reg, key)

			dirs := tmpDirs(t, 2)
			dir := dirs[0]
			file := filepath.Join(dirs[1], "f")
			if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			cases := map[string][]string{
				"relative":        {"relative/path"},
				"non-existent":    {filepath.Join(dir, "missing")},
				"not-a-directory": {file},
				"empty":           {""},
				"control":         {dir + "\x00"},
			}
			for name, paths := range cases {
				t.Run(name, func(t *testing.T) {
					if err := reg.SetPaths(p, paths); err == nil {
						t.Fatalf("SetPaths(%q) succeeded, want error", paths)
					} else if !errors.Is(err, settings.ErrValidation) {
						t.Errorf("error = %v, want ErrValidation", err)
					}
					got, err := reg.GetPaths(p)
					if err != nil {
						t.Fatalf("GetPaths: %v", err)
					}
					if len(got) != 0 {
						t.Errorf("registry changed after failed set: %v", got)
					}
				})
			}
		})
	}
}

func TestSetPaths_CountLimit(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			p := findPathList(t, reg, key)

			dirs := tmpDirs(t, 32)
			if err := reg.SetPaths(p, dirs); err != nil {
				t.Fatalf("SetPaths(32): %v", err)
			}
			got, err := reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if len(got) != 32 {
				t.Fatalf("got %d paths, want 32", len(got))
			}

			dirs33 := append(append([]string{}, dirs...), tmpDirs(t, 1)[0])
			if setErr := reg.SetPaths(p, dirs33); setErr == nil {
				t.Fatal("SetPaths(33) succeeded, want error")
			} else if !errors.Is(setErr, settings.ErrValidation) {
				t.Errorf("error = %v, want ErrValidation", setErr)
			}
			got, err = reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if len(got) != 32 {
				t.Errorf("registry changed after rejected 33-path set: %d paths", len(got))
			}
		})
	}
}

func TestSetPaths_PersistenceReloadAndDisappearedDir(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			doc := storage.NewDocumentStore(t.TempDir())
			reg := settings.New(doc, &fakeSecretStore{})
			p := findPathList(t, reg, key)

			dirs := tmpDirs(t, 2)
			if err := reg.SetPaths(p, dirs); err != nil {
				t.Fatalf("SetPaths: %v", err)
			}

			// Reload from the same store: the canonical list survives.
			reg2 := settings.New(doc, &fakeSecretStore{})
			got, err := reg2.GetPaths(findPathList(t, reg2, key))
			if err != nil {
				t.Fatalf("GetPaths after reload: %v", err)
			}
			if !reflect.DeepEqual(got, dirs) {
				t.Errorf("reloaded paths = %v, want %v", got, dirs)
			}

			// A directory that disappears after a valid save stays visible on
			// reload (no re-stat), so a sandbox launch can fail closed on it
			// instead of silently dropping the requested baseline.
			if rmErr := os.Remove(dirs[1]); rmErr != nil {
				t.Fatalf("Remove: %v", rmErr)
			}
			reg3 := settings.New(doc, &fakeSecretStore{})
			got, err = reg3.GetPaths(findPathList(t, reg3, key))
			if err != nil {
				t.Fatalf("GetPaths after dir removal: %v", err)
			}
			if !reflect.DeepEqual(got, dirs) {
				t.Errorf("disappeared dir was dropped: %v, want it preserved", got)
			}
		})
	}
}

func TestSetPaths_ResetRestoresDefault(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			p := findPathList(t, reg, key)

			if err := reg.SetPaths(p, tmpDirs(t, 1)); err != nil {
				t.Fatalf("SetPaths: %v", err)
			}
			if err := reg.Reset(p); err != nil {
				t.Fatalf("Reset: %v", err)
			}
			got, err := reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("after reset got %v, want empty", got)
			}
		})
	}
}

func TestSetPaths_CopyIsolation(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			p := findPathList(t, reg, key)
			dirs := tmpDirs(t, 1)
			if err := reg.SetPaths(p, dirs); err != nil {
				t.Fatalf("SetPaths: %v", err)
			}

			// GetPaths returns a copy.
			got, err := reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			got[0] = "/corrupted"
			again, err := reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths again: %v", err)
			}
			if again[0] != dirs[0] {
				t.Errorf("registry mutated through GetPaths copy: %v", again)
			}

			// The snapshot hands out a copy too.
			snap, err := reg.GetSnapshot()
			if err != nil {
				t.Fatalf("GetSnapshot: %v", err)
			}
			if sv, ok := snap.Values[p.Key()].([]string); ok {
				sv[0] = "/corrupted"
			}
			again, err = reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if again[0] != dirs[0] {
				t.Errorf("registry mutated through snapshot copy: %v", again)
			}

			// NonSecretOverrides hands out a copy too.
			overrides := reg.NonSecretOverrides()
			if ov, ok := overrides[p.Key()].([]string); ok {
				ov[0] = "/corrupted"
			}
			again, err = reg.GetPaths(p)
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if again[0] != dirs[0] {
				t.Errorf("registry mutated through NonSecretOverrides copy: %v", again)
			}
		})
	}
}

func TestSetPaths_MalformedStoredTypeNeverEffective(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			tooMany := `{"schemaVersion":1,"values":{"` + key + `":[` +
				strings.Repeat(`"/x",`, 32) + `"/x"]},"secretRefs":{}}`
			cases := map[string]string{
				"object":   `{"schemaVersion":1,"values":{"` + key + `":{"a":"b"}},"secretRefs":{}}`,
				"string":   `{"schemaVersion":1,"values":{"` + key + `":"/tmp"},"secretRefs":{}}`,
				"mixed":    `{"schemaVersion":1,"values":{"` + key + `":["/a",123]},"secretRefs":{}}`,
				"too-many": tooMany,
			}
			for name, raw := range cases {
				t.Run(name, func(t *testing.T) {
					reg := settings.New(&fakeDoc{data: map[string][]byte{"settings.json": []byte(raw)}}, &fakeSecretStore{})
					p := findPathList(t, reg, key)
					// The typed getter refuses to hand back a corrupted list.
					if _, err := reg.GetPaths(p); err == nil {
						t.Fatal("GetPaths on corrupted stored value returned no error")
					}
					// The snapshot keeps the corruption observable (never []),
					// so the sandbox open path can fail closed rather than drop
					// the baseline.
					snap, err := reg.GetSnapshot()
					if err != nil {
						t.Fatalf("GetSnapshot: %v", err)
					}
					if _, ok := snap.Values[p.Key()].([]string); ok {
						t.Error("corrupted value was coerced to []string in snapshot")
					}
				})
			}
		})
	}
}

func TestApplyValues_RestoresPathList(t *testing.T) {
	for _, key := range pathListKeys() {
		t.Run(key, func(t *testing.T) {
			src := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			p := findPathList(t, src, key)
			dirs := tmpDirs(t, 2)
			if err := src.SetPaths(p, dirs); err != nil {
				t.Fatalf("SetPaths: %v", err)
			}
			snap, err := src.GetSnapshot()
			if err != nil {
				t.Fatalf("GetSnapshot: %v", err)
			}

			dst := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
			if applyErr := dst.ApplyValues(snap.Values); applyErr != nil {
				t.Fatalf("ApplyValues: %v", applyErr)
			}
			got, err := dst.GetPaths(findPathList(t, dst, key))
			if err != nil {
				t.Fatalf("GetPaths: %v", err)
			}
			if !reflect.DeepEqual(got, dirs) {
				t.Errorf("restored paths = %v, want %v", got, dirs)
			}
		})
	}
}

// ── Sandbox path-class conflict detection ───────────────────────────────

// tmpParentChild returns (parent, child) where child is a subdirectory of parent.
func tmpParentChild(t *testing.T) (parent, child string) {
	t.Helper()
	parent = filepath.Join(t.TempDir(), "parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("MkdirAll parent: %v", err)
	}
	child = filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatalf("MkdirAll child: %v", err)
	}
	return
}

func TestSetPaths_RejectsSandboxConflict(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	rw := findPathList(t, reg, "sandbox.allowedWritablePaths")
	ro := findPathList(t, reg, "sandbox.allowedReadOnlyPaths")

	parent, child := tmpParentChild(t)
	unrelated := tmpDirs(t, 1)[0]

	if err := reg.SetPaths(rw, []string{parent}); err != nil {
		t.Fatalf("SetPaths(rw): %v", err)
	}

	// RO == RW — rejected.
	err := reg.SetPaths(ro, []string{parent})
	if err == nil {
		t.Fatal("SetPaths(ro == rw) succeeded, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
	got, _ := reg.GetPaths(ro)
	if len(got) != 0 {
		t.Errorf("ro changed after rejected set: %v", got)
	}

	// RO child of RW — rejected.
	err = reg.SetPaths(ro, []string{child})
	if err == nil {
		t.Fatal("SetPaths(ro child of rw) succeeded, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}

	// RO unrelated — allowed.
	if err := reg.SetPaths(ro, []string{unrelated}); err != nil {
		t.Fatalf("SetPaths(ro unrelated): %v", err)
	}
}

func TestSetPaths_AllowsRWUnderRO(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	rw := findPathList(t, reg, "sandbox.allowedWritablePaths")
	ro := findPathList(t, reg, "sandbox.allowedReadOnlyPaths")

	parent, child := tmpParentChild(t)

	if err := reg.SetPaths(ro, []string{parent}); err != nil {
		t.Fatalf("SetPaths(ro): %v", err)
	}
	if err := reg.SetPaths(rw, []string{child}); err != nil {
		t.Fatalf("SetPaths(rw child under ro): %v", err)
	}

	gotRO, _ := reg.GetPaths(ro)
	if len(gotRO) != 1 || gotRO[0] != parent {
		t.Errorf("ro = %v, want [%s]", gotRO, parent)
	}
	gotRW, _ := reg.GetPaths(rw)
	if len(gotRW) != 1 || gotRW[0] != child {
		t.Errorf("rw = %v, want [%s]", gotRW, child)
	}
}

func TestSetPaths_RejectsRWConflictReverse(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	rw := findPathList(t, reg, "sandbox.allowedWritablePaths")
	ro := findPathList(t, reg, "sandbox.allowedReadOnlyPaths")

	parent, child := tmpParentChild(t)

	if err := reg.SetPaths(ro, []string{child}); err != nil {
		t.Fatalf("SetPaths(ro): %v", err)
	}
	err := reg.SetPaths(rw, []string{parent})
	if err == nil {
		t.Fatal("SetPaths(rw encompassing ro) succeeded, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
	got, _ := reg.GetPaths(rw)
	if len(got) != 0 {
		t.Errorf("rw changed after rejected set: %v", got)
	}
}

func TestReplaceNonSecretOverrides_RejectsSandboxConflict(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})

	parent, child := tmpParentChild(t)

	_, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"sandbox.allowedWritablePaths": []string{parent},
		"sandbox.allowedReadOnlyPaths": []string{parent},
	})
	if err == nil {
		t.Fatal("ReplaceNonSecretOverrides with conflict succeeded, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}

	gotRO, _ := reg.GetPaths(findPathList(t, reg, "sandbox.allowedReadOnlyPaths"))
	if len(gotRO) != 0 {
		t.Errorf("ro changed after rejected bulk replace: %v", gotRO)
	}
	gotRW, _ := reg.GetPaths(findPathList(t, reg, "sandbox.allowedWritablePaths"))
	if len(gotRW) != 0 {
		t.Errorf("rw changed after rejected bulk replace: %v", gotRW)
	}

	// RO below RW — rejected.
	_, err = reg.ReplaceNonSecretOverrides(map[string]any{
		"sandbox.allowedWritablePaths": []string{parent},
		"sandbox.allowedReadOnlyPaths": []string{child},
	})
	if err == nil {
		t.Fatal("ReplaceNonSecretOverrides with RO-below-RW succeeded, want error")
	}

	// RW under RO — allowed.
	_, err = reg.ReplaceNonSecretOverrides(map[string]any{
		"sandbox.allowedReadOnlyPaths": []string{parent},
		"sandbox.allowedWritablePaths": []string{child},
	})
	if err != nil {
		t.Fatalf("ReplaceNonSecretOverrides with RW-under-RO: %v", err)
	}
}

func TestApplyValues_RejectsSandboxConflict(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})

	parent, child := tmpParentChild(t)

	err := reg.ApplyValues(map[string]any{
		"sandbox.allowedWritablePaths": []string{parent},
		"sandbox.allowedReadOnlyPaths": []string{parent},
	})
	if err == nil {
		t.Fatal("ApplyValues with conflict succeeded, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}

	gotRO, _ := reg.GetPaths(findPathList(t, reg, "sandbox.allowedReadOnlyPaths"))
	if len(gotRO) != 0 {
		t.Errorf("ro changed after rejected ApplyValues: %v", gotRO)
	}

	// RW under RO — allowed.
	err = reg.ApplyValues(map[string]any{
		"sandbox.allowedReadOnlyPaths": []string{parent},
		"sandbox.allowedWritablePaths": []string{child},
	})
	if err != nil {
		t.Fatalf("ApplyValues with RW-under-RO: %v", err)
	}
}

func TestApplyValues_AllowsRWUnderRO(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})

	parent, child := tmpParentChild(t)

	err := reg.ApplyValues(map[string]any{
		"sandbox.allowedReadOnlyPaths": []string{parent},
		"sandbox.allowedWritablePaths": []string{child},
	})
	if err != nil {
		t.Fatalf("ApplyValues: %v", err)
	}

	gotRO, _ := reg.GetPaths(findPathList(t, reg, "sandbox.allowedReadOnlyPaths"))
	if len(gotRO) != 1 || gotRO[0] != parent {
		t.Errorf("ro = %v, want [%s]", gotRO, parent)
	}
	gotRW, _ := reg.GetPaths(findPathList(t, reg, "sandbox.allowedWritablePaths"))
	if len(gotRW) != 1 || gotRW[0] != child {
		t.Errorf("rw = %v, want [%s]", gotRW, child)
	}
}

func TestReset_NoFalseSandboxConflict(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	rw := findPathList(t, reg, "sandbox.allowedWritablePaths")
	ro := findPathList(t, reg, "sandbox.allowedReadOnlyPaths")

	parent, child := tmpParentChild(t)

	if err := reg.SetPaths(ro, []string{parent}); err != nil {
		t.Fatalf("SetPaths(ro): %v", err)
	}
	if err := reg.SetPaths(rw, []string{child}); err != nil {
		t.Fatalf("SetPaths(rw): %v", err)
	}

	if err := reg.Reset(ro); err != nil {
		t.Fatalf("Reset(ro): %v", err)
	}
	gotRW, _ := reg.GetPaths(rw)
	if len(gotRW) != 1 || gotRW[0] != child {
		t.Errorf("rw changed after ro reset: %v", gotRW)
	}

	if err := reg.Reset(rw); err != nil {
		t.Fatalf("Reset(rw): %v", err)
	}
	gotRW, _ = reg.GetPaths(rw)
	if len(gotRW) != 0 {
		t.Errorf("rw not empty after reset: %v", gotRW)
	}
}

func TestNonSandboxPathsUnaffectedByConflictCheck(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})

	if err := reg.SetBool(settings.HistoryEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	got, _ := reg.GetBool(settings.HistoryEnabled)
	if !got {
		t.Error("HistoryEnabled should be true")
	}

	if err := reg.Reset(settings.HistoryEnabled); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	_, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"history.enabled": true,
	})
	if err != nil {
		t.Fatalf("ReplaceNonSecretOverrides: %v", err)
	}
}

func TestSetPaths_SymlinkConflict(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	rw := findPathList(t, reg, "sandbox.allowedWritablePaths")
	ro := findPathList(t, reg, "sandbox.allowedReadOnlyPaths")

	target := tmpDirs(t, 1)[0]
	link := filepath.Join(filepath.Dir(target), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if err := reg.SetPaths(rw, []string{target}); err != nil {
		t.Fatalf("SetPaths(rw): %v", err)
	}

	err := reg.SetPaths(ro, []string{link})
	if err == nil {
		t.Fatal("SetPaths(ro symlink to rw) succeeded, want error")
	}
	if !errors.Is(err, settings.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
}

// TestCanonicalPaths_CaseInsensitiveAliasDedupe verifies that canonicalPaths
// collapses filesystem-equivalent case aliases first-wins. On case-sensitive
// filesystems the test is skipped.
func TestCanonicalPaths_CaseInsensitiveAliasDedupe(t *testing.T) {
	dir := t.TempDir()
	lower := filepath.Join(dir, "dedupetest")
	upper := filepath.Join(dir, "DEDUPETEST")
	if err := os.MkdirAll(lower, 0o750); err != nil {
		t.Fatalf("mkdir lower: %v", err)
	}
	li, errL := os.Stat(lower)
	ui, errU := os.Stat(upper)
	if errL != nil || errU != nil || !os.SameFile(li, ui) {
		t.Skip("skipping case-insensitive alias test: filesystem is case-sensitive")
	}

	reg := settings.New(storage.NewDocumentStore(t.TempDir()), &fakeSecretStore{})
	rw := findPathList(t, reg, "sandbox.allowedWritablePaths")
	if err := reg.SetPaths(rw, []string{lower, upper}); err != nil {
		t.Fatalf("SetPaths: %v", err)
	}
	got, err := reg.GetPaths(rw)
	if err != nil {
		t.Fatalf("GetPaths: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetPaths: got %d entries, want 1 (first-wins dedupe): %v", len(got), got)
	}
}

func TestAppendSandboxPathIsAtomicAndIdempotent(t *testing.T) {
	doc := storage.NewDocumentStore(t.TempDir())
	reg := settings.New(doc, &fakeSecretStore{})
	dirs := tmpDirs(t, 2)
	first, second := dirs[0], dirs[1]

	rev1, err := reg.AppendSandboxPath(settings.SandboxAllowedReadOnlyPaths, first)
	if err != nil {
		t.Fatalf("AppendSandboxPath(first): %v", err)
	}
	rev2, err := reg.AppendSandboxPath(settings.SandboxAllowedReadOnlyPaths, second)
	if err != nil {
		t.Fatalf("AppendSandboxPath(second): %v", err)
	}
	rev3, err := reg.AppendSandboxPath(settings.SandboxAllowedReadOnlyPaths, first)
	if err != nil {
		t.Fatalf("AppendSandboxPath(duplicate): %v", err)
	}
	got, err := reg.GetPaths(settings.SandboxAllowedReadOnlyPaths)
	if err != nil {
		t.Fatalf("GetPaths: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("paths = %v, want two unique entries", got)
	}
	if rev2 <= rev1 || rev3 != rev2 {
		t.Fatalf("revisions = %d, %d, %d; duplicate must not bump", rev1, rev2, rev3)
	}

	reloaded := settings.New(doc, &fakeSecretStore{})
	persisted, err := reloaded.GetPaths(settings.SandboxAllowedReadOnlyPaths)
	if err != nil || !reflect.DeepEqual(got, persisted) {
		t.Fatalf("persisted = %v, err = %v; want %v", persisted, err, got)
	}
}
