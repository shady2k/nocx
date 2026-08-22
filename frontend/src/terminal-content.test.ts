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
import { beforeEach, describe, expect, it, vi } from 'vitest'
// node builtins are untyped here (@types/node is not installed), so the
// imports sit behind @ts-expect-error and the calls behind a contained
// no-unsafe disable — the same trade theme-catalogue.test.ts makes at file
// level, confined to this setup instead.

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const srcDir = import.meta.dirname ?? resolve(new URL('.', import.meta.url).pathname)
const STYLE_ENTRY = resolve(srcDir, 'style.css')

import type { PaneIdentity } from './terminal-content'
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
import { TerminalContent, type SnippetFire, type TerminalContentHooks } from './terminal-content'
import { LOCAL_TARGET_ID } from './ports-client'
import { Pane } from './panes'
import { SURFACE_TERMINAL } from './pane-content'
import { LifecycleKernel, shouldShowEditor } from './lifecycle/state'
import { ProfileClient, type SSHProfile } from './profiles'
import { Dispatcher, RpcError } from './dispatcher'
import type { WSClient } from './ipc'
import { createCommandBlock } from './scrollback/blocks'
import { CommandSnapshotStore } from './command-snapshot'
import type { DesiredMode } from './capability'
import type { ScrollbackController } from './scrollback/controller'
import { pushOverlay, popOverlay } from './ui/overlay/stack'
import { _resetThemeState } from './renderers/theme-adapter'
import { showToast } from './ui/toast'
import { BufferLine } from './scrollback/test-helpers'

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

describe('sandboxed session launch failure', () => {
  it('shows the typed failure and removes the unconfirmed tab', async () => {
    const failure = new RpcError('sandbox setup failed', -32007, { reason: 'setup-failed' })
    const client = makeClient({
      openSandboxedSession: vi.fn().mockRejectedValue(failure),
    })
    const content = new TerminalContent(
      client as unknown as WSClient,
      anchoredPane('sandbox-failure'),
      makeClipboard(),
      new ClipboardGate(),
      makeBanner(),
      null,
      () => {},
      undefined,
      {
        sandbox: {
          workspace: '/workspace',
          settingsRevision: 0,
          addWritable: [],
          removeWritable: [],
          addReadOnly: [],
          removeReadOnly: [],
        },
      },
    )
    const tab = new Pane(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: null,
        supportsAttention: true,
        defaultTitle: '',
      },
      100,
      'sandbox-failure-tab',
    )
    const requestClose = vi.fn()
    tab.onCloseRequested = requestClose

    await tab.start()

    await expect(content.ready).resolves.toBe(false)
    expect(client.openSandboxedSession).toHaveBeenCalledWith(
      80,
      24,
      {
        workspace: '/workspace',
        settingsRevision: 0,
        addWritable: [],
        removeWritable: [],
        addReadOnly: [],
        removeReadOnly: [],
      },
      { paneId: 'sandbox-failure' },
    )
    expect(client.openSession).not.toHaveBeenCalled()
    expect(showToast).toHaveBeenCalledWith({
      level: 'danger',
      message: 'Sandboxed shell failed to start: sandbox setup failed',
    })
    expect(requestClose).toHaveBeenCalledOnce()
    tab.close()
  })
})

