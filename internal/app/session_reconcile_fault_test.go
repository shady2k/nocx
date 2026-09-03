package app

// A reconciliation pass that fails PART WAY THROUGH (nocx-cujkz).
//
// session_reconcile_test.go asserts the rule one session at a time: no failure
// mode produces `absent`. This file asserts it as an INTERVAL over a pass of
// many sessions, which is the shape the rule is actually exposed to at a cold
// start — a coordinator comes up holding several carried-over sessions, asks
// about them one at a time, and the host it is asking goes away in the middle.
//
// The interval has two ends and both are asserted. It OPENS when the ask
// fails: from that moment every session the pass could not reach is still on
// disk, still recorded, still awaiting a verdict, and none of them has been
// swept on the strength of a failure — including the one a reachable host
// would have called absent, which is the case where the honest answer and the
// destructive one differ. It CLOSES when a later pass reaches the host again
// and judges exactly what the first one could not.
//
// The fault is placed on the RE-ADOPTION ask, and that is not a narrower test
// than it looks. `judgeFrom` is the one place in session_reconcile.go that
// turns an inventory's answer into a verdict, for both of its callers — an
// inventory the coordinator already held, and one a re-adoption brought into
// existence — because it is a single decision and two copies of it would be
// two decisions (AD-8). Re-adoption is also the only arrangement in which a
// pass can fail PART WAY THROUGH one inventory's id space: a coordinator-held
// inventory is asked once and its answer memoized for every session it owns,
// while re-adoption asks per session and deliberately does not reuse the
// answer, so an ask's ordinal is a position in the pass.
//
// Two disciplines this file owes, both from the house pattern
// (internal/shellintegration/publisher_fault_test.go and
// internal/content/reconcile_test.go's TestARefusedAbsentSweepLeavesNothingHalfJudged):
//
//   1. The expected outcome is DISCOVERED by running the pass with nothing
//      failing, not written down. A table that predicts each position's result
//      from its ordinal asserts the test author's model of the code.
//   2. Every assertion is read back through a store OPENED AFRESH OVER THE
//      SAME FILE. The carried-over set is recomputed at `Open` from the tables
//      (content/reconcile_sqlite.go: carryOver), so a fresh incarnation reports
//      what is on disk rather than what a double remembers — and `absent` is
//      the one verdict with a durable trace, because it is the one that
//      deletes.

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// The fixture: six sessions a previous incarnation left behind, all on one
// host and one helper generation. Six rather than two because the property is
// about a POSITION in the pass, and a pass of two has no middle.
var faultPassSessions = []string{
	"session-1", "session-2", "session-3", "session-4", "session-5", "session-6",
}

// heldByTheHost is what a reachable host would answer. Sessions 3 and 5 are
// the ones it no longer holds — the two the fault-free pass judges `absent`,
// and therefore the two that make the negative below bite: a failed ask must
// leave them exactly as it found them, even though the truthful answer for
// them is the destructive one.
func heldByTheHost() map[string]struct{} {
	return map[string]struct{}{
		"session-1": {}, "session-2": {}, "session-4": {}, "session-6": {},
	}
}

const (
	faultPassWorkspace = "workspace:default"
	faultPassPane      = "pane-default"
	faultPassHost      = "host.example"
	faultPassAccount   = "deploy"
	faultPassGen       = "generation-a"
)

func faultPassKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func openFaultPassStore(t *testing.T, path string) content.ContentDB {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path:   path,
		Key:    faultPassKey(),
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(discardLogger()),
	})
	if err != nil {
		t.Fatalf("content.Open(%s): %v", path, err)
	}
	return db
}

