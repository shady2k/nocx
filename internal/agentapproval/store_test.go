package agentapproval

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
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

// Reading answers back and unmaking them (nocx-6jbad). A denial is never
// silently retried, which is right and is only defensible while a person has
// somewhere to reconsider it; before these two the store was write-only from
// the product's side and the only way back was editing JSON by hand.

func TestListReturnsEveryAnswerInAStableOrder(t *testing.T) {
	store := testStore(&memoryDocumentStore{})
	second := Executable{Path: "/usr/bin/codex", SHA256: strings.Repeat("b", 64)}
	first := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	if err := store.Record(second, "tool-endpoint:workspace:default", Denied); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(first, "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatal(err)
	}

	got := store.List()
	if len(got) != 2 {
		t.Fatalf("List returned %d answers, want 2", len(got))
	}
	// Ordered by path, not by the map's whim: a list that reshuffles under a
	// person choosing what to forget is a list they cannot use.
	if got[0].Executable.Path != "/usr/bin/claude" || got[1].Executable.Path != "/usr/bin/codex" {
		t.Fatalf("List order = %q, %q; want claude before codex",
			got[0].Executable.Path, got[1].Executable.Path)
	}
	if got[0].Answer != Granted || got[1].Answer != Denied {
		t.Fatalf("answers = %q, %q; want granted, denied", got[0].Answer, got[1].Answer)
	}
	if got[0].Scope != "tool-endpoint:workspace:default" {
		t.Fatalf("scope = %q, want the scope it was recorded under", got[0].Scope)
	}
}

func TestForgetUnmakesTheAnswerAndAsksAgain(t *testing.T) {
	doc := &memoryDocumentStore{}
	store := testStore(doc)
	agent := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	if err := store.Record(agent, "tool-endpoint:workspace:default", Denied); err != nil {
		t.Fatal(err)
	}

	forgotten, err := store.Forget(agent, "tool-endpoint:workspace:default")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if !forgotten {
		t.Fatal("Forget reported nothing to forget, but an answer was recorded")
	}
	if _, ok := store.Lookup(agent, "tool-endpoint:workspace:default"); ok {
		t.Fatal("the answer is still remembered after being forgotten")
	}
	if len(store.List()) != 0 {
		t.Fatalf("List still shows %d answers", len(store.List()))
	}
	// It left the document, not just the map: a forget that a restart undoes
	// is not a forget.
	fresh := testStore(doc)
	if _, ok := fresh.Lookup(agent, "tool-endpoint:workspace:default"); ok {
		t.Fatal("a store reloaded from the document still remembers the forgotten answer")
	}
}

// Asking to forget what is not there is what the caller wanted, so it is a
// success. Raising would turn a second click — or a page whose read predates
// somebody else's forget — into an error about a state the person asked for.
func TestForgettingWhatIsNotThereIsNotAnError(t *testing.T) {
	store := testStore(&memoryDocumentStore{})
	agent := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	forgotten, err := store.Forget(agent, "tool-endpoint:workspace:default")
	if err != nil {
		t.Fatalf("Forget on an empty store: %v", err)
	}
	if forgotten {
		t.Fatal("Forget claimed to remove an answer that was never recorded")
	}
}

// A failed write leaves the answer standing. The alternative is a store that
// reports a revocation it did not persist, which is the direction that costs
// somebody something: they believe the agent is no longer admitted.
func TestAFailedWriteKeepsTheAnswer(t *testing.T) {
	doc := &memoryDocumentStore{}
	store := testStore(doc)
	agent := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	if err := store.Record(agent, "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatal(err)
	}
	doc.err = errors.New("disk is gone")
	if _, err := store.Forget(agent, "tool-endpoint:workspace:default"); err == nil {
		t.Fatal("Forget reported success while its write failed")
	}
	doc.err = nil
	if answer, ok := store.Lookup(agent, "tool-endpoint:workspace:default"); !ok || answer != Granted {
		t.Fatalf("after a failed forget the answer is %q/%v, want granted/true", answer, ok)
	}
}
