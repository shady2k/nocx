//go:build nocx_local_ssh

package sshsvc_test

// The third contract check (contracts/helper/README.md), applied to the named
// probes: the payloads OFF THE WIRE, not the structs a test built.
//
// A separate file from the ssh service's own contract test because it is a
// separate subject: that one drives the probe op and the four reverse ops the
// helper asks, and this one drives the lease and the five probes the coordinator
// asks. Both directions, every op, and — the point of the check — validating
// what each side actually SENT through the real framing rather than a value the
// test marshalled itself.

import (
	"context"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
)

// TestTheLeaseAndEveryProbeConformToTheirContractsOverTheWire runs one lease and
// one of every named probe, then validates every params and every result that
// crossed.
func TestTheLeaseAndEveryProbeConformToTheirContractsOverTheWire(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) {
		return "NOCX-PD/1\nLISTEN 0 511 0.0.0.0:6768 0.0.0.0:*\nNOCX-PD/1\n", "", 0, false
	})
	lease := acquireLease(t, stand, f)
	ctx := context.Background()

	if _, err := lease.Uname(ctx); err != nil {
		t.Fatalf("uname: %v", err)
	}
	if _, err := lease.Home(ctx); err != nil {
		t.Fatalf("home: %v", err)
	}
	if _, err := lease.SamplePorts(ctx, remoteprobe.PortSS); err != nil {
		t.Fatalf("sample-ports: %v", err)
	}
	if _, err := lease.Completion(ctx, "/etc", "ls pas", 6, 20, "nonce-1"); err != nil {
		t.Fatalf("completion: %v", err)
	}
	if _, err := lease.CommandNames(ctx, remoteprobe.CommandNamesScan, "nonce-2"); err != nil {
		t.Fatalf("command-names: %v", err)
	}
	// Released HERE rather than left to the test's cleanup: the unlease is one
	// of the ops whose contract this test checks on the wire, and a release
	// that happened after the checks would leave its params unvalidated.
	if err := lease.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}

	params := map[string]*jsonschema.Schema{
		proto.OpLease:        loadHelperSchema(t, "ssh.lease.params.schema.json"),
		proto.OpUnlease:      loadHelperSchema(t, "ssh.unlease.params.schema.json"),
		proto.OpUname:        loadHelperSchema(t, "ssh.uname.params.schema.json"),
		proto.OpHome:         loadHelperSchema(t, "ssh.home.params.schema.json"),
		proto.OpSamplePorts:  loadHelperSchema(t, "ssh.sample-ports.params.schema.json"),
		proto.OpCompletion:   loadHelperSchema(t, "ssh.completion.params.schema.json"),
		proto.OpCommandNames: loadHelperSchema(t, "ssh.command-names.params.schema.json"),
	}
	results := map[string]*jsonschema.Schema{
		proto.OpLease:        loadHelperSchema(t, "ssh.lease.schema.json"),
		proto.OpUnlease:      loadHelperSchema(t, "ssh.unlease.schema.json"),
		proto.OpUname:        loadHelperSchema(t, "ssh.uname.schema.json"),
		proto.OpHome:         loadHelperSchema(t, "ssh.home.schema.json"),
		proto.OpSamplePorts:  loadHelperSchema(t, "ssh.sample-ports.schema.json"),
		proto.OpCompletion:   loadHelperSchema(t, "ssh.completion.schema.json"),
		proto.OpCommandNames: loadHelperSchema(t, "ssh.command-names.schema.json"),
	}

	// ── what the COORDINATOR sent ──────────────────────────────────────
	opByID := map[uint64]string{}
	sent := map[string]bool{}
	for _, payload := range framesOf(t, stand.toCoord.bytes(), proto.TypeRequest) {
		req := decodeRequest(t, payload)
		if req.Service != proto.ServiceSSH {
			t.Fatalf("the coordinator sent %s.%s, want only the ssh service", req.Service, req.Op)
		}
		schema, ok := params[req.Op]
		if !ok {
			t.Fatalf("the coordinator sent %s.%s, which is not one of the probe ops", req.Service, req.Op)
		}
		if err := validateHelperJSON(schema, req.Params); err != nil {
			t.Errorf("%s params off the wire do not satisfy their contract:\n%v\n\npayload was:\n%s", req.Op, err, req.Params)
		}
		opByID[req.ID] = req.Op
		sent[req.Op] = true
	}
	for op := range params {
		if !sent[op] {
			t.Errorf("no %s request was recorded, so its contract was never checked on the wire", op)
		}
	}

	// ── what the HELPER answered ───────────────────────────────────────
	answered := map[string]bool{}
	for _, payload := range framesOf(t, stand.toHelper.bytes(), proto.TypeResponse) {
		resp := decodeResponse(t, payload)
		op, ok := opByID[resp.ID]
		if !ok {
			t.Fatalf("the helper answered request %d, which the coordinator never sent", resp.ID)
		}
		if resp.Error != nil {
			t.Fatalf("%s was refused: %s", op, resp.Error.Message)
		}
		if err := validateHelperJSON(results[op], resp.Result); err != nil {
			t.Errorf("%s result off the wire does not satisfy its contract:\n%v\n\npayload was:\n%s", op, err, resp.Result)
		}
		answered[op] = true
	}
	for op := range results {
		if !answered[op] {
			t.Errorf("no %s result was recorded, so its contract was never checked on the wire", op)
		}
	}
}
