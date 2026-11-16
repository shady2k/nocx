package transport

// connections.hostMode and connections.setHostMode — the connection editor's
// read and write of the HOST's integration method (ADR-727g63tr decision 1).
//
// THE METHOD IS THE HOST'S, so a saved connection has no copy to edit. The
// editor shows a connection's host and the method that host uses, and changing
// it changes what every later open of that host does. That is what makes the
// read and the write packet-shaped around a PROFILE at all: a profile is how a
// person names a host in this product, and the backend resolves it exactly as
// an open would — alias, port and jump route through ~/.ssh/config — to the
// fingerprints known_hosts holds for that destination.
//
// THE WRITE IS NOT THE ASK. connections.setIntegrationMethod records the answer
// a person gave the connect-time ask, and choosing helper there also grants the
// machine's consent (ADR-0034). This write records a method for a host whose key
// is already trusted; it grants nothing. A host set to helper here still meets
// the one ask at connect, because permission to deploy a binary is the
// machine's question and no editor answers it (ADR-0105, ADR-0068).
//
// A HOST WITH NO KNOWN KEY HAS NO RECORD TO HOLD AN ANSWER. Auto is the only
// method such a host can be shown and the only one it can be set to: the record
// is keyed by a fingerprint and a destination nobody has trusted has none. A
// write naming any other method is refused with the sentence that says why,
// rather than kept somewhere a second answer could live.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// HostKeyIndex answers which host-key fingerprints this machine's known_hosts
// holds for a destination, resolved as a dial resolves it. internal/app
// implements it over internal/ssh's own reader; the interface lives here, at
// the consumer, per the repo's DI convention.
type HostKeyIndex interface {
	FingerprintsFor(ctx context.Context, host string, cfg *ssh.ConnectConfig) ([]string, error)
}

// WithHostKeyIndex attaches the host-key index connections.hostMode reads. Not
// wired means the read and the write answer an error naming that: the record is
// keyed by a fingerprint, and a handler that invented one would write an answer
// nothing will look up again.
func WithHostKeyIndex(index HostKeyIndex) WSServerOption {
	return func(s *WSServer) { s.hostKeyIndex = index }
}

// hostModeParams is the payload of both methods: the saved connection whose
// host is being read, and — for the write — the method asked for.
type hostModeParams struct {
	ProfileID string `json:"profileId"`
	Mode      string `json:"mode,omitempty"`
}

// hostModeResult is what both methods answer: the connection named, the method
// in force for its host after the call, and — on a write that stands for this
// run but could not be persisted — the sentence that says so.
type hostModeResult struct {
	ProfileID string `json:"profileId"`
	Mode      string `json:"mode"`
	Note      string `json:"note,omitempty"`
}

type hostModeHandlers struct {
	resolver *resolverHolder        // nil entry → no profile resolver wired
	index    HostKeyIndex           // nil → known_hosts cannot be asked
	modes    *hostmode.Store        // nil → nothing may be recorded
	r        Responder
	log      log.Logger
}

// hostModeUnanswered is the sentence a write answers when the destination has
// no trusted key yet. It says what to do rather than what failed: the state is
// ordinary for a connection nobody has opened, and the remedy is one connect.
const hostModeUnanswered = "This host's key is not known yet, so nocx cannot remember a method for it. Connect to it once, then choose."

// hostModeNotSaved is the sentence a write answers when the record could not be
// persisted. The method stands for this run — hostmode.Store keeps it — and the
// sentence says what that means after a restart, in the same words the ask uses.
const hostModeNotSaved = "The method applies to this host, but the host's record was not saved, so this host will be asked again after a restart."

// handleHostMode answers what method the connection's host uses.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.hostMode","params":{"profileId":"ssh-1"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"profileId":"ssh-1","mode":"script"}}
func (h hostModeHandlers) handleHostMode(ctx context.Context, req jsonrpcRequest) {
	var params hostModeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.ProfileID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
		return
	}
	fingerprints, code, msg := h.fingerprints(ctx, params.ProfileID)
	if msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: code, Message: msg})
		return
	}
	mode := hostmode.Auto
	for _, fingerprint := range fingerprints {
		if recorded, ok := h.modes.Lookup(fingerprint); ok {
			mode = recorded
			break
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(hostModeResult{ProfileID: params.ProfileID, Mode: string(mode)}))
}

