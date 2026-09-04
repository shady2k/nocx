package wave

// The mailbox: the second of the six things §6 of the 2026-08-24
// orchestration mechanism design says the record holds, and the one this
// package had left unbuilt.
//
// # A read takes nothing from anyone
//
// This exists because of a measured failure: two readers, one mailbox, and
// seventeen minutes in which mail that had been consumed by the first was
// invisible to the second and to everybody looking for it. The cure is not a
// better queue, it is a different verb. Reading is not TAKING. A message is a
// row that stays where it is; what moves is a cursor, and every reader has
// its own.
//
// So a mailbox here is not a queue and cannot be drained. Two readers read
// the same messages, in the same order, and neither one's read moves the
// other's cursor.
//
// # Four acknowledgements, and the three a backend can witness
//
// D8 keeps four facts apart and never merges them: committed to the mailbox,
// FETCHED by the participant, present in the MODEL'S CONTEXT, and ACTED UPON.
// Collapsing them into one "delivered" is how consumed mail became invisible
// in the failure above.
//
// The backend can witness three of the four. It knows what it committed, it
// knows what it handed out, and it knows what a reader CLAIMED it acted on.
// It cannot know what reached a model's context — nothing outside the model
// can — so this package does not have a word for it and must not invent one.
// A record that claimed the third would be the self-matching sentinel the
// whole design is written against.
//
// The cursor advances on the SECOND. Two marks, and both are load-bearing:
// fetched is what stops a message being handed out forever, and acted is what
// stops a retry of the same response committing the same spawn twice — "read
// consumes nothing" prevents loss and does not prevent duplication, and §7.2
// says the reader acknowledges the cursor together with the effects it
// commits from that response.
//
// # Whose read counts as delivery
//
// The RECIPIENT's own cursor, and no other reader's. A second reader looking
// into a mailbox — which is what mesh will bring — moves its own cursor and
// delivers nothing, because a message is delivered when the participant it
// was addressed to has taken it, not when somebody has seen it.

import (
	"errors"
	"time"
)

// ReaderID names a mailbox and names a reader, deliberately with one type.
//
// A mailbox belongs to whoever is addressed; a reader is whoever is looking.
// In star topology those are the same string for every legitimate read, and
// when mesh arrives they are not — so a design with two types would have to
// convert between them at every call site, which is where the confusion the
// 17-minute failure came from would come back.
//
// The value itself is BACKEND-OWNED and never supplied by a caller (A9): a
// worker is named by its participant id, and a coordinator by its session
// (AD-7), which is what makes a restarted coordinator the same reader — the
// property D3 already rests on.
type ReaderID string

// MessageID names one message.
type MessageID string

// MaxMessageBytes bounds one message's body.
//
// It is a bound and not a policy: §10.9 lists message size among the nine
// still open, and a mailbox with no ceiling is a way to fill an encrypted
// store from inside an agent's turn. The number is generous enough for the
// only thing mail is for here — an instruction or a result summary — and far
// too small to move a transcript through, which is deliberate.
const MaxMessageBytes = 16 << 10

// MaxFetch bounds one fetch. A reader that has fallen behind catches up over
// several calls rather than being handed an unbounded page it must fit in a
// context window.
const MaxFetch = 50

// Message is one thing that was said, and it is content: nothing derives
// authority from a body, and no state anywhere is decided by one.
type Message struct {
	ID   MessageID
	Wave ID
	// Recipient is the mailbox it sits in.
	Recipient ReaderID
	// Sender is who wrote it. Stamped by the backend from the authenticated
	// caller, never carried in the call: a sender a caller could name is a
	// sender a caller could forge.
	Sender ReaderID
	// Seq is the message's position in ITS MAILBOX, monotonic and dense
	// enough to compare. It is what a cursor points at.
	Seq         int64
	Body        string
	CommittedAt time.Time
}

// Cursor is one reader's position in one mailbox.
//
// Two marks, because D8 keeps two of its four acknowledgements here and they
// answer different questions: Fetched is "what have I been handed", Acted is
// "what have I finished committing the effects of". A reader that fetched and
// then died mid-effect is exactly the case the second mark exists for.
//
// The two marks are also the whole of what a message's standing IS, which is
// why there is no separate word for it here: a message at or below Acted was
// acted on, at or below Fetched was handed over, and above Fetched is
// COMMITTED AND NOT DELIVERED — the state the measured failure made
// invisible, and the one Undelivered reports as itself. A three-valued type
// beside these marks would be a second derivation of one fact, and the one
// with no enforcement behind it.
type Cursor struct {
	Mailbox ReaderID
	Reader  ReaderID
	// Fetched is the highest Seq handed to this reader. It advances on the
	// FETCH, which is D8's second acknowledgement and the only one the
	// backend can witness at the moment it happens.
	Fetched int64
	// Acted is the highest Seq the reader has said it finished acting on. It
	// advances only when the reader says so, because only the reader knows.
	Acted     int64
	UpdatedAt time.Time
}

// Fetch is what one inbox check returns: the messages, and the cursor as it
// stands after them.
//
// The cursor travels WITH the messages because §7.2 requires the reader to
// acknowledge a position together with the effects it commits from that
// response. A reader handed messages and no position could only acknowledge
// by guessing.
type Fetch struct {
	Messages []Message
	Cursor   Cursor
	// More reports that the mailbox holds messages past this page. It is a
	// fact about the mailbox and not an invitation: a reader that stops
	// reading loses nothing, because nothing was consumed.
	More bool
}

var (
	// ErrNotAMember means the sender or the recipient is not in the wave.
	// MEMBERSHIP is what makes a participant addressable, and it is the only
	// thing mail is checked against — a delegation is what makes a
	// participant controllable, and a human takeover that suspends control
	// must not also stop the coordinator being able to write to its worker.
	ErrNotAMember = errors.New("wave: not a member of that wave")
	// ErrMessageTooLarge means the body exceeds MaxMessageBytes.
	ErrMessageTooLarge = errors.New("wave: message is too large")
	// ErrEmptyMessage means there was nothing to say. It is refused rather
	// than committed, because an empty row in a mailbox costs a reader a
	// fetch and tells it nothing.
	ErrEmptyMessage = errors.New("wave: message is empty")
)
