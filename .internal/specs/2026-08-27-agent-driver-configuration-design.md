# Per-agent driver configuration — the user describes an agent, and repairs one without waiting for an update

- **Date:** 2026-08-27
- **Session bead:** `nocx-khgcp`, discovered from `nocx-dg51h`
- **Status:** approved by the owner in session, 2026-08-27

## 1. What this decides, and what those documents already decided

`.internal/specs/2026-08-15-workspaces-lineage-and-orchestration-design.md`
**D12** decided that detection rules are "**local, user-editable settings with
shipped defaults, switchable off per agent, and accompanied by a live 'what is
this agent emitting' view**", against herdr's network manifest catalogue, which
is a network dependency for correct behaviour and so against `vision.md`'s "no
cloud, ever". It adds: "the emitting-view is **not** optional: a rule the user
must write blind is a dead rule." Its §5 separately decided that "**launch
configuration is data, per agent** (termic's shape)".

`nocx-szb40` cited D12 and carried only its network half into "deliberately
out". The local, user-editable half was neither included nor excluded — it was
not mentioned in any bead, and so was never built. This document is that half.

**The AD-6 amendment** (`docs/architecture.md:151`) permits a live VT grid in
the backend for an **enrolled** pane and grants it exactly two powers, both
positional. **ADR-0041** pins `charmbracelet/x/vt` as the emulator precisely
because it reproduces xterm.js's **column geometry** — "a chrome anchor is a
thing at a position". Nothing here widens either: a user-authored rule reads
the same `panegrid.Frame` under the same interval, and gains no power the
shipped driver does not have.

**AD-8** and AGENTS.md's "look for the existing answer before you write a
second one" decide the shape of the answer: not a Go classifier plus a separate
user-rule engine, but **one grammar with two authors**. The shipped Claude
driver becomes the first document written in it.

**ADR-0024** owns the enrolment channel; **ADR-0028** owns our own agent loop.
Neither changes.

## 2. The problem, measured

`internal/agentdriver` is a compile-time Go registry with one implementation,
`claude.go`, whose rules were read off a real PTY corpus. Measured 2026-08-27,
that driver is **seven literals inside a positional skeleton**:

| literal                   | what it anchors                                            |
| ------------------------- | ---------------------------------------------------------- |
| `❯`                       | the input marker, and the selected option's glyph          |
| `─`                       | the full-width rule bounding the input box                 |
| `Do you want to`          | separates the agent's question from a menu the user opened |
| `… (` … `)`               | the live-spinner grammar                                   |
| `●` / `◯`                 | the task-panel bullets under the mode line                 |
| `/tasks to see subagents` | the mode-line segment for a backgrounded agent             |
| digits `.` space          | the numbered-option grammar                                |

Everything else is frame arithmetic: the cursor's cell, column-exact
full-width rules, regions computed relative to a floating box, a capped status
stack, and branch ordering. **The literals are what an agent's next release
breaks. The arithmetic is what keeps the driver honest.** A user who can edit
the first without touching the second repairs a broken driver in a minute and
cannot lift a safety cap while doing it.

termic (`~/repos/termic`, `src/lib/agents.ts`, `types.ts`) keeps an editable
`Agent[]` registry in Settings, with hard-coded fallbacks used only before the
registry loads. Its detection is `signals: {busy, idle, attention}` — regex
sources tested against the **OSC 0/2 title**, plus an opt-in tier that also
scans stdout lines. In this repository's provenance table that is
`declared-anonymous`, the row marked "may wake your phone: **no by default**",
because a file containing `ESC]0;Action Required BEL` would push a notification
from a `cat`. nocx already has that source (`agent-status.ts`) and already
draws it weaker. **So termic's registry shape transfers; termic's detection
shape would be a step backwards, and the owner asked for a step forwards.**

## 3. The state model: three facets, not one enum

The owner enumerated the states that matter: waiting on a subagent/MCP/shell;
actively working; claiming to work while nothing changes; blocked by a
question, a permission request or the TUI's own error; and idle.

These have **three different sources and three different update moments**, so
they are three fields. D11 and §5.2 of the 2026-08-15 spec already decided
this ("facets are separate, not merged"), and the reason bites here: "working"
and "stalled" is a **conjunction**, not a choice. Forced into one enum, a
stalled worker leaves `working`, and a coordinator asking "who is working"
stops seeing the worker that most needs it.

| facet    | source                                               | values                                                                                                       |
| -------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| liveness | the process                                          | `running` · `exited`                                                                                         |
| screen   | one `panegrid.Frame`, via the rule                   | `idle` · `working` (optional refinement `waiting-on-child`) · `asks-you` · `menu-open` · `error` · `unknown` |
| progress | a sequence of frames plus a clock, via the same rule | `moving` · `stalled`                                                                                         |

Notes that are load-bearing:

- **`unknown` is not a state of the agent. It is a state of our knowledge**, and
  every consumer treats it as busy, so the failure direction is a refusal to
  type. It may never be dropped or defaulted away.
- **`asks-you` and `menu-open` stay separate** because which menu it is decides
  whether answering it answers the **agent**. `/model` is not the agent asking.
  A person may be shown both in one colour; the caller that types may not.
- **`error` means the TUI's own error** — API unavailable, overloaded, no
  connection, quota exhausted — which the agent draws as chrome. It earns its
  own value because `unknown` would fold it into busy, and busy tells the person
  "leave it alone" when the truth is "come here, this will not resolve itself".
  There is nothing to answer, so it is not `asks-you` either.
- **An error the agent printed into the transcript and then went back to waiting
  is `idle`**, and deliberately so: on the screen it is indistinguishable from
  idle because it _is_ idle — the agent finished, badly, and is waiting for you.
  Telling the two apart requires reading meaning, which no driver does.
- **`stalled` is not "the frame did not change"**: a hung agent's spinner keeps
  spinning. The predicate is that the **transcript** stopped growing, and which
  region is transcript is something only the rule knows. So progress is a second
  question asked of the same rule, not an independent timer.

## 4. The rule: a closed grammar with two authors

A rule is a document. It names an agent, and for each screen value gives an
ordered list of branches; each branch is a conjunction of **named predicates**,
every one of them implemented in Go.

The predicate set is closed. A user composes predicates; a user cannot invent
one, and cannot lift a bound that a predicate enforces:

- `cursor_on(glyph)` — the cursor's own cell carries this text. **This is the
  predicate an agent cannot forge**: printed text cannot take the cursor,
  because the TUI parks it after every repaint.
- `row_opens_with(glyph)` — the first non-blank column of a row is this cell.
- `full_width_rule()` — a row that is nothing but one repeated cell, edge to
  edge.
- `between_rules()` — the anchor sits between the two nearest full-width rules.
- `region(anchor, direction, max_rows, col0_only)` — a search region computed
  from an anchor rather than from a fixed row. `max_rows` is a **cap the engine
  owns**: it is what stops an agent whose printed lines abut the chrome from
  extending the region a forged marker would be looked for in.
- `matches(pattern)` / `contains(text)` — the grammar applied _inside_ a region
  that geometry already narrowed.
- `nearest_nonblank_above(anchor) contains(text)`.
- `below_mode_line_opens_only_with(glyphs)` — anything else down there is
  chrome the rule has not seen, and answers `unknown`.

Branch **order** is part of the document, and is a safety property, not a
style: the dialog branches are evaluated before the free-text branch, so a
dialog can never be masked by an input box drawn beneath it.

**`claude.go` is re-expressed as the first document in this grammar, against
its existing corpus tests unchanged.** That rewrite is the grammar's proof: a
grammar that cannot express the one driver we know is correct is not ready to
be offered to anybody else. If a claude rule cannot be written without a new
predicate, the predicate is missing — that is the signal to add it, deliberately.

**Precedence.** Shipped rules are the default and work out of the box. A user
rule for an agent replaces the shipped one for that agent entirely — not a
merge, because a half-overridden rule is two owners of one decision. A user may
also switch an agent's detection off, which is not the same as deleting the
rule: off means the pane reports `unknown`, i.e. busy.

## 5. Calibration: the person reproduces the screen, and the frames are labelled

D12's emitting-view is not a passive log. `cmd/agent-capture` already records a
real PTY to JSONL and replays it to any moment (`nocx-szb40.7`), and that turns
the view into a **guided calibration**:

1. nocx asks the person to drive their agent into a named state — "now let it
   ask you for permission", "now let it work", "now leave it waiting for input".
2. The frame at that moment is captured **with the label**.
3. From the differences between labelled frames — where the cursor sits, what
   opens a row, what appeared and what vanished — nocx **proposes a draft rule**
   and shows it beside the frames it was derived from.
4. The person edits the draft. The proposal is a convenience and may be wrong;
   trust does not come from it.
5. **Verification is the gate**: the rule is replayed against every labelled
   frame, and each must classify to its label.

**A rule that has not classified every labelled state correctly may light the
indicator, and may not gate typing into a pane.** That is the concrete answer
to the failure `nocx-szb40.3` names: a mistimed keystroke does not fail to
arrive, it answers whatever modal is on screen, and can approve a tool call the
person never saw.

Required labels are the three a person can produce on demand: **idle**,
**working**, **asks-you**. `exited` is read from the process, not the screen.
`menu-open`, `error` and `waiting-on-child` are optional: uncalibrated, they
fall to `unknown`, which is busy — a refusal, not a wrong answer.

## 6. Where the rules live

Local files under the app directory (`internal/storage/appdir.go`, so a dev
stand keeps its own), one document per agent, editable from Settings → Agents
and readable by hand. Shipped defaults are embedded in the binary and are not
written to disk, so an upgrade improves an agent the user never touched, and
never silently rewrites one they did.

The Settings surface per agent: enable/disable, the rule document, the
calibration action, the live emitting view, and the verification verdict with
its consequence stated ("verified against 3 of 3 labelled states — may type"
/ "not verified — indicator only").

## 7. Deliberately out

- **Launch configuration** (§5's termic shape: command, args with placeholders,
  YOLO args, `--session-id`/`--resume`, `--name`, environment). It is a real
  deliverable and gets its own epic; it shares the registry and nothing else.
- **The acknowledgement facet** — filled vs hollow dot for a finished worker
  you have already looked at. Filed as `nocx-s6hj2`; independent of who wrote
  the rule.
- A network catalogue or any fetch of rules (D12, and `nocx-szb40` already).
- Reading meaning out of the transcript. Every predicate above is about chrome
  and geometry.
- The attention queue, phone notifications, and delegation.

## 8. Testing

- The grammar's evaluator is exercised against `internal/agentdriver/testdata/captures`
  — the corpus that made the Claude driver right — and the re-expressed claude
  rule must pass those tests **unchanged**.
- For each predicate: a test that it matches, and a test that it refuses the
  near-miss it exists to refuse (`cursor_on` against text printed into the
  transcript; `region` against an agent trying to extend it).
- Calibration end to end: labelled frames in, a proposed rule out, verification
  green, and the same rule failing verification when one label is changed.
- The epic's happy path (AGENTS.md rule 2): a person calibrates an agent nocx
  ships no driver for, and afterwards the tab shows its three states correctly.
