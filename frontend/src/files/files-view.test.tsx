// @vitest-environment jsdom
// Files is a SIDEBAR VIEW — the deliverable is the activity-bar icon, not a
// palette item and not a tab. AGENTS.md rule 1: a user opens the view from
// the activity bar — the FIRST icon — and sees the files of the tab they are
// looking at; expanding a directory lists a page; "show next" reveals the
// rest; clicking a file reaches the opener; switching tabs mid-flight never
// paints one machine's listing into another's tree; a symlink cycle renders
// cyclic; tooLarge and timedOut each render their own state.
//
// These start from a real PaneManager and the real mountSidebar — the panel
// never mounts in a vacuum. The ACTIVE ORIGIN values come from a fixture map
// keyed by tab: the PaneContent capability that terminal content will answer
// (design §5.4) is the one wire this wave cannot exercise, so the tests fake
// its VALUES while the whole mechanism around them — tab switch, signal,
// re-scope, staleness guard — is real.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSignal } from 'solid-js'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { FILES_VIEW_ID, FILES_VIEW_ORDER, createFilesView } from './files-view'
import { mountSidebar, type SidebarHandle, type SidebarViewDescriptor } from '../sidebar'
import { PlugIcon } from '../ui/icons'
import { createRendererMock, makeClient, mountPaneManager } from '../test-support/panes-fixtures'
import type { FilesListEntry, FilesListResult } from '../generated/files.list'
import type { FilesOpenResult } from '../generated/files.open'
import type { FilesReadResult } from '../generated/files.read'
import type { FilesPanelServices } from './files-client'
import type { ActiveOrigin, PaneContent } from '../pane-content'
import { ToastHost, clearToasts } from '../ui/toast'
import type { ClipboardAccess } from '../clipboard'
import { createUploadFlow, type UploadSource } from './upload-flow'
import { createUploadStore } from './upload-store'
import { fakeUploadServices } from './upload-fixtures'
import type { UploadSurface } from './upload-surface'
import { createDownloadFlow } from './download-flow'
import { createDownloadStore } from './download-store'
import { downloadResultFixture, fakeDownloadServices, fakeSaver } from './download-fixtures'
import type { DownloadSurface } from './download-surface'

/** A clipboard fake: the seam's contract is writeText rejects when the
 *  platform refused — tests that assert failure override it. */
function clipboardFixture(over: Partial<ClipboardAccess> = {}): ClipboardAccess {
  return {
    readText: vi.fn().mockResolvedValue(''),
    writeText: vi.fn().mockResolvedValue(undefined),
    ...over,
  }
}

