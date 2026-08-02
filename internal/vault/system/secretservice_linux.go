//go:build linux

// Package system provides the OS keychain provider backed by the
// freedesktop.org Secret Service (org.freedesktop.secrets) over D-Bus.
//
// Linux build only: the package is gated by a build constraint because
// go-keyring's backend is platform-specific and the D-Bus interrogation below
// is meaningless on macOS/Windows.
package system

import (
	"errors"

	dbus "github.com/godbus/dbus/v5"

	"github.com/shady2k/nocx/internal/vault"
)

// errUnexpectedPropertyType means the service answered with a type the
// Secret Service spec does not allow for that property. It is treated exactly
// like a read failure: something is there, and it cannot be trusted.
var errUnexpectedPropertyType = errors.New("secret service: unexpected property type")

const (
	secretServiceName = "org.freedesktop.secrets"
	secretServicePath = "/org/freedesktop/secrets"

	// collectionsProperty and lockedProperty are the fully-qualified property
	// names godbus expects: interface name plus member.
	collectionsProperty = "org.freedesktop.Secret.Service.Collections"
	lockedProperty      = "org.freedesktop.Secret.Collection.Locked"

	// loginCollection and defaultCollectionAlias are the two paths
	// zalando/go-keyring writes through, in its order of preference.
	loginCollection        = "/org/freedesktop/secrets/collection/login"
	defaultCollectionAlias = "/org/freedesktop/secrets/aliases/default"
)

// serviceState is what interrogating org.freedesktop.secrets found. It exists
// because "is a Secret Service available" has more than two answers, and
// collapsing them is the defect this file fixes: a daemon owning the bus name
// with a locked collection is neither absent nor usable (nocx-25k9.6).
type serviceState int

const (
	// stateNoService: nothing owns org.freedesktop.secrets, or the session bus
	// itself is unreachable. There is no keyring to unlock.
	stateNoService serviceState = iota
	// stateLocked: a service is running and the collection it writes through
	// reports Locked. The remedy is to unlock it, not to install one.
	stateLocked
	// stateUnusable: a service owns the name but will not answer for its
	// collection. Present, and not something a caller can rely on.
	stateUnusable
	// stateUsable: a service is running and its collection is unlocked.
	stateUsable
)

// secretServiceBus is the slice of the session bus this package needs. It is an
// interface so the interrogation logic can be tested without a daemon —
// arranging a real locked collection is unreliable, because a gnome-keyring
// started with --login holds the password and re-unlocks itself on next access.
type secretServiceBus interface {
	// NameHasOwner reports whether anything owns a well-known bus name.
	NameHasOwner(name string) (bool, error)
	// Collections lists the collection paths the service exposes.
	Collections() ([]string, error)
	// Locked reads a collection's Locked property.
	Locked(collectionPath string) (bool, error)
	// Close releases the connection.
	Close() error
}

// SecretServiceAvailable reports whether a Secret Service is running on the
// session bus *and* the collection it writes through is unlocked.
//
// "Available" means usable, not merely present. The weaker reading — the bus
// name has an owner — is what let a test guard on this function while asserting
// on Probe: a half-working store then produced a failing test rather than a
// skipped one, which reads like a regression and is not.
//
// A test that exercises the OS keychain should call this at the top and
// t.Skipf with a descriptive message when it returns false:
//
//	if !system.SecretServiceAvailable() {
//	    t.Skipf("skipping: no usable Secret Service on the session bus")
//	}
func SecretServiceAvailable() bool {
	return secretServiceAvailableVia(openSessionBus)
}

func secretServiceAvailableVia(open func() (secretServiceBus, error)) bool {
	bus, err := open()
	if err != nil {
		return false
	}
	defer func() { _ = bus.Close() }()
	return inspectSecretService(bus) == stateUsable
}

// platformReason observes the Secret Service and reports why a keyring call
// that named no cause failed. It is the ReasonProbe the provider defaults to on
// Linux.
func platformReason() vault.Reason {
	return platformReasonVia(openSessionBus)
}

func platformReasonVia(open func() (secretServiceBus, error)) vault.Reason {
	bus, err := open()
	if err != nil {
		// No session bus at all: there is nothing here that could be a keyring.
		return vault.ReasonNoService
	}
	defer func() { _ = bus.Close() }()
	return reasonForState(inspectSecretService(bus))
}

// inspectSecretService determines the state of the Secret Service by reading
// D-Bus properties, never by reading error text.
func inspectSecretService(bus secretServiceBus) serviceState {
	owned, err := bus.NameHasOwner(secretServiceName)
	if err != nil || !owned {
		return stateNoService
	}
	locked, err := bus.Locked(writeCollection(bus))
	if err != nil {
		return stateUnusable
	}
	if locked {
		return stateLocked
	}
	return stateUsable
}

// writeCollection mirrors go-keyring's GetLoginCollection: the login collection
// when the service lists it, the default alias otherwise. Interrogating any
// other collection would yield a confident answer about the wrong thing.
func writeCollection(bus secretServiceBus) string {
	paths, err := bus.Collections()
	if err != nil {
		return defaultCollectionAlias
	}
	for _, p := range paths {
		if p == loginCollection {
			return loginCollection
		}
	}
	return defaultCollectionAlias
}

// reasonForState translates an observation into the discriminator the UI shows
// copy for. Note that stateUsable maps to denied rather than to no opinion: the
// caller only asks after a keyring call has already failed, so a store that is
// present and unlocked and still refused the write has denied it. Reporting
// "no service" there would deny the existence of a daemon we just talked to.
func reasonForState(s serviceState) vault.Reason {
	switch s {
	case stateNoService:
		return vault.ReasonNoService
	case stateLocked:
		return vault.ReasonLocked
	default: // stateUnusable, stateUsable
		return vault.ReasonDenied
	}
}

// --- the real session bus ---

// dbusBus adapts a godbus session connection to secretServiceBus.
type dbusBus struct{ conn *dbus.Conn }

func openSessionBus() (secretServiceBus, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return &dbusBus{conn: conn}, nil
}

func (b *dbusBus) NameHasOwner(name string) (bool, error) {
	var hasOwner bool
	err := b.conn.BusObject().Call(
		"org.freedesktop.DBus.NameHasOwner", 0, name,
	).Store(&hasOwner)
	if err != nil {
		return false, err
	}
	return hasOwner, nil
}

func (b *dbusBus) Collections() ([]string, error) {
	obj := b.conn.Object(secretServiceName, dbus.ObjectPath(secretServicePath))
	v, err := obj.GetProperty(collectionsProperty)
	if err != nil {
		return nil, err
	}
	paths, ok := v.Value().([]dbus.ObjectPath)
	if !ok {
		return nil, errUnexpectedPropertyType
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, string(p))
	}
	return out, nil
}

func (b *dbusBus) Locked(collectionPath string) (bool, error) {
	obj := b.conn.Object(secretServiceName, dbus.ObjectPath(collectionPath))
	v, err := obj.GetProperty(lockedProperty)
	if err != nil {
		return false, err
	}
	locked, ok := v.Value().(bool)
	if !ok {
		return false, errUnexpectedPropertyType
	}
	return locked, nil
}

func (b *dbusBus) Close() error { return b.conn.Close() }
