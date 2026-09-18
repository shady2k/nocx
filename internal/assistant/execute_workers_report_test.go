package assistant

// workers.report at the tool (nocx-luqz9.4; design §4.2, §5.1).
//
// What is asserted here is what a WORKER can do and what it cannot: the run's
// own participant is the only sender and the record derives the recipient, the
// call returns rather than holding an answer open, and a run that is not a
// worker is refused at the constructor rather than in an executor.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/workers"
)

// reportParams is what the calls below send, so the wire shape under test is
// one place rather than a literal repeated per case.
func reportParams(t *testing.T, body string) json.RawMessage {
	t.Helper()
	return json.RawMessage(body)
}

// THE CRITERION, at the tool: a worker's `done` reaches the record with its
// kind, its own id as the sender, and its text intact — and the record is asked
// about the run's OWN participant, never about one the call could name.
func TestWorkerReportSendsTheRunsOwnParticipantAndItsKind(t *testing.T) {
	rec := &fakeWorkerRecord{}
	out, err := executeWorkerReport(context.Background(),
		agenttools.NewWorkerParticipant("worker-1"),
		reportParams(t, `{"kind":"done","text":"the migration landed"}`),
		workerSeams(rec))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rec.reported) != 1 {
		t.Fatalf("the record was asked to report %d times, want 1", len(rec.reported))
	}
	got := rec.reported[0]
	if got.id != "worker-1" {
		t.Fatalf("the report was made as %q, want the run's own participant", got.id)
	}
	if got.rep.Kind != workers.KindDone {
		t.Fatalf("kind = %q, want %q", got.rep.Kind, workers.KindDone)
	}
	if got.rep.Text != "the migration landed" {
		t.Fatalf("text = %q, want it intact", got.rep.Text)
	}
	// And the answer is the committed row, with no verdict in it: nocx records
	// no outcome, so a result carrying one would be a claim nothing witnessed.
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	for _, forbidden := range []string{"ok", "success", "failed", "outcome", "state"} {
		if _, present := result[forbidden]; present {
			t.Fatalf("the result carries a verdict field %q: %s", forbidden, out)
		}
	}
	if result["seq"] == nil || result["id"] == nil {
		t.Fatalf("the result does not name the committed row: %s", out)
	}
}

// The three kinds all pass through, and a checkpoint's two optional extras go
// with them — a field the tool accepts and the record never receives would be a
// soft degrade visible nowhere.
func TestWorkerReportPassesTheCheckpointsOwnExtras(t *testing.T) {
	rec := &fakeWorkerRecord{}
	for _, tc := range []struct {
		body string
		kind workers.MessageKind
	}{
		{`{"kind":"question","text":"which branch?"}`, workers.KindQuestion},
		{`{"kind":"progress","text":"half way","estimate":40,"artifact":"commit 4f2a1c9"}`, workers.KindProgress},
		{`{"kind":"progress","text":"no number yet"}`, workers.KindProgress},
	} {
		if _, err := executeWorkerReport(context.Background(),
			agenttools.NewWorkerParticipant("worker-1"), reportParams(t, tc.body), workerSeams(rec)); err != nil {
			t.Fatalf("report %s: %v", tc.body, err)
		}
	}
	if len(rec.reported) != 3 {
		t.Fatalf("the record was asked %d times, want 3", len(rec.reported))
	}
	if got := rec.reported[0].rep.Kind; got != workers.KindQuestion {
		t.Fatalf("second report kind = %q", got)
	}
	checkpoint := rec.reported[1].rep
	if checkpoint.Kind != workers.KindProgress {
		t.Fatalf("third report kind = %q", checkpoint.Kind)
	}
	if checkpoint.Estimate == nil || *checkpoint.Estimate != 40 {
		t.Fatalf("a checkpoint's estimate = %v, want 40", checkpoint.Estimate)
	}
	if checkpoint.Artifact != "commit 4f2a1c9" {
		t.Fatalf("a checkpoint's artifact = %q", checkpoint.Artifact)
	}
	if rec.reported[2].rep.Estimate != nil {
		t.Fatalf("a checkpoint that gave no number arrived with one: %v", *rec.reported[2].rep.Estimate)
	}
}

