// @vitest-environment jsdom
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type Mock,
  type MockInstance,
} from 'vitest'
import { headerFitsPinned, ScrollbackController } from './controller'
import { LIVE_REGION_SELECTOR, insideLiveRegion } from '../live-region'
import { CommandSnapshotStore } from '../command-snapshot'
import type { LiveContentHeightSpy } from '../test-support/panes-fixtures'
import type { ExecutionAttempt } from '../lifecycle/state'
import { mintDomain, type IntegrationDomain } from '../lifecycle/domains'
import { PetOverlay } from '../pets/overlay'
import { mountWindowPet, unmountWindowPet } from '../pets/window-pet'
import { closeRunningBlock } from '../test-support/block-close'

function makeRenderer() {
  let cellWidth = 0
  let cellHeight = 0
  const liveContentHeight = vi.fn(() => 0)
  return {
    liveContentHeight,
    get cellWidth() {
      return cellWidth
    },
    get cellHeight() {
      return cellHeight
    },
    setCellWidth(value: number) {
      cellWidth = value
    },
    setCellHeight(value: number) {
      cellHeight = value
    },
  }
}

function makeController() {
  const pane = document.createElement('div')
  const controller = new ScrollbackController({
    pane,
    liveContentHeight: () => null,
    // A cell metric, because the live cap is a row boundary: without one the
    // ceiling cannot be floored and stays the pane's raw height.
    cellMetrics: () => ({ cellWidth: 8, cellHeight: 16 }),
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

/** What the follow sentinel delivers, through the port the controller listens
 *  on. A NEGATIVE delivery counts only for a scroller that can be measured
 *  (scrollback/tail-follow.ts), and jsdom lays nothing out — so name the
 *  geometry the claim needs before making it. */
function stopFollowing(controller: ScrollbackController): void {
  const area = controller.scrollbackArea
  if (area.clientHeight === 0) {
    Object.defineProperty(area, 'clientHeight', { configurable: true, value: 600 })
  }
  if (area.scrollHeight === 0) {
    Object.defineProperty(area, 'scrollHeight', { configurable: true, value: 4000 })
  }
  tailOf(controller).observe(area, false)
}

function tailOf(controller: ScrollbackController): {
  observe: (el: Element, following: boolean) => void
} {
  return (
    controller as unknown as {
      _tail: { observe: (el: Element, following: boolean) => void }
    }
  )._tail
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
    stopFollowing(controller)
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

    controller.onClearBoundary(null)

    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(0)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(0)
  })

  it('and a frozen block goes too — the freeze swaps the element the manager owns', () => {
    const { controller } = makeRestoredController()
    controller.restorePast([restored('old')])
    controller.blockManager.startBlock('ls', '~', 0)
    closeRunningBlock(controller.blockManager)
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(2)

    controller.onClearBoundary(null)

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
    closeRunningBlock(controller.blockManager)
    controller.blockManager.startBlock('clear', '~', 0)

    controller.onClearBoundary(null)

    expect(controller.blockManager.blocks.length).toBe(0)
    expect(controller.blockManager.runningBlock).toBeNull()
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(0)
    // The chrome the manager anchors on is NOT the manager's to remove.
    expect(controller.scrollbackInner.querySelector('.terminal-live-container')).not.toBeNull()
    expect(controller.scrollbackInner.querySelector('.scrollback-separator')).not.toBeNull()
  })

  it('a restore into a pane that has already been cleared still draws the past', () => {
    const { controller } = makeRestoredController()
    controller.blockManager.startBlock('clear', '~', 0)
    controller.onClearBoundary(null)

    controller.restorePast([restored('oldest'), restored('newest')])

    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(2)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).not.toBeNull()
    // And a second clear takes that past too — the registration is not a
    // one-shot that only the first restore gets.
    controller.onClearBoundary(null)
    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(0)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
  })
})

// ── the backend names the boundary, not the client (nocx-2v80t.3.17) ──────
// The removed maybeClear matched the typed command's text at submit time,
// before the command had even run. onClearBoundary is the backend's own
// rendezvous with a real VT erase it parsed, and it names WHICH block the
// erase happened inside — the running command's own block must survive
// its own report of the clear, or the block reporting `clear` finished
// would vanish out from under the stream still delivering its rows.
describe('onClearBoundary keeps the block reporting the clear (nocx-2v80t.3.17)', () => {
  it('keeps the running command bound to keepEntryId and removes every other block', () => {
    const { controller } = makeController()
    controller.blockManager.startBlock('make watch', '~', 0)
    closeRunningBlock(controller.blockManager)
    const running = controller.blockManager.startBlock('clear', '~', 0)
    controller.blockManager.bindAttempt('attempt-clear-1')

    controller.onClearBoundary('attempt-clear-1')

    expect(controller.blockManager.blocks.length).toBe(1)
    expect(controller.blockManager.runningBlock?.el).toBe(running.el)
    expect(controller.blockManager.runningBlock?.attemptId).toBe('attempt-clear-1')
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(1)
    expect(controller.scrollbackInner.contains(running.el)).toBe(true)
  })

  it('keeps the restored past out of it too — only the named entry survives', () => {
    const { controller } = makeController()
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      value: 4000,
      configurable: true,
    })
    const restoredEl = document.createElement('div')
    restoredEl.className = 'cmd-block'
    restoredEl.dataset.restored = 'true'
    controller.restorePast([restoredEl])
    const running = controller.blockManager.startBlock('clear', '~', 0)
    controller.blockManager.bindAttempt('attempt-clear-2')

    controller.onClearBoundary('attempt-clear-2')

    expect(controller.scrollbackInner.querySelectorAll('[data-restored]').length).toBe(0)
    expect(controller.scrollbackInner.querySelector('.scrollback-restore-boundary')).toBeNull()
    expect(controller.scrollbackInner.contains(running.el)).toBe(true)
  })

  it('a keepEntryId this pane does not own falls back to clearing everything', () => {
    const { controller } = makeController()
    controller.blockManager.startBlock('ls', '~', 0)
    closeRunningBlock(controller.blockManager)

    controller.onClearBoundary('some-other-panes-attempt')

    expect(controller.blockManager.blocks.length).toBe(0)
    expect(controller.scrollbackInner.querySelectorAll('.cmd-block').length).toBe(0)
  })
})

