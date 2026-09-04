// @vitest-environment jsdom
/**
 * User-path tests for Assistant permissions (nocx-hvb3r, design
 * `.internal/specs/2026-09-03-assistant-permissions-design.md` "The page").
 *
 * The page this replaced was a seven-row matrix of effect classes and
 * resource scopes — the model's vocabulary, faithfully rendered, and
 * unreadable to the person whose permissions it governs. The owner's verdict
 * twice running was «Я вообще не понимаю как это все работает и как
 * настраивать».
 *
 * So the unit here is A SENTENCE ABOUT A FUTURE QUESTION, and every test
 * below drives the surface through the control a person actually clicks —
 * never through a callback. A test written against the callback cannot report
 * a missing button, and a missing button is what this page is for.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent, within } from '@solidjs/testing-library'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import {
  PolicyClient,
  blankPolicy,
  type EffectKey,
  type PolicyExplanation,
  type PolicyMatrix,
  type PolicyClassification,
  type PolicyRule,
  type PolicyView,
  type RunsTiming,
} from './policy-client'
import { AssistantPermissionsSection } from './assistant-permissions-section'
import { clearToasts, toasts } from './ui'

/** The effect classes a declared tool carries today — the backend's answer,
 *  which the page renders and never derives. */
const LIVE: EffectKey[] = ['observe', 'mutate-destructive']

const ANSWERED_RULE: PolicyRule = {
  id: 'r-df',
  selector: { exact: [['df', '-h']] },
  decision: 'permit',
  createdAt: '2026-09-01T10:00:00Z',
  source: 'answered',
  evaluatorVersion: 2,
}

const WIDE_RULE: PolicyRule = {
  id: 'r-find',
  selector: { program: 'find' },
  decision: 'permit',
  grantedUnder: 'observe',
  createdAt: '2026-09-02T10:00:00Z',
  source: 'written',
  evaluatorVersion: 2,
}

/** A loose permit saved under an earlier reading of commands. It is INERT —
 *  the backend says so on the wire, and this page may never work it out. */
const STALE_RULE: PolicyRule = {
  id: 'r-stale',
  selector: { program: 'curl' },
  decision: 'permit',
  grantedUnder: 'cross-boundary',
  createdAt: '2026-08-01T10:00:00Z',
  source: 'answered',
  evaluatorVersion: 1,
}

/**
 * A refusal somebody WROTE, over a command word rather than a command line.
 *
 * It is the shape the page used to offer an answer it could not deliver.
 * `content.validateInvocationRules` will not take a `program` permit that does
 * not carry the effect it was granted under — a permit over a word is only as
 * narrow as the reading it is bound to, and this rule was never read — so
 * clicking Allowed on it sent a write the store turned down.
 */
const PROGRAM_REFUSAL: PolicyRule = {
  id: 'r-rm',
  selector: { program: 'rm' },
  decision: 'refuse',
  createdAt: '2026-09-02T11:00:00Z',
  source: 'written',
  evaluatorVersion: 2,
}

/** A refusal over a semantic FACT. A matcher this loose may only ever narrow,
 *  whatever it carries: there is no effect that would make it safe, so no
 *  gesture on this page can turn it into an Allowed. */
const FEATURE_REFUSAL: PolicyRule = {
  id: 'r-sort',
  selector: { hasFeature: { program: 'sort', feature: 'writes-option-named-path' } },
  decision: 'refuse',
  createdAt: '2026-09-02T12:00:00Z',
  source: 'written',
  evaluatorVersion: 2,
}

function matrixWith(patch: Partial<PolicyMatrix>): PolicyMatrix {
  return { ...blankPolicy(), ...patch }
}

/**
 * One read's answer, with FRESH objects every time — `PolicyClient.get()`
 * mints a new view per call, and a fixture handing back one shared object
 * would make a re-read look like a no-op and hide the thing these tests
 * exist to catch.
 */
function view(
  matrix: PolicyMatrix,
  opts: { live?: EffectKey[]; rules?: PolicyRule[]; awaitingReview?: string[] } = {},
): PolicyView {
  return {
    matrix: structuredClone(matrix),
    live: [...(opts.live ?? LIVE)],
    rules: structuredClone(opts.rules ?? []),
    awaitingReview: [...(opts.awaitingReview ?? [])],
  }
}

afterEach(() => {
  cleanup()
  clearToasts()
})

function mount(client: PolicyClient): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <AssistantPermissionsSection client={client} />, { container })
  return container
}

interface FakeOpts {
  setError?: Error
  setRuleError?: Error
  /** How many runs already in flight the backend says a rule write does not
   *  reach. Above zero, the write does NOT land and the page must raise the
   *  question — which is the whole of nocx-r4fh8 on this side. */
  affectedRuns?: number
  /** What a stop actually did, when the page asks for one. */
  stopped?: { stoppedRuns: number; finishedBeforeStop: number }
  explain?: PolicyExplanation
  /** What the BACKEND makes of a typed command. The renderer never derives
   *  one: it is the whole reason `+ Allow a command…` exists. */
  classify?: PolicyClassification
  classifyError?: Error
}

/** The reading of `df -h` a real backend answers with. */
const READ_DF: PolicyClassification = {
  program: 'df',
  commands: [['df', '-h']],
  effect: 'observe',
  features: [],
  eligible: true,
  reason: '',
}

