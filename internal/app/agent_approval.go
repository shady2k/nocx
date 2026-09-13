package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/transport"
)

const agentToolEndpointScopePrefix = "tool-endpoint:"

type hostApprovalRequester interface {
	RequestHost(context.Context, transport.HostAsk) (transport.HostAnswer, error)
}

// agentApprovalService joins durable human intent to the live process pin.
// The store answers whether this exact executable digest and scope was already
// decided; the requester is used only for an unanswered identity. A path-only
// identity would authorize a replaced binary, so the digest is displayed and
// keyed too. This does not defend against a same-UID process that can replace
// an approved executable and arrange the same digest; the kernel's (pid,
// start-time) pin remains the separate runtime defence.
//
// THE IDENTITY IS THE AGENT, NOT THE PANE'S SHELL (nocx-opiq5). It was the
// shell: the identity came from the session's owned process, which is the
// login shell nocx opened, so the key was /bin/bash's path and digest — the
// same in every pane on the machine. One yes therefore admitted every LATER
// agent silently (approve claude, and codex came in behind it), and one no
// refused every later agent with no question and no way to answer. The dialog
// meanwhile promised "this agent", and named a nix-store path to bash that no
// person could recognise as the thing they had typed. IdentityForExecutable
// existed for this and had no caller.
//
// What is now keyed is what the person was shown and typed: the executable the
// BACKEND resolves that agent name to. What this cannot verify is that the
// shell went on to exec that same file — the shell is inside the tree being
// granted, and a tree is the grant's unit by D13, so its claim about which
// agent it is launching is a claim. The live pin still proves the connecting
// peer belongs to a pane the person opened and enrolled; that is the fact this
// identity does not supply, and the reason both are required.
type agentApprovalService struct {
	sessions  workerAuthSessions
	store     *agentapproval.Store
	requester hostApprovalRequester
	scope     string
	workspace string

	// What was approved for a live enrolment, so the admit-time check reads
	// the same identity the person answered about. The ANSWER is still read
	// from the store on every call rather than cached here: a grant that was
	// withdrawn must stop admitting live panes, not only new ones.
	//
	// Each record also carries the EPOCH of the interval it opened. A session
	// can hold two intervals with identical approval — withdrawn and enrolled
	// again — and the epoch is the only thing that tells a connection admitted
	// under the first from one admitted under the second (ADR-0058, and
	// nocx-9mn6z for what its absence cost).
	mu       sync.Mutex
	enrolled map[session.ID]enrolledAgent
	next     toolendpoint.AdmissionEpoch
	// Identities with a question on screen, and every enrolment waiting on
	// it. Starting the agent again while the person is still reading must not
	// put a second copy of the question in the queue — it is the same
	// decision, and a stack of identical dialogs is how a person clicks one
	// without reading it — so a second start joins the first one's wait.
	//
	// The same executable on two MACHINES is two questions, which is why the
	// key carries the domain: joining one machine's question to another's
	// would show a person a dialog about the wrong host and record their
	// answer under it.
	asking map[askingKey][]chan string

	// authorityEnded is called whenever an answer stops holding — a revoke, a
	// withdrawn enrolment — with the session and the EPOCH that ended, so the
	// tool endpoint can close exactly the connections admitted under it.
	// Bound by the composition root; nil means no endpoint was built, and
	// nothing is admitted to close.
	authorityEnded func(session.ID, toolendpoint.AdmissionEpoch)
}

// enrolledAgent is what one live enrolment holds: the identity the person
// answered about, the MACHINE that answer was given for, and the interval it
// opened.
type enrolledAgent struct {
	executable agentapproval.Executable
	// domain is captured when the interval opens and read back with it. The
	// store is consulted with THIS domain rather than a freshly derived one:
	// the answer that opened the interval is the answer the interval holds
	// under, and a route fact that moved afterwards (a re-dial meeting a
	// different host key) must not silently re-point a live interval at
	// another machine's answer.
	domain agentapproval.Domain
	epoch  toolendpoint.AdmissionEpoch
	// token is the bearer this interval admits a far pane's agent with, minted
	// with the epoch and retired with it (nocx-50w7p.16). It is read back under
	// the same lock as the epoch, in one snapshot, because a bearer paired with
	// a newer interval would admit into an authority the answer never covered.
	token string
}

