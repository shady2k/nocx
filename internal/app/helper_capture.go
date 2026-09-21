package app

// The coordinator's capture storage (nocx-2v80t.2.2): what answers a
// helper's `session.capture` ask. The record was built by the helper's
// session runtime at the authenticated render boundary; this side stores it
// against the command's entry — through the content ledger's existing
// CaptureOutput, the ONE write path for what a command printed — and
// answers the store's own verdict: kept, or the reason nothing was kept.
// REFUSING TO STORE IS NOT AN ERROR: the result carries the refusal as
// data, exactly as the store's stance names it, because a helper that built
// a record must be able to say what became of it.
//
// # The fence→entry binding
//
// The record names no entry; it names the fence nonce whose rendezvous
// settled. The pairing is recorded by the transport's lifecycle projection
// (ws_lifecycle.go's CaptureBindings): the completed fact is the only
// moment both identities are named together, and the entry the projection
// closes is keyed by the kernel's own attempt id — the identity question is
// settled THERE, by the kernel's resolution of the completion, never by a
// string the wire carried. This file only reads that memory.
//
// # The artifact id is the fence's own name
//
// captureArtifactID derives the store's idempotency key from the nonce, so
// a retried ask (an ack lost after a successful store) is the store's own
// replay no-op rather than a second body of the same command. The layout is
// the artifact-id vocabulary's UUID shape with the fence's bytes in it; the
// time a command ran lives in its entry, never in this id.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
	nocxlog "github.com/shady2k/nocx/internal/log"
)

// captureReasonNoEntry is the result's reason for "nothing to store
// against": history was off (the row was never recorded), the bind never
// landed, or this machine runs without durable history at all. The other
// three reasons the result's contract names are the store's own stances and
// are mapped from them, never spelled here.
const captureReasonNoEntry = "noEntry"

// captureBindingsBound is how many fences the memory keeps. A capture ask
// is bounded by the helper's own one-ask timeout, so a few fences would do;
// the bound exists so a coordinator that never gets asked still cannot grow
// the map without end, and eviction runs oldest first.
const captureBindingsBound = 256

// captureBindings is the fence→entry memory the transport's completed-fact
// projection writes and this side's handler reads. Safe for concurrent use:
// the projection writes inside its emission turn, the reverse handler reads
// on the client's answer goroutine.
type captureBindings struct {
	mu        sync.Mutex
	entries   map[string]string
	order     []string
	open      map[string]string
	openOrder []string
}

func newCaptureBindings() *captureBindings {
	return &captureBindings{
		entries: make(map[string]string),
		open:    make(map[string]string),
	}
}

// Bind remembers which entry one settled fence belongs to. A nonce bound
// again is the same fact binding to the same value — the kernel sets an
// exit status exactly once, so one fence completes one attempt — and the
// value is overwritten, never duplicated.
func (b *captureBindings) Bind(nonce, entryID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.entries[nonce]; !exists {
		if len(b.order) >= captureBindingsBound {
			delete(b.entries, b.order[0])
			b.order = b.order[1:]
		}
		b.order = append(b.order, nonce)
	}
	b.entries[nonce] = entryID
}

// entryFor answers the entry one fence is bound to, or false — which is the
// handler's cue for noEntry, never an error.
func (b *captureBindings) entryFor(nonce string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.entries[nonce]
	return id, ok
}

func (b *captureBindings) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// BindOpen remembers which OPEN attempt's entry a session is running —
// the memory recordAttemptEntry feeds and the unfinished capture ask
// resolves against (nocx-2v80t.2.4). The entry already exists, keyed by
// the attempt id; nothing is invented here. A session's next attempt
// replaces the binding: one open attempt per session.
func (b *captureBindings) BindOpen(sessionID, entryID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.open[sessionID]; !exists {
		if len(b.openOrder) >= captureBindingsBound {
			delete(b.open, b.openOrder[0])
			b.openOrder = b.openOrder[1:]
		}
		b.openOrder = append(b.openOrder, sessionID)
	}
	b.open[sessionID] = entryID
}

