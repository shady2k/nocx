//go:build nocx_local_ssh

package session_test

// ONE DAEMON, TWO COORDINATORS: a pane's tool connections belong to the
// coordinator that OPENED the pane (nocx-50w7p.18).
//
// # The defect these tests are the acceptance for
//
// A helper endpoint socket is keyed by the GENERATION and its directory is
// derived from the account's home and nothing else, so one account runs ONE
// daemon per generation and that daemon serves every coordinator of that
// account at once (D12). Until this bead the daemon took the socket it
// forwards a pane's far-side tool connections into from its OWN start
// environment (`NOCX_TOOL_SOCKET`), which is the environment of whichever
// coordinator happened to start it. A pane opened by any other coordinator was
// therefore forwarded to the first one's endpoint — a person's agent reaching
// a backend that never asked for their pane.
//
// # What is real here, and what the "far host" is
//
// Real: the ssh connection, the `streamlocal-forward@openssh.com` listeners the
// far host's sshd granted at the helper's request, one `host.Host` protocol
// engine PER coordinator connection on the same daemon, the same session
// service behind both, the helper's per-connection dial into the endpoint each
// REQUEST named, and the coordinator-side Unix sockets the bytes land on.
//
// The far host is this package's in-process ssh fixture, as everywhere in this
// package: a real server with real listeners on 127.0.0.1. So "an agent
// process on the far host dials a path" is this test dialling the path the far
// host's sshd created — the same act, on the same machine, as the
// `nocx-helper mcp --socket <path>` process the launch enables.
//
// # What is NOT proven here, said out loud
//
// The tool endpoint's ADMISSION, exactly as ssh_tool_socket_test.go's header
// states: a far agent has no local pid to enrol, so what these tests prove is
// the TRANSPORT decision — WHICH endpoint a pane's bytes arrive at.

