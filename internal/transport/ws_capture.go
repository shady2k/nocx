package transport

// secrets.captureSave / secrets.captureDismiss — the settlement seam for
// the pending-capture registry (internal/credential/capture.go holds the
// contract; this file is where its triggers meet the wire).
//
// Saving is two stores, in one order: create the vault secret (atomically
// name-collision-resolved, the real name comes back), THEN rewrite every
// linked history row's redaction segment to the reference. Never the other
// order — rewriting first can leave a reference to a secret that does not
// exist. If the create succeeds and a rewrite fails: keep the secret, leave
// history safely masked, report the partial result, and let the rewrite be
// retried (the capture remembers the name and that the rewrite is owed, so
// the retry never mints openrouter.ai-2).
//
// The capture id is the idempotency key: a lost response retries with the
// same id and the registry answers with the recorded outcome instead of
// running the vault again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/secrets"
	"github.com/shady2k/nocx/internal/vault"
)

// captureSaveParams is the request for secrets.captureSave. Name is
// optional: absent, the backend-derived suggestion is used. The renderer
// may propose a name but must never predict that a suffixed name is free —
// the vault resolves collisions atomically and the real name comes back.
type captureSaveParams struct {
	CaptureID string `json:"captureId"`
	Name      string `json:"name,omitempty"`
}

// captureSaveResponse is the result of secrets.captureSave. Name is the
// vault name ACTUALLY used. Partial reports the brief's step-2 failure
// shape: the secret exists under Name, one or more history rewrites are
// still owed, and a retry of the same capture completes them without
// creating another secret.
type captureSaveResponse struct {
	Name    string `json:"name"`
	Partial bool   `json:"partial,omitempty"`
	// Error is the rewrite failure's message, present only when Partial.
	Error string `json:"error,omitempty"`
}

// captureDismissParams is the request for secrets.captureDismiss.
type captureDismissParams struct {
	CaptureID string `json:"captureId"`
}

// captureSaveHandlers answers secrets.captureSave: the settlement of a
// pending capture. The capture registry is connection-scoped transport state
// and stays a handler seam; the two store writes — the vault create and the
// history rewrites — go through the CaptureSaveOperation (vault, content
// gates). The wired flags reproduce the old dispatcher's unwired answers
// ("capture registry unavailable", "vault unavailable", the partial
// "history store unavailable" rewrite failure).
type captureSaveHandlers struct {
	op           capability.CaptureSaveOperation
	captures     *credential.CaptureRegistry
	r            Responder
	vaultWired   bool // vaultLifecycle != nil at construction
	contentWired bool // contentDB != nil at construction
}

