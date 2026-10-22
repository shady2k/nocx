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
//   - the launch side reports what it started and whether it was refused
//     outright — RegisterIntegration. That is the HOSTED opener for every
//     local pane and every helper-backed remote one (the daemon forks the
//     shell and only the opener sees its launch record), and the ssh connect
//     path for the rest; internal/app's local pty factory used to be the
//     first of those and was deleted with nocx-ie23r.3;
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
	"context"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/profile"
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

// integrationStartupBound is how long a session may sit on this axis saying
// `starting` before the product answers for it (nocx-1ebrs.20).
//
// THE AXIS MUST NEVER BE ABLE TO STAY `starting`. While it reads that, the
// pane shows "Checking this shell…" INSTEAD of a terminal and DROPS every
// keystroke (frontend/src/integration/status.ts's isAwaitingIntegration is the
// one read, and terminal-content.ts's onSessionIntent refuses on it), so a
// `starting` nothing ever revises is a terminal that never becomes usable and
// whose only exit is quitting the application. Every other missing capability
// in this product fails open; this one had no fallback of its own and depended
// on each transport arming its own detector — which is a chain that broke
// once already (nocx-zg3k3.14.9: a helper hello timeout during the open was
// dropped, and the pane waited until the person quit).
//
// THE NUMBER IS lifecycle.HelloTimeout (10 s), and it is that number for a
// reason rather than by inheritance: it is the protocol's own handshake bound
// (docs/lifecycle-protocol.md §5), the whole budget a shell is given to prove
// itself on every tier that arms one — the local minted adapter, the remote
// adapter, the expected listener domain. A session still unexplained one full
// handshake bound after it entered the axis has been given that entire budget
// and nothing has come of it, so the honest answer is a working terminal plus
// the reason, not a longer wait. It is ALSO the safe direction: the bound can
// only fire when the transport's own detector was silent or broken, because
// every such detector is armed EARLIER than this one — the adapter's timer
// starts when its transport is minted, this one when the session enters the
// axis, which is after the spawn — so on every working path the transport's
// finer answer still lands first and the first answer wins.
//
// The reason it publishes is the same `handshake-timeout` the adapter's own
// expiry produces, because it is the same event from where the person sits:
// nocx's integration never answered.
const integrationStartupBound = lifecycle.HelloTimeout

// startupBound is the bound this server applies: the field when a test set
// one, the constant otherwise.
func (s *WSServer) startupBound() time.Duration {
	if s.integrationStartupBound > 0 {
		return s.integrationStartupBound
	}
	return integrationStartupBound
}

// integrationStatus is one session's integration axis as the backend
// currently knows it. It is deliberately small: everything in it is either
// the launch side's own record or the kernel's conclusion, and nothing is
// derived from the byte stream (AD-6).
type integrationStatus struct {
	// shell is what the backend started, absolute where the launch had a
	// path. Fixed at registration and never revised: a later correction
	// would be a second answer to "what did nocx start".
	shell        string
	shellVersion string
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
	// byBound records that the answer standing here is the STARTUP BOUND's,
	// not a detector's: the axis ran out of patience rather than learning
	// anything. It is what lets a detector that describes the same startup
	// correct the net with its more specific word afterwards
	// (applyBootstrapOutcome, applyShellReplaced), while an answer a transport
	// actually reported keeps its ground — the axis's rule that the first
	// answer wins is about two ANSWERS, and a net is not one.
	byBound bool
}

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
	ShellVersion string             `json:"shellVersion,omitempty"`
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
func (s *WSServer) RegisterIntegration(sid session.ID, shell string, status string, reason ssh.RefusalReason, shellVersion string) {
	if sid == "" || shell == "" {
		return
	}
	// A lifecycle loss may arrive after the helper has spawned the shell but
	// before this open has registered its session state. Take the same lock
	// order as NoteIntegrationLoss so that either the registration consumes
	// the pending cause or the reporter observes the state and applies it.
	s.lifecycleMu.Lock()
	s.integrationMu.Lock()
	if s.integrations == nil {
		s.integrations = make(map[session.ID]*integrationStatus)
	}
	s.integrations[sid] = &integrationStatus{shell: shell, shellVersion: shellVersion, status: status, reason: reason}
	s.armStartupBoundLocked(sid, status)
	s.integrationMu.Unlock()
	if cause := s.pendingSessionLoss[sid]; cause != "" {
		delete(s.pendingSessionLoss, sid)
		_, _, _ = s.applyIntegrationLoss(sid, cause)
	}
	s.lifecycleMu.Unlock()
}

