// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import {
  parseOsc7,
  parseOsc133,
  parseRecoveryFence,
  parseRenderFence,
  XtermRenderer,
} from './xterm'
import { WORD_SEPARATORS } from '../word-selection'
import type { CommandMarkerEvent } from './types'
import { CommandSnapshotStore } from '../command-snapshot'
import type { OscNotification } from '../osc-notification'
import {
  CaptureAbortedError,
  CaptureIdentityTracker,
  ReadScreenRangeError,
} from '../frame/capture-identity'
import { getCurrentTheme } from './theme-adapter'

const stubBrowser = () => {
  window.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })
  ;(globalThis as Record<string, unknown>).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}

describe('XtermRenderer setReadOnly', () => {
  it('toggles disableStdin on the underlying terminal', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    // Access the private term via a cast — the test owns both sides.
    const term = (r as unknown as Record<string, unknown>).term as
      { options: { disableStdin: boolean } } | undefined
    expect(term).toBeDefined()

    r.setReadOnly(true)
    expect(term!.options.disableStdin).toBe(true)

    r.setReadOnly(false)
    expect(term!.options.disableStdin).toBe(false)
  })

  it('paste delivers the document even while the grid is read-only (nocx-u7uh.23)', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const term = (r as unknown as Record<string, unknown>).term as
      { options: { disableStdin: boolean } } | undefined
    expect(term).toBeDefined()

    const received: string[] = []
    r.onData((text) => received.push(text))

    // The editor owns input, so the grid is read-only — exactly the state a
    // submit is in. The submitted document must still reach the program: a
    // paste dropped here is the editor vanishing after every command.
    r.setReadOnly(true)
    r.paste('echo hi')
    expect(received).toEqual(['echo hi'])
    // The read-only guard is restored: user input stays blocked afterwards.
    expect(term!.options.disableStdin).toBe(true)

    r.setReadOnly(false)
    r.paste('echo again')
    expect(received).toEqual(['echo hi', 'echo again'])
  })

  it('uses the same word separator policy as the frozen block (parity by construction)', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    const term = (r as unknown as Record<string, unknown>).term as
      { options: { wordSeparator?: string } } | undefined
    // The live terminal and the frozen block share ONE separator set, so a
    // double-click selects the same token on both surfaces.
    expect(term?.options.wordSeparator).toBe(WORD_SEPARATORS)
    r.dispose()
  })
})

describe('parseOsc7', () => {
  it('parses a local file:/// path (empty host)', () => {
    const result = parseOsc7('file:///Users/shady/projects')
    expect(result).toEqual({ host: '', path: '/Users/shady/projects' })
  })

  it('parses a file://host/path with hostname', () => {
    const result = parseOsc7('file://macbook.local/Users/shady')
    expect(result).toEqual({ host: 'macbook.local', path: '/Users/shady' })
  })

  it('parses file://localhost/path', () => {
    const result = parseOsc7('file://localhost/tmp')
    expect(result).toEqual({ host: 'localhost', path: '/tmp' })
  })

  it('percent-decodes the host', () => {
    const result = parseOsc7('file://my%20host.local/path')
    expect(result).toEqual({ host: 'my host.local', path: '/path' })
  })

  it('percent-decodes the path', () => {
    const result = parseOsc7('file://host/Users/shady/My%20Documents')
    expect(result).toEqual({ host: 'host', path: '/Users/shady/My Documents' })
  })

  it('percent-decodes both host and path', () => {
    const result = parseOsc7('file://my%20mac/Users/shady/project%20name')
    expect(result).toEqual({ host: 'my mac', path: '/Users/shady/project name' })
  })

  it('returns null for non-file:// payloads', () => {
    expect(parseOsc7('not-a-file-uri')).toBeNull()
    expect(parseOsc7('')).toBeNull()
    expect(parseOsc7('http://example.com/path')).toBeNull()
  })

  it('returns null for file:// with no path separator', () => {
    expect(parseOsc7('file://justhost')).toBeNull()
  })

  it('returns null for malformed percent-encoding', () => {
    // '%ZZ' is not valid percent-encoding
    expect(parseOsc7('file:///tmp/%ZZ')).toBeNull()
    // incomplete percent sequence
    expect(parseOsc7('file:///tmp/%')).toBeNull()
  })

  it('handles deeply nested paths', () => {
    const result = parseOsc7('file:///a/b/c/d/e/f/g')
    expect(result).toEqual({ host: '', path: '/a/b/c/d/e/f/g' })
  })

  it('handles root path', () => {
    const result = parseOsc7('file:///')
    expect(result).toEqual({ host: '', path: '/' })
  })
})

describe('parseOsc133', () => {
  it('parses A (prompt start)', () => {
    expect(parseOsc133('A')).toEqual({ kind: 'A' })
  })

  it('parses B (prompt end)', () => {
    expect(parseOsc133('B')).toEqual({ kind: 'B' })
  })

  it('parses C (command output start)', () => {
    expect(parseOsc133('C')).toEqual({ kind: 'C' })
  })

  it('parses D without exit code', () => {
    expect(parseOsc133('D')).toEqual({ kind: 'D' })
  })

  it('parses D with exit code 0', () => {
    expect(parseOsc133('D;0')).toEqual({ kind: 'D', exitCode: 0 })
  })

  it('parses D with exit code 127', () => {
    expect(parseOsc133('D;127')).toEqual({ kind: 'D', exitCode: 127 })
  })

  it('parses D with exit code 1', () => {
    expect(parseOsc133('D;1')).toEqual({ kind: 'D', exitCode: 1 })
  })

  it('returns D without exitCode for invalid exit code', () => {
    expect(parseOsc133('D;abc')).toEqual({ kind: 'D' })
  })

  it('returns D without exitCode for negative exit code', () => {
    expect(parseOsc133('D;-1')).toEqual({ kind: 'D' })
  })

  it('returns D without exitCode for trailing junk', () => {
    expect(parseOsc133('D;1extra')).toEqual({ kind: 'D' })
  })

  it('returns D without exitCode for out-of-range exit code', () => {
    expect(parseOsc133('D;256')).toEqual({ kind: 'D' })
  })

  it('parses D with exit code 255', () => {
    expect(parseOsc133('D;255')).toEqual({ kind: 'D', exitCode: 255 })
  })

  it('returns null for empty payload', () => {
    expect(parseOsc133('')).toBeNull()
  })

  it('returns null for unknown marker', () => {
    expect(parseOsc133('X')).toBeNull()
  })

  it('returns null for lowercase marker', () => {
    expect(parseOsc133('a')).toBeNull()
  })
})

