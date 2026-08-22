package transport

import "time"

// wantWithin is how long a test in this package waits for a notification, a
// response or a condition that the code under test produces immediately.
//
// It is not a performance budget and must not be read as one. Nothing here
// measures latency; every wait is for an event the implementation hands over
// without doing work, so the only question the deadline answers is "did it
// arrive at all". The number therefore has to be large enough that a busy
// machine cannot be mistaken for a broken one.
//
// THE NUMBER WAS RAISED TWICE FOR A REASON THAT WAS NOT TRUE. This comment
// used to say the bound had been "bought twice": nocx-yht3 moved it from 2
// seconds when a container gate failed at exactly 2.00s, and nocx-2bvy moved
// it from 5 when two containerized runs out of four failed at exactly 5.00s,
// each under a different test name. Both readings were the same reading — "a
// bound that fails at exactly its own value, on a different test each time,
// is reporting the machine and not the code" — and both were wrong. So was
// the third: at 30 seconds it went on failing at exactly 30.0s, under eight
// names across eleven days (nocx-2h08, nocx-hbdw4.2).
//
// The frames were not late. They had been read off the socket and thrown
// away by whichever wait ran before the one that needed them, and gorilla
// makes the first read error permanent, so the wait that lost could never
// recover. Measured: a green package run discards zero frames; the failing
// run discarded 57, each one a notification eaten by the correlating read of
// the very call that caused it. Raising the bound could never have helped —
// there was nothing still coming. See ws_inbox_test.go, which is where that
// stopped being possible.
//
// So the number is left where it is, and this is what it now means: with no
// reader able to discard, the deadline is reached only by an event that is
// genuinely ABSENT, and thirty seconds bounds that well inside the package's
// own runtime while costing nothing on the runs where the code is correct. A
// slower machine waits longer for the same frames and still passes. If it
// ever fails at exactly 30.0s again, the notification is missing — go and
// find out why, and do not touch this line.
//
// It is deliberately NOT used for the windows that collect what arrives
// during an interval, or that assert something does not arrive: there the
// duration is the meaning of the test, and stretching it would either invert
// the assertion or make every run pay it.
const wantWithin = 30 * time.Second
