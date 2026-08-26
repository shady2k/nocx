package transport

// The lifecycle.changed control plane (ADR-0024 decision 7; bead nocx-u7uh.5):
// the publication boundary of the authenticated lifecycle protocol.
//
// Authentication terminates in the backend. internal/lifecyclepub wraps the
// kernel, and every mutation an adapter drives is projected into a
// schema-checked Fact; this file is the transport's half of that boundary —
// routing each fact to the lane's session's current subscriber and framing it
// as the lifecycle.changed JSON-RPC notification (contracts/
// lifecycle.changed.schema.json). The destination is resolved at emit time,
// never stored, which is what survives an AD-9 reconnect; with no subscriber
// the fact is dropped and the projection re-syncs on the next attach (the
// publisher's ReplayLane).
//
// The composition root wires WithLifecyclePublisher so the shell-spawn path
// (internal/transport/ws_shell.go) can create lifecycle adapters against the
// publisher, and calls pub.SetEmitter(tp) once the server exists. A session
// whose shell spawns an adapter registers its lane with RegisterLifecycleLane;
// until then the lane is unknown and facts about it are dropped with a debug
// log — the renderer keys enhanced mode on the published fact, so an
// unregistered lane is a conventional terminal, which is the safe direction.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ── lifecycle.* ingress bounds and validators (the per-field sweep) ───────

// maxDestinationRunes bounds a renderer-supplied DESTINATION identity: a DNS
// name, or a
// user@host:port destination. 512 runes covers the longest destination forms
// and bounds a display/identity field.
const maxDestinationRunes = 512

// validateLifecycleSubmitAttemptRaw checks lifecycle.submitAttempt: the
// domain, the app-owned command text, and the informational cwd/host. The
// command is the same product class the kernel bounds (decision 5), so the
// kernel's own ceiling applies here and the refusal moves before the kernel.
func validateLifecycleSubmitAttemptRaw(raw json.RawMessage) string {
	var p submitAttemptParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if strings.TrimSpace(p.Domain) == "" {
		return "domain is required"
	}
	if utf8.RuneCountInString(p.Domain) > maxIDRunes {
		return "domain exceeds the id length bound"
	}
	// An empty command is a bare newline, not an execution: it never opens
	// an attempt (an unstarted attempt would hold the domain and poison the
	// next attach) — the handler's own rule, moved earlier.
	if strings.TrimSpace(p.Command) == "" {
		return "command is required and must not be empty"
	}
	if len(p.Command) > lifecycle.MaxCommandBytes {
		return fmt.Sprintf("command exceeds %d bytes", lifecycle.MaxCommandBytes)
	}
	if utf8.RuneCountInString(p.Cwd) > maxCwdRunes {
		return "cwd exceeds the length bound"
	}
	if utf8.RuneCountInString(p.Host) > maxDestinationRunes {
		return "host exceeds the length bound"
	}
	// Refused HERE rather than at the store write: an attempt opened and
	// then refused would hold the domain and poison the next attach, and a
	// submit whose provenance is unknown must not open one at all.
	if p.Source != string(content.SourceUser) && p.Source != string(content.SourceAssistant) {
		return "source must be one of user, assistant"
	}
	return ""
}

// validateLifecycleRecoverAckRaw checks lifecycle.recoverAck: the session
// the ack is for, and the recovery generation — the hex form of the
// backend-minted one-shot fence nonce (lifecycle.FenceNonce, 32 bytes →
// 64 hex), the shape the handler's own contract documents ("<64 hex>").
func validateLifecycleRecoverAckRaw(raw json.RawMessage) string {
	var p lifecycleRecoverAckParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.SessionID, 32) {
		return "sessionId is required and must be the 32-hex id the backend minted"
	}
	if !isLowerHex(p.Generation, 64) {
		return "generation must be the 64-hex recovery generation the backend minted"
	}
	return ""
}

