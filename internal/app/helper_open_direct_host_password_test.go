//go:build nocx_local_ssh

package app

// AN UNSAVED HOST ASKS FOR ITS PASSWORD (nocx-1ebrs.44).
//
// The connect UI's direct entry — a host typed or pasted, with no saved
// connection behind it — opens through the same helper path a profile does,
// carrying one difference: no ConnectionName and no ProfileID, because there
// is nothing saved for a prompt to name or a remember to bind to. What the
// path used to do with a password-only host was end at the server's refusal:
// the keys the machine discovered were offered alone, the server answered
// "no supported methods remain", and the pane showed the raw chain — while
// typing `ssh` to the same host prompted and connected.
//
// These tests drive the shipped seam — this machine's helper over a real
// reverse channel, the real ssh service and spawner behind it, the scripted
// far side and the scripted coordinator answers, assembled as the Files
// acceptance builds them. What is asserted is the interval the bead names:
// a prompt is raised (naming no connection, because there is none), the
// answer authenticates, the pane opens — and when the answer is wrong, the
// sentence a person reads is one they can act on.

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// directHostAsker is the person at the dialog, scripted: it records what it
// was asked — so the ask's SHAPE is asserted, not only its answer — and
// replies with the value the test armed.
type directHostAsker struct {
	answer   string
	arrived  chan struct{}
	requests []ssh.PasswordRequest
}

func newDirectHostAsker(answer string) *directHostAsker {
	return &directHostAsker{answer: answer, arrived: make(chan struct{}, 32)}
}

func (a *directHostAsker) RequestConnectionPassword(_ context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	a.requests = append(a.requests, req)
	a.arrived <- struct{}{}
	return ssh.PasswordAnswer{Password: a.answer}, nil
}

func (a *directHostAsker) asked() []ssh.PasswordRequest { return a.requests }

func (a *directHostAsker) count() int { return len(a.requests) }

// awaitAsk blocks for the n-th ask, so a test reads a prompt that happened
// instead of racing one.
func (a *directHostAsker) awaitAsk(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-a.arrived:
		case <-time.After(15 * time.Second):
			t.Fatalf("the %d-th password ask never arrived", i+1)
		}
	}
}

// directHostPaneConfig is the session a direct-host open carries: the
// destination, the size, and NOTHING saved — no ProfileID, no
// ConnectionName, no stored credential. The requester is stamped here for
// the reason openPasswordConfig stamps its own: the transport stamps it in
// production (resolveRemote's direct-host branch, nocx-1ebrs.44), and this
// harness drives the seam below the transport. DesiredRaw keeps the bundle
// publish out of the open, so the pane's own dial is the only one — and the
// wrong-password case fails exactly where a person watches it fail.
func directHostPaneConfig(srv *pwSSHServer, asker *directHostAsker, paneID string) session.Config {
	return session.Config{
		Kind:   session.KindRemote,
		Host:   srv.addr,
		PaneID: paneID,
		Cols:   80, Rows: 24,
		Remote: &ssh.ConnectConfig{
			User: filesFixtureUser,
			// NO AuthMode: auto, the connect UI's shape. No secrets, no key
			// file, no agent — the machine has nothing to offer, which is
			// why the prompt rung is the ladder's whole answer.
			IntegrationMode:   string(hostmode.Raw),
			PasswordRequester: asker,
		},
	}
}

// TestOpenPath_DirectHostWithoutASecretPromptsAndOpens is the bead's first
// two tests in one: a direct-host open with no stored profile and no saved
// secret produces a password PROMPT when the server challenges, and the
// answer authenticates and opens the pane.
func TestOpenPath_DirectHostWithoutASecretPromptsAndOpens(t *testing.T) {
	srv := startPasswordSSHServer(t)
	// A direct host has no saved connection and the probe cannot name a host
	// key before the pane's own dial (it has no credential to offer), so its
	// method is auto — "not yet answered" (ADR-0033), which delivers the
	// launcher. The fixture therefore has to run the launch carrier; this test
	// is about the prompt, not about which carrier the far side accepts.
	srv.acceptLaunchExec = true
	stand := startFilesStand(t, srv, &askRecorder{value: openPasswordFixturePassword})
	seam := openSeamOver(t, stand)

	asker := newDirectHostAsker(openPasswordFixturePassword)
	stand.prompts.set(asker)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess := openPanes(t, ctx, seam, directHostPaneConfig(srv, asker, "pane-direct-prompt"), "claim-direct-prompt")

	// THE ASK'S SHAPE: one prompt, naming no connection — the dialog shows
	// no remember checkbox for an ask that has nothing to remember onto.
	asker.awaitAsk(t, 1)
	if got := asker.count(); got != 1 {
		t.Fatalf("the person was asked %d time(s), want exactly 1", got)
	}
	req := asker.asked()[0]
	if req.Connection != "" {
		t.Errorf("the ask names connection %q, want empty: a direct-host open is not a saved connection", req.Connection)
	}
	// The ask names the host WITHOUT the port: it is the resolved host a
	// person reads, not a dial address.
	hostOnly, _, splitErr := net.SplitHostPort(srv.addr)
	if splitErr != nil {
		hostOnly = srv.addr
	}
	if req.Host != hostOnly {
		t.Errorf("the ask names host %q, want the host being connected to (%s)", req.Host, hostOnly)
	}
	// THE SERVER SAW THE ANSWER: the prompt is not theatre.
	if attempts := srv.authAttempts(); len(attempts) == 0 || attempts[0] != openPasswordFixturePassword {
		t.Fatalf("the server authenticated %v, want exactly the password the person typed", attempts)
	}
	t.Cleanup(func() { _ = sess.Close() })
}

