package app

// Taking a helper-hosted session back after this coordinator replaced the one
// that opened it (nocx-k6p18.30) — the composition root's half.
//
// READ session_reconcile.go FIRST. This file adds a step to the pass that file
// describes; it does not add a second pass. The two are the same reader of the
// same durable binding, and splitting them would put two answers to "does this
// session still exist" in one process — which is the shape AD-8 forbids and
// the shape that would let a re-adoption succeed while a verdict said absent.
//
// WHAT IT ADDS. Reconciliation reaches a verdict by asking an inventory that
// this coordinator already holds. On a COLD start it holds none: helper
// channels are opened by `OpenHosted`, so a process that has opened no tab has
// no helper, and every carried-over session was `unknown/noInventory` for ever.
// The step below is what turns the binding into a helper connection: it dials
// the generation the binding names, asks it once for its sessions, and — when
// the session is there and this coordinator may have it — attaches to the
// EXISTING host session rather than spawning a second shell.
//
// A FAILURE IS STILL NEVER A VERDICT, and that rule is what shapes every
// return here. Every way this can fail returns an ERROR, which reconcile turns
// into `unknown` with the cause it failed for, exactly as a failed
// `LiveSessions` already does. The one path to `absent` is unchanged: an
// inventory that owns the id space was asked, ANSWERED, and does not report
// the session. A re-adoption that fails after the helper has answered "it is
// live" still reports live — the session exists; this coordinator merely could
// not take it — because `absent` deletes a recording and "I could not attach"
// is not evidence that a build stopped.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// sessionReadopter is the step reconcileSessions consults for a carried-over
// session no inventory it already holds can judge.
//
// THREE ANSWERS, AND THEY ARE NOT THREE VERDICTS:
//
//	(inv, nil)  the helper the binding names answered. The caller judges with
//	            it, exactly as it judges with an inventory it already had.
//	(nil, nil)  no route was recorded, so nobody may be asked. The caller's
//	            own `noInventory` stands, which is the behaviour that existed
//	            before this file.
//	(nil, err)  the helper could not be reached, or must not be. The caller
//	            turns the error into `unknown` with its cause.
//
// Whether the session was actually TAKEN BACK is deliberately not one of the
// answers. Re-adoption is a side effect of asking, and a verdict must not
// depend on it: a session the helper reports live is live whether or not this
// coordinator managed to attach to it.
type sessionReadopter interface {
	Readopt(ctx context.Context, p content.PendingSession) (sessionInventory, error)
}

// hostRouteResolver resolves a saved connection into the host and connect
// options that reach it. It is transport.ProfileResolver's shape, declared
// here as the narrow seam this file needs: re-adoption may resolve a stored
// connection and may do nothing else with the profile store.
type hostRouteResolver interface {
	Resolve(profileID string) (host string, cfg *ssh.ConnectConfig, err error)
}

// sessionAdopter installs the transport-owned half of a re-adopted session:
// the replay ring at the offset the recording ends at, the hole observer, the
// output pump and the exit monitor. *transport.WSServer satisfies it; the
// interface exists so this pass is testable without a WebSocket server, and so
// the composition root keeps naming the direction of the dependency.
type sessionAdopter interface {
	ReadoptHostedSession(ctx context.Context, sid session.ID, reattach transport.HostedSessionReattach) error
}

// readoptPass is the collaborator reconcileSessions calls. It holds the
// helper registry (which owns helper channels), the connection resolver, the
// transport half, and the consent store's own resolver factory.
type readoptPass struct {
	registry *helperRegistry
	routes   hostRouteResolver
	adopter  sessionAdopter
	// timeout bounds one attempt; zero means readoptAttemptTimeout. It is a
	// field rather than only a constant so the bound can be DRIVEN — a guard
	// whose failure path no test can reach is a guard nobody has seen work.
	timeout time.Duration
}

var _ sessionReadopter = (*readoptPass)(nil)

// readoptAttemptTimeout bounds ONE session's attempt.
//
// THE PASS IS SYNCHRONOUS AND THE BOUND IS WHY IT CAN BE. It runs before the
// WebSocket server listens, deliberately: a client that asked `sessions.live`
// while the pass was still running would be told the coordinator holds nothing
// and would open a fresh shell beside the one still running — the exact defect
// this bead was filed for, reintroduced as a race. So the pass must finish
// first, and the only thing that makes "finish first" safe is that it cannot
// take forever. A host that is switched off must cost this much and not a
// startup that never completes.
//
// Per ATTEMPT rather than per pass: with one budget for the whole pass, one
// unreachable host would spend it and every session after it would be reported
// timed out without ever having been asked, which is a false statement about
// hosts that were fine.
const readoptAttemptTimeout = 15 * time.Second

