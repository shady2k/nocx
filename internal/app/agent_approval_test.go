package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// The question is put on a goroutine of its own now (nocx-t7xds), so this
// double is read from the test and written from that goroutine. Under a lock,
// like anything else two goroutines share — `go test -race` names it
// otherwise, and a test that races is a test that reports at random.
type recordingRequester struct {
	mu     sync.Mutex
	asks   []transport.HostAsk
	answer bool
}

func (r *recordingRequester) RequestHost(_ context.Context, ask transport.HostAsk) (transport.HostAnswer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks = append(r.asks, ask)
	return transport.HostAnswer{Approved: r.answer}, nil
}

func (r *recordingRequester) seen() []transport.HostAsk {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transport.HostAsk(nil), r.asks...)
}

func (r *recordingRequester) answerWith(approved bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = approved
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

	// Each start raises its own question and refuses this enrolment
	// (nocx-t7xds): the person answers, and the NEXT start enrols. What is
	// under test is that the second agent gets a question of its own.
	first := fakeAgent(t, "claude")
	admit(t, svc, "pane-a", first)
	second := fakeAgent(t, "codex")
	admit(t, svc, "pane-b", second)

	if len(requester.seen()) != 2 {
		t.Fatalf("a second, different agent was admitted after %d question(s), want 2 — "+
			"the person answered about %s and never saw %s", len(requester.seen()), first, second)
	}
	if !strings.Contains(requester.seen()[1].Executable, second) {
		t.Fatalf("the second question named %q, want the agent being admitted, %s",
			requester.seen()[1].Executable, second)
	}
}

// The question names what the person typed. A path they cannot recognise is
// the same as no path at all: D13 asks for a human who SEES the executable.
func TestTheQuestionNamesTheAgentAndNotTheShell(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)

	agent := fakeAgent(t, "claude")
	admit(t, svc, "pane-a", agent)
	if len(requester.seen()) != 1 {
		t.Fatalf("asked %d times, want 1", len(requester.seen()))
	}
	if !strings.Contains(requester.seen()[0].Executable, agent) {
		t.Fatalf("the question named %q, want the agent %s", requester.seen()[0].Executable, agent)
	}
	if requester.seen()[0].Workspace != "workspace:default" {
		t.Fatalf("the question named workspace %q, want the one the answer covers",
			requester.seen()[0].Workspace)
	}
	// The digest travels as its own fact, so the renderer can give it a row
	// and say what it is for (nocx-fu18z).
	if len(requester.seen()[0].Digest) != 64 {
		t.Fatalf("the question carried digest %q, want a sha256", requester.seen()[0].Digest)
	}
}

// What the durable answer IS for: the same agent, typed again, is not a second
// question. This is the half that must survive the fix.
func TestTheSameAgentIsNotAskedTwice(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)

	agent := fakeAgent(t, "claude")
	admit(t, svc, "pane-a", agent)
	if err := svc.Approve(context.Background(), session.ID("pane-b"), agent); err != nil {
		t.Fatalf("a second pane was asked again about %s: %v", agent, err)
	}
	if len(requester.seen()) != 1 {
		t.Fatalf("asked %d times for one agent, want 1", len(requester.seen()))
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
	waitFor(t, func() bool { return len(requester.seen()) == 1 })
	if err := svc.Approve(context.Background(), session.ID("pane-b"), refused); err == nil {
		t.Fatal("a refused agent was admitted on a second pane")
	}
	if len(requester.seen()) != 1 {
		t.Fatalf("a refusal was re-asked: %d questions, want 1", len(requester.seen()))
	}
	// A different agent is a different question, not an inherited answer.
	other := fakeAgent(t, "codex")
	requester.answerWith(true)
	admit(t, svc, "pane-c", other)
}

// admit puts an agent through the two starts the product now takes: the first
// raises the question and refuses, the person answers, the second enrols. It
// fails the test if the second start is still refused.
func admit(t *testing.T, svc *agentApprovalService, sid session.ID, agent string) {
	t.Helper()
	if err := svc.Approve(context.Background(), sid, agent); err == nil {
		t.Fatalf("the first start of %s enrolled without anybody answering", agent)
	}
	// Waited on the ANSWER landing rather than by re-calling Approve: a retry
	// while the question is outstanding is a legitimate thing for a person to
	// do and must not be how this helper polls.
	waitFor(t, func() bool { return svc.answered(agent) })
	if err := svc.Approve(context.Background(), sid, agent); err != nil {
		t.Fatalf("the start after the answer was still refused for %s: %v", agent, err)
	}
}

