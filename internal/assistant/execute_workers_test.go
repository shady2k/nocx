package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/workers"
)

const testWorkerEnv = "env-local"

// fakeWorkerRecord records what it was asked and answers what it is told to.
type fakeWorkerRecord struct {
	registered []workers.RegisterRequest
	held       []workers.Participant
	registerFn func(workers.RegisterRequest) (workers.Participant, error)
	heldErr    error
	heldFor    []string
	owed       []workers.Fact
	mail       map[workers.ReaderID][]workers.Message
	sent       []workers.Message
	unread     []workers.Message
	fetchedBy  []workers.ReaderID
	acked      []int64
	closed     []workers.ParticipantID
	closeErr   error
	// waitedFor records the worker a wait was opened on, and waitHeld is what
	// it answers with — a double that returned HeldBy's rows would hide a
	// carrier that never waited at all.
	waitedFor []workers.ID
	waitHeld  []workers.Participant
	// readOrder records which of the two reads happened first, because the
	// order is the whole correctness of the answer: the fetch is what clears
	// the set, so asking after it always answers nothing.
	readOrder []string
}

func (f *fakeWorkerRecord) Register(_ context.Context, req workers.RegisterRequest) (workers.Participant, error) {
	f.registered = append(f.registered, req)
	if f.registerFn != nil {
		return f.registerFn(req)
	}
	return workers.Participant{ID: "p-1", State: workers.StateLive, Task: req.Task}, nil
}

func (f *fakeWorkerRecord) HeldBy(_ context.Context, coordinatorSession string) ([]workers.Participant, error) {
	f.heldFor = append(f.heldFor, coordinatorSession)
	f.readOrder = append(f.readOrder, "heldby")
	return f.held, f.heldErr
}

func (f *fakeWorkerRecord) Say(_ context.Context, id workers.ID, from, to workers.ReaderID, body string) (workers.Message, error) {
	m := workers.Message{
		ID:    workers.MessageID(fmt.Sprintf("m-%d", len(f.sent)+1)),
		Group: id, Sender: from, Recipient: to, Body: body,
		Seq: int64(len(f.sent) + 1),
	}
	f.sent = append(f.sent, m)
	return m, nil
}

// Inbox hands over what the mailbox holds and REMEMBERS WHO ASKED, because
// the coordinator's mailbox is named by its session and a carrier that
// fetched under the wrong name would look identical in the result.
func (f *fakeWorkerRecord) Inbox(_ context.Context, mailbox, reader workers.ReaderID, _ int) (workers.Fetch, error) {
	f.fetchedBy = append(f.fetchedBy, reader)
	msgs := f.mail[mailbox]
	if f.mail != nil {
		// Handing over is what advances a cursor, and this double stands in
		// for that: a second call must not return the same page again, or a
		// test could not tell "asking is what hands them over" from "asking
		// shows them".
		f.mail[mailbox] = nil
	}
	// The cursor comes back WITH the page, as the real registrar's does. A
	// double that returned messages and a zero position would let a carrier
	// that never reported one look correct here and be useless in the
	// product, where the position is the only thing a reader can acknowledge.
	out := workers.Fetch{Messages: msgs, Cursor: workers.Cursor{Mailbox: mailbox, Reader: reader}}
	for i := range out.Messages {
		if out.Messages[i].Seq == 0 {
			out.Messages[i].Seq = int64(i + 1)
		}
		out.Cursor.Fetched = out.Messages[i].Seq
	}
	return out, nil
}

func (f *fakeWorkerRecord) Undelivered(context.Context, workers.ID) ([]workers.Message, error) {
	return f.unread, nil
}

func (f *fakeWorkerRecord) Wait(_ context.Context, _ string, id workers.ID) ([]workers.Participant, error) {
	f.waitedFor = append(f.waitedFor, id)
	if f.waitHeld != nil {
		return f.waitHeld, nil
	}
	return f.held, nil
}

func (f *fakeWorkerRecord) Close(_ context.Context, _ string, id workers.ParticipantID) error {
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, id)
	return nil
}

func (f *fakeWorkerRecord) Acknowledge(_ context.Context, _, _ workers.ReaderID, through int64) error {
	f.acked = append(f.acked, through)
	return nil
}

func (f *fakeWorkerRecord) Undispatched() []workers.Fact {
	f.readOrder = append(f.readOrder, "undispatched")
	return f.owed
}

