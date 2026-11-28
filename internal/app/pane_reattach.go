package app

// A dropped SSH connection comes back INTO THE SAME SHELL (design §5.4, C16;
// ADR-ydcx00yd decides the ask order and where the route comes from).
//
// WHAT THIS ADDS, and what it deliberately does not. `ReconnectPane` in
// internal/transport replaces a pane's held shell: it ends what the pane had
// and runs the ordinary open again. That is right for every pane whose shell
// THIS machine owns — a local PTY with a local helper is born again as a new
// PTY — and wrong for the one case the epic is about: a helper on the far host
// keeps the shell through the drop, and a replacement is a SECOND shell beside
// a live one, whose cmd is now the prompt somebody left.
//
// So the transport asks one question before it ends or spawns anything, and
// this file is the answer. Three answers, and C16 turns on telling them apart:
//
//	taken back        the helper answered, holds it, and it is in the registry
//	                  again under the SAME id — nothing was spawned
//	confirmed absent  the helper ANSWERED and does not hold it: the only
//	                  answer that justifies another shell
//	nobody could ask  no carrier, an unreachable host, a generation that is
//	                  gone, another coordinator holding the keyboard — the
//	                  open is refused and the pane waits. A slow helper costs a
//	                  wait; it must never cost a second shell.
//
// WHERE THE ROUTE COMES FROM, which is the one thing this path has that a cold
// start does not. After a transport loss the process is the SAME process: the
// session object is gone (its channel died), its durable binding is still in
// the content store, and — decisively — the hosted carrier that opened it is
// still registered, because carrier loss alone does not end a far session
// (hosted_shell_carrier.go, D1). That carrier holds the binding the open
// actually used: the host, the account, the host-key pin and the connection
// OPTIONS, which are the complete route a saved profile and a typed host both
// reduce to. Re-deriving a route from a profile would ask a store that may have
// been edited since, and a route resolved differently is a different machine
// (nocx-k6p18.15). The carrier is also the only object that can still reach the
// helper's inventory, which is the question C16 is asked of.
//
// The ADOPTION is not reimplemented here. `readoptPass.readopt` is the one
// attach-and-adopt step, already used by restart reconciliation for both
// carriers, and this path hands it the same inputs an ordinary re-adoption
// gets: the captured inventory entry, the config, and the connection the
// attachment rides. A second attach path would agree with that one until the
// first of them moved (AGENTS.md, "look for the existing answer").

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// paneShellReattacher answers the transport's reconnection question for a far
// helper's shell.
type paneShellReattacher struct {
	registry *helperRegistry
	// opener is the local-hosted opener, and it is here for one act: naming
	// the pane's connection again once the shell is back. The connection the
	// dropped transport used is gone; the supervisor has just opened a new
	// one, and the pane's driver has to be told which it is before the
	// `pane.opened` this open produces can carry a generation that names it.
	opener *localHelperOpener
	// pass is the re-adoption step this path shares with restart
	// reconciliation. Its `local` collaborator is deliberately nil: the pane
	// binding below is made against the CARRIER's options, which this path
	// has and a resolved config does not.
	pass *readoptPass
	// bindings reads what the dead session's binding recorded: the lifecycle
	// offset its attachment had applied (ADR-0077) and the ROUTE its open
	// dialled (design §5.4). Nil is a legitimate wiring — a stubbed store —
	// and then the resume point is the helper's own window head and a shell
	// this machine's helper holds can still be asked about, but not re-dialled
	// from a recorded route.
	bindings sessionBindingReader
	// timeout bounds one attempt. A reconnect is an automatic action nobody
	// watches, so it must cost a pane a wait and never a stop.
	timeout time.Duration
}

// sessionBindingReader is the two reads this file needs from the store that
// keeps the durable binding: where the dead attachment stopped applying its
// lifecycle stream, and the complete route its open resolved.
type sessionBindingReader interface {
	LifecycleCursor(ctx context.Context, sessionID string) (uint64, bool, error)
	SessionRoute(ctx context.Context, sessionID string) (*content.SessionRoute, bool, error)
}