// TestOpenPath_DirectHostWrongPasswordReadsAsASentence is the third: the
// refusal path. A person who answers the prompt with the wrong password must
// read WHY the pane did not open in words they can act on — not in the
// handshake's own accounting ("attempted methods [...], no supported methods
// remain"), which names nothing a person can do.
func TestOpenPath_DirectHostWrongPasswordReadsAsASentence(t *testing.T) {
	srv := startPasswordSSHServer(t)
	stand := startFilesStand(t, srv, &askRecorder{value: openPasswordFixturePassword})
	seam := openSeamOver(t, stand)

	asker := newDirectHostAsker("a password the host will refuse")
	stand.prompts.set(asker)

	cfg := directHostPaneConfig(srv, asker, "pane-direct-wrong")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, selected, err := seam.hosted.OpenHosted(ctx, cfg, "claim-direct-wrong")
	if !selected {
		t.Fatal("the open path declined a remote destination: this machine's helper owns every ssh pane")
	}
	if err == nil {
		t.Fatal("a wrong password opened the pane")
	}

	// The person was asked, answered, and was refused: one prompt, and a
	// sentence.
	asker.awaitAsk(t, 1)
	got := err.Error()
	if strings.Contains(got, "attempted methods") || strings.Contains(got, "no supported methods remain") {
		t.Fatalf("the pane's reason is the handshake's own accounting, not a sentence: %s", got)
	}
	if !strings.Contains(got, "check the password") {
		t.Fatalf("the pane's reason names no action a person can take: %s", got)
	}
	if !strings.Contains(got, filesFixtureUser) {
		t.Fatalf("the pane's reason does not name the account the sign-in was for: %s", got)
	}
}

// TestOpenPath_DirectHostWrongPasswordAtThePublishReadsAsASentence is the
// same refusal at the open's OTHER dial: an integrating destination publishes
// the bundle before the pane's own spawn, so the wrong password is refused
// there first (nocx-1ebrs.18's one-rejection rule stops the open at that
// dial). The sentence must hold at both dials, because a person cannot tell
// which one asked them.
func TestOpenPath_DirectHostWrongPasswordAtThePublishReadsAsASentence(t *testing.T) {
	srv := startPasswordSFTPSSHServer(t, t.TempDir())
	stand := startFilesStand(t, srv, &askRecorder{value: openPasswordFixturePassword})
	seam := openSeamOver(t, stand)

	asker := newDirectHostAsker("a password the host will refuse")
	stand.prompts.set(asker)

	// Auto mode: the publish runs, and its lease dial is the first to ask.
	recordFixtureMode(t, stand, srv, hostmode.Auto)
	cfg := directHostPaneConfig(srv, asker, "pane-direct-publish")
	cfg.Remote.IntegrationMode = string(hostmode.Auto)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, selected, err := seam.hosted.OpenHosted(ctx, cfg, "claim-direct-publish")
	if !selected {
		t.Fatal("the open path declined a remote destination: this machine's helper owns every ssh pane")
	}
	if err == nil {
		t.Fatal("a wrong password opened the pane")
	}

	asker.awaitAsk(t, 1)
	got := err.Error()
	if strings.Contains(got, "attempted methods") || strings.Contains(got, "no supported methods remain") {
		t.Fatalf("the publish's rejection reaches the pane as the raw handshake: %s", got)
	}
	if !strings.Contains(got, "check the password") {
		t.Fatalf("the publish's rejection names no action a person can take: %s", got)
	}
}
