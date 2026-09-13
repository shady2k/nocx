package ssh

// The credential REFERENCE a coordinator mints and a helper only echoes,
// and the three kinds of thing it can name (nocx-50w7p.11).
//
// # Why a reference at all, and why it has a grammar
//
// The owner's invariant is that no ssh connection happens without the local
// helper, and the helper holds no secret at rest. So the material stays here —
// in the vault, in a key FILE this process reads, in the agent socket only this
// process may reach — and what crosses is a name for it. The helper asks for
// what it needs when the handshake needs it (`sign`, `secret`, `prompt`) and
// never before.
//
// What a key needs is a signature and not the key, which is why an inline key
// file and an agent key cross the wire looking exactly like a vault key: a
// reference, and the PUBLIC half the helper must declare before it is asked to
// sign. The three differ in one place only — what the coordinator does when
// that reference comes back on a `sign` request — and that is what the grammar
// below exists to name.
//
// # The grammar, and why it is not "the vault, unless"
//
// Every reference is namespaced — "vault:<secret id>", "file:<path>",
// "agent:<fingerprint>" — rather than leaving the vault's own opaque id bare
// with a prefix reserved for the two new kinds. A bare id plus a prefix is a
// grammar whose meaning depends on what a secret identifier happens to start
// with, and a vault id that began with "file:" would then be read as a path:
// the parse would still succeed and the wrong thing would be signed. Three
// namespaces and a refusal for anything else cannot fail that way, and the
// helper is unaffected either way — it never looks inside the string.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// CredentialKind is what a reference names, in a closed set.
type CredentialKind string

const (
	// CredentialVault names a secret in the credential store: a password, or
	// the private key of a linked credential.
	CredentialVault CredentialKind = "vault"
	// CredentialFile names a private key FILE on this machine. The file is
	// read and parsed here, at the moment a signature is wanted, and what
	// crosses is never its bytes.
	CredentialFile CredentialKind = "file"
	// CredentialAgent names one key the running ssh-agent holds, by the
	// fingerprint of its public half. The agent's socket stays here, and each
	// signature makes the agent answer for itself.
	CredentialAgent CredentialKind = "agent"
)

// CredentialRef is an opaque handle on one credential, minted by this
// coordinator. The helper echoes it verbatim and never interprets it.
//
// The zero value names nothing, which is how an absent passphrase and an
// absent credential are spelled. It is a struct rather than a string so that
// "which kind" cannot be answered by reading a prefix somewhere else: the
// grammar is parsed once, here, by ParseCredentialRef.
type CredentialRef struct {
	kind CredentialKind
	id   string
}

// VaultRef names a secret in the credential store.
func VaultRef(id credential.SecretID) CredentialRef {
	return CredentialRef{kind: CredentialVault, id: string(id)}
}

// FileRef names a private key file on this machine.
func FileRef(path string) CredentialRef {
	return CredentialRef{kind: CredentialFile, id: path}
}

// AgentRef names one key the ssh-agent holds, by the fingerprint of its
// public half (the `SHA256:…` form gossh.FingerprintSHA256 produces).
//
// The fingerprint is the identity rather than an index or an agent key's
// comment, because it is the one value that names the same key across two
// enumeration orders and cannot be made to mean a different key by anything
// the agent holds.
func AgentRef(fingerprint string) CredentialRef {
	return CredentialRef{kind: CredentialAgent, id: fingerprint}
}

// Kind is what this reference names, empty for the zero value.
func (r CredentialRef) Kind() CredentialKind { return r.kind }

// ID is the reference's own identifier inside its kind: a secret id, a path,
// or an agent key's fingerprint. Empty for the zero value.
func (r CredentialRef) ID() string { return r.id }

// IsZero reports whether this reference names nothing.
func (r CredentialRef) IsZero() bool { return r.kind == "" && r.id == "" }

// String is the wire spelling. The zero value is the empty string, which is
// how the wire says "no reference here".
func (r CredentialRef) String() string {
	if r.IsZero() {
		return ""
	}
	return string(r.kind) + ":" + r.id
}

// ParseCredentialRef reads the wire spelling back.
//
// A reference it does not recognise is refused rather than guessed at, and an
// empty one is refused too: every caller that can receive an empty reference
// is a caller that already treats "none" as its own state (an unencrypted key
// has no passphrase), so reaching here with one is a request that does not say
// what it wants rather than a request for nothing.
func ParseCredentialRef(s string) (CredentialRef, error) {
	if s == "" {
		return CredentialRef{}, errors.New("ssh: no credential reference")
	}
	name, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return CredentialRef{}, fmt.Errorf("ssh: credential reference %q names nothing", s)
	}
	switch kind := CredentialKind(name); kind {
	case CredentialVault, CredentialFile, CredentialAgent:
		return CredentialRef{kind: kind, id: id}, nil
	default:
		return CredentialRef{}, fmt.Errorf("ssh: credential reference %q is not one this build knows", s)
	}
}

