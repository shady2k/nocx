// @vitest-environment jsdom
/**
 * The overview panel, tested through what a person does with it.
 *
 * Every assertion below is reachable from the state a user starts in: they
 * open the overview, they see the panes they have, they pick one. Nothing here
 * asserts a shape only because the component happens to render it — the rule
 * that made a connection manager ship with no way to create a group
 * (AGENTS.md, testing rule 1).
 */
import { cleanup, fireEvent, render, within } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { OverviewPanel } from './overview-panel'
import { fakePane, FakeOverviewPort } from './fake-port'
import type { OverviewSnapshot } from './overview-port'

afterEach(cleanup)

const NOW = 1_700_000_000_000

function twoWorkspaces(): OverviewSnapshot {
  return {
    activePaneId: 'here',
    workspaces: [
      {
        id: 'w-default',
        name: null,
        panes: [
          fakePane({
            paneId: 'here',
            title: '~/repos/nocx',
            cwd: { state: 'known', cwd: '~/repos/nocx' },
          }),
        ],
      },
      {
        id: 'w1',
        name: 'refactor-auth',
        panes: [
          fakePane({
            paneId: 'elsewhere',
            title: 'claude',
            host: 'deploy@srv-01',
            cwd: { state: 'known', cwd: '~/app' },
            branch: 'main',
            agentStatus: 'idle',
            since: NOW - 5 * 60_000,
            lastLine: 'Should I drop the column?',
          }),
        ],
      },
    ],
  }
}

function cards(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('.overview__card'))
}

