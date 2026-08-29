package transport

// The session-plane control handlers as constructed types (migration map,
// "Session plane"): each handler holds its capability operation and the
// narrow transport seams it needs — never the *WSServer, so a handler cannot
// reach a store it was not constructed with.
//
// open, resize, close and attach run on the ordinary lane under their
// SessionOperation/OpenOperation gates; ack is ingress-critical ring trimming
// and runs inline via the ImmediateSubmission (registration.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/workspace"
)

// sessionMachine is the transport-owned session lifecycle surface the
// session-plane handlers need: rings, resize lanes, teardown and the
// files.changed flush. WSServer implements it; a handler is constructed with
// the interface, so it can reach exactly these operations and nothing else on
// the server. This is transport lifecycle, not a store — no capability gates
// it (migration map, close finding).
type sessionMachine interface {
	getRx(sid session.ID) *sessionRx
	getOrCreateRx(sid session.ID) *sessionRx
	removeRx(sid session.ID) *sessionRx
	laneFor(sid session.ID, sess session.Session) *sessionLane
	// takeSize hands a session the geometry of the client that now owns it
	// (nocx-eidfb.2). One narrow method rather than the lane itself: the
	// handler may report the take it just performed, and the failure and the
	// tombstone are answered in one place for both callers.
	takeSize(sid session.ID, sess session.Session, reported session.Size)
	closeLane(sid session.ID)
	closeSession(sid session.ID, sess session.Session)
	// markCloseRequested records that this session's end was asked for, so
	// the exit it produces is not filed as news the user has to read.
	markCloseRequested(sid session.ID)
	ringToConn(ctx context.Context, wconn *wsConn, sidBytes [16]byte, rx *sessionRx, startOffset uint64)
	flushFilesChanged(sid session.ID, wconn Responder)
	// flushUploadDone re-emits the upload outcomes that settled while
	// nothing was attached (upload design §5.3). Separate from the files
	// flush because it is a terminal fact rather than an invalidation: a
	// missed files.changed costs a stale listing the next poll corrects,
	// and a missed uploadDone leaves the UI saying "uploading" forever.
	flushUploadDone(sid session.ID, wconn Responder)
	notifyInputStalled(sid session.ID)
	// announceDisplacement tells the client that has just lost a session that
	// it lost it, and takes the session out of its connection state (D8). One
	// narrow method rather than the notification machinery itself: the handler
	// may report the displacement it caused, and nothing more.
	announceDisplacement(sid session.ID, ident session.Identity, prev *wsConn, prevState *connState)
	// replayLifecycleFacts re-emits the current lifecycle projection of the
	// session's lanes on reattach (ADR-0024 decision 8 / AD-9). One narrow
	// method rather than the publisher itself: the handler may resynchronise
	// a session it already owns, and nothing more.
	replayLifecycleFacts(sid session.ID)
	// replayIntegration re-sends the session's integration status on
	// reattach (nocx-dvql). Separate from the lifecycle replay because it
	// is a state rather than a transition: a frontend that reconnects after
	// the handshake expired must learn it is in a conventional terminal,
	// and no further transition is ever coming to tell it.
	replayIntegration(sid session.ID)
	// replayPaneObservation re-sends an enrolled pane's current
	// classification on reattach (nocx-szb40.3). Beside replayIntegration
	// and for the identical reason: only changes are pushed, so a renderer
	// that reconnects to a settled agent would otherwise never be told what
	// the pane is.
	replayPaneObservation(sid session.ID)
}

// openMachine is the transport-owned machinery handleOpen needs after the
// dial: rings, the output pump, the exit monitor, stored-forward replay and
// the discovery hooks. Same narrow-surface rule as sessionMachine.
type openMachine interface {
	getOrCreateRx(sid session.ID) *sessionRx
	removeRx(sid session.ID) *sessionRx
	pumpToRing(ctx context.Context, sess session.Session, ring *outputRing)
	monitorExit(rx *sessionRx, sess session.Session)
	ringToConn(ctx context.Context, wconn *wsConn, sidBytes [16]byte, rx *sessionRx, startOffset uint64)
	replayStoredForwards(profileID, host string, cfg *ssh.ConnectConfig)
	discoveryUp(profileID, host string, cfg *ssh.ConnectConfig)
	discoveryUpLocal()
	// Replays any lifecycle fact that arrived while open was still dialing.
	// Called only after the result has published the authoritative session id.
	replayLifecycleFacts(sid session.ID)
	// The session integration axis (nocx-dvql): the remote registration
	// from the connect path's own decision, and the first emission — which
	// must happen AFTER the open ack (AD-7).
	registerRemoteIntegration(sess session.Session, cfg session.Config)
	emitIntegration(sid session.ID)
}

// openHandlers answers "open". It holds the two-phase OpenOperation — the
// resolve under the [config, session] gates, the dial under neither — and
// the seams the dial needs.
// It needs the connection as identity, not just as a writer: it registers
// the connection as the session's subscriber, so the handler receives the
// *wsConn per call.
type openHandlers struct {
	op       capability.OpenOperation
	sess     openMachine
	resolver *resolverHolder // profile resolver, readable post-construction
	sshCfg   ssh.ConfigResolver
	launcher ssh.RemoteLauncher
	// installer publishes the bundle over SFTP on the direct-host path,
	// which is the only thing that installs it now that the command
	// carries no payload.
	installer ssh.RemoteInstaller
	// lifecycle is the authenticated-channel seam (ADR-0024): the dial
	// hands it to the far side so the shell can hand its lifecycle back
	// over a channel that is not the terminal. An explicit seam, not the
	// whole server.
	lifecycle ssh.RemoteLifecycle
	// panes resolves pane → tab → workspace for the open ack's workspaceId
	// (nocx-isoph.2). nil when the content store is not wired, which is the
	// honest state and not a degrade to hide: with no layout store there is
	// no chain to walk and every session is in the default workspace.
	//
	// It is the store's READ seam and not the gated LayoutOperation, and the
	// reason is a deadlock rather than a convenience. This handler already
	// runs inside the open operation, which holds [config, session] and then
	// the execution lane; acquiring the content operation inside it would
	// take a second lane permit while holding one, and with every lane permit
	// held by an open the whole control plane would stop. The read itself
	// needs no gate: layout reads go straight to the pool and never through
	// the single writer goroutine.
	panes paneWorkspaces
	log   log.Logger
}

// paneWorkspaces answers "which workspace is this pane in" — the one
// derivation §4.5 leaves in the backend, satisfied by
// content.LayoutRepository. Declared here as the narrow seam this handler
// needs rather than taken as the whole repository: an open may resolve a
// workspace and may not write a layout row.
type paneWorkspaces interface {
	WorkspaceForPane(ctx context.Context, paneID string) (string, error)
}

