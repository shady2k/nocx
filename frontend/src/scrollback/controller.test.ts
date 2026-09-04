// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import type { TerminalRenderer, RenderFenceEvent } from '../renderers/types'
import { ScrollbackController } from './controller'
import { CommandSnapshotStore } from '../command-snapshot'
import type { LiveContentHeightSpy } from '../test-support/panes-fixtures'
import type { ExecutionAttempt } from '../lifecycle/state'
import { mintDomain, type IntegrationDomain } from '../lifecycle/domains'
import { BufferLine } from './test-helpers'
import { PetOverlay } from '../pets/overlay'
import { mountWindowPet, unmountWindowPet } from '../pets/window-pet'

function makeRenderer(): TerminalRenderer {
  return {
    write: vi.fn(),
    onData: vi.fn(),
    onCommandMarker: vi.fn(),
    onBufferChange: vi.fn(),
    onTitle: vi.fn(),
    mount: vi.fn(() => Promise.resolve({ cols: 80, rows: 24 })),
    dispose: vi.fn(),
    focus: vi.fn(),
    setReadOnly: vi.fn(),
    registerMarker: vi.fn(() => undefined),
    paste: vi.fn(),
    clearViewport: vi.fn(),
    fitViewport: vi.fn(),
    // 0 = "cannot measure", which the frozen-block metric publisher treats
    // as "publish nothing" — existing tests are unaffected by the metric.
    cellWidth: 0,
    // 0 = "cannot measure yet"; setRunning sizes the live region from this
    // on the first frame (the command-start pop), so tests that want the
    // sizing path override it.
    liveContentHeight: vi.fn(() => 0),
    liveCursorBottom: vi.fn(() => 0),
    getBufferLine: vi.fn(() => null),
    cursorLine: vi.fn(() => 0),
    reset: vi.fn(),
  } as unknown as TerminalRenderer
}

function makeController() {
  const pane = document.createElement('div')
  const controller = new ScrollbackController({
    pane,
    renderer: makeRenderer(),
    snapshotStore: new CommandSnapshotStore(),
  })
  // jsdom does no layout: give the scrollback area a real height so the
  // fill-the-pane sizing has something to read.
  Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
    value: 360,
    configurable: true,
  })
  return { pane, controller }
}

describe('ScrollbackController restorePast (nocx-l21ib.3)', () => {
  it('lands the pane on the newest restored block, not the oldest', () => {
    const { controller } = makeController()
    const scrollTo = vi.fn()
    controller.scrollbackArea.scrollTo = scrollTo
    // jsdom does no layout: name the height the restored stack would have.
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      value: 4000,
      configurable: true,
    })

    const block = (label: string): HTMLElement => {
      const el = document.createElement('div')
      el.dataset.restored = 'true'
      el.textContent = label
      return el
    }
    controller.restorePast([block('oldest'), block('newest')])

    expect(scrollTo).toHaveBeenCalledWith({ top: 4000, behavior: 'instant' })
  })

  it('scrolls even when the follow sentinel says the person is not at the live end', () => {
    // The guard on the public scrollToBottom asks whether the person scrolled
    // AWAY from the live end. At a restore nobody has scrolled anything, and
    // the sentinel that answers it is exactly what the insert pushes out of
    // the scroller — so a restore that honoured the guard would leave the
    // pane at the top for a position the user never chose.
    const { controller } = makeController()
    const scrollTo = vi.fn()
    controller.scrollbackArea.scrollTo = scrollTo
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      value: 2500,
      configurable: true,
    })
    // Not following: the public door must stay shut...
    ;(controller as unknown as { _following: boolean })._following = false
    controller.scrollToBottom()
    expect(scrollTo).not.toHaveBeenCalled()

    // ...and the restore must still land at the bottom.
    const el = document.createElement('div')
    controller.restorePast([el])
    expect(scrollTo).toHaveBeenCalledWith({ top: 2500, behavior: 'instant' })
  })

  it('does nothing at all when there is no past to draw', () => {
    const { controller } = makeController()
    const scrollTo = vi.fn()
    controller.scrollbackArea.scrollTo = scrollTo
    controller.restorePast([])
    expect(scrollTo).not.toHaveBeenCalled()
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
  })
})

// ── `clear` empties the WHOLE scrollback (nocx-0zb1m) ─────────────────────
// The restored past used to be inserted past the manager, straight into
// `.scrollback-inner`, so `clearAll` — which walks what the manager holds —
// could not see it: `clear` ran, got its own block, and the previous
// session stayed on screen under its boundary. One owner is the fix, so
// these tests are about what the CONTAINER holds after the clear path, not
// about which list the manager kept it in.
describe('clear empties the whole scrollback, restored blocks included (nocx-0zb1m)', () => {
  /** A restored block as `restoredBlock()` builds one: a `.cmd-block`
   *  carrying `data-restored`. */
  const restored = (label: string): HTMLElement => {
    const el = document.createElement('div')
    el.className = 'cmd-block'
    el.dataset.restored = 'true'
    el.textContent = label
    return el
  }

  function makeRestoredController() {
    const { controller } = makeController()
    const scrollTo = vi.fn()
    controller.scrollbackArea.scrollTo = scrollTo
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      value: 4000,
      configurable: true,
    })
    return { controller, scrollTo }
  }

  it('takes the restored blocks and their boundary with it', () => {
    const { controller } = makeRestoredController()
    controller.restorePast([restored('oldest'), restored('newest')])
    controller.blockManager.startBlock('clear', '~', 0)
    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(2)

    controller.maybeClear('clear')

    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(0)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(0)
  })

  it('and a frozen block goes too — the freeze swaps the element the manager owns', () => {
    const { controller } = makeRestoredController()
    controller.restorePast([restored('old')])
    controller.blockManager.startBlock('ls', '~', 0)
    controller.blockManager.freezeBlock((y) => (y === 0 ? new BufferLine('out') : undefined), 0, 0)
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(2)

    controller.maybeClear('clear')

    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(0)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
  })

  it('restoration itself is unchanged: the past is above the present, the boundary between them', () => {
    const { controller, scrollTo } = makeRestoredController()
    // A pane that has already printed something: the past must not land
    // underneath its present.
    const live = controller.blockManager.startBlock('echo hi', '~', 0).el

    controller.restorePast([restored('oldest'), restored('newest')])

    const kids = Array.from(controller.scrollbackInner.children)
    const boundary = controller.scrollbackInner.querySelector('.scrollback-restore-boundary')
    expect(boundary?.textContent).toBe('Previous session')
    // oldest, newest, boundary, ... live block ... — the past above, the
    // line that labels it directly under it, the live session below.
    expect(kids.slice(0, 3).map((k) => k.textContent)).toEqual([
      'oldest',
      'newest',
      'Previous session',
    ])
    expect(kids.indexOf(live)).toBeGreaterThan(kids.indexOf(boundary as Element))
    // And the view still lands at the newest block.
    expect(scrollTo).toHaveBeenCalledWith({ top: 4000, behavior: 'instant' })
  })

  it('a clear in a pane with NO restored past still clears exactly what it did before', () => {
    const { controller } = makeRestoredController()
    controller.blockManager.startBlock('ls', '~', 0)
    controller.blockManager.freezeBlock((y) => (y === 0 ? new BufferLine('out') : undefined), 0, 0)
    controller.blockManager.startBlock('clear', '~', 0)

    controller.maybeClear('clear')

    expect(controller.blockManager.blocks.length).toBe(0)
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(0)
    // The chrome the manager anchors on is NOT the manager's to remove.
    expect(controller.scrollbackInner.querySelector('.xterm-live-container')).not.toBeNull()
    expect(controller.scrollbackInner.querySelector('.scrollback-separator')).not.toBeNull()
  })

  it('a restore into a pane that has already been cleared still draws the past', () => {
    const { controller } = makeRestoredController()
    controller.blockManager.startBlock('clear', '~', 0)
    controller.maybeClear('clear')

    controller.restorePast([restored('oldest'), restored('newest')])

    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(2)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).not.toBeNull()
    // And a second clear takes that past too — the registration is not a
    // one-shot that only the first restore gets.
    controller.maybeClear('clear')
    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(0)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
  })
})

