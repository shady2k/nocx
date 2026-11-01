package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/transport"
)

func testLaunchBinding(agent string) lifecyclepub.AgentLaunchBinding {
	return lifecyclepub.AgentLaunchBinding{Transport: "transport-1", Lane: "lane-1", Domain: "domain-1", Epoch: 7, Agent: agent}
}

func testLaunchSnapshot() agentLaunchSnapshot {
	return agentLaunchSnapshot{
		executable: agentapproval.Executable{Path: "/bin/agent", SHA256: strings.Repeat("a", 64)},
		argv:       []string{"/bin/agent", "--flag", "two words"},
		env:        []string{"EMPTY=", "KEY=value=with=equals"},
	}
}

func TestAgentLaunchTicketSurvivesPendingAndIsConsumedOnFinalResult(t *testing.T) {
	tickets := newAgentLaunchTickets()
	binding := testLaunchBinding("myagent")
	ticket, err := tickets.Issue(binding, testLaunchSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket) != 43 || strings.Contains(ticket, "=") {
		t.Fatalf("ticket shape is not 256-bit unpadded base64url: %q", ticket)
	}
	first, err := tickets.Begin(binding, ticket)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !equalStrings(first.argv, testLaunchSnapshot().argv) || !equalStrings(first.env, testLaunchSnapshot().env) {
		t.Fatalf("snapshot = %+v", first)
	}
	if !tickets.RetainPending(binding, ticket) {
		t.Fatal("pending approval consumed the ticket")
	}
	if _, err := tickets.Begin(binding, ticket); err != nil {
		t.Fatalf("retry with same ticket: %v", err)
	}
	if !tickets.Complete(binding, ticket) {
		t.Fatal("final answer did not consume the ticket")
	}
	if _, err := tickets.Begin(binding, ticket); err == nil {
		t.Fatal("consumed ticket was replayed")
	}
}

func TestAgentLaunchTicketSupersedesOnlyTheSameTransportLaneDomainEpoch(t *testing.T) {
	tickets := newAgentLaunchTickets()
	firstBinding := testLaunchBinding("first")
	first, firstErr := tickets.Issue(firstBinding, testLaunchSnapshot())
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	secondBinding := firstBinding
	secondBinding.Agent = "second"
	second, secondErr := tickets.Issue(secondBinding, testLaunchSnapshot())
	if secondErr != nil {
		t.Fatal(secondErr)
	}
	if _, err := tickets.Begin(firstBinding, first); err == nil {
		t.Fatal("new resolve did not supersede the earlier agent ticket for the binding")
	}
	if _, err := tickets.Begin(firstBinding, second); err == nil {
		t.Fatal("ticket was accepted for a different agent id")
	}
	if _, err := tickets.Begin(secondBinding, second); err != nil {
		t.Fatalf("current agent ticket was refused: %v", err)
	}

	other := firstBinding
	other.Domain = "domain-2"
	otherTicket, otherErr := tickets.Issue(other, testLaunchSnapshot())
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	if _, err := tickets.Begin(other, otherTicket); err != nil {
		t.Fatalf("other domain was affected by supersession: %v", err)
	}
}

func TestAgentLaunchTicketCancelAndInvalidationRequireMatchingBinding(t *testing.T) {
	tickets := newAgentLaunchTickets()
	binding := testLaunchBinding("myagent")
	ticket, issueErr := tickets.Issue(binding, testLaunchSnapshot())
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	wrong := binding
	wrong.Transport = "other-transport"
	if tickets.Cancel(wrong, ticket) {
		t.Fatal("wrong-transport cancel removed ticket")
	}
	wrongAgent := binding
	wrongAgent.Agent = "other-agent"
	if tickets.Cancel(wrongAgent, ticket) {
		t.Fatal("wrong-agent cancel removed ticket")
	}
	if _, err := tickets.Begin(binding, ticket); err != nil {
		t.Fatalf("wrong cancel removed ticket: %v", err)
	}
	if !tickets.Cancel(binding, ticket) {
		t.Fatal("matching ticket cancel did not remove ticket")
	}
	if _, err := tickets.Begin(binding, ticket); err == nil {
		t.Fatal("cancelled ticket was replayed")
	}

	domainTicket, domainErr := tickets.Issue(binding, testLaunchSnapshot())
	if domainErr != nil {
		t.Fatal(domainErr)
	}
	otherBinding := binding
	otherBinding.Lane = "lane-2"
	otherTicket, otherErr := tickets.Issue(otherBinding, testLaunchSnapshot())
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	tickets.InvalidateDomain(binding.Transport, binding.Lane, binding.Domain)
	if _, err := tickets.Begin(binding, domainTicket); err == nil {
		t.Fatal("domain invalidation retained ticket")
	}
	if _, err := tickets.Begin(otherBinding, otherTicket); err != nil {
		t.Fatalf("domain invalidation affected another lane: %v", err)
	}
	tickets.InvalidateTransport(binding.Transport)
	if _, err := tickets.Begin(otherBinding, otherTicket); err == nil {
		t.Fatal("transport loss retained ticket")
	}
}

