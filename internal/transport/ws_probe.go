package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// ---------------------------------------------------------------------------
// ProbeOutcome — re-exported from ssh package for convenience
// ---------------------------------------------------------------------------

// ProbeOutcome is a closed-enum outcome for a single-profile credential
// probe. It is defined in the ssh package alongside the error types it
// classifies; re-exported here so existing transport consumers do not
// need to change.
type ProbeOutcome = ssh.ProbeOutcome

const (
	OutcomeAccepted         = ssh.OutcomeAccepted
	OutcomeRejected         = ssh.OutcomeRejected
	OutcomeUnreachable      = ssh.OutcomeUnreachable
	OutcomeHostKeyUnknown   = ssh.OutcomeHostKeyUnknown
	OutcomeHostKeyChanged   = ssh.OutcomeHostKeyChanged
	OutcomeNeedsInteractive = ssh.OutcomeNeedsInteractive
)

// ---------------------------------------------------------------------------
// Prober — narrow interface for credential validation
// ---------------------------------------------------------------------------

// Prober performs a forced-fresh credential probe for a resolved profile.
//
// host is the dial-target hostname from the resolver; cfg is the resolved
// ConnectConfig — the probe must use exactly the parameters Connect would
// (same user, port, timeout, secret references, authorized endpoint).
//
// The single implementation asks THIS MACHINE'S HELPER (app.sshOverHelper,
// nocx-50w7p.10: every dial is the helper's, and the coordinator's own probe —
// ssh.RealClient.ProbeConfig — was deleted with the dials). It always answers
// the observed fingerprint, which is why there is one method and not two: the
// leg that dropped it had no caller, and a seam with an unused half is a seam
// that hides which questions are actually being asked.
//
// Defined here (consumer package) per the repo's DI convention.
type Prober interface {
	// ProbeWithResult validates the credentials and answers the host-key
	// fingerprint observed during the SSH handshake. The fingerprint is empty
	// when the handshake fails before host key verification (e.g. unreachable
	// host).
	ProbeWithResult(ctx context.Context, host string, cfg *ssh.ConnectConfig) (fingerprint string, err error)
}

// WithProber attaches a Prober for the connections.test JSON-RPC method.
// When not wired, the handler returns a JSON-RPC error — the probe handler
// does not create clients itself.
func WithProber(p Prober) WSServerOption {
	return func(s *WSServer) { s.prober = p }
}

// ---------------------------------------------------------------------------
// HostKeyTruster — accept-on-first-use for the connections.trustHostKey RPC
// ---------------------------------------------------------------------------

// HostKeyTruster appends a host's offered public key to known_hosts, the
// accept half of accept-on-first-use. The single implementation wraps
// ssh.RealClient.TrustHostKey. Defined here (consumer package) per the repo's
// DI convention, like Prober.
type HostKeyTruster interface {
	// TrustHostKey appends the offered key for addr to known_hosts and
	// returns its SHA256 fingerprint. key is the wire-format marshalled
	// public key, as carried by the connections.test hostKey evidence.
	TrustHostKey(ctx context.Context, addr string, key []byte) (fingerprint string, err error)
}

// WithHostKeyTruster attaches a HostKeyTruster for the
// connections.trustHostKey JSON-RPC method. When not wired, the handler
// returns a JSON-RPC error — accepting a host key is a backend write to
// known_hosts and is never synthesized by the renderer.
func WithHostKeyTruster(t HostKeyTruster) WSServerOption {
	return func(s *WSServer) { s.hostKeyTruster = t }
}

// ---------------------------------------------------------------------------
// connections.test — JSON-RPC types
// ---------------------------------------------------------------------------

// connectionsTestParams is the payload of the "connections.test" RPC call.
type connectionsTestParams struct {
	ProfileID string `json:"profileId"`
}

// connectionsTestResult carries the typed probe outcome, a human-readable
// detail string suitable for the UI to surface, and — for the two host-key
// outcomes — the offered key evidence the renderer needs to show the
// fingerprint and, on accept, echo back to connections.trustHostKey.
type connectionsTestResult struct {
	Outcome ProbeOutcome            `json:"outcome"`
	Detail  string                  `json:"detail,omitempty"`
	HostKey *connectionsTestHostKey `json:"hostKey,omitempty"`
}

