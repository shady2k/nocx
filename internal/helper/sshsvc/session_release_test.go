//go:build nocx_local_ssh

package sshsvc_test

// A SESSION THE FAR SIDE REFUSED, AND THE ONE RE-ASK (nocx-xn63t.4.10).
//
// The far side bounds the sessions it grants one connection at a time — a
// subsystem counts, the same as an exec — and it releases a session the client
// has just CLOSED only on a later pass of its own loop. Measured against the
// live-sshd fixture at MaxSessions 1 (nocx-xn63t.4.10's probe, quoted in
// internal/ssh/pool.go's newSession): closing a session and asking for the next
// one immediately was refused 59 times in 60 rounds, while the same step after
// 1 ms was granted 10 of 10 — and a refused attempt's own reply ordered the
// re-ask well enough that a bare retry was granted 59 of 59. The
// shell-integration bundle publish asks ONE pooled connection for two sessions
// by design (a probe for the account's home, then an sftp channel — AD-4, one
// authentication for one machine), so at MaxSessions 1 it was the sftp open
// that was refused and the session never integrated.
//
// internal/ssh's pool answers that with ONE re-ask, gated on the refusal and
// separated by an answer from the far side; nothing is paid when nothing is
// refused, which is what a host at OpenSSH's default `maxsessions 10` never
// makes it pay. These cases pin that contract from the far side's own ordered
// log of what it observed, so removing the re-ask turns them red without any
// timing in them.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// TestASessionTheFarSideRefusedIsReaskedAfterItAnswers is the release race on
// demand: the far side turns the next session away exactly once, with the
// spelling a real sshd uses when it is still holding the session the client
// just closed, and the caller must still get its channel.
func TestASessionTheFarSideRefusedIsReaskedAfterItAnswers(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) {
		return "/home/deploy\n", "", 0, false
	})
	lease := acquireLease(t, stand, f)

	// Session one: the probe that asks the account's home.
	if _, err := lease.Home(context.Background()); err != nil {
		t.Fatalf("home: %v", err)
	}

	// Session two, on the same connection: the first attempt is refused the way
	// the release race refuses one, and the helper must ask again rather than
	// report it.
	f.refuseNextSessionOpens(1)
	stream, err := stand.openChannel(t, sftpParams(t, f))
	if err != nil {
		t.Fatalf("open an sftp channel whose first attempt the far side refused: %v", err)
	}
	defer func() { _ = stream.Close() }()

	want := []string{"exec", "session-refused", "global:keepalive@openssh.com", "subsystem:sftp"}
	if got := f.observed(); !equal(got, want) {
		t.Fatalf("the far side observed %v, want %v — the refused session must be re-asked only after the far side has ANSWERED, and the channel must then be granted", got, want)
	}
	if seen := f.subsystemsSeen(); len(seen) != 1 || seen[0] != "sftp" {
		t.Errorf("the far side was asked for subsystems %v, want exactly [sftp]", seen)
	}
}

// TestASessionTheFarSideKeepsRefusingIsReportedAfterExactlyOneReask is the
// other half: a host that will never grant the session — policy, or a session
// somebody else is holding — is reported as the refusal it is, after ONE
// re-ask and no more. A re-ask that kept trying would be a retry loop against
// somebody else's machine, which is the thing this shape must not become.
func TestASessionTheFarSideKeepsRefusingIsReportedAfterExactlyOneReask(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) {
		return "/home/deploy\n", "", 0, false
	})
	lease := acquireLease(t, stand, f)
	if _, err := lease.Home(context.Background()); err != nil {
		t.Fatalf("home: %v", err)
	}

	// A session this host will not grant at all, in the OTHER spelling a real
	// sshd uses for the same decision (resource shortage) — one class, two
	// spellings, and the re-ask answers both.
	f.setSessionRefusal(true)
	stream, err := stand.openChannel(t, sftpParams(t, f))
	if err == nil {
		_ = stream.Close()
		t.Fatal("a session the far side refuses every time was granted")
	}
	if code := refusalCode(err); code != proto.ErrCodeChannelRefused {
		t.Fatalf("refusal = %q (%v), want %q — the caller must see the same refusal class a single attempt would have given it",
			code, err, proto.ErrCodeChannelRefused)
	}

	attempts := 0
	for _, event := range f.observed() {
		if event == "session-refused" {
			attempts++
		}
	}
	if attempts != 2 {
		t.Fatalf("the far side refused %d session opens, want exactly 2 — one attempt and one re-ask, never a loop", attempts)
	}
}

// TestAGrantedSessionCostsNoRoundTrip is the cost half of the contract, and the
// reason this shape replaced a wait before every session: a connection that has
// already carried a session pays nothing when the far side grants the next one.
// A far side at OpenSSH's own default (maxsessions 10) grants it, so the extra
// exchange must not exist.
func TestAGrantedSessionCostsNoRoundTrip(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	stand := probeStand(t, f, func(string) (string, string, int, bool) {
		return "/home/deploy\n", "", 0, false
	})
	lease := acquireLease(t, stand, f)

	if _, err := lease.Home(context.Background()); err != nil {
		t.Fatalf("home: %v", err)
	}
	stream, err := stand.openChannel(t, sftpParams(t, f))
	if err != nil {
		t.Fatalf("open an sftp channel after a probe session: %v", err)
	}
	defer func() { _ = stream.Close() }()

	want := []string{"exec", "subsystem:sftp"}
	if got := f.observed(); !equal(got, want) {
		t.Fatalf("the far side observed %v, want %v — a granted session must cost no round trip, which is what makes this shape free on a host at the default maxsessions", got, want)
	}
}

// equal compares two event logs element by element.
func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
