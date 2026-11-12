// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import type { NameColourDraft } from './name-colour-dialog'
import { resetSessionCounter, mountPaneManager } from './test-support/panes-fixtures'
import { HorizontalTabStrip, VerticalTabStrip } from './tab-strip'

// A window is a VIEWPORT, not a container (tabs/panes design §10): it shows
// one workspace at a time, and the chip is that sentence in the UI. These
// tests are about the two halves of it — which tabs the window draws, and
// what the chip says about which workspace they are in — plus the rule the
// whole epic turns on: THE DEFAULT WORKSPACE NEVER RENDERS, and creating a
// second workspace does not change that.

const showConfirmMock = vi.fn()
const workspaceCreateMock = vi.fn()
const workspaceEditMock = vi.fn()
const tabEditMock = vi.fn()
// PARTIAL: name-colour-dialog.tsx renders the kit's Dialog, so a wholesale
// mock of this module leaves it without one.
vi.mock('./ui/dialog', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./ui/dialog')>()),
  showConfirm: (...args: unknown[]) => showConfirmMock(...args) as Promise<boolean>,
}))

// Naming and colouring are one form now, for both subjects (nocx-2mipw.2).
// The tests drive it through one mock and read back what was asked for.
vi.mock('./name-colour-dialog', () => ({
  showWorkspaceCreateDialog: (...args: unknown[]) =>
    workspaceCreateMock(...args) as Promise<NameColourDraft | null>,
  showWorkspaceEditDialog: (...args: unknown[]) =>
    workspaceEditMock(...args) as Promise<NameColourDraft | null>,
  showTabEditDialog: (...args: unknown[]) =>
    tabEditMock(...args) as Promise<NameColourDraft | null>,
}))

const DEFAULT_WS = 'workspace:default'
const TAB = '.nocx-tab'
const CHIP = '.nocx-workspace-chip .ui-button'

const tabTitles = (root: HTMLElement): string[] =>
  [...root.querySelectorAll(TAB)].map(
    (el) => el.querySelector('.nocx-tab-title')?.textContent ?? '',
  )

const chip = (root: HTMLElement): HTMLElement | null => root.querySelector(CHIP)

/** The tabs a person can actually see: a folded workspace's rows are in the
 *  row and hidden (workspaces UX rework), not removed. */
const visibleTabs = (root: HTMLElement): HTMLElement[] =>
  [...root.querySelectorAll<HTMLElement>(TAB)].filter((el) => el.dataset.hidden !== 'true')

/** A workspace pill's own actions. A CLICK on the pill switches to that
 *  workspace now — one click is the whole of switching — so the actions live
 *  where a row's actions live: the context menu. */
const switcherRows = (root: HTMLElement): HTMLElement[] => {
  const pill = chip(root)
  if (!pill) return []
  pill.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
  return [...document.querySelectorAll<HTMLElement>('.ui-context-menu__item')]
}