describe('parseOsc133 nocx_env tags', () => {
  it('parses a tagged A marker exposing nocx_env', () => {
    expect(parseOsc133('A;nocx_env=env-ab12')).toEqual({ kind: 'A', nocxEnv: 'env-ab12' })
  })

  it('parses tagged B and C markers', () => {
    expect(parseOsc133('B;nocx_env=env-ab12')).toEqual({ kind: 'B', nocxEnv: 'env-ab12' })
    expect(parseOsc133('C;nocx_env=env-ab12')).toEqual({ kind: 'C', nocxEnv: 'env-ab12' })
  })

  it('parses a tagged D with exit code and nocx_env', () => {
    expect(parseOsc133('D;0;nocx_env=env-ab12')).toEqual({
      kind: 'D',
      exitCode: 0,
      nocxEnv: 'env-ab12',
    })
  })

  it('parses a tagged D without an exit code', () => {
    // The first parameter is a key=value property, not a positional exit
    // code, so it must not be swallowed.
    expect(parseOsc133('D;nocx_env=env-ab12')).toEqual({ kind: 'D', nocxEnv: 'env-ab12' })
  })

  it('leaves unknown well-formed parameters untagged', () => {
    expect(parseOsc133('A;Prompt=1')).toEqual({ kind: 'A' })
    expect(parseOsc133('A;Prompt=1;nocx_env=env-ab12')).toEqual({ kind: 'A', nocxEnv: 'env-ab12' })
  })

  it('tolerates an empty parameter as today', () => {
    expect(parseOsc133('A;')).toEqual({ kind: 'A' })
  })

  it('a marker whose tag is present but malformed is ignored entirely', () => {
    // Present-but-malformed ≠ absent: an absent tag keeps the legacy
    // untagged boundary, a malformed one must never be read as a marker.
    expect(parseOsc133('A;nocx_env=')).toBeNull()
    expect(parseOsc133('A;nocx_env=bad id')).toBeNull()
    expect(parseOsc133('A;nocx_env')).toBeNull()
    expect(parseOsc133('B;nocx_env=' + 'a'.repeat(65))).toBeNull()
    expect(parseOsc133('D;0;nocx_env=')).toBeNull()
    expect(parseOsc133('D;nocx_env=bad id')).toBeNull()
  })
})

describe('onCommandMarker fan-out', () => {
  it('exposes a tagged marker through the real parser into the enriched event', async () => {
    // jsdom lacks matchMedia and ResizeObserver, which xterm.js / our mount
    // code uses during init. Stub them so the terminal can initialise.
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }

    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    let resolveDone: () => void
    const done = new Promise<void>((res) => {
      resolveDone = res
    })
    let ev: CommandMarkerEvent | undefined
    r.onCommandMarker((e) => {
      ev = e
      resolveDone()
    })
    r.write('\x1b]133;A;nocx_env=env-ab12\x07')
    await done
    expect(ev?.kind).toBe('A')
    expect(ev?.nocxEnv).toBe('env-ab12')
    r.dispose()
  })
  it('fans out one enriched event per marker to every subscriber', async () => {
    // jsdom lacks matchMedia and ResizeObserver, which xterm.js / our mount
    // code uses during init. Stub them so the terminal can initialise.
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }

    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const a = vi.fn()
    let resolveDone: () => void
    const done = new Promise<void>((res) => {
      resolveDone = res
    })
    const b = vi.fn(() => resolveDone())
    r.onCommandMarker(a)
    r.onCommandMarker(b)

    // Drive an OSC 133;D;0 through the real parser; write() is async.
    r.write('\x1b]133;D;0\x07')
    await done

    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(1)
    const ev = a.mock.calls[0][0] as CommandMarkerEvent
    expect(ev.kind).toBe('D')
    expect(ev.exitCode).toBe(0)
    expect(ev.buffer).toBe('normal')
    expect(typeof ev.line).toBe('number')
    expect(typeof ev.col).toBe('number')
    r.dispose()
  })
})

describe('OSC 636 command-existence snapshot', () => {
  const stubBrowser = () => {
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  }

  const NONCE = 'a1b2c3d4e5f60718293a4b5c6d7e8f90'

  async function mountRenderer(): Promise<XtermRenderer> {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    return r
  }

  it('forwards a hello + snapshot through the real parser into the store', async () => {
    const r = await mountRenderer()

    const applied = new Promise<void>((resolve) => {
      const un = r.snapshotStore.subscribe(() => {
        un()
        resolve()
      })
    })
    r.write(`\x1b]636;H;${NONCE}\x07`)
    r.write(`\x1b]636;S;${NONCE};pwd;ls;café\x07`)
    await applied

    expect(r.snapshotStore.status).toBe('ready')
    expect(r.snapshotStore.has('pwd')).toBe(true)
    expect(r.snapshotStore.has('ls')).toBe(true)
    expect(r.snapshotStore.has('café')).toBe(true)
    r.dispose()
  })

  it('a snapshot carrying the wrong nonce is discarded', async () => {
    const r = await mountRenderer()

    // The 133 marker after the 636 bytes is a stream-order sync point: writes
    // are async, and a discarded snapshot notifies nobody.
    let markerDone: () => void
    const marker = new Promise<void>((resolve) => {
      markerDone = resolve
    })
    r.onCommandMarker(() => markerDone())

    r.write(`\x1b]636;H;${NONCE}\x07`)
    r.write('\x1b]636;S;deadbeefdeadbeefdeadbeefdeadbeef;pwd\nls\x07')
    r.write('\x1b]133;D;0\x07')
    await marker

    expect(r.snapshotStore.status).toBe('unavailable')
    r.dispose()
  })

  it('two renderers keep their snapshots separate (per-tab stores)', async () => {
    const r1 = await mountRenderer()
    const r2 = await mountRenderer()
    const NONCE_B = 'deadbeefdeadbeefdeadbeefdeadbeef'

    const applied1 = new Promise<void>((resolve) => {
      const un = r1.snapshotStore.subscribe(() => {
        un()
        resolve()
      })
    })
    r1.write(`\x1b]636;H;${NONCE}\x07`)
    r1.write(`\x1b]636;S;${NONCE};pwd;ls\x07`)
    await applied1

    const applied2 = new Promise<void>((resolve) => {
      const un = r2.snapshotStore.subscribe(() => {
        un()
        resolve()
      })
    })
    r2.write(`\x1b]636;H;${NONCE_B}\x07`)
    r2.write(`\x1b]636;S;${NONCE_B};kubectl\x07`)
    await applied2

    // Tab 1 resolves only its own names; tab 2 resolves only its own. Under
    // the old module singleton, r2's hello would have been discarded (nonce
    // already anchored by r1) and its snapshot rejected, leaving r2 judged
    // against r1's command set — this is the defect this test pins.
    expect(r1.snapshotStore.status).toBe('ready')
    expect(r1.snapshotStore.has('pwd')).toBe(true)
    expect(r1.snapshotStore.has('ls')).toBe(true)
    expect(r1.snapshotStore.has('kubectl')).toBe(false)
    expect(r2.snapshotStore.status).toBe('ready')
    expect(r2.snapshotStore.has('kubectl')).toBe(true)
    expect(r2.snapshotStore.has('pwd')).toBe(false)
    r1.dispose()
    r2.dispose()
  })

  it('a renderer whose session never sent a snapshot reports unavailable even when another tab has one', async () => {
    const r1 = await mountRenderer()
    const r2 = await mountRenderer()

    const applied1 = new Promise<void>((resolve) => {
      const un = r1.snapshotStore.subscribe(() => {
        un()
        resolve()
      })
    })
    r1.write(`\x1b]636;H;${NONCE}\x07`)
    r1.write(`\x1b]636;S;${NONCE};pwd;ls\x07`)
    await applied1

    // r2 never received a hello or snapshot — it must not inherit r1's.
    expect(r1.snapshotStore.status).toBe('ready')
    expect(r2.snapshotStore.status).toBe('unavailable')
    expect(r2.snapshotStore.has('pwd')).toBe(false)
    r1.dispose()
    r2.dispose()
  })

  it('a fresh renderer carries a fresh store (CommandSnapshotStore instance)', () => {
    const r = new XtermRenderer()
    expect(r.snapshotStore).toBeInstanceOf(CommandSnapshotStore)
    expect(r.snapshotStore.status).toBe('unavailable')
  })
})