describe('ScrollbackController unstructured mode', () => {
  it('fills the pane for a markerless session (plain SSH before any OSC 133)', () => {
    const { controller } = makeController()
    expect(controller.mode).toBe('idle')

    controller.setUnstructured()

    expect(controller.mode).toBe('unstructured')
    expect(controller.xtermLiveContainer.className).toContain('live-unstructured')
    expect(controller.xtermLiveContainer.style.height).toBe('360px')
  })

  it('lets the first OSC-133 marker transition back to the normal layout', () => {
    const { controller } = makeController()
    controller.setUnstructured()

    // The marker arrives: PROMPT_READY collapses the live region to idle.
    controller.setIdle()

    expect(controller.mode).toBe('idle')
    expect(controller.xtermLiveContainer.className).toContain('live-idle')
  })

  it('markerless alt-screen return restores the full pane, not the hidden idle', () => {
    const { controller } = makeController()
    controller.setUnstructured()
    controller.enterFullscreen()
    expect(controller.mode).toBe('fullscreen')

    // Buffer returned to normal without any OSC-133 ever arriving: the
    // terminal must fill the pane again, not collapse.
    controller.exitFullscreen()
    controller.setUnstructured()

    expect(controller.mode).toBe('unstructured')
    expect(controller.xtermLiveContainer.className).toContain('live-unstructured')
  })
})

describe('the live region follows the pane it fills (nocx-liveheight)', () => {
  // The defect: setUnstructured and enterFullscreen each wrote the scroller's
  // height to the box ONCE, at mode entry, and the re-measure path returned at
  // its first line unless the mode was `running`. So a pane that changed size
  // afterwards — the integration card raised and dismissed, or the window
  // resized — left the box at the height it had when the mode began. Because
  // .scrollback-inner hangs from the bottom of the scroller, the leftover
  // pixels showed up as a blank strip at the TOP, and the rows past the box
  // were clipped by its overflow: a 40-row grid showing 34 rows of `top`.
  it('re-measures an unstructured session when the scroller grows', () => {
    const { controller } = makeController()
    controller.setUnstructured()
    expect(controller.xtermLiveContainer.style.height).toBe('360px')

    // The card is dismissed and the pane gives the pixels back.
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 540,
      configurable: true,
    })
    controller.setLiveHeight(null)

    expect(controller.xtermLiveContainer.style.height).toBe('540px')
  })

  it('re-measures an alt-screen program when the scroller shrinks', () => {
    const { controller } = makeController()
    controller.enterFullscreen()
    expect(controller.xtermLiveContainer.style.height).toBe('360px')

    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 200,
      configurable: true,
    })
    controller.setLiveHeight(null)

    expect(controller.xtermLiveContainer.style.height).toBe('200px')
  })

  it('leaves an idle pane alone: there is no live box to fill', () => {
    const { controller } = makeController()
    controller.setIdle()
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 540,
      configurable: true,
    })
    controller.setLiveHeight(null)

    expect(controller.xtermLiveContainer.style.height).toBe('')
  })
})

