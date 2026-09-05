// ═══════════════════════════════════════════════════════════════════════════
// SkillViewCheck — what a model concluded about this skill, and the button
// that asks for a fresh reading (nocx-dh14q). Fills the placeholder Task 9
// left in skill-view-body.tsx's "THE CHECK" group, replacing it with the
// real thing.
//
// ONE MODEL CALL, AND IT WAITS FOR A PRESS. Opening the tab (and every
// reactivation) reads content.db through `skills.check` — free, and what
// makes "nobody has checked this" a fact rather than a guess. `skills.audit`
// — the one call that spends a model — runs ONLY from this component's own
// button, never from an effect: internal/profile/role.go refuses to spend a
// model call silently, and an effect that fired on mount would be exactly
// that, wearing a different shape. A test asserts `client.audit` is never
// called merely from opening the tab.
//
// A STALE CHECK IS STILL THE CHECK. `current:false` means the stored
// reading's digest no longer matches the bytes on disk — an edit, a
// reinstall — and it is shown IN FULL below, with one sentence above it
// saying so. Hiding it would throw away what the person paid a model for,
// and it would make "never checked" and "checked a while ago" the same
// state on screen, which is the defect skills.check.schema.json's own
// module doc calls out.
//
// THE VERDICT GATES NOTHING HERE EITHER. There is no branch in this file on
// `verdict` — not on ordering, not on which button renders, not on a
// disabled state, not on tone. It is text: attributed to the model that
// wrote it (the model and endpoint travel beside it), alongside the static
// scan's own count, which is OURS and deterministic and drawn as a separate
// sentence — design §3's "two claims of different kinds", never merged into
// one judgement.
//
// THE PROSE IS DATA, NEVER MARKUP. It is a model's account of a document a
// stranger may have written, so it reaches the DOM only through Solid's
// text interpolation (`{report}`, which sets textContent — never
// innerHTML), the same way status-card.tsx's own `description` does. No
// parsing, no anchors, nothing the report's bytes could turn into a live
// element or attribute — a test builds a report containing `<script>` and a
// `javascript:` link and asserts neither survives as markup.
// ═══════════════════════════════════════════════════════════════════════════

import { Show, createEffect, createSignal, on, onCleanup, type JSX } from 'solid-js'
import { Button, Caption, StatusCard } from '../ui'
import type { SkillsStore } from '../skills-store'
import type { SkillsCheck } from '../generated/skills.check'

export interface SkillViewCheckProps {
  /** The RESOLVED skill's name — see skill-view-content.tsx's module comment
   *  for why this is never the requested one. */
  name: string
  store: SkillsStore
  /** Bumped by SkillViewContent on every `setVisible(true)` — this panel
   *  re-reads the stored check on the same schedule skill-view-body.tsx's
   *  manifest and scan already keep (its own module comment), so a
   *  long-lived tab does not go on showing a check a second window has
   *  since replaced or superseded. */
  refreshToken: number
}

type Omission = { path: string; reason: 'too-large' | 'not-text' | 'budget-spent' | 'unreadable' }
type Finding = { path: string; patternId: string; line: string; lineNumber: number }

/** One reading, whichever call produced it — `skills.check`'s stored
 *  `check` object, or a fresh `skills.audit` result reshaped onto the same
 *  fields. Kept as one type so the render below draws from ONE shape,
 *  never two that could drift apart on what a "reading" contains. */
