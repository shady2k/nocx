// @vitest-environment jsdom
// The API workbench, as a user reaches it.
//
// AGENTS.md testing rule 1 is the bar, and it was bought by a connection
// manager that shipped with NO WAY TO CREATE A GROUP while 1041 frontend
// tests were green — every one of them mounting a component and asserting
// what it rendered. So nothing here calls the registry factory or the store
// directly to open the surface: the entry is clicked on a real activity bar,
// wired to a real PaneManager and a real SurfaceRegistry, and the assertions
// are about what appears on screen afterwards.
//
// What is faked is exactly one thing: the eight api.* calls. Everything
// between the click and the DOM — sidebar, registry, pane manager, pane
// lifecycle, Solid root, kit components — is the real code.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { SurfaceRegistry, SURFACE_ID_API } from '../surface-registry'
import { createRendererMock, makeClient, mountPaneManager } from '../test-support/panes-fixtures'
import { ApiContent, SINGLETON_API } from './api-content'
import { apiSidebarAction, openApiWorkbench, registerApiSurface } from './index'
import type { ApiWorkbenchServices } from './api-client'
import type { PaneHost } from '../pane-content'
import {
  CREATE_REL_PATH,
  HANDLE,
  LIST_REL_PATH,
  REQUEST,
  SECRET_PLACEHOLDER,
  SECRET_VALUE,
  collectionsFixture,
  exchangeFixture,
  sendFixture,
  servicesFixture,
} from './api-test-fixtures'
import { createSecretChip } from '../ui/secret-chip'

vi.mock('../renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

const liveHandles: SidebarHandle[] = []

afterEach(() => {
  for (const h of liveHandles) h.destroy()
  liveHandles.length = 0
  cleanup()
  document.body.replaceChildren()
})

// ── The app, as far as this surface needs one ─────────────────────────────

async function mountApp(over: Partial<ApiWorkbenchServices> = {}) {
  const { manager } = await mountPaneManager(makeClient())
  const registry = new SurfaceRegistry()
  registerApiSurface(registry, manager, servicesFixture(over))

  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  const handle = mountSidebar(bar, panel, [], [apiSidebarAction()])
  liveHandles.push(handle)
  return { manager, registry, bar, panel }
}

function apiEntry(bar: HTMLElement): HTMLButtonElement {
  const el = bar.querySelector<HTMLButtonElement>('button[data-action="api"]')
  if (!el) throw new Error('no API entry on the activity bar')
  return el
}

/** The workbench that is actually on screen. */
function workbench(): HTMLElement {
  const el = document.querySelector<HTMLElement>('.api-workbench')
  if (!el) throw new Error('the workbench is not on screen')
  return el
}

function row(relPath: string): HTMLElement {
  const el = workbench().querySelector<HTMLElement>(`[data-rel-path="${relPath}"]`)
  if (!el) throw new Error(`no tree row for ${relPath}`)
  return el
}

function field(name: string): HTMLInputElement | HTMLTextAreaElement {
  const el = workbench().querySelector<HTMLInputElement>(`#${name}`)
  if (!el) throw new Error(`no field ${name}`)
  return el
}

function control(field: string): HTMLSelectElement {
  const el = workbench().querySelector<HTMLSelectElement>(`[data-api-field="${field}"] select`)
  if (!el) throw new Error(`no control for ${field}`)
  return el
}

function button(name: string): HTMLButtonElement {
  const found = [...workbench().querySelectorAll('button')].find(
    (b) => (b.getAttribute('aria-label') ?? b.textContent ?? '').trim() === name,
  )
  if (!found) throw new Error(`no button named ${name}`)
  return found
}

function runCards(): HTMLElement[] {
  return [...workbench().querySelectorAll<HTMLElement>('.api-run')]
}

/** The secret chips inside one run's raw view. */
function rawChips(card: HTMLElement): HTMLElement[] {
  return [...card.querySelectorAll<HTMLElement>('.ui-secret-chip')]
}

/** What one raw block reads as, end to end — text runs and chips together.
 *  This is the assertion that catches a dropped span: anything the walk
 *  fails to emit is simply missing from the string. */
function rawBlockText(card: HTMLElement, label: string): string {
  const block = card.querySelector<HTMLElement>(`[aria-label="${label}"]`)
  if (!block) throw new Error(`no ${label} block on this run`)
  return block.textContent ?? ''
}

/** One run's pretty/raw choice, by the words on it. */
function optionIn(card: HTMLElement, label: string): HTMLElement {
  const found = [...card.querySelectorAll<HTMLElement>('[role="radio"]')].find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`no ${label} option on this run`)
  return found
}

