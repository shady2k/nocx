// Block manager tests — DOM creation, freeze lifecycle, clear behaviour.
// Updated for flat design (P0-1) and single-select model (P1-7, P1-8).

// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, beforeAll } from 'vitest'
import {
  BlockManager,
  createCommandBlock,
  createRunningBlock,
  freezeBlock,
  deselectAllBlocks,
  getSelectedBlock,
  blockOutputText,
  blockCommandText,
  blockKindRules,
  FENCE_DEFER_MS,
  type BlockKind,
} from './blocks'
import { shellHighlightReady } from '../shell-highlight'
import { BufferLine } from './test-helpers'
import { setCurrentTheme, _resetThemeState } from '../renderers/theme-adapter'
import { CommandSnapshotStore } from '../command-snapshot'
import { mintDomain, type IntegrationDomain } from '../lifecycle/domains'
import type { ExecutionAttempt } from '../lifecycle/state'

/** Helper: returns a container supplier that references the given element. */
function makeContainer(el: HTMLElement): () => HTMLElement {
  return () => el
}

const noopSelect = (): void => {}

/** A fresh, empty store — verdicts default to "no snapshot" per test. */
const freshStore = (): CommandSnapshotStore => new CommandSnapshotStore()

describe('createRunningBlock', () => {
  it('creates a div with classes cmd-block and cmd-block-running', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'ls -la', '~', '', () => container, noopSelect, freshStore())
    expect(el.tagName).toBe('DIV')
    expect(el.classList.contains('cmd-block')).toBe(true)
    expect(el.classList.contains('cmd-block-running')).toBe(true)
    expect(el.getAttribute('data-block-id')).toBe('1')
  })

  it('includes command text in header', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'ls -la', '~', '', () => container, noopSelect, freshStore())
    const text = el.querySelector('.cmd-header-text')
    expect(text?.textContent).toBe('ls -la')
  })

  it('includes cwd chip in the header (standard .nocx-chip component)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(
      1,
      'echo hi',
      '/home/dev/projects',
      '',
      () => container,
      noopSelect,
      freshStore(),
    )
    const cwd = el.querySelector('.cmd-header-cwd')
    expect(cwd?.textContent).toBe('\u{1F4C1} dev/projects')
    expect(cwd?.classList.contains('nocx-chip')).toBe(true)
  })

  it('shows a spinner for running state', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'sleep 10', '~', '', () => container, noopSelect, freshStore())
    const spinner = el.querySelector('.cmd-header-spinner')
    expect(spinner).not.toBeNull()
  })

  it('has no output area until freeze (P0-3)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'cmd', '~', '', () => container, noopSelect, freshStore())
    const output = el.querySelector('.cmd-output')
    expect(output).toBeNull()
  })

  it('includes overflow menu button (P2-9)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'cmd', '~', '', () => container, noopSelect, freshStore())
    const btn = el.querySelector('.cmd-overflow-btn')
    expect(btn).not.toBeNull()
  })

  it('marks a block whose author is not the human — the kit badge in its info tone (nocx-iadtt)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(
      1,
      'ls -la',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'agent',
    )
    const mark = el.querySelector('.ui-badge[data-author="agent"]')
    expect(mark).not.toBeNull()
    expect(mark?.getAttribute('data-tone')).toBe('info')
    expect(mark?.textContent).toBe('agent')
  })

  it("a human's block carries no mark at all — the default author is the shell (nocx-iadtt)", () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'ls -la', '~', '', () => container, noopSelect, freshStore())
    expect(el.querySelector('.ui-badge[data-author]')).toBeNull()
  })
})

describe('createCommandBlock', () => {
  const c = (): HTMLElement => document.createElement('div')

  it('creates a frozen block with success status', () => {
    const el = createCommandBlock(
      'command',
      1,
      'echo hello',
      '~',
      '',
      'output',
      42,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    expect(el.classList.contains('cmd-block')).toBe(true)
    const exit = el.querySelector('.cmd-header-exit-ok')
    expect(exit?.textContent).toBe('ok')
  })

  it('creates a frozen block with failure status', () => {
    const el = createCommandBlock(
      'command',
      2,
      'false',
      '~',
      '',
      '',
      5,
      1,
      'failure',
      c,
      noopSelect,
      freshStore(),
    )
    const exit = el.querySelector('.cmd-header-exit-fail')
    expect(exit?.textContent).toBe('exit 1')
  })

  it('includes serialized output', () => {
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '~',
      '',
      '<span class="term-line">file.txt</span>',
      10,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    const output = el.querySelector('.cmd-output')
    expect(output?.innerHTML).toContain('file.txt')
  })

  it('a double-click applies the whole-token selection ONCE, at the second mousedown', () => {
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '~',
      '',
      '<span class="term-line">profile-usage.json</span>',
      10,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    document.body.appendChild(el)
    const line = el.querySelector<HTMLElement>('.term-line')
    const node = (line?.firstChild as Text | null) ?? null
    expect(node).not.toBeNull()
    // jsdom has no hit-testing; point caretRangeFromPoint inside the token
    // ('usage') so the handler's browser seam resolves like a real click.
    const proto = Object.getOwnPropertyDescriptor(document, 'caretRangeFromPoint')
    Object.defineProperty(document, 'caretRangeFromPoint', {
      configurable: true,
      value: () => {
        const r = document.createRange()
        r.setStart(node!, 8)
        r.collapse(true)
        return r
      },
    })
    try {
      const sel = window.getSelection()
      const addRangeSpy = vi.spyOn(sel!, 'addRange')
      // The browser creates its native word selection on the SECOND mousedown
      // (event.detail === 2), before the dblclick event fires. The handler
      // must prevent that default and apply OUR token range in one operation
      // — exactly one selection state, nothing for copy-on-select to race.
      const ev = new MouseEvent('mousedown', {
        bubbles: true,
        cancelable: true,
        detail: 2,
        clientX: 10,
        clientY: 10,
      })
      line?.dispatchEvent(ev)
      expect(ev.defaultPrevented).toBe(true) // native word-select stopped
      expect(addRangeSpy).toHaveBeenCalledTimes(1) // ours, and only ours
      expect(sel?.toString()).toBe('profile-usage.json')
      // No dblclick listener remains: the event does nothing further.
      line?.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
      expect(addRangeSpy).toHaveBeenCalledTimes(1)
      expect(sel?.toString()).toBe('profile-usage.json')
    } finally {
      if (proto) {
        Object.defineProperty(document, 'caretRangeFromPoint', proto)
      } else {
        delete (document as { caretRangeFromPoint?: unknown }).caretRangeFromPoint
      }
      el.remove()
    }
  })

  it('a single mousedown is not intercepted — native selection and click-to-select keep working', () => {
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '~',
      '',
      '<span class="term-line">profile-usage.json</span>',
      10,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    document.body.appendChild(el)
    const line = el.querySelector<HTMLElement>('.term-line')
    const ev = new MouseEvent('mousedown', {
      bubbles: true,
      cancelable: true,
      detail: 1,
      clientX: 10,
      clientY: 10,
    })
    line?.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(false) // drag selection must survive
    el.remove()
  })

  it('includes duration', () => {
    const el = createCommandBlock(
      'command',
      1,
      'sleep 1',
      '~',
      '',
      'some output',
      1234,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    const dur = el.querySelector('.cmd-header-duration')
    expect(dur?.textContent).toBe('1.2s')
  })

  it('omits exit badge when exitCode is null', () => {
    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      'out',
      null,
      null,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    expect(el.querySelector('.cmd-header-exit')).toBeNull()
  })

  it('omits .cmd-output when outputHtml is empty (P0-3)', () => {
    const el = createCommandBlock(
      'command',
      1,
      'cd repos',
      '~',
      '',
      '',
      3,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    expect(el.querySelector('.cmd-output')).toBeNull()
  })

  it('omits .cmd-output when outputHtml is only empty term-lines (P0-3)', () => {
    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      '<span class="term-line"></span>',
      1,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    expect(el.querySelector('.cmd-output')).toBeNull()
  })

  it('includes overflow menu button (P2-9)', () => {
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '~',
      '',
      'output',
      10,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    const btn = el.querySelector('.cmd-overflow-btn')
    expect(btn).not.toBeNull()
  })

  it('cwd label uses plain text, no emoji (P0-1 flat pivot)', () => {
    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '/home/user/repos',
      '',
      'out',
      10,
      0,
      'success',
      c,
      noopSelect,
      freshStore(),
    )
    const cwdEl = el.querySelector('.cmd-header-cwd')
    expect(cwdEl?.textContent).toBe('\u{1F4C1} user/repos')
  })
})

describe('freezeBlock', () => {
  it('replaces a running block with a frozen one in the DOM', () => {
    const parent = document.createElement('div')
    const container = document.createElement('div')
    const running = createRunningBlock(
      1,
      'sleep 5',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
    )
    parent.appendChild(running)

    const frozen = freezeBlock(
      running,
      1,
      'sleep 5',
      '~',
      '',
      '<span>done</span>',
      5100,
      0,
      () => container,
      noopSelect,
      freshStore(),
      'success',
    )
    expect(parent.children.length).toBe(1)
    expect(parent.children[0]).toBe(frozen)
    expect(frozen.classList.contains('cmd-block')).toBe(true)
    expect(frozen.querySelector('.cmd-header-exit-ok')).not.toBeNull()
    expect(frozen.querySelector('.cmd-output')?.innerHTML).toContain('done')
  })

  it('adds overflow menu to frozen block (P2-9)', () => {
    const parent = document.createElement('div')
    const container = document.createElement('div')
    const running = createRunningBlock(1, 'ls', '~', '', () => container, noopSelect, freshStore())
    parent.appendChild(running)
    const frozen = freezeBlock(
      running,
      1,
      'ls',
      '~',
      '',
      '<span>ok</span>',
      100,
      0,
      () => container,
      noopSelect,
      freshStore(),
      'success',
    )
    expect(frozen.querySelector('.cmd-overflow-btn')).not.toBeNull()
  })
})

describe('block selection model (P1-7, P1-8)', () => {
  it('click selects a block', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )

    parent.appendChild(el)

    // Simulate click: mousedown + mouseup without movement
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))

    expect(el.classList.contains('cmd-block-selected')).toBe(true)
    document.body.removeChild(parent)
  })

  it('clicking a second block deselects the first (P1-8: single-select)', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)

    const el1 = createCommandBlock(
      'command',
      1,
      'cmd1',
      '~',
      '',
      'out1',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )
    const el2 = createCommandBlock(
      'command',
      2,
      'cmd2',
      '~',
      '',
      'out2',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )
    parent.append(el1, el2)

    // Select first block
    el1.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el1.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(el1.classList.contains('cmd-block-selected')).toBe(true)

    // Select second block — should deselect first
    el2.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el2.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(el1.classList.contains('cmd-block-selected')).toBe(false)
    expect(el2.classList.contains('cmd-block-selected')).toBe(true)

    document.body.removeChild(parent)
  })

  it('clicking an already-selected block deselects it', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)

    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )
    parent.appendChild(el)

    // First click selects
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(el.classList.contains('cmd-block-selected')).toBe(true)

    // Second click deselects
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(el.classList.contains('cmd-block-selected')).toBe(false)

    document.body.removeChild(parent)
  })

  it('drag (mousedown+mousemove+mouseup) does NOT select block', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)

    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )
    parent.appendChild(el)

    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mousemove', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))

    expect(el.classList.contains('cmd-block-selected')).toBe(false)

    document.body.removeChild(parent)
  })

  it('deselectAllBlocks removes selection', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)

    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )
    parent.appendChild(el)

    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(el.classList.contains('cmd-block-selected')).toBe(true)

    deselectAllBlocks(parent)
    expect(el.classList.contains('cmd-block-selected')).toBe(false)
    expect(getSelectedBlock(parent)).toBeNull()

    document.body.removeChild(parent)
  })

  it('getSelectedBlock returns the selected element', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)

    const el = createCommandBlock(
      'command',
      1,
      'cmd',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
    )
    parent.appendChild(el)

    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))

    expect(getSelectedBlock(parent)).toBe(el)

    document.body.removeChild(parent)
  })
})

