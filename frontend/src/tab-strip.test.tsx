// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { cleanup } from '@solidjs/testing-library'
import { HorizontalTabStrip, VerticalTabStrip } from './tab-strip'
import type { PaneView } from './tab-strip'

afterEach(() => {
  cleanup()
  document.body.innerHTML = ''
})

function makePane(id: number, title: string, tooltip: string): PaneView {
  return {
    id,
    title,
    tooltip,
    // The strip's filter reads the tooltip whether or not a second line is shown,
    // so these fixtures keep it searchable and leave the line itself empty.
    subtitle: '',
    preview: '',
    hasActivity: false,
    agentStatus: null,
    agentSource: null,
    paneId: `pane-${id}`,
    onDisplayChange: null,
  }
}

function setupVerticalStrip(): {
  strip: VerticalTabStrip
  container: HTMLElement
} {
  const strip = new VerticalTabStrip()
  const container = document.createElement('div')
  container.id = 'vertical-tabstrip'
  document.body.appendChild(container)
  strip.mount(container)
  return { strip, container }
}

function getSearchInput(): HTMLInputElement | null {
  return document.querySelector('input[type="search"]')
}

function getTabEl(idx: number): HTMLElement | null {
  const tabs = document.querySelectorAll('.nocx-tab')
  return (tabs[idx] as HTMLElement) ?? null
}

function isHidden(el: HTMLElement): boolean {
  return el.getAttribute('data-hidden') === 'true'
}

describe('VerticalTabStrip filtering', () => {
  it('renders search field in vertical mode', () => {
    setupVerticalStrip()
    const input = getSearchInput()
    expect(input).toBeTruthy()
  })

  it('filtering by title substring keeps matching rows and hides the rest', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'local terminal', '~/repos/nocx'))
    strip.addPane(makePane(2, 'SSH: server-01', 'ssh user@server-01'))
    strip.addPane(makePane(3, 'SSH: database', 'ssh dba@db-host'))

    const input = getSearchInput()!
    expect(input).toBeTruthy()

    // Simulate typing 'server' in the search field
    input.value = 'server'
    input.dispatchEvent(new Event('input', { bubbles: true }))

    // Tab 1 doesn't contain 'server' → hidden
    expect(isHidden(getTabEl(0)!)).toBe(true)
    // Tab 2 title contains 'server' → visible
    expect(isHidden(getTabEl(1)!)).toBe(false)
    // Tab 3 doesn't contain 'server' → hidden
    expect(isHidden(getTabEl(2)!)).toBe(true)
  })

  it('filtering by tooltip (cwd/host) substring matches too', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'local terminal', '~/repos/nocx'))
    strip.addPane(makePane(2, 'SSH: web', 'ssh deploy@web-01.prod'))

    const input = getSearchInput()!
    input.value = 'deploy'
    input.dispatchEvent(new Event('input', { bubbles: true }))

    // Tab 1 tooltip doesn't contain 'deploy' → hidden
    expect(isHidden(getTabEl(0)!)).toBe(true)
    // Tab 2 tooltip contains 'deploy' → visible
    expect(isHidden(getTabEl(1)!)).toBe(false)
  })

  it('clearing the query restores every row', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'local terminal', '~/repos/nocx'))
    strip.addPane(makePane(2, 'SSH: server', 'ssh user@server'))

    const input = getSearchInput()!

    // Filter to show only 'server'
    input.value = 'server'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    expect(isHidden(getTabEl(0)!)).toBe(true)
    expect(isHidden(getTabEl(1)!)).toBe(false)

    // Clear the field
    input.value = ''
    input.dispatchEvent(new Event('input', { bubbles: true }))

    // Both should be visible again
    expect(isHidden(getTabEl(0)!)).toBe(false)
    expect(isHidden(getTabEl(1)!)).toBe(false)
  })

  it('filtering does not change which tab is active', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'local terminal', '~/repos/nocx'))
    strip.addPane(makePane(2, 'SSH: server', 'ssh user@server'))
    strip.setActive(1)

    // Verify tab 1 is active
    const tab1 = getTabEl(0)!
    expect(tab1.getAttribute('aria-selected')).toBe('true')

    const input = getSearchInput()!
    input.value = 'server'
    input.dispatchEvent(new Event('input', { bubbles: true }))

    // Tab 1 is hidden by the filter but still active
    expect(isHidden(tab1)).toBe(true)
    expect(tab1.getAttribute('aria-selected')).toBe('true')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// Headings, the tree, and the chip (nocx-isoph.5)
