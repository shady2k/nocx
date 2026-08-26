// @vitest-environment jsdom
/**
 * Component-level acceptance for the model roles surface (nocx-e6kn2,
 * nocx-rikz5).
 *
 * Drives the real RolesSection the way a person drives it — the selects,
 * queried through the container exactly like the other section tests —
 * and asserts the product rules the beads name: every role of the closed
 * set is a visible row; a role with no assignment of its own reads "As
 * default"; assigning a pair reaches roles.assign with exactly that pair;
 * and the state sentence carries the same meaning the ask transaction
 * refuses on (deleted endpoint, removed model).
 *
 * The line's rule is what these tests hold: it speaks ONLY to refuse. A role
 * that resolves — through its own pair or through the default — gets no line
 * at all, because the page already says both: the two selects show an own
 * pair, and the Default model control above shows the pair "As default"
 * reads through. Repeating either under every role was noise the owner
 * struck (nocx-rikz5). What remains is the vocabulary the ask refuses on.
 */
import { describe, it, expect, vi, afterEach, type MockInstance } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { RolesSection, roleStateLine } from './roles-section'
import { EndpointClient, type Endpoint, type RoleAssignInput } from './endpoints'
import { Dispatcher } from './dispatcher'
import { clearToasts, toasts } from './ui'
import type { Role, RolesListResult } from './generated/roles.list'
import type { RolesAssignResult } from './generated/roles.assign'

type WireDefault = RolesListResult['default']

afterEach(() => {
  clearToasts()
  vi.clearAllMocks()
  cleanup()
  document.body.innerHTML = ''
})

function ep(id: string, name: string, models: string[]): Endpoint {
  return {
    id,
    name,
    baseUrl: `https://${name}.example.com/v1`,
    schema: 'openai-compatible',
    noKey: false,
    credential: null,
    models: models.map((m) => ({ name: m, alias: null })),
    headers: [],
  }
}

function role(
  role: 'answering' | 'classifier',
  endpointId: string | null,
  model: string | null,
): Role {
  return { role, endpointId, model }
}

/** Mount the section and return the container plus the spied client. */
function mountRoles(
  endpoints: Endpoint[],
  roles: Role[],
  def: WireDefault = null,
): {
  client: EndpointClient
  assignRole: MockInstance<(input: RoleAssignInput) => Promise<RolesAssignResult>>
  container: HTMLElement
} {
  const client = new EndpointClient(new Dispatcher())
  // The client returns the WHOLE result (bead nocx-rikz5) — the table AND
  // the default in one answer — so the mocks return it too.
  const assignRole = vi.spyOn(client, 'assignRole').mockImplementation((input) =>
    Promise.resolve({
      roles: [...roles].map((r) =>
        r.role === input.role ? { ...r, endpointId: input.endpointId, model: input.model } : r,
      ),
      default: def,
    }),
  )
  vi.spyOn(client, 'listRoles').mockImplementation(() =>
    Promise.resolve({ roles: [...roles], default: def }),
  )
  vi.spyOn(client, 'listEndpoints').mockImplementation(() => Promise.resolve([...endpoints]))
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <RolesSection client={client} />, { container })
  return { client, assignRole, container }
}

/**
 * A client over a dispatcher whose JSON-RPC calls are answered by method
 * NAME — the seam a real page talks through. Nothing here spies on
 * EndpointClient, so a page that calls the wrong method, or drops what the
 * wire returned, fails rather than passes: the only observation these tests
 * make afterwards is what the page renders.
 */
function seam(replies: Record<string, () => unknown>): {
  client: EndpointClient
  sent: { method: string; params: unknown }[]
} {
  const dispatcher = new Dispatcher()
  const sent: { method: string; params: unknown }[] = []
  vi.spyOn(dispatcher, 'call').mockImplementation((method: string, params: unknown) => {
    sent.push({ method, params })
    const reply = replies[method]
    if (reply === undefined) {
      return Promise.reject(new Error(`unexpected JSON-RPC method: ${method}`))
    }
    return Promise.resolve(reply())
  })
  return { client: new EndpointClient(dispatcher), sent }
}

function mountClient(client: EndpointClient): HTMLElement {
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <RolesSection client={client} />, { container })
  return container
}

function roleRows(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('.roles-role'))
}