describe('BlockManager', () => {
  let manager: BlockManager
  let inner: HTMLElement
  let xtermContainer: HTMLElement
  let fixedNow: number

  beforeEach(() => {
    _resetThemeState()
    inner = document.createElement('div')
    xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    fixedNow = 1000
    manager = new BlockManager(inner, xtermContainer, {
      now: () => fixedNow,
      snapshotStore: freshStore(),
    })
  })

  it('starts a running block', () => {
    const rec = manager.startBlock('ls -la', '~', 10)
    expect(rec.command).toBe('ls -la')
    expect(rec.status).toBe('running')
    expect(rec.id).toBe(1)
    expect(inner.children.length).toBe(2) // running block + xterm container
    expect(inner.children[0].classList.contains('cmd-block-running')).toBe(true)
    expect(manager.runningBlock).toBe(rec)
  })

  it('inserts the running block before the xterm container', () => {
    manager.startBlock('cmd', '~', 5)
    expect(inner.children[0]).toBe(manager.blocks[0]?.el)
    expect(inner.children[1]).toBe(xtermContainer)
  })

  it('finalizes orphaned running block on next start', () => {
    const first = manager.startBlock('cmd1', '~', 0)
    const second = manager.startBlock('cmd2', '~', 5)
    expect(first.status).toBe('failure')
    expect(manager.runningBlock).toBe(second)
  })

  it('stores blocks in order', () => {
    manager.startBlock('a', '~', 0)
    manager.startBlock('b', '~', 0)
    expect(manager.blocks.length).toBe(2)
    expect(manager.blocks[0].command).toBe('a')
    expect(manager.blocks[1].command).toBe('b')
  })

  it('clearAll removes all blocks and resets state', () => {
    manager.startBlock('test', '~', 0)
    expect(inner.children.length).toBe(2)
    manager.clearAll()
    expect(inner.children.length).toBe(1) // only xterm container remains
    expect(manager.blocks.length).toBe(0)
    expect(manager.runningBlock).toBeNull()
  })

  it('freezeBlock returns null when no running block', () => {
    const result = manager.freezeBlock(() => undefined, 20, 0)
    expect(result).toBeNull()
  })

  it('the author mark survives the running → frozen replacement (nocx-iadtt)', () => {
    const rec = manager.startBlock('ls', '~', 0, undefined, 'agent')
    expect(rec.author).toBe('agent')
    expect(rec.el.querySelector('.ui-badge[data-author="agent"]')).not.toBeNull()
    manager.freezeBlock((y) => new BufferLine('out' + y), 1, 0)
    const frozen = manager.blocks[0]
    expect(frozen?.author).toBe('agent')
    // The visual freeze REPLACES the element — the mark must be re-rendered
    // from the record, never carried over from the discarded running DOM.
    expect(frozen?.el.querySelector('.ui-badge[data-author="agent"]')).not.toBeNull()
  })

  it('dispose clears all', () => {
    manager.startBlock('test', '~', 0)
    manager.dispose()
    expect(manager.blocks.length).toBe(0)
    expect(inner.children.length).toBe(1)
  })

  it('selectedBlockId is null initially', () => {
    expect(manager.selectedBlockId).toBeNull()
  })

  it('deselectAll is safe when nothing is selected', () => {
    manager.startBlock('test', '~', 0)
    expect(() => manager.deselectAll()).not.toThrow()
  })

  it('deselectAll clears selectedBlockId', () => {
    const rec = manager.startBlock('test', '~', 0)
    // Programmatically select: add class + notify manager
    rec.el.classList.add('cmd-block-selected')
    manager._onBlockSelected(rec.id)
    expect(manager.selectedBlockId).toBe(rec.id)
    // Deselect
    manager.deselectAll()
    expect(manager.selectedBlockId).toBeNull()
    expect(rec.el.classList.contains('cmd-block-selected')).toBe(false)
  })

  it('freezeBlock captures theme snapshot at freeze time', () => {
    const themeA = {
      foreground: '#111111',
      background: '#000000',
      black: '#000000',
      red: '#aa0000',
      green: '#00aa00',
      yellow: '#aaaa00',
      blue: '#0000aa',
      magenta: '#aa00aa',
      cyan: '#00aaaa',
      white: '#aaaaaa',
      brightBlack: '#555555',
      brightRed: '#ff5555',
      brightGreen: '#55ff55',
      brightYellow: '#ffff55',
      brightBlue: '#5555ff',
      brightMagenta: '#ff55ff',
      brightCyan: '#55ffff',
      brightWhite: '#ffffff',
      cursor: '#ffffff',
      cursorAccent: '#000000',
      selectionBackground: '#335577',
    }
    const themeB = {
      foreground: '#cccccc',
      background: '#222222',
      black: '#222222',
      red: '#cc0000',
      green: '#00cc00',
      yellow: '#cccc00',
      blue: '#0000cc',
      magenta: '#cc00cc',
      cyan: '#00cccc',
      white: '#cccccc',
      brightBlack: '#666666',
      brightRed: '#ff6666',
      brightGreen: '#66ff66',
      brightYellow: '#ffff66',
      brightBlue: '#6666ff',
      brightMagenta: '#ff66ff',
      brightCyan: '#66ffff',
      brightWhite: '#eeeeee',
      cursor: '#eeeeee',
      cursorAccent: '#222222',
      selectionBackground: '#446688',
    }

    // First block with theme A
    setCurrentTheme(themeA)
    manager.startBlock('cmd1', '~', 0)
    const linesA = [new BufferLine('hello', false)]
    const recA = manager.freezeBlock((y) => linesA[y] ?? undefined, 0, 0)
    expect(recA).not.toBeNull()
    // Defaults are no longer baked in — plain text follows the app's colours —
    // so what this asserts is that the block exists and carries its text, with
    // the palette question moved to serializer.test.ts where a cell actually
    // sets an ANSI colour (nocx-6w4z).
    const outputA = recA!.el.querySelector('.cmd-output')
    expect(outputA?.innerHTML).toContain('hello')
    expect(outputA?.innerHTML).not.toContain('#111111')

    // Second block with theme B
    setCurrentTheme(themeB)
    manager.startBlock('cmd2', '~', 0)
    const linesB = [new BufferLine('world', false)]
    const recB = manager.freezeBlock((y) => linesB[y] ?? undefined, 0, 0)
    expect(recB).not.toBeNull()
    const outputB = recB!.el.querySelector('.cmd-output')
    expect(outputB?.innerHTML).toContain('world')
    expect(outputB?.innerHTML).not.toContain('#cccccc')
    expect(outputB?.innerHTML).not.toContain('#111111')

    // And the first block is still untouched by theme B — which is the property
    // this test is really about. It is asserted by absence now: neither block
    // carries a default colour at all, so a theme change cannot reach into an
    // old block's plain text. Frozen ANSI colours are covered in
    // serializer.test.ts, where a cell actually sets one (nocx-6w4z).
    expect(outputA?.innerHTML).toContain('hello')
    expect(outputA?.innerHTML).not.toContain('#cccccc')
  })
})

