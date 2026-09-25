package session

// The helper's row buffer is the person's setting (nocx-2v80t.3.36): the
// coordinator reads it and sends it at spawn, and the session keeps the bound
// it was given for its whole life, so a changed value applies to the next
// session and never to a running one — the seam here is the spawn.

import (
	"context"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func spawnWithBuffer(t *testing.T, svc *Service, bytes int64) *hostSession {
	t.Helper()
	res, err := svc.spawn(context.Background(), proto.SpawnParams{
		Cols: 80, Rows: 24, RowBufferBytes: bytes,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7,
			Capability: strings.Repeat("ab", 32),
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	hs, err := svc.find(res.Entry.Session)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	return hs
}

func TestTheRowBufferIsTheOneTheSpawnNamed(t *testing.T) {
	svc := New(Options{Generation: "gen-under-test", Spawner: &lcSpawner{}, Log: lcTestLog()})
	t.Cleanup(svc.Close)

	first := spawnWithBuffer(t, svc, 3<<20)
	second := spawnWithBuffer(t, svc, 5<<20)
	if first.rowBufferBytes != 3<<20 {
		t.Fatalf("the first session's buffer is %d bytes, want the 3 MB its spawn named", first.rowBufferBytes)
	}
	if second.rowBufferBytes != 5<<20 {
		t.Fatalf("a session spawned after the value changed has %d bytes, want the new 5 MB", second.rowBufferBytes)
	}
	if first.rowBufferBytes != 3<<20 {
		t.Fatal("a later spawn changed a running session's buffer")
	}
}

// Paired: a spawn that names no buffer gets the helper's default, and one
// naming more than the helper's ceiling is clamped to it — the memory is
// spent on this machine, whatever the coordinator asks for.
func TestARowBufferNamedByNobodyIsTheDefaultAndOneTooLargeIsClamped(t *testing.T) {
	svc := New(Options{Generation: "gen-under-test", Spawner: &lcSpawner{}, Log: lcTestLog()})
	t.Cleanup(svc.Close)

	if got := spawnWithBuffer(t, svc, 0).rowBufferBytes; got != DefaultRowBufferBytes {
		t.Fatalf("a spawn naming no buffer got %d bytes, want the default %d", got, DefaultRowBufferBytes)
	}
	if got := spawnWithBuffer(t, svc, 1<<40).rowBufferBytes; got != MaxRowBufferBytes {
		t.Fatalf("a spawn naming 1 TB got %d bytes, want the ceiling %d", got, MaxRowBufferBytes)
	}
}
