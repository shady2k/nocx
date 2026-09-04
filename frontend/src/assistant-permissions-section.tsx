/**
 * Assistant permissions — the Settings page where a person reads the answers
 * they have given about what the assistant may do, and takes any of them back
 * (nocx-hvb3r, design `.internal/specs/2026-09-03-assistant-permissions-design.md`).
 *
 * WHAT THIS REPLACED, AND WHY IT HAD TO GO. The page before it drew the
 * storage: seven rows of effect class by resource scope, each with a decision
 * select and a free-form scope field. Every control was correct and the whole
 * was unreadable — to predict what any one of them did, a person had to run
 * the evaluator in their head. The owner's verdict, twice: «Я вообще не
 * понимаю как это все работает и как настраивать». Three layouts were offered
 * for the same model and all three were rejected, which is the evidence that
 * the defect was never layout.
 *
 * THE UNIT IS A SENTENCE ABOUT A FUTURE QUESTION, not a field of the model.
 * Nobody arrives wanting to configure `observe x path x permit`; they arrive
 * having met a concrete request and wanting to decide whether it should be
 * asked again. So the page has two lists:
 *
 *   WHAT YOU HAVE ANSWERED   every standing answer, and every row off its
 *                            default, each as a sentence
 *                            -> Why . <the other answer> . Forget
 *   NOT ANSWERED YET         the live rows still on ask, named as the
 *                            questions they are
 *                            -> Allowed . Never
 *
 *   + Write a refusal        + Allow a command…
 *
 * SEVEN RULES ARE BUILT IN RATHER THAN ASSERTED IN PROSE.
 *
 * - **The store decides.** There is no Save button and no draft: every gesture
 *   writes ONE object and the page adopts what a fresh read answers. It can
 *   never show a permission the store did not take, and a write the store
 *   turns down leaves nothing behind to lose. The page before this kept a
 *   `draft` matrix beside its `wire` copy; that is gone.
 * - **One object per gesture.** The approval prompt writes rules while this
 *   page is open, so a whole-document write from here would race it — and that
 *   bug has already shipped once (nocx-39bly: a matrix save deleted every
 *   standing answer a person had approved). A rule gesture is `setRule` or
 *   `forgetRule`, by id. A row gesture is `policy.set`, which carries the
 *   matrix and CANNOT name a rule — the client's type has no key for one and
 *   the backend turns down a document that does.
 * - **`live` and `awaitingReview` are the backend's answers and only the
 *   backend's.** Which rows govern anything would mean mapping a tool name to
 *   an effect, and which answers are inert would mean comparing the reading of
 *   commands running now — the one thing no configuration path may do
 *   (ADR-0028 decision 4) and the one thing no renderer may decide (AD-8).
 * - **`Why` is READ, never built.** The precedence order has one
 *   implementation, `content.EvaluateInvocation`, and `policy.explain` carries
 *   the steps it actually took. This module renders those steps in the order
 *   they arrived and derives none of them;
 *   `policy-explain-is-not-reimplemented.test.ts` is what keeps that a rule
 *   rather than an intention.
 * - **A PERMIT IS WIDENED FROM A COMMAND THAT WAS READ, NEVER FROM ONE THAT
 *   WAS TYPED** (nocx-fl0o3). The two controls at the foot of the page are not
 *   mirror images, and the asymmetry is the design. A REFUSAL may be written
 *   over any command the backend can resolve: the worst a wrong one does is
 *   stop something. A PERMIT may not be typed at all — a person entering
 *   `find` has no way to know that `find . -delete` is the same word — so the
 *   only route to one is: type a command, have `policy.classify` READ it (it is
 *   never run), be shown what the resulting answer would and would not cover,
 *   and only then save a rule carrying the effect that reading found. That
 *   effect rides in `grantedUnder`, where `content.EvaluateInvocation` checks
 *   it against what a CALL classified as, so the permit does not follow the
 *   same word into something more serious.
 *
 * - **AN ANSWER IS GIVEN WHERE THE QUESTION IS ASKED, AND THERE ARE TWO OF
 *   THEM** (nocx-6szvl). "Ask every time" is not a third answer; it is the
 *   state of a question nobody has answered. Offered as a button it changes
 *   nothing and reads as Cancel — a person picked it, came out, and concluded
 *   the page had not taken their answer — and on an answered row it is Forget
 *   under a second name, minus the preview of what it releases. So a row
 *   carries its own answers, taking one back has one name, and the panel
 *   survives only where it holds what a row cannot: a row's PLACES, and a
 *   rule's reason for withholding its widest answer.
 *
 * - **REVOCATION HAS A TIME, AND THE PERSON CHOOSES IT** (nocx-r4fh8). What
 *   the assistant may do is fixed when it starts writing an answer, so taking
 *   a permission back does not reach an answer already being written. That is
 *   correct — it is what makes the permission a grant rather than a setting
 *   read afresh mid-call — and being silent about it is not: a person forgets
 *   an answer, believes the assistant can no longer do the thing, and
 *   something started thirty seconds ago goes on doing it. So a rule write
 *   that would leave live work behind writes NOTHING, says how many, and
 *   offers the two answers — leave them running, or stop them. The count is
 *   the backend's for `live`'s reason one step further: which answers in
 *   flight are still deciding under a permission is a question about each
 *   one's own frozen authority, and this page has neither the registry nor the
 *   evaluator to ask it.
 *
 * THE VOCABULARY. Three words are absent by construction — "effect class",
 * "resource scope" and the wire's word for a hard no — and no control names a
 * tool. What a person says instead: an ANSWER (Allowed, Ask every time,
 * Never), a PLACE (the folder, the address, the session a decision applies
 * in), and a QUESTION (what has not been answered yet). The evaluator's own
 * `detail` prose is deliberately NOT rendered: it is written in the vocabulary
 * this page exists to keep off the surface, and every step's typed fields
 * already carry what a person needs.
 *
 * PLACES ARE PICKED, NEVER TYPED. The place picker offers the places nocx
 * already knows — the ones the policy itself names — and there is no
 * free-form field and no kind select. A place is LEARNED, not invented: it
 * enters the policy when a person widens an answer at the prompt to cover a
 * resource a call actually named (design §5.3). That is the same argument
 * `+ Allow a command…` rests on — a permit cannot be typed from nothing,
 * because nothing would classify it.
 */
import { createMemo, createSignal, For, onMount, Show } from 'solid-js'
import {
  EFFECT_KEYS,
  type EffectKey,
  type PolicyClassification,
  type PolicyClient,
  type PolicyExplanation,
  type PolicyExplanationStep,
  type PolicyMatrix,
  type PolicyRow,
  type PolicyRule,
  type PolicyRuleWrite,
  type PolicyView,
  type PolicyWriteRuns,
  type RunsTiming,
} from './policy-client'
import type { Scope } from './generated/policy.get'
import { EFFECT_LABEL } from './effect-labels'
import { approvalScopeCoverage } from './agent-approval-prompt'
import {
  ActionGroup,
  Badge,
  Button,
  Caption,
  Checkbox,
  FactList,
  PageSection,
  RecordRow,
  Section,
  Stack,
  TextField,
  showToast,
  type BadgeTone,
  type Fact,
} from './ui'
import { Dialog } from './ui/dialog'
import { Spinner } from './ui/spinner'
import { formatTimestamp } from './ui/format-time'

type Decision = PolicyRow['decision']