// handleSetHostMode records the method a person chose for the connection's
// host. Auto means "ask me again", so it forgets the host's record instead of
// writing a fourth value (ADR-0033).
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.setHostMode","params":{"profileId":"ssh-1","mode":"raw"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"profileId":"ssh-1","mode":"raw"}}
func (h hostModeHandlers) handleSetHostMode(ctx context.Context, req jsonrpcRequest) {
	var params hostModeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.ProfileID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
		return
	}
	mode, ok := parseHostMode(params.Mode)
	if !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: mode must be auto, raw, script or helper"})
		return
	}
	fingerprints, code, msg := h.fingerprints(ctx, params.ProfileID)
	if msg != "" {
		_ = h.r.TryError(req.ID, RPCError{Code: code, Message: msg})
		return
	}
	if len(fingerprints) == 0 {
		// No key, no record, and auto is already the answer.
		if mode != hostmode.Auto {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: hostModeUnanswered})
			return
		}
		_ = h.r.TryResult(req.ID, mustMarshal(hostModeResult{ProfileID: params.ProfileID, Mode: string(mode)}))
		return
	}

	// EVERY KNOWN KEY OF THE DESTINATION, not the first: a host that offers two
	// algorithms has two fingerprints in known_hosts, and the dial decides which
	// one it meets. An answer written for one of them would be found on the day
	// the other was negotiated and lost on the next.
	var note string
	for _, fingerprint := range fingerprints {
		var err error
		if mode == hostmode.Auto {
			err = h.modes.Remove(fingerprint)
		} else {
			err = h.modes.Set(fingerprint, mode)
		}
		switch {
		case err == nil:
		case errors.Is(err, hostmode.ErrNotSaved):
			note = hostModeNotSaved
			h.warn("the host's method could not be saved; it stands for this run",
				"profile", params.ProfileID, "fingerprint", fingerprint, "mode", params.Mode, "error", err)
		default:
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Record the host's method failed: " + err.Error()})
			return
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(hostModeResult{ProfileID: params.ProfileID, Mode: string(mode), Note: note}))
}

// fingerprints resolves the connection and asks known_hosts which keys its
// destination is stored under. The returned code and message are a ready-made
// refusal; an empty message means the call may proceed.
func (h hostModeHandlers) fingerprints(ctx context.Context, profileID string) ([]string, int, string) {
	if h.modes == nil {
		return nil, -32603, "Host mode store not available (no store wired)"
	}
	resolver, ok := h.resolver.get()
	if !ok {
		return nil, -32603, "SSH sessions not available (no profile resolver wired)"
	}
	if h.index == nil {
		return nil, -32603, "Host keys not available (no known_hosts reader wired)"
	}
	host, cfg, err := resolver.Resolve(profileID)
	if err != nil {
		log.From(ctx).Warn("host mode: cannot resolve the connection", "profile", profileID, "error", err)
		return nil, -32602, "Invalid params: connection " + profileID + " cannot be resolved"
	}
	fingerprints, err := h.index.FingerprintsFor(ctx, host, cfg)
	if err != nil {
		// A store this machine cannot read is a real fault, but it is not an
		// answer: nothing is known about the host, and the record's own
		// fail-closed rule is "no record" (hostmode.Store). The dial refuses the
		// same store when a connection is actually opened, which is where the
		// person can act on it.
		h.warn("host mode: known_hosts cannot be read", "profile", profileID, "error", err)
		return nil, 0, ""
	}
	return fingerprints, 0, ""
}

func (h hostModeHandlers) warn(msg string, args ...any) {
	if h.log != nil {
		h.log.Warn(msg, args...)
	}
}

// parseHostMode reads the editor's closed set, which includes auto — the
// absence of a record — where hostmode.Parse deliberately does not.
func parseHostMode(s string) (hostmode.Mode, bool) {
	if s == string(hostmode.Auto) {
		return hostmode.Auto, true
	}
	return hostmode.Parse(s)
}

// validateHostModeReadRaw is the registered validator for connections.hostMode.
func validateHostModeReadRaw(raw json.RawMessage) string {
	return validateHostModeRaw(raw, false)
}

// validateHostModeWriteRaw is the registered validator for
// connections.setHostMode: the mode is the write's whole value, so a request
// naming none is refused before any store is touched.
func validateHostModeWriteRaw(raw json.RawMessage) string {
	return validateHostModeRaw(raw, true)
}

func validateHostModeRaw(raw json.RawMessage, write bool) string {
	var p hostModeParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.ProfileID == "" {
		return "profileId is required"
	}
	if msg := validateStringBound("profileId", p.ProfileID, maxIDRunes); msg != "" {
		return msg
	}
	if !write {
		return ""
	}
	if _, ok := parseHostMode(p.Mode); !ok {
		return "mode must be auto, raw, script or helper"
	}
	return ""
}
