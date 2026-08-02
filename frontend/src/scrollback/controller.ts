// DOM scrollback controller — wires the renderer's OSC 133 markers to block
// creation, manages the live region visibility, alt-screen transitions, and
// `clear` detection. Owns the scrollback DOM structure inside the pane.

import type { TerminalRenderer } from '../renderers/types'
import { BlockManager, type GetLineFn } from './blocks'

export type LiveRegionMode = 'idle' | 'running' | 'fullscreen' | 'unstructured'

export interface ScrollbackControllerOpts {
  /** The pane this controller owns the scrollback inside. */
  pane: HTMLElement
  /** The renderer for the terminal. */
  renderer: TerminalRenderer
  /** Injectable clock. */
  now?: () => number
}

export class ScrollbackController {
  readonly scrollbackLayout: HTMLElement
  readonly scrollbackArea: HTMLElement
  readonly scrollbackInner: HTMLElement
  /** Outer clipping container — height changes with mode. */
  readonly xtermLiveContainer: HTMLElement
  /** Inner wrapper with stable min-height — xterm mounts here so its grid
   *  stays sane regardless of the clipping container's CSS height. */
  readonly xtermInner: HTMLElement
  readonly separator: HTMLElement

  private _blockManager: BlockManager
  private _renderer: TerminalRenderer
  private _mode: LiveRegionMode = 'idle'
  /**
   * Sticky for the life of the command. Once a program has filled the pane the
   * flag stays set until the next idle or the next command start, so it cannot
   * oscillate: giving the editor's box back would shrink the scroller, which
   * shrinks the grid, which could put the program back at the ceiling and take
   * the box away again — a layout that flickers forever at a frame's period.
   * Codex's rule for this class of switch, and it applies verbatim: enter on
   * evidence, leave only at a command boundary.
   */
  private _filledPane = false
  /** True while the end of the live output is visible. */
  private _following = true
  private _followObserver: IntersectionObserver | null = null

  constructor(opts: ScrollbackControllerOpts) {
    this._renderer = opts.renderer
    const now = opts.now ?? (() => performance.now())

    // ── Build the scrollback DOM ─────────────────────────────────────────
    this.scrollbackLayout = document.createElement('div')
    this.scrollbackLayout.className = 'scrollback-layout'

    this.scrollbackArea = document.createElement('div')
    this.scrollbackArea.className = 'scrollback-area'

    this.scrollbackInner = document.createElement('div')
    this.scrollbackInner.className = 'scrollback-inner'

    // Blocks live in the inner wrapper.
    this.scrollbackArea.appendChild(this.scrollbackInner)

    // The xterm live container clips the xterm: idle=36px, running=140px,
    // fullscreen fills the viewport. Its child xterm-inner always keeps
    // a minimum height so the xterm grid never collapses to 1 row.
    this.xtermLiveContainer = document.createElement('div')
    this.xtermLiveContainer.className = 'xterm-live-container live-idle'

    this.xtermInner = document.createElement('div')
    this.xtermInner.className = 'xterm-inner'
    this.xtermLiveContainer.appendChild(this.xtermInner)

    // Separator between blocks and live region — inserted before the
    // xterm container so blocks stack above it. Hidden when no blocks.
    this.separator = document.createElement('div')
    this.separator.className = 'scrollback-separator'
    this.separator.style.display = 'none'
    this.scrollbackInner.appendChild(this.separator)
    this.scrollbackInner.appendChild(this.xtermLiveContainer)

    this.scrollbackLayout.appendChild(this.scrollbackArea)

    // Insert the layout as the first child of the pane (before the editor,
    // which is absolute-positioned).
    opts.pane.insertBefore(this.scrollbackLayout, opts.pane.firstChild)

    this._blockManager = new BlockManager(this.scrollbackInner, this.xtermLiveContainer, { now })

    // ── Follow state ─────────────────────────────────────────────────────
    // Whether the end of the live output is on screen. Not "did the last scroll
    // look like a human" — that question has no answer: the `scroll` event does
    // not carry its origin, and a classifier built from wheel and touch misses
    // Page Up, Home, dragging the scrollbar and jumping to a search hit. This
    // asks the thing actually wanted — can the user see the live end — and the
    // browser answers it.
    // Guarded: the test environment has no IntersectionObserver, and this
    // constructor runs inside the mount path — an unguarded `new` there took
    // the whole terminal down with "terminal content failed", not just the
    // follow behaviour. Without it the view simply always follows, which is the
    // behaviour that predates this observer anyway.
    if (typeof IntersectionObserver === 'undefined') return
    this._followObserver = new IntersectionObserver(
      (entries) => {
        for (const e of entries) this._following = e.isIntersecting
      },
      { root: this.scrollbackArea, threshold: 0 },
    )
    this._followObserver.observe(this.xtermLiveContainer)
  }