func workerSeams(rec WorkerRecord) toolSeams {
	return toolSeams{workerStore: rec, workerEnvironment: testWorkerEnv, runID: "run-1"}
}

// A coordinator holding the environment its grant named.
func testCoordinator(session string, environments ...string) *agenttools.WorkerCoordinator {
	scopes := make([]content.GrantScope, 0, len(environments))
	for _, e := range environments {
		scopes = append(scopes, content.GrantScope{Kind: content.ResourceEnvironment, ID: e})
	}
	return agenttools.NewWorkerCoordinator(session, scopes)
}

// D3, at the tool: the answer is about the run's OWN session, and the model
// has no way to ask about another — there is no parameter to put one in.
func TestWorkerHoldingsAnswersTheRunsOwnSession(t *testing.T) {
	rec := &fakeWorkerRecord{held: []workers.Participant{
		{ID: "p-1", State: workers.StateLive, Task: "read AGENTS.md"},
		{
			ID: "p-2", State: workers.StateCompleted, Task: "build it",
			Declared: &workers.Declaration{OK: true, Summary: "built"},
		},
	}}
	out, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv), json.RawMessage(`{}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("holdings: %v", err)
	}
	if len(rec.heldFor) != 1 || rec.heldFor[0] != "sess-coordinator" {
		t.Fatalf("record was asked about %v, want the run's own session", rec.heldFor)
	}
	var got workerHoldingsResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("participants = %+v, want both", got.Participants)
	}
	if got.Participants[0].Task != "read AGENTS.md" || got.Participants[0].State != "live" {
		t.Fatalf("first participant = %+v", got.Participants[0])
	}
	// The summary rides only when the worker actually said something.
	if got.Participants[0].Summary != "" {
		t.Fatalf("a worker that said nothing was given a summary: %+v", got.Participants[0])
	}
	if got.Participants[1].Summary != "built" {
		t.Fatalf("second participant = %+v", got.Participants[1])
	}
}

// A9, asserted against the wire and not against intent: the holdings schema
// has NO participant parameter, the way session.run's has no session
// parameter. What the model cannot spell it cannot ask for.
func TestTheWorkerToolsNameNoParticipantOnTheWire(t *testing.T) {
	for _, name := range []string{"workers.holdings.schema.json", "workers.spawn.schema.json"} {
		t.Run(name, func(t *testing.T) {
			//nolint:gosec // name is one of the two literals in the loop above
			raw, err := os.ReadFile("../../contracts/tools/" + name)
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			var schema struct {
				AdditionalProperties bool                       `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("schema: %v", err)
			}
			if schema.AdditionalProperties {
				t.Fatalf("%s admits additional properties, so it bounds nothing", name)
			}
			for prop := range schema.Properties {
				if strings.Contains(strings.ToLower(prop), "participant") ||
					strings.Contains(strings.ToLower(prop), "session") ||
					strings.Contains(strings.ToLower(prop), "worker") {
					t.Fatalf("%s takes %q; the holder's own resources live inside the object", name, prop)
				}
			}
		})
	}
}

// A spawn outside the run's fence is REFUSED, and the refusal names both what
// was asked for and what was available — a message that says only "no" leaves
// the model guessing at a boundary it cannot see.
func TestWorkerSpawnRefusesAnEnvironmentOutsideTheFence(t *testing.T) {
	rec := &fakeWorkerRecord{}
	// A coordinator whose grant named a DIFFERENT environment.
	_, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", "env-somewhere-else"),
		json.RawMessage(`{"command":"claude","task":"read it"}`), workerSeams(rec))
	if err == nil {
		t.Fatalf("a spawn outside the fence was accepted")
	}
	if !strings.Contains(err.Error(), testWorkerEnv) || !strings.Contains(err.Error(), "env-somewhere-else") {
		t.Fatalf("err = %v, want it to name what was asked for and what was available", err)
	}
	if len(rec.registered) != 0 {
		t.Fatalf("a refused spawn reached the record: %+v", rec.registered)
	}
}

// A10 in its strongest form: a coordinator minted from a grant with NO
// environment at all can spawn nowhere. This is what "a worker call carries no
// authority the session does not already have" means in code.
func TestACoordinatorFromAFenceWithNoEnvironmentCanSpawnNowhere(t *testing.T) {
	rec := &fakeWorkerRecord{}
	_, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(`{"command":"claude","task":"read it"}`), workerSeams(rec))
	if err == nil {
		t.Fatalf("a coordinator with an empty fence spawned a worker")
	}
	if len(rec.registered) != 0 {
		t.Fatalf("a refused spawn reached the record: %+v", rec.registered)
	}
}