import (
	"bufio"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// addCoordinator dials a SECOND coordinator connection to the SAME helper
// daemon, over the shared helper that keeps the two-coordinator test shape in
// one place (tool_endpoint_test.go): its own protocol engine, its own binding
// to the same session service, and its own reverse-answer registry — this
// daemon's ssh service included, which is what makes the second coordinator
// able to open ssh panes at all.
func (s *sshStand) addCoordinator(t *testing.T) *client.Client {
	t.Helper()
	return addCoordinatorTo(t, s.sessions, sshTestHash, standLog(), s.coord.registry(), func(h *host.Host) {
		h.Register(s.sshsvc)
	})
}

// TestEachCoordinatorsPaneForwardsToItsOwnToolEndpoint is the bead's first
// acceptance criterion, over the real socket: TWO coordinators ride ONE daemon,
// each opens a pane, and each pane's far-side tool connection arrives at ITS
// OWN coordinator's endpoint.
//
// The endpoints are two distinct Unix sockets and the far paths are two
// distinct paths on the fixture's "far host", so a pane forwarded to the wrong
// endpoint is visible as a line arriving at the wrong socket — which is what
// the mutation this bead fixes produced, for every pane but the first.
// waitRecord waits for the pane record a forwarded connection announces, and
// answers the session it names. It is separate from waitLine because the two
// are different facts: the record says WHICH pane the connection is for, the
// line says what the far agent asked.
func (e *toolEndpointStand) waitRecord(t *testing.T) string {
	t.Helper()
	select {
	case session := <-e.record:
		return session
	case <-time.After(toolEndpointWait):
		t.Fatalf("no pane record reached this coordinator's tool endpoint, so the connection named no pane")
		return ""
	}
}

func TestEachCoordinatorsPaneForwardsToItsOwnToolEndpoint(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	first := stand.client
	second := stand.addCoordinator(t)

	dir := storagetest.SocketDir(t)
	farA, farB := filepath.Join(dir, "far-a.sock"), filepath.Join(dir, "far-b.sock")
	coordA, coordB := filepath.Join(dir, "coord-a.sock"), filepath.Join(dir, "coord-b.sock")
	endpointA := serveToolEndpoint(t, coordA, true)
	endpointB := serveToolEndpoint(t, coordB, true)

	// EACH COORDINATOR OPENS ITS OWN PANE, and then asks for that pane's tool
	// socket with ITS OWN endpoint as the target (nocx-e2bws): the far path and
	// the local target are the request's now, where they used to ride the
	// spawn — which is what makes one pane's tools belong to the coordinator
	// that asked for them, on one daemon serving both.
	entryA, err := first.SpawnSSH(context.Background(), stand.spawnParams(t, proto.SSHModeAuto))
	if err != nil {
		t.Fatalf("the first coordinator's spawn-ssh: %v", err)
	}
	if _, opErr := first.OpenToolSocket(context.Background(), proto.ToolSocketParams{
		Destination: stand.spawnParams(t, proto.SSHModeAuto).Destination,
		Path:        farA, Target: coordA, Session: entryA.HostSessionID.Session,
	}); opErr != nil {
		t.Fatalf("the first coordinator's tool socket: %v", opErr)
	}
	f.waitForwardGranted(t)
	entryB, err := second.SpawnSSH(context.Background(), stand.spawnParams(t, proto.SSHModeAuto))
	if err != nil {
		t.Fatalf("the second coordinator's spawn-ssh: %v", err)
	}
	if _, opErr := second.OpenToolSocket(context.Background(), proto.ToolSocketParams{
		Destination: stand.spawnParams(t, proto.SSHModeAuto).Destination,
		Path:        farB, Target: coordB, Session: entryB.HostSessionID.Session,
	}); opErr != nil {
		t.Fatalf("the second coordinator's tool socket: %v", opErr)
	}
	f.waitForwardGranted(t)

	// AN AGENT PROCESS ON THE FAR HOST dials each pane's own path. Two writes,
	// and which endpoint sees which line is the whole assertion.
	agentA := dialFarTool(t, farA)
	agentB := dialFarTool(t, farB)
	if _, err := agentA.Write([]byte("a-tools\n")); err != nil {
		t.Fatalf("the first pane's far agent could not write: %v", err)
	}
	if _, err := agentB.Write([]byte("b-tools\n")); err != nil {
		t.Fatalf("the second pane's far agent could not write: %v", err)
	}

	// EACH ENDPOINT IS TOLD WHICH PANE ITS CONNECTION IS FOR before the bytes
	// arrive, and it is told the pane of the coordinator that ASKED (nocx-50w7p.16
	// over nocx-50w7p.18's per-request target): the two panes are the same
	// helper, the same daemon and the same account, and the only thing telling
	// them apart is this record.
	if got := endpointA.waitRecord(t); got != entryA.HostSessionID.Session {
		t.Fatalf("the first endpoint's connection announced pane %q, want its own pane %q", got, entryA.HostSessionID.Session)
	}
	if got := endpointB.waitRecord(t); got != entryB.HostSessionID.Session {
		t.Fatalf("the second endpoint's connection announced pane %q, want its own pane %q", got, entryB.HostSessionID.Session)
	}
	if entryA.HostSessionID.Session == entryB.HostSessionID.Session {
		t.Fatal("the two panes were given one session, so this test could not tell them apart")
	}
	if got := endpointA.waitLine(t); got != "a-tools" {
		t.Fatalf("the first coordinator's endpoint was sent %q, want its own pane's line", got)
	}
	if got := endpointB.waitLine(t); got != "b-tools" {
		t.Fatalf("the second coordinator's endpoint was sent %q, want its own pane's line", got)
	}
	// And nothing went anywhere else: the line a pane's agent writes is the
	// line ITS coordinator's endpoint receives, and neither endpoint received
	// the other coordinator's.
	endpointA.nothingSeen(t, "the first coordinator's endpoint, after its own pane's single line")
	endpointB.nothingSeen(t, "the second coordinator's endpoint, after its own pane's single line")
}

// dialFarTool dials a path the far host's sshd created, as the agent process in
// a pane does.
func dialFarTool(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the far host's agent could not dial %s: %v", path, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestAFarToolConnectionEndsWithTheSessionThatJustifiesIt — the closing half,
// and precisely the half TestAnUnforwardedToolSocketPathIsRefusedByTheFarSide
// stepped around: that test closes its live connection FIRST, and says why —
// "an open connection would keep the forwarded channel alive, and the teardown
// under test would be measuring the test's own client rather than the
// listener's withdrawal". A far agent's pipe outliving its pane is the defect,
// not a convenience for the test: the connection carries authority over a
// session that has ended (ADR-0058), and nobody on the far side is going to
// close it — the agent is a program that thinks it still has a tool socket.
//
// # Why there are TWO panes
//
// One pane cannot show this. Its close releases the pane's pooled reference,
// and when that is the last reference the ssh connection itself goes — which
// tears down every forwarded channel on it, the far agent's included. So a
// single-pane test passes with the cancellation removed, and proves the pool
// rather than the pane. The two panes here share one pooled connection (AD-4
// keys the pool by destination), so closing the FIRST leaves the connection
// alive: what ends its agent's tool connection is this pane ending it, and the
// second pane still working is the evidence that nothing else did.
func TestAFarToolConnectionEndsWithTheSessionThatJustifiesIt(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	dir := storagetest.SocketDir(t)
	farA, farB := filepath.Join(dir, "far-a.sock"), filepath.Join(dir, "far-b.sock")
	endpoint := serveToolEndpoint(t, stand.toolSocket, true)

	entryA := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeAuto))
	if _, opErr := stand.client.OpenToolSocket(context.Background(), proto.ToolSocketParams{
		Destination: stand.spawnParams(t, proto.SSHModeAuto).Destination,
		Path:        farA, Target: stand.toolSocket, Session: entryA.HostSessionID.Session,
	}); opErr != nil {
		t.Fatalf("the first pane's tool socket: %v", opErr)
	}
	f.waitForwardGranted(t)
	entryB := stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeAuto))
	if _, opErr := stand.client.OpenToolSocket(context.Background(), proto.ToolSocketParams{
		Destination: stand.spawnParams(t, proto.SSHModeAuto).Destination,
		Path:        farB, Target: stand.toolSocket, Session: entryB.HostSessionID.Session,
	}); opErr != nil {
		t.Fatalf("the second pane's tool socket: %v", opErr)
	}
	f.waitForwardGranted(t)

	// ONE AT A TIME, and that is not tidiness: what this test needs is which
	// agent connection belongs to which pane, and the only thing that says so
	// is the ORDER (the record names a session; the socket it arrived on is not
	// in the record). Dialling and serving A completely before B touches the
	// far side makes that mapping a fact rather than a hope.
	agentA := dialFarTool(t, farA)
	if _, err := agentA.Write([]byte("a-tools\n")); err != nil {
		t.Fatalf("the first far agent could not write: %v", err)
	}
	// PAIRED SUCCESS FIRST: each connection is SERVED while its session lives,
	// so what follows is something taken away rather than something never
	// given.
	if got := endpoint.waitRecord(t); got != entryA.HostSessionID.Session {
		t.Fatalf("the first connection announced pane %q, want %q", got, entryA.HostSessionID.Session)
	}
	if got := endpoint.waitLine(t); got != "a-tools" {
		t.Fatalf("the first far agent's line reached the endpoint as %q", got)
	}
	consumeAnswer(t, agentA)

	agentB := dialFarTool(t, farB)
	if _, err := agentB.Write([]byte("b-tools\n")); err != nil {
		t.Fatalf("the second far agent could not write: %v", err)
	}
	if got := endpoint.waitRecord(t); got != entryB.HostSessionID.Session {
		t.Fatalf("the second connection announced pane %q, want %q", got, entryB.HostSessionID.Session)
	}
	if got := endpoint.waitLine(t); got != "b-tools" {
		t.Fatalf("the second far agent's line reached the endpoint as %q", got)
	}
	consumeAnswer(t, agentB)

	// THE FIRST PANE'S OWN END. Nothing on the far side closes anything, and
	// the pooled connection survives it because the second pane holds it.
	if err := stand.client.CloseSession(context.Background(), entryA.HostSessionID); err != nil {
		t.Fatalf("close-session: %v", err)
	}

	if err := agentA.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if line, err := bufio.NewReader(agentA).ReadBytes('\n'); err == nil || len(line) > 0 {
		t.Fatalf("after the session closed the far agent's connection answered %q (err %v), want it ended", line, err)
	}

	// AND THE OTHER PANE IS UNTOUCHED — the pairing that says the ending above
	// was this pane's own, and not the transport going away for both.
	if _, err := agentB.Write([]byte("b-tools\n")); err != nil {
		t.Fatalf("the surviving pane's agent could not write: %v", err)
	}
	if got := endpoint.waitLine(t); got != "b-tools" {
		t.Fatalf("the surviving pane's line reached the endpoint as %q", got)
	}
}

