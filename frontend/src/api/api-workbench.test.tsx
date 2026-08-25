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
import type { ApiRequestScopeResult } from '../generated/api.request.scope'
import type { PaneHost } from '../pane-content'
import type { ApiEnvironmentRef, ApiRequest } from './api-model'
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
  folderCreatedFixture,
  folderOnDisk,
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
import { clearToasts, toasts, type Toast } from '../ui/toast'
import { JSON_LAYOUT_LIMIT } from '../ui/format-json'
import { malformedReason } from './malformed-reason'

vi.mock('../renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

// The pane makes its own clipboard through `createClipboardAccess` (no
// composition-root seam exists for the workbench yet), so the Copy Path
// tests replace the module seam with a recorder — the same controlled
// instrument the Files panel gets by injection.
const clipboardMock = vi.hoisted(() => {
  const writes: string[] = []
  return {
    writes,
    access: {
      writeText: (text: string): Promise<void> => {
        writes.push(text)
        return Promise.resolve()
      },
      readText: (): Promise<string> => Promise.resolve(''),
    },
  }
})

// PARTIAL, and it has to be. `panes-fixtures.ts` constructs a real
// `ClipboardGate` out of this same module for every mount in this file, so a
// whole-module replacement takes the gate away with the factory and every
// test that mounts the app dies before it reaches its own subject.
vi.mock('../clipboard', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../clipboard')>()),
  createClipboardAccess: () => clipboardMock.access,
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

/** A picker a person can reach right now.
 *
 *  REACHABLE, not merely rendered — the same distinction every helper below
 *  asks for, and it became load-bearing here when the two mode pickers moved
 *  out of the tab row and into the panels they govern (nocx-n9npi). Tabs
 *  renders every section and marks the inactive ones `hidden`, so the auth
 *  scheme is in the document while Body is open; without this filter
 *  "the picker is not offered on a tab it does not govern" is not a question
 *  this file can ask. */
function control(field: string): HTMLSelectElement {
  const el = [
    ...workbench().querySelectorAll<HTMLSelectElement>(`[data-api-field="${field}"] select`),
  ].find(reachable)
  if (!el) throw new Error(`no control for ${field}`)
  return el
}

/** True when the element is not sealed inside a closed `<dialog>`, and not
 *  inside a hidden subtree.
 *
 *  A closed native dialog keeps its children in the document, so the
 *  workbench holds the controls of BOTH its asks at all times and a plain
 *  `querySelectorAll` would answer with a Cancel the person cannot see. This
 *  is the difference between "rendered" and "reachable", and every helper
 *  below asks for the second.
 *
 *  `[hidden]` is the same distinction one level in, and it arrived with the
 *  import ask's reshape (nocx-ysyy2): the two path fields stay ADDRESSABLE —
 *  a native drop and the system picker answer with a path that has to land
 *  somewhere, and every drop test names them by id — while ceasing to be
 *  fields a person is asked to fill in. Without this half, "the ask no
 *  longer shows a path field" is not a question this file can ask. */
function reachable(el: Element): boolean {
  const dialog = el.closest('dialog')
  if (dialog !== null && !dialog.open) return false
  return el.closest('[hidden]') === null
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
    const collection = await vi.waitFor(() =>
      workbench().querySelector<HTMLElement>(`[data-row-key="${HANDLE}:"]`),
    )
    if (!collection) throw new Error('no collection row on screen')
    // A DEAD FOLDER IS STILL VISIBLE — the claim this test guards is that a
    // collection whose listing was replaced was not silently dropped, and
    // that is still true: the row is in the tree. What changed is WHERE the
    // reason lives — on the row's `title` (a title is an attribute, not text
    // content), so the old assertion against the tree's text is asserted on
    // the hover instead.
    expect(collection.querySelector('.ui-tree-row__name')?.getAttribute('title')).toBe(
      'the folder was replaced',
    )
    const tree = workbench().querySelector('.api-tree')
    if (!tree) throw new Error('no tree on screen')
    expect(tree.textContent).not.toContain('the folder was replaced')
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
    const rowEl = await vi.waitFor(() =>
      workbench().querySelector<HTMLElement>(`[data-row-key="${HANDLE}:!users/oops.json"]`),
    )
    if (!rowEl) throw new Error('no malformed row on screen')
    // ONE BAD FILE DOES NOT HIDE THE GOOD ONES — the claim this test guards:
    // a file the parser cannot read is a ROW rather than a silently dropped
    // entry, so it stays findable. What changed is WHERE what-was-wrong
    // lives: on the row's `title`, in the person-facing words, and the raw
    // decoder sentence appears nowhere in the tree's text (a title is an
    // attribute, not content).
    expect(rowEl.querySelector('.ui-tree-row__name')?.textContent).toContain('oops.json')
    expect(rowEl.querySelector('.ui-tree-row__name')?.getAttribute('title')).toBe(
      malformedReason('unexpected end of JSON input'),
    )
    const tree = workbench().querySelector('.api-tree')
    if (!tree) throw new Error('no tree on screen')
    expect(tree.textContent).not.toContain('unexpected end of JSON input')
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
/** Open a row's own menu the way a person does — the right button, aimed at
 *  the row. Returns the event, because "and the webview's own menu did not
 *  appear" is an assertion about it. */
function rightClick(el: HTMLElement): MouseEvent {
  const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
  el.dispatchEvent(e)
  return e
}

/** Right-click a request row and pick one of its actions. */
async function pickOnRow(relPath: string, label: string): Promise<void> {
  rightClick(row(relPath))
  await vi.waitFor(() => menuItem(label))
  fireEvent.click(menuItem(label))
}

/** The confirm the kit puts up — it renders onto document.body, outside the
 *  workbench, so it is never found by a query rooted in the pane. */
function confirmText(): string {
  const dialogs = [...document.querySelectorAll<HTMLDialogElement>('dialog')].filter((d) => d.open)
  return dialogs.map((d) => d.textContent ?? '').join(' ')
}

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

// ── A long line, and the box it moves in ──────────────────────────────────
//
// nocx-kdawd, reported by the owner from real use: a JSON body with one long
// value — a token, a base64 blob — could not be moved sideways INSIDE the
// editor. It wrapped, so a value's continuation sat against the line numbers,
// and the only scrollbar that moved anything was the pane's, which moved the
// whole surface with it.
//
// The answer is the one the read-only hosts already gave and the one the raw
// view now gives for the same octets: the line stays a line, and the box it
// is in is what scrolls. ONE answer for both, which is the half of the bead
// that is about not shipping two.

/** A body whose one value is longer than any pane this product has. */
const LONG_BODY = `{"token":"${'x'.repeat(400)}"}`

function bodyEditor(): HTMLElement {
  const el = workbench().querySelector<HTMLElement>('.api-body-editor')
  if (!el) throw new Error('the body editor is not on screen')
  return el
}

/** The document as CM6 has it: one div.cm-line per line, and no newline text
 *  nodes, so a plain textContent read would say every body is one line. */
function editorLines(el: HTMLElement): string[] {
  return [...el.querySelectorAll('.cm-line')].map((l) => l.textContent ?? '')
}

/** Whether a box wraps, asked of the thing that carries the decision: CM6
 *  puts `cm-lineWrapping` on the content it wraps, and CodeBlock marks the
 *  variant that does not on the element the stylesheet keys off. */
function wraps(el: HTMLElement): boolean {
  const content = el.querySelector('.cm-content')
  if (content) return content.classList.contains('cm-lineWrapping')
  return el.dataset.wrap !== 'false'
}

/** The product's own stylesheet, in the document, so a computed overflow is
 *  the shipped rule and not one this file re-typed.
 *
 *  jsdom applies CSS from a <style> element and computes NO layout, so this
 *  can say WHO owns the sideways scroll and can never say that a scrollbar
 *  appeared. The second half needs a real engine (e2e/), and the first is
 *  what this bead is about: the movement belongs to the editor's own box. */
function injectStyles(name: string): HTMLStyleElement {
  const style = document.createElement('style')
  style.textContent = readFileSync(`src/styles/components/${name}`, 'utf8')
  document.head.append(style)
  return style
}

describe('a long line is read by moving inside the box that holds it', () => {
  const styles: HTMLStyleElement[] = []
  afterEach(() => {
    for (const s of styles) s.remove()
    styles.length = 0
  })

  async function openLongBody(): Promise<void> {
    const { bar } = await mountApp({
      readRequest: vi.fn().mockResolvedValue({
        request: { ...REQUEST, body: { kind: 'json', text: LONG_BODY, fileRef: '' } },
      }),
    })
    await openRequest(bar)
    await vi.waitFor(() => expect(editorLines(bodyEditor())).toEqual([LONG_BODY]))
  }

  it('the body editor keeps the long value on one line rather than wrapping it', async () => {
    await openLongBody()
    // ONE line, and it is the whole body: nothing folded it, so there is
    // something for a sideways scroll to move. Before this the same document
    // was a stack of rows against the gutter.
    expect(editorLines(bodyEditor())).toHaveLength(1)
    expect(wraps(bodyEditor())).toBe(false)
  })

  it('the scroll that moves it belongs to the editor, and the pane is not asked', async () => {
    styles.push(injectStyles('api-workbench.css'))
    await openLongBody()

    const scroller = bodyEditor().querySelector<HTMLElement>('.cm-scroller')
    if (!scroller) throw new Error('the editor has no scroller of its own')
    expect(getComputedStyle(scroller).overflow).toBe('auto')
    // …and the editor's own box CLIPS, so the long line cannot reach the pane
    // around it and make the surface the thing that moves.
    expect(getComputedStyle(bodyEditor()).overflow).toBe('hidden')
  })

  it('the request preview in the run list answers the same way, not a second one', async () => {
    const { bar } = await mountApp({
      readRequest: vi.fn().mockResolvedValue({
        request: { ...REQUEST, body: { kind: 'json', text: LONG_BODY, fileRef: '' } },
      }),
    })
    await openRequest(bar)
    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(runCards()).toHaveLength(1))
    fireEvent.click(optionIn(runCards()[0], 'Raw'))

    const preview = runCards()[0].querySelector<HTMLElement>('[aria-label="Raw request"]')
    if (!preview) throw new Error('no raw request preview on this run')
    // The preview holds the bytes the editor holds, so it gives the editor's
    // answer. Asserted against the editor rather than against a literal:
    // "one answer, not two" is a statement about the pair.
    expect(wraps(preview)).toBe(wraps(bodyEditor()))
    expect(wraps(preview)).toBe(false)
  })
})

// ── Laying the request body out ───────────────────────────────────────────
//
// nocx-7c39h. A body pasted from a shell, a log or a colleague arrives on one
// line and there was no control to lay it out, so a person either read the
// line or took the body to another program and pasted it back.

/** A request whose body is JSON on one line, which is how a pasted one
 *  arrives. `over` is for the tests that need a different document. */
function jsonBodyRequest(text = '{"email":"a@b.c","meta":{"n":1}}') {
  return { ...REQUEST, body: { kind: 'json' as const, text, fileRef: '' } }
}

/** The body as the editor holds it, joined the way CM6 stores it. */
function bodyText(): string {
  return editorLines(bodyEditor()).join('\n')
}

/** Open the workbench on a request with the given body, and wait until the
 *  editor is holding it — the point at which a person could press anything. */
async function openBody(text?: string): Promise<void> {
  const { bar } = await mountApp({
    readRequest: vi.fn().mockResolvedValue({ request: jsonBodyRequest(text) }),
  })
  await openRequest(bar)
  fireEvent.click(button('Body •'))
  await vi.waitFor(() => expect(bodyText()).not.toBe(''))
}

describe('a body that arrived on one line can be laid out', () => {
  it('lays it out into one field per line, indented', async () => {
    await openBody()
    expect(bodyText()).toBe('{"email":"a@b.c","meta":{"n":1}}')

    fireEvent.click(button('Format'))

    await vi.waitFor(() => expect(editorLines(bodyEditor()).length).toBeGreaterThan(1))
    const laid = bodyText()
    expect(laid).toContain('\n  "email": "a@b.c"')
    expect(laid).toContain('\n    "n": 1')
  })

  // ONLY WHITESPACE MOVED, asserted on what is SENT rather than on what is
  // shown: the file behind the request is what the backend reads, so the
  // document that matters is the one Save writes.
  it('what is saved afterwards parses to the document that was there before', async () => {
    const write = vi.fn().mockResolvedValue({})
    const before = '{"email":"a@b.c","meta":{"n":1},"tags":[1,2]}'
    const { bar } = await mountApp({
      readRequest: vi.fn().mockResolvedValue({ request: jsonBodyRequest(before) }),
      writeRequest: write,
    })
    await openRequest(bar)
    fireEvent.click(button('Body •'))
    await vi.waitFor(() => expect(bodyText()).toBe(before))

    fireEvent.click(button('Format'))
    await vi.waitFor(() => expect(editorLines(bodyEditor()).length).toBeGreaterThan(1))

    // Nothing is pressed: the draft reaches its file when typing stops.
    await vi.waitFor(() => expect(write).toHaveBeenCalled(), { timeout: 3000 })
    const written = write.mock.calls[0][2] as { body: { text: string } }
    expect(written.body.text).not.toBe(before)
    expect(JSON.parse(written.body.text)).toEqual(JSON.parse(before))
  })

  it('is idempotent: pressing it a second time moves nothing', async () => {
    await openBody()
    fireEvent.click(button('Format'))
    await vi.waitFor(() => expect(editorLines(bodyEditor()).length).toBeGreaterThan(1))
    const once = bodyText()

    fireEvent.click(button('Format'))
    // Waited on rather than read straight back, so a second layout that DID
    // move something would have landed before the assertion.
    await vi.waitFor(() => expect(bodyText()).toBe(once))
  })

  // NOT MANGLED, AND NOT SILENT. A body a person is about to send is the last
  // place for a best effort, and a control that appears to do nothing is
  // indistinguishable from a broken one.
  it('a body that is not JSON is left exactly as it was, and the control says why', async () => {
    const notJson = 'name=a&email=a@b.c'
    await openBody(notJson)

    fireEvent.click(button('Format'))

    await vi.waitFor(() => expect(toasts()).toHaveLength(1))
    expect(toasts()[0].message).toContain('not valid JSON')
    expect(bodyText()).toBe(notJson)
  })

  // ABSENCE IS THE CAPABILITY — the rule this row already follows for its
  // pickers. A `raw` body has no formatter, so there is no Format beside it;
  // a greyed one would advertise something the surface cannot do.
  it('the control is absent where the body mode has no formatter, not present and inert', async () => {
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Body •'))
    // The worked example's body is `raw`.
    await vi.waitFor(() => expect(control('body-kind').value).toBe('raw'))
    expect(buttonNames()).not.toContain('Format')

    fireEvent.change(control('body-kind'), { target: { value: 'json' } })

    await vi.waitFor(() => expect(buttonNames()).toContain('Format'))

    // And it leaves again when the kind does, rather than lingering as a
    // control for a mode that no longer has one.
    fireEvent.change(control('body-kind'), { target: { value: 'form' } })
    await vi.waitFor(() => expect(buttonNames()).not.toContain('Format'))
  })

  it('offers nothing to format on the tabs that are not the body', async () => {
    const { bar } = await mountApp({
      readRequest: vi.fn().mockResolvedValue({ request: jsonBodyRequest() }),
    })
    await openRequest(bar)
    fireEvent.click(button('Body •'))
    await vi.waitFor(() => expect(buttonNames()).toContain('Format'))

    fireEvent.click(button('Auth •'))
    await vi.waitFor(() => expect(buttonNames()).not.toContain('Format'))
  })
})

// ── A section's own controls are inside that section ──────────────────────
//
// nocx-n9npi. The row that NAMES the four sections also held one section's
// contents: Format and the mode picker while Body was open, the scheme picker
// while Auth was, swapped under the tabs as a person moved between them. The
// kit's trailing slot is for a control that belongs to the SURFACE — the run
// card's status, size and elapsed sit there and are the same three whichever
// view is open — and a control drawn for exactly one tab is that tab's
// content, not the row's.
//
// Three costs, and the first is measured: the bar reported 566px in a 496px
// column and the overflow travelled up until the whole request column drew a
// horizontal scrollbar (nocx-kdawd); the mode picker decides how the editor
// below it reads and sat above the tab row instead of beside it; and Postman,
// the owner's reference, puts the tabs in one row and the body's own controls
// in a second row inside the Body panel.

/** A panel of the REQUEST editor's tabs. Scoped, because a run card's tabs
 *  use the ids `body` and `headers` too — with an exchange on screen the
 *  document holds two `#ui-tabpanel-body`s and the first one is the run's. */
function requestPanel(id: string): HTMLElement {
  const el = workbench().querySelector<HTMLElement>(`.api-request #ui-tabpanel-${id}`)
  if (!el) throw new Error(`no request panel ${id}`)
  return el
}

/** The row of section names in the request editor. */
function requestTabList(): HTMLElement {
  const el = workbench().querySelector<HTMLElement>('.api-request .ui-tabs__list')
  if (!el) throw new Error('no tab list on the request editor')
  return el
}

/** The trailing slot of the request editor's tab row. */
function requestTabActions(): HTMLElement {
  const el = workbench().querySelector<HTMLElement>('.api-request .ui-tabs__bar .ui-tabs__actions')
  if (!el) throw new Error('no actions slot on the request tab row')
  return el
}

/** A reachable button inside one element, by its words. */
function buttonIn(el: HTMLElement, name: string): HTMLButtonElement | undefined {
  return [...el.querySelectorAll('button')]
    .filter(reachable)
    .find((b) => (b.getAttribute('aria-label') ?? b.textContent ?? '').trim() === name)
}

/** True when `first` comes before `second` in document order. */
function precedes(first: Element, second: Element): boolean {
  return (first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
}

describe("a section's own controls are inside that section", () => {
  it('the body mode and Format are in the Body panel, above the editor', async () => {
    await openBody()

    const panel = requestPanel('body')
    expect(panel.querySelector('[data-api-field="body-kind"] select')).not.toBeNull()
    const format = buttonIn(panel, 'Format')
    expect(format).toBeDefined()

    // ABOVE the editor, which is the half that makes it a control of this
    // panel rather than a control that merely lives in it.
    const editor = panel.querySelector('.api-body-editor')
    if (!editor) throw new Error('no body editor in the panel')
    expect(precedes(format!, editor)).toBe(true)
    expect(precedes(panel.querySelector('[data-api-field="body-kind"]')!, editor)).toBe(true)
  })

  it('the auth scheme is in the Auth panel', async () => {
    await openAuth()

    expect(requestPanel('auth').querySelector('[data-api-field="auth-kind"] select')).not.toBeNull()
  })

  it('neither picker is offered on a tab it does not govern, and neither is greyed', async () => {
    // Tabs renders every section and hides the inactive ones, so both pickers
    // are in the document at all times — the question is which one a person
    // can reach, and the answer must still be exactly one.
    const { bar } = await mountApp({
      readRequest: vi.fn().mockResolvedValue({ request: jsonBodyRequest() }),
    })
    await openRequest(bar)

    fireEvent.click(button('Body •'))
    await vi.waitFor(() => expect(control('body-kind').value).toBe('json'))
    expect(() => control('auth-kind')).toThrow()

    fireEvent.click(button('Auth •'))
    await vi.waitFor(() => expect(() => control('auth-kind')).not.toThrow())
    expect(() => control('body-kind')).toThrow()

    // ABSENCE, NOT A GREYED CONTROL — neither row ever holds an inert one.
    expect(workbench().querySelectorAll('.api-request__controls [disabled]')).toHaveLength(0)
  })

  it('the tab row of this surface passes no actions at all, on every tab', async () => {
    // Nothing tab-independent is left over, so the slot is handed nothing —
    // and the slot itself STAYS in the kit, because the run card's tabs put
    // a genuinely section-independent thing in it.
    const { bar } = await mountApp({
      readRequest: vi.fn().mockResolvedValue({ request: jsonBodyRequest() }),
    })
    await openRequest(bar)

    // Every tab, named the way the row names them rather than by a list this
    // test keeps in step by hand.
    const tabs = [...requestTabList().querySelectorAll('button')].map((b) =>
      (b.textContent ?? '').trim(),
    )
    expect(tabs).toHaveLength(5)

    for (const tab of tabs) {
      fireEvent.click(button(tab))
      await vi.waitFor(() => expect(button(tab).getAttribute('aria-selected')).toBe('true'))
      expect(requestTabActions().childElementCount).toBe(0)
      expect(requestTabActions().textContent).toBe('')
    }
  })
})

// ── Reading the answer ────────────────────────────────────────────────────
//
// nocx-dhojo, reported by the owner from real use: a minified JSON answer was
// shown exactly as it arrived, so a 154-byte body was read by dragging a
// scrollbar sideways and a large one could not be read at all. Body and Raw
// are two different questions — Raw is what came off the socket and Body is
// what the answer SAYS — and only one of them is allowed to move.

/** The response body as it is on screen, line by line. */
function responseBodyLines(card: HTMLElement): string[] {
  const el = card.querySelector<HTMLElement>('[aria-label="Response body"]')
  if (!el) throw new Error('no response body on this run')
  return editorLines(el)
}

/** Send one exchange whose response is exactly the given fixture, and answer
 *  with a way to ASK for the card it produced.
 *
 *  An accessor rather than the element: choosing a tab goes through the store,
 *  which re-renders the list, so a card captured once is a card that stops
 *  being the one on screen the moment the test clicks anything. */
async function sentAnswer(over: Parameters<typeof sendFixture>[0]): Promise<() => HTMLElement> {
  const { bar } = await mountApp({ sendRequest: vi.fn().mockResolvedValue(sendFixture(over)) })
  await openRequest(bar)
  fireEvent.click(button('Send'))
  await vi.waitFor(() => expect(runCards()).toHaveLength(1))
  return () => runCards()[0]
}

describe('the answer is laid out for reading, and Raw stays the bytes', () => {
  const MINIFIED = '{"id":"usr_8f21","tags":["a","b"],"meta":{"n":1}}'

  it('a minified JSON answer is one field per line, indented', async () => {
    const card = await sentAnswer({ text: MINIFIED })

    await vi.waitFor(() => expect(responseBodyLines(card()).length).toBeGreaterThan(1))
    const lines = responseBodyLines(card())
    expect(lines[0]).toBe('{')
    expect(lines).toContain('  "id": "usr_8f21",')
    // Indented AGAIN where it nests, which is the depth a one-line document
    // does not show.
    expect(lines).toContain('    "n": 1')
    // And it is the same document: only whitespace moved.
    expect(JSON.parse(lines.join('\n'))).toEqual(JSON.parse(MINIFIED))
  })

  // THE OTHER TAB IS THE OTHER QUESTION. The raw view is composed by the
  // SENDER and shows what went over the socket, so the layout must be
  // invisible to it — byte for byte, including the minified body.
  it('Raw still shows the bytes exactly as they arrived', async () => {
    const card = await sentAnswer({ text: MINIFIED, raw: responseRawFixture() })
    fireEvent.click(optionIn(card(), 'Raw'))

    await vi.waitFor(() => expect(currentTab(card())).toBe('Raw'))
    const raw = rawBlockText(card(), 'Raw response')
    expect(raw).toContain('{"id":"usr_8f21"}')
    // Not a trace of the layout: no indented field, no line break inside the
    // document. If this ever fails, two tabs have become one.
    expect(raw).not.toContain('"id": "usr_8f21"')
  })

  // WHAT THE RUN RECORDED IS UNTOUCHED. The size is the backend's count of
  // the bytes it read, and a renderer that re-derived it from the text it is
  // showing would report the layout's whitespace as payload.
  it('the size on the run is the bytes off the socket, not the laid-out text', async () => {
    const card = await sentAnswer({ text: MINIFIED, size: 154 })
    await vi.waitFor(() => expect(responseBodyLines(card()).length).toBeGreaterThan(1))
    expect(card().textContent).toContain('154B')
    expect(card().textContent).toContain('154 bytes')
  })

  // DECLARED JSON, NOT SENT AS JSON. Shown as it arrived and nothing claims
  // otherwise — the panel does not argue with the header, and Raw is one tab
  // away for exactly that case.
  it('a body that does not parse is shown unchanged rather than mangled', async () => {
    const broken = '{"id":"usr_8f21"'
    const card = await sentAnswer({ text: broken })
    await vi.waitFor(() => expect(responseBodyLines(card())).toEqual([broken]))
    expect(card().textContent).not.toContain('laid out')
  })

  // AND A BODY THAT IS NOT DECLARED JSON IS NOT TOUCHED AT ALL. The content
  // type is what decides whether to try; the bytes never are.
  it('a body the server did not call JSON is shown as it arrived', async () => {
    const html = '<html><body>hello</body></html>'
    const card = await sentAnswer({
      text: html,
      headers: [{ name: 'Content-Type', value: 'text/html', enabled: true }],
    })
    await vi.waitFor(() => expect(responseBodyLines(card())).toEqual([html]))
  })

  // THE THRESHOLD, NAMED. Laying out a body is a main-thread parse that
  // cannot be interrupted, so past the cap the pane shows the bytes and says
  // why rather than freezing while a person waits for it.
  it(`a body over ${JSON_LAYOUT_LIMIT} characters says so instead of freezing the pane`, async () => {
    const filler = 'y'.repeat(JSON_LAYOUT_LIMIT - '{"k":""}'.length + 1)
    const oversize = `{"k":"${filler}"}`
    expect(oversize.length).toBe(JSON_LAYOUT_LIMIT + 1)

    const card = await sentAnswer({ text: oversize, size: oversize.length })

    // Shown as it arrived, on ONE line. Asserted as a PREFIX rather than as
    // the whole document: CM6 renders only what its viewport needs of an
    // enormous line, so the element holds the first couple of thousand
    // characters of it and never the rest. What matters is that nothing
    // reshaped it — a laid-out `{"k":"…"}` is more than one line and spells
    // its field `"k": "`, with a space.
    await vi.waitFor(() => expect(responseBodyLines(card())).toHaveLength(1))
    const shown = responseBodyLines(card())[0]
    expect(oversize.startsWith(shown)).toBe(true)
    expect(shown).toContain('{"k":"')
    // …and the pane SAYS so. A degrade nothing on screen mentions is how a
    // feature that does not exist survives a release.
    expect(card().textContent).toContain('too large to lay out')
    expect(card().textContent).toContain(`${JSON_LAYOUT_LIMIT / 1024}K characters`)
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
  // The paste box, because it is what the ask now OPENS as: the two path
  // fields are present and not reachable, so waiting on one of those would
  // wait for something that never comes (nocx-ysyy2).
  await vi.waitFor(() => expect(reachable(field('api-import-paste'))).toBe(true))
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
    // the half a test that called the picker directly could not say. It is
    // the REGION's now: the export field's own trailing picker went with the
    // field when the ask stopped asking for paths (nocx-ysyy2), and one
    // capability had two controls only for as long as there were two places
    // to put one.
    expect(buttonNames()).toContain('Or select a file')
    fireEvent.click(button('Or select a file'))

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
    fireEvent.click(button('Or select a file'))

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
    // What retires is the NATIVE picker, and the region is where it is
    // reached now (nocx-ysyy2). Its absence is read off the kit's own file
    // input, which is what the region falls back to: the browser half never
    // depended on Wails, so "the control is gone" cannot be read off the
    // button's words — both halves use them.
    expect(importAskBody().querySelector('.ui-file-input__native')).toBeNull()

    fireEvent.click(button('Or select a file'))

    await vi.waitFor(() =>
      expect(importAskBody().querySelector('.ui-file-input__native')).not.toBeNull(),
    )
    // The refusal costs the person nothing they typed, and is said where
    // every other refusal in this ask is said.
    expect(field('api-import-postman-file').value).toBe('/w/half-typed.json')
    expect(workbench().textContent).toContain('method not found')
  })

  it('cancelling the file picker leaves what was typed untouched', async () => {
    const { bar } = await mountApp({ openFile: vi.fn().mockResolvedValue({ path: '' }) })
    await openImportAsk(bar)
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/half-typed.json' } })

    fireEvent.click(button('Or select a file'))

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

    await vi.waitFor(() => expect(reachable(field('api-import-paste'))).toBe(true))
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/w/acme-api' } })
    fireEvent.click(button('Import'))

    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith({ path: '/w/acme.json' }, '/w/acme-api'),
    )
  })
})

// ── ONE QUESTION, NOT TWO PATHS ───────────────────────────────────────────
//
// nocx-ysyy2. The ask still opened as a form of two ABSOLUTE PATHS with a
// drop region added above them: a file a person downloaded thirty seconds
// ago, and a folder that does not exist yet, neither of which they can
// answer without leaving the app. Postman's own dialog — the owner's
// reference — asks one question across the top and offers the destination
// rather than demanding it.
//
// So the paste box is the ask now, the two fields stop being visible, and
// what the ask is holding is stated in one line a person can take back.
// The ids do not change: moving a field is not renaming it, and a native
// drop and the system picker still answer with a path that has to land
// somewhere.

/** What the ask says it is currently holding, or '' when it holds nothing. */
function sourceLine(): string {
  const el = importAskBody().querySelector<HTMLElement>('.api-import-source')
  return el === null || !reachable(el) ? '' : (el.textContent ?? '').trim()
}

/** The destination the one summary line NAMES, or '' when it names none —
 *  which is the line inviting the person to choose one, and is also what it
 *  reads while the field is out instead. */
function destSummary(): string {
  const el = importAskBody().querySelector<HTMLElement>('.api-import-dest')
  if (el === null || !reachable(el)) return ''
  const named = /^Imports into: (.+)$/.exec((el.textContent ?? '').trim())
  return named === null ? '' : named[1]
}

function paste(text: string): void {
  fireEvent.input(field('api-import-paste'), { target: { value: text } })
}

describe('the import ask asks one question', () => {
  it('opens with a paste box, and neither path field is reachable', async () => {
    const { bar } = await mountApp({})
    await openImportAsk(bar)

    expect(reachable(field('api-import-paste'))).toBe(true)
    // Present — a drop and the picker still land in it — and not something
    // the person is asked to fill in.
    expect(reachable(field('api-import-postman-file'))).toBe(false)
    expect(reachable(field('api-import-postman-dest'))).toBe(false)
  })

  it('proposes the collection name from a pasted export', async () => {
    const { bar } = await mountApp({})
    await openImportAsk(bar)

    paste('{"info":{"name":"Acme API"}}')

    await vi.waitFor(() => expect(destSummary()).toBe(`${DEFAULT_ROOT}/acme-api`))
    expect(sourceLine()).not.toBe('')
  })

  it('refuses a curl line here rather than spending a round trip on it', async () => {
    // curl has its own door in the request editor and is deliberately not
    // this ask's question: `parseImport` would hand it to the curl parser,
    // so a person who pasted a shell command would get a collection minted
    // from it, or an error mentioning curl in a dialog that never offered
    // curl.
    const importPostman = vi.fn()
    const { bar } = await mountApp({ importPostman })
    await openImportAsk(bar)

    paste('curl https://h -X POST')

    await vi.waitFor(() => expect(importAskBody().textContent).toMatch(/not a Postman export/i))
    expect(sourceLine()).toBe('')
    expect(destSummary()).toBe('')
    // And the line says so in words rather than standing empty: the pencil
    // beside it is the control that answers it.
    expect(importAskBody().textContent).toContain('Choose where this goes')
    expect(button('Import').disabled).toBe(true)
    expect(importPostman).not.toHaveBeenCalled()
  })

  it('sends the pasted document, and a second source replaces the first', async () => {
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({ importPostman })
    await openImportAsk(bar)

    // A path first, the way the picker and a native drop answer.
    fireEvent.input(field('api-import-postman-file'), { target: { value: '/w/acme.json' } })
    paste('{"info":{"name":"Acme"}}')
    fireEvent.click(button('Import'))

    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { document: '{"info":{"name":"Acme"}}' },
        `${DEFAULT_ROOT}/acme`,
      ),
    )
    // Exactly one source is held: the path is gone from the seam it landed
    // in, not merely outranked by the paste at submit time.
    expect(field('api-import-postman-file').value).toBe('')
  })

  it('a pasted URL is a source too, and proposes from its last segment', async () => {
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({ importPostman })
    await openImportAsk(bar)

    paste('https://example.test/exports/acme.postman_collection.json')

    await vi.waitFor(() => expect(destSummary()).toBe(`${DEFAULT_ROOT}/acme`))
    fireEvent.click(button('Import'))
    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { url: 'https://example.test/exports/acme.postman_collection.json' },
        `${DEFAULT_ROOT}/acme`,
      ),
    )
  })

  it('reveals the destination field behind the pencil, under its own id', async () => {
    const { bar } = await mountApp({ openDirectory: vi.fn().mockResolvedValue({ path: '/w' }) })
    await openImportAsk(bar)
    paste('{"info":{"name":"Acme"}}')

    await vi.waitFor(() => expect(destSummary()).toBe(`${DEFAULT_ROOT}/acme`))
    fireEvent.click(button('Change where this goes'))

    await vi.waitFor(() => expect(reachable(field('api-import-postman-dest'))).toBe(true))
    expect(field('api-import-postman-dest').value).toBe(`${DEFAULT_ROOT}/acme`)
    // The Browse control comes with it, in the field's trailing slot where
    // it has always been.
    expect(buttonNames()).toContain('Browse…')
  })

  it('forgetting the source empties the summary and disables Import', async () => {
    // A person who dropped the wrong file must be able to see what the ask
    // is holding and take it back.
    const { bar } = await mountApp({})
    await openImportAsk(bar)
    paste('{"info":{"name":"Acme"}}')
    await vi.waitFor(() => expect(button('Import').disabled).toBe(false))

    fireEvent.click(button('Forget this source'))

    await vi.waitFor(() => expect(sourceLine()).toBe(''))
    expect(destSummary()).toBe('')
    expect(button('Import').disabled).toBe(true)
    // And the box it was pasted into is empty, because the paste WAS the
    // source: a box still holding the text would go on offering a source
    // the ask no longer holds.
    expect(field('api-import-paste').value).toBe('')
  })

  it('the system picker fills the source line and proposes the folder', async () => {
    const openFile = vi.fn().mockResolvedValue({ path: '/downloads/acme.postman_collection.json' })
    const { bar } = await mountApp({ openFile })
    await openImportAsk(bar)

    fireEvent.click(button('Or select a file'))

    await vi.waitFor(() =>
      expect(sourceLine()).toContain('/downloads/acme.postman_collection.json'),
    )
    expect(destSummary()).toBe(`${DEFAULT_ROOT}/acme`)
  })

  it('a native drop fills the source line and proposes the folder', async () => {
    // The gesture the ask exists for on the desktop, and the one entrance
    // whose answer is a PATH nobody typed. It says what it is holding in the
    // same line a paste and a pick do — one statement, four entrances.
    const drop = nativeDropFixture()
    const { bar } = await mountApp({ nativeDrop: drop })
    await openImportAsk(bar)

    drop.emit({
      sessionId: DROP_SESSION,
      target: 'api-import',
      sources: [
        {
          sourceTicket: '',
          name: 'acme.postman_collection.json',
          size: 12,
          localPath: '/downloads/acme.postman_collection.json',
        },
      ],
    })

    await vi.waitFor(() =>
      expect(sourceLine()).toContain('/downloads/acme.postman_collection.json'),
    )
    expect(destSummary()).toBe(`${DEFAULT_ROOT}/acme`)
  })

  it('a destination the person edited is never overwritten by a later paste', async () => {
    const { bar } = await mountApp({})
    await openImportAsk(bar)

    fireEvent.click(button('Change where this goes'))
    await vi.waitFor(() => expect(reachable(field('api-import-postman-dest'))).toBe(true))
    fireEvent.input(field('api-import-postman-dest'), { target: { value: '/elsewhere/mine' } })

    paste('{"info":{"name":"Acme API"}}')

    await vi.waitFor(() => expect(sourceLine()).not.toBe(''))
    expect(field('api-import-postman-dest').value).toBe('/elsewhere/mine')
  })
})