// The ordinary case: the command and the task travel through untouched, the
// coordinator session is the run's own, and the result says live.
func TestWorkerSpawnStartsOneWorkerAndReturnsItLive(t *testing.T) {
	rec := &fakeWorkerRecord{}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude --resume","task":"read AGENTS.md and report"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(rec.registered) != 1 {
		t.Fatalf("record saw %d registrations, want 1", len(rec.registered))
	}
	req := rec.registered[0]
	if req.Command != "claude --resume" || req.Task != "read AGENTS.md and report" {
		t.Fatalf("registration = %+v", req)
	}
	if req.CoordinatorSession != "sess-coordinator" || req.Role != workers.RoleWorker {
		t.Fatalf("registration = %+v, want the run's own session and a worker role", req)
	}
	if req.Environment != testWorkerEnv {
		t.Fatalf("registration environment = %q, want the fenced one", req.Environment)
	}
	// Provenance travels; it decides nothing.
	if req.CreatedByRunID != "run-1" {
		t.Fatalf("registration lost its run provenance: %+v", req)
	}
	var got workerSpawnResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result: %v (%s)", err, out)
	}
	if got.ID != "p-1" || got.State != "live" {
		t.Fatalf("result = %+v, want the worker live", got)
	}
}

// A registration that failed is an ERROR and never a half-result: there is no
// id for a coordinator to address something that did not start.
func TestAFailedSpawnIsAnErrorAndNotAResult(t *testing.T) {
	rec := &fakeWorkerRecord{registerFn: func(workers.RegisterRequest) (workers.Participant, error) {
		return workers.Participant{ID: "p-1", State: workers.StateInterrupted}, workers.ErrEnrolmentNeverArrived
	}}
	out, err := executeWorkerSpawn(context.Background(),
		testCoordinator("sess-coordinator", testWorkerEnv),
		json.RawMessage(`{"command":"claude","task":"read it"}`), workerSeams(rec))
	if err == nil {
		t.Fatalf("a spawn that never enrolled returned a result: %s", out)
	}
	if !errors.Is(err, workers.ErrEnrolmentNeverArrived) {
		t.Fatalf("err = %v, want the cause to survive", err)
	}
	if out != "" {
		t.Fatalf("a failed spawn returned %q", out)
	}
}

// A backend with no record refuses and says so, rather than accepting a spawn
// into nothing. Both tools, because a coordinator that could ASK but not spawn
// would be told an empty holdings and conclude it had started nothing.
func TestTheWorkerToolsRefuseWhenThereIsNoRecord(t *testing.T) {
	c := testCoordinator("sess-coordinator", testWorkerEnv)
	seams := toolSeams{workerEnvironment: testWorkerEnv}
	if _, err := executeWorkerHoldings(context.Background(), c, json.RawMessage(`{}`), seams); err == nil {
		t.Fatalf("holdings answered with no record wired")
	}
	if _, err := executeWorkerSpawn(context.Background(), c,
		json.RawMessage(`{"command":"claude","task":"t"}`), seams); err == nil {
		t.Fatalf("spawn accepted with no record wired")
	}
}

// A run with no session cannot be answered about, and an empty holdings would
// be indistinguishable from a coordinator that holds nothing.
func TestWorkerToolsRefuseARunWithNoSession(t *testing.T) {
	rec := &fakeWorkerRecord{}
	if _, err := executeWorkerHoldings(context.Background(),
		testCoordinator("", testWorkerEnv), json.RawMessage(`{}`), workerSeams(rec)); err == nil {
		t.Fatalf("holdings answered for a run with no session")
	}
}

// Both tools refuse a capability that is not a coordinator's. The type switch
// is what proves the two authorities are distinct; this is the assertion that
// the executors honour it rather than casting hopefully.
func TestWorkerToolsRefuseAnotherCapability(t *testing.T) {
	notACoordinator := agenttools.NewSessionReader(nil, nil, nil)
	if _, err := executeWorkerHoldings(context.Background(), notACoordinator,
		json.RawMessage(`{}`), workerSeams(&fakeWorkerRecord{})); err == nil {
		t.Fatalf("holdings ran on a session reader")
	}
	if _, err := executeWorkerSpawn(context.Background(), notACoordinator,
		json.RawMessage(`{"command":"claude","task":"t"}`), workerSeams(&fakeWorkerRecord{})); err == nil {
		t.Fatalf("spawn ran on a session reader")
	}
}

