package transport

// connections.helperConsent — the write half of the connect-time helper ask
// (ADR-0068, owner's decision 2026-09-16). The renderer's one consent
// dialog (host-key-dialog.tsx) calls this after a person answers the helper
// question an open failure raised (session_open_helper_consent.go); the
// answer is persisted by fingerprint (ADR-0034, internal/helper/consent),
// and the caller retries the open, which now finds an answered machine.

import (
	"context"
	"encoding/json"
)

// HelperConsentWriter is the seam this method writes through: Grant and Deny
// are internal/helper/consent's own vocabulary (Store already satisfies
// this), so a saved answer means exactly what the connect-time resolver
// reads back (internal/app/consent.go's Resolve).
type HelperConsentWriter interface {
	Grant(fingerprint string) error
	Deny(fingerprint string) error
}

// WithHelperConsentWriter attaches a HelperConsentWriter for the
// connections.helperConsent JSON-RPC method. When not wired, the handler
// returns a JSON-RPC error rather than pretending to have recorded an
// answer nobody can look up again.
func WithHelperConsentWriter(w HelperConsentWriter) WSServerOption {
	return func(s *WSServer) { s.helperConsentWriter = w }
}

// connectionsHelperConsentParams is the payload of
// "connections.helperConsent": the fingerprint the answer is keyed by
// (echoed from the open failure's helperConsentData), and the person's
// decision. Host travels for the log line only — it is never what the
// answer is keyed by (ADR-0034: the fingerprint is the whole identity).
type connectionsHelperConsentParams struct {
	Fingerprint string `json:"fingerprint"`
	Host        string `json:"host,omitempty"`
	Granted     bool   `json:"granted"`
}

// connectionsHelperConsentResult confirms which fingerprint was answered and
// how, so the renderer's retry is keyed on the same identity it asked about
// rather than trusting its own copy of the params back.
type connectionsHelperConsentResult struct {
	Fingerprint string `json:"fingerprint"`
	Answer      string `json:"answer"`
}

type helperConsentHandlers struct {
	writer HelperConsentWriter
	r      Responder
}

// handleConnectionsHelperConsent writes the person's answer to the
// connect-time helper ask.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.helperConsent","params":{"fingerprint":"SHA256:MKEj…","host":"1.2.3.4:22","granted":true}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"fingerprint":"SHA256:MKEj…","answer":"granted"}}
func (h helperConsentHandlers) handleConnectionsHelperConsent(_ context.Context, req jsonrpcRequest) {
	var params connectionsHelperConsentParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.Fingerprint == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: fingerprint required"})
		return
	}
	if h.writer == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Helper consent not available (no writer wired)"})
		return
	}

	var (
		err    error
		answer string
	)
	if params.Granted {
		err = h.writer.Grant(params.Fingerprint)
		answer = "granted"
	} else {
		err = h.writer.Deny(params.Fingerprint)
		answer = "denied"
	}
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Record helper consent failed: " + err.Error()})
		return
	}

	_ = h.r.TryResult(req.ID, mustMarshal(connectionsHelperConsentResult{
		Fingerprint: params.Fingerprint,
		Answer:      answer,
	}))
}

// validateHelperConsentRaw is the registered validator for
// connections.helperConsent: fingerprint is the write's whole identity
// (ADR-0034), so a request naming none is refused before the store is
// touched.
func validateHelperConsentRaw(raw json.RawMessage) string {
	var p connectionsHelperConsentParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.Fingerprint == "" {
		return "fingerprint is required"
	}
	if msg := validateStringBound("fingerprint", p.Fingerprint, maxHostRunes); msg != "" {
		return msg
	}
	if p.Host != "" {
		if msg := validateStringBound("host", p.Host, maxHostRunes); msg != "" {
			return msg
		}
	}
	return ""
}
