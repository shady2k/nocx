package transport

// shell.integrate — the in-band bootstrap plan for the shell at a trusted
// prompt (spec §4.4, nocx-ynsx). The renderer alone may call it, gated on
// PROMPT_READY && trusted && owned: consent changes authorisation, not the
// identity of the foreground process, so the backend never offers this on
// its own. This file serves the plan from the shellintegration seam.

import (
	"context"
	"encoding/json"

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

// sessionShellHandlers answers shell.complete and shell.integrate. Both are
// per-session operations (SessionOperation via ForSession) whose registry
// liveness check runs inside the capability; the completion and integration
// logic itself stays in the handler, on the completion / in-band seams. It
// holds the operation factory, the Responder and its seams — never the
// *WSServer.
type sessionShellHandlers struct {
	ops    *capability.SessionOperations // session gate; nil → session store not wired
	r      Responder
	local  completion.Completer // shell.complete for KindLocal sessions
	remote completion.Completer // shell.complete for KindRemote sessions
	inBand InBandBootstrapper   // shell.integrate plan builder
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
		answerOperationRefusal(h.r, req.ID, err)
	}
}

// shellSpecs declares the shell-plane control methods (migration map, "The
// rest"): shell.complete and shell.integrate run under the per-session
// SessionOperation (the session gate — the registry liveness check is the
// capability's) and register on the operation queue; the launcher /
// footprint methods are seam handlers on the ordinary lane under no
// operation, holding only the seams the migration map names. The
// SessionOperations factory is built here from the wired stores and shared
// across the shell methods.
func (s *WSServer) shellSpecs(lane control.Admission, sessionGate control.Admission) []methodSpec {
	sessionOps := capability.NewSessionOperations(sessionGate, lane, s.registry, s.profileUsage)
	shellSub := s.operationQueue("shell")
	return []methodSpec{
		regResponder(shellSub, "shell.complete", func(r Responder) handlerFunc {
			h := sessionShellHandlers{ops: sessionOps, r: r, local: s.localCompleter, remote: s.sshCompleter}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleComplete(ctx, req) }
		}),
		regResponder(shellSub, "shell.integrate", func(r Responder) handlerFunc {
			h := sessionShellHandlers{ops: sessionOps, r: r, inBand: s.inBand}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleIntegrate(ctx, req) }
		}),
		// shell.launcherCommand and shell.environmentObserved are NOT
		// registered: they were the P7 stream-and-passport surface, gated on
		// the marker latch ADR-0024 forbids, and the branch deleted their
		// handler, contracts and generated types (nocx-292k). The footprint
		// methods below outlive them — they read the fact store, which stays.
		regResponder(s.lane, "shell.footprint.status", func(r Responder) handlerFunc {
			h := footprintHandlers{
				r:              r,
				facts:          s.installedFacts,
				helperInstalls: s.helperInstalls,
				resolver:       s.resolver,
				sshCfg:         s.sshConfigResolver,
				profiles:       s.profiles,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleFootprintStatus(ctx, req) }
		}),
		regResponder(s.lane, "shell.footprint.uninstall", func(r Responder) handlerFunc {
			h := footprintHandlers{
				r:           r,
				uninstaller: s.remoteUninstaller,
				resolver:    s.resolver,
				log:         s.log,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleFootprintUninstall(ctx, req) }
		}),
		reg(s.lane, "shell.footprint.consent", func(w *wsConn, state *connState) handlerFunc {
			h := footprintHandlers{
				r:        w,
				consent:  s.helperConsent,
				registry: s.registry,
				log:      s.log,
			}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleConsent(ctx, state, req) }
		}),
	}
}
