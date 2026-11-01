// @vitest-environment jsdom
//
// W3 policy-wiring tests: the editor's `onSelectionEnd` seam (ADR-0010
// §Decision 2) delivers the selected text, and TerminalContent decides
// whether to copy it. This file is the regression guard for the deleted
// textarea shim: under the shim, `selectionStart`/`selectionEnd`/`value`
// read off a contenteditable are all `undefined`, so `start === end` was
// `undefined === undefined` — the early return fired and copy-on-select was
// silently dead. The mount below exercises the REAL chain (editor mouseup →
// seam → terminal-content policy → clipboard), so that bug fails here.
//
// The mount uses the WS client fake to resolve a session, and `tab.start()`
// drives TerminalContent through the same mount() a real tab
// takes. The editor is reached through the same private-field escape hatch
// editor.test.ts uses, and the selection is seeded through the CM6 view —
// the same transaction a mouse drag produces.
import { beforeEach, describe, expect, it, vi } from 'vitest'
// node builtins are untyped here (@types/node is not installed), so the
// imports sit behind @ts-expect-error and the calls behind a contained
// no-unsafe disable — the same trade theme-catalogue.test.ts makes at file
// level, confined to this setup instead.

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const srcDir = import.meta.dirname ?? resolve(new URL('.', import.meta.url).pathname)
const STYLE_ENTRY = resolve(srcDir, 'style.css')
const BASE_STYLE_ENTRY = resolve(srcDir, 'styles/base.css')
const FRAME_STYLE_ENTRY = resolve(srcDir, 'frame/display.css')
// CommandBlockFrame's own stylesheet (spec 2026-09-15 §1.7, task B): the
// header's grid lives here now, not in styles/surfaces/command-block.css —
// see the "SSH block header" describe block below.
const COMMAND_BLOCK_FRAME_STYLE_ENTRY = resolve(srcDir, 'styles/components/command-block-frame.css')
const COMPOSER_STYLE = resolve(srcDir, 'styles/surfaces/composer.css')

import type { PaneIdentity, PaneScreenReading } from './terminal-content'
import { notifyAiConfigChanged } from './ai-config'
import type { SessionEffect } from './generated/session.effect'
import type { Row, SessionFrame } from './generated/session.frame'
import { frameOf, styleOf, wireRowOf, type CellSpec } from './painter/fixtures'
import type { CellPainter } from './painter/painter'
import type { CellModel, ScreenSnapshot } from './cell-model'
import { applyTerminalFont } from './terminal-font'
import { grantBlockFromElement, type GrantBlock } from './ask-entry'
import type { AgentStatusResult } from './generated/agent.status'
import { EditorView } from '@codemirror/view'
import {
  makeClient,
  makeClipboard,
  makeBanner,
  makeSession,
  anchoredPane,
  integrationHandler,
  lifecycleHandler,
  signalUndeliveredHandler,
  publishFailedHandler,
  historyRecordedHandler,
  blockClosedHandler,
  type ClipboardFake,
  type ClientFake,
  type SessionFake,
  FIXTURE_CWD,
  FIXTURE_DIRECTORY_LABEL,
} from './test-support/panes-fixtures'
import { ClipboardGate } from './clipboard'
import { CommandEditor } from './editor'
import { CommandLedger } from './command-ledger'
import {
  createRegistry,
  ShellInputTarget,
  type InputTarget,
  type InputTargetRegistry,
} from './input-target'
import type { ConnectionCondition } from './connection-condition'
import { TerminalContent, type SnippetFire, type TerminalContentHooks } from './terminal-content'
import { LOCAL_TARGET_ID } from './ports-client'
import { Pane } from './panes'
import { SURFACE_TERMINAL, type ContentViewport } from './pane-content'
import { LifecycleKernel, shouldShowEditor } from './lifecycle/state'
import { ProfileClient, type SSHProfile } from './profiles'
import { Dispatcher, RpcError } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import type { SessionHandle, SessionRecovery, WSClient } from './ipc'
import type { SessionIntentResult } from './generated/session.intent'
import type { SessionSignal } from './generated/session.signal'
import { blockOutputText, createCommandBlock, type RunningBlockActions } from './scrollback/blocks'
import type { AgentRunCompletion } from './run-command'
import { CommandSnapshotStore } from './command-snapshot'
import type { ActionFacts, DesiredMode } from './capability'
import type * as Capability from './capability'
import type { ScrollbackController } from './scrollback/controller'
import { pushOverlay, popOverlay } from './ui/overlay/stack'
import { _resetThemeState, getCurrentTheme, setCurrentTheme } from './renderers/theme-adapter'
import { showToast } from './ui/toast'
import type { CapturedFrame } from './frame/types'
import { createCapturedFrameView } from './frame/display'
import { emptyAttrs } from './scrollback/serializer'
import type { SessionHomeSource } from './where/session-home'
import type { BranchSource, BranchRequest } from './where/branch-source'
import type { FilesOpenResult } from './generated/files.open'
import { closeRunningBlock } from './test-support/block-close'

const capturedActionFacts = vi.hoisted(() => [] as ActionFacts[])
vi.mock('./capability', async () => {
  const actual = await vi.importActual<typeof Capability>('./capability')
  return {
    ...actual,
    deriveActions: (facts: ActionFacts) => {
      capturedActionFacts.push(facts)
      return actual.deriveActions(facts)
    },
  }
})

// The refusal path calls showToast, which mounts a Solid root; the
// export-section tests mock the module the same way.
vi.mock('./ui/toast', () => ({
  showToast: vi.fn(),
}))

// Selection is a document-global browser state, unlike the mounted pane.
// Clear it before every test so an earlier selection cannot own the next pane's
// affordance after its DOM has been removed.
beforeEach(() => {
  window.getSelection()?.removeAllRanges()
})

/**
 * The token the renderer minted for its most recent submit, read off the
 * lifecycle.submitAttempt params it actually sent.
 *
 * The published fact must echo it, exactly as the backend does
 * (internal/lifecyclepub/publisher.go): the ledger record and the attempt are
 * bound by equality on this value, so a fact that carries no token — or
 * someone else's — binds nothing. That is deliberate. Binding used to be
 * "the first unbound record still running", which silently handed one
 * command's exit status to another command's row (nocx-td6d4.10).
 */
function submitToken(client: ClientFake): string {
  const calls = client.dispatcher.call.mock.calls
  for (let i = calls.length - 1; i >= 0; i--) {
    const [method, params] = calls[i]
    if (
      method === 'lifecycle.submitAttempt' &&
      typeof params === 'object' &&
      params !== null &&
      'submitId' in params &&
      typeof params.submitId === 'string'
    ) {
      return params.submitId
    }
  }
  // No submit was made, so the backend would echo no token — and an attempt
  // with none binds no record, which is exactly the shell-originated case.
  return ''
}

describe('the old attachment surface is deleted', () => {
  it('leaves no attachment vocabulary in the grant-owned frontend files', () => {
    const productionFiles = [
      'editor.ts',
      'terminal-content.ts',
      'agent-ask.ts',
      'ask-entry.ts',
      'scrollback/blocks.ts',
      'style.css',
    ]
    const forbidden = [
      ['reference', 'Chip', 'Label'].join(''),
      ['.nocx-editor', 'reference', 'count'].join('-'),
      ['attach', 'output'].join('-'),
      ['attach', 'Affordance'].join(''),
      ['reference', 'Chips'].join(''),
      ['chip', 'Fingerprint'].join(''),
      ['data', 'attached'].join('-'),
      ['row', 'Start'].join(''),
      ['row', 'End'].join(''),
    ]
    for (const relative of productionFiles) {
      const source = readFileSync(resolve(srcDir, relative), 'utf8')
      for (const token of forbidden) expect(source).not.toContain(token)
    }
  })
})

/**
 * TerminalContent keeps the editor private; tests need the live instance the
 * mount created (same escape hatch editor.test.ts uses for the CM6 view).
 */
const editorOf = (content: TerminalContent): CommandEditor => {
  const withEditor = content as unknown as { editor: CommandEditor }
  return withEditor.editor
}
const editorViewOf = (editor: CommandEditor): EditorView =>
  (editor as unknown as { view: EditorView }).view

/** The live session mock behind TerminalContent's private field — the same
 *  escape hatch editorOf uses. `send` is what a raw pty write lands on. */
const sessionOf = (content: TerminalContent): SessionFake =>
  (content as unknown as { session: SessionFake }).session

/** The pane's input element (nocx-zg3k3.3.1): the seam a person's keyboard now
 *  reaches; it emits structured key intents. Private, reached the same way
 *  editorOf and sessionOf reach theirs. */
const intentElementOf = (content: TerminalContent): HTMLTextAreaElement => {
  const withInput = content as unknown as {
    _intentInput: { element: HTMLTextAreaElement } | null
  }
  const element = withInput._intentInput?.element
  if (!element) throw new Error('the pane has no input element mounted')
  return element
}

/** Type into the pane the way a person does: a physical key for a control key
 *  or a chord, and the committed text the layout produced for a character. The
 *  old `_fireData` drove xterm's onData with encoded bytes, which is no longer
 *  the person's path — the bytes on it now are the client's own writes. */
function typeIntoPane(content: TerminalContent, ...keys: string[]): void {
  const element = intentElementOf(content)
  const press = (code: string, key: string, init: KeyboardEventInit = {}): void => {
    element.dispatchEvent(
      new KeyboardEvent('keydown', { code, key, bubbles: true, cancelable: true, ...init }),
    )
  }
  for (const sequence of keys) {
    if (sequence === '\r') {
      press('Enter', 'Enter')
      continue
    }
    if (sequence === '\x03') {
      press('KeyC', 'c', { ctrlKey: true })
      continue
    }
    for (const ch of sequence) {
      press(/[a-z]/i.test(ch) ? `Key${ch.toUpperCase()}` : 'Unidentified', ch)
      const committed = new Event('input', { bubbles: true })
      Object.defineProperty(committed, 'data', { value: ch })
      Object.defineProperty(committed, 'inputType', { value: 'insertText' })
      element.dispatchEvent(committed)
    }
  }
}
/** Adapt the public-only fake at the injected adoption seam. SessionHandle's
 * private client prevents structural typing even though all used methods match. */
const asSessionHandleForTest = (session: SessionFake): SessionHandle =>
  session as unknown as SessionHandle

/** The scrollback owner behind TerminalContent, for placement assertions. */
const scrollbackFor = (content: TerminalContent): ScrollbackController => {
  const withScrollback = content as unknown as { scrollback: ScrollbackController }
  return withScrollback.scrollback
}

/** The live recall overlay behind TerminalContent's private field — the
 *  same escape hatch editorOf uses. */
const recallOf = (content: TerminalContent): { isOpen: boolean } => {
  const withRecall = content as unknown as { recall: { isOpen: boolean } }
  return withRecall.recall
}

/** The editor's internal CM6 view — reached only to seed selections. */
const viewOf = (ed: CommandEditor): EditorView => {
  const withView = ed as unknown as { view: EditorView }
  return withView.view
}

/** Mount options. */
interface MountOpts {
  /** Append the tab's pane to document.body. The document-level keydown
   *  handler bails on a disconnected target, so tests that exercise it need
   *  the pane in the tree. Default false — the copy-on-select tests do not. */
  attachToDocument?: boolean
  /** Mount an SSH tab (the capability rail is SSH-only, nocx-4t37.2).
   *  profileId '' is a hand-typed connection with no saved profile
   *  (ADR-0069's own case for the connect-time ask's session-only answer) —
   *  the same convention buildSSHPane uses, never undefined. */
  ssh?: { profileId: string; host: string }
  /** Host callbacks handed to the TerminalContent (TerminalContentHooks). */
  hooks?: Partial<TerminalContentHooks>
  /** What `content.ready` must settle to. Default true — an open that is
   *  expected to fail (the host-key refusal) sets it false. */
  expectedReady?: boolean
  /** The pane this content is the surface of, and when its row exists
   *  (nocx-rtg0.29). The default is a pane the chain already holds, which is
   *  what every test that is not about the race wants. */
  pane?: PaneIdentity
  /** Show the pane BEFORE it mounts — the order the real activation seam
   *  uses, and the one no test used to model: PaneManager.activate() runs
   *  between Pane.start() and the renderer being built, so the show lands
   *  on a pane with no scrollback and no second show ever comes. */
  visibleBeforeMount?: boolean
  /** The pane's real box, measured by its presentation host during mount. */
  viewportBeforeMount?: ContentViewport
}
/** Mount a real TerminalContent inside a Tab and return the live editor view. */
async function mountTerminal(
  clipboard: ClipboardFake = makeClipboard(),
  opts: MountOpts = {},
  client?: ClientFake,
  profileClient?: ProfileClient | null,
): Promise<{
  view: EditorView
  ed: CommandEditor
  clipboard: ClipboardFake
  gate: ClipboardGate
  content: TerminalContent
  tab: Pane
  teardown: () => void
}> {
  const clientFake = client ?? makeClient()
  // ClientFake is structurally a WSClient; the tab layer expects the real type.
  const wsClient = clientFake as unknown as WSClient
  const gate = new ClipboardGate()
  const content = new TerminalContent(
    wsClient,
    opts.pane ?? anchoredPane(),
    clipboard,
    gate,
    makeBanner(),
    profileClient ?? null,
    () => {},
    opts.ssh,
    opts.hooks,
  )
  const tab = new Pane(
    content,
    {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'local' },
      supportsAttention: true,
      defaultTitle: 'Terminal',
    },
    99,
    'tab-99',
  )
  const paneParent = document.createElement('div')
  paneParent.append(tab.pane)
  if (opts.attachToDocument) document.body.append(paneParent)
  if (opts.visibleBeforeMount) content.setVisible(true)
  if (opts.viewportBeforeMount) {
    const { width, height } = opts.viewportBeforeMount
    Object.defineProperty(tab.pane, 'getBoundingClientRect', {
      value: () => ({ width, height }) as DOMRect,
    })
  }
  await tab.start()
  await expect(content.ready).resolves.toBe(opts.expectedReady ?? true)

  const ed = editorOf(content)
  return {
    view: viewOf(ed),
    ed,
    clipboard,
    gate,
    content,
    tab,
    teardown: () => {
      tab.close()
      paneParent.remove()
    },
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Geometry handoff and PTY resize policy (nocx-cwnz0)
// ═══════════════════════════════════════════════════════════════════════════
// Two fit drivers share one geometry decision: the presentation delivery
// (viewportChanged) and the live-region output path (refitIfResized). A pane
// fit followed by identical parsed-output frames must not fit the same usable
// rectangle a second time, and the trailing PTY resize must settle on the
// final grid and be cancelled by disposal.
describe('TerminalContent geometry follows accepted painter frames', () => {
  it('uses committed backend cell metrics for the window size report', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      session.fireScreenFrame(
        screenFrame(4, ['accepted geometry'], { cellWidthPx: 17, cellHeightPx: 34 }),
      )
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(4))
      vi.advanceTimersByTime(80)

      const expected = {
        cols: SCREEN_COLS,
        rows: SCREEN_ROWS,
        xpixel: SCREEN_COLS * 17,
        ypixel: SCREEN_ROWS * 34,
      }
      expect(session.sendResize).toHaveBeenCalledWith(expected)
      expect(paneScreen()?.reported).toEqual(expected)
    } finally {
      teardown()
      vi.useRealTimers()
    }
  })

  it('converts accepted device metrics after a DPR change, then adopts accepted geometry', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      session.fireScreenFrame(
        screenFrame(4, ['accepted geometry'], { cellWidthPx: 7, cellHeightPx: 16 }),
      )
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(4))
      vi.advanceTimersByTime(80)
      session.sendResize.mockClear()

      vi.stubGlobal('devicePixelRatio', 2)
      expect(window.devicePixelRatio).toBe(2)
      ;(content as unknown as { _scheduleResizeReport(): void })._scheduleResizeReport()
      vi.advanceTimersByTime(80)

      const resizeCalls = session.sendResize.mock.calls
      const report = resizeCalls[resizeCalls.length - 1]?.[0]
      if (!report) throw new Error('expected a resize report after the DPR change')
      expect(report.xpixel).toBe(report.cols * 14)
      expect(report.ypixel).toBe(report.rows * 32)

      session.fireScreenFrame(
        screenFrame(5, ['updated geometry'], {
          cols: report.cols,
          rows: report.rows,
          cellWidthPx: 14,
          cellHeightPx: 32,
        }),
      )
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(5))
      expect(paneScreen()?.geometry).toMatchObject({ cellWidthPx: 14, cellHeightPx: 32 })
    } finally {
      vi.unstubAllGlobals()
      teardown()
      vi.useRealTimers()
    }
  })

  it('opens a fresh shell at its measured pane size before any screen frame exists', async () => {
    const style = document.createElement('style')
    style.textContent = '.cell-metric-probe { line-height: 16px; }'
    document.head.append(style)
    const rectSpy = vi
      .spyOn(HTMLElement.prototype, 'getBoundingClientRect')
      .mockImplementation(function (this: HTMLElement) {
        if (this.classList.contains('cell-metric-probe')) {
          // Glyph ink must not replace the declared 16px terminal line box.
          return { width: 64 * 8, height: 12 } as DOMRect
        }
        return Element.prototype.getBoundingClientRect.call(this)
      })
    const client = makeClient()
    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        attachToDocument: true,
        viewportBeforeMount: { width: 960, height: 640 },
      },
      client,
    )
    try {
      expect(client.openSession.mock.calls[0][0]).toEqual({
        cols: 120,
        rows: 40,
        xpixel: 960,
        ypixel: 640,
      })
    } finally {
      teardown()
      rectSpy.mockRestore()
      style.remove()
    }
  })

  it('bootstraps the first pixel report from the shared font probe when the backend has none', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const rectSpy = vi
      .spyOn(HTMLElement.prototype, 'getBoundingClientRect')
      .mockImplementation(function (this: HTMLElement) {
        if (this.classList.contains('cell-metric-probe')) {
          return { width: 64 * 8 } as DOMRect
        }
        if (this.classList.contains('term-grid-row')) {
          return { width: SCREEN_COLS * 8, height: 17 } as DOMRect
        }
        return Element.prototype.getBoundingClientRect.call(this)
      })
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['prompt'], { cellWidthPx: 0, cellHeightPx: 0 }))
      await vi.waitFor(() => expect(tab.pane.querySelector('.term-grid-row')).not.toBeNull())
      vi.advanceTimersByTime(80)

      expect(session.sendResize).toHaveBeenCalledWith({
        cols: SCREEN_COLS,
        rows: SCREEN_ROWS,
        xpixel: SCREEN_COLS * 8,
        ypixel: SCREEN_ROWS * 17,
      })
    } finally {
      teardown()
      rectSpy.mockRestore()
      vi.useRealTimers()
    }
  })

  it('refits when the scroller alone changes width, as a classic scrollbar does (nocx-zg3k3.30)', async () => {
    // A transient overflow put WebKitGTK's 10px scrollbar into the scroller
    // while the grid was fitted; the pane's own box never changed, so nothing
    // refitted when the bar went, and the grid stayed one column short.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const observed = new Map<Element, ResizeObserverCallback>()
    class RecordingResizeObserver {
      constructor(private readonly callback: ResizeObserverCallback) {}
      observe(el: Element): void {
        observed.set(el, this.callback)
      }
      unobserve(): void {}
      disconnect(): void {}
    }
    vi.stubGlobal('ResizeObserver', RecordingResizeObserver)
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['prompt']))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(1))
      const area = scrollbackFor(content).scrollbackArea
      let width = 790
      Object.defineProperty(area, 'clientWidth', { configurable: true, get: () => width })
      content.viewportChanged({ width: 800, height: 400 })
      vi.advanceTimersByTime(80)
      const first = session.sendResize.mock.calls[session.sendResize.mock.calls.length - 1]?.[0]
      expect(first).toBeDefined()

      // The bar goes: the scroller is 10px wider, the pane is not.
      width = 800
      const onScroller = observed.get(area)
      expect(onScroller).toBeDefined()
      onScroller!([], {} as ResizeObserver)
      vi.advanceTimersByTime(80)
      const second = session.sendResize.mock.calls[session.sendResize.mock.calls.length - 1]?.[0]
      // 790px and 800px of 8px cells: 98 columns, then the 100 that fit now.
      expect(first.cols).toBe(98)
      expect(second?.cols).toBe(100)
    } finally {
      vi.unstubAllGlobals()
      teardown()
      vi.useRealTimers()
    }
  })

  it('coalesces quick pane resizes and sends only the final grid', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['prompt']))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(1))

      content.viewportChanged({ width: 1200, height: 800 })
      content.viewportChanged({ width: 1000, height: 600 })
      content.viewportChanged({ width: 800, height: 400 })
      expect(session.sendResize).not.toHaveBeenCalled()
      vi.advanceTimersByTime(80)

      expect(session.sendResize).toHaveBeenCalledTimes(1)
      const report = session.sendResize.mock.calls[0][0]
      expect(report.cols).toBeGreaterThan(0)
      expect(report.rows).toBeGreaterThan(0)
      expect(report.xpixel).toBe(report.cols * 8)
      expect(report.ypixel).toBe(report.rows * 16)
    } finally {
      teardown()
      vi.useRealTimers()
    }
  })

  it('ignores stale frames and does not replace accepted geometry', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(3, ['accepted'], { cellWidthPx: 10, cellHeightPx: 20 }))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(3))
      vi.advanceTimersByTime(80)
      const firstReport =
        session.sendResize.mock.calls[session.sendResize.mock.calls.length - 1]?.[0]

      session.fireScreenFrame(screenFrame(2, ['stale'], { cellWidthPx: 20, cellHeightPx: 40 }))
      vi.advanceTimersByTime(80)
      expect(paneScreen()?.revision).toBe(3)
      expect(session.sendResize.mock.calls[session.sendResize.mock.calls.length - 1]?.[0]).toEqual(
        firstReport,
      )
    } finally {
      teardown()
      vi.useRealTimers()
    }
  })
})

describe('the pane is the native file-drop target (nocx-9le.5.8)', () => {
  // In the Wails window a drop delivers absolute paths on the BACKEND's
  // machine, and the renderer may never learn one (design R2). The runtime
  // finds the nearest ancestor carrying data-file-drop-target and hands Go
  // every attribute of that element, so these two attributes are the whole
  // renderer half of the native drop: which element takes the drop, and
  // which session it belongs to. Without data-session-id a drop mints
  // tickets nothing can route.
  it('marks the pane with data-file-drop-target and the session it belongs to', async () => {
    const { content, tab, teardown } = await mountTerminal()
    expect(tab.pane.hasAttribute('data-file-drop-target')).toBe(true)
    expect(tab.pane.getAttribute('data-session-id')).toBe(sessionOf(content).sessionId)
    teardown()
  })

  it('stops being a drop target when the session is gone', async () => {
    const { tab, teardown } = await mountTerminal()
    expect(tab.pane.hasAttribute('data-file-drop-target')).toBe(true)
    teardown()
    expect(tab.pane.hasAttribute('data-file-drop-target')).toBe(false)
    expect(tab.pane.hasAttribute('data-session-id')).toBe(false)
  })

  // D9, through the seam a person actually reaches: the pane element, a
  // real drag, and the draft afterwards. The module's own tests cover the
  // routing; this one covers that the pane is WIRED to it — a handler
  // nothing attached is a gesture that does nothing, and no unit test of
  // the handler can say so.
  //
  // This is the BROWSER half, and it is the half that cannot honour D9: a
  // `File` has a name and no location, so there is no path to insert and
  // the module refuses and says why (its own tests assert the message). The
  // draft therefore stays empty — inserting the base name would look like
  // it worked and then run the command against a different file or none.
  // What says the pane is wired at all is that the drop was CLAIMED:
  // preventDefault is the handler's first act on a files drag, and an
  // unattached one leaves the event untouched.
  it('a drop on a LOCAL tab is claimed, inserts no bare name, and uploads nothing', async () => {
    const client = makeClient()
    const { content, view, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      editorOf(content).show()
      const transfer = {
        types: ['Files'],
        files: [new File(['hello'], 'notes.txt')],
      } as unknown as DataTransfer
      const drop = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent
      Object.defineProperty(drop, 'dataTransfer', { value: transfer })
      tab.pane.dispatchEvent(drop)

      expect(drop.defaultPrevented).toBe(true)
      await vi.waitFor(() => expect(view.state.doc.toString()).toBe(''))
      // Copying a file onto the machine it is already on is not a thing
      // anybody asked for: no upload method is called at all.
      expect(client.dispatcher.call).not.toHaveBeenCalledWith('files.upload', expect.anything())
      expect(client.dispatcher.call).not.toHaveBeenCalledWith('files.open', expect.anything())
    } finally {
      teardown()
    }
  })

  it('leaves a drag that is not a files drag alone, so the tab strip still reorders', async () => {
    const { tab, teardown } = await mountTerminal()
    try {
      // The tab strip's own drag carries application/x-nocx-tab, never
      // Files (layout/strip-drag.ts) — and this is the same condition v3's
      // runtime applies before it touches a drag.
      const transfer = {
        types: ['application/x-nocx-tab'],
        files: [],
      } as unknown as DataTransfer
      const over = new Event('dragover', { bubbles: true, cancelable: true }) as DragEvent
      Object.defineProperty(over, 'dataTransfer', { value: transfer })
      tab.pane.dispatchEvent(over)
      expect(over.defaultPrevented).toBe(false)
    } finally {
      teardown()
    }
  })
})

describe('SSH open host-key recovery', () => {
  const routeEvidence = {
    host: 'db.example.com:22',
    knownHostsHost: 'nocx-v1-route:22',
    algorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:new',
    key: 'a2V5',
    changed: false,
  }

  it('waits for explicit trust and retries the exact failed open', async () => {
    const openSSHSession = vi
      .fn()
      .mockRejectedValueOnce(
        new RpcError('host-key-unknown: unknown host key', -32603, routeEvidence),
      )
      .mockResolvedValueOnce(makeSession())
    const onHostKeyError = vi.fn().mockResolvedValue(true)
    const client = makeClient({ openSSHSession })

    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'ssh:test:1', host: 'db.example.com' },
        hooks: { onHostKeyError },
      },
      client,
    )
    try {
      expect(onHostKeyError).toHaveBeenCalledWith(
        {
          ...routeEvidence,
          storedFingerprint: undefined,
          changed: false,
          profileId: 'ssh:test:1',
        },
        expect.any(AbortSignal),
      )
      expect(openSSHSession).toHaveBeenCalledTimes(2)
      expect(openSSHSession.mock.calls[0]).toEqual(openSSHSession.mock.calls[1])
    } finally {
      teardown()
    }
  })

  it('does not trust or retry when the user declines', async () => {
    const openSSHSession = vi.fn().mockRejectedValue(
      new RpcError('host-key-changed: host key mismatch', -32603, {
        ...routeEvidence,
        changed: true,
        storedFingerprint: 'SHA256:old',
      }),
    )
    const onHostKeyError = vi.fn().mockResolvedValue(false)
    const client = makeClient({ openSSHSession })

    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'ssh:test:1', host: 'db.example.com' },
        hooks: { onHostKeyError },
        expectedReady: false,
      },
      client,
    )
    try {
      expect(openSSHSession).toHaveBeenCalledTimes(1)
      expect(tab.pane.textContent).toContain('Host key was not trusted for db.example.com:22')
    } finally {
      teardown()
    }
  })
})

describe('SSH open connect-time-ask recovery (ADR-0069)', () => {
  it('on an already-trusted machine, asks about the method alone and retries once answered', async () => {
    const openSSHSession = vi
      .fn()
      .mockRejectedValueOnce(
        new RpcError(
          'nocx has not been told whether it may use its helper on db.example.com:22',
          -32603,
          {
            host: 'db.example.com:22',
            fingerprint: 'SHA256:trusted',
            helperAsk: true,
          },
        ),
      )
      .mockResolvedValueOnce(makeSession())
    const onHelperConsentAsk = vi.fn().mockResolvedValue('script')
    const onHostKeyError = vi.fn()
    const client = makeClient({ openSSHSession })

    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'ssh:test:1', host: 'db.example.com' },
        hooks: { onHelperConsentAsk, onHostKeyError },
      },
      client,
    )
    try {
      expect(onHelperConsentAsk).toHaveBeenCalledWith(
        {
          host: 'db.example.com:22',
          fingerprint: 'SHA256:trusted',
          hostKey: null,
          profileId: 'ssh:test:1',
        },
        expect.any(AbortSignal),
      )
      // The already-trusted case never raises the plain host-key dialog —
      // there is nothing about the key for it to show.
      expect(onHostKeyError).not.toHaveBeenCalled()
      expect(openSSHSession).toHaveBeenCalledTimes(2)
      expect(openSSHSession.mock.calls[0]).toEqual(openSSHSession.mock.calls[1])
    } finally {
      teardown()
    }
  })

  it('when the key is ALSO unknown, the combined evidence carries the nested host-key data and only one hook fires', async () => {
    const openSSHSession = vi
      .fn()
      .mockRejectedValueOnce(
        new RpcError(
          'a person must decide before nocx may use its helper on db.example.com:22',
          -32603,
          {
            host: 'db.example.com:22',
            fingerprint: 'SHA256:offered',
            helperAsk: true,
            hostKey: {
              host: 'db.example.com:22',
              knownHostsHost: 'nocx-v1-route:22',
              algorithm: 'ssh-ed25519',
              fingerprint: 'SHA256:offered',
              key: 'a2V5',
              changed: false,
            },
          },
        ),
      )
      .mockResolvedValueOnce(makeSession())
    const onHelperConsentAsk = vi.fn().mockResolvedValue('helper')
    const onHostKeyError = vi.fn()
    const client = makeClient({ openSSHSession })

    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'ssh:test:1', host: 'db.example.com' },
        hooks: { onHelperConsentAsk, onHostKeyError },
      },
      client,
    )
    try {
      expect(onHelperConsentAsk).toHaveBeenCalledWith(
        {
          host: 'db.example.com:22',
          fingerprint: 'SHA256:offered',
          hostKey: {
            host: 'db.example.com:22',
            knownHostsHost: 'nocx-v1-route:22',
            algorithm: 'ssh-ed25519',
            fingerprint: 'SHA256:offered',
            storedFingerprint: undefined,
            key: 'a2V5',
            changed: false,
            profileId: 'ssh:test:1',
          },
          profileId: 'ssh:test:1',
        },
        expect.any(AbortSignal),
      )
      // ONE dialog answers both: the plain host-key hook never fires for a
      // refusal the combined ask already claimed (owner's decision,
      // 2026-09-16 — never two dialogs in sequence).
      expect(onHostKeyError).not.toHaveBeenCalled()
      expect(openSSHSession).toHaveBeenCalledTimes(2)
    } finally {
      teardown()
    }
  })

  it('closing the ask without an answer fails the open rather than retrying', async () => {
    const openSSHSession = vi.fn().mockRejectedValue(
      new RpcError(
        'nocx has not been told whether it may use its helper on db.example.com:22',
        -32603,
        {
          host: 'db.example.com:22',
          fingerprint: 'SHA256:trusted',
          helperAsk: true,
        },
      ),
    )
    const onHelperConsentAsk = vi.fn().mockResolvedValue(null)
    const client = makeClient({ openSSHSession })

    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'ssh:test:1', host: 'db.example.com' },
        hooks: { onHelperConsentAsk },
        expectedReady: false,
      },
      client,
    )
    try {
      expect(openSSHSession).toHaveBeenCalledTimes(1)
      expect(tab.pane.textContent).toContain(
        'The connection to db.example.com:22 needs an answer before it can continue',
      )
    } finally {
      teardown()
    }
  })

  // A hand-typed ssh with no saved connection has nowhere to keep the
  // answer — connections.setIntegrationMethod writes nothing for it to
  // persist — so the retry itself must carry the chosen method, for this
  // session alone (ADR-0069's own words).
  it('a hand-typed connection with no saved profile carries the chosen method on the retried open', async () => {
    const openSSHSessionByHost = vi
      .fn()
      .mockRejectedValueOnce(
        new RpcError(
          'nocx has not been told whether it may use its helper on db.example.com:22',
          -32603,
          {
            host: 'db.example.com:22',
            fingerprint: 'SHA256:trusted',
            helperAsk: true,
          },
        ),
      )
      .mockResolvedValueOnce(makeSession())
    const onHelperConsentAsk = vi.fn().mockResolvedValue('script')
    const client = makeClient({ openSSHSessionByHost })

    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: '', host: 'db.example.com' },
        hooks: { onHelperConsentAsk },
      },
      client,
    )
    try {
      expect(openSSHSessionByHost).toHaveBeenCalledTimes(2)
      // First attempt: no override, since nothing has been answered yet.
      expect(openSSHSessionByHost.mock.calls[0][4]).toBeUndefined()
      // Retry: the method just chosen rides this open alone.
      expect(openSSHSessionByHost.mock.calls[1][4]).toBe('script')
    } finally {
      teardown()
    }
  })
})

/** Flip the active input target through the real composer chord. */
const switchInputTarget = (ed: CommandEditor): void => {
  viewOf(ed).contentDOM.dispatchEvent(
    new KeyboardEvent('keydown', {
      key: 'Enter',
      metaKey: true,
      bubbles: true,
      cancelable: true,
    }),
  )
}

/** Complete a mouse selection gesture over the editor surface. */
const mouseupOn = (view: EditorView): void => {
  view.contentDOM.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
}

describe('the editor never copies on selection (nocx-w7h.17)', () => {
  // Copy-on-select is the terminal's convention and belongs to text you can
  // only read. In the editor the same gesture means the opposite — you select
  // in order to replace — so copying there overwrote the clipboard with the
  // very text about to be deleted: the owner selected part of a header to
  // paste a key over it, and the key was gone.
  it('a completed selection gesture in the editor writes nothing to the clipboard', async () => {
    const { view, ed, clipboard, teardown } = await mountTerminal()
    try {
      ed.insertText('echo hello world')
      view.dispatch({ selection: { anchor: 5, head: 10 } }) // "hello"
      mouseupOn(view)
      expect(clipboard.writeText).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

// The focus bounce (P0-4, ~terminal-content.ts:2895) returns focus to the
// editor whenever something inside the pane takes it, with named exceptions —
// the editor itself, the live terminal, the receipt's review mode. A block's
// own ⋮ (nocx-9bpeq.1) is the newest one: it lives inside the pane like any
// other scrollback content, so an unconditional bounce took the caret back
// the instant the button took focus and the keyboard path to it (ADR-0008)
// could never land. No existing describe block in this file names this
// listener, so both directions are asserted here: the exception, and that it
// is still exactly as narrow as the receipt's.
//
// The check keys on `data-block-control` (nocx-9bpeq.12 round 6), not
// `data-block-actions` — the latter is the ⋮'s own identity alone, and a
// running block's Stop control shares only the former, the attribute this
// guard and block-selection actually care about.
describe('the focus bounce yields to a block’s own actions control (nocx-9bpeq.1)', () => {
  it('focusing [data-block-control] with the editor visible keeps focus there', async () => {
    const { ed, content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      content.setVisible(true)
      ed.show()
      expect(ed.isVisible).toBe(true)

      const actions = document.createElement('button')
      actions.setAttribute('data-block-control', '')
      tab.pane.appendChild(actions)

      actions.focus()

      expect(document.activeElement).toBe(actions)
    } finally {
      teardown()
    }
  })

  it('anything else inside the pane still bounces back to the editor', async () => {
    const { ed, content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      content.setVisible(true)
      ed.show()
      expect(ed.isVisible).toBe(true)

      const other = document.createElement('button')
      tab.pane.appendChild(other)

      other.focus()

      expect(ed.rootContains(document.activeElement)).toBe(true)
    } finally {
      teardown()
    }
  })
})

it('a focused interactive control keeps its keys — the typing rescue stands down (nocx-nak2)', async () => {
  const { view, ed, content, teardown } = await mountTerminal(makeClipboard(), {
    attachToDocument: true,
  })
  try {
    content.setVisible(true)
    ed.show()
    expect(ed.isVisible).toBe(true)

    // A button outside the terminal (the sidebar, say) has the focus —
    // the state a user reaches by tabbing, where Space must activate the
    // button, not type into the prompt.
    const probe = document.createElement('button')
    probe.textContent = 'probe'
    document.body.appendChild(probe)
    probe.focus()
    expect(document.activeElement).toBe(probe)

    const ev = new KeyboardEvent('keydown', { key: 'x', bubbles: true, cancelable: true })
    document.body.dispatchEvent(ev)

    // The rescue stood down: focus stayed on the button, nothing landed in
    // the prompt, and nothing was preventDefaulted (the rescue is
    // focus-only — the native insertion would not have been cancelled).
    expect(document.activeElement).toBe(probe)
    expect(view.state.doc.toString()).toBe('')
    expect(ev.defaultPrevented).toBe(false)
    probe.remove()
  } finally {
    teardown()
  }
})

describe('Escape with the editor visible but unfocused (focus-loss rescue)', () => {
  /** Dispatch Escape where a user's keystroke lands after clicking away —
   *  on the body, not on the editor surface. */
  const escapeOnBody = (): KeyboardEvent => {
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.body.dispatchEvent(ev)
    return ev
  }

  it('Escape clears the draft after a click outside the editor took the focus', async () => {
    const { view, ed, content, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('ls -la')
      expect(ed.isVisible).toBe(true)

      // A click on the scrollback moves the focus off the editor surface.
      view.contentDOM.blur()
      expect(document.activeElement).not.toBe(view.contentDOM)

      const ev = escapeOnBody()
      expect(ev.defaultPrevented).toBe(true)
      expect(ed.getDoc()).toBe('')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('Escape dismisses an open recall overlay and restores the captured draft, not clears it', async () => {
    const { view, ed, content, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    // TerminalContent keeps the session private; tests reach the live fake
    // through the same escape hatch editorOf uses.
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      // A real submitted command populates the history ledger.
      ed.show()
      ed.insertText('make deploy')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The shell command and Enter share the session-intent queue.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make deploy'],
          ['key', 'enter'],
        ]),
      )

      // A non-empty draft opens recall on Up-at-top; the overlay previews
      // the newest row into the editor.
      ed.show()
      ed.insertText('echo kept')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
      expect(ed.root.querySelector('.ui-floating-panel[data-variant="recall"]')).not.toBeNull()
      // The recall query crosses the control plane (nocx-rtg0.13); the
      // preview lands when the answer does.
      await vi.waitFor(() => expect(ed.getDoc()).toBe('make deploy')) // previewing the only row

      // The user clicked the scrollback while the overlay was up.
      view.contentDOM.blur()
      const ev = escapeOnBody()
      expect(ev.defaultPrevented).toBe(true)
      // The overlay dismissed and restored the captured draft — a clear
      // path would have emptied the doc and left the panel open. The panel
      // node stays mounted after close (its `dataset.open` is the
      // visibility contract, not its presence in the DOM).
      const panel = ed.root.querySelector<HTMLElement>('.ui-floating-panel[data-variant="recall"]')
      expect(panel?.dataset.open).toBe('false')
      expect(ed.getDoc()).toBe('echo kept')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it("Escape in somebody else's text control leaves the draft alone", async () => {
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    const input = document.createElement('input')
    document.body.append(input)
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('ls -la')
      input.focus()
      const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
      input.dispatchEvent(ev)
      expect(ev.defaultPrevented).toBe(false)
      expect(ed.getDoc()).toBe('ls -la')
    } finally {
      input.remove()
      teardown()
    }
  })

  it('Escape with a modal overlay open leaves the draft alone', async () => {
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    let closed = false
    const entry = pushOverlay(() => {
      closed = true
      return true
    })
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('ls -la')
      const ev = escapeOnBody()
      // The overlay stack owns Escape while a modal is up: its own handler
      // closes the overlay (and preventDefaults); the terminal rescue
      // stands down and the draft survives.
      expect(closed).toBe(true)
      expect(ev.defaultPrevented).toBe(true)
      expect(ed.getDoc()).toBe('ls -la')
    } finally {
      popOverlay(entry)
      teardown()
    }
  })

  it('Escape clears the draft when focus is parked in the live grid', async () => {
    const { ed, content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    let gridInput: HTMLTextAreaElement | null = null
    try {
      content.setVisible(true)
      ed.show()
      const live = tab.pane.querySelector<HTMLElement>('.terminal-live-container')
      expect(live).not.toBeNull()
      // The terminal input element lives inside the live container; while
      // the editor is up the grid is read-only dead space, so focus parked
      // there must be rescued like a click on the scrollback.
      gridInput = document.createElement('textarea')
      live!.appendChild(gridInput)
      ed.insertText('ls -la')
      gridInput.focus()
      expect(document.activeElement).toBe(gridInput)

      const ev = escapeOnBody()
      expect(ev.defaultPrevented).toBe(true)
      expect(ed.getDoc()).toBe('')
    } finally {
      gridInput?.remove()
      teardown()
    }
  })
})

describe('shell highlighting is actually wired (nocx-dgs)', () => {
  // Reachability, not tokenisation. shell-highlight.ts has its own tests for
  // what the tokens are; this one exists because a language layer that nothing
  // passes to the editor is a feature the product does not have. It fails if
  // the second constructor argument at the composition point is dropped.
  it('the editor the real mount builds colours shell syntax', async () => {
    const { view, ed, teardown } = await mountTerminal()
    try {
      ed.insertText('ls -la')
      const classes = [...view.contentDOM.querySelectorAll<HTMLElement>('[class^="tok-"]')].map(
        (span) => span.className,
      )
      expect(classes).toContain('tok-command')
      expect(classes).toContain('tok-flag')
    } finally {
      teardown()
    }
  })
})

describe('recall overlay is actually wired (nocx-w7h.4)', () => {
  /** Dispatch a keydown exactly where a user's keystroke lands. */
  const key = (view: EditorView, init: KeyboardEventInit): void => {
    view.contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
    )
  }

  // Reachability + the acceptance that the v4 rule inverted (nocx-w7h.5):
  // navigating previews the command INTO the editor, and Enter executes what
  // you can see through the NORMAL submit path — the same one a typed Enter
  // takes — with nothing bypassed. The command paste and Enter reach the
  // session as ordered intents, so both are asserted: a second, parallel route
  // would look different.
  it('Enter in the recall overlay takes the command into the line and sends nothing', async () => {
    const { view, ed, content, teardown } = await mountTerminal(makeClipboard())
    const session = (content as unknown as { session: SessionFake }).session
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      // A real command through the real submit path populates the ledger.
      ed.show()
      ed.insertText('make deploy')
      key(view, { key: 'Enter' }) // the one legitimate send
      // The command paste and Enter are distinct intents, delivered in order
      // through the same queue; selecting a recall result below sends neither.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['paste', 'make deploy'],
          ['key', 'enter'],
        ]),
      )
      const sentBefore = session.intent.mock.calls.length

      // The submit cleared the editor; Up at the empty prompt opens recall.
      key(view, { key: 'ArrowUp' })
      expect(ed.root.querySelector('.ui-floating-panel[data-variant="recall"]')).not.toBeNull()
      // The recall query crosses the control plane (nocx-rtg0.13); the
      // preview lands when the answer does.
      await vi.waitFor(() => expect(ed.getDoc()).toBe('make deploy')) // previewing the only row

      key(view, { key: 'Enter' }) // takes the row — it does not run it
      // NOTHING went to the pty: the overlay closes with the command in the
      // line, and the next Enter — on a command the person can now read and
      // edit — is the one that runs it (the owner's reversal, 2026-08-19).
      expect(session.intent.mock.calls.length).toBe(sentBefore)
      expect(ed.getDoc()).toBe('make deploy')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })
  it('Up recalls the command from this pane, not another tab', async () => {
    const client = makeClient()
    const paneId = 'pane-a'
    client.call.mockImplementation((method: string, raw?: unknown) => {
      if (method !== 'history.query') return Promise.reject(new Error('unexpected control call'))
      const params = raw as { scope?: string; paneId?: string }
      const thisPane = params.scope === 'pane' && params.paneId === paneId
      return Promise.resolve({
        entries: (thisPane
          ? ['echo from pane a', 'echo from pane a middle', 'echo from pane a older']
          : ['echo from pane b']
        ).map((command, index) => ({
          id: thisPane ? `pane-a-command-${index}` : 'pane-b-command',
          command,
          cwd: FIXTURE_CWD,
          host: '',
          status: 'success',
          exitCode: 0,
          startedAt: 1_750_000_000_000,
          endedAt: 1_750_000_000_100,
          maskedCount: 0,
          maskedKinds: [],
        })),
        scope: thisPane ? 'pane' : 'directory',
        exhausted: true,
        source: 'store',
        coverage: null,
      })
    })
    const { view, ed, teardown } = await mountTerminal(
      makeClipboard(),
      {
        pane: anchoredPane(paneId),
      },
      client,
    )
    try {
      ed.show()
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() => expect(ed.getDoc()).toBe('echo from pane a'))
    } finally {
      teardown()
    }
  })
})

describe("the dropdown owns the arrows while it is open; recall's bare-Up gesture waits for it to close (nocx-mlm7)", () => {
  /** A profile client whose quick-connect assembly answers two hosts. */
  const hostsClient = (): ProfileClient => {
    const pc = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([
      {
        id: 'prof:ssh:pi',
        type: 'ssh',
        name: 'pi',
        options: { host: 'raspberry.local', user: 'pi' },
      },
      {
        id: 'prof:ssh:web',
        type: 'ssh',
        name: 'web-prod',
        options: { host: 'web-prod.example.com' },
      },
    ] satisfies SSHProfile[])
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue({ aliases: [], unavailable: null })
    return pc
  }

  /** A client whose control plane answers history rows; everything else is
   *  the no-store rejection, so no other path is accidentally fed. */
  const historyClient = (): ClientFake => {
    const client = makeClient()
    // The real client's call() is async; this fake matches its signature and
    // answers from constants, so it has nothing to await.
    // eslint-disable-next-line @typescript-eslint/require-await
    client.call.mockImplementation(async (method: string) => {
      if (method === 'history.query') {
        return {
          entries: [
            {
              id: 'h1',
              command: 'ssh pi@192.0.2.93',
              cwd: FIXTURE_CWD,
              host: '',
              status: 'success',
              exitCode: 0,
              startedAt: 1_750_000_000_000,
              endedAt: 1_750_000_000_100,
              maskedCount: 0,
              maskedKinds: [],
            },
            {
              id: 'h2',
              command: 'ssh prod',
              cwd: FIXTURE_CWD,
              host: '',
              status: 'success',
              exitCode: 0,
              startedAt: 1_750_000_000_000,
              endedAt: 1_750_000_000_100,
              maskedCount: 0,
              maskedKinds: [],
            },
          ],
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
        }
      }
      // fs.complete: the stale-path check answers "does not exist" — the
      // history rows are demoted, never hidden, so they still render.
      if (method === 'fs.complete') return { entries: [] }
      throw new Error('no store wired (fake)')
    })
    return client
  }

  const selectedRow = (ed: CommandEditor): HTMLElement | null =>
    ed.root.querySelector<HTMLElement>('.ui-floating-panel__row[data-selected="true"]')
  const recallPanel = (ed: CommandEditor): HTMLElement | null =>
    ed.root.querySelector<HTMLElement>('.ui-floating-panel[data-variant="recall"]')

  it('with the dropdown open under `ssh `, ArrowDown and ArrowUp move the dropdown selection and never open recall', async () => {
    const { view, ed, teardown } = await mountTerminal(
      makeClipboard(),
      {},
      historyClient(),
      hostsClient(),
    )
    try {
      ed.show()
      ed.insertText('ssh ')
      // Tab opens the dropdown — the user's own path to the surface.
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }),
      )
      // Hosts and history land over the profile client and the control
      // plane; the dropdown opens on its first results.
      await vi.waitFor(() => expect(selectedRow(ed)).not.toBeNull())
      const rows = () =>
        [...ed.root.querySelectorAll<HTMLElement>('.ui-floating-panel__row')].map(
          (r) => r.textContent ?? '',
        )
      expect(rows().length).toBeGreaterThanOrEqual(3) // hosts + history
      const first = selectedRow(ed)!.textContent ?? ''
      expect(recallPanel(ed)?.dataset.open).not.toBe('true')

      // ArrowDown: the DROPDOWN's selection moves — recall stays closed.
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }),
      )
      expect(selectedRow(ed)?.textContent).not.toBe(first)
      expect(recallPanel(ed)?.dataset.open).not.toBe('true')
      expect(ed.getDoc()).toBe('ssh ')

      // ArrowUp: the selection moves back; recall still closed. The
      // editor's up-at-top gesture must not fire while the dropdown owns
      // the key — this is the ownership this suite guards.
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
      expect(selectedRow(ed)?.textContent).toBe(first)
      expect(recallPanel(ed)?.dataset.open).not.toBe('true')
    } finally {
      teardown()
    }
  })

  it('with no dropdown open, ArrowUp at the top of a single-line draft still opens recall', async () => {
    const { view, ed, teardown } = await mountTerminal(makeClipboard())
    try {
      ed.show()
      ed.insertText('echo kept')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
      expect(recallPanel(ed)?.dataset.open).toBe('true')
    } finally {
      teardown()
    }
  })
})

describe('the terminal keyboard sends physical key intents', () => {
  it('preserves Shift+Enter as a key intent instead of xterm-encoded bytes', async () => {
    const { content, teardown } = await mountTerminal(makeClipboard())
    try {
      const session = sessionOf(content)
      const input = (content as unknown as { _intentInput: { element: HTMLTextAreaElement } })
        ._intentInput.element
      const event = new KeyboardEvent('keydown', {
        key: 'Enter',
        code: 'Enter',
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      })
      input.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(true)
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('key', 'Shift+enter'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

describe('clipboard paste into the live terminal uses session intents', () => {
  it.each([
    ['right-click', 'contextmenu', undefined],
    ['middle-click', 'mousedown', 1],
  ])('%s queues one paste intent instead of using xterm', async (_name, eventType, button) => {
    const clipboard = makeClipboard({ readText: vi.fn().mockResolvedValue('clipboard text') })
    const { content, teardown } = await mountTerminal(clipboard)
    try {
      const session = sessionOf(content)
      const target = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      target.dispatchEvent(
        eventType === 'contextmenu'
          ? new MouseEvent(eventType, { bubbles: true, cancelable: true })
          : new MouseEvent(eventType, { bubbles: true, cancelable: true, button }),
      )
      await vi.waitFor(() => expect(clipboard.readText).toHaveBeenCalledOnce())
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('paste', 'clipboard text'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

describe('copy-on-select follows the gesture, not the element under the pointer at its end', () => {
  // A command that finishes while a person is selecting its block freezes
  // the block and moves the layout under the pointer, so the gesture can end
  // over something that is not the scrollback. The copy belongs to the
  // gesture that began in the scrollback; where it ended does not change
  // what was selected. Measured in the e2e container: clipboard.spec's
  // copy-on-select failed 2 in 15 on main, each with the command completing
  // inside the triple-click.
  const mountBlock = async () => {
    const clipboard = makeClipboard()
    const mounted = await mountTerminal(clipboard, { attachToDocument: true })
    const scrollback = (mounted.content as unknown as { scrollback: ScrollbackController })
      .scrollback
    mounted.content.setVisible(true)
    const block = createCommandBlock(
      'command',
      1,
      'echo marker',
      '~',
      '',
      '<span class="term-line">marker-output</span>',
      10,
      0,
      'success',
      () => scrollback.scrollbackInner,
      () => {},
      new CommandSnapshotStore(),
      'shell',
    )
    scrollback.scrollbackInner.appendChild(block)
    const line = block.querySelector('.term-line')!
    const select = () => {
      const range = document.createRange()
      range.selectNodeContents(line)
      const selection = window.getSelection()!
      selection.removeAllRanges()
      selection.addRange(range)
    }
    return { ...mounted, clipboard, block, select }
  }

  it('copies when the gesture began on a block and ended outside the scrollback', async () => {
    const { clipboard, block, select, teardown } = await mountBlock()
    try {
      block.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      block.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      select()
      document.body.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
      document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
      expect(clipboard.writeText).toHaveBeenCalledTimes(1)
      expect(clipboard.writeText).toHaveBeenCalledWith('marker-output')
    } finally {
      teardown()
    }
  })

  it('copies once when the gesture began and ended on the block', async () => {
    const { clipboard, block, select, teardown } = await mountBlock()
    try {
      block.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      block.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      select()
      block.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
      block.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
      expect(clipboard.writeText).toHaveBeenCalledTimes(1)
      expect(clipboard.writeText).toHaveBeenCalledWith('marker-output')
    } finally {
      teardown()
    }
  })

  it('does not copy a standing selection again for a gesture that began elsewhere', async () => {
    const { clipboard, block, select, teardown } = await mountBlock()
    try {
      block.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      select()
      block.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
      clipboard.writeText.mockClear()
      // A click on something outside the scrollback that leaves the
      // selection standing: the person copied something else meanwhile, and
      // this click must not put the old selection back.
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      document.body.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
      document.body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
      expect(clipboard.writeText).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

describe('paste with focus on a frozen block (nocx-w7h.9)', () => {
  it('Cmd/Ctrl+V redirects to the editor, deselects the block, and never reaches the session', async () => {
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    const session = (content as unknown as { session: SessionFake }).session
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    try {
      content.setVisible(true)
      ed.show()
      // A real frozen block, appended to the real scrollback DOM. The
      // onSelect wires the same way BlockManager.freezeBlock wires it — into
      // the manager's selection state, so deselectBlocks() knows about it.
      const manager = scrollback.blockManager as unknown as {
        _onBlockSelected(id: number): void
        _onBlockDeselected(id: number): void
      }
      const block = createCommandBlock(
        'command',
        1,
        'ls',
        '~',
        '',
        '<span class="term-line">out</span>',
        10,
        0,
        'success',
        () => scrollback.scrollbackInner,
        (bid, sel) => {
          if (sel) manager._onBlockSelected(bid)
          else manager._onBlockDeselected(bid)
        },
        new CommandSnapshotStore(),
        'shell',
      )
      scrollback.scrollbackInner.appendChild(block)
      // Click the block (mousedown + mouseup without movement) → selected.
      block.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      block.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
      expect(block.classList.contains('cmd-block-selected')).toBe(true)

      const sentBefore = session.send.mock.calls.length
      // Cmd/Ctrl+V lands on the block, bubbles to the document-level rescue.
      const ev = new KeyboardEvent('keydown', {
        key: 'v',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      })
      block.dispatchEvent(ev)
      // The editor owns the paste now: focus moved, block deselected, and
      // nothing was sent to the session (jsdom cannot run the native paste;
      // the inserted text is verified in a real browser).
      expect(document.activeElement !== null && ed.root.contains(document.activeElement)).toBe(true)
      expect(block.classList.contains('cmd-block-selected')).toBe(false)
      expect(session.send.mock.calls.length).toBe(sentBefore)
      expect(ev.defaultPrevented).toBe(false) // native paste still runs
    } finally {
      teardown()
    }
  })
  it('Cmd/Ctrl+V leaves the pane intent input as the text owner', async () => {
    const { ed, content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    const session = sessionOf(content)
    const textarea = intentElementOf(content)
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('keep this draft')
      const live = tab.pane.querySelector<HTMLElement>('.terminal-live-container')
      expect(live).not.toBeNull()
      expect(live!.contains(textarea)).toBe(true)
      textarea.focus()
      expect(document.activeElement).toBe(textarea)

      const ev = new KeyboardEvent('keydown', {
        key: 'v',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      })
      textarea.dispatchEvent(ev)

      // The pane's own intent input remains the active owner. Ctrl+V is left
      // to the browser's native paste event rather than claimed as terminal bytes.
      expect(ev.defaultPrevented).toBe(false)
      expect(document.activeElement).toBe(textarea)
      expect(ed.getDoc()).toBe('keep this draft')
      expect(session.intent.mock.calls).toEqual([['focus', 'in']])
    } finally {
      textarea.remove()
      teardown()
    }
  })
})

describe('inserting a saved secret into the pane in front (nocx-fk32)', () => {
  /** A client whose vault.resolveLine answers with a resolved value. */
  function resolvingClient(value: string) {
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'vault.resolveLine') {
        return Promise.resolve({ line: value, refs: [{ name: 'pi@far', resolved: true }] })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    return client
  }

  it('sends the VALUE and Return as intents when the terminal owns input', async () => {
    const client = resolvingClient('hunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      // The terminal owns input — a raw password prompt, an ssh handshake.
      // There is no reference machinery on the other side of the pty, so
      // the value goes across, WITH its newline: choosing a secret at a
      // password prompt is the answer to that prompt (owner, 2026-08-10).
      editorOf(content).hide()
      const intentCountBefore = session.intent.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('value')
      await vi.waitFor(() => expect(session.intent.mock.calls.length).toBe(intentCountBefore + 2))
      expect(session.intent.mock.calls.slice(intentCountBefore)).toEqual([
        ['text', 'hunter2'],
        ['key', 'enter'],
      ])
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('never writes a value the vault could not resolve — the far side must not receive the reference text', async () => {
    const client = makeClient()
    client.call.mockImplementation((method: string, params: unknown) => {
      if (method === 'vault.resolveLine') {
        const line = (params as { line?: string })?.line ?? ''
        return Promise.resolve({ line, refs: [{ name: 'gone', resolved: false }] })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      const intentCountBefore = session.intent.mock.calls.length
      await expect(content.insertSecret('gone')).resolves.toBe('unavailable')
      expect(session.send.mock.calls.length).toBe(sentBefore)
      expect(session.intent.mock.calls.length).toBe(intentCountBefore)
    } finally {
      teardown()
    }
  })

  it('puts the REFERENCE in the draft when the editor owns the prompt, and resolves nothing', async () => {
    const client = resolvingClient('hunter2')
    const { content, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const ed = editorOf(content)
      ed.show()
      const session = sessionOf(content)
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('reference')
      // ADR-0021: a command carrying a reference moves to another machine
      // and resolves that machine's secret; a command carrying a pasted key
      // is both dead and dangerous. So the draft gets the reference...
      expect(view.state.doc.toString()).toContain('{{secret:pi@far}}')
      // ...the value is never in the draft...
      expect(view.state.doc.toString()).not.toContain('hunter2')
      // ...and nothing was resolved or sent: that waits for submit.
      expect(client.call).not.toHaveBeenCalledWith('vault.resolveLine', expect.anything())
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })
})
describe('saved-secret input preserves the single intent route', () => {
  // Written BEFORE the extraction and required to pass unchanged after it:
  // the extraction may change only WHERE the question 'who owns input in
  // this pane' is answered (into a private inputOwner()), never the
  // answers. These three branches are the whole current contract.

  /** The reference line insertSecret must ask the vault to resolve. */
  const REFERENCE = '{{secret:pi@far}}'

  /** A client whose vault.resolveLine answers with a resolved value. */
  function resolvingClient(value: string) {
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'vault.resolveLine') {
        return Promise.resolve({ line: value, refs: [{ name: 'pi@far', resolved: true }] })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    return client
  }

  it('with the editor owning the prompt, the REFERENCE enters the draft and nothing resolves or sends', async () => {
    const client = resolvingClient('hunter2')
    const { content, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).show()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('reference')
      expect(view.state.doc.toString()).toContain(REFERENCE)
      expect(view.state.doc.toString()).not.toContain('hunter2')
      expect(client.call).not.toHaveBeenCalledWith('vault.resolveLine', expect.anything())
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('with the terminal owning input, the VALUE and Return are structured intents', async () => {
    const client = resolvingClient('hunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const intentCountBefore = session.intent.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('value')
      // The vault is asked for exactly the reference line — nothing more.
      expect(client.call).toHaveBeenCalledWith('vault.resolveLine', { line: REFERENCE })
      await vi.waitFor(() => expect(session.intent.mock.calls.length).toBe(intentCountBefore + 2))
      // The resolved value and Return travel as two typed intents. The runtime
      // supplies the mode-aware key encoding; the frontend never appends a
      // newline byte to a raw write.
      expect(session.intent.mock.calls.slice(intentCountBefore)).toEqual([
        ['text', 'hunter2'],
        ['key', 'enter'],
      ])
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('with no editor and no session the insert is unavailable and nothing is sent', async () => {
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {},
      resolvingClient('hunter2'),
    )
    const session = sessionOf(content)
    const withSession = content as unknown as { session: SessionFake | null }
    const original = withSession.session
    withSession.session = null
    try {
      const sentBefore = session.send.mock.calls.length
      const intentCountBefore = session.intent.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('unavailable')
      expect(session.send.mock.calls.length).toBe(sentBefore)
      expect(session.intent.mock.calls.length).toBe(intentCountBefore)
    } finally {
      withSession.session = original
      teardown()
    }
  })
})
describe('firing a snippet into the pane in front (nocx-xqu5)', () => {
  /** The refusal the palette must render when nothing owns input (§9.2). */
  const NO_OWNER: SnippetFire = { ok: false, reason: 'no-owner' }

  /** A client whose vault.resolveLine answers with a resolved value. */
  function resolvingClient(value: string) {
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'vault.resolveLine') {
        return Promise.resolve({ line: value, refs: [{ name: 'pi@far', resolved: true }] })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    return client
  }

  it('into the editor: the text enters the draft, no newline is appended, nothing resolves or sends', async () => {
    const client = resolvingClient('hunter2')
    const { content, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).show()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('git push --force')).resolves.toEqual({
        ok: true,
        where: 'editor',
      })
      // Exactly the body, nothing appended: the user submits with Enter.
      expect(view.state.doc.toString()).toBe('git push --force')
      // The secret policy resolves into the pty only; into the editor a
      // {{secret:…}} stays a reference for the chip and submit (§11.1), so
      // nothing touches the vault or the session.
      expect(client.call).not.toHaveBeenCalledWith('vault.resolveLine', expect.anything())
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('into the pty: {{secret:…}} resolves exactly as insertSecret resolves it, and NO newline is sent', async () => {
    const client = resolvingClient('run hunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('run {{secret:pi@far}}')).resolves.toEqual({
        ok: true,
        where: 'pty',
      })
      // The vault is asked for exactly the snippet body.
      expect(client.call).toHaveBeenCalledWith('vault.resolveLine', {
        line: 'run {{secret:pi@far}}',
      })
      // The resolved body remains one paste intent (no newline); the runtime
      // applies the program's current paste mode.
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('paste', 'run hunter2'))
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('refuses an unresolved {{secret:…}} and names it, pasting nothing', async () => {
    const client = makeClient()
    client.call.mockImplementation((method: string, params: unknown) => {
      if (method === 'vault.resolveLine') {
        let line = ''
        if (params !== null && typeof params === 'object' && 'line' in params) {
          const candidate = params.line
          if (typeof candidate === 'string') line = candidate
        }
        return Promise.resolve({ line, refs: [{ name: 'gone', resolved: false }] })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('fire {{secret:gone}}')).resolves.toEqual({
        ok: false,
        reason: 'unresolved-secret',
        name: 'gone',
      })
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('a vault that FAILS the call refuses the fire — it does not escape as a rejection', async () => {
    // The paired case for the test above: "unresolved" is an answer, and
    // this is no answer at all (the vault is sealed, the socket is gone,
    // the method is not wired). Nothing was written either way, and the
    // palette must get a refusal it can render — an exception here would
    // leave its panel waiting on a promise that never settles.
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'vault.resolveLine') return Promise.reject(new Error('vault is sealed'))
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('fire {{secret:pi@far}}')).resolves.toEqual({
        ok: false,
        reason: 'unresolved-secret',
      })
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it("'none' is a refusal, never a fallthrough: with no session the fire stops and the pty is untouched", async () => {
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {},
      resolvingClient('hunter2'),
    )
    const session = sessionOf(content)
    const withSession = content as unknown as { session: SessionFake | null }
    const original = withSession.session
    withSession.session = null
    try {
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('echo hi')).resolves.toEqual(NO_OWNER)
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      withSession.session = original
      teardown()
    }
  })

  it('uses the latest accepted backend bracketed-paste mode', async () => {
    const client = resolvingClient('line1\nhunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const frame = screenFrame(1, [])
      frame.state.bracketedPaste = true
      session.fireScreenFrame(frame)

      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: true,
        where: 'pty',
      })
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('paste', 'line1\nhunter2'))
    } finally {
      teardown()
    }
  })

  it('refuses multi-line paste when the latest accepted backend mode is disabled', async () => {
    const client = resolvingClient('line1\nhunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const frame = screenFrame(1, [])
      frame.state.bracketedPaste = false
      session.fireScreenFrame(frame)
      const sentBefore = session.send.mock.calls.length

      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: false,
        reason: 'multi-line-no-bracketed-paste',
      })
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('a multi-line body is refused when the destination has no bracketed paste — before any resolution or write', async () => {
    const client = resolvingClient('line1\nhunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: false,
        reason: 'multi-line-no-bracketed-paste',
      })
      // The refusal is about the destination, so it is decided before the
      // vault is asked and before the engine is touched (§9.4).
      expect(client.call).not.toHaveBeenCalledWith('vault.resolveLine', expect.anything())
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('a multi-line body is delivered WHOLE — resolved and newline intact — when bracketed paste is on', async () => {
    const client = resolvingClient('line1\nhunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const frame = screenFrame(1, [])
      frame.state.bracketedPaste = true
      session.fireScreenFrame(frame)
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: true,
        where: 'pty',
      })
      expect(client.call).toHaveBeenCalledWith('vault.resolveLine', {
        line: 'line1\n{{secret:pi@far}}',
      })
      // One document: resolved, newline intact, nothing appended (§9.4).
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('paste', 'line1\nhunter2'))
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      teardown()
    }
  })

  it('a single-line body is unaffected by the paste mode either way', async () => {
    for (const active of [false, true]) {
      const client = resolvingClient('echo hunter2')
      const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
      try {
        editorOf(content).hide()
        const frame = screenFrame(1, [])
        frame.state.bracketedPaste = active
        sessionOf(content).fireScreenFrame(frame)
        await expect(content.insertSnippet('echo {{secret:pi@far}}')).resolves.toEqual({
          ok: true,
          where: 'pty',
        })
        await vi.waitFor(() =>
          expect(sessionOf(content).intent).toHaveBeenCalledWith('paste', 'echo hunter2'),
        )
      } finally {
        teardown()
      }
    }
  })

  it('a multi-line body into the editor is fine: the refusal is about the pty, not the text', async () => {
    const { content, view, teardown } = await mountTerminal(
      makeClipboard(),
      {},
      resolvingClient('a\nb'),
    )
    try {
      editorOf(content).show()
      await expect(content.insertSnippet('a\nb')).resolves.toEqual({ ok: true, where: 'editor' })
      expect(view.state.doc.toString()).toBe('a\nb')
    } finally {
      teardown()
    }
  })

  it('snippet paste is queued as a session intent, not written by xterm', async () => {
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {},
      resolvingClient('echo hunter2'),
    )
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      await expect(content.insertSnippet('echo {{secret:pi@far}}')).resolves.toEqual({
        ok: true,
        where: 'pty',
      })
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('paste', 'echo hunter2'))
    } finally {
      teardown()
    }
  })
})

describe('the snippet palette chord (nocx-jj77)', () => {
  /** Dispatch a keydown exactly where a user's keystroke lands. */
  const key = (view: EditorView, init: KeyboardEventInit): void => {
    view.contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
    )
  }
  const CHORD = { key: 'p', code: 'KeyP', altKey: true, metaKey: true }

  it('the editor arbiter delegates the chord to the ONE opener (hooks.onSnippetChord)', async () => {
    const onSnippetChord = vi.fn()
    const { view, teardown } = await mountTerminal(makeClipboard(), { hooks: { onSnippetChord } })
    try {
      key(view, CHORD)
      expect(onSnippetChord).toHaveBeenCalledTimes(1)
      // The neighbouring chord (⌥⌘O) is NOT the snippet chord: the arbiter
      // lets it fall through to the editor.
      key(view, { key: 'o', code: 'KeyO', altKey: true, metaKey: true })
      expect(onSnippetChord).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })

  it("the grid's input element delegates the chord to the SAME opener, and no intent goes out", async () => {
    const onSnippetChord = vi.fn()
    const { content, teardown } = await mountTerminal(makeClipboard(), {
      hooks: { onSnippetChord },
    })
    try {
      const session = sessionOf(content)
      session.intent.mockClear()
      const element = intentElementOf(content)
      element.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'p',
          code: 'KeyP',
          altKey: true,
          metaKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      expect(onSnippetChord).toHaveBeenCalledTimes(1)
      // Consumed, not typed: the chord opens the palette and NO intent was
      // sent to the session for it — a key the pane owns is not input.
      expect(session.intent).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  // The former XtermRenderer chord subscription is obsolete: the grid's
  // actual intent input and the editor arbiter above now share the opener.
  // The input-surface acceptance test above covers the user-visible route.

  it("the chord closes the pane's own floating surfaces before opening the palette", async () => {
    const onSnippetChord = vi.fn()
    const { content, ed, teardown } = await mountTerminal(makeClipboard(), {
      hooks: { onSnippetChord },
      attachToDocument: true,
    })
    try {
      ed.show()
      // Open recall over the editor, then press the chord: recall must be
      // dismissed (the surfaces never stack) before the opener runs.
      const recall = recallOf(content)
      ed.insertText('ssh')
      // The recall shortcut opens it.
      const view = viewOf(ed)
      key(view, { key: 'r', metaKey: true })
      expect(recall.isOpen).toBe(true)
      key(view, CHORD)
      expect(recall.isOpen).toBe(false)
      expect(onSnippetChord).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })
})

describe('the snippet env view (nocx-jj77)', () => {
  it("exposes the active domain's raw cwd/host/user, or null with no session", async () => {
    const { content, teardown } = await mountTerminal()
    try {
      const env = content.snippetEnv()
      expect(env).not.toBeNull()
      expect(env!.cwd).toBe(FIXTURE_CWD)
      expect(typeof env!.host).toBe('string')
      expect(typeof env!.user).toBe('string')
    } finally {
      teardown()
    }
  })

  it('answers null when no session owns the pane', async () => {
    const { content, teardown } = await mountTerminal()
    const withSession = content as unknown as { session: SessionFake | null }
    const original = withSession.session
    withSession.session = null
    try {
      expect(content.snippetEnv()).toBeNull()
    } finally {
      withSession.session = original
      teardown()
    }
  })
})

describe('vault references in the prompt (ADR-0021, the renderer half)', () => {
  it('an unresolved reference is NOT sent: the draft stays and the editor stays up', async () => {
    // The real submit seam: Enter -> beforeSubmit -> planSubmit ->
    // vault.resolveLine -> the editor keeps the draft on a refusal.
    const client = makeClient()
    const callMock = client.call
    callMock.mockImplementation((method: string, params: unknown) => {
      if (method === 'vault.resolveLine') {
        const req = params as { line?: string }
        const line = typeof req?.line === 'string' ? req.line : ''
        return Promise.resolve({ line, refs: [{ name: 'nope', resolved: false }] })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const withSession = content as unknown as { session: SessionFake }
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('curl {{secret:nope}} https://api')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The verdict is async (references resolve over the wire): drain the
      // chain, then assert the refusal.
      for (let i = 0; i < 5; i++) await Promise.resolve()
      expect(withSession.session.send).not.toHaveBeenCalled()
      // The draft is intact and the editor is still up — nothing was lost.
      expect(ed.getDoc()).toBe('curl {{secret:nope}} https://api')
      expect(ed.isVisible).toBe(true)
    } finally {
      teardown()
    }
  })
})

describe('the live prompt says where Enter will land (nocx-3779)', () => {
  /**
   * Mount a real TerminalContent over an SSH session (alias path: no saved
   * profile, host+user resolved through ~/.ssh/config), the same mount a
   * real SSH tab takes. The chip assertions below must pass through this
   * seam — a pure editor test could not catch a second derivation of the
   * host string.
   */
  async function mountSshTerminal(): Promise<{
    ed: CommandEditor
    content: TerminalContent
    tab: Pane
    teardown: () => void
  }> {
    const clientFake = makeClient({
      openSSHSessionByHost: vi.fn(() => Promise.resolve(makeSession())),
    })
    const wsClient = clientFake as unknown as WSClient
    const content = new TerminalContent(
      wsClient,
      anchoredPane(),
      makeClipboard(),
      new ClipboardGate(),
      makeBanner(),
      null,
      () => {},
      { profileId: '', host: '192.0.2.57', user: 'root' },
    )
    const tab = new Pane(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
      'tab-99',
    )
    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)
    document.body.append(paneParent)
    await tab.start()
    await expect(content.ready).resolves.toBe(true)
    return {
      ed: editorOf(content),
      content,
      tab,
      teardown: () => {
        tab.close()
        paneParent.remove()
      },
    }
  }

  /** jsdom lacks scrollTo/scrollIntoView; the scrollback controller calls
   *  both when blocks are created and the layout changes. */

  it('an SSH prompt shows the location the block header would carry, strong in the context (spec §5.2)', async () => {
    const { content, tab, teardown } = await mountSshTerminal()
    try {
      content.setVisible(true)
      // The context is fed at session open from ONE derivation
      // (this.locationLine()); no stream marker may change it (ADR-0024 §1).
      const strong = tab.pane.querySelector('.nocx-editor-context [data-emphasis="strong"]')
      expect(strong?.textContent).toBe('root@192.0.2.57')
      // The block header never appears in the severed product: blocks are
      // a completion projection with no stream (or app) trigger.
      expect(tab.pane.querySelector('.cmd-header-meta')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('a local session names no host at all', async () => {
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      content.setVisible(true)
      expect(tab.pane.querySelector('.nocx-editor-context [data-emphasis="strong"]')).toBeNull()
    } finally {
      teardown()
    }
  })
})

describe('the recovery action chip in editor chrome (nocx-atyf.2)', () => {
  /** A client whose SSH open session carries the given destination mode.
   *  The refusal reason no longer rides the ack (nocx-dvql) — it arrives on
   *  session.integrationChanged, which the integration tests drive. */
  const clientWithPolicy = (desiredMode: DesiredMode): ClientFake => {
    const client = makeClient({
      openSSHSession: vi.fn(() => {
        const s = makeSession({ desiredMode })
        client._sessions.push(s)
        return Promise.resolve(s)
      }),
    })
    return client
  }

  const SSH = { profileId: 'ssh:test:1', host: 'test-host' }

  const recoveryLabel = (content: TerminalContent): string | null => {
    const withEditor = content as unknown as { editor: { root: HTMLElement } }
    const el = withEditor.editor.root.querySelector<HTMLElement>('[data-control="recovery"]')
    if (!el || el.style.display === 'none') return null
    return el.textContent
  }

  it('the healthy state shows nothing in the editor chrome', async () => {
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { ssh: SSH },
      clientWithPolicy('script'),
    )
    try {
      content.setVisible(true)
      // No markers yet: unsupported shell, no recovery needed.
      expect(recoveryLabel(content)).toBeNull()

      // A backend-accepted prompt boundary is the integration signal; the
      // client no longer parses shell markers.
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['$ prompt']))
      session.fireEffect({
        sessionId: session.sessionId,
        generation: '1',
        effectId: '1',
        kind: 'promptBoundary',
        screenRevision: 1,
        title: '',
        body: '',
      })

      // Healthy state: no recovery action shown.
      expect(recoveryLabel(content)).toBeNull()
    } finally {
      teardown()
    }
  })

  it('a launcher decline on a script profile leaves the mode script — the decline is a separate axis', async () => {
    const client = clientWithPolicy('script')
    const { content, teardown } = await mountTerminal(makeClipboard(), { ssh: SSH }, client)
    try {
      content.setVisible(true)
      // The decline arrives on session.integrationChanged now (nocx-dvql),
      // and it does NOT rewrite the resolved destination mode: the mode is
      // what the connection asked for, the status is what happened.
      integrationHandler(client)({
        sessionId: client._sessions[0].sessionId,
        status: 'conventional',
        reason: 'unsupported-shell',
        shell: 'auto',
      })
      expect(content.policy).toBe('script')
    } finally {
      teardown()
    }
  })

  it('the nocx-capability-rail element is gone', async () => {
    const { tab, content, teardown } = await mountTerminal(
      makeClipboard(),
      { ssh: SSH },
      clientWithPolicy('script'),
    )
    try {
      content.setVisible(true)
      const rail = tab.pane.querySelector('.nocx-capability-rail')
      expect(rail).toBeNull()
    } finally {
      teardown()
    }
  })
})

// The pane's waiting state for the `starting` interval (nocx-ui8q6.1):
// ADR-0024 decision 8's "worst of both" — a raw, half-bootstrapped prompt a
// keystroke could land on before the shell has proved itself either way.
// These assert through the seams a user actually reaches (AGENTS.md rule 1):
// the send seam a keystroke reaches or does not, and the DOM a person would
// see — never the implementation's own bookkeeping.
describe('the pane while shell integration is starting (nocx-ui8q6.1)', () => {
  const WAITING = '.nocx-integration-waiting'

  it('shows the waiting state and no terminal grid while the axis is starting', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      integrationHandler(client)({
        sessionId: client._sessions[0].sessionId,
        status: 'starting',
        shell: '/bin/zsh',
      })
      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(editorOf(content).isVisible).toBe(true)
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('none')
    } finally {
      teardown()
    }
  })

  it('shows the running block while the integration status is still starting', async () => {
    const session = makeSession({ awaitsIntegration: true })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const fireIntegration = integrationHandler(client, session)
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const lifecycle = lifecycleHandler(client, session.sessionId)
    const scrollback = scrollbackFor(content)
    try {
      fireIntegration({ sessionId: session.sessionId, status: 'starting', shell: '/bin/bash' })
      expect(scrollback.scrollbackLayout.style.display).toBe('none')

      lifecycle({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      lifecycle({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-startup', state: 'open', origin: 'shell', command: 'sleep 20' },
      })

      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(scrollback.scrollbackLayout.style.display).toBe('')
      expect(scrollback.blockManager.runningBlock?.command).toBe('sleep 20')
    } finally {
      teardown()
    }
  })

  it('holds a submitted command until prompt readiness, then attempts before writing it', async () => {
    const session = makeSession({ awaitsIntegration: true })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const fireIntegration = integrationHandler(client, session)
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    content.setVisible(true)
    const lifecycle = lifecycleHandler(client, session.sessionId)
    const editor = editorOf(content)
    const view = editorViewOf(editor)
    const scrollback = scrollbackFor(content)
    try {
      fireIntegration({ sessionId: session.sessionId, status: 'starting', shell: '/bin/bash' })
      session.intent.mockClear()
      session.send.mockClear()
      client.dispatcher.call.mockClear()

      editor.insertText('sleep 20')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

      // Enter during startup keeps the draft intact; neither a paste nor a
      // newline may reach the shell before prompt_ready.
      expect(view.state.doc.toString()).toBe('sleep 20')
      expect(session.intent).not.toHaveBeenCalled()
      expect(session.send).not.toHaveBeenCalled()
      expect(client.dispatcher.call).not.toHaveBeenCalled()

      lifecycle({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['paste', 'sleep 20'],
          ['key', 'enter'],
        ]),
      )

      const attemptCalls = client.dispatcher.call.mock.calls.filter(
        (call) => call[0] === 'lifecycle.submitAttempt',
      )
      expect(attemptCalls).toHaveLength(1)
      const attemptIndex = client.dispatcher.call.mock.calls.findIndex(
        (call) => call[0] === 'lifecycle.submitAttempt',
      )
      expect(attemptIndex).toBeGreaterThanOrEqual(0)
      const attempt = client.dispatcher.call.mock.calls[attemptIndex][1] as {
        command: string
        submitId: string
      }
      expect(attempt.command).toBe('sleep 20')
      expect(client.dispatcher.call.mock.invocationCallOrder[attemptIndex]).toBeLessThan(
        session.intent.mock.invocationCallOrder[0],
      )

      lifecycle({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-0',
          state: 'open',
          origin: 'app',
          submitId: attempt.submitId,
          command: 'sleep 20',
        },
      })
      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(scrollback.blockManager.runningBlock?.command).toBe('sleep 20')
      expect(scrollback.scrollbackLayout.style.display).toBe('')
    } finally {
      teardown()
    }
  })

  // nocx-xn63t.6.27, CI run 38577867125: a command typed into the startup
  // composer of a reconnected ssh session reached the shell as
  // `echo after-the-reconnec`. The integration status reaches `integrated` a
  // few milliseconds BEFORE the lifecycle publishes the first prompt_ready,
  // and in that interval the composer was taken off the screen and blurred —
  // the keystroke typed into it went to <body> and was gone, while the rest of
  // the draft survived and was submitted at the prompt. The composer the
  // person is typing into has to stay theirs until the prompt it is waiting
  // for has arrived.
  it('keeps the composer, its focus and a held Enter across integrated-before-prompt_ready', async () => {
    const session = makeSession({ awaitsIntegration: true })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const fireIntegration = integrationHandler(client, session)
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    content.setVisible(true)
    const lifecycle = lifecycleHandler(client, session.sessionId)
    const editor = editorOf(content)
    const view = editorViewOf(editor)
    const editorHasFocus = (): boolean => editor.rootContains(document.activeElement)
    try {
      fireIntegration({ sessionId: session.sessionId, status: 'starting', shell: '/bin/bash' })
      lifecycle({ lane: 'lane-1', lifecycle: 'native' })
      editor.insertText('echo after-the-reconnec')
      expect(editorHasFocus()).toBe(true)

      // The integration is proven; the first prompt is still on its way.
      fireIntegration({ sessionId: session.sessionId, status: 'integrated', shell: '/bin/bash' })
      session.intent.mockClear()
      session.send.mockClear()
      client.dispatcher.call.mockClear()

      // The next key the person types lands in the draft they are typing.
      expect(editor.isVisible).toBe(true)
      expect(editorHasFocus()).toBe(true)
      editor.insertText('t')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // Enter before the prompt is held exactly as it is while `starting`.
      expect(view.state.doc.toString()).toBe('echo after-the-reconnect')
      expect(session.intent).not.toHaveBeenCalled()

      lifecycle({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // The grid takes the keyboard at the submit (a `focus` intent, since
      // this pane is attached to the document); what reaches the shell is
      // the whole command, then Enter.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.filter((call) => call[0] !== 'focus')).toEqual([
          ['paste', 'echo after-the-reconnect'],
          ['key', 'enter'],
        ]),
      )
      expect(
        client.dispatcher.call.mock.calls.filter((call) => call[0] === 'lifecycle.submitAttempt'),
      ).toHaveLength(1)
    } finally {
      teardown()
    }
  })

  it('releases a held shell submit as a conventional write when startup settles degraded', async () => {
    const session = makeSession({ awaitsIntegration: true })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const fireIntegration = integrationHandler(client, session)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    content.setVisible(true)
    const editor = editorOf(content)
    const view = editorViewOf(editor)
    try {
      fireIntegration({ sessionId: session.sessionId, status: 'starting', shell: '/bin/bash' })
      session.intent.mockClear()
      session.send.mockClear()
      client.dispatcher.call.mockClear()

      editor.insertText('echo conventional')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(view.state.doc.toString()).toBe('echo conventional')
      expect(session.intent).not.toHaveBeenCalled()

      fireIntegration({
        sessionId: session.sessionId,
        status: 'conventional',
        reason: 'unsupported-shell',
        shell: '/bin/bash',
      })
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['paste', 'echo conventional'],
          ['key', 'enter'],
        ]),
      )
      expect(
        client.dispatcher.call.mock.calls.some((call) => call[0] === 'lifecycle.submitAttempt'),
      ).toBe(false)
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('a key pressed while starting reaches no send path', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      integrationHandler(client)({
        sessionId: client._sessions[0].sessionId,
        status: 'starting',
        shell: '/bin/zsh',
      })
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'x')
      expect(session.intent).not.toHaveBeenCalled()
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('the axis reaching integrated reveals the terminal and takes the next keypress', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      const sessionId = client._sessions[0].sessionId
      integrationHandler(client)({ sessionId, status: 'starting', shell: '/bin/zsh' })
      integrationHandler(client)({ sessionId, status: 'integrated', shell: '/bin/zsh' })

      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')

      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'y')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'y'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('the axis reaching conventional reveals a working terminal, takes input, and still says why', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const sessionId = client._sessions[0].sessionId
      integrationHandler(client)({ sessionId, status: 'starting', shell: '/bin/zsh' })
      integrationHandler(client)({
        sessionId,
        status: 'conventional',
        reason: 'unsupported-shell',
        shell: '/bin/zsh',
      })

      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')
      // The reason surface is a different axis from the waiting state and
      // must keep working: `conventional` still raises the degraded-session
      // card that says why there are no command blocks.
      expect(tab.pane.querySelector('.nocx-integration-notice')).not.toBeNull()

      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'z')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'z'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('a session that never emits on the axis is never made to wait', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(tab.pane.querySelector(WAITING)).toBeNull()
      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'a')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'a'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('a reattach that replays an already-integrated status never flashes the waiting state', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // The backend replays the CURRENT status on attach (replayIntegration)
      // rather than restarting the handshake at `starting` — reattach is a
      // state, not an event, and this pane must never have seen `starting`
      // at all for this to be a faithful reattach.
      integrationHandler(client)({
        sessionId: client._sessions[0].sessionId,
        status: 'integrated',
        shell: '/bin/zsh',
      })
      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')
    } finally {
      teardown()
    }
  })

  it('lost is not starting: a session that integrated and lost its channel keeps its terminal and input', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const sessionId = client._sessions[0].sessionId
      integrationHandler(client)({ sessionId, status: 'integrated', shell: '/bin/zsh' })
      integrationHandler(client)({
        sessionId,
        status: 'lost',
        reason: 'channel-lost',
        shell: '/bin/zsh',
      })

      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')

      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'b')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'b'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  // The gap this bead closes (nocx-ui8q6.6): the open ack's own
  // awaitsIntegration answers, before the FIRST session.integrationChanged
  // can possibly have arrived (it is sent strictly after the ack, AD-7),
  // whether this pane's grid should already be held. Without it, `_integration`
  // is null from mount until that first fact and null read as "conventional
  // by design" — the exact frame nocx-ui8q6.1 could not close, because it
  // only ever acted on a fact that had not arrived yet.
  it('a session the ack says will start `starting` never shows a live grid, even before the first fact', async () => {
    const session = makeSession({ awaitsIntegration: true })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const fireIntegration = integrationHandler(client, session)
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // No integrationHandler call yet — this is the ack's own promise
      // acted on alone, before any session.integrationChanged exists.
      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('none')
      // Hostile OSC 133 B cannot forge authenticated integration.
      session.fireEffect({
        sessionId: session.sessionId,
        generation: '1',
        effectId: '99',
        kind: 'promptBoundary',
        title: '',
        body: '',
      })
      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(content.shellState).not.toBe('integrated')
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'x')
      expect(session.intent).not.toHaveBeenCalled()
      expect(session.send).not.toHaveBeenCalled()

      // The fact the ack promised, now arriving: nothing about the pane's
      // state changes, because it was already waiting on it.
      fireIntegration({
        sessionId: session.sessionId,
        status: 'starting',
        shell: '/bin/zsh',
      })
      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('none')

      fireIntegration({
        sessionId: session.sessionId,
        status: 'integrated',
        shell: '/bin/zsh',
      })
      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'y')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'y'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  // The other half of the same acceptance: an ack that says false must not
  // wait either, whatever a stray fact says later this test does not send —
  // the case this bead's brief names as most likely to regress.
  it('a session the ack says never entered the axis is never made to wait, ack alone', async () => {
    const session = makeSession({ awaitsIntegration: false })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'z')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'z'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  // nocx-ty0hc: the RECLAIM half of the same gap. handleAttach's ack, like
  // handleOpen's, precedes replayIntegration's resend (AD-7), and a reclaimed
  // pane reads awaitsIntegration off that attach ack through the identical
  // ADOPTION seam (TerminalContentHooks.adoptSession) a restored tab actually
  // takes — not a fresh open. Before this bead, ipc.ts's reclaimSession never
  // asked the question at all and SessionHandle defaulted the field to
  // `false`, so a reclaimed `starting` session showed a live grid from its
  // very first frame, unconditionally.
  it('a reclaimed pane whose attach ack says starting never shows a live grid, even before the first fact', async () => {
    const client = makeClient()
    const session = makeSession({ awaitsIntegration: true })
    const fireIntegration = integrationHandler(client, session)
    const hooks = { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) }
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), { hooks }, client)
    try {
      // No integrationHandler call yet — the attach ack's own promise acted
      // on alone, before any session.integrationChanged exists.
      expect(tab.pane.querySelector(WAITING)).not.toBeNull()
      expect(editorOf(content).isVisible).toBe(true)
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('none')
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'x')
      expect(session.intent).not.toHaveBeenCalled()
      expect(session.send).not.toHaveBeenCalled()

      fireIntegration({
        sessionId: session.sessionId,
        status: 'integrated',
        shell: '/bin/zsh',
      })
      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'y')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'y'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  // The assertion the bead's brief names as most likely to break something
  // else, written first: a reclaim whose session never asked for integration
  // — the ordinary shape of a raw-mode pane taken back on restart — must show
  // its terminal immediately. reclaimSession must never invent a `true` for
  // a session with nothing to wait on, the way a shared default easily could.
  it('a reclaimed pane whose session never asked for integration shows its terminal immediately', async () => {
    const client = makeClient()
    const session = makeSession({ awaitsIntegration: false })
    const hooks = { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) }
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), { hooks }, client)
    try {
      expect(tab.pane.querySelector(WAITING)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.style.display).toBe('')
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'z')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'z'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

// The bundle's delivery outcome is its own fact on the wire
// (session.publishFailed, contracts/session.publishFailed.schema.json): a
// copy that did not land is not a refusal, so it cannot ride the integration
// axis, and it must not be confined to a log — the soft degrade AGENTS.md
// refuses by name.
describe('a pane whose integration bundle was not copied says so (nocx-1kejz.4)', () => {
  const noticeIn = (tab: { pane: HTMLElement }): Element | null =>
    tab.pane.querySelector('.nocx-publish-notice')

  const failed = (sessionId: string, reason: string): Record<string, unknown> => ({
    sessionId,
    instanceId: 'instance-1',
    sessionEpoch: 1,
    reason,
  })

  it('states what failed and what the tab is doing instead, above the terminal', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(noticeIn(tab)).toBeNull()

      publishFailedHandler(client)(failed(client._sessions[0].sessionId, 'home-unavailable'))

      const notice = noticeIn(tab)
      expect(notice).not.toBeNull()
      expect(tab.pane.querySelectorAll('.nocx-publish-notice')).toHaveLength(1)
      expect(notice!.querySelector('.ui-status-card__title')?.textContent).toBe(
        'Shell integration was not copied',
      )
      const description = notice!.querySelector('.ui-status-card__desc')?.textContent ?? ''
      expect(description).toContain('home directory')
      expect(description).toContain('whatever copy of the integration the host already had')
      // …and the terminal is still there, below the card: a message about the
      // session must not hide the session.
      expect(
        notice!.compareDocumentPosition(scrollbackFor(content).scrollbackLayout) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy()
    } finally {
      teardown()
    }
  })

  // The bound, and the reason it is needed: a host that refuses every copy
  // reports a failure per attempt, and a pane that retried a handful of times
  // must not answer with a wall of cards (the rule nocx-z5lnt set for repeated
  // live-loss notices).
  it('keeps one statement when the same failure repeats or its cause changes', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const sessionId = client._sessions[0].sessionId
      publishFailedHandler(client)(failed(sessionId, 'write-refused'))
      publishFailedHandler(client)(failed(sessionId, 'write-refused'))
      publishFailedHandler(client)(failed(sessionId, 'connection-failed'))

      expect(tab.pane.querySelectorAll('.nocx-publish-notice')).toHaveLength(1)
      expect(tab.pane.querySelectorAll('.ui-status-card')).toHaveLength(1)
      const description = noticeIn(tab)!.querySelector('.ui-status-card__desc')?.textContent ?? ''
      expect(description).toContain('did not stay up')
      expect(description).not.toContain('refused the files')
    } finally {
      teardown()
    }
  })

  // The other half of the regression: a publish that landed says nothing at
  // all. Healthy ssh panes are the overwhelming majority, and a card on each
  // of them would be a card nobody reads.
  it('says nothing when the bundle was copied', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // The only thing the backend sends on success is silence; the axis fact
      // that follows is a different fact and must not raise this card.
      integrationHandler(client)({
        sessionId: client._sessions[0].sessionId,
        status: 'integrated',
        shell: '/bin/zsh',
      })
      expect(noticeIn(tab)).toBeNull()
      expect(scrollbackFor(content).scrollbackLayout.textContent).not.toContain(
        'Shell integration was not copied',
      )
    } finally {
      teardown()
    }
  })

  it('leaves the pane usable: the keys still reach the session', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      publishFailedHandler(client)(failed(client._sessions[0].sessionId, 'write-refused'))
      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'l', 's')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'l'))
      expect(session.intent).toHaveBeenCalledWith('text', 's')
      expect(session.send).not.toHaveBeenCalled()
      expect(noticeIn(tab)).not.toBeNull()
    } finally {
      teardown()
    }
  })

  // The fact is replayed on every attach (transport's replayPublishFailure),
  // which is what makes a reloaded window learn it at all. It must therefore
  // not resurrect a card the person closed: a replay is not news.
  it('does not bring back a card the person closed', async () => {
    const client = makeClient()
    // In the document: the kit's buttons take their clicks through Solid's
    // delegated listener, which is on the document, so a pane outside it can
    // be rendered but not pressed.
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      const sessionId = client._sessions[0].sessionId
      publishFailedHandler(client)(failed(sessionId, 'write-refused'))
      tab.pane
        .querySelector<HTMLButtonElement>('.nocx-publish-notice button[aria-label="Dismiss"]')
        ?.click()
      expect(noticeIn(tab)).toBeNull()

      publishFailedHandler(client)(failed(sessionId, 'write-refused'))
      expect(noticeIn(tab)).toBeNull()
    } finally {
      teardown()
    }
  })
})

// One input authority remains after the xterm cutover (ADR-0066): renderer
// output cannot become PTY input, while a person's key and committed text
// arrive as structured session intents.
describe('terminal input uses session intents, never renderer bytes', () => {
  it('ignores renderer onData and forwards the person’s actions as intents', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      session.send.mockClear()
      session.intent.mockClear()

      // This is the old xterm protocol-reply/input seam. It has no route to the
      // session; only the input element below can submit a user's action.
      session.fireData('backend data-plane bytes')
      expect(session.send).not.toHaveBeenCalled()
      expect(session.intent).not.toHaveBeenCalled()

      typeIntoPane(content, 'ls', '\r')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledTimes(3))

      expect(session.intent.mock.calls).toEqual([
        ['text', 'l'],
        ['text', 's'],
        ['key', 'enter'],
      ])
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('restores the native shell with text and Enter intents', async () => {
    const { content, teardown } = await mountTerminal(makeClipboard())
    try {
      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()

      content.switchToTerminalInput()
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledTimes(2))

      expect(session.intent.mock.calls).toEqual([
        ['text', '__nocx_native_mode'],
        ['key', 'enter'],
      ])
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

// Regression table for the two-axis lifecycle kernel (ADR-0024 §6). The
// authority axis moves only on published facts; the buffer axis is a
// renderer-owned presentation fact; no stream marker, submit or passport
// event can reach the reducer. This table pins every transition the kernel
// is allowed to make, so a state or event change must extend it
// deliberately.
describe('lifecycle kernel transition table (ADR-0024 §6)', () => {
  const promptReady = { lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 } as const

  it('every allowed kernel transition is pinned in the table', () => {
    const k = new LifecycleKernel()
    // Native: a conventional terminal, raw input, no ownership.
    expect(k.state.kind).toBe('native')
    expect(shouldShowEditor(k.state)).toBe(false)

    // buffer alternate → the buffer axis moves; ownership never follows.
    k.setBuffer('alternate')
    expect(k.buffer).toBe('alternate')
    expect(k.state.kind).toBe('native')
    expect(shouldShowEditor(k.state)).toBe(false)

    // buffer normal → back; still no ownership.
    k.setBuffer('normal')
    expect(k.buffer).toBe('normal')
    expect(k.state.kind).toBe('native')

    // reset → Native from any state, buffer restored.
    k.setBuffer('alternate')
    k.applyFact(promptReady)
    expect(shouldShowEditor(k.state)).toBe(true)
    k.reset()
    expect(k.state.kind).toBe('native')
    expect(k.buffer).toBe('normal')
    expect(shouldShowEditor(k.state)).toBe(false)

    // No marker, submit or passport event exists on the kernel
    // (compile-time proof; never invoked, so the @ts-expect-error is the
    // point and no runtime TypeError follows).
    const noStreamPath = (): void => {
      // @ts-expect-error ADR-0024 §1: the marker event is deleted.
      k.applyMarker('A') // eslint-disable-line @typescript-eslint/no-unsafe-call -- ADR-0024 §1: no stream input exists
      // @ts-expect-error ADR-0024 §1: the submit event is deleted.
      k.submit('echo hi') // eslint-disable-line @typescript-eslint/no-unsafe-call -- ADR-0024 §1
      // @ts-expect-error ADR-0024 §1: the passport event is deleted.
      k.applyPassport('636;...') // eslint-disable-line @typescript-eslint/no-unsafe-call -- ADR-0024 §1
    }
    void noStreamPath
  })
})

describe('the lifecycle fact wires editor ownership (ADR-0024 §6)', () => {
  // The registration seam, which is the half of nocx-upqz the renderer owns:
  // the shell can authenticate while session.open is still dialing, and the
  // backend replays that projection the instant it installs the subscriber —
  // immediately after the open result. A subscription registered after the
  // await would therefore miss the replay, so it must already exist when
  // openSession is called, and the fact that follows must reach the tab.
  it('subscribes before session.open so the replay that follows the result is heard', async () => {
    const client = makeClient()
    const session = makeSession()
    let subscribedBeforeOpen = false
    client.openSession.mockImplementation(() => {
      client._sessions.push(session)
      subscribedBeforeOpen = client.dispatcher.subscribe.mock.calls.some(
        (candidate: unknown[]) => candidate[0] === 'lifecycle.changed',
      )
      return Promise.resolve(session)
    })

    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(subscribedBeforeOpen, 'lifecycle subscription must exist before session.open').toBe(
        true,
      )
      lifecycleHandler(client)({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
      })
      expect(editorOf(content).isVisible).toBe(true)
    } finally {
      teardown()
    }
  })

  it('a prompt_ready fact shows the editor and a native fact hides it — through the dispatcher seam', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const ed = editorOf(content)
      // The kernel starts Native: a conventional terminal, editor hidden.
      expect(ed.isVisible).toBe(false)
      // The LifecycleClient subscribed through the fake dispatcher.
      const subscribe = client.dispatcher.subscribe
      expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
      const handler = lifecycleHandler(client)
      // An authenticated prompt_ready fact for a live domain gives the
      // editor the keyboard.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      // A native fact revokes it again.
      handler({ lane: 'lane-1', lifecycle: 'native' })
      expect(ed.isVisible).toBe(false)
    } finally {
      teardown()
    }
  })
})

describe('the restoration episode (ADR-0024 decision 8)', () => {
  const LOST_WITH_RECOVERY = {
    lane: 'lane-1',
    lifecycle: 'lost',
    recovery: { episodeId: 'rec-' + 'ab'.repeat(16), state: 'pending' as const },
  } as const

  it('a lost fact with a recovery contract suppresses the restore-editor action across the whole span', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const ed = editorOf(content)
      const handler = lifecycleHandler(client)
      const setAction = vi.spyOn(ed, 'setRecoveryAction')

      // The interval: from the lost fact until the acknowledgement lands,
      // the session is neither an authenticated terminal nor advertised as
      // a usable conventional one — no editor may be offered at any point
      // inside it.
      handler(LOST_WITH_RECOVERY)
      const calls = setAction.mock.calls
      const last = calls[calls.length - 1]
      expect(last).toBeDefined()
      expect(last[0]).toBeNull() // the action is suppressed, never offered
      // A native fact ends the episode (the ack landed; the backend
      // published the transition).
      handler({ lane: 'lane-1', lifecycle: 'native' })
    } finally {
      teardown()
    }
  })

  it('acknowledges only after the current episode is sighted, by episodeId', async () => {
    const client = makeClient()
    const { teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = lifecycleHandler(client)
      const call = client.dispatcher.call
      handler(LOST_WITH_RECOVERY)
      expect(call).not.toHaveBeenCalledWith('lifecycle.recoverAck', expect.anything())
      const sighted = {
        ...LOST_WITH_RECOVERY,
        recovery: { ...LOST_WITH_RECOVERY.recovery, state: 'sighted' as const },
      }
      handler(sighted)
      await Promise.resolve()
      await Promise.resolve()
      const sid = client._sessions[0].sessionId
      expect(call).toHaveBeenCalledTimes(1)
      expect(call).toHaveBeenCalledWith('lifecycle.recoverAck', {
        sessionId: sid,
        episodeId: LOST_WITH_RECOVERY.recovery.episodeId,
      })
    } finally {
      teardown()
    }
  })

  it('a refused acknowledgement keeps the pending guard: no editor is offered until the episode ends', async () => {
    const client = makeClient()
    client.dispatcher.call = vi.fn().mockRejectedValue(new Error('session is not open'))
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const ed = editorOf(content)
      const handler = lifecycleHandler(client)
      const setAction = vi.spyOn(ed, 'setRecoveryAction')
      handler(LOST_WITH_RECOVERY)
      handler({
        ...LOST_WITH_RECOVERY,
        recovery: { ...LOST_WITH_RECOVERY.recovery, state: 'sighted' },
      })
      await Promise.resolve()
      await Promise.resolve()
      // The refusal left the episode pending: the action stays suppressed.
      const calls = setAction.mock.calls
      const last = calls[calls.length - 1]
      expect(last[0]).toBeNull()
    } finally {
      teardown()
    }
  })
  it('keeps a new recovery episode pending when an old bind acknowledgement settles late', async () => {
    const client = makeClient()
    const pending: Array<{
      resolve: (value: unknown) => void
      reject: (reason?: unknown) => void
    }> = []
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method !== 'lifecycle.recoverAck') return Promise.resolve({})
      let resolve!: (value: unknown) => void
      let reject!: (reason?: unknown) => void
      const promise = new Promise<unknown>((release, fail) => {
        resolve = release
        reject = fail
      })
      pending.push({ resolve, reject })
      return promise
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const subscriptions = () =>
        client.dispatcher.subscribe.mock.calls.filter(
          (call: unknown[]) => call[0] === 'lifecycle.changed',
        )
      const first = client._sessions[0]
      const firstHandler = subscriptions()[0]?.[1] as (params: unknown) => void
      firstHandler({ ...LOST_WITH_RECOVERY, sessionId: first.sessionId })
      firstHandler({
        ...LOST_WITH_RECOVERY,
        sessionId: first.sessionId,
        recovery: { ...LOST_WITH_RECOVERY.recovery, state: 'sighted' },
      })
      expect(pending).toHaveLength(1)

      const onExit = first.onExit.mock.calls[0]?.[0] as
        ((exit: { sessionId: string; cause: 'interrupted' }) => void) | undefined
      expect(onExit).toBeDefined()
      if (!onExit) throw new Error('the first session did not register an exit handler')
      onExit({ sessionId: first.sessionId, cause: 'interrupted' })
      expect(await content.reconnect()).toBe(true)

      const second = client._sessions[1]
      const secondHandler = subscriptions()[1]?.[1] as (params: unknown) => void
      secondHandler({ ...LOST_WITH_RECOVERY, sessionId: second.sessionId })
      secondHandler({
        ...LOST_WITH_RECOVERY,
        sessionId: second.sessionId,
        recovery: { ...LOST_WITH_RECOVERY.recovery, state: 'sighted' },
      })
      expect(
        pending,
        'a fresh bind must claim its recovery acknowledgement without inheriting the old bind',
      ).toHaveLength(2)

      // The old bind settles after the new bind has claimed the same-shaped
      // recovery contract. It must not clear the new episode.
      pending[0]?.resolve({ accepted: true })
      await Promise.resolve()
      await Promise.resolve()
      const recoveryChip = editorOf(content).root.querySelector<HTMLElement>(
        '[data-control="recovery"]',
      )
      expect(recoveryChip?.style.display).toBe('none')

      // The current acknowledgement refuses. A genuinely fresh episode must
      // be able to claim its own acknowledgement rather than inheriting the
      // old bind's in-flight guard.
      pending[1]?.reject(new Error('session is not open'))
      await Promise.resolve()
      await Promise.resolve()
      expect(recoveryChip?.style.display).toBe('none')
      const freshRecovery = {
        episodeId: 'rec-' + 'ef'.repeat(16),
        state: 'pending' as const,
      }
      secondHandler({
        sessionId: second.sessionId,
        lane: 'lane-1',
        lifecycle: 'lost',
        recovery: freshRecovery,
      })
      secondHandler({
        ...LOST_WITH_RECOVERY,
        sessionId: second.sessionId,
        recovery: { ...freshRecovery, state: 'sighted' },
      })
      expect(pending).toHaveLength(3)
    } finally {
      teardown()
    }
  })
})

/**
 * Extract the body of the first top-level rule whose selector contains
 * `className` as a whole class. Brace-matched, so nested blocks (media
 * queries) cannot truncate the body. Returns null when no rule matches.
 */
function extractRuleBlock(css: string, className: string): string | null {
  const re = new RegExp(`\\.${className}(?![\\w-])`)
  let i = 0
  while (i < css.length) {
    const open = css.indexOf('{', i)
    if (open === -1) return null
    let depth = 1
    let j = open + 1
    while (j < css.length && depth > 0) {
      if (css[j] === '{') depth++
      else if (css[j] === '}') depth--
      j++
    }
    if (depth !== 0) return null
    if (re.test(css.slice(i, open))) return css.slice(open + 1, j - 1)
    i = j
  }
  return null
}

const stripComments = (s: string): string => s.replace(/\/\*[\s\S]*?\*\//g, '')

// The SSH block header regression (nocx-a44m): the cwd meta used to park in
// the dead centre of the header because `.cmd-header-meta` (then
// `.cmd-header-chips`) separated its children with `justify-content:
// space-between` — right for two children (a local block is [meta, right])
// and wrong for three. Fixed in 30014e3 by pushing the right group out with
// its own `margin-left: auto`, which behaved identically for any child
// count. Superseded by the mockup pass's two-line header grid (spec
// 2026-09-15 §1.7, task B): the right group is now the header's OWN third
// grid child (ui/command-block-frame.ts), not nested inside the meta row —
// that is what lets it span the command line while running, instead of only
// ever widening row one. `margin-left: auto` inside a flex row cannot do
// that, which is why the mechanism changed rather than only the numbers.
// jsdom computes no layout, so these assertions pin what jsdom CAN see: the
// DOM order that expresses the intent ("where left, duration and outcome
// right"), and the stylesheet's structural contract that places the header's
// grid without assuming a child count.
describe('the SSH block header keeps its where-meta left and the right group right (nocx-a44m, spec 2026-09-15 §1.7)', () => {
  const container = (): HTMLElement => document.createElement('div')
  const store = (): CommandSnapshotStore => new CommandSnapshotStore()
  const noop = (): void => {}

  it('orders the where meta before the right group, on an SSH block', () => {
    const el = createCommandBlock(
      'command',
      1,
      'deploy',
      '/srv/www',
      'user@server', // location — the fact that made the where meta two parts
      '<span class="term-line">done</span>',
      1200,
      0,
      'success',
      container,
      noop,
      store(),
      'shell',
    )
    const header = el.querySelector('.cmd-header')
    const metaRow = el.querySelector('.cmd-header-meta')
    expect(header).not.toBeNull()
    expect(metaRow).not.toBeNull()
    const where = metaRow?.querySelector(':scope > .ui-prompt-context')
    const right = header?.querySelector(':scope > .cmd-header-right')
    expect(where).not.toBeNull()
    expect(right).not.toBeNull()

    const order = [...(header as HTMLElement).children]
    expect(order.indexOf(metaRow as HTMLElement)).toBeLessThan(order.indexOf(right as HTMLElement))

    // Where reads host then directory, as one PromptContext (spec 2026-09-15
    // §2). No home is known at this seam, so the path is the absolute one.
    const parts = [...(where as HTMLElement).querySelectorAll('.ui-prompt-context__part')].map(
      (p) => p.textContent,
    )
    expect(parts).toEqual(['user@server', '/srv/www'])

    // The right group holds what belongs on the right: the duration, and —
    // success being silent (spec 2026-09-14 §3.1) — no status word.
    expect(right?.querySelector('.ui-meta[data-column="duration"]')).not.toBeNull()
    expect(el.dataset.outcome).toBe('success')
    expect(right?.querySelector('.ui-meta:not([data-column])')).toBeNull()
  })

  it('keeps the where meta before the right group on a local block too', () => {
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '~',
      '',
      '<span class="term-line">file</span>',
      42,
      0,
      'success',
      container,
      noop,
      store(),
      'shell',
    )
    const header = el.querySelector('.cmd-header')
    const metaRow = el.querySelector('.cmd-header-meta')
    const where = metaRow?.querySelector(':scope > .ui-prompt-context')
    const right = header?.querySelector(':scope > .cmd-header-right')
    expect(where).not.toBeNull()
    expect(right).not.toBeNull()
    const order = [...(header as HTMLElement).children]
    expect(order.indexOf(metaRow as HTMLElement)).toBeLessThan(order.indexOf(right as HTMLElement))
  })

  it('the stylesheet places the header on its own explicit grid, not space-between', () => {
    const css: string = readFileSync(COMMAND_BLOCK_FRAME_STYLE_ENTRY, 'utf8')
    const header = stripComments(extractRuleBlock(css, 'cmd-header') ?? '')
    const metaRow = stripComments(extractRuleBlock(css, 'cmd-header-meta') ?? '')
    const right = stripComments(extractRuleBlock(css, 'cmd-header-right') ?? '')
    expect(header).not.toBe('')
    expect(metaRow).not.toBe('')
    expect(right).not.toBe('')

    // Explicit grid placement, not a flex row a count-sensitive rule could
    // mis-space (nocx-a44m's own defect: `justify-content: space-between`
    // assumed exactly two children). Column 2 is where the right group
    // lands regardless of how many parts the meta row grows.
    expect(header).toMatch(/display\s*:\s*grid/)
    expect(metaRow).not.toMatch(/justify-content\s*:\s*(space-between|space-around|space-evenly)/)
    expect(right).toMatch(/grid-column\s*:\s*2/)
  })
})

describe('the composer chrome keeps its row height whatever it holds (nocx-i4h04, spec §5.2)', () => {
  it('declares a fixed height, never a floor, and never distributes its children', () => {
    const css = stripComments(readFileSync(COMPOSER_STYLE, 'utf8'))
    const chrome = stripComments(extractRuleBlock(css, 'nocx-editor-chrome') ?? '')
    expect(chrome).toMatch(/(^|;|\s)height\s*:\s*var\(--control-height-xs\)/)
    expect(chrome).not.toMatch(/min-height/)
    expect(chrome).not.toMatch(/justify-content\s*:\s*(space-between|space-around|space-evenly)/)
  })
})

describe('summoned editor overlay stylesheet contract (nocx-92gfl)', () => {
  it('keeps ordered answers scrolling above the composer in one absolute pane stack', () => {
    const css = stripComments(readFileSync(STYLE_ENTRY, 'utf8'))
    const baseCss = stripComments(readFileSync(BASE_STYLE_ENTRY, 'utf8'))
    const stack = stripComments(extractRuleBlock(css, 'nocx-summon-stack') ?? '')
    const answers = stripComments(extractRuleBlock(css, 'nocx-summon-answers') ?? '')
    const editor =
      stripComments(readFileSync(COMPOSER_STYLE, 'utf8')).match(
        /\.nocx-summon-stack\s*>\s*\.nocx-editor\[data-placement=['"]overlay['"]\]\s*\{([^}]*)\}/,
      )?.[1] ?? ''
    const pane = baseCss.match(/\.pane\s*\{([^}]*)\}/)
    expect(stack).not.toBe('')
    expect(answers).not.toBe('')
    expect(editor).not.toBe('')
    expect(pane).not.toBeNull()
    // The pane insets nothing; the stack is full width and its rows carry the
    // gutter (nocx-9bpeq.8, asserted in scrollback/trailing-edge.test.ts).
    expect(pane?.[1] ?? '').not.toMatch(/(^|;)\s*padding\s*:/)
    expect(stack).toMatch(/position\s*:\s*absolute/)
    expect(stack).toMatch(/left\s*:\s*0/)
    expect(stack).toMatch(/right\s*:\s*0/)
    expect(stack).toMatch(/bottom\s*:\s*0/)
    expect(stack).toMatch(/display\s*:\s*flex/)
    expect(stack).toMatch(/flex-direction\s*:\s*column/)
    expect(stack).toMatch(/max-height\s*:\s*100%/)
    expect(answers).toMatch(/overflow-y\s*:\s*auto/)
    expect(answers).toMatch(/min-height\s*:\s*0/)
    // The stack's `max-height: 100%` is the pane, so the answers carry the
    // real cap. Without it a long answer covers the program the question is
    // about — and after Escape, which keeps these nodes and takes the composer
    // away, nothing else bounds them (nocx-7l4ex.8).
    expect(answers).toMatch(/max-height\s*:\s*min\(50vh,\s*calc\(36em \+ 6px\)\)/)
    expect(editor).toMatch(/position\s*:\s*relative/)
    expect(editor).toMatch(/flex\s*:\s*none/)
  })
  it('gives the answer stack its own opaque ground above the frozen frame', () => {
    const css = stripComments(readFileSync(STYLE_ENTRY, 'utf8'))
    const stack = stripComments(extractRuleBlock(css, 'nocx-summon-stack') ?? '')
    const frame = stripComments(readFileSync(FRAME_STYLE_ENTRY, 'utf8'))
    expect(stack).not.toBe('')
    expect(frame).not.toBe('')
    // The stack owns every row it occupies. Its ground makes the frozen
    // capture behind it no longer a second painter of those rows, and it is
    // the terminal screen's one ground (spec 2026-09-14 §7), not the canvas.
    expect(stack).toMatch(/background\s*:\s*var\(--terminal-background\)/)
    expect(stack).toMatch(/z-index\s*:\s*10/)
    expect(frame).toMatch(/z-index\s*:\s*1/)
  })
})
it('pins content-sized answer rows and the capped long-answer scroller contract', () => {
  // jsdom has no flex layout, so this test cannot read rendered pixel
  // heights. It does mount the answer-list DOM and uses its observable
  // scrollHeight/clientHeight seam to state the two layout outcomes; the
  // shipped flex/max-height rules are the browser-side implementation.
  const css = stripComments(readFileSync(STYLE_ENTRY, 'utf8'))
  const answers = stripComments(extractRuleBlock(css, 'nocx-summon-answers') ?? '')
  expect(answers).not.toBe('')
  expect(answers).toMatch(/flex\s*:\s*none/)
  expect(answers).toMatch(/max-height\s*:\s*min\(50vh,\s*calc\(36em \+ 6px\)\)/)
  expect(answers).toMatch(/overflow-y\s*:\s*auto/)

  const cols = 113
  const rows = 37
  const frame: CapturedFrame = {
    rows: Array.from({ length: rows }, () => ({
      kind: 'cells',
      cells: Array.from({ length: cols }, () => ({ char: ' ', attrs: emptyAttrs() })),
    })),
    cursor: null,
    provenance: {
      source: 'live',
      identity: {
        buffer: { kind: 'alternate', altSession: 4 },
        cols,
        rows,
        generation: 9,
      },
      range: { start: 0, end: rows },
      scrollbackCapLines: 10000,
    },
  }
  const frameCss = stripComments(readFileSync(FRAME_STYLE_ENTRY, 'utf8'))
  const frameRows = stripComments(extractRuleBlock(frameCss, 'nocx-freeze-frame__row') ?? '')
  const frameCells = stripComments(extractRuleBlock(frameCss, 'nocx-freeze-frame__cell') ?? '')
  expect(frameRows).toMatch(/height\s*:\s*var\(--term-cell-height,\s*1\.2em\)/)
  expect(frameCells).toMatch(/width\s*:\s*var\(--term-cell-width,\s*1ch\)/)
  expect(frameCells).toMatch(/height\s*:\s*var\(--term-cell-height,\s*1\.2em\)/)

  const host = document.createElement('div')
  host.style.setProperty('--term-cell-width', '11.25px')
  host.style.setProperty('--term-cell-height', '23.5px')
  const view = createCapturedFrameView(frame)
  host.appendChild(view)
  expect(view.querySelectorAll('.nocx-freeze-frame__row')).toHaveLength(rows)
  expect(view.querySelectorAll('.nocx-freeze-frame__cell')).toHaveLength(cols * rows)
  expect(host.style.getPropertyValue('--term-cell-width')).toBe('11.25px')
  expect(host.style.getPropertyValue('--term-cell-height')).toBe('23.5px')
  const answerList = document.createElement('div')
  answerList.className = 'nocx-summon-answers'
  answerList.appendChild(document.createElement('div'))
  host.appendChild(answerList)
  Object.defineProperty(answerList, 'scrollHeight', { configurable: true, value: 24 })
  Object.defineProperty(answerList, 'clientHeight', { configurable: true, value: 24 })
  expect(answerList.scrollHeight).toBe(answerList.clientHeight)
  answerList.appendChild(document.createElement('div'))
  Object.defineProperty(answerList, 'scrollHeight', { configurable: true, value: 900 })
  Object.defineProperty(answerList, 'clientHeight', { configurable: true, value: 564 })
  expect(answerList.scrollHeight).toBeGreaterThan(answerList.clientHeight)
})

describe('terminal/editor input switching (nocx-atyf.5)', () => {
  it('no stream sequence can flip the presentation to editor — it is always terminal', async () => {
    const { content, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      content.setVisible(true)
      // Even an accepted screen and prompt boundary cannot change the
      // presentation axis (ADR-0024 §1).
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['$ prompt']))
      session.fireEffect({
        sessionId: session.sessionId,
        generation: '1',
        effectId: '1',
        kind: 'promptBoundary',
        screenRevision: 1,
        title: '',
        body: '',
      })
      expect(content.presentation).toBe('terminal')
      // The user gestures still exist and leave the presentation alone.
      content.switchToTerminalInput()
      expect(content.presentation).toBe('terminal')
      content.switchToEditorInput()
      expect(content.presentation).toBe('terminal')
    } finally {
      teardown()
    }
  })

  it('the choice is session-scoped — every session starts terminal', async () => {
    const { content: first, teardown: teardown1 } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      first.setVisible(true)
      first.switchToTerminalInput()
      expect(first.presentation).toBe('terminal')
    } finally {
      teardown1()
    }

    // A brand-new session starts with the same default.
    const { content: second, teardown: teardown2 } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      second.setVisible(true)
      expect(second.presentation).toBe('terminal')
    } finally {
      teardown2()
    }
  })
})

describe('activeOrigin (B.9) — the machine the tab speaks for', () => {
  it('a live local session yields an origin with kind local and its sessionId', async () => {
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const origin = content.activeOrigin()
      expect(origin).not.toBeNull()
      expect(origin?.kind).toBe('local')
      expect(origin?.sessionId).toBe(session.sessionId)
      // The open ack's cwd is the provider's guess — unverified until an
      // OSC 7 report arrives (AD-5).
      expect(origin?.cwd).toBe(FIXTURE_CWD)
      expect(origin?.cwdVerified).toBe(false)
      expect(origin?.host).toBeNull()
    } finally {
      teardown()
    }
  })

  it('an ssh session answers kind ssh with the host the session was opened with', async () => {
    const client = makeClient()
    const session = makeSession()
    client.openSSHSessionByHost.mockResolvedValue(session)
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { ssh: { profileId: '', host: 'srv-01' } },
      client,
    )
    try {
      const origin = content.activeOrigin()
      expect(origin?.kind).toBe('ssh')
      expect(origin?.sessionId).toBe(session.sessionId)
      expect(origin?.host).toBe('srv-01')
    } finally {
      teardown()
    }
  })

  it('answers null when there is no session, and once the session has exited', async () => {
    // Not mounted: no session yet, so there is no machine to name.
    const wsClient = makeClient() as unknown as WSClient
    const unmounted = new TerminalContent(
      wsClient,
      anchoredPane(),
      makeClipboard(),
      new ClipboardGate(),
      makeBanner(),
      null,
      () => {},
    )
    expect(unmounted.activeOrigin()).toBeNull()

    // Mounted, then the session exits: the session is gone and the origin
    // must not name a machine that no longer exists. The fake's onExit mock
    // records the callback TerminalContent registered at mount.
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(content.activeOrigin()).not.toBeNull()
      const exitCb = session.onExit.mock.calls[0]?.[0] as (exit: {
        sessionId: string
        cause: 'exited' | 'interrupted'
      }) => void
      expect(exitCb).toBeTypeOf('function')
      exitCb({ sessionId: session.sessionId, cause: 'exited' })
      expect(content.activeOrigin()).toBeNull()
    } finally {
      teardown()
    }
  })

  it('cwdVerified is false for the session-open cwd and true after an OSC 7 report', async () => {
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(content.activeOrigin()?.cwdVerified).toBe(false)
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: 'host', path: '/srv/new/path' } })
      const after = content.activeOrigin()
      expect(after?.cwd).toBe('/srv/new/path')
      expect(after?.cwdVerified).toBe(true)
    } finally {
      teardown()
    }
  })

  it('fires onActiveOriginChange when the origin answer changes, not only on tab switch', async () => {
    // The Files panel follows the ACTIVE tab's origin through this hook:
    // an OSC 7 cwd, the session dying and an environment boundary all
    // change the answer, and each must push the change (brief §1 — named
    // onActiveOriginChange, not onCwdChange, for exactly this reason).
    const onActiveOriginChange = vi.fn()
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onActiveOriginChange } },
      client,
    )
    try {
      // The session open itself is an origin transition (null → origin).
      expect(onActiveOriginChange).toHaveBeenCalledTimes(1)

      // A verified OSC 7 cwd changes the answer.
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: 'host', path: '/srv/new/path' } })
      expect(onActiveOriginChange).toHaveBeenCalledTimes(2)
      expect(content.activeOrigin()?.cwd).toBe('/srv/new/path')

      // The session dying changes it back to null.
      const exitCb = session.onExit.mock.calls[0]?.[0] as (exit: {
        sessionId: string
        cause: 'exited' | 'interrupted'
      }) => void
      exitCb({ sessionId: session.sessionId, cause: 'exited' })
      expect(onActiveOriginChange).toHaveBeenCalledTimes(3)
      expect(content.activeOrigin()).toBeNull()
    } finally {
      teardown()
    }
  })
})

describe("the pane's where-facts, fed from fake sources (nocx-9bpeq.16)", () => {
  /** A minimal fake session-home source: records every `ensure` call and
   *  lets the test resolve a session's home on its own schedule, exactly
   *  like the real source's async `files.open`. */
  function makeFakeSessionHome(): SessionHomeSource & {
    ensureCalls: Array<{ sessionId: string; cwd?: string | null; cwdVerified?: boolean }>
    resolveHome: (sessionId: string, home: string) => void
  } {
    const homes = new Map<string, string>()
    const subs = new Map<string, Set<(home: string) => void>>()
    const ensureCalls: Array<{ sessionId: string; cwd?: string | null; cwdVerified?: boolean }> = []
    return {
      ensureCalls,
      home: (sessionId) => homes.get(sessionId),
      ensure: (sessionId, cwd, cwdVerified) => {
        ensureCalls.push({ sessionId, cwd, cwdVerified })
        return Promise.resolve({} as FilesOpenResult)
      },
      subscribe: (sessionId, cb) => {
        let set = subs.get(sessionId)
        if (!set) {
          set = new Set()
          subs.set(sessionId, set)
        }
        set.add(cb)
        return () => set?.delete(cb)
      },
      resolveHome: (sessionId, home) => {
        homes.set(sessionId, home)
        for (const cb of [...(subs.get(sessionId) ?? [])]) cb(home)
      },
    }
  }

  /** A minimal fake branch source: records every `request` and lets the
   *  test publish a branch on its own schedule. */
  function makeFakeBranchSource(): BranchSource & {
    requests: BranchRequest[]
    publish: (branch: string | undefined) => void
  } {
    const subs = new Set<(branch: string | undefined) => void>()
    const requests: BranchRequest[] = []
    let current: string | undefined
    return {
      requests,
      branch: () => current,
      request: (req) => requests.push(req),
      subscribe: (cb) => {
        subs.add(cb)
        return () => subs.delete(cb)
      },
      dispose: () => subs.clear(),
      publish: (branch) => {
        current = branch
        for (const cb of [...subs]) cb(branch)
      },
    }
  }

  /** The composer's or a block's rendered prompt-line part, or undefined
   *  when the part is not drawn at all (PromptContext only draws `branch`
   *  when one is known). */
  const partText = (root: ParentNode, part: string): string | null | undefined =>
    root.querySelector(`[data-part="${part}"]`)?.textContent

  it("gives the composer and every new block a '~' path once the session's home resolves", async () => {
    const client = makeClient()
    const sessionHome = makeFakeSessionHome()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { sessionHome } },
      client,
    )
    const handler = lifecycleHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const sessionId = sessionOf(content).sessionId
    try {
      content.setVisible(true)
      // A verified cwd asks the source to open, exactly once.
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: '', path: '/home/nocx/project' } })
      expect(sessionHome.ensureCalls).toHaveLength(1)
      expect(sessionHome.ensureCalls[0]).toMatchObject({
        sessionId,
        cwd: '/home/nocx/project',
        cwdVerified: true,
      })

      // Before the source resolves, the absolute path stands — never a
      // guessed `~` (spec §2: "Without a known home the absolute path is
      // shown").
      expect(partText(editorOf(content).root, 'path')).toBe('/home/nocx/project')

      // The home resolves asynchronously, exactly as the real binding
      // does — the composer restates its line.
      sessionHome.resolveHome(sessionId, '/home/nocx')
      expect(partText(editorOf(content).root, 'path')).toBe('~/project')

      // A block opened AFTER the home is known takes the `~` form too.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('ls')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The card opens on the runtime's authenticated start, not at the
      // submit (nocx-2v80t.3.2).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-home',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'ls',
        },
      })
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      expect(partText(block!.el, 'path')).toBe('~/project')
    } finally {
      teardown()
    }
  })

  it('names the tab `~` at home once the home resolves — never before (nocx-1ebrs.45)', async () => {
    const client = makeClient()
    const sessionHome = makeFakeSessionHome()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { sessionHome } },
      client,
    )
    const sessionId = sessionOf(content).sessionId
    try {
      content.setVisible(true)
      // The shell's OSC 7 reports the home directory as an ABSOLUTE path —
      // the exact reported moment: the title showed the path's tail
      // (`Users/dev`) where a person reads `~`.
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: '', path: '/Users/dev' } })
      // Home has not resolved yet, so the raw tail stands — never a guessed
      // `~` (the same rule the prompt line holds, spec §2).
      expect(tab.title).toBe('Users/dev')

      // The binding resolves the home AFTER the cwd: the title re-derives
      // from the same fact the prompt line already takes.
      sessionHome.resolveHome(sessionId, '/Users/dev')
      expect(tab.title).toBe('~')
    } finally {
      teardown()
    }
  })

  it("names a directory under home rooted at `~`, keeping the tab's own tail (nocx-1ebrs.45)", async () => {
    const client = makeClient()
    const sessionHome = makeFakeSessionHome()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { sessionHome } },
      client,
    )
    const sessionId = sessionOf(content).sessionId
    try {
      content.setVisible(true)
      fireFrameMetadata(sessionOf(content), 1, {
        cwd: { host: '', path: '/Users/dev/Projects/nocx' },
      })
      // Unknown home: the raw tail, exactly as before the fix.
      expect(tab.title).toBe('Projects/nocx')

      sessionHome.resolveHome(sessionId, '/Users/dev')
      expect(tab.title).toBe('~/Projects/nocx')

      // A later OSC 7 re-derives with the home already known — the rooted
      // form, tail cut by the tab's own short-tail policy.
      fireFrameMetadata(sessionOf(content), 2, {
        cwd: { host: '', path: '/Users/dev/other/deep/one' },
      })
      expect(tab.title).toBe('~/deep/one')
    } finally {
      teardown()
    }
  })

  it('a branch change after a command was submitted updates the composer and not the already-created block', async () => {
    const client = makeClient()
    const branchSource = makeFakeBranchSource()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { createBranchSource: () => branchSource } },
      client,
    )
    const handler = lifecycleHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    try {
      content.setVisible(true)
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: '', path: '/repo' } })
      branchSource.publish('main')

      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('git status')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The card opens on the runtime's authenticated start, not at the
      // submit (nocx-2v80t.3.2).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-branch',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'git status',
        },
      })
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      const blockTitle = () => block!.el.querySelector('.ui-prompt-context')?.getAttribute('title')
      expect(blockTitle()).toContain('main')

      // The branch changes after the command was submitted (a `git
      // checkout`, say) — the composer hears about it; the block's line
      // is history and does not.
      branchSource.publish('feature')
      expect(partText(editorOf(content).root, 'branch')).toBe('feature')
      expect(blockTitle()).toContain('main')
      expect(blockTitle()).not.toContain('feature')
    } finally {
      teardown()
    }
  })

  it('the branch source hears about a verified cwd change and a settled block, and nothing for an unverified one', async () => {
    const FENCE = 'e'.repeat(64)
    const client = makeClient()
    const branchSource = makeFakeBranchSource()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { createBranchSource: () => branchSource } },
      client,
    )
    const handler = lifecycleHandler(client)
    const sessionId = sessionOf(content).sessionId
    try {
      content.setVisible(true)
      // The session-open cwd is not verified: nothing asks (spec §3).
      expect(branchSource.requests).toHaveLength(0)

      // A verified cwd triggers exactly one request.
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: '', path: '/repo' } })
      expect(branchSource.requests).toHaveLength(1)
      expect(branchSource.requests[0]).toMatchObject({
        sessionId,
        cwd: '/repo',
        cwdVerified: true,
        isLocal: true,
      })

      // The SAME verified cwd reported again (an unrelated OSC 7 replay)
      // does not reset the debounce for nothing.
      fireFrameMetadata(sessionOf(content), 2, { cwd: { host: '', path: '/repo' } })
      expect(branchSource.requests).toHaveLength(1)

      // A settled command block asks again, unconditionally — a `git
      // checkout` moves the branch without moving the cwd, which the
      // dedup above would otherwise swallow.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('git checkout main')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-branch',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'git checkout main',
        },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-branch',
          state: 'completed',
          exitCode: 0,
          fence: FENCE,
          completedAt: '2026-09-15T00:00:00Z',
        },
      })
      blockClosedHandler(client)('att-branch')
      expect(branchSource.requests).toHaveLength(2)
      expect(branchSource.requests[1]).toMatchObject({
        sessionId,
        cwd: '/repo',
        cwdVerified: true,
        isLocal: true,
      })
    } finally {
      teardown()
    }
  })

  it('asks nothing for a hand-typed ssh child domain — the branch stays undefined in the composer (nocx-9bpeq.16 round 3)', async () => {
    // Round 2 briefly read `isLocal` from `this.sshOpts === undefined` —
    // the SESSION's own kind, fixed at session-open — reasoning that
    // git.open's consent gate is a session-level fact the backend never
    // revises from inside the shell (AD-6: no byte-stream sniffing). True,
    // but the wrong fix for the wrong problem: after a hand-typed
    // `ssh pi@192.0.2.93`, the verified cwd this pane reports is a path
    // on THAT host, not on the local one `this.sshOpts === undefined`
    // describes. Asking `git.open` about it would resolve `/home/pi`
    // against the LOCAL filesystem — the branch of whatever unrelated
    // local directory happens to share that path, or nothing, shown under
    // a prompt that reads as remote. `view.isLocal` — "which machine will
    // the next command actually run on" — is the fact this gate needs,
    // and it is exactly what a destination-bearing child domain reports
    // false (lifecycle/domain-environment.ts's `_seedFor`). This pane is a
    // LOCAL session throughout (`mountTerminal` with no `ssh` option); it
    // walks into a hand-typed `ssh pi@192.0.2.93` (the same fact shape
    // "a local tab whose pane walks onto a remote host" already uses,
    // ~line 3200), and the branch source must be asked NOTHING for a
    // verified cwd reported on that nested domain — the composer keeps
    // showing no branch.
    const client = makeClient()
    const branchSource = makeFakeBranchSource()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { createBranchSource: () => branchSource } },
      client,
    )
    const handler = lifecycleHandler(client)
    try {
      content.setVisible(true)
      // The local shell's own domain first.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // The parent suspends as the hand-typed ssh's child domain
      // establishes (protocol §9).
      handler({ lane: 'lane-1', lifecycle: 'native' })
      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: '192.0.2.93', user: 'pi' },
      })
      // A verified cwd reported on the nested domain — a path on the far
      // host, which this pane's own (local) session cannot answer for.
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: '192.0.2.93', path: '/home/pi' } })
      expect(branchSource.requests).toHaveLength(0)
      expect(partText(editorOf(content).root, 'branch')).toBeUndefined()
    } finally {
      teardown()
    }
  })
})

describe('an interrupted session is marked, never destroyed (nocx-ictcq)', () => {
  type ExitCb = (exit: {
    sessionId: string
    cause: 'exited' | 'interrupted'
    status?: number
  }) => void

  const exitCbOf = (session: SessionFake): ExitCb => session.onExit.mock.calls[0]?.[0] as ExitCb

  // The heart of the bead: a session whose connection dropped must not
  // vanish. The tab stays in the strip with its pane and scrollback, and the
  // warning mark says what the state is — the same mark the integration
  // axis already owns, with the loss's own wording.
  it('a loss keeps the tab present, marked with its own label', async () => {
    const warnings: Array<[boolean, string | undefined]> = []
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { onWarningChange: (w, l) => warnings.push([w, l]) } },
      client,
    )
    const closeRequested = vi.fn()
    tab.onCloseRequested = closeRequested
    try {
      exitCbOf(session)({ sessionId: session.sessionId, cause: 'interrupted' })

      // Not closed: the tab and its scrollback survive.
      expect(closeRequested).not.toHaveBeenCalled()
      expect(tab.pane.isConnected).toBe(true)
      // The session is gone, so the origin names nothing — the same cleanup
      // a clean exit performs.
      expect(content.activeOrigin()).toBeNull()
      // Marked, with a label that says what happened.
      expect(warnings[warnings.length - 1]).toEqual([true, 'Connection lost'])
    } finally {
      teardown()
    }
  })

  // A clean exit is unchanged: the tab closes exactly as it always did.
  it('a clean exit closes the tab as before', async () => {
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const closeRequested = vi.fn()
    tab.onCloseRequested = closeRequested
    try {
      exitCbOf(session)({ sessionId: session.sessionId, cause: 'exited', status: 0 })
      expect(closeRequested).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })

  // The backend may emit its last integration status in the same instant the
  // session dies; once the tab is marked lost, no later fact may clear it —
  // the lost state is terminal for this tab (the strip mark is what the user
  // looks at an hour later).
  it('a late integration fact does not clear the loss mark', async () => {
    const warnings: Array<[boolean, string | undefined]> = []
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const fireIntegration = integrationHandler(client, session)
    const { teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onWarningChange: (w, l) => warnings.push([w, l]) } },
      client,
    )
    try {
      exitCbOf(session)({ sessionId: session.sessionId, cause: 'interrupted' })
      const markedAt = warnings.length

      // A recovered-looking fact ("integrated", no reason) arriving late:
      // before the guard this cleared the mark and the tab read healthy.
      fireIntegration({
        sessionId: session.sessionId,
        status: 'integrated',
        shell: '/bin/bash',
      })

      expect(warnings.length).toBe(markedAt)
      expect(warnings[warnings.length - 1]).toEqual([true, 'Connection lost'])
    } finally {
      teardown()
    }
  })
})

describe('the ports target follows where the pane IS, not how it was opened (nocx-695k.3)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the wire seam tests deliver authenticated facts through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    const subscribe = client.dispatcher.subscribe
    expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
    return lifecycleHandler(client)
  }

  it('a local tab with no remote domain scopes to the local machine', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(content.portsTargetId).toBe(LOCAL_TARGET_ID)
      expect(content.portsUnavailableReason).toBe('')
    } finally {
      teardown()
    }
  })

  it('a local tab whose pane walks onto a remote host loses the local scope and names that host', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = factHandler(client)
      // The local shell's own domain first: a destination-bearing domain is
      // a CHILD of it, and only a child voids the scope. (The root is
      // seeded from the session facts; its destination is deliberately not
      // the child's.)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(content.portsTargetId).toBe(LOCAL_TARGET_ID)
      // The parent suspends as the child establishes (protocol §9: the
      // kernel rejects a second prompt_ready without a native between).
      handler({ lane: 'lane-1', lifecycle: 'native' })
      // The hand-typed `ssh pi@192.0.2.93`.
      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: '192.0.2.93', user: 'pi' },
      })
      expect(content.portsTargetId).toBeNull()
      // The reason is the same user@host the block header shows (the one
      // existing derivation, not a second one).
      expect(content.portsUnavailableReason).toBe('pi@192.0.2.93')
    } finally {
      teardown()
    }
  })

  it('walking in and back out fires onPortsTargetChange and the answer returns to local', async () => {
    const onPortsTargetChange = vi.fn()
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onPortsTargetChange } },
      client,
    )
    try {
      const handler = factHandler(client)
      // The session's own domain is not a change: still the local machine.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(onPortsTargetChange).not.toHaveBeenCalled()
      // The parent suspends, then the child establishes (§9).
      handler({ lane: 'lane-1', lifecycle: 'native' })

      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: '192.0.2.93', user: 'pi' },
      })
      expect(onPortsTargetChange).toHaveBeenCalledTimes(1)
      expect(content.portsTargetId).toBeNull()

      // Walk back out: the child suspends, then the parent re-activates
      // (activation is the only way a suspended domain returns, §9). The
      // one-way test would pass with a latched answer; this asserts the
      // pane is local again.
      handler({ lane: 'lane-1', lifecycle: 'native' })
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(onPortsTargetChange).toHaveBeenCalledTimes(2)
      expect(content.portsTargetId).toBe(LOCAL_TARGET_ID)
      expect(content.portsUnavailableReason).toBe('')
    } finally {
      teardown()
    }
  })

  it('a saved-profile tab sitting on its own host keeps its profile scope', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { ssh: { profileId: 'ct-pihole', host: 'pihole.local' } },
      client,
    )
    try {
      expect(content.portsTargetId).toBe('ct-pihole')
      expect(content.portsUnavailableReason).toBe('')
      // Even once the session's own root domain is live — whose host IS the
      // profile host — the pane is still on the machine the profile owns.
      // Host presence alone must not void the scope.
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(content.portsTargetId).toBe('ct-pihole')
      expect(content.portsUnavailableReason).toBe('')
    } finally {
      teardown()
    }
  })

  it('a saved-profile tab whose pane walks onto a different host loses the profile scope (the hole)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { ssh: { profileId: 'ct-pihole', host: 'pihole.local' } },
      client,
    )
    try {
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({ lane: 'lane-1', lifecycle: 'native' })
      // Hand-typed onward: the pane left the machine the profile owns, and
      // the Forward button must not stay live for the profile's host.
      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: '10.0.0.5', user: 'root' },
      })
      expect(content.portsTargetId).toBeNull()
      expect(content.portsUnavailableReason).toBe('root@10.0.0.5')
    } finally {
      teardown()
    }
  })
})

describe('the projections consume the kernel through the composition root (ADR-0024 §5–§7, bead nocx-u7uh.7)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the wire seam tests deliver authenticated facts through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    const subscribe = client.dispatcher.subscribe
    expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
    return lifecycleHandler(client)
  }

  it('the native escape holds through a later prompt_ready fact — the input router (ADR-0024 §6)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const ed = editorOf(content)
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      // The user's own escape: the editor hides and keys use structured intents.
      content.switchToTerminalInput()
      expect(ed.isVisible).toBe(false)
      // A native fact and ANOTHER authenticated prompt must not undo the
      // escape — the latch is the user's, the authority stays the kernel's.
      handler({ lane: 'lane-1', lifecycle: 'native' })
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(false)
      // The explicit switch back restores the editor at the authenticated prompt.
      content.switchToEditorInput()
      expect(ed.isVisible).toBe(true)
    } finally {
      teardown()
    }
  })

  it('the capability rail reports the kernel state — integrated only from an authenticated prompt', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // The kernel starts Native: a conventional terminal, unsupported.
      expect(content.shellState).toBe('unsupported')
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(content.shellState).toBe('integrated')
      handler({ lane: 'lane-1', lifecycle: 'lost' })
      expect(content.shellState).toBe('lost')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd2', epoch: 2 })
      expect(content.shellState).toBe('integrated')
    } finally {
      teardown()
    }
  })

  it('a receipt whose ack beats the render fence still lands, on the block it belongs to (nocx-ggha)', async () => {
    const FENCE = 'c'.repeat(64)
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const recorded = historyRecordedHandler(client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('echo sk-proj-abcdef')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-g',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'echo sk-proj-abcdef',
        },
      })
      // Completed WITH a fence that has not been sighted: the logical freeze
      // lands now and the VISUAL one defers, so the element still reads
      // cmd-block-running while the block is finished. This is the window
      // the ack arrives in on a cold render.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-g',
          state: 'completed',
          exitCode: 0,
          fence: FENCE,
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      const rec = withScrollback.scrollback.blockManager.blocks[0]
      expect(rec.status).toBe('success')
      expect(rec.el.classList.contains('cmd-block-running')).toBe(true)
      // This receipt test marks the block to prove identity survives the
      // visual freeze; make that setup an Ask target explicitly.
      ;(
        content as unknown as { inputTargets: { setActive(id: string): void } }
      ).inputTargets.setActive('agent')
      const menuButton = rec.el.querySelector<HTMLElement>('[data-block-actions]')
      expect(menuButton).not.toBeNull()
      menuButton!.click()
      const menuItems = Array.from(
        document.querySelectorAll<HTMLElement>(
          '[data-testid="block-actions-menu"] .ui-context-menu__item',
        ),
      )
      const grant = menuItems.find((item) => item.dataset.itemId === 'grant')
      const stop = menuItems.find((item) => item.dataset.itemId === 'stop')
      expect(grant).toBeDefined()
      expect(grant?.textContent).toBe('Ask about this block')
      grant!.click()
      const grantsBeforeAck = (content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks
      expect(grantsBeforeAck[0]?.command).toBe('echo sk-proj-abcdef')
      expect(stop).toBeUndefined()
      document.querySelector('[data-testid="block-actions-menu"]')?.remove()

      // The backend-owned receipt arrives over the notification subscription,
      // before the fence that replaces the visible block.
      recorded({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-g',
        maskedCount: 1,
        maskedKinds: ['openai'],
        entryId: 'e-ggha',
        source: 'user',
        redactions: [{ kind: 'openai', start: 5, end: 11, prefix: 'sk-', suffix: 'op' }],
        maskedCommand: 'echo sk-***',
        captures: [
          {
            id: 'cap-1',
            entryId: 'e-ggha',
            suggestedName: 'openai-key',
            redaction: { kind: 'openai', start: 5, end: 11, prefix: 'sk-', suffix: 'op' },
          },
        ],
      })

      // The fence lands and the visual freeze replaces the element. The
      // receipt must be on the NEW element — the one the user is looking at.
      blockClosedHandler(client)('att-g')
      expect(rec.el.classList.contains('cmd-block-running')).toBe(false)
      await vi.waitFor(() => expect(rec.el.querySelector('.ui-block-receipt')).not.toBeNull())
      expect(
        (content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks[0]?.command,
      ).toBe('echo sk-***')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('a submitted command freezes its block and persists history from the authenticated completion', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const recorded = historyRecordedHandler(client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The app-owned submit opened a ledger record. It opens NO card: the
      // card waits for the runtime's authenticated start (nocx-2v80t.3.2).
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)

      // The published attempt: the shell start attaches, then completes.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })
      const live = withScrollback.scrollback.blockManager.blocks[0]
      expect(live.el.dataset.entryId).toBe('att-1')
      expect(grantBlockFromElement(live.el)?.itemId).toBe('att-1')
      // The published running fact must NOT tear the block model down: the
      // pane stays in the running layout with the block visible. It used to
      // call setUnstructured unconditionally here, which put the pane back
      // into the full-pane conventional grid on every fact — the block was
      // in the DOM but hidden (inner-fullscreen-mode), so a live session
      // showed a flat stream with no block, no freeze, no exit status
      // (nocx-u7uh.25).
      expect(withScrollback.scrollback.mode).toBe('running')
      expect(
        withScrollback.scrollback.scrollbackInner.classList.contains('inner-fullscreen-mode'),
      ).toBe(false)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      recorded({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'user',
        redactions: [],
        captures: [],
        maskedCommand: 'make',
      })

      // The block froze with the authenticated status.
      const frozen = withScrollback.scrollback.blockManager.blocks[0]
      expect(frozen.status).toBe('success')
      expect(frozen.exitCode).toBe(0)
      expect(frozen.attemptId).toBe('att-1')
      expect(frozen.el.dataset.entryId).toBe('att-1')
      expect(grantBlockFromElement(frozen.el)?.itemId).toBe('att-1')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()

      expect(client.dispatcher.subscribe).toHaveBeenCalledWith(
        'history.recorded',
        expect.any(Function),
      )
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })
  it('reconciles mounted command rows from durable storage after reconnect', async () => {
    const client = makeClient()
    const line = JSON.stringify({
      from: 0,
      row: wireRowOf([['recovered', 1, true] as CellSpec]),
    })
    client.call.mockImplementation((method: string) => {
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
        })
      }
      if (method === 'ledger.artifact') {
        return Promise.resolve({
          id: 'art-rows',
          mediaType: 'application/x-nocx-rows',
          body: `${line}\n`,
          truncated: null,
          byteLen: line.length,
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })

      // No block notification is delivered during the outage. Reconnect must
      // find the already-mounted attempt and fetch its current durable rows.
      expect(withScrollback.scrollback.scrollbackInner.querySelector('.cmd-output')).toBeNull()
      client._fireReconnect()
      await vi.waitFor(() => {
        expect(
          withScrollback.scrollback.scrollbackInner.querySelector('.cmd-output')?.textContent,
        ).toContain('recovered')
      })
      expect(client.call.mock.calls.filter(([method]) => method === 'ledger.get')).toHaveLength(1)
    } finally {
      teardown()
    }
  })

  it('follows the tail when a block.grew delivery grows a block after the scroller already settled (nocx-2v80t.3.19)', async () => {
    // block.grew/block.closed only NAME the entry; the rows are a further
    // fetch (ledger.get then ledger.artifact) that resolves well after the
    // notification, on its own tick — a mutation the scrollback controller
    // never sees on its own, unlike a running block's own live growth
    // (controller.ts's inline height guard). Applying it without settling
    // around it left a person who was following the tail behind once the
    // fetch landed, silently.
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
        })
      }
      if (method === 'ledger.artifact') {
        const line = JSON.stringify({
          from: 0,
          row: wireRowOf([['x', 1, true] as CellSpec]),
        })
        return Promise.resolve({
          id: 'art-rows',
          mediaType: 'application/x-nocx-rows',
          body: `${line}\n`,
          truncated: null,
          byteLen: line.length,
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })

      const scrollTo = vi.fn()
      withScrollback.scrollback.scrollbackArea.scrollTo = scrollTo

      const blockGrew = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'block.grew',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(blockGrew).toBeDefined()
      blockGrew?.({ entryId: 'att-1' })

      // The fetch is two chained RPC round trips (ledger.get then
      // ledger.artifact) before the paint and the follow — let them run.
      await vi.waitFor(() => expect(scrollTo).toHaveBeenCalled())
    } finally {
      teardown()
    }
  })

  it('repaints a frozen block with the rows its close made final, so a settled block is whole (nocx-atpw5.1.19)', async () => {
    // THE WINDOW THE CI FAILURE FELL IN. block.closed freezes the card at once
    // and paints it from the rows the last OPEN read returned; the sealed read
    // that close starts is two RPC round trips behind and repaints. A census
    // taken between the two read 64 rows painted for 100 stored — the last
    // block of a 500-block transcript on webkit. What this pins is the other
    // half, which the census now waits for: the sealed read lands on the frozen
    // card and the block ends holding every row it stores.
    const row = (grapheme: string, from: number): string =>
      `${JSON.stringify({ from, row: wireRowOf([[grapheme, 1, true] as CellSpec]) })}\n`
    const bodies = [row('a', 0), `${row('a', 0)}${row('b', 1)}`]
    const releases: Array<() => void> = []
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
        })
      }
      if (method === 'ledger.artifact') {
        const body = bodies[releases.length]
        return new Promise((resolve) => {
          releases.push(() =>
            resolve({
              id: 'art-rows',
              mediaType: 'application/x-nocx-rows',
              body,
              truncated: null,
              byteLen: body.length,
            }),
          )
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const paintedRows = (): number =>
      withScrollback.scrollback.blockManager
        .blockForAttempt('att-1')!
        .el.querySelectorAll('.cmd-output .term-grid-row').length
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })
      const blockGrew = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'block.grew',
      )?.[1] as ((params: unknown) => void) | undefined
      const blockClosed = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'block.closed',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(blockGrew).toBeDefined()
      expect(blockClosed).toBeDefined()

      // The growth read: one row, painted on the running block.
      blockGrew?.({ entryId: 'att-1' })
      await vi.waitFor(() => expect(releases).toHaveLength(1))
      releases[0]()
      await vi.waitFor(() => expect(paintedRows()).toBe(1))

      // The command finishes (the logical freeze), and the backend's close —
      // which freezes the card on screen — arrives while its sealed read is
      // what is still in flight.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'c'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      blockClosed?.({ entryId: 'att-1', kept: true })
      await vi.waitFor(() => expect(releases).toHaveLength(2))
      await vi.waitFor(() =>
        expect(
          withScrollback.scrollback.blockManager
            .blockForAttempt('att-1')!
            .el.classList.contains('cmd-block-running'),
        ).toBe(false),
      )
      // Frozen on the rows it already had: the sealed read has not landed.
      expect(paintedRows()).toBe(1)

      // The rows the close made final land on the frozen card.
      releases[1]()
      await vi.waitFor(() => expect(paintedRows()).toBe(2))
      expect(
        withScrollback.scrollback.blockManager
          .blockForAttempt('att-1')!
          .el.classList.contains('cmd-block-running'),
      ).toBe(false)
    } finally {
      teardown()
    }
  })

  it('coalesces a growth burst and fetches final rows once after block.closed', async () => {
    const releases: Array<() => void> = []
    const body = `${JSON.stringify({
      from: 0,
      row: wireRowOf([['x', 1, true] as CellSpec]),
    })}\n`
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
        })
      }
      if (method === 'ledger.artifact') {
        return new Promise((resolve) => {
          releases.push(() =>
            resolve({
              id: 'art-rows',
              mediaType: 'application/x-nocx-rows',
              body,
              truncated: null,
              byteLen: body.length,
            }),
          )
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })
      const paintStoredRows = vi.spyOn(withScrollback.scrollback.blockManager, 'applyStoredRows')
      const blockGrew = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'block.grew',
      )?.[1] as ((params: unknown) => void) | undefined
      const blockClosed = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'block.closed',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(blockGrew).toBeDefined()
      expect(blockClosed).toBeDefined()
      for (let index = 0; index < 100; index += 1) blockGrew?.({ entryId: 'att-1' })
      expect(client.call.mock.calls.filter(([method]) => method === 'ledger.get')).toHaveLength(1)
      await vi.waitFor(() => expect(releases).toHaveLength(1))

      // The final notification arrives during the growth read. It must queue
      // one fresh read after that read settles rather than repainting the same
      // rows 100 times or closing on a partial artifact.
      blockClosed?.({ entryId: 'att-1', kept: true })
      releases[0]()
      await vi.waitFor(() => expect(releases).toHaveLength(2))
      await vi.waitFor(() => expect(paintStoredRows).toHaveBeenCalledTimes(1))
      releases[1]()
      await vi.waitFor(() => expect(paintStoredRows).toHaveBeenCalledTimes(2))
      expect(
        client.call.mock.calls.filter(([method]) => method === 'ledger.artifact'),
      ).toHaveLength(2)
    } finally {
      teardown()
    }
  })

  it('coalesces block.grew reads without losing the latest stored rows', async () => {
    type Artifact = {
      id: string
      mediaType: string
      body: string
      truncated: null
      byteLen: number
    }
    const artifact = (text: string): Artifact => {
      const line = JSON.stringify({
        from: 0,
        row: wireRowOf(Array.from(text, (ch) => [ch, 1, true] as CellSpec)),
      })
      return {
        id: 'art-rows',
        mediaType: 'application/x-nocx-rows',
        body: `${line}\n`,
        truncated: null,
        byteLen: line.length,
      }
    }

    let resolveFirst!: (value: Artifact) => void
    const firstRead = new Promise<Artifact>((resolve) => {
      resolveFirst = resolve
    })
    let entryReads = 0
    let artifactReads = 0
    const client = makeClient()
    client.call.mockImplementation((method: string, params?: unknown) => {
      const id =
        typeof params === 'object' && params !== null && 'id' in params ? params.id : undefined
      if (method === 'ledger.get' && id === 'att-1') {
        entryReads += 1
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
        })
      }
      if (method === 'ledger.artifact' && id === 'art-rows') {
        artifactReads += 1
        return artifactReads === 1 ? firstRead : Promise.resolve(artifact('latest rows'))
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })

    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    let outputObserver: MutationObserver | null = null
    const outputMutations: MutationRecord[] = []
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })

      const grew = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'block.grew',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(grew).toBeDefined()
      const block = scrollbackFor(content).blockManager.blockForAttempt('att-1')
      expect(block).toBeDefined()
      if (!block) throw new Error('running attempt did not open its command block')
      outputObserver = new MutationObserver((records) => outputMutations.push(...records))
      outputObserver.observe(block.el, { childList: true })
      grew?.({ entryId: 'att-1', from: 0, count: 1 })
      await vi.waitFor(() => expect(artifactReads).toBe(1))

      // Keep one read open while the producer says the artifact grew many
      // times. One pending bit must retain the last notification without
      // starting another full-artifact read for every growth frame.
      for (let index = 1; index <= 8; index += 1) {
        grew?.({ entryId: 'att-1', from: index, count: 1 })
      }
      expect(entryReads).toBe(1)
      expect(artifactReads).toBe(1)

      resolveFirst(artifact('earlier rows'))
      await vi.waitFor(() => expect(blockOutputText(block.el)).toBe('latest rows'))
      expect(entryReads).toBe(2)
      expect(artifactReads).toBe(2)
      // paintStoredRows replaces the whole .cmd-output node once per paint;
      // count those real subtree replacements, not a timer or a helper call.
      if (!outputObserver) throw new Error('output mutation observer was not installed')
      const outputPaints = [...outputMutations, ...outputObserver.takeRecords()].reduce(
        (count, mutation) =>
          count +
          Array.from(mutation.addedNodes).filter(
            (node) =>
              node.nodeType === Node.ELEMENT_NODE &&
              (node as HTMLElement).classList.contains('cmd-output'),
          ).length,
        0,
      )
      expect(outputPaints).toBe(2)
    } finally {
      outputObserver?.disconnect()
      teardown()
    }
  })

  it('a card is opened and closed only by what the backend sent (nocx-2v80t.3.2)', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('make')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

      // THE BACKEND HAS AUTHENTICATED NOTHING YET — no card. The submit
      // opens a ledger record and writes bytes; it does not decide a block
      // boundary, and the card waits for the runtime's word.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()

      // The authenticated start opens the card, with the submitting author.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.runningBlock?.status).toBe('running')
      expect(withScrollback.scrollback.blockManager.runningBlock?.command).toBe('make')
      expect(withScrollback.scrollback.blockManager.runningBlock?.author).toBe('shell')

      // The shell's fence lands after the output (the nonce row), then the
      // authenticated completion closes the card — with its body.
      blockClosedHandler(client)('att-1')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-09-21T12:00:02Z',
        },
      })
      const frozen = withScrollback.scrollback.blockManager.blocks[0]
      expect(frozen.status).toBe('success')
      expect(frozen.exitCode).toBe(0)
      // The frontend does not serialize the terminal buffer into a frozen
      // block. Backend row artifacts are painted through block.grew/closed.
      expect(blockOutputText(frozen.el)).toBe('')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('no timer decides a block boundary — the card waits for the runtime (nocx-2v80t.3.2)', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      vi.useFakeTimers()

      ed.insertText('sleep 300')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()

      // Nothing authenticated: no card now, and no timer opens one later.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)
      vi.advanceTimersByTime(60_000)
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)

      // The authenticated start opens the card; with no completion, no timer
      // closes it either — it is still running a minute later.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 300',
        },
      })
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      vi.advanceTimersByTime(60_000)
      expect(withScrollback.scrollback.blockManager.runningBlock).not.toBeNull()
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')
    } finally {
      vi.useRealTimers()
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('no submit-time open or fence timer survives in the boundary sources (nocx-2v80t.3.2)', () => {
    for (const rel of ['scrollback/controller.ts', 'scrollback/blocks.ts', 'terminal-content.ts']) {
      const src = readFileSync(resolve(srcDir, rel), 'utf8')
      expect(src, `${rel}: beginBlockNow`).not.toMatch(/beginBlockNow/)
      expect(src, `${rel}: FENCE_DEFER_MS`).not.toMatch(/FENCE_DEFER_MS/)
    }
  })

  it('no clear or rebase of the live cell model survives at command boundaries', () => {
    for (const rel of ['scrollback/controller.ts', 'scrollback/blocks.ts', 'terminal-content.ts']) {
      const src = readFileSync(resolve(srcDir, rel), 'utf8')
      expect(src, `${rel}: clearViewport`).not.toMatch(/clearViewport/)
      expect(src, `${rel}: _settleFrozen`).not.toMatch(/_settleFrozen/)
      expect(src, `${rel}: _freezeVisual`).not.toMatch(/_freezeVisual/)
      expect(src, `${rel}: _clearFrozenRows`).not.toMatch(/_clearFrozenRows/)
    }
  })

  it('submitAgentCommand runs the command through the ordinary path with the agent author and resolves with the completed run body (nocx-tjppv)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const ledger = (content as unknown as { ledger: CommandLedger }).ledger
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })

      const pending = content.submitAgentCommand('make')
      const session = sessionOf(content)
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make'],
          ['key', 'enter'],
        ]),
      )
      expect(session.send).not.toHaveBeenCalled()

      // The ordinary path ran: the ledger record was minted at submit with
      // the agent's author (design §3.1) — the command exists as a command,
      // not as bytes (criterion 3: asserted on the ledger, not the DOM).
      // The submit opens NO card: the block is the projection of the
      // runtime's authenticated start (nocx-2v80t.3.2).
      expect(ledger.records()).toHaveLength(1)
      expect(ledger.records()[0].author).toBe('agent')
      expect(ledger.records()[0].command).toBe('make')
      expect(ledger.records()[0].status).toBe('running')
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)
      // The attempt: the app-owned lifecycle submit ran with the agent's
      // command — the command exists as an attempt, not only as bytes
      // (criterion 3). The attempt goes through the DISPATCHER (the
      // LifecycleClient's seam), not the WS client's call.
      const attemptCall = client.dispatcher.call.mock.calls.find(
        (c) => c[0] === 'lifecycle.submitAttempt',
      )
      expect(attemptCall).toBeTruthy()
      expect((attemptCall![1] as { command: string }).command).toBe('make')
      // AND IT CARRIES WHO SUBMITTED IT. The durable row is opened by this
      // very call (nocx-kpqr3), so this is the only place the author reaches
      // the store — the backend's completion receipt moves the status and
      // leaves the column alone. An attempt submitted without it came back
      // from a restart as the person's command (nocx-1druc, agent-restore.spec.ts).
      expect((attemptCall![1] as { source: string }).source).toBe('assistant')

      // The attempt attaches and completes: the block freezes with the exit
      // status, exactly as a human command's does.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })
      // The authenticated start opened the card, with the agent's author.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.blocks[0].author).toBe('agent')
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      historyRecordedHandler(client)({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'assistant',
        redactions: [],
        maskedCommand: 'make',
        captures: [],
      })

      // The shell's fence lands after the output — the sighting the visual
      // boundary resolves on, and the only thing that cuts it (nocx-2v80t.3.2).
      blockClosedHandler(client)('att-1')
      const run = await pending
      // THE ENTRY ID IS THE STORE'S, and it is the one completion receipt
      // named (nocx-9sqii). It used to be `String(rec.id)` — the renderer's
      // own record number, which counts blocks in this tab and is not an
      // entry anywhere. The backend joins the command to the turn that ran
      // it by writing an edge against this id, and the ledger's foreign key
      // refuses an id that names no row, so the whole relation was dropped
      // in a log line nobody read: a restored turn came back with its
      // command outside it.
      expect(run.entryId).toBe('e1')
      expect(run.entryId).not.toBe(String(ledger.records()[0].id))
      expect(run.exitCode).toBe(0)
      expect(run.status).toBe('success')
      expect(run.total).toBeGreaterThanOrEqual(0)
      expect(typeof run.text).toBe('string')
      // The frozen block is the same object the freeze mutated — the wait
      // resolved on the block's completion, never on a timer.
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('success')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it("fetches a frozen block's stored rows itself when it froze before any block.grew/closed notification named it, and reports the real output (nocx-2v80t.3.19)", async () => {
    // The render fence that drives a freeze is a local, data-plane read; the
    // block.grew/block.closed notification that would otherwise start the
    // rows fetch crosses the control-plane socket, with no ordering promised
    // between the two. This drives the freeze with NEITHER notification ever
    // dispatched, so `_ensureBlockRows` has nothing to wait on and must start
    // the fetch itself — the run tool's result is the one place this
    // reaches: an unmarked `.cmd-output` still had `blockOutputText` fall
    // back to an empty read, so the model saw no output at all far more
    // often than not.
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
        })
      }
      if (method === 'ledger.artifact') {
        const line = JSON.stringify({
          from: 0,
          row: wireRowOf(Array.from('the real output', (ch) => [ch, 1, true] as CellSpec)),
        })
        return Promise.resolve({
          id: 'art-rows',
          mediaType: 'application/x-nocx-rows',
          body: `${line}\n`,
          truncated: null,
          byteLen: line.length,
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      const pending = content.submitAgentCommand('printf the-real-output')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'printf the-real-output',
        },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      historyRecordedHandler(client)({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'assistant',
        redactions: [],
        maskedCommand: 'printf the-real-output',
        captures: [],
      })
      // The fence — nothing else — settles the visual boundary; no
      // block.grew or block.closed notification is ever dispatched here.
      blockClosedHandler(client)('att-1')
      const run = await pending
      expect(run.text).toBe('the real output')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  // ── the block closes on the backend's block.closed, and on nothing else ──
  // (nocx-2v80t.3.27, ADR-0066). The renderer's own fence sighting used to
  // decide when a finished command's block closed: a fence callback that
  // never came, or a fence in the alternate buffer (ignored), left the
  // backend's block sealed while the block on screen and an agent run's
  // completion waited forever.
  describe("a block closes on the backend's block.closed alone (nocx-2v80t.3.27)", () => {
    async function runToCompletion(opts: { alternate?: boolean } = {}) {
      const client = makeClient()
      const { content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const handler = factHandler(client)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      const run = content.submitAgentCommand('vim notes.txt')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'vim notes.txt',
        },
      })
      // A full-screen program owns the terminal when its command ends.
      if (opts.alternate) fireBufferState(sessionOf(content), 'alternate', 20)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'c'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      historyRecordedHandler(client)({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'assistant',
        redactions: [],
        maskedCommand: 'vim notes.txt',
        captures: [],
      })
      const block = () => withScrollback.scrollback.blockManager.blockForAttempt('att-1')!
      return { client, run, block, teardown }
    }

    it('with no fence callback at all, the block finishes when the backend says block.closed', async () => {
      const { client, run, block, teardown } = await runToCompletion()
      try {
        // The block is logically done, and not yet closed on screen: the
        // backend has not said its rows are whole.
        expect(block().status).toBe('success')
        expect(block().el.classList.contains('cmd-block-running')).toBe(true)

        blockClosedHandler(client)('att-1')

        expect(block().el.classList.contains('cmd-block-running')).toBe(false)
        const done = await run
        expect(done.status).toBe('success')
        expect(done.exitCode).toBe(0)
      } finally {
        teardown()
      }
    })

    it('a command that ends inside the alternate buffer still finishes on block.closed', async () => {
      const { client, run, block, teardown } = await runToCompletion({ alternate: true })
      try {
        blockClosedHandler(client)('att-1')
        expect(block().el.classList.contains('cmd-block-running')).toBe(false)
        expect((await run).status).toBe('success')
      } finally {
        teardown()
      }
    })

    it('an attempt gone unknown keeps its block open on screen until block.closed, then closes it (nocx-2v80t.3.30)', async () => {
      const client = makeClient()
      const { content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const handler = factHandler(client)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      try {
        content.setVisible(true)
        handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
        const run = content.submitAgentCommand('sleep 100')
        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: {
            id: 'att-1',
            state: 'open',
            origin: 'app',
            submitId: submitToken(client),
            command: 'sleep 100',
          },
        })
        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: { id: 'att-1', state: 'unknown' },
        })
        const block = () => withScrollback.scrollback.blockManager.blockForAttempt('att-1')!
        expect(block().status).toBe('unknown')
        expect(block().el.classList.contains('cmd-block-running')).toBe(true)

        blockClosedHandler(client)('att-1')
        expect(block().el.classList.contains('cmd-block-running')).toBe(false)
        expect((await run).status).toBe('unknown')
      } finally {
        teardown()
      }
    })

    it('a block.closed that arrives before the completion closes the block the moment the completion lands', async () => {
      const client = makeClient()
      const { content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const handler = factHandler(client)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      try {
        content.setVisible(true)
        handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
        const run = content.submitAgentCommand('true')
        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: {
            id: 'att-1',
            state: 'open',
            origin: 'app',
            submitId: submitToken(client),
            command: 'true',
          },
        })
        blockClosedHandler(client)('att-1')
        const block = () => withScrollback.scrollback.blockManager.blockForAttempt('att-1')!
        // Rows whole is not the command done: the block keeps running until
        // the authenticated completion says how it ended.
        expect(block().status).toBe('running')

        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: {
            id: 'att-1',
            state: 'completed',
            exitCode: 0,
            fence: 'd'.repeat(64),
            completedAt: '2026-08-08T12:00:02Z',
          },
        })
        historyRecordedHandler(client)({
          sessionId: client._sessions[0].sessionId,
          attemptId: 'att-1',
          maskedCount: 0,
          maskedKinds: [],
          entryId: 'e1',
          source: 'assistant',
          redactions: [],
          maskedCommand: 'true',
          captures: [],
        })
        expect(block().el.classList.contains('cmd-block-running')).toBe(false)
        expect((await run).status).toBe('success')
      } finally {
        teardown()
      }
    })

    it('an agent run on a kept block reports the rows the closing read returned, not the ones it already had', async () => {
      // The block already holds rows from a block.grew when it completes; an
      // answer read from those would miss the closing screen.
      let final = false
      const rowsBody = (text: string) =>
        `${JSON.stringify({
          from: 0,
          row: wireRowOf(Array.from(text, (ch) => [ch, 1, true] as CellSpec)),
        })}\n`
      const client = makeClient()
      client.call.mockImplementation((method: string) => {
        if (method === 'ledger.get') {
          return Promise.resolve({
            entry: {},
            edges: [],
            artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
          })
        }
        if (method === 'ledger.artifact') {
          const body = rowsBody(final ? 'closing screen' : 'partial')
          return Promise.resolve({
            id: 'art-rows',
            mediaType: 'application/x-nocx-rows',
            body,
            truncated: null,
            byteLen: body.length,
          })
        }
        return Promise.reject(new Error('no store wired (fake)'))
      })
      const { content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const handler = factHandler(client)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      try {
        content.setVisible(true)
        handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
        const run = content.submitAgentCommand('make')
        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: {
            id: 'att-1',
            state: 'open',
            origin: 'app',
            submitId: submitToken(client),
            command: 'make',
          },
        })
        const grew = client.dispatcher.subscribe.mock.calls.find(
          ([method]) => method === 'block.grew',
        )?.[1] as (params: unknown) => void
        grew({ entryId: 'att-1', from: 0, count: 1 })
        const block = () => withScrollback.scrollback.blockManager.blockForAttempt('att-1')!
        await vi.waitFor(() => expect(block().storedRows).toBeDefined())

        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: {
            id: 'att-1',
            state: 'completed',
            exitCode: 0,
            fence: 'e'.repeat(64),
            completedAt: '2026-08-08T12:00:02Z',
          },
        })
        historyRecordedHandler(client)({
          sessionId: client._sessions[0].sessionId,
          attemptId: 'att-1',
          maskedCount: 0,
          maskedKinds: [],
          entryId: 'e1',
          source: 'assistant',
          redactions: [],
          maskedCommand: 'make',
          captures: [],
        })
        final = true
        blockClosedHandler(client)('att-1', true)
        expect((await run).text).toBe('closing screen')
      } finally {
        teardown()
      }
    })
  })

  // ── a stored-rows read that failed is said, never drawn as empty ──────
  // (nocx-2v80t.3.27). The successful read these pair with is the test just
  // above: the same agent run, the same freeze, and the rows arrive.
  describe('a block whose stored rows could not be read says so (nocx-2v80t.3.27)', () => {
    /** Drive one agent command to its freeze, with the store answering the
     *  rows read through `rowsRead`; returns the run and the frozen block. */
    async function agentRunWithRowsRead(
      rowsRead: (method: string) => Promise<unknown> | undefined,
    ): Promise<{
      run: Promise<AgentRunCompletion>
      block: () => HTMLElement
      teardown: () => void
    }> {
      const client = makeClient()
      client.call.mockImplementation((method: string) => {
        const answer = rowsRead(method)
        if (answer !== undefined) return answer
        return Promise.reject(new Error('no store wired (fake)'))
      })
      const { content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const handler = factHandler(client)
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      const run = content.submitAgentCommand('printf the-real-output')
      // Observed before anything else can: an unhandled rejection between
      // here and the test's own await would fail the file, not the test.
      run.catch(() => {})
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'printf the-real-output',
        },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      historyRecordedHandler(client)({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'assistant',
        redactions: [],
        maskedCommand: 'printf the-real-output',
        captures: [],
      })
      blockClosedHandler(client)('att-1')
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      const block = () => withScrollback.scrollback.blockManager.blockForAttempt('att-1')!.el
      return { run, block, teardown }
    }

    const ROWS_ENTRY = {
      entry: {},
      edges: [],
      artifacts: [{ id: 'art-rows', mediaType: 'application/x-nocx-rows' }],
    }

    it('an agent run whose rows read FAILED reports an error, and the block says its output could not be read', async () => {
      const { run, block, teardown } = await agentRunWithRowsRead((method) => {
        if (method === 'ledger.get') return Promise.resolve(ROWS_ENTRY)
        if (method === 'ledger.artifact') return Promise.reject(new Error('artifact read refused'))
        return undefined
      })
      try {
        await expect(run).rejects.toThrow(/could not be read/)
        const notice = block().querySelector('[data-output-unreadable]')
        expect(notice?.textContent).toContain('could not be read')
      } finally {
        teardown()
      }
    })

    it('an agent run whose stored rows are MALFORMED reports an error, and the block says so', async () => {
      const { run, block, teardown } = await agentRunWithRowsRead((method) => {
        if (method === 'ledger.get') return Promise.resolve(ROWS_ENTRY)
        if (method === 'ledger.artifact') {
          return Promise.resolve({
            id: 'art-rows',
            mediaType: 'application/x-nocx-rows',
            body: '{"from":12}\n',
            truncated: null,
            byteLen: 12,
          })
        }
        return undefined
      })
      try {
        await expect(run).rejects.toThrow(/malformed/)
        expect(block().querySelector('[data-output-unreadable]')).not.toBeNull()
      } finally {
        teardown()
      }
    })

    it('a successful read after a failed one takes the notice back and paints the rows', async () => {
      let fail = true
      const line = JSON.stringify({
        from: 0,
        row: wireRowOf(Array.from('the real output', (ch) => [ch, 1, true] as CellSpec)),
      })
      const client = makeClient()
      client.call.mockImplementation((method: string) => {
        if (method === 'ledger.get') return Promise.resolve(ROWS_ENTRY)
        if (method === 'ledger.artifact') {
          if (fail) return Promise.reject(new Error('socket closed'))
          return Promise.resolve({
            id: 'art-rows',
            mediaType: 'application/x-nocx-rows',
            body: `${line}\n`,
            truncated: null,
            byteLen: line.length,
          })
        }
        return Promise.reject(new Error('no store wired (fake)'))
      })
      const { view, ed, content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const handler = factHandler(client)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      try {
        content.setVisible(true)
        handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
        ed.insertText('make')
        view.contentDOM.dispatchEvent(
          new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
        )
        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: {
            id: 'att-1',
            state: 'open',
            origin: 'app',
            submitId: submitToken(client),
            command: 'make',
          },
        })
        const notify = (name: string) =>
          client.dispatcher.subscribe.mock.calls.find(([method]) => method === name)?.[1] as (
            params: unknown,
          ) => void
        const block = () => withScrollback.scrollback.blockManager.blockForAttempt('att-1')!.el

        notify('block.grew')({ entryId: 'att-1', from: 0, count: 1 })
        await vi.waitFor(() =>
          expect(block().querySelector('[data-output-unreadable]')).not.toBeNull(),
        )

        fail = false
        notify('block.closed')({ entryId: 'att-1', kept: true })
        await vi.waitFor(() => expect(blockOutputText(block())).toBe('the real output'))
        expect(block().querySelector('[data-output-unreadable]')).toBeNull()
      } finally {
        teardown()
      }
    })
  })

  it('an agent command the store wrote no row for resolves naming no entry at all (nocx-9sqii)', async () => {
    // History is off, or the record was dropped: the ack names no row. The
    // command still RAN and its output is the tool's result, so the run
    // resolves — with nothing where the entry id goes, which is the honest
    // answer and the one the backend degrades on (no join, plain ledger
    // order). Answering the renderer's own record number here instead is
    // what made the join fail silently when there WAS a row.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      const pending = content.submitAgentCommand('make')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make',
        },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      historyRecordedHandler(client)({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: '',
        source: 'assistant',
        redactions: [],
        maskedCommand: 'make',
        captures: [],
      })
      // The shell's fence lands after the output — the sighting the visual
      // boundary resolves on, and the only thing that cuts it (nocx-2v80t.3.2).
      blockClosedHandler(client)('att-1')
      const run = await pending
      expect(run.entryId).toBe('')
      // And the command's own outcome is unaffected: a missing row costs
      // the arrangement, never the result.
      expect(run.status).toBe('success')
      expect(run.exitCode).toBe(0)
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it("submitAgentCommand marks the agent's command in the flow and leaves the human's submissions untouched (nocx-tjppv, criterion 5)", async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const ledger = (content as unknown as { ledger: CommandLedger }).ledger
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })

      // The agent's command first, completed through the ordinary path.
      const pendingAgent = content.submitAgentCommand('agent-command')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'agent-command',
        },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      historyRecordedHandler(client)({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'assistant',
        redactions: [],
        maskedCommand: 'agent-command',
        captures: [],
      })
      // The shell's fence lands after the output — the sighting the visual
      // boundary resolves on, and the only thing that cuts it (nocx-2v80t.3.2).
      blockClosedHandler(client)('att-1')
      await pendingAgent

      // The human's command, through the same content's editor: still the
      // human's, in the same ledger, on the same flow.
      ed.insertText('human-command')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

      // The human's card opens on its own authenticated start, the way the
      // agent's did.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-2',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'human-command',
        },
      })

      const records = ledger.records()
      expect(records[0].author).toBe('agent')
      expect(records[1].author).toBe('shell')
      expect(withScrollback.scrollback.blockManager.blocks[0].author).toBe('agent')
      expect(withScrollback.scrollback.blockManager.blocks[1].author).toBe('shell')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('reports accepted backend frame buffer state to the lane, not the renderer parser (ADR-0020 decision 3)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const laneCalls = () =>
        client.call.mock.calls.filter((c) => c[0] === 'agent.laneInteractivity')
      const session = sessionOf(content)

      // No frame has been accepted yet, so there is no lane buffer state.
      expect(laneCalls()).toEqual([])

      // The lane follows the same accepted state as the painted screen.
      fireBufferState(session, 'alternate', 20)
      expect(laneCalls()).toEqual([
        ['agent.laneInteractivity', { sessionId: session.sessionId, bufferKind: 'alternate' }],
      ])
      fireBufferState(session, 'normal', 21)
      expect(laneCalls()).toEqual([
        ['agent.laneInteractivity', { sessionId: session.sessionId, bufferKind: 'alternate' }],
        ['agent.laneInteractivity', { sessionId: session.sessionId, bufferKind: 'normal' }],
      ])
    } finally {
      teardown()
    }
  })

  it('the whole authenticated cycle: submit attaches, output stays visible, the completion freezes the block, the status persists exactly once, and the next command reaches the shell', async () => {
    // The epic's positive criterion, watched end to end through the real
    // composition root (ADR-0024 §5–§7): in an authenticated session the
    // user submits a command, the authenticated start attaches to the app
    // attempt, ALL output stays visible, Complete plus fence freezes the
    // complete block, the exit status persists exactly once, PromptReady
    // returns the editor, and the next submitted command reaches the shell.
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const recorded = historyRecordedHandler(client)
    const handler = factHandler(client)
    const session = sessionOf(content)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)

      // 1. The authenticated prompt gives the editor the keyboard.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)

      // 2. The user submits; the command reaches the shell BEFORE any
      //    published start. The submit opens the ledger record and the
      //    attempt — NO card: the card waits for the runtime's word
      //    (nocx-2v80t.3.2).
      ed.insertText('echo hello')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)
      // Paste and Enter reach the session in order through the intent queue.
      await vi.waitFor(() =>
        expect(sessionOf(content).intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'echo hello'],
          ['key', 'enter'],
        ]),
      )

      // 3. The authenticated start attaches to the app attempt; the pane
      //    stays in the running layout with the block visible — output is
      //    never hidden by the lifecycle (nocx-u7uh.25).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'echo hello',
        },
      })
      // The authenticated start opened the card, running in the visible
      // layout (nocx-u7uh.25).
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')
      expect(withScrollback.scrollback.mode).toBe('running')
      expect(
        withScrollback.scrollback.scrollbackInner.classList.contains('inner-fullscreen-mode'),
      ).toBe(false)

      // 4. Output lands as an accepted backend frame and stays visible while
      // the command runs; it is not serialized from client-side terminal bytes.
      session.fireScreenFrame(screenFrame(1, ['hello']))
      await vi.waitFor(() =>
        expect(withScrollback.scrollback.terminalLiveContainer.textContent).toContain('hello'),
      )
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')

      // 5. Complete plus fence freezes the complete block with the
      //    authenticated status.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 1,
          fence: 'b'.repeat(64),
          completedAt: '2026-08-09T00:00:02Z',
        },
      })
      // The shell's fence lands after the output — the sighting the visual
      // boundary resolves on, and the only thing that cuts it (nocx-2v80t.3.2).
      blockClosedHandler(client)('att-1')
      const frozen = withScrollback.scrollback.blockManager.blocks[0]
      expect(frozen.status).toBe('failure')
      expect(frozen.exitCode).toBe(1)
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()

      // 6. The backend-owned receipt crosses the notification subscription
      // exactly once for the completed app-owned attempt.
      recorded({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-1',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e1',
        source: 'user',
        redactions: [],
        captures: [],
        maskedCommand: 'echo hello',
      })
      expect(client.dispatcher.subscribe).toHaveBeenCalledWith(
        'history.recorded',
        expect.any(Function),
      )

      // 7. PromptReady returns the editor.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      // Ignore the editor's focus transitions; this assertion is about the
      // next shell submission's ordered input intents.
      sessionOf(content).intent.mockClear()

      // 8. The next submitted command reaches the shell and opens a fresh
      //    attempt — the cycle restarts, not the previous block.
      ed.insertText('echo again')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() =>
        expect(sessionOf(content).intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'echo again'],
          ['key', 'enter'],
        ]),
      )
      // The second command's card opens on its own authenticated start.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-2',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'echo again',
        },
      })
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(2)
      expect(withScrollback.scrollback.blockManager.blocks[1].status).toBe('running')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('a desynchronized or lost domain keeps the conventional unstructured grid (ADR-0024 §4, §9)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = factHandler(client)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      // The kernel starts Native: a conventional terminal, full-pane grid.
      expect(withScrollback.scrollback.mode).toBe('unstructured')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // A desynchronized domain is not live (decision 9): its terminal
      // stays visible and the block model never takes over — the pane is
      // the conventional unstructured grid, blocks hidden.
      handler({ lane: 'lane-1', lifecycle: 'desynchronized', domain: 'd1', epoch: 1 })
      expect(withScrollback.scrollback.mode).toBe('unstructured')
      expect(
        withScrollback.scrollback.scrollbackInner.classList.contains('inner-fullscreen-mode'),
      ).toBe(true)
      // Loss is conventional too: the grid stays, the block model stays out.
      handler({ lane: 'lane-1', lifecycle: 'lost' })
      expect(withScrollback.scrollback.mode).toBe('unstructured')
    } finally {
      teardown()
    }
  })

  it('the grid turns writable when the command starts — raw input is never dropped (nocx-u7uh.23)', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const input = intentElementOf(content)
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      expect(ed.root.contains(document.activeElement)).toBe(true)

      // The atomic handoff: the editor leaves the layout at commit, box and
      // all (nocx-g6hnk), and the submit callback makes the grid writable in
      // the same synchronous step the bytes go out — keys typed before the
      // running fact lands reach the pty. The ORDER is what this asserts;
      // where the composer's 77px went is asserted in editor.test.ts.
      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(ed.isVisible).toBe(false)
      expect(ed.root.style.display).toBe('none')
      expect(document.activeElement).toBe(input)

      // The published attempt opens the running interval; the sync keeps
      // the grid writable — a program waiting on stdin (read, ssh, less)
      // is fed by raw keys with no editor and no input surface lost.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'read x',
        },
      })
      // Still gone, still writable: the lifecycle only RECONCILES here — the
      // commit already removed the composer — and reconciling must not
      // resurrect it or take the keyboard back off the program.
      expect(ed.isVisible).toBe(false)
      expect(ed.root.style.display).toBe('none')
      expect(document.activeElement).toBe(input)
      // The completion closes the interval, and back at the prompt the
      // editor returns and the grid locks again.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      expect(ed.root.style.visibility).toBe('')
      expect(ed.root.dataset.suspended).toBeUndefined()
      expect(ed.root.hasAttribute('inert')).toBe(false)
      expect(ed.root.contains(document.activeElement)).toBe(true)
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('the grid takes the keyboard in the same step the editor gives it up (nocx-yb5y)', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      // The keyboard belongs to the pane's INPUT ELEMENT since nocx-zg3k3.3.1,
      // so the handoff is asserted where it lands rather than on the renderer
      // that used to hold it.
      const focusElement = vi.spyOn(intentElementOf(content), 'focus')
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      focusElement.mockClear()

      // WRITABLE IS NOT ENOUGH — the grid must also be FOCUSED, and in the
      // same synchronous step. The editor gives the keyboard up at commit
      // (clearDoc → hide, and a display:none host drops the browser's focus
      // to <body>), while at a live prompt the paste is deferred behind the
      // lifecycle.submitAttempt round trip — and renderer.focus() used to
      // ride along with it. For that whole round trip nobody owned the
      // keyboard: keys went to <body> and were gone, with no editor to show
      // them and no grid to send them.
      //
      // A user typing into a program that is already reading stdin loses the
      // letters and keeps the Enter — so an ssh password prompt is answered
      // with an empty line, silently (nocx-yb5y).
      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(ed.isVisible).toBe(false)
      // Synchronously: no await between the dispatch and this assertion, so
      // the RPC cannot have resolved and this can only be the handoff.
      expect(focusElement).toHaveBeenCalled()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('a running command reclaims the keyboard from an unclaimed <body> (nocx-1ebrs.66.1.16)', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const input = intentElementOf(content)
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(document.activeElement).toBe(input)

      // The commit hands the keyboard over synchronously, but nothing says
      // it STAYS there across the lifecycle.submitAttempt round trip: the
      // editor's display:none host and the live region's move are both
      // focus-changing DOM operations. If the keyboard is on <body> when
      // the authenticated start lands, the command is running and nobody
      // owns the keys typed before the deferred focusGrid repair — the
      // letters are lost while the Enter that follows them is not
      // (nocx-1ebrs.66.1.16). The running fact must reclaim an UNCLAIMED
      // keyboard; a control's focus is still its own.
      input.blur()
      expect(document.activeElement).toBe(document.body)

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'read x',
        },
      })
      expect(document.activeElement).toBe(input)
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('keys typed at an unclaimed keyboard during the submit reach the pty BEHIND it (nocx-1ebrs.66.1.19)', async () => {
    const client = makeClient()
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    client.dispatcher.call.mockImplementation(() => attemptPromise)
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, visibleBeforeMount: true },
      client,
    )
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const session = sessionOf(content)
      const input = intentElementOf(content)
      /** What the program receives as INPUT, in the order it was produced.
       *  The focus reports are events a program sees too, but the subject
       *  here is the bytes: a keystroke that reached <body> and one that
       *  reached the grid must be indistinguishable at the pty. */
      const sent = (): unknown[] => session.intent.mock.calls.filter((call) => call[0] !== 'focus')
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      session.send.mockClear()

      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(document.activeElement).toBe(input)
      // The commit's handoff does not survive the round trip: the editor's
      // display:none host and the live region's move are focus-changing DOM
      // operations, and on WebKit either can land AFTER the synchronous
      // focus. What is left is a keyboard nobody owns, and no repair until
      // the running fact — which is the other end of this RPC
      // (nocx-1ebrs.66.1.19).
      input.blur()
      expect(document.activeElement).toBe(document.body)

      // THE PERSON TYPES. This keydown is delivered at <body>, so no element
      // reads it and NO INPUT EVENT FOLLOWS: the character exists only in
      // this event. Losing it is the defect — the letters go and the Enter
      // after them stays, so a program reading stdin is answered with an
      // empty line, silently.
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'h', code: 'KeyH', bubbles: true, cancelable: true }),
      )
      // The keyboard came with the key, so what follows is ordinary typing.
      typeIntoPane(content, 'i', '\r')

      // Held, all of it: the command it answers has not been sent yet, and
      // the answer must not arrive before the question. (The one intent so
      // far is the commit's own handoff focus — not input.)
      expect(sent()).toEqual([])
      expect(session.send).not.toHaveBeenCalled()

      resolveAttempt({
        id: 'att-read',
        domain: 'd1',
        state: 'open',
        command: 'read x',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      // Behind the command, in the order they were produced — the focus
      // reports included, because they are what the program would have seen
      // had the keyboard never been dropped.
      await vi.waitFor(() =>
        expect(sent()).toEqual([
          ['paste', 'read x'],
          ['key', 'enter'],
          ['text', 'hi'],
          ['key', 'enter'],
        ]),
      )
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('a key typed after the running fact reaches the pty when the keyboard is dropped again (nocx-1ebrs.66.1.19)', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, visibleBeforeMount: true },
      client,
    )
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      const session = sessionOf(content)
      const input = intentElementOf(content)
      /** What the program receives as INPUT, in the order it was produced.
       *  The focus reports are events a program sees too, but the subject
       *  here is the bytes: a keystroke that reached <body> and one that
       *  reached the grid must be indistinguishable at the pty. */
      const sent = (): unknown[] => session.intent.mock.calls.filter((call) => call[0] !== 'focus')
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      session.send.mockClear()

      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() =>
        expect(sent()).toEqual([
          ['paste', 'read x'],
          ['key', 'enter'],
        ]),
      )

      // The command is running and the fact reclaimed the keyboard — that is
      // 66.1.16's repair, and it works. What it cannot cover is the SAME
      // focus drop arriving after it: one more display:none or live-region
      // move, and no further fact is coming to repair anything
      // (nocx-1ebrs.66.1.19).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-0',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'read x',
        },
      })
      expect(document.activeElement).toBe(input)

      input.blur()
      expect(document.activeElement).toBe(document.body)
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'h', code: 'KeyH', bubbles: true, cancelable: true }),
      )
      typeIntoPane(content, 'i', '\r')

      // Routed, not buffered: nothing is in flight, so the keys are the
      // program's the moment they are typed. The out/in pair is not noise —
      // it is the program being told the keyboard came back.
      await vi.waitFor(() =>
        expect(sent()).toEqual([
          ['paste', 'read x'],
          ['key', 'enter'],
          ['text', 'hi'],
          ['key', 'enter'],
        ]),
      )
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('keeps session intents ordered when the control call is still in flight', async () => {
    const { content, teardown } = await mountTerminal(makeClipboard())
    try {
      const session = sessionOf(content)
      let finishFirst!: (result: SessionIntentResult) => void
      const first = new Promise<SessionIntentResult>((resolve) => {
        finishFirst = resolve
      })
      session.intent.mockImplementationOnce(() => first)

      typeIntoPane(content, 'a', 'b', 'c', '\r')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledTimes(1))
      expect(session.intent.mock.calls).toEqual([['text', 'a']])

      finishFirst({ state: 'executed', bytesWritten: 1, fenceAfter: 0 })
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledTimes(3))
      expect(session.intent.mock.calls).toEqual([
        ['text', 'a'],
        ['text', 'bc'],
        ['key', 'enter'],
      ])
    } finally {
      teardown()
    }
  })

  it('keys typed while the command is still in flight reach the pty BEHIND it (nocx-yb5y)', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const session = sessionOf(content)
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      session.send.mockClear()

      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // At a LIVE prompt the pty write waits on lifecycle.submitAttempt
      // (ADR-0024 §5), while the keyboard changed hands at the commit. So
      // this is a real window a user types into: `read` is what they are
      // answering, and the answer must not arrive before the question.
      typeIntoPane(content, 'h', 'i', '\r')
      // Held: nothing of it has been sent yet, as bytes or as intent.
      expect(session.send).not.toHaveBeenCalled()
      expect(session.intent).not.toHaveBeenCalled()

      // The attempt settles, the command goes out, and what was typed
      // behind it follows — in the order it was typed.
      // The command paste and Enter are ahead of the text and key intents
      // captured while submit was in flight. All four travel on one queue.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['paste', 'read x'],
          ['key', 'enter'],
          ['text', 'hi'],
          ['key', 'enter'],
        ]),
      )
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('a Ctrl-C while the command is still in flight cancels the submit (nocx-xn63t.6.12)', async () => {
    const client = makeClient()
    const submitAttempt = client.dispatcher.call
    // Promise.withResolvers needs ES2024 and this project targets ES2021, so
    // the resolver is captured via the executor form (the codebase pattern).
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    submitAttempt.mockImplementation(() => attemptPromise)
    const { content, ed, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const session = sessionOf(content)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      session.send.mockClear()

      ed.insertText('echo RACE')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The round trip is in flight: the attempt was opened with the
      // app-owned text, and not one byte has reached the pty. NO card
      // exists yet — the submit opens none, and the runtime has said
      // nothing (nocx-2v80t.3.2).
      expect(submitAttempt).toHaveBeenCalledWith(
        'lifecycle.submitAttempt',
        expect.objectContaining({ command: 'echo RACE' }),
      )
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)
      expect(session.send).not.toHaveBeenCalled()

      // Ctrl-C inside that window, which is the one path that turns bash
      // 5.2's parse-resume race into a certainty: the held keys are flushed
      // immediately behind the submitted line, so the pty would receive
      // `echo RACE\r\x03` back to back and the shell can notice QUIT while
      // its parser is consuming the accepted line — running what is left of
      // it, which is not the command that was typed. The person's own
      // gesture says the line is not to run, so it never goes out.
      typeIntoPane(content, '\x03')

      resolveAttempt({
        id: 'att-cancelled',
        domain: 'd1',
        state: 'open',
        command: 'echo RACE',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      // Waited on through the submission's own write path, never on a
      // duration: the RPC settled, the write was declined — the command's
      // bytes never reached the renderer's paste — and no card ever
      // existed for the person to watch close. The ledger record stays
      // unbound and running (it persists nothing). A card frozen `unknown`
      // for a line that never ran would be the client inventing a boundary
      // again (nocx-2v80t.3.2).
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)
      // Neither the command nor the 0x03. The cancel is served by the
      // withdrawal itself: nothing is running at the shell to interrupt, and
      // a byte written here would be an interrupt the person never asked
      // for — the second-owner defect the grid's own Ctrl+C already refuses.
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('the Ctrl-C discards the line it cancelled, and not the bytes after it (nocx-xn63t.6.12)', async () => {
    const client = makeClient()
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    client.dispatcher.call.mockImplementation(() => attemptPromise)
    const { content, ed, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const session = sessionOf(content)
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      session.send.mockClear()

      ed.insertText('echo RACE')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // Keys typed while the command was in flight are part of the pending
      // line, and ^C discards that line — a terminal with ISIG set flushes
      // the input queue it has not read yet, which is what every shell does
      // with the half-typed command somebody interrupts.
      typeIntoPane(content, 'ab')
      expect(session.send).not.toHaveBeenCalled()

      // What followed the ^C is not part of the line it discarded: after the
      // flush the shell is at a fresh prompt, and these bytes belong to it,
      // so they are delivered (the old behaviour for everything that is not
      // the pending line) rather than swallowed with it.
      // Three acts, in the order a person made them: the interrupt, then the
      // two keys typed at the fresh prompt it leaves behind.
      typeIntoPane(content, '\x03', 'c', 'd')
      // 'c' and 'd' are NOT part of the line the interrupt discarded: after the
      // flush the shell is at a fresh prompt, and they belong to it. They go out
      // as intent, at once, rather than being swallowed with the line.
      expect(session.send).not.toHaveBeenCalled()
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['text', 'c'],
          ['text', 'd'],
        ]),
      )

      resolveAttempt({
        id: 'att-cancelled',
        domain: 'd1',
        state: 'open',
        command: 'echo RACE',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      await vi.waitFor(() => expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull())
      // The command never ran, and 'cd' is the only byte the pty ever saw.
      expect(session.send).not.toHaveBeenCalled()
      expect(session.intent.mock.calls).toEqual([
        ['text', 'c'],
        ['text', 'd'],
      ])
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })

  it('a Ctrl-C after the command has gone out is delivered as it always was (nocx-xn63t.6.12)', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const session = sessionOf(content)
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      session.send.mockClear()

      ed.insertText('read x')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // This fake's attempt answers on its own, so the ordered paste and
      // Enter intents finish before there is no pending line left for ^C.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['paste', 'read x'],
          ['key', 'enter'],
        ]),
      )
      typeIntoPane(content, '\x03')

      // A Ctrl-C with no pending line is the shell's own interrupt, and it goes
      // out as the KEY the person pressed: what it means at the pty is the
      // runtime's decision, never this client's (nocx-zg3k3.3.1).
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toEqual([
          ['paste', 'read x'],
          ['key', 'enter'],
          ['key', 'Ctrl+c'],
        ]),
      )
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      teardown()
    }
  })
})

describe('two attempts and the live region stay separate while running (nocx-m87n, nocx-zn4d, nocx-mu8s)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the wire seam tests deliver authenticated facts through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    const subscribe = client.dispatcher.subscribe
    expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
    return lifecycleHandler(client)
  }

  it('two shell-originated attempts keep their own blocks while the first is still settling its fence (nocx-m87n)', async () => {
    // The owner's report: run `codex` (an inline TUI, no alternate buffer),
    // Ctrl-C it, type `codex` again. While the second run is live the pane
    // showed ONE running block whose body held both runs' content; on exit
    // the same content rendered as two correct frozen blocks. The attempt
    // model was right the whole time (both completions landed with the
    // right durations) — the defect is in the render: the first block must
    // freeze while the second command runs, each with its own exit status.
    // The second command is SHELL-originated (keys were raw), so it opens
    // through the openBlock port, not the editor submit.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      // The authenticated prompt; the first command is typed at the shell.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'codex' },
      })
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')
      expect(withScrollback.scrollback.mode).toBe('running')

      // Ctrl-C: the authenticated completion lands with its fence still in
      // flight — the LOGICAL freeze flips the status and frees the running
      // slot; the VISUAL boundary defers (u7uh.8) and the live region stays
      // up until it settles.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 130,
          fence: 'f'.repeat(64),
          completedAt: '2026-08-09T00:00:08Z',
        },
      })
      const first = withScrollback.scrollback.blockManager.blockForAttempt('att-1')
      expect(first?.status).toBe('failure')
      expect(first?.exitCode).toBe(130)
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()

      // The shell is back at the prompt and the user types `codex` again:
      // a SECOND shell-originated attempt opens its own block — the first
      // block's visual boundary is still pending, so the running slot must
      // be free for it.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-2', state: 'open', origin: 'shell', command: 'codex' },
      })
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(2)
      const second = withScrollback.scrollback.blockManager.blockForAttempt('att-2')
      expect(second).not.toBeNull()
      expect(second!.status).toBe('running')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBe(second)

      // The first fence lands while the second command runs: the first
      // block freezes with its own exit status, the second stays running,
      // and the live region belongs to the second command.
      blockClosedHandler(client)('att-1')
      const firstAfter = withScrollback.scrollback.blockManager.blockForAttempt('att-1')
      expect(firstAfter?.status).toBe('failure')
      expect(firstAfter?.exitCode).toBe(130)
      expect(firstAfter?.el.classList.contains('cmd-block-running')).toBe(false)
      const secondAfter = withScrollback.scrollback.blockManager.blockForAttempt('att-2')
      expect(secondAfter?.status).toBe('running')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBe(secondAfter)
      expect(withScrollback.scrollback.mode).toBe('running')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('the app-owned attempt opens the block before the bytes and marks the echo line outside the output range (nocx-4yhi)', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      ed.insertText('ls')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // The authenticated start opens the block before the bytes. Its output
      // anchor remains unknown until the backend publishes screen coordinates.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'ls',
        },
      })
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      expect(block!.startLine).toBe(0)
      expect(block!.outputStart).toBeUndefined()
    } finally {
      teardown()
    }
  })

  it('a shell-originated block\u2019s output range starts at the cursor line — its echo preceded the running fact (nocx-4yhi)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    try {
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'pwd' },
      })
      const block = withScrollback.scrollback.blockManager.blockForAttempt('att-1')
      expect(block).not.toBeNull()
      // Renderer cursor geometry is not an output anchor.
      expect(block!.outputStart).toBeUndefined()
    } finally {
      teardown()
    }
  })

  it('wires backend output anchors to the attempt and uses only a matching accepted screen frame', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    const anchor = {
      entryId: 'att-anchor',
      row: 12,
      screenRevision: 5,
      bufferKind: 'normal' as const,
      bufferInstance: '1',
      cols: SCREEN_COLS,
      rows: SCREEN_ROWS,
    }
    try {
      // The notification can beat the authenticated running fact.
      client._fireBlockOutputStart(anchor)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-anchor', state: 'open', origin: 'shell', command: 'ls' },
      })
      const block = scrollback.blockManager.blockForAttempt('att-anchor')!
      expect(block.outputStart).toBeUndefined()
      expect(block.outputAnchor).toEqual(anchor)

      const older = screenFrame(4, ['prompt ls', '', ''])
      older.state.baseRow = 10
      client._sessions[0].fireScreenFrame(older)
      expect(block.outputAnchorOffset).toBeUndefined()

      const alternate = screenFrame(5, ['prompt ls', '', ''])
      alternate.state.bufferKind = 'alternate'
      alternate.state.baseRow = 10
      client._sessions[0].fireScreenFrame(alternate)
      expect(block.outputAnchorOffset).toBeUndefined()

      const wrongInstance = screenFrame(6, ['prompt ls', '', ''])
      wrongInstance.state.baseRow = 10
      wrongInstance.state.bufferInstance = '2'
      client._sessions[0].fireScreenFrame(wrongInstance)
      expect(block.outputAnchorOffset).toBeUndefined()

      // A frame of another width is the estimate a reflow allows, and the
      // echo row it names is read like any other (nocx-zg3k3.32).
      const otherWidth = screenFrame(7, ['prompt ls', '', ''], { cols: SCREEN_COLS + 1 })
      otherWidth.state.baseRow = 10
      client._sessions[0].fireScreenFrame(otherWidth)
      expect(block.outputAnchorOffset).toBe(2)
      expect(block.outputAnchorText).toBe('')

      const accepted = screenFrame(8, ['', 'prompt ls', ''])
      accepted.state.baseRow = 10
      client._sessions[0].fireScreenFrame(accepted)
      expect(block.outputAnchor).toEqual(anchor)
      expect(block.outputAnchorOffset).toBe(2)
      expect(block.outputAnchorText).toBe('prompt ls')
      expect(block.outputStart).toBe(12)
      const mismatchAfterAcceptance = screenFrame(9, ['', '', 'replacement'])
      mismatchAfterAcceptance.state.baseRow = 10
      mismatchAfterAcceptance.state.bufferInstance = '2'
      client._sessions[0].fireScreenFrame(mismatchAfterAcceptance)
      expect(block.outputAnchorOffset).toBe(2)
      expect(block.outputAnchorText).toBe('prompt ls')

      // One accepted output row below the command echo must occupy one row
      // in the live clip. The controller subtracts the two-row echo offset;
      // liveContentHeight therefore supplies the full three-row grid height.
      Object.defineProperty(scrollback.scrollbackArea, 'clientHeight', {
        value: 400,
        configurable: true,
      })
      block.el.getBoundingClientRect = () => ({
        height: 20,
        width: 800,
        top: 0,
        left: 0,
        right: 800,
        bottom: 20,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      })
      const output = screenFrame(10, ['', 'prompt ls', 'one output row'])
      output.state.baseRow = 10
      client._sessions[0].fireScreenFrame(output)
      await vi.waitFor(() => expect(scrollback.terminalLiveViewport.style.height).toBe('16px'))
    } finally {
      teardown()
    }
  })

  it('keeps a running block on its own rows when the pane loses rows while it runs (nocx-zg3k3.32)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    const anchor = {
      entryId: 'att-rows',
      row: 12,
      screenRevision: 5,
      bufferKind: 'normal' as const,
      bufferInstance: '1',
      cols: SCREEN_COLS,
      rows: SCREEN_ROWS,
    }
    try {
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-rows', state: 'open', origin: 'shell', command: 'ls' },
      })
      client._fireBlockOutputStart(anchor)
      const block = scrollback.blockManager.blockForAttempt('att-rows')!

      // The pane was refitted one row shorter before the anchor's own frame
      // was accepted: the anchor is an absolute row of the shared screen
      // coordinate, which a change of row count does not renumber.
      const shorter = screenFrame(6, ['99', 'prompt ls', ''], { rows: SCREEN_ROWS - 1 })
      shorter.state.baseRow = 10
      client._sessions[0].fireScreenFrame(shorter)
      expect(block.outputAnchorOffset).toBe(2)

      // Shrinking again pushes the top row into history: the same output
      // start is now one row nearer the top, and the hidden band follows it.
      const shrunk = screenFrame(7, ['prompt ls', 'out'], { rows: SCREEN_ROWS - 2 })
      shrunk.state.baseRow = 11
      client._sessions[0].fireScreenFrame(shrunk)
      expect(block.outputAnchorOffset).toBe(1)
      expect(block.outputAnchorText).toBe('prompt ls')

      // A width change on the way (WebKit flashes a scrollbar during a
      // resize) still moves the band with the base; the echo reading is
      // kept from the last frame of the anchor's own width.
      const wider = screenFrame(8, ['out', ''], { cols: SCREEN_COLS + 1, rows: SCREEN_ROWS - 2 })
      wider.state.baseRow = 12
      client._sessions[0].fireScreenFrame(wider)
      expect(block.outputAnchorOffset).toBe(0)
      expect(block.outputAnchorText).toBe('prompt ls')
    } finally {
      teardown()
    }
  })

  it('hides the rows above a running block’s start before its output anchor arrives (nocx-zg3k3.32)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    try {
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // The previous command's rows are still on the screen; the cursor
      // stands on the line the new command is typed on.
      const before = screenFrame(5, ['98', '99', 'prompt read x', ''])
      before.state.baseRow = 20
      before.cursor = { x: 0, y: 2, visible: true }
      client._sessions[0].fireScreenFrame(before)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-early', state: 'open', origin: 'shell', command: 'read x' },
      })
      const block = scrollback.blockManager.blockForAttempt('att-early')!
      expect(block.startLine).toBe(22)
      // Placed at the open, from the frame it opened on: a command that
      // prints nothing sends no frame after it.
      expect(block.startOffset).toBe(2)
      expect(scrollback.terminalLiveInner.style.transform).toMatch(/^translateY\(-\d/)

      const output = screenFrame(6, ['98', '99', 'prompt read x', 'OWN'])
      output.state.baseRow = 20
      client._sessions[0].fireScreenFrame(output)
      expect(block.outputAnchorOffset).toBeUndefined()
      expect(block.startOffset).toBe(2)
      expect(scrollback.terminalLiveInner.style.transform).toMatch(/^translateY\(-\d/)
    } finally {
      teardown()
    }
  })

  it('shows none of a screen that cannot place the output anchor (nocx-1ebrs.66.1.24)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    const ROWS = 30
    try {
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // The previous command filled the screen and its last frames are still
      // in flight when the next command starts, so the running fact is
      // handled on a screen that is still two screens behind: the block is
      // minted on THAT screen's cursor row while the command's own row is a
      // hundred rows further down.
      const stale = screenFrame(5, ['1', '2', '3', '4'], { rows: ROWS })
      stale.state.baseRow = 0
      stale.cursor = { x: 0, y: ROWS - 1, visible: true }
      client._sessions[0].fireScreenFrame(stale)

      // The backend's anchor names the command's first output row, which is
      // below this screen's bottom edge.
      client._fireBlockOutputStart({
        entryId: 'att-lag',
        row: 102,
        screenRevision: 16,
        bufferKind: 'normal',
        bufferInstance: '1',
        cols: SCREEN_COLS,
        rows: ROWS,
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-lag', state: 'open', origin: 'shell', command: 'sleep 30' },
      })
      const block = scrollback.blockManager.blockForAttempt('att-lag')!
      // The anchor's own revision has not been accepted yet, so the row the
      // block was minted on still governs the shift.
      expect(block.startOffset).toBe(29)
      expect(scrollback.terminalLiveInner.style.transform).toBe('translateY(-464px)')

      // The screen catches up: base row 72, the command's echo on the last
      // row. The minted start is now above the screen and the anchor is one
      // row past its bottom edge — still nothing of the command's on it.
      const caught = screenFrame(
        16,
        [...Array.from({ length: ROWS - 1 }, (_, i) => String(72 + i)), 'sleep 30'],
        { rows: ROWS },
      )
      caught.state.baseRow = 72
      caught.cursor = { x: 0, y: ROWS - 1, visible: true }
      client._sessions[0].fireScreenFrame(caught)
      expect(block.startOffset).toBe(0)
      expect(block.outputAnchorOffset).toBe(ROWS)
      expect(scrollback.terminalLiveInner.style.transform).toBe('translateY(-480px)')

      // One more row written puts the anchor on the screen, and then it
      // places like any other: the echo above it is what the shift hides.
      const placed = screenFrame(
        17,
        [...Array.from({ length: ROWS - 2 }, (_, i) => String(73 + i)), 'sleep 30', ''],
        { rows: ROWS },
      )
      placed.state.baseRow = 73
      placed.cursor = { x: 0, y: ROWS - 1, visible: true }
      client._sessions[0].fireScreenFrame(placed)
      expect(block.outputAnchorOffset).toBe(29)
      expect(block.outputAnchorText).toBe('sleep 30')
      expect(scrollback.terminalLiveInner.style.transform).toBe('translateY(-464px)')
    } finally {
      teardown()
    }
  })

  it.each([
    {
      kind: 'soft-wrapped',
      command: 'printf fish; read answer',
      echoRows: ['printf fish;', ' read answer'],
      softWrap: true,
    },
    {
      kind: 'multiline',
      command: 'printf fish;\nread answer',
      echoRows: ['printf fish;', 'read answer'],
      softWrap: false,
    },
  ])(
    'keeps $kind command echoes out of the running body',
    async ({ command, echoRows, softWrap }) => {
      const client = makeClient()
      const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
      const handler = factHandler(client)
      const scrollback = scrollbackFor(content)
      try {
        Object.defineProperty(scrollback.scrollbackArea, 'clientHeight', {
          value: 300,
          configurable: true,
        })
        handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
        handler({
          lane: 'lane-1',
          lifecycle: 'running',
          domain: 'd1',
          epoch: 1,
          attempt: { id: 'att-wrapped-echo', state: 'open', origin: 'shell', command },
        })
        client._fireBlockOutputStart({
          entryId: 'att-wrapped-echo',
          row: 3,
          screenRevision: 1,
          bufferKind: 'normal',
          bufferInstance: '1',
          cols: 12,
          rows: 8,
        })
        const frame = screenFrame(1, ['old output', ...echoRows, 'current row'], {
          cols: 12,
          rows: 8,
        })
        frame.rows[1].wrap = softWrap
        frame.rows[2].continuation = softWrap
        frame.cursor.y = 3
        sessionOf(content).fireScreenFrame(frame)
        await vi.waitFor(() => {
          expect(scrollback.terminalLiveInner.style.transform).toBe('translateY(-48px)')
          expect(scrollback.terminalLiveViewport.style.height).toBe('16px')
        })

        // A program that clears and repaints the normal buffer keeps its new
        // top row; the old echo's coordinates cannot hide replacement output.
        const cleared = screenFrame(2, ['fresh output'], { cols: 12, rows: 8 })
        cleared.cursor.y = 0
        sessionOf(content).fireScreenFrame(cleared)
        await vi.waitFor(() => {
          expect(scrollback.terminalLiveInner.style.transform).toBe('')
          expect(scrollback.terminalLiveViewport.style.height).toBe('16px')
        })
      } finally {
        teardown()
      }
    },
  )

  it('sizes a short running block from visible output, not the blank cursor tail', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    const viewportRows = 31
    const anchor = {
      entryId: 'att-short-output',
      row: 1,
      screenRevision: 5,
      bufferKind: 'normal' as const,
      bufferInstance: '1',
      cols: SCREEN_COLS,
      rows: viewportRows,
    }

    try {
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-short-output', state: 'open', origin: 'shell', command: 'ls' },
      })
      client._fireBlockOutputStart(anchor)
      Object.defineProperty(scrollback.scrollbackArea, 'clientHeight', {
        value: 400,
        configurable: true,
      })
      // The live cap reserves the same fixed 32px running header, regardless
      // of command text or the number of populated output rows.
      // The accepted screen has four populated rows, but the cursor remains
      // in the final row of a taller viewport. A short block stays seated by
      // its output rather than reserving the blank tail as running content.
      const frame = screenFrame(6, ['ls', 'A', 'B', 'C'], { rows: viewportRows })
      frame.cursor.y = viewportRows - 1
      client._sessions[0].fireScreenFrame(frame)

      await vi.waitFor(() => {
        expect(scrollback.terminalLiveViewport.style.height).toBe('48px')
      })

      // A normal-buffer program with output all the way to the cursor still
      // fills the available running region, up to the same viewport cap.
      const fullScreen = screenFrame(
        7,
        ['ls', 'A', 'B', 'C', ...Array.from({ length: viewportRows - 4 }, () => 'top')],
        { rows: viewportRows },
      )
      fullScreen.cursor.y = viewportRows - 1
      client._sessions[0].fireScreenFrame(fullScreen)
      await vi.waitFor(() => {
        expect(scrollback.terminalLiveViewport.style.height).toBe('368px')
      })
    } finally {
      teardown()
    }
  })

  it('the first PARSED write stands the running command\u2019s stand-in down (nocx-vnirv.1)', async () => {
    // THE WIRING TEST for the block manager's output-arrival seam and the
    // accepted-frame call site. A running command carries the same "working,
    // nothing written yet" stand-in a turn does; its first accepted output
    // frame removes it, while later frames leave it absent.
    //
    // This guards the reachable write half of the seam: the authenticated
    // running fact opens the block, then an accepted backend frame removes
    // its stand-in through the real TerminalContent mount.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const session = sessionOf(content)
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'make' },
      })
      const live = withScrollback.scrollback.terminalLiveContainer
      // The command is running and nothing has been parsed: the stand-in
      // stands where the first output line will be written.
      expect(live.querySelector('.cmd-answer-typing')).not.toBeNull()
      // The first accepted frame stands the indicator down.
      session.fireScreenFrame(screenFrame(1, ['make output']))
      expect(live.querySelector('.cmd-answer-typing')).toBeNull()
      // Idempotent: later output frames do not rebuild the stand-in.
      session.fireScreenFrame(screenFrame(2, ['make output', 'next line']))
      expect(live.querySelector('.cmd-answer-typing')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('restores live output and stdin focus when the same bound attempt resumes after readiness', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const session = sessionOf(content)
    const raf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      content.setVisible(true)
      Object.defineProperty(withScrollback.scrollback.scrollbackArea, 'clientHeight', {
        value: 300,
        configurable: true,
      })
      withScrollback.scrollback.scrollbackArea.scrollTo = vi.fn()
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('read reply')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('paste', 'read reply'))
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-0',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'read reply',
        },
      })
      expect(withScrollback.scrollback.mode).toBe('running')
      const input = intentElementOf(content)
      expect(document.activeElement).toBe(input)
      const before = withScrollback.scrollback.terminalLiveContainer.style.height

      // Handing over bytes is not a client-visible update. Only an accepted
      // backend frame advances the cell model and sizes the live region.
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).toBe(before)
      const rows = Array.from({ length: 12 }, (_, i) => `output ${i + 1}`)
      session.fireScreenFrame(screenFrame(1, rows, { rows: rows.length }))
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).toBe('192px')

      const running = withScrollback.scrollback.blockManager.runningBlock
      expect(running).not.toBeNull()
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(editorOf(content).isVisible).toBe(true)
      expect(ed.root.contains(document.activeElement)).toBe(true)
      expect(withScrollback.scrollback.mode).toBe('idle')

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-0',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'read reply',
        },
      })
      expect(withScrollback.scrollback.mode).toBe('running')
      expect(document.activeElement).toBe(input)
      session.intent.mockClear()
      document.activeElement!.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          code: 'Enter',
          bubbles: true,
          cancelable: true,
        }),
      )
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('key', 'enter'))
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).toBe('192px')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBe(running)
      expect(running!.el.contains(withScrollback.scrollback.terminalLiveContainer)).toBe(true)
      expect(
        withScrollback.scrollback.scrollbackInner.querySelectorAll('.cmd-block-running'),
      ).toHaveLength(1)
    } finally {
      globalThis.requestAnimationFrame = raf
      teardown()
    }
  })

  it('the running grid is fitted to the live region cap, so a tall inline TUI keeps its last row reachable (nocx-zn4d)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const session = sessionOf(content)
    const reports = () => session.sendResize.mock.calls.map((call) => call[0])
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    const raf = globalThis.requestAnimationFrame
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      content.setVisible(true)
      // An accepted frame establishes the cell metrics; the delivered
      // viewport is then measured against those committed metrics.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      session.fireScreenFrame(screenFrame(1, ['prompt']))
      Object.defineProperty(withScrollback.scrollback.scrollbackArea, 'clientHeight', {
        value: 300,
        configurable: true,
      })
      content.viewportChanged({ width: 800, height: 300 })
      await vi.waitFor(() => expect(reports().length).toBeGreaterThan(0))
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'omp' },
      })
      expect(withScrollback.scrollback.mode).toBe('running')
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()

      // Output taller than the pane: the box and grid share the same 268px
      // budget (300 minus one 32px header). Output cannot change that budget.
      const before = reports().length
      session.fireScreenFrame(
        screenFrame(
          2,
          Array.from({ length: 20 }, () => 'repaint'),
          { rows: 20 },
        ),
      )
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).toBe('268px')
      expect(reports()).toHaveLength(before)
      expect(reports()[reports().length - 1]).toMatchObject({ rows: 16, ypixel: 256 })
    } finally {
      globalThis.requestAnimationFrame = raf
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('a running program is not resized by its own output growing (nocx-oikdu)', async () => {
    // omp draws a box, and in nocx the box came back MANGLED — its top border
    // truncated mid-row while its bottom corners sat at the full width. Two
    // different widths in one box is one thing: the grid changed size while
    // the program was drawing into it. Warp, running the same program on the
    // same host, drew it whole.
    //
    // The path is scheduleLiveResize: it runs on every chunk of parsed output
    // and ends in refitIfResized, whose height is runningLiveCap — the
    // scroller less the RUNNING BLOCK'S HEADER. That header is measured live,
    // so anything that changes its height mid-command (it gains a duration,
    // it wraps) re-fits the grid and sends the pty a SIGWINCH in the middle of
    // a repaint.
    //
    // A running program owns its grid. The pane changing size is still a
    // resize — that one the user asked for — but the program's OWN output
    // must never be.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const session = sessionOf(content)
    const reports = () => session.sendResize.mock.calls.map((call) => call[0])
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    const raf = globalThis.requestAnimationFrame
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      content.setVisible(true)
      session.fireScreenFrame(screenFrame(1, ['prompt']))
      content.viewportChanged({ width: 800, height: 400 })
      await vi.waitFor(() => expect(reports().length).toBeGreaterThan(0))
      Object.defineProperty(withScrollback.scrollback.scrollbackArea, 'clientHeight', {
        value: 300,
        configurable: true,
      })
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'omp' },
      })
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      let headerHeight = 24
      block!.el.getBoundingClientRect = () => ({
        height: headerHeight,
        width: 800,
        top: 0,
        left: 0,
        right: 800,
        bottom: headerHeight,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      })

      // First output arrives as a backend frame. Its rows fit inside the
      // running cap, so no size report is needed.
      const before = reports().length
      session.fireScreenFrame(screenFrame(2, ['box top']))
      expect(reports()).toHaveLength(before)

      // The header grows by one row mid-command — the block gains its
      // duration chip while omp is still drawing. Nothing about the PANE
      // changed, so the program must not be resized.
      headerHeight = 48
      session.fireScreenFrame(screenFrame(3, ['box top', 'box bottom']))

      expect(reports()).toHaveLength(before)
    } finally {
      globalThis.requestAnimationFrame = raf
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('a program repainting the same rows does not yank the scroll (nocx-6w4z)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const session = sessionOf(content)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    const raf = globalThis.requestAnimationFrame
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      content.setVisible(true)
      Object.defineProperty(withScrollback.scrollback.scrollbackArea, 'clientHeight', {
        value: 300,
        configurable: true,
      })
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'top' },
      })
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      // The cap takes the HEADER off the scroller — the block holds the live
      // region itself since ADR-0091 (see the test above).
      const headerEl = block!.el.querySelector<HTMLElement>(':scope > .cmd-header')!
      headerEl.getBoundingClientRect = () => ({
        height: 24,
        width: 800,
        top: 0,
        left: 0,
        right: 800,
        bottom: 24,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      })
      // jsdom does no layout, so a measured height never reflects the
      // inline height setLiveHeight wrote. A real browser applies it next
      // frame; the guard reads it. Emulate that so the guard is exercised.
      const live = withScrollback.scrollback.terminalLiveContainer
      live.getBoundingClientRect = () => ({
        height: parseFloat(live.style.height) || 0,
        width: 800,
        top: 0,
        left: 0,
        right: 800,
        bottom: parseFloat(live.style.height) || 0,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      })
      const scrollTo = vi.fn()
      withScrollback.scrollback.scrollbackArea.scrollTo = scrollTo

      // The accepted frame sizes the box to the running cap.
      const topFrame = screenFrame(
        1,
        Array.from({ length: 20 }, () => 'top frame'),
        { rows: 20 },
      )
      session.fireScreenFrame(topFrame)
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).toBe('268px')
      expect(scrollTo).toHaveBeenCalled()
      scrollTo.mockClear()

      // A repaint of the same rows does not change the height, so the early
      // return holds and the scroll is NOT yanked while the user reads.
      session.fireScreenFrame(
        screenFrame(
          2,
          Array.from({ length: 20 }, () => 'top frame'),
          { rows: 20 },
        ),
      )
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).toBe('268px')
      expect(scrollTo).not.toHaveBeenCalled()
    } finally {
      globalThis.requestAnimationFrame = raf
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('a foreign OSC 133 B/A repaint (omp writing its own prompt marks) no longer blanks the live region (nocx-mu8s)', async () => {
    // The mu8s report: omp writes ESC]133;B then ESC]133;A during the
    // repaint where it starts working on a message. The old marker cycle
    // read that as prompt-ready → setIdle() → `.live-idle { height: 0 }` —
    // the running program became invisible while still taking keys. The
    // lifecycle left the byte stream (ADR-0024 §1, nocx-u7uh.1): markers
    // are render-only, so the live region must stay up while the
    // authenticated attempt runs.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const session = sessionOf(content)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'omp' },
      })
      expect(withScrollback.scrollback.mode).toBe('running')

      // Repaint frames cannot alter lifecycle ownership. The backend's
      // screen plane updates the picture, while the authenticated lifecycle
      // remains the sole authority for prompt/running transitions.
      session.fireScreenFrame(screenFrame(1, ['omp repaint']))
      session.fireScreenFrame(screenFrame(2, ['omp prompt marks']))
      expect(withScrollback.scrollback.mode).toBe('running')
      expect(withScrollback.scrollback.terminalLiveContainer.className).not.toContain('live-idle')
      expect(withScrollback.scrollback.terminalLiveContainer.style.height).not.toBe('0px')
      // The editor never takes the keyboard from a stream mark — ownership
      // is the lifecycle axis (prompt_ready) alone (ADR-0024 §6).
      expect(content.presentation).toBe('terminal')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })
})

describe('the live box follows the pane it fills (nocx-liveheight)', () => {
  const scrollbackOf = (content: TerminalContent): ScrollbackController =>
    (content as unknown as { scrollback: ScrollbackController }).scrollback

  // A markerless session — which is every session whose shell integration did
  // not come up, the ones that wear the "Not integrated" card — fills the pane
  // with the terminal. The card sits in the pane's flow above it, so raising
  // and dismissing it changes the scroller's height underneath a mode that
  // used to measure itself once, on the way in. The grid was refitted and the
  // box was not, which is a blank strip at the top and clipped rows at the
  // bottom. A window resize does the same thing with no card involved, and
  // that is the seam this test drives.
  it('re-measures when the pane changes size, not only when the mode is entered', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const raf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      const sb = scrollbackOf(content)
      expect(sb.mode).toBe('unstructured')
      // jsdom does no layout: the scroller's height is named, as everywhere
      // else in this file.
      Object.defineProperty(sb.scrollbackArea, 'clientHeight', {
        value: 400,
        configurable: true,
      })
      content.viewportChanged({ width: 800, height: 400 })
      expect(sb.terminalLiveContainer.style.height).toBe('400px')

      // The card is dismissed (or the window grew): the scroller is taller.
      Object.defineProperty(sb.scrollbackArea, 'clientHeight', {
        value: 560,
        configurable: true,
      })
      content.viewportChanged({ width: 800, height: 560 })
      expect(sb.terminalLiveContainer.style.height).toBe('560px')
    } finally {
      globalThis.requestAnimationFrame = raf
      teardown()
    }
  })
})

describe('alt-screen exit and the ready prompt present the structured layout (nocx-u7uh.26, nocx-u7uh.27)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the wire seam tests deliver authenticated facts through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    const subscribe = client.dispatcher.subscribe
    expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
    return lifecycleHandler(client)
  }

  const scrollbackOf = (content: TerminalContent): ScrollbackController =>
    (content as unknown as { scrollback: ScrollbackController }).scrollback

  it('a ready prompt with a live domain presents the structured idle layout (nocx-u7uh.27)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = factHandler(client)
      // The kernel starts Native: a conventional terminal, flat grid.
      expect(scrollbackOf(content).mode).toBe('unstructured')
      // Integration establishes at the first prompt boundary. A live domain
      // entitles the session to the block model (ADR-0024 §4), so the pane
      // must move to the idle/structured layout — it used to stay on the
      // conventional grid until the first command opened a block, so the
      // first impression of an integrated session was a flat terminal.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(scrollbackOf(content).mode).toBe('idle')
      expect(
        scrollbackOf(content).scrollbackInner.classList.contains('inner-fullscreen-mode'),
      ).toBe(false)
    } finally {
      teardown()
    }
  })

  it('leaving the alternate buffer in an integrated session restores the structured layout (nocx-u7uh.26)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // vim enters the alternate buffer: the program takes the pane.
      fireBufferState(sessionOf(content), 'alternate', 20)
      expect(scrollbackOf(content).mode).toBe('fullscreen')
      // vim quits: leaving the alternate buffer must land on the structured
      // idle layout, not the flat conventional grid — the live domain
      // entitles the session to the block model on the way out too.
      fireBufferState(sessionOf(content), 'normal', 21)
      expect(scrollbackOf(content).mode).toBe('idle')
      expect(
        scrollbackOf(content).scrollbackInner.classList.contains('inner-fullscreen-mode'),
      ).toBe(false)
    } finally {
      teardown()
    }
  })

  it('leaving the alternate buffer while the attempt still runs keeps the live region up (nocx-u7uh.26)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // A shell-originated attempt opens a running block.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'vim file' },
      })
      expect(scrollbackOf(content).mode).toBe('running')
      fireBufferState(sessionOf(content), 'alternate', 20)
      expect(scrollbackOf(content).mode).toBe('fullscreen')
      // vim exits before the completion fact lands: the command cycle still
      // owns the live region, so the pane stays in the running layout until
      // the authenticated completion freezes the block.
      fireBufferState(sessionOf(content), 'normal', 21)
      expect(scrollbackOf(content).mode).toBe('running')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('a conventional session still lands unstructured after the alternate buffer (nocx-u7uh.26)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // Native: no domain, a conventional terminal. The alt-screen path
      // must leave it exactly as it found it — a flat grid.
      fireBufferState(sessionOf(content), 'alternate', 20)
      expect(scrollbackOf(content).mode).toBe('fullscreen')
      fireBufferState(sessionOf(content), 'normal', 21)
      expect(scrollbackOf(content).mode).toBe('unstructured')
    } finally {
      teardown()
    }
  })
})

describe('the editor submit opens the attempt before the pty write (ADR-0024 §5, nocx-u7uh.18)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the wire seam tests deliver authenticated facts through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    const subscribe = client.dispatcher.subscribe
    expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
    return lifecycleHandler(client)
  }

  /** Dispatch a keydown exactly where a user's keystroke lands. */
  const key = (view: EditorView, init: KeyboardEventInit): void => {
    view.contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
    )
  }

  /** jsdom does not implement scrollTo/scrollIntoView; the block model
   *  calls them on submit. Stub them for the duration — the same trade the
   *  projections tests make. Returns the restore. */
  function stubScrolling(): () => void {
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    return () => {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
    }
  }
  it('a finished command the ledger could not carry says so on the block (nocx-2vb9y)', async () => {
    // The person's end of it. The freeze is unconditional and the record is
    // not, so this block shows `exit 1` and reaches neither history nor the
    // bell. Absence cannot carry that — a line typed at the native prompt is
    // unrecorded by design and looks identical — so the block says it.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // The backend echoed a submit token, so an app submit really happened —
      // and this renderer holds no record carrying it.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-lost',
          state: 'open',
          origin: 'app',
          submitId: 'sub-that-never-was',
          command: 'make deploy',
        },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-lost',
          state: 'completed',
          exitCode: 1,
          fence: 'e'.repeat(64),
        },
      })

      const block = withScrollback.scrollback.blockManager.blockForAttempt('att-lost')
      expect(block).not.toBeNull()
      expect(block!.el.dataset.recorded).toBe('no')
      expect(block!.el.querySelector('.cmd-header-unrecorded')?.textContent).toBe('not recorded')
      // And nothing was sent to the store, which is the fact the chip states.
      expect(
        client.dispatcher.call.mock.calls.some((c) => c[0] === 'lifecycle.submitAttempt'),
      ).toBe(false)
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('a refused attempt still runs the command, and the record it opened still carries the outcome (nocx-2vb9y)', async () => {
    // The fail-open is deliberate: a control plane that is busy must never
    // swallow a command. What it used to cost was the RECORD — the submit
    // opened one carrying a token no attempt would ever echo, so the shell's
    // own start looked shell-originated, opened a SECOND block, and the
    // finished command could reach history and the bell through nothing but
    // a fallback that guesses. This asserts the whole interval: the bytes go
    // out, one block carries the command, and its authenticated completion
    // is recorded with the app-owned text.
    const client = makeClient()
    client.dispatcher.call.mockImplementation(() =>
      Promise.reject(new Error('control lane saturated')),
    )
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const recorded = historyRecordedHandler(client)
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.show()
      ed.insertText('make deploy')
      key(view, { key: 'Enter' })

      // Fail-open, unchanged: the refusal did not eat the command.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make deploy'],
          ['key', 'enter'],
        ]),
      )

      // The shell's own start, with no token — nothing minted one for this
      // submit. It must bind to the block already open, not open a second.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-refused', state: 'open', origin: 'shell', command: 'make deploy' },
      })
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-refused',
          state: 'completed',
          exitCode: 3,
          fence: 'd'.repeat(64),
        },
      })

      // Recorded — with the APP-OWNED text, which is the half a shell line
      // may never contribute.
      recorded({
        sessionId: client._sessions[0].sessionId,
        attemptId: 'att-refused',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'e-refused',
        source: 'user',
        redactions: [],
        maskedCommand: 'make deploy',
        captures: [],
      })
      expect(client.dispatcher.subscribe).toHaveBeenCalledWith(
        'history.recorded',
        expect.any(Function),
      )
    } finally {
      restoreScroll()
      teardown()
    }
  })
  it('a submit at a live prompt opens the attempt with the app-owned text BEFORE the pty write', async () => {
    const client = makeClient()
    const submitAttempt = client.dispatcher.call
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    submitAttempt.mockImplementation(() => attemptPromise)
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    // The escape hatch TerminalContent keeps `session` private (the
    // editorOf/rendererOf pattern).
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.show()
      ed.insertText('make deploy')
      key(view, { key: 'Enter' })

      // The attempt-open call went out synchronously at submit, with the
      // app-owned text, cwd and host — and the pty write has NOT happened.
      // The ordering is asserted, not assumed: the backend emits the
      // running fact INSIDE SubmitAttempt, so the bytes must wait for the
      // answer or the shell's start could open a second attempt first.
      // The source is the OTHER half of nocx-iadtt's rule, stated here as
      // the person's end of the interval: this line was typed, so the row
      // this call opens is the person's, whatever the assistant is doing in
      // the same pane at the same moment.
      const token = submitToken(client)
      expect(token).toMatch(/^sub-[0-9a-f]{32}$/)
      expect(submitAttempt).toHaveBeenCalledWith('lifecycle.submitAttempt', {
        domain: 'd1',
        command: 'make deploy',
        cwd: FIXTURE_CWD,
        host: '',
        source: 'user',
        // The correlation token this submit minted, carried so the published
        // attempt can name the record this submit opened rather than leaving
        // the projection to pick one (nocx-td6d4.10). Its value is opaque —
        // only its presence and its equality with the record's are meaning.
        submitId: token,
      })
      expect(session.send).not.toHaveBeenCalled()
      // The submit opens NO card — the published running fact opens it
      // through the projections (nocx-2v80t.3.2), still before the bytes.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(0)

      // The backend answers: only now do the bytes go out.
      resolveAttempt({
        id: 'att-9',
        domain: 'd1',
        state: 'open',
        command: 'make deploy',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make deploy'],
          ['key', 'enter'],
        ]),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-9',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'make deploy',
        },
      })
      // The authenticated start opened the card.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(client.call).toHaveBeenCalledWith('ledger.bind', {
        envelope: {
          id: 'att-9',
          sessionId: session.sessionId,
          cwd: FIXTURE_CWD,
          kind: 'shell',
          intent: 'make deploy',
          sensitivity: 'normal',
          clientSeq: 0,
          attemptId: 'att-9',
        },
        facts: {},
      })
    } finally {
      restoreScroll()
      teardown()
    }
  })
  it('a control focused while the attempt is answered keeps the keyboard (nocx-xn63t.6.37)', async () => {
    // The row exists BEFORE the pty write, by design: the backend publishes
    // the running fact inside SubmitAttempt (see the test above), so the
    // Stop button is on screen while the answer is still in flight. That is
    // the window a person tabs into, and the deferred handoff — the second
    // `focusGrid`, the one that runs when the answer settles — took the
    // keyboard back from whatever it found. On webkit that is what CI saw:
    // `stop.focus()` then `expect(stop).toBeFocused()` reading 'inactive'
    // for its whole 5 s (shard 1, run 39052926672).
    const client = makeClient()
    const submitAttempt = client.dispatcher.call
    // Promise.withResolvers needs ES2024 and this project targets ES2021, so
    // the resolver is captured via the executor form (the codebase pattern).
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    submitAttempt.mockImplementation(() => attemptPromise)
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const session = sessionOf(content)
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      key(view, { key: 'Enter' })

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-37',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      // The running row is on screen — the person's Stop, reached the way
      // the keyboard reaches it (Tab lands on it: it carries
      // `data-block-control`, the pane's own exemption from its focus
      // bounce).
      const stop = document.querySelector<HTMLButtonElement>(
        '.cmd-block [data-block-control][aria-label="Stop"]',
      )
      expect(stop).not.toBeNull()
      stop?.focus()
      expect(document.activeElement).toBe(stop)

      // The answer settles and the bytes go out. The keyboard stays where
      // the person put it: the deferred handoff is the repair for keys that
      // reached <body> during the round trip, and it is not a claim on a
      // control that is holding them.
      resolveAttempt({
        id: 'att-37',
        domain: 'd1',
        state: 'open',
        command: 'sleep 30',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      // The write really ran: the command went out. The grid's own focus
      // intents travel on this same seam, so the wait names the paste rather
      // than the tail of the list.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls).toContainEqual(['paste', 'sleep 30']),
      )
      expect(document.activeElement).toBe(stop)
    } finally {
      restoreScroll()
      teardown()
    }
  })
  it('keeps a focused running control across readiness and resumption', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      key(view, { key: 'Enter' })
      await vi.waitFor(() =>
        expect(sessionOf(content).intent).toHaveBeenCalledWith('paste', 'sleep 30'),
      )
      const attempt = {
        id: 'att-0',
        state: 'open' as const,
        origin: 'app' as const,
        submitId: submitToken(client),
        command: 'sleep 30',
      }
      handler({ lane: 'lane-1', lifecycle: 'running', domain: 'd1', epoch: 1, attempt })
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      const stop = block!.el.querySelector<HTMLButtonElement>('button[aria-label="Stop"]')
      expect(stop).not.toBeNull()
      stop!.focus()
      expect(document.activeElement).toBe(stop)

      // CI's native ready arrives after Stop takes the keyboard, followed by
      // this same attempt's start. Availability must not revoke that choice.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      expect(document.activeElement).toBe(stop)

      handler({ lane: 'lane-1', lifecycle: 'running', domain: 'd1', epoch: 1, attempt })
      expect(ed.isVisible).toBe(false)
      expect(document.activeElement).toBe(stop)
      expect(stop!.isConnected).toBe(true)
      expect(withScrollback.scrollback.blockManager.runningBlock).toBe(block)

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: attempt.id,
          state: 'completed',
          exitCode: 0,
          fence: 'a'.repeat(64),
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      expect(ed.root.contains(document.activeElement)).toBe(true)
    } finally {
      restoreScroll()
      teardown()
    }
  })
  it('still hands the grid the keyboard when the round trip left it on nobody (nocx-u7uh.23)', async () => {
    // The other half of the guard, and the reason it is a guard rather than a
    // deletion. The deferred handoff exists because the editor gives the
    // keyboard up synchronously at commit and a display:none host drops the
    // browser's focus to <body> — for the whole round trip keys typed into a
    // program already reading stdin then reached nobody, and an ssh password
    // prompt was answered with an empty line. <body> is still repaired; a
    // control the person reached is not.
    const client = makeClient()
    const submitAttempt = client.dispatcher.call
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    submitAttempt.mockImplementation(() => attemptPromise)
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    const grid = intentElementOf(content)
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('read x')
      key(view, { key: 'Enter' })

      // jsdom keeps the DOM's focus where a real browser would have dropped
      // it when the editor's host went display:none, so the drop is made
      // explicit — the same trade the pane's own tests make for it.
      ;(document.activeElement as HTMLElement | null)?.blur()
      expect(document.activeElement).toBe(document.body)

      resolveAttempt({
        id: 'att-38',
        domain: 'd1',
        state: 'open',
        command: 'read x',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      await vi.waitFor(() => expect(document.activeElement).toBe(grid))
    } finally {
      restoreScroll()
      teardown()
    }
  })
  it('an agent submission cancelled during the attempt round trip sends no bytes and releases its waiters', async () => {
    const client = makeClient()
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    client.dispatcher.call.mockImplementation(() => attemptPromise)
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const session = sessionOf(content)
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    const controller = new AbortController()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })

      const pending = content.submitAgentCommand('touch pid', 'req-cancel', controller.signal)
      expect(session.send).not.toHaveBeenCalled()

      // The broker cancellation lands while lifecycle.submitAttempt is still
      // in flight — this is the only window that proves the renderer checks
      // immediately before its write rather than only at submit entry.
      controller.abort()
      resolveAttempt({
        id: 'att-cancelled',
        domain: 'd1',
        state: 'open',
        command: 'touch pid',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })

      await expect(pending).rejects.toThrow('submission expired before execution')
      expect(session.send).not.toHaveBeenCalled()
      expect(session.signal).not.toHaveBeenCalled()
      const waiters = content as unknown as {
        agentRuns: Map<unknown, unknown>
        runEntryIds: Map<unknown, unknown>
      }
      expect(waiters.agentRuns.size).toBe(0)
      expect(waiters.runEntryIds.size).toBe(0)
    } finally {
      restoreScroll()
      teardown()
    }
  })
  it('the attempt receives the reference-intact record line, never the resolved send line', async () => {
    const client = makeClient()
    // vault.resolveLine goes over the WSClient seam (client.call); the
    // attempt-open goes over the dispatcher (client.dispatcher.call).
    client.call.mockImplementation((method: string) => {
      if (method === 'vault.resolveLine') {
        return Promise.resolve({
          line: 'make --token=SECRETVALUE',
          refs: [{ name: 'TOKEN', resolved: true }],
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const submitAttempt = client.dispatcher.call
    // Promise.withResolvers needs ES2024 and this project targets ES2021, so
    // the resolver is captured via the executor form (the codebase pattern).
    let resolveAttempt!: (v: unknown) => void
    const attemptPromise = new Promise<unknown>((done) => {
      resolveAttempt = done
    })
    submitAttempt.mockImplementation(() => attemptPromise)
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    // The escape hatch TerminalContent keeps `session` private (the
    // editorOf/rendererOf pattern).
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.show()
      ed.insertText('make --token={{secret:TOKEN}}')
      key(view, { key: 'Enter' })

      // The line with references resolves first (ADR-0021); the attempt
      // then opens with the RECORD line — reference intact (decision 5's
      // privacy rule) — while the RESOLVED line goes to the pty and
      // nowhere else.
      await vi.waitFor(() =>
        expect(submitAttempt).toHaveBeenCalledWith('lifecycle.submitAttempt', {
          domain: 'd1',
          command: 'make --token={{secret:TOKEN}}',
          cwd: FIXTURE_CWD,
          host: '',
          source: 'user',
          submitId: submitToken(client),
        }),
      )
      expect(submitToken(client)).toMatch(/^sub-[0-9a-f]{32}$/)
      expect(session.send).not.toHaveBeenCalled()
      resolveAttempt({
        id: 'att-10',
        domain: 'd1',
        state: 'open',
        command: 'make --token={{secret:TOKEN}}',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make --token=SECRETVALUE'],
          ['key', 'enter'],
        ]),
      )
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('a submit with no live domain opens no attempt — the terminal stays conventional', async () => {
    const client = makeClient()
    const submitAttempt = client.dispatcher.call
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    // The escape hatch TerminalContent keeps `session` private (the
    // editorOf/rendererOf pattern).
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      // No fact was delivered: the kernel is Native, the session is a
      // conventional terminal.
      ed.show()
      ed.insertText('make deploy')
      key(view, { key: 'Enter' })

      // No attempt-open call was made — nothing is fabricated for a
      // conventional terminal. Both input intents still use the session queue.
      expect(submitAttempt).not.toHaveBeenCalled()
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make deploy'],
          ['key', 'enter'],
        ]),
      )
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('an empty line is a bare newline: no attempt, no ledger record, but the shell still gets its newline', async () => {
    const client = makeClient()
    const submitAttempt = client.dispatcher.call
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    // The escape hatch TerminalContent keeps `session` private (the
    // editorOf/rendererOf pattern).
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    const withLedger = content as unknown as { ledger: CommandLedger }
    const handler = factHandler(client)
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.show()
      key(view, { key: 'Enter' }) // empty draft

      // A bare newline is not an execution: no attempt-open call, no ledger
      // record (CommandLedger.open refuses empty commands), no crash — and
      // the shell still receives its newline.
      expect(submitAttempt).not.toHaveBeenCalled()
      expect(withLedger.ledger.records()).toHaveLength(0)
      await vi.waitFor(() => expect(session.intent.mock.calls).toEqual([['key', 'enter']]))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('a refused attempt never swallows the command: the bytes still go out', async () => {
    const client = makeClient()
    client.dispatcher.call.mockRejectedValue(new RpcError('lifecycle: no prompt is ready', -32602))
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    // The escape hatch TerminalContent keeps `session` private (the
    // editorOf/rendererOf pattern).
    const withSession = content as unknown as { session: SessionFake }
    const session = withSession.session
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.show()
      ed.insertText('make deploy')
      key(view, { key: 'Enter' })

      // Fail-open: the domain lost its prompt between the last fact and the
      // Enter, the attempt was refused — and the command still runs, whole.
      await vi.waitFor(() =>
        expect(session.intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'make deploy'],
          ['key', 'enter'],
        ]),
      )
    } finally {
      restoreScroll()
      teardown()
    }
  })
})

describe('a degraded session says so in the product (nocx-dvql, nocx-5uu5)', () => {
  const TIMED_OUT = {
    status: 'conventional',
    reason: 'handshake-timeout',
    shell: '/bin/bash',
  } as const

  /** The status the backend publishes, delivered through the real
   *  subscription seam rather than reached for on the content object. */
  const publish = (client: ClientFake, over: Record<string, unknown> = {}): void =>
    integrationHandler(client)({
      sessionId: client._sessions[0].sessionId,
      ...TIMED_OUT,
      ...over,
    })

  const cardIn = (tab: { pane: HTMLElement }) => tab.pane.querySelector('.nocx-integration-notice')

  // A button's label is its visible text, or — for an icon-only control like
  // the dismiss cross — its accessible name (aria-label). A glyph is not text
  // (nocx-9bpeq.5): the cross renders a CloseIcon now, so textContent alone
  // would read as empty rather than as the button it is.
  const buttonLabel = (b: HTMLButtonElement): string =>
    (b.textContent ?? '').trim() || (b.getAttribute('aria-label') ?? '')

  /** Press one of the card's own actions, by the label the user reads. */
  const press = (tab: { pane: HTMLElement }, label: string): void => {
    const found = [...cardIn(tab)!.querySelectorAll('button')].find((b) => buttonLabel(b) === label)
    if (!found) throw new Error(`no card action labelled ${label}`)
    found.click()
  }

  beforeEach(() => {
    window.localStorage.clear()
  })

  // The test the bead names, from the user's side: a session whose shell
  // never completed the handshake is marked, and says why. Before this the
  // product was silent for the whole life of the session.
  it('marks the tab, with the reason in the mark', async () => {
    const warnings: Array<[boolean, string | undefined]> = []
    const client = makeClient()
    const { teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onWarningChange: (w: boolean, l?: string) => warnings.push([w, l]) } },
      client,
    )
    try {
      publish(client)
      expect(warnings[warnings.length - 1]).toEqual([true, 'Not integrated'])
    } finally {
      teardown()
    }
  })

  // `starting` is the honest interval, not a failure: marking it would put a
  // warning on every tab for its first seconds and teach the user that the
  // mark means nothing.
  it('does not mark a session that is still starting', async () => {
    const warnings: Array<[boolean, string | undefined]> = []
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onWarningChange: (w: boolean, l?: string) => warnings.push([w, l]) } },
      client,
    )
    try {
      publish(client, { status: 'starting', reason: undefined })
      expect(warnings.some(([w]) => w)).toBe(false)
      expect(cardIn(tab)).toBeNull()
    } finally {
      teardown()
    }
  })

  // nocx-wfxz, the owner reversing the once-per-(shell, reason) rule taken in
  // nocx-5uu5: the card used to be recorded as read the moment it was DRAWN,
  // so a user who closed it before working out what it meant never saw it
  // again. Nothing on the card said that looking at it spent it. The only
  // thing that writes anything now is the user saying so.
  it('raises the card again in the next session, having been told nothing', async () => {
    const clientA = makeClient()
    const first = await mountTerminal(makeClipboard(), { attachToDocument: true }, clientA)
    try {
      publish(clientA)
      expect(cardIn(first.tab)).not.toBeNull()
      expect(first.tab.pane.querySelector('.ui-status-card__title')!.textContent).toBe(
        'Not integrated',
      )
      press(first.tab, 'Dismiss')
    } finally {
      first.teardown()
    }

    // A second session, same shell, same reason. The user closed the first
    // card without answering it, so this one is still worth raising.
    const clientB = makeClient()
    const second = await mountTerminal(makeClipboard(), {}, clientB)
    try {
      publish(clientB)
      expect(cardIn(second.tab)).not.toBeNull()
    } finally {
      second.teardown()
    }
  })

  // The other end of the same interval: a card the user never touched at all
  // is not spent either. This is the tab that gets closed with the card still
  // on it.
  it('raises the card again after a session that ended with it still showing', async () => {
    const clientA = makeClient()
    const first = await mountTerminal(makeClipboard(), {}, clientA)
    try {
      publish(clientA)
      expect(cardIn(first.tab)).not.toBeNull()
    } finally {
      first.teardown()
    }

    const clientB = makeClient()
    const second = await mountTerminal(makeClipboard(), {}, clientB)
    try {
      publish(clientB)
      expect(cardIn(second.tab)).not.toBeNull()
    } finally {
      second.teardown()
    }
  })

  // Within ONE session it is a different question, and the answer is the
  // opposite: the user closed this card, in this tab, a moment ago. A status
  // republished on the same session — a reconnect re-announcing what it
  // already said — must not push it back up.
  it('does not push a closed card back up when the same status is republished', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      publish(client)
      press(tab, 'Dismiss')
      expect(cardIn(tab)).toBeNull()
      publish(client)
      expect(cardIn(tab)).toBeNull()
    } finally {
      teardown()
    }
  })

  // …and a session that degrades a NEW way after the user closed the first
  // card is telling them something they have not been told.
  it('raises a card for a new reason in the same session', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      publish(client)
      press(tab, 'Dismiss')
      publish(client, { status: 'lost', reason: 'channel-lost' })
      expect(cardIn(tab)).not.toBeNull()
    } finally {
      teardown()
    }
  })

  // nocx-rzvq, measured by the owner on the installed build: the card floated
  // over the terminal and covered the first prompt line. It now takes space
  // from the top of the pane — the pane is a flex column and the card is its
  // first child, so the scrollback is laid out in what is left rather than
  // underneath it.
  it('takes space above the terminal instead of covering it', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      publish(client)
      const card = cardIn(tab)
      const scrollback = tab.pane.querySelector('.scrollback-layout')
      expect(card).not.toBeNull()
      expect(scrollback).not.toBeNull()
      expect(tab.pane.firstElementChild).toBe(card)
      expect(card!.compareDocumentPosition(scrollback!) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
        Node.DOCUMENT_POSITION_FOLLOWING,
      )
    } finally {
      teardown()
    }
  })

  it('raises the card again when the same shell fails a different way', async () => {
    const clientA = makeClient()
    const first = await mountTerminal(makeClipboard(), {}, clientA)
    try {
      publish(clientA)
    } finally {
      first.teardown()
    }

    const clientB = makeClient()
    const second = await mountTerminal(makeClipboard(), {}, clientB)
    try {
      publish(clientB, { status: 'lost', reason: 'channel-lost' })
      expect(cardIn(second.tab)).not.toBeNull()
    } finally {
      second.teardown()
    }
  })

  // A session that never asked for integration receives no status at all,
  // so there is nothing to draw and nothing to nag about.
  it('shows nothing at all for a session that never reports a status', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(cardIn(tab)).toBeNull()
    } finally {
      teardown()
    }
  })

  it('drops the card when the session recovers', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      publish(client)
      expect(cardIn(tab)).not.toBeNull()
      publish(client, { status: 'integrated', reason: undefined })
      expect(cardIn(tab)).toBeNull()
    } finally {
      teardown()
    }
  })

  // ── the card's two silences, and the one thing neither silences ─────────
  //
  // The owner's composition (nocx-aimo): the cross and "Don't show again for
  // this shell" are on the card together because they are different
  // promises. Written as tests so the next reader who wants to collapse them
  // can see the difference rather than infer it.

  it('takes the card away when the cross is pressed, and leaves the mark on the tab', async () => {
    const warnings: Array<[boolean, string | undefined]> = []
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        attachToDocument: true,
        hooks: { onWarningChange: (w: boolean, l?: string) => warnings.push([w, l]) },
      },
      client,
    )
    try {
      publish(client)
      press(tab, 'Dismiss')
      expect(cardIn(tab)).toBeNull()
      // The mark is the state of the session, not a notification: dismissing
      // the card says nothing about whether the session is integrated.
      expect(warnings[warnings.length - 1]).toEqual([true, 'Not integrated'])
    } finally {
      teardown()
    }
  })

  it('leaves the mark alone when the user silences the shell as well', async () => {
    const warnings: Array<[boolean, string | undefined]> = []
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        attachToDocument: true,
        hooks: { onWarningChange: (w: boolean, l?: string) => warnings.push([w, l]) },
      },
      client,
    )
    try {
      publish(client)
      press(tab, "Don't show again for this shell")
      expect(cardIn(tab)).toBeNull()
      expect(warnings[warnings.length - 1]).toEqual([true, 'Not integrated'])
    } finally {
      teardown()
    }
  })

  // The difference between the two, from the user's side: the cross answers
  // the card in front of them, and this shell failing a DIFFERENT way is
  // still something they have not been told. "Don't show again for this
  // shell" answers for the shell, so nothing about it asks again.
  it('still reports a new way for this shell to fail after the cross', async () => {
    const clientA = makeClient()
    const first = await mountTerminal(makeClipboard(), { attachToDocument: true }, clientA)
    try {
      publish(clientA)
      press(first.tab, 'Dismiss')
    } finally {
      first.teardown()
    }

    const clientB = makeClient()
    const second = await mountTerminal(makeClipboard(), {}, clientB)
    try {
      publish(clientB, { status: 'lost', reason: 'channel-lost' })
      expect(cardIn(second.tab)).not.toBeNull()
    } finally {
      second.teardown()
    }
  })

  it('says nothing more about a shell the user has silenced, however it fails', async () => {
    const clientA = makeClient()
    const first = await mountTerminal(makeClipboard(), { attachToDocument: true }, clientA)
    try {
      publish(clientA)
      press(first.tab, "Don't show again for this shell")
    } finally {
      first.teardown()
    }

    const clientB = makeClient()
    const second = await mountTerminal(makeClipboard(), {}, clientB)
    try {
      publish(clientB, { status: 'lost', reason: 'channel-lost' })
      expect(cardIn(second.tab)).toBeNull()
    } finally {
      second.teardown()
    }
  })
})

describe('shell submit input intent ordering (nocx-zg3k3.3.1)', () => {
  it('delivers the command paste before Enter through one ordered intent path', async () => {
    const client = makeClient()
    const session = makeSession()
    client.openSession.mockResolvedValue(session)
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    let releasePaste!: () => void
    const pasteGate = new Promise<void>((resolve) => {
      releasePaste = resolve
    })
    const result: SessionIntentResult = { state: 'executed', bytesWritten: 1, fenceAfter: 0 }
    const delivered: Array<[string, string]> = []
    session.intent.mockImplementation((kind: string, payload: string) => {
      delivered.push([kind, payload])
      return (kind === 'paste' ? pasteGate : Promise.resolve()).then(() => result)
    })
    session.send.mockClear()
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('echo hi')
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

      // Hold the first wire result open. Enter must not even be handed to the
      // session until that paste has settled; call order alone is not enough.
      await vi.waitFor(() => expect(delivered).toEqual([['paste', 'echo hi']]))
      releasePaste()
      await vi.waitFor(() =>
        expect(delivered).toEqual([
          ['paste', 'echo hi'],
          ['key', 'enter'],
        ]),
      )
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      releasePaste()
      teardown()
    }
  })
})

// ───────────────────────────────────────────────────────────────────────────
// The ask entry gesture (nocx-4wtlh): nothing but the person changes where
// Enter goes. Plain Enter submits to the registry's active target; ⌘Enter is
// a one-shot question. Selecting output marks its whole containing block for
// the next question, changes nothing else, and leaves the selection usable
// for copy. A question carries exactly the marked block ids and no others.
// These tests drive the real TerminalContent, controller, BlockManager,
// editor submit, and registry chain.
describe('the ask entry gesture (nocx-4wtlh)', () => {
  /** A client whose dispatcher answers the agent.* transaction, records
   *  EVERY dispatcher call (so the test can assert a lifecycle attempt was
   *  never opened for a question), and keeps the default resolve for
   *  lifecycle.submitAttempt so a shell submit proceeds normally. */
  function agentDispatcher(
    status: {
      endpointConfigured?: boolean
      credential?: ('resolvable' | 'none' | 'deleted' | 'sealed' | 'unavailable') | null
      answering?: AgentStatusResult['answering']
    } = {},
  ) {
    const client = makeClient()
    const dispatcherCalls: Array<{ method: string; params: unknown }> = []
    // Each ask is a new run with a new answer entry — two asks in flight
    // stream concurrently and must never share an identity. Frames get
    // distinct ids too: two chips must carry two references.
    let nextRun = 6
    let nextFrame = 0
    client.dispatcher.call.mockImplementation((method: string, params: unknown) => {
      dispatcherCalls.push({ method, params })
      if (method === 'agent.captureFrame') {
        nextFrame += 1
        return Promise.resolve({ frameId: `frame-${nextFrame}` })
      }
      if (method === 'agent.ask') {
        nextRun += 1
        return Promise.resolve({ runId: nextRun, entryId: `entry-${nextRun}` })
      }
      if (method === 'agent.status') {
        // `answering` is not optional on the wire (nocx-rikz5, Task 3):
        // readiness is a fact about the ROLE, so every status carries
        // either a resolution or the reason there is none. This fixture
        // predated that field and omitted it, which is a payload the
        // backend cannot send.
        return Promise.resolve({
          endpointConfigured: status.endpointConfigured ?? true,
          credential: status.credential ?? 'resolvable',
          lastProbe: null,
          answering: status.answering ?? {
            ready: true,
            reason: null,
            endpoint: 'openrouter',
            model: 'm-a',
          },
        })
      }
      // lifecycle.submitAttempt and anything else the shell path opens.
      return Promise.resolve({
        id: 'att-0',
        domain: 'd1',
        state: 'open',
        command: '',
        cwd: '',
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
    })
    return { client, dispatcherCalls }
  }

  /** A frozen command block through the REAL manager chain. */
  let fixtureEntrySeq = 0
  function frozenBlockOf(
    content: TerminalContent,
    command = 'ls',
    output = ['total 12', 'docs'],
  ): HTMLElement {
    // A card that CARRIES rows, built the way a restored record is built:
    // the body is the record's stored text, rendered as term-line rows. The
    // freeze no longer cuts a body from the grid — nothing clears it
    // (nocx-2v80t.3.3), and the backend's own body is nocx-2v80t.3.4 — so
    // the gesture under test uses the shape that still has rows.
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    const manager = scrollback.blockManager as unknown as {
      _onBlockSelected(id: number): void
      _onBlockDeselected(id: number): void
      _runningActions?: RunningBlockActions
    }
    const outputHtml = output.map((t) => `<span class="term-line">${t}</span>`).join('')
    const el = createCommandBlock(
      'command',
      10_000 + fixtureEntrySeq + 1,
      command,
      '~',
      '',
      outputHtml,
      12,
      0,
      'success',
      () => scrollback.scrollbackInner,
      (bid, sel) => {
        if (sel) manager._onBlockSelected(bid)
        else manager._onBlockDeselected(bid)
      },
      new CommandSnapshotStore(),
      'shell',
      undefined,
      manager._runningActions,
    )
    // A grant names the block's ledger entry: the fixture mints a UNIQUE
    // one, the way the history ack does for a real block.
    el.dataset.entryId = `entry-fixture-${++fixtureEntrySeq}`
    scrollback.restorePast([el])
    return el
  }

  /** The registry's active label — the truth the indicator must render. */
  function activeLabel(content: TerminalContent): string {
    const registry = (
      content as unknown as {
        inputTargets: { active(): { label: string } }
      }
    ).inputTargets
    return registry.active().label
  }

  /** The indicator as mounted in ComposerFrame's field — a SIBLING of CM6's
   *  own root (`.ui-composer-frame__field`'s leading grid column, composer-
   *  frame.css), not a CM6 gutter inside it any more (task C, round 2): the
   *  token is beside the document and never in it, searched from `ed.root`
   *  because it is no longer a descendant of `viewOf(ed).dom` at all. */
  function indicatorOf(ed: CommandEditor): HTMLElement | null {
    return ed.root.querySelector<HTMLElement>('.ui-mode-indicator')
  }

  /** Dispatch a submit key exactly where a person's keystroke lands. */
  function submitKey(ed: CommandEditor, init: KeyboardEventInit = {}): void {
    viewOf(ed).contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...init }),
    )
  }

  /** Type a question at Ask through the REAL gestures, in the order a
   *  person reaches them since per-target drafts landed (nocx-4ff.7): the
   *  ⌘Enter flip FIRST — it swaps the editor to the Ask draft, so text
   *  typed before the flip belongs to the mode it was typed in — then the
   *  typing, then plain Enter, the one send key. */
  function typeAndAsk(ed: CommandEditor, content: TerminalContent, text: string): void {
    if (activeLabel(content) !== 'Agent') submitKey(ed, { metaKey: true })
    ed.insertText(text)
    submitKey(ed)
  }

  /** Select rows [start, end) of a frozen block's output and fire the
   *  selectionchange event the product listens for. jsdom does no layout, so
   *  the helper supplies one range for each covered row as a browser would. */
  function selectRows(block: HTMLElement, start: number, end: number): void {
    const lines = Array.from(block.querySelectorAll<HTMLElement>('.term-line'))
    expect(lines.length).toBeGreaterThanOrEqual(end)
    const first = lines[start]
    const last = lines[end - 1]
    const range = document.createRange()
    range.setStart(first.firstChild ?? first, 0)
    const textNode = Array.from(last.childNodes).find((node) => node.nodeType === Node.TEXT_NODE)
    range.setEnd(
      textNode ?? last,
      textNode ? (textNode.textContent?.length ?? 0) : last.childNodes.length,
    )
    range.getClientRects = () =>
      Array.from(
        { length: end - start },
        (_, i) => new DOMRect(100, 200 + (start + i) * 20, 200, 20),
      ) as unknown as DOMRectList
    const sel = window.getSelection()
    sel?.removeAllRanges()
    sel?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  }

  /** The recorded params of one dispatcher call, narrowed to an object. */
  function recordedParams(
    calls: Array<{ method: string; params: unknown }>,
    method: string,
  ): Record<string, unknown> {
    const call = calls.find((c) => c.method === method)
    if (!call || typeof call.params !== 'object' || call.params === null) {
      throw new Error(`ask-seam: expected a recorded ${method} call`)
    }
    return call.params as Record<string, unknown>
  }

  /** Deliver one server notification through the REAL subscription seam. */
  function deliverNotification(client: ClientFake, method: string, params: unknown): void {
    const sub = client.dispatcher.subscribe.mock.calls.find((c: unknown[]) => c[0] === method) as
      [string, (p: unknown) => void] | undefined
    if (!sub) throw new Error(`nothing subscribed to ${method}`)
    sub[1](params)
  }

  it('plain Enter goes to the shell; ⌘Enter flips to Ask and the next Enter goes to the assistant — one walk, and the indicator matches the registry after each (nocx-4wtlh)', async () => {
    const { client, dispatcherCalls } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // The indicator is the registry's own WORD, rendered at the line
      // start: Run for the shell target by default, and it says so before
      // anything is submitted. The registry's label ('Shell') is untouched
      // — the indicator maps the target to what the person does.
      expect(activeLabel(content)).toBe('Shell')
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      expect(indicatorOf(ed)?.dataset.target).toBe('shell')

      const inputIntentCount = () =>
        sessionOf(content).intent.mock.calls.filter(([kind]) => kind !== 'focus').length

      // ── Plain Enter: the shell receives the line ─────────────────────
      const sentBefore = inputIntentCount()
      ed.insertText('echo hi')
      submitKey(ed)
      // The shell got the paste and Enter through its ordered intent queue;
      // no question crossed the control plane.
      await vi.waitFor(() =>
        expect(sessionOf(content).intent.mock.calls.slice(-2)).toEqual([
          ['paste', 'echo hi'],
          ['key', 'enter'],
        ]),
      )
      expect(inputIntentCount()).toBe(sentBefore + 2)
      expect(dispatcherCalls.find((c) => c.method === 'agent.ask')).toBeUndefined()
      // The handoff hid the editor; the registry never moved.
      expect(ed.isVisible).toBe(false)
      expect(activeLabel(content)).toBe('Shell')

      // ── ⌘Enter then Enter: the assistant receives the line ────────────
      // The editor is re-shown (the next prompt, as the lifecycle would);
      // the indicator still renders what the registry reports.
      ed.show()
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      const sentAfterShell = inputIntentCount()
      typeAndAsk(ed, content, 'what does docs mean?')
      await vi.waitFor(() => {
        expect(dispatcherCalls.some((c) => c.method === 'agent.ask')).toBe(true)
      })
      const ask = recordedParams(dispatcherCalls, 'agent.ask')
      expect(ask.question).toBe('what does docs mean?')
      // A general question: no chips, no references.
      // A general question sends an explicit empty grant list.
      expect(ask.attachedContent).toEqual([])
      // ledger record — the shell's history is unchanged by a question.
      // THE SUBMIT opened exactly one record and NO card (nocx-2v80t.3.2):
      // the card is the projection of the runtime's authenticated start,
      // and this harness delivers no lifecycle facts. The ask neither
      // opened a record nor a card of its own, and the shell's record is
      // untouched.
      expect(inputIntentCount()).toBe(sentAfterShell)
      expect(dispatcherCalls.find((c) => c.method === 'lifecycle.submitAttempt')).toBeUndefined()
      expect(dispatcherCalls.find((c) => c.method === 'history.recorded')).toBeUndefined()
      const ledger = (content as unknown as { ledger: CommandLedger }).ledger
      expect(ledger?.records().map((r) => r.command)).toEqual(['echo hi'])
      expect(ledger?.records()[0].author).toBe('shell')
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      expect(scrollback.blockManager.blocks).toHaveLength(0)
      // A question is not a handoff: the editor stays on screen for the
      // next one. And Enter still goes to Ask — the person moved it, and
      // nothing but the person moves it back; the indicator says so.
      expect(ed.isVisible).toBe(true)
      expect(activeLabel(content)).toBe('Agent')
      expect(indicatorOf(ed)?.textContent).toBe('Ask')
    } finally {
      teardown()
    }
  })

  it('with only the shell target registered, every submit is attributed to the human and nothing regresses (nocx-iadtt)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // Criterion 5's interval endpoint: ONLY the shell target is
      // registered — the state the product was in before any second target
      // existed. The registry is replaced wholesale (the escape hatch the
      // other tests use for private fields), so the old path is driven
      // through the real orchestration, not a fake of it.
      const registry = createRegistry()
      const shellTarget = (content as unknown as { shellTarget: ShellInputTarget }).shellTarget
      registry.register(shellTarget)
      ;(content as unknown as { inputTargets: InputTargetRegistry }).inputTargets = registry

      ed.insertText('echo hi')
      submitKey(ed)

      // The record that opened at submit is the human's — every record is,
      // with no second target to confuse the mint.
      const ledger = (content as unknown as { ledger: CommandLedger }).ledger
      expect(ledger?.records().length).toBe(1)
      expect(ledger?.records()[0].author).toBe('shell')
      // The shell submit still reached the session through ordered intents.
      await vi.waitFor(() =>
        expect(sessionOf(content).intent.mock.calls).toEqual([
          ['paste', 'echo hi'],
          ['key', 'enter'],
        ]),
      )
    } finally {
      teardown()
    }
  })

  it("a command submitted by the shell target while another registered target's submission is in flight is attributed to the shell target (nocx-iadtt)", async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    let releaseAsk: () => void = () => {}
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // A second registered test target (no agent command target exists
      // yet — this is the stand-in) whose submission hangs: the ask is in
      // flight for the whole test. The interleaving is the point: the
      // human's own command arrives while the other target's submission
      // has not settled, and must be attributed to the shell target — the
      // author is minted at submit from the submitting target, never
      // derived from what else is running (design §3.1).
      const inFlight = new Promise<void>((resolve) => {
        releaseAsk = resolve
      })
      // The spy is held by name, never asserted through
      // `testTarget.submit` — detaching the method from its object is
      // exactly the accidental call the unbound-method rule exists to
      // refuse.
      const agentSubmit = vi.fn(() => inFlight)
      const testTarget: InputTarget = {
        id: 'agent',
        label: 'Test agent',
        routesToShell: false,
        author: 'agent',
        submit: agentSubmit,
      }
      const registry = createRegistry()
      const shellTarget = (content as unknown as { shellTarget: ShellInputTarget }).shellTarget
      registry.register(shellTarget)
      registry.register(testTarget)
      ;(content as unknown as { inputTargets: InputTargetRegistry }).inputTargets = registry

      // The other target's submission goes out THROUGH THE SEAM A PERSON
      // REACHES: the editor's ⌘Enter target toggle flips the active target
      // to it, plain Enter submits the question through the router. The
      // promise never settles until the test releases it, so the whole
      // shell submit below happens while it is in flight.
      submitKey(ed, { metaKey: true })
      ed.insertText('a question')
      submitKey(ed)
      expect(agentSubmit).toHaveBeenCalledTimes(1)

      // The person flips back to the shell (⌘Enter again) and submits
      // their own command while the question is still pending.
      submitKey(ed, { metaKey: true })
      ed.insertText('echo mine')
      submitKey(ed)

      const ledger = (content as unknown as { ledger: CommandLedger }).ledger
      expect(ledger?.records().length).toBe(1)
      expect(ledger?.records()[0].author).toBe('shell')
      expect(ledger?.records()[0].command).toBe('echo mine')
      // The in-flight submission never settled during the shell submit —
      // its author never leaked onto the shell's record.
      releaseAsk()
    } finally {
      // An assertion failure must not leak the pending submission; release
      // is idempotent.
      releaseAsk()
      teardown()
    }
  })

  it('an agent-authored command carries the badge through the real submit path — the closest seam a person will reach (nocx-iadtt)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // No production target can submit a command as the agent today — that
      // arrives with the agent's own lane (the agent-mode epic). The
      // closest seam a person will actually reach once it exists is the
      // shell submit orchestration itself: a routesToShell target whose
      // author is the agent. Everything below is the REAL chain — the
      // ⌘Enter target toggle, the registry router, the ledger record, the
      // running block.
      const agentTarget: InputTarget = {
        id: 'agent',
        label: 'Agent',
        routesToShell: true,
        author: 'agent',
        submit: vi.fn(async () => {}),
      }
      const registry = createRegistry()
      const shellTarget = (content as unknown as { shellTarget: ShellInputTarget }).shellTarget
      registry.register(shellTarget)
      registry.register(agentTarget)
      ;(content as unknown as { inputTargets: InputTargetRegistry }).inputTargets = registry

      submitKey(ed, { metaKey: true })
      ed.insertText('echo agent-run')
      submitKey(ed)

      // The record is the agent's — minted at submit from the submitting
      // target, through the same orchestration a human's command takes.
      const ledger = (content as unknown as { ledger: CommandLedger }).ledger
      expect(ledger?.records().length).toBe(1)
      expect(ledger?.records()[0].author).toBe('agent')

      // The card opens on the runtime's authenticated start (nocx-2v80t.3.2)
      // and binds to the record by the echoed submit token.
      const lifecycle = lifecycleHandler(client)
      lifecycle({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      lifecycle({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-agent',
          state: 'open',
          origin: 'app',
          submitId: ledger?.records()[0].submitId,
          command: 'echo agent-run',
        },
      })

      // The block carries the badge — the whole happy path, driven through
      // the real orchestration, never a manufactured block.
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      const running = scrollback.blockManager.runningBlock
      expect(running?.author).toBe('agent')
      const mark = running?.el.querySelector('.ui-badge[data-author="agent"]')
      expect(mark).not.toBeNull()
      expect(mark?.getAttribute('data-tone')).toBe('info')
      expect(mark?.textContent).toBe('agent')
    } finally {
      teardown()
    }
  })

  it('keeps selection and ordinary block actions in Run without offering a grant', async () => {
    const { client, dispatcherCalls } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'git status', ['clean'])
      selectRows(block, 0, 1)

      const selection = window.getSelection()
      expect(selection?.isCollapsed).toBe(false)
      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('none')
      const markShortcut = new KeyboardEvent('keydown', {
        key: 'a',
        ctrlKey: true,
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      })
      document.body.dispatchEvent(markShortcut)
      expect(markShortcut.defaultPrevented).toBe(false)
      expect((content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks).toEqual([])

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const items = Array.from(
        document.querySelectorAll<HTMLButtonElement>(
          '[data-testid="block-actions-menu"] .ui-context-menu__item',
        ),
      )
      expect(items.find((item) => item.dataset.itemId === 'grant')).toBeUndefined()
      expect(items.map((item) => item.textContent)).toContain('Copy command')
      expect(
        ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')?.style.display,
      ).toBe('none')
      expect((content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks).toEqual([])
      expect(dispatcherCalls.some((call) => call.method === 'agent.ask')).toBe(false)
    } finally {
      teardown()
    }
  })
  it('keeps exact whole-block and row grants invisible through Ask → Run → Ask', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const whole = frozenBlockOf(content, 'git status', ['clean'])
      const rows = frozenBlockOf(content, 'npm test', ['first', 'second'])

      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Agent')
      selectRows(rows, 1, 2)
      const offer = document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')
      expect(offer).not.toBeNull()
      expect(offer?.closest<HTMLElement>('.mark-affordance')?.style.display).toBe('block')
      offer!.click()
      const grantState = content as unknown as { grantedBlocks: GrantBlock[] }
      expect(grantState.grantedBlocks).toHaveLength(1)

      whole.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const menuGrant = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )
      expect(menuGrant).not.toBeNull()
      menuGrant!.click()

      const before = [...grantState.grantedBlocks]
      expect(before).toHaveLength(2)
      expect(before[0]).toMatchObject({ blockEl: rows, start: 1, count: 1 })
      expect(before[1]).toMatchObject({ blockEl: whole })
      const chip = ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')!
      chip.click()
      expect(document.querySelector('.ui-floating-panel[data-open="true"]')).not.toBeNull()
      expect(whole.dataset.granted).toBe('true')
      expect(rows.querySelectorAll('.term-line[data-granted]')).toHaveLength(1)

      // Hiding the pane removes every body-level and inline grant surface,
      // including a menu that otherwise lives outside the pane subtree.
      whole.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      expect(document.querySelector('[data-testid="block-actions-menu"]')).not.toBeNull()
      content.setVisible(false)
      expect(chip.style.display).toBe('none')
      expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
      expect(document.querySelector('[data-testid="block-actions-menu"]')).toBeNull()
      expect(whole.dataset.granted).toBeUndefined()
      expect(rows.querySelector('.term-line[data-granted]')).toBeNull()
      // A target change while the tab is in the background cannot repaint
      // its body-level grant surfaces.
      submitKey(ed, { metaKey: true })
      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Agent')
      expect(chip.style.display).toBe('none')
      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('none')

      content.setVisible(true)
      expect(chip.style.display).toBe('')
      expect(whole.dataset.granted).toBe('true')
      expect(rows.querySelectorAll('.term-line[data-granted]')).toHaveLength(1)

      submitKey(ed, { metaKey: true })

      expect(activeLabel(content)).toBe('Shell')
      expect(chip.style.display).toBe('none')
      expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
      expect(whole.dataset.granted).toBeUndefined()
      expect(rows.querySelector('.term-line[data-granted]')).toBeNull()
      expect(grantState.grantedBlocks[0]).toBe(before[0])
      expect(grantState.grantedBlocks[1]).toBe(before[1])

      whole.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      expect(document.querySelector('.ui-context-menu__item[data-item-id="grant"]')).toBeNull()

      submitKey(ed, { metaKey: true })

      expect(activeLabel(content)).toBe('Agent')
      expect(chip.style.display).toBe('')
      expect(chip.textContent).toContain('2')
      expect(grantState.grantedBlocks[0]).toBe(before[0])
      expect(grantState.grantedBlocks[1]).toBe(before[1])
      expect(whole.dataset.granted).toBe('true')
      expect(rows.querySelectorAll('.term-line[data-granted]')).toHaveLength(1)
    } finally {
      teardown()
    }
  })

  it('a Run selection stays ordinary text and never takes the focus off the composer', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      ed.focus()
      const block = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      expect(activeLabel(content)).toBe('Shell')

      selectRows(block, 0, 1)

      // The focus transfer exists to protect an offer (nocx-45vkz). In Run
      // there is no offer, so taking the focus is a silent loss with nothing
      // bought by it: the composer keeps the caret and the selection stays
      // selectable, copyable text.
      expect(ed.rootContains(document.activeElement)).toBe(true)
      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('none')
      expect(window.getSelection()?.isCollapsed).toBe(false)
    } finally {
      teardown()
    }
  })

  it('a target change closes a block menu that is offering the wrong actions', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const registry = (content as unknown as { inputTargets: { setActive(id: string): void } })
        .inputTargets
      registry.setActive('agent')
      const block = frozenBlockOf(content, 'git status', ['clean'])

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      expect(document.querySelector('.ui-context-menu__item[data-item-id="grant"]')).not.toBeNull()

      // Programmatic, because that is the case the pane-hide sweep cannot
      // reach: ask entry and restore both call setActive without anybody
      // clicking. A menu left open goes on offering Mark in Run.
      registry.setActive('shell')

      expect(document.querySelector('[data-testid="block-actions-menu"]')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('re-evaluates an existing Run selection when the real chord switches to Ask', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      selectRows(block, 0, 1)
      const selection = window.getSelection()
      const selectedText = selection?.toString()

      expect(activeLabel(content)).toBe('Shell')
      expect(selection?.isCollapsed).toBe(false)
      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('none')

      submitKey(ed, { metaKey: true })

      expect(activeLabel(content)).toBe('Agent')
      expect(window.getSelection()?.toString()).toBe(selectedText)
      expect(
        document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')?.textContent,
      ).toBe('Mark 1 line')
    } finally {
      teardown()
    }
  })

  it('makes no offer until the drag ends (nocx-hp8p2.16)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'ls', ['one', 'two'])
      submitKey(ed, { metaKey: true })

      // Mid-gesture: selectionchange fires on every pixel of a drag, and the
      // button used to appear over a range that was still growing.
      document.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
      selectRows(block, 0, 1)
      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('none')

      // Released: the range is what the person meant, and the offer is made.
      document.dispatchEvent(new PointerEvent('pointerup', { bubbles: true }))
      expect(
        document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')?.textContent,
      ).toBe('Mark 1 line')
    } finally {
      teardown()
    }
  })

  it('keeps a second row mark instead of replacing the first (nocx-hp8p2.16)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'ls', ['one', 'two', 'three'])
      submitKey(ed, { metaKey: true })
      const chip = ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')!

      selectRows(block, 0, 1)
      document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!.click()
      expect(chip.textContent).toContain('1')

      // A different line of the SAME block. Every row shares the block's item
      // id, so identifying a mark by that id alone made this replace the
      // first one and a person could never mark two lines.
      selectRows(block, 2, 3)
      document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!.click()
      expect(chip.textContent).toContain('2')
      expect(block.querySelectorAll<HTMLElement>('.term-line[data-granted]')).toHaveLength(2)
    } finally {
      teardown()
    }
  })

  it('the block menu clears every mark on that block, rows included (nocx-hp8p2.16)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'ls', ['one', 'two', 'three'])
      submitKey(ed, { metaKey: true })
      const chip = ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')!
      selectRows(block, 0, 1)
      document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!.click()
      selectRows(block, 2, 3)
      document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!.click()
      expect(chip.textContent).toContain('2')

      // The menu speaks about the BLOCK, so with any of it marked its action
      // is Unmark — and it takes the row marks with it rather than leaving
      // half of them behind under a label that says nothing is marked.
      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const action = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )!
      expect(action.textContent).toBe('Unmark')
      action.click()

      expect(chip.textContent).toContain('0')
      expect(block.querySelector('.term-line[data-granted]')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('offers selected rows, marks only when the button is used, and unmarks from its menu', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      submitKey(ed, { metaKey: true })
      selectRows(block, 0, 1)
      const chip = ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')!
      expect(chip.dataset.state).toBe('default')
      expect(chip.textContent).toContain('0')
      expect(block.dataset.granted).toBeUndefined()
      expect(block.querySelector('.term-line[data-granted]')).toBeNull()

      const offer = document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!
      expect(offer.textContent).toBe('Mark 1 line')
      offer.click()
      expect(chip.dataset.state).toBe('chosen')
      expect(chip.textContent).toContain('1')
      expect(block.dataset.granted).toBeUndefined()
      expect(block.querySelector<HTMLElement>('.term-line')?.dataset.granted).toBe('true')
      expect(block.querySelectorAll<HTMLElement>('.term-line[data-granted]')).toHaveLength(1)

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )
      expect(unmark?.textContent).toBe('Unmark')
      unmark?.click()
      expect(chip.textContent).toContain('0')
      expect(block.dataset.granted).toBeUndefined()
      expect(block.querySelector('.term-line[data-granted]')).toBeNull()
    } finally {
      teardown()
    }
  })
  it('marks a whole block from its menu without carrying a line range', async () => {
    const { client, dispatcherCalls } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const block = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      submitKey(ed, { metaKey: true })
      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      document
        .querySelector<HTMLButtonElement>('.ui-context-menu__item[data-item-id="grant"]')!
        .click()
      expect(block.dataset.granted).toBe('true')
      expect(block.querySelector('.term-line[data-granted]')).toBeNull()
      typeAndAsk(ed, content, 'what happened?')
      await vi.waitFor(() => {
        expect(dispatcherCalls.filter((call) => call.method === 'agent.ask')).toHaveLength(1)
      })
      const params = recordedParams(dispatcherCalls, 'agent.ask')
      expect(params).toMatchObject({
        attachedContent: [{ itemId: block.dataset.entryId, command: 'ls', state: 'exited' }],
      })
      const attached = params.attachedContent
      if (!Array.isArray(attached)) throw new Error('ask payload missing attachedContent')
      expect(attached[0]).not.toHaveProperty('start')
      expect(attached[0]).not.toHaveProperty('count')
    } finally {
      teardown()
    }
  })

  it('two runs of prose behind one turn keep their boundary: the backend’s block id must reach the manager (w-call-id-order)', async () => {
    // The backend names the `text` child a delta appends to (ADR-0040):
    // it seals one block when a call arrives and opens the NEXT on the
    // first delta after it, and the id rides every delta. The one seam that
    // can lose that fact is the openAnswer wrapper — it used to forward
    // append with a single argument, so a delta naming the next block
    // reached the manager as "no boundary" and two runs of prose merged
    // into one block. That is the id-less turn's "2 of 3 children". The
    // turn must draw the two runs as two children, in the order they
    // arrived, with no call announcement needed to explain the split.
    const { client, dispatcherCalls } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      typeAndAsk(ed, content, 'how much disk is free?')
      await vi.waitFor(() => {
        expect(dispatcherCalls.filter((c) => c.method === 'agent.ask')).toHaveLength(1)
      })
      const withScrollback = content as unknown as { scrollback: ScrollbackController }
      const scrollback = withScrollback.scrollback
      // The run's entry id lands on the block when the ask RESOLVES — a
      // delta delivered before then is refused by the routing check
      // (agent-ask.ts, "a stale undefined id"). Wait on the DOM fact the
      // route keys on, never on a duration.
      await vi.waitFor(() => {
        const ask = Array.from(
          scrollback.scrollbackInner.querySelectorAll<HTMLElement>('.cmd-block'),
        ).find((b) => b.dataset.blockKind === 'ask')
        expect(ask?.dataset.entryId).toBe('entry-7')
      })

      // One run of prose, the block's id — then the boundary: the backend
      // sealed that block and the next delta names a NEW one. The split is
      // the backend's fact; all the renderer has to do is pass it through.
      deliverNotification(client, 'agent.runDelta', {
        runId: 7,
        entryId: 'entry-7',
        blockId: 'text-1',
        seq: 0,
        text: 'Let me check.',
      })
      deliverNotification(client, 'agent.runDelta', {
        runId: 7,
        entryId: 'entry-7',
        blockId: 'text-2',
        seq: 1,
        text: 'Plenty.',
      })

      const turn = Array.from(
        scrollback.scrollbackInner.querySelectorAll<HTMLElement>('.cmd-block'),
      ).find((b) => b.dataset.blockKind === 'ask')
      expect(turn).toBeTruthy()
      const prose = turn!.querySelectorAll<HTMLElement>(
        ':scope > .cmd-children > .cmd-block[data-block-kind="text"]',
      )
      expect(prose).toHaveLength(2)
      const bodies = Array.from(prose).map(
        (p) => p.querySelector('[data-answer-body]')?.textContent,
      )
      expect(bodies).toEqual(['Let me check.', 'Plenty.'])

      // The paired end (AGENTS.md rule 3): a chunk naming the SAME block
      // continues that run — the id is a boundary only when it CHANGES. The
      // wire fact must pass through the wrapper in both directions.
      deliverNotification(client, 'agent.runDelta', {
        runId: 7,
        entryId: 'entry-7',
        blockId: 'text-2',
        seq: 2,
        text: ' enough.',
      })
      const proseAfter = turn!.querySelectorAll<HTMLElement>(
        ':scope > .cmd-children > .cmd-block[data-block-kind="text"]',
      )
      expect(proseAfter).toHaveLength(2)
      const continued = Array.from(proseAfter).map(
        (p) => p.querySelector('[data-answer-body]')?.textContent,
      )
      expect(continued).toEqual(['Let me check.', 'Plenty. enough.'])
    } finally {
      teardown()
    }
  })

  it('with no endpoint configured, a question surfaces the refusal on the surface — the toast, never a silent drop', async () => {
    const client = makeClient()
    const dispatcherCalls: Array<{ method: string; params: unknown }> = []
    client.dispatcher.call.mockImplementation((method: string, params: unknown) => {
      dispatcherCalls.push({ method, params })
      if (method === 'agent.ask') {
        const err = new Error('no endpoint configured') as Error & { code?: number }
        err.code = -32603
        return Promise.reject(err)
      }
      return Promise.resolve({ frameId: 'frame-1' })
    })
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      const marked = frozenBlockOf(content, 'git status', ['clean'])
      submitKey(ed, { metaKey: true })
      marked.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      document
        .querySelector<HTMLButtonElement>('.ui-context-menu__item[data-item-id="grant"]')!
        .click()
      const grantChip = ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')!
      expect(grantChip.dataset.state).toBe('chosen')

      typeAndAsk(ed, content, 'why did it fail?')

      await vi.waitFor(() => {
        expect(dispatcherCalls.some((c) => c.method === 'agent.ask')).toBe(true)
      })
      await vi.waitFor(() => {
        expect(showToast).toHaveBeenCalledWith(
          expect.objectContaining({ level: 'warning', message: 'no endpoint configured' }),
        )
      })
      expect(marked.dataset.granted).toBe('true')
      expect(grantChip.dataset.state).toBe('chosen')
      // The editor stays up for the next attempt — a refusal is not a
      // handoff and not a dead end.
      expect(ed.isVisible).toBe(true)
    } finally {
      teardown()
    }
  })

  it('the token is a gutter beside the input, never text inside it — the textbox holds exactly what was typed (nocx-4wtlh)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // WHERE it renders is the contract. `.cm-content` carries
      // role="textbox", so a control inside it becomes part of the line's
      // text: a screen reader read the chip's word as content, and every
      // check that reads the prompt got `Run` glued to the command. The
      // indicator sits in ComposerFrame's field as a SIBLING of CM6's own
      // root (`view.dom`) now — not inside it at all — so it is outside
      // the document twice over: outside `view.dom` and, inside that,
      // outside `contentDOM`.
      const view = viewOf(ed)
      expect(ed.root.querySelector('.ui-mode-indicator')).not.toBeNull()
      expect(view.dom.querySelector('.ui-mode-indicator')).toBeNull()
      expect(view.contentDOM.querySelector('.ui-mode-indicator')).toBeNull()
      expect(view.contentDOM.textContent).toBe('')

      ed.insertText('ls -la')
      expect(view.contentDOM.textContent).toBe('ls -la')
      expect(view.state.selection.main.head).toBe('ls -la'.length)
      // Still there while typing, and still outside the text.
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      expect(view.contentDOM.querySelector('.ui-mode-indicator')).toBeNull()

      // A real selection in the draft does not hide it: a person selecting
      // part of their command still wants to know where Enter goes.
      view.dispatch({ selection: { anchor: 0, head: 2 } })
      expect(indicatorOf(ed)?.textContent).toBe('Run')
    } finally {
      teardown()
    }
  })

  it('the editor occupies its own grid column, so a second line starts where the first one does (nocx-ex636)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // The per-line CM6 gutter this replaced (task C, round 2) kept the
      // token off every line but the first with one blank cell per row —
      // bookkeeping the mockup pass's ComposerFrame field grid
      // (composer-frame.css) does not need: ONE indicator sits in the
      // field's leading grid column and the WHOLE editor sits in the
      // column after it, so every line the editor draws — wrapped or
      // typed — starts at that column's left edge by construction, not by
      // a marker repeated per row.
      ed.insertText('one\ntwo')
      const field = ed.root.querySelector<HTMLElement>('.ui-composer-frame__field')
      expect(field).not.toBeNull()
      const indicators = field!.querySelectorAll('.ui-mode-indicator')
      expect(indicators.length).toBe(1)
      const editorHost = field!.querySelector('.ui-composer-frame__editor')
      expect(editorHost).not.toBeNull()
      // DOM order IS the grid's column order (composer-frame.css places no
      // explicit `grid-column` — the field relies on source order), so the
      // indicator immediately preceding the editor host is what puts the
      // whole multiline document in the SECOND column, never split across
      // per-line cells the indicator could occupy one of.
      expect(indicators[0].nextElementSibling).toBe(editorHost)
      expect(viewOf(ed).contentDOM.textContent).toBe('onetwo')
    } finally {
      teardown()
    }
  })

  it('⌘Enter flips the target and sends nothing — the indicator names what the person does, Run ⇄ Ask (nocx-4wtlh)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      expect(indicatorOf(ed)?.textContent).toBe('Run')

      // The explicit switch: the registry's active target is the agent —
      // its label is still the registry's 'Agent', while the indicator
      // says what the person does: Ask.
      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Agent')
      expect(indicatorOf(ed)?.textContent).toBe('Ask')
      expect(indicatorOf(ed)?.dataset.target).toBe('agent')

      // And back: the switch is a toggle, and the word follows it.
      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Shell')
      expect(indicatorOf(ed)?.textContent).toBe('Run')
    } finally {
      teardown()
    }
  })
  it('each mode keeps its own draft — the same text, caret and scroll survive a switch away and back, and the indicator tone follows (nocx-4ff.7)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const view = viewOf(ed)

      // Shell: a half-typed command with the caret mid-line and the
      // editor scrolled — the state a person is in when they pause.
      ed.insertText('git status --short')
      view.dispatch({ selection: { anchor: 4, head: 8 } })
      view.scrollDOM.scrollTop = 7

      // Flip to Ask: the shell draft is saved, the line is cleared (the
      // agent has never been edited), and the indicator wears the agent
      // register — the kit's ModeIndicator, never a hand-rolled token.
      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Agent')
      expect(ed.getDoc()).toBe('')
      expect(indicatorOf(ed)?.textContent).toBe('Ask')
      expect(indicatorOf(ed)?.dataset.tone).toBe('info')
      expect(indicatorOf(ed)?.classList.contains('ui-mode-indicator')).toBe(true)

      // Type a question at Ask, then flip back to the shell.
      ed.insertText('what does status mean?')
      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Shell')
      // The half-typed command is still there — the same text, the same
      // caret, the same scroll: nothing shared was disturbed (criterion
      // 4), and the shell draft survived the round trip (criterion 1).
      expect(ed.getDoc()).toBe('git status --short')
      expect(ed.getSelection()).toEqual({ from: 4, to: 8 })
      expect(ed.getScrollTop()).toBe(7)
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      expect(indicatorOf(ed)?.dataset.tone).toBe('neutral')

      // And the agent's own draft survives the round trip in the other
      // direction (criterion 1's other end).
      submitKey(ed, { metaKey: true })
      expect(activeLabel(content)).toBe('Agent')
      expect(ed.getDoc()).toBe('what does status mean?')
      expect(ed.getSelection()).toEqual({
        from: 'what does status mean?'.length,
        to: 'what does status mean?'.length,
      })
    } finally {
      teardown()
    }
  })

  it('recall in each mode yields only that mode’s corpus — submit in both, walk back in Ask (nocx-4ff.7)', async () => {
    const { client, dispatcherCalls } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()
      const view = viewOf(ed)

      // A shell command — the shell's corpus (the store), never a
      // question's.
      ed.insertText('echo shell-only')
      submitKey(ed)
      const ledger = (content as unknown as { ledger: CommandLedger }).ledger
      await vi.waitFor(() => {
        expect(ledger?.records().some((r) => r.command === 'echo shell-only')).toBe(true)
      })
      // The handoff hid the editor; the next prompt re-shows it, as the
      // lifecycle would.
      ed.show()

      // A question at Ask — the agent's corpus, recorded editor-side.
      typeAndAsk(ed, content, 'what is the answer?')
      await vi.waitFor(() => {
        expect(dispatcherCalls.some((c) => c.method === 'agent.ask')).toBe(true)
      })

      // Walk back in Ask: Up opens recall, which serves the AGENT's
      // corpus — the question is there, the shell command is not. The
      // shell's own Up still walks shell commands (the store path); the
      // corpora never interleave.
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() => expect(recallOf(content).isOpen).toBe(true))
      await vi.waitFor(() => {
        const rows = Array.from(ed.root.querySelectorAll<HTMLElement>('.ui-floating-panel__row'))
        const texts = rows.map((r) => r.textContent ?? '')
        expect(texts.some((t) => t.includes('what is the answer?'))).toBe(true)
        expect(texts.some((t) => t.includes('echo shell-only'))).toBe(false)
      })
    } finally {
      teardown()
    }
  })
})

// Visibility gates live painting, never acceptance or correlated processing.
describe('TerminalContent visibility preserves the accepted painter frame', () => {
  async function controlledPaintPane() {
    const cwd = vi.fn()
    const mounted = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
      hooks: { onPaneCwdChange: cwd },
    })
    const internals = mounted.content as unknown as {
      _painter: CellPainter
      _cellModel: CellModel
      _selectionGesture: { screenChanged(snapshot: ScreenSnapshot): void }
      _liveHistory: { reflowed(): void }
      host: { contentSettled(): void }
    }
    const paint = vi.spyOn(internals._painter, 'apply')
    const selection = vi.spyOn(internals._selectionGesture, 'screenChanged')
    const reflow = vi.spyOn(internals._liveHistory, 'reflowed')
    const settled = vi.spyOn(internals.host, 'contentSettled')
    const queued = new Map<number, FrameRequestCallback>()
    let next = 1
    const raf = vi.spyOn(globalThis, 'requestAnimationFrame').mockImplementation((cb) => {
      const id = next++
      queued.set(id, cb)
      return id
    })
    const cancel = vi.spyOn(globalThis, 'cancelAnimationFrame').mockImplementation((id) => {
      queued.delete(id)
    })
    const flush = (): void => {
      for (let pass = 0; queued.size > 0 && pass < 50; pass++) {
        const callbacks = [...queued.values()]
        queued.clear()
        for (const cb of callbacks) cb(0)
      }
      expect(queued.size).toBe(0)
    }
    return {
      ...mounted,
      session: sessionOf(mounted.content),
      model: internals._cellModel,
      paint,
      selection,
      reflow,
      settled,
      cwd,
      cancel,
      flush,
      teardown: () => {
        mounted.teardown()
        raf.mockRestore()
        cancel.mockRestore()
      },
    }
  }

  it('cancels a pending paint on hide and paints only the latest hidden state on show', async () => {
    const p = await controlledPaintPane()
    try {
      p.content.setVisible(true)
      p.session.fireScreenFrame(screenFrame(1, ['pending']))
      p.content.setVisible(false)
      expect(p.cancel).toHaveBeenCalled()
      p.flush()
      expect(p.paint).not.toHaveBeenCalled()

      p.session.fireScreenFrame(screenFrame(2, ['hidden normal']))
      const alternate = screenFrame(3, ['hidden alternate'], { cols: 60, rows: 6 })
      alternate.state.bufferKind = 'alternate'
      p.session.fireScreenFrame(alternate)
      expect(scrollbackFor(p.content).mode).toBe('fullscreen')
      const latest = screenFrame(4, ['latest normal'], { cols: 50, rows: 5 })
      latest.state.title = 'hidden title'
      latest.state.cwd = { host: '', path: '/hidden/project' }
      p.session.fireScreenFrame(latest)
      p.session.fireEffect({
        sessionId: p.session.sessionId,
        generation: '1',
        effectId: '1',
        kind: 'promptBoundary',
        title: '',
        body: '',
        screenRevision: 4,
      })
      p.flush()
      expect(p.paint).not.toHaveBeenCalled()
      expect(p.model.current()?.revision).toBe(4)
      expect(p.model.current()?.geometry).toMatchObject({ cols: 50, rows: 5 })
      expect(scrollbackFor(p.content).mode).toBe('unstructured')
      expect(p.selection).toHaveBeenCalledTimes(4)
      expect(p.reflow).toHaveBeenCalledTimes(3)
      expect(p.settled).toHaveBeenCalledOnce()
      expect(p.tab.title).toBe('hidden title')
      expect(p.cwd).toHaveBeenCalledWith('/hidden/project')
      expect(p.tab.pane.querySelectorAll('.term-grid-row')).toHaveLength(0)

      p.content.setVisible(true)
      p.flush()
      expect(p.paint).toHaveBeenCalledOnce()
      expect(p.paint.mock.calls[0][0]).toBe(p.model.current())
      expect(p.tab.pane.querySelector('.term-grid')?.textContent).toContain('latest normal')
      expect(p.tab.pane.querySelectorAll('.term-grid-row')).toHaveLength(5)
    } finally {
      p.teardown()
    }
  })

  it('defers theme and font repaint requests while hidden but paints an unfocused visible pane', async () => {
    const p = await controlledPaintPane()
    const other = document.createElement('button')
    document.body.append(other)
    try {
      p.session.fireScreenFrame(screenFrame(1, ['still live']))
      setCurrentTheme({ ...getCurrentTheme() })
      const root = document.createElement('div')
      root.dataset.terminalFont = 'nordwand-mono'
      await applyTerminalFont('default', root)
      p.flush()
      expect(p.paint).not.toHaveBeenCalled()
      other.focus()
      p.content.setVisible(true)
      p.flush()
      expect(document.activeElement).toBe(other)
      expect(p.paint).toHaveBeenCalledOnce()
      p.session.fireScreenFrame(screenFrame(2, ['unfocused output']))
      p.flush()
      expect(p.paint).toHaveBeenCalledTimes(2)
      expect(p.tab.pane.querySelector('.term-grid')?.textContent).toContain('unfocused output')
    } finally {
      other.remove()
      p.teardown()
    }
  })

  it('does not revive a pending or hidden painter after disposal', async () => {
    const p = await controlledPaintPane()
    try {
      p.content.setVisible(true)
      p.session.fireScreenFrame(screenFrame(1, ['pending']))
      p.tab.close()
      p.flush()
      p.content.setVisible(true)
      p.flush()
      expect(p.paint).not.toHaveBeenCalled()
    } finally {
      p.teardown()
    }
  })

  it('paints the accepted frame when the pane becomes visible', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['retained screen']))
      content.setVisible(true)
      await vi.waitFor(() =>
        expect(tab.pane.querySelector<HTMLElement>('.term-grid')?.textContent).toContain(
          'retained screen',
        ),
      )
      expect(tab.pane.classList.contains('active')).toBe(true)
    } finally {
      teardown()
    }
  })

  it('keeps the accepted rows while hidden, then restores the same painted grid', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['retained screen']))
      content.setVisible(true)
      await vi.waitFor(() =>
        expect(tab.pane.querySelector('.term-grid')?.textContent).toContain('retained screen'),
      )
      const row = tab.pane.querySelector('.term-grid-row')
      content.setVisible(false)
      expect(tab.pane.classList.contains('active')).toBe(false)
      expect(tab.pane.querySelector('.term-grid-row')).toBe(row)
      content.setVisible(true)
      expect(tab.pane.classList.contains('active')).toBe(true)
      expect(tab.pane.querySelector('.term-grid-row')).toBe(row)
    } finally {
      teardown()
    }
  })

  it('still toggles the active class on each visibility change', async () => {
    const { content, tab, teardown } = await mountTerminal()
    try {
      content.setVisible(true)
      expect(tab.pane.classList.contains('active')).toBe(true)
      content.setVisible(false)
      expect(tab.pane.classList.contains('active')).toBe(false)
    } finally {
      teardown()
    }
  })
})

// ── The tab is named by what runs in it (nocx-n8n82) ─────────────────────
//
// A tab's title has exactly three sources, in absolute order: the
// program's own OSC 0/2 title, the command currently running in it — the
// ledger's running record, the app-owned submit (ADR-0024 §5) — and the
// cwd. This suite watches the middle source through the real submit and
// the real authenticated completion, so a regression in either direction
// fails here.
describe('the running command names the tab (nocx-n8n82)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the wire seam tests deliver authenticated facts through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    const subscribe = client.dispatcher.subscribe
    expect(subscribe).toHaveBeenCalledWith('lifecycle.changed', expect.any(Function))
    return lifecycleHandler(client)
  }

  /** jsdom does not implement scrollTo/scrollIntoView; the block model
   *  calls them on submit. Stub them for the duration — the same trade
   *  the projections tests make. Returns the restore. */
  function stubScrolling(): () => void {
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    return () => {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
    }
  }

  /** Dispatch a submit key exactly where a user's keystroke lands. */
  function submitEnter(view: EditorView): void {
    view.contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
  }

  it('shows the running command while it runs and the cwd when it finishes', async () => {
    const client = makeClient()
    const { view, ed, content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      // A fresh tab is named after where it is.
      expect(tab.title).toBe(FIXTURE_DIRECTORY_LABEL)

      // 1. The authenticated prompt gives the editor the keyboard.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)

      // 2. The user submits; the command is now the ledger's running
      //    record, and the tab is named by it.
      ed.insertText('herdr')
      submitEnter(view)
      expect(tab.title).toBe('herdr')

      // 3. The authenticated completion ends the command: the record is
      //    no longer running, and the tab falls back to the cwd.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'b'.repeat(64),
          completedAt: '2026-08-16T00:00:00Z',
        },
      })
      expect(tab.title).toBe(FIXTURE_DIRECTORY_LABEL)
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('a program that declares an OSC 0/2 title still wins over the command name', async () => {
    const client = makeClient()
    const { view, ed, content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('herdr')
      submitEnter(view)
      expect(tab.title).toBe('herdr')

      // Absolute order: the program's own title outranks the command name.
      fireFrameMetadata(sessionOf(content), 1, { title: 'herdr 2.0' })
      expect(tab.title).toBe('herdr 2.0')
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('does not resurrect a name the program cleared on the way out', async () => {
    const client = makeClient()
    const { view, ed, content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('herdr')
      submitEnter(view)
      expect(tab.title).toBe('herdr')

      // The program sets its own title and clears it on the way out —
      // an OSC 0/2 with an EMPTY string (tabs.ts documents the gesture).
      fireFrameMetadata(sessionOf(content), 1, { title: 'herdr' })
      fireFrameMetadata(sessionOf(content), 2, { title: '' })
      // The command is still the running record: without the clear guard
      // the running-command source would resurrect 'herdr' right here.
      const ledger = (content as unknown as { ledger: CommandLedger }).ledger
      expect(ledger.records()[0].status).toBe('running')
      expect(tab.title).toBe(FIXTURE_DIRECTORY_LABEL)

      // And when the command finally completes, the name stays gone.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-1',
          state: 'completed',
          exitCode: 0,
          fence: 'b'.repeat(64),
          completedAt: '2026-08-16T00:00:00Z',
        },
      })
      expect(tab.title).toBe(FIXTURE_DIRECTORY_LABEL)
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('shows the location line when the title is a name of its own, and none when the title IS the location', async () => {
    const onSubtitleChange = vi.fn()
    const onProgramTitleChange = vi.fn()
    const client = makeClient()
    const { view, ed, content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { onSubtitleChange, onProgramTitleChange } },
      client,
    )
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      // The title IS the cwd: a second line would print the same string.
      expect(tab.title).toBe(FIXTURE_DIRECTORY_LABEL)
      expect(onSubtitleChange).toHaveBeenLastCalledWith('')

      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('herdr')
      submitEnter(view)
      // A command title is a name of its own: the location is the second
      // line, never a duplicate.
      expect(tab.title).toBe('herdr')
      expect(onSubtitleChange).toHaveBeenLastCalledWith(FIXTURE_CWD)
      // The agent-state classifier is fed the real program title, which
      // is empty here — never the composed string.
      expect(onProgramTitleChange).toHaveBeenLastCalledWith('')

      // A program title is also a name of its own — same second line.
      fireFrameMetadata(sessionOf(content), 1, { title: 'herdr' })
      expect(onSubtitleChange).toHaveBeenLastCalledWith(FIXTURE_CWD)
      expect(onProgramTitleChange).toHaveBeenLastCalledWith('herdr')
    } finally {
      restoreScroll()
      teardown()
    }
  })

  it('names the machine with the SAME string the strip prints, never a second spelling', async () => {
    // The amendment to nocx-hbdw4.4. The operations list is global — one
    // list for every tab — so each row has to say which machine, and the
    // name must be the one the person already reads on the tab. A second
    // `${user}@${host}` beside this one would agree everywhere anybody
    // looked and disagree the day there is no user, which is why the
    // derivation moved to machine-name.ts and both sides call it.
    //
    // Asserted against the strip's OWN value rather than against a literal:
    // if the two ever part, this fails.
    const onSubtitleChange = vi.fn()
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true, hooks: { onSubtitleChange } },
      client,
    )
    const handler = factHandler(client)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      // A program title is a name of its own, so the strip's second line is
      // the LOCATION line — which is where the strip names a machine.
      fireFrameMetadata(sessionOf(content), 1, { title: 'herdr' })

      // Local: the strip has nothing but the directory to show, and the
      // origin still names the machine, in words rather than a blank.
      expect(onSubtitleChange).toHaveBeenLastCalledWith(FIXTURE_CWD)
      expect(content.activeOrigin()?.machine).toBe('This machine')

      // Walk into a hand-typed ssh (protocol §9: the parent suspends first).
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({ lane: 'lane-1', lifecycle: 'native' })
      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: '192.0.2.93', user: 'pi' },
      })

      const subtitle = onSubtitleChange.mock.lastCall?.[0] as string
      expect(subtitle).toBe('pi@192.0.2.93')
      expect(content.activeOrigin()?.machine).toBe(subtitle)
    } finally {
      restoreScroll()
      teardown()
    }
  })
})

// A session the backend cannot currently reach (nocx-iarf9). Nothing has
// ended, so the tab stays exactly as it is — and the user is told, because a
// terminal that has silently stopped being connected to anything looks
// identical to one that is simply quiet.
describe('the reachability axis', () => {
  // It used to be two toasts, and that was the wrong shape for it. A dropped
  // connection is a PERSISTENT CONDITION, and the product already said so in
  // its own words — "a toast fades whether or not the condition has come
  // back" (connection-notice.tsx). So the statement is a mark that lasts
  // exactly as long as the condition, and the pane is its one owner.
  it('states an unreachable host, and takes it back when the host answers', async () => {
    const client = makeClient()
    const conditions: ConnectionCondition[] = []
    const latest = () => conditions[conditions.length - 1]
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onConnectionConditionChange: (c) => conditions.push(c) } },
      client,
    )
    try {
      content.setVisible(true)
      const session: SessionFake = client._sessions[0]
      expect(session.onLiveness).toHaveBeenCalled()

      session.fireLiveness('unknown')
      expect(latest()).toBe('unreachable')

      session.fireLiveness('alive', 3)
      expect(latest()).toBe('reachable')
    } finally {
      teardown()
    }
  })

  // A host answering LATE is not a host that is gone, and the two must not
  // draw the same thing: one is a warning about the link, the other says the
  // work on the far side may be unreachable.
  it('distinguishes a slow host from an unreachable one', async () => {
    const client = makeClient()
    const conditions: ConnectionCondition[] = []
    const latest = () => conditions[conditions.length - 1]
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onConnectionConditionChange: (c) => conditions.push(c) } },
      client,
    )
    try {
      content.setVisible(true)
      const session: SessionFake = client._sessions[0]

      session.fireSlowLiveness(3)
      expect(latest()).toBe('slow')

      session.fireLiveness('unknown', 4)
      expect(latest()).toBe('unreachable')
    } finally {
      teardown()
    }
  })

  // The grade is the BACKEND's (it has hysteresis, which the milliseconds
  // alone cannot reproduce). A renderer that thresholded the number for
  // itself would be a second derivation of one concept.
  it('does not invent a slow grade from the milliseconds', async () => {
    const client = makeClient()
    const conditions: ConnectionCondition[] = []
    const latest = () => conditions[conditions.length - 1]
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onConnectionConditionChange: (c) => conditions.push(c) } },
      client,
    )
    try {
      content.setVisible(true)
      const session: SessionFake = client._sessions[0]
      session.fireLiveness('alive', 3)
      expect(latest()).toBe('reachable')
    } finally {
      teardown()
    }
  })
})

describe('reconnecting a lost pane (nocx-rtzo4)', () => {
  type LostCb = (exit: { sessionId: string; cause: 'exited' | 'interrupted' }) => void
  const loseSession = (session: SessionFake) => {
    const cb = session.onExit.mock.calls[0]?.[0] as LostCb
    cb({ sessionId: session.sessionId, cause: 'interrupted' })
  }

  // THE POINT OF THE WHOLE THING: the tab is not closed and the work stays
  // readable. Everything else in this block is about not lying to the person
  // whose work it is.
  it('gets a new session without losing what the old one printed', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const first: SessionFake = client._sessions[0]
      first.fireData('what the old shell printed\r\n')

      loseSession(first)
      expect(content.connectionCondition()).toBe('lost')

      expect(await content.reconnect()).toBe(true)
      // A SECOND session, not the first one revived.
      expect(client._sessions.length).toBe(2)
      expect(content.connectionCondition()).toBe('reachable')
      // And the pane is usable again: the loss is no longer terminal.
      expect(content.sessionLost()).toBe(false)
    } finally {
      teardown()
    }
  })

  // A pane whose host has merely stopped answering may still have a live
  // shell on the far side. Replacing it would kill work that was never in
  // danger, so the offer is refused rather than merely not shown.
  it('refuses to reconnect a session that has not ended', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const session: SessionFake = client._sessions[0]
      session.fireLiveness('unknown')
      expect(content.connectionCondition()).toBe('unreachable')

      expect(await content.reconnect()).toBe(false)
      expect(client._sessions.length).toBe(1)
    } finally {
      teardown()
    }
  })

  // The scrollback carries a mark saying the shell changed. Without it a
  // person reads one continuous session and is wrong about what is still
  // running on the host.
  it('marks where the old shell ended', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      loseSession(client._sessions[0])
      await content.reconnect()
      expect(document.querySelector('[data-reconnect-boundary]')).not.toBeNull()
    } finally {
      teardown()
    }
  })

  // A second reconnect must not stack on the first: two binds racing would
  // leave one session unowned and its subscriptions delivering into a pane
  // that has moved on.
  it('does not start a second attempt while one is in flight', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      loseSession(client._sessions[0])
      const [a, b] = await Promise.all([content.reconnect(), content.reconnect()])
      expect([a, b].filter(Boolean).length).toBe(1)
      expect(client._sessions.length).toBe(2)
    } finally {
      teardown()
    }
  })
})

// ── liveWork: what is RUNNING in this pane (nocx-isoph.6, design D6) ───────
//
// The close prompts name what dies before it dies, and this is where the
// naming gets its facts. It is deliberately a third answer beside
// activeOrigin and lineage — see the capability's own comment in
// pane-content.ts for why merging any two of them loses a case.
describe('liveWork — what a close would destroy in this pane (nocx-isoph.6)', () => {
  /** The lifecycle fact handler TerminalContent registered on the fake
   *  dispatcher — the seam a live prompt arrives through. */
  function factHandler(client: ClientFake): (p: unknown) => void {
    return lifecycleHandler(client)
  }

  it('a local shell at a prompt holds a session and is running nothing', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // Not null — the pane holds a live session — and not live either:
      // closing an idle local shell loses nothing (live-work.ts).
      expect(content.liveWork()).toEqual({ command: null, host: null })
    } finally {
      teardown()
    }
  })

  it('names the command while it is running', async () => {
    const client = makeClient()
    const { content, ed, view, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      factHandler(client)({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('ansible-playbook deploy.yml')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

      expect(content.liveWork()?.command).toBe('ansible-playbook deploy.yml')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('names the machine a hand-typed ssh walked onto, with nothing running on it', async () => {
    // The case the capability exists for: a session on another machine is
    // live AT ITS PROMPT — the connection, and whatever state it is holding
    // open, is what the close destroys — and nobody opened it through a
    // profile, so the only record of it is the authenticated domain the pane
    // walked into (nocx-u7uh.11).
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(content.liveWork()).toEqual({ command: null, host: null })

      // The parent suspends as the child establishes (protocol §9), then the
      // hand-typed `ssh deploy@prod-01`.
      handler({ lane: 'lane-1', lifecycle: 'native' })
      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: 'prod-01', user: 'deploy' },
      })

      expect(content.liveWork()).toEqual({ command: null, host: 'deploy@prod-01' })
    } finally {
      teardown()
    }
  })

  it('answers null once the shell has exited, where lineage still speaks for the tab', async () => {
    const session = makeSession()
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const exitCb = session.onExit.mock.calls[0]?.[0] as (exit: {
        sessionId: string
        cause: 'exited' | 'interrupted'
      }) => void
      exitCb({ sessionId: session.sessionId, cause: 'exited' })

      // Nothing is running: the loss already happened, and a prompt naming
      // it would be describing a tab it cannot save.
      expect(content.liveWork()).toBeNull()
      // The tab is still on screen, so its provenance is still true. The two
      // answers differ on purpose.
      expect(content.lineage()?.sessionId).toBe(session.sessionId)
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// The session waits for its pane's row (nocx-rtg0.29)
// ═══════════════════════════════════════════════════════════════════════════
describe('the session waits for its pane row (nocx-rtg0.29)', () => {
  const PANE = '0199c5e2-8f3a-7c41-b9d6-2e7a04f18b53'

  it('does not open while the row is still in flight, then names the pane', async () => {
    let admit!: (registered: boolean) => void
    const registered = new Promise<boolean>((resolve) => {
      admit = resolve
    })
    const client = makeClient()
    const mounting = mountTerminal(makeClipboard(), { pane: { paneId: PANE, registered } }, client)

    // The lifecycle subscription is wired in mount() IMMEDIATELY before the
    // open, so waiting for it is waiting for the exact point the session
    // used to be opened from. Reaching it with no open having been sent is
    // what proves the order — no duration is involved, which is the only
    // kind of ordering assertion that survives a fast machine.
    await vi.waitFor(() => expect(client.dispatcher.subscribe).toHaveBeenCalled())
    expect(client.openSession).not.toHaveBeenCalled()

    admit(true)
    const { teardown } = await mounting
    try {
      expect(client.openSession).toHaveBeenCalledTimes(1)
      expect(client.openSession.mock.calls[0][1]).toEqual({ paneId: PANE })
    } finally {
      teardown()
    }
  })

  // Criterion 4: no layout store is not a refusal. The id is still one
  // identity for history.record and secrets.paneClosed; it simply names no
  // row, so the open must not claim it does.
  it('opens without a paneId when the pane has no row, rather than refusing', async () => {
    const client = makeClient()
    const { teardown } = await mountTerminal(
      makeClipboard(),
      { pane: { paneId: PANE, registered: Promise.resolve(false) } },
      client,
    )
    try {
      expect(client.openSession).toHaveBeenCalledTimes(1)
      expect(client.openSession.mock.calls[0][1]).toEqual({})
    } finally {
      teardown()
    }
  })

  it('names the pane on a profile ssh open too — an ssh tab is a pane', async () => {
    const client = makeClient()
    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'ssh:test:1', host: 'example.test' },
        pane: { paneId: PANE, registered: Promise.resolve(true) },
      },
      client,
    )
    try {
      expect(client.openSSHSession.mock.calls[0][2]).toEqual({ paneId: PANE })
    } finally {
      teardown()
    }
  })

  it('names the pane on a direct-host ssh open too', async () => {
    const client = makeClient()
    const { teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: '', host: 'example.test' },
        pane: { paneId: PANE, registered: Promise.resolve(true) },
      },
      client,
    )
    try {
      expect(client.openSSHSessionByHost.mock.calls[0][3]).toEqual({ paneId: PANE })
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// The pane reports where it IS, so a restore reopens it there (nocx-zkiv4)
// ═══════════════════════════════════════════════════════════════════════════
describe('the pane reports its directory (nocx-zkiv4)', () => {
  it('reports a verified local cwd once, and not again for the same directory', async () => {
    const reported: string[] = []
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { onPaneCwdChange: (cwd) => reported.push(cwd) } },
      client,
    )
    try {
      // The session-open cwd is a provider's fallback, not a report: nothing
      // has been verified yet, so nothing may be stored (AD-5).
      expect(reported).toEqual([])

      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: '', path: '/repo/frontend' } })
      expect(reported).toEqual(['/repo/frontend'])

      // A shell prints its prompt many times in one directory. The row is
      // written on CHANGE, or sitting still would cost a write per prompt.
      fireFrameMetadata(sessionOf(content), 2, { cwd: { host: '', path: '/repo/frontend' } })
      expect(reported).toEqual(['/repo/frontend'])

      fireFrameMetadata(sessionOf(content), 3, { cwd: { host: '', path: '/repo/internal' } })
      expect(reported).toEqual(['/repo/frontend', '/repo/internal'])
    } finally {
      teardown()
    }
  })

  it('never reports a directory from an ssh pane, because a local shell cannot reopen there', async () => {
    const reported: string[] = []
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {
        ssh: { profileId: 'p-1', host: 'pi.local' },
        hooks: { onPaneCwdChange: (cwd) => reported.push(cwd) },
      },
      client,
    )
    try {
      // /home/pi on a Raspberry Pi is not /home/pi here. Storing it would
      // send the restored pane to a directory that does not exist on this
      // machine — or, worse, to a different one that does.
      fireFrameMetadata(sessionOf(content), 1, { cwd: { host: 'pi.local', path: '/home/pi' } })
      expect(reported).toEqual([])
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// A pane draws its past the first time it is shown (nocx-m3fqk)
// ═══════════════════════════════════════════════════════════════════════════
describe('a pane draws its past (nocx-m3fqk)', () => {
  function storeWith(entries: unknown[], body: string | null) {
    const client = makeClient()
    client.call.mockImplementation((method: string, params?: unknown) => {
      if (method === 'ledger.query') {
        const p = params as { paneId?: string }
        return Promise.resolve({
          entries: p.paneId ? entries : [],
          scope: 'everywhere',
          exhausted: true,
          hasRows: entries.length > 0,
          coverage: null,
        })
      }
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: {},
          edges: [],
          artifacts: body === null ? [] : [{ id: 'art-1', mediaType: 'application/vt' }],
        })
      }
      if (method === 'ledger.artifact') {
        return Promise.resolve({
          id: 'art-1',
          mediaType: 'application/vt',
          body: body ?? '',
          truncated: null,
          byteLen: (body ?? '').length,
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    return client
  }

  const entry = (over: Record<string, unknown> = {}) => ({
    id: 'e-1',
    seq: 1,
    environmentId: 'env',
    host: null,
    cwd: '/repo',
    kind: 'shell',
    intent: 'make test',
    phase: 'closed',
    status: 'success',
    submittedAt: 1,
    startedAt: 1,
    endedAt: 2,
    durationMs: 1200,
    exitCode: 0,
    maskedCount: 0,
    maskedKinds: [],
    redactions: [],
    ...over,
  })

  it('draws the blocks it ran before, above a boundary, marked as not live', async () => {
    const client = storeWith([entry({ id: 'e-2', intent: 'make lint' }), entry()], '\u001b[32mPASS')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(2)
      })

      // Oldest first: the page comes back newest-first and a person reads
      // their past downwards.
      const restored = [...inner.querySelectorAll('[data-restored="true"]')]
      expect(restored[0].textContent).toContain('make test')
      expect(restored[1].textContent).toContain('make lint')

      // The boundary, and everything restored ABOVE it: what makes "this
      // shell is a new one" visible rather than implied (ADR-0019 §3).
      const boundary = inner.querySelector('[data-restore-boundary="true"]')
      expect(boundary).not.toBeNull()
      expect(
        boundary!.compareDocumentPosition(restored[1]) & Node.DOCUMENT_POSITION_PRECEDING,
      ).toBeTruthy()

      // Drawn ONCE: a pane is shown on every tab switch.
      content.setVisible(false)
      content.setVisible(true)
      await Promise.resolve()
      expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(2)
    } finally {
      teardown()
    }
  })

  it('the menu and selection offer produce the same block grant, but selection alone marks nothing', async () => {
    const client = storeWith([entry()], 'output')
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      ed.show()
      switchInputTarget(ed)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-restored="true"]')).not.toBeNull()
      })
      const block = inner.querySelector<HTMLElement>('[data-restored="true"]')!
      expect(block.dataset.entryId).toBe('e-1')

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const mark = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )
      expect(mark?.textContent).toBe('Ask about this block')
      mark?.click()

      const grantState = content as unknown as { grantedBlocks: GrantBlock[] }
      const viaMenu = grantState.grantedBlocks[0]
      expect(viaMenu?.itemId).toBe('e-1')
      expect(
        ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')?.textContent,
      ).toContain('1')

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )
      expect(unmark?.textContent).toBe('Unmark')
      unmark?.click()
      expect(grantState.grantedBlocks).toEqual([])

      const output = block.querySelector<HTMLElement>('.cmd-output')!
      const selection = window.getSelection()!
      const range = document.createRange()
      range.selectNodeContents(output)
      range.getClientRects = () => [new DOMRect(100, 200, 200, 20)] as unknown as DOMRectList
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))

      expect(grantState.grantedBlocks).toEqual([])
      const offer = document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')
      expect(offer?.textContent).toBe('Mark block')
      offer?.click()
      expect(grantState.grantedBlocks).toEqual([viaMenu])
    } finally {
      teardown()
    }
  })

  it('clearing or collapsing a selection dismisses the mark offer without marking', async () => {
    const client = storeWith([entry()], 'output')
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      ed.show()
      switchInputTarget(ed)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-restored="true"]')).not.toBeNull()
      })
      const block = inner.querySelector<HTMLElement>('[data-restored="true"]')!
      const output = block.querySelector<HTMLElement>('.cmd-output')!
      const selection = window.getSelection()!
      const range = document.createRange()
      range.selectNodeContents(output)
      range.getClientRects = () => [new DOMRect(100, 200, 200, 20)] as unknown as DOMRectList
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))
      expect(document.querySelector('.mark-affordance .ui-button')).not.toBeNull()

      selection.removeAllRanges()
      document.dispatchEvent(new Event('selectionchange'))
      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('none')
      expect((content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks).toEqual([])
    } finally {
      teardown()
    }
  })

  /** What CodeMirror 6 does to a selection made outside it while it holds
   *  focus: `docView.updateSelection` puts the DOM selection back on its own
   *  document — and returns early unless the contentDOM is the ACTIVE
   *  ELEMENT (@codemirror/view). That conditional is the whole contract
   *  here, so the rule is applied by hand rather than waited for: jsdom runs
   *  no measure cycle, and a test that waited out the ~50ms window would be
   *  asserting a duration instead of an owner. */
  function restoreSelectionLikeCodeMirror(view: EditorView): void {
    if (document.activeElement !== view.contentDOM) return
    const selection = window.getSelection()
    selection?.removeAllRanges()
    const range = document.createRange()
    range.selectNodeContents(view.contentDOM)
    range.collapse(true)
    selection?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  }

  // ── ONE OWNER FOR THE DOCUMENT SELECTION (nocx-45vkz) ─────────────────
  //
  // A selection can be made in the scrollback WITHOUT taking focus off the
  // composer — programmatically, from a keyboard, or by any gesture that is
  // not a mouse drag beginning on a block. The scrollback then offers a
  // grant over a selection CodeMirror still believes it owns, and about
  // 50ms later CodeMirror restores the selection into its own document: the
  // handler below sees a collapsed selection and hides the offer nobody has
  // had time to press. Both surfaces were claiming one input and the
  // composer won by evaluation order, which is exactly the arrangement
  // AGENTS.md forbids.
  //
  // So the claim moves the focus with it. The offer must outlive the
  // restore, not race it.
  it('a selection claimed by the scrollback takes the focus off the composer, so the offer outlives the restore', async () => {
    const client = storeWith([entry()], 'output')
    const { ed, view, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      ed.show()
      switchInputTarget(ed)
      // The state a person is actually in: drafting a question, caret in the
      // composer, when they go to select the output they want to point at.
      ed.focus()
      expect(ed.rootContains(document.activeElement)).toBe(true)

      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-restored="true"]')).not.toBeNull()
      })
      const block = inner.querySelector<HTMLElement>('[data-restored="true"]')!
      const output = block.querySelector<HTMLElement>('.cmd-output')!
      const selection = window.getSelection()!
      const range = document.createRange()
      range.selectNodeContents(output)
      range.getClientRects = () => [new DOMRect(100, 200, 200, 20)] as unknown as DOMRectList
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))

      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('block')
      // The owner changed hands with the claim — this, and not the speed of
      // the press, is what disarms the restore below.
      expect(ed.rootContains(document.activeElement)).toBe(false)

      restoreSelectionLikeCodeMirror(view)

      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('block')
      const offer = document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!
      offer.click()
      expect((content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks[0]?.itemId).toBe(
        'e-1',
      )
    } finally {
      teardown()
    }
  })

  // ── AND THE RELEASE MUST NOT TAKE THE SELECTION WITH IT (nocx-45vkz) ──
  //
  // Blurring an editing host is itself a selection change on some engines.
  // Measured in the e2e container on WebKit, polling the live DOM frame by
  // frame right after the claim:
  //
  //   after-addRange  active=DIV.cm-content  collapsed=false rc=1  wd=none
  //   selchange#1     active=BODY            collapsed=true  rc=0  wd=block
  //   selchange#2     active=BODY            collapsed=true  rc=0  wd=none
  //
  // The blur emptied the document selection synchronously — rangeCount 1 to
  // 0 — and the selectionchange announcing it arrived a frame later, where
  // the guard at the top of the handler read it as "the selection is gone"
  // and hid the offer the same handler had just shown. Chromium keeps the
  // selection across the blur and never sent that second event, so the fix
  // for one engine was a deterministic regression on the other.
  //
  // The handler therefore re-asserts its claim after releasing the focus,
  // conditioned on what the selection actually holds. The engine that does
  // not empty it takes the no-op branch. jsdom is a third engine again — it
  // keeps the selection too — so the emptying is applied BY HAND from the
  // blur event, the same way the test above applies CodeMirror's restore
  // rule by hand, and for the same reason: the contract is what the handler
  // must survive, not what one runtime happens to do.
  it('a selection survives the blur that claims it, so the engine that empties it on blur cannot hide the offer', async () => {
    const client = storeWith([entry()], 'output')
    const { ed, view, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      ed.show()
      switchInputTarget(ed)
      ed.focus()
      const focused = document.activeElement as HTMLElement
      expect(ed.rootContains(focused)).toBe(true)
      // WebKit, by hand: the document selection belongs to the editing host,
      // and goes when the host does.
      focused.addEventListener(
        'blur',
        () => {
          window.getSelection()?.removeAllRanges()
        },
        { once: true },
      )

      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-restored="true"]')).not.toBeNull()
      })
      const block = inner.querySelector<HTMLElement>('[data-restored="true"]')!
      const output = block.querySelector<HTMLElement>('.cmd-output')!
      const selection = window.getSelection()!
      const range = document.createRange()
      range.selectNodeContents(output)
      range.getClientRects = () => [new DOMRect(100, 200, 200, 20)] as unknown as DOMRectList
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))

      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('block')
      expect(ed.rootContains(document.activeElement)).toBe(false)
      // The person's highlight is still under the offer being made about it:
      // the surface that took the focus kept the selection it took it for.
      const held = window.getSelection()!
      expect(held.rangeCount).toBe(1)
      expect(held.getRangeAt(0).startContainer).toBe(output)
      expect(held.getRangeAt(0).endContainer).toBe(output)
      expect(held.isCollapsed).toBe(false)

      // The event the blur provoked, arriving a frame late — the one that
      // used to find an empty selection and hide the offer.
      document.dispatchEvent(new Event('selectionchange'))
      restoreSelectionLikeCodeMirror(view)

      expect(document.querySelector<HTMLElement>('.mark-affordance')?.style.display).toBe('block')
      const offer = document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!
      offer.click()
      expect((content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks[0]?.itemId).toBe(
        'e-1',
      )
    } finally {
      teardown()
    }
  })

  it('marks and unmarks a restored block from its menu, using the durable entry id', async () => {
    const client = storeWith([entry()], 'output')
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      ed.show()
      switchInputTarget(ed)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-restored="true"]')).not.toBeNull()
      })
      const block = inner.querySelector<HTMLElement>('[data-restored="true"]')!
      expect(block.dataset.entryId).toBe('e-1')

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const mark = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )
      expect(mark?.textContent).toBe('Ask about this block')
      mark?.click()

      const grantState = content as unknown as { grantedBlocks: GrantBlock[] }
      expect(grantState.grantedBlocks[0]?.itemId).toBe('e-1')
      expect(
        ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')?.textContent,
      ).toContain('1')
      const chip = ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')!
      chip.click()
      expect(
        ed.root.querySelector<HTMLElement>('.ui-floating-panel[data-variant="grant"]')?.textContent,
      ).toContain('make test')

      block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.ui-context-menu__item[data-item-id="grant"]',
      )
      expect(unmark?.textContent).toBe('Unmark')
      unmark?.click()
      expect(chip.textContent).toContain('0')
    } finally {
      teardown()
    }
  })

  it('does not mark a restored block when its output is selected', async () => {
    const client = storeWith([entry()], 'output')
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      ed.show()
      switchInputTarget(ed)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-restored="true"]')).not.toBeNull()
      })
      const block = inner.querySelector<HTMLElement>('[data-restored="true"]')!
      const output = block.querySelector<HTMLElement>('.cmd-output')!
      const selection = window.getSelection()!
      const range = document.createRange()
      range.selectNodeContents(output)
      range.getClientRects = () => [new DOMRect(100, 200, 200, 20)] as unknown as DOMRectList
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))

      const grantState = content as unknown as { grantedBlocks: GrantBlock[] }
      expect(grantState.grantedBlocks).toEqual([])
      expect(document.querySelector('.mark-affordance .ui-button')).not.toBeNull()
    } finally {
      teardown()
    }
  })

  // ── a duration nobody recorded is not a duration of zero (nocx-hoeq3) ──
  //
  // Off the wire `durationMs` is nullable, and null is what the ledger sends
  // for a time it never measured. It has to reach the header AS null: the
  // chip is drawn only when there is a duration, and coercing the null to 0
  // on the way in turns "we do not know" into the confident claim that the
  // command took no time at all. Same shape as an evicted body versus a
  // command that printed nothing (ADR-0019 §7) — two facts that must not
  // render alike.
  //
  // Asserted HERE, at the pane's own read, because that is where the
  // coercion lived: a unit fed a fixture with a number in it cannot see a
  // null it never receives.
  it('draws NO duration chip for a time the store never recorded, and 0ms for one it did', async () => {
    const client = storeWith(
      [entry({ id: 'e-2', intent: 'true', durationMs: 0 }), entry({ durationMs: null })],
      '',
    )
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(2)
      })
      const restored = [...inner.querySelectorAll('[data-restored="true"]')] as HTMLElement[]
      const [untimed, instant] = restored
      expect(untimed.querySelector('.cmd-header-text')?.textContent).toBe('make test')
      expect(
        untimed.querySelector(
          ':scope > .cmd-header .cmd-header-right > .ui-meta[data-column="duration"]',
        ),
      ).toBeNull()
      // And the other fact still says itself out loud, so the absence above
      // reads as "unknown" and never as "the chip was dropped".
      // Below a tenth of a second: `<0.1s`, not `0.0s` (spec 2026-09-15
      // §1.7 round 3) — the precise figure the tenths display rounds away
      // is the Meta's title instead.
      expect(
        instant.querySelector(
          ':scope > .cmd-header .cmd-header-right > .ui-meta[data-column="duration"]',
        )?.textContent,
      ).toBe('0ms')
    } finally {
      teardown()
    }
  })

  // ── a restored turn comes back with what it caused (nocx-h1l4o) ───────
  //
  // This is the PRODUCTION path, not the units under it: the pane's own read
  // reaches restoredBody, arrangedByCause and restoredBlock, and the DOM it
  // produces is what a person sees. A unit that never runs here is a feature
  // that does not exist (AGENTS.md, "is the code reachable").
  // RETIRED WITH THE ARRANGEMENT IT ASSERTED (ADR-0040, nocx-dc2fr.2).
  // 'draws a restored turn with the calls it made and the command it ran
  // beside it' stood here and read the turn as FRAGMENTS: two `ask` blocks
  // carrying data-turn-fragment 0 and 1, with the prose cut at the offset the
  // call was anchored at. There is no offset and no cut now — a turn is one
  // block carrying its children in stored order — so the test asserted a
  // mechanism rather than a property.
  //
  // Its PROPERTY survives and is commissioned as nocx-dc2fr.4: the relation
  // puts a command the turn RAN back inside that turn, and leaves a command
  // the person TYPED where the ledger had it — nothing is reordered that the
  // relation does not name. Read the retired test in git history before
  // writing the replacement; it is the shape of the assertion, not the
  // arrangement, that is worth keeping.

  it('falls back to plain ledger order when the relation is not there', async () => {
    // Criterion 4 in the product: no relation, an unreadable one and a
    // dangling one all land here, and the answer is the ledger's own order
    // with the assistant's command drawn as an independent agent block. It
    // is never attached to the turn that happens to sit above it.
    const turn = entry({ id: 'turn-1', seq: 1, kind: 'ask', intent: 'what went wrong?' })
    const typed = entry({ id: 'typed-1', seq: 2, intent: 'git status' })
    const ranByAgent = entry({
      id: 'cmd-1',
      seq: 3,
      kind: 'shell',
      source: 'assistant',
      intent: 'cat -n a.txt',
    })
    const client = makeClient()
    client.call.mockImplementation((method: string, params?: unknown) => {
      if (method === 'ledger.query') {
        const p = params as { paneId?: string }
        return Promise.resolve({
          entries: p.paneId ? [ranByAgent, typed, turn] : [],
          scope: 'everywhere',
          exhausted: true,
          hasRows: true,
          coverage: null,
        })
      }
      // The relation is UNREADABLE: ledger.get fails for every entry, which
      // is also what a store that cannot be reached looks like.
      if (method === 'ledger.get') return Promise.reject(new Error('socket closed'))
      return Promise.reject(new Error('no store wired (fake)'))
    })

    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(3)
      })
      const restored = [...inner.querySelectorAll('[data-restored="true"]')]
      expect(restored.map((el) => el.querySelector('.cmd-header-text')?.textContent)).toEqual([
        'what went wrong?',
        'git status',
        'cat -n a.txt',
      ])
      // Nothing was attached and nothing was invented: no call block
      // anywhere, and no turn carrying a child it was never told about.
      expect(inner.querySelector('.cmd-block[data-block-kind="tool"]')).toBeNull()
      expect(inner.querySelector('.cmd-children > .cmd-block')).toBeNull()
      // And the assistant's command still says the assistant ran it: the
      // badge is painted from the entry's own kind (nocx-4em1z), which the
      // relation's absence does not touch.
      expect(restored[2].querySelector<HTMLElement>('[data-author]')?.dataset.author).toBe('agent')
    } finally {
      teardown()
    }
  })

  it('puts a command the turn RAN inside the turn, and leaves a typed one where the ledger had it (nocx-dc2fr.4)', async () => {
    // The relation's property, at the pane's own read: a command the
    // assistant ran is seated INSIDE its turn at the stored position, and
    // a command the person typed keeps its ledger place outside. Nothing
    // is reordered that the relation does not name.
    const turn = entry({ id: 'turn-1', seq: 1, kind: 'ask', intent: 'what went wrong?' })
    const typed = entry({ id: 'typed-1', seq: 2, intent: 'git status' })
    const ranByAgent = entry({
      id: 'cmd-1',
      seq: 3,
      kind: 'shell',
      source: 'assistant',
      intent: 'cat -n a.txt',
    })
    const client = makeClient()
    client.call.mockImplementation((method: string, params?: unknown) => {
      if (method === 'ledger.query') {
        const p = params as { paneId?: string }
        return Promise.resolve({
          entries: p.paneId ? [ranByAgent, typed, turn] : [],
          scope: 'everywhere',
          exhausted: true,
          hasRows: true,
          coverage: null,
        })
      }
      if (method === 'ledger.get') {
        const id = (params as { id?: string }).id
        // The turn caused the two children: the prose run and the command
        // it ran, in seat order. Its own artifacts are empty, so it reads
        // as an ask with no body of its own (ADR-0040).
        if (id === 'turn-1')
          return Promise.resolve({
            entry: { ...turn },
            edges: [],
            artifacts: [{ id: 'art-t1', mediaType: 'text/plain' }],
            proseEvicted: false,
            caused: [
              {
                entryId: 'txt-1',
                position: 0,
                kind: 'text',
                source: 'assistant',
                intent: '',
                args: null,
                effect: null,
                resource: null,
                opensBlock: false,
              },
              {
                entryId: 'cmd-1',
                position: 1,
                kind: 'shell',
                source: 'assistant',
                intent: 'cat -n a.txt',
                args: null,
                effect: null,
                resource: null,
                opensBlock: false,
              },
            ],
          })
        // A top-level block's entry: no body data and no causes.
        return Promise.resolve({
          artifacts: [],
          edges: [],
          caused: [],
          proseEvicted: false,
        })
      }
      if (method === 'ledger.artifact') {
        return Promise.resolve({ body: 'let me look', truncated: null, byteLen: 11 })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })

    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        // TWO TOP-LEVEL restored blocks: the turn and the typed command.
        // The command the turn ran is NOT one of them — it is inside the
        // turn. The nested children are also marked data-restored, so the
        // count scopes to the scrollback's own children.
        const top = Array.from(inner.children).filter(
          (c) => c instanceof HTMLElement && c.dataset.restored === 'true',
        )
        expect(top.length).toBe(2)
      })
      const turnBlock = inner.querySelector<HTMLElement>(
        '.cmd-block[data-block-kind="ask"][data-restored="true"]',
      )
      expect(turnBlock).not.toBeNull()
      // The command the assistant ran is INSIDE the turn, in the seat after
      // the prose.
      const children = Array.from(
        turnBlock!.querySelectorAll<HTMLElement>(':scope > .cmd-children > .cmd-block'),
      )
      expect(children.map((c) => c.dataset.blockKind)).toEqual(['text', 'command'])
      // The typed command is a SEPARATE TOP-LEVEL block at its ledger
      // place — not a child of the turn (the nested command above is the
      // first `.cmd-block` match, but it is inside `.cmd-children`).
      const topLevel = Array.from(inner.children).filter(
        (c): c is HTMLElement => c instanceof HTMLElement && c.dataset.restored === 'true',
      )
      const typedBlock = topLevel.find(
        (el) =>
          el.dataset.blockKind === 'command' &&
          el.querySelector('.cmd-header-text')?.textContent === 'git status',
      )
      expect(typedBlock).toBeDefined()
      expect(typedBlock?.querySelector('.cmd-header-text')?.textContent).toBe('git status')
    } finally {
      teardown()
    }
  })

  it('copying a restored answer yields the answer, never the provider request (nocx-3dteo)', async () => {
    // THE WIRING, NOT A STUB OF IT. The block manager is handed a reader by
    // terminal-content, and every frontend test of the copy path so far
    // injected its own — so the one line that decides WHICH reader was
    // never asserted, and it pointed at a lookup by media type.
    //
    // What that lookup found on a turn is the provider wiretap: the raw
    // chat-completions request and response, hung on the ask entry as
    // text/plain by internal/app/assistant_wire_capture.go. So "Copy
    // output" on an answer put the system prompt and every tool schema on
    // the person's clipboard, live and restored alike.
    const WIRE =
      '{"model":"gpt-5","messages":[{"role":"system","content":"You are the assistant"}],' +
      '"tools":[{"type":"function","function":{"name":"files.edit"}}]}'
    const PROSE = 'Nothing went wrong — the tree is clean.'

    const turn = entry({ id: 'turn-1', seq: 1, kind: 'ask', intent: 'what went wrong?' })
    const client = makeClient()
    client.call.mockImplementation((method: string, params?: unknown) => {
      if (method === 'ledger.query') {
        const p = params as { paneId?: string }
        return Promise.resolve({
          entries: p.paneId ? [turn] : [],
          scope: 'everywhere',
          exhausted: true,
          hasRows: true,
          coverage: null,
        })
      }
      if (method === 'ledger.get') {
        const id = (params as { id?: string }).id
        if (id === 'turn-1')
          return Promise.resolve({
            entry: { ...turn },
            edges: [],
            // The turn's OWN artifacts are the wiretap captures and nothing
            // else — which is the real shape of an ask entry.
            artifacts: [
              { id: 'art-wire-req', mediaType: 'text/plain', captureMethod: 'raw-output' },
              { id: 'art-wire-res', mediaType: 'text/plain', captureMethod: 'raw-output' },
            ],
            proseEvicted: false,
            caused: [
              {
                entryId: 'txt-1',
                position: 0,
                kind: 'text',
                source: 'assistant',
                intent: '',
                args: null,
                effect: null,
                resource: null,
                opensBlock: false,
              },
            ],
          })
        return Promise.resolve({
          entry: { kind: 'text' },
          edges: [],
          artifacts: [{ id: 'art-prose', mediaType: 'text/plain', captureMethod: 'none' }],
          caused: [],
          proseEvicted: false,
        })
      }
      if (method === 'ledger.artifact') {
        const id = (params as { id?: string }).id
        const body = id === 'art-prose' ? PROSE : WIRE
        return Promise.resolve({ body, truncated: null, byteLen: body.length })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })

    const copied: string[] = []
    const realClipboard = navigator.clipboard
    Object.defineProperty(navigator, 'clipboard', {
      value: {
        writeText: (text: string) => {
          copied.push(text)
          return Promise.resolve()
        },
      },
      configurable: true,
    })

    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      let turnBlock: HTMLElement | null = null
      await vi.waitFor(() => {
        turnBlock = inner.querySelector<HTMLElement>(
          '.cmd-block[data-block-kind="ask"][data-restored="true"]',
        )
        expect(turnBlock).not.toBeNull()
      })
      // The turn draws NO body of its own — the wiretap is not an answer.
      expect(turnBlock!.querySelector(':scope > [data-answer-body]')).toBeNull()

      turnBlock!.querySelector<HTMLElement>('[data-block-actions]')!.click()
      const copyOut = Array.from(
        document.querySelectorAll<HTMLElement>(
          '[data-testid="block-actions-menu"] .ui-context-menu__item',
        ),
      ).find((el) => el.textContent === 'Copy output')
      expect(copyOut).toBeDefined()
      copyOut!.click()

      await vi.waitFor(() => {
        expect(copied).toEqual([PROSE])
      })
      expect(copied[0]).not.toContain('tools')
      expect(copied[0]).not.toContain('You are the assistant')
    } finally {
      Object.defineProperty(navigator, 'clipboard', {
        value: realClipboard,
        configurable: true,
      })
      teardown()
    }
  })

  it('survives being shown before it is mounted, which is what really happens', async () => {
    // The activation seam calls setVisible(true) while the pane is still
    // being built, so the first show finds no scrollback. Spending the
    // one-shot there costs the pane its whole past — and no unit test would
    // have seen it, because a test shows a pane that is already mounted.
    const client = storeWith([entry()], 'output')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const withScrollback = content as unknown as { scrollback: ScrollbackController | null }
      const real = withScrollback.scrollback
      withScrollback.scrollback = null
      content.setVisible(true)
      await Promise.resolve()
      withScrollback.scrollback = real

      content.setVisible(false)
      content.setVisible(true)
      await vi.waitFor(() => {
        expect(real!.scrollbackInner.querySelectorAll('[data-restored="true"]').length).toBe(1)
      })
    } finally {
      teardown()
    }
  })

  it('draws its past when the show arrived before the mount, with no second show', async () => {
    // THE ORDER THE PRODUCT ACTUALLY RUNS IN, and the one every test above
    // gets backwards. On a restored pane the log reads: Pane.start(),
    // PaneManager.activate() -> setVisible(true), and only THEN "creating
    // renderer". So the one show a restored pane ever gets lands while there
    // is no scrollback to draw into, and nothing runs the read again — the
    // tab is active and stays active, so no second setVisible(true) arrives,
    // and a page load is not a reconnect either.
    //
    // The e2e proved neither end was empty: the store held both blocks,
    // anchored to the right panes, and ledger.query with the restored pane id
    // answered with them in the second session. The read was simply never
    // issued. Nothing appeared in the log because "not yet" is silent, which
    // is correct — the defect was that "not yet" had no "later".
    const client = storeWith([entry()], 'output')
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { visibleBeforeMount: true },
      client,
    )
    try {
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(1)
      })
      expect(inner.querySelector('[data-restore-boundary="true"]')).not.toBeNull()
    } finally {
      teardown()
    }
  })

  it('does not read the past of a pane nobody has looked at yet', async () => {
    // The other half of the interval, and the reason the mount cannot simply
    // read unconditionally: eight panes at fifty blocks is four hundred
    // blocks of DOM before the first frame. A pane that mounts without being
    // shown asks nothing, and asks the moment it IS shown.
    const client = storeWith([entry()], 'output')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await Promise.resolve()
      expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(0)
      content.setVisible(true)
      await vi.waitFor(() => {
        expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(1)
      })
    } finally {
      teardown()
    }
  })

  it('tries again when the socket comes back, instead of showing an empty past forever', async () => {
    // The defect the e2e found, and the one no unit test would have: the
    // restart drops the socket, the pane is shown while it is still coming
    // back, ledger.query is refused — and a one-shot spent on that failure
    // leaves the pane with no past for the rest of the session. A reconnect
    // is ordinary (AD-9), so "could not ask" must not be recorded as
    // "there was nothing".
    const client = storeWith([entry()], 'output')
    const live = client.call.getMockImplementation()
    if (!live) throw new Error('storeWith must install the initial call handler')
    client.call.mockImplementation((method: string, params?: unknown) => {
      if (method === 'ledger.query') return Promise.reject(new Error('socket is reconnecting'))
      return live(method, params)
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      await Promise.resolve()
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(0)

      // The socket returns and the report fires. The past appears without
      // the person doing anything.
      expect(client.onReconnectResult).toHaveBeenCalled()
      client.call.mockImplementation(live)
      client._fireReconnect()
      await vi.waitFor(() => {
        expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(1)
      })
    } finally {
      teardown()
    }
  })

  it('says the output is gone when the artifact is, instead of drawing an empty block', async () => {
    const client = storeWith([entry()], null)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      await vi.waitFor(() => {
        expect(inner.querySelector('[data-output-evicted="true"]')).not.toBeNull()
      })
      expect(inner.textContent).toContain('Output is no longer kept')
    } finally {
      teardown()
    }
  })

  it('costs the past and never the pane when the store cannot be read', async () => {
    const client = makeClient()
    client.call.mockRejectedValue(new Error('store down'))
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      await Promise.resolve()
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      expect(inner.querySelectorAll('[data-restored="true"]').length).toBe(0)
      // The pane is alive and usable, which is the property that matters.
      expect(content.shellState).toBeDefined()
    } finally {
      teardown()
    }
  })
})

// ───────────────────────────────────────────────────────────────────────────
// The model chip in the composer (nocx-rikz5). The composer names the model
// that will answer, and it is the way to change it. The chain here is the
// real one: real registry, real editor, real AgentReadiness over the fake
// dispatcher — because the defect this task exists to prevent is a chip
// that is CORRECT ONCE and then stops moving.
//
// The height claim (the chrome row does not grow a second line when a
// 40-character model id arrives) is deliberately NOT here: jsdom computes
// no layout, so getBoundingClientRect returns zeroes and the assertion
// would pass vacuously. It belongs to the Playwright spec.
describe('the model chip in the composer (nocx-rikz5)', () => {
  const READY_ANSWERING: AgentStatusResult['answering'] = {
    ready: true,
    reason: null,
    endpoint: 'openrouter',
    model: 'm-a',
  }
  const UNASSIGNED_ANSWERING: AgentStatusResult['answering'] = {
    ready: false,
    reason: 'unassigned',
    endpoint: null,
    model: null,
  }
  const NO_ENDPOINTS_ANSWERING: AgentStatusResult['answering'] = {
    ready: false,
    reason: 'no-endpoints',
    endpoint: null,
    model: null,
  }

  /** A client whose agent.status answer is whatever `answering` currently
   *  holds — so a test can change the FACTS and then make the surface ask
   *  again, exactly as adding an endpoint does. */
  function statusClient(initial: AgentStatusResult['answering']) {
    const client = makeClient()
    const state = { answering: initial, asks: 0 }
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.status') {
        state.asks += 1
        return Promise.resolve({
          endpointConfigured: state.answering.ready,
          credential: state.answering.ready ? 'resolvable' : null,
          lastProbe: null,
          answering: state.answering,
        })
      }
      return Promise.resolve({
        id: 'att-0',
        domain: 'd1',
        state: 'open',
        command: '',
        cwd: '',
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
    })
    return { client, state }
  }

  const chipEls = (content: TerminalContent): HTMLElement[] =>
    Array.from(
      editorOf(content).root.querySelectorAll<HTMLElement>('[data-control^="model"]'),
    ).filter((el) => el.style.display !== 'none')

  const chipsOf = (content: TerminalContent): string[] =>
    chipEls(content).map((el) => el.textContent ?? '')

  const clickChip = (content: TerminalContent, text: string): void => {
    const el = chipEls(content).find((c) => c.textContent === text)
    if (!el) throw new Error(`no model chip reading ${JSON.stringify(text)}`)
    el.click()
  }

  /** The person's own explicit switch, pressed on the indicator — the
   *  twin of ⌘Enter and the one these tests reach for, because what they
   *  are about is the CHIP. The chord's own owner is a pane-level capture
   *  listener that an off-screen, detached fixture cannot reach, and it has
   *  its own specs (nocx-a7mw7.6).
   *
   *  nocx-9bpeq.15 turned the click into a menu open (ui/mode-indicator.ts:
   *  a kit ContextMenu, role="menu"/"menuitem", the active row wearing the
   *  check icon — see its own test for the same shape). A bare mousedown no
   *  longer toggles anything, so this opens the menu and picks whichever
   *  row is NOT the active one — the toggle these tests rely on, since Run
   *  and Ask are the only two registered targets. */
  const switchToAsk = (content: TerminalContent): void => {
    const el = editorOf(content).root.querySelector<HTMLButtonElement>('.ui-mode-indicator')
    if (!el) throw new Error('no mode indicator to switch with')
    el.click()
    const items = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    const inactive = items.find((i) => i.querySelector('.ui-context-menu__icon svg') === null)
    if (!inactive) throw new Error('no inactive mode-indicator row to pick')
    inactive.click()
  }

  it('shows no model chip while Enter goes to the shell', async () => {
    const { client } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      expect(chipsOf(content)).toEqual([])
    } finally {
      teardown()
    }
  })

  it('names the model that will answer once the target is the assistant', async () => {
    const { client } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
    } finally {
      teardown()
    }
  })

  it('takes the chip away again when the person switches back to Run', async () => {
    const { client } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      switchToAsk(content) // the switch is a toggle
      expect(chipsOf(content)).toEqual([])
    } finally {
      teardown()
    }
  })
  it('shows the grant chip only while the target is Ask', async () => {
    const { client } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const grant = editorOf(content).root.querySelector<HTMLElement>('[data-control="grant"]')!
      expect(grant.style.display).toBe('none')

      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      expect(grant.style.display).toBe('')

      switchToAsk(content)
      expect(grant.style.display).toBe('none')
    } finally {
      teardown()
    }
  })

  it('keeps the visible Ask chips ordered with the grant last', async () => {
    const { client } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      const left = editorOf(content).root.querySelector<HTMLElement>('.nocx-editor-controls')!
      const children = [...left.children]
      const grant = left.querySelector<HTMLElement>('[data-control="grant"]')!
      const visibleModels = chipEls(content)
      expect(visibleModels).toHaveLength(2)
      expect(visibleModels.every((chip) => children.indexOf(chip) < children.indexOf(grant))).toBe(
        true,
      )
      const chips = children.filter((child) => child.hasAttribute('data-control'))
      expect(chips[chips.length - 1]).toBe(grant)
    } finally {
      teardown()
    }
  })

  it('offers the rung, not the model, when nothing is chosen', async () => {
    const { client } = statusClient(UNASSIGNED_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Choose a model']))
    } finally {
      teardown()
    }
  })

  it('opens the page the rung names', async () => {
    const opened: string[] = []
    const { client } = statusClient(NO_ENDPOINTS_ANSWERING)
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {
        hooks: {
          onCreateEndpoint: () => opened.push('endpoints'),
          onOpenRoles: () => opened.push('roles'),
        },
      },
      client,
    )
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Add an endpoint first']))
      clickChip(content, 'Add an endpoint first')
      expect(opened).toEqual(['endpoints'])
    } finally {
      teardown()
    }
  })

  it('opens Endpoints from the provider and Roles from the model', async () => {
    const opened: string[] = []
    const { client } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {
        hooks: {
          onCreateEndpoint: () => opened.push('endpoints'),
          onOpenRoles: () => opened.push('roles'),
        },
      },
      client,
    )
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      clickChip(content, 'openrouter')
      clickChip(content, 'm-a')
      expect(opened).toEqual(['endpoints', 'roles'])
    } finally {
      teardown()
    }
  })

  it('repaints when the facts change, not only on mount', async () => {
    // Without this the end-to-end path cannot pass: the chip would still
    // read "Add an endpoint first" after an endpoint had been added. The
    // pane coming back to the front is the moment the facts can have
    // changed — the Endpoints form lives in another pane.
    const { client, state } = statusClient(NO_ENDPOINTS_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Add an endpoint first']))

      // An endpoint was added and a model chosen on the Settings pane;
      // the person comes back to the composer.
      state.answering = READY_ANSWERING
      content.setVisible(true)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
    } finally {
      teardown()
    }
  })

  it('asks again when the socket comes back', async () => {
    const { client, state } = statusClient(UNASSIGNED_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Choose a model']))
      state.answering = READY_ANSWERING
      client._fireReconnect()
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
    } finally {
      teardown()
    }
  })

  it('catches up when a configuration write lands after the pane is back in front', async () => {
    // The show was the ONLY seam (nocx-1ebrs.66.1.28), and a write still in
    // flight when the person came back answered the read made there: the
    // chip then named the model from before the write and no later read was
    // ever issued. The write announces its own landing now, and this is that
    // announcement, made while the chip is on screen and nobody is
    // navigating anywhere.
    const { client, state } = statusClient(UNASSIGNED_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['Choose a model']))
      const before = state.asks

      state.answering = READY_ANSWERING
      notifyAiConfigChanged()

      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      expect(state.asks).toBe(before + 1)
    } finally {
      teardown()
    }
  })

  it('pays no readiness call for a configuration write while Enter goes to the shell', async () => {
    // The same rule the show follows: a Run pane has no chip, so nothing can
    // have gone stale in front of anybody, and it asks nobody anything.
    const { client, state } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      notifyAiConfigChanged()
      await Promise.resolve()
      await Promise.resolve()
      expect(state.asks).toBe(0)
    } finally {
      teardown()
    }
  })

  it('pays no readiness call for a configuration write while the pane is behind', async () => {
    // Settings is in front while the write is made. This pane is hidden, and
    // its show will read the facts when the person comes back.
    const { client, state } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content)
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      content.setVisible(false)
      const before = state.asks
      notifyAiConfigChanged()
      await Promise.resolve()
      await Promise.resolve()
      expect(state.asks).toBe(before)
    } finally {
      teardown()
    }
  })

  it('stops listening for configuration writes once the pane is gone', async () => {
    const { client, state } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    content.setVisible(true)
    switchToAsk(content)
    await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
    const before = state.asks
    teardown()
    notifyAiConfigChanged()
    await Promise.resolve()
    await Promise.resolve()
    expect(state.asks).toBe(before)
  })

  it('does not ask while Enter goes to the shell — a Run pane pays no readiness call', async () => {
    const { client, state } = statusClient(READY_ANSWERING)
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      client._fireReconnect()
      await Promise.resolve()
      expect(state.asks).toBe(0)
    } finally {
      teardown()
    }
  })

  it('discards a late reply that would repaint an older state', async () => {
    // Two refreshes racing is the ORDINARY case here: adding an endpoint
    // and immediately choosing a model is one gesture apart.
    const client = makeClient()
    const pending: Array<(v: unknown) => void> = []
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.status') {
        return new Promise((resolve) => pending.push(resolve))
      }
      return Promise.resolve({
        id: 'att-0',
        domain: 'd1',
        state: 'open',
        command: '',
        cwd: '',
        host: '',
        origin: 'app',
        submitId: submitToken(client),
        startedAt: '2026-08-08T12:00:00Z',
      })
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      switchToAsk(content) // ask #0 — the OLD facts
      content.setVisible(true) // ask #1 — the NEW facts
      await vi.waitFor(() => expect(pending.length).toBe(2))

      const body = (answering: AgentStatusResult['answering']) => ({
        endpointConfigured: answering.ready,
        credential: answering.ready ? 'resolvable' : null,
        lastProbe: null,
        answering,
      })
      pending[1](body(READY_ANSWERING)) // the newer ask answers FIRST
      await vi.waitFor(() => expect(chipsOf(content)).toEqual(['openrouter', 'm-a']))
      pending[0](body(NO_ENDPOINTS_ANSWERING)) // the older ask answers LAST
      await Promise.resolve()
      await Promise.resolve()
      expect(chipsOf(content)).toEqual(['openrouter', 'm-a'])
    } finally {
      teardown()
    }
  })
})

/**
 * A COMMAND IS RUNNING, AND THE PERSON AT IT IS NOT POWERLESS (nocx-92gfl,
 * nocx-23rph).
 *
 * Two gestures, one situation. While a command runs the editor is hidden —
 * that is deliberate and stays (an inline TUI on the normal buffer needs
 * both its ROWS and its KEYS, and nocx cannot tell `top` from `du` without
 * sniffing the stream, which AD-6 forbids). What was missing was any way
 * back in: nowhere to type a question about the command, and no way to stop
 * it once the keyboard had gone anywhere else.
 *
 * Every test below drives the seam a person actually reaches — a keystroke
 * on the pane, an item in the ⋮ menu — and reads the answer off the product:
 * the grid's writability, a byte arriving at the session, the target the
 * mode indicator names. None of them reads a private flag as the assertion.
 */
describe('asking about, and stopping, a running command (nocx-92gfl, nocx-23rph)', () => {
  /** jsdom implements neither; the block model calls both at submit. */
  function stubScrolling(): () => void {
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    return () => {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
    }
  }

  /** The pane element TerminalContent mounted into — where the pane-level
   *  key handlers live, and an ancestor of the live grid. */
  const paneOf = (content: TerminalContent): HTMLElement =>
    (content as unknown as { _paneTarget: HTMLElement })._paneTarget

  /** The live grid container: where the focus is while a command runs and
   *  the person has not clicked away. */
  const gridOf = (content: TerminalContent): HTMLElement =>
    (content as unknown as { scrollback: ScrollbackController }).scrollback.terminalLiveContainer

  /** The grid's writability seam. Reached through a cast because the typed
   *  interface says setReadOnly(boolean) while the mock is a spy — the same
   *  trade the ownership tests above make. */
  const editorOwnsKeyboard = (content: TerminalContent): boolean =>
    editorOf(content).root.contains(document.activeElement)

  /** The registry's active target id — read through the product's own
   *  account of it, the mode indicator's data-target, wherever the editor
   *  is on screen to carry one. */
  function targetNamed(ed: CommandEditor): string | null {
    const el = ed.root.querySelector<HTMLElement>('.ui-mode-indicator')
    return el?.dataset.target ?? null
  }

  function defaultPinnedFrame(): CapturedFrame {
    const text = 'pinned frame'
    return {
      rows: [
        {
          kind: 'cells',
          cells: Array.from({ length: 80 }, (_, index) => ({
            char: text[index] ?? ' ',
            attrs: emptyAttrs(),
          })),
        },
      ],
      cursor: { line: 0, col: text.length },
      provenance: {
        source: 'live',
        identity: {
          buffer: { kind: 'normal' },
          cols: 80,
          rows: 24,
          generation: 0,
        },
        range: { start: 0, end: 1 },
        scrollbackCapLines: 10000,
      },
    }
  }

  /** ⌘/Ctrl+Enter, dispatched where a person's keystroke lands while the
   *  grid owns input: on the live grid, so it travels the same path. */
  async function summonChord(content: TerminalContent): Promise<void> {
    if (!Object.prototype.hasOwnProperty.call(content, 'captureLiveFrame')) {
      content.captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
    }
    gridOf(content).dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Enter',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    )
    await Promise.resolve()
  }

  function escapeOn(el: HTMLElement): void {
    el.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
  }

  /** A prompt, then one running command. The two facts a person's pane goes
   *  through before any of this matters. */
  function startCommand(client: ClientFake, command = 'sleep 300'): (p: unknown) => void {
    const handler = lifecycleHandler(client)
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: {
        id: 'att-run',
        state: 'open',
        origin: 'app',
        submitId: submitToken(client),
        command,
      },
    })
    return handler
  }

  /** The completion of that command, which is what brings the prompt back. */
  function finishCommand(handler: (p: unknown) => void): void {
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: {
        id: 'att-run',
        state: 'completed',
        exitCode: 0,
        fence: 'c'.repeat(64),
        completedAt: '2026-08-23T12:00:00Z',
      },
    })
    // The SAME domain and the SAME epoch: epochs name an incarnation of the
    // integration, not a command, and the kernel refuses a fact whose epoch
    // does not match the domain it already holds (resolveDomain).
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
  }

  async function waitForSessionQueue(content: TerminalContent): Promise<void> {
    const queue = content as unknown as {
      _sessionCallQueue: unknown[]
      _sessionCallSending: boolean
    }
    await vi.waitFor(() => {
      expect(queue._sessionCallQueue).toHaveLength(0)
      expect(queue._sessionCallSending).toBe(false)
    })
  }

  it('uses the accepted backend cursor row for command block coordinates', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const rows = Array.from({ length: 24 }, () => [] as CellSpec[])
      const screen = (revision: number, baseRow: number, cursorY: number): SessionFrame =>
        frameOf(
          revision,
          rows,
          { x: 0, y: cursorY, visible: true },
          { cols: 80, rows: 24 },
          {
            bufferKind: 'normal',
            bufferInstance: '9',
            baseRow,
            bracketedPaste: false,
            mouseTracking: false,
            pointerShape: null,
            title: null,
            cwd: null,
          },
        )
      const session = sessionOf(content)
      session.fireScreenFrame(screen(1, 100, 3))

      const lifecycle = startCommand(client)
      const manager = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .blockManager
      expect(manager.runningBlock?.startLine).toBe(103)

      session.fireScreenFrame(screen(2, 200, 5))
      finishCommand(lifecycle)
      // Completion and the backend's block.closed may cross on the control
      // plane. The accepted screen at block.closed owns the final row.
      session.fireScreenFrame(screen(3, 300, 7))
      const closeSubscriber = client.dispatcher.subscribe.mock.calls.find(
        (call: unknown[]) => call[0] === 'block.closed',
      ) as [string, (params: unknown) => void] | undefined
      if (!closeSubscriber) throw new Error('block.closed subscription was not installed')
      closeSubscriber[1]({
        entryId: 'att-run',
        kept: false,
        screenAnchor: {
          row: 307,
          screenRevision: 3,
          bufferKind: 'normal',
          bufferInstance: '9',
          cols: 80,
          rows: 24,
        },
      })

      expect(manager.blocks[0]?.endLine).toBe(307)
    } finally {
      teardown()
    }
  })

  it('Ctrl+Enter while a command is running summons the editor as an overlay without resizing the grid', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      const session = sessionOf(content)
      const accepted = screenFrame(1, ['running command'])
      session.fireScreenFrame(accepted)
      const dimensions = { cols: accepted.geometry.cols, rows: accepted.geometry.rows }
      const resizeCalls = session.sendResize.mock.calls.length
      session.send.mockClear()
      // The editor is gone while it runs — that is deliberate and it
      // is the state this gesture exists for.
      expect(ed.isVisible).toBe(false)
      capturedActionFacts.length = 0

      await summonChord(content)

      const summonedFacts = capturedActionFacts[capturedActionFacts.length - 1]
      expect(summonedFacts).toEqual(expect.objectContaining({ presentation: 'editor' }))
      const recovery = ed.root.querySelector<HTMLElement>('[data-control="recovery"]')
      expect(recovery?.style.display).toBe('none')

      expect(ed.isVisible).toBe(true)
      expect(ed.root.dataset.placement).toBe('overlay')
      // The gesture is what attaches, not the kind of program: the person
      // pressed the chord over THIS command, the product froze THIS screen
      // and says so on the badge below. An ordinary running command is no
      // exception — a summon that pins a photograph and then sends nothing
      // is where the owner's zero-count report came from (nocx-hp8p2.4).
      expect(ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label')).toContain(
        'frozen screen attached automatically',
      )
      // Ask, not the shell: the summoned editor's only target. Read off the
      // indicator, which is the product's own account of where Enter goes.
      expect(targetNamed(ed)).toBe('agent')
      const frozen = ed.root.querySelector<HTMLElement>('[role="status"]')
      expect(frozen?.textContent).toBe('Screen frozen while you ask')
      expect(content.pinnedFrame()?.provenance.source).toBe('live')
      expect(gridOf(content).parentElement?.parentElement?.textContent).toContain('pinned frame')
      // The grid remains the same size and no PTY resize was requested. The
      // overlay is removed from layout rather than making a second viewport
      // fit pass.
      expect(paneScreen()?.geometry).toMatchObject(dimensions)
      expect(session.send).not.toHaveBeenCalled()
      expect(session.sendResize).toHaveBeenCalledTimes(resizeCalls)
      // The editor now owns keyboard focus; the terminal input is no longer
      // the active keyboard surface.
      expect(editorOwnsKeyboard(content)).toBe(true)
    } finally {
      restore()
      teardown()
    }
  })
  it('attaches the still-running owner when its block froze before summon capture', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'top')
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      scrollback.beginBlock('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      closeRunningBlock(scrollback.blockManager)
      fireBufferState(sessionOf(content), 'alternate', 20)

      await summonChord(content)

      expect(ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label')).toContain(
        'frozen screen attached automatically',
      )
    } finally {
      restore()
      teardown()
    }
  })

  it('attaches the live alternate screen of a program that has never frozen (nocx-hp8p2.4)', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'top')
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      scrollback.beginBlock('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      fireBufferState(sessionOf(content), 'alternate', 20)

      await summonChord(content)

      // The program is still painting its screen — it has never frozen, so the
      // attachment owner is the running block itself. Without this the
      // assistant is asked about a screen nothing handed it.
      expect(ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label')).toContain(
        'frozen screen attached automatically',
      )
    } finally {
      restore()
      teardown()
    }
  })

  it('attaches the screen of a full-viewport program that never left the normal buffer (nocx-hp8p2.4)', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'top')
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      scrollback.beginBlock('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      sessionOf(content).fireScreenFrame(screenFrame(1, ['top output']))

      await summonChord(content)

      // procps `top` owns the whole viewport without taking the alternate
      // screen, and it is the program the owner reported this against. The
      // gesture, not the buffer kind, is what says which screen the question
      // carries.
      expect(ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label')).toContain(
        'frozen screen attached automatically',
      )
    } finally {
      restore()
      teardown()
    }
  })

  it('gives the person the seam between the frozen screen and the assistant (nocx-hp8p2.6)', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'top')
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      scrollback.beginBlock('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      sessionOf(content).fireScreenFrame(screenFrame(1, ['top output']))

      await summonChord(content)

      // The kit's separator, on the one axis this edge has: the frozen
      // screen is above and the assistant below, so what a person drags is
      // how much of the pane each keeps.
      //
      // And it has to be VISIBLE. The kit's grab line is transparent at
      // rest — "the pane border beside it is the visible seam" — so the
      // surface owes it that border, or the edge is a six-pixel invisible
      // strip nobody can find.
      const stackCss = stripComments(
        extractRuleBlock(readFileSync(STYLE_ENTRY, 'utf8'), 'nocx-summon-stack') ?? '',
      )
      expect(stackCss).toMatch(/border-top\s*:\s*1px\s+solid/)
      const stack = document.querySelector<HTMLElement>('.nocx-summon-stack')
      expect(stack).not.toBeNull()
      const seam = stack!.querySelector<HTMLElement>('[role="separator"]')
      expect(seam).not.toBeNull()
      expect(seam?.getAttribute('aria-orientation')).toBe('horizontal')
      // It is above the answers it measures, which is where the edge is.
      const answers = stack!.querySelector<HTMLElement>('.nocx-summon-answers')
      expect(answers).not.toBeNull()
      expect(
        seam!.compareDocumentPosition(answers!) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy()

      // Dragging the seam up gives the assistant more room. Read off the
      // element's own height, which is what a person sees change.
      const before = Number.parseFloat(answers!.style.height || '0')
      seam!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
      const after = Number.parseFloat(answers!.style.height || '0')
      expect(after).toBeGreaterThan(before)
      // And the cap survives the drag: the program the question is about
      // may never be pushed off the pane entirely, which is the whole point
      // of asking WITHOUT leaving it. The ceiling therefore stops short of
      // the space the stack could otherwise fill.
      const ceiling = Number(seam!.getAttribute('aria-valuemax'))
      const floor = Number(seam!.getAttribute('aria-valuemin'))
      expect(ceiling).toBeGreaterThan(floor)
      expect(ceiling).toBeLessThan(window.innerHeight)

      expect(ed.isVisible).toBe(true)
    } finally {
      restore()
      teardown()
    }
  })

  it('gives the summon a pointer way out beside its badge (nocx-hp8p2.8)', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)

      await summonChord(content)

      // Escape was the only exit, and a surface whose only way out is a key
      // a person has to already know is one they can be stuck in.
      const dismiss = ed.root.querySelector<HTMLButtonElement>(
        '.nocx-freeze-dismiss .ui-icon-button',
      )
      expect(dismiss).not.toBeNull()
      expect(dismiss?.getAttribute('aria-label')).toContain('Close the assistant')

      dismiss!.click()

      // The same unwind the key performs — one exit, two ways to reach it.
      expect(content.pinnedFrame()).toBeNull()
      expect(ed.root.querySelector('[role="status"]')).toBeNull()
    } finally {
      restore()
      teardown()
    }
  })

  it('disposal removes the freeze presentation and restores the live host', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      const frameHost = gridOf(content)
      frameHost.style.position = 'sticky'

      await summonChord(content)

      const frame = frameHost.querySelector('.nocx-freeze-frame')
      const marker = ed.root.querySelector('[role="status"]')
      expect(ed.root.dataset.placement).toBe('overlay')
      expect(frameHost.style.position).toBe('relative')
      expect(frame).not.toBeNull()
      expect(marker).not.toBeNull()

      content.dispose()

      expect(ed.root.dataset.placement).toBe('inline')
      expect(content.pinnedFrame()).toBeNull()
      expect(frame?.isConnected).toBe(false)
      expect(marker?.isConnected).toBe(false)
      expect(frameHost.style.position).toBe('sticky')
      expect(paneOf(content).querySelector('.nocx-summon-stack')).toBeNull()
    } finally {
      restore()
      teardown()
    }
  })

  it('waits for the pinned frame before showing the editor or marker', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    let release!: (frame: CapturedFrame) => void
    try {
      content.setVisible(true)
      startCommand(client)
      content.captureLiveFrame = vi.fn(
        () =>
          new Promise<CapturedFrame>((resolve) => {
            release = resolve
          }),
      )

      await summonChord(content)
      expect(ed.isVisible).toBe(false)
      expect(content.pinnedFrame()).toBeNull()
      expect(ed.root.querySelector('[role="status"]')).toBeNull()

      release(defaultPinnedFrame())
      await vi.waitFor(() => expect(ed.isVisible).toBe(true))
      expect(content.pinnedFrame()).not.toBeNull()
    } finally {
      restore()
      teardown()
    }
  })

  it('refused capture leaves the live pane untouched', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      content.captureLiveFrame = vi.fn().mockRejectedValue(new Error('capture refused'))

      await summonChord(content)
      expect(ed.isVisible).toBe(false)
      expect(ed.root.dataset.placement).toBe('inline')
      expect(content.pinnedFrame()).toBeNull()
      expect(ed.root.querySelector('[role="status"]')).toBeNull()
      expect(
        gridOf(content).parentElement?.parentElement?.querySelector('.nocx-freeze-frame'),
      ).toBeNull()
    } finally {
      restore()
      teardown()
    }
  })

  it('Escape dismisses it and the keys reach the process again', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'top')
      await summonChord(content)
      expect(ed.isVisible).toBe(true)
      const session = sessionOf(content)
      const focus = vi.spyOn(intentElementOf(content), 'focus')
      focus.mockClear()
      session.fireScreenFrame(screenFrame(1, ['during freeze']))
      await vi.waitFor(() => expect(gridOf(content).textContent).toContain('during freeze'))

      escapeOn(viewOf(ed).contentDOM)

      expect(ed.isVisible).toBe(false)
      expect(ed.root.dataset.placement).toBe('inline')
      expect(editorOwnsKeyboard(content)).toBe(false)
      expect(focus).toHaveBeenCalledTimes(1)
      // The assertion that matters is not the flag: it is a key the PROGRAM
      expect(content.pinnedFrame()).toBeNull()
      expect(gridOf(content).textContent).toContain('during freeze')
      expect(
        gridOf(content).parentElement?.parentElement?.querySelector('.nocx-freeze-frame'),
      ).toBeNull()
      expect(ed.root.querySelector('[role="status"]')).toBeNull()
      // consumes arriving at the session. `q` is what quits `top`, and if
      // the editor still owned input it would be a letter in a draft.
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'q')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'q'))
      expect(session.send).not.toHaveBeenCalled()
      // And the summon is UNWOUND, not merely hidden: the target it
      // displaced is back, so the prompt that returns when the command ends
      // is the shell's again. Leaving it on Ask would put the person's next
      // Enter in front of the model without them ever choosing that.
      expect(targetNamed(ed)).toBe('shell')
    } finally {
      restore()
      teardown()
    }
  })
  it('Escape dismisses an accepted summon when the terminal input owns focus', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summonChord(content)
      const editor = editorOf(content)
      editor.insertText('why is this still running?')
      viewOf(editor).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() =>
        expect(client.dispatcher.call.mock.calls.some(([method]) => method === 'agent.ask')).toBe(
          true,
        ),
      )

      const textarea = document.createElement('textarea')
      gridOf(content).append(textarea)
      textarea.focus()
      expect(document.activeElement).toBe(textarea)

      const event = new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      })
      textarea.dispatchEvent(event)

      expect(event.defaultPrevented).toBe(true)
      expect(content.pinnedFrame()).toBeNull()
      expect(ed.isVisible).toBe(false)
    } finally {
      teardown()
    }
  })

  it('a half-typed question survives the dismissal, and is still there on the next summon', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      await summonChord(content)
      ed.insertText('why is this slow')

      escapeOn(viewOf(ed).contentDOM)
      expect(ed.isVisible).toBe(false)

      await summonChord(content)
      // Escape here means "put this away", not "throw it away": the person
      // dismissed a surface, they did not cancel their question.
      expect(ed.getDoc()).toBe('why is this slow')
      // And with words still in the box, the target stayed on Ask through
      // the dismissal: they are a question, and re-pointing them at the
      // shell is the one thing that must never happen to them.
      expect(targetNamed(ed)).toBe('agent')
    } finally {
      restore()
      teardown()
    }
  })

  it('the summoned editor cannot target the shell, so no second command starts over the running one', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      await summonChord(content)
      expect(targetNamed(ed)).toBe('agent')

      // The chord is now the editor's own — the explicit target switch. It
      // must refuse the shell while the shell is busy.
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      expect(targetNamed(ed)).toBe('agent')
      // And it says so rather than doing nothing: a control that ignores a
      // deliberate gesture in silence reads as broken.
      expect(showToast).toHaveBeenCalled()

      // The direct assertion the criterion asks for: Enter on a typed line
      // starts no command. A shell submission would paste the line into the
      // grid and open a running block; a question does neither.
      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()
      ed.insertText('rm -rf /tmp/x')
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(session.intent).not.toHaveBeenCalled()
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      restore()
      teardown()
    }
  })

  it('when the command ends the editor comes back and the target returns to the shell', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      await summonChord(content)
      expect(targetNamed(ed)).toBe('agent')

      finishCommand(handler)
      expect(ed.root.dataset.placement).toBe('inline')

      expect(ed.isVisible).toBe(true)
      expect(targetNamed(ed)).toBe('shell')
    } finally {
      restore()
      teardown()
    }
  })

  it('but a half-typed question is never re-targeted at the shell', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      await summonChord(content)
      ed.insertText('what did that do')

      finishCommand(handler)

      // The prompt is back — and the words in the box are still a question,
      // addressed to the assistant. Re-pointing them at the shell would
      // turn the next Enter into a command the person never wrote.
      expect(ed.isVisible).toBe(true)
      expect(targetNamed(ed)).toBe('agent')
      expect(ed.getDoc()).toBe('what did that do')
    } finally {
      restore()
      teardown()
    }
  })

  // ── ONE OWNER FOR THE TARGET CHORD (nocx-a7mw7.6) ────────────────────
  //
  // ⌘/Ctrl+Enter used to be claimed in three places, each keyed on where
  // the browser had parked the focus: a capture listener on the editor's
  // root (flip the target), a capture listener on terminal input (summon),
  // and a document bubble listener added so a scrollback selection — which
  // blurs onto <body>, neither of the other two — could still reach the
  // flip. Three guard sets for one key, and the third had none of its own
  // tested.
  //
  // Now one capture listener on document decides, per pane, and it reads
  // the STATE rather than the focus: `editor.isVisible` chooses flip or
  // summon, exactly the fact canSummonEditor's first line already tested.
  // These tests press the chord from every place a person's focus can be.
  describe('the target chord has one owner (nocx-a7mw7.6)', () => {
    /** A settled block with output, the way the ask-entry specs build one. */
    function frozenBlock(content: TerminalContent, command: string): HTMLElement {
      const manager = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .blockManager
      manager.startBlock(command, '~', 0)
      manager.bindAttempt(`att-chord-${manager.blocks.length}`)
      const frozen = closeRunningBlock(manager, 0)
      expect(frozen).not.toBeNull()
      return frozen!.el
    }

    /** The prompt frame is already accepted before its boundary effect.
     *  Background output is eligible only after that backend ordering point. */
    async function promptPainted(content: TerminalContent): Promise<void> {
      const session = sessionOf(content)
      session.fireScreenFrame(screenFrame(1, ['$ ']))
      session.fireEffect({
        sessionId: session.sessionId,
        generation: '1',
        effectId: '1',
        kind: 'promptBoundary',
        screenRevision: 1,
        title: '',
        body: '',
      })
      await Promise.resolve()
    }

    /** Give the automatic attachment the whole of its asynchronous path — the
     *  capture promise and the checks after it — so "nothing was attached" is
     *  a statement about a path that had its chance, not about a promise
     *  nobody awaited. */
    async function settleAttachment(): Promise<void> {
      for (let i = 0; i < 2; i++) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      }
      await Promise.resolve()
      await Promise.resolve()
    }

    /** The chord as a person presses it, from wherever focus happens to be. */
    function chordOn(el: EventTarget): void {
      el.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          metaKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
    }

    it('flips the target from the composer without letting CM6 insert a blank line', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        ed.insertText('ls -la')
        expect(targetNamed(ed)).toBe('shell')

        chordOn(viewOf(ed).contentDOM)

        expect(targetNamed(ed)).toBe('agent')
        // Back to the shell draft: `defaultKeymap` binds Mod-Enter to
        // insertBlankLine, so the one owner must SWALLOW the chord in
        // capture. A draft that came back with a newline in it would be
        // CM6 having had the key after us.
        chordOn(viewOf(ed).contentDOM)
        expect(targetNamed(ed)).toBe('shell')
        expect(ed.getDoc()).toBe('ls -la')
      } finally {
        teardown()
      }
    })
    it('attaches the retained frozen owner when Ctrl+Enter flips a visible editor to Ask', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'top')
        // The prompt boundary closes the old screen interval. A later
        // accepted frame is eligible as the background program's live screen.
        await promptPainted(content)
        sessionOf(content).fireScreenFrame(screenFrame(2, ['background repaint']))
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)

        await vi.waitFor(() =>
          expect(
            ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
          ).toContain('frozen screen attached automatically'),
        )
        expect(captureLiveFrame).toHaveBeenCalledTimes(1)
      } finally {
        teardown()
      }
    })
    it('does not offer parsed prompt bytes when Ask is selected before promptBoundary arrives', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'echo beta')
        // An accepted prompt frame may precede its control-plane boundary.
        // Ask must not treat that frame as background program output.
        sessionOf(content).fireScreenFrame(screenFrame(1, ['$ prompt']))
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        expect(targetNamed(ed)).toBe('agent')
        await settleAttachment()

        const session = sessionOf(content)
        session.fireEffect({
          sessionId: session.sessionId,
          generation: '1',
          effectId: '2',
          kind: 'promptBoundary',
          screenRevision: 1,
          title: '',
          body: '',
        })
        await settleAttachment()

        expect(captureLiveFrame).not.toHaveBeenCalled()
        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
      } finally {
        teardown()
      }
    })

    it('revalidates the live-screen owner when promptBoundary arrives during Ask capture', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'top')
        await promptPainted(content)
        // A background frame makes this screen eligible; a later boundary
        // during capture revokes that candidate before capture commits.
        sessionOf(content).fireScreenFrame(screenFrame(2, ['background tui']))
        let finishCapture!: (frame: CapturedFrame) => void
        const captureLiveFrame = vi.fn(
          () => new Promise<CapturedFrame>((resolve) => (finishCapture = resolve)),
        )
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        expect(targetNamed(ed)).toBe('agent')
        expect(captureLiveFrame).toHaveBeenCalledTimes(1)

        const session = sessionOf(content)
        session.fireEffect({
          sessionId: session.sessionId,
          generation: '1',
          effectId: '2',
          kind: 'promptBoundary',
          screenRevision: 2,
          title: '',
          body: '',
        })
        finishCapture(defaultPinnedFrame())
        await settleAttachment()

        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
      } finally {
        teardown()
      }
    })

    it('does not attach a historical block when no screen bytes follow its freeze', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'old command')
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        await settleAttachment()

        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
        expect(captureLiveFrame).not.toHaveBeenCalled()
      } finally {
        teardown()
      }
    })
    // THE agent-ask DEFECT AT UNIT LEVEL (nocx-7l4ex.21). A command that has
    // finished is not a screen anybody is painting — but the shell writes its
    // own prompt after the block freezes (D/A/OSC 7, then the B marker in
    // PS1), so "bytes newer than the freeze" was true of every finished
    // command that printed anything, and every one of them was attached. The
    // person who marked block A was handed block B as well, under a sentence
    // calling it the current screen of a full-screen program. The baseline
    // moves to the prompt's own last byte, which is what B is.
    it('does not open a live-screen interval until the prompt boundary is accepted', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'echo beta')
        const session = sessionOf(content)
        // The prompt frame has arrived, but its boundary has not. Selecting
        // Ask in this interval cannot attach the prompt as a live screen.
        session.fireScreenFrame(screenFrame(1, ['$ prompt']))
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        expect(targetNamed(ed)).toBe('agent')
        expect(captureLiveFrame).not.toHaveBeenCalled()

        session.fireEffect({
          sessionId: session.sessionId,
          generation: '1',
          effectId: '1',
          kind: 'promptBoundary',
          screenRevision: 1,
          title: '',
          body: '',
        })
        await settleAttachment()
        expect(captureLiveFrame).not.toHaveBeenCalled()
      } finally {
        teardown()
      }
    })

    it('does not attach a finished command whose prompt redrew and nothing else painted', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'echo beta')
        // The prompt frame is accepted before the boundary effect. With no
        // later background frame, a finished command is not live screen.
        await promptPainted(content)
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        await settleAttachment()

        expect(targetNamed(ed)).toBe('agent')
        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
        expect(captureLiveFrame).not.toHaveBeenCalled()
      } finally {
        teardown()
      }
    })

    // THE SAME DEFECT, SECOND WAY IN (nocx-7l4ex.21, CI run 38443315536). A
    // freeze changes the layout, the pane sends a resize, and the backend
    // answers with a frame at a newer revision — the same prompt, laid out on
    // a taller grid. Nothing is painting; the geometry moved. Measured in
    // that run: the resize reached the backend 22 ms before Ctrl+Enter, and
    // the unmarked `echo` block was attached as "the current screen of the
    // full-screen program".
    it('does not attach a finished command when only a resize followed its prompt', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'echo beta')
        await promptPainted(content)
        sessionOf(content).fireScreenFrame(
          screenFrame(2, ['$ '], { rows: SCREEN_ROWS + 4, cols: SCREEN_COLS + 10 }),
        )
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        await settleAttachment()

        expect(targetNamed(ed)).toBe('agent')
        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
        expect(captureLiveFrame).not.toHaveBeenCalled()
      } finally {
        teardown()
      }
    })

    it('keeps the next live repaint eligible when promptBoundary follows parsed prompt bytes', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'echo beta')
        const session = sessionOf(content)

        // The accepted prompt frame precedes its ordered boundary effect.
        session.fireScreenFrame(screenFrame(1, ['$ prompt']))
        session.fireEffect({
          sessionId: session.sessionId,
          generation: '1',
          effectId: '1',
          kind: 'promptBoundary',
          screenRevision: 1,
          title: '',
          body: '',
        })
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        // The next accepted frame is a background TUI repaint, not prompt
        // tail. It remains eligible after the boundary is closed.
        session.fireScreenFrame(screenFrame(2, ['background repaint']))
        chordOn(viewOf(ed).contentDOM)
        await settleAttachment()

        expect(targetNamed(ed)).toBe('agent')
        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).toContain('frozen screen attached automatically')
        expect(captureLiveFrame).toHaveBeenCalledTimes(1)
      } finally {
        teardown()
      }
    })

    // THE WEBKIT CI FLAKE (nocx-1ebrs.66.1.13). notify.raise is an OSC 9
    // effect delivered when the marker is INGESTED, not when its screen frame
    // is ACCEPTED and painted by this pane. The spec presses the chord on
    // that effect, so under load the chord can arrive while the accepted
    // screen is still the prompt. The attachment is decided once, at the
    // chord; without a later offer the finished command stays unattached
    // forever even as its marker is painted.
    it('attaches the frozen screen when the repaint that makes it eligible is accepted after the chord', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'sh put-marker-on-screen.sh &')
        await promptPainted(content)
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame
        const session = sessionOf(content)

        // The gesture lands while the accepted screen is still the prompt.
        chordOn(viewOf(ed).contentDOM)
        expect(targetNamed(ed)).toBe('agent')
        await settleAttachment()
        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
        expect(captureLiveFrame).not.toHaveBeenCalled()

        // The background writer's next repaint is accepted after the gesture.
        // It is the state the attachment reads, so it must still attach the
        // frozen command rather than being refused once and forgotten.
        session.fireScreenFrame(screenFrame(2, ['NOCX-SCREEN-00000000']))
        await settleAttachment()

        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).toContain('frozen screen attached automatically')
        expect(captureLiveFrame).toHaveBeenCalledTimes(1)
      } finally {
        teardown()
      }
    })

    // The same early chord, but here the prompt boundary is the last state
    // transition. The marker repaint was accepted while the boundary was
    // still in flight, so only applying that boundary can make the frozen
    // block eligible.
    it('attaches the frozen screen when the prompt boundary that makes it eligible arrives after the chord', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'sh put-marker-on-screen.sh &')
        const session = sessionOf(content)
        session.fireScreenFrame(screenFrame(1, ['$ ']))
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        content.captureLiveFrame = captureLiveFrame

        // The gesture lands while the block still awaits its boundary.
        chordOn(viewOf(ed).contentDOM)
        expect(targetNamed(ed)).toBe('agent')
        await settleAttachment()
        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
        expect(captureLiveFrame).not.toHaveBeenCalled()

        // The marker repaint is accepted before the boundary, so the accepted
        // screen has moved on but the block is not yet eligible.
        session.fireScreenFrame(screenFrame(2, ['NOCX-SCREEN-00000000']))
        await settleAttachment()
        expect(captureLiveFrame).not.toHaveBeenCalled()

        // The boundary names the prompt revision, not the marker repaint. The
        // pending offer must still attach the screen the person asked about.
        session.fireEffect({
          sessionId: session.sessionId,
          generation: '1',
          effectId: '2',
          kind: 'promptBoundary',
          screenRevision: 1,
          title: '',
          body: '',
        })
        await settleAttachment()

        expect(
          ed.root.querySelector('[data-control="grant"]')?.getAttribute('aria-label'),
        ).toContain('frozen screen attached automatically')
        expect(captureLiveFrame).toHaveBeenCalledTimes(1)
      } finally {
        teardown()
      }
    })

    it('flips the target when a scrollback selection has parked focus on the body', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        viewOf(ed).contentDOM.blur()
        expect(document.activeElement).toBe(document.body)

        chordOn(document.body)

        expect(targetNamed(ed)).toBe('agent')
      } finally {
        teardown()
      }
    })

    it('summons the editor from the grid instead of flipping, and never both', async () => {
      const client = makeClient()
      const { ed, content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const restore = stubScrolling()
      try {
        content.setVisible(true)
        startCommand(client)
        expect(ed.isVisible).toBe(false)
        const session = sessionOf(content)
        session.send.mockClear()

        await summonChord(content)

        expect(ed.isVisible).toBe(true)
        // The summoned editor's only target is the assistant, and the chord
        // that brought it did not ALSO flip anything: one press, one meaning.
        expect(targetNamed(ed)).toBe('agent')
        // And it never became a CR in the running command's stdin, which is
        // why this owner is in the capture phase.
        expect(session.send).not.toHaveBeenCalled()
      } finally {
        restore()
        teardown()
      }
    })

    it('leaves the chord alone while an overlay owns the keyboard', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      const entry = pushOverlay(() => true)
      try {
        content.setVisible(true)
        ed.show()
        expect(targetNamed(ed)).toBe('shell')

        chordOn(document.body)

        expect(targetNamed(ed)).toBe('shell')
      } finally {
        popOverlay(entry)
        teardown()
      }
    })

    it("leaves the chord alone in another surface's text field", async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      const field = document.createElement('input')
      document.body.append(field)
      try {
        content.setVisible(true)
        ed.show()
        field.focus()
        expect(document.activeElement).toBe(field)

        chordOn(field)

        expect(targetNamed(ed)).toBe('shell')
      } finally {
        field.remove()
        teardown()
      }
    })

    it('summons from anywhere in the pane, not only from the grid', async () => {
      const client = makeClient()
      const { ed, content, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      const restore = stubScrolling()
      try {
        content.setVisible(true)
        startCommand(client)
        expect(ed.isVisible).toBe(false)
        // The state a person is in after reading the scrollback while the
        // command runs: the selection released the focus onto <body>, which
        // is neither the composer nor the grid. The gesture is the same
        // gesture and must still summon — when the chord was claimed by
        // whichever element happened to hold the focus, this pressed nothing.
        if (!Object.prototype.hasOwnProperty.call(content, 'captureLiveFrame')) {
          content.captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        }
        intentElementOf(content).blur()
        expect(document.activeElement).toBe(document.body)

        chordOn(document.body)
        await Promise.resolve()

        expect(ed.isVisible).toBe(true)
        expect(targetNamed(ed)).toBe('agent')
      } finally {
        restore()
        teardown()
      }
    })

    it('is not swallowed by an open block menu, which owns no keys', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        const block = frozenBlock(content, 'git status')
        block.querySelector<HTMLButtonElement>('[data-block-actions]')!.click()
        expect(document.querySelector('[data-testid="block-actions-menu"]')).not.toBeNull()

        chordOn(document.body)

        // A deliberate gesture that does nothing in silence reads as a broken
        // control (toggleInputTarget says so about its own refusal). The menu
        // has no Enter semantics; it is not a claimant on this key.
        expect(targetNamed(ed)).toBe('agent')
      } finally {
        teardown()
      }
    })

    it('is not claimed by a pane that is not on screen', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        content.setVisible(false)

        chordOn(document.body)

        expect(targetNamed(ed)).toBe('shell')
      } finally {
        teardown()
      }
    })
  })

  it('nothing changes for a command nobody summons the editor during', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'top')

      // This is the regression each dropped design would have introduced:
      // an inline TUI keeps every row and every key it has today, until the
      // person deliberately asks for the editor.
      expect(ed.isVisible).toBe(false)
      expect(editorOwnsKeyboard(content)).toBe(false)
      const session = sessionOf(content)
      session.intent.mockClear()
      session.send.mockClear()
      typeIntoPane(content, 'q')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'q'))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      restore()
      teardown()
    }
  })

  it('the alternate buffer also gets the ask overlay without changing its grid', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client, 'htop')
      const session = sessionOf(content)
      const alternate = screenFrame(20, ['alternate program'])
      alternate.state.bufferKind = 'alternate'
      session.fireScreenFrame(alternate)
      const dimensions = { cols: alternate.geometry.cols, rows: alternate.geometry.rows }
      const resizeCalls = session.sendResize.mock.calls.length

      await summonChord(content)

      expect(ed.isVisible).toBe(true)
      expect(ed.root.dataset.placement).toBe('overlay')
      expect(targetNamed(ed)).toBe('agent')
      expect(paneScreen()?.geometry).toMatchObject(dimensions)
      expect(session.sendResize).toHaveBeenCalledTimes(resizeCalls)
    } finally {
      restore()
      teardown()
    }
  })

  // ── stopping it (nocx-23rph) ─────────────────────────────────────────────

  /** Every signal this pane addressed to its own session, in order. Read
   *  off the SESSION HANDLE, which is the addressing: a signal reaches the
   *  command running in the session it was asked of, and the pane has
   *  exactly one. */
  function signalsSent(content: TerminalContent): string[] {
    return sessionOf(content).signal.mock.calls.map((c: unknown[]) => c[0] as string)
  }

  it('Ctrl+C keeps selection ownership tied to the focused target', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    const selection = window.getSelection()!
    const targets: Array<{ name: string; element: HTMLElement; take?: () => void }> = []
    const scrollbackBackground = tab.pane.querySelector<HTMLElement>('.scrollback-area')
    expect(scrollbackBackground).not.toBeNull()
    const scrollbackInner = scrollbackBackground!.querySelector<HTMLElement>('.scrollback-inner')
    expect(scrollbackInner).not.toBeNull()

    const frozen = document.createElement('div')
    frozen.className = 'cmd-block'
    const frozenButton = document.createElement('button')
    frozenButton.setAttribute('data-block-actions', '')
    const selectedText = document.createTextNode('selected output')
    frozen.append(selectedText, frozenButton)
    scrollbackInner!.append(frozen)
    targets.push({ name: 'frozen block', element: frozenButton })

    const chrome = document.createElement('button')
    chrome.className = 'nocx-editor-chrome'
    tab.pane.append(chrome)
    targets.push({ name: 'chrome', element: chrome })

    const priorTabIndex = scrollbackBackground!.getAttribute('tabindex')
    scrollbackBackground!.tabIndex = 0
    targets.push({ name: 'scrollback background', element: scrollbackBackground! })

    const menu = document.createElement('div')
    menu.className = 'ui-context-menu'
    menu.dataset.testid = 'block-actions-menu'
    const menuItem = document.createElement('button')
    menuItem.className = 'ui-context-menu__item'
    menu.append(menuItem)
    document.body.append(menu)
    targets.push({ name: 'block action menu', element: menuItem })

    // The row the merge with main needed (nocx-45vkz + nocx-4ff.38). Claiming
    // a selection for the scrollback RELEASES the composer's focus and puts
    // nothing in its place, so the person who just dragged across a running
    // command's output has focus on the body and a live highlight in front of
    // them. Their Ctrl+C is a copy. Answering it with a SIGINT is the worst
    // reading of the gesture available, and it is what the two changes did
    // together while each was right alone.
    targets.push({
      name: 'released composer, selection still claimed',
      element: document.body,
      take: () => (document.activeElement as HTMLElement | null)?.blur(),
    })

    const results: Array<{
      target: string
      activeElement: string
      defaultPrevented: boolean
      signals: string[]
    }> = []
    try {
      content.setVisible(true)
      startCommand(client)
      for (const target of targets) {
        if (target.take) target.take()
        else target.element.focus()
        const range = document.createRange()
        range.selectNodeContents(selectedText)
        selection.removeAllRanges()
        selection.addRange(range)
        client.dispatcher.call.mockClear()
        sessionOf(content).signal.mockClear()
        const event = new KeyboardEvent('keydown', {
          key: 'c',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        })
        target.element.dispatchEvent(event)
        await waitForSessionQueue(content)
        const signals = signalsSent(content)
        results.push({
          target: target.name,
          activeElement: document.activeElement?.constructor.name ?? 'null',
          defaultPrevented: event.defaultPrevented,
          signals,
        })
      }
      expect(results).toEqual([
        {
          target: 'frozen block',
          activeElement: 'HTMLButtonElement',
          defaultPrevented: false,
          signals: [],
        },
        {
          target: 'chrome',
          activeElement: 'HTMLButtonElement',
          defaultPrevented: true,
          signals: ['interrupt'],
        },
        {
          target: 'scrollback background',
          activeElement: 'HTMLDivElement',
          defaultPrevented: false,
          signals: [],
        },
        {
          target: 'block action menu',
          activeElement: 'HTMLButtonElement',
          defaultPrevented: true,
          signals: ['interrupt'],
        },
        {
          target: 'released composer, selection still claimed',
          activeElement: 'HTMLBodyElement',
          defaultPrevented: false,
          signals: [],
        },
      ])
    } finally {
      selection.removeAllRanges()
      if (priorTabIndex === null) scrollbackBackground!.removeAttribute('tabindex')
      else scrollbackBackground!.setAttribute('tabindex', priorTabIndex)
      menu.remove()
      frozen.remove()
      chrome.remove()
      restore()
      teardown()
    }
  })

  it('Ctrl+C is addressed to the running command even when the grid does not hold focus', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    // Somewhere in the pane that is NOT the grid and NOT a text control —
    // the state the owner reported, where the key reaches nobody today.
    const elsewhere = document.createElement('div')
    elsewhere.tabIndex = -1
    paneOf(content).append(elsewhere)
    try {
      content.setVisible(true)
      startCommand(client)
      elsewhere.focus()
      expect(document.activeElement).toBe(elsewhere)

      elsewhere.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      await waitForSessionQueue(content)

      expect(signalsSent(content)).toEqual(['interrupt'])
    } finally {
      restore()
      elsewhere.remove()
      teardown()
    }
  })

  it('and it stands down where the byte already arrives: the grid keeps its own Ctrl+C', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      // The terminal input element owns focus; Ctrl+C becomes an interrupt
      // intent and the backend line discipline handles it.
      // turns it into SIGINT. Two owners for one keystroke would signal
      // twice — which is a second interrupt the person did not ask for.
      const textarea = document.createElement('textarea')
      gridOf(content).append(textarea)
      textarea.focus()

      textarea.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )

      expect(signalsSent(content)).toEqual([])
    } finally {
      restore()
      teardown()
    }
  })
  // ── the copy chord over a live-grid selection (nocx-1ebrs.19.1) ──────────
  //
  // THE OWNER'S DECISION, 2026-10-20: Cmd+C with a VISIBLE LIVE-GRID SELECTION
  // copies it; with no selection the chord keeps whatever it meant before.
  // Both halves are asserted below, at the seam a person's keystroke reaches:
  // the pane's own input element, which is where the keyboard sits after a
  // press on the grid, and where `_globalKeydown`'s interrupt and the intent
  // the program receives are both decided.

  /** A painted live row with `text` on it, and the selectable geometry the
   *  gesture resolves cells from — the painter's mapping, one cell 8 x 16. */
  async function paintLiveRow(
    content: TerminalContent,
    client: ClientFake,
    text: string,
  ): Promise<HTMLElement> {
    const session: SessionFake = client._sessions[0]
    const frame = screenFrame(91, [text])
    frame.state.mouseTracking = true
    session.fireScreenFrame(frame)
    const live = gridOf(content)
    await vi.waitFor(() => expect(live.querySelector('.term-grid')).not.toBeNull())
    const surface = live.querySelector<HTMLElement>('.term-grid')!
    await vi.waitFor(() => expect(surface.textContent).toContain(text))
    surface.getBoundingClientRect = () =>
      ({ left: 0, top: 0, right: 320, bottom: 64, width: 320, height: 64 }) as DOMRect
    return surface
  }

  /** A pointer event of the kind the gesture reads, in the surface's own
   *  coordinates: one cell is 8 x 16 here, so `col` and a row-0 press. */
  function atCell(type: string, col: number, buttons: number): Event {
    const event = new Event(type, { bubbles: true, cancelable: true })
    Object.defineProperties(event, {
      button: { value: 0 },
      buttons: { value: buttons },
      clientX: { value: col * 8 + 2 },
      clientY: { value: 4 },
    })
    return event
  }

  /** The person's drag across cells `from..to` of row 0, made the way the
   *  pointer makes one: press on the surface, motion on the window, release. */
  function dragAcross(surface: HTMLElement, from: number, to: number): void {
    surface.dispatchEvent(atCell('pointerdown', from, 1))
    window.dispatchEvent(atCell('pointermove', to, 1))
    surface.dispatchEvent(atCell('pointerup', to, 0))
  }

  /** The KEY intents this pane sent its program, and only those: a focus
   *  report and the pointer sequence the drag itself is both travel the same
   *  channel asynchronously, and they are not what these tests are about. */
  const keyIntents = (content: TerminalContent): string[] =>
    sessionOf(content)
      .intent.mock.calls.filter((call: unknown[]) => call[0] === 'key')
      .map((call: unknown[]) => String(call[1]))

  it('the copy chord copies a visible live-grid selection and sends nothing', async () => {
    const clipboard = makeClipboard()
    const client = makeClient()
    const { content, teardown } = await mountTerminal(clipboard, { attachToDocument: true }, client)
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      const surface = await paintLiveRow(content, client, 'alpha bravo charlie')

      // The selection a person makes: "bravo" is cells 6..10 of row 0.
      dragAcross(surface, 6, 10)
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
      expect(clipboard.writeText).toHaveBeenCalledWith('bravo')

      // From here on, only the chord may be heard. Every intent already sent
      // belongs to the pointer sequence the drag itself is (nocx-zg3k3.15.5).
      sessionOf(content).signal.mockClear()

      // WHERE A PERSON'S KEYSTROKE LANDS: the pane's input element, which a
      // press on the grid focuses (takeKeyboardToGrid) and which turns a
      // keydown into the intent the program receives. Focusing it is itself an
      // intent (a focus report the program may have asked for), so the records
      // are cleared AFTER it and after the queue has run.
      const input = gridOf(content).querySelector<HTMLElement>('.ui-terminal-input')!
      expect(input).not.toBeNull()
      input.focus()
      expect(document.activeElement).toBe(input)
      await waitForSessionQueue(content)
      clipboard.writeText.mockClear()
      sessionOf(content).intent.mockClear()
      const chord = new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        metaKey: true,
        bubbles: true,
        cancelable: true,
      })
      input.dispatchEvent(chord)
      await waitForSessionQueue(content)

      // COPIED: the same text copy-on-select already wrote at the release —
      // a second way to ask, not a second copy path.
      expect(clipboard.writeText).toHaveBeenCalledWith('bravo')
      // CLEARED: the highlight is gone with the chord that used it.
      expect(surface.querySelector('.term-grid-selection')).toBeNull()
      // AND NOT SENT: the program received no key at all, so the chord became
      // neither a keypress nor an interrupt, and no signal was addressed to
      // the command.
      await waitForSessionQueue(content)
      expect(keyIntents(content)).toEqual([])
      expect(signalsSent(content)).toEqual([])
      // The key is the pane's: the browser does not also act on it.
      expect(chord.defaultPrevented).toBe(true)

      // AND CTRL+C IS NOT THIS CHORD. A selection does not turn the interrupt
      // into a copy: the person reaching for a SIGINT gets one, and the
      // selection goes with the key that was sent to the program.
      dragAcross(surface, 6, 10)
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
      await waitForSessionQueue(content)
      clipboard.writeText.mockClear()
      sessionOf(content).intent.mockClear()
      const interrupt = new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      })
      input.dispatchEvent(interrupt)
      await vi.waitFor(() => expect(keyIntents(content)).toEqual(['Ctrl+c']))
      expect(clipboard.writeText).not.toHaveBeenCalled()
      expect(surface.querySelector('.term-grid-selection')).toBeNull()
    } finally {
      restore()
      teardown()
    }
  })

  it("the copy chord over a selection leaves another surface's own copy alone", async () => {
    // TWO SURFACES MAY NEVER OWN ONE INPUT (AGENTS.md). A grid selection can
    // be on screen while the person is typing somewhere else — a settings
    // field, a dialog, the composer — and the copy chord there is that
    // surface's own. The gesture answers "is a selection painted", which is
    // true in all of those states, so the PANE is where the ownership question
    // is answered.
    const clipboard = makeClipboard()
    const client = makeClient()
    const { content, teardown } = await mountTerminal(clipboard, { attachToDocument: true }, client)
    const restore = stubScrolling()
    const field = document.createElement('input')
    document.body.append(field)
    try {
      content.setVisible(true)
      startCommand(client)
      const surface = await paintLiveRow(content, client, 'alpha bravo charlie')
      dragAcross(surface, 6, 10)
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()

      // Now the keyboard is somewhere else, and the field's own copy is what
      // Cmd+C means there.
      field.focus()
      expect(document.activeElement).toBe(field)
      await waitForSessionQueue(content)
      clipboard.writeText.mockClear()
      sessionOf(content).intent.mockClear()
      const chord = new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        metaKey: true,
        bubbles: true,
        cancelable: true,
      })
      field.dispatchEvent(chord)
      await waitForSessionQueue(content)

      expect(clipboard.writeText).not.toHaveBeenCalled()
      expect(chord.defaultPrevented).toBe(false)
      expect(keyIntents(content)).toEqual([])
      // And the selection is still the person's: nothing used it up.
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
    } finally {
      field.remove()
      restore()
      teardown()
    }
  })

  it('a pane that is not the active one does not answer the copy chord', async () => {
    // The gesture is per pane and its selection survives until the pane is
    // looked at again, so "there is a selection somewhere" is not the same fact
    // as "this pane has the keyboard". Only the active pane answers.
    const clipboard = makeClipboard()
    const client = makeClient()
    const { content, teardown } = await mountTerminal(clipboard, { attachToDocument: true }, client)
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      const surface = await paintLiveRow(content, client, 'alpha bravo charlie')
      dragAcross(surface, 6, 10)
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()

      content.setVisible(false)
      clipboard.writeText.mockClear()
      const chord = new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        metaKey: true,
        bubbles: true,
        cancelable: true,
      })
      document.dispatchEvent(chord)
      await waitForSessionQueue(content)

      expect(clipboard.writeText).not.toHaveBeenCalled()
      expect(chord.defaultPrevented).toBe(false)
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
    } finally {
      restore()
      teardown()
    }
  })

  it('with nothing selected the copy chord means what it always did', async () => {
    const clipboard = makeClipboard()
    const client = makeClient()
    const { content, teardown } = await mountTerminal(clipboard, { attachToDocument: true }, client)
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      const surface = await paintLiveRow(content, client, 'alpha bravo charlie')
      const input = gridOf(content).querySelector<HTMLElement>('.ui-terminal-input')!
      input.focus()
      // A drag taught nothing is left behind: press and release on ONE cell is
      // a click, so the state below is "the person clicked, nothing is
      // selected" — the state the chord must not change.
      dragAcross(surface, 6, 6)
      expect(surface.querySelector('.term-grid-selection')).toBeNull()
      await waitForSessionQueue(content)
      clipboard.writeText.mockClear()
      sessionOf(content).intent.mockClear()
      sessionOf(content).signal.mockClear()

      // Ctrl+C is the interrupt's own spelling, on the grid it already
      // belonged to: the intent the program receives, and no second signal
      // (nocx-23rph — one owner per keystroke).
      const ctrlC = new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      })
      input.dispatchEvent(ctrlC)
      await vi.waitFor(() => expect(keyIntents(content)).toEqual(['Ctrl+c']))
      expect(signalsSent(content)).toEqual([])
      expect(clipboard.writeText).not.toHaveBeenCalled()
      expect(ctrlC.defaultPrevented).toBe(true)

      // And Cmd+C is what it was before this bead: the same key intent with
      // Super held, which is what reaches the program when nothing is
      // selected. It is NOT claimed, so the browser's own copy still runs.
      sessionOf(content).intent.mockClear()
      const metaC = new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        metaKey: true,
        bubbles: true,
        cancelable: true,
      })
      input.dispatchEvent(metaC)
      await vi.waitFor(() => expect(keyIntents(content)).toEqual(['Super+c']))
      expect(clipboard.writeText).not.toHaveBeenCalled()
      expect(metaC.defaultPrevented).toBe(true)
    } finally {
      restore()
      teardown()
    }
  })

  it('with nothing running, Ctrl+C signals nothing', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    const elsewhere = document.createElement('div')
    elsewhere.tabIndex = -1
    paneOf(content).append(elsewhere)
    try {
      content.setVisible(true)
      lifecycleHandler(client)({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
      })
      elsewhere.focus()

      elsewhere.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          code: 'KeyC',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )

      // Nothing is addressed, because there is no addressee. The wire
      // method's own honest refusal covers the race where the command ends
      // between the gesture and the call.
      expect(signalsSent(content)).toEqual([])
    } finally {
      restore()
      elsewhere.remove()
      teardown()
    }
  })

  it('the summoned Agent editor delegates Ctrl+C once to the running command and never to the turn', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      await summonChord(content)
      ed.insertText('what is it waiting on')
      const session = sessionOf(content)
      session.send.mockClear()

      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          code: 'KeyC',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )

      // The command — not the turn — receives exactly one ordinary interrupt
      // through its signal ladder, and the half-typed question survives.
      expect(ed.getDoc()).toBe('what is it waiting on')
      await waitForSessionQueue(content)
      expect(signalsSent(content)).toEqual(['interrupt'])
      expect(session.send).not.toHaveBeenCalledWith('\x03')
      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
      ).toHaveLength(0)
    } finally {
      restore()
      teardown()
    }
  })

  it('and a bare Enter on an empty question sends no CR either (nocx-oova)', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      startCommand(client)
      await summonChord(content)
      const session = sessionOf(content)
      await waitForSessionQueue(content)
      session.send.mockClear()
      session.intent.mockClear()

      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await waitForSessionQueue(content)

      expect(session.send).not.toHaveBeenCalled()
      expect(session.intent).not.toHaveBeenCalled()
    } finally {
      restore()
      teardown()
    }
  })

  it('with the shell target active, Enter and Ctrl+C keep their shell behaviour', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      lifecycleHandler(client)({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
      })
      expect(ed.isVisible).toBe(true)
      expect(targetNamed(ed)).toBe('shell')
      const session = sessionOf(content)
      session.send.mockClear()

      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() => expect(session.intent.mock.calls).toEqual([['key', 'enter']]))
      expect(session.send).not.toHaveBeenCalled()

      session.intent.mockClear()
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          code: 'KeyC',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      await vi.waitFor(() => expect(session.intent.mock.calls).toEqual([['key', 'Ctrl+c']]))
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      restore()
      teardown()
    }
  })

  // ── the ⋮ menu, which is what makes both gestures visible ────────────────

  /** Open the running block's overflow menu and return its items. */
  function runningBlockMenu(content: TerminalContent): HTMLElement[] {
    const btn = paneOf(content).querySelector<HTMLElement>(
      '.cmd-block-running [data-block-actions]',
    )
    expect(btn, 'the running block has no ⋮ button').not.toBeNull()
    btn!.click()
    return Array.from(
      document.querySelectorAll<HTMLElement>(
        '[data-testid="block-actions-menu"] .ui-context-menu__item',
      ),
    )
  }

  function itemNamed(items: HTMLElement[], action: string): HTMLElement | undefined {
    return items.find((el) => el.dataset.itemId === action)
  }

  it('the running block grant action exists only after the target switches to Ask', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 300')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 300',
        },
      })
      expect(ed.isVisible).toBe(false)
      content.captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())

      const runItems = runningBlockMenu(content)
      expect(itemNamed(runItems, 'grant')).toBeUndefined()
      expect(itemNamed(runItems, 'stop')?.textContent).toBe('Stop')
      paneOf(content).querySelector<HTMLElement>('.cmd-block-running [data-block-actions]')!.click()

      await summonChord(content)
      await vi.waitFor(() => expect(ed.isVisible).toBe(true))
      expect(targetNamed(ed)).toBe('agent')
      const grant = itemNamed(runningBlockMenu(content), 'grant')
      expect(grant?.textContent).toBe('Ask about this block')
      grant?.click()

      const block = paneOf(content).querySelector<HTMLElement>('.cmd-block-running')
      expect(block?.dataset.granted).toBe('true')
      expect(
        ed.root.querySelector<HTMLButtonElement>('[data-control="grant"]')?.dataset.state,
      ).toBe('chosen')

      escapeOn(view.contentDOM)
      expect(ed.isVisible).toBe(false)
      expect(targetNamed(ed)).toBe('shell')
      expect(block?.dataset.granted).toBeUndefined()
      expect(itemNamed(runningBlockMenu(content), 'grant')).toBeUndefined()
      paneOf(content).querySelector<HTMLElement>('.cmd-block-running [data-block-actions]')!.click()

      await summonChord(content)
      const unmark = itemNamed(runningBlockMenu(content), 'grant')
      expect(unmark?.textContent).toBe('Unmark')
      unmark?.click()
      expect(block?.dataset.granted).toBeUndefined()
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('an assistant command offers Stop once the runtime names it running', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const handler = lifecycleHandler(client)
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      void content.submitAgentCommand('sleep 300')
      await Promise.resolve()
      // The submit opened no card (nocx-2v80t.3.2): nothing is drawn, and
      // nothing is offered — the command's bytes are not even out yet.
      expect(scrollbackFor(content).blockManager.runningBlock).toBeNull()

      // The runtime names the attempt running: the card opens WITH the
      // kernel's word, so its Stop is there the moment the card is.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 300',
        },
      })
      const stop = itemNamed(runningBlockMenu(content), 'stop')
      expect(stop, 'the card the runtime opened offers no Stop').toBeDefined()
      stop!.click()
      await waitForSessionQueue(content)
      expect(signalsSent(content)).toEqual(['stop'])
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('and Stop, which goes through the escalation ladder rather than a second answer', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 300')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 300',
        },
      })

      const stop = itemNamed(runningBlockMenu(content), 'stop')
      expect(stop, 'the running block’s menu offers no Stop').toBeDefined()
      stop!.click()

      // `stop`, not `interrupt`: the menu item promises the command is gone,
      // and the backend's ladder is what keeps that promise.
      await waitForSessionQueue(content)
      expect(signalsSent(content)).toEqual(['stop'])
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('names a lifecycle/foreground contradiction instead of claiming nothing is running', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-shared',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'top',
        },
      })
      sessionOf(content).signal.mockResolvedValue({
        signal: 'stop',
        outcome: 'unreconciled',
      })
      vi.mocked(showToast).mockClear()

      itemNamed(runningBlockMenu(content), 'stop')!.click()

      await vi.waitFor(() => expect(showToast).toHaveBeenCalled())
      const calls = vi.mocked(showToast).mock.calls
      const warning = calls[calls.length - 1]?.[0]
      expect(warning).toMatchObject({ level: 'warning' })
      expect(warning?.message).toContain('still recorded as running')
      expect(warning?.message).not.toContain('Nothing is running')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  // ── the window between the block and the kernel (2026-09-16) ─────────────
  //
  // The block manager used to draw the running block — and its Stop — the
  // moment the submit was answered, while the lifecycle kernel learned of the
  // attempt a beat LATER, over its own channel. Both doors to the stop
  // handler used to ask the KERNEL first, so a click inside that window did
  // nothing and said nothing. nocx-2v80t.3.2 closed the window structurally:
  // the submit opens no card, and the card the runtime's authenticated start
  // opens carries the kernel's word with it — block and kernel state can no
  // longer disagree about whether a command is running.

  it('the running card offers both doors to the stop handler the moment it opens', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      // No fact, no card (nocx-2v80t.3.2) — and no Stop to press.
      expect(scrollbackFor(content).blockManager.runningBlock).toBeNull()
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run-2',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      // The card the runtime opened carries both doors to the stop handler:
      // the block's own control and the ⋮ menu. They reach session.signal
      // without consulting the kernel first — the rule the 2026-09-16 window
      // bought, which outlives the window itself.
      const stop = paneOf(content).querySelector<HTMLElement>(
        '.cmd-block-running [data-block-control][aria-label="Stop"]',
      )
      expect(stop, 'the running block has no Stop control').not.toBeNull()
      stop!.click()
      await waitForSessionQueue(content)
      expect(signalsSent(content)).toEqual(['stop'])
      expect(itemNamed(runningBlockMenu(content), 'stop')).not.toBeUndefined()
    } finally {
      restore()
      teardown()
    }
  })

  // ── a stopped command is cancelled, never failed (nocx-9bpeq.19) ─────────
  //
  // The backend's own completion fact (contracts/lifecycle.changed.
  // schema.json's `attempt`) states only exitCode, completedAt and fence —
  // never a cause — so SIGINT's 130 and a program's own exit 130 are
  // otherwise indistinguishable. These two tests are the pairing the bead
  // asks for: the SAME exit code, through the SAME completion path, reading
  // two different ways depending on whether nocx actually sent the signal.

  it('orders Interrupt after a queued mouse release', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    const session = sessionOf(content)
    let releasePress!: () => void
    const pressGate = new Promise<void>((resolve) => {
      releasePress = resolve
    })
    const result: SessionIntentResult = { state: 'executed', bytesWritten: 1, fenceAfter: 0 }
    const events: string[] = []
    session.intent.mockImplementation((kind: string, payload: string) => {
      events.push(`intent:${payload}`)
      return (payload === 'press left 2 1' ? pressGate : Promise.resolve()).then(() => result)
    })
    session.signal.mockImplementation((signal: SessionSignal['signal']) => {
      events.push(`signal:${signal}`)
      return Promise.resolve({ signal, outcome: 'delivered' })
    })

    try {
      content.setVisible(true)
      const frame = screenFrame(1, ['mouse target'])
      frame.state.mouseTracking = true
      session.fireScreenFrame(frame)
      startCommand(client, 'cat -v')

      const surface = gridOf(content).querySelector<HTMLElement>('.term-grid')!
      await vi.waitFor(() => expect(surface.textContent).toContain('mouse target'))
      await waitForSessionQueue(content)
      events.length = 0
      surface.getBoundingClientRect = () =>
        ({ left: 10, top: 20, right: 330, bottom: 84, width: 320, height: 64 }) as DOMRect
      const pointer = (type: 'pointerdown' | 'pointerup', buttons: number): Event => {
        const event = new Event(type, { bubbles: true, cancelable: true })
        Object.defineProperties(event, {
          button: { value: 0 },
          buttons: { value: buttons },
          clientX: { value: 26 },
          clientY: { value: 44 },
        })
        return event
      }
      surface.dispatchEvent(pointer('pointerdown', 1))
      surface.dispatchEvent(pointer('pointerup', 0))
      await vi.waitFor(() => expect(events).toEqual(['intent:press left 2 1']))
      expect(
        (
          content as unknown as {
            _sessionCallQueue: Array<{ intent?: { payload: string } }>
          }
        )._sessionCallQueue.map((call) => call.intent?.payload),
      ).toEqual(['release left 2 1'])

      const interrupt = paneOf(content).querySelector<HTMLButtonElement>(
        '[data-control="interrupt"]',
      )
      expect(interrupt).not.toBeNull()
      interrupt!.click()
      expect(events).toEqual(['intent:press left 2 1'])

      releasePress()
      await vi.waitFor(() =>
        expect(events).toEqual([
          'intent:press left 2 1',
          'intent:release left 2 1',
          'signal:interrupt',
        ]),
      )
    } finally {
      releasePress()
      restore()
      teardown()
    }
  })

  it('a command stopped through the pane settles as cancelled, never failed', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-stop-19',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      expect(rec).not.toBeNull()

      // The REAL stop path: the Stop menu item, signalActiveCommand, the
      // pane's session.signal — not a status flipped by hand.
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await waitForSessionQueue(content)
      expect(signalsSent(content)).toEqual(['stop'])

      // The escalation ladder's SIGINT rung. `fence` is REQUIRED here, not
      // decorative: lifecycle/state.ts's own `freezeBlock` — the gate
      // terminal-content.ts's `freezeBlock` callback calls before it will
      // even reach `scrollback.freezeFromAttempt` at all — refuses an
      // attempt with `fence === undefined` outright (ADR-0024 §7), so
      // omitting it here left `freezeFromAttempt` never called and
      // `rec.status` stuck at 'running'. A real authenticated completion
      // always carries one (contracts/lifecycle.changed.schema.json:
      // "Present exactly when state is completed"). The completion event
      // lands first and the fence bytes right behind it — the sighting
      // settles the visual boundary, and nothing else does (nocx-2v80t.3.2).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-stop-19',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '9'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-stop-19')
      expect(rec.status).toBe('cancelled')

      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).not.toBe('failure')
      expect(rec.el.dataset.outcome).toBe('cancelled')
      expect(rec.el.classList.contains('cmd-block')).toBe(true)
      expect(
        rec.el.querySelector(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')
          ?.textContent,
      ).toBe('Stopped')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('a Stop accepted before the command started is armed, and its 130 still reads as stopped', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-held-19',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      // The app attempt is open and the shell has not begun the line: the
      // backend ACCEPTS the gesture and holds the byte for the authenticated
      // start (nocx-zas0d, internal/transport/ws_signal.go).
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'held' })
      vi.mocked(showToast).mockClear()

      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await waitForSessionQueue(content)
      expect(signalsSent(content)).toEqual(['stop'])

      await vi.waitFor(() => expect(showToast).toHaveBeenCalled())
      const calls = vi.mocked(showToast).mock.calls
      const toast = calls[calls.length - 1]?.[0]
      // Said out loud, and said as what it is: the stop is armed, not done.
      expect(toast).toMatchObject({ level: 'info' })
      expect(toast?.message).toContain('had not started yet')
      expect(toast?.message).not.toContain('Nothing is running')
      // The acceptance is kept, because the byte IS coming: reverting it
      // would make the completion below — SIGINT's 130 — read as the
      // program's own failure.
      expect(rec.stopRequested).toBe(true)

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-held-19',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '7'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-held-19')
      expect(rec.status).toBe('cancelled')

      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).toBe('cancelled')
      expect(
        rec.el.querySelector(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')
          ?.textContent,
      ).toBe('Stopped')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('a stop the backend could not deliver is NOT a stop: the block is the shell’s own again', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-undelivered-19',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'held' })
      vi.mocked(showToast).mockClear()
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await vi.waitFor(() => expect(showToast).toHaveBeenCalled())
      expect(rec.stopRequested).toBe(true)

      // The obligation ends without a write: the attempt is gone before the
      // byte could be put in (nocx-zas0d, review finding 2). The acceptance is
      // therefore NOT evidence of a stop, and the pane must stop treating it as
      // one — this is the reviewer's case: a nonzero completion arriving after
      // a stop that never landed.
      signalUndeliveredHandler(client)({
        sessionId: sessionOf(content).sessionId,
        signal: 'stop',
        attempt: 'att-undelivered-19',
        reason: 'attempt-closed',
      })
      expect(rec.stopRequested).toBe(false)

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-undelivered-19',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '5'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-undelivered-19')
      expect(rec.status).toBe('failure')

      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).toBe('failure')
      expect(
        rec.el.querySelector(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')
          ?.textContent,
      ).toBe('Exit 130')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('a refused delivery says so, and names only the attempt it belongs to', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-refused-19',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'held' })
      vi.mocked(showToast).mockClear()
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await vi.waitFor(() => expect(showToast).toHaveBeenCalled())
      vi.mocked(showToast).mockClear()

      // The MARK is attempt-scoped: a notice about ANOTHER attempt must not
      // touch this block's, because a command that replaced it is not the one
      // the person stopped. The MESSAGE is not — a stop addressed to this
      // session that did not land is the person's own gesture whatever the
      // block it named has become, and hiding it would be the silence the
      // whole notice exists to end.
      signalUndeliveredHandler(client)({
        sessionId: sessionOf(content).sessionId,
        signal: 'stop',
        attempt: 'att-someone-else',
        reason: 'write-refused',
      })
      expect(rec.stopRequested).toBe(true)
      expect(showToast).toHaveBeenCalledTimes(1)

      // The block's own notice: the mark comes off and the person is told the
      // command may still be running, with what to do about it.
      signalUndeliveredHandler(client)({
        sessionId: sessionOf(content).sessionId,
        signal: 'stop',
        attempt: 'att-refused-19',
        reason: 'write-refused',
      })
      expect(rec.stopRequested).toBe(false)
      const calls = vi.mocked(showToast).mock.calls
      const toast = calls[calls.length - 1]?.[0]
      expect(toast).toMatchObject({ level: 'warning' })
      expect(toast?.message).toContain('could not deliver the stop')
      expect(toast?.message).toContain('press Stop again')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('the backend’s settlement decides "Stopped", with no notification at all', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-state-19',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'held' })
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await vi.waitFor(() => expect(rec.stopRequested).toBe(true))

      // NO session.signalUndelivered IS EVER DELIVERED — a dropped frame, or a
      // reconnect that replayed the fact instead. The completion fact carries
      // the backend's own settlement, and THAT is what the block settles by
      // (nocx-zas0d, review of 6830b43d, major 2): an outcome that only a
      // notification could carry would leave this command painted as one the
      // person stopped.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        signalDelivery: 'undelivered',
        attempt: {
          id: 'att-state-19',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '3'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-state-19')
      expect(rec.status).toBe('failure')

      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).toBe('failure')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('and a delivered stop settles as stopped from that same state', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-state-20',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sleep 30',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'held' })
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await vi.waitFor(() => expect(rec.stopRequested).toBe(true))

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        signalDelivery: 'delivered',
        attempt: {
          id: 'att-state-20',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '4'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-state-20')
      expect(rec.status).toBe('cancelled')

      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).toBe('cancelled')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('a retry that lands after a held stop failed is drawn as Stopped (nocx-zas0d, review 3)', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sleep 30')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      const open = {
        id: 'att-retry-21',
        state: 'open' as const,
        origin: 'app' as const,
        submitId: submitToken(client),
        command: 'sleep 30',
      }
      handler({ lane: 'lane-1', lifecycle: 'running', domain: 'd1', epoch: 1, attempt: open })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'held' })
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await vi.waitFor(() => expect(rec.stopRequested).toBe(true))

      // The held Stop FAILED, and the notice was dropped: what reaches the pane
      // is the open attempt's fact, re-published with its record (item 4).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        signalDelivery: 'undelivered',
        attempt: open,
      })

      // The person presses Stop again, and this time it lands.
      sessionOf(content).signal.mockResolvedValue({ signal: 'stop', outcome: 'delivered' })
      itemNamed(runningBlockMenu(content), 'stop')!.click()
      await vi.waitFor(() => expect(sessionOf(content).signal).toHaveBeenCalledTimes(2))

      // The completion carries the attempt's LATEST outcome, and the block
      // says the person stopped it (item 2) — not the earlier failure.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        signalDelivery: 'delivered',
        attempt: {
          id: 'att-retry-21',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '6'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-retry-21')
      expect(rec.status).toBe('cancelled')
      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).toBe('cancelled')
      expect(rec.el.textContent).toContain('Stopped')
    } finally {
      restore()
      teardown()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    }
  })

  it('a program that exits 130 on its own, with no stop request, still reads as failure', async () => {
    const client = makeClient()
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      const handler = lifecycleHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('sh -c "exit 130"')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-self-19',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'sh -c "exit 130"',
        },
      })
      const rec = scrollbackFor(content).blockManager.runningBlock!
      expect(rec).not.toBeNull()

      // No Stop, no ⋮ item, no session.signal at all — the SAME exit code
      // the test above delivers, but nothing in nocx asked for it.
      expect(signalsSent(content)).toEqual([])
      // `fence` required — see the sibling test's comment: without it,
      // lifecycle/state.ts's `freezeBlock` refuses the completion before
      // `freezeFromAttempt` is ever called, and `rec.status` never leaves
      // 'running'.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-self-19',
          state: 'completed',
          exitCode: 130,
          completedAt: '2026-09-15T00:00:00Z',
          fence: '1'.repeat(64),
        },
      })
      blockClosedHandler(client)('att-self-19')
      expect(rec.status).toBe('failure')

      await vi.waitFor(() => expect(rec.el.classList.contains('cmd-block-running')).toBe(false))
      expect(rec.el.dataset.outcome).toBe('failure')
      expect(
        rec.el.querySelector(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')
          ?.textContent,
      ).toBe('Exit 130')
    } finally {
      restore()
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// BEL is two facts (nocx-n3nfg)
// ═══════════════════════════════════════════════════════════════════════════
//
// A program printed BEL. That is a local attention signal — the tab dot,
// which costs nothing and needs no backend — AND a notification event with a
// kind, a trust class, an attribution and a feed row. The wiring must do
// both, because they are different facts: the dot says "output you have not
// seen", the event says "a program asked for you", and neither answers the
// other's question. Replacing one with the other is the defect these tests
// exist to catch, so every one of them asserts both halves.
//
// The session runtime parses BEL and delivers an identity-bearing effect.
// This suite checks the product behavior at the session-effect boundary.
describe('a program printing BEL (nocx-n3nfg)', () => {
  /** The bell only lights the dot on a pane that has SETTLED and is not the
   *  one being looked at (panes.ts markActivity): a shell's banner and first
   *  prompt are not unread output. The B marker is what a real prompt-end
   *  delivers, so the settle here is the one a user's session performs. */
  const settle = (content: TerminalContent): void => {
    const session = sessionOf(content)
    session.fireScreenFrame(screenFrame(1, ['$ prompt']))
    session.fireEffect({
      sessionId: session.sessionId,
      generation: '1',
      effectId: '1',
      kind: 'promptBoundary',
      screenRevision: 1,
      title: '',
      body: '',
    })
  }

  const fireRuntimeEffect = (
    session: SessionFake,
    kind: SessionEffect['kind'],
    effectId: string,
    body = '',
    title = '',
  ): void => {
    session.fireEffect({
      sessionId: session.sessionId,
      generation: '1',
      effectId,
      kind,
      title,
      body,
    })
  }

  /** The notify.bell requests this pane sent, in order. */
  const bellCalls = (client: ClientFake): unknown[][] =>
    client.dispatcher.call.mock.calls.filter((c: unknown[]) => c[0] === 'notify.bell')

  it('routes backend notification effects through notify.raise', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      fireRuntimeEffect(session, 'notification', '2', 'Build finished', 'Build')
      expect(client.dispatcher.call).toHaveBeenCalledWith('notify.raise', {
        sessionId: session.sessionId,
        title: 'Build',
        body: 'Build finished',
      })
    } finally {
      teardown()
    }
  })

  it('settles integrated startup on authenticated prompt_ready before a background bell', async () => {
    const session = makeSession({ awaitsIntegration: true })
    const client = makeClient()
    client.openSession.mockResolvedValue(session)
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      // The backend's promptBoundary is an observation, not lifecycle authority.
      settle(content)
      fireRuntimeEffect(session, 'bell', '2')
      expect(tab.hasActivity).toBe(false)

      lifecycleHandler(
        client,
        session.sessionId,
      )({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
      })
      fireRuntimeEffect(session, 'bell', '3')

      expect(tab.hasActivity).toBe(true)
      expect(bellCalls(client)).toHaveLength(2)
    } finally {
      teardown()
    }
  })

  it('reports the bell AND marks the tab, addressing the live session', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      settle(content)
      expect(tab.hasActivity).toBe(false)

      fireRuntimeEffect(sessionOf(content), 'bell', '2')

      // The notification: its own method, carrying addressing and nothing
      // else. The kind is not here because it cannot be — it is stamped from
      // the method invoked, which is the whole reason notify.bell exists
      // rather than an argument on notify.raise (ADR-0047 §2.2, design §3).
      expect(bellCalls(client)).toEqual([
        ['notify.bell', { sessionId: sessionOf(content).sessionId }],
      ])
      // The tab dot, which is local and never went near the backend.
      expect(tab.hasActivity).toBe(true)
    } finally {
      teardown()
    }
  })

  // AGENTS.md rule 3: for every external call, a test where that call fails.
  // The bell is fire-and-forget, so the failure must cost the report and
  // nothing else — the pane keeps running and the tab dot, which never
  // needed the backend, still lights. A rejection that escaped here would be
  // an unhandled promise rejection on a byte any shell prints.
  it('keeps the tab dot when the backend refuses the report', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      settle(content)
      // Set after the mount so this rejection belongs to the bell alone.
      client.dispatcher.call.mockRejectedValue(new Error('notify.bell not available'))

      fireRuntimeEffect(sessionOf(content), 'bell', '2')
      // Let the rejection settle: an unhandled one surfaces here, not later.
      await Promise.resolve()
      await Promise.resolve()

      expect(bellCalls(client).length).toBe(1)
      expect(tab.hasActivity).toBe(true)
      expect(content.shellState).toBeDefined()
    } finally {
      teardown()
    }
  })

  it('ignores an effect from a session no longer bound to the pane', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      settle(content)
      const session = sessionOf(content)
      ;(content as unknown as { session: SessionFake | null }).session = null

      fireRuntimeEffect(session, 'bell', '2')

      expect(bellCalls(client)).toEqual([])
      expect(tab.hasActivity).toBe(false)
    } finally {
      teardown()
    }
  })
})
describe('the summoned frame stays a local display pin', () => {
  function frame(text: string): CapturedFrame {
    return {
      rows: [
        {
          kind: 'cells',
          cells: Array.from({ length: 80 }, (_, index) => ({
            char: text[index] ?? ' ',
            attrs: emptyAttrs(),
          })),
        },
      ],
      cursor: { line: 0, col: text.length },
      provenance: {
        source: 'live',
        identity: {
          buffer: { kind: 'alternate', altSession: 1 },
          cols: 80,
          rows: 24,
          generation: 11,
        },
        range: { start: 0, end: 1 },
        scrollbackCapLines: 10000,
      },
    }
  }

  function startRunning(client: ClientFake, command = 'top'): void {
    const handler = lifecycleHandler(client)
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: {
        id: 'att-run',
        state: 'open',
        origin: 'app',
        submitId: submitToken(client),
        command,
      },
    })
  }

  function gridOf(content: TerminalContent): HTMLElement {
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    return withScrollback.scrollback.terminalLiveContainer
  }

  async function summon(content: TerminalContent): Promise<void> {
    gridOf(content).dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Enter',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    )
    await vi.waitFor(() => expect(editorOf(content).isVisible).toBe(true))
  }

  function dispatchSummon(content: TerminalContent): KeyboardEvent {
    const event = new KeyboardEvent('keydown', {
      key: 'Enter',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    })
    gridOf(content).dispatchEvent(event)
    return event
  }

  it('rejects capture with CaptureAbortedError before any backend frame is accepted', async () => {
    const { content, teardown } = await mountTerminal(makeClipboard(), { attachToDocument: true })
    try {
      await expect(content.captureLiveFrame()).rejects.toMatchObject({
        name: 'CaptureAbortedError',
        message: 'frame capture aborted: no accepted screen frame is available',
      })
    } finally {
      teardown()
    }
  })

  it('captures the accepted backend screen at its absolute active-buffer rows', async () => {
    const { content, teardown } = await mountTerminal(makeClipboard(), { attachToDocument: true })
    try {
      const rows: CellSpec[][] = [
        [['A', 1, true]],
        ...Array.from({ length: 23 }, () => [] as CellSpec[]),
      ]
      sessionOf(content).fireScreenFrame(
        frameOf(
          9,
          rows,
          { x: 2, y: 0, visible: true },
          { cols: 80, rows: 24 },
          {
            bufferKind: 'normal',
            bufferInstance: '9007199254740993',
            baseRow: 41,
            bracketedPaste: false,
            mouseTracking: false,
            pointerShape: null,
            title: null,
            cwd: null,
          },
        ),
      )

      const captured = await content.captureLiveFrame()
      if (captured.provenance.source !== 'live') throw new Error('expected live capture')
      expect(captured.provenance.range).toEqual({ start: 41, end: 65 })
      expect(captured.provenance.identity.buffer).toEqual({
        kind: 'normal',
        instance: '9007199254740993',
      })
      expect(captured.provenance.identity.generation).toBe(9)
      expect(captured.cursor).toEqual({ line: 41, col: 2 })
      expect(captured.rows[0].kind).toBe('cells')
    } finally {
      teardown()
    }
  })

  it('freezes an alternate-screen paint from row zero', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      fireBufferState(sessionOf(content), 'alternate', 20)
      const capture = vi.fn().mockResolvedValue(frame('top: load 1.00\nTasks: 254 total'))
      content.captureLiveFrame = capture

      await summon(content)

      expect(capture).toHaveBeenCalledTimes(1)
      expect(gridOf(content).querySelector('.nocx-freeze-frame')?.textContent).toMatch(
        /^top: load 1\.00/,
      )
    } finally {
      teardown()
    }
  })

  it('uses the live renderer again after the overlay closes', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      const pinned = frame('pinned top frame')
      const capture = vi.fn().mockResolvedValue(pinned)
      content.captureLiveFrame = capture
      await summon(content)

      const editorWithView = editorOf(content) as unknown as { view: EditorView }
      const view = editorWithView.view
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      expect(content.pinnedFrame()).toBeNull()

      const live = frame('live after next question')
      capture.mockResolvedValue(live)
      const result = await content.captureLiveFrame()
      expect(result).toBe(live)
      expect(capture).toHaveBeenCalledTimes(2)
    } finally {
      teardown()
    }
  })

  it('names the native latch in the alternate buffer and keeps the program untouched', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      const session = sessionOf(content)
      fireBufferState(sessionOf(content), 'alternate', 20)
      content.switchToTerminalInput()
      session.send.mockClear()
      session.sendResize.mockClear()
      vi.mocked(showToast).mockClear()

      const event = dispatchSummon(content)
      await Promise.resolve()

      expect(event.defaultPrevented).toBe(true)
      expect(showToast).toHaveBeenCalledWith({
        level: 'warning',
        message: 'Native input is active — enable command editor to ask the assistant.',
      })
      expect(ed.isVisible).toBe(false)
      expect(session.send).not.toHaveBeenCalled()
      expect(session.sendResize).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('says when the pane has no assistant target', async () => {
    const client = makeClient()
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      ;(content as unknown as { agentTarget: null }).agentTarget = null
      const session = sessionOf(content)
      session.send.mockClear()
      vi.mocked(showToast).mockClear()

      const event = dispatchSummon(content)
      await Promise.resolve()

      expect(event.defaultPrevented).toBe(true)
      expect(showToast).toHaveBeenCalledWith({
        level: 'warning',
        message: 'This pane has no assistant.',
      })
      expect(ed.isVisible).toBe(false)
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('keeps the two benign refusals silent', async () => {
    const client = makeClient()
    const first = await mountTerminal(makeClipboard(), { attachToDocument: true }, client)
    try {
      first.content.setVisible(true)
      startRunning(client)
      first.ed.show()
      vi.mocked(showToast).mockClear()
      dispatchSummon(first.content)
      await Promise.resolve()
      expect(showToast).not.toHaveBeenCalled()
    } finally {
      first.teardown()
    }

    const secondClient = makeClient()
    const second = await mountTerminal(makeClipboard(), { attachToDocument: true }, secondClient)
    try {
      second.content.setVisible(true)
      vi.mocked(showToast).mockClear()
      dispatchSummon(second.content)
      await Promise.resolve()
      expect(showToast).not.toHaveBeenCalled()
    } finally {
      second.teardown()
    }
  })
})
describe('summoned answers return one composer and take ordered seats (nocx-7l4ex.4/.8)', () => {
  const commandFence = 'd'.repeat(64)
  const firstCallFence = 'e'.repeat(64)
  const secondCallFence = 'f'.repeat(64)
  const editorOwnsKeyboard = (content: TerminalContent): boolean =>
    editorOf(content).root.contains(document.activeElement)

  function startCommand(client: ClientFake): (params: unknown) => void {
    const handler = lifecycleHandler(client)
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: {
        id: 'att-run',
        state: 'open',
        origin: 'app',
        submitId: submitToken(client),
        command: 'top',
      },
    })
    return handler
  }

  function dispatchSummon(content: TerminalContent): KeyboardEvent {
    const grid = (content as unknown as { scrollback: ScrollbackController }).scrollback
      .terminalLiveContainer
    const event = new KeyboardEvent('keydown', {
      key: 'Enter',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    })
    grid.dispatchEvent(event)
    return event
  }

  async function summon(content: TerminalContent): Promise<void> {
    // The accepted backend frame is the authority for the live-screen
    // identity used by capture. No renderer dimensions or parser are involved.
    const frame = screenFrame(1, [])
    sessionOf(content).fireScreenFrame(frame)
    content.captureLiveFrame = vi.fn().mockResolvedValue({
      rows: [],
      cursor: { line: frame.cursor.y, col: frame.cursor.x },
      provenance: {
        source: 'live',
        identity: {
          buffer: { kind: 'normal' },
          cols: frame.geometry.cols,
          rows: frame.geometry.rows,
          generation: 0,
        },
        range: { start: 0, end: 0 },
        scrollbackCapLines: 10000,
      },
    } satisfies CapturedFrame)
    const grid = (content as unknown as { scrollback: ScrollbackController }).scrollback
      .terminalLiveContainer
    grid.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Enter',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      }),
    )
    await vi.waitFor(() => expect(editorOf(content).isVisible).toBe(true))
  }

  async function submitQuestion(
    content: TerminalContent,
    client: ClientFake,
    question: string,
  ): Promise<HTMLElement> {
    const editor = editorOf(content)
    const askCount = client.dispatcher.call.mock.calls.filter(
      ([method]) => method === 'agent.ask',
    ).length
    editor.insertText(question)
    viewOf(editor).contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    await vi.waitFor(() =>
      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.ask'),
      ).toHaveLength(askCount + 1),
    )
    const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
    const answer = Array.from(
      pane.querySelectorAll<HTMLElement>('.cmd-block[data-block-kind="ask"]'),
    ).find((candidate) => candidate.querySelector('.cmd-header-text')?.textContent === question)
    expect(answer).toBeDefined()
    return answer!
  }

  it('streams readable prose in the summoned overlay', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'what is on screen?')

      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(delta).toBeDefined()
      delta!({ runId: 42, entryId: 'entry-42', text: '## The answer\n' })

      expect(answer.classList.contains('nocx-answer-overlay')).toBe(true)
      expect(answer.querySelector('.cmd-header-text')?.textContent).toBe('what is on screen?')
      expect(answer.querySelector('[data-answer-body]')).not.toBeNull()
      expect(editorOf(content).isVisible).toBe(false)
      expect(answer.querySelector('[data-md="h2"]')?.textContent).toBe('The answer')
      expect(answer.dataset.entryId).toBe('entry-42')

      const askCall = client.dispatcher.call.mock.calls.find(([method]) => method === 'agent.ask')
      const askCalls = client.dispatcher.call.mock.calls.filter(
        ([method]) => method === 'agent.ask',
      )
      expect(askCalls).toHaveLength(1)
      const askParams = askCall?.[1] as { askId?: unknown; question?: unknown }
      expect(typeof askParams.askId).toBe('string')
      expect(askParams.question).toBe('what is on screen?')
      expect(askCall?.[1]).not.toHaveProperty('placement')

      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      content.dispose()
      expect(answer.isConnected).toBe(false)
      expect(pane.querySelector('.nocx-summon-stack')).toBeNull()
    } finally {
      teardown()
    }
  })

  /** A browser-shaped scroller over a jsdom element: `scrollTop` is clamped
   *  on both read and write the way a real one is, `scrollHeight` follows the
   *  text the mutation actually appends, and `clientHeight` is ours to
   *  collapse — which is what a nested absolute surface measured mid-layout
   *  hands the code (nocx-yfpxl). */
  function stubScroller(
    el: HTMLElement,
    viewport: number,
  ): { collapse: () => void; restore: () => void } {
    let clientHeight = viewport
    let stored = 0
    const scrollHeight = (): number => 400 + (el.textContent?.length ?? 0)
    const max = (): number => Math.max(0, scrollHeight() - clientHeight)
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => clientHeight })
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: scrollHeight })
    Object.defineProperty(el, 'scrollTop', {
      configurable: true,
      get: () => Math.min(stored, max()),
      set: (value: number) => {
        stored = Math.max(0, Math.min(value, max()))
      },
    })
    // Where the open path leaves it: appending the turn ends with
    // `answers.scrollTop = answers.scrollHeight`, so the list starts at its
    // tail and every move from here belongs to the code under test.
    el.scrollTop = el.scrollHeight
    return {
      collapse: () => {
        clientHeight = 0
      },
      restore: () => {
        clientHeight = viewport
      },
    }
  }

  it('keeps following its tail when the list measures zero mid-mutation (nocx-yfpxl)', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      await submitQuestion(content, client, 'what is on screen?')
      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      const list = pane.querySelector<HTMLElement>('.nocx-summon-answers')
      expect(list).not.toBeNull()
      const scroller = stubScroller(list!, 120)
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(delta).toBeDefined()

      // The person is following: the first chunk leaves the list at its tail.
      delta!({ runId: 42, entryId: 'entry-42', text: `${'a'.repeat(200)}\n` })
      expect(list!.scrollTop + list!.clientHeight).toBeGreaterThanOrEqual(list!.scrollHeight - 2)

      // The freeze/thaw transition measures the nested surface mid-layout, so
      // the list reports a zero viewport for the length of one mutation. That
      // is not the person scrolling away, and the follow must survive it.
      scroller.collapse()
      delta!({ runId: 42, entryId: 'entry-42', text: `${'b'.repeat(400)}\n` })

      // Thawed: the list can be measured again, and the tail is where the
      // person left it.
      scroller.restore()
      expect(list!.scrollTop + list!.clientHeight).toBeGreaterThanOrEqual(list!.scrollHeight - 2)
    } finally {
      teardown()
    }
  })

  it('returns the same composer for ordered follow-ups and seats each answer once', async () => {
    const client = makeClient()
    let nextRunId = 0
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        const runId = ++nextRunId
        return Promise.resolve({ runId, entryId: `entry-${runId}`, model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      const editorRoot = ed.root
      await summon(content)
      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      const session = sessionOf(content)
      session.send.mockClear()
      const resizeCalls = session.sendResize.mock.calls.length

      const first = await submitQuestion(content, client, 'what is the first state?')
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(delta).toBeDefined()
      expect(state).toBeDefined()
      delta!({ runId: 1, entryId: 'entry-1', text: 'first answer' })
      expect(ed.isVisible).toBe(false)
      const repeatedSummon = dispatchSummon(content)
      await Promise.resolve()
      expect(repeatedSummon.defaultPrevented).toBe(true)
      expect((content.captureLiveFrame as ReturnType<typeof vi.fn>).mock.calls).toHaveLength(1)
      expect(pane.querySelectorAll('.nocx-freeze-frame')).toHaveLength(1)
      state!({
        runId: 1,
        entryId: 'entry-1',
        state: 'failed',
        error: 'test terminal failure',
        droppedDeltas: 0,
      })

      const stack = pane.querySelector<HTMLElement>('.nocx-summon-stack')
      const answerList = stack?.querySelector<HTMLElement>('.nocx-summon-answers')
      expect(stack).not.toBeNull()
      expect(answerList).not.toBeNull()
      expect(editorOf(content)).toBe(ed)
      expect(ed.root).toBe(editorRoot)
      expect(ed.isVisible).toBe(true)
      expect(first.parentElement).toBe(answerList)
      expect(ed.root.parentElement).toBe(stack)
      // The stack's seats, in order: the draggable seam between the frozen
      // screen and the assistant, the answers, then the one composer.
      const seam = stack?.querySelector<HTMLElement>('.nocx-summon-seam')
      expect(seam).not.toBeNull()
      expect(Array.from(stack!.children)).toEqual([seam, answerList, editorRoot])
      expect(editorOwnsKeyboard(content)).toBe(true)
      expect(session.send).not.toHaveBeenCalled()
      expect(session.sendResize).toHaveBeenCalledTimes(resizeCalls)

      session.send.mockClear()
      const followupResizeCalls = session.sendResize.mock.calls.length
      const second = await submitQuestion(content, client, 'what changed next?')
      delta!({ runId: 2, entryId: 'entry-2', text: 'second answer' })
      expect(ed.isVisible).toBe(false)
      state!({ runId: 2, entryId: 'entry-2', state: 'completed', droppedDeltas: 0 })

      expect(ed.root).toBe(editorRoot)
      expect(ed.isVisible).toBe(true)
      expect(editorOwnsKeyboard(content)).toBe(true)
      expect(Array.from(answerList!.children)).toEqual([first, second])
      expect(first.textContent).toContain('first answer')
      expect(second.textContent).toContain('second answer')
      expect(session.send).not.toHaveBeenCalled()
      expect(session.sendResize).toHaveBeenCalledTimes(followupResizeCalls)
      expect(session.sendResize).toHaveBeenCalledTimes(resizeCalls)

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'completed',
          exitCode: 0,
          fence: commandFence,
          completedAt: '2026-08-28T12:00:00Z',
        },
      })
      blockClosedHandler(client)('att-run')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })

      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      const command = inner.querySelector<HTMLElement>('.cmd-block[data-block-kind="command"]')
      const seated = Array.from(
        inner.querySelectorAll<HTMLElement>('.cmd-block[data-block-kind="ask"]'),
      )
      const children = Array.from(inner.children)
      expect(seated).toEqual([first, second])
      expect(children.indexOf(command!)).toBeLessThan(children.indexOf(first))
      expect(children.indexOf(first)).toBeLessThan(children.indexOf(second))
      expect(first.parentElement).toBe(inner)
      expect(second.parentElement).toBe(inner)
      expect(pane.querySelector('.nocx-summon-stack')).toBeNull()
      expect(ed.root.parentElement).toBe(pane)
      expect(session.send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
  it('keeps one composer unavailable across every call in an active turn', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const recorded = historyRecordedHandler(client)
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'what happens across calls?')
      expect(ed.isVisible).toBe(false)

      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(state).toBeDefined()

      // The host command finishes while the assistant turn still owns the
      // answer. Its intermediate lifecycle result must not return the
      // keyboard to the composer.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'completed',
          exitCode: 0,
          fence: commandFence,
          completedAt: '2026-08-31T12:00:00Z',
        },
      })
      blockClosedHandler(client)('att-run')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(false)

      // Two assistant tool calls share the same turn. Each call opens and
      // closes its own command block, but neither intermediate result owns the
      // composer while the answer remains non-terminal.
      const firstCall = content.submitAgentCommand('first call')
      await vi.waitFor(() =>
        expect(
          client.dispatcher.call.mock.calls.filter(
            ([method]) => method === 'lifecycle.submitAttempt',
          ),
        ).toHaveLength(1),
      )
      await Promise.resolve()
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-first',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'first call',
        },
      })
      recorded({
        attemptId: 'att-first',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'entry-first',
        source: 'user',
        redactions: [],
        maskedCommand: 'first call',
        captures: [],
      })
      expect(ed.isVisible).toBe(false)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-first',
          state: 'completed',
          exitCode: 0,
          fence: firstCallFence,
          completedAt: '2026-08-31T12:00:01Z',
        },
      })
      blockClosedHandler(client)('att-first')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(false)
      await expect(firstCall).resolves.toEqual(expect.objectContaining({ status: 'success' }))

      const secondCall = content.submitAgentCommand('second call')
      await vi.waitFor(() =>
        expect(
          client.dispatcher.call.mock.calls.filter(
            ([method]) => method === 'lifecycle.submitAttempt',
          ),
        ).toHaveLength(2),
      )
      await Promise.resolve()
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-second',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'second call',
        },
      })
      recorded({
        attemptId: 'att-second',
        maskedCount: 0,
        maskedKinds: [],
        entryId: 'entry-second',
        source: 'user',
        redactions: [],
        maskedCommand: 'second call',
        captures: [],
      })
      expect(ed.isVisible).toBe(false)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-second',
          state: 'completed',
          exitCode: 0,
          fence: secondCallFence,
          completedAt: '2026-08-31T12:00:02Z',
        },
      })
      blockClosedHandler(client)('att-second')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(false)
      await expect(secondCall).resolves.toEqual(expect.objectContaining({ status: 'success' }))

      state!({ runId: 42, entryId: 'entry-42', state: 'completed', droppedDeltas: 0 })
      await vi.waitFor(() => expect(answer.isConnected).toBe(true))
      await vi.waitFor(() => expect(ed.isVisible).toBe(true))
    } finally {
      teardown()
    }
  })

  it('keeps overlapping pre-ack answers correlated when the earlier ask is refused', async () => {
    const client = makeClient()
    let rejectFirst!: (error: Error) => void
    let resolveSecond!: (value: { runId: number; entryId: string; model: string }) => void
    const firstAsk = new Promise<never>((_resolve, reject) => {
      rejectFirst = reject
    })
    const secondAsk = new Promise<{ runId: number; entryId: string; model: string }>((resolve) => {
      resolveSecond = resolve
    })
    let asks = 0
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        asks += 1
        return asks === 1 ? firstAsk : secondAsk
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const first = await submitQuestion(content, client, 'the request that will refuse')
      const second = await submitQuestion(content, client, 'the request that remains')

      resolveSecond({ runId: 2, entryId: 'entry-2', model: 'test-model' })
      await vi.waitFor(() => expect(second.dataset.entryId).toBe('entry-2'))
      expect(ed.isVisible).toBe(false)

      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(state).toBeDefined()
      state!({ runId: 2, entryId: 'entry-2', state: 'completed', droppedDeltas: 0 })
      // The older pre-ack turn still owns the composer even though the newer
      // accepted turn terminalized first.
      expect(ed.isVisible).toBe(false)

      rejectFirst(new Error('first ask refused'))
      await vi.waitFor(() => expect(first.isConnected).toBe(false))
      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      const answers = pane.querySelector<HTMLElement>('.nocx-summon-answers')
      expect(answers).not.toBeNull()
      expect(Array.from(answers!.children)).toEqual([second])
      expect(second.isConnected).toBe(true)
      expect(ed.isVisible).toBe(true)
      expect(Array.from(answers!.children)).toEqual([second])
      expect(sessionOf(content).send).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('does not cancel an older turn when the newest answer is still pre-ack', async () => {
    const client = makeClient()
    let resolveFirst!: (value: { runId: number; entryId: string; model: string }) => void
    let rejectSecond!: (error: Error) => void
    const firstAsk = new Promise<{ runId: number; entryId: string; model: string }>((resolve) => {
      resolveFirst = resolve
    })
    const secondAsk = new Promise<never>((_resolve, reject) => {
      rejectSecond = reject
    })
    let asks = 0
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        asks += 1
        return asks === 1 ? firstAsk : secondAsk
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const first = await submitQuestion(content, client, 'older accepted turn')
      await submitQuestion(content, client, 'newer pre-ack turn')
      resolveFirst({ runId: 1, entryId: 'entry-1', model: 'test-model' })
      await vi.waitFor(() => expect(first.dataset.entryId).toBe('entry-1'))

      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      await Promise.resolve()
      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
      ).toHaveLength(0)
      expect(content.pinnedFrame()).toBeNull()

      rejectSecond(new Error('newer ask refused during cleanup'))
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      state?.({ runId: 1, entryId: 'entry-1', state: 'completed', droppedDeltas: 0 })
    } finally {
      teardown()
    }
  })

  it('Escape stops an ordinary answer, with no summon anywhere (nocx-hp8p2.14)', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      const ed = editorOf(content)
      ed.show()
      // Point Enter at Ask through the same chord a person uses. No summon:
      // nothing is frozen and no command is running — this is the ordinary
      // panel, where Escape used to be a dead key.
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          metaKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      await submitQuestion(content, client, 'what is in this directory?')

      // The answer's Stop existed only in its ⋮ menu until now.
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      await Promise.resolve()

      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
      ).toHaveLength(1)
    } finally {
      teardown()
    }
  })

  it('does not resurrect answers after their running command is cleared', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'answer removed with clear')
      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      expect(answer.isConnected).toBe(true)

      scrollback.blockManager.clearAll()
      expect(answer.isConnected).toBe(false)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'completed',
          exitCode: 0,
          fence: commandFence,
          completedAt: '2026-08-28T12:00:00Z',
        },
      })

      expect(answer.isConnected).toBe(false)
      expect(pane.querySelector('.nocx-summon-stack')).toBeNull()
      expect(pane.querySelectorAll('.cmd-block[data-block-kind="ask"]')).toHaveLength(0)
    } finally {
      teardown()
    }
  })

  it('keeps the streaming turn in the overlay until both command and answer end', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'why is it changing?')
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(delta).toBeDefined()
      expect(state).toBeDefined()
      delta!({ runId: 42, entryId: 'entry-42', text: 'before the program ends' })
      const answerBody = answer.querySelector('[data-answer-body]')
      expect(answerBody?.textContent).toContain('before the program ends')

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'completed',
          exitCode: 0,
          fence: commandFence,
          completedAt: '2026-08-27T12:00:00Z',
        },
      })
      blockClosedHandler(client)('att-run')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })

      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      const inner = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .scrollbackInner
      const command = inner.querySelector<HTMLElement>('.cmd-block[data-block-kind="command"]')
      const answerList = pane.querySelector<HTMLElement>('.nocx-summon-answers')
      expect(command).not.toBeNull()
      expect(answerList).not.toBeNull()
      expect(answer.parentElement).toBe(answerList)
      expect(ed.isVisible).toBe(false)

      delta!({ runId: 42, entryId: 'entry-42', text: ' while it ends' })
      expect(answerBody).toBe(answer.querySelector('[data-answer-body]'))
      expect(answerBody?.textContent).toContain('before the program ends while it ends')

      state!({ runId: 42, entryId: 'entry-42', state: 'completed', droppedDeltas: 0 })
      await vi.waitFor(() => expect(answer.parentElement).toBe(inner))
      expect(answer.isConnected).toBe(true)
      expect(Array.from(inner.children).indexOf(command!)).toBeLessThan(
        Array.from(inner.children).indexOf(answer),
      )
    } finally {
      teardown()
    }
  })
  it('corrects the tail after seated growth becomes observable', async () => {
    let resize: (() => void) | null = null
    const originalResizeObserver = globalThis.ResizeObserver
    class DeferredResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        resize = () => callback([], this as unknown as ResizeObserver)
      }

      observe(): void {}

      disconnect(): void {}
    }
    vi.stubGlobal('ResizeObserver', DeferredResizeObserver)

    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    let teardown: (() => void) | undefined
    try {
      const mounted = await mountTerminal(makeClipboard(), { attachToDocument: true }, client)
      teardown = mounted.teardown
      const { content } = mounted
      content.setVisible(true)
      const handler = startCommand(client)
      await summon(content)
      await submitQuestion(content, client, 'how did the tail move?')
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      expect(state).toBeDefined()

      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'completed',
          exitCode: 0,
          fence: commandFence,
          completedAt: '2026-08-31T12:00:00Z',
        },
      })
      blockClosedHandler(client)('att-run')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })

      const area = scrollbackFor(content).scrollbackArea
      let scrollHeight = 900
      let scrollTop = 500
      Object.defineProperty(area, 'clientHeight', { configurable: true, value: 400 })
      Object.defineProperty(area, 'scrollHeight', {
        configurable: true,
        get: () => scrollHeight,
      })
      Object.defineProperty(area, 'scrollTop', {
        configurable: true,
        get: () => scrollTop,
        set: (value: number) => {
          scrollTop = value
        },
      })
      const scrollTo = vi.fn((options: { top: number }) => {
        scrollTop = options.top
      })
      Object.defineProperty(area, 'scrollTo', { configurable: true, value: scrollTo })
      scrollbackFor(content).scrollToBottom()
      scrollTop = 500
      scrollTo.mockClear()

      state!({ runId: 42, entryId: 'entry-42', state: 'completed', droppedDeltas: 0 })
      expect(scrollTo).toHaveBeenCalledWith({ top: 900, behavior: 'instant' })
      expect(resize).toBeDefined()

      // Seating grows the scroller after the first read. The resize callback
      // is the observable completion of that layout change, not a timer.
      scrollHeight = 1400
      resize!()
      expect(scrollTo).toHaveBeenLastCalledWith({ top: 1400, behavior: 'instant' })
    } finally {
      vi.stubGlobal('ResizeObserver', originalResizeObserver)
      teardown?.()
    }
  })

  it('Escape seats the answer in scrollback before the running program resumes', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      if (method === 'agent.cancel')
        return Promise.resolve({ runId: 42, state: 'cancelled', cancelled: true })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'remove this overlay')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-42'))
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      delta!({ runId: 42, entryId: 'entry-42', text: 'overlay prose' })

      const pane = answer.parentElement?.parentElement?.parentElement
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback

      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )

      expect(answer.isConnected).toBe(true)
      expect(answer.parentElement).toBe(scrollback.scrollbackInner)
      expect(answer.classList.contains('nocx-answer-overlay')).toBe(false)
      expect(pane?.querySelector('.nocx-summon-stack')).toBeNull()
      expect(content.pinnedFrame()).toBeNull()
      expect(content.presentation).toBe('terminal')
    } finally {
      teardown()
    }
  })

  // nocx-yfpxl. The e2e's own assertion is about the OUTER scroller: after
  // the thaw it polls `.scrollback-area` for atBottom and for the seated
  // answer's tail being inside it, and on WebKit both were false for the
  // whole five-second window while `seated` was true. So the answer took its
  // seat below the fold and nothing ever moved the pane to it.
  it('seats the answer at the live end even when the outer sentinel went stale (nocx-yfpxl)', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      if (method === 'agent.cancel')
        return Promise.resolve({ runId: 42, state: 'cancelled', cancelled: true })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'what is on screen?')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-42'))
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      delta!({ runId: 42, entryId: 'entry-42', text: 'a long streamed answer' })

      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      const area = scrollback.scrollbackArea
      // jsdom lays nothing out: name a viewport and a transcript taller than
      // it, which is the state the assertion is made in.
      Object.defineProperty(area, 'clientHeight', { configurable: true, value: 600 })
      Object.defineProperty(area, 'scrollHeight', { configurable: true, value: 4000 })
      const scrollTo = vi.fn()
      Object.defineProperty(area, 'scrollTo', { configurable: true, value: scrollTo })
      // The outer follow sentinel reports the reader away from the live end
      // while the overlay owns the pane. They scrolled nothing — their eyes
      // are on the answer list, and that list is at its own tail.
      ;(
        scrollback as unknown as {
          _tail: { observe: (el: Element, following: boolean) => void }
        }
      )._tail.observe(area, false)

      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )

      expect(answer.parentElement).toBe(scrollback.scrollbackInner)
      expect(scrollTo).toHaveBeenCalledWith({ top: 4000, behavior: 'instant' })
    } finally {
      teardown()
    }
  })

  // nocx-yfpxl, and the measurement is nocx-nleo1's: across four container
  // runs the follow sentinel ALWAYS intersected the scrollback area, so
  // following is not switched off by the full-screen program — and yet
  // atBottom read false with 57px of overflow at scrollTop 0, and the sample
  // differed in three of four identical runs. That is a scroll issued against
  // a scrollHeight that has not grown yet: it lands short, and nothing after
  // it moves the pane again.
  it('holds the seated tail through the growth that lands after the scroll (nocx-yfpxl)', async () => {
    const deliveries: Array<{ el: Element; deliver: (height: number) => void }> = []
    vi.stubGlobal(
      'ResizeObserver',
      class {
        private disconnected = false

        constructor(private readonly callback: ResizeObserverCallback) {}

        observe(el: Element): void {
          deliveries.push({
            el,
            deliver: (height: number) => {
              // A disconnected observer receives nothing, which is the whole
              // point of the test: the old code stopped listening after one
              // delivery.
              if (this.disconnected) return
              this.callback(
                [{ target: el, contentRect: { height } } as unknown as ResizeObserverEntry],
                this,
              )
            },
          })
        }

        unobserve(): void {}

        disconnect(): void {
          this.disconnected = true
        }
      },
    )
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      if (method === 'agent.cancel')
        return Promise.resolve({ runId: 42, state: 'cancelled', cancelled: true })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'what is on screen?')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-42'))
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      delta!({ runId: 42, entryId: 'entry-42', text: 'a long streamed answer' })

      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      const area = scrollback.scrollbackArea
      // A previous transcript already overflows, and the old scroller is
      // genuinely at its tail. Its first ResizeObserver delivery is still a
      // pre-growth baseline; that stale tail measurement must not release the
      // hold before the seated answer's own box grows.
      let scrollHeight = 900
      let scrollTop = 500
      Object.defineProperty(area, 'clientHeight', { configurable: true, value: 400 })
      Object.defineProperty(area, 'scrollHeight', { configurable: true, get: () => scrollHeight })
      // A scroller clamps a write AT THE MOMENT OF THE WRITE and keeps the
      // clamped value — which is exactly why a scroll issued before the
      // content grew is lost rather than merely early.
      const clamp = (value: number): number =>
        Math.max(0, Math.min(value, Math.max(0, scrollHeight - area.clientHeight)))
      Object.defineProperty(area, 'scrollTop', {
        configurable: true,
        get: () => scrollTop,
        set: (value: number) => {
          scrollTop = clamp(value)
        },
      })
      Object.defineProperty(area, 'scrollTo', {
        configurable: true,
        value: (options: { top: number }) => {
          scrollTop = clamp(options.top)
        },
      })

      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      expect(answer.parentElement).toBe(scrollback.scrollbackInner)

      const seated = deliveries.find((entry) => entry.el === answer)
      expect(seated).toBeDefined()
      // The delivery that arrives while the node still has its overlay box —
      // the moment the old code took for proof and stopped listening.
      seated!.deliver(120)
      // And the growth, one layout later: the seated answer owns its box and
      // the transcript is finally taller than the viewport.
      scrollHeight = 946
      seated!.deliver(230)

      expect(area.scrollTop + area.clientHeight).toBeGreaterThanOrEqual(area.scrollHeight - 2)

      // AND IT LETS GO. The command is still running and the answer may still
      // be streaming; a hold that outlived the seat would be a second owner of
      // the scroll position, and the reader who scrolls up to reread would be
      // dragged back on every chunk.
      scrollHeight = 1200
      area.scrollTop = 0
      seated!.deliver(400)
      expect(area.scrollTop).toBe(0)
    } finally {
      teardown()
      vi.unstubAllGlobals()
    }
  })

  // nocx-hp8p2.8. The live region is where a RUNNING command's output is —
  // the block holds only its header until the freeze — so an answer seated
  // after the block element alone reads as spliced into the middle of `top`.
  // The ordering has to hold in both directions at once: the answer below
  // the running output, and the NEXT command below the answer.
  it('seats an answer below the running command output, and opens the next command below the answer (nocx-hp8p2.8)', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      if (method === 'agent.cancel')
        return Promise.resolve({ runId: 42, state: 'cancelled', cancelled: true })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      const handler = startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'what is on screen?')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-42'))
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      delta!({ runId: 42, entryId: 'entry-42', text: 'overlay prose' })

      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      const inner = scrollback.scrollbackInner
      const live = scrollback.terminalLiveContainer

      // Escape while `top` is STILL running.
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )

      const command = inner.querySelector<HTMLElement>('.cmd-block[data-block-kind="command"]')
      expect(command).not.toBeNull()
      // THE ANSWER GOES BELOW THE COMMAND, AND THE COMMAND'S OWN OUTPUT IS IN
      // THE COMMAND (ADR-0091): the live region is that block's body while it
      // runs, so it is not a child of the stack to be ordered against — the
      // block and everything it holds is one entry.
      const seatedOrder = (): number[] => {
        const kids = Array.from(inner.children)
        return [kids.indexOf(command!), kids.indexOf(answer)]
      }
      const [cmdAt, answerAt] = seatedOrder()
      expect(cmdAt).toBeGreaterThanOrEqual(0)
      expect(cmdAt).toBeLessThan(answerAt)
      expect(live.parentElement).toBe(command)

      // The order a person watched during the run is the order they keep.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run',
          state: 'completed',
          exitCode: 0,
          fence: commandFence,
          completedAt: '2026-08-28T12:00:00Z',
        },
      })
      blockClosedHandler(client)('att-run')
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      const frozen = inner.querySelector<HTMLElement>('.cmd-block[data-block-kind="command"]')
      const afterFreeze = Array.from(inner.children)
      expect(afterFreeze.indexOf(frozen!)).toBeLessThan(afterFreeze.indexOf(answer))

      // And the next command opens BELOW the answer, which is what makes
      // "seat after the live container" wrong on its own.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-run-2',
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: 'ls',
        },
      })
      const commands = Array.from(
        inner.querySelectorAll<HTMLElement>('.cmd-block[data-block-kind="command"]'),
      )
      const next = commands[commands.length - 1]
      expect(next).not.toBe(frozen)
      const kids = Array.from(inner.children)
      expect(kids.indexOf(answer)).toBeLessThan(kids.indexOf(next))
      // And the live region is THAT command's body, not a stack child to be
      // ordered against: one block, header to its last live row (ADR-0091).
      expect(live.parentElement).toBe(next)
    } finally {
      teardown()
    }
  })

  // nocx-hp8p2.7. The frozen screen became scrollable in nocx-hp8p2.6, which
  // enabled a selection gesture that led nowhere: the frame is not a
  // .cmd-block, so no mark was ever built and the chip counted zero.
  it('marks rows selected inside the frozen screen onto the attachment itself (nocx-hp8p2.7)', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      const session = sessionOf(content)
      const acceptedFrame = screenFrame(20, ['load 1.00', 'PID USER', 'nocx 0.1', 'idle'])
      session.fireScreenFrame(acceptedFrame)
      content.captureLiveFrame = vi.fn().mockResolvedValue({
        rows: ['load 1.00', 'PID USER', 'nocx 0.1', 'idle'].map((text) => ({
          kind: 'text' as const,
          text,
        })),
        cursor: { line: 0, col: 0 },
        provenance: {
          source: 'live',
          identity: {
            buffer: { kind: 'normal' },
            cols: acceptedFrame.geometry.cols,
            rows: acceptedFrame.geometry.rows,
            generation: 0,
          },
          range: { start: 0, end: 4 },
          scrollbackCapLines: 10000,
        },
      } satisfies CapturedFrame)
      const grid = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .terminalLiveContainer
      grid.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      await vi.waitFor(() => expect(editorOf(content).isVisible).toBe(true))
      await vi.waitFor(() =>
        expect(document.querySelector<HTMLElement>('.nocx-freeze-frame')).not.toBeNull(),
      )

      const freezeFrame = document.querySelector<HTMLElement>('.nocx-freeze-frame')
      expect(freezeFrame).not.toBeNull()
      const rows = Array.from(freezeFrame!.querySelectorAll<HTMLElement>('.nocx-freeze-frame__row'))
      expect(rows).toHaveLength(4)

      // Select the middle band and finish the gesture, the way a scrollback
      // selection reaches the same handler.
      const range = document.createRange()
      range.setStart(rows[1].firstChild!, 0)
      range.setEnd(rows[2].firstChild!, rows[2].textContent.length)
      range.getClientRects = () =>
        [new DOMRect(100, 220, 200, 20), new DOMRect(100, 240, 200, 20)] as unknown as DOMRectList
      const selection = window.getSelection()!
      selection.removeAllRanges()
      selection.addRange(range)
      document.dispatchEvent(new Event('selectionchange'))

      await vi.waitFor(() =>
        expect(
          document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button'),
        ).not.toBeNull(),
      )
      const affordance = document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')
      expect(affordance).not.toBeNull()
      affordance!.click()

      await vi.waitFor(() =>
        expect(
          document.querySelector<HTMLElement>('[data-control="grant"]')?.textContent,
        ).toContain('· 1'),
      )

      // Counted as a person mark, and the rows are painted as granted.
      const chip = document.querySelector<HTMLElement>('[data-control="grant"]')
      expect(chip?.textContent).toContain('· 1')
      expect(rows[1].dataset.granted).toBe('true')
      expect(rows[2].dataset.granted).toBe('true')
      expect(rows[0].dataset.granted).toBeUndefined()
      expect(rows[3].dataset.granted).toBeUndefined()

      // ONE item on the wire: the attachment's own id, from the accepted backend frame,
      // now carrying the band.
      await submitQuestion(content, client, 'what are these two lines?')
      const ask = client.dispatcher.call.mock.calls.find(([method]) => method === 'agent.ask')
      const attached = (ask?.[1] as { attachedContent: Record<string, unknown>[] }).attachedContent
      expect(attached).toHaveLength(1)
      expect(attached[0]).toMatchObject({ start: 1, count: 2, automatic: true })

      // And dismissing the row mark leaves the whole screen attached: the
      // person took back the narrowing, not the attachment.
      chip!.click()
      document.querySelector<HTMLButtonElement>('[data-action="dismiss-grant"]')!.click()
      expect(chip?.textContent).toContain('· 0')
      expect(chip?.textContent).toContain('+ screen')
      expect(rows[1].dataset.granted).toBeUndefined()
      await submitQuestion(content, client, 'and the whole screen?')
      const second = client.dispatcher.call.mock.calls.filter(
        ([method]) => method === 'agent.ask',
      )[1]
      const secondAttached = (second?.[1] as { attachedContent: Record<string, unknown>[] })
        .attachedContent
      expect(secondAttached).toHaveLength(1)
      expect(secondAttached[0]).toMatchObject({ automatic: true })
      expect(secondAttached[0]).not.toHaveProperty('start')
      expect(secondAttached[0]).not.toHaveProperty('count')
    } finally {
      teardown()
    }
  })

  it('Escape cancellation seats a stopped answer, never failed, after runState', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      if (method === 'agent.cancel')
        return Promise.resolve({ runId: 42, state: 'cancelled', cancelled: true })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'stop only this answer')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-42'))
      const delta = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runDelta',
      )?.[1] as ((params: unknown) => void) | undefined
      delta!({ runId: 42, entryId: 'entry-42', text: 'partial prose survives' })

      const escape = (): KeyboardEvent => {
        const event = new KeyboardEvent('keydown', {
          key: 'Escape',
          bubbles: true,
          cancelable: true,
        })
        document.body.dispatchEvent(event)
        return event
      }
      expect(escape().defaultPrevented).toBe(true)
      escape()

      await vi.waitFor(() =>
        expect(
          client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
        ).toEqual([['agent.cancel', { runId: 42 }]]),
      )
      expect(sessionOf(content).signal).not.toHaveBeenCalled()
      expect(answer.isConnected).toBe(true)
      expect(answer.parentElement).toBe(scrollbackFor(content).scrollbackInner)
      expect(answer.classList.contains('nocx-answer-overlay')).toBe(false)
      expect(scrollbackFor(content).blockManager.runningBlock).not.toBeNull()
      expect(content.pinnedFrame()).toBeNull()
      expect(editorOf(content).isVisible).toBe(false)

      await vi.waitFor(() =>
        expect(
          answer.querySelector(
            ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])',
          )?.textContent,
        ).toBe('stopped'),
      )
      expect(answer.dataset.turnState).toBe('cancelled')
      expect(answer.dataset.outcome).toBe('cancelled')
      expect(answer.querySelector('[data-answer-body]')?.textContent).toContain(
        'partial prose survives',
      )

      // The notification is allowed to arrive after the reserved cancellation
      // response; it must be idempotent and cannot replace the stopped word.
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      state!({ runId: 42, entryId: 'entry-42', state: 'cancelled', droppedDeltas: 0 })
      expect(answer.dataset.outcome).not.toBe('failure')
      expect(answer.dataset.outcome).toBe('cancelled')
      expect(
        answer.querySelector(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')
          ?.textContent,
      ).toBe('stopped')
    } finally {
      teardown()
    }
  })

  it('shows pre-execution submission failure in the answer block, not as terminalization', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask') {
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      }
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'run the deployment')
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      state!({
        runId: 42,
        entryId: 'entry-42',
        state: 'failed',
        error: 'submission expired before execution',
      })

      expect(answer.querySelector('.cmd-answer-error')?.textContent).toBe(
        'submission expired before execution',
      )
      expect(answer.querySelector('.cmd-answer-error')?.textContent).not.toContain('terminal')
    } finally {
      teardown()
    }
  })

  it('leaving the alternate buffer seats the answer and clears the frozen frame', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      fireBufferState(sessionOf(content), 'alternate', 20)
      await summon(content)
      const answer = await submitQuestion(content, client, 'leave when the program thaws')
      const pane = answer.closest<HTMLElement>('.pane')
      expect(pane).not.toBeNull()
      const paneElement = pane!
      expect(answer.isConnected).toBe(true)
      expect(paneElement.querySelector('.nocx-freeze-frame')).not.toBeNull()

      fireBufferState(sessionOf(content), 'normal', 21)

      expect(answer.isConnected).toBe(true)
      expect(answer.parentElement).toBe(
        (content as unknown as { scrollback: ScrollbackController }).scrollback.scrollbackInner,
      )
      expect(answer.classList.contains('nocx-answer-overlay')).toBe(false)
      expect(paneElement.querySelector('.nocx-summon-stack')).toBeNull()
      expect(paneElement.querySelector('.nocx-freeze-frame')).toBeNull()
      expect(content.pinnedFrame()).toBeNull()
      expect(editorOf(content).isVisible).toBe(false)
    } finally {
      teardown()
    }
  })

  it('tab change seats the answer before the pane hides', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'leave when the tab changes')
      const pane = answer.closest<HTMLElement>('.pane')
      expect(pane).not.toBeNull()
      const paneElement = pane!
      expect(answer.isConnected).toBe(true)
      expect(paneElement.querySelector('.nocx-freeze-frame')).not.toBeNull()

      content.setVisible(false)

      expect(answer.isConnected).toBe(true)
      expect(answer.parentElement).toBe(
        (content as unknown as { scrollback: ScrollbackController }).scrollback.scrollbackInner,
      )
      expect(answer.classList.contains('nocx-answer-overlay')).toBe(false)
      expect(paneElement.querySelector('.nocx-summon-stack')).toBeNull()
      expect(paneElement.querySelector('.nocx-freeze-frame')).toBeNull()
      expect(content.pinnedFrame()).toBeNull()
      expect(editorOf(content).isVisible).toBe(false)
    } finally {
      teardown()
    }
  })

  it('a streaming answer leaves Escape with text controls, overlays, and the block menu', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 43, entryId: 'entry-43', model: 'test-model' })
      if (method === 'agent.cancel')
        return Promise.resolve({ runId: 43, state: 'cancelled', cancelled: true })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const input = document.createElement('input')
    const menu = document.createElement('div')
    menu.className = 'ui-context-menu'
    let overlayClosed = false
    let overlay: ReturnType<typeof pushOverlay> | null = null
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'owners keep Escape')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-43'))
      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget
      pane.append(input)

      input.focus()
      input.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      input.blur()

      overlay = pushOverlay(() => {
        overlayClosed = true
        return true
      })
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      expect(overlayClosed).toBe(true)
      popOverlay(overlay)
      overlay = null

      document.body.append(menu)
      document.body.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )

      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
      ).toHaveLength(0)
      expect(content.pinnedFrame()).not.toBeNull()
    } finally {
      if (overlay !== null) popOverlay(overlay)
      input.remove()
      menu.remove()
      teardown()
    }
  })

  it('Ctrl+C keeps one command owner across chrome, grid, selection, and text-control focus while the turn streams', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 44, entryId: 'entry-44', model: 'test-model' })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const key = (target: HTMLElement): void => {
      target.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          code: 'KeyC',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
    }
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'do not stop this turn')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-44'))
      const session = sessionOf(content)
      session.signal.mockClear()
      const pane = (content as unknown as { _paneTarget: HTMLElement })._paneTarget

      const chrome = document.createElement('button')
      pane.append(chrome)
      chrome.focus()
      key(chrome)
      expect(session.signal.mock.calls.map((call: unknown[]) => call[0])).toEqual(['interrupt'])

      session.signal.mockClear()
      const gridInput = document.createElement('textarea')
      ;(
        content as unknown as { scrollback: ScrollbackController }
      ).scrollback.terminalLiveContainer.append(gridInput)
      gridInput.focus()
      key(gridInput)
      expect(session.signal).not.toHaveBeenCalled()

      const otherInput = document.createElement('input')
      pane.append(otherInput)
      otherInput.focus()
      key(otherInput)
      expect(session.signal).not.toHaveBeenCalled()

      const selected = document.createElement('span')
      const selectedText = document.createTextNode('copy this output')
      selected.append(selectedText)
      ;(
        content as unknown as { scrollback: ScrollbackController }
      ).scrollback.scrollbackArea.append(selected)
      const selectionSpy = vi.spyOn(window, 'getSelection').mockReturnValue({
        anchorNode: selectedText,
        focusNode: selectedText,
        isCollapsed: false,
        toString: () => 'copy this output',
      } as unknown as Selection)
      otherInput.blur()
      try {
        key(document.body)
        expect(session.signal).not.toHaveBeenCalled()
      } finally {
        selectionSpy.mockRestore()
      }
      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
      ).toHaveLength(0)
      expect(content.pinnedFrame()).not.toBeNull()
    } finally {
      window.getSelection()?.removeAllRanges()
      teardown()
    }
  })

  it('Escape on a settled summoned answer keeps dismiss-only behaviour', async () => {
    const client = makeClient()
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method === 'agent.ask')
        return Promise.resolve({ runId: 45, entryId: 'entry-45', model: 'test-model' })
      return Promise.resolve({
        endpointConfigured: true,
        credential: 'resolvable',
        answering: { ready: true, reason: null, endpoint: 'test', model: 'test-model' },
      })
    })
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startCommand(client)
      await summon(content)
      const answer = await submitQuestion(content, client, 'already settled')
      await vi.waitFor(() => expect(answer.dataset.entryId).toBe('entry-45'))
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      state!({ runId: 45, entryId: 'entry-45', state: 'completed', droppedDeltas: 0 })
      await vi.waitFor(() => expect(editorOf(content).isVisible).toBe(true))

      viewOf(editorOf(content)).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )

      expect(
        client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.cancel'),
      ).toHaveLength(0)
      expect(editorOf(content).isVisible).toBe(false)
      expect(content.pinnedFrame()).toBeNull()
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// A reclaimed pane says what is missing (nocx-fz4qa)
// ═══════════════════════════════════════════════════════════════════════════
// The backend already tells a reclaiming client which byte ranges it could
// not give back (session.recoveryStatus's `gaps`, plus the renderer-derived
// `unrecorded` stretch nothing kept), and until this bead nothing read it: a
// pane came back short and said nothing about it. AGENTS.md forbids exactly
// that — a soft degrade must be visible in the product.
//
// These go through the ADOPTION seam a restored tab actually takes
// (TerminalContentHooks.adoptSession), not through the notice module, so
// they can report the wiring being absent.
describe('a reclaimed pane says what is missing (nocx-fz4qa)', () => {
  /** The pane's own reclaim: a session handle carrying what the claim
   *  recovered, handed back by the thunk PaneManager supplies. */
  function reclaiming(recovered: SessionRecovery | null) {
    const session = makeSession({ recovered })
    return {
      session,
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    }
  }

  const cardTitle = (tab: Pane) =>
    tab.pane.querySelector('.nocx-recovery-notice .ui-status-card__title')?.textContent ?? null
  const cardDesc = (tab: Pane) =>
    tab.pane.querySelector('.nocx-recovery-notice .ui-status-card__desc')?.textContent ?? null
  const liveCard = (tab: Pane) => tab.pane.querySelector('[data-notice-kind="live-output-gap"]')
  const liveTitle = (tab: Pane) =>
    liveCard(tab)?.querySelector('.ui-status-card__title')?.textContent ?? null
  const liveDesc = (tab: Pane) =>
    liveCard(tab)?.querySelector('.ui-status-card__desc')?.textContent ?? null
  const fireGap = (session: SessionFake, start: number, end: number, reason = 'hostWindow') =>
    session.fireOutputGap({ sessionId: session.sessionId, start, end, reason })

  it('keeps a screenshot-shaped burst of 32 distinct losses in the same single live card', async () => {
    const { session, hooks } = reclaiming(null)
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    try {
      fireGap(session, 0, 32768)
      const host = liveCard(tab)
      const dismiss = host?.querySelector('button[aria-label="Dismiss"]')
      expect(host).not.toBeNull()
      expect(dismiss).not.toBeNull()
      for (let i = 1; i < 32; i++) {
        fireGap(session, i * 32768, (i + 1) * 32768)
        expect(tab.pane.querySelectorAll('[data-notice-kind="live-output-gap"]')).toHaveLength(1)
        expect(liveCard(tab)).toBe(host)
        expect(liveCard(tab)?.querySelector('button[aria-label="Dismiss"]')).toBe(dismiss)
      }
      expect(liveTitle(tab)).toBe('1048576 bytes of live output are missing')
      expect(tab.pane.querySelectorAll('.nocx-recovery-notice')).toHaveLength(1)
    } finally {
      teardown()
    }
  })

  it('unions overlaps and duplicates without attributing unknown-only loss to the host', async () => {
    const { session, hooks } = reclaiming(null)
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    try {
      fireGap(session, 10, 20, 'future-cause')
      expect(liveTitle(tab)).toBe('10 bytes of live output are missing')
      expect(liveDesc(tab)).not.toContain('execution host')
      const host = liveCard(tab)

      // A different reason on already accounted bytes adds neither bytes nor a cause.
      fireGap(session, 10, 20)
      expect(liveDesc(tab)).not.toContain('execution host')
      fireGap(session, 15, 25)
      fireGap(session, 10, 20, 'future-cause')
      expect(liveTitle(tab)).toBe('15 bytes of live output are missing')
      expect(liveDesc(tab)).toContain('execution host')
      expect(liveDesc(tab)).toContain('unknown cause')
      expect(liveCard(tab)).toBe(host)
      expect(tab.pane.querySelectorAll('[data-notice-kind="live-output-gap"]')).toHaveLength(1)
    } finally {
      teardown()
    }
  })

  it('expires at the original 8000ms deadline even when new bytes arrive at 4000ms', async () => {
    const { session, hooks } = reclaiming(null)
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireGap(session, 10, 20)
      const host = liveCard(tab)
      expect(host).not.toBeNull()
      await vi.advanceTimersByTimeAsync(4000)
      fireGap(session, 15, 25)
      expect(liveTitle(tab)).toBe('15 bytes of live output are missing')
      expect(liveCard(tab)).toBe(host)
      await vi.advanceTimersByTimeAsync(3999)
      fireGap(session, 10, 20)
      expect(liveCard(tab)).toBe(host)
      await vi.advanceTimersByTimeAsync(1)
      expect(liveCard(tab)).toBeNull()

      fireGap(session, 10, 20)
      fireGap(session, 15, 25)
      expect(liveCard(tab)).toBeNull()
      fireGap(session, 30, 35)
      expect(liveTitle(tab)).toBe('20 bytes of live output are missing')
      expect(liveCard(tab)).not.toBe(host)
      expect(tab.pane.querySelectorAll('[data-notice-kind="live-output-gap"]')).toHaveLength(1)
      await vi.advanceTimersByTimeAsync(7999)
      expect(liveCard(tab)).not.toBeNull()
      await vi.advanceTimersByTimeAsync(1)
      expect(liveCard(tab)).toBeNull()
    } finally {
      teardown()
      vi.useRealTimers()
    }
  })

  it('dismisses only the live presentation and rearms a cumulative card only for new loss', async () => {
    const { session, hooks } = reclaiming(null)
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireGap(session, 10, 20)
      fireGap(session, 15, 25)
      expect(liveTitle(tab)).toBe('15 bytes of live output are missing')
      await vi.advanceTimersByTimeAsync(4000)
      liveCard(tab)!.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click()
      expect(liveCard(tab)).toBeNull()
      fireGap(session, 10, 20)
      fireGap(session, 15, 25)
      expect(liveCard(tab)).toBeNull()

      fireGap(session, 30, 35, 'future-cause')
      expect(liveTitle(tab)).toBe('20 bytes of live output are missing')
      expect(liveDesc(tab)).toContain('execution host')
      expect(liveDesc(tab)).toContain('unknown cause')
      const remounted = liveCard(tab)
      // The dismissed card's original deadline must not remove its replacement.
      await vi.advanceTimersByTimeAsync(4000)
      expect(liveCard(tab)).toBe(remounted)
      await vi.advanceTimersByTimeAsync(3999)
      expect(liveCard(tab)).toBe(remounted)
      await vi.advanceTimersByTimeAsync(1)
      expect(liveCard(tab)).toBeNull()
    } finally {
      teardown()
      vi.useRealTimers()
    }
  })

  it('keeps expired live bytes out of later historical recovery revisions', async () => {
    const { session, hooks } = reclaiming({
      produced: 60,
      gaps: [
        { start: 10, end: 30, reason: 'hostWindow' },
        { start: 50, end: 60, reason: 'cap' },
      ],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireGap(session, 10, 20)
      fireGap(session, 15, 25)
      await vi.advanceTimersByTimeAsync(8000)
      expect(liveCard(tab)).toBeNull()
      session.fireRecoveryChanged({
        produced: 65,
        gaps: [
          { start: 10, end: 30, reason: 'hostWindow' },
          { start: 50, end: 65, reason: 'cap' },
        ],
      })
      expect(tab.pane.querySelectorAll('.nocx-recovery-notice')).toHaveLength(1)
      expect(cardTitle(tab)).toBe("20 B of this session's output is missing")
      expect(cardDesc(tab)).toContain('5 B went missing as "hostWindow"')
      expect(cardDesc(tab)).toContain("the recording's size limit dropped the oldest 15 B")
      fireGap(session, 10, 20)
      expect(liveCard(tab)).toBeNull()
    } finally {
      teardown()
      vi.useRealTimers()
    }
  })

  it.each(['rejected', 'pending'] as const)(
    'expires the live warning at its original deadline while reconnect is %s',
    async (outcome) => {
      const client = makeClient()
      const { content, tab, teardown } = await mountTerminal(
        makeClipboard(),
        { attachToDocument: true },
        client,
      )
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
      let rejectOpening!: (reason: Error) => void
      const opening = new Promise<SessionFake>((_resolve, reject) => {
        rejectOpening = reject
      })
      let reconnect: Promise<boolean> | undefined
      try {
        const session = client._sessions[0]
        fireGap(session, 10, 20)
        const notice = liveCard(tab)
        expect(notice).not.toBeNull()
        await vi.advanceTimersByTimeAsync(4000)
        const loseSession = session.onExit.mock.calls[0][0] as (exit: {
          sessionId: string
          cause: 'interrupted'
        }) => void
        loseSession({ sessionId: session.sessionId, cause: 'interrupted' })
        client.openSession.mockReturnValueOnce(opening)
        reconnect = content.reconnect()
        if (outcome === 'rejected') {
          rejectOpening(new Error('reconnect refused'))
          expect(await reconnect).toBe(false)
        }
        await vi.advanceTimersByTimeAsync(3999)
        expect(liveCard(tab)).toBe(notice)
        await vi.advanceTimersByTimeAsync(1)
        expect(liveCard(tab)).toBeNull()
      } finally {
        rejectOpening(new Error('end pending reconnect'))
        await reconnect
        teardown()
        vi.useRealTimers()
      }
    },
  )

  it('resets the live count and causes on reconnect and ignores the old session and deadline', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const timeout = vi.spyOn(globalThis, 'setTimeout')
    const clearTimeout = vi.spyOn(globalThis, 'clearTimeout')
    try {
      const first = client._sessions[0]
      fireGap(first, 10, 20)
      const deadlineIndex = timeout.mock.calls.findIndex(([, delay]) => delay === 8000)
      expect(deadlineIndex).toBeGreaterThanOrEqual(0)
      const oldDeadline: unknown = timeout.mock.results[deadlineIndex].value
      await vi.advanceTimersByTimeAsync(4000)
      const loseSession = first.onExit.mock.calls[0][0] as (exit: {
        sessionId: string
        cause: 'interrupted'
      }) => void
      loseSession({ sessionId: first.sessionId, cause: 'interrupted' })
      expect(await content.reconnect()).toBe(true)
      expect(clearTimeout).toHaveBeenCalledWith(oldDeadline)
      expect(liveCard(tab)).toBeNull()

      const second = client._sessions[1]
      fireGap(first, 30, 35)
      expect(liveCard(tab)).toBeNull()
      // Reuse old offsets: the ledger as well as the displayed total must reset.
      fireGap(second, 10, 17, 'future-cause')
      expect(liveTitle(tab)).toBe('7 bytes of live output are missing')
      const replacement = liveCard(tab)
      fireGap(first, 40, 45)
      expect(liveTitle(tab)).toBe('7 bytes of live output are missing')
      expect(liveDesc(tab)).not.toContain('execution host')
      await vi.advanceTimersByTimeAsync(4000)
      expect(liveCard(tab)).toBe(replacement)
      await vi.advanceTimersByTimeAsync(3999)
      expect(liveCard(tab)).toBe(replacement)
      await vi.advanceTimersByTimeAsync(1)
      expect(liveCard(tab)).toBeNull()
    } finally {
      teardown()
      timeout.mockRestore()
      clearTimeout.mockRestore()
      vi.useRealTimers()
    }
  })

  it('cancels the live deadline on pane close and refuses later session gaps', async () => {
    const { session, hooks } = reclaiming(null)
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const timeout = vi.spyOn(globalThis, 'setTimeout')
    const clearTimeout = vi.spyOn(globalThis, 'clearTimeout')
    try {
      fireGap(session, 10, 20)
      expect(liveCard(tab)).not.toBeNull()
      const deadlineIndex = timeout.mock.calls.findIndex(([, delay]) => delay === 8000)
      expect(deadlineIndex).toBeGreaterThanOrEqual(0)
      const deadline: unknown = timeout.mock.results[deadlineIndex].value
      tab.close()
      expect(clearTimeout).toHaveBeenCalledWith(deadline)
      expect(liveCard(tab)).toBeNull()
      fireGap(session, 30, 35)
      await vi.advanceTimersByTimeAsync(8000)
      fireGap(session, 40, 45)
      expect(liveCard(tab)).toBeNull()
      expect(document.querySelector('[data-notice-kind="live-output-gap"]')).toBeNull()
    } finally {
      teardown()
      timeout.mockRestore()
      clearTimeout.mockRestore()
      vi.useRealTimers()
    }
  })

  it('aggregates, dismisses and expires live notices independently in two panes', async () => {
    const first = reclaiming(null)
    const second = reclaiming(null)
    const left = await mountTerminal(makeClipboard(), {
      hooks: first.hooks,
      attachToDocument: true,
    })
    const right = await mountTerminal(makeClipboard(), {
      hooks: second.hooks,
      attachToDocument: true,
    })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      fireGap(first.session, 10, 20)
      await vi.advanceTimersByTimeAsync(4000)
      fireGap(second.session, 10, 30, 'future-cause')
      fireGap(first.session, 15, 25)
      expect(liveTitle(left.tab)).toBe('15 bytes of live output are missing')
      expect(liveTitle(right.tab)).toBe('20 bytes of live output are missing')
      expect(liveDesc(right.tab)).not.toContain('execution host')
      const rightHost = liveCard(right.tab)
      liveCard(left.tab)!.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click()
      expect(liveCard(left.tab)).toBeNull()
      expect(liveCard(right.tab)).toBe(rightHost)
      fireGap(first.session, 10, 20)
      expect(liveCard(left.tab)).toBeNull()
      await vi.advanceTimersByTimeAsync(4000)
      expect(liveCard(right.tab)).toBe(rightHost)
      fireGap(first.session, 30, 35)
      expect(liveTitle(left.tab)).toBe('20 bytes of live output are missing')
      await vi.advanceTimersByTimeAsync(4000)
      expect(liveCard(right.tab)).toBeNull()
      expect(liveTitle(left.tab)).toBe('20 bytes of live output are missing')
      await vi.advanceTimersByTimeAsync(4000)
      expect(liveCard(left.tab)).toBeNull()
    } finally {
      left.teardown()
      right.teardown()
      vi.useRealTimers()
    }
  })

  it('tells the user how much of the run is gone and that the size limit took it', async () => {
    const { hooks } = reclaiming({
      produced: 0,
      gaps: [{ start: 0, end: 3_000_000, reason: 'cap' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      await vi.waitFor(() => expect(cardTitle(tab)).not.toBeNull())
      expect(cardTitle(tab)).toContain('3.0 MB')
      expect(cardDesc(tab)).toContain('size limit')
    } finally {
      teardown()
    }
  })

  it('keeps distinct historical cap loss beside a live host-window notice without double-counting', async () => {
    const hostWindow = { start: 2722, end: 98_304, reason: 'hostWindow' }
    const cap = { start: 131_072, end: 4_046_848, reason: 'cap' }
    const { session, hooks } = reclaiming({
      produced: 0,
      gaps: [hostWindow, cap],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      session.fireOutputGap({
        sessionId: session.sessionId,
        ...hostWindow,
      })

      const notices = [...tab.pane.querySelectorAll('.nocx-recovery-notice')]
      expect(notices).toHaveLength(2)
      const cards = notices.map((notice) => ({
        title: notice.querySelector('.ui-status-card__title')?.textContent ?? '',
        description: notice.querySelector('.ui-status-card__desc')?.textContent ?? '',
      }))
      expect(cards.map((card) => card.title)).toContain('95582 bytes of live output are missing')
      expect(cards.map((card) => card.title)).toContain(
        "3.9 MB of this session's output is missing",
      )
      const historical = cards.find((card) => card.title.includes("of this session's output"))!
      expect(historical.description).toContain(
        "the recording's size limit dropped the oldest 3.9 MB",
      )
      expect(historical.description).not.toContain('hostWindow')
    } finally {
      teardown()
    }
  })

  // THE CARD IS NOT A PHOTOGRAPH (nocx-zg3k3.14.24). The reclaim reads
  // session.recoveryStatus once, before it attaches; the recording goes on
  // appending and its retention bound goes on dropping, so that answer is a
  // fact about an instant that has passed. On PR #380's CI the card said
  // 3.1 MB while a fresh session.recoveryStatus said 3.6 MB about the same
  // session, and nothing recomputed it. A person who stays on the pane must
  // see the number reach the ledger's without reopening the tab.
  it('states the recording’s own revised total without reopening the pane (nocx-zg3k3.14.24)', async () => {
    const { session, hooks } = reclaiming({
      produced: 3_400_000,
      gaps: [{ start: 131_072, end: 3_446_848, reason: 'cap' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      await vi.waitFor(() =>
        expect(cardTitle(tab)).toBe("3.3 MB of this session's output is missing"),
      )
      expect(cardDesc(tab)).toContain("the recording's size limit dropped the oldest 3.3 MB")

      // …and the recording drops more while the person is reading it.
      session.fireRecoveryChanged({
        produced: 4_262_996,
        gaps: [{ start: 131_072, end: 4_046_848, reason: 'cap' }],
      })

      await vi.waitFor(() =>
        expect(cardTitle(tab)).toBe("3.9 MB of this session's output is missing"),
      )
      expect(cardDesc(tab)).toContain("the recording's size limit dropped the oldest 3.9 MB")
    } finally {
      teardown()
    }
  })

  it('does not rebuild the card for a revision that states the same total (nocx-zg3k3.14.24)', async () => {
    const { session, hooks } = reclaiming({
      produced: 3_400_000,
      gaps: [{ start: 131_072, end: 3_446_848, reason: 'cap' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      await vi.waitFor(() =>
        expect(cardTitle(tab)).toBe("3.3 MB of this session's output is missing"),
      )
      const mounted = tab.pane.querySelector('.nocx-recovery-notice')
      expect(mounted).not.toBeNull()

      // The recording's FRONTIER moved — it appended bytes — while the
      // account it states did not. A saturated recording revises on every
      // chunk it appends, and a card rebuilt for each of those would replace a
      // DOM node and re-measure the pane for a number nobody can see change.
      session.fireRecoveryChanged({
        produced: 3_410_000,
        gaps: [{ start: 131_072, end: 3_446_848, reason: 'cap' }],
      })
      await Promise.resolve()

      expect(tab.pane.querySelector('.nocx-recovery-notice')).toBe(mounted)
      expect(cardTitle(tab)).toBe("3.3 MB of this session's output is missing")
    } finally {
      teardown()
    }
  })

  it('keeps a revised total from counting the live notice’s bytes twice (nocx-zg3k3.14.24)', async () => {
    const { session, hooks } = reclaiming({
      produced: 3_400_000,
      gaps: [{ start: 3_043, end: 98_304, reason: 'hostWindow' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      session.fireOutputGap({
        sessionId: session.sessionId,
        start: 3_043,
        end: 98_304,
        reason: 'hostWindow',
      })
      session.fireRecoveryChanged({
        produced: 4_262_996,
        gaps: [
          { start: 3_043, end: 98_304, reason: 'hostWindow' },
          { start: 131_072, end: 4_046_848, reason: 'cap' },
        ],
      })

      await vi.waitFor(() =>
        expect(cardTitle(tab)).toBe("3.9 MB of this session's output is missing"),
      )
      expect(cardDesc(tab)).not.toContain('hostWindow')
    } finally {
      teardown()
    }
  })

  it('unions overlapping live losses into one card and dismisses it independently of history', async () => {
    const { session, hooks } = reclaiming({
      produced: 0,
      gaps: [
        { start: 10, end: 30, reason: 'hostWindow' },
        { start: 50, end: 60, reason: 'cap' },
      ],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    try {
      session.fireOutputGap({
        sessionId: session.sessionId,
        start: 10,
        end: 20,
        reason: 'hostWindow',
      })
      session.fireOutputGap({
        sessionId: session.sessionId,
        start: 15,
        end: 25,
        reason: 'hostWindow',
      })
      session.fireOutputGap({
        sessionId: session.sessionId,
        start: 10,
        end: 20,
        reason: 'hostWindow',
      })

      const notices = [...tab.pane.querySelectorAll('.nocx-recovery-notice')]
      expect(notices).toHaveLength(2)
      const titleOf = (notice: Element) =>
        notice.querySelector('.ui-status-card__title')?.textContent ?? ''
      expect(notices.map(titleOf)).toContain('15 bytes of live output are missing')
      expect(notices.map(titleOf)).toContain("15 B of this session's output is missing")
      const historical = notices.find((notice) =>
        titleOf(notice).includes("of this session's output"),
      )!
      expect(historical.querySelector('.ui-status-card__desc')?.textContent).toContain(
        '5 B went missing as "hostWindow"',
      )
      expect(historical.querySelector('.ui-status-card__desc')?.textContent).toContain(
        "the recording's size limit dropped the oldest 10 B",
      )

      const live = tab.pane.querySelector('[data-notice-kind="live-output-gap"]')!
      live.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click()
      expect(tab.pane.querySelectorAll('.nocx-recovery-notice')).toHaveLength(1)
      expect(tab.pane.querySelector('[data-notice-kind="live-output-gap"]')).toBeNull()
      expect(tab.pane.querySelector('.nocx-recovery-notice')).toBe(historical)
      expect(titleOf(historical)).toBe("15 B of this session's output is missing")
    } finally {
      teardown()
    }
  })

  it('does not resurrect a dismissed historical card when another live gap arrives', async () => {
    const { session, hooks } = reclaiming({
      produced: 0,
      gaps: [{ start: 10, end: 20, reason: 'cap' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    try {
      const historical = () =>
        [...tab.pane.querySelectorAll('.nocx-recovery-notice')].find((notice) =>
          notice
            .querySelector('.ui-status-card__title')
            ?.textContent?.includes("of this session's output"),
        )
      await vi.waitFor(() => expect(historical()).toBeDefined())
      historical()!.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click()
      await vi.waitFor(() => expect(historical()).toBeUndefined())

      session.fireOutputGap({
        sessionId: session.sessionId,
        start: 30,
        end: 35,
        reason: 'hostWindow',
      })
      await vi.waitFor(() =>
        expect(
          [...tab.pane.querySelectorAll('.nocx-recovery-notice')].some((notice) =>
            notice
              .querySelector('.ui-status-card__title')
              ?.textContent?.includes('live output are missing'),
          ),
        ).toBe(true),
      )
      expect(historical()).toBeUndefined()
    } finally {
      teardown()
    }
  })

  it('keeps historical host-window loss visible when no live gap notification arrives', async () => {
    const { hooks } = reclaiming({
      produced: 0,
      gaps: [{ start: 100, end: 200, reason: 'hostWindow' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      await vi.waitFor(() => expect(cardTitle(tab)).not.toBeNull())
      expect(cardTitle(tab)).toBe("100 B of this session's output is missing")
      expect(cardDesc(tab)).toContain('went missing as "hostWindow"')
    } finally {
      teardown()
    }
  })

  it('names the unrecorded stretch, which is a different fact from the bound', async () => {
    const { hooks } = reclaiming({
      produced: 0,
      gaps: [{ start: 100, end: 5100, reason: 'unrecorded' }],
    })
    const { tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      await vi.waitFor(() => expect(cardTitle(tab)).not.toBeNull())
      expect(cardDesc(tab)).toContain('never recorded')
      expect(cardDesc(tab)).not.toContain('size limit')
    } finally {
      teardown()
    }
  })

  // The negative, and the reason the positive means anything.
  it('says nothing when the reclaim recovered the whole recording', async () => {
    const { hooks } = reclaiming({ produced: 8192, gaps: [] })
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), { hooks })
    try {
      await expect(content.ready).resolves.toBe(true)
      expect(tab.pane.querySelector('.nocx-recovery-notice')).toBeNull()
    } finally {
      teardown()
    }
  })

  // A pane that opened a fresh session recovered nothing and has nothing to
  // report — `recovered` is null, and a card here would be a claim about a
  // session that never had a past.
  it('says nothing on a pane that opened its own session', async () => {
    const { content, tab, teardown } = await mountTerminal()
    try {
      await expect(content.ready).resolves.toBe(true)
      expect(tab.pane.querySelector('.nocx-recovery-notice')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('shows an inconsistent output-gap protocol error through the sticky danger toast', async () => {
    const { session, hooks } = reclaiming(null)
    const { teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    try {
      vi.mocked(showToast).mockClear()
      session.fireProtocolError(
        'Session output protocol error: gap [4, 5) does not match current offset 3. Output is paused.',
      )
      expect(showToast).toHaveBeenCalledWith({
        level: 'danger',
        message:
          'Session output protocol error: gap [4, 5) does not match current offset 3. Output is paused.',
      })
    } finally {
      teardown()
    }
  })

  it('is taken away when the user dismisses it', async () => {
    const { hooks } = reclaiming({
      produced: 0,
      gaps: [{ start: 0, end: 1000, reason: 'cap' }],
    })
    // In the document: Solid delegates `click` at the document root, so a
    // detached pane never sees the press a user makes.
    const { tab, teardown } = await mountTerminal(makeClipboard(), {
      hooks,
      attachToDocument: true,
    })
    try {
      await vi.waitFor(() => expect(cardTitle(tab)).not.toBeNull())
      const cross = [...tab.pane.querySelectorAll('button')].find(
        (b) => b.getAttribute('aria-label') === 'Dismiss',
      )
      expect(cross).toBeDefined()
      cross!.click()
      await vi.waitFor(() => expect(tab.pane.querySelector('.nocx-recovery-notice')).toBeNull())
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// A client that is not the one the channel follows draws at the SESSION's
// grid, not at its own window (nocx-eidfb.3)
// ═══════════════════════════════════════════════════════════════════════════
//
// The size a session runs at is the BACKEND's (nocx-eidfb.1) and it follows
// the client that attached last (nocx-eidfb.2). Every other client therefore
// has two answers in front of it — what its own window measures, and what the
// channel is actually running at — and only one of them describes the bytes
// arriving down the wire. A window that draws them at its own width wraps
// them a second time, and no later resize can unpick that: two clients then
// show two different screens of one session.
//
// The seam a person reaches is the window that takes a live session back. It
// attaches without reporting any geometry (ipc.reclaimSession), so until its
// own fit runs the session is still at somebody else's size, and the
// recovered scrollback is written in exactly that interval.
describe('a pane drawing a session it did not size (nocx-eidfb.3)', () => {
  const SESSION_GRID = { cols: 132, rows: 43, xpixel: 0, ypixel: 0 }
  /** The grid a backend-spawned worker runs at before any window has
   *  measured it, and the crate makes exactly this promise about it: "the
   *  first client to attach reports its own geometry and the session
   *  resizes" (internal/app/workers.go, participantCols/participantRows). */
  const WORKER_GRID = { cols: 120, rows: 40 }
  const RECOVERED = 'what the session printed while this window was away'

  /** A pane that takes back a live session running at SESSION_GRID, with the
   *  recovered scrollback waiting to be flushed the moment the surface
   *  registers its data callback — which is what WSClient.onSessionData does
   *  with the bytes reclaimSession queued ahead of the live stream. */
  const reclaimingPane = async () => {
    const session = makeSession({ recovered: { produced: 0, gaps: [] } })
    const mounted = await mountTerminal(makeClipboard(), {
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    })
    // Recovery communicates metadata, not bytes. The backend's accepted
    // frame is what supplies the visible screen and the grid dimensions.
    mounted.tab.setActive(true)
    const frame = screenFrame(1, [RECOVERED], { cols: SESSION_GRID.cols, rows: SESSION_GRID.rows })
    session.fireScreenFrame(frame)
    return { ...mounted, session }
  }

  it('paints the recovered screen from the accepted frame at the session grid', async () => {
    const { tab, teardown } = await reclaimingPane()
    try {
      const live = tab.pane.querySelector<HTMLElement>('.terminal-live-container')!
      await vi.waitFor(() => expect(live.textContent).toContain(RECOVERED))
      // The painter receives the backend's full accepted geometry, including
      // blank rows beyond the recovered line.
      expect(live.querySelectorAll('.term-grid-row')).toHaveLength(SESSION_GRID.rows)
    } finally {
      teardown()
    }
  })

  it('does not echo an accepted session grid until this window measures its own', async () => {
    const { content, session, teardown } = await reclaimingPane()
    try {
      vi.useFakeTimers()
      try {
        expect(session.sendResize).not.toHaveBeenCalled()
        content.viewportChanged({ width: 800, height: 400 })
        vi.advanceTimersByTime(10_000)
        expect(session.sendResize).toHaveBeenCalledTimes(1)
        const report = session.sendResize.mock.calls[0][0]
        expect(report).not.toMatchObject({ cols: SESSION_GRID.cols, rows: SESSION_GRID.rows })
        expect(report.xpixel).toBe(report.cols * 8)
        expect(report.ypixel).toBe(report.rows * 16)
      } finally {
        vi.useRealTimers()
      }
    } finally {
      teardown()
    }
  })

  // THE RESTORED TAB THAT STAYED AT THE DEFAULT (nocx-1ebrs.42). The order
  // here is a restore's, not an attach's: the pane's viewport is delivered
  // BEFORE the recovered screen exists, and the first accepted frame is an
  // alternate-buffer screen at the size the backend dropped the detached
  // session to — the default grid and NO pixel metrics (internal/session/
  // size.go: NoClient hands back a grid and no pixels). In a real layout
  // that pane is fullscreen, so the natural-advance probe the fit measures
  // with is one of the children the fullscreen rule has hidden — both width
  // sources dead, the fit never ran, and the program was never told the
  // pane it had come back into. The rows the painter put up are the one
  // measurement that survives every mode: a row is exactly cols cells wide.
  it('refits a restored full-screen pane whose frames carry no pixel metrics', async () => {
    const session = makeSession({ recovered: { produced: 0, gaps: [] } })
    const mounted = await mountTerminal(makeClipboard(), {
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    })
    const { content, tab, teardown } = mounted
    try {
      tab.setActive(true)
      // The real restore order: the viewport arrives first; the recovered
      // screen follows it.
      content.viewportChanged({ width: 1632, height: 832 })
      const frame = screenFrame(1, [RECOVERED], {
        cols: 80,
        rows: 24,
        cellWidthPx: 0,
        cellHeightPx: 0,
      })
      frame.state.bufferKind = 'alternate'
      session.fireScreenFrame(frame)

      // The pane painted the recovered screen; give its rows the box a real
      // layout would (80 cols at 8 px, a 16 px row) — jsdom measures nothing.
      const live = tab.pane.querySelector<HTMLElement>('.terminal-live-container')!
      await vi.waitFor(() => expect(live.querySelector('.term-grid-row')).not.toBeNull())
      const row = live.querySelector<HTMLElement>('.term-grid-row')!
      Object.defineProperty(row, 'getBoundingClientRect', {
        value: () =>
          ({ width: 80 * 8, height: 16, top: 0, left: 0, right: 640, bottom: 16 }) as DOMRect,
      })

      // The rows are measurable now; re-deliver the pane's box the way a
      // real re-measure arrives and wait for the fit's own report.
      content.viewportChanged({ width: 1632, height: 832 })
      await vi.waitFor(() => {
        const reports = session.sendResize.mock.calls.map((call) => call[0])
        const mine = reports.find((report) => report.cols > 80)
        expect(mine).toBeDefined()
        expect(mine!.cols).toBe(Math.floor(1632 / 8))
        expect(mine!.xpixel).toBe(mine!.cols * 8)
      })
    } finally {
      teardown()
    }
  })

  it('lets the session grid pad or scroll inside the window rather than being cut', async () => {
    const { content, teardown } = await reclaimingPane()
    try {
      const area = scrollbackFor(content).scrollbackArea
      expect(area.dataset.gridOwner).toBe('session')

      // …and the stylesheet is what makes that attribute mean something. The
      // scroller's overflow-x is `hidden` for a grid this window chose —
      // deliberately, so a wide grid can never widen the box its own cols are
      // derived from. A grid this window did NOT choose is not in that loop,
      // and being cut mid-glyph is the one outcome that is not allowed.
      const css = stripComments(readFileSync(STYLE_ENTRY, 'utf8'))
      const owned =
        css.match(/\.scrollback-area\[data-grid-owner=['"]session['"]\]\s*\{([^}]*)\}/)?.[1] ?? ''
      expect(owned).toMatch(/overflow-x\s*:\s*auto/)
    } finally {
      teardown()
    }
  })

  it('hands the grid back to the window when its measured viewport changes', async () => {
    const { content, teardown } = await reclaimingPane()
    try {
      const area = scrollbackFor(content).scrollbackArea
      expect(area.dataset.gridOwner).toBe('session')
      // A local measurement takes ownership; the accepted frame remains the
      // visual source until the backend publishes its resized screen.
      content.viewportChanged({ width: 800, height: 400 })
      expect(area.dataset.gridOwner).toBeUndefined()
    } finally {
      teardown()
    }
  })

  /** A pane whose session the BACKEND sized and started — a nocx-spawned
   *  worker, the shape nocx-xn63t.6.35 was reported against. The pane is
   *  built while its tab is in the background and the running program paints
   *  on its own, which is the whole difference from a shell sitting at a
   *  prompt: an agent's TUI repaints while a person resizes the window. */
  const workerPane = async () => {
    const session = makeSession({ recovered: { produced: 0, gaps: [] } })
    const mounted = await mountTerminal(makeClipboard(), {
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    })
    let revision = 1
    const paint = (lines: string[] = ['worker'], at = WORKER_GRID): void => {
      session.fireScreenFrame(screenFrame(revision++, lines, { cols: at.cols, rows: at.rows }))
    }
    return { ...mounted, session, paint }
  }

  it('reports the grid this window measured when the program paints inside the settle window (nocx-xn63t.6.35)', async () => {
    const { content, tab, session, paint, teardown } = await workerPane()
    try {
      paint(['worker', 'thinking'])
      expect(scrollbackFor(content).scrollbackArea.dataset.gridOwner).toBe('session')
      vi.useFakeTimers()
      try {
        // The worker's tab comes to the front, and the pane is laid out.
        tab.setActive(true)
        content.viewportChanged({ width: 1632, height: 832 })
        expect(content.cols).toBe(204)
        // AND THE WORKER KEEPS PAINTING. Its frame puts the session's own grid
        // back into the state the pending report is computed from — which is
        // what the report must not read.
        paint(['worker', 'still thinking'])
        vi.advanceTimersByTime(1_000)
        expect(session.sendResize).toHaveBeenCalledTimes(1)
        expect(session.sendResize.mock.calls[0][0]).toMatchObject({
          cols: 204,
          rows: 52,
          xpixel: 204 * 8,
          ypixel: 52 * 16,
        })
      } finally {
        vi.useRealTimers()
      }
    } finally {
      teardown()
    }
  })

  it('keeps its own measured grid when a frame moves the session grid under it (nocx-xn63t.6.35)', async () => {
    const { content, session, paint, teardown } = await workerPane()
    try {
      paint(['worker'])
      vi.useFakeTimers()
      try {
        content.viewportChanged({ width: 1632, height: 832 })
        vi.advanceTimersByTime(1_000)
        session.sendResize.mockClear()
        // The session's grid moves under this window — another client resized
        // it. The frame describes THEIR grid, and this pane is not theirs.
        paint(['worker', 'a grid another window chose'], { cols: 90, rows: 30 })
        vi.advanceTimersByTime(1_000)
        expect(session.sendResize).toHaveBeenCalledTimes(1)
        expect(session.sendResize.mock.calls[0][0]).toMatchObject({ cols: 204, rows: 52 })
      } finally {
        vi.useRealTimers()
      }
    } finally {
      teardown()
    }
  })

  it('invents no grid for a pane that has measured nothing of its own (nocx-xn63t.6.35)', async () => {
    const { session, paint, teardown } = await workerPane()
    try {
      vi.useFakeTimers()
      try {
        paint(['worker'])
        vi.advanceTimersByTime(1_000)
        // The only true answer for a window that has measured nothing of its
        // own is the grid the session states. 80x24 is this class's own seed,
        // and reporting it would shrink a session over a pane nobody laid out.
        expect(session.sendResize).toHaveBeenCalledTimes(1)
        expect(session.sendResize.mock.calls[0][0]).toMatchObject({
          cols: WORKER_GRID.cols,
          rows: WORKER_GRID.rows,
        })
      } finally {
        vi.useRealTimers()
      }
    } finally {
      teardown()
    }
  })

  it('leaves the pane its own window: only the terminal takes the session grid', async () => {
    const { content, tab, teardown } = await reclaimingPane()
    try {
      // Acceptance 3: the tab strip, the sidebar and the panels are DOM and
      // lay themselves out. Nothing here may write the session's geometry
      // onto the pane — a width put there is a window sized by somebody
      // else's terminal.
      expect(tab.pane.style.width).toBe('')
      expect(scrollbackFor(content).scrollbackArea.style.width).toBe('')
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// A reclaimed pane is still named after where it is (nocx-07cf4)
// ═══════════════════════════════════════════════════════════════════════════
// A tab is named by its pane and a pane by its cwd. A RECLAIM carries no cwd
// in the open acknowledgement — `ipc.reclaimSession` passes '' on purpose —
// so the backend-owned full-frame baseline must restore the latest OSC 7
// report without parsing recovered PTY bytes in the renderer.
describe('a reclaimed pane is still named after where it is (nocx-07cf4)', () => {
  /** A reclaimed session has no cwd in its open acknowledgement. Its first
   *  accepted frame carries the runtime's current metadata instead. */
  const reclaimingPaneReporting = async (cwd: string) => {
    const session = makeSession({
      cwd: '',
      recovered: { produced: 0, gaps: [] },
      pendingData: 'the screen this window was away for',
    })
    const mounted = await mountTerminal(makeClipboard(), {
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    })
    fireFrameMetadata(session, 1, { cwd: { host: '', path: cwd } })
    return mounted
  }

  it('names the tab from the OSC 7 in the scrollback the claim recovered', async () => {
    const { tab, teardown } = await reclaimingPaneReporting(FIXTURE_CWD)
    try {
      // The pane's own title, not `displayTitle`: the descriptor's fallback
      // would answer 'Terminal' for a pane that was never named, and the
      // defect this guards is exactly a pane that is never named.
      await vi.waitFor(() => expect(tab.title).toBe(FIXTURE_DIRECTORY_LABEL))
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// A reclaimed pane shows the work that never stopped (nocx-ht15k)
// ═══════════════════════════════════════════════════════════════════════════
// The lifecycle facts of a reclaimed session are replayed by the backend the
// instant the attach installs its subscriber (ws_lifecycle replayLifecycleFacts)
// — which is INSIDE the bind, because since nocx-k6p18.6 a reclaiming bind
// awaits `awaitWriteBarrier()` over the recovered scrollback before it returns.
// mount publishes `this.renderer` only after that bind, so a block port that
// read the field instead of the renderer it was built with saw null for the
// whole of that window, and LifecycleProjections.pump records an attempt in
// `_bound` before it calls the port: the drop is permanent, and the person who
// came back to their pane never saw the command that was still running.
describe('replayed completion restores a durable block outcome (nocx-gm21o)', () => {
  it('restores the persisted failure outcome for a completed entry', async () => {
    const session = makeSession({
      recovered: {
        produced: 0,
        gaps: [],
      },
    })
    const entry = {
      id: 'entry-replayed-command',
      seq: 1,
      environmentId: 'env-1',
      host: 'host.example',
      cwd: '/repo',
      kind: 'shell',
      source: 'user',
      intent: 'make deploy',
      phase: 'bound',
      status: 'failure',
      unreconciled: null,
      submittedAt: 1,
      startedAt: 1,
      endedAt: 2,
      durationMs: 1,
      exitCode: 7,
      maskedCount: 0,
      maskedKinds: [],
      redactions: [],
    } as const
    let resolveQuery!: (value: unknown) => void
    const queryPending = new Promise<unknown>((resolve) => {
      resolveQuery = resolve
    })
    const client = makeClient({
      call: vi.fn<WSClient['call']>().mockImplementation((method) => {
        if (method === 'ledger.query') return queryPending
        if (method === 'ledger.get') {
          return Promise.resolve({
            entry,
            edges: [],
            artifacts: [{ id: 'artifact-1', mediaType: 'application/vt' }],
            proseEvicted: false,
            caused: [],
          })
        }
        if (method === 'ledger.artifact') {
          return Promise.resolve({
            id: 'artifact-1',
            mediaType: 'application/vt',
            body: '',
            truncated: null,
            byteLen: 0,
          })
        }
        return Promise.reject(new Error(`unexpected method ${method}`))
      }),
    })
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        visibleBeforeMount: true,
        hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
      },
      client,
    )
    try {
      // The completed outcome is durable history, not a client-parsed marker.
      resolveQuery({
        entries: [entry],
        scope: 'everywhere',
        exhausted: true,
        hasRows: true,
        coverage: null,
      })

      await vi.waitFor(() =>
        expect(tab.pane.querySelector('[data-entry-id="entry-replayed-command"]')).not.toBeNull(),
      )
      const restored = tab.pane.querySelector<HTMLElement>(
        '[data-entry-id="entry-replayed-command"]',
      )
      expect(restored?.dataset.outcome).toBe('failure')
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// The pane receives the screen plane into its cell model (nocx-zg3k3.2.8)
// ═══════════════════════════════════════════════════════════════════════════
// The pane holds ONE cell model, fed by accepted session frames; the painter
// draws from it. These tests read the model
// only through the seam an e2e spec reads — window.__nocxPaneScreen() over
// the ACTIVE pane — so the unit layer asserts the same surface the e2e
// layer drives, not a private field.

const SCREEN_COLS = 40
const SCREEN_ROWS = 4

/** One valid frame at `revision` whose first rows carry `lines` — each
 *  row's own explicit content is only as long as its text; the model's
 *  decode pads the rest of geometry.cols in the default style, and an
 *  entirely absent row is the empty string, which pads the same way. That
 *  is the shape the backend actually publishes (nocx-zg3k3.2.12): a row
 *  does not carry its untouched tail, and a reader that wants the full
 *  rectangle (this fixture's caller, joining cells back into a line) gets
 *  it from the model's own padding, not from a fixture hand-building 40
 *  columns per row. */
function screenFrame(
  revision: number,
  lines: string[],
  geometry: Partial<SessionFrame['geometry']> = {},
): SessionFrame {
  const style = styleOf({ foreground: { kind: 1, palette: 7, rgb: { r: 0, g: 0, b: 0 } } })
  const rows: Row[] = lines.map((text) =>
    wireRowOf(Array.from(text, (ch) => [ch, 1, true, style] as CellSpec)),
  )
  while (rows.length < (geometry.rows ?? SCREEN_ROWS)) rows.push({ text: '' })
  return {
    revision,
    geometry: {
      cols: SCREEN_COLS,
      rows: SCREEN_ROWS,
      cellWidthPx: 8,
      cellHeightPx: 16,
      revision,
      ...geometry,
    },
    cursor: { x: 0, y: 0, visible: true },
    state: {
      bufferKind: 'normal',
      bufferInstance: '1',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: false,
      pointerShape: null,
      title: null,
      cwd: null,
    },
    rows,
  }
}

function fireBufferState(
  session: SessionFake,
  bufferKind: 'normal' | 'alternate',
  revision: number,
): void {
  const frame = screenFrame(revision, [])
  frame.state.bufferKind = bufferKind
  session.fireScreenFrame(frame)
}

function fireFrameMetadata(
  session: SessionFake,
  revision: number,
  metadata: { title?: string; cwd?: { host: string; path: string } },
): void {
  const frame = screenFrame(revision, ['metadata'])
  if (metadata.title !== undefined) frame.state.title = metadata.title
  if (metadata.cwd !== undefined) frame.state.cwd = metadata.cwd
  session.fireScreenFrame(frame)
}

function paneScreen(): PaneScreenReading | null {
  const host = window as unknown as { __nocxPaneScreen?: () => PaneScreenReading | null }
  return host.__nocxPaneScreen?.() ?? null
}

function paneCellToPixel(col: number, row: number): { x: number; y: number } | null {
  const host = window as unknown as {
    __nocxPaneCellToPixel?: (col: number, row: number) => { x: number; y: number } | null
  }
  return host.__nocxPaneCellToPixel?.(col, row) ?? null
}

describe('the pane screen mapping follows the session lifetime', () => {
  it('drops the old mapping when reconnect replaces its session', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      const first = sessionOf(content)
      tab.pane.setAttribute('data-session-id', first.sessionId)
      first.fireScreenFrame(screenFrame(1, ['prompt']))
      await vi.waitFor(() => expect(paneCellToPixel(1, 1)).toEqual({ x: 8, y: 16 }))

      const onExit = first.onExit.mock.calls[0]?.[0] as
        ((exit: { sessionId: string; cause: 'interrupted' }) => void) | undefined
      expect(onExit).toBeDefined()
      onExit!({ sessionId: first.sessionId, cause: 'interrupted' })
      await expect(content.reconnect()).resolves.toBe(true)

      // Exercise the public renderer seam with the departed session identity.
      tab.pane.setAttribute('data-session-id', first.sessionId)
      expect(paneCellToPixel(1, 1)).toBeNull()
    } finally {
      teardown()
    }
  })

  it('drops the mapping when the pane is finally disposed', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const session = sessionOf(content)
    try {
      tab.setActive(true)
      tab.pane.setAttribute('data-session-id', session.sessionId)
      session.fireScreenFrame(screenFrame(1, ['prompt']))
      await vi.waitFor(() => expect(paneCellToPixel(1, 1)).toEqual({ x: 8, y: 16 }))

      tab.close()
      tab.pane.setAttribute('data-session-id', session.sessionId)
      expect(paneCellToPixel(1, 1)).toBeNull()
    } finally {
      teardown()
    }
  })
})

describe('the pane receives the screen plane into its cell model (nocx-zg3k3.2.8)', () => {
  it('a valid frame advances the model the active pane reads, paired with a refused stale one', async () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => undefined)
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session: SessionFake = client._sessions[0]
      // Before the first accepted frame, the model has no committed cell
      // metric. The open reports the named 80x24 grid with zero pixel sizes;
      // only a backend frame can establish the painter's cell dimensions.
      expect(paneScreen()).toEqual({
        revision: null,
        rows: [],
        geometry: null,
        reported: { cols: 80, rows: 24, xpixel: 0, ypixel: 0 },
      })
      session.fireScreenFrame(screenFrame(3, ['PROMPT$ ls', 'NOCX-MARKER-1']))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(3))
      const reading = paneScreen()
      expect(reading?.rows[0]).toBe('PROMPT$ ls')
      expect(reading?.rows[1]).toContain('NOCX-MARKER-1')

      // THE REFUSAL, paired: a stale frame neither throws out of the
      // socket handler nor moves the model, and the refusal is logged with
      // the model's own vocabulary.
      expect(() => session.fireScreenFrame(screenFrame(2, ['stale']))).not.toThrow()
      expect(paneScreen()?.revision).toBe(3)
      const refusalLine = debug.mock.calls.find((c) =>
        String(c[0]).includes('screen frame refused'),
      )
      expect(refusalLine).toBeDefined()
      expect(String(refusalLine?.[0])).toContain('stale-revision')

      // …and the pane is where the seam looked for it: not active, not read.
      tab.pane.classList.remove('active')
      expect(paneScreen()).toBeNull()
    } finally {
      debug.mockRestore()
      teardown()
    }
  })

  it('restores program title and cwd only from an accepted backend frame', async () => {
    const reported: string[] = []
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        attachToDocument: true,
        hooks: { onPaneCwdChange: (cwd) => reported.push(cwd) },
      },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session: SessionFake = client._sessions[0]
      const accepted = screenFrame(3, ['restored screen'])
      accepted.state.title = 'restored program title'
      accepted.state.cwd = { host: '', path: '/repo/restored' }
      session.fireScreenFrame(accepted)
      await vi.waitFor(() => expect(tab.title).toBe('restored program title'))
      expect(reported).toEqual(['/repo/restored'])

      const stale = screenFrame(2, ['stale screen'])
      stale.state.title = 'stale title'
      stale.state.cwd = { host: '', path: '/repo/stale' }
      session.fireScreenFrame(stale)
      expect(tab.title).toBe('restored program title')
      expect(reported).toEqual(['/repo/restored'])
    } finally {
      teardown()
    }
  })

  it('drops a prompt boundary and settlement when its correlated screen frame was refused', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      const host = (content as unknown as { host: { contentSettled: () => void } }).host
      const settled = vi.spyOn(host, 'contentSettled')
      session.fireScreenFrame(screenFrame(3, ['accepted']))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(3))

      const refused = screenFrame(7, ['refused'])
      refused.geometry.cols = 0
      session.fireScreenFrame(refused)
      expect(paneScreen()?.revision).toBe(3)

      session.fireEffect({
        sessionId: session.sessionId,
        generation: '1',
        effectId: '9',
        kind: 'promptBoundary',
        title: '',
        body: '',
        screenRevision: 7,
      })
      expect(settled).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('applies a prompt boundary only after its session model accepts the screen revision', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session = sessionOf(content)
      const host = (content as unknown as { host: { contentSettled: () => void } }).host
      const settled = vi.spyOn(host, 'contentSettled')
      session.fireScreenFrame(screenFrame(7, ['accepted']))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(7))

      settled.mockClear()
      session.fireEffect({
        sessionId: session.sessionId,
        generation: '1',
        effectId: '10',
        kind: 'promptBoundary',
        title: '',
        body: '',
        screenRevision: 7,
      })

      expect(settled).toHaveBeenCalledOnce()
    } finally {
      teardown()
    }
  })

  it('drops an early prompt boundary until its screen revision is accepted', async () => {
    const client = makeClient()
    const pendingSession = makeSession()
    const pendingEffect: SessionEffect = {
      sessionId: pendingSession.sessionId,
      generation: '1',
      effectId: '11',
      kind: 'promptBoundary',
      title: '',
      body: '',
      screenRevision: 1,
    }
    const delivered = vi.fn()
    pendingSession.onEffect = vi.fn((cb: (effect: SessionEffect) => void) => {
      delivered()
      cb(pendingEffect)
    })
    client.openSession.mockImplementation(() => Promise.resolve(pendingSession))

    const { teardown } = await mountTerminal(makeClipboard(), { attachToDocument: true }, client)
    try {
      expect(delivered).toHaveBeenCalledOnce()
    } finally {
      teardown()
    }
  })

  // THE COMMITTED METRIC IS DEVICE PIXELS (review round 1, nocx-zg3k3.2.9).
  // xterm builds its CSS cell FROM an integer device cell (css = device /
  // dpr), so device pixels are the unit where the metric is exact: at dpr 2
  // a 17x34 device cell is a fractional 8.5x17 CSS cell, and rounding CSS
  // would drift the painter's grid off xterm's by up to half a pixel per
  // cell. The report carries the DEVICE cell — cols x device width exactly,
  // no rounding anywhere — and the committed geometry decodes back to it,
  // so committed px / dpr IS the CSS cell. A dims change at the same
  // cols/rows (this display move, a zoom) reaches the report through the
  // same settle door, and a REPEATED metric sends nothing (the ipc dedupe).
  it('reports and commits the device cell: committed px / dpr is the CSS cell', async () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => undefined)
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session: SessionFake = client._sessions[0]
      // The backend commits a dense display's device cell (17x34 at dpr 2).
      // That accepted frame is the sole metric source; there is no renderer
      // callback or client-side terminal measurement to synthesize.
      vi.stubGlobal('devicePixelRatio', 2)
      session.fireScreenFrame(
        screenFrame(4, ['NOCX-MARKER-1'], { cellWidthPx: 17, cellHeightPx: 34 }),
      )
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(4))
      await vi.waitFor(() => {
        const expected = {
          cols: SCREEN_COLS,
          rows: SCREEN_ROWS,
          xpixel: SCREEN_COLS * 17,
          ypixel: SCREEN_ROWS * 34,
        }
        expect(session.sendResize).toHaveBeenCalledWith(expected)
        expect(paneScreen()?.reported).toEqual(expected)
      })
      // THE ROUND TRIP, exact on both axes: the frame's committed device px
      // over the grid the client named is the client's report per cell, and
      // committed px / dpr is the CSS cell — no rounding step anywhere.
      const reading = paneScreen()
      expect(reading?.geometry?.cellWidthPx).toBe(17)
      expect(reading?.geometry?.cellHeightPx).toBe(34)
      expect(reading!.geometry!.cellWidthPx * reading!.reported!.cols).toBe(
        reading!.reported!.xpixel,
      )
      expect(reading!.geometry!.cellHeightPx * reading!.reported!.rows).toBe(
        reading!.reported!.ypixel,
      )
      expect(reading!.geometry!.cellWidthPx / 2).toBe(8.5)
      expect(reading!.geometry!.cellHeightPx / 2).toBe(17)
    } finally {
      vi.unstubAllGlobals()
      debug.mockRestore()
      teardown()
    }
  })

  it('a document that is not a frame at all is dropped without throwing out of the handler', async () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => undefined)
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      await vi.waitFor(() => expect(paneScreen()).not.toBeNull())
      const session: SessionFake = client._sessions[0]

      session.fireScreenFrame(screenFrame(1, ['before']))
      await vi.waitFor(() => expect(paneScreen()?.revision).toBe(1))
      // A document with no frame shape — apply throws inside it, and the
      // handler catches rather than tearing down the socket.
      const notAFrame = { nonsense: true } as unknown as SessionFrame
      expect(() => session.fireScreenFrame(notAFrame)).not.toThrow()

      await vi.waitFor(() =>
        expect(
          debug.mock.calls.find((c) => String(c[0]).includes('could not be applied')),
        ).toBeDefined(),
      )
      expect(paneScreen()?.revision).toBe(1)
      const dropLine = debug.mock.calls.find((c) => String(c[0]).includes('could not be applied'))
      expect(dropLine).toBeDefined()
    } finally {
      debug.mockRestore()
      teardown()
    }
  })
})
// ═══════════════════════════════════════════════════════════════════════════
// The live region is the painter picture (nocx-zg3k3.2.5)
// ═══════════════════════════════════════════════════════════════════════════
// The cutover: what a person sees in the live region is the cell painter
// drawing the backend frames. These tests read the
// DOM a person eyes reach — the painted grid inside the live container, the
// cursor overlay placed by THE mapping over the committed geometry, and the
// absence of a legacy xterm root. Every wait is on painted DOM
// state, never on a duration.

describe('the live region is the painter picture (nocx-zg3k3.2.5)', () => {
  const liveRegionOf = (pane: HTMLElement): HTMLElement =>
    pane.querySelector('.terminal-live-container') as HTMLElement

  it('paints frame rows into the live region with the cursor on the committed geometry', async () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => undefined)
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      const session: SessionFake = client._sessions[0]
      const live = liveRegionOf(tab.pane)
      // Before any frame nothing is PAINTED: the painter's surface exists
      // from mount (it is the live region's picture plane), but it holds
      // only the cursor overlay — no rows, no xterm canvas.
      expect(live.querySelectorAll('.term-grid-row').length).toBe(0)
      session.fireScreenFrame(screenFrame(7, ['PROMPT$ ls', 'NOCX-PAINTED-1']))
      // The wait is on the PAINTED state: the grid surface exists from
      // mount, so the rows are the thing that can only be there once a
      // frame has been applied.
      const rows = await vi.waitFor(() => {
        const painted = [...live.querySelectorAll('.term-grid-row')]
        expect(painted.length).toBe(SCREEN_ROWS)
        return painted
      })
      expect(rows[0].textContent?.startsWith('PROMPT$ ls')).toBe(true)
      expect(rows[1].textContent?.includes('NOCX-PAINTED-1')).toBe(true)

      // The grid is the painter's surface, inside the live container.
      const grid = live.querySelector<HTMLElement>('.term-grid')!
      expect(grid).not.toBeNull()

      // THE CURSOR RIDES THE COMMITTED GEOMETRY: cell (0, 0) of an 8x16
      // device-pixel frame at the fixture dpr-1 identity is a zero offset,
      // and the overlay is exactly one CSS cell.
      const cursor = grid.querySelector<HTMLElement>('.term-grid-cursor')
      expect(cursor).not.toBeNull()
      expect(cursor!.hidden).toBe(false)
      expect(cursor!.style.left).toBe('0px')
      expect(cursor!.style.width).toBe('8px')
      expect(cursor!.style.height).toBe('16px')
    } finally {
      debug.mockRestore()
      teardown()
    }
  })

  it('repaints only the rows that changed and reuses the rest', async () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => undefined)
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      const session: SessionFake = client._sessions[0]
      const live = liveRegionOf(tab.pane)
      session.fireScreenFrame(screenFrame(7, ['PROMPT$ ls', 'NOCX-REUSE-1']))
      await vi.waitFor(() => {
        expect(live.querySelectorAll('.term-grid-row').length).toBe(SCREEN_ROWS)
      })
      const before = [...live.querySelectorAll('.term-grid-row')]

      // One row changed. The changed row is rebuilt; every unchanged row
      // keeps its DOM — the node reuse the frame budget is measured on.
      session.fireScreenFrame(screenFrame(8, ['PROMPT$ ls', 'NOCX-REUSE-2']))
      await vi.waitFor(() => {
        const second = live.querySelectorAll('.term-grid-row')[1]
        expect(second.textContent).toContain('NOCX-REUSE-2')
      })
      const after = [...live.querySelectorAll('.term-grid-row')]
      expect(after.length).toBe(SCREEN_ROWS)
      expect(after[0]).toBe(before[0])
      expect(after[1]).not.toBe(before[1])
      expect(after[3]).toBe(before[3])
    } finally {
      debug.mockRestore()
      teardown()
    }
  })

  it('batches a same-tick burst of frames into one painted state', async () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => undefined)
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      const session: SessionFake = client._sessions[0]
      const live = liveRegionOf(tab.pane)
      session.fireScreenFrame(screenFrame(7, ['BURST-BASE-0']))
      await vi.waitFor(() => {
        expect(live.textContent).toContain('BURST-BASE-0')
      })

      // Two frames in one tick: nothing paints synchronously, and what
      // lands is the LATEST revision only.
      session.fireScreenFrame(screenFrame(8, ['BURST-MID-8']))
      session.fireScreenFrame(screenFrame(9, ['BURST-LAST-9']))
      expect(live.textContent).not.toContain('BURST-LAST-9')
      await vi.waitFor(() => {
        expect(live.textContent).toContain('BURST-LAST-9')
      })
      expect(live.textContent).not.toContain('BURST-MID-8')
    } finally {
      debug.mockRestore()
      teardown()
    }
  })

  it('sends painted-grid pointer coordinates as session mouse intent', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      const session = client._sessions[0]
      const frame = screenFrame(1, ['pointer target'])
      frame.state.mouseTracking = true
      session.fireScreenFrame(frame)
      const live = liveRegionOf(tab.pane)
      await vi.waitFor(() => expect(live.querySelector('.term-grid')).not.toBeNull())
      const surface = live.querySelector<HTMLElement>('.term-grid')!
      await vi.waitFor(() => expect(surface.textContent).toContain('pointer target'))
      surface.getBoundingClientRect = () =>
        ({ left: 10, top: 20, right: 330, bottom: 84, width: 320, height: 64 }) as DOMRect

      const pointer = (
        type: 'pointerdown' | 'pointermove' | 'pointerup',
        buttons: number,
        x = 26,
      ): Event => {
        const event = new Event(type, { bubbles: true, cancelable: true })
        Object.defineProperties(event, {
          button: { value: 0 },
          buttons: { value: buttons },
          clientX: { value: x },
          clientY: { value: 44 },
        })
        return event
      }
      surface.dispatchEvent(pointer('pointerdown', 1))
      // A drag is a MOTION (nocx-zg3k3.14.19): the selection gesture paints
      // only once the pointer has left its 4 px slop around the press, so the
      // moved pointer carries real distance — 64 px, five cells right. The
      // move rides the window, the gesture's own listener; the press and the
      // release stay on the surface at the pinned cell (2, 1).
      window.dispatchEvent(pointer('pointermove', 1, 90))
      // THE DRAG IS THE PERSON'S EVEN WHILE THE PROGRAM REPORTS THE MOUSE
      // (nocx-zg3k3.15.5). The intents asserted below are what the program
      // receives, and they travel this pane's own intent path — not the
      // selection gesture — so standing that gesture down for a reporting
      // program bought the program nothing and left the highlight to the
      // browser, which cleared it at the release.
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
      surface.dispatchEvent(pointer('pointerup', 0))
      // ONE ROW OF MOVEMENT, because a wheel is counted in LINES and not in
      // events (nocx-1ebrs.26): the pane supplies the row pitch, so anything
      // smaller would accumulate instead of reporting. This frame's cell
      // height is 16px.
      surface.dispatchEvent(
        new WheelEvent('wheel', { bubbles: true, clientX: 26, clientY: 44, deltaY: -16 }),
      )

      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledTimes(3))
      expect(session.intent.mock.calls).toEqual([
        ['mouse', 'press left 2 1'],
        ['mouse', 'release left 2 1'],
        ['mouse', 'press wheel-up 2 1'],
      ])
    } finally {
      teardown()
    }
  })

  it('keeps local selection and scrolling when the accepted backend mode is off', async () => {
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      tab.setActive(true)
      const session = client._sessions[0]
      const frame = screenFrame(1, ['selectable text'])
      frame.state.mouseTracking = false
      session.fireScreenFrame(frame)
      const live = liveRegionOf(tab.pane)
      await vi.waitFor(() => expect(live.querySelector('.term-grid')).not.toBeNull())
      const surface = live.querySelector<HTMLElement>('.term-grid')!
      await vi.waitFor(() => expect(surface.textContent).toContain('selectable text'))
      surface.getBoundingClientRect = () =>
        ({ left: 10, top: 20, right: 330, bottom: 84, width: 320, height: 64 }) as DOMRect

      const pointer = (
        type: 'pointerdown' | 'pointermove' | 'pointerup',
        buttons: number,
        x = 26,
      ): Event => {
        const event = new Event(type, { bubbles: true, cancelable: true })
        Object.defineProperties(event, {
          button: { value: 0 },
          buttons: { value: buttons },
          clientX: { value: x },
          clientY: { value: 44 },
        })
        return event
      }
      session.intent.mockClear()
      surface.dispatchEvent(pointer('pointerdown', 1))
      // A drag is a MOTION (nocx-zg3k3.14.19): the selection gesture paints
      // only once the pointer has left its 4 px slop around the press, so the
      // moved pointer carries real distance — 64 px, five cells right.
      window.dispatchEvent(pointer('pointermove', 1, 90))
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
      surface.dispatchEvent(pointer('pointerup', 0))
      // ONE ROW OF MOVEMENT, because a wheel is counted in LINES and not in
      // events (nocx-1ebrs.26): the pane supplies the row pitch, so anything
      // smaller would accumulate instead of reporting. This frame's cell
      // height is 16px.
      surface.dispatchEvent(
        new WheelEvent('wheel', { bubbles: true, clientX: 26, clientY: 44, deltaY: -16 }),
      )

      expect(session.intent).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })
})

describe('the grid is not re-measured around every command (nocx-2v80t.3.46)', () => {
  // The composer changes the scroller's height, but not the common terminal
  // area. The program always receives that area's full width and its height
  // less the fixed running header, before submit and in either buffer.
  const COMPOSER = 130
  const HEADER = 32
  /** `scroller` and `composer` are the laid-out boxes the fit must add up; the
   *  default keeps the integers every other test in this describe was written
   *  against. A fractional pair is the pane the browser really lays out. */
  async function paneWithLayout(layout: { scroller?: number; composer?: number } = {}) {
    const client = makeClient()
    const mounted = await mountTerminal(makeClipboard(), { attachToDocument: true }, client)
    const { content } = mounted
    const handler = lifecycleHandler(client)
    const session = sessionOf(content)
    let frameRevision = 0
    const paint = (line: string): void => {
      session.fireScreenFrame(screenFrame(++frameRevision, [line]))
    }
    const inner = content as unknown as {
      scrollback: ScrollbackController
      editor: CommandEditor
    }
    const sb = inner.scrollback
    const composerPx = layout.composer ?? COMPOSER
    let pane = layout.scroller !== undefined ? layout.scroller + composerPx : 676
    const composerShown = (): boolean =>
      inner.editor.isVisible && inner.editor.root.dataset.placement !== 'overlay'
    Object.defineProperty(sb.scrollbackArea, 'clientHeight', {
      configurable: true,
      // `clientHeight` is a whole number: it ROUNDS the box the browser laid
      // out. The rect below is the same box in the fractional pixels the fit
      // adds up (nocx-3an1s.1.4).
      get: () => Math.round(pane - (composerShown() ? composerPx : 0)),
    })
    Object.defineProperty(sb.scrollbackArea, 'clientWidth', { configurable: true, value: 926 })
    const rect = (height: number): DOMRect =>
      ({ height, width: 926, top: 0, left: 0, right: 926, bottom: height, x: 0, y: 0 }) as DOMRect
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoRect = Element.prototype.getBoundingClientRect
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    const raf = globalThis.requestAnimationFrame
    /* eslint-enable @typescript-eslint/unbound-method */
    // A hostile DOM measurement cannot change the fixed header budget.
    let headerHeight = HEADER
    const measuredProbes: Element[] = []
    Element.prototype.getBoundingClientRect = function (this: Element): DOMRect {
      if (this === sb.scrollbackArea) return rect(pane - (composerShown() ? composerPx : 0))
      if (this === inner.editor.root) return rect(composerShown() ? composerPx : 0)
      if (this.classList.contains('cmd-block')) {
        if ((this as HTMLElement).style.position === 'absolute') measuredProbes.push(this)
        return rect(headerHeight)
      }
      return rect(0)
    }
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    const restore = (): void => {
      Element.prototype.getBoundingClientRect = protoRect
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      globalThis.requestAnimationFrame = raf
      mounted.teardown()
    }

    let n = 0
    /** Put a command in the composer — typing, and nothing else. The
     *  geometry a command will run at settles HERE, at the prompt, before a
     *  submit exists to disturb. */
    const typeCommand = (text: string): void => {
      // A keystroke, not a programmatic set: the editor fires onInputChange
      // for user-driven changes only, and the prediction rides that event.
      mounted.view.dispatch({
        changes: { from: 0, to: mounted.view.state.doc.length, insert: text },
      })
    }
    /** Enter: the submit, the running fact it publishes before the shell's
     *  own bytes exist (ADR-0024 §5) — the point `_openAuthenticatedBlock`
     *  opens the block. Returns the attempt id `finishCommand` needs to
     *  close it. */
    const submitCommand = (text: string): string => {
      n += 1
      const id = `att-${n}`
      mounted.view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id,
          state: 'open',
          origin: 'app',
          submitId: submitToken(client),
          command: text,
        },
      })
      return id
    }
    /** One composer command: type it, then submit it. */
    const openCommand = (): string => {
      const text = `printf ${n + 1}`
      typeCommand(text)
      return submitCommand(text)
    }
    /** A command typed at the shell instead: its bytes are already on the
     *  grid — echoed before any running fact could exist — when the fact
     *  arrives. The order a composer submit can never produce, and the one
     *  where a fit at block-open lands after the command started. */
    const openShellCommand = (): string => {
      n += 1
      const id = `att-${n}`
      paint(`$ printf ${n}`)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id, state: 'open', origin: 'shell', command: `printf ${n}` },
      })
      return id
    }
    /** Output, completion and the block's close, through the return to a
     *  ready prompt. */
    const finishCommand = (id: string): void => {
      paint(`out ${id}`)
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id,
          state: 'completed',
          exitCode: 0,
          fence: 'f'.repeat(64),
          completedAt: '2026-09-26T00:00:00Z',
        },
      })
      blockClosedHandler(client)(id)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // The prompt's own redraw is output too, and re-measures like any.
      paint('$ ')
    }
    /** One ordinary command, through the composer, to its block's close. */
    const runCommand = (): void => {
      finishCommand(openCommand())
    }
    const resize = (height: number): void => {
      pane = height
      content.viewportChanged({ width: 936, height })
    }
    /** Simulate a header whose DOM rectangle changes independently of the pane. */
    const setHeaderHeight = (px: number): void => {
      headerHeight = px
    }

    content.setVisible(true)
    content.viewportChanged({ width: 936, height: pane })
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    // The opening prompt's own output: the grid's first parse is where the
    // prediction first reads layout, so the prompt settles the grid before
    // anyone types (a live pane has the same prompt behind it).
    paint('$ ')
    /** Every size report sent to the backend, derived from accepted frame metrics. */
    const fits = (): { cols: number; rows: number; xpixel: number; ypixel: number }[] =>
      session.sendResize.mock.calls.map((call) => call[0])
    await vi.waitFor(() => expect(fits().length).toBeGreaterThan(0))
    return {
      content,
      session,
      tab: mounted.tab,
      sb,
      fits,
      runCommand,
      typeCommand,
      submitCommand,
      openCommand,
      openShellCommand,
      finishCommand,
      measuredProbes,
      setHeaderHeight,
      resize,
      restore,
    }
  }

  it('the prompt’s area and the running cap are one fractional height (nocx-3an1s.1.4)', async () => {
    // A pane the browser lays out at 677.0px: a 546.6px scroller and a 130.4px
    // composer. `clientHeight` rounds each half separately (547 + 130.4), which
    // is 0.4px MORE than the same pane measured once the composer hides and the
    // scroller takes its box. The pty was fitted into that difference — a row
    // the running box has no room for — and the last row ended below the clip
    // (e2e/running-grid-fits-its-clip.spec.ts). ADR-3kqrem7v's one geometry
    // means one measurement in the pane's own fractional pixels, so the two
    // numbers are equal before any row is fitted.
    const { content, sb, fits, openCommand, restore } = await paneWithLayout({
      scroller: 546.6,
      composer: 130.4,
    })
    try {
      const area = sb.scrollbackArea
      const inner = content as unknown as { _terminalAreaPx(a: HTMLElement): number }
      const promptHeight = inner._terminalAreaPx(area) - sb.runningHeaderHeightPx
      const prompt = fits()[fits().length - 1]
      expect(prompt).toBeDefined()

      openCommand()
      const cap = sb.runningLiveCap
      expect(cap).not.toBeNull()
      expect(promptHeight).toBeCloseTo(cap!, 6)
      // And the rows the prompt fitted occupy no more than the box that will
      // show them.
      expect(prompt.rows * 16).toBeLessThanOrEqual(cap!)
    } finally {
      restore()
    }
  })

  it('commits no geometry across twenty commands in a pane whose size did not change', async () => {
    const { sb, fits, runCommand, restore } = await paneWithLayout()
    try {
      // The pane's first command settles what a command takes off the pane.
      runCommand()
      expect(sb.mode).toBe('idle')
      const before = fits().length
      for (let i = 0; i < 20; i++) runCommand()
      expect(fits().slice(before)).toEqual([])
    } finally {
      restore()
    }
  })

  it('a pane’s FIRST command commits no geometry from Enter to close (nocx-2v80t.3.50)', async () => {
    // No calibration command before this one. The fixed running-header budget
    // must be reserved before Enter. The snapshot sits between
    // typing and Enter, where a person's submit begins: a fit committed at
    // the submit, the running fact, the first parsed write or the close is
    // a pty resize the command's own echo or output is already inside.
    const { fits, typeCommand, submitCommand, finishCommand, resize, restore } =
      await paneWithLayout()
    try {
      typeCommand('printf 1')
      const before = fits().length
      const id = submitCommand('printf 1')
      finishCommand(id)
      expect(fits().length).toBe(before)
      // The pairing: the grid is not frozen — a genuine pane resize still
      // commits, exactly once.
      resize(776)
      await vi.waitFor(() => expect(fits().length).toBe(before + 1))
    } finally {
      restore()
    }
  })

  it('long command text and a changed header measurement cannot resize a command (nocx-3an1s.1.4)', async () => {
    // Text truncates on one line. Even a hostile DOM height must not become
    // a second owner of the running program's geometry.
    const {
      fits,
      runCommand,
      typeCommand,
      submitCommand,
      finishCommand,
      setHeaderHeight,
      resize,
      restore,
    } = await paneWithLayout()
    try {
      runCommand()
      setHeaderHeight(2 * HEADER)
      const longCommand = 'printf staging && deploy && tell everyone '.repeat(12)
      const settled = fits()[fits().length - 1]
      typeCommand(longCommand)
      expect(fits()[fits().length - 1]).toEqual(settled)
      const before = fits().length
      const id = submitCommand(longCommand)
      finishCommand(id)
      expect(fits().length).toBe(before)
      resize(776)
      await vi.waitFor(() => expect(fits().length).toBe(before + 1))
    } finally {
      restore()
    }
  })

  it('a shell-originated start commits no geometry after its bytes are out (nocx-2v80t.3.50)', async () => {
    // Typed at the shell, not the composer: the echo is on the grid before
    // any running fact could exist, so the block opens AFTER the command
    // started and a fit at the fact lands inside it. First command of the
    // pane, so nothing was learned that could be right by luck. Whatever
    // height the grid needs while this command runs had to be true before
    // its bytes went out.
    const { fits, openShellCommand, finishCommand, resize, restore } = await paneWithLayout()
    try {
      const before = fits().length
      const id = openShellCommand()
      finishCommand(id)
      expect(fits().length).toBe(before)
      resize(776)
      await vi.waitFor(() => expect(fits().length).toBe(before + 1))
    } finally {
      restore()
    }
  })

  it('reserves exactly one fixed header at the prompt and does not measure command-text probes', async () => {
    const { fits, typeCommand, submitCommand, finishCommand, measuredProbes, restore } =
      await paneWithLayout()
    try {
      const initial = fits()[fits().length - 1]
      expect(initial).toEqual({ cols: 115, rows: 40, xpixel: 920, ypixel: 640 })
      const before = fits().length
      for (const text of ['git', 'git status', 'deploy staging && tell everyone '.repeat(20)]) {
        typeCommand(text)
      }
      const id = submitCommand('deploy staging && tell everyone '.repeat(20))
      finishCommand(id)
      expect(measuredProbes).toEqual([])
      expect(fits()).toHaveLength(before)
      expect(fits()[fits().length - 1]).toEqual(initial)
    } finally {
      restore()
    }
  })

  it('fits the full pane width independently of the live node’s ancestor insets', async () => {
    const { content, sb, fits, openCommand, restore } = await paneWithLayout()
    try {
      openCommand()
      const initial = fits()[fits().length - 1]
      const before = fits().length
      sb.scrollbackInner.style.paddingInline = '24px'
      sb.terminalLiveContainer.style.paddingLeft = '24px'
      sb.terminalLiveContainer.style.paddingRight = '24px'
      content.viewportChanged({ width: 936, height: 676 })
      expect(fits()).toHaveLength(before)
      expect(fits()[fits().length - 1]).toEqual(initial)
      expect(initial.cols).toBe(115)
      expect(initial.xpixel).toBe(920)
    } finally {
      restore()
    }
  })

  it('keeps the same desired grid across alternate entry, output, and return with no bottom panel', async () => {
    const { content, session, tab, sb, fits, openCommand, resize, restore } = await paneWithLayout()
    try {
      openCommand()
      const initial = fits()[fits().length - 1]
      const before = fits().length
      expect(tab.pane.querySelector('.ui-process-bar')).toBeNull()
      const alternate = screenFrame(10, ['ALT'], { cols: 40, rows: 20 })
      alternate.state.bufferKind = 'alternate'
      session.fireScreenFrame(alternate)
      expect(sb.mode).toBe('fullscreen')
      expect(session.resizeApplied).toHaveBeenLastCalledWith(initial)
      expect(tab.pane.querySelector('.ui-process-bar')).toBeNull()
      expect(fits()).toHaveLength(before)

      // Output and the header's new DOM seat are not a real pane resize.
      const repaint = screenFrame(11, ['ALT redraw'], { cols: 40, rows: 20 })
      repaint.state.bufferKind = 'alternate'
      session.fireScreenFrame(repaint)
      content.viewportChanged({ width: 936, height: 676 })
      expect(fits()).toHaveLength(before)

      // The equal applied receipt opens the switch fence. A later genuine
      // resize follows the normal report path, rather than replacing an
      // outstanding receipt (whose mock does not call sendResize).
      typeIntoPane(content, 'x')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'x'))
      resize(776)
      await vi.waitFor(() => expect(fits()).toHaveLength(before + 1))
      const resized = fits()[fits().length - 1]
      expect(resized).toEqual({ cols: 115, rows: 46, xpixel: 920, ypixel: 736 })
      session.fireScreenFrame(screenFrame(12, ['PRIMARY'], { cols: 40, rows: 20 }))
      expect(sb.mode).toBe('running')
      expect(fits()).toHaveLength(before + 1)
      expect(fits()[fits().length - 1]).toEqual(resized)
      expect(tab.pane.querySelector('.ui-process-bar')).toBeNull()
    } finally {
      restore()
    }
  })

  it('and a real resize commits once, at the resize, and not again at the next command', async () => {
    const { fits, runCommand, resize, restore } = await paneWithLayout()
    try {
      runCommand()
      const last = () => fits()[fits().length - 1]
      const running = last()
      const before = fits().length

      resize(776)
      await vi.waitFor(() => expect(fits().length).toBe(before + 1))
      expect(last().rows).toBeGreaterThan(running.rows)
      expect(last().ypixel).toBeGreaterThan(running.ypixel)

      for (let i = 0; i < 5; i++) runCommand()
      expect(fits().length).toBe(before + 1)
    } finally {
      restore()
    }
  })
})
// ADR-3kqrem7v replaces text-dependent off-flow header probes with one fixed
// running-header budget. These checks cover the old probe failure cases through
// the desired report: long text, a burst of typing, immediate submit, ancestor
// insets and alternate-screen seating must not change the pane's grid.
describe('runtime command snapshots update the per-pane store', () => {
  it('applies only a valid backend snapshot', async () => {
    const client = makeClient()
    const mounted = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = client._sessions[0]
      if (!session) throw new Error('session not opened')
      const store = mounted.content.snapshotStore
      const effect: SessionEffect = {
        sessionId: session.sessionId,
        generation: '1',
        effectId: '1',
        kind: 'commandSnapshot',
        title: '',
        body: '',
        commandNames: ['alias', 'git'],
      }
      session.fireEffect(effect)
      expect(store.has('alias')).toBe(true)
      expect(store.has('git')).toBe(true)
      store.applyBackendSnapshot([])
      expect(store.has('alias')).toBe(true)
    } finally {
      mounted.teardown()
    }
  })
})

describe('runtime clipboard effects keep the existing permission gate', () => {
  it('denies a clipboard write until permission is granted, then writes once', async () => {
    const clipboard = makeClipboard()
    const client = makeClient()
    const mounted = await mountTerminal(clipboard, {}, client)
    try {
      const session = client._sessions[0]
      if (!session) throw new Error('session not opened')
      const effect = {
        sessionId: session.sessionId,
        generation: '1',
        effectId: '1',
        kind: 'clipboard' as const,
        title: '',
        body: 'runtime text',
      }
      session.fireEffect(effect)
      await Promise.resolve()
      expect(clipboard.writeText).not.toHaveBeenCalled()
      mounted.gate.allow()
      session.fireEffect({ ...effect, effectId: '2' })
      expect(clipboard.writeText).toHaveBeenCalledTimes(1)
      expect(clipboard.writeText).toHaveBeenCalledWith('runtime text')
    } finally {
      mounted.teardown()
    }
  })
})

// ── the copy chord leaves plain Ctrl+C to the interrupt (nocx-1ebrs.19.1) ──
//
// THE OWNER'S DECISION, 2026-10-20: Cmd+C over a visible live-grid selection
// copies it, and with nothing selected the chord keeps today's meaning. CTRL+C
// IS NOT THAT CHORD. It stays the interrupt whether or not a selection is on
// screen, because a person who pressed Ctrl+C mid-command asked to STOP
// something — and the decision's own sentence about Cmd+C, read the other way
// round, is what makes this the answer rather than a preference.
//
// WHAT THIS GUARDS IS ONE LINE. The chord is claimed in the capture phase, so
// it answers before both `_globalKeydown`'s interrupt and the program's input,
// and its first statement is a modifier test. One `|| event.ctrlKey` added to
// that test would take the interrupt away from every pane that happens to hold
// a selection: the key would be swallowed before it ever became an intent, and
// nothing else in this suite would notice, because every other part of the
// chord would go on working exactly as it does.
describe('the copy chord does not claim plain Ctrl+C (nocx-1ebrs.19.1)', () => {
  it('with a settled live-grid selection, Ctrl+C still reaches the program', async () => {
    const clipboard = makeClipboard()
    const client = makeClient()
    const { tab, teardown } = await mountTerminal(clipboard, { attachToDocument: true }, client)
    try {
      tab.setActive(true)
      const session: SessionFake = client._sessions[0]
      const frame = screenFrame(1, ['selectable text'])
      frame.state.mouseTracking = false
      session.fireScreenFrame(frame)
      const live = tab.pane.querySelector<HTMLElement>('.terminal-live-container')!
      await vi.waitFor(() => expect(live.querySelector('.term-grid')).not.toBeNull())
      const surface = live.querySelector<HTMLElement>('.term-grid')!
      await vi.waitFor(() => expect(surface.textContent).toContain('selectable text'))
      // One cell is 8 x 16 in this frame; the top-left of the grid is 10,20.
      surface.getBoundingClientRect = () =>
        ({ left: 10, top: 20, right: 330, bottom: 84, width: 320, height: 64 }) as DOMRect

      const pointer = (
        type: 'pointerdown' | 'pointermove' | 'pointerup',
        buttons: number,
        clientX: number,
      ): Event => {
        const event = new Event(type, { bubbles: true, cancelable: true })
        Object.defineProperties(event, {
          button: { value: 0 },
          buttons: { value: buttons },
          clientX: { value: clientX },
          // Row 0 of the painted grid: (24 - 20) / 16 rounds into it.
          clientY: { value: 24 },
        })
        return event
      }
      // A REAL DRAG, RELEASED, ACROSS CELLS THE TEXT IS ON. Pressing and
      // releasing on one cell is a click and copies nothing, so the pointer
      // moves two columns before it lifts: cols 2..4 of "selectable text" are
      // "lec". Copy-on-select has written it at the release, which is the
      // state the chord could wrongly answer.
      surface.dispatchEvent(pointer('pointerdown', 1, 26))
      window.dispatchEvent(pointer('pointermove', 1, 42))
      surface.dispatchEvent(pointer('pointerup', 0, 42))
      expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
      const atRelease = clipboard.writeText.mock.calls.length
      expect(atRelease, 'copy-on-select wrote nothing at the release').toBeGreaterThan(0)

      session.intent.mockClear()
      const input = tab.pane.querySelector<HTMLTextAreaElement>('.ui-terminal-input')
      expect(input, 'the pane has no input element to press Ctrl+C on').not.toBeNull()
      // `code` IS THE KEY IDENTITY the encoder reads (intent-input's table is
      // keyed by the physical key, not by `key`); a synthetic event without it
      // produces no intent at all, whatever the pane decides about the chord.
      input!.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          code: 'KeyC',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )

      // THE INTERRUPT'S OWN SPELLING, produced by the input element's encoder:
      // the key was not swallowed on its way there.
      await vi.waitFor(() => expect(session.intent.mock.calls).toEqual([['key', 'Ctrl+c']]))
      // AND NOTHING WAS COPIED A SECOND TIME — the chord never ran.
      expect(clipboard.writeText.mock.calls.length).toBe(atRelease)
      // AND THE HIGHLIGHT IS OVER, because a key was SENT to the program
      // (nocx-1ebrs.19's lifetime rule). The same fact from the other side: if
      // the chord had taken this key, nothing would have been sent.
      expect(surface.querySelector('.term-grid-selection')).toBeNull()
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// The alternate-screen size fence (nocx-1ebrs.10)
// ═══════════════════════════════════════════════════════════════════════════
// Measured on the tree before this fix: the first alternate-screen frames
// carried the PREVIOUS mode's geometry (192 columns with the pane settled
// wider; 70 columns in a 1394 px pane), the program read the person's keys
// while its own stty still reported the old width, and the resize request
// went out 87–234 ms after the first fullscreen frame and always behind the
// post-switch input.
//
// So the pane now does three things at the switch, and these tests are the
// three statements: it asks for its own measured grid immediately, it presents
// the frames that arrive before that request applies on the pane's grid rather
// than the program's, and it holds what the person produces until the request
// answers that it APPLIED — never until a frame, which can depend on the input
// being held.
describe('an alternate-screen switch holds input for its applied grid (nocx-1ebrs.10)', () => {
  const PANE = { width: 800, height: 400 }
  const CELL = { cellWidthPx: 8, cellHeightPx: 16 }
  /** One full-width pane less the same 32px header in both buffers. */
  const FITTED = { cols: 100, rows: 23 }
  const GRID_HEIGHT = FITTED.rows * CELL.cellHeightPx

  /** Mount a pane, give it a measured box and a live normal-buffer frame, then
   * fire the frame that switches to the alternate screen.
   *
   * The switch frame carries the geometry the defect recorded: the PREVIOUS
   * mode's 40 columns in a pane that has measured 100. */
  async function switchingPane(): Promise<{
    content: TerminalContent
    session: SessionFake
    tab: Pane
    grid: () => HTMLElement
    receipt: (outcome: 'applied' | 'superseded' | 'closed') => void
    /** Answer the request at `index` (0 is the switch's own), for a test that
     *  needs an OLDER answer to arrive while a newer request is in flight. */
    receiptAt: (index: number, outcome: 'applied' | 'superseded' | 'closed') => void
    fail: (error: Error) => void
    switchFrame: () => SessionFrame
    drawProgramFrame: (revision: number, cols?: number) => SessionFrame
    /** The number of resize requests this pane has issued. */
    requested: () => number
    /** Run every animation frame the pane has scheduled, in order. */
    flushFrames: () => void
    teardown: () => void
  }> {
    const client = makeClient()
    const mounted = await mountTerminal(makeClipboard(), { attachToDocument: true }, client)
    const { content, tab } = mounted
    // The painter's only ordering boundary is an animation frame. The frames
    // are COLLECTED and run on demand (flushFrames) rather than run inline:
    // inline would paint inside the frame handler, before the mode change the
    // same handler makes a moment later — an order the browser never
    // produces, and the one that hid the real defect (a paint on the fit from
    // before the switch). Installed after the mount, because the editor
    // measures through the same frame callback.
    const raf = globalThis.requestAnimationFrame
    let queued: FrameRequestCallback[] = []
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      queued.push(cb)
      return queued.length
    }
    const flushFrames = (): void => {
      // A callback may schedule the next one (the paint schedules the refit),
      // so the queue is drained rather than run once. The bound is a guard
      // against a scheduling loop in the code under test, not a frame count
      // anything depends on.
      for (let pass = 0; queued.length > 0 && pass < 50; pass++) {
        const frame = queued
        queued = []
        for (const cb of frame) cb(0)
      }
    }
    const teardown = (): void => {
      queued = []
      globalThis.requestAnimationFrame = raf
      mounted.teardown()
    }
    tab.setActive(true)
    const session = sessionOf(content)
    const scrollback = scrollbackFor(content)
    Object.defineProperty(scrollback.scrollbackArea, 'clientWidth', {
      configurable: true,
      value: PANE.width,
    })
    Object.defineProperty(scrollback.scrollbackArea, 'clientHeight', {
      configurable: true,
      value: PANE.height,
    })
    // The pane's own box, delivered the way the presentation host delivers it.
    content.viewportChanged({ width: PANE.width, height: PANE.height })
    const lifecycle = lifecycleHandler(client)
    lifecycle({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    lifecycle({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: { id: 'alt-command', state: 'open', origin: 'shell', command: 'top' },
    })
    // A first frame in the normal buffer gives the pane a committed cell
    // metric, which is what its fit measures with.
    session.fireScreenFrame(screenFrame(1, ['$ prompt'], CELL))
    flushFrames()
    await vi.waitFor(() =>
      expect(session.sendResize).toHaveBeenCalledWith({
        cols: FITTED.cols,
        rows: FITTED.rows,
        xpixel: PANE.width,
        ypixel: GRID_HEIGHT,
      }),
    )
    session.sendResize.mockClear()
    // Setup may seat and focus the program input. Only intents produced
    // after this boundary belong to the alternate-screen fence under test.
    session.intent.mockClear()
    const grid = () => tab.pane.querySelector<HTMLElement>('.term-grid')!
    const receipts: Array<(outcome: 'applied' | 'superseded' | 'closed') => void> = []
    const failures: Array<(error: Error) => void> = []
    session.resizeApplied.mockImplementation(
      () =>
        new Promise<'applied' | 'superseded' | 'closed'>((resolve, reject) => {
          receipts.push(resolve)
          failures.push(reject)
        }),
    )
    const switchFrame = (): SessionFrame => {
      const frame = screenFrame(2, ['ALT-READY'], { ...CELL, cols: 40, rows: 20 })
      frame.state.bufferKind = 'alternate'
      frame.state.mouseTracking = true
      session.fireScreenFrame(frame)
      return frame
    }
    const drawProgramFrame = (revision: number, cols = 100): SessionFrame => {
      const frame = screenFrame(revision, ['ALT-READY'], { ...CELL, cols, rows: 20 })
      frame.state.bufferKind = 'alternate'
      frame.state.mouseTracking = true
      session.fireScreenFrame(frame)
      return frame
    }
    return {
      content,
      session,
      tab,
      grid,
      // `receipt`/`fail` answer the NEWEST request, the way the transport's
      // newest request owns the pending slot.
      receipt: (outcome) => receipts[receipts.length - 1]?.(outcome),
      receiptAt: (index, outcome) => receipts[index]?.(outcome),
      fail: (error) => failures[failures.length - 1]?.(error),
      switchFrame,
      drawProgramFrame,
      requested: () => receipts.length,
      flushFrames,
      teardown,
    }
  }

  it('asks for the pane’s measured grid as the switch is accepted, not the frame’s', async () => {
    const { session, switchFrame, teardown } = await switchingPane()
    try {
      switchFrame()
      // Synchronously: the switch is a known moment, and there is nothing
      // to settle. The frame the pane just accepted says 40 columns; the
      // pane says 100, and the pane is the one that knows its box.
      expect(session.resizeApplied).toHaveBeenCalledWith({
        cols: FITTED.cols,
        rows: FITTED.rows,
        xpixel: PANE.width,
        ypixel: GRID_HEIGHT,
      })
    } finally {
      teardown()
    }
  })

  it('holds an intent the mode change itself produces, behind the resize', async () => {
    // The mode change seats the keyboard surface, and seating it emits a focus
    // intent. That intent is produced by the switch, so it belongs behind the
    // resize like everything else the switch produces — which is only true if
    // the fence is armed BEFORE the mode change, not after it. The real stack
    // measured the failure this prevents: an intent 3 ms ahead of the request.
    const { content, session, switchFrame, receipt, teardown } = await switchingPane()
    try {
      const input = intentElementOf(content)
      const scrollback = scrollbackFor(content)
      /* eslint-disable @typescript-eslint/unbound-method */
      const enter = scrollback.enterFullscreen
      /* eslint-enable @typescript-eslint/unbound-method */
      const seated = vi.spyOn(scrollback, 'enterFullscreen').mockImplementation(() => {
        enter.call(scrollback)
        input.dispatchEvent(new FocusEvent('focus'))
      })

      switchFrame()
      // The focus intent was produced inside the mode change and did NOT go
      // out; the resize did.
      expect(session.intent).not.toHaveBeenCalled()
      expect(session.resizeApplied).toHaveBeenCalledTimes(1)

      receipt('applied')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('focus', 'in'))
      expect(seated).toHaveBeenCalledTimes(1)
      // And in that order on the wire: the resize first, the held intent after.
      expect(session.resizeApplied.mock.invocationCallOrder[0]).toBeLessThan(
        session.intent.mock.invocationCallOrder[0],
      )
    } finally {
      teardown()
    }
  })

  it('keeps the primary grid across alternate entry instead of deriving a new fit from its DOM seat', async () => {
    const { content, session, tab, grid, switchFrame, flushFrames, teardown } =
      await switchingPane()
    try {
      const scrollback = scrollbackFor(content)
      const block = scrollback.blockManager.runningBlock!
      const header = block.el.querySelector('.cmd-header')!
      // Transcript ancestors can retain prose insets. They do not own the
      // desired grid, and seating the active live node elsewhere changes none
      // of its columns or rows.
      scrollback.scrollbackInner.style.paddingLeft = '24px'
      scrollback.scrollbackInner.style.paddingRight = '24px'
      switchFrame()
      flushFrames()
      expect(session.resizeApplied).toHaveBeenLastCalledWith({
        cols: FITTED.cols,
        rows: FITTED.rows,
        xpixel: PANE.width,
        ypixel: GRID_HEIGHT,
      })
      expect(session.sendResize).not.toHaveBeenCalled()
      expect(tab.pane.contains(header)).toBe(true)
      expect(grid().textContent).toContain('ALT-READY')
      expect(grid().getAttribute('aria-colcount')).toBe(String(FITTED.cols))
      expect(grid().querySelectorAll('.term-grid-row')).toHaveLength(20)
    } finally {
      teardown()
    }
  })

  it('presents the switch frame on the pane’s grid, keeping its text and its row count', async () => {
    const { grid, switchFrame, flushFrames, teardown } = await switchingPane()
    try {
      switchFrame()
      flushFrames()
      const painted = grid()
      expect(painted.textContent).toContain('ALT-READY')
      // The size is the pane's…
      expect(painted.getAttribute('aria-colcount')).toBe(String(FITTED.cols))
      // …and only the size: the program's rows are the program's, so no
      // row is invented to fill the box, and the frame's own rows are all
      // still there.
      expect(painted.querySelectorAll('.term-grid-row')).toHaveLength(20)
    } finally {
      teardown()
    }
  })

  it('holds what the person produces and releases it on the applied receipt', async () => {
    const { content, session, switchFrame, receipt, teardown } = await switchingPane()
    try {
      switchFrame()
      typeIntoPane(content, 'q')
      expect(session.intent).not.toHaveBeenCalledWith('text', 'q')
      expect(session.intent).not.toHaveBeenCalledWith('key', 'q')
      // The request went out before the input did: the resize is what the
      // program needs before the key can mean anything.
      expect(session.resizeApplied.mock.invocationCallOrder[0]).toBeLessThan(
        session.intent.mock.invocationCallOrder[0] ?? Number.POSITIVE_INFINITY,
      )

      receipt('applied')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'q'))
      // Delivered ONCE, in the order produced: the queue is replayed, not
      // re-decided.
      const delivered = session.intent.mock.calls
        .map(([kind, payload]) => `${kind}:${String(payload)}`)
        .filter((call) => call.endsWith(':q'))
      expect(delivered).toEqual(['text:q'])
    } finally {
      teardown()
    }
  })

  it('does not release on a painted frame — only on the receipt', async () => {
    const { content, session, switchFrame, drawProgramFrame, receipt, teardown } =
      await switchingPane()
    try {
      switchFrame()
      typeIntoPane(content, 'q')
      // The program repaints at the grid it was told, twice: output is not
      // an answer to a question about the pty's size.
      drawProgramFrame(3)
      drawProgramFrame(4)
      await Promise.resolve()
      expect(session.intent).not.toHaveBeenCalledWith('text', 'q')

      receipt('applied')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'q'))
    } finally {
      teardown()
    }
  })

  it('keeps holding on a superseded receipt and releases on the newer resize', async () => {
    const { content, session, tab, switchFrame, receipt, flushFrames, teardown } =
      await switchingPane()
    try {
      switchFrame()
      flushFrames()
      typeIntoPane(content, 'q')
      // Superseded is not this pane's failure: a newer resize owns the
      // session's pending slot, so the grid is still on its way. No toast
      // (nothing failed) and no retry (a retry is a resize storm).
      receipt('superseded')
      await Promise.resolve()
      expect(session.intent).not.toHaveBeenCalledWith('text', 'q')
      // No toast: nothing failed, a newer resize is simply in flight.
      const announced = vi
        .mocked(showToast)
        .mock.calls.map(([toast]) => JSON.stringify(toast))
        .filter((toast) => /resiz/i.test(toast))
      expect(announced).toEqual([])

      // The pane widens while the switch is still in flight: this is the
      // newer request, and ITS receipt opens the fence.
      const scrollback = scrollbackFor(content)
      Object.defineProperty(scrollback.scrollbackArea, 'clientWidth', {
        configurable: true,
        value: 960,
      })
      content.viewportChanged({ width: 960, height: PANE.height })
      await vi.waitFor(() =>
        expect(session.resizeApplied).toHaveBeenLastCalledWith({
          cols: 120,
          rows: FITTED.rows,
          xpixel: 960,
          ypixel: GRID_HEIGHT,
        }),
      )
      expect(session.intent).not.toHaveBeenCalledWith('text', 'q')

      receipt('applied')
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'q'))
      // The pointer and the keyboard both read the grid the receipt named.
      expect(session.resizeApplied).toHaveBeenLastCalledWith({
        cols: 120,
        rows: FITTED.rows,
        xpixel: 960,
        ypixel: GRID_HEIGHT,
      })
      expect(tab.pane.querySelector('.term-grid')?.textContent).toContain('ALT-READY')
    } finally {
      teardown()
    }
  })

  it('releases on a failed request rather than holding the keys for a size it cannot get', async () => {
    const { content, session, switchFrame, fail, teardown } = await switchingPane()
    try {
      switchFrame()
      typeIntoPane(content, 'q')
      expect(session.intent).not.toHaveBeenCalledWith('text', 'q')

      fail(new Error('the session is no longer attached'))
      await vi.waitFor(() => expect(session.intent).toHaveBeenCalledWith('text', 'q'))
    } finally {
      teardown()
    }
  })

  it('releases on a closed session without delivering input to it', async () => {
    const { content, session, switchFrame, receipt, teardown } = await switchingPane()
    try {
      switchFrame()
      typeIntoPane(content, 'q')
      receipt('closed')
      await Promise.resolve()
      await Promise.resolve()
      expect(session.intent).not.toHaveBeenCalledWith('text', 'q')
    } finally {
      teardown()
    }
  })

  it('maps the pointer through the grid the applied receipt reported', async () => {
    const { content, session, switchFrame, receipt, flushFrames, teardown } = await switchingPane()
    try {
      switchFrame()
      flushFrames()
      const input = intentElementOf(content)
      // Column 50 exists in this pane (100 columns) and not in the program's
      // 40 — the click that landed on empty space before.
      input.dispatchEvent(
        new PointerEvent('pointerdown', {
          bubbles: true,
          cancelable: true,
          button: 0,
          clientX: 50 * CELL.cellWidthPx,
          clientY: 2 * CELL.cellHeightPx,
        }),
      )
      expect(session.intent).not.toHaveBeenCalledWith('mouse', expect.anything())

      receipt('applied')
      await vi.waitFor(() =>
        expect(session.intent).toHaveBeenCalledWith('mouse', 'press left 50 2'),
      )
    } finally {
      teardown()
    }
  })

  /** What the person did, in the order the session was told about it. Only the
   *  input kinds: a `focus` intent is bookkeeping around them. */
  function deliveredActs(session: SessionFake): string[] {
    return session.intent.mock.calls
      .filter(([kind]) => kind === 'text' || kind === 'key')
      .map(([kind, payload]) => `${kind}:${String(payload)}`)
  }

  it('releases on its own bound when the receipt never arrives, delivering everything held', async () => {
    const { content, session, switchFrame, teardown } = await switchingPane()
    // Fake timers AFTER the mount: the mount's own settle timers must not be
    // part of this test, and the fence's bound is armed at the switch.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      switchFrame()
      typeIntoPane(content, 'a', '\r', 'b')
      expect(deliveredActs(session)).toEqual([])

      // THE INTERVAL'S MISSING END: the socket is alive and the request is
      // simply never answered. Nothing in the client bounds it — a pending
      // call is settled by its answer, a socket close or an abort, and the
      // connection's heartbeat keeps a socket that answers PINGS alive — so
      // without the fence's own bound this is where the keys would stay.
      await vi.advanceTimersByTimeAsync(1_499)
      expect(deliveredActs(session)).toEqual([])

      await vi.advanceTimersByTimeAsync(1)
      // Delivered, not dropped: a key typed at the old width is a rendering
      // problem, a key swallowed forever is lost work.
      expect(deliveredActs(session)).toEqual(['text:a', 'key:enter', 'text:b'])
      // And the forced release is observable rather than silent.
      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining('no resize receipt at an alternate-screen switch'),
      )
    } finally {
      warn.mockRestore()
      vi.useRealTimers()
      teardown()
    }
  })

  it('delivers a key produced while the release loop runs, once and behind the held ones', async () => {
    const { content, session, switchFrame, receipt, teardown } = await switchingPane()
    try {
      switchFrame()
      typeIntoPane(content, 'a', '\r', 'b')
      const input = intentElementOf(content)
      // A key produced FROM INSIDE the release: the last held intent reaches
      // the session and the pane is asked for another one before the queue it
      // was walking has drained. A key that arrived while the release was in
      // flight must land BEHIND everything that was held, exactly once — the
      // shape that doubled characters once before.
      let reentered = false
      session.intent.mockImplementation((kind, payload) => {
        if (!reentered && kind === 'text' && payload === 'b') {
          reentered = true
          input.dispatchEvent(
            new KeyboardEvent('keydown', {
              code: 'ArrowDown',
              key: 'ArrowDown',
              bubbles: true,
              cancelable: true,
            }),
          )
        }
        return Promise.resolve({ state: 'executed', bytesWritten: 1, fenceAfter: 0 })
      })

      receipt('applied')
      await vi.waitFor(() => expect(deliveredActs(session)).toHaveLength(4))
      const acts = deliveredActs(session)
      // Every act exactly once, and the one that arrived mid-release is
      // behind the ones that were waiting rather than inside their run.
      expect(acts).toEqual(['text:a', 'key:enter', 'text:b', 'key:down'])
      expect(new Set(acts).size).toBe(4)
    } finally {
      teardown()
    }
  })

  it('keeps a second switch holding after the first switch’s late receipt arrives', async () => {
    const { content, session, switchFrame, receiptAt, requested, teardown } = await switchingPane()
    try {
      switchFrame()
      typeIntoPane(content, 'a')
      expect(requested()).toBe(1)

      // The program leaves the alternate screen and comes back — a second
      // switch, with the first one's receipt still outstanding.
      const thawed = screenFrame(3, ['$ prompt'], { ...CELL, cols: 40, rows: 20 })
      session.fireScreenFrame(thawed)
      await vi.waitFor(() => expect(deliveredActs(session)).toEqual(['text:a']))
      const resumed = screenFrame(4, ['ALT-READY'], { ...CELL, cols: 40, rows: 20 })
      resumed.state.bufferKind = 'alternate'
      session.fireScreenFrame(resumed)
      expect(requested()).toBe(2)
      typeIntoPane(content, 'b')
      await Promise.resolve()
      expect(deliveredActs(session)).toEqual(['text:a'])

      // The FIRST switch's answer arrives late. It describes a fence that is
      // gone, and it may not open the one that replaced it.
      receiptAt(0, 'applied')
      await Promise.resolve()
      await Promise.resolve()
      expect(deliveredActs(session)).toEqual(['text:a'])

      // The second switch's own receipt does.
      receiptAt(1, 'applied')
      await vi.waitFor(() => expect(deliveredActs(session)).toEqual(['text:a', 'text:b']))
    } finally {
      teardown()
    }
  })
})