/** The "Default model" control — the one block above the role rows. */
function defaultControl(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>('.roles-default')
  if (!el) throw new Error('no default-model control on the page')
  return el
}

/** The endpoint select and the model select of one block. */
function selects(row: HTMLElement): [HTMLSelectElement, HTMLSelectElement] {
  const els = row.querySelectorAll<HTMLSelectElement>('select')
  if (els.length !== 2) throw new Error(`expected 2 selects, got ${els.length}`)
  return [els[0], els[1]]
}

function text(row: HTMLElement): string {
  return row.textContent ?? ''
}

/** The labels a select actually offers, in order. */
function optionLabels(select: HTMLSelectElement): string[] {
  return Array.from(select.options).map((o) => o.textContent ?? '')
}

/** Pick an (endpoint, model) pair through a block's two selects, the way a
 *  person does: the endpoint first, then the model it offers. */
async function pickPair(block: HTMLElement, endpointId: string, model: string): Promise<void> {
  const [endpointSelect, modelSelect] = selects(block)
  fireEvent.change(endpointSelect, { target: { value: endpointId } })
  await vi.waitFor(() => {
    expect(Array.from(modelSelect.options).map((o) => o.value)).toContain(model)
  })
  fireEvent.change(modelSelect, { target: { value: model } })
}

describe('the closed role set is visible', () => {
  it('renders every role of the wire as a row — an unassigned role is a row, never absent', async () => {
    const { container } = mountRoles(
      [],
      [role('answering', null, null), role('classifier', null, null)],
    )
    await vi.waitFor(() => {
      expect(roleRows(container).length).toBe(2)
    })
    expect(text(roleRows(container)[0])).toContain('Answering')
    expect(text(roleRows(container)[1])).toContain('Classifier')
  })

  it('an unassigned role with no default to fall back on shows the no-model warning', async () => {
    const { container } = mountRoles(
      [ep('e1', 'OpenAI', ['gpt-4o'])],
      [role('answering', null, null), role('classifier', null, null)],
    )
    await vi.waitFor(() => {
      expect(roleRows(container).length).toBe(2)
    })
    expect(text(roleRows(container)[0])).toMatch(/No model assigned/)
  })
})

describe('roleStateLine — the sentence the row and the backend share', () => {
  const eps = [ep('e1', 'OpenAI', ['gpt-4o', 'gpt-4o-mini']), ep('e2', 'Local', ['qwen3'])]

  it('says nothing for a role that names its own resolvable pair — the two selects already show it', () => {
    expect(roleStateLine(role('answering', 'e2', 'qwen3'), null, eps)).toBeNull()
    // And still nothing when a default exists: the role does not use it.
    expect(
      roleStateLine(role('answering', 'e2', 'qwen3'), { endpointId: 'e1', model: 'gpt-4o' }, eps),
    ).toBeNull()
  })

  it('an unassigned role with no default is a warning — the visible failure the ask refuses on', () => {
    const line = roleStateLine(role('answering', null, null), null, eps)
    expect(line?.tone).toBe('warning')
  })

  it('says NOTHING for a role that resolves through the default — the Default model control names the pair once', () => {
    expect(
      roleStateLine(role('answering', null, null), { endpointId: 'e2', model: 'qwen3' }, eps),
    ).toBeNull()
  })

  it('a deleted endpoint is an error that says so — never a hop to a neighbour', () => {
    const line = roleStateLine(role('answering', 'e9-gone', 'gpt-4o'), null, eps)
    expect(line?.tone).toBe('error')
    expect(line?.text).toMatch(/endpoint.*no longer exists/)
  })

  it('a removed model is an error that names the model and the endpoint', () => {
    const line = roleStateLine(role('classifier', 'e2', 'gpt-4o'), null, eps)
    expect(line?.tone).toBe('error')
    expect(line?.text).toContain('gpt-4o')
    expect(line?.text).toContain('Local')
  })

  it('names the failing rung when it is the DEFAULT that no longer resolves', () => {
    const gone = roleStateLine(
      role('answering', null, null),
      { endpointId: 'e9-gone', model: 'x' },
      eps,
    )
    expect(gone?.tone).toBe('error')
    expect(gone?.text).toMatch(/endpoint.*no longer exists/)

    const dropped = roleStateLine(
      role('answering', null, null),
      { endpointId: 'e2', model: 'gpt-4o' },
      eps,
    )
    expect(dropped?.tone).toBe('error')
    expect(dropped?.text).toContain('gpt-4o')
    expect(dropped?.text).toContain('Local')
  })
})