// validateLifecycleEstablishAckRaw checks lifecycle.establishAck: the
// {session, lane, domain, epoch, generation} addressing tuple of decision 9.
// The generation is compared for equality by the publisher, so its shape is
// left to that check; presence and bound are enforced here.
func validateLifecycleEstablishAckRaw(raw json.RawMessage) string {
	var p lifecycleEstablishAckParams
	if msg := decodeParams(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.SessionID, 32) {
		return "sessionId is required and must be the 32-hex id the backend minted"
	}
	if strings.TrimSpace(p.Lane) == "" {
		return "lane is required"
	}
	if utf8.RuneCountInString(p.Lane) > maxIDRunes {
		return "lane exceeds the id length bound"
	}
	if strings.TrimSpace(p.Domain) == "" {
		return "domain is required"
	}
	if utf8.RuneCountInString(p.Domain) > maxIDRunes {
		return "domain exceeds the id length bound"
	}
	if p.Epoch == 0 {
		return "epoch is required and must be non-zero"
	}
	if strings.TrimSpace(p.Generation) == "" {
		return "generation is required"
	}
	if utf8.RuneCountInString(p.Generation) > maxIDRunes {
		return "generation exceeds the id length bound"
	}
	return ""
}

// lifecycleChangedNotification is the server-initiated lifecycle.changed
// frame — contracted like the files.changed and git.changed notifications
// because an unsolicited notification is exactly where an addressing or shape
// defect hides. Its schema covers the params object only; the params are the
// lifecyclepub.Fact, declared once (AD-8: one owner per behaviour).
type lifecycleChangedNotification struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  lifecycleChangedParams `json:"params"`
}

// lifecycleChangedParams is the renderer-facing addressing envelope. Fact
// remains the lifecycle publisher's single projection; SessionID is added at
// the transport seam because one WebSocket owns several terminal tabs and only
// this layer knows which session the lane belongs to.
type lifecycleChangedParams struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	lifecyclepub.Fact
}

// WithLifecyclePublisher wires the lifecycle publication boundary into the
// server: the shell-spawn path reads the publisher to create lifecycle
// adapters against it, and every fact the publisher emits is routed to the
// lane's session by this server. When nil, no lifecycle adapters can be
// created and no facts are routed — sessions stay conventional.
func WithLifecyclePublisher(pub *lifecyclepub.Publisher) WSServerOption {
	return func(s *WSServer) { s.lifecyclePub = pub }
}

// RegisterLifecycleLane records that a lane belongs to a session, so facts
// about it route to that session's current subscriber. Called by the shell
// spawn path when it creates a lifecycle adapter; the lane is the one the
// adapter minted. Re-registering a lane moves it to the new session.
func (s *WSServer) RegisterLifecycleLane(lane lifecycle.LaneID, sid session.ID) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleLanes == nil {
		s.lifecycleLanes = make(map[lifecycle.LaneID]session.ID)
	}
	s.lifecycleLanes[lane] = sid
}

// unregisterLifecycleLanes drops every lane bound to a session, called from
// closeSession so the registry cannot grow with dead sessions.
func (s *WSServer) unregisterLifecycleLanes(sid session.ID) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	for lane, cur := range s.lifecycleLanes {
		if cur == sid {
			delete(s.lifecycleLanes, lane)
		}
	}
}

// PublishLifecycleProjection updates server-owned projections without
// emitting a duplicate lifecycle notification to the renderer.
func (s *WSServer) PublishLifecycleProjection(f lifecyclepub.Fact) {
	s.syncLifecycleLedger(f)
}

