package session

// One-shot targets minted from a retained snapshot (nocx-6q1uh.4, spec §6.2):
// the structural digest of a coordinator-chosen row range, bound by an
// HMAC-SHA256 signature under a key this book draws from crypto/rand and
// never exports, spent at most once at the session's commit point.
//
// # Why a token rather than a live reference
//
// A target names rows the coordinator already classified — a menu, an input
// box — and the write that follows must be judged against the SAME screen,
// never a fresher one a program redrew underneath it. A live pointer into the
// runtime cannot promise that: the runtime moves on every ingest. A signed,
// self-describing token can: it carries the digest it was minted against, so
// [tokenBook.Verify] and the runtime's own commit-point check both judge the
// live screen against a fixed, tamper-evident description of the screen the
// coordinator actually saw.
//
// # Bounded, and one-shot
//
// Minting reserves a slot; a session holds at most [maxLiveTokens]. Nothing
// is ever evicted to make room — a coordinator still holding a live replay
// barrier is never the thing capacity is bought back from — so a full book
// refuses [ErrCapacity] before a token exists at all. A slot releases only
// once its token has expired AND [resultRetention] has passed since its
// result became terminal (or since it expired unused): retention never
// depends on a result having been read, because this book cannot observe
// receipt.
import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// TokenID names one minted target. It is drawn from crypto/rand rather than
// counted: a counted id would let one session's live tokens be enumerated by
// a caller that only ever saw one of them, leaving the MAC as the only thing
// standing between a guessed id and a live slot.
type TokenID [16]byte

// Token is a one-shot target (spec §6.2): the structural digest of the rows a
// coordinator classified, the identity and geometry it was read at, and an
// HMAC over all of it under a key no caller outside [tokenBook] ever holds.
type Token struct {
	ID            TokenID
	Session       proto.HostSessionID
	At            sessionruntime.Incarnation
	Identity      sessionruntime.ScreenIdentity
	Kind          sessionruntime.TargetKind
	Rows          sessionruntime.RowRange
	Digest        [32]byte
	IncludeCursor bool
	AccessEpoch   uint64
	MintedAt      time.Time
	ExpiresAt     time.Time
	MAC           [32]byte
}

const (
	// maxLiveTokens bounds how many slots one session's book holds at once
	// (spec §6.2).
	maxLiveTokens = 256
	// tokenLifetime is how long a minted token verifies before [ErrExpired]
	// (spec §6.2).
	tokenLifetime = 60 * time.Second
	// resultRetention is how long a slot's result is kept, from the moment it
	// became terminal, before the slot is eligible for release (spec §6.2).
	resultRetention = 5 * time.Minute
)

var (
	// ErrCapacity names a book already holding maxLiveTokens live slots.
	// Mint refuses it rather than evicting anything.
	ErrCapacity = errors.New("session: capacity")
	// ErrForged names a token whose signature does not match this book's
	// key, or whose incarnation is not this book's own.
	ErrForged = errors.New("session: forged")
	// ErrExpired names a token whose lifetime has passed.
	ErrExpired = errors.New("session: expired")
	// ErrTokenSpent names a token already consumed under a DIFFERENT
	// canonical intent, or one whose slot no longer exists at all — the two
	// look the same from Consume's side of the lock: nothing left to give
	// back for it.
	ErrTokenSpent = errors.New("session: token_spent")
	// ErrSnapshotGone names a mint attempted against a snapshot id the ring
	// no longer holds — evicted by age or overwritten by a newer one.
	ErrSnapshotGone = errors.New("session: snapshot_gone")
)

// canonicalIntent is what a token is bound to at consume time (spec §6.2): a
// second session.intent with the SAME token and the same canonical intent
// replays the recorded (or in-flight) result rather than writing twice; a
// DIFFERENT one is refused [ErrTokenSpent].
type canonicalIntent struct {
	Kind        sessionruntime.IntentKind
	Payload     []byte
	AccessEpoch uint64
}

