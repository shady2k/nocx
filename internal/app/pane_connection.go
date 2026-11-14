package app

// The composition root's half of pane.connection (design §5.6, nocx-oalo1.5.3).
//
// internal/panedriver decides WHICH pane is told WHAT; internal/transport owns
// the wire shape and the per-pane ordering. This file is the one adapter
// between them, and it holds no decision of its own: a field that is present in
// one shape and absent in the other is a pointer here, and a moment is
// formatted once.

import (
	"time"

	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/transport"
)

// paneConnectionWire renders one snapshot as the notification's payload.
func paneConnectionWire(s panedriver.Snapshot) transport.PaneConnection {
	p := transport.PaneConnection{
		Generation:         s.Generation,
		PaneID:             s.PaneID,
		ConnectionID:       s.ConnectionID,
		ConnectionEpoch:    s.ConnectionEpoch,
		ConnectionSequence: s.ConnectionSequence,
		InstanceID:         s.InstanceID,
		SessionID:          s.SessionID,
		Transport: transport.PaneConnectionTransport{
			State:  s.Transport.State,
			Stage:  s.Transport.Stage,
			Person: s.Transport.Person,
			// CAUSE IS A FACT ABOUT THE END, NOT ABOUT AN ATTEMPT (design §5.6).
			// The coordinator keeps the retry cause internally (it decides
			// auto-retry on it), but the renderer's exact branch admits a cause
			// only on failed and ended — where a person is being told why the
			// connection stopped.
			Cause:   causeOnTheWire(s),
			Attempt: s.Transport.Attempt,
			StateMs: s.Transport.StateMs,
			// THE POINTER IS THE WHOLE POINT: connected with no plain shell
			// says 0, opening must not say it at all.
			PlainShells: plainShellsOf(s),
		},
		Attempt: transport.PaneConnectionAttempt{
			Shown:     s.Attempt.Shown,
			Candidate: s.Attempt.Candidate,
			Reason:    s.Attempt.Reason,
		},
		ObservedAt: s.ObservedAt.UTC().Format(time.RFC3339),
	}
	if s.ShellFailure != nil {
		p.ShellFailure = &transport.PaneShellFailure{Code: s.ShellFailure.Code, Message: s.ShellFailure.Message, Data: s.ShellFailure.Data}
	}
	if s.Transport.HasLook {
		p.Transport.LookAnsweredAt = s.Transport.LookAnsweredAt.UTC().Format(time.RFC3339)
	}
	if s.Quality != nil {
		p.Quality = &transport.PaneConnectionQuality{
			Slow:        s.Quality.Slow,
			RoundTripMS: s.Quality.RoundTripMs,
		}
	}
	// THE OFFER'S PRESENCE IS THE BUTTON (design §5.8): a nil object means no
	// action is shown, so an empty one could never stand for "offered, ends
	// nothing".
	if s.Reconnect != nil {
		p.Reconnect = &transport.PaneConnectionReconnect{EndsShells: s.Reconnect.EndsShells}
	}
	return p
}

// causeOnTheWire answers the cause for the states the renderer's contract gives
// one to, and empty otherwise.
func causeOnTheWire(s panedriver.Snapshot) string {
	switch s.Transport.State {
	case "failed", "ended":
		return s.Transport.Cause
	}
	return ""
}

// plainShellsOf answers the count for the states that carry one, and nil for
// every other state: the wire schema admits the field exactly there.
func plainShellsOf(s panedriver.Snapshot) *int {
	if !s.Transport.HasPlainShells {
		return nil
	}
	n := s.Transport.PlainShells
	return &n
}