// PublishLifecycle routes one published fact to the lane's session's current
// subscriber and writes the notification. This is the Emitter half of
// internal/lifecyclepub.Emitter: the composition root binds the server as the
// publisher's emitter after construction. The destination is resolved at emit
// time, exactly like files.changed — with no subscriber the fact is dropped
// and the projection re-syncs on the next attach.
func (s *WSServer) PublishLifecycle(f lifecyclepub.Fact) {
	lane := lifecycle.LaneID(f.Lane)
	s.lifecycleMu.Lock()
	sid, ok := s.lifecycleLanes[lane]
	s.lifecycleMu.Unlock()
	if !ok {

		s.log.Debug("lifecycle.changed for unregistered lane", "lane", f.Lane)
		return
	}
	s.syncLifecycleLedger(f)
	// Session death wins, and it wins BEFORE the wire (protocol §12.1).
	// When the pty/SSH channel's Done() has closed, the session's whole
	// remaining contract is `exit`: "emit exit, cancel any pending
	// restoration, reject late acknowledgements, report a disconnected
	// terminal, and make no restoration claim. If the two race, session
	// death wins." So a fact for a dead session is SUPPRESSED here — the
	// episode it might have opened is cancelled and nothing is sent.
	//
	// This used to strip the recovery promise and deliver the fact anyway,
	// which made the observable outcome depend on which goroutine won:
	// monitorExit removing the receiver, or the lifecycle channel's reader
	// noticing EOF and publishing. Both orderings are legal and neither is
	// arranged, so the renderer saw a stripped `lost` before its `exit` or
	// saw nothing, at random. The renderer does the same thing either way —
	// `exit` closes the tab and disposes the projections — so the delivery
	// bought nothing and cost determinism (nocx-2h08).
	//
	// The kernel transition is NOT suppressed and must not be: TransportLost
	// has already marked the domains lost and open attempts unknown. What is
	// dropped is a notification to a session that is going away, never the
	// backend's own authority state.
	sess, err := s.registry.Get(sid)
	if err != nil {
		s.cancelRecovery(sid)
		return
	}
	// The installed fact (nocx-ak2d): what the far shell said it was brought
	// up from, recorded once per domain. Before the routing decisions below,
	// because the fact is about the HOST and does not depend on anybody being
	// attached to watch it — a session whose renderer has gone away has still
	// integrated the host it connected to.
	s.recordInstalledFact(f)

	// The session's integration axis (nocx-dvql): a live domain is the
	// kernel's own word that this session integrated, and it is read from
	// the published fact rather than re-derived, so there is exactly one
	// authority for "is a domain live". The loss half is NOT taken from
	// here — a handshake that expires moves no projection and publishes no
	// fact — it comes from the adapter's loss cause (NoteIntegrationLoss).
	// Before the subscriber checks below: this updates backend state, and
	// the emit inside it does its own subscriber lookup.
	if integrationLiveFromFact(f) {
		s.noteIntegrationLive(sid)
	}

	// An episode without a subscriber is not opened: the next attach replays
	// the fact, and the episode opens then, when the ack can actually come
	// back.
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		// Said out loud, because the drop is otherwise invisible: the fact is
		// gone and the only trace is a renderer that never hears about a
		// transition. That silence is what made nocx-2h08 read as three
		// different tests hanging on three different deadlines.
		s.log.Debug("lifecycle.changed dropped: no subscriber", "session", string(sid), "lane", f.Lane, "lifecycle", f.Lifecycle)
		return
	}
	if f.Lifecycle == lifecyclepub.LifecycleLost && f.Recovery != nil {
		s.openRecovery(sid, f)
	}
	// The envelope is the Responder's now (nocx-292k): every write goes
	// through the outbound queue and its pump, which is the only writer on
	// the socket. SessionID is transport addressing, not a lifecycle fact:
	// one WebSocket carries several tabs, and the renderer must route this
	// notification before any tab mutates or acknowledges its state.
	//
	// The session identity rides the fact (nocx-3oupk): the renderer
	// compares it against the pair it learned at open, so a fact for this
	// sessionId out of a previous backend instance — or an earlier epoch of
	// this one — is refused instead of applied. It is distinct from the
	// domain epoch the Fact itself carries, which is the lifecycle
	// kernel's per-domain counter.
	params := lifecycleChangedParams{
		SessionID:    string(sid),
		InstanceID:   string(sess.Identity().InstanceID),
		SessionEpoch: sess.Identity().Epoch,
		Fact:         f,
	}
	if err := wconn.TryNotify("lifecycle.changed", mustMarshal(params)); err != nil {
		s.log.Debug("write lifecycle.changed", "session", string(sid), "lane", f.Lane, "error", err)
	}
}

