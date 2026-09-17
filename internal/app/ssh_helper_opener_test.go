//go:build nocx_local_ssh

package app

// The app suite's stand-in helper route for an ssh pane (nocx-50w7p.5).
//
// An ssh pane is opened by a helper now, and a remote destination no opener
// claimed is a NAMED REFUSAL ("no helper opener is wired") rather than a
// fallback dial. That is the production behaviour; the tests in this package
// whose SUBJECT is something else — a launcher's integration reason on the
// wire, a resize race, a ledger row — must still get a session when they open
// an ssh pane, or they stop measuring their subject and start measuring the
// refusal.
//
// # What it is, and what it deliberately is not
//
// It is a stand-in, and it says so: it adopts what the registry already opens
// and relabels it as the helper's, so every assertion those tests already make
// (which options reached the ssh factory, which size the registry chose, which
// channel the session holds) keeps holding.
//
// What it does NOT do is assert the route. "The pane is this machine's helper's
// on the real wire" is asserted where it is true — helper_open_password_test.go
// and the helper acceptance tests — and the other end of the same rule (a
// remote open with no opener is refused by name) has its own test.

import (
	"context"

	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport"
)

// standHelperOpener answers a remote destination the way this machine's helper
// does, for the app suite's ssh-opening tests.
//
// It answers only for a REMOTE destination: a local pane is not this opener's
// and returns selected=false, so a server wired with it keeps opening local
// panes through the registry's local seam.
type standHelperOpener struct {
	reg *session.Reg
}

func (o *standHelperOpener) OpenHosted(ctx context.Context, cfg session.Config, _ string) (transport.HostedSessionOpen, bool, error) {
	if o == nil || o.reg == nil || cfg.Kind != session.KindRemote {
		return transport.HostedSessionOpen{}, false, nil
	}
	sess, err := o.reg.Open(ctx, cfg)
	if err != nil {
		return transport.HostedSessionOpen{}, true, err
	}
	return transport.HostedSessionOpen{
		Session:    sess,
		Host:       cfg.Host,
		Account:    standHelperAccount(cfg),
		Generation: "gen-test",
	}, true, nil
}

// standHelperAccount is the account the resolved destination names, or empty
// when the config carries no remote half.
func standHelperAccount(cfg session.Config) string {
	if cfg.Remote == nil {
		return ""
	}
	return cfg.Remote.User
}

// standHelperOpt wires the helper route into one server, so a call site reads as
// "this server has a helper" rather than repeating the opener's construction.
func standHelperOpt(reg *session.Reg) transport.WSServerOption {
	return transport.WithHelperSessionOpener(&standHelperOpener{reg: reg})
}