describe('assigning a model to a role in the product', () => {
  it('picking an endpoint then a model reaches roles.assign with EXACTLY that pair — and never a half-pair', async () => {
    const eps = [ep('e1', 'OpenAI', ['gpt-4o']), ep('e2', 'Local', ['qwen3'])]
    const roles = [role('answering', null, null), role('classifier', null, null)]
    const { container, assignRole } = mountRoles(eps, roles)
    await vi.waitFor(() => expect(roleRows(container).length).toBe(2))

    await pickPair(roleRows(container)[0], 'e2', 'qwen3')

    await vi.waitFor(() => {
      expect(assignRole).toHaveBeenCalledWith({
        role: 'answering',
        endpointId: 'e2',
        model: 'qwen3',
      })
    })
    // Exactly ONE write: the half-pair (endpoint, no model) never went out.
    expect(assignRole).toHaveBeenCalledTimes(1)
  })

  it('does not repeat the selects: an explicitly assigned, resolving role gets no status line', async () => {
    const eps = [ep('e1', 'OpenAI', ['gpt-4o'])]
    const { container } = mountRoles(eps, [
      role('answering', 'e1', 'gpt-4o'),
      role('classifier', null, null),
    ])
    await vi.waitFor(() => {
      // The selects say it, and they are the ones that say it.
      expect(selects(roleRows(container)[0])[0].value).toBe('e1')
    })
    expect(selects(roleRows(container)[0])[1].value).toBe('gpt-4o')
    expect(roleRows(container)[0].querySelector('.roles-role__state')).toBeNull()
    expect(text(roleRows(container)[0])).not.toContain('gpt-4o · ')
  })

  it('offers "As default" on the endpoint select, and shows it for a role with no assignment of its own', async () => {
    const eps = [ep('e1', 'OpenAI', ['gpt-4o'])]
    const { container } = mountRoles(eps, [
      role('answering', null, null),
      role('classifier', 'e1', 'gpt-4o'),
    ])
    await vi.waitFor(() => expect(roleRows(container).length).toBe(2))
    const unassigned = selects(roleRows(container)[0])[0]
    expect(optionLabels(unassigned)).toContain('As default')
    expect(unassigned.value).toBe('')
    expect(unassigned.options[unassigned.selectedIndex].textContent).toBe('As default')
  })

  it('choosing "As default" on the endpoint drops the role\'s own assignment', async () => {
    const eps = [ep('e1', 'OpenAI', ['gpt-4o'])]
    const roles = [role('answering', 'e1', 'gpt-4o'), role('classifier', null, null)]
    const { container, assignRole } = mountRoles(eps, roles)
    await vi.waitFor(() => {
      expect(selects(roleRows(container)[0])[0].value).toBe('e1')
    })
    fireEvent.change(selects(roleRows(container)[0])[0], { target: { value: '' } })
    await vi.waitFor(() => {
      expect(assignRole).toHaveBeenCalledWith({ role: 'answering', endpointId: null, model: null })
    })
  })

  it('an assigned row whose endpoint was deleted renders the unresolvable error, and reassignment is possible', async () => {
    // The wire still carries the assignment; the endpoint list no longer
    // has it. The row must SAY so, and offer a working replacement.
    const eps = [ep('e2', 'Local', ['qwen3'])]
    const roles = [role('answering', 'e9-gone', 'gpt-4o'), role('classifier', null, null)]
    const { container, assignRole } = mountRoles(eps, roles)
    await vi.waitFor(() => {
      expect(text(roleRows(container)[0])).toMatch(/endpoint.*no longer exists/)
    })
    // The model select offers only the placeholder until a replacement
    // endpoint is picked (the gone endpoint has no models to offer).
    expect(Array.from(selects(roleRows(container)[0])[1].options)).toHaveLength(1)
    await pickPair(roleRows(container)[0], 'e2', 'qwen3')
    // The reassignment write carries the NEW pair, not the dangling one.
    await vi.waitFor(() => {
      expect(assignRole).toHaveBeenCalledWith({
        role: 'answering',
        endpointId: 'e2',
        model: 'qwen3',
      })
    })
  })
})

