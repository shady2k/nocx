package app

// The coordinator's side of the helper's ssh connection: the handlers this
// machine's helper asks when it dials (plan §2, nocx-50w7p.2).
//
// # What this file is, and what it deliberately is not
//
// It is the composition-root answer to a closed set of questions — what
// material to present, what to sign, what to make of a host key — and it is
// REGISTERED here rather than looked up anywhere else, because a reverse
// handler is a coordinator capability and the list has to be readable in one
// place (client.ReverseRegistry).
//
// It is NOT a migration. The consumers that dial (files, forwards, discovery,
// the git lane, the settings probe) still reach the coordinator's own client
// (nocx-50w7p.3), and nothing in this file moves one of them: it answers the
// questions a helper would ask if one did, so the path is real and testable
// before anything depends on it.
//
// # Where the authority for each answer lives
//
// Every handler delegates to the thing that already owns the answer, and none
// of them re-derives it:
//
//   - material is read through the same stanced credential resolver the
//     coordinator's own dial path uses, so a sealed vault raises the unlock a
//     person answers (ADR-0032) instead of answering "no such secret";
//   - a host key is judged by the same known_hosts callback the dial path
//     installs, and recorded by the same single write path (ssh.RealClient);
//   - a signature is made by the same signer construction the public-key rung
//     of the auth chain uses, so key material never leaves the vault's Use
//     callback.
//
// # The trust decision is the CALLER's, and it travels with the request
//
// `trust-host-key` writes a key into known_hosts, and it is deliberately not a
// thing a helper can decide: the request that starts a probe carries
// AcceptOnTrust, the coordinator sets it only after the accept flow it already
// owns has run (connections.trustHostKey), and the helper asks for the write
// only when that flag says it may (internal/helper/sshsvc). A helper therefore
// cannot turn "ask the user" into "trust it".
//
// What is NOT defended against is the same-UID boundary itself (D12): anything
// running as this user can write known_hosts for themselves, so a hostile
// helper asking for the same write is not a widening — it is the trust model
// this whole level is built on, stated rather than assumed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/shady2k/nocx/internal/credential"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

// helperReverseHandlers builds the registry a helper's questions are answered
// through. It is constructed once, at the composition root, and handed to
// every connection this coordinator opens to its helper.
func helperReverseHandlers(client *ssh.RealClient, secrets credential.Resolver, log *slog.Logger) *helperclient.ReverseRegistry {
	h := &helperReverse{client: client, secrets: secrets, log: log}
	r := helperclient.NewReverseRegistry()
	r.Register(proto.ServiceSSH, proto.OpSecret, h.secret)
	r.Register(proto.ServiceSSH, proto.OpSign, h.sign)
	r.Register(proto.ServiceSSH, proto.OpVerifyHostKey, h.verifyHostKey)
	r.Register(proto.ServiceSSH, proto.OpTrustHostKey, h.trustHostKey)
	return r
}

// helperReverse holds the three seams every handler delegates to: the ssh
// client (host keys and signatures), the credential resolver (material) and
// the logger.
type helperReverse struct {
	client  *ssh.RealClient
	secrets credential.Resolver
	log     *slog.Logger
}

// secret answers the material a helper has to PRESENT. The read is stanced and
// the reason is per-purpose, so the vault's own audit says which question was
// asked and not merely that one was.
func (h *helperReverse) secret(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.SecretParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
	}
	if h.secrets == nil {
		return nil, errors.New("no credential store is wired, so no material can be read")
	}

	var (
		ref    string
		reason string
	)
	switch p.Purpose {
	case proto.PurposePassword:
		ref = p.Credential.Ref
		reason = "read the password a helper must present"
	case proto.PurposePassphrase:
		// A passphrase has its own reference because it is its own row: a key
		// and the passphrase that unlocks it are two stored secrets, and
		// resolving one where the other was meant is how a wrong-but-valid
		// password gets tried against a server.
		ref = p.Credential.PassphraseRef
		reason = "read the passphrase of a key a helper must unlock"
	default:
		return nil, badReverseParams(fmt.Sprintf("purpose %q is not one this coordinator knows", p.Purpose))
	}
	if ref == "" {
		return nil, badReverseParams(fmt.Sprintf("no %s reference in the credential", string(p.Purpose)))
	}

	secret, err := h.secrets.Resolve(ctx, credential.SecretID(ref), credential.Operation(reason))
	if err != nil {
		return nil, h.materialRefusal(err)
	}
	var material []byte
	if err := secret.Use(func(b []byte) error {
		material = append([]byte(nil), b...)
		return nil
	}); err != nil {
		return nil, h.materialRefusal(err)
	}
	if len(material) == 0 {
		return nil, fmt.Errorf("the stored %s is empty", string(p.Purpose))
	}
	return proto.SecretResult{Secret: material}, nil
}

