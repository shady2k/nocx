package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/paneview/paneviewtest"
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
)

// A SESSION'S END ENDS ITS ADMISSION (ADR-0058, nocx-9mn6z).
//
// The endpoint admits a peer once per connection, so the grant inside a
// connection is immutable — which is the design, and also why the interval has
// to be CLOSED when the session it was granted for is over. It was not: the
// transport's unwatch closed the observation and withdrew the pane's screen and
// told nobody about the authority, so an exited-but-retained session kept its
// admitted caller, and every tool call it made went on running under a grant
// nobody held.
//
// What is real here: the shipped transport over a real session registry, the
// shipped endpoint over a real unix socket, the shipped authorizer, the shipped
// approval service over its real store and the shipped dispatch pipeline. The
// fakes are the pinner and the peer credentials — the same cut the other tests
// in this package make, because what is under test is whether a session's end
// closes a connection, not whether a process tree can be walked.
//
// THE CONNECTION IS LIVE AND HOSTILE WHEN IT ENDS. Every assertion below is made
// while a connection that has already been served is still open and willing to
// call again, because an interval that closes only for a connection that was
// never established would be the easy half.

// sessionEndStand is one admitted session, opened through the transport's OWN
// session-open path (the one monitorExit is started by) with a live tool
// connection attached.
type sessionEndStand struct {
	reg      *session.Reg
	ptys     *workerTestPTYFactory
	approval *agentApprovalService
	endpoint *toolendpoint.Endpoint
	sid      session.ID
	conn     net.Conn
}