/** A client whose reads answer `reads` in order (the last one repeating). */
function fakeClient(reads: PolicyView[], opts: FakeOpts = {}) {
  const client = new PolicyClient(new Dispatcher(fixedEndpoint(9876)))
  let n = 0
  const get = vi.spyOn(client, 'get').mockImplementation(() => {
    const answer = reads[Math.min(n, reads.length - 1)]
    n++
    return Promise.resolve(answer)
  })
  const set = opts.setError
    ? vi.spyOn(client, 'set').mockRejectedValue(opts.setError)
    : vi.spyOn(client, 'set').mockResolvedValue({ ok: true })
  // Both rule methods answer the WHOLE declared shape, including what the
  // write did to the work already running: a fake that omitted those fields
  // would be a fake of a wire that does not exist, and the page's question
  // would be tested against it rather than against the contract.
  const affected = opts.affectedRuns ?? 0
  const runsOf = (runs?: RunsTiming) => {
    if (affected > 0 && (runs === undefined || runs === 'ask')) {
      return { applied: false, affectedRuns: affected, stoppedRuns: 0, finishedBeforeStop: 0 }
    }
    const stop =
      runs === 'stop'
        ? (opts.stopped ?? { stoppedRuns: affected, finishedBeforeStop: 0 })
        : { stoppedRuns: 0, finishedBeforeStop: 0 }
    return { applied: true, affectedRuns: affected, ...stop }
  }
  const setRule = opts.setRuleError
    ? vi.spyOn(client, 'setRule').mockRejectedValue(opts.setRuleError)
    : vi
        .spyOn(client, 'setRule')
        .mockImplementation((_rule, runs) =>
          Promise.resolve({ id: 'r-df', added: false, ...runsOf(runs) }),
        )
  const forgetRule = vi
    .spyOn(client, 'forgetRule')
    .mockImplementation((_id, runs) =>
      Promise.resolve({ removed: runsOf(runs).applied, ...runsOf(runs) }),
    )
  const classify = opts.classifyError
    ? vi.spyOn(client, 'classify').mockRejectedValue(opts.classifyError)
    : vi.spyOn(client, 'classify').mockResolvedValue(opts.classify ?? READ_DF)
  const explain = vi.spyOn(client, 'explain').mockResolvedValue(
    opts.explain ?? {
      effect: 'observe',
      decision: 'permit',
      trace: [{ kind: 'effect-row', effect: 'observe', decision: 'ask' }],
    },
  )
  return { client, get, set, setRule, forgetRule, explain, classify }
}

async function loaded(container: HTMLElement): Promise<void> {
  await vi.waitFor(() => {
    expect(container.querySelector('[data-answers]')).not.toBeNull()
  })
}

/** Every answer the page lists, in the order it lists them, as the section it
 *  is under plus its own key. Reading the DOM, not a signal. */
function answerKeys(container: HTMLElement, section: 'answered' | 'unanswered'): string[] {
  const host = container.querySelector(`[data-answers="${section}"]`)
  if (!host) return []
  return Array.from(host.querySelectorAll<HTMLElement>('[data-answer]')).map((el) =>
    el.getAttribute('data-answer')!,
  )
}

function click(container: HTMLElement, name: string | RegExp): void {
  fireEvent.click(within(container).getByRole('button', { name }))
}

/** The open panel — Why, Change or Forget — with its own actions, which is
 *  the whole dialog: a preview whose "Forget it" sat outside what a test
 *  reached would let the preview and the gesture drift apart. */
function panel(): HTMLElement {
  const body = document.querySelector<HTMLElement>('[data-permissions-panel]')
  expect(body, 'no permissions panel is open').not.toBeNull()
  const dialog = body!.closest<HTMLElement>('[role="dialog"], dialog')
  return dialog ?? body!
}

async function openPanel(container: HTMLElement, name: string | RegExp): Promise<HTMLElement> {
  click(container, name)
  await vi.waitFor(() => panel())
  return panel()
}

describe('assistant permissions: what you have answered', () => {
  it('lists every standing answer AND every row off its default, each as a sentence', async () => {
    const { client } = fakeClient([
      view(
        matrixWith({
          observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/workspace' }] },
          // Off its default and NOT live: it is still an answer a person gave
          // and can take back, so it is listed — with the fact that it
          // governs nothing yet, rather than as an equal.
          delegate: { decision: 'refuse', scopes: [] },
        }),
        { rules: [ANSWERED_RULE, WIDE_RULE] },
      ),
    ])
    const container = mount(client)
    await loaded(container)

    expect(answerKeys(container, 'answered')).toEqual([
      'rule:r-df',
      'rule:r-find',
      'row:observe',
      'row:delegate',
    ])

    const text = container.textContent ?? ''
    // The sentences, in the words the prompt used when it asked.
    expect(text).toContain('df -h')
    expect(text).toContain('in every session, from now on')
    expect(text).toContain('any find command')
    expect(text).toContain('read and inspect')
    // A row nothing can produce yet says so rather than looking like a
    // permission that works.
    expect(text).toContain('Governs nothing yet')
  })

  it('names the live rows still on ask as unanswered questions, and only those', async () => {
    const { client } = fakeClient([
      view(
        matrixWith({
          observe: { decision: 'permit', scopes: [] },
        }),
      ),
    ])
    const container = mount(client)
    await loaded(container)

    // observe is answered; mutate-destructive is live and still on ask. The
    // five rows outside `live` govern nothing, so they are not offered as
    // questions a person could usefully answer.
    expect(answerKeys(container, 'unanswered')).toEqual(['row:mutate-destructive'])
    expect(container.textContent).toContain('make changes that cannot be undone')
    // One question, and its two answers are on it — not behind a dialog.
    const questions = container.querySelector<HTMLElement>('[data-answers="unanswered"]')!
    expect(within(questions).getAllByRole('button', { name: /^Allowed/ }).length).toBe(1)
    expect(within(questions).getAllByRole('button', { name: /^Never/ }).length).toBe(1)
  })

  it('offers Why, Change and Forget on an answer, and no Save button anywhere', async () => {
    const { client } = fakeClient([view(matrixWith({}), { rules: [ANSWERED_RULE] })])
    const container = mount(client)
    await loaded(container)

    expect(within(container).getByRole('button', { name: /^Why/ })).toBeTruthy()
    expect(within(container).getByRole('button', { name: /^Change/ })).toBeTruthy()
    expect(within(container).getByRole('button', { name: /^Forget/ })).toBeTruthy()
    expect(within(container).queryByRole('button', { name: /save/i })).toBeNull()
  })
})

