package transport

// shell.complete — remote shell completion (nocx-w7h.15).
// The result shape is declared once in contracts/shell.complete.schema.json.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/session"
)

// shellCompleteParams is the request for shell.complete.
//
//	sessionId — required; the session to complete for
//	cwd       — required; the session's cwd, from OSC 7
//	line      — required; the full line being typed
//	pos       — required; the caret offset into line
//	limit     — optional; <1 → 50, >200 → 200
type shellCompleteParams struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Line      string `json:"line"`
	Pos       int    `json:"pos"`
	Limit     *int   `json:"limit"`
}

// shellCompleteEntry is one row of the shell.complete result, matching
// the schema exactly.
type shellCompleteEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Source string `json:"source"`
	IsDir  bool   `json:"isDir,omitempty"`
}

// shellCompleteResponse is the result of shell.complete. Entries is never
// nil: no matches is [].
type shellCompleteResponse struct {
	Entries   []shellCompleteEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
	Reason    string               `json:"reason,omitempty"`
}

// handleComplete serves the shell.complete method.
//
// Routes by session kind: a KindLocal session delegates to the local
// completer (the backend's own filesystem); a KindRemote session delegates
// to the SSH completer with the session's exact connect options. The
// SessionTargetOperation copies those immutable facts under the session gate,
// releases that gate, and retains the ordinary lane for the remote work.
func (h sessionShellHandlers) handleComplete(ctx context.Context, req jsonrpcRequest) {
	params, errMsg := parseShellCompleteParams(req)
	if errMsg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: " + errMsg})
		return
	}

	op, err := h.ops.ForSessionTarget(session.ID(params.SessionID))
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Session not found: " + params.SessionID})
		return
	}
	err = op.Run(ctx, func(ctx context.Context, target capability.SessionTarget) error {
		compReq := completion.Request{
			Host:  target.Host,
			Cwd:   params.Cwd,
			Line:  params.Line,
			Pos:   params.Pos,
			Limit: params.limit(),
		}

		var compResp *completion.Response
		var compErr error
		switch target.Kind {
		case session.KindLocal:
			if h.local != nil {
				compResp, compErr = h.local.Complete(ctx, compReq)
			}
		case session.KindRemote:
			if h.remote != nil {
				compResp, compErr = h.remote.Complete(ctx, compReq, target.SSHOptions...)
			}
		}
		if compResp == nil && compErr == nil {
			_ = h.r.TryResult(req.ID, mustMarshal(shellCompleteResponse{
				Entries: []shellCompleteEntry{},
				Reason:  "completion unavailable for this session kind",
			}))
			return nil
		}
		if compErr != nil {
			_ = h.r.TryResult(req.ID, mustMarshal(shellCompleteResponse{
				Entries: []shellCompleteEntry{},
				Reason:  "completion unavailable",
			}))
			return nil
		}

		_ = h.r.TryResult(req.ID, mustMarshal(toWireResponse(compResp)))
		return nil
	})
	if err != nil {
		if capability.IsRefused(err) {
			answerOperationRefusal(h.r, req, err)
			return
		}
		// The session closed after ForSessionTarget's construction-time
		// check but before its gated snapshot.
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Session not found: " + params.SessionID})
	}
}

// parseShellCompleteParams validates the request against the handler contract.
func parseShellCompleteParams(req jsonrpcRequest) (*shellCompleteParams, string) {
	var p shellCompleteParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, "params must be an object"
	}
	if p.SessionID == "" {
		return nil, "sessionId is required"
	}
	if p.Cwd == "" {
		return nil, "cwd is required"
	}
	if p.Line == "" {
		return nil, "line is required"
	}
	return &p, ""
}

func (p shellCompleteParams) limit() int {
	if p.Limit == nil {
		return 50
	}
	l := *p.Limit
	if l < 1 {
		return 50
	}
	if l > 200 {
		return 200
	}
	return l
}

// toWireResponse converts the completion package's response to the wire
// shape declared in contracts/shell.complete.schema.json.
func toWireResponse(resp *completion.Response) shellCompleteResponse {
	entries := make([]shellCompleteEntry, len(resp.Candidates))
	for i, c := range resp.Candidates {
		entries[i] = shellCompleteEntry{
			Name:   c.Name,
			Path:   c.Path,
			Source: c.Source,
			IsDir:  c.IsDir,
		}
	}
	return shellCompleteResponse{
		Entries:   entries,
		Truncated: resp.Truncated,
		Reason:    resp.Reason,
	}
}