  /** The element the xterm renderer mounts into. Returns the stable
   *  xterm-inner wrapper, NOT the clipping live-container, so the grid
   *  never collapses to 1 row (P0-1 fix). */
  get mountTarget(): HTMLElement {
    return this.xtermInner
  }

  get blockManager(): BlockManager {
    return this._blockManager
  }

  /** The currently selected block id, or null (P1-8). */
  get selectedBlockId(): number | null {
    return this._blockManager.selectedBlockId
  }

  /** Deselect all blocks (P0-4: Escape key, P1-8: click empty space). */
  deselectBlocks(): void {
    this._blockManager.deselectAll()
  }

  get mode(): LiveRegionMode {
    return this._mode
  }

  // ── Live region visibility ────────────────────────────────────────────

  /** Collapse the live region when the prompt is idle. */
  setIdle(): void {
    if (this._mode === 'fullscreen') return
    this._mode = 'idle'
    // The inline height set by setLiveHeight has to go, or it outranks the
    // class's `height: 0` and an idle region keeps the last command's size.
    this.xtermLiveContainer.style.height = ''
    this._setFilledPane(false)
    this.xtermLiveContainer.className = 'xterm-live-container live-idle'
    this.xtermInner.className = 'xterm-inner'
    this._updateSeparator()
  }

  /** Expand the live region while a command runs. */
  setRunning(): void {
    if (this._mode === 'fullscreen') return
    this._mode = 'running'
    this.xtermLiveContainer.style.height = ''
    this._setFilledPane(false)
    this.xtermLiveContainer.className = 'xterm-live-container live-running'
    this.xtermInner.className = 'xterm-inner'
    this._updateSeparator()
    this._scrollToBottom()
  }

  /** Fill the pane with the live region — for sessions with no shell
   *  integration (no OSC 133 markers yet, e.g. a plain SSH shell): the
   *  scrollback-block model never takes over, so the terminal itself must be
   *  visible at full size. The FIRST marker transitions back to the normal
   *  layout (setIdle/setRunning/enterFullscreen all override this mode).
   *  Class mirrors live-fullscreen's fill treatment; the semantics differ —
   *  this is not an alt-screen program. */
  setUnstructured(): void {
    if (this._mode === 'fullscreen') return
    this._mode = 'unstructured'
    this.xtermLiveContainer.className = 'xterm-live-container live-unstructured'
    this.xtermInner.className = 'xterm-inner inner-fullscreen'
    this._setFilledPane(true)
    const max = this.scrollbackArea.clientHeight
    if (max > 0) this.xtermLiveContainer.style.height = `${max}px`
  }

