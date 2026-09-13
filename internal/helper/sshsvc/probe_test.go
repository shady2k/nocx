//go:build nocx_local_ssh

package sshsvc_test

// The acceptance half: a probe through the helper authenticates with material
// only the coordinator has, and every way it can fail is a named answer with a
// success beside it.
//
// Each refusal below is paired with the success it is the refusal OF, in the
// same test or the adjacent one, because a refusal alone cannot be told apart
// from an op that never worked: "the server rejected the key" and "the helper
// never sent one" produce the same red.

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// TestAProbeAuthenticatesWithAPasswordOnlyTheCoordinatorHas is the acceptance
// criterion's first half: the password is supplied by a COORDINATOR-SIDE
// handler, over the reverse channel, at the moment the server challenges for it
// — and the server sees exactly it.
func TestAProbeAuthenticatesWithAPasswordOnlyTheCoordinatorHas(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	coord := &coordinator{password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()}
	stand := newStand(t, coord)

	result, err := stand.probe(t, passwordProbeParams(t, f))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}

	passwords, _ := f.authAttempts()
	if len(passwords) != 1 || passwords[0] != password {
		t.Fatalf("the server saw %q, want exactly the password the coordinator supplied", passwords)
	}
	// The host key is asked about FIRST — before a credential is offered — and
	// then the password is asked for. Two questions, in that order.
	if asked := coord.asked(); len(asked) != 2 || asked[0] != proto.OpVerifyHostKey || asked[1] != proto.OpSecret {
		t.Fatalf("reverse ops asked = %v, want [%s %s]", asked, proto.OpVerifyHostKey, proto.OpSecret)
	}
}

// TestAProbeWithTheWrongPasswordIsRejected is that success's refusal half: the
// same path, a credential the server will not take. It is a CLASSIFIED outcome
// rather than an op failure, because "the server said no" is an answer.
func TestAProbeWithTheWrongPasswordIsRejected(t *testing.T) {
	f := newFixture(t, "the right password", newSigner(t))
	coord := &coordinator{password: "the wrong password", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()}
	stand := newStand(t, coord)

	result, err := stand.probe(t, passwordProbeParams(t, f))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeRejected {
		t.Fatalf("outcome = %q (%s), want rejected", result.Outcome, result.Detail)
	}
	passwords, _ := f.authAttempts()
	if len(passwords) != 1 || passwords[0] != "the wrong password" {
		t.Fatalf("the server saw %q, want the password the coordinator supplied", passwords)
	}
}

// TestAProbeAuthenticatesWithAKeyTheCoordinatorSignsWith is the acceptance
// criterion's second half, and the interesting one: the helper proves
// possession of a key it does not have, because the signature is made one
// process away and only the public half travelled.
func TestAProbeAuthenticatesWithAKeyTheCoordinatorSignsWith(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "", key.signer)
	coord := &coordinator{
		signer: key.signer, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, keyProbeParams(t, f, key.signer))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}
	if asked := coord.asked(); !contains(asked, proto.OpSign) {
		t.Fatalf("reverse ops asked = %v, want a %s among them", asked, proto.OpSign)
	}
	_, fingerprints := f.authAttempts()
	if len(fingerprints) != 1 || fingerprints[0] != gosshFingerprint(key) {
		t.Fatalf("the server authenticated %q, want the coordinator's key", fingerprints)
	}
}

// TestAProbeWithAKeyTheServerDoesNotAcceptIsRejected is the refusal half of the
// key path. The coordinator signs with a key that is NOT the one the server
// holds: the helper is honest throughout — it offers the public half it was
// given — and only the far side says no.
func TestAProbeWithAKeyTheServerDoesNotAcceptIsRejected(t *testing.T) {
	accepted := newTestKey(t)
	offered := newTestKey(t)
	f := newFixture(t, "", accepted.signer)
	coord := &coordinator{
		signer: offered.signer, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, keyProbeParams(t, f, offered.signer))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeRejected {
		t.Fatalf("outcome = %q (%s), want rejected", result.Outcome, result.Detail)
	}
}