// workspaceForOpen derives the workspace this session's ack will carry.
//
// THE CHAIN IS THE ANSWER, never a value the renderer sent: the renderer
// names a PANE — the durable identity it already owns — and the backend walks
// pane → tab → workspace itself. A paneId naming no pane is refused rather
// than defaulted, because "the pane you named does not exist" and "you named
// no pane" are different facts and answering both with the default would hide
// the first.
//
// No paneId is the second fact, and it is the ordinary one until the renderer
// starts minting panes (nocx-isoph.4): the session is in the default
// workspace, resolved through internal/workspace.Default, which is the single
// owner of that decision (AD-7).
func (h openHandlers) workspaceForOpen(ctx context.Context, paneID string) (string, error) {
	if paneID == "" || h.panes == nil {
		return string(workspace.Default), nil
	}
	return h.panes.WorkspaceForPane(ctx, paneID)
}

// openResult is the open ack payload, declared once (contracts/open.schema
// .json) and pinned by the DTO contract test. The session identity
// (nocx-3oupk) rides it because this is where the renderer first learns
// the session: instanceId + sessionEpoch are minted by the backend
// (AD-7), never here or on the renderer, and every later observation of
// this session is compared against the pair this ack carried.
type openResult struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	// WorkspaceID is never empty and has no omitempty: a tab is always
	// in a workspace and there is no null (design §4.2). The renderer
	// reads it from here rather than assuming a default, because the
	// default never renders and so the renderer has no name for it.
	WorkspaceID string `json:"workspaceId"`
	Cwd         string `json:"cwd"`
	DesiredMode string `json:"desiredMode"`
	// EffectiveSize is the geometry the BACKEND decided this session runs
	// at (nocx-eidfb.1). The open params carry what the client measured;
	// this carries what was done with it, so a renderer learns the answer
	// rather than assuming its own report was adopted. It is never absent
	// and never zero: a session with no client attached holds the named
	// default, which is the state this field exists to make expressible.
	EffectiveSize sizeResult `json:"effectiveSize"`
	// Parent is the edge the backend RECORDED, echoed back so the renderer
	// stores what was admitted rather than what it asked for (nocx-9hu9d).
	// Null for a root session — and null rather than absent, because the
	// schema requires the key: an omitempty here would drop it for every root
	// session and leave "no parent" indistinguishable from "an old backend".
	Parent *openParentResult `json:"parent"`
}

// sizeResult is a session's geometry on the wire, in the same four words
// AD-1's open and resize params use — one shape for one concept, so the
// answer cannot be spelled differently from the question.
type sizeResult struct {
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	XPixel uint16 `json:"xpixel"`
	YPixel uint16 `json:"ypixel"`
}

// sizeResultOf renders a session's effective size onto the wire. One place
// converts, for the reason parentResultFor is one place (AD-8).
func sizeResultOf(s session.Size) sizeResult {
	return sizeResult{Cols: s.Cols, Rows: s.Rows, XPixel: s.XPixel, YPixel: s.YPixel}
}

// openParentResult is the recorded parent edge on the wire: the full identity
// of the session that opened this one, in the same three words the open params
// and every later observation use.
type openParentResult struct {
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
}

// parentResultFor renders a session's recorded parent edge onto the wire, or
// nil for a root session. One place converts the record into the wire shape,
// so a later reader of this edge cannot spell it differently (AD-8).
func parentResultFor(sess session.Session) *openParentResult {
	edge, has := sess.Parent()
	if !has {
		return nil
	}
	return &openParentResult{
		SessionID:    string(edge.ID),
		InstanceID:   string(edge.Identity.InstanceID),
		SessionEpoch: edge.Identity.Epoch,
	}
}

// isLineageRefusal reports whether err is the session registry refusing a
// parent claim. It decides one thing: whether the renderer sent something that
// can never work (-32602, the caller's fault) or the open failed for a reason
// retrying might survive (-32603). Every lineage sentinel is named here rather
// than matched on a message, so adding one without deciding its wire answer is
// a compile-visible omission rather than a silent -32603.
func isLineageRefusal(err error) bool {
	return errors.Is(err, session.ErrParentUnknown) ||
		errors.Is(err, session.ErrParentForeignInstance) ||
		errors.Is(err, session.ErrParentSelf) ||
		errors.Is(err, session.ErrParentCycle) ||
		errors.Is(err, session.ErrTooDeep)
}

// answerOpenFailure maps a failed phase of an open onto the wire. Both
// phases end here — the resolve's sealed vault and the dial's ssh taxonomy
// are the same answer to the caller, which asked for a terminal and did not
// get one — so the mapping lives in one place rather than being written
// twice and drifting once.
func (h openHandlers) answerOpenFailure(r Responder, req jsonrpcRequest, err error) {
	// A gate refusal: another operation holds the config or session
	// domain — the request is refused, never queued.
	if capability.IsRefused(err) {
		var rej *capability.RefusedError
		errors.As(err, &rej)
		// Both sides of the merge: main's refusal now names the method
		// it refused (nocx-rq9p), and this handler's writes go through
		// the Responder rather than the raw connection, so the sealed
		// normalizer sees them (nocx-k41yv).
		_ = r.TryError(req.ID, saturationRPCError(req.Method, &rej.Rejection))
		return
	}
	// A refused parent edge is a bad claim in the params, not a server
	// fault: nothing the renderer can retry will make it true, and
	// answering -32603 would invite exactly that retry (nocx-9hu9d).
	if isLineageRefusal(err) {
		h.log.Warn("open refused: parent edge", "error", err)
		_ = respond(r, newJSONRPCError(req.ID, -32602, "Invalid params: "+err.Error()))
		return
	}
	h.log.Error("failed to open session", "error", err)
	// A sealed vault surfaces here for EVERY connection that needs it —
	// this is still a vault access, and the renderer must get the reason
	// so the vault-owned unlock prompt appears instead of an error
	// (the dispatcher intercepts reason="vault-sealed" on any RPC).
	if errors.Is(err, vault.ErrVaultSealed) || errors.Is(err, vault.ErrVaultUninitialized) {
		_ = r.TryError(req.ID, rpcErrorFor(-32603, "", err))
		return
	}
	// Classify the SSH error through the same taxonomy the probe uses
	// so the user sees what actually failed, not "Internal error".
	pr := classifyProbeError(err)
	var msg string
	if pr.err == nil {
		msg = string(pr.outcome) + ": " + pr.detail
	} else {
		msg = err.Error() // unclassifiable — use the raw wrapped error
	}
	resp := newJSONRPCError(req.ID, -32603, msg)
	// For host-key errors, attach the evidence so the renderer can
	// offer the accept-on-first-use dialog (the same one the probe
	// path raises). Without this, open shows "Terminal failed to
	// start" and the user has no way to accept the key (nocx-shat).
	if hk := hostKeyInfoFromError(err); hk != nil {
		resp.Error.Data = hk
	}
	_ = respond(r, resp)
}