// ── A URL SAYS WHICH CONNECTION IT TRAVELS THROUGH ────────────────────────
//
// nocx-zz3cy. A URL is the one source the BACKEND goes and gets, so it is
// the one source that has a route at all: a path is read where Go runs, and
// a document and a dropped file are already in hand. An export served inside
// a network the app can only reach through a bastion was therefore askable
// for and unfetchable, and the collection it minted would have carried
// `direct` into every request under it.
//
// The picker's grammar is environment-view's, deliberately: "Direct" plus
// one option per connection, the id as the value and the name as the label,
// in the store's order. A second grammar for one concept is the defect
// AGENTS.md names.

/** The connection picker, or null while the ask is not offering one. */
function routePicker(): HTMLSelectElement | null {
  const el = importAskBody().querySelector<HTMLSelectElement>('#api-import-route')
  return el === null || !reachable(el) ? null : el
}

/** What the picker offers, in the order it offers it. */
function routeOptions(): string[] {
  const el = routePicker()
  if (el === null) throw new Error('the ask is offering no route picker')
  return [...el.options].map((o) => o.label)
}

describe('a URL import says which connection it travels through', () => {
  it('reveals the picker for a URL and hides it again for a document', async () => {
    const { bar } = await mountApp({
      listConnections: vi.fn().mockResolvedValue([{ id: 'p1', name: 'prod-bastion' }]),
    })
    await openImportAsk(bar)

    paste('https://h/acme.json')
    await vi.waitFor(() => expect(routePicker()).not.toBeNull())

    // A pasted document is already in hand — nothing travels, so there is
    // nothing to ask about.
    paste('{"info":{"name":"A"}}')
    await vi.waitFor(() => expect(routePicker()).toBeNull())
  })

  it('offers Direct plus one option per connection, in the store order', async () => {
    const { bar } = await mountApp({
      listConnections: vi.fn().mockResolvedValue([
        { id: 'p1', name: 'prod-bastion' },
        { id: 'p2', name: 'staging' },
      ]),
    })
    await openImportAsk(bar)

    paste('https://h/acme.json')
    await vi.waitFor(() => expect(routeOptions()).toEqual(['Direct', 'prod-bastion', 'staging']))
    // The VALUE is the id the route stores, never the name a person reads.
    expect([...routePicker()!.options].map((o) => o.value)).toEqual(['', 'p1', 'p2'])
  })

  it('draws only Direct on a build with no profile store', async () => {
    // `listConnections` is absent from the fixture, which is what a build
    // with no profile store looks like: absence IS the capability, so the
    // picker is over nothing and offers the one answer there is.
    const { bar } = await mountApp({})
    await openImportAsk(bar)

    paste('https://h/acme.json')
    await vi.waitFor(() => expect(routePicker()).not.toBeNull())
    expect(routeOptions()).toEqual(['Direct'])
  })

  it('sends the chosen connection as the route', async () => {
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({
      importPostman,
      listConnections: vi.fn().mockResolvedValue([{ id: 'p1', name: 'prod-bastion' }]),
    })
    await openImportAsk(bar)

    paste('https://h/acme.json')
    await vi.waitFor(() => expect(routePicker()).not.toBeNull())
    fireEvent.change(routePicker()!, { target: { value: 'p1' } })
    fireEvent.click(button('Import'))

    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        {
          url: 'https://h/acme.json',
          route: { kind: 'connection', profileId: 'p1', insecureTls: false },
        },
        `${DEFAULT_ROOT}/acme`,
      ),
    )
  })

  it('sends no route key at all when the fetch goes direct', async () => {
    // Not `route: undefined`: the Go side decodes strictly, and an absent
    // route already reads as direct.
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({
      importPostman,
      listConnections: vi.fn().mockResolvedValue([{ id: 'p1', name: 'prod-bastion' }]),
    })
    await openImportAsk(bar)

    paste('https://h/acme.json')
    await vi.waitFor(() => expect(routePicker()).not.toBeNull())
    fireEvent.click(button('Import'))

    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { url: 'https://h/acme.json' },
        `${DEFAULT_ROOT}/acme`,
      ),
    )
    // The KEY SET, and not only the value: `toHaveBeenCalledWith` treats an
    // explicit `route: undefined` as equal to no route at all, and the Go
    // side's strict decoder does not.
    const [sent] = importPostman.mock.calls[0] as [Record<string, unknown>, string]
    expect(Object.keys(sent)).toEqual(['url'])
  })

  it('a connection chosen for a URL is not carried into a document import', async () => {
    const importPostman = vi.fn().mockResolvedValue({ unsupported: [] })
    const { bar } = await mountApp({
      importPostman,
      listConnections: vi.fn().mockResolvedValue([{ id: 'p1', name: 'prod-bastion' }]),
    })
    await openImportAsk(bar)

    paste('https://h/acme.json')
    await vi.waitFor(() => expect(routePicker()).not.toBeNull())
    fireEvent.change(routePicker()!, { target: { value: 'p1' } })
    paste('{"info":{"name":"Acme"}}')

    await vi.waitFor(() => expect(routePicker()).toBeNull())
    fireEvent.click(button('Import'))
    await vi.waitFor(() =>
      expect(importPostman).toHaveBeenCalledWith(
        { document: '{"info":{"name":"Acme"}}' },
        `${DEFAULT_ROOT}/acme`,
      ),
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
    fireEvent.click(button('Or select a file'))

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

    fireEvent.click(button('Or select a file'))

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

  it("the region's picker is the ask's ONLY picker, and it answers the whole gesture", async () => {
    // It used to be one of two — the export field carried the same handler
    // in its trailing slot — and the test asserted that both reached one
    // mock, because one derivation of "choose an export" was the point. The
    // field stopped being visible when the ask stopped asking for paths
    // (nocx-ysyy2), so the second control went with it rather than staying
    // on as a button nobody can reach. What is left to assert is the half
    // that still matters: a pick answers BOTH halves of the ask, exactly as
    // a drop does.
    const openFile = vi.fn().mockResolvedValue({ path: '/downloads/acme.postman_collection.json' })
    const { bar } = await mountApp({ openFile, nativeDrop: nativeDropFixture() })
    await openImportAsk(bar)

    expect(buttonNames()).not.toContain('Choose export…')
    fireEvent.click(button('Or select a file'))

    await vi.waitFor(() =>
      expect(field('api-import-postman-file').value).toBe(
        '/downloads/acme.postman_collection.json',
      ),
    )
    expect(openFile).toHaveBeenCalledTimes(1)
    expect(field('api-import-postman-dest').value).toBe(`${DEFAULT_ROOT}/acme`)
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

// ── Auth can create the secret it asks for ────────────────────────────────
//
// nocx-m4cyq. The tab asked for the NAME of a variable that had to already
// exist and already be bound somewhere else — so a person holding a token,
// which is the only reason anybody opens this tab, could do nothing here at
// all. Every test below starts where they start: a request with no
// credential, and a value in the clipboard.
//
// What each one is really asking is where the value ends up. It goes to the
// client's ONE credential-carrying call and to nowhere else — not into the
// request the client is asked to write, not into an environment, and not back
// onto the screen.

/** The worked example with nothing authenticating it. */
function unauthenticated(): ApiRequest {
  return { ...REQUEST, auth: { kind: 'none', token: '', password: '', user: '' } }
}

/** Open the worked example with no credential on it, on the Auth tab, under
 *  the environments given. Answers the three calls the assertions are about. */
async function openAuth(
  envs: ApiEnvironmentRef[] = [DEV_ENV],
  over: Partial<ApiWorkbenchServices> = {},
) {
  const bindSecret = vi.fn().mockResolvedValue({})
  const writeRequest = vi.fn().mockResolvedValue({})
  const writeEnvironment = vi.fn().mockResolvedValue({})
  const sendRequest = vi.fn().mockResolvedValue(sendFixture())
  const { bar } = await mountApp({
    listCollections: vi.fn().mockResolvedValue({
      collections: [collectionsFixture({ collection: collectionFixture({ environments: envs }) })],
      defaultRoot: DEFAULT_ROOT,
    }),
    readRequest: vi.fn().mockResolvedValue({ request: unauthenticated() }),
    bindSecret,
    writeRequest,
    writeEnvironment,
    sendRequest,
    ...over,
  })
  await openRequest(bar)
  fireEvent.click(button('Auth'))
  return { bindSecret, writeRequest, writeEnvironment, sendRequest }
}

/** The field a value is pasted into, when the tab offers one. */
const authSecretField = (): HTMLInputElement | null =>
  workbench().querySelector<HTMLInputElement>('#api-auth-secret-value')

describe('a credential can be created from the Auth tab', () => {
  it('the value is given here, the product proposes the name, and the request then sends', async () => {
    const { bindSecret, writeRequest, writeEnvironment, sendRequest } = await openAuth()

    // The scheme, chosen the way a person chooses it. Nothing is named yet:
    // what the product WOULD call it is the field's placeholder, so the
    // proposal is on screen before anything is pressed.
    fireEvent.change(control('auth-kind'), { target: { value: 'bearer' } })
    await vi.waitFor(() => expect(authSecretField()).not.toBeNull())
    expect(field('api-auth-var').value).toBe('')
    expect(field('api-auth-var').getAttribute('placeholder')).toBe('token')
    expect(document.querySelector('label[for="api-auth-var"]')?.textContent).toBe('Token')

    fireEvent.input(authSecretField()!, { target: { value: SECRET_VALUE } })
    fireEvent.click(button('Store'))

    // ONE call carries it, and it is the same one the environments page
    // makes: the collection, the environment, the NAME, the value.
    await vi.waitFor(() =>
      expect(bindSecret).toHaveBeenCalledWith(HANDLE, DEV_ENV.relPath, 'token', SECRET_VALUE),
    )
    expect(bindSecret).toHaveBeenCalledTimes(1)

    // The REFERENCE is what lands in the form — the field carries `{{token}}`
    // text, resolved by the same substitution as the URL — and the value is
    // not on screen anywhere afterwards.
    await vi.waitFor(() => expect(field('api-auth-var').value).toBe('{{token}}'))
    expect(authSecretField()!.value).toBe('')
    expect(workbench().innerHTML).not.toContain(SECRET_VALUE)

    // AND THAT IS THE WHOLE OF IT: Send, with nothing else asked of anybody.
    fireEvent.click(button('Send'))
    await vi.waitFor(() =>
      expect(sendRequest).toHaveBeenCalledWith(
        HANDLE,
        CREATE_REL_PATH,
        DEV_ENV.relPath,
        expect.any(String),
      ),
    )

    // What the client was ASKED TO WRITE — the assertion that no screenshot
    // could make. The request file carries the reference and no byte of the
    // value, and no environment file was rewritten at all: a binding is not
    // a row.
    const calls = writeRequest.mock.calls
    const written = calls[calls.length - 1][2] as ApiRequest
    expect(written.auth).toEqual({ kind: 'bearer', token: '{{token}}', password: '', user: '' })
    expect(JSON.stringify(written)).not.toContain(SECRET_VALUE)
    expect(writeEnvironment).not.toHaveBeenCalled()
  })
  it('a name already referenced is the name the value is bound under', async () => {
    // Creating is an ADDITION to the tab: a variable somebody already bound
    // elsewhere is still referenced by writing `{{name}}` into the field —
    // that is the name's spelling now, since the field is text — and a
    // value created here is stored under that name rather than under the
    // scheme's proposal.
    const { bindSecret } = await openAuth()
    fireEvent.change(control('auth-kind'), { target: { value: 'bearer' } })
    await vi.waitFor(() => expect(authSecretField()).not.toBeNull())

    fireEvent.input(field('api-auth-var'), { target: { value: '{{API_TOKEN}}' } })
    fireEvent.input(authSecretField()!, { target: { value: SECRET_VALUE } })
    fireEvent.click(button('Store'))

    await vi.waitFor(() =>
      expect(bindSecret).toHaveBeenCalledWith(HANDLE, DEV_ENV.relPath, 'API_TOKEN', SECRET_VALUE),
    )
    expect(field('api-auth-var').value).toBe('{{API_TOKEN}}')
  })

  it('a refusal keeps the pasted value and changes nothing on the request', async () => {
    // The one state this door exists to get somebody OUT of is a file naming
    // a variable nothing answers, so a write that did not land must not leave
    // the name behind — and it must not cost the person the value either.
    const bindSecret = vi.fn().mockRejectedValue(new RpcError('the vault is sealed', -32000))
    await openAuth([DEV_ENV], { bindSecret })
    fireEvent.change(control('auth-kind'), { target: { value: 'bearer' } })
    await vi.waitFor(() => expect(authSecretField()).not.toBeNull())

    fireEvent.input(authSecretField()!, { target: { value: SECRET_VALUE } })
    fireEvent.click(button('Store'))

    await vi.waitFor(() => expect(bindSecret).toHaveBeenCalled())
    await vi.waitFor(() => expect(workbench().textContent).toContain('the vault is sealed'))
    expect(authSecretField()!.value).toBe(SECRET_VALUE)
    expect(field('api-auth-var').value).toBe('')
  })

  // ABSENCE IS THE CAPABILITY, and the two absences are different problems.
  it('with no environment chosen there is nothing to type a value into, and it says which absence it is', async () => {
    // Two environments and nothing chosen is "No environment" — a row a
    // person can pick, so this is an ordinary state and not a broken one.
    await openAuth([DEV_ENV, PROD_ENV])
    fireEvent.change(control('auth-kind'), { target: { value: 'bearer' } })

    await vi.waitFor(() => expect(field('api-auth-var').value).toBe(''))
    expect(chosenEnvironment()).toBe('No environment')
    expect(authSecretField()).toBeNull()
    expect(buttonNames()).not.toContain('Store')
    expect(workbench().textContent).toContain('Choose an environment')

    // And choosing one is all it takes.
    fireEvent.click(environmentRow(PROD_ENV.name)!)
    await vi.waitFor(() => expect(authSecretField()).not.toBeNull())
    expect(workbench().textContent).toContain(PROD_ENV.name)
  })

  it('a request with no file behind it says so instead of offering a control that cannot work', async () => {
    // A converted curl line lands in the form with nothing on disk behind it
    // — there is no collection and no environment for a binding to belong
    // to, and that is a different sentence from the one above. It takes NO
    // collection open to reach that state now: with one, the import writes
    // the file as it converts.
    const { bar } = await mountApp({
      ...noCollections(),
      importCurl: vi.fn().mockResolvedValue({ request: unauthenticated(), unsupported: [] }),
    })
    await openWorkbench(bar)
    fireEvent.click(button('Import a curl command'))
    fireEvent.input(field('api-import-curl'), { target: { value: 'curl https://h/x' } })
    fireEvent.click(button('Convert to a request'))
    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))

    fireEvent.click(button('Auth'))
    fireEvent.change(control('auth-kind'), { target: { value: 'bearer' } })

    await vi.waitFor(() => expect(field('api-auth-var').value).toBe(''))
    expect(authSecretField()).toBeNull()
    expect(workbench().textContent).toContain('Open a collection to store a value for it')
  })

  it('the tab says the reference once, not twice', async () => {
    // nocx-qoavg: under the field sat `Sends 🔒name`, which is the field's own
    // contents read back. The chip's job is in the RUN view, where it stands
    // where a credential's bytes were.
    const { bar } = await mountApp()
    await openRequest(bar)
    fireEvent.click(button('Auth •'))
    const panel = workbench().querySelector<HTMLElement>('#ui-tabpanel-auth')
    if (!panel) throw new Error('no auth panel')
    expect(panel.querySelectorAll('.ui-secret-chip')).toHaveLength(0)
    // The input's VALUE is not textContent, so the panel text must carry no
    // copy of the reference at all — the "once, not twice" claim: field
    // plus no second rendering.
    expect(panel.textContent?.match(/{{API_TOKEN}}/g) ?? []).toHaveLength(0)
  })
})

describe('a token pasted into the Auth tab is sent as the literal it is', () => {
  it('the value the person typed is the value the file records and Send is reached', async () => {
    // nocx-6hg2w.20, from the FORM: a literal pasted into the bearer field
    // is text like every other field — it is written to the file as-is, is
    // sent (the backend substitutes nothing for it), and nothing rewrites
    // or refuses it. The web of assertions below pins the whole route from
    // the field the person touches to the request that goes out.
    const literal = '88730fee-9a4c-4c9d-8f4c-a1b2c3d4e5f6'
    const sendRequest = vi.fn().mockResolvedValue(sendFixture())
    const writeRequest = vi.fn().mockResolvedValue({})
    const { bar } = await mountApp({ sendRequest, writeRequest })
    await openRequest(bar)

    fireEvent.click(button('Auth •'))
    fireEvent.change(control('auth-kind'), { target: { value: 'bearer' } })
    fireEvent.input(field('api-auth-var'), { target: { value: literal } })

    fireEvent.click(button('Send'))
    await vi.waitFor(() => expect(sendRequest).toHaveBeenCalled())

    // The value is on the wire: what the client was asked to write carries
    // the literal verbatim, and the send reach is reached.
    const calls = writeRequest.mock.calls
    const written = calls[calls.length - 1][2] as ApiRequest
    expect(written.auth).toEqual({ kind: 'bearer', token: literal, password: '', user: '' })
    expect(JSON.stringify(written)).toContain(literal)
    // And it went out as ITSELF: nothing here renamed it `{{token}}` or
    // decided it was a variable nobody bound. That is the whole of the
    // decision — the product does not hide or move a credential a person
    // typed.
    expect(sendRequest).toHaveBeenCalledWith(
      HANDLE,
      expect.any(String),
      expect.any(String),
      expect.any(String),
    )
  })
})

// ── The pane's lifecycle ──────────────────────────────────────────────────
//
// One test each for what SolidPaneContent exists to make correct and what a
// bare Solid component would have got wrong silently.

function paneHostFake(over: Partial<PaneHost> = {}): PaneHost {
  return {
    setTitle: vi.fn(),
    updateTooltip: vi.fn(),
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

describe('the request Variables tab explains the effective scope', () => {
  it('shows ordered inherited rows, sources, winners and redacted vault names', async () => {
    const { bar } = await mountApp({
      requestScope: vi.fn().mockResolvedValue({
        variables: [
          {
            name: 'id',
            value: 'request',
            scope: 'request',
            from: '',
            overridden: false,
            refused: '',
          },
          {
            name: 'id',
            value: 'users',
            scope: 'folder',
            from: 'users',
            overridden: true,
            refused: '',
          },
          {
            name: 'baseUrl',
            value: 'https://api.example.test',
            scope: 'environment',
            from: 'environments/dev.json',
            overridden: false,
            refused: '',
          },
          { name: 'token', value: '', scope: 'vault', from: '', overridden: false, refused: '' },
        ],
      }),
    })
    await openRequest(bar)
    fireEvent.click(button('Variables'))

    await vi.waitFor(() => {
      expect(workbench().textContent).toContain('users')
      expect(workbench().textContent).toContain('Overridden')
      expect(workbench().textContent).toContain('https://api.example.test')
      expect(workbench().textContent).toContain('Bound in the vault')
    })
    expect(workbench().textContent).not.toContain('request-scope-secret-value')
    expect(
      workbench().querySelector('table[aria-label="Inherited request variables"] button'),
    ).toBeNull()
  })
  it('shows a scope refusal and the empty state without exposing a value', async () => {
    const { bar } = await mountApp({
      requestScope: vi.fn().mockResolvedValue({
        variables: [
          {
            name: 'token',
            value: '',
            scope: 'environment',
            from: 'environments/dev.json',
            overridden: false,
            refused: 'api: a request variable would shadow a name this environment declares secret',
          },
        ],
      }),
    })
    await openRequest(bar)
    fireEvent.click(button('Variables'))

    await vi.waitFor(() => {
      expect(workbench().querySelector('[data-api-scope-refusal]')?.textContent).toContain('token')
      expect(workbench().textContent).toContain("No variables in this request's effective scope.")
    })
    expect(workbench().textContent).not.toContain('request-scope-secret-value')
  })

  it('sends every draft variable change to the backend scope resolver', async () => {
    const requestScope = vi
      .fn()
      .mockImplementation(
        (
          _handle: string,
          _relPath: string,
          _envRelPath: string,
          variables: ApiRequest['variables'],
        ) => ({
          variables: [
            {
              name: 'id',
              value: 'users',
              scope: 'folder',
              from: 'users',
              overridden: variables.some((variable) => variable.enabled && variable.name === 'id'),
              refused: '',
            },
          ],
        }),
      )
    const { bar } = await mountApp({ requestScope })
    await openRequest(bar)
    fireEvent.click(button('Variables'))
    await vi.waitFor(() => expect(workbench().textContent).toContain('users'))

    fireEvent.click(button('Add variable'))
    const nameInputs = () =>
      [...workbench().querySelectorAll<HTMLInputElement>('input')].filter(
        (input) => input.id.startsWith('api-variable-name-') && reachable(input),
      )
    await vi.waitFor(() => expect(nameInputs()).toHaveLength(1))
    fireEvent.input(nameInputs()[0], { target: { value: 'id' } })

    await vi.waitFor(() => {
      const inherited = workbench().querySelector('table[aria-label="Inherited request variables"]')
      expect(inherited?.textContent).toContain('Overridden')
    })
    expect(requestScope.mock.calls[requestScope.mock.calls.length - 1]?.[3]).toEqual([
      { name: 'id', value: '', enabled: true },
    ])
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
    requestScope: vi
      .fn()
      .mockImplementation(
        (
          _handle: string,
          _relPath: string,
          _envRelPath: string,
          requestVariables: ApiRequest['variables'],
        ): Promise<ApiRequestScopeResult> =>
          Promise.resolve({
            variables: [
              ...requestVariables.map((variable) => ({
                name: variable.name,
                value: variable.value,
                scope: 'request' as const,
                from: '',
                overridden: false,
                refused: '',
              })),
              ...Object.entries(values).map(([name, value]) => ({
                name,
                value,
                scope: 'environment' as const,
                from: DEV_ENV.relPath,
                overridden: false,
                refused: '',
              })),
            ],
          }),
      ),
  })
  it("a folder's variable binds the address and offers that folder to edit", async () => {
    const folderScope: ApiRequestScopeResult = {
      variables: [
        {
          name: 'baseUrl',
          value: 'https://folder.example.test',
          scope: 'folder',
          from: 'users',
          overridden: false,
          refused: '',
        },
      ],
    }
    const { bar } = await mountApp({
      requestScope: vi.fn().mockResolvedValue(folderScope),
    })
    await openRequest(bar)

    await vi.waitFor(() => expect(marks()[0]?.dataset.tone).toBe('reference'))
    fireEvent.click(marks()[0])
    await vi.waitFor(() => expect(variableMenu()).toBeTruthy())

    expect(variableMenu()?.textContent).toContain('https://folder.example.test')
    expect(variableMenu()?.textContent).toContain('from folder users')
    expect(
      [...(variableMenu()?.querySelectorAll('button') ?? [])].some((button) =>
        (button.textContent ?? '').includes('Edit folder users'),
      ),
    ).toBe(true)
    expect(
      [...(variableMenu()?.querySelectorAll('button') ?? [])].some((button) =>
        (button.textContent ?? '').includes('Add baseUrl'),
      ),
    ).toBe(false)
  })

  it('does not paint an unanswered address while the scope answer is in flight', async () => {
    let resolveScope!: (result: ApiRequestScopeResult) => void
    const requestScope = vi.fn().mockImplementation(
      () =>
        new Promise<ApiRequestScopeResult>((resolve) => {
          resolveScope = resolve
        }),
    )
    const { bar } = await mountApp({ requestScope })
    const opening = openRequest(bar)

    await vi.waitFor(() => expect(requestScope).toHaveBeenCalled())
    expect(marks()[0]?.dataset.tone).toBe('reference')

    resolveScope({
      variables: [
        {
          name: 'baseUrl',
          value: 'https://folder.example.test',
          scope: 'folder',
          from: 'users',
          overridden: false,
          refused: '',
        },
      ],
    })
    await opening
    await vi.waitFor(() => expect(marks()[0]?.dataset.tone).toBe('reference'))
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
      requestScope: vi.fn().mockResolvedValue({
        variables: [
          {
            name: 'baseUrl',
            value: '',
            scope: 'vault',
            from: '',
            overridden: false,
            refused: '',
          },
        ],
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

// ── The doors a request was missing (nocx-c8ozb, nocx-lpo2m, nocx-bp44a) ──
//
// Making a request meant aiming at a collection row and hitting a plus that
// is only there while the pointer is over it; naming one meant renaming
// `Untitled request` by hand, so a tree filled with rows nobody could tell
// apart; and copying one meant retyping it. These three describe the doors,
// and each drives the seam a person reaches — the control, from the state
// they start in, and what is on screen afterwards.

/** The name the header's crumb trail is showing, or '' when it shows none. */
function crumbName(): string {
  const el = workbench().querySelector<HTMLElement>('.api-crumbs__name')
  return (el?.textContent ?? '').trim()
}

/** Every dialog that is OPEN — the assertion behind "with no dialog". */
function openDialogs(): HTMLDialogElement[] {
  return [...workbench().querySelectorAll('dialog')].filter((d) => d.open)
}

describe('a new request has a door that does not move', () => {
  it('the control in the header makes one WHERE THE PERSON IS and puts it in the form', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)

    // The trail above the control says which folder this is, and the file
    // has to land in the one it names.
    expect(workbench().querySelector('.api-crumbs__folder')?.textContent).toBe('users')
    fireEvent.click(button('New request'))

    // Written into the collection the workbench is pointed at, in the folder
    // the open request lives in, under a path the allocator chose — nothing
    // was asked. It went to the collection's ROOT before (nocx-8aczn.6): a
    // control that contradicted the line it sits in, and a request a person
    // then had to move by hand into the folder they were already in.
    await vi.waitFor(() =>
      expect(disk.writeRequest).toHaveBeenCalledWith(
        HANDLE,
        'users/untitled-request.json',
        expect.objectContaining({ name: 'Untitled request', method: 'GET', url: '' }),
      ),
    )
    // And it is what the form is showing, read back off the file.
    await vi.waitFor(() => expect(crumbName()).toBe('Untitled request'))
    // The URL field follows too. The pane leaves the caret in it
    // (api-content.ts) and on macOS a button click does not take it away, so
    // this is the state a person is actually in when they press the control.
    await vi.waitFor(() => expect(field('api-url').value).toBe(''))
    expect(openDialogs()).toEqual([])
  })

  it("and a person's own typing is still theirs — the caret keeps the field", async () => {
    // The other end of the interval the door moved. The field owns its text
    // while it has the caret AND is showing the same request: type the `?`
    // of a query by hand and the model cannot represent it yet, so a form
    // that pushed the derived address back would erase the character as it
    // was typed.
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)

    field('api-url').focus()
    fireEvent.input(field('api-url'), { target: { value: '{{baseUrl}}/users?' } })

    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users?'))
  })

  it('it is there before any request is open — an empty collection is where it is needed', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    // Nothing is in the form yet, and the door is still there.
    expect(crumbName()).toBe('')
    fireEvent.click(button('New request'))
    await vi.waitFor(() => expect(crumbName()).toBe('Untitled request'))
    // With no request open there is no folder a person is in, so the
    // collection's root is the whole answer.
    expect(disk.writeRequest).toHaveBeenCalledWith(
      HANDLE,
      'untitled-request.json',
      expect.objectContaining({ name: 'Untitled request' }),
    )
  })

  it('with no collection open the control is ABSENT, not present and refusing', async () => {
    const { bar } = await mountApp(noCollections())
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('No collections open'))

    expect(buttonNames()).not.toContain('New request')
  })

  it('the existing doors stay — the row is how a person makes one somewhere else', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    // The row's own plus, which names the collection it acts on.
    expect(buttonNames()).toContain('New request in acme-api')
    // And the row's menu still offers it.
    fireEvent.click(button('More actions for acme-api'))
    await vi.waitFor(() =>
      expect(document.querySelector('[data-testid="api-folder-row-menu"]')).toBeTruthy(),
    )
    const item = [
      ...(document
        .querySelector('[data-testid="api-folder-row-menu"]')
        ?.querySelectorAll('button') ?? []),
    ].find((b) => (b.textContent ?? '').trim() === 'New request')
    expect(item, 'the row menu still offers New request').toBeTruthy()
  })
})

describe('a request names itself from what you typed', () => {
  /** Rename the open request the way a person does: the name in the crumb
   *  trail is a control, and pressing it puts a field in its place. */
  function rename(to: string): void {
    fireEvent.click(button(crumbName()))
    const nameField = field('api-request-name')
    fireEvent.input(nameField, { target: { value: to } })
    fireEvent.blur(nameField)
  }

  /** Make one through the header's door and wait for it to be in the form. */
  async function newRequest(): Promise<void> {
    fireEvent.click(button('New request'))
    await vi.waitFor(() => expect(crumbName()).toBe('Untitled request'))
  }

  it('a request nobody has named takes its name as the URL is typed', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)
    await newRequest()

    fireEvent.input(field('api-url'), {
      target: { value: 'http://127.0.0.1:8080/v1/broker-access' },
    })

    await vi.waitFor(() => expect(crumbName()).toBe('GET broker-access'))
    // The verb is half the name, so changing it changes the name too —
    // while nobody has taken it over.
    fireEvent.change(control('method'), { target: { value: 'POST' } })
    await vi.waitFor(() => expect(crumbName()).toBe('POST broker-access'))
  })

  it('a request the person HAS named is never renamed by this, whatever the URL does', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)
    await newRequest()

    fireEvent.input(field('api-url'), { target: { value: 'https://h/v1/broker-access' } })
    await vi.waitFor(() => expect(crumbName()).toBe('GET broker-access'))

    rename('Broker access, live')
    await vi.waitFor(() => expect(crumbName()).toBe('Broker access, live'))

    // Not when the URL changes, and not ever.
    fireEvent.input(field('api-url'), { target: { value: 'https://h/v2/tenants' } })
    fireEvent.change(control('method'), { target: { value: 'DELETE' } })
    await vi.waitFor(() => expect(field('api-url').value).toBe('https://h/v2/tenants'))
    expect(crumbName()).toBe('Broker access, live')
  })

  it('an address with nothing to take a name from leaves the request named as it is', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)
    await newRequest()

    // A host is not a path segment: the offer is absent rather than wrong.
    fireEvent.input(field('api-url'), { target: { value: 'http://127.0.0.1:8080' } })
    await vi.waitFor(() => expect(field('api-url').value).toBe('http://127.0.0.1:8080'))
    expect(crumbName()).toBe('Untitled request')
  })

  it('a request that came off disk under its own name keeps it', async () => {
    // The offer is for a request nobody has named. `create` is a name
    // somebody gave — the file carries it — so typing an address must not
    // take it away, and reopening the request must not start the offer over.
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)

    fireEvent.input(field('api-url'), { target: { value: 'https://h/v1/broker-access' } })
    await vi.waitFor(() => expect(field('api-url').value).toBe('https://h/v1/broker-access'))
    expect(crumbName()).toBe('create')
  })
})

