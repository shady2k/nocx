// The pending-capture registry (the security-critical half of the secrets
// redesign): the backend-side holder of a submitted credential between the
// command running and the user deciding whether to save it.
//
// CONTRACT — pasted from the round's brief, and every clause is enforced
// here or at the transport seam that owns the triggers:
//
//	A submitted credential awaiting a save decision is held only in
//	backend process memory as a single-use pending capture; the renderer
//	receives only an opaque capture id and non-secret display metadata. A
//	pending capture is scoped to the originating tab, session and
//	submitted history-entry id. It is destroyed immediately on save,
//	dismissal, tab or session closure, vault seal or app lock, transport
//	disconnect, application shutdown, or history-record failure.
//
//	Saving consumes the capture exactly once through an idempotent
//	operation. Destruction leaves the already-written masked history entry
//	unchanged. A save that races with the lock either acquires the vault
//	operation before the lock barrier and completes, or fails without
//	creating a secret; the outcome must never depend on renderer timing.
//	Pending plaintext must not enter DOM state, logs, telemetry, crash
//	metadata, JSON responses or durable storage.
//
// Two clauses were in the brief and are deliberately NOT here, both removed
// after the first round of real use (the owner's decision, 2026-08-03).
//
// There is no expiry timer. It bounded how long one credential sits in this
// process's memory — while the same command sits in cleartext in the
// shell's own history file on disk, which this product has decided not to
// treat as a threat. What it bought in exchange was an offer that retired
// itself while the user was still reading the output that came with it.
//
// And a superseding submission no longer destroys anything. Deciding about
// a key is rarely the next thing you do — you run one more command to check
// something, and under that rule the offer was gone for good. Several
// unsaved captures can now be pending at once, one per block that has an
// unanswered offer, and each lives until it is saved, dismissed, or one of
// the real events above takes it. The cost is one credential in memory per
// unanswered offer for the life of the tab, which is the trade this product
// has already made everywhere else.
//
// Neither Go nor JS can promise physical erasure: a copied byte can
// outlive the heap it came from, and the operating system owns the page
// cache. What this registry provides is lifetime minimisation, not
// zeroisation — every clause above is about when the plaintext stops being
// reachable, never about whether the memory was overwritten.
package credentialcapture

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
)

// ErrCaptureUnknown is returned when a save/dismiss addresses a capture
// that is not pending or settled: destroyed, or never minted.
var ErrCaptureUnknown = errors.New("capture: unknown")

// ErrCaptureConsumed is returned when a save addresses a dismissed capture
// — the single-use token was spent by the dismissal.
var ErrCaptureConsumed = errors.New("capture: already dismissed")

// ErrCaptureSaveFailed wraps the failure of a settled save: the capture was
// consumed by the attempt (nothing was created), and a retry reports the
// same outcome rather than running the vault again.
var ErrCaptureSaveFailed = errors.New("capture: save failed")

// CaptureID is the opaque, unguessable handle the renderer holds for a
// capture. It is a capability token: holding it is the only way to save or
// dismiss the capture, and it carries no secret material.
type CaptureID string

// CaptureScope is where a capture came from — the identity facts that decide
// which destruction event owns it. Connection is the backend's own
// per-connection id (the transport assigns it; it never crosses the wire).
// Tab is the renderer-minted per-tab identity that rides history.record
// (nocx-tsajw — the one wire exception to "session-local ids never cross",
// and the handle tab closure destroys by). Destruction is keyed on the PAIR:
// a tab id is opaque and minted per renderer, so it is not an authorization
// boundary on its own — a tab's captures die only to its own connection.
// SessionIDs are the terminal sessions the tab held at record time
// (informational: a tab can hold several sessions, and ambiguous ownership
// falls back rather than guessing); EntryID is the store row the capture
// first attached to ("" when no row was written); Generation is the pane's
// submission counter, which is what makes "the next submission from that
// pane" a fact instead of a guess.
type CaptureScope struct {
	Connection string
	Pane       string
	SessionIDs []string
	EntryID    string
	Generation uint64
}

// CaptureLink is one masked history row attached to a capture: its stable
// id and the redaction segment that becomes the vault reference on save.
// One save repairs every linked row.
type CaptureLink struct {
	EntryID   string
	Redaction content.Redaction
}

// PendingCredential is one detected credential in one submission, ready to
// be captured. Value is the plaintext (the transport slices it from the
// submitted line and drops it after the call); SuggestedName is the
// backend-derived vault name; Redaction is the segment the row keeps.
type PendingCredential struct {
	Value         []byte
	SuggestedName string
	Redaction     content.Redaction
}