describe('overflow menu (P1-6)', () => {
  it('opens menu on ⋮ click', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createCommandBlock(
      'command',
      1,
      'echo hello',
      '~',
      '',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    container.appendChild(el)

    const btn = el.querySelector('.cmd-overflow-btn') as HTMLElement
    expect(btn).not.toBeNull()

    // Click the ⋮ button
    btn.click()

    // Menu should now exist in document.body
    const menu = document.body.querySelector('.cmd-overflow-menu')
    expect(menu).not.toBeNull()

    // Clean up
    menu?.remove()
    document.body.removeChild(container)
  })

  it('closes menu on outside click', async () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createCommandBlock(
      'command',
      1,
      'echo hello',
      '~',
      '',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    container.appendChild(el)

    const btn = el.querySelector('.cmd-overflow-btn') as HTMLElement
    btn.click()

    // Menu should exist
    expect(document.body.querySelector('.cmd-overflow-menu')).not.toBeNull()

    // Wait for the setTimeout(0) that registers the close listener
    await new Promise((r) => setTimeout(r, 10))

    // Click outside
    document.body.click()

    // Menu should be removed
    expect(document.body.querySelector('.cmd-overflow-menu')).toBeNull()

    document.body.removeChild(container)
  })

  it('closes menu on Escape key', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createCommandBlock(
      'command',
      1,
      'echo hello',
      '~',
      '',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    container.appendChild(el)

    const btn = el.querySelector('.cmd-overflow-btn') as HTMLElement
    btn.click()

    expect(document.body.querySelector('.cmd-overflow-menu')).not.toBeNull()

    // Press Escape
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))

    expect(document.body.querySelector('.cmd-overflow-menu')).toBeNull()

    document.body.removeChild(container)
  })

  it('toggles menu closed on second ⋮ click', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createCommandBlock(
      'command',
      1,
      'echo hello',
      '~',
      '',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    container.appendChild(el)

    const btn = el.querySelector('.cmd-overflow-btn') as HTMLElement

    // First click opens
    btn.click()
    expect(document.body.querySelector('.cmd-overflow-menu')).not.toBeNull()

    // Second click closes
    btn.click()
    expect(document.body.querySelector('.cmd-overflow-menu')).toBeNull()

    document.body.removeChild(container)
  })
})

describe('frozen block header highlighting', () => {
  // The frozen header is highlighted by the same Shiki tokenizer as the live
  // editor; the grammar loads asynchronously at module init, so wait for it.
  beforeAll(async () => {
    await shellHighlightReady
  })

  it('highlights the frozen header with the same token classes as the live editor', () => {
    const container = document.createElement('div')
    const el = createCommandBlock(
      'command',
      1,
      'ls -la | grep foo > out.txt',
      '~',
      '',
      '<div></div>',
      100,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    const byClass = new Map<string, string[]>()
    for (const span of el.querySelectorAll<HTMLElement>('.cmd-header-text [class^="tok-"]')) {
      const cls = span.className
      byClass.set(cls, [...(byClass.get(cls) ?? []), span.textContent ?? ''])
    }
    expect(byClass.get('tok-command')).toEqual(['ls', 'grep'])
    expect(byClass.get('tok-flag')).toEqual(['-la'])
    expect(byClass.get('tok-operator')).toEqual(['|', '>'])
    // Bare words after the command are unquoted arguments in the VS Code
    // grammar, so `foo` shares the path role with the redirect target.
    expect(byClass.get('tok-path')).toEqual(['foo', 'out.txt'])
    // The visible text is unchanged by the highlight pass.
    expect(el.querySelector('.cmd-header-text')?.textContent).toBe('ls -la | grep foo > out.txt')
  })

  it('keeps a running header plain (no token spans)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(
      1,
      'ls -la | grep foo > out.txt',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
    )
    expect(el.querySelector('.cmd-header-text')?.textContent).toBe('ls -la | grep foo > out.txt')
    expect(el.querySelectorAll('.cmd-header-text [class^="tok-"]').length).toBe(0)
  })
})

// ── Frozen headers carry command-existence verdicts (OSC 636) ──────────────
// The verdict is read at freeze time from the session snapshot: a header
// frozen before the snapshot arrives keeps no verdict (the one-shot snapshot
// never changes, so a frozen verdict can never go stale).

