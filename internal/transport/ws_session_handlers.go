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
	removeRx(sid session.ID)
	laneFor(sid session.ID, sess session.Session) *sessionLane
	closeLane(sid session.ID)
	closeSession(sid session.ID, sess session.Session)
	ringToConn(ctx context.Context, wconn *wsConn, sidBytes [16]byte, ring *outputRing, startOffset uint64)
	flushFilesChanged(sid session.ID, wconn Responder)
	notifyInputStalled(sid session.ID)
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
}

// openMachine is the transport-owned machinery handleOpen needs after the
// dial: rings, the output pump, the exit monitor, stored-forward replay and
// the discovery hooks. Same narrow-surface rule as sessionMachine.
type openMachine interface {
	getOrCreateRx(sid session.ID) *sessionRx
	removeRx(sid session.ID)
	pumpToRing(ctx context.Context, sess session.Session, ring *outputRing)
	monitorExit(rx *sessionRx, sess session.Session)
	ringToConn(ctx context.Context, wconn *wsConn, sidBytes [16]byte, ring *outputRing, startOffset uint64)
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

// openHandlers answers "open". It holds the OpenOperation (config, session
// gates — the dial runs inside the callback) and the seams the dial needs.
// It needs the connection as identity, not just as a writer: it registers
// the connection as the session's subscriber, so the handler receives the
// *wsConn per call.
type openHandlers struct {
	op       capability.OpenOperation
	sess     openMachine
	resolver *resolverHolder // profile resolver, readable post-construction
	sshCfg   ssh.ConfigResolver
	launcher ssh.RemoteLauncher
	// lifecycle is the authenticated-channel seam (ADR-0024): the dial
	// hands it to the far side so the shell can hand its lifecycle back
	// over a channel that is not the terminal. An explicit seam, not the
	// whole server.
	lifecycle ssh.RemoteLifecycle
	log       log.Logger
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

	cfg := session.Config{
		Kind:   session.KindLocal,
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
	}
	// ProfileID is deliberately NOT set here. It is recorded below, only once
	// the resolver has accepted it, because a local PTY has no profile and
	// setting it up front lets a renderer attach any profile id to a local
	// session it opens. sessions.status would then report that profile live and
	// the connection list would draw a row as connected with nothing behind it
	// (nocx-uxs5.4).

	var sess session.Session
	opened := false
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.OpenService) error {
		// SSH session — when kind="ssh", open a remote channel instead of
		// local PTY. The resolve and the dial both run inside the callback:
		// the operation holds [config, session] for the whole open
		// (conservative grain, open.go).
		if params.Kind == "ssh" {
			var host string
			var remote *ssh.ConnectConfig

			if params.ProfileID != "" {
				// Profile-based resolution: look up the stored profile, resolve
				// credentials and jump hosts through the profile resolver.
				if _, ok := h.resolver.get(); !ok {
					resp := newJSONRPCError(req.ID, -32603, "SSH sessions not available (no profile resolver wired)")
					_ = respond(r, resp)
					return nil
				}

				var err error
				host, remote, err = svc.Resolve(params.ProfileID)
				if err != nil {
					h.log.Error("profile resolve failed", "profileId", params.ProfileID, "error", err)
					// Resolving reads the stored password, so a sealed vault surfaces
					// here — the renderer needs the reason to offer an unlock.
					_ = r.TryError(req.ID, rpcErrorFor(-32603, "", err))
					return nil
				}

				remote.Cols = params.Cols
				remote.Rows = params.Rows
				remote.XPixel = params.XPixel
				remote.YPixel = params.YPixel
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
					User:            user,
					Port:            port,
					KeyFile:         keyFile,
					Cols:            params.Cols,
					Rows:            params.Rows,
					RemoteLauncher:  h.launcher,
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

		var oerr error
		sess, oerr = svc.Open(ctx, cfg)
		if oerr != nil {
			return oerr
		}
		opened = true
		return nil
	})
	if err != nil {
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
	result := map[string]string{
		"sessionId":   string(sess.ID()),
		"cwd":         sess.Cwd(),
		"desiredMode": desiredModeForAck(cfg.Remote),
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
	rx.setSubscriber(wconn, state)
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
	// ends StartOutput and lets the pump return.
	go h.sess.pumpToRing(context.Background(), sess, rx.ring)

	// Start exactly one monitorExit goroutine per session (DEFECT 2).
	rx.monitorOnce.Do(func() {
		go h.sess.monitorExit(rx, sess)
	})

	sidBytes, _ := session.IDToBytes(sess.ID())
	go h.sess.ringToConn(ctx, wconn, sidBytes, rx.ring, 0)
}

// sessionOpsHandlers answers resize, close and attach: the per-session
// operations (SessionOperation via ForSession) plus the transport lanes. It
// needs the connection's connState (session ownership checks) per call.
type sessionOpsHandlers struct {
	ops     *capability.SessionOperations // nil → session store not wired
	r       Responder
	machine sessionMachine
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
			cols:   params.Cols,
			rows:   params.Rows,
			xpixel: params.XPixel,
			ypixel: params.YPixel,
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

// handleAttach reattaches a connection to a session's output ring at the
// given byte offset (AD-9 reconnect).
//
//	--> {"jsonrpc":"2.0","id":N,"method":"attach","params":{"sessionId":"...","offset":1234}}
//
// Result when offset is still in the ring:
//
//	<-- {"jsonrpc":"2.0","id":N,"result":{"resumed":true,"from":1234}}
//
// Result when offset is too old (ring has advanced past it):
//
//	<-- {"jsonrpc":"2.0","id":N,"result":{"reset":true,"from":5678}}
//
// Unknown sessionId → JSON-RPC error.
// Offset ahead of written → JSON-RPC error (DEFECT 4).
// Duplicate attach on the same connection → JSON-RPC error (DEFECT 3).
func (h sessionOpsHandlers) handleAttach(ctx context.Context, wconn *wsConn, r Responder, state *connState, req jsonrpcRequest) {
	var params attachParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: sessionId and offset required")
		_ = respond(r, resp)
		return
	}

	sid := session.ID(params.SessionID)

	op, err := h.ops.ForSession(sid)
	if err != nil {
		resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
		_ = respond(r, resp)
		return
	}
	err = op.Run(ctx, func(ctx context.Context, svc capability.SessionService) error {
		sess, gerr := svc.Get(sid)
		if gerr != nil {
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
			_ = respond(r, resp)
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
			resp := newJSONRPCError(req.ID, -32602, "Invalid params: unknown sessionId")
			_ = respond(r, resp)
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
		rx.setSubscriber(wconn, state)

		if needsReset {
			respJSON, _ := json.Marshal(map[string]any{"reset": true, "from": from})
			resp := newJSONRPCResult(req.ID, respJSON)
			_ = respond(r, resp)
		} else {
			respJSON, _ := json.Marshal(map[string]any{"resumed": true, "from": from})
			resp := newJSONRPCResult(req.ID, respJSON)
			_ = respond(r, resp)
		}

		// Files (fm-w8): deliver the dirty paths the session's bindings
		// accumulated while no connection was attached. Runs after the attach
		// response — and after setSubscriber above, so the notifications
		// resolve to THIS connection (spec §5.2: the destination is resolved
		// at emit time, and a reconnect is exactly when the accumulation was
		// made).
		h.machine.flushFilesChanged(sid, wconn)

		// Lifecycle (ADR-0024 decision 8): a reattached frontend must resume
		// the existing domain, so its current projection is re-emitted to
		// THIS connection — after the attach response and after
		// setSubscriber, like the files flush above. The publisher's
		// ReplayLane bypasses the change-dedupe on purpose: the renderer
		// needs the current state even when no transition happened since it
		// last saw this session.
		h.machine.replayLifecycleFacts(sid)
		h.machine.replayIntegration(sid)

		sidBytes, _ := session.IDToBytes(sid)
		go h.machine.ringToConn(ctx, wconn, sidBytes, rx.ring, from)
		return nil
	})
	if err != nil {
		answerOperationRefusal(wconn, req, err)
	}
}

// ackHandler answers "ack": ingress-critical ring trimming. It holds no
// capability — the ring is transport-owned state (migration map) — and runs
// inline on the read loop via the ImmediateSubmission.
type ackHandler struct {
	machine sessionMachine
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
	return []methodSpec{
		reg(openSub, "open", params(validateOpenRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := openHandlers{op: openOp, sess: s, resolver: s.resolver, sshCfg: s.sshConfigResolver, launcher: s.remoteLauncher, lifecycle: s.remoteLifecycle, log: s.log}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleOpen(ctx, w, r, state, req) }
		}),
		reg(ordered, "resize", params(validateResizeRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := sessionOpsHandlers{ops: sessionOps, r: r, machine: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleResize(ctx, state, req) }
		}),
		reg(ordered, "close", params(validateCloseRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := sessionOpsHandlers{ops: sessionOps, r: r, machine: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleClose(ctx, state, req) }
		}),
		reg(sessionSub, "attach", params(validateAttachRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := sessionOpsHandlers{ops: sessionOps, r: r, machine: s}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleAttach(ctx, w, r, state, req) }
		}),
		reg(immediate, "ack", params(validateAckRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := ackHandler{machine: s, log: s.log}
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
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
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
	return ""
}

// validateResizeRaw is the registered validator for "resize": the sessionId
// must be a real server-minted id, and cols/rows nonzero.
func validateResizeRaw(raw json.RawMessage) string {
	var p resizeParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
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
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
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
func validateAttachRaw(raw json.RawMessage) string {
	var p attachParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	return ""
}

// validateAckRaw is the registered validator for the "ack" notification:
// the sessionId must be a real server-minted id. ack is ingress-critical
// (registration.go), so this runs inline on the read loop and is deliberately
// trivial: decode and two string checks, no allocation beyond the struct.
func validateAckRaw(raw json.RawMessage) string {
	var p ackParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.SessionID == "" {
		return "sessionId is required"
	}
	if msg := validateSessionIDShape(p.SessionID); msg != "" {
		return "sessionId " + msg
	}
	return ""
}
