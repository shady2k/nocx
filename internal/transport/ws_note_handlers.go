package transport

// The notes.* control handlers as constructed types: each holds a
// NoteOperation and the Responder — never the *WSServer, and never the
// *note.Service directly. The service arrives inside op.Run, guard-bound,
// so a handler cannot reach the store outside the operation that gates it.
//
// Notes belong to the config conflict domain for the same reason snippets
// do: a restore replaces the library underneath a write, and that is the
// two-writer race the config gate serialises.
//
// A LIST never carries bodies (design §5). The list is a list; sending
// every note's prose so a row can render forty pixels of it is a cost that
// stays invisible until somebody's library is big.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/note"
)

type noteHandlers struct {
	op    capability.NoteOperation // nil → notes domain not wired
	wired bool
	r     Responder
}

func (h noteHandlers) handleMethod(ctx context.Context, req jsonrpcRequest) {
	if !h.wired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "notes not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.NoteService) error {
		switch req.Method {
		case "notes.list":
			rows, err := svc.List(ctx)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: noteErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireNoteRows(rows)))
		case "notes.get":
			var p noteIDParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			n, err := svc.Get(ctx, p.ID)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: noteErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireNote(n)))
		case "notes.create":
			var p noteCreateParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			created, err := svc.Create(ctx, p.Body)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: noteErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireNote(created)))
		case "notes.update":
			var p noteUpdateParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			updated, err := svc.Update(ctx, p.ID, p.Body)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: noteErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(wireNote(updated)))
		case "notes.delete":
			var p noteIDParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			if err := svc.Delete(ctx, p.ID); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: noteErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(noteDeleteResponse(p)))
		case "notes.search":
			var p noteSearchParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
				return nil
			}
			hits, err := svc.Search(ctx, p.Query)
			if err != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: noteErrorCode(err), Message: err.Error()})
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(noteSearchResponse{Matches: wireNoteRows(hits).Notes}))
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// noteErrorCode maps a notes error to a JSON-RPC code: a missing record is
// the client's error (-32602); a store that could not answer is the
// server's (-32603), and the surface says the library is unavailable rather
// than showing an empty list.
func noteErrorCode(err error) int {
	if errors.Is(err, note.ErrNotFound) {
		return -32602
	}
	return -32603
}

// wireNote stamps the derived title on the way out. The store never keeps
// one (there is no title column), so this is the only place a note's name
// is decided — deriving it in the renderer as well would be the second
// owner of one fact (design §7).
func wireNote(n note.Note) note.Note {
	n.Title = note.DeriveTitle(n.Body)
	return n
}

// wireNoteRows forces the list to be non-nil: an empty library marshals as
// [] and never null — the renderer's first .map assumes it.
func wireNoteRows(rows []note.Row) noteListResponse {
	if rows == nil {
		rows = []note.Row{}
	}
	return noteListResponse{Notes: rows}
}

type noteListResponse struct {
	Notes []note.Row `json:"notes"`
}

type noteSearchResponse struct {
	Matches []note.Row `json:"matches"`
}

type noteDeleteResponse struct {
	ID string `json:"id"`
}

type noteIDParams struct {
	ID string `json:"id"`
}

type noteCreateParams struct {
	Body string `json:"body"`
}

type noteUpdateParams struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

type noteSearchParams struct {
	Query string `json:"query"`
}

// maxNoteBodyRunes bounds a note on the wire. A note is prose somebody
// writes in one sitting, and this is generous for that; it is a bound
// because the control plane may not carry an unbounded document, and
// because the only other ceiling is the params budget, which is about
// bytes rather than about notes.
const maxNoteBodyRunes = 200_000

// maxNoteQueryRunes bounds a search query. A query is what a person typed
// into a field.
const maxNoteQueryRunes = 1_000

func validateNoteIDRaw(raw json.RawMessage) string {
	var p noteIDParams
	if msg := decodeObject(raw, &p, "id"); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	return configIDRunes("id", p.ID)
}

func validateNoteCreateRaw(raw json.RawMessage) string {
	var p noteCreateParams
	if msg := decodeObject(raw, &p, "body"); msg != "" {
		return msg
	}
	// An EMPTY body is legal and ordinary: the chord opens a note and the
	// person types into it (design §6.3).
	return boundedRunes("body", p.Body, maxNoteBodyRunes)
}

func validateNoteUpdateRaw(raw json.RawMessage) string {
	var p noteUpdateParams
	if msg := decodeObject(raw, &p, "id", "body"); msg != "" {
		return msg
	}
	if p.ID == "" {
		return "id is required"
	}
	if msg := configIDRunes("id", p.ID); msg != "" {
		return msg
	}
	return boundedRunes("body", p.Body, maxNoteBodyRunes)
}

func validateNoteSearchRaw(raw json.RawMessage) string {
	var p noteSearchParams
	if msg := decodeObject(raw, &p, "query"); msg != "" {
		return msg
	}
	return boundedRunes("query", p.Query, maxNoteQueryRunes)
}
