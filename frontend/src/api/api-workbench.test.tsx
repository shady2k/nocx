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
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { SurfaceRegistry, SURFACE_ID_API } from '../surface-registry'
import { createRendererMock, makeClient, mountPaneManager } from '../test-support/panes-fixtures'
import { API_PANE_TITLE, ApiContent, SINGLETON_API } from './api-content'
import { ArrowRightIcon, ArrowRightLeftIcon } from '../ui/icons'
import { apiSidebarAction, openApiWorkbench, registerApiSurface } from './index'
import type { ApiWorkbenchServices } from './api-client'
import { RpcError } from '../dispatcher'
import type { PaneHost } from '../pane-content'
import type { ApiEnvironmentRef } from './api-model'
import {
  COLLECTION_PATH,
  CREATED_HANDLE,
  CREATED_NAME,
  CREATE_REL_PATH,
  DEV_ENV,
  HANDLE,
  LIST_REL_PATH,
  PROD_ENV,
  REQUEST,
  SECRET_PLACEHOLDER,
  SECRET_VALUE,
  collectionFixture,
  collectionsFixture,
  DEFAULT_ROOT,
  DROP_SESSION,
  nativeDropFixture,
  type NativeDropFixture,
  createdFixture,
  failedSendFixture,
  requestRawFixture,
  responseRawFixture,
  stoppedSendFixture,
  noCollections,
  sendFixture,
  servicesFixture,
  watchFixture,
} from './api-test-fixtures'
import { createSecretChip } from '../ui/secret-chip'
import { clearToasts, toasts } from '../ui/toast'

vi.mock('../renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

const liveHandles: SidebarHandle[] = []

afterEach(() => {
  for (const h of liveHandles) h.destroy()
  liveHandles.length = 0
  cleanup()
  document.body.replaceChildren()
  // The toast queue is module state shared by every test in this file, and
  // the import path below reads it as its account of what the person was
  // told. A queue left standing would let one test read the last one's toast.
  clearToasts()
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

/** True when the element is not sealed inside a closed `<dialog>`.
 *
 *  A closed native dialog keeps its children in the document, so the
 *  workbench holds the controls of BOTH its asks at all times and a plain
 *  `querySelectorAll` would answer with a Cancel the person cannot see. This
 *  is the difference between "rendered" and "reachable", and every helper
 *  below asks for the second. */
function reachable(el: Element): boolean {
  const dialog = el.closest('dialog')
  return dialog === null || dialog.open
}

function button(name: string): HTMLButtonElement {
  const found = [...workbench().querySelectorAll('button')]
    .filter(reachable)
    .find((b) => (b.getAttribute('aria-label') ?? b.textContent ?? '').trim() === name)
  if (!found) throw new Error(`no button named ${name}`)
  return found
}

/** The buttons a person can reach right now, by their words. */
function buttonNames(): string[] {
  return [...workbench().querySelectorAll('button')]
    .filter(reachable)
    .map((b) => (b.getAttribute('aria-label') ?? b.textContent ?? '').trim())
}

/** The dialog that owns a field, by that field's id. Two asks live in this
 *  surface — a name and a folder — so "the dialog" is never a query on its
 *  own. */
function dialogFor(fieldId: string): HTMLDialogElement {
  const el = workbench().querySelector<HTMLInputElement>(`#${fieldId}`)?.closest('dialog')
  if (!el) throw new Error(`no dialog around #${fieldId}`)
  return el
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
/** One of a run's view tabs, by its label. It was a SegmentedControl of two
 *  (Pretty/Raw) and is a tab row of three (Body/Headers/Raw): the headers
 *  used to be stacked above the body in one pane, where a long body pushed
 *  them off screen and a long header list pushed the body off. */
function optionIn(card: HTMLElement, label: string): HTMLElement {
  const found = [...card.querySelectorAll<HTMLElement>('[role="tab"]')].find((b) =>
    (b.textContent ?? '').trim().startsWith(label),
  )
  if (!found) throw new Error(`no ${label} tab on this run`)
  return found
}

/** Which of a run's view tabs is the current one. */
function currentTab(card: HTMLElement): string {
  const on = card.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')
  return (on?.textContent ?? '').trim()
}

/** Open the workbench the way a person does, and wait for the tree. */
async function openWorkbench(bar: HTMLElement): Promise<void> {
  apiEntry(bar).click()
  await vi.waitFor(() => workbench())
}

/** Wait until `files.watch` has RETURNED.
 *
 *  The tree rows say `api.collections.list` came back, which is a different
 *  call — a check that fires a change before the watch is established races
 *  the baseline and is testing nothing. The slot's `data-watch-mode` is the
 *  observable that says the watch is up; that is what it is for. */
async function watching(): Promise<void> {
  await vi.waitFor(() => {
    const slot = workbench().querySelector('[data-testid="api-polling-badge-slot"]')
    if (slot?.getAttribute('data-watch-mode') === null) throw new Error('not watching yet')
  })
}

/** Open the workbench and put the design's worked example in the form. */
async function openRequest(bar: HTMLElement): Promise<void> {
  await openWorkbench(bar)
  await vi.waitFor(() => row(CREATE_REL_PATH))
  fireEvent.click(row(CREATE_REL_PATH))
  await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))
}

// ── The panel notices, instead of asking you to press a button ────────────
//
// The owner's question was "зачем там обновить?" — why is there a Refresh at
// all. The honest answer is that the workbench had no other way to learn a
// folder had changed, while the Files panel two metres away had one. These
// checks are about the panel doing the noticing; nothing below presses
// anything except where the point IS the button.

describe('the workbench notices the folder changed', () => {
  it('a change on disk puts the new request on screen with nothing pressed', async () => {
    const watch = watchFixture()
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const { bar } = await mountApp({ listCollections: list, watchCollections: watch.port })
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    await watching()
    expect(workbench().textContent).not.toContain('delete.json')

    // The colleague's `git pull` lands. The backend is the only thing that
    // says so, through the subscription the store itself registered.
    list.mockResolvedValue({
      collections: [
        collectionsFixture({
          collection: collectionFixture({
            requests: [
              { relPath: CREATE_REL_PATH, name: 'create', method: 'POST' },
              { relPath: 'users/delete.json', name: 'delete', method: 'DELETE' },
            ],
          }),
        }),
      ],
    })
    watch.changed(COLLECTION_PATH)

    await vi.waitFor(() => row('users/delete.json'))
  })

  it('the roots it renders are the set it watches, and it watches them from a local session', async () => {
    const watch = watchFixture()
    const { bar } = await mountApp({ watchCollections: watch.port })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(watch.sets()).toEqual([[COLLECTION_PATH]]))
    expect(watch.open).toHaveBeenCalledTimes(1)
  })

  it('closing the collection takes its folder out of the set the backend holds', async () => {
    const watch = watchFixture()
    const { bar } = await mountApp({ watchCollections: watch.port })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(watch.sets()).toEqual([[COLLECTION_PATH]]))
    // Closing is a MENU ITEM now, not a bare ✕ on the row: one unlabelled
    // click from closing somebody's folder, with nothing between the pointer
    // and the act, is what it replaced.
    fireEvent.click(button('More actions for acme-api'))
    await vi.waitFor(() => menuItem('Close collection'))
    fireEvent.click(menuItem('Close collection'))
    await vi.waitFor(() => expect(watch.lastSet()).toEqual([]))
  })
})

describe('the header says whether the panel is still following the disk', () => {
  it('carries the established mode, which is what says files.watch returned', async () => {
    const watch = watchFixture()
    const { bar } = await mountApp({ watchCollections: watch.port })
    await openWorkbench(bar)
    await vi.waitFor(() =>
      expect(
        workbench()
          .querySelector('[data-testid="api-polling-badge-slot"]')
          ?.getAttribute('data-watch-mode'),
      ).toBe('polling'),
    )
  })

  it('designed-mode polling warns about nothing', async () => {
    const watch = watchFixture()
    const { bar } = await mountApp({ watchCollections: watch.port })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(watch.sets()).toHaveLength(1))
    expect(workbench().querySelector('[data-testid="api-polling-badge"]')).toBeNull()
  })

  it('a degrade with a reason is a persistent badge, and the reason is reachable on it', async () => {
    const call = vi
      .fn()
      .mockResolvedValueOnce({ mode: 'polling', degradedReason: 'inotify watch limit reached' })
    call.mockResolvedValue({ mode: 'watching' })
    const watch = watchFixture({ watch: call })
    const { bar } = await mountApp({ watchCollections: watch.port })
    await openWorkbench(bar)
    const badge = await vi.waitFor(() => {
      const el = workbench().querySelector<HTMLElement>('[data-testid="api-polling-badge"]')
      if (!el) throw new Error('no badge yet')
      return el
    })
    expect(badge.textContent).toBe('Polling')
    expect(badge.getAttribute('title')).toBe('inotify watch limit reached')

    // …and it clears the instant watching recovers, which is the other end of
    // the interval: a warning that outlives its cause teaches the reader to
    // ignore the next one.
    fireEvent.click(button('More collection actions'))
    await vi.waitFor(() => menuItem('Re-read the open folders'))
    fireEvent.click(menuItem('Re-read the open folders'))
    await vi.waitFor(() =>
      expect(workbench().querySelector('[data-testid="api-polling-badge"]')).toBeNull(),
    )
  })

  it('a watch that could not be established is on the surface, with the retry beside it', async () => {
    const call = vi.fn().mockRejectedValueOnce(new Error('watch limit reached'))
    call.mockResolvedValue({ mode: 'watching' })
    const watch = watchFixture({ watch: call })
    const { bar } = await mountApp({ watchCollections: watch.port })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('watch limit reached'))
    fireEvent.click(button('Retry'))
    await vi.waitFor(() => expect(workbench().textContent).not.toContain('watch limit reached'))
  })
})

