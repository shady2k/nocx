package toolendpoint

// workers.report over the REAL socket (nocx-luqz9.4).
//
// Two things are asserted here and neither is available anywhere else. AGENTS.md
// rule 5's third check: the real result, off the real Unix socket, validated
// against `$defs/result` — a payload the test built itself would prove the
// struct is well-formed, not that the server sends it. And the refusal an agent
// reads: a session that is not a worker's is refused with a sentence that sends
// it somewhere, produced through the same `rpcErrorFor` every other refusal goes
// through.

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tools "github.com/shady2k/nocx/contracts/tools"
	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/workers"
)

// reportContractRecord is a record that accepts a worker's report and answers
// with the committed row, including the kind and the checkpoint extras — the
// fields whose absence from a test double is exactly how a wire field goes
// unasserted.
type reportContractRecord struct{ contractWorkerRecord }

func (reportContractRecord) Report(_ context.Context, id workers.ParticipantID, rep workers.Report) (workers.Message, error) {
	return workers.Message{
		ID: workers.MessageID("message-1"), Group: workers.ID(id),
		Sender: workers.ReaderID(id), Seq: 7,
		Kind: rep.Kind, Body: rep.Text,
		// The record's own stamp, which every writer in the package sets: a row
		// with no time would still satisfy the schema as a zero instant, and this
		// fixture exists to send what production sends.
		CommittedAt: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	}, nil
}

// reportWorkerInvocation is the invocation a WORKER's connection is admitted
// with: a participant identity, which is the whole of what makes this call
// reachable.
func reportWorkerInvocation(workspace string) assistant.ToolInvocation {
	return assistant.ToolInvocation{
		Context: context.Background(),
		RunContext: agenttools.RunContext{
			RunID:     "run-1",
			Session:   "worker-session-1",
			Workspace: workspace,
			// The identity the authorizer establishes from the peer's process
			// tree, and the only thing workers.report narrows on.
			Participant: "worker-1",
		},
		Grant: reportWorkerGrant(),
	}
}

// reportWorkerGrant is what participantGrant mints for a worker: Observe alone,
// over a workspace scope and nothing else. It is written out here rather than
// imported because internal/app owns the real one and this package cannot
// depend on it — and the shape matters, because a grant that carried a session
// scope would be admitting a caller the product never produces.
func reportWorkerGrant() content.Grant {
	permit := content.EffectRow{Decision: content.DecisionPermit}
	refuse := content.EffectRow{Decision: content.DecisionRefuse}
	return content.EffectPolicy{
		Observe:           permit,
		MutateReversible:  refuse,
		MutateDestructive: refuse,
		PrivilegeChange:   refuse,
		Disclose:          refuse,
		CrossBoundary:     refuse,
		Delegate:          refuse,
	}.AsGrant([]content.GrantScope{
		{Kind: content.ResourceWorkspace, ID: agenttools.ParticipantWorkspaceScopeID(contractWorkspace)},
	})
}

// TestGroupReport_OverTheWireConformsToContract is rule 5's third check for
// workers.report, for both a `done` and a checkpoint carrying the two optional
// extras — the second of which is the only case that exercises their part of
// the result schema.
func TestGroupReport_OverTheWireConformsToContract(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry, reportContractRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: reportWorkerInvocation(contractWorkspace)}
	endpoint := startEndpoint(t, Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   testLogger(),
	})
	schema := workerResultSchema(t, "workers.report")

	for i, params := range []string{
		`{"kind":"done","text":"the migration landed"}`,
		`{"kind":"progress","text":"half way","estimate":40,"artifact":"commit 4f2a1c9"}`,
	} {
		t.Run(params, func(t *testing.T) {
			conn := dialEndpoint(t, endpoint)
			defer func() { _ = conn.Close() }()
			request := `{"jsonrpc":"2.0","id":` + string(rune('1'+i)) +
				`,"method":"workers.report","params":` + params + "}\n"
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error != nil {
				t.Fatalf("response error = %+v", response.Error)
			}
			validateGroupResult(t, schema, response.Result, "workers.report")
		})
	}
}