// ═══════════════════════════════════════════════════════════════════════════

const DEFAULT_WS = 'workspace:default'
const HEADING = '.tabstrip-group-heading'

function grouped(id: number, groupKey: string, depth = 0): PaneView {
  return { ...makePane(id, `tab ${id}`, ''), groupKey, depth }
}

function headings(): string[] {
  // The heading carries its own close mark now, so its NAME is the control it
  // places rather than everything inside the row.
  return [...document.querySelectorAll(HEADING)].map(
    (el) => el.querySelector('.ui-button')?.textContent ?? '',
  )
}

/** The rendered strip, row by row, as a person reads it down the column:
 *  headings where headings are, tabs where tabs are, indentation included. */
function readStrip(): string[] {
  const container = document.querySelector('.tabs-container')!
  return [...container.children].map((el) =>
    el.classList.contains('nocx-tab')
      ? `${'·'.repeat(Number(el.getAttribute('data-depth') ?? 0))}${el.querySelector('.nocx-tab-title')?.textContent}`
      : `# ${el.querySelector('.ui-button')?.textContent ?? ''}`,
  )
}

describe('the vertical strip draws headings, and the default workspace has none', () => {
  it('draws a heading above each group that has one, in the order the rows arrive', () => {
    const { strip } = setupVerticalStrip()
    strip.setGroupHeadings([
      { key: 'ws-1', heading: 'refactor-auth', colour: null },
      { key: DEFAULT_WS, heading: null, colour: null },
    ])
    strip.addPane(grouped(1, 'ws-1'))
    strip.addPane(grouped(2, 'ws-1'))
    strip.addPane(grouped(3, DEFAULT_WS))

    expect(readStrip()).toEqual(['# refactor-auth', 'tab 1', 'tab 2', 'tab 3'])
  })

  it('leaves the default workspace exactly as it was when a second workspace appears', () => {
    // THE RULE A NAIVE IMPLEMENTATION BREAKS (§4.2). The default's chrome is
    // what it is because it is the default — never because of how many
    // workspaces exist. So this compares the default's rendered rows, byte
    // for byte, across the arrival of another workspace.
    const { strip } = setupVerticalStrip()
    strip.setGroupHeadings([{ key: DEFAULT_WS, heading: null, colour: null }])
    strip.addPane(grouped(1, DEFAULT_WS))
    strip.addPane(grouped(2, DEFAULT_WS))
    const alone = readStrip()
    const rowHtml = [...document.querySelectorAll('.nocx-tab')].map((el) => el.outerHTML)
    expect(headings()).toEqual([])

    strip.setGroupHeadings([
      { key: DEFAULT_WS, heading: null, colour: null },
      { key: 'ws-1', heading: 'refactor-auth', colour: null },
    ])
    strip.addPane(grouped(3, 'ws-1'))

    // The default's rows: same DOM, same absence of a heading above them.
    expect(
      [...document.querySelectorAll('.nocx-tab')].slice(0, 2).map((el) => el.outerHTML),
    ).toEqual(rowHtml)
    expect(readStrip().slice(0, alone.length)).toEqual(alone)
    // One heading, and it belongs to the workspace that has a name.
    expect(headings()).toEqual(['refactor-auth'])
  })

  it('indents a lineage child under its parent', () => {
    const { strip } = setupVerticalStrip()
    strip.setGroupHeadings([{ key: DEFAULT_WS, heading: null, colour: null }])
    strip.addPane(grouped(1, DEFAULT_WS))
    strip.addPane(grouped(2, DEFAULT_WS, 1))

    expect(readStrip()).toEqual(['tab 1', '·tab 2'])
    expect(document.querySelectorAll('.nocx-tab')[1].getAttribute('data-depth')).toBe('1')
  })

  it('takes a heading away with the group the filter empties, and brings it back', () => {
    // A heading over a group with nothing under it reads as a broken list.
    // The rows are hidden rather than removed, so the heading has to ask
    // whether any of its own survived the filter.
    const { strip } = setupVerticalStrip()
    strip.setGroupHeadings([{ key: 'ws-1', heading: 'refactor-auth', colour: null }])
    strip.addPane({ ...makePane(1, 'deploy', 'ssh deploy@srv-01'), groupKey: 'ws-1' })
    strip.addPane({ ...makePane(2, 'notes', '~/notes'), groupKey: '' })
    expect(headings()).toEqual(['refactor-auth'])

    const input = getSearchInput()!
    input.value = 'notes'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    expect(headings()).toEqual([])

    input.value = ''
    input.dispatchEvent(new Event('input', { bubbles: true }))
    expect(headings()).toEqual(['refactor-auth'])
  })

  it('draws every row it is given even when nothing said which group it is in', () => {
    // A strip with no chain behind it — the layout store refused, or a test —
    // still draws its tabs. Grouping is a way of drawing the list, never a
    // gate on being drawn at all.
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'one', ''))
    strip.addPane(makePane(2, 'two', ''))

    expect(readStrip()).toEqual(['one', 'two'])
    expect(headings()).toEqual([])
  })
})