describe('frozen headers and the command snapshot', () => {
  beforeAll(async () => {
    await shellHighlightReady
  })

  const c = (): HTMLElement => document.createElement('div')
  const SEED_NONCE = 'a1b2c3d4e5f60718293a4b5c6d7e8f90'
  /** A fresh tab store seeded with the given names (or left empty). */
  const makeStore = (names?: string[]): CommandSnapshotStore => {
    const store = freshStore()
    if (names) {
      store.ingest(`H;${SEED_NONCE}`)
      store.ingest(`S;${SEED_NONCE};${names.join(';')}`)
    }
    return store
  }

  it('a frozen header of an unknown command carries the underline class', () => {
    const el = createCommandBlock(
      'command',
      1,
      'sdfsdf',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      c,
      noopSelect,
      makeStore(['pwd']),
    )
    const span = el.querySelector<HTMLElement>('.cmd-header-text span')
    expect(span?.className).toBe('tok-command tok-unresolved')
  })

  it('a frozen header of a known builtin keeps the plain command class', () => {
    const el = createCommandBlock(
      'command',
      2,
      'pwd',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      c,
      noopSelect,
      makeStore(['pwd']),
    )
    const span = el.querySelector<HTMLElement>('.cmd-header-text span')
    expect(span?.className).toBe('tok-command')
  })

  it('with no snapshot a frozen header carries no verdict', () => {
    const el = createCommandBlock(
      'command',
      3,
      'sdfsdf',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      c,
      noopSelect,
      makeStore(),
    )
    const span = el.querySelector<HTMLElement>('.cmd-header-text span')
    expect(span?.className).toBe('tok-command')
  })

  it("a header frozen against one tab's snapshot never sees another tab's names", () => {
    const other = makeStore(['kubectl']) // the sibling tab's session…
    const mine = makeStore(['pwd']) // …vs this tab's session
    expect(other.has('kubectl')).toBe(true)
    expect(mine.has('kubectl')).toBe(false)
    const el = createCommandBlock(
      'command',
      4,
      'kubectl',
      '~',
      '',
      'out',
      10,
      0,
      'success',
      c,
      noopSelect,
      mine,
    )
    const span = el.querySelector<HTMLElement>('.cmd-header-text span')
    expect(span?.className).toBe('tok-command tok-unresolved')
  })
})

describe('a vault reference in a block reads as a chip, not as its own syntax', () => {
  // The editor draws {{secret:NAME}} as a chip and the block drew it raw, so
  // the same command looked like two different things depending on whether
  // it had been submitted yet.
  it('renders the reference as the resolved chip and keeps the text for copy', () => {
    const command = 'curl -H "Authorization: Bearer {{secret:openrouter.ai}}" https://api'
    const container = document.createElement('div')
    const running = createRunningBlock(
      1,
      command,
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
    )
    const el = freezeBlock(
      running,
      1,
      command,
      '~',
      '',
      '<span>ok</span>',
      100,
      0,
      () => container,
      noopSelect,
      freshStore(),
      'success',
    )
    const chip = el.querySelector('.ui-secret-chip')
    expect(chip).not.toBeNull()
    expect(chip?.textContent).toContain('openrouter.ai')
    expect(el.querySelector('.cmd-header-text')?.textContent).not.toContain('{{secret:')
    // Copy still yields the command as typed — the chip is a label.
    expect(el.dataset.recordedCommand).toBe(command)
  })
})

describe('freezeBlock entered presentation (N6, nocx-y5v5)', () => {
  // When a hand-typed ssh enters a remote environment, its block freezes with
  // NO exit code and is painted as neither success nor failure — the bug this
  // must not inherit is freezeBlock deriving 'failure' from a null exit code.

  it('freezeBlock with status entered renders neither success nor failure and no exit code', () => {
    const parent = document.createElement('div')
    const container = document.createElement('div')
    const running = createRunningBlock(
      1,
      'ssh pi@192.168.0.93',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
    )
    parent.appendChild(running)

    const frozen = freezeBlock(
      running,
      1,
      'ssh pi@192.168.0.93',
      '~',
      '',
      '<span>host key prompt</span>',
      3200,
      null,
      () => container,
      noopSelect,
      freshStore(),
      'entered',
    )
    expect(frozen.querySelector('.cmd-header-exit')).toBeNull() // no exit code at all
    expect(frozen.querySelector('.cmd-header-exit-ok')).toBeNull()
    expect(frozen.querySelector('.cmd-header-exit-fail')).toBeNull()
    expect(frozen.querySelector('.cmd-header-spinner')).toBeNull() // frozen, not running
    expect(frozen.classList.contains('cmd-block-entered')).toBe(true)
    expect(frozen.querySelector('.cmd-output')?.innerHTML).toContain('host key prompt')
  })

  it('freezeBlock with status entered never shows an exit chip even if a code were passed', () => {
    const parent = document.createElement('div')
    const container = document.createElement('div')
    const running = createRunningBlock(
      1,
      'ssh host',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
    )
    parent.appendChild(running)
    const frozen = freezeBlock(
      running,
      1,
      'ssh host',
      '~',
      '',
      '',
      100,
      255,
      () => container,
      noopSelect,
      freshStore(),
      'entered',
    )
    expect(frozen.querySelector('.cmd-header-exit')).toBeNull()
    expect(frozen.classList.contains('cmd-block-entered')).toBe(true)
  })
})

describe('BlockManager entered freeze (N6, nocx-y5v5)', () => {
  let manager: BlockManager
  let inner: HTMLElement
  let xtermContainer: HTMLElement

  beforeEach(() => {
    _resetThemeState()
    inner = document.createElement('div')
    xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    manager = new BlockManager(inner, xtermContainer, {
      now: () => 1000,
      snapshotStore: freshStore(),
    })
  })

  it('freezeEntered freezes the running block as entered with no exit code', () => {
    const rec = manager.startBlock('ssh pi@192.168.0.93', '~', 0)
    const entered = manager.freezeEntered(() => undefined, 3)
    expect(entered).not.toBeNull()
    expect(entered!.id).toBe(rec.id)
    expect(entered!.status).toBe('entered')
    expect(entered!.exitCode).toBeNull()
    expect(manager.runningBlock).toBeNull()
    expect(manager.cmdStartTime).toBeNull()
    // The frozen block paints neither success nor failure.
    expect(entered!.el.querySelector('.cmd-header-exit')).toBeNull()
    expect(entered!.el.querySelector('.cmd-header-spinner')).toBeNull()
    expect(entered!.el.classList.contains('cmd-block-entered')).toBe(true)
    // The running block element was replaced in the DOM.
    expect(inner.querySelectorAll('.cmd-block-running').length).toBe(0)
    expect(inner.querySelectorAll('.cmd-block-entered').length).toBe(1)
  })

  it('freezeEntered returns null when no block is running', () => {
    expect(manager.freezeEntered(() => undefined, 0)).toBeNull()
  })

  it('after freezeEntered the next startBlock creates a new running block and leaves the entered one untouched', () => {
    manager.startBlock('ssh pi@192.168.0.93', '~', 0)
    const entered = manager.freezeEntered(() => undefined, 3)
    expect(manager.runningBlock).toBeNull()

    const remote = manager.startBlock('pwd', '~', 5)
    expect(manager.runningBlock).toBe(remote)
    expect(remote.status).toBe('running')
    expect(entered!.status).toBe('entered') // not finalised by the new block
    expect(entered!.exitCode).toBeNull()
    expect(manager.blocks.length).toBe(2)
    expect(inner.querySelectorAll('.cmd-block-running').length).toBe(1)
    expect(inner.querySelectorAll('.cmd-block-entered').length).toBe(1)
  })

  it('a normal D freeze after an entered block still renders success/failure from the real code', () => {
    manager.startBlock('ssh pi@192.168.0.93', '~', 0)
    manager.freezeEntered(() => undefined, 3)
    manager.startBlock('pwd', '~', 5)
    const done = manager.freezeBlock(() => undefined, 8, 0)
    expect(done!.status).toBe('success')
    expect(done!.exitCode).toBe(0)
    expect(done!.el.querySelector('.cmd-header-exit-ok')).not.toBeNull()
    expect(manager.blocks[0].status).toBe('entered') // still untouched
  })
})

