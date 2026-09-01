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
// The mount follows tabs.test.ts's pattern: the xterm renderer is mocked
// (jsdom cannot run xterm.js), the WS client fake resolves a session, and
// `tab.start()` drives TerminalContent through the same mount() a real tab
// takes. The editor is reached through the same private-field escape hatch
// editor.test.ts uses, and the selection is seeded through the CM6 view —
// the same transaction a mouse drag produces.
import { beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
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

import type { PaneIdentity } from './terminal-content'
import { grantBlockFromElement, type GrantBlock } from './ask-entry'
import type { AgentStatusResult } from './generated/agent.status'
import { EditorView } from '@codemirror/view'
import {
  createRendererMock,
  makeClient,
  makeClipboard,
  makeBanner,
  makeSession,
  anchoredPane,
  integrationHandler,
  lifecycleHandler,
  type ClipboardFake,
  type ClientFake,
  type LiveContentHeightSpy,
  type RendererMock,
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
import { SURFACE_TERMINAL } from './pane-content'
import { LifecycleKernel, shouldShowEditor } from './lifecycle/state'
import { ProfileClient, type SSHProfile } from './profiles'
import { Dispatcher, RpcError } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import type { SessionHandle, SessionRecovery, WSClient } from './ipc'
import { createCommandBlock } from './scrollback/blocks'
import { mountReadScreenHandler } from './read-screen'
import { CommandSnapshotStore } from './command-snapshot'
import type { ActionFacts, DesiredMode } from './capability'
import type * as Capability from './capability'
import type { ScrollbackController } from './scrollback/controller'
import { pushOverlay, popOverlay } from './ui/overlay/stack'
import { _resetThemeState } from './renderers/theme-adapter'
import { showToast } from './ui/toast'
import type { CapturedFrame } from './frame/types'
import { createCapturedFrameView } from './frame/display'
import { emptyAttrs } from './scrollback/serializer'
import { BufferLine } from './scrollback/test-helpers'

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

// Mock the XtermRenderer class before any imports use it (same as tabs.test.ts).
// The shared fixture mock implements the full TerminalRenderer surface,
// including the recovery-fence seam and the _fire* test helpers.
vi.mock('./renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

// The refusal path calls showToast, which mounts a Solid root; the
// export-section tests mock the module the same way.
vi.mock('./ui/toast', () => ({
  showToast: vi.fn(),
}))

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

/** TerminalContent also keeps the renderer private; tests reach the live
 *  mock through the same escape hatch editorOf uses (the field is typed
 *  TerminalRenderer in the class, structurally the mock). */
const rendererOf = (content: TerminalContent): RendererMock => {
  const withRenderer = content as unknown as { renderer: RendererMock }
  return withRenderer.renderer
}

/** The live session mock behind TerminalContent's private field — the same
 *  escape hatch editorOf uses. `send` is what a raw pty write lands on. */
const sessionOf = (content: TerminalContent): SessionFake =>
  (content as unknown as { session: SessionFake }).session
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
  /** Mount an SSH tab (the capability rail is SSH-only, nocx-4t37.2). */
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
  content: TerminalContent
  tab: Pane
  teardown: () => void
}> {
  const clientFake = client ?? makeClient()
  // ClientFake is structurally a WSClient; the tab layer expects the real type.
  const wsClient = clientFake as unknown as WSClient
  const content = new TerminalContent(
    wsClient,
    opts.pane ?? anchoredPane(),
    clipboard,
    new ClipboardGate(),
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
  await tab.start()
  await expect(content.ready).resolves.toBe(opts.expectedReady ?? true)

  const ed = editorOf(content)
  return {
    view: viewOf(ed),
    ed,
    clipboard,
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
describe('TerminalContent geometry handoff and PTY resize policy (nocx-cwnz0)', () => {
  it('a pane fit followed by identical parsed-output frames does not re-fit the same usable rectangle', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const renderer = rendererOf(content)
    const raf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      content.viewportChanged({ width: 800, height: 400 })
      /* eslint-disable @typescript-eslint/unbound-method */
      expect(renderer.fitViewport).toHaveBeenCalledTimes(1)
      expect(renderer.fitViewport).toHaveBeenLastCalledWith(
        expect.objectContaining({ width: 800, height: 400 }),
      )

      // The live-region output path runs on every parsed frame; the usable
      // rectangle is unchanged, so it must not fit again — the existing grid
      // stays authoritative.
      renderer._fireWriteParsed()
      renderer._fireWriteParsed()
      renderer._fireWriteParsed()
      expect(renderer.fitViewport).toHaveBeenCalledTimes(1)
      /* eslint-enable @typescript-eslint/unbound-method */
    } finally {
      globalThis.requestAnimationFrame = raf
      teardown()
    }
  })

  it('sends only the final grid to the session after the 80 ms settle window', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    let teardown: (() => void) | undefined
    try {
      const client = makeClient()
      const mounted = await mountTerminal(makeClipboard(), {}, client)
      teardown = mounted.teardown
      const renderer = rendererOf(mounted.content)
      const session = sessionOf(mounted.content)
      renderer._fireResize(120, 40)
      renderer._fireResize(100, 30)
      renderer._fireResize(90, 28)

      expect(session.sendResize).not.toHaveBeenCalled()
      vi.advanceTimersByTime(80)
      expect(session.sendResize).toHaveBeenCalledTimes(1)
      expect(session.sendResize).toHaveBeenCalledWith(90, 28)
    } finally {
      teardown?.()
      vi.useRealTimers()
    }
  })

  it('disposal cancels a pending PTY resize', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      const client = makeClient()
      const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
      const renderer = rendererOf(content)
      const session = sessionOf(content)

      renderer._fireResize(90, 28)
      expect(session.sendResize).not.toHaveBeenCalled()

      // Dispose before the settle window elapses: the trailing resize must
      // never reach the session.
      teardown()

      vi.advanceTimersByTime(100)
      expect(session.sendResize).not.toHaveBeenCalled()
    } finally {
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
      // The command and its '\r' — the paste is an onData too (nocx-yb5y).
      expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual(['make deploy', '\r'])

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
      const live = tab.pane.querySelector<HTMLElement>('.xterm-live-container')
      expect(live).not.toBeNull()
      // xterm's real hidden input lives inside the live container; while
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
  // takes — with nothing bypassed. The command text reaches the PTY via the
  // renderer's paste handoff and the trailing '\r' via the session, so both
  // are asserted: a second, parallel route would look different.
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
      // Two frames, in this order: the command through the renderer's paste
      // (which is an onData like any keystroke) and then the '\r' the shell
      // target appends. The renderer mock used to swallow the paste, so this
      // read as one frame and the command was invisible here (nocx-yb5y).
      expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual(['make deploy', '\r'])
      const sentBefore = session.send.mock.calls.length
      // The send payload is a string; String() gives the linter a typed value
      // without assuming the exact wire bytes (the '\r' the shell target
      // appends today could legitimately change).
      const sentShape = String(session.send.mock.calls[sentBefore - 1][0])

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
      expect(session.send.mock.calls.length).toBe(sentBefore)
      expect(ed.getDoc()).toBe('make deploy')
      // The shape the typed submit sent is still the reference for what a
      // real send looks like; nothing here produced one.
      expect(sentShape).toBe('\r')
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
              command: 'ssh pi@192.168.0.93',
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
  it("Cmd/Ctrl+V leaves xterm's helper textarea as the text owner", async () => {
    const { ed, content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    const renderer = rendererOf(content)
    const textarea = document.createElement('textarea')
    try {
      content.setVisible(true)
      ed.show()
      ed.insertText('keep this draft')
      renderer.paste.mockClear()
      const live = tab.pane.querySelector<HTMLElement>('.xterm-live-container')
      expect(live).not.toBeNull()
      live!.append(textarea)
      textarea.focus()
      expect(document.activeElement).toBe(textarea)

      const ev = new KeyboardEvent('keydown', {
        key: 'v',
        ctrlKey: true,
        bubbles: true,
        cancelable: true,
      })
      textarea.dispatchEvent(ev)

      // xterm owns paste while its helper textarea is focused. The
      // document rescue must not steal a browser-native paste from it.
      expect(ev.defaultPrevented).toBe(false)
      expect(document.activeElement).toBe(textarea)
      expect(ed.getDoc()).toBe('keep this draft')
      expect(renderer.paste).not.toHaveBeenCalled()
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

  it('writes the VALUE to the pty when the terminal owns input, and sends it', async () => {
    const client = resolvingClient('hunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      // The terminal owns input — a raw password prompt, an ssh handshake.
      // There is no reference machinery on the other side of the pty, so
      // the value goes across, WITH its newline: choosing a secret at a
      // password prompt is the answer to that prompt (owner, 2026-08-10).
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('value')
      const sent = session.send.mock.calls.slice(sentBefore).map((c: unknown[]) => c[0])
      expect(sent).toEqual(['hunter2\n'])
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
      await expect(content.insertSecret('gone')).resolves.toBe('unavailable')
      expect(session.send.mock.calls.length).toBe(sentBefore)
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
describe('characterising insertSecret before the input-owner extraction (nocx-xqu5)', () => {
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

  it('with the terminal owning input, the value is resolved and sent WITH its newline', async () => {
    const client = resolvingClient('hunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSecret('pi@far')).resolves.toBe('value')
      // The vault is asked for exactly the reference line — nothing more.
      expect(client.call).toHaveBeenCalledWith('vault.resolveLine', { line: REFERENCE })
      const sent = session.send.mock.calls.slice(sentBefore).map((c: unknown[]) => c[0])
      // The resolved value crosses, with the newline: choosing a secret at
      // a password prompt IS the answer to that prompt (owner, 2026-08-10).
      expect(sent).toEqual(['hunter2\n'])
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
      await expect(content.insertSecret('pi@far')).resolves.toBe('unavailable')
      expect(session.send.mock.calls.length).toBe(sentBefore)
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
      const renderer = rendererOf(content)
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
      // The paste goes through the engine with the resolved line, and the
      // engine's onData is what reaches the session — byte for byte. No
      // newline is appended anywhere on this path (§9.3).
      expect(renderer.paste).toHaveBeenCalledWith('run hunter2')
      const sent = session.send.mock.calls.slice(sentBefore).map((c: unknown[]) => c[0])
      expect(sent).toEqual(['run hunter2'])
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
      const renderer = rendererOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('fire {{secret:gone}}')).resolves.toEqual({
        ok: false,
        reason: 'unresolved-secret',
        name: 'gone',
      })
      expect(renderer.paste).not.toHaveBeenCalled()
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
      const renderer = rendererOf(content)
      editorOf(content).hide()
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('fire {{secret:pi@far}}')).resolves.toEqual({
        ok: false,
        reason: 'unresolved-secret',
      })
      expect(renderer.paste).not.toHaveBeenCalled()
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
    const renderer = rendererOf(content)
    const withSession = content as unknown as { session: SessionFake | null }
    const original = withSession.session
    withSession.session = null
    try {
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('echo hi')).resolves.toEqual(NO_OWNER)
      expect(renderer.paste).not.toHaveBeenCalled()
      expect(session.send.mock.calls.length).toBe(sentBefore)
    } finally {
      withSession.session = original
      teardown()
    }
  })

  it('a multi-line body is refused when the destination has no bracketed paste — before any resolution or write', async () => {
    const client = resolvingClient('line1\nhunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const session = sessionOf(content)
      const renderer = rendererOf(content)
      editorOf(content).hide()
      renderer.bracketedPasteActive.mockReturnValue(false)
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: false,
        reason: 'multi-line-no-bracketed-paste',
      })
      // The refusal is about the destination, so it is decided before the
      // vault is asked and before the engine is touched (§9.4).
      expect(client.call).not.toHaveBeenCalledWith('vault.resolveLine', expect.anything())
      expect(renderer.paste).not.toHaveBeenCalled()
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
      const renderer = rendererOf(content)
      editorOf(content).hide()
      renderer.bracketedPasteActive.mockReturnValue(true)
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: true,
        where: 'pty',
      })
      expect(client.call).toHaveBeenCalledWith('vault.resolveLine', {
        line: 'line1\n{{secret:pi@far}}',
      })
      // One document: resolved, newline intact, nothing appended (§9.4).
      expect(renderer.paste).toHaveBeenCalledWith('line1\nhunter2')
      const sent = session.send.mock.calls.slice(sentBefore).map((c: unknown[]) => c[0])
      expect(sent).toEqual(['line1\nhunter2'])
    } finally {
      teardown()
    }
  })

  // nocx-8rtr.1. The bytes a program sent HAVE arrived — the wire carries
  // them, measured in the stand — but renderer.write() is fire-and-forget
  // and the parse is one pass behind. A synchronous read of the mode at the
  // moment of the fire therefore answers about the terminal BEFORE the
  // program's DECSET, and refuses a body the destination can honour.
  //
  // Measured on the real renderer (renderers/xterm.test.ts's own
  // 'bracketed paste, read from the real parser' seam): immediately after
  // write('\x1b[?2004h') the read is false, and after the parse it is true.
  // So the fire must fence on the parse before it decides.
  it('a multi-line body is delivered when the program enabled the mode and the parse is still one pass behind', async () => {
    const client = resolvingClient('line1\nhunter2')
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const renderer = rendererOf(content)
      editorOf(content).hide()

      // The terminal as it is at the instant of the fire: the DECSET is in
      // the write queue, not yet in the parser. It becomes true only once
      // the barrier the renderer already offers has settled.
      let parsed = false
      renderer.bracketedPasteActive.mockImplementation(() => parsed)
      renderer.awaitWriteBarrier.mockImplementation(() => {
        parsed = true
        return Promise.resolve()
      })

      await expect(content.insertSnippet('line1\n{{secret:pi@far}}')).resolves.toEqual({
        ok: true,
        where: 'pty',
      })
      expect(renderer.paste).toHaveBeenCalledWith('line1\nhunter2')
    } finally {
      teardown()
    }
  })

  it('a single-line body is unaffected by the paste mode either way', async () => {
    for (const active of [false, true]) {
      const client = resolvingClient('echo hunter2')
      const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
      try {
        const renderer = rendererOf(content)
        editorOf(content).hide()
        renderer.bracketedPasteActive.mockReturnValue(active)
        await expect(content.insertSnippet('echo {{secret:pi@far}}')).resolves.toEqual({
          ok: true,
          where: 'pty',
        })
        expect(renderer.paste).toHaveBeenCalledWith('echo hunter2')
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

  it('a paste that reports no write is a refusal, not a reported delivery', async () => {
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      {},
      resolvingClient('echo hunter2'),
    )
    try {
      const session = sessionOf(content)
      const renderer = rendererOf(content)
      editorOf(content).hide()
      renderer.paste.mockReturnValue(false)
      const sentBefore = session.send.mock.calls.length
      await expect(content.insertSnippet('echo {{secret:pi@far}}')).resolves.toEqual({
        ok: false,
        reason: 'write-failed',
      })
      expect(session.send.mock.calls.length).toBe(sentBefore)
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

  it('the xterm boundary (the renderer chord registration) delegates to the SAME opener', async () => {
    const onSnippetChord = vi.fn()
    const { content, teardown } = await mountTerminal(makeClipboard(), {
      hooks: { onSnippetChord },
    })
    try {
      rendererOf(content)._fireSnippetChord()
      expect(onSnippetChord).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })

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
      { profileId: '', host: '192.168.0.57', user: 'root' },
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

  it('an SSH prompt shows the location chip the block header would carry', async () => {
    const { content, tab, teardown } = await mountSshTerminal()
    try {
      content.setVisible(true)
      // The chip is fed at session open from ONE derivation
      // (this.locationLine()); no stream marker may change it (ADR-0024 §1).
      const chip = tab.pane.querySelector<HTMLElement>('.nocx-editor-location')
      expect(chip).not.toBeNull()
      expect(chip!.style.display).not.toBe('none')
      expect(chip!.textContent).toBe('root@192.168.0.57')
      // The block header never appears in the severed product: blocks are
      // a completion projection with no stream (or app) trigger.
      expect(tab.pane.querySelector('.cmd-header-location')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('a local session grows no location chip', async () => {
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {
      attachToDocument: true,
    })
    try {
      content.setVisible(true)
      const chip = tab.pane.querySelector<HTMLElement>('.nocx-editor-location')
      expect(chip).not.toBeNull()
      expect(chip!.style.display).toBe('none')
      expect(chip!.textContent).toBe('')
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
    const el = withEditor.editor.root.querySelector<HTMLElement>('.nocx-editor-recovery')
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

      // Fire markers to reach integrated + editor = healthy.
      const renderer = rendererOf(content)
      renderer._fireCommandMarker({ kind: 'A', line: 0, col: 0, buffer: 'normal' })
      renderer._fireCommandMarker({ kind: 'B', line: 0, col: 0, buffer: 'normal' })

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
        generation: 'est-0000000000000000',
      })
      expect(editorOf(content).isVisible).toBe(true)
      expect(client.dispatcher.call).toHaveBeenCalledWith('lifecycle.establishAck', {
        sessionId: session.sessionId,
        lane: 'lane-1',
        domain: 'd1',
        epoch: 1,
        generation: 'est-0000000000000000',
      })
    } finally {
      teardown()
    }
  })

  // ADR-0024 decision 9 across an AD-9 reconnect. The acknowledgement is what
  // flushes the backend's pending ACCEPT; nothing else does. An ack that was
  // in flight when the socket dropped is rejected by the dispatcher
  // (rejectAllPending) and the backend never saw it, so its accept is still
  // pending — and the reattach replay carries the SAME generation, because
  // only a fresh shell hello mints a new one. The renderer must therefore
  // treat "sent" and "landed" as different states: claiming the generation
  // optimistically suppressed the one retry that could still complete the
  // handshake, and the tab stayed conventional until the accept expired.
  it('re-acknowledges the replayed generation when the first acknowledgement never landed', async () => {
    const client = makeClient()
    const acks: unknown[] = []
    let failNext = true
    client.dispatcher.call.mockImplementation((method: string, params: unknown) => {
      if (method !== 'lifecycle.establishAck') return Promise.resolve({})
      acks.push(params)
      if (failNext) {
        failNext = false
        return Promise.reject(new Error('ws closed'))
      }
      return Promise.resolve({ accepted: true })
    })
    const { teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = lifecycleHandler(client)
      const fact = {
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
        generation: 'est-0000000000000000',
      }
      handler(fact)
      await Promise.resolve()
      await Promise.resolve()
      expect(acks).toHaveLength(1)

      // The reattach replay: same lane, same domain, same generation.
      handler(fact)
      await Promise.resolve()
      await Promise.resolve()
      expect(acks).toHaveLength(2)

      // And once it HAS landed, a further replay is not acknowledged again —
      // the accept is flushed and a second ack would only be refused.
      handler(fact)
      await Promise.resolve()
      await Promise.resolve()
      expect(acks).toHaveLength(2)
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
    recovery: { fence: 'ab'.repeat(32), generation: 'ab'.repeat(32) },
  } as const
  const WRONG_FENCE = 'cd'.repeat(32)

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

  it('only the exact pre-provisioned fence is acknowledged, once, with the session id and generation', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const renderer = rendererOf(content)
      const handler = lifecycleHandler(client)
      const call = client.dispatcher.call
      handler(LOST_WITH_RECOVERY)

      // A wrong fence — a hostile byte, a different episode — changes
      // nothing: the renderer never pattern-matches, it matches the nonce.
      renderer._fireRecoveryFence(WRONG_FENCE)
      expect(call).not.toHaveBeenCalledWith('lifecycle.recoverAck', expect.anything())

      // The shell's one-shot fence (the exact pre-provisioned nonce)
      // triggers exactly one acknowledgement, carrying only the session id
      // and the generation — nothing else.
      renderer._fireRecoveryFence(LOST_WITH_RECOVERY.recovery.fence)
      renderer._fireRecoveryFence(LOST_WITH_RECOVERY.recovery.fence) // a repeat sighting must not double-ack
      const sid = client._sessions[0].sessionId
      expect(call).toHaveBeenCalledTimes(1)
      expect(call).toHaveBeenCalledWith('lifecycle.recoverAck', {
        sessionId: sid,
        generation: LOST_WITH_RECOVERY.recovery.generation,
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
      rendererOf(content)._fireRecoveryFence(LOST_WITH_RECOVERY.recovery.fence)
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
      rendererOf(content)._fireRecoveryFence(LOST_WITH_RECOVERY.recovery.fence)
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
      rendererOf(content)._fireRecoveryFence(LOST_WITH_RECOVERY.recovery.fence)
      expect(
        pending,
        'a fresh bind must claim its recovery acknowledgement without inheriting the old bind',
      ).toHaveLength(2)

      // The old bind settles after the new bind has claimed the same-shaped
      // recovery contract. It must not clear the new episode.
      pending[0]?.resolve({ accepted: true })
      await Promise.resolve()
      await Promise.resolve()
      const recoveryChip =
        editorOf(content).root.querySelector<HTMLElement>('.nocx-editor-recovery')
      expect(recoveryChip?.style.display).toBe('none')

      // The current acknowledgement refuses. A genuinely fresh episode must
      // be able to claim its own acknowledgement rather than inheriting the
      // old bind's in-flight guard.
      pending[1]?.reject(new Error('session is not open'))
      await Promise.resolve()
      await Promise.resolve()
      expect(recoveryChip?.style.display).toBe('none')
      const freshRecovery = {
        fence: '12'.repeat(32),
        generation: 'ef'.repeat(32),
      }
      secondHandler({
        sessionId: second.sessionId,
        lane: 'lane-1',
        lifecycle: 'lost',
        recovery: freshRecovery,
      })
      rendererOf(content)._fireRecoveryFence(freshRecovery.fence)
      expect(pending).toHaveLength(3)
    } finally {
      teardown()
    }
  })
})

describe('the establishment acknowledgement (ADR-0024 decision 9)', () => {
  // The renderer half of the gate: the backend withholds the shell's ACCEPT
  // — and therefore the shell's authority to suppress its native prompt —
  // until this acknowledgement says the editor presentation is committed.
  // Silence here is the fail-open direction, not a no-op: the handshake
  // times out and the session stays a conventional terminal.
  it('acknowledges a prompt_ready generation exactly once, after the fact is applied', async () => {
    const client = makeClient()
    const { teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = lifecycleHandler(client)
      const call = client.dispatcher.call

      // No generation on the fact: there is no establishment episode open,
      // so there is nothing to release and nothing is acknowledged.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(call).not.toHaveBeenCalledWith('lifecycle.establishAck', expect.anything())

      // A live transition and the post-open replay can carry the same
      // backend-minted generation. Both apply idempotently, but only one
      // acknowledgement may claim the generation.
      const establishment = {
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
        generation: 'est-0000000000000000',
      } as const
      handler(establishment)
      handler(establishment)
      const sid = client._sessions[0].sessionId
      expect(call).toHaveBeenCalledWith('lifecycle.establishAck', {
        sessionId: sid,
        lane: 'lane-1',
        domain: 'd1',
        epoch: 1,
        generation: 'est-0000000000000000',
      })
      expect(
        call.mock.calls.filter((c: unknown[]) => c[0] === 'lifecycle.establishAck'),
      ).toHaveLength(1)
    } finally {
      teardown()
    }
  })
  it('does not let an old bind acknowledgement complete the new establishment episode', async () => {
    const client = makeClient()
    const pending: Array<{ resolve: (value: unknown) => void }> = []
    client.dispatcher.call.mockImplementation((method: string) => {
      if (method !== 'lifecycle.establishAck') return Promise.resolve({})
      let resolve!: (value: unknown) => void
      const promise = new Promise<unknown>((release) => {
        resolve = release
      })
      pending.push({ resolve })
      return promise
    })
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const first = client._sessions[0]
      const subscriptions = () =>
        client.dispatcher.subscribe.mock.calls.filter(
          (call: unknown[]) => call[0] === 'lifecycle.changed',
        )
      const firstHandler = subscriptions()[0]?.[1] as (params: unknown) => void
      firstHandler({
        sessionId: first.sessionId,
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
        generation: 'est-old',
      })
      expect(pending).toHaveLength(1)

      const onExit = first.onExit.mock.calls[0]?.[0] as
        ((exit: { sessionId: string; cause: 'interrupted' }) => void) | undefined
      onExit?.({ sessionId: first.sessionId, cause: 'interrupted' })
      expect(await content.reconnect()).toBe(true)

      const second = client._sessions[1]
      const secondHandler = subscriptions()[1]?.[1] as (params: unknown) => void
      const freshFact = {
        sessionId: second.sessionId,
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        generation: 'est-new',
      } as const
      secondHandler(freshFact)
      expect(pending).toHaveLength(2)

      // The new bind lands first. The old bind then resolves late and must
      // not overwrite which generation the current bind has acknowledged.
      pending[1]?.resolve({ accepted: true })
      await Promise.resolve()
      await Promise.resolve()
      pending[0]?.resolve({ accepted: true })
      await Promise.resolve()
      await Promise.resolve()

      secondHandler(freshFact)
      expect(
        client.dispatcher.call.mock.calls.filter(
          (call: unknown[]) => call[0] === 'lifecycle.establishAck',
        ),
        'a late acknowledgement from the old bind must not mint a duplicate acknowledgement for the current bind',
      ).toHaveLength(2)
    } finally {
      teardown()
    }
  })

  it('never acknowledges a fact that names no live domain', async () => {
    const client = makeClient()
    const { teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const handler = lifecycleHandler(client)
      const call = client.dispatcher.call
      // native and lost carry no establishment; a running fact is past the
      // gate. None of them may release an accept.
      handler({ lane: 'lane-1', lifecycle: 'native' })
      handler({ lane: 'lane-1', lifecycle: 'running', domain: 'd1', epoch: 1, generation: 'est-1' })
      expect(call).not.toHaveBeenCalledWith('lifecycle.establishAck', expect.anything())
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

// The SSH block header regression (nocx-a44m): the cwd chip used to park in
// the dead centre of the header because `.cmd-header-chips` separated its
// children with `justify-content: space-between` — right for two children
// (a local block is [cwd, right]) and wrong for three (an SSH block adds the
// location chip, and three children space evenly). Fixed in 30014e3 by
// pushing the right group out with its own `margin-left: auto`, which behaves
// identically for any child count. jsdom computes no layout, so these
// assertions pin what jsdom CAN see: the DOM order that expresses the intent
// ("cwd left, duration and exit right"), and the stylesheet's structural
// contract that turns that order into position without assuming a count.
describe('the SSH block header keeps cwd left and duration/exit right (nocx-a44m)', () => {
  const container = (): HTMLElement => document.createElement('div')
  const store = (): CommandSnapshotStore => new CommandSnapshotStore()
  const noop = (): void => {}

  it('orders an SSH block header location, cwd, then the right group', () => {
    const el = createCommandBlock(
      'command',
      1,
      'deploy',
      '/srv/www',
      'user@server', // location — the chip that made the header three children
      '<span class="term-line">done</span>',
      1200,
      0,
      'success',
      container,
      noop,
      store(),
      'shell',
    )
    const chips = el.querySelector('.cmd-header-chips')
    expect(chips).not.toBeNull()
    const loc = chips?.querySelector('.cmd-header-location')
    const cwd = chips?.querySelector('.cmd-header-cwd')
    const right = chips?.querySelector('.cmd-header-right')
    expect(loc).not.toBeNull()
    expect(cwd).not.toBeNull()
    expect(right).not.toBeNull()

    const order = [...(chips as HTMLElement).children]
    expect(order.indexOf(loc as HTMLElement)).toBeLessThan(order.indexOf(cwd as HTMLElement))
    expect(order.indexOf(cwd as HTMLElement)).toBeLessThan(order.indexOf(right as HTMLElement))

    // The right group holds what belongs on the right: duration and exit.
    expect(right?.querySelector('.cmd-header-duration')).not.toBeNull()
    expect(right?.querySelector('.cmd-header-exit-ok')).not.toBeNull()
  })

  it('keeps cwd before the right group on a local block too', () => {
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
    const chips = el.querySelector('.cmd-header-chips')
    const cwd = chips?.querySelector('.cmd-header-cwd')
    const right = chips?.querySelector('.cmd-header-right')
    expect(cwd).not.toBeNull()
    expect(right).not.toBeNull()
    const order = [...(chips as HTMLElement).children]
    expect(order.indexOf(cwd as HTMLElement)).toBeLessThan(order.indexOf(right as HTMLElement))
  })

  it('the stylesheet pushes the right group with its own auto margin, not space-between', () => {
    const css: string = readFileSync(STYLE_ENTRY, 'utf8')
    const chips = stripComments(extractRuleBlock(css, 'cmd-header-chips') ?? '')
    const right = stripComments(extractRuleBlock(css, 'cmd-header-right') ?? '')
    expect(chips).not.toBe('')
    expect(right).not.toBe('')

    // space-between assumes exactly two children; the location chip made the
    // SSH header three. The container must not distribute, and the right
    // group must carry its own auto margin — the mechanism that behaves
    // identically for any child count (nocx-a44m).
    expect(chips).not.toMatch(/justify-content\s*:\s*(space-between|space-around|space-evenly)/)
    expect(right).toMatch(/margin-left\s*:\s*auto/)
  })
})

// The command editor's chrome row has the same latent class as the SSH
// header above: `justify-content: space-between` is only correct for exactly
// two children (left group + clock), and the row must not break if a third
// joins it. Same fix, same contract assertion: the row does not distribute,
// and the clock — the right-edge element — carries its own auto margin
// (nocx-a44m).
describe('the command editor chrome pins the clock to the right edge without distributing (nocx-a44m)', () => {
  it('the stylesheet gives the clock its own auto margin, not space-between on the row', () => {
    const css: string = readFileSync(STYLE_ENTRY, 'utf8')
    const chrome = stripComments(extractRuleBlock(css, 'nocx-editor-chrome') ?? '')
    const time = stripComments(extractRuleBlock(css, 'nocx-editor-time') ?? '')
    expect(chrome).not.toBe('')
    expect(time).not.toBe('')

    expect(chrome).not.toMatch(/justify-content\s*:\s*(space-between|space-around|space-evenly)/)
    expect(time).toMatch(/margin-left\s*:\s*auto/)
  })
})

describe('summoned editor overlay stylesheet contract (nocx-92gfl)', () => {
  it('keeps ordered answers scrolling above the composer in one absolute pane stack', () => {
    const css = stripComments(readFileSync(STYLE_ENTRY, 'utf8'))
    const baseCss = stripComments(readFileSync(BASE_STYLE_ENTRY, 'utf8'))
    const stack = stripComments(extractRuleBlock(css, 'nocx-summon-stack') ?? '')
    const answers = stripComments(extractRuleBlock(css, 'nocx-summon-answers') ?? '')
    const editor =
      css.match(
        /\.nocx-summon-stack\s*>\s*\.nocx-editor\[data-placement=['"]overlay['"]\]\s*\{([^}]*)\}/,
      )?.[1] ?? ''
    const pane = baseCss.match(/\.pane\s*\{([^}]*)\}/)
    expect(stack).not.toBe('')
    expect(answers).not.toBe('')
    expect(editor).not.toBe('')
    expect(pane).not.toBeNull()
    expect(pane?.[1] ?? '').toMatch(/--pane-inline-padding\s*:\s*10px/)
    expect(stack).toMatch(/position\s*:\s*absolute/)
    expect(stack).toMatch(/left\s*:\s*var\(--pane-inline-padding\)/)
    expect(stack).toMatch(/right\s*:\s*var\(--pane-inline-padding\)/)
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
    // capture behind it no longer a second painter of those rows.
    expect(stack).toMatch(/background\s*:\s*var\(--color-canvas\)/)
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
    const renderer = rendererOf(content)
    try {
      content.setVisible(true)
      // Even a full marker cycle cannot reach 'editor': the presentation
      // axis is severed (ADR-0024 §1) — no ownership, no editor.
      renderer._fireCommandMarker({ kind: 'A', line: 0, col: 0, buffer: 'normal' })
      renderer._fireCommandMarker({ kind: 'B', line: 0, col: 0, buffer: 'normal' })
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
    const renderer = rendererOf(content)
    try {
      expect(content.activeOrigin()?.cwdVerified).toBe(false)
      renderer._fireCwd('host', '/srv/new/path')
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
    const renderer = rendererOf(content)
    try {
      // The session open itself is an origin transition (null → origin).
      expect(onActiveOriginChange).toHaveBeenCalledTimes(1)

      // A verified OSC 7 cwd changes the answer.
      renderer._fireCwd('host', '/srv/new/path')
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
      integrationHandler(client)({
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
      // The hand-typed `ssh pi@192.168.0.93`.
      handler({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd2',
        epoch: 1,
        destination: { host: '192.168.0.93', user: 'pi' },
      })
      expect(content.portsTargetId).toBeNull()
      // The reason is the same user@host the block header shows (the one
      // existing derivation, not a second one).
      expect(content.portsUnavailableReason).toBe('pi@192.168.0.93')
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
        destination: { host: '192.168.0.93', user: 'pi' },
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
      // The user's own escape: the editor hides, keys route raw.
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
    client.call.mockImplementation((method: string) => {
      if (method === 'history.record') {
        return Promise.resolve({
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
    const renderer = rendererOf(content)
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
        attempt: { id: 'att-g', state: 'open', origin: 'app', command: 'echo sk-proj-abcdef' },
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
      const menuButton = rec.el.querySelector<HTMLElement>('.cmd-overflow-btn')
      expect(menuButton).not.toBeNull()
      menuButton!.click()
      const menuItems = Array.from(
        document.querySelectorAll<HTMLElement>('.cmd-overflow-menu-item'),
      )
      const grant = menuItems.find((item) => item.dataset.action === 'grant')
      const stop = menuItems.find((item) => item.dataset.action === 'stop')
      expect(grant).toBeDefined()
      expect(grant?.textContent).toBe('Ask about this block')
      grant!.click()
      const grantsBeforeAck = (content as unknown as { grantedBlocks: GrantBlock[] }).grantedBlocks
      expect(grantsBeforeAck[0]?.command).toBe('echo sk-proj-abcdef')
      expect(stop).toBeUndefined()
      document.querySelector('.cmd-overflow-menu')?.remove()

      // The ack lands here. It used to be refused for the class alone and
      // dropped for good — no retry, nothing shown, nothing logged.
      await vi.waitFor(() =>
        expect(client.call.mock.calls.some((c) => c[0] === 'history.record')).toBe(true),
      )
      await Promise.resolve()
      await Promise.resolve()

      // The fence lands and the visual freeze replaces the element. The
      // receipt must be on the NEW element — the one the user is looking at.
      renderer._fireRenderFence({ hex: FENCE, line: 3, buffer: 'normal' })
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
    const callMock = client.call
    callMock.mockImplementation((method: string) => {
      if (method === 'history.record') {
        return Promise.resolve({
          maskedCount: 0,
          maskedKinds: [],
          entryId: 'e1',
          source: 'user',
          redactions: [],
          captures: [],
          maskedCommand: 'make',
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
      // The app-owned submit opened a ledger record and a running block.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')

      // The published attempt: the shell start attaches, then completes.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'make' },
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

      // The block froze with the authenticated status.
      const frozen = withScrollback.scrollback.blockManager.blocks[0]
      expect(frozen.status).toBe('success')
      expect(frozen.exitCode).toBe(0)
      expect(frozen.attemptId).toBe('att-1')
      expect(frozen.el.dataset.entryId).toBe('att-1')
      expect(grantBlockFromElement(frozen.el)?.itemId).toBe('att-1')
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()

      // History persisted the app-owned text, authorized by the attempt.
      const recordCall = callMock.mock.calls.find((c) => c[0] === 'history.record')
      expect(recordCall).toBeTruthy()
      const params = recordCall![1] as { command: string; status: string; exitCode: number }
      expect(params.command).toBe('make')
      expect(params.status).toBe('success')
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
      teardown()
    }
  })

  it('submitAgentCommand runs the command through the ordinary path with the agent author and resolves with the completed run body (nocx-tjppv)', async () => {
    const client = makeClient()
    const callMock = client.call
    callMock.mockImplementation((method: string) => {
      if (method === 'history.record') {
        return Promise.resolve({
          maskedCount: 0,
          maskedKinds: [],
          entryId: 'e1',
          source: 'assistant',
          redactions: [],
          captures: [],
          maskedCommand: 'make',
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

      // The ordinary path ran: the ledger record and the running block were
      // BOTH minted at submit with the agent's author (design §3.1) — the
      // command exists as a command, not as bytes (criterion 3: asserted on
      // the ledger, not the DOM).
      expect(ledger.records()).toHaveLength(1)
      expect(ledger.records()[0].author).toBe('agent')
      expect(ledger.records()[0].command).toBe('make')
      expect(ledger.records()[0].status).toBe('running')
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.blocks[0].author).toBe('agent')
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')
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
      // the store — history.record's close moves the status and leaves the
      // column alone. An attempt submitted without it came back from a
      // restart as the person's command (nocx-1druc, agent-restore.spec.ts).
      expect((attemptCall![1] as { source: string }).source).toBe('assistant')

      // The attempt attaches and completes: the block freezes with the exit
      // status, exactly as a human command's does.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'make' },
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

      const run = await pending
      // THE ENTRY ID IS THE STORE'S, and it is the one history.record's ack
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

  it('an agent command the store wrote no row for resolves naming no entry at all (nocx-9sqii)', async () => {
    // History is off, or the record was dropped: the ack names no row. The
    // command still RAN and its output is the tool's result, so the run
    // resolves — with nothing where the entry id goes, which is the honest
    // answer and the one the backend degrades on (no join, plain ledger
    // order). Answering the renderer's own record number here instead is
    // what made the join fail silently when there WAS a row.
    const client = makeClient()
    const callMock = client.call
    callMock.mockImplementation((method: string) => {
      if (method === 'history.record') {
        return Promise.resolve({
          maskedCount: 0,
          maskedKinds: [],
          entryId: '',
          source: 'assistant',
          redactions: [],
          captures: [],
          maskedCommand: 'make',
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
      const pending = content.submitAgentCommand('make')
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'make' },
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
        attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'agent-command' },
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
      await pendingAgent

      // The human's command, through the same content's editor: still the
      // human's, in the same ledger, on the same flow.
      ed.insertText('human-command')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

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

  it('reports the lane buffer kind to the backend on every buffer change and at session open (ADR-0020 decision 3)', async () => {
    // The backend cannot see the alternate screen (AD-6 — it never sniffs
    // the byte stream), so the renderer reports the buffer kind it owns
    // and the backend decides the awaiting-takeover transition from it:
    // a program that takes the alternate screen demotes the agent.
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      const laneCalls = () =>
        client.call.mock.calls.filter((c) => c[0] === 'agent.laneInteractivity')
      const renderer = rendererOf(content)
      const session = sessionOf(content)

      // The open re-reported the CURRENT kind once the session had a
      // backend id (a change before open had no session to name).
      expect(laneCalls().length).toBeGreaterThanOrEqual(1)
      expect(laneCalls()[0][1]).toEqual({ sessionId: session.sessionId, bufferKind: 'normal' })

      // A program enters the alternate screen: reported as it happens.
      renderer._fireBufferChange('alternate')
      await vi.waitFor(() => {
        expect(laneCalls()[laneCalls().length - 1]?.[1]).toEqual({
          sessionId: session.sessionId,
          bufferKind: 'alternate',
        })
      })
      // The TUI exits: the lane leaves awaiting-takeover, reported again.
      renderer._fireBufferChange('normal')
      await vi.waitFor(() => {
        expect(laneCalls()[laneCalls().length - 1]?.[1]).toEqual({
          sessionId: session.sessionId,
          bufferKind: 'normal',
        })
      })
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
    const callMock = client.call
    let recordCalls = 0
    callMock.mockImplementation((method: string) => {
      if (method === 'history.record') {
        recordCalls++
        return Promise.resolve({
          maskedCount: 0,
          maskedKinds: [],
          entryId: 'e1',
          source: 'user',
          redactions: [],
          captures: [],
          maskedCommand: 'echo hello',
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
    const renderer = rendererOf(content)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    /* eslint-disable @typescript-eslint/unbound-method */
    const pasteSpy = renderer.paste
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
      //    published start, and the app-owned attempt opens a running block.
      ed.insertText('echo hello')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)
      expect(withScrollback.scrollback.blockManager.blocks[0].status).toBe('running')
      // The app-owned attempt opens BEFORE the pty write; the write itself
      // lands in the microtask after the submit RPC settles.
      await vi.waitFor(() => expect(pasteSpy).toHaveBeenCalledWith('echo hello'))

      // 3. The authenticated start attaches to the app attempt; the pane
      //    stays in the running layout with the block visible — output is
      //    never hidden by the lifecycle (nocx-u7uh.25).
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'echo hello' },
      })
      expect(withScrollback.scrollback.mode).toBe('running')
      expect(
        withScrollback.scrollback.scrollbackInner.classList.contains('inner-fullscreen-mode'),
      ).toBe(false)

      // 4. Output lands and stays visible while the command runs.
      renderer.write('hello\n')
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
      const frozen = withScrollback.scrollback.blockManager.blocks[0]
      expect(frozen.status).toBe('failure')
      expect(frozen.exitCode).toBe(1)
      expect(withScrollback.scrollback.blockManager.runningBlock).toBeNull()

      // 6. The exit status persists exactly once — one history.record for
      //    the completed app-owned attempt.
      await vi.waitFor(() => expect(recordCalls).toBe(1))
      const recordCall = callMock.mock.calls.find((c) => c[0] === 'history.record')
      const params = recordCall![1] as { command: string; status: string; exitCode: number }
      expect(params.command).toBe('echo hello')
      expect(params.status).toBe('failure')
      expect(params.exitCode).toBe(1)

      // 7. PromptReady returns the editor.
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)

      // 8. The next submitted command reaches the shell and opens a fresh
      //    attempt — the cycle restarts, not the previous block.
      ed.insertText('echo again')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() => expect(pasteSpy).toHaveBeenLastCalledWith('echo again'))
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
    const { content, ed, view, teardown } = await mountTerminal(makeClipboard(), {}, client)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    try {
      const renderer = rendererOf(content)
      // The typed interface says setReadOnly(boolean); the mock clears its
      // call history — reached through a cast, like the existing tests do.
      const readOnlyMock = (renderer as unknown as { setReadOnly: ReturnType<typeof vi.fn> })
        .setReadOnly
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      expect(readOnlyMock).toHaveBeenLastCalledWith(true)
      readOnlyMock.mockClear()

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
      expect(readOnlyMock).toHaveBeenLastCalledWith(false)

      // The published attempt opens the running interval; the sync keeps
      // the grid writable — a program waiting on stdin (read, ssh, less)
      // is fed by raw keys with no editor and no input surface lost.
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'read x' },
      })
      // Still gone, still writable: the lifecycle only RECONCILES here — the
      // commit already removed the composer — and reconciling must not
      // resurrect it or take the keyboard back off the program.
      expect(ed.isVisible).toBe(false)
      expect(ed.root.style.display).toBe('none')
      expect(readOnlyMock).toHaveBeenLastCalledWith(false)
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
      expect(readOnlyMock).toHaveBeenLastCalledWith(true)
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
      const renderer = rendererOf(content)
      const focusMock = (renderer as unknown as { focus: ReturnType<typeof vi.fn> }).focus
      const handler = factHandler(client)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      expect(ed.isVisible).toBe(true)
      focusMock.mockClear()

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
      expect(focusMock).toHaveBeenCalled()
    } finally {
      Element.prototype.scrollTo = protoScrollTo
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
      const renderer = rendererOf(content)
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
      renderer._fireData('h')
      renderer._fireData('i')
      renderer._fireData('\r')
      // Held: not one byte of it has been sent yet.
      expect(session.send).not.toHaveBeenCalled()

      // The attempt settles, the command goes out, and what was typed
      // behind it follows — in the order it was typed.
      await vi.waitFor(() => expect(session.send).toHaveBeenCalled())
      // The command FIRST, whole, then its '\r', then the answer somebody
      // typed while it was in flight. The command travels through the
      // renderer's paste (it owns bracketed-paste wrapping) and a paste is
      // an onData, so it is subject to the same hold — which is why the
      // hold is lifted before the write rather than after it.
      expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual([
        'read x',
        '\r',
        'h',
        'i',
        '\r',
      ])
      // `paste` is a method declaration on TerminalRenderer, so referencing it
      // detached trips unbound-method; the mock property type does not.
      const pasteMock = renderer as unknown as { paste: ReturnType<typeof vi.fn> }
      expect(pasteMock.paste).toHaveBeenCalledWith('read x')
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
      rendererOf(content)._fireRenderFence({
        hex: 'f'.repeat(64),
        line: 3,
        buffer: 'normal',
      })
      const firstAfter = withScrollback.scrollback.blockManager.blockForAttempt('att-1')
      expect(firstAfter?.status).toBe('failure')
      expect(firstAfter?.exitCode).toBe(130)
      expect(firstAfter?.endLine).toBe(3)
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

  it('the app-owned submit opens the block before the bytes and marks the echo line outside the output range (nocx-4yhi)', async () => {
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
      // The block opened at submit — BEFORE the bytes — on the prompt line
      // (fixture cursorLine is 0). The shell's echo of `ls` will land on
      // that same line, so the block's OUTPUT range starts one row later;
      // the creation line and the output range are two different things.
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      expect(block!.startLine).toBe(0)
      expect(block!.outputStart).toBe(1)
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
      // The user typed at the shell: the command was echoed as they typed,
      // and the running fact landed after it — so the block's output range
      // is exactly where the block opened, no echo to skip.
      expect(block!.startLine).toBe(block!.outputStart)
    } finally {
      teardown()
    }
  })

  it('the first PARSED write stands the running command\u2019s stand-in down (nocx-vnirv.1)', async () => {
    // THE WIRING TEST for the seam the block manager exposes and this
    // file's onWriteParsed handler is the call site of. A running command
    // carries the same "working, nothing written yet" stand-in a turn does,
    // in the live region where its output will appear; it must stand down
    // at the first byte of output — and "arrives" means the renderer has
    // PARSED the write, not that bytes were handed over (write() parses
    // asynchronously, so onData firing is not output existing).
    //
    // This is the check that failed before the wiring existed:
    // noteCommandOutput had no caller outside blocks.test.ts, so the first
    // parsed write left the stand-in in place — a live block, reached
    // through the same interface as every other block, with its write half
    // unreachable, the exact shape AGENTS.md names as invisible to
    // deadcode's RTA. The drive is the real mount: the authenticated
    // running fact opens the block, and the frame double fires
    // onWriteParsed exactly as xterm's parse pass does (xterm.test.ts
    // exercises the same event off the real renderer).
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const renderer = rendererOf(content)
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
      const live = withScrollback.scrollback.xtermLiveContainer
      // The command is running and nothing has been parsed: the stand-in
      // stands where the first output line will be written.
      expect(live.querySelector('.cmd-answer-typing')).not.toBeNull()
      // The first parsed write stands it down.
      renderer._fireWriteParsed()
      expect(live.querySelector('.cmd-answer-typing')).toBeNull()
      // Idempotent: every later chunk fires the same event and nothing
      // changes — the seam is called again, not rebuilt.
      renderer._fireWriteParsed()
      expect(live.querySelector('.cmd-answer-typing')).toBeNull()
    } finally {
      teardown()
    }
  })

  it('sizes the live region when the grid has PARSED the bytes, not when they were handed over', async () => {
    // xterm parses asynchronously. The measure used to be scheduled from
    // session.onData, one line after renderer.write(), so it ran on the
    // animation frame BEFORE the chunk was in the buffer and sized the region
    // to the grid as it was WITHOUT the output. A command that prints
    // everything in one chunk has no next chunk to correct it, so it ran at
    // the wrong size until it finished and then arrived all at once: `seq 1
    // 10` measured three rows live and froze eleven, and the block leapt
    // 153px up the pane at the end of a command that had already finished
    // (2026-08-19 frame capture, e2e container).
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    const handler = factHandler(client)
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const renderer = rendererOf(content)
    const raf = globalThis.requestAnimationFrame
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      Object.defineProperty(withScrollback.scrollback.scrollbackArea, 'clientHeight', {
        value: 300,
        configurable: true,
      })
      withScrollback.scrollback.scrollbackArea.scrollTo = vi.fn()
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-1', state: 'open', origin: 'shell', command: 'seq' },
      })
      expect(withScrollback.scrollback.mode).toBe('running')
      const before = withScrollback.scrollback.xtermLiveContainer.style.height

      // The bytes arrive and the grid ALREADY reports the taller content —
      // but they have not been parsed, so nothing may be measured yet.
      ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(200)
      client._sessions[0].fireData('one\ntwo\nthree\n')
      expect(withScrollback.scrollback.xtermLiveContainer.style.height).toBe(before)

      // The parse pass settles: NOW the rows exist and the region is sized.
      renderer._fireWriteParsed()
      expect(withScrollback.scrollback.xtermLiveContainer.style.height).toBe('200px')
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
    const renderer = rendererOf(content)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    const raf = globalThis.requestAnimationFrame
    const fitViewport = renderer.fitViewport
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    }
    try {
      content.setVisible(true)
      // The initial fit uses the delivered viewport (jsdom reports no
      // layout, so clientHeight is 0 at this point).
      content.viewportChanged({ width: 800, height: 400 })
      expect(fitViewport).toHaveBeenLastCalledWith(
        expect.objectContaining({ width: 800, height: 400 }),
      )

      // A real scroller height, and a running block header that occupies
      // part of it: grid and live box share the scroller, so the grid must
      // be fitted to scroller MINUS header — the same cap setLiveHeight
      // clamps the box to. A taller grid's last rows sit outside the box
      // (overflow: hidden) and are unreachable: the omp report, where the
      // composer at the bottom of the program could not be scrolled to.
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
      expect(withScrollback.scrollback.mode).toBe('running')
      const block = withScrollback.scrollback.blockManager.runningBlock
      expect(block).not.toBeNull()
      block!.el.getBoundingClientRect = () => ({
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

      // Output taller than the pane: the box is capped at scroller minus
      // header (276) and the grid must be fitted to the SAME 276 — not the
      // bare scroller, which would leave the last rows clipped and
      // unscrollable inside the box.
      ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(1000)
      client._sessions[0].fireData('repaint')
      renderer._fireWriteParsed()
      expect(withScrollback.scrollback.xtermLiveContainer.style.height).toBe('276px')
      expect(fitViewport).toHaveBeenLastCalledWith(
        expect.objectContaining({ width: 800, height: 276 }),
      )
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
    const renderer = rendererOf(content)
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
      block!.el.getBoundingClientRect = () => ({
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
      const live = withScrollback.scrollback.xtermLiveContainer
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

      // The first sizing sets the height; the box grows to the cap.
      ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(276)
      client._sessions[0].fireData('top frame')
      renderer._fireWriteParsed()
      expect(withScrollback.scrollback.xtermLiveContainer.style.height).toBe('276px')
      expect(scrollTo).toHaveBeenCalled()
      scrollTo.mockClear()

      // `top` repaints the same rows: the height does not change, so the
      // early return holds and the scroll is NOT yanked while the user
      // reads (nocx-6w4z).
      ;(renderer.liveContentHeight as LiveContentHeightSpy).mockReturnValue(276)
      client._sessions[0].fireData('top frame')
      renderer._fireWriteParsed()
      expect(withScrollback.scrollback.xtermLiveContainer.style.height).toBe('276px')
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
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const renderer = rendererOf(content)
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

      // omp's repaint writes its own prompt marks. Under the severed
      // lifecycle these are render-only: no setIdle, no zero-height live
      // region, no editor, no keyboard handoff.
      renderer._fireCommandMarker({ kind: 'B', line: 12, col: 0, buffer: 'normal' })
      renderer._fireCommandMarker({ kind: 'A', line: 12, col: 0, buffer: 'normal' })
      expect(withScrollback.scrollback.mode).toBe('running')
      expect(withScrollback.scrollback.xtermLiveContainer.className).not.toContain('live-idle')
      expect(withScrollback.scrollback.xtermLiveContainer.style.height).not.toBe('0px')
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
      expect(sb.xtermLiveContainer.style.height).toBe('400px')

      // The card is dismissed (or the window grew): the scroller is taller.
      Object.defineProperty(sb.scrollbackArea, 'clientHeight', {
        value: 560,
        configurable: true,
      })
      content.viewportChanged({ width: 800, height: 560 })
      expect(sb.xtermLiveContainer.style.height).toBe('560px')
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
      const renderer = rendererOf(content)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      // vim enters the alternate buffer: the program takes the pane.
      renderer._fireBufferChange('alternate')
      expect(scrollbackOf(content).mode).toBe('fullscreen')
      // vim quits: leaving the alternate buffer must land on the structured
      // idle layout, not the flat conventional grid — the live domain
      // entitles the session to the block model on the way out too.
      renderer._fireBufferChange('normal')
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
    const renderer = rendererOf(content)
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
      renderer._fireBufferChange('alternate')
      expect(scrollbackOf(content).mode).toBe('fullscreen')
      // vim exits before the completion fact lands: the command cycle still
      // owns the live region, so the pane stays in the running layout until
      // the authenticated completion freezes the block.
      renderer._fireBufferChange('normal')
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
      const renderer = rendererOf(content)
      // Native: no domain, a conventional terminal. The alt-screen path
      // must leave it exactly as it found it — a flat grid.
      renderer._fireBufferChange('alternate')
      expect(scrollbackOf(content).mode).toBe('fullscreen')
      renderer._fireBufferChange('normal')
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
  it('a submit at a live prompt opens the attempt with the app-owned text BEFORE the pty write', async () => {
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
      expect(submitAttempt).toHaveBeenCalledWith('lifecycle.submitAttempt', {
        domain: 'd1',
        command: 'make deploy',
        cwd: FIXTURE_CWD,
        host: '',
        source: 'user',
      })
      expect(session.send).not.toHaveBeenCalled()
      // The running block opened at submit, before any fact could arrive —
      // the published running fact always finds the block it binds to.
      expect(withScrollback.scrollback.blockManager.blocks).toHaveLength(1)

      // The backend answers: only now do the bytes go out.
      resolveAttempt({
        id: 'att-9',
        domain: 'd1',
        state: 'open',
        command: 'make deploy',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        startedAt: '2026-08-08T12:00:00Z',
      })
      await vi.waitFor(() =>
        expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual(['make deploy', '\r']),
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
          command: 'make deploy',
        },
      })
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
    const renderer = rendererOf(content)
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
        }),
      )
      expect(session.send).not.toHaveBeenCalled()
      resolveAttempt({
        id: 'att-10',
        domain: 'd1',
        state: 'open',
        command: 'make --token={{secret:TOKEN}}',
        cwd: FIXTURE_CWD,
        host: '',
        origin: 'app',
        startedAt: '2026-08-08T12:00:00Z',
      })
      await vi.waitFor(() =>
        expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual([
          'make --token=SECRETVALUE',
          '\r',
        ]),
      )
      const pasteMock = renderer as unknown as { paste: ReturnType<typeof vi.fn> }
      expect(pasteMock.paste).toHaveBeenCalledWith('make --token=SECRETVALUE')
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

      // The write goes out on the synchronous path and no attempt-open call
      // was ever made — nothing is fabricated for a conventional terminal.
      expect(submitAttempt).not.toHaveBeenCalled()
      expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual(['make deploy', '\r'])
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
      expect(session.send).toHaveBeenCalledTimes(1)
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
        expect(session.send.mock.calls.map((c: unknown[]) => c[0])).toEqual(['make deploy', '\r']),
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

  /** Press one of the card's own actions, by the label the user reads. */
  const press = (tab: { pane: HTMLElement }, label: string): void => {
    const found = [...cardIn(tab)!.querySelectorAll('button')].find(
      (b) => (b.textContent ?? '').trim() === label,
    )
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
      press(first.tab, '×')
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
      press(tab, '×')
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
      press(tab, '×')
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
      press(tab, '×')
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
      press(first.tab, '×')
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
        startedAt: '2026-08-08T12:00:00Z',
      })
    })
    return { client, dispatcherCalls }
  }

  /** A frozen command block through the REAL manager chain. */
  function frozenBlockOf(
    content: TerminalContent,
    command = 'ls',
    output = ['total 12', 'docs'],
  ): HTMLElement {
    const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
    const manager = scrollback.blockManager
    manager.startBlock(command, '~', 0)
    manager.bindAttempt(`att-fixture-${manager.blocks.length}`)
    const lines = output.map((t) => new BufferLine(t))
    const frozen = manager.freezeBlock((y) => lines[y], lines.length - 1, 0)
    expect(frozen).not.toBeNull()
    return frozen!.el
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

  /** The indicator as rendered in the editor's gutter — the editor's DOM,
   *  deliberately NOT its contentDOM: the token is beside the document and
   *  never in it (see the gutter test below for what that buys). */
  function indicatorOf(ed: CommandEditor): HTMLElement | null {
    return viewOf(ed).dom.querySelector<HTMLElement>('.ui-mode-indicator')
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

      // ── Plain Enter: the shell receives the line ─────────────────────
      const sentBefore = sessionOf(content).send.mock.calls.length
      ed.insertText('echo hi')
      submitKey(ed)
      // The shell got the command and its CR — the ordinary handoff — and
      // no question ever crossed the control plane.
      expect(sessionOf(content).send.mock.calls.length).toBe(sentBefore + 2)
      expect(dispatcherCalls.find((c) => c.method === 'agent.ask')).toBeUndefined()
      // The handoff hid the editor; the registry never moved.
      expect(ed.isVisible).toBe(false)
      expect(activeLabel(content)).toBe('Shell')

      // ── ⌘Enter then Enter: the assistant receives the line ────────────
      // The editor is re-shown (the next prompt, as the lifecycle would);
      // the indicator still renders what the registry reports.
      ed.show()
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      const sentAfterShell = sessionOf(content).send.mock.calls.length
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
      // (The running block from the earlier `echo hi` is untouched — the
      // ask neither opened one of its own nor disturbed the shell's.)
      expect(sessionOf(content).send.mock.calls.length).toBe(sentAfterShell)
      expect(dispatcherCalls.find((c) => c.method === 'lifecycle.submitAttempt')).toBeUndefined()
      expect(dispatcherCalls.find((c) => c.method === 'history.record')).toBeUndefined()
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      expect(scrollback.blockManager.runningBlock?.command).toBe('echo hi')
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
      // The shell submit still reached the pty: nothing regressed.
      expect(sessionOf(content).send.mock.calls.length).toBeGreaterThan(0)
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
      // The block that opened at the same submit carries the badge — the
      // whole happy path, driven through the real orchestration, never a
      // manufactured block.
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

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const items = Array.from(
        document.querySelectorAll<HTMLButtonElement>('.cmd-overflow-menu-item'),
      )
      expect(items.find((item) => item.dataset.action === 'grant')).toBeUndefined()
      expect(items.map((item) => item.textContent)).toContain('Copy command')
      expect(ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')?.style.display).toBe(
        'none',
      )
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

      whole.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const menuGrant = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
      )
      expect(menuGrant).not.toBeNull()
      menuGrant!.click()

      const before = [...grantState.grantedBlocks]
      expect(before).toHaveLength(2)
      expect(before[0]).toMatchObject({ blockEl: rows, start: 1, count: 1 })
      expect(before[1]).toMatchObject({ blockEl: whole })
      const chip = ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')!
      chip.click()
      expect(document.querySelector('.ui-floating-panel[data-open="true"]')).not.toBeNull()
      expect(whole.dataset.granted).toBe('true')
      expect(rows.querySelectorAll('.term-line[data-granted]')).toHaveLength(1)

      // Hiding the pane removes every body-level and inline grant surface,
      // including a menu that otherwise lives outside the pane subtree.
      whole.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      expect(document.querySelector('.cmd-overflow-menu')).not.toBeNull()
      content.setVisible(false)
      expect(chip.style.display).toBe('none')
      expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
      expect(document.querySelector('.cmd-overflow-menu')).toBeNull()
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

      whole.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      expect(document.querySelector('.cmd-overflow-menu-item[data-action="grant"]')).toBeNull()

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

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      expect(document.querySelector('.cmd-overflow-menu-item[data-action="grant"]')).not.toBeNull()

      // Programmatic, because that is the case the pane-hide sweep cannot
      // reach: ask entry and restore both call setActive without anybody
      // clicking. A menu left open goes on offering Mark in Run.
      registry.setActive('shell')

      expect(document.querySelector('.cmd-overflow-menu')).toBeNull()
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
      const chip = ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')!

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
      const chip = ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')!
      selectRows(block, 0, 1)
      document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!.click()
      selectRows(block, 2, 3)
      document.querySelector<HTMLButtonElement>('.mark-affordance .ui-button')!.click()
      expect(chip.textContent).toContain('2')

      // The menu speaks about the BLOCK, so with any of it marked its action
      // is Unmark — and it takes the row marks with it rather than leaving
      // half of them behind under a label that says nothing is marked.
      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const action = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
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
      const chip = ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')!
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

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
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
      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      document
        .querySelector<HTMLButtonElement>('.cmd-overflow-menu-item[data-action="grant"]')!
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
      marked.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      document
        .querySelector<HTMLButtonElement>('.cmd-overflow-menu-item[data-action="grant"]')!
        .click()
      const grantChip = ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')!
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
      // gutter is outside the document, so the text is the text.
      const view = viewOf(ed)
      expect(view.dom.querySelector('.ui-mode-indicator')).not.toBeNull()
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

  it('the gutter reserves its column for EVERY line, so a second line starts where the first one does (nocx-ex636)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      _resetThemeState()
      ed.show()

      // The widget this replaced sat on line one only, so line two began
      // underneath the token and the command read as two ragged columns.
      // A gutter has one element per line by construction: the marker on
      // the first, an empty cell on the rest, both the same width.
      ed.insertText('one\ntwo')
      const view = viewOf(ed)
      const cells = Array.from(
        view.dom.querySelectorAll('.nocx-editor-target-gutter .cm-gutterElement'),
      )
      // The spacer element CM6 keeps for measurement is in this list too;
      // what matters is that the lines have their cells and only the first
      // carries the token.
      expect(cells.length).toBeGreaterThanOrEqual(2)
      const withToken = cells.filter((c) => c.querySelector('.ui-mode-indicator'))
      expect(withToken.length).toBeGreaterThanOrEqual(1)
      expect(view.contentDOM.textContent).toBe('onetwo')
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

// ── The atlas repaint on becoming visible (nocx-e27, nocx-jfgb) ───────────
//
// Panes are hidden with a CSS class, not unmounted, so the WebGL texture
// atlas of a hidden pane goes stale and its glyphs come back drawn from
// wrong coordinates. nocx-e27 fixed that with a viewport-wide repaint on
// activation; 21fd7f6a (the PaneContent seam) carried refreshAtlas() down
// into TerminalContent and left the call behind in PaneManager, so the fix
// became dead code and the corruption came back.
//
// The repaint therefore hangs off setVisible, which is where visibility is
// owned — a caller that has to remember is a caller that forgets.
describe('TerminalContent visibility repaints the grid', () => {
  it('repaints the whole viewport when the pane becomes visible (nocx-jfgb)', async () => {
    const { content, teardown } = await mountTerminal()
    try {
      const repaint = vi.spyOn(rendererOf(content), 'refreshAtlas')

      content.setVisible(true)
      expect(repaint).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })

  it('does not repaint when the pane is hidden — the atlas is stale, not the screen', async () => {
    const { content, teardown } = await mountTerminal()
    try {
      const repaint = vi.spyOn(rendererOf(content), 'refreshAtlas')

      content.setVisible(false)
      expect(repaint).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('still toggles the active class, so the repaint is added to setVisible and not swapped for it', async () => {
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
    const renderer = rendererOf(content)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('herdr')
      submitEnter(view)
      expect(tab.title).toBe('herdr')

      // Absolute order: the program's own title outranks the command name.
      renderer._fireTitle('herdr 2.0')
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
    const renderer = rendererOf(content)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('herdr')
      submitEnter(view)
      expect(tab.title).toBe('herdr')

      // The program sets its own title and clears it on the way out —
      // an OSC 0/2 with an EMPTY string (tabs.ts documents the gesture).
      renderer._fireTitle('herdr')
      renderer._fireTitle('')
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
    const renderer = rendererOf(content)
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
      renderer._fireTitle('herdr')
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
    const renderer = rendererOf(content)
    const restoreScroll = stubScrolling()
    try {
      content.setVisible(true)
      // A program title is a name of its own, so the strip's second line is
      // the LOCATION line — which is where the strip names a machine.
      renderer._fireTitle('herdr')

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
        destination: { host: '192.168.0.93', user: 'pi' },
      })

      const subtitle = onSubtitleChange.mock.lastCall?.[0] as string
      expect(subtitle).toBe('pi@192.168.0.93')
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
      expect(client.openSession.mock.calls[0][2]).toEqual({ paneId: PANE })
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
      expect(client.openSession.mock.calls[0][2]).toEqual({})
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
      expect(client.openSSHSession.mock.calls[0][3]).toEqual({ paneId: PANE })
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
      expect(client.openSSHSessionByHost.mock.calls[0][4]).toEqual({ paneId: PANE })
    } finally {
      teardown()
    }
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// A frozen block sends what it printed (nocx-2f0f)
// ═══════════════════════════════════════════════════════════════════════════
describe('a frozen block sends what it printed (nocx-2f0f)', () => {
  // The whole feature, through the seam a person reaches: they type a command,
  // it finishes, and the text it printed leaves for the store. Everything
  // below the assertion is the ordinary path — the app-owned submit, the
  // authenticated completion, the render fence, the history.record ack — and
  // none of it is stubbed past the socket.
  it('takes the entry from the record ack and sends the body against it', async () => {
    const FENCE = 'd'.repeat(64)
    const client = makeClient()
    client.call.mockImplementation((method: string) => {
      if (method === 'history.record') {
        return Promise.resolve({
          maskedCount: 0,
          maskedKinds: [],
          entryId: 'e-capture',
          // Required by the contract, and load-bearing: the renderer refuses
          // an ack whose source is not the one it minted (design §3.1), so a
          // fixture without it is a backend that dropped the fact.
          source: 'user',
          redactions: [],
          maskedCommand: 'echo hello',
          captures: [],
        })
      }
      if (method === 'ledger.capture') {
        return Promise.resolve({ artifactId: 'a-1', stored: true })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
    const { view, ed, content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    // The lifecycle fact handler the content registered on the fake
    // dispatcher — the seam authenticated facts arrive through.
    const handler = lifecycleHandler(client)
    const renderer = rendererOf(content)
    /* eslint-disable @typescript-eslint/unbound-method */
    const protoScrollTo = Element.prototype.scrollTo
    const protoScrollIntoView = Element.prototype.scrollIntoView
    /* eslint-enable @typescript-eslint/unbound-method */
    Element.prototype.scrollTo = () => {}
    Element.prototype.scrollIntoView = () => {}
    try {
      content.setVisible(true)
      handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
      ed.insertText('echo hello')
      view.contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: { id: 'att-c', state: 'open', origin: 'app', command: 'echo hello' },
      })
      handler({
        lane: 'lane-1',
        lifecycle: 'running',
        domain: 'd1',
        epoch: 1,
        attempt: {
          id: 'att-c',
          state: 'completed',
          exitCode: 0,
          fence: FENCE,
          completedAt: '2026-08-08T12:00:02Z',
        },
      })
      await vi.waitFor(() =>
        expect(client.call.mock.calls.some((c) => c[0] === 'history.record')).toBe(true),
      )
      // The visual freeze is what produces the bodies, and it waits for the
      // fence. Until it runs there is nothing to send — which is why the ack
      // parks rather than being dropped.
      renderer._fireRenderFence({ hex: FENCE, line: 3, buffer: 'normal' })

      await vi.waitFor(() =>
        expect(client.call.mock.calls.some((c) => c[0] === 'ledger.capture')).toBe(true),
      )
      const sent = client.call.mock.calls
        .filter((c) => c[0] === 'ledger.capture')
        .map((c) => c[1] as { entryId: string; mediaType: string; seq: number })
      expect(sent[0].entryId).toBe('e-capture')
      expect(sent[0].mediaType).toBe('application/vt')
      expect(sent[0].seq).toBe(1)
      // Both bodies: the durable one and the derived text the second names it
      // from. One without the other is half the artifact pair.
      await vi.waitFor(() =>
        expect(
          client.call.mock.calls
            .filter((c) => c[0] === 'ledger.capture')
            .some((c) => (c[1] as { mediaType: string }).mediaType === 'text/plain'),
        ).toBe(true),
      )
    } finally {
      Element.prototype.scrollTo = protoScrollTo
      Element.prototype.scrollIntoView = protoScrollIntoView
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
    const renderer = rendererOf(content)
    try {
      // The session-open cwd is a provider's fallback, not a report: nothing
      // has been verified yet, so nothing may be stored (AD-5).
      expect(reported).toEqual([])

      renderer._fireCwd('', '/repo/frontend')
      expect(reported).toEqual(['/repo/frontend'])

      // A shell prints its prompt many times in one directory. The row is
      // written on CHANGE, or sitting still would cost a write per prompt.
      renderer._fireCwd('', '/repo/frontend')
      expect(reported).toEqual(['/repo/frontend'])

      renderer._fireCwd('', '/repo/internal')
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
    const renderer = rendererOf(content)
    try {
      // /home/pi on a Raspberry Pi is not /home/pi here. Storing it would
      // send the restored pane to a directory that does not exist on this
      // machine — or, worse, to a different one that does.
      renderer._fireCwd('pi.local', '/home/pi')
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

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const mark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
      )
      expect(mark?.textContent).toBe('Ask about this block')
      mark?.click()

      const grantState = content as unknown as { grantedBlocks: GrantBlock[] }
      const viaMenu = grantState.grantedBlocks[0]
      expect(viaMenu?.itemId).toBe('e-1')
      expect(ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')?.textContent).toContain(
        '1',
      )

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
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

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const mark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
      )
      expect(mark?.textContent).toBe('Ask about this block')
      mark?.click()

      const grantState = content as unknown as { grantedBlocks: GrantBlock[] }
      expect(grantState.grantedBlocks[0]?.itemId).toBe('e-1')
      expect(ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')?.textContent).toContain(
        '1',
      )
      const chip = ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')!
      chip.click()
      expect(
        ed.root.querySelector<HTMLElement>('.ui-floating-panel[data-variant="grant"]')?.textContent,
      ).toContain('make test')

      block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
      const unmark = document.querySelector<HTMLButtonElement>(
        '.cmd-overflow-menu-item[data-action="grant"]',
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
      expect(untimed.querySelector('.cmd-header-duration')).toBeNull()
      // And the other fact still says itself out loud, so the absence above
      // reads as "unknown" and never as "the chip was dropped".
      expect(instant.querySelector('.cmd-header-duration')?.textContent).toBe('0ms')
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

      turnBlock!.querySelector<HTMLElement>('.cmd-overflow-btn')!.click()
      const copyOut = Array.from(
        document.querySelectorAll<HTMLElement>('.cmd-overflow-menu-item'),
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
    const live = client.call.getMockImplementation()! as (
      method: string,
      params?: unknown,
    ) => Promise<unknown>
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
        startedAt: '2026-08-08T12:00:00Z',
      })
    })
    return { client, state }
  }

  const chipEls = (content: TerminalContent): HTMLElement[] =>
    Array.from(editorOf(content).root.querySelectorAll<HTMLElement>('.nocx-editor-model')).filter(
      (el) => el.style.display !== 'none',
    )

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
   *  its own specs (nocx-a7mw7.6). */
  const switchToAsk = (content: TerminalContent): void => {
    const el = viewOf(editorOf(content)).dom.querySelector<HTMLElement>('.ui-mode-indicator')
    if (!el) throw new Error('no mode indicator to switch with')
    el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
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
      const grant = editorOf(content).root.querySelector<HTMLElement>('.nocx-editor-grant')!
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
      const left = editorOf(content).root.querySelector<HTMLElement>('.nocx-editor-chrome-left')!
      const children = [...left.children]
      const grant = left.querySelector<HTMLElement>('.nocx-editor-grant')!
      const visibleModels = chipEls(content)
      expect(visibleModels).toHaveLength(2)
      expect(visibleModels.every((chip) => children.indexOf(chip) < children.indexOf(grant))).toBe(
        true,
      )
      const chips = children.filter((child) => child.classList.contains('nocx-chip'))
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
    (content as unknown as { scrollback: ScrollbackController }).scrollback.xtermLiveContainer

  /** The grid's writability seam. Reached through a cast because the typed
   *  interface says setReadOnly(boolean) while the mock is a spy — the same
   *  trade the ownership tests above make. */
  const readOnlyOf = (content: TerminalContent): ReturnType<typeof vi.fn> =>
    (rendererOf(content) as unknown as { setReadOnly: ReturnType<typeof vi.fn> }).setReadOnly

  /** The registry's active target id — read through the product's own
   *  account of it, the mode indicator's data-target, wherever the editor
   *  is on screen to carry one. */
  function targetNamed(ed: CommandEditor): string | null {
    const el = viewOf(ed).dom.querySelector<HTMLElement>('.ui-mode-indicator')
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
    const renderer = rendererOf(content)
    if (typeof renderer.captureLiveFrame !== 'function') {
      renderer.captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
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
      attempt: { id: 'att-run', state: 'open', origin: 'app', command },
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
      const renderer = rendererOf(content)
      const session = sessionOf(content)
      const dimensions = { cols: renderer.cols, rows: renderer.rows }
      const resizeCalls = session.sendResize.mock.calls.length
      session.send.mockClear()
      // The editor is gone while it runs — that is deliberate and it
      // is the state this gesture exists for.
      expect(ed.isVisible).toBe(false)
      capturedActionFacts.length = 0

      await summonChord(content)

      const summonedFacts = capturedActionFacts[capturedActionFacts.length - 1]
      expect(summonedFacts).toEqual(expect.objectContaining({ presentation: 'editor' }))
      const recovery = ed.root.querySelector<HTMLElement>('.nocx-editor-recovery')
      expect(recovery?.style.display).toBe('none')

      expect(ed.isVisible).toBe(true)
      expect(ed.root.dataset.placement).toBe('overlay')
      // The gesture is what attaches, not the kind of program: the person
      // pressed the chord over THIS command, the product froze THIS screen
      // and says so on the badge below. An ordinary running command is no
      // exception — a summon that pins a photograph and then sends nothing
      // is where the owner's zero-count report came from (nocx-hp8p2.4).
      expect(ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label')).toContain(
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
      expect({ cols: renderer.cols, rows: renderer.rows }).toEqual(dimensions)
      expect(session.send).not.toHaveBeenCalled()
      expect(session.sendResize).toHaveBeenCalledTimes(resizeCalls)
      // And the grid is read-only again, which is the invariant
      // _syncLifecycleOwnership states about itself: editor shown ⟺ grid
      // read-only.
      expect(readOnlyOf(content)).toHaveBeenLastCalledWith(true)
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
      scrollback.beginBlockNow('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      scrollback.blockManager.freezeBlock(() => undefined, 0, 0)
      const renderer = rendererOf(content)
      renderer._fireBufferChange('alternate')
      renderer._fireWriteParsed()

      await summonChord(content)

      expect(ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label')).toContain(
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
      scrollback.beginBlockNow('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      const renderer = rendererOf(content)
      renderer._fireBufferChange('alternate')
      renderer._fireWriteParsed()

      await summonChord(content)

      // The program is still painting its screen — it has never frozen, so the
      // attachment owner is the running block itself. Without this the
      // assistant is asked about a screen nothing handed it.
      expect(ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label')).toContain(
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
      scrollback.beginBlockNow('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      rendererOf(content)._fireWriteParsed()

      await summonChord(content)

      // procps `top` owns the whole viewport without taking the alternate
      // screen, and it is the program the owner reported this against. The
      // gesture, not the buffer kind, is what says which screen the question
      // carries.
      expect(ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label')).toContain(
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
      scrollback.beginBlockNow('top', '~', 0)
      scrollback.blockManager.bindAttempt('att-run')
      rendererOf(content)._fireWriteParsed()

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
      const renderer = rendererOf(content)
      renderer.captureLiveFrame = vi.fn(
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
      rendererOf(content).captureLiveFrame = vi.fn().mockRejectedValue(new Error('capture refused'))

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
      const renderer = rendererOf(content)
      const focus = vi.spyOn(renderer, 'focus')
      focus.mockClear()
      const write = Object.getOwnPropertyDescriptor(renderer, 'write')?.value as Mock<
        (data: string) => void
      >
      const liveOutput = document.createElement('span')
      gridOf(content).appendChild(liveOutput)
      write.mockImplementation((data: string) => {
        liveOutput.textContent += data
      })
      write.mockClear()
      sessionOf(content).fireData('during freeze\n')
      expect(write).toHaveBeenCalledWith('during freeze\n')

      escapeOn(viewOf(ed).contentDOM)

      expect(ed.isVisible).toBe(false)
      expect(ed.root.dataset.placement).toBe('inline')
      expect(readOnlyOf(content)).toHaveBeenLastCalledWith(false)
      expect(focus).toHaveBeenCalledTimes(1)
      // The assertion that matters is not the flag: it is a key the PROGRAM
      expect(content.pinnedFrame()).toBeNull()
      expect(gridOf(content).textContent).toContain('during freeze\n')
      expect(
        gridOf(content).parentElement?.parentElement?.querySelector('.nocx-freeze-frame'),
      ).toBeNull()
      expect(ed.root.querySelector('[role="status"]')).toBeNull()
      // consumes arriving at the session. `q` is what quits `top`, and if
      // the editor still owned input it would be a letter in a draft.
      const session = sessionOf(content)
      session.send.mockClear()
      rendererOf(content)._fireData('q')
      expect(session.send).toHaveBeenCalledWith('q')
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
  it("Escape dismisses an accepted summon when xterm's helper textarea owns focus", async () => {
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
      const renderer = rendererOf(content)
      renderer.paste.mockClear()
      ed.insertText('rm -rf /tmp/x')
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      expect(renderer.paste).not.toHaveBeenCalled()
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
  // root (flip the target), a capture listener on the xterm host (summon),
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
    function frozenBlock(content: TerminalContent, command: string, output: string[]): HTMLElement {
      const manager = (content as unknown as { scrollback: ScrollbackController }).scrollback
        .blockManager
      manager.startBlock(command, '~', 0)
      manager.bindAttempt(`att-chord-${manager.blocks.length}`)
      const lines = output.map((t) => new BufferLine(t))
      const frozen = manager.freezeBlock((y) => lines[y], lines.length - 1, 0)
      expect(frozen).not.toBeNull()
      return frozen!.el
    }

    /** THE PROMPT FINISHED PAINTING. nocx.bash appends the OSC 133 B marker
     *  to PS1 as its final action, so B rides the prompt's last byte and the
     *  parse pass that carried it is the pass the prompt's redraw ends in —
     *  which is why the marker and the write-parsed fire together here, in
     *  that order, exactly as xterm's OSC handler and onWriteParsed do. */
    function promptPainted(renderer: ReturnType<typeof rendererOf>): void {
      renderer._fireCommandMarker({ kind: 'B', line: 0, col: 0, buffer: 'normal' })
      renderer._fireWriteParsed()
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
        frozenBlock(content, 'top', ['screen marker'])
        const renderer = rendererOf(content)
        // The prompt came back and finished painting — the screen is nobody's
        // as of this pass.
        promptPainted(renderer)
        // …and then the background child repainted it. That write, parsed
        // after the handback, is what makes this screen worth attaching; the
        // finished-command case below has no such write and is refused.
        renderer._fireWriteParsed()
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        renderer.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)

        await vi.waitFor(() =>
          expect(ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label')).toContain(
            'frozen screen attached automatically',
          ),
        )
        expect(captureLiveFrame).toHaveBeenCalledTimes(1)
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
        frozenBlock(content, 'old command', ['old output'])
        const renderer = rendererOf(content)
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        renderer.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        await settleAttachment()

        expect(
          ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label'),
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
    it('does not attach a finished command whose prompt redrew and nothing else painted', async () => {
      const { ed, content, teardown } = await mountTerminal(makeClipboard(), {
        attachToDocument: true,
      })
      try {
        content.setVisible(true)
        ed.show()
        ed.focus()
        frozenBlock(content, 'echo beta', ['beta'])
        const renderer = rendererOf(content)
        // The prompt's own bytes, parsed after the freeze — the one write
        // this screen will ever see again, and it ends in B. Fire it WITHOUT
        // the marker and this test goes green on a broken product, because a
        // bare write after the freeze is what the defect mistook for a live
        // screen.
        promptPainted(renderer)
        const captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        renderer.captureLiveFrame = captureLiveFrame

        chordOn(viewOf(ed).contentDOM)
        await settleAttachment()

        expect(targetNamed(ed)).toBe('agent')
        expect(
          ed.root.querySelector('.nocx-editor-grant')?.getAttribute('aria-label'),
        ).not.toContain('frozen screen attached automatically')
        expect(captureLiveFrame).not.toHaveBeenCalled()
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
        const renderer = rendererOf(content)
        if (typeof renderer.captureLiveFrame !== 'function') {
          renderer.captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())
        }
        gridOf(content).blur()
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
        const block = frozenBlock(content, 'git status', ['clean'])
        block.querySelector<HTMLButtonElement>('.cmd-overflow-btn')!.click()
        expect(document.querySelector('.cmd-overflow-menu')).not.toBeNull()

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
      expect(readOnlyOf(content)).toHaveBeenLastCalledWith(false)
      const session = sessionOf(content)
      session.send.mockClear()
      rendererOf(content)._fireData('q')
      expect(session.send).toHaveBeenCalledWith('q')
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
      const renderer = rendererOf(content)
      const session = sessionOf(content)
      renderer._fireBufferChange('alternate')
      const dimensions = { cols: renderer.cols, rows: renderer.rows }
      const resizeCalls = session.sendResize.mock.calls.length

      await summonChord(content)

      expect(ed.isVisible).toBe(true)
      expect(ed.root.dataset.placement).toBe('overlay')
      expect(targetNamed(ed)).toBe('agent')
      expect({ cols: renderer.cols, rows: renderer.rows }).toEqual(dimensions)
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
    const selection = {
      anchorNode: null as Node | null,
      focusNode: null as Node | null,
      isCollapsed: false,
      rangeCount: 1,
      toString: () => {
        return 'selected output'
      },
    }
    const selectionSpy = vi
      .spyOn(window, 'getSelection')
      .mockReturnValue(selection as unknown as Selection)
    const targets: Array<{ name: string; element: HTMLElement; take?: () => void }> = []
    const scrollbackBackground = tab.pane.querySelector<HTMLElement>('.scrollback-area')
    expect(scrollbackBackground).not.toBeNull()
    const scrollbackInner = scrollbackBackground!.querySelector<HTMLElement>('.scrollback-inner')
    expect(scrollbackInner).not.toBeNull()

    const frozen = document.createElement('div')
    frozen.className = 'cmd-block'
    const frozenButton = document.createElement('button')
    frozenButton.className = 'cmd-overflow-btn'
    const selectedText = document.createTextNode('selected output')
    frozen.append(selectedText, frozenButton)
    scrollbackInner!.append(frozen)
    selection.anchorNode = selectedText
    selection.focusNode = selectedText
    targets.push({ name: 'frozen block', element: frozenButton })

    const chrome = document.createElement('button')
    chrome.className = 'nocx-editor-chrome'
    tab.pane.append(chrome)
    targets.push({ name: 'chrome', element: chrome })

    const priorTabIndex = scrollbackBackground!.getAttribute('tabindex')
    scrollbackBackground!.tabIndex = 0
    targets.push({ name: 'scrollback background', element: scrollbackBackground! })

    const menu = document.createElement('div')
    menu.className = 'cmd-overflow-menu'
    const menuItem = document.createElement('button')
    menuItem.className = 'cmd-overflow-menu-item'
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
        client.dispatcher.call.mockClear()
        sessionOf(content).signal.mockClear()
        const event = new KeyboardEvent('keydown', {
          key: 'c',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        })
        target.element.dispatchEvent(event)
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
      selectionSpy.mockRestore()
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
      // The grid's hidden textarea is where xterm parks focus; from there
      // the key becomes 0x03 on the data plane and the line discipline
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
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )

      // The command — not the turn — receives exactly one ordinary interrupt
      // through its signal ladder, and the half-typed question survives.
      expect(ed.getDoc()).toBe('what is it waiting on')
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
      session.send.mockClear()

      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )

      expect(session.send).not.toHaveBeenCalledWith('\r')
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
      expect(session.send).toHaveBeenCalledWith('\r')

      session.send.mockClear()
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'c',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      expect(session.send).toHaveBeenCalledWith('\x03')
    } finally {
      restore()
      teardown()
    }
  })

  // ── the ⋮ menu, which is what makes both gestures visible ────────────────

  /** Open the running block's overflow menu and return its items. */
  function runningBlockMenu(content: TerminalContent): HTMLElement[] {
    const btn = paneOf(content).querySelector<HTMLElement>('.cmd-block-running .cmd-overflow-btn')
    expect(btn, 'the running block has no ⋮ button').not.toBeNull()
    btn!.click()
    return Array.from(document.querySelectorAll<HTMLElement>('.cmd-overflow-menu-item'))
  }

  function itemNamed(items: HTMLElement[], action: string): HTMLElement | undefined {
    return items.find((el) => el.dataset.action === action)
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
        attempt: { id: 'att-run', state: 'open', origin: 'app', command: 'sleep 300' },
      })
      expect(ed.isVisible).toBe(false)
      rendererOf(content).captureLiveFrame = vi.fn().mockResolvedValue(defaultPinnedFrame())

      const runItems = runningBlockMenu(content)
      expect(itemNamed(runItems, 'grant')).toBeUndefined()
      expect(itemNamed(runItems, 'stop')?.textContent).toBe('Stop')
      paneOf(content).querySelector<HTMLElement>('.cmd-block-running .cmd-overflow-btn')!.click()

      await summonChord(content)
      await vi.waitFor(() => expect(ed.isVisible).toBe(true))
      expect(targetNamed(ed)).toBe('agent')
      const grant = itemNamed(runningBlockMenu(content), 'grant')
      expect(grant?.textContent).toBe('Ask about this block')
      grant?.click()

      const block = paneOf(content).querySelector<HTMLElement>('.cmd-block-running')
      expect(block?.dataset.granted).toBe('true')
      expect(ed.root.querySelector<HTMLButtonElement>('.nocx-editor-grant')?.dataset.state).toBe(
        'chosen',
      )

      escapeOn(view.contentDOM)
      expect(ed.isVisible).toBe(false)
      expect(targetNamed(ed)).toBe('shell')
      expect(block?.dataset.granted).toBeUndefined()
      expect(itemNamed(runningBlockMenu(content), 'grant')).toBeUndefined()
      paneOf(content).querySelector<HTMLElement>('.cmd-block-running .cmd-overflow-btn')!.click()

      await summonChord(content)
      const unmark = itemNamed(runningBlockMenu(content), 'grant')
      expect(unmark?.textContent).toBe('Unmark')
      unmark?.click()
      expect(block?.dataset.granted).toBeUndefined()
    } finally {
      restore()
      teardown()
      document.querySelectorAll('.cmd-overflow-menu').forEach((m) => m.remove())
    }
  })

  it('Stop remains available for an assistant command before lifecycle facts arrive', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    const restore = stubScrolling()
    try {
      content.setVisible(true)
      void content.submitAgentCommand('sleep 300')
      await Promise.resolve()
      const stop = itemNamed(runningBlockMenu(content), 'stop')
      expect(stop, 'an assistant command has no Stop before lifecycle facts').toBeDefined()
      stop!.click()
      expect(signalsSent(content)).toEqual(['stop'])
    } finally {
      restore()
      teardown()
      document.querySelectorAll('.cmd-overflow-menu').forEach((m) => m.remove())
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
        attempt: { id: 'att-run', state: 'open', origin: 'app', command: 'sleep 300' },
      })

      const stop = itemNamed(runningBlockMenu(content), 'stop')
      expect(stop, 'the running block’s menu offers no Stop').toBeDefined()
      stop!.click()

      // `stop`, not `interrupt`: the menu item promises the command is gone,
      // and the backend's ladder is what keeps that promise.
      expect(signalsSent(content)).toEqual(['stop'])
    } finally {
      restore()
      teardown()
      document.querySelectorAll('.cmd-overflow-menu').forEach((m) => m.remove())
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
        attempt: { id: 'att-shared', state: 'open', origin: 'app', command: 'top' },
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
      document.querySelectorAll('.cmd-overflow-menu').forEach((el) => el.remove())
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
// The BEL BYTE reaching onBell is proven against the real VT parser in
// renderers/xterm.test.ts ("onBell through the real parser"), including that
// the BEL terminating an OSC sequence does NOT ring. This file owns the
// other half of the chain: what the callback does when it fires.
describe('a program printing BEL (nocx-n3nfg)', () => {
  /** The bell only lights the dot on a pane that has SETTLED and is not the
   *  one being looked at (panes.ts markActivity): a shell's banner and first
   *  prompt are not unread output. The B marker is what a real prompt-end
   *  delivers, so the settle here is the one a user's session performs. */
  const settle = (content: TerminalContent): void => {
    rendererOf(content)._fireCommandMarker({ kind: 'B', line: 0, col: 0, buffer: 'normal' })
  }

  /** The notify.bell requests this pane sent, in order. */
  const bellCalls = (client: ClientFake): unknown[][] =>
    client.dispatcher.call.mock.calls.filter((c: unknown[]) => c[0] === 'notify.bell')

  it('reports the bell AND marks the tab, addressing the live session', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      settle(content)
      expect(tab.hasActivity).toBe(false)

      rendererOf(content)._fireBell()

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

      rendererOf(content)._fireBell()
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

  // The id is read at FIRE time, not at subscribe time: it is
  // server-authoritative (AD-7) and a reattach replaces it, so a captured one
  // would address the session the pane used to hold. With no session there is
  // nothing to address — and the half that never needed one still happens.
  it('marks the tab and reports nothing when the pane holds no session', async () => {
    const client = makeClient()
    const { content, tab, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      settle(content)
      ;(content as unknown as { session: SessionFake | null }).session = null

      rendererOf(content)._fireBell()

      expect(bellCalls(client)).toEqual([])
      expect(tab.hasActivity).toBe(true)
    } finally {
      teardown()
    }
  })
})
describe('session.read serves the frame the question is about (nocx-7l4ex.3)', () => {
  type ReadCall = { method: string; params: unknown }
  type ReadCell = { char: string }
  type ReadRow = { cells?: ReadCell[] }
  type ReadResolution = {
    requestId: string
    outcome: string
    rows?: ReadRow[]
    cursor?: unknown
    identity?: unknown
    range?: unknown
    error?: string
  }
  type ReadCallFunction = (method: string, params: unknown) => Promise<undefined>
  type ReadDispatcher = {
    handlers: Map<string, (params: unknown) => void>
    calls: ReadCall[]
    subscribe: (method: string, handler: (params: unknown) => void) => () => void
    call: ReadCallFunction
  }

  function readDispatcher(): ReadDispatcher {
    const handlers = new Map<string, (params: unknown) => void>()
    const calls: ReadCall[] = []
    const subscribe = (method: string, handler: (params: unknown) => void): (() => void) => {
      handlers.set(method, handler)
      return () => handlers.delete(method)
    }
    const call = vi.fn((method: string, params: unknown) => {
      calls.push({ method, params })
      return Promise.resolve(undefined)
    })
    return { handlers, calls, subscribe, call }
  }

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

  function wireText(params: unknown): string {
    const resolution = params as ReadResolution
    const rows = resolution.rows ?? []
    return rows.map((row) => (row.cells ?? []).map((cell) => cell.char).join('')).join('\n')
  }

  function startRunning(client: ClientFake, command = 'top'): void {
    const handler = lifecycleHandler(client)
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: { id: 'att-run', state: 'open', origin: 'app', command },
    })
  }

  function gridOf(content: TerminalContent): HTMLElement {
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    return withScrollback.scrollback.xtermLiveContainer
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

  function readCurrent(
    dispatcher: ReadDispatcher,
    content: TerminalContent,
    requestId: string,
  ): void {
    mountReadScreenHandler(dispatcher as unknown as Dispatcher, (sessionId) =>
      sessionId === content.sessionId() ? content : null,
    )
    dispatcher.handlers.get('agent.readScreenRequest')!({
      requestId,
      sessionId: content.sessionId(),
    })
  }

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
      const renderer = rendererOf(content)
      renderer._fireBufferChange('alternate')
      const capture = vi.fn().mockResolvedValue(frame('top: load 1.00\nTasks: 254 total'))
      renderer.captureLiveFrame = capture

      await summon(content)

      expect(capture).toHaveBeenCalledWith({ start: 0, end: renderer.rows })
      expect(gridOf(content).querySelector('.nocx-freeze-frame')?.textContent).toMatch(
        /^top: load 1\.00/,
      )
    } finally {
      teardown()
    }
  })

  it('returns the pinned frame for both reads in one question turn', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      const pinned = frame('top: load 1.00')
      const renderer = rendererOf(content)
      const capture = vi.fn().mockResolvedValue(pinned)
      renderer.captureLiveFrame = capture
      await summon(content)
      expect(capture).toHaveBeenCalledWith(undefined)
      capture.mockClear()

      const dispatcher = readDispatcher()
      readCurrent(dispatcher, content, 'read-1')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(1))
      readCurrent(dispatcher, content, 'read-2')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(2))

      const first = dispatcher.calls[0].params as ReadResolution
      const second = dispatcher.calls[1].params as ReadResolution
      expect(first).toMatchObject({ requestId: 'read-1', outcome: 'frame' })
      expect(second).toMatchObject({ requestId: 'read-2', outcome: 'frame' })
      expect(wireText(first)).toContain('top: load 1.00')
      expect(second.rows).toEqual(first.rows)
      expect(second.cursor).toEqual(first.cursor)
      expect(second.identity).toEqual(first.identity)
      expect(second.range).toEqual(first.range)
      expect(capture).not.toHaveBeenCalled()
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
      const renderer = rendererOf(content)
      const capture = vi.fn().mockResolvedValue(pinned)
      renderer.captureLiveFrame = capture
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

  it('answers a repainting top without waiting on the live capture fence', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client, 'top')
      const pinned = frame('top: pinned while repainting')
      const renderer = rendererOf(content)
      const capture = vi.fn().mockResolvedValue(pinned)
      renderer.captureLiveFrame = capture
      await summon(content)

      let liveCaptureStarted = false
      const blockedCapture = vi.fn(
        () =>
          new Promise<CapturedFrame>(() => {
            liveCaptureStarted = true
          }),
      )
      renderer.captureLiveFrame = blockedCapture

      await expect(content.captureLiveFrame()).resolves.toBe(pinned)
      expect(liveCaptureStarted).toBe(false)
      expect(blockedCapture).not.toHaveBeenCalled()
    } finally {
      teardown()
    }
  })

  it('keeps region reads live while the whole-screen read is pinned', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      const pinned = frame('pinned whole screen')
      const renderer = rendererOf(content)
      const capture = vi.fn().mockResolvedValue(pinned)
      renderer.captureLiveFrame = capture
      await summon(content)

      const live = frame('live selected rows')
      capture.mockResolvedValue(live)
      const result = await content.captureLiveFrame({ start: 3, end: 5 })

      expect(result).toBe(live)
      expect(capture).toHaveBeenLastCalledWith({ start: 3, end: 5 })
    } finally {
      teardown()
    }
  })

  it('keeps the renderer failed outcome when no frame can be produced', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      rendererOf(content).captureLiveFrame = vi.fn().mockRejectedValue(new Error('capture refused'))

      const dispatcher = readDispatcher()
      readCurrent(dispatcher, content, 'read-failed')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(1))

      expect(dispatcher.calls[0].params).toMatchObject({
        requestId: 'read-failed',
        outcome: 'failed',
        error: 'capture refused',
      })
    } finally {
      teardown()
    }
  })
  it('keeps the read pin through Escape until the stopped ask turn terminalizes', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      const pinned = frame('pinned while the answer streams')
      const live = frame('live after the answer ends')
      const renderer = rendererOf(content)
      const capture = vi.fn().mockResolvedValue(pinned)
      renderer.captureLiveFrame = capture
      client.dispatcher.call.mockImplementation((method: string) => {
        if (method === 'agent.ask') {
          return Promise.resolve({ runId: 42, entryId: 'entry-42', model: 'test-model' })
        }
        return Promise.resolve({
          id: 'att-0',
          domain: 'd1',
          state: 'open',
          command: '',
          cwd: '',
          host: '',
          origin: 'app',
          startedAt: '2026-08-08T12:00:00Z',
        })
      })

      await summon(content)
      capture.mockClear()
      const ed = editorOf(content)
      ed.insertText('why is this screen changing?')
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() =>
        expect(client.dispatcher.call.mock.calls.some(([method]) => method === 'agent.ask')).toBe(
          true,
        ),
      )

      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      expect(content.pinnedFrame()).toBeNull()

      const dispatcher = readDispatcher()
      readCurrent(dispatcher, content, 'read-after-escape-1')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(1))
      readCurrent(dispatcher, content, 'read-after-escape-2')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(2))
      const first = dispatcher.calls[0].params as ReadResolution
      const second = dispatcher.calls[1].params as ReadResolution
      expect(wireText(first)).toContain('pinned while the answer streams')
      expect(second.rows).toEqual(first.rows)
      expect(capture).not.toHaveBeenCalled()

      const runState = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )
      expect(runState).toBeDefined()
      ;(runState![1] as (params: unknown) => void)({ runId: 42, state: 'cancelled' })

      capture.mockResolvedValue(live)
      readCurrent(dispatcher, content, 'read-after-turn')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(3))
      expect(wireText(dispatcher.calls[2].params)).toContain('live after the answer ends')
      expect(capture).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })
  it('clears an unsettled turn pin when the next question starts without a summon', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(
      makeClipboard(),
      { attachToDocument: true },
      client,
    )
    try {
      content.setVisible(true)
      startRunning(client)
      const pinned = frame('stale first-turn frame')
      const live = frame('live second-turn frame')
      const renderer = rendererOf(content)
      const capture = vi.fn().mockResolvedValue(pinned)
      renderer.captureLiveFrame = capture
      let nextRun = 42
      client.dispatcher.call.mockImplementation((method: string) => {
        if (method === 'agent.status') {
          return Promise.resolve({
            endpointConfigured: true,
            credential: 'resolvable',
            lastProbe: null,
            answering: { ready: true, reason: null, endpoint: 'openrouter', model: 'test-model' },
          })
        }
        if (method === 'agent.ask') {
          nextRun += 1
          return Promise.resolve({
            runId: nextRun,
            entryId: `entry-${nextRun}`,
            model: 'test-model',
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
          startedAt: '2026-08-08T12:00:00Z',
        })
      })

      await summon(content)
      capture.mockClear()
      const ed = editorOf(content)
      ed.insertText('what is this first screen?')
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() =>
        expect(client.dispatcher.call.mock.calls.some(([method]) => method === 'agent.ask')).toBe(
          true,
        ),
      )

      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
      )
      lifecycleHandler(client)({
        lane: 'lane-1',
        lifecycle: 'prompt_ready',
        domain: 'd1',
        epoch: 1,
      })
      ed.show()
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', {
          key: 'Enter',
          ctrlKey: true,
          bubbles: true,
          cancelable: true,
        }),
      )
      ed.insertText('what is this second screen?')
      viewOf(ed).contentDOM.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
      )
      await vi.waitFor(() =>
        expect(
          client.dispatcher.call.mock.calls.filter(([method]) => method === 'agent.ask'),
        ).toHaveLength(2),
      )

      capture.mockResolvedValue(live)
      const dispatcher = readDispatcher()
      readCurrent(dispatcher, content, 'read-second-turn')
      await vi.waitFor(() => expect(dispatcher.calls).toHaveLength(1))
      expect(wireText(dispatcher.calls[0].params)).toContain('live second-turn frame')
      expect(capture).toHaveBeenCalledTimes(1)
    } finally {
      teardown()
    }
  })
})

describe('summoning answers instead of vanishing (nocx-og42r)', () => {
  function startRunning(contentClient: ClientFake): void {
    const handler = lifecycleHandler(contentClient)
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: { id: 'att-run', state: 'open', origin: 'app', command: 'htop' },
    })
  }

  function dispatchSummon(content: TerminalContent): KeyboardEvent {
    const event = new KeyboardEvent('keydown', {
      key: 'Enter',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    })
    const withScrollback = content as unknown as { scrollback: ScrollbackController }
    const scrollback = withScrollback.scrollback
    scrollback.xtermLiveContainer.dispatchEvent(event)
    return event
  }

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
      const renderer = rendererOf(content)
      const session = sessionOf(content)
      renderer._fireBufferChange('alternate')
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
  const rendererReadOnly = (content: TerminalContent): ReturnType<typeof vi.fn> =>
    (rendererOf(content) as unknown as { setReadOnly: ReturnType<typeof vi.fn> }).setReadOnly

  function startCommand(client: ClientFake): (params: unknown) => void {
    const handler = lifecycleHandler(client)
    handler({ lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    handler({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: { id: 'att-run', state: 'open', origin: 'app', command: 'top' },
    })
    return handler
  }

  function dispatchSummon(content: TerminalContent): KeyboardEvent {
    const grid = (content as unknown as { scrollback: ScrollbackController }).scrollback
      .xtermLiveContainer
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
    const renderer = rendererOf(content)
    renderer.captureLiveFrame = vi.fn().mockResolvedValue({
      rows: [],
      cursor: { line: 0, col: 0 },
      provenance: {
        source: 'live',
        identity: {
          buffer: { kind: 'normal' },
          cols: renderer.cols,
          rows: renderer.rows,
          generation: 0,
        },
        range: { start: 0, end: 0 },
        scrollbackCapLines: 10000,
      },
    } satisfies CapturedFrame)
    const grid = (content as unknown as { scrollback: ScrollbackController }).scrollback
      .xtermLiveContainer
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
      expect(
        (rendererOf(content).captureLiveFrame as ReturnType<typeof vi.fn>).mock.calls,
      ).toHaveLength(1)
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
      expect(rendererReadOnly(content)).toHaveBeenLastCalledWith(true)
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
      expect(rendererReadOnly(content)).toHaveBeenLastCalledWith(true)
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
      rendererOf(content)._fireRenderFence({ hex: commandFence, line: 3, buffer: 'normal' })
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
    client.call.mockImplementation((method: string) => {
      if (method === 'history.record') {
        return Promise.resolve({
          maskedCount: 0,
          maskedKinds: [],
          entryId: '',
          source: 'assistant',
          redactions: [],
          captures: [],
          maskedCommand: 'assistant command',
        })
      }
      return Promise.reject(new Error('no store wired (fake)'))
    })
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
      rendererOf(content)._fireRenderFence({ hex: commandFence, line: 3, buffer: 'normal' })
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
        attempt: { id: 'att-first', state: 'open', origin: 'app', command: 'first call' },
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
      rendererOf(content)._fireRenderFence({ hex: firstCallFence, line: 4, buffer: 'normal' })
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
        attempt: { id: 'att-second', state: 'open', origin: 'app', command: 'second call' },
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
      rendererOf(content)._fireRenderFence({ hex: secondCallFence, line: 5, buffer: 'normal' })
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
      rendererOf(content)._fireRenderFence({
        hex: commandFence,
        line: 3,
        buffer: 'normal',
      })
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
      rendererOf(content)._fireRenderFence({ hex: commandFence, line: 3, buffer: 'normal' })
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
        expect(answer.querySelector(':scope > .cmd-header .cmd-header-exit')?.textContent).toBe(
          'stopped',
        ),
      )
      expect(answer.dataset.turnState).toBe('cancelled')
      expect(answer.querySelector(':scope > .cmd-header .cmd-header-exit')?.textContent).not.toBe(
        'failed',
      )
      expect(answer.querySelector('[data-answer-body]')?.textContent).toContain(
        'partial prose survives',
      )

      // The notification is allowed to arrive after the reserved cancellation
      // response; it must be idempotent and cannot replace the stopped chip.
      const state = client.dispatcher.subscribe.mock.calls.find(
        ([method]) => method === 'agent.runState',
      )?.[1] as ((params: unknown) => void) | undefined
      state!({ runId: 42, entryId: 'entry-42', state: 'cancelled', droppedDeltas: 0 })
      expect(answer.querySelector(':scope > .cmd-header .cmd-header-exit')?.textContent).not.toBe(
        'failed',
      )
      expect(answer.querySelector(':scope > .cmd-header .cmd-header-exit')?.textContent).toBe(
        'stopped',
      )
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
      const renderer = rendererOf(content)
      renderer._fireBufferChange('alternate')
      await summon(content)
      const answer = await submitQuestion(content, client, 'leave when the program thaws')
      const pane = answer.closest<HTMLElement>('.pane')
      expect(pane).not.toBeNull()
      const paneElement = pane!
      expect(answer.isConnected).toBe(true)
      expect(paneElement.querySelector('.nocx-freeze-frame')).not.toBeNull()

      renderer._fireBufferChange('normal')

      expect(answer.isConnected).toBe(true)
      expect(answer.parentElement).toBe(
        (content as unknown as { scrollback: ScrollbackController }).scrollback.scrollbackInner,
      )
      expect(answer.classList.contains('nocx-answer-overlay')).toBe(false)
      expect(paneElement.querySelector('.nocx-summon-stack')).toBeNull()
      expect(paneElement.querySelector('.nocx-freeze-frame')).toBeNull()
      expect(content.pinnedFrame()).toBeNull()
      expect(editorOf(content).isVisible).toBe(false)
      expect(rendererReadOnly(content)).toHaveBeenLastCalledWith(false)
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
    menu.className = 'cmd-overflow-menu'
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
      ).scrollback.xtermLiveContainer.append(gridInput)
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
// not give back (session.output's `gaps`, plus the renderer-derived
// `unrecorded` stretch nothing kept), and until this bead nothing read it: a
// pane came back short and said nothing about it. AGENTS.md forbids exactly
// that — a soft degrade must be visible in the product.
//
// These go through the ADOPTION seam a restored tab actually takes
// (TerminalContentHooks.adoptSession), not through the notice module, so
// they can report the wiring being absent.
describe('a reclaimed pane says what is missing (nocx-fz4qa)', () => {
  const RECLAIM_SIZE = { cols: 80, rows: 24, xpixel: 0, ypixel: 0 }

  /** The pane's own reclaim: a session handle carrying what the claim
   *  recovered, handed back by the thunk PaneManager supplies. */
  function reclaiming(recovered: SessionRecovery | null) {
    const session = makeSession({ recovered: recovered ?? undefined })
    return {
      session,
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    }
  }

  const cardTitle = (tab: Pane) =>
    tab.pane.querySelector('.nocx-recovery-notice .ui-status-card__title')?.textContent ?? null
  const cardDesc = (tab: Pane) =>
    tab.pane.querySelector('.nocx-recovery-notice .ui-status-card__desc')?.textContent ?? null

  it('tells the user how much of the run is gone and that the size limit took it', async () => {
    const { hooks } = reclaiming({
      bytes: 8192,
      gaps: [{ start: 0, end: 3_000_000, reason: 'cap' }],
      size: RECLAIM_SIZE,
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

  it('names the unrecorded stretch, which is a different fact from the bound', async () => {
    const { hooks } = reclaiming({
      bytes: 0,
      gaps: [{ start: 100, end: 5100, reason: 'unrecorded' }],
      size: RECLAIM_SIZE,
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
    const { hooks } = reclaiming({ bytes: 8192, gaps: [], size: RECLAIM_SIZE })
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

  it('is taken away when the user dismisses it', async () => {
    const { hooks } = reclaiming({
      bytes: 8192,
      gaps: [{ start: 0, end: 1000, reason: 'cap' }],
      size: RECLAIM_SIZE,
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
  const RECOVERED = 'what the session printed while this window was away'

  /** A pane that takes back a live session running at SESSION_GRID, with the
   *  recovered scrollback waiting to be flushed the moment the surface
   *  registers its data callback — which is what WSClient.onSessionData does
   *  with the bytes reclaimSession queued ahead of the live stream. */
  const reclaimingPane = async () => {
    const session = makeSession({
      recovered: { bytes: RECOVERED.length, gaps: [], size: SESSION_GRID },
      pendingData: RECOVERED,
    })
    const mounted = await mountTerminal(makeClipboard(), {
      hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
    })
    return { ...mounted, session }
  }

  it('writes the recovered screen at the grid the session is running at', async () => {
    const { content, teardown } = await reclaimingPane()
    try {
      const renderer = rendererOf(content)
      /* eslint-disable @typescript-eslint/unbound-method */
      expect(renderer.setGrid).toHaveBeenCalledWith(132, 43)
      // ORDER IS THE ASSERTION. Bytes wrapped at 132 columns written into an
      // 80-column grid are wrapped twice over, and the grid change has to
      // land before them or there is nothing left to correct.
      const sized = (renderer.setGrid as Mock).mock.invocationCallOrder[0]
      const wrote = (renderer.write as Mock).mock.invocationCallOrder[0]
      /* eslint-enable @typescript-eslint/unbound-method */
      expect(wrote).toBeGreaterThan(sized)
    } finally {
      teardown()
    }
  })

  it('does not report the session grid back as a size this window measured', async () => {
    const { content, session, teardown } = await reclaimingPane()
    try {
      // The grid moved to 132×43, and xterm reports every grid change the
      // same way whoever caused it. A report sent from here would be this
      // window claiming a size it never measured — and under nocx-eidfb.2 a
      // report is a claim on the session.
      //
      // Fake time rather than a wait: the report is debounced, so "it was not
      // sent" is only a fact once the debounce has had its chance, and a real
      // delay would be a test measuring a duration.
      vi.useFakeTimers()
      try {
        rendererOf(content)._fireResize(132, 43)
        vi.advanceTimersByTime(10_000)
        expect(session.sendResize).not.toHaveBeenCalled()

        // And the control, so the assertion above cannot pass by the report
        // being broken for everyone: this window's OWN measurement is still
        // reported, and it is what ends the borrowed grid.
        content.viewportChanged({ width: 800, height: 400 })
        rendererOf(content)._fireResize(100, 30)
        vi.advanceTimersByTime(10_000)
        expect(session.sendResize).toHaveBeenCalledWith(100, 30)
      } finally {
        vi.useRealTimers()
      }
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

  it('hands the grid back to the window the moment this window measures itself', async () => {
    const { content, teardown } = await reclaimingPane()
    try {
      const renderer = rendererOf(content)
      // THE CLOSING END OF THE INTERVAL. A fit is this window taking the
      // session's size over — applying it is what makes this the client the
      // channel follows — so from here the grid is its own again, and so is
      // the scroller's overflow.
      content.viewportChanged({ width: 800, height: 400 })
      /* eslint-disable-next-line @typescript-eslint/unbound-method */
      expect(renderer.fitViewport).toHaveBeenCalledTimes(1)
      expect(scrollbackFor(content).scrollbackArea.dataset.gridOwner).toBeUndefined()
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

describe('replayed completion restores a durable block outcome (nocx-gm21o)', () => {
  it('lands OSC 133 D on the restored entry rather than leaving it unknown', async () => {
    const session = makeSession({
      recovered: {
        bytes: 1,
        gaps: [],
        size: { cols: 80, rows: 24 },
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
      status: 'running',
      unreconciled: 'notYetAsked',
      submittedAt: 1,
      startedAt: 1,
      endedAt: null,
      durationMs: null,
      exitCode: null,
      maskedCount: 0,
      maskedKinds: [],
      redactions: [],
    } as const
    let resolveQuery!: (value: unknown) => void
    const queryPending = new Promise<unknown>((resolve) => {
      resolveQuery = resolve
    })
    const client = makeClient({
      call: vi.fn((method: string) => {
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
    const { content, tab, teardown } = await mountTerminal(
      makeClipboard(),
      {
        visibleBeforeMount: true,
        hooks: { adoptSession: () => Promise.resolve(asSessionHandleForTest(session)) },
      },
      client,
    )
    try {
      // The parsed C/D callbacks arrive while the durable page read is still
      // pending. This is the replacement-reader race; xterm parser coverage
      // is owned by parseOsc133 tests, while this test pins the association.
      const renderer = rendererOf(content)
      renderer._fireCommandMarker({ kind: 'C', line: 1, col: 0, buffer: 'normal' })
      renderer._fireCommandMarker({
        kind: 'D',
        exitCode: 7,
        line: 2,
        col: 0,
        buffer: 'normal',
      })
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
      expect(
        restored?.querySelector('.cmd-header-exit')?.textContent,
        'the restored block still reads unknown after its completion replayed',
      ).toBe('exit 7')
    } finally {
      teardown()
    }
  })
})
