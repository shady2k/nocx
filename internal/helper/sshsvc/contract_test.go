//go:build nocx_local_ssh

package sshsvc_test

// The third contract check, applied to the ssh service: the payloads OFF THE
// WIRE, not the structs a test built.
//
// The difference is the whole point of the check (contracts/helper/README.md):
// a test that marshals its own value proves the struct is well-formed, while
// this takes the bytes each side actually sent — through the real framing, the
// real writer mutexes, the real response envelopes — and validates THOSE. Both
// directions are checked, because this service speaks both: `probe` is a
// request the coordinator sends and a result the helper answers, and the four
// reverse ops are the same two frames the other way round.
//
// The schema loader mirrors internal/helper/client's (the package that owns the
// other half of this ABI's validation). It is duplicated rather than shared for
// the reason this package's ssh fixture is: those symbols live in another
// package's `_test.go` file, and Go does not export those.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
)

const helperContractDir = "../../../contracts/helper"

func loadHelperSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	entries, err := os.ReadDir(helperContractDir)
	if err != nil {
		t.Fatalf("read contracts/helper: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		f, openErr := os.Open(filepath.Join(helperContractDir, e.Name())) //nolint:gosec // test-only path under contracts/
		if openErr != nil {
			t.Fatalf("open %s: %v", e.Name(), openErr)
		}
		doc, parseErr := jsonschema.UnmarshalJSON(f)
		_ = f.Close()
		if parseErr != nil {
			t.Fatalf("parse %s: %v", e.Name(), parseErr)
		}
		if addErr := c.AddResource("https://nocx.local/contracts/helper/"+e.Name(), doc); addErr != nil {
			t.Fatalf("add %s: %v", e.Name(), addErr)
		}
	}
	s, err := c.Compile("https://nocx.local/contracts/helper/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

func validateHelperJSON(s *jsonschema.Schema, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

// framesOf collects the payloads of one frame type from a recorded direction.
//
// The recording starts mid-handshake — its first bytes are the sentinel LINE,
// which is not a frame — and the decoder's own resync is what gets past it,
// exactly as the shipping client's pump does. That is not a convenience
// borrowed from the decoder: it is the same behaviour a real peer depends on.
func framesOf(t *testing.T, raw []byte, want proto.FrameType) [][]byte {
	t.Helper()
	var out [][]byte
	dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
		if ty == want {
			out = append(out, append([]byte(nil), payload...))
		}
	}, func(int) {})
	if err := dec.Feed(raw); err != nil {
		t.Fatalf("decode recorded frames: %v", err)
	}
	return out
}

// TestTheSSHServiceOpsConformToTheirContractsOverTheWire drives one password
// probe and one key probe — between them every op this generation added — and
// validates every params and every result that crossed, in both directions.
func TestTheSSHServiceOpsConformToTheirContractsOverTheWire(t *testing.T) {
	key := newTestKey(t)
	f := newFixture(t, "pw", key.signer)
	coord := &coordinator{
		password: "pw", signer: key.signer,
		verdict: proto.HostKeyUnknown, fingerprint: f.hostKeyFingerprint(),
		expected: "SHA256:stored",
	}
	stand := newStand(t, coord)

	// The password probe first; the key probe follows. BOTH accept on trust,
	// because the scripted coordinator answers `unknown` — first contact — and
	// that is what makes all four reverse ops part of one run: verify, then
	// trust, then the material each auth kind needs.
	password := passwordProbeParams(t, f)
	password.AcceptOnTrust = true
	if _, err := stand.probe(t, password); err != nil {
		t.Fatalf("password probe: %v", err)
	}
	withKey := keyProbeParams(t, f, key.signer)
	withKey.AcceptOnTrust = true
	if _, err := stand.probe(t, withKey); err != nil {
		t.Fatalf("key probe: %v", err)
	}

	// ── what the COORDINATOR sent ──────────────────────────────────────
	probeParams := loadHelperSchema(t, "ssh.probe.params.schema.json")
	probes := 0
	for _, payload := range framesOf(t, stand.toCoord.bytes(), proto.TypeRequest) {
		req := decodeRequest(t, payload)
		if req.Service != proto.ServiceSSH || req.Op != proto.OpProbe {
			t.Fatalf("the coordinator sent %s.%s, want only the probe", req.Service, req.Op)
		}
		if err := validateHelperJSON(probeParams, req.Params); err != nil {
			t.Errorf("probe params off the wire do not satisfy their contract:\n%v\n\npayload was:\n%s", err, req.Params)
		}
		probes++
	}
	if probes != 2 {
		t.Fatalf("recorded %d probe requests, want 2", probes)
	}

	// ── what the HELPER answered, forward direction ────────────────────
	probeResult := loadHelperSchema(t, "ssh.probe.schema.json")
	results := 0
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeResponse) {
		resp := decodeResponse(t, payload)
		if err := validateHelperJSON(probeResult, resp.Result); err != nil {
			t.Errorf("the probe result off the wire does not satisfy its contract:\n%v\n\npayload was:\n%s", err, resp.Result)
		}
		results++
	}
	if results != 2 {
		t.Fatalf("recorded %d probe results, want 2", results)
	}

	// ── what the HELPER asked, reverse direction ───────────────────────
	// Each request's id is what its answer will carry, so the ops are matched
	// by CORRELATION and not by guessing which result shape a payload looks
	// like.
	reverseParams := map[string]*jsonschema.Schema{
		proto.OpSecret:        loadHelperSchema(t, "ssh.secret.params.schema.json"),
		proto.OpSign:          loadHelperSchema(t, "ssh.sign.params.schema.json"),
		proto.OpVerifyHostKey: loadHelperSchema(t, "ssh.verify-host-key.params.schema.json"),
		proto.OpTrustHostKey:  loadHelperSchema(t, "ssh.trust-host-key.params.schema.json"),
	}
	reverseResults := map[string]*jsonschema.Schema{
		proto.OpSecret:        loadHelperSchema(t, "ssh.secret.schema.json"),
		proto.OpSign:          loadHelperSchema(t, "ssh.sign.schema.json"),
		proto.OpVerifyHostKey: loadHelperSchema(t, "ssh.verify-host-key.schema.json"),
		proto.OpTrustHostKey:  loadHelperSchema(t, "ssh.trust-host-key.schema.json"),
	}
	opByID := map[uint64]string{}
	asked := map[string]bool{}
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeRequest) {
		req := decodeRequest(t, payload)
		schema, ok := reverseParams[req.Op]
		if !ok {
			t.Fatalf("the helper asked %s.%s, which is not one of this service's reverse ops", req.Service, req.Op)
		}
		if err := validateHelperJSON(schema, req.Params); err != nil {
			t.Errorf("%s params off the wire do not satisfy their contract:\n%v\n\npayload was:\n%s", req.Op, err, req.Params)
		}
		opByID[req.ID] = req.Op
		asked[req.Op] = true
	}
	for op := range reverseParams {
		if !asked[op] {
			t.Errorf("no %s request was recorded, so its contract was never checked on the wire", op)
		}
	}

	// ── what the COORDINATOR answered, reverse direction ───────────────
	answered := map[string]bool{}
	for _, payload := range framesOf(t, stand.toCoord.bytes(), proto.TypeResponse) {
		resp := decodeResponse(t, payload)
		op, ok := opByID[resp.ID]
		if !ok {
			t.Fatalf("the coordinator answered reverse request %d, which the helper never sent", resp.ID)
		}
		if resp.Error != nil {
			t.Fatalf("%s was refused: %s", op, resp.Error.Message)
		}
		if err := validateHelperJSON(reverseResults[op], resp.Result); err != nil {
			t.Errorf("%s result off the wire does not satisfy its contract:\n%v\n\npayload was:\n%s", op, err, resp.Result)
		}
		answered[op] = true
	}
	for op := range reverseResults {
		if !answered[op] {
			t.Errorf("no %s result was recorded, so its contract was never checked on the wire", op)
		}
	}
}