// sign answers one signature over one challenge.
//
// The passphrase reference, when there is one, goes where the key is: the
// coordinator is the party that parses a private key, so an encrypted key is
// unlocked here and never at the helper (proto/ssh_service.go says why a
// signature can cross where a key cannot).
func (h *helperReverse) sign(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.SignParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Credential.Ref == "" {
		return nil, badReverseParams("no credential reference to sign with")
	}
	if len(p.Challenge) == 0 {
		// Signing nothing is a request nobody makes. Refusing it is also what
		// keeps `sign` a cryptographic operation rather than a discovery one:
		// there is no empty-challenge query hiding behind this op.
		return nil, badReverseParams("no challenge to sign")
	}
	if p.Algorithm == "" {
		return nil, badReverseParams("no algorithm to sign with")
	}
	if h.client == nil {
		return nil, errors.New("no ssh client is wired, so nothing can sign")
	}

	cfg := &ssh.ConnectConfig{
		KeySecretID: credential.SecretID(p.Credential.Ref),
		Secrets:     h.secrets,
	}
	if p.Credential.PassphraseRef != "" {
		cfg.PassphraseSecretID = credential.SecretID(p.Credential.PassphraseRef)
	}

	signature, algorithm, err := h.client.SignWithStoredKey(ctx, cfg, p.Challenge)
	if err != nil {
		var locked *ssh.ErrEncryptedKey
		if errors.As(err, &locked) {
			// The key is fine; it is locked, and the passphrase is nowhere to
			// be read. That is the `needs-interactive` answer the probe
			// reports, not a failure of the credential.
			return nil, &proto.Refusal{Code: proto.ErrCodeNeedsInteractive, Message: err.Error()}
		}
		return nil, h.materialRefusal(err)
	}
	if algorithm != p.Algorithm {
		return nil, badReverseParams(fmt.Sprintf(
			"the credential's key is %s and the request asked to sign as %s", algorithm, p.Algorithm))
	}
	return proto.SignResult{Signature: signature}, nil
}

// verifyHostKey answers what to make of a host key the helper was just
// offered, from the same known_hosts the coordinator's own dials consult.
//
// It WRITES nothing: accepting a host is the next handler, and a query that
// recorded what it read would make the answer to "is this known" depend on who
// asked it last.
func (h *helperReverse) verifyHostKey(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.VerifyHostKeyParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
	}
	key, err := h.checkHostKeyParams(p.Host, p.Algorithm, p.Key)
	if err != nil {
		return nil, err
	}
	if h.client == nil {
		return nil, errors.New("no ssh client is wired, so no host key can be judged")
	}

	err = h.client.CheckHostKey(p.Host, p.Key)
	var (
		unknown *ssh.ErrUnknownHostKey
		changed *ssh.ErrHostKeyMismatch
	)
	switch {
	case err == nil:
		return proto.VerifyHostKeyResult{
			Verdict:     proto.HostKeyTrusted,
			Fingerprint: gossh.FingerprintSHA256(key),
		}, nil
	case errors.As(err, &unknown):
		return proto.VerifyHostKeyResult{
			Verdict:     proto.HostKeyUnknown,
			Fingerprint: unknown.Fingerprint,
		}, nil
	case errors.As(err, &changed):
		// The stored fingerprints travel WITH the verdict, so a changed key
		// can never be shown to a person without the value it changed from.
		return proto.VerifyHostKeyResult{
			Verdict:     proto.HostKeyChanged,
			Fingerprint: changed.Fingerprint,
			Expected:    changed.Expected,
		}, nil
	}
	return nil, fmt.Errorf("host key: %w", err)
}