describe('the window shows one workspace at a time (nocx-isoph.5)', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
    workspaceCreateMock.mockResolvedValue({ name: 'refactor-auth', colour: 'blue' })
    workspaceEditMock.mockResolvedValue({ name: 'refactor-auth', colour: 'blue' })
    tabEditMock.mockResolvedValue({ name: 'refactor-auth', colour: null })
    showConfirmMock.mockResolvedValue(true)
  })

  /** One tab in the default workspace, and one workspace of its own beside
   *  it — the state every assertion below is about. */
  async function twoWorkspaces() {
    const mounted = await mountPaneManager()
    const { manager, bar } = mounted
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(1))

    await manager.newWorkspace()
    await vi.waitFor(() => expect(manager.currentWorkspaceId()).not.toBe(DEFAULT_WS))
    return mounted
  }

  it('creates a workspace together with its first tab, and shows it', async () => {
    const { manager, bar, backend } = await twoWorkspaces()

    expect(workspaceCreateMock).toHaveBeenCalled()
    const rows = backend.rows()
    const workspace = rows.workspaces.find((w) => w.id !== DEFAULT_WS)!
    expect(workspace.name).toBe('refactor-auth')
    // Creation is always creation-with-content: the workspace arrived with a
    // tab, and that tab with a pane.
    const member = rows.tabs.find((t) => t.workspaceId === workspace.id)!
    expect(rows.panes.some((p) => p.tabId === member.id)).toBe(true)
    expect(manager.activeTabPanel()).toEqual({ view: '', collapsed: true })
    await vi.waitFor(() =>
      expect(backend.rows().tabs.find((tab) => tab.id === member.id)).toMatchObject({
        panelView: '',
        panelCollapsed: true,
      }),
    )
    expect(manager.currentWorkspaceId()).toBe(workspace.id)
    // THE HORIZONTAL STRIP SHOWS THE CURRENT WORKSPACE'S TABS AND FOLDS THE
    // NAMED REST. The default's rows are top-level (§4.2 — it draws no pill,
    // so folding them would put them out of reach), which is why the row
    // holds two visible tabs here and not one: the default's, and the new
    // workspace's.
    expect(visibleTabs(bar)).toHaveLength(2)
    const made = bar.querySelector<HTMLElement>(`${TAB}[aria-selected="true"]`)
    expect(made?.dataset.hidden).not.toBe('true')
  })

  it('does not create anything when the person cancels the name', async () => {
    workspaceCreateMock.mockResolvedValue(null)
    const { manager, backend } = await mountPaneManager()

    await manager.newWorkspace()

    expect(backend.rows().workspaces.filter((w) => w.id !== DEFAULT_WS)).toEqual([])
    expect(manager.currentWorkspaceId()).toBe(DEFAULT_WS)
  })

  it('clicking a workspace pill switches to it, and its tabs are the ones on show', async () => {
    // ONE CLICK IS THE WHOLE OF SWITCHING now: every workspace is a pill IN
    // the row, and the one holding the current tab shows its tabs while the
    // rest are folded to their pill. There is no dropdown in the path.
    const { manager, bar } = await mountPaneManager()
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(1))
    await manager.newWorkspace()
    await vi.waitFor(() => expect(manager.currentWorkspaceId()).not.toBe(DEFAULT_WS))
    const made = manager.currentWorkspaceId()

    // A second named workspace, so there is one to fold.
    workspaceCreateMock.mockResolvedValue({ name: 'second', colour: 'red' })
    await manager.newWorkspace()
    await vi.waitFor(() => expect(manager.currentWorkspaceId()).not.toBe(made))
    // Three rows exist — the default's and one per named workspace — and two
    // of them are on show: the default's, which is never folded (§4.2: it
    // draws no pill, so folding it would put its tabs out of reach), and the
    // current workspace's. The third is folded to its pill.
    await vi.waitFor(() => expect(bar.querySelectorAll(TAB)).toHaveLength(3))
    expect(visibleTabs(bar)).toHaveLength(2)

    // Clicking that pill is the whole of switching: no menu in the path.
    const pill = [...bar.querySelectorAll<HTMLElement>(CHIP)].find((el) =>
      el.textContent?.includes('refactor-auth'),
    )!
    pill.click()

    await vi.waitFor(() => expect(manager.currentWorkspaceId()).toBe(made))
    expect(visibleTabs(bar)).toHaveLength(2)
  })

  it('gives the default workspace no pill at all, whatever else exists', async () => {
    // §4.2 at the seam a person touches: the default renders NO chrome — not
    // an unnamed pill, not an empty one — and the arrival of another
    // workspace does not give it any. The pill that appears belongs to the
    // workspace that was made, and it wears that workspace's name.
    const { manager, bar } = await mountPaneManager()
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(1))
    expect(chip(bar)).toBeNull()

    await manager.newWorkspace()
    await vi.waitFor(() => expect(manager.currentWorkspaceId()).not.toBe(DEFAULT_WS))

    const pills = [...bar.querySelectorAll<HTMLElement>(CHIP)]
    expect(pills).toHaveLength(1)
    expect(pills[0].textContent).toContain('refactor-auth')
  })

  it('offers to close a workspace from its pill, and the default has none to offer', async () => {
    const { bar } = await twoWorkspaces()

    expect(switcherRows(bar).map((el) => el.textContent)).toContain('Close workspace')

    // Nothing about the default can be asked for, because the default draws
    // nothing to ask it from.
    expect([...bar.querySelectorAll(CHIP)]).toHaveLength(1)
  })

  it('asks before closing a workspace, and then takes the whole container', async () => {
    const { manager, backend } = await twoWorkspaces()
    const id = manager.currentWorkspaceId()

    await manager.closeWorkspaceById(manager.currentWorkspaceId())

    expect(showConfirmMock).toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(backend.rows().workspaces.map((w) => w.id)).not.toContain(id)
    })
    // Back where a window with no workspace of its own is: the default.
    expect(manager.currentWorkspaceId()).toBe(DEFAULT_WS)
  })

  it('leaves the workspace standing when the person says no', async () => {
    showConfirmMock.mockResolvedValue(false)
    const { manager, backend } = await twoWorkspaces()
    const id = manager.currentWorkspaceId()

    await manager.closeWorkspaceById(manager.currentWorkspaceId())

    expect(backend.rows().workspaces.map((w) => w.id)).toContain(id)
    expect(manager.currentWorkspaceId()).toBe(id)
  })

  it('refuses to close the default workspace without even asking', async () => {
    const { manager } = await mountPaneManager()

    await manager.closeWorkspaceById(manager.currentWorkspaceId())

    expect(showConfirmMock).not.toHaveBeenCalled()
  })

  it('draws every workspace in the vertical strip, and heads only the named ones', async () => {
    // The vertical strip is the surface you look at coming back from lunch:
    // all workspaces at once. The default's rows are top-level rows with no
    // header — with one workspace beside it, exactly as without.
    const { manager, bar } = await twoWorkspaces()
    manager.replaceStrip(new VerticalTabStrip())

    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(2))
    const headings = [...bar.querySelectorAll('.tabstrip-group-heading')].map(
      (el) => el.querySelector('.ui-button')?.textContent,
    )
    expect(headings).toEqual(['refactor-auth'])
  })

  // ── nocx-isoph.7: the vertical strip is not a second-class placement ──

  it('lets a user in VERTICAL placement create a workspace', async () => {
    // THE BUG THIS CLOSES. The chip is horizontal-only by design — the
    // vertical strip already shows every workspace, so a chip there would be
    // a second answer to one question — and the consequence was that after
    // changing one real setting a whole feature vanished with no explanation.
    // Driven IN vertical placement, never in horizontal with a note that
    // vertical is similar.
    const { manager, bar, backend } = await mountPaneManager()
    manager.replaceStrip(new VerticalTabStrip())
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(1))
    expect(chip(bar)).toBeNull()

    const before = backend.rows().workspaces.length
    // FIVE GLYPHS BECAME THREE, in both placements: creating a workspace is a
    // named row in the strip's own menu, where the horizontal row has always
    // had it, rather than a second unlabelled mark beside the layers one.
    bar.querySelector<HTMLElement>('[aria-label="More"]')!.click()
    const create = [...document.querySelectorAll<HTMLElement>('.ui-context-menu__item')].find(
      (el) => el.textContent?.includes('New workspace'),
    )!
    create.click()

    await vi.waitFor(() => {
      expect(backend.rows().workspaces.length).toBe(before + 1)
    })
    const made = backend.rows().workspaces.find((w) => w.name === 'refactor-auth')!
    // Creation is always creation-with-content, in either placement.
    expect(backend.rows().tabs.some((t) => t.workspaceId === made.id)).toBe(true)
  })

  it('offers a named workspace its own actions from the heading it heads', async () => {
    // ONE MECHANISM PLACED TWICE: these are the rows the chip shows for the
    // workspace in front of it, opened here from the heading instead.
    const { manager, bar } = await twoWorkspaces()
    manager.replaceStrip(new VerticalTabStrip())
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(2))

    const heading = bar.querySelector<HTMLElement>('.tabstrip-group-heading')!
    // The heading carries its own close mark now, so its NAME is the control
    // rather than the whole row's text.
    expect(heading.querySelector('.ui-button')!.textContent).toBe('refactor-auth')
    // The control is the kit's Button, so it is focusable and keyboard
    // operable without this file re-deriving either — which is what
    // nocx/no-role-impersonation exists to enforce.
    const control = heading.querySelector<HTMLElement>('.ui-button')!
    expect(control).not.toBeNull()
    control.click()

    const rows = [...document.querySelectorAll<HTMLElement>('.ui-context-menu__item')].map(
      (el) => el.textContent,
    )
    expect(rows).toContain('Rename workspace…')
    expect(rows).toContain('Close workspace')
  })

  it("renames a workspace from the strip, and the new name is the backend's", async () => {
    const { manager, bar, backend } = await twoWorkspaces()
    manager.replaceStrip(new VerticalTabStrip())
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(2))
    // Renaming and recolouring are one form now (nocx-2mipw.2): the row opens
    // the edit dialog, and what it answers is what the backend is asked for.
    workspaceEditMock.mockResolvedValue({ name: 'deploy', colour: 'green' })

    bar.querySelector<HTMLElement>('.tabstrip-group-heading .ui-button')!.click()
    ;[...document.querySelectorAll<HTMLElement>('.ui-context-menu__item')]
      .find((el) => el.textContent === 'Rename workspace…')!
      .click()

    await vi.waitFor(() => {
      expect(backend.rows().workspaces.map((w) => w.name)).toContain('deploy')
    })
    // The heading redraws from the store's answer, not from what was typed.
    await vi.waitFor(() => {
      expect(bar.querySelector('.tabstrip-group-heading .ui-button')?.textContent).toBe('deploy')
    })
  })

  it('never offers the default workspace a heading, or anything to do to it', async () => {
    // The rule §4.2 fixes, restated at the new surface: the default has no
    // heading, so it has nowhere to hang an action — and asking for its rows
    // directly answers with none either.
    const { manager, bar } = await twoWorkspaces()
    manager.replaceStrip(new VerticalTabStrip())
    await vi.waitFor(() => expect(visibleTabs(bar)).toHaveLength(2))

    const headings = [...bar.querySelectorAll('.tabstrip-group-heading')].map(
      (el) => el.querySelector('.ui-button')?.textContent,
    )
    expect(headings).toEqual(['refactor-auth'])

    manager.switchWorkspace(DEFAULT_WS)
    manager.replaceStrip(new HorizontalTabStrip())
    // And in the other placement the same rule holds by ABSENCE: the one pill
    // in the row belongs to the named workspace, so there is no surface from
    // which the default could be renamed, recoloured or closed.
    await vi.waitFor(() => expect([...bar.querySelectorAll(CHIP)]).toHaveLength(1))
    expect(chip(bar)!.textContent).toContain('refactor-auth')
  })

  it('opens a new tab in the workspace the window is showing', async () => {
    // The + is in the strip of ONE workspace. A tab that landed in the
    // default instead would either vanish the moment it was created or drag
    // the window out of the workspace the person was working in.
    const { manager, bar, backend } = await twoWorkspaces()
    const id = manager.currentWorkspaceId()

    manager.newPane()

    await vi.waitFor(() => {
      // Two in this workspace, plus the default's own row, which is always in
      // the strip (§4.2).
      expect(visibleTabs(bar)).toHaveLength(3)
    })
    expect(backend.rows().tabs.filter((t) => t.workspaceId === id)).toHaveLength(2)
    expect(manager.currentWorkspaceId()).toBe(id)
  })

  it('addresses the tabs the window is showing when a position is activated', async () => {
    // Cmd+1..9 is workspace-scoped now (§4.3): the strip shows one
    // workspace's tabs, so a position in it means a position in THAT set. A
    // key that counted every tab in the application would select a tab the
    // person cannot see.
    const { manager, bar } = await twoWorkspaces()
    const visible = tabTitles(bar)

    manager.activateByIndex(0)

    await vi.waitFor(() => {
      expect(bar.querySelector(`${TAB}[aria-selected="true"] .nocx-tab-title`)?.textContent).toBe(
        visible[0],
      )
    })
  })
})
