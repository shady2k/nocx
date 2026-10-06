package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/session"
)

func (s *WSServer) sandboxSpecs() []methodSpec {
	queue := s.operationQueue("sandbox")
	cancelQueue := s.operationQueue("sandbox-cancel")
	return []methodSpec{
		regResponder(queue, "sandbox.status", params(validateSandboxStatusRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.status", func(c SandboxControl) (any, error) {
					var p SandboxStatusRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.Status(ctx, p)
				})
			}
		}),
		regResponder(queue, "sandbox.profile.get", params(validateSandboxProfileRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.profile.get", func(c SandboxControl) (any, error) {
					var p SandboxProfileRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.Profile(ctx, p)
				})
			}
		}),
		regResponder(queue, "sandbox.profile.update", params(validateSandboxProfileUpdateRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.profile.update", func(c SandboxControl) (any, error) {
					var p SandboxProfileUpdateRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.UpdateProfile(ctx, p)
				})
			}
		}),
		regResponder(queue, "sandbox.profile.reset", params(validateSandboxProfileResetRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.profile.reset", func(c SandboxControl) (any, error) {
					var p SandboxProfileResetRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.ResetProfile(ctx, p)
				})
			}
		}),
		regResponder(queue, "sandbox.preview", params(validateSandboxPreviewRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.preview", func(c SandboxControl) (any, error) {
					var p SandboxPreviewRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.Preview(ctx, p)
				})
			}
		}),
		reg(queue, "sandbox.replace", params(validateSandboxReplaceRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				var p SandboxReplaceRequest
				if json.Unmarshal(req.Params, &p) != nil {
					s.sandboxError(r, req.ID, "sandbox.replace", errors.New("invalid params"))
					return
				}
				if s.sandbox == nil {
					s.sandboxUnavailable(r, req.ID)
					return
				}
				op, err := s.sandbox.Replace(ctx, p)
				if err != nil {
					s.sandboxError(r, req.ID, "sandbox.replace", err)
					return
				}
				_, err = s.sandboxOperationResult(ctx, w, state, r, req, op)
				if err != nil {
					s.sandboxError(r, req.ID, "sandbox.replace", err)
				}
			}
		}),
		regResponder(cancelQueue, "sandbox.cancel", params(validateSandboxReplaceRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				var p SandboxReplaceRequest
				_ = json.Unmarshal(req.Params, &p)
				s.sandboxCall(ctx, r, req, "sandbox.cancel", func(c SandboxControl) (any, error) { return struct{}{}, c.Cancel(ctx, p) })
			}
		}),
		reg(queue, "sandbox.operation.get", params(validateSandboxOperationRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				var p SandboxOperationRequest
				_ = json.Unmarshal(req.Params, &p)
				if s.sandbox == nil {
					s.sandboxUnavailable(r, req.ID)
					return
				}
				op, err := s.sandbox.Operation(ctx, p)
				if err != nil {
					s.sandboxError(r, req.ID, "sandbox.operation.get", err)
					return
				}
				_, err = s.sandboxOperationResult(ctx, w, state, r, req, op)
				if err != nil {
					s.sandboxError(r, req.ID, "sandbox.operation.get", err)
				}
			}
		}),
		regResponder(queue, "sandbox.grant.get", params(validateSandboxGrantRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.grant.get", func(c SandboxControl) (any, error) {
					var p SandboxGrantRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.Grant(ctx, p)
				})
			}
		}),
		regResponder(queue, "sandbox.access.list", params(validateSandboxAccessListRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.access.list", func(c SandboxControl) (any, error) {
					var p SandboxAccessListRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.AccessList(ctx, p)
				})
			}
		}),
		regResponder(queue, "sandbox.access.resolve", params(validateSandboxAccessResolveRaw), func(r Responder) handlerFunc {
			return func(ctx context.Context, req jsonrpcRequest) {
				s.sandboxCall(ctx, r, req, "sandbox.access.resolve", func(c SandboxControl) (any, error) {
					var p SandboxAccessResolveRequest
					_ = json.Unmarshal(req.Params, &p)
					return c.AccessResolve(ctx, p)
				})
			}
		}),
	}
}

func (s *WSServer) sandboxCall(ctx context.Context, r Responder, req jsonrpcRequest, method string, call func(SandboxControl) (any, error)) {
	if s.sandbox == nil {
		s.sandboxUnavailable(r, req.ID)
		return
	}
	result, err := call(s.sandbox)
	if err != nil {
		s.sandboxError(r, req.ID, method, err)
		return
	}
	_ = r.TryResult(req.ID, mustMarshal(result))
}

func (s *WSServer) sandboxUnavailable(r Responder, id json.RawMessage) {
	_ = r.TryError(id, RPCError{Code: -32601, Message: "sandbox control not available"})
}

func (s *WSServer) sandboxError(r Responder, id json.RawMessage, method string, err error) {
	var sandboxErr *SandboxError
	if errors.As(err, &sandboxErr) {
		_ = r.TryError(id, RPCError{Code: -32602, Message: method + ": " + sandboxErr.Reason, Data: map[string]string{"reason": sandboxErr.Reason}})
		return
	}
	_ = r.TryError(id, rpcErrorFor(-32603, method+": ", err))
}

