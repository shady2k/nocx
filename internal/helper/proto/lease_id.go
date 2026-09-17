package proto

// The PROBE LEASE's identity: one value the helper mints, the coordinator
// echoes, and every named probe on that destination is asked under.
//
// # Why a lease is a thing on this wire
//
// AD-4 says one connection per (host, identity), with channels multiplexing
// over it, and it closes when the last holder releases it. A probe is a
// command, not a channel — but a probe asked with no reference held would
// acquire and release inside the one request, which closes the connection only
// for the next probe to redial it. For a host whose credential is a password
// that is a second authentication per sample; for a host running fail2ban it is
// an afternoon's work to get banned.
//
// So the coordinator holds a REFERENCE rather than a connection: `lease`
// acquires one and answers this id, every probe op runs a command on the pooled
// connection that reference keeps alive, and `unlease` drops it. Nothing else
// about the pool changes — a lease is one more holder beside the tabs, and the
// connection still closes when the last of them lets go.
//
// # Why the encoding is shared with ChannelID rather than written again
//
// Both identities are sixteen random bytes the helper owns, both are shown to a
// person and carried in JSON as 32 hex characters, and a second codec would be
// the second vocabulary AD-8 refuses — agreeing on every input anybody tried
// until one of them gained a prefix. So the codec is formatID/parseID/
// unmarshalID in channel_frame.go and this file declares only what is
// different: the type, and therefore what a malformed value is CALLED.
//
// # Why it is neither ChannelID nor ForwardID
//
// A channel is one stream somebody holds and a listener is the thing streams
// arrive on. A lease is neither: it is a REFERENCE to a connection, held for as
// long as a caller wants probes answered on the same transport. Three
// identities, three ops naming them, and an id that could be any of the three
// would make `close`, `unforward` and `unlease` interchangeable at the type
// level while meaning different things on the wire.

import "encoding/json"

// LeaseID identifies one reference the helper holds on a pooled connection for
// the coordinator that asked for it. It is minted by the HELPER — the end that
// owns the connection — echoed verbatim by the coordinator, and opaque in both
// directions.
//
// It is deliberately not an authorization, for the reason ChannelID states:
// same-UID trust already decides who may connect, and a guessed id buys a
// second reference on a connection the guesser is already authenticated for.
type LeaseID [16]byte

// String is the 32-hex form, the same spelling ChannelID uses.
func (l LeaseID) String() string { return formatID(l[:]) }

// IsZero reports whether the id was never minted. Like ChannelID's zero it is
// refused at every boundary rather than looked up: an unset field must not
// resolve to "some connection".
func (l LeaseID) IsZero() bool {
	var zero LeaseID
	return l == zero
}

// MarshalJSON writes the hex form.
func (l LeaseID) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// UnmarshalJSON reads the hex form. Malformed hex is an error and never a zero
// id: a payload that cannot name a lease must be refused, not looked up under
// whichever one the zero value happens to be.
func (l *LeaseID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := ParseLeaseID(s)
	if err != nil {
		return err
	}
	*l = id
	return nil
}

// ParseLeaseID reads the 32-hex form back. It is what JSON decoding goes
// through (UnmarshalJSON), and it is exported for the same reason
// ParseChannelID is: an id that crosses as text has exactly one reader, and it
// should be reachable by name for a log line or a test vector rather than only
// in the middle of a decode.
func ParseLeaseID(s string) (LeaseID, error) {
	var id LeaseID
	return id, parseID("lease id", s, id[:])
}
