package session

// The RPC-facing half of the one-shot write path (nocx-6q1uh.6, spec §6, §7.2):
// session.snapshot, session.target, session.intent, session.intent-status and
// session.access-bump. Everything that decides WHAT the wire's five ops mean
// lives here; owner.go, tokens.go and access.go are what they call into.
//
// # Why session.intent grants control the first time it is asked
//
// sessionruntime.Session.Admit refuses ErrNoController until somebody calls
// GrantControl (ADR-0066's own authority model), and nothing before this task
// ever did: the runtime's control epoch exists for the day several principals
// may compete for one session (nocx-6q1uh.7's DescendantPaneAccess), and until
// that capability layer lands there is exactly one legitimate writer of a
// session's input — the coordinator holding it — so the first session.intent
// this session ever receives grants control to a single, permanent "session
// controller" principal rather than leaving every wire-driven intent refused
// ErrNoController forever. ensureControl is the whole of that: idempotent,
// serialised under hs.mu (the same mutex the write-capability bookkeeping
// already uses), and never re-granted once a holder exists.
//
// It is deliberately NOT granted eagerly at spawn (finishSpawn, service.go):
// that would make this method's own grant path dead code in every production
// spawn — check-then-grant always finding one already in force — and Task 7
// is what gives this a real per-caller identity, not a second place that
// grants a placeholder one.
import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// errBadTargetKind names a session.target request naming a kind outside the
// closed set spec §6.3 draws (menu, input, working, region). It is the
// caller's request that is wrong, not the session's state, which is why
// Service.Refusal (service.go) maps it to ErrCodeBadParams rather than to
// any of session.intent's own wire refusal causes.
var errBadTargetKind = errors.New("session: target kind is not one of menu, input, working, region")

// regionNowMax bounds session.intent's own regionNow (spec §6.5, "Global
// constraints": named once, referenced by tests): 16 KiB of normalised text,
// enough to show a caller what is on screen without letting an oversized
// region turn one refusal into an unbounded payload.
const regionNowMax = 16 << 10

// retryAfterInProgressMs is what session.intent answers alongside
// state:"in_progress" (a replay of a token whose first attempt has not yet
// settled): how long before asking session.intent-status again is worth it.
// It is advisory — a caller may poll sooner — and chosen to be comfortably
// under a human's own sense of "instant" without polling so often that a
// still-blocked write is asked about needlessly.
const retryAfterInProgressMs = 200

// --- session.snapshot --------------------------------------------------------

func (s *Service) snapshot(p proto.SnapshotParams) (proto.SnapshotResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.SnapshotResult{}, err
	}
	snap, err := hs.takeSnapshot()
	if err != nil {
		return proto.SnapshotResult{}, err
	}
	return proto.SnapshotResult{
		SnapshotID:   uint64(snap.ID),
		Frame:        screenFrame(snap.Frame),
		Revision:     uint64(snap.Revision),
		InputFence:   uint64(snap.InputFence),
		Completeness: completenessName(snap.Completeness),
		AccessEpoch:  snap.AccessEpoch,
		ReadBarrier:  hs.owner.hasReadBarrier(),
	}, nil
}

// --- session.target ----------------------------------------------------------

func (s *Service) target(p proto.TargetParams) (proto.TargetResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.TargetResult{}, err
	}
	kind, ok := targetKindFromWire(p.Kind)
	if !ok {
		return proto.TargetResult{}, fmt.Errorf("%w: %q", errBadTargetKind, p.Kind)
	}
	tok, err := hs.mintTarget(SnapshotID(p.SnapshotID), kind, sessionruntime.RowRange{First: p.First, Last: p.Last})
	if err != nil {
		return proto.TargetResult{}, err
	}
	wire, err := marshalToken(tok)
	if err != nil {
		return proto.TargetResult{}, fmt.Errorf("session: encode target: %w", err)
	}
	return proto.TargetResult{
		Token:       wire,
		TokenID:     hex.EncodeToString(tok.ID[:]),
		ExpiresAtMs: tok.ExpiresAt.UnixMilli(),
	}, nil
}