// Readopt is one attempt for one carried-over session.
func (rp *readoptPass) Readopt(ctx context.Context, p content.PendingSession) (sessionInventory, error) {
	if rp == nil || rp.registry == nil || rp.routes == nil || rp.adopter == nil {
		return nil, nil
	}
	bound := rp.timeout
	if bound <= 0 {
		bound = readoptAttemptTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	// THE ROUTE IS REQUIRED IN FULL, and a partial one is refused rather than
	// completed by inference. Deriving a host from a session id, or a
	// generation from a host, is exactly what nocx-k6p18.15's ordering exists
	// to forbid: it turns one helper's truthful "I do not hold that" about
	// somebody else's id into a deletion of live work.
	if p.SessionID == "" || p.Generation == "" || p.Host == "" ||
		p.ProfileID == "" || p.HelperCommand == "" || p.PaneID == "" {
		return nil, nil
	}

	host, cfg, err := rp.routes.Resolve(p.ProfileID)
	if err != nil {
		// A sealed vault arrives here, and it is the one cause on the
		// unreconciled list a person clears in one gesture. causeFor already
		// classifies it; this only has to not swallow it.
		return nil, fmt.Errorf("resolve the connection this session was opened on: %w", err)
	}
	if cfg == nil {
		return nil, errors.New("the connection this session was opened on resolved to nothing")
	}
	opts := session.SSHOptionsFromConfig(cfg)
	// THE ROUTE MUST STILL LEAD WHERE IT LED. A saved connection is editable:
	// the host or the user behind one profile id can be changed between two
	// runs, and asking THAT machine about this session's id would be asking a
	// stranger about somebody else's id space. Refused as unreachable, which
	// is what it is — the machine this session is on was not asked at all.
	if account := accountFromOptions(opts); host != p.Host || account != p.Account {
		return nil, fmt.Errorf(
			"the saved connection now resolves to %s@%s and this session is on %s@%s, so its host was not asked",
			account, host, p.Account, p.Host)
	}
	// CONSENT IS RE-ASKED, never assumed to have survived. Opening a helper
	// channel to a machine is the act consent governs (D8), and a person who
	// withdrew it between two runs must not have one opened silently.
	if rp.registry.consent != nil && p.Fingerprint != "" {
		resolver := newResolver(
			withStore(rp.registry.consent),
			// The artifact is not consulted and nothing is installed: this
			// path connects to a helper the machine is ALREADY running. What
			// is being asked is the machine's own decision, so the two inputs
			// that decide it are supplied as satisfied and the answer comes
			// from the store.
			withHelperArtifactAvailable(true),
			withHelperRequested(true),
		)
		if resolver.Resolve(Machine{
			Fingerprint: p.Fingerprint,
			Mode:        profile.DesiredMode(cfg.DesiredMode),
		}) != DesiredRelay {
			return nil, fmt.Errorf(
				"this machine no longer consents to the nocx helper, so %s was not asked about its sessions", p.Host)
		}
	}

	f := &sessionFactory{
		reg: rp.registry, sid: session.ID(p.SessionID), host: p.Host,
		account: p.Account, opts: opts,
		command: p.HelperCommand, expectHash: p.Generation,
	}
	h := &hostHelper{f: f, lanes: rp.registry.lanes, log: rp.registry.log}
	h.mu.Lock()
	c, outcome, connectErr := h.connectLocked(ctx)
	h.mu.Unlock()
	if connectErr != nil {
		return nil, fmt.Errorf("connect the helper holding this session: %w", connectErr)
	}
	if outcome.State != "" {
		// A §6 refusal — a version or content mismatch, an exec the host
		// refused, no helper serving that generation any more. Each is a
		// reason nobody could be ASKED, so each is `unknown`. In particular
		// "the generation that held this session is gone" must never read as
		// "the session is gone": nothing answered.
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
		return nil, errors.New(outcome.Message)
	}

	entries, err := c.Sessions(ctx)
	if err != nil {
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
		return nil, fmt.Errorf("ask the helper holding this session what it holds: %w", err)
	}

	// THE ANSWER IS CAPTURED, not re-asked. The inventory handed back answers
	// from the entries this one call returned, so the fact a verdict is
	// reached on is the fact the re-adoption below acted on. Asking twice
	// would let the two disagree, and the disagreement that matters is the one
	// where the second ask fails and a session already taken back is judged
	// unknown.
	inv := &readoptedInventory{
		generation: p.Generation, host: p.Host, account: p.Account,
		live: make(map[string]struct{}, len(entries)),
	}
	var mine *client.SessionEntry
	for i := range entries {
		if entries[i].HostSessionID.Generation != p.Generation {
			continue
		}
		inv.live[entries[i].HostSessionID.Session] = struct{}{}
		if entries[i].HostSessionID.Session == p.SessionID {
			mine = &entries[i]
		}
	}
	if mine == nil {
		// The helper answered and does not hold it. That is the ONE path to
		// absent and it belongs to the caller; this side only has to stop
		// holding a channel nobody needs.
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
		return inv, nil
	}

	if err := rp.readopt(ctx, p, cfg, h, c, *mine); err != nil {
		// The session is LIVE and this coordinator could not take it. Said out
		// loud, because a pane that quietly opened a second shell to the same
		// host is the failure this bead exists to end, and the only trace of
		// it would otherwise be the absence of a row.
		rp.registry.log.Warn("a session that is still running on its host could not be taken back; its pane will open a new shell instead",
			"session_id", p.SessionID, "host", p.Host, "error", err)
		h.mu.Lock()
		h.closeLocked()
		h.mu.Unlock()
	}
	return inv, nil
}

// readopt is the attach-and-adopt half, and it is deliberately the same half
// OpenHosted runs: attach, adopt into the registry, register the helper
// channel, hand the transport its ring and its pump. What differs is exactly
// two things and no more — the attachment is not Fresh, and it resumes at the
// offset this machine's recording ends at rather than at the window's base.
func (rp *readoptPass) readopt(
	ctx context.Context,
	p content.PendingSession,
	cfg *ssh.ConnectConfig,
	h *hostHelper,
	c *client.Client,
	entry client.SessionEntry,
) error {
	sid := session.ID(p.SessionID)
	// adopted names the interval this function has to be able to undo: it opens
	// when Adopt puts the session in the registry and closes either when the
	// transport half returns nil — the session is now the transport's to tear
	// down — or here, on the error path. Without it a transport that refused
	// AFTER the adopt would leave a session in `sessions.live` with no ring, no
	// pump and nothing reading it: a row a restored pane would claim and then
	// find silent.
	adopted := false
	err := rp.adopter.ReadoptHostedSession(ctx, sid, func(ctx context.Context, from uint64) (transport.HostedSessionOpen, error) {
		var subscriberRaw [16]byte
		if _, randErr := rand.Read(subscriberRaw[:]); randErr != nil {
			return transport.HostedSessionOpen{}, randErr
		}
		// THE LIFECYCLE LEG IS RE-ESTABLISHED BEFORE THE ATTACH, because
		// the attach has to carry the offset the adoption implies and
		// because a refusal here must cost nothing: no attachment, no
		// adapter, no half-adopted domain. What comes back is either the
		// launch to adopt, "this session is conventional", or a reason the
		// product will state (nocx-k6p18.31).
		adoption := rp.adoptLifecycle(ctx, c, entry)
		attached, err := c.Attach(ctx, proto.AttachParams{
			Subscriber: proto.SubscriberID(hex.EncodeToString(subscriberRaw[:])),
			Session: proto.HostSessionID{
				Generation: proto.GenerationID(entry.HostSessionID.Generation),
				Session:    entry.HostSessionID.Session,
			},
			// FRESH IS FALSE AND THE OFFSET IS OURS. `Fresh` says the caller
			// has no render state; this caller has a RECORDING, and the whole
			// point of resuming at its end is that the two stretches of one
			// stream share a coordinate. The helper answers `resumed` when
			// that offset is still inside its window and `reset` — with the
			// range it lost — when the host out-produced the window while
			// nobody was listening, which is the case this epic is about.
			Offset: proto.StreamOffset(from), Fresh: false,
			// THE LIFECYCLE STREAM RESUMES AT THE HELPER'S HEAD, NOT AT ITS
			// BASE, and that is a security property rather than an
			// optimisation. The adopted domain keeps the capability the shell
			// has been stamping every frame with, so the helper's retained
			// window is a stretch of already-authenticated events: replaying
			// it into the new kernel would re-deliver commands that already
			// ran. `Fresh` is true because this coordinator holds no lifecycle
			// state at all — the offset is where the stream stands now, and
			// what came before it belongs to the kernel that is gone.
			LifecycleOffset: proto.StreamOffset(entry.LifecycleWindow.Written),
			LifecycleFresh:  true,
			RequestWrite:    true,
		})
		if err != nil {
			adoption.abort()
			return transport.HostedSessionOpen{}, fmt.Errorf("attach to the session still running on %s: %w", p.Host, err)
		}
		// THE HOST'S OWN VERDICT ON A SHELL THAT ENDED WHILE WE WERE AWAY.
		// The helper's exit notification fired once, at the moment the process
		// died, to whichever coordinator was bound then — and that was the one
		// that is gone. Without carrying it here the attachment below would
		// read the rest of the window, reach EOF, and the product would say
		// "was interrupted" about a build whose real status the helper has
		// been holding all along (nocx-k6p18.23). Carried BEFORE the adopt so
		// the session can never be observed without it.
		if entry.Exit != nil {
			attached.AdoptExitStatus(*entry.Exit)
		}
		if !attached.WriteGranted() {
			// Another coordinator is holding this session's one write
			// capability (D12 serves a second coordinator rather than refusing
			// it; nocx-k6p18.16 binds the lease to the connection that holds
			// it). Adopting it here would put a pane on screen whose
			// keystrokes go nowhere. Declined, and the session stays LIVE —
			// the other coordinator owns it, and nothing here may delete its
			// recording.
			_ = attached.Close()
			adoption.abort()
			return transport.HostedSessionOpen{}, fmt.Errorf(
				"another nocx already holds the keyboard of this session on %s", p.Host)
		}
		sess, err := rp.registry.registry.Adopt(session.Config{
			Kind: session.KindRemote, Host: p.Host,
			// The cwd is the HELPER's, read off the launch record it has kept
			// since the shell started. The alternative is the pane's stored
			// cwd, which is where the pane was opened and not where the shell
			// is now, and a tab named after a directory the process left is a
			// statement that used to be true.
			Cwd:       entry.Launch.Cwd,
			PaneID:    p.PaneID,
			ProfileID: p.ProfileID,
			// No size: nothing here measured a viewport. The registry's own
			// default stands until the client that claims this session
			// resizes it, which it does on attach.
			Remote:       cfg,
			CredentialID: cfg.CredentialID,
		}, sid, attached)
		if err != nil {
			_ = attached.Close()
			adoption.abort()
			return transport.HostedSessionOpen{}, fmt.Errorf("adopt the re-attached session: %w", err)
		}
		// The registry entry is what makes `sessions.inventory` answer for
		// this generation on a cold start, and it is written only now — after
		// the attach and the adopt, so nothing claims a helper for a session
		// this coordinator does not hold.
		rp.registry.mu.Lock()
		rp.registry.hosts[sid] = h
		rp.registry.mu.Unlock()
		adopted = true
		open := transport.HostedSessionOpen{
			Session: sess, Host: p.Host, Account: p.Account,
			Generation: p.Generation, HelperCommand: p.HelperCommand,
			Fingerprint:        p.Fingerprint,
			ObserveOutputHoles: attached.OnOutputHole,
			// The integration axis is the product's own sentence about this
			// pane, and it is filled in on BOTH arms: a lifecycle channel
			// that was re-established says starting (the kernel's published
			// fact turns it into integrated), and one that could not be says
			// conventional with the reason. A re-adopted pane that said
			// nothing at all is what this bead was filed for — absence on
			// this axis means "conventional by design", and a shell that was
			// integrated five minutes ago is not that.
			IntegrationShell:  entry.Launch.Shell,
			IntegrationStatus: adoption.status,
			IntegrationReason: adoption.reason,
		}
		adoption.attachTo(&open, attached)
		return open, nil
	})
	if err != nil && adopted {
		// The transport refused a session the registry already holds. Closing
		// it here rather than leaving it is the whole of the interval above:
		// this returns an error, the caller closes the helper channel, and
		// nothing anywhere is left claiming to hold this session.
		_ = rp.registry.registry.Close(sid)
	}
	return err
}

// readoptedInventory answers for one generation on one host from the entries a
// single `sessions` call returned.
//
// It answers from a captured set rather than by calling again, and that is the
// point rather than an optimisation: the verdict and the re-adoption are then
// two consequences of ONE answer. A second call could disagree with the first,
// and the disagreement that costs something is the one where it fails and a
// session already taken back is judged on an error.
type readoptedInventory struct {
	generation string
	host       string
	account    string
	live       map[string]struct{}
}

func (i *readoptedInventory) Generation() string { return i.generation }
func (i *readoptedInventory) Host() string       { return i.host }
func (i *readoptedInventory) Account() string    { return i.account }

// Owns is the same rule helperSessionInventory applies: a generation is an id
// space, and an inventory with no generation owns none. The host and account
// are matched by the caller before this is asked.
func (i *readoptedInventory) Owns(_ string) bool { return i.generation != "" }

func (i *readoptedInventory) LiveSessions(_ context.Context) (map[string]struct{}, error) {
	return i.live, nil
}

var _ sessionInventory = (*readoptedInventory)(nil)
