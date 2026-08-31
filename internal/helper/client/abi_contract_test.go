package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The three checks contracts/README.md names, applied to the helper ABI, and
// the third is the point: a test that validates a payload the test itself
// built proves the struct is well-formed, not that the wire carries it.
//
// What the third check here does and does not prove, said plainly because the
// difference matters. It drives the REAL host and the REAL client over a real
// socket — the framing, the writer mutex, the response envelope, the chunking
// path and the client's decode — so a shape that survives it survives the
// transport. It does NOT drive the session service: when this file was written
// there was not one, the name being reserved and unbuilt (D15).
//
// There is one now (nocx-k6p18.3), and the schemas below did not change — which
// is the whole claim of a frozen ABI, and the reason this file was left alone
// rather than rewritten around the new service. Its over-the-wire cases live in
// session_service_contract_test.go, beside the ops that needed a PTY to have
// semantics.

const helperContractDir = "../../../contracts/helper"

// loadHelperSchema compiles one schema from contracts/helper, registering
// every file in the directory under its canonical $id first so the cross-file
// $refs into identities.schema.json resolve locally instead of being fetched
// from a network that is not there.
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

// validateHelperJSON decodes with jsonschema's own reader rather than into
// `any`: encoding/json turns every number into a float64, which silently
// loses precision above 2^53 — and a stream offset is 64-bit precisely
// because it goes past that.
func validateHelperJSON(s *jsonschema.Schema, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	return s.Validate(doc)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func sub(s proto.SubscriberID) *proto.SubscriberID { return &s }

var (
	abiSession = proto.HostSessionID{
		Generation: "GJ4S6YVBNQKX2ZHM7TLDCPWA3E",
		Session:    "0123456789abcdef0123456789abcdef",
	}
	abiResumed = proto.Resume{Resumed: true, Reset: false, From: 1 << 40}
	abiReset   = proto.Resume{
		Resumed: false, Reset: true, From: 9000,
		Gap: &proto.Gap{Start: 12, End: 9000, Reason: proto.GapReasonWindow},
	}
)

// ── the DTO's own conformance ──────────────────────────────────────────
// Cheap and fast: field tags, omitempty behaviour, how a pointer renders,
// whether an enum value spells what the schema says. It is not the check that
// catches a missing field.

func TestHelperAttachDTOConformsToContract(t *testing.T) {
	params := loadHelperSchema(t, "session.attach.params.schema.json")
	result := loadHelperSchema(t, "session.attach.schema.json")

	for _, tc := range []struct {
		name string
		in   proto.AttachParams
	}{
		{"a reconnect at its own offset", proto.AttachParams{
			Subscriber: "pane-a", Session: abiSession, Offset: 4096, Fresh: false, RequestWrite: true,
		}},
		{"a fresh reader at a non-zero offset", proto.AttachParams{
			Subscriber: "pane-b", Session: abiSession, Offset: 4096, Fresh: true, RequestWrite: false,
		}},
		{"a fresh reader at the base", proto.AttachParams{
			Subscriber: "pane-c", Session: abiSession, Offset: 0, Fresh: true, RequestWrite: false,
		}},
	} {
		if err := validateHelperJSON(params, mustMarshal(t, tc.in)); err != nil {
			t.Errorf("%s does not satisfy the attach params contract: %v", tc.name, err)
		}
	}

	for _, tc := range []struct {
		name string
		in   proto.AttachResult
	}{
		{"granted the write capability", proto.AttachResult{
			Attachment: "att-1", Resume: abiResumed,
			Write: proto.WriteGrant{Granted: true, Epoch: 1, Holder: nil},
		}},
		{"refused, naming the holder", proto.AttachResult{
			Attachment: "att-2", Resume: abiResumed,
			Write: proto.WriteGrant{Granted: false, Epoch: 0, Holder: sub("pane-a")},
		}},
		{"reset, with the hole it left", proto.AttachResult{
			Attachment: "att-3", Resume: abiReset,
			Write: proto.WriteGrant{Granted: false, Epoch: 0, Holder: nil},
		}},
	} {
		if err := validateHelperJSON(result, mustMarshal(t, tc.in)); err != nil {
			t.Errorf("%s does not satisfy the attach result contract: %v", tc.name, err)
		}
	}
}

func TestHelperAckDetachAndResetDTOsConformToContract(t *testing.T) {
	ack := loadHelperSchema(t, "session.ack.params.schema.json")
	if err := validateHelperJSON(ack, mustMarshal(t, proto.AckParams{
		Subscriber: "pane-a", Session: abiSession, Offset: 1 << 40,
	})); err != nil {
		t.Errorf("ack params: %v", err)
	}

	detachParams := loadHelperSchema(t, "session.detach.params.schema.json")
	if err := validateHelperJSON(detachParams, mustMarshal(t, proto.DetachParams{Attachment: "att-1"})); err != nil {
		t.Errorf("detach params: %v", err)
	}
	detachResult := loadHelperSchema(t, "session.detach.schema.json")
	for _, released := range []bool{true, false} {
		if err := validateHelperJSON(detachResult, mustMarshal(t, proto.DetachResult{ReleasedWrite: released})); err != nil {
			t.Errorf("detach result (releasedWrite=%v): %v", released, err)
		}
	}

	reset := loadHelperSchema(t, "session.reset.schema.json")
	if err := validateHelperJSON(reset, mustMarshal(t, proto.SessionReset{
		Subscriber: "pane-a", Session: abiSession, Resume: abiReset,
	})); err != nil {
		t.Errorf("reset notification: %v", err)
	}
}

// TestGapAndResetSemanticsAreEnforcedByTheContract is the D8 half the bead
// names "gap and reset semantics, specified and tested". The rules live in the
// SCHEMA rather than in a Go predicate on purpose: the decision they describe
// — is the offset still in the window, and where does the reader restart — is
// internal/transport's outputRing.snapshot today and moves into the helper
// with the window itself (nocx-k6p18.3). Writing a second implementation of it
// here would be two owners of one behaviour, agreeing everywhere anybody looks
// and disagreeing somewhere nobody does.
//
// What the contract enforces is the SHAPE of the answer, which is the part the
// wire owns: exactly one of resumed/reset, and a gap present exactly when
// reset is. Each refusal below is a real defect the shape would otherwise
// carry — a reset with no gap hides what was lost, a resume with a gap claims
// a loss that did not happen, and a payload with neither flag is the "did the
// helper not mention reset, or say there was none" ambiguity both booleans
// exist to remove.
func TestGapAndResetSemanticsAreEnforcedByTheContract(t *testing.T) {
	s := loadHelperSchema(t, "session.reset.schema.json")
	envelope := func(resume string) []byte {
		return []byte(`{"subscriber":"pane-a","session":{"generation":"G","session":"0123456789abcdef0123456789abcdef"},"resume":` + resume + `}`)
	}

	accepted := []struct {
		name   string
		resume string
	}{
		{"a resume with no gap", `{"resumed":true,"reset":false,"from":4096}`},
		{"a reset carrying its gap", `{"resumed":false,"reset":true,"from":9000,"gap":{"start":12,"end":9000,"reason":"window"}}`},
	}
	for _, tc := range accepted {
		if err := validateHelperJSON(s, envelope(tc.resume)); err != nil {
			t.Errorf("%s was refused: %v", tc.name, err)
		}
	}

	refused := []struct {
		name   string
		resume string
	}{
		{"a reset with no gap — the loss would be silent", `{"resumed":false,"reset":true,"from":9000}`},
		{"a resume carrying a gap — a loss that did not happen", `{"resumed":true,"reset":false,"from":4096,"gap":{"start":1,"end":2,"reason":"window"}}`},
		{"both flags true", `{"resumed":true,"reset":true,"from":9000,"gap":{"start":1,"end":2,"reason":"window"}}`},
		{"neither flag true", `{"resumed":false,"reset":false,"from":4096}`},
		{"reset omitted entirely", `{"resumed":true,"from":4096}`},
		{"a negative offset", `{"resumed":true,"reset":false,"from":-1}`},
		{"a gap blamed on the recording's cap", `{"resumed":false,"reset":true,"from":9000,"gap":{"start":12,"end":9000,"reason":"cap"}}`},
		{"a gap with no reason at all", `{"resumed":false,"reset":true,"from":9000,"gap":{"start":12,"end":9000}}`},
	}
	for _, tc := range refused {
		if err := validateHelperJSON(s, envelope(tc.resume)); err == nil {
			t.Errorf("%s was accepted by the contract", tc.name)
		}
	}
}

// TestAnAttachmentMayNotStandInForASession is the identity half, checked on
// the wire rather than only in the type system. The Go types make the mistake
// a compile error; this makes it a contract failure too, for the sake of any
// other implementation of this ABI — a generation is reached by whatever
// coordinator is installed, not only by this build.
func TestAnAttachmentMayNotStandInForASession(t *testing.T) {
	params := loadHelperSchema(t, "session.attach.params.schema.json")
	cases := []struct {
		name string
		raw  string
	}{
		{
			"a bare session string where a qualified handle belongs",
			`{"subscriber":"pane-a","session":"0123456789abcdef0123456789abcdef","offset":0,"fresh":true,"requestWrite":false}`,
		},
		{
			"a session with no generation",
			`{"subscriber":"pane-a","session":{"session":"0123456789abcdef0123456789abcdef"},"offset":0,"fresh":true,"requestWrite":false}`,
		},
		{
			"an attach that names an attachment instead of a subscriber",
			`{"attachment":"att-1","session":{"generation":"G","session":"0123456789abcdef0123456789abcdef"},"offset":0,"fresh":true,"requestWrite":false}`,
		},
		{
			"fresh left to be inferred",
			`{"subscriber":"pane-a","session":{"generation":"G","session":"0123456789abcdef0123456789abcdef"},"offset":0,"requestWrite":false}`,
		},
	}
	for _, tc := range cases {
		if err := validateHelperJSON(params, []byte(tc.raw)); err == nil {
			t.Errorf("%s was accepted by the contract", tc.name)
		}
	}
}

// ── the real payload, off the real socket ──────────────────────────────

// abiService answers the frozen ops with the frozen shapes. Its NAME is not
// proto.ServiceSession, and it stays that way now that the name is taken: what
// this drives is the SHAPES and the transport, deliberately without the real
// service behind them, so an attach answer that no implementation happens to
// produce today is still checked against the contract. The real service's own
// over-the-wire cases are in session_service_contract_test.go.
type abiService struct{}

func (abiService) Name() string { return "session-abi-freeze" }

func (abiService) Ops() []string { return []string{proto.OpAttach, proto.OpAck, proto.OpDetach} }

func (abiService) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpAttach:
		return host.SchemaFor(proto.AttachParams{})
	case proto.OpAck:
		return host.SchemaFor(proto.AckParams{})
	case proto.OpDetach:
		return host.SchemaFor(proto.DetachParams{})
	}
	return nil
}

