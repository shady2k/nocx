package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/workspace"
)

// factorySession is a minimal session.Session: the factory reads Kind, Host
// and SSHOptions and nothing else, so everything below them answers the
// zero value rather than pretending to a terminal this double does not have.
//
// host and opts are the two the factory actually reads, and they are fields
// because the acceptance test for a remote Files provider needs a real
// destination and the connect options a locked profile would carry; every
// other test leaves them zero.
type factorySession struct {
	kind session.Kind
	host string
	opts []ssh.ConnectOption
	pane string
}

func (s factorySession) ID() session.ID             { return "s1" }
func (s factorySession) Identity() session.Identity { return session.Identity{} }
func (s factorySession) Parent() (session.Ref, bool) {
	return session.Ref{}, false
}

func (s factorySession) WorkspaceID() workspace.ID { return workspace.Default }

// OpenedAt answers the zero time for the same reason every method above it
// answers a zero value: the factory never asks. The floor exists so "this
// session's blocks" is expressible (session.go), and this double records no
// blocks.
func (s factorySession) OpenedAt() time.Time { return time.Time{} }

func (s factorySession) Kind() session.Kind        { return s.kind }
func (s factorySession) PaneID() string            { return s.pane }
func (s factorySession) Host() string              { return s.host }
func (s factorySession) Cwd() string               { return "/" }
func (s factorySession) ProfileID() string         { return "" }
func (s factorySession) CredentialID() string      { return "" }
func (s factorySession) Write([]byte) (int, error) { return 0, nil }
func (s factorySession) EnqueueWrite([]byte) bool  { return true }

// EnqueueInputIf answers "queued" and settles at once: this double has no
// queue to validate against.
func (s factorySession) EnqueueInputIf(p []byte, holds func() bool, settle func(written bool, err error)) bool {
	if holds != nil && !holds() {
		if settle != nil {
			settle(false, nil)
		}
		return true
	}
	if settle != nil {
		settle(true, nil)
	}
	return true
}
func (s factorySession) Close() error                    { return nil }
func (s factorySession) Done() <-chan struct{}           { return make(chan struct{}) }
func (s factorySession) SSHOptions() []ssh.ConnectOption { return s.opts }
func (s factorySession) EffectiveSize() session.Size     { return session.DefaultSize() }

func (s factorySession) Resize(context.Context, session.Size) error {
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

func (s factorySession) HostKeyFingerprint() string { return "" }

// refusingLease stands where the SFTP lease provider does. The remote half
// of these tests never gets a lease — the point is what the LOCAL branch
// builds — and a lease that refuses is how "the remote path was not
// disturbed" stays observable without a live server.
type refusingLease struct{ err error }

func noFilesPaneConnection(string, string) (string, bool) { return "", false }

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
	factory := filesystemProviderFactory(refusingLease{err: errors.New("no lease in this test")}, nil, noFilesPaneConnection)

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
	factory := filesystemProviderFactory(refusingLease{err: errors.New("no lease in this test")}, nil, noFilesPaneConnection)
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
	factory := filesystemProviderFactory(refusingLease{err: sentinel}, nil, noFilesPaneConnection)

	p, err := factory(factorySession{kind: session.KindRemote}, "")

	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v, want the lease's own refusal — a remote session must not fall into the local branch", err)
	}
	if p != nil {
		t.Errorf("provider %v, want none when the lease could not be had", p)
	}
}

type filesystemFarRegistry struct {
	state farhelper.State
	asked []string
}

func (r *filesystemFarRegistry) Axis(id string) (farhelper.Status, bool) {
	return farhelper.Status{State: r.state, Reason: farhelper.ReasonConsentDenied, Action: farhelper.ActionNone}, id == "original"
}

func (*filesystemFarRegistry) Watch(func(string, farhelper.Status)) func() { return func() {} }

func (r *filesystemFarRegistry) Acquire(_ context.Context, id string) (*farhelper.Lease, error) {
	r.asked = append(r.asked, id)
	return nil, &farhelper.Unavailable{Reason: farhelper.ReasonConsentDenied, Action: farhelper.ActionNone}
}

func TestFilesystemProviderFactorySelectsServingHelperAndPinsConnection(t *testing.T) {
	sftpFailure := errors.New("SFTP fallback requested")
	sess := factorySession{kind: session.KindRemote, pane: "pane", host: "example.test"}
	for _, state := range []farhelper.State{farhelper.StateStarting, farhelper.StatePaused, farhelper.StateUnavailable, farhelper.StateServing} {
		t.Run(string(state), func(t *testing.T) {
			registry := &filesystemFarRegistry{state: state}
			current := "original"
			factory := filesystemProviderFactory(refusingLease{err: sftpFailure}, registry, func(pane, sessionID string) (string, bool) {
				return current, pane == sess.PaneID() && sessionID == string(sess.ID())
			})
			p, err := factory(sess, "/project")
			if state != farhelper.StateServing {
				if p != nil || !errors.Is(err, sftpFailure) {
					t.Fatalf("factory: %v, %v; want SFTP fallback", p, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			attested, ok := p.(*endpointAttestedProvider)
			if !ok || attested.EndpointID() != endpointIDFor(sess) || attested.Sink() == nil || attested.Source() == nil {
				t.Fatalf("factory returned %T without the remote attestation or transfer seams", p)
			}
			p = attested

			// The axis and the conditional write must survive the same wrapping
			// the seams above had to survive: the composition site asserts these
			// on what the factory returned, which is the wrapper, so a wrapper
			// that carries only the embedded interface's methods would leave the
			// panel with no reason to draw and the write with no version to check
			// — while every other files.* call kept working.
			axis, ok := any(p).(filesystem.AxisProvider)
			if !ok {
				t.Fatal("the factory's remote provider carries no axis; C28's reason could never reach a panel")
			}
			got, known := axis.HelperAxis()
			if !known || got.State != string(farhelper.StateServing) {
				t.Fatalf("HelperAxis = %+v, %v; want the serving axis the registry publishes", got, known)
			}
			versioned, ok := any(p).(filesystem.VersionedProvider)
			if !ok || versioned.Versioned() == nil {
				t.Fatal("the factory's helper-backed provider carries no conditional write")
			}

			var unavailable *filesystem.ErrUnavailable
			if _, err := p.Root(t.Context()); !errors.As(err, &unavailable) || unavailable.Reason != "consent-denied" {
				t.Fatalf("Root: %v; want registry refusal", err)
			}
			current = "replacement"
			if _, err := p.Root(t.Context()); !errors.As(err, &unavailable) || unavailable.Reason != "connection" {
				t.Fatalf("Root after pane moved: %v; want connection refusal", err)
			}
			// The axis follows the PANE's binding, not only the connection's
			// lifetime: the connection is still in the registry, and a panel that
			// went on drawing its old state would name a helper this tab is no
			// longer behind.
			if moved, _ := axis.HelperAxis(); moved.Reason != "connection" {
				t.Fatalf("HelperAxis after the pane moved = %+v; want the connection reason", moved)
			}
			if len(registry.asked) != 1 || registry.asked[0] != "original" {
				t.Fatalf("acquisitions = %v; want [original]", registry.asked)
			}
		})
	}
}