describe('re-reading the disk is one of the rare actions, in the menu', () => {
  // It sat in a header of its own across the top of the pane, which is where
  // it went when it was the only action the panel had. It is one of four
  // occasional, deliberate acts now — open a folder, import one, re-read —
  // and they are together behind one control, so the column belongs to the
  // tree.
  it('is one press from the collections menu, and re-lists', async () => {
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const { bar } = await mountApp({ listCollections: list })
    await openWorkbench(bar)

    fireEvent.click(button('More collection actions'))
    await vi.waitFor(() => menuItem('Re-read the open folders'))
    fireEvent.click(menuItem('Re-read the open folders'))
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2))
  })

  it('there is exactly one of it — no copy is left in the column', async () => {
    const { bar } = await mountApp()
    await openWorkbench(bar)
    fireEvent.click(button('More collection actions'))
    await vi.waitFor(() => menuItem('Re-read the open folders'))
    const inPanel = buttonNames().filter((n) => n === 'Re-read the open folders')
    expect(inPanel).toHaveLength(0)
    expect(
      [...document.querySelectorAll('.ui-context-menu__item')].filter(
        (b) => (b.textContent ?? '').trim() === 'Re-read the open folders',
      ),
    ).toHaveLength(1)
  })
})

// ── The entry ─────────────────────────────────────────────────────────────