var _ transport.PaneShellReattacher = (*paneShellReattacher)(nil)

// ReattachPaneShell takes the shell named by spec.Reattach back for this open.
func (r *paneShellReattacher) ReattachPaneShell(ctx context.Context, spec transport.OpenSpec, cfg session.Config) (transport.PaneShellReattach, bool, error) {
	ref := spec.Reattach
	if ref == nil || ref.Session == "" {
		return transport.PaneShellReattach{}, false, nil
	}
	sid := ref.Session
	carrier := r.carrierFor(sid)
	if carrier == nil {
		// THE SHELL MAY BE THIS MACHINE'S HELPER'S, and then there is no far
		// carrier to find: a pane from a typed host whose far helper declined
		// is a shell channel the daemon on THIS machine holds (nocx-50w7p.5),
		// and the route back to it is the binding its open recorded. Asking
		// that daemon is a different carrier, not a different decision.
		if r.pass != nil && r.pass.local != nil && ref.Generation != "" {
			return r.reattachLocal(ctx, spec, ref)
		}
		// THE PROCESS IS HOLDING NOTHING FOR THIS SHELL, so nobody can be
		// asked. It is NOT "gone": that verdict needs an inventory that
		// answered, and inventing it here would replace a shell nobody
		// looked for — the exact trade C16 forbids.
		return transport.PaneShellReattach{}, false, &transport.PaneShellWait{Reason: "nocx holds no connection to the helper that was holding this pane's shell, so it cannot say whether the shell is still running; nothing was opened"}
	}
	// THE ROUTE MUST STILL LEAD WHERE IT LED. The carrier is the route the
	// shell was OPENED with; the config beside it was resolved moments ago from
	// the current profile. A profile edited to another machine between the loss
	// and the reconnect would otherwise ask the old host and then, on an
	// "absent" answer, spawn a replacement at the NEW one — a silent move of a
	// session to a machine nobody recorded (design §5.4, nocx-k6p18.15).
	if err := routeUnchanged(carrier.f.host, carrier.f.opts, cfg); err != nil {
		return transport.PaneShellReattach{}, false, err
	}
	bound := r.timeout
	if bound <= 0 {
		bound = readoptAttemptTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	entries, err := r.inventory(ctx, carrier)
	if err != nil {
		// A WAIT, NEVER A VERDICT (design §5.4, C16): an inventory that timed
		// out or would not answer says nothing about the shell, so the pane
		// keeps its one shell and the automatic loop asks again. This is the
		// part of C16 that made a slow helper cost a person a second shell
		// beside a live one.
		return transport.PaneShellReattach{}, false, &transport.PaneShellWait{Reason: fmt.Sprintf(
			"asking the helper that holds this pane's shell what it holds: %v", err)}
	}
	mine := entryFor(entries, carrier)
	if mine == nil {
		// THE HELPER ANSWERED AND DOES NOT HOLD IT — C16's one path to a
		// replacement shell. The carrier is over for the same reason: the
		// generation it named answered about its own id space and this
		// session is not in it.
		r.registry.SessionEnded(sid)
		return transport.PaneShellReattach{}, false, nil
	}

	cfg.Kind = session.KindRemote
	cfg.Host = carrier.f.host
	cfg.PaneID = spec.PaneID
	cfg.ProfileID = spec.ProfileID
	cfg.Cwd = launchCwd(mine)
	// THE SAME SHELL DID NOT RE-OPEN, so its pipe's moment is the helper's own
	// record of when it did — never the reconnect's (the rule the restart path
	// states at pipeOpenedAt).
	cfg.OpenedAt = pipeOpenedAt(mine, time.Now())
	// THE ROUTE IS THE CARRIER'S OPTIONS, not a config rebuilt from a profile.
	// They are what the open dialled with and what the helper's bridge was
	// selected through; a re-resolved profile could name another machine.
	cfg.Options = carrier.f.opts
	cfg.Remote = nil

	p := content.PendingSession{
		SessionID:     string(sid),
		Host:          carrier.f.host,
		Account:       carrier.f.account,
		Generation:    carrier.f.install.generation,
		HelperCommand: carrier.f.install.command,
		Fingerprint:   carrier.f.fp,
		PaneID:        spec.PaneID,
		ProfileID:     spec.ProfileID,
		Since:         time.Now(),
	}
	if applied, ok := r.appliedCursor(ctx, sid); ok {
		p.LifecycleApplied = &applied
	}

	carrier.mu.Lock()
	c, outcome, connErr := carrier.connectLocked(ctx)
	carrier.mu.Unlock()
	if connErr != nil {
		// The same wait, one step earlier: the helper could not be reached, so
		// it was not asked.
		return transport.PaneShellReattach{}, false, &transport.PaneShellWait{Reason: fmt.Sprintf(
			"reaching the helper that holds this pane's shell: %v", connErr)}
	}
	if outcome.State != "" {
		return transport.PaneShellReattach{}, false, fmt.Errorf("the helper holding this pane's shell refused: %s", outcome.Message)
	}
	taken, err := r.pass.readopt(ctx, p, cfg, carrier, c, *mine)
	if err != nil {
		// THE SHELL IS LIVE and this coordinator could not take it. Refused
		// rather than replaced: the shell exists, so a new one beside it is
		// the second shell C16 forbids, and the pane keeps waiting with the
		// reason attached.
		return transport.PaneShellReattach{}, false, fmt.Errorf(
			"taking this pane's running shell back from %s: %w", reattachTarget(p), err)
	}
	if err := r.bindPaneConnection(ctx, cfg, carrier, taken.Session); err != nil {
		// Naming the connection may refuse panels; it must never cost the
		// shell that survived the drop — the same trade the restart path
		// makes at readoptSSHConnection.
		carrierLog(r).Warn("re-attached session could not bind its pane connection; the shell remains available",
			"session_id", string(sid), "pane_id", cfg.PaneID, "error", err)
	}
	return transport.PaneShellReattach{Session: taken.Session, Config: cfg, Hosted: taken}, true, nil
}

// carrierFor is the hosted carrier this coordinator still holds for a session,
// or nil. A carrier that ended — the helper reported the shell's end — is
// already forgotten by the registry, which is why nil here is "nothing is
// known" rather than "the helper said no".
func (r *paneShellReattacher) carrierFor(sid session.ID) *hostedShellCarrier {
	if r == nil || r.registry == nil {
		return nil
	}
	r.registry.mu.Lock()
	defer r.registry.mu.Unlock()
	return r.registry.hosts[sid]
}

// inventory asks the helper once what it holds. A failure is an ERROR and never
// an empty answer: "the host did not answer" and "the host holds nothing" are
// the two facts C16 exists to separate.
func (r *paneShellReattacher) inventory(ctx context.Context, carrier *hostedShellCarrier) ([]client.SessionEntry, error) {
	carrier.mu.Lock()
	c, outcome, err := carrier.connectLocked(ctx)
	carrier.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if outcome.State != "" {
		return nil, errors.New(outcome.Message)
	}
	return c.Sessions(ctx)
}

// entryFor is this session's row in an inventory, judged by the ONE rule the
// restart path uses (readoptPass.Readopt): the generation qualifies an id
// space, and only an entry on this generation's space may answer about this
// session. An entry from another generation is somebody else's id, and
// matching on the bare id would adopt a stranger's shell.
func entryFor(entries []client.SessionEntry, carrier *hostedShellCarrier) *client.SessionEntry {
	if carrier == nil || carrier.f == nil {
		return nil
	}
	want := carrier.f.sid
	generation := carrier.f.install.generation
	for i := range entries {
		if entries[i].HostSessionID.Generation == generation && entries[i].HostSessionID.Session == string(want) {
			return &entries[i]
		}
	}
	return nil
}

// appliedCursor is where the dead attachment stopped applying lifecycle
// frames, when the store records one.
func (r *paneShellReattacher) appliedCursor(ctx context.Context, sid session.ID) (uint64, bool) {
	if r == nil || r.bindings == nil {
		return 0, false
	}
	applied, ok, err := r.bindings.LifecycleCursor(context.WithoutCancel(ctx), string(sid))
	if err != nil {
		// Not fatal and not silent: the resume falls back to the helper's
		// window head, which re-delivers nothing before it, and a person's
		// blocks are then settled from the helper's own record rather than
		// from this cursor.
		carrierLog(r).Warn("re-attached session: its stored lifecycle cursor could not be read",
			"session_id", string(sid), "error", err)
		return 0, false
	}
	return applied, ok
}

// bindPaneConnection names the connection the supervisor has just restored as
// this pane's, exactly as an ordinary open does.
//
// THE OPTIONS ARE THE CARRIER'S, and that is the whole reason this is not
// readoptSSHConnection: that function resolves the destination from a
// ConnectConfig, and a re-attachment has none — it has the option list the
// original open dialled with, which is the same route in the form the open
// kept it.
func (r *paneShellReattacher) bindPaneConnection(ctx context.Context, cfg session.Config, carrier *hostedShellCarrier, sess session.Session) error {
	if r.opener == nil || cfg.PaneID == "" || r.opener.paneConnections() == nil {
		return nil
	}
	if sess == nil {
		return errors.New("re-attached session has no shell to bind")
	}
	target, err := r.opener.resolveTarget(ctx, carrier.f.host, carrier.f.opts)
	if err != nil {
		return err
	}
	c, _, err := r.opener.connect(ctx)
	if err != nil {
		return err
	}
	binding, err := r.opener.bindSSHConnection(ctx, cfg, c, target)
	if err != nil {
		return err
	}
	// The probe that selected this host recorded the key; a re-attachment has
	// no selection pass of its own, and the carrier kept the answer.
	r.opener.recordConfirmedConnection(binding.connectionID, target, carrier.f.fp)
	r.opener.paneConnections().AdoptSession(cfg.PaneID, binding.connectionID, string(sess.ID()), string(sess.Identity().InstanceID), binding.generation)
	return nil
}

// carrierLog is one nil check for the two diagnostics above.
func carrierLog(r *paneShellReattacher) *slog.Logger { return r.registry.log }

// reattachLocal asks THIS machine's helper whether it still holds the pane's
// shell and, when it does, re-attaches it — the local carrier's half of the
// same question the far route asks its own carrier.
//
// IT IS readoptLocal, ONE CALLER OVER (AGENTS.md, "look for the existing
// answer"): the ask, the capture of the answer, the route check, the attach and
// the adopt are that function's, and this one only supplies what a live
// reconnect knows that a cold start does not — the pane, the shell's own
// generation, and the lifecycle cursor the dead attachment stored.
func (r *paneShellReattacher) reattachLocal(ctx context.Context, spec transport.OpenSpec, ref *transport.PaneShellRef) (transport.PaneShellReattach, bool, error) {
	p := content.PendingSession{
		SessionID:  string(ref.Session),
		Generation: ref.Generation,
		Host:       ref.Host,
		Account:    ref.Account,
		PaneID:     spec.PaneID,
		ProfileID:  spec.ProfileID,
		// The pipe's own opened-at comes from the helper's launch record, the
		// same value the far route reads; the fallback is this attempt.
		Since: time.Now(),
	}
	if route, ok, err := r.recordedRoute(ctx, ref.Session); err != nil {
		return transport.PaneShellReattach{}, false, err
	} else if ok {
		p.Route = route
	}
	if applied, ok := r.appliedCursor(ctx, ref.Session); ok {
		p.LifecycleApplied = &applied
	}
	inv, took, err := r.pass.readoptLocal(ctx, p)
	if err != nil {
		// THE SHELL IS LIVE AND THIS COORDINATOR COULD NOT TAKE IT. Refused,
		// never replaced: a new shell beside a live one is the second shell
		// C16 forbids, and the pane waits with the reason attached.
		return transport.PaneShellReattach{}, false, err
	}
	if inv == nil {
		// NOBODY MAY BE ASKED — no local route, no generation to name. Not
		// "gone": nothing answered.
		return transport.PaneShellReattach{}, false, &transport.PaneShellWait{Reason: "this machine's helper could not be asked whether it still holds this pane's shell; nothing was opened"}
	}
	if took == nil || took.Open.Session == nil {
		// THE DAEMON ANSWERED AND DOES NOT HOLD IT: C16's one path to a
		// replacement shell. readoptLocal has already taken the shell out of
		// the daemon's held set for the same reason.
		return transport.PaneShellReattach{}, false, nil
	}
	return transport.PaneShellReattach{Session: took.Open.Session, Config: took.Config, Hosted: took.Open}, true, nil
}

// recordedRoute is the route the binding recorded at open, when there is one.
// A read failure is not fatal: a shell this machine's helper holds is asked
// about by generation alone, and only the re-dial needs the route — so the
// attempt continues and the refusal, if any, names the route as the reason.
func (r *paneShellReattacher) recordedRoute(ctx context.Context, sid session.ID) (*content.SessionRoute, bool, error) {
	if r == nil || r.bindings == nil {
		return nil, false, nil
	}
	route, ok, err := r.bindings.SessionRoute(context.WithoutCancel(ctx), string(sid))
	if err != nil {
		carrierLog(r).Warn("re-attached session: its recorded route could not be read",
			"session_id", string(sid), "error", err)
		return nil, false, nil
	}
	return route, ok, nil
}

// effectiveSSHPort resolves the default SSH port when omitted.
func effectiveSSHPort(port int) int {
	if port == 0 {
		return 22
	}
	return port
}

// routeUnchanged refuses a re-attachment whose freshly resolved config no
// longer names the destination the shell was opened on.
//
// THE CARRIER'S OPTIONS ARE THE ANSWER, not a second profile lookup: they are
// what the open dialled with. A difference in host, account, port or jump hop
// is a different machine or path, and the sentence says exactly that — the
// shell is not replaced, because a replacement at the new destination is a
// silent move nobody asked for.
func routeUnchanged(openedHost string, openedOpts []ssh.ConnectOption, cfg session.Config) error {
	if openedHost == "" || cfg.Host == "" {
		return nil
	}
	if cfg.Host != openedHost {
		return fmt.Errorf(
			"this pane's shell is on %s and the connection now resolves to %s, so the old shell is out of reach; nothing was opened",
			openedHost, cfg.Host)
	}
	if cfg.Remote == nil {
		return nil
	}
	var opened ssh.ConnectConfig
	for _, opt := range openedOpts {
		if opt != nil {
			opt(&opened)
		}
	}
	if opened.User != "" && cfg.Remote.User != "" && opened.User != cfg.Remote.User {
		return fmt.Errorf(
			"this pane's shell is on %s@%s and the connection now resolves to %s@%s, so the old shell is out of reach; nothing was opened",
			opened.User, openedHost, cfg.Remote.User, cfg.Host)
	}
	if (opened.Port != 0 || cfg.Remote.Port != 0) && effectiveSSHPort(opened.Port) != effectiveSSHPort(cfg.Remote.Port) {
		return fmt.Errorf(
			"this pane's shell is on %s:%d and the connection now resolves to %s:%d, so the old shell is out of reach; nothing was opened",
			openedHost, effectiveSSHPort(opened.Port), cfg.Host, effectiveSSHPort(cfg.Remote.Port))
	}
	if opened.JumpHost != cfg.Remote.JumpHost ||
		(opened.JumpHost != "" && (effectiveSSHPort(opened.JumpPort) != effectiveSSHPort(cfg.Remote.JumpPort) ||
			(opened.JumpUser != "" && cfg.Remote.JumpUser != "" && opened.JumpUser != cfg.Remote.JumpUser))) {
		return fmt.Errorf(
			"this pane's shell was opened through jump %q and the connection now resolves through %q, so the old shell is out of reach; nothing was opened",
			opened.JumpHost, cfg.Remote.JumpHost)
	}
	return nil
}
