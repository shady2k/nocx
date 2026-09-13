package ssh

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// recordingInstaller is the smallest ssh.RemoteInstaller double that records
// what the capability hands it: the live *gossh.Client. The SFTP behaviour
// itself is shellintegration's fixture; here the contract under test is that
// the capability OWNS the dial-and-call — the client never leaves internal/ssh.
//
// It is handed no remote home, and that is the point of the shape: the home
// question travels with the transport (ssh.RemoteInstaller), so the party that
// asks it is the one holding the client — which is what this double stands in
// for.
type recordingInstaller struct {
	mu         sync.Mutex
	clientSeen *gossh.Client
	removed    []string
	conflicts  []string
}

func (r *recordingInstaller) EnsureInstalledRemote(context.Context, string, ...ConnectOption) error {
	return nil
}

func (r *recordingInstaller) UninstallRemote(_ context.Context, c *gossh.Client) ([]string, []string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clientSeen = c
	return r.removed, r.conflicts, nil
}

// uninstallConnectOpts is the dial plumbing every UninstallIntegration test
// needs: the same authorization a Connect uses.
func uninstallConnectOpts(t *testing.T, srv *testSSHServer) []ConnectOption {
	t.Helper()
	t.Cleanup(srv.close)
	return []ConnectOption{
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
	}
}

// newUninstallClient builds a RealClient that trusts the test server's host
// key and answers config resolution with the stub.
func newUninstallClient(t *testing.T, srv *testSSHServer) *RealClient {
	t.Helper()
	khPath := writeKnownHosts(t, srv, srv.addr)
	rc, err := NewReal(log.NewSlogAdapter(nil),
		WithConfigResolver(NewStubConfigResolver()), WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// TestUninstallIntegration_OwnsTheDialAndCall: the capability acquires a
// pooled connection the way Connect does, hands the live connection to the
// carrier, and returns the two lists it answered with — the recording double
// proves the carrier saw a live client and nothing else.
func TestUninstallIntegration_OwnsTheDialAndCall(t *testing.T) {
	srv := startTestSSHServer(t)
	rc := newUninstallClient(t, srv)

	rec := &recordingInstaller{removed: []string{"manifest.json"}, conflicts: []string{"integration/v10/nocx.bash"}}
	opts := append(uninstallConnectOpts(t, srv), WithRemoteInstaller(rec))

	removed, conflicts, err := rc.UninstallIntegration(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("UninstallIntegration: %v", err)
	}
	if len(removed) != 1 || removed[0] != "manifest.json" {
		t.Errorf("removed = %v, want [manifest.json]", removed)
	}
	if len(conflicts) != 1 || conflicts[0] != "integration/v10/nocx.bash" {
		t.Errorf("conflicts = %v, want [integration/v10/nocx.bash]", conflicts)
	}
	rec.mu.Lock()
	live := rec.clientSeen != nil
	rec.mu.Unlock()
	if !live {
		t.Error("the carrier was not handed a live pooled client, so the removal had no connection to run on")
	}
}

// TestUninstallIntegration_NoInstallerRefuses: without a carrier in the
// ConnectConfig nothing can be removed, and the refusal is an error — never
// an empty success.
func TestUninstallIntegration_NoInstallerRefuses(t *testing.T) {
	srv := startTestSSHServer(t)
	rc := newUninstallClient(t, srv)

	_, _, err := rc.UninstallIntegration(context.Background(), srv.addr, uninstallConnectOpts(t, srv)...)
	if err == nil {
		t.Fatal("UninstallIntegration without a RemoteInstaller succeeded")
	}
}

// TestUninstallIntegration_CarrierRefusalIsReported: a carrier that cannot
// complete the removal reports it, and the capability neither swallows it nor
// answers with an empty success. The home question lives inside the carrier
// now (it is the party holding the client, and the commands are
// internal/remoteprobe's), so this is the failure that used to be a failed
// home read — same contract, one interface narrower.
func TestUninstallIntegration_CarrierRefusalIsReported(t *testing.T) {
	srv := startTestSSHServer(t)
	rc := newUninstallClient(t, srv)

	refusing := &refusingUninstaller{err: errors.New("the far side would not read the home directory")}

	removed, conflicts, err := rc.UninstallIntegration(context.Background(), srv.addr, append(uninstallConnectOpts(t, srv), WithRemoteInstaller(refusing))...)
	if err == nil {
		t.Fatal("UninstallIntegration with a refusing carrier succeeded")
	}
	if removed != nil || conflicts != nil {
		t.Errorf("a refused removal answered with lists (removed=%v conflicts=%v), want none", removed, conflicts)
	}
	if !strings.Contains(err.Error(), "the far side would not read the home directory") {
		t.Errorf("the refusal is %q, want the carrier's own cause in it — a failure nobody can read is a failure nobody can act on", err)
	}
}

// refusingUninstaller is a carrier whose removal fails.
type refusingUninstaller struct {
	err error
}

func (f *refusingUninstaller) EnsureInstalledRemote(context.Context, string, ...ConnectOption) error {
	return nil
}

func (f *refusingUninstaller) UninstallRemote(context.Context, *gossh.Client) ([]string, []string, error) {
	return nil, nil, f.err
}