func newAgentApprovalService(sessions workerAuthSessions, store *agentapproval.Store, scope string) *agentApprovalService {
	return &agentApprovalService{
		sessions: sessions, store: store,
		scope:     agentToolEndpointScopePrefix + scope,
		workspace: scope,
		enrolled:  map[session.ID]enrolledAgent{},
		asking:    map[askingKey][]chan string{},
	}
}

func (s *agentApprovalService) SetRequester(requester hostApprovalRequester) {
	s.requester = requester
}

// Interval answers for the AGENT this session enrolled, which is the identity
// the person was asked about, and names the EPOCH of the interval that answer
// opened. A session that never enrolled an agent has none, and is refused
// rather than falling back to some other identity the process tree happens to
// offer.
//
// The epoch is returned with the verdict and not asked for separately, because
// the two are one fact read at one moment: a caller that asked "is this session
// approved" and then "which interval is it in" could get a yes from one
// interval and an epoch from the next, which is the gap the epoch exists to
// close.
func (s *agentApprovalService) Interval(sid session.ID, scope string) (toolendpoint.AdmissionEpoch, bool) {
	s.mu.Lock()
	enrolled, known := s.enrolled[sid]
	s.mu.Unlock()
	if !known {
		return 0, false
	}
	answer, ok := s.store.Lookup(enrolled.executable, enrolled.domain, scope)
	if !ok || answer != agentapproval.Granted {
		return 0, false
	}
	return enrolled.epoch, true
}

// IntervalToken answers the same question Interval does and returns the
// bearer the interval admits with, in ONE snapshot (nocx-50w7p.16).
//
// Atomic on purpose: a caller that asked for the epoch and then for the token
// could pair an epoch from one interval with a bearer from the next, and the
// admission check exists to close exactly that kind of gap. The token is empty
// for a session with no live interval — which is not a token that admits, it is
// the absence of one.
func (s *agentApprovalService) IntervalToken(sid session.ID, scope string) (toolendpoint.AdmissionEpoch, string, bool) {
	s.mu.Lock()
	enrolled, known := s.enrolled[sid]
	s.mu.Unlock()
	if !known {
		return 0, "", false
	}
	answer, ok := s.store.Lookup(enrolled.executable, enrolled.domain, scope)
	if !ok || answer != agentapproval.Granted {
		return 0, "", false
	}
	return enrolled.epoch, enrolled.token, true
}

// mintToolToken mints the per-pane bearer: 32 random bytes, lower-case hex —
// the shape panebind's own validator enforces on both ends. A
// failure is not fatal here — an empty token is one no connection can present,
// so the interval admits by epoch alone exactly as it did before this existed,
// and the pane's agent is refused rather than admitted on a guess.
func mintToolToken() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

// mintEpoch names the interval a session's answer opens. The counter only moves
// forward and is shared by every session, so no two epochs are ever equal —
// which is what lets a retirement name one interval and no other, including the
// interval a live session held before it was enrolled again.
//
// A session that already holds an interval keeps it: an enrolment is what starts
// an interval, and a pane starting a second agent under the answer it has is
// still inside the first one.
//
// Called with mu held.
func (s *agentApprovalService) mintEpoch(sid session.ID) toolendpoint.AdmissionEpoch {
	if enrolled, known := s.enrolled[sid]; known && enrolled.epoch != 0 {
		return enrolled.epoch
	}
	s.next++
	return s.next
}

// Forget drops what an enrolment approved when that enrolment ends, so the map
// follows the live intervals rather than growing for the life of the backend.
//
// THE INTERVAL ENDS WITH THE ANSWER. An enrolment that stops approving is also
// an admission that must stop being usable: the endpoint admits a connection
// once (ADR-0058), so a live one carries the approval it was let in under and
// nothing re-reads it per call. Ending the answer is therefore half the work,
// and the endpoint is told — with the EPOCH that ended — before this returns.
//
// The lock is released before that notice, deliberately. The notice closes
// sockets, and no lock in this file may be held across work the rest of the
// backend does; what makes that safe is the epoch rather than the lock. An
// enrolment landing between the two steps here opens a NEW interval, and the
// notice still names the old one, so the connection admitted under the old
// interval is closed and the new one is not (nocx-9mn6z, and the interleaving
// its test drives).
func (s *agentApprovalService) Forget(sid session.ID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	enrolled, known := s.enrolled[sid]
	delete(s.enrolled, sid)
	s.mu.Unlock()
	if !known {
		return
	}
	s.endAuthority(sid, enrolled.epoch)
}

