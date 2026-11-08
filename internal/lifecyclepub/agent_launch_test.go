package lifecyclepub_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	nocxlog "github.com/shady2k/nocx/internal/log"
)

type fakeLaunchResolver struct {
	result       lifecyclepub.AgentLaunchResolution
	err          error
	bindings     []lifecyclepub.AgentLaunchBinding
	contexts     []lifecyclepub.AgentLaunchContext
	cancelled    []string
	cancelBounds []lifecyclepub.AgentLaunchBinding
}

func (r *fakeLaunchResolver) Resolve(_ context.Context, binding lifecyclepub.AgentLaunchBinding, pane lifecyclepub.AgentLaunchContext) (lifecyclepub.AgentLaunchResolution, error) {
	r.bindings = append(r.bindings, binding)
	r.contexts = append(r.contexts, pane)
	return r.result, r.err
}

func (r *fakeLaunchResolver) Cancel(binding lifecyclepub.AgentLaunchBinding, ticket string) {
	r.cancelled = append(r.cancelled, ticket)
	r.cancelBounds = append(r.cancelBounds, binding)
}
func (*fakeLaunchResolver) InvalidateBinding(lifecyclepub.AgentLaunchBinding) {}
func (*fakeLaunchResolver) InvalidateDomain(lifecycle.TransportID, lifecycle.LaneID, lifecycle.DomainID) {
}
func (*fakeLaunchResolver) InvalidateTransport(lifecycle.TransportID) {}

func establishedLaunchPublisher(t *testing.T, resolver lifecyclepub.AgentLaunchResolver, opts ...lifecyclepub.Option) (*lifecyclepub.Publisher, *recordingPort, lifecycle.DomainHandle) {
	t.Helper()
	k := lifecycle.New(lifecycle.Options{})
	options := append([]lifecyclepub.Option{lifecyclepub.WithAgentLaunchResolver(resolver)}, opts...)
	pub := lifecyclepub.New(k, options...)
	pub.SetEmitter(&recorder{})
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	return pub, port, h
}

func launchResolveEvent(rid lifecycle.RequestID, agent string) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindAgentLaunchResolve, AgentLaunchResolve: &lifecycle.AgentLaunchResolve{RequestID: rid, Agent: agent}}
}

func TestLocalLaunchResolutionUsesAuthenticatedBindingAndReturnsData(t *testing.T) {
	resolver := &fakeLaunchResolver{result: lifecyclepub.AgentLaunchResolution{Local: true, ToolSurface: true, Ticket: "0123456789abcdef0123456789abcdef0123456789A", Payload: "v1\n1\n2f62696e2f6167656e74\n0\n"}}
	pub, port, handle := establishedLaunchPublisher(t, resolver)
	request := launchResolveEvent("r-resolve-0", "myagent")
	panePath := "/pane/bin:/usr/bin"
	request.AgentLaunchResolve.PanePath = &panePath
	request.AgentLaunchResolve.PaneCwd = "/pane/work"
	mustIngest(t, pub, "T", env("L", handle, 2, request))

	answer := answerFrom(t, port, lifecycle.KindAgentLaunchResolved).Event.AgentLaunchResolved
	if answer == nil || !answer.Local || answer.Agent != "myagent" || answer.RequestID != "r-resolve-0" {
		t.Fatalf("launch response = %+v", answer)
	}
	if !answer.ToolSurface {
		t.Fatalf("a record launch on this machine lost its tool surface: %+v", answer)
	}
	if answer.Ticket != resolver.result.Ticket || answer.Payload != resolver.result.Payload {
		t.Fatalf("response omitted resolved ticket/payload: %+v", answer)
	}
	want := lifecyclepub.AgentLaunchBinding{Transport: "T", Lane: "L", Domain: handle.Domain, Epoch: handle.Epoch, Agent: "myagent"}
	if len(resolver.bindings) != 1 || resolver.bindings[0] != want {
		t.Fatalf("resolver binding = %+v, want authenticated binding %+v", resolver.bindings, want)
	}
	if len(resolver.contexts) != 1 || resolver.contexts[0].Path == nil || *resolver.contexts[0].Path != panePath || resolver.contexts[0].Cwd != "/pane/work" {
		t.Fatalf("pane resolution context was lost: %+v", resolver.contexts)
	}
}