/** What each answer means to the person giving it. The wire's three words are
 *  the model's; these are the product's, and they are the same three the page
 *  before this used — a person who learned them there has not had them
 *  moved. */
const ANSWER_LABEL: Record<Decision, string> = {
  permit: 'Allowed',
  ask: 'Ask every time',
  refuse: 'Never',
}

/**
 * The answers a person can GIVE, widest first.
 *
 * There are two, and the absence of the third is the design (nocx-6szvl).
 * "Ask every time" is not a third answer — it IS the state of a question
 * nobody has answered. Offered on an unanswered row it is a button that
 * changes nothing and is indistinguishable from Cancel, which is exactly what
 * a person reported: they went in, left it on "ask", came out, and concluded
 * the page had not taken their answer. Offered on an answered one it is
 * Forget under a second name — the same act, minus the preview of what it
 * releases — and two names for one concept is what AD-8 exists to prevent.
 *
 * So taking an answer back has one name and one code path, and it is Forget.
 * `ANSWER_LABEL` still spells "Ask every time", because the page has to LABEL
 * that state; nothing writes it.
 */
const ANSWER_ORDER: Decision[] = ['permit', 'refuse']

/** The tone an answer carries in a list. `permit` is the only one that gives
 *  anything away, so it is the only one that is not quiet. */
const ANSWER_TONE: Record<Decision, BadgeTone> = {
  permit: 'warning',
  ask: 'neutral',
  refuse: 'neutral',
}

/**
 * The control that widens an answer from nothing, named exactly as its button
 * reads.
 *
 * It is written once because the page has to POINT at it — a Change panel that
 * cannot offer Allowed says where that answer comes from — and a sentence
 * naming the control by any other words points at something a person cannot
 * find on the screen in front of them.
 */
const ALLOW_A_COMMAND = '+ Allow a command…'

/**
 * What each writing panel says about itself, and the ONE place either sentence
 * is written.
 *
 * The allow one is said twice: over the box in its own panel, and over a
 * standing answer that cannot be widened where it stands, which has to explain
 * what can. Both are the same fact about the same gesture, so they are the
 * same string — a second spelling of one concept is what AD-8 exists to
 * prevent, and this page has paid for it twice already.
 */
const WRITER_CAPTION: Record<'allow' | 'refuse', string> = {
  allow:
    'Type a command you would rather not be asked about. nocx reads it — it is never run — and shows what allowing it would cover before anything is saved.',
  refuse:
    'Type a command the assistant should not run on its own. nocx reads it — it is never run — so the answer covers what the command does rather than the way it was typed.',
}

/** The classifier's closed feature vocabulary, in words. The wire declares the
 *  keys; this is the sentence a person reads for each. */
const FEATURE_WORDS: Record<'writes-option-named-path', string> = {
  'writes-option-named-path': 'writes a file to a path named by one of its own options',
}

/**
 * What one step of an explanation says, in the product's words.
 *
 * Chosen by the step's `kind` and filled from its typed fields — the page
 * SWITCHES on the vocabulary and never adds to it. The evaluator's own
 * `detail` string is not rendered: it is the backend's prose about rows,
 * rules and hard noes, and this surface may not speak that vocabulary.
 */
function stepSentence(step: PolicyExplanationStep): string {
  const row = step.effect ? `“${EFFECT_LABEL[step.effect]}”` : 'the matching answer'
  const decided = step.decision ? ANSWER_LABEL[step.decision] : ''
  switch (step.kind) {
    case 'unparsed':
      return 'The command could not be read, so nothing you have answered could speak about it. It is asked.'
    case 'effect-row':
      return `Your answer for ${row} was read first: ${decided}.`
    case 'row-refuses':
      return `${row} is set to Never, and a standing answer is an exception to that layer alone — so no standing answer was read.`
    case 'disqualified':
      return `The command uses a shell feature whose outcome cannot be worked out, so it takes the answer for ${row} above and no standing answer was read.`
    case 'rule-matched':
      return `A standing answer covers this call: ${decided}.`
    case 'rule-stale':
      return 'A standing answer covers this call but is waiting for you to read what it now means, so it did not count.'
    case 'rule-other-effect':
      return `A standing answer covers this call but was given for ${row}, so it does not reach it.`
    case 'resource-inside':
      return 'Every place this call touches is one your answer already covers.'
    case 'resource-outside-fence':
      return 'This call touches a place outside a bound no answer of yours can move.'
    case 'resource-outside-row-scope':
      return 'This call touches a place this answer does not cover yet. Adding that place would let the same call run.'
    case 'resource-not-reached':
      return 'The answer was already No, and looking at places can only narrow an answer, so no place was compared.'
  }
}

/** A place, as a person picks it: the scope the policy states, plus the words
 *  it is offered under. */
interface Place {
  /** Identity across rows — kind and id together, never the id alone. */
  key: string
  scope: Scope
  name: string
}

/** What nocx calls a place. A session's id is an internal handle and stays off
 *  the surface (the rule the approval prompt follows); everything else is
 *  already the person's own word for it. */
function placeName(scope: Scope): string {
  switch (scope.kind) {
    case 'path':
      return scope.id
    case 'destination':
      return scope.id === '*' ? 'anywhere on the network' : scope.id
    case 'session':
      return 'a terminal session'
    case 'environment':
      return `the ${scope.id} environment`
    case 'credential':
      return `the ${scope.id} credential`
  }
}

function placeKey(scope: Scope): string {
  return `${scope.kind} ${scope.id}`
}

/** A row is on its default when it decides ask and bounds nothing. Anything
 *  else is an answer somebody gave and can take back. */
function offDefault(row: PolicyRow): boolean {
  return !(row.decision === 'ask' && row.scopes.length === 0)
}

/** What a selector covers, as a person would say it.
 *
 *  `exact` is the command line the prompt showed them; `program` and
 *  `hasFeature` are the two loose shapes, and both read as "any … command"
 *  because that is exactly what they mean.
 *
 *  It takes a SELECTOR and not a rule because the writing surfaces below name
 *  a rule that does not exist yet — the offer a person is about to accept has
 *  to read in the same words as the answer it becomes, or they cannot tell
 *  that the button did what it said. */
function selectorSubject(sel: PolicyRule['selector']): string {
  if (sel.hasFeature) {
    return `any ${sel.hasFeature.program} command that ${FEATURE_WORDS[sel.hasFeature.feature]}`
  }
  if (sel.program) return `any ${sel.program} command`
  return (sel.exact ?? []).map((command) => command.join(' ')).join(' ; ')
}

/** The rule form of a canonical parse the backend answered with.
 *
 *  The contract says a reading carries no commands at all when it was refused,
 *  and an `exact` selector must name at least one — so this is where the two
 *  meet, and it answers null rather than letting a caller assert past it. */
function exactSelector(
  commands: PolicyClassification['commands'],
): NonNullable<PolicyRule['selector']['exact']> | null {
  const [first, ...rest] = commands
  return first === undefined ? null : [first, ...rest]
}

/** What a standing answer covers. One derivation with the offer above, so an
 *  answer cannot be named one way while it is being made and another once it
 *  is in the list. */
function ruleSubject(rule: PolicyRule): string {
  return selectorSubject(rule.selector)
}