describe('finished command landing', () => {
  let pendingFrames: FrameRequestCallback[]

  beforeEach(() => {
    pendingFrames = []
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      pendingFrames.push(callback)
      return pendingFrames.length
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function flushFrames(): void {
    while (pendingFrames.length > 0) {
      pendingFrames.shift()!(performance.now())
    }
  }

  type ScrollIntoViewSpy = (arg?: ScrollIntoViewOptions | boolean) => void

  function setFollowing(controller: ScrollbackController, following: boolean): void {
    const state = controller as unknown as { _following: boolean }
    state._following = following
  }

  function finishWithMeasuredBlock(
    height: number,
    scrollIntoView: Mock<ScrollIntoViewSpy>,
  ): ScrollbackController {
    const { controller } = makeController()
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      configurable: true,
      value: 500,
    })
    controller.beginBlockNow('printf output', '~', 0, 0)
    controller.onCommandEnd(() => new BufferLine('output'), 2, 0)
    const block = controller.scrollbackInner.querySelector<HTMLElement>('.cmd-block')
    expect(block).not.toBeNull()
    Object.defineProperty(block!, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ height }),
    })
    block!.scrollIntoView = scrollIntoView
    flushFrames()
    return controller
  }

  it('lands a block taller than the viewport at its end', () => {
    const scrollIntoView = vi.fn<ScrollIntoViewSpy>()
    const controller = finishWithMeasuredBlock(720, scrollIntoView)

    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'end', behavior: 'instant' })
    controller.blockManager.clearAll()
  })

  it('does not scroll a block that fits the viewport', () => {
    const scrollIntoView = vi.fn<ScrollIntoViewSpy>()
    const controller = finishWithMeasuredBlock(300, scrollIntoView)

    expect(scrollIntoView).not.toHaveBeenCalled()
    controller.blockManager.clearAll()
  })

  it('does not move a person who scrolled away from the live end', () => {
    const { controller } = makeController()
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      configurable: true,
      value: 500,
    })
    const scrollIntoView = vi.fn<ScrollIntoViewSpy>()
    setFollowing(controller, false)
    controller.beginBlockNow('printf output', '~', 0, 0)
    controller.onCommandEnd(() => new BufferLine('output'), 2, 0)
    const block = controller.scrollbackInner.querySelector<HTMLElement>('.cmd-block')
    expect(block).not.toBeNull()
    Object.defineProperty(block!, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ height: 720 }),
    })
    block!.scrollIntoView = scrollIntoView
    flushFrames()

    expect(scrollIntoView).not.toHaveBeenCalled()
    controller.blockManager.clearAll()
  })
})

describe('ScrollbackController render-fence rendezvous (nocx-u7uh.8)', () => {
  const FENCE = 'ab'.repeat(32)
  const domain = mintDomain({
    lane: 'l',
    lifecycle: 'prompt_ready',
    domain: 'd1',
    epoch: 1,
  }) as IntegrationDomain

  // jsdom implements no scrollIntoView; the deferred-freeze settle scrolls
  // the finished block into view via rAF, exactly like terminal-content's
  // composition-root tests stub it.
  const protoScrollIntoView = Element.prototype.scrollIntoView?.bind(Element.prototype)
  beforeEach(() => {
    Element.prototype.scrollIntoView = () => {}
  })
  afterEach(() => {
    Element.prototype.scrollIntoView = protoScrollIntoView
  })

  function completedAttempt(fence: string): ExecutionAttempt {
    return {
      id: 'att-1',
      domain,
      state: 'completed',
      exitCode: 0,
      fence,
    }
  }

  /** A renderer that records the fence callback instead of wiring it. */
  function rendererWithFence(): {
    renderer: TerminalRenderer
    sight: (ev: RenderFenceEvent) => void
  } {
    const renderer = makeRenderer()
    let fenceCb: ((ev: RenderFenceEvent) => void) | null = null
    renderer.onRenderFence = (cb: (ev: RenderFenceEvent) => void) => {
      fenceCb = cb
    }
    return {
      renderer,
      sight: (ev) => fenceCb?.(ev),
    }
  }
  it('flips the status on the completion event and settles the boundary when the fence arrives', () => {
    const { renderer, sight } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    // The block opens at submit and binds to the published attempt.
    controller.blockManager.startBlock('make', '~', 0)
    controller.blockManager.bindAttempt('att-1')
    controller.setRunning()

    // The authenticated completion lands with the fence still in flight:
    // the LOGICAL freeze lands now — status flips, the running slot frees
    // — while the VISUAL boundary defers (false: the live region stays up).
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 2)).toBe(false)
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.blockManager.blockForAttempt('att-1')?.status).toBe('success')
    // PromptReady may arrive before the render fence. It must not collapse
    // the live rows into empty space while their DOM boundary is pending.
    controller.setIdle()
    expect(controller.mode).toBe('running')

    // The fence bytes land (via the renderer's OSC 1337 handler): the block
    // serializes at the fence's line and the live region settles.
    sight({ hex: FENCE, line: 5, buffer: 'normal' })
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.blockManager.blockForAttempt('att-1')?.status).toBe('success')
    expect(controller.blockManager.blockForAttempt('att-1')?.endLine).toBe(5)
    expect(controller.mode).toBe('idle')
  })

  it('a fence in the alternate buffer is ignored — it has no scrollback line to serialize', () => {
    const { renderer, sight } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    // jsdom implements no scrollTo; setRunning scrolls to the live end.
    controller.scrollbackArea.scrollTo = vi.fn()
    controller.blockManager.startBlock('make', '~', 0)
    controller.blockManager.bindAttempt('att-1')
    controller.setRunning() // the live region is up while the block runs
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 2)).toBe(false)

    // The status flipped on the event; the boundary is still pending. An
    // alternate-buffer fence is ignored: the pending stays and the live
    // region is NOT settled.
    sight({ hex: FENCE, line: 5, buffer: 'alternate' })
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.blockManager.blockForAttempt('att-1')?.status).toBe('success')
    expect(controller.mode).toBe('running')
    controller.blockManager.clearAll()
  })
  it('a second shell-originated attempt opens its own block while the first is pending its fence — no merge (nocx-m87n)', () => {
    const { renderer, sight } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    // First shell-originated command: the running fact opens the block.
    controller.beginBlock('codex', '~', 0)
    controller.blockManager.bindAttempt('att-1')
    expect(controller.mode).toBe('running')

    // Its completion lands while the fence is still in flight: the LOGICAL
    // freeze flips the status and frees the running slot, but the VISUAL
    // boundary defers — the live region stays up (u7uh.8).
    expect(
      controller.freezeFromAttempt({ ...completedAttempt(FENCE), id: 'att-1', exitCode: 130 }, 3),
    ).toBe(false)
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.blockManager.blockForAttempt('att-1')?.status).toBe('failure')

    // The second shell-originated command starts while the first block's
    // boundary is still pending: a NEW block opens and owns the running
    // slot (the owner's Ctrl-C then `codex` again — keys are raw, so the
    // second command arrives through openBlock, not the editor).
    controller.beginBlock('codex', '~', 4)
    controller.blockManager.bindAttempt('att-2')
    expect(controller.blockManager.blocks).toHaveLength(2)
    expect(controller.blockManager.runningBlock?.status).toBe('running')
    expect(controller.blockManager.blockForAttempt('att-2')?.status).toBe('running')

    // The first fence lands: the first block freezes with its own exit
    // status — while the second command is still running.
    sight({ hex: FENCE, line: 3, buffer: 'normal' })
    const first = controller.blockManager.blockForAttempt('att-1')
    expect(first?.status).toBe('failure')
    expect(first?.exitCode).toBe(130)
    expect(first?.el.classList.contains('cmd-block-running')).toBe(false)
    const second = controller.blockManager.blockForAttempt('att-2')
    expect(second?.status).toBe('running')
    expect(controller.blockManager.runningBlock).toBe(second)
    // The live region belongs to the second command, not the first's tail.
    expect(controller.mode).toBe('running')
    controller.blockManager.clearAll()
  })
})

