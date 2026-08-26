// @vitest-environment jsdom
/**
 * User-path tests for the Agent policy surface (ADR-0020 §7 as amended,
 * accepted 2026-08-16).
 *
 * What a person does here: they read what the assistant may do on its own,
 * they change one of those answers, and the change is in force immediately —
 * there is no Save button and therefore no unsaved state to lose. The tests
 * below watch exactly that, through the DOM, because the seam that broke last
 * time type-checked while being wrong: Solid's Setter is overloaded to accept
 * any non-function value, so `.then(setMatrix)` on a `{matrix, live}` view was
 * silent to tsc. Only a rendered assertion can report that.
 *
 * Two rules are asserted at the vocabulary this page speaks:
 * - the kind select offers no 'tool' option, so the surface cannot express a
 *   rule over a tool name (ADR-0028 decision 4);
 * - the ADR's own words never reach the surface.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent, within } from '@solidjs/testing-library'
import { Dispatcher } from './dispatcher'
import {
  EFFECT_KEYS,
  PolicyClient,
  blankPolicy,
  type EffectKey,
  type PolicyMatrix,
  type PolicyView,
} from './policy-client'
import { AgentPolicySection } from './agent-policy-section'
import { clearToasts, toasts } from './ui'

/** The two effect classes a declared tool actually carries today
 *  (agenttools.LiveEffects) — the backend's answer, which the page renders
 *  first and never derives for itself. */
const LIVE: EffectKey[] = ['observe', 'mutate-destructive']

const LOADED: PolicyMatrix = {
  observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/workspace' }] },
  'mutate-reversible': { decision: 'ask', scopes: [] },
  'mutate-destructive': { decision: 'refuse', scopes: [] },
  'privilege-change': { decision: 'ask', scopes: [] },
  disclose: { decision: 'ask', scopes: [] },
  'cross-boundary': { decision: 'ask', scopes: [] },
  delegate: { decision: 'ask', scopes: [] },
}

/**
 * One read's answer, with FRESH objects every time — `PolicyClient.get()`
 * mints a new matrix on every call, and the page's controls re-apply their
 * value when the signal's identity changes. A fixture handing back one shared
 * object would make a re-read look like a no-op and hide the very thing these
 * tests exist to catch.
 */
function view(matrix: PolicyMatrix, live: EffectKey[] = LIVE): PolicyView {
  return { matrix: structuredClone(matrix), live: [...live] }
}

afterEach(() => {
  cleanup()
  clearToasts()
})

function mount(client: PolicyClient): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <AgentPolicySection client={client} />, { container })
  return container
}

/** A client whose reads answer `reads` in order (the last one repeating) and
 *  whose writes resolve, unless `setError` says otherwise. */
function fakeClient(reads: PolicyView[], setError?: Error) {
  const client = new PolicyClient(new Dispatcher())
  let n = 0
  const get = vi.spyOn(client, 'get').mockImplementation(() => {
    const answer = reads[Math.min(n, reads.length - 1)]
    n++
    return Promise.resolve(answer)
  })
  const set = setError
    ? vi.spyOn(client, 'set').mockRejectedValue(setError)
    : vi.spyOn(client, 'set').mockResolvedValue({ ok: true })
  return { client, get, set }
}

function row(container: HTMLElement, key: EffectKey): HTMLElement {
  return container.querySelector(`.st-policy__row[data-effect="${key}"]`) as HTMLElement
}

/** The row's decision select is its FIRST select — the scope rows that follow
 *  carry kind selects of their own. */
function decisionSelect(container: HTMLElement, key: EffectKey): HTMLSelectElement {
  return row(container, key).querySelector('select') as HTMLSelectElement
}

async function waitForRows(container: HTMLElement): Promise<void> {
  await vi.waitFor(() => {
    expect(container.querySelectorAll('.st-policy__row').length).toBeGreaterThan(0)
  })
}