// THE REFUSAL AN AGENT READS. A session that is not a worker's is refused, and
// the sentence says which fact refused it and where its own reports come from
// instead — the difference `rpcErrorFor` exists to make.
//
// It is driven over the socket rather than against the classifier directly,
// because the wrap the dispatcher puts around the constructor's error
// ("tool %q: construct capability: %w") is part of what the classifier has to
// see through, and only the wire exercises it end to end.
func TestGroupReportRefusesASessionThatIsNotAWorker(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry, reportContractRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	// A COORDINATOR: a session and a workspace, and no participant.
	auth := &testAuthorizer{inv: assistant.ToolInvocation{
		Context: context.Background(),
		RunContext: agenttools.RunContext{
			RunID: "run-1", Session: "sess-coordinator", Workspace: contractWorkspace,
		},
		Grant: contractGrant(),
	}}
	endpoint := startEndpoint(t, Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   testLogger(),
	})
	conn := dialEndpoint(t, endpoint)
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"workers.report","params":{"kind":"done","text":"from a coordinator"}}`+"\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error == nil {
		t.Fatalf("a coordinator made a worker's own call: %s", response.Result)
	}
	code, message, reason := rpcErrorFor(agenttools.ErrNoParticipant)
	if response.Error.Code != code || response.Error.Message != message {
		t.Fatalf("refusal = %d %q, want %d %q", response.Error.Code, response.Error.Message, code, message)
	}
	if response.Error.Data == nil || response.Error.Data.Reason != reason {
		t.Fatalf("refusal carried %+v, want the sentence rpcErrorFor writes", response.Error.Data)
	}
	// The sentence is an INSTRUCTION: it says what this session is not, and what
	// to call instead. Both parts are asserted, because each is a different
	// failure — an agent told only "refused" retries, and one told nothing about
	// workers.inbox waits for a report that already arrived.
	if reason == "" || message == "" {
		t.Fatalf("refusal = %q/%q, want both set", message, reason)
	}
	if !strings.Contains(reason, "worker") || !strings.Contains(reason, "workers.inbox") {
		t.Fatalf("the refusal does not say what this session is or what to call instead: %q", reason)
	}
	// NOT the unclassified arm: that sentence calls the failure a fault inside
	// nocx and tells the agent to stop, which is wrong twice over here.
	if response.Error.Code == rpcInternalError {
		t.Fatalf("the participant refusal was classified as a fault inside nocx: %+v", response.Error)
	}
}

// The params schema is enforced, and by the referenced document rather than by
// a second copy: a kind outside the three, or a checkpoint extra on a report of
// another kind, is refused before any executor runs. It is the same check
// `TestGroupEndpoint_ParamsUseTheReferencedToolSchemas` makes for the five older
// calls, extended to the one whose enum and whose two conditional extras are
// most of what the tool means.
func TestGroupReportRefusesParamsTheSchemaDoesNotDeclare(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	dispatcher, err := assistant.NewToolDispatcher(
		registry, reportContractRecord{}, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	endpoint := startEndpoint(t, Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     &testAuthorizer{inv: reportWorkerInvocation(contractWorkspace)},
		Dispatch: dispatcher,
		Logger:   testLogger(),
	})
	for _, params := range []string{
		`{"kind":"observation","text":"a state is not a report"}`,
		`{"kind":"done"}`,
		`{"kind":"done","text":"x","estimate":40}`,
		`{"kind":"progress","text":"x","estimate":150}`,
	} {
		t.Run(params, func(t *testing.T) {
			conn := dialEndpoint(t, endpoint)
			defer func() { _ = conn.Close() }()
			if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"method":"workers.report","params":`+params+"}\n"); err != nil {
				t.Fatalf("write request: %v", err)
			}
			response := readResponse(t, conn)
			if response.Error == nil || response.Error.Code != rpcInvalidParams {
				t.Fatalf("response error = %+v, want invalid params", response.Error)
			}
		})
	}
}

