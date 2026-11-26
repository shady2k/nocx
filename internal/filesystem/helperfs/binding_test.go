package helperfs_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/sessionid"
)

// axisOnlyRegistry is the connection's axis and nothing else. Its Acquire
// REFUSES and counts, which is the assertion C28 needs: creating the provider,
// registering its binding and reading the axis must not attempt a single call
// on the connection — a panel has to draw the reason on a host where nothing is
// serving, which is exactly when a dial would fail or, worse, succeed slowly.
type axisOnlyRegistry struct {
	status farhelper.Status
	known  bool

	mu       sync.Mutex
	acquires int
}

func (r *axisOnlyRegistry) Acquire(context.Context, string) (*farhelper.Lease, error) {
	r.mu.Lock()
	r.acquires++
	r.mu.Unlock()
	return nil, &farhelper.Unavailable{Reason: r.status.Reason, Action: r.status.Action}
}

func (r *axisOnlyRegistry) Axis(string) (farhelper.Status, bool) { return r.status, r.known }

func (*axisOnlyRegistry) Watch(func(string, farhelper.Status)) func() { return func() {} }

func (r *axisOnlyRegistry) acquireCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acquires
}

// owningEverything stands in for the transport's Caller seam: D15 is
// binding_test.go's subject and must not get in the way here.
type owningEverything struct{}

func (owningEverything) Owns(sessionid.ID) bool { return true }

// TestABindingOnAnUnavailableConnectionNamesTheAxisWithoutDialling is C28 from
// the panel's end: the binding exists while the axis is unavailable, answers
// the connection's own reason, and fails a call with the same reason it
// reports. Both ends are asserted because they are what the panel draws
// together — a reason in the header and a different one in the error is the
// defect this criterion exists to prevent.
//
// The acquire count is the other half. The axis is a fact the registry holds,
// so nothing about creating, registering or reading it may reach the
// connection; only the call that cannot be served may try.
func TestABindingOnAnUnavailableConnectionNamesTheAxisWithoutDialling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status farhelper.Status
		known  bool
		want   filesystem.HelperAxis
	}{
		{
			name:   "a host whose helper was not consented to",
			status: farhelper.Status{State: farhelper.StateUnavailable, Reason: farhelper.ReasonConsentDenied, Action: farhelper.ActionHostSettings},
			known:  true,
			want: filesystem.HelperAxis{
				State:  string(farhelper.StateUnavailable),
				Reason: string(farhelper.ReasonConsentDenied),
				Action: string(farhelper.ActionHostSettings),
			},
		},
		{
			name:   "a connection that is no longer bound",
			status: farhelper.Status{},
			known:  false,
			want: filesystem.HelperAxis{
				State:  string(farhelper.StateUnavailable),
				Reason: string(farhelper.ReasonConnection),
				Action: string(farhelper.ActionNone),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &axisOnlyRegistry{status: tc.status, known: tc.known}
			ctx := testContext(t)
			bindings := filesystem.New()
			p := helperfs.New(reg, panelID, "/home/u")
			id, err := bindings.Register(p, "s1", "v1:abc", filesystem.Capabilities{
				Axis:      p.HelperAxis,
				Versioned: p.Versioned(),
			})
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			h, release, err := bindings.Acquire(id, owningEverything{})
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer release()

			got, ok := h.HelperAxis()
			if !ok {
				t.Fatal("a helper-backed binding answered no axis; the panel would have nothing to draw")
			}
			if got != tc.want {
				t.Fatalf("HelperAxis = %+v, want %+v", got, tc.want)
			}
			if n := reg.acquireCount(); n != 0 {
				t.Fatalf("creating, registering and reading the axis made %d call(s) on the connection, want 0 — the binding must exist before any I/O", n)
			}

			_, err = h.List(ctx, "/home/u", filesystem.Page{Limit: 10})
			var unavailable *filesystem.ErrUnavailable
			if !errors.As(err, &unavailable) {
				t.Fatalf("List = %v, want *filesystem.ErrUnavailable", err)
			}
			if unavailable.Reason != tc.want.Reason || unavailable.Action != tc.want.Action {
				t.Fatalf("List reason = %q/%q, want the axis's %q/%q — the panel and its error must name one reason",
					unavailable.Reason, unavailable.Action, tc.want.Reason, tc.want.Action)
			}
			if n := reg.acquireCount(); n != 1 {
				t.Fatalf("List attempted %d acquires, want exactly 1: the call is the one thing that may reach the connection", n)
			}
		})
	}
}

// TestALocalBindingNamesNoAxis is C28's other end: a local tab has no helper
// axis at all, and saying so is the same false the binding answers — not an
// axis whose reason happens to be empty. The conditional write is refused as
// well, because a local provider checks no version at a rename either.
func TestALocalBindingNamesNoAxis(t *testing.T) {
	bindings := filesystem.New()
	p := local.New()
	id, err := bindings.Register(p, "s1", "", filesystem.Capabilities{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	h, release, err := bindings.Acquire(id, owningEverything{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	if got, ok := h.HelperAxis(); ok {
		t.Fatalf("HelperAxis on a local binding = %+v, true; want false — a local tab is not behind a helper", got)
	}
	_, err = h.Versioned()
	var unsupported *filesystem.ErrVersionedUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("Versioned on a local binding = %v, want ErrVersionedUnsupported", err)
	}
}

// TestTheConditionalWriteRefusesWithoutAClientButIsStillThere pins the shape
// the unavailable case must have on the write side: the seam is present (a
// helper-backed provider always has one), and reaching the host through it
// refuses with the connection's own reason rather than with a nil.
func TestTheConditionalWriteRefusesWithoutAClientButIsStillThere(t *testing.T) {
	reg := &axisOnlyRegistry{
		status: farhelper.Status{State: farhelper.StateUnavailable, Reason: farhelper.ReasonInstallFailed, Action: farhelper.ActionRefresh},
		known:  true,
	}
	bindings := filesystem.New()
	p := helperfs.New(reg, panelID, "/home/u")
	id, err := bindings.Register(p, "s1", "", filesystem.Capabilities{Versioned: p.Versioned()})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	h, release, err := bindings.Acquire(id, owningEverything{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	write, err := h.Versioned()
	if err != nil {
		t.Fatalf("Versioned: %v — an unavailable connection has a seam, it just cannot reach the host", err)
	}

	_, _, err = write.ReadVersion(testContext(t), "/home/u/note", 1<<20)
	var unavailable *filesystem.ErrUnavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != string(farhelper.ReasonInstallFailed) {
		t.Fatalf("ReadVersion = %v, want ErrUnavailable naming install-failed", err)
	}
}