// syncLifecycleLedger projects authenticated attempt facts onto the same
// entry the submit handler opened. It runs synchronously in the publisher's
// emitter callback, so a lifecycle fact cannot outrun its store transition.
// The lifecycle publisher is the authority for state; this function only
// advances the ledger's existing Submit → StartExecution → FinishExecution
// lifecycle and never invents a second phase machine.
func (s *WSServer) syncLifecycleLedger(f lifecyclepub.Fact) {
	if s.contentDB == nil || f.Attempt == nil {
		return
	}
	// Owner: lifecycle publisher. Closing event: session close, after which
	// no lifecycle facts are emitted.
	ctx := context.Background()
	ledger := s.contentDB.Ledger()
	row, err := ledger.Entry(ctx, f.Attempt.ID)
	if err != nil {
		s.log.Warn("lifecycle ledger read failed", "attempt", f.Attempt.ID, "error", err)
		return
	}
	if row == nil {
		// Shell-originated attempts have no app-opened row. Only the
		// submit path has the authenticated app identity this projection
		// is allowed to carry.
		return
	}
	start := func() (int64, error) {
		execID, startErr := ledger.StartExecution(ctx, content.StartExecution{EntryID: row.ID})
		if startErr == nil {
			return execID, nil
		}
		env := content.Environment{ID: row.EnvironmentID}
		if row.Environment != nil {
			env = *row.Environment
		}
		if ensureErr := ledger.EnsureEnvironment(ctx, env); ensureErr != nil {
			return 0, ensureErr
		}
		if _, observeErr := ledger.RecordObservation(ctx, content.Observation{
			EnvironmentID: row.EnvironmentID,
			Confidence:    "{}",
			Criticality:   content.CriticalityRoutine,
			Payload:       "{}",
		}); observeErr != nil {
			return 0, observeErr
		}
		return ledger.StartExecution(ctx, content.StartExecution{EntryID: row.ID})
	}
	if f.Attempt.State == lifecyclepub.AttemptOpen {
		if row.Phase == content.PhaseOpen {
			if _, startErr := start(); startErr != nil {
				s.log.Warn("lifecycle ledger start failed", "attempt", row.ID, "error", startErr)
			}
		}
		return
	}
	if f.Attempt.State != lifecyclepub.AttemptCompleted && f.Attempt.State != lifecyclepub.AttemptUnknown {
		return
	}
	if row.Phase == content.PhaseClosed {
		return
	}
	execID, ok := liveExecutionOf(row)
	if !ok {
		execID, err = start()
		if err != nil {
			s.log.Warn("lifecycle ledger recovery start failed", "attempt", row.ID, "error", err)
			return
		}
	}
	end := content.FinishExecution{
		EndedAt:           time.Now().UnixMilli(),
		Status:            content.EntryUnknown,
		TerminationReason: content.TermTransportGone,
	}
	if f.Attempt.State == lifecyclepub.AttemptCompleted {
		end.TerminationReason = content.TermCompleted
		end.Status = content.EntryFailure
		if f.Attempt.ExitCode != nil && *f.Attempt.ExitCode == 0 {
			end.Status = content.EntrySuccess
		}
		if f.Attempt.CompletedAt != nil {
			end.EndedAt = f.Attempt.CompletedAt.UnixMilli()
		}
		payload := content.ShellPayloadJSON(f.Attempt.ExitCode)
		end.Payload = &payload
	}
	if !f.Attempt.StartedAt.IsZero() {
		startedAt := f.Attempt.StartedAt.UnixMilli()
		end.StartedAt = &startedAt
	}
	if err := ledger.FinishExecution(ctx, execID, end); err != nil {
		s.log.Warn("lifecycle ledger finish failed", "attempt", row.ID, "error", err)
	}
}

