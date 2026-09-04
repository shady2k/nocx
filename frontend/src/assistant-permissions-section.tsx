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
 *                            -> Why . Change . Forget
 *   NOT ANSWERED YET         the live rows still on ask, named as the
 *                            questions they are
 *                            -> Answer this now
 *
 * FOUR RULES ARE BUILT IN RATHER THAN ASSERTED IN PROSE.
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
  type PolicyClient,
  type PolicyExplanation,
  type PolicyExplanationStep,
  type PolicyMatrix,
  type PolicyRow,
  type PolicyRule,
  type PolicyView,
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

/** The order the three are offered in — widest first, so the default sits in
 *  the middle where it reads as the middle. */
const ANSWER_ORDER: Decision[] = ['permit', 'ask', 'refuse']

/** The tone an answer carries in a list. `permit` is the only one that gives
 *  anything away, so it is the only one that is not quiet. */
const ANSWER_TONE: Record<Decision, BadgeTone> = {
  permit: 'warning',
  ask: 'neutral',
  refuse: 'neutral',
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

/** What a standing answer covers, as a person would say it.
 *
 *  `exact` is the command line the prompt showed them; `program` and
 *  `hasFeature` are the two loose shapes, and both read as "any … command"
 *  because that is exactly what they mean. */
function ruleSubject(rule: PolicyRule): string {
  const sel = rule.selector
  if (sel.hasFeature) {
    return `any ${sel.hasFeature.program} command that ${FEATURE_WORDS[sel.hasFeature.feature]}`
  }
  if (sel.program) return `any ${sel.program} command`
  return (sel.exact ?? []).map((command) => command.join(' ')).join(' ; ')
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

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
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
   * Run one write and adopt what a fresh read answers.
   *
   * The re-read is the point: `policy.set` and `policy.setRule` acknowledge,
   * they do not echo the document back, so trusting the payload we just sent
   * is exactly the "page shows a permission the store did not take" state this
   * surface exists to remove. A write the store turns down raises the danger
   * toast and re-reads too — there is no draft to keep, so the page simply
   * goes back to showing what is there.
   *
   * It takes the write ALREADY IN FLIGHT rather than a thunk: a thunk closing
   * over `props.client` is reactivity read outside a tracked scope, which the
   * lint rule flags honestly (solid/reactivity). Every caller is an event
   * handler, so starting the call there is where it belongs anyway.
   */
  async function write(gesture: Promise<unknown>): Promise<void> {
    setBusy(true)
    try {
      await gesture
      await read()
    } catch (e) {
      showToast({ message: `That change was not saved: ${messageOf(e)}`, level: 'danger' })
      await read()
    } finally {
      setBusy(false)
    }
  }

  /** One row of the matrix, changed; every other row and every standing answer
   *  travels back exactly as it was read. */
  function writeRow(effect: EffectKey, next: PolicyRow): Promise<void> {
    const m = matrix()
    if (!m) return Promise.resolve()
    return write(props.client.set({ ...m, [effect]: next }))
  }

  /** One standing answer, changed. `id` names the rule that already exists, so
   *  this replaces it in place rather than writing a second one. */
  function writeRule(rule: PolicyRule, decision: Decision): Promise<void> {
    return write(
      props.client.setRule({
        id: rule.id,
        selector: rule.selector,
        decision,
        grantedUnder: rule.grantedUnder,
      }),
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
    setPanel(null)
    setExplanation(null)
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
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={busy()}
                    ariaLabel={`Change ${subject}`}
                    onClick={() => openPanel('change', answer)}
                  >
                    Change
                  </Button>
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
    return (
      <div class="st-permissions__answer" data-answer={answer.key}>
        <RecordRow
          title={`May the assistant ${subject}?`}
          meta="Nobody has answered this, so it is asked every time it comes up."
          actions={
            <Button
              variant="default"
              size="sm"
              disabled={busy()}
              ariaLabel={`Answer this now: may the assistant ${subject}?`}
              onClick={() => openPanel('change', answer)}
            >
              Answer this now
            </Button>
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
    const current = () => decisionOf(answer)
    return (
      <Stack>
        <Caption>{`This answer covers ${coverage(subjectOf(answer))}.`}</Caption>
        <ActionGroup ariaLabel={`Your answer for ${subjectOf(answer)}`}>
          <For each={ANSWER_ORDER}>
            {(decision) => (
              <Button
                variant={decision === current() ? 'primary' : 'default'}
                disabled={busy()}
                ariaLabel={`${ANSWER_LABEL[decision]} — ${coverage(subjectOf(answer))}`}
                onClick={() => {
                  if (answer.kind === 'rule') {
                    void writeRule(answer.rule, decision)
                    return
                  }
                  const row = matrix()?.[answer.effect]
                  if (row) void writeRow(answer.effect, { ...row, decision })
                }}
              >
                {ANSWER_LABEL[decision]}
              </Button>
            )}
          </For>
        </ActionGroup>
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
                <Show when={p.mode === 'forget'}>
                  <Button
                    variant="danger"
                    disabled={busy()}
                    onClick={() => {
                      const on = p.on
                      const done =
                        on.kind === 'rule'
                          ? write(props.client.forgetRule(on.rule.id))
                          : writeRow(on.effect, { decision: 'ask', scopes: [] })
                      void done.then(closePanel)
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
            </div>
          </Dialog>
        )}
      </Show>
    </PageSection>
  )
}