// connectionsTestHostKey carries the offered host key for the host-key
// outcomes of connections.test. A host key is public material (ADR-0011 §3),
// so it may cross the wire and be shown; the key blob is echoed back by
// connections.trustHostKey as the accept payload.
type connectionsTestHostKey struct {
	Host              string `json:"host"`
	KnownHostsHost    string `json:"knownHostsHost"`
	Changed           bool   `json:"changed"`
	Algorithm         string `json:"algorithm"`
	Fingerprint       string `json:"fingerprint"`
	StoredFingerprint string `json:"storedFingerprint,omitempty"`
	Key               string `json:"key"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// handleConnectionsTest probes one saved profile and returns a typed outcome.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.test","params":{"profileId":"ssh:p1:1"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"outcome":"accepted"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"outcome":"rejected","detail":"authentication failed for user@host"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"outcome":"unreachable","detail":"dial tcp host:22: connect: connection refused"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"outcome":"host-key-unknown","detail":"unknown host key for 1.2.3.4:22: ecdsa-sha2-nistp256 SHA256:MKEj…","hostKey":{"host":"1.2.3.4:22","algorithm":"ecdsa-sha2-nistp256","fingerprint":"SHA256:MKEj…","key":"AAAAC…"}}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"outcome":"host-key-changed","detail":"host key mismatch for 1.2.3.4:22: got SHA256:MKEj…, expected SHA256:OLd…","hostKey":{"host":"1.2.3.4:22","algorithm":"ecdsa-sha2-nistp256","fingerprint":"SHA256:MKEj…","storedFingerprint":"SHA256:OLd…","key":"AAAAC…"}}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"outcome":"needs-interactive","detail":"private key requires passphrase"}}
//
// probeHandlers answers connections.test. It holds the resolver holder, the
// prober and the probe result store — the seams — and its Responder; nothing
// else. The off-loop machinery (probe admission composed with the lane,
// inflight registration) lives in the registration, not in the handler.
type probeHandlers struct {
	resolver         *resolverHolder
	prober           Prober
	probeResultStore *ProbeResultStore
	r                Responder
}

func (h probeHandlers) handleConnectionsTest(ctx context.Context, req jsonrpcRequest) {
	var params connectionsTestParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.ProfileID == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: profileId required"})
		return
	}
	if _, ok := h.resolver.get(); !ok {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Probing not available (no profile resolver wired)"})
		return
	}
	if h.prober == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Probing not available (no prober wired)"})
		return
	}

	host, connectCfg, err := h.resolver.Resolve(params.ProfileID)
	if err != nil {
		// Resolving reads the stored secret, so a sealed vault surfaces here —
		// the renderer needs the reason to offer the unlock prompt (the vault
		// owns it; no call site wraps its own vault calls).
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "Resolve failed: ", err))
		return
	}

	// The probe runs OFF the read loop under the probe admission
	// (ws_control.go): a 30-second SSH probe must not freeze the socket that
	// feeds every other tab, and a disconnect must be able to cancel it. The
	// task context derives from the connection context, so the disconnect
	// cancels the probe; the admission permit is released when the task
	// returns, cancelled or not — that frees the slot for the next
	// connection. A refused submit (one probe already in flight) answers the
	// control-saturated error.
	// The dispatch admitted this task via the probe submission (the probe
	// admission composed with the lane, registered in the inflight set
	// BEFORE TrySubmit so shutdown cancels it and waits, bounded, for it).
	// The task context derives from the connection, so a disconnect cancels
	// the probe; the admission permit frees when the task returns, cancelled
	// or not — that frees the slot for the next connection. A refusal (one
	// probe already in flight) was answered by the dispatcher with the
	// control-saturated error.
	h.runProbe(ctx, h.r, req.ID, host, connectCfg)
}

// runProbe runs one probe and answers its request. It executes on the probe
// task's goroutine, never the read loop; the Responder is safe from any
// goroutine and silently drops the response when the connection died.
func (h probeHandlers) runProbe(ctx context.Context, wconn Responder, id json.RawMessage, host string, connectCfg *ssh.ConnectConfig) {
	// Probe uses the same resolved parameters Connect would.
	fingerprint, err := h.prober.ProbeWithResult(ctx, host, connectCfg)
	result := classifyProbeError(err)

	// Record the probe result in the store for operational evidence.
	// All classified outcomes (accepted, rejected, unreachable, host-key
	// unknown, host-key changed, needs-interactive) are stored; unclassifiable
	// errors skip the store because they represent a probe bug, not a valid
	// outcome.
	if result.err == nil && h.probeResultStore != nil {
		h.storeProbeResult(host, connectCfg, fingerprint, result.outcome, result.detail)
	}

	if result.err != nil {
		// A sealed vault surfaces here too — the renderer needs the reason to
		// offer the unlock prompt instead of showing an error.
		_ = wconn.TryError(id, rpcErrorFor(-32603, "probe config: ", result.err))
		return
	}

	_ = wconn.TryResult(id, mustMarshal(connectionsTestResult{
		Outcome: result.outcome,
		Detail:  result.detail,
		HostKey: hostKeyInfoFromError(err),
	}))
}

// hostKeyInfoFromError builds the wire host-key evidence from the probe
// error, or nil when the error is not a host-key error (or nil). The offered
// key is public material; the stored fingerprint is present only when the
// key changed, so the renderer can never show a changed key without the
// stored one to compare it against.
func hostKeyInfoFromError(err error) *connectionsTestHostKey {
	var unknownKey *ssh.ErrUnknownHostKey
	if errors.As(err, &unknownKey) {
		return &connectionsTestHostKey{
			Host:           unknownKey.Addr,
			KnownHostsHost: knownHostsAddr(unknownKey.Addr, unknownKey.KnownHostsAddr),
			Changed:        false,
			Algorithm:      unknownKey.KeyAlgo,
			Fingerprint:    unknownKey.Fingerprint,
			Key:            base64.StdEncoding.EncodeToString(unknownKey.Key),
		}
	}
	var keyMismatch *ssh.ErrHostKeyMismatch
	if errors.As(err, &keyMismatch) {
		return &connectionsTestHostKey{
			Host:              keyMismatch.Addr,
			KnownHostsHost:    knownHostsAddr(keyMismatch.Addr, keyMismatch.KnownHostsAddr),
			Changed:           true,
			Algorithm:         keyMismatch.KeyAlgo,
			Fingerprint:       keyMismatch.Fingerprint,
			StoredFingerprint: keyMismatch.Expected,
			Key:               base64.StdEncoding.EncodeToString(keyMismatch.Key),
		}
	}
	return nil
}

func knownHostsAddr(displayAddr, lookupAddr string) string {
	if lookupAddr != "" {
		return lookupAddr
	}
	return displayAddr
}

// storeProbeResult builds a ProbeResultRecord from the probe output and
// stores it. All classified outcomes are recorded.
func (h probeHandlers) storeProbeResult(host string, cfg *ssh.ConnectConfig, fingerprint string, outcome ProbeOutcome, detail string) {
	port := 22
	if cfg.Port > 0 {
		port = cfg.Port
	}
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))

	authPolicy := cfg.AuthMode
	if authPolicy == "" {
		authPolicy = "auto"
	}

	h.probeResultStore.Store(ProbeResultRecord{
		Identity: ProbeResultIdentity{
			Endpoint:           endpoint,
			HostKeyFingerprint: fingerprint,
			Username:           cfg.User,
			AuthPolicy:         authPolicy,
			Timestamp:          time.Now(),
		},
		Outcome:      outcome,
		Detail:       detail,
		CredentialID: cfg.CredentialID,
	})
}

// probeResult is the internal classified outcome.
type probeResult struct {
	outcome ProbeOutcome
	detail  string
	err     error // non-nil only for unclassifiable errors → RPC error
}

// classifyProbeError maps an SSH probe error to a typed outcome.
// Unclassifiable errors are returned as a wrapped error for the RPC
// error path — they are never collapsed into "rejected".
func classifyProbeError(err error) probeResult {
	outcome, detail, e := ssh.ClassifyProbeError(err)
	return probeResult{outcome: outcome, detail: detail, err: e}
}

// ---------------------------------------------------------------------------
// connections.trustHostKey — accept-on-first-use
// ---------------------------------------------------------------------------

// connectionsTrustHostKeyParams is the payload of the
// "connections.trustHostKey" RPC call: the host address and key blob echoed
// verbatim from the connections.test result's hostKey evidence.
type connectionsTrustHostKeyParams struct {
	Host string `json:"host"`
	Key  string `json:"key"`
}

// connectionsTrustHostKeyResult reports the fingerprint of the key appended
// to known_hosts.
type connectionsTrustHostKeyResult struct {
	Fingerprint string `json:"fingerprint"`
}

// trustHostKeyHandlers answers connections.trustHostKey. It holds the
// host-key truster seam and its Responder; nothing else.
type trustHostKeyHandlers struct {
	truster HostKeyTruster
	r       Responder
}

// handleConnectionsTrustHostKey appends the offered host key to known_hosts —
// the accept half of accept-on-first-use. The renderer only ever echoes back
// the host and key that a connections.test result carried; the write itself
// is backend-side (the renderer has no known_hosts).
//
//	--> {"jsonrpc":"2.0","id":1,"method":"connections.trustHostKey","params":{"host":"1.2.3.4:22","key":"AAAAC…"}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"fingerprint":"SHA256:MKEj…"}}
func (h trustHostKeyHandlers) handleConnectionsTrustHostKey(ctx context.Context, req jsonrpcRequest) {
	var params connectionsTrustHostKeyParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if params.Host == "" || params.Key == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: host and key required"})
		return
	}
	if h.truster == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Host key trust not available (no truster wired)"})
		return
	}

	keyBlob, err := base64.StdEncoding.DecodeString(params.Key)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Invalid key: not base64: " + err.Error()})
		return
	}

	fingerprint, err := h.truster.TrustHostKey(ctx, params.Host, keyBlob)
	if err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "Trust host key failed: " + err.Error()})
		return
	}

	_ = h.r.TryResult(req.ID, mustMarshal(connectionsTrustHostKeyResult{
		Fingerprint: fingerprint,
	}))
}