describe('assistant permissions: Why', () => {
  it('renders the steps the wire sent, in the order it sent them, and nothing else', async () => {
    const wire: PolicyExplanation = {
      effect: 'observe',
      decision: 'ask',
      cause: 'row-scope',
      resource: { kind: 'path', id: '/etc' },
      trace: [
        { kind: 'effect-row', effect: 'observe', decision: 'ask' },
        { kind: 'rule-matched', ruleId: 'r-df', decision: 'permit' },
        { kind: 'resource-outside-row-scope', effect: 'observe', decision: 'ask' },
      ],
    }
    const { client, explain } = fakeClient([view(matrixWith({}), { rules: [ANSWERED_RULE] })], {
      explain: wire,
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Why/)
    await vi.waitFor(() => {
      expect(p.querySelectorAll('[data-step]').length).toBe(3)
    })
    expect(
      Array.from(p.querySelectorAll<HTMLElement>('[data-step]')).map((el) =>
        el.getAttribute('data-step'),
      ),
    ).toEqual(['effect-row', 'rule-matched', 'resource-outside-row-scope'])
    expect(explain).toHaveBeenCalledWith('df -h', 'observe')
  })

  it('asks the backend again rather than reusing the last answer', async () => {
    const { client, explain } = fakeClient([view(matrixWith({}), { rules: [ANSWERED_RULE] })])
    const container = mount(client)
    await loaded(container)

    await openPanel(container, /^Why/)
    await vi.waitFor(() => expect(explain).toHaveBeenCalledTimes(1))
    fireEvent.click(within(panel()).getByRole('button', { name: 'Close' }))
    await openPanel(container, /^Why/)
    await vi.waitFor(() => expect(explain).toHaveBeenCalledTimes(2))
  })
})

describe('assistant permissions: Change', () => {
  it('offers the two answers on a standing answer and writes ONE rule', async () => {
    const { client, setRule, get } = fakeClient([view(matrixWith({}), { rules: [ANSWERED_RULE] })])
    const container = mount(client)
    await loaded(container)
    const before = get.mock.calls.length

    const p = await openPanel(container, /^Change/)
    for (const answer of ['Allowed', 'Never']) {
      expect(within(p).getByRole('button', { name: new RegExp(`^${answer}`) })).toBeTruthy()
    }
    // And NOT a third. "Ask every time" is where a rule goes when it is
    // forgotten, and Forget is the control that says so and previews it.
    expect(within(p).queryByRole('button', { name: /^Ask every time/ })).toBeNull()
    fireEvent.click(within(p).getByRole('button', { name: /^Never/ }))

    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    expect(setRule).toHaveBeenCalledWith(
      {
        id: 'r-df',
        selector: ANSWERED_RULE.selector,
        decision: 'refuse',
        grantedUnder: undefined,
      },
      'ask',
    )
    // And the page re-reads: the store is the truth, never the payload we sent.
    await vi.waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(before))
  })

  it('offers named places on a row — and no free-form field and no kind select', async () => {
    const { client, set } = fakeClient([
      view(
        matrixWith({
          observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/workspace' }] },
          'mutate-destructive': { decision: 'ask', scopes: [{ kind: 'path', id: '/tmp' }] },
        }),
      ),
    ])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Change read and inspect/)
    // The two places nocx knows about, both offered; /workspace already on.
    const workspace = within(p).getByRole<HTMLInputElement>('checkbox', { name: /\/workspace/ })
    const tmp = within(p).getByRole<HTMLInputElement>('checkbox', { name: /\/tmp/ })
    expect(workspace.checked).toBe(true)
    expect(tmp.checked).toBe(false)

    // A person picks from places that exist; they never type one.
    expect(p.querySelector('input[type="text"]')).toBeNull()
    expect(p.querySelector('select')).toBeNull()

    fireEvent.click(tmp)
    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe.scopes).toEqual([
      { kind: 'path', id: '/workspace' },
      { kind: 'path', id: '/tmp' },
    ])
  })

  it('answering a question writes that row and carries every other one back untouched', async () => {
    const { client, set } = fakeClient([
      view(matrixWith({ delegate: { decision: 'refuse', scopes: [] } })),
    ])
    const container = mount(client)
    await loaded(container)

    const question = container.querySelector<HTMLElement>('[data-answer="row:observe"]')!
    fireEvent.click(within(question).getByRole('button', { name: /^Allowed/ }))

    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    const document_ = set.mock.calls[0][0]
    expect(document_.observe).toEqual({ decision: 'permit', scopes: [] })
    // `policy.set` carries the whole matrix, so every row a person did not
    // touch has to travel back exactly as it was read.
    expect(document_.delegate).toEqual({ decision: 'refuse', scopes: [] })
  })
})

/**
 * AN ANSWER THAT CANNOT BE GIVEN IS NOT OFFERED (nocx-4yjwk.6).
 *
 * `Change → Allowed` on a rule the store will not widen used to send the write
 * anyway. Nothing unsafe happened — the gate refused it and the page raised
 * the danger toast and re-read — and that is the whole defect: a person
 * clicked an answer, got a red message, and learned nothing. Least of all the
 * thing they needed, which is that a permit comes from a command that was
 * READ, and that `+ Allow a command…` is where that happens.
 *
 * The tests drive the panel a person opens and assert the ABSENCE of the
 * control, not the failure of a write.
 */
describe('assistant permissions: an answer the store would not widen', () => {
  it('does not offer Allowed on it, and still offers the answer it can take', async () => {
    const { client, setRule } = fakeClient([
      view(matrixWith({}), { rules: [PROGRAM_REFUSAL, FEATURE_REFUSAL] }),
    ])
    const container = mount(client)
    await loaded(container)

    for (const subject of ['any rm command', 'any sort command that writes a file']) {
      const p = await openPanel(container, new RegExp(`^Change ${subject}`))
      expect(
        within(p).queryByRole('button', { name: /^Allowed/ }),
        `Allowed is offered for ${subject}`,
      ).toBeNull()
      // Narrowing is what this answer may still do, so the answer it is
      // already at is still drawn — as the one that is pressed.
      within(p).getByRole('button', { name: /^Never/ })
      fireEvent.click(within(p).getAllByRole('button', { name: /^(Close|Cancel)$/ })[0])
      await settle()
    }
    // And the way back is Forget, which is the only name for it.
    within(container).getByRole('button', { name: 'Forget any rm command' })
    expect(setRule).not.toHaveBeenCalled()
  })

  it('says why, and points at the gesture that can give that answer', async () => {
    const { client } = fakeClient([view(matrixWith({}), { rules: [PROGRAM_REFUSAL] })])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Change any rm command/)
    const text = p.textContent ?? ''
    // The reason, in the person's words, and the control by the name its
    // button actually wears — a sentence pointing somewhere they cannot find
    // is not an answer.
    expect(text).toContain('is not offered')
    expect(text).toContain('nocx reads it — it is never run')
    expect(text).toContain('+ Allow a command…')
  })

  it('still offers Allowed where the store would take it', async () => {
    // The exact answer names one command line; the wide one names a word AND
    // carries the effect it was granted under. Both are documents the gate
    // accepts, and a fix that removed the offer everywhere is not a fix.
    const { client, setRule } = fakeClient([
      view(matrixWith({}), { rules: [ANSWERED_RULE, WIDE_RULE] }),
    ])
    const container = mount(client)
    await loaded(container)

    for (const subject of ['df -h', 'any find command']) {
      const p = await openPanel(container, new RegExp(`^Change ${subject}`))
      within(p).getByRole('button', { name: /^Allowed/ })
      expect(p.textContent).not.toContain('is not offered')
      fireEvent.click(within(p).getAllByRole('button', { name: /^(Close|Cancel)$/ })[0])
      await settle()
    }

    const p = await openPanel(container, /^Change any find command/)
    fireEvent.click(within(p).getByRole('button', { name: /^Allowed/ }))
    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    expect(setRule).toHaveBeenCalledWith(
      {
        id: 'r-find',
        selector: WIDE_RULE.selector,
        decision: 'permit',
        grantedUnder: 'observe',
      },
      'ask',
    )
  })

  it('offers both answers on a row, which no rule gate speaks about', async () => {
    // A row is written by policy.set and carries no selector at all, so the
    // rule gate has nothing to say about it. Reading the absence of a permit
    // onto rows would take away the only way to answer an open question.
    const { client } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    const question = container.querySelector<HTMLElement>('[data-answer="row:observe"]')!
    within(question).getByRole('button', { name: /^Allowed/ })
    within(question).getByRole('button', { name: /^Never/ })
    expect(question.textContent).not.toContain('is not offered')
  })
})

