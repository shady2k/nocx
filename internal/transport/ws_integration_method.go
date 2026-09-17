package transport

// connections.setIntegrationMethod — the write half of the connect-time ask
// (ADR-0069, superseding the yes/no shape of ADR-0068's connections.
// helperConsent). The renderer's one consent dialog (host-key-dialog.tsx)
// calls this after a person chooses an integration method for an open
// failure's ask (session_open_helper_consent.go): raw, script or helper —
// never auto, which is not an answer (ADR-0033).
//
// The answer is written to the SAME place the connection editor writes it —
// profileId's desiredMode, through capability.ConfigService.PatchProfile,
// the exact path profiles.patch uses — so this method is never a second
// owner of what a connection's method is (AD-8). A hand-typed connection
// with no saved profile has nowhere to keep the answer; profileId is then
// absent, nothing is patched, and the renderer carries the choice on the
// very open it retries instead (open.params.schema.json's desiredMode).
//
// Choosing helper ALSO records the machine's consent to deploy the binary,
// by fingerprint (ADR-0034, internal/helper/consent) — the same store the
// connect-time decision (openHoldingLease) and the git-lane selection both
// read. Choosing raw or script writes NOTHING to that store: a fingerprint
// record answers "may a binary be deployed here", never "which method this
// connection uses" — the two questions stay unmerged (ADR-0034's own
// rationale, carried forward by ADR-0069).

import (
	"context"
	"encoding/json"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/profile"
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

// integrationMethodParams is the payload of "connections.setIntegrationMethod":
// the fingerprint the ask named (echoed from the open failure's
// helperConsentData), the chosen method, and — when the answer belongs to a
// saved connection — the profile it is written to. Host travels for the log
// line only.
type integrationMethodParams struct {
	Fingerprint string `json:"fingerprint"`
	Host        string `json:"host,omitempty"`
	ProfileID   string `json:"profileId,omitempty"`
	Method      string `json:"method"`
}

// integrationMethodResult confirms which fingerprint was answered, with
// which method, and which connection (if any) received the write — so the
// renderer's retry is keyed on the identity the backend actually acted on
// rather than trusting its own copy of the request back.
type integrationMethodResult struct {
	Fingerprint string `json:"fingerprint"`
	Method      string `json:"method"`
	ProfileID   string `json:"profileId,omitempty"`
}

type integrationMethodHandlers struct {
	op      capability.ConfigOperation // nil → config domain not wired
	wired   bool                       // profile repository wired (needed only when ProfileID is set)
	granter HelperConsentGranter       // nil → choosing helper fails
	r       Responder
}

// handleConnectionsSetIntegrationMethod writes the person's answer to the
// connect-time ask.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.setIntegrationMethod","params":{"fingerprint":"SHA256:MKEj…","host":"1.2.3.4:22","profileId":"ssh-1","method":"script"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"fingerprint":"SHA256:MKEj…","method":"script","profileId":"ssh-1"}}
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
	switch profile.DesiredMode(params.Method) {
	case profile.DesiredRaw, profile.DesiredScript, profile.DesiredHelper:
	default:
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: method must be raw, script or helper"})
		return
	}

	// The saved-connection write, through the SAME path profiles.patch
	// uses — never a second writer of desiredMode (AD-8). Absent profileId
	// is legitimate (a hand-typed connection): nothing is patched, and the
	// renderer carries the choice on the retried open instead.
	if params.ProfileID != "" {
		if !h.wired || h.op == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "profiles not available"})
			return
		}
		err := h.op.Run(ctx, func(ctx context.Context, svc capability.ConfigService) error {
			return svc.PatchProfile(params.ProfileID, map[string]any{
				"options.desiredMode": params.Method,
			}, nil)
		})
		if err != nil {
			code := profileMethodErrorCode(err)
			if patchValidationError(err) {
				code = -32602
			}
			_ = h.r.TryError(req.ID, RPCError{Code: code, Message: err.Error()})
			return
		}
	}

	// The machine grant, by fingerprint (ADR-0034) — only for helper.
	// Raw and script write nothing here: the two questions ("which method"
	// and "may a binary be deployed here") stay unmerged.
	if profile.DesiredMode(params.Method) == profile.DesiredHelper {
		if h.granter == nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Helper consent not available (no granter wired)"})
			return
		}
		if err := h.granter.Grant(params.Fingerprint); err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Record helper consent failed: " + err.Error()})
			return
		}
	}

	_ = h.r.TryResult(req.ID, mustMarshal(integrationMethodResult{
		Fingerprint: params.Fingerprint,
		Method:      params.Method,
		ProfileID:   params.ProfileID,
	}))
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
	switch p.Method {
	case "raw", "script", "helper":
	default:
		return "method must be raw, script or helper"
	}
	if p.Host != "" {
		if msg := validateStringBound("host", p.Host, maxHostRunes); msg != "" {
			return msg
		}
	}
	if p.ProfileID != "" {
		if msg := validateStringBound("profileId", p.ProfileID, maxIDRunes); msg != "" {
			return msg
		}
	}
	return ""
}
