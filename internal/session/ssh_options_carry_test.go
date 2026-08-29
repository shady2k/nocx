package session

// The translation that decides which of the resolver's conclusions survive.
//
// `internal/connection.Resolver` resolves a profile's effective options and
// writes four of them onto the ConnectConfig — keepaliveInterval,
// keepaliveCountMax, readyTimeout and agentForward (resolver.go:175-190). The
// session→ssh seam then converts that config into ConnectOptions, and a field
// it does not translate is a field the resolver computed for nobody.
//
// This file exists because that has now happened three times in this one
// function, and its own comments say what it costs: "a field that is carried
// and discarded is worse than one that is missing, because it looks
// configured" (nocx-mlm7 for DesiredMode, nocx-pu4.1 for Shell). Keepalive is
// the third, and it is the one that costs a person their session: with no
// interval the prober is never started at all (ssh_keepalive.go:96 returns nil
// for a zero interval), so nothing in the product ever notices a transport
// that died without saying so, and the tab sits on a dead pipe in silence.
//
// The test is written against the OPTIONS, not against a live dial, because
// the defect is exactly that the options are never produced.

import (
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// applied replays the options onto a fresh ConnectConfig, which is what
// ssh.Connect does with them. Asserting on the resulting config rather than on
// the option list is the honest statement: what matters is the value the dial
// sees, not that some function was appended.
func applied(t *testing.T, cfg *ssh.ConnectConfig) *ssh.ConnectConfig {
	t.Helper()
	out := &ssh.ConnectConfig{}
	for _, opt := range sshOptionsFromConfig(cfg) {
		opt(out)
	}
	return out
}

// The resolver's keepalive decision must reach the dial. Without it there is
// no prober, and without a prober nothing in nocx can ever say that a host
// stopped answering.
func TestSSHOptions_CarryKeepalive(t *testing.T) {
	got := applied(t, &ssh.ConnectConfig{
		User:              "u",
		KeepaliveInterval: 30 * time.Second,
		KeepaliveCountMax: 3,
	})

	if got.KeepaliveInterval != 30*time.Second {
		t.Errorf("KeepaliveInterval = %v, want 30s — the prober is never started and "+
			"a silently dead transport is never noticed", got.KeepaliveInterval)
	}
	if got.KeepaliveCountMax != 3 {
		t.Errorf("KeepaliveCountMax = %d, want 3", got.KeepaliveCountMax)
	}
}

// The profile's connect deadline must reach the dial too. internal/ssh falls
// back to 30s for a zero value (ssh.go:598), so the failure here is quieter
// than keepalive's — a profile that asked for 5s silently waits 30 — but it is
// the same dropped field.
func TestSSHOptions_CarryReadyTimeout(t *testing.T) {
	got := applied(t, &ssh.ConnectConfig{User: "u", ReadyTimeout: 5 * time.Second})

	if got.ReadyTimeout != 5*time.Second {
		t.Errorf("ReadyTimeout = %v, want 5s — the profile's connect deadline never reaches the dial",
			got.ReadyTimeout)
	}
}

// Agent forwarding is a per-profile switch a person turns on and expects to
// work. Dropped here, it is a setting that governs nothing.
func TestSSHOptions_CarryAgentForward(t *testing.T) {
	got := applied(t, &ssh.ConnectConfig{User: "u", AgentForward: true})

	if !got.AgentForward {
		t.Error("AgentForward = false, want true — the profile's agent-forwarding switch is discarded")
	}
}
