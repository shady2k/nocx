package transport

// The connect-time helper ask (ADR-0068, and the owner's decision of
// 2026-09-16 that settles the gap ADR-0068's prose leaves).
//
// ADR-0068 decided the MOMENT: an auto connection (ADR-0033) is asked about
// the helper when it connects, keyed by the destination's host-key
// fingerprint (ADR-0034), never by a feature surface reaching for it. The
// owner's decision settles what ADR-0068 left open — that the ask is ONE
// connect-time surface, not merely an extension of the host-key dialog,
// because a host whose key is already trusted raises no host-key dialog at
// all, and hanging the question off that dialog alone would leave every
// already-trusted host at auto forever, unasked.
//
// So there are two shapes this ask reaches the wire in, and this file is the
// one place both are built, because a caller that built its own would be a
// second owner of what the refusal looks like (AD-8):
//
//   - the key is already trusted: nothing about the host key rides the
//     refusal, and the dialog asks about the helper alone.
//   - the key is ALSO unknown or changed: the dial that discovered that is
//     the same dial the helper-tier decision was waiting on (openHoldingLease
//     probes the destination before it ever asks about the helper), so the
//     host-key evidence and the helper ask arrive in the SAME refusal —
//     never two dialogs in sequence, which is the owner's explicit rule.
//
// ErrHelperConsentNeeded is what internal/app's connect-time decision
// (helper_git.go's openHoldingLease) returns instead of silently falling
// through to a script-tier open when an auto connection's fingerprint has no
// consent record (internal/helper/consent). It travels up through
// sessionOpener.Open exactly like any other refusal the open path produces
// itself, and answerOpenFailure is where it is turned into the wire shape —
// reusing hostKeyInfoFromError for the nested host-key evidence rather than
// classifying the ssh error a second time.

import (
	"errors"
)

// ErrHelperConsentNeeded is the open path's own signal that a person must
// answer for this destination's helper before the connect may proceed.
//
// Cause, when non-nil, is the host-key error the dial that discovered the
// need for this ask actually produced (an *ssh.ErrUnknownHostKey or
// *ssh.ErrHostKeyMismatch) — carried so hostKeyInfoFromError can build the
// same host-key evidence the probe-time and open-time host-key dialogs
// already show, without a second reader of those error types. Nil means the
// key is already trusted and only the helper question remains.
type ErrHelperConsentNeeded struct {
	// Host is the destination as the open path named it (session.Config's
	// Host), for the sentence a person reads and for the wire's host field.
	Host string
	// Fingerprint is the identity the answer is keyed by (ADR-0034): the
	// TRUSTED fingerprint when Cause is nil, and the OFFERED fingerprint —
	// deterministic from the key bytes alone, independent of trust — when
	// Cause carries an unknown or changed key. The same value is what
	// connections.setIntegrationMethod is asked to write the answer under.
	Fingerprint string
	Cause       error
}

func (e *ErrHelperConsentNeeded) Error() string {
	if e.Cause != nil {
		return "a person must decide before nocx may use its helper on " + e.Host + ": " + e.Cause.Error()
	}
	return "nocx has not been told whether it may use its helper on " + e.Host
}

func (e *ErrHelperConsentNeeded) Unwrap() error { return e.Cause }

// NewHelperConsentNeeded builds the signal for the connect-time ask.
// internal/app constructs it directly — every field is exported and the
// type is the one owner of the wire shape it becomes, built in
// answerOpenFailure below — rather than through an openRefusal, because this
// refusal is decided inside the helper opener (internal/app), a package
// below transport in the open path's own layering (session_open.go's file
// header), and openRefusal's carrier is unexported.
func NewHelperConsentNeeded(host, fingerprint string, cause error) error {
	return &ErrHelperConsentNeeded{Host: host, Fingerprint: fingerprint, Cause: cause}
}

// helperConsentData is the open error's data field for the connect-time ask.
// It is deliberately NOT the connections.probe hostKey shape reused at the
// top level: that shape requires knownHostsHost, algorithm and key, which
// are unknown when the key is already trusted and only the helper question
// remains — so those five travel NESTED, present only when the key itself
// also needs a trust decision (contracts/open.helperConsent.schema.json).
type helperConsentData struct {
	Host        string                  `json:"host"`
	Fingerprint string                  `json:"fingerprint"`
	HelperAsk   bool                    `json:"helperAsk"`
	HostKey     *connectionsTestHostKey `json:"hostKey,omitempty"`
}

// answerHelperConsentNeeded answers an open failed by ErrHelperConsentNeeded:
// the sentence a person reads, plus the machine-readable data the renderer's
// one consent dialog keys on (host-key-dialog.tsx). Returns false when err is
// not this refusal, so the caller falls through to its ordinary taxonomy.
func answerHelperConsentNeeded(r Responder, req jsonrpcRequest, err error) bool {
	var ask *ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		return false
	}
	resp := newJSONRPCError(req.ID, -32603, ask.Error())
	resp.Error.Data = helperConsentData{
		Host:        ask.Host,
		Fingerprint: ask.Fingerprint,
		HelperAsk:   true,
		HostKey:     hostKeyInfoFromError(ask.Cause),
	}
	_ = respond(r, resp)
	return true
}
