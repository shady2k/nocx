package ghostty

import "github.com/shady2k/nocx/internal/emulator"

// The clear boundary (nocx-2v80t.3.17): ED3, `CSI 3 J` — ESC [ 3 J — the
// sequence `clear(1)` emits after homing the cursor and erasing the display
// (nocx-zg3k3.10.3's owner decision, verified: `clear` on an ordinary
// machine emits ESC[H ESC[2J ESC[3J).
//
// # Why this is scanned, not read off the library's own state
//
// A first version of this port read the effect off the scrollback depth
// measurement noteDepartedLocked already takes every feed (a decrease to
// zero), reasoning that the library already parses and acts on ED2/ED3 and a
// byte scanner here would be a second, possibly-disagreeing decoder of a
// fact the library already owns. That measured WRONG on the e2e
// (nocx-2v80t.3.17): a short session whose live rows never scrolled into
// history has a depth of ZERO before `clear` runs, and ED3 leaves it at
// zero — not a transition the delta can see at all — so the signal never
// fired for the case the feature exists for: a person runs one command, the
// screen is nowhere near full, and `clear` tidies up the still-VISIBLE
// blocks, none of which ever departed into scrollback.
//
// So this is scanned exactly the way the fence is (fence.go): the SEQUENCE
// is the fact, not what it happened to do to a counter. The library still
// parses and executes it — ED3 reaches vt_write unchanged, the same as
// every other byte scanMarkers lets through — this scanner only locates
// WHERE in the feed it sits, the way the fence's and the output mark's
// scanners do.
//
// # Not a rendezvous with an authenticated event
//
// Unlike the fence and the output mark, sighting this needs no
// authentication and locates nothing else: ED3 is a real, standard VT
// operation the library executes as part of the very vt_write that carries
// it (ADR-0066 — the backend owns the whole VT grammar, not a private
// second reading), so the sighting IS the fact rather than a location for
// one authenticated elsewhere.
const eraseSavedLinesFixed = "\x1b[3J"

// eraseSavedLinesMatches reports whether b is the idx'th byte of ED3.
func eraseSavedLinesMatches(idx int, b byte) bool {
	return idx < len(eraseSavedLinesFixed) && b == eraseSavedLinesFixed[idx]
}

// sightEraseSavedLines appends the clear-boundary effect. Like sightFence
// and sightOutputMark this carries no authority of its own — the runtime is
// what decides what a sighting means (ADR-0024 decision 1's shape; here
// there is nothing to authenticate in the first place, see the package
// doc above).
func (t *terminal) sightEraseSavedLines() {
	t.effects = append(t.effects, emulator.Effect{Kind: emulator.EffectClearBoundary})
}
