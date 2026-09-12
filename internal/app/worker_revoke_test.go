package app

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
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
	if err := grid.Enrol(string(sess.ID()), 80, 24); err != nil {
		t.Fatalf("enrol session grid: %v", err)
	}

	approval := approvalServiceForTest(t, nil)
	executable, err := agentapproval.IdentityForPath(fakeAgent(t, "revoked-agent"))
	if err != nil {
		t.Fatalf("identify agent: %v", err)
	}
	// The state a finished enrolment leaves behind, written the way the
	// enrolment writes it: the identity this session was approved AS, and the
	// answer in the durable store. Approved reads both.
	approval.mu.Lock()
	approval.enrolled[sess.ID()] = executable
	approval.mu.Unlock()
	if recordErr := approval.store.Record(executable, approval.scope, agentapproval.Granted); recordErr != nil {
		t.Fatalf("record the answer: %v", recordErr)
	}
	if !approval.Approved(sess.ID(), approval.scope) {
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

	forgotten, err := approval.ForgetAgentAccess(executable.Path, executable.SHA256, workerTestWorkspace)
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

// enrolledExecutable is what this session is currently approved as, read the
// way the service reads it.
func (s *agentApprovalService) enrolledExecutable(t *testing.T) (agentapproval.Executable, bool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, executable := range s.enrolled {
		return executable, true
	}
	return agentapproval.Executable{}, false
}
