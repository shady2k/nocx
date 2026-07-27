// DOM scrollback block manager.
// Creates, freezes, and manages DOM command blocks in the scrollback area.
// Flat warp-style design (P0-1): no card borders, dividers between blocks,
// subtle background tint on hover/select.

import { serializeRange, fromITheme } from './serializer'
import { getCurrentTheme } from '../renderers/theme-adapter'
import type { IBufferLine } from '@xterm/xterm'

// ── Clipboard helper ────────────────────────────────────────────────────────

function clipboardFallback(text: string): void {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text).catch(() => {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.left = '-9999px'
      document.body.appendChild(ta)
      ta.select()
      try {
        document.execCommand('copy')
      } catch {
        /* silent */
      }
      document.body.removeChild(ta)
    })
  }
}

// ── Block model ────────────────────────────────────────────────────────────

export interface BlockRecord {
  id: number
  command: string
  cwd: string
  /** Duration in ms: C marker to D marker. */
  durationMs: number | null
  exitCode: number | null
  status: 'running' | 'success' | 'failure'
  /** IMarker line for C boundary. */
  startLine: number
  /** IMarker line for D boundary (approx). */
  endLine: number
  el: HTMLElement
}

/** Line accessor function — matches xterm's IBufferLine.getLine(). */
export type GetLineFn = (y: number) => IBufferLine | undefined

// ── DOM helpers ────────────────────────────────────────────────────────────

function div(className: string, ...children: (string | HTMLElement)[]): HTMLElement {
  const el = document.createElement('div')
  el.className = className
  for (const c of children) {
    if (typeof c === 'string') {
      el.appendChild(document.createTextNode(c))
    } else {
      el.appendChild(c)
    }
  }
  return el
}

// ── Duration formatter ─────────────────────────────────────────────────────

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms.toFixed(0)}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  const min = Math.floor(ms / 60000)
  const sec = ((ms % 60000) / 1000).toFixed(0)
  return `${min}m ${sec}s`
}

// ── CWD display ────────────────────────────────────────────────────────────

function cwdLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '') || '~'
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path
  return parts.slice(-2).join('/')
}

// ── Block DOM factory ───────────────────────────────────────────────────────

/**
 * Create the header row for a command block — flat, warp-style (P0-1).
 * No card background, no pill/chip styling. Plain muted small text.
 */
function createHeader(
  command: string,
  cwd: string,
  durationMs: number | null,
  exitCode: number | null,
  status: 'running' | 'success' | 'failure',
): HTMLElement {
  const header = div('cmd-header')

  // ── Chips row (above command text): cwd left, duration+exit right ──
  const chipsRow = div('cmd-header-chips')

  // CWD — standard chip component
  if (cwd) {
    const cwdEl = document.createElement('span')
    cwdEl.className = 'nocx-chip cmd-header-cwd'
    cwdEl.textContent = `📁 ${cwdLabel(cwd)}`
    chipsRow.appendChild(cwdEl)
  }

  // Right: duration + exit status (or spinner while running)
  const right = div('cmd-header-right')

  if (status === 'running') {
    const spinner = document.createElement('span')
    spinner.className = 'cmd-header-spinner'
    right.appendChild(spinner)
  } else {
    if (durationMs !== null) {
      const dur = document.createElement('span')
      dur.className = 'nocx-chip nocx-chip-muted cmd-header-duration'
      dur.textContent = formatDuration(durationMs)
      right.appendChild(dur)
    }

    if (exitCode !== null) {
      const exit = document.createElement('span')
      exit.className =
        exitCode === 0
          ? 'nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok'
          : 'nocx-chip nocx-chip-fail cmd-header-exit cmd-header-exit-fail'
      exit.textContent = exitCode === 0 ? 'ok' : `exit ${exitCode}`
      right.appendChild(exit)
    }
  }

  chipsRow.appendChild(right)
  header.appendChild(chipsRow)

  // ── Command text (below chips) ─────────────────────────────────────
  const cmdSpan = document.createElement('span')
  cmdSpan.className = 'cmd-header-text'
  cmdSpan.textContent = command || '(empty)'
  header.appendChild(cmdSpan)

  return header
}

/**
 * Returns true when the serialized output HTML is effectively empty.
 */
function isOutputEmpty(html: string): boolean {
  const stripped = html.replace(/<[^>]*>/g, '').replace(/\s/g, '')
  return stripped.length === 0
}

