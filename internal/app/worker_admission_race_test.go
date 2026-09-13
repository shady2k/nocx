package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/workers"
)

// THE RACE AN INDEPENDENT REVIEW FOUND (nocx-9mn6z, HIGH 2), DRIVEN AT BOTH
// NAMED GAPS.
//
// An admission is a decision and a publication, and they used to be two steps
// with a gap between them: the authorizer read the answer, returned the grant,
// and the endpoint recorded the connection afterwards. A withdrawal or a
// revocation landing in that gap found nothing registered to close — so the
// connection went on serving a grant nobody held, and the endpoint's own book
// was empty of it.
//
// Both tests below are CONTROLLED INTERLEAVINGS at one named gap each, held by a
// barrier rather than by a sleep: the admission runs to the gap on its own
// goroutine, the ending happens while it is parked there, and only then is it
// let go. Every one of them is paired with the same run without the ending,
// because a barrier that refused the admission on its own would prove nothing.

// admissionGate is the controller that parks an admission at the named gap. The
// publication is the LAST step of Admit, so parking the seam that runs just
// before it parks exactly the gap the review named — after the answer has been
// read, before the connection is recorded.
type admissionGate struct {
	inner   workerAuthParticipants
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newAdmissionGate(inner workerAuthParticipants) *admissionGate {
	return &admissionGate{inner: inner, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *admissionGate) ParticipantOf(ctx context.Context, sessionID string) (workers.Participant, error) {
	g.once.Do(func() {
		close(g.reached)
		<-g.release
	})
	return g.inner.ParticipantOf(ctx, sessionID)
}

// countingDispatcher wraps the stand's tool surface and counts what reached it,
// so a test can assert not merely that a caller was refused but that NOTHING was
// dispatched under an interval that had ended.
type countingDispatcher struct {
	inner dispatchingCatalogue
	mu    sync.Mutex
	calls int
}

func (d *countingDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return d.inner.Dispatch(inv)
}

func (d *countingDispatcher) Catalogue(grant content.Grant) []agenttools.Tool {
	return d.inner.Catalogue(grant)
}

func (d *countingDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// raceStand is the shipped endpoint over a real socket with the shipped
// authorizer and the shipped approval service, admitting through whatever
// participants seam the test hands it.
type raceStand struct {
	reg      *session.Reg
	grid     *paneviewtest.Views
	sess     session.Session
	agent    string
	approval *agentApprovalService
	endpoint *toolendpoint.Endpoint
	dispatch *countingDispatcher
}

func newRaceStand(t *testing.T, participants workerAuthParticipants) *raceStand {
	t.Helper()
	reg, sess, grid := openWorkerAuthSession(t)
	const ownedPID = 4242
	if err := reg.RecordOwnedProcessPID(sess.ID(), ownedPID); err != nil {
		t.Fatalf("record owned process pid: %v", err)
	}
	if err := grid.Watch(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	approval := approvalServiceForTest(t, nil)
	agent := fakeAgent(t, "racing-agent")
	executable, err := agentapproval.IdentityForPath(agent)
	if err != nil {
		t.Fatalf("identify agent: %v", err)
	}
	if recordErr := approval.store.Record(executable, approval.scope, agentapproval.Granted); recordErr != nil {
		t.Fatalf("record the answer: %v", recordErr)
	}
	if enrolErr := approval.Approve(context.Background(), sess.ID(), agent); enrolErr != nil {
		t.Fatalf("enrol the agent: %v", enrolErr)
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := mustToolAuthorizer(t, pinner, reg, grid, participants, workerTestWorkspace, approval)
	dispatch := &countingDispatcher{inner: newSharedToolDispatcher(t, emptyWorkerRecord())}
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      shortWorkerSocketDir(t),
		Peers:    workerAuthEndpointPeers{},
		Owner:    workerAuthEndpointOwner{},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new tool endpoint: %v", err)
	}
	if err := endpoint.Start(); err != nil {
		t.Fatalf("start tool endpoint: %v", err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })

	return &raceStand{reg: reg, grid: grid, sess: sess, agent: agent, approval: approval, endpoint: endpoint, dispatch: dispatch}
}

// enrollAgain is the withdrawal-then-re-enrol pair's second half: a person's
// answer is still in the store (a withdrawal ends the ENROLMENT, not the
// answer), so starting the agent again enrols the pane into a NEW interval.
func (s *raceStand) enrollAgain(t *testing.T) toolendpoint.AdmissionEpoch {
	t.Helper()
	if err := s.approval.Approve(context.Background(), s.sess.ID(), s.agent); err != nil {
		t.Fatalf("enrol again: %v", err)
	}
	epoch, live := s.approval.Interval(s.sess.ID(), s.approval.scope)
	if !live {
		t.Fatal("the second enrolment did not put the session into an interval")
	}
	return epoch
}

// A WITHDRAWAL BETWEEN THE DECISION AND THE PUBLICATION REFUSES THE CONNECTION.
//
// The decision was taken while the answer held — the epoch is read and the grant
// is built — and the withdrawal lands before the connection is recorded. The
// decision is therefore taken under an epoch that has ended, and the endpoint
// refuses the publication: the connection is closed, no call is served, and the
// endpoint's book holds nothing for the session.
//
// The pair below is the same run without the withdrawal. It is what makes this
// a test of the epoch rather than of the barrier.
func TestAWithdrawalBetweenTheDecisionAndThePublicationRefusesTheConnection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw bool
	}{
		{name: "withdrawn at the gap, the admission is refused", withdraw: true},
		{name: "not withdrawn, the same admission is served", withdraw: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := newAdmissionGate(emptyWorkerRecord())
			stand := newRaceStand(t, gate)

			conn, err := net.Dial("unix", stand.endpoint.SocketPath())
			if err != nil {
				t.Fatalf("dial tool endpoint: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			defer func() { closeIfOpen(t, gate.release) }()

			// The admission is parked at the gap: everything the grant is built
			// from has been read, and the connection is not yet recorded.
			select {
			case <-gate.reached:
			case <-time.After(5 * time.Second):
				t.Fatal("the admission never reached the publication gap")
			}

			if tc.withdraw {
				stand.approval.Forget(stand.sess.ID())
			}
			close(gate.release)

			if !tc.withdraw {
				// THE PAIRED SUCCESS: the same barrier, the same endpoint, and
				// a served call — so nothing below can be an artifact of the
				// gate itself.
				callOverEndpoint(t, conn, "paired-success")
				if stand.dispatch.count() != 1 {
					t.Fatalf("calls that reached the tool surface = %d, want the one that was served", stand.dispatch.count())
				}
				return
			}

			// NOTHING WAS SERVED UNDER THE ENDED EPOCH, and not merely "the
			// caller saw a refusal": the tool surface was never reached, because
			// the endpoint refuses before it reads a single request off the
			// connection. A hostile caller may have pipelined its call into the
			// socket before this point; those bytes are never read.
			line, err := readEndpointLine(conn, `{"jsonrpc":"2.0","id":"late","method":"workers.holdings","params":{}}`)
			if err == nil && !strings.Contains(line, `"error"`) {
				t.Fatalf("a call admitted under an ended epoch was served: %s", line)
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("the connection was neither served nor refused: %v", err)
			}
			if got := stand.dispatch.count(); got != 0 {
				t.Fatalf("calls that reached the tool surface = %d, want none: an ended interval dispatched work", got)
			}
			awaitAdmittedNone(t, stand.endpoint)
			expectNotAdmitted(t, stand.endpoint.SocketPath(), "a caller whose interval ended at the gap")
		})
	}
}

// A RE-ENROLMENT DOES NOT RESCUE THE CONNECTION ADMITTED BEFORE THE WITHDRAWAL.
//
// This is the second half of the review's finding, and the one a boolean cannot
// answer: the withdrawal ends interval 1, the pane enrols again into interval 2
// under the SAME answer, and a check that asks "is this session approved" now
// says yes — for a connection that was admitted before the withdrawal. The
// barrier sits inside the withdrawal, between the answer going and the interval
// being ended, which is exactly where the re-enrolment has to land for the
// question to be asked wrongly.
func TestAReEnrolmentDoesNotRescueTheConnectionAdmittedBeforeTheWithdrawal(t *testing.T) {
	stand := newRaceStand(t, emptyWorkerRecord())
	sid := stand.sess.ID()

	conn, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial tool endpoint: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	callOverEndpoint(t, conn, "first-interval")
	awaitAdmittedSession(t, stand.endpoint, sid)
	first, live := stand.approval.Interval(sid, stand.approval.scope)
	if !live {
		t.Fatal("the session was never in an interval")
	}

	// THE BARRIER: the withdrawal reaches the point where the answer has gone
	// and the interval has not yet been ended, and waits there.
	notice := make(chan struct{})
	released := make(chan struct{})
	var release sync.Once
	inner := stand.approval.authorityEnded
	if inner == nil {
		t.Fatal("the approval service has no interval end bound: nothing could ever close an admission")
	}
	stand.approval.BindAuthorityEnded(func(sessionID session.ID, epoch toolendpoint.AdmissionEpoch) {
		select {
		case <-notice:
		default:
			close(notice)
		}
		<-released
		inner(sessionID, epoch)
	})
	t.Cleanup(func() { release.Do(func() { close(released) }) })

	withdrawn := make(chan struct{})
	go func() {
		defer close(withdrawn)
		stand.approval.Forget(sid)
	}()
	select {
	case <-notice:
	case <-time.After(5 * time.Second):
		t.Fatal("the withdrawal never reached the interval's end")
	}

	// AND NOW the re-enrolment, inside that gap: a new interval, the same
	// answer, and a live connection still admitted under the old one.
	second := stand.enrollAgain(t)
	if second == first {
		t.Fatalf("the second enrolment re-used epoch %d; a new interval must be a new one", first)
	}
	release.Do(func() { close(released) })

	select {
	case <-withdrawn:
	case <-time.After(5 * time.Second):
		t.Fatal("the withdrawal never finished")
	}

	// THE OLD CONNECTION IS CLOSED ANYWAY, because it was admitted under the
	// interval that ended — not because the session is unapproved, since it is
	// approved again.
	expectClosed(t, conn, "the connection admitted under the withdrawn interval")
	awaitAdmittedNone(t, stand.endpoint)

	// AND THE NEW INTERVAL'S CALLER IS SERVED. A retirement of the first
	// interval must not take the second's connection with it — which is what a
	// closer keyed on the session rather than on the interval would do.
	fresh, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial tool endpoint after the re-enrolment: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	callOverEndpoint(t, fresh, "second-interval")
	awaitAdmittedSession(t, stand.endpoint, sid)
}

// callOverEndpoint requires a call to have been admitted and answered over a
// connection this test holds.
func callOverEndpoint(t *testing.T, conn net.Conn, id string) {
	t.Helper()
	line, err := readEndpointLine(conn, `{"jsonrpc":"2.0","id":"`+id+`","method":"workers.holdings","params":{}}`)
	if err != nil {
		t.Fatalf("call %s: %v", id, err)
	}
	if strings.Contains(line, `"error"`) {
		t.Fatalf("call %s refused: %s", id, line)
	}
}

// awaitAdmittedSession waits for the endpoint's book to hold exactly one
// session, the one named. It waits on an observable state change rather than on
// a duration, and the deadline is a hang limit.
func awaitAdmittedSession(t *testing.T, endpoint *toolendpoint.Endpoint, sid session.ID) {
	t.Helper()
	waitFor(t, "the endpoint to record the session's admission", func() bool {
		admitted := endpoint.AdmittedSessions()
		return len(admitted) == 1 && admitted[0] == string(sid)
	})
}

// awaitAdmittedNone waits for the endpoint's book to hold nothing.
func awaitAdmittedNone(t *testing.T, endpoint *toolendpoint.Endpoint) {
	t.Helper()
	waitFor(t, "the endpoint to hold no admission", func() bool {
		return len(endpoint.AdmittedSessions()) == 0
	})
}

// closeIfOpen closes a barrier's release channel once, whatever else ran.
func closeIfOpen(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

var (
	_ assistant.ToolDispatcher = (*holdingAppDispatcher)(nil)
	_ workerAuthParticipants   = (*admissionGate)(nil)
)
