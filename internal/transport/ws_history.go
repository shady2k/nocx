package transport

// history.query — the recall ladder (design §10.6, nocx-ms7v.1). The result
// shape is declared once in contracts/history.query.schema.json and belongs
// to neither side; this file serves it from the ContentDB seam.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
)

// historyQueryParams is the request the recall overlay sends. The earlier
// decision to leave params unpinned was wrong: history.query.params.schema.json
// is now the wire contract, and its registered validator remains runtime enforcement.
//
//	scope  — required; directory | host | everywhere
//	cwd    — required when scope=directory; the exact directory rung
//	host   — required when scope=host; "" is the local machine
//	limit  — optional; <1 → 50, >200 → 200
//	before — optional; the opaque row id the previous page ended at
//	text   — optional; the search filter (nocx-ms7v), a case-insensitive
//	         substring over command within the rung; empty means no filter
type historyQueryParams struct {
	Scope  string  `json:"scope"`
	Cwd    *string `json:"cwd"`
	Host   *string `json:"host"`
	Limit  *int    `json:"limit"`
	Before *string `json:"before"`
	Text   *string `json:"text"`
}

// historyQueryEntry is one row of the history.query result, matching the
// schema exactly. ID is the opaque row handle: the string form of the
// store's row id, stable for the life of the row and usable as `before`.
// StartedAt rides along so the detail pane can render a duration; it is
// nullable and omitted when the store never observed a start.
type historyQueryEntry struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	Host      string `json:"host"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	StartedAt *int64 `json:"startedAt,omitempty"`
	EndedAt   *int64 `json:"endedAt"`
	// MaskedCount and MaskedKinds are what the store redacted from Command
	// at record time — the durable text is always the masked one, and the
	// facts ride the row so a block reconstructed after a restart can say
	// "3 secrets masked" with the kinds. Never null: no mask is []
	// (contracts/history.query.schema.json).
	MaskedCount int      `json:"maskedCount"`
	MaskedKinds []string `json:"maskedKinds"`
	// Redactions are the row's structured segments, offsets in UTF-16 code
	// units into Command (the store holds bytes; the wire converts once).
	// A segment the user saved to a vault reference is gone from this list
	// — the reference sits in Command instead. Never null: none is [].
	Redactions []redactionWire `json:"redactions"`
}

// historyQueryResponse is the result of history.query. Entries is never nil:
// no matches is [] (the schema says so, and a null would throw the overlay's
// first .map — the nocx-25k9.14 defect class). Coverage is the store-wide
// horizon (oldest retained entry's ended_at), null when the store holds
// nothing — the overlay renders the line only when there is a horizon.
type historyQueryResponse struct {
	Entries   []historyQueryEntry `json:"entries"`
	Scope     string              `json:"scope"`
	Exhausted bool                `json:"exhausted"`
	Source    string              `json:"source"`
	Coverage  *int64              `json:"coverage"`
}

// defaultHistoryPageLimit is the page size when the caller sends none.
const defaultHistoryPageLimit = 50

// maxHistoryPageLimit caps a page so a runaway overlay cannot ask for the
// whole history in one request.
const maxHistoryPageLimit = 200

// historyQueryHandlers answers history.query. It holds the ContentOperation,
// the durable-history availability read, and the Responder; nothing else
// (migration map, "history.* — the content domain").
//
// Two things mean "there is no store to answer from" and both answer
// source=unavailable, never an error: a nil operation (nothing wired at all)
// and an availability that says durable history is not running. The second
// is the production case — the composition root injects a stub ContentDB on
// its degrade paths, so the operation is non-nil and the store would answer
// ErrNotImplemented; before this the honest fallback below was unreachable
// in the shipped app and the overlay got a -32603 instead (nocx-rtg0.15).
type historyQueryHandlers struct {
	op capability.ContentOperation // nil → content store not wired
	// durable reports whether durable history is running. Never nil; the
	// registration supplies the server's reader.
	durable func() bool
	r       Responder
}

// handleHistoryQuery serves the history.query method.
//
// Three behaviours carry the decisions the schema names:
func (h historyQueryHandlers) handleHistoryQuery(ctx context.Context, req jsonrpcRequest) {
	scope, cwd, host, limit, before, text, errMsg := parseHistoryQueryParams(req)
	if errMsg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + errMsg})
		return
	}

	// The default answer is the honest one when there is nothing to answer
	// from: empty, exhausted, scope echoed, no horizon. Source starts at
	// session — the store answered and holds nothing — and becomes store
	// below when it holds rows, or unavailable when there is no store at
	// all. Three states, because the first two used to be one and the
	// overlay could not tell a terminal that has forgotten nothing from a
	// terminal that is not keeping anything.
	resp := historyQueryResponse{
		Entries:   []historyQueryEntry{},
		Scope:     string(scope),
		Exhausted: true,
		Source:    "session",
	}

	if h.op == nil || !h.durable() {
		resp.Source = "unavailable"
		_ = h.r.TryResult(req.ID, mustMarshal(resp))
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ContentService) error {
		page, err := svc.QueryHistory(ctx, historyLedgerQuery(scope, cwd, host, limit, before, text))
		if err != nil {
			return err
		}
		if page.HasRows {
			resp.Source = "store"
		}
		resp.Exhausted = page.Exhausted
		resp.Coverage = page.Coverage
		for _, row := range page.Entries {
			entry, mapErr := historyQueryEntryOf(row)
			if mapErr != nil {
				return mapErr
			}
			if entry == nil {
				continue
			}
			resp.Entries = append(resp.Entries, *entry)
		}
		_ = h.r.TryResult(req.ID, mustMarshal(resp))
		return nil
	})
	if err != nil {
		// A gate refusal is the saturation error; anything else keeps the
		// store-failure answer unchanged from the pre-capability handler.
		var rej *capability.RefusedError
		if errors.As(err, &rej) {
			_ = h.r.TryError(req.ID, saturationRPCError(req.Method, &rej.Rejection))
			return
		}
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "history.query: ", err))
	}
}

// parseHistoryQueryParams validates the request against the handler contract
// above. The returned message is empty when the params are usable.
func parseHistoryQueryParams(req jsonrpcRequest) (content.Scope, string, string, int, *string, string, string) {
	var p historyQueryParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return "", "", "", 0, nil, "", "params must be an object"
	}

	var scope content.Scope
	switch p.Scope {
	case "directory":
		scope = content.ScopeDirectory
	case "host":
		scope = content.ScopeHost
	case "everywhere":
		scope = content.ScopeEverywhere
	default:
		return "", "", "", 0, nil, "", "scope must be one of directory, host, everywhere"
	}

	var cwd, host string
	if p.Cwd != nil {
		cwd = *p.Cwd
	}
	if p.Host != nil {
		host = *p.Host
	}
	// Presence, not value: "" is a legitimate directory rung (a command whose
	// cwd was never known) and the local-machine host rung.
	if scope == content.ScopeDirectory && p.Cwd == nil {
		return "", "", "", 0, nil, "", "cwd is required for scope=directory"
	}
	if scope == content.ScopeHost && p.Host == nil {
		return "", "", "", 0, nil, "", "host is required for scope=host"
	}

	limit := defaultHistoryPageLimit
	if p.Limit != nil {
		limit = *p.Limit
		if limit < 1 {
			limit = defaultHistoryPageLimit
		} else if limit > maxHistoryPageLimit {
			limit = maxHistoryPageLimit
		}
	}

	// THE CURSOR IS OPAQUE, and after nocx-rtg0.19 it is opaque in fact and
	// not only in the contract's word: it used to be parsed as the interim
	// table's decimal rowid, and it is now the entry's own id, which the
	// store resolves to a position. Nothing here inspects its shape — a
	// handle this transport can parse is a handle it will one day be tempted
	// to compare, and the order is commit order and never an id.
	var before *string
	if p.Before != nil {
		if *p.Before == "" {
			return "", "", "", 0, nil, "", "before must be the opaque row id of the previous page"
		}
		before = p.Before
	}

	// Absent and empty are the same state on the wire: no filter. The
	// client omits the field when it has nothing to filter by, and the
	// store treats "" as no filter either way.
	text := ""
	if p.Text != nil {
		text = *p.Text
	}
	return scope, cwd, host, limit, before, text, ""
}