// TestAnUnknownHostKeyIsRefusedAndAcceptedOnTrust is the accept-on-first-use
// pair, and it carries the property that matters: an unseen key is refused
// BEFORE the credential is offered, so a person who has not decided yet does
// not hand their password to whoever answered the port.
func TestAnUnknownHostKeyIsRefusedAndAcceptedOnTrust(t *testing.T) {
	const password = "s3cret"
	f := newFixture(t, password, newSigner(t))
	coord := &coordinator{
		password: password,
		verdict:  proto.HostKeyUnknown, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	refused, err := stand.probe(t, passwordProbeParams(t, f))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if refused.Outcome != proto.ProbeHostKeyUnknown {
		t.Fatalf("outcome = %q (%s), want host-key-unknown", refused.Outcome, refused.Detail)
	}
	if passwords, _ := f.authAttempts(); len(passwords) != 0 {
		t.Fatalf("the server was offered %q before the host key was accepted", passwords)
	}
	if trusted := coord.trusted(); len(trusted) != 0 {
		t.Fatalf("trust-host-key was asked for %d times, want none", len(trusted))
	}

	// The caller has since accepted the key: the coordinator now says so, and
	// the same probe goes through.
	accepted := passwordProbeParams(t, f)
	accepted.AcceptOnTrust = true

	result, err := stand.probe(t, accepted)
	if err != nil {
		t.Fatalf("probe with accept-on-trust: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}
	trusted := coord.trusted()
	if len(trusted) != 1 {
		t.Fatalf("trust-host-key was asked for %d times, want exactly one", len(trusted))
	}
	if trusted[0].Algorithm == "" || len(trusted[0].Key) == 0 {
		t.Fatalf("the trust request carried no key: %+v", trusted[0])
	}

	// The helper did not re-verify afterwards: one question, one write, no
	// loop. Two verify requests would mean the answer was not trusted.
	verifies := 0
	for _, op := range coord.asked() {
		if op == proto.OpVerifyHostKey {
			verifies++
		}
	}
	if verifies != 2 {
		t.Fatalf("verify-host-key was asked %d times for two probes, want exactly 2", verifies)
	}
}

// TestAChangedHostKeyIsRefused is the one refusal that is never a first
// contact: a key IS recorded and this is not it. It must not be conflated with
// the routine case above, and the stored value travels with it so a person can
// see what it changed from.
func TestAChangedHostKeyIsRefused(t *testing.T) {
	const stored = "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f := newFixture(t, "pw", newSigner(t))
	coord := &coordinator{
		verdict: proto.HostKeyChanged, fingerprint: f.hostKeyFingerprint(), expected: stored,
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, passwordProbeParams(t, f))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeHostKeyChanged {
		t.Fatalf("outcome = %q (%s), want host-key-changed", result.Outcome, result.Detail)
	}
	if !strings.Contains(result.Detail, stored) {
		t.Fatalf("the refusal does not name the recorded key: %q", result.Detail)
	}
	if passwords, _ := f.authAttempts(); len(passwords) != 0 {
		t.Fatalf("the server was offered %q despite a changed host key", passwords)
	}
	if trusted := coord.trusted(); len(trusted) != 0 {
		t.Fatalf("a CHANGED key was recorded, which no answer may do: %+v", trusted)
	}
}

// TestATrustedHostKeyLetsTheProbeThrough is the success the two host-key
// refusals above are refusals OF.
func TestATrustedHostKeyLetsTheProbeThrough(t *testing.T) {
	const password = "pw"
	f := newFixture(t, password, newSigner(t))
	coord := &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, passwordProbeParams(t, f))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}
}

// TestASealedVaultRefusesTheProbeByItsOwnName is the vault pair: sealed refuses
// with the code the coordinator's own caller turns into its unlock sheet, and
// unsealed — the test above — proceeds. The refusal must be its OWN code and
// not `internal`, because a generic failure would send a person to look at the
// host.
func TestASealedVaultRefusesTheProbeByItsOwnName(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	coord := &coordinator{
		password: "pw", sealed: true,
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	_, err := stand.probe(t, passwordProbeParams(t, f))
	if err == nil {
		t.Fatal("a probe whose material could not be read succeeded")
	}
	if code := refusalCode(err); code != proto.ErrCodeVaultSealed {
		t.Fatalf("refusal code = %q (err %v), want %q", code, err, proto.ErrCodeVaultSealed)
	}
	if passwords, _ := f.authAttempts(); len(passwords) != 0 {
		t.Fatalf("the server was offered %q from a sealed vault", passwords)
	}
}

// TestAProbeWithNoCoordinatorConnectionIsRefusedByName is the no-hang
// criterion, and the only way to reach the state is the way the plan names: a
// request with no connection on it. The refusal is immediate and named —
// errNoAuthChannel's code — rather than a wait for an answer that has nobody to
// come from.
func TestAProbeWithNoCoordinatorConnectionIsRefusedByName(t *testing.T) {
	realClient, err := ssh.NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = realClient.Close() })
	svc := sshsvc.New(realClient, discardLogger())

	params := proto.ProbeParams{
		Destination: proto.SSHDestination{
			Host: "127.0.0.1", Port: 1, User: "test",
			Identity: proto.SSHIdentity{
				Credential: &proto.SSHCredential{Ref: wantRef},
				Auth:       proto.SSHAuthPassword,
			},
		},
	}
	// A context with NO connection on it: no host served this call, so there is
	// nobody to ask (host.ConnectionFrom answers nil).
	_, err = svc.Call(context.Background(), proto.OpProbe, mustJSON(t, params))
	if err == nil {
		t.Fatal("a probe with no coordinator connection succeeded")
	}
	code, _ := svc.Refusal(err)
	if code != proto.ErrCodeNoAuthChannel {
		t.Fatalf("refusal code = %q (err %v), want %q", code, err, proto.ErrCodeNoAuthChannel)
	}
}