describe('ScrollbackController unstructured mode', () => {
  it('fills the pane for a markerless session (plain SSH before any OSC 133)', () => {
    const { controller } = makeController()
    expect(controller.mode).toBe('idle')

    controller.setUnstructured()

    expect(controller.mode).toBe('unstructured')
    expect(controller.terminalLiveContainer.className).toContain('live-unstructured')
    expect(controller.terminalLiveContainer.style.height).toBe('352px')
  })

  it('lets the first OSC-133 marker transition back to the normal layout', () => {
    const { controller } = makeController()
    controller.setUnstructured()

    // The marker arrives: PROMPT_READY collapses the live region to idle.
    controller.setIdle()

    expect(controller.mode).toBe('idle')
    expect(controller.terminalLiveContainer.className).toContain('live-idle')
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
    expect(controller.terminalLiveContainer.className).toContain('live-unstructured')
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
    expect(controller.terminalLiveContainer.style.height).toBe('352px')

    // The card is dismissed and the pane gives the pixels back.
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 540,
      configurable: true,
    })
    controller.setLiveHeight(null)

    // Floored to whole rows, like every filled pane box (see runningLiveCap):
    // 540px is 33.75 rows of a 16px cell.
    expect(controller.terminalLiveContainer.style.height).toBe('528px')
  })

  it('re-measures an alt-screen program when the scroller shrinks', () => {
    const { controller } = makeController()
    controller.enterFullscreen()
    // ONE ROW LESS THAN THE PANE (ADR-2yzsq8mk): 352px of pane is 22 rows of a
    // 16px cell, and the program's box gives one of them back to the stylesheet's
    // bottom margin so its last row never touches the window's edge.
    expect(controller.terminalLiveContainer.style.height).toBe('336px')

    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 200,
      configurable: true,
    })
    controller.setLiveHeight(null)

    // 200px is 12.5 rows of 16: floored to 12, less the margin row.
    expect(controller.terminalLiveContainer.style.height).toBe('176px')
  })

  it('the region answers the shared live-region identity, in every mode (ADR-0091)', () => {
    // The identity is asked by the block layer, the painter and the pane
    // (live-region.ts), and the element is built here. One literal, two
    // modules: this is the assertion that keeps them one answer rather than a
    // comment asking a reader to check.
    const { controller } = makeController()
    for (const mode of ['setIdle', 'setRunning', 'setUnstructured', 'enterFullscreen'] as const) {
      controller[mode]()
      expect(controller.terminalLiveContainer.matches(LIVE_REGION_SELECTOR)).toBe(true)
      expect(insideLiveRegion(controller.terminalLiveContainer)).toBe(true)
    }
  })

  it('leaves an idle pane alone: there is no live box to fill', () => {
    const { controller } = makeController()
    controller.setIdle()
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 540,
      configurable: true,
    })
    controller.setLiveHeight(null)

    expect(controller.terminalLiveContainer.style.height).toBe('')
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

  function rendererWithFence(): { renderer: ReturnType<typeof makeRenderer> } {
    return { renderer: makeRenderer() }
  }
  it("flips the status on the completion event and closes the block on the backend's block.closed (nocx-2v80t.3.27)", () => {
    const { renderer } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
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
    // PromptReady may arrive before block.closed. It must not collapse the
    // live rows into empty space while the block's closing rows are not in
    // it yet.
    controller.setIdle()
    expect(controller.mode).toBe('running')

    // The backend says the block is closed: it closes on screen, and the
    // idle layout the prompt asked for lands now that nothing waits.
    controller.blockManager.blockClosed('att-1')
    const block = controller.blockManager.blockForAttempt('att-1')
    expect(block?.status).toBe('success')
    expect(block?.el.classList.contains('cmd-block-running')).toBe(false)
    expect(controller.mode).toBe('idle')
  })

  it('re-judges a header from its own box, with no scroll and no resize (nocx-4y82c.1.3)', async () => {
    // ONE MECHANISM: the size observer watches each ledger-level header and
    // the scroller. A header whose own box changes is judged from that change
    // alone -- a freeze swapping the element in, a font re-wrapping it, a
    // newly compact one -- so there is no per-trigger path to miss.
    class FakeSizeObserver {
      static latest: FakeSizeObserver | null = null
      readonly observed = new Set<Element>()
      private readonly cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
        FakeSizeObserver.latest = this
      }
      observe(el: Element): void {
        this.observed.add(el)
      }
      unobserve(el: Element): void {
        this.observed.delete(el)
      }
      disconnect(): void {
        this.observed.clear()
      }
      deliver(): void {
        this.cb([], this)
      }
    }
    vi.stubGlobal('ResizeObserver', FakeSizeObserver)
    const pane = document.createElement('div')
    try {
      const { renderer } = rendererWithFence()
      // The judge skips a header that is not in the document; a real pane is.
      document.body.appendChild(pane)
      const controller = new ScrollbackController({
        pane,
        liveContentHeight: () => renderer.liveContentHeight(),
        cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
        snapshotStore: new CommandSnapshotStore(),
      })
      controller.scrollbackArea.scrollTo = vi.fn()
      Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
        value: 300,
        configurable: true,
      })

      // A running block opens, then block.closed swaps in the frozen one.
      controller.blockManager.startBlock('make', '~', 0)
      controller.blockManager.bindAttempt('att-1')
      controller.setRunning()
      expect(controller.freezeFromAttempt(completedAttempt(FENCE), 2)).toBe(false)
      controller.blockManager.blockClosed('att-1')
      // The stack's childList changed: the mutation observer registers the
      // header that replaced the running one, and the size observer judges it.
      await new Promise((resolve) => setTimeout(resolve, 0))
      const block = controller.blockManager.blockForAttempt('att-1')
      const header = block!.el.querySelector<HTMLElement>(':scope > .cmd-header')!
      expect(FakeSizeObserver.latest!.observed.has(header), 'the new header is watched').toBe(true)
      Object.defineProperty(header, 'offsetHeight', { value: 400, configurable: true })
      FakeSizeObserver.latest!.deliver()
      expect(header.getAttribute('data-pinnable'), 'a third of 300 cannot hold 400').toBe('false')

      // AND THE OTHER DIRECTION: a header that becomes compact is pinnable
      // again without a scroll, a resize or a freeze.
      Object.defineProperty(header, 'offsetHeight', { value: 80, configurable: true })
      FakeSizeObserver.latest!.deliver()
      expect(header.getAttribute('data-pinnable')).toBeNull()
    } finally {
      vi.unstubAllGlobals()
      pane.remove()
    }
  })

  it('a command that ends while a full-screen program owns the pane still closes on block.closed', () => {
    const { renderer } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    // jsdom implements no scrollTo; setRunning scrolls to the live end.
    controller.scrollbackArea.scrollTo = vi.fn()
    controller.blockManager.startBlock('vim', '~', 0)
    controller.blockManager.bindAttempt('att-1')
    controller.setRunning()
    controller.enterFullscreen()
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 2)).toBe(false)

    controller.blockManager.blockClosed('att-1')
    const block = controller.blockManager.blockForAttempt('att-1')
    expect(block?.status).toBe('success')
    expect(block?.el.classList.contains('cmd-block-running')).toBe(false)
    controller.blockManager.clearAll()
  })
  it('a second shell-originated attempt opens its own block while the first is pending its close — no merge (nocx-m87n)', () => {
    const { renderer } = rendererWithFence()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
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
    controller.beginBlock('codex', '~', 0)
    controller.blockManager.bindAttempt('att-2')
    expect(controller.blockManager.blocks).toHaveLength(2)
    expect(controller.blockManager.runningBlock?.status).toBe('running')
    expect(controller.blockManager.blockForAttempt('att-2')?.status).toBe('running')

    // The first block's block.closed lands: it closes with its own exit
    // status — while the second command is still running.
    controller.blockManager.blockClosed('att-1')
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

describe('the echoed command line leaves the live region too (nocx-w1n4)', () => {
  // The frozen body already skips the echo: the app-owned submit opens the
  // block BEFORE the bytes, the shell's echo lands on the creation line,
  // and the output range starts one row later (nocx-4yhi). The LIVE region
  // is the painted grid itself, which still holds that echoed line on the
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

  function rendererWithGeometry() {
    const renderer = makeRenderer()
    renderer.setCellHeight(16)
    return renderer
  }

  it('releases the shift only when an accepted backend row no longer contains the echo', () => {
    const renderer = rendererWithGeometry()
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.beginBlock('top', '~', 0)
    const record = controller.blockManager.runningBlock!
    record.outputAnchorOffset = 1
    record.outputAnchorText = 'prompt top'
    controller.outputAnchorChanged()
    expect(controller.terminalLiveInner.style.transform).toBe('translateY(-16px)')
    record.outputAnchorText = 'top - 19:20:15 up 18 days,  1:53,  2 users'
    controller.outputAnchorChanged()
    expect(controller.terminalLiveInner.style.transform).toBe('')
  })

  it('hides the backend-anchored echo row and sizes the live region without renderer line numbers', () => {
    const renderer = rendererWithGeometry()

    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()

    // The accepted backend anchor says the echoed command is one row below
    // the current base row. No renderer viewport line is consulted.
    controller.beginBlock('ls', '~', 0)
    const record = controller.blockManager.runningBlock!
    record.outputAnchorOffset = 1
    record.outputAnchorText = 'prompt ls'
    controller.outputAnchorChanged()
    expect(controller.mode).toBe('running')
    expect(controller.terminalLiveInner.style.transform).toBe('translateY(-16px)')

    // Output arrives: the flow box and clip reserve only the two shown rows;
    // the translated echo row occupies neither visible space nor layout.
    controller.setLiveHeight(3 * 16)
    expect(controller.terminalLiveContainer.style.height).toBe('32px')
    expect(controller.terminalLiveViewport.style.height).toBe('32px')
    expect(controller.terminalLiveInner.style.transform).toBe('translateY(-16px)')

    // A measurement no taller than the translated echo must clear, rather
    // than retain, stale flow space from a previous chunk.
    controller.setLiveHeight(8)
    expect(controller.terminalLiveContainer.style.height).toBe('0px')
    expect(controller.terminalLiveViewport.style.height).toBe('0px')

    // A backend frame can move the base row past the anchor: its new offset is
    // zero, so the ECHO no longer needs hiding. The grid is still shifted here
    // — by what the cap hides, not by the anchor — because 384px of content
    // against a 320px box means the box shows the last 320px of it. Deriving
    // the translation from the box is what keeps a row still when the cap
    // clamps (owner, 2026-11-08); an anchor-only shift moved every row down a
    // line in exactly that frame.
    record.outputAnchorOffset = 0
    controller.outputAnchorChanged()
    controller.setLiveHeight(24 * 16)
    expect(controller.terminalLiveInner.style.transform).toBe('translateY(-64px)')
    // Both the content clip and flow box clamp to the live-region cap.
    expect(controller.terminalLiveViewport.style.height).toBe('320px')
    expect(controller.terminalLiveContainer.style.height).toBe('320px')

    // A later matching accepted frame may place the anchor back in view.
    record.outputAnchorOffset = 1
    controller.outputAnchorChanged()
    controller.setLiveHeight(3 * 16)
    expect(controller.terminalLiveInner.style.transform).toBe('translateY(-16px)')
    expect(controller.terminalLiveViewport.style.height).toBe('32px')
    expect(controller.terminalLiveContainer.style.height).toBe('32px')

    // The freeze paints the verdict and the live region STAYS UP — the
    // whole terminal is the session's surface now, and nothing clears it
    // (nocx-2v80t.3.3). With no running block the echo shift is moot, so
    // the grid sits untransformed beneath the frozen card.
    controller.blockManager.bindAttempt('att-1')
    controller.blockManager.blockClosed('att-1')
    expect(controller.freezeFromAttempt(completedAttempt(FENCE), 3)).toBe(true)
    expect(controller.mode).toBe('running')
    expect(controller.terminalLiveInner.style.transform).toBe('')
    controller.blockManager.clearAll()
  })

  it('keeps active rows edge-to-edge with no extra body space, while excluding the echoed row', () => {
    const renderer = rendererWithGeometry()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()

    controller.beginBlock('ls', '~', 0)
    controller.blockManager.runningBlock!.outputAnchorOffset = 1
    controller.blockManager.runningBlock!.outputAnchorText = 'prompt ls'
    controller.outputAnchorChanged()
    controller.setLiveHeight(3 * 16)
    expect(controller.terminalLiveViewport.style.height).toBe('32px')
    expect(controller.terminalLiveContainer.style.height).toBe('32px')

    // Full capacity is the pane less the one header, FLOORED TO WHOLE ROWS:
    // 360 - 32 = 328px is 20.5 rows of a 16px cell, and a ceiling between two
    // rows leaves the clip slicing the twenty-first (owner, 2026-11-08: "the
    // live grid is cut short mid-row"). 320 is twenty rows. The live box and
    // clip have the same size; neither reserves frozen-output padding or air.
    controller.setLiveHeight(100 * 16)
    expect(controller.runningLiveCap).toBe(320)
    expect(controller.terminalLiveContainer.style.height).toBe('320px')
    expect(controller.terminalLiveViewport.style.height).toBe('320px')
    controller.blockManager.clearAll()
    pane.remove()
  })

  it('uses the kit header token with a 32px fallback, independent of header text or DOM seat', () => {
    const { controller } = makeController()
    document.body.appendChild(controller.scrollbackArea)
    try {
      expect(controller.runningHeaderHeightPx).toBe(32)
      controller.beginBlock('a very long command '.repeat(30), '~', 0)
      // 328px of pane is 20.5 rows; the cap is a row boundary (see the full
      // capacity case above).
      expect(controller.runningLiveCap).toBe(320)
      const header =
        controller.blockManager.runningBlock!.el.querySelector<HTMLElement>('.cmd-header')!
      header.getBoundingClientRect = () => ({ height: 120 }) as DOMRect
      expect(controller.runningLiveCap).toBe(320)
      controller.scrollbackArea.style.setProperty('--control-height-md', '40px')
      expect(controller.runningHeaderHeightPx).toBe(40)
      expect(controller.runningLiveCap).toBe(320)
      controller.scrollbackArea.style.setProperty('--control-height-md', 'invalid')
      expect(controller.runningHeaderHeightPx).toBe(32)
      expect(controller.runningLiveCap).toBe(320)
      Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
        value: 460,
        configurable: true,
      })
      // 460 - 32 = 428px, floored to the same roomless boundary: 26 rows.
      expect(controller.runningLiveCap).toBe(416)
    } finally {
      controller.blockManager.clearAll()
      controller.scrollbackArea.remove()
    }
  })

  it('keeps the header above the live clip: only the shifted echo row, never a visible row, lies under it', () => {
    // nocx-atpw5.1.18. The 32px running header is layout space before the live
    // container, not an overlay on it; the echo shift is what puts the shell's
    // echoed command above the clip. So the row whose centre falls inside the
    // header is the echo row — a row the region does not show — and the first
    // row a person can see and click starts at the container's top, below the
    // header. jsdom computes no layout, so this pins the block order and the
    // controller's own numbers (the same seam the tests above use).
    const renderer = rendererWithGeometry()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    try {
      controller.beginBlock('cat', '~', 0)
      const block = controller.blockManager.runningBlock!.el
      const header = block.querySelector<HTMLElement>(':scope > .cmd-header')!
      expect(header).not.toBeNull()
      expect(controller.terminalLiveContainer.parentElement).toBe(block)
      expect(block.lastElementChild).toBe(controller.terminalLiveContainer)
      expect(Array.from(block.children).indexOf(header)).toBeLessThan(
        Array.from(block.children).indexOf(controller.terminalLiveContainer),
      )
      expect(controller.runningHeaderHeightPx).toBe(32)

      // Anchor the output one row below the echoed command: the grid moves up
      // by that row. Row 0 (the echo) then ends at the container's top, so its
      // centre is 8px above the clip — inside the header's 32px box — while
      // row 1, the printed output, starts at the top of the clip.
      controller.blockManager.runningBlock!.outputAnchorOffset = 1
      controller.outputAnchorChanged()
      controller.setLiveHeight(3 * 16)
      expect(controller.terminalLiveInner.style.transform).toBe('translateY(-16px)')
      expect(controller.terminalLiveViewport.style.height).toBe('32px')
      expect(controller.terminalLiveContainer.style.height).toBe('32px')
    } finally {
      controller.blockManager.clearAll()
      pane.remove()
    }
  })

  it('reserves the same header and full live capacity before and after an alternate round trip', () => {
    const { controller } = makeController()
    controller.beginBlock('top', '~', 0)
    controller.setLiveHeight(100 * 16)
    const block = controller.blockManager.runningBlock!
    const header = block.el.querySelector('.cmd-header')!
    expect(controller.terminalLiveContainer.style.height).toBe('320px')
    controller.enterFullscreen()
    // The full-screen box is the pane less the one row of bottom margin
    // (ADR-2yzsq8mk): same header, same capacity, one row given back to it.
    expect(controller.terminalLiveContainer.style.height).toBe('304px')
    expect(controller.scrollbackArea.contains(header)).toBe(true)
    controller.exitFullscreen()
    controller.setRunning()
    controller.setLiveHeight(100 * 16)
    expect(controller.terminalLiveContainer.style.height).toBe('320px')
    expect(controller.blockManager.runningBlock).toBe(block)
    expect(block.el.contains(header)).toBe(true)
    controller.blockManager.clearAll()
  })
})

