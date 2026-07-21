package ssh

import (
	"errors"
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

// ErrHostKeyMismatch is returned when the host key presented by the remote
// does not match the one recorded in known_hosts.
type ErrHostKeyMismatch struct {
	Addr        string
	Fingerprint string
	Expected    string
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

// Sentinel errors used internally.
var (
	errNoAuthMethods = errors.New("no usable auth methods")
)