describe('parseRenderFence (OSC 1337 NOCX_FENCE — ADR-0024 §7 carve-out)', () => {
  const FENCE = 'ab'.repeat(32) // 64 hex chars, what the shell generates

  it('parses a well-formed fence payload', () => {
    expect(parseRenderFence(`NOCX_FENCE;${FENCE}`)).toEqual({ hex: FENCE })
  })

  it('rejects payloads without the NOCX_FENCE; prefix (foreign OSC 1337)', () => {
    expect(parseRenderFence(`File=name;size=42`)).toBeNull() // iTerm2 file transfer
    expect(parseRenderFence(`NOCX_IB_READY`)).toBeNull()
    expect(parseRenderFence('')).toBeNull()
  })

  it('rejects a non-hex, short, long or empty nonce — only exactly 64 lowercase hex', () => {
    expect(parseRenderFence(`NOCX_FENCE;deadbeef`)).toBeNull() // 8 chars, not 64
    expect(parseRenderFence(`NOCX_FENCE;${'g'.repeat(64)}`)).toBeNull()
    expect(parseRenderFence(`NOCX_FENCE;${'A'.repeat(64)}`)).toBeNull() // uppercase
    expect(parseRenderFence(`NOCX_FENCE;${FENCE}x`)).toBeNull() // 65 chars
    expect(parseRenderFence(`NOCX_FENCE;`)).toBeNull()
  })
})

describe('parseRecoveryFence (OSC 1337 NOCX_RECOVERY — ADR-0024 decision 8)', () => {
  const NONCE = 'ab'.repeat(32)

  it('parses a well-formed recovery fence payload', () => {
    expect(parseRecoveryFence(`NOCX_RECOVERY;${NONCE}`)).toEqual({ hex: NONCE })
  })

  it('rejects foreign OSC 1337 payloads and non-conforming nonces', () => {
    expect(parseRecoveryFence(`File=name;size=42`)).toBeNull()
    expect(parseRecoveryFence(`NOCX_FENCE;${NONCE}`)).toBeNull() // the completion fence is not a recovery
    expect(parseRecoveryFence(`NOCX_RECOVERY;deadbeef`)).toBeNull()
    expect(parseRecoveryFence(`NOCX_RECOVERY;${'g'.repeat(64)}`)).toBeNull()
    expect(parseRecoveryFence(`NOCX_RECOVERY;${'A'.repeat(64)}`)).toBeNull()
    expect(parseRecoveryFence('')).toBeNull()
  })
})

describe('XtermRenderer fence delivery through the real parser', () => {
  const stubBrowser = () => {
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  }

  async function mountRenderer(): Promise<XtermRenderer> {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    return r
  }

  const FENCE = 'cd'.repeat(32)

  it('reports the fence and the line it landed on', async () => {
    const r = await mountRenderer()
    let seen: { hex: string; line: number } | null = null
    r.onRenderFence((ev) => {
      seen = { hex: ev.hex, line: ev.line }
    })

    // The 133 marker after the 1337 bytes is a stream-order sync point:
    // writes are async, and the fence callback has no other completion
    // signal. When the marker lands, the fence before it has been parsed.
    let markerDone: () => void
    const marker = new Promise<void>((resolve) => {
      markerDone = resolve
    })
    r.onCommandMarker(() => markerDone())

    r.write(`\x1b]1337;NOCX_FENCE;${FENCE}\x07`)
    r.write('\x1b]133;A\x07')
    await marker

    expect(seen).toEqual({ hex: FENCE, line: 0 })
    r.dispose()
  })

  it('a malformed or foreign OSC 1337 never fires the callback', async () => {
    const r = await mountRenderer()
    const cb = vi.fn()
    r.onRenderFence(cb)

    let markerDone: () => void
    const marker = new Promise<void>((resolve) => {
      markerDone = resolve
    })
    r.onCommandMarker(() => markerDone())

    r.write(`\x1b]1337;NOCX_FENCE;deadbeef\x07`) // not 64 hex
    r.write('\x1b]1337;File=name;size=42\x07') // iTerm2's 1337, not ours
    r.write('\x1b]133;A\x07')
    await marker

    expect(cb).not.toHaveBeenCalled()
    r.dispose()
  })

  it('delivers a recovery fence through the OSC path — the same handler as the render fence (nocx-u7uh.24)', async () => {
    const r = await mountRenderer()
    const NONCE = 'ef'.repeat(32)
    const seen: string[] = []
    r.onRecoveryFence((hex) => seen.push(hex))

    let markerDone: () => void
    const marker = new Promise<void>((resolve) => {
      markerDone = resolve
    })
    r.onCommandMarker(() => markerDone())

    r.write(`\x1b]1337;NOCX_RECOVERY;${NONCE}\x07`)
    r.write('\x1b]133;A\x07')
    await marker

    // The shell's one-shot recovery nonce reached the subscribers — the
    // production path that previously parsed NOCX_RECOVERY nowhere.
    expect(seen).toEqual([NONCE])

    // The completion fence is a different payload kind on the same ident:
    // it must NOT fan out to recovery subscribers.
    seen.length = 0
    r.onRenderFence(() => {})
    let fenceMarkerDone: () => void
    const fenceMarker = new Promise<void>((resolve) => {
      fenceMarkerDone = resolve
    })
    r.onCommandMarker(() => fenceMarkerDone())
    r.write(`\x1b]1337;NOCX_FENCE;${'ab'.repeat(32)}\x07`)
    r.write('\x1b]133;A\x07')
    await fenceMarker
    expect(seen).toEqual([])
    r.dispose()
  })
})