// TestThePrivateKeyNeverCrossesToTheHelper is the acceptance criterion's
// negative half, checked on the BYTES rather than on the result: the same
// key-auth probe that succeeds above, with the recording of both directions
// searched for the private key — in its PEM text and in the base64 an embedded
// copy would have to wear.
func TestThePrivateKeyNeverCrossesToTheHelper(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "", key.signer)
	coord := &coordinator{
		signer: key.signer, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, keyProbeParams(t, f, key.signer))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}

	// Both directions: what the coordinator sent the helper, and what the
	// helper sent back. A key that crossed either way is a key the helper held.
	encoded := []byte(base64.StdEncoding.EncodeToString(key.pem))
	for _, direction := range []struct {
		name string
		wire []byte
	}{
		{"coordinator to helper", stand.toHelper.bytes()},
		{"helper to coordinator", stand.toCoord.bytes()},
	} {
		if bytes.Contains(direction.wire, key.pem) {
			t.Fatalf("the private key crossed %s in the clear", direction.name)
		}
		if bytes.Contains(direction.wire, encoded) {
			t.Fatalf("the private key crossed %s base64-encoded", direction.name)
		}
	}

	// And the positive control, which is what shows the search above is not
	// blind: the SAME recording, searched with the SAME bytes.Contains, does
	// find a key — the public half, in the base64 JSON gives a byte slice. An
	// assertion that found nothing ever would pass the two above for the wrong
	// reason, and this is the one that would not.
	if asked := coord.asked(); !contains(asked, proto.OpSign) {
		t.Fatalf("reverse ops asked = %v: no signature was ever requested, so the search proved nothing", asked)
	}
	if !bytes.Contains(stand.toCoord.bytes(), []byte(base64.StdEncoding.EncodeToString(key.signer.PublicKey().Marshal()))) {
		t.Fatal("the public half never crossed either, so this probe did not authenticate with a key at all")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func gosshFingerprint(key testKey) string {
	return gossh.FingerprintSHA256(key.signer.PublicKey())
}

// TestAProbeAnswersAServersKeyboardInteractiveChallengeFromTheCoordinator is the
// prompt rung's success: the server asks its own question, the helper relays it
// to the coordinator, and the answer somebody gave is what authenticates.
//
// The evidence is the far side's own record of what it RECEIVED. A helper that
// answered from nothing — an empty string, a stored secret it happened to have —
// would fail here, and so would one that invented a question: the text the
// coordinator was asked for is asserted from the request that crossed.
func TestAProbeAnswersAServersKeyboardInteractiveChallengeFromTheCoordinator(t *testing.T) {
	f := newFixture(t, "", nil)
	f.kbdPassword = "s3cret-answer"

	coord := &coordinator{
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
		promptAnswers: []string{"s3cret-answer"},
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, interactiveProbeParams(t, f))
	if err != nil {
		t.Fatalf("probe with a keyboard-interactive credential: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}
	if got := f.kbdAnswersSeen(); len(got) != 1 || got[0] != "s3cret-answer" {
		t.Fatalf("the server received %q, want the answer the coordinator gave", got)
	}
	asked := coord.promptsAsked()
	if len(asked) != 1 {
		t.Fatalf("the coordinator was asked %d challenge(s), want 1", len(asked))
	}
	if len(asked[0].Prompts) != 1 || asked[0].Prompts[0].Prompt != "Password: " {
		t.Fatalf("the coordinator was asked %+v, want the server's own question verbatim", asked[0].Prompts)
	}
	if asked[0].Prompts[0].Echo {
		t.Fatal("the question was relayed as echoed; a password prompt must not be")
	}
	if asked[0].Host == "" || asked[0].User == "" {
		t.Fatalf("the ask names no connection: %+v", asked[0])
	}
	// Nothing was read from a store: the person is the credential.
	if ops := coord.asked(); contains(ops, proto.OpSecret) || contains(ops, proto.OpSign) {
		t.Fatalf("a keyboard-interactive probe asked for stored material: it asked %v", ops)
	}
}

// TestAPromptACoordinatorCannotAnswerIsRefusedByName is the paired refusal, in
// its two real shapes: the person dismissed the question, and the coordinator
// has nobody attached to ask. They are DIFFERENT codes because they are
// different facts — one is a decision, the other is a missing surface — and
// neither may be answered with an empty string, which a server reads as a wrong
// password and reports as a rejected credential.
func TestAPromptACoordinatorCannotAnswerIsRefusedByName(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		message string
	}{
		{name: "the person dismissed it", code: proto.ErrCodePromptCancelled, message: "the prompt was cancelled"},
		{name: "no renderer is attached", code: proto.ErrCodeNoAuthChannel, message: "no renderer is attached"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "", nil)
			f.kbdPassword = "s3cret-answer"
			coord := &coordinator{
				verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
				promptRefusal: &proto.Refusal{Code: tc.code, Message: tc.message},
			}
			stand := newStand(t, coord)

			_, err := stand.probe(t, interactiveProbeParams(t, f))
			if got := refusalCode(err); got != tc.code {
				t.Fatalf("the refusal code is %q (err %v), want %q", got, err, tc.code)
			}
			// The refusal reached the server as NO answer: the challenge was
			// never answered, so nothing was tried against it.
			if got := f.kbdAnswersSeen(); len(got) != 0 {
				t.Fatalf("the server received %q, want nothing: the coordinator could not answer", got)
			}
		})
	}
}