/**
 * Whether this standing answer could be turned into an Allowed at all.
 *
 * IT MIRRORS A BACKEND RULE AND DERIVES NOTHING. The gate is
 * `content.validateInvocationRules`: a selector that covers more than one
 * command line may only NARROW, and the single loose form that may widen —
 * a command word — is only as narrow as the effect it was granted under, so it
 * has to carry one. Both facts a person's answer turns on are on the wire, in
 * the rule itself: which of the three selectors is set, and whether
 * `grantedUnder` is there. Nothing here reads a command, maps a word to an
 * effect, or reasons about what a call would do — that is the classifier's
 * work and this page has never had it.
 *
 * It exists because the page was OFFERING AN ANSWER IT COULD NOT DELIVER
 * (nocx-4yjwk.6): `Change → Allowed` on a hand-written refusal over a command
 * word sent a permit, the gate turned it down, and the person got a red
 * message that taught them nothing.
 */
function canBeAllowed(rule: PolicyRule): boolean {
  if (rule.selector.hasFeature) return false
  if (rule.selector.program) return rule.grantedUnder !== undefined
  return true
}

/**
 * How far an answer reaches, in the words the question used.
 *
 * Built by the approval prompt's own coverage builder, deliberately: the
 * sentence a person reads here must be character for character the one they
 * agreed to there, and it changes with it when it is reworded. `available`
 * and `reason` describe an OFFER being made; an answer already given has
 * neither, so the standing shape is filled in here rather than by every
 * caller.
 */
function coverage(subject: string): string {
  return approvalScopeCoverage('always', { available: true, rule: subject, reason: '' }, '')
}

/** When and how an answer came into being. The zero time means a document
 *  stated none and none was invented, so no date is shown rather than 1 AD. */
function provenance(rule: PolicyRule): string {
  const gave =
    rule.source === 'answered' ? 'You answered this at a prompt' : 'Written into your permissions'
  const at = Date.parse(rule.createdAt)
  if (!Number.isFinite(at) || rule.createdAt.startsWith('0001-')) return `${gave}.`
  return `${gave} on ${formatTimestamp(at)}.`
}

/** One thing the page lists. A rule and a row are different objects with
 *  different gestures, and the union is what keeps every switch on that
 *  difference exhaustive. */
type Answer =
  { kind: 'rule'; key: string; rule: PolicyRule } | { kind: 'row'; key: string; effect: EffectKey }

/** Which panel is open, and over what. Nothing here is a copy of the policy: a
 *  panel names the answer it is about and reads the store's own value every
 *  render, so a write underneath it cannot leave it showing something stale. */
interface Panel {
  mode: 'why' | 'change' | 'forget'
  on: Answer
}

/**
 * ONE write to an answer, held so it can be repeated with the timing the
 * person picks (nocx-r4fh8 for a standing answer, nocx-4yjwk.8 for a row).
 *
 * EVERY writing gesture on this page becomes one of these, because every one
 * of them can leave live work behind: forgetting a standing answer, changing
 * one, writing a new refusal, and moving one of the seven rows all change what
 * a run started thirty seconds ago is still deciding under. The row was the
 * one that did it in silence — it goes through `policy.set`, which used to
 * carry no timing at all.
 *
 * It is DATA rather than a closure so that repeating a gesture is repeating
 * the same object, not re-running a callback whose captured state may have
 * moved. The row variant holds the WHOLE matrix for that reason: the write is
 * a matrix write, and rebuilding it from the store on the repeat would send a
 * document that had moved under the question.
 */
type PolicyGesture =
  | { kind: 'forget'; id: string }
  | { kind: 'write'; rule: PolicyRuleWrite }
  | { kind: 'row'; matrix: PolicyMatrix }

/**
 * A write the backend would not apply yet, and why: it does not reach the work
 * already running, and the person has not said what should happen to it.
 *
 * `count` is the backend's, and only the backend's. Which runs are still
 * deciding under an answer is a question about each run's own frozen grant —
 * the renderer has no run registry and no evaluator, and a number worked out
 * here would be a second answer to drift from the enforcement's.
 */
