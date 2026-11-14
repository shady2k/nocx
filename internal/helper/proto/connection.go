package proto

// The connection vocabulary between a helper and the coordinator that opened a
// pane (design §5.3, §5.6; stage 3).
//
// # Why a pane needs this to be a wire fact
//
// The connection supervisor lives in the HELPER, because that is the process
// that dials (ADR-0057). The pane lives in the COORDINATOR, because that is the
// process with a person looking at it. So "what is this connection doing" has
// to cross, and it crosses as two things this file declares: an op that answers
// which connection a destination's shell will ride, and an event that says what
// that connection is doing.
//
// # The op, and the early open
//
// `connection` takes the same resolved destination every other op takes and
// answers the identity of the connection the dial will use. It is deliberately
// a question with NO side effect: it derives the same key `spawn-ssh`'s own
// acquire will derive, and answers it before a socket exists. That is what lets
// the coordinator bind its pane to a connection BEFORE the shell does — the
// first pane.connection then arrives while the dial is still opening, which is
// the whole of design §5.5's "answers when the connection is registered".
//
// # The event
//
// `connection-state` is a notification, because a state is not an answer to a
// request: it is revised for as long as the connection exists, in both
// directions, and carries a sequence number so a receiver can refuse one that
// arrives after a newer one. It is sent on the connection the request arrived
// on — the coordinator that opened the pane — and never broadcast, because a
// helper serves several coordinators (D12) and another one's pane is not this
// one's to draw.
//
// The state names are hostconn's own, character for character. A second
// vocabulary for one machine would be the defect AD-8 names, and the two would
// first disagree about the value a person reads.

// OpConnection is the forward op that answers which connection a destination's
// shell will ride. See this file's header: it is a derivation and dials nothing.
const OpConnection = "connection"

// EventConnectionState is the notification that a connection's state (or its
// measurement) changed. It is the only per-connection event the ssh service
// sends to a coordinator that is not already holding a stream.
const EventConnectionState = "connection-state"

// The state names a connection event carries. They are hostconn.Kind's
// spellings, restated here because this package is the wire's leaf and must not
// import the state machine.
const (
	ConnectionOpening       = "opening"
	ConnectionWaitingPerson = "waiting_person"
	ConnectionConnected     = "connected"
	ConnectionStalled       = "stalled"
	ConnectionReconnecting  = "reconnecting"
	ConnectionFailed        = "failed"
	ConnectionEnded         = "ended"
)

// ConnectionParams is the `connection` op's request: one RESOLVED destination,
// the same shape probe, open, forward and spawn-ssh carry.
type ConnectionParams struct {
	Destination SSHDestination `json:"destination"`
}

// ConnectionResult names the connection the destination's shell will ride.
//
// It is a derived identity and not a minted one, so two coordinators asking
// about one destination get one answer, and the answer survives a restart of
// either process. The coordinator uses it only as a correlation key; what a
// person reads is the state, never this string.
type ConnectionResult struct {
	ConnectionID string `json:"connectionId"`
}

// ConnectionStateEvent is one observation of a connection, as the coordinator
// publishes it per pane.
//
// # Which fields belong to which state, and why they are not one shape
//
// Design §5.6 makes the same distinctions the state machine does: a stage
// exists while opening, a person only while waiting, a cause only when a
// connection failed or ended, an attempt only while reconnecting. Sending them
// all at once would leave a receiver guessing which of them the current state
// makes true, and a shape that cannot exist cannot be sent. The helper fills
// exactly the fields its state names; the coordinator's own wire schema is the
// stricter half (one branch per state) and this one stays flat because a
// generation's ABI is frozen against the fields, not against the combination.
type ConnectionStateEvent struct {
	// ConnectionID is the supervisor's connection, the same value the
	// `connection` op answered for this destination.
	ConnectionID string `json:"connectionId"`
	// Seq orders observations of ONE connection. Monotonic for the life of the
	// supervisor; a receiver drops anything not greater than the last sequence
	// it applied, which is how a late event for an old epoch is refused.
	Seq uint64 `json:"seq"`
	// State is one of Connection*'s closed set.
	State string `json:"state"`
	// Stage is where an opening is: resolve, tcp, ssh-version, key-exchange,
	// sign-in, shell.
	Stage string `json:"stage,omitempty"`
	// Person is what a waiting_person state waits for: host-key,
	// helper-consent, vault-unlock, passphrase, password, second-factor,
	// key-signature.
	Person string `json:"person,omitempty"`
	// Cause is why a failed or ended connection stopped: host-key-changed,
	// auth-rejected, no-auth-method, lost-link, released, silence, tainted.
	Cause string `json:"cause,omitempty"`
	// Attempt is how many attempts a reconnecting connection has made since it
	// last worked.
	Attempt int `json:"attempt,omitempty"`
	// StateMs is how long the connection has been in this state, at the
	// observation. The renderer ticks it locally from there, so a quiet
	// connection costs no frames.
	StateMs int64 `json:"stateMs"`
	// LookAnsweredAt is when a look-only dial was answered during the current
	// stall, RFC3339 in UTC. It means the HOST accepts new connections while
	// this flow is silent — evidence for a person, never a verdict on the flow.
	LookAnsweredAt string `json:"lookAnsweredAt,omitempty"`
	// PlainShells is how many shells would end if this connection closed: its
	// own, and those of everything dialled through it. It is what makes
	// "Reconnect ends 2 shells" sayable, and it is why silence never closes a
	// connection carrying one.
	PlainShells int `json:"plainShells,omitempty"`
	// RoundTripMs is the newest probe's measurement, absent until a probe has
	// answered. Absent and zero are the same statement: nothing measured.
	RoundTripMs int64 `json:"roundTripMs,omitempty"`
	// Slow is the backend's GRADE of that measurement, with hysteresis; see
	// hostconn.GradeRoundTrip. Slow without a measurement never happens.
	Slow bool `json:"slow,omitempty"`
}
