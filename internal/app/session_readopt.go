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

// localHelperRoute reaches THIS machine's own daemon (nocx-ie23r.2, extended by
// nocx-ie23r.5). It is a seam of its own rather than a second method on
// hostRouteResolver because the two have nothing in common but the generation:
// a remote route is a saved connection and an ssh dial, a local one is a socket
// in the person's own home, and folding one into the other would put a profile
// lookup on a path that has no profile. helper_local.go's localHelperOpener is
// the only implementation.
//
// IT IS ALSO THE CARRIER, and that composition is the point rather than
// convenience: the connection this route asks about a generation is not the
// connection an attachment rides (the ask may not start a daemon; an attachment
// is an open-shaped act on this machine's daemon), and the object that owns
// both is the one that owns this machine's route. See helper_local.go for the
// two dial rules.
type localHelperRoute interface {
	LocalSessions(ctx context.Context, generation string) ([]client.SessionEntry, error)
	hostedCarrier
	// Release gives up the connection this route opened for ONE session that
	// did not come back. It exists because the connection is opened before the
	// attach can succeed (the lifecycle question comes first, on the same
	// connection) and cannot be opened twice without breaking that pairing —
	// so the outcome the route cannot see is told to it. A refused attach is
	// not exotic: it is what a second coordinator meets on every session
	// another one holds, and a socket per refusal kept until the process ends
	// is a cost nobody would choose.
	Release(sid string)
}

