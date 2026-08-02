//go:build linux

package system

import (
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/vault"
)

// fakeBus is a scriptable secretServiceBus. Every method can be made to fail,
// because a Secret Service that answers some questions and not others is the
// state this whole file exists to describe.
type fakeBus struct {
	owned       bool
	ownerErr    error
	collections []string
	collErr     error
	locked      map[string]bool
	lockedErr   error
	closed      bool

	// askedFor records the collection path Locked was called with, so a test
	// can assert we interrogated the collection the writes actually go to.
	askedFor string
}

func (b *fakeBus) NameHasOwner(string) (bool, error) {
	if b.ownerErr != nil {
		return false, b.ownerErr
	}
	return b.owned, nil
}

func (b *fakeBus) Collections() ([]string, error) {
	if b.collErr != nil {
		return nil, b.collErr
	}
	return b.collections, nil
}

func (b *fakeBus) Locked(path string) (bool, error) {
	b.askedFor = path
	if b.lockedErr != nil {
		return false, b.lockedErr
	}
	return b.locked[path], nil
}

func (b *fakeBus) Close() error {
	b.closed = true
	return nil
}

func TestInspectSecretService(t *testing.T) {
	tests := []struct {
		name string
		bus  *fakeBus
		want serviceState
	}{
		{
			name: "nothing owns the bus name",
			bus:  &fakeBus{owned: false},
			want: stateNoService,
		},
		{
			// Asking the bus is itself an external call, and it can fail.
			name: "the ownership query fails",
			bus:  &fakeBus{ownerErr: errors.New("bus went away")},
			want: stateNoService,
		},
		{
			// The defect from nocx-25k9.6, stated as an observation: the name
			// is owned and the collection is locked.
			name: "a running service with a locked login collection",
			bus: &fakeBus{
				owned:       true,
				collections: []string{loginCollection},
				locked:      map[string]bool{loginCollection: true},
			},
			want: stateLocked,
		},
		{
			name: "a running service with an unlocked login collection",
			bus: &fakeBus{
				owned:       true,
				collections: []string{loginCollection},
				locked:      map[string]bool{loginCollection: false},
			},
			want: stateUsable,
		},
		{
			// No login collection listed: go-keyring falls back to the default
			// alias, so the interrogation must follow it there.
			name: "no login collection falls back to the default alias",
			bus: &fakeBus{
				owned:       true,
				collections: []string{"/org/freedesktop/secrets/collection/other"},
				locked:      map[string]bool{defaultCollectionAlias: true},
			},
			want: stateLocked,
		},
		{
			name: "listing collections fails, so the default alias is used",
			bus: &fakeBus{
				owned:   true,
				collErr: errors.New("no such property"),
				locked:  map[string]bool{defaultCollectionAlias: false},
			},
			want: stateUsable,
		},
		{
			// Present, and refuses to say anything useful about itself. Not
			// absent — reporting "no service" here is the misdiagnosis.
			name: "the Locked property cannot be read",
			bus: &fakeBus{
				owned:       true,
				collections: []string{loginCollection},
				lockedErr:   errors.New("access denied"),
			},
			want: stateUnusable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inspectSecretService(tt.bus); got != tt.want {
				t.Fatalf("inspectSecretService = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWriteCollectionMatchesGoKeyring pins the collection we interrogate to the
// one go-keyring writes through. A confident answer about a collection nobody
// writes to is worse than no answer.
func TestWriteCollectionMatchesGoKeyring(t *testing.T) {
	bus := &fakeBus{
		owned:       true,
		collections: []string{loginCollection},
		locked:      map[string]bool{loginCollection: true},
	}
	inspectSecretService(bus)
	if bus.askedFor != loginCollection {
		t.Fatalf("interrogated %q, want %q", bus.askedFor, loginCollection)
	}

	alias := &fakeBus{
		owned:       true,
		collections: []string{},
		locked:      map[string]bool{defaultCollectionAlias: true},
	}
	inspectSecretService(alias)
	if alias.askedFor != defaultCollectionAlias {
		t.Fatalf("interrogated %q, want %q", alias.askedFor, defaultCollectionAlias)
	}
}

func TestReasonForState(t *testing.T) {
	tests := []struct {
		state serviceState
		want  vault.Reason
	}{
		{stateNoService, vault.ReasonNoService},
		{stateLocked, vault.ReasonLocked},
		{stateUnusable, vault.ReasonDenied},
		// Present, unlocked, and it still refused the write that brought us
		// here. That is a denial, not an absence.
		{stateUsable, vault.ReasonDenied},
	}
	for _, tt := range tests {
		if got := reasonForState(tt.state); got != tt.want {
			t.Fatalf("reasonForState(%v) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// TestPlatformReasonVia covers the whole path a provider takes, including the
// case where the session bus cannot be opened at all.
func TestPlatformReasonVia(t *testing.T) {
	t.Run("no session bus is no service", func(t *testing.T) {
		got := platformReasonVia(func() (secretServiceBus, error) {
			return nil, errors.New("no DBUS_SESSION_BUS_ADDRESS")
		})
		if got != vault.ReasonNoService {
			t.Fatalf("platformReasonVia = %q, want %q", got, vault.ReasonNoService)
		}
	})

	t.Run("a locked collection is locked", func(t *testing.T) {
		bus := &fakeBus{
			owned:       true,
			collections: []string{loginCollection},
			locked:      map[string]bool{loginCollection: true},
		}
		got := platformReasonVia(func() (secretServiceBus, error) { return bus, nil })
		if got != vault.ReasonLocked {
			t.Fatalf("platformReasonVia = %q, want %q", got, vault.ReasonLocked)
		}
		if !bus.closed {
			t.Fatal("the bus connection was not closed")
		}
	})
}

// TestSecretServiceAvailableVia is the guard/probe agreement the acceptance
// criterion asks for: available means usable, so a running-but-locked service
// makes the integration test skip rather than fail.
func TestSecretServiceAvailableVia(t *testing.T) {
	tests := []struct {
		name string
		open func() (secretServiceBus, error)
		want bool
	}{
		{
			name: "no session bus",
			open: func() (secretServiceBus, error) { return nil, errors.New("no bus") },
			want: false,
		},
		{
			name: "nothing owns the name",
			open: func() (secretServiceBus, error) { return &fakeBus{owned: false}, nil },
			want: false,
		},
		{
			name: "running but locked is not available",
			open: func() (secretServiceBus, error) {
				return &fakeBus{
					owned:       true,
					collections: []string{loginCollection},
					locked:      map[string]bool{loginCollection: true},
				}, nil
			},
			want: false,
		},
		{
			name: "running and unlocked is available",
			open: func() (secretServiceBus, error) {
				return &fakeBus{
					owned:       true,
					collections: []string{loginCollection},
					locked:      map[string]bool{loginCollection: false},
				}, nil
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := secretServiceAvailableVia(tt.open); got != tt.want {
				t.Fatalf("secretServiceAvailableVia = %v, want %v", got, tt.want)
			}
		})
	}
}