// ── Shift+Enter as its own chord (nocx-nt70) ──────────────────────────────
// A program that owns the keyboard receives Enter as a bare CR and cannot
// tell Shift+Enter apart — xterm drops the modifier. The renderer re-encodes
// the plain chord as ESC CR (the decision and the named alternative live at
// SHIFT_ENTER_SEQUENCE in xterm.ts). These tests pin the exact bytes that
// leave the renderer for each chord, driven through xterm's real keydown
// path — the same DOM events a user's keystrokes produce.
describe('Shift+Enter as its own chord (nocx-nt70)', () => {
  async function mountKeyRenderer(): Promise<{ r: XtermRenderer; received: string[] }> {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    const received: string[] = []
    r.onData((text) => received.push(text))
    return { r, received }
  }

  /** Dispatch a real keydown at xterm's hidden textarea — the production
   *  path (xterm binds its key handling to the textarea with capture). */
  function pressKey(r: XtermRenderer, init: KeyboardEventInit & { keyCode: number }): void {
    const term = (r as unknown as Record<string, unknown>).term as { element: HTMLElement }
    const textarea = term.element.querySelector('textarea')
    expect(textarea).not.toBeNull()
    const event = new KeyboardEvent('keydown', { ...init, bubbles: true })
    // jsdom does not compute keyCode from `key`; xterm's encoder reads it.
    Object.defineProperty(event, 'keyCode', { value: init.keyCode })
    textarea!.dispatchEvent(event)
  }

  it('sends ESC CR for Shift+Enter and CR for Enter — the exact bytes a program sees', async () => {
    const { r, received } = await mountKeyRenderer()
    pressKey(r, { key: 'Enter', keyCode: 13 })
    pressKey(r, { key: 'Enter', keyCode: 13, shiftKey: true })
    expect(received).toEqual(['\r', '\x1b\r'])
    r.dispose()
  })

  it('leaves every neighbouring chord byte-identical to xterm\u2019s own encoding', async () => {
    const { r, received } = await mountKeyRenderer()
    // Ctrl+Enter, Alt+Enter, Ctrl+Shift+Enter, Shift+Tab, then a bare Enter.
    // Only the plain Shift+Enter chord is re-encoded; everything else must
    // stay exactly what xterm produced before the hook existed (the
    // "nothing negotiated, nothing changed" property, pinned per chord).
    // Alt+Enter = ESC CR is the collision the decision names: a program
    // still cannot tell it from Shift+Enter under the legacy encoding.
    pressKey(r, { key: 'Enter', keyCode: 13, ctrlKey: true })
    pressKey(r, { key: 'Enter', keyCode: 13, altKey: true })
    pressKey(r, { key: 'Enter', keyCode: 13, shiftKey: true, ctrlKey: true })
    pressKey(r, { key: 'Tab', keyCode: 9, shiftKey: true })
    pressKey(r, { key: 'Enter', keyCode: 13 })
    expect(received).toEqual(['\r', '\x1b\r', '\r', '\x1b[Z', '\r'])
    r.dispose()
  })

  it('never re-encodes a Ctrl-modified Enter into the Shift+Enter bytes', async () => {
    const { r, received } = await mountKeyRenderer()
    // Ctrl+Shift+Enter is not the plain chord: the hook must not fire for it.
    // If the hook condition ever widened past Enter+Shift alone, this key
    // would come back as ESC CR — the bytes this test pins it against.
    pressKey(r, { key: 'Enter', keyCode: 13, shiftKey: true, ctrlKey: true })
    expect(received).toEqual(['\r'])
    r.dispose()
  })
})

describe('the snippet palette chord (⌥⌘P, nocx-jj77)', () => {
  async function mountChordRenderer(): Promise<{ r: XtermRenderer; received: string[] }> {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    const received: string[] = []
    r.onData((text) => received.push(text))
    return { r, received }
  }

  /** Dispatch a real keydown at xterm's hidden textarea — the production
   *  path (xterm binds its key handling to the textarea with capture). */
  function pressKey(r: XtermRenderer, init: KeyboardEventInit & { keyCode: number }): void {
    const term = (r as unknown as Record<string, unknown>).term as { element: HTMLElement }
    const textarea = term.element.querySelector('textarea')
    expect(textarea).not.toBeNull()
    const event = new KeyboardEvent('keydown', { ...init, bubbles: true })
    Object.defineProperty(event, 'keyCode', { value: init.keyCode })
    textarea!.dispatchEvent(event)
  }
  // The snippet palette chord (⌥⌘P, design §10.1, bead nocx-jj77) is
  // consumed at the xterm boundary: the custom key handler sees it BEFORE
  // xterm encodes it, calls the registered opener and returns false — so
  // ZERO bytes reach the pty. The proof subscribes onData: the opener
  // fires and the byte log stays empty.
  it('consumes the snippet chord (⌥⌘P): the opener fires and ZERO bytes reach the pty', async () => {
    const { r, received } = await mountChordRenderer()
    const opener = vi.fn()
    r.onSnippetChord(opener)
    pressKey(r, { key: 'p', code: 'KeyP', keyCode: 80, altKey: true, metaKey: true })
    expect(opener).toHaveBeenCalledTimes(1)
    expect(received).toEqual([])
    r.dispose()
  })

  it('the chord is consumed even before the opener is wired — zero bytes then too', async () => {
    // The handler is registered at mount, before any keystroke, so the
    // pre-wiring state never faces a user; still, the chord's contract is
    // unconditional: ZERO bytes, handler or none.
    const { r, received } = await mountChordRenderer()
    pressKey(r, { key: 'p', code: 'KeyP', keyCode: 80, altKey: true, metaKey: true })
    expect(received).toEqual([])
    r.dispose()
  })

  it('leaves every neighbouring chord to xterm: the handler never fires for Alt+P, ⌘P, ⌥⌘⇧P or ⌥⌘O', async () => {
    const { r, received } = await mountChordRenderer()
    const opener = vi.fn()
    r.onSnippetChord(opener)
    // Alt+P alone (no Meta): not the chord — the handler returns true and
    // xterm encodes ESC p, exactly as before the hook existed.
    pressKey(r, { key: 'p', code: 'KeyP', keyCode: 80, altKey: true })
    // ⌘P alone, ⌥⌘⇧P and ⌥⌘O: xterm drops Meta-chords in this (non-mac,
    // jsdom) environment — they are browser-level chords there. The bytes
    // are xterm's own decision; the assertion is that MY handler neither
    // fired nor swallowed them (each still produced xterm's answer).
    pressKey(r, { key: 'p', code: 'KeyP', keyCode: 80, metaKey: true })
    pressKey(r, {
      key: 'P',
      code: 'KeyP',
      keyCode: 80,
      altKey: true,
      metaKey: true,
      shiftKey: true,
    })
    pressKey(r, { key: 'o', code: 'KeyO', keyCode: 79, altKey: true, metaKey: true })
    expect(opener).not.toHaveBeenCalled()
    expect(received).toEqual(['\x1bp'])
    r.dispose()
  })
})

