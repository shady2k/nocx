package transport

// nocx-zg3k3.5.11 Round 4: the shell's exit may not overtake a replayed fact
// that preceded it (REVIEW-2's decision). A re-adopted session whose helper
// retained a lifecycle window replays that window through its lane; the exit
// carry (AdoptExitStatus → Done → monitorExit) can fire while the replay is
// still in flight, and the teardown used to unregister the lane before the
// window's start frame ingested — the start's attemptFact found no lane and
// dropped silently, no block ever opened, and the boundary settle sealed
// nothing (the 1/20 loaded shape, Round 2's probe chain).
//
// The hold is the transport's per-session answer: the readopt arms it with
// the attachment's lifecycle-drain signal, monitorExit waits it after Done,
// and the boundary consults settle only behind it. This test drives the REAL
// monitorExit (started by ReadoptHostedSession, as production starts it),
// ingests the window's frames through the publisher by hand (the bridge is
// not under test), and asserts the ORDER: EndSession does not run while the
// hold is open; the window's start opens its block through the still-living
// lane; the window's close seals it and settles the entry; only the release
// of the hold lets the exit through.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// boundedFalse observes, for a bounded window, that nothing has happened.
// The pass is the window's expiry and nothing else — the same judgement
// tryReadNotification makes for "no notification leaked", and the reason a
// plain negative has no waittest form: an absence has no event to wait on.
// The duration never decides a pass, and a window missed is caught again by
// the assertions behind it.
func boundedFalse(t *testing.T, what string, window time.Duration, happens func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if happens() {
			t.Fatalf("%s, within %s of watching for it not to", what, window)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheArmedEndHoldKeepsTheExitBehindTheReplayedWindow(t *testing.T) {
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)
	kernel := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(kernel)
	port := &lifecycleRecordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	const lane = lifecycle.LaneID("lane-end-hold-1")
	h, err := pub.RequestDomain(lane, nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}

	// The durable half: the store the block lands in and the ledger the
	// settle reads. One registry, owned by the server under test — the
	// teardown monitorExit runs is THIS server's, so the assertions read the
	// same registry the teardown empties.
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	sid := session.ID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	// THE HOLD IS ARMED BEFORE ANYTHING ADOPTS THE SESSION, exactly as the
	// readopt arms it inside its own re-attachment callback — before the
	// block rows bind, so every boundary consult finds it. drained stands in
	// for client.AttachedSession.LifecycleDrained(windowHead): the test
	// releases it when the window has been ingested, as the attachment's
	// ingest cursor would.
	drained := make(chan struct{})
	reg := newRegWithStub(logger)
	ws := NewWSServer(logger, reg, WithContentDB(db), WithLifecyclePublisher(pub))
	pub.SetEmitter(ws)
	// The pane the replayed start's entry is recorded against: the session's
	// own PaneID, which recordAttemptEntry resolves in the store.
	colour := "#000000"
	if _, wsErr := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-hold", Name: "hold", Colour: &colour, Position: 0},
		content.Tab{ID: "tab-hold", WorkspaceID: "ws-hold", Position: 0, Layout: "column"},
		content.Pane{ID: "pane-under-test", TabID: "tab-hold", Kind: "local", SizeShare: 1, Cwd: "/repo"}); wsErr != nil {
		t.Fatalf("CreateWorkspace: %v", wsErr)
	}
	if sessErr := led.CreateSession(ctx, content.Session{ID: string(sid), WorkspaceID: "ws-hold"}); sessErr != nil {
		t.Fatalf("CreateSession: %v", sessErr)
	}
	// The rows source is what makes attemptFact more than a no-op, and its
	// attach is the first boundary consult: behind the hold, its settle must
	// wait for the window, not run now.
	ws.HoldSessionEndFor(sid, drained)
	ws.AttachBlockRows(sid)

	// The re-adopt, over the same double the exit pipeline's own test uses:
	// the REAL monitorExit is started by ReadoptHostedSession, and the lane
	// registers inside it, before any frame of the window is ingested.
	ch := newExitEndableChannel()
	err = ws.ReadoptHostedSession(ctx, sid,
		func(ctx context.Context, from uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(ctx,
				session.Config{Kind: session.KindRemote, Host: "far-host", PaneID: "pane-under-test"},
				sid, ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{
				Session: sess, LifecycleLane: lane, StartLifecycle: func() {},
			}, nil
		})
	if err != nil {
		t.Fatalf("adopting the session: %v", err)
	}

	// THE SHELL'S EXIT CARRY ARRIVES WHILE THE WINDOW IS STILL UNREPLAYED —
	// the loaded shape: the pane's output drained to the exit frontier long
	// before the lifecycle window's frames did.
	ch.exitAt(&fakeHelperExitStatus{code: 0})

	// THE HOLD KEEPS THE EXIT BACK. monitorExit has woken on Done, but the
	// session must still be in the registry and the channel must not have
	// been told anything: the teardown waits for the window.
	boundedFalse(t, "the exit tore the session down while the end hold was open", 750*time.Millisecond,
		func() bool {
			_, ended := ch.state()
			if ended {
				return true
			}
			_, regErr := reg.Get(sid)
			return regErr != nil
		})

	// THE WINDOW REPLAYS, through the lane the hold is protecting. The
	// establishment, the prompt, the command's start — the start's fact is
	// what the Round-2 probe saw dropped with "no lane".
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 1, lifecycleHelloEvt()))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 2, lifecyclePromptEvt()))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleStartEvt(nil, "make")))
	// THE BLOCK THE REPLAYED START OPENS, while the lane still lives.
	var attempt, artifact string
	waittest.WaitForDetail(t, "the replayed start to open its block", func() string {
		open, openErr := led.OpenBlockRowsForSession(ctx, string(sid))
		return "open block: " + open.EntryID + "/" + open.ArtifactID + " err: " + fmtOpenErr(openErr)
	}, func() bool {
		open, openErr := led.OpenBlockRowsForSession(ctx, string(sid))
		if openErr != nil || open.EntryID == "" {
			return false
		}
		attempt, artifact = open.EntryID, open.ArtifactID
		return true
	})

	// THE WINDOW'S OWN CLOSE: the helper's recorded domain_closed. Through
	// the living lane it seals the block and settles the entry — the entry
	// settles from the helper's facts, not from the exit's teardown.
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 4, lifecycle.Event{
		Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{},
	}))
	waittest.WaitForDetail(t, "the replayed close to seal the block and settle the entry", func() string {
		art, artErr := led.Artifact(ctx, artifact)
		if artErr != nil || art == nil {
			return "artifact unreadable: " + fmtOpenErr(artErr)
		}
		return "artifact state " + string(art.State)
	}, func() bool {
		art, artErr := led.Artifact(ctx, artifact)
		if artErr != nil || art == nil || art.State != content.ArtifactSealed {
			return false
		}
		row, rowErr := led.Entry(ctx, attempt)
		return rowErr == nil && row != nil && row.Status == content.EntryUnknown
	})

	// AND NOW THE WINDOW IS DRAINED: the attachment's cursor has reached the
	// window's head. The hold lifts, and only NOW does the exit proceed.
	close(drained)
	waittest.WaitForDetail(t, "the exit to end the session once the hold lifted", func() string {
		_, ended := ch.state()
		return "ended=" + boolStr(ended)
	}, func() bool {
		if _, err := reg.Get(sid); err != nil {
			return true
		}
		_, ended := ch.state()
		return ended
	})
	if _, err := reg.Get(sid); err == nil {
		t.Fatal("the session is still in the registry after the hold lifted and the exit ran")
	}
}

func fmtOpenErr(err error) string {
	if err != nil {
		return err.Error()
	}
	return "<nil>"
}