describe('a request can be duplicated', () => {
  it('the copy is beside the original, named apart from it, and in the form', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Duplicate')

    // BESIDE THE ORIGINAL: the same folder inside the collection, under a
    // path the allocator chose — a copy of `users/create.json` at the
    // collection's root is not beside anything.
    await vi.waitFor(() =>
      expect(disk.writeRequest).toHaveBeenCalledWith(
        HANDLE,
        'users/create-copy.json',
        expect.objectContaining({ name: 'create copy', id: 'users/create-copy' }),
      ),
    )
    // It is a FILE like any other: the row is there because the collection
    // listing has it, which is the route a colleague's git pull takes.
    await vi.waitFor(() => row('users/create-copy.json'))
    // And it is the request in the form, so the change they came to make is
    // the next thing they do.
    await vi.waitFor(() => expect(crumbName()).toBe('create copy'))
  })

  it('the copy carries everything, including the auth VARIABLE and no value', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Duplicate')
    await vi.waitFor(() => expect(disk.files.has('users/create-copy.json')).toBe(true))

    const copy = disk.files.get('users/create-copy.json') as ApiRequest
    expect(copy.method).toBe(REQUEST.method)
    expect(copy.url).toBe(REQUEST.url)
    expect(copy.headers).toEqual(REQUEST.headers)
    expect(copy.query).toEqual(REQUEST.query)
    expect(copy.variables).toEqual(REQUEST.variables)
    expect(copy.body).toEqual(REQUEST.body)
    // The auth VARIABLE NAME travels; there is nowhere in the file a value
    // could be spelled, and the copy must not become the place (design §8).
    expect(copy.auth).toEqual(REQUEST.auth)
    expect(JSON.stringify(copy)).not.toContain(SECRET_VALUE)
    // Two things do not travel, and both are the file's own identity: the
    // path it lives at and the id minted from it.
    expect(copy.id).not.toBe(REQUEST.id)
  })

  it('duplicating twice gives two copies — not a collision and not an overwrite', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Duplicate')
    await vi.waitFor(() => row('users/create-copy.json'))
    await pickOnRow(CREATE_REL_PATH, 'Duplicate')
    await vi.waitFor(() => row('users/create-copy-2.json'))

    // Three requests where there was one, and the first copy still holds
    // what was written into it.
    expect([...disk.files.keys()]).toEqual([
      CREATE_REL_PATH,
      'users/create-copy.json',
      'users/create-copy-2.json',
    ])
    // Told apart in the tree, which is the whole point of copying one.
    expect(crumbName()).toBe('create copy 2')
  })

  it('copies what is in the FORM when the form is showing that request', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))

    // An edit that has NOT been saved — which is the state a person is in
    // when they think "this is how I want it, now give me another one".
    fireEvent.input(field('api-url'), { target: { value: '{{baseUrl}}/users/search' } })
    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users/search'))

    await pickOnRow(CREATE_REL_PATH, 'Duplicate')
    await vi.waitFor(() => expect(disk.files.has('users/create-copy.json')).toBe(true))

    // What they were looking at is what they got.
    expect((disk.files.get('users/create-copy.json') as ApiRequest).url).toBe(
      '{{baseUrl}}/users/search',
    )
    // And duplicating is not a save: the original's file is what it was.
    expect((disk.files.get(CREATE_REL_PATH) as ApiRequest).url).toBe(REQUEST.url)
  })

  it('copies the FILE for a row the form is not showing', async () => {
    const disk = folderOnDisk()
    disk.files.set('users/archive.json', { ...REQUEST, id: 'users/archive', name: 'archive' })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('users/archive.json'))
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))
    fireEvent.input(field('api-url'), { target: { value: '{{baseUrl}}/nowhere' } })

    // The row aimed at has no draft anywhere — the open request's edits are
    // not it, and copying them onto another request's copy is the defect the
    // pairing above exists to make impossible.
    await pickOnRow('users/archive.json', 'Duplicate')

    await vi.waitFor(() => expect(disk.files.has('users/archive-copy.json')).toBe(true))
    expect((disk.files.get('users/archive-copy.json') as ApiRequest).url).toBe(REQUEST.url)
  })

  it('a copy that could not be written says so and puts no row in the tree', async () => {
    const disk = folderOnDisk({
      writeRequest: vi.fn().mockRejectedValue(new Error('read-only file system')),
    })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Duplicate')

    await vi.waitFor(() => expect(workbench().textContent).toContain('read-only file system'))
    expect(workbench().querySelector('[data-rel-path="users/create-copy.json"]')).toBeNull()
  })
})

