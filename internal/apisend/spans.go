package apisend

// Diagnostics: the raw text of both sides, segmented, and never a secret
// value (design §11).
//
// # Why raw rides on the send result rather than on a method of its own
//
// The plan proposed an `api.request.raw` method. The raw text belongs to a
// PARTICULAR RUN — this exchange, these substitutions, this truncation — so
// a second round trip could only ever fetch the raw of a different send.
// Two sends racing on one request would answer each other's question, and
// neither caller could tell. So it is a field on the result of the send
// that produced it, and the schema says so.
//
// # The value never crosses, in any of the three states
//
// ADR-0011 keeps credential values away from the renderer, and §11.2 states
// the consequence: the raw text crosses ALREADY SEGMENTED — literal runs,
// plus spans annotated with the NAME of a secret. This package therefore
// elides the bytes and writes a placeholder in their place. A renderer that
// ignores spans entirely still shows no credential, which is the property a
// span-aware renderer alone would not have.
//
// Three states, never two (§11.1):
//
//   - the bytes still equal the secret → "secret", named. This is exactly
//     the secret you bound, and the badge is EVIDENCE rather than a curtain.
//   - our span, bytes differ → "secret-damaged", naming the SHAPE of the
//     damage and never its bytes. This is the row that makes the whole
//     thing safe: a truncated token is a PREFIX OF A LIVE TOKEN, so "show
//     the text when it does not match" would print the beginning of a real
//     credential in the clear.
//   - not our span → "text". It is not a secret.
//
// # The two sides are two mechanisms, and saying otherwise was wrong (§11.3)
//
// A placement is an offset where the SENDER ITSELF put a value. It says
// nothing about whether a server echoed those bytes back, or where, so the
// response cannot be marked by the same mechanism: it has to be searched.
// MarkRequest verifies placements; SearchResponse runs a bounded
// known-plaintext search over the two or three values this request actually
// used. The limits of that search are stated on SearchResponse rather than
// discovered in production.

import (
	"fmt"
	"sort"
	"strings"
)

// The three span kinds of §11.1. They are exported because the schema
// declares them as an enum and the renderer switches on them; a fourth
// would be a fourth row in that table, decided there first.
const (
	SpanText          = "text"
	SpanSecret        = "secret"
	SpanSecretDamaged = "secret-damaged"
)

