// Package hostsvc is the remote helper's `ports` service: the host's own
// listening sockets, read by the host's own tools (design §5.10,
// nocx-oalo1.13.2).
//
// It is a mapping and nothing else. The ladder, its order, the classification
// of a failed rung and the backoff all live in internal/portsprobe — the
// package the far helper may link (C30) — and this file only turns a
// portsprobe.Sample into the wire's own spelling of it. A second
// implementation of the classification here would be two answers to one
// question, agreeing everywhere anyone looked.
//
// ONE DETECTOR PER ACCEPTED CONNECTION, and that is the same division
// internal/git/hostsvc keeps: the accept loop constructs a service per
// connection, so the probe selection each connection's ladder learned dies
// with it. A detector shared across connections would let one machine's
// missing ss decide another connection's rungs, which is a fact about the
// host and not about the caller — but the CACHE is per connection because
// the deployment's generations are.
package hostsvc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/portsprobe"
)

// Service is the helper's "ports" service.
type Service struct {
	detector *portsprobe.Detector
}

// New builds the ports service over the runner that executes the ladder's
// rungs. The runner is the whole of the service's execution surface: the
// composition root passes portsprobe.NewExecRunner(), and a test injects the
// host it wants to pretend to be. A nil runner would be a service with no
// ladder at all, which is a programming error rather than a mode.
func New(runner portsprobe.Runner) *Service {
	return &Service{detector: portsprobe.NewDetector(runner)}
}

// Name is the reserved service name the host registers it under.
func (s *Service) Name() string { return proto.ServicePorts }

// Ops is the closed set of operations this service serves: one sample, and no
// arguments to it.
func (s *Service) Ops() []string { return []string{proto.OpPortsSample} }

// ParamsSchema declares each op's params type; the host decodes a request's
// params through it and audits it for argv at registration (D3). The sample's
// params are empty, which is the audit's easiest case: there is nothing here
// that could name a command.
func (s *Service) ParamsSchema(op string) *host.Schema {
	if op == proto.OpPortsSample {
		return host.SchemaFor(proto.PortsSampleParams{})
	}
	return nil
}

// Call dispatches one named operation. A sample runs under the request's
// context, so a caller that gives up stops the host's tool rather than leaving
// it running for nobody.
func (s *Service) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	if op != proto.OpPortsSample {
		return nil, fmt.Errorf("hostsvc: no op %q", op)
	}
	res := s.detector.Sample(ctx)
	if res.Canceled {
		// A withdrawn request has no sample to report. Answering anyway would
		// send the carry-over state — `pending`, which the contract does not
		// have — to a caller who has already stopped listening; the refusal is
		// what the host sends instead of a value outside the enum.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	}
	return sampleResult(res), nil
}

// sampleResult is the wire's spelling of one sample. The two list fields are
// the contract's "never null" half: the ladder hands back nil for an empty
// pass, and a decoder must not have to tell "nothing listens" from "no answer".
func sampleResult(s portsprobe.Sample) proto.PortsSampleResult {
	listeners := make([]proto.PortsListener, 0, len(s.Listeners))
	for _, l := range s.Listeners {
		listeners = append(listeners, proto.PortsListener{
			Family:  string(l.Family),
			Address: l.Address,
			Port:    l.Port,
			Process: proto.PortsProcess{
				Evidence: string(l.Process.Evidence),
				Name:     l.Process.Name,
				PID:      l.Process.PID,
			},
		})
	}
	tried := append([]string{}, s.ProbesTried...)
	return proto.PortsSampleResult{
		State:          string(s.State),
		Listeners:      listeners,
		Probe:          s.Probe,
		ProbesTried:    tried,
		Classification: s.Classification,
		Stderr:         s.Stderr,
	}
}
