//go:build nocx_local_ssh

package session_test

// The pane's AGENT TOOL SOCKET, reaching the coordinator's tool endpoint
// through a forward on the pane's own connection (nocx-50w7p.14).
//
// # What is real, and what the "far host" is
//
// Real: the ssh connection, the `streamlocal-forward@openssh.com` listener the
// far host's sshd granted at the helper's request, the helper's forwarding of
// every connection that arrives on it, and the coordinator-side Unix socket the
// bytes land on.
//
// The far host is this fixture, which is what every test in this package means
// by it: a real ssh server in-process with real listeners on 127.0.0.1. So "an
// agent process on the far host dials a path" is this test dialling the path
// the far host's sshd created — the same act, on the same machine, as the
// `nocx-helper mcp --socket <path>` process the launch enables.
//
// # What is NOT proven here, said out loud
//
// The tool endpoint's ADMISSION. A tool endpoint connection is admitted by the
// kernel's Peer{UID,PID} against an enrolled local process tree
// (internal/toolendpoint's Authorizer; internal/app's toolAuthorizer requires
// OwnedProcessPID for the session and Member(peer.PID, root)). A remote pane's
// agent has no local pid, and `helperRegistry` records none for an ssh session,
// so the admission path for a far agent does not exist yet — internal/app's own
// note says so ("Admit currently refuses sessions whose root process the
// backend did not launch ... for when remote admission exists"). This test
// therefore proves the TRANSPORT the criterion names: the far side's bytes
// reach the coordinator's tool socket and come back. Whether the endpoint
// admits the connection as that pane's agent is a separate seam, and it is
// reported as remaining work rather than implied here.

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/shellintegration"
)

// TestAFarSideAgentReachesTheCoordinatorsToolSocketThroughThePaneForward is
// this bead's second acceptance criterion's positive half.
//
// The chain it watches: the coordinator names a path on the FAR host (the only
// party that knows that machine's layout can), the helper asks the far side's
// sshd to listen there, the far side's agent process dials it, and the bytes
// arrive at the coordinator's own tool socket and come back.
func TestAFarSideAgentReachesTheCoordinatorsToolSocketThroughThePaneForward(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	endpoint := serveToolEndpoint(t, stand.toolSocket, true)

	// The far host's path, named by the coordinator. It does not exist yet: the
	// far side's sshd creates it, and a fixture-owned directory stands in for
	// the account's own run directory there.
	farPath := filepath.Join(t.TempDir(), "nocx-tool.sock")
	const farHelper = "/far/home/.nocx/helpers/gen/nocx-helper"

	params := stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolSocketPath = farPath
	params.AgentHelperPath = farHelper
	pane := stand.mustSpawn(t, params)

	// The far host granted the path (its own record), so the path exists there.
	f.waitForwardGranted(t)

	// AN AGENT PROCESS ON THE FAR HOST dials the path its host now serves.
	agent, err := net.Dial("unix", farPath)
	if err != nil {
		t.Fatalf("the far host's agent could not dial %s: %v", farPath, err)
	}
	defer func() { _ = agent.Close() }()
	if _, writeErr := agent.Write([]byte("tools/list\n")); writeErr != nil {
		t.Fatalf("the agent could not write to the forwarded path: %v", writeErr)
	}

	// The coordinator's endpoint saw the PANE first (nocx-50w7p.16): the
	// record the helper writes ahead of the far agent's bytes names the session
	// whose pane this connection arrived on, which is the only thing that can
	// tell the coordinator which pane it is answering — a far agent has no pid
	// here to be matched against a process tree.
	if got := endpoint.waitRecord(t); got != pane.HostSessionID.Session {
		t.Fatalf("the connection announced pane %q, want the session the helper opened (%q)", got, pane.HostSessionID.Session)
	}
	// ...and then the far agent's own bytes, intact and unmoved.
	if got := endpoint.waitLine(t); got != "tools/list" {
		t.Fatalf("the coordinator's tool endpoint was sent %q, want the far agent's own line", got)
	}
	// ...and the answer came back to the agent, which is the half that proves
	// the pipe is bidirectional rather than a one-way deliver.
	answer, readErr := bufio.NewReader(agent).ReadString('\n')
	if readErr != nil {
		t.Fatalf("the far agent read no answer: %v", readErr)
	}
	if answer != "tools-ok\n" {
		t.Fatalf("the far agent was answered %q, want the endpoint's own line", answer)
	}

	// And the agent was TOLD where the socket is: the launcher's environment
	// reaches the far shell through stage-1, which is what the far side
	// received. The wait is on the bootstrap's own outcome token.
	f.waitFarOutput(t, shellintegration.OutcomePrefix)
	delivered := string(f.programInputSeen())
	if !strings.Contains(delivered, shellintegration.ToolSocketEnvVar+"='"+farPath+"'") {
		t.Fatalf("the far shell was not told the tool socket path %s:\n%s", farPath, tail(delivered, 600))
	}
	if !strings.Contains(delivered, "NOCX_AGENT_HELPER_PATH='"+farHelper+"'") {
		t.Fatalf("the far shell was not told the helper path %s:\n%s", farHelper, tail(delivered, 600))
	}

	// One connection, still: the socket's listener and the shell rode the
	// pane's own pooled connection.
	if got := f.connections(); got != 1 {
		t.Fatalf("the far host saw %d ssh connections for a pane with a tool socket", got)
	}
}

