// ═══════════════════════════════════════════════════════════════════════════
// SkillViewCheckPanel — what a model concluded about this skill, drawn in
// the RIGHT PANE exactly the way a file is (nocx-dh14q, review round 2).
//
// PRESENTATIONAL ONLY. The state this panel draws — what `skills.check`
// answered, whether `skills.audit` is in flight, its last failure — is
// owned by skill-view-body.tsx, the same place that already owns the file
// list's manifest/scan/file state: both are "what governs the right pane
// and the left column's rows", and a second owner here would be exactly
// the two-owners defect the file-list dots already avoid. This module
// keeps the pure presentation and the pure helpers (date/sentence
// formatting) that draw from that state, plus `checkRowTitle`, the short
// summary the LEFT COLUMN's row shows, and `readingFromCheck`, the
// reshaping `skill-view-body.tsx`'s own `loadCheck` needs.
//
// WHY THIS MOVED OUT OF THE LEFT COLUMN (review round 2 finding). The first
// shape put the whole panel — verdict, prose, scan sentence — inside "THE
// CHECK" group in the left column, a [180px, 40% of the pane] list column
// shared with Files, inside the SAME `overflow-y:auto` as the file rows.
// The design (`.internal/specs/2026-09-05-the-skill-viewer-design.md
// :140-169`) draws THE CHECK as one ROW in that column and the report in
// the RIGHT PANE at full width, the same way selecting a file does — for
// the reason a 16 KiB-bounded report does not fit a narrow rail, and
// stacking it above Files pushed the tab's own primary navigation below a
// report a person has to scroll past first. That was the modal's original
// complaint — several components stacked in one column — reproduced in a
// narrower column. `skill-view-body.tsx` now treats "the check" as a
// THIRD kind of thing the right pane can show, selected by a `RecordRow`
// beside the file rows, alongside a file.
//
// THE VERDICT GATES NOTHING HERE. There is no branch in this file on
// `verdict` — not on ordering, not on which button renders, not on tone,
// not on a disabled state. It is text: attributed to the model that wrote
// it (the model and endpoint travel beside it), alongside the static
// scan's own count, which is OURS and deterministic and drawn as a
// separate sentence — design §3's "two claims of different kinds", never
// merged into one judgement.
//
// THE PROSE IS DATA, NEVER MARKUP. It is a model's account of a document a
// stranger may have written, so it reaches the DOM only through Solid's
// text interpolation (`{report}`, which sets textContent — never
// innerHTML), the same way status-card.tsx's own `description` does. No
// parsing, no anchors, nothing the report's bytes could turn into a live
// element or attribute.
// ═══════════════════════════════════════════════════════════════════════════

import { Show, type JSX } from 'solid-js'
import { Button, Caption, StatusCard } from '../ui'
import type { SkillsCheck } from '../generated/skills.check'

/** The wire's own shapes, never re-declared by hand (review round 2's
 *  minor: a hand-rolled union is a fourth place these could drift from the
 *  schema they came from). */
type Omission = NonNullable<SkillsCheck['check']>['omitted'][number]
type Finding = NonNullable<SkillsCheck['check']>['findings'][number]

/** One reading, whichever call produced it — `skills.check`'s stored
 *  `check` object, or a fresh `skills.audit` result reshaped onto the same
 *  fields. Kept as one type so the render below draws from ONE shape,
 *  never two that could drift apart on what a "reading" contains. */
export interface Reading {
  verdict: 'clear' | 'suspect'
  report: string
  role: 'auditing' | 'answering'
  endpoint: string
  model: string
  checkedAt: string
  omitted: readonly Omission[]
  findings: readonly Finding[]
  /** '' when the reading is stored, whatever produced it — a check read
   *  back from content.db is stored by definition. Set only by a fresh
   *  audit whose `stored` came back 'no': the model was already billed by
   *  the time the write was attempted, and the report stays on screen
   *  with a sentence saying it did not stick (design §6). */
  storedNote: string
}