// replayLifecycleFacts re-emits the current lifecycle projection of every
// lane bound to the session. It runs after both open and attach results: the
// renderer first learns or resumes the server-authoritative session id, then
// receives the current state of its domains even when the transition happened
// while it could not acknowledge it. Lanes with no state derive nothing and
// are skipped.
func (s *WSServer) replayLifecycleFacts(sid session.ID) {
	if s.lifecyclePub == nil {
		return
	}
	s.lifecycleMu.Lock()
	var lanes []lifecycle.LaneID
	for lane, cur := range s.lifecycleLanes {
		if cur == sid {
			lanes = append(lanes, lane)
		}
	}
	s.lifecycleMu.Unlock()
	for _, lane := range lanes {
		s.lifecyclePub.ReplayLane(lane)
	}
}

// ── lifecycle.submitAttempt (ADR-0024 decision 5) ────────────────────────

// submitAttemptParams is the payload of the "lifecycle.submitAttempt" RPC:
// the app-owned half of a command's execution, declared before the bytes
// that can cause the shell's own start event are written to the pty. The
// command text is the reference-intact record line — never the resolved
// send line (decision 5's privacy rule).
type submitAttemptParams struct {
	Domain  string `json:"domain"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Host    string `json:"host"`
	// Source is WHO submitted this command, in the ledger's own vocabulary
	// ('user' is the person at the keyboard, 'assistant' is the agent's
	// lane) — minted by the submitting target at submit and carried
	// verbatim onto the row this call opens (design §3.1, nocx-iadtt).
	//
	// REQUIRED, WITH NO DEFAULT, and that is the whole point: since the
	// entry is opened HERE (nocx-kpqr3) this is the only write that decides
	// the author — history.record's close moves phase, status and times and
	// leaves the column alone. A default would let a submit path forget it
	// and silently attribute the assistant's command to the person, which
	// is what nocx-1druc found: a hard-coded 'user' here, and a restored
	// pane that no longer knew the assistant had run the command.
	Source string `json:"source"`
}

// lifecycleSubmitAttemptResult is the result of lifecycle.submitAttempt:
// the attempt as the kernel created it. The state is always "open" and the
// origin always "app" — the schema pins both. The domain's post-submit
// lifecycle (the move to running) is NOT echoed here: the publisher emits
// the running lifecycle.changed fact for the same mutation, and the
// renderer keys its state machine on that fact alone (AD-8: one owner per
// behaviour).
type lifecycleSubmitAttemptResult struct {
	ID        string    `json:"id"`
	Domain    string    `json:"domain"`
	State     string    `json:"state"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd"`
	Host      string    `json:"host"`
	Origin    string    `json:"origin"`
	StartedAt time.Time `json:"startedAt"`
}