// ── The right button, and one list behind two doors (nocx-rmjj8) ──────────
//
// Duplicate arrived as an IconButton drawn on every request row and the owner
// stopped at it on sight. The row is a NAME in a narrow list; an always-drawn
// control on it competes with the one thing the row is for. Meanwhile the
// right button — which the kit's own ContextMenu docstring names as its
// purpose, and which the Files panel and the tab strip both wire — did
// nothing here at all, so right-clicking a request handed over the webview's
// menu: reload, save image as.

describe("a request's actions are behind the right button", () => {
  /** The same collection with a second request in it, because the defect this
   *  is about only exists when the row aimed at is not the one in the form. */
  function twoRequests(over: Partial<ApiWorkbenchServices> = {}) {
    const disk = folderOnDisk(over)
    disk.files.set('users/archive.json', { ...REQUEST, id: 'users/archive', name: 'archive' })
    return disk
  }

  it('offers the actions at the pointer, and the webview keeps its own menu to itself', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    const e = rightClick(row(CREATE_REL_PATH))

    // The platform's menu is what a person got before, over a request.
    expect(e.defaultPrevented).toBe(true)
    await vi.waitFor(() => menuItem('Duplicate'))
    expect(menuItem('Delete request…')).toBeTruthy()
  })

  it('acts on the row that was aimed at, not on the request that happens to be open', async () => {
    const remove = vi.fn().mockResolvedValue({})
    const disk = twoRequests({ deleteRequest: remove })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('users/archive.json'))

    // One request in the form, and the right button aimed at the OTHER one.
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(crumbName()).toBe('create'))
    await pickOnRow('users/archive.json', 'Delete request…')

    // The question names the file that goes. Reading the OPEN request here —
    // which is what the header's door used to hand in — would name one file
    // and remove another.
    await vi.waitFor(() => expect(confirmText()).toContain('archive'))
    expect(confirmText()).not.toContain('Delete create?')

    fireEvent.click(
      [...document.querySelectorAll<HTMLButtonElement>('dialog button')].find(
        (b) => (b.textContent ?? '').trim() === 'Delete',
      ) as HTMLButtonElement,
    )
    await vi.waitFor(() => expect(remove).toHaveBeenCalledWith(HANDLE, 'users/archive.json'))
  })

  it('duplicates the row that was aimed at, leaving the open request alone', async () => {
    const disk = twoRequests()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('users/archive.json'))

    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(crumbName()).toBe('create'))
    await pickOnRow('users/archive.json', 'Duplicate')

    await vi.waitFor(() => expect(disk.files.has('users/archive-copy.json')).toBe(true))
    expect(disk.files.has('users/create-copy.json')).toBe(false)
  })

  it("the header's ⋮ offers the same list, about the request in the form", async () => {
    const disk = twoRequests()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(crumbName()).toBe('create'))

    fireEvent.click(button('More actions for this request'))

    // Same words as the row's, because it is the same list — the ⋮ held
    // Delete alone while the only other act a request had was drawn on a
    // different line entirely.
    await vi.waitFor(() => menuItem('Duplicate'))
    expect(menuItem('Delete request…')).toBeTruthy()
    fireEvent.click(menuItem('Duplicate'))
    await vi.waitFor(() => expect(disk.files.has('users/create-copy.json')).toBe(true))
  })

  it('a collection row answers the right button with the menu its ⋮ opens', async () => {
    const { bar } = await mountApp()
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    const collection = workbench().querySelector<HTMLElement>(`[data-row-key="${HANDLE}:"]`)
    if (!collection) throw new Error('no collection row')
    const e = rightClick(collection)

    expect(e.defaultPrevented).toBe(true)
    await vi.waitFor(() => menuItem('New request'))
    expect(menuItem('Close collection')).toBeTruthy()
  })
})