/**
 * ANSWERING WHERE THE QUESTION IS ASKED (nocx-6szvl).
 *
 * The owner, on first sight of the page: «Захожу на страницу настроек и вижу
 * «ответить на вопрос». Если зайти внутрь и оставить «спрашивать каждый раз»,
 * всё выглядит так, что я ничего не ответил. Зачем вообще этот отдельный
 * диалог?»
 *
 * Three defects with one shape. "Ask every time" is not a third ANSWER to an
 * unanswered question — it IS the unanswered state, so offering it was
 * offering a button indistinguishable from Cancel. On an answered row it and
 * Forget were the same act under two names, and only one of them previewed
 * what it released. And the dialog that carried all three bought a person
 * nothing on the unanswered path: a row's panel holds the place picker and
 * nothing else a row cannot say, and there are no places to pick until
 * somebody has widened an answer to reach one.
 */
describe('assistant permissions: answering where the question is asked', () => {
  /** The row for one kind of work, in whichever list it is currently in. */
  function rowOf(container: HTMLElement, effect: EffectKey): HTMLElement {
    const el = container.querySelector<HTMLElement>(`[data-answer="row:${effect}"]`)
    expect(el, `no row for ${effect}`).not.toBeNull()
    return el!
  }

  it('answers an unanswered question from the row, with no dialog in between', async () => {
    const { client, set } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    const row = rowOf(container, 'mutate-destructive')
    fireEvent.click(within(row).getByRole('button', { name: /^Allowed/ }))

    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0]['mutate-destructive'].decision).toBe('permit')
    // Nothing opened on the way. The click a person makes IS the answer.
    expect(document.querySelector('[data-permissions-panel]')).toBeNull()
  })

  it('offers no control that writes the row the state it is already in', async () => {
    const { client, set } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    // Every control the question offers, and every control in whatever it
    // opens — asserting the ABSENCE of a no-op by walking the surface rather
    // than by naming the button somebody remembered.
    const row = rowOf(container, 'mutate-destructive')
    for (const button of Array.from(row.querySelectorAll<HTMLButtonElement>('button'))) {
      if (button.disabled) continue
      fireEvent.click(button)
      await settle()
      const open = document.querySelector<HTMLElement>('[data-permissions-panel]')
      if (!open) continue
      const dialog = open.closest<HTMLElement>('[role="dialog"], dialog') ?? open
      for (const inner of Array.from(dialog.querySelectorAll<HTMLButtonElement>('button'))) {
        if (inner.disabled || /^(close|cancel|close dialog)$/i.test(nameOf(inner))) continue
        fireEvent.click(inner)
        await settle()
      }
      const stillOpen = document.querySelector<HTMLElement>('[data-permissions-panel]')
      if (stillOpen) {
        const host = stillOpen.closest<HTMLElement>('[role="dialog"], dialog') ?? stillOpen
        const close = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(dismisses)
        if (close) fireEvent.click(close)
        await settle()
      }
    }

    expect(set.mock.calls.length).toBeGreaterThan(0)
    for (const [document_] of set.mock.calls) {
      expect(
        document_['mutate-destructive'],
        'a control wrote the row the state it was already in',
      ).not.toEqual({ decision: 'ask', scopes: [] })
    }
  })

  it('offers one name for taking an answer back, and it previews what it releases', async () => {
    // A place exists, so the row still opens a panel — which is exactly where
    // the second spelling used to hide.
    const { client, set } = fakeClient([
      view(matrixWith({ observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/w' }] } })),
    ])
    const container = mount(client)
    await loaded(container)

    const change = await openPanel(container, /^Change read and inspect/)
    expect(
      within(change).queryByRole('button', { name: /^Ask every time/ }),
      'a second way to take the answer back, without the preview',
    ).toBeNull()
    fireEvent.click(within(change).getAllByRole('button', { name: /^(Close|Cancel)$/ })[0])
    await settle()

    const forget = await openPanel(container, /^Forget read and inspect/)
    // The one name, and it says what stops governing before it is taken.
    expect(forget.textContent).toContain('asked about again')
    fireEvent.click(within(forget).getByRole('button', { name: 'Forget it' }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe).toEqual({ decision: 'ask', scopes: [] })
  })

  it('opens no panel for a row with no places to pick and nothing withheld', async () => {
    const { client } = fakeClient([
      view(matrixWith({ observe: { decision: 'permit', scopes: [] } })),
    ])
    const container = mount(client)
    await loaded(container)

    // Neither list offers one: the panel a row opens carries the places and
    // nothing else, and there are none.
    expect(within(container).queryByRole('button', { name: /^Change read and inspect/ })).toBeNull()
    expect(within(container).queryByRole('button', { name: /^Answer this now/ })).toBeNull()
    // And the answers are still reachable, on the rows themselves.
    within(rowOf(container, 'observe')).getByRole('button', { name: /^Never/ })
    within(rowOf(container, 'mutate-destructive')).getByRole('button', { name: /^Allowed/ })
  })

  it('still opens one for a row that has places to pick', async () => {
    // The guard against over-fixing: the panel is not gone, it is conditional.
    const { client } = fakeClient([
      view(
        matrixWith({
          observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/workspace' }] },
        }),
      ),
    ])
    const container = mount(client)
    await loaded(container)

    const answered = await openPanel(container, /^Change read and inspect/)
    within(answered).getByRole('checkbox', { name: /\/workspace/ })
    fireEvent.click(within(answered).getAllByRole('button', { name: /^(Close|Cancel)$/ })[0])
    await settle()

    // And an UNANSWERED row too: a question can be answered in a place before
    // it has ever been answered anywhere.
    const question = await openPanel(
      container,
      'Answer this now: may the assistant make changes that cannot be undone?',
    )
    within(question).getByRole('checkbox', { name: /\/workspace/ })
  })
})

describe('assistant permissions: Forget', () => {
  it('previews what it releases and writes nothing until it is taken', async () => {
    const { client, forgetRule } = fakeClient([view(matrixWith({}), { rules: [WIDE_RULE] })])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Forget/)
    // The calls whose outcome changes, named before the gesture is taken.
    expect(p.textContent).toContain('any find command')
    expect(p.textContent).toContain('asked about again')
    expect(forgetRule).not.toHaveBeenCalled()

    fireEvent.click(within(p).getByRole('button', { name: 'Forget it' }))
    await vi.waitFor(() => expect(forgetRule).toHaveBeenCalledWith('r-find', 'ask'))
  })

  it('a forgotten row goes back to being asked about, everywhere', async () => {
    const { client, set } = fakeClient([
      view(matrixWith({ observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/w' }] } })),
    ])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Forget read and inspect/)
    expect(p.textContent).toContain('read and inspect')
    fireEvent.click(within(p).getByRole('button', { name: 'Forget it' }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe).toEqual({ decision: 'ask', scopes: [] })
  })
})

describe('assistant permissions: the work already running', () => {
  it('says how many answers in flight are still using it, and writes nothing yet', async () => {
    const { client, forgetRule } = fakeClient([view(matrixWith({}), { rules: [WIDE_RULE] })], {
      affectedRuns: 3,
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Forget/)
    fireEvent.click(within(p).getByRole('button', { name: 'Forget it' }))

    // The count, said out loud, and the two answers offered with it.
    await vi.waitFor(() => expect(p.querySelector('[data-permissions-runs]')).not.toBeNull())
    const question = p.querySelector('[data-permissions-runs]') as HTMLElement
    expect(question.dataset.permissionsRuns).toBe('3')
    expect(question.textContent).toContain('3 answers the assistant is writing right now')
    expect(question.textContent).toContain('Nothing has changed yet')
    within(question).getByRole('button', { name: 'Leave them running' })
    within(question).getByRole('button', { name: 'Stop them' })

    // And the write did NOT happen: the only call was the one that asked.
    expect(forgetRule.mock.calls).toEqual([['r-find', 'ask']])
  })

  it('asks nothing when no answer in flight is using it, and just applies', async () => {
    const { client, forgetRule } = fakeClient([view(matrixWith({}), { rules: [WIDE_RULE] })], {
      affectedRuns: 0,
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Forget/)
    fireEvent.click(within(p).getByRole('button', { name: 'Forget it' }))

    await vi.waitFor(() => expect(forgetRule).toHaveBeenCalledWith('r-find', 'ask'))
    // The panel closes because the gesture is finished; no question was raised
    // and no second call was made.
    await vi.waitFor(() => expect(container.querySelector('[data-permissions-panel]')).toBeNull())
    expect(container.querySelector('[data-permissions-runs]')).toBeNull()
    expect(forgetRule).toHaveBeenCalledTimes(1)
  })

  it('leaves the running work alone when that is the answer', async () => {
    const { client, forgetRule } = fakeClient([view(matrixWith({}), { rules: [WIDE_RULE] })], {
      affectedRuns: 2,
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Forget/)
    fireEvent.click(within(p).getByRole('button', { name: 'Forget it' }))
    await vi.waitFor(() => expect(p.querySelector('[data-permissions-runs]')).not.toBeNull())
    const question = p.querySelector('[data-permissions-runs]') as HTMLElement
    fireEvent.click(within(question).getByRole('button', { name: 'Leave them running' }))

    await vi.waitFor(() => expect(forgetRule).toHaveBeenCalledTimes(2))
    expect(forgetRule.mock.calls[1]).toEqual(['r-find', 'future'])
    // Nothing was stopped, so nothing is claimed to have been.
    expect(
      toasts()
        .map((t) => t.message)
        .join(' '),
    ).not.toContain('Stopped')
  })

  it('stops them when that is the answer, and says what it actually stopped', async () => {
    const { client, forgetRule } = fakeClient([view(matrixWith({}), { rules: [WIDE_RULE] })], {
      affectedRuns: 3,
      stopped: { stoppedRuns: 2, finishedBeforeStop: 1 },
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /^Forget/)
    fireEvent.click(within(p).getByRole('button', { name: 'Forget it' }))
    await vi.waitFor(() => expect(p.querySelector('[data-permissions-runs]')).not.toBeNull())
    const question = p.querySelector('[data-permissions-runs]') as HTMLElement
    fireEvent.click(within(question).getByRole('button', { name: 'Stop them' }))

    await vi.waitFor(() => expect(forgetRule).toHaveBeenCalledTimes(2))
    expect(forgetRule.mock.calls[1]).toEqual(['r-find', 'stop'])
    // Two counts, two facts: one this gesture caused and one it did not.
    await vi.waitFor(() =>
      expect(
        toasts()
          .map((t) => t.message)
          .join(' '),
      ).toContain('Stopped 2 answers; 1 had already finished on its own.'),
    )
  })

  it('raises the same question for a NEW refusal, which no answer in flight will see', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))], { affectedRuns: 1 })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Write a refusal/)
    await readCommand(p, 'df -h')
    fireEvent.click(within(p).getByRole('button', { name: /^Never allow df -h/ }))

    await vi.waitFor(() => expect(p.querySelector('[data-permissions-runs]')).not.toBeNull())
    const question = p.querySelector('[data-permissions-runs]') as HTMLElement
    expect(question.textContent).toContain('1 answer the assistant is writing right now')
    within(question).getByRole('button', { name: 'Stop it' })
    expect(setRule).toHaveBeenCalledTimes(1)
  })
})

describe('assistant permissions: an answer waiting to be re-read', () => {
  it('is shown as inert, with Review and Forget and no Change', async () => {
    const { client, setRule } = fakeClient([
      view(matrixWith({}), { rules: [STALE_RULE], awaitingReview: ['r-stale'] }),
    ])
    const container = mount(client)
    await loaded(container)

    const text = container.textContent ?? ''
    expect(text).toContain('Grants nothing until you read what it now means')
    expect(within(container).getByRole('button', { name: /^Review/ })).toBeTruthy()
    expect(within(container).getByRole('button', { name: /^Forget/ })).toBeTruthy()
    // Re-agreeing and re-granting are different gestures.
    expect(within(container).queryByRole('button', { name: /^Change/ })).toBeNull()

    click(container, /^Review/)
    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    // Confirming changes NOTHING the rule says: the backend stamps the
    // reading of commands running now, and that is the whole gesture.
    expect(setRule).toHaveBeenCalledWith(
      {
        id: 'r-stale',
        selector: STALE_RULE.selector,
        decision: 'permit',
        grantedUnder: 'cross-boundary',
      },
      'ask',
    )
  })

  it('a healthy rule beside it is not marked inert', async () => {
    const { client } = fakeClient([
      view(matrixWith({}), { rules: [STALE_RULE, WIDE_RULE], awaitingReview: ['r-stale'] }),
    ])
    const container = mount(client)
    await loaded(container)

    const rows = container.querySelectorAll<HTMLElement>('[data-answer^="rule:"]')
    expect(rows.length).toBe(2)
    expect(rows[0].textContent).toContain('Grants nothing until you read what it now means')
    expect(rows[1].textContent).not.toContain('Grants nothing until you read what it now means')
  })
})

describe('assistant permissions: the words are the product’s', () => {
  it('never says effect class, resource scope or refuse, and no control names a tool', async () => {
    const { client } = fakeClient([
      view(
        matrixWith({
          observe: { decision: 'refuse', scopes: [{ kind: 'path', id: '/workspace' }] },
          'mutate-destructive': { decision: 'permit', scopes: [] },
        }),
        { rules: [ANSWERED_RULE, STALE_RULE], awaitingReview: ['r-stale'] },
      ),
    ])
    const container = mount(client)
    await loaded(container)

    const text = (container.textContent ?? '').toLowerCase()
    expect(text).not.toContain('effect class')
    expect(text).not.toContain('resource scope')
    expect(text).not.toContain('refuse')

    // ADR-0028 decision 4: no configuration path may name a tool. Every
    // control here names an effect, a place, or a command word.
    const named = Array.from(
      container.querySelectorAll<HTMLElement>('button, input, select, [role="checkbox"]'),
    ).map((el) => `${el.getAttribute('aria-label') ?? ''} ${el.textContent ?? ''}`.toLowerCase())
    for (const label of named) {
      for (const tool of ['files.read', 'files.edit', 'session.run', 'fetch.url', 'git.status']) {
        expect(label).not.toContain(tool)
      }
    }
  })

  it('keeps the three words out of every panel too, the trace included', async () => {
    // The trace is where the vocabulary would come back: the evaluator's own
    // `detail` prose says "the row refuses", and a page that printed it
    // verbatim would speak the model's words at the one moment it is
    // explaining itself to a person. Every step kind is exercised, so a
    // sentence added for a new one cannot slip past this.
    const everyStep: PolicyExplanation = {
      effect: 'observe',
      decision: 'refuse',
      cause: 'fence',
      resource: { kind: 'destination', id: '*' },
      trace: [
        { kind: 'unparsed', decision: 'ask' },
        { kind: 'effect-row', effect: 'observe', decision: 'refuse' },
        {
          kind: 'row-refuses',
          effect: 'observe',
          decision: 'refuse',
          detail: 'the row refuses, and a rule is an exception to the effect layer alone',
        },
        { kind: 'disqualified', effect: 'observe', decision: 'ask' },
        { kind: 'rule-matched', ruleId: 'r-df', decision: 'permit' },
        { kind: 'rule-stale', ruleId: 'r-stale', decision: 'permit' },
        { kind: 'rule-other-effect', ruleId: 'r-find', effect: 'observe', decision: 'permit' },
        { kind: 'resource-inside', effect: 'observe', decision: 'permit' },
        { kind: 'resource-outside-fence', effect: 'observe', decision: 'refuse' },
        { kind: 'resource-outside-row-scope', effect: 'observe', decision: 'ask' },
        { kind: 'resource-not-reached', effect: 'observe', decision: 'refuse' },
      ],
    }
    const { client } = fakeClient(
      [
        view(
          matrixWith({ observe: { decision: 'refuse', scopes: [{ kind: 'path', id: '/w' }] } }),
          { rules: [ANSWERED_RULE] },
        ),
      ],
      { explain: everyStep },
    )
    const container = mount(client)
    await loaded(container)

    // The Why panel first, and only once its steps are on screen: a check
    // that ran against an empty panel would pass for ever.
    const why = await openPanel(container, /^Why df -h/)
    await vi.waitFor(() => {
      expect(why.querySelectorAll('[data-step]').length).toBe(everyStep.trace.length)
    })
    fireEvent.click(within(why).getByRole('button', { name: 'Close' }))

    for (const open of [/^Why df -h/, /^Change df -h/, /^Forget df -h/, /^Why read and inspect/]) {
      const p = await openPanel(container, open)
      await vi.waitFor(() => expect(p.textContent).toBeTruthy())
      const text = (p.textContent ?? '').toLowerCase()
      expect(text, `panel ${String(open)}`).not.toContain('effect class')
      expect(text, `panel ${String(open)}`).not.toContain('resource scope')
      expect(text, `panel ${String(open)}`).not.toContain('refuse')
      fireEvent.click(within(p).getAllByRole('button', { name: /^(Close|Cancel)$/ })[0])
    }
  })
})

describe('assistant permissions: a refused write', () => {
  it('raises the danger toast and the surface re-reads', async () => {
    const answered = view(matrixWith({}), { rules: [ANSWERED_RULE] })
    const { client, get } = fakeClient([answered], {
      setRuleError: new Error('the store said no'),
    })
    const container = mount(client)
    await loaded(container)
    const before = get.mock.calls.length

    const p = await openPanel(container, /^Change/)
    fireEvent.click(within(p).getByRole('button', { name: /^Never/ }))

    await vi.waitFor(() => {
      expect(toasts().some((t) => t.level === 'danger')).toBe(true)
    })
    expect(toasts()[0].message).toContain('the store said no')
    // The page holds no draft to diverge: it re-reads and shows the store.
    await vi.waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(before))
    expect(container.textContent).toContain('df -h')
  })

  it('says so when the policy cannot be read at all', async () => {
    const client = new PolicyClient(new Dispatcher(fixedEndpoint(9876)))
    vi.spyOn(client, 'get').mockRejectedValue(new Error('backend is gone'))
    const container = mount(client)
    await vi.waitFor(() => {
      expect(container.textContent).toContain('backend is gone')
    })
  })
})

/**
 * WIDENING FROM A CLASSIFIED WITNESS (nocx-fl0o3).
 *
 * The asymmetry below is the design and not a precaution. A REFUSAL may be
 * written from typed text, because the worst a wrong one does is stop
 * something. A PERMIT may not: a person typing `find` into a box does not know
 * that `find . -delete` is the same word, so the only route to one is to have
 * the backend READ a command, be shown what the resulting rule would and would
 * not reach, and only then save a rule carrying the effect that reading found.
 *
 * Every test here drives that through the control a person clicks, and the
 * last one sweeps the whole surface rather than the buttons anyone remembered.
 */

/** Let the page's promises settle — a classification arrives asynchronously,
 *  and the controls it unlocks do not exist until it has. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
}

/** Type into the one command box of an open panel. */
function typeCommand(host: HTMLElement, command: string): void {
  const field = host.querySelector<HTMLInputElement>('input[type="text"], textarea')
  expect(field, 'the panel has no box to type a command into').not.toBeNull()
  fireEvent.input(field!, { target: { value: command } })
}

/** Type a command and have the backend read it — the two gestures that must
 *  happen before any permit can exist. */
async function readCommand(host: HTMLElement, command: string): Promise<void> {
  typeCommand(host, command)
  fireEvent.click(within(host).getByRole('button', { name: /^Read this command/ }))
  await settle()
}

describe('assistant permissions: + Allow a command…', () => {
  /**
   * The box a command is typed into is the one on this page whose contents
   * become a permission, and it had no accessible name at all: the panels drew
   * `<TextField label="The command">` with no `id`, so the kit's `<label for>`
   * and the input's `id` were both empty and the label was bound to nothing.
   * A reader announced an unnamed text field; `getByLabel` reached only the
   * action group whose aria-label happens to contain those words.
   *
   * Both panels are asserted because both are surfaces a person types a
   * command into, and the assertion resolves to the INPUT — a label that
   * matches a group around it is exactly the state this test exists to refuse.
   */
  it('names the command box on both writing panels', async () => {
    const { client } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    for (const opener of [/Allow a command/, /Write a refusal/]) {
      const p = await openPanel(container, opener)
      const field = within(p).getByLabelText('The command')
      expect(field.tagName, `panel ${String(opener)}`).toBe('INPUT')
      expect(field.getAttribute('class')).toBe('ui-text-field__input')
      fireEvent.click(within(p).getAllByRole('button', { name: /^(Close|Cancel)$/ })[0])
      await settle()
    }
  })

  it('has the two ways to add an answer, and neither is a matrix', async () => {
    const { client } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    expect(within(container).getByRole('button', { name: /Allow a command/ })).toBeTruthy()
    expect(within(container).getByRole('button', { name: /Write a refusal/ })).toBeTruthy()
  })

  it('reads the command and shows what the rule would and would not match, saving nothing', async () => {
    const { client, classify, setRule } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Allow a command/)
    await readCommand(p, 'df -h')

    expect(classify).toHaveBeenCalledWith('df -h')
    // NOTHING is saved by reading. The preview exists so a person can change
    // their mind after seeing what they were about to widen.
    expect(setRule).not.toHaveBeenCalled()

    const text = p.textContent ?? ''
    // What it WOULD cover: every df command, not the one that was typed.
    expect(text).toContain('any df command')
    expect(text).toContain('read and inspect')
    // What it would NOT: the same word doing something the reading never saw.
    // This is the half that teaches a person that `find` covers `find . -delete`.
    expect(text.toLowerCase()).toContain('would not allow')
    expect(text).toContain('make changes that cannot be undone')
  })

  it('saves a Program rule carrying the effect the reading found, and only then', async () => {
    const { client, setRule, get } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)
    const before = get.mock.calls.length

    const p = await openPanel(container, /Allow a command/)
    await readCommand(p, 'df -h')
    fireEvent.click(within(p).getByRole('button', { name: /^Allow any df command/ }))

    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    // The rule that went over the wire, whole. `grantedUnder` is the effect the
    // BACKEND read, and it is what stops this permit reaching the same command
    // doing something more serious.
    expect(setRule).toHaveBeenCalledWith(
      {
        selector: { program: 'df' },
        decision: 'permit',
        grantedUnder: 'observe',
      },
      'ask',
    )
    // And the page adopts the store rather than the payload it sent.
    await vi.waitFor(() => expect(get.mock.calls.length).toBeGreaterThan(before))
  })

  it('offers nothing to save until the backend has read something', async () => {
    const { client } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Allow a command/)
    typeCommand(p, 'find')
    await settle()

    // Typed, unread: there is no permit to click, whatever the word says.
    expect(within(p).queryByRole('button', { name: /^Allow any/ })).toBeNull()
  })

  it('says why a command cannot be allowed in advance, in the backend’s words', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))], {
      classify: {
        program: '',
        commands: [],
        features: [],
        eligible: false,
        reason: 'the command uses an indirect wrapper or shell feature',
      },
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Allow a command/)
    await readCommand(p, 'sudo df -h')

    expect(p.textContent).toContain('indirect wrapper')
    // Refused, and refused loudly: no save, and nothing to save with.
    expect(within(p).queryByRole('button', { name: /^Allow any/ })).toBeNull()
    expect(setRule).not.toHaveBeenCalled()
  })

  it('says so when the backend cannot be asked, rather than going quiet', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))], {
      classifyError: new Error('the backend is gone'),
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Allow a command/)
    await readCommand(p, 'df -h')

    await vi.waitFor(() => expect(toasts().some((t) => t.level === 'danger')).toBe(true))
    expect(within(p).queryByRole('button', { name: /^Allow any/ })).toBeNull()
    expect(setRule).not.toHaveBeenCalled()
  })
})