/** Open the workbench the way a person does, and wait for the tree. */
async function openWorkbench(bar: HTMLElement): Promise<void> {
  apiEntry(bar).click()
  await vi.waitFor(() => workbench())
}

/** Open the workbench and put the design's worked example in the form. */
async function openRequest(bar: HTMLElement): Promise<void> {
  await openWorkbench(bar)
  await vi.waitFor(() => row(CREATE_REL_PATH))
  fireEvent.click(row(CREATE_REL_PATH))
  await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))
}

// ── The entry ─────────────────────────────────────────────────────────────

describe('the API workbench is reachable', () => {
  it('the activity bar carries an API entry, enabled from a cold start', async () => {
    const { bar } = await mountApp()
    const entry = apiEntry(bar)
    expect(entry.disabled).toBe(false)
    expect(entry.getAttribute('aria-label')).toBe('API')
  })

  it('activating it opens the workbench and never touches the side panel', async () => {
    const { bar, panel, manager } = await mountApp()
    const before = manager.paneCount
    const collapsedBefore = panel.classList.contains('collapsed')

    apiEntry(bar).click()

    await vi.waitFor(() => expect(manager.paneCount).toBe(before + 1))
    await vi.waitFor(() => workbench())
    expect(panel.classList.contains('collapsed')).toBe(collapsedBefore)
  })

  it('activating it twice yields one pane — the workbench is a singleton', async () => {
    const { bar, manager } = await mountApp()
    const before = manager.paneCount
    apiEntry(bar).click()
    await vi.waitFor(() => expect(manager.paneCount).toBe(before + 1))
    apiEntry(bar).click()
    await vi.waitFor(() => workbench())
    expect(manager.paneCount).toBe(before + 1)
    expect(document.querySelectorAll('.api-workbench')).toHaveLength(1)
  })

  it('the registry entry and the opener agree on the singleton key', async () => {
    const { registry } = await mountApp()
    expect(registry.build(SURFACE_ID_API).descriptor.singletonKey).toBe(SINGLETON_API)
  })

  it('the entry and the opener reach the SAME pane, never a second one', async () => {
    const { bar, manager } = await mountApp()
    await openWorkbench(bar)
    const after = manager.paneCount

    // openApiWorkbench is what main.tsx's entry calls; the singleton key is
    // what makes the second ask a focus rather than a second workbench.
    openApiWorkbench()

    await vi.waitFor(() => expect(document.querySelectorAll('.api-workbench')).toHaveLength(1))
    expect(manager.paneCount).toBe(after)
  })
})

// ── The tree, the form, and Send ──────────────────────────────────────────