// handleOpen creates a new session and output ring.
//
// Per AD-7: the server assigns the authoritative session-id. The JSON-RPC
// request id serves as the correlation-id — we do NOT add a second
// correlationId field, because two correlation identifiers for one exchange
// is redundant state with two owners.
func (h openHandlers) handleOpen(ctx context.Context, wconn *wsConn, r Responder, state *connState, req jsonrpcRequest) {
	var params openParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Cols == 0 || params.Rows == 0 {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: cols and rows required")
		_ = respond(r, resp)
		return
	}

	// The workspace is resolved BEFORE anything is spawned or dialed, for
	// the reason the parent claim is: a request that cannot be satisfied must
	// not cost the user a shell or an ssh handshake, and a refused open must
	// leave nothing behind.
	workspaceID, wsErr := h.workspaceForOpen(ctx, params.PaneID)
	if wsErr != nil {
		_ = respond(r, newJSONRPCError(req.ID, -32602, "Invalid params: "+wsErr.Error()))
		return
	}

	cfg := session.Config{
		Kind: session.KindLocal,
		// The client's REPORT of its own geometry, carried through as a
		// measurement (nocx-eidfb.1). The registry decides the size the
		// channel is created at and the ack reports what it decided, so
		// nothing here may treat these four as the answer — including this
		// handler, which reads the size back off the session below rather
		// than echoing what it just sent.
		Cols:   params.Cols,
		Rows:   params.Rows,
		XPixel: params.XPixel,
		YPixel: params.YPixel,
		// Every session asks to be integrated, and the ones that cannot be
		// fall back to an ordinary terminal (nocx-tr2n). This is not a
		// policy the renderer may express: it arrived as an `enhanced` open
		// parameter, both ssh openers omitted it, and the result was a
		// second — silent, always-negative — answer to the question
		// `desiredMode` (raw|script|relay) already answers per connection
		// (AD-8). Nothing below fails closed on the request: a launcher
		// that declines, a channel the far sshd refuses, a raw destination
		// all end at a visible native prompt, so asking always is the safe
		// direction and forgetting to ask is the one that shipped a tab
		// with no blocks and no diagnostic.
		Enhanced: true,
		// The pane this session is the pipe of, recorded so the ledger can
		// anchor every block it records without the renderer restating it
		// per event (nocx-rtg0.28, design §6.1). It is the id the chain walk
		// above has already resolved, so a session never carries one that
		// names nothing.
		PaneID: params.PaneID,
	}
	// The claimed parent edge (nocx-9hu9d). Carried into the registry as a
	// claim; the registry is the single owner of whether it may be recorded,
	// and it refuses before anything is spawned. Absent means a root session.
	if params.Parent != nil {
		cfg.Parent = session.Ref{
			ID: session.ID(params.Parent.SessionID),
			Identity: session.Identity{
				InstanceID: session.InstanceID(params.Parent.InstanceID),
				Epoch:      params.Parent.SessionEpoch,
			},
		}
	}
	// ProfileID is deliberately NOT set here. It is recorded below, only once
	// the resolver has accepted it, because a local PTY has no profile and
	// setting it up front lets a renderer attach any profile id to a local
	// session it opens. sessions.status would then report that profile live and
	// the connection list would draw a row as connected with nothing behind it
	// (nocx-uxs5.4).

	var sess session.Session
	opened := false
	// answered: a resolve step already wrote the response (no resolver
	// wired, no target named, a sealed vault). Nothing is dialed after one.
	answered := false
	// PHASE ONE — resolve, under [config, session]. Store and vault reads
	// only; both gates are released before anything is dialed (open.go).
	err := h.op.Prepare(ctx, func(ctx context.Context, svc capability.OpenService) error {
		// SSH session — when kind="ssh", open a remote channel instead of
		// local PTY. Only the RESOLVE runs here.
		if params.Kind == "ssh" {
			var host string
			var remote *ssh.ConnectConfig

			if params.ProfileID != "" {
				// Profile-based resolution: look up the stored profile, resolve
				// credentials and jump hosts through the profile resolver.
				if _, ok := h.resolver.get(); !ok {
					resp := newJSONRPCError(req.ID, -32603, "SSH sessions not available (no profile resolver wired)")
					_ = respond(r, resp)
					answered = true
					return nil
				}

				var err error
				host, remote, err = svc.Resolve(params.ProfileID)
				if err != nil {
					h.log.Error("profile resolve failed", "profileId", params.ProfileID, "error", err)
					// Resolving reads the stored password, so a sealed vault surfaces
					// here — the renderer needs the reason to offer an unlock.
					_ = r.TryError(req.ID, rpcErrorFor(-32603, "", err))
					answered = true
					return nil
				}

				// The size is deliberately NOT set here (nocx-eidfb.1): the
				// params carry what the client MEASURED, and the session
				// registry is what decides the size the channel opens at.
				// Writing it onto the ConnectConfig as well would put the
				// renderer's number back on the path the registry's
				// conclusion travels.
				remote.RemoteLauncher = h.launcher
				remote.RemoteLifecycle = h.lifecycle

				h.log.Info("SSH open via profile", "profileId", params.ProfileID, "host", host, "user", remote.User)

				cfg.Kind = session.KindRemote
				cfg.Host = host
				cfg.Remote = remote
				// Recorded here and nowhere else: the resolver has just accepted this
				// id, so the association is the backend's own conclusion rather than
				// the renderer's claim.
				cfg.ProfileID = params.ProfileID
				// CredentialID from the resolver: scoped revocation matches
				// sessions by credential. Empty for sessions with no linked
				// credential (inline auth).
				cfg.CredentialID = remote.CredentialID

			} else if params.Host != "" {
				// Direct host resolution: resolve through ~/.ssh/config (ssh -G)
				// and build a minimal ConnectConfig. Used for SSH aliases from
				// the config file — no stored profile involved.
				if h.sshCfg == nil {
					resp := newJSONRPCError(req.ID, -32603, "SSH config resolver not available")
					_ = respond(r, resp)
					answered = true
					return nil
				}

				resolved, err := h.sshCfg.ResolveConfig(ctx, params.Host)
				if err != nil {
					h.log.Warn("SSH config resolution degraded for direct host", "host", params.Host, "error", err)
				}

				user := params.User
				if user == "" && resolved != nil && resolved.User != "" {
					user = resolved.User
				}
				port := 0
				if resolved != nil && resolved.Port > 0 {
					port = resolved.Port
				}
				remoteHost := params.Host
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
					// No size: see the profile branch above — the registry
					// decides it, and this struct carries what the caller
					// supplied (nocx-eidfb.1).
					RemoteLauncher:  h.launcher,
					RemoteInstaller: h.installer,
					RemoteLifecycle: h.lifecycle,
				}

				h.log.Info("SSH open via direct host", "host", params.Host, "resolvedHost", remoteHost, "user", user)

				cfg.Kind = session.KindRemote
				cfg.Host = remoteHost
				cfg.Remote = remote
				// No ProfileID — this is not a saved profile. The usage tracker
				// does not record it.
			} else {
				resp := newJSONRPCError(req.ID, -32602, "Invalid params: profileId or host required for ssh session")
				_ = respond(r, resp)
				answered = true
				return nil
			}
			// Shell pin (nocx-pu4.1): the open may name the far shell the
			// launcher must target. A pin beats auto-detection — a user who
			// knows their host runs zsh can say so, and where detection is
			// wrong they have an override. Anything else is ignored with a
			// warn, never honoured: detection is the safe degrade for a
			// meaningless pin, and the launcher refuses unmapped kinds rather
			// than guessing if one slips past.
			if params.Shell != "" {
				switch ssh.ShellKind(params.Shell) {
				case ssh.ShellBash, ssh.ShellZsh, ssh.ShellUnknown, ssh.ShellAuto:
					remote.Shell = ssh.ShellKind(params.Shell)
				default:
					h.log.Warn("ignoring unknown shell pin", "profileId", params.ProfileID, "shell", params.Shell)
				}
			}
		}
		return nil
	})
	if err != nil {
		h.answerOpenFailure(r, req, err)
		return
	}
	if answered {
		return
	}

	// PHASE TWO — dial, on the execution lane and no domain gate. The
	// handshake waits on the network and sometimes on a person (the
	// password prompt), and a gate held across that wait is what refused
	// every other pane's open with "the terminal is busy" while one tab
	// was still connecting.
	err = h.op.Dial(ctx, func(ctx context.Context, svc capability.OpenService) error {
		var oerr error
		sess, oerr = svc.Open(ctx, cfg)
		if oerr != nil {
			return oerr
		}
		opened = true
		return nil
	})
	if err != nil {
		h.answerOpenFailure(r, req, err)
		return
	}
	if !opened {
		// The callback answered a refusal already (missing resolver,
		// missing target); nothing further to do.
		return
	}

	state.add(sess)

	rx := h.sess.getOrCreateRx(sess.ID())
	if rx == nil {
		state.remove(sess.ID())
		_ = h.op.Run(ctx, func(ctx context.Context, svc capability.OpenService) error {
			return svc.Close(sess.ID())
		})
		resp := newJSONRPCError(req.ID, -32603, "Internal error: server shutting down")
		_ = respond(r, resp)
		return
	}

	// Port discovery (nocx-wzc4.2): only now, once the session's ring
	// exists, is the target "up" — a session that failed its ring setup
	// must not leave a discovery target behind with nobody to tear it down.
	// The subscriber is NOT attached yet; it lands after the open result
	// below, because a session-scoped notification may not precede the id
	// that addresses it (AD-7). This call only schedules — whatever the
	// scheduler later publishes does its own subscriber lookup — so the
	// ring alone is the condition the target's liveness waits on.
	switch {
	case cfg.ProfileID != "":
		h.sess.discoveryUp(cfg.ProfileID, cfg.Host, cfg.Remote)
	case cfg.Kind == session.KindLocal:
		// A local tab is a target too: the machine listens like any host,
		// and the same ladder finds it (nocx-wzc4.8). Keyed by the
		// reserved LocalTargetID, torn down when the last local tab closes.
		h.sess.discoveryUpLocal()
	}

	// cwd rides the open result so the tab has a name before any program sets
	// a title (nocx-9vr). It is the starting directory only — following `cd`
	// needs OSC 7 (nocx-5mn.2).
	// shellIntegrationReason no longer rides it (nocx-dvql). It could only
	// answer once, at open, and the two failures that matter most arrive
	// later: a handshake that expires ten seconds in, and a channel lost
	// mid-session. session.integrationChanged answers the same question as
	// a state that keeps being revised, and two places answering it would
	// be the defect AD-8 names — so the field is removed, not kept beside
	// the notification.
	// desiredMode still rides it and carries the RESOLVED destination mode
	// (nocx-mlm7): the connection-scope default the tab's capability control
	// starts from — script wraps and installs automatically, raw adds
	// nothing, relay is consent-gated. It is the mode, never proof
	// integration succeeded.
	// parent rides the ack as the edge the REGISTRY recorded, read back off
	// the session rather than echoed from the params: the two agree only
	// because the claim was admitted, and reading the record is what makes the
	// ack an answer instead of a repetition (nocx-9hu9d).
	ident := sess.Identity()
	result := openResult{
		SessionID:    string(sess.ID()),
		InstanceID:   string(ident.InstanceID),
		SessionEpoch: ident.Epoch,
		WorkspaceID:  workspaceID,
		Cwd:          sess.Cwd(),
		DesiredMode:  desiredModeForAck(cfg.Remote),
		// Read off the SESSION, never echoed from the params: the two agree
		// only when the report was adopted, and reading the record is what
		// makes the ack an answer instead of a repetition.
		EffectiveSize: sizeResultOf(sess.EffectiveSize()),
		Parent:        parentResultFor(sess),
	}
	resultJSON, _ := json.Marshal(result)
	resp := newJSONRPCResult(req.ID, resultJSON)
	_ = respond(r, resp)

	// Every session-scoped notification must follow the open result (AD-7).
	// Install the subscriber only now: lifecycle can authenticate during the
	// dial above, and publishing it to this shared WebSocket before the
	// renderer knows sessionId lets an existing tab claim the fact. The
	// current projection is replayed immediately after installation, so a
	// fact dropped during the pre-result window is not lost.
	// Nothing can have been attached to a session that did not exist a moment
	// ago, so the displaced pair is discarded here rather than announced: the
	// slot is empty by construction on this path, and a displacement that
	// cannot happen must not be dressed up as one that did.
	_, _ = rx.setSubscriber(wconn, state)
	h.sess.replayLifecycleFacts(sess.ID())
	// A remote session's launch-time refusal is registered here rather than
	// at the dial because ShellIntegrationReason is the ssh channel's own
	// answer and this is where the session first exists as a session. A
	// local session was registered by the pty factory, which is the only
	// thing that knows which binary it exec'd; registering it twice is what
	// AD-8 forbids, so registerRemoteIntegration returns early for one.
	h.sess.registerRemoteIntegration(sess, cfg)
	h.sess.emitIntegration(sess.ID())

	// Stored forwards (nocx-wzc4.5): replay the profile's configured
	// forwards onto the connection. Deliberately ASYNC and only after the
	// ack — a slow connector acquire must never delay the open result.
	// The rows are connection-owned, not tab-owned (spec §7.3): closing
	// this tab leaves them running.
	if cfg.ProfileID != "" {
		go h.sess.replayStoredForwards(cfg.ProfileID, cfg.Host, cfg.Remote)
	}

	// Start the PTY → ring output pump only after the ack is sent.
	// AD-7: the ack must precede the session's own traffic in both
	// directions, otherwise the first prompt races the open result and
	// the client drops it (its sessionId is still null).
	// Background is deliberate — server/session-owned, the canonical member
	// of that class. Owner: the session and its replay ring, which outlive
	// every WebSocket (AD-9); the pump must survive a disconnect so the
	// session's output keeps flowing into the ring for the next reattach.
	// Closing event: session teardown — closeSession's registry.Close, which
	// ends the read pump StartOutput started. This call itself returns
	// immediately: StartOutput installs the handler and starts that pump on
	// its own goroutine rather than blocking, so nothing may hang off its
	// return as though it meant "the output is over" (nocx-szb40.5).
	go h.sess.pumpToRing(context.Background(), sess, rx.ring)

	// Start exactly one monitorExit goroutine per session (DEFECT 2).
	rx.monitorOnce.Do(func() {
		go h.sess.monitorExit(rx, sess)
	})

	sidBytes, _ := session.IDToBytes(sess.ID())
	go h.sess.ringToConn(ctx, wconn, sidBytes, rx, 0)
}

