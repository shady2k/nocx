package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
)

// The approval service had no tests of its own, which is how the identity it
// records came to be one thing and the identity the dialog promises another
// (nocx-opiq5).

type approvalDocStore struct {
	docs map[string][]byte
	// failWrites is a disk that will not keep an answer.
	failWrites bool
}

func (s *approvalDocStore) Read(name string, into any) (bool, error) {
	raw, ok := s.docs[name]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, into)
}

func (s *approvalDocStore) Write(name string, doc any) error {
	if s.failWrites {
		return errors.New("the disk is full")
	}
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
	return approvalServiceOver(t, &approvalDocStore{}, requester)
}

func approvalServiceOver(t *testing.T, docs *approvalDocStore, requester hostApprovalRequester) *agentApprovalService {
	t.Helper()
	store := agentapproval.NewStore(log.NewSlogAdapter(nil), docs, "agent-approvals.json")
	svc := newAgentApprovalService(approvalSessions{pid: os.Getpid()}, store, "workspace:default")
	svc.SetRequester(requester)
	return svc
}

// pendingOf reads the verdict an unanswered identity gets: not a refusal but a
// wait, holding the channel that closes it (nocx-cyhfw).
func pendingOf(t *testing.T, err error) *lifecyclepub.EnrolmentPending {
	t.Helper()
	var pending *lifecyclepub.EnrolmentPending
	if !errors.As(err, &pending) {
		t.Fatalf("verdict = %v, want one that waits on the person", err)
	}
	return pending
}

// settledOf waits for the question behind a wait to close, and returns what it
// closed with: nothing when a person answered, a sentence when nobody could.
func settledOf(t *testing.T, pending *lifecyclepub.EnrolmentPending) string {
	t.Helper()
	select {
	case reason := <-pending.Settled:
		return reason
	case <-time.After(3 * time.Second):
		t.Fatal("the question never closed, so the pane waiting on it would wait for ever")
		return ""
	}
}

func isPending(err error) bool {
	var pending *lifecyclepub.EnrolmentPending
	return errors.As(err, &pending)
}

// THE DEFECT, stated as the thing it costs a person: they admitted ONE agent
// and every other agent came in behind it, in silence. The identity recorded
// was the pane's shell, and two panes on one machine run one shell binary at
// one path with one digest, so the store's key could not tell the two apart.
func TestApprovingOneAgentDoesNotAdmitAnother(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)

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
// After it the verdict is a refusal, never another wait: a person who said no
// is not asked again every time the agent starts.
func TestARefusalIsRememberedForThatAgentAlone(t *testing.T) {
	requester := &recordingRequester{answer: false}
	svc := approvalServiceForTest(t, requester)

	refused := fakeAgent(t, "claude")
	if reason := settledOf(t, pendingOf(t, svc.Approve(context.Background(), session.ID("pane-a"), refused))); reason != "" {
		t.Fatalf("the question closed with %q, want the person's answer", reason)
	}
	for _, pane := range []session.ID{"pane-a", "pane-b"} {
		err := svc.Approve(context.Background(), pane, refused)
		if err == nil {
			t.Fatalf("a refused agent was admitted on %s", pane)
		}
		if isPending(err) {
			t.Fatalf("a refused agent was asked again on %s instead of refused", pane)
		}
	}
	if len(requester.seen()) != 1 {
		t.Fatalf("a refusal was re-asked: %d questions, want 1", len(requester.seen()))
	}
	// A different agent is a different question, not an inherited answer.
	other := fakeAgent(t, "codex")
	requester.answerWith(true)
	admit(t, svc, "pane-c", other)
}

// admit puts an agent through what a start now is: the first enrolment waits
// on the question, the person answers, and the enrolment after the answer is
// admitted. It fails the test if that second enrolment is not.
func admit(t *testing.T, svc *agentApprovalService, sid session.ID, agent string) {
	t.Helper()
	pending := pendingOf(t, svc.Approve(context.Background(), sid, agent))
	if reason := settledOf(t, pending); reason != "" {
		t.Fatalf("the question about %s closed with %q, want an answer", agent, reason)
	}
	if err := svc.Approve(context.Background(), sid, agent); err != nil {
		t.Fatalf("the start after the answer was still refused for %s: %v", agent, err)
	}
}

