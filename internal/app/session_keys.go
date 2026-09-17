package app

// session.keys — one key, one text atom, or an option, spent under a
// target a prior session.read minted (design §4.2, §6.4, §6.5, Task 9).
//
// Mirrors Task 8's layering: assistant.PaneKeys is the interface the
// executor consumes (internal/assistant/execute_session_keys.go); paneKeys
// here is its one production implementation, wrapping the SAME paneReader
// session.read already builds (its own record of each mint, design §6.2)
// and the SAME authority hub Task 7 built — never a second, independent
// answer to "which helper holds this pane" or "does this chain still hold".

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/agentdriver"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/workers"
)

// commitWindow is how far ahead of "now" every intent's own commitBy is set
// (design §7.2): 5s after the coordinator sends it, on the shared monotonic
// clock, so a helper that never acknowledges an access-epoch bump is waited
// out by the LATEST commitBy this coordinator sent that session, never by a
// duration a test would have to fake elapsing.
const commitWindow = 5 * time.Second

// optionSettle bounds how long the option loop (design §6.4) waits, after
// sending one Up/Down, for a snapshot whose selection moved by exactly one.
const optionSettle = 2 * time.Second

// optionPollInterval is how often the option loop re-reads while waiting for
// a selection to settle. It is a scheduling yield bounded by optionSettle,
// never itself the exit condition — the loop's own exit is always "the
// selection moved by exactly one" or optionSettle passing.
const optionPollInterval = 20 * time.Millisecond

// paneKeysReader is what PaneKeys needs from the session.read path: minting
// a fresh target for the option loop's own re-reads (Read, exactly
// assistant.PaneReader's own method) and this coordinator's record of a
// PRIOR mint (Record, paneReader's own bookkeeping, design §6.2, Task 8).
// *paneReader (session_targets.go) satisfies both, so PaneKeys needs no
// second reader of its own — the same reasoning paneReader's own doc gives
// for reusing hub.lookup rather than a second helper lookup.
type paneKeysReader interface {
	assistant.PaneReader
	Record(tokenID string) (targetRecord, bool)
}

// menuReader is the optional half a paneKeysReader may implement — *paneReader
// does (session_targets.go's Menu) — to answer the agent rule's own reading of
// a MENU off a frame it already holds, WITHOUT minting anything.
//
// The option loop's settle wait (awaitSelectionMove, design §6.4 step 4) is a
// probe: it asks whether the selection moved, and never spends what it reads.
// Before nocx-xn63t.4.1 it asked by minting a target per poll, which is a
// token-book slot held for the whole wait (spec §6.2: maxLiveTokens, never
// evicted) — up to optionSettle's worth of polls per move step, none of them
// spent, so one menu answer could exhaust the book the answer itself needs.
//
// A reader that does not implement this cannot answer that wait, and the loop
// refuses rather than minting to find out: guessing there would be the leak
// this seam exists to remove.
type menuReader interface {
	Menu(sessionID string, f paneview.Frame) (agentdriver.Menu, bool)
}

// paneKeys is assistant.PaneKeys' one production implementation.
type paneKeys struct {
	reader paneKeysReader
	hub    *paneAccessHub
}

// newPaneKeys builds a PaneKeys over reader (the session.read path's own
// mint record and its Read for the option loop's re-reads) and hub (the
// authority chain's StillHolds, the admission gate, the commitBy ledger,
// the per-session helper lookup and the monotonic clock — hub already owns
// all five, so a caller passing its own helpers/clock alongside hub would
// be a second owner of facts hub already keeps; this is a deliberate,
// documented narrowing of the plan's newPaneKeys(reader, hub, helpers)
// signature to newPaneKeys(reader, hub) for that reason).
func newPaneKeys(reader paneKeysReader, hub *paneAccessHub) *paneKeys {
	return &paneKeys{reader: reader, hub: hub}
}

