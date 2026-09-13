package app

import (
	"bufio"
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
	"github.com/shady2k/nocx/internal/peerpin"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
)

// ENDING AN ANSWER MUST END THE ADMISSION IT OPENED (ADR-0058).
//
// The endpoint admits a peer ONCE PER CONNECTION, and a coordinator now holds
// one connection for its whole session — so the grant inside that interval is
// immutable, which is the design and also the reason the interval has to be
// closable. Before this, revoking an agent's access deleted a document: the live
// connection, and with it the grant, went on working until the socket happened
// to end, and the next mutation from an agent a person had just turned off ran
// anyway.
//
// What is real here: the shipped endpoint over a real unix socket, the shipped
// authorizer (newToolAuthorizer), the shipped approval service over its real
// store, the session registry, the pane grid and the dispatch pipeline. The
// fakes are the pinner and the peer credentials — the same cut worker_auth_test.go
// makes, because what is under test is whether ending an answer closes a
// connection, not whether a process tree can be walked.

// admittedWorkerEndpoint publishes the shipped endpoint with one connection
// already admitted for one approved session, and hands back the pieces a test
// needs to end that answer.
func admittedWorkerEndpoint(t *testing.T) (*agentApprovalService, *toolendpoint.Endpoint, net.Conn) {
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
	agent := fakeAgent(t, "revoked-agent")
	executable, err := agentapproval.IdentityForPath(agent)
	if err != nil {
		t.Fatalf("identify agent: %v", err)
	}
	// The state a finished enrolment leaves behind, reached the way the
	// enrolment reaches it: the answer in the durable store, and then the
	// enrolment that reads it, which is what mints the interval's epoch.
	if recordErr := approval.store.Record(executable, agentapproval.LocalDomain(), approval.scope, agentapproval.Granted); recordErr != nil {
		t.Fatalf("record the answer: %v", recordErr)
	}
	if enrolErr := approval.Approve(context.Background(), sess.ID(), agent); enrolErr != nil {
		t.Fatalf("enrol the agent: %v", enrolErr)
	}
	if _, live := approval.Interval(sess.ID(), approval.scope); !live {
		t.Fatal("the approval this test is about to revoke was never in force")
	}

	pinner := &workerAuthPinner{
		root:   peerpin.Root{PID: ownedPID, StartTime: time.Unix(123, 0)},
		member: map[int]bool{9001: true},
	}
	record := emptyWorkerRecord()
	auth := mustToolAuthorizer(t, pinner, reg, grid, record, workerTestWorkspace, approval)

	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      shortWorkerSocketDir(t),
		Peers:    workerAuthEndpointPeers{},
		Owner:    workerAuthEndpointOwner{},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: newSharedToolDispatcher(t, record),
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
	// A request is what proves the admission: the endpoint admits on accept and
	// answers over the same connection.
	line, readErr := readEndpointLine(conn, `{"jsonrpc":"2.0","id":"1","method":"workers.holdings","params":{}}`)
	if readErr != nil {
		t.Fatalf("the admitted call was never answered: %v", readErr)
	}
	if strings.Contains(line, `"error"`) {
		t.Fatalf("the admitted call was refused: %s", line)
	}
	admitted := endpoint.AdmittedSessions()
	if len(admitted) != 1 || admitted[0] != string(sess.ID()) {
		t.Fatalf("admitted sessions = %v, want the one that just called", admitted)
	}
	return approval, endpoint, conn
}

// readEndpointLine writes one request (when there is one) and reads the next
// response line over the connection.
func readEndpointLine(conn net.Conn, request string) (string, error) {
	if request != "" {
		if _, err := io.WriteString(conn, request+"\n"); err != nil {
			return "", err
		}
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return line, err
	}
	return line, nil
}

// dialAndAsk dials the endpoint afresh and returns whatever came back, which is
// how "the next call is refused" is observed from outside.
func dialAndAsk(t *testing.T, socket string) (string, error) {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial tool endpoint: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return readEndpointLine(conn, `{"jsonrpc":"2.0","id":"2","method":"workers.holdings","params":{}}`)
}

// expectClosed requires the connection to have ENDED, not merely to have gone
// quiet: a read that times out is a connection still open, and accepting any
// error here would let a revocation that closed nothing pass as one that did.
func expectClosed(t *testing.T, conn net.Conn, what string) {
	t.Helper()
	line, err := readEndpointLine(conn, "")
	if err == nil {
		t.Fatalf("%s was still answering: %s", what, line)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("%s was left open: the read only expired (%v)", what, err)
	}
}