// TestAKeyboardInteractiveProbeWithNoCoordinatorConnectionIsRefusedByName is the
// rung's own version of the no-coordinator state: a request that arrived with no
// connection on it has nobody to ask, and it is refused by NAME before anything
// is dialed rather than answered from a fallback that does not exist.
func TestAKeyboardInteractiveProbeWithNoCoordinatorConnectionIsRefusedByName(t *testing.T) {
	realClient, err := ssh.NewReal(log.NewSlogAdapter(nil))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = realClient.Close() })
	svc := sshsvc.New(realClient, discardLogger())

	params := proto.ProbeParams{
		Destination: proto.SSHDestination{
			Host: "127.0.0.1", Port: 1, User: "test",
			Identity: proto.SSHIdentity{Auth: proto.SSHAuthInteractive},
		},
	}
	_, err = svc.Call(context.Background(), proto.OpProbe, mustJSON(t, params))
	if err == nil {
		t.Fatal("a keyboard-interactive probe with no coordinator connection succeeded")
	}
	if code, _ := svc.Refusal(err); code != proto.ErrCodeNoAuthChannel {
		t.Fatalf("refusal code = %q (err %v), want %q", code, err, proto.ErrCodeNoAuthChannel)
	}
}

// ── a key QUEUE, offered in order (nocx-50w7p.19) ──────────────────────────
//
// An agent holding several keys is the ordinary setup, and the one a given host
// accepts is frequently not the first. This is the case at the helper's own
// seam: the identity carries a QUEUE, the helper declares every entry in turn
// within ONE `publickey` method, and the far side answers each — so the key that
// authenticates can be any of them, and the ones before it are queries rather
// than failed attempts.