describe('the frozen block\u2019s rows leave the grid (nocx-m87n live-region window)', () => {
  // The live region is the xterm grid clipped to the box `setLiveHeight`
  // sizes. A block's rows are serialized into its DOM element at freeze,
  // but they STAY in the grid — unless the viewport is cleared at the
  // freeze boundary. On a grid that has not scrolled, the box then
  // re-displays those rows inside the running command: `ls`, its output,
  // `pwd`, its output, and only then the running command's own rows, each
  // row on screen twice (once frozen, once live). This describe block
  // pins the seam that prevents it: every freeze hands the rows to the DOM
  // and clears them from the grid, and the clear never fires while a newer
  // command owns the running slot (its rows share the buffer below the
  // frozen ones — wiping the grid would wipe its serialization window).
  const FENCE = 'ab'.repeat(32)
  const FENCE2 = 'cd'.repeat(32)
  // The fence-rendezvous describe above scopes its own renderer factory,
  // domain and attempt helper — this sibling describe needs its own.
  function rendererWithFence(): {
    renderer: TerminalRenderer
    sight: (ev: RenderFenceEvent) => void
  } {
    const renderer = makeRenderer()
    let fenceCb: ((ev: RenderFenceEvent) => void) | null = null
    renderer.onRenderFence = (cb: (ev: RenderFenceEvent) => void) => {
      fenceCb = cb
    }
    return {
      renderer,
      sight: (ev) => fenceCb?.(ev),
    }
  }
  const domain = mintDomain({
    lane: 'l',
    lifecycle: 'prompt_ready',
    domain: 'd1',
    epoch: 1,
  }) as IntegrationDomain
  function completedAttempt(fence: string): ExecutionAttempt {
    return {
      id: 'att-1',
      domain,
      state: 'completed',
      exitCode: 0,
      fence,
    }
  }

  it('clears the grid when a block freezes, so the live region never re-displays rows the DOM block owns', () => {
    const { renderer, sight } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    /* eslint-disable @typescript-eslint/unbound-method */
    const clearViewport = renderer.clearViewport
    /* eslint-enable @typescript-eslint/unbound-method */
    controller.scrollbackArea.scrollTo = vi.fn()

    // `ls` runs and finishes: the block freezes at the sighted fence line
    // and its rows leave the grid.
    controller.beginBlock('ls', '~', 0)
    controller.blockManager.bindAttempt('att-1')
    expect(controller.mode).toBe('running')
    sight({ hex: FENCE, line: 2, buffer: 'normal' })
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 2)).toBe(true)
    expect(controller.mode).toBe('idle')
    expect(clearViewport).toHaveBeenCalledTimes(1)

    // `pwd` runs and finishes the same way — a second freeze, a second clear.
    controller.beginBlock('pwd', '~', 3)
    controller.blockManager.bindAttempt('att-2')
    sight({ hex: FENCE2, line: 4, buffer: 'normal' })
    expect(controller.freezeFromAttempt({ ...completedAttempt(FENCE2), id: 'att-2' }, 4)).toBe(true)
    expect(clearViewport).toHaveBeenCalledTimes(2)

    // `codex` runs: its rows are the ONLY rows in the grid (both earlier
    // blocks were cleared at their freezes), and nothing clears mid-run.
    controller.beginBlock('codex', '~', 5)
    controller.blockManager.bindAttempt('att-3')
    expect(controller.mode).toBe('running')
    expect(clearViewport).toHaveBeenCalledTimes(2)
    controller.blockManager.clearAll()
  })

  it('a deferred freeze clears the grid only when no newer command owns the running slot', () => {
    const { renderer, sight } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    /* eslint-disable @typescript-eslint/unbound-method */
    const clearViewport = renderer.clearViewport
    /* eslint-enable @typescript-eslint/unbound-method */
    controller.scrollbackArea.scrollTo = vi.fn()

    // The first command completes with its fence still in flight: the
    // VISUAL freeze defers and the grid is untouched.
    controller.beginBlock('codex', '~', 0)
    controller.blockManager.bindAttempt('att-1')
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 2)).toBe(false)
    expect(clearViewport).not.toHaveBeenCalled()

    // A second command starts while the first's boundary is pending: its
    // rows sit BELOW the first block's rows in the buffer. The first
    // fence landing must serialize the first block WITHOUT clearing —
    // clearing would wipe the second command's still-unserialized rows.
    controller.beginBlock('codex', '~', 4)
    controller.blockManager.bindAttempt('att-2')
    sight({ hex: FENCE, line: 3, buffer: 'normal' })
    expect(
      controller.blockManager.blockForAttempt('att-1')?.el.classList.contains('cmd-block-running'),
    ).toBe(false)
    expect(controller.blockManager.runningBlock).toBe(
      controller.blockManager.blockForAttempt('att-2'),
    )
    expect(clearViewport).not.toHaveBeenCalled()

    // The second command completes and its fence is already sighted: the
    // rec path freezes it and NOW the grid clears — its rows were the last
    // in the buffer.
    sight({ hex: FENCE2, line: 7, buffer: 'normal' })
    expect(controller.freezeFromAttempt({ ...completedAttempt(FENCE2), id: 'att-2' }, 7)).toBe(true)
    expect(clearViewport).toHaveBeenCalledTimes(1)
    controller.blockManager.clearAll()
  })
})