// targetKindFromWire validates the wire's kind string against the closed set
// spec §6.3 draws — sessionruntime.TargetKind is already spelled identically
// to the wire (digest.go's own doc: "crosses the wire unchanged"), so this is
// a membership check rather than a translation.
func targetKindFromWire(s string) (sessionruntime.TargetKind, bool) {
	switch sessionruntime.TargetKind(s) {
	case sessionruntime.TargetMenu, sessionruntime.TargetInput, sessionruntime.TargetWorking, sessionruntime.TargetRegion:
		return sessionruntime.TargetKind(s), true
	default:
		return "", false
	}
}

// --- session.intent ------------------------------------------------------------

func (s *Service) intent(ctx context.Context, p proto.IntentParams) (proto.IntentResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.IntentResult{}, err
	}
	return hs.intent(ctx, p)
}

// intent is session.intent's own decision (spec §6.5): parse the presented
// token, ensure this session has a controller to admit under, submit the
// intent to the owner and render whatever it answers.
//
// Cancelling ctx while the owner is still deciding does NOT cancel the
// intent (D11): the select below stops WAITING, it does not stop the owner —
// the owner's own goroutine keeps running with the done channel it was
// handed (buffered(1), so the eventual send never blocks on a reader that
// left), and the outcome it settles on is recorded exactly as it would be
// for a caller that stayed to hear it, retrievable afterward through
// session.intent-status.
func (hs *hostSession) intent(ctx context.Context, p proto.IntentParams) (proto.IntentResult, error) {
	tok, err := parseToken(p.Token)
	if err != nil {
		return refusedIntent("forged"), nil
	}
	ctrl, err := hs.ensureControl()
	if err != nil {
		return proto.IntentResult{}, err
	}
	kind := intentKindFromWire(p.Kind)
	pi := &pendingIntent{
		Intent: sessionruntime.Intent{
			At:      tok.At,
			Under:   ctrl.Epoch,
			By:      ctrl.Holder,
			Kind:    kind,
			Payload: p.Payload,
		},
		Check:     checkToken(tok),
		Token:     tok,
		Canonical: canonicalIntent{Kind: kind, Payload: p.Payload, AccessEpoch: p.AccessEpoch},
		CommitBy:  p.CommitBy,
	}
	done, err := hs.owner.submit(ownerItem{kind: itemIntent, intent: pi})
	if err != nil {
		return refusedIntent(causeOf(err)), nil
	}
	select {
	case res := <-done:
		return hs.renderIntentResult(res, tok), nil
	case <-ctx.Done():
		return proto.IntentResult{}, ctx.Err()
	}
}

// ensureControl grants this session's one controller principal the first
// time session.intent needs one (see this file's own package doc for why),
// and answers the grant already in force otherwise. Serialised under hs.mu,
// the same lock the write-capability bookkeeping already uses, so two
// concurrent first intents cannot both observe PrincipalNone and both grant
// — a second grant would bump the control epoch again and cancel whatever
// the first grant had just admitted under it.
func (hs *hostSession) ensureControl() (sessionruntime.Control, error) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	ctrl := hs.runtime.Control()
	if ctrl.Holder.Kind != sessionruntime.PrincipalNone {
		return ctrl, nil
	}
	return hs.runtime.GrantControl(sessionruntime.Principal{Kind: sessionruntime.PrincipalAgent, ID: "session.intent"})
}

// intentKindFromWire maps the wire's closed kind set onto sessionruntime's
// own vocabulary. An unrecognised spelling maps to IntentKindNone, which
// sessionruntime's own encode refuses cannot_encode (ErrIntentUnsupported) at
// the commit point — the same "an unrecognised claim must never look
// stronger than it is" rule client.completenessFromWire already states one
// layer up, applied here on the way IN rather than out.
func intentKindFromWire(s string) sessionruntime.IntentKind {
	switch s {
	case "key":
		return sessionruntime.IntentKindKey
	case "text":
		return sessionruntime.IntentKindText
	default:
		return sessionruntime.IntentKindNone
	}
}

