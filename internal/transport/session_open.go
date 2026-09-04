package transport

// One session-open path, two callers (nocx-dkawo.6).
//
// Opening a session used to exist only inside `open`'s JSON-RPC handler, and
// it was written for the only caller it had: a renderer that already created
// the pane row, that is waiting on a response frame, and that will be
// attached to the session's ring the moment it exists. None of those hold for
// the second caller. A wave participant is spawned by the BACKEND — there is
// no request id to answer, no connection to attach, and the pane is one the
// backend is creating rather than one it was handed.
//
// The wrong fix is a second open beside the first. The two would agree on the
// day they were written and disagree the first time either moved: the parent
// claim's admission, the workspace chain, the Enhanced default, the rule that
// a profile id is recorded only after the resolver accepted it, the helper's
// binding written before the ack, the compensation when that write fails.
// Every one of those is a decision with an argument behind it, and a copy
// carries the code without the argument.
//
// So the handler is split where its two jobs already meet. sessionOpener does
// what makes a SESSION — resolve the workspace, build the config, resolve ssh,
// dial, persist the helper binding — and knows nothing about a connection. The
// handler does what makes an ANSWER — the ring, the subscriber, the ack, the
// pumps — and is the only half that names a wsConn.
//
// The seam is not invented for this: `attach` already does the second half
// alone, for a session that exists with no client on it. A session outliving
// every WebSocket is AD-9, and a session that never had one is the same fact
// one step further.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/workspace"
)

// OpenSpec is what to open, with no caller in it.
//
// It is deliberately not openParams. openParams is a WIRE shape — it carries
// what a renderer may say, validated by validateOpenRaw before anything here
// runs — and a backend caller that had to fill one in would be pretending to
// be a renderer. What survives the translation is only what makes a session.
type OpenSpec struct {
	// PaneID is the pane this session becomes the pipe of. Empty means the
	// default workspace, which is the ordinary case until every caller mints
	// panes.
	PaneID string
	// Kind is "", "local" or "ssh". The wire's closed set, unchanged: an
	// unrecognised kind is refused by validateOpenRaw before this, and a
	// backend caller that invents one gets a local session, which is what
	// the zero value has always meant.
	Kind string
	// Cols, Rows, XPixel and YPixel are the caller's REPORT of geometry. The
	// registry decides the size the channel opens at; nothing here may treat
	// these as the answer.
	Cols, Rows, XPixel, YPixel uint16
	// ProfileID or Host names an ssh destination. Exactly one, and the wire
	// validator has already refused neither.
	ProfileID string
	Host      string
	User      string
	// Shell pins the far shell the launcher targets. Anything unrecognised
	// is ignored with a warn, never honoured.
	Shell string
	// Parent is the claimed edge, carried as a CLAIM: the registry is the
	// single owner of whether it may be recorded, and it refuses before
	// anything is spawned.
	Parent *session.Ref
}

// OpenedSession is a session that exists, together with the facts a caller
// needs to finish its own half.
//
// Config is returned rather than re-derived because it holds what the open
// CONCLUDED — the resolved host, the profile id the resolver accepted, the
// remote config the discovery and forward replays key on — and a caller that
// rebuilt it from the spec would be rebuilding the renderer's claim instead of
// the backend's conclusion.
type OpenedSession struct {
	Session     session.Session
	Config      session.Config
	Hosted      *HostedSessionOpen
	WorkspaceID string
}

// openRefusal is an answer the open path produced ITSELF, as distinct from a
// failure a phase returned. The difference matters on the wire: a refusal
// carries a sentence a person can act on — "SSH sessions not available (no
// profile resolver wired)" — and routing it through the ssh error taxonomy
// would turn it into "Internal error", which is the shape a person cannot act
// on at all.
type openRefusal struct {
	code    int
	message string
	// cause, when set, is what the caller maps through rpcErrorFor instead of
	// message. It exists for the sealed vault: the renderer's unlock prompt
	// is raised off the REASON carried in the error data, so flattening it to
	// a sentence would replace an unlock dialog with an error.
	cause error
}

