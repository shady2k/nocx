package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/lifecycleremote"
	"github.com/shady2k/nocx/internal/transport"
)

// §6.2's loss events, and the property the design asks for by name: they are
// DETECTED SEPARATELY and they stay separate all the way to the product.
//
// The two vocabularies are spelled independently — the adapter owns its set,
// the transport matches on strings so it does not depend on the adapter's
// package, and the composition root is the only thing that sees both — so a
// rename on either side silently stops mapping an event to its outcome. Same
// shape as TestLossCauseSpellingsAgree above it, for the remote adapter this
// design gave one to.

func TestRemoteLossCauseSpellingsAgree(t *testing.T) {
	pairs := map[string]struct {
		adapter lifecycleremote.LossCause
		wire    string
	}{
		"hello-timeout":      {lifecycleremote.LossHelloTimeout, transport.LossCauseHelloTimeout},
		"closed":             {lifecycleremote.LossClosed, transport.LossCauseClosed},
		"listener-gone":      {lifecycleremote.LossListenerGone, transport.LossCauseListenerGone},
		"transport-gone":     {lifecycleremote.LossTransportGone, transport.LossCauseTransportGone},
		"master-socket-gone": {lifecycleremote.LossMasterSocketGone, transport.LossCauseMasterSocketGone},
		"master-exited":      {lifecycleremote.LossMasterExited, transport.LossCauseMasterExited},
	}
	for name, p := range pairs {
		if string(p.adapter) != p.wire {
			t.Errorf("%s spelled %q by the adapter and %q by the transport", name, p.adapter, p.wire)
		}
	}
}

// The three events §6.2 names are distinguishable from one another, in the
// vocabulary and not only in prose. It is a cheap assertion and it is the one
// that fails if somebody later decides two of them "are really the same
// thing" — which is exactly the collapse the design forbids.
func TestSixTwoLossEventsAreDistinct(t *testing.T) {
	events := map[string]lifecycleremote.LossCause{
		"the socket file going":           lifecycleremote.LossMasterSocketGone,
		"the master process dying":        lifecycleremote.LossMasterExited,
		"the underlying SSH transport":    lifecycleremote.LossTransportGone,
		"nocx's own listener going":       lifecycleremote.LossListenerGone,
		"the shell never proving itself":  lifecycleremote.LossHelloTimeout,
		"the shell closing its end":       lifecycleremote.LossEndOfStream,
		"the shell's stream breaking":     lifecycleremote.LossReadError,
		"the session's own disposal path": lifecycleremote.LossClosed,
	}
	seen := map[lifecycleremote.LossCause]string{}
	for name, c := range events {
		if c == "" {
			t.Errorf("%s has no name: an unnamed path is the defect this vocabulary removes", name)
			continue
		}
		if other, dup := seen[c]; dup {
			t.Errorf("%q names both %q and %q; §6.2 detects them separately", c, name, other)
		}
		seen[c] = name
	}
}