vi.mock('../renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

// ── Fixtures ──────────────────────────────────────────────────────────────

const openFixture = (over: Partial<FilesOpenResult> = {}): FilesOpenResult => ({
  bindingId: 'b1',
  endpointId: null,
  // A revealer wired: the default fixture models a supported backend, so
  // the local-tab menu expectations below exercise the offered path. The
  // absent case passes revealAvailable: false explicitly.
  revealAvailable: true,
  root: { path: '/', display: '/', inferred: false, inferredReason: '' },
  ...over,
})

const entryFixture = (over: Partial<FilesListEntry>): FilesListEntry => ({
  name: 'file',
  path: '/file',
  kind: 'regular',
  size: 0,
  modTime: '2026-08-06T00:00:00Z',
  mode: 0o644,
  ...over,
})

const listFixture = (
  canonical: string,
  entries: FilesListEntry[],
  over: Partial<FilesListResult & { state: 'ok' }> = {},
): FilesListResult => ({
  state: 'ok',
  path: '/',
  canonical,
  entries,
  offset: 0,
  total: entries.length,
  hasMore: false,
  rev: 'r1',
  ...over,
})

const readFixture = (over: Partial<FilesReadResult> = {}): FilesReadResult => ({
  path: '/notes.md',
  canonical: 'C:/notes.md',
  text: 'hello',
  size: 5,
  modTime: '2026-08-06T00:00:00Z',
  truncated: false,
  binary: false,
  lossy: false,
  changed: false,
  ...over,
})

function fakeServices(over: Partial<FilesPanelServices> = {}): FilesPanelServices {
  return {
    open: vi.fn().mockResolvedValue(openFixture()),
    list: vi.fn().mockResolvedValue(listFixture('C:/', [])),
    read: vi.fn().mockResolvedValue(readFixture()),
    watch: vi.fn().mockResolvedValue({ mode: 'watching' }),
    reveal: vi.fn().mockResolvedValue({}),
    subscribeFilesChanged: vi.fn().mockReturnValue(() => {}),
    onConnect: vi.fn().mockReturnValue(() => {}),
    close: vi.fn().mockResolvedValue({}),
    ...over,
  }
}

// Fixture origins stand in for the PaneContent capability (design §5.4) —
// the one wire this wave cannot exercise, because terminal content's
// implementation is the coordinator's to assign. The paneId values are
// fixtures too: the guard only needs them to differ between tabs.
const LOCAL_ORIGIN: ActiveOrigin = {
  paneId: 1,
  sessionId: 's-local',
  kind: 'local',
  cwd: '/',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const SSH_ORIGIN: ActiveOrigin = {
  paneId: 2,
  sessionId: 's-ssh',
  kind: 'ssh',
  cwd: '/home/alice',
  cwdVerified: false,
  cwdFollow: true,
  host: 'srv-01',
  // What terminal-content answers the activeOrigin capability with — the
  // machine-name.ts string, derived once in the composition root so no
  // surface builds a second spelling of one machine.
  machine: 'alice@srv-01',
}

const liveHandles: SidebarHandle[] = []

async function mountApp(
  services: FilesPanelServices,
  clipboard?: ClipboardAccess,
  uploadDeps?: {
    upload?: UploadSurface
    /** The download surface, when the test is about the Download item.
     *  Absent means the panel is not offered one, which is its own case:
     *  no item, whatever the tab. */
    download?: DownloadSurface
    pickSources?: () => Promise<UploadSource[]>
    /** "Are we inside the Wails webview" — jsdom is a browser, so the
     *  default is false and a desktop case must say so (nocx-9le.5.24). */
    native?: () => boolean
  },
) {
  const client = makeClient()
  const { manager } = await mountPaneManager(client)

  // Keyed by CONTENT, not tab: PaneManager keeps its active tab private, and
  // the seam's polymorphism means the map must not care which content class
  // is in front. newSSHPane returns its Tab, whose content is public.
  const originFor = new Map<PaneContent, ActiveOrigin>()
  const initial = manager.activeTerminalContent()
  if (!initial) throw new Error('no initial tab')
  originFor.set(initial, LOCAL_ORIGIN)

  const [activeOrigin, setActiveOrigin] = createSignal<ActiveOrigin | null>(null)
  manager.onActivePaneChange = () =>
    setActiveOrigin(originFor.get(manager.activeTerminalContent()!) ?? null)
  setActiveOrigin(originFor.get(manager.activeTerminalContent()!) ?? null)

  // The opener's mock is kept as a bare reference (not `opener.open`): the
  // assertions call it detached from the object, and unbound-method exists
  // to catch exactly that detachment — the mock is the object's own.
  const open = vi.fn()
  const files = createFilesView({
    services,
    opener: { open },
    activeOrigin,
    clipboard,
    upload: uploadDeps?.upload,
    download: uploadDeps?.download,
    pickSources: uploadDeps?.pickSources,
    native: uploadDeps?.native,
  })
  // Ports stands in at order 0 (main.tsx registers it there); the views
  // reach mountSidebar in order-sorted arrangement, which is what makes
  // Files the FIRST activity-bar icon (SidebarSolid renders array order).
  const ports: SidebarViewDescriptor = {
    id: 'ports',
    title: 'Ports',
    icon: PlugIcon,
    view: () => null,
    order: 0,
  }
  const views = [files, ports].sort((a, b) => a.order - b.order)

  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  /* eslint-disable solid/reactivity -- mountSidebar consumes this accessor
     reactively (SidebarViewProps.activeOrigin, fed to the Files view the
     same way main.tsx feeds portsTargetId); the reads happen inside the
     view's tracked scopes, and the gate cannot see across the function
     boundary. Same justification as the existing main.tsx disable. */
  const handle = mountSidebar(
    bar,
    panel,
    views,
    [],
    undefined,
    () => null,
    () => activeOrigin(),
  )
  /* eslint-enable solid/reactivity */
  // A ToastHost so action outcomes (copies, refused reveals) are
  // ASSERTABLE as rendered toasts, the way a user sees them.
  render(() => <ToastHost />)
  liveHandles.push(handle)
  return { manager, bar, panel, handle, open, originFor, setActiveOrigin }
}

function filesIcon(bar: HTMLElement): HTMLElement {
  const el = bar.querySelector<HTMLElement>(`button[data-view="${FILES_VIEW_ID}"]`)
  if (!el) throw new Error('no files activity-bar button')
  return el
}

function rowsOf(panel: HTMLElement): HTMLElement[] {
  return [...panel.querySelectorAll<HTMLElement>('[data-testid="files-row"]')]
}

function rowNamed(panel: HTMLElement, name: string): HTMLElement {
  const row = rowsOf(panel).find((r) => r.textContent?.includes(name))
  if (!row) throw new Error(`no row named ${name}`)
  return row
}

afterEach(() => {
  clearToasts()
  for (const h of liveHandles) h.destroy()
  liveHandles.length = 0
  cleanup()
  document.body.replaceChildren()
})

describe('files sidebar view', () => {
  it('registers below Ports, so the Files icon is the FIRST view in the activity bar', () => {
    // Ports registers order 0 (main.tsx); below means the top of the view
    // zone — an owner requirement, asserted here, not a consequence of a
    // number somebody picked.
    expect(FILES_VIEW_ORDER).toBeLessThan(0)
  })

  it('from a cold start the Files icon is first, enabled, and opens the panel', async () => {
    const open = vi.fn().mockResolvedValue(openFixture())
    const services = fakeServices({ open })
    const { bar, panel } = await mountApp(services)

    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-panel"]')?.getAttribute('data-root')).toBe(
        '/',
      ),
    )
    await vi.waitFor(() => expect(open).toHaveBeenCalledWith('s-local', '/'))

    // First in the view zone — before Ports.
    const viewButtons = bar.querySelectorAll<HTMLElement>('.activity-bar-top [data-view]')
    expect(viewButtons.length).toBeGreaterThanOrEqual(2)
    expect(viewButtons[0]?.dataset.view).toBe(FILES_VIEW_ID)

    // Enabled from a cold start (nothing gates it on prior state).
    const icon = filesIcon(bar) as HTMLButtonElement
    expect(icon.disabled).toBe(false)

    // Clicking the icon closes and reopens the panel with the tree intact.
    icon.click()
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))
    icon.click()
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    expect(panel.querySelector('[data-testid="files-panel"]')?.getAttribute('data-root')).toBe('/')
  })

  it('expanding a directory reaches files.list and the returned entries appear as rows', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listFixture('C:/docs', [entryFixture({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const services = fakeServices({ list })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())

    const docs = rowNamed(panel, 'docs')
    const disclosure = docs.querySelector<HTMLElement>('.ui-tree-row__disclosure')
    expect(disclosure).not.toBeNull()
    disclosure!.click()

    await vi.waitFor(() => expect(list).toHaveBeenCalledWith('b1', '/docs', 0, expect.any(Number)))
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())
  })

  it('clicking a directory row anywhere expands it, and the disclosure toggles exactly once', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listFixture('C:/docs', [entryFixture({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const services = fakeServices({ list })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())

    // The row, not the 16px disclosure: this is the click a user makes.
    rowNamed(panel, 'docs').click()
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    // And clicking it again collapses it — one click, one toggle.
    rowNamed(panel, 'docs').click()
    await vi.waitFor(() =>
      expect(rowsOf(panel).find((r) => r.textContent?.includes('notes.md'))).toBeUndefined(),
    )

    // The disclosure is inside the row, so its click reaches the row's
    // handler too unless the kit stops it. If it did, this would expand and
    // immediately collapse, and the one control built for the job would be
    // the only place in the row that does nothing.
    rowNamed(panel, 'docs').querySelector<HTMLElement>('.ui-tree-row__disclosure')!.click()
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())
  })

  // ── The reveal (nocx-r3bz: the terminal owns "where am I"; the panel,
  //    rooted at /, follows by revealing) ───────────────────────────────
  it('a cd in the terminal reveals and selects the new directory, without a click or a tab switch', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listFixture('C:/docs', [entryFixture({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const { panel, setActiveOrigin } = await mountApp(fakeServices({ list }))
    await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())
    // The origin's cwd was the root: nothing is selected.
    expect(panel.querySelector('[data-selected="true"]')).toBeNull()

    // The OSC 7 arrives: same session, new VERIFIED cwd. The reveal walks
    // / → docs, selects the row — no click, no tab switch.
    setActiveOrigin({ ...LOCAL_ORIGIN, cwd: '/docs' })
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-selected="true"]')?.textContent).toContain('docs'),
    )
    // The kit row carries the selection (data-selected lives on .ui-tree-row).
    const docsRow = rowNamed(panel, 'docs').querySelector('.ui-tree-row')
    expect(docsRow?.getAttribute('data-selected')).toBe('true')
    // The target is expanded too, so its children were listed: landing on
    // a closed folder is not an answer to "where am I".
    expect(list.mock.calls.filter(([, p]) => p === '/docs').length).toBeGreaterThan(0)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())
  })

  it('the reveal scrolls the selected row into view', async () => {
    const scrollIntoView = vi.fn()
    /* eslint-disable @typescript-eslint/unbound-method -- the prototype
       reads mirror the scroll-stub precedent in terminal-content.test.ts */
    const original = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollIntoView = scrollIntoView
    try {
      const list = vi
        .fn()
        .mockImplementation((bindingId: string, path: string) =>
          Promise.resolve(
            path === '/'
              ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
              : listFixture('C:/docs', [
                  entryFixture({ name: 'notes.md', path: '/docs/notes.md' }),
                ]),
          ),
        )
      const { panel, setActiveOrigin } = await mountApp(fakeServices({ list }))
      await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())

      setActiveOrigin({ ...LOCAL_ORIGIN, cwd: '/docs' })
      await vi.waitFor(() => expect(scrollIntoView).toHaveBeenCalled())
      expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start' })
    } finally {
      Element.prototype.scrollIntoView = original
    }
  })

  it('switching to a viewer tab moves nothing — a no-opinion origin never reveals', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listFixture('C:/docs', [entryFixture({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const { panel, setActiveOrigin } = await mountApp(fakeServices({ list }))
    await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())

    // Reveal /docs first, so there IS a selection to move.
    setActiveOrigin({ ...LOCAL_ORIGIN, cwd: '/docs' })
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-selected="true"]')?.textContent).toContain('docs'),
    )
    const listsBefore = list.mock.calls.length

    // A viewer answers the same session with NO opinion (cwdFollow false):
    // the panel keeps its tree and binding, and nothing moves — not even
    // towards the viewer's frozen cwd.
    setActiveOrigin({ ...LOCAL_ORIGIN, paneId: 99, cwd: '/elsewhere', cwdFollow: false })
    await new Promise((r) => setTimeout(r, 20))

    expect(panel.querySelector('[data-selected="true"]')?.textContent).toContain('docs')
    expect(list.mock.calls.length).toBe(listsBefore)
    expect(list.mock.calls.filter(([, p]) => p === '/elsewhere')).toHaveLength(0)
  })

  it('an unverified cwd reveals nothing', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listFixture('C:/docs', [entryFixture({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const { panel, setActiveOrigin } = await mountApp(fakeServices({ list }))
    await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())

    setActiveOrigin({ ...LOCAL_ORIGIN, cwd: '/docs', cwdVerified: false })
    await new Promise((r) => setTimeout(r, 20))

    expect(panel.querySelector('[data-selected="true"]')).toBeNull()
    expect(list.mock.calls.filter(([, p]) => p === '/docs')).toHaveLength(0)
  })

  it('"show next" reveals the rest of a paginated directory', async () => {
    const list = vi.fn().mockImplementation((bindingId: string, path: string, offset: number) =>
      Promise.resolve(
        offset === 0
          ? listFixture('C:/', [entryFixture({ name: 'f1' })], {
              total: 3,
              hasMore: true,
            })
          : listFixture('C:/', [entryFixture({ name: 'f2' }), entryFixture({ name: 'f3' })], {
              offset: 1,
              total: 3,
              hasMore: false,
            }),
      ),
    )
    const services = fakeServices({ list })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'f1')).not.toBeUndefined())

    const moreBtn = panel.querySelector<HTMLElement>('[data-testid="files-show-more"]')
    expect(moreBtn).not.toBeNull()
    expect(moreBtn?.textContent).toContain('Show next 2')
    moreBtn!.click()

    await vi.waitFor(() => expect(list).toHaveBeenCalledWith('b1', '/', 1, expect.any(Number)))
    await vi.waitFor(() => expect(rowNamed(panel, 'f2')).not.toBeUndefined())
    expect(rowNamed(panel, 'f3')).not.toBeUndefined()
    expect(panel.querySelector('[data-testid="files-show-more"]')).toBeNull()
  })

  it('clicking a file row reaches FileOpener.open with the right target', async () => {
    const read = vi.fn().mockResolvedValue(readFixture())
    const services = fakeServices({
      read,
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [
            entryFixture({ name: 'notes.md', path: '/notes.md' }),
            entryFixture({ name: 'docs', path: '/docs', kind: 'dir' }),
          ]),
        ),
    })
    const { panel, open } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    rowNamed(panel, 'notes.md').click()
    await vi.waitFor(() => expect(open).toHaveBeenCalledTimes(1))
    expect(read).toHaveBeenCalledWith('b1', '/notes.md', 0)
    expect(open).toHaveBeenCalledWith({
      bindingId: 'b1',
      endpointId: null,
      path: '/notes.md',
      canonical: 'C:/notes.md',
      displayHost: null,
      name: 'notes.md',
      // The click-time scope minus the paneId — the viewer's activeOrigin
      // answer, which keeps the panel on this machine while the viewer tab
      // is in front (design §5.4).
      origin: {
        sessionId: 's-local',
        kind: 'local',
        cwd: '/',
        cwdVerified: true,
        // The viewer answers "where are we" with NO opinion — the frozen
        // origin must never drive a reveal.
        cwdFollow: false,
        host: null,
      },
    })

    // A directory row opens nothing.
    rowNamed(panel, 'docs').click()
    expect(open).toHaveBeenCalledTimes(1)
  })

  // ── The §0 test, through the real wiring ───────────────────────────────
  it("switching tabs mid-flight does not paint one machine's listing into another's tree", async () => {
    let releaseRootA!: (v: FilesListResult) => void
    const pendingA = new Promise<FilesListResult>((res) => {
      releaseRootA = res
    })
    const list = vi
      .fn()
      .mockResolvedValueOnce(pendingA) // tab A's root listing, still in flight
      .mockResolvedValueOnce(
        listFixture('C:/home/alice', [
          entryFixture({ name: 'b-only.txt', path: '/home/alice/b-only.txt' }),
        ]),
      )
    const services = fakeServices({ list })
    const { manager, panel, originFor } = await mountApp(services)
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(1))

    // The user activates an SSH tab while A's listing is unresolved.
    const sshPane = manager.newSSHPane('p1', 'host.example', 'alice')
    originFor.set(sshPane.content, SSH_ORIGIN)
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect(rowNamed(panel, 'b-only.txt')).not.toBeUndefined())

    // A's listing finally lands — it must not paint A's machine into B's tree.
    releaseRootA(listFixture('C:/', [entryFixture({ name: 'a-only.txt', path: '/a-only.txt' })]))
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2))
    expect(rowsOf(panel).map((r) => r.textContent)).not.toContain('a-only.txt')
    expect(rowNamed(panel, 'b-only.txt')).not.toBeUndefined()
  })

  it('a directory symlink whose canonical matches an expanded ancestor renders cyclic with no children', async () => {
    const list = vi.fn().mockImplementation((bindingId: string, path: string) =>
      Promise.resolve(
        path === '/'
          ? listFixture('C:/', [
              entryFixture({
                name: 'loop',
                path: '/loop',
                kind: 'symlink',
                linkKind: 'dir',
                linkTarget: '/',
              }),
            ])
          : listFixture('C:/', [entryFixture({ name: 'leak.md' })]),
      ),
    )
    const services = fakeServices({ list })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'loop')).not.toBeUndefined())

    rowNamed(panel, 'loop').querySelector<HTMLElement>('.ui-tree-row__disclosure')!.click()
    await vi.waitFor(() => expect(list).toHaveBeenCalledWith('b1', '/loop', 0, expect.any(Number)))

    // Renders cyclic (a leaf — no disclosure), and no children were listed.
    // data-cyclic lives on the kit row (.ui-tree-row), not the surface
    // wrapper, so the assertion queries the attribute directly.
    await vi.waitFor(() => expect(panel.querySelector('[data-cyclic="true"]')).not.toBeNull())
    expect(rowNamed(panel, 'loop').querySelector('.ui-tree-row__disclosure')).toBeNull()
    expect(rowsOf(panel).map((r) => r.textContent)).not.toContain('leak.md')
    expect(list.mock.calls.filter(([, p]) => p === '/loop')).toHaveLength(1)
  })

  it('tooLarge and timedOut each render their own state', async () => {
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/')
        return Promise.resolve(
          listFixture('C:/', [
            entryFixture({ name: 'big', path: '/big', kind: 'dir' }),
            entryFixture({ name: 'slow', path: '/slow', kind: 'dir' }),
          ]),
        )
      if (path === '/big')
        return Promise.resolve({ state: 'tooLarge' as const, observedCount: 12_345, limit: 1_000 })
      return Promise.resolve({ state: 'timedOut' as const, timeout: 5_000 })
    })
    const services = fakeServices({ list })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'big')).not.toBeUndefined())

    rowNamed(panel, 'big').querySelector<HTMLElement>('.ui-tree-row__disclosure')!.click()
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-state-too-large"]')).not.toBeNull(),
    )
    const tooLarge = panel.querySelector('[data-testid="files-state-too-large"]')
    expect(tooLarge?.textContent).toContain('More than 1000 entries')
    expect(tooLarge?.textContent).toContain('12345 entries')
    // No pagination offered for a capped directory.
    expect(panel.querySelector('[data-testid="files-show-more"]')).toBeNull()

    rowNamed(panel, 'slow').querySelector<HTMLElement>('.ui-tree-row__disclosure')!.click()
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-state-timed-out"]')).not.toBeNull(),
    )
    expect(panel.querySelector('[data-testid="files-state-timed-out"]')?.textContent).toContain(
      'took too long',
    )

    // timedOut retries the same enumeration.
    const retry = panel.querySelector<HTMLElement>('[data-testid="files-retry"]')
    expect(retry).not.toBeNull()
    list.mockImplementation((bindingId: string, path: string) => {
      if (path === '/')
        return Promise.resolve(
          listFixture('C:/', [
            entryFixture({ name: 'big', path: '/big', kind: 'dir' }),
            entryFixture({ name: 'slow', path: '/slow', kind: 'dir' }),
          ]),
        )
      if (path === '/big')
        return Promise.resolve({ state: 'tooLarge' as const, observedCount: 12_345, limit: 1_000 })
      return Promise.resolve(
        listFixture('C:/slow', [entryFixture({ name: 'x.md', path: '/slow/x.md' })]),
      )
    })
    retry!.click()
    await vi.waitFor(() => expect(rowNamed(panel, 'x.md')).not.toBeUndefined())
  })

  it('a tab with no origin shows the no-files state, never a stale tree', async () => {
    const services = fakeServices()
    const { panel, setActiveOrigin } = await mountApp(services)
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-panel"]')?.getAttribute('data-root')).toBe(
        '/',
      ),
    )

    setActiveOrigin(null)
    await vi.waitFor(() => expect(panel.textContent).toContain('No files to show'))
    expect(
      panel.querySelector('[data-testid="files-panel"]')?.getAttribute('data-root'),
    ).toBeFalsy()
  })

  it('the header refresh re-lists the tree and the polling badge slot sits beside it', async () => {
    const list = vi.fn().mockResolvedValue(listFixture('C:/', [entryFixture({ name: 'a.txt' })]))
    const services = fakeServices({ list })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'a.txt')).not.toBeUndefined())

    const refresh = panel.querySelector<HTMLElement>('[data-testid="files-refresh"]')
    expect(refresh?.closest('.ui-sidebar-view__header')).not.toBeNull()
    refresh!.click()
    await vi.waitFor(() => expect(list.mock.calls.length).toBeGreaterThanOrEqual(2))

    // The §5.5 badge slot is beside Refresh in the header, and a healthy
    // watch renders nothing into it.
    const slot = panel.querySelector<HTMLElement>('[data-testid="files-polling-badge-slot"]')
    expect(slot?.closest('.ui-sidebar-view__header')).not.toBeNull()
    expect(panel.querySelector('[data-testid="files-polling-badge"]')).toBeNull()
  })

  // ── Row actions (fm-w13) ─────────────────────────────────────────────

  it('right-clicking a row opens the menu with both copy entries and Show in Finder', async () => {
    const services = fakeServices({
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
    })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 40, clientY: 60 })

    const menu = document.querySelector('[data-testid="files-context-menu"]')
    expect(menu).not.toBeNull()
    const items = [...menu!.querySelectorAll<HTMLElement>('[role="menuitem"]')]
    expect(items.map((i) => i.textContent)).toEqual([
      'Copy Relative Path',
      'Copy Absolute Path',
      'Show in Finder',
    ])
    // Picking an item dismisses the menu.
    items[0].click()
    expect(document.querySelector('[data-testid="files-context-menu"]')).toBeNull()
  })

  it('every row in the menu carries its mark, not an empty reserved column', async () => {
    // The kit RESERVES the icon column whether or not an icon is passed, so
    // an unmarked menu renders perfectly and reads as a list of words —
    // which is how three of the four call sites shipped with no marks at all
    // and nothing said so (nocx-inbw1). The lint rule catches the literal;
    // this catches what a person actually sees, and neither can substitute
    // for the other: the rule cannot see a row assembled some other way, and
    // a test naming three labels cannot see the fourth row somebody adds.
    const services = fakeServices({
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
    })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 40, clientY: 60 })

    const items = [
      ...document.querySelectorAll<HTMLElement>(
        '[data-testid="files-context-menu"] [role="menuitem"]',
      ),
    ]
    expect(items.length).toBeGreaterThan(0)
    for (const item of items) {
      expect(item.querySelector('.ui-context-menu__icon svg')).not.toBeNull()
    }
  })

  it('copying the relative path puts the path as spelled from the root on the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listFixture('C:/', [entryFixture({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listFixture('C:/docs', [entryFixture({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const { panel } = await mountApp(fakeServices({ list }), clipboardFixture({ writeText }))
    await vi.waitFor(() => expect(rowNamed(panel, 'docs')).not.toBeUndefined())
    rowNamed(panel, 'docs').querySelector<HTMLElement>('.ui-tree-row__disclosure')!.click()
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    // The row is inside an expanded subdirectory: the copy is docs/notes.md,
    // not a depth-0 spelling and not the absolute path.
    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 10, clientY: 10 })
    const relative = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (i) => i.textContent === 'Copy Relative Path',
    )
    relative!.click()
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith('docs/notes.md'))
  })

  it("copying the absolute path puts the lexical path there — a symlink's own path, not its target", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const services = fakeServices({
      list: vi.fn().mockResolvedValue(
        listFixture('C:/', [
          entryFixture({
            name: 'link',
            path: '/link',
            kind: 'symlink',
            linkKind: 'regular',
            linkTarget: '/elsewhere/real.txt',
          }),
        ]),
      ),
    })
    const { panel } = await mountApp(services, clipboardFixture({ writeText }))
    await vi.waitFor(() => expect(rowNamed(panel, 'link')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'link'), { clientX: 10, clientY: 10 })
    const absolute = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (i) => i.textContent === 'Copy Absolute Path',
    )
    absolute!.click()
    // The link's own path, lexical — the canonical (which resolves symlinks)
    // is the deduplication identity, never the copy.
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith('/link'))
    expect(writeText).not.toHaveBeenCalledWith('/elsewhere/real.txt')
  })

  it('"Show in Finder" is present on a local tab and ABSENT on a remote one', async () => {
    // The two trees are distinguishable: the SSH binding lists a row that
    // only exists on the remote machine, so the test waits for the RESCOPE
    // to land, not for a row that both trees share.
    const open = vi.fn().mockImplementation((sessionId: string) =>
      Promise.resolve(
        sessionId === 's-local'
          ? openFixture()
          : openFixture({
              bindingId: 'b2',
              root: {
                path: '/home/alice',
                display: '~/alice',
                inferred: false,
                inferredReason: '',
              },
            }),
      ),
    )
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/home/alice'
            ? listFixture('C:/home/alice', [
                entryFixture({ name: 'remote.md', path: '/home/alice/remote.md' }),
              ])
            : listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
      )
    const { manager, panel, originFor } = await mountApp(fakeServices({ open, list }))
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    const menuItems = () =>
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((i) => i.textContent)

    // Local: present.
    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 10, clientY: 10 })
    expect(menuItems()).toContain('Show in Finder')
    fireEvent.pointerDown(document.body)
    await vi.waitFor(() =>
      expect(document.querySelector('[data-testid="files-context-menu"]')).toBeNull(),
    )

    // Remote: the item is ABSENT — not disabled. Assert the absence
    // explicitly; a test that only checks presence cannot catch the item
    // leaking onto SSH tabs.
    const sshPane = manager.newSSHPane('p1', 'host.example', 'alice')
    originFor.set(sshPane.content, SSH_ORIGIN)
    await vi.waitFor(() => expect(rowNamed(panel, 'remote.md')).not.toBeUndefined())
    fireEvent.contextMenu(rowNamed(panel, 'remote.md'), { clientX: 10, clientY: 10 })
    expect(menuItems()).toEqual(['Copy Relative Path', 'Copy Absolute Path'])
    expect(menuItems()).not.toContain('Show in Finder')
  })

  it('"Show in Finder" is ABSENT on a local tab when the backend has no revealer', async () => {
    // The other half of the capability gate (nocx-ngf3u): a backend on a
    // platform nocx ships no file-manager reveal for answers
    // revealAvailable:false on the open result, and the item must not
    // render — a menu that offers a capability the backend refuses is the
    // exact defect this bead exists to remove. Absence, not a greyed-out
    // row, and not a toast the person discovers by clicking.
    const open = vi.fn().mockResolvedValue(openFixture({ revealAvailable: false }))
    const { panel } = await mountApp(
      fakeServices({
        open,
        list: vi
          .fn()
          .mockResolvedValue(
            listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
          ),
      }),
    )
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 10, clientY: 10 })
    const menuItems = () =>
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((i) => i.textContent)
    expect(menuItems()).toEqual(['Copy Relative Path', 'Copy Absolute Path'])
    expect(menuItems()).not.toContain('Show in Finder')
  })

  it('"Show in Finder" IS present on a local tab when the backend has a revealer', async () => {
    // The paired positive of the gate: the default fixture carries
    // revealAvailable:true (a supported backend), and the item renders —
    // the absence above is the exception, not the rule.
    const open = vi.fn().mockResolvedValue(openFixture({ revealAvailable: true }))
    const { panel } = await mountApp(
      fakeServices({
        open,
        list: vi
          .fn()
          .mockResolvedValue(
            listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
          ),
      }),
    )
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 10, clientY: 10 })
    const menuItems = () =>
      [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].map((i) => i.textContent)
    expect(menuItems()).toContain('Show in Finder')
  })

  it('a refused files.reveal is rendered as a toast, never swallowed', async () => {
    const reveal = vi.fn().mockRejectedValue(new Error('method not found'))
    const services = fakeServices({
      reveal,
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
    })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 10, clientY: 10 })
    const revealItem = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (i) => i.textContent === 'Show in Finder',
    )
    expect(revealItem).not.toBeUndefined()
    revealItem!.click()

    await vi.waitFor(() => expect(reveal).toHaveBeenCalledWith('b1', '/notes.md'))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
        'method not found',
      ),
    )
  })

  it('a clipboard write that fails is reported to the user', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('no clipboard backend available'))
    const services = fakeServices({
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
    })
    const { panel } = await mountApp(services, clipboardFixture({ writeText }))
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    fireEvent.contextMenu(rowNamed(panel, 'notes.md'), { clientX: 10, clientY: 10 })
    const relative = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(
      (i) => i.textContent === 'Copy Relative Path',
    )
    relative!.click()
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
        'no clipboard backend available',
      ),
    )
  })

  // ── Watching (fm-w13 part 2) ─────────────────────────────────────────

  it('the Polling badge shows for a degraded LOCAL watch and nothing on a remote one', async () => {
    const watch = vi
      .fn()
      .mockResolvedValue({ mode: 'polling', degradedReason: 'local watch unavailable' })
    const services = fakeServices({
      watch,
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
    })
    const { manager, panel, originFor } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())

    // Local + polling + a reason: the persistent badge, hover carries the
    // reason.
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-polling-badge"]')).not.toBeNull(),
    )
    const badge = panel.querySelector('[data-testid="files-polling-badge"]')
    expect(badge?.getAttribute('title')).toBe('local watch unavailable')

    // Remote + polling (even WITH a reason — the kind check is the guard):
    // nothing. The remote half is what stops the badge becoming wallpaper.
    const sshPane = manager.newSSHPane('p1', 'host.example', 'alice')
    originFor.set(sshPane.content, SSH_ORIGIN)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-polling-badge"]')).toBeNull(),
    )
  })

  it('a failed watch escalates to a sticky inline message with Retry', async () => {
    const watch = vi.fn().mockRejectedValue(new Error('not connected'))
    const services = fakeServices({
      watch,
      list: vi
        .fn()
        .mockResolvedValue(
          listFixture('C:/', [entryFixture({ name: 'notes.md', path: '/notes.md' })]),
        ),
    })
    const { panel } = await mountApp(services)
    await vi.waitFor(() => expect(rowNamed(panel, 'notes.md')).not.toBeUndefined())
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-watch-error"]')).not.toBeNull(),
    )
    expect(panel.querySelector('[data-testid="files-watch-error"]')?.textContent).toContain(
      'not connected',
    )

    // Retry is the refresh cycle; the message clears the instant the watch
    // recovers.
    watch.mockResolvedValue({ mode: 'watching' })
    panel.querySelector<HTMLElement>('[data-testid="files-watch-retry"]')!.click()
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-watch-error"]')).toBeNull(),
    )
  })
})

