package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport"
	"github.com/shady2k/nocx/internal/workspace"
)

// factorySession supplies the identity and kind used by the provider factory.
type factorySession struct {
	kind session.Kind
	host string
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
func (s factorySession) SSHOptions() []ssh.ConnectOption { return nil }
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

// TestFactory_ALocalSessionGetsAWritableProvider is the composition-root
// half of D7 as corrected. It is not a restatement of the local package's
// own test: what the product depends on is that the value THIS FACTORY
// returns carries the seam, because internal/capability asserts
// filesystem.Uploader on exactly that value and a wrapper or a narrowing
// interface anywhere between here and there would silently drop it — the
// same failure endpointAttestedProvider's test guards on the remote side.
func TestFactory_ALocalSessionGetsAWritableProvider(t *testing.T) {
	factory := filesystemProviderFactory(nil, nil)

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
	factory := filesystemProviderFactory(nil, nil)
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

func TestFilesystemProviderFactoryRefusesUnboundRemoteSession(t *testing.T) {
	registry := &filesystemFarRegistry{}
	factories := map[string]transport.FilesystemProviderFactory{
		"nil registry": filesystemProviderFactory(nil, func(string, string) (string, bool) {
			return "original", true
		}),
		"unbound": filesystemProviderFactory(registry, func(string, string) (string, bool) {
			return "", false
		}),
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			provider, err := factory(factorySession{kind: session.KindRemote}, "")
			var unavailable *filesystem.ErrUnavailable
			if provider != nil || !errors.As(err, &unavailable) || unavailable.Reason != "connection" || unavailable.Action != "none" {
				t.Fatalf("factory = %v, %v; want ErrUnavailable(connection, none)", provider, err)
			}
		})
	}
}

type filesystemFarRegistry struct {
	status    farhelper.Status
	axisReads int
	asked     []string
}

func (r *filesystemFarRegistry) Axis(id string) (farhelper.Status, bool) {
	r.axisReads++
	return r.status, id == "original"
}

func (*filesystemFarRegistry) Watch(func(string, farhelper.Status)) func() { return func() {} }

func (r *filesystemFarRegistry) Acquire(_ context.Context, id string) (*farhelper.Lease, error) {
	r.asked = append(r.asked, id)
	return nil, &farhelper.Unavailable{Reason: r.status.Reason, Action: r.status.Action}
}

func TestFilesystemProviderFactoryBindsEveryHelperStateWithoutAcquiring(t *testing.T) {
	sess := factorySession{kind: session.KindRemote, pane: "pane", host: "example.test"}
	statuses := map[string]farhelper.Status{
		"starting":       {State: farhelper.StateStarting, Action: farhelper.ActionNone},
		"paused":         {State: farhelper.StatePaused, Reason: farhelper.ReasonConnection, Action: farhelper.ActionNone},
		"raw":            {State: farhelper.StateUnavailable, Reason: farhelper.ReasonModeRaw, Action: farhelper.ActionHostSettings},
		"consent denied": {State: farhelper.StateUnavailable, Reason: farhelper.ReasonConsentDenied, Action: farhelper.ActionNone},
		"install failed": {State: farhelper.StateUnavailable, Reason: farhelper.ReasonInstallFailed, Action: farhelper.ActionNone},
		"serving":        {State: farhelper.StateServing, Action: farhelper.ActionNone},
	}
	for name, status := range statuses {
		t.Run(name, func(t *testing.T) {
			registry := &filesystemFarRegistry{status: status}
			factory := filesystemProviderFactory(registry, func(pane, sessionID string) (string, bool) {
				return "original", pane == sess.PaneID() && sessionID == string(sess.ID())
			})
			provider, err := factory(sess, "/project")
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			if registry.axisReads != 0 || len(registry.asked) != 0 {
				t.Fatalf("factory consulted the registry: %d axis reads, acquisitions %v", registry.axisReads, registry.asked)
			}
			caps, endpoint := capability.ProviderCapabilities(provider)
			if endpoint != endpointIDFor(sess) || caps.Sink == nil || caps.Source == nil || caps.Axis == nil || caps.Versioned == nil {
				t.Fatalf("provider capabilities = %+v, endpoint = %q; want transfers, the helper axis, and versioned access for this endpoint", caps, endpoint)
			}
			want := filesystem.HelperAxis{State: string(status.State), Reason: string(status.Reason), Action: string(status.Action)}
			if got, known := caps.Axis(); !known || got != want {
				t.Fatalf("HelperAxis = %+v (known %t), want %+v", got, known, want)
			}
			if status.Reason == "" {
				// A starting decision can finish while a binding is open.
				// A serving helper can also become unavailable on that binding.
				registry.status = statuses["raw"]
			}
			_, rootErr := provider.Root(t.Context())
			_, listErr := provider.List(t.Context(), "/project", filesystem.Page{Limit: 1})
			for _, err := range []error{rootErr, listErr} {
				var unavailable *filesystem.ErrUnavailable
				if !errors.As(err, &unavailable) || unavailable.Reason != string(registry.status.Reason) || unavailable.Action != string(registry.status.Action) {
					t.Fatalf("provider call = %v; want ErrUnavailable(%s, %s)", err, registry.status.Reason, registry.status.Action)
				}
			}
			if !slices.Equal(registry.asked, []string{"original", "original"}) {
				t.Fatalf("acquisitions = %v; want [original original]", registry.asked)
			}
		})
	}
}

func TestFilesystemProviderFactoryPinsTheOriginalConnection(t *testing.T) {
	registry := &filesystemFarRegistry{status: farhelper.Status{State: farhelper.StateServing, Action: farhelper.ActionNone}}
	current := "original"
	factory := filesystemProviderFactory(registry, func(string, string) (string, bool) {
		return current, true
	})
	provider, err := factory(factorySession{kind: session.KindRemote}, "/project")
	if err != nil {
		t.Fatal(err)
	}
	current = "replacement"
	caps, _ := capability.ProviderCapabilities(provider)
	if got, known := caps.Axis(); !known || got.Reason != "connection" || got.State != "unavailable" {
		t.Fatalf("HelperAxis after the pane moved = %+v (known %t), want unavailable(connection)", got, known)
	}
	var unavailable *filesystem.ErrUnavailable
	if _, err := provider.Root(t.Context()); !errors.As(err, &unavailable) || unavailable.Reason != "connection" {
		t.Fatalf("Root after the pane moved: %v; want ErrUnavailable(connection)", err)
	}
	if len(registry.asked) != 0 {
		t.Fatalf("acquisitions after the pane moved = %v; want none", registry.asked)
	}
}
