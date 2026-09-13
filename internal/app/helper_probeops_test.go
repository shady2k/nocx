package app

// The composition root's half of the named probes (nocx-50w7p.9), at the two
// points where being wrong is expensive and invisible:
//
//   - the destination handed to the helper must be the RESOLVED one, carrying
//     the session's own route. A provider that dropped the options would turn a
//     jump-routed completion into a direct dial to the target, which the old
//     adapter test existed to prevent and which nothing else would report.
//
//   - the ONE exec-shaped seam left in this coordinator must stay one command
//     wide. `platformLease.Exec` is what the install path's probe calls; it
//     answers the fixed uname command from a typed op and refuses everything
//     else HERE, before a helper is even reached. The pair of these is the
//     behavioural half of the ratchet that says no coordinator path issues a
//     free-form exec over ssh.

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/ssh"
)

// ── a helper that records what it was asked for ────────────────────────

type recordingHelperSource struct {
	asked []proto.LeaseParams
	lease *fakeProbeCommands
}

func (s *recordingHelperSource) probeHelper(context.Context) (probeHelper, error) {
	return s, nil
}

func (s *recordingHelperSource) AcquireProbeLease(_ context.Context, params proto.LeaseParams) (probeCommands, error) {
	s.asked = append(s.asked, params)
	if s.lease == nil {
		s.lease = &fakeProbeCommands{}
	}
	return s.lease, nil
}

type recordingResolver struct {
	host string
	opts []ssh.ConnectOption
	got  ssh.ConnectConfig
}

func (r *recordingResolver) ResolveTarget(_ context.Context, host string, opts ...ssh.ConnectOption) (ssh.DialTarget, error) {
	r.host = host
	r.opts = opts
	for _, o := range opts {
		o(&r.got)
	}
	return ssh.DialTarget{
		DialEndpoint: ssh.DialEndpoint{
			Host:           "10.0.0.7",
			Port:           2222,
			User:           "deploy",
			Auth:           ssh.DialAuthPassword,
			Credential:     ssh.VaultRef("cred-9"),
			KnownHostsAddr: "nocx-v1-route-digest:22",
		},
		// One hop, because the profile above names one: the resolved route has
		// to reach the helper with the destination, and this is the assertion
		// that it does.
		Route: []ssh.DialEndpoint{{
			Host: "bastion.example", Port: 2200, User: "jumper",
			Auth:       ssh.DialAuthPassword,
			Credential: ssh.VaultRef("jump-cred"),
		}},
	}, nil
}

// TestTheHelperIsHandedTheResolvedDestination is the composition check: an alias
// and a jump route go IN, an address and a resolved account come OUT, and the
// helper is told what to authenticate with.
func TestTheHelperIsHandedTheResolvedDestination(t *testing.T) {
	resolver := &recordingResolver{}
	helper := &recordingHelperSource{}
	probes := &helperProbes{local: helper, resolve: resolver}

	if _, err := probes.HelperCompletionProvider(
		ssh.WithUser("alice"),
		ssh.WithPort(2222),
		ssh.WithJumpHost("bastion.example", 2200, "jumper", "password"),
	)(context.Background(), "target.example"); err != nil {
		t.Fatalf("completion lease: %v", err)
	}

	if resolver.host != "target.example" {
		t.Fatalf("resolved host = %q, want the session's own host", resolver.host)
	}
	// The session's route, not a fresh one: a dropped option is the jump route
	// lost, and the answer would be a direct dial to a host only the bastion can
	// reach.
	if resolver.got.User != "alice" || resolver.got.Port != 2222 {
		t.Fatalf("session options lost: %+v", resolver.got)
	}
	if resolver.got.JumpHost != "bastion.example" || resolver.got.JumpPort != 2200 || resolver.got.JumpUser != "jumper" {
		t.Fatalf("jump route lost: %+v", resolver.got)
	}

	if len(helper.asked) != 1 {
		t.Fatalf("leases asked for = %d, want 1", len(helper.asked))
	}
	got := helper.asked[0]
	if got.Destination.Host != "10.0.0.7" || got.Destination.Port != 2222 || got.Destination.User != "deploy" {
		t.Fatalf("the helper was handed %+v, want the RESOLVED destination", got.Destination)
	}
	if got.Destination.Identity.Credential.Ref != ssh.VaultRef("cred-9").String() {
		t.Fatalf("credential reference = %q, want the coordinator's own handle", got.Destination.Identity.Credential.Ref)
	}
	// The ROUTE travels with the destination, and a hop carries its own
	// account and its own credential reference: a lease that dropped the hops
	// would dial an address only the bastion can reach.
	if len(got.Destination.Jumps) != 1 {
		t.Fatalf("the helper was handed %d hops, want 1", len(got.Destination.Jumps))
	}
	hop := got.Destination.Jumps[0]
	if hop.Host != "bastion.example" || hop.Port != 2200 || hop.User != "jumper" {
		t.Fatalf("hop = %s@%s:%d, want jumper@bastion.example:2200", hop.User, hop.Host, hop.Port)
	}
	if hop.Identity.Credential.Ref != ssh.VaultRef("jump-cred").String() {
		t.Fatalf("hop credential = %q, want the hop's own reference", hop.Identity.Credential.Ref)
	}
	// The storage identity of a routed destination is not its dial address, and
	// the helper is told which one to look the key up under.
	if got.Destination.KnownHostsAddr == "" || got.Destination.KnownHostsAddr == net.JoinHostPort("10.0.0.7", "2222") {
		t.Fatalf("routed destination storage identity = %q", got.Destination.KnownHostsAddr)
	}
	if got.Destination.Identity.Auth != proto.SSHAuthKind(ssh.DialAuthPassword) {
		t.Fatalf("auth kind = %q", got.Destination.Identity.Auth)
	}
	// A probe raises no accept sheet: the question "may this key be recorded"
	// belongs to an act a person watches, and these are questions the product
	// asked itself.
	if got.AcceptOnTrust {
		t.Fatal("a probe asked for accept-on-trust, which would record a host key behind a user's back")
	}
}

