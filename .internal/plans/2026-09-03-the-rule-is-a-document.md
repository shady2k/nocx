# The rule is a document, and claude.go is the first one written in it

- **Date:** 2026-09-03
- **Bead:** `nocx-ayc4v`, a child of `nocx-dhq4u`
- **Design:** `.internal/specs/2026-08-27-agent-driver-configuration-design.md` §4
- **Sequenced before:** `nocx-l5lok`, then `nocx-jse6x`

The plan is the brief. A worker takes one task and reads that task's section in full.

## Why this order in the epic

`nocx-l5lok` grows the state model — `error` as its own value, `waiting-on-child` as an optional
refinement of `working`. `nocx-ayc4v` turns the rule into a document while the answer set stays
exactly what it is today. **Document first**, so that growing the model is adding a branch to a
document rather than editing Go a second time. The beads carry no edge between them; this is the
choice, and it is here so it is not re-litigated.

## What the corpus already proved, and what it costs us

The design says: "If a claude rule cannot be written without a new predicate, the predicate is
missing — that is the signal to add it, deliberately." **It fired.** Measured against
`internal/agentdriver/claude.go`, the design's eight-predicate list cannot express seven things
the shipped driver does. Every one of them is in T1's list, and none is optional: the acceptance
criterion is that `testdata/captures` passes **unchanged**, so a predicate set that cannot express
a branch is a set that fails the corpus.

Two of the seven are safety, not convenience:

- **`cell_at(col 0)` is not `row_opens_with`.** The input box requires `❯` at column 0 exactly
  (`claude.go:96-99`); the menu marker requires it at the first non-blank column
  (`claude.go:140-142`). Widening the first into the second is the "widen an existing predicate"
  move the bead forbids, and it would weaken one of the two markers the whole safety argument
  rests on.
- **`below_mode_line_opens_only_with` is three-valued, not boolean** (`claude.go:243-262`): all
  rows match → `working`; a counterexample → **`unknown`**; no rows at all → fall through. A
  boolean collapses two of the three, and the collapse turns a refusal into `free_text`, which is
  the expensive direction.

## The seam, and the two constraints it imposes

Everything goes through `agentdriver.Claude() Driver` (`claude.go:19`) and
`Driver.Classify(panegrid.Frame) State`. The tests live in the external package
`agentdriver_test`, so **no unexported symbol in `claude.go` is under test** and all of them may
be deleted.

1. **`Claude()` takes no arguments and returns no error**, and `agentdriver_test.go:57` calls it
   twice in one expression. The claude document must therefore load infallibly: `go:embed` in
   `internal/agentdriver/`, parsed at construction, with a malformed document a `panic` at process
   start rather than an error return — it is a wiring mistake, which `agentdriver.go:39-41`
   already says belongs to process start. Any signature that can fail forces edits to
   `agentdriver_test.go:12/29/57` and `app.go:1299`, which the acceptance criterion forbids.
2. **`Agent()` answers without a frame** (`claude_test.go:179`), so the agent name is a field of
   the document, read at construction.

`Registry` does not change in this bead. `NewRegistry(agentdriver.Claude())` must keep working
verbatim — `app.go:1299` plus four tests spell it that way.

---

## T1 — The predicate set, decided and implemented

Implement the closed set as Go, one file, each predicate a named type with a `Match` over a frame
and its bound anchors. **This is the whole safety surface: a user composes these and can invent
none.**

The design's eight, kept:

- `cursor_on(glyph)` — the cursor's own cell carries this text. The unforgeable one.
- `row_opens_with(anchor, glyph)` — first non-blank column of the row is this cell.
- `full_width_rule(row)` — every column `0..Cols-1` is this cell exactly. Note it does **not**
  skip `Width == 0` continuation cells, unlike `Frame.Text` — keep that, `claude.go:106-120`
  depends on it.