describe('a request goes out from the workbench', () => {
  it('the open collections and their requests are on screen when it opens', async () => {
    const { bar } = await mountApp()
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('acme-api'))
    expect(workbench().textContent).toContain('create')
    expect(workbench().textContent).toContain('list')
  })

  it('opening a collection request puts the method and the URL in the form', async () => {
    const { bar } = await mountApp()
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(row(CREATE_REL_PATH))

    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))
    expect(control('method').value).toBe('POST')
  })

  it('a folder that could not be re-read says so in the tree', async () => {
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({
        collections: [collectionsFixture({ error: 'the folder was replaced' })],
      }),
    })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('the folder was replaced'))
  })

  it('a file the format does not recognise is visible, with what was wrong', async () => {
    const bad = collectionsFixture()
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          {
            ...bad,
            collection: {
              ...bad.collection,
              malformed: [{ relPath: 'users/oops.json', reason: 'unexpected end of JSON input' }],
            },
          },
        ],
      }),
    })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('oops.json'))
    expect(workbench().textContent).toContain('unexpected end of JSON input')
  })

  it('pressing Send reaches the client method and the run appears afterwards', async () => {
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { bar } = await mountApp({ sendRequest: send })
    await openRequest(bar)

    fireEvent.click(button('Send'))

    await vi.waitFor(() => expect(send).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
  })

  it('the run shows the status, the elapsed time and the size', async () => {
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const text = runCards()[0].textContent ?? ''
    expect(text).toContain('201')
    expect(text).toContain('184ms')
    expect(text).toContain('1.2KB')
  })

  it('a second Send adds a second run rather than replacing the first', async () => {
    const send = vi
      .fn()
      .mockResolvedValueOnce(sendFixture({ status: 201 }))
      .mockResolvedValueOnce(sendFixture({ status: 422, size: 310 }))
    const { bar } = await mountApp({ sendRequest: send })
    await openRequest(bar)

    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(2))

    expect(runCards()[0].textContent).toContain('422')
    expect(runCards()[1].textContent).toContain('201')
  })

  it('a send that fails is a run that says why', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockRejectedValue(new Error('dial tcp 10.0.3.17:443: refused')),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    expect(runCards()[0].textContent).toContain('dial tcp 10.0.3.17:443: refused')
  })

  it('Send is refused while nothing is selected', async () => {
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { bar } = await mountApp({ sendRequest: send })
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    expect(button('Send').disabled).toBe(true)
    expect(send).not.toHaveBeenCalled()
  })

  it('switching to another request re-reads it', async () => {
    const read = vi.fn().mockResolvedValue({ request: REQUEST })
    const { bar } = await mountApp({ readRequest: read })
    await openRequest(bar)
    fireEvent.click(row(LIST_REL_PATH))
    await vi.waitFor(() => expect(read).toHaveBeenCalledWith(HANDLE, LIST_REL_PATH))
  })
})

// ── Raw, and how a body is described ──────────────────────────────────────

