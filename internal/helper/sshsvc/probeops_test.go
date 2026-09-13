//go:build nocx_local_ssh

package sshsvc_test

// The acceptance half of the named probes (nocx-50w7p.9): every probe the
// coordinator used to run by composing a command runs HERE, as a typed op on a
// lease, against a real ssh server whose shell really executes it.
//
// Each refusal is paired with the success it is the refusal OF, in the same test
// or the adjacent one: a refusal alone cannot be told apart from an op that
// never worked, and "the host refused exec" and "the helper never sent one"
// produce the same red.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/waittest"
)

// leaseParams builds the lease request for the fixture host, with the password
// credential the scripted coordinator answers for.
func leaseParams(t *testing.T, f *fixture, acceptOnTrust bool) proto.LeaseParams {
	t.Helper()
	probe := passwordProbeParams(t, f)
	return proto.LeaseParams{
		Destination:   probe.Destination,
		AcceptOnTrust: acceptOnTrust,
	}
}

// probeStand is a helper with a real ssh server beyond it whose shell runs the
// commands it is asked to.
func probeStand(t *testing.T, f *fixture, handler func(cmd string) (stdout, stderr string, exit int, refuse bool)) *stand {
	t.Helper()
	f.setExecHandler(handler)
	coord := &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()}
	return newStand(t, coord)
}

func acquireLease(t *testing.T, s *stand, f *fixture) *client.ProbeLease {
	t.Helper()
	lease, err := s.client.AcquireProbeLease(context.Background(), leaseParams(t, f, true))
	if err != nil {
		t.Fatalf("AcquireProbeLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease
}

// TestEveryNamedProbeRunsItsOwnFixedCommandOverTheWire is the criterion's core:
// five probes, five named ops, and the host sees exactly the command each name
// means — spelled by internal/remoteprobe, which is the package the helper
// composes from and the coordinator parses alongside.
func TestEveryNamedProbeRunsItsOwnFixedCommandOverTheWire(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	var mu sync.Mutex
	seen := map[string]int{}
	stand := probeStand(t, f, func(cmd string) (string, string, int, bool) {
		mu.Lock()
		seen[cmd]++
		mu.Unlock()
		switch {
		// The completion script itself mentions $HOME, so the framing markers
		// are what identify its command and the fixed word is checked last.
		case strings.Contains(cmd, "NOCXEOF_"):
			return "NONCE:n1:START\npath\tfile.txt\t/tmp/file.txt\t0\nNONCE:n1:END\n", "", 0, false
		case strings.Contains(cmd, "NOCX_CN"):
			return "NOCX_CN n2 BEGIN\nN ls\nNOCX_CN n2 END\n", "", 0, false
		case strings.Contains(cmd, "NOCX-PD/1"):
			return "NOCX-PD/1\nLISTEN 0 511 0.0.0.0:6768 0.0.0.0:*\nNOCX-PD/1\n", "", 0, false
		case strings.Contains(cmd, "uname"):
			return "Linux x86_64\n", "", 0, false
		default:
			return "/home/deploy\n", "", 0, false
		}
	})

	lease := acquireLease(t, stand, f)
	ctx := context.Background()

	uname, err := lease.Uname(ctx)
	if err != nil {
		t.Fatalf("uname: %v", err)
	}
	if string(uname.Stdout) != "Linux x86_64\n" {
		t.Fatalf("uname stdout = %q", uname.Stdout)
	}

	home, err := lease.Home(ctx)
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if home != "/home/deploy" {
		t.Fatalf("home = %q, want the trimmed answer to the home question", home)
	}

	ports, err := lease.SamplePorts(ctx, remoteprobe.PortSS)
	if err != nil {
		t.Fatalf("sample-ports: %v", err)
	}
	if !strings.Contains(string(ports.Stdout), "0.0.0.0:6768") {
		t.Fatalf("sample-ports stdout = %q", ports.Stdout)
	}

	completion, err := lease.Completion(ctx, "/etc", "ls pas", 6, 20, "n1")
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	if !strings.Contains(string(completion.Stdout), "NONCE:n1:START") {
		t.Fatalf("completion stdout = %q", completion.Stdout)
	}

	names, err := lease.CommandNames(ctx, remoteprobe.CommandNamesProbe, "n2")
	if err != nil {
		t.Fatalf("command-names: %v", err)
	}
	if !strings.Contains(string(names.Stdout), "NOCX_CN n2 BEGIN") {
		t.Fatalf("command-names stdout = %q", names.Stdout)
	}

	// What the host was ASKED is the contract: each name is one command, and the
	// text is the leaf package's — no caller-supplied string is in it.
	ran := f.ran()
	wantCommands := map[string]string{
		remoteprobe.UnameCommand: "uname",
		remoteprobe.HomeCommand:  "home",
	}
	for _, probe := range []remoteprobe.PortProbe{remoteprobe.PortSS} {
		cmd, ok := remoteprobe.PortCommand(probe)
		if !ok {
			t.Fatalf("no command for %q", probe)
		}
		wantCommands[cmd] = "sample-ports"
	}
	if len(ran) != 5 {
		t.Fatalf("the host ran %d commands, want 5: %q", len(ran), ran)
	}
	for cmd, what := range wantCommands {
		if !contains(ran, cmd) {
			t.Errorf("the %s probe's own command never reached the host; ran: %q", what, ran)
		}
	}
	if !strings.Contains(ran[3], "NOCXEOF_") || !strings.HasPrefix(ran[3], "bash -s -- '/etc' 'ls pas' 6 20 ") {
		t.Errorf("the completion command was not the composed one: %q", ran[3])
	}
	if !strings.HasPrefix(ran[4], "sh -s n2") {
		t.Errorf("the enumeration command was not the composed one: %q", ran[4])
	}

	// ONE connection for five probes: the lease is what makes a sample cost no
	// handshake, and AD-4's pool is what the probes ride.
	if got := f.connections(); got != 1 {
		t.Fatalf("the host was dialed %d times for five probes on one lease, want 1", got)
	}
}

// TestAnExitStatusIsAnAnswerNotARefusal: 127 means the tool is missing, and
// every consumer classifies that itself. A helper that folded it into a failure
// would report "discovery is broken" where the answer is "this host has no ss".
func TestAnExitStatusIsAnAnswerNotARefusal(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) {
		return "NOCX-PD/1\nNOCX-PD/1\n", "sh: ss: not found\n", 127, false
	})
	lease := acquireLease(t, stand, f)

	result, err := lease.SamplePorts(context.Background(), remoteprobe.PortSS)
	if err != nil {
		t.Fatalf("a probe whose command exited 127 was reported as a failure: %v", err)
	}
	if result.ExitStatus != 127 {
		t.Fatalf("exit status = %d, want 127 carried as an ANSWER", result.ExitStatus)
	}
	if !strings.Contains(string(result.Stderr), "not found") {
		t.Fatalf("stderr = %q, want the shell's own words", result.Stderr)
	}
}