describe('the echoed command line leaves the live region too (nocx-w1n4)', () => {
  // The frozen body already skips the echo: the app-owned submit opens the
  // block BEFORE the bytes, the shell's echo lands on the creation line,
  // and the output range starts one row later (nocx-4yhi). The LIVE region
  // is the xterm grid itself, which still holds that echoed line on the
  // creation row — so the running block showed one row more than the frozen
  // one will. The range was decided in the block model; this describe pins
  // the grid to the same decision: the region's first SHOWN row is
  // outputStart. The box clips the grid's TOP rows, so offsetting the
  // box's height hides the bottom, never the echo — the grid itself must
  // move, and it does: a vertical translate on the inner wrapper.
  const FENCE = 'ab'.repeat(32)
  const domain = mintDomain({
    lane: 'l',
    lifecycle: 'prompt_ready',
    domain: 'd1',
    epoch: 1,
  }) as IntegrationDomain
  const completedAttempt = (fence: string): ExecutionAttempt => ({
    id: 'att-1',
    domain,
    state: 'completed',
    exitCode: 0,
    fence,
  })

  /** A renderer whose cell geometry the controller can read, with a
   *  settable viewport top — the number that decides when the echo has
   *  scrolled out of the grid. */
  function rendererWithGeometry(): {
    renderer: TerminalRenderer
    sight: (ev: RenderFenceEvent) => void
    setViewportTop: (line: number) => void
  } {
    const renderer = makeRenderer()
    let fenceCb: ((ev: RenderFenceEvent) => void) | null = null
    renderer.onRenderFence = (cb: (ev: RenderFenceEvent) => void) => {
      fenceCb = cb
    }
    let top = 0
    Object.defineProperty(renderer, 'cellHeight', { value: 16, configurable: true })
    Object.defineProperty(renderer, 'viewportTopLine', {
      configurable: true,
      get: () => top,
    })
    return {
      renderer,
      sight: (ev) => fenceCb?.(ev),
      setViewportTop: (line: number) => {
        top = line
      },
    }
  }

  it('releases the shift when the program overwrote the echo row (nocx-hp8p2.9)', () => {
    const { renderer } = rendererWithGeometry()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()

    controller.beginBlock('top', '~', 0, 1)
    expect(controller.xtermInner.style.transform).toBe('translateY(-16px)')

    // `top` clears the screen and paints from home, so its FIRST line lands
    // on the row the shell echoed the command on. The shift would then hide
    // a line of the program's output — the uptime header — instead of the
    // echo, which is exactly what the owner saw.
    renderer.getBufferLine = () =>
      ({
        translateToString: () => 'top - 19:20:15 up 18 days,  1:53,  2 users',
      }) as unknown as ReturnType<typeof renderer.getBufferLine>
    controller.setLiveHeight(3 * 16)
    expect(controller.xtermInner.style.transform).toBe('')
  })

  it('hides the echo row from the running block and releases it once the grid scrolls past', () => {
    const { renderer, sight, setViewportTop } = rendererWithGeometry()

    /* eslint-disable @typescript-eslint/unbound-method */
    const clearViewport = renderer.clearViewport
    /* eslint-enable @typescript-eslint/unbound-method */
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()

    // App-owned submit: the block opens at the prompt line and the output
    // range starts one row later (nocx-4yhi) — the shape Defect 1 is about.
    // The grid was cleared at the previous freeze, so the echo row IS the
    // grid's top row and the shift is exactly one cell.
    controller.beginBlock('ls', '~', 0, 1)
    expect(controller.mode).toBe('running')
    expect(controller.xtermInner.style.transform).toBe('translateY(-16px)')

    // Output arrives: the flow box and clip reserve only the two shown rows;
    // the translated echo row occupies neither visible space nor layout.
    controller.setLiveHeight(3 * 16)
    expect(controller.xtermLiveContainer.style.height).toBe('32px')
    expect(controller.xtermLiveViewport.style.height).toBe('32px')
    expect(controller.xtermInner.style.transform).toBe('translateY(-16px)')

    // A measurement no taller than the translated echo must clear, rather
    // than retain, stale flow space from a previous chunk.
    controller.setLiveHeight(8)
    expect(controller.xtermLiveContainer.style.height).toBe('0px')
    expect(controller.xtermLiveViewport.style.height).toBe('0px')

    // The output outgrows the viewport: the echo row scrolls above the
    // grid, and the shift MUST release — a stale shift would clip the
    // first real output row instead.
    setViewportTop(1)
    controller.setLiveHeight(24 * 16)
    expect(controller.xtermInner.style.transform).toBe('')
    // Both the content clip and flow box clamp to the live-region cap.
    expect(controller.xtermLiveViewport.style.height).toBe('360px')
    expect(controller.xtermLiveContainer.style.height).toBe('360px')

    // The shift is live, not one-shot: back before the echo scrolled out,
    // it re-applies on the next sizing frame.
    setViewportTop(0)
    controller.setLiveHeight(3 * 16)
    expect(controller.xtermInner.style.transform).toBe('translateY(-16px)')
    expect(controller.xtermLiveViewport.style.height).toBe('32px')
    expect(controller.xtermLiveContainer.style.height).toBe('32px')

    // Freeze hands the rows to the DOM and the live region settles: the
    // shift is gone at idle, exactly like the box's height.
    controller.blockManager.bindAttempt('att-1')
    sight({ hex: FENCE, line: 3, buffer: 'normal' })
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 3)).toBe(true)
    expect(controller.mode).toBe('idle')
    expect(controller.xtermInner.style.transform).toBe('')
    expect(clearViewport).toHaveBeenCalledTimes(1)
    controller.blockManager.clearAll()
  })

  it('reserves the block body padding around the rows, and caps the WHOLE box', () => {
    // THE RUNNING REGION IS THE BLOCK'S BODY. The frozen body has padding
    // (`--cmd-output-pad-*`); until the live region wore the same, it was
    // that much shorter than the body that replaces it, and the scrollback —
    // which hangs from its bottom edge — moved at every freeze. The number
    // is never repeated in TypeScript: the controller reads it off the
    // element, which is what this test writes to it.
    const { renderer } = rendererWithGeometry()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    controller.xtermLiveContainer.style.paddingTop = '2px'
    controller.xtermLiveContainer.style.paddingBottom = '6px'
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()

    controller.beginBlock('ls', '~', 0, 1)
    // Two shown rows (the third is the translated echo): the clip holds the
    // rows, the flow box holds the rows plus the body's 8px.
    controller.setLiveHeight(3 * 16)
    expect(controller.xtermLiveViewport.style.height).toBe('32px')
    expect(controller.xtermLiveContainer.style.height).toBe('40px')

    // The cap is a ceiling on the BOX, not on the rows inside it: a grid
    // taller than the pane must not push the region past the space it shares
    // with the running block's header (nocx-zn4d), padding included.
    controller.setLiveHeight(100 * 16)
    expect(controller.xtermLiveContainer.style.height).toBe('360px')
    expect(controller.xtermLiveViewport.style.height).toBe('352px')
    controller.blockManager.clearAll()
    pane.remove()
  })
})

