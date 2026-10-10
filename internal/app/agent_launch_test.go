package app

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
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
	resolved, resolveErr := service.Resolve(context.Background(), binding)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if !resolved.Local || resolved.Ticket == "" || resolved.Payload == "" {
		t.Fatalf("local record did not resolve: %+v", resolved)
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
	next, nextErr := service.Resolve(context.Background(), binding)
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
	refused, err := service.Resolve(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if !refused.Local || refused.Ticket != "" || refused.Payload != "" || refused.Reason == "" {
		t.Fatalf("disabled local record was not refused: %+v", refused)
	}
	remote := local
	remote.Transport, remote.Lane, remote.Domain = "remote", "lane-remote", "dom-remote"
	remoteResult, err := service.Resolve(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if remoteResult.Local || remoteResult.Ticket != "" || remoteResult.Payload != "" {
		t.Fatalf("remote lookup returned local configuration: %+v", remoteResult)
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
	resolved, err := service.Resolve(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Local || resolved.Ticket != "" || resolved.Payload != "" {
		t.Fatalf("worker's literal command was resolved through the local record: %+v", resolved)
	}
}

// literalAgentLaunchResolver stands in for a server-classified worker pane in
// tests that build lifecycle publishers directly. Those harnesses test the
// worker tool's literal command contract rather than the app's Settings
// resolver, so their local shell receives no record ticket or payload.
type literalAgentLaunchResolver struct {
	transports *transportRegistry
}

func (r literalAgentLaunchResolver) Resolve(_ context.Context, binding lifecyclepub.AgentLaunchBinding) (lifecyclepub.AgentLaunchResolution, error) {
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