// aStoreThatCarriedSessionsOver writes the rows as a PREVIOUS incarnation,
// closes, and hands back a store opened over the same file. The close and
// reopen are not ceremony: the carried-over set is computed at `Open` and is
// exact only because nothing this incarnation wrote exists yet, so a store
// that wrote the rows itself carries nothing over and has nothing to reconcile.
func aStoreThatCarriedSessionsOver(t *testing.T, path string) content.ContentDB {
	t.Helper()
	ctx := context.Background()
	first := openFaultPassStore(t, path)
	if _, err := first.Layout().CreateWorkspace(ctx,
		content.Workspace{ID: faultPassWorkspace, Name: "default"},
		content.Tab{ID: "tab-default", WorkspaceID: faultPassWorkspace, Layout: content.LayoutRow},
		content.Pane{ID: faultPassPane, TabID: "tab-default", Cwd: "/", Kind: content.PaneSSH, SizeShare: 1},
	); err != nil {
		_ = first.Close()
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for _, id := range faultPassSessions {
		if err := first.Ledger().CreateSession(ctx, content.Session{
			ID: id, WorkspaceID: faultPassWorkspace,
			Host: faultPassHost, Account: faultPassAccount, Generation: faultPassGen,
			PaneID: faultPassPane, ProfileID: "profile-1",
			HelperCommand: "/home/deploy/.nocx/helper/" + faultPassGen + "/nocx-helper",
			Fingerprint:   "SHA256:host-example",
		}); err != nil {
			_ = first.Close()
			t.Fatalf("CreateSession(%s): %v", id, err)
		}
		// A recording apiece, because the recording is what the two verdicts
		// are asymmetric ABOUT: `absent` deletes it, `unknown` keeps it for a
		// week. A fixture with no bytes in it cannot tell the two apart.
		res, err := first.SessionOutput().Append(ctx, content.SessionOutputAppend{
			SessionID: id, Offset: 0, Body: []byte("what " + id + " printed"),
		})
		if err != nil || !res.Kept {
			_ = first.Close()
			t.Fatalf("record output for %s: %v (kept=%v)", id, err, res.Kept)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close the previous incarnation's store: %v", err)
	}
	return openFaultPassStore(t, path)
}

// stillOnDisk is THE reader: a brand-new incarnation over the same file,
// reporting every session that survived and how many bytes of its output are
// still kept. A session judged `absent` is absent from it because the verdict
// deleted the row and the recording; a session nobody could ask about is
// present with its bytes intact, which is the week of disk `unknown` costs.
func stillOnDisk(t *testing.T, path string) map[string]uint64 {
	t.Helper()
	fresh := openFaultPassStore(t, path)
	defer func() { _ = fresh.Close() }()
	pending, err := fresh.Reconcile().Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending on a fresh reader over %s: %v", path, err)
	}
	out := make(map[string]uint64, len(pending))
	for _, p := range pending {
		out[p.SessionID] = p.RecordedBytes
	}
	return out
}

func sortedIDs(m map[string]uint64) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// askedInventory is one session's inventory as a re-adoption produces it: it
// owns exactly the id it was made for, and it either answers or fails.
type askedInventory struct {
	id   string
	live map[string]struct{}
	err  error
}

func (i askedInventory) Owns(id string) bool { return id == i.id }

func (i askedInventory) LiveSessions(context.Context) (map[string]struct{}, error) {
	if i.err != nil {
		return nil, i.err
	}
	return i.live, nil
}

// faultingReadopter is the ask, one session at a time, with the fault placed
// by ORDINAL. reconcileSessions calls Readopt once per carried-over session
// and deliberately does not reuse the answer (session_reconcile.go says so in
// as many words), so an ask's ordinal IS its position in the pass — which is
// what an enumeration of "the failure arrives here" needs. An ordinal says
// WHEN; a path or an id would say WHERE, and this test is about when.
//
// The fault persists from failFrom onward rather than firing once, because
// that is the failure being modelled: the host went away in the middle of the
// pass, and it is still away for every session after it.
type faultingReadopter struct {
	held     map[string]struct{}
	failFrom int // 1-based ask ordinal from which the host stops answering; 0 is healthy
	asks     []string
}

func (r *faultingReadopter) Readopt(_ context.Context, p content.PendingSession) (sessionInventory, error) {
	r.asks = append(r.asks, p.SessionID)
	inv := askedInventory{id: p.SessionID, live: r.held}
	if r.failFrom > 0 && len(r.asks) >= r.failFrom {
		inv.err = fmt.Errorf("ask the helper on %s about %s: %w",
			p.Host, p.SessionID, syscall.ECONNREFUSED)
	}
	return inv, nil
}

// theFaultFreePass runs the whole pass with nothing failing and reports what
// it did: the order the sessions were asked in, and the state it converged to.
// Both are DISCOVERED here and used as the table's expectation, so no
// assertion below encodes a prediction about which session gets which verdict.
func theFaultFreePass(t *testing.T) (asks []string, converged map[string]uint64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.db")
	store := aStoreThatCarriedSessionsOver(t, path)
	readopt := &faultingReadopter{held: heldByTheHost()}
	reconcileSessions(context.Background(), store.Reconcile(), nil, readopt, time.Hour, quietLogger())
	if err := store.Close(); err != nil {
		t.Fatalf("close after the fault-free pass: %v", err)
	}
	return readopt.asks, stillOnDisk(t, path)
}

func TestAnAskThatFailsPartWayThroughLeavesTheRestUnjudgedAndTheNextPassCompletesThem(t *testing.T) {
	ctx := context.Background()
	askOrder, converged := theFaultFreePass(t)

	if len(askOrder) != len(faultPassSessions) {
		t.Fatalf("the fault-free pass asked about %v, want one ask per carried-over session %v",
			askOrder, faultPassSessions)
	}
	if len(converged) == len(faultPassSessions) {
		t.Fatal("the fault-free pass swept nothing, so a faulted pass that also sweeps nothing " +
			"would pass this test by doing nothing at all")
	}
	if len(converged) == 0 {
		t.Fatal("the fault-free pass swept every session, so nothing is left to prove survives a failure")
	}

	for n := 1; n <= len(askOrder); n++ {
		t.Run(fmt.Sprintf("the host stops answering at ask %d of %d", n, len(askOrder)), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "content.db")
			store := aStoreThatCarriedSessionsOver(t, path)
			readopt := &faultingReadopter{held: heldByTheHost(), failFrom: n}
			reconcileSessions(ctx, store.Reconcile(), nil, readopt, time.Hour, quietLogger())

			// Why each session is where it is, in THIS incarnation's words.
			// Read before the close because a cause describes the attempts of
			// the incarnation that made them and is deliberately not written
			// to the rows.
			pending, err := store.Reconcile().Pending(ctx)
			if err != nil {
				t.Fatalf("Pending after the faulted pass: %v", err)
			}
			causes := map[string]content.PendingSession{}
			for _, p := range pending {
				causes[p.SessionID] = p
			}
			if err = store.Close(); err != nil {
				t.Fatalf("close after the faulted pass: %v", err)
			}

			// ── The interval opens ────────────────────────────────────────
			// Everything from the failed ask onward is still there, with its
			// output still kept, and it is the SESSIONS THE HEALTHY PASS
			// DELETED that make this an assertion rather than a tautology.
			afterFault := stillOnDisk(t, path)
			for _, id := range askOrder[n-1:] {
				keptBytes, ok := afterFault[id]
				if !ok {
					t.Fatalf("%s is gone after an ask that FAILED at position %d — a failure is never a "+
						"verdict, and absent deletes the recording and closes the block", id, n)
				}
				if keptBytes == 0 {
					t.Fatalf("%s survived but its recording did not (%d bytes) — `unknown` costs a week "+
						"of disk precisely so the work is still there when the host comes back", id, keptBytes)
				}
				p, judged := causes[id]
				if !judged {
					t.Fatalf("%s left the pending set without being judged", id)
				}
				if p.SessionExists {
					t.Fatalf("%s reads as existing on the host, but nobody could be asked about it", id)
				}
				if p.Cause != content.CauseConnectionRefused {
					t.Fatalf("cause for %s = %q, want %q — the product says WHY nobody could be asked",
						id, p.Cause, content.CauseConnectionRefused)
				}
				if p.Detail == "" {
					t.Fatalf("%s carries no detail — a bug report needs the error's own words", id)
				}
			}

			// The asks BEFORE the failure kept the verdicts they reached, and
			// exactly those: what the fault-free pass deleted among them is
			// deleted, what it kept is kept.
			for _, id := range askOrder[:n-1] {
				_, wanted := converged[id]
				_, got := afterFault[id]
				if wanted != got {
					t.Fatalf("%s was judged at position %d, before the failure, and its outcome does not "+
						"match the pass that never failed (on disk=%v, want %v)", id, n, got, wanted)
				}
			}

			// And nothing anywhere was swept that the healthy pass did not
			// sweep. Stated over the whole set rather than over the tail
			// alone, because the failure this guards against is a verdict
			// escaping to a session the pass never even reached.
			for _, id := range faultPassSessions {
				if _, ok := afterFault[id]; ok {
					continue
				}
				if _, healthyKept := converged[id]; healthyKept {
					t.Fatalf("%s was deleted by a pass that failed at ask %d, and the pass that reached "+
						"the host kept it", id, n)
				}
			}

			// ── The interval closes ───────────────────────────────────────
			// A later start over the same file: everything still awaiting a
			// verdict is carried over again, and with the host answering the
			// pass judges exactly that remainder. A session already judged
			// absent is not asked a second time — it is not there to ask
			// about. A session judged `live` IS asked again, deliberately:
			// the verdict says the host session exists, not that the open
			// attempt has been recovered, so the marker survives it.
			remainder := sortedIDs(afterFault)
			next := openFaultPassStore(t, path)
			healed := &faultingReadopter{held: heldByTheHost()}
			reconcileSessions(ctx, next.Reconcile(), nil, healed, time.Hour, quietLogger())
			if err = next.Close(); err != nil {
				t.Fatalf("close after the completing pass: %v", err)
			}
			if !reflect.DeepEqual(healed.asks, remainder) {
				t.Fatalf("the completing pass asked about %v, want exactly the remainder %v",
					healed.asks, remainder)
			}
			if final := stillOnDisk(t, path); !reflect.DeepEqual(final, converged) {
				t.Fatalf("after the fault was healed the store is %v, want the state the pass that "+
					"never failed converged to, %v", final, converged)
			}
		})
	}
}