// A test-only read of whether the store now holds an answer for this agent,
// so the helper above waits on the fact rather than on a side effect.
func (s *agentApprovalService) answered(agent string) bool {
	executable, err := agentapproval.IdentityForExecutable(agent)
	if err != nil {
		return false
	}
	_, ok := s.store.Lookup(executable, s.scope)
	return ok
}

// Starting an agent again while its question is still on screen must not
// queue a second copy of it: same decision, same dialog, and a stack of
// identical questions is how a person clicks one without reading it.
func TestARetryWhileTheQuestionIsUpDoesNotAskTwice(t *testing.T) {
	requester := &blockingRequester{asked: make(chan transport.HostAsk, 4), release: make(chan struct{})}
	svc := approvalServiceForTest(t, requester)
	agent := fakeAgent(t, "claude")

	for i := 0; i < 3; i++ {
		if err := svc.Approve(context.Background(), session.ID("pane-a"), agent); err == nil {
			t.Fatal("an unanswered identity was enrolled")
		}
	}
	select {
	case <-requester.asked:
	case <-time.After(2 * time.Second):
		t.Fatal("no question was raised at all")
	}
	select {
	case ask := <-requester.asked:
		t.Fatalf("a second question was raised for the same agent: %+v", ask)
	case <-time.After(200 * time.Millisecond):
	}
	close(requester.release)
}

// THE HANDSHAKE MUST NOT WAIT ON A PERSON (nocx-t7xds). The shell gives an
// enrolment five seconds (nocx.bash __nocx_lc_grant_timeout_s); the approval
// question has no deadline at all, deliberately, because it waits on somebody
// reading it. So an unanswered identity must return AT ONCE with a reason
// that says a question is up, and the ask must still go out.
type blockingRequester struct {
	asked   chan transport.HostAsk
	release chan struct{}
}

func (r *blockingRequester) RequestHost(ctx context.Context, ask transport.HostAsk) (transport.HostAnswer, error) {
	r.asked <- ask
	select {
	case <-r.release:
		return transport.HostAnswer{Approved: true}, nil
	case <-ctx.Done():
		return transport.HostAnswer{}, ctx.Err()
	}
}

func TestAnUnansweredIdentityDoesNotBlockTheEnrolment(t *testing.T) {
	requester := &blockingRequester{asked: make(chan transport.HostAsk, 1), release: make(chan struct{})}
	svc := approvalServiceForTest(t, requester)
	agent := fakeAgent(t, "claude")

	done := make(chan error, 1)
	go func() { done <- svc.Approve(context.Background(), session.ID("pane-a"), agent) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an identity nobody has answered for was enrolled")
		}
		// The sentence is what the pane prints, so it must say what is
		// happening rather than that something timed out.
		if !strings.Contains(err.Error(), "asking") {
			t.Fatalf("refusal reads %q; want it to say a question is up", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Approve blocked on the person: the shell would have given up at five seconds")
	}

	// And the question still went out, or the person would have nothing to
	// answer and the next start would refuse for ever.
	select {
	case ask := <-requester.asked:
		if !strings.Contains(ask.Executable, agent) {
			t.Fatalf("the ask named %q, want the agent %s", ask.Executable, agent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no question was raised, so nothing can ever be answered")
	}
	close(requester.release)
}

// And the answer, once given, is recorded — so the next start enrols with no
// question at all. This is the half that makes the refusal above acceptable.
func TestTheAnswerGivenLateIsStillRecorded(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)
	agent := fakeAgent(t, "claude")

	if err := svc.Approve(context.Background(), session.ID("pane-a"), agent); err == nil {
		t.Fatal("the first enrolment did not refuse while the question was unanswered")
	}
	// The recording requester answers at once, so by the time the ask has
	// been seen the answer is stored.
	waitFor(t, func() bool { return len(requester.seen()) == 1 })
	waitFor(t, func() bool {
		return svc.Approve(context.Background(), session.ID("pane-b"), agent) == nil
	})
	if len(requester.seen()) != 1 {
		t.Fatalf("the second start asked again: %d questions, want 1", len(requester.seen()))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
