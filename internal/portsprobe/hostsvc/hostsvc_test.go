package hostsvc_test

// The ports service is a mapping, so what these tests pin is the mapping: a
// sample's vocabulary becomes the wire's, and the two list fields the contract
// says are never null arrive as [] on this side whatever the ladder handed back.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/portsprobe"
	"github.com/shady2k/nocx/internal/portsprobe/hostsvc"
)

// toolRunner answers one program and reports every other as absent.
type toolRunner struct {
	program string
	result  *portsprobe.Result
}

func (r toolRunner) Run(_ context.Context, c portsprobe.Command) (*portsprobe.Result, error) {
	if c.Argv[0] == r.program {
		return r.result, nil
	}
	return nil, fmt.Errorf("%w: %s", portsprobe.ErrCommandAbsent, c.Argv[0])
}

const ssFixture = `LISTEN 0      511          0.0.0.0:6768  0.0.0.0:* users:(("orca-ide",pid=871,fd=81))
LISTEN 0      128             [::]:22       [::]:*`

func TestTheServiceMapsASampleOntoTheWire(t *testing.T) {
	svc := hostsvc.New(toolRunner{program: "ss", result: &portsprobe.Result{Stdout: []byte(ssFixture)}})
	if svc.Name() != proto.ServicePorts {
		t.Fatalf("name = %q, want %q", svc.Name(), proto.ServicePorts)
	}
	if ops := svc.Ops(); len(ops) != 1 || ops[0] != proto.OpPortsSample {
		t.Fatalf("ops = %v, want [%s]", ops, proto.OpPortsSample)
	}

	out, err := svc.Call(context.Background(), proto.OpPortsSample, nil)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	sample, ok := out.(proto.PortsSampleResult)
	if !ok {
		t.Fatalf("sample answered %T, want proto.PortsSampleResult", out)
	}
	if sample.State != proto.PortsStateAvailable {
		t.Fatalf("state = %q, want available", sample.State)
	}
	if sample.Probe != "ss" || len(sample.ProbesTried) != 1 || sample.ProbesTried[0] != "ss" {
		t.Fatalf("probe/probesTried = %q/%v, want ss/[ss]", sample.Probe, sample.ProbesTried)
	}
	if len(sample.Listeners) != 2 {
		t.Fatalf("listeners = %+v, want the fixture's two rows", sample.Listeners)
	}
	first := sample.Listeners[0]
	if first.Family != "ipv4" || first.Address != "0.0.0.0" || first.Port != 6768 {
		t.Fatalf("first listener = %+v, want 0.0.0.0:6768", first)
	}
	if first.Process.Evidence != "known" || first.Process.Name != "orca-ide" || first.Process.PID != 871 {
		t.Fatalf("first process = %+v, want the known owner ss printed", first.Process)
	}
	if second := sample.Listeners[1]; second.Family != "ipv6" || second.Process.Evidence != "permission-denied" {
		t.Fatalf("second listener = %+v, want ipv6 with permission-denied evidence", second)
	}
}

// TestAnUnavailableHostAnswersEmptyListsRatherThanNull is scenario 8 at the
// service boundary: a host with no tool at all is an ANSWER, and its two lists
// are [] because the contract says a reader must not tell "nothing" from "no
// answer".
func TestAnUnavailableHostAnswersEmptyListsRatherThanNull(t *testing.T) {
	svc := hostsvc.New(toolRunner{program: "nothing-installed"})

	out, err := svc.Call(context.Background(), proto.OpPortsSample, nil)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	sample, ok := out.(proto.PortsSampleResult)
	if !ok {
		t.Fatalf("sample answered %T, want proto.PortsSampleResult", out)
	}
	if sample.State != proto.PortsStateUnavailable {
		t.Fatalf("state = %q, want unavailable", sample.State)
	}
	if sample.Listeners == nil || len(sample.Listeners) != 0 {
		t.Fatalf("listeners = %#v, want an empty non-nil slice", sample.Listeners)
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"listeners":[]`) {
		t.Fatalf("marshalled sample %s does not carry an empty listener list", raw)
	}
}

func TestAnUnknownOpIsRefused(t *testing.T) {
	svc := hostsvc.New(toolRunner{})
	if _, err := svc.Call(context.Background(), "stat", nil); err == nil {
		t.Fatal("an op this service does not have was answered")
	}
	if schema := svc.ParamsSchema("stat"); schema != nil {
		t.Fatal("an op this service does not have declared a params schema")
	}
}

// TestTheSampleTakesNoParams is D3's easiest case: the request cannot name a
// command because there is nothing in it to name one with.
func TestTheSampleTakesNoParams(t *testing.T) {
	schema := hostsvc.New(toolRunner{}).ParamsSchema(proto.OpPortsSample)
	if schema == nil {
		t.Fatal("the sample declared no params schema")
	}
	if fields := schema.Fields(); len(fields) != 0 {
		t.Fatalf("sample params carry %v, want none", fields)
	}
	if _, err := schema.Decode(nil); err != nil {
		t.Fatalf("an omitted params payload was refused: %v", err)
	}
}