// The wake says "call workers.holdings", so holdings has to distinguish the
// worker it was about from the ones that have not moved.
//
// And it has to read the set BEFORE the fetch, because the fetch is what
// clears it (D8: the cursor advances on the fetch). The order is asserted
// directly rather than inferred from the answer, because the wrong order
// produces a correct-looking empty flag on every row.
func TestHoldingsMarksWhatTheCoordinatorHasNotBeenToldAbout(t *testing.T) {
	rec := &fakeWorkerRecord{
		held: []workers.Participant{
			{ID: "p-new", State: workers.StateCompleted, Task: "reported"},
			{ID: "p-old", State: workers.StateLive, Task: "still working"},
		},
		owed: []workers.Fact{{Participant: "p-new", Kind: workers.FactDeclared}},
	}
	raw, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"), nil, workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.holdings: %v", err)
	}
	var got workerHoldingsResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(got.Participants))
	}
	if !got.Participants[0].NeedsJudgement {
		t.Fatalf("the worker that just reported is not marked as new: %+v", got.Participants[0])
	}
	if got.Participants[1].NeedsJudgement {
		t.Fatalf("a worker that has not moved is marked as new: %+v", got.Participants[1])
	}
	if len(rec.readOrder) != 2 || rec.readOrder[0] != "undispatched" {
		t.Fatalf("read order = %v, want the set read before the fetch that clears it", rec.readOrder)
	}
}

// THE WIRE IS A PARTY TO THE CONTRACT (AGENTS.md rule 5), and here the wire
// is what the model reads.
//
// It validates the REAL result of the real executor against the schema's own
// result definition, not a payload the test built: a test that validated its
// own fixture would prove the fixture is well-formed and nothing about what
// the tool returns. additionalProperties:false plus required is what makes
// that exact — a field the executor emits and the schema does not declare is
// a field the model was told about by nobody.
func TestWorkerHoldingsResultConformsToItsContract(t *testing.T) {
	rec := &fakeWorkerRecord{
		held: []workers.Participant{
			{
				ID: "p-1", Group: "worker-1", State: workers.StateCompleted, Task: "read AGENTS.md",
				Declared: &workers.Declaration{OK: true, Summary: "read it"},
			},
			{ID: "p-2", Group: "worker-1", State: workers.StateLive, Task: "still working"},
		},
		owed: []workers.Fact{{Participant: "p-1", Kind: workers.FactDeclared}},
		// Mail and undelivered mail both present, so additionalProperties:
		// false is validating the shape it is actually asked about rather
		// than a result that happens to omit the new fields.
		mail: map[workers.ReaderID][]workers.Message{
			"sess-coordinator": {{Sender: "p-1", Body: "the file moved"}},
		},
		unread: []workers.Message{{Sender: "sess-coordinator", Recipient: "p-2", Body: "wait for p-1"}},
	}
	raw, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"), nil, workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.holdings: %v", err)
	}

	c := jsonschema.NewCompiler()
	//nolint:gosec // a literal path to a contract in the tree
	f, err := os.Open("../../contracts/tools/workers.holdings.schema.json")
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	const id = "https://nocx.local/contracts/tools/workers.holdings.schema.json"
	if addErr := c.AddResource(id, doc); addErr != nil {
		t.Fatalf("add resource: %v", addErr)
	}
	schema, err := c.Compile(id + "#/$defs/result")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if err := schema.Validate(payload); err != nil {
		t.Fatalf("workers.holdings result does not satisfy its contract: %v\npayload was:\n%s", err, raw)
	}
	if !strings.Contains(raw, `"needsJudgement":true`) {
		t.Fatalf("the result names nothing as new, so the schema check proved nothing: %s", raw)
	}
	if !strings.Contains(raw, `"mail"`) || !strings.Contains(raw, `"undeliveredMail"`) {
		t.Fatalf("the result carries no mail, so the schema check proved nothing about it: %s", raw)
	}
}