// TestAnExecTheHostRefusesIsTheProhibitedRefusal is the failure path for a
// restricted shell: the exec request is answered false, and the caller gets the
// code it acts on rather than a transport error.
func TestAnExecTheHostRefusesIsTheProhibitedRefusal(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) { return "", "", 0, true })
	lease := acquireLease(t, stand, f)

	_, err := lease.Uname(context.Background())
	if code := refusalCode(err); code != proto.ErrCodeExecProhibited {
		t.Fatalf("refusal = %q (%v), want %q", code, err, proto.ErrCodeExecProhibited)
	}
}

// TestASessionTheHostWillNotGiveIsTheSessionRefusedRefusal is the MaxSessions
// case: the far side gives no session channel, which is a fact about the host's
// policy rather than about the command.
func TestASessionTheHostWillNotGiveIsTheSessionRefusedRefusal(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) { return "", "", 0, false })
	lease := acquireLease(t, stand, f)
	f.setSessionRefusal(true)

	_, err := lease.Uname(context.Background())
	if code := refusalCode(err); code != proto.ErrCodeExecSessionRefused {
		t.Fatalf("refusal = %q (%v), want %q", code, err, proto.ErrCodeExecSessionRefused)
	}
}

// TestAProbeTooLargeToSendIsRefusedByNocx: a command that would die in the
// remote execve at MAX_ARG_STRLEN is refused by name instead, and NOTHING
// reaches the host.
func TestAProbeTooLargeToSendIsRefusedByNocx(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) { return "", "", 0, false })
	lease := acquireLease(t, stand, f)

	_, err := lease.Completion(context.Background(), "/tmp", strings.Repeat("x", remoteprobe.MaxCommandLen), 0, 20, "n")
	if code := refusalCode(err); code != proto.ErrCodeExecTooLong {
		t.Fatalf("refusal = %q (%v), want %q", code, err, proto.ErrCodeExecTooLong)
	}
	if ran := f.ran(); len(ran) != 0 {
		t.Fatalf("the host ran %q; an over-long probe must be refused before it is sent", ran)
	}
}

// TestAnAnswerPastTheCaptureBoundIsTruncated: a hostile or wedged command must
// stop at the bound, and the caller learns the answer is a PREFIX. For port
// discovery that is the difference between "no listeners" and "I could not see
// them".
func TestAnAnswerPastTheCaptureBoundIsTruncated(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	big := strings.Repeat("NOCX-PD/1\n", remoteprobe.MaxOutputBytes/4)
	stand := probeStand(t, f, func(string) (string, string, int, bool) { return big, "", 0, false })
	lease := acquireLease(t, stand, f)

	result, err := lease.SamplePorts(context.Background(), remoteprobe.PortSS)
	if err != nil {
		t.Fatalf("sample-ports: %v", err)
	}
	if !result.Truncated {
		t.Fatalf("an answer of %d bytes was not marked truncated", len(result.Stdout))
	}
	if len(result.Stdout) > remoteprobe.MaxOutputBytes {
		t.Fatalf("captured %d bytes, over the %d-byte bound", len(result.Stdout), remoteprobe.MaxOutputBytes)
	}
}

