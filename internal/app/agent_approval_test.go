package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
)

// The approval service had no tests of its own, which is how the identity it
// records came to be one thing and the identity the dialog promises another
// (nocx-opiq5).

type approvalDocStore struct{ docs map[string][]byte }

func (s *approvalDocStore) Read(name string, into any) (bool, error) {
	raw, ok := s.docs[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, into)
}

func (s *approvalDocStore) Write(name string, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if s.docs == nil {
		s.docs = map[string][]byte{}
	}
	s.docs[name] = raw
	return nil
}

func (s *approvalDocStore) Delete(string) error     { return nil }
func (s *approvalDocStore) List() ([]string, error) { return nil, nil }

var _ storage.DocumentStore = (*approvalDocStore)(nil)

// approvalSessions gives every pane the SAME owned process, which is what two
// panes on one machine really have: one shell binary, one digest.
type approvalSessions struct{ pid int }

func (approvalSessions) List() []session.Session { return nil }
func (s approvalSessions) OwnedProcessPID(session.ID) (int, bool) {
	return s.pid, s.pid > 0
}

type recordingRequester struct {
	asks   []transport.HostAsk
	answer bool
}

func (r *recordingRequester) RequestHost(_ context.Context, ask transport.HostAsk) (transport.HostAnswer, error) {
	r.asks = append(r.asks, ask)
	return transport.HostAnswer{Approved: r.answer}, nil
}

// A file that exists and can be digested: IdentityForExecutable refuses to
// guess an identity from a command's spelling, so the agent must be real.
func fakeAgent(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	// 0o600, not 0o755: the identity is the file's BYTES. IdentityForPath
	// reads and digests it, and an absolute agent name never reaches
	// exec.LookPath, so the execute bit decides nothing here.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"+name), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func approvalServiceForTest(t *testing.T, requester hostApprovalRequester) *agentApprovalService {
	t.Helper()
	store := agentapproval.NewStore(log.NewSlogAdapter(nil), &approvalDocStore{}, "agent-approvals.json")
	svc := newAgentApprovalService(approvalSessions{pid: os.Getpid()}, store, "workspace:default")
	svc.SetRequester(requester)
	return svc
}

// THE DEFECT, stated as the thing it costs a person: they admitted ONE agent
// and every other agent came in behind it, in silence. The identity recorded
// was the pane's shell, and two panes on one machine run one shell binary at
// one path with one digest, so the store's key could not tell the two apart.
func TestApprovingOneAgentDoesNotAdmitAnother(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)

	first := fakeAgent(t, "claude")
	if err := svc.Approve(context.Background(), session.ID("pane-a"), first); err != nil {
		t.Fatalf("approving %s: %v", first, err)
	}
	second := fakeAgent(t, "codex")
	if err := svc.Approve(context.Background(), session.ID("pane-b"), second); err != nil {
		t.Fatalf("approving %s: %v", second, err)
	}

	if len(requester.asks) != 2 {
		t.Fatalf("a second, different agent was admitted after %d question(s), want 2 — "+
			"the person answered about %s and never saw %s", len(requester.asks), first, second)
	}
	if !strings.Contains(requester.asks[1].Executable, second) {
		t.Fatalf("the second question named %q, want the agent being admitted, %s",
			requester.asks[1].Executable, second)
	}
}

// The question names what the person typed. A path they cannot recognise is
// the same as no path at all: D13 asks for a human who SEES the executable.
func TestTheQuestionNamesTheAgentAndNotTheShell(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)

	agent := fakeAgent(t, "claude")
	if err := svc.Approve(context.Background(), session.ID("pane-a"), agent); err != nil {
		t.Fatalf("approving %s: %v", agent, err)
	}
	if len(requester.asks) != 1 {
		t.Fatalf("asked %d times, want 1", len(requester.asks))
	}
	if !strings.Contains(requester.asks[0].Executable, agent) {
		t.Fatalf("the question named %q, want the agent %s", requester.asks[0].Executable, agent)
	}
	if requester.asks[0].Workspace != "workspace:default" {
		t.Fatalf("the question named workspace %q, want the one the answer covers",
			requester.asks[0].Workspace)
	}
	// The digest travels as its own fact, so the renderer can give it a row
	// and say what it is for (nocx-fu18z).
	if len(requester.asks[0].Digest) != 64 {
		t.Fatalf("the question carried digest %q, want a sha256", requester.asks[0].Digest)
	}
}

// What the durable answer IS for: the same agent, typed again, is not a second
// question. This is the half that must survive the fix.
func TestTheSameAgentIsNotAskedTwice(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)

	agent := fakeAgent(t, "claude")
	for _, sid := range []session.ID{"pane-a", "pane-b"} {
		if err := svc.Approve(context.Background(), sid, agent); err != nil {
			t.Fatalf("approving %s in %s: %v", agent, sid, err)
		}
	}
	if len(requester.asks) != 1 {
		t.Fatalf("asked %d times for one agent, want 1", len(requester.asks))
	}
}

// A no is remembered, and it is remembered about the agent it was said of.
func TestARefusalIsRememberedForThatAgentAlone(t *testing.T) {
	requester := &recordingRequester{answer: false}
	svc := approvalServiceForTest(t, requester)

	refused := fakeAgent(t, "claude")
	if err := svc.Approve(context.Background(), session.ID("pane-a"), refused); err == nil {
		t.Fatal("a refused agent was admitted")
	}
	if err := svc.Approve(context.Background(), session.ID("pane-b"), refused); err == nil {
		t.Fatal("a refused agent was admitted on a second pane")
	}
	if len(requester.asks) != 1 {
		t.Fatalf("a refusal was re-asked: %d questions, want 1", len(requester.asks))
	}
	other := fakeAgent(t, "codex")
	requester.answer = true
	if err := svc.Approve(context.Background(), session.ID("pane-c"), other); err != nil {
		t.Fatalf("a different agent inherited the refusal: %v", err)
	}
}