// THE CHIP-AS-SWITCHER TESTS ARE GONE, and their absence is the record of a
// design that was withdrawn. §4.3 gave the horizontal strip ONE chip: the row
// drew the current workspace's tabs and every other workspace was reachable
// only through that chip's dropdown. The rework replaced it — every workspace
// is a pill IN the row, one click switches, and the strip has no
// `setWorkspaceChip` at all — so tests that drove that method were asserting a
// mechanism the product no longer has.
//
// What they were protecting is protected still, and end to end rather than at
// this seam: panes-workspaces.test.ts drives a real PaneManager for "the
// default draws no pill whatever else exists", "clicking a pill switches to
// that workspace", "a workspace's actions come from its pill", and "the
// default is offered nothing to do to it".

describe('the snippets action (nocx-d346)', () => {
  // The strip is a presentation port: it reports that the action was picked,
  // nothing more. What opens is the quick-connect palette in its snippets
  // variant — the same surface the key row beside it opens.
  //
  // IT IS A ROW IN THE STRIP'S MENU, in BOTH placements, and it used to be a
  // glyph of its own in each. Five same-weight marks became three (the
  // overview, a new tab, and the caret), and the rest became named rows —
  // which is also what stopped the two placements from meaning different
  // things by the same glyph.
  const pressesOf = (strip: { onSnippets: (() => void) | null }) => {
    let presses = 0
    strip.onSnippets = () => (presses += 1)
    return () => presses
  }

  /** Open the strip's caret menu and hand back its rows. */
  const menuRows = (): HTMLElement[] => {
    document.querySelector<HTMLButtonElement>('[aria-label="More"]')!.click()
    return [...document.querySelectorAll<HTMLElement>('.ui-context-menu__item')]
  }

  const snippetsRow = (): HTMLElement | undefined =>
    menuRows().find((el) => el.textContent?.includes('Snippets'))

  it('the vertical strip offers it and reports the press', () => {
    const { strip } = setupVerticalStrip()
    const presses = pressesOf(strip)

    const row = snippetsRow()
    expect(row, 'the vertical strip does not offer snippets').toBeDefined()
    row!.click()

    expect(presses()).toBe(1)
  })

  it('the horizontal strip offers it too — a strip replacement must not lose it', () => {
    const strip = new HorizontalTabStrip()
    const container = document.createElement('div')
    document.body.appendChild(container)
    strip.mount(container)
    const presses = pressesOf(strip)

    const row = snippetsRow()
    expect(row, 'the horizontal strip does not offer snippets').toBeDefined()
    row!.click()

    expect(presses()).toBe(1)
  })

  it('with no callback wired the row is inert rather than broken', () => {
    setupVerticalStrip()
    const row = snippetsRow()!
    expect(() => row.click()).not.toThrow()
  })
})

