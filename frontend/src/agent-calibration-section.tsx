/**
 * CALIBRATION: the person reproduces each state, and the frames are labelled
 * (nocx-etejh; the per-agent driver configuration design, 2026-08-27 §5).
 *
 * Verifying a rule against a recording it happened to be pointed at proves
 * nothing — a rule that never matches looks exactly like a rule that works.
 * So this page does not offer a recording and a list of labels to pair up. It
 * asks for ONE state at a time, in words, and the only thing a person can do
 * about the question is answer it: capture what is on the pane now, or, for an
 * optional state, decline.
 *
 * # There is deliberately no label picker here
 *
 * Not because a picker would be untidy, but because it is the thing the bead
 * is falsified by: a label attached to a frame the person did not produce for
 * it. Nothing on this page names a label to the backend. The step being asked
 * is the backend's, the label comes from that step, and the frame comes from
 * the pane's live grid at the instant the answer arrives — so a label always
 * sits on the screen the person had in front of them when they answered the
 * question they were being asked.
 *
 * The cost that would otherwise buy is paid by "do that step again", which
 * re-ASKS the step rather than re-pointing its label. A person who captured at
 * the wrong moment is asked again, and the next capture reads a fresh screen.
 *
 * # Skipping, and what it costs
 *
 * The three required states cannot be skipped, and the button for them is not
 * drawn: a calibration without idle, working and asks-you cannot verify a rule,
 * so it may not complete. The optional three can be declined, and the page says
 * what declining means — that state falls to unknown, which is busy, which is a
 * refusal rather than a wrong answer.
 *
 * # AND THE VERDICT, WITH ITS CONSEQUENCE (nocx-jse6x)
 *
 * A labelled set is evidence, not permission. The page draws both, in two
 * sections and in that order: what this agent is calibrated for, and what its
 * rule may therefore do. The second one says "may type" or "indicator only"
 * rather than "verified" or "not verified", because the fact a person needs is
 * what nocx will do with their keyboard — a mistimed keystroke answers whatever
 * modal is on screen, and the modal a coding agent puts up is a tool approval
 * whose first option is Yes.
 *
 * A rule that disagrees with a label has that label listed with both readings,
 * because repair is the point: one of the two is wrong and only the person can
 * say which.
 *
 * # The interval, and both its ends
 *
 * The state is PULLED on a timer that lives with this component: it opens on
 * the first read after mount with a pane selected and closes when the timer is
 * cleared. There is nothing to close in the backend because there is nothing
 * open there — the request is the looking.
 */
import { createMemo, createSignal, For, onCleanup, onMount, Show } from 'solid-js'
import {
  type CalibrationAction,
  type CalibrationClient,
  type CalibrationPane,
  type CalibrationRecord,
  type CalibrationState,
  type CalibrationStep,
  type CalibrationVerdict,
} from './calibration-client'
import { type TypingClient, type TypingResult } from './typing-client'
import { PageSection, Section, Select, Badge, Button, EmptyState } from './ui'
import { Spinner } from './ui/spinner'

/**
 * How often the open page asks again.
 *
 * The same half-second the emitting view uses, and for the same reason: this
 * is read by a pair of human eyes standing beside a terminal, not by the
 * indicator. The poll exists so a pane that stops being watched leaves the
 * list while somebody is looking at it; every answer to an action is returned
 * by the action itself, so nothing here waits on the timer.
 */
export const CALIBRATION_POLL_MS = 500

/**
 * The line the test types. Fixed, and not a field the person fills in.
 *
 * This surface proves a primitive; it is not a way to talk to an agent from
 * Settings, and a free-text box here would be a second place to submit into a
 * pane beside the one a person already has — their keyboard. It says what it
 * is so that a person who finds it in their input box a minute later knows
 * where it came from.
 */
export const TEST_LINE = 'a test line from nocx — nothing was sent'