// sessionOpsHandlers answers resize, close and attach: the per-session
// operations (SessionOperation via ForSession) plus the transport lanes. It
// needs the connection's connState (session ownership checks) per call.
type sessionOpsHandlers struct {
	ops *capability.SessionOperations // nil → session store not wired
	r   Responder
	// instance is THIS backend's identity, read from the registry at
	// construction and held as a value: a claim is judged against it, and the
	// judgement must be possible when the registry holds nothing. A value
	// rather than the registry itself, because a handler that could reach the
	// registry could do more than judge (migration map: handlers hold seams,
	// never stores).
	instance session.InstanceID
	machine  sessionMachine
}

// handleResize enqueues a resize into the session's operation lane.
func (h sessionOpsHandlers) handleResize(ctx context.Context, state *connState, req jsonrpcRequest) {
	var params resizeParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" || params.Cols == 0 || params.Rows == 0 {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId, cols, and rows required")
		_ = respond(h.r, resp)
		return
	}

	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = respond(h.r, resp)
		return
	}

	op, err := h.ops.ForSession(sid)
	if err != nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = respond(h.r, resp)
		return
	}
	err = op.Run(ctx, func(ctx context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
			_ = respond(h.r, resp)
			return nil
		}

		// The resize is handed to the session's lane, which applies it off the
		// read loop with a per-session cancellable context: a window-change
		// blocked on a dead transport must not freeze this connection, and the
		// session's close (which cancels the lane) must not queue behind it.
		// The response completes when the lane settles the op (applied,
		// superseded, or cancelled by close) — the renderer never reads it.
		rop := &resizeOp{
			reported: session.Size{
				Cols:   params.Cols,
				Rows:   params.Rows,
				XPixel: params.XPixel,
				YPixel: params.YPixel,
			},
			done: func(err error) {
				if err != nil {
					resp := newJSONRPCError(req.ID, -32603, "Internal error")
					_ = respond(h.r, resp)
					return
				}
				result, _ := json.Marshal(map[string]any{})
				resp := newJSONRPCResult(req.ID, result)
				_ = respond(h.r, resp)
			},
		}
		if !h.machine.laneFor(sid, sess).enqueue(rop) {
			// The session's close was already admitted (a second connection may
			// have closed it between the checks above and the enqueue): the
			// resize cannot reach it. Same refusal as a session the registry
			// no longer holds.
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
			_ = respond(h.r, resp)
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleClose closes a session: the resize lane's terminal gate first (a
// close must never queue behind a dead resize), then the registry close
// through the session operation, then the transport teardown (rings,
// bindings, discovery — migration map, close finding). The git/files
// binding teardown is shared transport lifecycle, not a handler capability:
// it also runs on AD-9 disconnect via monitorExit, and the registries are
// their own exclusion.
func (h sessionOpsHandlers) handleClose(ctx context.Context, state *connState, req jsonrpcRequest) {
	var params closeParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId required")
		_ = respond(h.r, resp)
		return
	}

	sid := session.ID(params.SessionID)
	if !state.has(sid) {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = respond(h.r, resp)
		return
	}

	op, err := h.ops.ForSession(sid)
	if err != nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = respond(h.r, resp)
		return
	}
	err = op.Run(ctx, func(ctx context.Context, svc capability.SessionService) error {
		// Close is terminal for the session's resize lane: from here, queued
		// and in-flight resizes are cancelled and nothing new may reach the
		// session. Runs BEFORE the teardown below, and never waits for the
		// lane's worker — the one operation that can unblock a dead resize
		// must not queue behind it.
		h.machine.closeLane(sid)

		sess, gerr := svc.Get(sid)
		if gerr != nil {
			// The session is already gone from the registry; the transport
			// teardown still runs (idempotent).
			h.machine.closeSession(sid, nil)
			state.remove(sid)
			result, _ := json.Marshal(map[string]any{})
			resp := newJSONRPCResult(req.ID, result)
			_ = respond(h.r, resp)
			return nil
		}
		// The end is the user's own doing, and the session layer cannot
		// see that: a forced teardown records no shell report, so the exit
		// it produces reads as ExitInterrupted like any dropped connection.
		// Marked BEFORE the registry close, because the registry close is
		// what wakes monitorExit — which takes the marker and files
		// nothing. Not marked on the branch above: the session is already
		// out of the registry there, so no exit of ours is coming and the
		// entry would have nothing to clear it.
		h.machine.markCloseRequested(sid)
		_ = svc.Close(sid)
		h.machine.closeSession(sid, sess)
		state.remove(sid)

		result, _ := json.Marshal(map[string]any{})
		resp := newJSONRPCResult(req.ID, result)
		_ = respond(h.r, resp)
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleAttach gives a connection a session's output stream from a byte
// offset: the AD-9 reconnect, and the CLAIM by which a fresh client takes back
// a pane it has never seen (nocx-oevq4, the nocx-server design D5).
//
// ONE METHOD FOR BOTH, deliberately. The fresh client differs from the
// reconnecting one in exactly one thing — where it learned the session and the
// offset — and that difference is answered by sessions.live, not by a second
// reattach. The ring, the offsets, the reset and the replays are already this
// handler's, and a second path to them would be the concept implemented twice.
//
//	--> {"jsonrpc":"2.0","id":N,"method":"attach","params":{"sessionId":"...","instanceId":"...","sessionEpoch":1,"offset":1234}}
//
// Result when offset is still in the ring:
//
//	<-- {"jsonrpc":"2.0","id":N,"result":{"resumed":true,"reset":false,"from":1234}}
//
// Result when offset is too old (ring has advanced past it):
//
//	<-- {"jsonrpc":"2.0","id":N,"result":{"resumed":false,"reset":true,"from":5678}}
//
// A refused claim → -32602 carrying data.reason, one of foreign-instance,
// foreign-incarnation or unknown-session (judgeClaim owns which).
// Offset ahead of written → JSON-RPC error (DEFECT 4).
// Duplicate attach on the same connection → JSON-RPC error (DEFECT 3).
//
// A successful attach TAKES the session (D8): whoever held it is told and
// stops holding it, before this connection is given a byte.
func (h sessionOpsHandlers) handleAttach(ctx context.Context, wconn *wsConn, r Responder, state *connState, req jsonrpcRequest) {
	var params attachParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId and offset required")
		_ = respond(r, resp)
		return
	}

	sid := session.ID(params.SessionID)

	// The instance is judged before the session is looked up at all, so a
	// claim carried across a coordinator restart is answered "that is a
	// different backend" rather than "no such session" — see judgeClaim.
	// ForSession's own failure is indistinguishable from an unknown session,
	// so it is answered in the claim's vocabulary too.
	if foreignInstance(h.instance, params.InstanceID) {
		_ = r.TryError(req.ID, foreignInstanceRefusal())
		return
	}

	op, err := h.ops.ForSession(sid)
	if err != nil {
		_ = r.TryError(req.ID, refuseClaim(reasonUnknownSession, "Invalid params: unknown sessionId"))
		return
	}
	err = op.Run(ctx, func(ctx context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			sess = nil
		}
		if refusal := judgeClaim(h.instance, params.InstanceID, params.claimedEpoch(), sess); refusal != nil {
			_ = r.TryError(req.ID, *refusal)
			return nil
		}

		// Reject duplicate attach on the same connection (DEFECT 3).
		// Without this guard, handleOpen already started a ringToConn for the
		// open connection; a second attach on the same session would start
		// another ringToConn, doubling every output byte for that subscriber.
		if state.has(sid) {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: already attached to this session")
			_ = respond(r, resp)
			return nil
		}

		rx := h.machine.getRx(sid)
		if rx == nil {
			// The registry holds the session and the transport does not hold
			// its ring: a teardown in flight, or an open that has not reached
			// its ring yet. Same word as an unknown session, because the same
			// thing is true of the claim — there is no stream to give it.
			_ = r.TryError(req.ID, refuseClaim(reasonUnknownSession, "Invalid params: unknown sessionId"))
			return nil
		}

		// Reject offsets that run ahead of what the ring has produced (DEFECT 4).
		// ring.ack already validates this; attach must be equally distrustful.
		// An offset > written means the client claims to have received bytes
		// that were never produced — a silent data skip waiting to happen.
		// Uses the locking accessor rather than reaching into the ring's mu.
		w := rx.ring.writtenLocked()
		if params.Offset > w {
			resp := newJSONRPCError(req.ID, -32602, fmt.Sprintf("Invalid params: offset %d exceeds written %d", params.Offset, w))
			_ = respond(r, resp)
			return nil
		}

		_, from, needsReset := rx.ring.snapshot(params.Offset)

		state.add(sess)
		prev, prevState := rx.setSubscriber(wconn, state)
		if prev != nil && prev != wconn {
			// The take, before this connection is answered: the loser is told,
			// loses the session from its own state, and its pump is woken so
			// it observes that it is no longer the subscriber and stops. The
			// wake is needed because a pump parked in waitForData would
			// otherwise go on holding the session until its next byte.
			h.machine.announceDisplacement(sid, sess.Identity(), prev, prevState)
			rx.ring.wake()
		}

		// THE OTHER HALF OF THE TAKE (nocx-eidfb.2): the client that attached
		// last is the active one, and the shared channel resizes to ITS
		// geometry. Beside the displacement rather than anywhere else,
		// because they are one event — the session changed hands, so both the
		// old owner's claim and the old owner's geometry stop being true at
		// the same instant.
		//
		// A claim carrying no measurement is passed over rather than answered
		// with the default: a fresh window reclaiming a pane it has never
		// rendered has not measured itself yet, and that is not the no-client
		// state (session.NoClient). The size then stands until somebody
		// reports one.
		//
		// AD-1 is untouched by this. The channel is created at its final size
		// and this session's was created long before the claim arrived; a
		// resize applied to a live channel by a window-change is what AD-1
		// describes, not what it forbids.
		if reported := params.reportedSize(); reported.Valid() {
			h.machine.takeSize(sid, sess, reported)
		}

		_ = r.TryResult(req.ID, mustMarshal(attachResult{
			Resumed: !needsReset,
			Reset:   needsReset,
			From:    from,
		}))

		// Files (fm-w8): deliver the dirty paths the session's bindings
		// accumulated while no connection was attached. Runs after the attach
		// response — and after setSubscriber above, so the notifications
		// resolve to THIS connection (spec §5.2: the destination is resolved
		// at emit time, and a reconnect is exactly when the accumulation was
		// made).
		h.machine.flushFilesChanged(sid, wconn)

		// Uploads (upload design §5.3): the same reasoning one step
		// further. A transfer is bounded by its SESSION, so it runs on
		// through a WebSocket drop and can settle with nothing attached;
		// the outcome was retained then, and this is the attach it was
		// retained for.
		h.machine.flushUploadDone(sid, wconn)

		// Lifecycle (ADR-0024 decision 8): a reattached frontend must resume
		// the existing domain, so its current projection is re-emitted to
		// THIS connection — after the attach response and after
		// setSubscriber, like the files flush above. The publisher's
		// ReplayLane bypasses the change-dedupe on purpose: the renderer
		// needs the current state even when no transition happened since it
		// last saw this session.
		h.machine.replayLifecycleFacts(sid)
		h.machine.replayIntegration(sid)
		h.machine.replayPaneObservation(sid)

		sidBytes, _ := session.IDToBytes(sid)
		go h.machine.ringToConn(ctx, wconn, sidBytes, rx, from)
		return nil
	})
	if err != nil {
		answerOperationRefusal(wconn, req, err)
	}
}

// ackHandler answers "ack": ingress-critical ring trimming. It holds no
// capability — the ring is transport-owned state (migration map) — and runs
// inline on the read loop via the ImmediateSubmission.
//
// It is CONNECTION-SCOPED, and that is the whole of nocx-7ih2d: the ack
// cursor is what trim frees bytes against, so the answer to "may this ack
// move it" is "does this connection hold the session", which only the
// subscriber slot knows. The handler set is materialised per connection
// anyway (registration.go), so the connection is simply kept rather than
// looked up.
type ackHandler struct {
	machine sessionMachine
	conn    *wsConn
	log     log.Logger
}

// handleAck processes an ack notification (AD-9 trimming).
//
//	<-- {"jsonrpc":"2.0","method":"ack","params":{"sessionId":"...","offset":1234}}
//
// Offsets that run ahead of what was produced or go backwards are rejected
// with a warn — the server never trusts the client blindly.
func (h ackHandler) handleAck(req jsonrpcRequest) {
	var params ackParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		h.log.Warn("ack invalid params")
		return
	}

	sid := session.ID(params.SessionID)

	rx := h.machine.getRx(sid)
	if rx == nil {
		h.log.Warn("ack for unknown session", "session_id", string(sid))
		return
	}

	// Only the connection that HOLDS the session may free its bytes
	// (nocx-7ih2d). A displacement takes the claim and leaves the socket up,
	// so the loser goes on being read; an ack it had already queued would
	// otherwise trim the ring past the position the new subscriber's pump is
	// streaming from, and the ring cannot then serve that pump at all. The
	// subscriber slot is the existing owner of "whose session is this" and
	// the same comparison ringToConn makes on every pass; nothing new decides
	// it here.
	if sub, _ := rx.getSubscriber(); sub != h.conn {
		h.log.Warn("ack from a connection that does not hold the session",
			"session_id", string(sid), "conn", h.conn.id)
		return
	}

	if err := rx.ring.ack(params.Offset); err != nil {
		h.log.Warn("ack rejected", "session_id", string(sid), "error", err)
	}
}

