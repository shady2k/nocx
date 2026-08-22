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
}

const liveHandles: SidebarHandle[] = []

async function mountApp(
  services: FilesPanelServices,
  clipboard?: ClipboardAccess,
  uploadDeps?: {
    upload: UploadSurface
    pickSources?: () => Promise<UploadSource[]>
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
    pickSources: uploadDeps?.pickSources,
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
  async function mountOnRemote(over: { pickSources?: () => Promise<UploadSource[]> } = {}) {
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

  it('offers no Upload on a LOCAL tab, because a local binding has no uploader', async () => {
    // R1 stated as absence, not as a greyed-out row — the same rule "Show
    // in Finder" follows in the opposite direction.
    const u = uploadFixture()
    const { panel } = await mountApp(fakeServices(), undefined, { upload: u.surface })
    await vi.waitFor(() =>
      expect(panel.querySelector('[data-testid="files-panel"]')).not.toBeNull(),
    )
    expect(panel.querySelector('[data-testid="files-overflow"]')).toBeNull()
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

  it('shows the transfer it started, and what it knows about it', async () => {
    const app = await mountOnRemote({
      pickSources: () =>
        Promise.resolve([{ name: 'big.iso', size: 400, blob: new Blob([new Uint8Array(400)]) }]),
    })
    app.services.nextResult = [{ transferId: 't1' }]
    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    menuItem('Upload File').click()

    const row = () => app.panel.querySelector('[data-testid="files-upload-row"]')
    await vi.waitFor(() => expect(row()).not.toBeNull())
    expect(row()?.getAttribute('data-phase')).toBe('running')
    // No sample has arrived, so the line says the size and NOT "0 B of
    // 400 B", which would claim the transfer had stalled at zero.
    expect(app.panel.querySelector('[data-testid="files-upload-progress"]')?.textContent).toBe(
      '400 B',
    )

    app.services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'big.iso',
      stranded: [],
    })
    await vi.waitFor(() => expect(row()?.getAttribute('data-phase')).toBe('written'))
    expect(row()?.textContent).toContain('Uploaded')

    // And the person can put it away.
    app.panel.querySelector<HTMLElement>('[data-testid="files-upload-dismiss"]')!.click()
    await vi.waitFor(() => expect(row()).toBeNull())
  })

  it('cancels a running transfer through the wire, not by deciding locally', async () => {
    const app = await mountOnRemote({
      pickSources: () =>
        Promise.resolve([{ name: 'a.txt', size: 1, sourceTicket: 'c'.repeat(32) }]),
    })
    app.services.nextResult = [{ transferId: 't1' }]
    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    menuItem('Upload File').click()
    await vi.waitFor(() =>
      expect(app.panel.querySelector('[data-testid="files-upload-cancel"]')).not.toBeNull(),
    )

    app.panel.querySelector<HTMLElement>('[data-testid="files-upload-cancel"]')!.click()
    expect(app.services.cancels).toEqual(['t1'])
    // Still running: the cancel races the transfer's own completion, and
    // uploadDone is what says which won.
    expect(
      app.panel.querySelector('[data-testid="files-upload-row"]')?.getAttribute('data-phase'),
    ).toBe('running')
  })

  it('does not call a 409 a failure — the row says it is waiting and still offers the cancel', async () => {
    // The transfer is ALIVE: another claimant's body is running for this
    // ticket. A row that said "Failed" would announce it dead and take
    // away the only control that can still stop it.
    const app = await mountOnRemote({
      pickSources: () =>
        Promise.resolve([{ name: 'big.iso', size: 400, blob: new Blob([new Uint8Array(400)]) }]),
    })
    app.services.nextResult = [{ transferId: 't1', ticket: 'tk', url: '/upload/tk' }]
    app.services.nextSendBody = [{ ok: false, kind: 'status', status: 409 }]
    app.panel.querySelector<HTMLElement>('[data-testid="files-overflow"]')!.click()
    menuItem('Upload File').click()

    const row = () => app.panel.querySelector('[data-testid="files-upload-row"]')
    await vi.waitFor(() => expect(row()?.getAttribute('data-phase')).toBe('unsettled'))
    expect(row()?.textContent).toContain('Waiting for the server')
    expect(row()?.textContent).not.toContain('Failed')

    const cancel = () => app.panel.querySelector<HTMLElement>('[data-testid="files-upload-cancel"]')
    expect(cancel()).not.toBeNull()
    cancel()!.click()
    expect(app.services.cancels).toEqual(['t1'])

    app.services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'big.iso',
      stranded: [],
    })
    await vi.waitFor(() => expect(row()?.getAttribute('data-phase')).toBe('written'))
    expect(row()?.textContent).toContain('Uploaded')
  })
})
