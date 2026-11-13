package ssh

import (
	"fmt"
)

// Domain error markers for the SSH package. Each wraps a distinguishable
// type the UI layer can switch on to surface the right user-facing action
// (e.g. "unknown host — add to known_hosts?", "wrong key — try another?").

// ErrAuthFailed is returned when none of the supplied auth methods succeeded.
type ErrAuthFailed struct {
	User string
	Host string
	Err  error
}

func (e *ErrAuthFailed) Error() string {
	return fmt.Sprintf("ssh authentication failed for %s@%s: %v", e.User, e.Host, e.Err)
}

func (e *ErrAuthFailed) Unwrap() error { return e.Err }

// ErrAuthRejected is the far side's DEFINITIVE answer that the credential it was
// offered is not one it accepts, rebuilt on this side of the helper's wire.
//
// It exists beside ErrAuthFailed rather than instead of it, and the difference
// is what a caller DOES with it: ErrAuthFailed says "none of the methods this
// process tried succeeded", which a retry can sometimes change (a transient
// transport failure inside the handshake reaches it too); ErrAuthRejected says
// "the server answered no", which no second attempt can change. `no supported
// methods remain` is the server saying stop, and a terminal that turns one
// gesture into four handshakes can get a person locked out of their own host by
// fail2ban, MaxAuthTries or pam_faillock (nocx-1ebrs.18).
//
// Error() is the sentence the helper sent, verbatim, because the person reading
// it is reading the far side's own account of what it would not accept.
type ErrAuthRejected struct {
	// Host and User name the destination the answer is about. They are the
	// coordinator's resolved values and not a second parse of the message.
	Host string
	User string
	// Reason is the helper's sentence.
	Reason string
}

func (e *ErrAuthRejected) Error() string { return e.Reason }

// ErrNoAuthMethod is returned when the connection had nothing to authenticate
// with: the auth method it names has no material behind it. It is a
// configuration answer, not a server one, and it is deliberately separate from
// ErrAuthFailed — the server rejecting a password and nocx never sending one
// are different problems with different fixes, and reporting the second as the
// first sends the user to look at the host.
//
// Reported from the running app: a connection set to publicKey whose
// credential held no key produced "attempted methods [none], no supported
// methods remain" — the Go library naming its own internals to a user whose
// actual problem was an empty credential.
type ErrNoAuthMethod struct {
	User string
	Host string
	// Mode is the auth method the connection names: publicKey, password,
	// agent, keyboardInteractive, or "" for auto.
	Mode string
}

func (e *ErrNoAuthMethod) Error() string {
	switch e.Mode {
	case "publicKey":
		return fmt.Sprintf(
			"no private key to offer %s@%s: this connection is set to key authentication and no key is stored for it",
			e.User, e.Host,
		)
	case "password":
		return fmt.Sprintf(
			"no password to offer %s@%s: this connection is set to password authentication and no password is stored for it",
			e.User, e.Host,
		)
	case "agent":
		return fmt.Sprintf(
			"no agent to ask for %s@%s: this connection is set to agent authentication and no SSH agent is reachable (SSH_AUTH_SOCK is unset or empty)",
			e.User, e.Host,
		)
	default:
		return fmt.Sprintf(
			"nothing to authenticate %s@%s with: no key, no password and no reachable agent",
			e.User, e.Host,
		)
	}
}

// ErrHostKeyMismatch is returned when the host key presented by the remote
// does not match the one recorded in known_hosts.
type ErrHostKeyMismatch struct {
	Addr string
	// KnownHostsAddr is the storage identity used for this route. It equals
	// Addr for direct connections and is an opaque route identity for a
	// target reached through one or more jump hosts.
	KnownHostsAddr string
	KeyAlgo        string
	Fingerprint    string
	// Expected holds the fingerprint(s) recorded in known_hosts. A host
	// with several entries (key rotation) yields a comma-joined list.
	Expected string
	// Key is the wire-format marshalled offered public key — public
	// material, carried so the accept path can append it to known_hosts
	// without re-probing.
	Key []byte
}

func (e *ErrHostKeyMismatch) Error() string {
	return fmt.Sprintf("host key mismatch for %s: got %s, expected %s",
		e.Addr, e.Fingerprint, e.Expected)
}

// ErrUnknownHostKey is returned when the remote host is not present in
// known_hosts at all. The UI should prompt the user to accept and add it.
type ErrUnknownHostKey struct {
	Addr    string
	KeyAlgo string
	// KnownHostsAddr is the storage identity used for this route. It equals
	// Addr for direct connections and is an opaque route identity for a
	// target reached through one or more jump hosts.
	KnownHostsAddr string
	Fingerprint    string
	// Key is the wire-format marshalled offered public key — public
	// material, carried so the accept path can append it to known_hosts
	// without re-probing.
	Key []byte
}

func (e *ErrUnknownHostKey) Error() string {
	return fmt.Sprintf("unknown host key for %s: %s %s",
		e.Addr, e.KeyAlgo, e.Fingerprint)
}

// ErrEncryptedKey is returned when a private key requires a passphrase.
type ErrEncryptedKey struct {
	Path string
}

func (e *ErrEncryptedKey) Error() string {
	return fmt.Sprintf("private key %s is encrypted and requires a passphrase (not supported)", e.Path)
}

// ErrCredentialAuthorizationFailed is returned when a linked credential is not
// authorized for the resolved endpoint. The credential may only be spent on the
// endpoint its profile identifies (computed authorization, replacing the old
// host-bound check). Matching uses the resolved hostname and effective port
// after ~/.ssh/config merge — never the alias the renderer chose.
type ErrCredentialAuthorizationFailed struct {
	CredentialID string
	Expected     string // endpoint the credential is authorized for
	ResolvedHost string
	ResolvedPort int
	Jump         bool
}

func (e *ErrCredentialAuthorizationFailed) Error() string {
	hop := "target"
	if e.Jump {
		hop = "jump host"
	}
	return fmt.Sprintf(
		"credential %s is authorized for %s but the %s resolves to %s:%d — refusing to submit it",
		e.CredentialID, e.Expected, hop, e.ResolvedHost, e.ResolvedPort,
	)
}