func (e *openRefusal) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.message
}

func (e *openRefusal) Unwrap() error { return e.cause }

// refuse builds a refusal carrying a sentence.
func refuse(code int, message string) error {
	return &openRefusal{code: code, message: message}
}

// refuseWithCause builds a refusal whose wire shape comes from the error.
func refuseWithCause(code int, cause error) error {
	return &openRefusal{code: code, cause: cause}
}

// sessionOpener owns "a session comes into existence". Its fields are the
// seams the two phases need and nothing else: there is no Responder here, no
// connection and no request id, which is what makes the second caller
// possible rather than merely intended.
type sessionOpener struct {
	op       capability.OpenOperation
	resolver *resolverHolder
	sshCfg   ssh.ConfigResolver
	launcher ssh.RemoteLauncher
	// installer publishes the bundle over SFTP on the direct-host path,
	// which is the only thing that installs it now that the command carries
	// no payload.
	installer ssh.RemoteInstaller
	// lifecycle is the authenticated-channel seam (ADR-0024): the dial hands
	// it to the far side so the shell can hand its lifecycle back over a
	// channel that is not the terminal.
	lifecycle ssh.RemoteLifecycle
	// panes resolves pane → tab → workspace. nil when the content store is
	// not wired, which is a visible unavailable state and not a degrade.
	panes paneWorkspaces
	// ledger is the durable writer for the helper binding — the existing
	// content ledger seam, never a second session store.
	ledger content.LedgerRepository
	helper HelperSessionOpener
	// laneRegistrar records the lifecycle lane a helper-hosted open returned.
	// It is a seam and not the whole machine because what this needs is one
	// statement: this lane belongs to this session.
	laneRegistrar lifecycleLaneRegistrar
	log           log.Logger
}

// lifecycleLaneRegistrar is the narrow view of "remember which session this
// lane speaks for" (AD-8).
type lifecycleLaneRegistrar interface {
	RegisterLifecycleLane(lifecycle.LaneID, session.ID)
}

// workspaceForOpen derives the workspace this session belongs to.
//
// THE CHAIN IS THE ANSWER, never a value the caller sent: a caller names a
// PANE — the durable identity it already owns — and the backend walks pane →
// tab → workspace itself. A paneId naming no pane is refused rather than
// defaulted, because "the pane you named does not exist" and "you named no
// pane" are different facts and answering both with the default would hide the
// first.
//
// No paneId is the second fact, and it is the ordinary one until every caller
// mints panes (nocx-isoph.4): the session is in the default workspace,
// resolved through internal/workspace.Default, which is the single owner of
// that decision (AD-7).
func (o *sessionOpener) workspaceForOpen(ctx context.Context, paneID string) (string, error) {
	if paneID == "" || o.panes == nil {
		return string(workspace.Default), nil
	}
	return o.panes.WorkspaceForPane(ctx, paneID)
}