// A9 has one exception here and it is worth pinning rather than assuming:
// workers.say NAMES A WORKER, because the recipient is an ADDRESS and not one of
// the holder's own resources. What it must not name is the sender or the
// session, which are the holder's own and live inside the capability.
func TestWorkerSayNamesAnAddressAndNeverTheSender(t *testing.T) {
	//nolint:gosec // a literal path to a contract in the tree
	raw, err := os.ReadFile("../../contracts/tools/workers.say.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if schema.AdditionalProperties {
		t.Fatalf("workers.say admits additional properties, so it bounds nothing")
	}
	if _, ok := schema.Properties["worker"]; !ok {
		t.Fatalf("workers.say cannot address anybody")
	}
	for prop := range schema.Properties {
		switch strings.ToLower(prop) {
		case "sender", "from", "session", "as", "participant":
			t.Fatalf("workers.say takes %q; who is speaking is the run's, not the model's", prop)
		}
	}
}

// ── the mailbox's carriers (nocx-dkawo.11) ────────────────────────────────

// MAIL RIDES OUR OWN CALLS (D7). The coordinator asks what it holds and is
// told what was said to it in the same breath, because those are one question
// — what has happened since I last looked — and because a hook that pushed
// mail onto the result of whatever tool the model happened to call is the
// alternative D7 names and rejects.
func TestHoldingsCarriesTheCoordinatorsMail(t *testing.T) {
	rec := &fakeWorkerRecord{
		held: []workers.Participant{{ID: "p-1", Group: "worker-1", State: workers.StateLive, Task: "read it"}},
		mail: map[workers.ReaderID][]workers.Message{
			"sess-coordinator": {
				{Sender: "p-1", Body: "the file is not where you said"},
			},
		},
	}
	raw, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"), nil, workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.holdings: %v", err)
	}
	var got workerHoldingsResult
	if decErr := json.Unmarshal([]byte(raw), &got); decErr != nil {
		t.Fatalf("unmarshal: %v", decErr)
	}
	if len(got.Mail) != 1 || got.Mail[0].From != "p-1" {
		t.Fatalf("mail = %+v, want the worker's one message", got.Mail)
	}
	if got.Mail[0].Message != "the file is not where you said" {
		t.Fatalf("message = %q", got.Mail[0].Message)
	}
	// The coordinator's mailbox is named by its SESSION, which is what makes
	// a restarted coordinator the same reader. A carrier that fetched under
	// any other name would look identical in this result and be wrong.
	if len(rec.fetchedBy) != 1 || rec.fetchedBy[0] != "sess-coordinator" {
		t.Fatalf("fetched by %v, want the coordinator's own session", rec.fetchedBy)
	}

	// Asking is what hands it over, so a second ask does not show it again.
	raw, err = executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"), nil, workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.holdings again: %v", err)
	}
	got = workerHoldingsResult{}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Mail) != 0 {
		t.Fatalf("mail was handed over twice: %+v", got.Mail)
	}
}

// What the coordinator SAID and nobody took is visible, and it is counted
// separately from what was said to it. A worker that never looks is a worker
// that never got the instruction, and this is the only place that difference
// shows.
func TestHoldingsCountsWhatTheCoordinatorSaidAndNobodyTook(t *testing.T) {
	rec := &fakeWorkerRecord{
		held: []workers.Participant{{ID: "p-1", Group: "worker-1", State: workers.StateLive}},
		unread: []workers.Message{
			{Sender: "sess-coordinator", Recipient: "p-1", Body: "start with AGENTS.md"},
			// Somebody else's undelivered mail is not the coordinator's
			// count: it says nothing about whether IT was heard.
			{Sender: "p-2", Recipient: "p-1", Body: "not mine"},
		},
	}
	raw, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"), nil, workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.holdings: %v", err)
	}
	var got workerHoldingsResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UndeliveredMail != 1 {
		t.Fatalf("undeliveredMail = %d, want 1", got.UndeliveredMail)
	}
}

// workers.say writes as the RUN'S OWN SESSION and never as anything the model
// named. A sender a caller could choose is a sender a caller could forge.
func TestSayWritesAsTheRunsOwnSessionAndIntoItsOwnGroup(t *testing.T) {
	rec := &fakeWorkerRecord{
		held: []workers.Participant{{ID: "p-1", Group: "worker-7", State: workers.StateLive}},
	}
	raw, err := executeWorkerSay(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(`{"worker":"p-1","message":"start with AGENTS.md"}`),
		workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.say: %v", err)
	}
	if len(rec.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(rec.sent))
	}
	m := rec.sent[0]
	if m.Sender != "sess-coordinator" {
		t.Fatalf("sender = %q, want the run's own session", m.Sender)
	}
	if m.Recipient != "p-1" || m.Body != "start with AGENTS.md" {
		t.Fatalf("message = %+v", m)
	}
	// The worker is read off what the session holds rather than assumed to
	// equal the session id, because the record permits a named workers.
	if m.Group != "worker-7" {
		t.Fatalf("worker = %q, want the one this session's worker is in", m.Group)
	}
	var out workerSayResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Seq != 1 || out.ID == "" {
		t.Fatalf("result = %+v", out)
	}
}