/**
 * Build the "⋮" overflow menu button + dropdown (P2-9, P1-6 fix).
 * The menu is rendered as a child of document.body with position:fixed
 * so it floats above ALL blocks and scroll containers. Position is
 * calculated from the button's bounding rect. Closes on outside click
 * and Escape key.
 */
function buildOverflowMenu(command: string, outputEl: HTMLElement | null): HTMLElement {
  const btn = document.createElement('button')
  btn.className = 'cmd-overflow-btn'
  btn.textContent = '\u22EE' // ⋮ vertical ellipsis
  btn.setAttribute('aria-label', 'Block actions')

  let menu: HTMLElement | null = null
  let closeOnEscape: ((e: KeyboardEvent) => void) | null = null
  let closeOnClick: ((ev: MouseEvent) => void) | null = null

  const closeMenu = () => {
    if (menu) {
      menu.remove()
      menu = null
    }
    if (closeOnEscape) {
      document.removeEventListener('keydown', closeOnEscape)
      closeOnEscape = null
    }
    if (closeOnClick) {
      document.removeEventListener('click', closeOnClick)
      closeOnClick = null
    }
  }

  btn.addEventListener('click', (e) => {
    e.stopPropagation()
    e.preventDefault()

    // If menu is already open, close it.
    if (menu) {
      closeMenu()
      return
    }

    // Build the dropdown.
    menu = document.createElement('div')
    menu.className = 'cmd-overflow-menu'

    const copyCmd = document.createElement('button')
    copyCmd.className = 'cmd-overflow-menu-item'
    copyCmd.textContent = 'Copy command'
    copyCmd.addEventListener('click', (ev) => {
      ev.stopPropagation()
      clipboardFallback(command)
      closeMenu()
    })

    const copyOut = document.createElement('button')
    copyOut.className = 'cmd-overflow-menu-item'
    copyOut.textContent = 'Copy output'
    copyOut.addEventListener('click', (ev) => {
      ev.stopPropagation()
      const text = outputEl?.textContent ?? ''
      clipboardFallback(text)
      closeMenu()
    })

    const copyAll = document.createElement('button')
    copyAll.className = 'cmd-overflow-menu-item'
    copyAll.textContent = 'Copy all'
    copyAll.addEventListener('click', (ev) => {
      ev.stopPropagation()
      const outText = outputEl?.textContent ?? ''
      clipboardFallback(`${command}\n${outText}`)
      closeMenu()
    })

    menu.append(copyCmd, copyOut, copyAll)

    // Render at body level so it floats above all scroll containers (P1-6).
    document.body.appendChild(menu)

    // Position relative to the button using fixed coordinates.
    const btnRect = btn.getBoundingClientRect()
    menu.style.position = 'fixed'
    menu.style.top = `${btnRect.bottom + 2}px`
    menu.style.right = `${window.innerWidth - btnRect.right}px`

    // Close on outside click (after this event finishes).
    closeOnClick = (ev: MouseEvent) => {
      if (!menu?.contains(ev.target as Node) && ev.target !== btn) {
        closeMenu()
      }
    }
    setTimeout(() => document.addEventListener('click', closeOnClick!), 0)

    // Close on Escape.
    closeOnEscape = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        closeMenu()
      }
    }
    document.addEventListener('keydown', closeOnEscape)
  })

  return btn
}

// ── Selection helpers ──────────────────────────────────────────────────────

const SELECTED_CLASS = 'cmd-block-selected'

/**
 * Get the currently selected block's DOM element, if any.
 */
export function getSelectedBlock(container: HTMLElement): HTMLElement | null {
  return container.querySelector(`.${SELECTED_CLASS}`)
}

/**
 * Deselect all blocks inside the container. Returns true if a block was deselected.
 */
export function deselectAllBlocks(container: HTMLElement): boolean {
  const sel = getSelectedBlock(container)
  if (sel) {
    sel.classList.remove(SELECTED_CLASS)
    return true
  }
  return false
}

/**
 * Wire full-block click-to-select (P1-7).
 * Click (mousedown+up without significant movement) selects the block.
 * Drag (mousedown+move) starts text selection and does NOT select the block.
 * @param onSelect callback(id, selected) — notifies the manager of selection changes.
 */