// RegisterOutcome says what one credential's submission did.
type RegisterOutcome int

const (
	// OutcomeCaptured: a new pending capture was minted; the offer opens.
	OutcomeCaptured RegisterOutcome = iota
	// OutcomeLinked: the same fingerprint is already pending; the new entry
	// attached to the existing capture — one save repairs both rows, and no
	// second offer opens.
	OutcomeLinked
	// OutcomeSaved: the same fingerprint was saved earlier this session;
	// the row stores the existing reference automatically and nothing is
	// offered.
	OutcomeSaved
	// OutcomeSuppressed: the same fingerprint was dismissed this session;
	// nothing is offered (and the row stays masked).
	OutcomeSuppressed
)

// RegisterResult is the per-credential answer to one submission.
type RegisterResult struct {
	Outcome       RegisterOutcome
	CaptureID     CaptureID
	SavedName     string // OutcomeSaved: the reference name to store
	SuggestedName string // display metadata for the offer
	Redaction     content.Redaction
}

// SaveHandle is a reserved capture: the caller runs the vault create and
// the history rewrites with Value and Links, then calls Complete. A handle
// with Completed is an idempotent retry: the save already settled, Name is
// the name that was used, and RewritePending means the history rewrites are
// still owed (the vault secret exists; the row rewrite failed or was never
// run) — the caller re-runs only those.
type SaveHandle struct {
	CaptureID     CaptureID
	Value         credential.Secret
	SuggestedName string
	Links         []CaptureLink

	Completed      bool
	Name           string
	SecretID       credential.SecretID
	RewritePending bool
}

type captureState int

const (
	statePending captureState = iota
	stateSaving
	stateSaved
	stateFailed
	stateDismissed
)

// capture is one pending credential. The value lives only while the state
// is pending or saving; every settlement releases it.
type capture struct {
	id            CaptureID
	fingerprint   string
	value         credential.Secret
	suggestedName string
	scope         CaptureScope
	links         []CaptureLink
	state         captureState

	// done closes when the capture leaves pending: the settlement (saved,
	// failed, dismissed) or the destruction. A concurrent Reserve waits on
	// it instead of racing the settle.
	done chan struct{}

	// Outcome (stateSaved / stateFailed), recorded before done closes so a
	// waiter reading them after the wait sees a settled value.
	name           string
	secretID       credential.SecretID
	rewritePending bool
	saveErr        error
}

// CaptureRegistry is the in-process store of pending captures.
//
// The fingerprint key is minted per registry (per process), held only in
// memory, and never leaves it: an HMAC keyed with a per-process secret is
// not a durable password oracle, and a fingerprint computed under a key
// that dies with the process cannot outlive the session it was computed
// for. The three suppression rules (pending, saved, dismissed) are
// therefore bounded by the application session, exactly as the brief
// scopes dismissal: "suppressed for the rest of the application session,
// not forever".
type CaptureRegistry struct {
	mu   sync.Mutex
	key  []byte
	byID map[CaptureID]*capture
	// byPending maps a fingerprint to its ONE pending capture, so a
	// re-submission links instead of minting a second offer.
	byPending map[string]*capture
	// saved maps a fingerprint to the name its value was saved under this
	// session; dismissed is the session's negative set. Both are equality
	// facts only — the fingerprint never crosses to the renderer, never
	// appears in a log or a contract, and is not the vault id.
	saved     map[string]string
	dismissed map[string]struct{}
}

// NewCaptureRegistry builds an empty registry with a fresh per-process
// fingerprint key. There is no clock and no lifetime: a capture lives until
// it is settled or one of the destruction events takes it (see the contract
// at the top of this file).
func NewCaptureRegistry() (*CaptureRegistry, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("capture registry: fingerprint key: %w", err)
	}
	return &CaptureRegistry{
		key:       key,
		byID:      make(map[CaptureID]*capture),
		byPending: make(map[string]*capture),
		saved:     make(map[string]string),
		dismissed: make(map[string]struct{}),
	}, nil
}

// Fingerprint returns the equality token for a credential value: HMAC(app
// key, value). Keyed, never a bare digest — an unkeyed hash of
// password123 is a durable password oracle. Used only for equality.
func (r *CaptureRegistry) Fingerprint(value []byte) string {
	mac := hmac.New(sha256.New, r.key)
	_, _ = mac.Write(value)
	return hex.EncodeToString(mac.Sum(nil))
}

