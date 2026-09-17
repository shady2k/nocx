package toolendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

type endpointAttemptLedger struct {
	mu          sync.Mutex
	calls       []string
	submissions []content.SubmitEntry
	finished    []content.FinishExecution
}

func (l *endpointAttemptLedger) EnsureEnvironment(context.Context, content.Environment) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "ensure-env")
	return nil
}

func (l *endpointAttemptLedger) RecordObservation(context.Context, content.Observation) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "observe")
	return 1, nil
}

func (l *endpointAttemptLedger) Submit(_ context.Context, in content.SubmitEntry) (content.SubmitResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "submit:"+in.Intent)
	l.submissions = append(l.submissions, in)
	return content.SubmitResult{ID: "endpoint-entry"}, nil
}

func (l *endpointAttemptLedger) StartExecution(_ context.Context, in content.StartExecution) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "start:"+in.EntryID)
	return 1, nil
}

func (l *endpointAttemptLedger) FinishExecution(_ context.Context, _ int64, in content.FinishExecution) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "finish:"+string(in.Status))
	l.finished = append(l.finished, in)
	return nil
}

func (l *endpointAttemptLedger) AddCause(context.Context, string, string) (int, error) {
	return 0, nil
}

func (l *endpointAttemptLedger) CaptureOutput(context.Context, content.CaptureOutput) (bool, error) {
	return true, nil
}

type endpointOutcomeDispatcher struct{}

func (endpointOutcomeDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	if inv.Method == "workers.nope" {
		return "", assistant.ErrUnknownMethod
	}
	return "", assistant.ErrUnreachableMethod
}
func (endpointOutcomeDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

func TestEndpointRefusalLeavesAuditableAttempt(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	ledger := &endpointAttemptLedger{}
	dispatcher, err := assistant.NewAttemptRecordingDispatcher(
		registry,
		&endpointOutcomeDispatcher{},
		ledger,
	)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}
	auth := &testAuthorizer{inv: assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{Session: "session-1"},
		Grant:      contractGrant(),
	}}
	ep := startEndpoint(t, Config{
		Dir:      filepath.Join(t.TempDir(), "runtime"),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	// The reason is asserted by what it must SAY rather than word for word:
	// these sentences are written for an agent to act on and are expected to
	// be improved, while what each refusal is ABOUT must not drift. The
	// vocabulary itself is pinned in errors_test.go.
	cases := []struct {
		id, method, says string
		code             int
	}{
		{"refused", "workers.holdings", "not offered to this session", rpcDomainError},
		{"unknown", "workers.nope", "no tool by that name", rpcMethodNotFound},
	}
	for _, tc := range cases {
		conn := dialEndpoint(t, ep)
		request := `{"jsonrpc":"2.0","id":"` + tc.id + `","method":"` + tc.method + `","params":{}}` + "\n"
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatalf("write %s request: %v", tc.method, err)
		}
		response := readResponse(t, conn)
		if response.Error == nil || response.Error.Code != tc.code {
			t.Fatalf("%s response error = %+v, want code %d", tc.method, response.Error, tc.code)
		}
		if response.Error.Data == nil || !strings.Contains(response.Error.Data.Reason, tc.says) {
			t.Fatalf("%s response refusal data = %+v, want a reason saying %q", tc.method, response.Error.Data, tc.says)
		}
		_ = conn.Close()
	}

	ledger.mu.Lock()
	calls := append([]string(nil), ledger.calls...)
	submissions := append([]content.SubmitEntry(nil), ledger.submissions...)
	finished := append([]content.FinishExecution(nil), ledger.finished...)
	ledger.mu.Unlock()
	wantCalls := []string{
		"ensure-env", "observe", "submit:workers.holdings", "start:endpoint-entry", "finish:failure",
		"ensure-env", "observe", "submit:workers.nope", "start:endpoint-entry", "finish:failure",
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("ledger calls = %v, want %v", calls, wantCalls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Fatalf("ledger call %d = %q, want %q; all calls = %v", i, calls[i], wantCalls[i], calls)
		}
	}
	if len(submissions) != 2 || len(finished) != 2 {
		t.Fatalf("submissions/finished = %d/%d, want 2/2", len(submissions), len(finished))
	}
	for i, want := range []struct {
		intent string
		effect content.Effect
	}{
		{"workers.holdings", content.EffectObserve},
		{"workers.nope", ""},
	} {
		if submissions[i].Intent != want.intent {
			t.Fatalf("submission %d intent = %q, want %q", i, submissions[i].Intent, want.intent)
		}
		var payload struct {
			Tool   string         `json:"tool"`
			Effect content.Effect `json:"effect"`
			RunID  string         `json:"runId"`
		}
		if err := json.Unmarshal([]byte(submissions[i].Payload), &payload); err != nil {
			t.Fatalf("attempt payload %d = %q: %v", i, submissions[i].Payload, err)
		}
		if payload.Tool != want.intent || payload.Effect != want.effect || payload.RunID != "" {
			t.Fatalf("attempt payload %d = %+v, want tool %q/effect %q/no run id", i, payload, want.intent, want.effect)
		}
	}
}

func TestEndpointAdmissionRefusalLeavesNoAttempt(t *testing.T) {
	registry, err := agenttools.Assemble(os.DirFS("../../contracts/tools"))
	if err != nil {
		t.Fatalf("assemble worker tools: %v", err)
	}
	ledger := &endpointAttemptLedger{}
	dispatcher, err := assistant.NewAttemptRecordingDispatcher(
		registry,
		&endpointOutcomeDispatcher{},
		ledger,
	)
	if err != nil {
		t.Fatalf("new attempt-recording dispatcher: %v", err)
	}
	auth := &testAuthorizer{err: errors.New("admission refused"), invoked: make(chan struct{}, 1)}
	ep := startEndpoint(t, Config{
		Dir:      filepath.Join(t.TempDir(), "runtime"),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     auth,
		Dispatch: dispatcher,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	conn := dialEndpoint(t, ep)
	<-auth.invoked
	response := readResponse(t, conn)
	if response.Error == nil || response.Error.Code != rpcInternalError {
		t.Fatalf("response error = %+v, want internal refusal for arbitrary admission error", response.Error)
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if len(ledger.calls) != 0 || len(ledger.submissions) != 0 || len(ledger.finished) != 0 {
		t.Fatalf("ledger writes after admission refusal = calls %v, submissions %d, finished %d; want none", ledger.calls, len(ledger.submissions), len(ledger.finished))
	}
}
