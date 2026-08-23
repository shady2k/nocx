package transport

// shell.integrate — the in-band bootstrap plan for the shell at a trusted
// prompt (spec §4.4, nocx-ynsx). The renderer alone may call it, gated on
// PROMPT_READY && trusted && owned: consent changes authorisation, not the
// identity of the foreground process, so the backend never offers this on
// its own. This file serves the plan from the shellintegration seam.

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/transport/control"
)

// InBandBootstrapper builds the in-band integration plan for a live session.
// *shellintegration.Impl satisfies it with identical signatures — no
// adapter. When not wired, shell.integrate returns a JSON-RPC error; the
// transport never constructs the capability itself.
//
// ch is the authenticated-channel configuration (lane, domain, epoch and the
// loopback port the kernel's transport listens on), minted by whoever set up
// the domain; nil builds a capability-free plan (conventional shell). The
// capability is never an argument and never crosses this seam's results.
type InBandBootstrapper interface {
	InBandBootstrap(sessionID string, ch *shellintegration.ChannelConfig) (shellintegration.InBandPlan, error)
}

// WithInBandBootstrapper attaches the in-band bootstrap builder behind the
// shell.integrate JSON-RPC method (nocx-ynsx). The single implementation is
// *shellintegration.Impl, wired at the composition root so the payload the
// renderer streams carries the session id the registry minted (AD-7).
func WithInBandBootstrapper(b InBandBootstrapper) WSServerOption {
	return func(s *WSServer) { s.inBand = b }
}

// shellIntegrateResult is the result of shell.integrate, matching
// contracts/shell.integrate.schema.json exactly. The renderer types the
// wrapper at the trusted prompt; the backend writes the capability line and
// the payload into the pty once READY arrives (the renderer never holds the
// capability — ADR-0024 decision 7). The terminator is sent alone to cancel.
//
// The capability deliberately has NO representation here: the result is
// built field-by-field from the plan and InBandPlan.Capability is never
// copied, so the per-epoch bearer cannot cross the WebSocket. The contract
// test in ws_shell_test.go proves it.
type shellIntegrateResult struct {
	Wrapper    string `json:"wrapper"`
	Payload    string `json:"payload"`
	Terminator string `json:"terminator"`
}

// shellIntegrateResultFromPlan copies exactly the three renderer-visible
// fields. InBandPlan.Capability is deliberately NOT copied: the per-epoch
// bearer is the backend's to write into the pty after READY, and this copy
// is the renderer boundary (ADR-0024 decision 7). The contract test in
// ws_shell_test.go proves a capability set on the plan never reaches the
// marshaled result.
func shellIntegrateResultFromPlan(plan shellintegration.InBandPlan) shellIntegrateResult {
	return shellIntegrateResult{
		Wrapper:    plan.Wrapper,
		Payload:    plan.Payload,
		Terminator: plan.Terminator,
	}
}

// sessionShellHandlers answers shell.complete and shell.integrate. Completion
// uses a staged SessionTargetOperation: copy immutable route facts while the
// session gate is held, then release it before remote I/O while retaining the
// ordinary execution lane. Integration remains a regular SessionOperation.
// The handler holds the operation factory, the Responder and its seams —
// never the *WSServer.
type sessionShellHandlers struct {
	ops    *capability.SessionOperations // session gate; nil → session store not wired
	r      Responder
	local  completion.Completer // shell.complete for KindLocal sessions
	remote RemoteCompleter      // shell.complete for KindRemote sessions
	inBand InBandBootstrapper   // shell.integrate plan builder
	names  CommandNamesResolver // shell.commandNames shared scan
}