// answerOperationRefusal answers a *capability.RefusedError (a gate refusal)
// with the saturation error; any other error is unexpected and answered as an
// internal error. A nil error is a no-op.
func answerOperationRefusal(r Responder, req jsonrpcRequest, err error) {
	var rej *capability.RefusedError
	if errors.As(err, &rej) {
		_ = r.TryError(req.ID, saturationRPCError(req.Method, &rej.Rejection))
		return
	}
	_ = r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
}

// sessionSpecs declares the session-plane control methods. open and attach
// need the connection as identity (subscriber registration); resize and close
// need connState (session ownership); ack is ingress-critical and never
// queues. The OpenOperation and the per-session factory are built here from
// the wired stores (composition root for this domain); both acquire the
// conflict gates (waiting) before the execution lane inside Run, so they
// register on per-operation queue submissions rather than the lane
// submission.
func (s *WSServer) sessionSpecs(lane control.Admission, sessionGate, configGate control.Admission) []methodSpec {
	openOp := capability.NewOpenOperation(configGate, sessionGate, lane, s.resolver, s.registry)
	sessionOps := capability.NewSessionOperations(sessionGate, lane, s.registry, s.profileUsage)
	// The whole-domain operation sessions.live reads the registry under, beside
	// the per-session factory the other methods use: the list is about every
	// session and no one of them.
	liveOp := capability.NewSessionOperation(sessionGate, lane, s.registry, s.profileUsage)
	immediate := control.ImmediateSubmission{}
	openSub := s.operationQueue("open")
	sessionSub := s.operationQueue("session")
	// resize and close share an ORDERED submission (control package): the
	// resize enqueue's arrival order is load-bearing for the coalescing
	// lane, and a close admitted after a resize on the same socket must
	// observe the resize's enqueue first — the same-socket ordering the
	// read loop used to provide by running everything inline. The ordered
	// worker preserves submission order; the bound refuses under a flood
	// with the saturation contract like any admission-backed method.
	ordered := control.NewOrderedSubmission("session-ops", 32)
	// The instance identity is read ONCE, here, where the registry is: a
	// handler judging a claim needs the value, never the registry.
	instance := s.instanceIdentity()
	return []methodSpec{
		reg(openSub, "open", params(validateOpenRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := openHandlers{op: openOp, sess: s, resolver: s.resolver, sshCfg: s.sshConfigResolver, launcher: s.remoteLauncher, installer: s.remoteInstaller, lifecycle: s.remoteLifecycle, panes: s.layoutReader(), log: s.log}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleOpen(ctx, w, r, state, req) }
		}),
		reg(ordered, "resize", params(validateResizeRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := sessionOpsHandlers{ops: sessionOps, r: r, instance: instance, machine: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleResize(ctx, state, req) }
		}),
		reg(ordered, "close", params(validateCloseRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := sessionOpsHandlers{ops: sessionOps, r: r, instance: instance, machine: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleClose(ctx, state, req) }
		}),
		reg(sessionSub, "attach", params(validateAttachRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := sessionOpsHandlers{ops: sessionOps, r: r, instance: instance, machine: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAttach(ctx, w, r, state, req) }
		}),
		// sessions.live is the session plane's read, and it rides the same
		// per-operation queue as attach: the two are one act for a fresh
		// client — ask what is alive, take one — and a list answered under a
		// different admission than the claim it feeds would be a list that can
		// be admitted while every claim it names is refused.
		regResponder(sessionSub, "sessions.live", noParams(), func(r Responder) handlerFunc {
			h := sessionsLiveHandlers{op: liveOp, rings: s, r: r, log: s.log}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSessionsLive(ctx, req) }
		}),
		// session.output is the session plane's OTHER read, and it rides the
		// same per-operation queue as attach and sessions.live for the same
		// reason: a fresh client asks what is alive, reads what one printed,
		// and takes it — three steps of one act, and a read admitted under a
		// different admission than the claim it feeds would answer while the
		// claim it is preparing was refused. Available only while a store is
		// wired: with no recording there is nothing to hand back, and the
		// caller's next move is to stop asking rather than to fix its
		// arguments (registration.go).
		whenAvailable(regResponder(sessionSub, "session.output", params(validateSessionOutputRaw), func(r Responder) handlerFunc {
			h := sessionOutputHandlers{ops: sessionOps, store: s.sessionRecorder, instance: instance, r: r, log: s.log}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleSessionOutput(ctx, req) }
		}), func() bool { return s.sessionRecorder != nil }, "method not found: session output store not wired"),
		reg(immediate, "ack", params(validateAckRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := ackHandler{machine: s, conn: w, log: s.log}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAck(req) }
		}),
	}
}