// equal compares two canonical intents by VALUE. Payload is a []byte, so
// canonicalIntent is not itself comparable with ==; this is the one place
// that matters, because Consume's whole "same or different" distinction
// depends on it.
func (c canonicalIntent) equal(o canonicalIntent) bool {
	return c.Kind == o.Kind && c.AccessEpoch == o.AccessEpoch && bytes.Equal(c.Payload, o.Payload)
}

// storedResult is the compact, bounded record a spent token keeps (spec
// §6.2): never regionNow, and small enough that holding maxLiveTokens of
// them at once is not a budget anyone has to think about — see
// tokens_test.go's own check that every cause encodes under 128 bytes.
type storedResult struct {
	State        string `json:"state"`
	BytesWritten int    `json:"bytesWritten,omitempty"`
	FenceAfter   uint64 `json:"fenceAfter,omitempty"`
	Cause        string `json:"cause,omitempty"`
}

// tokenSlot is one reserved record, from Mint through release.
type tokenSlot struct {
	token Token
	// canonical is nil until the FIRST session.intent binds it (Consume);
	// a pointer rather than a value so "never consumed" and "consumed with
	// the zero canonicalIntent" are distinguishable.
	canonical *canonicalIntent
	// result is nil until Record. A consumed-but-unrecorded slot is
	// in-flight: Consume answers inProgress for it and sweepLocked never
	// releases it, because nothing here could say the write is truly done.
	result *storedResult
	// terminalAt is when result was recorded — the reference point
	// resultRetention counts from. It is meaningless while result is nil.
	terminalAt time.Time
}

// tokenBook is one session incarnation's live tokens (spec §6.2): the HMAC
// key drawn at construction and never exported, and the bounded, one-shot
// slots minted from it. Safe for concurrent use — a book's Verify, Consume,
// Record and Status are called from whatever RPC or owner goroutine handles
// session.snapshot/target/intent/intent-status, and Consume in particular
// must serialise a race between two submissions of the same token (see its
// own doc).
type tokenBook struct {
	mu      sync.Mutex
	key     [32]byte // immutable after construction; read without mu
	at      sessionruntime.Incarnation
	session proto.HostSessionID
	now     func() time.Time
	slots   map[TokenID]*tokenSlot
}

// newTokenBook draws a fresh 32-byte key from crypto/rand for the incarnation
// named by at — never persisted, never logged, and never reused: a new
// incarnation (a respawned shell) is a new key, so a token minted before a
// restart can never verify against the session that replaces it.
//
// now is injected so a caller can drive Mint's capacity sweep and a slot's
// release with a fake clock instead of a real 60-second and 5-minute wait
// (AGENTS.md: no test depends on timing).
func newTokenBook(at sessionruntime.Incarnation, now func() time.Time) *tokenBook {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		// crypto/rand failing means this machine cannot mint an unguessable
		// key at all, which is not a condition this book can degrade under —
		// a zero key would sign every session's tokens identically. This
		// runs once per session, at spawn, the same place a construction
		// failure elsewhere in this package already aborts the spawn.
		panic(fmt.Sprintf("session: tokenBook: crypto/rand: %v", err))
	}
	return &tokenBook{key: key, at: at, now: now, slots: map[TokenID]*tokenSlot{}}
}

// bindSession names the session id this book's tokens report themselves
// under. It exists because [proto.HostSessionID] is minted by
// [Service.finishSpawn]'s hostSession literal, one line after the book
// itself is constructed — at construction time there is no id yet to give
// it. Called exactly once, before either the owner or any RPC handler can
// reach this book.
func (b *tokenBook) bindSession(id proto.HostSessionID) {
	b.mu.Lock()
	b.session = id
	b.mu.Unlock()
}

