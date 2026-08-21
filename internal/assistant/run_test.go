package assistant

// run tests (nocx-tjppv): the headline tool — the agent runs a command
// through the same submit path a person uses. The renderer half submits via
// the ordinary orchestration (block, ledger entry, attempt, artifact — all
// minted at the renderer), waits for the completion, and resolves the
// broker request with the entry id, the exit status and a window of the
// output. The backend never writes to the PTY (design §2.1 — rejected, not
// open for re-litigation): the executor asks the renderer through the run's
// requester seam, exactly as readScreen does.
//
// These tests mirror readscreen_test.go: the session narrowing (criterion 4
// — asserted by trying: a grant naming session A cannot run in session B,
// and the renderer is never asked), the wiring-gap honesty, and the window
// contract of the return (design §4.4).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/content"
)

// recordingRunner is the renderer-request seam with a call log and a
// scripted answer: the run tests assert what the tool ASKED the renderer —
// the "asserted by trying, not by inspecting" seam. RequestScreen exists
// only to satisfy the RendererRequester interface; run tests never call it.
type recordingRunner struct {
	mu      sync.Mutex
	asked   []askedRun
	body    json.RawMessage
	err     error
	screen  json.RawMessage
	screenE error
}

type askedRun struct {
	sessionID string
	command   string
}

func (r *recordingRunner) RequestScreen(ctx context.Context, sessionID string, region *FrameRegion) (json.RawMessage, error) {
	if r.screenE != nil {
		return nil, r.screenE
	}
	return r.screen, nil
}

func (r *recordingRunner) RequestRun(ctx context.Context, sessionID string, command string) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, askedRun{sessionID: sessionID, command: command})
	if r.err != nil {
		return nil, r.err
	}
	return r.body, nil
}

func (r *recordingRunner) runCalls() []askedRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]askedRun(nil), r.asked...)
}

// runResolvedBody builds the resolved body the transport's run kind would
// deliver: the entry id, the exit status (null when the block froze without
// one — an entered environment), the output's total line count, the span of
// the window actually returned and its text.
func runResolvedBody(entryID string, exitCode *int, status string, total, start, end int, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"entryId": entryID, "exitCode": exitCode, "status": status,
		"total": total, "start": start, "end": end, "text": text,
	})
	return b
}