describe('BlockManager attempt projections (ADR-0024 §5, §7 — bead nocx-u7uh.7)', () => {
  let manager: BlockManager
  let inner: HTMLElement
  let xtermContainer: HTMLElement

  beforeEach(() => {
    _resetThemeState()
    inner = document.createElement('div')
    xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    manager = new BlockManager(inner, xtermContainer, {
      now: () => 1000,
      snapshotStore: freshStore(),
    })
  })

  const domain = mintDomain({
    lane: 'l',
    lifecycle: 'prompt_ready',
    domain: 'd1',
    epoch: 1,
  }) as IntegrationDomain
  const FENCE = 'a'.repeat(64)
  const attempt = (over: Partial<ExecutionAttempt> = {}): ExecutionAttempt => ({
    id: 'att-1',
    domain,
    state: 'completed',
    exitCode: 0,
    fence: FENCE,
    ...over,
  })

  it('bindAttempt ties the running block to the attempt; freezeFromAttempt freezes it with the authenticated status', () => {
    const rec = manager.startBlock('make', '~', 0)
    manager.bindAttempt('att-1')
    expect(rec.attemptId).toBe('att-1')
    expect(manager.blockForAttempt('att-1')).toBe(rec)

    // The fence landed before the completion: the rendezvous is complete
    // and the freeze lands at the fence's line.
    manager.sightFence(FENCE, 8)
    const frozen = manager.freezeFromAttempt(
      attempt({ exitCode: 0 }),
      () => undefined,
      8,
      () => 9,
    )
    expect(frozen).not.toBeNull()
    expect(frozen!.status).toBe('success')
    expect(frozen!.exitCode).toBe(0)
    expect(frozen!.attemptId).toBe('att-1')
    expect(manager.runningBlock).toBeNull()
    expect(manager.blockForAttempt('att-1')).toBe(frozen)
  })

  it('freezeFromAttempt refuses a non-completed attempt — an open attempt cannot freeze a block', () => {
    manager.startBlock('make', '~', 0)
    manager.bindAttempt('att-1')
    expect(
      manager.freezeFromAttempt(
        attempt({ state: 'open' }),
        () => undefined,
        8,
        () => 9,
      ),
    ).toBeNull()
    expect(manager.runningBlock?.status).toBe('running')
  })

  it('freezeFromAttempt refuses when the running block is bound to a different attempt', () => {
    manager.startBlock('make', '~', 0)
    manager.bindAttempt('att-1')
    expect(
      manager.freezeFromAttempt(
        attempt({ id: 'att-other' }),
        () => undefined,
        8,
        () => 9,
      ),
    ).toBeNull()
    expect(manager.runningBlock?.status).toBe('running')
  })

  it('abandonAttempt freezes the bound block as unknown — never successful, no exit code', () => {
    const rec = manager.startBlock('sleep 100', '~', 0)
    manager.bindAttempt('att-1')
    const frozen = manager.abandonAttempt(attempt({ state: 'unknown' }), () => undefined, 6)
    expect(frozen).not.toBeNull()
    expect(frozen!.status).toBe('unknown')
    expect(frozen!.exitCode).toBeNull()
    expect(frozen!.el.querySelector('.cmd-header-exit')).toBeNull()
    expect(manager.runningBlock).toBeNull()
    expect(rec.attemptId).toBe('att-1')
  })

  it('abandonAttempt refuses a completed attempt and a foreign binding', () => {
    manager.startBlock('make', '~', 0)
    manager.bindAttempt('att-1')
    expect(manager.abandonAttempt(attempt(), () => undefined, 6)).toBeNull()
    expect(
      manager.abandonAttempt(attempt({ id: 'att-other', state: 'unknown' }), () => undefined, 6),
    ).toBeNull()
    expect(manager.runningBlock?.status).toBe('running')
  })

  it('clearAll drops the attempt binding', () => {
    manager.startBlock('make', '~', 0)
    manager.bindAttempt('att-1')
    manager.clearAll()
    expect(manager.blockForAttempt('att-1')).toBeNull()
  })
})