// keyQueue builds the identity a coordinator sends when it has several keys to
// offer: one entry per key, in the order they must be declared.
func keyQueue(refs []string, signers []gossh.Signer) proto.SSHIdentity {
	offers := make([]proto.SSHKeyOffer, 0, len(signers))
	for i, s := range signers {
		offers = append(offers, proto.SSHKeyOffer{
			Credential: proto.SSHCredential{Ref: refs[i]},
			PublicKey:  s.PublicKey().Marshal(),
		})
	}
	return proto.SSHIdentity{Auth: proto.SSHAuthKey, Keys: offers}
}

// keyQueueParams is keyProbeParams for a queue: the same destination, with the
// identity replaced by every key the coordinator would offer.
func keyQueueParams(t *testing.T, f *fixture, refs []string, signers []gossh.Signer) proto.ProbeParams {
	t.Helper()
	p := passwordProbeParams(t, f)
	p.Destination.Identity = keyQueue(refs, signers)
	return p
}

// TestAProbeAuthenticatesWithALaterKeyOfTheQueue is the multi-key criterion: a
// host that accepts only the LAST key of the queue authenticates, because the
// helper offered every key in turn rather than the first one.
//
// The far side's record is the evidence rather than the outcome: a queue that
// arrived with one entry would still be accepted if that entry happened to be
// the right key, so what is asserted is that all three were DECLARED, in order,
// and that the accepted one is the third.
func TestAProbeAuthenticatesWithALaterKeyOfTheQueue(t *testing.T) {
	first, second, wanted := newTestKey(t), newTestKey(t), newTestKey(t)
	// The server accepts only `wanted` — the LAST key the coordinator offers.
	f := newFixture(t, "", wanted.signer)
	refs := []string{"file:first", "file:second", "file:wanted"}
	coord := &coordinator{
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
		keyring: map[string]gossh.Signer{
			refs[0]: first.signer, refs[1]: second.signer, refs[2]: wanted.signer,
		},
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, keyQueueParams(t, f, refs, []gossh.Signer{first.signer, second.signer, wanted.signer}))
	if err != nil {
		t.Fatalf("probe with a three-key queue: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted: the host accepts the queue's LAST key", result.Outcome, result.Detail)
	}
	_, declared := f.authAttempts()
	want := []string{gosshFingerprint(first), gosshFingerprint(second), gosshFingerprint(wanted)}
	if len(declared) != len(want) {
		t.Fatalf("the far side was shown %v, want all three keys %v", declared, want)
	}
	for i := range want {
		if declared[i] != want[i] {
			t.Fatalf("the far side was shown %v, want %v (first difference at %d)", declared, want, i)
		}
	}
	if asked := coord.asked(); !contains(asked, proto.OpSign) {
		t.Fatalf("reverse ops asked = %v: nothing was ever signed for", asked)
	}
}

// TestAProbeWithEveryKeyOfTheQueueRejectedIsRejected is the paired refusal: the
// server accepts none of the four keys offered, which is a REJECTED credential
// rather than a malformed request — and it saw the whole queue before saying so.
func TestAProbeWithEveryKeyOfTheQueueRejectedIsRejected(t *testing.T) {
	accepted := newTestKey(t)
	offered := []testKey{newTestKey(t), newTestKey(t), newTestKey(t)}
	f := newFixture(t, "", accepted.signer)

	refs := []string{"agent:one", "agent:two", "agent:three"}
	signers := []gossh.Signer{offered[0].signer, offered[1].signer, offered[2].signer}
	coord := &coordinator{
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
		keyring: map[string]gossh.Signer{refs[0]: signers[0], refs[1]: signers[1], refs[2]: signers[2]},
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, keyQueueParams(t, f, refs, signers))
	if err != nil {
		t.Fatalf("probe with a queue the host refuses: %v", err)
	}
	if result.Outcome != proto.ProbeRejected {
		t.Fatalf("outcome = %q (%s), want rejected", result.Outcome, result.Detail)
	}
	_, declared := f.authAttempts()
	if len(declared) != 3 {
		t.Fatalf("the far side was shown %v, want every key of the queue", declared)
	}
}