describe('agent policy surface', () => {
  it('has no Save button: a decision change writes at once and the page adopts what the store returned', async () => {
    // The store takes the write and then answers something ELSE than what was
    // sent. The page must show the store's answer: that is the whole content
    // of "it can never show a policy the store did not take".
    const storeAnswered: PolicyMatrix = {
      ...blankPolicy(),
      observe: { decision: 'refuse', scopes: [] },
    }
    const { client, get, set } = fakeClient([view(blankPolicy()), view(storeAnswered)])
    const container = mount(client)
    await waitForRows(container)

    expect(within(container).queryByRole('button', { name: /save/i })).toBeNull()

    fireEvent.change(decisionSelect(container, 'observe'), { target: { value: 'permit' } })

    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe.decision).toBe('permit')
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => {
      expect(decisionSelect(container, 'observe').value).toBe('refuse')
    })
  })

  it('a refused write toasts and re-reads: the page never shows a policy the store did not take', async () => {
    const { client, get } = fakeClient([view(blankPolicy())], new Error('config domain busy'))
    const container = mount(client)
    await waitForRows(container)

    fireEvent.change(decisionSelect(container, 'observe'), { target: { value: 'permit' } })

    await vi.waitFor(() => {
      expect(
        toasts().some((t) => t.level === 'danger' && t.message.includes('config domain busy')),
      ).toBe(true)
    })
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => {
      expect(decisionSelect(container, 'observe').value).toBe('ask')
    })
  })

  it('a scope field writes on blur, not on every keystroke', async () => {
    // ParseEffectPolicy rejects a non-absolute path, so a per-keystroke write
    // would be a refused write and a toast on every character of "/workspace".
    const { client, set } = fakeClient([view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    fireEvent.click(within(observe).getByRole('button', { name: 'Scope' }))
    const field = within(observe).getAllByRole('textbox')[0]

    fireEvent.input(field, { target: { value: '/w' } })
    fireEvent.input(field, { target: { value: '/workspace' } })
    expect(set).not.toHaveBeenCalled()

    fireEvent.blur(field)
    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe.scopes).toEqual([{ kind: 'path', id: '/workspace' }])
  })

  // TYPING MUST NOT REBUILD THE FIELD. Solid's `For` is keyed by REFERENCE,
  // and editing a scope mints a new scope object, so a `For` over the scopes
  // tore the row down and rebuilt it on every keystroke — taking the focused
  // input with it. A person could type one character of "/workspace" and then
  // be typing into nothing. The assertion is the node's identity, because
  // that is what focus follows.
  it('typing a path keeps the same field: the row is not rebuilt per keystroke', async () => {
    const { client } = fakeClient([view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    fireEvent.click(within(observe).getByRole('button', { name: 'Scope' }))
    const field = within(observe).getAllByRole('textbox')[0]
    field.focus()

    fireEvent.input(field, { target: { value: '/w' } })
    fireEvent.input(field, { target: { value: '/wo' } })

    expect(within(observe).getAllByRole('textbox')[0]).toBe(field)
    expect(document.activeElement).toBe(field)
  })

  it('a scope field writes on Enter, without leaving the field', async () => {
    const { client, set } = fakeClient([view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    fireEvent.click(within(observe).getByRole('button', { name: 'Scope' }))
    const field = within(observe).getAllByRole('textbox')[0]
    fireEvent.input(field, { target: { value: '/workspace' } })
    fireEvent.keyDown(field, { key: 'Enter' })

    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe.scopes).toEqual([{ kind: 'path', id: '/workspace' }])
  })

  it('a scope nobody typed into is never written: leaving a blank field is not a change', async () => {
    const { client, set } = fakeClient([view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    fireEvent.click(within(observe).getByRole('button', { name: 'Scope' }))
    fireEvent.blur(within(observe).getAllByRole('textbox')[0])

    await new Promise((r) => setTimeout(r, 0))
    expect(set).not.toHaveBeenCalled()
  })

  it('draws only the effect classes a declared tool can produce today', async () => {
    const { client } = fakeClient([view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    // Five of the seven classes have no tool behind them: nothing can produce
    // them, so there is nothing to decide about them and a row would say
    // otherwise. The backend's `live` list is the authority — when a tool
    // carrying one is declared, its row appears without anybody editing this
    // page.
    const visible = [...container.querySelectorAll('.st-policy__row')].map((r) =>
      r.getAttribute('data-effect'),
    )
    expect(visible).toEqual(['observe', 'mutate-destructive'])
    expect(container.querySelector('.st-policy__dormant')).toBeNull()
  })

  it('a class nothing can produce still shows the answer somebody already gave it', async () => {
    // An answer nobody can see is an answer nobody can take back. This one
    // predates the tool it would govern — the row stays on the page, saying
    // it governs nothing, until the person sets it back to Ask.
    const answered: PolicyMatrix = {
      ...blankPolicy(),
      delegate: { decision: 'refuse', scopes: [] },
    }
    const { client, set } = fakeClient([view(answered), view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    const group = container.querySelector('.st-policy__dormant') as HTMLElement
    expect(group).toBeTruthy()
    expect(group.textContent).toMatch(/does not have yet/i)
    // Visible without a disclosure to open: hiding a standing answer behind
    // one is the same defect as not drawing it at all.
    const delegate = within(group).getByText('Hand work to another agent')
    expect(delegate).toBeTruthy()
    expect(group.textContent).toContain('Never')

    // And it can be taken back, which is the whole reason it is drawn.
    fireEvent.change(decisionSelect(container, 'delegate'), { target: { value: 'ask' } })
    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].delegate.decision).toBe('ask')
    // The second read answers the blank policy: nothing is left to say.
    await vi.waitFor(() => {
      expect(container.querySelector('.st-policy__dormant')).toBeNull()
    })
  })

  it('a row off the default says what it means, in the same words the prompt used', async () => {
    const { client } = fakeClient([view(LOADED)])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    await vi.waitFor(() => {
      expect(observe.textContent).toContain('Read and inspect')
    })
    expect(observe.textContent).toContain('Allowed')

    // The default row says nothing extra: silence is the normal state.
    const destructive = row(container, 'mutate-destructive')
    expect(destructive.querySelector('.st-policy__state')).toBeTruthy()
    expect(observe.querySelectorAll('.st-policy__state')).toHaveLength(1)
  })

  it('a row on the default carries no standing-decision line', async () => {
    const { client } = fakeClient([view(blankPolicy())])
    const container = mount(client)
    await waitForRows(container)

    expect(row(container, 'observe').querySelector('.st-policy__state')).toBeNull()
  })

  it("says nothing in the ADR's words", async () => {
    // Every class on the page at once, live or merely answered, so the
    // vocabulary check covers the sentences both of them carry.
    const answeredEverywhere: PolicyMatrix = blankPolicy()
    for (const k of EFFECT_KEYS) answeredEverywhere[k] = { decision: 'refuse', scopes: [] }
    const { client } = fakeClient([view(answeredEverywhere)])
    const container = mount(client)
    await waitForRows(container)
    await vi.waitFor(() => {
      expect(container.querySelectorAll('.st-policy__row')).toHaveLength(7)
    })

    const text = (container.textContent ?? '').toLowerCase()
    for (const word of ['effect class', 'resource scope', 'is refused', 'rows nobody set']) {
      expect(text).not.toContain(word)
    }
  })

  // THE ROW IS A GRID AND THE SCOPES ARE ONE OF ITS CELLS (nocx-c72pl).
  // Emitted as direct children of the grid, the second scope wrapped into the
  // next grid row's FIRST column — the effect-label column — so it rendered
  // visibly narrower than the first and read as though it belonged to a
  // different effect. The scopes and their add control are one group and must
  // share one cell, so the assertion is structural: every scope of a row has
  // the same parent, and that parent is not the row itself.
  it('keeps every scope of a row in one container, not spread across grid cells', async () => {
    const twoScopes: PolicyMatrix = {
      ...LOADED,
      observe: {
        decision: 'permit',
        scopes: [
          { kind: 'path', id: '/workspace' },
          { kind: 'path', id: '/srv' },
        ],
      },
    }
    const { client } = fakeClient([view(twoScopes)])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    await vi.waitFor(() => {
      expect(observe.querySelectorAll('.st-policy__scope')).toHaveLength(2)
    })
    const scopes = Array.from(observe.querySelectorAll('.st-policy__scope'))
    const parents = new Set(scopes.map((s) => s.parentElement))
    expect(parents.size).toBe(1)
    expect(scopes[0].parentElement).not.toBe(observe)
  })

  it('removes a scope, and the removal is the write', async () => {
    const { client, set } = fakeClient([view(LOADED)])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    await vi.waitFor(() => {
      expect(within(observe).queryByDisplayValue('/workspace')).not.toBeNull()
    })
    fireEvent.click(within(observe).getByRole('button', { name: /remove/i }))

    await vi.waitFor(() => expect(set).toHaveBeenCalledTimes(1))
    expect(set.mock.calls[0][0].observe.scopes).toEqual([])
  })

  it('offers no tool scope kind — the grant never names tools', async () => {
    const { client } = fakeClient([view(LOADED)])
    const container = mount(client)
    await waitForRows(container)

    const observe = row(container, 'observe')
    await vi.waitFor(() => {
      expect(within(observe).queryByDisplayValue('/workspace')).not.toBeNull()
    })
    const selects = observe.querySelectorAll('select')
    const kinds = selects[selects.length - 1]
    const options = Array.from(kinds.querySelectorAll('option')).map((o) => o.value)
    expect(options).not.toContain('tool')
    expect(options).toContain('path')
  })

  it('says the read failed rather than drawing a policy nobody sent', async () => {
    const client = new PolicyClient(new Dispatcher())
    vi.spyOn(client, 'get').mockRejectedValue(new Error('store unavailable'))
    const container = mount(client)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('store unavailable')
    })
    expect(container.querySelectorAll('.st-policy__row')).toHaveLength(0)
  })
})
