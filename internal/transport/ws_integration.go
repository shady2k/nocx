package transport

// The session.integrationChanged control plane (nocx-dvql; contracts/
// session.integrationChanged.schema.json): whether a session's shell
// integration is live, and when it is not, why.
//
// It replaces the shellIntegrationReason field of the session-open ack, which
// could only answer once, at open, and therefore could never report the two
// failures that matter most — a handshake that expires ten seconds later, and
// a channel lost mid-session. Keeping the field beside this notification would
// leave two places answering "is this session integrated", which is the defect
// AD-8 names, so the field is gone rather than deprecated (greenfield,
// clean-only).
//
// This is NOT lifecycle.changed and does not duplicate it. That fact is what
// the authenticated kernel concluded about a DOMAIN; this one is about a
// SESSION and carries what only the launcher and the transport know: which
// shell was actually started, and why integration was refused, declined or
// lost. The two are combined here and nowhere else:
//
//   - the launch side (the local pty factory, and the ssh connect path)
//     reports what it started and whether it was refused outright —
//     RegisterIntegration;
//   - the kernel's published facts say when a domain went live —
//     PublishLifecycle calls noteIntegrationLive;
//   - the bootstrap says how the far side answered nocx's own setup, before
//     any shell or domain exists — NoteBootstrapOutcome;
//   - the adapter says which path ended a transport — NoteIntegrationLoss.
//
// The split is not cosmetic. A handshake that times out never establishes a
// domain, so the lane's projection never moves and the publisher emits
// nothing at all: a status derived from published facts alone could not
// report the dominant local failure, which is the whole defect. Conversely
// "the domain is live" is the kernel's word and must not be re-derived from
// the transport's. One owner per question, on each side of the seam.

