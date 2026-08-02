package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/settings"
)

// fakeDoc is an in-memory DocumentStore for testing.
type fakeDoc struct {
	data map[string][]byte
}

func (f *fakeDoc) Read(name string, into any) (bool, error) {
	b, ok := f.data[name]
	if !ok || b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, into)
}

func (f *fakeDoc) Write(name string, doc any) error {
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

// ── Non-secret override snapshot (ADR-0018) ────────────────────────────

type notifierTracker struct {
	calls [][]string
}

func (n *notifierTracker) track(_ int, keys []string) {
	n.calls = append(n.calls, keys)
}

func TestNonSecretOverrides_ExcludesSecrets(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	// Set a non-secret and a secret setting.
	cb := findBool(t, reg, "clipboard.osc52Suppressed")
	if err := reg.SetBool(cb, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	testSecret := findSecret(t, reg, "test.secretExample")
	if err := reg.SecretSet(testSecret, "s3cret"); err != nil {
		t.Fatalf("SecretSet: %v", err)
	}

	overrides := reg.NonSecretOverrides()
	if _, hasSecret := overrides["test.secretExample"]; hasSecret {
		t.Error("SecretAuthenticator key should not appear in overrides")
	}
	if v, ok := overrides["clipboard.osc52Suppressed"]; !ok || v != true {
		t.Errorf("non-secret override missing or wrong: %v", overrides)
	}
}

func TestNonSecretOverrides_ExcludesDefaults(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	overrides := reg.NonSecretOverrides()
	if len(overrides) != 0 {
		t.Errorf("fresh registry should have no overrides, got %d", len(overrides))
	}
}

func TestReplaceNonSecretOverrides_ValidBatch(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	cb := findBool(t, reg, "clipboard.osc52Suppressed")
	sel := findSelect(t, reg, "tab.placement")

	pn, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"clipboard.osc52Suppressed": true,
		"tab.placement":             "vertical",
	})
	if err != nil {
		t.Fatalf("ReplaceNonSecretOverrides: %v", err)
	}

	// Check values are applied.
	v, _ := reg.GetBool(cb)
	if !v {
		t.Error("bool override not applied")
	}
	s, _ := reg.GetSelect(sel)
	if s != "vertical" {
		t.Errorf("select override not applied: %q", s)
	}

	// Notifier should NOT fire yet (but we haven't set one, so just publish).
	reg.Publish(pn)
}

func TestReplaceNonSecretOverrides_RejectsSecretClass(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	_, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"test.secretExample": "value",
	})
	if err == nil {
		t.Fatal("expected error for secret-class key")
	}
}

func TestReplaceNonSecretOverrides_InvalidBatchNoPartialWrite(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	cb := findBool(t, reg, "clipboard.osc52Suppressed")
	_ = reg.SetBool(cb, false)

	_, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"clipboard.osc52Suppressed": true,
		"test.secretExample":        "value", // invalid
	})
	if err == nil {
		t.Fatal("expected error for invalid batch")
	}

	// First key must NOT be partially written.
	v, _ := reg.GetBool(cb)
	if v {
		t.Error("bool should not have been changed after invalid batch")
	}
}

func TestReplaceNonSecretOverrides_NotifierAfterPublish(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	var tracker notifierTracker
	reg.SetNotifier(tracker.track)

	pn, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"clipboard.osc52Suppressed": true,
	})
	if err != nil {
		t.Fatalf("ReplaceNonSecretOverrides: %v", err)
	}

	// Notifier should NOT have fired yet.
	if len(tracker.calls) != 0 {
		t.Errorf("notifier fired before Publish: %v", tracker.calls)
	}

	reg.Publish(pn)

	if len(tracker.calls) != 1 {
		t.Fatalf("notifier should have fired once after Publish, got %d", len(tracker.calls))
	}
}

func TestReplaceNonSecretOverrides_SecretRefsSurvive(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	testSecret := findSecret(t, reg, "test.secretExample")
	if err := reg.SecretSet(testSecret, "s3cret"); err != nil {
		t.Fatalf("SecretSet: %v", err)
	}

	_, err := reg.ReplaceNonSecretOverrides(map[string]any{
		"clipboard.osc52Suppressed": true,
	})
	if err != nil {
		t.Fatalf("ReplaceNonSecretOverrides: %v", err)
	}

	exists, err := reg.SecretExists(testSecret)
	if err != nil || !exists {
		t.Errorf("secret ref should survive replace: exists=%v err=%v", exists, err)
	}
}

func TestReplaceNonSecretOverrides_ResetsAbsentKeys(t *testing.T) {
	doc := &fakeDoc{}
	sec := &fakeSecretStore{}
	reg := settings.New(doc, sec)

	cb := findBool(t, reg, "clipboard.osc52Suppressed")
	_ = reg.SetBool(cb, true)

	// Replace with empty set — clipboard.osc52Suppressed should reset.
	_, err := reg.ReplaceNonSecretOverrides(map[string]any{})
	if err != nil {
		t.Fatalf("ReplaceNonSecretOverrides: %v", err)
	}

	v, _ := reg.GetBool(cb)
	if v {
		t.Error("bool should have been reset to default after empty replace")
	}
}
