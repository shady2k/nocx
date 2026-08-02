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
	Addr        string
	KeyAlgo     string
	Fingerprint string
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
	Addr        string
	KeyAlgo     string
	Fingerprint string
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

// ErrDisconnected is returned when an operation is attempted on a channel
// whose underlying SSH connection has been closed. It is distinguishable
// from a transient network error — this channel is permanently dead.
// Callers can check for it with errors.As.
type ErrDisconnected struct{}

func (e *ErrDisconnected) Error() string {
	return "ssh channel disconnected"
}
