package transport

// history.query — the recall ladder (design §10.6, nocx-ms7v.1). The result
// shape is declared once in contracts/history.query.schema.json and belongs
// to neither side; this file serves it from the ContentDB seam.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/secrets"
)

// historyQueryParams is the request the recall overlay sends. There is
// deliberately no params schema (contracts/README.md): the handler is the
// check, and rejects what it cannot parse.
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
	ID        string                `json:"id"`
	Command   string                `json:"command"`
	Cwd       string                `json:"cwd"`
	Host      string                `json:"host"`
	Status    content.CommandStatus `json:"status"`
	ExitCode  *int                  `json:"exitCode,omitempty"`
	StartedAt *int64                `json:"startedAt,omitempty"`
	EndedAt   *int64                `json:"endedAt"`
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

// historyQueryHandlers answers history.query. It holds the ContentOperation
// and the Responder; nothing else (migration map, "history.* — the content
// domain"). A nil operation is the content store not being wired: the
// handler then answers the honest source=session fallback, never an error.
type historyQueryHandlers struct {
	op capability.ContentOperation // nil → content store not wired
	r  Responder
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
	// from: session, empty, exhausted, scope echoed, no horizon. The overlay
	// labels it "this session only" rather than presenting it as all history.
	resp := historyQueryResponse{
		Entries:   []historyQueryEntry{},
		Scope:     string(scope),
		Exhausted: true,
		Source:    "session",
	}

	if h.op == nil {
		_ = h.r.TryResult(req.ID, mustMarshal(resp))
		return
	}

	err := h.op.Run(ctx, func(ctx context.Context, svc capability.ContentService) error {
		page, err := svc.QueryHistory(ctx, scope, cwd, host, limit, before, text)
		if err != nil {
			return err
		}
		if page.HasRows {
			resp.Source = "store"
		}
		resp.Exhausted = page.Exhausted
		resp.Coverage = page.Coverage
		for _, r := range page.Entries {
			kinds := r.MaskedKinds
			if kinds == nil {
				kinds = []string{}
			}
			reds := make([]redactionWire, 0, len(r.Redactions))
			for _, red := range r.Redactions {
				start, end := secrets.ToUTF16Span(r.Command, red.Start, red.End)
				reds = append(reds, redactionWire{
					Kind: red.Kind, Start: start, End: end, Prefix: red.Prefix, Suffix: red.Suffix,
				})
			}
			resp.Entries = append(resp.Entries, historyQueryEntry{
				ID:          strconv.FormatInt(r.ID, 10),
				Command:     r.Command,
				Cwd:         r.Cwd,
				Host:        r.Host,
				Status:      r.Status,
				ExitCode:    r.ExitCode,
				StartedAt:   r.StartedAt,
				EndedAt:     r.EndedAt,
				MaskedCount: r.MaskedCount,
				MaskedKinds: kinds,
				Redactions:  reds,
			})
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
func parseHistoryQueryParams(req jsonrpcRequest) (content.Scope, string, string, int, *int64, string, string) {
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

	var before *int64
	if p.Before != nil {
		n, err := strconv.ParseInt(*p.Before, 10, 64)
		if err != nil {
			return "", "", "", 0, nil, "", "before must be the opaque row id of the previous page"
		}
		before = &n
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
