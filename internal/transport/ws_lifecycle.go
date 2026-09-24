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
	if p.RequestID != "" && utf8.RuneCountInString(p.RequestID) > maxIDRunes {
		return "requestId exceeds the id length bound"
	}
	if p.SubmitID != "" && utf8.RuneCountInString(p.SubmitID) > maxIDRunes {
		return "submitId exceeds the id length bound"
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
	// SignalDelivery is what the Stops made for this fact's attempt came to —
	// "delivered" or "undelivered", absent when there were none or a delivery
	// is still on its way (nocx-zas0d; signalDeliveryFor decides it). It rides the fact rather
	// than a notification because the renderer must derive "the person stopped
	// this" from state it can replay: a notification a dropped frame can lose
	// would leave a command that ran to its own nonzero end painted as one the
	// person stopped. The transport adds it at the wire boundary because the
	// signal is the transport's to know and the kernel's projection is not
	// where it lives.
	SignalDelivery string `json:"signalDelivery,omitempty"`
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

// signalDeliveryFor is what the lifecycle fact publishes about this attempt's
// Stops, if it has any: "delivered" once an interrupt reached the command,
// "undelivered" once none did and none can, and nothing at all while that is
// still being decided (stopState.whileOpen).
//
// THE CLOSURE CASE IS THE INTERESTING ONE, and it is decided rather than
// guessed. Every delivery path writes its byte or sends its signal only while
// the attempt is open (the byte's condition is "open and started"), so once
// the attempt has left `open`, a Stop that has not landed never will — except
// one ALREADY on its way: a byte the writer took before the closure, whose
// verdict the writer has not handed back yet, or a signal whose rung has not
// returned. The shell's report of the completion is a round trip behind that
// write, so the record counts deliveries in flight and this WAITS for them to
// settle, bounded by the cooperative grace, instead of racing them. A delivery
// that lands wins at once; if the bound expires with one still out, the answer
// is undelivered — the residual one-syscall window the write-time condition
// admits, and the direction that never paints a stop that did not happen.
func (s *WSServer) signalDeliveryFor(f lifecyclepub.Fact) string {
	if f.Attempt == nil {
		return ""
	}
	attempt := lifecycle.AttemptID(f.Attempt.ID)
	s.stopStateMu.Lock()
	rec, ok := s.stopStates[attempt]
	if !ok {
		s.stopStateMu.Unlock()
		return ""
	}
	if f.Attempt.State == lifecyclepub.AttemptOpen {
		v := rec.whileOpen()
		s.stopStateMu.Unlock()
		return v
	}
	s.stopStateMu.Unlock()
	bound := s.effectiveRunLease().SignalGrace
	if bound <= 0 {
		bound = defaultRunSignalGrace
	}
	timer := time.NewTimer(bound)
	defer timer.Stop()
	for {
		s.stopStateMu.Lock()
		delivered, settled, changed := rec.delivered, rec.inflight == 0 || rec.dropped, rec.changed
		s.stopStateMu.Unlock()
		switch {
		case delivered:
			return signalDeliveryDelivered
		case settled:
			return signalDeliveryUndelivered
		}
		select {
		case <-changed:
		case <-timer.C:
			s.stopStateMu.Lock()
			delivered = rec.delivered
			s.stopStateMu.Unlock()
			if delivered {
				return signalDeliveryDelivered
			}
			return signalDeliveryUndelivered
		}
	}
}

// PublishLifecycleProjection updates server-owned projections without
// emitting a duplicate lifecycle notification to the renderer.
func (s *WSServer) PublishLifecycleProjection(f lifecyclepub.Fact) {
	recorded := s.syncLifecycleLedger(f)
	if recorded != nil {
		s.publishHistoryRecorded(f, *recorded)
	}
	// The streamed block's half of the same fact: an authenticated start
	// opens (and answers the keep decision for) the command's block, a
	// completed attempt publishes the fence its interval end waits for.
	s.blockStream.attemptFact(s, f)
}

// THE TWO TRANSITIONS THE FACT STREAM CANNOT CARRY are delivered through the
// publisher's optional emitter, type-asserted there — so a drifting signature
// on either method unwires them in silence. Pinned as a compile-time assertion
// for that reason: the held Stop's delivery is the only consumer today, and a
// test that reads the obligation itself would be the only thing that noticed.
var _ lifecyclepub.AttemptTransitionEmitter = (*WSServer)(nil)

func (s *WSServer) publishHistoryRecorded(f lifecyclepub.Fact, data historyRecordedData) {
	s.lifecycleMu.Lock()
	sid, ok := s.lifecycleLanes[lifecycle.LaneID(f.Lane)]
	s.lifecycleMu.Unlock()
	if !ok {
		return
	}
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, state := rx.getSubscriber()
	if wconn != nil {
		s.historyRecordedNotification(wconn, state, data)
	}
}

func (s *WSServer) publishClosedAttemptHistory(id lifecycle.AttemptID) {
	if s.lifecyclePub == nil {
		return
	}
	att, ok := s.lifecyclePub.Attempt(id)
	if !ok || (att.State != lifecycle.AttemptCompleted && att.State != lifecycle.AttemptUnknown) {
		return
	}
	origin := lifecyclepub.OriginShell
	if att.Origin == lifecycle.OriginApp {
		origin = lifecyclepub.OriginApp
	}
	fact := lifecyclepub.Fact{
		Lane: string(att.Lane),
		Attempt: &lifecyclepub.Attempt{
			ID: string(att.ID), State: lifecyclepub.AttemptCompleted, Command: att.Command,
			Origin: origin, SubmitID: att.SubmitID, StartedAt: att.StartedAt,
			ExitCode: att.ExitCode, CompletedAt: att.CompletedAt,
		},
	}
	if att.State == lifecycle.AttemptUnknown {
		fact.Attempt.State = lifecyclepub.AttemptUnknown
	}
	if recorded := s.syncLifecycleLedger(fact); recorded != nil {
		if att.State != lifecycle.AttemptUnknown || !s.unknownAttemptImpliesSessionEnd(att.Domain) {
			s.raiseLifecycleBlockFinished(*recorded, fact)
		}
		// STASHED, not sent: this report runs before the lane's own
		// completing fact (transitionsBelow's ordering in Ingest), and a
		// renderer that read this receipt first still holds the attempt
		// open — the receipt then attaches to no finished block and is
		// dropped for good (nocx-2v80t.3.22). PublishLifecycle flushes it
		// right after sending the fact that names this same attempt done.
		s.stashHistoryRecorded(id, *recorded)
	}
}

// stashHistoryRecorded holds one completed attempt's history.recorded data
// for PublishLifecycle to deliver, so the wire never carries the receipt
// ahead of the fact that tells the renderer the attempt it names is done.
func (s *WSServer) stashHistoryRecorded(id lifecycle.AttemptID, data historyRecordedData) {
	s.lifecycleMu.Lock()
	if s.pendingHistoryReceipts == nil {
		s.pendingHistoryReceipts = make(map[lifecycle.AttemptID]historyRecordedData)
	}
	s.pendingHistoryReceipts[id] = data
	s.lifecycleMu.Unlock()
}

// takeStashedHistoryRecorded takes and clears the stashed receipt for id, if
// any. Taking rather than peeking keeps a lane that publishes the same
// attempt id twice (a replay) from resending a receipt already delivered.
func (s *WSServer) takeStashedHistoryRecorded(id lifecycle.AttemptID) (historyRecordedData, bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	data, ok := s.pendingHistoryReceipts[id]
	if ok {
		delete(s.pendingHistoryReceipts, id)
	}
	return data, ok
}

// dropPendingHistoryReceiptsFor discards every receipt stashed for sid,
// called from closeSession alongside unregisterLifecycleLanes, dropStopStatesFor
// and dropHeldStopsFor — the same "session ends, its per-attempt records go
// with it" rule, applied to the one record among them keyed by attempt id
// rather than by lane.
//
// A receipt sits in pendingHistoryReceipts between publishClosedAttemptHistory
// stashing it and PublishLifecycle's own fact naming the same attempt done
// (nocx-2v80t.3.22); ONLY that fact ever takes it back out. When the session
// or its lane ends before the fact arrives — the lane's Unknown transition IS
// often the session ending, and unregisterLifecycleLanes drops the lane in the
// same teardown — nothing calls takeStashedHistoryRecorded for that attempt
// ever again, and the entry, with its masked command, would otherwise outlive
// the session for the rest of the server's life (nocx-2v80t.3.23). Keyed by
// SessionID rather than by walking lifecycleLanes: the receipt already carries
// the session it belongs to, and a lane can be re-registered to a new session
// before this runs.
func (s *WSServer) dropPendingHistoryReceiptsFor(sid session.ID) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	for id, data := range s.pendingHistoryReceipts {
		if data.SessionID == sid {
			delete(s.pendingHistoryReceipts, id)
		}
	}
}