// refusedIntent builds a state:"refused" IntentResult carrying no regionNow —
// the shape a refusal reached before a token was ever verified uses (a
// forged or unparseable token names no rows this session could honestly
// render), and the shape submit's own errors (busy, closing) use too, since
// neither is about the screen's content.
func refusedIntent(cause string) proto.IntentResult {
	return proto.IntentResult{State: "refused", Refusal: &proto.IntentRefusal{Cause: cause}}
}

// renderIntentResult turns one settled ownerResult into the wire's own
// IntentResult (spec §6.5): the closed state vocabulary, and — for a
// "refused" state produced by THIS attempt (never a replay, ownerResult's own
// Cause/Replay-by-proxy convention) — the region the token named, as it reads
// now.
func (hs *hostSession) renderIntentResult(res ownerResult, tok Token) proto.IntentResult {
	state := wireIntentState(res)
	out := proto.IntentResult{
		State:        state,
		BytesWritten: res.BytesWritten,
		FenceAfter:   uint64(res.FenceAfter),
	}
	if state == "in_progress" {
		out.RetryAfterMs = retryAfterInProgressMs
		return out
	}
	if state != "refused" {
		return out
	}
	cause := res.Cause
	if cause == "" {
		cause = causeOf(res.Err)
	}
	refusal := &proto.IntentRefusal{Cause: cause}
	if res.Cause != "" {
		// res.Cause is only ever set by resultFromStored (tokens.go): this
		// refusal is a REPLAY of an already-recorded outcome, not the
		// attempt that produced it, so it answers regionOmitted rather than
		// a stale echo of a screen that may have moved on (spec §6.2).
		refusal.RegionOmitted = true
	} else {
		region, truncated := hs.regionNow(tok)
		refusal.RegionNow = region
		refusal.RegionTruncated = truncated
	}
	out.Refusal = refusal
	return out
}

// regionNow renders the rows tok named as they read RIGHT NOW (spec §6.5),
// clipped to regionNowMax. A read failure (the runtime already ended) answers
// an empty region rather than a second error layered onto a refusal already
// being reported — the refusal's own cause is the fact that matters.
func (hs *hostSession) regionNow(tok Token) (text string, truncated bool) {
	if tok.ID == (TokenID{}) {
		return "", false
	}
	frame, _, _, err := hs.readFrame()
	if err != nil {
		return "", false
	}
	first, last := tok.Rows.First, tok.Rows.Last
	if first < 0 {
		first = 0
	}
	if last >= len(frame.Lines) {
		last = len(frame.Lines) - 1
	}
	if last < first {
		return "", false
	}
	var b strings.Builder
	for y := first; y <= last; y++ {
		if y > first {
			b.WriteByte('\n')
		}
		b.WriteString(frame.Text(y))
	}
	return clipRegionNow(b.String())
}

// clipRegionNow bounds s to regionNowMax bytes, trimmed back to a whole rune
// boundary rather than splitting one — a truncated multi-byte grapheme would
// decode as U+FFFD on the far end, showing a caller a character that was
// never on their screen.
func clipRegionNow(s string) (string, bool) {
	if len(s) <= regionNowMax {
		return s, false
	}
	b := []byte(s)[:regionNowMax]
	for len(b) > 0 && !utf8.RuneStart(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return string(b), true
}

// --- session.intent-status ----------------------------------------------------

func (s *Service) intentStatus(p proto.IntentStatusParams) (proto.IntentStatusResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.IntentStatusResult{}, err
	}
	return hs.intentStatus(p.TokenID), nil
}

