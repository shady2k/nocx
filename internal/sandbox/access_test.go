package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeGrantStore struct {
	workspaceID string
	access      AccessClass
	path        string
	rev         int64
	err         error
}

func (s *fakeGrantStore) PromoteSandboxPath(workspaceID string, access AccessClass, path string) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.workspaceID, s.access, s.path = workspaceID, access, path
	s.rev++
	return s.rev, nil
}

func TestAccessInboxCoalescesAndResolvesAtomically(t *testing.T) {
	store := &fakeGrantStore{}
	inbox := NewAccessInbox(store)
	identity := SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 7}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "outside", "file.txt")
	at := time.Unix(100, 0).UTC()

	inbox.Record(AccessObservation{Identity: identity, Shell: "/bin/zsh", Executable: "/usr/bin/cat", Path: path, Access: AccessReadOnly, Operation: "openat", Source: AccessSourceLinuxSeccomp, At: at})
	inbox.Record(AccessObservation{Identity: identity, Shell: "/bin/zsh", Executable: "/usr/bin/cat", Path: path, Access: AccessReadOnly, Operation: "openat", Source: AccessSourceLinuxSeccomp, At: at.Add(time.Second)})

	page := inbox.List(AccessListOptions{Limit: 10})
	if len(page.Events) != 1 || page.Events[0].Count != 2 {
		t.Fatalf("events = %#v, want one coalesced event with count 2", page.Events)
	}
	if page.Events[0].State != AccessStatePending {
		t.Fatalf("state = %q, want pending", page.Events[0].State)
	}

	resolved, err := inbox.Resolve(AccessResolveRequest{EventID: page.Events[0].ID, Decision: AccessDecisionWorkspaceReadOnly})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.State != AccessStateGranted || store.access != AccessReadOnly || store.path != page.Events[0].Directory || resolved.ProfileRevision != 1 {
		t.Fatalf("resolved = %#v, store = %#v", resolved, store)
	}
	if _, err := inbox.Resolve(AccessResolveRequest{EventID: page.Events[0].ID, Decision: AccessDecisionDismiss}); !errors.Is(err, ErrAccessEventResolved) {
		t.Fatalf("second Resolve err = %v, want ErrAccessEventResolved", err)
	}
}

func TestAccessInboxBoundsAndSessionLifecycle(t *testing.T) {
	inbox := NewAccessInbox(nil)
	inbox.capacity = 2
	identity := SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 3}
	for i, path := range []string{"/tmp/a", "/tmp/b", "/tmp/c"} {
		inbox.Record(AccessObservation{Identity: identity, Path: path, Access: AccessReadWrite, Source: AccessSourceDarwinSeatbelt, At: time.Unix(int64(i+1), 0)})
	}
	page := inbox.List(AccessListOptions{Limit: 10})
	if len(page.Events) != 2 || page.Lost != 1 {
		t.Fatalf("page = %#v, want two events and one lost", page)
	}
	inbox.CloseSession(identity)
	page = inbox.List(AccessListOptions{Limit: 10})
	for _, event := range page.Events {
		if event.State == AccessStatePending {
			t.Fatalf("closed session event remains pending: %#v", event)
		}
	}
}

func TestAccessInboxRejectsInvalidObservationAndUnknownEvent(t *testing.T) {
	inbox := NewAccessInbox(nil)
	inbox.Record(AccessObservation{Identity: SessionIdentity{SessionID: "s", InstanceID: "i", Epoch: 1}, Path: "relative", Access: AccessReadOnly})
	if got := inbox.List(AccessListOptions{Limit: 10}); len(got.Events) != 0 {
		t.Fatalf("invalid observation stored: %#v", got.Events)
	}
	if _, err := inbox.Resolve(AccessResolveRequest{EventID: "missing", Decision: AccessDecisionDismiss}); !errors.Is(err, ErrAccessEventNotFound) {
		t.Fatalf("Resolve err = %v, want ErrAccessEventNotFound", err)
	}
}