interface RunsQuestion {
  gesture: PolicyGesture
  count: number
  /** What to close once the person has answered — the panel that raised it. */
  after: () => void
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/**
 * What a stop actually did, in one sentence.
 *
 * The two counts are reported separately because they are two different facts
 * and only one of them is this gesture's doing: a run can reach its own ending
 * between the count and the stop, and saying "stopped 3" when one of them
 * finished by itself tells a person something untrue about their own work.
 */
function stoppedSentence(res: PolicyWriteRuns): string {
  const stopped = res.stoppedRuns === 1 ? 'Stopped 1 answer' : `Stopped ${res.stoppedRuns} answers`
  if (res.finishedBeforeStop === 0)
    return `${stopped} that ${res.stoppedRuns === 1 ? 'was' : 'were'} using it.`
  const already =
    res.finishedBeforeStop === 1
      ? '1 had already finished on its own'
      : `${res.finishedBeforeStop} had already finished on their own`
  return `${stopped}; ${already}.`
}

export interface AssistantPermissionsSectionProps {
  client: PolicyClient
}

export function AssistantPermissionsSection(props: AssistantPermissionsSectionProps) {
  /** What the store held at the last read — the ONLY thing this page draws.
   *  Null until the first read answers. */
  const [view, setView] = createSignal<PolicyView | null>(null)
  const [loadError, setLoadError] = createSignal<string | null>(null)
  const [busy, setBusy] = createSignal(false)
  const [panel, setPanel] = createSignal<Panel | null>(null)
  /** The explanation the backend last returned for the open Why panel. Null
   *  while it is in flight, and cleared when the panel opens — a stale trace
   *  under a fresh question would explain the wrong call. */
  const [explanation, setExplanation] = createSignal<PolicyExplanation | null>(null)
  /** Which writing panel is open, or null. They are two controls and not one
   *  panel with a three-way select, because what may be written from nothing
   *  differs between the two answers: a select would offer a permit wherever
   *  it offered a refusal — see `commandWriter` below. */
  const [writer, setWriter] = createSignal<'allow' | 'refuse' | null>(null)
  /** The command a person has typed into the writing panel. */
  const [typed, setTyped] = createSignal('')
  /**
   * What the BACKEND made of that command, and null until it has been asked.
   *
   * This signal is the whole safety property of the widening surface. Nothing
   * a person types produces a permit; a permit is offered only while this
   * holds a reading, it names the command word that reading answered with, and
   * it is bound to the effect that reading found. Editing the box clears it,
   * because a reading belongs to the text it was taken from.
   */
  const [reading, setReading] = createSignal<PolicyClassification | null>(null)
  /**
   * The question a rule write raised about the work already running, or null.
   *
   * It is ONE signal for the whole page rather than one per panel: only one
   * gesture can be in flight at a time (every control is disabled while `busy`
   * is set), and a second copy would be a second place for a stale count to
   * survive a panel closing.
   */
  const [runsQuestion, setRunsQuestion] = createSignal<RunsQuestion | null>(null)

  async function read(): Promise<void> {
    try {
      setView(await props.client.get())
      setLoadError(null)
    } catch (e) {
      setLoadError(messageOf(e))
    }
  }

  // Once, on mount: the client prop never changes on a live page, and a bare
  // top-level read-and-chain would be the untracked fire-and-forget the
  // reactivity rule flags honestly (solid/reactivity).
  onMount(() => {
    void read()
  })

  const matrix = (): PolicyMatrix | null => view()?.matrix ?? null
  const rules = (): PolicyRule[] => view()?.rules ?? []
  const live = (): EffectKey[] => view()?.live ?? []
  const inert = (rule: PolicyRule): boolean => (view()?.awaitingReview ?? []).includes(rule.id)

  /** Every answer a person has given: their standing answers first, in
   *  document order, then the rows they moved off the default. Rules lead
   *  because they are what a person actually produced — clicking "Allow
   *  always" saves one — and the page before this drew only the rows. */
  const answered = createMemo<Answer[]>(() => {
    const m = matrix()
    if (!m) return []
    return [
      ...rules().map((rule): Answer => ({ kind: 'rule', key: `rule:${rule.id}`, rule })),
      ...EFFECT_KEYS.filter((k) => offDefault(m[k])).map((effect): Answer => ({
        kind: 'row',
        key: `row:${effect}`,
        effect,
      })),
    ]
  })

  /** The questions nobody has answered: the LIVE rows still on their default.
   *  A row outside `live` governs nothing yet, so offering it as a question
   *  would be offering to answer something that cannot be asked. */
  const unanswered = createMemo<Answer[]>(() => {
    const m = matrix()
    if (!m) return []
    return live()
      .filter((k) => !offDefault(m[k]))
      .map((effect): Answer => ({ kind: 'row', key: `row:${effect}`, effect }))
  })

  /** The places nocx knows about: every place the policy itself names, once
   *  each, in the rows' order. This is the whole picker — see the header for
   *  why a place is learned rather than typed. */
  const places = createMemo<Place[]>(() => {
    const m = matrix()
    if (!m) return []
    const seen = new Map<string, Place>()
    for (const key of EFFECT_KEYS) {
      for (const scope of m[key].scopes) {
        const k = placeKey(scope)
        if (!seen.has(k)) seen.set(k, { key: k, scope, name: placeName(scope) })
      }
    }
    return [...seen.values()]
  })

  /**
   * One row of the matrix, changed; every other row and every standing answer
   * travels back exactly as it was read.
   *
   * It goes through `runGesture` like every other write on this page, and that
   * is the whole of nocx-4yjwk.8 here: a row is an answer a run is deciding
   * under, so moving one asks the same question about the work already running
   * that forgetting a standing answer asks — in the same words, from the same
   * block, with the same two answers.
   */
  function writeRow(
    effect: EffectKey,
    next: PolicyRow,
    after: () => void = () => {},
  ): Promise<void> {
    const m = matrix()
    if (!m) return Promise.resolve()
    return runGesture({ kind: 'row', matrix: { ...m, [effect]: next } }, 'ask', after)
  }

  /**
   * Run ONE writing gesture — a standing answer or a row — and RAISE THE
   * QUESTION when the backend says the write does not reach the work already
   * running.
   *
   * This is the whole of nocx-r4fh8 and nocx-4yjwk.8 on this side. A run's
   * authority is minted when it starts and is fixed for the run, so an answer
   * changed here never reaches a run already using it — correct, and silent,
   * which is the defect: a person acts specifically to stop something and is
   * told nothing while it goes on. So the default timing writes NOTHING when
   * live runs would be left behind, answers with how many, and this raises
   * that as a question with two answers. Repeating the same gesture with the
   * timing they chose is what applies it.
   *
   * EVERY write goes through here, including a matrix write, and that is the
   * point rather than tidiness: a row that had its own path would be a second
   * account of what happens to the work already running, and the second
   * account is the one that would go on being silent.
   *
   * The re-read after every outcome: the methods acknowledge rather than echo
   * the document, so the page adopts what a fresh read says and can never show
   * a permission the store did not take — including after a write that
   * deliberately did not happen.
   */
  async function runGesture(g: PolicyGesture, runs: RunsTiming, after: () => void): Promise<void> {
    setBusy(true)
    try {
      const res: PolicyWriteRuns = await callFor(g, runs)
      await read()
      if (!res.applied) {
        setRunsQuestion({ gesture: g, count: res.affectedRuns, after })
        return
      }
      if (runs === 'stop') showToast({ message: stoppedSentence(res), level: 'info' })
      setRunsQuestion(null)
      after()
    } catch (e) {
      showToast({ message: `That change was not saved: ${messageOf(e)}`, level: 'danger' })
      await read()
      setRunsQuestion(null)
      after()
    } finally {
      setBusy(false)
    }
  }

  /**
   * The one call each gesture is. Split out because `runGesture` is about the
   * TIMING and would otherwise carry a three-way branch in the middle of it;
   * the three methods differ only in what they name, never in what the answer
   * means.
   */
  function callFor(g: PolicyGesture, runs: RunsTiming): Promise<PolicyWriteRuns> {
    switch (g.kind) {
      case 'forget':
        return props.client.forgetRule(g.id, runs)
      case 'write':
        return props.client.setRule(g.rule, runs)
      case 'row':
        return props.client.set(g.matrix, runs)
    }
  }

  /** Answer the open question with a timing, repeating the gesture it holds. */
  function answerRunsQuestion(runs: RunsTiming): void {
    const q = runsQuestion()
    if (!q) return
    void runGesture(q.gesture, runs, q.after)
  }

  /** One standing answer, changed. `id` names the rule that already exists, so
   *  this replaces it in place rather than writing a second one. */
  function writeRule(rule: PolicyRule, decision: Decision): Promise<void> {
    return runGesture(
      {
        kind: 'write',
        rule: {
          id: rule.id,
          selector: rule.selector,
          decision,
          grantedUnder: rule.grantedUnder,
        },
      },
      'ask',
      () => {},
    )
  }

  /**
   * Say "I have read what this now means and I still mean it".
   *
   * NOT a re-grant: the selector, the answer and the effect it was granted
   * under all travel back untouched, and the only thing that changes is the
   * reading of commands the store stamps on the write. Widening any of them is
   * a different gesture with a different question attached, which is why an
   * inert answer is offered Review and not Change.
   */
  function review(rule: PolicyRule): Promise<void> {
    return writeRule(rule, rule.decision)
  }

  function openWriter(mode: 'allow' | 'refuse'): void {
    setWriter(mode)
    setTyped('')
    setReading(null)
  }

  function closeWriter(): void {
    setRunsQuestion(null)
    setWriter(null)
    setTyped('')
    setReading(null)
  }

  /**
   * Have the BACKEND read the typed command.
   *
   * It is read and never run: `policy.classify` parses and classifies, and the
   * transport asserts directly that a command with a side effect leaves no
   * trace. That is the property the whole gesture stands on — asking "may I
   * allow this?" must not be a way of doing it.
   *
   * The reading is cleared first for the Why panel's reason: an answer to the
   * previous question shown under a new one is an answer about the wrong
   * command.
   */
  function readTyped(): void {
    const command = typed().trim()
    if (command === '') return
    setReading(null)
    setBusy(true)
    void props.client
      .classify(command)
      .then(setReading)
      .catch((e: unknown) => {
        showToast({ message: `That command could not be read: ${messageOf(e)}`, level: 'danger' })
      })
      .finally(() => setBusy(false))
  }

  /**
   * Save the widened answer the reading justifies.
   *
   * `grantedUnder` carries the effect THE BACKEND FOUND, and that is the whole
   * of the safety argument: the evaluator checks it against the effect a CALL
   * classified as, so this permit reaches `df` while it keeps reading and does
   * not reach it doing something the reading never saw. Nothing here derives
   * it, and there is no control through which a person could state it.
   */
  function saveWidened(r: PolicyClassification): void {
    if (!r.eligible || r.effect === undefined) return
    void runGesture(
      {
        kind: 'write',
        rule: { selector: { program: r.program }, decision: 'permit', grantedUnder: r.effect },
      },
      'ask',
      closeWriter,
    )
  }

  /** Stop exactly the command that was read. */
  function saveExactRefusal(r: PolicyClassification): void {
    const exact = exactSelector(r.commands)
    if (!r.eligible || exact === null) return
    void runGesture(
      { kind: 'write', rule: { selector: { exact }, decision: 'refuse' } },
      'ask',
      closeWriter,
    )
  }

  /** Stop every command of that word carrying the FACT the classifier
   *  recorded. It matches the fact and never the spelling of the token that
   *  carried it — `-o`, `--output`, `--output=file` and an attached short
   *  option are one fact written four ways, and a rule over token text is
   *  evaded by the first of them the parser normalizes differently. */
  function saveFeatureRefusal(
    r: PolicyClassification,
    feature: PolicyClassification['features'][number],
  ): void {
    if (!r.eligible) return
    void runGesture(
      {
        kind: 'write',
        rule: { selector: { hasFeature: { program: r.program, feature } }, decision: 'refuse' },
      },
      'ask',
      closeWriter,
    )
  }

  /** What each answer a person has NOT given would still be asked about — the
   *  live rows other than the one a widening permit is bound to. It is a set
   *  difference over the backend's own list and not a ranking: which effect is
   *  worse than which is the evaluator's business, and this only has to name
   *  the ones this permit does not reach. */
  function otherLiveEffects(effect: EffectKey): string {
    const others = live().filter((k) => k !== effect)
    if (others.length === 0) return 'Anything else it might do'
    return others.map((k) => EFFECT_LABEL[k]).join(', ')
  }

  /** What an answer is called, wherever it is named — the list, a button's
   *  label, a panel's title. One derivation, so a control and the thing it
   *  acts on cannot end up with two names. */
  function subjectOf(answer: Answer): string {
    return answer.kind === 'rule' ? ruleSubject(answer.rule) : EFFECT_LABEL[answer.effect]
  }

  function decisionOf(answer: Answer): Decision {
    if (answer.kind === 'rule') return answer.rule.decision
    return matrix()?.[answer.effect].decision ?? 'ask'
  }

  /**
   * The command line to ask `policy.explain` about, or null when there is none
   * to ask about.
   *
   * An `exact` answer names a literal command line and that is the question. A
   * loose one names a command word, which is a command line of one token — the
   * backend parses and classifies it, this page does not. A row answers for
   * calls that may have no command line at all, and there the honest thing is
   * to have no trace rather than to invent a call.
   */
  function ruleSubjectCommand(rule: PolicyRule): string | null {
    const sel = rule.selector
    if (sel.exact && sel.exact.length === 1) return sel.exact[0].join(' ')
    if (sel.program) return sel.program
    if (sel.hasFeature) return sel.hasFeature.program
    return null
  }

  /**
   * Which row to ask `policy.explain` under.
   *
   * The effect is the CALLER's to state and never this page's to derive: a
   * widening answer names the one it was granted under, and everything else
   * belongs to the row the person is looking at. An `exact` answer names
   * neither, and there is no honest third source — deriving one from the
   * command word would be mapping a command to an effect, which is the
   * classifier's job and nobody else's — so the page asks under the first row
   * the backend says is live and labels the trace with the row it came back
   * for.
   */
  function explainUnder(answer: Answer): EffectKey | null {
    if (answer.kind === 'row') return answer.effect
    return answer.rule.grantedUnder ?? live()[0] ?? null
  }

  function openPanel(mode: Panel['mode'], on: Answer): void {
    setPanel({ mode, on })
    if (mode !== 'why') return
    setExplanation(null)
    const command = on.kind === 'rule' ? ruleSubjectCommand(on.rule) : null
    const effect = explainUnder(on)
    if (command === null || effect === null) return
    void props.client
      .explain(command, effect)
      .then(setExplanation)
      .catch((e: unknown) => {
        showToast({ message: `Could not ask why: ${messageOf(e)}`, level: 'danger' })
      })
  }

  function closePanel(): void {
    setRunsQuestion(null)
    setPanel(null)
    setExplanation(null)
  }

  // Answering ----------------------------------------------------------

  /**
   * Whether this answer's widest option can be given at all.
   *
   * A ROW is not a rule and the rule gate says nothing about it: it is written
   * by `policy.set`, carries no selector, and answering one Allowed is the
   * only way to answer an open question. Only a standing answer is asked.
   */
  function canWiden(answer: Answer): boolean {
    return answer.kind !== 'rule' || canBeAllowed(answer.rule)
  }

  /** The answers this one may be set to — both, unless the store would not
   *  take the widest (see `canBeAllowed`). */
  function answersOffered(answer: Answer): Decision[] {
    return ANSWER_ORDER.filter((d) => d !== 'permit' || canWiden(answer))
  }

  /**
   * The answers that would CHANGE something, which is all a row offers.
   *
   * In a panel the current answer is drawn too, as the one that is pressed —
   * there it is information, "you are here". In a row the badge already says
   * that, so a button for the answer already in force would be a control that
   * writes what is already written: the person clicks, nothing moves, and they
   * conclude the page did not take it (nocx-6szvl).
   */
  function answersThatChange(answer: Answer): Decision[] {
    return answersOffered(answer).filter((d) => d !== decisionOf(answer))
  }

  /**
   * Whether the panel carries anything this answer's ROW cannot say.
   *
   * A row's panel holds the PLACES and nothing else: its coverage sentence is
   * already the row's title, and its answers are on the row now. With no place
   * to pick it is a dialog that buys a person nothing and costs them the sense
   * that answering worked — «зашёл внутрь… всё выглядит так, что я ничего не
   * ответил» — which is the whole of nocx-6szvl. Where places DO exist it
   * survives, and it draws the answers beside them because the two are one
   * decision: what may happen, and where.
   *
   * A RULE always opens one, and that is not an exemption. A rule names a
   * command rather than one of seven fixed questions; its panel is where the
   * reason its widest answer is withheld is explained, and that is a paragraph
   * which belongs in no row.
   */
  function panelCarriesMore(answer: Answer): boolean {
    return answer.kind === 'rule' || places().length > 0
  }

  /** Set one answer, whichever kind of thing it is. ONE code path, so a row
   *  control and a panel control cannot come to mean different things. */
  function answerWith(answer: Answer, decision: Decision): void {
    if (answer.kind === 'rule') {
      void writeRule(answer.rule, decision)
      return
    }
    const row = matrix()?.[answer.effect]
    if (row) void writeRow(answer.effect, { ...row, decision })
  }

  /** One answer, as a control. Built once so the row and the panel offer the
   *  same button under the same name — a person who reads it in one place has
   *  read it in the other. */
  function answerButton(answer: Answer, decision: Decision, size?: 'sm') {
    return (
      <Button
        variant={decision === decisionOf(answer) ? 'primary' : 'default'}
        size={size}
        disabled={busy()}
        ariaLabel={`${ANSWER_LABEL[decision]} — ${coverage(subjectOf(answer))}`}
        onClick={() => answerWith(answer, decision)}
      >
        {ANSWER_LABEL[decision]}
      </Button>
    )
  }

  // The two lists ------------------------------------------------------

  function answeredRow(answer: Answer) {
    const subject = subjectOf(answer)
    const waiting = () => answer.kind === 'rule' && inert(answer.rule)
    const dormant = () => answer.kind === 'row' && !live().includes(answer.effect)
    const status = () => {
      if (waiting()) {
        return { tone: 'warning' as const, text: 'Grants nothing until you read what it now means' }
      }
      if (dormant()) return { tone: 'neutral' as const, text: 'Governs nothing yet' }
      return undefined
    }
    return (
      <div class="st-permissions__answer" data-answer={answer.key}>
        <RecordRow
          title={coverage(subject)}
          kind={{ label: ANSWER_LABEL[decisionOf(answer)], tone: ANSWER_TONE[decisionOf(answer)] }}
          status={status()}
          detail={answer.kind === 'rule' ? provenance(answer.rule) : undefined}
          actions={
            <>
              <Button
                variant="ghost"
                size="sm"
                disabled={busy()}
                ariaLabel={`Why ${subject}`}
                onClick={() => openPanel('why', answer)}
              >
                Why
              </Button>
              <Show
                when={waiting()}
                fallback={
                  <>
                    {/* A ROW's other answer, here rather than behind a dialog:
                        it is the whole of what Change offered a row that has
                        no place to pick, and a click is the gesture. A rule's
                        answers stay in its panel, which it always has. */}
                    <Show when={answer.kind === 'row' && answersThatChange(answer).length > 0}>
                      <ActionGroup ariaLabel={`Your answer for ${subject}`}>
                        <For each={answersThatChange(answer)}>
                          {(decision) => answerButton(answer, decision, 'sm')}
                        </For>
                      </ActionGroup>
                    </Show>
                    <Show when={panelCarriesMore(answer)}>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={busy()}
                        ariaLabel={`Change ${subject}`}
                        onClick={() => openPanel('change', answer)}
                      >
                        Change
                      </Button>
                    </Show>
                  </>
                }
              >
                {/* Re-agreeing and re-granting are different gestures, so an
                    answer waiting to be re-read is offered Review and NOT
                    Change: changing it would quietly re-grant it under the new
                    reading without anybody having read it. */}
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy()}
                  ariaLabel={`Review ${subject}`}
                  onClick={() => {
                    if (answer.kind === 'rule') void review(answer.rule)
                  }}
                >
                  Review
                </Button>
              </Show>
              <Button
                variant="ghost"
                size="sm"
                disabled={busy()}
                ariaLabel={`Forget ${subject}`}
                onClick={() => openPanel('forget', answer)}
              >
                Forget
              </Button>
            </>
          }
        />
      </div>
    )
  }