// ── The Upload action (design §4: the panel's gesture) ────────────────────
//
// A user opens the overflow, picks Upload, chooses files, and they arrive in
// the folder the panel is showing. The header must NOT grow a seventh
// button: it is already over-full and nocx-a8cz owns how it overflows.
describe('uploading from the panel', () => {
  function uploadFixture() {
    const services = fakeUploadServices()
    const store = createUploadStore({ services })
    const said: string[] = []
    const flow = createUploadFlow({
      services,
      store,
      ask: () => Promise.resolve({ answer: 'skip', applyToAll: false }),
      report: (m) => said.push(m),
    })
    const surface: UploadSurface = { services, store, flow }
    return { services, store, flow, surface, said }
  }

  /** A menu row, found by the words on it — ContextMenu gives its items no
   *  test id of their own, and a person picks the row that says Upload. */
  function menuItem(label: string): HTMLElement {
    const items = document.querySelectorAll<HTMLElement>(
      '[data-testid="files-overflow-menu"] .ui-context-menu__item',
    )
    for (const item of items) if ((item.textContent ?? '').includes(label)) return item
    throw new Error(`no menu item named ${label}`)
  }

  /** The bar the header actions render into. */
  function header(panel: HTMLElement): HTMLElement {
    const el = panel.querySelector<HTMLElement>('.ui-sidebar-view__actions')
    if (!el) throw new Error('no header actions')
    return el
  }

  /** An ssh tab whose cwd is verified — the panel reveals it, and the
   *  revealed folder is what the action uploads into. */
  async function mountOnRemote(
    over: { pickSources?: () => Promise<UploadSource[]>; native?: () => boolean } = {},
  ) {
    const u = uploadFixture()
    const services = fakeServices({
      list: vi
        .fn()
        .mockImplementation((_b: string, path: string) =>
          Promise.resolve(
            path === '/'
              ? listFixture('C:/', [entryFixture({ name: 'srv', path: '/srv', kind: 'dir' })])
              : listFixture('C:/srv', []),
          ),
        ),
    })
    const app = await mountApp(services, undefined, {
      upload: u.surface,
      pickSources: over.pickSources,
      native: over.native,
    })
    app.setActiveOrigin({ ...SSH_ORIGIN, cwd: '/srv', cwdVerified: true })
    await vi.waitFor(() =>
      expect(app.panel.querySelector('[data-testid="files-overflow"]')).not.toBeNull(),
    )
    // The panel's folder is the one the reveal reached, so the action has
    // no destination until the reveal lands — wait for the selection the
    // reveal draws, not merely for the button.
    await vi.waitFor(() =>
      expect(app.panel.querySelector('[data-selected="true"]')?.textContent).toContain('srv'),
    )
    return { ...app, ...u }
  }

  it('puts Upload in the overflow menu and not in the header', async () => {
    const app = await mountOnRemote()
    // Nothing in the header says "upload" — the whole point of the bead
    // that owns this header is that it cannot hold another button.
    expect(header(app.panel).textContent ?? '').not.toContain('Upload')

    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    const menu = document.querySelector('[data-testid="files-overflow-menu"]')
    expect(menu).not.toBeNull()
    expect(menu?.textContent).toContain('Upload File')
  })

  // ── The four combinations of the upload rule (nocx-9le.5.24) ───────────
  // The overflow draws nothing when it would open empty, so on a local tab
  // the BUTTON's presence is the item's presence — which makes the two
  // local cases the sharpest statement of the rule this header follows.
  it('offers Upload on a LOCAL tab in a BROWSER, because the bytes are here and the shell is not', async () => {
    // The defect this replaces: the header said "no uploader" while a drop
    // on the same tab uploaded. A `File` is bytes on the browser's machine
    // and the tab's shell is on the backend's, so there is somewhere to
    // send it.
    const u = uploadFixture()
    const { panel } = await mountApp(fakeServices(), undefined, {
      upload: u.surface,
      native: () => false,
    })
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-overflow"]')).not.toBeNull(),
    )
    panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    expect(document.querySelector('[data-testid="files-overflow-menu"]')?.textContent).toContain(
      'Upload File',
    )
  })

  it('offers no Upload on a LOCAL tab in the WAILS WINDOW, where the file is already there', async () => {
    // The one absence, stated as absence and not as a greyed-out row — the
    // same rule "Show in Finder" follows in the opposite direction.
    const u = uploadFixture()
    const { panel } = await mountApp(fakeServices(), undefined, {
      upload: u.surface,
      native: () => true,
    })
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-panel"]')).not.toBeNull(),
    )
    expect(panel.querySelector('[data-testid="files-overflow"]')).toBeNull()
  })

  it('offers Upload on a REMOTE tab in the WAILS WINDOW too', async () => {
    // The ticket names a file on the backend's machine and the shell is
    // elsewhere: the transfer is real, so the item belongs.
    const app = await mountOnRemote({ native: () => true })
    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    expect(document.querySelector('[data-testid="files-overflow-menu"]')?.textContent).toContain(
      'Upload File',
    )
  })

  it('sends the chosen files into the folder the panel is showing', async () => {
    const app = await mountOnRemote({
      pickSources: () =>
        Promise.resolve([{ name: 'notes.txt', size: 5, blob: new Blob([new Uint8Array(5)]) }]),
    })
    app.services.nextResult = [{ transferId: 't1' }]

    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    menuItem('Upload File').click()

    await vi.waitFor(() => expect(app.services.uploads).toHaveLength(1))
    expect(app.services.uploads[0]).toEqual({
      bindingId: 'b1',
      destDir: '/srv',
      name: 'notes.txt',
      size: 5,
    })
  })

  it('draws no transfer rows at all — the operations indicator owns that list', async () => {
    // The deliberate absence, and an untested absence is not one. The panel
    // used to be the ONLY place a running transfer could be seen, which is
    // the defect nocx-hbdw4 exists to fix: switch sidebar view or collapse
    // the panel and a 2 GB upload was invisible and uncancellable. A
    // contextual copy here would also have to answer "which transfers
    // belong to this panel", and the store has no bindingId to answer it
    // with.
    const app = await mountOnRemote({
      pickSources: () =>
        Promise.resolve([{ name: 'big.iso', size: 400, blob: new Blob([new Uint8Array(400)]) }]),
    })
    app.services.nextResult = [{ transferId: 't1' }]
    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    menuItem('Upload File').click()

    // The transfer really started — the panel's action still reaches the
    // flow — and the panel still draws nothing about it.
    await vi.waitFor(() => expect(app.services.uploads).toHaveLength(1))
    expect(app.store.transfers()).toHaveLength(1)
    expect(app.panel.querySelector('.ui-operation-row')).toBeNull()
    expect(app.panel.textContent).not.toContain('big.iso')
  })
})