describe('assistant permissions: + Write a refusal', () => {
  it('writes an Exact refusal over the command that was read', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Write a refusal/)
    await readCommand(p, 'df -h')
    fireEvent.click(within(p).getByRole('button', { name: /^Never allow df -h/ }))

    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    expect(setRule).toHaveBeenCalledWith(
      {
        selector: { exact: [['df', '-h']] },
        decision: 'refuse',
      },
      'ask',
    )
  })

  it('writes a HasFeature refusal over the fact, never the spelling of a token', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))], {
      classify: {
        program: 'sort',
        commands: [['sort', '-o', '/tmp/out', '/tmp/in']],
        effect: 'mutate-reversible',
        features: ['writes-option-named-path'],
        eligible: true,
        reason: '',
      },
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Write a refusal/)
    await readCommand(p, 'sort -o /tmp/out /tmp/in')
    // The offer is made in words, over the FACT the classifier recorded:
    // `-o`, `--output` and `--output=file` are one fact written three ways.
    fireEvent.click(
      within(p).getByRole('button', {
        name: /^Never allow any sort command that writes a file to a path named by one of its own options/,
      }),
    )

    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    expect(setRule).toHaveBeenCalledWith(
      {
        selector: { hasFeature: { program: 'sort', feature: 'writes-option-named-path' } },
        decision: 'refuse',
      },
      'ask',
    )
  })

  it('has no path through it that produces a permit', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))], {
      classify: {
        program: 'sort',
        commands: [['sort', '-o', '/tmp/out', '/tmp/in']],
        effect: 'mutate-reversible',
        features: ['writes-option-named-path'],
        eligible: true,
        reason: '',
      },
    })
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, /Write a refusal/)
    await readCommand(p, 'sort -o /tmp/out /tmp/in')

    // Every button this panel offers once the command has been read, not the
    // one this test remembered. Reading again is skipped for the reason the
    // sweep below skips it: an edit or a re-read discards the offer, and this
    // is about what the offer can save.
    for (const button of Array.from(p.querySelectorAll<HTMLButtonElement>('button'))) {
      if (
        button.disabled ||
        /^(Read this command|Close|Cancel|Close dialog)$/i.test(
          (button.getAttribute('aria-label') ?? button.textContent ?? '').trim(),
        )
      ) {
        continue
      }
      fireEvent.click(button)
      await settle()
    }

    expect(setRule.mock.calls.length).toBeGreaterThan(0)
    for (const [rule] of setRule.mock.calls) {
      expect(rule.decision, `${JSON.stringify(rule)} came out of the refusal flow`).toBe('refuse')
      expect(rule.grantedUnder).toBeUndefined()
    }
  })
})