describe('the overview a person opens', () => {
  it('shows a card for a pane in a workspace they are not in, and says what it needs', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))

    const texts = cards(container).map((c) => c.textContent ?? '')
    const remote = texts.find((t) => t.includes('claude'))
    expect(remote).toBeDefined()
    // What is running, where, in what state, for how long, and its last line.
    expect(remote).toContain('deploy@srv-01')
    expect(remote).toContain('~/app')
    expect(remote).toContain('main')
    expect(remote).toContain('Waiting on you for 5m')
    expect(remote).toContain('Should I drop the column?')
  })

  it("shows what the OS says about a session's process, and says so when it does not know", () => {
    // The reader nocx-k6p18.13 built, extended to the three diagnostics
    // nocx-k6p18.12 added. It reaches the surface a person actually looks at,
    // because a projection nothing renders is a field nobody reads.
    const port = new FakeOverviewPort({
      activePaneId: null,
      workspaces: [
        {
          id: 'w1',
          name: 'hosts',
          panes: [
            fakePane({
              paneId: 'stopped',
              title: 'suspended shell',
              process: {
                observed: true,
                processState: 'stopped',
                startTimeMs: NOW - 3 * 60 * 60 * 1000,
                ppid: 4242,
              },
            }),
            fakePane({
              paneId: 'blind',
              title: 'blind shell',
              process: {
                observed: true,
                processState: null,
                startTimeMs: null,
                ppid: null,
              },
            }),
          ],
        },
      ],
    })
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))

    const texts = cards(container).map((c) => c.textContent ?? '')
    const stopped = texts.find((t) => t.includes('suspended shell'))
    expect(stopped).toContain('Stopped')
    expect(stopped).toContain('started 3h ago')
    expect(stopped).toContain('parent 4242')

    // Asked and unanswered is a SENTENCE on the card, not an absence: a
    // reader shown nothing goes looking in the launch record.
    const blind = texts.find((t) => t.includes('blind shell'))
    expect(blind).toContain('State unavailable')
    expect(blind).toContain('start time unavailable')
    expect(blind).toContain('parent unavailable')
    expect(blind).not.toContain('Stopped')
  })

  it('draws no pixel thumbnail of a terminal anywhere', () => {
    // The whole argument for this surface: twelve scaled-down terminals are
    // grey noise, so the card is TEXT. A canvas or an xterm mount here would
    // be the feature turning back into the thing it replaced.
    const port = new FakeOverviewPort(twoWorkspaces())
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))
    expect(container.querySelectorAll('canvas').length).toBe(0)
    expect(container.querySelectorAll('img').length).toBe(0)
    expect(container.querySelectorAll('.xterm').length).toBe(0)
  })

  it('gives a named workspace a column head with its pane count and attention, and the ungrouped panes none', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))

    // EVERY COLUMN IS HEADED HERE, including the ungrouped one — and that is
    // not a breach of §4.2 but the place where its reason runs out. In the
    // STRIP the default draws nothing because its rows sit among the tabs and
    // a chip for it would be a second answer to "which workspace am I in". In
    // a picture of every workspace at once, a column with no head is a column
    // a person cannot name, so the ungrouped one says exactly that.
    expect(container.querySelectorAll('.overview__column').length).toBe(2)
    const heads = Array.from(container.querySelectorAll<HTMLElement>('.overview__column-head'))
    expect(heads.length).toBe(2)
    expect(heads[0].textContent).toContain('Ungrouped')
    expect(heads[1].textContent).toContain('refactor-auth')
    expect(heads[1].textContent).toContain('1 pane')
    expect(heads[1].dataset.attention).toBe('waiting')
  })

  it('goes to a workspace when its head is pressed, and closes', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    let closed = 0
    const { container } = render(() => (
      <OverviewPanel
        port={port}
        onClose={() => {
          closed++
        }}
        now={() => NOW}
      />
    ))
    // The NAMED workspace's head — the ungrouped column is headed too now, and
    // it is the first of the two.
    const head = container.querySelectorAll<HTMLElement>('.overview__column-head .ui-button')[1]
    head?.click()
    // It asks the application to switch and never picks a pane itself: which
    // one you land on is the MRU question, and PaneManager owns the answer.
    expect(port.switched).toEqual(['w1'])
    expect(closed).toBe(1)
  })

  it('opens a tab in the column it was asked from, and closes', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    let closed = 0
    const { container } = render(() => (
      <OverviewPanel
        port={port}
        onClose={() => {
          closed++
        }}
        now={() => NOW}
      />
    ))
    // The create sits in the SPOTLIGHT column's footer now, not in its head:
    // the head names the workspace and offers to close it, and a column that
    // is not in front offers no create at all — the gesture would land in a
    // workspace the person is only glancing at. So the subject is read off
    // the column that HAS the action, which is the fact being asserted.
    const column = container.querySelector<HTMLElement>(
      '.overview__column:has(.overview__column-actions)',
    )!
    const subject = column.dataset.workspaceId
    column.querySelector<HTMLElement>('.overview__column-actions .ui-button')!.click()
    // The WORKSPACE it was pressed in, not "wherever the window is": this
    // surface shows every workspace at once.
    expect(port.tabsCreated).toEqual([subject])
    expect(closed).toBe(1)
  })

  it('offers a way to make a workspace, and closes when it is taken', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    let closed = 0
    const { container } = render(() => (
      <OverviewPanel
        port={port}
        onClose={() => {
          closed++
        }}
        now={() => NOW}
      />
    ))
    const create = container.querySelector<HTMLElement>('.overview__new-workspace .ui-button')
    expect(create).not.toBeNull()
    create?.click()
    expect(port.workspacesCreated).toBe(1)
    expect(closed).toBe(1)
  })

  it('activates the pane a click lands on, and closes', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    let closed = 0
    const { container } = render(() => (
      <OverviewPanel
        port={port}
        onClose={() => {
          closed++
        }}
        now={() => NOW}
      />
    ))

    const remote = cards(container).find((c) => (c.textContent ?? '').includes('claude'))
    fireEvent.click(remote!.querySelector('.ui-collection-row')!)

    expect(port.activated).toEqual(['elsewhere'])
    expect(closed).toBe(1)
  })

  it('activates with Enter on the card the keyboard is on', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))

    // The keyboard lands on the card's NAME, which is a real button, so
    // Enter is the browser's own (nocx-5xwub). It used to land on the kit
    // row, which listened for Enter while announcing "list item" — the
    // change is deliberate: the roving now targets the thing a person is
    // actually told they can press.
    const control = within(container).getAllByRole('button', { name: 'claude' })[0]
    fireEvent.click(control)
    expect(port.activated.length).toBe(1)
  })

  it('moves between cards with the arrow keys', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))

    // The roving target is the card's own name-control (nocx-5xwub), not the
    // kit row it sits in.
    const focusables = cards(container).map((c) =>
      c.querySelector<HTMLElement>('.ui-record-row__open')!,
    )
    focusables[0].focus()
    expect(document.activeElement).toBe(focusables[0])

    fireEvent.keyDown(focusables[0], { key: 'ArrowRight' })
    expect(document.activeElement).toBe(focusables[1])

    fireEvent.keyDown(focusables[1], { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(focusables[0])

    // The ends hold: there is nothing before the first card, and a wrap would
    // move the eye across the whole window for a key that means "one over".
    fireEvent.keyDown(focusables[0], { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(focusables[0])
  })

  it('closes when the click lands outside every card', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    let closed = 0
    const { container } = render(() => (
      <OverviewPanel
        port={port}
        onClose={() => {
          closed++
        }}
        now={() => NOW}
      />
    ))

    const backdrop = container.querySelector<HTMLElement>('.overview')!
    fireEvent.mouseDown(backdrop)
    expect(closed).toBe(1)
    expect(port.activated).toEqual([])
  })

  it('keeps Tab inside itself while it is open', () => {
    const port = new FakeOverviewPort(twoWorkspaces())
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))

    // The trap holds every control the panel draws — the cards AND the
    // buttons around them (a column's head, its close mark, its create, the
    // make-a-workspace action). What it promises is not an order but a
    // boundary: Tab and Shift+Tab move, and neither ever leaves.
    const panel = container.querySelector<HTMLElement>('.overview')!
    const cardStops = cards(container).map((c) => c.querySelector<HTMLElement>('[tabindex]')!)
    const last = cardStops[cardStops.length - 1]

    last.focus()
    fireEvent.keyDown(last, { key: 'Tab' })
    expect(document.activeElement).not.toBe(last)
    expect(panel.contains(document.activeElement)).toBe(true)

    cardStops[0].focus()
    fireEvent.keyDown(cardStops[0], { key: 'Tab', shiftKey: true })
    expect(document.activeElement).not.toBe(cardStops[0])
    expect(panel.contains(document.activeElement)).toBe(true)
  })
})

