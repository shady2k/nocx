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

// OpConnections subscribes the coordinator to EVERY connection this helper
// holds and answers the ones that exist now.
//
// IT IS NOT A WIDER `connection`, and the difference is what a coordinator that
// restarted needs. `connection` names one destination, so a coordinator
// receives events only about the connections its own panes asked for — and a
// connection nobody's pane rides (a tunnel, a probe lease, the far helper's own
// lane) is invisible to it even though the helper is still holding it. Design
// §5.10's seventh scenario is that restart: what the coordinator rebuilds its
// entries from has to be the helper's whole connection table, and it has to
// arrive without a destination to ask about.
//
// The subscription is per coordinator CONNECTION, like `connection-state`'s own
// deliveries, so a helper serving several coordinators (D12) tells each of them
// only what it holds for them. The answer carries the same observation the
// event stream does, so a receiver folds the list and the stream with one
// function.
const OpConnections = "connections"

// OpDisconnect closes the connection a destination rides, if this helper has
// one, and answers whether it did.
//
// IT IS THE RECONNECT'S FIRST MOVE, and the order is the design's (5.3:
// "Reconnect closes the socket first"). A stalled connection is still in the
// pool, so a new shell asked for on the same destination would JOIN it and park
// behind the dead flow instead of dialing; closing it here is what makes the
// acquire that follows a fresh dial. It is idempotent: a destination this
// helper holds no live connection for is answered as closed=false, because the
// ordinary caller is a pane whose transport already ended.
const OpDisconnect = "disconnect"

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
	Destination     SSHDestination `json:"destination"`
	PaneID          string         `json:"paneId,omitempty"`
	ReconnectPolicy string         `json:"reconnectPolicy,omitempty"`
}

// DisconnectParams names the connection to close: the same resolved destination
// every other op takes, so one derivation of "which connection is this" is used
// by asking, watching and closing alike.
type DisconnectParams struct {
	Destination SSHDestination `json:"destination"`
}

// DisconnectResult answers what the close found. Closed is false for a
// destination this helper holds no live connection for, which is a normal
// answer rather than a failure: the caller asked to make sure it is gone.
type DisconnectResult struct {
	ConnectionID string `json:"connectionId"`
	Closed       bool   `json:"closed"`
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
	// Generation is which of the supervisor's LINKS this observation is about,
	// counting from one.
	//
	// IT IS ON THE WIRE BECAUSE IT CANNOT BE DERIVED, and this is the field's
	// whole justification. A coordinator is handed the observations it reads,
	// and the driver refuses any that is not newer — so a `reconnecting` that
	// was followed by `connected` before the coordinator read arrives as ONE
	// `connected`. "The state came back" is then the same fact whether the
	// transport survived the stall or was replaced under it, and a coordinator
	// that must drop what belongs to a link it has lost (a far helper's answer,
	// an attempt's result) would drop it by guessing. The supervisor is the
	// only party holding the link, so the number travels from there.
	//
	// It moves when a link is retired and when a new one is admitted, and it
	// stands still for every other transition: a stall and its recovery are one
	// transport. Zero has one meaning — an observation taken before this
	// connection ever had a link — and a receiver that needs a link's identity
	// treats it as "none yet" rather than as a number.
	Generation uint64 `json:"generation"`
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

// ConnectionsParams asks for the connection table, and asks to be kept current
// about it. It carries nothing: the answer is "what this helper holds for THIS
// coordinator", and the coordinator is a property of the connection the request
// arrived on (D12), not of a field.
type ConnectionsParams struct{}

// ConnectionsResult is the connection table this coordinator now watches.
//
// Connections is never null on the wire: an empty table is `[]`, because a
// decoder distinguishing "this helper holds nothing" from "no answer" needs the
// empty array to arrive as one — and a coordinator that read a missing list as
// "nothing" would rebuild nothing after a restart and never notice.
type ConnectionsResult struct {
	Connections []ConnectionSummary `json:"connections"`
}

// ConnectionSummary is one connection in that table: the same observation the
// event stream carries, plus the two facts a stream does not need to repeat.
//
// The observation is the EVENT TYPE and not a second shape for the same state,
// so a receiver folds a list entry and a live event with one function (AD-8).
// What it adds is what a coordinator rebuilding its entries needs and what an
// event cannot carry: the destination the connection was acquired for, and the
// host key its current link was verified against. Both are absent when this
// helper does not know them — a connection named by a reconnect before anything
// dialled has a destination and no key, and one whose destination predates this
// bookkeeping has neither.
type ConnectionSummary struct {
	Connection ConnectionStateEvent `json:"connection"`
	// Destination is the RESOLVED destination this connection was acquired for,
	// exactly as the op that acquired it carried it. It is what a coordinator
	// that restarted redials or re-uses, and it cannot be re-derived from the
	// connection id: the id is a digest over the account, the credential
	// principal and the route, and a digest is not a destination.
	Destination *SSHDestination `json:"destination,omitempty"`
	// HostKeyFingerprint is the SHA256 fingerprint of the host key the current
	// link was verified against. The host's mode and the person's consent are
	// both keyed by it (ADR-0034, ADR-727g63tr), so a coordinator that
	// restarted cannot rebuild an entry without it.
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
}

// OpReconnect registers one supervisor-owned reconnect; progress is an event.
const OpReconnect = "reconnect"

// OpReleaseConnection drops the pane's logical hold, even after its shell died.
const OpReleaseConnection = "release-connection"

type ReconnectParams struct {
	Destination SSHDestination `json:"destination"`
	ExpectedSeq uint64         `json:"expectedSeq,omitempty"`
}
type ReleaseConnectionParams struct {
	PaneID string `json:"paneId"`
}
type ReconnectResult struct {
	Registered bool `json:"registered"`
}
type ReleaseConnectionResult struct {
	Released bool `json:"released"`
}