describe('the pane moves rather than jumping (nocx-i4h04.2)', () => {
  /** A controller whose stack reports a position the test controls, and whose
   *  two moving elements record what they were asked to animate. */
  function movingController(tops: number[]): {
    controller: ScrollbackController
    frames: Array<{ el: string; keyframes: unknown }>
  } {
    const renderer = makeRenderer()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 600,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    // jsdom lays nothing out, so the stack's position is the test's to say:
    // one answer per measurement, in the order `_glide` takes them.
    const queue = [...tops]
    controller.scrollbackInner.getBoundingClientRect = () =>
      ({ top: queue.length > 1 ? (queue.shift() as number) : queue[0] }) as DOMRect
    const frames: Array<{ el: string; keyframes: unknown }> = []
    for (const [name, el] of [
      ['inner', controller.scrollbackInner],
      ['live', controller.xtermLiveContainer],
    ] as const) {
      ;(el as unknown as { animate: unknown }).animate = (keyframes: unknown) => {
        frames.push({ el: name, keyframes })
        return { cancel: () => {}, finished: Promise.resolve() } as unknown as Animation
      }
    }
    return { controller, frames }
  }

  beforeEach(() => {
    window.matchMedia = (query: string) =>
      ({
        matches: false,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList
  })

  it('gives the stack the inverse of what it just moved, and releases it', async () => {
    // FLIP: the DOM change lands whole, then the stack is put back where it
    // looked to be and let go. What animates is a transform — it takes no part
    // in layout, which is why this is not the height animation that was tried
    // and removed.
    const { controller, frames } = movingController([500, 440])

    controller.beginBlock('ls', '~', 0, 1)

    // ONE element, because the stack is one element — and the follow
    // observer's sentinel is deliberately outside it (nocx-i4h04.3).
    expect(frames.map((f) => f.el)).toEqual(['inner'])
    // And no scrollbar for the settle's own overflow: the displaced stack
    // hangs past the scroller's bottom edge for as long as this lasts, which
    // flashed a bar on every command (nocx-i4h04.4).
    expect(controller.scrollbackArea.classList.contains('is-settling')).toBe(true)
    for (const f of frames) {
      expect(f.keyframes).toEqual([
        { transform: 'translateY(60px)' },
        { transform: 'translateY(0px)' },
      ])
    }

    // The settle ends and the scroller offers its bar again.
    await Promise.resolve()
    await Promise.resolve()
    expect(controller.scrollbackArea.classList.contains('is-settling')).toBe(false)
    controller.blockManager.clearAll()
  })

  it('answers "am I following the output" from outside the stack it moves', () => {
    // The observer reports the TRANSFORMED box. While the settle displaces the
    // stack, anything inside it is out of the scroller — and the live region,
    // which used to be the target, is inside it. The pane then concluded the
    // person had scrolled away and stopped following the output, which is a
    // defect on its own and skipped every later glide (nocx-i4h04.3).
    const { controller } = movingController([500, 500])

    expect(controller.followSentinel.parentElement).toBe(controller.scrollbackArea)
    expect(controller.scrollbackInner.contains(controller.followSentinel)).toBe(false)
    expect(controller.scrollbackInner.contains(controller.xtermLiveContainer)).toBe(true)
  })

  it('moves nothing when the person asked for less motion', () => {
    window.matchMedia = (query: string) =>
      ({
        matches: true,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList
    const { controller, frames } = movingController([500, 440])

    controller.beginBlock('ls', '~', 0, 1)

    expect(frames).toEqual([])
    controller.blockManager.clearAll()
  })

  it('moves nothing while the person is reading further up', () => {
    // Movement they did not cause is not theirs to watch: a pane scrolled away
    // from the bottom keeps its position, and the glide would be a second
    // owner of it.
    const { controller, frames } = movingController([500, 440])
    ;(controller as unknown as { _following: boolean })._following = false

    controller.beginBlock('ls', '~', 0, 1)

    expect(frames).toEqual([])
    controller.blockManager.clearAll()
  })
})

describe('the frozen block metric is published from the renderer (nocx-yy9g)', () => {
  /** A renderer whose cell width the test controls and whose cell-dims
   *  notification the test can fire. */
  function metricRenderer() {
    let cellWidth = 8.5
    let onChange: (() => void) | null = null
    const renderer = makeRenderer() as TerminalRenderer & {
      cellWidth: number
      onCellDimsChange: (cb: () => void) => void
      _setCellWidth: (w: number) => void
      _fireCellDimsChange: () => void
    }
    renderer.cellWidth = cellWidth
    renderer.onCellDimsChange = (cb) => {
      onChange = cb
    }
    renderer._setCellWidth = (w) => {
      cellWidth = w
      renderer.cellWidth = w
    }
    renderer._fireCellDimsChange = () => onChange?.()
    return renderer
  }

  /** The publisher's probe measures its text as 64 W's at 10px = 640px,
   *  so the natural advance is 10 — a stand-in for the real layout the
   *  browser computes. */
  function stubProbeMeasurement(container: HTMLElement): void {
    const probe = container.querySelector<HTMLElement>('.cell-metric-probe')
    expect(probe).not.toBeNull()
    Object.defineProperty(probe!, 'getBoundingClientRect', {
      value: () => ({ width: 640, height: 16 }),
      configurable: true,
    })
  }

  it('publishes the renderer cell width onto the scrollback at construction', () => {
    const pane = document.createElement('div')
    const renderer = metricRenderer()
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    stubProbeMeasurement(controller.scrollbackInner)
    // The constructor publish ran before the probe was measurable, so the
    // properties land on the first refresh — same path the mount-end
    // notification takes in the app.
    renderer._fireCellDimsChange()
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-width')).toBe('8.5px')
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-delta')).toBe('-1.5px')
  })

  it('re-publishes when the renderer reports its cell dims changed (resize, dpr)', () => {
    const pane = document.createElement('div')
    const renderer = metricRenderer()
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    stubProbeMeasurement(controller.scrollbackInner)

    renderer._setCellWidth(9)
    renderer._fireCellDimsChange()

    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-width')).toBe('9px')
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-delta')).toBe('-1px')
  })

  it('publishes nothing while the renderer cannot measure — blocks keep their natural advance', () => {
    const pane = document.createElement('div')
    const renderer = metricRenderer()
    renderer._setCellWidth(0)
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    renderer._fireCellDimsChange()
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-width')).toBe('')
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-delta')).toBe('')
  })
})
describe('the running live region follows its written rows', () => {
  const protoScrollTo = Element.prototype.scrollTo?.bind(Element.prototype)
  beforeEach(() => {
    Element.prototype.scrollTo = () => {}
  })
  afterEach(() => {
    Element.prototype.scrollTo = protoScrollTo
  })

  it('reaches the row a running command parked its caret on, and stops at the freeze (nocx-hg0dg)', () => {
    // omp's composer: the caret sits on a row that holds nothing until you
    // type, so the written-rows measurement stops one row above it and the
    // person cannot see their own keystrokes.
    //
    // The row a SHELL moves to after a newline looks identical and must NOT
    // be reserved — it is the next prompt's, the frozen block ends above it,
    // and counting it drops the pane by a row at every freeze (nocx-i4h04.1).
    // Ownership is what tells them apart: the logical freeze frees the
    // running slot before the rows are serialized, so the block's existence
    // is the boundary, and this asserts both sides of it.
    const renderer = makeRenderer()
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(40)
    ;(renderer.liveCursorBottom as LiveContentHeightSpy).mockReturnValue(60)
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })

    // No command owns the screen: the caret's row is the next prompt's.
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.liveBottomEdge()).toBe(40)

    // A command owns it: the caret's row is where a person is typing.
    controller.beginBlock('omp', '~', 0)
    expect(controller.blockManager.runningBlock).not.toBeNull()
    expect(controller.liveBottomEdge()).toBe(60)

    // A grid nobody has written to is still no rows at all — the caret in
    // the corner must not open the region between the keypress and the first
    // byte of output.
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(0)
    expect(controller.liveBottomEdge()).toBe(0)
  })

  it('sizes the flow box and clip together as output grows', () => {
    const renderer = makeRenderer()
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(19)
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })

    controller.beginBlock('printf slow', '~', 0, 0)
    expect(controller.xtermLiveContainer.style.height).toBe('19px')
    expect(controller.xtermLiveViewport.style.height).toBe('19px')

    controller.setLiveHeight(38)
    expect(controller.xtermLiveContainer.style.height).toBe('38px')
    expect(controller.xtermLiveViewport.style.height).toBe('38px')

    controller.setLiveHeight(57)
    expect(controller.xtermLiveContainer.style.height).toBe('57px')
    expect(controller.xtermLiveViewport.style.height).toBe('57px')
  })

  it('keeps the CSS fallback visible while the empty command waits', () => {
    // Zero is a grid nobody has written to yet. The running command's
    // stand-in is already mounted, so collapsing the live box to body padding
    // would clip the dots at the bottom of the pane.
    const renderer = makeRenderer()
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(null)
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })

    controller.beginBlock('ls', '~', 0, 1)

    expect(controller.xtermLiveContainer.style.height).toBe('')
    expect(controller.xtermLiveViewport.style.height).toBe('')
    expect(controller.mode).toBe('running')
  })

  it('opens at nothing but the body padding for a command that has printed nothing yet', () => {
    const renderer = makeRenderer()
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(0)
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    controller.xtermLiveContainer.style.paddingTop = '2px'
    controller.xtermLiveContainer.style.paddingBottom = '6px'
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })

    controller.beginBlock('ls', '~', 0, 1)

    // The waiting stand-in keeps the CSS fallback box until output retires it.
    expect(controller.xtermLiveContainer.style.height).toBe('')
    expect(controller.xtermLiveViewport.style.height).toBe('')
    expect(controller.xtermLiveContainer.querySelector('.cmd-answer-typing')).not.toBeNull()
    pane.remove()
  })
})

