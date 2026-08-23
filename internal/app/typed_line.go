package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/ssh/mux"
	"github.com/shady2k/nocx/internal/transport"
)

// The typed-`ssh` path: a host reached by typing `ssh` by hand comes up
// integrated, through a connection nocx owns (ADR-0035, design §4.3).
//
// What the binding texts already decided, before this says what it builds.
// AD-6 gives the backend the PTY and forbids it to sniff the byte stream;
// design §5.5 is the one bounded exception, written into AD-6 and ADR-0024
// rather than assumed, and internal/session's bootstrap window is where this
// file touches it. AD-8 puts variation behind an interface at one composition
// root, which is this file. ADR-0015 makes `ssh -G` the oracle for the user's
// configuration, so the decision to interpose at all is
// internal/ssh.TypedWrapper's and not composed here. ADR-0025 fixed the
// domain request at host/user/port and refused a pass-through for the user's
// options; this does not widen it — the options come from the shell's own
// collection of the line the user typed, which is what ADR-0025's third field
// already carries.
//
// # The shape
//
// The user's own `ssh` process IS the multiplex master and IS the interactive
// session. We add two options to their line and nothing else, so the agent,
// ProxyJump, an interactive password, keyboard-interactive and 2FA, the
// host-key prompt, identity selection, port forwards, their own -F and -o and
// the process's exit status all keep working: none of them was
// reimplemented. Auxiliary channels for the publish are opened on that
// connection AFTER ownership is proven, over the multiplex socket, and there
// is never a second interactive session and never a second authentication.
//
// # The frames, and why they travel on the pty
//
// The far-side loader reads its stdin. Its stdin is the `ssh` client's stdin,
// which is the parent shell's stdin, which is the pty this backend owns — so
// the frames go there, and the loader's tokens come back there. A separate
// channel cannot carry them: the receiver and the interactive shell would be
// different children of sshd, and a value passes between them only through
// the carriers D4 forbids (design §5.1).

const (
	// typedOwnershipDeadline bounds the wait for the multiplex socket to
	// answer our handshake. It is generous because what happens in it is
	// the USER authenticating — a host-key confirmation, a password, a
	// second factor — and none of that is ours to hurry.
	typedOwnershipDeadline = 5 * time.Minute
	// typedOwnershipPoll is how often the socket is asked. The master binds
	// its listener immediately after authentication (the spike measured the
	// socket answering about a millisecond BEFORE the remote command
	// starts), so this only decides how quickly we notice.
	typedOwnershipPoll = 20 * time.Millisecond
	// typedPublishDeadline bounds the auxiliary publish. It is a bound on
	// OUR work, never a claim about the remote host.
	typedPublishDeadline = 60 * time.Second
	// typedBootstrapDeadline bounds the whole frame exchange once ownership
	// is proven.
	typedBootstrapDeadline = 60 * time.Second
)

// ---------------------------------------------------------------------------
// Seams

// TypedMaster is a proven-owned multiplex master, behind an interface so the
// typed path can be driven without an OpenSSH master in the test (AD-8).
type TypedMaster interface {
	// PID is the master process, as the master itself reported it.
	PID() int
	// Aux opens one auxiliary channel on the master's connection. A refusal
	// is a refusal: the adapter never opens a connection of its own.
	Aux(req mux.SessionRequest) (io.ReadWriteCloser, error)
	// Exit asks the master to terminate.
	Exit() error
	// Close drops this client's control connection without stopping the
	// master.
	Close() error
}

// TypedMuxDialer proves ownership of a control socket, or fails. It is the
// event design §6.2 calls "the successful mux handshake": before it returns,
// nothing may be published and no remote state touched.
type TypedMuxDialer func(controlPath string) (TypedMaster, error)

// realMuxMaster adapts internal/ssh/mux to the seam.
type realMuxMaster struct{ m *mux.Master }

func (r realMuxMaster) PID() int    { return r.m.PID() }
func (r realMuxMaster) Exit() error { return r.m.Exit() }
func (r realMuxMaster) Close() error {
	return r.m.Close()
}