var _ assistant.PaneKeys = (*paneKeys)(nil)

// keyPayload translates a design §4.2 KeyName into sessionruntime's own
// name+modifier spelling (internal/sessionruntime/intent.go's keyNames
// table and splitMods) — a straight pass-through for every plain name
// sessionruntime already spells the same way (case-insensitively), and two
// translations sessionruntime has no identity for on its own: Esc
// (sessionruntime names it "escape") and BackTab (sessionruntime has no
// backtab identity; Shift+Tab reaches the same key through the modifier
// parsing it already has for an ordinary Shift+Tab press).
func keyPayload(k assistant.KeyName) []byte {
	switch string(k) {
	case "Esc":
		return []byte("Escape")
	case "BackTab":
		return []byte("Shift+Tab")
	default:
		return []byte(k)
	}
}

// refused builds a KeysResult naming cause, with no bytes written — every
// refusal this package produces itself (as opposed to one the helper's own
// session.intent answered) shares this shape.
func refused(cause string) assistant.KeysResult {
	return assistant.KeysResult{State: "refused", Refusal: &proto.IntentRefusal{Cause: cause}}
}

func fromIntentResult(r proto.IntentResult, steps int) assistant.KeysResult {
	return assistant.KeysResult{
		State: r.State, BytesWritten: r.BytesWritten, Refusal: r.Refusal, Steps: steps,
	}
}

// Send implements assistant.PaneKeys.
func (k *paneKeys) Send(ctx context.Context, access any, req assistant.KeysRequest) (assistant.KeysResult, error) {
	da, ok := access.(*DescendantPaneAccess)
	if !ok || da == nil {
		return assistant.KeysResult{}, workers.ErrNotReachable
	}
	if k.reader == nil || k.hub == nil {
		return assistant.KeysResult{}, fmt.Errorf("session.keys: %w", errNoPaneRuntime)
	}
	rec, ok := k.reader.Record(req.TokenID)
	if !ok || rec.Access != da {
		// design §6.2: "A token presented under another capability →
		// forged." An unknown tokenId (never minted by this record, or its
		// slot already released) reads the same way to a caller: nothing
		// here identifies it as belonging to this capability, so there is
		// nothing to distinguish it from a token that never was one.
		return refused("forged"), nil
	}

	if req.Option != nil {
		return k.sendOption(ctx, da, rec, *req.Option)
	}

	kind, payload, err := encodeKeyOrText(req)
	if err != nil {
		return assistant.KeysResult{}, err
	}
	result, err := k.commitStep(ctx, da, rec.SessionID, rec.Chain, rec.View.Token, rec.View.TokenID, rec.AccessEpoch, kind, payload)
	if err != nil {
		return assistant.KeysResult{}, err
	}
	result.Steps = 1
	return result, nil
}

// encodeKeyOrText turns req's Key or Text into session.intent's own
// kind+payload pair (design §6.5). Exactly one of Key/Text/Option is
// guaranteed set by executeSessionKeys before Send is ever called; Option
// is handled by sendOption before this is reached.
func encodeKeyOrText(req assistant.KeysRequest) (kind string, payload []byte, err error) {
	switch {
	case req.Key != nil:
		return "key", keyPayload(*req.Key), nil
	case req.Text != nil:
		return "text", []byte(*req.Text), nil
	default:
		return "", nil, errors.New("session.keys: neither a key nor text nor an option was given")
	}
}