describe('the raw view and the body', () => {
  it('raw shows the request text and the response text, off the wire', async () => {
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    fireEvent.click(optionIn(runCards()[0], 'Raw'))

    await vi.waitFor(() => expect(runCards()[0].textContent).toContain('POST /users HTTP/1.1'))
    const text = runCards()[0].textContent ?? ''
    // The request side, as the BACKEND rendered it — not composed here from
    // the form, which is what this used to be and what §11.2 says it must
    // not be: the sender is the only party that knows what it put on the
    // socket, and a second derivation of it would be a second truth.
    expect(text).toContain('Host: api.internal')
    expect(text).toContain('Content-Type: application/json')
    // …and the response side.
    expect(text).toContain('HTTP/1.1 201 Created')
    expect(text).toContain('{"id":"usr_8f21"}')
  })

  it('a binary body says how many bytes and shows no base64', async () => {
    const { bar } = await mountApp({
      sendRequest: vi
        .fn()
        .mockResolvedValue(sendFixture({ binary: true, text: '', size: 90210, status: 200 })),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const text = runCards()[0].textContent ?? ''
    expect(text).toContain('binary body, 90210 bytes')
    // A base64 payload in a JSON result is the bulk-data-through-a-side-door
    // AD-1 forbids (design §12.3). Nothing that long may appear.
    expect(text).not.toMatch(/[A-Za-z0-9+/]{40,}={0,2}/)
  })

  it('a truncated body says it was cut, and does not read like an empty one', async () => {
    const send = vi
      .fn()
      .mockResolvedValueOnce(sendFixture({ truncated: true, size: 2097152, text: '{"a":1' }))
      .mockResolvedValueOnce(sendFixture({ truncated: false, size: 0, text: '', status: 204 }))
    const { bar } = await mountApp({ sendRequest: send })
    await openRequest(bar)

    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    const truncated = runCards()[0].textContent ?? ''

    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(2))
    const empty = runCards()[0].textContent ?? ''

    expect(truncated).toContain('truncated')
    expect(empty).toContain('empty body')
    expect(empty).not.toContain('truncated')
    expect(truncated).not.toBe(empty)
  })

  it('a body that was not valid text says so rather than passing as clean', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockResolvedValue(sendFixture({ lossy: true, size: 40, text: 'caf�' })),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    expect(runCards()[0].textContent).toContain('not valid text')
  })

  // ── §11.1's three states, on screen ───────────────────────────────────
  //
  // The badge is not a curtain, it is EVIDENCE: it says "this is exactly the
  // secret you named". Which means a reader must be able to tell an intact
  // one from a damaged one, and that distinction is the whole reason the
  // three states exist rather than two.

  it('an intact secret is a chip naming it, and the placeholder is consumed', async () => {
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(optionIn(runCards()[0], 'Raw'))
    await vi.waitFor(() => expect(rawChips(runCards()[0])).toHaveLength(1))

    const chip = rawChips(runCards()[0])[0]
    expect(chip.dataset.variant).toBe('resolved')
    expect(chip.textContent).toContain('API_TOKEN')

    const text = runCards()[0].textContent ?? ''
    // The secret's bytes are nowhere — the backend elided them, and nothing
    // here reconstructs them.
    expect(text).not.toContain(SECRET_VALUE)
    // And the SPAN was actually consumed: a renderer that dumped `raw.text`
    // and ignored spans would leave the placeholder standing here, which is
    // exactly the gap this closes.
    expect(text).not.toContain(SECRET_PLACEHOLDER)
  })

  it('a damaged secret is a visibly different chip carrying the damage', async () => {
    const { bar } = await mountApp({
      sendRequest: vi
        .fn()
        .mockResolvedValue(sendFixture({ raw: exchangeFixture('truncated, 24 of 214 bytes') })),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(optionIn(runCards()[0], 'Raw'))
    await vi.waitFor(() => expect(rawChips(runCards()[0])).toHaveLength(1))

    const chip = rawChips(runCards()[0])[0]
    expect(chip.dataset.variant).toBe('damaged')
    expect(chip.textContent).toContain('API_TOKEN')
    expect(chip.textContent).toContain('truncated, 24 of 214 bytes')

    // Different from the intact rendering by more than its colour — the
    // damage text is on it, and the glyph differs.
    const intact = createSecretChip('API_TOKEN')
    expect(chip.dataset.tone).not.toBe(intact.dataset.tone)
    expect(chip.textContent).not.toBe(intact.textContent)

    const text = runCards()[0].textContent ?? ''
    // The surviving bytes are nowhere: a truncated token is a PREFIX of a
    // live one, so the shape of the damage is all a person may be shown.
    expect(text).not.toContain(SECRET_VALUE)
    expect(text).not.toContain(SECRET_VALUE.slice(0, 8))
    expect(text).not.toContain(SECRET_PLACEHOLDER)
  })

  it('walking the spans reproduces the whole payload — no run is dropped', async () => {
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(optionIn(runCards()[0], 'Raw'))
    await vi.waitFor(() => expect(rawChips(runCards()[0])).toHaveLength(1))

    const wire = exchangeFixture()
    const chipReading = (rawChips(runCards()[0])[0].textContent ?? '').trim()
    // What the reader should end up with: the wire's own text, with the
    // elided placeholder replaced by what the chip says in its place.
    const expected = wire.request.text.replace(SECRET_PLACEHOLDER, chipReading)

    expect(rawBlockText(runCards()[0], 'Raw request')).toBe(expected)
    expect(rawBlockText(runCards()[0], 'Raw response')).toBe(wire.response.text)
  })

  it('a side with nothing to mark still shows all of its text', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockResolvedValue(
        sendFixture({
          raw: {
            request: { text: 'GET /health HTTP/1.1\r\n\r\n', spans: [] },
            response: { text: 'HTTP/1.1 204 No Content\r\n\r\n', spans: [] },
          },
        }),
      ),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(optionIn(runCards()[0], 'Raw'))

    await vi.waitFor(() =>
      expect(rawBlockText(runCards()[0], 'Raw request')).toBe('GET /health HTTP/1.1\r\n\r\n'),
    )
    expect(rawBlockText(runCards()[0], 'Raw response')).toBe('HTTP/1.1 204 No Content\r\n\r\n')
    expect(rawChips(runCards()[0])).toHaveLength(0)
  })

  it('the raw choice belongs to one run, not to the list', async () => {
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(2))

    fireEvent.click(optionIn(runCards()[1], 'Raw'))

    await vi.waitFor(() => expect(runCards()[1].textContent).toContain('HTTP/1.1 201 Created'))
    expect(runCards()[0].textContent).not.toContain('── request ──')
  })
})