// Both halves refuse what they cannot do, and say what was missing.
func TestSayRefusesAnEmptyMessageAndAnUnnamedWorker(t *testing.T) {
	for _, args := range []string{
		`{"worker":"","message":"hello"}`,
		`{"worker":"p-1","message":""}`,
	} {
		rec := &fakeWorkerRecord{held: []workers.Participant{{ID: "p-1", Group: "w"}}}
		if _, err := executeWorkerSay(context.Background(),
			testCoordinator("sess-coordinator"), json.RawMessage(args), workerSeams(rec)); err == nil {
			t.Fatalf("workers.say(%s) was accepted", args)
		}
		if len(rec.sent) != 0 {
			t.Fatalf("workers.say(%s) wrote something anyway", args)
		}
	}
}

// A backend with no record refuses rather than pretending to have written.
func TestSayRefusesWhenThereIsNoRecord(t *testing.T) {
	if _, err := executeWorkerSay(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(`{"worker":"p-1","message":"hi"}`),
		toolSeams{}); err == nil {
		t.Fatalf("workers.say without a record was accepted")
	}
}

// THE FOUR ACKNOWLEDGEMENTS ARE NEVER MERGED (D8). A fetch is not a claim
// that the coordinator acted; the coordinator says that separately, on a
// later call, about a cursor it was handed — which is §7.2's "acknowledges
// the cursor together with the effects it commits from that response".
func TestHoldingsAcknowledgesOnlyWhatTheCoordinatorSendsBack(t *testing.T) {
	rec := &fakeWorkerRecord{
		held: []workers.Participant{{ID: "p-1", Group: "worker-1", State: workers.StateLive}},
		mail: map[workers.ReaderID][]workers.Message{
			"sess-coordinator": {{Sender: "p-1", Body: "please spawn a second worker"}},
		},
	}
	// A fetch with no acknowledgement acknowledges nothing.
	raw, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"), nil, workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.holdings: %v", err)
	}
	if len(rec.acked) != 0 {
		t.Fatalf("a fetch acknowledged %v on its own", rec.acked)
	}
	var got workerHoldingsResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cursor == 0 {
		t.Fatalf("the coordinator was handed mail and no position to acknowledge: %s", raw)
	}

	// And the position it sends back is the one it was given.
	if _, err := executeWorkerHoldings(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(fmt.Sprintf(`{"acknowledge":%d}`, got.Cursor)),
		workerSeams(rec)); err != nil {
		t.Fatalf("workers.holdings with an acknowledgement: %v", err)
	}
	if len(rec.acked) != 1 || rec.acked[0] != got.Cursor {
		t.Fatalf("acknowledged %v, want %d", rec.acked, got.Cursor)
	}
}

// ── the wait and the close (nocx-dkawo.13) ────────────────────────────────