export interface AgentCalibrationSectionProps {
  client: CalibrationClient
  /** The typing primitive (nocx-dkawo.1). It is here rather than in a section
   *  of its own because the fact it proves is this section's headline: the
   *  verdict says nocx may type into a pane running this agent, and a claim
   *  about what will happen to somebody's keyboard should be checkable in the
   *  place it is made. */
  typing: TypingClient
  /** What to call a pane, when the window knows. Optional: without it the
   *  picker says what the wire said, which is honest and unmemorable. */
  nameOf?: (sessionId: string) => string | null
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

/** What the set on disk HOLDS. It stops there deliberately: whether the rule
 *  classifies those labels is a separate question with a separate answer, and
 *  the two are drawn as two sentences because a person repairing a rule needs
 *  to know which of them is wrong — the evidence or the rule reading it. */
function storedSummary(state: CalibrationState): string {
  const stored = state.stored
  if (!stored) return 'This agent has never been calibrated.'
  const captured = stored.labels.filter((l) => !l.skipped).length
  const declined = stored.labels.filter((l) => l.skipped).length
  const required = state.steps.filter((s) => s.required).length
  const head = stored.complete
    ? `Calibrated: ${captured} labelled states, including all ${required} a rule must classify.`
    : `Incomplete: ${captured} labelled states, and a required one is missing.`
  return declined === 0
    ? head
    : `${head} ${declined} optional ${declined === 1 ? 'state was' : 'states were'} declined, and ${declined === 1 ? 'it stays' : 'they stay'} uncalibrated — which reads as busy rather than as a wrong answer.`
}

function recordNote(rec: CalibrationRecord | undefined): string {
  if (!rec) return 'not asked yet'
  return rec.skipped ? 'declined — stays uncalibrated' : 'labelled'
}

/**
 * THE VERDICT, SAID WITH ITS CONSEQUENCE (nocx-jse6x).
 *
 * Not "verified" and "not verified", which are facts about a check nobody
 * asked for, but what nocx will and will not do — because the thing at stake
 * is a keystroke landing in a tool-approval dialog whose first option is Yes.
 * A soft degrade the UI does not state is how a feature that does not exist
 * survives a release, so the unverified sentence names what the person still
 * gets and what they do not.
 */
function verdictSummary(v: CalibrationVerdict, agent: string): string {
  if (v.mayType) {
    return `Verified against ${v.agreed} of ${v.labelled} labelled ${v.labelled === 1 ? 'state' : 'states'} — nocx may type into a pane running ${agent}.`
  }
  const consequence = `Not verified — indicator only: nocx will go on showing what this pane is doing and will not type into it.`
  return v.reason === undefined ? consequence : `${consequence} ${sentence(v.reason)}`
}

/** The backend writes its reasons as clauses, in the person's vocabulary. This
 *  is the only thing done to them, and it is punctuation rather than wording:
 *  a reason is never rephrased here, because two places wording one refusal is
 *  two places for it to be wrong. */
function sentence(reason: string): string {
  const head = reason.charAt(0).toUpperCase() + reason.slice(1)
  return head.endsWith('.') ? head : `${head}.`
}

/**
 * WHAT HAPPENED TO THE TEST LINE, said as what nocx did rather than as a code.
 *
 * A refusal is the product working, and it comes back as a RESULT rather than
 * an error precisely so it can be said out loud: a pane asking the person to
 * approve a tool is not a malformed request, and a control that silently does
 * nothing is indistinguishable from a broken one. The backend's reason is
 * shown as it came — it is written in the person's vocabulary, and two places
 * wording one refusal is two places for it to be wrong.
 */
function typingNote(r: TypingResult): string {
  if (r.outcome === 'typed') {
    return 'The line is in that pane\u2019s input box now, and nothing was sent. Read it there, then send or clear it yourself.'
  }
  return r.reason === undefined
    ? `Nothing was typed: that pane is ${r.state}.`
    : `Nothing was typed. ${sentence(r.reason)}`
}

/** One label the rule reads differently from the person who produced it. Both
 *  sides are named because the point of showing it is repair, and only the
 *  person can say which of the two is wrong. */
function disagreementNote(d: CalibrationVerdict['disagreements'][number]): string {
  return `you produced this state, and the rule reads that screen as ${d.got} rather than ${d.expected}`
}

export function AgentCalibrationSection(props: AgentCalibrationSectionProps) {
  const [panes, setPanes] = createSignal<CalibrationPane[]>([])
  const [selected, setSelected] = createSignal<string>('')
  const [state, setState] = createSignal<CalibrationState | null>(null)
  const [loaded, setLoaded] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [busy, setBusy] = createSignal(false)
  const [typed, setTyped] = createSignal<TypingResult | null>(null)

  const adopt = (answer: { panes: CalibrationPane[]; calibration?: CalibrationState }): void => {
    setPanes(answer.panes)
    // The selection is adopted from the ANSWER, never held against it: a pane
    // whose observation closed leaves the list, and keeping it selected would
    // leave a person answering steps into a walk that has nowhere to go.
    if (!answer.panes.some((p) => p.sessionId === selected())) setSelected('')
    setState(answer.calibration ?? null)
    setError(null)
  }

  const read = async (): Promise<void> => {
    // A read that landed mid-action would redraw the step the person is
    // answering out from under them, and the answer's own reply is the
    // fresher of the two.
    if (busy()) return
    try {
      adopt(await props.client.read(selected() || undefined))
    } catch (e) {
      setError(messageOf(e))
      setState(null)
    } finally {
      setLoaded(true)
    }
  }

  const act = async (action: CalibrationAction, step?: number): Promise<void> => {
    const pane = selected()
    if (pane === '' || busy()) return
    setBusy(true)
    try {
      adopt(await props.client.answer(pane, action, step))
    } catch (e) {
      // A refusal is the product working: a required state cannot be
      // skipped, and a stale answer cannot be applied. It is shown as it
      // came, because the backend says it in the person's vocabulary.
      setError(messageOf(e))
    } finally {
      setBusy(false)
    }
  }

  /**
   * Put one line into the pane's input and press NOTHING.
   *
   * Type rather than Submit, deliberately: this is a person checking that a
   * rule they just calibrated works, and a button in Settings that started a
   * turn in their agent would be spending their tokens to answer a question
   * about a keystroke. The text lands where they can read it and they decide
   * what happens next — which is also exactly what the primitive's `typed`
   * outcome means.
   */
  const tryTyping = async (pane: string): Promise<void> => {
    if (busy()) return
    setBusy(true)
    try {
      setTyped(await props.typing.type(pane, TEST_LINE))
    } catch (e) {
      setTyped(null)
      setError(messageOf(e))
    } finally {
      setBusy(false)
    }
  }

  onMount(() => {
    void read()
    const timer = setInterval(() => void read(), CALIBRATION_POLL_MS)
    onCleanup(() => clearInterval(timer))
  })

  const options = createMemo(() => [
    { value: '', label: 'Choose a pane…' },
    ...panes().map((p) => ({
      value: p.sessionId,
      label: props.nameOf?.(p.sessionId) ?? `${p.agent} · ${p.sessionId.slice(0, 8)}`,
    })),
  ])

  const pending = createMemo<CalibrationStep | null>(() => {
    const s = state()
    if (!s?.walk) return null
    return s.steps[s.walk.pending] ?? null
  })

  /** What the walk has said about a step so far, by label. */
  const givenByLabel = createMemo(() => {
    const map = new Map<string, CalibrationRecord>()
    for (const rec of state()?.walk?.given ?? []) map.set(rec.label, rec)
    for (const rec of state()?.walk ? [] : (state()?.stored?.labels ?? [])) map.set(rec.label, rec)
    return map
  })

  return (
    <PageSection
      title="Calibrate an agent"
      description="nocx asks you to drive your agent into one named state at a time and labels the screen with the state it asked for. The labelled set is what a detection rule is then checked against — a rule verified against a recording nobody produced on purpose proves nothing."
    >
      <Show when={error()}>
        <Badge tone="danger">{error()!}</Badge>
      </Show>
      <Show when={!loaded() && error() === null}>
        <Spinner size="sm" label="Looking for panes nocx is watching" />
      </Show>
      <Show when={loaded()}>
        <Show
          when={panes().length > 0}
          fallback={
            <EmptyState
              title="No pane is enrolled"
              description="nocx watches a pane only after an agent asks it to. Start an agent in a terminal tab and it appears here."
            />
          }
        >
          <div class="st-calibration__picker">
            <Select
              value={selected()}
              options={options()}
              onChange={(v) => {
                setSelected(v)
                setState(null)
                // The answer was about the pane that was selected, and it
                // stops being true the moment another one is.
                setTyped(null)
                void read()
              }}
            />
          </div>
        </Show>

        <Show when={state()} keyed>
          {(s) => (
            <>
              <Section id="calibration-stored" title="What this agent is calibrated for">
                <div class="st-calibration__stored">
                  <Badge tone={s.stored?.complete ? 'success' : 'neutral'}>
                    {s.stored?.complete ? 'calibrated' : 'not calibrated'}
                  </Badge>
                  <span class="st-calibration__summary">{storedSummary(s)}</span>
                </div>
              </Section>

              <Section id="calibration-verdict" title="What this rule may do">
                <div
                  class="st-calibration__stored"
                  data-may-type={s.verification.mayType ? 'true' : 'false'}
                >
                  <Badge tone={s.verification.mayType ? 'success' : 'warning'}>
                    {s.verification.mayType ? 'may type' : 'indicator only'}
                  </Badge>
                  <span class="st-calibration__summary">
                    {verdictSummary(s.verification, s.agent)}
                  </span>
                </div>
                <p class="st-calibration__detail">
                  A rule earns the right to be typed against by classifying every state you produced
                  for it, and it earns that again every time you look — so an agent whose update
                  changes what its screen looks like loses it here, rather than somewhere a
                  keystroke lands in a dialog.
                </p>
                <For each={s.verification.disagreements}>
                  {(d) => (
                    <div class="st-calibration__row" data-disagreement={d.label}>
                      <span class="st-calibration__name">{d.label}</span>
                      <span class="st-calibration__detail">{disagreementNote(d)}</span>
                    </div>
                  )}
                </For>
                <Show when={s.verification.mayType}>
                  <div class="st-calibration__actions">
                    <Button
                      variant="default"
                      disabled={busy()}
                      onClick={() => void tryTyping(s.sessionId)}
                      data-typing-action="try"
                    >
                      Type a test line into this pane
                    </Button>
                  </div>
                  <p class="st-calibration__detail">
                    nocx types only into a pane its rule reads as waiting for input, and it looks
                    again in the instant before each write — so if your agent starts working, or
                    asks you to approve something, between now and then, nothing is typed and this
                    says which of those it was.
                  </p>
                  <Show when={typed()} keyed>
                    {(result) => (
                      <div
                        class="st-calibration__row"
                        data-typing-outcome={result.outcome}
                        data-typing-state={result.state}
                      >
                        <Badge tone={result.outcome === 'refused' ? 'warning' : 'success'}>
                          {result.outcome === 'refused' ? 'nothing typed' : 'typed'}
                        </Badge>
                        <span class="st-calibration__detail">{typingNote(result)}</span>
                      </div>
                    )}
                  </Show>
                </Show>
              </Section>

              <Show
                when={s.walk}
                keyed
                fallback={
                  <Section id="calibration-start" title="Walk the calibration">
                    <p class="st-calibration__detail">
                      You will be asked for each state in turn. Get your agent into the state, then
                      capture — nocx reads the pane as it is at that moment, so nothing is labelled
                      that you did not produce for it. Starting again leaves the set you already
                      have alone until a new walk finishes.
                    </p>
                    <Button
                      variant="primary"
                      disabled={busy()}
                      onClick={() => void act('begin')}
                      data-calibration-action="begin"
                    >
                      Start calibration
                    </Button>
                  </Section>
                }
              >
                {(walk) => (
                  <Section id="calibration-step" title="What to do now">
                    <div
                      class="st-calibration__ask"
                      data-step={walk.pending}
                      data-label={pending()?.label}
                    >
                      <Badge tone={pending()?.required ? 'warning' : 'neutral'}>
                        {pending()?.required ? 'required' : 'optional'}
                      </Badge>
                      <span class="st-calibration__instruction">{pending()?.ask}</span>
                    </div>
                    <p class="st-calibration__detail">
                      {pending()?.required
                        ? 'This state cannot be skipped: a rule that has not classified it may not be allowed to type into a pane, so a calibration without it verifies nothing.'
                        : 'You can decline this one. Declined, it stays uncalibrated and reads as unknown — which every part of nocx treats as busy, so the cost is a refusal rather than a wrong answer.'}
                    </p>
                    <div class="st-calibration__actions">
                      <Button
                        variant="primary"
                        disabled={busy()}
                        onClick={() => void act('capture', walk.pending)}
                        data-calibration-action="capture"
                      >
                        Capture the pane now
                      </Button>
                      <Show when={pending() && !pending()!.required}>
                        <Button
                          variant="default"
                          disabled={busy()}
                          onClick={() => void act('skip', walk.pending)}
                          data-calibration-action="skip"
                        >
                          Skip this state
                        </Button>
                      </Show>
                      <Show when={walk.pending > 0}>
                        <Button
                          variant="default"
                          disabled={busy()}
                          onClick={() => void act('redo', walk.pending)}
                          data-calibration-action="redo"
                        >
                          Do the previous step again
                        </Button>
                      </Show>
                      <Button
                        variant="ghost"
                        disabled={busy()}
                        onClick={() => void act('abandon')}
                        data-calibration-action="abandon"
                      >
                        Stop
                      </Button>
                    </div>
                  </Section>
                )}
              </Show>

              <Section id="calibration-steps" title="The states, in the order you are asked">
                <For each={s.steps}>
                  {(step, i) => (
                    <div
                      class="st-calibration__row"
                      data-label={step.label}
                      data-required={step.required ? 'true' : 'false'}
                      data-pending={s.walk?.pending === i() ? 'true' : undefined}
                    >
                      <span class="st-calibration__name">{step.label}</span>
                      <span class="st-calibration__detail">
                        {`${step.required ? 'required' : 'optional'} · ${recordNote(givenByLabel().get(step.label))}`}
                      </span>
                    </div>
                  )}
                </For>
              </Section>
            </>
          )}
        </Show>
      </Show>
    </PageSection>
  )
}