// trustHostKey records a key for a host, through the one write path this
// repository has for known_hosts.
func (h *helperReverse) trustHostKey(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.TrustHostKeyParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
	}
	if _, err := h.checkHostKeyParams(p.Host, p.Algorithm, p.Key); err != nil {
		return nil, err
	}
	if h.client == nil {
		return nil, errors.New("no ssh client is wired, so no host key can be recorded")
	}
	fingerprint, err := h.client.TrustHostKey(p.Host, p.Key)
	if err != nil {
		return nil, fmt.Errorf("record the host key: %w", err)
	}
	return proto.TrustHostKeyResult{Fingerprint: fingerprint}, nil
}

// checkHostKeyParams validates a host-key payload and answers the parsed key:
// the host is required, the key must be a public key, and the algorithm the
// caller declared must be the key's own.
//
// The algorithm check is what makes the field load-bearing rather than
// decorative: a mismatch means the two ends disagree about WHICH key this is,
// and the answer to that disagreement is a refusal, not a fingerprint of a key
// nobody asked about.
func (h *helperReverse) checkHostKeyParams(host, algorithm string, blob []byte) (gossh.PublicKey, error) {
	if host == "" {
		return nil, badReverseParams("no host")
	}
	// The identity has to be an ADDRESSABLE one — host:port — and it is checked
	// here rather than left to the trust store, because what the trust store
	// does with a string it cannot split is not a refusal it is allowed to
	// invent. The coordinator's own dial path passes the resolved dial address,
	// so a request that is not one came from something that is not a probe.
	if _, _, err := net.SplitHostPort(host); err != nil {
		return nil, badReverseParams("the host is not host:port: " + err.Error())
	}
	if len(blob) == 0 {
		return nil, badReverseParams("no key")
	}
	key, err := gossh.ParsePublicKey(blob)
	if err != nil {
		return nil, badReverseParams("the key is not a public key: " + err.Error())
	}
	if algorithm == "" || key.Type() != algorithm {
		return nil, badReverseParams(fmt.Sprintf(
			"the key is %s and the request declared %q", key.Type(), algorithm))
	}
	return key, nil
}

// materialRefusal turns a material read's failure into the answer the caller
// acts on. A vault that will not open is its OWN code, because the coordinator
// turns exactly that into the unlock sheet it already owns — and a generic
// failure would send the user to look at the host instead of at the vault.
func (h *helperReverse) materialRefusal(err error) error {
	if errors.Is(err, vault.ErrVaultSealed) || errors.Is(err, vault.ErrVaultUninitialized) {
		return &proto.Refusal{Code: proto.ErrCodeVaultSealed, Message: err.Error()}
	}
	return err
}

// badReverseParams names a request this coordinator will not answer as it
// stands. It is the same code the host uses for its own dispatch, so a caller
// has one thing to switch on whichever side refused.
func badReverseParams(message string) error {
	return &proto.Refusal{Code: proto.ErrCodeBadParams, Message: message}
}

// decodeReverseParams decodes a reverse request's params, refusing a payload
// that does not parse as the params of this op. Absent params are the zero
// value, exactly as they are on the other side of this wire (host.Schema
// .Decode) — the handler's own validation is what refuses an empty one.
func decodeReverseParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return badReverseParams("malformed params: " + err.Error())
	}
	return nil
}

// setReverseHandlers records the answers this coordinator gives the helper on
// this machine.
//
// It is a SETTER rather than a constructor argument for the same reason
// installedLocalGeneration is: the opener is built early (it needs nothing but
// a logger) and the material it answers with does not exist until the vault and
// the ssh client have been built. The alternative — moving the opener's
// construction down beside them — would make one wiring order mandatory for two
// unrelated facts.
func (o *localHelperOpener) setReverseHandlers(reverse *helperclient.ReverseRegistry) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reverse = reverse
}