// armStartupBoundLocked starts the session's startup bound if its entry says
// `starting`, and clears any bound an earlier registration left. Called with
// integrationMu held, from the one place a session enters the axis.
//
// A `starting` that is already answered by a queued loss still gets a bound
// armed here, and that is deliberate: the stop is the EMIT's job
// (emitIntegration runs once after every actual change, and the open handler's
// emit follows the registration that applied the queued loss), so the two
// orders — loss first, answer first — converge on one state instead of two
// paths that each have to remember to cancel.
func (s *WSServer) armStartupBoundLocked(sid session.ID, status string) {
	s.stopStartupBoundLocked(sid)
	if status != IntegrationStarting {
		return
	}
	if s.integrationStartupBounds == nil {
		s.integrationStartupBounds = make(map[session.ID]*time.Timer)
	}
	s.integrationStartupBounds[sid] = time.AfterFunc(s.startupBound(), func() {
		s.expireIntegrationStartup(sid)
	})
}

// stopStartupBoundLocked cancels the session's startup bound, if any. Called
// with integrationMu held, from the emit that observes the axis leaving
// `starting` and from the teardown that drops the entry altogether.
func (s *WSServer) stopStartupBoundLocked(sid session.ID) {
	if t := s.integrationStartupBounds[sid]; t != nil {
		t.Stop()
		delete(s.integrationStartupBounds, sid)
	}
}

// expireIntegrationStartup ends the honest `starting` interval on the
// session's behalf when nothing else did: the axis becomes `conventional` with
// the bound's own reason, and the pane is a working terminal again.
//
// IT ANSWERS ONCE. applyIntegrationLoss refuses a session that has already
// concluded (its first-answer rule), so a bound that fires in the same
// instant as a transport's own detector publishes nothing — the finer answer
// stands.
func (s *WSServer) expireIntegrationStartup(sid session.ID) {
	status, reason, changed := s.applyIntegrationLoss(sid, LossCauseStartupBound)
	if !changed {
		return
	}
	// SAID, and said as ITSELF. This detector and the adapter's own expiry
	// publish the same wire answer, which is right — they are one event to the
	// person — but the log names the cause, so "the channel said so" and
	// "nothing said anything at all" never read alike.
	s.log.Info("session integration degraded: no transport proved this session's shell inside the startup bound",
		"session", sid, "status", status, "reason", string(reason),
		"cause", LossCauseStartupBound, "bound_ms", s.startupBound().Milliseconds())
	s.emitIntegration(sid)
}