// The wait answers what holdings answers, because it is the same question
// asked at a different moment. A shape of its own would be a second account
// of what a session holds, and the two would disagree the first time either
// moved.
func TestWaitAnswersWhatHoldingsAnswers(t *testing.T) {
	rec := &fakeWorkerRecord{
		waitHeld: []workers.Participant{
			{
				ID: "p-1", Group: "worker-1", State: workers.StateCompleted, Task: "read it",
				Declared: &workers.Declaration{OK: true, Summary: "done"},
			},
			{ID: "p-2", Group: "worker-1", State: workers.StateLive, Task: "still going"},
		},
		owed: []workers.Fact{{Participant: "p-1", Kind: workers.FactDeclared}},
		mail: map[workers.ReaderID][]workers.Message{
			"sess-coordinator": {{Sender: "p-1", Body: "here is what I found"}},
		},
	}
	raw, err := executeWorkerWait(context.Background(),
		testCoordinator("sess-coordinator"), json.RawMessage(`{"seconds":1}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.wait: %v", err)
	}
	var got workerHoldingsResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Participants) != 2 {
		t.Fatalf("participants = %d, want 2", len(got.Participants))
	}
	if got.Participants[0].State != string(workers.StateCompleted) || !got.Participants[0].NeedsJudgement {
		t.Fatalf("the settled worker is not reported as settled and new: %+v", got.Participants[0])
	}
	if got.Participants[1].State != string(workers.StateLive) {
		t.Fatalf("the other worker should still be live: %+v", got.Participants[1])
	}
	// D7: a report, a WAIT or an explicit inbox check returns pending mail.
	if len(got.Mail) != 1 || got.Mail[0].From != "p-1" {
		t.Fatalf("the wait carried no mail: %+v", got.Mail)
	}
	// It WAITED. A carrier that quietly answered from HeldBy would look
	// identical in the result and would never hold a turn at all.
	if len(rec.waitedFor) != 1 {
		t.Fatalf("the carrier waited %d times, want once", len(rec.waitedFor))
	}
	if len(rec.heldFor) != 0 {
		t.Fatalf("the carrier fetched through HeldBy as well as waiting: %v", rec.heldFor)
	}
}

// The wait's bound is the coordinator's to choose, and the default covers a
// coordinator that names none.
func TestWaitTakesItsBoundFromTheCallAndOtherwiseDefaults(t *testing.T) {
	for _, args := range []string{`{}`, ``, `{"seconds":5}`} {
		rec := &fakeWorkerRecord{waitHeld: []workers.Participant{}}
		var raw json.RawMessage
		if args != "" {
			raw = json.RawMessage(args)
		}
		if _, err := executeWorkerWait(context.Background(),
			testCoordinator("sess-coordinator"), raw, workerSeams(rec)); err != nil {
			t.Fatalf("workers.wait(%s): %v", args, err)
		}
		if len(rec.waitedFor) != 1 {
			t.Fatalf("workers.wait(%s) did not wait", args)
		}
	}
}

// A close ends the named worker as the RUN'S OWN SESSION, and says what was
// asked rather than what happened: ending a process is a request, and how it
// ended is a fact nocx observes for itself.
func TestCloseEndsTheNamedWorkerAndClaimsNothingMore(t *testing.T) {
	rec := &fakeWorkerRecord{held: []workers.Participant{{ID: "p-1", Group: "worker-1"}}}
	raw, err := executeWorkerClose(context.Background(),
		testCoordinator("sess-coordinator"),
		json.RawMessage(`{"worker":"p-1"}`), workerSeams(rec))
	if err != nil {
		t.Fatalf("workers.close: %v", err)
	}
	if len(rec.closed) != 1 || rec.closed[0] != "p-1" {
		t.Fatalf("closed %v, want p-1", rec.closed)
	}
	var out workerCloseResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != "p-1" || !out.Ended {
		t.Fatalf("result = %+v", out)
	}
	// The result carries nothing about the worker's STATE. A close that
	// reported one would be claiming a fact it did not witness.
	if strings.Contains(raw, "state") || strings.Contains(raw, "completed") {
		t.Fatalf("the close result claims a state: %s", raw)
	}
}

// A refused close is an error the coordinator can read, and it names nothing
// as ended.
func TestCloseRefusalsReachTheCoordinator(t *testing.T) {
	t.Run("no worker named", func(t *testing.T) {
		rec := &fakeWorkerRecord{}
		if _, err := executeWorkerClose(context.Background(),
			testCoordinator("sess-coordinator"), json.RawMessage(`{"worker":""}`), workerSeams(rec)); err == nil {
			t.Fatal("a close with no worker was accepted")
		}
		if len(rec.closed) != 0 {
			t.Fatalf("it ended %v anyway", rec.closed)
		}
	})

	t.Run("the record refuses", func(t *testing.T) {
		rec := &fakeWorkerRecord{closeErr: workers.ErrNotDelegated}
		_, err := executeWorkerClose(context.Background(),
			testCoordinator("sess-coordinator"), json.RawMessage(`{"worker":"p-1"}`), workerSeams(rec))
		if !errors.Is(err, workers.ErrNotDelegated) {
			t.Fatalf("err = %v, want the record's refusal to reach the coordinator", err)
		}
	})

	t.Run("no record at all", func(t *testing.T) {
		if _, err := executeWorkerClose(context.Background(),
			testCoordinator("sess-coordinator"),
			json.RawMessage(`{"worker":"p-1"}`), toolSeams{}); err == nil {
			t.Fatal("a close without a record was accepted")
		}
	})
}