// hostedCarrier is the connection a re-attachment is made over: it takes the
// attachment a re-adopted pane's output and keystrokes ride, and it answers the
// one other question the pass asks about a session it is taking back — the
// lifecycle identity its shell is still speaking with (nocx-k6p18.31).
//
// ONE INTERFACE RATHER THAN TWO CALLBACKS, because on each route both are
// properties of ONE connection and the choice of connection is the whole of
// what differs: remotely the helper channel's client, which the registry owns,
// and locally the coordinator's own connection to this machine's daemon, which
// the opener owns. *client.Client satisfies it; so does localHelperOpener, by
// forwarding both to the connection it holds.
type hostedCarrier interface {
	Attach(ctx context.Context, params proto.AttachParams) (*client.AttachedSession, error)
	AdoptLifecycle(ctx context.Context, id client.HostSessionID) (*proto.LifecycleLaunch, error)
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
	// local is this machine's own route (nocx-ie23r.2, extended by
	// nocx-ie23r.5). It is a separate collaborator because it needs neither
	// of the two above: a local binding names no saved connection, so there
	// is nothing to resolve, and its daemon is reached over a socket in the
	// person's own home rather than over an ssh exec lane. Nil is a
	// legitimate wiring — a composition root that has no endpoint directory —
	// and makes a local binding answer exactly as it did before this existed:
	// `noInventory`.
	local localHelperRoute
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
	if rp == nil {
		return nil, nil
	}
	// THIS MACHINE'S OWN SESSIONS ARE ASKED OVER A SOCKET (nocx-ie23r.2), and
	// the branch comes FIRST because everything below it would refuse this
	// binding for the wrong reason. The route requirements further down —
	// Host, ProfileID, HelperCommand — are what an ssh exec lane needs, and a
	// local session has none of them by design: its daemon is reached by
	// dialling a socket named after its generation, and its route back is a
	// generation plus the pane it was the pipe of (helper_local.go's
	// OpenHosted says so at the field it leaves empty). Falling through would
	// keep answering `noInventory` for every local session, which is the
	// false "may still be running" this bead was filed to end.
	if isLocalBinding(p) {
		return rp.readoptLocal(ctx, p)
	}
	if rp.registry == nil || rp.routes == nil || rp.adopter == nil {
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
		}) != DesiredHelper {
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

	if err := rp.readopt(ctx, p, session.Config{
		Kind: session.KindRemote, Host: p.Host,
		// The cwd is the HELPER's, read off the launch record it has kept
		// since the shell started. The alternative is the pane's stored
		// cwd, which is where the pane was opened and not where the shell
		// is now, and a tab named after a directory the process left is a
		// statement that used to be true.
		Cwd:       mine.Launch.Cwd,
		PaneID:    p.PaneID,
		ProfileID: p.ProfileID,
		// No size: nothing here measured a viewport. The registry's own
		// default stands until the client that claims this session
		// resizes it, which it does on attach.
		Remote:       cfg,
		CredentialID: cfg.CredentialID,
	}, h, c, *mine); err != nil {
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

// readoptLocal is the local half of the same step: ask this machine's daemon
// the ONE question, and — when it still holds the session — take it back.
//
// IT IS THE REMOTE HALF, ONE CARRIER OVER, and that is the whole of what this
// function adds. The ask, the capture of the answer, the judge's rule and the
// attach-and-adopt step are each a single behaviour that already exists; what
// differs is that the route to the daemon is a socket in this person's own
// home, the session's config names no host and no connection, and the
// connection the attachment rides is the coordinator's own rather than one
// helper channel per session (AGENTS.md, "look for the existing answer" — the
// alternative was a second re-attach path that would have agreed with this one
// until the first of them moved).
//
// EVERY FAILURE IS AN ERROR, so every failure is `unknown` with a cause. There
// is no branch of this function that can produce `absent`: that verdict needs
// an ACCOUNT that was asked and did not report the session, and a machine
// whose helper did not answer has told nobody anything. A daemon that ANSWERS
// and does not hold the session is the one path to `absent`, and it belongs to
// the caller — this side only has to stop holding a connection nobody needs.
//
// A REFUSED ATTACH IS NOT `absent` EITHER, and it is not a lost session: the
// daemon has just said the shell is there. The verdict is `live` — the session
// exists; this coordinator merely could not take it — and the warn line says
// why the pane will show up as a new shell instead.
func (rp *readoptPass) readoptLocal(ctx context.Context, p content.PendingSession) (sessionInventory, error) {
	if rp.local == nil {
		// No local route is wired. The caller's own `noInventory` stands, and
		// the session is not deleted on the strength of a missing collaborator.
		return nil, nil
	}
	bound := rp.timeout
	if bound <= 0 {
		bound = readoptAttemptTimeout
	}
	// The bound is the same one the remote attempt carries and for the same
	// reason: a daemon that accepts a connection and then stops talking must
	// cost a start this much and not a start that never finishes. The pass is
	// SYNCHRONOUS before the server listens, so an unbounded ask here is a
	// backend that never serves.
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	asked, err := rp.local.LocalSessions(ctx, p.Generation)
	if err != nil {
		return nil, fmt.Errorf("ask this machine's helper what it holds: %w", err)
	}
	if asked == nil {
		// No route was recorded for this binding. Nobody may be asked, which
		// is the answer that stood before the local route existed. A daemon
		// that ANSWERED with an empty list is a different thing and is judged
		// `absent` below, which is why the two are not the same value.
		return nil, nil
	}
	// THE ANSWER IS CAPTURED, not re-asked — the same rule and the same type
	// the remote half uses, because the verdict and the re-adoption must be
	// two consequences of ONE answer. Host and Account are empty here and that
	// is exact rather than an omission: a local binding has neither, and an
	// inventory that filled them in with this machine's name would be claiming
	// a target the binding never named.
	inv := &readoptedInventory{
		generation: p.Generation,
		live:       make(map[string]struct{}, len(asked)),
	}
	var mine *client.SessionEntry
	for i := range asked {
		if asked[i].HostSessionID.Generation != p.Generation {
			continue
		}
		inv.live[asked[i].HostSessionID.Session] = struct{}{}
		if asked[i].HostSessionID.Session == p.SessionID {
			mine = &asked[i]
		}
	}
	if mine == nil || rp.adopter == nil || rp.registry == nil || rp.registry.registry == nil {
		// Either the daemon answered and does not hold it — the one path to
		// `absent`, which is the caller's to apply — or this coordinator has
		// no transport to adopt into. Both answer with the inventory and
		// nothing else: the ask released its own connection, so there is
		// nothing here that could be left holding a socket.
		return inv, nil
	}
	if err := rp.readopt(ctx, p, session.Config{
		Kind: session.KindLocal,
		// The cwd is the HELPER's, exactly as on the remote route: the launch
		// record the daemon has kept since the shell started, not the pane's
		// stored cwd, which is where the pane was opened.
		Cwd:    mine.Launch.Cwd,
		PaneID: p.PaneID,
		// No Host, no ProfileID, no Remote: a local binding names none of
		// them (helper_local.go's OpenHosted leaves them empty), and the
		// registry's Kind is what routes the pane's screen read back to this
		// machine's daemon.
	}, nil, rp.local, *mine); err != nil {
		// The session is LIVE and this coordinator could not take it. Said out
		// loud, because the pane will otherwise open a second shell beside the
		// one still running, and the only trace of it would be the absence of
		// a row.
		rp.registry.log.Warn("a local session that is still running could not be taken back; its pane will open a new shell instead",
			"session_id", p.SessionID, "error", err)
		rp.local.Release(p.SessionID)
	}
	return inv, nil
}

// isLocalBinding answers whether a carried-over binding names THIS machine's
// daemon rather than a host reached over ssh — the discriminator the local
// route is chosen by (nocx-ie23r.2).
//
// IT IS THE BINDING'S SHAPE, and the shape is the statement. A local
// HostedSessionOpen carries a generation and NOTHING ELSE (helper_local.go,
// at the fields it deliberately leaves empty): there is no host to resolve and
// no helper command to exec, because the route is a socket whose name is
// derived from the generation. A remote one carries a host, always — it is
// what the ssh lane connects to — and the two readopt routes are therefore
// told apart by the field that exists rather than by a field that is merely
// empty.
//
// The three empties are checked together because each of them is enough to
// send the binding down a route that cannot work: a host with no profile
// would be refused for a partial route, a profile with no host would resolve
// a connection to nowhere, and a helper command with no host would exec a
// binary on this machine over an ssh lane that does not exist. Requiring all
// three keeps the predicate one question — "does this name any half of an ssh
// route?" — instead of four.
func isLocalBinding(p content.PendingSession) bool {
	return p.Generation != "" && p.Host == "" && p.ProfileID == "" && p.HelperCommand == ""
}

// readopt is the attach-and-adopt half, and it is deliberately the same half
// OpenHosted runs: attach, adopt into the registry, register the helper
// channel, hand the transport its ring and its pump. What differs is exactly
// two things and no more — the attachment is not Fresh, and it resumes at the
// offset this machine's recording ends at rather than at the window's base.
//
// IT IS ONE FUNCTION FOR TWO CARRIERS (nocx-ie23r.5). `cfg` is the config the
// registry adopts under, so the remote route hands in a KindRemote config with
// the ssh connection and the saved profile, and the local route hands in a
// KindLocal one with neither; `h` is the remote route's helper channel, which
// the registry indexes by session so an inventory can answer for it on a cold
// start, and is nil locally because a local daemon's channel IS this
// attachment (there is no second thing to reach through it). Everything else —
// the lifecycle leg, the offset, the write lease, the rollback on every
// failure — is the same act and must not have two copies.
//
// `carrier` IS THE ONE DIFFERENCE THAT IS A CONNECTION RATHER THAN A DECISION,
// and it is a parameter because a CONNECTION has an owner on each route and
// this function is not it. Remotely it is the helper channel's client, owned
// by the registry entry `h` above; locally it is the coordinator's own
// connection to this machine's daemon, owned by the opener that dialled it.
// The alternative — passing a client and closing it here — would give this
// function a lifetime it cannot see the end of, because the attachment lives
// ON that client: a caller that closed it after the attach returned would
// detach the session it had just recovered.
func (rp *readoptPass) readopt(
	ctx context.Context,
	p content.PendingSession,
	cfg session.Config,
	h *hostHelper,
	carrier hostedCarrier,
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
		adoption := rp.adoptLifecycle(ctx, carrier, entry)
		attached, err := carrier.Attach(ctx, proto.AttachParams{
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
			return transport.HostedSessionOpen{}, fmt.Errorf("attach to the session still running on %s: %w", reattachTarget(p), err)
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
				"another nocx already holds the keyboard of this session on %s", reattachTarget(p))
		}
		sess, err := rp.registry.registry.Adopt(ctx, cfg, sid, attached)
		if err != nil {
			_ = attached.Close()
			adoption.abort()
			return transport.HostedSessionOpen{}, fmt.Errorf("adopt the re-attached session: %w", err)
		}
		// The registry entry is what makes `sessions.inventory` answer for
		// this generation on a cold start, and it is written only now — after
		// the attach and the adopt, so nothing claims a helper for a session
		// this coordinator does not hold. A LOCAL re-attachment has no such
		// entry to write and says so by passing no helper: its channel is this
		// attachment, and there is nothing else it could be reached through.
		if h != nil {
			rp.registry.mu.Lock()
			rp.registry.hosts[sid] = h
			rp.registry.mu.Unlock()
		} else {
			rp.rearmLocal(sess, entry)
		}
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

// reattachTarget names where a session is being taken back from, for the two
// errors in readopt a person reads. A remote binding carries a host; a local
// one carries none by construction (isLocalBinding), and "the session still
// running on " with nothing after it is not a sentence.
func reattachTarget(p content.PendingSession) string {
	if p.Host != "" {
		return p.Host
	}
	return "this machine"
}

// rearmLocal completes a LOCAL re-attachment with the one fact an open records
// that an attach cannot.
//
// It is reached only for a local session (readopt's `h == nil`), and the split
// is not cosmetic: the remote route's helper channel is registered in the
// registry because an inventory answers through it on a cold start, while a
// local session's connection belongs to the opener that opened it — the same
// owner the pane's screen read asks, so a registry entry here would be a second
// answer to a question that already has one.
func (rp *readoptPass) rearmLocal(sess session.Session, entry client.SessionEntry) {
	// THE PROCESS THE DAEMON STARTED is recorded exactly as an open records
	// it (helper_local.go's OpenHosted), because two decisions read that fact:
	// worker admission's root-pid check and agent approval's "this pane is
	// ours". A re-attached pane that left it unknown would refuse both, which
	// is the feature silently going away across a restart.
	if entry.Launch.Pid > 0 {
		if err := rp.registry.registry.RecordOwnedProcessPID(sess.ID(), entry.Launch.Pid); err != nil {
			rp.registry.log.Warn("a re-attached local pane's launch pid was not recorded",
				"session_id", string(sess.ID()), "error", err)
		}
	}
}

// readoptedInventory answers for one generation from the entries a single
// `sessions` call returned — over an ssh lane for a remote session, over this
// machine's own socket for a local one.
//
// It answers from a captured set rather than by calling again, and that is the
// point rather than an optimisation: the verdict and the re-adoption are then
// two consequences of ONE answer. A second call could disagree with the first,
// and the disagreement that costs something is the one where it fails and a
// session already taken back is judged on an error.
//
// Host and Account are EMPTY for a local answer, and that is exact rather than
// an omission: a local binding names no host, and an inventory that filled one
// in with this machine's name would claim a target reconciliation could then
// match against — which is the shape that turns a truthful "I do not hold
// that" about somebody else's id into a deletion of live work.
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