- `region(anchor, direction, max_rows, col0_only)`.
- `contains(text)` / `matches(pattern)` inside a narrowed region.
- `nearest_nonblank_above(anchor) contains(text)`.
- `below_mode_line_opens_only_with(glyphs)`.
- `between_rules()` — subsumed by T2's binding form; do not implement it separately.

The seven the corpus forces, each with the branch that needs it:

1. **`cell_at(anchor, col, glyph)`** — the box's `❯` at column 0 exactly (`claude.go:96-99`).
   Distinct from `row_opens_with`; see above.
2. **`region` gains `stop_at_blank`** — the spinner region terminates at the first blank row
   (`claude.go:206`), while `nearest_nonblank_above` and the mode-line scan skip blanks
   (`:328`, `:251`). Same shape, opposite semantics, and the spinner is wrong without it:
   `claude-subagent@70000` has `✻ Sautéed for 29s` at `meter-1` with a blank above it.
3. **`row_contains(anchor, text)`** — reading the mode line's own text
   (`claude.go:271-277`, `"/tasks to see subagents"`). Every listed predicate reads rows above or
   below an anchor; none reads the anchor's row.
4. **A three-valued `below_mode_line_opens_only_with`** — see above. Express it as an outcome per
   case in the document rather than as a boolean, so all three stay distinguishable.
5. **`anchor_above(a, b)`, with optional-anchor semantics** — `if hasBox && y >= box.meter`
   (`claude.go:150-152`). Two bound positions compared, and the comparison applies only when the
   anchor bound at all.
6. **`has_suffix(text)`** — `HasSuffix(text, ")")` (`claude.go:210`).
7. **The numbered-option grammar** — `^ *[0-9]+\. ` (`claude.go:162-172`).

**Decide `matches`'s language in this task and write the decision into the document schema.** If
it is a real regex, 6 and 7 fall out of it and only 1–5 are new. If it is glob or literal, 6 and 7
are two more predicates. Recommendation: **RE2 via `regexp`**, because Go's regexp has no
catastrophic backtracking and a user-authored pattern is untrusted input; then state that the
pattern applies to one of three explicit row renderings — `Frame.Text(y)`, that right-trimmed, or
the row rendered from a column onward and right-trimmed — because `claude.go` uses all three and a
predicate that leaves it implicit is ambiguous.

**Tests.** For every predicate: one that it matches, and one that it refuses the near-miss it
exists to refuse. Specifically `cursor_on` must refuse the same glyph printed into the transcript
(the corpus already has the technique — `claude_test.go:121-141` feeds extra bytes with `ESC 7` /
`ESC 8` so the cursor is not moved), and `region` must refuse an agent whose printed lines abut the
chrome and would extend the region past `max_rows`.

## T2 — Anchor binding

Nothing in the design's list says how `box.meter`, `box.prompt`, `box.bottomRule` or the mode line
come to exist, and every predicate takes an anchor. **This task is what the rest rests on.**

The form: search for a row satisfying a predicate, in a direction, from a starting row, with an
optional cap; bind the result under a name; derive further names by fixed offsets. It must express
`claude.go:75-101` exactly, including the parts that look like details and are not:

- `bottomRule` — last `full_width_rule`, scanning **up** from `Rows-1`; reject if `< 2`.
- `topRule` — next `full_width_rule`, scanning **up** from `bottomRule-2` down to row 1 inclusive;
  reject if `< 1`. The `-2` start is what forces at least one row between the rules; the `>= 1`
  floor is what makes `meter = top-1` always valid.
- `prompt = topRule + 1`, and `cell_at(prompt, 0) == "❯"` or the binding fails.
- `meter = topRule - 1`.

A binding that fails is not an error and not `false` — it is an **absent anchor**, and T1's
predicate 5 depends on being able to ask whether it bound.