/**
 * Click EVERY control the page offers, and every control in whatever it opens,
 * with a command typed into every box on the way.
 *
 * A test that checked the one button somebody remembered would pass a page
 * that grew a second route to a permit next week. This walks the surface.
 */
/** A control's accessible name, which is what a person reads. */
function nameOf(button: HTMLButtonElement): string {
  return (button.getAttribute('aria-label') ?? button.textContent ?? '').trim()
}

/** A control that only closes what is open. Clicking one is not a gesture the
 *  sweep is interested in, and the dialog's own X is one of them — clicking it
 *  first would end every walk on its first step. */
function dismisses(button: HTMLButtonElement): boolean {
  return /^(close|cancel|close dialog)$/i.test(nameOf(button))
}

async function sweepEveryGesture(container: HTMLElement, typed: string): Promise<number> {
  let clicked = 0
  const pageButtons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'))
  for (const opener of pageButtons) {
    if (opener.disabled) continue
    fireEvent.click(opener)
    clicked++
    await settle()
    const used = new Set<string>()
    // Three rounds: a box is filled, the reading it unlocks arrives, and the
    // controls that appear only afterwards are clicked in the round after that.
    for (let round = 0; round < 3; round++) {
      const open = document.querySelector<HTMLElement>('[data-permissions-panel]')
      if (!open) break
      const dialog = open.closest<HTMLElement>('[role="dialog"], dialog') ?? open
      for (const field of Array.from(
        dialog.querySelectorAll<HTMLInputElement>('input[type="text"], textarea'),
      )) {
        // Only what is not already typed. Re-typing the same text is still an
        // edit, and an edit discards the reading it was taken from — which is
        // the property under test, not something to fight.
        if (field.value === typed) continue
        fireEvent.input(field, { target: { value: typed } })
      }
      for (const button of Array.from(dialog.querySelectorAll<HTMLButtonElement>('button'))) {
        if (button.disabled || dismisses(button)) continue
        // Once each. A control that has already been used is not clicked
        // again, because re-reading a command discards the offer built on it
        // and this sweep would then never reach the control that saves.
        const label = nameOf(button)
        if (used.has(label)) continue
        used.add(label)
        fireEvent.click(button)
        clicked++
        await settle()
      }
    }
    const stillOpen = document.querySelector<HTMLElement>('[data-permissions-panel]')
    if (stillOpen) {
      const dialog = stillOpen.closest<HTMLElement>('[role="dialog"], dialog') ?? stillOpen
      const close = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(dismisses)
      if (close) fireEvent.click(close)
      await settle()
    }
  }
  return clicked
}