describe('the new-note row', () => {
  // A note is one keystroke away (⌥⌘N) for somebody who knows the chord, and
  // unreachable for everybody else until they have found the Notes panel. The
  // caret menu is where the strip keeps the actions that have a name instead
  // of a glyph, so it is where "New note" belongs — in BOTH placements, since
  // a row wired onto the first strip and lost on the next one is the failure
  // the snippets row above already records.
  const menuRows = (): HTMLElement[] => {
    document.querySelector<HTMLButtonElement>('[aria-label="More"]')!.click()
    return [...document.querySelectorAll<HTMLElement>('.ui-context-menu__item')]
  }

  const noteRow = (): HTMLElement | undefined =>
    menuRows().find((el) => el.textContent?.includes('New note'))

  it('the vertical strip offers it and reports the press', () => {
    const { strip } = setupVerticalStrip()
    let presses = 0
    strip.onNewNote = () => (presses += 1)

    const row = noteRow()
    expect(row, 'the vertical strip does not offer a new note').toBeDefined()
    row!.click()

    expect(presses).toBe(1)
  })

  it('the horizontal strip offers it too — a strip replacement must not lose it', () => {
    const strip = new HorizontalTabStrip()
    const container = document.createElement('div')
    document.body.appendChild(container)
    strip.mount(container)
    let presses = 0
    strip.onNewNote = () => (presses += 1)

    const row = noteRow()
    expect(row, 'the horizontal strip does not offer a new note').toBeDefined()
    row!.click()

    expect(presses).toBe(1)
  })

  it('carries no ellipsis, because nothing further is asked', () => {
    setupVerticalStrip()
    // The three rows above it end in one and mean it: each opens a palette or
    // a dialog. This row opens the note itself, and an ellipsis that promises
    // a question nobody is asked is a menu telling a small lie every time.
    expect(noteRow()!.textContent).toContain('New note')
    expect(noteRow()!.textContent).not.toContain('…')
  })

  it('wears a mark of its own rather than the snippets one', () => {
    // One glyph on two rows of one menu is a person pointing at the wrong
    // row: TextQuoteIcon means a saved phrase, and it used to mean a note too.
    setupVerticalStrip()
    const rows = menuRows()
    const note = rows.find((el) => el.textContent?.includes('New note'))!
    const snippets = rows.find((el) => el.textContent?.includes('Snippets'))!
    const pathsOf = (row: HTMLElement) =>
      [...row.querySelectorAll('path')].map((p) => p.getAttribute('d'))

    expect(pathsOf(note).length).toBeGreaterThan(0)
    for (const d of pathsOf(note)) expect(pathsOf(snippets)).not.toContain(d)
  })

  it('with no callback wired the row is inert rather than broken', () => {
    setupVerticalStrip()
    const row = noteRow()!
    expect(() => row.click()).not.toThrow()
  })
})

// ── The children a pane's agent spawned (nocx-o1v0h) ──────────────────────
//
// The rows come from the pane's OWN SCREEN — the backend reads the agent's
// task panel off a live VT grid and sends the names on the same notification
// that carries the pane's state. Nothing here is a hook, and nothing about a
// child decides anything about its parent.

function withChildren(pane: PaneView, ...children: { name: string; task?: string }[]): PaneView {
  return { ...pane, agentChildren: children }
}

function subagentRows(): HTMLElement[] {
  return [...document.querySelectorAll('.nocx-subagent')] as HTMLElement[]
}

// Found by its ACCESSIBLE NAME, the way a person using assistive technology
// finds it — not by its position among the row's controls, which would pass
// just as well if the control were the close button.
function disclosure(): HTMLElement | null {
  return document.querySelector<HTMLElement>('.nocx-tab [aria-label$="subagents"]')
}

