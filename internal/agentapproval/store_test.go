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

// testLocal is the domain of the machine the backend runs on — the domain every
// answer had before machines were part of the key, and still the one most
// answers are given in.
func testLocal() Domain { return LocalDomain() }

// testSSH is one ssh machine: host, account and the host key the connection was
// accepted under.
func testSSH(host, account, hostKey string) Domain {
	return Domain{Kind: DomainSSH, Host: host, Account: account, HostKey: hostKey}
}

// sshA and sshB are two DIFFERENT machines, and sshA2 is the second account on
// the first one. They differ in exactly one field each, which is what makes the
// keying tests say which field did the work.
func sshA() Domain  { return testSSH("build.example.com", "deploy", "SHA256:key-a") }
func sshA2() Domain { return testSSH("build.example.com", "root", "SHA256:key-a") }
func sshB() Domain  { return testSSH("other.example.com", "deploy", "SHA256:key-b") }

// sshAReKeyed is machine A after its host key changed: same host, same account,
// a different key — the machine ADR-0023 says is a different answer to give.
func sshAReKeyed() Domain { return testSSH("build.example.com", "deploy", "SHA256:key-c") }

func TestStoreRemembersGrantedAndDeniedAcrossReconstruction(t *testing.T) {
	doc := &memoryDocumentStore{}
	first := testStore(doc)
	if err := first.Record(testExecutable(), testLocal(), "session=s1;environment=local", Granted); err != nil {
		t.Fatalf("record granted: %v", err)
	}
	if err := first.Record(testExecutable(), testLocal(), "session=s2;environment=local", Denied); err != nil {
		t.Fatalf("record denied: %v", err)
	}

	second := testStore(doc)
	if got, ok := second.Lookup(testExecutable(), testLocal(), "session=s1;environment=local"); !ok || got != Granted {
		t.Fatalf("granted lookup = %q, %v", got, ok)
	}
	if got, ok := second.Lookup(testExecutable(), testLocal(), "session=s2;environment=local"); !ok || got != Denied {
		t.Fatalf("denied lookup = %q, %v", got, ok)
	}
	if _, ok := second.Lookup(testExecutable(), testLocal(), "session=s3;environment=local"); ok {
		t.Fatal("an unapproved scope was granted")
	}
}

func TestStoreFailClosedForCorruptDocument(t *testing.T) {
	doc := &memoryDocumentStore{data: map[string][]byte{"agent-approvals.json": []byte(`{"version":2,"approvals":[{"executable":{"path":"/x","sha256":"bad"},"domain":{"kind":"local"},"scope":"s","answer":"granted"}]}`)}}
	if _, ok := testStore(doc).Lookup(testExecutable(), testLocal(), "s"); ok {
		t.Fatal("corrupt approval document granted access")
	}
}

// A version 1 document is refused, and that is the greenfield choice stated in
// the store's own comment rather than a shim: its records name no machine, so
// importing one would key a person's yes to whichever machine asked next —
// exactly the defect the domain exists to end. Refusing costs one re-answer.
func TestStoreRefusesAVersionOneDocument(t *testing.T) {
	doc := &memoryDocumentStore{data: map[string][]byte{"agent-approvals.json": []byte(
		`{"version":1,"approvals":[{"executable":{"path":"/opt/agents/claude","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},"scope":"tool-endpoint:workspace:default","answer":"granted"}]}`)}}
	store := testStore(doc)
	if _, ok := store.Lookup(testExecutable(), testLocal(), "tool-endpoint:workspace:default"); ok {
		t.Fatal("a version 1 answer was read as though it named a machine")
	}
	if len(store.List()) != 0 {
		t.Fatalf("List showed %d answers from a version 1 document", len(store.List()))
	}
}

