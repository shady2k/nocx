package client_test

// nocx-oalo1.13.2's wire contract, in the three checks contracts/README.md
// names: the DTO against the schema, and the REAL answer off a REAL socket for
// the half a test-built payload cannot prove — that `listeners` and
// `probesTried` are `[]` and never `null` on the wire, which is the difference
// between "nothing listens" and "no answer" for the panel that renders it.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/portsprobe"
	portshostsvc "github.com/shady2k/nocx/internal/portsprobe/hostsvc"
)

// oneToolRunner is a host whose own ss answers whatever the test asked for.
type oneToolRunner struct {
	program string
	result  *portsprobe.Result
}

func (r oneToolRunner) Run(_ context.Context, c portsprobe.Command) (*portsprobe.Result, error) {
	if c.Argv[0] == r.program {
		return r.result, nil
	}
	return nil, fmt.Errorf("%w: %s", portsprobe.ErrCommandAbsent, c.Argv[0])
}

const portsFixture = `LISTEN 0      128          0.0.0.0:22    0.0.0.0:* users:(("sshd",pid=1,fd=3))`

func TestPortsSampleDTOConformsToContract(t *testing.T) {
	schema := loadHelperSchema(t, "ports.sample.schema.json")
	params := loadHelperSchema(t, "ports.sample.params.schema.json")

	if err := validateHelperJSON(params, mustMarshal(t, proto.PortsSampleParams{})); err != nil {
		t.Errorf("the sample's params do not satisfy their contract: %v", err)
	}

	for _, tc := range []struct {
		name string
		in   proto.PortsSampleResult
	}{
		{"a host with one listener", proto.PortsSampleResult{
			State:          proto.PortsStateAvailable,
			Listeners:      []proto.PortsListener{{Family: "ipv4", Address: "0.0.0.0", Port: 22, Process: proto.PortsProcess{Evidence: "known", Name: "sshd", PID: 1}}},
			Probe:          "ss",
			ProbesTried:    []string{"ss"},
			Classification: "",
			Stderr:         "",
		}},
		{"a host with no tool at all", proto.PortsSampleResult{
			State:          proto.PortsStateUnavailable,
			Listeners:      []proto.PortsListener{},
			ProbesTried:    []string{"ss", "netstat", "lsof", "sockstat"},
			Classification: "no probe tool usable on this host",
		}},
		{"a truncated pass", proto.PortsSampleResult{
			State:          proto.PortsStateFailedTransiently,
			Listeners:      []proto.PortsListener{},
			ProbesTried:    []string{"ss"},
			Classification: "probe output incomplete",
			Stderr:         "ss: invalid option",
		}},
	} {
		if err := validateHelperJSON(schema, mustMarshal(t, tc.in)); err != nil {
			t.Errorf("%s does not satisfy the ports.sample contract: %v", tc.name, err)
		}
	}

	// The contract is exact: a null list, a state the ladder cannot answer, and
	// a field the DTO does not carry are all refused. Without this half a
	// schema would be a description rather than a fence.
	refused := []struct {
		name string
		body string
	}{
		{"null listeners", `{"state":"available","listeners":null,"probe":"ss","probesTried":["ss"],"classification":"","stderr":""}`},
		{"null probesTried", `{"state":"available","listeners":[],"probe":"","probesTried":null,"classification":"","stderr":""}`},
		{"a state the ladder does not have", `{"state":"permission-or-policy-refused","listeners":[],"probe":"","probesTried":[],"classification":"","stderr":""}`},
		{"an extra field", `{"state":"available","listeners":[],"probe":"","probesTried":[],"classification":"","stderr":"","host":"example"}`},
		{"a missing classification", `{"state":"available","listeners":[],"probe":"","probesTried":[],"stderr":""}`},
	}
	for _, tc := range refused {
		if err := validateHelperJSON(schema, []byte(tc.body)); err == nil {
			t.Errorf("%s was accepted by the ports.sample contract", tc.name)
		}
	}
}

func TestPortsSampleOverTheWireConformsToContract(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runner  oneToolRunner
		state   string
		wantRaw []string
	}{
		{
			name:    "a host whose ss answers",
			runner:  oneToolRunner{program: "ss", result: &portsprobe.Result{Stdout: []byte(portsFixture)}},
			state:   proto.PortsStateAvailable,
			wantRaw: []string{`"state":"available"`},
		},
		{
			name:   "a host with nothing to sample with",
			runner: oneToolRunner{program: "nothing-installed"},
			state:  proto.PortsStateUnavailable,
			// The whole point: the list is [] on the wire, never null.
			wantRaw: []string{`"listeners":[]`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadHelperSchema(t, "ports.sample.schema.json")
			c, raw := dialPortsHelper(t, tc.runner)

			var sample proto.PortsSampleResult
			if err := c.Call(context.Background(), proto.ServicePorts, proto.OpPortsSample, proto.PortsSampleParams{}, &sample); err != nil {
				t.Fatalf("%s.%s: %v", proto.ServicePorts, proto.OpPortsSample, err)
			}
			if sample.State != tc.state {
				t.Fatalf("state = %q (%s), want %q", sample.State, sample.Classification, tc.state)
			}
			if err := validateHelperJSON(schema, mustMarshal(t, sample)); err != nil {
				t.Fatalf("the answer off the socket does not satisfy ports.sample: %v", err)
			}
			for _, want := range tc.wantRaw {
				if !strings.Contains(raw.captured(), want) {
					t.Fatalf("the frame on the wire %s does not carry %s", raw.captured(), want)
				}
			}
		})
	}
}

// frameRecorder keeps the helper's own writes, so a test can read the BYTES of
// the answer rather than the struct the client decoded them into — a client
// that normalised nil to [] would hide exactly the defect this checks.
type frameRecorder struct {
	w   net.Conn
	mu  sync.Mutex
	got []byte
}

func (r *frameRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.got = append(r.got, p...)
	r.mu.Unlock()
	return r.w.Write(p)
}

// captured is everything the helper wrote on this connection. The assertions
// are over the whole capture rather than a frame boundary: the strings they
// look for are a field name and its value, which the hello-ok cannot spell.
func (r *frameRecorder) captured() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.got)
}

// dialPortsHelper stands up one real helper with the ports service on it and
// dials it with the real client.
func dialPortsHelper(t *testing.T, runner portsprobe.Runner) (*client.Client, *frameRecorder) {
	t.Helper()
	helperEnd, coordEnd := net.Pipe()
	rec := &frameRecorder{w: helperEnd}
	h := host.New(helperEnd, rec, "testhash", "instance-1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.Register(portshostsvc.New(runner))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Serve(ctx) }()

	c, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(coordEnd),
		ExpectHash:  "testhash",
		SentinelTTL: 5 * time.Second,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec
}