// Mint reserves a slot and signs a token over s's retained rows (spec §6.2).
// includeCursor is true exactly for a [sessionruntime.TargetInput] target —
// the one kind whose digest depends on where the caret is, not only on what
// is drawn.
//
// It refuses [ErrCapacity] at maxLiveTokens live slots WITHOUT evicting
// anything: a replay barrier a coordinator is still holding must never be
// the thing this book makes room by discarding.
func (b *tokenBook) Mint(s retainedSnapshot, kind sessionruntime.TargetKind, rows sessionruntime.RowRange) (Token, error) {
	includeCursor := kind == sessionruntime.TargetInput
	digest := sessionruntime.Digest(s.Identity, s.Rows, rows, s.Cursor, includeCursor)

	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.sweepLocked(now)
	if len(b.slots) >= maxLiveTokens {
		return Token{}, ErrCapacity
	}

	var id TokenID
	if _, err := rand.Read(id[:]); err != nil {
		return Token{}, fmt.Errorf("session: mint token id: %w", err)
	}
	t := Token{
		ID:            id,
		Session:       b.session,
		At:            b.at,
		Identity:      s.Identity,
		Kind:          kind,
		Rows:          rows,
		Digest:        digest,
		IncludeCursor: includeCursor,
		AccessEpoch:   s.AccessEpoch,
		MintedAt:      now,
		ExpiresAt:     now.Add(tokenLifetime),
	}
	t.MAC = b.mac(t)
	b.slots[id] = &tokenSlot{token: t}
	return t, nil
}