func TestAgentLaunchTicketCapRefusesRatherThanEvicting(t *testing.T) {
	tickets := newAgentLaunchTickets()
	bindings := make([]lifecyclepub.AgentLaunchBinding, maxAgentLaunchTickets)
	tokens := make([]string, maxAgentLaunchTickets)
	var epoch uint64
	for i := range bindings {
		epoch++
		bindings[i] = lifecyclepub.AgentLaunchBinding{Transport: lifecycle.TransportID("transport"), Lane: lifecycle.LaneID("lane-" + strconv.Itoa(i+1)), Domain: lifecycle.DomainID("domain"), Epoch: epoch, Agent: "agent"}
		token, err := tickets.Issue(bindings[i], testLaunchSnapshot())
		if err != nil {
			t.Fatalf("issue ticket %d: %v", i, err)
		}
		tokens[i] = token
	}
	tooMany := lifecyclepub.AgentLaunchBinding{Transport: "transport", Lane: "overflow", Domain: "domain", Epoch: 1, Agent: "agent"}
	if _, err := tickets.Issue(tooMany, testLaunchSnapshot()); err == nil {
		t.Fatal("ticket cap evicted an existing live entry instead of refusing")
	}
	if _, err := tickets.Begin(bindings[0], tokens[0]); err != nil {
		t.Fatalf("cap evicted existing ticket: %v", err)
	}
	replacement := bindings[0]
	replacement.Agent = "new-agent"
	if _, err := tickets.Issue(replacement, testLaunchSnapshot()); err != nil {
		t.Fatalf("same binding could not supersede at cap: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAgentLaunchServiceReadsCurrentLocalRecordWithoutLeakingIt(t *testing.T) {
	records, storeErr := agentrecord.New(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	command, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	doc := agentrecord.Document{Command: command, Args: []string{"--record-arg", "two words"}, Env: []string{"NOCX_PRIVATE=private-value"}}
	if err := records.Save("myagent", doc); err != nil {
		t.Fatal(err)
	}
	transports := newTransportRegistry()
	transports.register("local-transport", transportKind{local: true})
	tickets := newAgentLaunchTickets()
	service := newAgentLaunchService(records, transports, tickets)
	binding := lifecyclepub.AgentLaunchBinding{Transport: "local-transport", Lane: "lane-1", Domain: "domain-1", Epoch: 1, Agent: "myagent"}
	resolved, resolveErr := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{})
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if !resolved.Local || resolved.Ticket == "" || resolved.Payload == "" {
		t.Fatalf("local record did not resolve: %+v", resolved)
	}
	if !resolved.ToolSurface {
		t.Fatalf("a local record launch on this machine resolves with no tool surface: %+v", resolved)
	}
	for _, private := range []string{"/bin/echo", "--record-arg", "two words", "private-value"} {
		if strings.Contains(resolved.Payload, private) {
			t.Errorf("payload exposes private record text %q", private)
		}
	}
	snapshot, beginErr := tickets.Begin(binding, resolved.Ticket)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if snapshot.executable.Path != command || !equalStrings(snapshot.argv, []string{command, "--record-arg", "two words"}) || !equalStrings(snapshot.env, doc.Env) {
		t.Fatalf("resolved snapshot = %+v", snapshot)
	}

	updated := agentrecord.Document{Command: command, Args: []string{"--edited"}, Env: []string{"NOCX_PRIVATE=new-value"}}
	if err := records.Save("myagent", updated); err != nil {
		t.Fatal(err)
	}
	next, nextErr := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{})
	if nextErr != nil {
		t.Fatal(nextErr)
	}
	if next.Ticket == resolved.Ticket {
		t.Fatal("new record resolution reused its old ticket")
	}
	if _, err := tickets.Begin(binding, resolved.Ticket); err == nil {
		t.Fatal("new resolution did not supersede the previous snapshot")
	}
	current, currentErr := tickets.Begin(binding, next.Ticket)
	if currentErr != nil {
		t.Fatal(currentErr)
	}
	if !equalStrings(current.argv, []string{command, "--edited"}) || !equalStrings(current.env, updated.Env) {
		t.Fatalf("new record was not used immediately: %+v", current)
	}
}

func TestAgentLaunchServiceReturnsNamesOnlyForRemoteAndRefusesDisabledLocal(t *testing.T) {
	records, storeErr := agentrecord.New(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	doc := agentrecord.Document{Command: "/bin/echo", Args: []string{"secret-arg"}, Env: []string{"SECRET=value"}, Disabled: true}
	if err := records.Save("myagent", doc); err != nil {
		t.Fatal(err)
	}
	transports := newTransportRegistry()
	transports.register("local", transportKind{local: true})
	transports.register("remote", transportKind{local: false})
	tickets := newAgentLaunchTickets()
	service := newAgentLaunchService(records, transports, tickets)
	local := lifecyclepub.AgentLaunchBinding{Transport: "local", Lane: "lane-local", Domain: "dom-local", Epoch: 1, Agent: "myagent"}
	refused, err := service.Resolve(context.Background(), local, lifecyclepub.AgentLaunchContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !refused.Local || refused.Ticket != "" || refused.Payload != "" || refused.Reason == "" {
		t.Fatalf("disabled local record was not refused: %+v", refused)
	}
	remote := local
	remote.Transport, remote.Lane, remote.Domain = "remote", "lane-remote", "dom-remote"
	remoteResult, err := service.Resolve(context.Background(), remote, lifecyclepub.AgentLaunchContext{})
	if err != nil {
		t.Fatal(err)
	}
	if remoteResult.Local || remoteResult.Ticket != "" || remoteResult.Payload != "" {
		t.Fatalf("remote lookup returned local configuration: %+v", remoteResult)
	}
	if remoteResult.ToolSurface {
		t.Fatalf("a launch on somebody else's host was promised this machine's tool surface: %+v", remoteResult)
	}
	if len(tickets.byToken) != 0 {
		t.Fatalf("refusal or remote lookup created tickets: %d", len(tickets.byToken))
	}
}

func TestAgentLaunchServiceDoesNotResolveWorkerSpawnCommandAsRecord(t *testing.T) {
	records, storeErr := agentrecord.New(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	command, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	if err := records.Save("myagent", agentrecord.Document{Command: command, Args: []string{"record-only"}, Env: []string{"SECRET=record-only"}}); err != nil {
		t.Fatal(err)
	}
	transports := newTransportRegistry()
	transports.register("local-transport", transportKind{local: true})
	service := newAgentLaunchService(records, transports, newAgentLaunchTickets())
	service.workerLane = func(lane lifecycle.LaneID) bool { return lane == "worker-lane" }
	binding := lifecyclepub.AgentLaunchBinding{Transport: "local-transport", Lane: "worker-lane", Domain: "domain-1", Epoch: 2, Agent: "myagent"}
	resolved, err := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Local || resolved.Ticket != "" || resolved.Payload != "" {
		t.Fatalf("worker's literal command was resolved through the local record: %+v", resolved)
	}
	// AND IT STILL GETS NOCX'S TOOLS (nocx-xn63t.6.49). The literal command
	// and the tool surface are different answers: the record must not reach a
	// worker, and the surface must, because the worker's pane is this
	// machine's and its report is how its coordinator hears anything at all.
	if !resolved.ToolSurface {
		t.Fatalf("a worker's launch resolved with no tool surface: %+v", resolved)
	}
}

// A WORKER LANE IS NOT A REMOTE ONE, and the difference is the whole defect:
// on a remote transport there is no surface of ours to point at, so the same
// literal command runs with none. That answer has to stay false, or a
// published generation would be handed a per-agent argument for a helper that
// is not there.
func TestAgentLaunchServicePromisesNoToolSurfaceOnARemoteTransport(t *testing.T) {
	transports := newTransportRegistry()
	transports.register("remote-transport", transportKind{local: false})
	service := newAgentLaunchService(nil, transports, newAgentLaunchTickets())
	binding := lifecyclepub.AgentLaunchBinding{Transport: "remote-transport", Lane: "worker-lane", Domain: "domain-1", Epoch: 2, Agent: "myagent"}
	resolved, err := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Local || resolved.ToolSurface || resolved.Ticket != "" || resolved.Payload != "" {
		t.Fatalf("a remote launch was answered with local configuration or a local surface: %+v", resolved)
	}
}

// literalAgentLaunchResolver stands in for a server-classified worker pane in
// tests that build lifecycle publishers directly. Those harnesses test the
// worker tool's literal command contract rather than the app's Settings
// resolver, so their local shell receives no record ticket or payload.
type literalAgentLaunchResolver struct {
	transports *transportRegistry
}

func (r literalAgentLaunchResolver) Resolve(_ context.Context, binding lifecyclepub.AgentLaunchBinding, _ lifecyclepub.AgentLaunchContext) (lifecyclepub.AgentLaunchResolution, error) {
	if r.transports != nil {
		r.transports.register(binding.Transport, transportKind{local: true})
	}
	return lifecyclepub.AgentLaunchResolution{Local: false}, nil
}
func (literalAgentLaunchResolver) Cancel(lifecyclepub.AgentLaunchBinding, string)    {}
func (literalAgentLaunchResolver) InvalidateBinding(lifecyclepub.AgentLaunchBinding) {}
func (literalAgentLaunchResolver) InvalidateDomain(lifecycle.TransportID, lifecycle.LaneID, lifecycle.DomainID) {
}
func (literalAgentLaunchResolver) InvalidateTransport(lifecycle.TransportID) {}

// A login shell can find an agent even when the application environment cannot.
func TestAgentLaunchServiceUsesPanePathAndApprovesResolvedExecutable(t *testing.T) {
	physicalDir := t.TempDir()
	paneDir := filepath.Join(t.TempDir(), "agent-bin-link")
	if err := os.Symlink(physicalDir, paneDir); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(paneDir, "pane-only-agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	shell := exec.Command("/bin/sh", "-c", "command -v pane-only-agent")
	shell.Env = []string{"PATH=" + paneDir}
	found, err := shell.Output()
	if err != nil || strings.TrimSpace(string(found)) != command {
		t.Fatalf("pane cannot find fixture: %q, %v", found, err)
	}
	t.Setenv("PATH", t.TempDir())
	records, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := records.Save("myagent", agentrecord.Document{Command: "pane-only-agent"}); saveErr != nil {
		t.Fatal(saveErr)
	}
	transports := newTransportRegistry()
	transports.register("local-transport", transportKind{local: true})
	tickets := newAgentLaunchTickets()
	service := newAgentLaunchService(records, transports, tickets)
	binding := lifecyclepub.AgentLaunchBinding{Transport: "local-transport", Lane: "lane-1", Domain: "domain-1", Epoch: 1, Agent: "myagent"}
	resolved, err := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{Path: &paneDir, Cwd: paneDir})
	if err != nil || resolved.Ticket == "" {
		t.Fatalf("pane found the executable but nocx refused it: %+v, %v", resolved, err)
	}
	snapshot, err := tickets.Begin(binding, resolved.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := agentapproval.IdentityForPath(command)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.executable != identity || snapshot.argv[0] != identity.Path {
		t.Fatalf("approval and launch must name the pane's absolute executable: %+v, want %+v", snapshot, identity)
	}
	requester := &consentGateRequester{entered: make(chan transport.HostAsk, 1), answer: make(chan bool, 1)}
	approval := approvalServiceForTest(t, requester)
	pending := pendingOf(t, approval.ApproveResolved(context.Background(), "pane-custom", "myagent", snapshot.executable))
	ask := <-requester.entered
	if ask.Executable != identity.Path || ask.Digest != identity.SHA256 {
		t.Fatalf("approval asked about %q/%q, want %+v", ask.Executable, ask.Digest, identity)
	}
	requester.answer <- true
	if reason := settledOf(t, pending); reason != "" {
		t.Fatalf("resolved executable approval failed: %s", reason)
	}
	if answer, ok := approval.store.Lookup(identity, agentapproval.Domain{Kind: agentapproval.DomainLocal}, approval.scope); !ok || answer != agentapproval.Granted {
		t.Fatal("approval was not stored against the resolved absolute path and digest")
	}

	// A matching name on the GUI PATH must not rescue a failed pane lookup.
	t.Setenv("PATH", paneDir)
	missing := t.TempDir()
	refused, err := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{Path: &missing, Cwd: missing})
	if err != nil || refused.Ticket != "" || refused.Payload != "" || !strings.Contains(refused.Reason, "not on the pane's PATH") {
		t.Fatalf("failed pane lookup fell back to GUI PATH or hid its reason: %+v, %v", refused, err)
	}
	// An older shell omits context; preserve its existing resolution behavior.
	legacy, err := service.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{})
	if err != nil || legacy.Ticket == "" {
		t.Fatalf("older shell resolution changed: %+v, %v", legacy, err)
	}
}

// A restored configured worker is launched through a newly integrated pane
// shell. It carries that shell's PATH/cwd, and must reuse prior consent even
// when an absolute PATH directory has a symlink spelling.
func TestPaneAgentLaunchReusesApprovalIssuedBeforeRestart(t *testing.T) {
	physicalDir := t.TempDir()
	paneDir := filepath.Join(t.TempDir(), "agent-bin-link")
	if err := os.Symlink(physicalDir, paneDir); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(paneDir, "restart-agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", paneDir)
	requester := &consentGateRequester{entered: make(chan transport.HostAsk, 1), answer: make(chan bool, 1)}
	docs := &approvalDocStore{}
	first := approvalServiceOver(t, docs, requester)
	pending := pendingOf(t, first.Approve(context.Background(), "pane-custom", "restart-agent"))
	ask := <-requester.entered
	requester.answer <- true
	if reason := settledOf(t, pending); reason != "" {
		t.Fatal(reason)
	}

	records, err := agentrecord.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := records.Save("myagent", agentrecord.Document{Command: "restart-agent"}); saveErr != nil {
		t.Fatal(saveErr)
	}
	transports := newTransportRegistry()
	transports.register("transport-1", transportKind{local: true})
	tickets := newAgentLaunchTickets()
	resolver := newAgentLaunchService(records, transports, tickets)
	binding := testLaunchBinding("myagent")
	resolved, err := resolver.Resolve(context.Background(), binding, lifecyclepub.AgentLaunchContext{Path: &paneDir, Cwd: paneDir})
	if err != nil || resolved.Ticket == "" {
		t.Fatalf("resolve restored agent: %+v, %v", resolved, err)
	}
	snapshot, err := tickets.Begin(binding, resolved.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.executable.Path != ask.Executable || snapshot.executable.SHA256 != ask.Digest || snapshot.argv[0] != ask.Executable {
		t.Fatalf("restore changed the approved executable spelling: %+v, prior %q/%q", snapshot, ask.Executable, ask.Digest)
	}
	// Rehydrate the approval service over the same durable document. No new
	// requester exists: a changed key is a refusal, not an unanswered dialog.
	restarted := approvalServiceOver(t, docs, nil)
	if approveErr := restarted.ApproveResolved(context.Background(), "pane-custom", "myagent", snapshot.executable); approveErr != nil {
		t.Fatalf("restore did not reuse prior consent: %v", approveErr)
	}
}

// THE RECORD'S NON-LAUNCH WORDS REACH THE SHELL (nocx-3an1s.1.3, GitHub #541).
//
// The shell holds the typed argv and this side holds the record, so the split
// is one list in the resolve answer rather than a second copy of "update" in
// three scripts. What this test pins is the wiring no shell test can reach:
// the shipped record's words appearing in the answer a local pane receives,
// and nothing invented for a launch that has no record of its own.
func TestAgentLaunchResolveCarriesTheRecordsNonLaunchSubcommands(t *testing.T) {
	dir := t.TempDir()
	records, err := agentrecord.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A record the person wrote, pointing at a program that really exists in
	// the pane's PATH: resolve is the function that identifies it.
	bin := t.TempDir()
	command := filepath.Join(bin, "claude")
	if writeErr := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o755); writeErr != nil { //nolint:gosec // executable fixture inside t.TempDir
		t.Fatal(writeErr)
	}
	if saveErr := records.Save("claude", agentrecord.Document{
		Command:              command,
		NonLaunchSubcommands: []string{"update", "update-config"},
	}); saveErr != nil {
		t.Fatal(saveErr)
	}
	transports := newTransportRegistry()
	transports.register(testLaunchBinding("claude").Transport, transportKind{local: true})
	service := newAgentLaunchService(records, transports, newAgentLaunchTickets())

	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	resolution, err := service.Resolve(
		context.Background(),
		testLaunchBinding("claude"),
		lifecyclepub.AgentLaunchContext{Path: &path, Cwd: dir},
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolution.Local || !resolution.ToolSurface {
		t.Fatalf("resolution = %+v, want a local launch with a surface", resolution)
	}
	if resolution.NonLaunchSubcommands != "update update-config" {
		t.Fatalf("non-launch subcommands = %q, want the record's list space-joined", resolution.NonLaunchSubcommands)
	}

	// A record that marks nothing answers with nothing: the field is the
	// record's, never a default the backend invented.
	if saveErr := records.Save("quiet", agentrecord.Document{Command: command}); saveErr != nil {
		t.Fatal(saveErr)
	}
	quiet, err := service.Resolve(
		context.Background(),
		testLaunchBinding("quiet"),
		lifecyclepub.AgentLaunchContext{Path: &path, Cwd: dir},
	)
	if err != nil {
		t.Fatalf("resolve quiet: %v", err)
	}
	if quiet.NonLaunchSubcommands != "" {
		t.Fatalf("a record that marks nothing answered %q", quiet.NonLaunchSubcommands)
	}
}