// consumeAnswer reads one answer from a far agent, so that a later read is about
// what happened NEXT rather than about a reply already in flight.
func consumeAnswer(t *testing.T, agent net.Conn) {
	t.Helper()
	if err := agent.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if answer, err := bufio.NewReader(agent).ReadString('\n'); err != nil {
		t.Fatalf("a far agent was never answered, so nothing is being taken from it: %v", err)
	} else if !strings.Contains(answer, "tools-ok") {
		t.Fatalf("a far agent was answered %q, want the endpoint's own answer", answer)
	}
}

// TestAPaneWhoseCoordinatorHasGoneIsRefusedAndNotReRouted is the bead's second
// acceptance criterion: the target is the one the REQUEST named, so a
// coordinator that has gone leaves its pane's tool connections REFUSED — never
// re-routed to another coordinator of the same account that is still running.
//
// The arrangement is the production one: coordinator A is the one whose
// environment the daemon was started from (the stand's default endpoint, LIVE
// here), coordinator B opens this pane and names its own endpoint, and then B
// goes away — its process exited and its socket file is still there, which is
// the state a coordinator's death actually leaves on disk.
//
// Three assertions, and each is a different half of the sentence:
//
//   - the far agent's connection ENDS (the helper closes it) rather than being
//     left hanging or answered by somebody else;
//   - coordinator A's endpoint receives NOTHING — this is the defect, and it is
//     exactly where a daemon-scoped target (A's, since A started the daemon)
//     sent these bytes;
//   - the helper's own account of the refusal NAMES the endpoint that is gone,
//     which is what "refused by name" means where the refusing party is the
//     only one that can say it: a closed Unix socket can carry no sentence.
//
// The far agent's WRITE is not one of them. The helper closes this connection
// in the same moment it refuses it, and the far side is free to write into a
// connection that has already been closed — EPIPE is the refusal winning the
// race, which is the outcome this test wants, not a failure. Asserting that
// write would make the test about which goroutine reached the socket first.
func TestAPaneWhoseCoordinatorHasGoneIsRefusedAndNotReRouted(t *testing.T) {
	// logtest.Slog, not a bare bytes.Buffer (nocx-n14oo.10): the refusal this
	// test reads for is logged by toolSocket.forward on ITS OWN goroutine
	// (internal/helper/sshsvc/toolsocket.go), racing this test's read of the
	// same buffer under -race — CI's own finding. logtest's store is
	// mutex-guarded and WaitForSlog is a condition wait on it, so the read
	// below is ordered after the write rather than merely usually after it.
	standLog := logtest.Slog(t)
	standLogger = standLog
	t.Cleanup(func() { standLogger = nil })

	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	second := stand.addCoordinator(t)
	// The coordinator that started this daemon, still running.
	alive := serveToolEndpoint(t, stand.toolSocket, true)

	dir := storagetest.SocketDir(t)
	farPath := filepath.Join(dir, "far.sock")
	gonePath := filepath.Join(dir, "coord-gone.sock")
	gone, err := net.Listen("unix", gonePath)
	if err != nil {
		t.Fatalf("bind the coordinator endpoint that is about to go: %v", err)
	}

	entry, err := second.SpawnSSH(context.Background(), stand.spawnParams(t, proto.SSHModeAuto))
	if err != nil {
		t.Fatalf("spawn-ssh through the coordinator that is about to go: %v", err)
	}
	// The departed coordinator's endpoint is the TARGET of this request, which
	// is what makes its disappearance the thing under test (nocx-e2bws: the
	// target is the caller's, per request).
	if _, opErr := second.OpenToolSocket(context.Background(), proto.ToolSocketParams{
		Destination: stand.spawnParams(t, proto.SSHModeAuto).Destination,
		Path:        farPath, Target: gonePath, Session: entry.HostSessionID.Session,
	}); opErr != nil {
		t.Fatalf("the tool socket of the coordinator that is about to go: %v", opErr)
	}
	f.waitForwardGranted(t)
	if err := gone.Close(); err != nil {
		t.Fatalf("close the departed coordinator's listener: %v", err)
	}

	agent := dialFarTool(t, farPath)

	// The write is an ATTEMPT, and its outcome is deliberately NOT asserted.
	//
	// The refusal happens on the helper's side: it accepts this connection,
	// dials the pane's endpoint, finds it gone, logs the refusal and closes the
	// connection. `net.Dial` on the far side returns as soon as the connection
	// is ACCEPTED, which is before any of that — so the byte below can arrive
	// either before the close (write succeeds, the read then sees the end) or
	// after it (EPIPE). Both are the same product behaviour, and the second is
	// the refusal arriving FIRST rather than a failure of anything.
	//
	// What the criterion requires is that no byte reaches another coordinator
	// and that the refusal is named. Those are asserted below and neither of
	// them needs this write to have landed — which is why the ONLY error
	// tolerated here is the refusal itself (the helper closed the connection
	// under the write: EPIPE, or ECONNRESET where the kernel delivers a reset
	// instead). Anything else is a broken arrangement rather than a refusal, so
	// it still fails this test.
	if _, writeErr := agent.Write([]byte("tools/list\n")); writeErr != nil {
		if !errors.Is(writeErr, syscall.EPIPE) && !errors.Is(writeErr, syscall.ECONNRESET) {
			t.Fatalf("the far agent's write failed for a reason that is not the pane's refusal: %v", writeErr)
		}
		t.Logf("the pane refused the connection before the far agent's write arrived: %v", writeErr)
	}

	// The refusal is the connection ending. A read that blocks would be the
	// opposite of a refusal, so the deadline is what turns "nothing ever came"
	// into a failure with a sentence rather than a hang.
	if err := agent.SetReadDeadline(time.Now().Add(paneWait)); err != nil {
		t.Fatalf("set the far agent's read deadline: %v", err)
	}
	buf := make([]byte, 64)
	n, readErr := agent.Read(buf)
	if readErr == nil {
		t.Fatalf("the far agent was answered %d bytes by a pane whose coordinator is gone: %q", n, buf[:n])
	}
	if n != 0 {
		t.Fatalf("the far agent read %d bytes from a pane whose coordinator is gone: %q", n, buf[:n])
	}

	alive.nothingSeen(t, "a coordinator that did not open this pane")

	// A CONDITION WAIT, not a read: the far agent's connection ending (above)
	// proves the helper has decided to refuse, not that it has finished
	// LOGGING the refusal — toolSocket.forward writes that line after
	// closing the connection, on its own goroutine. Waiting for the record
	// is what makes the text.Contains checks below deterministic instead of
	// racing that write, which is what -race caught.
	if !logtest.WaitForSlog(standLog, paneWait, func(r logtest.Record) bool {
		return strings.Contains(r.Message, "refusing a far-side tool connection")
	}) {
		t.Fatalf("the helper did not refuse by name:\n%s", logtest.TextSlog(standLog))
	}
	if text := logtest.TextSlog(standLog); !strings.Contains(text, gonePath) {
		t.Fatalf("the refusal does not name the endpoint that is gone (%s):\n%s", gonePath, text)
	}
}
