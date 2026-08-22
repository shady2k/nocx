package app

// The clean start's WIRING (nocx-l21ib.4): which of the two settings states
// reaches the store, and what happens when the store refuses. The end-to-end
// behaviour is layout_clean_start_test.go, over the real socket and across
// three composition roots; this is the decision and its failure path, where
// they can be exercised one at a time.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/settings"
)

// clearWindowProbe is a ContentDB whose layout seam records whether the sweep
// was asked for. The stub underneath answers everything else, so the probe
// says one thing only — which is the question this test asks.
type clearWindowProbe struct {
	content.ContentDB
	layout *clearWindowLayout
}

func (p *clearWindowProbe) Layout() content.LayoutRepository { return p.layout }

type clearWindowLayout struct {
	content.LayoutRepository
	calls        int
	sandboxCalls int
	err          error
}

func (l *clearWindowLayout) ClearWindow(context.Context) error {
	l.calls++
	return l.err
}

func (l *clearWindowLayout) CloseSandboxPanes(context.Context) error {
	l.sandboxCalls++
	return l.err
}

func newClearWindowProbe(err error) *clearWindowProbe {
	stub := content.NewStub(log.NewSlogAdapter(nil))
	return &clearWindowProbe{
		ContentDB: stub,
		layout:    &clearWindowLayout{LayoutRepository: stub.Layout(), err: err},
	}
}

// ON means the window opens on what was left, so nothing is swept. Asserted
// because the sweep is destructive of the window (not of the rows): a helper
// that ran unconditionally would close the tabs of every user who wanted them
// back, and no test of the OFF case could see it.
func TestCleanStartSweepsNothingWhenRestoreIsOn(t *testing.T) {
	fd := &appFakeDoc{}
	reg := settings.New(fd, nil)
	if err := reg.SetBool(settings.RestoreOnStartup, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	probe := newClearWindowProbe(nil)

	clearWindowOnCleanStart(context.Background(), reg, probe, slog.Default())

	if probe.layout.calls != 0 {
		t.Fatalf("ClearWindow calls with restore ON = %d, want 0", probe.layout.calls)
	}
}

func TestCleanStartSweepsTheWindowWhenRestoreIsOff(t *testing.T) {
	fd := &appFakeDoc{}
	reg := settings.New(fd, nil)
	if err := reg.SetBool(settings.RestoreOnStartup, false); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	probe := newClearWindowProbe(nil)

	clearWindowOnCleanStart(context.Background(), reg, probe, slog.Default())

	if probe.layout.calls != 1 {
		t.Fatalf("ClearWindow calls with restore OFF = %d, want 1", probe.layout.calls)
	}
}

// The failure path, which is the one a person lives through: the store cannot
// be swept, and the application still starts. It opens on the leftovers the
// sweep did not reach — worse than a clean start, far better than a backend
// that refuses to come up over a preference — and it says so in the log
// rather than passing silently.
func TestCleanStartSurvivesAStoreThatRefusesTheSweep(t *testing.T) {
	fd := &appFakeDoc{}
	reg := settings.New(fd, nil)
	if err := reg.SetBool(settings.RestoreOnStartup, false); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	probe := newClearWindowProbe(content.ErrNotImplemented)

	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))
	clearWindowOnCleanStart(context.Background(), reg, probe, logger)

	if probe.layout.calls != 1 {
		t.Fatalf("ClearWindow calls = %d, want 1", probe.layout.calls)
	}
	if !strings.Contains(logged.String(), "clean start") {
		t.Fatalf("log after a refused sweep = %q, want the failure named", logged.String())
	}
}

func TestStartupAlwaysSweepsSandboxPanesAndSurvivesRefusal(t *testing.T) {
	probe := newClearWindowProbe(content.ErrNotImplemented)
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))

	closeSandboxPanesOnStartup(context.Background(), probe, logger)

	if probe.layout.sandboxCalls != 1 {
		t.Fatalf("CloseSandboxPanes calls = %d, want 1", probe.layout.sandboxCalls)
	}
	if !strings.Contains(logged.String(), "sandbox") {
		t.Fatalf("log after a refused sandbox sweep = %q, want the failure named", logged.String())
	}
}
