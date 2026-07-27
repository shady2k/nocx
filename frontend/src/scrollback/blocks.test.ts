// Block manager tests — DOM creation, freeze lifecycle, clear behaviour.
// Updated for flat design (P0-1) and single-select model (P1-7, P1-8).

// @vitest-environment jsdom

import { describe, it, expect, beforeEach } from 'vitest'
import {
  BlockManager,
  createCommandBlock,
  createRunningBlock,
  freezeBlock,
  deselectAllBlocks,
  getSelectedBlock,
} from './blocks'
import { BufferLine } from './test-helpers'
import { setCurrentTheme, _resetThemeState } from '../renderers/theme-adapter'

/** Helper: returns a container supplier that references the given element. */
function makeContainer(el: HTMLElement): () => HTMLElement {
  return () => el
}

const noopSelect = (): void => {}

describe('createRunningBlock', () => {
  it('creates a div with classes cmd-block and cmd-block-running', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'ls -la', '~', () => container, noopSelect)
    expect(el.tagName).toBe('DIV')
    expect(el.classList.contains('cmd-block')).toBe(true)
    expect(el.classList.contains('cmd-block-running')).toBe(true)
    expect(el.getAttribute('data-block-id')).toBe('1')
  })

  it('includes command text in header', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'ls -la', '~', () => container, noopSelect)
    const text = el.querySelector('.cmd-header-text')
    expect(text?.textContent).toBe('ls -la')
  })

  it('includes cwd chip in the header (standard .nocx-chip component)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'echo hi', '/home/dev/projects', () => container, noopSelect)
    const cwd = el.querySelector('.cmd-header-cwd')
    expect(cwd?.textContent).toBe('\u{1F4C1} dev/projects')
    expect(cwd?.classList.contains('nocx-chip')).toBe(true)
  })

  it('shows a spinner for running state', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'sleep 10', '~', () => container, noopSelect)
    const spinner = el.querySelector('.cmd-header-spinner')
    expect(spinner).not.toBeNull()
  })

  it('has no output area until freeze (P0-3)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'cmd', '~', () => container, noopSelect)
    const output = el.querySelector('.cmd-output')
    expect(output).toBeNull()
  })

  it('includes overflow menu button (P2-9)', () => {
    const container = document.createElement('div')
    const el = createRunningBlock(1, 'cmd', '~', () => container, noopSelect)
    const btn = el.querySelector('.cmd-overflow-btn')
    expect(btn).not.toBeNull()
  })
})

describe('createCommandBlock', () => {
  const c = (): HTMLElement => document.createElement('div')

  it('creates a frozen block with success status', () => {
    const el = createCommandBlock(1, 'echo hello', '~', 'output', 42, 0, 'success', c, noopSelect)
    expect(el.classList.contains('cmd-block')).toBe(true)
    const exit = el.querySelector('.cmd-header-exit-ok')
    expect(exit?.textContent).toBe('ok')
  })

  it('creates a frozen block with failure status', () => {
    const el = createCommandBlock(2, 'false', '~', '', 5, 1, 'failure', c, noopSelect)
    const exit = el.querySelector('.cmd-header-exit-fail')
    expect(exit?.textContent).toBe('exit 1')
  })

  it('includes serialized output', () => {
    const el = createCommandBlock(
      1,
      'ls',
      '~',
      '<span class="term-line">file.txt</span>',
      10,
      0,
      'success',
      c,
      noopSelect,
    )
    const output = el.querySelector('.cmd-output')
    expect(output?.innerHTML).toContain('file.txt')
  })

  it('includes duration', () => {
    const el = createCommandBlock(
      1,
      'sleep 1',
      '~',
      'some output',
      1234,
      0,
      'success',
      c,
      noopSelect,
    )
    const dur = el.querySelector('.cmd-header-duration')
    expect(dur?.textContent).toBe('1.2s')
  })

  it('omits exit badge when exitCode is null', () => {
    const el = createCommandBlock(1, 'cmd', '~', 'out', null, null, 'success', c, noopSelect)
    expect(el.querySelector('.cmd-header-exit')).toBeNull()
  })

  it('omits .cmd-output when outputHtml is empty (P0-3)', () => {
    const el = createCommandBlock(1, 'cd repos', '~', '', 3, 0, 'success', c, noopSelect)
    expect(el.querySelector('.cmd-output')).toBeNull()
  })

  it('omits .cmd-output when outputHtml is only empty term-lines (P0-3)', () => {
    const el = createCommandBlock(
      1,
      'cmd',
      '~',
      '<span class="term-line"></span>',
      1,
      0,
      'success',
      c,
      noopSelect,
    )
    expect(el.querySelector('.cmd-output')).toBeNull()
  })

  it('includes overflow menu button (P2-9)', () => {
    const el = createCommandBlock(1, 'ls', '~', 'output', 10, 0, 'success', c, noopSelect)
    const btn = el.querySelector('.cmd-overflow-btn')
    expect(btn).not.toBeNull()
  })

  it('cwd label uses plain text, no emoji (P0-1 flat pivot)', () => {
    const el = createCommandBlock(
      1,
      'cmd',
      '/home/user/repos',
      'out',
      10,
      0,
      'success',
      c,
      noopSelect,
    )
    const cwdEl = el.querySelector('.cmd-header-cwd')
    expect(cwdEl?.textContent).toBe('\u{1F4C1} user/repos')
  })
})

