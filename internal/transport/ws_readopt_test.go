package transport

// The transport's half of taking a session back (nocx-k6p18.30).
//
// TWO PROPERTIES, and the second is the one a green suite would otherwise hide.
//
// The ring a re-adopted session gets starts at the offset THIS MACHINE'S
// RECORDING ENDS AT, not at zero. A helper-hosted session's stream offset is
// the host's, counted from the first byte the shell ever produced, and the
// recording is keyed by it; a ring restarting at zero would renumber the second
// half of one stream and the recording would splice two stretches that are not
// adjacent — the second coordinate system contracts/session.output.schema.json
// names as the defect it exists to avoid.
//
// And an attempt that FAILS leaves nothing behind. The interval, both ends
// named: from the moment ReadoptHostedSession returns nil until the session is
// closed, the transport holds a receiver for it; before the first and after the
// second it holds none. A receiver left behind by a failed attempt is a session
// a claim could resolve against with no session in the registry.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

// readoptChannel is the far end of a re-adopted session: it hands over
// whatever the test says the host is still producing and then parks, the way a
// live shell does.
type readoptChannel struct {
	mu    sync.Mutex
	out   []byte
	done  chan struct{}
	once  sync.Once
	drain chan struct{}
}

func newReadoptChannel(out []byte) *readoptChannel {
	return &readoptChannel{out: out, done: make(chan struct{}), drain: make(chan struct{})}
}

func (c *readoptChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	if len(c.out) > 0 {
		n := copy(p, c.out)
		c.out = c.out[n:]
		empty := len(c.out) == 0
		c.mu.Unlock()
		if empty {
			c.once.Do(func() { close(c.drain) })
		}
		return n, nil
	}
	c.mu.Unlock()
	c.once.Do(func() { close(c.drain) })
	<-c.done
	return 0, io.EOF
}

func (c *readoptChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *readoptChannel) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return nil
}

func (c *readoptChannel) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }

func (c *readoptChannel) Done() <-chan struct{} { return c.done }

// drained blocks until everything the host had for us has been handed over.
// A state change, never a duration: the reader itself closes the channel.
func (c *readoptChannel) drained(t *testing.T) {
	t.Helper()
	select {
	case <-c.drain:
	case <-time.After(10 * time.Second):
		t.Fatal("the re-adopted session's output never reached the ring")
	}
}

func readoptServer(t *testing.T, rec SessionOutputRecorder) (*WSServer, *session.Reg) {
	t.Helper()
	reg := session.New(log.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil))), nil)
	ws := NewWSServer(log.NewSlogAdapter(slog.New(slog.NewTextHandler(io.Discard, nil))), reg,
		WithSessionOutputRecorder(rec))
	return ws, reg
}

// THE PROPERTY: the recording continues. A previous coordinator recorded the
// first stretch of this session; the replacement records what comes next AT THE
// OFFSET IT COMES AT, so the two are one stream with one coordinate.
func TestAReadoptedSessionsRecordingContinuesWhereItStopped(t *testing.T) {
	const already = "what the coordinator that is gone recorded"
	rec := newFakeRecorder()
	// The previous incarnation's recording, as the store holds it.
	if _, err := rec.Append(context.Background(), content.SessionOutputAppend{
		SessionID: "sid-readopt", Offset: 0, Body: []byte(already),
	}); err != nil {
		t.Fatal(err)
	}
	ws, reg := readoptServer(t, rec)

	const next = "and what the replacement receives"
	ch := newReadoptChannel([]byte(next))
	var resumedAt uint64
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-readopt"),
		func(_ context.Context, from uint64) (HostedSessionOpen, error) {
			resumedAt = from
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-readopt"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{Session: sess}, nil
		})
	if err != nil {
		t.Fatalf("take the session back: %v", err)
	}

	if resumedAt != uint64(len(already)) {
		t.Fatalf("the attachment was told to resume at %d, want %d — the offset this machine's "+
			"recording ends at, which is what makes the host's stream and the recording one coordinate",
			resumedAt, len(already))
	}

	ch.drained(t)
	waitFor(t, "the replacement's bytes to reach the recording", func() bool {
		return string(rec.recorded()) == already+next
	})
	if got := string(rec.recorded()); got != already+next {
		t.Fatalf("recording = %q, want %q — the second stretch did not continue the first", got, already+next)
	}
	if len(rec.skipCalls()) != 0 {
		t.Fatalf("holes were recorded over bytes nothing lost: %+v. A ring that restarted at zero "+
			"would report everything already on disk as a gap", rec.skipCalls())
	}
	// THE OFFSET, not only the bytes. A store that appends whatever it is
	// handed produces the right SEQUENCE from a ring that restarted at zero and
	// a corrupt recording in the real one, where the offset is the coordinate a
	// client's acks and the recording's gaps are both measured against.
	offsets := rec.appendOffsets()
	if len(offsets) != 2 || offsets[1] != uint64(len(already)) {
		t.Fatalf("the runs were written at %v, want the second at %d — the replacement's bytes "+
			"belong where the previous coordinator's recording ended, not at the start of the stream",
			offsets, len(already))
	}
	_ = ch.Close()
}