// ── The tree says which request is open (nocx-aug1m) ──────────────────────

describe('the tree says which request is open', () => {
  const marked = (): string[] =>
    [...workbench().querySelectorAll<HTMLElement>('[data-rel-path] [data-selected="true"]')].map(
      (el) => el.closest('[data-rel-path]')?.getAttribute('data-rel-path') ?? '',
    )

  it('marks the open request, and only it, and moves the mark', async () => {
    const disk = folderOnDisk()
    disk.files.set('users/archive.json', { ...REQUEST, id: 'users/archive', name: 'archive' })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('users/archive.json'))

    // Nothing is open, so nothing is marked — the header names no request
    // either, and the two have to agree.
    expect(marked()).toEqual([])

    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(marked()).toEqual([CREATE_REL_PATH]))

    fireEvent.click(row('users/archive.json'))
    await vi.waitFor(() => expect(marked()).toEqual(['users/archive.json']))
  })

  it('an import moves the mark onto the file it just made, never leaves it behind', async () => {
    const disk = folderOnDisk({
      importCurl: vi
        .fn()
        .mockResolvedValue({ request: { ...REQUEST, id: '', name: 'ping' }, unsupported: [] }),
    })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(marked()).toEqual([CREATE_REL_PATH]))

    // The import writes the converted line into a file of its own and opens
    // THAT. The mark must follow the form; what it must never do is stay on
    // the row the form has stopped showing, which is the half a mark driven
    // by anything other than `store.selected()` would get wrong.
    fireEvent.click(button('Import a curl command'))
    fireEvent.input(field('api-import-curl'), { target: { value: 'curl https://h/v1/ping' } })
    fireEvent.click(button('Convert to a request'))

    await vi.waitFor(() => expect(crumbName()).toBe('ping'))
    await vi.waitFor(() => expect(marked()).not.toEqual([CREATE_REL_PATH]))
    expect(marked()).toHaveLength(1)
    expect(marked()[0]).toContain('ping')
  })
})

// ── A collection can be given a folder from the tree (nocx-8v1fu) ─────────
//
// The half of design §6.2 the product could not reach. A collection is a
// folder and it may contain folders; the Postman importer writes them, so an
// imported collection arrived with structure and one built inside nocx could
// never have any — there was no folder-creation door anywhere on the surface.
//
// Every check below drives the seam a person reaches: the control is found by
// its words, activated from the state a person starts in, and what appears
// afterwards is the assertion. Nothing calls the store.

describe('a collection can be given a folder', () => {
  /** A folder or collection row, by the path within the collection — '' is
   *  the collection's own row. Requests are addressed by `data-rel-path`;
   *  every row carries `data-row-key`, which is the handle and the path. */
  function treeRow(relPath: string, handle: string = HANDLE): HTMLElement {
    const el = workbench().querySelector<HTMLElement>(`[data-row-key="${handle}:${relPath}"]`)
    if (!el) throw new Error(`no tree row for ${relPath || '(the collection)'}`)
    return el
  }

  /** Right-click a folder row and pick one of its acts. */
  async function pickOnFolder(relPath: string, label: string): Promise<void> {
    rightClick(treeRow(relPath))
    await vi.waitFor(() => menuItem(label))
    fireEvent.click(menuItem(label))
  }

  /** Answer the folder ask the way a person does. */
  function answerWith(name: string): void {
    const field = document.querySelector<HTMLInputElement>('#api-new-folder-name')
    if (!field) throw new Error('the folder ask is not on screen')
    fireEvent.input(field, { target: { value: name } })
    fireEvent.click(button('Create folder'))
  }

  /** The reason under the folder ask's field, or '' when it is clean. */
  function folderRefusal(): string {
    return (
      document.querySelector<HTMLElement>('#api-new-folder-name')?.closest('.ui-field')
        ?.textContent ?? ''
    )
  }

  /** A backend whose listing carries the folders it is given. */
  function withFolders(folders: string[], over: Partial<ApiWorkbenchServices> = {}) {
    return {
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({ collection: collectionFixture({ requests: [], folders }) }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
      ...over,
    }
  }

  it('a folder with nothing in it is a row — the state a folder spends its first minutes in', async () => {
    const { bar } = await mountApp(withFolders(['reports']))
    await openWorkbench(bar)
    await vi.waitFor(() => treeRow('reports'))
  })

  it('makes one inside the open collection, and it is in the tree afterwards', async () => {
    const createFolder = vi.fn().mockResolvedValue(folderCreatedFixture('reports'))
    const { bar } = await mountApp({ createFolder })
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    // The door a person starts at: the collection's own row, whose menu is
    // reachable by the button beside it as well as by the right button.
    fireEvent.click(button('More actions for acme-api'))
    await vi.waitFor(() => menuItem('New folder…'))
    fireEvent.click(menuItem('New folder…'))

    await vi.waitFor(() => expect(document.querySelector('#api-new-folder-name')).toBeTruthy())
    answerWith('reports')

    // A NAME and the collection's ROOT — never a path, and never a root on
    // the wire (§13.1).
    await vi.waitFor(() => expect(createFolder).toHaveBeenCalledWith(HANDLE, '', 'reports'))
    // And the row is there, drawn from the collection the call answered.
    await vi.waitFor(() => treeRow('reports'))
    expect(toasts().map((t) => t.message)).toContain('Created reports')
  })

  it('makes one INSIDE a folder, naming the parent it already has', async () => {
    const createFolder = vi.fn().mockResolvedValue(folderCreatedFixture('users/admin'))
    const { bar } = await mountApp({ createFolder })
    await openWorkbench(bar)
    await vi.waitFor(() => treeRow('users'))

    await pickOnFolder('users', 'New folder…')
    await vi.waitFor(() => expect(document.querySelector('#api-new-folder-name')).toBeTruthy())
    answerWith('admin')

    // NESTING IS REPEATED CALLS: the parent is the folder that is already
    // there, and the name is one component. A surface that sent
    // `users/admin` as the name would be asking the backend to sanitise a
    // path, which §13.1 exists to make impossible.
    await vi.waitFor(() => expect(createFolder).toHaveBeenCalledWith(HANDLE, 'users', 'admin'))
    await vi.waitFor(() => treeRow('users/admin'))
  })

  it('the name the backend refuses is refused IN THE ASK, holding what was typed', async () => {
    // §13.1's grammar: a folder name is one path component. The renderer does
    // not know that rule and must not learn it — a surface that sanitised
    // `a/b` into `a-b` would make a folder nobody asked for and report
    // success. The backend's own sentence goes under the field.
    const createFolder = vi
      .fn()
      .mockRejectedValue(new Error('invalid folder name: a folder name is one path component'))
    const { bar } = await mountApp({ createFolder })
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnFolder('', 'New folder…')
    await vi.waitFor(() => expect(document.querySelector('#api-new-folder-name')).toBeTruthy())
    answerWith('a/b')

    await vi.waitFor(() => expect(folderRefusal()).toContain('one path component'))
    // The ask is still open and still holds the answer, so the correction is
    // one keystroke rather than a retype.
    expect(dialogFor('api-new-folder-name').open).toBe(true)
    expect(document.querySelector<HTMLInputElement>('#api-new-folder-name')?.value).toBe('a/b')
  })

  it('a folder that is already there is refused rather than merged into', async () => {
    // Mkdir's own EEXIST. The import refuses an existing destination the same
    // way, and for the same reason: adopting a folder somebody else made puts
    // two owners on one directory.
    const createFolder = vi.fn().mockRejectedValue(new Error('folder already exists: "users"'))
    const { bar } = await mountApp({ createFolder })
    await openWorkbench(bar)
    await vi.waitFor(() => treeRow('users'))

    await pickOnFolder('', 'New folder…')
    await vi.waitFor(() => expect(document.querySelector('#api-new-folder-name')).toBeTruthy())
    answerWith('users')

    await vi.waitFor(() => expect(folderRefusal()).toContain('already exists'))
    // One row, not two, and nothing was said to have worked.
    expect(workbench().querySelectorAll(`[data-row-key="${HANDLE}:users"]`)).toHaveLength(1)
    expect(toasts()).toHaveLength(0)
  })

  it('a new request can be saved into the folder', async () => {
    // The criterion that makes a folder a place rather than a row: a request
    // goes IN it, under a path the allocator freed inside it.
    const disk = folderOnDisk(withFolders(['reports']))
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => treeRow('reports'))

    await pickOnFolder('reports', 'New request')

    await vi.waitFor(() =>
      expect(disk.writeRequest).toHaveBeenCalledWith(
        HANDLE,
        'reports/untitled-request.json',
        expect.objectContaining({ name: 'Untitled request' }),
      ),
    )
    // And it is the request in the form, so the address is the next thing
    // typed rather than a file that has to be found again.
    await vi.waitFor(() => expect(crumbName()).toBe('Untitled request'))
  })

  it('a file that will not read is still a file: its row carries the reason on hover and two acts', async () => {
    // THE OLD RULE WAS ABSENCE. A malformed row's whole answer was the
    // reason printed under it, so the right button was left to the platform
    // and the row's reason sat in the document flow as a red paragraph. The
    // rule was written for a menu of REQUEST acts — Duplicate, Rename, Send
    // — all of which need a decoded request; it is false for acts on a
    // FILE, and a reason the kit forbids in the document flow (a message
    // about an action does not live there) has a home the kit provides: the
    // row's own hover.
    const remove = vi.fn().mockResolvedValue({})
    const { bar } = await mountApp({
      deleteRequest: remove,
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({
            collection: collectionFixture({
              requests: [],
              folders: [],
              malformed: [{ relPath: 'users/oops.json', reason: 'unexpected end of JSON input' }],
            }),
          }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
    })
    await openWorkbench(bar)
    const rowEl = await vi.waitFor(() => treeRow('!users/oops.json'))

    // The reason is the row's hover — whatever the person-facing sentence
    // is, the row's title IS that function's answer (the sentences are the
    // other module's tests) — and it lives nowhere else.
    expect(rowEl.querySelector('.ui-tree-row__name')?.getAttribute('title')).toBe(
      malformedReason('unexpected end of JSON input'),
    )
    // THE DECODER'S OWN WORDS ARE NOWHERE IN THE TREE. The class check this
    // replaced was vacuous — the stylesheet rule for `.api-tree__reason`
    // left with the paragraphs, so nothing could wear the class even if the
    // raw reason were still printed. What matters is the TEXT: the reason
    // lives on the hover and only on the hover, so the raw sentence appears
    // in no descendant's content (a `title` is an attribute, not content).
    const tree = workbench().querySelector('.api-tree')
    if (!tree) throw new Error('no tree on screen')
    expect(tree.textContent).not.toContain('unexpected end of JSON input')

    // Right-clicking opens ONE of ours, with the two acts that need no
    // decoded request.
    const e = rightClick(rowEl)
    expect(e.defaultPrevented).toBe(true)
    await vi.waitFor(() => menuItem('Delete…'))
    expect(menuItem('Copy Absolute Path')).toBeTruthy()
    expect(document.querySelectorAll('.ui-context-menu__item')).toHaveLength(2)

    // Delete names the file that goes, and only a confirmation reaches the
    // backend with this row's handle and path.
    fireEvent.click(menuItem('Delete…'))
    await vi.waitFor(() => expect(confirmText()).toContain('oops.json'))
    fireEvent.click(
      [...document.querySelectorAll<HTMLButtonElement>('dialog button')].find(
        (b) => (b.textContent ?? '').trim() === 'Delete',
      ) as HTMLButtonElement,
    )
    await vi.waitFor(() => expect(remove).toHaveBeenCalledWith(HANDLE, 'users/oops.json'))
  })

  it('Copy Path on a malformed file puts the collection path and relPath on the clipboard', async () => {
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({
            collection: collectionFixture({
              requests: [],
              folders: [],
              malformed: [{ relPath: 'users/oops.json', reason: 'unexpected end of JSON input' }],
            }),
          }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
    })
    await openWorkbench(bar)
    const rowEl = await vi.waitFor(() => treeRow('!users/oops.json'))

    rightClick(rowEl)
    await vi.waitFor(() => menuItem('Copy Absolute Path'))
    fireEvent.click(menuItem('Copy Absolute Path'))

    // The same seam Files uses for the same wording: the collection's own
    // path joined to the row's relPath, written through the clipboard seam.
    await vi.waitFor(() =>
      expect(clipboardMock.writes).toContain(`${COLLECTION_PATH}/users/oops.json`),
    )
  })

  it('a collection whose listing failed carries the error on hover, not as a paragraph', async () => {
    const { bar } = await mountApp({
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({
            error: 'no such folder',
            collection: collectionFixture({ requests: [], folders: [], malformed: [] }),
          }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
    })
    await openWorkbench(bar)
    const collection = await vi.waitFor(() => treeRow(''))
    const tree = workbench().querySelector('.api-tree')
    if (!tree) throw new Error('no tree on screen')
    // The listing's own words (written for a person) are the row's hover —
    // they do NOT go through malformedReason — and live nowhere else. The
    // same pair as a malformed row's: the error is a `title`, and the
    // listing's own words appear in no descendant's content (the class
    // check this replaced was vacuous once the stylesheet rule left).
    expect(collection.querySelector('.ui-tree-row__name')?.getAttribute('title')).toBe(
      'no such folder',
    )
    expect(tree.textContent).not.toContain('no such folder')
  })

  it('with no collection open there is no folder door at all', async () => {
    const { bar } = await mountApp(noCollections())
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('No collections open'))

    // Absence is the capability: a folder needs a collection to go in, and
    // there is none. There is no row to aim at, so no door — rather than a
    // door that is drawn and refuses. The ask itself is mounted for the life
    // of the surface (a closed `<dialog>` keeps its children), so what is
    // asserted is REACHABILITY, which is what a person has.
    expect(dialogFor('api-new-folder-name').open).toBe(false)
    expect(buttonNames().filter((n) => n.startsWith('More actions for'))).toEqual([])
  })
})

// ── A request can be put into a folder (nocx-8aczn.2) ────────────────────
//
// api.request.move existed on the wire; the tree had no door onto it. A
// person who right-clicked a request got Duplicate and Delete and nothing
// for the obvious question — "this request should live under that folder".
// The gesture is one more item in the row's menu plus a chooser for where:
// this collection's own folders and its root (the store already holds them,
// `collection.folders`), and a way to make a folder from the same place.
//
// Every check below drives the seam a person reaches: the menu is opened on
// a real row, the destination is picked in the chooser, and what appears
// afterwards is the assertion. Nothing calls the store directly.

interface MovingDisk {
  files: Map<string, ApiRequest>
  moveRequest: ReturnType<typeof vi.fn>
  listCollections: ReturnType<typeof vi.fn>
  readRequest: ReturnType<typeof vi.fn>
  createFolder: ReturnType<typeof vi.fn>
  services: Partial<ApiWorkbenchServices>
}

function movingDisk(folders: string[] = ['users', 'reports']): MovingDisk {
  const files = new Map<string, ApiRequest>([
    [CREATE_REL_PATH, REQUEST],
    ['ping.json', { ...REQUEST, id: 'ping', name: 'ping', url: 'https://example.test/ping' }],
  ])
  const listCollections = vi.fn(() =>
    Promise.resolve({
      collections: [
        collectionsFixture({
          collection: collectionFixture({
            requests: [...files].map(([relPath, request]) => ({
              relPath,
              name: request.name,
              method: request.method,
            })),
            folders,
          }),
        }),
      ],
      defaultRoot: DEFAULT_ROOT,
    }),
  )
  const readRequest = vi.fn((_handle: string, relPath: string) => {
    const file = files.get(relPath)
    return file === undefined
      ? Promise.reject(new Error(`no such request: ${relPath}`))
      : Promise.resolve({ request: file })
  })
  const moveRequest = vi.fn((_handle: string, from: string, to: string) => {
    const file = files.get(from)
    if (file === undefined) return Promise.reject(new Error(`no such request: ${from}`))
    files.delete(from)
    files.set(to, file)
    return Promise.resolve({ relPath: to })
  })
  const createFolder = vi.fn().mockResolvedValue(folderCreatedFixture('reports'))
  return {
    files,
    moveRequest,
    listCollections,
    readRequest,
    createFolder,
    services: {
      listCollections,
      readRequest,
      moveRequest,
      createFolder,
    },
  }
}