describe('the render fence rendezvous (ADR-0024 §7 carve-out, bead nocx-u7uh.8)', () => {
  let manager: BlockManager
  let inner: HTMLElement
  let xtermContainer: HTMLElement

  beforeEach(() => {
    _resetThemeState()
    inner = document.createElement('div')
    xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    manager = new BlockManager(inner, xtermContainer, {
      now: () => 1000,
      snapshotStore: freshStore(),
    })
  })

  const domain = mintDomain({
    lane: 'l',
    lifecycle: 'prompt_ready',
    domain: 'd1',
    epoch: 1,
  }) as IntegrationDomain
  const FENCE_A = 'a'.repeat(64)
  const FENCE_B = 'b'.repeat(64)
  const attempt = (over: Partial<ExecutionAttempt> = {}): ExecutionAttempt => ({
    id: 'att-1',
    domain,
    state: 'completed',
    exitCode: 0,
    fence: FENCE_A,
    ...over,
  })

  /** THE acceptance case: the last output bytes land AFTER the authenticated
   *  completion. The fence proves where the output ended, so the block
   *  contains ALL of it — truncation is the defect this bead exists to fix.
   *  The two halves settle at DIFFERENT times: the status flips on the
   *  completion event alone, the output boundary only when the fence lands. */
  it('output delayed past the completion is captured in full once the fence lands', () => {
    manager.startBlock('slow', '~', 0)
    manager.bindAttempt('att-1')
    const lines = [new BufferLine('first'), new BufferLine('second'), new BufferLine('the tail')]
    const getLine = (y: number) => lines[y]

    // The completion event arrives while only the first two lines are in the
    // buffer, and the fence has NOT been sighted. The LOGICAL freeze lands
    // NOW, on the authenticated event alone: the status flips and the running
    // slot is freed. (The deferred return means the caller keeps the live
    // region up — the boundary is still in flight.)
    const frozen = manager.freezeFromAttempt(attempt(), getLine, 1, () => 3)
    expect(frozen).toBeNull()
    expect(manager.runningBlock).toBeNull()
    const block = manager.blockForAttempt('att-1')
    expect(block).not.toBeNull()
    expect(block!.status).toBe('success')
    expect(block!.exitCode).toBe(0)

    // ...but the output boundary has NOT landed yet: no rows are serialized
    // (the running element has no output region) and the end line is still
    // the start. Status first, boundary later — the different-times split.
    expect(block!.el.querySelector('.cmd-output')).toBeNull()
    expect(block!.endLine).toBe(0)

    // The tail and the fence land together: the fence line IS the output
    // end, and every line up to it is serialized into the block.
    manager.sightFence(FENCE_A, 2)
    expect(manager.runningBlock).toBeNull()
    expect(block!.endLine).toBe(2)
    const text = blockOutputText(block!.el)
    expect(text).toContain('first')
    expect(text).toContain('second')
    expect(text).toContain('the tail')
  })

  it('a fence with no authenticated event behind it changes nothing at all', () => {
    // No block, no attempt: the sighting is remembered for a future match
    // and freezes nothing.
    manager.sightFence(FENCE_A, 3)
    expect(manager.runningBlock).toBeNull()
    expect(manager.blocks).toHaveLength(0)

    // Even with a block running, a foreign fence never freezes it.
    manager.startBlock('cmd', '~', 0)
    manager.bindAttempt('att-1')
    manager.sightFence(FENCE_B, 4)
    expect(manager.runningBlock?.status).toBe('running')
  })

  it('a replayed fence — the same value twice, or one for an already-frozen block — does nothing', () => {
    manager.startBlock('cmd', '~', 0)
    manager.bindAttempt('att-1')

    // Sighted once, then the same bytes again: the second sighting is a
    // replay and does nothing (the line is not even overwritten).
    manager.sightFence(FENCE_A, 3)
    manager.sightFence(FENCE_A, 9)
    const frozen = manager.freezeFromAttempt(
      attempt(),
      () => undefined,
      0,
      () => 9,
    )
    expect(frozen).not.toBeNull()
    expect(frozen!.endLine).toBe(3) // the ORIGINAL sighting's line, not the replay's

    // The same fence again after the block froze: an already-frozen block's
    // fence changes nothing.
    manager.sightFence(FENCE_A, 10)
    expect(manager.runningBlock).toBeNull()
    expect(manager.blocks).toHaveLength(1)
    expect(manager.blockForAttempt('att-1')!.endLine).toBe(3)
  })

  it('a completion whose fence never arrives defers the boundary, then settles at the current output end', () => {
    vi.useFakeTimers()
    try {
      manager.startBlock('cmd', '~', 0)
      manager.bindAttempt('att-1')

      // Completion at endLine 0 with the fence still in flight: the STATUS
      // flips now on the event alone; the boundary defers — the block is
      // NOT serialized at the truncated event-time end.
      const frozen = manager.freezeFromAttempt(
        attempt(),
        () => undefined,
        0,
        () => 10,
      )
      expect(frozen).toBeNull()
      expect(manager.runningBlock).toBeNull()
      expect(manager.blockForAttempt('att-1')!.status).toBe('success')

      // The whole deferral window passes with no fence: the freeze settles
      // at the CURRENT output end (10), where the in-flight tail has landed.
      vi.advanceTimersByTime(FENCE_DEFER_MS)
      expect(manager.runningBlock).toBeNull()
      const block = manager.blockForAttempt('att-1')
      expect(block).not.toBeNull()
      expect(block!.status).toBe('success')
      expect(block!.endLine).toBe(10)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a completion that carries NO fence at all still defers — the boundary is never cut on the event alone', () => {
    vi.useFakeTimers()
    try {
      manager.startBlock('cmd', '~', 0)
      manager.bindAttempt('att-1')

      // No fence on the attempt (unreachable from the kernel, which
      // requires the nonce on completed attempts, but the manager guards
      // callers that bypass it): the status flips now; the boundary defers
      // instead of truncating at the event-time output end.
      const frozen = manager.freezeFromAttempt(
        attempt({ fence: undefined }),
        () => undefined,
        0,
        () => 10,
      )
      expect(frozen).toBeNull()
      expect(manager.runningBlock).toBeNull()
      expect(manager.blockForAttempt('att-1')!.status).toBe('success')

      // No sighting can match a null-hex pending — a stray fence lands in
      // the sighting ring and changes nothing.
      manager.sightFence('ff'.repeat(32), 5)
      expect(manager.runningBlock).toBeNull()
      expect(manager.blockForAttempt('att-1')!.status).toBe('success')

      // The deferral window settles it at the CURRENT output end, where the
      // in-flight tail has landed — the same degrade as a fence that never
      // arrives, never a truncation.
      vi.advanceTimersByTime(FENCE_DEFER_MS)
      expect(manager.runningBlock).toBeNull()
      const block = manager.blockForAttempt('att-1')
      expect(block).not.toBeNull()
      expect(block!.status).toBe('success')
      expect(block!.endLine).toBe(10)
    } finally {
      vi.useRealTimers()
    }
  })

  it('the deferral window is a named policy — FENCE_DEFER_MS — not a magic number', () => {
    expect(FENCE_DEFER_MS).toBeGreaterThan(0)
  })

  it('onDeferredFreeze fires when the sighting resolves the pending freeze', () => {
    const onDeferredFreeze = vi.fn()
    manager = new BlockManager(inner, xtermContainer, {
      now: () => 1000,
      snapshotStore: freshStore(),
      onDeferredFreeze,
    })
    manager.startBlock('cmd', '~', 0)
    manager.bindAttempt('att-1')
    manager.freezeFromAttempt(
      attempt(),
      () => undefined,
      0,
      () => 9,
    )
    expect(onDeferredFreeze).not.toHaveBeenCalled()
    manager.sightFence(FENCE_A, 4)
    expect(onDeferredFreeze).toHaveBeenCalledTimes(1)
  })

  it('clearAll cancels a pending deferral — the block is gone, the timer fires into nothing', () => {
    vi.useFakeTimers()
    try {
      manager.startBlock('cmd', '~', 0)
      manager.bindAttempt('att-1')
      manager.freezeFromAttempt(
        attempt(),
        () => undefined,
        0,
        () => 9,
      )
      manager.clearAll()
      vi.advanceTimersByTime(FENCE_DEFER_MS)
      expect(manager.blocks).toHaveLength(0)
      expect(manager.runningBlock).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('the serialized output range vs the block creation line (nocx-4yhi)', () => {
  // The app-owned submit opens the block BEFORE the bytes go out, so the
  // shell's echo of the typed command lands on the creation line itself.
  // The block's OUTPUT range therefore starts one row after it — the
  // header already shows the command, and a body that repeats it is the
  // defect this describe pins. Shell-originated blocks open at the cursor
  // line at fact time, which is already past the echo: their output range
  // starts where the block opened.
  let manager: BlockManager
  let inner: HTMLElement
  let xtermContainer: HTMLElement

  beforeEach(() => {
    _resetThemeState()
    inner = document.createElement('div')
    xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    manager = new BlockManager(inner, xtermContainer, {
      snapshotStore: freshStore(),
    })
  })

  it('serializes from outputStart when the creation line carries the shell echo', () => {
    const rec = manager.startBlock('ls', '~', 5, 6)
    expect(rec.outputStart).toBe(6)
    // Line 5 is the prompt line the echo lands on; 6-7 are the output.
    const lines = [
      new BufferLine('$ ls'),
      new BufferLine('file1'),
      new BufferLine('file2'),
      new BufferLine(''),
    ]
    const getLine = (y: number) => lines[y - 5]
    const frozen = manager.freezeBlock(getLine, 8, 0)
    expect(frozen).not.toBeNull()
    const text = blockOutputText(frozen!.el)
    expect(text).toContain('file1')
    expect(text).toContain('file2')
    expect(text).not.toContain('$ ls')
  })

  it('defaults the output range to the creation line — the shell-originated case', () => {
    // The running fact lands after the echo (the user typed at the shell),
    // so the cursor line is already past it and the block serializes from
    // exactly where it opened.
    const rec = manager.startBlock('pwd', '~', 7)
    expect(rec.outputStart).toBe(7)
    const getLine = (y: number) => (y === 7 ? new BufferLine('out1') : undefined)
    const frozen = manager.freezeBlock(getLine, 7, 0)
    expect(frozen).not.toBeNull()
    const text = blockOutputText(frozen!.el)
    expect(text).toContain('out1')
  })
})

// ── Answer blocks (nocx-x8s2.2) ───────────────────────────────────────────

describe('BlockManager.addAnswerBlock', () => {
  function newManager() {
    const inner = document.createElement('div')
    const xtermContainer = document.createElement('div')
    // The manager inserts blocks BEFORE the xterm container, so the
    // container must already be a child (the mount path attaches both).
    inner.appendChild(xtermContainer)
    const manager = new BlockManager(inner, xtermContainer, {
      snapshotStore: freshStore(),
    })
    return { inner, manager }
  }

  it('renders a selectable block with the question as its header', () => {
    const { inner, manager } = newManager()
    manager.addAnswerBlock('what does this mean?', '/repo')
    expect(inner.querySelector('.cmd-block')).not.toBeNull()
    const text = inner.querySelector('.cmd-header-text')?.textContent
    expect(text).toBe('what does this mean?')
    expect(manager.selectedBlockId).toBeNull()
  })

  it('appends streamed chunks, continuing the partial line across chunks', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('hello ')
    h.append('world')
    const rows = Array.from(h.el.querySelectorAll('.term-line')).map((r) => r.textContent)
    expect(rows).toEqual(['hello world'])
  })

  it('a chunk ending in \\n starts a NEW row; the next chunk does not merge ("a\\n" + "b" = two rows)', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('a\n')
    h.append('b')
    const rows = Array.from(h.el.querySelectorAll('.term-line')).map((r) => r.textContent)
    expect(rows).toEqual(['a', 'b'])
  })

  it('interior blank lines are preserved ("a\\n\\nb")', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('a\n\nb')
    const rows = Array.from(h.el.querySelectorAll('.term-line')).map((r) => r.textContent)
    expect(rows).toEqual(['a', '', 'b'])
  })

  it('close marks the status; failed appends the renderable reason; a trailing \\n trims the empty row', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('partial\n')
    h.close('failure', 'the model returned no text')
    const rows = Array.from(h.el.querySelectorAll('.term-line')).map((r) => r.textContent)
    expect(rows).toEqual(['partial'])
    expect(h.el.querySelector('.cmd-answer-error')?.textContent).toBe('the model returned no text')
    const chip = h.el.querySelector('.cmd-header-exit')
    expect(chip?.textContent).toBe('failed')
  })

  // nocx-e6kn2 acceptance: the person must be able to tell which model
  // answered. The pinned model rides the ask result and close names it on
  // the block; a close without a model (failure, or an older caller) keeps
  // the block unadorned rather than inventing an attribution.
  it('close with the model renders the provenance line on success', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('the answer')
    h.close('success', undefined, 'gpt-4o')
    expect(h.el.querySelector('.cmd-answer-provenance')?.textContent).toBe('answered by gpt-4o')
  })

  it('a success close WITHOUT a model renders no provenance — nobody is named who did not answer', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('the answer')
    h.close('success')
    expect(h.el.querySelector('.cmd-answer-provenance')).toBeNull()
  })

  it('clearAll removes answer blocks too', () => {
    const { inner, manager } = newManager()
    manager.addAnswerBlock('q', '/')
    manager.addAnswerBlock('q2', '/')
    manager.clearAll()
    expect(inner.querySelectorAll('.cmd-block').length).toBe(0)
  })
})