// A session with no recording at all resumes at zero. The paired positive for
// the offset above: without it, "the recording continues" is satisfiable by a
// function that always answers with whatever the store happens to hold.
func TestASessionWithNoRecordingResumesAtTheStart(t *testing.T) {
	rec := newFakeRecorder()
	ws, reg := readoptServer(t, rec)

	ch := newReadoptChannel([]byte("first bytes this machine ever saw"))
	var resumedAt uint64 = 12345
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-nothing-recorded"),
		func(_ context.Context, from uint64) (HostedSessionOpen, error) {
			resumedAt = from
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-nothing-recorded"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{Session: sess}, nil
		})
	if err != nil {
		t.Fatalf("take the session back: %v", err)
	}
	if resumedAt != 0 {
		t.Fatalf("resumed at %d with nothing recorded, want 0", resumedAt)
	}
	_ = ch.Close()
}

// A failed attempt leaves NOTHING. The receiver is reserved before the helper
// is asked, so the only rollback there can be is removing it — and the test
// that proves it is one that asks for the receiver afterwards.
func TestAFailedReadoptLeavesNoReceiverBehind(t *testing.T) {
	rec := newFakeRecorder()
	ws, _ := readoptServer(t, rec)

	refusal := errors.New("attach to the session still running: connection refused")
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-failed"),
		func(context.Context, uint64) (HostedSessionOpen, error) {
			return HostedSessionOpen{}, refusal
		})
	if !errors.Is(err, refusal) {
		t.Fatalf("error = %v, want the helper's own refusal carried through unchanged", err)
	}
	if rx := ws.getRx(session.ID("sid-failed")); rx != nil {
		t.Fatal("a failed re-adoption left a receiver behind: a later claim could resolve against " +
			"a ring for a session the registry does not hold")
	}
}

// The helper answered about a different session than the binding named. The id
// is what the whole reconciliation ordering stands on, so a substitution is
// refused rather than adopted — and, like every other failure, leaves nothing.
func TestAReadoptAnsweringForAnotherSessionIsRefused(t *testing.T) {
	rec := newFakeRecorder()
	ws, reg := readoptServer(t, rec)

	ch := newReadoptChannel(nil)
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-asked-for"),
		func(context.Context, uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-answered-with"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{Session: sess}, nil
		})
	if err == nil {
		t.Fatal("a re-attachment that answered for a different session was accepted; that binds one " +
			"pane's ring to another pane's shell")
	}
	if rx := ws.getRx(session.ID("sid-asked-for")); rx != nil {
		t.Fatal("the refused re-adoption left a receiver behind")
	}
	_ = ch.Close()
}

// waitFor polls a predicate to a bound. It waits on a STATE CHANGE and never on
// a duration: the deadline is only there so a broken build fails with a
// sentence instead of hanging.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// WHAT THE HOST'S WINDOW DROPPED IS NAMED, in the recording, with the reason
// that says whose bound took it. `hostWindow` is not `cap` and not
// `unrecorded`: nothing here ever had those bytes and no knob on this machine
// would have kept them (nocx-k6p18.25). It is asserted on the re-adoption path
// specifically because that is where it is now REACHED — a coordinator taking a
// session back meets the hole at the attach, before its first byte.
func TestTheHostsLostWindowIsRecordedAsAHostWindowGap(t *testing.T) {
	rec := newFakeRecorder()
	if _, err := rec.Append(context.Background(), content.SessionOutputAppend{
		SessionID: "sid-hole", Offset: 0, Body: []byte("before nocx was replaced"),
	}); err != nil {
		t.Fatal(err)
	}
	ws, reg := readoptServer(t, rec)

	const after = "and what came back"
	ch := newReadoptChannel([]byte(after))
	var report func(uint64, string)
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-hole"),
		func(context.Context, uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-hole"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{
				Session: sess,
				// The attachment's own seam, installed exactly as the helper
				// client's is: a registration, because where a hole sits is
				// only known in stream order.
				ObserveOutputHoles: func(f func(uint64, string)) { report = f },
			}, nil
		})
	if err != nil {
		t.Fatalf("take the session back: %v", err)
	}
	if report == nil {
		t.Fatal("the re-adoption never installed the hole observer, so a lost window would be silent")
	}

	// The helper's word for the cause, carried across untranslated. What the
	// recording calls it is this side's vocabulary and the mapping is one
	// function, not two.
	report(4096, proto.GapReasonWindow)

	ch.drained(t)
	waitFor(t, "the hole to reach the recording", func() bool { return len(rec.skipCalls()) > 0 })
	skips := rec.skipCalls()
	if skips[0].reason != content.GapReasonHostWindow {
		t.Fatalf("the hole was recorded as %q, want %q — `cap` would send a person to a retention "+
			"knob that never held these bytes, and `unrecorded` would say nobody was listening when "+
			"a recorder was running the whole time", skips[0].reason, content.GapReasonHostWindow)
	}
	_ = ch.Close()
}

