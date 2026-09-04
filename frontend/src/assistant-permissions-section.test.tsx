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
  type PolicyRule,
  type PolicyView,
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
  explain?: PolicyExplanation
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
  const setRule = opts.setRuleError
    ? vi.spyOn(client, 'setRule').mockRejectedValue(opts.setRuleError)
    : vi.spyOn(client, 'setRule').mockResolvedValue({ id: 'r-df', added: false })
  const forgetRule = vi.spyOn(client, 'forgetRule').mockResolvedValue({ removed: true })
  const explain = vi.spyOn(client, 'explain').mockResolvedValue(
    opts.explain ?? {
      effect: 'observe',
      decision: 'permit',
      trace: [{ kind: 'effect-row', effect: 'observe', decision: 'ask' }],
    },
  )
  return { client, get, set, setRule, forgetRule, explain }
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
    expect(within(container).getAllByRole('button', { name: /^Answer this now/ }).length).toBe(1)
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
  it('offers the three answers on a standing answer and writes ONE rule', async () => {
    const { client, setRule, get } = fakeClient([view(matrixWith({}), { rules: [ANSWERED_RULE] })])
    const container = mount(client)
    await loaded(container)
    const before = get.mock.calls.length

    const p = await openPanel(container, /^Change/)
    for (const answer of ['Allowed', 'Ask every time', 'Never']) {
      expect(within(p).getByRole('button', { name: new RegExp(`^${answer}`) })).toBeTruthy()
    }
    fireEvent.click(within(p).getByRole('button', { name: /^Never/ }))

    await vi.waitFor(() => expect(setRule).toHaveBeenCalledTimes(1))
    expect(setRule).toHaveBeenCalledWith({
      id: 'r-df',
      selector: ANSWERED_RULE.selector,
      decision: 'refuse',
      grantedUnder: undefined,
    })
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

  it('Answer this now opens the same three answers for an unanswered row', async () => {
    const { client, set } = fakeClient([view(matrixWith({}))])
    const container = mount(client)
    await loaded(container)

    const p = await openPanel(container, 'Answer this now: may the assistant read and inspect?')
    fireEvent.click(within(p).getByRole('button', { name: /^Allowed/ }))
    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe.decision).toBe('permit')
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
    await vi.waitFor(() => expect(forgetRule).toHaveBeenCalledWith('r-find'))
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
    expect(setRule).toHaveBeenCalledWith({
      id: 'r-stale',
      selector: STALE_RULE.selector,
      decision: 'permit',
      grantedUnder: 'cross-boundary',
    })
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
