// Block manager tests — DOM creation, freeze lifecycle, clear behaviour.
// Updated for flat design (P0-1) and single-select model (P1-7, P1-8).

// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, beforeAll, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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
import { clampMenuPosition } from '../ui/menu-geometry'
import { shellHighlightReady } from '../shell-highlight'
import { applyReasoningExpanded } from '../reasoning-expanded'
import { clearToasts, toasts } from '../ui/toast'
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
    expect(el.dataset.blockId).toBeUndefined()
    expect(el.dataset.entryId).toBeUndefined()
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
  afterEach(() => {
    manager.dispose()
    inner.remove()
  })

  it('shows and advances a live duration without repainting the block', () => {
    vi.useFakeTimers()
    try {
      const rec = manager.startBlock('find /', '~', 10)
      const duration = rec.el.querySelector<HTMLElement>('.cmd-header-duration')
      expect(duration?.textContent).toBe('0s')

      fixedNow = 66_250
      vi.advanceTimersByTime(1000)

      expect(duration?.textContent).toBe('1m 5s')
      expect(rec.el).toBe(inner.children[0])
      expect(inner.children).toHaveLength(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('stops the live duration timer when the block settles and when the pane is disposed', () => {
    vi.useFakeTimers()
    try {
      manager.startBlock('sleep 5', '~', 10)
      expect(vi.getTimerCount()).toBe(1)

      fixedNow = 2_250
      const frozen = manager.freezeBlock(() => undefined, 10, 0)
      expect(frozen?.durationMs).toBe(1_250)
      expect(vi.getTimerCount()).toBe(0)

      manager.startBlock('sleep 10', '~', 20)
      expect(vi.getTimerCount()).toBe(1)
      manager.dispose()
      expect(vi.getTimerCount()).toBe(0)
    } finally {
      vi.useRealTimers()
    }
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

  it('restorePast puts the past above everything, and clearAll takes it away again (nocx-0zb1m)', () => {
    // The restored past used to be inserted past this manager, so the list
    // clearAll walks could not name it. The manager owns it now: it goes in
    // here, and it comes out with everything else.
    manager.startBlock('live', '~', 0)
    const past = ['oldest', 'newest'].map((label) => {
      const el = document.createElement('div')
      el.className = 'cmd-block'
      el.dataset.restored = 'true'
      el.textContent = label
      return el
    })
    manager.restorePast(past)

    expect(
      Array.from(inner.children)
        .slice(0, 3)
        .map((k) => k.textContent),
    ).toEqual(['oldest', 'newest', 'Previous session'])
    expect(inner.querySelector('.scrollback-restore-boundary')).not.toBeNull()

    manager.clearAll()

    expect(inner.querySelectorAll('.cmd-block').length).toBe(0)
    expect(inner.querySelector('.scrollback-restore-boundary')).toBeNull()
    expect(inner.children.length).toBe(1) // only the xterm container remains
  })

  it('restorePast with nothing to draw adds no boundary — an empty past is not a past', () => {
    manager.restorePast([])
    expect(inner.querySelector('.scrollback-restore-boundary')).toBeNull()
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
      // Existence needs BOTH halves of command discovery: the shell's own
      // tables (OSC 636, above) and the target's PATH set, which the backend
      // computes once per host and hands over shell.commandNames. A store
      // holding only one of them answers `unavailable` on purpose — with the
      // PATH half missing, calling a name nonexistent would strike through
      // every real command on the machine. These fixtures supply an EMPTY
      // shared set: present, so the store can judge, and empty, so a name that
      // is not in the seeded tables is genuinely absent.
      store.applySharedNames({ state: 'ready', names: [], ageMs: 0, reason: '', truncated: false })
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
      'shell',
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
      'shell',
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
      'shell',
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
      'shell',
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
    expect(rec.el.dataset.entryId).toBe('att-1')
    expect(rec.el.dataset.blockId).toBeUndefined()
    expect(rec.el.getAttribute('data-entry-id')).toBe('att-1')
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
    expect(frozen!.el.dataset.entryId).toBe('att-1')
    expect(frozen!.el.dataset.blockId).toBeUndefined()
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

// ── Menu and clipboard helpers, shared by the copy tests in both
// describes below (nocx-v13pd). One block, one menu, one recorder.
/** Records what reached the clipboard, and answers with the list. */
function captureClipboard(): string[] {
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
  return copied
}

/** Open one block's ⋮ menu and return it. */
function openBlockMenu(blockEl: HTMLElement): HTMLElement {
  blockEl
    .querySelector<HTMLElement>('.cmd-overflow-btn')!
    .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
  return document.body.querySelector<HTMLElement>('.cmd-overflow-menu')!
}

/** Open the menu and click the item with this label. */
function clickMenuItem(blockEl: HTMLElement, label: string): void {
  Array.from(openBlockMenu(blockEl).querySelectorAll<HTMLElement>('.cmd-overflow-menu-item'))
    .find((b) => b.textContent === label)!
    .dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
}
function newManager(
  sessionName?: (id: string) => string | null,
  answerText?: (entryId: string) => Promise<string | null>,
) {
  const inner = document.createElement('div')
  // Attached to the document, like the real scrollback: the settings
  // applier that opens the thinking notes already on screen walks the
  // document, and a detached tree is not on screen.
  document.body.appendChild(inner)
  const xtermContainer = document.createElement('div')
  // The manager inserts blocks BEFORE the xterm container, so the
  // container must already be a child (the mount path attaches both).
  inner.appendChild(xtermContainer)
  const manager = new BlockManager(inner, xtermContainer, {
    snapshotStore: freshStore(),
    sessionName,
    answerText,
  })
  return { inner, xtermContainer, manager }
}

describe('BlockManager.addAnswerBlock', () => {
  // ── the turn's children, in order (ADR-0040, nocx-s92so) ────────────

  /** Everything a reader meets inside a turn, in DOM order, each as
   *  "kind:text" — the ORDER is the property these tests are about, so
   *  nothing is queried by class in isolation.
   *
   *  A turn's children are BLOCKS now, and a run of prose is one of them, so
   *  this flattens a `text` block into the rows it holds: what a person meets
   *  is a row of prose, not the box around it. */
  function flowOf(h: { el: HTMLElement }): string[] {
    const out: string[] = []
    const piece = (c: Element): void => {
      if (c.classList.contains('ui-reasoning')) {
        out.push(`thinking:${c.querySelector('.ui-reasoning__body')?.textContent ?? ''}`)
        return
      }
      if (c.classList.contains('term-line')) {
        out.push(`text:${c.textContent ?? ''}`)
        return
      }
      out.push(c.className)
    }
    const box = h.el.querySelector(':scope > .cmd-children')
    for (const child of Array.from(box?.children ?? [])) {
      const el = child as HTMLElement
      if (!el.classList.contains('cmd-block')) {
        // The working stand-in (cmd-answer-typing) is chrome, not content:
        // it stands in for the next output until that output lands, so it
        // is not a piece of the flow. Its presence and absence are the
        // dedicated tests' business (nocx-vnirv.1).
        if (el.classList.contains('cmd-answer-typing')) continue
        piece(el)
        continue
      }
      const kind = el.dataset.blockKind ?? 'command'
      const header = el.querySelector(':scope > .cmd-header .cmd-header-text')?.textContent ?? ''
      if (kind === 'tool') {
        out.push(`call:${header}`)
        continue
      }
      if (kind !== 'text') {
        out.push(`${kind}:${header}`)
        continue
      }
      for (const row of Array.from(el.querySelector('[data-answer-body]')?.children ?? [])) {
        piece(row)
      }
    }
    return out
  }

  it('draws a tool call WHERE IT ARRIVED, before the text written from it', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('what went wrong?', '/')
    h.append('let me look')
    h.toolCall({
      callId: 'call_1',
      tool: 'files.read',
      args: { path: '/repo/a.txt' },
      effect: 'observe',
      resource: { kind: 'path', id: '/repo/a.txt' },
      opensBlock: false,
    })
    h.append('line 3 is wrong')
    // Not "a call appears somewhere in the block": the call sits BETWEEN
    // the prose that preceded it and the prose written from its result.
    // That is the defect this fixes — the run tool's block used to land
    // below a finished answer written from its output.
    expect(flowOf(h)).toEqual([
      'text:let me look',
      'call:files.read path=/repo/a.txt',
      'text:line 3 is wrong',
    ])
  })

  it('names the session a call touched by the PANE\u2019s name, never the id (nocx-vnzek)', () => {
    // The tab strip's derivation reaches the call's block through the
    // manager. The id is on the wire and stays there: what a person reads is
    // the pane.
    const { manager } = newManager((id) => (id === 'sess-9bb9' ? 'home/dev' : null))
    const h = manager.addAnswerBlock('what is on my screen?', '/')
    h.toolCall({
      callId: 'call_1',
      tool: 'readScreen',
      args: { sessionId: 'sess-9bb9' },
      effect: 'observe',
      resource: { kind: 'session', id: 'sess-9bb9' },
      opensBlock: false,
    })
    expect(flowOf(h)).toEqual(['call:readScreen sessionId=home/dev'])
    expect(h.el.textContent).not.toContain('sess-9bb9')
  })

  it('renders one child per call id, however many times the backend announces it', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    // A call that opens NO block, so its own child is what a repeat
    // announcement could duplicate — a `run` call draws none at all
    // (ADR-0040), and this test is about the idempotence, not about which
    // tool it was.
    const call = {
      callId: 'call_1',
      tool: 'files.read',
      effect: 'observe' as const,
      opensBlock: false,
    }
    h.toolCall(call)
    // An approved egress resume puts the SAME call through the pipeline a
    // second time, so the same announcement arrives twice.
    h.toolCall(call)
    expect(flowOf(h)).toEqual(['call:files.read'])
  })

  it('renders two calls when BOTH lack an id — an empty key is not an identity', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    // A provider that omits the id is not malformed (w-call-id-order): the
    // dedupe must not merge two distinct calls into one because their empty
    // keys collide.
    h.toolCall({ callId: '', tool: 'files.read', effect: 'observe', opensBlock: false })
    h.toolCall({ callId: '', tool: 'files.read', effect: 'observe', opensBlock: false })
    expect(flowOf(h)).toEqual(['call:files.read', 'call:files.read'])
  })

  it('puts the thinking in its own note and never in the answer text', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.reasoning('the user asks about ')
    h.reasoning('the screen')
    h.append('it says hello')
    expect(flowOf(h)).toEqual(['thinking:the user asks about the screen', 'text:it says hello'])
    const rows = Array.from(h.el.querySelectorAll('.term-line')).map((r) => r.textContent)
    expect(rows).toEqual(['it says hello'])
  })

  it('opens the thinking note when the person asked for it, and only then (nocx-y9e88)', () => {
    const { manager } = newManager()
    applyReasoningExpanded(false)
    const shut = manager.addAnswerBlock('q', '/')
    shut.reasoning('weighing the two options')
    expect(shut.el.querySelector<HTMLDetailsElement>('.ui-reasoning')?.open).toBe(false)

    applyReasoningExpanded(true)
    const open = manager.addAnswerBlock('q', '/')
    open.reasoning('weighing the two options')
    expect(open.el.querySelector<HTMLDetailsElement>('.ui-reasoning')?.open).toBe(true)

    // The one already on screen follows the change too — a setting the
    // surface contradicts is the defect.
    expect(shut.el.querySelector<HTMLDetailsElement>('.ui-reasoning')?.open).toBe(true)
    applyReasoningExpanded(false)
  })

  it('renders nothing at all for a model that thought nothing, with the setting ON', () => {
    const { manager } = newManager()
    applyReasoningExpanded(true)
    const h = manager.addAnswerBlock('q', '/')
    h.append('hello world')
    h.close('success')
    expect(h.el.querySelector('.ui-reasoning')).toBeNull()
    applyReasoningExpanded(false)
  })

  it('a model with no reasoning and no calls renders exactly what it always did', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('hello world')
    h.close('success')
    expect(h.el.querySelector('.ui-reasoning')).toBeNull()
    expect(h.el.querySelector('.cmd-block[data-block-kind="tool"]')).toBeNull()
    expect(flowOf(h)).toEqual(['text:hello world'])
  })

  it('a call in flight returns the stand-in where the answer will be written', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.toolCall({ callId: 'call_1', tool: 'readScreen', effect: 'observe', opensBlock: false })
    // A call writes no prose, so the stand-in is back where the next words
    // will land — the empty-body defect this fixes (nocx-vnirv.1)...
    expect(h.el.querySelector('.cmd-answer-typing')).not.toBeNull()
    // ...and the corner keeps saying the run is working, because it is: the
    // model has done something and it has not answered.
    expect(h.el.querySelector('.cmd-answer-waiting')).not.toBeNull()
    // The moment a delta lands, the stand-in stands down.
    h.append('the answer')
    expect(h.el.querySelector('.cmd-answer-typing')).toBeNull()
  })

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

  // nocx-kez4m: one header grammar for every kind. The overflow button is
  // appended when the block is BUILT, so anything a later lifecycle adds to
  // the header — the ask kind's terminal word is the only one today — lands
  // to the RIGHT of the ⋮ unless it is placed deliberately. The owner saw
  // "⋮ failed" above "50ms ok ⋮" and asked why one row read backwards.
  it('the overflow button stays last in the header, for a finished answer as for a command', () => {
    const { manager } = newManager()
    const cmd = createCommandBlock(
      'command',
      91,
      'echo hi',
      '~',
      '',
      'out',
      50,
      0,
      'success',
      () => document.createElement('div'),
      noopSelect,
      freshStore(),
      'shell',
    )
    const cmdRight = cmd.querySelector('.cmd-header-right')!
    expect(cmdRight.lastElementChild?.classList.contains('cmd-overflow-btn')).toBe(true)

    const h = manager.addAnswerBlock('q', '/')
    h.append('the answer')
    h.close('failure', 'the model returned no text')
    const askRight = h.el.querySelector('.cmd-header-right')!
    expect(askRight.querySelector('.cmd-header-exit')?.textContent).toBe('failed')
    expect(askRight.lastElementChild?.classList.contains('cmd-overflow-btn')).toBe(true)
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
  it('a live turn uses the shared overflow menu for Stop and settles it away', () => {
    const { manager } = newManager()
    const stop = vi.fn()
    const actions = { stop, isActive: () => true }
    const h = manager.addAnswerBlock('q', '/', actions)

    const menu = openBlockMenu(h.el)
    expect(menu.querySelector<HTMLElement>('[data-action="stop"]')?.textContent).toBe('Stop')
    menu.querySelector<HTMLElement>('[data-action="stop"]')!.click()
    expect(stop).toHaveBeenCalledTimes(1)

    const secondMenu = openBlockMenu(h.el)
    h.close('cancelled')
    expect(secondMenu.isConnected).toBe(false)
    expect(h.el.querySelector('.cmd-header-exit')?.textContent).toBe('stopped')
    expect(h.el.querySelector('.cmd-answer-waiting')).toBeNull()
    expect(h.el.querySelector('.cmd-answer-typing')).toBeNull()
  })
})

// ── The ONE "working, nothing written yet" stand-in (nocx-vnirv.1) ───────
// A turn and a running command are two hosts of ONE indicator: the same
// class, built by the same function, removed by output, by failure and by
// cancellation. The owner's words: "вообще поведение должно быть
// одинаковое" — the behavior should be the same.
describe('the working stand-in (nocx-vnirv.1)', () => {
  it('the turn and the running command wear the SAME indicator — one class, one owner', () => {
    const { inner, xtermContainer, manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    manager.startBlock('make', '~', 0)
    const turn = h.el.querySelector('.cmd-answer-typing')
    const command = xtermContainer.querySelector('.cmd-answer-typing')
    expect(turn).not.toBeNull()
    expect(command).not.toBeNull()
    // AD-8: NOT two indicators that merely look alike — the same class.
    expect(command!.className).toBe(turn!.className)
    inner.remove()
  })

  it('a running command shows the stand-in in the live region until the first byte', () => {
    const { xtermContainer, manager } = newManager()
    manager.startBlock('sleep 5', '~', 0)
    expect(xtermContainer.querySelector('.cmd-answer-typing')).not.toBeNull()
    // The seam: the first parsed output byte stands it down. Idempotent —
    // every later chunk calls the seam again and nothing changes.
    manager.noteCommandOutput()
    expect(xtermContainer.querySelector('.cmd-answer-typing')).toBeNull()
    manager.noteCommandOutput()
    expect(xtermContainer.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('a terminal freeze removes the stand-in — no dots type a command that ended', () => {
    const { xtermContainer, manager } = newManager()
    manager.startBlock('sleep 5', '~', 0)
    expect(xtermContainer.querySelector('.cmd-answer-typing')).not.toBeNull()
    manager.freezeBlock(() => undefined, 2, 0)
    expect(xtermContainer.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('an abandoned command removes the stand-in too — cancellation is a close', () => {
    const { xtermContainer, manager } = newManager()
    manager.startBlock('ssh host', '~', 0)
    manager.bindAttempt('att-1')
    manager.abandonAttempt(
      { id: 'att-1', state: 'unknown' } as ExecutionAttempt,
      () => undefined,
      6,
    )
    expect(xtermContainer.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('clearAll removes the stand-in with everything else', () => {
    const { xtermContainer, manager } = newManager()
    manager.startBlock('make', '~', 0)
    expect(xtermContainer.querySelector('.cmd-answer-typing')).not.toBeNull()
    manager.clearAll()
    expect(xtermContainer.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('a second command replaces the stand-in rather than stacking a second one', () => {
    const { xtermContainer, manager } = newManager()
    manager.startBlock('one', '~', 0)
    manager.startBlock('two', '~', 1)
    expect(xtermContainer.querySelectorAll('.cmd-answer-typing').length).toBe(1)
  })

  it('a turn shows the stand-in at open, loses it at a delta, regains it during a call, loses it at the next delta', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    expect(h.el.querySelector('.cmd-answer-typing')).not.toBeNull()
    h.append('let me look')
    expect(h.el.querySelector('.cmd-answer-typing')).toBeNull()
    // A call in flight writes no prose, so the stand-in returns — the
    // empty-body defect this task fixes.
    h.toolCall({ callId: 'call_1', tool: 'readScreen', effect: 'observe', opensBlock: false })
    expect(h.el.querySelector('.cmd-answer-typing')).not.toBeNull()
    h.append('line 3 is wrong')
    expect(h.el.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('a run call\u2019s command block lands ABOVE the stand-in, which keeps the tail', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock('make', '/repo', 0, 0, 'agent')
    const children = h.el.querySelector(':scope > .cmd-children')!
    // The stand-in marks where the answer will continue: after the block
    // the call opened, never pushed aside by it.
    expect(children.lastElementChild?.classList.contains('cmd-answer-typing')).toBe(true)
    expect(children.querySelector('.cmd-block-running')).not.toBeNull()
  })

  it('a failing turn with no output leaves no dots typing an answer that will never arrive', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.close('failure', 'the model returned no text')
    expect(h.el.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('reasoning is content: it stands the stand-in down', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.reasoning('weighing the two options')
    expect(h.el.querySelector('.cmd-answer-typing')).toBeNull()
  })

  it('the live-region stand-in is OUT OF FLOW — the height constraint holds by construction', () => {
    // jsdom computes no layout, so the contract is asserted on the shipped
    // stylesheet (the same discipline as cmd-output-wrap.test.ts): the
    // stand-in is absolutely positioned inside the live container, which
    // is position:relative — so it adds no flow height to the box the
    // controller measures and sizes, and that box is exactly the frozen
    // body that replaces the region. Nothing moves at the swap.
    const css = readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8')
    const container = css.match(/\.xterm-live-container\.live-running\s*\{([^}]*)\}/)
    expect(container).not.toBeNull()
    expect(container![1]).toContain('position: relative')
    const standIn = css.match(
      /\.xterm-live-container\.live-running > \.cmd-answer-typing\s*\{([^}]*)\}/,
    )
    expect(standIn).not.toBeNull()
    expect(standIn![1]).toContain('position: absolute')
  })
})

// ── The ⋮ menu never leaves the viewport (nocx-vnirv.2) ───────────────────
// A running block sits at the bottom of the scrollback by construction, so
// an unclamped menu opened past the window's bottom edge and the two
describe('the block overflow menu stays in the viewport', () => {
  // The imperative menu appends itself to document.body and stays until
  // dismissed; a test that opens one and ends must take it down, or the
  // NEXT describe's openBlockMenu finds THIS menu first (they share the
  // same body-level query) and clicks an item that belongs to a dead test.
  afterEach(() => {
    document.querySelectorAll('.cmd-overflow-menu').forEach((m) => m.remove())
  })

  function openMenu(nearBottom: boolean): { menu: HTMLElement; buttonRect: DOMRect } {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createRunningBlock(1, 'make', '~', '', () => container, noopSelect, freshStore())
    container.appendChild(el)
    const btn = el.querySelector<HTMLElement>('.cmd-overflow-btn')!
    const rect = nearBottom
      ? { top: 743, bottom: 765, left: 950, right: 972, width: 22, height: 22 }
      : { top: 700, bottom: 722, left: 1000, right: 1022, width: 22, height: 22 }
    btn.getBoundingClientRect = () => rect as DOMRect
    btn.click()
    const menu = document.querySelector<HTMLElement>('.cmd-overflow-menu')!
    return { menu, buttonRect: rect as DOMRect }
  }

  it('clamps the menu inside the viewport when the ⋮ sits near the bottom edge', () => {
    const { menu, buttonRect } = openMenu(true)
    const menuRect = menu.getBoundingClientRect()
    const left = Number.parseFloat(menu.style.left)
    const top = Number.parseFloat(menu.style.top)
    expect(left).toBeGreaterThanOrEqual(8)
    expect(top).toBeGreaterThanOrEqual(8)
    expect(left + menuRect.width).toBeLessThanOrEqual(window.innerWidth - 8)
    expect(top + menuRect.height).toBeLessThanOrEqual(window.innerHeight - 8)
    // AND it is exactly the SHARED geometry's answer (the seam): the anchor
    // is below the button, right-aligned to it. This assertion fails if a
    // second, private copy of the clamp ever appears.
    const expected = clampMenuPosition(
      { x: buttonRect.right - menuRect.width, y: buttonRect.bottom + 2 },
      { width: menuRect.width, height: menuRect.height },
      { width: window.innerWidth, height: window.innerHeight },
    )
    expect({ left, top }).toEqual(expected)
  })

  it('measures the menu OUT OF FLOW — measured in flow it reports the window\u2019s width and lands nowhere near its ⋮', () => {
    // jsdom has no box model, so the two tests above cannot tell an in-flow
    // menu from a fixed one: every rect is zeros and the arithmetic agrees
    // with itself. This one supplies the difference the browser makes, and
    // it is the difference the defect was made of (owner, 2026-08-24): a
    // plain div appended to `body` is an in-flow block box as wide as the
    // body, so measuring it there reports the WINDOW width as the menu's,
    // `btnRect.right - width` goes negative, and the clamp does exactly as
    // asked — pins the menu to the left edge of the screen.
    const CONTENT_WIDTH = 160
    // Through the descriptor rather than the bare method: a prototype method
    // captured by reference is what the unbound-method lint exists for, and
    // the stub still needs the original's dynamic `this` to delegate.
    const originalDesc = Object.getOwnPropertyDescriptor(
      Element.prototype,
      'getBoundingClientRect',
    )!
    const delegate = originalDesc.value as (this: Element) => DOMRect
    Element.prototype.getBoundingClientRect = function (this: Element): DOMRect {
      if (this instanceof HTMLElement && this.classList.contains('cmd-overflow-menu')) {
        const width = this.style.position === 'fixed' ? CONTENT_WIDTH : window.innerWidth
        return {
          x: 0,
          y: 0,
          top: 0,
          left: 0,
          right: width,
          bottom: 120,
          width,
          height: 120,
        } as DOMRect
      }
      return delegate.call(this)
    }
    try {
      // The ⋮ that is NOT against the right edge, so the clamp has nothing
      // to correct and the assertion is about the measurement alone.
      const { menu, buttonRect } = openMenu(true)
      const left = Number.parseFloat(menu.style.left)
      // Beside the ⋮ that opened it, right-aligned to the button — and
      // therefore NOT against the left edge, which is where the in-flow
      // measurement put it.
      expect(left).toBe(buttonRect.right - CONTENT_WIDTH)
      expect(left).toBeGreaterThan(8)
    } finally {
      Object.defineProperty(Element.prototype, 'getBoundingClientRect', originalDesc)
    }
  })

  it('clamps the menu back inside when the ⋮ hugs the right edge', () => {
    const { menu, buttonRect } = openMenu(false)
    const menuRect = menu.getBoundingClientRect()
    const left = Number.parseFloat(menu.style.left)
    const top = Number.parseFloat(menu.style.top)
    expect(left + menuRect.width).toBeLessThanOrEqual(window.innerWidth - 8)
    expect(top + menuRect.height).toBeLessThanOrEqual(window.innerHeight - 8)
    const expected = clampMenuPosition(
      { x: buttonRect.right - menuRect.width, y: buttonRect.bottom + 2 },
      { width: menuRect.width, height: menuRect.height },
      { width: window.innerWidth, height: window.innerHeight },
    )
    expect({ left, top }).toEqual(expected)
  })

  it('opens at body level with fixed positioning: nothing under it moves and nothing scrolls', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const el = createRunningBlock(1, 'make', '~', '', () => container, noopSelect, freshStore())
    container.appendChild(el)
    const scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
    el.querySelector<HTMLElement>('.cmd-overflow-btn')!.click()
    const menu = document.querySelector<HTMLElement>('.cmd-overflow-menu')!
    // Fixed at body level: out of flow, so the block underneath never moves
    // to make room, and nothing in the open path scrolls the page.
    expect(menu.parentElement).toBe(document.body)
    expect(menu.style.position).toBe('fixed')
    expect(scrollTo).not.toHaveBeenCalled()
    scrollTo.mockRestore()
    container.remove()
  })

  it('a menu taller than the viewport scrolls WITHIN the shell — the CSS contract', () => {
    // jsdom lays nothing out, so the reachability half of the clamp is
    // asserted on the shipped stylesheet: the shell caps its height and
    // scrolls its own items, instead of running past the window's edge.
    const css = readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8')
    const rule = css.match(/\.cmd-overflow-menu\s*\{([^}]*)\}/)
    expect(rule).not.toBeNull()
    expect(rule![1]).toContain('max-height')
    expect(rule![1]).toContain('overflow-y: auto')
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

  function newManager(
    sessionName?: (id: string) => string | null,
    answerText?: (entryId: string) => Promise<string | null>,
  ) {
    const inner = document.createElement('div')
    const xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    // Attached to the document like the real scrollback: the ⋮ menu renders
    // at body level and is found from there.
    document.body.appendChild(inner)
    const manager = new BlockManager(inner, xtermContainer, {
      snapshotStore: freshStore(),
      sessionName,
      answerText,
    })
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
      cancelled: 'stopped',
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
      'shell',
    )
    expect(el.querySelector('.cmd-header-text')?.querySelector('.tok-command')).not.toBeNull()
  })

  it('a run of prose carries the wrapping class its kind declares', () => {
    // The body hangs on the `text` child now, not on the turn (ADR-0040) —
    // and the class still comes from the kind's rules, which own the wrap
    // policy: prose wraps, a command's grid does not (nocx-juau).
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('an answer')
    const body = h.el.querySelector('[data-answer-body]')
    expect(body?.className).toBe('cmd-output cmd-output-ask')
    expect(body?.closest('.cmd-block')?.getAttribute('data-block-kind')).toBe('text')
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
    // Both delimiters remain in the row model, but CSS hides them from the
    // rendered answer so the fence is one uninterrupted code unit.
    const codeRows = Array.from(code!.querySelectorAll<HTMLElement>('.term-line'))
    expect(codeRows.map((r) => r.textContent)).toEqual(['```', 'printf "hi"', '```'])
    expect(codeRows[0].dataset.fenceDelim).toBe('open')
    expect(codeRows[codeRows.length - 1]?.dataset.fenceDelim).toBe('close')
    // Prose rows are the body's own children, never inside the code block.
    const prose = Array.from(h.el.querySelectorAll('.cmd-output > .term-line')).map(
      (r) => r.textContent,
    )
    expect(prose).toEqual(['before', 'after'])
    // Copying the block returns the whole answer, fence markers included.
    expect(blockOutputText(h.el)).toBe('before\n```\nprintf "hi"\n```\nafter')
    const css = readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8')
    expect(css).toMatch(
      /\.cmd-output-code\s*>\s*\.term-line\[data-fence-delim\]\s*\{[^}]*display:\s*none/s,
    )
  })

  it('answer fences expose the kit copy control for code only', async () => {
    const copied = captureClipboard()
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('before\n```bash\nprintf "hi"\necho done\n```\nafter')
    h.close('success')

    const code = h.el.querySelector<HTMLElement>('.cmd-output-code')!
    const button = code.querySelector<HTMLButtonElement>('.ui-icon-button')
    expect(button).not.toBeNull()
    button!.click()

    await vi.waitFor(() => expect(copied).toEqual(['printf "hi"\necho done']))
  })

  it('keeps an unclosed fence stable within one answer run, then starts prose at the next answer boundary', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    // Policy: an unclosed fence owns every delta in its answer run; a new
    // run id is the backend's answer boundary and starts a fresh body.
    h.append('```\ncode', 'run-1')
    const firstBody = h.el.querySelector('[data-answer-body]')!
    const code = firstBody.querySelector('.cmd-output-code')!
    h.append('\nmore', 'run-1')
    expect(firstBody.querySelector('.cmd-output-code')).toBe(code)
    expect(Array.from(code.querySelectorAll('.term-line')).map((r) => r.textContent)).toEqual([
      '```',
      'code',
      'more',
    ])

    h.append('rest', 'run-2')
    const bodies = h.el.querySelectorAll('[data-answer-body]')
    expect(bodies.length).toBe(2)
    expect(bodies[1].querySelector('.term-line')?.textContent).toBe('rest')
    expect(bodies[1].textContent).toContain('rest')
    expect(bodies[1].querySelector('.cmd-output-code')).toBeNull()
  })

  it('highlights a shell fence with the SAME lexer the editor uses (nocx-swoje)', async () => {
    await shellHighlightReady
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('```bash\necho hi\n```\n')
    h.close('success')
    const code = h.el.querySelector('.cmd-output-code')!
    const rows = Array.from(code.querySelectorAll('.term-line'))
    // The delimiters stay plain — they mark the region, but CSS hides them.
    expect(rows[0].querySelector('span')).toBeNull()
    // The command line is tokenised, and the classes are the editor's own.
    expect(rows[1].querySelector('[class^="tok-"]')).not.toBeNull()
    // The bytes are unchanged: copying a fence still returns what arrived.
    expect(rows[1].textContent).toBe('echo hi')
  })

  it('renders a fence in another language plainly rather than colouring it wrongly', async () => {
    await shellHighlightReady
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('```python\nprint("hi")\n```\n')
    h.close('success')
    const rows = Array.from(h.el.querySelectorAll('.cmd-output-code .term-line'))
    expect(rows[1].querySelector('span')).toBeNull()
    expect(rows[1].textContent).toBe('print("hi")')
  })

  it('a fence with no language is shell — this is a terminal (nocx-swoje)', async () => {
    await shellHighlightReady
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('```\necho hi\n```\n')
    h.close('success')
    const rows = Array.from(h.el.querySelectorAll('.cmd-output-code .term-line'))
    expect(rows[1].querySelector('[class^="tok-"]')).not.toBeNull()
  })

  it('paints the markdown a model emits, and escapes every byte of it', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('## Findings\n- run `ls` in **the repo**\n<script>alert(1)</script>\n')
    h.close('success')
    const rows = Array.from(h.el.querySelectorAll<HTMLElement>('.cmd-output > .term-line'))
    expect(rows[0].dataset.md).toBe('h2')
    expect(rows[1].dataset.md).toBe('li')
    expect(rows[1].querySelector('code.ui-md-code')?.textContent).toBe('ls')
    expect(rows[1].querySelector('strong.ui-md-strong')?.textContent).toBe('the repo')
    // The model's tag is text, and the answer body grew no script element.
    expect(h.el.querySelector('script')).toBeNull()
    expect(rows[2].textContent).toBe('<script>alert(1)</script>')
  })

  it('an answer with no code and no structure renders exactly as it always did', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('the command exited with 1\nnothing else happened\n')
    h.close('success')
    const rows = Array.from(h.el.querySelectorAll<HTMLElement>('.cmd-output > .term-line'))
    expect(rows.map((r) => r.textContent)).toEqual([
      'the command exited with 1',
      'nothing else happened',
    ])
    for (const r of rows) {
      expect(r.dataset.md).toBeUndefined()
      expect(r.children.length).toBe(0)
    }
  })

  it('fence → prose → fence keeps each fence in its own container, after the prose', () => {
    const { manager } = newManager()
    const h = manager.addAnswerBlock('q', '/')
    h.append('```\ncode1\n```\nbetween\n```\ncode2\n```')
    h.close('success')
    const codeBlocks = h.el.querySelectorAll('.cmd-output-code')
    expect(codeBlocks.length).toBe(2)
    expect(codeBlocks[0].querySelector('.cmd-output-code-copy-host')).not.toBeNull()
    expect(codeBlocks[1].querySelector('.cmd-output-code-copy-host')).not.toBeNull()
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

  it('Copy output on an ANSWER returns the STORED text, not the painted DOM (nocx-v13pd)', async () => {
    const copied = captureClipboard()
    // Delimiters remain as hidden rows so the DOM row model and Copy output
    // keep the original answer text in one place.
    const stored = '## Findings\n- run `ls` in **the repo**\n```\ncode\n```'
    const { manager } = newManager(undefined, () => Promise.resolve(stored))
    const h = manager.addAnswerBlock('question?', '/')
    h.el.dataset.entryId = 'entry-7'
    h.append('## Findings\n- run `ls` in **the repo**\n```\ncode\n```')
    h.close('success')
    // Markdown prose is painted for reading, but the fence rows are still
    // present in the copied DOM text.
    expect(blockOutputText(h.el)).toBe('Findings\n•run ls in the repo\n```\ncode\n```')

    clickMenuItem(h.el, 'Copy output')
    await vi.waitFor(() => expect(copied.length).toBe(1))
    expect(copied[0]).toBe(stored)

    clickMenuItem(h.el, 'Copy all')
    await vi.waitFor(() => expect(copied.length).toBe(2))
    expect(copied[1]).toBe(`question?\n${stored}`)
  })

  it('says so when the stored answer is gone, rather than copying the painted text', async () => {
    const copied = captureClipboard()
    clearToasts()
    // Null is retention AND an unreachable store — one refusal for both.
    const { manager } = newManager(undefined, () => Promise.resolve(null))
    const h = manager.addAnswerBlock('question?', '/')
    h.el.dataset.entryId = 'entry-7'
    h.append('some answer')
    h.close('success')

    clickMenuItem(h.el, 'Copy output')
    await vi.waitFor(() => expect(toasts().length).toBe(1))
    expect(toasts()[0].level).toBe('warning')
    // Nothing was copied: a copy that quietly differs from the record is
    // worse than a refusal.
    expect(copied).toEqual([])
    clearToasts()
  })

  it('says it is working while it fetches, so the menu never looks inert', async () => {
    captureClipboard()
    let release: (v: string | null) => void = () => {}
    const pending = new Promise<string | null>((resolve) => {
      release = resolve
    })
    const { manager } = newManager(undefined, () => pending)
    const h = manager.addAnswerBlock('question?', '/')
    h.el.dataset.entryId = 'entry-7'
    h.append('some answer')
    h.close('success')

    const menu = openBlockMenu(h.el)
    const item = Array.from(
      menu.querySelectorAll<HTMLButtonElement>('.cmd-overflow-menu-item'),
    ).find((b) => b.textContent === 'Copy output')!
    item.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    // The item reports the work rather than sitting there looking clicked.
    expect(item.dataset.busy).toBe('')
    expect(item.disabled).toBe(true)
    expect(item.textContent).not.toBe('Copy output')

    release('stored')
    await vi.waitFor(() => expect(document.body.querySelector('.cmd-overflow-menu')).toBeNull())
  })

  it('a COMMAND block still copies what the terminal drew — unchanged', () => {
    const copied = captureClipboard()
    const { manager } = newManager(undefined, () =>
      Promise.reject(new Error('a command must never reach the ledger for its copy')),
    )
    manager.startBlock('echo hi', '/repo', 0)
    const rec = manager.freezeBlock((y) => (y === 0 ? new BufferLine('hi') : undefined), 0, 0)!
    clickMenuItem(rec.el, 'Copy output')
    expect(copied[0]).toBe('hi')
    clickMenuItem(rec.el, 'Copy all')
    expect(copied[1]).toBe('echo hi\nhi')
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
      'shell',
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
    'shell',
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

// ── a block is a block, whoever submitted it (nocx-9sqii, criterion 3) ────
//
// The assistant's command stays an ORDINARY top-level block. That claim is
// only worth something if it is checked by the SAME assertions a person's
// block is checked by — a separate "and the agent's block also works" test
// is written from the implementation and passes for whatever was built.
//
// So this runs one set over both authors. What differs between them is the
// author badge and nothing else, and that difference is asserted here too so
// the sameness is not sameness by accident.
describe.each(['shell', 'agent'] as const)('a %s-authored block', (author) => {
  const build = (parent: HTMLElement, command = 'cat -n a.txt') =>
    createCommandBlock(
      'command',
      1,
      command,
      '/repo',
      '',
      '<span class="term-line">     1\tfirst</span><span class="term-line">     2\tsecond</span>',
      120,
      0,
      'success',
      makeContainer(parent),
      noopSelect,
      freshStore(),
      author,
    )

  it('is selectable by clicking it', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = build(parent)
    parent.appendChild(el)
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
    expect(el.classList.contains('cmd-block-selected')).toBe(true)
    parent.remove()
  })

  it('says what was run — the "what did I run" reading, off the block itself', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = build(parent)
    expect(blockCommandText(el)).toBe('cat -n a.txt')
    expect(el.querySelector('.cmd-header-text')?.textContent).toBe('cat -n a.txt')
    parent.remove()
  })

  it('gives its output back with the line breaks put back — the copy path', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = build(parent)
    expect(blockOutputText(el)).toBe('     1\tfirst\n     2\tsecond')
    parent.remove()
  })

  it('offers the overflow menu, with the same items', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = build(parent)
    parent.appendChild(el)
    const btn = el.querySelector('.cmd-overflow-btn') as HTMLElement
    expect(btn).not.toBeNull()
    btn.click()
    const items = Array.from(
      document.body.querySelectorAll('.cmd-overflow-menu .cmd-overflow-menu-item'),
    ).map((i) => i.textContent)
    expect(items).toContain('Copy command')
    expect(items).toContain('Copy output')
    expect(items).toContain('Copy all')
    document.body.querySelector('.cmd-overflow-menu')?.remove()
    parent.remove()
  })

  it('is a top-level block of the ordinary kind — never nested inside a turn', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = build(parent)
    parent.appendChild(el)
    expect(el.classList.contains('cmd-block')).toBe(true)
    expect(el.dataset.blockKind).toBe('command')
    expect(el.parentElement).toBe(parent)
    expect(el.closest('[data-answer-body]')).toBeNull()
    parent.remove()
  })

  it('carries the author mark when it is not the human, and none when it is', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    const el = build(parent)
    const mark = el.querySelector<HTMLElement>('.ui-badge[data-author]')
    if (author === 'shell') expect(mark).toBeNull()
    else expect(mark?.dataset.author).toBe('agent')
    parent.remove()
  })
})

// ── ONE OWNER FOR THE HEADER'S RIGHT-HAND GROUP (nocx-hoeq3) ───────────────
//
// The owner put a `df` block and an assistant turn side by side and asked why
// the chips differ — their number, their placement — when nothing about a
// header is supposed to be per-kind except the WORDS (nocx-ex636).
//
// Two constructions were standing. The command's exit chip was built in
// createHeader and carried `cmd-header-exit-ok`/`-fail`; the turn's was built
// again in the answer flow's close and carried neither. And a turn was handed
// `durationMs = null` at build and never given one afterwards, so its group
// held one chip where a command's holds two.
//
// So the assertions below are COMPARISONS between the two kinds rather than
// snapshots of either: a second construction cannot agree with the first by
// accident, and a chip that only one kind emits shows up as a difference in
// the group.
describe('the header’s right-hand group has one owner (nocx-hoeq3)', () => {
  beforeAll(async () => {
    await shellHighlightReady
  })

  /** A manager whose clock is ours, so a turn's duration is a number this
   *  test chose rather than however long jsdom took. */
  function newManager(now: () => number = () => 0) {
    const inner = document.createElement('div')
    const xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    document.body.appendChild(inner)
    const manager = new BlockManager(inner, xtermContainer, {
      snapshotStore: freshStore(),
      now,
    })
    return { inner, manager }
  }

  /** A settled command block, built the way the freeze path builds one. */
  function settledCommand(durationMs: number, exitCode: number): HTMLElement {
    return createCommandBlock(
      'command',
      1,
      'df -h',
      '/home/dev',
      '',
      '<span class="term-line">out</span>',
      durationMs,
      exitCode,
      exitCode === 0 ? 'success' : 'failure',
      () => document.createElement('div'),
      noopSelect,
      freshStore(),
      'shell',
    )
  }

  /** A turn driven to its close, on a clock that makes it take `ms`. */
  function closedTurn(ms: number, status: 'success' | 'failure' = 'success') {
    let t = 0
    const { manager } = newManager(() => t)
    const turn = manager.addAnswerBlock('how much disk is free?', '/home/dev')
    turn.append('41G free')
    t = ms
    turn.close(status, status === 'failure' ? 'the model did not answer' : undefined)
    return turn.el
  }

  /** The right-hand group's contents, as the class list of each child in DOM
   *  order. The class list is the whole identity — the tone, the shared chip
   *  appearance and the identity class an e2e spec reads are all in it — so
   *  two kinds whose groups read the same here are carrying the same chips,
   *  built by the same code, in the same order. */
  function rightGroup(el: HTMLElement): string[] {
    const right = el.querySelector('.cmd-header-right')!
    return Array.from(right.children).map((c) => c.className)
  }

  it('the class list of a command’s terminal chip and a turn’s is the same list', () => {
    // Criterion 1. Not "both contain cmd-header-exit": the ASSERTION is
    // equality, so a second construction anywhere — one modifier missing, one
    // class added — fails here rather than the day somebody styles
    // `.cmd-header-exit-ok` and only one kind moves.
    const okCmd = settledCommand(27, 0).querySelector('.cmd-header-exit')!
    const okTurn = closedTurn(1200, 'success').querySelector('.cmd-header-exit')!
    expect(okTurn.className).toBe(okCmd.className)
    expect(okCmd.className).toBe('nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok')

    const failCmd = settledCommand(27, 2).querySelector('.cmd-header-exit')!
    const failTurn = closedTurn(1200, 'failure').querySelector('.cmd-header-exit')!
    expect(failTurn.className).toBe(failCmd.className)
    expect(failCmd.className).toBe('nocx-chip nocx-chip-fail cmd-header-exit cmd-header-exit-fail')
  })

  it('the WORDS stay the kind’s own — a turn is completed, a command is ok', () => {
    // The other half of criterion 1, and the line nocx-ex636 drew: one chip,
    // two vocabularies. An answer is not a command's output and must not
    // borrow its words, so sharing the construction must not share the text.
    expect(settledCommand(27, 0).querySelector('.cmd-header-exit')?.textContent).toBe('ok')
    expect(settledCommand(27, 2).querySelector('.cmd-header-exit')?.textContent).toBe('exit 2')
    expect(closedTurn(1200, 'success').querySelector('.cmd-header-exit')?.textContent).toBe(
      'completed',
    )
    expect(closedTurn(1200, 'failure').querySelector('.cmd-header-exit')?.textContent).toBe(
      'failed',
    )
  })

  it('each kind declares what its right group holds, in the rules table', () => {
    // Criterion 2: the decision is beside the other per-kind rules, so a
    // third kind declares its group or fails loudly — it never inherits the
    // command's group by being built through the same builder.
    expect(blockKindRules('command').headerRight.chips).toEqual(['duration', 'terminal'])
    expect(blockKindRules('ask').headerRight.chips).toEqual(['duration', 'terminal'])
  })

  it('a finished turn says how long it took, in the same chip a command uses', () => {
    // Criterion 3. A turn HAS a duration — the model took time, and that is
    // as worth knowing as `df` taking 27ms. Same chip, same formatter.
    const turn = closedTurn(1234)
    const dur = turn.querySelector('.cmd-header-duration')!
    expect(dur.textContent).toBe('1.2s')
    expect(dur.className).toBe(
      settledCommand(27, 0).querySelector('.cmd-header-duration')!.className,
    )
    // The same formatter, asserted at a second magnitude so an agreement at
    // one number is not mistaken for an agreement about formatting.
    expect(closedTurn(27).querySelector('.cmd-header-duration')?.textContent).toBe('27ms')
  })

  it('the two headers agree on their right group: same chips, same order, same ⋮ last', () => {
    // Criterion 4, off the DOM. The right edge and the gap to the ⋮ are one
    // CSS rule (.cmd-header-right: margin-left auto, gap 8px) applied to one
    // element class, so what geometry actually turns on is WHAT IS IN THE
    // GROUP — which is what this reads.
    const cmd = settledCommand(27, 0)
    const turn = closedTurn(1234)
    expect(rightGroup(turn)).toEqual(rightGroup(cmd))
    expect(rightGroup(cmd)).toEqual([
      'nocx-chip nocx-chip-muted cmd-header-duration',
      'nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok',
      'cmd-overflow-btn',
    ])
    // …and neither group is trivially equal by being empty or by hanging off
    // a different container.
    expect(turn.querySelector('.cmd-header-right')).not.toBeNull()
  })

  it('the turn states its outcome once, on its own header, however much it did', () => {
    // Criterion 5, as ADR-0040 leaves it. The outcome used to be a question
    // of WHICH FRAGMENT states it — the turn was several blocks and only the
    // last one had ended. There is one block now, so how long the turn took
    // and how it ended land on the header that carries the question, and no
    // child of it says anything about an outcome it does not have.
    let t = 0
    const { manager } = newManager(() => t)
    const turn = manager.addAnswerBlock('how much disk is free?', '/repo')
    turn.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock('df -h', '/repo', 0, 0, 'agent')
    turn.append('41G free')
    t = 900
    turn.close('success')

    const own = turn.el.querySelector(':scope > .cmd-header')!
    expect(own.querySelector('.cmd-header-duration')?.textContent).toBe('900ms')
    expect(own.querySelector('.cmd-header-exit')?.textContent).toBe('completed')
    // Exactly one of each in the whole turn: the command's block is still
    // running, so nothing else states a duration or an outcome yet.
    expect(turn.el.querySelectorAll('.cmd-header-exit')).toHaveLength(1)
    // And a run of prose has no header to state anything with.
    const prose = turn.el.querySelector('.cmd-block[data-block-kind="text"]')!
    expect(prose.querySelector('.cmd-header')).toBeNull()
  })
})

describe('the block grant menu action', () => {
  const menuItems = (el: HTMLElement): HTMLElement[] => {
    el.querySelector<HTMLElement>('.cmd-overflow-btn')!.click()
    return Array.from(document.querySelectorAll<HTMLElement>('.cmd-overflow-menu-item'))
  }

  afterEach(() => {
    document.querySelectorAll('.cmd-overflow-menu').forEach((menu) => menu.remove())
  })

  it('marks running and finished blocks through one liveness-free action', () => {
    const container = document.createElement('div')
    const toggleGrant = vi.fn()
    const isActive = vi.fn(() => true)
    const running = createRunningBlock(
      1,
      'git status',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      { stop: vi.fn(), isActive, toggleGrant },
    )
    const finished = createCommandBlock(
      'command',
      2,
      'npm test',
      '~',
      '',
      '<span class="term-line">ok</span>',
      120,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      undefined,
      { stop: vi.fn(), isActive, toggleGrant },
    )
    document.body.append(running, finished)
    try {
      const runningGrant = menuItems(running).find((item) => item.dataset.action === 'grant')
      expect(runningGrant?.textContent).toBe('Ask about this block')
      isActive.mockClear()
      runningGrant?.click()
      expect(toggleGrant).toHaveBeenCalledWith(running)
      expect(isActive).not.toHaveBeenCalled()

      const finishedGrant = menuItems(finished).find((item) => item.dataset.action === 'grant')
      expect(finishedGrant?.textContent).toBe('Ask about this block')
      isActive.mockClear()
      finishedGrant?.click()
      expect(toggleGrant).toHaveBeenCalledWith(finished)
      expect(isActive).not.toHaveBeenCalled()
    } finally {
      running.remove()
      finished.remove()
    }
  })

  it('labels the same item unmark when the block is already granted', () => {
    const container = document.createElement('div')
    const el = createCommandBlock(
      'command',
      3,
      'pwd',
      '~',
      '',
      '<span class="term-line">/tmp</span>',
      120,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      undefined,
      {
        stop: vi.fn(),
        isActive: vi.fn(),
        isGranted: () => true,
        toggleGrant: vi.fn(),
      },
    )
    document.body.append(el)
    try {
      const grant = menuItems(el).find((item) => item.dataset.action === 'grant')
      expect(grant?.textContent).toBe('Unmark')
    } finally {
      el.remove()
    }
  })

  it("a running block's Stop action calls the host once and closes its menu", () => {
    const container = document.createElement('div')
    const stop = vi.fn()
    const el = createRunningBlock(
      4,
      'npm test',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      { stop, isActive: vi.fn(() => true) },
    )
    document.body.append(el)
    try {
      const stopItem = menuItems(el).find((item) => item.dataset.action === 'stop')
      expect(stopItem?.textContent).toBe('Stop')
      stopItem!.click()
      expect(stop).toHaveBeenCalledTimes(1)
      expect(document.querySelector('.cmd-overflow-menu')).toBeNull()
    } finally {
      el.remove()
    }
  })

  it('rechecks liveness before Stop fires', () => {
    const container = document.createElement('div')
    const stop = vi.fn()
    let active = true
    const el = createRunningBlock(
      5,
      'npm test',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      { stop, isActive: () => active },
    )
    document.body.append(el)
    try {
      const stopItem = menuItems(el).find((item) => item.dataset.action === 'stop')
      active = false
      stopItem!.click()
      expect(stop).not.toHaveBeenCalled()
      expect(document.querySelector('.cmd-overflow-menu')).toBeNull()
    } finally {
      el.remove()
    }
  })

  it('uses supplied block liveness to gate Stop', () => {
    const container = document.createElement('div')
    const isActive = vi.fn(() => false)
    const el = createRunningBlock(
      6,
      'npm test',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      { stop: vi.fn(), isActive },
    )
    document.body.append(el)
    try {
      const items = menuItems(el)
      expect(items.find((item) => item.dataset.action === 'stop')).toBeUndefined()
      expect(isActive).toHaveBeenCalledWith(el)
    } finally {
      el.remove()
    }
  })

  it('a frozen block offers no Stop action', () => {
    const container = document.createElement('div')
    const el = createCommandBlock(
      'command',
      7,
      'npm test',
      '~',
      '',
      '<span class="term-line">ok</span>',
      120,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
    )
    document.body.append(el)
    try {
      expect(menuItems(el).find((item) => item.dataset.action === 'stop')).toBeUndefined()
    } finally {
      el.remove()
    }
  })

  it('rechecks the block before Stop fires when another command starts', () => {
    const container = document.createElement('div')
    const stop = vi.fn()
    let activeBlock: HTMLElement | null = null
    const actions = {
      stop,
      isActive: vi.fn((blockEl: HTMLElement) => blockEl === activeBlock),
    }
    const first = createRunningBlock(
      4,
      'npm test',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      actions,
    )
    const second = createRunningBlock(
      5,
      'git status',
      '~',
      '',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
      actions,
    )
    activeBlock = first
    document.body.append(first, second)
    try {
      const menu = menuItems(first)
      const stopItem = menu.find((item) => item.dataset.action === 'stop')
      expect(stopItem?.textContent).toBe('Stop')
      expect(actions.isActive).toHaveBeenCalledWith(first)

      activeBlock = second
      stopItem?.click()

      expect(actions.isActive).toHaveBeenLastCalledWith(first)
      expect(stop).not.toHaveBeenCalled()
    } finally {
      first.remove()
      second.remove()
    }
  })

  it('keeps an unwired host on its ordinary copy and wrap menu', () => {
    const container = document.createElement('div')
    const el = createCommandBlock(
      'command',
      6,
      'pwd',
      '~',
      '',
      '<span class="term-line">/repo</span>',
      10,
      0,
      'success',
      () => container,
      noopSelect,
      freshStore(),
      'shell',
    )
    document.body.appendChild(el)
    try {
      const items = menuItems(el)
      const labels = items.map((item) => item.textContent)
      expect(labels).toEqual(
        expect.arrayContaining(['Copy command', 'Copy output', 'Copy all', 'Wrap lines']),
      )
      expect(items.find((item) => item.dataset.action === 'grant')).toBeUndefined()
      expect(items.find((item) => item.dataset.action === 'stop')).toBeUndefined()
    } finally {
      el.remove()
    }
  })
})