// unknownAttemptImpliesSessionEnd reports whether an attempt going Unknown on
// THIS domain is the session itself ending, rather than a nested integration
// alone being lost (the coordinator's decision for nocx-2v80t.3.22, one fact
// one notification). The lane's ROOT domain (no parent) is the session's own
// shell — a local pty, or whatever process it started directly. Once that
// domain closes there is nothing left on the lane to run the command, and
// the session's own end is what monitorExit already raises
// (notify.KindSessionEnded, ws.go); a second "<command> finished" would say
// the same fact twice, and it does — measured 2026-09-25, a background tab
// whose shell died mid-command (`exit 1`) raised both, reading "2" on the
// bell for one death. A NESTED domain going Unknown (an ssh hop whose
// transport dropped) leaves the parent shell running the lane, so the
// session lives on and the command's own notification is the only word
// anybody gets that it stopped — exactly the case nocx-ictcq wants kept.
func (s *WSServer) unknownAttemptImpliesSessionEnd(domain lifecycle.DomainID) bool {
	if s.lifecyclePub == nil {
		return false
	}
	dom, ok := s.lifecyclePub.Domain(domain)
	return ok && dom.Parent == nil
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
	recorded := s.syncLifecycleLedger(f)
	// The streamed block's half of the same fact (ws_block_rows.go): an
	// authenticated start opens — and answers the keep decision for — the
	// command's block; a completed attempt publishes the fence its interval
	// end waits for. Inert without a rows source.
	s.blockStream.attemptFact(s, f)
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
	// The installed fact (nocx-ak2d) is recorded before subscriber routing
	// because it describes the host integration, not the renderer watching it.
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
	//
	// BOTH drop paths are audible now (nocx-n14oo.8). This one returned in
	// silence while the one below it said "no subscriber" out loud, and the
	// difference is not cosmetic: a session opened by the BACKEND has no
	// receiver at all — OpenSession creates no ring and no subscriber by
	// design — so this is the branch a worker participant's pane takes, every
	// time, and the whole establishment then expires with the only visible
	// trace being the adapter's bare hello-timeout ten seconds later.
	rx := s.getRx(sid)
	if rx == nil {
		s.log.Info("lifecycle.changed dropped: the session has no receiver",
			"session", string(sid), "lane", f.Lane, "lifecycle", f.Lifecycle)
		return
	}
	wconn, state := rx.getSubscriber()
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
		SessionID:      string(sid),
		InstanceID:     string(sess.Identity().InstanceID),
		SessionEpoch:   sess.Identity().Epoch,
		SignalDelivery: s.signalDeliveryFor(f),
		Fact:           f,
	}
	// THIS FACT BEFORE ITS OWN RECEIPT (nocx-2v80t.3.22): history.recorded
	// names the attempt this fact is the one to report as done, and a
	// renderer that reads the receipt first still has the attempt open in
	// its own kernel — the receipt then has no finished block to attach to
	// and is dropped for good, never retried. Measured against a real echo
	// of a command carrying a credential: the receipt reached the socket
	// every time before the fact naming its completion did, and the block
	// never got its capture-offer chip. Sending the fact first costs
	// nothing else it did not already cost — the receipt is still the very
	// next write on this connection.
	if err := wconn.TryNotify("lifecycle.changed", mustMarshal(params)); err != nil {
		s.log.Debug("write lifecycle.changed", "session", string(sid), "lane", f.Lane, "error", err)
		return
	}
	// The delivered path is audible too. Every drop above says so, and this
	// was the one branch that did not — so a renderer that never showed its
	// editor left a log in which "sent and refused" and "never sent" read the
	// same (nocx-n14oo.8's reasoning, for the other half).
	s.log.Debug("lifecycle.changed sent", "session", string(sid), "lane", f.Lane,
		"lifecycle", f.Lifecycle, "domain", f.Domain, "epoch", f.Epoch)
	if recorded != nil {
		s.historyRecordedNotification(wconn, state, *recorded)
	} else if f.Attempt != nil {
		// publishClosedAttemptHistory (transitionsBelow, run before this
		// fact by Ingest) is usually the one that actually closed the
		// ledger row for this attempt's completion, which is why syncing it
		// again HERE answers nil — its receipt is waiting in the stash for
		// exactly this fact, never sent ahead of it (nocx-2v80t.3.22).
		if stashed, ok := s.takeStashedHistoryRecorded(lifecycle.AttemptID(f.Attempt.ID)); ok {
			s.historyRecordedNotification(wconn, state, stashed)
		}
	}
}