// A test-only read of whether the store now holds an answer for this agent.
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
// identical questions is how a person clicks one without reading it. And
// every pane that waited is told when it closes, not only the one that raised
// it — each of them has a person's command held behind that one answer.
func TestARetryWhileTheQuestionIsUpDoesNotAskTwice(t *testing.T) {
	requester := &blockingRequester{asked: make(chan transport.HostAsk, 4), release: make(chan struct{})}
	svc := approvalServiceForTest(t, requester)
	agent := fakeAgent(t, "claude")

	var waits []*lifecyclepub.EnrolmentPending
	for i := 0; i < 3; i++ {
		pane := session.ID(fmt.Sprintf("pane-%d", i))
		waits = append(waits, pendingOf(t, svc.Approve(context.Background(), pane, agent)))
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
	for i, wait := range waits {
		if reason := settledOf(t, wait); reason != "" {
			t.Fatalf("pane-%d's question closed with %q, want the answer", i, reason)
		}
	}
}

// THE HANDSHAKE MUST NOT WAIT ON A PERSON (nocx-t7xds). The shell gives an
// enrolment five seconds (nocx.bash __nocx_lc_grant_timeout_s); the approval
// question has no deadline at all, deliberately, because it waits on somebody
// reading it. So an unanswered identity must return AT ONCE, with a wait that
// says a question is up, and the ask must still go out.
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
		// The sentence is what the pane prints while it waits, so it must
		// say what is happening rather than that something timed out.
		if pending := pendingOf(t, err); !strings.Contains(pending.Reason, "asking") {
			t.Fatalf("the wait reads %q; want it to say a question is up", pending.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Approve blocked on the person: the shell would have given up at five seconds")
	}

	// And the question still went out, or the person would have nothing to
	// answer and the pane would wait for ever.
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

// And the answer, once given, is recorded before the question closes — so
// the enrolment the shell sends on the close is admitted with no question at
// all, on this pane or any other.
func TestTheAnswerGivenLateIsStillRecorded(t *testing.T) {
	requester := &recordingRequester{answer: true}
	svc := approvalServiceForTest(t, requester)
	agent := fakeAgent(t, "claude")

	if reason := settledOf(t, pendingOf(t, svc.Approve(context.Background(), session.ID("pane-a"), agent))); reason != "" {
		t.Fatalf("the question closed with %q, want the answer", reason)
	}
	if err := svc.Approve(context.Background(), session.ID("pane-b"), agent); err != nil {
		t.Fatalf("the start after the answer was refused: %v", err)
	}
	if len(requester.seen()) != 1 {
		t.Fatalf("the second start asked again: %d questions, want 1", len(requester.seen()))
	}
}

// failingRequester is a client that cannot put a question on screen.
type failingRequester struct {
	mu   sync.Mutex
	asks int
}

func (r *failingRequester) RequestHost(context.Context, transport.HostAsk) (transport.HostAnswer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks++
	return transport.HostAnswer{}, errors.New("no window is open to ask in")
}

// A question nobody could be shown closes WITH A SENTENCE, so the pane waiting
// on it starts the agent without tools and says why. Closing it as answered
// would send the shell back to enrol, find no answer, and wait again on a
// question that can never appear. And nothing is recorded: an unanswerable ask
// is not a "no" the person said (nocx-6jbad), so the next start asks afresh.
func TestAQuestionNobodyCouldBeShownClosesWithAReason(t *testing.T) {
	svc := approvalServiceForTest(t, &failingRequester{})
	agent := fakeAgent(t, "claude")

	reason := settledOf(t, pendingOf(t, svc.Approve(context.Background(), session.ID("pane-a"), agent)))
	if !strings.Contains(reason, "no window is open to ask in") {
		t.Fatalf("the question closed with %q, want the reason nobody could be asked", reason)
	}
	if svc.answered(agent) {
		t.Fatal("a question nobody saw was recorded as an answer")
	}
	if again := pendingOf(t, svc.Approve(context.Background(), session.ID("pane-a"), agent)); again.Reason == "" {
		t.Fatal("the next start waited on a question that says nothing about what it asks")
	}
}

// An answer that could not be kept closes with a sentence too, for the same
// reason: closed as answered, the shell would enrol, find nothing stored, and
// put the question the person just answered back on screen.
func TestAnAnswerThatCouldNotBeKeptClosesWithAReason(t *testing.T) {
	svc := approvalServiceOver(t, &approvalDocStore{failWrites: true}, &recordingRequester{answer: true})
	agent := fakeAgent(t, "claude")

	reason := settledOf(t, pendingOf(t, svc.Approve(context.Background(), session.ID("pane-a"), agent)))
	if !strings.Contains(reason, "the disk is full") {
		t.Fatalf("the question closed with %q, want the reason the answer was not kept", reason)
	}
}

// waitFor polls an OBSERVABLE state until it holds, and fails the test on the
// deadline. The deadline is a HANG LIMIT and not the answer: what is being
// waited for is a state change, and every caller names it, so a timeout says
// which one never happened.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