func TestStoreWriteFailureDoesNotLeaveGrantInMemory(t *testing.T) {
	doc := &memoryDocumentStore{err: os.ErrPermission}
	store := testStore(doc)
	if err := store.Record(testExecutable(), testLocal(), "scope", Granted); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("record error = %v, want permission error", err)
	}
	if _, ok := store.Lookup(testExecutable(), testLocal(), "scope"); ok {
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

// THE TRUST DOMAIN (nocx-50w7p.16). One test per way two machines can share a
// key, each paired with the matching machine still matching — an answer that
// matched nothing would pass every one of these tests and be useless.

func TestAnAnswerForOneMachineDoesNotStandForAnother(t *testing.T) {
	for _, tc := range []struct {
		name   string
		given  Domain
		asked  Domain
		shared string
	}{
		{"local does not stand for an ssh host", testLocal(), sshA(), "same executable, no machine in the key"},
		{"host A does not stand for host B", sshA(), sshB(), "same account, different host"},
		{"one account does not stand for another on the same host", sshA(), sshA2(), "same host, different account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := &memoryDocumentStore{}
			store := testStore(doc)
			if err := store.Record(testExecutable(), tc.given, "tool-endpoint:workspace:default", Granted); err != nil {
				t.Fatalf("record: %v", err)
			}
			// The pairing: the machine the person actually answered about
			// still matches. Without this line the test would pass on a store
			// that had simply lost the answer.
			if got, ok := store.Lookup(testExecutable(), tc.given, "tool-endpoint:workspace:default"); !ok || got != Granted {
				t.Fatalf("the answer given for %+v does not match itself (%v, %v)", tc.given, got, ok)
			}
			if _, ok := store.Lookup(testExecutable(), tc.asked, "tool-endpoint:workspace:default"); ok {
				t.Fatalf("an answer given for %+v matched %+v — %s", tc.given, tc.asked, tc.shared)
			}
			// And it survives a reload, so the separation is in the document
			// rather than in one process's map.
			fresh := testStore(doc)
			if _, ok := fresh.Lookup(testExecutable(), tc.asked, "tool-endpoint:workspace:default"); ok {
				t.Fatalf("after reload an answer for %+v matched %+v", tc.given, tc.asked)
			}
		})
	}
}