describe('the overview when the application has little to show', () => {
  it('offers the make-one action rather than an empty window when there is nothing at all', () => {
    const port = new FakeOverviewPort({ workspaces: [], activePaneId: null })
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))
    // A board with no columns is not an error state that needs a sentence
    // explaining it: the last column is already there and it is the thing to
    // do next. An EmptyState here would have been a message beside a control
    // that says the same thing — and the control also works.
    expect(container.querySelector('.overview__new-workspace .ui-button')).not.toBeNull()
    expect(container.querySelectorAll('.overview__column').length).toBe(0)
    expect(cards(container).length).toBe(0)
  })

  it('still draws a card for a pane that has composed no title yet', () => {
    const port = new FakeOverviewPort({
      activePaneId: null,
      workspaces: [{ id: 'w1', name: 'fresh', panes: [fakePane({ paneId: 'new' })] }],
    })
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))
    expect(cards(container).length).toBe(1)
    expect(cards(container)[0].textContent).toContain('Untitled pane')
    expect(cards(container)[0].textContent).toContain('Idle')
  })

  it('draws a workspace the renderer never drew a row for, with no cards under it', () => {
    const port = new FakeOverviewPort({
      activePaneId: null,
      workspaces: [{ id: 'w1', name: 'adopted', panes: [] }],
    })
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))
    const heads = Array.from(container.querySelectorAll<HTMLElement>('.overview__column-head'))
    expect(heads.length).toBe(1)
    expect(heads[0].textContent).toContain('0 panes')
    expect(cards(container).length).toBe(0)
  })

  it('re-reads the application when the port says something changed', () => {
    const port = new FakeOverviewPort({
      activePaneId: null,
      workspaces: [{ id: 'w1', name: 'w', panes: [fakePane({ paneId: 'a', title: 'first' })] }],
    })
    const { container } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))
    expect(container.textContent).toContain('first')

    port.setSnapshot({
      activePaneId: null,
      workspaces: [
        {
          id: 'w1',
          name: 'w',
          panes: [
            fakePane({ paneId: 'a', title: 'first' }),
            fakePane({ paneId: 'b', title: 'second' }),
          ],
        },
      ],
    })
    expect(cards(container).length).toBe(2)
    expect(container.textContent).toContain('second')
  })

  it('lets go of the port when it unmounts', () => {
    const port = new FakeOverviewPort({ workspaces: [], activePaneId: null })
    const { unmount } = render(() => (
      <OverviewPanel port={port} onClose={() => {}} now={() => NOW} />
    ))
    expect(port.listenerCount).toBe(1)
    unmount()
    expect(port.listenerCount).toBe(0)
  })
})