// Span is one run of the raw text. From and To index Text — the text as it
// crosses, AFTER elision — so a renderer draws the whole payload by walking
// the spans in order, and they tile it with neither gap nor overlap.
//
// Name is the NAME of a secret, never its value. Damage is the shape of the
// damage — "truncated, 24 of 214 bytes" — and is empty unless the kind is
// secret-damaged. There is no field here in which a value could ride, which
// is the point: the property is structural rather than a rule somebody has
// to remember when adding a field.
type Span struct {
	From   int    `json:"from"`
	To     int    `json:"to"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Damage string `json:"damage"`
}

// Raw is one side of the exchange: the text that crosses, and how it is
// segmented. Spans is never nil — a side with nothing to mark carries [],
// because a renderer walking null is a crash rather than an empty view.
type Raw struct {
	Text  string `json:"text"`
	Spans []Span `json:"spans"`
}

// The two sides are NOT a pair type. They are two mechanisms with two
// different guarantees (§11.3), and they now live at two different levels
// for a reason that is the same fact stated once more: the request side is
// composed before the dial and belongs to the ATTEMPT, so it is on Exchange
// and a run that never got an answer still has it; the response side exists
// only when something answered, so it is on Response. A struct holding both
// would have had to sit somewhere, and wherever it sat it would have taken
// the request text away from the runs that most need it.

// Placement is what the sender knows BECAUSE IT DID THE SUBSTITUTING: the
// offsets in the text it composed where a named secret's value was written.
// It is why the request side is a VERIFICATION rather than a search.
//
// Want is the value that was placed, and it never crosses the wire: nothing
// marshals a Placement, and this is an input to MarkRequest rather than a
// field of Raw. The offsets are into the text as COMPOSED; the text that
// finally crosses may be shorter, and the gap between the two is exactly
// what the damaged state reports.
type Placement struct {
	From, To   int
	Name, Want string
}

// NamedSecret is one value this request used, with the name to badge it by.
// It is the input to the response search and, on the request side, what the
// sender locates in the text it composed.
type NamedSecret struct {
	Name  string
	Value string
}

// MarkRequest segments text against the placements the sender recorded.
//
// It VERIFIES rather than searches: for each placement it asks whether the
// bytes at those offsets are still the value that was put there. They are
// not, whenever the composed text was bounded before it crossed — the
// placement is what the sender did, the text is what fitted, and the
// difference between them is the damage. In every case the bytes are elided
// and a placeholder naming the secret takes their place.
func MarkRequest(text string, placed []Placement) Raw {
	return segment(text, collapse(placed))
}

// SearchResponse marks the values this request used wherever a server
// echoed them back. APIs echo credentials into error text, and without this
// such a response reaches the renderer as ORDINARY TEXT in a view whose
// entire purpose is to show everything (§11.4).
//
// It is a BOUNDED KNOWN-PLAINTEXT search over the two or three values this
// request actually used — never a sweep against the vault, which §3 excludes
// — and three limits are stated here rather than discovered in production
// (§11.3). Each has a test:
//
//   - It runs on the DECODED body only. A compressed or chunked frame is
//     searched after decoding, never before; the caller passes the decoded
//     text, and a binary body has none, so nothing is searched.
//   - It makes NO attempt at transformed spellings. A base64-wrapped or
//     URL-escaped token is NOT found, deliberately, so that the coverage is
//     never overstated. Adding encodings would be a sweep wearing a
//     bounded search's clothes, and each one added would still miss the
//     next.
//   - Overlapping matches COLLAPSE TO THE LONGEST, so one run of bytes is
//     marked once and the spans still tile the text.
func SearchResponse(decoded string, used []NamedSecret) Raw {
	return segment(decoded, collapse(locate(decoded, used)))
}

// locate finds every occurrence of every used value. This is the search of
// §11.3 on the response side; on the request side the sender calls it on
// the text IT COMPOSED, where it is bounded to bytes the sender wrote
// rather than to whatever a server chose to send.
//
// An empty value matches nothing. An unbound variable that resolved to ""
// would otherwise match at every offset and turn the whole body into
// badges.
func locate(text string, used []NamedSecret) []Placement {
	var out []Placement
	for _, s := range used {
		if s.Value == "" {
			continue
		}
		for at := 0; ; {
			i := strings.Index(text[at:], s.Value)
			if i < 0 {
				break
			}
			from := at + i
			out = append(out, Placement{From: from, To: from + len(s.Value), Name: s.Name, Want: s.Value})
			at = from + len(s.Value)
		}
	}
	return out
}

// collapse orders the placements and resolves overlaps to the longest, so
// the spans that come out of segment tile the text exactly once. A
// placement with nothing to mark — an empty value, an inverted or negative
// range — is dropped rather than turned into a zero-width badge.
func collapse(ps []Placement) []Placement {
	kept := make([]Placement, 0, len(ps))
	for _, p := range ps {
		if p.Want == "" || p.From < 0 || p.To <= p.From {
			continue
		}
		kept = append(kept, p)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].From != kept[j].From {
			return kept[i].From < kept[j].From
		}
		// The longest first at a given start, so the first one kept is the
		// one the overlap rule wants.
		return kept[i].To-kept[i].From > kept[j].To-kept[j].From
	})

	out := make([]Placement, 0, len(kept))
	for _, p := range kept {
		if len(out) == 0 || p.From >= out[len(out)-1].To {
			out = append(out, p)
			continue
		}
		if last := out[len(out)-1]; p.To-p.From > last.To-last.From {
			// Overlapping and longer: it replaces the shorter one. Ordering
			// holds because p.From >= last.From >= the end of the one
			// before it.
			out[len(out)-1] = p
		}
	}
	return out
}

// segment builds the text that crosses and the spans over it. The
// placements must already be ordered and non-overlapping.
//
// The output text is NOT the input: every placed run is elided and replaced
// by a placeholder. That is what makes "the value never crosses" a property
// of the payload rather than of the renderer, and it is why the acceptance
// criterion is written against the marshalled JSON — a span that elided its
// own bytes while leaving them in Text would be a leak wearing a badge.
func segment(text string, placed []Placement) Raw {
	if len(placed) == 0 {
		// Nothing is marked, so nothing is elided and the text is the text.
		// Returning it uncopied is not only cheaper: a 2 MiB body would
		// otherwise be rebuilt byte by byte to produce the identical
		// string, and the capture test that proves the body is never
		// buffered would be measuring this instead.
		if text == "" {
			return Raw{Spans: []Span{}}
		}
		return Raw{Text: text, Spans: []Span{{To: len(text), Kind: SpanText}}}
	}
	out := Raw{Spans: make([]Span, 0, len(placed)*2+1)}
	var b strings.Builder
	cursor := 0

	for _, p := range placed {
		from := min(max(p.From, 0), len(text))
		to := min(max(p.To, from), len(text))
		if from > cursor {
			out.Spans = append(out.Spans, writeSpan(&b, text[cursor:from], SpanText, "", ""))
		}
		surviving := text[from:to]
		if p.To <= len(text) && surviving == p.Want {
			out.Spans = append(out.Spans, writeSpan(&b, "⟦"+p.Name+"⟧", SpanSecret, p.Name, ""))
		} else {
			d := damage(surviving, p.Want)
			out.Spans = append(out.Spans, writeSpan(&b, "⟦"+p.Name+" · "+d+"⟧", SpanSecretDamaged, p.Name, d))
		}
		cursor = to
	}
	if cursor < len(text) {
		out.Spans = append(out.Spans, writeSpan(&b, text[cursor:], SpanText, "", ""))
	}

	out.Text = b.String()
	return out
}

// writeSpan appends s to b and returns the span that covers it.
func writeSpan(b *strings.Builder, s, kind, name, damage string) Span {
	from := b.Len()
	b.WriteString(s)
	return Span{From: from, To: b.Len(), Kind: kind, Name: name, Damage: damage}
}

// damage names the SHAPE of the damage and nothing else. Both sentences
// carry only lengths: the surviving bytes are the beginning of a live
// credential, so the count is the most that can be said about them.
func damage(surviving, want string) string {
	if strings.HasPrefix(want, surviving) {
		return fmt.Sprintf("truncated, %d of %d bytes", len(surviving), len(want))
	}
	return fmt.Sprintf("altered, %d of %d bytes match", commonPrefix(surviving, want), len(want))
}

// commonPrefix is how much of the value is still where it was put.
func commonPrefix(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