describe('a request can be moved to a folder', () => {
  /** The chooser's folder group — the dialog is found THROUGH it, because
   *  the kit's Dialog takes no data-* and a field id appears inside it only
   *  once New folder is the chosen destination. */
  function moveChooser(): HTMLElement {
    const group = workbench().querySelector<HTMLElement>('.api-move__folders')
    if (!group) throw new Error('the move chooser is not on screen')
    return group
  }

  /** One destination row of the chooser, by the words it shows. The kit's
   *  Radio puts the label in a sibling span, so the row is found by its
   *  text — the aria-label is the same sentence, but the span is what a
   *  person reads. */
  function moveOption(label: string): HTMLInputElement {
    const options = [...moveChooser().querySelectorAll<HTMLLabelElement>('label.ui-radio')]
    const row = options.find((r) => (r.textContent ?? '').trim() === label)
    if (!row) throw new Error(`no move destination named ${label}`)
    const input = row.querySelector<HTMLInputElement>('input[type="radio"]')
    if (!input) throw new Error(`the ${label} destination has no radio input`)
    return input
  }

  /** The chooser's affirmative button, by the words it carries now. */
  function moveSubmit(): HTMLButtonElement {
    const dialog = moveChooser().closest('dialog')
    if (!dialog) throw new Error('the move chooser is not in a dialog')
    const found = [...dialog.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) =>
        (b.textContent ?? '').trim() === 'Move here' ||
        (b.textContent ?? '').trim() === 'Create and move',
    )
    if (!found) throw new Error('no Move button on the chooser')
    return found
  }

  /** A backend whose FOLDER actually changes under a move: the file leaves
   *  one path and lands at another, so "the row is under the new folder
   *  afterwards" is a question the test can ask at all. */
  it("the menu offers Move to folder…, and the chooser lists this collection's folders, its root, and New folder", async () => {
    const { bar } = await mountApp({})
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Move to folder…')
    await vi.waitFor(() => moveChooser())

    // The store's own list, not a second one derived from the requests'
    // paths: `users` is the folder the worked example's requests live in,
    // and a folder with nothing in it would be listed the same way.
    expect(moveOption('Root of acme-api')).toBeTruthy()
    expect(moveOption('users')).toBeTruthy()
    // The new-folder door, because createFolder exists and a young
    // collection is exactly where somebody moves into a folder that is not
    // there yet.
    expect(moveOption('New folder…')).toBeTruthy()
  })

  it('choosing a folder reaches the client method, and the row is under it afterwards', async () => {
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    // A request AT THE ROOT, so the move from beside the folders into one
    // of them is visible: `users/create.json` already LIVES in `users/`,
    // and moving it there would be a no-op that proves nothing.
    await vi.waitFor(() => row('ping.json'))

    await pickOnRow('ping.json', 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    fireEvent.click(moveOption('users'))
    fireEvent.click(moveSubmit())

    // THE CALL IS THE CONTRACT: from the row's path to the destination
    // FILE inside the chosen folder — the folder the person picked, joined
    // to the request's own name (the wire takes two file paths; the
    // chooser offers folders).
    await vi.waitFor(() =>
      expect(disk.moveRequest).toHaveBeenCalledWith(HANDLE, 'ping.json', 'users/ping.json'),
    )
    // And the tree draws it there and no longer beside the root's rows —
    // the destination is where the row is, which is what a move IS. The
    // row is re-read from the listing AFTER the call, so this asserts the
    // outcome, not the call.
    await vi.waitFor(() => row('users/ping.json'))
    expect(disk.files.has('ping.json')).toBe(false)
    expect(disk.files.has('users/ping.json')).toBe(true)
  })

  it('moving the OPEN request leaves it open, and the header names the new place', async () => {
    const disk = movingDisk(['users', 'reports'])
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    // The request in the form, in `users/`.
    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(crumbName()).toBe('create'))

    await pickOnRow(CREATE_REL_PATH, 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    fireEvent.click(moveOption('reports'))
    fireEvent.click(moveSubmit())

    // Still the same request in the form — the move did not throw it away —
    // and the crumb trail now names the folder it lives in.
    await vi.waitFor(() => expect(crumbName()).toBe('create'))
    await vi.waitFor(() => expect(folderCrumb()).toContain('reports'))
    expect(disk.files.has(CREATE_REL_PATH)).toBe(false)
    expect(disk.files.has('reports/create.json')).toBe(true)
  })

  it('a chooser cancelled and reopened starts empty — the last answer is not the next one', async () => {
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    // First opening: pick a NEW folder and start typing a name, then walk
    // away (Cancel). What was chosen must not survive the reopen.
    await pickOnRow('ping.json', 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    fireEvent.click(moveOption('New folder…'))
    await vi.waitFor(() => field('api-move-new-folder-name'))
    fireEvent.input(field('api-move-new-folder-name'), { target: { value: 'reports' } })
    fireEvent.click(button('Cancel'))
    await vi.waitFor(() => expect(moveChooser().closest('dialog')?.open).toBe(false))
    expect(disk.moveRequest).not.toHaveBeenCalled()
    expect(disk.createFolder).not.toHaveBeenCalled()

    // Second opening, still on the same request: the chooser is back at
    // the root choice with nothing typed — pressing the primary would move
    // it to the ROOT, not to a folder nobody chose for it this time.
    await pickOnRow('ping.json', 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    expect(moveOption('Root of acme-api').checked).toBe(true)
    expect(moveOption('New folder…').checked).toBe(false)
    expect(moveSubmit().textContent).toBe('Move here')
    expect(document.querySelector('#api-move-new-folder-name')).toBeNull()
  })

  it('a refusal from the backend reaches the chooser, and the request does not move', async () => {
    const moveRequest = vi
      .fn()
      .mockRejectedValue(new Error('a request with that name is already there'))
    const { bar } = await mountApp({ moveRequest })
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    fireEvent.click(moveOption('users'))
    fireEvent.click(moveSubmit())

    // The backend's own sentence, in a toast — a move into an existing
    // folder has no field the refusal belongs to, so it is said where the
    // kit says outcomes are said. The chooser stays open, so the person can retry.
    await vi.waitFor(() =>
      expect(toasts().some((t) => t.message.includes('already there'))).toBe(true),
    )
    expect(row(CREATE_REL_PATH)).toBeTruthy()
    expect(moveSubmit()).toBeTruthy()
  })

  it('a move takes the last edit with it, because the file already holds it', async () => {
    // THE RULE THIS REPLACES: a move used to be REFUSED while the open
    // request had unsaved edits, with "save, then move" as the remedy — a
    // move that wrote the draft first would have been a second act nobody
    // asked for. Nothing on this surface is saved by a gesture any more, so
    // there is no second act to perform: the edit reaches its file on its
    // own, and the move that follows renames a file that already holds it.
    const disk = movingDisk()
    // This fixture's `files` is moved by `moveRequest` and never written to,
    // so the write the autosave performs has to land in it for the move to
    // be able to carry anything.
    const write = vi.fn((_h: string, relPath: string, request: ApiRequest) => {
      disk.files.set(relPath, request)
      return Promise.resolve({})
    })
    const { bar } = await mountApp({ ...disk.services, writeRequest: write })
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(row(CREATE_REL_PATH))
    await vi.waitFor(() => expect(crumbName()).toBe('create'))
    fireEvent.input(field('api-url'), { target: { value: 'https://h/edited' } })
    await vi.waitFor(() => expect(write).toHaveBeenCalled(), { timeout: 3000 })

    await pickOnRow(CREATE_REL_PATH, 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    fireEvent.click(moveOption('users'))
    fireEvent.click(moveSubmit())

    await vi.waitFor(() =>
      expect(disk.moveRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, 'users/create.json'),
    )
    // The edit is in the moved file, and nobody was told to go and save.
    expect(disk.files.get('users/create.json')?.url).toBe('https://h/edited')
    expect(toasts().some((t) => t.message.includes('Save the request first'))).toBe(false)
  })

  it('makes the folder from the same place, then moves — one gesture for a young collection', async () => {
    const disk = movingDisk(['users'])
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    await pickOnRow(CREATE_REL_PATH, 'Move to folder…')
    await vi.waitFor(() => moveChooser())
    fireEvent.click(moveOption('New folder…'))
    await vi.waitFor(() => field('api-move-new-folder-name'))
    fireEvent.input(field('api-move-new-folder-name'), { target: { value: 'reports' } })
    fireEvent.click(moveSubmit())

    // The two acts a person asked for in one: make the folder at the root,
    // then move the request into it. Both through the store's own methods,
    // so the spies the fixture owns are the ones asserted.
    await vi.waitFor(() => expect(disk.createFolder).toHaveBeenCalledWith(HANDLE, '', 'reports'))
    await vi.waitFor(() =>
      expect(disk.moveRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, 'reports/create.json'),
    )
    // And the row is under the new folder — the chip on the tree.
    await vi.waitFor(() => row('reports/create.json'))
    expect(disk.files.has('reports/create.json')).toBe(true)
    expect(disk.files.has(CREATE_REL_PATH)).toBe(false)
  })

  /** The crumb segment between the collection and the request name. */
  function folderCrumb(): string {
    const el = workbench().querySelector<HTMLElement>('.api-crumbs__folder')
    return (el?.textContent ?? '').trim()
  }
})

// ── Drag a request into a folder (nocx-9db1m) ───────────────────────────
//
// The right-button menu's "Move to folder…" already reaches `moveRequest`;
// these tests are about the ACCELERATOR — dragging a request row onto a
// folder row — calling that same method. The grammar is decided once and
// the decisions are written in the code where it makes them.

/** A DataTransfer jsdom does not implement, carrying a request row's
 *  identity the way a real drag carries the tab strip's own type. The MIME
 *  type keeps OS file drags out of the move path (they carry `Files`) and
 *  gives the drop handler an authoritative source even after a rerender. */
function requestTransfer(handle: string, relPath: string): DataTransfer {
  const data = new Map<string, string>()
  data.set('application/x-nocx-api-request', JSON.stringify({ handle, relPath }))
  return {
    get types() {
      return ['application/x-nocx-api-request']
    },
    files: [],
    getData: (type: string) => data.get(type) ?? '',
    setData: (type: string, value: string) => data.set(type, value),
    clearData: () => data.clear(),
    setDragImage: () => {},
  } as unknown as DataTransfer
}

/** A DataTransfer carrying nothing — a drag from outside this surface, the
 *  way an OS file drag or the tab strip's own drag arrives. */
function foreignTransfer(): DataTransfer {
  return { types: ['Files'], files: [] } as unknown as DataTransfer
}

/** The row a FOLDER is, by the data-row-key the tree builds for every row.
 *  A collection row ('handle:') and a directory row ('handle:path') are
 *  both folders for drag purposes — a collection IS a folder (§6.1). */
/**
 * The folder page's own tab strip (nocx-x3cax.6). The page opens on its
 * CONTENTS, so a test that wants the variables editor has to go there the way
 * a person does — reaching straight for the field would be a test asserting a
 * layout the product no longer has.
 */
function folderTab(name: string): HTMLElement {
  // SCOPED TO THE FOLDER PAGE. The request form has a tab strip of its own,
  // and one of its tabs is also called Variables — an unscoped query finds
  // whichever is first in the document and would let this suite drive the
  // wrong surface while staying green.
  const el = [...workbench().querySelectorAll<HTMLElement>('.api-folder [role="tab"]')].find(
    (tab) => (tab.textContent ?? '').trim().startsWith(name),
  )
  if (!el) throw new Error(`no folder tab ${name}`)
  return el
}

function folderRow(key: string): HTMLElement {
  const el = workbench().querySelector<HTMLElement>(`[data-row-key="${key}"]`)
  if (!el) throw new Error(`no row for key ${key}`)
  return el
}

describe('a request can be dragged into a folder', () => {
  it('drags a request row onto a folder and finds the request inside it', async () => {
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    // A request at the root, dragged onto the `users` folder.
    const source = row('ping.json')
    const target = folderRow(`${HANDLE}:users`)
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(source, { dataTransfer: transfer })
    fireEvent.dragOver(target, { dataTransfer: transfer })
    // The folder shows it is a legal drop target.
    expect(target.dataset.dropTarget).toBe('ok')
    fireEvent.drop(target, { dataTransfer: transfer })

    // The same call the menu makes: from the row's path to the destination
    // file inside the chosen folder.
    await vi.waitFor(() =>
      expect(disk.moveRequest).toHaveBeenCalledWith(HANDLE, 'ping.json', 'users/ping.json'),
    )
    // The tree draws it there.
    await vi.waitFor(() => row('users/ping.json'))
    expect(disk.files.has('ping.json')).toBe(false)
    expect(disk.files.has('users/ping.json')).toBe(true)
  })

  it('dragging the OPEN request leaves it open, pointed at the new path', async () => {
    const disk = movingDisk(['users', 'reports'])
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    // Open the request so it is the one in the form.
    fireEvent.click(row('ping.json'))
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))

    const target = folderRow(`${HANDLE}:reports`)
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })
    fireEvent.dragOver(target, { dataTransfer: transfer })
    fireEvent.drop(target, { dataTransfer: transfer })

    // Still the same request in the form — the move did not throw it away —
    // and the crumb trail names the folder it lives in now. The URL field
    // is the form's own seam: a header that just re-pointed could pass the
    // crumb check while the form held a different request.
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))
    await vi.waitFor(() => expect(folderCrumb()).toContain('reports'))
    await vi.waitFor(() => expect(field('api-url').value).toBe('https://example.test/ping'))
    expect(disk.files.has('reports/ping.json')).toBe(true)
  })

  it('a drop onto the folder it is already in sends no call and reports no error', async () => {
    // The tree is sorted, not free-order: a request's position is its path.
    // Moving to the folder it is already in is a no-op the surface knows
    // about and does not send — `api.request.move` refuses a move to where
    // it already is, and sending a call we know will be refused is a call
    // wasted and an error for a gesture that was a no-op.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    // CREATE_REL_PATH is `users/create.json`, already inside `users`.
    const target = folderRow(`${HANDLE}:users`)
    const transfer = requestTransfer(HANDLE, CREATE_REL_PATH)
    fireEvent.dragStart(row(CREATE_REL_PATH), { dataTransfer: transfer })
    fireEvent.dragOver(target, { dataTransfer: transfer })
    // The folder does not offer itself: a move into the folder the request
    // already sits in would change nothing, and `api.request.move` would
    // refuse it. The row says so with `no` feedback rather than pretending.
    expect(target.dataset.dropTarget).toBe('no')
    fireEvent.drop(target, { dataTransfer: transfer })

    // No call was made, no error reported, and the request is still there.
    await vi.waitFor(() => expect(disk.moveRequest).not.toHaveBeenCalled())
    expect(toasts().some((t) => t.level === 'danger')).toBe(false)
    expect(row(CREATE_REL_PATH)).toBeTruthy()
  })

  it('a drop onto the row it came from sends no call and reports no error', async () => {
    // Dragging a request onto itself: the same handle and the same path.
    // This is a no-op, not an error — the gesture said nothing by doing it.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    const source = row('ping.json')
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(source, { dataTransfer: transfer })
    // A request row is not a folder, so it shows `no` — it is not a target.
    fireEvent.dragOver(source, { dataTransfer: transfer })
    expect(source.dataset.dropTarget).toBe('no')
    fireEvent.drop(source, { dataTransfer: transfer })

    expect(disk.moveRequest).not.toHaveBeenCalled()
    expect(toasts().some((t) => t.level === 'danger')).toBe(false)
  })

  it('a drop onto a request row does not move — only folders take drops', async () => {
    // Between two rows is not a drop target either: the tree is sorted,
    // not free-order, and a request's position is its path, not its row
    // index. Reordering is not a concept here.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))
    await vi.waitFor(() => row(CREATE_REL_PATH))

    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })
    // A request row is not a folder: dragover sees it, refuses the drop.
    fireEvent.dragOver(row(CREATE_REL_PATH), { dataTransfer: transfer })
    expect(row(CREATE_REL_PATH).dataset.dropTarget).toBe('no')
    fireEvent.drop(row(CREATE_REL_PATH), { dataTransfer: transfer })

    expect(disk.moveRequest).not.toHaveBeenCalled()
  })

  it('a drop onto a malformed row does not move — it is not a folder', async () => {
    // A malformed file is not a folder, so it is not a drop target. Its
    // row renders so one bad file cannot hide every good one, and the drag
    // grammar treats it exactly like a request row: `no` feedback, no call.
    const listCollections = vi.fn(() =>
      Promise.resolve({
        collections: [
          collectionsFixture({
            collection: collectionFixture({
              requests: [
                { relPath: CREATE_REL_PATH, name: 'create', method: 'POST' },
                { relPath: 'ping.json', name: 'ping', method: 'GET' },
              ],
              folders: ['users'],
              malformed: [{ relPath: 'broken.json', reason: 'not valid JSON' }],
            }),
          }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
    )
    const moveRequest = vi.fn((_h: string, _from: string, _to: string) =>
      Promise.resolve({ relPath: _to }),
    )
    const { bar } = await mountApp({ listCollections, moveRequest })
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))
    await vi.waitFor(() => folderRow(`${HANDLE}:!broken.json`))

    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })
    const malformed = folderRow(`${HANDLE}:!broken.json`)
    fireEvent.dragOver(malformed, { dataTransfer: transfer })
    expect(malformed.dataset.dropTarget).toBe('no')
    fireEvent.drop(malformed, { dataTransfer: transfer })

    expect(moveRequest).not.toHaveBeenCalled()
  })

  it("the scrolled tree's empty edge is not a drop target — no reorder, no auto-scroll", async () => {
    // The tree scrolls inside `.api-tree`; its empty edge (above the first
    // row and below the last) is NOT a drop target. A drop there cannot
    // reorder — the tree is sorted, and a request's position is its path,
    // not where the pointer let go — and the surface does not add
    // auto-scroll for a gesture the tree cannot answer anyway. The gesture
    // is refused silently: no call, no feedback, no scroll.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    const tree = workbench().querySelector<HTMLElement>('.api-tree')
    if (!tree) throw new Error('no tree container')
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })

    // Over the container itself, not over any row: no target is offered.
    fireEvent.dragOver(tree, { dataTransfer: transfer })
    expect(tree.dataset.dropTarget).toBeUndefined()
    expect(tree.scrollTop).toBe(0)
    fireEvent.drop(tree, { dataTransfer: transfer })

    expect(disk.moveRequest).not.toHaveBeenCalled()
    // The request is still at the root — nothing moved, nothing scrolled.
    expect(row('ping.json')).toBeTruthy()
    expect(tree.scrollTop).toBe(0)
  })

  it('a drop onto the collection row moves into the collection root', async () => {
    // A collection IS a folder (§6.1) and its `relPath` is '' — the root.
    // Dragging a request from inside `users` onto the collection row moves
    // it to the root, exactly the way the menu's "Root of…" does.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    const target = folderRow(`${HANDLE}:`)
    const transfer = requestTransfer(HANDLE, CREATE_REL_PATH)
    fireEvent.dragStart(row(CREATE_REL_PATH), { dataTransfer: transfer })
    fireEvent.dragOver(target, { dataTransfer: transfer })
    expect(target.dataset.dropTarget).toBe('ok')
    fireEvent.drop(target, { dataTransfer: transfer })

    // From `users/create.json` to `create.json` at the root.
    await vi.waitFor(() =>
      expect(disk.moveRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH, 'create.json'),
    )
    await vi.waitFor(() => row('create.json'))
  })

  it('a drop onto another collection says it cannot move there', async () => {
    // `api.request.move` does not cross collections — nocx-8aczn put that
    // out of the METHOD, not just out of the gesture. The drag refuses and
    // SAYS so, rather than silently doing nothing.
    // The FIRST collection carries `ping.json` (the source), the second
    // (`other`) is the foreign collection the drop lands on.
    const first = collectionsFixture({
      collection: collectionFixture({
        requests: [
          { relPath: CREATE_REL_PATH, name: 'create', method: 'POST' },
          { relPath: 'ping.json', name: 'ping', method: 'GET' },
        ],
        folders: ['users'],
      }),
    })
    const other = collectionsFixture({
      handle: 'h2',
      path: '/w/other-api',
      collection: collectionFixture({ name: 'other-api', requests: [], folders: [] }),
    })
    const listCollections = vi.fn(() =>
      Promise.resolve({ collections: [first, other], defaultRoot: DEFAULT_ROOT }),
    )
    const readRequest = vi.fn((_h: string, relPath: string) => {
      const files = new Map<string, ApiRequest>([
        [CREATE_REL_PATH, REQUEST],
        ['ping.json', { ...REQUEST, id: 'ping', name: 'ping', url: 'https://example.test/ping' }],
      ])
      const file = files.get(relPath)
      return file === undefined
        ? Promise.reject(new Error(`no such request: ${relPath}`))
        : Promise.resolve({ request: file })
    })
    const moveRequest = vi.fn((_h: string, _from: string, _to: string) =>
      Promise.resolve({ relPath: _to }),
    )
    const { bar } = await mountApp({ listCollections, readRequest, moveRequest })
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    // Drag a request from the first collection onto the second collection's row.
    const target = folderRow('h2:')
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })
    fireEvent.dragOver(target, { dataTransfer: transfer })
    // The foreign collection's row is NOT offered: the row says `no`, but
    // the drop is still accepted so the refusal can be SAID (a dragover
    // that refuses preventDefault never fires a drop event).
    expect(target.dataset.dropTarget).toBe('no')
    fireEvent.drop(target, { dataTransfer: transfer })

    // No call was made and the person was told why.
    await vi.waitFor(() => expect(moveRequest).not.toHaveBeenCalled())
    await vi.waitFor(() =>
      expect(toasts().some((t) => t.message.includes('own collection'))).toBe(true),
    )
  })

  it('a foreign drag (OS files) does not start a request move', async () => {
    // The private MIME type keeps drags from outside this surface out of
    // the move path. An OS file drag carries `Files`, not the request type.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    const target = folderRow(`${HANDLE}:users`)
    const transfer = foreignTransfer()
    fireEvent.dragOver(target, { dataTransfer: transfer })
    // A foreign drag is not recognised: no drop-target feedback.
    expect(target.dataset.dropTarget).toBeUndefined()
    fireEvent.drop(target, { dataTransfer: transfer })

    expect(disk.moveRequest).not.toHaveBeenCalled()
  })

  it('a folder shows drop feedback while a legal drag is over it', async () => {
    // A drag with no feedback is a drag people do not trust. The folder
    // under the pointer says `data-drop-target="ok"` while a request drag
    // is in flight over it, and nothing when the drag leaves.
    const disk = movingDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    const target = folderRow(`${HANDLE}:users`)
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })

    // Before the drag is over the target, no feedback.
    expect(target.dataset.dropTarget).toBeUndefined()

    fireEvent.dragOver(target, { dataTransfer: transfer })
    expect(target.dataset.dropTarget).toBe('ok')

    // A request row under the same drag says it is not a target.
    fireEvent.dragOver(row('ping.json'), { dataTransfer: transfer })
    expect(row('ping.json').dataset.dropTarget).toBe('no')

    // Back to the folder, then leaving: feedback clears.
    fireEvent.dragOver(target, { dataTransfer: transfer })
    fireEvent.dragLeave(target, { dataTransfer: transfer })
    expect(target.dataset.dropTarget).toBeUndefined()
  })

  it('a refusal from the backend reaches the person, and the request stays put', async () => {
    // The drag is a new path to the same `moveRequest` call the menu uses,
    // and a backend refusal is visible the same way: a toast, because a
    // drag has no field the refusal could belong to. The fixture is
    // movingDisk's, with the move made to refuse.
    const disk = movingDisk()
    disk.moveRequest.mockRejectedValue(new Error('a request with that name is already there'))
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row('ping.json'))

    const target = folderRow(`${HANDLE}:users`)
    const transfer = requestTransfer(HANDLE, 'ping.json')
    fireEvent.dragStart(row('ping.json'), { dataTransfer: transfer })
    fireEvent.dragOver(target, { dataTransfer: transfer })
    fireEvent.drop(target, { dataTransfer: transfer })

    await vi.waitFor(() =>
      expect(toasts().some((t) => t.message.includes('already there'))).toBe(true),
    )
    expect(row('ping.json')).toBeTruthy()
  })

  /** The crumb segment between the collection and the request name. */
  function folderCrumb(): string {
    const el = workbench().querySelector<HTMLElement>('.api-crumbs__folder')
    return (el?.textContent ?? '').trim()
  }
})
// ── The import's report: whose it is, and how it ends (nocx-q2cx5, nocx-favvl) ──
//
// What one import could not carry is the soft degrade AGENTS.md asks to be
// visible in the product rather than only in a log, which is why it never
// lived inside the ask that closes. It was a Section pinned under the tree;
// it is a TOAST now — the same fact, raised where the person is already
// looking, and gone from the sidebar the rest of the time.
//
// STICKY is the half that keeps the rule. A warning's default is eight
// seconds, which for a degrade is a slower way of being invisible, so these
// assert the duration as well as the words: the report ends when the person
// ends it, exactly as the panel's dismiss control did.
//
// Every check below drives the seam a person reaches — the ask is opened by
// its words, the line is typed, and what appears afterwards is the assertion.