func TestAccessSessionCommitsOnlyAfterReadinessAndRejectsLateDelivery(t *testing.T) {
	inbox := NewAccessInbox(nil)
	identity := SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 9}
	session := inbox.BeginSession(identity, "pane-1", "ws-1")
	observation := AccessObservation{
		Path: "/tmp/outside.txt", Access: AccessReadOnly, Source: AccessSourceLinuxSeccomp, At: time.Unix(1, 0),
	}
	session.Record(observation)
	if page := inbox.List(AccessListOptions{Limit: 10}); len(page.Events) != 0 {
		t.Fatalf("provisional event became visible: %#v", page.Events)
	}
	session.Activate()
	if page := inbox.List(AccessListOptions{Limit: 10}); len(page.Events) != 1 {
		t.Fatalf("activated events = %#v, want one", page.Events)
	}
	session.Close()
	session.Record(observation)
	page := inbox.List(AccessListOptions{Limit: 10})
	if page.Events[0].State != AccessStateExpired || page.Lost != 1 {
		t.Fatalf("late delivery state = %#v, lost = %d", page.Events[0], page.Lost)
	}
}

func TestAccessSessionCloseCannotOvertakeActivationDelivery(t *testing.T) {
	inbox := NewAccessInbox(nil)
	session := inbox.BeginSession(SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 10}, "pane-1", "ws-1")
	session.Record(AccessObservation{
		Path: "/tmp/outside.txt", Access: AccessReadOnly, Source: AccessSourceLinuxSeccomp, At: time.Unix(1, 0),
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	unsubscribe := inbox.Subscribe(func(uint64) {
		enteredOnce.Do(func() { close(entered) })
		<-release
	})
	defer unsubscribe()

	activated := make(chan struct{})
	go func() {
		session.Activate()
		close(activated)
	}()
	<-entered

	closed := make(chan struct{})
	go func() {
		session.Close()
		close(closed)
	}()
	select {
	case <-closed:
		close(release)
		<-activated
		t.Fatal("Close returned before provisional delivery completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(release)
	<-activated
	<-closed
	page := inbox.List(AccessListOptions{Limit: 10})
	if len(page.Events) != 1 || page.Events[0].State != AccessStateExpired {
		t.Fatalf("events after activation/close = %#v, want one expired event", page.Events)
	}
}

func TestAccessInboxRejectsChangedGrantDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	inbox := NewAccessInbox(&fakeGrantStore{})
	inbox.Record(AccessObservation{
		Identity: SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1},
		Path:     filepath.Join(alias, "file.txt"), Access: AccessReadOnly, Source: AccessSourceLinuxSeccomp,
	})
	event := inbox.List(AccessListOptions{Limit: 10}).Events[0]
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Resolve(AccessResolveRequest{EventID: event.ID, Decision: AccessDecisionWorkspaceReadOnly}); !errors.Is(err, ErrAccessGrantUnavailable) {
		t.Fatalf("Resolve err = %v, want ErrAccessGrantUnavailable", err)
	}
	updated := inbox.List(AccessListOptions{Limit: 10}).Events[0]
	if updated.CanGrant || updated.State != AccessStatePending {
		t.Fatalf("updated event = %#v, want pending non-grantable", updated)
	}
}

func TestAccessInboxListUsesEmptyArray(t *testing.T) {
	page := NewAccessInbox(nil).List(AccessListOptions{})
	if page.Events == nil {
		t.Fatal("empty access inbox returned nil events; JSON contract requires []")
	}
}

func TestAccessInboxDoesNotWidenToNearestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "nested", "target.txt")
	inbox := NewAccessInbox(nil)
	inbox.Record(AccessObservation{
		Identity: SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1},
		Path:     path, Access: AccessReadWrite, Source: AccessSourceLinuxSeccomp,
	})
	event := inbox.List(AccessListOptions{Limit: 10}).Events[0]
	if event.CanGrant || event.Directory != "" {
		t.Fatalf("event = %#v, nearest existing ancestor %q must not be offered", event, root)
	}
}

func TestAccessInboxNeverOffersFilesystemRootGrant(t *testing.T) {
	path := filepath.Join(string(os.PathSeparator), "nocx-does-not-exist", "target.txt")
	inbox := NewAccessInbox(nil)
	inbox.Record(AccessObservation{
		Identity: SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1},
		Path:     path, Access: AccessReadWrite, Source: AccessSourceLinuxSeccomp,
	})
	event := inbox.List(AccessListOptions{Limit: 10}).Events[0]
	if event.CanGrant || event.Directory != "" {
		t.Fatalf("event = %#v, filesystem root must never be promotable", event)
	}
}

