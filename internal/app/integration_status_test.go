package app

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
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
	status, reason := localIntegrationStatus("/usr/bin/fish", lifecycle.LaneID("lane-1"))
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

// The two packages spell the loss causes independently — the adapter owns the
// vocabulary, the transport matches on strings so it does not depend on the
// adapter package, and the composition root is the only thing that sees both.
// A rename on either side would otherwise silently stop mapping a handshake
// timeout to its reason, and the symptom would be the same silence the bead
// exists to end.
func TestLossCauseSpellingsAgree(t *testing.T) {
	if string(lifecyclechannel.LossHelloTimeout) != transport.LossCauseHelloTimeout {
		t.Errorf("hello-timeout spelled %q by the adapter and %q by the transport",
			lifecyclechannel.LossHelloTimeout, transport.LossCauseHelloTimeout)
	}
	if string(lifecyclechannel.LossClosed) != transport.LossCauseClosed {
		t.Errorf("closed spelled %q by the adapter and %q by the transport",
			lifecyclechannel.LossClosed, transport.LossCauseClosed)
	}
	// The two causes the transport does NOT name must not accidentally
	// collide with the two it does, or a broken descriptor would be
	// reported as a handshake that expired.
	for _, c := range []lifecyclechannel.LossCause{lifecyclechannel.LossEndOfStream, lifecyclechannel.LossReadError} {
		if string(c) == transport.LossCauseHelloTimeout || string(c) == transport.LossCauseClosed {
			t.Errorf("loss cause %q collides with a cause the transport treats specially", c)
		}
		if strings.TrimSpace(string(c)) == "" {
			t.Errorf("loss cause is empty: an unnamed path is the defect this bead removed")
		}
	}
}