  function unansweredRow(answer: Answer) {
    const subject = subjectOf(answer)
    // The `kind` badge carries the state this question is in, in the same slot
    // an answered row names its answer — so the two lists read in one grammar.
    // It is a BADGE and not a button: "Ask every time" is where this row
    // already is, and a control for it would change nothing.
    return (
      <div class="st-permissions__answer" data-answer={answer.key}>
        <RecordRow
          title={`May the assistant ${subject}?`}
          kind={{ label: ANSWER_LABEL.ask, tone: ANSWER_TONE.ask }}
          meta="Nobody has answered this, so it is asked every time it comes up."
          actions={
            <>
              {/* The real answers, on the row the question is asked in. */}
              <ActionGroup ariaLabel={`Your answer for ${subject}`}>
                <For each={answersThatChange(answer)}>
                  {(decision) => answerButton(answer, decision, 'sm')}
                </For>
              </ActionGroup>
              {/* And the panel only where it carries the places, which is the
                  one thing a row cannot hold. */}
              <Show when={panelCarriesMore(answer)}>
                <Button
                  variant="default"
                  size="sm"
                  disabled={busy()}
                  ariaLabel={`Answer this now: may the assistant ${subject}?`}
                  onClick={() => openPanel('change', answer)}
                >
                  Answer this now
                </Button>
              </Show>
            </>
          }
        />
      </div>
    )
  }