// A MAILBOX THAT REFUSES THE ROW, over the socket (criterion 8). The caller is
// told it failed rather than handed a silent ok — and the NEXT report works,
// which is the half that makes "say it again" an honest instruction rather than
// a repeat of something already refused.
//
// The fault is the record's OWN sentinel (workers.ErrReportNotRecorded), the
// same one the record returns when its mailbox write fails, so this exercises
// the classifier's arm rather than a failure invented for the test.
func TestGroupReportTellsTheCallerAFailedWriteAndTheNextCallStillWorks(t *testing.T) {
	registry, err := agenttools.Assemble(tools.Schemas)
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	rec := &failingReportRecord{}
	dispatcher, err := assistant.NewToolDispatcher(
		registry, rec, content.EnvironmentIDFor(content.EnvLocal, ""),
	)
	if err != nil {
		t.Fatalf("new worker dispatcher: %v", err)
	}
	endpoint := startEndpoint(t, Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     &testAuthorizer{inv: reportWorkerInvocation(contractWorkspace)},
		Dispatch: dispatcher,
		Logger:   testLogger(),
	})

	call := func(id string) rpcResponse {
		t.Helper()
		conn := dialEndpoint(t, endpoint)
		defer func() { _ = conn.Close() }()
		if _, err := io.WriteString(conn,
			`{"jsonrpc":"2.0","id":`+id+`,"method":"workers.report","params":{"kind":"done","text":"this one is lost"}}`+"\n",
		); err != nil {
			t.Fatalf("write request: %v", err)
		}
		return readResponse(t, conn)
	}

	refused := call("1")
	if refused.Error == nil {
		t.Fatalf("a report whose write failed answered as though it had been recorded: %s", refused.Result)
	}
	code, message, reason := rpcErrorFor(workers.ErrReportNotRecorded)
	if refused.Error.Code != code || refused.Error.Message != message {
		t.Fatalf("refusal = %d %q, want %d %q", refused.Error.Code, refused.Error.Message, code, message)
	}
	if refused.Error.Code == rpcInternalError {
		t.Fatalf("a failed mailbox write was reported as a fault nothing classified: %+v", refused.Error)
	}
	if refused.Error.Data == nil || !strings.Contains(refused.Error.Data.Reason, "again") {
		t.Fatalf("the refusal does not say the report may be sent again, which is the one thing "+
			"that works here: %+v", refused.Error)
	}
	// The reason the WIRE carried is the classifier's own, word for word: the
	// two are read here rather than trusted to stay in step, because a sentence
	// that drifted between the arm and the socket would leave the arm's test
	// green about something no agent ever sees.
	if refused.Error.Data == nil || refused.Error.Data.Reason != reason {
		t.Fatalf("the wire's reason is not the one rpcErrorFor writes: %+v", refused.Error.Data)
	}

	// The next report is the ordinary path: the failure was not held.
	accepted := call("2")
	if accepted.Error != nil {
		t.Fatalf("the report after a failed one was refused too: %+v", accepted.Error)
	}
	validateGroupResult(t, workerResultSchema(t, "workers.report"), accepted.Result, "workers.report")
	if rec.calls != 2 {
		t.Fatalf("the record saw %d reports, want both attempts", rec.calls)
	}
}

// failingReportRecord refuses the FIRST report and accepts every one after it,
// which is the shape criterion 8 names: a write that failed, and a caller for
// whom the next attempt is the ordinary path.
type failingReportRecord struct {
	contractWorkerRecord
	calls int
}

func (r *failingReportRecord) Report(_ context.Context, id workers.ParticipantID, rep workers.Report) (workers.Message, error) {
	r.calls++
	if r.calls == 1 {
		return workers.Message{}, workers.ErrReportNotRecorded
	}
	return r.contractWorkerRecord.Report(context.Background(), id, rep)
}