func TestLaunchResolutionBoundsTheWholeEncodedResponseAndDiscardsTicket(t *testing.T) {
	resolver := &fakeLaunchResolver{result: lifecyclepub.AgentLaunchResolution{Local: true, Ticket: "0123456789abcdef0123456789abcdef0123456789A", Payload: strings.Repeat("x", lifecycle.MaxFrameBytes)}}
	pub, port, handle := establishedLaunchPublisher(t, resolver)
	mustIngest(t, pub, "T", env("L", handle, 2, launchResolveEvent("r-resolve-1", "myagent")))
	answer := answerFrom(t, port, lifecycle.KindAgentLaunchResolved).Event.AgentLaunchResolved
	if answer == nil || !answer.Local || answer.Ticket != "" || answer.Payload != "" || answer.Reason == "" {
		t.Fatalf("oversized response did not fail closed: %+v", answer)
	}
	if len(resolver.cancelled) != 1 || resolver.cancelled[0] != resolver.result.Ticket {
		t.Fatalf("oversized response left ticket live: cancelled=%v", resolver.cancelled)
	}
}

// A WORKER-LITERAL ANSWER CARRIES THE SURFACE AND NOTHING ELSE
// (nocx-xn63t.6.49). The classification is two independent facts and the
// publisher must not fold them into one: a launch with no record still runs on
// this machine, so the shell has to hear that a tool surface exists. The
// ticket and payload are the record's, and there is no record here — the
// assertion that they are empty is what keeps a worker command literal.
func TestWorkerLiteralLaunchResolutionKeepsTheToolSurfaceAndNoRecord(t *testing.T) {
	resolver := &fakeLaunchResolver{result: lifecyclepub.AgentLaunchResolution{Local: false, ToolSurface: true}}
	pub, port, handle := establishedLaunchPublisher(t, resolver)
	mustIngest(t, pub, "T", env("L", handle, 2, launchResolveEvent("r-resolve-3", "claude")))

	answer := answerFrom(t, port, lifecycle.KindAgentLaunchResolved).Event.AgentLaunchResolved
	if answer == nil {
		t.Fatal("no launch answer reached the shell")
	}
	if answer.Local || answer.Ticket != "" || answer.Payload != "" {
		t.Fatalf("a worker-literal answer carried record configuration: %+v", answer)
	}
	if !answer.ToolSurface {
		t.Fatalf("a worker-literal answer dropped the tool surface, which is what left the worker with no tools: %+v", answer)
	}
}

func TestRemoteLaunchResolutionCannotReturnLocalConfiguration(t *testing.T) {
	resolver := &fakeLaunchResolver{result: lifecyclepub.AgentLaunchResolution{Local: false, Ticket: "0123456789abcdef0123456789abcdef0123456789A", Payload: "secret"}}
	pub, port, handle := establishedLaunchPublisher(t, resolver)
	mustIngest(t, pub, "T", env("L", handle, 2, launchResolveEvent("r-resolve-2", "myagent")))
	answer := answerFrom(t, port, lifecycle.KindAgentLaunchResolved).Event.AgentLaunchResolved
	if answer == nil || answer.Local || answer.Ticket != "" || answer.Payload != "" {
		t.Fatalf("remote response carried local configuration: %+v", answer)
	}
}

// A REFUSAL MUST CARRY ITS CAUSE, AND THE LOG MUST HOLD IT (nocx-8gg8y).
//
// A person typed their agent's name in an ssh pane and read "the local agent
// record could not be resolved" — one sentence the publisher took in place of
// the resolver's error. Three causes share that branch (no owner for records,
// a frame whose context had already ended, a transport nobody registered) and
// the backend log held no line about any of them, so the field could not name
// which one it was. The refusal now carries the resolver's own words into the
// pane, and the same error goes into the log with the frame's transport, lane,
// domain and agent beside it.
func TestALaunchRefusalCarriesAndLogsTheResolversOwnError(t *testing.T) {
	const cause = "agent transport is not registered"
	var buf syncBuffer
	resolver := &fakeLaunchResolver{err: errors.New(cause)}
	pub, port, handle := establishedLaunchPublisher(t, resolver, lifecyclepub.WithLogger(
		nocxlog.NewSlogAdapter(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))))
	mustIngest(t, pub, "T", env("L", handle, 2, launchResolveEvent("r-resolve-refused", "claude")))

	answer := answerFrom(t, port, lifecycle.KindAgentLaunchResolved).Event.AgentLaunchResolved
	if answer == nil {
		t.Fatal("a refused launch reached no answer")
	}
	if !answer.Local || answer.ToolSurface {
		t.Fatalf("a refused launch did not fail closed: %+v", answer)
	}
	if !strings.Contains(answer.Reason, cause) {
		t.Fatalf("the refusal dropped the resolver's own error: %q", answer.Reason)
	}
	out := buf.String()
	for _, want := range []string{cause, "transport=T", "lane=L", "domain=" + string(handle.Domain), "agent=claude"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the refused launch was not logged with %q:\n%s", want, out)
		}
	}
}

