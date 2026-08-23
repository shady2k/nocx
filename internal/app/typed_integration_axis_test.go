package app

// The typed path's terminal outcome, asserted where the product reads it: the
// `session.integrationChanged` notification, off the real socket.
//
// The saved path has had this route since P5 — the launcher adapter reports
// the bootstrap's outcome into the session integration axis, which is how its
// `publish-unavailable` becomes something a renderer can say. The typed path
// reached the same terminal outcome and wrote it to a log. The refusal
// vocabulary was opened from seven members to thirty-one precisely so a user
// could be told WHICH refusal happened, so one path naming it and the other
// not is a soft degrade the UI contradicts (AGENTS.md).
//
// Asserted off the wire and never off the Go enum: a test that reads the
// reason back out of the value it passed in proves the value round-trips, not
// that the product ever hears it. Everything between the delivery and the
// socket — the lane lookup, the axis's own admission rule, the contract's
// closed enum — is exactly what the log-only version had no need of and what
// a reason has to survive.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

const typedAxisLane = lifecycle.LaneID("lane-typed-axis")

// typedAxisStack is the real transport with a stub pty behind it: the only
// thing faked is the shell, because what is under test is which frame reaches
// the socket.
type typedAxisStack struct {
	ws   *transport.WSServer
	conn *websocket.Conn
	sid  string
	// frames carries every session.integrationChanged this session's
	// subscriber received, drained by one goroutine.
	//
	// One reader, and never a read deadline: a gorilla websocket that has
	// failed a read stays failed, and a second read on it PANICS. A helper
	// that polled the socket with a timeout therefore turned "the frame has
	// not arrived yet" into a crash — which is also why nothing here waits
	// on a duration: the channel is the event.
	frames chan integrationFrame
}

type typedAxisPTYFactory struct{ stub *pty.Stub }

func (f *typedAxisPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	return f.stub, nil
}

func newTypedAxisStack(t *testing.T) *typedAxisStack {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	reg := session.New(logger, &typedAxisPTYFactory{stub: pty.NewStub(logger)})
	ws := transport.NewWSServer(logger, reg)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("ws.Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := reachConnectWS(t, ws)

	resp := reachJSONRPCCall(t, conn, "open", map[string]any{"cols": 80, "rows": 24})
	var envelope struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("open: %v\nraw: %s", err, resp)
	}
	if envelope.Result.SessionID == "" {
		t.Fatalf("open returned no session id: %s", resp)
	}
	s := &typedAxisStack{
		ws: ws, conn: conn, sid: envelope.Result.SessionID,
		frames: make(chan integrationFrame, 64),
	}
	s.ws.RegisterLifecycleLane(typedAxisLane, session.ID(s.sid))
	go s.drain()
	return s
}

// drain is the single reader on the socket. It ends when the connection does,
// which the test's cleanup causes.
func (s *typedAxisStack) drain() {
	for {
		typ, msg, err := s.conn.ReadMessage()
		if err != nil {
			close(s.frames)
			return
		}
		if typ != websocket.TextMessage {
			continue
		}
		var frame struct {
			Method string           `json:"method"`
			Params integrationFrame `json:"params"`
		}
		if json.Unmarshal(msg, &frame) != nil {
			continue
		}
		if frame.Method != "session.integrationChanged" || frame.Params.SessionID != s.sid {
			continue
		}
		s.frames <- frame.Params
	}
}

// integratedParent puts the session where a typed `ssh` actually starts from:
// the user's own local shell has integrated, so the axis has ALREADY answered
// for this session and recorded that a domain was live on it.
//
// That state is the whole reason the route needed more than one line. The axis
// answers once per registration — "a session already answered keeps its first
// answer" — so an outcome reported against it is dropped, and a report that
// reaches a drop is worse than no report at all, because it looks routed. What
// makes the outcome land is arm's own registration of the NEW attempt, and
// this helper is what makes that assertion non-vacuous.
func (s *typedAxisStack) integratedParent(t *testing.T) {
	t.Helper()
	// The subscriber is installed by the open handler's tail, which outlives
	// the response it answered, so the first emit can be dropped for having
	// nobody to reach. Re-registering resets the axis to `starting`, which
	// makes the next published fact a CHANGE again — so this retries the
	// state transition and waits on the frame, never on a duration.
	deadline := time.Now().Add(wantWithinTypedAxis)
	for time.Now().Before(deadline) {
		s.ws.RegisterIntegration(session.ID(s.sid), "/bin/bash", transport.IntegrationStarting, ssh.ReasonNone)
		s.ws.PublishLifecycle(lifecyclepub.Fact{
			Lane:      string(typedAxisLane),
			Domain:    "dom-typed-axis",
			Epoch:     1,
			Lifecycle: lifecyclepub.LifecyclePromptReady,
		})
		if got, ok := s.tryReadIntegration(200 * time.Millisecond); ok && got.Status == "integrated" {
			return
		}
	}
	t.Fatal("the parent session never reported `integrated`; the precondition this test is about was never reached")
}

const wantWithinTypedAxis = 30 * time.Second

// integrationFrame is the notification's shape, declared here rather than
// imported: the transport's own struct is unexported, and restating the three
// fields under test keeps this a reader of the WIRE.
type integrationFrame struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Shell     string `json:"shell"`
}

func (s *typedAxisStack) tryReadIntegration(within time.Duration) (integrationFrame, bool) {
	select {
	case f, ok := <-s.frames:
		return f, ok
	case <-time.After(within):
		return integrationFrame{}, false
	}
}