describe('assistant permissions: a permit is never typed from nothing', () => {
  it('mints no standing permit anywhere on the page when the backend reads nothing', async () => {
    // An EMPTY policy: no standing answer to change, no row off its default. So
    // any rule that came out of this sweep was made from typed text alone.
    const { client, setRule, classify } = fakeClient([view(matrixWith({}))], {
      classifyError: new Error('the backend will not read that'),
    })
    const container = mount(client)
    await loaded(container)

    const clicked = await sweepEveryGesture(container, 'find')

    // The guard on the guard: a sweep that clicked nothing passes for ever.
    expect(clicked).toBeGreaterThan(3)
    expect(classify).toHaveBeenCalled()
    for (const [rule] of setRule.mock.calls) {
      expect(rule.decision, `${JSON.stringify(rule)} was minted without a reading`).not.toBe(
        'permit',
      )
    }
  })

  it('mints a permit only over what the backend read, and bound to what it found', async () => {
    const { client, setRule } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    await sweepEveryGesture(container, 'df -h')

    const permits = setRule.mock.calls.map(([rule]) => rule).filter((r) => r.decision === 'permit')
    expect(permits.length).toBeGreaterThan(0)
    for (const permit of permits) {
      // Every one names the command word the READING answered with, and is
      // bound to the effect that reading found. Nothing here was typed: the
      // person typed `df -h`, and what the rule carries came back over the wire.
      expect(permit.selector).toEqual({ program: READ_DF.program })
      expect(permit.grantedUnder).toBe(READ_DF.effect)
    }
    // A row answer is a different object and is deliberately not caught here:
    // it answers a question the page NAMED, over no command at all, and
    // travels by policy.set. What may never be typed is a rule over a command.
  })
})