// UnbindOpen removes a session's open binding. The authenticated boundary
// calls it: from the completion on, the entry is addressed by the fence's
// own memory, and a finished entry must never catch a stray unfinished ask.
func (b *captureBindings) UnbindOpen(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.open, sessionID)
	for i, s := range b.openOrder {
		if s == sessionID {
			b.openOrder = append(b.openOrder[:i:i], b.openOrder[i+1:]...)
			break
		}
	}
}

// openEntryFor answers the entry of the attempt a session still has open,
// or false — the unfinished ask's cue for noEntry, never an error.
func (b *captureBindings) openEntryFor(sessionID string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.open[sessionID]
	return id, ok
}

// openSize is how many sessions the open memory answers for.
func (b *captureBindings) openSize() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.open)
}

// captureSink is the coordinator's answer to a helper's settled record: the
// ledger it stores through and the fence→entry memory it resolves against.
//
// The ledger arrives through set rather than the constructor because of the
// composition root's own order: the reverse registry is built before the
// content store is (the same reason helperPrompt is a holder), and a
// capture answered by a stub ledger would answer kept while storing
// nothing — the one lie this seam must never tell. An unwired sink answers
// noEntry, which is true: no row was ever recorded on this machine.
type captureSink struct {
	mu     sync.RWMutex
	ledger content.LedgerRepository
	binds  *captureBindings
}

func newCaptureSink() *captureSink {
	return &captureSink{binds: newCaptureBindings()}
}

// set records the ledger once the content store exists. It is the
// composition root's one call, beside the store it names.
func (s *captureSink) set(ledger content.LedgerRepository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ledger = ledger
}

func (s *captureSink) ledgerOf() content.LedgerRepository {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ledger
}

// capture answers one session.capture ask. Every outcome that is an ANSWER
// returns a result and a nil error; an error means this coordinator could
// not decide at all — a body the store refused for a reason neither the
// store's stances nor this handler names — and the helper reports it and
// drops the record, which is a gap history can name.
func (s *captureSink) capture(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.CaptureParams
	if err := decodeReverseParams(raw, &p); err != nil {
		return nil, err
	}
	// The state is the address: a settled record is bound by the fence the
	// completion authenticated; an unfinished record is bound by the entry
	// recordAttemptEntry already opened for the attempt (nocx-2v80t.2.4) —
	// the identity was never missing, only the fence.
	var entryID, artifactID string
	switch p.State {
	case proto.CaptureSettled:
		if !isFenceNonce(p.Nonce) {
			return nil, badReverseParams("capture: nonce must be 64 lowercase hex characters")
		}
		id, ok := s.binds.entryFor(p.Nonce)
		if !ok {
			return proto.CaptureResult{Kept: false, Reason: captureReasonNoEntry}, nil
		}
		entryID, artifactID = id, captureArtifactID(p.Nonce)
	case proto.CaptureUnfinished:
		// No fence may ride an unfinished ask: carrying one would claim an
		// authenticated boundary that has not happened.
		if p.Nonce != "" {
			return nil, badReverseParams("capture: an unfinished record names no fence")
		}
		id, ok := s.binds.openEntryFor(p.Session.Session)
		if !ok {
			return proto.CaptureResult{Kept: false, Reason: captureReasonNoEntry}, nil
		}
		entryID, artifactID = id, unfinishedArtifactID(id)
	default:
		return nil, badReverseParams("capture: unknown record state")
	}
	ledger := s.ledgerOf()
	if ledger == nil {
		// Unreachable while the binds are fed: the transport writes them
		// through the same store this setter names, so a bound nonce
		// implies a wired ledger. Stated rather than assumed — the
		// unwired answer is the same noEntry either way.
		return proto.CaptureResult{Kept: false, Reason: captureReasonNoEntry}, nil
	}
	// The geometry names the screen the record's reads were taken against:
	// the boundary screen for a settled record, the opening for an
	// unfinished one, which has no boundary screen at all.
	screen := p.Closing
	if p.State == proto.CaptureUnfinished {
		screen = p.Opening
	}
	cols, rows := screen.Cols, screen.Rows
	stance, err := ledger.CaptureOutput(ctx, content.CaptureOutput{
		EntryID:        entryID,
		ArtifactID:     artifactID,
		MediaType:      content.MediaJSON,
		CaptureMethod:  content.CaptureTerminalCells,
		CaptureVersion: 1,
		TerminalCols:   &cols,
		TerminalRows:   &rows,
		Seq:            1,
		Body:           []byte(raw),
	})
	if errors.Is(err, content.ErrNoSuchEntry) {
		// The bind named a row the store does not hold — history was
		// turned off between the bind and the ask, or the row's write
		// was suppressed. The contract's word for it is noEntry.
		return proto.CaptureResult{Kept: false, Reason: captureReasonNoEntry}, nil
	}
	if err != nil {
		nocxlog.From(ctx).Warn("helper capture: the record could not be stored",
			"entry", entryID, "nonce", p.Nonce, "error", err)
		return nil, err
	}
	result, mapped := captureResultOf(stance)
	if !mapped {
		nocxlog.From(ctx).Warn("helper capture: the store answered an unknown stance",
			"stance", string(stance), "entry", entryID)
		return nil, fmt.Errorf("capture: the store answered %q", stance)
	}
	return result, nil
}