describe('VerticalTabStrip subagent rows', () => {
  it('draws one row per child, with its name and what it is doing', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(
      withChildren(
        makePane(1, 'claude', '~/repos/nocx'),
        { name: 'Explore', task: 'List files in directory' },
        { name: 'Plan', task: 'Draft the change' },
      ),
    )

    const rows = subagentRows()
    expect(rows).toHaveLength(2)
    expect(rows[0]?.textContent).toContain('Explore')
    expect(rows[0]?.textContent).toContain('List files in directory')
    expect(rows[1]?.textContent).toContain('Plan')
  })

  // The rows are drawn UNDER the row they belong to, and one generation in.
  // A child row that floated to the end of the list would say nothing about
  // whose child it is, which is most of what it has to say.
  it('places the rows immediately under their parent, one generation in', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(withChildren(makePane(1, 'claude', '~/a'), { name: 'Explore' }))
    strip.addPane(makePane(2, 'shell', '~/b'))

    const drawn = [...document.querySelectorAll('.nocx-tab, .nocx-subagent')] as HTMLElement[]
    expect(drawn.map((el) => el.className)).toEqual(['nocx-tab', 'nocx-subagent', 'nocx-tab'])
    expect(drawn[1]?.getAttribute('data-depth')).toBe('1')
  })

  // EXPANDED BY DEFAULT, because a spawned child is work in flight. The
  // chevron is there to fold it away, not to reveal it.
  it('shows the rows without being asked, and folds them to a count on request', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(withChildren(makePane(1, 'claude', '~/a'), { name: 'Explore' }, { name: 'Plan' }))
    expect(subagentRows()).toHaveLength(2)
    expect(getTabEl(0)?.getAttribute('data-disclosure')).toBe('expanded')
    expect(getTabEl(0)?.textContent).not.toContain('+2')

    disclosure()!.click()
    expect(subagentRows()).toHaveLength(0)
    expect(getTabEl(0)?.getAttribute('data-disclosure')).toBe('collapsed')
    // Folded away is not gone: the count is what keeps a person from
    // forgetting the pane has children at all.
    expect(getTabEl(0)?.textContent).toContain('+2')

    disclosure()!.click()
    expect(subagentRows()).toHaveLength(2)
    expect(getTabEl(0)?.textContent).not.toContain('+2')
  })

  // A pane with no children is exactly the strip it always was: no control,
  // no count, no extra row.
  it('draws no disclosure at all for a pane whose agent spawned nothing', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'shell', '~/a'))
    expect(getTabEl(0)?.getAttribute('data-disclosure')).toBe('leaf')
    expect(disclosure()).toBeNull()
    expect(subagentRows()).toHaveLength(0)
  })

  // A CHILD HAS NO PANE OF ITS OWN. Clicking its row goes to the pane its
  // parent runs in, because that is the only place there is to go — and a row
  // that looks clickable and goes nowhere is worse than one that is not.
  it('activates the parent pane when a child row is clicked', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(makePane(1, 'shell', '~/a'))
    strip.addPane(withChildren(makePane(2, 'claude', '~/b'), { name: 'Explore' }))
    const activated: number[] = []
    strip.onActivate = (id) => activated.push(id)

    subagentRows()[0]?.click()
    expect(activated).toEqual([2])
    expect(subagentRows()[0]?.getAttribute('aria-controls')).toBe('pane-2')
  })

  // The rows follow the parent's filter, because a child drawn under a hidden
  // parent is a row with no parent.
  it('hides a child whose parent the filter hid', () => {
    const { strip } = setupVerticalStrip()
    strip.addPane(withChildren(makePane(1, 'claude', '~/a'), { name: 'Explore' }))
    strip.addPane(makePane(2, 'server-01', '~/b'))

    const input = getSearchInput()!
    input.value = 'server'
    input.dispatchEvent(new Event('input', { bubbles: true }))

    expect(subagentRows()[0]?.getAttribute('data-hidden')).toBe('true')
  })

  // A CHILD FACT MAY NEVER DECIDE THE PARENT'S STATE, at the last seam it
  // could have gone wrong: the row a person actually looks at. Children
  // arriving, changing and going away leave the parent's indicator exactly
  // where it was.
  it('leaves the parent indicator untouched as children come and go', () => {
    const { strip } = setupVerticalStrip()
    const base = { ...makePane(1, 'claude', '~/a'), agentStatus: 'working' as const }
    strip.addPane(base)
    const status = (): string | null => getTabEl(0)?.getAttribute('data-agent-status') ?? null
    expect(status()).toBe('working')

    strip.refreshPane(withChildren(base, { name: 'Explore' }))
    expect(subagentRows()).toHaveLength(1)
    expect(status()).toBe('working')

    strip.refreshPane(withChildren(base, { name: 'Explore' }, { name: 'Plan' }))
    expect(subagentRows()).toHaveLength(2)
    expect(status()).toBe('working')

    strip.refreshPane(base)
    expect(subagentRows()).toHaveLength(0)
    expect(status()).toBe('working')
  })
})