// ── The pane's lifecycle ──────────────────────────────────────────────────
//
// One test each for what SolidPaneContent exists to make correct and what a
// bare Solid component would have got wrong silently.

function paneHostFake(over: Partial<PaneHost> = {}): PaneHost {
  return {
    setTitle: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
    contentSettled: vi.fn(),
    ...over,
  }
}

describe('ApiContent lifecycle', () => {
  it('a mount aborted before it completes renders nothing and leaves nothing behind', async () => {
    const content = new ApiContent(servicesFixture())
    const target = document.createElement('div')
    document.body.append(target)
    const ac = new AbortController()
    ac.abort()

    await content.mount(target, paneHostFake(), ac.signal)

    expect(target.querySelector('.api-workbench')).toBeNull()
    expect(target.childElementCount).toBe(0)
    // And disposing an unmounted content is safe.
    content.dispose()
  })

  it('setVisible before the first measurement marks the target without mounting', () => {
    const content = new ApiContent(servicesFixture())
    const target = document.createElement('div')
    document.body.append(target)

    content.setTarget(target)
    content.setVisible(true)

    expect(target.classList.contains('active')).toBe(true)
    expect(target.querySelector('.api-workbench')).toBeNull()
    content.setVisible(false)
    expect(target.classList.contains('active')).toBe(false)
  })

  it('focus lands where the next keystroke belongs, in both states', async () => {
    const content = new ApiContent(servicesFixture())
    const target = document.createElement('div')
    document.body.append(target)
    await content.mount(target, paneHostFake(), new AbortController().signal)
    await vi.waitFor(() => expect(target.querySelector('#api-url')).not.toBeNull())

    // Nothing open: the URL is disabled and cannot take focus, so the
    // keyboard goes to the one thing a person can do from here.
    content.focus()
    expect(document.activeElement).toBe(target.querySelector('#api-collection-path'))

    // With a request in the form it is the URL, which is the field edited
    // between one send and the next.
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() =>
      expect(target.querySelector<HTMLInputElement>('#api-url')?.disabled).toBe(false),
    )
    content.focus()
    expect(document.activeElement).toBe(target.querySelector('#api-url'))

    content.dispose()
  })

  it('dispose tears the surface down, and doing it twice is safe', async () => {
    const content = new ApiContent(servicesFixture())
    const target = document.createElement('div')
    document.body.append(target)
    await content.mount(target, paneHostFake(), new AbortController().signal)
    await vi.waitFor(() => expect(target.querySelector('.api-workbench')).not.toBeNull())

    content.dispose()
    expect(target.querySelector('.api-workbench')).toBeNull()
    expect(() => content.dispose()).not.toThrow()
  })

  it('the pane names itself in the strip', async () => {
    const content = new ApiContent(servicesFixture())
    const setTitle = vi.fn()
    const target = document.createElement('div')
    document.body.append(target)
    await content.mount(target, paneHostFake({ setTitle }), new AbortController().signal)
    expect(setTitle).toHaveBeenCalledWith('API')
    content.dispose()
  })

  it('a listing that fails leaves the surface usable and says why', async () => {
    const content = new ApiContent(
      servicesFixture({
        listCollections: vi.fn().mockRejectedValue(new Error('the api domain is busy')),
      }),
    )
    const target = document.createElement('div')
    document.body.append(target)
    await content.mount(target, paneHostFake(), new AbortController().signal)

    await vi.waitFor(() => expect(target.textContent).toContain('the api domain is busy'))
    expect(target.querySelector('.api-workbench')).not.toBeNull()
    content.dispose()
  })
})