describe('the API workbench is reachable', () => {
  it('the activity bar carries an API entry, enabled from a cold start', async () => {
    const { bar } = await mountApp()
    const entry = apiEntry(bar)
    expect(entry.disabled).toBe(false)
    expect(entry.getAttribute('aria-label')).toBe('API testing')
  })

  it('the entry and the pane call the surface the same thing: API testing', async () => {
    const { bar } = await mountApp()
    // One constant behind all three — the rail's name, the rail's tooltip,
    // and the title the pane hands the strip — so the tab strip label
    // follows the entry rather than being spelled a second time (nocx-zccer).
    expect(apiEntry(bar).getAttribute('aria-label')).toBe('API testing')
    expect(apiEntry(bar).getAttribute('title')).toBe('API testing')
    expect(API_PANE_TITLE).toBe('API testing')
  })

  it('the entry wears the exchange glyph, not the navigation arrow it used to', async () => {
    const { bar } = await mountApp()
    // The descriptor names the kit component…
    expect(apiSidebarAction().icon).toBe(ArrowRightLeftIcon)
    expect(apiSidebarAction().icon).not.toBe(ArrowRightIcon)
    // …and the glyph that actually rendered on the bar is that one: a
    // request out and a response back, four paths, not the single arrow
    // every other rail uses to mean "go there" (nocx-zccer).
    const drawn = [...apiEntry(bar).querySelectorAll('path')].map((p) => p.getAttribute('d'))
    expect(drawn).toHaveLength(4)
    const navigation = render(() => ArrowRightIcon({}))
    for (const p of navigation.container.querySelectorAll('path')) {
      expect(drawn).not.toContain(p.getAttribute('d'))
    }
    navigation.unmount()
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
        defaultRoot: DEFAULT_ROOT,
      }),
    })
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('the folder was replaced'))
  })

  it('a file the format does not recognise is visible, with what was wrong', async () => {
    const bad = collectionsFixture()
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({
        defaultRoot: DEFAULT_ROOT,
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

    // No environment: the default fixture's collection declares none, so the
    // send names none — the request as written, on the direct route.
    await vi.waitFor(() =>
      expect(send).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, '', expect.any(String)),
    )
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
  })

  // ── the run exists from the moment Send is pressed (nocx-pgp9c.4) ───────
  //
  // Every test in this block goes through the seam a person reaches — the
  // button, the row — and never the store. And none of them waits on a
  // duration: the send is a promise this test resolves, so "in flight" is a
  // state the test holds open rather than a moment it hopes to catch.

  it('Send puts a row on screen before any answer exists, and the SAME row carries the answer', async () => {
    let answer: (result: unknown) => void = () => {}
    const send = vi.fn().mockReturnValue(
      new Promise((resolve) => {
        answer = resolve
      }),
    )
    const { bar } = await mountApp({ sendRequest: send })
    await openRequest(bar)
    fireEvent.click(button('Send'))

    // Before anything has answered. This is what did not exist at all
    // before: the only signal a request was in flight was a disabled button.
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    const pending = runCards()[0]
    expect(pending.dataset.outcome).toBe('pending')
    const runId = pending.dataset.runId
    // …and it says what is being sent, so the row is worth looking at.
    expect(pending.textContent).toContain('POST')
    expect(pending.textContent).toContain('{{baseUrl}}/users')

    answer(sendFixture())

    await vi.waitFor(() => expect(runCards()[0].dataset.outcome).toBe('answered'))
    // THE SAME ROW. A second one would mean the pending row was a placeholder
    // rather than the run.
    expect(runCards()).toHaveLength(1)
    expect(runCards()[0].dataset.runId).toBe(runId)
    expect(runCards()[0].textContent).toContain('201')
  })

  it('Send becomes Stop while the run is in flight, is enabled, and reaches the cancel method', async () => {
    let answer: (result: unknown) => void = () => {}
    const send = vi.fn().mockReturnValue(
      new Promise((resolve) => {
        answer = resolve
      }),
    )
    const cancelRequest = vi.fn().mockResolvedValue({})
    const { bar } = await mountApp({ sendRequest: send, cancelRequest })
    await openRequest(bar)
    fireEvent.click(button('Send'))

    await vi.waitFor(() => expect(buttonNames()).toContain('Stop'))
    const stop = button('Stop')
    // ENABLED, which is the whole point: a disabled button was the old
    // signal, and the moment something is happening is the moment a person
    // most needs a control.
    expect(stop.disabled).toBe(false)
    expect(buttonNames()).not.toContain('Send')

    fireEvent.click(stop)
    await vi.waitFor(() => expect(cancelRequest).toHaveBeenCalledTimes(1))

    answer(stoppedSendFixture())
    await vi.waitFor(() => expect(runCards()[0].dataset.outcome).toBe('stopped'))
    // And the line goes back to offering a send.
    expect(buttonNames()).toContain('Send')
  })

  it('a failed run shows what was sent and how far it got — the card is not the whole story', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockResolvedValue(
        failedSendFixture({
          phase: 'resolve',
          reason: 'apisend: resolving "api.internal": no such host',
        }),
      ),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.dataset.outcome).toBe('failed')
    // WHERE it stopped, in the product's words rather than the wire's token.
    expect(card.textContent).toContain('the name did not resolve')
    expect(card.textContent).toContain('no such host')

    // AND WHAT WENT OUT. The raw view is reachable on a run that never got
    // an answer, which is exactly the run that most needs it.
    fireEvent.click(optionIn(card, 'Raw'))
    await vi.waitFor(() =>
      expect(rawBlockText(runCards()[0], 'Raw request')).toContain('POST /users HTTP/1.1'),
    )
    // There is no response side, and none is drawn: a heading over an empty
    // block would say a server replied with nothing.
    expect(runCards()[0].querySelector('[aria-label="Raw response"]')).toBeNull()
    // Body and Headers are not offered either — there is no body to show.
    expect(() => optionIn(runCards()[0], 'Body')).toThrow()
  })

  it('a variable nothing bound is a run naming the variable, not a complaint about a URL', async () => {
    // nocx-pgp9c.6, at the surface: the backend answers the unresolved
    // reason at phase `compose`, and what a person reads is that sentence
    // beside the request they wrote — never `"{{baseUrl}}/zen" is not an
    // absolute URL`, which named neither the variable nor where to bind it.
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockResolvedValue(
        failedSendFixture({
          phase: 'compose',
          reason: 'apicoll: the request uses a variable with no value: baseUrl (in the URL)',
        }),
      ),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.dataset.outcome).toBe('failed')
    expect(card.textContent).toContain('the request could not be built')
    expect(card.textContent).toContain('baseUrl')
    expect(card.textContent).toContain('in the URL')
    expect(card.textContent).not.toContain('is not an absolute URL')
  })

  it('a stopped run is neither worded nor toned as a failure', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockResolvedValue(stoppedSendFixture()),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.dataset.outcome).toBe('stopped')
    const status = card.querySelector<HTMLElement>('.ui-status-card')
    expect(status).not.toBeNull()
    // The kit's own account of its tone, rather than a colour read off a
    // stylesheet: `danger` is what the product uses for "this went wrong".
    expect(status?.dataset.tone).not.toBe('danger')
    expect(card.textContent).toContain('Stopped')
    // A person's own Stop is not described to them as an error.
    expect(card.textContent).not.toContain('did not go out')
  })

  it('cancelling the unlock leaves the run refused and says why (nocx-pgp9c.7)', async () => {
    // A send through a connection whose credential the vault holds now comes
    // back as the canonical SEALED ERROR rather than as a dead run, so the
    // dispatcher raises one unlock dialog and keeps this promise pending
    // (frontend/src/dispatcher.ts). Cancelling that dialog rejects it with
    // VaultOperationCancelledError — the class vault.tsx exports for exactly
    // this — and what must not happen then is a row that sits pending for
    // ever, or vanishes, or claims an exchange nobody made.
    //
    // THIS COVERS THE STORE'S HALF. The dialog, the coalescing and the
    // rejection are the vault seam's, tested where they live; what is
    // asserted here is that a person who says "not now" is left with a row
    // that says so.
    class VaultOperationCancelledError extends Error {
      constructor() {
        super('Vault operation cancelled')
        this.name = 'VaultOperationCancelledError'
      }
    }
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockRejectedValue(new VaultOperationCancelledError()),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.dataset.outcome).toBe('refused')
    expect(card.textContent).toContain('Vault operation cancelled')
    // Not pending — a row still spinning after the person declined is the
    // silent nothing this criterion exists against.
    expect(card.dataset.outcome).not.toBe('pending')
    // And the line offers a send again, so "not now" is not "not ever".
    expect(buttonNames()).toContain('Send')
  })

  it('an ask the backend REFUSED says so, and claims no attempt', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockRejectedValue(new Error('unknown collection handle')),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.dataset.outcome).toBe('refused')
    expect(card.textContent).toContain('The request did not go out')
    expect(card.textContent).toContain('unknown collection handle')
    // No raw view: nothing was composed, so there is nothing to show and the
    // card genuinely IS the whole story for this one.
    expect(card.querySelector('[aria-label="Raw request"]')).toBeNull()
  })

  // ── The badge means something (nocx-6hg2w.19) ───────────────────────────
  //
  // It was drawn from the environment's SETTING, so it appeared on every run
  // under an environment with verification off — the owner's screenshot has
  // it on `https://httpbin.org`, a public host with an ordinary Amazon
  // chain, wearing the words and the colour a self-signed development host
  // would get. What it means now is that THIS run accepted something that
  // would otherwise have been refused, which the backend answers.

  const withTrust = (trust: { state: string; reason: string }) =>
    vi.fn().mockResolvedValue({
      ...sendFixture(),
      route: { kind: 'direct' as const, profileId: '', insecureTls: true },
      response: { ...sendFixture().response!, trust },
    })

  it('warns only when the run accepted a chain that would have been refused, and says why', async () => {
    const { bar } = await mountApp({
      sendRequest: withTrust({
        state: 'unchecked-untrusted',
        reason: 'x509: certificate signed by unknown authority',
      }),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.textContent).toContain('unverified TLS')
    // The REASON, because a warning that only repeats the setting is the one
    // this replaces: a person has to know it is an unknown authority rather
    // than an expiry or a name they can fix.
    expect(card.textContent).toContain('certificate signed by unknown authority')
    const warning = [...card.querySelectorAll<HTMLElement>('.ui-badge')].find(
      (b) => b.dataset.tone === 'warning',
    )
    expect(warning, 'the badge is drawn in the kit’s warning tone').toBeTruthy()
  })

  it('does NOT warn when the switch was on and the chain would have passed anyway', async () => {
    // The same environment, the same switch — this is the run the old badge
    // was wrong about. The fact still appears, in the connection block,
    // because a switch somebody left on is worth seeing; it is simply not an
    // alarm.
    const { bar } = await mountApp({
      sendRequest: withTrust({ state: 'unchecked-trusted', reason: '' }),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.textContent).not.toContain('unverified TLS')
    expect(
      [...card.querySelectorAll<HTMLElement>('.ui-badge')].some(
        (b) => b.dataset.tone === 'warning',
      ),
    ).toBe(false)

    fireEvent.click(optionIn(card, 'Raw'))
    await vi.waitFor(() =>
      expect(rawBlockText(runCards()[0], 'Connection')).toContain('not checked'),
    )
  })

  it('does NOT warn on an ordinary verified run, whatever the environment says', async () => {
    // route.insecureTls is TRUE on this run and the chain verified: the
    // setting and the answer disagree, and the answer is what the surface
    // reads. Without this the badge could still be drawn from the setting
    // and every other case here would pass.
    const { bar } = await mountApp({
      sendRequest: withTrust({ state: 'verified', reason: '' }),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))

    const card = runCards()[0]
    expect(card.textContent).not.toContain('unverified TLS')
    fireEvent.click(optionIn(card, 'Raw'))
    await vi.waitFor(() => expect(rawBlockText(runCards()[0], 'Connection')).toBeTruthy())
    expect(rawBlockText(runCards()[0], 'Connection')).not.toContain('not checked')
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

// ── Making one ────────────────────────────────────────────────────────────

/** The tree row for one collection, by the handle that addresses it. */
function collectionRow(handle: string): HTMLElement {
  const el = workbench().querySelector<HTMLElement>(`[data-row-key="${handle}:"] .ui-tree-row`)
  if (!el) throw new Error(`no collection row for ${handle}`)
  return el
}

/** The New collection dialog's own element. */
function newCollectionDialog(): HTMLDialogElement {
  return dialogFor('api-new-collection-name')
}

/** Reach the "open a folder" ask, which lives behind the collections menu.
 *  It was a button in the sidebar; the `+` beside COLLECTIONS makes a
 *  collection now — the act people do most — and the rarer doors moved into
 *  the menu beside it. */
async function openFolderAsk(): Promise<void> {
  await vi.waitFor(() => button('More collection actions'))
  fireEvent.click(button('More collection actions'))
  await vi.waitFor(() => menuItem('Open folder…'))
  fireEvent.click(menuItem('Open folder…'))
}

/** A row of the kit's context menu. It is searched in the DOCUMENT and not
 *  in the workbench: the menu is a Portal onto document.body, which is what
 *  keeps a popover out of the scroll box it was opened from. */
function menuItem(label: string): HTMLButtonElement {
  const found = [...document.querySelectorAll<HTMLButtonElement>('.ui-context-menu__item')].find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`no menu item named ${label}`)
  return found
}

/** The Open folder dialog's own element. */
function openFolderDialog(): HTMLDialogElement {
  return dialogFor('api-collection-path')
}

/** Open the workbench with nothing open and ask to open a folder, typing a
 *  path into the dialog — the whole gesture a person makes. */
async function typeFolderPath(bar: HTMLElement, path: string): Promise<void> {
  await openWorkbench(bar)
  await openFolderAsk()
  await vi.waitFor(() => expect(openFolderDialog().open).toBe(true))
  fireEvent.input(field('api-collection-path'), { target: { value: path } })
}

/** A backend whose native directory picker answers with `path`. */
function withPicker(path: string): Partial<ApiWorkbenchServices> {
  return { ...noCollections(), openDirectory: vi.fn().mockResolvedValue({ path }) }
}

/** Open the workbench with nothing open, ask for a new collection, and type
 *  a name into the dialog — the whole gesture a person makes. */
async function typeNewCollectionName(bar: HTMLElement, name: string): Promise<void> {
  await openWorkbench(bar)
  await vi.waitFor(() => button('New collection'))
  fireEvent.click(button('New collection'))
  await vi.waitFor(() => field('api-new-collection-name'))
  fireEvent.input(field('api-new-collection-name'), { target: { value: name } })
}

describe('a person can make a collection', () => {
  it('the action is on screen and enabled from a cold start with nothing open', async () => {
    const { bar } = await mountApp(noCollections())
    await openWorkbench(bar)

    await vi.waitFor(() => expect(workbench().textContent).toContain('No collections open'))
    // The state a person actually starts in is exactly the one where they
    // need this, so the action may not depend on a collection being open,
    // on a path having been typed, or on anything else being filled in.
    expect(button('New collection').disabled).toBe(false)
  })

  it('typing a name and confirming reaches api.collections.create', async () => {
    const create = vi.fn().mockResolvedValue(createdFixture())
    const { bar } = await mountApp({ ...noCollections(), createCollection: create })
    await typeNewCollectionName(bar, CREATED_NAME)

    fireEvent.click(button('Create'))

    await vi.waitFor(() => expect(create).toHaveBeenCalledWith(CREATED_NAME))
  })

  it('the new collection is on screen and selected, and open is never called again', async () => {
    const open = vi.fn()
    const { bar } = await mountApp({ ...noCollections(), openCollection: open })
    await typeNewCollectionName(bar, CREATED_NAME)

    fireEvent.click(button('Create'))

    await vi.waitFor(() => expect(workbench().textContent).toContain(CREATED_NAME))
    await vi.waitFor(() =>
      expect(collectionRow(CREATED_HANDLE).getAttribute('data-selected')).toBe('true'),
    )
    // create answers the same handle-and-collection an open does, so there is
    // nothing left to ask for.
    expect(open).not.toHaveBeenCalled()
  })

  it('the dialog goes away once the collection exists', async () => {
    const { bar } = await mountApp(noCollections())
    await typeNewCollectionName(bar, CREATED_NAME)
    expect(newCollectionDialog().open).toBe(true)

    fireEvent.click(button('Create'))

    // A `<dialog>` stays in the document when it closes; `open` is what the
    // user sees the difference in.
    await vi.waitFor(() => expect(newCollectionDialog().open).toBe(false))
  })

  it('a name the backend refuses shows the reason it gave, on screen', async () => {
    const { bar } = await mountApp({
      ...noCollections(),
      createCollection: vi
        .fn()
        .mockRejectedValue(new Error('a folder called orders-api is already there')),
    })
    await typeNewCollectionName(bar, CREATED_NAME)

    fireEvent.click(button('Create'))

    await vi.waitFor(() =>
      expect(workbench().textContent).toContain('a folder called orders-api is already there'),
    )
    // And the dialog stays, with what was typed still in it: the name is
    // what has to change, and closing the form would make the person type it
    // again to find out.
    expect(field('api-new-collection-name').value).toBe(CREATED_NAME)
  })

  it('asking a second time starts with an empty field, not the last name', async () => {
    const { bar } = await mountApp(noCollections())
    await typeNewCollectionName(bar, CREATED_NAME)
    fireEvent.click(button('Create'))
    await vi.waitFor(() => expect(newCollectionDialog().open).toBe(false))

    fireEvent.click(button('New collection'))

    await vi.waitFor(() => expect(newCollectionDialog().open).toBe(true))
    expect(field('api-new-collection-name').value).toBe('')
  })

  it('a blank name is never sent — the dialog refuses what the backend would', async () => {
    const create = vi.fn().mockResolvedValue(createdFixture())
    const { bar } = await mountApp({ ...noCollections(), createCollection: create })
    await typeNewCollectionName(bar, '   ')

    expect(button('Create').disabled).toBe(true)
    fireEvent.click(button('Create'))
    expect(create).not.toHaveBeenCalled()
  })
})

// ── Opening one, which is the second door ─────────────────────────────────
//
// The panel used to WEAR this: a "Collection folder" text field and an "Open
// folder" button stacked under "New collection", so the first thing a person
// with no collections saw was a bare form. It is an action that asks now
// (nocx-84shs) — the same shape the name ask has, for the reason
// name-colour-dialog.tsx gives about the workspace create and edit forms:
// a person who has met one of these has already learnt the other.

describe('a person can open a folder somebody else made', () => {
  it('the panel wears no form — there are two actions, and each asks', async () => {
    const { bar } = await mountApp(noCollections())
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('No collections open'))

    // Nothing to fill in before a person can act: the path field is inside
    // the ask, and until the ask is on screen it is not reachable.
    expect(workbench().querySelector('#api-collection-path')).not.toBeNull()
    expect(reachable(workbench().querySelector('#api-collection-path')!)).toBe(false)
    // Making one is the `+` beside COLLECTIONS; opening somebody else's is
    // in the menu next to it, with the other occasional acts.
    expect(button('New collection').disabled).toBe(false)
    fireEvent.click(button('More collection actions'))
    await vi.waitFor(() => menuItem('Open folder…'))
  })

  it('typing a folder and confirming reaches api.collections.open', async () => {
    const open = vi
      .fn()
      .mockResolvedValue({ handle: HANDLE, collection: collectionsFixture().collection })
    const { bar } = await mountApp({ ...noCollections(), openCollection: open })
    await typeFolderPath(bar, '/work/acme-api')

    fireEvent.click(button('Open'))

    await vi.waitFor(() => expect(open).toHaveBeenCalledWith('/work/acme-api'))
    // And the ask goes away once the folder is open.
    await vi.waitFor(() => expect(openFolderDialog().open).toBe(false))
    await vi.waitFor(() => expect(workbench().textContent).toContain('acme-api'))
  })

  it('a path the backend refuses keeps the ask open, with the reason on screen', async () => {
    const { bar } = await mountApp({
      ...noCollections(),
      openCollection: vi.fn().mockRejectedValue(new Error('/work/nope is not a collection')),
    })
    await typeFolderPath(bar, '/work/nope')

    fireEvent.click(button('Open'))

    await vi.waitFor(() =>
      expect(openFolderDialog().textContent).toContain('/work/nope is not a collection'),
    )
    expect(openFolderDialog().open).toBe(true)
    // What was typed survives the refusal — the path is what has to change.
    expect(field('api-collection-path').value).toBe('/work/nope')
  })

  it('an empty path is never sent — the ask refuses what the backend would', async () => {
    const open = vi.fn()
    const { bar } = await mountApp({ ...noCollections(), openCollection: open })
    await typeFolderPath(bar, '   ')

    expect(button('Open').disabled).toBe(true)
    fireEvent.click(button('Open'))
    expect(open).not.toHaveBeenCalled()
  })

  it('asking a second time starts empty, not with the last path', async () => {
    const { bar } = await mountApp(noCollections())
    await typeFolderPath(bar, '/work/acme-api')
    fireEvent.click(button('Open'))
    await vi.waitFor(() => expect(openFolderDialog().open).toBe(false))

    await openFolderAsk()

    await vi.waitFor(() => expect(openFolderDialog().open).toBe(true))
    expect(field('api-collection-path').value).toBe('')
  })
})

// ── The native directory picker, and its ordinary absence ─────────────────
//
// `dialog.openDirectory` answers -32601 wherever there is no Wails runtime,
// which is EVERY `make dev-web` run — the configuration this feedback came
// from. So the absent case is the ordinary one and it may not look broken:
// no Browse control at all, and typing the path is simply how it is done.

describe('the folder ask offers Browse only when there is a picker', () => {
  it('with no picker there is no Browse control, and typing still works', async () => {
    // servicesFixture carries no openDirectory — the dev-web shape.
    const open = vi
      .fn()
      .mockResolvedValue({ handle: HANDLE, collection: collectionsFixture().collection })
    const { bar } = await mountApp({ ...noCollections(), openCollection: open })
    await typeFolderPath(bar, '/work/acme-api')

    expect(buttonNames()).not.toContain('Browse…')
    // …and the field is not disabled or waiting on anything.
    expect(field('api-collection-path').disabled).toBe(false)
    fireEvent.click(button('Open'))
    await vi.waitFor(() => expect(open).toHaveBeenCalledWith('/work/acme-api'))
  })

  it('with a picker, Browse fills the field with what was chosen', async () => {
    const services = withPicker('/chosen/acme-api')
    const { bar } = await mountApp(services)
    await typeFolderPath(bar, '')

    fireEvent.click(button('Browse…'))

    await vi.waitFor(() => expect(field('api-collection-path').value).toBe('/chosen/acme-api'))
    expect(services.openDirectory).toHaveBeenCalled()
  })

  it('cancelling the picker leaves what was typed untouched', async () => {
    // A cancelled picker answers an EMPTY path and no error — the contract
    // dialog.openFile already keeps. Writing it into the field would erase
    // what the person had typed as the price of changing their mind.
    const { bar } = await mountApp(withPicker(''))
    await typeFolderPath(bar, '/work/half-typed')

    fireEvent.click(button('Browse…'))

    await vi.waitFor(() => expect(field('api-collection-path').value).toBe('/work/half-typed'))
    expect(button('Open').disabled).toBe(false)
  })

  it('a picker that reports itself unavailable retires the control and says why', async () => {
    // The interval, both ends: the control is on screen from the moment the
    // ask opens with a picker wired until the picker answers -32601, and
    // never returns for the life of the surface — a second click on a
    // control that has already refused once is the broken-looking fallback
    // this exists to avoid.
    const { bar } = await mountApp({
      ...noCollections(),
      openDirectory: vi.fn().mockRejectedValue(new RpcError('method not found', -32601)),
    })
    await typeFolderPath(bar, '/work/half-typed')
    expect(buttonNames()).toContain('Browse…')

    fireEvent.click(button('Browse…'))

    await vi.waitFor(() => expect(buttonNames()).not.toContain('Browse…'))
    expect(openFolderDialog().textContent).toContain('method not found')
    // The refusal costs the person nothing they typed.
    expect(field('api-collection-path').value).toBe('/work/half-typed')
    expect(openFolderDialog().open).toBe(true)
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
      // The DAMAGE is on the request side, which is where a placement can be
      // damaged at all — the response side is a search over what actually
      // crossed (§11.3) — and the request side now rides the exchange.
      sendRequest: vi.fn().mockResolvedValue({
        ...sendFixture(),
        request: requestRawFixture('truncated, 24 of 214 bytes'),
      }),
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

    const chipReading = (rawChips(runCards()[0])[0].textContent ?? '').trim()
    // What the reader should end up with: the wire's own text, with the
    // elided placeholder replaced by what the chip says in its place.
    const expected = requestRawFixture().text.replace(SECRET_PLACEHOLDER, chipReading)

    expect(rawBlockText(runCards()[0], 'Raw request')).toBe(expected)
    expect(rawBlockText(runCards()[0], 'Raw response')).toBe(responseRawFixture().text)
  })

  it('a side with nothing to mark still shows all of its text', async () => {
    const { bar } = await mountApp({
      sendRequest: vi.fn().mockResolvedValue({
        ...sendFixture({ raw: { text: 'HTTP/1.1 204 No Content\r\n\r\n', spans: [] } }),
        request: { text: 'GET /health HTTP/1.1\r\n\r\n', spans: [] },
      }),
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

    // Asserted on which tab is CURRENT, not on the text in the card: every
    // panel is rendered and the inactive ones are `hidden`, which keeps a
    // section's own state while it is not on screen (ui/tabs.tsx). textContent
    // would read them all and say nothing about what a person is looking at.
    await vi.waitFor(() => expect(currentTab(runCards()[1])).toBe('Raw'))
    expect(currentTab(runCards()[0])).toBe('Body')
  })
})

// ── The Import section has an entrance ────────────────────────────────────
//
// nocx-6siis. The section was written `collapsible open={false} onToggle={()
// => undefined}` — a literal and a no-op — so the disclosure reported
// "collapsed" for ever and the body behind it was never in the document at
// all. Measured in the container: `aria-expanded` stayed "false" across 14
// polls after a real click, in two runs.
//
// WHY NOTHING CAUGHT IT. All four `importPostman` call sites in
// `frontend/src` drove the CLIENT or the STORE; not one drove the entrance.
// That is AGENTS.md's first testing rule in its exact shape — every unit
// correct, the user's task impossible — so these two go through the door.

// ── Importing a collection ───────────────────────────────────────────────
//
// It was a collapsible SECTION in the sidebar holding a curl box, two path
// fields and two buttons — four controls and a disclosure, permanently
// occupying the column whose job is the tree. It is an ask off the
// collections menu now, and the curl half is on the request line, where a
// person pasting a command is already looking.

/** Open the workbench and the import ask — the entrance a person uses, off
 *  the collections menu, rather than a signal set from the outside. */
async function openImportAsk(bar: HTMLElement): Promise<void> {
  await openWorkbench(bar)
  fireEvent.click(button('More collection actions'))
  await vi.waitFor(() => menuItem('Import collection…'))
  fireEvent.click(menuItem('Import collection…'))
  await vi.waitFor(() => expect(reachable(field('api-import-postman-file'))).toBe(true))
}

describe('a Postman export is imported through an ask', () => {
  it('the panel wears no import form — the fields live inside the ask', async () => {
    const { bar } = await mountApp()
    await openWorkbench(bar)

    // Present in the document (the dialog is mounted for the life of the
    // surface) and NOT reachable, which is the difference this suite's
    // `reachable` exists for.
    const file = workbench().querySelector('#api-import-postman-file')
    expect(file).not.toBeNull()
    expect(reachable(file!)).toBe(false)
  })

  // ── The export's own picker, and the destination it proposes ────────────
  //
  // The ask named the export by PATH and offered no way to choose one: a
  // person opened a terminal, found the file they had just downloaded from
  // Postman, copied its path and pasted it back. And it asked for a
  // destination as an absolute path with nothing in it, while
  // `api.collections.create` next door takes a name and puts the folder
  // where nocx keeps collections — the same concept behind two doors of very
  // different difficulty (nocx-6hg2w.15, nocx-6hg2w.14).

  it('with no file picker there is no control on the export field, and typing still works', async () => {
    // servicesFixture carries no openFile — the dev-web shape, and the
    // ordinary one: `dialog.openFile` answers -32601 wherever there is no
    // Wails runtime, so the absent case may not look broken.
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({ importPostman })
    await openImportAsk(bar)

    expect(buttonNames()).not.toContain('Choose export…')
    expect(field('api-import-postman-file').disabled).toBe(false)
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/w/acme-api' } })
    fireEvent.click(button('Import'))
    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith({ path: '/w/acme.json' }, '/w/acme-api'),
    )
  })

  it('with a picker, choosing an export fills the field and PROPOSES where the collection lands', async () => {
    const openFile = vi.fn().mockResolvedValue({ path: '/work/acme.postman_collection.json' })
    const { bar } = await mountApp({ openFile })
    await openImportAsk(bar)

    // The control is reachable from the state the ask opens in — which is
    // the half a test that called the picker directly could not say.
    expect(buttonNames()).toContain('Choose export…')
    fireEvent.click(button('Choose export…'))

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe('/work/acme.postman_collection.json'),
    )
    expect(openFile).toHaveBeenCalled()
    // BOTH suffixes go: a folder called `acme.postman_collection` would be
    // named after our import machinery rather than after the collection.
    await vi.waitFor(() =>
      expect(field('api-import-postman-dest').value).toBe(`${DEFAULT_ROOT}/acme`),
    )
  })

  it('a destination the person has typed is never overwritten by a later pick', async () => {
    const openFile = vi.fn().mockResolvedValue({ path: '/work/acme.postman_collection.json' })
    const { bar } = await mountApp({ openFile })
    await openImportAsk(bar)

    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/elsewhere/mine' } })
    fireEvent.click(button('Choose export…'))

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe('/work/acme.postman_collection.json'),
    )
    // The person said where it goes. A proposal that argued with them is the
    // surface deciding something they had already decided.
    expect(field('api-import-postman-dest').value).toBe('/elsewhere/mine')
  })

  it("typing an export proposes too — the offer is not the picker's alone", async () => {
    const { bar } = await mountApp()
    await openImportAsk(bar)

    fireEvent.input(field('api-import-postman-file'), {
      target: { value: '/work/orders.postman_collection.json' },
    })

    await vi.waitFor(() =>
      expect(field('api-import-postman-dest').value).toBe(`${DEFAULT_ROOT}/orders`),
    )
  })

  it('with no default location the ask proposes nothing and stays typeable', async () => {
    // '' is what a build with no app directory answers — the state
    // apicoll.ErrNoDefaultLocation names. Nothing was promised, so nothing
    // degrades: the person types a path, exactly as before.
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({ collections: [], defaultRoot: '' }),
    })
    await openImportAsk(bar)

    fireEvent.input(field('api-import-postman-file'), {
      target: { value: '/work/orders.postman_collection.json' },
    })

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe('/work/orders.postman_collection.json'),
    )
    expect(field('api-import-postman-dest').value).toBe('')
    expect(field('api-import-postman-dest').disabled).toBe(false)
  })

  it('a file picker that reports itself unavailable retires its control and says why', async () => {
    // The same interval the folder ask's picker has, and its own signal: the
    // two dialog methods retire independently, so this must not depend on
    // the directory picker at all.
    const { bar } = await mountApp({
      openFile: vi.fn().mockRejectedValue(new RpcError('method not found', -32601)),
    })
    await openImportAsk(bar)
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/half-typed.json' } })
    expect(buttonNames()).toContain('Choose export…')

    fireEvent.click(button('Choose export…'))

    await vi.waitFor(() => expect(buttonNames()).not.toContain('Choose export…'))
    // The refusal costs the person nothing they typed, and is said where
    // every other refusal in this ask is said.
    expect(field('api-import-postman-file').value).toBe('/w/half-typed.json')
    expect(workbench().textContent).toContain('method not found')
  })

  it('cancelling the file picker leaves what was typed untouched', async () => {
    const { bar } = await mountApp({ openFile: vi.fn().mockResolvedValue({ path: '' }) })
    await openImportAsk(bar)
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/half-typed.json' } })

    fireEvent.click(button('Choose export…'))

    await vi.waitFor(() => expect(button('Import')).toBeTruthy())
    expect(field('api-import-postman-file').value).toBe('/w/half-typed.json')
  })

  it('entrance, fields, confirm — and the backend is reached', async () => {
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({ importPostman })
    await openWorkbench(bar)

    fireEvent.click(button('More collection actions'))
    await vi.waitFor(() => menuItem('Import collection…'))
    fireEvent.click(menuItem('Import collection…'))

    await vi.waitFor(() => expect(reachable(field('api-import-postman-file'))).toBe(true))
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/w/acme-api' } })
    fireEvent.click(button('Import'))

    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith({ path: '/w/acme.json' }, '/w/acme-api'),
    )
  })
})