// TestExecuteRun_SessionOutsideGrantNeverRequests is criterion 4: a grant
// naming session A cannot run a command in session B — asserted by trying,
// at the executor, through the narrowed capability. Naming B is refused
// BEFORE any renderer request: the recording runner proves the broker was
// never asked about B. The paired end: a run in A succeeds and the runner
// was asked exactly about A with the command.
func TestExecuteRun_SessionOutsideGrantNeverRequests(t *testing.T) {
	runner := agenttools.NewRunner([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	req := &recordingRunner{body: runResolvedBody("entry-1", new(0), "success", 2, 0, 2, "hello\nworld")}

	_, err := executeRun(context.Background(), runner, req, json.RawMessage(`{"sessionId":"session-b","command":"ls"}`))
	if err == nil || !strings.Contains(err.Error(), "outside the run's grant") {
		t.Fatalf("run in session-b error = %v, want the grant refusal", err)
	}
	if calls := req.runCalls(); len(calls) != 0 {
		t.Fatalf("a refused session reached the renderer: %+v", calls)
	}

	out, err := executeRun(context.Background(), runner, req, json.RawMessage(`{"sessionId":"session-a","command":"ls -la"}`))
	if err != nil {
		t.Fatalf("run in session-a failed: %v", err)
	}
	calls := req.runCalls()
	if len(calls) != 1 || calls[0].sessionID != "session-a" || calls[0].command != "ls -la" {
		t.Fatalf("runner asked %+v, want exactly one run of session-a with the command", calls)
	}
	var res struct {
		SessionID string `json:"sessionId"`
		EntryID   string `json:"entryId"`
		Status    string `json:"status"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.SessionID != "session-a" || res.EntryID != "entry-1" || res.Status != "success" || res.Text != "hello\nworld" {
		t.Fatalf("result = %+v, want session-a entry-1 success with text hello\\nworld", res)
	}
}

// TestMiddleware_RunRefusedOutsideGrantTerminates is the policy half of the
// same rule, through the real middleware: a model call naming a session the
// grant does not cover is REFUSED (terminal — ErrPolicyRefused), and the
// renderer is never asked. The grant names session-a; the model names
// session-b.
func TestMiddleware_RunRefusedOutsideGrantTerminates(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	req := &recordingRunner{body: runResolvedBody("entry-1", new(0), "success", 1, 0, 1, "x")}
	mw := middlewareForWithRequester(t, grant, &fakeLedger{}, nil, req)

	_, err := wrappedEndpoint(mw, "run", "c1", `{"sessionId":"session-b","command":"ls"}`)
	if !errors.Is(err, ErrPolicyRefused) {
		t.Fatalf("out-of-grant run error = %v, want ErrPolicyRefused", err)
	}
	if calls := req.runCalls(); len(calls) != 0 {
		t.Fatalf("a refused call reached the renderer: %+v", calls)
	}

	out, err := wrappedEndpoint(mw, "run", "c2", `{"sessionId":"session-a","command":"ls"}`)
	if err != nil {
		t.Fatalf("in-grant run failed: %v", err)
	}
	calls := req.runCalls()
	if len(calls) != 1 || calls[0].sessionID != "session-a" || calls[0].command != "ls" {
		t.Fatalf("runner asked %+v, want exactly one run of session-a", calls)
	}
	if !strings.Contains(out, `"entryId":"entry-1"`) {
		t.Fatalf("result %q lacks the entry id", out)
	}
}

// TestMiddleware_RunWithoutRequesterIsHonest: a run whose transport wired no
// renderer-request seam reports the wiring gap as an error — a declared
// InRenderer tool never silently no-ops.
func TestMiddleware_RunWithoutRequesterIsHonest(t *testing.T) {
	grant := sessionGrant("session-a", autonomousMatrix())
	mw := middlewareFor(t, grant, &fakeLedger{}, nil) // requester nil

	_, err := wrappedEndpoint(mw, "run", "c1", `{"sessionId":"session-a","command":"ls"}`)
	if err == nil || !strings.Contains(err.Error(), "no renderer requester is wired") {
		t.Fatalf("error = %v, want the wiring-gap refusal", err)
	}
}

// TestExecuteRun_WindowIsHonest is design §4.4's window contract on the run
// return: total (the block's output line count), the window that was asked
// for (run asks for the whole output — [0, total)), the window that was
// actually returned (the renderer clamps to what it can carry — a long
// output is answered honestly, never as an error), and the text.
func TestExecuteRun_WindowIsHonest(t *testing.T) {
	runner := agenttools.NewRunner([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	// The block holds five output lines; the renderer returns the first
	// three — the window statement tells the model the rest exists.
	req := &recordingRunner{body: runResolvedBody("e1", new(0), "success", 5, 0, 3, "one\ntwo\nthree")}

	out, err := executeRun(context.Background(), runner, req, json.RawMessage(`{"sessionId":"session-a","command":"seq 5"}`))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var res struct {
		Total  int `json:"total"`
		Window struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"window"`
		Returned struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"returned"`
		Text     string `json:"text"`
		ExitCode *int   `json:"exitCode"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.Total != 5 {
		t.Errorf("total = %d, want 5 (the block's output line count)", res.Total)
	}
	if res.Window.Start != 0 || res.Window.End != 5 {
		t.Errorf("window = [%d,%d), want the asked [0,5)", res.Window.Start, res.Window.End)
	}
	if res.Returned.Start != 0 || res.Returned.End != 3 {
		t.Errorf("returned = [%d,%d), want the renderer's actual [0,3)", res.Returned.Start, res.Returned.End)
	}
	if res.Text != "one\ntwo\nthree" {
		t.Errorf("text = %q, want one\\ntwo\\nthree", res.Text)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", res.ExitCode)
	}
	if res.Status != "success" {
		t.Errorf("status = %q, want success", res.Status)
	}
}

// TestExecuteRun_EnteredCarriesNoExitCode: a command that froze as "entered"
// (an environment transition — the local `ssh` block freezes with no exit
// code) reports its status honestly and a null exit code, never a made-up
// one.
func TestExecuteRun_EnteredCarriesNoExitCode(t *testing.T) {
	runner := agenttools.NewRunner([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	req := &recordingRunner{body: runResolvedBody("e-ssh", nil, "entered", 1, 0, 1, "deploy@host:~$")}

	out, err := executeRun(context.Background(), runner, req, json.RawMessage(`{"sessionId":"session-a","command":"ssh host"}`))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var res struct {
		ExitCode *int   `json:"exitCode"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if res.ExitCode != nil {
		t.Errorf("exitCode = %v, want null for an entered block", *res.ExitCode)
	}
	if res.Status != "entered" {
		t.Errorf("status = %q, want entered", res.Status)
	}
}

// TestExecuteRun_FailedOutcomeSurfaces: a renderer that refuses or fails the
// submission surfaces as a tool error — the model hears why, and the run is
// not left hanging (the broker's honest terminal answer crosses as an error,
// not a hang).
func TestExecuteRun_FailedOutcomeSurfaces(t *testing.T) {
	runner := agenttools.NewRunner([]content.GrantScope{{Kind: content.ResourceSession, ID: "session-a"}})
	req := &recordingRunner{err: errors.New("run: the renderer refused the submission: the agent lane is not prompt-ready")}

	_, err := executeRun(context.Background(), runner, req, json.RawMessage(`{"sessionId":"session-a","command":"ls"}`))
	if err == nil || !strings.Contains(err.Error(), "refused the submission") {
		t.Fatalf("error = %v, want the renderer's failure sentence", err)
	}
}
