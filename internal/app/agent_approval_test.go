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
	"github.com/shady2k/nocx/internal/ssh"
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

// localMachineFacts is the machine the backend runs on, as the wire spells it.
// The stands in this package open local panes unless a test says otherwise, so
// this is the machine their answers are given for.
func localMachineFacts() transport.MachineFacts {
	return transport.MachineFacts{Kind: string(agentapproval.DomainLocal)}
}

// sshMachineFacts is one ssh host, as the wire spells it.
func sshMachineFacts(host, account, hostKey string) transport.MachineFacts {
	return transport.MachineFacts{
		Kind: string(agentapproval.DomainSSH), Host: host, Account: account, HostKey: hostKey,
	}
}

// approvalSessions gives every pane the SAME owned process, which is what two
// panes on one machine really have: one shell binary, one digest. A test about
// a MACHINE sets sess, and one that does not care gets a local pane — the
// domain of the machine this backend runs on.
type approvalSessions struct {
	pid  int
	sess session.Session
}

func (approvalSessions) List() []session.Session { return nil }
func (s approvalSessions) OwnedProcessPID(session.ID) (int, bool) {
	return s.pid, s.pid > 0
}

func (s approvalSessions) Get(session.ID) (session.Session, error) {
	if s.sess != nil {
		return s.sess, nil
	}
	// A seam with a pid is a live local pane, which is what the original
	// fixture meant. A seam with NEITHER holds nothing: answering a local pane
	// for an id it was never given would hide a derivation that invents a
	// machine, which is the one thing this seam exists to make visible.
	if s.pid <= 0 {
		return nil, errors.New("no such session")
	}
	return workerAuthSessionOverride{kind: session.KindLocal}, nil
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

// approvalServiceOnMachine is approvalServiceOver for a pane on a NAMED machine.
// The domain an answer is keyed by is derived from this session and from
// nothing else, so a test about machines is a test about which session the
// backend was shown. Two of these over one docs are one person's two panes.
func approvalServiceOnMachine(t *testing.T, docs *approvalDocStore, requester hostApprovalRequester, machine session.Session) *agentApprovalService {
	t.Helper()
	store := agentapproval.NewStore(log.NewSlogAdapter(nil), docs, "agent-approvals.json")
	svc := newAgentApprovalService(approvalSessions{pid: os.Getpid(), sess: machine}, store, "workspace:default")
	svc.SetRequester(requester)
	return svc
}

// localPane is a pane on the machine the backend runs on.
func localPane() session.Session { return workerAuthSessionOverride{kind: session.KindLocal} }

// sshPane is a pane on a machine reached over ssh: the host, the account its
// connection authenticated as, and the host key it was accepted under — the
// three facts the domain is derived from.
func sshPane(host, account, hostKey string) session.Session {
	return workerAuthSessionOverride{
		kind:        session.KindRemote,
		host:        host,
		sshOpts:     []ssh.ConnectOption{ssh.WithUser(account)},
		fingerprint: hostKey,
	}
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

// A test-only read of whether the store now holds an answer for this agent on
// the machine the test's pane is in — local, which is what approvalSessions'
// default pane is.
func (s *agentApprovalService) answered(agent string) bool {
	executable, err := agentapproval.IdentityForExecutable(agent)
	if err != nil {
		return false
	}
	_, ok := s.store.Lookup(executable, agentapproval.LocalDomain(), s.scope)
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

// ── the trust domain (nocx-50w7p.16) ────────────────────────────────────────
//
// One person, two panes, ONE document — and the same executable typed in both.
// Every case below answers YES for the first machine and then asks the second
// for the same agent, so a key that could not tell machines apart would admit
// the second in silence. Each case is paired with the machine the person DID
// answer about still being admitted, which is what makes the second machine's
// question a separation rather than a store that lost the answer.

func TestAYesForOneMachineDoesNotAdmitAnAgentOnAnother(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  session.Session
		second session.Session
		why    string
	}{
		{
			"local does not stand for an ssh host", localPane(), sshPane("build.example.com", "deploy", "SHA256:key-a"),
			"the same path and bytes on another machine is the same key without a domain",
		},
		{
			"host A does not stand for host B", sshPane("build.example.com", "deploy", "SHA256:key-a"),
			sshPane("other.example.com", "deploy", "SHA256:key-b"), "two hosts, one account",
		},
		{
			"one account does not stand for another on one host", sshPane("build.example.com", "deploy", "SHA256:key-a"),
			sshPane("build.example.com", "root", "SHA256:key-a"), "two accounts on one host",
		},
		{
			"a machine whose host key changed does not stand for itself", sshPane("build.example.com", "deploy", "SHA256:key-a"),
			sshPane("build.example.com", "deploy", "SHA256:key-c"), "a changed key is a different answer to give",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := &approvalDocStore{}
			agent := fakeAgent(t, "claude")
			first := approvalServiceOnMachine(t, docs, &recordingRequester{answer: true}, tc.first)
			second := approvalServiceOnMachine(t, docs, &recordingRequester{answer: true}, tc.second)

			// The person answers about the FIRST machine, all the way through.
			admit(t, first, session.ID("pane-first"), agent)

			// PAIRED SUCCESS — the machine they answered about is still admitted.
			if err := first.Approve(context.Background(), session.ID("pane-first-again"), agent); err != nil {
				t.Fatalf("the machine the person answered about was refused: %v — %s", err, tc.why)
			}

			// THE CRITERION — the second machine is ASKED, not admitted. A wait
			// is the only verdict that says "no answer of yours covers this";
			// an admitted nil is the defect this test exists for.
			pending := pendingOf(t, second.Approve(context.Background(), session.ID("pane-second"), agent))
			if pending.Reason == "" {
				t.Fatal("the second machine was asked with no reason given, so a pane cannot say what it waits on")
			}
			// The person answers about the SECOND machine too, and the answer is
			// kept: the question is closed only after the record is written.
			if reason := settledOf(t, pending); reason != "" {
				t.Fatalf("the question about the second machine closed with %q, want the person's answer", reason)
			}
			secondRequester, isRecording := second.requester.(*recordingRequester)
			if !isRecording {
				t.Fatal("the standing questioner is not the recording one, so this test cannot see the question")
			}
			ask := secondRequester.seen()
			if len(ask) != 1 {
				t.Fatalf("the second machine asked %d questions, want 1", len(ask))
			}
			// And the question names the machine it is for, so the answer the
			// person gives is one about that machine.
			wantDomain, err := sessionDomain(approvalSessions{sess: tc.second}, session.ID("pane-second"))
			if err != nil {
				t.Fatalf("the second machine has no derivable domain: %v", err)
			}
			if got := ask[0].Machine; got != machineFacts(wantDomain) {
				t.Fatalf("the question named machine %+v, want %+v", got, machineFacts(wantDomain))
			}
			// PAIRED SUCCESS on the second machine: now that the person has
			// answered about IT, it admits — and the two answers coexist, each
			// bound to its own machine.
			if err := second.Approve(context.Background(), session.ID("pane-second-again"), agent); err != nil {
				t.Fatalf("the machine the person then answered about was refused: %v", err)
			}
			if rows := second.ListAgentAccess(); len(rows) != 2 {
				t.Fatalf("after two machines were answered about, the read-back has %d rows, want 2", len(rows))
			}
		})
	}
}

// The question a person is asked names the machine, because the answer is kept
// for that machine and for no other.
func TestTheQuestionNamesTheMachineItIsFor(t *testing.T) {
	requester := &recordingRequester{answer: true}
	ssh := sshPane("build.example.com", "deploy", "SHA256:key-a")
	svc := approvalServiceOnMachine(t, &approvalDocStore{}, requester, ssh)
	agent := fakeAgent(t, "claude")

	admit(t, svc, session.ID("pane-a"), agent)

	ask := requester.seen()
	if len(ask) != 1 {
		t.Fatalf("asked %d times, want 1", len(ask))
	}
	got := ask[0].Machine
	if got.Kind != string(agentapproval.DomainSSH) || got.Host != "build.example.com" ||
		got.Account != "deploy" || got.HostKey != "SHA256:key-a" {
		t.Fatalf("the question named machine %+v, want the ssh machine the pane is on", got)
	}

	// And a LOCAL pane says so rather than saying nothing: an absent machine
	// would be a second empty spelling of local for a surface to know about.
	localRequester := &recordingRequester{answer: true}
	localSvc := approvalServiceOnMachine(t, &approvalDocStore{}, localRequester, localPane())
	admit(t, localSvc, session.ID("pane-b"), fakeAgent(t, "codex"))
	if got := localRequester.seen()[0].Machine; got.Kind != string(agentapproval.DomainLocal) ||
		got.Host != "" || got.Account != "" || got.HostKey != "" {
		t.Fatalf("a local pane's question named machine %+v, want local and nothing else", got)
	}
}

// The read-back carries the machine, so two rows for one executable can be told
// apart — and the row a person clicks is the row that is forgotten.
func TestTheReadBackNamesTheMachineAndRevokesThatRowAlone(t *testing.T) {
	docs := &approvalDocStore{}
	agent := fakeAgent(t, "claude")
	localSvc := approvalServiceOnMachine(t, docs, &recordingRequester{answer: true}, localPane())
	sshSvc := approvalServiceOnMachine(t, docs, &recordingRequester{answer: true}, sshPane("build.example.com", "deploy", "SHA256:key-a"))
	admit(t, localSvc, session.ID("pane-local"), agent)
	admit(t, sshSvc, session.ID("pane-ssh"), agent)

	records := sshSvc.ListAgentAccess()
	if len(records) != 2 {
		t.Fatalf("read-back showed %d rows for one executable on two machines, want 2", len(records))
	}
	// Selected by DOMAIN, not by position: the store orders rows by path and
	// digest first, and both rows share those, so an index would be asserting
	// the sort rather than the machine.
	var sshRecord, localRecord *transport.AgentAccessRecord
	for i := range records {
		switch records[i].Machine.Kind {
		case string(agentapproval.DomainSSH):
			sshRecord = &records[i]
		case string(agentapproval.DomainLocal):
			localRecord = &records[i]
		}
	}
	if sshRecord == nil || localRecord == nil {
		t.Fatalf("read-back did not name both machines: %+v", records)
	}
	if sshRecord.Machine != sshMachineFacts("build.example.com", "deploy", "SHA256:key-a") {
		t.Fatalf("the ssh row names %+v, want the machine its pane is on", sshRecord.Machine)
	}

	// Revoke the ssh row BY ITS OWN FACTS. The local answer is not the row this
	// call names and must survive it — byte for byte, which is why the
	// surviving row is compared to the whole facts value.
	forgotten, err := sshSvc.ForgetAgentAccess(sshRecord.Executable, sshRecord.Digest, sshRecord.Workspace, sshRecord.Machine)
	if err != nil || !forgotten {
		t.Fatalf("revoke = %v, %v; want true, nil", forgotten, err)
	}
	remaining := sshSvc.ListAgentAccess()
	if len(remaining) != 1 {
		t.Fatalf("after revoking one machine's row, %d remain, want 1", len(remaining))
	}
	if remaining[0].Machine != localMachineFacts() {
		t.Fatalf("the surviving row names %+v, want the local machine that was not revoked", remaining[0].Machine)
	}
}

// The domain comes from the session's route and from nothing else, and a route
// that cannot name a machine is a refusal rather than a guess.
func TestTheDomainIsDerivedFromTheRouteOrRefused(t *testing.T) {
	local, err := sessionDomain(approvalSessions{sess: localPane()}, "pane")
	if err != nil || local != agentapproval.LocalDomain() {
		t.Fatalf("local domain = %+v, %v; want the local domain", local, err)
	}

	sshDomain, err := sessionDomain(approvalSessions{sess: sshPane("h.example", "deploy", "SHA256:k")}, "pane")
	if err != nil {
		t.Fatalf("ssh domain: %v", err)
	}
	if sshDomain.Kind != agentapproval.DomainSSH || sshDomain.Host != "h.example" ||
		sshDomain.Account != "deploy" || sshDomain.HostKey != "SHA256:k" {
		t.Fatalf("ssh domain = %+v, want the pane's own route", sshDomain)
	}

	// An ssh pane whose route cannot name a machine keys nothing — and the
	// refusal is a sentence, because it is printed in the person's own pane.
	for _, partial := range []session.Session{
		sshPane("", "deploy", "SHA256:k"),
		sshPane("h.example", "", "SHA256:k"),
		sshPane("h.example", "deploy", ""),
	} {
		if _, err := sessionDomain(approvalSessions{sess: partial}, "pane"); err == nil {
			t.Fatal("a pane whose route cannot name a machine was given a domain anyway")
		} else if !strings.Contains(err.Error(), "machine") {
			t.Fatalf("refusal = %q, want a sentence naming what could not be told", err)
		}
	}

	// A registry that does not hold the pane refuses too, rather than deriving
	// a domain from nothing: approvalSessions with no pid and no session holds
	// no pane, which is what the production registry does for an id it has
	// never minted.
	if _, err := sessionDomain(approvalSessions{}, "pane"); err == nil {
		t.Fatal("an unknown pane was given a trust domain")
	} else if !strings.Contains(err.Error(), "machine") {
		t.Fatalf("refusal = %q, want a sentence naming what could not be told", err)
	}
}