describe('a nested running command borrows the full pane without changing its transcript slot', () => {
  it('cancels ancestor insets only for the active block and freezes one replacement in the same order', () => {
    const { pane, controller } = makeController()
    /* eslint-disable @typescript-eslint/unbound-method */
    const scrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollIntoView = () => {}
    document.body.appendChild(pane)
    controller.scrollbackArea.scrollTo = vi.fn()
    Object.defineProperty(controller.scrollbackArea, 'clientWidth', {
      value: 500,
      configurable: true,
    })
    const manager = controller.blockManager
    const turn = manager.addAnswerBlock('inspect disk space', '/repo')
    turn.el.style.paddingLeft = '24px'
    turn.append('before command', 'nested-before')
    turn.toolCall({
      callId: 'nested-run',
      tool: 'run',
      effect: 'mutate-destructive',
      opensBlock: true,
    })
    controller.beginBlock('df -h', '/repo', 0, 'agent')
    manager.bindAttempt('nested-command')
    const record = manager.runningBlock!
    const running = record.el
    const header = running.querySelector<HTMLElement>(':scope > .cmd-header')!
    const children = turn.el.querySelector<HTMLElement>(':scope > .cmd-children')!
    children.style.paddingLeft = '12px'
    children.style.borderLeft = '1px solid transparent'
    turn.append('after command', 'nested-after')
    const before = children.querySelector<HTMLElement>('[data-entry-id="nested-before"]')!
    const after = children.querySelector<HTMLElement>('[data-entry-id="nested-after"]')!
    before.style.paddingLeft = '7px'
    after.style.paddingLeft = '9px'
    const originalStyles = [turn.el, children, before, after].map((el) => el.style.cssText)
    const originalOrder = Array.from(children.children)
    const slot = originalOrder.indexOf(running)
    expect(slot).toBeGreaterThan(0)
    expect(originalOrder[slot - 1]).toBe(before)
    expect(originalOrder[slot + 1]).toBe(after)

    try {
      controller.setLiveHeight(160)
      expect(running.parentElement).toBe(children)
      expect(running.style.width).toBe('500px')
      expect(running.style.marginLeft).toBe('-37px')
      expect(Array.from(children.children)).toEqual(originalOrder)

      // A buffer switch keeps the actual header DOM node and record alive.
      controller.enterFullscreen()
      expect(record.el).toBe(running)
      expect(running.parentElement).toBe(children)
      expect(controller.scrollbackInner.querySelector(':scope > .cmd-header')).toBe(header)
      expect(
        controller.scrollbackArea.querySelectorAll('[data-entry-id="nested-command"]'),
      ).toHaveLength(1)
      expect([turn.el, children, before, after].map((el) => el.style.cssText)).toEqual(
        originalStyles,
      )

      controller.exitFullscreen()
      controller.setRunning()
      controller.setLiveHeight(160)
      expect(running.querySelector(':scope > .cmd-header')).toBe(header)
      expect(running.parentElement).toBe(children)
      expect(running.style.width).toBe('500px')
      expect(running.style.marginLeft).toBe('-37px')
      expect(Array.from(children.children)).toEqual(originalOrder)

      manager.blockClosed('nested-command')
      expect(
        controller.freezeFromAttempt(
          {
            id: 'nested-command',
            domain: mintDomain({
              lane: 'nested-lane',
              lifecycle: 'prompt_ready',
              domain: 'nested-domain',
              epoch: 1,
            }) as IntegrationDomain,
            state: 'completed',
            exitCode: 0,
            fence: 'f'.repeat(64),
          },
          10,
        ),
      ).toBe(true)
      // Freeze keeps record/backend identity, but replaces the running DOM
      // element with the ordinary indented frozen presentation in its slot.
      const frozen = manager.blockForAttempt('nested-command')!
      expect(frozen).toBe(record)
      expect(frozen.el).not.toBe(running)
      expect(frozen.el.parentElement).toBe(children)
      expect(frozen.el.dataset.entryId).toBe('nested-command')
      expect(frozen.el.style.width).toBe('')
      expect(frozen.el.style.marginLeft).toBe('')
      expect(
        controller.scrollbackArea.querySelectorAll('[data-entry-id="nested-command"]'),
      ).toHaveLength(1)
      expect(Array.from(children.children)).toEqual(
        originalOrder.map((el) => (el === running ? frozen.el : el)),
      )
      expect([turn.el, children, before, after].map((el) => el.style.cssText)).toEqual(
        originalStyles,
      )
      expect(running.isConnected).toBe(false)
      expect(header.isConnected).toBe(false)
    } finally {
      turn.close('success')
      controller.dispose()
      pane.remove()
      Element.prototype.scrollIntoView = scrollIntoView
    }
  })
})