// TestProbeOutcomeSpellingsMatchTheSSHVocabulary is the drift guard between the
// wire's closed set and the package that produces it.
//
// The spellings are declared twice on purpose — proto is the wire's leaf and
// may not import internal/ssh, whose x/crypto/ssh and pkg/sftp links would then
// reach the artifact deployed to somebody else's host — so the two are held
// together by a test rather than by luck. A member added to the ssh vocabulary
// without a wire spelling, or spelled differently, fails here.
func TestProbeOutcomeSpellingsMatchTheSSHVocabulary(t *testing.T) {
	fromSSH := []ssh.ProbeOutcome{
		ssh.OutcomeAccepted,
		ssh.OutcomeRejected,
		ssh.OutcomeUnreachable,
		ssh.OutcomeHostKeyUnknown,
		ssh.OutcomeHostKeyChanged,
		ssh.OutcomeNeedsInteractive,
	}
	onWire := map[proto.ProbeOutcome]bool{
		proto.ProbeAccepted:         true,
		proto.ProbeRejected:         true,
		proto.ProbeUnreachable:      true,
		proto.ProbeHostKeyUnknown:   true,
		proto.ProbeHostKeyChanged:   true,
		proto.ProbeNeedsInteractive: true,
	}
	if len(onWire) != len(fromSSH) {
		t.Fatalf("the wire spells %d outcomes and the ssh vocabulary has %d values", len(onWire), len(fromSSH))
	}
	for _, outcome := range fromSSH {
		if !onWire[proto.ProbeOutcome(outcome)] {
			t.Errorf("ssh spells the outcome %q and the wire does not", string(outcome))
		}
	}
}

