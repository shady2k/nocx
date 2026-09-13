package app

// A FAR PANE'S AGENT, ADMITTED BY ITS PANE (nocx-50w7p.16).
//
// Everything here is the shipped mechanism: the real tool endpoint over a real
// Unix socket, the real authorizer, the real approval service and the real
// dispatch, with the helper's half of the exchange represented the only way it
// can be — by the record a helper writes on the connection. What the tests
// answer is which SESSION such a connection is admitted as, and for how long.
//
// The helper is not stood in for by a fake: it is stood in for by the record
// the helper writes, which is the fact under test. A test that faked admission
// would be testing its own fake.

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

// rpcEnvelope is what this file reads back from the socket: the endpoint's own
// JSON-RPC answer, decoded narrowly enough to assert on.
type rpcEnvelope struct {
	Error *struct {
		Code int `json:"code"`
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
		Message string `json:"message"`
	} `json:"error"`
}

// requestHoldings is one ordinary call: how a connection proves it was admitted
// and served, rather than merely dialed.
const requestHoldings = `{"jsonrpc":"2.0","id":1,"method":"workers.holdings","params":{}}`

const (
	farPaneP = session.ID("0f9b4d7159d38afee9648a843654516f")
	farPaneQ = session.ID("11111111111111111111111111111111")
)

// farSessions is a registry holding named sessions and nothing else. It is a
// fake because the real one opens processes; the SHAPE of what it answers is
// the real one's, which is all the authorizer reads.
type farSessions struct {
	byID map[session.ID]session.Session
}

func (f farSessions) List() []session.Session {
	out := make([]session.Session, 0, len(f.byID))
	for _, sess := range f.byID {
		out = append(out, sess)
	}
	return out
}

func (f farSessions) Get(id session.ID) (session.Session, error) {
	sess, ok := f.byID[id]
	if !ok {
		return nil, errors.New("no such session")
	}
	return sess, nil
}

// OwnedProcessPID answers the way the real registry does: a LOCAL pane's
// process is one this machine launched, so it has an owned root pid; a remote
// pane's is on somebody else's machine and has none. It is the second half that
// makes the local admission arm refuse a far pane, and the first half that lets
// a local pane be enrolled at all.
func (f farSessions) OwnedProcessPID(id session.ID) (int, bool) {
	sess, ok := f.byID[id]
	if !ok || sess.Kind() != session.KindLocal {
		return 0, false
	}
	return farOwnedPID, true
}

// farOwnedPID is the pid the fake registry records for a local pane. No pinner
// in these tests resolves it: a local pane's admission is not what they are
// about, and a record naming one is refused before any pin is taken.
const farOwnedPID = 4242

// farWatched is the enrolment seam: which panes nocx is watching right now.
type farWatched map[string]bool

func (w farWatched) Watched(paneID string) bool { return w[paneID] }

// farDispatcher records the invocation the endpoint admitted under, which is
// where the session and the grant live.
type farDispatcher struct {
	mu    sync.Mutex
	calls []assistant.ToolInvocation
}

func (d *farDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	d.mu.Lock()
	d.calls = append(d.calls, inv)
	d.mu.Unlock()
	return `{"held":[]}`, nil
}

func (d *farDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

func (d *farDispatcher) last() assistant.ToolInvocation {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calls) == 0 {
		return assistant.ToolInvocation{}
	}
	return d.calls[len(d.calls)-1]
}