describe('XtermRenderer cell metric (nocx-yy9g)', () => {
  async function mountRenderer() {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    return { r, container }
  }

  it('reports the render service cell width — the same source FitAddon fits to', async () => {
    const { r } = await mountRenderer()
    // Access the private term via a cast — the test owns both sides,
    // exactly like the setReadOnly test above.
    const term = (r as unknown as Record<string, unknown>).term as
      { _core: Record<string, unknown> } | undefined
    term!._core._renderService = {
      dimensions: { css: { cell: { width: 8.5, height: 15.6 } } },
    }
    expect(r.cellWidth).toBe(8.5)
    r.dispose()
  })

  it('reports 0 when the render service cannot measure yet', async () => {
    const { r } = await mountRenderer()
    // jsdom has no layout, so xterm's own char-size measurement is 0 and
    // the fallback char-measure element measures nothing either.
    expect(r.cellWidth).toBe(0)
    r.dispose()
  })

  it('fires onCellDimsChange once at the end of mount (fonts loaded, atlas attached)', async () => {
    const r = new XtermRenderer()
    const cb = vi.fn()
    r.onCellDimsChange(cb)
    stubBrowser()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    expect(cb).toHaveBeenCalledTimes(1)
    r.dispose()
  })

  it('re-fires onCellDimsChange when the device pixel ratio changes', async () => {
    const changeListeners: Array<() => void> = []
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: (type: string, l: EventListenerOrEventListenerObject) => {
        if (type === 'change') changeListeners.push(l as () => void)
      },
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    // Registered after mount so the mount-end fire is not counted.
    const cb = vi.fn()
    r.onCellDimsChange(cb)
    for (const l of changeListeners) l()
    expect(cb).toHaveBeenCalledTimes(1)
    r.dispose()
  })
})

// The live region hides the shell's echo of the command by translating the
// grid up by one row (scrollback/controller.ts `_echoShiftPx`), so `cellHeight`
// has to be the pitch the grid is DRAWN at. It was a second derivation
// instead: the `.xterm-char-measure-element` box, which xterm styles
// `line-height: normal`, with `ceil(FONT_SIZE * LINE_HEIGHT)` behind it. On a
// Retina Mac xterm picks its OffscreenCanvas measure strategy and never
// creates that element, so the constant 17 was what the shift used against a
// real pitch of 20 — and 3px of the echoed command stayed on screen, cut
// across the middle (nocx-rnrl, measured in the app: dpr 2, cell 8.5 × 20).
describe('XtermRenderer cellHeight is the grid row pitch (nocx-rnrl)', () => {
  async function mountRenderer() {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    return r
  }

  function publishDims(r: XtermRenderer, cell: { width: number; height: number }): void {
    const term = (r as unknown as Record<string, unknown>).term as
      { _core: Record<string, unknown> } | undefined
    term!._core._renderService = { dimensions: { css: { cell } } }
  }

  it('measures the rows the block will keep, and not the row the cursor moved to', async () => {
    // The row the cursor sits on after a newline is the row the NEXT prompt
    // will occupy: the frozen block ends at its render fence, one row above
    // it. Counting it made the live region one row taller than the block it
    // becomes, and the pane dropped by exactly that at every freeze
    // (nocx-i4h04.1). Two written rows, cursor parked on the third: two.
    const r = await mountRenderer()
    // The rows go in BEFORE the dimensions are published: `publishDims`
    // replaces the render service with a measurement stub, and xterm's write
    // path calls into the real one.
    // The 133 marker after the text is the stream-order sync point this file
    // already uses: writes are async, and when the marker lands the rows
    // before it are in the buffer.
    let markerDone: () => void
    const marker = new Promise<void>((resolve) => {
      markerDone = resolve
    })
    r.onCommandMarker(() => markerDone())
    r.write('one\r\ntwo\r\n')
    r.write('\x1b]133;A\x07')
    await marker
    publishDims(r, { width: 8.5, height: 20 })

    expect(r.liveContentHeight()).toBe(40)
    r.dispose()
  })

  it('reports nothing for a grid nobody has written to', async () => {
    // A blank grid with the cursor in its corner is not one row of content —
    // it is none, and the live region must not reserve a row for it between
    // the keypress and the first byte of output.
    const r = await mountRenderer()
    publishDims(r, { width: 8.5, height: 20 })

    expect(r.liveContentHeight()).toBe(0)
    r.dispose()
  })

  it('reports the pitch the grid is fitted to, not the char box', async () => {
    const r = await mountRenderer()
    publishDims(r, { width: 8.5, height: 20 })
    // The same source cellWidth, fit() and liveContentHeight() already read:
    // one owner for the grid's geometry, both halves off it.
    expect(r.cellHeight).toBe(20)
    expect(r.cellWidth).toBe(8.5)
    r.dispose()
  })

  it('does not settle on the pre-measurement guess once the grid can measure', async () => {
    const r = await mountRenderer()
    // Before the render service has dimensions there is nothing to report but
    // a guess; the defect is keeping it. A cached guess outlives every frame
    // until a grid resize happens to clear it, and a DPR change need not
    // resize the grid at all.
    expect(r.cellHeight).toBe(17)
    publishDims(r, { width: 8.5, height: 20 })
    expect(r.cellHeight).toBe(20)
    r.dispose()
  })

  it('re-reads the pitch when the device pixel ratio changes', async () => {
    const changeListeners: Array<() => void> = []
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: (type: string, l: EventListenerOrEventListenerObject) => {
        if (type === 'change') changeListeners.push(l as () => void)
      },
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    publishDims(r, { width: 8.5, height: 20 })
    expect(r.cellHeight).toBe(20)

    // Dragged to a display with a different ratio: xterm re-snaps its cell to
    // whole device pixels, and the grid keeps its rows and columns — so no
    // resize fires and nothing else would clear the cache.
    publishDims(r, { width: 8.4, height: 19 })
    for (const l of changeListeners) l()
    expect(r.cellHeight).toBe(19)
    r.dispose()
  })

  it('never reports a char measurement as a cell', async () => {
    const r = await mountRenderer()
    const term = (r as unknown as Record<string, unknown>).term as
      { element?: HTMLElement; _core: Record<string, unknown> } | undefined
    term!._core._renderService = undefined
    // xterm's DOM measure strategy leaves a span of 32 'W's behind when it is
    // the one selected. Its box is neither a cell width (it is 32 of them) nor
    // a row pitch (it carries no lineHeight), and reading it was how both lies
    // got in. Given layout, it must still change nothing.
    const el = term!.element
    expect(el).toBeDefined()
    // jsdom measures nothing, so xterm's own spans report 0 and would be
    // skipped whatever the code did. Clear them and leave exactly one span
    // that DOES measure — otherwise this test passes without asserting.
    for (const stale of Array.from(el!.querySelectorAll('.xterm-char-measure-element'))) {
      stale.remove()
    }
    const span = document.createElement('span')
    span.className = 'xterm-char-measure-element'
    span.textContent = 'W'.repeat(32)
    Object.defineProperty(span, 'getBoundingClientRect', {
      value: () => ({ width: 272, height: 16.7 }) as DOMRect,
    })
    el!.appendChild(span)

    expect(r.cellWidth).toBe(0)
    expect(r.cellHeight).toBe(17)
    r.dispose()
  })
})