function wireBlockSelection(
  blockEl: HTMLElement,
  container: HTMLElement,
  overflowBtn: HTMLElement,
  blockId: number,
  onSelect: (id: number, selected: boolean) => void,
): void {
  let mouseMoved = false

  blockEl.addEventListener('mousedown', (e) => {
    if ((e.target as HTMLElement).closest('.cmd-overflow-btn, .cmd-overflow-menu')) return
    mouseMoved = false
  })

  blockEl.addEventListener('mousemove', () => {
    mouseMoved = true
  })

  blockEl.addEventListener('mouseup', (e) => {
    if ((e.target as HTMLElement).closest('.cmd-overflow-btn, .cmd-overflow-menu')) return
    if (mouseMoved) return

    // Toggle selection: if already selected, deselect; otherwise select
    const currentlySelected = blockEl.classList.contains(SELECTED_CLASS)
    if (currentlySelected) {
      blockEl.classList.remove(SELECTED_CLASS)
      onSelect(blockId, false)
    } else {
      // Deselect others first (single-select P1-8)
      const prev = getSelectedBlock(container)
      if (prev) prev.classList.remove(SELECTED_CLASS)
      blockEl.classList.add(SELECTED_CLASS)
      onSelect(blockId, true)
    }
    mouseMoved = false
  })
}

// ── Block builders ─────────────────────────────────────────────────────────

/**
 * Create a frozen command block DOM element with header + serialized output.
 */
export function createCommandBlock(
  id: number,
  command: string,
  cwd: string,
  outputHtml: string,
  durationMs: number | null,
  exitCode: number | null,
  status: 'success' | 'failure',
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
): HTMLElement {
  const wrapper = document.createElement('div')
  wrapper.className = 'cmd-block'
  wrapper.setAttribute('data-block-id', String(id))

  const header = createHeader(command, cwd, durationMs, exitCode, status)

  let outputEl: HTMLElement | null = null
  if (outputHtml && !isOutputEmpty(outputHtml)) {
    outputEl = document.createElement('div')
    outputEl.className = 'cmd-output'
    outputEl.innerHTML = outputHtml
  }

  // Overflow menu (P2-9) — always the LAST element of the header-right
  // group (owner directive: ⋮ never shifts position).
  const overflow = buildOverflowMenu(command, outputEl)
  const right = header.querySelector('.cmd-header-right')
  if (right) right.appendChild(overflow)

  wrapper.appendChild(header)
  if (outputEl) wrapper.appendChild(outputEl)

  // Full-block click-to-select with drag distinction (P1-7, P1-8).
  wireBlockSelection(wrapper, getContainer(), overflow, id, onSelect)

  return wrapper
}

/**
 * Create a "running" block element — shows a spinner, no output area.
 */
export function createRunningBlock(
  id: number,
  command: string,
  cwd: string,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
): HTMLElement {
  const wrapper = document.createElement('div')
  wrapper.className = 'cmd-block cmd-block-running'
  wrapper.setAttribute('data-block-id', String(id))

  const header = createHeader(command, cwd, null, null, 'running')

  // Overflow menu — minimal: copy command only while running.
  // Always the LAST element of header-right (owner directive).
  const overflow = buildOverflowMenu(command, null)
  const right = header.querySelector('.cmd-header-right')
  if (right) right.appendChild(overflow)

  wrapper.appendChild(header)
  wireBlockSelection(wrapper, getContainer(), overflow, id, onSelect)

  return wrapper
}

/**
 * Freeze a running block: replace it with a frozen version.
 */
export function freezeBlock(
  el: HTMLElement,
  id: number,
  command: string,
  cwd: string,
  outputHtml: string,
  durationMs: number,
  exitCode: number | null,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
): HTMLElement {
  const newEl = createCommandBlock(
    id,
    command,
    cwd,
    outputHtml,
    durationMs,
    exitCode,
    exitCode === 0 ? 'success' : 'failure',
    getContainer,
    onSelect,
  )

  if (el.parentNode) {
    el.parentNode.replaceChild(newEl, el)
  }

  return newEl
}

// ── Block manager ──────────────────────────────────────────────────────────

export interface BlockManagerOpts {
  now?: () => number
}

export class BlockManager {
  private _blocks: BlockRecord[] = []
  private _nextId = 1
  private _now: () => number
  private _scrollbackInner: HTMLElement
  private _xtermContainer: HTMLElement
  private _runningBlock: BlockRecord | null = null
  private _cmdStartTime: number | null = null
  /** Currently selected block id, or null if none selected (P1-8). */
  private _selectedBlockId: number | null = null