// raiseLifecycleBlockFinished raises the attested completion event for the
// lifecycle-owned writer. A recorded receipt means FinishExecution closed the
// same ledger row, so this is the one boundary at which a shell command may
// enter the notification feed. ledger.close keeps its own raise for the
// legacy close-only path; the two paths cannot close one attempt twice.
func (s *WSServer) raiseLifecycleBlockFinished(data historyRecordedData, f lifecyclepub.Fact) {
	if s.notifyRaiser == nil || data.SessionID == "" || f.Attempt == nil {
		return
	}
	sess, err := s.registry.Get(data.SessionID)
	if err != nil {
		return
	}
	status := content.EntryFailure
	facts := ledgerCloseFacts{ExitCode: f.Attempt.ExitCode}
	if f.Attempt.State == lifecyclepub.AttemptUnknown {
		status = content.EntryUnknown
		facts.TerminationReason = string(content.TermTransportGone)
	} else if f.Attempt.ExitCode != nil && *f.Attempt.ExitCode == 0 {
		status = content.EntrySuccess
		facts.TerminationReason = string(content.TermCompleted)
	} else if s.signalDeliveryFor(f) == signalDeliveryDelivered {
		status = content.EntryInterrupted
		facts.TerminationReason = string(content.TermUserKilled)
	} else {
		facts.TerminationReason = string(content.TermCompleted)
	}
	// Owner: the lifecycle completion publisher, which raises the attested
	// notification after the ledger transition closes. Closing event: Raise
	// returns after the notification pipeline has admitted the event.
	s.notifyRaiser.Raise(context.Background(), blockFinishedEvent(sess, data.Command, status, facts))
}