describe('a delivery sizes the box from what was painted (owner, 2026-11-08)', () => {
  /** A running controller whose measured content the test owns, and whose
   *  scroller records where it was asked to go. */
  function runningController(content: { px: number | null }) {
    const renderer = makeRenderer()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => content.px,
      cellMetrics: () => ({ cellWidth: 8, cellHeight: 16 }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(controller.scrollbackArea)
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    controller.beginBlock('ls', '~', 0)
    return { controller, renderer }
  }

  it('does not follow a fresher measurement than the rows on screen', () => {
    // THE ORDER THE BROWSER PRODUCED, driven here by hand: a stored-rows
    // delivery arrives after a frame was accepted and before it is painted, so
    // the model answers with the NEWER frame's extent. The box must keep the
    // height the last paint computed — the only one its rows agree with — and
    // take the new measurement on the next paint, not on the delivery.
    const content = { px: 3 * 16 }
    const { controller } = runningController(content)
    try {
      // The paint that put three rows on screen.
      controller.setLiveHeight(content.px, true)
      expect(controller.terminalLiveContainer.style.height).toBe('48px')

      // The delivery: the model has moved on (two rows of text, say), but no
      // frame has painted them.
      content.px = 2 * 16
      controller.settleAround(() => {})
      expect(controller.terminalLiveContainer.style.height).toBe('48px')

      // The paint that does show them takes the measurement.
      controller.setLiveHeight(content.px, true)
      expect(controller.terminalLiveContainer.style.height).toBe('32px')
    } finally {
      controller.blockManager.clearAll()
      controller.scrollbackArea.remove()
    }
  })

  it('a sizing pass before the paint cannot become the height a delivery trusts', () => {
    // THE BEAD'S FRAME (nocx-4y82c.1.1): scrollTop 156 -> 173, the block
    // -155+831 -> -172+848, the live box 629 both frames and the grid's
    // translateY(-34px) both frames — and a visible row 34px DOWN. Nothing
    // about the live region moved, so the box was sized from a frame the grid
    // had not painted: the per-paint resize pass (`scheduleLiveResize`) wrote
    // the box AND the remembered painted height from the model, and the
    // stored-rows delivery that followed sized from that number. The grid
    // showed the older frame, so every visible row moved down by the two rows
    // the model was ahead.
    const content = { px: 3 * 16 }
    const { controller } = runningController(content)
    try {
      // The paint that put three rows on screen.
      controller.setLiveHeight(content.px, true)
      expect(controller.terminalLiveContainer.style.height).toBe('48px')

      // The per-paint resize pass: the model has a frame two rows on, and no
      // paint has drawn it.
      content.px = 2 * 16
      controller.setLiveHeight(content.px)
      // A pass that did not paint re-applies the painted height.
      expect(controller.terminalLiveContainer.style.height).toBe('48px')

      // The delivery that lands next sizes from the same painted height, not
      // from the number the pass left behind.
      controller.settleAround(() => {})
      expect(controller.terminalLiveContainer.style.height).toBe('48px')

      // The paint that shows the newer frame is the one that moves the box.
      controller.setLiveHeight(content.px, true)
      expect(controller.terminalLiveContainer.style.height).toBe('32px')
    } finally {
      controller.blockManager.clearAll()
      controller.scrollbackArea.remove()
    }
  })

  it('a box change keeps the painted height, not the model at that instant', () => {
    // The scroller's own box watcher is the same door: it fires between a
    // frame's acceptance and its paint (the composer leaving, a pane resize),
    // and it used to size the live box from the model it read at that moment.
    class BoxObserver {
      static latest: BoxObserver | null = null
      readonly disconnect = vi.fn()
      private readonly cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
        BoxObserver.latest = this
      }
      observe(): void {}
      unobserve(): void {}
      deliver(): void {
        this.cb([], {} as ResizeObserver)
      }
    }
    vi.stubGlobal('ResizeObserver', BoxObserver)
    const content = { px: 3 * 16 }
    try {
      const pane = document.createElement('div')
      const controller = new ScrollbackController({
        pane,
        liveContentHeight: () => content.px,
        cellMetrics: () => ({ cellWidth: 8, cellHeight: 16 }),
        snapshotStore: new CommandSnapshotStore(),
      })
      document.body.appendChild(controller.scrollbackArea)
      Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
        value: 360,
        configurable: true,
      })
      Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
        value: 360,
        configurable: true,
      })
      controller.scrollbackArea.scrollTo = vi.fn()
      controller.beginBlock('ls', '~', 0)
      try {
        controller.setLiveHeight(content.px, true)
        expect(controller.terminalLiveContainer.style.height).toBe('48px')
        content.px = 2 * 16
        BoxObserver.latest!.deliver()
        expect(controller.terminalLiveContainer.style.height).toBe('48px')
      } finally {
        controller.blockManager.clearAll()
        controller.scrollbackArea.remove()
      }
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('entering the running layout sizes from the frame the grid already shows', () => {
    // The mode entry is the third door of the same kind. The grid is the
    // painter's last frame — painted while the pane was idle — and the model
    // may already hold a newer one; the box must take the painted height.
    const content = { px: 2 * 16 }
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => content.px,
      cellMetrics: () => ({ cellWidth: 8, cellHeight: 16 }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(controller.scrollbackArea)
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    try {
      // The paint that put three rows on screen, at the prompt.
      controller.setLiveHeight(3 * 16, true)
      // A newer frame the painter has not drawn.
      content.px = 2 * 16
      controller.beginBlock('ls', '~', 0)
      expect(controller.mode).toBe('running')
      expect(controller.terminalLiveContainer.style.height).toBe('48px')
    } finally {
      controller.blockManager.clearAll()
      controller.scrollbackArea.remove()
    }
  })
})