// SavedName returns the name a fingerprint was saved under this session,
// and whether it was. The record seam calls this BEFORE writing the row so
// it can store the existing reference automatically.
func (r *CaptureRegistry) SavedName(fingerprint string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.saved[fingerprint]
	return name, ok
}

// Submit processes one command submission from a tab. In one atomic step
// it:
//
//  1. Links same-fingerprint findings to the existing pending capture (no
//     second offer; one save repairs every linked masked row).
//  2. Applies the session's saved/dismissed suppression.
//  3. Mints new captures for everything else.
//
// A new submission does NOT destroy the tab's older pending captures: the
// supersede rule was removed by the owner's decision (file header) — several
// unsaved captures coexist, one per unanswered offer, each living until it
// is settled or a real destruction event takes it.
//
// The results are parallel to creds.
func (r *CaptureRegistry) Submit(scope CaptureScope, creds []PendingCredential) []RegisterResult {
	r.mu.Lock()
	defer r.mu.Unlock()

	results := make([]RegisterResult, 0, len(creds))
	for _, cred := range creds {
		fp := r.fingerprintLocked(cred.Value)
		if pending, ok := r.byPending[fp]; ok && pending.state == statePending {
			pending.links = append(pending.links, CaptureLink{
				EntryID:   scope.EntryID,
				Redaction: cred.Redaction,
			})
			results = append(results, RegisterResult{
				Outcome:       OutcomeLinked,
				CaptureID:     pending.id,
				SuggestedName: pending.suggestedName,
				Redaction:     cred.Redaction,
			})
			continue
		}
		if name, ok := r.saved[fp]; ok {
			results = append(results, RegisterResult{
				Outcome:   OutcomeSaved,
				SavedName: name,
				Redaction: cred.Redaction,
			})
			continue
		}
		if _, ok := r.dismissed[fp]; ok {
			results = append(results, RegisterResult{
				Outcome:   OutcomeSuppressed,
				Redaction: cred.Redaction,
			})
			continue
		}
		id, err := newCaptureID()
		if err != nil {
			// No entropy: fail this submission's captures closed — the
			// row stays masked and nothing is offered. Minting a
			// predictable id would hand the renderer a token it could
			// guess.
			results = append(results, RegisterResult{Outcome: OutcomeSuppressed, Redaction: cred.Redaction})
			continue
		}
		c := &capture{
			id:            id,
			fingerprint:   fp,
			value:         credential.NewSecretBytes(cred.Value),
			suggestedName: cred.SuggestedName,
			scope:         scope,
			links:         []CaptureLink{{EntryID: scope.EntryID, Redaction: cred.Redaction}},
			state:         statePending,
			done:          make(chan struct{}),
		}
		r.byID[id] = c
		r.byPending[fp] = c
		results = append(results, RegisterResult{
			Outcome:       OutcomeCaptured,
			CaptureID:     id,
			SuggestedName: cred.SuggestedName,
			Redaction:     cred.Redaction,
		})
	}
	return results
}

// Reserve claims a capture for saving. A pending capture becomes saving
// (single-use) and the handle carries the value, the suggested name and
// every linked row. A save that already settled returns the recorded
// outcome (idempotent retry — the caller re-runs only the owed rewrites);
// a save in flight blocks until it settles, so two concurrent saves cannot
// mint two secrets. A dismissed capture is consumed; a destroyed one is
// unknown.
func (r *CaptureRegistry) Reserve(id CaptureID) (SaveHandle, error) {
	for {
		r.mu.Lock()
		c, ok := r.byID[id]
		if !ok {
			r.mu.Unlock()
			return SaveHandle{}, ErrCaptureUnknown
		}
		switch c.state {
		case statePending:
			c.state = stateSaving
			h := SaveHandle{
				CaptureID:     id,
				Value:         c.value,
				SuggestedName: c.suggestedName,
				Links:         c.links,
			}
			r.mu.Unlock()
			return h, nil
		case stateSaving:
			done := c.done
			r.mu.Unlock()
			<-done
			// Loop: re-read the settled state.
		case stateSaved:
			h := SaveHandle{
				CaptureID:      id,
				Completed:      true,
				Name:           c.name,
				SecretID:       c.secretID,
				RewritePending: c.rewritePending,
			}
			r.mu.Unlock()
			return h, nil
		case stateFailed:
			err := c.saveErr
			r.mu.Unlock()
			return SaveHandle{}, fmt.Errorf("%w: %v", ErrCaptureSaveFailed, err)
		case stateDismissed:
			r.mu.Unlock()
			return SaveHandle{}, ErrCaptureConsumed
		}
	}
}

