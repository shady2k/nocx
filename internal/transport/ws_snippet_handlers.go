package transport

// The snippets.* control handlers as constructed types: each handler holds a
// SnippetOperation and the Responder — never the *WSServer, and never the
// *snippet.Service directly. The service is handed to the callback inside
// op.Run, guard-bound, so a handler cannot reach the store outside the
// operation that gates it (capability.ErrOperationInactive).
//
// Snippets belong to the config conflict domain: the library is one document
// under the profile directory that backup/restore also writes, so a snippet
// mutation must conflict with a config-domain operation the way profiles.*
// and settings.* do — a restore replacing the document underneath the
// mutation is exactly the two-writer race the config gate serialises. The
// operation therefore holds the config gate, and only it: snippets never
// resolve vault rows, so the vault gate is not held.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/snippet"
)

type snippetHandlers struct {
	op    capability.SnippetOperation // nil → snippets domain not wired
	wired bool                        // snippet service wired
	r     Responder
}

func (h snippetHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "snippets not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.SnippetService) error {
		switch req.Method {
		case "snippets.list":
			all, err := svc.List()
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: snippetMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireSnippetList(all)))
		case "snippets.create":
			var p snippetCreateParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			created, err := svc.Create(p.Title, p.Body)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: snippetMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(created))
		case "snippets.update":
			var p snippetUpdateParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if p.ID == "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "id required"})
				return nil
			}
			updated, err := svc.Update(p.ID, p.Title, p.Body)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: snippetMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(updated))
		case "snippets.delete":
			var p snippetDeleteParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if p.ID == "" {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "id required"})
				return nil
			}
			if err := svc.Delete(p.ID); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: snippetMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(snippetDeleteResponse(p)))
		case "snippets.reorder":
			var p snippetReorderParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			reordered, err := svc.Reorder(p.IDs)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: snippetMethodErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireSnippetList(reordered)))
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// snippetMethodErrorCode maps a snippet service error to a JSON-RPC code: a
// missing record or a non-permutation reorder is the client's error (-32602);
// anything else is the server's (-32603).
func snippetMethodErrorCode(err error) int {
	if errors.Is(err, snippet.ErrNotFound) || errors.Is(err, snippet.ErrNotAPermutation) {
		return -32602
	}
	return -32603
}

// wireSnippetList forces the result's snippets slice to be non-nil: an empty
// library must marshal as [] and never null — the renderer's first .map
// assumes it (the schema's own description).
func wireSnippetList(snips []snippet.Snippet) snippetListResponse {
	if snips == nil {
		snips = []snippet.Snippet{}
	}
	return snippetListResponse{Snippets: snips}
}

type snippetListResponse struct {
	Snippets []snippet.Snippet `json:"snippets"`
}

type snippetDeleteResponse struct {
	ID string `json:"id"`
}

type snippetCreateParams struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type snippetUpdateParams struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type snippetDeleteParams struct {
	ID string `json:"id"`
}

type snippetReorderParams struct {
	IDs []string `json:"ids"`
}

// maxSnippetBodyRunes bounds a snippet body on the wire. The title is a name
// like every other config name (maxConfigNameRunes); the body is a phrase a
// person saved, so it is generous — but it is a bound, and it is the only
// one the library has: snippet.Service stores what it is handed and the
// document is read whole on every list.
const maxSnippetBodyRunes = 16_000

// validateSnippetCreateRaw is the registered validator for snippets.create.
// The id is minted by the backend (nocx-b7b5), so there is none to check.
func validateSnippetCreateRaw(raw json.RawMessage) string {
	var p snippetCreateParams
	if msg := decodeObjectStrict(raw, &p, "title", "body"); msg != "" {
		return msg
	}
	return validateSnippetTextParams(p.Title, p.Body)
}

// validateSnippetUpdateRaw is the registered validator for snippets.update.
func validateSnippetUpdateRaw(raw json.RawMessage) string {
	var p snippetUpdateParams
	if msg := decodeObjectStrict(raw, &p, "id", "title", "body"); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	if msg := configIDRunes("id", p.ID); msg != "" {
		return msg
	}
	return validateSnippetTextParams(p.Title, p.Body)
}

// validateSnippetDeleteRaw is the registered validator for snippets.delete.
func validateSnippetDeleteRaw(raw json.RawMessage) string {
	var p snippetDeleteParams
	if msg := decodeObjectStrict(raw, &p, "id"); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	return configIDRunes("id", p.ID)
}

// validateSnippetReorderRaw is the registered validator for snippets.reorder.
// Whether the ids are a PERMUTATION of the library is the service's rule and
// stays there (snippet.ErrNotAPermutation) — it needs the stored list, which
// a wire validator does not have and must not read.
func validateSnippetReorderRaw(raw json.RawMessage) string {
	var p snippetReorderParams
	if msg := decodeObjectStrict(raw, &p, "ids"); msg != "" {
		return msg
	}
	for _, id := range p.IDs {
		if msg := configIDRunes("ids", id); msg != "" {
			return msg
		}
	}
	return ""
}

// validateSnippetTextParams bounds the two fields a snippet carries.
func validateSnippetTextParams(title, body string) string {
	if msg := boundedRunes("title", title, maxConfigNameRunes); msg != "" {
		return msg
	}
	return boundedRunes("body", body, maxSnippetBodyRunes)
}