// SessionEnded ends the admission interval of a session whose output is over.
//
// It is the transport's end of the interval and not the enrolling shell's: a
// pane whose program exited, a tab somebody closed, and a watch the transport
// withdrew all reach here, because the one thing they have in common is the
// fact that matters — the session those connections were admitted for cannot
// produce another call (nocx-9mn6z). Without it an exited-but-retained session
// kept its admitted caller, and the grant inside it stayed usable until the
// socket happened to end.
func (s *agentApprovalService) SessionEnded(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.Forget(session.ID(sessionID))
}

// BindAuthorityEnded takes what closes the tool-endpoint connections admitted
// under an answer that has stopped holding, told which session and which
// interval ended. newToolAuthorizer calls it while both are in hand; an
// approval seam that never gets one is one whose sessions nothing can admit, so
// there is nothing to close.
func (s *agentApprovalService) BindAuthorityEnded(ended func(session.ID, toolendpoint.AdmissionEpoch)) {
	if s == nil {
		return
	}
	s.authorityEnded = ended
}

// endAuthority tells the tool endpoint that an answer has stopped holding, so
// the connections admitted under it are closed. Nil before the authorizer binds
// it, and nil is safe: an endpoint that was never built admits nobody.
func (s *agentApprovalService) endAuthority(sid session.ID, epoch toolendpoint.AdmissionEpoch) {
	if s == nil || s.authorityEnded == nil || epoch == 0 {
		return
	}
	s.authorityEnded(sid, epoch)
}

func (s *agentApprovalService) Approve(ctx context.Context, sid session.ID, agent string) error {
	if s == nil || s.sessions == nil || s.store == nil {
		return errors.New("nocx cannot ask for agent approval")
	}
	// Not for the identity — for the provenance, and this is now the TWO
	// provenances one fact each.
	//
	// A local pane's process is a tree nocx launched on THIS machine, and the
	// owned root pid is what says so. A remote pane's process is a shell
	// channel on somebody else's machine: there is no pid here to own, and
	// what replaces it is the route — the session is one THIS coordinator
	// opened, in this registry, and its kind says the process is remote. What
	// was refused before was minting a durable answer nothing could ever use;
	// a far pane's agent can use one now (worker_auth.go's admittedPane), so
	// the refusal that exists to prevent a useless record no longer applies to
	// it.
	//
	// The stronger form of the route — that this session rides a helper lane
	// of this coordinator — belongs with the spawn path that creates it
	// (nocx-50w7p.5), which does not exist yet. Until then the facts here are
	// the ones the registry itself holds.
	if !s.canEnrol(sid) {
		return errors.New("nocx cannot identify the enrolled agent executable")
	}
	// WHICH MACHINE, from the session's own route and nothing else. A session
	// the registry does not know, or an ssh one whose host key was never
	// observed, has no machine this answer could be keyed to — and it is
	// refused rather than keyed on a partial fact, because an answer that
	// cannot name its machine is an answer for whichever machine asks next.
	domain, err := sessionDomain(s.sessions, sid)
	if err != nil {
		return err
	}
	executable, err := agentapproval.IdentityForExecutable(agent)
	if err != nil {
		return err
	}
	// THE QUESTION COMES FIRST, AND THE HANDSHAKE DOES NOT WAIT FOR IT.
	//
	// The two deadlines are irreconcilable and both are right. The shell gives
	// an enrolment five seconds (nocx.bash __nocx_lc_grant_timeout_s) because
	// a handshake between two programs must be bounded. The approval question
	// has no deadline at all (transport.hostTimeoutFor) because it waits on
	// somebody reading it. Waiting for the second inside the first meant a
	// person could never answer in time (nocx-t7xds). Refusing instead meant
	// the shell did what it does with a refusal — it started the agent without
	// tools while the dialog was still up, and told the person to start it
	// again after answering (nocx-cyhfw).
	//
	// So the verdict is a WAIT: returned now, inside the handshake's bound,
	// holding a channel that closes when the question does. The shell waits
	// on it outside the handshake and enrols again once it closes, and that
	// enrolment reads the stored answer here like any other.
	//
	// Decided under the lock, the store read included. An ask that finished
	// between a lookup and joining its wait would otherwise leave this
	// enrolment waiting on a question that had already closed, or put the
	// question the person had just answered back on screen. The ask records
	// before it settles, so under the lock the store is never behind.
	s.mu.Lock()
	answer, found := s.store.Lookup(executable, domain, s.scope)
	switch {
	case found && answer == agentapproval.Granted:
		// THE EPOCH IS MINTED WHERE THE INTERVAL OPENS, and only there. An
		// enrolment is what starts an interval, so a session starting a second
		// agent under the answer it already has keeps the epoch it is in —
		// and a session whose interval ended gets a new one, which is what
		// lets the next admission be told apart from a connection admitted
		// under the interval that ended.
		s.enrolled[sid] = enrolledAgent{
			executable: executable, domain: domain,
			epoch: s.mintEpoch(sid), token: mintToolToken(),
		}
		s.mu.Unlock()
		return nil
	case found:
		s.mu.Unlock()
		return fmt.Errorf("agent approval was denied for %s", agent)
	case s.requester == nil:
		s.mu.Unlock()
		return errors.New("nocx has no client to ask for agent approval")
	}
	k := askingKey{executable: executable, domain: domain}
	waiters, up := s.asking[k]
	settled := make(chan string, 1)
	s.asking[k] = append(waiters, settled)
	s.mu.Unlock()
	if !up {
		go s.ask(executable, agent, domain)
	}
	return &lifecyclepub.EnrolmentPending{
		Reason:  fmt.Sprintf("nocx is asking whether %s may use its tools", agent),
		Settled: settled,
	}
}

