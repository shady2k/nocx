# ADR-nswp57x0 — Catalogues live in the renderer, Go ships keys beside its English, and an untranslated string is English rather than a defect

Date: 2026-11-06
Status: Accepted. Extends the localisation design of epic `nocx-dej6` (stage 3's close,
`nocx-dej6.3.8`) with the rules the interface's second language is held to from here on.

## What was wrong

The product speaks English at every point of use. Making it speak Russian was done as a
sweep — `t(key, english)` at the call site, a catalogue per language — and the sweep is
only as good as the rules underneath it, which were never written down together. Three
failures came out of that in the same review:

- **A string captured once cannot follow the language.** `t()` reads a signal. Called at
  module evaluation, at component setup, or into a table built while a descriptor is
  constructed, it answers with the language the window started in, and the surface keeps
  that answer for the life of the process. The approval prompt's title, the activity
  rail's view names, the vault's auto-lock choices and the notification filters were all
  in that state, and no reload would ever have shown it.
- **A catalogue that must be complete lies about what has been reviewed.** The Russian
  catalogue was complete "by policy": an unapproved wording was copied in as English and
  counted as translated, so the file could not say which of its 1 400 values a round had
  actually ruled on.
- **Nothing stopped the next hardcoded string.** A ratchet existed, but it saw only a
  literal in a text position — not the conditional beside it, not the variable it came
  from, not the element whose `textContent` was assigned.

## What is decided

**1. The catalogues live in the renderer, and Go ships keys beside its English prose.**
`frontend/src/i18n/catalogues/en.ts` and `ru.ts` are the only catalogues. Every Go
declaration that reaches a screen carries a minted key beside its English string —
`LabelKeyFor`, `DescriptionKeyFor`, `OptionLabelKeyFor`, `ZeroLabelKeyFor`,
`SectionKeyFor`, `GroupKeyFor` in `internal/settings`, `KindLabelKey` and
`KindDescriptionKey` in `internal/notify` — and the English is the fallback the key
resolves when no entry exists. The key is a pure function of the identity (setting key,
kind id), so a declaration site cannot invent a second spelling.

The alternative — one catalogue served to both sides over the wire — was rejected: it
would put display text on the control plane, which is a protocol change for a rendering
concern, and it would make the backend a participant in a decision it has no context
for. The alternative — translating Go's prose on the way out — was rejected because text
is never an identifier: a screen must never compare, key on, or route by a translated
string, and the moment the backend starts rewriting prose there is no line between the
two.

**2. Resolution order, one owner.** `t(key, fallback)` resolves the active catalogue,
then the English catalogue, then the call-site literal, then the key itself. Never blank;
never a raw key. `tPlural(key, count, fallback)` is the same order with the plural form
chosen by `Intl.PluralRules` in the active locale, and `{count}` is interpolated on every
path — including the fallback, because a sentence that prints `{count}` is the same
defect as a blank one.

**3. A missing translation is not a defect.** A key present in `en` and absent from `ru`
renders the English catalogue value: that is the stated behaviour, and the checker
REPORTS such keys as a worklist without failing. The two directions that are errors are
the ones with no runtime answer: a key in `ru` that `en` does not know (it can never
resolve), and a literal `t()` call-site key that no catalogue mints (it renders its
fallback forever). The same checker requires the two catalogues to carry the same
placeholders, form by form, so a translation that drops `{error}` cannot print a hole.

**4. Text is never compared, sent, or used as an identifier.** No surface may branch on a
translated string, key a map by one, or send one over the wire. Identities are ids and
keys (`ui.language`, `notifications.route.bell.toast`, a wire enum); the words are the
catalogue's. Anything that needs a stable name has one that is not a sentence.

**5. Terminal content is out.** What a program prints is not ours to localise (AD-6): the
painted cells, the scrollback bodies, the screen frames and the OSC-derived titles reach
the grid as bytes and are never translated, offered to a catalogue, or measured for a
language. The chrome around them is; the content inside never is.

**6. The contract: new user-visible text goes through the catalogue.** `check-i18n-hardcoded.mjs`
is that contract, and it reads four shapes:

- a literal JSX text node, or a literal in a text-bearing prop (`title`, `aria-label`/
  `ariaLabel`, `label`, `placeholder`, `description`, `hint`, `message`);
- a CONDITIONAL in a text position — each branch is copy;
- a VARIABLE or a FUNCTION RETURN that lands in one (resolved within the file);
- imperative DOM text: `textContent`/`innerText`/`innerHTML`/`title`/`label`/… assigned a
  literal, and `setAttribute('aria-label'|'title'|…, literal)`. `dataset.*` is data, never
  copy, and is not read as a sentence.

An existing violation is baselined in `lint-fixtures/i18n-hardcoded-baseline.json`, and
the baseline may only shrink: the updater refuses growth, so a new literal is a
violation to fix rather than an entry to mint. A permanently-English string — an example
value, a protocol name, a unit, a measured glyph — lives in
`lint-fixtures/i18n-hardcoded-allowed.json` with a reason, reviewed when touched.

**7. The timing rule is a gate too.** A string that goes through `t()` still keeps the
language the window started in if the call runs once. `check-i18n-timing.mjs` refuses a
`t()`/`tPlural()` at module scope, in a component body (outside JSX and outside the kit's
lazy builders), in a class field, and in the function a module invokes at its own top
level (`main()`) — the four shapes that made a Russian window ask its questions in
English. Its baseline is empty at the time of writing, and it may only shrink. What it
deliberately does not refuse: a call in JSX, in a nested handler, in a `create*` factory
or in a plainly-named helper, because those run when their result is used — the moment
is the caller's, and a checker that guessed would be switched off rather than fixed.

## The kit cannot localise itself

`frontend/src/ui/` never imports the catalogue (the rule in `ui/README.md`): a catalogue
read inside a kit component freezes at the locale of its first mount, and the kit is
placed, never repainted. Its words arrive as props — `i18n/kit-labels.ts` builds them for
the surfaces — and where a control's name was the kit's own invention, the answer was to
take the invention away rather than to give the kit a language: a disclosure button's
name is the record it discloses, and `aria-expanded` already says open or closed.

## What the next person inherits

- Resolve at render. A `t()` at module scope, in a component body, or in a descriptor
  built at construction is a snapshot, and the symptom is a surface that answers in the
  language the window started in. Hold a key or an accessor.
- Mint in `en` first, in the same commit. A missing `ru` entry is reported and is fine; a
  missing `en` entry is an error.
- Do not build a sentence by concatenating a number to a fixed phrase — that is what
  `tPlural` and `{count}` are for, and Russian has three forms where English has two.
- Do not interpolate with `String.prototype.replace`: a `$&` in somebody's secret name is
  a replacement pattern, and the sentence a person agreed to is not the one on screen.
  `t(key, fallback, params)` is the one interpolator.
