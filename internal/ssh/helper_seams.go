package ssh

// Two seams this package owes a caller that has no *gossh.Client to hand it:
// answering what a host key IS, and signing a challenge with a stored key.
//
// Both exist for the same reason and neither is a new mechanism. A helper that
// dials the connection cannot consult this package's dial path — it has no
// known_hosts file to read (`ssh/knownhosts` is a forbidden import in the
// deployed artifact, internal/helper/deploy/dependency_test.go) and it must
// never hold key material at rest — so the two questions its handshake needs
// answered are answered here, by the code that already answers them, and cross
// to the helper as ordinary op results (proto/ssh_service.go).
//
// # What is deliberately NOT here
//
// There is no seam that hands out a gossh.HostKeyCallback, and no seam that
// hands out a *gossh.ClientConfig. Both would move the DECISION into the
// helper: a callback decides "accept or refuse", and the coordinator's answer
// to a host key it has never seen is a question for a person, not a predicate
// the helper can run. What crosses instead is a verdict — trusted, changed,
// unknown (proto.HostKeyVerdict) — and the helper acts on the answer rather
// than on a rule.

import (
	"context"
	"crypto/rand"
	"fmt"

	gossh "golang.org/x/crypto/ssh"
)

// addr is a net.Addr for a caller that only ever had the address STRING.
//
// knownhosts needs one — its check splits the peer address to build the
// second, IP-keyed lookup it tries after the hostname one — so a check given
// nil panics rather than refusing, which is how this type came to exist. The
// string IS the address: for a direct route the dial address and the peer
// address spell the same host and port, and a caller that resolved a route
// passes exactly what it will dial (and look up) under.
type addrString string

func (a addrString) Network() string { return "tcp" }
func (a addrString) String() string  { return string(a) }

// CheckHostKey answers how the offered key stands against this client's
// known_hosts, in the vocabulary this package already uses for it:
//
//	nil                  the key is the one recorded for addr (trusted)
//	*ErrUnknownHostKey   nothing is recorded for addr
//	*ErrHostKeyMismatch  a key IS recorded, and this is a different one
//
// The two error types carry the evidence a person is shown — the offered
// fingerprint, the recorded one, and the offered key's wire bytes so the
// accept path can write without re-probing — and they are the SAME values the
// dial path produces, because this calls the same callback the dial path
// installs (hostKeyCallbackFor). A second derivation of "is this the key we
// recorded" would be the defect AD-8 names, and it would be the one place in
// this program where the answer could be wrong in the quiet direction.
//
// addr is the STORAGE identity, not the dial address: a route through a jump
// host records its key under an opaque digest (knownHostsTargetAddr), so the
// caller that knows the route passes the identity the next connection will
// look up. A direct route passes its own address, which is what the
// coordinator does today.
//
// A malformed key blob is an error and not a verdict: it means the peer sent
// something that is not a public key, which is a fact about this handshake and
// not about the trust store.
func (rc *RealClient) CheckHostKey(addr string, keyBlob []byte) error {
	key, err := gossh.ParsePublicKey(keyBlob)
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}
	cb, err := rc.hostKeyCallbackFor(knownHostsTargetAddr(addr, nil))
	if err != nil {
		return fmt.Errorf("host key callback: %w", err)
	}
	// The peer address is the same string, and it has to be a real net.Addr:
	// see addrString for what a nil one costs.
	return cb(addr, addrString(addr), key)
}

// DialAuth dials addr, performs the handshake with a CALLER-SUPPLIED
// configuration, and answers the authenticated client. The client owns the
// connection and must close it.
//
// cfg is the caller's in full — its Auth and its HostKeyCallback in particular
// — because this is the seam a caller that decides both uses: the helper's
// probe has no known_hosts to consult and no stored credential to build a
// chain from, and it must not be given either. What this method contributes is
// the DIAL itself: context cancellation that actually ends a handshake
// (x/crypto/ssh has no context-aware form, so the watchdog is what makes
// cancellation real), and the classification of an authentication refusal as
// *ErrAuthFailed — which is what makes an auth failure one fact instead of two
// processes' worth of string matching.
//
// host and user are what a refusal is reported AGAINST, and nothing else: they
// do not select, resolve or authorize anything. Resolution and authorization
// stay with the caller (plan §3), so the address this dials is exactly the
// address it was given.
func (rc *RealClient) DialAuth(ctx context.Context, addr, host, user string, cfg *gossh.ClientConfig) (*gossh.Client, error) {
	return (&dialer{client: rc}).dialDirect(ctx, addr, cfg, host, user)
}

// SignWithStoredKey signs challenge with the private key a config's
// KeySecretID names, answering the signature in the wire encoding of an ssh
// signature (string format, string blob) — the form x/crypto/ssh's own
// Unmarshal reads back into a Signature, so the party that asked can hand it
// to the library unchanged.
//
// cfg carries the material references and nothing else that matters here:
// KeySecretID, PassphraseSecretID when the key is encrypted, and Secrets, the
// stanced resolver. The signer is built by the same function the dial path
// builds it with (storedKeySigner), so the passphrase stays inside the vault's
// Use callback, the sealed-vault error propagates as itself, and an encrypted
// key with no passphrase is *ErrEncryptedKey rather than a parse failure.
//
// ctx is the caller's, and it is not decoration: the read is STANCED, so a
// sealed vault raises the unlock a person answers through, and that wait is
// bounded by whatever the caller is willing to wait for.
//
// The key's bytes never leave this call: what returns is a signature, which is
// what a signature is for. The ALGORITHM comes back beside it because the
// caller asked for one and the key is the only party that can settle whether
// they agree — a signature made with a key other than the one named is a
// signature for a handshake that never happened.
func (rc *RealClient) SignWithStoredKey(ctx context.Context, cfg *ConnectConfig, challenge []byte) (signature []byte, algorithm string, err error) {
	signer, err := rc.storedKeySigner(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	sig, err := signer.Sign(rand.Reader, challenge)
	if err != nil {
		return nil, "", fmt.Errorf("sign with the stored key: %w", err)
	}
	return gossh.Marshal(sig), signer.PublicKey().Type(), nil
}