func (abiService) Call(_ context.Context, op string, params json.RawMessage) (any, error) {
	switch op {
	case proto.OpAttach:
		var in proto.AttachParams
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		// A second write request is refused and names the holder; a first
		// one is granted. The lease epoch rises from 1, so zero names no
		// grant.
		write := proto.WriteGrant{Granted: false, Epoch: 0, Holder: nil}
		if in.RequestWrite {
			if in.Subscriber == "pane-a" {
				write = proto.WriteGrant{Granted: true, Epoch: 1}
			} else {
				write = proto.WriteGrant{Granted: false, Epoch: 0, Holder: sub("pane-a")}
			}
		}
		// A reader asking for an offset the window no longer holds is
		// answered with a reset and the hole it left; anything else resumes.
		resume := proto.Resume{Resumed: true, Reset: false, From: in.Offset}
		if in.Offset < 12 {
			resume = proto.Resume{
				Resumed: false, Reset: true, From: 9000,
				Gap: &proto.Gap{Start: in.Offset, End: 9000, Reason: proto.GapReasonWindow},
			}
		}
		return proto.AttachResult{Attachment: proto.AttachmentID("att-" + string(in.Subscriber)), Resume: resume, Write: write}, nil
	case proto.OpAck:
		return struct{}{}, nil
	case proto.OpDetach:
		var in proto.DetachParams
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		return proto.DetachResult{ReleasedWrite: in.Attachment == "att-pane-a"}, nil
	}
	return nil, nil
}

