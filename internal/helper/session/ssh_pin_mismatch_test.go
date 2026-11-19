//go:build nocx_local_ssh

package session_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestWarmSpawnPinMismatchRefusesBeforeAnyIntegration(t *testing.T) {
	for _, lifecycle := range []bool{false, true} {
		name := "shell"
		if lifecycle {
			name = "listeners"
		}
		t.Run(name, func(t *testing.T) {
			f := newSSHFixture(t, "pw", "printf 'RAW-ALIVE\n'; cat")
			stand := newSSHStand(t, f, &sshCoordinator{
				password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
			})
			stand.mustSpawn(t, stand.spawnParams(t, proto.SSHModeRaw))
			f.waitStarted(t)

			params := stand.spawnParams(t, proto.SSHModeAuto)
			if lifecycle {
				params = newCoordinatorPaneLifecycle(t).spawnParamsFor(t, stand, proto.SSHModeAuto)
			}
			params.HostKeyFingerprint = "SHA256:not-this-transport"
			_, err := stand.spawn(t, params)
			if got := refusalCode(err); got != proto.ErrCodeHostKeyPinMismatch {
				t.Fatalf("warm spawn answered %v (code %q), want host-key-pin-mismatch", err, got)
			}
			if execs := f.execsSeen(); len(execs) != 0 {
				t.Fatalf("pin mismatch executed integration: %q", execs)
			}
			if forwards := f.liveForwards(); len(forwards) != 0 {
				t.Fatalf("pin mismatch opened listeners: %v", forwards)
			}
			if shells := f.shellsSeen(); shells != 1 {
				t.Fatalf("pin mismatch opened a shell: got %d, want the original one", shells)
			}

			// The coordinator can explicitly retry raw without a script pin.
			params.IntegrationMode = proto.SSHModeRaw
			params.HostKeyFingerprint = ""
			params.Lifecycle = nil
			stand.mustSpawn(t, params)
			if shells := f.shellsSeen(); shells != 2 {
				t.Fatalf("raw retry opened %d shells, want 2 including the original", shells)
			}
			if execs := f.execsSeen(); len(execs) != 0 {
				t.Fatalf("raw retry executed integration: %q", execs)
			}
			if forwards := f.liveForwards(); len(forwards) != 0 {
				t.Fatalf("raw retry opened integration listeners: %v", forwards)
			}
			if got := f.connections(); got != 1 {
				t.Fatalf("raw retry discarded the authenticated transport: %d connections", got)
			}
		})
	}
}

func TestColdRawRetryAfterPinMismatchStillRequiresHostTrust(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict proto.HostKeyVerdict
		code    proto.ProbeOutcome
	}{
		{name: "unknown", verdict: proto.HostKeyUnknown, code: proto.ProbeHostKeyUnknown},
		{name: "changed", verdict: proto.HostKeyChanged, code: proto.ProbeHostKeyChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSSHFixture(t, "pw", "printf 'UNEXPECTED-SHELL\n'; cat")
			coord := &sshCoordinator{
				password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.fingerprint(),
			}
			stand := newSSHStand(t, f, coord)
			params := stand.spawnParams(t, proto.SSHModeAuto)
			params.HostKeyFingerprint = "SHA256:not-this-host"
			_, err := stand.spawn(t, params)
			if got := refusalCode(err); got != proto.ErrCodeHostKeyPinMismatch {
				t.Fatalf("cold spawn answered %v (code %q), want host-key-pin-mismatch", err, got)
			}

			coord.mu.Lock()
			coord.verdict = tc.verdict
			coord.expected = "SHA256:recorded-key"
			coord.mu.Unlock()
			params.IntegrationMode = proto.SSHModeRaw
			params.HostKeyFingerprint = ""
			params.AcceptOnTrust = false
			_, err = stand.spawn(t, params)
			if got := refusalCode(err); got != string(tc.code) {
				t.Fatalf("raw retry answered %v (code %q), want %q", err, got, tc.code)
			}
			if shells := f.shellsSeen(); shells != 0 {
				t.Fatalf("untrusted raw retry opened %d shells", shells)
			}
			if execs := f.execsSeen(); len(execs) != 0 {
				t.Fatalf("untrusted raw retry executed integration: %q", execs)
			}
			if forwards := f.liveForwards(); len(forwards) != 0 {
				t.Fatalf("untrusted raw retry opened listeners: %v", forwards)
			}
			if asked := coord.asked(); contains(asked, proto.OpTrustHostKey) {
				t.Fatalf("raw retry trusted a key without consent: %v", asked)
			}
		})
	}
}