// ask puts the question, records what comes back, and closes the question for
// everybody waiting on it. It outlives the enrolment that raised it,
// deliberately: a person reading a consent dialog is not on the shell's clock.
//
// Its own context, for the same reason. The enrolment's is finished by the
// time anybody clicks, and cancelling the question with it would take the
// dialog off the screen mid-read.
func (s *agentApprovalService) ask(executable agentapproval.Executable, agent string, domain agentapproval.Domain) {
	response, err := s.requester.RequestHost(context.Background(), transport.HostAsk{
		Capability: transport.HostCapAgentApproval,
		Executable: executable.Path,
		Digest:     executable.SHA256,
		Workspace:  s.workspace,
		Machine:    machineFacts(domain),
	})
	if err != nil {
		// Nothing is recorded. An unanswerable question — no client, a window
		// that went away — must not become a durable "no": the person never
		// said it, and a denial is never silently retried, so it would be
		// permanent (nocx-6jbad). It closes WITH A SENTENCE, so the pane
		// starts the agent without tools and says why, rather than enrolling
		// again into a question that cannot appear.
		s.settle(executable, domain, fmt.Sprintf("nocx could not ask whether %s may use its tools: %v", agent, err))
		return
	}
	decision := agentapproval.Denied
	if response.Approved {
		decision = agentapproval.Granted
	}
	if err := s.store.Record(executable, domain, s.scope, decision); err != nil {
		// A sentence here too. Closed as answered, the shell would enrol, find
		// nothing stored, and put the question the person just answered back
		// on screen.
		s.settle(executable, domain, fmt.Sprintf("nocx could not keep your answer about %s: %v", agent, err))
		return
	}
	s.settle(executable, domain, "")
}