describe('the pane holds the tail and never animates (owner, 2026-11-08)', () => {
  /** A controller whose stack records whether anything asked it to animate. */
  function movingController(): {
    controller: ScrollbackController
    frames: Array<{ el: string; keyframes: unknown }>
  } {
    const renderer = makeRenderer()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 600,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    const frames: Array<{ el: string; keyframes: unknown }> = []
    for (const [name, el] of [
      ['inner', controller.scrollbackInner],
      ['live', controller.terminalLiveContainer],
    ] as const) {
      ;(el as unknown as { animate: unknown }).animate = (keyframes: unknown) => {
        frames.push({ el: name, keyframes })
        return { cancel: () => {}, finished: Promise.resolve() } as unknown as Animation
      }
    }
    return { controller, frames }
  }

  it('opens a block without playing anything back', () => {
    // The owner's decision replaces the FLIP settle this describe block used
    // to prove: "like Warp, no animation. The output is anchored to the
    // bottom, and every frame shows the newest rows where they will stay."
    // Nothing is translated, so nothing asks either moving element to animate,
    // and the settle's state class never appears on the scroller.
    const { controller, frames } = movingController()

    controller.beginBlock('ls', '~', 0)

    expect(frames).toEqual([])
    expect(controller.scrollbackArea.classList.contains('is-settling')).toBe(false)
    controller.blockManager.clearAll()
  })

  it('writes nothing to the scroller it holds, however long the transcript is', async () => {
    // A class write on the scroller restyles and re-lays out every block under
    // it — measured in WebKit at 218 ms for 181 blocks — so holding the tail
    // must be scroll work only. Counted as writes, not timed.
    for (const blocks of [1, 50, 500]) {
      const { controller, frames } = movingController()
      Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
        value: 600 + blocks * 2000,
        configurable: true,
      })
      const written: MutationRecord[] = []
      const writes = new MutationObserver((records) => written.push(...records))
      writes.observe(controller.scrollbackArea, { attributes: true, attributeFilter: ['class'] })

      controller.beginBlock('ls', '~', 0)
      await Promise.resolve()
      await Promise.resolve()

      written.push(...writes.takeRecords())
      expect(frames).toEqual([])
      expect(written).toEqual([])
      expect(controller.scrollbackArea.classList.contains('is-settling')).toBe(false)
      writes.disconnect()
      controller.blockManager.clearAll()
    }
  })

  it('answers "am I following the output" from outside the stack it moves', () => {
    // The sentinel sits OUTSIDE the stack, so nothing the stack does can move
    // the answer to "am I following the output". It used to matter because a
    // settle displaced the stack's box (nocx-i4h04.3) and the pane then read
    // its own displacement as the person scrolling away; the rule outlives
    // the settle and costs nothing.
    const { controller } = movingController()

    expect(controller.followSentinel.parentElement).toBe(controller.scrollbackArea)
    expect(controller.scrollbackInner.contains(controller.followSentinel)).toBe(false)
    expect(controller.scrollbackInner.contains(controller.terminalLiveContainer)).toBe(true)
  })

  it('answers "am I following the output" from outside the stack it moves', () => {
    // The sentinel sits OUTSIDE the stack, so nothing the stack does can move
    // the answer to "am I following the output". It used to matter because a
    // settle displaced the stack's box (nocx-i4h04.3) and the pane then read
    // its own displacement as the person scrolling away; the rule outlives
    // the settle and costs nothing.
    const { controller } = movingController()

    expect(controller.followSentinel.parentElement).toBe(controller.scrollbackArea)
    expect(controller.scrollbackInner.contains(controller.followSentinel)).toBe(false)
    expect(controller.scrollbackInner.contains(controller.terminalLiveContainer)).toBe(true)
  })

  it('moves nothing while the person is reading further up', () => {
    // Movement they did not cause is not theirs: a pane scrolled away from the
    // bottom keeps its position, and the tail is not re-pinned under them.
    const { controller, frames } = movingController()
    stopFollowing(controller)

    controller.beginBlock('ls', '~', 0)

    expect(frames).toEqual([])
    controller.blockManager.clearAll()
  })
})

