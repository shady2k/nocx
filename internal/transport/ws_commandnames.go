package transport

// shell.commandNames — the SHARED half of command discovery (carrier design
// §8, nocx-m8jwn.6).
//
// The session's own shell answers the session-local half over OSC 636:
// aliases, builtins, keywords and functions, which belong to one shell and
// may never be cached for another. This method answers the other half — the
// executables on the target's PATH — which is identical for every session to
// the same target and expensive enough that running it per tab was the
// defect. The backend owns one in-memory cache for it, keyed on the resolved
// route, the remote user, the shell family, a hash of the effective PATH and
// the integration generation, and invalidated on the mtime of each PATH
// directory.
//
// The result carries a STATE, and that is the point of it being a result
// rather than a list: a missing snapshot used to render as "command names
// are still loading", which is true only while a scan is running.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/commandnames"
	"github.com/shady2k/nocx/internal/session"
)

// CommandNamesResolver answers the shared name set for one live session
// target. The composition root wires the implementation that decides, from
// the target's kind and options, which source to build — the local machine
// under a supervised process group, or one resolved SSH route over the
// discovery lane. The transport never decides that itself, and never holds
// an SSH option.
type CommandNamesResolver interface {
	CommandNames(ctx context.Context, target capability.SessionTarget) commandnames.Result
}

// WithCommandNames attaches the shared command-name service behind
// shell.commandNames. Nil leaves the method answering a stated `failed` for
// every session rather than a JSON-RPC error: an unwired seam is a degrade
// the product must be able to say out loud, and the renderer already has a
// row for exactly that sentence.
func WithCommandNames(r CommandNamesResolver) WSServerOption {
	return func(s *WSServer) { s.commandNames = r }
}

// shellCommandNamesResult is the wire shape, matching
// contracts/shell.commandNames.schema.json exactly.
//
// Every field is `required` in the schema and none is omitempty here, which
// is deliberate: `vault.status` shipped for weeks without a field the
// renderer read on every render because an omitted field and an absent one
// look identical to a Go test that decodes into an anonymous struct.
type shellCommandNamesResult struct {
	State     string   `json:"state"`
	Names     []string `json:"names"`
	AgeMs     int64    `json:"ageMs"`
	Reason    string   `json:"reason"`
	Truncated bool     `json:"truncated"`
}

// toWireCommandNames converts one service result. Names is never nil: the
// schema wants an array, and `null` there would decode to a renderer state
// nothing in the contract describes — the same defect the vault's
// `providers` field had.
func toWireCommandNames(res commandnames.Result) shellCommandNamesResult {
	names := res.Names
	if names == nil {
		names = []string{}
	}
	return shellCommandNamesResult{
		State:     string(res.State),
		Names:     names,
		AgeMs:     res.Age.Milliseconds(),
		Reason:    res.Reason,
		Truncated: res.Truncated,
	}
}

// handleCommandNames serves shell.commandNames.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"shell.commandNames","params":{"sessionId":"0123…"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"state":"ready","names":["git","ls"],"ageMs":0,"reason":"","truncated":false}}
//
// It runs under the same staged SessionTargetOperation shell.complete uses:
// the immutable route facts are copied while the session gate is held, and
// the gate is released before any remote work. A scan can take up to its
// whole deadline, and holding the session gate for that would block resize
// and close on every other tab.
func (h sessionShellHandlers) handleCommandNames(ctx context.Context, req jsonrpcRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.SessionID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: sessionId required"})
		return
	}
	op, err := h.ops.ForSessionTarget(session.ID(params.SessionID))
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Session not found: " + params.SessionID})
		return
	}
	err = op.Run(ctx, func(ctx context.Context, target capability.SessionTarget) error {
		if h.names == nil {
			// A stated degrade, not an error. The renderer's row for
			// `failed` says this in a sentence a person can read; a
			// JSON-RPC error would leave the dropdown with a spinner that
			// never resolves.
			_ = h.r.TryResult(req.ID, mustMarshal(toWireCommandNames(commandnames.Result{
				State:  commandnames.StateFailed,
				Reason: "command discovery is not available in this build",
			})))
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(toWireCommandNames(h.names.CommandNames(ctx, target))))
		return nil
	})
	if err != nil {
		if capability.IsRefused(err) {
			answerOperationRefusal(h.r, req, err)
			return
		}
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Session not found: " + params.SessionID})
	}
}

// validateShellCommandNamesRaw is the registered validator: the sessionId is
// server-minted and must have that shape before anything is looked up.
func validateShellCommandNamesRaw(raw json.RawMessage) string {
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