func (s *WSServer) sandboxOperationResult(ctx context.Context, w *wsConn, state *connState, r Responder, req jsonrpcRequest, op SandboxOperation) (SandboxOperationResult, error) {
	result := SandboxOperationResult{OperationID: op.Launch.ID, PaneID: op.Launch.PaneID, State: op.Launch.State, Mode: op.Launch.Mode, Reason: op.Reason}
	if op.Opened == nil || op.Opened.Session == nil {
		if err := r.TryResult(req.ID, mustMarshal(result)); err != nil {
			return SandboxOperationResult{}, err
		}
		return result, nil
	}
	opened := *op.Opened
	sess := opened.Session
	rx := s.getRx(sess.ID())
	if rx == nil {
		result.Reason = "unknown/publication-pending"
		if err := r.TryResult(req.ID, mustMarshal(result)); err != nil {
			return SandboxOperationResult{}, err
		}
		return result, nil
	}
	ident := sess.Identity()
	open := &openResult{
		SessionID: string(sess.ID()), InstanceID: string(ident.InstanceID), SessionEpoch: ident.Epoch,
		WorkspaceID: opened.WorkspaceID, Cwd: sess.Cwd(), DesiredMode: desiredModeForAck(opened.Config.Remote),
		EffectiveSize: sizeResultOf(sess.EffectiveSize()), Parent: parentResultFor(sess),
		AwaitsIntegration: s.sessionAwaitsIntegration(sess.ID()),
	}
	result.Open = open
	if err := r.TryResult(req.ID, mustMarshal(result)); err != nil {
		return SandboxOperationResult{}, err
	}
	// Bind only after the nested standard open acknowledgement is queued. This
	// keeps any session-scoped traffic behind its identity just as ordinary open.
	alreadyAttached := state.has(sess.ID())
	state.add(sess)
	prev, prevState := rx.setSubscriber(w, state)
	if prev != nil && prev != w {
		s.announceDisplacement(sess.ID(), sess.Identity(), prev, prevState)
		rx.ring.wake()
	}
	_, from, _, _ := rx.ring.snapshot(0)
	s.flushFilesChanged(sess.ID(), w)
	s.flushUploadDone(sess.ID(), w)
	s.replayLifecycleFacts(sess.ID())
	s.replayIntegration(sess.ID())
	s.replayToolSurface(sess.ID())
	s.replayPaneObservation(sess.ID())
	if !alreadyAttached {
		sidBytes, _ := session.IDToBytes(sess.ID())
		go s.ringToConn(ctx, w, sidBytes, rx, from)
	}
	s.resendScreen(ctx, sess.ID())
	return result, nil
}

func sandboxRequired(raw json.RawMessage, target any, fields ...string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "invalid params"
	}
	if reason := decodeAPIParams(raw, target); reason != "" {
		return reason
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "invalid params"
	}
	for _, field := range fields {
		if len(obj[field]) == 0 || string(obj[field]) == "null" {
			return fmt.Sprintf("%s required", field)
		}
	}
	return ""
}

func validateSandboxStatusRaw(raw json.RawMessage) string {
	var p SandboxStatusRequest
	return sandboxRequired(raw, &p, "paneId")
}

func validateSandboxProfileRaw(raw json.RawMessage) string {
	var p SandboxProfileRequest
	if len(raw) == 0 || string(raw) == "null" {
		return "invalid params"
	}
	return sandboxRequired(raw, &p)
}

func validateSandboxProfileUpdateRaw(raw json.RawMessage) string {
	var p SandboxProfileUpdateRequest
	return sandboxRequired(raw, &p, "expectedRevision", "roots")
}

func validateSandboxProfileResetRaw(raw json.RawMessage) string {
	var p SandboxProfileResetRequest
	return sandboxRequired(raw, &p, "workspaceId", "expectedRevision")
}

func validateSandboxPreviewRaw(raw json.RawMessage) string {
	var p SandboxPreviewRequest
	if reason := sandboxRequired(raw, &p, "paneId", "expectedHeadId", "mode", "delta"); reason != "" {
		return reason
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["source"] == nil {
		return "source required"
	}
	return ""
}

func validateSandboxReplaceRaw(raw json.RawMessage) string {
	var p SandboxReplaceRequest
	return sandboxRequired(raw, &p, "operationId", "confirmationId")
}

func validateSandboxOperationRaw(raw json.RawMessage) string {
	var p SandboxOperationRequest
	return sandboxRequired(raw, &p, "operationId")
}

func validateSandboxGrantRaw(raw json.RawMessage) string {
	var p SandboxGrantRequest
	return sandboxRequired(raw, &p, "launchId")
}

func validateSandboxAccessListRaw(raw json.RawMessage) string {
	var p SandboxAccessListRequest
	if reason := sandboxRequired(raw, &p, "paneId", "launchId", "cursor", "limit"); reason != "" {
		return reason
	}
	if p.PaneID == "" || p.LaunchID == "" || p.Cursor > 500 || p.Limit > 200 {
		return "invalid params"
	}
	return ""
}

func validateSandboxAccessResolveRaw(raw json.RawMessage) string {
	var p SandboxAccessResolveRequest
	if reason := sandboxRequired(raw, &p, "paneId", "launchId", "eventId", "eventRevision", "decision", "expectedStandardRevision", "expectedWorkspaceRevision"); reason != "" {
		return reason
	}
	if p.PaneID == "" || p.LaunchID == "" || p.EventID == "" || len(p.EventID) > 256 || p.EventRevision == 0 {
		return "invalid params"
	}
	if p.Decision != "dismiss" && p.Decision != "allow-ro" && p.Decision != "allow-rw" {
		return "invalid params"
	}
	return ""
}