func (r realMuxMaster) Aux(req mux.SessionRequest) (io.ReadWriteCloser, error) {
	s, err := r.m.Session(req)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// DialTypedMux is the production dialer.
func DialTypedMux(controlPath string) (TypedMaster, error) {
	m, err := mux.Open(controlPath)
	if err != nil {
		return nil, err
	}
	return realMuxMaster{m: m}, nil
}

// typedPublisher publishes the bundle over an auxiliary channel.
type typedPublisher interface {
	EnsureInstalledOverPipe(ctx context.Context, rw io.ReadWriteCloser, remoteHome string) error
}

// typedSessions opens the bootstrap window on the session that owns the
// terminal the frames travel on. One method, deliberately: what this path
// needs of a session is the window and nothing else, and a seam that asked
// for the whole Session would be asking for authority it does not use.
type typedSessions interface {
	OpenBootstrapWindow(id session.ID) (session.BootstrapWindow, error)
}

// sessionWindows adapts the session registry to that seam.
type sessionWindows struct{ reg *session.Reg }

func (s sessionWindows) OpenBootstrapWindow(id session.ID) (session.BootstrapWindow, error) {
	sess, err := s.reg.Get(id)
	if err != nil {
		return nil, err
	}
	return sess.OpenBootstrapWindow()
}

// typedRunner holds everything the typed path needs beyond one grant.
type typedRunner struct {
	log      log.Logger
	wrapper  *ssh.TypedWrapper
	dial     TypedMuxDialer
	publish  typedPublisher
	sessions typedSessions
	// probes are the master observations §6.2's loss events are detected
	// with. Injected so a test can state a loss instead of arranging one.
	probes func(controlPath string, pid int, m TypedMaster) ssh.MasterProbes
	// reportIntegration and reportBootstrapOutcome are this path's route
	// into the session integration axis, and they are the SAME two seams the
	// saved path uses — the launch side saying what it started, and the
	// bootstrap saying how the far side answered (ws_integration.go names
	// both). The composition root binds them in bindTypedIntegrationAxis.
	//
	// Without them this path reached a terminal outcome and lg.Warn'd it.
	// The refusal vocabulary was opened from seven members to thirty-one so
	// that a user could be told WHICH refusal happened, and on the typed
	// path they were told nothing while the same refusal on the saved path
	// was a structured reason — a soft degrade the UI contradicts, which is
	// how a feature that does not exist survives a release (AGENTS.md).
	//
	// Nil reports nowhere, which is the state before this and the safe
	// direction: a delivery driven without a composition root still runs.
	reportIntegration      func(sid, shell, status string, reason ssh.RefusalReason)
	reportBootstrapOutcome func(lane string, reason ssh.RefusalReason)
}

// bindTypedIntegrationAxis routes the two facts a typed delivery produces into
// the session integration axis. It exists as a function rather than two
// assignments at the composition root so that a test asserting the ROUTE
// exercises the production wiring instead of a copy of it (AGENTS.md rule 5:
// a check against a copy the test itself wrote proves the copy).
func bindTypedIntegrationAxis(typed *typedRunner, axis typedIntegrationAxis) {
	if typed == nil || axis == nil {
		return
	}
	typed.reportIntegration = func(sid, shell, status string, reason ssh.RefusalReason) {
		axis.RegisterIntegration(session.ID(sid), shell, status, reason)
	}
	typed.reportBootstrapOutcome = func(lane string, reason ssh.RefusalReason) {
		if lane == "" {
			return
		}
		axis.NoteBootstrapOutcome(lifecycle.LaneID(lane), reason)
	}
}

// typedIntegrationAxis is the transport's half of that route, behind an
// interface for AD-8's reason: this file must not decide anything about the
// wire, and the two methods are the whole of what it needs.
type typedIntegrationAxis interface {
	RegisterIntegration(sid session.ID, shell string, status string, reason ssh.RefusalReason)
	NoteBootstrapOutcome(lane lifecycle.LaneID, reason ssh.RefusalReason)
}

// ---------------------------------------------------------------------------
// The decision

// composeSSHLine renders the invocation the parent shell runs:
//
//	ssh <our multiplex options> <what the caller must add> <the user's own
//	    options, in their order> [-p N] <destination> [<remote command>]
//
// The order is not decorative. Our options come first so an insertion can
// never split a user option from its value, and the user's come last among
// the options so that where OpenSSH lets the last occurrence win, theirs
// does.
//
// Quoted one token at a time, never joined and quoted once: each token is a
// separate argv entry on the user's side and must stay one here. This line is
// REBUILT rather than edited, so anything not carried here is not merely
// reordered — it is gone, which is how a user's `-i ~/.ssh/prod -J bastion`
// once connected with the wrong key and no jump host at all (nocx-c6z0).
func composeSSHLine(wrap ssh.TypedWrap, extra []string, inv ssh.TypedInvocation, remoteCommand string) string {
	var b strings.Builder
	b.WriteString("ssh")
	// Ours are quoted for the same reason theirs are: this text is evaluated
	// by the parent's shell, and a control root under a $TMPDIR with a space
	// in it would otherwise become two argv words.
	for _, o := range wrap.MuxOptions {
		b.WriteString(" ")
		b.WriteString(shellintegration.ShellQuote(o))
	}
	for _, o := range extra {
		b.WriteString(" ")
		b.WriteString(shellintegration.ShellQuote(o))
	}
	for _, o := range inv.Opts {
		b.WriteString(" ")
		b.WriteString(shellintegration.ShellQuote(o))
	}
	if inv.Port != 0 {
		b.WriteString(" -p ")
		b.WriteString(strconv.Itoa(inv.Port))
	}
	b.WriteString(" ")
	b.WriteString(shellintegration.ShellQuote(inv.Destination()))
	if remoteCommand != "" {
		b.WriteString(" ")
		b.WriteString(shellintegration.ShellQuote(remoteCommand))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The run

// typedDelivery is one typed session's delivery, armed BEFORE the grant is
// handed to the parent shell and run once the line is going.
type typedDelivery struct {
	runner      *typedRunner
	sessionID   string
	lane        string
	controlPath string
	plan        shellintegration.BootstrapPlan
	window      session.BootstrapWindow
	// publishSettled closes when the publish attempt has reached a terminal
	// outcome — committed, unchanged, failed or contended. Design §6.1 step
	// 5: the secret is minted only after it, so a stage-1 that verifies
	// microseconds before an atomic commit cannot degrade a session whose
	// publish then succeeds.
	publishSettled chan struct{}
	once           sync.Once
	// publishErr is what the publish attempt ended with, or nil. Written by
	// publish before publishSettled closes and read after the bootstrap that
	// waited on publishSettled has finished, which is the same happens-before
	// the saved path gets from its mint gate.
	publishErr atomic.Pointer[error]
}

// arm opens the bootstrap window on the parent's session. It runs
// SYNCHRONOUSLY, before the grant is delivered, and that is the whole reason
// it is a separate step: once the parent has the line it can start `ssh` at
// once, and a window opened afterwards could miss the loader's readiness
// token and leave the far side blocked on a frame nobody would send.
//
// The window's READ side opens here. Its input quarantine does NOT — that
// waits for ownership proof, because everything the user types until then is
// theirs, addressed to their own ssh client (design §5.3).
func (r *typedRunner) arm(sessionID, lane, controlPath string, plan shellintegration.BootstrapPlan) (*typedDelivery, error) {
	w, err := r.sessions.OpenBootstrapWindow(session.ID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("typed ssh: the parent session's terminal is not available: %w", err)
	}
	// The session's axis opens a NEW attempt here, and it has to be opened
	// for the outcome below to land at all: the axis answers once per
	// registration, and this session's answer is already in — the parent's
	// own local shell integrated and the axis says so. A report arriving
	// against that answer is dropped, by the rule ws_integration.go states
	// three times over ("a session already answered keeps its first
	// answer"), so routing the outcome without this line would be a call
	// that reaches a drop: the worst shape of all, because it looks routed.
	//
	// What is being said is true and is the launch side's own sentence: a
	// second shell is starting in this session, this is what nocx asked the
	// far host to run, and it has not proved itself yet. `auto` is the
	// honest shell name for the same reason remoteShellName gives it on the
	// saved path — the carrier detects the login shell AT THE FAR END, so
	// nocx did not choose one and must not name one.
	if r.reportIntegration != nil {
		r.reportIntegration(sessionID, string(ssh.ShellAuto), transport.IntegrationStarting, ssh.ReasonNone)
	}
	return &typedDelivery{
		runner:         r,
		sessionID:      sessionID,
		lane:           lane,
		controlPath:    controlPath,
		plan:           plan,
		window:         w,
		publishSettled: make(chan struct{}),
	}, nil
}

// reportOutcome carries one terminal outcome to the session integration axis.
//
// Every exit from doRun goes through it, including the one where ownership was
// never proven: §7 forbids `starting` from being permanent, and arm has just
// put this session there. An outcome that only reached the log would leave the
// axis at `starting` with nothing else coming — which is the same defect this
// whole route exists to close, wearing the opposite face.
func (d *typedDelivery) reportOutcome(reason ssh.RefusalReason) {
	if d.runner.reportBootstrapOutcome == nil {
		return
	}
	d.runner.reportBootstrapOutcome(d.lane, reason)
}

// run drives the whole delivery to one terminal outcome and closes the
// ownership interval behind it. It is the §6.1 order, in order.
func (d *typedDelivery) run(ctx context.Context) {
	d.once.Do(func() { d.doRun(ctx) })
}

func (d *typedDelivery) doRun(ctx context.Context) {
	lg := d.runner.log
	defer func() { _ = d.window.Close() }()

	// 1. Ownership proven — and NOT ASSUMED. Nothing before this line
	//    publishes, mints or touches remote state; what happens in this wait
	//    is the user authenticating to their own client.
	master, err := d.awaitOwnership(ctx)
	if err != nil {
		lg.Warn("typed ssh: ownership of the control socket was never proven; the session stays a plain ssh",
			"session_id", d.sessionID, "socket", d.controlPath, "error", err)
		// The far side may still be our loader, blocked on a header we are
		// never going to send — and a blocked loader eats the user's next
		// keystrokes as frame bytes. This is the one thing we may still
		// write: bytes it must refuse, so it names its outcome and execs a
		// native login shell with its stdin intact.
		if _, werr := d.window.Write(shellintegration.AbortFrame()); werr != nil {
			lg.Debug("typed ssh: the abort frame could not be written", "error", werr)
		}
		// §6.4's `channel` row: the channel nocx owns never came up, so
		// nothing was published, nothing was minted and the far side was
		// handed a non-secret refusal. The user has a working prompt on
		// their own connection and is entitled to know why it is not
		// integrated.
		d.reportOutcome(ssh.ReasonChannelUnavailable)
		return
	}
	defer func() { _ = master.Close() }()

	// The input quarantine opens HERE and not earlier (design §5.3): from
	// now until the terminal outcome the user's keystrokes are refused, not
	// buffered.
	d.window.QuarantineInput()

	own := ssh.NewOwnership(lg, d.controlPath, master.PID(),
		d.runner.probes(d.controlPath, master.PID(), master), ssh.SystemClock{})
	own.MarkProven()
	defer own.Close(context.WithoutCancel(ctx))

	// 2. The publish and the loader run CONCURRENTLY (design §6.1 step 2).
	//    The publish's terminal outcome is what releases the mint, which is
	//    step 5 expressed as code rather than as a comment.
	go d.publish(ctx, own, master)

	// 3-9. The frames, under the frame protocol.
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), typedBootstrapDeadline)
	defer cancel()
	outcome := shellintegration.DeliverBootstrap(bctx, lg, d.window, d.plan)
	// The same joining rule the saved path uses, in the same function: a far
	// side that found no generation AFTER a failed publish is told the cause
	// rather than the symptom (§6.4's subsystem row). publishSettled has
	// closed by now — plan.Ordered waited on it before frame 2 — so the
	// pointer read here is ordered after the write.
	var publishErr error
	if perr := d.publishErr.Load(); perr != nil {
		publishErr = *perr
	}
	reason := bootstrapProductReason(lg, outcome, publishErr)
	if outcome == shellintegration.OutcomeBootstrapAccepted {
		own.MarkIntegrated()
		lg.Info("typed ssh: the session came up integrated on the user's own connection",
			"session_id", d.sessionID, "socket", d.controlPath)
	} else {
		lg.Warn("typed ssh: the session did not integrate; the far side is at a native login shell",
			"session_id", d.sessionID, "outcome", string(outcome), "reason", string(reason))
	}
	// Reported on BOTH arms. An accepted bootstrap reports ReasonNone, which
	// the axis deliberately treats as "leave it alone" — "a domain is live"
	// is the kernel's word and this is not it — so the call is not a
	// conditional here either.
	d.reportOutcome(reason)

	// §5.3'S CLOSING EDGE, AND IT IS THE OUTCOME — not the end of the
	// ownership interval, which is a different interval with a different
	// end. The design says it in two places and in as many words: the input
	// interval "closes at exactly one terminal outcome — BOOTSTRAP_ACCEPTED
	// or BOOTSTRAP_REFUSED(reason)", and the reader's one effect on input is
	// "to end the quarantine the bootstrap opened".
	//
	// Only the deferred Close stood here, and a deferred Close runs after
	// awaitMasterEnd — so the quarantine outlived the outcome by the whole
	// life of the user's master. A session that had JUST come up integrated
	// dropped every keystroke and told the user so: "this connection has
	// stopped accepting input". Measured on the epic's own journey, where
	// `echo journey-1-ok` was refused 0.4 s after the log said the session
	// came up integrated.
	//
	// Idempotent (the window's Close is a sync.Once), so the defer above
	// stays exactly as it is — it is the closing edge for every path that
	// returns before an outcome exists.
	_ = d.window.Close()
	// AND THE CONTROL CONNECTION GOES WITH IT, for a reason that is not
	// tidiness. An attached mux client is an open channel on the master's
	// connection and `ssh` does not exit while one is open — so the
	// connection Open took to PROVE ownership, held until the deferred
	// Close below, kept the user's own `ssh` running after their far shell
	// had exited, and their local shell waiting on it. Measured on
	// 2026-08-21 in e2e/nocxify-journey.spec.ts: `exit` on the far side, and
	// the master still alive 20.1 s later; released here instead, the two
	// events are 65 ms apart and the remote command that preceded them fell
	// from 19 s to 49 ms.
	//
	// Everything after this line still works without it: awaitMasterEnd
	// probes the PROCESS and the SOCKET FILE, and mux.Master.Exit dials its
	// own connection exactly as Alive does. Close is idempotent, so the
	// defer above remains the closing edge for the paths that return early.
	_ = master.Close()

	// The ownership interval closes when the last owned session and
	// auxiliary channel have finished. The user's own process is still the
	// master and still theirs; what ends here is OUR use of it.
	d.awaitMasterEnd(ctx, own)
}

// awaitOwnership polls the control socket until it completes our handshake.
// The poll is the only thing a duration decides: the EVENT is the handshake
// succeeding, and the deadline exists so a line that never connects does not
// leave a goroutine behind.
func (d *typedDelivery) awaitOwnership(ctx context.Context) (TypedMaster, error) {
	deadline := time.NewTimer(typedOwnershipDeadline)
	defer deadline.Stop()
	tick := time.NewTicker(typedOwnershipPoll)
	defer tick.Stop()
	var last error
	for {
		m, err := d.runner.dial(d.controlPath)
		if err == nil {
			return m, nil
		}
		last = err
		select {
		case <-tick.C:
		case <-deadline.C:
			return nil, fmt.Errorf("the control socket never completed the handshake: %w", last)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// publish opens the auxiliary channels and publishes the bundle, then closes
// publishSettled WHATEVER HAPPENED. That is deliberate: design §6.1 step 5
// waits for the publish to reach a TERMINAL OUTCOME, not for it to succeed —
// after a failed publish the far side may still accept a generation installed
// earlier, so a failed publish is not a refusal.
func (d *typedDelivery) publish(ctx context.Context, own *ssh.Ownership, master TypedMaster) {
	var perr error
	defer func() {
		// The error is stored BEFORE publishSettled closes, so the gate the
		// bootstrap waits on is also the happens-before that lets the
		// outcome read it. Stored on every path including success, so "not
		// answered yet" is never mistaken for "answered nil".
		d.publishErr.Store(&perr)
		close(d.publishSettled)
	}()
	lg := d.runner.log
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), typedPublishDeadline)
	defer cancel()

	home, err := typedRemoteHome(master)
	if err != nil {
		perr = err
		d.reportAuxFailure(own, "the far side's home directory could not be read", err)
		return
	}
	aux, err := master.Aux(mux.SessionRequest{Subsystem: true, Command: "sftp"})
	if err != nil {
		perr = err
		d.reportAuxFailure(own, "the subsystem session was refused; nothing is published", err)
		return
	}
	own.Own(aux)
	if err := d.runner.publish.EnsureInstalledOverPipe(pctx, aux, home); err != nil {
		perr = err
		lg.Warn("typed ssh: the publish did not commit; the far side decides whether an earlier generation is still valid",
			"session_id", d.sessionID, "error", err)
		return
	}
}

// reportAuxFailure records an auxiliary-channel failure. A refused subsystem
// is §6.4's `publish-unavailable` row: nothing written, native shell, named
// reason — never a fallback that opens a connection of its own, which is the
// second authentication D3 exists to prevent.
func (d *typedDelivery) reportAuxFailure(own *ssh.Ownership, what string, err error) {
	if errors.Is(err, mux.ErrSessionRefused) {
		d.runner.log.Warn("typed ssh: the master refused an auxiliary channel; nothing is published and no connection is opened",
			"session_id", d.sessionID, "what", what, "error", err)
		return
	}
	// A transport that is gone is one of §6.2's three events, and it is the
	// only one observable from here: the master answers and its socket is
	// there, and a channel on it still failed.
	if err != nil && !errors.Is(err, mux.ErrSessionRefused) {
		own.ReportTransportLoss(err)
	}
	d.runner.log.Warn("typed ssh: an auxiliary channel failed",
		"session_id", d.sessionID, "what", what, "error", err)
}

// awaitMasterEnd waits for the user's own process to finish with its
// connection and then closes the ownership interval. The interval's closing
// event is §6.2's, and its cleanup is bounded at ssh.MasterCleanupBound.
func (d *typedDelivery) awaitMasterEnd(ctx context.Context, own *ssh.Ownership) {
	tick := time.NewTicker(typedOwnershipPoll * 25)
	defer tick.Stop()
	for {
		if outcome, lost := own.Detect(); lost {
			d.runner.log.Info("typed ssh: the master's ownership interval ended",
				"session_id", d.sessionID, "event", string(outcome.Event),
				"phase", own.Phase().String(),
				"reason", string(outcome.Reason), "ends_session", outcome.EndsSession)
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return
		}
	}
}

// typedRemoteHome asks the far side where $HOME is, over one auxiliary
// channel of the connection nocx owns. It is the same question
// GetRemoteHome asks over a *gossh.Client, over the transport this path has.
func typedRemoteHome(master TypedMaster) (string, error) {
	aux, err := master.Aux(mux.SessionRequest{Command: "echo $HOME"})
	if err != nil {
		return "", err
	}
	defer func() { _ = aux.Close() }()
	out, err := io.ReadAll(io.LimitReader(aux, 4096))
	if err != nil && len(out) == 0 {
		return "", err
	}
	home := strings.TrimSpace(string(out))
	if home == "" {
		return "", fmt.Errorf("typed ssh: the far side reported no home directory")
	}
	return home, nil
}

// defaultMasterProbes are the production observations for §6.2's three loss
// events: the socket file, the master process, and (reported by whoever holds
// a channel) the transport.
func defaultMasterProbes(controlPath string, _ int, master TypedMaster) ssh.MasterProbes {
	return ssh.MasterProbes{
		SocketPresent: socketPresent,
		ProcessAlive:  processAlive,
		Terminate:     master.Exit,
	}
}
