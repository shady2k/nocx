package client_test

// The watch's half of contracts/README.md's third check: fs.watch and
// fs.unwatch driven through the REAL host and the REAL client, with the params
// this test sends and the result the socket returns validated against the
// schemas that freeze them — and the `changed` notification read off the same
// socket and validated against its own.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/waittest"
)

// contractClock is the host's timer under this test's control: a change is
// announced on the rung the test advances to, never on a wall-clock guess.
type contractClock struct {
	mu      sync.Mutex
	waiters []chan time.Time
}

func (c *contractClock) After(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.waiters = append(c.waiters, ch)
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.waiters = slices.DeleteFunc(c.waiters, func(waiter chan time.Time) bool { return waiter == ch })
	}
}

func (c *contractClock) waitForPollTimer(t *testing.T) {
	t.Helper()
	waittest.WaitFor(t, "the host to arm a poll", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.waiters) > 0
	})
}

// rung fires one poll and waits for it to finish.
func (c *contractClock) rung(t *testing.T) {
	t.Helper()
	c.waitForPollTimer(t)
	c.mu.Lock()
	waiters := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	for _, ch := range waiters {
		ch <- time.Now()
	}
	c.waitForPollTimer(t)
}

// TestFSWatchAndUnwatchOverTheWireConformToTheirContracts installs a watch,
// checks the digest it answers is the directory's own, then releases it —
// contract-validating every frame.
func TestFSWatchAndUnwatchOverTheWireConformToTheirContracts(t *testing.T) {
	dir := fsTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := hostedFS(t, dir)

	raw := callContract(t, c, "watch", proto.OpWatch, proto.FSWatchParams{ID: "contract-watch", Path: dir})
	var installed proto.FSWatchResult
	if err := decodeJSON(t, raw, &installed); err != nil {
		t.Fatalf("decode watch result: %v", err)
	}
	var listed proto.FSListResult
	listRaw := callContract(t, c, "list", proto.OpList, proto.FSListParams{Path: dir, Offset: 0, Limit: 1})
	if err := decodeJSON(t, listRaw, &listed); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	if installed.Rev != listed.Rev {
		t.Fatalf("watch answered rev %q, want the digest fs.list answers %q", installed.Rev, listed.Rev)
	}

	callContract(t, c, "unwatch", proto.OpUnwatch, proto.FSUnwatchParams{ID: "contract-watch"})
	// A second release of the same id is the state the caller asked for, so it
	// is answered as success rather than as an error about the host.
	callContract(t, c, "unwatch", proto.OpUnwatch, proto.FSUnwatchParams{ID: "contract-watch"})
}

// TestFSChangedNotificationOverTheWireConformsToItsContract reads the
// notification the real service writes: one change on the watched directory is
// one `changed` frame on the same socket, and its params satisfy the schema
// that freezes them.
func TestFSChangedNotificationOverTheWireConformsToItsContract(t *testing.T) {
	dir := fsTempDir(t)
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := &contractClock{}
	c := hostedFS(t, dir, hostsvc.WithWatchClock(clock))

	changes := make(chan proto.FSChanged, 4)
	var installed proto.FSWatchResult
	if err := c.Call(context.Background(), proto.ServiceFS, proto.OpWatch,
		proto.FSWatchParams{ID: "wire-watch", Path: dir}, &installed); err != nil {
		t.Fatalf("watch: %v", err)
	}
	stop := c.OnFSChanged("wire-watch", func(changed proto.FSChanged) {
		select {
		case changes <- changed:
		default:
		}
	})
	t.Cleanup(stop)

	// A quiet directory: one rung, no change.
	clock.rung(t)
	select {
	case changed := <-changes:
		t.Fatalf("a quiet directory announced %+v", changed)
	default:
	}

	if err := os.WriteFile(file, []byte("two, longer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var announced proto.FSChanged
	clock.rung(t)
	select {
	case announced = <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("the host announced no change")
	}
	// What the socket CARRIED, not what this test built: the id the caller
	// minted, and the digest of the directory as a list answers it now.
	if announced.ID != "wire-watch" {
		t.Fatalf("the change arrived for id %q, want the caller's own", announced.ID)
	}
	var relisted proto.FSListResult
	if err := c.Call(context.Background(), proto.ServiceFS, proto.OpList,
		proto.FSListParams{Path: dir, Offset: 0, Limit: 1}, &relisted); err != nil {
		t.Fatalf("list after the change: %v", err)
	}
	if announced.Rev != relisted.Rev {
		t.Fatalf("announced rev %q, want the digest of the changed listing %q", announced.Rev, relisted.Rev)
	}
	if err := validateHelperJSON(loadHelperSchema(t, "fs.changed.schema.json"), mustMarshal(t, announced)); err != nil {
		t.Fatalf("the changed notification does not satisfy its contract: %v", err)
	}
}

func decodeJSON(t *testing.T, raw []byte, out any) error {
	t.Helper()
	return json.Unmarshal(raw, out)
}