describe('XtermRenderer contrast floor', () => {
  // nocx-3lrm. xterm.js renders the palette literally, and mc's default skin
  // paints its panels with an ANSI colour as the BACKGROUND
  // (`_default_ = lightgray;blue`). Under tokyo-night that pair is 1.19:1 —
  // the owner's mc over ssh was unreadable, while the same mc in Warp was
  // fine, because Warp raises the foreground against the actual cell
  // background. xterm.js has the same mechanism as an option whose default is
  // documented as "1: do nothing", and we never set it.
  //
  // The assertion is on the live Terminal, not on a constant: a constant
  // proves someone typed a number, not that the renderer ships it.
  it('applies a minimum contrast ratio to the terminal it creates', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const term = (r as unknown as Record<string, unknown>).term as
      { options: { minimumContrastRatio?: number } } | undefined
    expect(term).toBeDefined()
    // 4.5 is the WCAG AA floor — the threshold the theme audit measured
    // against. Anything at or below 1 is xterm's "do nothing".
    expect(term!.options.minimumContrastRatio).toBe(4.5)
  })
})

describe('XtermRenderer frame capture surface (nocx-3j9b)', () => {
  // The renderer reports the frame facts: parse-settles, explicit clear/
  // reset, the pending-write fence, the cursor column. The identity semantics
  // (generation, comparability, alt-session minting) live in frame/ and are
  // tested there; this surface is the wire into xterm.
  it('fires onWriteParsed after a write parses into the buffer', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const fired = vi.fn()
    r.onWriteParsed(fired)
    r.write('hello')
    // Wait on the observable state (the event), never on a duration.
    await vi.waitFor(() => expect(fired).toHaveBeenCalled())
    r.dispose()
  })

  it('hasUnsettledWrite is true right after write() and false once the parse settles — the capture fence', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    r.write('x')
    expect(r.hasUnsettledWrite()).toBe(true)
    await vi.waitFor(() => expect(r.hasUnsettledWrite()).toBe(false))
    r.dispose()
  })

  it('attaches a subscriber registered BEFORE mount — the generation signal is not lost', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })

    const fired = vi.fn()
    r.onWriteParsed(fired)
    await r.mount(container)
    r.write('x')
    await vi.waitFor(() => expect(fired).toHaveBeenCalled())
    r.dispose()
  })

  it('fires onClear after clearViewport and onReset after reset — the explicit mutations', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const cleared = vi.fn()
    const reset = vi.fn()
    r.onClear(cleared)
    r.onReset(reset)
    r.clearViewport()
    expect(cleared).toHaveBeenCalledTimes(1)
    r.reset()
    expect(reset).toHaveBeenCalledTimes(1)
    r.dispose()
  })
  it('reports the cursor column after a write', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    r.write('ab')
    await vi.waitFor(() => expect(r.cursorCol()).toBe(2))
    r.dispose()
  })

  it('a repaint does NOT advance the frame generation — refreshAtlas and applyTheme leave the identity untouched (ADR-0005)', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const tracker = new CaptureIdentityTracker(r)
    const before = tracker.identity()
    // Both of these force a full viewport refresh — the same class of
    // repaint ADR-0005's pump performs every 42ms on Linux/WebKitGTK. If the
    // generation moved on paint, a motionless screen would go stale forever
    // on one platform only.
    r.refreshAtlas()
    r.applyTheme(getCurrentTheme())
    expect(tracker.identity()).toEqual(before)
    r.dispose()
  })

  it('a write advances the generation through the real renderer, and awaitSettled opens the fence', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const tracker = new CaptureIdentityTracker(r)
    const before = tracker.identity()
    r.write('hello')
    await vi.waitFor(() => expect(tracker.identity().generation).toBe(before.generation + 1))
    // The fence on the real renderer: after the write settles there is
    // nothing pending, so awaitSettled resolves immediately.
    await tracker.awaitSettled()
    expect(r.hasUnsettledWrite()).toBe(false)
    r.dispose()
  })

  it('captureLiveFrame mints the visible screen through the real renderer (nocx-ljfwz)', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    r.write('hi')
    await vi.waitFor(() => expect(r.hasUnsettledWrite()).toBe(false))
    const frame = await r.captureLiveFrame()
    expect(frame.provenance.source).toBe('live')
    if (frame.provenance.source !== 'live') throw new Error('expected a live provenance')
    expect(frame.provenance.identity.cols).toBe(r.cols)
    expect(frame.rows).toHaveLength(r.rows)
    // The first row carries the written text: the mint reads the buffer the
    // renderer owns — the same code the push path would use.
    const first = frame.rows[0]
    if (first.kind !== 'cells') throw new Error('expected a cells row')
    const text = first.cells
      .slice(0, 2)
      .map((c) => c.char)
      .join('')
    expect(text).toBe('hi')
    r.dispose()
  })

  it('captureLiveFrame clamps a region to the buffer and refuses one past the end (nocx-ljfwz)', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    r.write('x')
    await vi.waitFor(() => expect(r.hasUnsettledWrite()).toBe(false))

    // A region beyond the current buffer clamps: rows are minted for the
    // overlap, the frame states the actual span.
    const clamped = await r.captureLiveFrame({ start: 0, end: 10_000 })
    if (clamped.provenance.source !== 'live') throw new Error('expected a live provenance')
    expect(clamped.provenance.range.end - clamped.provenance.range.start).toBe(clamped.rows.length)
    expect(clamped.provenance.range.end).toBeLessThanOrEqual(10_000)

    // A region entirely past the end is refused — the renderer never lies
    // about gaps.
    await expect(r.captureLiveFrame({ start: 1_000_000, end: 1_000_001 })).rejects.toThrow(
      ReadScreenRangeError,
    )
    r.dispose()
  })
})