// ── Upload from a ROW (nocx-9le.5.21) ────────────────────────────────────
//
// AGENTS.md rule 1: a user right-clicks a folder in the tree, picks Upload,
// chooses files, and they arrive in THAT folder — not in the folder the panel
// happens to be showing, which is the header action's derivation and the one
// the panel keeps.
//
// This is not the drop-on-a-folder-row design §4 refuses: that refused a
// GESTURE, where the folder under a dragged pointer is a guess about what the
// person meant. A row they right-clicked is an explicit choice.
describe('uploading into the row you right-clicked', () => {
  function uploadFixture() {
    const services = fakeUploadServices()
    const store = createUploadStore({ services })
    const flow = createUploadFlow({
      services,
      store,
      ask: () => Promise.resolve({ answer: 'skip', applyToAll: false }),
      report: () => {},
    })
    const surface: UploadSurface = { services, store, flow }
    return { services, store, flow, surface }
  }

  /** A tree with TWO folders: the one the panel reveals (/srv) and another
   *  (/opt). The pair is the whole point — a test whose only folder is the
   *  revealed one cannot tell "the row" from "the panel's folder", which is
   *  exactly the confusion this item must not have. */
  const twoFolders = () =>
    fakeServices({
      list: vi
        .fn()
        .mockImplementation((_b: string, path: string) =>
          Promise.resolve(
            path === '/'
              ? listFixture('C:/', [
                  entryFixture({ name: 'opt', path: '/opt', kind: 'dir' }),
                  entryFixture({ name: 'srv', path: '/srv', kind: 'dir' }),
                  entryFixture({ name: 'notes.md', path: '/notes.md' }),
                ])
              : listFixture(`C:${path}`, []),
          ),
        ),
    })

  const rowMenuItems = () =>
    [
      ...document.querySelectorAll<HTMLElement>(
        '[data-testid="files-context-menu"] [role="menuitem"]',
      ),
    ].map((i) => i.textContent)

  function rowMenuItem(label: string): HTMLElement {
    const items = document.querySelectorAll<HTMLElement>(
      '[data-testid="files-context-menu"] [role="menuitem"]',
    )
    for (const item of items) if ((item.textContent ?? '').includes(label)) return item
    throw new Error(`no menu item named ${label}`)
  }

  async function mountOnRemote(
    over: { pickSources?: () => Promise<UploadSource[]>; native?: () => boolean } = {},
  ) {
    const u = uploadFixture()
    const app = await mountApp(twoFolders(), undefined, {
      upload: u.surface,
      pickSources: over.pickSources,
      native: over.native,
    })
    app.setActiveOrigin({ ...SSH_ORIGIN, cwd: '/srv', cwdVerified: true })
    // Wait for the RESCOPE and the reveal both: the panel's own folder is
    // /srv, and the assertions below are about a row that is not it.
    await vi.waitFor(() => expect(rowNamed(app.panel, 'opt')).not.toBeUndefined())
    await vi.waitFor(() =>
      expect(app.panel.querySelector('[data-selected="true"]')?.textContent).toContain('srv'),
    )
    return { ...app, ...u }
  }

  it('sends the chosen files into THAT directory, not the one the panel is showing', async () => {
    const app = await mountOnRemote({
      pickSources: () =>
        Promise.resolve([{ name: 'notes.txt', size: 5, blob: new Blob([new Uint8Array(5)]) }]),
    })
    app.services.nextResult = [{ transferId: 't1' }]

    // The panel is showing /srv. The row is /opt.
    fireEvent.contextMenu(rowNamed(app.panel, 'opt'), { clientX: 10, clientY: 10 })
    rowMenuItem('Upload').click()

    await vi.waitFor(() => expect(app.services.uploads).toHaveLength(1))
    expect(app.services.uploads[0]).toEqual({
      bindingId: 'b1',
      destDir: '/opt',
      name: 'notes.txt',
      size: 5,
    })
  })

  // ── The four combinations of the upload rule (nocx-9le.5.24) ───────────
  //
  // The rule is `uploadMovesTheFile` and it has exactly one `false`. An
  // untested absence is not an absence — a test that only checks presence
  // cannot catch the row leaking onto the tab where it must not be — and an
  // untested PRESENCE is what shipped the defect this replaces: the menu
  // read `kind === 'ssh'`, which is a second answer to the question the
  // drop handler already answers, and the two disagreed about exactly the
  // browser-on-a-local-tab case below.
  //
  // The menu is walked with the environment fixed and the tab's origin
  // moved, because that is the pair the rule turns on.
  async function mountBoth(native: boolean) {
    const u = uploadFixture()
    const app = await mountApp(twoFolders(), undefined, {
      upload: u.surface,
      native: () => native,
    })
    await vi.waitFor(() => expect(rowNamed(app.panel, 'opt')).not.toBeUndefined())
    return app
  }

  /** Open the row menu on /opt, read it, and close it again. */
  async function itemsOnOpt(
    app: Awaited<ReturnType<typeof mountBoth>>,
  ): Promise<(string | null)[]> {
    fireEvent.contextMenu(rowNamed(app.panel, 'opt'), { clientX: 10, clientY: 10 })
    const items = rowMenuItems()
    fireEvent.pointerDown(document.body)
    await vi.waitFor(() =>
      expect(document.querySelector('[data-testid="files-context-menu"]')).toBeNull(),
    )
    return items
  }

  /** Move the panel onto the ssh tab and wait for the reveal to land. */
  async function goRemote(app: Awaited<ReturnType<typeof mountBoth>>): Promise<void> {
    app.setActiveOrigin({ ...SSH_ORIGIN, cwd: '/srv', cwdVerified: true })
    await vi.waitFor(() =>
      expect(app.panel.querySelector('[data-selected="true"]')?.textContent).toContain('srv'),
    )
  }

  it('in a BROWSER is present on a local tab and on a remote one', async () => {
    // Local first, and it is the case that was wrong: a `File` is bytes on
    // the browser's machine, the tab's shell is on the backend's, and the
    // drop on this very tab uploads.
    const app = await mountBoth(false)
    expect(await itemsOnOpt(app)).toEqual([
      'Copy Relative Path',
      'Copy Absolute Path',
      'Upload…',
      'Show in Finder',
    ])

    await goRemote(app)
    expect(await itemsOnOpt(app)).toEqual(['Copy Relative Path', 'Copy Absolute Path', 'Upload…'])
  })

  it('in the WAILS WINDOW is absent on a local tab and present on a remote one', async () => {
    // The one absence: the runtime hands Go a path for a file already on
    // that machine, so there is nowhere to send it and the drop inserts the
    // path instead.
    const app = await mountBoth(true)
    expect(await itemsOnOpt(app)).toEqual([
      'Copy Relative Path',
      'Copy Absolute Path',
      'Show in Finder',
    ])

    await goRemote(app)
    expect(await itemsOnOpt(app)).toEqual(['Copy Relative Path', 'Copy Absolute Path', 'Upload…'])
  })

  it('offers nothing on a FILE row — a file is not a place to put a file', async () => {
    const app = await mountOnRemote()
    fireEvent.contextMenu(rowNamed(app.panel, 'notes.md'), { clientX: 10, clientY: 10 })
    expect(rowMenuItems()).toEqual(['Copy Relative Path', 'Copy Absolute Path'])
  })

  it('offers nothing where no upload surface was injected', async () => {
    // A row that reached nothing is worse than no row: the panel degrades by
    // not offering the action, never by offering one that goes nowhere.
    const app = await mountApp(twoFolders())
    app.setActiveOrigin({ ...SSH_ORIGIN, cwd: '/srv', cwdVerified: true })
    await vi.waitFor(() => expect(rowNamed(app.panel, 'opt')).not.toBeUndefined())
    fireEvent.contextMenu(rowNamed(app.panel, 'opt'), { clientX: 10, clientY: 10 })
    expect(rowMenuItems().some((l) => (l ?? '').includes('Upload'))).toBe(false)
  })

  it('carries no Download row — nocx-9le.8 has not started', async () => {
    // A menu item that does nothing, or says "coming soon", is a feature
    // that does not exist surviving a release. Download joins this menu with
    // its own epic or not at all.
    const app = await mountOnRemote()
    fireEvent.contextMenu(rowNamed(app.panel, 'opt'), { clientX: 10, clientY: 10 })
    expect(rowMenuItems().some((l) => /download/i.test(l ?? ''))).toBe(false)
  })

  it('picking nothing sends nothing', async () => {
    // A cancelled picker is an answer, and the answer is "no files".
    const app = await mountOnRemote({ pickSources: () => Promise.resolve([]) })
    fireEvent.contextMenu(rowNamed(app.panel, 'opt'), { clientX: 10, clientY: 10 })
    rowMenuItem('Upload').click()
    await Promise.resolve()
    expect(app.services.uploads).toHaveLength(0)
  })
})

