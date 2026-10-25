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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { ScrollbackController } from './controller'
import { IntentInput, type SessionIntent } from '../intent-input'
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
    cellMetrics: () => null,
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

describe('live projection changes preserve the input owner', () => {
  it('keeps stdin focus through qualification and leaves another control alone', () => {
    const { pane, controller } = makeController()
    document.body.append(pane)
    const style = document.createElement('style')
    const source = import.meta.dirname ?? resolve(new URL('.', import.meta.url).pathname)
    style.textContent = readFileSync(resolve(source, '../style.css'), 'utf8')
    document.head.append(style)
    const intents: SessionIntent[] = []
    const input = new IntentInput({ emit: (intent) => intents.push(intent) })
    controller.terminalLiveInner.append(input.element)
    const other = document.createElement('button')
    document.body.append(other)
    const nativeFocus = input.element.focus.bind(input.element)
    // jsdom models the blur caused by reparenting, but not the browser's
    // refusal to focus an element while an ancestor is display:none.
    const focus = vi.spyOn(input.element, 'focus').mockImplementation((options) => {
      for (let node: HTMLElement | null = input.element; node !== null; node = node.parentElement) {
        if (getComputedStyle(node).display === 'none') return
      }
      nativeFocus(options)
    })
    try {
      const command = controller.blockManager.startBlock('read reply', '~', 0)
      controller.setRunning()
      const history = document.createElement('div')
      history.className = 'live-history'
      controller.setHistoryPresentation('terminal', history, command)
      input.focus()
      expect(document.activeElement).toBe(input.element)

      // A late accepted anchor qualifies the running body. The destination
      // card starts hidden by the opaque projection, not by the input owner.
      controller.setHistoryPresentation('block', history, command)
      expect(document.activeElement).toBe(input.element)
      intents.length = 0
      document.activeElement!.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          code: 'Enter',
          bubbles: true,
          cancelable: true,
        }),
      )
      expect(intents).toEqual([{ kind: 'key', payload: 'enter' }])

      other.focus()
      controller.setHistoryPresentation('terminal', history, command)
      expect(document.activeElement).toBe(other)
    } finally {
      focus.mockRestore()
      input.destroy()
      controller.dispose()
      pane.remove()
      other.remove()
      style.remove()
    }
  })

  it('keeps only the stable owner context path through freeze, replacement and retirement', () => {
    const { controller } = makeController()
    const unrelated = controller.blockManager.addAnswerBlock('older archived question', '~')
    const turn = controller.blockManager.addAnswerBlock('run the program', '~')
    turn.toolCall({
      callId: 'context-run',
      tool: 'run',
      effect: 'mutate-destructive',
      opensBlock: true,
    })
    const command = controller.blockManager.startBlock('read reply', '~', 0, 'agent')
    const children = turn.el.querySelector<HTMLElement>(':scope > .cmd-children')!
    expect(children.contains(command.el)).toBe(true)
    controller.setRunning()
    const history = document.createElement('div')
    history.className = 'live-history'
    const changes = new MutationObserver(() => {})
    try {
      controller.setHistoryPresentation('terminal', history, command)
      for (const node of [command.el, children, turn.el]) {
        expect(node.dataset.liveContext).toBe('true')
      }
      expect(unrelated.el.dataset.liveContext).toBeUndefined()
      expect(controller.scrollbackInner.dataset.liveContext).toBeUndefined()
      expect(command.el.contains(history)).toBe(false)
      expect(command.el.contains(controller.terminalLiveContainer)).toBe(false)

      // Repeated accepted frames keep the same context without attribute writes.
      changes.observe(controller.scrollbackInner, {
        subtree: true,
        attributes: true,
        attributeFilter: ['data-live-context'],
      })
      controller.setHistoryPresentation('terminal', history, command)
      expect(changes.takeRecords()).toHaveLength(0)

      const runningElement = command.el
      closeRunningBlock(controller.blockManager)
      expect(command.el).not.toBe(runningElement)
      expect(controller.blockManager.runningBlock).toBeNull()
      controller.setHistoryPresentation('terminal', history, undefined)
      expect(runningElement.dataset.liveContext).toBeUndefined()
      for (const node of [command.el, children, turn.el]) {
        expect(node.dataset.liveContext).toBe('true')
      }

      // A new root command retires the previous nested ancestry completely.
      const next = controller.blockManager.startBlock('next command', '~', 0)
      controller.setHistoryPresentation('terminal', history, next)
      expect(next.el.dataset.liveContext).toBe('true')
      for (const node of [command.el, children, turn.el]) {
        expect(node.dataset.liveContext).toBeUndefined()
      }

      controller.setHistoryPresentation('none', history, null)
      expect(next.el.dataset.liveContext).toBeUndefined()
      controller.setHistoryPresentation('terminal', history, next)
      next.el.remove()
      controller.setHistoryPresentation('terminal', history, undefined)
      expect(next.el.dataset.liveContext).toBeUndefined()
    } finally {
      changes.disconnect()
      controller.dispose()
    }
  })
})

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
    expect(controller.terminalLiveContainer.style.height).toBe('360px')
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
    expect(controller.terminalLiveContainer.style.height).toBe('360px')

    // The card is dismissed and the pane gives the pixels back.
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 540,
      configurable: true,
    })
    controller.setLiveHeight(null)

    expect(controller.terminalLiveContainer.style.height).toBe('540px')
  })

  it('re-measures an alt-screen program when the scroller shrinks', () => {
    const { controller } = makeController()
    controller.enterFullscreen()
    expect(controller.terminalLiveContainer.style.height).toBe('360px')

    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 200,
      configurable: true,
    })
    controller.setLiveHeight(null)

    expect(controller.terminalLiveContainer.style.height).toBe('200px')
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

    // A backend frame can move the base row past the anchor. Its new offset
    // is zero, so the first real output row is no longer shifted.
    record.outputAnchorOffset = 0
    controller.outputAnchorChanged()
    controller.setLiveHeight(24 * 16)
    expect(controller.terminalLiveInner.style.transform).toBe('')
    // Both the content clip and flow box clamp to the live-region cap.
    expect(controller.terminalLiveViewport.style.height).toBe('360px')
    expect(controller.terminalLiveContainer.style.height).toBe('360px')

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

  it('reserves the block body padding around the rows, and caps the WHOLE box', () => {
    // THE RUNNING REGION IS THE BLOCK'S BODY. The frozen body has padding
    // (`--cmd-output-pad-*`); until the live region wore the same, it was
    // that much shorter than the body that replaces it, and the scrollback —
    // which hangs from its bottom edge — moved at every freeze. The number
    // is never repeated in TypeScript: the controller reads it off the
    // element, which is what this test writes to it.
    const renderer = rendererWithGeometry()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    controller.terminalLiveContainer.style.paddingTop = '2px'
    controller.terminalLiveContainer.style.paddingBottom = '6px'
    Object.defineProperty(controller.scrollbackArea, 'clientHeight', {
      value: 360,
      configurable: true,
    })
    controller.scrollbackArea.scrollTo = vi.fn()

    controller.beginBlock('ls', '~', 0)
    controller.blockManager.runningBlock!.outputAnchorOffset = 1
    controller.blockManager.runningBlock!.outputAnchorText = 'prompt ls'
    controller.outputAnchorChanged()
    // Two shown rows (the third is the translated echo): the clip holds the
    // rows, the flow box holds the rows plus the body's 8px.
    controller.setLiveHeight(3 * 16)
    expect(controller.terminalLiveViewport.style.height).toBe('32px')
    expect(controller.terminalLiveContainer.style.height).toBe('40px')

    // The cap is a ceiling on the BOX, not on the rows inside it: a grid
    // taller than the pane must not push the region past the space it shares
    // with the running block's header (nocx-zn4d), padding included.
    controller.setLiveHeight(100 * 16)
    expect(controller.terminalLiveContainer.style.height).toBe('360px')
    expect(controller.terminalLiveViewport.style.height).toBe('352px')
    controller.blockManager.clearAll()
    pane.remove()
  })

  it('reports the inline inset the grid must not be fitted into (nocx-9bpeq.8)', () => {
    // Rows carry the pane gutter now, the live region included, so the grid's
    // box is the scroller MINUS that inset. Read off the element, like the body
    // padding above: the stylesheet stays the one place the number lives.
    const renderer = rendererWithGeometry()
    const pane = document.createElement('div')
    const controller = new ScrollbackController({
      pane,
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
      snapshotStore: new CommandSnapshotStore(),
    })
    document.body.appendChild(pane)
    expect(controller.liveInlineInsetPx).toBe(0)
    controller.terminalLiveContainer.style.paddingLeft = '16px'
    controller.terminalLiveContainer.style.paddingRight = '16px'
    expect(controller.liveInlineInsetPx).toBe(32)
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
      liveContentHeight: () => renderer.liveContentHeight(),
      cellMetrics: () => ({ cellWidth: renderer.cellWidth, cellHeight: renderer.cellHeight }),
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
      ['live', controller.terminalLiveContainer],
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

    controller.beginBlock('ls', '~', 0)

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

  it('leaves the scroller alone when the transcript already has a bar, however long it is (nocx-2v80t.3.35)', async () => {
    // Toggling `overflow` on the scroller restyles and re-lays out every
    // block under it — 218 ms per toggle in WebKit at 181 blocks, about seven
    // settles per command — so a settle that toggled it made each new command
    // cost more than the last. A transcript that already overflows shows its
    // bar before, during and after the settle: there is no flash to hide, and
    // the settle's work on the scroller must not depend on how many blocks
    // sit above the live end. Counted as writes, not timed.
    for (const blocks of [1, 50, 500]) {
      const { controller, frames } = movingController([500, 440])
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

      // The stack still settles — the glide is the point — and the scroller
      // was never written to.
      expect(frames.map((f) => f.el)).toEqual(['inner'])
      written.push(...writes.takeRecords())
      expect(written).toEqual([])
      expect(controller.scrollbackArea.classList.contains('is-settling')).toBe(false)
      writes.disconnect()
      controller.blockManager.clearAll()
    }
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
    expect(controller.scrollbackInner.contains(controller.terminalLiveContainer)).toBe(true)
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

    controller.beginBlock('ls', '~', 0)

    expect(frames).toEqual([])
    controller.blockManager.clearAll()
  })

  it('moves nothing while the person is reading further up', () => {
    // Movement they did not cause is not theirs to watch: a pane scrolled away
    // from the bottom keeps its position, and the glide would be a second
    // owner of it.
    const { controller, frames } = movingController([500, 440])
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

  it('opens at nothing but the body padding for a command that has printed nothing yet', () => {
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
    controller.terminalLiveContainer.style.paddingTop = '2px'
    controller.terminalLiveContainer.style.paddingBottom = '6px'
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
      // later pin to land as a jump.
      FakeBoxResizeObserver.latest!.deliver()
      expect(scrollTo).toHaveBeenCalledWith({ top: 1400, behavior: 'instant' })

      // Not following: the box is not the reader's movement to answer.
      scrollTo.mockClear()
      stopFollowing(controller)
      FakeBoxResizeObserver.latest!.deliver()
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