describe('XtermRenderer write refusal under flow control (nocx-x8s2.3)', () => {
  async function mountRenderer(): Promise<XtermRenderer> {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    return r
  }

  it('a refused write reaches the caller AND leaves the fence counter exact', async () => {
    const r = await mountRenderer()

    const term = (r as unknown as Record<string, unknown>).term as {
      write: (data: string, cb?: () => void) => void
    }
    const spy = vi.spyOn(term, 'write').mockImplementation(() => {
      // xterm's real refusal: WriteBuffer throws before queueing anything
      // once the pending-data watermark is exceeded.
      throw new Error('write data discarded, use flow control to avoid losing data')
    })
    try {
      expect(r.hasUnsettledWrite()).toBe(false)
      // The refusal is surfaced, never swallowed — before the fix the catch
      // repaired the counter and returned success, so dropped output was
      // indistinguishable from a program that printed nothing.
      expect(() => r.write('overflow')).toThrow('write data discarded')
      // The counter was repaired: the refusal did not wedge the fence.
      expect(r.hasUnsettledWrite()).toBe(false)
    } finally {
      spy.mockRestore()
    }
    r.dispose()
  })

  it('a normal write still succeeds and still settles — the paired positive', async () => {
    const r = await mountRenderer()
    expect(() => r.write('hello')).not.toThrow()
    expect(r.hasUnsettledWrite()).toBe(true)
    await vi.waitFor(() => expect(r.hasUnsettledWrite()).toBe(false))
    r.dispose()
  })
})

describe('XtermRenderer pre-mount subscriptions (nocx-x8s2.4)', () => {
  it('delivers a resize and a buffer switch to subscribers registered BEFORE mount', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const resizes: Array<[number, number]> = []
    const buffers: Array<'normal' | 'alternate'> = []
    r.onResize((cols, rows) => resizes.push([cols, rows]))
    r.onBufferChange((t) => buffers.push(t))

    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    // Resize through the real terminal (xterm fires onResize synchronously).
    const term = (r as unknown as Record<string, unknown>).term as {
      resize: (cols: number, rows: number) => void
    }
    term.resize(100, 30)
    expect(resizes).toContainEqual([100, 30])

    // Buffer switch through the real parser: enter the alternate screen.
    r.write('\x1b[?1049h')
    await vi.waitFor(() => expect(buffers).toContain('alternate'))
    r.dispose()
  })

  it('a tracker built before mount reports notComparable across a buffer switch and a resize', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const tracker = new CaptureIdentityTracker(r) // BEFORE mount — the defect
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    // Buffer switch: incomparability, never "moved" (ADR-0029 rule 2). A
    // pre-mount subscription that was silently dropped would leave the
    // tracker believing the normal buffer still owns the screen.
    const normal = tracker.identity()
    r.write('\x1b[?1049h')
    await vi.waitFor(() => expect(tracker.identity().buffer.kind).toBe('alternate'))
    expect(tracker.compareIdentity(normal)).toEqual({ status: 'notComparable' })

    // Leave the alternate screen, then resize: also not comparable.
    r.write('\x1b[?1049l')
    await vi.waitFor(() => expect(tracker.identity().buffer.kind).toBe('normal'))
    const preResize = tracker.identity()
    const term = (r as unknown as Record<string, unknown>).term as {
      resize: (cols: number, rows: number) => void
    }
    term.resize(90, 25)
    expect(tracker.compareIdentity(preResize)).toEqual({ status: 'notComparable' })
    r.dispose()
  })
})

describe('XtermRenderer disposal mid-capture (nocx-x8s2.4)', () => {
  it('a pending awaitSettled settles (rejects) when the renderer is disposed mid-write — this test hung before the fix', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    const tracker = new CaptureIdentityTracker(r)
    r.write('x') // queued; the fence is up — proven in this environment by
    // the capture-surface test above (hasUnsettledWrite() is true right
    // after write; parsing is async)
    const pending = tracker.awaitSettled() // waiter registered synchronously
    r.dispose() // the subscriptions go away — the waiter must settle, not
    // hang forever
    await expect(pending).rejects.toThrow(CaptureAbortedError)
  })
})

// ── The repaint that has to follow a grid resize (nocx-q18, nocx-jfgb) ────
//
// A resize rebuilds xterm's char atlas, and on the real WKWebView the cells
// that are not re-marked dirty go on drawing from the old one — mangled,
// overlapping glyphs. nocx-q18 shipped a viewport-wide repaint after every
// fit; e0d0a490 replaced the renderer's own ResizeObserver with fitViewport
// and did not carry the repaint across, so the corruption came back.
//
// It lives inside fitViewport rather than in the caller: the atlas belongs
// to the renderer, and a caller that has to remember to repaint after a
// resize is the caller that stopped remembering.
describe('XtermRenderer repaints after a grid resize (nocx-jfgb)', () => {
  /** Mount with a stubbed cell metric — jsdom has no layout, so the real
   *  measurement returns null and fitViewport would bail before resizing. */
  const mountWithCell = async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    ;(r as unknown as Record<string, unknown>)._getCellDims = () => ({ width: 10, height: 20 })
    const term = (r as unknown as Record<string, unknown>).term as {
      rows: number
      cols: number
      resize: (cols: number, rows: number) => void
      refresh: (start: number, end: number) => void
    }
    return { r, term }
  }

  it('repaints every row after the grid changes size', async () => {
    const { r, term } = await mountWithCell()
    const resize = vi.spyOn(term, 'resize')
    const refresh = vi.spyOn(term, 'refresh')

    r.fitViewport({ width: 800, height: 400 })

    expect(resize).toHaveBeenCalledWith(80, 20)
    expect(refresh).toHaveBeenCalledWith(0, term.rows - 1)
  })

  it('does not repaint when the grid is unchanged, so a growing live region does not repaint per frame', async () => {
    const { r, term } = await mountWithCell()
    r.fitViewport({ width: 800, height: 400 })
    const refresh = vi.spyOn(term, 'refresh')

    // Same grid, a viewport a few pixels taller: the live region delivers
    // this on every layout tick as output grows.
    r.fitViewport({ width: 800, height: 405 })

    expect(refresh).not.toHaveBeenCalled()
  })
})