describe('follow intent survives block geometry changes (nocx-n5q44)', () => {
  let reportFollow: IntersectionObserverCallback | null = null

  beforeEach(() => {
    reportFollow = null
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(callback: IntersectionObserverCallback) {
          reportFollow = callback
        }

        observe(): void {}

        disconnect(): void {}

        unobserve(): void {}

        takeRecords(): IntersectionObserverEntry[] {
          return []
        }
      },
    )
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function observerReports(intersecting: boolean): void {
    if (reportFollow === null) throw new Error('follow observer was not constructed')
    reportFollow(
      [{ isIntersecting: intersecting } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    )
  }

  interface TailGeometry {
    setScrollHeight: (height: number) => void
    scrollTo: Mock
  }

  function atTail(controller: ScrollbackController): TailGeometry {
    let scrollHeight = 1000
    let scrollTop = 600
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      configurable: true,
      value: 400,
    })
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(controller.scrollbackArea, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
      },
    })
    const scrollTo = vi.fn((options: { top: number }) => {
      scrollTop = options.top
    })
    Object.defineProperty(controller.scrollbackArea, 'scrollTo', {
      configurable: true,
      value: scrollTo,
    })
    return {
      setScrollHeight: (height) => {
        scrollHeight = height
      },
      scrollTo,
    }
  }

  it('keeps the tail visible when a tool-call block is inserted', () => {
    const { controller } = makeController()
    const geometry = atTail(controller)
    observerReports(false)

    controller.settleAround(() => {
      controller.blockManager.startBlock('tool command', '~', 0)
      observerReports(false)
      geometry.setScrollHeight(1400)
    })

    expect(geometry.scrollTo).toHaveBeenCalledWith({ top: 1400, behavior: 'instant' })
  })

  it('keeps the tail visible when a running block is replaced', () => {
    const { controller } = makeController()
    const geometry = atTail(controller)
    observerReports(true)
    controller.blockManager.startBlock('tool command', '~', 0)
    controller.setRunning()
    geometry.scrollTo.mockClear()

    geometry.setScrollHeight(1400)
    controller.onCommandEnd(
      () => {
        observerReports(false)
        return new BufferLine('tool output')
      },
      2,
      0,
    )

    expect(geometry.scrollTo).toHaveBeenCalledWith({ top: 1400, behavior: 'instant' })
  })
})