// ── The ask opens on our folder ───────────────────────────────────────────
//
// nocx-9ivof. `nocx-6hg2w.14` put `defaultRoot` on the wire and proposed a
// destination AFTER a source was chosen; before that both fields were empty
// and the destination's placeholder read `/work/acme-api` — an arbitrary
// path rather than the place this product keeps collections, which is where
// `Create` next door puts one without asking. Proposing on open also mints a
// value that could only ever be refused (the root itself certainly exists),
// so `ready()` has to know its own proposal.

/** The ask, opened on a build whose collections live at `root`. */
async function openAskWithRoot(root: string): Promise<void> {
  const { bar } = await mountApp({
    listCollections: vi.fn().mockResolvedValue({ collections: [], defaultRoot: root }),
  })
  await openImportAsk(bar)
}

describe('the import ask proposes our folder', () => {
  it('opens with the default root already in the destination, and Import disabled', async () => {
    await openAskWithRoot('/data/collections')

    expect(field('api-import-postman-dest').value).toBe('/data/collections/')
    // The root names a folder that certainly exists, so submitting it could
    // only come back "a folder is already there" about the collections root
    // rather than about anything the person chose.
    expect(button('Import').disabled).toBe(true)

    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    fireEvent.input(field('api-import-postman-dest'), {
      target: { value: '/data/collections/acme' },
    })
    expect(button('Import').disabled).toBe(false)
  })

  it('refuses the root without its separator too', async () => {
    // Both values `askForImport` can leave behind: a person who deletes the
    // trailing slash has still said nothing.
    await openAskWithRoot('/data/collections')

    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/data/collections' } })
    expect(button('Import').disabled).toBe(true)
  })

  it('proposes nothing on a build with no default location', async () => {
    // '' is the degraded state — apicoll.ErrNoDefaultLocation. Nothing was
    // promised, so the field says nothing at all rather than an example path
    // from somebody else's disk.
    await openAskWithRoot('')

    const dest = field('api-import-postman-dest')
    expect(dest.value).toBe('')
    expect(dest.getAttribute('placeholder')).toBe('')
  })

  it('does not overwrite a destination the person edited', async () => {
    // The nocx-6hg2w.14 rule, re-asserted because the prefill moves the code
    // that honours it: the proposal writes the signal directly and must not
    // count as the person having spoken.
    const openFile = vi.fn().mockResolvedValue({ path: '/downloads/acme.postman_collection.json' })
    const { bar } = await mountApp({
      openFile,
      listCollections: vi
        .fn()
        .mockResolvedValue({ collections: [], defaultRoot: '/data/collections' }),
    })
    await openImportAsk(bar)

    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/work/mine' } })
    fireEvent.click(button('Choose export…'))

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe(
        '/downloads/acme.postman_collection.json',
      ),
    )
    expect(field('api-import-postman-dest').value).toBe('/work/mine')
  })

  it('still completes the proposal when nobody has touched the destination', async () => {
    // The other side of the same rule: the field opening non-empty must not
    // look like a person having typed, or the pick would never land.
    const openFile = vi.fn().mockResolvedValue({ path: '/downloads/acme.postman_collection.json' })
    const { bar } = await mountApp({
      openFile,
      listCollections: vi
        .fn()
        .mockResolvedValue({ collections: [], defaultRoot: '/data/collections' }),
    })
    await openImportAsk(bar)

    fireEvent.click(button('Choose export…'))

    await vi.waitFor(() =>
      expect(field('api-import-postman-dest').value).toBe('/data/collections/acme'),
    )
  })
})