**Decide the cap question here, deliberately.** The bead's falsifier says a document may not
express a region without a cap, and two of today's scans are uncapped: `nearestNonBlankAbove`
walks to row 0 (`claude.go:326-333`) and the mode-line scan walks to `f.Rows` (`:249`). For the
latter the frame's own bottom edge is a defensible engine-owned cap. For the former a cap is a
real behaviour change — in the corpus the answer is always within two rows (permission 26→25,
modal 31→29 with one blank skipped), so a small cap passes, but **that is a decision, not a
detail**. Write which you chose and why into the document schema's comment.

## T3 — The evaluator

Document in, `State` out. An ordered list of branches per screen value; each branch a conjunction
of T1 predicates over T2 anchors; first match wins.

**Branch order is a safety property and the evaluator must preserve document order exactly.** The
dialog branches evaluate before the free-text branch so a dialog can never be masked by an input
box drawn beneath it (`claude.go:23-27`).

The evaluator holds no state between frames — `agentdriver.go:102-104`, "a rule that remembers is
a rule that can be stuck". A `Classify` that reads anything other than the frame it was handed is
this task failing.

Two engine-owned behaviours that are not branches and must not be expressible in a document:
the degenerate-frame refusal (`Rows <= 0 || Cols <= 0 || len(Lines) == 0` → `unknown`,
`claude.go:29-31`), and the final default when no branch matches. Today's default is `free_text`
(`claude.go:54`); **make the default a field of the document**, because `l5lok` and every
user-authored rule need it and a hardcoded `free_text` is the wrong direction for a rule the
engine does not understand.

## T4 — The claude document, and `Claude()` re-expressed

Write the seven branches of `claude.go` as the document, embedded with `go:embed`. In evaluation
order: the menu branch (permission vs modal by `nearest_nonblank_above` containing
`"Do you want to"`); the no-box-no-menu refusal; the live spinner; the task panel; the
under-something-else refusal; the mode-line background-agent branch; the `free_text` default.

`Claude()` becomes a document driver over that embedded document, same signature.

**The acceptance criterion is the whole point: `testdata/captures` tests pass UNCHANGED — not
adjusted, not re-recorded.** Eight captures × 71 moments, plus the subagent interval's two ends,
plus the forgery test, plus the two synthetic marker/rules tests. If a capture needs its expected
value changed, the document is wrong or the grammar is missing a predicate; the corpus is not
negotiable, and that is what makes this the grammar's proof rather than its first customer.

## T5 — Delete what the document replaced

`claudeInputBox`, `claudeMenu`, `claudeSpinnerIsLive`, `claudeUnderTheModeLine`,
`claudeIsFullWidthRule`, `claudeIsNumberedOption`, `underMode` and the six frame-arithmetic
helpers go, unless the predicate implementations genuinely reuse them — in which case they move
into the predicate file and stop being claude-specific. No unexported symbol here is under test,
so nothing outside the package notices.

`deadcode -tags gtk3 -whylive` on the new evaluator's entry point, contrasted against one symbol
that should be dead. AGENTS.md is explicit that `-filter` cannot report a dead method behind a
live interface, which is exactly this package's shape.

---

## What this plan does not cover

- **`nocx-l5lok`** — the grown state model. After T4 it is branches plus one new value, and it
  gets its own tasks.
- **`nocx-jse6x`** — typing authority earned by the labelled set. It consumes this grammar and
  does not change it.
- **User-authored documents loaded from disk** (`nocx-y6w66`). That wants a parse that can fail
  and a registry built from a document set — a second constructor **beside** `NewRegistry`, never
  a change to it. Three of `NewRegistry`'s reasons to exist (nil, unnamed, duplicate) generalise
  to a malformed document set cleanly, and two properties must survive: a malformed document is a
  wiring mistake belonging to process start, and an agent with no document still answers
  `unknown` rather than an error.
- **`nocx-tnx44`'s progress facet.** It needs a frame SEQUENCE and a clock, and the driver
  contract forbids state between frames while `panegrid.Cell` carries no styling and no
  changed-since flag. Filed as `nocx-i2pwl`; do not let it leak into this grammar.