// commitStep is the one place that spends a target: the authority re-check
// (design §7.1's "resolved server-side per call", §6.2's StillHolds), the
// admission gate a bump holds closed (§7.2), the commitBy this coordinator
// promises (§7.2), and the helper round trip with its own transport-error
// recovery (§7.2's "transport timeouts are not safety boundaries" — a
// failed call is resolved through session.intent.status before it is ever
// reported, and only reported indeterminate, never cancelled, once that
// cannot answer either).
func (k *paneKeys) commitStep(ctx context.Context, da *DescendantPaneAccess, sessionID string, chain workers.Chain, token, tokenID string, accessEpoch uint64, kind string, payload []byte) (assistant.KeysResult, error) {
	if !k.hub.admitting(sessionID) {
		// A revocation's bump is outstanding for this session right now
		// (design §7.2): "until a session's bump is acknowledged... the
		// coordinator admits no new intent there."
		return refused("access_revoked"), nil
	}
	if k.hub.registrar == nil || !k.hub.registrar.StillHolds(ctx, chain) {
		return refused("access_revoked"), nil
	}
	if k.hub.lookup == nil {
		return assistant.KeysResult{}, fmt.Errorf("session.keys: %w", errNoPaneRuntime)
	}
	helper, ok := k.hub.lookup.HelperFor(ctx, sessionID)
	if !ok {
		return assistant.KeysResult{}, fmt.Errorf("session.keys: %w", errNoPaneRuntime)
	}
	now := k.clockNow()
	commitBy := now + Nanos(commitWindow)
	k.hub.noteCommitBy(sessionID, commitBy)

	result, err := helper.Intent(ctx, sessionID, proto.IntentParams{
		Token: token, AccessEpoch: accessEpoch, CommitBy: int64(commitBy), Kind: kind, Payload: payload,
	})
	if err == nil {
		return fromIntentResult(result, 0), nil
	}
	// The RPC itself failed: this step's own outcome is unknown, never
	// assumed not to have happened (design §7.2). session.intent.status
	// (never presenting the payload again) is asked before anything is
	// reported; pollStatus tries it at least once even if commitBy has
	// already passed, and reports indeterminate — never cancelled — once
	// neither the call nor the poll can answer.
	return k.pollStatus(ctx, sessionID, tokenID, commitBy)
}