// captureResultOf maps the store's stance onto the result's wire vocabulary.
// The mapping is total over the stances the store answers; an unmapped
// stance is a decision this code has not made, reported rather than guessed.
func captureResultOf(stance content.SessionOutputStance) (proto.CaptureResult, bool) {
	switch stance {
	case content.SessionOutputKept:
		return proto.CaptureResult{Kept: true}, true
	case content.SessionOutputRetentionOff:
		return proto.CaptureResult{Kept: false, Reason: "outputOff"}, true
	case content.SessionOutputSensitive:
		return proto.CaptureResult{Kept: false, Reason: "sensitive"}, true
	case content.SessionOutputCritical:
		return proto.CaptureResult{Kept: false, Reason: "critical"}, true
	}
	return proto.CaptureResult{}, false
}

// isFenceNonce is the fence shape every op on this service spells: 64
// lowercase hex characters. The kernel authenticated the nonce at the
// completion; this check only refuses what cannot be a fence at all.
func isFenceNonce(nonce string) bool {
	if len(nonce) != 64 {
		return false
	}
	for i := 0; i < len(nonce); i++ {
		c := nonce[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// captureArtifactID derives the artifact id from the fence nonce: the
// nonce's own bytes in the store's artifact-id shape, with the UUID version
// and variant bits set. Deterministic, because the id is the store's
// idempotency key — an ack lost after a successful store makes the retried
// ask the store's own replay no-op, and the body of one command is stored
// exactly once whatever the wire does.
// unfinishedArtifactID derives the artifact id of an open interval's
// record from the ENTRY'S own name: the entry already exists — the one
// identity the open interval has — and the id must be deterministic, so a
// retried ask is the store's replay no-op exactly as it is for a fence's
// id. The digest is stamped into the artifact-id vocabulary's UUID shape
// (version 4, because the bytes are a digest and not a timestamp).
func unfinishedArtifactID(entryID string) string {
	sum := sha256.Sum256([]byte(entryID))
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

func captureArtifactID(nonce string) string {
	raw, err := hex.DecodeString(nonce)
	if err != nil || len(raw) < 16 {
		// Unreachable behind isFenceNonce; a derive that cannot run must
		// not invent a random id — the caller's own refusal is the honest
		// answer, so return a name that cannot be mistaken for a key.
		return ""
	}
	var u [16]byte
	copy(u[:], raw[:16])
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
