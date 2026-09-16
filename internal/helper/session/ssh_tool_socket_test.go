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
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestAFarSideAgentReachesTheCoordinatorsToolSocketThroughThePaneForward is the
// positive half of a far pane's tool surface (nocx-e2bws).
//
// The chain it watches: the coordinator names a path on the FAR host (the only
// party that knows that machine's layout can), the helper asks that host's sshd
// to listen there, an agent process there dials it, the bytes arrive at the
// coordinator's own tool socket with the PANE RECORD first, and the answer comes
// back. Then the same id ends the listener and the far side stops answering —
// the interval's closing edge, in the same test as its opening one.
//
// IT DRIVES THE OP, not a spawn: the spawn-ssh route's far paths are gone with
// the case they served (a pane this machine's helper carries on a host with no
// helper of its own has no tool surface at all), and this is the route that
// replaced them.
func TestAFarSideAgentReachesTheCoordinatorsToolSocketThroughThePaneForward(t *testing.T) {
	f := newSSHFixture(t, "pw", "printf 'ALIVE\n'; cat")
	stand := newSSHStand(t, f, &sshCoordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
	})
	endpoint := serveToolEndpoint(t, stand.toolSocket, true)

	// The far host's path, named by the coordinator. It does not exist yet: the
	// far side's sshd creates it, and a fixture-owned directory stands in for
	// the account's own run directory there.
	farPath := filepath.Join(storagetest.SocketDir(t), "nocx-tool.sock")

	// THE PANE FIRST, because the socket belongs to it: the session id the far
	// daemon mints is what every connection through the socket announces.
	params := stand.spawnParams(t, proto.SSHModeAuto)
	pane := stand.mustSpawn(t, params)

	opened, err := stand.client.OpenToolSocket(context.Background(), proto.ToolSocketParams{
		Destination: params.Destination,
		Path:        farPath,
		Target:      stand.toolSocket,
		Session:     pane.HostSessionID.Session,
	})
	if err != nil {
		t.Fatalf("open the far pane's tool socket: %v", err)
	}
	if opened.Path != farPath || opened.Forward.IsZero() {
		t.Fatalf("the op answered %+v, want the far path it bound and a listener id", opened)
	}

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

	// The coordinator's endpoint saw the PANE first (nocx-50w7p.16): the record
	// the helper writes ahead of the far agent's bytes names the session whose
	// pane this connection arrived on, which is the only thing that can tell the
	// coordinator which pane it is answering — a far agent has no pid here to be
	// matched against a process tree.
	if got := endpoint.waitRecord(t); got != pane.HostSessionID.Session {
		t.Fatalf("the connection announced pane %q, want the session the helper opened (%q)", got, pane.HostSessionID.Session)
	}
	// ...and then the far agent's own bytes, intact and unmoved.
	if got := endpoint.waitLine(t); got != "tools/list" {
		t.Fatalf("the coordinator's tool endpoint was sent %q, want the far agent's own line", got)
	}
	// ...and the answer came back to the agent, which is the half that proves the
	// pipe is bidirectional rather than a one-way deliver.
	answer, readErr := bufio.NewReader(agent).ReadString('\n')
	if readErr != nil {
		t.Fatalf("the far agent read no answer: %v", readErr)
	}
	if answer != "tools-ok\n" {
		t.Fatalf("the far agent was answered %q, want the endpoint's own line", answer)
	}

	// One connection, still: the listener and the shell rode the pane's own
	// pooled connection.
	if got := f.connections(); got != 1 {
		t.Fatalf("the far host saw %d ssh connections for a pane with a tool socket", got)
	}

	// AND THE SAME CALL ENDS IT. The id `unforward` names is the one this op
	// answered, and the far side stops answering with it — a socket nobody can
	// dial is what "the pane's tool surface is over" means on that host.
	_ = agent.Close()
	if err := stand.client.CloseListener(context.Background(), opened.Forward); err != nil {
		t.Fatalf("close the far pane's tool socket: %v", err)
	}
	f.waitNoForwards(t)
	if live, afterErr := net.Dial("unix", farPath); afterErr == nil {
		_ = live.Close()
		t.Fatalf("the far side still accepted a connection at %s after the listener was ended", farPath)
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

	// THE OTHER DIRECTION IS A CODE THE SHELLS OF THIS BUILD DO KNOW, and it is
	// accepted — the closed set is a gate and not a ban (the ssh route no longer
	// carries a far socket path at all, so there is nothing left for a reason to
	// contradict: nocx-e2bws).
	params = stand.spawnParams(t, proto.SSHModeAuto)
	params.AgentToolsAbsent = string(shellintegration.AgentToolsNoHelperOnHost)
	if _, err := stand.spawn(t, params); err != nil {
		t.Fatalf("a spawn naming a reason this helper knows was refused: %v", err)
	}
}