// TestAVerdictThisHelperDoesNotKnowIsRefusedAsItsOwnFailure is the regression a
// mutation found: an unrecognised verdict used to come back as `rejected`,
// because x/crypto wraps whatever a callback returned as "handshake failed" and
// the dial seam reads that as an authentication failure. A protocol mistake in
// this process reported as "the server refused your credential" would send a
// person to check a host that is not the problem.
func TestAVerdictThisHelperDoesNotKnowIsRefusedAsItsOwnFailure(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	// A verb a generation nobody has written yet could answer with.
	coord := &coordinator{
		password: "pw", verdict: proto.HostKeyVerdict("probably-fine"), fingerprint: f.hostKeyFingerprint(),
	}
	stand := newStand(t, coord)

	result, err := stand.probe(t, passwordProbeParams(t, f))
	if err == nil {
		t.Fatalf("an unrecognised host-key verdict was answered as %q (%s)", result.Outcome, result.Detail)
	}
	if code := refusalCode(err); code != proto.ErrCodeInternal {
		t.Fatalf("refusal code = %q (err %v), want %q", code, err, proto.ErrCodeInternal)
	}
	if passwords, _ := f.authAttempts(); len(passwords) != 0 {
		t.Fatalf("the credential was offered despite the refused verdict: %q", passwords)
	}
}

// TestAProbeToAPortNothingListensOnIsUnreachable is the reachability half of
// the outcome vocabulary, end to end: the classification a coordinator sees
// from the helper is the one its own probe would have produced, and the reverse
// channel is not even consulted, because a connection that never opens has no
// key to ask about.
func TestAProbeToAPortNothingListensOnIsUnreachable(t *testing.T) {
	coord := &coordinator{
		password: "pw", verdict: proto.HostKeyTrusted, fingerprint: "SHA256:whatever",
	}
	stand := newStand(t, coord)

	params := proto.ProbeParams{
		// Reserved port 1 on loopback: nothing listens there, and the failure
		// is a refused connection rather than a timeout.
		Host: "127.0.0.1", Port: 1, User: "test",
		Identity: proto.SSHIdentity{
			Credential: proto.SSHCredential{Ref: wantRef},
			Auth:       proto.SSHAuthPassword,
		},
	}
	result, err := stand.probe(t, params)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.Outcome != proto.ProbeUnreachable {
		t.Fatalf("outcome = %q (%s), want unreachable", result.Outcome, result.Detail)
	}
	if asked := coord.asked(); len(asked) != 0 {
		t.Fatalf("the coordinator was asked %v by a connection that never opened", asked)
	}
}

func decodeRequest(t *testing.T, payload []byte) proto.Request {
	t.Helper()
	var req proto.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request from the wire: %v", err)
	}
	return req
}

func decodeResponse(t *testing.T, payload []byte) proto.Response {
	t.Helper()
	var resp proto.Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("decode response from the wire: %v", err)
	}
	return resp
}