// expectNotAdmitted requires the fresh call to have been REFUSED. A refusal line
// is the ordinary shape; a connection error is the other one, because the
// endpoint refuses and closes and the two can cross — but a timeout is neither,
// and would mean nothing happened at all.
func expectNotAdmitted(t *testing.T, socket, what string) {
	t.Helper()
	line, err := dialAndAsk(t, socket)
	switch {
	case err == nil:
		if !strings.Contains(line, "worker caller refused") {
			t.Fatalf("%s was admitted and answered: %s", what, line)
		}
	case errors.Is(err, os.ErrDeadlineExceeded):
		t.Fatalf("%s was neither answered nor refused: %v", what, err)
	}
}

// The revocation a person performs in Settings: the transport's handler calls
// exactly this method, with the executable, its digest and the workspace.
func TestRevokingAgentApprovalClosesTheAdmittedEndpointConnection(t *testing.T) {
	approval, endpoint, conn := admittedWorkerEndpoint(t)
	executable, ok := approval.enrolledExecutable(t)
	if !ok {
		t.Fatal("the session has no approved executable to revoke")
	}

	forgotten, err := approval.ForgetAgentAccess(executable.Path, executable.SHA256, workerTestWorkspace, localMachineFacts())
	if err != nil || !forgotten {
		t.Fatalf("revoke: forgotten=%v err=%v", forgotten, err)
	}

	// THE INTERVAL ENDS. The connection admitted under that answer is closed —
	// which is what releases the session's caller slot with it — instead of
	// going on answering for an agent the person has turned off.
	expectClosed(t, conn, "the revoked agent's connection")

	// AND THE NEXT CALL IS REFUSED, because a fresh connection re-runs the
	// admission the revocation was about.
	expectNotAdmitted(t, endpoint.SocketPath(), "a revoked agent's next call")
}

// Withdrawal ends the same interval, and paneEnroller.Withdraw calls exactly
// this method. It is the other end of the same fact — an answer that stops
// holding — so it takes the same path.
func TestWithdrawingTheEnrolmentClosesTheAdmittedEndpointConnection(t *testing.T) {
	approval, endpoint, conn := admittedWorkerEndpoint(t)
	sid := admittedSession(t, endpoint)

	approval.Forget(sid)

	expectClosed(t, conn, "the withdrawn pane's connection")
	expectNotAdmitted(t, endpoint.SocketPath(), "a withdrawn pane's next call")
}

// admittedSession names the session the endpoint is holding an interval for.
func admittedSession(t *testing.T, endpoint *toolendpoint.Endpoint) session.ID {
	t.Helper()
	admitted := endpoint.AdmittedSessions()
	if len(admitted) != 1 {
		t.Fatalf("admitted sessions = %v, want one", admitted)
	}
	return session.ID(admitted[0])
}

