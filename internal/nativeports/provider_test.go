package nativeports

// Provider tests: every failure branch is exercised with an injected read
// (AGENTS.md rule 3), and the paired "on an ordinary machine it succeeds"
// test is the real-machine one that opens a listener in the test itself and
// asserts the provider lists it — it cannot pass against a stale table.
import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/portsprobe"
)

func testProvider(read func(ctx context.Context) ([]portsprobe.Listener, error)) *Provider {
	return &Provider{read: read}
}

// TestProvider_UnsupportedPlatform_IsUnavailable: an unsupported OS maps to
// the unavailable state — a sentence the panel already renders — never to a
// convincing empty list.
func TestProvider_UnsupportedPlatform_IsUnavailable(t *testing.T) {
	p := testProvider(func(context.Context) ([]portsprobe.Listener, error) {
		return nil, ErrUnsupported
	})
	s := p.Sample(context.Background())
	if s.State != portsprobe.StateUnavailable {
		t.Fatalf("state = %q, want unavailable", s.State)
	}
	if len(s.Listeners) != 0 {
		t.Fatalf("listeners = %d, want 0 (unsupported is could-not-determine, never a table)", len(s.Listeners))
	}
	if !strings.Contains(s.Classification, "not supported") {
		t.Errorf("classification = %q, want the platform mention", s.Classification)
	}
	if s.Probe != "" {
		t.Errorf("probe = %q, want empty on failure", s.Probe)
	}
}

// TestProvider_ToolMissing_IsUnavailable: darwin's lsof absent is terminal,
// like an unsupported platform — retrying cannot conjure the tool.
func TestProvider_ToolMissing_IsUnavailable(t *testing.T) {
	p := testProvider(func(context.Context) ([]portsprobe.Listener, error) {
		return nil, ErrToolMissing
	})
	s := p.Sample(context.Background())
	if s.State != portsprobe.StateUnavailable {
		t.Fatalf("state = %q, want unavailable", s.State)
	}
	if !strings.Contains(s.Classification, "no listener tool") {
		t.Errorf("classification = %q, want the tool mention", s.Classification)
	}
}

// TestProvider_ReadFailure_IsFailedTransiently: a generic read failure is
// failed-transiently with the cause named — the cadence retries it.
func TestProvider_ReadFailure_IsFailedTransiently(t *testing.T) {
	p := testProvider(func(context.Context) ([]portsprobe.Listener, error) {
		return nil, errors.New("procfs exploded")
	})
	s := p.Sample(context.Background())
	if s.State != portsprobe.StateFailedTransiently {
		t.Fatalf("state = %q, want failed-transiently", s.State)
	}
	if !strings.Contains(s.Classification, "procfs exploded") {
		t.Errorf("classification = %q, want the cause", s.Classification)
	}
}

// TestProvider_Success_ProjectsEvidence: the provider projects through the
// same SampleState the remote ladder uses, so available vs available-limited
// means the same thing on both transports.
func TestProvider_Success_ProjectsEvidence(t *testing.T) {
	known := portsprobe.Listener{Port: 1, Process: portsprobe.Process{Evidence: portsprobe.EvidenceKnown, Name: "a", PID: 1}}
	denied := portsprobe.Listener{Port: 2, Process: portsprobe.Process{Evidence: portsprobe.EvidencePermissionDenied}}

	tests := []struct {
		name string
		in   []portsprobe.Listener
		want portsprobe.State
	}{
		{"mixed evidence", []portsprobe.Listener{known, denied}, portsprobe.StateAvailable},
		{"all permission-denied", []portsprobe.Listener{denied, denied}, portsprobe.StateAvailableLimited},
		{"empty table", []portsprobe.Listener{}, portsprobe.StateAvailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := testProvider(func(context.Context) ([]portsprobe.Listener, error) { return tc.in, nil })
			s := p.Sample(context.Background())
			if s.State != tc.want {
				t.Fatalf("state = %q, want %q", s.State, tc.want)
			}
			if s.Probe != probeName {
				t.Errorf("probe = %q, want %q", s.Probe, probeName)
			}
		})
	}
}

// TestProvider_ListsPortOpenedByThisTest is the acceptance check (the
// brief's freshness criterion): the local provider lists the machine's
// listening ports and the test asserts a port the test itself opened, so it
// cannot pass against a stale table. The evidence for the test's own
// listener must be known — the owner walk sees the test process.
func TestProvider_ListsPortOpenedByThisTest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot open a test listener: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("test listener address is %T, want *net.TCPAddr", ln.Addr())
	}
	port := addr.Port

	p := NewProvider(log.NewSlogAdapter(nil))
	s := p.Sample(context.Background())
	if s.State != portsprobe.StateAvailable && s.State != portsprobe.StateAvailableLimited {
		t.Fatalf("state = %q, want available/available-limited (classification=%q)",
			s.State, s.Classification)
	}
	found := false
	for _, l := range s.Listeners {
		if l.Port == port && l.Address == "127.0.0.1" {
			found = true
			if l.Process.Evidence != portsprobe.EvidenceKnown || l.Process.PID != os.Getpid() {
				t.Errorf("test listener evidence = %+v, want known with the test pid %d",
					l.Process, os.Getpid())
			}
		}
	}
	if !found {
		// Two very different failures used to arrive as one sentence, and the
		// macOS runner has been failing here with no way to tell which
		// (nocx-ou3e). An EMPTY table is a capability failure — the machine's
		// listing tool told us nothing, and SampleState calls that
		// "available" because a successful empty result is a legitimate
		// answer, so the state assertion above passes and says nothing. A
		// POPULATED table missing this one port is a parse or filter defect,
		// and the sample is the evidence for which.
		if len(s.Listeners) == 0 {
			t.Fatalf("the provider returned an EMPTY table on this machine (state=%q classification=%q), "+
				"so it cannot see the port this test opened (%d) or any other. "+
				"That is the listing capability failing, not this port going missing: "+
				"on darwin the provider shells out to %s, which lists nothing when it is absent, "+
				"refused, or unable to read the network table for this user",
				s.State, s.Classification, port, lsofPathForDiagnostics())
		}
		var sample []string
		for i, l := range s.Listeners {
			if i == 5 {
				break
			}
			sample = append(sample, fmt.Sprintf("%s:%d(pid=%d,ev=%s)", l.Address, l.Port, l.Process.PID, l.Process.Evidence))
		}
		t.Fatalf("provider listed %d ports but not the one this test opened (%d) — "+
			"the table is populated, so this is a parse or filter defect, not a missing capability; "+
			"state=%q classification=%q first entries: %s",
			len(s.Listeners), port, s.State, s.Classification, strings.Join(sample, " "))
	}
}
