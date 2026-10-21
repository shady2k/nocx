package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/transport"
)

// The local half of nocx-dvql. session.go said in as many words that local
// sessions always return ReasonNone, so the wire the product renders was laid
// and never connected on this side: a degraded local session was
// indistinguishable from an integrated one, in the UI and in the log.
//
// These tests are about the OPEN, because it is the only thing that knows what
// the daemon actually started. Until nocx-ie23r.3 that was the pty factory,
// which knew because it had exec'd the binary itself; it is now the local
// hosted open, which knows because the helper's launch record says so and
// because it is the thing that established (or did not establish) the
// lifecycle lane. The transport's half — turning this into the notification —
// is proven over the real socket in internal/transport, and that the answer
// travels from here to there at all is proven by
// TestALocalPaneEntersTheIntegrationAxis in local_pane_test.go.

// An enhanced local session enters the axis as `starting`, naming the binary
// the helper actually started. `starting` and not `integrated`: the shell has
// proved nothing yet, and a product that claimed either outcome here would be
// guessing for the ten seconds that matter most.
func TestLocalEnhancedSessionReportsWhatItStarted(t *testing.T) {
	status, reason := localIntegrationStatus("/bin/bash", lifecycle.LaneID("lane-1"))
	if status != transport.IntegrationStarting || reason != ssh.ReasonNone {
		t.Fatalf("status/reason = %q/%q, want starting with no refusal", status, reason)
	}
}

// A login shell nocx has no local tier for is reported CONVENTIONAL with the
// reason, rather than quietly started as something else. Substituting bash was
// the defect nocx-wwz0 removed; degrading silently is the one AGENTS.md names.
// The helper starts the user's own shell either way — the difference this
// makes is that the product says so.
func TestLocalUnsupportedShellSaysSo(t *testing.T) {
	status, reason := localIntegrationStatus("/usr/bin/csh", lifecycle.LaneID("lane-1"))
	if status != transport.IntegrationConventional || reason != ssh.ReasonUnsupportedShell {
		t.Fatalf("status/reason = %q/%q, want conventional/unsupported-shell", status, reason)
	}
}

// A session that never asked for integration says nothing at all. Absence is
// how "conventional by design" is expressed — the surface has nothing to nag
// about, and a badge on a tab the user deliberately opened raw would be noise
// that teaches them to ignore the badge.
//
// Two ways to be that session, and both must be silent: no lifecycle lane was
// established (the kernel is not wired, or the coordinator asked for none),
// and no shell was reported at all.
func TestLocalConventionalSessionReportsNothing(t *testing.T) {
	if status, _ := localIntegrationStatus("/bin/bash", ""); status != "" {
		t.Errorf("status = %q, want silence for a session with no lifecycle lane", status)
	}
	if status, _ := localIntegrationStatus("", lifecycle.LaneID("lane-1")); status != "" {
		t.Errorf("status = %q, want silence when the helper named no shell", status)
	}
}

func TestLocalFishRequiresTheNativeStartupLane(t *testing.T) {
	status, reason := localIntegrationStatus("/usr/bin/fish", lifecycle.LaneID("fish-lane"))
	if status != transport.IntegrationStarting || reason != ssh.ReasonNone {
		t.Fatalf("native Fish launch = %q/%q, want starting without refusal", status, reason)
	}
	status, reason = localIntegrationStatus("/usr/bin/fish", "")
	if status != transport.IntegrationConventional || reason != ssh.ReasonUnsupportedShell {
		t.Fatalf("Fish without a verified native launch = %q/%q, want an explicit conventional fallback", status, reason)
	}
}