// pollStatus is commitStep's short recovery loop: retry session.intent.status
// until it answers a terminal result or commitBy passes, never longer —
// once commitBy has passed, no old intent can still commit (design §7.2),
// so an answer of "unknown" or "in_progress" past that point is reported
// indeterminate rather than waited on further.
func (k *paneKeys) pollStatus(ctx context.Context, sessionID, tokenID string, commitBy Nanos) (assistant.KeysResult, error) {
	if k.hub.lookup == nil {
		return assistant.KeysResult{State: "indeterminate"}, nil
	}
	helper, ok := k.hub.lookup.HelperFor(ctx, sessionID)
	if !ok {
		return assistant.KeysResult{State: "indeterminate"}, nil
	}
	for {
		status, err := helper.IntentStatus(ctx, sessionID, tokenID)
		if err == nil && status.Result != nil {
			return fromIntentResult(*status.Result, 0), nil
		}
		if k.clockNow() >= commitBy {
			return assistant.KeysResult{State: "indeterminate"}, nil
		}
		select {
		case <-ctx.Done():
			return assistant.KeysResult{State: "indeterminate"}, nil
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (k *paneKeys) clockNow() Nanos {
	if k.hub.clock == nil {
		return 0
	}
	return k.hub.clock.Now()
}

// sendOption is the coordinator-side loop design §6.4 describes: under the
// call's own approval and step budget (len(options)+2), move the menu's
// selection one step at a time and confirm once it lands on the wanted
// option. rec is the CALL's own target record — its View.Menu is the menu
// the caller saw when it asked for this option, and a menu that no longer
// matches it (question or option set moved) ends the loop rather than
// guessing at a screen the caller never saw.
func (k *paneKeys) sendOption(ctx context.Context, da *DescendantPaneAccess, rec targetRecord, option string) (assistant.KeysResult, error) {
	if rec.View.Menu == nil || rec.View.Kind != sessionruntime.TargetMenu {
		return refused("stale_target"), nil
	}
	origMenu := *rec.View.Menu
	wantIdx := -1
	for i, o := range origMenu.Options {
		if o == option {
			wantIdx = i
			break
		}
	}
	if wantIdx < 0 {
		return assistant.KeysResult{}, fmt.Errorf("session.keys: %q is not one of this menu's options", option)
	}

	budget := len(origMenu.Options) + 2
	steps := 0
	sessionID := rec.SessionID
	menuKind := sessionruntime.TargetMenu

	for steps < budget {
		read, err := k.reader.Read(ctx, da, sessionID, &menuKind, nil)
		if err != nil {
			return assistant.KeysResult{}, err
		}
		if read.Target == nil || read.Target.Kind != sessionruntime.TargetMenu || read.Target.Menu == nil {
			return refused("stale_target"), nil
		}
		cur := *read.Target.Menu
		if cur.Question != origMenu.Question || !sameOptions(cur.Options, origMenu.Options) {
			return refused("stale_target"), nil
		}

		if k.hub.registrar == nil || !k.hub.registrar.StillHolds(ctx, rec.Chain) {
			return refused("access_revoked"), nil
		}

		if cur.Selected == wantIdx {
			steps++
			result, commitErr := k.commitStep(ctx, da, sessionID, rec.Chain, read.Target.Token, read.Target.TokenID, rec.AccessEpoch, "key", keyPayload("Enter"))
			if commitErr != nil {
				return assistant.KeysResult{}, commitErr
			}
			result.Steps = steps
			return result, nil
		}

		moveKey := assistant.KeyName("Down")
		if wantIdx < cur.Selected {
			moveKey = "Up"
		}
		steps++
		before := cur.Selected
		moveResult, err := k.commitStep(ctx, da, sessionID, rec.Chain, read.Target.Token, read.Target.TokenID, rec.AccessEpoch, "key", keyPayload(moveKey))
		if err != nil {
			return assistant.KeysResult{}, err
		}
		if moveResult.State != "executed" {
			moveResult.Steps = steps
			return moveResult, nil
		}

		if !k.awaitSelectionMove(ctx, da, sessionID, origMenu, before) {
			return refused("stale_target"), nil
		}
	}
	return refused("stale_target"), nil
}

// awaitSelectionMove is design §6.4 step 4's wait: after one Up/Down, poll
// for a snapshot whose selection moved by exactly one, within optionSettle.
// A selection that did not move, moved by more than one, or moved back to a
// row already seen this call is refused by the caller (sendOption) via a
// false return, never accepted as "close enough".
//
// It reads and never mints (nocx-xn63t.4.1): "did the selection move" is a
// question about the screen, answered by the same agent rule the read path
// already classifies with (menuReader), and a target minted to ask it is a
// token-book slot held for the whole wait — up to optionSettle's worth of
// polls per move step, none of them spent. That is the same defect the message
// delivery's probes had, in the one call a coordinator uses to ANSWER the menu
// those probes were starving the book for.
func (k *paneKeys) awaitSelectionMove(ctx context.Context, da *DescendantPaneAccess, sessionID string, orig agentdriver.Menu, before int) bool {
	menus, ok := k.reader.(menuReader)
	if !ok {
		// No menu reading, no way to ask this without minting. Refused rather
		// than minted: see menuReader's own doc.
		return false
	}
	deadline := time.Now().Add(optionSettle)
	for time.Now().Before(deadline) {
		read, err := k.reader.Read(ctx, da, sessionID, nil, nil)
		if err == nil {
			if cur, found := menus.Menu(sessionID, read.Frame); found {
				if cur.Question == orig.Question && sameOptions(cur.Options, orig.Options) {
					if cur.Selected == before+1 || cur.Selected == before-1 {
						return true
					}
					if cur.Selected != before {
						// Moved by more than one, or to somewhere this call did
						// not ask for — never accepted as progress.
						return false
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(optionPollInterval):
		}
	}
	return false
}

func sameOptions(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
