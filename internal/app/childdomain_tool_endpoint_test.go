package app

// A nested domain's launch names ITS COORDINATOR's tool endpoint — nocx-1n56d.
//
// What was wrong. Both nested-domain launches (the sudo/su child and the ssh
// child) were composed with `AgentToolSocketPath: os.Getenv("NOCX_TOOL_SOCKET")`,
// read in the BACKEND's own process. Nothing sets that variable in a
// coordinator's environment — its only writers are pane launch scripts, one
// shell down — so the launch named no endpoint at all, and inside a backend
// somebody had started from a pane it named THAT pane's socket: a fact about
// whoever launched this process rather than about the child being composed.
//
// The fix is one derivation, read per grant (nestedToolSocket), answering from
// the composition root's own holder of the value — App.SetLocalToolSocketPath,
// whose value is localHelperOpener.toolEndpoint, which is exactly what every
// local pane this machine opens carries to its shell.
//
// These tests read the path the LAUNCH carries, never the accessor: the
// returned bootstrap IS the rcfile the parent stages into the sudo child's
// preserved descriptor, so the text under assertion is the text the child's
// own shell sources.
//
// The far half is deliberately absent, and must not be faked. A child on
// another machine — the ssh child, or a sudo/su inside an ssh pane — has no
// tool-socket carrier on this path at all today: the far-side listener that
// would answer it is the decision nocx-e2bws records as open, and a fixture
// that invented one would be evidence of the fixture rather than of the
// product. So what is asserted for a child that runs elsewhere is that its
// launch names NOTHING, which is the honest state and which the env read this
// replaced would already have violated.

