package wave

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// defaultBound is how many non-terminal participants one wave may hold when
// the composition root names no other number. It matches the grid's own
// enrolment bound, because a participant without a grid cannot be typed into
// and one bound above the other would be decorative.
const defaultBound = 64

// defaultEnrolmentDeadline closes the interval on a launcher that never
// enrols. It is a bound on a HANDSHAKE this backend is itself waiting for, not
// a poll and not a heartbeat: nothing ticks once the enrolment arrives.
const defaultEnrolmentDeadline = 30 * time.Second

// newParticipantID mints an id before anything is forked. It is random rather
// than sequential because it names a row a coordinator will be told back, and
// a guessable participant name is a name a model can spell from memory after
// the record it belonged to is gone.
func newParticipantID() ParticipantID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform we ship; if it ever
		// does, a duplicate id would be worse than a panic here, because it
		// would silently attach one coordinator's evidence to another's
		// worker.
		panic("wave: no entropy for a participant id: " + err.Error())
	}
	return ParticipantID(hex.EncodeToString(b[:]))
}
