package transport

// The transport suite's ONE helper route for an ssh pane (nocx-50w7p.5).
//
// # Why this exists
//
// An ssh pane is opened by a helper now, and a helper that cannot be reached is
// a NAMED REFUSAL rather than a fallback: `session_open.go` answers "SSH
// sessions are opened by this machine's helper (no helper opener is wired)" for
// a remote destination no opener claimed. That is the production behaviour and
// it is asserted by its own test.
//
// What it also means is that every test whose SUBJECT is something else — a
// resize race, a ledger query, a forward replay, a wire contract — must still
// get a session when it opens an ssh pane, or it stops measuring its subject
// and starts measuring the refusal. This file is how they get one, once, rather
// than fifteen times.
//
// # What it is, and what it deliberately is not
//
// It is a stand-in, and it says so: it adopts the session the registry already
// opens, relabelled as the helper's. That keeps every assertion those tests
// already make — which options reached the ssh factory, which size the registry
// chose, which channel the session holds — because the registry does exactly
// what it did before.
//
// What it does NOT do is assert the ROUTE. "The pane is this machine's helper's"
// is asserted where it is true: ws_helper_open_test.go drives a real
// HostedSessionOpen through the shipped handler, and
// TestAnSSHOpenWithNoHelperOpenerIsANamedRefusal asserts the other end of the
// same rule.

import (
	"context"

	"github.com/shady2k/nocx/internal/session"
)

// stubHelperOpener answers a remote destination the way this machine's helper
// does, on behalf of the transport suite's ssh-opening tests.
//
// It answers only for a REMOTE destination: a local pane is not this opener's
// and returns selected=false, so every server in this package that is wired
// with it keeps opening local panes through the registry's local seam.
type stubHelperOpener struct {
	reg *session.Reg
}

// OpenHosted is the shipped interface, with the stand-in's own answer.
func (o *stubHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (HostedSessionOpen, bool, error) {
	if o == nil || o.reg == nil || cfg.Kind != session.KindRemote {
		return HostedSessionOpen{}, false, nil
	}
	sess, err := o.reg.Open(ctx, cfg)
	if err != nil {
		return HostedSessionOpen{}, true, err
	}
	// The route back, shaped like the local opener's answer for a remote
	// destination: Host/Account name the DESTINATION, Generation names the id
	// space, and the rest is what the readopt pass reads.
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
	return WithHelperSessionOpener(&stubHelperOpener{reg: reg})
}
