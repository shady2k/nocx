# ADR-3myzaab2 — Delivery sentences travel as keyed segments, beside the raw text

Date: 2026-11-25
Status: Accepted. Applies ADR-nswp57x0 to the notification centre's feed occurrence
(`nocx-dej6.3.11`). Supersedes nothing and is not superseded.

## What was wrong

Three sentences that nocx composes reached the screen as English with no translation key
on the wire:

- `"Not delivered"`, and `": " + <the event's title>` when the event has one
  (`RecordDeliveryFailure`, `internal/notify/feed.go`);
- `"<channel> could not deliver it: <cause>"` (the same function);
- `"<n> more <noun>"`, and the body joining the newest held-back event's title and body
  with an em dash (`deliverSummary`, `internal/notify/policy.go`).

ADR-nswp57x0 requires every string a person reads to be translatable, except raw backend
error prose. The occurrence carried only `title` and `body`, so the renderer had no way to
translate any of the three. Worse, the title case composes a keyed sentence around another
piece of text: the event's title is program prose, and it is embedded in a sentence that
must be translated.

## What is decided

An occurrence keeps its raw `title` and `body` unchanged, and gains two ordered lists beside
them: `titleSegments` and `bodySegments`. Each list is the sentence for its slot, in order.
A segment is one of exactly two closed forms:

- a **keyed** segment, `{ key, params? }`, where `key` names a catalogue entry and `params`
  is a map of scalars (a count, a catalogue channel id);
- a **text** segment, `{ text }`, shown verbatim, for a program's or the backend's own words.

The renderer joins a slot's translated segments in order. A slot with no segments shows the
raw string, so a locale without the keys, and any future producer that composes nothing,
still reads the event.

The three sentences become:

| Sentence                       | Segments                                                              |
| ------------------------------ | --------------------------------------------------------------------- |
| Failure title, no event title  | `[key notify.delivery.failed.title]`                                  |
| Failure title, with event title| `[key …title, key …titleSeparator, text <event title>]`              |
| Failure body                   | `[key notify.delivery.failed.body {channel}, key …causeSeparator, text <cause>]` |
| Window summary title           | `[key notify.delivery.more {count}]` (a plural form)                  |
| Window summary body            | `[text <newest title>, key …more.separator, text <newest body>]`      |

## Why segments, and not one key per slot

The first design put one key and one set of params on each slot. It cannot express the
failure title. `"Not delivered: <event title>"` has two parts: a translatable sentence and
another occurrence's text. One key per slot forces the embedded text into a param, which
makes program prose a parameter of a translated sentence. Then the renderer would be
interpolating display text, and the composed title could never be keyed end to end. A list
of segments keeps each piece either translatable or verbatim, and never mixes them.

## Why params are scalars, and text is never a param

A param is data: a count, a channel id, a number. Text that a person or a program wrote is
prose, and prose is not ours to translate, so it travels as a `text` segment, not as a
param. The wire schema enforces this shape: `params` is a map of string or number, and a
segment has exactly one of the two forms, each closed with `additionalProperties: false`.

## Why the cause stays English

The `%v` cause in `"could not deliver it: %v"` is backend error prose. ADR-nswp57x0 keeps
backend prose raw, so it is a `text` segment and is never keyed. The sentence's own words,
"could not deliver it", are keyed.

## Why the channel is a catalogue id, not a label

The channel is a fixed id (`banner`, `toast`) or the pipeline's name. It is passed as a
`channel` param, so the renderer can translate it through the catalogue. A translated label
is never sent or compared (ADR-nswp57x0 point 4). The pipeline's name has no catalogue entry
yet; a locale without one shows the raw English through the body's raw fallback.

## What the next person inherits

- A new composed sentence adds keys to both catalogues and a segment list on its producer.
  Do not concatenate translated words in Go.
- A segment is `key` or `text`, never both. The schema rejects a segment that carries both
  forms, so a producer cannot send text that is also meant to be translated.
- The raw `title` and `body` are still produced and still the fallback. Removing them would
  make a locale without the keys show nothing.
- The over-the-wire test `TestNotifyFeedRead_OverTheWireCarriesDeliverySegments` reads the
  real result off the socket. A test that builds its own payload would prove only that the
  struct is well-formed.