// ListAgentAccess is the answers a person gave, in the terms the surface
// speaks: the facts they were shown — the executable, its digest, the
// workspace, AND the machine the answer was given for — and the store's own
// answer. The durable scope key does not travel — this is where it is
// composed, so this is where it is taken apart, and the transport never learns
// its grammar.
//
// The machine travels because a list that showed two machines' answers alike
// could not offer to unmake one of them: the row is what a person clicks, and
// the row is the whole identity now (nocx-50w7p.16).
//
// Answers under some other scope are dropped rather than shown. This backend
// composes exactly one, and a row a person cannot address is a row they
// cannot act on.
func (s *agentApprovalService) ListAgentAccess() []transport.AgentAccessRecord {
	if s == nil || s.store == nil {
		return nil
	}
	records := []transport.AgentAccessRecord{}
	for _, record := range s.store.List() {
		if record.Scope != s.scope {
			continue
		}
		records = append(records, transport.AgentAccessRecord{
			Executable: record.Executable.Path,
			Digest:     record.Executable.SHA256,
			Workspace:  s.workspace,
			Answer:     string(record.Answer),
			Machine:    machineFacts(record.Domain),
		})
	}
	return records
}

// ForgetAgentAccess unmakes one answer, and ends every admission that answer
// was holding.
//
// The DOCUMENT is only half of it now. A tool connection is admitted once
// (ADR-0058), so nothing re-reads this store per call, and revoking the answer
// alone left a live connection — and the grant inside it — working until the
// socket happened to end. Closing the connections admitted under an answer that
// has gone is what makes the revocation take effect on the next call rather
// than on the next session; the PANE still goes on running, which is the state
// a denial produces, because nocx does not reach into an agent's process.
//
// WHICH SESSIONS IS READ FROM THE LIVE ENROLMENTS, not re-asked of the approval
// afterwards. Asking "is this session still approved" was the previous shape and
// it cannot answer this question: a session withdrawn and enrolled again about
// the same agent is approved once more, so the re-asked answer says yes to a
// connection that was admitted before the withdrawal — which is how the OLD
// interval's connection survived the scan (nocx-9mn6z). The sessions this
// answer actually holds are the ones still enrolled as it, and each is ended
// with the epoch that enrolment is in.
//
// A workspace this backend does not answer for is refused rather than
// composed into a key that would match nothing: silently forgetting nothing
// and reporting success is how a person comes to believe a revocation landed.
//
// The MACHINE comes from the caller here, and that is not a hole in "the
// domain is the backend's": this path does not mint or admit anything. It
// names a row the backend itself listed (agentAccess.list), and the only thing
// it can do with that name is remove an answer. An unrecognised or incomplete
// machine matches no key, so the call forgets nothing and says so.
func (s *agentApprovalService) ForgetAgentAccess(executable, digest, workspace string, machine transport.MachineFacts) (bool, error) {
	if s == nil || s.store == nil {
		return false, errors.New("nocx has no record of admitted agents")
	}
	if workspace != s.workspace {
		return false, fmt.Errorf("nocx does not hold answers for workspace %q", workspace)
	}
	domain := domainFromFacts(machine)
	if !domain.Valid() {
		return false, errors.New("nocx cannot tell which machine that answer was for")
	}
	revoked := agentapproval.Executable{Path: executable, SHA256: digest}
	forgotten, err := s.store.Forget(revoked, domain, s.scope)
	if err != nil {
		return forgotten, err
	}
	if !forgotten {
		// Nothing forgotten means no answer ended, so there is nothing to
		// close; closing anyway would end intervals whose answer still holds,
		// and the answer to "did this call change anything" should not be "it
		// depends".
		return false, nil
	}
	// AFTER the Forget and never before: before it, the answer still reads as
	// granted, and a re-enrolment racing this could mint its epoch against an
	// answer that had not stopped holding.
	for _, sid := range s.enrolledAs(revoked, domain) {
		s.Forget(sid)
	}
	return true, nil
}

// enrolledAs names the sessions currently enrolled as one agent ON ONE MACHINE.
// It is a snapshot, taken under the lock the enrolment mutates, so it can only
// miss a session that enrolled as this agent AFTER the revocation — and that
// enrolment is refused by the store, because the answer is already gone.
//
// The domain is part of the match: a revocation is one machine's answer going,
// and an interval enrolled under another machine's answer is not the interval
// this call ends. Without it, revoking an agent here would close a colleague
// host's live interval.
func (s *agentApprovalService) enrolledAs(executable agentapproval.Executable, domain agentapproval.Domain) []session.ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := make([]session.ID, 0, len(s.enrolled))
	for sid, enrolled := range s.enrolled {
		if enrolled.executable == executable && enrolled.domain == domain {
			held = append(held, sid)
		}
	}
	return held
}

