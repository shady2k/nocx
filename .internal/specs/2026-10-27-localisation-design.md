# Localisation: the UI speaks a second language

- **Status:** approved by the owner (2026-10-27).
- **Beads:** epic `nocx-dej6`; this design is the deliverable of `nocx-sjpuk`.
- **Owner decisions already given:** the second language is Russian (2026-10-27); only
  user-visible strings are translated, logs and internal prose stay English; the
  catalogue-architecture question is answered below with options, and the owner asked to
  see the options in the design.
- **Owner decisions on the design (2026-10-27):** Option B approved — the TypeScript
  catalogue owns translations, Go ships keys alongside English prose as the fallback.
  Error reason-codes approved — incremental (vault, connections, files first), raw English
  as the visible fallback. Language options show endonyms: English / Русский.

## Problem

Every user-facing string in nocx is an English literal at its point of use. There is no
catalogue, no locale selection, no plural or date handling anywhere. Beyond the frontend,
two vocabularies are Go-owned and shipped to the renderer: the settings declarations
(`internal/settings/settings.go`, ~68 `Label:` strings plus descriptions, option labels and
`ZeroLabel`), and the notify catalogue (`internal/notify/catalogue.go`, `Label`+`Description`
per kind, rendered in `frontend/src/notify/notifications-panel.tsx`). Go error prose also
reaches the renderer verbatim in ~60 toast call sites (`err.message` shown raw).

## Inventory of user-visible English

| Surface                                                                                                                                                  | Where                                                                     | Approx. count                  |
| -------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | ------------------------------ |
| Settings declarations                                                                                                                                    | Go `settings.go` (labels, descriptions, option labels, zero-labels)       | ~68 label sites + descriptions |
| Notify catalogue                                                                                                                                         | Go `internal/notify/catalogue.go`                                         | ~15 kinds × label+description  |
| Frontend surfaces (api, connections, vault, secrets, files, git, skills, notes, snippets, notify panel, settings chrome, overview, ports, tabs, sidebar) | `frontend/src/*`                                                          | ~800–1000                      |
| UI kit literals                                                                                                                                          | `frontend/src/ui/` (toast dismiss, folder-picker, secret-picker, dialogs) | ~25                            |
| Terminal/pane notices                                                                                                                                    | `terminal-content.ts` and friends                                         | ~15                            |
| Go error prose shown in toasts                                                                                                                           | `err.message` at ~60 call sites                                           | unbounded, mostly raw          |

## Design questions and options

### Q1 — Where does the catalogue live

**Option A — Go owns one catalogue; the frontend receives translated strings over the wire.**
`settings.describe` and `notify.catalogue` ship `label`/`description` already resolved to the
active locale; the frontend keeps a small TS catalogue only for its own chrome. Two
catalogue owners (Go for backend-driven text, TS for frontend chrome) — matches today's
ownership split exactly, no new wire surface. Cost: Go learns about translations; a frontend
string cannot be translated without a Go release. Rejected as the single answer because the
bulk of strings (~800+) are frontend-owned; shipping them through Go inverts ownership.

**Option B — TypeScript owns the whole catalogue; Go ships keys, not prose.**
Declarations carry a stable `key`; the frontend maps `key → {en, ru}` in TS. The generated
settings screen needs the contract to grow a key field (a schema change, regenerated types),
and the Go test that asserts English labels today keeps asserting the source language. All
strings — frontend chrome and backend-driven — are translated in one place, one format, one
reviewer. Cost: the contract change, and the English source of truth for declarations moves
into the TS catalogue.

**Option C — shared catalogue file (e.g. `catalogues/messages.<locale>.json`), consumed by both Go and TS through code generation.**
One source, both runtimes. Cost: a new build step, a new file format to police, generated
artefacts in two languages (Go and TS), and the repo's lint fixtures would need rules for
it. The single-source benefit over B is real only for the ~80 Go-owned strings; for the
other ~900 it adds machinery without changing anything.

**Recommendation: B**, with the Go-owned vocabularies reduced to keys over time. It keeps
the settings screen inside the generated-screen invariant (the renderer translates; the
declarations declare), puts all translation work in one catalogue with one format, and needs
no new build pipeline. The fallback rule (below) keeps the English text visible whenever a
key is missing from the Russian catalogue, so an untranslated setting never renders blank.

### Q2 — How does a declaration carry its translation

Under B, every declaration, option and group gains a stable **translation key** next to the
English prose, minted in Go (e.g. `settings.ui.theme.label`). The wire keeps carrying the
English `label`/`description` — they are the fallback — and adds `labelKey`/`descriptionKey`
(the same convention for options and groups). The generated screen resolves
`t(labelKey, label)`: the catalogue's Russian entry when present, the shipped English string
otherwise. Adding a language remains catalogue-only: Go mints keys, the catalogue adds a
column, no code change — which is the epic's own success criterion.

### Q3 — What happens to Go-authored error prose the renderer shows verbatim