// handleLifecycleSubmitAttempt opens an app-originated attempt on a live
// domain at a ready prompt, synchronously, before the renderer writes the
// command bytes to the pty.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"lifecycle.submitAttempt","params":{"domain":"dom-1","command":"make","cwd":"/srv/app","host":"build.example.com","source":"user"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"id":"att-…","domain":"dom-1","state":"open","command":"make","cwd":"/srv/app","host":"build.example.com","origin":"app","startedAt":"2026-08-08T12:00:00.123456Z"}}
//
// Ownership is enforced exactly like the git/files bindings: the domain's
// lane must be registered to a session THIS connection opened or reattached
// to. This is a mutating call, and it must not be addressable by a domain
// id guessed from another session.
func (s *WSServer) handleLifecycleSubmitAttempt(ctx context.Context, wconn *wsConn, r Responder, state *connState, req jsonrpcRequest) {
	if s.lifecyclePub == nil {
		_ = r.TryError(req.ID, RPCError{Code: -32601, Message: "lifecycle not available"})
		return
	}
	var params submitAttemptParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Domain == "" || params.Command == "" ||
		(params.Source != string(content.SourceUser) && params.Source != string(content.SourceAssistant)) {
		// An empty command is a bare newline, not an execution: it never
		// opens an attempt (an unstarted attempt would hold the domain
		// and poison the next attach).
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: domain, command and source (user|assistant) required"})
		return
	}
	dom, ok := s.lifecyclePub.Domain(lifecycle.DomainID(params.Domain))
	if !ok {
		_ = r.TryError(req.ID, RPCError{Code: lifecycleSubmitErrorCode(lifecycle.ErrUnknownDomain), Message: lifecycle.ErrUnknownDomain.Error()})
		return
	}
	s.lifecycleMu.Lock()
	sid, registered := s.lifecycleLanes[dom.Lane]
	s.lifecycleMu.Unlock()
	if !registered || !state.has(sid) {
		_ = r.TryError(req.ID, RPCError{Code: lifecycleSubmitErrorCode(lifecycle.ErrUnknownDomain), Message: lifecycle.ErrUnknownDomain.Error()})
		return
	}
	sess, ok := state.get(sid)
	if !ok {
		_ = r.TryError(req.ID, RPCError{Code: lifecycleSubmitErrorCode(lifecycle.ErrUnknownDomain), Message: lifecycle.ErrUnknownDomain.Error()})
		return
	}
	att, err := s.lifecyclePub.SubmitAttempt(lifecycle.DomainID(params.Domain), params.Command, params.Cwd, params.Host)
	if err != nil {
		_ = r.TryError(req.ID, RPCError{Code: lifecycleSubmitErrorCode(err), Message: err.Error()})
		return
	}
	if s.contentDB != nil {
		masked, maskErr := maskLedgerCommand(params.Command)
		if maskErr != nil {
			s.log.Warn("lifecycle ledger masking failed; command remains executable", "attempt", att.ID, "error", maskErr)
		} else {
			ledger := s.contentDB.Ledger()
			env := environmentForSession(sess)
			if envErr := ledger.EnsureEnvironment(ctx, env); envErr != nil {
				s.log.Warn("lifecycle ledger environment unavailable; command remains executable", "attempt", att.ID, "error", envErr)
			} else {
				startedAt := att.StartedAt.UnixMilli()
				payload, payloadErr := content.WithEntryMasking("{}", content.EntryMasking{
					MaskedCount: len(masked.findings),
					MaskedKinds: maskedKindsOf(masked.findings),
					Redactions:  redactionsOf(masked.findings, masked.segments),
				})
				if payloadErr != nil {
					s.log.Warn("lifecycle ledger masking receipt failed; command remains executable", "attempt", att.ID, "error", payloadErr)
				} else if _, submitErr := ledger.Submit(ctx, content.SubmitEntry{
					ID:            string(att.ID),
					Client:        fmt.Sprintf("%d", wconn.id),
					EnvironmentID: env.ID,
					PaneID:        panePtr(sess.PaneID()),
					Cwd:           att.Cwd,
					Kind:          content.EntryShell,
					// The submitting target's own word, never derived here
					// from the lane or the run state (design §3.1): a person
					// typing while the assistant works is the person's
					// command, and the assistant's is the assistant's. This
					// row is the only place the fact is written, so a
					// derivation here is one nothing downstream can repair.
					Source:      content.Source(params.Source),
					Intent:      masked.text,
					StartedAt:   &startedAt,
					Sensitivity: content.SensitivityNormal,
					Payload:     payload,
				}); submitErr != nil {
					s.log.Warn("lifecycle ledger submit failed; command remains executable", "attempt", att.ID, "error", submitErr)
				}
			}
		}
	}
	if current, ok := s.lifecyclePub.Attempt(att.ID); ok && current.Started {
		// The shell can authenticate its Start concurrently with the
		// store insert. The publisher emitted that fact before this row
		// existed, so reconcile the kernel's current state once the row
		// is durable.
		s.syncLifecycleLedger(lifecyclepub.Fact{
			Attempt: &lifecyclepub.Attempt{ID: string(current.ID), State: lifecyclepub.AttemptOpen},
		})
	}
	_ = r.TryResult(req.ID, mustMarshal(lifecycleSubmitAttemptResult{
		ID:        string(att.ID),
		Domain:    string(att.Domain),
		State:     lifecyclepub.AttemptOpen,
		Command:   att.Command,
		Cwd:       att.Cwd,
		Host:      att.Host,
		Origin:    lifecyclepub.OriginApp,
		StartedAt: att.StartedAt,
	}))
}