// Open runs both phases and returns a live session.
//
// THE ORDER IS THE ROLLBACK, and each step names what is true if the next one
// fails. The workspace is resolved BEFORE anything is spawned or dialed,
// because a request that cannot be satisfied must not cost the caller a shell
// or an ssh handshake, and a refused open must leave nothing behind. The
// resolve runs under the [config, session] gates and touches stores only; both
// gates are released before anything is dialed, because a gate held across a
// network wait — and sometimes across a person, at a password prompt — is what
// refused every other pane's open with "the terminal is busy" while one tab
// was still connecting. The helper's binding is written before the session is
// returned, so reconciliation can never observe a live helper session without
// the generation that qualifies its id space; a failed write closes the
// session rather than exposing one no inventory may safely judge.
func (o *sessionOpener) Open(ctx context.Context, spec OpenSpec) (OpenedSession, error) {
	workspaceID, wsErr := o.workspaceForOpen(ctx, spec.PaneID)
	if wsErr != nil {
		return OpenedSession{}, refuse(-32602, "Invalid params: "+wsErr.Error())
	}

	cfg := session.Config{
		Kind: session.KindLocal,
		// The caller's REPORT of its own geometry, carried through as a
		// measurement (nocx-eidfb.1). The registry decides the size the
		// channel is created at and the ack reports what it decided, so
		// nothing here may treat these four as the answer.
		Cols:   spec.Cols,
		Rows:   spec.Rows,
		XPixel: spec.XPixel,
		YPixel: spec.YPixel,
		// Every session asks to be integrated, and the ones that cannot be
		// fall back to an ordinary terminal (nocx-tr2n). This is not a policy
		// a caller may express: it arrived as an `enhanced` open parameter,
		// both ssh openers omitted it, and the result was a second — silent,
		// always-negative — answer to the question `desiredMode` already
		// answers per connection (AD-8).
		Enhanced: true,
		// The pane this session is the pipe of, recorded so the ledger can
		// anchor every block it records without the caller restating it per
		// event (nocx-rtg0.28).
		PaneID: spec.PaneID,
	}
	// The claimed parent edge (nocx-9hu9d). Carried into the registry as a
	// claim; the registry is the single owner of whether it may be recorded,
	// and it refuses before anything is spawned. Absent means a root session.
	if spec.Parent != nil {
		cfg.Parent = *spec.Parent
	}
	// ProfileID is deliberately NOT set here. It is recorded below, only once
	// the resolver has accepted it, because a local PTY has no profile and
	// setting it up front lets a caller attach any profile id to a local
	// session it opens. sessions.status would then report that profile live
	// and the connection list would draw a row as connected with nothing
	// behind it (nocx-uxs5.4).

	// L7 — THE PANE'S CLAIM IS DURABLE BEFORE THE SPAWN. Written here, before
	// either phase, because the point of it is to precede the first
	// irreversible effect and a resolve can already cost a password prompt.
	claim, claimErr := o.claimSpawn(ctx, cfg, workspaceID)
	if claimErr != nil {
		return OpenedSession{}, claimErr
	}

	// PHASE ONE — resolve, under [config, session]. Store and vault reads
	// only.
	if err := o.op.Prepare(ctx, func(ctx context.Context, svc capability.OpenService) error {
		if spec.Kind != "ssh" {
			return nil
		}
		return o.resolveRemote(ctx, svc, spec, &cfg)
	}); err != nil {
		return OpenedSession{}, err
	}

	// PHASE TWO — dial, on the execution lane and no domain gate.
	var (
		sess   session.Session
		hosted *HostedSessionOpen
	)
	if err := o.op.Dial(ctx, func(ctx context.Context, svc capability.OpenService) error {
		if o.helper != nil {
			openedHosted, selected, oerr := o.helper.OpenHosted(ctx, cfg, claim)
			if selected {
				if oerr != nil {
					return oerr
				}
				if openedHosted.Session == nil {
					return errors.New("helper session opener returned no session")
				}
				sess = openedHosted.Session
				hosted = &openedHosted
				return nil
			}
			if oerr != nil {
				return oerr
			}
		}
		var oerr error
		sess, oerr = svc.Open(ctx, cfg)
		return oerr
	}); err != nil {
		// The spawn did not happen, or did not survive its own failure arm.
		// The claim goes with it: a key left standing would name a session
		// nothing produced, and the next open of this pane would replay it.
		o.releaseClaim(ctx, claim)
		return OpenedSession{}, err
	}

	if err := o.recordHostedBinding(ctx, sess, cfg, hosted, workspaceID, claim); err != nil {
		return OpenedSession{}, err
	}

	if hosted != nil && hosted.LifecycleLane != "" && o.laneRegistrar != nil {
		o.laneRegistrar.RegisterLifecycleLane(hosted.LifecycleLane, sess.ID())
	}

	return OpenedSession{Session: sess, Config: cfg, Hosted: hosted, WorkspaceID: workspaceID}, nil
}