// registerOpenedIntegration enters a session this open has just produced into
// the integration axis, from whatever the open itself already decided.
//
// ONE OWNER, TWO SOURCES, and the sources do not overlap. A HOSTED open — this
// machine's helper or another's — carries what it knows in its own result,
// because the opener is the only thing that saw which binary the daemon
// started and whether a lifecycle lane was established; before nocx-ie23r.3
// that was internal/app's local pty factory saying the same sentence about a
// shell it had forked itself. A REMOTE ssh open carries nothing, because
// ShellIntegrationReason is the ssh channel's own answer and this is where the
// session first exists as a session to hang it on.
//
// A session that asked for nothing and was refused nothing is not registered
// at all, and so emits nothing: absence is how "conventional by design" is
// expressed (the schema says so in as many words), and a raw-mode connection
// has no integration to nag about.
func (s *WSServer) registerOpenedIntegration(sess session.Session, cfg session.Config, hosted *HostedSessionOpen) {
	if hosted != nil && hosted.IntegrationStatus != "" && hosted.IntegrationShell != "" {
		s.RegisterIntegration(sess.ID(), hosted.IntegrationShell, hosted.IntegrationStatus, hosted.IntegrationReason, hosted.IntegrationShellVersion)
		return
	}
	if sess.Kind() != session.KindRemote {
		return
	}
	reason := sess.ShellIntegrationReason()
	// Whether this mode attempts integration is the axis's own question,
	// asked of the axis (AD-8) — not re-spelled here. This line used to
	// read `== "script"`, a second copy of internal/ssh's open-time gate,
	// and the two silently disagreed the moment the default moved: every
	// unconfigured session integrated and then reported nothing, because
	// the gate said yes and the reporter said no. A configured
	// RemoteCommand is refused in every mode, so a reason outranks the
	// mode.
	requested := profile.DesiredMode(desiredModeForAck(cfg.Remote)).DeliversScripts()
	if reason == ssh.ReasonNone && !requested {
		return
	}
	status := IntegrationStarting
	if reason != ssh.ReasonNone {
		status = IntegrationConventional
	}
	s.RegisterIntegration(sess.ID(), remoteShellName(cfg.Remote), status, reason, "")
}