// ── The ask accepts a drop ────────────────────────────────────────────────
//
// nocx-txoxr. The window drop already reaches the renderer as
// `files.dropped`, and for a LOCAL tab it carries the file's absolute path —
// which is exactly what `api.import.postman` consumes. What was missing is
// the surface saying it accepts one: without a drop target on the ask, a
// person who had just downloaded an export had to find its path and type it,
// beside a dialog that was already asking for a path.
//
// The drop is filtered by TARGET as well as by session, because the local
// tab's terminal pane is a drop surface of the same session and the session
// alone cannot tell the two apart.

/** The ask, opened on a build whose window drop is `drop` — `undefined` is a
 *  build with no Wails runtime at all, which is every `make dev-web` run and
 *  the whole e2e harness. */
async function openAskWithDrop(drop?: NativeDropFixture): Promise<void> {
  const { bar } = await mountApp({
    listCollections: vi
      .fn()
      .mockResolvedValue({ collections: [], defaultRoot: '/data/collections' }),
    nativeDrop: drop,
  })
  await openImportAsk(bar)
}

/** The ask's BODY — the element the drop zone is inside. Not the `<dialog>`:
 *  the kit does not forward arbitrary `data-*` to it, and reaching past the
 *  component to paint attributes on it would be the repaint rule in another
 *  form. */