describe('bracketed paste, read from the real parser', () => {
  const stubBrowser = () => {
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
  }

  // The paired "and on a normal machine it succeeds" for the multi-line
  // policy (AGENTS.md rule 2). Every other test of that policy MOCKS
  // bracketedPasteActive, so all of them would keep passing if the real read
  // never answered true — and a snippet with two lines would then be refused
  // for everybody, always.
  it('reports the mode a program turned on, and reports it off again', async () => {
    stubBrowser()
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)

    expect(r.bracketedPasteActive()).toBe(false)

    // write() is fire-and-forget; onWriteParsed is the renderer's own
    // "the bytes have been parsed" signal, and it is what a caller reading
    // parser state has to wait for.
    const parsed = (data: string) =>
      new Promise<void>((resolve) => {
        r.onWriteParsed(resolve)
        r.write(data)
      })
    await parsed('\x1b[?2004h')
    expect(r.bracketedPasteActive()).toBe(true)

    await parsed('\x1b[?2004l')
    expect(r.bracketedPasteActive()).toBe(false)
    r.dispose()
  })
})

describe('onNotification fan-out (ADR-0029)', () => {
  // jsdom lacks matchMedia and ResizeObserver, which xterm.js / our mount
  // code uses during init. Stub them so the terminal can initialise.
  async function mountRenderer(): Promise<XtermRenderer> {
    window.matchMedia = (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })
    ;(globalThis as Record<string, unknown>).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    const r = new XtermRenderer()
    const container = document.createElement('div')
    Object.defineProperty(container, 'clientWidth', { value: 800 })
    Object.defineProperty(container, 'clientHeight', { value: 600 })
    await r.mount(container)
    return r
  }

  /** xterm parses writes asynchronously, so the assertion cannot follow the
   *  write on the same turn. Wait on an observable state change rather than a
   *  duration (AGENTS.md): write the payloads, then a sentinel notification,
   *  and return once the sentinel arrives. The parser preserves order, so
   *  everything written before it has been delivered by then — which is what
   *  lets a test assert that nothing was raised. */
  const SENTINEL = '\x1b]777;notify;sentinel;flush\x07'

  async function requestsFrom(r: XtermRenderer, ...writes: string[]) {
    const seen: OscNotification[] = []
    const flushed = new Promise<void>((resolve) => {
      r.onNotification((req) => {
        if (req.title === 'sentinel') {
          resolve()
          return
        }
        seen.push(req)
      })
    })
    for (const w of writes) r.write(w)
    r.write(SENTINEL)
    await flushed
    return seen
  }

  it('carries an OSC 9 payload through the real parser as the body', async () => {
    const r = await mountRenderer()
    const seen = await requestsFrom(r, '\x1b]9;build finished\x07')
    expect(seen).toEqual([{ title: '', body: 'build finished' }])
  })

  it('splits an OSC 777 payload into title and body', async () => {
    const r = await mountRenderer()
    const seen = await requestsFrom(r, '\x1b]777;notify;deploy;to staging\x07')
    expect(seen).toEqual([{ title: 'deploy', body: 'to staging' }])
  })

  // The trap this whole path exists to disarm: ESC]9;4;… is the ConEmu
  // progress protocol, which a progress bar emits continuously. If it
  // reached the subscriber, any `npm install` would be a notification storm.
  it('raises nothing for the ConEmu progress form of OSC 9', async () => {
    const r = await mountRenderer()
    const seen = await requestsFrom(
      r,
      '\x1b]9;4;1;10\x07',
      '\x1b]9;4;1;50\x07',
      '\x1b]9;4;0\x07',
      '\x1b]9;4\x07',
    )
    expect(seen).toEqual([])
  })

  // Untrusted bytes from whatever the user ran: the handler must not throw
  // inside the parser callback, which would take the renderer down.
  it('survives malformed payloads on both idents without raising', async () => {
    const r = await mountRenderer()
    const seen = await requestsFrom(
      r,
      '\x1b]9;\x07',
      '\x1b]9;   \x07',
      '\x1b]777;\x07',
      '\x1b]777;notify\x07',
      '\x1b]777;precmd;x;y\x07',
    )
    expect(seen).toEqual([])
  })

  it('fans one request out to every subscriber', async () => {
    const r = await mountRenderer()
    const a: OscNotification[] = []
    const b: OscNotification[] = []
    r.onNotification((req) => a.push(req))
    r.onNotification((req) => b.push(req))
    await requestsFrom(r, '\x1b]9;done\x07')
    expect(a).toEqual([
      { title: '', body: 'done' },
      { title: 'sentinel', body: 'flush' },
    ])
    expect(b).toEqual(a)
  })

  // Both idents are one request: nothing downstream may depend on which
  // spelling a program chose, so they must land on the same subscriber list.
  it('delivers both spellings to one subscriber list', async () => {
    const r = await mountRenderer()
    const seen = await requestsFrom(r, '\x1b]9;one\x07', '\x1b]777;notify;two;three\x07')
    expect(seen).toEqual([
      { title: '', body: 'one' },
      { title: 'two', body: 'three' },
    ])
  })

  it('raises nothing after dispose', async () => {
    const r = await mountRenderer()
    const seen: OscNotification[] = []
    r.onNotification((req) => seen.push(req))
    r.dispose()
    r.write('\x1b]9;after dispose\x07')
    // No sentinel is possible here — dispose removed the handler, so nothing
    // can signal a flush. A generous turn count is the only option, and it is
    // sound in the negative direction: more turns can only ever ADD a
    // delivery, never hide one.
    for (let i = 0; i < 50; i++) await Promise.resolve()
    expect(seen).toEqual([])
  })
})