// ── The Download item (nocx-9le.8.3) ──────────────────────────────────────
//
// What a person can do that they could not before: right-click a file on
// the machine their tab is on, pick Download, and get the file — with the
// transfer showing in the operations list, cancellable, wherever they are.
// The backend and the wire landed in nocx-9le.8.1 and nothing reached them;
// this is the half that does.
//
// THE ABSENCES ARE THE OTHER HALF, and they are tested in all four
// combinations of tab kind and build rather than in the one that is easy to
// reach. Absence is how the panel says a capability does not apply to a
// machine — never a greyed-out row — and an untested absence is not a rule,
// it is a coincidence that currently holds.
describe('downloading a file from the tree', () => {
  function downloadFixture() {
    const services = fakeDownloadServices()
    const store = createDownloadStore({ services })
    const saver = fakeSaver()
    const said: string[] = []
    const flow = createDownloadFlow({
      services,
      store,
      saver,
      report: (m) => said.push(m),
    })
    const surface: DownloadSurface = { services, store, flow }
    return { services, store, flow, saver, surface, said }
  }

  /** A root holding one file and one folder, so "only on a file" has
   *  something to be false about. */
  const oneFileOneFolder = () =>
    fakeServices({
      list: vi
        .fn()
        .mockImplementation((_b: string, path: string) =>
          Promise.resolve(
            path === '/'
              ? listFixture('C:/', [
                  entryFixture({ name: 'srv', path: '/srv', kind: 'dir' }),
                  entryFixture({ name: 'notes.md', path: '/notes.md' }),
                ])
              : listFixture(`C:${path}`, []),
          ),
        ),
    })

  const menuLabels = () =>
    [
      ...document.querySelectorAll<HTMLElement>(
        '[data-testid="files-context-menu"] [role="menuitem"]',
      ),
    ].map((i) => i.textContent ?? '')

  function menuRow(label: string): HTMLElement {
    const items = document.querySelectorAll<HTMLElement>(
      '[data-testid="files-context-menu"] [role="menuitem"]',
    )
    for (const item of items) if ((item.textContent ?? '').includes(label)) return item
    throw new Error(`no menu item named ${label}`)
  }

  /** Mount on a tab of the given kind, inside or outside the Wails window,
   *  and open the menu on `notes.md`. Returns the fixture so a test can ask
   *  what reached the wire. */
  async function openMenuOn(
    kind: 'local' | 'ssh',
    native: boolean,
    row = 'notes.md',
    over: { download?: DownloadSurface } = {},
  ) {
    const d = over.download === undefined ? downloadFixture() : null
    const surface = over.download ?? d!.surface
    const app = await mountApp(oneFileOneFolder(), undefined, {
      download: surface,
      native: () => native,
    })
    if (kind === 'ssh') app.setActiveOrigin(SSH_ORIGIN)
    await vi.waitFor(() => expect(rowNamed(app.panel, row)).not.toBeUndefined())
    fireEvent.contextMenu(rowNamed(app.panel, row), { clientX: 10, clientY: 10 })
    return { ...app, ...(d ?? {}) }
  }

  it('offers Download in a browser on an SSH tab', async () => {
    await openMenuOn('ssh', false)
    expect(menuLabels().some((l) => l.includes('Download'))).toBe(true)
  })

  it('offers Download in a browser on a LOCAL tab — the file is on the backend`s machine', async () => {
    // The row that is easy to get wrong. "Local" names the BACKEND's
    // machine; the person is in a browser somewhere else and cannot reach
    // those bytes any other way. Getting this wrong is what the upload rule
    // did before nocx-9le.5.24 corrected it.
    await openMenuOn('local', false)
    expect(menuLabels().some((l) => l.includes('Download'))).toBe(true)
  })

  it('offers Download in the Wails window on an SSH tab', async () => {
    await openMenuOn('ssh', true)
    expect(menuLabels().some((l) => l.includes('Download'))).toBe(true)
  })

  it('offers NO Download in the Wails window on a local tab, and Show in Finder instead', async () => {
    // The one combination where the file is already on the disk the window
    // is running from. The item is ABSENT, not disabled: a greyed-out
    // Download would be a promise the product cannot keep, and the action
    // that DOES apply to that machine is in the same menu.
    await openMenuOn('local', true)
    expect(menuLabels().some((l) => l.includes('Download'))).toBe(false)
    expect(menuLabels().some((l) => l.includes('Show in Finder'))).toBe(true)
  })

  it('offers no Download on a FOLDER, on a tab where a file would have one', async () => {
    // A directory download is a second thing nobody has specified — an
    // archive, a recursive walk, a question about symlinks.
    await openMenuOn('ssh', false, 'srv')
    expect(menuLabels().some((l) => l.includes('Download'))).toBe(false)
  })

  it('offers no Download when the panel was given no download surface', async () => {
    // An item that reaches nothing is worse than no item.
    const app = await mountApp(oneFileOneFolder(), undefined, { native: () => false })
    app.setActiveOrigin(SSH_ORIGIN)
    await vi.waitFor(() => expect(rowNamed(app.panel, 'notes.md')).not.toBeUndefined())
    fireEvent.contextMenu(rowNamed(app.panel, 'notes.md'), { clientX: 10, clientY: 10 })
    expect(menuLabels().some((l) => l.includes('Download'))).toBe(false)
  })

  it('picking it names the file on the wire and hands the browser the URL', async () => {
    const app = await openMenuOn('ssh', false)
    app.services!.nextResult.push(downloadResultFixture({ name: 'notes.md', size: 12 }))
    menuRow('Download').click()

    await vi.waitFor(() => expect(app.services!.downloads).toHaveLength(1))
    expect(app.services!.downloads[0]).toEqual({ bindingId: 'b1', path: '/notes.md' })
    // The renderer named no destination — it could not, and the saver was
    // handed the URL the backend minted.
    await vi.waitFor(() => expect(app.saver!.saved).toHaveLength(1))
    expect(app.saver!.saved[0]).toContain('/download/')
  })

  it('records WHICH MACHINE the file came from, off the origin the panel follows', async () => {
    // The operations list is global, so the row is read out of the context
    // of the tab that started the work. The panel does not derive the
    // string — machine-name.ts already answered it in the composition root
    // and it rides the origin — and it must not reach the WIRE, which
    // already knows which host a binding names.
    const app = await openMenuOn('ssh', false)
    app.services!.nextResult.push(downloadResultFixture({ name: 'notes.md', size: 12 }))
    menuRow('Download').click()

    await vi.waitFor(() => expect(app.store!.transfers()).toHaveLength(1))
    expect(app.store!.transfers()[0].machine).toBe('alice@srv-01')
    expect(app.services!.downloads[0]).toEqual({ bindingId: 'b1', path: '/notes.md' })
  })

  it('the transfer appears in the download store, which is what the operations list reads', async () => {
    const app = await openMenuOn('ssh', false)
    app.services!.nextResult.push(downloadResultFixture({ name: 'notes.md', size: 12 }))
    menuRow('Download').click()

    await vi.waitFor(() => expect(app.store!.transfers()).toHaveLength(1))
    expect(app.store!.transfers()[0]).toMatchObject({
      name: 'notes.md',
      sourcePath: '/notes.md',
      size: 12,
      phase: 'running',
    })
  })

  it('a refused download is reported and leaves no half-started row', async () => {
    const app = await openMenuOn('ssh', false)
    // Nothing queued: the fake rejects, the way a closed binding does.
    menuRow('Download').click()
    await vi.waitFor(() => expect(app.said!).toHaveLength(1))
    expect(app.said![0]).toContain('Could not download /notes.md')
    expect(app.store!.transfers()).toEqual([])
  })
})