describe('the default model — chosen here, and read through by every role that has no pair', () => {
  it('adopts the default the wire returns, and every unassigned role then reads through it', async () => {
    // Through the dispatcher seam, not a spy on the client: the assertion is
    // that the PAGE changed, which is the thing a person can see. A test
    // asserting only that setDefault was called stays green when the
    // returned state is dropped, when the control never updates, and when
    // the method name is wrong.
    const unassigned = [role('answering', null, null), role('classifier', null, null)]
    const { client, sent } = seam({
      'roles.list': () => ({ roles: unassigned, default: null }),
      'endpoints.list': () => ({ endpoints: [ep('e1', 'openrouter', ['m-a'])] }),
      'roles.setDefault': () => ({
        roles: unassigned,
        default: { endpointId: 'e1', model: 'm-a' },
      }),
    })
    const container = mountClient(client)
    await vi.waitFor(() => expect(roleRows(container).length).toBe(2))
    // Before: nothing to fall back on, and both rows say exactly that.
    expect(text(roleRows(container)[0])).toMatch(/No model assigned/)

    await pickPair(defaultControl(container), 'e1', 'm-a')

    // The control holds what the STORE took, not what was typed at it...
    const [defEndpoint, defModel] = selects(defaultControl(container))
    await vi.waitFor(() => expect(defEndpoint.value).toBe('e1'))
    expect(defModel.value).toBe('m-a')
    // ...and every role with no pair of its own now resolves through it and
    // falls SILENT: the warning that it could not be used is gone, and the
    // pair is named once, by the control above, not again under each row.
    for (const row of roleRows(container)) {
      expect(row.querySelector('.roles-role__state')).toBeNull()
    }
    expect(sent.filter((s) => s.method === 'roles.setDefault')).toEqual([
      { method: 'roles.setDefault', params: { endpointId: 'e1', model: 'm-a' } },
    ])
  })

  it('a person reaches a working assistant from this page alone: set the default, every role reads "As default"', async () => {
    const unassigned = [role('answering', null, null), role('classifier', null, null)]
    const { client } = seam({
      'roles.list': () => ({ roles: unassigned, default: null }),
      'endpoints.list': () => ({ endpoints: [ep('e1', 'openrouter', ['m-a'])] }),
      'roles.setDefault': () => ({
        roles: unassigned,
        default: { endpointId: 'e1', model: 'm-a' },
      }),
    })
    const container = mountClient(client)
    await vi.waitFor(() => expect(roleRows(container).length).toBe(2))
    await pickPair(defaultControl(container), 'e1', 'm-a')

    await vi.waitFor(() => {
      for (const row of roleRows(container)) {
        const endpointSelect = selects(row)[0]
        expect(endpointSelect.options[endpointSelect.selectedIndex].textContent).toBe('As default')
      }
    })
  })

  it('a refused setDefault keeps the default the store took on screen, and says why', async () => {
    const roles = [role('answering', null, null)]
    const { client } = seam({
      'roles.list': () => ({ roles, default: { endpointId: 'e1', model: 'm-a' } }),
      'endpoints.list': () => ({
        endpoints: [ep('e1', 'openrouter', ['m-a']), ep('e2', 'Local', ['qwen3'])],
      }),
      'roles.setDefault': () => {
        throw new Error('config domain busy')
      },
    })
    const container = mountClient(client)
    await vi.waitFor(() => expect(roleRows(container).length).toBe(1))
    const [defEndpoint, defModel] = selects(defaultControl(container))
    expect(defEndpoint.value).toBe('e1')

    await pickPair(defaultControl(container), 'e2', 'qwen3')

    await vi.waitFor(() => {
      expect(
        toasts().some((t) => t.level === 'danger' && t.message.includes('config domain busy')),
      ).toBe(true)
    })
    // The page never shows a default the store did not take.
    await vi.waitFor(() => expect(defEndpoint.value).toBe('e1'))
    expect(defModel.value).toBe('m-a')
    // And the role still resolves through it, so it still says nothing.
    expect(roleRows(container)[0].querySelector('.roles-role__state')).toBeNull()
  })
})