// ── session-plane ingress bounds ───────────────────────────────────────────
//
// open, resize, close, attach and ack take session ids — server-minted, so
// the 32-hex shape is the honest check (an id that is not one can never
// resolve, and the shape's owner is session.IDToBytes) — terminal sizes
// (uint16 by wire type, nonzero where a size of zero is meaningless) and
// byte offsets (uint64 by wire type). A rejected request is answered
// -32602 before the handler runs, so a bad session id never crosses into
// the capability or the ring.

// maxShellPinRunes bounds the open's shell pin. The product pins are
// bash|zsh|unknown|auto — 7 characters at most — and anything else is
// deliberately ignored with a warn (detection is the safe degrade, and the
// launcher refuses unmapped kinds if one slips past), so the validator
// bounds and does not refuse: refusing would turn the documented degrade
// into a failed open.
const maxShellPinRunes = 32

// validateOpenRaw is the registered validator for "open": cols and rows are
// required and nonzero (a zero-size terminal is meaningless), kind is the
// two-value enum the handler actually branches on, and the ssh branch needs
// a target. profileId and host are optional (a local open carries neither);
// when present they are held to the same bounds the seam methods apply, and
// host/user reach the ssh subprocess, so control characters are refused.
func validateOpenRaw(raw json.RawMessage) string {
	var p openParams
	if msg := decodeObject(raw, &p, "cols", "rows"); msg != "" {
		return msg
	}
	if p.Cols == 0 {
		return "cols is required"
	}
	if p.Rows == 0 {
		return "rows is required"
	}
	// kind is a closed set: absent or "local" opens a local PTY, "ssh" opens
	// an SSH channel. The handler only branches on "ssh", so an unrecognised
	// kind would silently open a local session — the wrong kind of session
	// for a caller that believes it is connecting to a host. Refuse it
	// rather than let a typo'd kind open the wrong one.
	//
	// "local" is in the set because callers SEND it: the closed set was
	// first written as ssh-or-absent, which the container gate refused at
	// session open for every local session in the product. A closed set has
	// to be read off what the product sends, never off what the handler
	// happens to branch on.
	if p.Kind != "" && p.Kind != "ssh" && p.Kind != "local" {
		return `kind must be "ssh", "local", or absent`
	}
	if p.Kind == "ssh" && p.ProfileID == "" && p.Host == "" {
		return "profileId or host is required for an ssh session"
	}
	if p.ProfileID != "" {
		if msg := validateStringBound("profileId", p.ProfileID, maxIDRunes); msg != "" {
			return msg
		}
	}
	if msg := validateStringBound("host", p.Host, maxHostRunes); msg != "" {
		return msg
	}
	if msg := validateStringBound("user", p.User, maxUserRunes); msg != "" {
		return msg
	}
	if utf8.RuneCountInString(p.Shell) > maxShellPinRunes {
		return fmt.Sprintf("shell exceeds %d characters", maxShellPinRunes)
	}
	// The parent edge (nocx-9hu9d) is optional — a root session carries none —
	// but a present one must be COMPLETE and well-shaped. Half an identity is
	// the bare parentId the full identity exists to replace, and it is refused
	// here rather than left for the registry, because a shape that cannot name
	// a session should never reach it. Both ids are server-minted 32-hex
	// (session.IDToBytes owns that shape for both), and the epoch is minted
	// from 1, so zero names no incarnation.
	// The pane is frontend-minted and UNTRUSTED (design §7), so its SHAPE is
	// checked here and its EXISTENCE by the chain walk in the handler. Both,
	// because they are different answers: before this, a malformed id went
	// straight to WorkspaceForPane, which can only report "no such pane", so
	// "you sent nonsense" and "that pane is gone" came back as one fact.
	// Absent is legitimate — a session attached to no recorded pane.
	if p.PaneID != "" {
		if msg := layoutID("paneId", p.PaneID); msg != "" {
			return msg
		}
	}
	if p.Parent != nil {
		if msg := validateSessionIDShape(p.Parent.SessionID); msg != "" {
			return "parent.sessionId " + msg
		}
		if msg := validateSessionIDShape(p.Parent.InstanceID); msg != "" {
			return "parent.instanceId " + msg
		}
		if p.Parent.SessionEpoch == 0 {
			return "parent.sessionEpoch is required and starts at 1"
		}
	}
	return ""
}