function importAskBody(): HTMLElement {
  const el = dialogFor('api-import-postman-file').querySelector<HTMLElement>('.nocx-dialog__body')
  if (!el) throw new Error('the import ask has no body')
  return el
}

/** What the destination field's validation slot says right now, or ''. */
function destError(): string {
  return importAskBody().querySelector('#api-import-postman-dest__error')?.textContent ?? ''
}

/** One dropped file, described the way a LOCAL tab's drop is: nothing minted,
 *  and the absolute path in `localPath`. */
function droppedSource(name: string, localPath: string) {
  return { sourceTicket: '', name, size: 12, localPath }
}

describe('the import ask accepts a drop', () => {
  it('advertises itself as a drop target while it is open', async () => {
    await openAskWithDrop(nativeDropFixture())

    const zone = importAskBody().querySelector<HTMLElement>('[data-file-drop-target]')
    expect(zone?.getAttribute('data-file-drop-target')).toBe('api-import')
    expect(zone?.getAttribute('data-session-id')).toBe(DROP_SESSION)
  })

  it('fills both fields from one gesture', async () => {
    const drop = nativeDropFixture()
    await openAskWithDrop(drop)

    drop.emit({
      sessionId: DROP_SESSION,
      target: 'api-import',
      sources: [
        droppedSource('acme.postman_collection.json', '/downloads/acme.postman_collection.json'),
      ],
    })

    expect(field('api-import-postman-file').value).toBe('/downloads/acme.postman_collection.json')
    expect(field('api-import-postman-dest').value).toBe('/data/collections/acme')
  })

  it('ignores a drop meant for the terminal', async () => {
    // The same session owns both surfaces, so this is the case the target
    // field exists for: without it one gesture reached both and the winner
    // was evaluation order.
    const drop = nativeDropFixture()
    await openAskWithDrop(drop)

    drop.emit({
      sessionId: DROP_SESSION,
      target: 'terminal',
      sources: [droppedSource('a.json', '/downloads/a.json')],
    })

    expect(field('api-import-postman-file').value).toBe('')
    expect(field('api-import-postman-dest').value).toBe('/data/collections/')
  })

  it('refuses several files with a sentence, and changes nothing', async () => {
    // One import makes one collection; N collections is N destinations,
    // which is a different question and not one this ask can answer by
    // guessing which of them was meant.
    const drop = nativeDropFixture()
    await openAskWithDrop(drop)

    drop.emit({
      sessionId: DROP_SESSION,
      target: 'api-import',
      sources: [
        droppedSource('a.json', '/downloads/a.json'),
        droppedSource('b.json', '/downloads/b.json'),
      ],
    })

    expect(field('api-import-postman-file').value).toBe('')
    expect(field('api-import-postman-dest').value).toBe('/data/collections/')
    expect(destError()).toMatch(/one export/i)
  })

  it('ignores a drop that names no path — a remote tab mints a ticket instead', async () => {
    // Nothing here can read a ticket: `api.import.postman` takes a path, and
    // a remote tab's drop carries none.
    const drop = nativeDropFixture()
    await openAskWithDrop(drop)

    drop.emit({
      sessionId: DROP_SESSION,
      target: 'api-import',
      sources: [{ sourceTicket: 'f'.repeat(32), name: 'a.json', size: 12 }],
    })

    expect(field('api-import-postman-file').value).toBe('')
  })

  // The two absences fail independently, so they are two tests. A build with
  // no Wails still has local sessions — `make dev-web` and the e2e harness
  // both do — so a target gated on the session alone would light up under a
  // drag there and then deliver nothing.
  it('names no NATIVE drop target where there is no Wails runtime', async () => {
    // The attributes are what Wails reads off the dropped-on element, so
    // without a runtime they would name a route nothing travels. The REGION
    // is a different question and is drawn anyway — the browser half answers
    // it (see the browser-drop block below).
    await openAskWithDrop(undefined)

    expect(importAskBody().querySelector('[data-file-drop-target]')).toBeNull()
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/downloads/a.json' } })
    expect(field('api-import-postman-file').value).toBe('/downloads/a.json')
  })

  it('draws no drop target when this window has no local session', async () => {
    await openAskWithDrop(nativeDropFixture(null))

    expect(importAskBody().querySelector('[data-file-drop-target]')).toBeNull()
  })

  // ── The affordance says so BEFORE the gesture ───────────────────────────
  //
  // nocx-9hb5g. The zone painted only under a drag, so the finished ask
  // looked exactly as it had before the drop existed — the owner opened it in
  // the Wails window and said the import had not changed at all. It had; the
  // capability just never said so. Postman, this ask's reference, shows the
  // region at rest, permanently.

  it('shows the drop region above both fields, with no drag having happened', async () => {
    await openAskWithDrop(nativeDropFixture())

    const region = importAskBody().querySelector<HTMLElement>('.ui-drop-zone__region')
    expect(region).not.toBeNull()
    expect(region!.textContent).toMatch(/drop/i)
    const isBelowRegion = (el: Element): boolean =>
      (region!.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
    expect(isBelowRegion(field('api-import-postman-file'))).toBe(true)
    expect(isBelowRegion(field('api-import-postman-dest'))).toBe(true)
  })

  it('still draws the region on a build with no native drop — the browser half has one', async () => {
    // This test asserted the opposite until nocx-1gfbw, and it was wrong the
    // day it was written: the owner opened the ask at localhost:5180 and
    // found no drop region at all. A browser drop carries the BYTES, which
    // reach whichever machine the backend is on, so a build with no Wails is
    // not a build with no drop.
    await openAskWithDrop(undefined)

    const zone = importAskBody().querySelector<HTMLElement>('.ui-drop-zone')!
    expect(zone.querySelector('.ui-drop-zone__region')).not.toBeNull()
    expect(zone.hasAttribute('data-file-drop-target')).toBe(false)
  })

  it("the region's picker is the export field's picker — one capability, two controls", async () => {
    // Two controls, ONE derivation of "choose an export": the region's button
    // is threaded the handler the field's trailing button already calls, so a
    // pick made either way proposes the destination the same way.
    const openFile = vi.fn().mockResolvedValue({ path: '/downloads/acme.postman_collection.json' })
    const { bar } = await mountApp({ openFile, nativeDrop: nativeDropFixture() })
    await openImportAsk(bar)

    expect(buttonNames()).toContain('Choose export…')
    fireEvent.click(button('Or select a file'))

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe(
        '/downloads/acme.postman_collection.json',
      ),
    )
    expect(openFile).toHaveBeenCalledTimes(1)
    // The field's own control reaches the very same mock — which is the
    // assertion that the two controls are one capability rather than two.
    fireEvent.click(button('Choose export…'))
    await vi.waitFor(() => expect(openFile).toHaveBeenCalledTimes(2))
  })
})

// ── The ask accepts a BROWSER drop, and imports the DOCUMENT ─────────────
//
// nocx-1gfbw. The drop region was gated on a Wails runtime AND an open local
// terminal session, so `make dev-web` — where every contributor works and
// where a browser user lives — drew nothing at all. Both conditions were
// wrong (spec §1a): a browser drop carries `File` objects with BYTES, and
// bytes reach whichever machine the backend runs on, while a PATH names a
// file on the backend's machine and is right only when that machine is also
// the person's. So the browser is the general case, not the degraded one,
// and the import is not a terminal session's gesture.

/** A DataTransfer jsdom does not have, carrying what a browser drop carries. */
function browserTransfer(files: File[]): DataTransfer {
  return { types: ['Files'], files } as unknown as DataTransfer
}

/** The gesture itself, on the ask's own zone. */
function dropOnAsk(files: File[]): void {
  const zone = importAskBody().querySelector<HTMLElement>('.ui-drop-zone')
  if (!zone) throw new Error('the ask has no drop zone')
  const e = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent
  Object.defineProperty(e, 'dataTransfer', { value: browserTransfer(files) })
  zone.dispatchEvent(e)
}

const EXPORT_TEXT = '{"info":{"name":"Acme"},"item":[]}'

function exportFile(name: string, text = EXPORT_TEXT): File {
  return new File([text], name, { type: 'application/json' })
}

/** The ask on a build with NO Wails at all — no native drop, no system
 *  pickers. It is `make dev-web`, the e2e harness and every browser. */
async function openBrowserAsk(over: Partial<ApiWorkbenchServices> = {}) {
  const mounted = await mountApp({
    listCollections: vi
      .fn()
      .mockResolvedValue({ collections: [], defaultRoot: '/data/collections' }),
    ...over,
  })
  await openImportAsk(mounted.bar)
  return mounted
}