/** The degrade toast, or undefined when the import carried everything. */
function notImportedToast(): Toast | undefined {
  return toasts().find((t) => t.level === 'warning')
}

/** Import one curl line the way a person does: the door on the request
 *  line, the field, the button. */
/**
 * The ask the store raises before an import throws away unsaved work
 * (nocx-86wvw). It renders into `document.body` through `showConfirm`, OUTSIDE
 * the workbench, so it is reached document-wide rather than through `button`.
 *
 * Answered the way a person answers it rather than mocked away: the ask is on
 * the path this suite is about, and a suite that stubbed the door would go on
 * passing on the day the door stopped opening.
 */
async function answerDiscard(): Promise<void> {
  const ok = await vi.waitFor(() => {
    const found = [...document.querySelectorAll('button')].find(
      (b) => (b.textContent ?? '').trim() === 'Discard and import',
    )
    if (!found) throw new Error('the discard ask is not on screen')
    return found
  })
  fireEvent.click(ok)
}

/**
 * `discarding` says the form is holding work this import will replace, so the
 * ask above stands between Convert and the result. It is a parameter rather
 * than a look-and-click-if-present because "was the person asked" is the fact
 * each test is making a claim about: a test that shrugged either way could not
 * tell the ask disappearing from a defect.
 */
async function convertCurl(line: string, discarding = false): Promise<void> {
  fireEvent.click(button('Import a curl command'))
  await vi.waitFor(() => field('api-import-curl'))
  fireEvent.input(field('api-import-curl'), { target: { value: line } })
  fireEvent.click(button('Convert to a request'))
  if (discarding) await answerDiscard()
}

describe('the import report says whose it is and can be ended', () => {
  const DROPPED = [
    { what: '--insecure', why: 'a transport option: the send owns this, not the request' },
  ]

  it('names the request the curl line became, and the itemised entry keeps its words', async () => {
    const { bar } = await mountApp({
      importCurl: vi
        .fn()
        .mockResolvedValue({ request: { ...REQUEST, id: '', name: 'ping' }, unsupported: DROPPED }),
    })
    await openWorkbench(bar)
    await convertCurl('curl -k https://h/v1/ping')

    await vi.waitFor(() => expect(notImportedToast()).toBeDefined())
    const told = notImportedToast()!
    // WHICH import. Without this the message is a list about nothing a
    // person can point at in the tree.
    expect(told.message).toContain('ping')
    // …and the entry itself is unchanged: the feature named, and why.
    expect(told.message).toContain('--insecure')
    expect(told.message).toContain('a transport option')
    // It ends when the person ends it. A degrade that dismisses itself while
    // they are reading the request it is about was never told to them.
    expect(told.duration).toBe(0)
  })

  it('names the folder a Postman export was imported into', async () => {
    const { bar } = await mountApp({
      importPostman: vi.fn().mockResolvedValue({ unsupported: DROPPED }),
    })
    await openImportAsk(bar)
    paste('{"info":{"name":"Acme"}}')
    await vi.waitFor(() => expect(destSummary()).toBe(`${DEFAULT_ROOT}/acme`))
    fireEvent.click(button('Import'))

    await vi.waitFor(() => expect(notImportedToast()).toBeDefined())
    expect(notImportedToast()?.message ?? '').toContain(`${DEFAULT_ROOT}/acme`)
    // The success sentence is its own toast and says nothing about the
    // degrade: one act, two facts, neither wearing the other's words.
    expect(toastMessages().some((m) => m.startsWith('Imported into'))).toBe(true)
  })

  it('what it was about is still in the form, so the report costs the person nothing', async () => {
    const { bar } = await mountApp({
      importCurl: vi
        .fn()
        .mockResolvedValue({ request: { ...REQUEST, id: '', name: 'ping' }, unsupported: DROPPED }),
    })
    await openWorkbench(bar)
    await convertCurl('curl -k https://h/v1/ping')
    await vi.waitFor(() => expect(notImportedToast()).toBeDefined())

    // The report is a sentence beside the work, never instead of it: the
    // request the import produced is the one in the form.
    expect(crumbName()).toBe('ping')
  })

  it('a later import replaces the report rather than stacking a second', async () => {
    const importCurl = vi
      .fn()
      .mockResolvedValueOnce({
        request: { ...REQUEST, id: '', name: 'ping' },
        unsupported: DROPPED,
      })
      .mockResolvedValueOnce({
        request: { ...REQUEST, id: '', name: 'pong' },
        unsupported: [{ what: '--proxy', why: 'refused: it changes where the request goes' }],
      })
    const { bar } = await mountApp({ importCurl })
    await openWorkbench(bar)
    await convertCurl('curl -k https://h/v1/ping')
    await vi.waitFor(() => expect(notImportedToast()?.message ?? '').toContain('ping'))

    // ONE report at a time, which is what the panel did by being one panel.
    // Two sticky toasts would leave the first import's list standing beside
    // the second's for as long as nobody closed it. Nothing is asked in
    // between: the first import wrote its request into the collection as it
    // converted, so the second is not replacing anything unsaved.
    await convertCurl('curl --proxy http://p https://h/v1/pong')
    await vi.waitFor(() => expect(notImportedToast()?.message ?? '').toContain('pong'))
    expect(notImportedToast()?.message ?? '').toContain('--proxy')
    expect(toasts().filter((t) => t.level === 'warning')).toHaveLength(1)
  })

  it('an import that lost nothing says nothing at all', async () => {
    // The importer's own rule (internal/apiimport/curl.go): a flag that
    // cannot change the request that is sent is not itemised, so `-sS`
    // arrives with an EMPTY list — and an empty list is silence, not a
    // reassuring sentence.
    const { bar } = await mountApp({
      importCurl: vi
        .fn()
        .mockResolvedValue({ request: { ...REQUEST, id: '', name: 'ping' }, unsupported: [] }),
    })
    await openWorkbench(bar)
    await convertCurl('curl -sS https://h/v1/ping')
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))

    expect(notImportedToast()).toBeUndefined()
  })
})

// ── What the folder ask promises about committing (nocx-flidy) ────────────
//
// The ask used to say "It is safe to commit: no secret value is ever written
// into it", which was true while every credential arrived as a variable NAME
// resolved from the vault. nocx-14exx made a pasted credential stay where the
// person put it, so a curl line's Authorization header is TEXT in the request
// file — in the folder the sentence is about.

/** A request carrying a credential the way a pasted curl line leaves one:
 *  the header, as text, exactly as it was typed. */
function withPastedCredential(): ApiRequest {
  return {
    ...REQUEST,
    id: '',
    name: 'ping',
    auth: { kind: 'none', token: '', password: '', user: '' },
    headers: [
      { name: 'Content-Type', value: 'application/json', enabled: true },
      { name: 'Authorization', value: 'Bearer ghp_liveTokenTypedByHand', enabled: true },
    ],
  }
}

/** What the New collection ask says under its field. */
function newCollectionNote(): string {
  return newCollectionDialog().textContent ?? ''
}

describe('the folder ask says what it actually covers', () => {
  it('promises only the variable half, and does not warn, when nothing pasted is open', async () => {
    const { bar } = await mountApp(noCollections())
    await openWorkbench(bar)
    await vi.waitFor(() => button('New collection'))
    fireEvent.click(button('New collection'))
    await vi.waitFor(() => expect(newCollectionDialog().open).toBe(true))

    const note = newCollectionNote()
    // The promise that was false is gone.
    expect(note).not.toContain('safe to commit')
    expect(note).not.toContain('no secret value is ever written')
    // What is true is still said.
    expect(note).toContain('bound to a variable')
    // And the ordinary case — a collection with nothing pasted in it — is
    // not made frightening by a caveat about somebody else's folder.
    expect(note).not.toContain('Authorization')
  })

  it('says so when the request about to go in carries a credential as text', async () => {
    const { bar } = await mountApp({
      ...noCollections(),
      importCurl: vi.fn().mockResolvedValue({ request: withPastedCredential(), unsupported: [] }),
    })
    await openWorkbench(bar)
    await convertCurl("curl -H 'Authorization: Bearer ghp_liveTokenTypedByHand' https://h/v1/ping")
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))

    fireEvent.click(button('New collection'))
    await vi.waitFor(() => expect(newCollectionDialog().open).toBe(true))

    const note = newCollectionNote()
    expect(note).toContain('Authorization')
    expect(note).toContain('bound to a variable')
    // The VALUE is never repeated back — naming the header is the whole of
    // what the sentence has to say.
    expect(note).not.toContain('ghp_liveTokenTypedByHand')
  })

  it('says nothing about a header whose credential IS a variable', async () => {
    const { bar } = await mountApp({
      ...noCollections(),
      importCurl: vi.fn().mockResolvedValue({
        request: {
          ...withPastedCredential(),
          headers: [{ name: 'Authorization', value: 'Bearer {{token}}', enabled: true }],
        },
        unsupported: [],
      }),
    })
    await openWorkbench(bar)
    await convertCurl("curl -H 'Authorization: Bearer {{token}}' https://h/v1/ping")
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))

    fireEvent.click(button('New collection'))
    await vi.waitFor(() => expect(newCollectionDialog().open).toBe(true))

    expect(newCollectionNote()).not.toContain('Authorization')
  })

  it('and nothing is rewritten, refused or sanitised on the way in', async () => {
    // nocx-14exx, re-confirmed: the product does not hide or relocate what a
    // person typed. The sentence is the only thing this bead changes.
    const { bar } = await mountApp({
      ...noCollections(),
      importCurl: vi.fn().mockResolvedValue({ request: withPastedCredential(), unsupported: [] }),
    })
    await openWorkbench(bar)
    await convertCurl("curl -H 'Authorization: Bearer ghp_liveTokenTypedByHand' https://h/v1/ping")
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))

    fireEvent.click(button('Headers 2'))
    await vi.waitFor(() =>
      expect(
        [...workbench().querySelectorAll<HTMLInputElement>('input')].some(
          (i) => i.value === 'Bearer ghp_liveTokenTypedByHand',
        ),
      ).toBe(true),
    )
  })

  it('the open-a-folder ask says the same thing, in both states', async () => {
    const { bar } = await mountApp({
      ...noCollections(),
      importCurl: vi.fn().mockResolvedValue({ request: withPastedCredential(), unsupported: [] }),
    })
    await openWorkbench(bar)
    await openFolderAsk()
    await vi.waitFor(() => expect(openFolderDialog().open).toBe(true))
    expect(openFolderDialog().textContent ?? '').not.toContain('safe to commit')
    expect(openFolderDialog().textContent ?? '').not.toContain('Authorization')
    fireEvent.click(button('Cancel'))

    await convertCurl("curl -H 'Authorization: Bearer ghp_liveTokenTypedByHand' https://h/v1/ping")
    await vi.waitFor(() => expect(crumbName()).toBe('ping'))
    await openFolderAsk()
    await vi.waitFor(() => expect(openFolderDialog().open).toBe(true))
    expect(openFolderDialog().textContent ?? '').toContain('Authorization')
  })
})

// ── A folder is a place, not a fold ───────────────────────────────────────
//
// Clicking a folder answered with the ABSENCE of its children, which is no
// answer to "what is in here" and none at all to "where am I": the trail
// went on naming a request from somewhere else, and the plus beside it made
// its request somewhere else too. These drive the three doors that changed —
// the click, the ⋮ that puts the form down, and the ask that says where an
// imported curl line goes — each from the state a person starts in.

/** The trail's last segment when it names a PAGE. */
function crumbHere(): string {
  const el = workbench().querySelector<HTMLElement>('.api-crumbs__here')
  return (el?.textContent ?? '').trim()
}

/** The rows of the folder page, as a person reads them. */
function pageRows(): string[] {
  return [...workbench().querySelectorAll<HTMLElement>('.api-folder .ui-record-row__title')].map(
    (el) => (el.textContent ?? '').trim(),
  )
}

