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
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
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

// paneParams is spawnParams with this pane's own two tool values: the far
// host's path, and the endpoint ON THIS MACHINE the far side's bytes are for.
func (s *sshStand) paneParams(t *testing.T, farPath, endpoint string) proto.SSHSpawnParams {
	t.Helper()
	params := s.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolSocketPath = farPath
	params.AgentToolEndpoint = endpoint
	return params
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
func TestEachCoordinatorsPaneForwardsToItsOwnToolEndpoint(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	first := stand.client
	second := stand.addCoordinator(t)

	dir := t.TempDir()
	farA, farB := filepath.Join(dir, "far-a.sock"), filepath.Join(dir, "far-b.sock")
	coordA, coordB := filepath.Join(dir, "coord-a.sock"), filepath.Join(dir, "coord-b.sock")
	endpointA := serveToolEndpoint(t, coordA)
	endpointB := serveToolEndpoint(t, coordB)

	if _, err := first.SpawnSSH(context.Background(), stand.paneParams(t, farA, coordA)); err != nil {
		t.Fatalf("the first coordinator's spawn-ssh: %v", err)
	}
	f.waitForwardGranted(t)
	if _, err := second.SpawnSSH(context.Background(), stand.paneParams(t, farB, coordB)); err != nil {
		t.Fatalf("the second coordinator's spawn-ssh: %v", err)
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
func TestAPaneWhoseCoordinatorHasGoneIsRefusedAndNotReRouted(t *testing.T) {
	var logs bytes.Buffer
	standLogger = slog.New(slog.NewTextHandler(&logs, nil))
	t.Cleanup(func() { standLogger = nil })

	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	second := stand.addCoordinator(t)
	// The coordinator that started this daemon, still running.
	alive := serveToolEndpoint(t, stand.toolSocket)

	dir := t.TempDir()
	farPath := filepath.Join(dir, "far.sock")
	gonePath := filepath.Join(dir, "coord-gone.sock")
	gone, err := net.Listen("unix", gonePath)
	if err != nil {
		t.Fatalf("bind the coordinator endpoint that is about to go: %v", err)
	}

	if _, err := second.SpawnSSH(context.Background(), stand.paneParams(t, farPath, gonePath)); err != nil {
		t.Fatalf("spawn-ssh through the coordinator that is about to go: %v", err)
	}
	f.waitForwardGranted(t)
	if err := gone.Close(); err != nil {
		t.Fatalf("close the departed coordinator's listener: %v", err)
	}

	agent := dialFarTool(t, farPath)
	if _, err := agent.Write([]byte("tools/list\n")); err != nil {
		t.Fatalf("the far agent could not write to the forwarded path: %v", err)
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
	if !strings.Contains(logs.String(), "refusing a far-side tool connection") {
		t.Fatalf("the helper did not refuse by name:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), gonePath) {
		t.Fatalf("the refusal does not name the endpoint that is gone (%s):\n%s", gonePath, logs.String())
	}
}
