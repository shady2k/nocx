package apibind

// Design §8, §8.2, §12.2. The binding document is the ONLY place an
// identifier for stored credential material exists for this feature, and
// the two-store write it performs is stated as an invariant with BOTH ends:
//
//	A vault value for a secret variable exists from before its binding is
//	written until the collection that declares that variable is deleted.
//	Deleting a collection deletes the bindings it owns and the values only
//	those bindings referenced.
//
// The opening end is Bind's ordering; the CLOSING end is UnbindCollection,
// and it is real rather than aspirational — an invariant named only at its
// start buys a test that guards only its start.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/vault"
)

// ─── a fake vault ──────────────────────────────────────────────────────────

// fakeSecrets is the vault, narrowed to what a binding needs, and it counts
// every call. The counts are the assertions in the tests that matter: "the
// vault was never asked" is a stronger claim than "a guard refused".
type fakeSecrets struct {
	mu      sync.Mutex
	values  map[credential.SecretID]string
	names   map[credential.SecretID]string
	next    int
	gets    int
	creates int
	deletes int

	createErr error
	deleteErr error
	getErr    error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{
		values: map[credential.SecretID]string{},
		names:  map[credential.SecretID]string{},
	}
}

func (f *fakeSecrets) CreateNamed(_ context.Context, v credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.createErr != nil {
		return "", f.createErr
	}
	f.next++
	id := credential.SecretID(idFor(f.next))
	var plain string
	_ = v.Use(func(b []byte) error { plain = string(b); return nil })
	f.values[id] = plain
	f.names[id] = meta.Name
	return id, nil
}

func (f *fakeSecrets) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.getErr != nil {
		return credential.Secret{}, f.getErr
	}
	v, ok := f.values[id]
	if !ok {
		// The vault's own contract: an absent id is an empty Secret and a
		// nil error, not a failure.
		return credential.Secret{}, nil
	}
	return credential.NewSecret(v), nil
}

func (f *fakeSecrets) Delete(_ context.Context, id credential.SecretID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.values, id)
	delete(f.names, id)
	return nil
}

func (f *fakeSecrets) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.values[credential.SecretID(id)]
	return ok
}

func (f *fakeSecrets) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.values)
}

func (f *fakeSecrets) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func (f *fakeSecrets) nameOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.names[credential.SecretID(id)]
}

func idFor(n int) string { return "fake:" + string(rune('a'+n-1)) }

// ─── a fake document store ─────────────────────────────────────────────────

// fakeDocs is storage.DocumentStore in memory, with a switch for making the
// write fail — which is the half of the two-store write §12.2 chooses to
// risk, so it has to be exercised.
type fakeDocs struct {
	mu       sync.Mutex
	docs     map[string][]byte
	writeErr error
	readErr  error
	writes   int
}

func newFakeDocs() *fakeDocs { return &fakeDocs{docs: map[string][]byte{}} }

func (d *fakeDocs) Read(name string, into any) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.readErr != nil {
		return false, d.readErr
	}
	raw, ok := d.docs[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, into)
}

func (d *fakeDocs) Write(name string, doc any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes++
	if d.writeErr != nil {
		return d.writeErr
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	d.docs[name] = raw
	return nil
}

func (d *fakeDocs) Delete(name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.docs, name)
	return nil
}

func (d *fakeDocs) raw(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.docs[name])
}

// seed writes a binding document directly, which is how a document that
// arrived from an earlier build — or from a hand edit — is modelled.
func (d *fakeDocs) seed(t *testing.T, bindings ...record) {
	t.Helper()
	raw, err := json.Marshal(bindingDocument{SchemaVersion: Module.Current, Bindings: bindings})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.docs[DocumentName] = raw
}

func newTestStore(t *testing.T) (*JSONStore, *fakeDocs, *fakeSecrets) {
	t.Helper()
	docs, secrets := newFakeDocs(), newFakeSecrets()
	return NewStore(docs, secrets), docs, secrets
}

func key(collection, env, variable string) Key {
	return Key{Collection: collection, Environment: env, Variable: variable}
}

// ─── the surface ───────────────────────────────────────────────────────────

func TestNewStore_SatisfiesStore(t *testing.T) {
	var _ Store = NewStore(newFakeDocs(), newFakeSecrets())
	var _ ValueResolver = NewStore(newFakeDocs(), newFakeSecrets())
}