// ── The block kind (nocx-ex636) ────────────────────────────────────────────
// A block declares its kind once; highlighting, wrapping and the status
// vocabulary are read from it — a fourth kind must declare itself or fail,
// never inherit the command rules by accident.
describe('the block kind owns the grammar (nocx-ex636)', () => {
  beforeAll(async () => {
    await shellHighlightReady
  })

  function newManager() {
    const inner = document.createElement('div')
    const xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    const manager = new BlockManager(inner, xtermContainer, { snapshotStore: freshStore() })
    return { inner, manager }
  }

  it('declares the kind on the block: ask vs command, distinguishable without reading the text', () => {
    const { manager } = newManager()
    const answer = manager.addAnswerBlock('question?', '/')
    manager.startBlock('ls', '~', 0)
    const frozen = manager.freezeBlock((y) => (y === 0 ? new BufferLine('out') : undefined), 0, 0)
    expect(answer.el.dataset.blockKind).toBe('ask')
    expect(frozen!.el.dataset.blockKind).toBe('command')
    // The visible difference in the flow: the ask block's header names its
    // in-progress state; a command block's never does.
    expect(answer.el.querySelector('.cmd-answer-waiting')).not.toBeNull()
    expect(frozen!.el.querySelector('.cmd-answer-waiting')).toBeNull()
  })

  it('the kind rules are read from one table; a kind that declares nothing fails loudly', () => {
    const command = blockKindRules('command')
    const ask = blockKindRules('ask')
    expect(command.highlightHeader).toBe(true)
    expect(command.outputClass).toBe('cmd-output')
    expect(command.statusChips).toBeNull()
    expect(ask.highlightHeader).toBe(false)
    expect(ask.outputClass).toBe('cmd-output cmd-output-ask')
    expect(ask.statusChips).toEqual({
      inProgress: 'thinking',
      done: 'completed',
      failed: 'failed',
    })
    expect(() => blockKindRules('diary' as BlockKind)).toThrow(/unknown block kind/)
  })

  it('a question renders as prose, never through the shell lexer (non-ASCII question)', () => {
    const { inner, manager } = newManager()
    // A highlighter that only knows shell tokens must not pass by accident:
    // «нужен какой-то индикатор?» would colour «нужен» as a command.
    const question = 'нужен какой-то индикатор? Подсветка — странное решение'
    manager.addAnswerBlock(question, '/')
    const text = inner.querySelector<HTMLElement>('.cmd-header-text')!
    expect(text.textContent).toBe(question)
    // The shell pass emits tok-* spans; prose must contain none.
    expect(text.querySelector('.tok-command, .tok-string, [class*="tok-"]')).toBeNull()
    expect(text.querySelector('span')).toBeNull()
  })

  it('a command header still runs through the shell lexer', () => {
    const container = document.createElement('div')
    const el = createCommandBlock(
      'command',
      1,
      'git push',
      '~',
      '',
      '<span class="term-line">ok</span>',
      10,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    expect(el.querySelector('.cmd-header-text')?.querySelector('.tok-command')).not.toBeNull()
  })

  it('the answer body carries the ask kind wrapping class', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    const body = h.el.querySelector('.cmd-output')
    expect(body?.className).toBe('cmd-output cmd-output-ask')
  })

  it('says it is thinking between submit and the first delta, and stops on the first delta', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    const waiting = h.el.querySelector('.cmd-answer-waiting')
    expect(waiting?.textContent).toBe('thinking')
    h.append('first')
    expect(h.el.querySelector('.cmd-answer-waiting')).toBeNull()
  })

  it('a run that fails before any delta stops waiting and says failed', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    expect(h.el.querySelector('.cmd-answer-waiting')).not.toBeNull()
    h.close('failure', 'the model did not answer in time')
    expect(h.el.querySelector('.cmd-answer-waiting')).toBeNull()
    expect(h.el.querySelector('.cmd-header-exit')?.textContent).toBe('failed')
    expect(h.el.querySelector('.cmd-answer-error')?.textContent).toBe(
      'the model did not answer in time',
    )
  })

  it('terminal output inside an answer lands in a fenced, unwrapped container; prose stays outside', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('before\n```\nprintf "hi"\n```\nafter')
    h.close('success')
    const code = h.el.querySelector('.cmd-output-code')
    expect(code).not.toBeNull()
    // Both delimiters belong to the code region.
    expect(Array.from(code!.querySelectorAll('.term-line')).map((r) => r.textContent)).toEqual([
      '```',
      'printf "hi"',
      '```',
    ])
    // Prose rows are the body's own children, never inside the code block.
    const prose = Array.from(h.el.querySelectorAll('.cmd-output > .term-line')).map(
      (r) => r.textContent,
    )
    expect(prose).toEqual(['before', 'after'])
    // Copying the block returns the whole answer, fence markers included.
    expect(blockOutputText(h.el)).toBe('before\n```\nprintf "hi"\n```\nafter')
  })

  it('fence → prose → fence keeps each fence in its own container, after the prose', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('```\ncode1\n```\nbetween\n```\ncode2\n```')
    h.close('success')
    const codeBlocks = h.el.querySelectorAll('.cmd-output-code')
    expect(codeBlocks.length).toBe(2)
    expect(
      Array.from(codeBlocks[0].querySelectorAll('.term-line')).map((r) => r.textContent),
    ).toEqual(['```', 'code1', '```'])
    expect(
      Array.from(codeBlocks[1].querySelectorAll('.term-line')).map((r) => r.textContent),
    ).toEqual(['```', 'code2', '```'])
    expect(blockOutputText(h.el)).toBe('```\ncode1\n```\nbetween\n```\ncode2\n```')
  })

  it('a fence marker split across chunks still toggles', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('``')
    h.append('`\ncode')
    h.close('success')
    expect(h.el.querySelector('.cmd-output-code')).not.toBeNull()
    expect(blockOutputText(h.el)).toBe('```\ncode')
  })

  it('Copy output and Copy all read the answer through the block, fence included', () => {
    const copied: string[] = []
    Object.defineProperty(navigator, 'clipboard', {
      value: {
        writeText: vi.fn((t: string) => {
          copied.push(t)
          return Promise.resolve()
        }),
      },
      configurable: true,
    })
    const { manager } = newManager()
    const h = manager.addAnswerBlock('question?', '/')
    h.append('answer prose\n```\ncode\n```')
    h.close('success')

    const openMenu = () => {
      h.el
        .querySelector<HTMLElement>('.cmd-overflow-btn')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      return document.body.querySelector<HTMLElement>('.cmd-overflow-menu')!
    }
    const clickItem = (menu: HTMLElement, label: string) => {
      Array.from(menu.querySelectorAll<HTMLElement>('.cmd-overflow-menu-item'))
        .find((b) => b.textContent === label)!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    }

    clickItem(openMenu(), 'Copy output')
    expect(copied[0]).toBe('answer prose\n```\ncode\n```')

    clickItem(openMenu(), 'Copy all')
    expect(copied[1]).toBe('question?\nanswer prose\n```\ncode\n```')
  })

  // The wrap override lives in the ⋮ menu because it is the exception: the
  // kind is right nearly always, and this is for the one wide table or the
  // one answer somebody wants exactly as it arrived. It is a state on the
  // BLOCK, so the kind's own rule stays the default underneath it.
  it('the overflow menu toggles wrap per block, and the label says what the click will do', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('question?', '/')
    h.append('answer prose')
    h.close('success')

    const openMenu = () => {
      h.el
        .querySelector<HTMLElement>('.cmd-overflow-btn')!
        .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      return document.body.querySelector<HTMLElement>('.cmd-overflow-menu')!
    }
    const item = (menu: HTMLElement, label: string) =>
      Array.from(menu.querySelectorAll<HTMLElement>('.cmd-overflow-menu-item')).find(
        (b) => b.textContent === label,
      )

    // No override until somebody asks for one: the kind decides.
    expect(h.el.hasAttribute('data-wrap')).toBe(false)

    const menu = openMenu()
    const on = item(menu, 'Wrap lines')
    expect(on, 'the menu offers wrapping').toBeTruthy()
    on!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(h.el.getAttribute('data-wrap')).toBe('on')

    // Re-opened, the item names the OTHER direction — a toggle whose label
    // does not change is a control you have to try to understand.
    const menu2 = openMenu()
    expect(item(menu2, 'Wrap lines')).toBeUndefined()
    const off = item(menu2, 'Do not wrap')
    expect(off).toBeTruthy()
    off!.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    expect(h.el.getAttribute('data-wrap')).toBe('off')
  })
})

