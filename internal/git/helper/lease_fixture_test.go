package helper_test

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log/logtest"
)

type panelConnections struct{}

func (panelConnections) List() []farhelper.Connection {
	return []farhelper.Connection{{ID: "panel", HostKeyFingerprint: "machine", State: hostconn.Connected, Generation: 1, Shells: 1}}
}
func (panelConnections) Watch(func(farhelper.Connection)) func() { return func() {} }

type panelModes struct{}

func (panelModes) Lookup(string) (hostmode.Mode, bool) { return hostmode.Helper, true }
func (panelModes) Watch(func()) func()                 { return func() {} }

type panelConsents struct{}

func (panelConsents) Lookup(string) (consent.Answer, bool) { return consent.Granted, true }
func (panelConsents) Watch(func()) func()                  { return func() {} }

type panelRemote struct{ c *client.Client }

func (panelRemote) Probe(context.Context, farhelper.Target) (farhelper.Platform, error) {
	return farhelper.Platform{GOOS: "linux", GOARCH: "amd64"}, nil
}

func (panelRemote) Find(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, bool, error) {
	return farhelper.Installed{Dir: "/helper", Generation: "testhash"}, true, nil
}

func (panelRemote) Install(context.Context, farhelper.Target, farhelper.Platform) (farhelper.Installed, error) {
	panic("unexpected install")
}

func (p panelRemote) Dial(context.Context, farhelper.Target, farhelper.Installed) (*client.Client, error) {
	return p.c, nil
}

func acquirePanelLease(t *testing.T, c *client.Client) *farhelper.Lease {
	t.Helper()
	remote := panelRemote{c: c}
	_, logger := logtest.New(t)
	registry := farhelper.New(farhelper.Config{Connections: panelConnections{}, Modes: panelModes{}, Consents: panelConsents{}, Probe: remote, Install: remote, Dial: remote, Log: logger})
	t.Cleanup(registry.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lease, err := registry.Acquire(ctx, "panel")
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