  // The panels ---------------------------------------------------------

  /** The facts a Why panel states about the answer itself, beside — or instead
   *  of — the backend's trace. */
  function whyFacts(answer: Answer): Fact[] {
    const facts: Fact[] = [
      { name: 'Your answer', value: ANSWER_LABEL[decisionOf(answer)] },
      { name: 'It covers', value: coverage(subjectOf(answer)) },
    ]
    if (answer.kind === 'rule') {
      facts.push({ name: 'Where it came from', value: provenance(answer.rule) })
      if (answer.rule.grantedUnder) {
        facts.push({
          name: 'Given while the command was doing',
          value: EFFECT_LABEL[answer.rule.grantedUnder],
          note: 'It does not reach the same command doing something more serious.',
        })
      }
    } else {
      const scopes = matrix()?.[answer.effect].scopes ?? []
      facts.push({
        name: 'Places it applies in',
        value: scopes.length === 0 ? 'No place named yet' : scopes.map(placeName).join(', '),
      })
    }
    return facts
  }

  function whyPanel(answer: Answer) {
    return (
      <Stack>
        <FactList facts={whyFacts(answer)} ariaLabel="What this answer says" />
        <Show
          when={explanation()}
          fallback={
            <Show
              when={answer.kind === 'rule'}
              fallback={
                <Caption>
                  This answer covers work the assistant does without a command line, so there is no
                  single call to walk through. Open Why on one of your command answers to see the
                  steps.
                </Caption>
              }
            >
              <Spinner size="sm" label="Asking why" />
            </Show>
          }
        >
          {(explained) => (
            <>
              <Caption>
                {`Under “${EFFECT_LABEL[explained().effect]}”, step by step, ending in: ${ANSWER_LABEL[explained().decision]}.`}
              </Caption>
              {/* The steps the backend took, in the order it took them. This
                  list is a rendering of the wire and nothing else — no step is
                  added, dropped or reordered here, because the order IS the
                  thing being explained. */}
              <ol class="st-permissions__trace">
                <For each={explained().trace}>
                  {(step) => (
                    <li class="st-permissions__step" data-step={step.kind}>
                      {stepSentence(step)}
                    </li>
                  )}
                </For>
              </ol>
              <Show when={explained().resource} keyed>
                {(resource) => (
                  <FactList
                    facts={[{ name: 'The place it touched', value: placeName(resource) }]}
                    ariaLabel="The place this call touched"
                  />
                )}
              </Show>
            </>
          )}
        </Show>
      </Stack>
    )
  }

  function rowCovers(answer: Answer, place: Place): boolean {
    if (answer.kind !== 'row') return false
    const scopes = matrix()?.[answer.effect].scopes ?? []
    return scopes.some((scope) => placeKey(scope) === place.key)
  }

  function togglePlace(answer: Answer, place: Place, on: boolean): Promise<void> {
    if (answer.kind !== 'row') return Promise.resolve()
    const row = matrix()?.[answer.effect]
    if (!row) return Promise.resolve()
    const scopes = on
      ? [...row.scopes, place.scope]
      : row.scopes.filter((scope) => placeKey(scope) !== place.key)
    return writeRow(answer.effect, { ...row, scopes })
  }