// syncLifecycleLedger projects authenticated attempt facts onto the same
// entry the submit handler opened. It runs synchronously in the publisher's
// emitter callback, so a lifecycle fact cannot outrun its store transition.
// The lifecycle publisher is the authority for state; this function only
// advances the ledger's existing Submit → StartExecution → FinishExecution
// lifecycle and never invents a second phase machine.
func (s *WSServer) syncLifecycleLedger(f lifecyclepub.Fact) *historyRecordedData {
	if s.contentDB == nil || f.Attempt == nil {
		return nil
	}
	// Owner: the lifecycle publisher's synchronous projection callback.
	// Closing event: callback return after this ledger transition completes.
	ctx := context.Background()
	ledger := s.contentDB.Ledger()
	row, err := ledger.Entry(ctx, f.Attempt.ID)
	if err != nil {
		s.log.Warn("lifecycle ledger read failed", "attempt", f.Attempt.ID, "error", err)
		return nil
	}
	var sid session.ID
	s.lifecycleMu.Lock()
	sid = s.lifecycleLanes[lifecycle.LaneID(f.Lane)]
	s.lifecycleMu.Unlock()
	if row == nil {
		if f.Attempt.Origin != lifecyclepub.OriginShell || strings.TrimSpace(f.Attempt.Command) == "" {
			s.lifecycleMu.Lock()
			scope, scoped := s.historySources[f.Attempt.ID]
			sid = s.lifecycleLanes[lifecycle.LaneID(f.Lane)]
			s.lifecycleMu.Unlock()
			if !scoped || (f.Attempt.State != lifecyclepub.AttemptCompleted && f.Attempt.State != lifecyclepub.AttemptUnknown) {
				return nil
			}
			prepared, prepareErr := prepareHistoryCommand(f.Attempt.Command, s.captures)
			if prepareErr != nil {
				s.log.Warn("history.recorded masking failed", "attempt", f.Attempt.ID, "error", prepareErr)
				return nil
			}
			s.lifecycleMu.Lock()
			delete(s.historySources, f.Attempt.ID)
			s.lifecycleMu.Unlock()
			return &historyRecordedData{
				SessionID: sid, AttemptID: f.Attempt.ID, PaneID: scope.Pane,
				Generation: scope.Generation, Source: scope.Source,
				Command: prepared.rowCommand, MaskedCount: prepared.maskedCount,
				MaskedKinds: prepared.maskedKinds, Redactions: prepared.redactions,
				Credentials: prepared.credentials,
			}
		}
		s.lifecycleMu.Lock()
		laneSID, ok := s.lifecycleLanes[lifecycle.LaneID(f.Lane)]
		sid = laneSID
		s.lifecycleMu.Unlock()
		if !ok {
			return nil
		}
		sess, sessErr := s.registry.Get(sid)
		if sessErr != nil {
			return nil
		}
		s.recordAttemptEntry(ctx, f.Attempt.ID, f.Attempt.Command, "",
			lifecycleShellLedgerClient, sess, f.Attempt.StartedAt, content.SourceUser)
		row, err = ledger.Entry(ctx, f.Attempt.ID)
		if err != nil {
			s.log.Warn("lifecycle ledger read failed", "attempt", f.Attempt.ID, "error", err)
			return nil
		}
		if row == nil {
			if f.Attempt.State != lifecyclepub.AttemptCompleted && f.Attempt.State != lifecyclepub.AttemptUnknown {
				return nil
			}
			prepared, prepareErr := prepareHistoryCommand(f.Attempt.Command, s.captures)
			if prepareErr != nil {
				s.log.Warn("history.recorded masking failed", "attempt", f.Attempt.ID, "error", prepareErr)
				return nil
			}
			return &historyRecordedData{
				SessionID: sid, AttemptID: f.Attempt.ID, PaneID: sess.PaneID(),
				Generation: s.nextHistoryGeneration.Add(1), Source: content.SourceUser,
				Command: prepared.rowCommand, MaskedCount: prepared.maskedCount,
				MaskedKinds: prepared.maskedKinds, Redactions: prepared.redactions,
				Credentials: prepared.credentials,
			}
		}
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
			EnvironmentID: row.EnvironmentID, Confidence: "{}", Criticality: content.CriticalityRoutine, Payload: "{}",
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
		return nil
	}
	if f.Attempt.State != lifecyclepub.AttemptCompleted && f.Attempt.State != lifecyclepub.AttemptUnknown {
		return nil
	}
	if row.Phase == content.PhaseClosed {
		return nil
	}
	execID, ok := liveExecutionOf(row)
	if !ok {
		execID, err = start()
		if err != nil {
			s.log.Warn("lifecycle ledger recovery start failed", "attempt", row.ID, "error", err)
			return nil
		}
	}
	end := content.FinishExecution{
		EndedAt: time.Now().UnixMilli(), Status: content.EntryUnknown,
		TerminationReason: content.TermTransportGone,
	}
	if f.Attempt.State == lifecyclepub.AttemptCompleted {
		end.TerminationReason = content.TermCompleted
		end.Status = content.EntryFailure
		if f.Attempt.ExitCode != nil && *f.Attempt.ExitCode == 0 {
			end.Status = content.EntrySuccess
		}
		if s.signalDeliveryFor(f) == signalDeliveryDelivered {
			end.TerminationReason = content.TermUserKilled
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
		return nil
	}
	prepared, prepareErr := prepareHistoryCommand(f.Attempt.Command, s.captures)
	if prepareErr != nil {
		s.log.Warn("history.recorded masking failed", "attempt", f.Attempt.ID, "error", prepareErr)
		return nil
	}
	entryCommand := row.Intent
	receipt, receiptErr := content.EntryMaskingOf(row.Payload)
	if receiptErr == nil {
		prepared.maskedCount = receipt.MaskedCount
		prepared.maskedKinds = receipt.MaskedKinds
		prepared.redactions = receipt.Redactions
	}
	source := row.Source
	paneID := ""
	if row.PaneID != nil {
		paneID = *row.PaneID
	}
	generation := uint64(0)
	s.lifecycleMu.Lock()
	scope, scoped := s.historySources[f.Attempt.ID]
	delete(s.historySources, f.Attempt.ID)
	if row.SessionID != nil {
		sid = session.ID(*row.SessionID)
	}
	s.lifecycleMu.Unlock()
	if scoped {
		source = scope.Source
		if paneID == "" {
			paneID = scope.Pane
		}
		generation = scope.Generation
	}
	if source == "" {
		source = content.SourceUser
	}
	if generation == 0 {
		generation = s.nextHistoryGeneration.Add(1)
	}
	if entryCommand == "" {
		entryCommand = prepared.rowCommand
	}
	return &historyRecordedData{
		SessionID: sid, AttemptID: f.Attempt.ID, EntryID: row.ID,
		PaneID: paneID, Generation: generation, Source: source,
		Command: entryCommand, MaskedCount: prepared.maskedCount,
		MaskedKinds: prepared.maskedKinds, Redactions: prepared.redactions,
		Credentials: prepared.credentials,
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
	Domain string `json:"domain"`
	// SubmitID is the renderer's correlation token for this submit: minted
	// before the call, carried on the ledger record it opened, and echoed on
	// the attempt so the renderer binds record to attempt by equality rather
	// than searching for one by position (nocx-td6d4.10). Optional, because a
	// caller with no ledger record to bind has nothing to correlate.
	SubmitID string `json:"submitId,omitempty"`
	// RequestID is the optional broker id of the assistant run that caused
	// this submit. It is transport correlation only: user submits omit it.
	RequestID string `json:"requestId,omitempty"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	Host      string `json:"host"`
	// Source is WHO submitted this command, in the ledger's own vocabulary
	// ('user' is the person at the keyboard, 'assistant' is the agent's
	// lane) — minted by the submitting target at submit and carried
	// verbatim onto the row this call opens (design §3.1, nocx-iadtt).
	//
	// REQUIRED, WITH NO DEFAULT, and that is the whole point: since the
	// entry is opened HERE (nocx-kpqr3) this is the only write that decides
	// the author. A default would let a submit path forget it and silently
	// attribute the assistant's command to the person, which is what
	// nocx-1druc found: a hard-coded 'user' here, and a restored pane that no
	// longer knew the assistant had run the command.
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
	SubmitID  string    `json:"submitId,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

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
	att, err := s.lifecyclePub.SubmitAttempt(lifecycle.DomainID(params.Domain), params.Command, params.Cwd, params.Host, params.SubmitID)
	if err != nil {
		_ = r.TryError(req.ID, RPCError{Code: lifecycleSubmitErrorCode(err), Message: err.Error()})
		return
	}
	if params.Source == string(content.SourceAssistant) && params.RequestID != "" {
		if !s.broker.bindRunAttempt(params.RequestID, string(att.ID), wconn) {
			s.log.Warn("lifecycle submit attempt has no authorized live assistant run to bind",
				"request_id", params.RequestID, "attempt", att.ID)
		}
	}
	s.lifecycleMu.Lock()
	s.historySources[string(att.ID)] = historyAttemptScope{
		Source: content.Source(params.Source), Pane: sess.PaneID(), Generation: state.nextGeneration(),
	}
	s.lifecycleMu.Unlock()
	if s.contentDB != nil {
		s.recordAttemptEntry(ctx, string(att.ID), params.Command, params.Cwd,
			fmt.Sprintf("%d", wconn.id), sess, att.StartedAt, content.Source(params.Source))
	}
	// The submit IS this command's authenticated start: the block stream
	// answers the keep decision here, once per command, before any row
	// exists. Inert without a rows source (ws_block_rows.go).
	s.blockStream.openAttemptFor(s, sid, string(att.ID))
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
		SubmitID:  att.SubmitID,
		StartedAt: att.StartedAt,
	}))
}