// sessionAwaitsIntegration answers whether sid has just entered the
// integration axis in the `starting` state (nocx-ui8q6.6). It is read
// immediately after registerOpenedIntegration decides sid's entry, before the
// open ack is built, so the ack can state — as its own `awaitsIntegration`
// field — whether a session.integrationChanged notification is guaranteed to
// follow it.
//
// It is not a second opinion on the axis: registerOpenedIntegration already
// wrote the one map this reads, under the one lock that guards it, and
// nothing between that write and this read can move the axis — the launch
// that produced the entry is still on this same goroutine, before the ack it
// is answering has even been marshalled. Everything that COULD move it later
// (noteIntegrationLive, applyIntegrationLoss, applyBootstrapOutcome,
// applyShellReplaced) needs the session registered in this transport's
// registry first, which does not happen until after this handler returns.
//
// False covers two different sessions on purpose: one that never entered the
// axis at all (RegisterIntegration was never called — nothing is ever
// coming), and one that entered it already resolved to `conventional` at
// open (a fact IS coming, moments after this ack, but isAwaitingIntegration
// on the renderer only ever gates `starting`, so neither has anything to make
// a pane wait on). The renderer does not need to tell those two apart; it
// only needs to know whether to hold the grid for the FIRST fact, and
// `starting` is the only status that means yes.
func (s *WSServer) sessionAwaitsIntegration(sid session.ID) bool {
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	st, ok := s.integrations[sid]
	return ok && st.status == IntegrationStarting
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
//
// It wakes anyone in AwaitIntegration first: a session torn down mid-wait
// (the tab closed, the process killed) is not going to answer, and a waiter
// that only listened for a status change would hold its whole context
// deadline for a session that no longer exists to change one.
func (s *WSServer) unregisterIntegration(sid session.ID) {
	s.integrationMu.Lock()
	delete(s.integrations, sid)
	// The bound goes with the entry: a timer for a session nobody can reach
	// would fire into a map it no longer appears in, and the pane it was
	// armed for is gone.
	s.stopStartupBoundLocked(sid)
	waiters := s.integrationWaiters[sid]
	delete(s.integrationWaiters, sid)
	s.integrationMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// IntegrationOutcome is what AwaitIntegration settles to.
type IntegrationOutcome struct {
	// Registered is false when sid never entered the axis at all — the same
	// absence RegisterIntegration's own doc calls "conventional by design":
	// a session that asked for nothing was refused nothing and has nothing
	// to report. Status and Reason are meaningless when this is false.
	Registered bool
	// Status is one of the wire values above, always something other than
	// IntegrationStarting: AwaitIntegration does not return while that is
	// still current.
	Status string
	Reason ssh.RefusalReason
}

// AwaitIntegration blocks until sid's integration axis leaves `starting`, or
// ctx ends first (nocx-ui8q6.4). It never derives that status itself — it
// only reads the one map RegisterIntegration and emitIntegration already
// write (AD-8) — and it wakes from the exact place those transitions already
// converge: emitIntegration runs once after every actual change to the axis,
// including the very first one a caller's RegisterIntegration produces, so
// waking there needs no second classification of what counts as "resolved".
//
// ABSENCE IS A FAST ANSWER, not a wait. A session that never asked to be
// tracked (RegisterIntegration never called for it) will never move, and
// blocking on a map entry that will never exist would be waiting for an
// event that cannot happen. The check and the registration below happen
// under one lock acquisition, so a session that enters the axis, or leaves
// `starting`, between the check and the wait is still observed: nothing here
// can miss a transition that lands in the gap, because there is no gap.
func (s *WSServer) AwaitIntegration(ctx context.Context, sid session.ID) (IntegrationOutcome, error) {
	for {
		s.integrationMu.Lock()
		st, ok := s.integrations[sid]
		if !ok {
			s.integrationMu.Unlock()
			return IntegrationOutcome{Registered: false}, nil
		}
		if st.status != IntegrationStarting {
			out := IntegrationOutcome{Registered: true, Status: st.status, Reason: st.reason}
			s.integrationMu.Unlock()
			return out, nil
		}
		ch := make(chan struct{})
		if s.integrationWaiters == nil {
			s.integrationWaiters = make(map[session.ID][]chan struct{})
		}
		s.integrationWaiters[sid] = append(s.integrationWaiters[sid], ch)
		s.integrationMu.Unlock()

		select {
		case <-ch:
			// A wake is a "go look again", not an answer: the loop re-reads
			// the map under the lock rather than trusting anything about why
			// it fired, which is what lets the very first emission (still
			// `starting`, for a session whose own launch reported that) cost
			// one extra iteration instead of a second wake mechanism.
		case <-ctx.Done():
			// The channel this iteration registered is left in the map
			// rather than removed here under a second lock acquisition: the
			// next mutation (or unregisterIntegration on teardown) already
			// pops and closes the whole per-session slice, so it is reclaimed
			// on the very next event either way, and nothing reads a closed
			// channel as a signal of anything.
			return IntegrationOutcome{}, ctx.Err()
		}
	}
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
	if !ok {
		// Only a lane explicitly reserved by a helper open can be pending.
		// Unrelated or already-abandoned lanes remain ignored.
		if _, pending := s.pendingLifecycleLanes[lane]; pending && cause != LossCauseClosed {
			if s.pendingLifecycleLoss == nil {
				s.pendingLifecycleLoss = make(map[lifecycle.LaneID]string)
			}
			if s.pendingLifecycleLoss[lane] == "" {
				s.pendingLifecycleLoss[lane] = cause
			}
		}
		s.lifecycleMu.Unlock()
		return
	}
	s.lifecycleMu.Unlock()
	s.applyOrQueueIntegrationLoss(lane, sid, cause)
}

// applyOrQueueIntegrationLoss keeps a loss received during open until the
// session enters the integration axis. It revalidates the lane under
// lifecycleMu because the caller may have been descheduled after its initial
// lane lookup while teardown removed that mapping.
func (s *WSServer) applyOrQueueIntegrationLoss(lane lifecycle.LaneID, sid session.ID, cause string) {
	s.lifecycleMu.Lock()
	if current, ok := s.lifecycleLanes[lane]; !ok || current != sid {
		s.lifecycleMu.Unlock()
		return
	}
	status, reason, changed := s.applyIntegrationLoss(sid, cause)
	if !changed && cause != LossCauseClosed {
		s.integrationMu.Lock()
		_, registered := s.integrations[sid]
		s.integrationMu.Unlock()
		if !registered {
			if s.pendingSessionLoss == nil {
				s.pendingSessionLoss = make(map[session.ID]string)
			}
			if s.pendingSessionLoss[sid] == "" {
				s.pendingSessionLoss[sid] = cause
			}
		}
	}
	s.lifecycleMu.Unlock()
	// The lifecycle channel carries the replay window a hold waits for. A
	// loss releases it even when the integration axis has already answered.
	s.releaseSessionEndHolds(sid)
	if !changed {
		return
	}
	s.publishIntegrationLoss(sid, cause, status, reason)
}

func (s *WSServer) publishIntegrationLoss(sid session.ID, cause, status string, reason ssh.RefusalReason) {
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
//
// THE ONE EXCEPTION IS THE STARTUP BOUND'S OWN ANSWER (integrationStatus.byBound,
// nocx-1ebrs.20), and it is not an exception to that rule: the bound is a net
// thrown when nothing arrived, not an answer about what happened. A bootstrap
// that finishes after it — a far host that took longer than one handshake
// bound — knows what the net could not, and the person is better served by the
// specific reason than by "nocx's integration never answered". The reverse is
// forbidden: a bootstrap cannot overwrite a transport's own verdict.
func (s *WSServer) applyBootstrapOutcome(sid session.ID, reason ssh.RefusalReason) (string, ssh.RefusalReason, bool) {
	s.integrationMu.Lock()
	defer s.integrationMu.Unlock()
	st, ok := s.integrations[sid]
	if !ok {
		return "", "", false
	}
	if st.everLive || (st.status != IntegrationStarting && !st.byBound) {
		return "", "", false
	}
	next := *st
	next.status = IntegrationConventional
	next.reason = reason
	// The specific word replaces the net, and is itself no longer a net.
	next.byBound = false
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
// The reason is deliberately the one the bound would have produced, and since
// nocx-ie23r.3 it is the ONLY thing that produces it: the bootstrap-progress
// descriptor that used to report where a startup stopped needed the
// coordinator to fork the shell, and the daemon forks it now. Inventing a
// value here would leave the product with two words for one situation. What
// this adds is the timing and the observation.
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
//
// IT MAY STILL CORRECT THE STARTUP BOUND'S NET (integrationStatus.byBound):
// "the bound expired and nobody said why" is not a diagnosis, and an
// observation that names the takeover is one. That is the same window read
// from the other side — the axis has not been ANSWERED, it has only run out of
// patience.
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
	if st.everLive || (st.status != IntegrationStarting && !st.byBound) {
		return "", "", false
	}
	next := *st
	next.byBound = false
	next.observedProcess = observed
	next.status = IntegrationConventional
	// The startup, not the bound. The helper exec's the login shell itself,
	// so a second exec on that pid before the hello can only have come from
	// inside the shell's own startup, and this arrives milliseconds after the
	// fork instead of ten seconds later. Reporting the bound would name the
	// detector rather than the event, and the two answers would then race:
	// this one lands first and the first answer wins, so the vaguer word
	// would be the one the user is left with.
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
	next := integrationStatus{shell: st.shell, shellVersion: st.shellVersion, everLive: st.everLive, observedProcess: st.observedProcess}
	switch {
	case st.everLive:
		// It was integrated and is not any more. Which of the transport's
		// paths noticed does not change the answer the user needs.
		next.status = IntegrationLost
		next.reason = ssh.ReasonChannelLost
	case cause == LossCauseHelloTimeout, cause == LossCauseStartupBound:
		// The same answer for two detectors, because it is one event: the
		// shell was given its handshake bound and nothing proved it.
		next.status = IntegrationConventional
		next.reason = ssh.ReasonHandshakeTimeout
		// WHICH ONE SAID IT MATTERS TO WHAT MAY CORRECT IT. An answer the
		// STARTUP BOUND produced is the axis's own net rather than a
		// diagnosis — nothing was observed, a budget simply ran out — so a
		// detector that describes the same startup may still land its more
		// specific word afterwards (applyBootstrapOutcome, applyShellReplaced).
		// An answer a transport produced is evidence and keeps its ground.
		next.byBound = cause == LossCauseStartupBound
	case cause == LossCauseListenerGone || cause == LossCauseMasterSocketGone ||
		cause == LossCauseMasterExited || cause == LossCauseStoreRefused:
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
	// LossCauseStoreRefused is the store refusing a lifecycle frame on every
	// attempt (ADR-0077): this coordinator's channel to the shell is halted,
	// though the shell's own is fine.
	LossCauseStoreRefused = "store-refused"
	// LossCauseStartupBound is the AXIS's own cause, not a transport's: the
	// session sat at `starting` for one whole handshake bound and no transport
	// said anything, so the axis answered for it (nocx-1ebrs.20). It maps to
	// the same wire answer as LossCauseHelloTimeout and is spelled separately
	// so a reader of the log can tell "the channel said so" from "nothing
	// said anything", which is the difference between a working detector and a
	// broken one.
	LossCauseStartupBound = "startup-bound"
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
	// A domain went live: whatever the answer standing here was, a transport
	// proved the shell, so nothing may be read as "the net's".
	st.byBound = false
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
//
// Emits are serialized from the read to the write (integrationEmitMu), so the
// order a subscriber hears them in is the order the axis moved in: every
// change is followed by an emit, and whichever emit runs last read the axis
// after the last change.
func (s *WSServer) emitIntegration(sid session.ID) {
	s.integrationEmitMu.Lock()
	defer s.integrationEmitMu.Unlock()
	s.integrationMu.Lock()
	st, ok := s.integrations[sid]
	var snap integrationStatus
	if ok {
		snap = *st
		// THE BOUND ENDS WITH THE QUESTION IT BOUNDS. This function runs once
		// after every actual change to the axis (see below), so an axis that
		// is no longer `starting` has its answer and needs no timer; one that
		// is still `starting` keeps the timer it was armed with.
		if st.status != IntegrationStarting {
			s.stopStartupBoundLocked(sid)
		}
	}
	// AwaitIntegration's wake, from inside the same lock that just read the
	// snapshot above: this function runs once after every actual change to
	// the axis (RegisterIntegration's caller, then noteIntegrationLive,
	// applyIntegrationLoss, applyShellReplaced and applyBootstrapOutcome each
	// call it exactly when they changed something), so it is the one place a
	// waiter needs to be told to look again — never a second reader deciding
	// on its own when the axis has "really" moved.
	waiters := s.integrationWaiters[sid]
	delete(s.integrationWaiters, sid)
	s.integrationMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
	if !ok {
		return
	}
	// Every branch below says what happened to the notification, the way
	// lifecycle.changed's do (nocx-n14oo.8): a pane left on "Checking this
	// shell…" otherwise has a log in which "sent", "dropped" and "never
	// produced" all read as silence.
	rx := s.getRx(sid)
	if rx == nil {
		s.log.Debug("session.integrationChanged dropped: the session has no receiver",
			"session", string(sid), "status", snap.status)
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		s.log.Debug("session.integrationChanged dropped: no subscriber; the next attach replays it",
			"session", string(sid), "status", snap.status)
		return
	}
	// The identity the renderer compares observations against (nocx-3oupk):
	// minted by the backend at open, never here. A session that has left
	// the registry is gone — its teardown is already racing the exit
	// notification, so there is nobody honest to address a status to, and
	// the notification is dropped rather than sent without identity.
	sess, err := s.registry.Get(sid)
	if err != nil {
		s.log.Debug("session.integrationChanged dropped: the session left the registry",
			"session", string(sid), "status", snap.status)
		return
	}
	ident := sess.Identity()
	params := integrationChangedParams{
		SessionID:    string(sid),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		Status:       snap.status,
		Shell:        snap.shell,
		ShellVersion: snap.shellVersion,
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
		return
	}
	s.log.Debug("session.integrationChanged sent", "session", string(sid), "status", snap.status,
		"instance", params.InstanceID, "epoch", params.SessionEpoch)
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