describe('the import ask accepts a browser drop', () => {
  it('draws the region with no Wails runtime and no session whatever', async () => {
    await openBrowserAsk()

    const region = importAskBody().querySelector<HTMLElement>('.ui-drop-zone__region')
    expect(region).not.toBeNull()
    expect(region!.textContent).toMatch(/drop/i)
    // And it names no terminal tab: the import writes to the BACKEND's disk
    // and is not a session gesture, so borrowing a local tab's id here was
    // the bug rather than the addressing (spec §1a).
    expect(importAskBody().querySelector('.ui-drop-zone')!.hasAttribute('data-session-id')).toBe(
      false,
    )
  })

  it('takes a dropped file and imports it as the DOCUMENT, not as a path', async () => {
    // The renderer holds the bytes and the backend may be on another host —
    // `make dev-web` is documented as forwarding both ports over SSH — so
    // the file's name is not a path anybody could open there.
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    await openBrowserAsk({ importPostman })

    dropOnAsk([exportFile('acme.postman_collection.json')])

    // The gesture answers BOTH halves of the ask, exactly as the picker's
    // answer does: what was chosen, and where its collection lands.
    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe('acme.postman_collection.json'),
    )
    expect(field('api-import-postman-dest').value).toBe('/data/collections/acme')

    fireEvent.click(button('Import'))
    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { document: EXPORT_TEXT },
        '/data/collections/acme',
      ),
    )
  })

  it('refuses several dropped files with the sentence the native half uses', async () => {
    // One import makes one collection, and N collections is N destinations.
    // The same sentence because it is the same rule, derived once.
    await openBrowserAsk()

    dropOnAsk([exportFile('a.json'), exportFile('b.json')])

    await vi.waitFor(() => expect(destError()).toMatch(/one export/i))
    expect(field('api-import-postman-file').value).toBe('')
    expect(field('api-import-postman-dest').value).toBe('/data/collections/')
  })

  it('a file chosen with the kit input reaches the handler a dropped file reaches', async () => {
    // ONE derivation of "here is the export". Two would agree everywhere
    // anybody looked and disagree about the proposed destination somewhere
    // nobody did.
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    await openBrowserAsk({ importPostman })

    const input = importAskBody().querySelector<HTMLInputElement>('.ui-file-input__native')
    expect(input).not.toBeNull()
    const file = exportFile('acme.postman_collection.json')
    Object.defineProperty(input!, 'files', { value: [file] })
    fireEvent.change(input!)

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe('acme.postman_collection.json'),
    )
    expect(field('api-import-postman-dest').value).toBe('/data/collections/acme')

    fireEvent.click(button('Import'))
    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { document: EXPORT_TEXT },
        '/data/collections/acme',
      ),
    )
  })

  it('sends a TYPED path as a path, on the very same build', async () => {
    // The two routes are chosen by what the gesture could answer with, never
    // by what kind of build this is: a person naming a file on the backend's
    // own machine still gets the path route in a browser.
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    await openBrowserAsk({ importPostman })

    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/w/acme-api' } })
    fireEvent.click(button('Import'))

    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith({ path: '/w/acme.json' }, '/w/acme-api'),
    )
  })

  it('forgets the document the moment a path is typed over it', async () => {
    // Two sources for one field would be two owners of the answer, and the
    // stale one wins by evaluation order: the person edits the field, and
    // the import silently sends the bytes they replaced.
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    await openBrowserAsk({ importPostman })

    dropOnAsk([exportFile('acme.postman_collection.json')])
    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe('acme.postman_collection.json'),
    )
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/other.json' } })
    fireEvent.click(button('Import'))

    // And the destination follows the new source, because nobody has typed
    // into that field — the `nocx-6hg2w.14` rule, unchanged by any of this.
    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { path: '/w/other.json' },
        '/data/collections/other',
      ),
    )
  })

  it('is gated on no build question — neither file reads the Wails runtime', () => {
    // The capability is what the GESTURE can answer with, never what kind of
    // build this is. `hasWailsWebview()` has exactly one caller in this
    // path — the kit's DropZone, deciding whether Go has already taken the
    // drop — and the ask asking it again would be a second answer to a
    // question that has an owner (AD-8).
    for (const file of ['src/api/import-dialogs.tsx', 'src/api/api-pane.tsx']) {
      expect(readFileSync(file, 'utf8')).not.toMatch(/hasWailsWebview/)
    }
  })
})

// ── An import opens what it wrote ─────────────────────────────────────────
//
// nocx-vkp9d. `api.collections.list` answers the OPEN folders and
// `api.import.postman` registers nothing, so a successful import left the
// collection on disk and absent from the tree: the person went to "Open a
// collection folder…" and named the path that had been in the field beside
// them a second earlier. That is the second step the whole import rework
// (nocx-is3qh) exists to remove, arriving through the back door.
//
// These drive the ASK, not the store — the defect was never in
// `store.openFolder`, which has always worked; it was that nothing called it.

/** The import ask, opened on a stand with nothing in the tree, with the two
 *  fields filled and Import pressed. `openCollection` is the call the OPEN
 *  goes through, so a test that is about the open overrides it. */
async function importInto(dest: string, over: Partial<ApiWorkbenchServices> = {}): Promise<void> {
  const { bar } = await mountApp({ ...noCollections(), ...over })
  await openImportAsk(bar)
  fireEvent.input(field('api-import-postman-file'), { target: { value: '/downloads/acme.json' } })
  fireEvent.input(field('api-import-postman-dest'), { target: { value: dest } })
  fireEvent.click(button('Import'))
}

/** Every message the person has been shown, newest last. */
function toastMessages(): string[] {
  return toasts().map((t) => t.message)
}

describe('an import opens its destination', () => {
  it('puts the imported collection in the tree with nothing else pressed', async () => {
    // The imported collection is named apart from anything the listing
    // carries, so "it is in the tree" cannot be satisfied by a row that was
    // already there.
    const openCollection = vi
      .fn()
      .mockResolvedValue({ handle: 'h-imported', collection: collectionFixture({ name: 'Acme' }) })
    await importInto('/data/collections/acme', { openCollection })

    // The open goes to the destination the import just wrote — not to
    // anything the person has to name a second time.
    await vi.waitFor(() => expect(openCollection).toHaveBeenCalledWith('/data/collections/acme'))
    await vi.waitFor(() => expect(workbench().textContent).toContain('Acme'))
    // And the ask has gone: nothing else is waiting to be pressed.
    await vi.waitFor(() => expect(dialogFor('api-import-postman-file').open).toBe(false))
  })

  it('the success toast says what happened and no more', async () => {
    const openCollection = vi
      .fn()
      .mockResolvedValue({ handle: 'h-imported', collection: collectionFixture({ name: 'Acme' }) })
    await importInto('/data/collections/acme', { openCollection })

    await vi.waitFor(() =>
      expect(toastMessages()).toContain('Imported into /data/collections/acme'),
    )
    expect(toasts().every((t) => t.level === 'success')).toBe(true)
    // The folder ask's own toast belongs to the folder ask. One act, one
    // sentence — a person who pressed Import once is not told twice.
    expect(toastMessages()).toHaveLength(1)
  })

  it('a REFUSED import opens nothing and keeps the ask, with the reason under the field', async () => {
    const openCollection = vi.fn()
    await importInto('/data/collections/acme', {
      importPostman: vi.fn().mockRejectedValue(new Error('a folder is already there')),
      openCollection,
    })

    await vi.waitFor(() => expect(destError()).toContain('a folder is already there'))
    expect(dialogFor('api-import-postman-file').open).toBe(true)
    expect(openCollection).not.toHaveBeenCalled()
    expect(toastMessages()).toHaveLength(0)
    // What was typed survives — the destination is what has to change.
    expect(field('api-import-postman-dest').value).toBe('/data/collections/acme')
  })

  it('an import whose OPEN fails reports THAT, not a success the tree contradicts', async () => {
    await importInto('/data/collections/acme', {
      openCollection: vi.fn().mockRejectedValue(new Error('permission denied')),
    })

    await vi.waitFor(() => expect(toasts()).toHaveLength(1))
    const told = toasts()[0]
    // The ask has closed — the import DID happen — so the validation slot is
    // gone and the toast is the only surface left. It must carry the second
    // failure's own words.
    expect(told.level).toBe('danger')
    expect(told.message).toContain('permission denied')
    // And it must not stand as a plain "Imported into X" while X is not on
    // screen: the sentence has to say the collection is not in the tree.
    expect(told.message).not.toBe('Imported into /data/collections/acme')
    expect(workbench().textContent).not.toContain('Acme')
  })

  it('a failed OPEN leaves the folder ask untouched — it owns none of this', async () => {
    // openFolder() next door owns `opening`, `openingFolder` and
    // `pathRefused`. The import borrows the store call and none of that
    // state, so a refusal here may not put a reason inside a dialog the
    // person never opened.
    await importInto('/data/collections/acme', {
      openCollection: vi.fn().mockRejectedValue(new Error('permission denied')),
    })

    await vi.waitFor(() => expect(toasts()).toHaveLength(1))
    expect(openFolderDialog().open).toBe(false)
    expect(openFolderDialog().textContent).not.toContain('permission denied')
  })
})

// ── The environment a request goes out under ──────────────────────────────
//
// nocx-pnvnn. `envRelPath` appeared NOWHERE in frontend/ or contracts/: the
// send path resolved variables against an environment the renderer had no
// way to name, so a collection whose URL is `{{baseUrl}}/…` — nearly every
// Postman export — failed from the product (`-32603 "{{baseUrl}}/users" is
// not an absolute URL`) while working perfectly over the control plane.
//
// So these drive the CHOICE, not the client. A dropdown that exists and
// cannot be reached is the same defect one layer up, and a test that called
// `store.send()` with a path it supplied itself would prove the client can
// spell envRelPath and nothing about whether a person can choose one.

/** The environment picker, as a person reaches it — and it is absent, not
 *  disabled, for a collection that has no environments at all. */
/** One row of the environments rail, by the name it shows. The picker was a
 *  Select on the pane's header; it is a list in the SIDEBAR now, beside the
 *  collections — an environment is chosen many times a day and edited rarely,
 *  so what is always on screen is the list, and the list IS the picker. */