  function changePanel(answer: Answer) {
    return (
      <Stack>
        <Caption>{`This answer covers ${coverage(subjectOf(answer))}.`}</Caption>
        {/* Every answer, the current one included and drawn as pressed: here
            it is information rather than a control that changes nothing, and
            it is the only place the person is told where they are without
            reading the list behind the dialog. */}
        <ActionGroup ariaLabel={`Your answer for ${subjectOf(answer)}`}>
          <For each={answersOffered(answer)}>{(decision) => answerButton(answer, decision)}</For>
        </ActionGroup>
        {/* Where the widest answer is missing, why — and where it is given
            instead. Saying nothing here would leave a person looking at two
            answers where they remember three, which is the same failure as
            offering a third that cannot be taken, one step quieter. */}
        <Show when={!canWiden(answer)}>
          <Caption>
            {`“${ANSWER_LABEL.permit}” is not offered here: this answer speaks about more than the one command line it was written over, and that is an answer nocx only gives to a command it has read. “${ALLOW_A_COMMAND}”, at the foot of the page, is where that happens.`}
          </Caption>
          <Caption>{WRITER_CAPTION.allow}</Caption>
        </Show>
        <Show when={answer.kind === 'row'}>
          <Section id="permissions-places" title="Where this applies" dense>
            <Show
              when={places().length > 0}
              fallback={
                <Caption>
                  nocx has not learned any places yet. A place is named the first time you widen an
                  answer to cover somewhere a call actually reached, and it can be picked here
                  afterwards.
                </Caption>
              }
            >
              <For each={places()}>
                {(place) => (
                  <Checkbox
                    label={place.name}
                    checked={rowCovers(answer, place)}
                    disabled={busy()}
                    onChange={(on) => void togglePlace(answer, place, on)}
                  />
                )}
              </For>
            </Show>
          </Section>
        </Show>
      </Stack>
    )
  }

  /**
   * What forgetting releases, said before it is taken.
   *
   * Forgetting is where a person can silently give themselves a permission
   * problem — taking back a Never can uncover an Allowed underneath it — so
   * the panel names the calls whose outcome changes and what stops governing
   * them. What it does NOT do is predict which of the person's other answers
   * wins afterwards: that is the precedence order, it has one implementation,
   * and guessing it here would be the second one.
   */
  function forgetPanel(answer: Answer) {
    const subject = subjectOf(answer)
    const decision = decisionOf(answer)
    return (
      <Stack>
        <FactList
          facts={[
            { name: 'Forgetting', value: `${ANSWER_LABEL[decision]} — ${coverage(subject)}` },
            {
              name: 'The calls this changes',
              value: answer.kind === 'rule' ? subject : `everything under ${subject}`,
            },
          ]}
          ariaLabel="What forgetting this releases"
        />
        <Caption>
          {decision === 'permit'
            ? `From then on ${subject} is asked about again, unless another answer of yours already covers it.`
            : `From then on this answer stops governing ${subject}, and whatever else you have answered decides it — which may be a question and may be a No. It is asked about again if nothing else covers it.`}
        </Caption>
      </Stack>
    )
  }

  /**
   * WHAT HAPPENS TO THE WORK ALREADY RUNNING — the question this feature
   * exists to ask out loud (nocx-r4fh8).
   *
   * It JOINS the panel that raised it rather than opening a second dialog with
   * its own words: a person who clicked Forget is in the middle of one
   * gesture, and the question is the rest of that gesture, not a new one.
   *
   * Two things it is careful about. Nothing has changed yet, and it says so —
   * a question that arrived after the write would be a notification wearing a
   * question's clothes. And the count is the backend's: which runs are still
   * deciding under an answer is a question about each run's own frozen grant,
   * and a number worked out here would be a second answer to the one the
   * enforcement gives.
   */
  function runsQuestionBlock() {
    return (
      <Show when={runsQuestion()} keyed>
        {(q) => (
          <Section id="permissions-runs-question" title="The work already running" dense>
            <div data-permissions-runs={q.count}>
              <Stack>
                <FactList
                  facts={[
                    {
                      name: 'Still using this',
                      value: `${q.count} ${q.count === 1 ? 'answer' : 'answers'} the assistant is writing right now`,
                    },
                  ]}
                  ariaLabel="What is still using this answer"
                />
                <Caption>
                  Nothing has changed yet. What the assistant may do is fixed when it starts writing
                  an answer, so this reaches everything it starts from now on either way — you are
                  choosing what happens to the {q.count === 1 ? 'one' : q.count} in progress.
                </Caption>
                {/* WHAT THE NUMBER MEANS, and it is not the same sentence for
                    the two kinds of answer. A standing answer names a command,
                    so its count is the runs still deciding about THAT command.
                    A row names a whole class of thing the assistant does, so
                    its count is every run whose permissions still say the old
                    thing — which on an ordinary afternoon is all of them. That
                    is the truth rather than a rounding of it, and a person who
                    is not told would read a big number as the page counting
                    runs instead of counting answers. */}
                <Show when={q.gesture.kind === 'row'}>
                  <Caption>
                    This answer covers everything of its kind, not one command, so this is every
                    piece of work whose permissions still say what you have just changed.
                  </Caption>
                </Show>
                <ActionGroup ariaLabel="What happens to the work already running">
                  <Button
                    variant="default"
                    disabled={busy()}
                    onClick={() => answerRunsQuestion('future')}
                  >
                    Leave them running
                  </Button>
                  <Button
                    variant="danger"
                    disabled={busy()}
                    onClick={() => answerRunsQuestion('stop')}
                  >
                    {q.count === 1 ? 'Stop it' : 'Stop them'}
                  </Button>
                </ActionGroup>
              </Stack>
            </div>
          </Section>
        )}
      </Show>
    )
  }

  /**
   * THE WRITING PANEL, and the asymmetry that shapes it.
   *
   * Both answers begin the same way — a command, typed, and READ by the
   * backend — and that is not symmetry for its own sake. A refusal could
   * safely be written from typed text alone, because the worst a wrong one
   * does is stop something; it goes through the reading anyway because a rule
   * only matches a command the parser can resolve, so a refusal written over
   * text nobody read would be a permission a person believes they have
   * removed and has not. That is the soft degrade this page exists to keep
   * off the surface.
   *
   * A PERMIT has no such option. `find` is a read-shaped word and
   * `find . -delete` is the same word deleting; a person typing it has made a
   * claim they have no way to check. So the permit button does not exist until
   * a reading does, it names the command word THAT READING answered with, and
   * it is bound to the effect the reading found.
   */
  function commandWriter(mode: 'allow' | 'refuse') {
    return (
      <Stack>
        <Caption>{WRITER_CAPTION[mode]}</Caption>
        <TextField
          label="The command"
          value={typed()}
          placeholder="df -h"
          disabled={busy()}
          error={readingRefusal()}
          onInput={(value) => {
            setTyped(value)
            // A reading belongs to the text it was taken from. The moment that
            // text changes, the offer built on it stops being an offer about
            // this command — and the permit button goes with it.
            setReading(null)
          }}
          onCommit={() => readTyped()}
        />
        <ActionGroup ariaLabel="Read the command">
          <Button
            variant="default"
            disabled={busy() || typed().trim() === ''}
            onClick={() => readTyped()}
          >
            Read this command
          </Button>
        </ActionGroup>
        <Show when={reading()}>{(r) => (mode === 'allow' ? allowOffer(r) : refusalOffer(r))}</Show>
      </Stack>
    )
  }

  /** Why the backend would not let a rule be written over this command, in its
   *  own words, on the box the command was typed into. Undefined while there
   *  is nothing to say — a message under an empty field is a complaint about
   *  something nobody did yet. */
  function readingRefusal(): string | undefined {
    const r = reading()
    if (r === null || r.eligible) return undefined
    return r.reason
  }