// SignWithCredentialRef signs one challenge with whatever a reference names,
// and answers the signature in the wire encoding of an ssh signature (string
// format, string blob) beside the algorithm the key signs with.
//
// It is the ONE place the reference grammar is dispatched on, and it lives here
// rather than in the coordinator's reverse handler because the grammar is this
// package's: three kinds, three resolutions, and a handler that switched on the
// kind itself would be a second reading of a spelling this file owns. What the
// handler keeps is the part that is genuinely its own — turning the failures
// into wire refusals.
//
// Nothing here ever answers with key material. A vault key is parsed inside the
// store's own Use callback, a file is read, parsed and used within this call,
// and an agent key is never held at all: what leaves is a signature, which is
// what a signature is for.
func (rc *RealClient) SignWithCredentialRef(
	ctx context.Context, secrets credential.Resolver,
	ref CredentialRef, passphrase CredentialRef, challenge []byte,
) (signature []byte, algorithm string, err error) {
	switch ref.Kind() {
	case CredentialVault:
		cfg := &ConnectConfig{
			KeySecretID: credential.SecretID(ref.ID()),
			Secrets:     secrets,
		}
		if passphrase.Kind() == CredentialVault {
			cfg.PassphraseSecretID = credential.SecretID(passphrase.ID())
		}
		return rc.SignWithStoredKey(ctx, cfg, challenge)

	case CredentialFile:
		cfg := &ConnectConfig{
			KeyFile: ref.ID(),
			Secrets: secrets,
		}
		if passphrase.Kind() == CredentialVault {
			cfg.PassphraseSecretID = credential.SecretID(passphrase.ID())
		}
		signer, err := rc.loadKey(ctx, ref.ID(), cfg)
		if err != nil {
			return nil, "", err
		}
		sig, err := signer.Sign(rand.Reader, challenge)
		if err != nil {
			return nil, "", fmt.Errorf("sign with the key file %s: %w", ref.ID(), err)
		}
		return gossh.Marshal(sig), signer.PublicKey().Type(), nil

	case CredentialAgent:
		if passphrase.Kind() != "" {
			// An agent key is not parsed here and has no passphrase to unlock:
			// an agent that holds an encrypted key has already asked the person
			// for it, on their own desktop, when the key was added.
			return nil, "", fmt.Errorf("an ssh-agent key takes no passphrase")
		}
		return rc.signWithAgentKey(ctx, ref.ID(), challenge)

	default:
		return nil, "", fmt.Errorf("ssh: %q is not a credential reference this build signs with", ref.String())
	}
}

// agentKeys lists what the running agent holds, as public keys.
//
// It dials the socket and closes it again: nothing is signed here, and the
// question — which keys would be offered — is answered entirely by the
// enumeration. The connection is not kept because a signer bound to it would
// be a signer bound to a socket this process would then have to keep open
// between resolution and the handshake, which is a lifetime nothing in the
// resolution path owns.
//
// A failure is the caller's to name: there is no key here, only a reason, and
// the reason an ssh-agent cannot answer is a fact about the person's desktop
// session rather than about the host. ResolveTarget is what turns it into the
// refusal a connection reports (ErrNoAuthMethod with Mode "agent"), which is
// the same type that error has carried since the app's own auth ladder had
// this rung.
func (rc *RealClient) agentKeys() ([]gossh.PublicKey, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is unset")
	}
	conn, err := net.DialTimeout("unix", sock, agentDialTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		return nil, err
	}
	if len(signers) == 0 {
		return nil, fmt.Errorf("the agent at %s holds no keys", sock)
	}
	keys := make([]gossh.PublicKey, 0, len(signers))
	for _, s := range signers {
		keys = append(keys, s.PublicKey())
	}
	return keys, nil
}

// agentDialTimeout bounds the socket dial. An agent socket is a local unix
// socket and an answer that has not arrived in this window is an agent that is
// not going to answer — a hung agent must not hang a connection open.
const agentDialTimeout = 5 * time.Second

// signWithAgentKey asks the agent for one signature with the key a reference
// names, and answers it in the wire encoding of an ssh signature.
//
// The agent is dialed per signed challenge and closed immediately after, which
// is the same discipline the coordinator's own agent rung keeps: a Signer bound
// to a long-lived socket would be a socket this process holds open for the life
// of a connection it is not using, and ssh-agent's own protocol is cheap enough
// that the second dial costs nothing a person can measure.
func (rc *RealClient) signWithAgentKey(ctx context.Context, fingerprint string, challenge []byte) ([]byte, string, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, "", errors.New("no ssh-agent to sign with: SSH_AUTH_SOCK is unset")
	}
	conn, err := (&net.Dialer{Timeout: agentDialTimeout}).DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, "", fmt.Errorf("the ssh-agent could not be reached: %w", err)
	}
	defer func() { _ = conn.Close() }()

	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		return nil, "", fmt.Errorf("the ssh-agent could not be listed: %w", err)
	}
	for _, s := range signers {
		if gossh.FingerprintSHA256(s.PublicKey()) != fingerprint {
			continue
		}
		sig, err := s.Sign(rand.Reader, challenge)
		if err != nil {
			return nil, "", fmt.Errorf("the agent refused to sign: %w", err)
		}
		return gossh.Marshal(sig), s.PublicKey().Type(), nil
	}
	// The agent no longer holds the key the connection was resolved with:
	// `ssh-add -D` between the resolution and the signature, or a different
	// agent behind the same socket. Named as what it is, because the request
	// is not wrong — the key it names is simply gone.
	return nil, "", fmt.Errorf("the ssh-agent no longer holds the key %s", fingerprint)
}