// awaitStatus waits for the axis to REACH a status. A re-send of the status
// the session already had is not a state change, so it is skipped rather than
// mistaken for the answer. The deadline is a failsafe against a hang and never
// the measurement.
func (s *typedAxisStack) awaitStatus(t *testing.T, want string) integrationFrame {
	t.Helper()
	deadline := time.After(wantWithinTypedAxis)
	for {
		select {
		case got, ok := <-s.frames:
			if !ok {
				t.Fatalf("the socket closed before the session reached status %q", want)
			}
			if got.Status == want {
				return got
			}
		case <-deadline:
			t.Fatalf("the session never reached status %q", want)
		}
	}
}

// typedAxisDelivery arms a delivery on the stack's session, wired to the
// PRODUCTION route (bindTypedIntegrationAxis) rather than to a copy of it.
func typedAxisDelivery(t *testing.T, s *typedAxisStack) (*typedDelivery, *harnessWindow) {
	t.Helper()
	runner, _, _, _ := typedTestRunner(t, refusingOracle{})
	bindTypedIntegrationAxis(runner, s.ws)
	runner.dial = func(string) (TypedMaster, error) { return refusingMaster{}, nil }
	win := newHarnessWindow()
	win.attach(devNull(t))
	runner.sessions = &countingTerminals{win: win}

	opts := shellintegration.LaunchOptions{
		SessionID: s.sid, Enhanced: true,
		Lane: string(typedAxisLane), Domain: "dom-typed-axis", Epoch: 1,
		LifecyclePort: 40123,
	}
	stage, err := shellintegration.Stage1Frame(shellintegration.ShellAuto, opts)
	if err != nil {
		t.Fatalf("stage-1: %v", err)
	}
	d, err := runner.arm(s.sid, string(typedAxisLane), filepath.Join(t.TempDir(), "m"),
		shellintegration.BootstrapPlan{Stage1: stage})
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	// §6.1 step 5's barrier, exactly as buildSSHChildBootstrap installs it:
	// frame 2 waits for the publish to reach a terminal outcome. It is what
	// orders the publish's own failure before the outcome that reads it, so
	// the substitution below is a happens-before and not a race.
	d.plan.Ordered = func(ctx context.Context) error {
		select {
		case <-d.publishSettled:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d, win
}

// A refusal the far side names reaches the product as that refusal, on the
// wire, on the typed path — not as a log line and not as `unknown`.
func TestTypedLine_ARefusedBootstrapNamesItsReasonOnTheWire(t *testing.T) {
	s := newTypedAxisStack(t)
	s.integratedParent(t)

	d, win := typedAxisDelivery(t, s)
	// The far side answers with a terminal outcome from the closed set before
	// it ever announces itself: §6.4's "a refusal before READY". Scripted, so
	// nothing here waits on a deadline to produce the outcome.
	win.feed([]byte(shellintegration.OutcomePrefix +
		shellintegration.OutcomeToken(shellintegration.OutcomeNoSecureTemp) + "\n"))
	d.doRun(context.Background())

	got := s.awaitStatus(t, transport.IntegrationConventional)
	if got.Reason != string(ssh.ReasonNoSecureTemp) {
		t.Errorf("the product was told %q, want %q — the backend knew which of thirty-one "+
			"refusals happened and the user must be told the same one",
			got.Reason, ssh.ReasonNoSecureTemp)
	}
}

// And the path with no outcome to report at all: the control socket never
// completes the handshake, so nothing is published and nothing is minted.
//
// §7 forbids `starting` from being permanent, and arm has just put this
// session there — so this path must name a reason too, or the route would have
// traded a log-only degrade for a session stuck in `starting` with nothing
// else coming.
func TestTypedLine_OwnershipNeverProvenStillNamesAReason(t *testing.T) {
	s := newTypedAxisStack(t)
	s.integratedParent(t)

	d, _ := typedAxisDelivery(t, s)
	// A dialer that never succeeds, and a context already cancelled so the
	// wait ends at the event rather than at the five-minute deadline.
	d.runner.dial = (&countingDialer{}).dial
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.doRun(ctx)

	got := s.awaitStatus(t, transport.IntegrationConventional)
	if got.Reason != string(ssh.ReasonChannelUnavailable) {
		t.Errorf("the product was told %q, want %q", got.Reason, ssh.ReasonChannelUnavailable)
	}
}

// §6.4's `subsystem` row on the typed path, reported as its CAUSE. The far
// side answers generation-unavailable, which is true and is the symptom; the
// half a user can act on is that nocx could not write a generation, because
// the master refused the auxiliary channel it would have written it over.
//
// The saved path has asserted this since P5
// (TestMintOrdering_AFailedPublishRenamesAMissingGeneration). Asserting it
// here too is the point of bootstrapProductReason being one function: two
// paths, one answer, and a test on each side that would catch them drifting.
func TestTypedLine_AFailedPublishRenamesAMissingGenerationOnTheWire(t *testing.T) {
	s := newTypedAxisStack(t)
	s.integratedParent(t)

	// typedAxisDelivery's master refuses every auxiliary channel, so the
	// publish reaches its terminal outcome as a failure without anything
	// being written to the far host.
	d, win := typedAxisDelivery(t, s)
	win.feed([]byte(shellintegration.LoaderReadyToken + "\n"))
	win.feed([]byte(shellintegration.StageReadyToken + "\n"))
	win.feed([]byte(shellintegration.OutcomePrefix +
		shellintegration.OutcomeToken(shellintegration.OutcomeGenerationUnavailable) + "\n"))
	d.doRun(context.Background())

	got := s.awaitStatus(t, transport.IntegrationConventional)
	if got.Reason != string(ssh.ReasonPublishUnavailable) {
		t.Errorf("the product was told %q, want %q — the symptom is that nothing is installed, "+
			"the cause is that nocx could not install it", got.Reason, ssh.ReasonPublishUnavailable)
	}
}
