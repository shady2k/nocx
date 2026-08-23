package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/workspace"
)

// factorySession is a minimal session.Session: the factory reads Kind, Host
// and SSHOptions and nothing else, so everything below them answers the
// zero value rather than pretending to a terminal this double does not have.
type factorySession struct {
	kind session.Kind
}

func (s factorySession) ID() session.ID             { return "s1" }
func (s factorySession) Identity() session.Identity { return session.Identity{} }
func (s factorySession) Parent() (session.Ref, bool) {
	return session.Ref{}, false
}

func (s factorySession) Liveness() session.LivenessState {
	return session.LivenessState{Liveness: session.LivenessAlive, Epoch: 1}
}
func (s factorySession) WorkspaceID() workspace.ID       { return workspace.Default }
func (s factorySession) Kind() session.Kind              { return s.kind }
func (s factorySession) PaneID() string                  { return "" }
func (s factorySession) Host() string                    { return "" }
func (s factorySession) Cwd() string                     { return "/" }
func (s factorySession) ProfileID() string               { return "" }
func (s factorySession) CredentialID() string            { return "" }
func (s factorySession) Write([]byte) (int, error)       { return 0, nil }
func (s factorySession) EnqueueWrite([]byte) bool        { return true }
func (s factorySession) Close() error                    { return nil }
func (s factorySession) Done() <-chan struct{}           { return make(chan struct{}) }
func (s factorySession) SSHOptions() []ssh.ConnectOption { return nil }
func (s factorySession) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}

func (s factorySession) StartOutput(context.Context, session.OutputHandler) error {
	return nil
}

func (s factorySession) OpenBootstrapWindow() (session.BootstrapWindow, error) {
	return nil, errors.New("factorySession has no terminal")
}
func (s factorySession) ShellIntegrationReason() ssh.RefusalReason { return "" }
func (s factorySession) ExitOutcome() (session.ExitCause, int) {
	return session.ExitInterrupted, 0
}

// refusingLease stands where the SFTP lease provider does. The remote half
// of these tests never gets a lease — the point is what the LOCAL branch
// builds — and a lease that refuses is how "the remote path was not
// disturbed" stays observable without a live server.
type refusingLease struct{ err error }

func (l refusingLease) FSConn(context.Context, string, ...ssh.ConnectOption) (ssh.FSConn, error) {
	return nil, l.err
}

// TestFactory_ALocalSessionGetsAWritableProvider is the composition-root
// half of D7 as corrected. It is not a restatement of the local package's
// own test: what the product depends on is that the value THIS FACTORY
// returns carries the seam, because internal/capability asserts
// filesystem.Uploader on exactly that value and a wrapper or a narrowing
// interface anywhere between here and there would silently drop it — the
// same failure endpointAttestedProvider's test guards on the remote side.
func TestFactory_ALocalSessionGetsAWritableProvider(t *testing.T) {
	factory := filesystemProviderFactory(refusingLease{err: errors.New("no lease in this test")})

	p, err := factory(factorySession{kind: session.KindLocal}, "")
	if err != nil {
		t.Fatalf("factory for a local session: %v", err)
	}
	u, ok := any(p).(filesystem.Uploader)
	if !ok {
		t.Fatal("the local provider the factory returns carries no write seam; a browser drop on a local tab would refuse every upload while every other files.* call worked")
	}
	if u.Sink() == nil {
		t.Fatal("Sink() is nil — a live Uploader that hands back none is a capability that refuses without saying why")
	}
}

// TestFactory_TheLocalSinkWritesOntoThisMachine is the end of the wire this
// worker owns: the sink the factory's provider hands back actually puts the
// bytes on the backend's own disk, which is the machine a local tab's shell
// is on (R1). A seam that exists and writes nowhere would pass the
// assertion above.
func TestFactory_TheLocalSinkWritesOntoThisMachine(t *testing.T) {
	factory := filesystemProviderFactory(refusingLease{err: errors.New("no lease in this test")})
	p, err := factory(factorySession{kind: session.KindLocal}, "")
	if err != nil {
		t.Fatalf("factory for a local session: %v", err)
	}
	dir := t.TempDir()

	u, ok := any(p).(filesystem.Uploader)
	if !ok {
		t.Fatal("the local provider the factory returns carries no write seam")
	}
	out, err := u.Sink().Put(
		context.Background(),
		transfer.Upload{DestDir: dir, Name: "dropped.txt", Size: 5, OnExists: transfer.Overwrite},
		strings.NewReader("hello"),
		nil,
	)
	if err != nil {
		t.Fatalf("Put through the factory's local sink: %v", err)
	}
	if out.State != transfer.StateWritten {
		t.Fatalf("outcome %+v, want written", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "dropped.txt")) //nolint:gosec // a path this test built under its own t.TempDir()
	if err != nil || string(b) != "hello" {
		t.Fatalf("file is %q (%v), want the dropped bytes on this machine", b, err)
	}
}

// TestFactory_ARemoteSessionStillGoesToTheLease is the "the other path is
// unchanged" half the brief asks for, stated the only way it can be without
// a server: a remote session does not take the local branch — it asks for a
// lease, and the lease's refusal is what comes back.
func TestFactory_ARemoteSessionStillGoesToTheLease(t *testing.T) {
	sentinel := errors.New("the lease was asked for")
	factory := filesystemProviderFactory(refusingLease{err: sentinel})

	p, err := factory(factorySession{kind: session.KindRemote}, "")

	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v, want the lease's own refusal — a remote session must not fall into the local branch", err)
	}
	if p != nil {
		t.Errorf("provider %v, want none when the lease could not be had", p)
	}
}