// resolveRemote fills cfg's remote half. It is phase one's whole ssh body and
// it writes nothing outside cfg, so a refusal here has dialed nothing.
func (o *sessionOpener) resolveRemote(ctx context.Context, svc capability.OpenService, spec OpenSpec, cfg *session.Config) error {
	var remote *ssh.ConnectConfig

	switch {
	case spec.ProfileID != "":
		// Profile-based resolution: look up the stored profile, resolve
		// credentials and jump hosts through the profile resolver.
		if _, ok := o.resolver.get(); !ok {
			return refuse(-32603, "SSH sessions not available (no profile resolver wired)")
		}
		host, resolved, err := svc.Resolve(spec.ProfileID)
		if err != nil {
			o.log.Error("profile resolve failed", "profileId", spec.ProfileID, "error", err)
			// Resolving reads the stored password, so a sealed vault
			// surfaces here — the caller needs the reason to offer an
			// unlock.
			return refuseWithCause(-32603, err)
		}
		remote = resolved
		// The size is deliberately NOT set here (nocx-eidfb.1): the spec
		// carries what the caller MEASURED, and the session registry is what
		// decides the size the channel opens at. Writing it onto the
		// ConnectConfig as well would put the caller's number back on the
		// path the registry's conclusion travels.
		remote.RemoteLauncher = o.launcher
		remote.RemoteLifecycle = o.lifecycle

		o.log.Info("SSH open via profile", "profileId", spec.ProfileID, "host", host, "user", remote.User)

		cfg.Kind = session.KindRemote
		cfg.Host = host
		cfg.Remote = remote
		// Recorded here and nowhere else: the resolver has just accepted this
		// id, so the association is the backend's own conclusion rather than
		// the caller's claim.
		cfg.ProfileID = spec.ProfileID
		// CredentialID from the resolver: scoped revocation matches sessions
		// by credential. Empty for sessions with no linked credential.
		cfg.CredentialID = remote.CredentialID

	case spec.Host != "":
		// Direct host resolution: resolve through ~/.ssh/config (ssh -G) and
		// build a minimal ConnectConfig. Used for SSH aliases from the config
		// file — no stored profile involved.
		if o.sshCfg == nil {
			return refuse(-32603, "SSH config resolver not available")
		}
		resolved, err := o.sshCfg.ResolveConfig(ctx, spec.Host)
		if err != nil {
			o.log.Warn("SSH config resolution degraded for direct host", "host", spec.Host, "error", err)
		}

		user := spec.User
		if user == "" && resolved != nil && resolved.User != "" {
			user = resolved.User
		}
		port := 0
		if resolved != nil && resolved.Port > 0 {
			port = resolved.Port
		}
		remoteHost := spec.Host
		if resolved != nil && resolved.HostName != "" {
			remoteHost = resolved.HostName
		}
		var keyFile string
		if resolved != nil {
			keyFile = resolved.IdentityFile
		}
		remote = &ssh.ConnectConfig{
			User:    user,
			Port:    port,
			KeyFile: keyFile,
			// No size: see the profile branch above — the registry decides
			// it, and this struct carries what the caller supplied.
			RemoteLauncher:  o.launcher,
			RemoteInstaller: o.installer,
			RemoteLifecycle: o.lifecycle,
		}

		o.log.Info("SSH open via direct host", "host", spec.Host, "resolvedHost", remoteHost, "user", user)

		cfg.Kind = session.KindRemote
		cfg.Host = remoteHost
		cfg.Remote = remote
		// No ProfileID — this is not a saved profile. The usage tracker does
		// not record it.

	default:
		// Unreachable from the wire: validateOpenRaw refuses an ssh open that
		// names neither, with "profileId or host is required for an ssh
		// session", and that is the sentence a person actually sees
		// (ws_open_refusal_test.go). A second answer here would be a second
		// owner of one question — so this one names the first rather than
		// restating it, and exists only because a backend caller does not
		// pass through the wire validator.
		return refuse(-32602, "Invalid params: profileId or host is required for an ssh session")
	}

	// Shell pin (nocx-pu4.1): the open may name the far shell the launcher
	// must target. A pin beats auto-detection — a user who knows their host
	// runs zsh can say so, and where detection is wrong they have an
	// override. Anything else is ignored with a warn, never honoured:
	// detection is the safe degrade for a meaningless pin, and the launcher
	// refuses unmapped kinds rather than guessing if one slips past.
	if spec.Shell != "" {
		switch ssh.ShellKind(spec.Shell) {
		case ssh.ShellBash, ssh.ShellZsh, ssh.ShellUnknown, ssh.ShellAuto:
			remote.Shell = ssh.ShellKind(spec.Shell)
		default:
			o.log.Warn("ignoring unknown shell pin", "profileId", spec.ProfileID, "shell", spec.Shell)
		}
	}
	return nil
}