// A REVOKE ENDS THE SESSIONS THAT ANSWER HELD, AND NOBODY ELSE'S.
//
// The revocation names an executable and a digest, and the sessions it holds
// are the ones still enrolled as that agent. Ending them is the fix; ending
// MORE than them is a defect of its own — one person turning off one agent must
// not drop a colleague's work in flight — which is why the sessions are read
// from the live enrolments rather than from a scan of everything the endpoint
// has admitted (nocx-9mn6z).
func TestRevokingOneAgentLeavesAnotherAgentsSessionAdmitted(t *testing.T) {
	ctx := context.Background()
	reg, first, grid := openWorkerAuthSession(t)
	second, err := reg.Open(ctx, session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open the second session: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(second.ID()) })

	const (
		firstRoot  = 4242
		secondRoot = 4243
		firstPeer  = 9001
		secondPeer = 9002
		nextPeer   = 9003
	)
	for _, pair := range []struct {
		sid  session.ID
		root int
	}{{first.ID(), firstRoot}, {second.ID(), secondRoot}} {
		if pidErr := reg.RecordOwnedProcessPID(pair.sid, pair.root); pidErr != nil {
			t.Fatalf("record owned process pid for %s: %v", pair.sid, pidErr)
		}
		if watchErr := grid.Watch(string(pair.sid), 80, 24); watchErr != nil {
			t.Fatalf("enrol %s: %v", pair.sid, watchErr)
		}
	}

	approval := approvalServiceForTest(t, nil)
	agents := map[session.ID]string{first.ID(): fakeAgent(t, "agent-one"), second.ID(): fakeAgent(t, "agent-two")}
	for sid, agent := range agents {
		executable, idErr := agentapproval.IdentityForPath(agent)
		if idErr != nil {
			t.Fatalf("identify %s: %v", agent, idErr)
		}
		if recordErr := approval.store.Record(executable, agentapproval.LocalDomain(), approval.scope, agentapproval.Granted); recordErr != nil {
			t.Fatalf("record the answer for %s: %v", sid, recordErr)
		}
		if enrolErr := approval.Approve(ctx, sid, agent); enrolErr != nil {
			t.Fatalf("enrol %s: %v", sid, enrolErr)
		}
	}

	// Two callers, one per pane: the peer pid is what binds a connection to a
	// session, so the pids are handed out in dial order and the pinner answers
	// which root each belongs to.
	peers := &dialOrderedPeers{pids: []int{firstPeer, secondPeer, nextPeer}}
	pinner := &multiRootPinner{
		roots: map[int]peerpin.Root{
			firstRoot:  {PID: firstRoot, StartTime: time.Unix(123, 0)},
			secondRoot: {PID: secondRoot, StartTime: time.Unix(123, 0)},
		},
		members: map[int]int{firstPeer: firstRoot, secondPeer: secondRoot, nextPeer: firstRoot},
	}
	auth := mustToolAuthorizer(t, pinner, reg, grid, emptyWorkerRecord(), workerTestWorkspace, approval)
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      shortWorkerSocketDir(t),
		Peers:    peers,
		Owner:    workerAuthEndpointOwner{},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: newSharedToolDispatcher(t, emptyWorkerRecord()),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("new tool endpoint: %v", err)
	}
	if startErr := endpoint.Start(); startErr != nil {
		t.Fatalf("start tool endpoint: %v", startErr)
	}
	t.Cleanup(func() { _ = endpoint.Close() })

	firstConn, err := net.Dial("unix", endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial the first caller: %v", err)
	}
	t.Cleanup(func() { _ = firstConn.Close() })
	callOverEndpoint(t, firstConn, "first-agent")

	secondConn, err := net.Dial("unix", endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial the second caller: %v", err)
	}
	t.Cleanup(func() { _ = secondConn.Close() })
	callOverEndpoint(t, secondConn, "second-agent")

	both := endpoint.AdmittedSessions()
	if len(both) != 2 {
		t.Fatalf("admitted sessions = %v, want both panes", both)
	}

	// The person turns ONE agent off.
	revoked, idErr := agentapproval.IdentityForPath(agents[first.ID()])
	if idErr != nil {
		t.Fatalf("identify the revoked agent: %v", idErr)
	}
	forgotten, revokeErr := approval.ForgetAgentAccess(revoked.Path, revoked.SHA256, workerTestWorkspace, localMachineFacts())
	if revokeErr != nil || !forgotten {
		t.Fatalf("revoke: forgotten=%v err=%v", forgotten, revokeErr)
	}

	expectClosed(t, firstConn, "the revoked agent's connection")
	// AND THE OTHER AGENT'S WORK IS UNTOUCHED: its connection was not closed,
	// its interval is intact, and it goes on being served.
	callOverEndpoint(t, secondConn, "still-working")
	if got := endpoint.AdmittedSessions(); len(got) != 1 || got[0] != string(second.ID()) {
		t.Fatalf("admitted sessions after the revoke = %v, want only %s", got, second.ID())
	}
}

// multiRootPinner pins more than one root, which the single-root fixture cannot:
// a test about one revocation sparing another agent's session needs two sessions
// admitted at once.
type multiRootPinner struct {
	roots   map[int]peerpin.Root
	members map[int]int
}

func (p *multiRootPinner) Pin(pid int) (peerpin.Root, error) {
	root, ok := p.roots[pid]
	if !ok {
		return peerpin.Root{}, peerpin.ErrGone
	}
	return root, nil
}

func (p *multiRootPinner) Member(child int, root peerpin.Root) (bool, error) {
	return p.members[child] == root.PID, nil
}

// dialOrderedPeers hands each accepted connection the next pid in a list, so a
// test can say which pane a connection belongs to.
type dialOrderedPeers struct {
	mu   sync.Mutex
	pids []int
	next int
}

func (p *dialOrderedPeers) PeerUID(*net.UnixConn) (uint32, error) { return 1000, nil }

func (p *dialOrderedPeers) PeerPID(*net.UnixConn) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next >= len(p.pids) {
		return 0, errors.New("the test ran out of peer pids")
	}
	pid := p.pids[p.next]
	p.next++
	return pid, nil
}

// enrolledExecutable is what this session is currently approved as, read the
// way the service reads it.
func (s *agentApprovalService) enrolledExecutable(t *testing.T) (agentapproval.Executable, bool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, enrolled := range s.enrolled {
		return enrolled.executable, true
	}
	return agentapproval.Executable{}, false
}