// intentStatus answers session.intent-status without presenting a payload
// (spec §6.2): unknown, in_progress, or the terminal state a bound intent
// settled at, mirrored into a compact IntentResult with RegionOmitted set —
// a status poll never carries regionNow, because it never re-attempted
// anything a screen read could be paired with.
func (hs *hostSession) intentStatus(tokenID string) proto.IntentStatusResult {
	id, err := decodeTokenID(tokenID)
	if err != nil {
		return proto.IntentStatusResult{State: "unknown"}
	}
	state, r := hs.tokens.Status(id)
	switch state {
	case "in_progress":
		return proto.IntentStatusResult{State: "in_progress"}
	case "recorded":
		// r.State is already the exact wire spelling Record stored
		// (wireIntentState, tokens.go); decoding it back through
		// ownerStateFromName and an empty Token{} just to hand
		// renderIntentResult a state it re-derives to the same string keeps
		// ONE function reading that spelling rather than two. r.Cause,
		// carried through as-is, is what tells renderIntentResult this is a
		// replay (never a fresh attempt) — a status poll never re-reads the
		// screen, so a refused record always answers regionOmitted rather
		// than a fresh regionNow (spec §6.2).
		res := ownerResult{
			State:        ownerStateFromName(r.State),
			BytesWritten: r.BytesWritten,
			FenceAfter:   sessionruntime.Fence(r.FenceAfter),
			Cause:        r.Cause,
		}
		result := hs.renderIntentResult(res, Token{})
		return proto.IntentStatusResult{State: r.State, Result: &result}
	default:
		return proto.IntentStatusResult{State: "unknown"}
	}
}

// decodeTokenID parses the wire's 32-hex-character tokenId back into a
// TokenID. A malformed one can never have been minted, so it is read the
// same as "unknown" rather than as a request error — session.intent-status
// carries no way to refuse a request, only an answer.
func decodeTokenID(s string) (TokenID, error) {
	var id TokenID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return TokenID{}, fmt.Errorf("session: bad token id %q", s)
	}
	copy(id[:], b)
	return id, nil
}

// --- session.access-bump -------------------------------------------------------

func (s *Service) accessBump(p proto.AccessBumpParams) (proto.AccessBumpResult, error) {
	hs, err := s.find(p.Session)
	if err != nil {
		return proto.AccessBumpResult{}, err
	}
	epoch, err := hs.accessBump(p.Above)
	if err != nil {
		return proto.AccessBumpResult{}, err
	}
	return proto.AccessBumpResult{Epoch: epoch}, nil
}

// accessBump submits an itemAccessBump to the owner and waits for it: the
// owner answers only once every older uncommitted intent this session held
// is terminal (access.go's applyAccessBump), which is what makes THIS
// return the moment spec §7.2's revocation may trust the new epoch.
func (hs *hostSession) accessBump(above uint64) (uint64, error) {
	done, err := hs.owner.submit(ownerItem{kind: itemAccessBump, above: above})
	if err != nil {
		return 0, err
	}
	res := <-done
	if res.Err != nil {
		return 0, res.Err
	}
	return res.Epoch, nil
}

// --- the wire token: Token (tokens.go) marshalled opaque and self-describing --

// wireToken is Token's own wire shape (nocx-6q1uh.6): every field JSON-tagged
// and fixed-width binary fields hex-encoded, then the whole thing base64-
// encoded into the one opaque string session.target answers and
// session.intent presents back. It carries Token.Identity.At through the
// SAME AtSession/AtGeneration pair as Token.At: at mint time
// (tokenBook.Mint) both are read from this session's CURRENT incarnation —
// Token.At from the book's own field, Token.Identity.At from the snapshot's
// screenStateLocked — so they are always the same value in the one
// production path that ever builds a Token, and reusing one wire pair for
// both is exact rather than an approximation.
type wireToken struct {
	ID             string `json:"id"`
	Generation     string `json:"generation"`
	Session        string `json:"session"`
	AtSession      string `json:"atSession"`
	AtGeneration   uint64 `json:"atGeneration"`
	AltScreen      bool   `json:"altScreen"`
	BufferInstance uint64 `json:"bufferInstance"`
	GeomCols       int    `json:"geomCols"`
	GeomRows       int    `json:"geomRows"`
	Kind           string `json:"kind"`
	First          int    `json:"first"`
	Last           int    `json:"last"`
	Digest         string `json:"digest"`
	IncludeCursor  bool   `json:"includeCursor"`
	AccessEpoch    uint64 `json:"accessEpoch"`
	MintedAt       int64  `json:"mintedAt"`
	ExpiresAt      int64  `json:"expiresAt"`
	MAC            string `json:"mac"`
}