// A QUESTION DOES NOT WAIT (ADR-0070's "why not a blocking ask"): the call
// answers with the committed row rather than holding anything open, and a
// second question is sent rather than queued behind the first.
//
// "Holds nothing" is asserted as an ordering rather than with a clock: the
// record's second Report is reached before anything has answered the first,
// which is impossible for a call that waits.
func TestWorkerReportDoesNotWaitForAnAnswer(t *testing.T) {
	rec := &fakeWorkerRecord{}
	capability := agenttools.NewWorkerParticipant("worker-1")
	first, err := executeWorkerReport(context.Background(), capability,
		reportParams(t, `{"kind":"question","text":"which branch?"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("first question: %v", err)
	}
	second, err := executeWorkerReport(context.Background(), capability,
		reportParams(t, `{"kind":"question","text":"and the old one?"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("second question: %v", err)
	}
	var one, two struct {
		Seq int64 `json:"seq"`
	}
	if err := json.Unmarshal([]byte(first), &one); err != nil {
		t.Fatalf("first result: %v", err)
	}
	if err := json.Unmarshal([]byte(second), &two); err != nil {
		t.Fatalf("second result: %v", err)
	}
	if two.Seq <= one.Seq {
		t.Fatalf("the second question answered at %d, want it past the first's %d — "+
			"the first call was still holding something", two.Seq, one.Seq)
	}
	if len(rec.reported) != 2 {
		t.Fatalf("the record saw %d reports, want both questions", len(rec.reported))
	}
}

// NOT A WORKER, NOT A HOLDER OF THIS CALL. A coordinator's run context carries
// no participant identity, so the narrow refuses — before any executor runs,
// which is what makes this a property of the constructor rather than a check
// somebody has to remember.
//
// It is driven through the DISPATCHER rather than by calling the constructor,
// because the dispatcher is where the refusal is produced in production and the
// wrap it puts around the constructor's error is exactly what the endpoint's
// classifier has to see through.
//
// The refusal is the EXPORTED sentinel, because the endpoint turns it into a
// sentence an agent can act on (rpcErrorFor): an unexported one would be
// classified as an unclassified fault and told to stop.
func TestWorkerReportRefusesARunThatIsNotAWorker(t *testing.T) {
	dispatcher := toolDispatcherForTest(t, &fakeWorkerRecord{})
	// A COORDINATOR'S OWN INVOCATION, built the way the authorizer builds one
	// (internal/app's Admit): the session, the workspace, and NO participant —
	// which is the single fact this call turns on. The workspace is supplied
	// because it is what the declaration resolves its resource from; leaving it
	// out would fail one step earlier with a different error and this case would
	// then be asserting the wrong refusal.
	const workspace = "workspace-1"
	invocation := ToolInvocation{
		Context: context.Background(),
		Method:  "workers.report",
		RunContext: agenttools.RunContext{
			RunID:     "run-1",
			Session:   "sess-coordinator",
			Workspace: workspace,
		},
		Grant:     wholeRegistryGrantForTest(),
		RawParams: json.RawMessage(`{"kind":"done","text":"not mine to send"}`),
	}
	_, err := dispatcher.Dispatch(invocation)
	if !errors.Is(err, agenttools.ErrNoParticipant) {
		t.Fatalf("dispatch err = %v, want ErrNoParticipant", err)
	}

	// And the capability path: a coordinator's object is not this call's
	// holder, so a declaration mispaired with this executor is refused rather
	// than allowed to write a row addressed to nobody.
	if _, err := executeWorkerReport(context.Background(),
		agenttools.NewWorkerCoordinator("sess-coordinator", session.Identity{}, nil),
		reportParams(t, `{"kind":"done","text":"not mine to send"}`), workerSeams(&fakeWorkerRecord{}),
	); err == nil {
		t.Fatal("a coordinator capability was accepted by a worker's own call")
	}
	// An EMPTY participant is refused too: it names a mailbox belonging to
	// nobody, which must not be written to at all.
	if _, err := executeWorkerReport(context.Background(),
		agenttools.NewWorkerParticipant(""), reportParams(t, `{"kind":"done","text":"to nobody"}`),
		workerSeams(&fakeWorkerRecord{}),
	); err == nil {
		t.Fatal("a report from an empty participant was accepted")
	}
}

// The failure path the criterion names: the mailbox write failed, so the caller
// is told it failed rather than handed a silent ok — and the error it names is
// the one the endpoint maps to "say it again".
func TestWorkerReportSurfacesAFailedMailboxWrite(t *testing.T) {
	rec := &fakeWorkerRecord{reportErr: workers.ErrReportNotRecorded}
	_, err := executeWorkerReport(context.Background(),
		agenttools.NewWorkerParticipant("worker-1"),
		reportParams(t, `{"kind":"done","text":"this one is lost"}`), workerSeams(rec))
	if !errors.Is(err, workers.ErrReportNotRecorded) {
		t.Fatalf("err = %v, want ErrReportNotRecorded", err)
	}
	// The other end of the same fact: a worker whose coordinator is not
	// recorded has no box at all, and that is a different report to the caller.
	rec = &fakeWorkerRecord{reportErr: workers.ErrNoCoordinator}
	if _, err := executeWorkerReport(context.Background(),
		agenttools.NewWorkerParticipant("worker-1"),
		reportParams(t, `{"kind":"question","text":"anyone?"}`), workerSeams(rec),
	); !errors.Is(err, workers.ErrNoCoordinator) {
		t.Fatalf("err = %v, want ErrNoCoordinator", err)
	}
	// A backend that keeps no record owes the caller a sentence, not a panic.
	if _, err := executeWorkerReport(context.Background(),
		agenttools.NewWorkerParticipant("worker-1"),
		reportParams(t, `{"kind":"done","text":"x"}`),
		toolSeams{workerEnvironment: testWorkerEnv},
	); err == nil || !strings.Contains(err.Error(), "keeps no worker record") {
		t.Fatalf("a backend with no record answered %v", err)
	}
}

// A worker cannot reach another coordinator's mailbox or another worker's BY
// ANY ARGUMENT IT CAN PASS, and that is asserted as text: the params carry no
// recipient, so the words that would name one are not in the schema at all.
func TestTheReportSchemaHasNoRecipientToName(t *testing.T) {
	reg, err := agenttools.Assemble(toolsDirFS(t))
	if err != nil {
		t.Fatalf("assemble tools: %v", err)
	}
	tool, ok := reg.Lookup("workers.report")
	if !ok {
		t.Fatal("workers.report is not registered")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.ParamsSchema, &schema); err != nil {
		t.Fatalf("decode params schema: %v", err)
	}
	for _, forbidden := range []string{"worker", "to", "recipient", "coordinator", "from", "sender", "session"} {
		if _, present := schema.Properties[forbidden]; present {
			t.Errorf("the params carry %q, so a worker could name somebody else", forbidden)
		}
	}
	if len(schema.Properties) != 4 {
		t.Errorf("params are %v, want exactly kind, text, estimate and artifact", schema.Properties)
	}
	for _, want := range []string{"kind", "text"} {
		found := false
		for _, name := range schema.Required {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is not required", want)
		}
	}
}
