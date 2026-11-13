package transport

// The transport suite's ONE helper route for an ssh pane (nocx-50w7p.5, and
// the dial stack's removal in nocx-oalo1.3.2).
//
// # Why this exists
//
// An ssh pane is opened by a helper, and a helper that cannot be reached is a
// NAMED REFUSAL rather than a fallback: `session_open.go` answers "SSH sessions
// are opened by this machine's helper (no helper opener is wired)" for a remote
// destination no opener claimed. That is the production behaviour and it is
// asserted by its own test.
//
// What it also means is that every test whose SUBJECT is something else — a
// resize race, a ledger query, a forward replay, a wire contract — must still
// get a session when it opens an ssh pane, or it stops measuring its subject
// and starts measuring the refusal. This file is how they get one, once, rather
// than fifteen times.
//
// # What it is, and what it deliberately is not
//
// It is a stand-in, and it says so: it ADOPTS a session the way the shipped
// helper route does — the helper mints the id and the channel, and the
// registry adopts both (ADR-7). What a test can script is the three things a
// helper decides: whether it answers at all and with what error, which channel
// it hands over, and what it was asked (the resolved config, which is the ready
// half of the assertions that used to read ConnectOptions off the registry's
// deleted dial).
//
// What it does NOT do is assert the ROUTE. "The pane is this machine's helper's"
// is asserted where it is true: ws_helper_open_test.go drives a real
// HostedSessionOpen through the shipped handler, and
// TestAnSSHOpenWithNoHelperOpenerIsANamedRefusal asserts the other end of the
// same rule.

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
)

// stubHelperOpener answers a remote destination the way this machine's helper
// does, on behalf of the transport suite's ssh-opening tests.
//
// It answers only for a REMOTE destination: a local pane is not this opener's
// and returns selected=false, so every server in this package that is wired
// with it keeps opening local panes through the registry's local seam.
type stubHelperOpener struct {
	reg *session.Reg
	// open, when set, is the helper's whole answer: it may block (a dial in
	// flight) and may fail (the refusal a real helper reports). Nil adopts a
	// session over channel below.
	open func(ctx context.Context, cfg session.Config) (HostedSessionOpen, bool, error)
	// channel answers the pane's channel when open is nil; nil means a pty
	// stub, which is a session with no output.
	channel func() session.Channel
	// onOpen, when set, sees the resolved config the open handed the helper.
	onOpen func(cfg session.Config)
}

// stubHelper builds the opener so a test can script its answer.
func stubHelper(reg *session.Reg) *stubHelperOpener {
	return &stubHelperOpener{reg: reg}
}

// OpenHosted is the shipped interface, with the stand-in's own answer.
func (o *stubHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	if o == nil || o.reg == nil || cfg.Kind != session.KindRemote {
		return HostedSessionOpen{}, false, nil
	}
	if o.onOpen != nil {
		o.onOpen(cfg)
	}
	if o.open != nil {
		return o.open(ctx, cfg)
	}
	return o.adopt(ctx, cfg)
}

// adopt is the ordinary answer: the helper mints the id and the channel, and
// the registry adopts both. A test that scripts the first half of the helper's
// work calls it once its own half is done.
func (o *stubHelperOpener) adopt(ctx context.Context, cfg session.Config) (HostedSessionOpen, bool, error) {
	var ch session.Channel = pty.NewStub(log.NewSlogAdapter(nil))
	if o.channel != nil {
		ch = o.channel()
	}
	sess, err := o.reg.Adopt(ctx, cfg, session.NewID(), ch)
	if err != nil {
		return HostedSessionOpen{}, true, err
	}
	return HostedSessionOpen{
		Session:    sess,
		Host:       cfg.Host,
		Account:    remoteUser(cfg),
		Generation: "gen-test",
	}, true, nil
}

// remoteUser is the account the resolved destination names, or empty when the
// config carries no remote half. It is a function rather than a field read
// because cfg.Remote is the resolved half and may be absent in a config a test
// built by hand.
func remoteUser(cfg session.Config) string {
	if cfg.Remote == nil {
		return ""
	}
	return cfg.Remote.User
}

// sshHelperOpt wires the helper route into one server, so a call site reads as
// "this server has a helper" rather than repeating the opener's construction.
func sshHelperOpt(reg *session.Reg) WSServerOption {
	return WithHelperSessionOpener(stubHelper(reg))
}

// scriptedReason is a helper-handover channel whose ShellIntegrationReason is
// scripted — the fact the renderer reads off the open, and one only a channel
// can carry.
type scriptedReason struct {
	done   chan struct{}
	reason ssh.RefusalReason
}

func newScriptedReason(reason ssh.RefusalReason) *scriptedReason {
	return &scriptedReason{done: make(chan struct{}), reason: reason}
}

func (c *scriptedReason) Read([]byte) (int, error) { return 0, io.EOF }
func (c *scriptedReason) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *scriptedReason) Close() error {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return nil
}
func (c *scriptedReason) Done() <-chan struct{}                             { return c.done }
func (c *scriptedReason) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }
func (c *scriptedReason) ShellIntegrationReason() ssh.RefusalReason         { return c.reason }

// TestAnSSHOpenWithNoHelperOpenerIsANamedRefusal is the other end of the rule
// this file's fixture serves, and it is the assertion the epic turns on: a
// remote destination no helper claims is REFUSED BY NAME rather than dialed
// from this process (nocx-50w7p.5, ADR-0057's "no Tier A fallback", and
// nocx-oalo1.3.2, which removed the last dial that could have answered it).
func TestAnSSHOpenWithNoHelperOpenerIsANamedRefusal(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	reg := newRegWithStub(logger)
	// No WithHelperSessionOpener: this machine has no helper, and that is the
	// condition under test.
	ws := NewWSServer(logger, reg, WithProfileResolver(&fakeResolver{
		resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
			return "host.example.com", &ssh.ConnectConfig{User: "test", Port: 22}, nil
		},
	}))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	resp := jsonrpcCall(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "kind": "ssh", "profileId": "ssh:test:1",
	})
	got := string(resp)
	if !strings.Contains(got, "no helper opener is wired") {
		t.Fatalf("a remote open with no helper opener answered %s, want the refusal that names the helper — a silent dial from this process is what this asserts against", got)
	}
	if !strings.Contains(got, "-32603") {
		t.Errorf("the refusal's code is not -32603, so the renderer cannot classify it: %s", got)
	}
}
