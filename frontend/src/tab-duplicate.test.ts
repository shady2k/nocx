// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  anchoredPane,
  makeBanner,
  makeClipboard,
  makeClient,
  makeLayoutStore,
  mountPaneManager,
  resetSessionCounter,
} from './test-support/panes-fixtures'
import { ClipboardGate } from './clipboard'
import { SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'

// THE TAB MENU'S DUPLICATE (nocx-ouru5.1.4), as the renderer asks for it.
//
// What a copy IS — the seat, the directory, the connection — belongs to the
// backend and to the shell that answers where it stands, and the e2e is where
// those are watched (e2e/tab-duplicate.spec.ts). What is asserted here is the
// half only the renderer can get wrong: that the row lives in the ONE list of
// a tab's menu, that it asks for a seat beside the tab the person pointed at,
// and that it copies the chain's answer — the pane's own reported directory,
// its workspace, its kind — rather than anything the chrome guessed.
//
// The second case is the negative one: a surface the window draws without
// storing it (Settings) has nothing to copy, so its menu must not offer the
// action at all.

const TAB = '.nocx-tab'
const MENU_ITEM = '.ui-context-menu__item'

/** Open one row's context menu and answer the labels it offers. */
function menuLabels(row: Element): string[] {
  row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
  return [...document.querySelectorAll<HTMLElement>(MENU_ITEM)].map((el) => el.textContent ?? '')
}

function pick(label: string): void {
  const row = [...document.querySelectorAll<HTMLElement>(MENU_ITEM)].find(
    (el) => el.textContent === label,
  )
  if (!row) throw new Error(`no menu row ${label}`)
  row.click()
}

describe("the tab's menu offers Duplicate (nocx-ouru5.1.4)", () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
    document.body.innerHTML = ''
  })

  it('seats the copy beside its source, in the source pane’s own directory and workspace', async () => {
    const layout = makeLayoutStore()
    const ask = vi.spyOn(layout.backend, 'createTab')
    const { manager, bar, backend } = await mountPaneManager(
      undefined,
      undefined,
      undefined,
      undefined,
      layout,
    )
    await vi.waitFor(() => expect(bar.querySelectorAll(TAB)).toHaveLength(1))

    const first = backend.rows()
    const tab = first.tabs[0]
    const pane = first.panes[0]
    // The directory the pane's own shell reported (OSC 7, AD-5). It is the
    // chain's answer, so it is what a copy starts in.
    await layout.store.setPaneCwd(pane.id, '/tmp/duplicated')

    const row = bar.querySelectorAll(TAB)[0]
    expect(menuLabels(row)).toContain('Duplicate')
    pick('Duplicate')

    await vi.waitFor(() => expect(ask).toHaveBeenCalledTimes(2))
    const asked = ask.mock.calls[1][0]
    expect(asked.afterTabId, 'seated immediately after the row that was asked about').toBe(tab.id)
    expect(asked.workspaceId, "in the source's workspace").toBe(tab.workspaceId)
    expect(asked.firstPane).toMatchObject({
      cwd: '/tmp/duplicated',
      kind: 'local',
      endpoint: null,
    })
    // And the strip draws it: a create the store folded in.
    await vi.waitFor(() => expect(bar.querySelectorAll(TAB)).toHaveLength(2))
    expect(manager.paneCount).toBe(2)
  })

  it('offers no Duplicate on a surface the chain does not hold', async () => {
    const { manager, bar } = await mountPaneManager()
    await vi.waitFor(() => expect(bar.querySelectorAll(TAB)).toHaveLength(1))

    // The surface is a pane with no chain row, which is what a view surface is
    // (Settings, a file viewer). Its own client never answers, so the pane
    // stays before its session and the descriptor's title is what the row
    // shows — which is how the row is found below.
    const client = makeClient()
    client.openSession = vi.fn(() => new Promise(() => {}))

    const { TerminalContent } = await import('./terminal-content')
    const content = new TerminalContent(
      client as unknown as import('./ipc').WSClient,
      anchoredPane(),
      makeClipboard(),
      new ClipboardGate(),
      makeBanner(),
      null,
      () => {},
    )
    manager.openPane(content, {
      surfaceType: SURFACE_SETTINGS,
      singletonKey: SINGLETON_SETTINGS,
      restoreDescriptor: { type: 'local' },
      supportsAttention: false,
      defaultTitle: 'Settings',
    })
    await vi.waitFor(() => expect(bar.querySelectorAll(TAB)).toHaveLength(2))

    const rows = [...bar.querySelectorAll(TAB)]
    const view = rows.find((el) => el.querySelector('.nocx-tab-title')?.textContent === 'Settings')
    expect(view, 'the view pane is on the strip').toBeTruthy()
    expect(menuLabels(view!)).not.toContain('Duplicate')

    // …while the terminal row beside it still has it, so the assertion above
    // is about the row and not about a menu that failed to open.
    const terminal = rows.find((el) => el !== view)
    expect(menuLabels(terminal!)).toContain('Duplicate')
  })
})
