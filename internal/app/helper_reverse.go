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
	"sync"

	"github.com/shady2k/nocx/internal/credential"
	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

// helperReverseHandlers builds the registry a helper's questions are answered
// through. It is constructed once, at the composition root, and handed to
// every connection this coordinator opens to its helper.
func helperReverseHandlers(client *ssh.RealClient, secrets credential.Resolver, prompts *helperPrompt, hostKeys *hostKeyObserver, log *slog.Logger) *helperclient.ReverseRegistry {
	h := &helperReverse{client: client, secrets: secrets, prompts: prompts, hostKeys: hostKeys, log: log}
	r := helperclient.NewReverseRegistry()
	r.Register(proto.ServiceSSH, proto.OpSecret, h.secret)
	r.Register(proto.ServiceSSH, proto.OpSign, h.sign)
	r.Register(proto.ServiceSSH, proto.OpPrompt, h.prompt)
	r.Register(proto.ServiceSSH, proto.OpVerifyHostKey, h.verifyHostKey)
	r.Register(proto.ServiceSSH, proto.OpTrustHostKey, h.trustHostKey)
	return r
}

// helperPrompt is the coordinator's answer to a helper that needs a PERSON: the
// seam onto the renderer's connection-password ask, whose implementation does
// not exist when the reverse handlers are bound.
//
// It is a holder rather than an argument because of the composition root's own
// order: the vault and the ssh client exist when the opener is built, and the
// transport — which is what can raise a question on a renderer — is built
// later in the same function. The alternative is a setter on the registry,
// which would make "who answers a prompt" a second thing to keep in step with
// "who answers material". Setting it here keeps the whole answer in one
// registry, and a prompt asked before the transport exists is refused by name
// (no asker wired) rather than silently answered with nothing.
type helperPrompt struct {
	mu    sync.RWMutex
	asker ssh.ConnectionPasswordRequester
	log   *slog.Logger
}

// set records the asker once the transport is built.
func (p *helperPrompt) set(asker ssh.ConnectionPasswordRequester) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asker = asker
}

// RequestConnectionPassword is the ssh package's own seam, unchanged: it is
// what the coordinator's own prompt rung calls, so a helper's question and a
// dial's question reach the person through ONE path with one set of outcomes.
func (p *helperPrompt) RequestConnectionPassword(ctx context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	p.mu.RLock()
	asker := p.asker
	p.mu.RUnlock()
	if asker == nil {
		return ssh.PasswordAnswer{}, errNoPromptAsker
	}
	return asker.RequestConnectionPassword(ctx, req)
}

// errNoPromptAsker is this coordinator having no surface to ask a person on:
// the transport that raises the question is not built yet, or this process runs
// without a renderer at all. It is a sentinel rather than a sentence because the
// prompt handler turns exactly it into the wire's `no_auth_channel` — the same
// state a helper with no coordinator connection is in, and the same answer.
var errNoPromptAsker = errors.New("no asker is wired for a connection prompt")

// helperReverse holds the seams every handler delegates to: the ssh client
// (host keys and signatures), the credential resolver (material), the prompt
// seam (the person) and the logger.
type helperReverse struct {
	client  *ssh.RealClient
	secrets credential.Resolver
	prompts *helperPrompt
	log     *slog.Logger
	// hostKeys is where a TRUSTED verdict's fingerprint is cached, keyed by
	// the same storage identity the dial resolved (nocx-y6fh7 items 5 and
	// 6). Nil is a legitimate wiring for a test double that exercises no
	// consumer of it.
	hostKeys *hostKeyObserver
}

// credentialRef reads a reference that crossed the wire back into the typed
// handle this package resolves, refusing anything else by name.
func credentialRef(ref string, purpose string) (ssh.CredentialRef, error) {
	parsed, err := ssh.ParseCredentialRef(ref)
	if err != nil {
		return ssh.CredentialRef{}, badReverseParams(fmt.Sprintf("%s: %v", purpose, err))
	}
	return parsed, nil
}