import (
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// Wire values of the status axis (contracts/session.integrationChanged.schema
// .json). The renderer keys its badge and its card on these exact strings.
const (
	// IntegrationStarting is the honest interval before the shell has
	// proved itself: a session that asked for integration begins here and
	// stays until it either integrates or gives up, so the product never
	// claims either outcome early.
	IntegrationStarting = "starting"
	// IntegrationIntegrated means an authenticated domain is live.
	IntegrationIntegrated = "integrated"
	// IntegrationConventional means integration was attempted and did not
	// happen: a working terminal with a native prompt, and a reason.
	IntegrationConventional = "conventional"
	// IntegrationLost means it was integrated and is not any more.
	IntegrationLost = "lost"
)

// integrationStatus is one session's integration axis as the backend
// currently knows it. It is deliberately small: everything in it is either
// the launch side's own record or the kernel's conclusion, and nothing is
// derived from the byte stream (AD-6).
type integrationStatus struct {
	// shell is what the backend started, absolute where the launch had a
	// path. Fixed at registration and never revised: a later correction
	// would be a second answer to "what did nocx start".
	shell string
	// status is the wire value above.
	status string
	// reason is why, present exactly when status is conventional or lost.
	reason ssh.RefusalReason
	// everLive records that an authenticated domain WAS live on this
	// session. It is what separates "never integrated" from "integrated and
	// then lost" — the same transport loss means different things either
	// side of it, and only the session knows which side it is on.
	everLive bool
	// observedProcess is the best-effort process observation for the
	// details surface. Always a guess, never authority, and never derived
	// from the byte stream; empty when the backend observed nothing.
	observedProcess string
}

// Bootstrap progress stages as internal/bootstrapprogress spells them.
// Declared here as plain strings for the same reason the loss causes are:
// the transport does not import the reader, the composition root passes the
// stage through, and a conformance test in the app package pins the two
// spellings together.
//
// A stage is diagnostic and nothing else. It is not authenticated, it cannot
// be (the descriptor it arrives on is inherited by every descendant of the
// shell), and it therefore may never decide anything but which sentence the
// product says about a failure that has already happened. It never opens an
// attempt, never marks a session integrated and never emits on its own.
const (
	BootstrapStageStartupEntered = "startup-entered"
	BootstrapStageUserRCReturned = "user-rc-returned"
)

// integrationChangedParams is the params object of the
// session.integrationChanged notification. Contracted like every other
// unsolicited notification, because a server-initiated frame has no request
// to correlate against and nothing checking its shape at the call site.
type integrationChangedParams struct {
	SessionID    string             `json:"sessionId"`
	InstanceID   string             `json:"instanceId"`
	SessionEpoch uint64             `json:"sessionEpoch"`
	Status       string             `json:"status"`
	Reason       string             `json:"reason,omitempty"`
	Shell        string             `json:"shell"`
	Detail       *integrationDetail `json:"detail,omitempty"`
}

// integrationDetail is the best-effort half. Marked as a guess by the
// product, never by omission here.
type integrationDetail struct {
	ObservedProcess string `json:"observedProcess"`
}

// RegisterIntegration records what a session's launch started and what it
// already knows about the outcome, and is the only way a session enters the
// integration axis at all. A session that never asked for integration is
// never registered and therefore emits nothing — absence is how "conventional
// by design" is expressed, so the surface has nothing to nag about.
//
// It does not emit. The open ack must precede the session's own traffic in
// both directions (AD-7), and the launch runs inside the open call, so the
// first notification is sent by the open handler after the ack —
// emitIntegration below.
func (s *WSServer) RegisterIntegration(sid session.ID, shell string, status string, reason ssh.RefusalReason) {
	if sid == "" || shell == "" {
		return
	}
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	if s.integrations == nil {
		s.integrations = make(map[session.ID]*integrationStatus)
	}
	s.integrations[sid] = &integrationStatus{shell: shell, status: status, reason: reason}
}

// registerRemoteIntegration enters a REMOTE session into the integration
// axis from what the ssh connect path already decided. A local session is
// registered by the pty factory instead — it is the only thing that knows
// which binary it exec'd — and returns early here rather than being answered
// twice.
//
// A session that asked for nothing and was refused nothing is not registered
// at all, and so emits nothing: absence is how "conventional by design" is
// expressed (the schema says so in as many words), and a raw-mode connection
// has no integration to nag about.
func (s *WSServer) registerRemoteIntegration(sess session.Session, cfg session.Config) {
	if sess.Kind() != session.KindRemote {
		return
	}
	reason := sess.ShellIntegrationReason()
	// script is the only mode that attempts integration (nocx-mlm7): raw
	// publishes nothing, and relay is inert. A configured RemoteCommand is
	// refused in every mode, so a reason outranks the mode.
	requested := desiredModeForAck(cfg.Remote) == "script"
	if reason == ssh.ReasonNone && !requested {
		return
	}
	status := IntegrationStarting
	if reason != ssh.ReasonNone {
		status = IntegrationConventional
	}
	s.RegisterIntegration(sess.ID(), remoteShellName(cfg.Remote), status, reason)
}

// remoteShellName is what the connect path asked the far host to run. A
// profile pin is a real shell name; unpinned, the launcher emits a POSIX
// dispatcher that detects the login shell AT THE FAR END, so the honest
// answer this side has is "auto" — nocx did not choose one. Reporting a
// guess instead would be exactly the invented confidence the details surface
// exists to avoid; the far shell's real name is authenticated only in the
// hello, which a refused or expired session never sends.
func remoteShellName(cfg *ssh.ConnectConfig) string {
	if cfg != nil && cfg.Shell != "" {
		return string(cfg.Shell)
	}
	return string(ssh.ShellAuto)
}

// unregisterIntegration drops a session's axis, called from the same teardown
// that drops its lanes so the map cannot grow with dead sessions.
func (s *WSServer) unregisterIntegration(sid session.ID) {
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	delete(s.integrations, sid)
	delete(s.bootstrapStages, sid)
}

// NoteBootstrapStage records how far a session's shell got through nocx's
// rcfile. It records and nothing else: it emits no notification, changes no
// status and cannot make a session integrated, because the descriptor it comes
// from is inherited by every descendant of the shell and authenticates nobody
// (ADR-0024 decision 4). The one thing it may do is make the next failure
// legible — see applyIntegrationLoss.
//
// Deliberately tolerant of arriving before the session is registered: the
// shell writes its first fact as it starts, and the launch registers the axis
// only once the pty is back.
func (s *WSServer) NoteBootstrapStage(sid session.ID, stage string) {
	if sid == "" || stage == "" {
		return
	}
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	if s.bootstrapStages == nil {
		s.bootstrapStages = make(map[session.ID]string)
	}
	s.bootstrapStages[sid] = stage
}

// NoteIntegrationLoss records why a session's lifecycle transport ended and
// publishes the resulting status. The cause is the adapter's
// lifecyclechannel.LossCause, passed as its string so the transport does not
// depend on the adapter package; the composition root is what joins them.
//
// This is an emission trigger in its own right, and it has to be. A handshake
// that expires never established a domain, so the kernel's projection for
// that lane never changes and no lifecycle.changed fact is published — the
// publisher announces only lanes whose projection moved. Waiting for a fact
// here would reproduce the silence this whole notification exists to end.
func (s *WSServer) NoteIntegrationLoss(lane lifecycle.LaneID, cause string) {
	s.lifecycleMu.Lock()
	sid, ok := s.lifecycleLanes[lane]
	s.lifecycleMu.Unlock()
	if !ok {
		return
	}
	status, reason, changed := s.applyIntegrationLoss(sid, cause)
	if !changed {
		return
	}
	s.log.Info("session integration degraded",
		"session", sid, "status", status, "reason", string(reason), "cause", cause)
	s.emitIntegration(sid)
}

// NoteBootstrapOutcome records the terminal outcome of a session's shell
// bootstrap (carrier design §5.5, §6.1) and publishes the resulting status.
// reason is ReasonNone when the bootstrap was accepted, and the axis is then
// left alone: "a domain is live" is the kernel's word and this is not it — an
// accepted bootstrap means the shell is on its way to proving itself, not that
// it has.
//
// It is a THIRD emission trigger on this axis, and it is one for the reason the
// other two are. The bootstrap runs after the open ack has gone out and reaches
// a terminal outcome of its own, before any domain exists: no lifecycle fact is
// published for it, and no loss cause describes it. Before this the whole
// closed outcome set — an absent hasher on the far host, a digest that did not
// match, a generation that is not installed — was a log line, and the product
// either said nothing at all or, seconds later, said "unknown".
//
// Keyed by the lifecycle lane, which is the addressing this file already uses
// for the loss half. A session with no lane never had a channel to bootstrap.
func (s *WSServer) NoteBootstrapOutcome(lane lifecycle.LaneID, reason ssh.RefusalReason) {
	if reason == ssh.ReasonNone {
		return
	}
	s.lifecycleMu.Lock()
	sid, ok := s.lifecycleLanes[lane]
	s.lifecycleMu.Unlock()
	if !ok {
		return
	}
	status, applied, changed := s.applyBootstrapOutcome(sid, reason)
	if !changed {
		return
	}
	s.log.Info("session integration degraded",
		"session", sid, "status", status, "reason", string(applied), "cause", causeBootstrapRefused)
	s.emitIntegration(sid)
}

// causeBootstrapRefused names this detector in the log, beside the adapter's
// loss causes and the process observer. It is a diagnostic and never a wire
// value.
const causeBootstrapRefused = "bootstrap-refused"

// applyBootstrapOutcome moves the axis for a refused bootstrap under the lock.
//
// The window it may answer in has both ends, exactly like the process
// observer's: it opens when the session is registered as `starting` and closes
// the moment anything else has concluded the axis. A session that has already
// integrated is not degraded by a late bootstrap report — there is no such
// thing, since the bootstrap precedes the shell — and a session already
// answered keeps its first answer, which is the rule this file states twice
// already and this is the third meeting of it.
func (s *WSServer) applyBootstrapOutcome(sid session.ID, reason ssh.RefusalReason) (string, ssh.RefusalReason, bool) {
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	st, ok := s.integrations[sid]
	if !ok {
		return "", "", false
	}
	if st.everLive || st.status != IntegrationStarting {
		return "", "", false
	}
	next := *st
	next.status = IntegrationConventional
	next.reason = reason
	*st = next
	return next.status, next.reason, true
}

// NoteShellReplaced records that the executable nocx started is no longer the
// one running under a session's pty, and concludes the axis now rather than
// when the handshake bound expires.
//
// It is a SECOND DETECTOR of a conclusion the product already had, never a
// second answer: until this existed, the only way the backend learned a shell
// would not answer was that it had not answered for ten seconds, so the user
// read a working terminal for ten seconds and then had a card put over it
// (nocx-cgzc). The observation is not authority — the domain is still
// established only by the authenticated hello, and this never grants one —
// and the name it carries is a guess the product labels as one.
//
// Unlike every other trigger on this axis it can fire INSIDE the open call,
// because the launch is what starts the process being watched and a wrapper
// takes it over milliseconds later. That is safe without a special case:
// emitIntegration resolves the subscriber at emit time, so before the
// subscriber is attached the notification is simply dropped, and the open
// handler's own emitIntegration — which runs after the ack, as AD-7 requires
// — then sends the status this call had already recorded.
//
// The reason is deliberately the one the bound would have produced. The
// vocabulary is a closed server enum whose extension belongs to the bootstrap
// progress facts (nocx-yww2, whose `startup-did-not-return` the renderer
// already names as not-yet-emittable), and inventing a value here would leave
// the product with two words for one situation. What this adds is the timing
// and the observation, which is exactly what the bead asked for.
func (s *WSServer) NoteShellReplaced(sid session.ID, observed string) {
	status, reason, changed := s.applyShellReplaced(sid, observed)
	if !changed {
		return
	}
	s.log.Info("session integration degraded",
		"session", sid, "status", status, "reason", string(reason),
		"cause", causeShellReplaced, "observed", observed)
	s.emitIntegration(sid)
}

// causeShellReplaced names this detector in the log, beside the adapter's own
// loss causes. It is a diagnostic and never a wire value: a reader has to be
// able to tell "the bound expired" from "we watched the shell go", because
// the two need different fixes.
const causeShellReplaced = "shell-replaced"

// applyShellReplaced moves the axis for an observed takeover under the lock,
// and reports whether anything changed.
//
// The window it may answer in has both ends: it opens when the session is
// registered as `starting` and closes the moment anything else concludes the
// axis — an authenticated domain going live, or a transport loss. Outside it
// the observation is dropped, because an integrated shell may replace its own
// image legitimately (the adapter's own comment says a re-exec keeps speaking
// for the same domain), and tearing a working session down for that would be
// a defect this detector introduced rather than found.
func (s *WSServer) applyShellReplaced(sid session.ID, observed string) (string, ssh.RefusalReason, bool) {
	// A guess nobody can name is not worth showing: the contract requires a
	// name inside detail, and "something replaced your shell and I cannot
	// say what" is not something a user can act on.
	if sid == "" || observed == "" {
		return "", "", false
	}
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	st, ok := s.integrations[sid]
	if !ok {
		return "", "", false
	}
	if st.everLive || st.status != IntegrationStarting {
		return "", "", false
	}
	next := *st
	next.observedProcess = observed
	next.status = IntegrationConventional
	// The stage, not the bound. nocx exec's the login shell itself, so a
	// second exec on that pid before the hello can only have come from inside
	// the shell's own startup — which is the same fact the bootstrap progress
	// descriptor reports by falling silent after `startup-entered`
	// (nocx-yww2), arriving here milliseconds after the fork instead of ten
	// seconds later. Reporting the bound would name the detector rather than
	// the event, and the two answers would then race: this one lands first
	// and the first answer wins, so the vaguer word would be the one the user
	// is left with.
	next.reason = ssh.ReasonStartupDidNotReturn
	*st = next
	return next.status, next.reason, true
}

// applyIntegrationLoss maps a transport loss onto the session's axis under
// the lock, and reports whether anything changed.
func (s *WSServer) applyIntegrationLoss(sid session.ID, cause string) (string, ssh.RefusalReason, bool) {
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	st, ok := s.integrations[sid]
	if !ok {
		return "", "", false
	}
	// The session's own disposal path is not a degrade: the tab is going
	// away and the product has nothing to say about it. Emitting here would
	// paint every closing tab as broken on its way out.
	if cause == LossCauseClosed {
		return "", "", false
	}
	// Already answered, and the first answer wins. A session that never
	// integrated and is already conventional has been explained once — by
	// the launch itself, or by the process observation above — and the loss
	// that follows says nothing the user does not already know. Without
	// this, the descriptor the abandoned shell leaves behind would report
	// end-of-stream and DOWNGRADE a specific answer to `unknown` seconds
	// after the product gave it. The adapter states the same rule for its
	// own three paths ("the FIRST cause wins, which is the one that actually
	// ended the channel"); this is that rule where two detectors meet.
	if st.status == IntegrationConventional && !st.everLive {
		return "", "", false
	}
	next := integrationStatus{shell: st.shell, everLive: st.everLive, observedProcess: st.observedProcess}
	switch {
	case st.everLive:
		// It was integrated and is not any more. Which of the transport's
		// paths noticed does not change the answer the user needs.
		next.status = IntegrationLost
		next.reason = ssh.ReasonChannelLost
	case s.bootstrapStages[sid] == BootstrapStageStartupEntered:
		// nocx's rcfile began executing and the user's own startup file
		// never gave control back, so the install line after it was never
		// reached. Ahead of the cause arm on purpose: the stage says WHERE
		// it stopped, the cause says only which of our own timers noticed,
		// and the stage is the half a user can act on. It stays a stage and
		// never becomes a culprit — see ssh.ReasonStartupDidNotReturn.
		next.status = IntegrationConventional
		next.reason = ssh.ReasonStartupDidNotReturn
	case cause == LossCauseHelloTimeout:
		next.status = IntegrationConventional
		next.reason = ssh.ReasonHandshakeTimeout
	case cause == LossCauseListenerGone || cause == LossCauseMasterSocketGone ||
		cause == LossCauseMasterExited:
		// §6.2's second row: after the channel existed and before
		// integration was live. What went away is nocx's own channel to the
		// shell — the forwarded listener, the multiplex socket, or the
		// master holding it — so the shell is fine and the channel is not,
		// which is a different sentence from "your shell did not answer".
		next.status = IntegrationConventional
		next.reason = ssh.ReasonChannelUnavailable
	case cause == LossCauseTransportGone:
		// The underlying SSH transport died. §6.2 is explicit that this
		// ENDS THE SESSION rather than degrading it — there is no prompt to
		// keep. The axis still records the honest reason, because the
		// notification and the session's `exit` race and the renderer may
		// see either; what it must never do is claim a usable conventional
		// terminal, and channel-lost does not.
		next.status = IntegrationConventional
		next.reason = ssh.ReasonBootstrapInterrupted
	default:
		// The descriptor ended or broke before the shell ever proved
		// itself. The backend genuinely cannot say why, and "unknown" is a
		// real visible answer rather than a synonym for success — inventing
		// handshake-timeout here would claim a bound expired when it did
		// not.
		next.status = IntegrationConventional
		next.reason = ssh.ReasonUnknown
	}
	if next.status == st.status && next.reason == st.reason {
		return "", "", false
	}
	*st = next
	return next.status, next.reason, true
}

// Loss causes as the adapter spells them. Declared here as plain strings so
// the transport does not import internal/lifecyclechannel; the composition
// root passes lifecyclechannel.LossCause through, and the adapter's own
// constants are the single source of the spelling. A conformance test in the
// app package pins the two together.
const (
	LossCauseHelloTimeout = "hello-timeout"
	LossCauseClosed       = "closed"
	// The carrier design's §6.2 events, which the REMOTE adapter reports and
	// the local one has no analogue for. They are here as plain strings for
	// the same reason the two above are, and a conformance test in the app
	// package pins each spelling to the adapter constant that produces it.
	//
	// The design insists they are detected separately, and the reason is
	// that they mean different things to a user: nocx's own channel going
	// away, the SSH connection dying, and the shell falling silent are three
	// situations with three different answers, and one word for all three is
	// the "cannot say why" this axis exists to stop giving.
	LossCauseListenerGone     = "listener-gone"
	LossCauseTransportGone    = "transport-gone"
	LossCauseMasterSocketGone = "master-socket-gone"
	LossCauseMasterExited     = "master-exited"
)

// noteIntegrationLive records that an authenticated domain went live on a
// session and publishes the resulting status. Called from PublishLifecycle:
// the kernel is the sole authority on "is a domain live", and this is the
// transport reading that conclusion rather than re-deriving it.
func (s *WSServer) noteIntegrationLive(sid session.ID) {
	s.integrationMu.Lock()
	st, ok := s.integrations[sid]
	if !ok {
		s.integrationMu.Unlock()
		return
	}
	changed := st.status != IntegrationIntegrated
	st.everLive = true
	st.status = IntegrationIntegrated
	st.reason = ssh.ReasonNone
	s.integrationMu.Unlock()
	if changed {
		s.emitIntegration(sid)
	}
}

// emitIntegration writes the session's current integration status to its
// current subscriber. The destination is resolved at emit time and never
// stored, exactly like files.changed and lifecycle.changed — which is what
// survives an AD-9 reconnect; with no subscriber the notification is dropped
// and replayIntegration re-sends it on the next attach.
func (s *WSServer) emitIntegration(sid session.ID) {
	s.integrationMu.Lock()
	st, ok := s.integrations[sid]
	var snap integrationStatus
	if ok {
		snap = *st
	}
	s.integrationMu.Unlock()
	if !ok {
		return
	}
	rx := s.getRx(sid)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return
	}
	// The identity the renderer compares observations against (nocx-3oupk):
	// minted by the backend at open, never here. A session that has left
	// the registry is gone — its teardown is already racing the exit
	// notification, so there is nobody honest to address a status to, and
	// the notification is dropped rather than sent without identity.
	sess, err := s.registry.Get(sid)
	if err != nil {
		return
	}
	ident := sess.Identity()
	params := integrationChangedParams{
		SessionID:    string(sid),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		Status:       snap.status,
		Shell:        snap.shell,
	}
	// Present exactly when the status is conventional or lost, absent
	// otherwise — the schema pins both halves, so a reason on a 'starting'
	// fact would fail the contract rather than reach a renderer.
	if snap.status == IntegrationConventional || snap.status == IntegrationLost {
		params.Reason = string(snap.reason)
		if params.Reason == "" {
			params.Reason = string(ssh.ReasonUnknown)
		}
	}
	if snap.observedProcess != "" {
		params.Detail = &integrationDetail{ObservedProcess: snap.observedProcess}
	}
	if err := wconn.TryNotify("session.integrationChanged", mustMarshal(params)); err != nil {
		s.log.Debug("write session.integrationChanged", "session", sid, "error", err)
	}
}

// replayIntegration re-sends the session's current integration status on
// reattach — the AD-9 resume, beside replayLifecycleFacts. A status is a
// state, not an event: a frontend that reconnects after the handshake expired
// must learn it is in a conventional terminal, and no further transition is
// coming to tell it.
func (s *WSServer) replayIntegration(sid session.ID) {
	s.emitIntegration(sid)
}

// integrationLiveFromFact answers whether a published lifecycle fact means an
// authenticated domain is live on that lane. PromptReady and Running are the
// two states that require one; Desynchronized still HAS a domain but has
// revoked the editor's authority, and Native and Lost have none. Reading the
// published fact rather than the kernel's internals keeps the transport on
// the publication boundary (ADR-0024 decision 7).
func integrationLiveFromFact(f lifecyclepub.Fact) bool {
	return f.Lifecycle == lifecyclepub.LifecyclePromptReady ||
		f.Lifecycle == lifecyclepub.LifecycleRunning
}
