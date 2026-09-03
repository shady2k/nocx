package content

// The redaction receipt and the one rewrite policy both stores obey
// (nocx-rtg0.24).
//
// WHY THE RECEIPT IS IN entries.payload AND NOT IN COLUMNS OF ITS OWN.
// sqlite.go states the protocol: any change to schemaV1 bumps schemaVersion.
// When this was written a bump DROPPED every table — including
// command_history, which was then the live history path holding the user's
// real commands — so adding three columns here would have made the user pay
// that cost a second time for a receipt on a table nothing read yet. A bump
// is a migration now (nocx-lmb6v.1) and costs a rung rather than the rows,
// so the argument for the payload column is the weaker one it always also
// was: a receipt is not kind-specific and needs no shape of its own.
//
// That column's comment calls it "kind payload, sparse extension only", and a
// redaction is not kind-specific — this widens the field's meaning
// deliberately and says so, because a field that quietly acquires a second
// owner is how two writers of one column start disagreeing. The widening has
// one rule, enforced by the two functions below: the payload is a JSON
// OBJECT, the kind arm owns its own keys, and the receipt owns exactly one —
// `masking`. Neither writer touches the other's keys.
//
// WHY THE POLICY IS SHARED. Two tables hold masked command text: the interim
// command_history (rowid) and entries (client-minted UUIDv7). Each repository
// writes its own rows — that is the table boundary — but the DECISION a
// rewrite makes is one behaviour and has one owner (AD-8): validate the span
// against the text it addresses, refuse rather than corrupt when it no longer
// fits, and treat the row's CURRENT redactions as the idempotency authority
// so a retried save is a no-op rather than a replacement at stale offsets.
// applyRedactionRewrite is that owner; doRewrite and the ledger's
// RewriteRedaction are both callers of it. Every line of it was bought by
// internal/content/redaction_test.go.

import (
	"encoding/json"
	"fmt"
)

// entryMaskingKey is the payload key the receipt owns. Nothing else may
// write it, and the kind arm never reads it.
const entryMaskingKey = "masking"

// EntryMasking is the redaction receipt of one ledger entry: what the wire's
// masker took out of the intent before it became durable. The three fields
// are exactly command_history's masked_count, masked_kinds and redactions,
// and exactly the three history.query's contract declares on every entry.
//
// MaskedCount and MaskedKinds describe what WAS masked and are never revised
// afterwards — a span saved to the vault is a later event, not a claim that
// the command never carried a secret. Redactions is the live list: a segment
// leaves it when it becomes a vault reference, which is what makes the list
// the idempotency authority for a retried save.
type EntryMasking struct {
	MaskedCount int         `json:"maskedCount"`
	MaskedKinds []string    `json:"maskedKinds"`
	Redactions  []Redaction `json:"redactions"`
}

// normalized returns the receipt with its two lists non-nil. Never null on
// the wire and never null in the column: history.query's contract says
// "no matches is []", and a null the renderer has to special-case is the
// same defect one layer down.
func (m EntryMasking) normalized() EntryMasking {
	if m.MaskedKinds == nil {
		m.MaskedKinds = []string{}
	}
	if m.Redactions == nil {
		m.Redactions = []Redaction{}
	}
	return m
}

// WithEntryMasking merges the receipt into an entry payload, preserving every
// other key the payload holds — the kind arm above all. arm may be "" or
// "{}" for an entry that has no kind payload yet.
//
// It is a merge and not a construction because two writers contribute to this
// one column at different moments: the open writes the receipt, the close
// writes the kind arm, and a save rewrites the receipt in between. Whichever
// writes second must not erase what the other wrote.
func WithEntryMasking(arm string, m EntryMasking) (string, error) {
	obj, err := decodePayloadObject(arm)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(m.normalized())
	if err != nil {
		return "", err
	}
	obj[entryMaskingKey] = json.RawMessage(body)
	out, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// EntryMaskingOf reads the receipt back out of an entry payload. A payload
// carrying no receipt answers the empty one — which is the right answer for
// a row nothing was masked from, and the honest answer for a row written by a
// build that kept no receipts. Both are "no spans to rewrite"; neither is a
// reason to slice an intent at offsets nobody recorded.
func EntryMaskingOf(payload string) (EntryMasking, error) {
	obj, err := decodePayloadObject(payload)
	if err != nil {
		return EntryMasking{}, err
	}
	raw, ok := obj[entryMaskingKey]
	if !ok {
		return EntryMasking{}.normalized(), nil
	}
	var m EntryMasking
	if err := json.Unmarshal(raw, &m); err != nil {
		return EntryMasking{}, fmt.Errorf("content: entry payload %q is not a redaction receipt: %w", entryMaskingKey, err)
	}
	return m.normalized(), nil
}

// decodePayloadObject parses an entry payload as a JSON object. An empty
// string is the empty object (the column's own default); anything that is not
// an object is refused rather than replaced — a caller handing over garbage
// must learn it did, not have it silently overwritten.
func decodePayloadObject(payload string) (map[string]json.RawMessage, error) {
	if payload == "" {
		return map[string]json.RawMessage{}, nil
	}
	obj := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return nil, fmt.Errorf("content: entry payload is not a JSON object: %w", err)
	}
	return obj, nil
}

// applyRedactionRewrite is the one decision a redaction rewrite makes,
// whichever table holds the row. It returns the rewritten text, the
// redactions that survive, and whether anything matched at all.
//
//   - The span is BYTE offsets into text. One that no longer fits means the
//     row changed shape underneath the caller: refuse, never corrupt.
//   - The span must be one of the CURRENT redactions. A retried save (a lost
//     response) re-sends the span it captured at record time; the first
//     attempt already removed it, so the retry is a no-op instead of a
//     replacement at stale offsets. matched=false is that no-op, and it is
//     not an error.
//
// rowID appears only in the refusal message, so the caller's own key type —
// a rowid or a UUIDv7 — reads correctly in the error.
func applyRedactionRewrite(
	text string, redactions []Redaction, span Redaction, reference, rowID string,
) (rewritten string, kept []Redaction, matched bool, err error) {
	if span.Start < 0 || span.End > len(text) || span.Start > span.End {
		return "", nil, false, fmt.Errorf(
			"content: redaction span [%d:%d] out of range for row %s", span.Start, span.End, rowID)
	}
	kept = make([]Redaction, 0, len(redactions))
	for _, r := range redactions {
		if r.Start == span.Start && r.End == span.End && r.Kind == span.Kind {
			matched = true
			continue
		}
		kept = append(kept, r)
	}
	if !matched {
		return text, redactions, false, nil
	}
	return text[:span.Start] + reference + text[span.End:], kept, true, nil
}