// handleIntegrate serves the shell.integrate method.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.integrate","params":{"sessionId":"0123456789abcdef0123456789abcdef"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"wrapper":"saved=$(stty -g); …","payload":"# nocx in-band integration — dispatcher…","terminator":"NOCX_IB_EOF"}}
//
// The session id is server-authoritative (AD-7): the session must be live in
// the registry or the plan is refused, so a stale or forged id can never
// anchor NOCX_SESSION_ID in a payload typed into a shell.
//
// The channel configuration is not available at this layer today (the domain
// minting and the transport listener are the composition root's wiring), so
// the plan is built capability-free — a conventional shell. The wiring that
// mints the domain passes the config here and writes the capability line
// into the pty after READY; neither step ever routes the capability through
// this result.
func (h sessionShellHandlers) handleIntegrate(ctx context.Context, req jsonrpcRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId required"})
		return
	}
	sid := session.ID(params.SessionID)
	op, err := h.ops.ForSession(sid)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
		return
	}
	err = op.Run(ctx, func(ctx context.Context, svc capability.SessionService) error {
		if _, getErr := svc.Get(sid); getErr != nil {
			// The session closed between ForSession and the run: same
			// refusal as a session the registry never held.
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sessionId"})
			return nil
		}
		if h.inBand == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "shell.integrate: in-band bootstrap not available"})
			return nil
		}
		plan, planErr := h.inBand.InBandBootstrap(params.SessionID, nil)
		if planErr != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "shell.integrate: ", planErr))
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(shellIntegrateResultFromPlan(plan)))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// shellSpecs declares the shell-plane control methods (migration map, "The
// rest"): shell.complete snapshots its session target before remote work;
// shell.integrate runs under the regular per-session SessionOperation. Both
// register on the operation queue. The launcher / footprint methods are seam
// handlers on the ordinary lane under no operation, holding only the seams
// the migration map names. The SessionOperations factory is built here from
// the wired stores and shared across the shell methods.
func (s *WSServer) shellSpecs(lane control.Admission, sessionGate control.Admission) []methodSpec {
	sessionOps := capability.NewSessionOperations(sessionGate, lane, s.registry, s.profileUsage)
	shellSub := s.operationQueue("shell")
	return []methodSpec{
		regResponder(shellSub, "shell.complete", params(validateShellCompleteRaw), func(r Responder) handlerFunc {
			h := sessionShellHandlers{ops: sessionOps, r: r, local: s.localCompleter, remote: s.sshCompleter}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleComplete(ctx, req) }
		}),
		// shell.commandNames rides the same staged session-target operation
		// as shell.complete, for the same reason: the scan behind it may
		// take its whole deadline, and the session gate must not be held
		// across it.
		regResponder(shellSub, "shell.commandNames", params(validateShellCommandNamesRaw), func(r Responder) handlerFunc {
			h := sessionShellHandlers{ops: sessionOps, r: r, names: s.commandNames}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCommandNames(ctx, req) }
		}),
		regResponder(shellSub, "shell.integrate", params(validateShellIntegrateRaw), func(r Responder) handlerFunc {
			h := sessionShellHandlers{ops: sessionOps, r: r, inBand: s.inBand}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleIntegrate(ctx, req) }
		}),
		// shell.launcherCommand and shell.environmentObserved are NOT
		// registered: they were the P7 stream-and-passport surface, gated on
		// the marker latch ADR-0024 forbids, and the branch deleted their
		// handler, contracts and generated types (nocx-292k). The footprint
		// methods below outlive them — they read the fact store, which stays.
		regResponder(s.lane, "shell.footprint.status", noParams(), func(r Responder) handlerFunc {
			h := footprintHandlers{
				r:        r,
				facts:    s.installedFacts,
				resolver: s.resolver,
				sshCfg:   s.sshConfigResolver,
				profiles: s.profiles,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleFootprintStatus(ctx, req) }
		}),
		regResponder(s.lane, "shell.footprint.uninstall", params(validateFootprintUninstallRaw), func(r Responder) handlerFunc {
			h := footprintHandlers{
				r:           r,
				uninstaller: s.remoteUninstaller,
				resolver:    s.resolver,
				log:         s.log,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleFootprintUninstall(ctx, req) }
		}),
	}
}

// ── shell ingress bounds ───────────────────────────────────────────────────

// validateShellCompleteRaw is the registered validator for shell.complete:
// sessionId is server-minted (32-hex shape), cwd is a renderer-supplied
// path held to the agent surface's path bound, and line is the line being
// completed — bounded at the floor's wire-cost ceiling because the product
// has no tighter one (a shell line is bounded by the session's own input,
// not by this method). pos is the caret offset into line, and the completion
// contract (internal/completion.Request) reads the word at pos and treats a
// pos outside [0, len(line)] as completing nothing — the same byte
// semantics as the contract, so it is refused here. limit is left to the
// handler, which clamps it to 1..200.
func validateShellCompleteRaw(raw json.RawMessage) string {
	var p shellCompleteParams
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
	if p.Cwd == "" {
		return "cwd is required"
	}
	if msg := validateStringBound("cwd", p.Cwd, maxCwdRunes); msg != "" {
		return msg
	}
	if p.Line == "" {
		return "line is required"
	}
	if utf8.RuneCountInString(p.Line) > maxGenericStringRunes {
		return fmt.Sprintf("line exceeds %d characters", maxGenericStringRunes)
	}
	if p.Pos < 0 || p.Pos > len(p.Line) {
		return "pos must be an offset within line"
	}
	return ""
}

// validateShellIntegrateRaw is the registered validator for shell.integrate:
// the sessionId must be a real server-minted id — the plan's payload anchors
// NOCX_SESSION_ID in a shell, and the handler already refuses ids that are
// not live in the registry (AD-7); the shape check refuses ones that cannot
// be, before the capability is touched.
func validateShellIntegrateRaw(raw json.RawMessage) string {
	var p struct {
		SessionID string `json:"sessionId"`
	}
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

// validateFootprintUninstallRaw is the registered validator for
// shell.footprint.uninstall: the profileId names the saved connection whose
// credentials the dial will use — the same id shape every profile-taking
// method checks.
func validateFootprintUninstallRaw(raw json.RawMessage) string {
	var p struct {
		ProfileID string `json:"profileId"`
	}
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	return validateProfileID(p.ProfileID)
}