// validateResizeRaw is the registered validator for "resize": the sessionId
// must be a real server-minted id, and cols/rows nonzero.
func validateResizeRaw(raw json.RawMessage) string {
	var p resizeParams
	if msg := decodeObject(raw, &p, "sessionId", "cols", "rows"); msg != "" {
		return msg
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	if p.Cols == 0 {
		return "cols is required"
	}
	if p.Rows == 0 {
		return "rows is required"
	}
	return ""
}

// validateCloseRaw is the registered validator for "close": the sessionId
// must be a real server-minted id.
func validateCloseRaw(raw json.RawMessage) string {
	var p closeParams
	if msg := decodeObject(raw, &p, "sessionId"); msg != "" {
		return msg
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	return ""
}

// validateAttachRaw is the registered validator for "attach": the sessionId
// must be a real server-minted id. offset is uint64 by wire type and 0 (from
// the start of the ring) is ordinary — the handler checks the offset against
// what the ring actually wrote.
//
// The claimed identity is OPTIONAL and, when present, must be WELL SHAPED
// (nocx-oevq4). Both halves matter and they are different answers: "you sent
// nonsense" is refused here by shape, and "that identity names another
// backend" is a fact about a claim, decided by the handler and told apart by
// data.reason. Before the open params drew that line, a malformed pane id went
// straight to the store, which could only report "no such pane".
func validateAttachRaw(raw json.RawMessage) string {
	var p attachParams
	if msg := decodeObject(raw, &p, "sessionId"); msg != "" {
		return msg
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	if p.InstanceID != "" {
		if msg := validateSessionIDShape(p.InstanceID); msg != "" {
			return "instanceId " + msg
		}
	}
	if p.SessionEpoch != nil && *p.SessionEpoch == 0 {
		return "sessionEpoch starts at 1"
	}
	return ""
}

// validateAckRaw is the registered validator for the "ack" notification:
// the sessionId must be a real server-minted id. ack is ingress-critical
// (registration.go), so this runs inline on the read loop and is deliberately
// trivial: decode and two string checks, no allocation beyond the struct.
func validateAckRaw(raw json.RawMessage) string {
	var p ackParams
	if msg := decodeObject(raw, &p, "sessionId"); msg != "" {
		return msg
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	return ""
}