// TestNoKeyOfTheQueueEverCrossesToTheHelper extends the negative half to the
// multi-key path, where there is more than one private half to leak: every key's
// PEM text and its base64 body are searched for in BOTH directions, and the
// positive control is that the helper really did ask for a signature.
func TestNoKeyOfTheQueueEverCrossesToTheHelper(t *testing.T) {
	keys := []testKey{newTestKey(t), newTestKey(t), newTestKey(t)}
	f := newFixture(t, "", keys[2].signer)
	refs := []string{"file:one", "agent:two", "file:three"}
	coord := &coordinator{
		verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
		keyring: map[string]gossh.Signer{refs[0]: keys[0].signer, refs[1]: keys[1].signer, refs[2]: keys[2].signer},
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, keyQueueParams(t, f, refs, []gossh.Signer{keys[0].signer, keys[1].signer, keys[2].signer}))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeAccepted {
		t.Fatalf("outcome = %q (%s), want accepted", result.Outcome, result.Detail)
	}

	for i, key := range keys {
		encoded := []byte(base64.StdEncoding.EncodeToString(key.pem))
		for _, direction := range []struct {
			name string
			wire []byte
		}{
			{"coordinator to helper", stand.toHelper.bytes()},
			{"helper to coordinator", stand.toCoord.bytes()},
		} {
			if bytes.Contains(direction.wire, key.pem) {
				t.Fatalf("key %d crossed %s in the clear", i, direction.name)
			}
			if bytes.Contains(direction.wire, encoded) {
				t.Fatalf("key %d crossed %s base64-encoded", i, direction.name)
			}
		}
	}

	// The positive control: the SAME search over the SAME recording does find
	// something, so "nothing was found" is a fact about the keys rather than
	// about the search.
	if !bytes.Contains(stand.toCoord.bytes(), []byte(base64.StdEncoding.EncodeToString(keys[2].signer.PublicKey().Marshal()))) {
		t.Fatal("not even a public half crossed, so this probe did not authenticate with a key at all")
	}
}

// TestAKeyIdentityThisHelperCannotOfferIsRefused is the refusal half of the
// identity SHAPE, one case per way a queue can be unofferable: no keys at all, an
// entry with no reference to sign through, an entry with nothing to declare, and
// the two shapes that mix kinds (a password carrying keys, a key carrying a bare
// credential).
func TestAKeyIdentityThisHelperCannotOfferIsRefused(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	coord := &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()}
	stand := newStand(t, coord)

	good := newTestKey(t)
	cases := []struct {
		name     string
		identity proto.SSHIdentity
	}{
		{"no keys at all", proto.SSHIdentity{Auth: proto.SSHAuthKey}},
		{"an entry with no reference", proto.SSHIdentity{Auth: proto.SSHAuthKey, Keys: []proto.SSHKeyOffer{
			{PublicKey: good.signer.PublicKey().Marshal()},
		}}},
		{"an entry with nothing to declare", proto.SSHIdentity{Auth: proto.SSHAuthKey, Keys: []proto.SSHKeyOffer{
			{Credential: proto.SSHCredential{Ref: wantRef}},
		}}},
		{"an entry whose public half is not a key", proto.SSHIdentity{Auth: proto.SSHAuthKey, Keys: []proto.SSHKeyOffer{
			{Credential: proto.SSHCredential{Ref: wantRef}, PublicKey: []byte("not a key")},
		}}},
		{"a key identity carrying a bare credential", proto.SSHIdentity{
			Auth:       proto.SSHAuthKey,
			Credential: &proto.SSHCredential{Ref: wantRef},
			Keys:       keyQueue([]string{wantRef}, []gossh.Signer{good.signer}).Keys,
		}},
		{"a password identity carrying keys", proto.SSHIdentity{
			Auth:       proto.SSHAuthPassword,
			Credential: &proto.SSHCredential{Ref: wantRef},
			Keys:       keyQueue([]string{wantRef}, []gossh.Signer{good.signer}).Keys,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := passwordProbeParams(t, f)
			p.Destination.Identity = tc.identity
			_, err := stand.probe(t, p)
			if err == nil {
				t.Fatal("a probe with an unofferable identity succeeded")
			}
			if code := refusalCode(err); code != proto.ErrCodeBadParams {
				t.Fatalf("refusal code = %q (err %v), want %q", code, err, proto.ErrCodeBadParams)
			}
			if passwords, _ := f.authAttempts(); len(passwords) != 0 {
				t.Fatalf("the server was offered %q by a probe that should never have dialed", passwords)
			}
		})
	}
}
