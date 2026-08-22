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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
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
	removeRx(sid session.ID) *sessionRx
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
	panes    paneWorkspaces
	settings capability.SettingsService
	log      log.Logger
}

// paneWorkspaces is the narrow layout seam needed by open: resolve pane
// provenance, reject duplicate sandbox authority, and durably record the
// realized grant before native enforcement starts.
type paneWorkspaces interface {
	WorkspaceForPane(ctx context.Context, paneID string) (string, error)
	SandboxGrantExists(ctx context.Context, paneID string) (bool, error)
	InsertSandboxGrant(ctx context.Context, grant content.SandboxGrant) error
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
	// Parent is the edge the backend RECORDED, echoed back so the renderer
	// stores what was admitted rather than what it asked for (nocx-9hu9d).
	// Null for a root session — and null rather than absent, because the
	// schema requires the key: an omitempty here would drop it for every root
	// session and leave "no parent" indistinguishable from "an old backend".
	Parent  *openParentResult    `json:"parent"`
	Sandbox *sandbox.SessionInfo `json:"sandbox,omitempty"`
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
	var statusErr *sandbox.StatusError
	if errors.As(err, &statusErr) {
		h.log.Warn("sandbox backend unavailable",
			"backend", statusErr.Status.Backend,
			"reason", statusErr.Status.Reason,
			"abi", statusErr.Status.ABI,
		)
		_ = r.TryError(req.ID, RPCError{
			Code: -32006, Message: statusErr.Status.Reason,
			Data: map[string]any{"reason": statusErr.Status.Reason},
		})
		return
	}
	var setupErr *sandbox.SetupError
	if errors.As(err, &setupErr) {
		// The reason is a fixed token the sandbox package chose, never the
		// error text: the detail behind it names paths, and neither the wire
		// nor the log may carry those. A setup failure with no typed reason
		// stays generic.
		reason := setupErr.Reason
		if reason == "" {
			reason = "setup-failed"
		}
		h.log.Error("sandbox setup failed", "reason", reason)
		_ = r.TryError(req.ID, RPCError{
			Code: -32007, Message: "sandbox setup failed",
			Data: map[string]any{"reason": reason},
		})
		return
	}
	if errors.Is(err, sandbox.ErrInvalidPermissions) {
		h.log.Warn("sandbox request paths became invalid before launch")
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
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

func canonicalizeOpenCwd(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("cwd must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("cwd must be a directory")
	}
	return canonical, nil
}

// handleOpen creates a new session and output ring.
//
// Per AD-7: the server assigns the authoritative session-id. The JSON-RPC
// request id serves as the correlation-id — we do NOT add a second
// correlationId field, because two correlation identifiers for one exchange
// is redundant state with two owners.
func (h openHandlers) handleOpen(ctx context.Context, wconn *wsConn, r Responder, state *connState, req jsonrpcRequest) {
	params, err := decodeOpenParams(req.Params)
	if err != nil || params.Cols == 0 || params.Rows == 0 {
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

	var sandboxReq *sandbox.Request
	if params.Sandbox != nil {
		if params.Kind == "ssh" || params.ProfileID != "" || params.Host != "" {
			_ = r.TryError(req.ID, RPCError{
				Code: -32602, Message: "Invalid params: sandbox is only valid for local sessions",
			})
			return
		}
		if h.settings == nil {
			_ = r.TryError(req.ID, RPCError{
				Code: -32005, Message: "Filesystem sandbox is disabled",
				Data: map[string]any{"reason": "feature-disabled"},
			})
			return
		}
		snap, snapErr := h.settings.GetSnapshot()
		if snapErr != nil {
			h.log.Error("sandbox settings snapshot failed", "reason", "setup-failed")
			_ = r.TryError(req.ID, RPCError{
				Code: -32007, Message: "sandbox setup failed",
				Data: map[string]any{"reason": "setup-failed"},
			})
			return
		}
		enabled, _ := snap.Values[settings.SandboxEnabled.Key()].(bool)
		if !enabled {
			_ = r.TryError(req.ID, RPCError{
				Code: -32005, Message: "Filesystem sandbox is disabled",
				Data: map[string]any{"reason": "feature-disabled"},
			})
			return
		}
		if snap.Revision != params.Sandbox.SettingsRevision {
			_ = r.TryError(req.ID, RPCError{
				Code: -32602, Message: "Invalid params: settings revision mismatch",
			})
			return
		}
		globalWritable, globalReadOnly, baselineErr := sandboxBaselines(snap)
		if baselineErr != nil {
			h.log.Error("sandbox setup failed", "reason", "setup-failed")
			_ = r.TryError(req.ID, RPCError{
				Code: -32007, Message: "sandbox setup failed",
				Data: map[string]any{"reason": "setup-failed"},
			})
			return
		}
		workspacePath, workspaceErr := sandbox.CanonicalizeWorkspace(params.Sandbox.Workspace)
		if workspaceErr != nil {
			_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
			return
		}
		if params.PaneID == "" || h.panes == nil {
			_ = r.TryError(req.ID, RPCError{
				Code: -32602, Message: "Invalid params: sandbox requires an open pane",
			})
			return
		}
		granted, grantErr := h.panes.SandboxGrantExists(ctx, params.PaneID)
		if grantErr != nil {
			_ = r.TryError(req.ID, RPCError{
				Code: -32602, Message: "Invalid params: sandbox requires an open pane",
			})
			return
		}
		if granted {
			_ = r.TryError(req.ID, RPCError{
				Code: -32602, Message: "Invalid params: sandbox pane already granted",
			})
			return
		}
		sandboxReq = &sandbox.Request{
			Workspace:      workspacePath,
			GlobalWritable: globalWritable,
			GlobalReadOnly: globalReadOnly,
			AddWritable:    params.Sandbox.AddWritable,
			RemoveWritable: params.Sandbox.RemoveWritable,
			AddReadOnly:    params.Sandbox.AddReadOnly,
			RemoveReadOnly: params.Sandbox.RemoveReadOnly,
		}
	}

	if params.Cwd != "" && (params.Kind == "ssh" || params.Sandbox != nil) {
		_ = r.TryError(req.ID, RPCError{
			Code: -32602, Message: "Invalid params: cwd is only valid for ordinary local sessions",
		})
		return
	}
	localCwd, cwdErr := canonicalizeOpenCwd(params.Cwd)
	if cwdErr != nil {
		_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: cwd"})
		return
	}

	cfg := session.Config{
		Kind:   session.KindLocal,
		Cols:   params.Cols,
		Cwd:    localCwd,
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
	if sandboxReq != nil {
		cfg.Cwd = sandboxReq.Workspace
		cfg.Sandbox = sandboxReq
		cfg.SandboxPrepared = func(prepared *sandbox.PreparedCommand) error {
			info := sandbox.SessionInfo{
				Backend:         prepared.Backend,
				Workspace:       prepared.Policy.Workspace,
				WritableRoots:   append([]string(nil), prepared.Policy.WritableRoots...),
				ReadOnlyRoots:   append([]string(nil), prepared.Policy.ReadOnlyRoots...),
				HomeProjections: append([]sandbox.HomeProjection(nil), prepared.Policy.HomeProjections...),
			}
			payload, marshalErr := json.Marshal(info)
			if marshalErr != nil {
				return marshalErr
			}
			return h.panes.InsertSandboxGrant(ctx, content.SandboxGrant{
				PaneID: params.PaneID, Version: 1, IssuedAt: time.Now().Unix(),
				Workspace: sandboxReq.Workspace, Payload: string(payload),
			})
		}
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
	err = h.op.Prepare(ctx, func(ctx context.Context, svc capability.OpenService) error {
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

				var resolveErr error
				host, remote, resolveErr = svc.Resolve(params.ProfileID)
				if resolveErr != nil {
					h.log.Error("profile resolve failed", "profileId", params.ProfileID, "error", resolveErr)
					// Resolving reads the stored password, so a sealed vault surfaces
					// here — the renderer needs the reason to offer an unlock.
					_ = r.TryError(req.ID, rpcErrorFor(-32603, "", resolveErr))
					answered = true
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
					answered = true
					return nil
				}

				resolved, resolveErr := h.sshCfg.ResolveConfig(ctx, params.Host)
				if resolveErr != nil {
					h.log.Warn("SSH config resolution degraded for direct host", "host", params.Host, "error", resolveErr)
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
		Parent:       parentResultFor(sess),
		Sandbox:      sess.SandboxInfo(),
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
			h := openHandlers{op: openOp, sess: s, resolver: s.resolver, sshCfg: s.sshConfigResolver, launcher: s.remoteLauncher, lifecycle: s.remoteLifecycle, panes: s.layoutReader(), settings: s.settings, log: s.log}
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
	if len(raw) == 0 {
		return "params are required"
	}
	p, err := decodeOpenParams(raw)
	if err != nil {
		return "params must be a strict JSON object: " + err.Error()
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

const maxSandboxPaths = 32

// decodeOpenParams rejects unknown/duplicate members and trailing JSON. A
// sandbox opt-in cannot be allowed to disappear into the zero value of a
// permissive decoder; main's paneId and parent remain part of the same strict
// object.
func decodeOpenParams(data []byte) (openParams, error) {
	var p openParams
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return p, fmt.Errorf("open params: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return p, errors.New("open params: must be an object")
	}
	seen := make(map[string]bool, 13)
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return p, fmt.Errorf("open params: %w", keyErr)
		}
		key, ok := keyTok.(string)
		if !ok {
			return p, errors.New("open params: non-string member name")
		}
		if seen[key] {
			return p, fmt.Errorf("open params: duplicate member %q", key)
		}
		seen[key] = true
		switch key {
		case "cols":
			err = dec.Decode(&p.Cols)
		case "rows":
			err = dec.Decode(&p.Rows)
		case "xpixel":
			err = dec.Decode(&p.XPixel)
		case "ypixel":
			err = dec.Decode(&p.YPixel)
		case "cwd":
			p.Cwd, err = decodeStringField(dec)
		case "kind":
			p.Kind, err = decodeStringField(dec)
		case "profileId":
			p.ProfileID, err = decodeStringField(dec)
		case "host":
			p.Host, err = decodeStringField(dec)
		case "user":
			p.User, err = decodeStringField(dec)
		case "shell":
			p.Shell, err = decodeStringField(dec)
		case "paneId":
			p.PaneID, err = decodeStringField(dec)
		case "parent":
			p.Parent, err = decodeOpenParent(dec)
		case "sandbox":
			p.Sandbox, err = decodeOpenSandbox(dec)
		default:
			return p, fmt.Errorf("open params: unknown member %q", key)
		}
		if err != nil {
			return p, fmt.Errorf("open params %s: %w", key, err)
		}
	}
	closeTok, err := dec.Token()
	if err != nil {
		return p, fmt.Errorf("open params: closing object: %w", err)
	}
	if delim, ok := closeTok.(json.Delim); !ok || delim != '}' {
		return p, errors.New("open params: malformed closing object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return p, errors.New("open params: trailing data")
	}
	return p, nil
}

func decodeOpenParent(dec *json.Decoder) (*openParentParams, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, errors.New("null is not a valid parent")
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("must be an object")
	}
	var p openParentParams
	seen := make(map[string]bool, 3)
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return nil, keyErr
		}
		key, ok := keyTok.(string)
		if !ok || seen[key] {
			return nil, errors.New("parent has an invalid or duplicate member")
		}
		seen[key] = true
		switch key {
		case "sessionId":
			p.SessionID, err = decodeStringField(dec)
		case "instanceId":
			p.InstanceID, err = decodeStringField(dec)
		case "sessionEpoch":
			err = dec.Decode(&p.SessionEpoch)
		default:
			return nil, fmt.Errorf("unknown member %q", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	for _, key := range []string{"sessionId", "instanceId", "sessionEpoch"} {
		if !seen[key] {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	return &p, nil
}

func decodeOpenSandbox(dec *json.Decoder) (*openSandboxParams, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	if tok == nil {
		return nil, errors.New("sandbox: null is not a valid opt-in")
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("sandbox: must be an object")
	}
	var sb openSandboxParams
	seen := make(map[string]bool, 6)
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return nil, fmt.Errorf("sandbox: %w", keyErr)
		}
		key, ok := keyTok.(string)
		if !ok || seen[key] {
			return nil, errors.New("sandbox: invalid or duplicate member")
		}
		seen[key] = true
		switch key {
		case "workspace":
			sb.Workspace, err = decodeStringField(dec)
		case "settingsRevision":
			var n *int
			err = dec.Decode(&n)
			if err == nil {
				if n == nil || *n < 0 {
					err = errors.New("must be a non-negative integer")
				} else {
					sb.SettingsRevision = *n
				}
			}
		case "addWritable":
			sb.AddWritable, err = decodeStringArray(dec)
		case "removeWritable":
			sb.RemoveWritable, err = decodeStringArray(dec)
		case "addReadOnly":
			sb.AddReadOnly, err = decodeStringArray(dec)
		case "removeReadOnly":
			sb.RemoveReadOnly, err = decodeStringArray(dec)
		default:
			return nil, fmt.Errorf("sandbox: unknown member %q", key)
		}
		if err != nil {
			return nil, fmt.Errorf("sandbox %s: %w", key, err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("sandbox: closing object: %w", err)
	}
	if !seen["workspace"] || !seen["settingsRevision"] {
		return nil, errors.New("sandbox: workspace and settingsRevision are required")
	}
	return &sb, nil
}

func decodeStringArray(dec *json.Decoder) ([]string, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return nil, errors.New("must be a non-null array of strings")
	}
	out := make([]string, 0)
	seen := make(map[string]bool)
	for dec.More() {
		value, valueErr := decodeStringField(dec)
		if valueErr != nil {
			return nil, valueErr
		}
		if len(out) == maxSandboxPaths {
			return nil, fmt.Errorf("at most %d entries", maxSandboxPaths)
		}
		if seen[value] {
			return nil, errors.New("duplicate entry")
		}
		seen[value] = true
		out = append(out, value)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeStringField(dec *json.Decoder) (string, error) {
	var value *string
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	if value == nil {
		return "", errors.New("expected a string")
	}
	return *value, nil
}

func sandboxBaselines(snap settings.SettingsSnapshot) (writable, readOnly []string, err error) {
	writable, err = sandboxPathList(snap, settings.SandboxAllowedWritablePaths.Key())
	if err != nil {
		return nil, nil, err
	}
	readOnly, err = sandboxPathList(snap, settings.SandboxAllowedReadOnlyPaths.Key())
	if err != nil {
		return nil, nil, err
	}
	return writable, readOnly, nil
}

func sandboxPathList(snap settings.SettingsSnapshot, key string) ([]string, error) {
	raw, ok := snap.Values[key]
	if !ok {
		return nil, fmt.Errorf("%s missing from settings snapshot", key)
	}
	paths, ok := raw.([]string)
	if !ok || len(paths) > maxSandboxPaths {
		return nil, fmt.Errorf("%s is not a valid bounded path list", key)
	}
	return append([]string(nil), paths...), nil
}