// settle closes the question for every enrolment waiting on it — nothing when
// a person's answer was kept, a sentence when there is none — and releases the
// identity, answered or not: a question nobody could deliver must be askable
// again. Each channel is buffered and settled exactly once, so a pane that
// stopped waiting costs nothing.
func (s *agentApprovalService) settle(executable agentapproval.Executable, domain agentapproval.Domain, reason string) {
	s.mu.Lock()
	k := askingKey{executable: executable, domain: domain}
	waiters := s.asking[k]
	delete(s.asking, k)
	s.mu.Unlock()
	for _, settled := range waiters {
		settled <- reason
	}
}

// askingKey names one question on screen. The machine is part of it because the
// same executable on two hosts is two questions, and a person may answer them
// differently.
type askingKey struct {
	executable agentapproval.Executable
	domain     agentapproval.Domain
}

// canEnrol answers whether this session's process is one nocx can name: a tree
// it launched here, or a remote session this coordinator opened.
//
// It is a predicate and not an inline check because the two provenances are
// different questions with one shape — "did nocx start this" — and a second
// inline copy of that question is how the two answers drift apart.
func (s *agentApprovalService) canEnrol(sid session.ID) bool {
	if _, owned := s.sessions.OwnedProcessPID(sid); owned {
		return true
	}
	sess, err := s.sessions.Get(sid)
	if err != nil || sess == nil {
		return false
	}
	// Remote, and therefore a process this machine holds no pid for. The
	// answer is kept for the machine its route names, which sessionDomain
	// derives next: a remote session whose host key was never observed is
	// refused there rather than enrolled here.
	return sess.Kind() == session.KindRemote
}

// sessionDomain derives the trust domain of a session from the session's OWN
// route: its kind, its host, the account its connection authenticated as, and
// the host key that connection was accepted under. Nothing else may supply
// these — a probe, a caller or the agent itself would be a value the admitted
// party chooses, and the whole point of the domain is that the backend knows
// which machine it is talking to.
//
// It refuses rather than guesses. A session the registry does not hold, or an
// ssh session whose host key was never observed, has no machine an answer could
// be keyed to; keying one on a partial fact is how two machines come to share
// an answer.
func sessionDomain(sessions workerAuthSessions, sid session.ID) (agentapproval.Domain, error) {
	sess, err := sessions.Get(sid)
	if err != nil {
		return agentapproval.Domain{}, fmt.Errorf("nocx does not know this pane's machine: %w", err)
	}
	if sess.Kind() != session.KindRemote {
		return agentapproval.LocalDomain(), nil
	}
	host := sess.Host()
	account := accountFromOptions(sess.SSHOptions())
	hostKey := sess.HostKeyFingerprint()
	if host == "" || account == "" || hostKey == "" {
		return agentapproval.Domain{}, errors.New(
			"nocx does not know which machine this pane's agent would run on, so it cannot remember an answer about one")
	}
	return agentapproval.Domain{Kind: agentapproval.DomainSSH, Host: host, Account: account, HostKey: hostKey}, nil
}

// machineFacts renders a domain as the wire's four facts. The renderer words
// them; a composed sentence would be the transport parsing a key's grammar,
// which is the one thing this boundary must not do.
func machineFacts(domain agentapproval.Domain) transport.MachineFacts {
	return transport.MachineFacts{
		Kind:    string(domain.Kind),
		Host:    domain.Host,
		Account: domain.Account,
		HostKey: domain.HostKey,
	}
}

// domainFromFacts is machineFacts read back, for the ONE path that addresses a
// row the backend itself listed (ForgetAgentAccess). It validates, so a caller
// cannot name a domain the backend would never derive.
func domainFromFacts(facts transport.MachineFacts) agentapproval.Domain {
	return agentapproval.Domain{
		Kind:    agentapproval.DomainKind(facts.Kind),
		Host:    facts.Host,
		Account: facts.Account,
		HostKey: facts.HostKey,
	}
}

var _ agentApproval = (*agentApprovalService)(nil)