describe('the frozen block metric follows accepted painter metrics', () => {
  it('publishes the current cell width and row pitch', () => {
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (
      this: HTMLElement,
    ) {
      if (this.classList.contains('cell-metric-probe')) {
        return { width: 64 * 8 } as DOMRect
      }
      return Element.prototype.getBoundingClientRect.call(this)
    })
    const metrics = { cellWidth: 8.5, cellHeight: 16 }
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      liveContentHeight: () => null,
      cellMetrics: () => metrics,
      snapshotStore: new CommandSnapshotStore(),
    })
    // Metrics are published only after the backend accepts a screen geometry.
    controller.cellMetricsChanged()
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-width')).toBe('8.5px')
    expect(controller.scrollbackInner.style.getPropertyValue('--term-cell-height')).toBe('16px')
    controller.dispose()
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

  it('sizes the flow box and clip together as output grows', () => {
    const renderer = makeRenderer()
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(19)
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })

    controller.beginBlock('printf slow', '~', 0)
    expect(controller.terminalLiveContainer.style.height).toBe('19px')
    expect(controller.terminalLiveViewport.style.height).toBe('19px')

    controller.setLiveHeight(38)
    expect(controller.terminalLiveContainer.style.height).toBe('38px')
    expect(controller.terminalLiveViewport.style.height).toBe('38px')

    controller.setLiveHeight(57)
    expect(controller.terminalLiveContainer.style.height).toBe('57px')
    expect(controller.terminalLiveViewport.style.height).toBe('57px')
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
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })

    controller.beginBlock('ls', '~', 0)

    expect(controller.terminalLiveContainer.style.height).toBe('')
    expect(controller.terminalLiveViewport.style.height).toBe('')
    expect(controller.mode).toBe('running')
  })

  it('keeps the waiting fallback for a command that has printed nothing yet', () => {
    const renderer = makeRenderer()
    ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(0)
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })

    controller.beginBlock('ls', '~', 0)

    // The waiting stand-in keeps the CSS fallback box until output retires it.
    expect(controller.terminalLiveContainer.style.height).toBe('')
    expect(controller.terminalLiveViewport.style.height).toBe('')
    expect(controller.terminalLiveContainer.querySelector('.cmd-answer-typing')).not.toBeNull()
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

  it('a delivery from a pane with no area does not end the follow (nocx-yfpxl)', () => {
    const { controller } = makeController()
    const geometry = atTail(controller)
    // The pane cannot be measured for a moment — mid-layout, a hidden tab, an
    // overlay taking the box. An IntersectionObserver whose ROOT has no area
    // reports every target as non-intersecting, so the sentinel says the
    // reader left a live end nobody left.
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      configurable: true,
      value: 0,
    })
    observerReports(false)

    // Layout is back and the content has grown past the viewport, so the
    // geometry alone can no longer say the reader was at the live end: only
    // the remembered intent can, and it is still theirs.
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      configurable: true,
      value: 400,
    })
    geometry.setScrollHeight(1400)
    controller.scrollToBottomIfFollowing()

    expect(geometry.scrollTo).toHaveBeenCalledWith({ top: 1400, behavior: 'instant' })
  })

  it('and a delivery from a pane that CAN be measured ends it: they scrolled away', () => {
    const { controller } = makeController()
    const geometry = atTail(controller)
    observerReports(false)
    geometry.setScrollHeight(1400)
    geometry.scrollTo.mockClear()

    controller.scrollToBottomIfFollowing()

    expect(geometry.scrollTo).not.toHaveBeenCalled()
  })

  it('keeps the tail visible when a running block is replaced', () => {
    const { controller } = makeController()
    const geometry = atTail(controller)
    observerReports(true)
    controller.blockManager.startBlock('tool command', '~', 0)
    controller.setRunning()
    geometry.scrollTo.mockClear()

    geometry.setScrollHeight(1400)
    // The real close: the completion, then the backend's block.closed, which
    // swaps the running element for the frozen one.
    const running = controller.blockManager.runningBlock!
    controller.blockManager.bindAttempt('att-tail')
    const domain = mintDomain({
      lane: 'l',
      lifecycle: 'prompt_ready',
      domain: 'd1',
      epoch: 1,
    }) as IntegrationDomain
    expect(
      controller.freezeFromAttempt(
        { id: 'att-tail', domain, state: 'completed', exitCode: 0, fence: 'f'.repeat(64) },
        2,
      ),
    ).toBe(false)
    controller.blockManager.blockClosed('att-tail')
    expect(running.el.isConnected).toBe(false)

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
    heard: MockInstance<PetOverlay['reactTo']>
  } {
    const renderer = makeRenderer()
    // Spied on the prototype: the overlay is built inside the controller, so
    // there is no instance to hand a double to — which is the point. A double
    // injected from the test would have passed against the dead method too.
    const heard = vi.spyOn(PetOverlay.prototype, 'reactTo').mockImplementation(() => {})
    mountWindowPet(document.createElement('div'))
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    return { controller, heard }
  }

  function finish(
    controller: ScrollbackController,
    exitCode: number,
    author: 'shell' | 'agent' = 'shell',
  ): void {
    controller.beginBlock('ls', '~', 0, author)
    controller.blockManager.bindAttempt('att-1')
    controller.blockManager.blockClosed('att-1')
    controller.freezeFromAttempt(
      { id: 'att-1', domain, state: 'completed', exitCode, fence: FENCE },
      2,
    )
  }

  it('says success when the command succeeded', () => {
    const { controller, heard } = petController()
    finish(controller, 0)
    expect(heard).toHaveBeenCalledWith('success', 'shell')
  })

  it('says failure when it did not', () => {
    const { controller, heard } = petController()
    finish(controller, 2)
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
    const { controller, heard } = petController()
    finish(controller, 0, 'agent')
    expect(heard).toHaveBeenCalledWith('success', 'agent')
  })

  it('says nothing at all in a window with no pet', () => {
    unmountWindowPet()
    const heard = vi.spyOn(PetOverlay.prototype, 'reactTo').mockImplementation(() => {})
    const renderer = makeRenderer()
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    finish(controller, 0)
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
    heard: MockInstance<PetOverlay['attendTo']>
  } {
    const heard = vi.spyOn(PetOverlay.prototype, 'attendTo').mockImplementation(() => {})
    mountWindowPet(document.createElement('div'))
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      liveContentHeight: () => null,
      cellMetrics: () => null,
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    return { controller, heard }
  }

  it('tells it when a command of yours begins', () => {
    const { controller, heard } = petController()
    controller.beginBlock('go build ./...', '~', 0)
    expect(heard).toHaveBeenCalledWith('shell')
  })

  it('tells it whose lane the command is in', () => {
    const { controller, heard } = petController()
    controller.beginBlock('go test ./...', '~', 0, 'agent')
    expect(heard).toHaveBeenCalledWith('agent')
  })

  it('says nothing at all in a window with no pet', () => {
    unmountWindowPet()
    const heard = vi.spyOn(PetOverlay.prototype, 'attendTo').mockImplementation(() => {})
    const controller = new ScrollbackController({
      pane: document.createElement('div'),
      liveContentHeight: () => null,
      cellMetrics: () => null,
      snapshotStore: new CommandSnapshotStore(),
    })
    controller.scrollbackArea.scrollTo = vi.fn()
    controller.beginBlock('ls', '~', 0)
    expect(heard).not.toHaveBeenCalled()
  })
})