// newSessionEndStand opens a session the way the backend does, enrols its pane,
// admits an agent to its tools, and leaves the connection open.
func newSessionEndStand(t *testing.T, dispatch assistant.ToolDispatcher) *sessionEndStand {
	t.Helper()
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)

	ptys := &workerTestPTYFactory{log: logger}
	reg := session.New(logger, ptys)
	approval := approvalServiceForTest(t, nil)
	// The admission interval's end, wired where the composition root wires it
	// (app.New): a session's end reaches the answer that admitted its agent's
	// tool connection.
	tp := transport.NewWSServer(logger, reg, transport.WithPaneAdmissions(approval))
	if err := tp.Start(ctx); err != nil {
		t.Fatalf("start ws server: %v", err)
	}
	t.Cleanup(func() {
		for _, s := range reg.List() {
			_ = reg.Close(s.ID())
		}
		_ = tp.Stop(ctx)
	})

	opened, err := tp.OpenSession(ctx, transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session through the transport: %v", err)
	}
	sid := opened.Session.ID()

	// The pane's enrolment, which the enroller opens on agent_enrol.
	grid := paneviewtest.NewViews(logger)
	if watchErr := grid.Watch(string(sid), 80, 24); watchErr != nil {
		t.Fatalf("enrol the pane: %v", watchErr)
	}
	const ownedPID = 4242
	if pidErr := reg.RecordOwnedProcessPID(sid, ownedPID); pidErr != nil {
		t.Fatalf("record owned process pid: %v", pidErr)
	}

	// The answer, and the enrolment that reads it — which is what mints the
	// interval's epoch.
	agent := fakeAgent(t, "admitted-agent")
	executable, err := agentapproval.IdentityForPath(agent)
	if err != nil {
		t.Fatalf("identify agent: %v", err)
	}
	if recordErr := approval.store.Record(executable, agentapproval.LocalDomain(), approval.scope, agentapproval.Granted); recordErr != nil {
		t.Fatalf("record the answer: %v", recordErr)
	}
	if enrolErr := approval.Approve(ctx, sid, agent); enrolErr != nil {
		t.Fatalf("enrol the agent: %v", enrolErr)
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	auth := mustToolAuthorizer(t, pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, approval)
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
	if startErr := endpoint.Start(); startErr != nil {
		t.Fatalf("start tool endpoint: %v", startErr)
	}
	t.Cleanup(func() { _ = endpoint.Close() })

	conn, err := net.Dial("unix", endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial tool endpoint: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	line, readErr := readEndpointLine(conn, `{"jsonrpc":"2.0","id":"1","method":"workers.holdings","params":{}}`)
	if readErr != nil {
		t.Fatalf("the admitted call was never answered: %v", readErr)
	}
	if containsRefusal(line) {
		t.Fatalf("the call this stand is about was refused: %s", line)
	}
	if admitted := endpoint.AdmittedSessions(); len(admitted) != 1 || admitted[0] != string(sid) {
		t.Fatalf("admitted sessions = %v, want the session that just called", admitted)
	}
	return &sessionEndStand{reg: reg, ptys: ptys, approval: approval, endpoint: endpoint, sid: sid, conn: conn}
}

// containsRefusal reports whether a response line is an error rather than a
// result. It is a substring test on the JSON-RPC envelope and not on a
// sentence, because the sentence is expected to be improved.
func containsRefusal(line string) bool { return strings.Contains(line, `"error"`) }

// endedBySessionExit closes the pane's PTY, which is what a program that
// finished looks like from here: the session's own channel ends and monitorExit
// wakes on sess.Done().
func (s *sessionEndStand) endedBySessionExit(t *testing.T) {
	t.Helper()
	pty := s.ptys.last()
	if pty == nil {
		t.Fatal("no pty was recorded for the session")
	}
	if err := pty.Close(); err != nil {
		t.Fatalf("end the pane's program: %v", err)
	}
}

// endedByExplicitClose closes the session through the registry, which is the
// path the renderer's own session.close takes.
func (s *sessionEndStand) endedByExplicitClose(t *testing.T) {
	t.Helper()
	if err := s.reg.Close(s.sid); err != nil {
		t.Fatalf("close the session: %v", err)
	}
}

// assertIntervalEnded requires the session's admission to be over, observed
// three ways: the live connection is closed, the endpoint admits nothing for the
// session, and a fresh dial is refused.
func (s *sessionEndStand) assertIntervalEnded(t *testing.T, what string) {
	t.Helper()
	expectClosed(t, s.conn, what)
	waitFor(t, "the endpoint to forget the session's admission", func() bool {
		return len(s.endpoint.AdmittedSessions()) == 0
	})
	if _, live := s.approval.Interval(s.sid, s.approval.scope); live {
		t.Fatalf("%s: the answer that admitted the caller is still in force", what)
	}
	expectNotAdmitted(t, s.endpoint.SocketPath(), what)
}

// THE SESSION'S OWN END. The pane's program exits, so nothing sends a
// withdrawal and nobody closes a tab: the end has to come from the session.
func TestASessionsExitClosesTheAdmittedToolConnection(t *testing.T) {
	stand := newSessionEndStand(t, newSharedToolDispatcher(t, emptyWorkerRecord()))
	stand.endedBySessionExit(t)
	stand.assertIntervalEnded(t, "an exited session's connection")
}

// THE EXPLICIT CLOSE. The renderer asks for the session to be closed, which is
// the same end reached deliberately rather than by the program finishing.
func TestAnExplicitCloseClosesTheAdmittedToolConnection(t *testing.T) {
	stand := newSessionEndStand(t, newSharedToolDispatcher(t, emptyWorkerRecord()))
	stand.endedByExplicitClose(t)
	stand.assertIntervalEnded(t, "a closed session's connection")
}

// holdingAppDispatcher answers every call but one at once, and holds
// workers.wait until its CONTEXT ends — so a close that never reached the
// request is a hold that never ends, which is what makes "the call in flight was
// cancelled" observable from outside rather than inferred from the socket.
type holdingAppDispatcher struct {
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func newHoldingAppDispatcher() *holdingAppDispatcher {
	return &holdingAppDispatcher{started: make(chan struct{}), canceled: make(chan struct{})}
}

func (d *holdingAppDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	if inv.Method != "workers.wait" {
		return `{"held":[]}`, nil
	}
	d.once.Do(func() {
		close(d.started)
		defer close(d.canceled)
		<-inv.Context.Done()
	})
	return "", inv.Context.Err()
}

func (d *holdingAppDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

// AND THE CALL IN FLIGHT IS CANCELLED WITH IT. A mutation an agent had already
// asked for must not go on running for a session that is over; the endpoint
// derives every request's context from its connection, so ending the interval
// ends the call.
func TestASessionsEndCancelsTheCallInFlightOnItsAdmittedConnection(t *testing.T) {
	dispatch := newHoldingAppDispatcher()
	stand := newSessionEndStand(t, dispatch)

	if _, err := io.WriteString(stand.conn, `{"jsonrpc":"2.0","id":"held","method":"workers.wait","params":{}}`+"\n"); err != nil {
		t.Fatalf("write the call that stays in flight: %v", err)
	}
	select {
	case <-dispatch.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the dispatcher")
	}

	stand.endedByExplicitClose(t)

	select {
	case <-dispatch.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the session ended and left the call in flight: its context was never canceled")
	}
	stand.assertIntervalEnded(t, "a closed session's connection")
}