func TestAccessInboxRejectsWritableProtectedSystemRoot(t *testing.T) {
	store := &fakeGrantStore{}
	inbox := NewAccessInbox(store)
	inbox.Record(AccessObservation{
		Identity: SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1},
		Path:     filepath.Join("/usr", "nocx-denied-target"), Access: AccessReadWrite, Source: AccessSourceLinuxSeccomp,
	})
	event := inbox.List(AccessListOptions{Limit: 10}).Events[0]
	if !event.CanGrant {
		t.Skip("/usr is not an existing directory on this platform")
	}
	if _, err := inbox.Resolve(AccessResolveRequest{EventID: event.ID, Decision: AccessDecisionWorkspaceReadWrite}); !errors.Is(err, ErrAccessGrantUnavailable) {
		t.Fatalf("Resolve err = %v, want ErrAccessGrantUnavailable", err)
	}
	if store.path != "" {
		t.Fatalf("protected path persisted: %q", store.path)
	}
}

// blockingGrantStore parks inside PromoteSandboxPath until released, so a test
// can hold the store write open and observe what else the inbox allows.
type blockingGrantStore struct {
	entered chan struct{}
	release chan struct{}
	rev     int64
}

func (s *blockingGrantStore) PromoteSandboxPath(string, AccessClass, string) (int64, error) {
	s.rev++
	close(s.entered)
	<-s.release
	return s.rev, nil
}

func TestAccessInboxResolveDoesNotBlockObservationWhileGrantPromotes(t *testing.T) {
	store := &blockingGrantStore{entered: make(chan struct{}), release: make(chan struct{})}
	inbox := NewAccessInbox(store)
	identity := SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dir", "file.txt")
	inbox.Record(AccessObservation{Identity: identity, Shell: "/bin/zsh", Executable: "/usr/bin/cat", Path: path, Access: AccessReadOnly, Operation: "openat", Source: AccessSourceLinuxSeccomp, At: time.Now().UTC()})
	event := inbox.List(AccessListOptions{Limit: 10}).Events[0]

	done := make(chan error, 1)
	go func() {
		_, err := inbox.Resolve(AccessResolveRequest{EventID: event.ID, Decision: AccessDecisionWorkspaceReadOnly})
		done <- err
	}()

	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("grant promotion never entered")
	}

	// While the grant promotion is parked, a read must not block on the inbox
	// mutex — the whole point of not holding it across the store write.
	listDone := make(chan struct{})
	go func() { _ = inbox.List(AccessListOptions{Limit: 10}); close(listDone) }()
	select {
	case <-listDone:
	case <-time.After(2 * time.Second):
		t.Fatal("List blocked on the inbox mutex held across grant promotion")
	}

	close(store.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve did not finish after release")
	}
}

func TestAccessInboxConcurrentResolvePromotesExactlyOnce(t *testing.T) {
	store := &blockingGrantStore{entered: make(chan struct{}), release: make(chan struct{})}
	inbox := NewAccessInbox(store)
	identity := SessionIdentity{SessionID: "session", InstanceID: "instance", Epoch: 1}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dir", "file.txt")
	inbox.Record(AccessObservation{Identity: identity, Shell: "/bin/zsh", Executable: "/usr/bin/cat", Path: path, Access: AccessReadOnly, Operation: "openat", Source: AccessSourceLinuxSeccomp, At: time.Now().UTC()})
	event := inbox.List(AccessListOptions{Limit: 10}).Events[0]

	firstDone := make(chan error, 1)
	go func() {
		_, err := inbox.Resolve(AccessResolveRequest{EventID: event.ID, Decision: AccessDecisionWorkspaceReadOnly})
		firstDone <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("grant promotion never entered")
	}

	// A second Resolve for the same event, while the first promotion is still
	// in flight, must fail closed rather than double-promote the directory.
	if _, err := inbox.Resolve(AccessResolveRequest{EventID: event.ID, Decision: AccessDecisionWorkspaceReadOnly}); !errors.Is(err, ErrAccessEventResolved) {
		t.Fatalf("concurrent Resolve err = %v, want ErrAccessEventResolved", err)
	}
	if store.rev != 1 {
		t.Fatalf("promotions = %d, want 1", store.rev)
	}

	close(store.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if store.rev != 1 {
		t.Fatalf("promotions = %d after completion, want 1", store.rev)
	}
}