export type CheckState =
  | { kind: 'loading' }
  | { kind: 'unavailable'; message: string }
  | { kind: 'none' }
  | { kind: 'ready'; reading: Reading; current: boolean }

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** `4 Sep` — the design's own format (§2's layout diagram). A fixed short
 *  form rather than `toLocaleDateString`, whose day/month ORDER (not only
 *  the month's name) varies by locale and would make this line read
 *  differently on two machines checking the same skill. */
function shortDate(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return `${d.getDate()} ${MONTHS[d.getMonth()]}`
}

const verdictWord = (verdict: Reading['verdict']): string =>
  verdict === 'suspect' ? 'Suspect' : 'Clear'

/** `Suspect — gemma-4-26b-a4b · local · 4 Sep` (design §2, §4): the verdict
 *  word, capitalised, then the three facts a reader needs before weighing
 *  it at all — which model, which endpoint, when. */
function verdictLine(reading: Reading): string {
  return `${verdictWord(reading.verdict)} — ${reading.model} · ${reading.endpoint} · ${shortDate(reading.checkedAt)}`
}

/** `Suspect · 4 Sep` — the LEFT COLUMN row's own summary (review round 2:
 *  "keep the row's summary short — the verdict word and the date are
 *  enough for a row"). Total over `CheckState` so a fifth state fails
 *  this switch's compile rather than leaving the row silently blank. */
export function checkRowTitle(state: CheckState): string {
  switch (state.kind) {
    case 'loading':
      return 'Checking…'
    case 'unavailable':
      return 'Check unavailable'
    case 'none':
      return 'Not checked'
    case 'ready':
      return `${verdictWord(state.reading.verdict)} · ${shortDate(state.reading.checkedAt)}`
  }
}

const REASON_WORDS: Record<Omission['reason'], string> = {
  'too-large': "is larger than one file's read budget",
  'not-text': 'is not text',
  'budget-spent': 'arrived after the reading was already full',
  unreadable: 'could not be opened',
}

function joinWithAnd(items: readonly string[]): string {
  if (items.length <= 1) return items[0] ?? ''
  if (items.length === 2) return `${items[0]} and ${items[1]}`
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}

/** WHAT WAS LEFT OUT, as a sentence — design §4's replacement for the old
 *  MarkerList: the files the model actually read ARE the left column now,
 *  so only the omissions still need saying, and one clause per file names
 *  WHICH bytes the model never saw rather than only that some were. */
function omissionsSentence(omitted: readonly Omission[]): string {
  const clauses = omitted.map((o) => `${o.path} ${REASON_WORDS[o.reason]}`)
  return `Not sent to the model: ${joinWithAnd(clauses)}.`
}

/** THE SCAN'S OWN COUNT, apart from the verdict — design §3's "two claims
 *  of different kinds". PAST TENSE ON A STALE CHECK (review round 2's
 *  minor): a stale reading's scan count is a fact about what was read
 *  THEN (design §4), not a claim about the bytes on disk right now, which
 *  may no longer contain what it matched — or may have grown a match this
 *  sentence never saw. */
function scanCountSentence(findings: readonly Finding[], current: boolean): string {
  if (findings.length === 0) {
    return current
      ? 'The static scan matched nothing in these files.'
      : 'When this reading was made, the static scan matched nothing in these files.'
  }
  const files = [...new Set(findings.map((f) => f.path))]
  const count = findings.length
  const clause = `matched ${count} line${count === 1 ? '' : 's'}, in ${joinWithAnd(files)}.`
  return current
    ? `The static scan ${clause}`
    : `When this reading was made, the static scan ${clause}`
}

/** ABSENCE OF A MATCH IS NOT SAFETY — kept UNCONDITIONALLY (design §3),
 *  not folded into the zero-findings branch the way an earlier version of
 *  this file had it (review round 2's finding: with one match and three
 *  clean files, nothing said those three were unvouched-for). The scan is
 *  a fixed set of known phrasings; a file it matched nothing in is a file
 *  it had nothing to say about, whether or not it said something about a
 *  different file in the same reading. */
const SCAN_CAVEAT =
  'That is not the same as safe: the scan looks for a fixed set of known phrasings, so files it matched nothing in are files it had nothing to say about.'

export interface SkillViewCheckPanelProps {
  name: string
  state: CheckState
  auditing: boolean
  auditError: string
  onRunAudit: () => void
}

export function SkillViewCheckPanel(props: SkillViewCheckPanelProps): JSX.Element {
  const readyResult = (): { reading: Reading; current: boolean } | null =>
    props.state.kind === 'ready' ? props.state : null

  const unavailableMessage = (): string =>
    props.state.kind === 'unavailable' ? props.state.message : ''

  return (
    <div class="skill-view__check">
      <Show when={props.state.kind === 'loading'}>
        <StatusCard
          tone="neutral"
          title="Reading the stored check"
          description={`Reading what content.db knows about “${props.name}”.`}
        />
      </Show>
      <Show when={unavailableMessage()}>
        <StatusCard
          tone="danger"
          title="The stored check could not be read"
          description={unavailableMessage()}
        />
      </Show>
      <Show when={readyResult()}>
        {(held) => (
          <>
            {/* A STALE CHECK IS STILL THE CHECK — shown in full below,
                never trimmed or dimmed. This is the one sentence that says
                it is about earlier bytes (design §5). */}
            <Show when={!held().current}>
              <StatusCard
                tone="warning"
                title="This check is about an earlier version of these files"
                description="Something moved since this reading was made — an edit, a reinstall. It is shown below exactly as it was recorded; re-check to read the bytes as they are now."
              />
            </Show>
            <p class="skill-view__check-verdict">{verdictLine(held().reading)}</p>
            <Caption>
              This is the model's conclusion, not nocx's, and it decides nothing here — a skill's
              own text can address whoever reads it, so read this beside the files rather than
              instead of them.
            </Caption>
            <p class="skill-view__check-report">{held().reading.report}</p>
            <p>{scanCountSentence(held().reading.findings, held().current)}</p>
            <p>{SCAN_CAVEAT}</p>
            <Show when={held().reading.omitted.length > 0}>
              <p>{omissionsSentence(held().reading.omitted)}</p>
            </Show>
            <Show when={held().reading.storedNote}>
              <StatusCard
                tone="warning"
                title="This reading was not saved"
                description={held().reading.storedNote}
              />
            </Show>
          </>
        )}
      </Show>
      {/* ALWAYS EXACTLY ONE BUTTON, IN EVERY STATE (review round 2's
          Important #2). A store READ failure (`unavailable`) must not
          withhold the model call: `skills.check` errors only on a genuine
          store fault (a stub or unwired store answers `checked:false`
          instead), while `skills.audit` would run happily and report
          `stored:'no'`, which design §6 says must still reach the person
          — the model is billed before the write is even attempted. An
          earlier version of this panel rendered no button at all in the
          `unavailable` branch, which withheld a call the backend was
          willing to make. */}
      <Button onClick={props.onRunAudit} disabled={props.auditing}>
        {props.state.kind === 'ready' ? 'Re-check' : 'Check this skill'}
      </Button>
      <Show when={props.auditing}>
        <StatusCard
          tone="neutral"
          title="Reading this skill"
          description={`A model is reading the files of “${props.name}” and writing a description of them.`}
        />
      </Show>
      <Show when={props.auditError}>
        <StatusCard tone="danger" title="This skill was not read" description={props.auditError} />
      </Show>
    </div>
  )
}

/** `skills.check`'s stored `check`, reshaped into `Reading` — the same
 *  shape a fresh `skills.audit` produces, so the render above draws from
 *  one type regardless of which call answered. Exported for
 *  skill-view-body.tsx's own `loadCheck`. */
export function readingFromCheck(check: NonNullable<SkillsCheck['check']>): Reading {
  return {
    verdict: check.verdict,
    report: check.report,
    role: check.role,
    endpoint: check.endpoint,
    model: check.model,
    checkedAt: check.checkedAt,
    omitted: check.omitted,
    findings: check.findings,
    storedNote: '',
  }
}