// THE THIRD PROPERTY (nocx-k6p18.31): a re-adopted session that says something
// about itself is HEARD. The re-attachment is the only thing that knows whether
// this pane's lifecycle channel came back, and its answer has to reach two
// places here — the integration axis the product renders, and the lane registry
// every published lifecycle fact is routed through. A lane registered and an
// axis left empty is a pane that produces blocks and cannot say it is
// integrated; an axis filled in and no lane is the reverse. Both, or the pane
// is back to the silence this bead was filed for.
func TestATakenBackSessionsIntegrationAndLaneAreBothRegistered(t *testing.T) {
	rec := newFakeRecorder()
	ws, reg := readoptServer(t, rec)
	ch := newReadoptChannel(nil)
	t.Cleanup(func() { _ = ch.Close() })

	started := false
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-axis"),
		func(_ context.Context, _ uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-axis"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{
				Session:           sess,
				LifecycleLane:     lifecycle.LaneID("lane-taken-back"),
				IntegrationShell:  "/bin/bash",
				IntegrationStatus: IntegrationStarting,
				StartLifecycle:    func() { started = true },
			}, nil
		})
	if err != nil {
		t.Fatalf("take the session back: %v", err)
	}
	if !started {
		t.Fatal("the lifecycle bridge was never started, so no frame the shell sends can arrive")
	}
	ws.lifecycleMu.Lock()
	sid, laneKnown := ws.lifecycleLanes[lifecycle.LaneID("lane-taken-back")]
	ws.lifecycleMu.Unlock()
	if !laneKnown || sid != session.ID("sid-axis") {
		t.Fatal("the lane was not registered; every lifecycle fact for this pane is dropped as unroutable")
	}
	ws.integrationMu.Lock()
	st, onAxis := ws.integrations[session.ID("sid-axis")]
	ws.integrationMu.Unlock()
	if !onAxis {
		t.Fatal("the session is not on the integration axis, so the product says nothing about it at all")
	}
	if st.status != IntegrationStarting || st.shell != "/bin/bash" {
		t.Fatalf("axis = %q/%q, want %q//bin/bash", st.status, st.shell, IntegrationStarting)
	}
}

// The paired negative, and it is not symmetry for its own sake: absence on the
// integration axis is how "conventional by design" is expressed, so a
// re-attachment with nothing to say must leave the session OFF it. Registering
// every re-adopted session would make the product nag about panes that never
// offered shell integration in the first place.
func TestATakenBackSessionWithNothingToSayStaysOffTheAxis(t *testing.T) {
	rec := newFakeRecorder()
	ws, reg := readoptServer(t, rec)
	ch := newReadoptChannel(nil)
	t.Cleanup(func() { _ = ch.Close() })

	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-quiet"),
		func(_ context.Context, _ uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-quiet"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{Session: sess}, nil
		})
	if err != nil {
		t.Fatalf("take the session back: %v", err)
	}
	ws.integrationMu.Lock()
	_, onAxis := ws.integrations[session.ID("sid-quiet")]
	ws.integrationMu.Unlock()
	if onAxis {
		t.Fatal("a session that asked for no integration was put on the axis; the product will nag about a pane that is correct")
	}
}

// A re-attachment that already took a lifecycle domain over, refused by the
// transport's own id guard: the domain must be disposed with the session. Left
// behind it would sit in the kernel holding a lane, with a carrier nothing
// reads and a pane that does not exist — and the next attempt on that session
// would meet its own lane already busy.
func TestARefusedReadoptDisposesTheLifecycleChannelItTookOver(t *testing.T) {
	rec := newFakeRecorder()
	ws, reg := readoptServer(t, rec)
	ch := newReadoptChannel(nil)
	t.Cleanup(func() { _ = ch.Close() })

	aborted := false
	err := ws.ReadoptHostedSession(context.Background(), session.ID("sid-wanted"),
		func(_ context.Context, _ uint64) (HostedSessionOpen, error) {
			sess, adoptErr := reg.Adopt(session.Config{Kind: session.KindRemote, Host: "h"},
				session.ID("sid-answered"), ch)
			if adoptErr != nil {
				return HostedSessionOpen{}, adoptErr
			}
			return HostedSessionOpen{
				Session:        sess,
				LifecycleLane:  lifecycle.LaneID("lane-orphan"),
				AbortLifecycle: func() { aborted = true },
			}, nil
		})
	if err == nil {
		t.Fatal("a re-attachment answering for another session was accepted")
	}
	if !aborted {
		t.Fatal("the lifecycle domain the refused re-attachment took over was left in the kernel")
	}
	ws.lifecycleMu.Lock()
	_, laneKnown := ws.lifecycleLanes[lifecycle.LaneID("lane-orphan")]
	ws.lifecycleMu.Unlock()
	if laneKnown {
		t.Fatal("a lane was registered for a session the transport refused")
	}
}
