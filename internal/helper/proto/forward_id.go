package proto

// The REMOTE LISTENER's identity: one value the helper mints, the coordinator
// echoes, and every accepted connection on it is announced under.
//
// # Why the encoding is shared with ChannelID rather than written again
//
// Both identities are sixteen random bytes the helper owns, both are shown to
// a person and carried in JSON as 32 hex characters, and the two codecs living
// apart would be the second vocabulary AD-8 refuses — agreeing on every input
// anybody tried until one of them gained a prefix. So the codec is
// formatID/parseID/unmarshalID in channel_frame.go and this file declares only
// what is different: the type, and therefore what a malformed value is
// CALLED.
//
// # Why it is not ChannelID
//
// A channel is one stream somebody holds; a listener is the thing streams
// arrive ON. They are created by different ops, ended by different ops
// (`close` versus `unforward`), and an id that could be either would make
// those two interchangeable at the type level while meaning different things
// on the wire — which is exactly the confusion the separate `close` and
// `unforward` exist to prevent.

import "encoding/json"

// ForwardID identifies one listener the helper holds for the life of the
// connection that asked for it. Like ChannelID it is minted by the HELPER —
// the end that owns the listener — echoed verbatim by the coordinator, and
// opaque in both directions.
//
// It is deliberately not an authorization, for the reason ChannelID states:
// same-UID trust already decides who may connect, and a guessed id buys a
// second listener on a connection the guesser is already authenticated for.
type ForwardID [16]byte

// String is the 32-hex form, the same spelling ChannelID uses.
func (f ForwardID) String() string { return formatID(f[:]) }

// IsZero reports whether the id was never minted. Like ChannelID's zero it is
// refused at every boundary rather than looked up: an unset field must not
// resolve to "some listener".
func (f ForwardID) IsZero() bool {
	var zero ForwardID
	return f == zero
}

// MarshalJSON writes the hex form.
func (f ForwardID) MarshalJSON() ([]byte, error) { return json.Marshal(f.String()) }

// UnmarshalJSON reads the hex form. Malformed hex is an error and never a zero
// id: a payload that cannot name a listener must be refused, not looked up
// under whichever one the zero value happens to be.
func (f *ForwardID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := ParseForwardID(s)
	if err != nil {
		return err
	}
	*f = id
	return nil
}

// ParseForwardID reads the 32-hex form back. It is what JSON decoding goes
// through (UnmarshalJSON), and it is exported for the same reason
// ParseChannelID is: an id that crosses as text has exactly one reader, and it
// should be reachable by name for a log line or a test vector rather than only
// in the middle of a decode.
func ParseForwardID(s string) (ForwardID, error) {
	var id ForwardID
	return id, parseID("forward id", s, id[:])
}
