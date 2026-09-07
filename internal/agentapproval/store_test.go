package agentapproval

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

type memoryDocumentStore struct {
	data map[string][]byte
	err  error
}

func (s *memoryDocumentStore) Read(name string, into any) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	data, ok := s.data[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(data, into)
}

func (s *memoryDocumentStore) Write(name string, doc any) error {
	if s.err != nil {
		return s.err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[name] = data
	return nil
}

func (s *memoryDocumentStore) Delete(string) error     { return nil }
func (s *memoryDocumentStore) List() ([]string, error) { return nil, nil }

var _ storage.DocumentStore = (*memoryDocumentStore)(nil)

func testExecutable() Executable {
	return Executable{Path: "/opt/agents/claude", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
}

func testStore(doc storage.DocumentStore) *Store {
	return NewStore(log.NewSlogAdapter(nil), doc, "agent-approvals.json")
}

func TestStoreRemembersGrantedAndDeniedAcrossReconstruction(t *testing.T) {
	doc := &memoryDocumentStore{}
	first := testStore(doc)
	if err := first.Record(testExecutable(), "session=s1;environment=local", Granted); err != nil {
		t.Fatalf("record granted: %v", err)
	}
	if err := first.Record(testExecutable(), "session=s2;environment=local", Denied); err != nil {
		t.Fatalf("record denied: %v", err)
	}

	second := testStore(doc)
	if got, ok := second.Lookup(testExecutable(), "session=s1;environment=local"); !ok || got != Granted {
		t.Fatalf("granted lookup = %q, %v", got, ok)
	}
	if got, ok := second.Lookup(testExecutable(), "session=s2;environment=local"); !ok || got != Denied {
		t.Fatalf("denied lookup = %q, %v", got, ok)
	}
	if _, ok := second.Lookup(testExecutable(), "session=s3;environment=local"); ok {
		t.Fatal("an unapproved scope was granted")
	}
}

func TestStoreFailClosedForCorruptDocument(t *testing.T) {
	doc := &memoryDocumentStore{data: map[string][]byte{"agent-approvals.json": []byte(`{"version":1,"approvals":[{"executable":{"path":"/x","sha256":"bad"},"scope":"s","answer":"granted"}]}`)}}
	if _, ok := testStore(doc).Lookup(testExecutable(), "s"); ok {
		t.Fatal("corrupt approval document granted access")
	}
}

func TestStoreWriteFailureDoesNotLeaveGrantInMemory(t *testing.T) {
	doc := &memoryDocumentStore{err: os.ErrPermission}
	store := testStore(doc)
	if err := store.Record(testExecutable(), "scope", Granted); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("record error = %v, want permission error", err)
	}
	if _, ok := store.Lookup(testExecutable(), "scope"); ok {
		t.Fatal("failed write left a grant in memory")
	}
}

func TestIdentityForPathChangesWhenBinaryChanges(t *testing.T) {
	path := t.TempDir() + "/agent"
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := IdentityForPath(path)
	if err != nil {
		t.Fatalf("first identity: %v", err)
	}
	if writeErr := os.WriteFile(path, []byte("second"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	second, err := IdentityForPath(path)
	if err != nil {
		t.Fatalf("second identity: %v", err)
	}
	if first.Path != second.Path {
		t.Fatalf("path changed: %q vs %q", first.Path, second.Path)
	}
	if first.SHA256 == second.SHA256 {
		t.Fatal("binary replacement kept the approval identity")
	}
}