// ── the one command-wide exec seam ─────────────────────────────────────

type fakeProbeCommands struct {
	unameCalls int
	result     *remoteprobe.Result
	err        error
}

func (f *fakeProbeCommands) Uname(context.Context) (*remoteprobe.Result, error) {
	f.unameCalls++
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &remoteprobe.Result{Stdout: []byte("Linux x86_64\n")}, nil
}

func (f *fakeProbeCommands) Home(context.Context) (string, error) {
	return "", errors.New("not asked")
}

func (f *fakeProbeCommands) SamplePorts(context.Context, remoteprobe.PortProbe) (*remoteprobe.Result, error) {
	return nil, errors.New("not asked")
}

func (f *fakeProbeCommands) Completion(context.Context, string, string, int, int, string) (*remoteprobe.Result, error) {
	return nil, errors.New("not asked")
}

func (f *fakeProbeCommands) CommandNames(context.Context, remoteprobe.CommandNamesPhase, string) (*remoteprobe.Result, error) {
	return nil, errors.New("not asked")
}
func (f *fakeProbeCommands) Fingerprint() string   { return "SHA256:host" }
func (f *fakeProbeCommands) Done() <-chan struct{} { return nil }
func (f *fakeProbeCommands) LostErr() error        { return nil }
func (f *fakeProbeCommands) Close() error          { return nil }

// TestThePlatformProbeRunsItsOneNamedCommandAndNothingElse is the bound the
// exec-shaped seam is held to.
//
// deploy's probe passes exactly one command, spelled once in internal/remoteprobe,
// and it is answered from the helper's typed `ssh.uname` op. Everything else is
// refused here — including a command that is nearly right — and the proof that it
// is refused BEFORE anything is sent is that the lease is never asked for a
// uname: a caller cannot use this path to run something else on somebody's
// machine.
func TestThePlatformProbeRunsItsOneNamedCommandAndNothingElse(t *testing.T) {
	lease := &fakeProbeCommands{
		result: &remoteprobe.Result{Stdout: []byte("Darwin arm64\n"), Stderr: []byte("warning\n"), ExitStatus: 3, Truncated: true},
	}
	platform := &platformLease{helperProbeLease: &helperProbeLease{lease: lease}, host: "host.example"}

	got, err := platform.Exec(context.Background(), remoteprobe.UnameCommand)
	if err != nil {
		t.Fatalf("the platform probe's own command was refused: %v", err)
	}
	if string(got.Stdout) != "Darwin arm64\n" || string(got.Stderr) != "warning\n" || got.ExitStatus != 3 || !got.Truncated {
		t.Fatalf("result = %+v, want the probe's answer field for field", got)
	}
	if lease.unameCalls != 1 {
		t.Fatalf("uname calls = %d, want 1", lease.unameCalls)
	}

	for _, cmd := range []string{
		"uname -s",        // nearly right
		"uname -s -m; id", // right prefix, more besides
		"rm -rf /",        // the shape the ban exists for
		"",                // nothing
	} {
		if _, err := platform.Exec(context.Background(), cmd); err == nil {
			t.Errorf("%q was accepted by the platform seam", cmd)
		}
		if lease.unameCalls != 1 {
			t.Fatalf("%q reached the lease: uname calls = %d, want 1 — a refused command must not be sent anywhere",
				cmd, lease.unameCalls)
		}
	}
}

// TestThePlatformSeamReportsTheHostKeyItDialed reads the one value the install
// path keys a consent decision by, off the lease rather than off anything of its
// own.
func TestThePlatformSeamReportsTheHostKeyItDialed(t *testing.T) {
	platform := &platformLease{
		helperProbeLease: &helperProbeLease{lease: &fakeProbeCommands{}},
		host:             "host.example",
	}
	if got := platform.HostKeyFingerprint(); got != "SHA256:host" {
		t.Fatalf("fingerprint = %q, want the lease's own observation", got)
	}
}

// TestTheRefusalKeepsTheCoordinatorsOwnSentence checks the translation the
// install path depends on: a helper's refusal must arrive in the vocabulary this
// process already switches on, and one that carries host-key evidence must
// rebuild the typed error rather than degrade to a sentence.
func TestTheRefusalKeepsTheCoordinatorsOwnSentence(t *testing.T) {
	err := translateProbeRefusal("host.example", refusalWith(string(proto.ProbeHostKeyChanged), map[string]any{
		"addr": "host.example:22", "knownHostsAddr": "host.example:22",
		"algorithm": "ssh-ed25519", "key": "a2V5", "fingerprint": "SHA256:offered", "expected": "SHA256:recorded",
	}))
	var changed *ssh.ErrHostKeyMismatch
	if !errors.As(err, &changed) {
		t.Fatalf("translate = %v (%T), want *ssh.ErrHostKeyMismatch", err, err)
	}
	if changed.Expected != "SHA256:recorded" {
		t.Fatalf("expected = %q, want the value it changed FROM", changed.Expected)
	}
}

// TestAnUnreachableProbeKeepsItsSentence: nothing in this path can classify
// "the network refused you" as a credential problem, and the sentence the helper
// produced is what a person reads.
func TestAnUnreachableProbeKeepsItsSentence(t *testing.T) {
	err := translateProbeRefusal("host.example", refusalWith("unreachable", nil))
	if err == nil || !strings.Contains(err.Error(), "the helper refused") {
		t.Fatalf("err = %v, want the helper's own sentence carried through", err)
	}
}