// ── The pet hears about the command (nocx-q4qeh.1) ────────────────────────
//
// This exists because the first wiring was hung on `onCommandEnd`, which
// reads like the obvious seam and has no caller at all — so the reaction
// fired never, and both the unit tests of the pet and a look at the running
// app agreed it worked. The check is therefore about the SEAM, not about the
// animal: every freeze path settles through `_settleFrozen`, and the pet must
// be told there.
describe('ScrollbackController tells the pet how the command went', () => {
  const FENCE = 'ab'.repeat(16)
  const domain = mintDomain({
    lane: 'l',
    lifecycle: 'prompt_ready',
    domain: 'd-pet',
    epoch: 1,
  }) as IntegrationDomain

  const protoScrollIntoView = Element.prototype.scrollIntoView?.bind(Element.prototype)
  beforeEach(() => {
    Element.prototype.scrollIntoView = () => {}
  })
  afterEach(() => {
    Element.prototype.scrollIntoView = protoScrollIntoView
    unmountWindowPet()
    vi.restoreAllMocks()
  })

  function petController(): {
    controller: ScrollbackController
    sight: (ev: RenderFenceEvent) => void
    heard: ReturnType<typeof vi.spyOn>
  } {
    const renderer = makeRenderer()
    let fenceCb: ((ev: RenderFenceEvent) => void) | null = null
    renderer.onRenderFence = (cb: (ev: RenderFenceEvent) => void) => {
      fenceCb = cb
    }
    // Spied on the prototype: the overlay is built inside the controller, so
    // there is no instance to hand a double to — which is the point. A double
    // injected from the test would have passed against the dead method too.
    const heard = vi.spyOn(PetOverlay.prototype, 'reactTo').mockImplementation(() => {})
    mountWindowPet(document.createElement('div'))
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    return { controller, sight: (ev) => fenceCb?.(ev), heard }
  }

  function finish(
    controller: ScrollbackController,
    sight: (ev: RenderFenceEvent) => void,
    exitCode: number,
    author: 'shell' | 'agent' = 'shell',
  ): void {
    controller.beginBlock('ls', '~', 0, undefined, author)
    controller.blockManager.bindAttempt('att-1')
    sight({ hex: FENCE, line: 2, buffer: 'normal' })
    controller.freezeFromAttempt(
      { id: 'att-1', domain, state: 'completed', exitCode, fence: FENCE },
      2,
    )
  }

  it('says success when the command succeeded', () => {
    const { controller, sight, heard } = petController()
    finish(controller, sight, 0)
    expect(heard).toHaveBeenCalledWith('success', 'shell')
  })

  it('says failure when it did not', () => {
    const { controller, sight, heard } = petController()
    finish(controller, sight, 2)
    expect(heard).toHaveBeenCalledWith('failure', 'shell')
  })

  it('says unknown for a block that is neither — an environment entry', () => {
    // 'entered' is not a verdict on the command, and a pet that read it as
    // failure would sulk at every ssh.
    const { controller, heard } = petController()
    controller.beginBlock('ssh prod', '~', 0)
    controller.enterBlock(1)
    expect(heard).toHaveBeenCalledWith('unknown', 'shell')
  })

  it('says WHOSE command it was, rather than leaving the pet to guess', () => {
    // The author is minted at submit and carried on the block. Deriving which
    // lane a finished block came from is exactly what the ledger exists to
    // make unnecessary.
    const { controller, sight, heard } = petController()
    finish(controller, sight, 0, 'agent')
    expect(heard).toHaveBeenCalledWith('success', 'agent')
  })

  it('says nothing at all in a window with no pet', () => {
    unmountWindowPet()
    const heard = vi.spyOn(PetOverlay.prototype, 'reactTo').mockImplementation(() => {})
    const renderer = makeRenderer()
    let fenceCb: ((ev: RenderFenceEvent) => void) | null = null
    renderer.onRenderFence = (cb: (ev: RenderFenceEvent) => void) => {
      fenceCb = cb
    }
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      renderer,
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    finish(controller, (ev) => fenceCb?.(ev), 0)
    expect(heard).not.toHaveBeenCalled()
  })
})

// ── The pet hears about a command STARTING too (nocx-q4qeh.1) ─────────────
//
// Endings were all it ever learned about, so during the minute a build takes
// — the minute somebody is actually watching the terminal — it wandered
// about as though nothing were happening.
describe('ScrollbackController tells the pet a command has started', () => {
  const protoScrollIntoView = Element.prototype.scrollIntoView?.bind(Element.prototype)
  beforeEach(() => {
    Element.prototype.scrollIntoView = () => {}
  })
  afterEach(() => {
    Element.prototype.scrollIntoView = protoScrollIntoView
    unmountWindowPet()
    vi.restoreAllMocks()
  })

  function petController(): {
    controller: ScrollbackController
    heard: ReturnType<typeof vi.spyOn>
  } {
    const heard = vi.spyOn(PetOverlay.prototype, 'attendTo').mockImplementation(() => {})
    mountWindowPet(document.createElement('div'))
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      renderer: makeRenderer(),
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    return { controller, heard }
  }

  it('tells it when a command of yours begins', () => {
    const { controller, heard } = petController()
    controller.beginBlockNow('go build ./...', '~', 0)
    expect(heard).toHaveBeenCalledWith('shell')
  })

  it('tells it whose lane the command is in', () => {
    const { controller, heard } = petController()
    controller.beginBlockNow('go test ./...', '~', 0, undefined, 'agent')
    expect(heard).toHaveBeenCalledWith('agent')
  })

  it('says nothing at all in a window with no pet', () => {
    unmountWindowPet()
    const heard = vi.spyOn(PetOverlay.prototype, 'attendTo').mockImplementation(() => {})
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      renderer: makeRenderer(),
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    controller.beginBlockNow('ls', '~', 0)
    expect(heard).not.toHaveBeenCalled()
  })
})