// Complete settles a reserved save: name and secretID on success — with
// rewritePending when the history rewrites are still owed (the vault secret
// exists, the row rewrite failed or was never run, and a retry must redo
// only that) — or saveErr on failure. Either way the plaintext is released:
// the capture never holds the value after the save settles.
func (r *CaptureRegistry) Complete(id CaptureID, name string, secretID credential.SecretID, rewritePending bool, saveErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	if !ok {
		return
	}
	// A retried rewrite settles a save that already settled: the vault was
	// never re-run, so the name cannot change — only the owed-rewrite flag
	// advances, and done was already closed by the first settle.
	if c.state == stateSaved {
		if saveErr == nil {
			c.rewritePending = rewritePending
		}
		return
	}
	c.value = credential.Secret{} // release the plaintext reference
	if saveErr != nil {
		c.state = stateFailed
		c.saveErr = saveErr
	} else {
		c.state = stateSaved
		c.name = name
		c.secretID = secretID
		c.rewritePending = rewritePending
		r.saved[c.fingerprint] = name
	}
	delete(r.byPending, c.fingerprint)
	close(c.done)
}

// Dismiss destroys a pending capture and marks its fingerprint dismissed
// for the rest of the application session — never forever: durably
// tracking a negative decision about a secret the user declined to store
// would outlive the context the decision was made in. Saving a dismissed
// capture is ErrCaptureConsumed. A second dismiss of the same capture is a
// no-op (idempotent — the renderer may double-dismiss), which is why a
// dismissed capture keeps a tombstone in byID instead of being removed.
func (r *CaptureRegistry) Dismiss(id CaptureID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	if !ok {
		return ErrCaptureUnknown
	}
	switch c.state {
	case statePending:
		r.dismissed[c.fingerprint] = struct{}{}
		c.state = stateDismissed
		delete(r.byPending, c.fingerprint)
		c.value = credential.Secret{}
		close(c.done)
		return nil
	case stateDismissed:
		return nil // idempotent
	default:
		// Saving or settled: the offer is already gone; a dismiss must not
		// undo a save's outcome.
		return nil
	}
}

// DestroyPane destroys every pending capture originating from a pane: pane
// closure (the renderer's secrets.paneClosed notification) and history-record failure.
// The connection is part of the key: the pane identity is renderer-minted and
// opaque, so a pane id from one connection must never destroy another
// connection's captures. A capture whose save is in flight is left to settle
// — its outcome is already decided and a seal or disconnect must not make it
// create a secret it was entitled to.
func (r *CaptureRegistry) DestroyPane(conn, pane string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.byID {
		if c.scope.Connection == conn && c.scope.Pane == pane && c.state == statePending {
			r.destroyLocked(c)
		}
	}
}

// DestroyConnection destroys every pending capture from one connection:
// transport disconnect. One WebSocket carries every pane in a window, so the
// connection's death takes all of them — the per-pane destroy (DestroyPane)
// cannot express "the connection is gone", and a disconnect is the one
// destruction event that is genuinely connection-scoped. Settling saves are
// left to finish, exactly as DestroyPane leaves them.
func (r *CaptureRegistry) DestroyConnection(conn string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.byID {
		if c.scope.Connection == conn && c.state == statePending {
			r.destroyLocked(c)
		}
	}
}

// DestroyAll destroys every pending capture: vault seal or app lock,
// application shutdown. Settling saves are left to finish.
func (r *CaptureRegistry) DestroyAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.byID {
		if c.state == statePending {
			r.destroyLocked(c)
		}
	}
}

// destroyLocked removes a pending capture entirely: the id is spent, the
// value is released, waiters wake. The already-written masked history entry
// is untouched — destruction never rewrites a row.
func (r *CaptureRegistry) destroyLocked(c *capture) {
	delete(r.byID, c.id)
	if r.byPending[c.fingerprint] == c {
		delete(r.byPending, c.fingerprint)
	}
	c.value = credential.Secret{}
	close(c.done)
}

func (r *CaptureRegistry) fingerprintLocked(value []byte) string {
	return r.Fingerprint(value)
}

func newCaptureID() (CaptureID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return CaptureID("cap_" + hex.EncodeToString(b[:])), nil
}