// recordHostedBinding persists the helper's authoritative id and binding facts
// before the session is handed back.
//
// The write comes first for a reason with both ends named: reconciliation may
// never observe a live helper session without the generation that qualifies
// its id space, so the interval in which that is possible must be empty. A
// failed write therefore REFUSES the open and closes the helper session,
// rather than returning a session no inventory may safely judge.
func (o *sessionOpener) recordHostedBinding(ctx context.Context, sess session.Session, cfg session.Config, hosted *HostedSessionOpen, workspaceID, claim string) error {
	if hosted == nil || o.ledger == nil {
		o.releaseClaim(ctx, claim)
		return nil
	}
	err := o.ledger.CreateSession(ctx, content.Session{
		ID:          string(sess.ID()),
		WorkspaceID: workspaceID,
		Host:        hosted.Host,
		Account:     hosted.Account,
		Generation:  hosted.Generation,
		// The route back, written in the same statement as the binding it
		// completes (nocx-k6p18.30). The pane and the profile come off the
		// config the registry has just accepted, never off the caller's
		// spec: a caller may name any pane it likes, and the session is what
		// records which one it actually became the pipe of. A direct-host
		// open carries no profile, writes none, and is therefore a session a
		// later coordinator cannot take back — which is stated by the empty
		// field rather than by a guess.
		PaneID:        cfg.PaneID,
		ProfileID:     cfg.ProfileID,
		HelperCommand: hosted.HelperCommand,
		Fingerprint:   hosted.Fingerprint,
	})
	if err == nil {
		// THE CLOSING END OF L7'S INTERVAL, and it is this line rather than
		// the spawn: the claim stands from before the spawn until the row
		// naming the session that spawn produced exists. Dropped after the
		// binding and never before it, so a crash between the two leaves the
		// binding AND the claim rather than neither.
		o.releaseClaim(ctx, claim)
		return nil
	}
	o.releaseClaim(ctx, claim)
	if hosted.AbortLifecycle != nil {
		hosted.AbortLifecycle()
	}
	_ = o.op.Run(ctx, func(_ context.Context, svc capability.OpenService) error {
		return svc.Close(sess.ID())
	})
	return fmt.Errorf("recording the helper binding: %w", err)
}

// claimSpawn writes the pane's durable claim on the spawn it is about to ask
// for, and answers with the idempotency key the spawn will carry (L7).
//
// THE COORDINATOR CANNOT RECORD WHAT IT IS ABOUT TO GET. The helper mints the
// session id, so there is no row to write naming a session that does not exist
// yet — only a row naming what was ASKED FOR. That is what the key is: a name
// for the SPAWN, minted here, written durably here, and carried into the
// helper, where a repeat with the same key answers with the session the first
// one made rather than forking a second shell (proto.SpawnParams.IdempotencyKey).
//
// The row's id IS the key, deliberately. A claim has no session id to be keyed
// by — that is the whole reason it exists — and a claim whose identity were a
// pane could not tell two spawns of one pane apart.
//
// LOCAL ONLY, and that is a scope statement rather than a rule. A remote
// hosted open has exactly the same hole between its spawn and its binding; it
// is not closed here because closing it changes the remote path's behaviour,
// which nocx-ie23r.3 is explicitly not allowed to do. The seam is the same one
// and the key travels the same way when that bead is taken.
func (o *sessionOpener) claimSpawn(ctx context.Context, cfg session.Config, workspaceID string) (string, error) {
	if o.helper == nil || o.ledger == nil || cfg.Kind != session.KindLocal {
		return "", nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", refuse(-32603, "the pane's claim on this session could not be minted: "+err.Error())
	}
	key := "spawn-" + hex.EncodeToString(raw[:])
	// Host, Account and Generation are deliberately EMPTY. They say which
	// helper owns an id space, and this row names no session id at all — it
	// names a request. Filling them in with this machine's own generation
	// would make a claim look like a binding to every reader of the table,
	// which is the one thing reconciliation must be able to tell apart.
	if err := o.ledger.CreateSession(ctx, content.Session{
		ID:          key,
		WorkspaceID: workspaceID,
		PaneID:      cfg.PaneID,
	}); err != nil {
		// The claim is not optional and a failure is not a degrade: without
		// it the spawn below would be the live PTY nothing claims, which is
		// exactly the state L7 exists to make unreachable.
		return "", fmt.Errorf("recording the pane's claim on this session: %w", err)
	}
	return key, nil
}