describe('sandboxed session tooltip (ADR-0037 §8, ADR-0040)', () => {
  const sandboxRequest = {
    workspace: '/w',
    settingsRevision: 0,
    addWritable: [],
    removeWritable: [],
    addReadOnly: [],
    removeReadOnly: [],
  }

  it('reports both installed root classes and populated HOME projections', async () => {
    const client = makeClient({
      openSandboxedSession: vi.fn(() =>
        Promise.resolve(
          makeSession({
            sandbox: {
              backend: 'landlock',
              workspace: '/w',
              writableRoots: ['/w', '/extra'],
              readOnlyRoots: ['/usr', '/opt'],
              homeProjections: [
                {
                  hostPath: '/host/home/.config/opencode',
                  relativePath: '.config/opencode',
                },
              ],
            },
          }),
        ),
      ),
    })
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { sandbox: sandboxRequest } },
      client,
    )
    try {
      expect(tab.tooltip).toContain('writable: /w, /extra')
      expect(tab.tooltip).toContain('read-only: /usr, /opt')
      expect(tab.tooltip).toContain(
        'Home projections: ~/.config/opencode -> /host/home/.config/opencode',
      )
    } finally {
      teardown()
    }
  })

  it('states that HOME is isolated when no host folder projects', async () => {
    const client = makeClient({
      openSandboxedSession: vi.fn(() =>
        Promise.resolve(
          makeSession({
            sandbox: {
              backend: 'landlock',
              workspace: '/w',
              writableRoots: ['/w'],
              readOnlyRoots: ['/usr'],
              homeProjections: [],
            },
          }),
        ),
      ),
    })
    const { tab, teardown } = await mountTerminal(
      makeClipboard(),
      { hooks: { sandbox: sandboxRequest } },
      client,
    )
    try {
      expect(tab.tooltip).toContain('Home: isolated; no host folders projected')
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
})

describe("the dropdown owns the arrows while it is open; recall's bare-Up gesture waits for it to close (nocx-mlm7)", () => {
  /** A profile client whose quick-connect assembly answers two hosts. */
  const hostsClient = (): ProfileClient => {
    const pc = new ProfileClient(new Dispatcher())
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
          redactions: [],
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
      content.viewportChanged({ width: 800, height: 400, devicePixelRatio: 1 })
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
      expect(submitAttempt).toHaveBeenCalledWith('lifecycle.submitAttempt', {
        domain: 'd1',
        command: 'make deploy',
        cwd: FIXTURE_CWD,
        host: '',
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
// Enter goes. Plain Enter submits to the registry's active target (the
// shell, always, unless the person switched it); ⌘Enter is a ONE-SHOT
// question that never touches the active target. Selecting a region of a
// finished block's output FREEZES it into a reference chip in the input
// line — and changes nothing else: the target does not move, and a plain
// Enter after a selection still reaches the shell. A ⌘Enter question
// carries exactly the chips in the line and no others. These tests drive
// the REAL chain: real TerminalContent, real controller, real BlockManager
// freeze, real editor submit, real registry.
describe('the ask entry gesture (nocx-4wtlh)', () => {
  /** A client whose dispatcher answers the agent.* transaction, records
   *  EVERY dispatcher call (so the test can assert a lifecycle attempt was
   *  never opened for a question), and keeps the default resolve for
   *  lifecycle.submitAttempt so a shell submit proceeds normally. */
  function agentDispatcher(
    status: {
      endpointConfigured?: boolean
      credential?: ('resolvable' | 'none' | 'deleted' | 'sealed' | 'unavailable') | null
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
        return Promise.resolve({ runId: nextRun, answerEntryId: `entry-${nextRun}` })
      }
      if (method === 'agent.status') {
        return Promise.resolve({
          endpointConfigured: status.endpointConfigured ?? true,
          credential: status.credential ?? 'resolvable',
          lastProbe: null,
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
    return viewOf(ed).dom.querySelector<HTMLElement>('.nocx-editor-target-indicator')
  }

  /** The reference chip strip inside the editor. */
  function chipStripOf(ed: CommandEditor): HTMLElement {
    return ed.root.querySelector<HTMLElement>('.nocx-editor-references')!
  }

  function chipsIn(ed: CommandEditor): HTMLElement[] {
    return Array.from(chipStripOf(ed).querySelectorAll<HTMLElement>('.nocx-editor-reference-chip'))
  }

  /** Dispatch a submit key exactly where a person's keystroke lands. */
  function submitKey(ed: CommandEditor, init: KeyboardEventInit = {}): void {
    viewOf(ed).contentDOM.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...init }),
    )
  }

  /** Ask through the REAL gesture: ⌘Enter flips the active target to Ask
   *  and sends NOTHING, then plain Enter — the one send key — delivers the
   *  question. The flip is skipped when Ask is already active, exactly as
   *  a person experiences it: the target stays where they put it. */
  function askKey(ed: CommandEditor, content: TerminalContent): void {
    if (activeLabel(content) !== 'Agent') submitKey(ed, { metaKey: true })
    submitKey(ed)
  }

  /** Select rows [start, end) of a frozen block's output and fire the
   *  selectionchange event the product listens for. */
  function selectRows(block: HTMLElement, start: number, end: number): void {
    const lines = Array.from(block.querySelectorAll<HTMLElement>('.term-line'))
    expect(lines.length).toBeGreaterThanOrEqual(end)
    const first = lines[start]
    const last = lines[end - 1]
    const range = document.createRange()
    range.setStart(first.firstChild ?? first, 0)
    range.setEnd(last.lastChild ?? last, (last.textContent ?? '').length)
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
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
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
      ed.insertText('what does docs mean?')
      askKey(ed, content)
      await vi.waitFor(() => {
        expect(dispatcherCalls.some((c) => c.method === 'agent.ask')).toBe(true)
      })
      const ask = recordedParams(dispatcherCalls, 'agent.ask')
      expect(ask.question).toBe('what does docs mean?')
      // A general question: no chips, no references.
      expect(ask.references).toEqual([])
      // Nothing shell ran FOR THE QUESTION: no pty bytes, no attempt, no
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

  it('selecting output puts a reference chip in the input line and changes nothing else — plain Enter after the selection still reaches the SHELL (nocx-4wtlh)', async () => {
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

      selectRows(block, 0, 2)
      const chips = chipsIn(ed)
      expect(chips).toHaveLength(1)
      expect(chips[0].textContent).toContain('ls')
      expect(chips[0].textContent).toContain('rows 1–2')

      // The selection created the chip and NOTHING else: the active target
      // is untouched and the indicator still says Run.
      expect(activeLabel(content)).toBe('Shell')
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      expect(dispatcherCalls.find((c) => c.method === 'agent.ask')).toBeUndefined()

      // Submit with plain Enter: the SHELL receives the command — the chip
      // never armed ask, the registry never moved.
      const sentBefore = sessionOf(content).send.mock.calls.length
      ed.insertText('echo back')
      submitKey(ed)
      expect(sessionOf(content).send.mock.calls.length).toBe(sentBefore + 2)
      expect(dispatcherCalls.find((c) => c.method === 'agent.ask')).toBeUndefined()
      expect(activeLabel(content)).toBe('Shell')
    } finally {
      teardown()
    }
  })

  it('a question carries the chips that are in the line and no others — two selections, one unrelated block (nocx-4wtlh)', async () => {
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
      const blockA = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      const blockB = frozenBlockOf(content, 'git log', ['commit abc'])
      const unrelated = frozenBlockOf(content, 'sleep 1', ['zzz'])

      selectRows(blockA, 0, 2)
      selectRows(blockB, 0, 1)
      expect(chipsIn(ed)).toHaveLength(2)

      const sentBefore = sessionOf(content).send.mock.calls.length
      ed.insertText('how are these related?')
      askKey(ed, content)
      await vi.waitFor(() => {
        expect(dispatcherCalls.filter((c) => c.method === 'agent.ask')).toHaveLength(1)
      })

      // Exactly the two chip blocks were captured — the unrelated block's
      // rows never left the DOM.
      const frames = dispatcherCalls
        .filter((c) => c.method === 'agent.captureFrame')
        .map((c) => (c.params as { rows: { text: string }[] }).rows.map((r) => r.text))
      expect(frames).toHaveLength(2)
      expect(frames).toContainEqual(['total 12', 'docs'])
      expect(frames).toContainEqual(['commit abc'])
      // The unrelated block's rows exist in the flow and never left it.
      expect(
        Array.from(unrelated.querySelectorAll('.term-line')).map((l) => l.textContent),
      ).toEqual(['zzz'])
      expect(frames.some((f) => f.includes('zzz'))).toBe(false)

      // The references are exactly the chips — each with its own region and
      // its own frame.
      const ask = recordedParams(dispatcherCalls, 'agent.ask')
      expect(ask.references).toEqual([
        { frameId: 'frame-1', region: { rowStart: 0, rowEnd: 2 } },
        { frameId: 'frame-2', region: { rowStart: 0, rowEnd: 1 } },
      ])
      // Nothing shell ran; the chips were consumed by the question.
      expect(sessionOf(content).send.mock.calls.length).toBe(sentBefore)
      expect(chipsIn(ed)).toHaveLength(0)
    } finally {
      teardown()
    }
  })

  it('a chip can be dismissed from the line, and the next question carries what remains', async () => {
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
      const blockA = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      const blockB = frozenBlockOf(content, 'git log', ['commit abc'])

      selectRows(blockA, 0, 2)
      selectRows(blockB, 0, 1)
      const [chipA] = chipsIn(ed)
      chipA.querySelector<HTMLButtonElement>('.nocx-editor-reference-chip__drop')?.click()
      expect(chipsIn(ed)).toHaveLength(1)

      ed.insertText('what is left?')
      askKey(ed, content)
      await vi.waitFor(() => {
        expect(dispatcherCalls.some((c) => c.method === 'agent.ask')).toBe(true)
      })
      const ask = recordedParams(dispatcherCalls, 'agent.ask')
      expect(ask.references).toEqual([{ frameId: 'frame-1', region: { rowStart: 0, rowEnd: 1 } }])
    } finally {
      teardown()
    }
  })

  it('the editor stays available after a question — a second question works while the first streams, and each answer lands on its own entry (nocx-wmy4)', async () => {
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
      const scrollback = (content as unknown as { scrollback: ScrollbackController }).scrollback
      const blockA = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      const blockB = frozenBlockOf(content, 'git log', ['commit abc'])

      // Question one, through the REAL gesture: ⌘Enter to Ask, then Enter.
      selectRows(blockA, 0, 2)
      ed.insertText('what does docs mean?')
      askKey(ed, content)
      await vi.waitFor(() => {
        expect(dispatcherCalls.filter((c) => c.method === 'agent.ask')).toHaveLength(1)
      })
      // The question left the editor up — no handoff.
      expect(ed.isVisible).toBe(true)

      // While the first answer streams, point at block B and ask again.
      selectRows(blockB, 0, 1)
      ed.insertText('what did it fix?')
      askKey(ed, content)
      await vi.waitFor(() => {
        expect(dispatcherCalls.filter((c) => c.method === 'agent.ask')).toHaveLength(2)
      })
      // The second payload carries block B's rows.
      const frames = dispatcherCalls
        .filter((c) => c.method === 'agent.captureFrame')
        .map((c) => (c.params as { rows: { text: string }[] }).rows.map((r) => r.text))
      expect(frames).toHaveLength(2)
      expect(frames[1]).toEqual([{ kind: 'text', text: 'commit abc' }].map((r) => r.text))

      // Both streams interleave through the REAL subscription seam; each
      // run's deltas land on its own answer block, never the other's.
      deliverNotification(client, 'agent.runDelta', {
        runId: 7,
        entryId: 'entry-7',
        seq: 0,
        text: 'docs: a directory',
      })
      deliverNotification(client, 'agent.runDelta', {
        runId: 8,
        entryId: 'entry-8',
        seq: 0,
        text: 'fix: the bug',
      })
      deliverNotification(client, 'agent.runDelta', {
        runId: 7,
        entryId: 'entry-7',
        seq: 1,
        text: ' of files',
      })
      deliverNotification(client, 'agent.runDelta', {
        runId: 8,
        entryId: 'entry-8',
        seq: 1,
        text: ' now',
      })
      const answers = Array.from(
        scrollback.scrollbackInner.querySelectorAll<HTMLElement>('[data-answer-entry-id]'),
      )
      expect(answers).toHaveLength(2)
      const bodyOf = (entryId: string): string | undefined =>
        answers.find((a) => a.dataset.answerEntryId === entryId)?.querySelector('.cmd-output')
          ?.textContent
      expect(bodyOf('entry-7')).toBe('docs: a directory of files')
      expect(bodyOf('entry-8')).toBe('fix: the bug now')

      deliverNotification(client, 'agent.runState', { runId: 7, state: 'completed' })
      deliverNotification(client, 'agent.runState', { runId: 8, state: 'completed' })
      await vi.waitFor(() => {
        expect(
          answers.every((a) => a.querySelector('.cmd-header-exit')?.textContent === 'completed'),
        ).toBe(true)
      })
      expect(ed.isVisible).toBe(true)
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
      const block = frozenBlockOf(content, 'ls', ['total 12', 'docs'])
      selectRows(block, 0, 2)

      ed.insertText('why did it fail?')
      askKey(ed, content)
      await vi.waitFor(() => {
        expect(dispatcherCalls.some((c) => c.method === 'agent.ask')).toBe(true)
      })
      await vi.waitFor(() => {
        expect(showToast).toHaveBeenCalledWith(
          expect.objectContaining({ level: 'warning', message: 'no endpoint configured' }),
        )
      })
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
      expect(view.dom.querySelector('.nocx-editor-target-indicator')).not.toBeNull()
      expect(view.contentDOM.querySelector('.nocx-editor-target-indicator')).toBeNull()
      expect(view.contentDOM.textContent).toBe('')

      ed.insertText('ls -la')
      expect(view.contentDOM.textContent).toBe('ls -la')
      expect(view.state.selection.main.head).toBe('ls -la'.length)
      // Still there while typing, and still outside the text.
      expect(indicatorOf(ed)?.textContent).toBe('Run')
      expect(view.contentDOM.querySelector('.nocx-editor-target-indicator')).toBeNull()

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
      const withToken = cells.filter((c) => c.querySelector('.nocx-editor-target-indicator'))
      expect(withToken.length).toBeGreaterThanOrEqual(1)
      expect(view.contentDOM.textContent).toBe('onetwo')
    } finally {
      teardown()
    }
  })

  it('⌘Enter flips the target and sends nothing — the indicator names what the person does, Run ⇄ Ask (nocx-4wtlh)', async () => {
    const { client } = agentDispatcher()
    const { ed, content, teardown } = await mountTerminal(makeClipboard(), {}, client)
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
})

// A session the backend cannot currently reach (nocx-iarf9). Nothing has
// ended, so the tab stays exactly as it is — and the user is told, because a
// terminal that has silently stopped being connected to anything looks
// identical to one that is simply quiet.
describe('the reachability axis', () => {
  it('says so when the connection stops responding, and again when it returns', async () => {
    const client = makeClient()
    const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
    try {
      content.setVisible(true)
      const session = client._sessions[0]
      expect(session.onLiveness).toHaveBeenCalled()

      session.fireLiveness('unknown')
      expect(showToast).toHaveBeenCalledWith({
        level: 'warning',
        message:
          'This connection has stopped responding — the session may still be running on the host.',
      })

      session.fireLiveness('alive', 3)
      expect(showToast).toHaveBeenCalledWith({
        level: 'info',
        message: 'This connection is responding again.',
      })
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
