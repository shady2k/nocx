package profile

import (
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage"
)

// barrierDocStore wraps a DocumentStore and ensures up to N concurrent Read
// calls block until all N have arrived, or a timeout releases the barrier.
// This allows deterministic reproduction of lost-update races (when Read is
// called without a caller-level lock) without deadlocking when the caller
// already serializes access.
type barrierDocStore struct {
	storage.DocumentStore

	mu      sync.Mutex
	waiters int
	release chan struct{}
	armed   bool
	done    bool
}

// arm activates the barrier for the next N concurrent Read calls.
func (b *barrierDocStore) arm(n int) {
	b.mu.Lock()
	b.release = make(chan struct{})
	b.waiters = 0
	b.armed = true
	b.done = false
	b.mu.Unlock()
}

// releaseBarrier closes the release channel exactly once. Must be called
// while NOT holding b.mu (the close synchronizes with readers blocked on
// the channel). Callers must set b.done = true under b.mu first.
func (b *barrierDocStore) releaseBarrier() {
	b.mu.Lock()
	if b.done {
		// Already released — the channel is either already closed or
		// about to be closed by the goroutine that set done.
		b.mu.Unlock()
		return
	}
	b.done = true
	// Capture the channel under the lock so we don't race with arm().
	ch := b.release
	b.mu.Unlock()
	close(ch)
}

func (b *barrierDocStore) Read(name string, into any) (bool, error) {
	b.mu.Lock()
	if !b.armed || b.done {
		b.mu.Unlock()
		return b.DocumentStore.Read(name, into)
	}
	b.waiters++
	trigger := b.waiters >= 2
	b.mu.Unlock()

	if trigger {
		b.releaseBarrier()
	} else {
		select {
		case <-b.release:
		case <-time.After(200 * time.Millisecond):
			b.releaseBarrier()
		}
	}
	return b.DocumentStore.Read(name, into)
}

// TestJSONStore_NoLostUpdateOnConcurrentSave proves that two concurrent
// SaveProfile calls for different profile IDs do not lose each other's
// mutations. The barrierDocStore blocks concurrent reads to ensure both
// goroutines load the same snapshot when the caller does NOT hold a lock
// (the bug). When the lock is held around load-modify-write (the fix),
// access is naturally serialized; the barrier times out harmlessly.
func TestJSONStore_NoLostUpdateOnConcurrentSave(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race-sensitive test in short mode")
	}

	bds := &barrierDocStore{
		DocumentStore: storage.NewDocumentStore(t.TempDir()),
	}
	store := NewJSONStoreWithDocStore(bds, "profiles.json")

	// Pre-populate with two profiles (barrier not yet armed, passes through).
	p1 := SSHProfile{
		Base:    Base{ID: "id1", Name: "profile-1"},
		Options: StoredSSHProfileOptions{Host: "host1.example.com", Port: Ptr(22)},
	}
	p2 := SSHProfile{
		Base:    Base{ID: "id2", Name: "profile-2"},
		Options: StoredSSHProfileOptions{Host: "host2.example.com", Port: Ptr(22)},
	}
	if err := store.CreateProfile(p1); err != nil {
		t.Fatalf("pre-pop p1: %v", err)
	}
	if err := store.CreateProfile(p2); err != nil {
		t.Fatalf("pre-pop p2: %v", err)
	}

	// Arm the barrier for 2 concurrent reads.
	bds.arm(2)

	// Synchronize goroutine start so they race.
	var start sync.WaitGroup
	start.Add(2)

	p1mod := p1
	p1mod.Options.Host = "host1-modified.example.com"
	p2mod := p2
	p2mod.Options.Host = "host2-modified.example.com"

	var wg sync.WaitGroup
	wg.Add(2)

	var err1, err2 error
	go func() {
		start.Done()
		start.Wait()
		defer wg.Done()
		err1 = store.UpdateProfile(p1mod)
	}()
	go func() {
		start.Done()
		start.Wait()
		defer wg.Done()
		err2 = store.UpdateProfile(p2mod)
	}()

	wg.Wait()

	if err1 != nil {
		t.Errorf("goroutine 1 SaveProfile: %v", err1)
	}
	if err2 != nil {
		t.Errorf("goroutine 2 SaveProfile: %v", err2)
	}

	// Both modifications must survive.
	profs, err := store.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}

	found := map[string]string{}
	for _, p := range profs {
		found[p.ID] = p.Options.Host
	}

	if found["id1"] != "host1-modified.example.com" {
		t.Errorf("profile id1 host = %q, want host1-modified.example.com (lost update!)", found["id1"])
	}
	if found["id2"] != "host2-modified.example.com" {
		t.Errorf("profile id2 host = %q, want host2-modified.example.com (lost update!)", found["id2"])
	}
}