func TestHelperAttachOverTheWireConformsToContract(t *testing.T) {
	conn := newFakeConn(hostPeer("testhash", abiService{}))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	schema := loadHelperSchema(t, "session.attach.schema.json")

	cases := []struct {
		name string
		in   proto.AttachParams
	}{
		{"the writer resumes", proto.AttachParams{
			Subscriber: "pane-a", Session: abiSession, Offset: 4096, Fresh: false, RequestWrite: true,
		}},
		{"a second reader is refused the capability and told who has it", proto.AttachParams{
			Subscriber: "pane-b", Session: abiSession, Offset: 4096, Fresh: true, RequestWrite: true,
		}},
		{"a reader behind the window is reset and told what it lost", proto.AttachParams{
			Subscriber: "pane-c", Session: abiSession, Offset: 0, Fresh: false, RequestWrite: false,
		}},
	}
	for _, tc := range cases {
		var raw json.RawMessage
		if err := c.Call(context.Background(), "session-abi-freeze", proto.OpAttach, tc.in, &raw); err != nil {
			t.Fatalf("%s: Call: %v", tc.name, err)
		}
		if err := validateHelperJSON(schema, raw); err != nil {
			t.Errorf("%s: the result off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", tc.name, err, raw)
		}
	}
}

func TestHelperDetachOverTheWireConformsToContract(t *testing.T) {
	conn := newFakeConn(hostPeer("testhash", abiService{}))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	schema := loadHelperSchema(t, "session.detach.schema.json")
	for _, attachment := range []proto.AttachmentID{"att-pane-a", "att-pane-b"} {
		var raw json.RawMessage
		if err := c.Call(context.Background(), "session-abi-freeze", proto.OpDetach,
			proto.DetachParams{Attachment: attachment}, &raw); err != nil {
			t.Fatalf("detach %s: %v", attachment, err)
		}
		if err := validateHelperJSON(schema, raw); err != nil {
			t.Errorf("detach %s off the socket does not satisfy its contract:\n%v\n\npayload was:\n%s", attachment, err, raw)
		}
	}
}

// TestTheHelperAcceptsTheAttachParamsTheContractDeclares closes the loop the
// other way: the host decodes params through the service's declared type, so a
// payload the contract accepts must be one the helper can actually take. A
// schema nobody's decoder agrees with is theatre.
func TestTheHelperAcceptsTheAttachParamsTheContractDeclares(t *testing.T) {
	conn := newFakeConn(hostPeer("testhash", abiService{}))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	params := loadHelperSchema(t, "session.ack.params.schema.json")
	in := proto.AckParams{Subscriber: "pane-a", Session: abiSession, Offset: 1 << 40}
	if err := validateHelperJSON(params, mustMarshal(t, in)); err != nil {
		t.Fatalf("the ack params the test sends do not satisfy the contract: %v", err)
	}
	if err := c.Call(context.Background(), "session-abi-freeze", proto.OpAck, in, nil); err != nil {
		t.Fatalf("the helper refused params its own contract accepts: %v", err)
	}
}