  /**
   * Size the live region to the output it is showing.
   *
   * The height used to be a constant 140px in `style.css`, sized for the few
   * lines a command normally prints. Anything that repaints a whole screen
   * WITHOUT the alternate buffer — `top`, `claude`, any TUI that keeps your
   * scrollback on purpose — got those same 140 pixels of a pane ten times
   * taller, with the rest blank above it. The alt-screen path was never
   * involved, which is why it looked like alt-screen was broken and was not:
   * `top` sends `ESC[?1h ESC= ESC[?25l ESC[H` and no `1049` at all.
   *
   * The frozen block already did the right thing — a finished command's block
   * is as tall as its output. This makes the live half agree with the half that
   * was already correct.
   *
   * Ignored outside `running`: `idle` is a zero-height region by definition and
   * `fullscreen` is owned by the alt-screen path (nocx-6w4z).
   */
  setLiveHeight(px: number): void {
    if (this._mode !== 'running') return
    if (px <= 0) return
    // The ceiling is the SCROLLER's client height, not the pane's. They are the
    // same number while a command runs — the editor takes its box away when it
    // hides — but not at a prompt, and measuring the element that actually
    // displays the grid is the statement that stays true either way.
    // Less the running block's header, because the two share the viewport. Sized
    // against the bare scroller instead, header plus region came to more than
    // the space available and the last rows of a program that filled the pane
    // had nowhere to be drawn — the same defect as the editor's reserved box,
    // one element along.
    const header = this._blockManager.runningBlock?.el.getBoundingClientRect().height ?? 0
    const max = this.scrollbackArea.clientHeight - header
    if (max <= 0) return
    const previous = this.xtermLiveContainer.getBoundingClientRect().height
    const h = Math.min(px, max)
    if (Math.abs(h - previous) < 0.5) return
    this.xtermLiveContainer.style.height = `${h}px`
    // Keep the block's bottom at the bottom of the view — but only on the frames
    // where the block actually CHANGED SIZE. This runs on every chunk of output,
    // and a program that repaints in place changes nothing about the layout:
    // `top` redrawing the same 33 rows every three seconds was moving the
    // scroll each time for no reason, which is unpleasant if you are reading.
    // The early return above is the whole guard.
    //
    // The rule itself gives both halves of what is wanted, because the region is
    // capped at "viewport minus this block's header": the block therefore FITS,
    // so the bottom of the view shows all of it — and once the output is big
    // enough to fill the pane the header arrives at the top of its own accord
    // and can go no further (nocx-6w4z).
    if (this._following) this._scrollToBottom()
  }

  /**
   * Hide the block chrome, or bring it back.
   *
   * Driven by the ALTERNATE BUFFER and nothing else. A rule was tried that hid
   * the chrome whenever the live region reached the top of the pane, on the
   * reasoning that a header has nowhere to live once a program fills the
   * screen. Warp does not do that: `top` runs there with its block header, its
   * duration and its actions intact, and only a true alt-screen program such as
   * `htop` takes the pane bare. Filling the pane is something a long `ls` does
   * too, and it is not a reason to take away the only handle on the block.
   *
   * So there is no classifier here, and no heuristic anywhere in this file: the
   * program's choice of buffer decides the chrome, and its output decides the
   * height (nocx-6w4z).
   */
  private _setFilledPane(on: boolean): void {
    if (on && this._filledPane) return
    this._filledPane = on
    this.scrollbackInner.classList.toggle('inner-fullscreen-mode', on)
  }

  /**
   * Alt-screen: the program takes the PANE.
   *
   * It used to take the viewport — `position: fixed` with all four insets —
   * which covered the tab strip and the activity bar, so `htop` erased the
   * window's own chrome while `top` (same kind of program, different buffer)
   * did not. Two programs doing the same thing looked like two different
   * products.
   *
   * The two paths are one treatment now: the live region grows to the
   * scroller, the block chrome steps aside, and the editor gives up its
   * reserved box. Which buffer the program chose stops being a layout
   * decision and goes back to being what it is — a detail of how the program
   * preserves your scrollback (nocx-6w4z).
   */
  enterFullscreen(): void {
    this._mode = 'fullscreen'
    this.xtermLiveContainer.className = 'xterm-live-container live-fullscreen'
    this.xtermInner.className = 'xterm-inner inner-fullscreen'
    this._setFilledPane(true)
    const max = this.scrollbackArea.clientHeight
    if (max > 0) this.xtermLiveContainer.style.height = `${max}px`
  }