// TestBindAndLookup_RoundTrip is what a user can now do that they could
// not: supply a value for a variable a shared collection names, and have
// the request resolve it.
func TestBindAndLookup_RoundTrip(t *testing.T) {
	s, _, secrets := newTestStore(t)
	k := key("/c/api", "prod", "token")

	if err := s.Bind(context.Background(), k, []byte("t0k3n")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	id, found, err := s.Lookup(k)
	if err != nil || !found {
		t.Fatalf("Lookup = %q, %v, %v; want the binding", id, found, err)
	}
	if !secrets.has(id) {
		t.Errorf("the vault holds no value for %q", id)
	}
	v, found, err := s.Resolve(context.Background(), k)
	if err != nil || !found {
		t.Fatalf("Resolve = %v, %v", found, err)
	}
	var got string
	_ = v.Use(func(b []byte) error { got = string(b); return nil })
	if got != "t0k3n" {
		t.Errorf("Resolve gave %q, want the bound value", got)
	}
}

// TestLookup_AnUnboundVariableIsNotAnError is §6.5's mechanism: "not bound"
// is a NORMAL state that blocks the send, never a failure of the store.
// Flattening the two would tell a user to unlock a vault that is open.
func TestLookup_AnUnboundVariableIsNotAnError(t *testing.T) {
	s, _, _ := newTestStore(t)
	id, found, err := s.Lookup(key("/c", "prod", "nope"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found || id != "" {
		t.Errorf("Lookup = %q, %v; want nothing found", id, found)
	}
}

// TestLookup_IsKeyedByCollectionAndEnvironment: two collections with a
// variable of the same name do not share a value, which is why the key is a
// triple rather than a variable name. A hostile collection writing
// `{{token}}` gets whatever the reader bound IN THEIR OWN environment —
// nothing else.
func TestLookup_IsKeyedByCollectionAndEnvironment(t *testing.T) {
	s, _, _ := newTestStore(t)
	mine := key("/home/me/mine", "prod", "token")
	theirs := key("/home/me/from-a-pull-request", "prod", "token")
	staging := key("/home/me/mine", "staging", "token")

	if err := s.Bind(context.Background(), mine, []byte("mine")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for name, k := range map[string]Key{"another collection": theirs, "another environment": staging} {
		if _, found, err := s.Lookup(k); err != nil || found {
			t.Errorf("%s resolved token (found=%v, err=%v); the key is a triple for exactly this reason", name, found, err)
		}
	}
}

// TestResolve_AVaultIdentifierIsJustAnUnknownVariableName is §8 at the
// store: a file whose variable name IS a raw vault identifier belonging to
// an SSH profile resolves to nothing, and — the assertion that matters —
// THE VAULT IS NEVER ASKED. There is no path from a name to a value except
// through a binding somebody made, so the identifier's shape buys the file
// nothing. That is a stronger property than a check that inspects it: §8
// rejected the draft in which a resolver refused cross-scope references,
// because a guard bolted onto a permitting format is not the argument.
func TestResolve_AVaultIdentifierIsJustAnUnknownVariableName(t *testing.T) {
	s, _, secrets := newTestStore(t)
	ctx := context.Background()
	// The SSH profile's passphrase is in the vault, as it would really be.
	secrets.mu.Lock()
	secrets.values["keychain:nocx-ssh-prod-bastion"] = "the bastion passphrase"
	secrets.mu.Unlock()
	// And the reader has bound their own token, so the store is live.
	if err := s.Bind(ctx, key("/c", "prod", "token"), []byte("mine")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	before := secrets.getCount()

	hostile := key("/c", "prod", "keychain:nocx-ssh-prod-bastion")
	if id, found, err := s.Lookup(hostile); err != nil || found {
		t.Fatalf("Lookup = %q, %v, %v; want nothing bound under that name", id, found, err)
	}
	if _, found, err := s.Resolve(ctx, hostile); err != nil || found {
		t.Fatalf("Resolve = %v, %v; want nothing", found, err)
	}
	if got := secrets.getCount(); got != before {
		t.Errorf("the vault was read %d times for an unbound name, want 0 — the resolution goes through a binding or nowhere", got-before)
	}
	// The value is still there. Nothing was spent, and nothing was refused
	// either; there was simply no path.
	if !secrets.has("keychain:nocx-ssh-prod-bastion") {
		t.Error("the SSH profile's secret was touched")
	}
}

// ─── §8.2 and §12.2: the value first, the binding second ───────────────────

// TestBind_WritesTheValueBeforeTheBinding pins the ORDER, not its
// consequence — the consequence is tested by the two failure cases below,
// and this is what makes them meaningful.
func TestBind_WritesTheValueBeforeTheBinding(t *testing.T) {
	docs, secrets := newFakeDocs(), newFakeSecrets()
	var order []string
	obs := &observingDocs{fakeDocs: docs, onWrite: func() { order = append(order, "binding") }}
	obsSecrets := &observingSecrets{fakeSecrets: secrets, onCreate: func() { order = append(order, "value") }}

	if err := NewStore(obs, obsSecrets).Bind(context.Background(), key("/c", "e", "v"), []byte("x")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(order) != 2 || order[0] != "value" || order[1] != "binding" {
		t.Fatalf("order = %v, want the value first and the binding second (§8.2)", order)
	}
}

// TestBind_WhenTheValueWriteFails: nothing is written at all. There is no
// binding pointing at a value that is not there — which is the failure
// §12.2 refuses to risk, because it is a variable that reports itself
// unresolved on every send until somebody rebinds it.
func TestBind_WhenTheValueWriteFails(t *testing.T) {
	s, docs, secrets := newTestStore(t)
	secrets.createErr = errors.New("vault is sealed")

	err := s.Bind(context.Background(), key("/c", "e", "v"), []byte("x"))
	if !errors.Is(err, secrets.createErr) {
		t.Fatalf("Bind returned %v, want the vault's own failure", err)
	}
	if _, found, _ := s.Lookup(key("/c", "e", "v")); found {
		t.Error("a binding was written for a value that was never stored")
	}
	if docs.raw(DocumentName) != "" {
		t.Errorf("the binding document was written: %s", docs.raw(DocumentName))
	}
}

// TestBind_WhenTheBindingWriteFails is the failure §12.2 DOES choose to
// risk, and it is chosen because it is benign: a vault value nobody can
// reach is dead weight, while a binding naming a value that is not there
// blocks every send.
//
// The cleanup here is OURS, not reconciliation's. internal/vault/journal.go:119
// clears the journal entry and KEEPS the secret whenever a catalogue record
// exists, and CreateNamed writes value and record in the same save
// (vault.go:1122) — so a create that returned is exactly the state
// reconciliation treats as complete, and nothing upstream will ever collect
// it.
func TestBind_WhenTheBindingWriteFails(t *testing.T) {
	s, docs, secrets := newTestStore(t)
	docs.writeErr = errors.New("disk is full")

	err := s.Bind(context.Background(), key("/c", "e", "v"), []byte("x"))
	if !errors.Is(err, docs.writeErr) {
		t.Fatalf("Bind returned %v, want the document store's failure", err)
	}
	if _, found, _ := s.Lookup(key("/c", "e", "v")); found {
		t.Error("Lookup found a binding after the binding write failed")
	}
	// Best-effort cleanup of the unreachable value, because nothing else
	// will do it.
	if secrets.count() != 0 {
		t.Errorf("%d values left in the vault, want the unreachable one collected here", secrets.count())
	}
}

// TestBind_WhenTheBindingWriteAndTheCleanupBothFail: the value survives as
// unreachable dead weight, and the error the CALLER sees is still the one
// that describes what went wrong with their bind. A cleanup failure must
// not replace it — the user's remedy is "the disk is full", not "a delete
// failed".
func TestBind_WhenTheBindingWriteAndTheCleanupBothFail(t *testing.T) {
	s, docs, secrets := newTestStore(t)
	docs.writeErr = errors.New("disk is full")
	secrets.deleteErr = errors.New("keychain refused")

	err := s.Bind(context.Background(), key("/c", "e", "v"), []byte("x"))
	if !errors.Is(err, docs.writeErr) {
		t.Fatalf("Bind returned %v, want the document store's failure", err)
	}
	if secrets.count() != 1 {
		t.Errorf("%d values, want the orphan left behind — unreachable, harmless, and collectable later", secrets.count())
	}
}

// TestBind_ReplacingAValueNeverLeavesTheBindingPointingAtNothing: the same
// ordering on the replace path. The new value lands, the binding is
// repointed, and only then is the old value removed — so at no instant does
// the binding name something that is not there.
func TestBind_ReplacingAValueNeverLeavesTheBindingPointingAtNothing(t *testing.T) {
	s, _, secrets := newTestStore(t)
	k := key("/c", "e", "v")
	if err := s.Bind(context.Background(), k, []byte("old")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	oldID, _, _ := s.Lookup(k)

	if err := s.Bind(context.Background(), k, []byte("new")); err != nil {
		t.Fatalf("Bind (replace): %v", err)
	}
	newID, found, err := s.Lookup(k)
	if err != nil || !found {
		t.Fatalf("Lookup after replace = %v, %v", found, err)
	}
	if newID == oldID {
		t.Fatal("the replace reused the id; the value is written first, so it is a new one")
	}
	if secrets.has(oldID) {
		t.Error("the replaced value is still in the vault; nothing references it")
	}
	if !secrets.has(newID) {
		t.Error("the new value is not in the vault")
	}
	if secrets.count() != 1 {
		t.Errorf("%d values after a replace, want 1", secrets.count())
	}
}

// TestBind_ARebindWithTheSameValueStillReplaces: the second bind is a
// different secret, and the first is gone. Not an optimisation — comparing
// values would mean reading the old one back, which is a vault read on a
// path that does not need one.
func TestBind_ARebindWithTheSameValueStillReplaces(t *testing.T) {
	s, _, secrets := newTestStore(t)
	k := key("/c", "e", "v")
	for i := 0; i < 3; i++ {
		if err := s.Bind(context.Background(), k, []byte("same")); err != nil {
			t.Fatalf("Bind %d: %v", i, err)
		}
	}
	if secrets.count() != 1 {
		t.Errorf("%d values after three binds of one variable, want 1", secrets.count())
	}
}

// TestBind_RefusesAnIncompleteKey: a key missing any of its three parts is
// a binding nothing could ever look up, and writing one would put a row in
// the document that only a hand edit could reach.
func TestBind_RefusesAnIncompleteKey(t *testing.T) {
	s, _, secrets := newTestStore(t)
	for name, k := range map[string]Key{
		"no collection":  key("", "e", "v"),
		"no environment": key("/c", "", "v"),
		"no variable":    key("/c", "e", ""),
	} {
		if err := s.Bind(context.Background(), k, []byte("x")); err == nil {
			t.Errorf("Bind(%s) succeeded, want it refused", name)
		}
	}
	if secrets.creates != 0 {
		t.Errorf("the vault was written %d times for keys that were refused, want 0", secrets.creates)
	}
}

// TestBind_NamesTheSecretForAPersonWithoutLeakingAPath: an orphaned value
// is only collectable later if somebody can SEE it, and the Secrets page is
// where they would. The label names the collection by its folder name
// rather than its path — the vault catalogue is not the place to record
// where on this machine somebody keeps their work.
func TestBind_NamesTheSecretForAPersonWithoutLeakingAPath(t *testing.T) {
	s, _, secrets := newTestStore(t)
	k := key("/home/someone/private/work/api", "prod", "token")
	if err := s.Bind(context.Background(), k, []byte("x")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	id, _, _ := s.Lookup(k)
	name := secrets.nameOf(id)
	for _, want := range []string{"api", "prod", "token"} {
		if !strings.Contains(name, want) {
			t.Errorf("secret name %q does not contain %q", name, want)
		}
	}
	if strings.Contains(name, "/home/someone") {
		t.Errorf("secret name %q carries the collection's path", name)
	}
}

// TestBind_NeverWritesTheValueIntoTheBindingDocument: the document holds an
// IDENTIFIER, never the material. A document that carried the value would
// be a second secret store with no lifecycle, no backup and no unlock —
// which is what AD-8 exists to prevent (§8.1).
func TestBind_NeverWritesTheValueIntoTheBindingDocument(t *testing.T) {
	s, docs, _ := newTestStore(t)
	if err := s.Bind(context.Background(), key("/c", "e", "v"), []byte("s3cr3t-material")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if strings.Contains(docs.raw(DocumentName), "s3cr3t-material") {
		t.Fatalf("the binding document carries the value: %s", docs.raw(DocumentName))
	}
}

// ─── the closing event ─────────────────────────────────────────────────────

// TestUnbindCollection_IsTheClosingEvent is §12.2's invariant at its far
// end: deleting a collection deletes the bindings it owns AND the values
// only those bindings referenced.
func TestUnbindCollection_IsTheClosingEvent(t *testing.T) {
	s, _, secrets := newTestStore(t)
	ctx := context.Background()
	mine := "/c/mine"
	other := "/c/other"
	for _, k := range []Key{
		key(mine, "prod", "token"),
		key(mine, "staging", "token"),
		key(other, "prod", "token"),
	} {
		if err := s.Bind(ctx, k, []byte("x")); err != nil {
			t.Fatalf("Bind: %v", err)
		}
	}
	keptID, _, _ := s.Lookup(key(other, "prod", "token"))

	if err := s.UnbindCollection(ctx, mine); err != nil {
		t.Fatalf("UnbindCollection: %v", err)
	}
	for _, k := range []Key{key(mine, "prod", "token"), key(mine, "staging", "token")} {
		if _, found, _ := s.Lookup(k); found {
			t.Errorf("%v survived the deletion of its collection", k)
		}
	}
	if _, found, _ := s.Lookup(key(other, "prod", "token")); !found {
		t.Error("another collection's binding was deleted too")
	}
	if secrets.count() != 1 || !secrets.has(keptID) {
		t.Errorf("%d values left, want only the other collection's", secrets.count())
	}
}

// TestUnbindCollection_AValueSharedWithAnotherBindingSurvives is the rule
// that makes "the values ONLY those bindings referenced" a real clause
// rather than decoration. A document can name one value from two bindings —
// from an earlier build, from a hand edit, from a future dedup — and
// deleting one collection must not spend the other's credential.
func TestUnbindCollection_AValueSharedWithAnotherBindingSurvives(t *testing.T) {
	docs, secrets := newFakeDocs(), newFakeSecrets()
	shared := "fake:shared"
	lonely := "fake:lonely"
	secrets.mu.Lock()
	secrets.values[credential.SecretID(shared)] = "s"
	secrets.values[credential.SecretID(lonely)] = "l"
	secrets.mu.Unlock()
	docs.seed(t,
		record{Collection: "/c/going", Environment: "prod", Variable: "token", SecretID: shared},
		record{Collection: "/c/going", Environment: "prod", Variable: "other", SecretID: lonely},
		record{Collection: "/c/staying", Environment: "prod", Variable: "token", SecretID: shared},
	)
	s := NewStore(docs, secrets)

	if err := s.UnbindCollection(context.Background(), "/c/going"); err != nil {
		t.Fatalf("UnbindCollection: %v", err)
	}
	if !secrets.has(shared) {
		t.Error("a value another binding still references was deleted")
	}
	if secrets.has(lonely) {
		t.Error("a value only the deleted bindings referenced survived")
	}
	if _, found, _ := s.Lookup(key("/c/staying", "prod", "token")); !found {
		t.Error("the surviving binding is gone")
	}
}

// TestUnbind_RemovesTheBindingBeforeTheValue: the mirror of Bind's
// ordering, and it picks the same benign failure. If the removal is
// interrupted, what is left is an unreachable value — never a binding
// naming a value that has already been deleted.
func TestUnbind_RemovesTheBindingBeforeTheValue(t *testing.T) {
	docs, secrets := newFakeDocs(), newFakeSecrets()
	var order []string
	obs := &observingDocs{fakeDocs: docs, onWrite: func() { order = append(order, "binding") }}
	obsSecrets := &observingSecrets{fakeSecrets: secrets, onDelete: func() { order = append(order, "value") }}
	s := NewStore(obs, obsSecrets)
	k := key("/c", "e", "v")
	if err := s.Bind(context.Background(), k, []byte("x")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	order = nil

	if err := s.Unbind(context.Background(), k); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if len(order) != 2 || order[0] != "binding" || order[1] != "value" {
		t.Fatalf("order = %v, want the binding removed first", order)
	}
	if secrets.count() != 0 {
		t.Errorf("%d values left after Unbind, want 0", secrets.count())
	}
}

// TestUnbind_AnAbsentBindingSucceeds: absence is the desired end state, and
// an operation that removes several bindings must be safe to re-run after
// being interrupted part-way through — the same contract
// storage.DocumentStore.Delete signs.
func TestUnbind_AnAbsentBindingSucceeds(t *testing.T) {
	s, _, secrets := newTestStore(t)
	if err := s.Unbind(context.Background(), key("/c", "e", "nope")); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if err := s.UnbindCollection(context.Background(), "/c/never-bound"); err != nil {
		t.Fatalf("UnbindCollection: %v", err)
	}
	if secrets.deletes != 0 {
		t.Errorf("the vault was asked to delete %d times for bindings that did not exist, want 0", secrets.deletes)
	}
}

// TestUnbindCollection_ReportsValuesItCouldNotRemove: deletion is the whole
// point of the call, so a value that could not be removed is REPORTED
// rather than logged and forgotten. A soft degrade must be visible in the
// product (AGENTS.md) — and here it also is: the leftover carries the
// collection's name in the vault catalogue, so it can be found and removed.
func TestUnbindCollection_ReportsValuesItCouldNotRemove(t *testing.T) {
	s, _, secrets := newTestStore(t)
	ctx := context.Background()
	if err := s.Bind(ctx, key("/c", "e", "v"), []byte("x")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	secrets.deleteErr = errors.New("keychain refused")

	err := s.UnbindCollection(ctx, "/c")
	if err == nil {
		t.Fatal("UnbindCollection succeeded while a value could not be removed")
	}
	// The bindings ARE gone: the primary effect landed and is not undone
	// by a failure downstream of it.
	if _, found, lookupErr := s.Lookup(key("/c", "e", "v")); found || lookupErr != nil {
		t.Errorf("the binding survived a failed value delete (found=%v, err=%v)", found, lookupErr)
	}
}

// TestUnbindCollection_WhenTheDocumentWriteFails: the bindings stay, and so
// do the values. Nothing is deleted on the strength of a write that did not
// land — the failure leaves the collection exactly as it was.
func TestUnbindCollection_WhenTheDocumentWriteFails(t *testing.T) {
	s, docs, secrets := newTestStore(t)
	ctx := context.Background()
	if err := s.Bind(ctx, key("/c", "e", "v"), []byte("x")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	docs.writeErr = errors.New("disk is full")

	if err := s.UnbindCollection(ctx, "/c"); !errors.Is(err, docs.writeErr) {
		t.Fatalf("UnbindCollection returned %v, want the write failure", err)
	}
	docs.writeErr = nil
	if _, found, _ := s.Lookup(key("/c", "e", "v")); !found {
		t.Error("the binding is gone although the document write failed")
	}
	if secrets.count() != 1 {
		t.Errorf("%d values, want the value kept — nothing is deleted on a write that did not land", secrets.count())
	}
}

// ─── reading the document ──────────────────────────────────────────────────

// TestLookup_ReportsAnUnreadableDocument is §12.1 for the external call the
// read path makes. It is NOT reported as "unbound": a store that cannot be
// read has not told you the variable is unbound, and answering "unbound"
// would send the user to bind something they had already bound.
func TestLookup_ReportsAnUnreadableDocument(t *testing.T) {
	docs := newFakeDocs()
	docs.readErr = errors.New("permission denied")
	s := NewStore(docs, newFakeSecrets())

	_, found, err := s.Lookup(key("/c", "e", "v"))
	if err == nil {
		t.Fatal("Lookup succeeded on an unreadable document")
	}
	if found {
		t.Error("Lookup reported a binding it could not read")
	}
}

// TestReadDocument_RefusesADocumentFromANewerBuild is the document
// protocol's job (ADR-0011 §6): a binding document a newer build wrote may
// name fields this one does not know, and guessing at it would spend the
// wrong secret.
func TestReadDocument_RefusesADocumentFromANewerBuild(t *testing.T) {
	docs := newFakeDocs()
	docs.mu.Lock()
	docs.docs[DocumentName] = []byte(`{"schemaVersion":99,"bindings":[]}`)
	docs.mu.Unlock()
	s := NewStore(docs, newFakeSecrets())

	if _, _, err := s.Lookup(key("/c", "e", "v")); !errors.Is(err, storage.ErrVersionTooNew) {
		t.Fatalf("Lookup returned %v, want storage.ErrVersionTooNew", err)
	}
}

// TestResolve_ReportsAVaultFailureRatherThanReportingUnbound: the same
// distinction one layer down. A sealed vault is not an unresolved variable.
func TestResolve_ReportsAVaultFailureRatherThanReportingUnbound(t *testing.T) {
	s, _, secrets := newTestStore(t)
	k := key("/c", "e", "v")
	if err := s.Bind(context.Background(), k, []byte("x")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	secrets.getErr = errors.New("vault is sealed")

	_, found, err := s.Resolve(context.Background(), k)
	if !errors.Is(err, secrets.getErr) {
		t.Fatalf("Resolve returned %v, want the vault's failure", err)
	}
	if found {
		t.Error("Resolve reported a value it could not read")
	}
}

// TestResolve_ABindingNamingAValueThatIsGoneIsUnresolved is the repairable
// half of §8.2: a binding whose value has vanished reports itself
// UNRESOLVED and blocks the send — §6.5's rule, not a new one — rather than
// resolving to nothing at all.
func TestResolve_ABindingNamingAValueThatIsGoneIsUnresolved(t *testing.T) {
	docs, secrets := newFakeDocs(), newFakeSecrets()
	docs.seed(t, record{Collection: "/c", Environment: "e", Variable: "v", SecretID: "fake:vanished"})
	s := NewStore(docs, secrets)

	_, found, err := s.Resolve(context.Background(), key("/c", "e", "v"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if found {
		t.Error("Resolve answered with a value the vault does not hold")
	}
}

// TestVariables_ResolvesOnlyItsOwnCollectionAndEnvironment is the lookup
// the send path composes with the environment's plain values. The
// identifier never leaves this package through it — the caller gets a
// variable NAME in and a value out, and has nothing to hand an id to.
func TestVariables_ResolvesOnlyItsOwnCollectionAndEnvironment(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.Bind(ctx, key("/c", "prod", "token"), []byte("P")); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := s.Bind(ctx, key("/c", "staging", "token"), []byte("S")); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	look := s.Variables(ctx, "/c", "prod")
	v, found, err := look("token")
	if err != nil || !found || v != "P" {
		t.Errorf("Variables(prod)(token) = %q, %v, %v; want P", v, found, err)
	}
	if _, found, err := look("nope"); err != nil || found {
		t.Errorf("an unbound variable reported found=%v err=%v; want a clean miss that blocks the send", found, err)
	}
}

// TestConcurrentBinds_DoNotLoseEachOther: two collections bound at once are
// two rows, not one. The document is read, edited and written under one
// lock; without it the second write would be built on a document read
// before the first landed.
func TestConcurrentBinds_DoNotLoseEachOther(t *testing.T) {
	s, _, _ := newTestStore(t)
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := key("/c", "e", string(rune('a'+i)))
			if err := s.Bind(context.Background(), k, []byte("x")); err != nil {
				t.Errorf("Bind %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if _, found, err := s.Lookup(key("/c", "e", string(rune('a'+i)))); err != nil || !found {
			t.Errorf("binding %d lost (found=%v, err=%v)", i, found, err)
		}
	}
}

// ─── observers, for the ordering tests ─────────────────────────────────────

type observingDocs struct {
	*fakeDocs
	onWrite func()
}

func (d *observingDocs) Write(name string, doc any) error {
	d.onWrite()
	return d.fakeDocs.Write(name, doc)
}

type observingSecrets struct {
	*fakeSecrets
	onCreate func()
	onDelete func()
}

func (s *observingSecrets) CreateNamed(ctx context.Context, v credential.Secret, meta vault.SecretMeta) (credential.SecretID, error) {
	if s.onCreate != nil {
		s.onCreate()
	}
	return s.fakeSecrets.CreateNamed(ctx, v, meta)
}

func (s *observingSecrets) Delete(ctx context.Context, id credential.SecretID) error {
	if s.onDelete != nil {
		s.onDelete()
	}
	return s.fakeSecrets.Delete(ctx, id)
}