type fakeResolvedEnroller struct {
	binding lifecyclepub.AgentLaunchBinding
	ticket  string
	cols    int
	rows    int
	calls   int
}

func (*fakeResolvedEnroller) Enrol(lifecycle.LaneID, string, int, int) error { return nil }
func (*fakeResolvedEnroller) Withdraw(lifecycle.LaneID)                      {}
func (e *fakeResolvedEnroller) EnrolResolved(_ context.Context, binding lifecyclepub.AgentLaunchBinding, ticket string, cols, rows int) error {
	e.binding, e.ticket, e.cols, e.rows = binding, ticket, cols, rows
	e.calls++
	return nil
}

func TestAgentEnrolPassesResolvedTicketToTheBoundEnroller(t *testing.T) {
	enroller := &fakeResolvedEnroller{}
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, lifecyclepub.WithAgentEnroller(enroller))
	pub.SetEmitter(&recorder{})
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	evt := lifecycle.Event{Kind: lifecycle.KindAgentEnrol, AgentEnrol: &lifecycle.AgentEnrol{RequestID: "r-enrol-0", Agent: "myagent", LaunchTicket: "0123456789abcdef0123456789abcdef0123456789A", Cols: 91, Rows: 37}}
	mustIngest(t, pub, "T", env("L", h, 2, evt))
	answer := answerFrom(t, port, lifecycle.KindAgentEnrolled).Event.AgentEnrolled
	if answer == nil || !answer.Enrolled {
		t.Fatalf("enrolment answer = %+v", answer)
	}
	want := lifecyclepub.AgentLaunchBinding{Transport: "T", Lane: "L", Domain: h.Domain, Epoch: h.Epoch, Agent: "myagent"}
	if enroller.calls != 1 || enroller.binding != want || enroller.ticket != evt.AgentEnrol.LaunchTicket || enroller.cols != 91 || enroller.rows != 37 {
		t.Fatalf("resolved enrolment = %+v, want binding=%+v ticket/geometry from request", enroller, want)
	}
}

func TestTicketedEnrolmentFailsClosedWithoutResolvedEnroller(t *testing.T) {
	enroller := &fakeEnroller{}
	pub, port, h := establishedPub(t, enroller)
	evt := lifecycle.Event{Kind: lifecycle.KindAgentEnrol, AgentEnrol: &lifecycle.AgentEnrol{RequestID: "r-enrol-1", Agent: "myagent", LaunchTicket: "0123456789abcdef0123456789abcdef0123456789A", Cols: 80, Rows: 24}}
	mustIngest(t, pub, "T", env("L", h, 2, evt))
	answer := answerFrom(t, port, lifecycle.KindAgentEnrolled).Event.AgentEnrolled
	if answer == nil || answer.Enrolled || answer.Reason == "" {
		t.Fatalf("ticketed enrolment was not refused: %+v", answer)
	}
	if len(enroller.enrolled) != 0 {
		t.Fatalf("ticket bypassed the ticket-aware seam: %v", enroller.enrolled)
	}
}

func TestAgentLaunchCancelUsesFullAuthenticatedBinding(t *testing.T) {
	resolver := &fakeLaunchResolver{result: lifecyclepub.AgentLaunchResolution{Local: true, Ticket: "0123456789abcdef0123456789abcdef0123456789A", Payload: "v1\n1\n2f62696e2f6167656e74\n0\n"}}
	pub, _, handle := establishedLaunchPublisher(t, resolver)
	mustIngest(t, pub, "T", env("L", handle, 2, launchResolveEvent("r-resolve-cancel", "myagent")))
	cancel := lifecycle.Event{Kind: lifecycle.KindAgentLaunchCancel, AgentLaunchCancel: &lifecycle.AgentLaunchCancel{Agent: "myagent", Ticket: resolver.result.Ticket}}
	mustIngest(t, pub, "T", env("L", handle, 3, cancel))
	want := lifecyclepub.AgentLaunchBinding{Transport: "T", Lane: "L", Domain: handle.Domain, Epoch: handle.Epoch, Agent: "myagent"}
	if len(resolver.cancelBounds) != 1 || resolver.cancelBounds[0] != want || resolver.cancelled[0] != resolver.result.Ticket {
		t.Fatalf("cancel binding/ticket = %+v/%v, want %+v/%s", resolver.cancelBounds, resolver.cancelled, want, resolver.result.Ticket)
	}
}