// lifecycleShellLedgerClient is the client identity a shell-originated row
// carries. A renderer's row names the connection that wrote it (the numeric
// connection id) and the assistant's name "agent"; this row's writer is the
// transport's own lifecycle projection, and the identity must be stable — it
// binds the idempotency key the attempt id rides (content.SubmitEntry).
const lifecycleShellLedgerClient = "lifecycle-shell"

// recordAttemptEntry opens the durable ledger row for one authenticated
// attempt, under the attempt's own id. It is the ONE writer both open paths
// share — the renderer's submit (handleLifecycleSubmitAttempt) and the
// shell-originated projection (syncLifecycleLedger) — so the masking pass,
// the masking receipt on entries.payload and the row shape cannot drift
// between lifecycle submission and completion. Masking is the one owner
// (maskLedgerCommand, ws_ledger.go); the store's own policy governs whether a
// command row is written at all (history off: Submit records nothing, no
// error — the zero result), output and sensitivity downstream.
//
// Every failure here is fail-open — one warning line, no row — because the
// command has already run or is about to: refusing the record fails nothing
// the person did.
func (s *WSServer) recordAttemptEntry(ctx context.Context, attemptID, command, cwd, client string, sess session.Session, startedAt time.Time, source content.Source) {
	prepared, prepareErr := prepareHistoryCommand(command, s.captures)
	if prepareErr != nil {
		s.log.Warn("lifecycle ledger masking failed; command remains executable", "attempt", attemptID, "error", prepareErr)
		return
	}
	ledger := s.contentDB.Ledger()
	env := environmentForSession(sess)
	if envErr := ledger.EnsureEnvironment(ctx, env); envErr != nil {
		s.log.Warn("lifecycle ledger environment unavailable; command remains executable", "attempt", attemptID, "error", envErr)
		return
	}
	started := startedAt.UnixMilli()
	payload, payloadErr := content.WithEntryMasking("{}", content.EntryMasking{
		MaskedCount: prepared.maskedCount,
		MaskedKinds: prepared.maskedKinds,
		Redactions:  prepared.redactions,
	})
	if payloadErr != nil {
		s.log.Warn("lifecycle ledger masking receipt failed; command remains executable", "attempt", attemptID, "error", payloadErr)
		return
	}
	if _, submitErr := ledger.Submit(ctx, content.SubmitEntry{
		ID: attemptID, Client: client, EnvironmentID: env.ID,
		PaneID: panePtr(sess.PaneID()), SessionID: sessionPtr(sess.ID()),
		Cwd: cwd, Kind: content.EntryShell, Source: source,
		Intent: prepared.rowCommand, StartedAt: &started,
		Sensitivity: content.SensitivityNormal, Payload: payload,
	}); submitErr != nil {
		s.log.Warn("lifecycle ledger submit failed; command remains executable", "attempt", attemptID, "error", submitErr)
	}
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

// lifecycleSpecs declares the two lifecycle control methods (nocx-292k).
//
// ADR-0062 retired a third method, the renderer's establishment
// acknowledgement, and with it the race
// this ordered submission used to exist to close: the renderer used to send
// that ack without awaiting it and then await lifecycle.submitAttempt before
// writing the command bytes to the pty, and a concurrent submission could
// start the submit first while the kernel already reported the domain
// PromptReady with its ACCEPT still pending — opening the attempt before the
// shell was released from its handshake. The accept is now flushed
// synchronously inside Ingest, before the fact that reports PromptReady is
// even published, so there is no window left in which the domain reports
// ready with its accept undelivered. The two methods remain on one ordered
// submission anyway: they are still transport-owned state on one socket, and
// splitting them would buy nothing back.
//
// Not ImmediateSubmission: neither method blocks waiting for a resolution
// that arrives over the same socket, so they are outside the closed
// ingress-critical set (registration.go), and claiming it would fail the
// server build.
//
// No capability gate: the lifecycle kernel, its lane registry and the
// recovery episodes are transport-owned state with their own mutexes — the
// sessionMachine rule ("transport lifecycle, not a store"), not a store any
// capability owns.
//
// reg rather than regResponder: submitAttempt and recoverAck both check
// session ownership via connState, so the handlers need connection
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
	}
}

// lifecycleQueueDepth bounds the ordered lifecycle queue. The traffic is one
// submit per command and an occasional recovery ack on a single connection,
// so the depth only has to absorb a burst; beyond it the submission refuses
// with the ordinary saturation contract, which both methods answer
// fail-open.
const lifecycleQueueDepth = 32