describe('a folder is something you open', () => {
  it('clicking one shows what is in it, and says where you are', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))

    // The trail says the place — the question the fold could not answer.
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))
    // And the page says what is in it, in the words the tree uses.
    expect(pageRows()).toContain('create')
  })

  it('opening it does not fold it away — that is the disclosure alone', async () => {
    // The row toggled before, which is what a row does when clicking it
    // means nothing else. Once it means "go in", a click that shut the
    // column was the surface arguing with the person.
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))

    // What is in it is still in the column, and a second press on the folder
    // a person is already in changes nothing.
    expect(row(CREATE_REL_PATH)).toBeTruthy()
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))
    expect(row(CREATE_REL_PATH)).toBeTruthy()

    // And the disclosure still folds, which is now the only thing that does.
    fireEvent.click(button('Collapse users'))
    await vi.waitFor(() =>
      expect(workbench().querySelector(`[data-rel-path="${CREATE_REL_PATH}"]`)).toBeNull(),
    )
  })

  it('every row carries what it can be, on the row', async () => {
    // The acts stand on the row the way the Snippets and Endpoints lists put
    // theirs — this is a page, not the narrow column the tree is, and a ⋮
    // here would be a click spent reaching three things that fit.
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(pageRows()).toContain('create'))

    // The row says which FILE it is, under the name — the only thing that
    // tells two requests somebody named the same apart.
    expect(workbench().querySelector('.api-folder')?.textContent).toContain('create.json')

    // And every act the tree's menu offers is a control here, firing the
    // same thing: duplicating from this row makes the copy.
    expect(buttonNames()).toEqual(
      expect.arrayContaining(['Duplicate create', 'Move create', 'Delete create']),
    )
    fireEvent.click(button('Duplicate create'))

    await vi.waitFor(() =>
      expect(disk.writeRequest).toHaveBeenCalledWith(
        HANDLE,
        'users/create-copy.json',
        expect.objectContaining({ name: 'create copy' }),
      ),
    )
  })

  it('a folder row offers what a folder can hold', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:`))
    await vi.waitFor(() => expect(pageRows()).toContain('users'))

    expect(buttonNames()).toEqual(
      expect.arrayContaining(['New request in users', 'New folder in users']),
    )
    fireEvent.click(button('New request in users'))

    await vi.waitFor(() =>
      expect(disk.writeRequest).toHaveBeenCalledWith(
        HANDLE,
        'users/untitled-request.json',
        expect.objectContaining({ name: 'Untitled request' }),
      ),
    )
  })

  it('the plus on that page makes the request IN that folder', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))

    fireEvent.click(button('New request in this folder'))

    await vi.waitFor(() =>
      expect(disk.writeRequest).toHaveBeenCalledWith(
        HANDLE,
        'users/untitled-request.json',
        expect.objectContaining({ name: 'Untitled request' }),
      ),
    )
    // And what it made is on screen: a page left open over it would hide the
    // request the person just asked for.
    await vi.waitFor(() => expect(crumbName()).toBe('Untitled request'))
  })

  it('a folder with nothing in it says so and offers the door', async () => {
    const disk = folderOnDisk({
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({
            collection: collectionFixture({ requests: [], folders: ['reports'] }),
          }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
    })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => folderRow(`${HANDLE}:reports`))

    fireEvent.click(folderRow(`${HANDLE}:reports`))

    await vi.waitFor(() => expect(crumbHere()).toBe('reports'))
    expect(workbench().textContent).toContain('This folder is empty')
    expect(buttonNames()).toContain('New request')
    // A folder in the column says how much is in it before anybody goes in.
    fireEvent.click(folderRow(`${HANDLE}:`))
    await vi.waitFor(() => expect(crumbHere()).toBe(''))
    expect(workbench().querySelector('.api-folder')?.textContent).toContain('Empty')
  })

  it('opening a request from the page puts it in the form', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(pageRows()).toContain('create'))

    const created = [
      ...workbench().querySelectorAll<HTMLElement>('.api-folder .ui-collection-row'),
    ].find((el) => (el.textContent ?? '').includes('create'))
    fireEvent.click(created as HTMLElement)

    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/users'))
  })
  it('opens on what is in the folder, with the editor a tab away', async () => {
    // The page used to open on the variables editor with the requests pushed
    // under it (nocx-x3cax.6). What a person came for is the contents, so that
    // is the section a folder opens on — and the editor is still one click
    // away, marked with its count so they can see it holds something without
    // going there.
    const readFolder = vi.fn().mockResolvedValue({
      variables: [{ name: 'baseUrl', value: 'https://api.example.test', enabled: true }],
    })
    const disk = folderOnDisk({ readFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(readFolder).toHaveBeenCalledWith(HANDLE, 'users'))

    // The contents are on screen and the editor is not.
    expect(folderTab('Contents').getAttribute('aria-selected')).toBe('true')
    expect(workbench().querySelector('#api-folder-var-name-0')).toBeNull()
    // …and the tab says the folder declares one, in the words the request's
    // own strip uses for the same fact.
    await vi.waitFor(() => expect(folderTab('Variables').textContent?.trim()).toBe('Variables 1'))

    fireEvent.click(folderTab('Variables'))
    await vi.waitFor(() => expect(field('api-folder-var-name-0').value).toBe('baseUrl'))
  })

  it('edits folder variables through the page and saves the rows', async () => {
    const readFolder = vi.fn().mockResolvedValue({ variables: [] })
    const writeFolder = vi.fn().mockResolvedValue({
      variables: [{ name: 'baseUrl', value: 'https://api.example.test', enabled: true }],
    })
    const disk = folderOnDisk({ readFolder, writeFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(readFolder).toHaveBeenCalledWith(HANDLE, 'users'))
    fireEvent.click(folderTab('Variables'))
    await vi.waitFor(() => expect(button('Add variable')).toBeTruthy())
    fireEvent.click(button('Add variable'))
    const table = workbench().querySelector('.ui-row-list__table')
    expect(
      [...(table?.querySelectorAll('thead th') ?? [])].map((cell) => cell.textContent?.trim()),
    ).toEqual(['Send', 'Name', 'Value', 'Remove'])
    expect(workbench().querySelector('[aria-label="Use variable 1"]')).toBeTruthy()
    fireEvent.input(field('api-folder-var-name-0'), { target: { value: 'baseUrl' } })
    fireEvent.input(field('api-folder-var-value-0'), {
      target: { value: 'https://api.example.test' },
    })

    // NOTHING IS PRESSED (nocx-x3cax.7). The rows write themselves once typing
    // stops, and the absence of the control is the feature — so the test also
    // says the page offers no Save to press.
    expect(
      [...workbench().querySelectorAll('.api-folder button')].some(
        (b) => (b.textContent ?? '').trim() === 'Save',
      ),
    ).toBe(false)
    await vi.waitFor(() =>
      expect(writeFolder).toHaveBeenCalledWith(HANDLE, 'users', [
        { name: 'baseUrl', value: 'https://api.example.test', enabled: true },
      ]),
    )
    // The outcome is said quietly, on the page, and not as a toast per
    // keystroke-batch: a save that happened without being asked for still has
    // to be visible, or a person cannot trust it happened.
    await vi.waitFor(() => expect(workbench().textContent).toContain('Saved'))
    expect(toasts().some((toast) => toast.message === 'Saved folder variables')).toBe(false)
  })

  it('two edits in a row cost one write, not two', async () => {
    // The debounce is the reason there is no button. If it did not coalesce, a
    // row typed character by character would be a write per character, which is
    // the cost that makes autosave a bad idea rather than a good one.
    const readFolder = vi.fn().mockResolvedValue({ variables: [] })
    const writeFolder = vi.fn().mockResolvedValue({ variables: [] })
    const disk = folderOnDisk({ readFolder, writeFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(readFolder).toHaveBeenCalledWith(HANDLE, 'users'))
    fireEvent.click(folderTab('Variables'))
    await vi.waitFor(() => expect(button('Add variable')).toBeTruthy())
    fireEvent.click(button('Add variable'))
    fireEvent.input(field('api-folder-var-name-0'), { target: { value: 'baseUrl' } })
    fireEvent.input(field('api-folder-var-value-0'), { target: { value: 'https://one.test' } })

    await vi.waitFor(() => expect(writeFolder).toHaveBeenCalled())
    // Both edits landed inside one pause, so there is one timer and one write —
    // and the write carries the LAST state of the table, not the first.
    expect(writeFolder).toHaveBeenCalledTimes(1)
    expect(writeFolder).toHaveBeenCalledWith(HANDLE, 'users', [
      { name: 'baseUrl', value: 'https://one.test', enabled: true },
    ])
  })

  it('a person who types and walks away still has their edit written', async () => {
    // The failure mode a debounce buys if nobody thinks about it: the last edit
    // is lost because somebody clicked something else before the timer fired.
    // Leaving flushes, and the pending write carries the folder it was typed
    // into rather than wherever the person went.
    const readFolder = vi.fn().mockResolvedValue({ variables: [] })
    const writeFolder = vi.fn().mockResolvedValue({ variables: [] })
    const disk = folderOnDisk({ readFolder, writeFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(readFolder).toHaveBeenCalledWith(HANDLE, 'users'))
    fireEvent.click(folderTab('Variables'))
    await vi.waitFor(() => expect(button('Add variable')).toBeTruthy())
    fireEvent.click(button('Add variable'))
    fireEvent.input(field('api-folder-var-name-0'), { target: { value: 'walkAway' } })

    // Away, at once — well inside the pause the debounce waits out.
    fireEvent.click(row(CREATE_REL_PATH))

    await vi.waitFor(() =>
      expect(writeFolder).toHaveBeenCalledWith(HANDLE, 'users', [
        { name: 'walkAway', value: '', enabled: true },
      ]),
    )
  })

  it('shows the rows the folder read returns', async () => {
    const readFolder = vi.fn().mockResolvedValue({
      variables: [{ name: 'baseUrl', value: 'https://api.example.test', enabled: true }],
    })
    const disk = folderOnDisk({ readFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(readFolder).toHaveBeenCalledWith(HANDLE, 'users'))
    fireEvent.click(folderTab('Variables'))
    await vi.waitFor(() => expect(field('api-folder-var-name-0').value).toBe('baseUrl'))
    expect(field('api-folder-var-value-0').value).toBe('https://api.example.test')
  })

  it('keeps a refused read on the card and explains malformed files', async () => {
    const readFolder = vi
      .fn()
      .mockRejectedValue(
        new Error('apicoll: folder variables file is malformed: ".variables.json"'),
      )
    const disk = folderOnDisk({ readFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() =>
      expect(workbench().querySelector('.ui-status-card__title')?.textContent).toBe(
        'Folder variables unavailable',
      ),
    )
    const card = workbench().querySelector('.ui-status-card')
    expect(card?.textContent).toContain('.variables.json')
    expect(card?.textContent).toContain('Correct')
    expect(toasts().filter((toast) => toast.level === 'danger')).toHaveLength(0)
  })

  it('shows a refused request delete while its folder page is open', async () => {
    const deleteRequest = vi.fn().mockRejectedValue(new Error('delete refused'))
    const disk = folderOnDisk({ deleteRequest })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(pageRows()).toContain('create'))
    fireEvent.click(button('Delete create'))
    await vi.waitFor(() => expect(confirmText()).toContain('create'))
    fireEvent.click(
      [...document.querySelectorAll<HTMLButtonElement>('dialog button')].find(
        (candidate) => (candidate.textContent ?? '').trim() === 'Delete',
      ) as HTMLButtonElement,
    )

    await vi.waitFor(() => expect(deleteRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH))
    await vi.waitFor(() =>
      expect(workbench().querySelector('.ui-status-card__title')?.textContent).toBe(
        'That did not work',
      ),
    )
    expect(workbench().textContent).toContain('delete refused')
  })

  it('keeps a refused save on the table and says it in a danger toast', async () => {
    const readFolder = vi.fn().mockResolvedValue({
      variables: [{ name: 'baseUrl', value: 'https://api.example.test', enabled: true }],
    })
    const writeFolder = vi.fn().mockRejectedValue(new Error('disk went away'))
    const disk = folderOnDisk({ readFolder, writeFolder })
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(readFolder).toHaveBeenCalledWith(HANDLE, 'users'))
    fireEvent.click(folderTab('Variables'))
    await vi.waitFor(() => expect(field('api-folder-var-name-0').value).toBe('baseUrl'))
    fireEvent.input(field('api-folder-var-value-0'), {
      target: { value: 'https://changed.example.test' },
    })

    await vi.waitFor(() => expect(writeFolder).toHaveBeenCalled())
    await vi.waitFor(() =>
      expect(
        toasts().some((toast) => toast.level === 'danger' && toast.message === 'disk went away'),
      ).toBe(true),
    )
    expect(workbench().querySelector('.ui-row-list__table')).toBeTruthy()
    expect(workbench().querySelector('.ui-status-card')).toBeNull()
  })
})

describe('a request can be put down', () => {
  /** Open the ⋮ over the request in the form and pick one of its acts. */
  async function pickOnOpenRequest(label: string): Promise<void> {
    fireEvent.click(button('More actions for this request'))
    await vi.waitFor(() => menuItem(label))
    fireEvent.click(menuItem(label))
  }

  it('closes it, empties the form, and leaves the file alone', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)

    await pickOnOpenRequest('Close request')

    // The form is empty and the tree still has the row: this is a close, not
    // a delete.
    await vi.waitFor(() => expect(crumbName()).toBe(''))
    expect(disk.files.get(CREATE_REL_PATH)).toBeTruthy()
    expect(disk.writeRequest).not.toHaveBeenCalled()
    // What is left on screen is the place the person is still in.
    expect(crumbHere()).toBe('users')
  })

  it('asks nothing and loses nothing — the last edit is written as it closes', async () => {
    // The ask this replaces was about unsaved edits. There are none to be
    // about: closing flushes the draft to its file first, so the honest
    // answer to "are you sure" is to do the save and let go.
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openRequest(bar)
    fireEvent.input(field('api-url'), { target: { value: '{{baseUrl}}/tenants' } })
    await vi.waitFor(() => expect(field('api-url').value).toBe('{{baseUrl}}/tenants'))

    await pickOnOpenRequest('Close request')

    await vi.waitFor(() => expect(crumbName()).toBe(''))
    expect(openDialogs()).toEqual([])
    expect(disk.files.get(CREATE_REL_PATH)?.url).toBe('{{baseUrl}}/tenants')
  })

  it('a row nobody has opened is not offered a close', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))

    // Right-clicked, never opened: the form is empty and this row is a name
    // in a list.
    fireEvent.contextMenu(row(CREATE_REL_PATH))
    await vi.waitFor(() => menuItem('Duplicate'))

    const labels = [...document.querySelectorAll('.ui-context-menu__item')].map((b) =>
      (b.textContent ?? '').trim(),
    )
    expect(labels).not.toContain('Close request')
  })
})

describe('an imported curl line lands where the ask said', () => {
  it('the ask opens holding the folder the person is standing in', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))

    fireEvent.click(button('Import a curl command into this folder'))

    const dest = await vi.waitFor(() => {
      const el = workbench().querySelector<HTMLSelectElement>('#api-import-curl-dest')
      if (!el) throw new Error('the ask offers no destination')
      return el
    })
    // Offered, not demanded: it arrives answered.
    expect(dest.value).toBe('users')
    // And the collection's own root is sayable, under the name the tree uses.
    expect([...dest.options].map((o) => o.value)).toEqual(['', 'users'])
    expect([...dest.options][0]?.textContent).toBe('acme-api')
  })

  it('converts, and the file is in that folder with nothing pressed', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))
    fireEvent.click(button('Import a curl command into this folder'))
    await vi.waitFor(() => field('api-import-curl'))

    fireEvent.input(field('api-import-curl'), {
      target: { value: 'curl https://h/v1/ping' },
    })
    fireEvent.click(button('Convert to a request'))

    // "Nothing is written until the request is saved" (design §10) resolves
    // to the import itself now that nothing on this surface is saved by a
    // gesture: the request is in the folder the ask named, and it is in the
    // tree, before anybody presses anything.
    await vi.waitFor(() => expect(crumbName()).not.toBe(''))
    await vi.waitFor(() => expect(disk.writeRequest.mock.calls[0]?.[1]).toMatch(/^users\//))
    expect(row(disk.writeRequest.mock.calls[0][1])).toBeTruthy()
  })

  it('the trail says which folder it went into', async () => {
    // The trail is where a person looks to see where they are, so it is
    // where the ask's answer has to appear — an import whose destination the
    // surface never states is one a person has to go and look for.
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))
    fireEvent.click(button('Import a curl command into this folder'))
    await vi.waitFor(() => field('api-import-curl'))

    fireEvent.input(field('api-import-curl'), { target: { value: 'curl https://h/v1/ping' } })
    fireEvent.click(button('Convert to a request'))

    await vi.waitFor(() => expect(crumbName()).not.toBe(''))
    const folder = workbench().querySelector<HTMLElement>('.api-crumbs__folder')
    expect((folder?.textContent ?? '').trim()).toBe('users')
  })

  it('the root is a destination somebody can choose, standing in a folder', async () => {
    const disk = folderOnDisk()
    const { bar } = await mountApp(disk.services)
    await openWorkbench(bar)
    await vi.waitFor(() => row(CREATE_REL_PATH))
    fireEvent.click(folderRow(`${HANDLE}:users`))
    await vi.waitFor(() => expect(crumbHere()).toBe('users'))
    fireEvent.click(button('Import a curl command into this folder'))
    await vi.waitFor(() => field('api-import-curl'))

    const dest = workbench().querySelector<HTMLSelectElement>('#api-import-curl-dest')
    fireEvent.change(dest as HTMLSelectElement, { target: { value: '' } })
    fireEvent.input(field('api-import-curl'), { target: { value: 'curl https://h/v1/ping' } })
    fireEvent.click(button('Convert to a request'))

    await vi.waitFor(() => expect(disk.writeRequest).toHaveBeenCalled())
    expect(disk.writeRequest.mock.calls[0]?.[1]).not.toContain('/')
  })

  it('with no collection open the ask offers no destination at all', async () => {
    const { bar } = await mountApp(noCollections())
    await openWorkbench(bar)
    await vi.waitFor(() => expect(workbench().textContent).toContain('No collections open'))

    fireEvent.click(button('Import a curl command'))
    await vi.waitFor(() => field('api-import-curl'))

    expect(workbench().querySelector('#api-import-curl-dest')).toBeNull()
  })
})