// optionalCredentialRef is the same read for a reference that may be absent — a
// passphrase, for instance, which most keys have not got. An empty value is the
// zero reference rather than an error, and anything non-empty must parse.
func optionalCredentialRef(ref string, purpose string) (ssh.CredentialRef, error) {
	if ref == "" {
		return ssh.CredentialRef{}, nil
	}
	return credentialRef(ref, purpose)
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
		ref    ssh.CredentialRef
		reason string
		err    error
	)
	switch p.Purpose {
	case proto.PurposePassword:
		ref, err = credentialRef(p.Credential.Ref, "the password reference")
		reason = "read the password a helper must present"
	case proto.PurposePassphrase:
		// A passphrase has its own reference because it is its own row: a key
		// and the passphrase that unlocks it are two stored secrets, and
		// resolving one where the other was meant is how a wrong-but-valid
		// password gets tried against a server.
		ref, err = credentialRef(p.Credential.PassphraseRef, "the passphrase reference")
		reason = "read the passphrase of a key a helper must unlock"
	default:
		return nil, badReverseParams(fmt.Sprintf("purpose %q is not one this coordinator knows", p.Purpose))
	}
	if err != nil {
		return nil, err
	}
	if ref.Kind() != ssh.CredentialVault {
		// Material a helper presents is always STORED material: a file key and
		// an agent key are asked for as SIGNATURES (`sign`), never as bytes,
		// and a password has no file or agent form. Answering this would mean
		// reading a private key out of a file into the wire.
		return nil, badReverseParams(fmt.Sprintf(
			"a %s reference cannot be presented as %s material", ref.Kind(), string(p.Purpose)))
	}

	secret, err := h.secrets.Resolve(ctx, credential.SecretID(ref.ID()), credential.Operation(reason))
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
// The reference decides WHERE the signature is made — a vault key parsed inside
// the store's own Use callback, a key file read here, an agent asked to answer
// for itself — and in all three cases the private half stays on this side of
// the wire. The passphrase reference, when there is one, goes where the key is:
// this process is the party that parses a private key, so an encrypted key is
// unlocked here and never at the helper (proto/ssh_service.go says why a
// signature can cross where a key cannot).
func (h *helperReverse) sign(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.SignParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
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
	ref, err := credentialRef(p.Credential.Ref, "the signing reference")
	if err != nil {
		return nil, err
	}
	passphrase, err := optionalCredentialRef(p.Credential.PassphraseRef, "the passphrase reference")
	if err != nil {
		return nil, err
	}

	signature, algorithm, err := h.client.SignWithCredentialRef(ctx, h.secrets, ref, passphrase, p.Challenge)
	if err != nil {
		var locked *ssh.ErrEncryptedKey
		if errors.As(err, &locked) {
			// The key is fine; it is locked, and the passphrase is nowhere to
			// be read. That is the `needs-interactive` answer the probe
			// reports, not a failure of the credential. It covers both halves
			// of a wrong passphrase — absent and incorrect — because for a
			// person they are one state: the key cannot be opened without one.
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

// prompt asks the PERSON at this coordinator's UI the questions a server asked
// during a keyboard-interactive challenge, and answers what they said.
//
// It is the one reverse op whose answer nobody holds: `secret` reads a stored
// value, `sign` computes one, and this one exists only for the moment a person
// is in front of the screen. That is also why its failures are named rather
// than folded together — a dismissed prompt and a coordinator with no renderer
// are different states, and neither may be answered with an empty string, which
// a server reads as a wrong password and reports as a rejected credential.
//
// The ask goes through the SAME seam the coordinator's own prompt rung uses
// (ssh.ConnectionPasswordRequester), so a helper's question and a dial's
// question raise one dialog with one set of outcomes; the connection and the
// account the server's question is about travel with it, because a bare "enter
// password" box is how the wrong password ends up in the wrong connection.
func (h *helperReverse) prompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.PromptParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Prompts) == 0 {
		return nil, badReverseParams("no prompts to answer")
	}
	if p.Host == "" || p.User == "" {
		return nil, badReverseParams("the prompts name no host or account")
	}
	// The port is checked like every other endpoint on this wire: it is display
	// data for the ask ("which connection is this"), and a renderer shown a
	// port of 0 has been handed a fact nobody resolved. The schema says the
	// same thing; this is the running coordinator saying it.
	if p.Port <= 0 || p.Port > 65535 {
		return nil, badReverseParams(fmt.Sprintf("the prompts name port %d", p.Port))
	}
	if h.prompts == nil {
		return nil, &proto.Refusal{
			Code:    proto.ErrCodeNoAuthChannel,
			Message: "no prompt can be raised: this coordinator is not connected to a renderer",
		}
	}

	// ONE ASK PER QUESTION, in the order the server asked them, because that is
	// what a terminal does and because the alternative leaks secrets: this
	// seam collects one value, and copying it across every question would send
	// a password into a "Verification code:" prompt. The server's own text is
	// the question a person reads in each ask.
	answers := make([]string, 0, len(p.Prompts))
	for _, prompt := range p.Prompts {
		answer, err := h.prompts.RequestConnectionPassword(ctx, ssh.PasswordRequest{
			User: p.User,
			Host: p.Host,
			// The server's own question, verbatim: nocx invented neither the
			// question nor the answer, and a reason of its own would be a
			// sentence about a host instead of the question being asked.
			Reason: prompt.Prompt,
		})
		if err != nil {
			return nil, h.promptRefusal(ctx, err)
		}
		answers = append(answers, answer.Password)
	}
	return proto.PromptResult{Answers: answers}, nil
}

// promptRefusal types a failed ask for the helper.
//
// A prompt a person DISMISSED is its own code, because it is a decision and not
// a failure: the credential may be perfectly good and the answer is simply no.
// Anything else — no renderer attached, a transport that died mid-ask, the
// context cancelled — is this coordinator having nowhere to ask, which is the
// same state a helper with no coordinator connection is in and the same code.
func (h *helperReverse) promptRefusal(ctx context.Context, err error) error {
	if errors.Is(err, transport.ErrPasswordPromptCancelled) {
		return &proto.Refusal{Code: proto.ErrCodePromptCancelled, Message: err.Error()}
	}
	if errors.Is(err, transport.ErrPasswordNoClientConnected) || errors.Is(err, errNoPromptAsker) || ctx.Err() != nil {
		return &proto.Refusal{Code: proto.ErrCodeNoAuthChannel, Message: err.Error()}
	}
	return err
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
	// The STORAGE identity is what is looked up, and it is not always the
	// offered address: a host reached through a jump has a route-derived one,
	// and the answer here must be about the record the coordinator's own dial
	// would have consulted. The offered address is still checked — it travels
	// in the error and in the evidence a person is shown.
	storageAddr := p.KnownHostsAddr
	if storageAddr == "" {
		storageAddr = p.Host
	}
	key, err := h.checkHostKeyParams(storageAddr, p.Algorithm, p.Key)
	if err != nil {
		return nil, err
	}
	if h.client == nil {
		return nil, errors.New("no ssh client is wired, so no host key can be judged")
	}

	err = h.client.CheckHostKey(storageAddr, p.Key)
	var (
		unknown *ssh.ErrUnknownHostKey
		changed *ssh.ErrHostKeyMismatch
	)
	switch {
	case err == nil:
		fp := gossh.FingerprintSHA256(key)
		// Recorded on the way out, not before: the ONLY verdict a later
		// consumer (a session's HostKeyFingerprint, the consent decision at
		// connect) may act on is one this dial actually trusted — an
		// unknown or changed key is judged, never cached, because the pane
		// this ask belongs to may never open at all (nocx-y6fh7 items 5, 6).
		h.hostKeys.record(storageAddr, fp)
		return proto.VerifyHostKeyResult{
			Verdict:     proto.HostKeyTrusted,
			Fingerprint: fp,
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