  /** Exit alt-screen: restore normal layout. */
  exitFullscreen(): void {
    this._mode = 'idle'
    this.xtermLiveContainer.style.height = ''
    this._setFilledPane(false)
    this.xtermLiveContainer.className = 'xterm-live-container live-idle'
    this.xtermInner.className = 'xterm-inner'
    this.scrollbackInner.classList.remove('inner-fullscreen-mode')
    this._updateSeparator()
  }

  // ── Command cycle ─────────────────────────────────────────────────────

  /**
   * Called on OSC 133 C: create a running block, expand the live region.
   */
  onCommandStart(command: string, cwd: string, startLine: number): void {
    const cmd = command || '(empty)'
    this._blockManager.startBlock(cmd, cwd, startLine)
    this.setRunning()
  }

  /**
   * Called on OSC 133 D: serialize output, freeze the block.
   * @param getLine Accessor for xterm buffer lines.
   * @param endLine Absolute buffer line of the OSC 133 D marker.
   * @param exitCode Optional exit code from the D payload.
   */
  onCommandEnd(getLine: GetLineFn, endLine: number, exitCode: number | null): void {
    const rec = this._blockManager.freezeBlock(getLine, endLine, exitCode)
    if (rec) {
      this.setIdle()
      this._scrollToLastBlockStart()
    }
  }

  /**
   * Put the top of the finished command's block at the top of the view.
   *
   * Nothing scrolled here at all before, so the view stayed wherever the
   * running command had left it — and a program that had filled the pane left
   * it at the top of a block taller than the viewport, showing its first screen
   * with the prompt somewhere far below. Neither "stay put" nor "jump to the
   * bottom" is right for a block UI: what you want to read when a command
   * finishes is the command and the start of what it printed.
   *
   * A frame late on purpose. `setIdle` has just collapsed the live region and
   * the block was inserted in the same tick, so the scroller's height is still
   * the old one until layout runs.
   */
  private _scrollToLastBlockStart(): void {
    requestAnimationFrame(() => {
      const blocks = this.scrollbackInner.querySelectorAll('.cmd-block')
      const last = blocks[blocks.length - 1]
      if (!last) return
      last.scrollIntoView({ block: 'start', behavior: 'instant' })
    })
  }

  // ── clear handling ────────────────────────────────────────────────────

  /**
   * Check if a command was `clear` (or starts with `clear`). If so, clear
   * all DOM blocks. The xterm viewport is already cleared by the escape
   * sequence `clear` emits — we just clean up our blocks.
   */
  maybeClear(command: string): void {
    const trimmed = command.trim()
    const firstWord = trimmed.split(/\s+/)[0] ?? ''
    const isClear = firstWord === 'clear' || firstWord.endsWith('/clear')
    if (isClear) {
      this._blockManager.clearAll()
      this._updateSeparator()
    }
  }

  // ── Auto-scroll ───────────────────────────────────────────────────────

  /** Scroll to the bottom, unless the user has scrolled away from the live end. */
  scrollToBottom(): void {
    if (!this._following) return
    this._scrollToBottom()
  }

  private _scrollToBottom(): void {
    this.scrollbackArea.scrollTo({
      top: this.scrollbackArea.scrollHeight,
      behavior: 'instant',
    })
  }

  private _updateSeparator(): void {
    const hasBlocks = this._blockManager.blocks.length > 0
    this.separator.style.display = hasBlocks && this._mode !== 'fullscreen' ? '' : 'none'
  }

  dispose(): void {
    this._followObserver?.disconnect()
    this._followObserver = null
    this._blockManager.dispose()
    this.scrollbackLayout.remove()
  }
}