  constructor(
    scrollbackInner: HTMLElement,
    xtermContainer: HTMLElement,
    opts: BlockManagerOpts = {},
  ) {
    this._scrollbackInner = scrollbackInner
    this._xtermContainer = xtermContainer
    this._now = opts.now ?? (() => performance.now())
  }

  get blocks(): readonly BlockRecord[] {
    return this._blocks
  }

  get runningBlock(): BlockRecord | null {
    return this._runningBlock
  }

  get cmdStartTime(): number | null {
    return this._cmdStartTime
  }

  /** The currently selected block id, or null (P1-8). */
  get selectedBlockId(): number | null {
    return this._selectedBlockId
  }

  /** Lazy container supplier bound to this manager's scrollback inner. */
  private _getContainer = (): HTMLElement => this._scrollbackInner

  /**
   * Deselect the currently selected block without clearing the block list.
   * Safe to call from keyboard handlers (P0-4: Escape deselects).
   */
  deselectAll(): void {
    if (this._selectedBlockId !== null) {
      const el = this._scrollbackInner.querySelector('.cmd-block-selected')
      if (el) el.classList.remove('cmd-block-selected')
      this._selectedBlockId = null
    }
  }

  /**
   * Called by wireBlockSelection when a block's selection state changes.
   * Keeps _selectedBlockId in sync with single-select semantics (P1-8).
   */
  _onBlockSelected(blockId: number): void {
    if (this._selectedBlockId === blockId) {
      // Clicking the already-selected block deselects it
      this._selectedBlockId = null
      return
    }
    // Deselect previous
    if (this._selectedBlockId !== null) {
      for (const b of this._blocks) {
        if (b.id === this._selectedBlockId) {
          b.el.classList.remove('cmd-block-selected')
        }
      }
    }
    this._selectedBlockId = blockId
  }

  /**
   * Called by wireBlockSelection when a block is deselected.
   */
  _onBlockDeselected(blockId: number): void {
    if (this._selectedBlockId === blockId) {
      this._selectedBlockId = null
    }
  }

  /**
   * Start a new running block. Called on OSC 133 C.
   */
  startBlock(command: string, cwd: string, startLine: number): BlockRecord {
    if (this._runningBlock) {
      this._finalizeRunningUnsafe()
    }

    const id = this._nextId++
    this._cmdStartTime = this._now()

    const el = createRunningBlock(id, command, cwd, this._getContainer, (bid, sel) => {
      if (sel) this._onBlockSelected(bid)
      else this._onBlockDeselected(bid)
    })
    this._scrollbackInner.insertBefore(el, this._xtermContainer)

    const rec: BlockRecord = {
      id,
      command,
      cwd,
      durationMs: null,
      exitCode: null,
      status: 'running',
      startLine,
      endLine: startLine,
      el,
    }
    this._blocks.push(rec)
    this._runningBlock = rec

    return rec
  }

  /**
   * Freeze the running block on OSC 133 D.
   */
  freezeBlock(getLine: GetLineFn, endLine: number, exitCode: number | null): BlockRecord | null {
    const rec = this._runningBlock
    if (!rec) return null

    rec.endLine = endLine
    const durationMs = this._cmdStartTime !== null ? this._now() - this._cmdStartTime : null
    this._cmdStartTime = null

    const snapshot = fromITheme(getCurrentTheme())
    const outputHtml = serializeRange(snapshot, getLine, rec.startLine, endLine)

    const newEl = freezeBlock(
      rec.el,
      rec.id,
      rec.command,
      rec.cwd,
      outputHtml,
      durationMs ?? 0,
      exitCode,
      this._getContainer,
      (bid, sel) => {
        if (sel) this._onBlockSelected(bid)
        else this._onBlockDeselected(bid)
      },
    )

    rec.el = newEl
    rec.durationMs = durationMs
    rec.exitCode = exitCode
    rec.status = exitCode === 0 ? 'success' : 'failure'
    this._runningBlock = null

    return rec
  }

  clearAll(): void {
    for (const b of this._blocks) {
      b.el.remove()
    }
    this._blocks = []
    this._runningBlock = null
    this._cmdStartTime = null
    this._selectedBlockId = null
  }

  private _finalizeRunningUnsafe(): void {
    if (!this._runningBlock) return
    this._runningBlock.status = 'failure'
    this._runningBlock.exitCode = null
    this._runningBlock = null
    this._cmdStartTime = null
  }

  dispose(): void {
    this.clearAll()
  }
}