function environmentRow(name: string): HTMLButtonElement | null {
  const rail = workbench().querySelector('.api-environments-rail')
  if (!rail) return null
  return (
    [...rail.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) => (b.textContent ?? '').trim() === name,
    ) ?? null
  )
}

/** Which row a send will go out under: the one the rail marks selected. */
function chosenEnvironment(): string {
  const rail = workbench().querySelector('.api-environments-rail')
  const on = rail?.querySelector<HTMLButtonElement>('button[aria-selected="true"]')
  return (on?.textContent ?? '').trim()
}

/** Open the workbench on a collection with the given environments, and put
 *  the worked example in the form. */
async function openRequestWithEnvironments(
  envs: ApiEnvironmentRef[],
  over: Partial<ApiWorkbenchServices> = {},
): Promise<{ sendRequest: ReturnType<typeof vi.fn> }> {
  const sendRequest = vi.fn().mockResolvedValue(sendFixture())
  const { bar } = await mountApp({
    listCollections: vi.fn().mockResolvedValue({
      collections: [collectionsFixture({ collection: collectionFixture({ environments: envs }) })],
      defaultRoot: DEFAULT_ROOT,
    }),
    sendRequest,
    ...over,
  })
  await openRequest(bar)
  return { sendRequest }
}

describe('the environment a request goes out under', () => {
  it('one environment needs no choosing — the send carries it', async () => {
    const { sendRequest } = await openRequestWithEnvironments([DEV_ENV])

    // The rail is there and already on it. A collection with exactly one
    // environment has no choice in it, and starting on None would make every
    // imported collection fail its first Send on a variable the folder can
    // answer.
    await vi.waitFor(() => expect(chosenEnvironment()).toBe(DEV_ENV.name))

    fireEvent.click(button('Send'))
    await vi.waitFor(() =>
      expect(sendRequest).toHaveBeenCalledWith(
        HANDLE,
        CREATE_REL_PATH,
        DEV_ENV.relPath,
        expect.any(String),
      ),
    )
  })

  it('a person chooses, and the NEXT send goes out under what they chose', async () => {
    const { sendRequest } = await openRequestWithEnvironments([DEV_ENV, PROD_ENV])

    // Several environments and nothing chosen: NONE, deliberately. The first
    // in a list is not a choice a person made, and one of them is usually
    // production.
    await vi.waitFor(() => expect(chosenEnvironment()).toBe('No environment'))
    const prod = environmentRow(PROD_ENV.name)
    if (!prod) throw new Error('no row for the production environment')

    fireEvent.click(prod)

    fireEvent.click(button('Send'))
    await vi.waitFor(() =>
      expect(sendRequest).toHaveBeenCalledWith(
        HANDLE,
        CREATE_REL_PATH,
        PROD_ENV.relPath,
        expect.any(String),
      ),
    )
  })

  it('choosing None again sends the request as written', async () => {
    const { sendRequest } = await openRequestWithEnvironments([DEV_ENV])
    await vi.waitFor(() => expect(chosenEnvironment()).toBe(DEV_ENV.name))

    // Sending under NONE is a choice somebody makes, so it is a row like the
    // others rather than an absence they have to work out.
    const none = environmentRow('No environment')
    if (!none) throw new Error('no row for sending as written')
    fireEvent.click(none)

    fireEvent.click(button('Send'))
    await vi.waitFor(() =>
      expect(sendRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, '', expect.any(String)),
    )
  })

  it('a collection with no environments offers no rows and says so', async () => {
    await openRequestWithEnvironments([])
    expect(environmentRow(DEV_ENV.name)).toBeNull()
    expect(workbench().textContent).toContain('None yet')
  })

  it('the run says which environment answered, in the words the BACKEND used', async () => {
    // Not an echo of what the renderer asked for: it names an environment by
    // its PATH and the name lives inside the file, so only the result can say
    // which record answered. The fixture's name is deliberately not the
    // file's stem, so a renderer deriving one from the other fails here.
    const sendRequest = vi.fn().mockResolvedValue(sendFixture({}, DEV_ENV.name))
    await openRequestWithEnvironments([DEV_ENV], { sendRequest })
    await vi.waitFor(() => expect(chosenEnvironment()).toBe(DEV_ENV.name))

    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    expect(runCards()[0].textContent).toContain(DEV_ENV.name)
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
    // keyboard goes to the one thing a person can do from here — the control
    // the rare doors are behind. The panel wears no form (nocx-84shs) and
    // every path field lives inside a dialog that is closed.
    content.focus()
    expect(document.activeElement).toBe(target.querySelector('#api-collections-menu'))

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
    expect(setTitle).toHaveBeenCalledWith('API testing')
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

describe('a variable in the address says whether anything answers it', () => {
  const marks = (): HTMLElement[] => [
    ...workbench().querySelectorAll<HTMLElement>('.ui-text-field__mark'),
  ]

  const variableMenu = (): HTMLElement | null =>
    document.querySelector<HTMLElement>('[data-testid="api-variable-menu"]')

  /** A collection with ONE environment, so it is the active one without
   *  anybody choosing — and a read that answers whatever the test says. */
  const withEnvironment = (values: Record<string, string>): Partial<ApiWorkbenchServices> => ({
    listCollections: vi.fn().mockResolvedValue({
      collections: [
        collectionsFixture({
          collection: collectionFixture({ environments: [DEV_ENV] }),
        }),
      ],
      defaultRoot: '/data/collections',
    }),
    readEnvironment: vi.fn().mockResolvedValue({
      environment: {
        name: 'dev',
        values,
        secretVars: [],
        route: { kind: 'direct' as const, profileId: '', insecureTls: false },
      },
    }),
  })

  it('an environment that answers the name leaves the mark ordinary, and says what it is', async () => {
    const { bar } = await mountApp(withEnvironment({ baseUrl: 'https://api.example.test' }))
    await openRequest(bar)

    await vi.waitFor(() => expect(marks()[0]?.dataset.tone).toBe('reference'))

    fireEvent.click(marks()[0])
    await vi.waitFor(() => expect(variableMenu()).toBeTruthy())
    // The VALUE, because that is what a person clicked the thing to find out.
    expect(variableMenu()?.textContent).toContain('https://api.example.test')
  })

  it("a request's own variable answers before the environment, and the panel says so", async () => {
    // The inheritance is the point: the same name is answered twice, and
    // which one wins decides what goes out.
    const { bar } = await mountApp(
      withEnvironment({ baseUrl: 'https://from-the-environment.test' }),
    )
    await openRequest(bar)
    await vi.waitFor(() => expect(marks()[0]?.dataset.tone).toBe('reference'))

    // Add it at REQUEST scope through the panel's own door.
    fireEvent.click(marks()[0])
    await vi.waitFor(() => expect(variableMenu()).toBeTruthy())
    const here = [...(variableMenu()?.querySelectorAll('button') ?? [])].find((b) =>
      (b.textContent ?? '').includes('to this request'),
    )
    expect(here, "the panel offers the request's own scope").toBeTruthy()
    fireEvent.click(here as HTMLButtonElement)

    // It is in the request's variables, and the panel now names that scope
    // rather than the environment's value.
    await vi.waitFor(() => {
      const values = [...workbench().querySelectorAll<HTMLInputElement>('input')].map(
        (i) => i.value,
      )
      expect(values).toContain('baseUrl')
    })
    fireEvent.click(marks()[0])
    await vi.waitFor(() => expect(variableMenu()?.textContent).toContain("this request's own"))
    expect(variableMenu()?.textContent).not.toContain('from-the-environment')
  })

  it("a secret reference wears the vault's colour and never a value", async () => {
    // The panel does not hold a secret's value and must not learn to want
    // one: what it can say is that the name stands for something the vault
    // holds, and where.
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({ collection: collectionFixture({ environments: [DEV_ENV] }) }),
        ],
        defaultRoot: '/data/collections',
      }),
      readEnvironment: vi.fn().mockResolvedValue({
        environment: {
          name: 'dev',
          values: {},
          secretVars: ['baseUrl'],
          route: { kind: 'direct' as const, profileId: '', insecureTls: false },
        },
      }),
    })
    await openRequest(bar)

    await vi.waitFor(() => expect(marks()[0]?.dataset.tone).toBe('secret'))

    fireEvent.click(marks()[0])
    await vi.waitFor(() => expect(variableMenu()).toBeTruthy())
    expect(variableMenu()?.textContent).toContain('from the vault')
  })

  it('a name nothing answers is marked, and the menu offers to define it', async () => {
    // An environment that answers something ELSE: the state a person is in
    // when they have typed a variable and not yet said what it is.
    const { bar } = await mountApp(withEnvironment({ other: 'x' }))
    await openRequest(bar)

    await vi.waitFor(() => expect(marks()[0]?.dataset.tone).toBe('unknown'))

    fireEvent.click(marks()[0])
    await vi.waitFor(() => expect(variableMenu()).toBeTruthy())
    // The header names BOTH scopes it looked in — which is what tells a
    // person the request could answer this itself.
    expect(variableMenu()?.textContent).toContain('nothing answers it')
    expect(variableMenu()?.textContent).toContain('this request')

    // Two doors, and the environment one lands the row in the environment
    // editor. (The request-scope door is asserted in its own test below.)
    const define = [...(variableMenu()?.querySelectorAll('button') ?? [])].find((b) =>
      (b.textContent ?? '').includes('Add baseUrl to dev'),
    )
    expect(define, 'the menu offers to define the variable').toBeTruthy()
    fireEvent.click(define as HTMLButtonElement)
    await vi.waitFor(() => {
      const values = [...workbench().querySelectorAll<HTMLInputElement>('input')].map(
        (i) => i.value,
      )
      expect(values).toContain('baseUrl')
    })
  })
})
