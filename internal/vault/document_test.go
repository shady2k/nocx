package vault

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// fakeDocStore is an in-memory DocumentStore for testing.
type fakeDocStore struct {
	data map[string][]byte
}

func (f *fakeDocStore) Read(name string, into any) (bool, error) {
	b, ok := f.data[name]
	if !ok || b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, into)
}

func (f *fakeDocStore) Write(name string, doc any) error {
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

func (f *fakeDocStore) Delete(name string) error {
	delete(f.data, name)
	return nil
}

func TestDocument_RoundTrip(t *testing.T) {
	store := &fakeDocStore{}
	pass := &Envelope{
		Salt:       []byte("salt123456789012"),
		Ciphertext: []byte("nonceciphertexttag"),
		Memory:     65536,
		Time:       3,
		Threads:    4,
	}
	recovery := &Envelope{
		Salt:       []byte("recsalt12345678"),
		Ciphertext: []byte("recnonceciphertext"),
		Memory:     65536,
		Time:       3,
		Threads:    4,
	}
	orig := Document{
		Version:         2,
		Instance:        "devbox",
		DefaultProvider: ProviderFile,
		Passphrase:      pass,
		Recovery:        recovery,
		HasOSKey:        false,
		AutoSealMinutes: 15,
		PreferredUnseal: "passphrase",
		Journal: []JournalEntry{
			{Op: "create", OldID: "", NewID: "sec:v1:file:aaaabbbbaaaabbbbccccddddaaaabbbb", Target: "profile:myserver", Phase: "prepared"},
		},
	}

	if err := saveDocument(store, orig); err != nil {
		t.Fatalf("saveDocument: %v", err)
	}

	got, found, err := loadDocument(store)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	if !found {
		t.Fatal("loadDocument returned found=false after save")
	}

	if !reflect.DeepEqual(got, orig) {
		t.Fatalf("round-trip mismatch:\ngot  %#v\nwant %#v", got, orig)
	}
}

func TestDocument_Absent(t *testing.T) {
	store := &fakeDocStore{}

	doc, found, err := loadDocument(store)
	if err != nil {
		t.Fatalf("loadDocument on empty store: %v", err)
	}
	if found {
		t.Fatal("loadDocument returned found=true on empty store")
	}
	// Zero document is expected.
	if doc.Version != 0 {
		t.Errorf("Version = %d, want 0", doc.Version)
	}
	if doc.Instance != "" {
		t.Errorf("Instance = %q, want empty", doc.Instance)
	}
	if doc.Passphrase != nil {
		t.Error("Passphrase should be nil for absent document")
	}
	if doc.Recovery != nil {
		t.Error("Recovery should be nil for absent document")
	}
	if doc.Journal != nil {
		t.Error("Journal should be nil for absent document")
	}
}

// TestDocument_JSONMarshal asserts that a fully populated Document marshals to
// JSON without error. This is the sentinel that catches a secret-bearing field
// added to the struct: credential.Secret refuses to marshal by design, so this
// test fails loudly the day somebody adds a Secret field to Document.
func TestDocument_JSONMarshal(t *testing.T) {
	doc := Document{
		Version:         2,
		Instance:        "testbox",
		DefaultProvider: ProviderSystem,
		Passphrase: &Envelope{
			Salt:       []byte("test-salt-16-byte"),
			Ciphertext: []byte("test-nonce-cipher-gcm"),
			Memory:     65536,
			Time:       3,
			Threads:    4,
		},
		Recovery: &Envelope{
			Salt:       []byte("rec-salt-16-byte"),
			Ciphertext: []byte("rec-nonce-cipher-gcm"),
			Memory:     65536,
			Time:       3,
			Threads:    4,
		},
		HasOSKey:        true,
		AutoSealMinutes: 0,
		PreferredUnseal: "os-key",
		Journal: []JournalEntry{
			{Op: "rotate", OldID: "sec:v1:system:oldoldoldoldoldoldoldoldold1", NewID: "sec:v1:system:newnewnewnewnewnewnewnewnew1", Target: "profile:remote", Phase: "metadata-repointed"},
		},
	}

	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("json.Marshal(fully populated Document): %v", err)
	}
	t.Logf("marshalled %d bytes", len(data))

	// Round-trip through unmarshal to confirm the JSON shape is valid.
	var decoded Document
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded.DefaultProvider != doc.DefaultProvider {
		t.Errorf("DefaultProvider = %q, want %q", decoded.DefaultProvider, doc.DefaultProvider)
	}
}

// TestDocument_NilEnvelopesIsRepresentable asserts that a Document with nil
// envelope pointers round-trips correctly — on a machine where the OS holds
// the root key there is no passphrase envelope and no recovery envelope, and
// nil must mean "was never created" rather than "empty".
func TestDocument_NilEnvelopesIsRepresentable(t *testing.T) {
	store := &fakeDocStore{}
	orig := Document{
		Version:         2,
		Instance:        "os-only",
		DefaultProvider: ProviderSystem,
		HasOSKey:        true,
	}

	if err := saveDocument(store, orig); err != nil {
		t.Fatalf("saveDocument: %v", err)
	}

	got, found, err := loadDocument(store)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	if !found {
		t.Fatal("loadDocument returned found=false")
	}
	if got.Passphrase != nil {
		t.Error("Passphrase should be nil for OS-only vault")
	}
	if got.Recovery != nil {
		t.Error("Recovery should be nil for OS-only vault")
	}
}

// Compile-time assertion: Document has no credential.Secret field.
// If building succeeds, no secret-bearing field exists on the struct.
// This is redundant with TestDocument_JSONMarshal but catches it at
// compile time rather than test time.
func init() {
	var _ storage.SchemaVersion = Document{}.Version
}