// ── The filter (nocx-708q.2) ──────────────────────────────────────────────
//
// The Files panel had no filter while Ports, Git, Settings and quick-connect
// all had one, and the file-manager design put a name filter in scope. This
// is the second panel to ship without the filter its design promised
// (nocx-52by was the first), so what these assert is not only that it
// filters but that it filters the way Git's does — and the one property
// that separates a filter from a new listing.
describe('filtering the tree by name', () => {
  /** A root with two folders and a file, where each folder holds a file. It
   *  takes a nested match to tell "narrows" from "collapses". */
  const nestedTree = () =>
    fakeServices({
      list: vi
        .fn()
        .mockImplementation((_b: string, path: string) =>
          Promise.resolve(
            path === '/'
              ? listFixture('C:/', [
                  entryFixture({ name: 'src', path: '/src', kind: 'dir' }),
                  entryFixture({ name: 'docs', path: '/docs', kind: 'dir' }),
                  entryFixture({ name: 'notes.md', path: '/notes.md' }),
                ])
              : path === '/src'
                ? listFixture('C:/src', [
                    entryFixture({ name: 'button.tsx', path: '/src/button.tsx' }),
                  ])
                : listFixture(`C:${path}`, []),
          ),
        ),
    })

  const box = (panel: HTMLElement): HTMLInputElement => {
    const el = panel.querySelector<HTMLInputElement>(
      '[data-testid="files-filter"] .ui-search-field__input',
    )
    if (!el) throw new Error('no filter field')
    return el
  }

  const shown = (panel: HTMLElement): string[] =>
    rowsOf(panel).map((r) => r.textContent?.trim() ?? '')

  async function mountWithTree() {
    const app = await mountApp(nestedTree())
    await vi.waitFor(() => expect(rowNamed(app.panel, 'notes.md')).not.toBeUndefined())
    return app
  }

  it('is the kit`s SearchField, not a hand-rolled input', () => {
    // A surface may place a kit component and may never draw its own. The
    // identity class is what says which one is here.
    return mountWithTree().then(({ panel }) => {
      const field = panel.querySelector('[data-testid="files-filter"] .ui-search-field')
      expect(field).not.toBeNull()
      expect(box(panel).getAttribute('aria-label')).toBe('Filter files by name')
    })
  })

  it('narrows the rows to the names that match', async () => {
    const { panel } = await mountWithTree()
    expect(shown(panel)).toHaveLength(3)
    fireEvent.input(box(panel), { target: { value: 'notes' } })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(1))
    expect(shown(panel)[0]).toContain('notes.md')
  })

  it('DOES NOT COLLAPSE what the person expanded, and shows the match under it', async () => {
    // The property that makes this a filter. A filter that collapsed the
    // tree and re-expanded the matches would throw away the person's own
    // work of opening four levels.
    const { panel } = await mountWithTree()
    rowNamed(panel, 'src').click()
    await vi.waitFor(() => expect(rowNamed(panel, 'button.tsx')).not.toBeUndefined())

    fireEvent.input(box(panel), { target: { value: 'button' } })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(2))
    // The match, and the folder it is in — an indented row with no parent
    // above it is a lie about where the file lives.
    expect(shown(panel)[0]).toContain('src')
    expect(shown(panel)[1]).toContain('button.tsx')
    // Still expanded, because nothing wrote to the store.
    expect(
      rowNamed(panel, 'src').querySelector('[aria-expanded]')?.getAttribute('aria-expanded'),
    ).toBe('true')
  })

  it('clearing RESTORES the view rather than resetting it', async () => {
    const { panel } = await mountWithTree()
    rowNamed(panel, 'src').click()
    await vi.waitFor(() => expect(rowNamed(panel, 'button.tsx')).not.toBeUndefined())
    const before = shown(panel)

    fireEvent.input(box(panel), { target: { value: 'button' } })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(2))
    fireEvent.input(box(panel), { target: { value: '' } })

    // Exactly what was there: the same rows, in the same order, with src
    // still open. A re-listing would have to re-fetch and would lose it.
    await vi.waitFor(() => expect(shown(panel)).toEqual(before))
  })

  it('Escape drops the filter, the way the Git panel`s does', async () => {
    const { panel } = await mountWithTree()
    fireEvent.input(box(panel), { target: { value: 'notes' } })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(1))
    fireEvent.keyDown(box(panel), { key: 'Escape' })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(3))
  })

  it('a filter that matches nothing is a state with a way out, never a blank tree', async () => {
    const { panel } = await mountWithTree()
    fireEvent.input(box(panel), { target: { value: 'zzzz' } })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(0))

    const clear = panel.querySelector<HTMLElement>('[data-testid="files-filter-clear"]')
    expect(clear).not.toBeNull()
    clear!.click()
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(3))
  })

  it('offers no search-scope toggle, because there is only one scope', async () => {
    // There WAS a Names/Contents toggle here, from the file-manager design's
    // Orca reference. Content search is not built, so its other half was
    // permanently disabled — and a two-option control with one option is not
    // a control. In a ~250 px rail it also took a third of the row and broke
    // the layout, and the owner had not asked for it. If content search is
    // ever built the toggle returns with it; until then its absence is the
    // honest state and this test is what keeps it from drifting back.
    const app = await mountWithTree()
    expect(app.panel.querySelector('.ui-segmented-control')).toBeNull()
    expect(app.panel.querySelector('.ui-search-field')).not.toBeNull()
  })

  it('the filter survives the panel being swapped out and back', async () => {
    // It lives in the store, the way the Git panel's does: the panel
    // unmounts whenever another sidebar view is in front, and a filter that
    // evaporated on a glance at Ports would be a control nobody can rely on.
    const { panel, bar } = await mountWithTree()
    fireEvent.input(box(panel), { target: { value: 'notes' } })
    await vi.waitFor(() => expect(shown(panel)).toHaveLength(1))

    filesIcon(bar).click()
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(true))
    filesIcon(bar).click()
    await vi.waitFor(() => expect(panel.classList.contains('collapsed')).toBe(false))
    await vi.waitFor(() => expect(box(panel).value).toBe('notes'))
  })
})
