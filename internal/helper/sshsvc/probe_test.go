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
		Host: "127.0.0.1", Port: 1, User: "test",
		Identity: proto.SSHIdentity{
			Credential: proto.SSHCredential{Ref: wantRef},
			Auth:       proto.SSHAuthPassword,
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

	// The search itself is checked, so the two assertions above cannot pass
	// because the predicate is blind: a recording that DID contain the key is
	// seen by the very same call.
	poisoned := append(append([]byte(nil), stand.toCoord.bytes()...), key.pem...)
	if !bytes.Contains(poisoned, key.pem) {
		t.Fatal("the leak predicate cannot see the private key at all, so the assertions above prove nothing")
	}

	// And the positive control, so the assertion above is not vacuous: the same
	// probe DID put a signature and a public key on the wire.
	if asked := coord.asked(); !contains(asked, proto.OpSign) {
		t.Fatalf("reverse ops asked = %v: no signature was ever requested, so the search proved nothing", asked)
	}
	// The public half travels COORDINATOR to helper, inside the probe's
	// identity — base64, because that is what JSON makes of a byte slice.
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