func (d *farDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// farStand is the whole coordinator-side chain for one test.
type farStand struct {
	approval *agentApprovalService
	sessions farSessions
	watched  farWatched
	endpoint *toolendpoint.Endpoint
	dispatch *farDispatcher
}

// remoteSession is a pane on a host, in the shape sessionDomain reads: the kind,
// the host, the account the connection authenticated as, and the host key it was
// accepted under.
func remoteSession(id session.ID, host, account, hostKey string) session.Session {
	return workerAuthSessionOverride{
		id:   id,
		kind: session.KindRemote,
		host: host,
		// The account is not a field of its own: it is an option on the
		// connection, which is why accountFromOptions exists and why this
		// test builds one the same way the coordinator does.
		sshOpts:     []ssh.ConnectOption{ssh.WithUser(account)},
		fingerprint: hostKey,
	}
}

func localSession(id session.ID) session.Session {
	return workerAuthSessionOverride{id: id, kind: session.KindLocal}
}

func newFarStand(t *testing.T, sessions ...session.Session) *farStand {
	t.Helper()
	byID := make(map[session.ID]session.Session, len(sessions))
	watched := farWatched{}
	for _, sess := range sessions {
		byID[sess.ID()] = sess
		watched[string(sess.ID())] = true
	}
	seam := farSessions{byID: byID}

	store := agentapproval.NewStore(log.NewSlogAdapter(nil), &approvalDocStore{}, "agent-approvals.json")
	approval := newAgentApprovalService(seam, store, workerTestWorkspace)
	approval.SetRequester(&recordingRequester{answer: true})

	auth := mustToolAuthorizer(t, &workerAuthPinner{}, seam, watched, emptyWorkerRecord(), workerTestWorkspace, approval)
	dispatch := &farDispatcher{}
	endpoint, err := toolendpoint.New(toolendpoint.Config{
		Dir:      shortWorkerSocketDir(t),
		Peers:    workerAuthEndpointPeers{},
		Owner:    workerAuthEndpointOwner{},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		// The helper's lane. A test dials this socket itself, so this test
		// process IS the helper for the purpose of the record — which is the
		// point: what the endpoint trusts is the lane, and here the lane is
		// the test.
		Lane: func(toolendpoint.Peer) bool { return true },
	})
	if err != nil {
		t.Fatalf("new tool endpoint: %v", err)
	}
	if err := endpoint.Start(); err != nil {
		t.Fatalf("start tool endpoint: %v", err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })

	return &farStand{approval: approval, sessions: seam, watched: watched, endpoint: endpoint, dispatch: dispatch}
}

// enrol answers the person's question about this agent for this pane, all the
// way through the shipped path: the answer is recorded for the pane's own
// machine and the enrolment opens the interval.
func (s *farStand) enrol(t *testing.T, sid session.ID, agent string) {
	t.Helper()
	err := s.approval.Approve(context.Background(), sid, agent)
	var pending *lifecyclepub.EnrolmentPending
	if errors.As(err, &pending) {
		// The question is open; this stand's requester says yes immediately, so
		// the answer is kept as soon as it settles.
		select {
		case reason := <-pending.Settled:
			if reason != "" {
				t.Fatalf("the question closed with %q, want the person's answer", reason)
			}
		case <-t.Context().Done():
			t.Fatal("the question never closed")
		}
		err = s.approval.Approve(context.Background(), sid, agent)
	}
	if err != nil {
		t.Fatalf("enrol %s: %v", sid, err)
	}
}

// callOver writes one pane record and one request on a fresh connection and
// answers what the endpoint said.
func (s *farStand) callOver(t *testing.T, pane string) rpcEnvelope {
	t.Helper()
	conn, err := net.Dial("unix", s.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial endpoint: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if pane != "" {
		record, encErr := panebind.Encode(pane)
		if encErr != nil {
			t.Fatalf("encode record: %v", encErr)
		}
		if _, werr := conn.Write(record); werr != nil {
			t.Fatalf("write record: %v", werr)
		}
	}
	if _, werr := conn.Write([]byte(requestHoldings + "\n")); werr != nil {
		// A refusal is written and the connection closed, and a write racing
		// that close fails with EPIPE — the answer is already on its way, and
		// the read below is what reports it.
		_ = werr
	}
	if derr := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	line, rerr := bufio.NewReader(conn).ReadBytes('\n')
	if rerr != nil {
		t.Fatalf("read response: %v", rerr)
	}
	var env rpcEnvelope
	if uerr := json.Unmarshal(line, &env); uerr != nil {
		t.Fatalf("decode %q: %v", line, uerr)
	}
	return env
}

// TestAFarPanesAgentIsAdmittedByThePaneItNamed — the positive half, and it
// asserts the SESSION the call ran under: the pane the helper named, not the
// helper's own process and not another pane of the same coordinator.
func TestAFarPanesAgentIsAdmittedByThePaneItNamed(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	agent := fakeAgent(t, "far-agent")
	stand.enrol(t, farPaneP, agent)

	env := stand.callOver(t, string(farPaneP))
	if env.Error != nil {
		t.Fatalf("a far pane's connection was refused: %+v", env.Error)
	}
	if got := stand.dispatch.last().RunContext.Session; got != string(farPaneP) {
		t.Fatalf("the call ran under session %q, want the pane the record named (%q)", got, farPaneP)
	}
}

// TestAFarConnectionIsNotAdmittedForAnotherPaneOfTheSameCoordinator — two panes
// on one coordinator, both enrolled and both approved. The grant a connection
// carries is the pane it NAMED and never the other one; a coordinator that
// answered with "some enrolled session" would hand one pane's agent the other
// pane's authority, and the two are the same helper, the same daemon and the
// same account.
func TestAFarConnectionIsNotAdmittedForAnotherPaneOfTheSameCoordinator(t *testing.T) {
	stand := newFarStand(t,
		remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"),
		remoteSession(farPaneQ, "other.example.com", "deploy", "SHA256:key-b"),
	)
	agent := fakeAgent(t, "far-agent")
	stand.enrol(t, farPaneP, agent)
	stand.enrol(t, farPaneQ, agent)

	if env := stand.callOver(t, string(farPaneP)); env.Error != nil {
		t.Fatalf("the first pane's connection was refused: %+v", env.Error)
	}
	first := stand.dispatch.last()
	if got := first.RunContext.Session; got != string(farPaneP) {
		t.Fatalf("the first pane's call ran under %q, want %q", got, farPaneP)
	}

	if env := stand.callOver(t, string(farPaneQ)); env.Error != nil {
		t.Fatalf("the second pane's connection was refused: %+v", env.Error)
	}
	second := stand.dispatch.last()
	if got := second.RunContext.Session; got != string(farPaneQ) {
		t.Fatalf("the second pane's call ran under %q, want %q", got, farPaneQ)
	}

	// And the scopes are the panes' own: a session scope names P for the first
	// call and Q for the second, so neither call could act for the other.
	if !grantScopesAPane(first.Grant, string(farPaneP)) {
		t.Fatalf("the first call's grant carries no session %q: %+v", farPaneP, first.Grant.Scopes)
	}
	if grantScopesAPane(first.Grant, string(farPaneQ)) {
		t.Fatalf("the first call's grant also carries the OTHER pane %q", farPaneQ)
	}
	if !grantScopesAPane(second.Grant, string(farPaneQ)) {
		t.Fatalf("the second call's grant carries no session %q: %+v", farPaneQ, second.Grant.Scopes)
	}
}

// grantScopesAPane answers whether one session's scope is named in the grant.
func grantScopesAPane(grant content.Grant, sid string) bool {
	for _, scope := range grant.Scopes {
		if scope.Kind == content.ResourceSession && scope.ID == sid {
			return true
		}
	}
	return false
}

// TestAFarPanesAgentIsRefusedAfterItsSessionEnds — the closing end. An admitted
// connection is closed when the session's interval ends, and a connection that
// arrives afterwards is refused rather than admitted into an interval that is
// over. The pair is the same call BEFORE the end.
func TestAFarPanesAgentIsRefusedAfterItsSessionEnds(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	agent := fakeAgent(t, "far-agent")
	stand.enrol(t, farPaneP, agent)

	// PAIRED SUCCESS FIRST, so "refused" below means the end did it.
	if env := stand.callOver(t, string(farPaneP)); env.Error != nil {
		t.Fatalf("before the session ended, the connection was refused: %+v", env.Error)
	}

	// A connection that is LIVE when the session ends, and PROVED live by being
	// served — a request and its answer. Waiting instead for "the session is
	// admitted" would wait on the wrong fact: the call above already admitted
	// this session's interval, so that wait returns at once and the session
	// could end before this connection had been read at all.
	conn, err := net.Dial("unix", stand.endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	record, err := panebind.Encode(string(farPaneP))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := conn.Write(append(record, []byte(requestHoldings+"\n")...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("the second connection was never served, so nothing is being tested about its end: %v", err)
	}

	stand.approval.SessionEnded(string(farPaneP))

	// The live connection is closed: a read that RETURNS is the closing event,
	// and a read that blocks is the defect — the grant it was let in under
	// would still be usable.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if line, err := reader.ReadBytes('\n'); err == nil || len(line) > 0 {
		t.Fatalf("after the session ended the connection answered %q (err %v), want it closed", line, err)
	}

	// And a NEW connection naming that pane is refused: the interval is over,
	// so the epoch it would have been admitted under is retired.
	env := stand.callOver(t, string(farPaneP))
	if env.Error == nil {
		t.Fatal("after the session ended, a new far connection was admitted")
	}
}

// TestAFarPaneIsAdmittedWithoutAProcessPinner — the seam that keeps the two
// arms from being one arm. Admission by pane is a session, an enrolment and an
// interval: it names a session on a host, and asks nothing of THIS machine's
// process tree. Admission by pid is the opposite — it says "a process of mine
// is the caller", and `pinner` is what turns a pid into that claim.
//
// So a coordinator that cannot pin processes must still admit a far agent and
// must not admit a local one. Required at the top of admittedPeer, the pinner
// would refuse far agents for a reason that does not apply to them, and the
// test would pass while the far arm was unreachable on such a machine.
func TestAFarPaneIsAdmittedWithoutAProcessPinner(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	stand.enrol(t, farPaneP, "claude")

	unpinnable, err := newToolAuthorizer(
		nil, stand.sessions, stand.watched, emptyWorkerRecord(), workerTestWorkspace, stand.approval)
	if err != nil {
		t.Fatalf("newToolAuthorizer: %v", err)
	}

	if _, _, _, ok := unpinnable.admittedPeer(toolendpoint.Peer{Pane: string(farPaneP)}); !ok {
		t.Fatal("a far pane was refused because this coordinator cannot pin processes")
	}
	if _, _, _, ok := unpinnable.admittedPeer(toolendpoint.Peer{PID: farOwnedPID}); ok {
		t.Fatal("a pid was admitted with no pinner to check it against")
	}
}

// TestAFarRecordCannotStandForALocalPane — the arm's guard. A local pane's
// agent can dial this socket itself, and it could prefix a record naming its own
// session; if a record were honoured for a local pane, that would be a way to
// claim authority without the kernel pin. It is not honoured, and the local
// rule still answers for it.
func TestAFarRecordCannotStandForALocalPane(t *testing.T) {
	stand := newFarStand(t, localSession(farPaneP))
	agent := fakeAgent(t, "far-agent")
	stand.enrol(t, farPaneP, agent)

	env := stand.callOver(t, string(farPaneP))
	if env.Error == nil {
		t.Fatal("a record naming a LOCAL pane was admitted through the far arm")
	}
	if stand.dispatch.count() != 0 {
		t.Fatalf("the dispatcher ran %d call(s) for a local pane claimed by a record", stand.dispatch.count())
	}
}

// TestAFarPaneIsRefusedUntilItIsEnrolledAndApproved — the two admission acts in
// order, each with its paired success: a watched pane with no answer is refused,
// and the same pane admits once the person has answered.
func TestAFarPaneIsRefusedUntilItIsEnrolledAndApproved(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	agent := fakeAgent(t, "far-agent")

	// Watched, but nobody has answered: the interval never opened.
	if env := stand.callOver(t, string(farPaneP)); env.Error == nil {
		t.Fatal("a pane with no approval was admitted")
	}
	if stand.dispatch.count() != 0 {
		t.Fatalf("the dispatcher ran %d call(s) before the person answered", stand.dispatch.count())
	}

	// PAIRED SUCCESS: the same connection, after the answer.
	stand.enrol(t, farPaneP, agent)
	if env := stand.callOver(t, string(farPaneP)); env.Error != nil {
		t.Fatalf("the pane the person answered about was refused: %+v", env.Error)
	}

	// And an UNWATCHED pane is refused even with an answer: the observation
	// interval is a separate admission act, and a pane nocx is not watching
	// cannot be an admitting principal.
	//
	// A stand of its own, and the REFUSAL'S REASON asserted: on the stand above
	// this call would be refused for the other reason a connection can be
	// refused — the session's caller slot is held by a connection that is still
	// open — and the test would pass without the enrolment check existing.
	unwatched := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	unwatched.enrol(t, farPaneP, agent)
	delete(unwatched.watched, string(farPaneP))
	env := unwatched.callOver(t, string(farPaneP))
	if env.Error == nil {
		t.Fatal("a pane that is no longer watched was admitted")
	}
	if !strings.Contains(env.Error.Data.Reason, "not in a pane nocx has enrolled") {
		t.Fatalf("the unwatched pane was refused for %q, want the enrolment refusal", env.Error.Data.Reason)
	}
}

// TestAFarRecordNamingASessionThisCoordinatorDoesNotHaveIsRefused — the name is
// not enough. A helper's record names a session; a session this coordinator does
// not hold is not a pane it may answer for.
func TestAFarRecordNamingASessionThisCoordinatorDoesNotHaveIsRefused(t *testing.T) {
	stand := newFarStand(t, remoteSession(farPaneP, "build.example.com", "deploy", "SHA256:key-a"))
	agent := fakeAgent(t, "far-agent")
	stand.enrol(t, farPaneP, agent)

	env := stand.callOver(t, string(farPaneQ))
	if env.Error == nil {
		t.Fatal("a record naming a session this coordinator does not have was admitted")
	}
	if stand.dispatch.count() != 0 {
		t.Fatalf("the dispatcher ran %d call(s) for an unknown pane", stand.dispatch.count())
	}
}

// TestTheLaneIsTheProcessAtTheOtherEndOfTheHelperConnection — the comparison
// itself, and the refusal when there is no helper connection to compare with.
func TestTheLaneIsTheProcessAtTheOtherEndOfTheHelperConnection(t *testing.T) {
	if !laneMatches(toolendpoint.Peer{UID: 1000, PID: 4242}, 4242) {
		t.Fatal("the helper's own pid was not recognised as the lane")
	}
	for _, tc := range []struct {
		name string
		peer toolendpoint.Peer
		lane int
	}{
		{"another process on this machine", toolendpoint.Peer{UID: 1000, PID: 4243}, 4242},
		{"no lane at all", toolendpoint.Peer{UID: 1000, PID: 4242}, 0},
		{"a peer with no pid", toolendpoint.Peer{UID: 1000}, 4242},
	} {
		if laneMatches(tc.peer, tc.lane) {
			t.Fatalf("%s was treated as the helper's lane", tc.name)
		}
	}
	// And an app with no helper connection has no lane: a coordinator that
	// never opened a pane never dialed a helper.
	var a *App
	if a.ToolLane(toolendpoint.Peer{UID: 1000, PID: os.Getpid()}) {
		t.Fatal("an app with no helper connection answered that a peer was the lane")
	}
}