// handleCaptureSave settles a capture into the vault. Idempotent: a retry
// of a settled capture returns the recorded name (and re-runs only the
// owed rewrites); a save in flight blocks until it settles, so two
// concurrent saves cannot mint two secrets.
func (h captureSaveHandlers) handleCaptureSave(ctx context.Context, req jsonrpcRequest) {
	if h.captures == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "secrets.captureSave: capture registry unavailable"})
		return
	}
	var p captureSaveParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
		return
	}
	if p.CaptureID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: captureId is required"})
		return
	}

	handle, err := h.captures.Reserve(credential.CaptureID(p.CaptureID))
	if err != nil {
		_ = h.r.TryError(req.ID, captureErrorFor(err))
		return
	}

	// An idempotent retry: the save already settled.
	if handle.Completed {
		if handle.RewritePending {
			runErr := h.op.Run(ctx, func(ctx context.Context, svc capability.CaptureSaveService) error {
				if rwErr := h.rewriteLinks(ctx, svc, handle.Links, handle.Name); rwErr != nil {
					_ = h.r.TryResult(req.ID, mustMarshal(captureSaveResponse{
						Name: handle.Name, Partial: true, Error: rwErr.Error(),
					}))
					return nil
				}
				h.captures.Complete(handle.CaptureID, handle.Name, handle.SecretID, false, nil)
				_ = h.r.TryResult(req.ID, mustMarshal(captureSaveResponse{Name: handle.Name}))
				return nil
			})
			if runErr != nil {
				answerOperationRefusal(h.r, req, runErr)
			}
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(captureSaveResponse{Name: handle.Name}))
		return
	}

	// The live save. The value never leaves the process: it goes from the
	// capture straight into the vault create.
	if !h.vaultWired {
		h.captures.Complete(handle.CaptureID, "", "", false, errors.New("vault unavailable"))
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "secrets.captureSave: vault unavailable"})
		return
	}
	name := handle.SuggestedName
	if p.Name != "" {
		name = sanitizeCaptureName(p.Name)
	}
	kind := vault.KindPassword
	for _, l := range handle.Links {
		if l.Redaction.Kind == string(secrets.KindPrivateKey) {
			kind = vault.KindPrivateKey
			break
		}
	}
	err = h.op.Run(ctx, func(ctx context.Context, svc capability.CaptureSaveService) error {
		secretID, realName, createErr := svc.CreateSecret(ctx, handle.Value,
			vault.SecretMeta{Name: name, Kind: kind})
		if createErr != nil {
			h.captures.Complete(handle.CaptureID, "", "", false, createErr)
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "secrets.captureSave: ", createErr))
			return nil
		}
		if rwErr := h.rewriteLinks(ctx, svc, handle.Links, "{{secret:"+realName+"}}"); rwErr != nil {
			// Step 1 done, step 2 owed: report the partial result; a retry
			// with the same capture completes the rewrite without a second
			// secret (the registry records name + rewrite-owed).
			h.captures.Complete(handle.CaptureID, realName, secretID, true, nil)
			_ = h.r.TryResult(req.ID, mustMarshal(captureSaveResponse{
				Name: realName, Partial: true, Error: rwErr.Error(),
			}))
			return nil
		}
		h.captures.Complete(handle.CaptureID, realName, secretID, false, nil)
		_ = h.r.TryResult(req.ID, mustMarshal(captureSaveResponse{Name: realName}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// captureDismissHandlers answers secrets.captureDismiss: registry only, no
// capability — destroying a pending capture touches no store.
type captureDismissHandlers struct {
	captures *credential.CaptureRegistry
	r        Responder
}

// handleCaptureDismiss destroys a pending capture and suppresses its
// fingerprint for the rest of the application session. Idempotent.
func (h captureDismissHandlers) handleCaptureDismiss(ctx context.Context, req jsonrpcRequest) {
	if h.captures == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "secrets.captureDismiss: capture registry unavailable"})
		return
	}
	var p captureDismissParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: params must be an object"})
		return
	}
	if p.CaptureID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: captureId is required"})
		return
	}
	if err := h.captures.Dismiss(credential.CaptureID(p.CaptureID)); err != nil {
		_ = h.r.TryError(req.ID, captureErrorFor(err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// rewriteLinks rewrites every linked history row's redaction segment to the
// reference through the operation's service. The rows are addressed by their
// stable ids. A row the retention sweep removed is skipped — the rewrite is
// moot, the secret still exists; anything else fails the rewrite set. When
// the content store is not wired the old dispatcher's "history store
// unavailable" failure is reported, which settles the save as a partial
// result exactly like a real rewrite failure.
func (h captureSaveHandlers) rewriteLinks(ctx context.Context, svc capability.CaptureSaveService, links []credential.CaptureLink, reference string) error {
	if !h.contentWired {
		return errors.New("history store unavailable")
	}
	var firstErr error
	for _, l := range links {
		if l.EntryID == "" {
			continue
		}
		id, err := strconv.ParseInt(l.EntryID, 10, 64)
		if err != nil {
			// The entry id is the store's numeric id in string form; a
			// non-numeric one is internal corruption, not a caller fact.
			if firstErr == nil {
				firstErr = fmt.Errorf("bad entry id %q: %w", l.EntryID, err)
			}
			continue
		}
		if err := svc.RewriteRedaction(ctx, id, l.Redaction, reference); err != nil {
			if errors.Is(err, content.ErrNotFound) {
				continue // swept away — nothing to rewrite
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// sanitizeCaptureName makes a renderer-proposed vault name safe to embed in
// a {{secret:NAME}} reference: braces are structural there, so they cannot
// ride a name. The backend's own suggestions need no such scrubbing (they
// are derived from hosts and env keys), but the renderer can type anything.
func sanitizeCaptureName(name string) string {
	return strings.NewReplacer("{", "", "}", "").Replace(strings.TrimSpace(name))
}

// captureErrorFor maps the registry's sentinel failures to JSON-RPC errors
// with a machine-readable reason, the way the vault errors carry theirs —
// the renderer must tell "expired" from "already consumed" from "the save
// failed earlier" apart, or it cannot decide what to show.
func captureErrorFor(err error) RPCError {
	reason := "capture-error"
	code := -32603
	switch {
	case errors.Is(err, credential.ErrCaptureUnknown):
		code, reason = -32010, "capture-expired"
	case errors.Is(err, credential.ErrCaptureConsumed):
		code, reason = -32011, "capture-consumed"
	case errors.Is(err, credential.ErrCaptureSaveFailed):
		code, reason = -32012, "capture-save-failed"
	}
	rpcErr := RPCError{Code: code, Message: err.Error()}
	if reason != "capture-error" {
		rpcErr.Data = json.RawMessage(`{"reason":"` + reason + `"}`)
	}
	return rpcErr
}