import (
	"strings"
	"testing"

	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// nestedGrantHarness is one coordinator's child-domain grant path as the
// composition root wires it (app.go): the production builder, a real kernel
// and publisher, and a REAL parent domain on a real transport.
//
// The parent is ADOPTED rather than handshaken, and that is a statement about
// what this file is for: the handshake that establishes a parent has its own
// proofs (lifecyclechannel's, and ssh_child_assembly_test.go's live assembly),
// while what is under test here is the text the CHILD's launch carries.
type nestedGrantHarness struct {
	t       *testing.T
	builder lifecyclepub.GrantBuilder
	lane    lifecycle.LaneID
	parent  lifecycle.DomainID
}

func newNestedGrantHarness(t *testing.T, kind transportKind, endpoint func() string, localHelper func() string) *nestedGrantHarness {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	k := lifecycle.New(lifecycle.Options{})
	sessions := newSessionRegistry()
	transports := newTransportRegistry()
	lane := lifecycle.LaneID("lane-nested-tool-endpoint")
	sessions.register(lane, "aabbccddeeff00112233445566778899")

	var pub *lifecyclepub.Publisher
	// typed is nil: it is the SSH child's delivery seam, and these tests
	// compose the sudo/su child, which never reaches for it.
	builder := newChildGrantBuilder(logger,
		func() *lifecyclepub.Publisher { return pub }, transports, sessions, nil, endpoint, localHelper)
	pub = lifecyclepub.New(k, lifecyclepub.WithGrantBuilder(builder))

	parentLn, err := lifecyclechannel.NewListener(logger, pub)
	if err != nil {
		t.Fatalf("parent transport: %v", err)
	}
	t.Cleanup(func() { _ = parentLn.Close() })
	transports.register(parentLn.TransportID(), kind)

	var capability lifecycle.Capability
	capability[0] = 0x11
	var recovery lifecycle.FenceNonce
	recovery[0] = 0x22
	parent, err := pub.AdoptDomain(lane, "dom-nested-tool-parent", 1, capability, recovery, parentLn.TransportID())
	if err != nil {
		t.Fatalf("adopt the parent domain: %v", err)
	}
	return &nestedGrantHarness{t: t, builder: builder, lane: lane, parent: parent.Domain}
}

// grantSudo asks for the sudo child and answers the launch the parent shell
// would execute.
func (h *nestedGrantHarness) grantSudo() string {
	h.t.Helper()
	boot, err := h.builder(lifecyclepub.GrantRequest{
		Lane: h.lane, Parent: h.parent, RequestID: "r-nested-tool-endpoint", Env: lifecycle.EnvSudo,
	})
	if err != nil {
		h.t.Fatalf("the sudo child was refused: %v", err)
	}
	if boot.Domain == "" || boot.Bootstrap == "" {
		h.t.Fatalf("the grant came back with no child and no launch: %+v", boot)
	}
	if !strings.Contains(boot.Bootstrap, "NOCX_SHELL_INTEGRATION=1") {
		h.t.Fatalf("the grant is not an integrated child launch: %q", boot.Bootstrap)
	}
	return boot.Bootstrap
}

// TestNestedLocalChildLaunchNamesTheCoordinatorsToolEndpoint is criterion 1 at
// the composition root: a sudo child started inside a pane on THIS machine
// carries the path cmd/nocx-server published for this backend's tool endpoint,
// through the same accessor the local helper opener answers panes with.
//
// The assertion is on the ASSIGNMENT and not on the bare name, on purpose: the
// rcfile embeds the whole nocx.bash, which READS the variable
// (`__nocx_agent_tool_socket="${NOCX_TOOL_SOCKET:-}"`), so a substring check for
// the name alone would be satisfied by the script's own logic rather than by
// what this launch rendered (the same trap spawn_local_test.go records).
func TestNestedLocalChildLaunchNamesTheCoordinatorsToolEndpoint(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.localHelper == nil {
		t.Fatal("newTestApp built no local helper opener to derive the endpoint through")
	}
	const sock = "/run/user/1000/nocx/tool.sock"
	// What cmd/nocx-server does once it knows whether it is running a tool
	// endpoint at all — the production seam, not a value handed to the test.
	a.SetLocalToolSocketPath(sock)

	h := newNestedGrantHarness(t, transportKind{local: true}, a.localHelper.toolEndpoint, a.localHelper.installedHelperBinary)
	launch := h.grantSudo()

	want := shellintegration.ToolSocketEnvVar + "='" + sock + "'"
	if !strings.Contains(launch, want) {
		t.Fatalf("the nested child's launch does not name this coordinator's tool endpoint %s:\n%s", want, launch)
	}
}

// TestNestedChildOnAnotherMachineNamesNoToolEndpoint is the other half of
// criterion 1, and it is the one the defect is visible through. A child whose
// parent sits on a REMOTE transport (a sudo typed inside an ssh pane) runs on
// that host, where this backend's socket is not reachable — so its launch must
// name nothing, even though the variable the old code read is set to a path
// with a value in it. The env var is set to exactly what a backend started
// from inside a pane would have inherited, so an env read cannot pass this test
// by accident.
func TestNestedChildOnAnotherMachineNamesNoToolEndpoint(t *testing.T) {
	t.Setenv(shellintegration.ToolSocketEnvVar, "/run/user/1000/nocx/foreign-coordinator.sock")

	h := newNestedGrantHarness(t, transportKind{port: 41234}, func() string { return "/run/user/1000/nocx/tool.sock" },
		func() string { return "/home/u/.nocx/helper/installed/nocx-helper" })
	launch := h.grantSudo()

	if strings.Contains(launch, shellintegration.ToolSocketEnvVar+"=") {
		t.Fatalf("the launch of a child that runs on another machine exported a tool socket:\n%s", launch)
	}
}

// TestNestedLocalChildWithNoToolSurfaceNamesNoVariable is criterion 2: a
// backend that publishes no tool surface (cmd/nocx-server's startToolEndpoint
// answered nil, nil — no authorizer or dispatcher) leaves the opener naming
// nothing, and the child's launch then renders NO variable at all. Absent, not
// empty: an empty assignment is a shell told it has an endpoint and given a
// path of zero characters, while absence leaves the shell's own fallback and
// its refusal text intact.
func TestNestedLocalChildWithNoToolSurfaceNamesNoVariable(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.localHelper == nil {
		t.Fatal("newTestApp built no local helper opener to derive the endpoint through")
	}

	h := newNestedGrantHarness(t, transportKind{local: true}, a.localHelper.toolEndpoint, a.localHelper.installedHelperBinary)
	launch := h.grantSudo()

	if strings.Contains(launch, shellintegration.ToolSocketEnvVar+"=") {
		t.Fatalf("a backend with no tool surface still rendered a tool socket into the child's launch:\n%s", launch)
	}
	// The launch is a real one, so the assertion above cannot be satisfied by
	// an empty grant: grantSudo already refuses that, and this states the
	// variable the launch is expected to carry INSTEAD.
	if !strings.Contains(launch, "NOCX_SESSION_ID='aabbccddeeff00112233445566778899'") {
		t.Fatalf("the child's launch lost its session identity too:\n%s", launch)
	}
}

// TestNestedLocalChildNamesThisMachinesInstalledHelperBinary is the third
// criterion at the composition root, and it is the twin of the endpoint test
// above one field over (nocx-e2bws): a sudo child started inside a pane on
// THIS machine must carry the executable of the helper generation THIS backend
// installed, because that is the MCP adapter its agent will run.
//
// THE ENVIRONMENT IS THE MUTATION, and it is set rather than merely unset. The
// variable the builder used to read is given the path a backend started from
// inside a pane inherits — another generation's binary — so a launch that
// still read it would carry THAT value and fail here, while a launch that asks
// the opener carries the installed one. Restoring the env read turns this test
// red; that is what makes it evidence rather than a description.
func TestNestedLocalChildNamesThisMachinesInstalledHelperBinary(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.localHelper == nil {
		t.Fatal("newTestApp built no local helper opener to derive the binary through")
	}
	// The value Start's install step records, through the production setter it
	// records it with — not a field the test reaches into.
	const installed = "/home/u/.nocx/helper/12-linux-amd64-abcd/nocx-helper"
	a.localHelper.installedLocalGeneration(helperlocal.Installed{Binary: installed, Generation: "abcd"})

	const foreign = "/home/u/.nocx/helper/11-linux-amd64-ffff/nocx-helper"
	t.Setenv("NOCX_AGENT_HELPER_PATH", foreign)

	h := newNestedGrantHarness(t, transportKind{local: true}, a.localHelper.toolEndpoint, a.localHelper.installedHelperBinary)
	launch := h.grantSudo()

	if want := "NOCX_AGENT_HELPER_PATH='" + installed + "'"; !strings.Contains(launch, want) {
		t.Fatalf("the nested child's launch does not name this machine's installed helper %s:\n%s", want, launch)
	}
	if strings.Contains(launch, foreign) {
		t.Fatalf("the nested child's launch names the binary this process's environment carries (%s), which is a fact about whoever launched the backend rather than about the child:\n%s",
			foreign, launch)
	}
}

// TestNestedSSHChildNamesNoToolSurface is the other half of the same criterion,
// and it is the one the owner's decision of 2026-09-14 is visible through. A
// nested ssh child's shell runs on ANOTHER machine: nocx installs no helper
// there, the integration bundle carries no executable, and nothing else may put
// one there — so its launch names neither a bridge binary nor a socket, and the
// shell's own stage reports that absence to the person.
//
// Both environment variables are set to values with a path in them, so a
// launch that still read the environment — which is exactly what both fields
// did — would name the WRONG MACHINE's binary and socket here and fail.
func TestNestedSSHChildNamesNoToolSurface(t *testing.T) {
	t.Setenv("NOCX_AGENT_HELPER_PATH", "/home/u/.nocx/helper/12-linux-amd64-abcd/nocx-helper")
	t.Setenv(shellintegration.ToolSocketEnvVar, "/run/user/1000/nocx/tool.sock")

	opts := sshChildLaunchOptions("aabbccddeeff00112233445566778899", typedTestRequest(),
		lifecycle.DomainHandle{Domain: "dom-ssh-child", Epoch: 1})

	if opts.AgentHelperPath != "" {
		t.Fatalf("a nested ssh child's launch names %q as its bridge binary; that path is on THIS machine and the child runs on another",
			opts.AgentHelperPath)
	}
	if opts.AgentToolSocketPath != "" {
		t.Fatalf("a nested ssh child's launch names %q as its tool socket; this backend's endpoint is a path nothing on the far host reaches",
			opts.AgentToolSocketPath)
	}
	// The launch is a real one: the identity the far side needs is still
	// there, so an empty options value cannot be satisfied by an empty launch.
	if opts.SessionID == "" || opts.Lane != string(typedTestRequest().Lane) || opts.Capability == "" {
		t.Fatalf("the far launch lost its own identity: %+v", opts)
	}
}