// ── No per-block ask control (nocx-4wtlh) ─────────────────────────────────
// The ask entry is the gesture at the prompt (⌘Enter + the caret
// indicator), not a button on every finished block. A finished block — the
// exact construction that used to carry `.cmd-ask-btn` — renders no ask
// control; running blocks and answer blocks never had one either.
describe('no finished block renders an ask control (nocx-4wtlh)', () => {
  it('a frozen block carries no ask control', () => {
    const container = document.createElement('div')
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '~',
      '',
      '<span class="term-line">out</span>',
      10,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
    )
    expect(el.querySelector('.cmd-ask-btn')).toBeNull()
    expect(el.querySelector('[aria-label="Ask about this block"]')).toBeNull()
  })

  it('running blocks and answer blocks carry no ask control either', () => {
    const container = document.createElement('div')
    const running = createRunningBlock(2, 'ls', '~', '', () => container, noopSelect, freshStore())
    expect(running.querySelector('.cmd-ask-btn')).toBeNull()

    const inner = document.createElement('div')
    const xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    const manager = new BlockManager(inner, xtermContainer, { snapshotStore: freshStore() })
    manager.startBlock('ls', '~', 0)
    const frozen = manager.freezeBlock((y) => (y === 0 ? new BufferLine('out') : undefined), 0, 0)
    expect(frozen).not.toBeNull()
    expect(frozen!.el.querySelector('.cmd-ask-btn')).toBeNull()
    const answer = manager.addAnswerBlock('a question', '/')
    expect(answer.el.querySelector('.cmd-ask-btn')).toBeNull()
  })
})

it('blockCommandText reads the header, and the recorded command when the ack landed', () => {
  const container = document.createElement('div')
  const el = createCommandBlock(
    'command',
    1,
    'ssh pi@host',
    '~',
    '',
    '',
    10,
    0,
    'success',
    () => container,
    noopSelect,
    freshStore(),
  )
  expect(blockCommandText(el)).toBe('ssh pi@host')
  el.dataset.recordedCommand = 'ssh pi@***'
  expect(blockCommandText(el)).toBe('ssh pi@***')
})

it('selectBlock is a non-toggle single-select: the id and the class move together', () => {
  const inner = document.createElement('div')
  const xtermContainer = document.createElement('div')
  inner.appendChild(xtermContainer)
  const manager = new BlockManager(inner, xtermContainer, { snapshotStore: freshStore() })
  manager.startBlock('a', '~', 0)
  const a = manager.freezeBlock((y) => (y === 0 ? new BufferLine('a') : undefined), 0, 0)!.el
  manager.startBlock('b', '~', 0)
  const b = manager.freezeBlock((y) => (y === 0 ? new BufferLine('b') : undefined), 0, 0)!.el

  manager.selectBlock(a)
  expect(a.classList.contains('cmd-block-selected')).toBe(true)
  expect(manager.selectedBlockId).toBe(manager.blocks.find((r) => r.el === a)?.id ?? null)

  // Selecting the SAME block again does not toggle it off.
  manager.selectBlock(a)
  expect(a.classList.contains('cmd-block-selected')).toBe(true)

  // Selecting another moves the selection.
  manager.selectBlock(b)
  expect(a.classList.contains('cmd-block-selected')).toBe(false)
  expect(b.classList.contains('cmd-block-selected')).toBe(true)
  expect(manager.selectedBlockId).toBe(manager.blocks.find((r) => r.el === b)?.id ?? null)
})

// ── What the freeze keeps for the store (nocx-2f0f) ───────────────────────
describe('the visual freeze parks the durable bodies', () => {
  let inner: HTMLElement
  let xtermContainer: HTMLElement
  let manager: BlockManager

  beforeEach(() => {
    _resetThemeState()
    inner = document.createElement('div')
    xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    manager = new BlockManager(inner, xtermContainer, {
      now: () => 1000,
      snapshotStore: freshStore(),
      dimensions: () => ({ cols: 100, rows: 30 }),
    })
  })

  it('keeps the rows as SGR and as characters, with the grid it saw', () => {
    manager.startBlock('echo hi', '~', 0)
    const lines = [new BufferLine('hi', false)]
    const rec = manager.freezeBlock((y) => lines[y] ?? undefined, 0, 0)
    expect(rec).not.toBeNull()
    expect(rec?.captured).toEqual({ sgr: 'hi', text: 'hi', cols: 100, rows: 30 })
  })

  it('keeps an EMPTY body for a command that printed nothing, rather than none', () => {
    // An alt-screen program leaves no scrollback rows, and so does `true`.
    // Nothing here tells them apart and nothing may: a classifier in the
    // capture path is the defect the byte-stream design was withdrawn over.
    // An empty body says "this printed nothing into the scrollback", which
    // is true of both; NO artifact is reserved for "nothing was captured",
    // which is a different sentence a restored block has to be able to say.
    manager.startBlock('htop', '~', 0)
    const rec = manager.freezeBlock(() => undefined, 0, 0)
    expect(rec?.captured).toEqual({ sgr: '', text: '', cols: 100, rows: 30 })
  })

  it("gives each of two blocks frozen back to back its own rows and none of the other's", () => {
    // The epic's own criterion, and the one a boundary bug shows up in.
    // Asserted by FREEZING TWO BLOCKS, not by feeding frames: the boundary is
    // the block's own line range, and a test that fed bytes would be testing
    // the recognizer that was deleted rather than the rule that replaced it.
    const lines = [new BufferLine('first output', false), new BufferLine('second output', false)]
    const getLine = (y: number) => lines[y] ?? undefined

    manager.startBlock('echo first', '~', 0)
    const a = manager.freezeBlock(getLine, 0, 0)
    manager.startBlock('echo second', '~', 1)
    const b = manager.freezeBlock(getLine, 1, 1)

    expect(a?.captured?.text).toBe('first output')
    expect(a?.captured?.text).not.toContain('second')
    expect(b?.captured?.text).toBe('second output')
    expect(b?.captured?.text).not.toContain('first')
  })

  it('parks nothing when the caller supplies no grid, because provenance is not optional', () => {
    const otherInner = document.createElement('div')
    const otherXterm = document.createElement('div')
    otherInner.appendChild(otherXterm)
    document.body.appendChild(otherInner)
    const noDims = new BlockManager(otherInner, otherXterm, {
      now: () => 1000,
      snapshotStore: freshStore(),
    })
    noDims.startBlock('echo hi', '~', 0)
    const lines = [new BufferLine('hi', false)]
    const rec = noDims.freezeBlock((y) => lines[y] ?? undefined, 0, 0)
    expect(rec?.captured).toBeUndefined()
  })
})