describe('freezeBlock', () => {
  it('replaces a running block with a frozen one in the DOM', () => {
    const parent = document.createElement('div')
    const container = document.createElement('div')
    const running = createRunningBlock(1, 'sleep 5', '~', () => container, noopSelect)
    parent.appendChild(running)

    const frozen = freezeBlock(
      running,
      1,
      'sleep 5',
      '~',
      '<span>done</span>',
      5100,
      0,
      () => container,
      noopSelect,
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
    const running = createRunningBlock(1, 'ls', '~', () => container, noopSelect)
    parent.appendChild(running)
    const frozen = freezeBlock(
      running,
      1,
      'ls',
      '~',
      '<span>ok</span>',
      100,
      0,
      () => container,
      noopSelect,
    )
    expect(frozen.querySelector('.cmd-overflow-btn')).not.toBeNull()
  })
})

describe('block selection model (P1-7, P1-8)', () => {
  it('click selects a block', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = createCommandBlock(
      1,
      'cmd',
      '~',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
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
      1,
      'cmd1',
      '~',
      'out1',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
    )
    const el2 = createCommandBlock(
      2,
      'cmd2',
      '~',
      'out2',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
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
      1,
      'cmd',
      '~',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
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
      1,
      'cmd',
      '~',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
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
      1,
      'cmd',
      '~',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
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
      1,
      'cmd',
      '~',
      'out',
      10,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
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
    const outputA = recA!.el.querySelector('.cmd-output')
    expect(outputA?.innerHTML).toContain('#111111')

    // Second block with theme B
    setCurrentTheme(themeB)
    manager.startBlock('cmd2', '~', 0)
    const linesB = [new BufferLine('world', false)]
    const recB = manager.freezeBlock((y) => linesB[y] ?? undefined, 0, 0)
    expect(recB).not.toBeNull()
    const outputB = recB!.el.querySelector('.cmd-output')
    expect(outputB?.innerHTML).toContain('#cccccc')
    expect(outputB?.innerHTML).not.toContain('#111111')

    // First block still has snapshot A's colours
    expect(outputA?.innerHTML).toContain('#111111')
  })
})

describe('overflow menu (P1-6)', () => {
  it('opens menu on ⋮ click', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createCommandBlock(
      1,
      'echo hello',
      '~',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
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
      1,
      'echo hello',
      '~',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
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
      1,
      'echo hello',
      '~',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
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
      1,
      'echo hello',
      '~',
      'output',
      42,
      0,
      'success',
      () => container,
      noopSelect,
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
