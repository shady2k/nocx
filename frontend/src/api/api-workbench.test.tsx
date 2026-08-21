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
  createdFixture,
  exchangeFixture,
  noCollections,
  sendFixture,
  servicesFixture,
  watchFixture,
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

    // No environment: the default fixture's collection declares none, so the
    // send names none — the request as written, on the direct route.
    await vi.waitFor(() => expect(send).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, ''))
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
      expect(importPostman).toHaveBeenCalledWith('/w/acme.json', '/w/acme-api'),
    )
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
      expect(sendRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, DEV_ENV.relPath),
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
      expect(sendRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, PROD_ENV.relPath),
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
    await vi.waitFor(() => expect(sendRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, ''))
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