// marshalToken renders t as the one opaque string session.target answers.
// The signature (t.MAC) already makes tampering detectable at Verify
// (tokens.go); this encoding only needs to be lossless, not itself secret.
func marshalToken(t Token) (string, error) {
	w := wireToken{
		ID:             hex.EncodeToString(t.ID[:]),
		Generation:     string(t.Session.Generation),
		Session:        t.Session.Session,
		AtSession:      string(t.At.Session),
		AtGeneration:   uint64(t.At.Generation),
		AltScreen:      t.Identity.AltScreen,
		BufferInstance: t.Identity.BufferInstance,
		GeomCols:       t.Identity.Cols,
		GeomRows:       t.Identity.Rows,
		Kind:           string(t.Kind),
		First:          t.Rows.First,
		Last:           t.Rows.Last,
		Digest:         hex.EncodeToString(t.Digest[:]),
		IncludeCursor:  t.IncludeCursor,
		AccessEpoch:    t.AccessEpoch,
		MintedAt:       t.MintedAt.UnixNano(),
		ExpiresAt:      t.ExpiresAt.UnixNano(),
		MAC:            hex.EncodeToString(t.MAC[:]),
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// parseToken reverses marshalToken. Every failure — bad base64, bad JSON, a
// hex field of the wrong length — is reported as ErrForged: a token this
// malformed could not have come from a real Mint, which is the same fact
// ErrForged already names for a signature that does not match.
func parseToken(s string) (Token, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %v", ErrForged, err)
	}
	var w wireToken
	if err = json.Unmarshal(raw, &w); err != nil {
		return Token{}, fmt.Errorf("%w: %v", ErrForged, err)
	}
	id, err := decodeFixed16(w.ID)
	if err != nil {
		return Token{}, fmt.Errorf("%w: id: %v", ErrForged, err)
	}
	digest, err := decodeFixed32(w.Digest)
	if err != nil {
		return Token{}, fmt.Errorf("%w: digest: %v", ErrForged, err)
	}
	mac, err := decodeFixed32(w.MAC)
	if err != nil {
		return Token{}, fmt.Errorf("%w: mac: %v", ErrForged, err)
	}
	at := sessionruntime.Incarnation{
		Session:    sessionruntime.SessionID(w.AtSession),
		Generation: sessionruntime.Generation(w.AtGeneration),
	}
	return Token{
		ID:      id,
		Session: proto.HostSessionID{Generation: proto.GenerationID(w.Generation), Session: w.Session},
		At:      at,
		Identity: sessionruntime.ScreenIdentity{
			At:             at,
			AltScreen:      w.AltScreen,
			BufferInstance: w.BufferInstance,
			Cols:           w.GeomCols,
			Rows:           w.GeomRows,
		},
		Kind:          sessionruntime.TargetKind(w.Kind),
		Rows:          sessionruntime.RowRange{First: w.First, Last: w.Last},
		Digest:        digest,
		IncludeCursor: w.IncludeCursor,
		AccessEpoch:   w.AccessEpoch,
		MintedAt:      time.Unix(0, w.MintedAt),
		ExpiresAt:     time.Unix(0, w.ExpiresAt),
		MAC:           mac,
	}, nil
}

func decodeFixed16(s string) ([16]byte, error) {
	var out [16]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(b) != len(out) {
		return out, fmt.Errorf("want %d bytes, got %d", len(out), len(b))
	}
	copy(out[:], b)
	return out, nil
}

func decodeFixed32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(b) != len(out) {
		return out, fmt.Errorf("want %d bytes, got %d", len(out), len(b))
	}
	copy(out[:], b)
	return out, nil
}