func TestAChangedHostKeyStopsMatchingAndAnUnchangedOneKeepsMatching(t *testing.T) {
	doc := &memoryDocumentStore{}
	store := testStore(doc)
	if err := store.Record(testExecutable(), sshA(), "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Paired success first: the key that has not moved still answers.
	if _, ok := store.Lookup(testExecutable(), sshA(), "tool-endpoint:workspace:default"); !ok {
		t.Fatal("an answer stopped matching its own machine while its host key was unchanged")
	}
	// A machine whose key changed is a different answer to give, so the stored
	// yes must stop matching and the question be asked again.
	if _, ok := store.Lookup(testExecutable(), sshAReKeyed(), "tool-endpoint:workspace:default"); ok {
		t.Fatal("a yes given under one host key matched the same host after its key changed")
	}
}

func TestAnIncompleteMachineKeysNothingAndCannotBeWritten(t *testing.T) {
	store := testStore(&memoryDocumentStore{})
	partial := []Domain{
		{Kind: DomainSSH, Account: "deploy", HostKey: "SHA256:key"},
		{Kind: DomainSSH, Host: "build.example.com", HostKey: "SHA256:key"},
		{Kind: DomainSSH, Host: "build.example.com", Account: "deploy"},
		{Kind: ""},
		{Kind: DomainLocal, Host: "build.example.com"},
	}
	for _, domain := range partial {
		if domain.Valid() {
			t.Fatalf("%+v was accepted as a trust domain", domain)
		}
		if _, ok := store.Lookup(testExecutable(), domain, "scope"); ok {
			t.Fatalf("%+v answered a lookup", domain)
		}
		if err := store.Record(testExecutable(), domain, "scope", Granted); err == nil {
			t.Fatalf("%+v was recorded", domain)
		}
	}
	// The pairing: the complete shapes are accepted, so the test above is
	// about completeness and not about the store refusing everything.
	if !testLocal().Valid() || !sshA().Valid() {
		t.Fatal("a complete local or ssh domain was rejected")
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
	if err := store.Record(second, testLocal(), "tool-endpoint:workspace:default", Denied); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(first, testLocal(), "tool-endpoint:workspace:default", Granted); err != nil {
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

// The read-back carries the machine, because two machines' answers for one
// executable are two rows and a surface cannot offer to unmake one it cannot
// tell apart.
func TestListTellsTwoMachinesAnswersApart(t *testing.T) {
	store := testStore(&memoryDocumentStore{})
	if err := store.Record(testExecutable(), testLocal(), "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(testExecutable(), sshA(), "tool-endpoint:workspace:default", Denied); err != nil {
		t.Fatal(err)
	}
	got := store.List()
	if len(got) != 2 {
		t.Fatalf("List returned %d answers, want 2 — the same executable on two machines", len(got))
	}
	// Same path, same digest, same scope: the domain is the only thing telling
	// these two rows apart.
	if got[0].Executable != got[1].Executable || got[0].Scope != got[1].Scope {
		t.Fatalf("the two rows differ by more than their machine: %+v / %+v", got[0], got[1])
	}
	if got[0].Domain == got[1].Domain {
		t.Fatal("two machines' answers carry the same domain, so a surface cannot tell them apart")
	}
	if got[0].Domain.Kind != DomainLocal || got[1].Domain != sshA() {
		t.Fatalf("domains = %+v / %+v, want local then sshA", got[0].Domain, got[1].Domain)
	}
}

func TestForgetUnmakesTheAnswerAndAsksAgain(t *testing.T) {
	doc := &memoryDocumentStore{}
	store := testStore(doc)
	agent := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	if err := store.Record(agent, testLocal(), "tool-endpoint:workspace:default", Denied); err != nil {
		t.Fatal(err)
	}

	forgotten, err := store.Forget(agent, testLocal(), "tool-endpoint:workspace:default")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if !forgotten {
		t.Fatal("Forget reported nothing to forget, but an answer was recorded")
	}
	if _, ok := store.Lookup(agent, testLocal(), "tool-endpoint:workspace:default"); ok {
		t.Fatal("the answer is still remembered after being forgotten")
	}
	if len(store.List()) != 0 {
		t.Fatalf("List still shows %d answers", len(store.List()))
	}
	// It left the document, not just the map: a forget that a restart undoes
	// is not a forget.
	fresh := testStore(doc)
	if _, ok := fresh.Lookup(agent, testLocal(), "tool-endpoint:workspace:default"); ok {
		t.Fatal("a store reloaded from the document still remembers the forgotten answer")
	}
}

// Forgetting is one machine's answer going. An answer for another machine is
// not the row this call names, and must survive it.
func TestForgetLeavesAnotherMachinesAnswerAlone(t *testing.T) {
	store := testStore(&memoryDocumentStore{})
	agent := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	if err := store.Record(agent, sshA(), "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(agent, sshB(), "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatal(err)
	}

	forgotten, err := store.Forget(agent, sshA(), "tool-endpoint:workspace:default")
	if err != nil || !forgotten {
		t.Fatalf("Forget(sshA) = %v, %v; want true, nil", forgotten, err)
	}
	if _, ok := store.Lookup(agent, sshA(), "tool-endpoint:workspace:default"); ok {
		t.Fatal("the named machine's answer survived its own revocation")
	}
	if _, ok := store.Lookup(agent, sshB(), "tool-endpoint:workspace:default"); !ok {
		t.Fatal("revoking one machine's answer revoked another machine's")
	}
}

// Asking to forget what is not there is what the caller wanted, so it is a
// success. Raising would turn a second click — or a page whose read predates
// somebody else's forget — into an error about a state the person asked for.
func TestForgettingWhatIsNotThereIsNotAnError(t *testing.T) {
	store := testStore(&memoryDocumentStore{})
	agent := Executable{Path: "/usr/bin/claude", SHA256: strings.Repeat("a", 64)}
	forgotten, err := store.Forget(agent, testLocal(), "tool-endpoint:workspace:default")
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
	if err := store.Record(agent, testLocal(), "tool-endpoint:workspace:default", Granted); err != nil {
		t.Fatal(err)
	}
	doc.err = errors.New("disk is gone")
	if _, err := store.Forget(agent, testLocal(), "tool-endpoint:workspace:default"); err == nil {
		t.Fatal("Forget reported success while its write failed")
	}
	doc.err = nil
	if answer, ok := store.Lookup(agent, testLocal(), "tool-endpoint:workspace:default"); !ok || answer != Granted {
		t.Fatalf("after a failed forget the answer is %q/%v, want granted/true", answer, ok)
	}
}
