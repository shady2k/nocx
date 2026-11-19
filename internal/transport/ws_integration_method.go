package transport

// connections.setIntegrationMethod — the write half of the connect-time ask
// (ADR-0069, superseding the yes/no shape of ADR-0068's connections.
// helperConsent). The renderer's one consent dialog (host-key-dialog.tsx)
// calls this after a person chooses an integration method for an open
// failure's ask (session_open_helper_consent.go): raw, script or helper —
// never auto, which is not an answer (ADR-0033).
//
// THE ANSWER IS THE HOST'S (ADR-727g63tr decision 1, which supersedes
// ADR-0069's "the saved connection's desiredMode, and a typed ssh's choice
// applies to that session only"). It is written to internal/hostmode's record
// by the fingerprint the ask named, for a saved connection and a hand-typed
// one alike, because which method a machine uses is a fact about that machine
// — two tabs to one host resolve one mode, and a second open raises no ask.
//
// One other write rides beside it and does not own the answer: the machine's
// CONSENT to deploy the binary, by fingerprint, and only when the chosen method
// is helper (ADR-0034, internal/helper/consent). Choosing raw or script writes
// nothing to that store: a fingerprint record there answers "may a binary be
// deployed here", never "which method this host uses" — the two questions stay
// unmerged (ADR-0105). That is why hostmode is a separate store beside consent
// and not a field of it. The connection editor's own write is a separate
// method (ws_host_mode.go) because it names a profile instead of a fingerprint
// and grants nothing.

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
)

// HelperConsentGranter is the seam this method grants the helper through:
// Grant is internal/helper/consent's own vocabulary (Store already satisfies
// this), so a saved grant means exactly what the connect-time resolver reads
// back (internal/app/consent.go's Resolve). Deny is not part of this seam —
// choosing raw or script writes nothing to the consent store, so nothing
// calls it any more (ADR-0069; internal/helper/consent.Store.Deny is gone
// with the last caller).
type HelperConsentGranter interface {
	Grant(fingerprint string) error
}

// WithHelperConsentWriter attaches a HelperConsentGranter for the
// connections.setIntegrationMethod JSON-RPC method's helper arm. When not
// wired, choosing helper fails the request rather than pretending to have
// recorded a grant nobody can look up again; choosing raw or script needs no
// granter at all.
func WithHelperConsentWriter(w HelperConsentGranter) WSServerOption {
	return func(s *WSServer) { s.helperConsentWriter = w }
}

// WithHostModeStore attaches the HOST record the connect-time ask writes and
// the open path reads (ADR-727g63tr decision 1). It is required: without it
// this method has nowhere to record an answer that every later open of the
// host depends on, so a request naming no store is refused rather than
// answered into nothing.
func WithHostModeStore(store *hostmode.Store) WSServerOption {
	return func(s *WSServer) { s.hostModes = store }
}

// integrationMethodParams is the payload of "connections.setIntegrationMethod":
// the fingerprint the ask named (echoed from the open failure's
// helperConsentData) and the chosen method. Host travels for the log line
// only.
type integrationMethodParams struct {
	Fingerprint string `json:"fingerprint"`
	Host        string `json:"host,omitempty"`
	Method      string `json:"method"`
}

// integrationMethodResult confirms which fingerprint was answered, with which
// method — so the renderer's retry is keyed on the identity the backend
// actually acted on rather than trusting its own copy of the request back.
type integrationMethodResult struct {
	Fingerprint     string `json:"fingerprint"`
	Method          string `json:"method"`
	HostRecordSaved bool   `json:"hostRecordSaved"`
}

type integrationMethodHandlers struct {
	granter HelperConsentGranter // nil → choosing helper fails
	modes   *hostmode.Store      // nil → nothing may be answered
	r       Responder
	log     log.Logger
}

// handleConnectionsSetIntegrationMethod writes the person's answer to the
// connect-time ask.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.setIntegrationMethod","params":{"fingerprint":"SHA256:MKEj…","host":"1.2.3.4:22","profileId":"ssh-1","method":"script"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"fingerprint":"SHA256:MKEj…","method":"script","hostRecordSaved":true}}
func (h integrationMethodHandlers) handle(ctx context.Context, req jsonrpcRequest) {
	var params integrationMethodParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.Fingerprint == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: fingerprint required"})
		return
	}
	mode, ok := hostmode.Parse(params.Method)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: method must be raw, script or helper"})
		return
	}

	// The host record owns the answer for saved and typed connections alike
	// (ADR-727g63tr decision 1). Refuse before granting consent if no store
	// can retain the chosen mode.
	if h.modes == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Host mode store not available (no store wired)"})
		return
	}
	// The machine grant, by fingerprint (ADR-0034) — only for helper.
	// Raw and script write nothing here: the two questions ("which method
	// this host uses" and "may a binary be deployed here") stay unmerged.
	if mode == hostmode.Helper {
		if h.granter == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Helper consent not available (no granter wired)"})
			return
		}
		if err := h.granter.Grant(params.Fingerprint); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Record helper consent failed"})
			return
		}
	}
	// Consent and mode are separate facts (ADR-0105). Grant first: a failed
	// consent write must never leave a durable helper mode without consent.
	recordErr := h.modes.Set(params.Fingerprint, mode)

	// A failed host-record write still applies the answer for this run.
	// This is success with hostRecordSaved=false, so the renderer can retry
	// the open and explain that the host will ask again after restart.
	if recordErr != nil {
		// The detail is the developer's (a document store that could not
		// write); the person gets the sentence that tells them what it means
		// for them. Never the store's own error text in a pane.
		h.warn("the host's record could not be saved; the answer stands for this run",
			"fingerprint", params.Fingerprint, "mode", params.Method, "error", recordErr)
	}

	_ = h.r.TryResult(req.ID, mustMarshal(integrationMethodResult{
		Fingerprint:     params.Fingerprint,
		Method:          params.Method,
		HostRecordSaved: recordErr == nil,
	}))
}

// warn writes one line about a write that failed while the answer stood. The
// handler holds the logger for the developer who has to find the store;
// the response carries only whether the record was saved.
func (h integrationMethodHandlers) warn(msg string, args ...any) {
	if h.log != nil {
		h.log.Warn(msg, args...)
	}
}

// validateIntegrationMethodRaw is the registered validator for
// connections.setIntegrationMethod: fingerprint and method are the write's
// whole identity, so a request naming neither is refused before any store is
// touched.
func validateIntegrationMethodRaw(raw json.RawMessage) string {
	var p integrationMethodParams
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
	if _, ok := hostmode.Parse(p.Method); !ok {
		return "method must be raw, script or helper"
	}
	if p.Host != "" {
		if msg := validateStringBound("host", p.Host, maxHostRunes); msg != "" {
			return msg
		}
	}
	return ""
}