// TestAReleaseEndsTheLeaseAndItsRunningProbe: unlease drops the pooled
// reference and tears down whatever is still running on it. Closing the session
// is the only thing that stops a remote exec, so a caller that gave up must not
// leave a command running on somebody's host.
func TestAReleaseEndsTheLeaseAndItsRunningProbe(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	started := make(chan struct{})
	stand := probeStand(t, f, func(string) (string, string, int, bool) {
		close(started)
		select {} // never answers: the release has to stop it
	})
	lease := acquireLease(t, stand, f)

	probeDone := make(chan error, 1)
	go func() {
		_, err := lease.Uname(context.Background())
		probeDone <- err
	}()
	<-started

	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waittest.WaitFor(t, "the in-flight probe to end with the lease", func() bool {
		select {
		case err := <-probeDone:
			if err == nil {
				t.Error("a probe on a released lease reported success")
			}
			return true
		default:
			return false
		}
	})

	// And the probe after it is refused as a CLOSED lease — the caller's own
	// act, not a fact about the host — rather than answered from a connection
	// nobody holds. (The wire-level "this helper holds no such lease" case is
	// the next test.)
	_, err := lease.Uname(context.Background())
	var probeErr *remoteprobe.Error
	if !errors.As(err, &probeErr) || probeErr.Kind != remoteprobe.KindLeaseClosed {
		t.Fatalf("probe after release = %v, want the lease-closed kind", err)
	}
}

// TestAProbeOnALeaseThisHelperDoesNotHoldIsLost: a helper that restarted holds
// nothing, and the honest answer is that the connection the id named is gone —
// not a bad-request refusal, which would send a caller looking at its own code.
func TestAProbeOnALeaseThisHelperDoesNotHoldIsLost(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) { return "", "", 0, false })
	lease := acquireLease(t, stand, f)

	if err := lease.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var out proto.ProbeExecResult
	err := stand.client.Call(context.Background(), proto.ServiceSSH, proto.OpUname,
		proto.UnameParams{ProbeRunParams: proto.ProbeRunParams{Lease: lease.ID()}}, &out)
	if code := refusalCode(err); code != proto.ErrCodeExecLost {
		t.Fatalf("refusal = %q (%v), want %q", code, err, proto.ErrCodeExecLost)
	}
}

// TestAnUnknownProbeNameIsRefusedBeforeAnythingRuns: the closed set is closed at
// the service too, not only in its schema — a member nobody implemented is a bad
// request and not a command invented on the spot.
func TestAnUnknownProbeNameIsRefusedBeforeAnythingRuns(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) { return "", "", 0, false })
	lease := acquireLease(t, stand, f)

	var out proto.ProbeExecResult
	err := stand.client.Call(context.Background(), proto.ServiceSSH, proto.OpSamplePorts,
		proto.SamplePortsParams{
			ProbeRunParams: proto.ProbeRunParams{Lease: lease.ID()},
			Probe:          proto.PortProbe("nmap"),
		}, &out)
	if code := refusalCode(err); code != proto.ErrCodeBadParams {
		t.Fatalf("refusal = %q (%v), want %q", code, err, proto.ErrCodeBadParams)
	}
	if ran := f.ran(); len(ran) != 0 {
		t.Fatalf("the host ran %q for a probe that does not exist", ran)
	}
}

// TestTheProbePlaneAgreesWithTheLeafSpellings keeps the two vocabularies in
// step: the wire's closed set and the leaf package's commands are one fact, and
// a member added to one without the other is exactly how a probe silently stops
// existing.
func TestTheProbePlaneAgreesWithTheLeafSpellings(t *testing.T) {
	for _, probe := range []remoteprobe.PortProbe{
		remoteprobe.PortSS, remoteprobe.PortNetstat, remoteprobe.PortBusyboxNetstat,
		remoteprobe.PortLsof, remoteprobe.PortSockstat,
	} {
		if _, ok := remoteprobe.PortCommand(probe); !ok {
			t.Errorf("the ladder names %q and remoteprobe has no command for it", probe)
		}
		if proto.PortProbe(probe) == "" {
			t.Errorf("the wire has no member for %q", probe)
		}
	}
	for _, phase := range []remoteprobe.CommandNamesPhase{remoteprobe.CommandNamesProbe, remoteprobe.CommandNamesScan} {
		if _, ok := remoteprobe.CommandNamesCommand(phase, "n"); !ok {
			t.Errorf("the enumeration names %q and remoteprobe has no command for it", phase)
		}
		if proto.CommandNamesPhase(phase) == "" {
			t.Errorf("the wire has no member for %q", phase)
		}
	}
}
