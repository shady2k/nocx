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

	first := spawnWithBuffer(t, svc, 5<<20)
	second := spawnWithBuffer(t, svc, 7<<20)
	if first.rowBufferBytes != 5<<20 {
		t.Fatalf("the first session's buffer is %d bytes, want the 5 MB its spawn named", first.rowBufferBytes)
	}
	if second.rowBufferBytes != 7<<20 {
		t.Fatalf("a session spawned after the value changed has %d bytes, want the new 7 MB", second.rowBufferBytes)
	}
	if first.rowBufferBytes != 5<<20 {
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

// The helper owns a floor (nocx-2v80t.3.38): a request below it — one byte —
// gets the floor, because a buffer that cannot hold one closing screen would
// end every block incomplete on its first end marker. Paired with a request
// above the floor, which is kept (TestTheRowBufferIsTheOneTheSpawnNamed).
func TestARowBufferBelowTheFloorIsRaisedToIt(t *testing.T) {
	svc := New(Options{Generation: "gen-under-test", Spawner: &lcSpawner{}, Log: lcTestLog()})
	t.Cleanup(svc.Close)

	hs := spawnWithBuffer(t, svc, 1)
	if hs.rowBufferBytes != MinRowBufferBytes {
		t.Fatalf("a 1-byte request got %d bytes, want the floor %d", hs.rowBufferBytes, MinRowBufferBytes)
	}
	if got := hs.launch.RowBufferBytes(); got != MinRowBufferBytes {
		t.Fatalf("the launch record reports %d bytes, want what the session got, %d", got, MinRowBufferBytes)
	}
}

// The row buffer is spent on the helper's machine, so it counts against the
// helper-wide aggregate the output window does (AD-10, nocx-2v80t.3.38): a
// session within the budget gets what it asked for; one asking for more than
// is left gets what is left, never below the floor; and each reports what it
// actually got.
func TestTheRowBufferCountsAgainstTheAggregateBudget(t *testing.T) {
	// Each spawn here names a lifecycle, so its window is reserved twice at
	// the spawn; the test spawner grants no lifecycle carrier, so the second
	// reservation is returned once the session exists.
	window := DefaultLimits().DefaultWindowBytes
	budget := (window + 6<<20) + (2*window + 5<<20)
	svc := New(Options{
		Generation: "gen-under-test", Spawner: &lcSpawner{}, Log: lcTestLog(),
		Limits: Limits{BudgetBytes: budget},
	})
	t.Cleanup(svc.Close)

	within := spawnWithBuffer(t, svc, 6<<20)
	if within.rowBufferBytes != 6<<20 || within.launch.RowBufferBytes() != 6<<20 {
		t.Fatalf("a session within the budget got %d bytes (record %d), want the 6 MB it asked for",
			within.rowBufferBytes, within.launch.RowBufferBytes())
	}
	clamped := spawnWithBuffer(t, svc, 10<<20)
	if clamped.rowBufferBytes != 5<<20 || clamped.launch.RowBufferBytes() != 5<<20 {
		t.Fatalf("a session past the budget got %d bytes (record %d), want the 5 MB that was left",
			clamped.rowBufferBytes, clamped.launch.RowBufferBytes())
	}
	if got, want := svc.WindowBytesInUse(), 2*window+6<<20+5<<20; got != want {
		t.Fatalf("the helper has %d bytes committed, want %d — both windows and both row buffers", got, want)
	}
}