Today ~60 toast sites interpolate `err.message` raw. Localising Go's whole error corpus is
out of proportion: much of it is operator-facing (logs), and the repo's own comment at
`ws_history_status.go:33` says an error code alone deliberately under-describes. The design
extends the pattern the vault already uses (`REASON_MESSAGES` in `frontend/src/vault.tsx`,
fed by a structured `reason` in `err.data`):

- Go sends a stable **reason code** in the error's `data` field for the errors a person can
  act on. The frontend maps `reason → {en, ru}` sentence, interpolating parameters, and
  shows that; the raw `err.message` remains the fallback and is always available on the
  toast for copying.
- Errors without a reason code keep showing the English message verbatim — an honest
  fallback, consistent with the visible-fallback rule.
- Reason codes are added incrementally, starting with the most common paths (vault,
  connections, files). Full coverage is explicitly out of this epic's first slice; the
  mechanism is the deliverable, coverage follows as catalogued work.

### Q4 — Locale selection and switching

A `ui.language` Select setting declared in Go (`en`, `ru`), following the `ui.theme` flow
exactly: `MustRegisterSelect` in `internal/settings/settings.go` → shipped by
`settings.describe` → a consumed-key constant and `reconcileLocaleFromGo` in
`frontend/src/main.tsx`, seeded synchronously from localStorage before first render (as
`theme-bootstrap.ts` does) so the first frame already speaks the right language.

## Frontend catalogue shape

`frontend/src/i18n/` with:

- `state/locale-model.ts` — a signal holding the active locale plus the `t(key, fallback?)`
  lookup, imported by surfaces through `state/` only (no cycles; the toast-host singleton
  signal at `ui/toast.tsx:96` is the existing pattern).
- `catalogues/en.ts`, `catalogues/ru.ts` — flat key → string maps, typed so a missing
  Russian entry is a type error at build time when `en` and `ru` key sets are compared by a
  lint fixture (new `check-i18n-catalogues.mjs`, wired into `lint:static`): a key missing
  from `ru` warns, a key missing from `en` fails.
- `t(key, fallback)` resolution order: `ru[key]` → `en[key]` → the fallback literal at the
  call site → the key itself. The call-site literal stays in place as the visible fallback
  (the epic's criterion: fallback is visible English, never blank, never a raw key).

Keys are namespaced by surface: `connections.title`, `settings.ui.theme.label`,
`vault.unlock.prompt`, `notify.kind.commandFinished.label`. The English catalogue doubles as
the completeness check: a key that exists nowhere in `en` is dead.

## Plural and date handling

Russian needs plural forms (`1 команда / 2 команды / 5 команд`). The catalogue value is a
string or a small plural object `{ one, few, many, other }` resolved with
`Intl.PluralRules('ru')`. Dates and times use `Intl.DateTimeFormat` with the active locale.
No dates are formatted by hand anywhere in the frontend today (grep for `toLocaleDateString`
found none), so the two `Intl` primitives cover the whole grammar need.

## Stories

1. **Switch language:** a person opens Settings → Interface → Language, picks Русский; every
   surface, including the generated settings screen and the notify panel, re-renders in
   Russian without a restart.
2. **Missing translation:** a key absent from the Russian catalogue renders its English
   fallback from the call site — never blank, never the raw key.
3. **New setting, no translation yet:** a Go declaration lands without a Russian entry; the
   settings screen shows it in English while everything around it is Russian.
4. **Error with a reason code:** a vault refusal shows the Russian sentence mapped from the
   reason code with its parameters interpolated.
5. **Error without a reason code:** the toast shows the raw Go message, in English, with
   copy available — the honest fallback.
6. **Persistence and first frame:** the language choice persists; on next launch the first
   rendered frame already uses it (localStorage seed, before the Go snapshot reconciles).
7. **Plural counts:** a count of items renders with correct Russian plural forms.
8. **Adding a third language:** requires only a new catalogue file and a new option in the
   `ui.language` Select's option list — no code change.

## Stages (proposed)

1. **Mechanism:** locale model, `t()`, catalogues, `ui.language` setting, reconcile flow,
   lint fixture. English-only behaviour unchanged.
2. **Backend-driven vocabularies:** keys on settings declarations and options (contract
   change: `labelKey`/`descriptionKey`), keys for the notify catalogue, generated screen
   translates; Russian catalogue for both.
3. **Frontend surfaces:** translate surface by surface (api workbench, connections, vault,
   secrets, files, git, settings chrome, notify panel, overview, kit literals, terminal
   notices), in several tasks by area.
4. **Error reason codes:** the `data.reason` mechanism plus Russian sentences for vault,
   connections, files first; a catch-up task files the remaining paths.

Deliberately out of scope (unchanged from the epic): RTL layout, translating terminal
content (AD-6), POSIX locale for the PTY (handled in nocx-5hs), localising Go logs.

## Open questions for the owner

1. Approve Option B (TS-owned catalogue, Go ships keys alongside English prose)?
2. Approve the reason-code approach for Go errors (incremental, vault/connections/files
   first), with raw English as the visible fallback?
3. Is «Русский» the right label for the option in the picker (vs «Russian» in the English
   locale — the picker shows each language in its own tongue, endonyms: English, Русский)?