// Verify checks a token's signature, its incarnation and its lifetime. It
// never touches a slot: a verified token whose slot has since been released
// is [Consume]'s to answer, not this method's — Verify answers a question
// about the TOKEN, Consume about the BOOK's record of it.
func (b *tokenBook) Verify(t Token) error {
	mac := b.mac(t)
	if !hmac.Equal(mac[:], t.MAC[:]) {
		return ErrForged
	}
	if t.At != b.at {
		// Belt and braces: a mismatched incarnation would already fail the
		// MAC above, since [newTokenBook] draws a fresh key per incarnation.
		// This check is what makes that a NAMED invariant rather than an
		// accident of how the key happens to be drawn.
		return ErrForged
	}
	if b.now().After(t.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// mac signs every field of t except MAC itself, fixed-width and
// length-prefixed throughout so that no two distinct field sequences can
// ever sign the same way. b.key is immutable after [newTokenBook], so this
// needs no lock.
func (b *tokenBook) mac(t Token) [32]byte {
	h := hmac.New(sha256.New, b.key[:])
	writeMacString(h, t.Session.Session)
	writeMacString(h, string(t.Session.Generation))
	writeMacString(h, string(t.At.Session))
	writeMacUint64(h, uint64(t.At.Generation))
	writeMacBool(h, t.Identity.AltScreen)
	writeMacUint64(h, t.Identity.BufferInstance)
	writeMacUint64(h, uint64(t.Identity.Cols)) // #nosec G115 -- a terminal geometry, always positive
	writeMacUint64(h, uint64(t.Identity.Rows)) // #nosec G115 -- a terminal geometry, always positive
	writeMacString(h, string(t.Kind))
	writeMacUint64(h, uint64(t.Rows.First)) // #nosec G115 -- signed for hashing only, never indexed
	writeMacUint64(h, uint64(t.Rows.Last))  // #nosec G115 -- signed for hashing only, never indexed
	h.Write(t.Digest[:])
	writeMacBool(h, t.IncludeCursor)
	writeMacUint64(h, t.AccessEpoch)
	writeMacUint64(h, uint64(t.MintedAt.UnixNano()))  // #nosec G115 -- a Unix nanosecond timestamp, positive until the year 2262
	writeMacUint64(h, uint64(t.ExpiresAt.UnixNano())) // #nosec G115 -- a Unix nanosecond timestamp, positive until the year 2262
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeMacUint64(h hash.Hash, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	h.Write(buf[:])
}

func writeMacBool(h hash.Hash, v bool) {
	if v {
		writeMacUint64(h, 1)
		return
	}
	writeMacUint64(h, 0)
}

func writeMacString(h hash.Hash, s string) {
	writeMacUint64(h, uint64(len(s)))
	h.Write([]byte(s))
}

// Consume is the commit point's one-shot gate (spec §6.2): "unused ->
// consumed bound to intent; same token+intent -> recorded or in_progress;
// different -> ErrTokenSpent."
//
//   - recorded is non-nil only once the bound write has a stored result.
//   - inProgress is true while the FIRST caller's write is still in flight
//     (bound, no result yet) and the caller currently asking is the same one
//     that bound it — a genuine replay, not a hijack.
//   - err is [ErrTokenSpent] for a different canonical intent, whether that
//     is a real conflict or a token this book never minted (or already
//     released) at all: a caller reaches Consume only after [Verify]
//     succeeded, and a released slot's token has always already expired, so
//     Verify would have refused it first in every production path.
//
// It is safe under concurrent calls with the SAME token: exactly one caller
// observes canonical == nil and binds it, and every other caller — whatever
// order they arrive in relative to that one — sees either inProgress or the
// recorded result for a matching intent, or ErrTokenSpent for a different
// one. There is no window in which two callers can both believe they bound
// the slot.
func (b *tokenBook) Consume(t TokenID, in canonicalIntent) (recorded *storedResult, inProgress bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	slot, ok := b.lookupLocked(t)
	if !ok {
		return nil, false, ErrTokenSpent
	}
	if slot.canonical == nil {
		bound := in
		slot.canonical = &bound
		return nil, false, nil
	}
	if !slot.canonical.equal(in) {
		return nil, false, ErrTokenSpent
	}
	if slot.result != nil {
		res := *slot.result
		return &res, false, nil
	}
	return nil, true, nil
}

// Record stores the outcome once the owner's write settles (or a refusal is
// decided before ever reaching one), making it the answer replayed to a
// same-token, same-intent Consume and to [Status] until the slot releases.
// A token this book does not hold — already released, or never minted — is
// silently ignored: there is nothing left to correct.
func (b *tokenBook) Record(t TokenID, r storedResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	slot, ok := b.slots[t]
	if !ok {
		return
	}
	slot.result = &r
	slot.terminalAt = b.now()
}

// Status answers session.intent.status without presenting a payload (spec
// §6.2): "unknown" for a token this book never minted, has released, or has
// only minted and not yet had an intent bound to it; "in_progress" for one
// bound and awaiting a result; the recorded result otherwise.
func (b *tokenBook) Status(t TokenID) (state string, r *storedResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	slot, ok := b.lookupLocked(t)
	if !ok {
		return "unknown", nil
	}
	if slot.result != nil {
		res := *slot.result
		return "recorded", &res
	}
	if slot.canonical != nil {
		return "in_progress", nil
	}
	return "unknown", nil
}

// lookupLocked answers a slot only if it exists AND has not become
// releasable since the last sweep — a released slot is deleted on the spot,
// so [Consume] and [Status] never need [sweepLocked] to have run recently to
// answer correctly; sweepLocked (Mint's own) exists only to reclaim CAPACITY
// ahead of need, not for correctness.
func (b *tokenBook) lookupLocked(id TokenID) (*tokenSlot, bool) {
	slot, ok := b.slots[id]
	if !ok {
		return nil, false
	}
	if releasable(slot, b.now()) {
		delete(b.slots, id)
		return nil, false
	}
	return slot, true
}

// sweepLocked releases every slot eligible under [releasable]. It runs at
// the head of Mint, which is the only place a released slot's capacity is
// ever needed back: there is no background goroutine, so a book that mints
// once and is never asked to mint again simply never sweeps, and that is
// fine — nothing depends on a sweep happening before it is asked for.
func (b *tokenBook) sweepLocked(now time.Time) {
	for id, slot := range b.slots {
		if releasable(slot, now) {
			delete(b.slots, id)
		}
	}
}

// releasable is spec §6.2's release rule: "only when both its token has
// expired AND 5 minutes have passed since the token reached a terminal
// state (or since it expired unused)". A slot consumed but never recorded —
// not a real production path, since the owner always Records one way or
// another, but a defensive case worth naming — is never released: nothing
// here could say the write is truly done, so discarding it would let a
// genuine replay see [ErrTokenSpent] instead of its own result.
func releasable(slot *tokenSlot, now time.Time) bool {
	if !now.After(slot.token.ExpiresAt) {
		return false
	}
	ref := slot.token.ExpiresAt
	switch {
	case slot.result != nil:
		ref = slot.terminalAt
	case slot.canonical != nil:
		return false
	}
	return now.Sub(ref) >= resultRetention
}

// --- the commit-point hook (owner.go, nocx-6q1uh.4) -------------------------
//
// commitIntent calls tokenGate before it ever touches sessionruntime.Admit,
// and recordTokenOutcome once Admit/Commit or the write itself has settled.
// Both live here rather than in owner.go so that owner.go's own diff stays
// the "small, localized hook" the task's own brief asks for: everything that
// knows this book's vocabulary — Verify, Consume, Record, the refusal causes
// — is in this file.

// errIncomparable and errStaleTarget are checkToken's two refusals (spec
// §6.2): a structural change since mint (buffer, geometry or incarnation)
// is ALWAYS incomparable, checked before the digest is recomputed at all —
// comparing rows across a resize is not "the same content, changed" so much
// as a question that no longer has an answer. A digest mismatch with an
// unchanged identity is stale_target: the same screen, different content.
var (
	errIncomparable = errors.New("session: incomparable")
	errStaleTarget  = errors.New("session: stale_target")
)

// checkToken builds the [sessionruntime.Session.Commit] check for a
// token-bearing intent. It is this task's own vocabulary (Digest,
// ScreenIdentity) that decides both of its refusals, which is why it lives
// here rather than being assembled by whoever wires session.intent
// (nocx-6q1uh.5): that caller supplies the token and the canonical intent
// and gets this function back ready to pass as pendingIntent.Check.
func checkToken(t Token) func(sessionruntime.Snapshot) error {
	return func(snap sessionruntime.Snapshot) error {
		if snap.Identity != t.Identity {
			return errIncomparable
		}
		got := sessionruntime.Digest(snap.Identity, snap.Rows, t.Rows, snap.Cursor, t.IncludeCursor)
		if got != t.Digest {
			return errStaleTarget
		}
		return nil
	}
}

// tokenGate is commitIntent's hook into this book: Verify, then Consume,
// then the commitBy deadline, all decided before commitIntent ever calls
// Admit. It resolves it and answers true for every outcome except "unused ->
// proceed", which answers false and lets commitIntent continue into
// Admit/Commit exactly as it would for a token-less intent — pi.Check
// (ordinarily [checkToken] bound to pi.Token) is what the runtime's own
// commit point then runs against a fresh, consistent read.
func (o *sessionOwner) tokenGate(it ownerItem, pi *pendingIntent) (handled bool) {
	tb := o.tokens
	if tb == nil {
		// A token arrived before SetTokens ran — a construction ordering
		// bug elsewhere, never a caller's mistake, so it is reported rather
		// than silently treated as "no token".
		o.resolve(it, ownerResult{
			State: sessionruntime.IntentStateRefused,
			Err:   errors.New("session: an intent carried a token and no token book is bound"),
		})
		return true
	}
	if err := tb.Verify(pi.Token); err != nil {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateRefused, Err: err})
		return true
	}
	recorded, inProgress, err := tb.Consume(pi.Token.ID, pi.Canonical)
	if err != nil {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateRefused, Err: err})
		return true
	}
	if recorded != nil {
		o.resolve(it, resultFromStored(*recorded))
		return true
	}
	if inProgress {
		o.resolve(it, ownerResult{State: sessionruntime.IntentStateAdmitted})
		return true
	}
	// Unused -> now consumed and bound. commitBy is checked here, after
	// binding rather than before, so a replay of the SAME token+intent after
	// a deadline refusal sees the recorded commit_deadline result rather
	// than a fresh chance to race the deadline again.
	if o.nowMono() > pi.CommitBy {
		res := storedResult{State: "refused", Cause: "commit_deadline"}
		tb.Record(pi.Token.ID, res)
		o.resolve(it, resultFromStored(res))
		return true
	}
	return false
}