// TestAPaneWithNoToolEndpointRefusesAFarSocketPathByName is criterion 2's paired
// negative, and it asserts the refusal rather than the byte path: a coordinator
// that runs no tool endpoint (cmd/nocx-server answers nil, nil when it has no
// tool surface) asked the helper to listen on a far path it cannot serve, and
// the helper refuses BY NAME instead of rendering a path nothing answers into
// the far shell's environment.
//
// The consequence is asserted too: nothing was dialed and nothing was opened,
// so a request that cannot be honoured costs the far host nothing.
func TestAPaneWithNoToolEndpointRefusesAFarSocketPathByName(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStandWithoutToolEndpoint(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	farPath := filepath.Join(t.TempDir(), "nocx-tool.sock")
	params := stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolSocketPath = farPath

	_, err := stand.spawn(t, params)
	if err == nil {
		t.Fatal("a pane with no tool endpoint behind its far socket path spawned anyway")
	}
	if !strings.Contains(err.Error(), "no tool endpoint") || !strings.Contains(err.Error(), farPath) {
		t.Fatalf("the refusal does not name what it refused: %v", err)
	}
	if f.shellsSeen() != 0 || len(f.execsSeen()) != 0 {
		t.Fatalf("the far host was asked for a shell (%d) or an exec (%d) by a request the helper had already refused",
			f.shellsSeen(), len(f.execsSeen()))
	}
	if len(f.liveForwards()) != 0 {
		t.Fatalf("the far host holds listeners %v after a refusal", f.liveForwards())
	}
}

// TestAnUnforwardedToolSocketPathIsRefusedByTheFarSide is the other reading of
// "an unforwarded socket path is refused by name", and the one the far side
// itself answers: once the session is over the path is gone from that machine,
// and a process dialling it gets the path named in the error rather than a
// socket that accepts and says nothing.
func TestAnUnforwardedToolSocketPathIsRefusedByTheFarSide(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	_ = serveToolEndpoint(t, stand.toolSocket, true)

	farPath := filepath.Join(t.TempDir(), "nocx-tool.sock")
	params := stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolSocketPath = farPath
	entry := stand.mustSpawn(t, params)
	f.waitForwardGranted(t)

	live, err := net.Dial("unix", farPath)
	if err != nil {
		t.Fatalf("the forwarded path did not answer before the session ended: %v", err)
	}
	// Closed before the session ends: an open connection would keep the
	// forwarded channel alive, and the teardown under test would be measuring
	// the test's own client rather than the listener's withdrawal.
	_ = live.Close()
	if err := stand.client.CloseSession(context.Background(), entry.HostSessionID); err != nil {
		t.Fatalf("close-session: %v", err)
	}
	f.waitNoForwards(t)

	// The path is gone with the listener, and the refusal carries it.
	_, afterErr := net.Dial("unix", farPath)
	if afterErr == nil {
		t.Fatalf("the far side still accepted a connection at %s after the listener was withdrawn", farPath)
	}
	if !strings.Contains(afterErr.Error(), farPath) {
		t.Fatalf("the far side's refusal does not name the path: %v", afterErr)
	}
	if _, statErr := os.Stat(farPath); statErr == nil {
		t.Fatalf("the socket file %s outlived the listener", farPath)
	}
}

// TestAFarShellWithNoHelperIsToldWhyItHasNoTools is criterion 2 of nocx-e2bws:
// a pane this machine's helper carries on a host with no nocx helper of its own
// has NO tool surface, and the far shell is TOLD why in the words a person can
// act on rather than left reporting a path no launch gave it.
//
// It reads the launch the far side actually received — the delivered stage-1
// frame, which is the same text the assertions above read for the presence case
// — so what is asserted is what the shell sources rather than what the request
// meant.
//
// THE ABSENCES ARE CHECKED AS ASSIGNMENTS, not as bare names: the delivered
// frame embeds the whole nocx.bash, which READS both variables
// (`${NOCX_TOOL_SOCKET:-}`, `${NOCX_AGENT_HELPER_PATH:-nocx-helper}`), so a
// substring check for the name alone would be satisfied by the script's own
// logic rather than by what this launch rendered (spawn_local_test.go records
// the same trap).
func TestAFarShellWithNoHelperIsToldWhyItHasNoTools(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	params := stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolsAbsent = string(shellintegration.AgentToolsNoHelperOnHost)
	stand.mustSpawn(t, params)

	f.waitFarOutput(t, shellintegration.OutcomePrefix)
	delivered := string(f.programInputSeen())

	want := shellintegration.AgentToolsAbsentEnvVar + "='" + string(shellintegration.AgentToolsNoHelperOnHost) + "'"
	if !strings.Contains(delivered, want) {
		t.Fatalf("the far shell was not told why it has no tools (%s):\n%s", want, tail(delivered, 600))
	}
	for _, name := range []string{shellintegration.ToolSocketEnvVar, "NOCX_AGENT_HELPER_PATH"} {
		if strings.Contains(delivered, name+"=") {
			t.Fatalf("the launch of a pane with no tool surface rendered %s=:\n%s", name, tail(delivered, 600))
		}
	}
}

// TestASpawnNamingAnAbsentReasonTheHelperDoesNotKnowIsRefused — and the same
// for a reason that CONTRADICTS a path. Both are refusals of the REQUEST, and
// both are raised before the claim is taken or anything is dialed: the code is
// rendered into a shell's environment, so a helper handing on a code its shells
// cannot turn into a sentence would leave a person with a pane that reports
// nothing at all (nocx-e2bws).
func TestASpawnNamingAnAbsentReasonTheHelperDoesNotKnowIsRefused(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})

	// A code from a newer coordinator: refused by name, in both directions
	// (a helper ten generations old answers the same way).
	params := stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolsAbsent = "some-future-code"
	if _, err := stand.spawn(t, params); err == nil {
		t.Fatal("a spawn naming a reason this helper does not know was accepted")
	}

	// AND A REASON BESIDE A PATH: the two say opposite things about this pane,
	// and a launch rendering both is a shell told there is a socket it may not
	// use.
	params = stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolsAbsent = string(shellintegration.AgentToolsNoHelperOnHost)
	params.AgentToolSocketPath = filepath.Join(t.TempDir(), "nocx-tool.sock")
	params.AgentToolEndpoint = stand.toolSocket
	if _, err := stand.spawn(t, params); err == nil {
		t.Fatal("a spawn that named both an absent reason and a tool socket was accepted")
	}
}
