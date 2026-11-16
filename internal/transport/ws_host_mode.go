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

// hostModeReadResult is what connections.hostMode answers: the connection
// named, the method its host keeps, and whether this machine has any key for
// that host at all. The last field exists because auto alone cannot tell
// "nobody answered" from "never connected" — the editor shows two different
// states for them (contracts/connections.hostMode.schema.json).
type hostModeReadResult struct {
	ProfileID string `json:"profileId"`
	Mode      string `json:"mode"`
	HostKnown bool   `json:"hostKnown"`
}

// hostModeWriteResult is what connections.setHostMode answers once the record
// was written. There is deliberately no "note": a write that could not be
// persisted is an error with the reason, not a success with a caveat, because
// nothing about the host changes and the editor's Save can be retried.
type hostModeWriteResult struct {
	ProfileID string `json:"profileId"`
	Mode      string `json:"mode"`
}

type hostModeHandlers struct {
	resolver *resolverHolder // nil entry → no profile resolver wired
	index    HostKeyIndex    // nil → known_hosts cannot be asked
	modes    *hostmode.Store // nil → nothing may be recorded
	r        Responder
	log      log.Logger
}

// hostModeUnanswered is the sentence a write answers when the destination has
// no trusted key yet. It says what to do rather than what failed: the state is
// ordinary for a connection nobody has opened, and the remedy is one connect.
const hostModeUnanswered = "This host's key is not known yet, so nocx cannot remember a method for it. Connect to it once, then choose."

// hostModeNotSaved is the reason a write answers when the record could not be
// persisted. It is a FRAGMENT, not a sentence: the editor puts it after "Not
// saved: " under the field, and inside the split-outcome toast that names the
// host — the same words in both places, composed by the surface that has the
// host name. A failed write changes nothing (hostmode.Store.Persist), so the
// host keeps the method it had and the editor's Save can be retried.
const hostModeNotSaved = "the host's settings could not be written"

// hostModeUnreadable is the reason both methods answer when this machine's
// known_hosts cannot be read. It is a fragment for the same reason: the read
// failure is the field's error after "Could not read this machine's method: ".
// The fail-closed answer (auto) is not available here — a default shown in
// place of an unread value is the soft degrade the interface rules forbid.
const hostModeUnreadable = "the known_hosts file could not be read"

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
	_ = h.r.TryResult(req.ID, mustMarshal(hostModeReadResult{
		ProfileID: params.ProfileID,
		Mode:      string(mode),
		HostKnown: len(fingerprints) > 0,
	}))
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
		_ = h.r.TryResult(req.ID, mustMarshal(hostModeWriteResult{ProfileID: params.ProfileID, Mode: string(mode)}))
		return
	}

	// EVERY KNOWN KEY OF THE DESTINATION, not the first: a host that offers two
	// algorithms has two fingerprints in known_hosts, and the dial decides which
	// one it meets. An answer written for one of them would be found on the day
	// the other was negotiated and lost on the next.
	//
	// THE WRITE IS THE EDITOR'S, NOT THE ASK'S. Persist and Remove change the
	// host only when the document was written, so a failure here is a refusal
	// the form reports and retries — never an answer kept quietly in memory
	// while the field says it was not saved.
	for _, fingerprint := range fingerprints {
		var err error
		if mode == hostmode.Auto {
			err = h.modes.Remove(fingerprint)
		} else {
			err = h.modes.Persist(fingerprint, mode)
		}
		switch {
		case err == nil:
		case errors.Is(err, hostmode.ErrNotSaved):
			h.warn("the host's method could not be saved; nothing about the host changed",
				"profile", params.ProfileID, "fingerprint", fingerprint, "mode", params.Mode, "error", err)
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: hostModeNotSaved})
			return
		default:
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Record the host's method failed: " + err.Error()})
			return
		}
	}
	_ = h.r.TryResult(req.ID, mustMarshal(hostModeWriteResult{ProfileID: params.ProfileID, Mode: string(mode)}))
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
		// A reason, not a sentence: the editor's read-failed state puts it
		// after "Could not read this machine's method: " and its write path
		// puts it after "Not saved: ". The connection id stays in the log;
		// it is not something a person reads here.
		return nil, -32602, "the connection's address could not be resolved"
	}
	fingerprints, err := h.index.FingerprintsFor(ctx, host, cfg)
	if err != nil {
		// A store this machine cannot read is not an answer. Answering auto
		// would show a default as if it had been read (interface-rules.md): the
		// editor's read-failed state exists for exactly this, and it shows the
		// reason under the field with Retry beside it. A MISSING known_hosts is
		// not this case — internal/ssh answers it as no keys, which is the
		// honest "never connected".
		h.warn("host mode: known_hosts cannot be read", "profile", profileID, "error", err)
		return nil, -32603, hostModeUnreadable
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