describe('the scroller box keeps the tail (nocx-1ebrs.40)', () => {
  // jsdom has no ResizeObserver; the stand-in lets the test fire the delivery
  // a real box change would. One instance per construction, recorded here.
  class FakeBoxResizeObserver {
    static latest: FakeBoxResizeObserver | null = null
    readonly disconnect = vi.fn()
    private readonly cb: ResizeObserverCallback
    constructor(cb: ResizeObserverCallback) {
      this.cb = cb
      FakeBoxResizeObserver.latest = this
    }
    observe(): void {}
    unobserve(): void {}
    deliver(): void {
      this.cb([], {} as ResizeObserver)
    }
  }

  it('a box change with no block mutation keeps the live end on the bottom edge', () => {
    vi.stubGlobal('ResizeObserver', FakeBoxResizeObserver)
    try {
      const { controller } = makeController()
      const scrollTo = vi.fn()
      controller.scrollbackArea.scrollTo = scrollTo
      Object.defineProperty(controller.scrollbackArea, 'scrollHeight', {
        value: 1400,
        configurable: true,
      })
      // The composer's box animated away, the process bar swapped in: the
      // scroller's box changed and no block mutation ran. Following, the
      // live end stays pinned — nothing accumulates below the fold for a
      // later pin to land as a jump. Deliver on the BOX observer: the
      // pinnability observer is constructed after it and is a different
      // instance of the same stand-in.
      const box = (controller as unknown as { _boxResizeObserver: FakeBoxResizeObserver | null })
        ._boxResizeObserver!
      box.deliver()
      expect(scrollTo).toHaveBeenCalledWith({ top: 1400, behavior: 'instant' })

      // Not following: the box is not the reader's movement to answer.
      scrollTo.mockClear()
      stopFollowing(controller)
      box.deliver()
      expect(scrollTo).not.toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('no box watcher is installed where the platform has none', () => {
    // The pod's node carries a ResizeObserver global, so the absence must be
    // named: the constructor must skip, not throw — the same guard the follow
    // observer carries for IntersectionObserver.
    vi.stubGlobal('ResizeObserver', undefined)
    try {
      const { controller } = makeController()
      const box = (controller as unknown as { _boxResizeObserver: unknown })._boxResizeObserver
      expect(box).toBeNull()
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('dispose disconnects the box watcher', () => {
    vi.stubGlobal('ResizeObserver', FakeBoxResizeObserver)
    try {
      const { controller } = makeController()
      const watcher = FakeBoxResizeObserver.latest!
      controller.dispose()
      expect(watcher.disconnect).toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

/** jsdom lays nothing out, so a test that is about where rows sit on the glass
 *  names the rectangles itself: the scroller's box, each row's height, and the
 *  top edge that moves when something above the reader changes. */
function rect(top: number, height: number, width = 800): DOMRect {
  return {
    top,
    bottom: top + height,
    height,
    left: 0,
    right: width,
    width,
    x: 0,
    y: top,
    toJSON: () => ({}),
  }
}

/** A transcript the anchor can be taken from: a block with its own body, and —
 *  when `nested` — a command inside a turn's children, which is where a
 *  command's rows live when an agent turn owns them (ADR-0040). */
function transcript(
  controller: ScrollbackController,
  options: { nested: boolean },
): { row: HTMLElement; outer: HTMLElement; move: (by: number) => void } {
  const outer = document.createElement('div')
  outer.className = 'cmd-block'
  const body = document.createElement('div')
  body.className = 'cmd-output'
  const row = document.createElement('div')
  row.className = 'term-grid-row'
  row.dataset.blockId = 'att-row'
  row.dataset.logicalLine = '7'
  let top = 120
  row.getBoundingClientRect = () => rect(top, 17)
  if (options.nested) {
    const children = document.createElement('div')
    children.className = 'cmd-children'
    const nestedBlock = document.createElement('div')
    nestedBlock.className = 'cmd-block cmd-block-running'
    const nestedBody = document.createElement('div')
    nestedBody.className = 'cmd-output'
    nestedBody.appendChild(row)
    nestedBlock.appendChild(nestedBody)
    children.appendChild(nestedBlock)
    outer.appendChild(children)
    nestedBlock.getBoundingClientRect = () => rect(0, 2000)
  } else {
    body.appendChild(row)
    outer.appendChild(body)
  }
  controller.scrollbackInner.appendChild(outer)
  outer.getBoundingClientRect = () => rect(0, 2000)
  return { row, outer, move: (by: number) => (top += by) }
}

/** The scroller, laid out: 1000px into a 4000px transcript whose box is 600px
 *  tall — a reader away from both ends, which is the state every case here is
 *  about. */
function laidOut(controller: ScrollbackController): HTMLElement {
  const area = controller.scrollbackArea
  area.getBoundingClientRect = () => rect(0, 600)
  Object.defineProperty(area, 'scrollHeight', { value: 4000, configurable: true })
  area.scrollTop = 1000
  return area
}

describe("the reader's anchor (nocx-3wmve)", () => {
  it('names a row inside a nested command, not only a top-level body', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    const { move } = transcript(controller, { nested: true })

    controller.holdReaderPlace(() => {
      move(40)
      Object.defineProperty(area, 'scrollHeight', { value: 4040, configurable: true })
    })

    expect(area.scrollTop).toBe(1040)
  })

  it("finds the reader's row in a long block without reading every row (nocx-3wmve)", () => {
    const { controller } = makeController()
    stopFollowing(controller)
    laidOut(controller)
    // A block the size of a long command: rows laid out one under the next,
    // each with a measured top the way the browser answers. The reader is near
    // row 800 of 1 000; every measurement is counted, because that count is the
    // cost a settle pays on every delivery.
    const block = document.createElement('div')
    block.className = 'cmd-block'
    const body = document.createElement('div')
    body.className = 'cmd-output'
    let reads = 0
    for (let index = 0; index < 1000; index += 1) {
      const row = document.createElement('div')
      row.className = 'term-grid-row'
      row.dataset.blockId = 'att-long'
      row.dataset.logicalLine = String(index)
      const top = index * 17 - 800 * 17
      row.getBoundingClientRect = () => {
        reads += 1
        return rect(top, 17)
      }
      body.appendChild(row)
    }
    block.appendChild(body)
    controller.scrollbackInner.appendChild(block)
    block.getBoundingClientRect = () => rect(-800 * 17, 1000 * 17)

    // THE ORDINAL WALK IS COUNTED TOO: a row restores by its block and its
    // logical line, so counting its position among the body's children would
    // walk every sibling on every settle for a number nothing reads.
    const bodyEl = block.querySelector('.cmd-output') as HTMLElement
    const indexOf = Array.prototype.indexOf
    let walks = 0
    Array.prototype.indexOf = function (this: unknown, ...args: unknown[]) {
      if (this === bodyEl.children) walks += 1
      return indexOf.apply(this as never, args as never)
    } as typeof Array.prototype.indexOf
    try {
      controller.holdReaderPlace(() => {})
    } finally {
      Array.prototype.indexOf = indexOf
    }
    expect(walks, 'the capture walked the body for an ordinal a row does not need').toBe(0)

    // The row nearest the top edge is row 800 — a linear walk from the block's
    // first row reads ~800 of them; a bisection reads a handful.
    expect(reads, `row measurements for one capture: ${reads}`).toBeLessThanOrEqual(40)
  })

  /** A body of answer prose: what an agent's turn renders, and nothing a row
   *  identity can name. The stream appends at the end, so a paragraph's ordinal
   *  among the body's units is stable while it grows. */
  function prose(controller: ScrollbackController): {
    units: HTMLElement[]
    move: (by: number) => void
  } {
    const block = document.createElement('div')
    block.className = 'cmd-block'
    block.dataset.entryId = 'att-prose'
    const body = document.createElement('div')
    body.className = 'cmd-output'
    const tops = [0, 40, 80, 120, 160]
    const units = tops.map((start) => {
      const line = document.createElement('span')
      line.className = 'term-line'
      line.textContent = `paragraph at ${start}`
      line.getBoundingClientRect = () => rect(start, 40)
      body.appendChild(line)
      return line
    })
    block.appendChild(body)
    controller.scrollbackInner.appendChild(block)
    block.getBoundingClientRect = () => rect(0, 2000)
    return {
      units,
      move: (by: number) => {
        for (const unit of units) {
          const previous = unit.getBoundingClientRect.bind(unit)
          unit.getBoundingClientRect = () => {
            const box = previous()
            return rect(box.top + by, box.height)
          }
        }
      },
    }
  }

  it('anchors a body of answer prose, which carries no row identity (nocx-3wmve)', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    const { move } = prose(controller)

    controller.holdReaderPlace(() => {
      move(40)
      Object.defineProperty(area, 'scrollHeight', { value: 4040, configurable: true })
    })

    // The prose unit nearest the top edge stays where it was read; falling
    // back to the numeric position leaves it 40px lower on the glass.
    expect(area.scrollTop).toBe(1040)
  })

  it('anchors the tall unit that covers the top edge from above (nocx-3wmve)', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    // One unit taller than the viewport, its own top 100px above the edge: a
    // code fence, a table, a wrapped paragraph. The person is reading it, and
    // nothing in the viewport STARTS inside it.
    const block = document.createElement('div')
    block.className = 'cmd-block'
    block.dataset.entryId = 'att-tall'
    const body = document.createElement('div')
    body.className = 'cmd-output'
    const tall = document.createElement('div')
    tall.className = 'cmd-output-code'
    let top = -100
    tall.getBoundingClientRect = () => rect(top, 400)
    body.appendChild(tall)
    block.appendChild(body)
    controller.scrollbackInner.appendChild(block)
    block.getBoundingClientRect = () => rect(-2000, 4000)

    controller.holdReaderPlace(() => {
      // something above it grew: the unit moves DOWN inside the viewport
      top += 40
      Object.defineProperty(area, 'scrollHeight', { value: 4040, configurable: true })
    })

    // The unit that covers the edge keeps its (negative) offset: the view
    // follows the change above it.
    expect(area.scrollTop).toBe(1040)
  })

  it('names a prose unit inside its own body, not across the whole turn (nocx-3wmve)', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    // A TURN holds two commands, each with its own body: A above the viewport,
    // and B — the prose the person is reading, from its tenth line. A late row
    // committed to A shifts every unit's ordinal ACROSS the turn, which is how
    // the restore used to land three lines away in B.
    const turn = document.createElement('div')
    turn.className = 'cmd-block'
    turn.dataset.entryId = 'att-turn'
    const children = document.createElement('div')
    children.className = 'cmd-children'
    const command = (id: string, units: HTMLElement[]): HTMLElement => {
      const block = document.createElement('div')
      block.className = 'cmd-block'
      block.dataset.entryId = id
      const body = document.createElement('div')
      body.className = 'cmd-output'
      for (const unit of units) body.appendChild(unit)
      block.appendChild(body)
      block.getBoundingClientRect = () => rect(-4000, 8000)
      return block
    }
    const rowTops = [-200, -183, -166, -149, -132]
    const rows = rowTops.map((top, index) => {
      const row = document.createElement('div')
      row.className = 'term-grid-row'
      row.dataset.logicalLine = String(index)
      row.getBoundingClientRect = () => rect(top, 17)
      return row
    })
    const proseTops = Array.from({ length: 12 }, (_, index) => (index - 9) * 17)
    const lines = proseTops.map((top, index) => {
      const line = document.createElement('span')
      line.className = 'term-line'
      line.textContent = `line ${index}`
      line.getBoundingClientRect = () => rect(top, 17)
      return line
    })
    const a = command('att-a', rows)
    const b = command('att-b', lines)
    children.appendChild(a)
    children.appendChild(b)
    turn.appendChild(children)
    controller.scrollbackInner.appendChild(turn)
    turn.getBoundingClientRect = () => rect(-4000, 8000)

    controller.holdReaderPlace(() => {
      // A commits three rows at the START of its own body: the turn's global
      // ordinal moves by three, B's own ordinal does not, and everything below
      // A moves down by three rows.
      const body = a.querySelector(':scope > .cmd-output')!
      for (let added = 0; added < 3; added += 1) {
        const row = document.createElement('div')
        row.className = 'term-grid-row'
        row.dataset.logicalLine = String(100 + added)
        row.getBoundingClientRect = () => rect(-260 + added * 17, 17)
        body.insertBefore(row, body.firstChild)
      }
      for (const [index, line] of lines.entries()) {
        const top = proseTops[index] + 3 * 17
        line.getBoundingClientRect = () => rect(top, 17)
      }
      Object.defineProperty(area, 'scrollHeight', { value: 4051, configurable: true })
    })

    // B's tenth line was the anchor; it is three rows lower in the transcript
    // now, so the view follows it by exactly those three rows.
    expect(area.scrollTop).toBe(1051)
  })

  it('anchors a long wrapped header that fills the viewport (nocx-3wmve)', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    // A command whose header wraps over more than a screen, and nothing of its
    // body in view: the header is the only thing the person can be reading, and
    // a search over body units alone left them on the numeric fallback.
    const block = document.createElement('div')
    block.className = 'cmd-block'
    block.dataset.entryId = 'att-header'
    const header = document.createElement('div')
    header.className = 'cmd-header'
    let top = -100
    header.getBoundingClientRect = () => rect(top, 400)
    block.appendChild(header)
    controller.scrollbackInner.appendChild(block)
    block.getBoundingClientRect = () => rect(-3000, 6000)

    controller.holdReaderPlace(() => {
      // a block above it grew: the header moves down inside the viewport
      top += 40
      Object.defineProperty(area, 'scrollHeight', { value: 4040, configurable: true })
    })

    expect(area.scrollTop).toBe(1040)
  })

  it('never anchors on a header that is pinned at the edge (nocx-3wmve)', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    // A long block whose header is STICKY and stuck at the viewport's top edge,
    // with the row the person is reading underneath it. The pinned header stays
    // at y=0 whatever the rows do, so anchoring on it reports no displacement
    // and the restore moves nobody.
    const block = document.createElement('div')
    block.className = 'cmd-block'
    block.dataset.entryId = 'att-sticky'
    const header = document.createElement('div')
    header.className = 'cmd-header'
    header.style.position = 'sticky'
    header.getBoundingClientRect = () => rect(0, 40)
    const body = document.createElement('div')
    body.className = 'cmd-output'
    const row = document.createElement('div')
    row.className = 'term-grid-row'
    row.dataset.logicalLine = '12'
    let top = 40
    row.getBoundingClientRect = () => rect(top, 17)
    body.appendChild(row)
    block.appendChild(header)
    block.appendChild(body)
    controller.scrollbackInner.appendChild(block)
    block.getBoundingClientRect = () => rect(-4000, 8000)

    controller.holdReaderPlace(() => {
      top += 40
      Object.defineProperty(area, 'scrollHeight', { value: 4040, configurable: true })
    })

    expect(area.scrollTop).toBe(1040)
  })

  it('anchors a tool header that carries no id, by its element (nocx-3wmve)', () => {
    const { pane, controller } = makeController()
    // An element reference is the anchor, so the pane has to be IN the document
    // for `isConnected` to mean what it means in the product.
    document.body.appendChild(pane)
    stopFollowing(controller)
    const area = laidOut(controller)
    // A TURN whose own header is pinned at the edge, and inside it a live tool
    // block — addAnswerBlock's toolCall — with NO data-entry-id. The person is
    // reading the tool's header. Named by identity it becomes the turn's, whose
    // header is the pinned one; the element the person is on is the tool's.
    const turn = document.createElement('div')
    turn.className = 'cmd-block'
    turn.dataset.entryId = 'att-turn'
    const turnHeader = document.createElement('div')
    turnHeader.className = 'cmd-header'
    turnHeader.style.position = 'sticky'
    turnHeader.getBoundingClientRect = () => rect(0, 32)
    const children = document.createElement('div')
    children.className = 'cmd-children'
    const tool = document.createElement('div')
    tool.className = 'cmd-block'
    const toolHeader = document.createElement('div')
    toolHeader.className = 'cmd-header'
    let top = 100
    toolHeader.getBoundingClientRect = () => rect(top, 40)
    tool.appendChild(toolHeader)
    tool.getBoundingClientRect = () => rect(60, 2000)
    children.appendChild(tool)
    turn.appendChild(turnHeader)
    turn.appendChild(children)
    controller.scrollbackInner.appendChild(turn)
    turn.getBoundingClientRect = () => rect(-3000, 6000)

    controller.holdReaderPlace(() => {
      top += 40
      Object.defineProperty(area, 'scrollHeight', { value: 4040, configurable: true })
    })

    expect(area.scrollTop).toBe(1040)
    document.body.removeChild(pane)
  })

  it('puts a FOLLOWER back at the bottom edge when a change grows the transcript (nocx-3wmve)', () => {
    const { controller } = makeController()
    const area = laidOut(controller)
    area.scrollTop = 4000 - area.clientHeight // at the live end, following
    // jsdom implements no scrolling: the controller's own `scrollTo` is the
    // browser's here, exactly as the other tests in this file stand in for it.
    area.scrollTo = ((options: ScrollToOptions | number) => {
      const asked = typeof options === 'object' ? Number(options.top) : Number(options)
      // the browser clamps to what exists; jsdom lays nothing out and does not
      area.scrollTop = Math.min(asked, area.scrollHeight - area.clientHeight)
    }) as typeof area.scrollTo

    controller.holdReaderPlace(() => {
      Object.defineProperty(area, 'scrollHeight', { value: 4100, configurable: true })
    })

    // One settle, two outcomes: a follower is put back at the end. The freeze's
    // swap used to be anchor-only, so a deferred freeze under a follower grew
    // the transcript and left them off the end with nothing to re-pin them.
    expect(area.scrollTop).toBe(4100 - area.clientHeight)
  })

  it('holds a reader who is not following across any content change (settleAround)', () => {
    const { controller } = makeController()
    stopFollowing(controller)
    const area = laidOut(controller)
    const { move } = transcript(controller, { nested: false })

    controller.settleAround(() => {
      move(60)
      Object.defineProperty(area, 'scrollHeight', { value: 4060, configurable: true })
    })

    expect(area.scrollTop).toBe(1060)
  })
})

describe('a pinned header is a compact part of the view (nocx-4y82c.1.3, P2)', () => {
  it('allows at most a third of the scrollport, and nothing before layout', () => {
    expect(headerFitsPinned(100, 300), 'a third exactly').toBe(true)
    expect(headerFitsPinned(101, 300), 'one pixel over a third').toBe(false)
    expect(headerFitsPinned(600, 300), 'a header taller than the view').toBe(false)
    expect(headerFitsPinned(20, 0), 'no scrollport yet').toBe(false)
  })
})