// recordTokenOutcome writes res to pi's token, if pi carries one and a book
// is bound — a no-op for every intent that carries no token at all (Task 2's
// own tests, still). It runs from every place commitIntent or finishItem
// settles a token-bearing intent's fate: Admit refusing, Commit's check
// refusing (stale_target, incomparable, or anything else pi.Check names),
// and the write itself completing, failing or falling short.
func (o *sessionOwner) recordTokenOutcome(pi *pendingIntent, res ownerResult) {
	if o.tokens == nil || pi.Token.ID == (TokenID{}) {
		return
	}
	stored := storedResult{
		State:        ownerStateName(res.State),
		BytesWritten: res.BytesWritten,
		FenceAfter:   uint64(res.FenceAfter),
	}
	if res.Err != nil {
		stored.Cause = causeOf(res.Err)
	}
	o.tokens.Record(pi.Token.ID, stored)
}

// resultFromStored turns a tokenBook-recorded result back into an
// ownerResult (spec §6.2: a same-token, same-intent replay answers the SAME
// thing the original attempt did). Its Err carries the cause string rather
// than the original error value, which the book never keeps — only the
// compact record does.
func resultFromStored(r storedResult) ownerResult {
	res := ownerResult{
		State:        ownerStateFromName(r.State),
		BytesWritten: r.BytesWritten,
		FenceAfter:   sessionruntime.Fence(r.FenceAfter),
	}
	if r.Cause != "" {
		res.Err = fmt.Errorf("session: %s", r.Cause)
	}
	return res
}

