package transport

// A REPEATED FRAME IS A NO-OP BY IDENTITY (ADR-0077, the owner's second
// requirement of 2026-09-30). The cursor keeps a coordinator from being
// offered a frame the one before it applied; this file asks what happens
// when one is offered anyway. Every write a lifecycle frame causes — the
// entry, its execution, the block's artifact, the block stream's open and
// seal — is keyed by a stable identity of the block (the attempt id the
// shell minted), so a frame delivered twice, to two coordinators over one
// store, leaves exactly what it left the first time: one entry, one
// execution, one artifact, the same status.

import (
	"context"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// repeatPane is the pane newLifecycleLedgerEnv's session stands in.
const repeatPane = "01930000-0000-7000-8000-0000000000a1"

// freshCoordinator is a second coordinator over the SAME store: a new
// kernel and publisher that adopt the first one's domain (the capability the
// shell still holds), and a new server that re-adopts the same session —
// what a restarted nocx-server is, minus the helper in between.
type freshCoordinator struct {
	ws  *WSServer
	pub *lifecyclepub.Publisher
}

func newFreshCoordinator(t *testing.T, db content.ContentDB, sid string, lane lifecycle.LaneID, h lifecycle.DomainHandle) *freshCoordinator {
	t.Helper()
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)
	pub := lifecyclepub.New(lifecycle.New(lifecycle.Options{}))
	reg := newRegWithStub(logger)
	ws := NewWSServer(logger, reg, WithContentDB(db), WithLifecyclePublisher(pub))
	pub.SetEmitter(ws)
	if err := pub.BindTransport("T2", noopPort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	if _, err := pub.AdoptDomain(lane, h.Domain, h.Epoch, h.Capability, h.Recovery, "T2"); err != nil {
		t.Fatalf("AdoptDomain: %v", err)
	}
	ch := newExitEndableChannel()
	t.Cleanup(func() { ch.exitAt(nil) })
	if err := ws.ReadoptHostedSession(ctx, session.ID(sid),
		func(ctx context.Context, _ uint64) (HostedSessionOpen, error) {
			sess, err := reg.Adopt(ctx, session.Config{Kind: session.KindLocal, PaneID: repeatPane, Cwd: "/repo"}, session.ID(sid), ch)
			if err != nil {
				return HostedSessionOpen{}, err
			}
			return HostedSessionOpen{Session: sess, LifecycleLane: lane, StartLifecycle: func() {}}, nil
		}); err != nil {
		t.Fatalf("re-adopting the session: %v", err)
	}
	ws.AttachBlockRows(session.ID(sid))
	return &freshCoordinator{ws: ws, pub: pub}
}

// ledgerShape is what a repeat may not change: every entry the store holds,
// with its status and how many executions and block artifacts it carries.
func ledgerShape(t *testing.T, db content.ContentDB) string {
	t.Helper()
	ctx := context.Background()
	entries, err := db.Ledger().ListEntries(ctx, 100)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	out := ""
	for _, e := range entries {
		row, err := db.Ledger().Entry(ctx, e.ID)
		if err != nil || row == nil {
			t.Fatalf("Entry(%s) = %v, %v", e.ID, row, err)
		}
		arts := 0
		for _, ex := range row.Executions {
			for _, a := range ex.Artifacts {
				if a.MediaType == content.MediaBlockRows {
					arts++
				}
			}
		}
		out += fmt.Sprintf("[%s %s/%s executions=%d artifacts=%d]", row.ID, row.Phase, row.Status, len(row.Executions), arts)
	}
	return out
}

// repeatCase is one frame kind offered again: what the first coordinator
// applied and stored, in order, and the frame the fresh one is offered.
type repeatCase struct {
	name    string
	applied func(r repeatFrames) []lifecycle.Envelope
	repeat  func(r repeatFrames) lifecycle.Envelope
}

// repeatFrames builds the shell's frames for one domain, as the shipped
// shells send them: every start and every complete names the attempt the
// shell minted at start (s-<domain>-<n>).
type repeatFrames struct {
	lane lifecycle.LaneID
	h    lifecycle.DomainHandle
}

func (r repeatFrames) prompt(seq uint64) lifecycle.Envelope {
	return lifecycleEnv(r.lane, r.h, seq, lifecyclePromptEvt())
}

func (r repeatFrames) start(seq uint64, n int, cmd string) lifecycle.Envelope {
	id := lifecycle.AttemptID(fmt.Sprintf("s-dom-repeat-%d", n))
	return lifecycleEnv(r.lane, r.h, seq, lifecycleStartEvt(&id, cmd))
}

func (r repeatFrames) complete(seq uint64, n int) lifecycle.Envelope {
	return lifecycleEnv(r.lane, r.h, seq, lifecycleCompleteEvt(lifecycle.AttemptID(fmt.Sprintf("s-dom-repeat-%d", n)), 0, lifecycleFence(byte(0x50+n))))
}

func (r repeatFrames) closed(seq uint64) lifecycle.Envelope {
	return lifecycleEnv(r.lane, r.h, seq, lifecycle.Event{Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{}})
}

func (r repeatFrames) snapshot(seq uint64, n int) lifecycle.Envelope {
	code := 0
	return lifecycleEnv(r.lane, r.h, seq, lifecycle.Event{Kind: lifecycle.KindSnapshot, Snapshot: &lifecycle.Snapshot{
		RequestID: "req-repeat", ShellState: lifecycle.ShellAtPrompt,
		LastCompleted: &lifecycle.CompletedRef{AttemptID: lifecycle.AttemptID(fmt.Sprintf("s-dom-repeat-%d", n)), ExitCode: &code},
		NextSequence:  seq + 1,
	}})
}

func last(frames []lifecycle.Envelope) lifecycle.Envelope { return frames[len(frames)-1] }

func TestARepeatedLifecycleFrameIsANoOpOnAFreshCoordinator(t *testing.T) {
	cases := []repeatCase{
		{"start", func(r repeatFrames) []lifecycle.Envelope {
			return []lifecycle.Envelope{r.prompt(2), r.start(3, 0, "make build")}
		}, nil},
		{"complete", func(r repeatFrames) []lifecycle.Envelope {
			return []lifecycle.Envelope{r.prompt(2), r.start(3, 0, "make build"), r.complete(4, 0)}
		}, nil},
		{"exit", func(r repeatFrames) []lifecycle.Envelope {
			return []lifecycle.Envelope{r.prompt(2), r.start(3, 0, "make build"), r.complete(4, 0), r.prompt(5), r.closed(6)}
		}, nil},
		// The frame most exposed to a guess: a completion offered again
		// while the NEXT command is already running. Resolved by the id it
		// names, it closes nothing; resolved as "the session's open block",
		// it would close the running command with the finished one's status.
		{"complete while the next command runs", func(r repeatFrames) []lifecycle.Envelope {
			return []lifecycle.Envelope{r.prompt(2), r.start(3, 0, "make build"), r.complete(4, 0), r.prompt(5), r.start(6, 1, "make test")}
		}, func(r repeatFrames) lifecycle.Envelope { return r.complete(4, 0) }},
		// A snapshot answers a refresh_request only (ADR-0024 decision 7):
		// a fresh coordinator has asked none, so a snapshot offered to it is
		// refused and writes nothing.
		{"snapshot", func(r repeatFrames) []lifecycle.Envelope {
			return []lifecycle.Envelope{r.prompt(2), r.start(3, 0, "make build"), r.complete(4, 0)}
		}, func(r repeatFrames) lifecycle.Envelope { return r.snapshot(5, 0) }},
	}
	for _, app := range []bool{false, true} {
		for _, tc := range cases {
			name := tc.name
			if app {
				name = "app " + name
			}
			t.Run(name, func(t *testing.T) {
				e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
				// The binding, born with its cursor as production's is: the
				// repeat below is applied inside a frame, whose cursor must
				// land on it (ADR-0077 decision 9).
				zero := uint64(0)
				if err := db.Ledger().CreateSession(context.Background(), content.Session{
					ID: sid, WorkspaceID: "ws-lifecycle", LifecycleApplied: &zero,
				}); err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				e.ws.AttachBlockRows(session.ID(sid))
				r := repeatFrames{lane: lane, h: h}
				frames := tc.applied(r)
				for _, env := range frames {
					if app && env.Event.Kind == lifecycle.KindStart {
						// The command was submitted from nocx's own editor:
						// the app minted the attempt and keyed the entry by
						// it, and the shell's start attaches under the
						// shell's own id.
						got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
							lifecycleSubmitParams(string(h.Domain), env.Event.Start.Command), 41))
						waittest.WaitFor(t, "the submitted entry to be stored", func() bool {
							row, err := db.Ledger().Entry(context.Background(), got.ID)
							return err == nil && row != nil
						})
					}
					mustLifecycleIngest(t, pub, "T", env)
				}
				before := ledgerShape(t, db)
				assertOneOfEach(t, db)

				repeat := last(frames)
				if tc.repeat != nil {
					repeat = tc.repeat(r)
				}
				fresh := newFreshCoordinator(t, db, sid, lane, h)
				// Offered the way the coordinator applies every frame: inside
				// the store's own frame, so a repeat whose writes failed —
				// an entry the store refused as a conflicting replay, say —
				// fails the frame here rather than passing unseen.
				var ingestErr error
				if err := db.Ledger().ApplyLifecycleFrame(context.Background(), sid, 1<<20, func(ctx context.Context) error {
					ingestErr = fresh.pub.Ingest(ctx, "T2", repeat)
					return nil
				}); err != nil {
					t.Fatalf("the repeated %s failed its frame: %v", repeat.Event.Kind, err)
				}
				if ingestErr != nil {
					t.Logf("the fresh kernel refused the repeated %s: %v", repeat.Event.Kind, ingestErr)
				}
				if after := ledgerShape(t, db); after != before {
					t.Fatalf("the repeated %s changed the store:\n before %s\n after  %s", repeat.Event.Kind, before, after)
				}
				assertOneOfEach(t, db)
			})
		}
	}
}

// assertOneOfEach is the repeat's invariant stated per block: every entry
// the store holds carries exactly one execution and one block artifact.
func assertOneOfEach(t *testing.T, db content.ContentDB) {
	t.Helper()
	ctx := context.Background()
	entries, err := db.Ledger().ListEntries(ctx, 100)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	for _, e := range entries {
		row, err := db.Ledger().Entry(ctx, e.ID)
		if err != nil || row == nil {
			t.Fatalf("Entry(%s) = %v, %v", e.ID, row, err)
		}
		arts := 0
		for _, ex := range row.Executions {
			for _, a := range ex.Artifacts {
				if a.MediaType == content.MediaBlockRows {
					arts++
				}
			}
		}
		if len(row.Executions) != 1 || arts != 1 {
			t.Fatalf("entry %s holds %d executions and %d block artifacts, want one of each: %s",
				row.ID, len(row.Executions), arts, ledgerShape(t, db))
		}
	}
}