// lifecycleSubmitErrorCode maps a lifecycle.SubmitAttempt refusal to a
// JSON-RPC code, mirroring the gitErrorCode convention: caller-side
// conditions (no live domain, no ready prompt, an oversize command) are
// invalid params; everything else is an internal error. The renderer treats
// every refusal the same way — fail-open, the command still reaches the
// pty and the session stays conventional — so the code is a diagnostic, not
// a branch.
func lifecycleSubmitErrorCode(err error) int {
	switch {
	case errors.Is(err, lifecycle.ErrUnknownDomain),
		errors.Is(err, lifecycle.ErrNoActiveDomain),
		errors.Is(err, lifecycle.ErrDomainNotLive),
		errors.Is(err, lifecycle.ErrDomainDesynchronized),
		errors.Is(err, lifecycle.ErrNotPromptReady),
		errors.Is(err, lifecycle.ErrAttemptOpen),
		errors.Is(err, lifecycle.ErrOversizeCommand):
		return -32602
	default:
		return -32603
	}
}

// lifecycleSpecs declares the three lifecycle control methods (nocx-292k).
//
// They share ONE ordered submission, and the sharing is the point. The
// renderer sends lifecycle.establishAck without awaiting it (it is
// fire-and-forget in terminal-content.ts) and then awaits
// lifecycle.submitAttempt before writing the command bytes to the pty — so
// the two are adjacent on one socket, in that order. A concurrent
// submission could start the submit first, and the kernel already reports
// the domain PromptReady while its ACCEPT is still pending, so the attempt
// would open before the shell was released from its handshake. The read
// loop used to provide that ordering by accident, running everything
// inline; control.NewOrderedSubmission is what states it.
//
// Not ImmediateSubmission: none of the three blocks waiting for a
// resolution that arrives over the same socket, so they are outside the
// closed ingress-critical set (registration.go), and claiming it would fail
// the server build.
//
// No capability gate: the lifecycle kernel, its lane registry and the
// recovery episodes are transport-owned state with their own mutexes — the
// sessionMachine rule ("transport lifecycle, not a store"), not a store any
// capability owns.
//
// reg rather than regResponder: submitAttempt checks session ownership via
// connState, and establishAck additionally checks that this connection is
// still the session's current subscriber, so the handlers need connection
// identity, not just a writer.
func (s *WSServer) lifecycleSpecs() []methodSpec {
	sub := control.NewOrderedSubmission("lifecycle", lifecycleQueueDepth)
	return []methodSpec{
		reg(sub, "lifecycle.submitAttempt", params(validateLifecycleSubmitAttemptRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) { s.handleLifecycleSubmitAttempt(ctx, w, r, state, req) }
		}),
		reg(sub, "lifecycle.recoverAck", params(validateLifecycleRecoverAckRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) { s.handleLifecycleRecoverAck(r, state, req) }
		}),
		reg(sub, "lifecycle.establishAck", params(validateLifecycleEstablishAckRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) { s.handleLifecycleEstablishAck(w, r, state, req) }
		}),
	}
}

// lifecycleQueueDepth bounds the ordered lifecycle queue. The traffic is one
// submit per command and one ack per prompt on a single connection, so the
// depth only has to absorb a burst; beyond it the submission refuses with
// the ordinary saturation contract, which every one of the three answers
// fail-open.
const lifecycleQueueDepth = 32