  /** What allowing this command would, and would NOT, cover.
   *
   *  The second half is the point and not decoration. A `program` answer
   *  speaks about every command of that word, including ones nobody has run —
   *  which is exactly how a person learns that allowing `find` is a sentence
   *  about `find . -delete` as well. What keeps it safe is the binding to the
   *  effect the reading found, and this says so before anything is saved. */
  function allowOffer(r: () => PolicyClassification) {
    // BOTH halves of the reading, or nothing: the backend said a rule may be
    // written over this command AND it found the effect such a rule has to be
    // bound to. A permit with neither is the document the gate refuses, and a
    // permit with only the first is the one it should.
    return (
      <Show when={r().eligible && r().effect}>
        {(effect) => (
          <>
            <FactList
              ariaLabel="What allowing this command would cover"
              facts={[
                {
                  name: 'It would allow',
                  value: coverage(selectorSubject({ program: r().program })),
                  note: `Every ${r().program} command, including ones you have not run — while it does no more than ${EFFECT_LABEL[effect()]}.`,
                },
                {
                  name: 'It would not allow',
                  value: `a ${r().program} command that does anything more`,
                  note: `${otherLiveEffects(effect())} — those are still asked about, exactly as now. So is a command with a wrapper, more than one command in it, or a shell feature: none of them is within reach of a standing answer.`,
                },
              ]}
            />
            <ActionGroup ariaLabel="Save this answer">
              <Button variant="primary" disabled={busy()} onClick={() => saveWidened(r())}>
                {`Allow ${selectorSubject({ program: r().program })}`}
              </Button>
            </ActionGroup>
          </>
        )}
      </Show>
    )
  }

  /** What this answer would stop: the command as it was read, and — when the
   *  classifier recorded one — the whole class of commands carrying the same
   *  fact. Both are offered because they are different sentences, and a person
   *  choosing between them is choosing how far the answer reaches. */
  function refusalOffer(r: () => PolicyClassification) {
    return (
      <Show when={r().eligible && exactSelector(r().commands)}>
        {(exact) => (
          <>
            <FactList
              ariaLabel="What this answer would stop"
              facts={[
                {
                  name: 'nocx read this as',
                  value:
                    r().effect === undefined
                      ? 'a command it could not place'
                      : EFFECT_LABEL[r().effect!],
                  note: 'It was read, not run.',
                },
              ]}
            />
            <ActionGroup ariaLabel="Save this answer">
              <Button variant="primary" disabled={busy()} onClick={() => saveExactRefusal(r())}>
                {`Never allow ${selectorSubject({ exact: exact() })}`}
              </Button>
              <For each={r().features}>
                {(feature) => (
                  <Button
                    variant="default"
                    disabled={busy()}
                    onClick={() => saveFeatureRefusal(r(), feature)}
                  >
                    {`Never allow ${selectorSubject({ hasFeature: { program: r().program, feature } })}`}
                  </Button>
                )}
              </For>
            </ActionGroup>
          </>
        )}
      </Show>
    )
  }

  function panelTitle(p: Panel): string {
    const subject = subjectOf(p.on)
    if (p.mode === 'why') return `Why ${subject}`
    if (p.mode === 'change') return `Your answer for ${subject}`
    return `Forget: ${subject}`
  }

  return (
    <PageSection
      title="Assistant permissions"
      description="The answers you have given about what the assistant may do on its own, and the questions nobody has answered yet. Anything unanswered is asked."
    >
      <Show when={loadError()}>
        <Badge tone="danger">{`Your permissions could not be read: ${loadError()}`}</Badge>
      </Show>
      <Show when={view() === null && loadError() === null}>
        <Spinner size="sm" label="Loading your permissions" />
      </Show>
      <Show when={view() !== null}>
        <Section id="permissions-answered" title="What you have answered" divided dense>
          <Show
            when={answered().length > 0}
            fallback={
              <Caption>
                You have not answered anything yet, so the assistant asks about everything it does.
              </Caption>
            }
          >
            <div role="list" aria-label="What you have answered" data-answers="answered">
              <For each={answered()}>{(answer) => answeredRow(answer)}</For>
            </div>
          </Show>
        </Section>
        <Section id="permissions-unanswered" title="Not answered yet" divided dense>
          <Show
            when={unanswered().length > 0}
            fallback={<Caption>Nothing is waiting on you.</Caption>}
          >
            <div role="list" aria-label="Not answered yet" data-answers="unanswered">
              <For each={unanswered()}>{(answer) => unansweredRow(answer)}</For>
            </div>
          </Show>
        </Section>
        {/* The two ways to add an answer from nothing, and they are not
            mirror images. A refusal may be written over any command the parser
            can resolve; a permit may only be widened from a command the
            backend has READ, which is why one button says "allow a command"
            and neither says "add a rule". */}
        <ActionGroup ariaLabel="Add an answer">
          <Button variant="default" disabled={busy()} onClick={() => openWriter('refuse')}>
            + Write a refusal
          </Button>
          <Button variant="default" disabled={busy()} onClick={() => openWriter('allow')}>
            {ALLOW_A_COMMAND}
          </Button>
        </ActionGroup>
        {/* The question JOINS the gesture that raised it, and a row answered
            ON THE ROW has no dialog to join — a row whose panel would carry
            nothing does not open one (nocx-6szvl), so its answer buttons live
            in the list. It lands here, and only here: a dialog is open exactly
            when the gesture was made inside one, and two copies of a block
            with one id is two places for one question. */}
        <Show when={panel() === null && writer() === null}>{runsQuestionBlock()}</Show>
      </Show>

      <Show when={writer()} keyed>
        {(mode) => (
          <Dialog
            open
            onClose={closeWriter}
            title={mode === 'allow' ? 'Allow a command' : 'Write a refusal'}
            footer={
              <Button variant="default" onClick={closeWriter}>
                Cancel
              </Button>
            }
          >
            <div data-permissions-panel={mode}>
              {commandWriter(mode)}
              {runsQuestionBlock()}
            </div>
          </Dialog>
        )}
      </Show>

      <Show when={panel()} keyed>
        {(p) => (
          <Dialog
            open
            onClose={closePanel}
            title={panelTitle(p)}
            footer={
              <>
                <Button variant="default" onClick={closePanel}>
                  {p.mode === 'forget' ? 'Cancel' : 'Close'}
                </Button>
                <Show when={p.mode === 'forget' && runsQuestion() === null}>
                  <Button
                    variant="danger"
                    disabled={busy()}
                    onClick={() => {
                      const on = p.on
                      if (on.kind === 'rule') {
                        void runGesture({ kind: 'forget', id: on.rule.id }, 'ask', closePanel)
                        return
                      }
                      // A ROW moved back to its default is the same gesture
                      // as forgetting a standing answer, and carries the same
                      // question about the work already running — the panel
                      // closes once the write has actually landed, never
                      // while the question is still open (nocx-4yjwk.8).
                      void writeRow(on.effect, { decision: 'ask', scopes: [] }, closePanel)
                    }}
                  >
                    Forget it
                  </Button>
                </Show>
              </>
            }
          >
            <div data-permissions-panel={p.mode}>
              <Show when={p.mode === 'why'}>{whyPanel(p.on)}</Show>
              <Show when={p.mode === 'change'}>{changePanel(p.on)}</Show>
              <Show when={p.mode === 'forget'}>{forgetPanel(p.on)}</Show>
              {runsQuestionBlock()}
            </div>
          </Dialog>
        )}
      </Show>
    </PageSection>
  )
}