// causeOf names an error in the wire's refusal vocabulary (spec §6.5) for
// the causes THIS task's code can produce. access_revoked, no_read_barrier
// and would_submit have no caller yet — nocx-6q1uh.7 and nocx-6q1uh.3 are
// what add them — so an error this switch does not recognise falls back to
// a generic "refused": the original error is still on ownerResult.Err for
// anything inspecting it directly, and the string is read back only from a
// REPLAY of the exact attempt that produced it.
func causeOf(err error) string {
	switch {
	case errors.Is(err, ErrForged):
		return "forged"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrTokenSpent):
		return "token_spent"
	case errors.Is(err, ErrSnapshotGone):
		return "snapshot_gone"
	case errors.Is(err, ErrCapacity):
		return "capacity"
	case errors.Is(err, errIncomparable):
		return "incomparable"
	case errors.Is(err, errStaleTarget):
		return "stale_target"
	case errors.Is(err, sessionruntime.ErrCompletenessUnknown):
		return "completeness_unknown"
	case errors.Is(err, sessionruntime.ErrIntentUnsupported):
		return "cannot_encode"
	case errors.Is(err, sessionruntime.ErrUnavailable):
		return "closing"
	case errors.Is(err, errOwnerClosing):
		return "closing"
	case errors.Is(err, errBusy):
		return "busy"
	default:
		return "refused"
	}
}

// ownerStateName and ownerStateFromName are storedResult.State's own small
// vocabulary, round-tripped through [sessionruntime.IntentState]. It is
// provisional — nocx-6q1uh.5 owns the actual wire vocabulary (spec §6.5's
// executed/refused/failed_partial/delivery_unknown/cancelled/in_progress)
// and is expected to refine this mapping when it wires session.intent's
// response, in particular the failed/failed_partial split this package's
// IntentState does not itself draw.
func ownerStateName(s sessionruntime.IntentState) string {
	switch s {
	case sessionruntime.IntentStateExecuted:
		return "executed"
	case sessionruntime.IntentStateRefused:
		return "refused"
	case sessionruntime.IntentStateFailed:
		return "failed"
	case sessionruntime.IntentStateCancelled:
		return "cancelled"
	case sessionruntime.IntentStateAdmitted:
		return "admitted"
	default:
		return "unknown"
	}
}

func ownerStateFromName(s string) sessionruntime.IntentState {
	switch s {
	case "executed":
		return sessionruntime.IntentStateExecuted
	case "refused":
		return sessionruntime.IntentStateRefused
	case "failed":
		return sessionruntime.IntentStateFailed
	case "cancelled":
		return sessionruntime.IntentStateCancelled
	case "admitted":
		return sessionruntime.IntentStateAdmitted
	default:
		return sessionruntime.IntentStateNone
	}
}