// releaseClaim drops a claim that is over, either way. It is best-effort by
// construction: the claim's purpose is served the moment the binding exists,
// and a delete that fails leaves a row reconciliation already knows how to
// judge — one naming no session any inventory can report.
func (o *sessionOpener) releaseClaim(ctx context.Context, claim string) {
	if claim == "" || o.ledger == nil {
		return
	}
	if err := o.ledger.DeleteSession(ctx, claim); err != nil {
		o.log.Warn("the pane's spawn claim outlived its spawn", "claim", claim, "error", err)
	}
}

// close ends a session the opener produced, through the same operation the
// open ran under. It exists so a caller that could not finish its own half —
// a ring that could not be created because the server is shutting down — ends
// the session rather than leaking it, without holding the capability seam
// itself.
func (o *sessionOpener) close(ctx context.Context, id session.ID) {
	_ = o.op.Run(ctx, func(_ context.Context, svc capability.OpenService) error {
		return svc.Close(id)
	})
}

// OpenSession is the backend's own way in (nocx-dkawo.6).
//
// It is the SAME path `open` takes and not a parallel one: the identical
// opener instance, the identical two-phase capability operation, the identical
// refusals. What a backend caller does not get is the second half — no ring,
// no subscriber, no ack — because it has no connection to attach and no
// request to answer. A client that later attaches picks the session up through
// `attach`, exactly as it does for a session whose client went away.
//
// The opener exists from construction, not from Start: the control plane —
// and with it the admission gates the two phases run under — is built by
// NewWSServer. That is what lets a composition root hand this to the wave
// record while it is still wiring, rather than having to wait for a server
// that is already serving. The nil guard is for a zero-value server, which is
// reachable only in-package and would otherwise panic inside the operation
// rather than say what is wrong.
func (s *WSServer) OpenSession(ctx context.Context, spec OpenSpec) (OpenedSession, error) {
	if s.opener == nil {
		return OpenedSession{}, errors.New("transport: the session opener is not running")
	}
	opened, err := s.opener.Open(ctx, spec)
	if err != nil {
		return OpenedSession{}, err
	}
	// THE LIFECYCLE LEG IS STARTED HERE, and this is the one place the two
	// callers legitimately differ in TIMING rather than in behaviour. The
	// handler starts it after the ack, because AD-7 requires every
	// session-scoped notification to follow the open result and the shell can
	// authenticate the moment the bridge is pumping. A backend caller has no
	// ack to order against, so there is nothing to wait for.
	//
	// It was missing entirely until nocx-ie23r.3, and it did not show: the
	// only backend-opened sessions were wave participants, and until this
	// machine's panes became helper sessions none of them was hosted. A
	// hosted session whose bridge is never pumped integrates never — its
	// shell says hello into a pipe nobody reads and the handshake bound
	// expires ten seconds later, which is a working terminal with the
	// integration silently off.
	if opened.Hosted != nil && opened.Hosted.StartLifecycle != nil {
		opened.Hosted.StartLifecycle()
	}
	return opened, nil
}