interface Reading {
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
   *  the time the write was attempted, and the report stays on screen with
   *  a sentence saying it did not stick (design §6, "a stub database is a
   *  visible state, not a degrade"). */
  storedNote: string
}

type CheckState =
  | { kind: 'loading' }
  | { kind: 'unavailable'; message: string }
  | { kind: 'none' }
  | { kind: 'ready'; reading: Reading; current: boolean }

const messageOf = (err: unknown): string => (err instanceof Error ? err.message : String(err))

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

/** `Suspect — gemma-4-26b-a4b · local · 4 Sep` (design §2, §4): the verdict
 *  word, capitalised, then the three facts a reader needs before weighing
 *  it at all — which model, which endpoint, when. */
function verdictLine(reading: Reading): string {
  const verdict = reading.verdict === 'suspect' ? 'Suspect' : 'Clear'
  return `${verdict} — ${reading.model} · ${reading.endpoint} · ${shortDate(reading.checkedAt)}`
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
 *  of different kinds", restated as the one sentence carrying the scan's
 *  half. Zero matches carries its own caveat rather than reading as an
 *  all-clear: the scan is a fixed set of known phrasings, and a file none
 *  of them hit is a file they had nothing to say about, not one anything
 *  vouched for — the surviving caveat design §3 keeps. */
function scanSentence(findings: readonly Finding[]): string {
  if (findings.length === 0) {
    return 'The static scan matched nothing in these files. That is not the same as safe: the scan looks for a fixed set of known phrasings, so files it matched nothing in are files it had nothing to say about.'
  }
  const files = [...new Set(findings.map((f) => f.path))]
  const count = findings.length
  return `The static scan matched ${count} line${count === 1 ? '' : 's'}, in ${joinWithAnd(files)}.`
}

/** `skills.check`'s stored `check`, reshaped into `Reading` — the same
 *  shape a fresh `skills.audit` produces below, so the render draws from
 *  one type regardless of which call answered. */
function readingFromCheck(check: NonNullable<SkillsCheck['check']>): Reading {
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

export function SkillViewCheck(props: SkillViewCheckProps): JSX.Element {
  const [state, setState] = createSignal<CheckState>({ kind: 'loading' })
  const [auditing, setAuditing] = createSignal(false)
  const [auditError, setAuditError] = createSignal('')

  let disposed = false
  let generation = 0

  /** The only place `skills.check` is called — never `skills.audit`, which
   *  is the button below's alone. */
  const loadCheck = async (): Promise<void> => {
    const asked = ++generation
    try {
      const result = await props.store.check(props.name)
      if (disposed || asked !== generation) return
      if (!result.checked || result.check === undefined) {
        setState({ kind: 'none' })
      } else {
        setState({
          kind: 'ready',
          reading: readingFromCheck(result.check),
          current: result.current ?? true,
        })
      }
    } catch (err) {
      if (disposed || asked !== generation) return
      setState({ kind: 'unavailable', message: messageOf(err) })
    }
  }

  // On mount, and again on every `refreshToken` change (every tab
  // reactivation) — the same schedule skill-view-body.tsx's manifest and
  // scan already keep, for the same reason: a tab "lives for days" and must
  // not go on showing a check a second window has since replaced.
  createEffect(
    on(
      () => props.refreshToken,
      () => void loadCheck(),
    ),
  )

  onCleanup(() => {
    disposed = true
  })

  /** THE ONLY PLACE `skills.audit` IS CALLED — from a press, never an
   *  effect. The `auditing` guard at the top refuses a second press while
   *  one is in flight even if the disabled attribute has not yet painted
   *  (the signal write and the DOM update are not the same instant), so
   *  "exactly once per press" holds whichever race a test catches it in. */
  const runAudit = async (): Promise<void> => {
    if (auditing()) return
    setAuditing(true)
    setAuditError('')
    try {
      const result = await props.store.audit(props.name)
      if (disposed) return
      setState({
        kind: 'ready',
        // Just produced, from the bytes as they are right now.
        current: true,
        reading: {
          verdict: result.verdict,
          report: result.report,
          role: result.role,
          endpoint: result.endpoint,
          model: result.model,
          // skills.audit carries no timestamp of its own — only a STORED
          // check does — and this reading was made this instant, so that
          // is what the line says.
          checkedAt: new Date().toISOString(),
          omitted: result.omitted,
          findings: result.findings,
          storedNote:
            result.stored === 'no' ? (result.storedError ?? 'This reading was not saved.') : '',
        },
      })
    } catch (err) {
      if (disposed) return
      setAuditError(messageOf(err))
    } finally {
      if (!disposed) setAuditing(false)
    }
  }

  const unavailableMessage = (): string => {
    const held = state()
    return held.kind === 'unavailable' ? held.message : ''
  }

  const readyResult = (): { reading: Reading; current: boolean } | null => {
    const held = state()
    return held.kind === 'ready' ? held : null
  }

  return (
    <div class="skill-view__check">
      <Show when={state().kind === 'loading'}>
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
      <Show when={state().kind === 'none'}>
        <Button onClick={() => void runAudit()} disabled={auditing()}>
          Check this skill
        </Button>
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
            <p>{scanSentence(held().reading.findings)}</p>
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
            <Button onClick={() => void runAudit()} disabled={auditing()}>
              Re-check
            </Button>
          </>
        )}
      </Show>
      <Show when={auditing()}>
        <StatusCard
          tone="neutral"
          title="Reading this skill"
          description={`A model is reading the files of “${props.name}” and writing a description of them.`}
        />
      </Show>
      <Show when={auditError()}>
        <StatusCard tone="danger" title="This skill was not read" description={auditError()} />
      </Show>
    </div>
  )
}
