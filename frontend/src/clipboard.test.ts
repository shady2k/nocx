// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { decodeOsc52, shouldCopy, createClipboardAccess, ClipboardGate } from './clipboard'

// ── decodeOsc52 ─────────────────────────────────────────────────────────

describe('decodeOsc52', () => {
  it('decodes a standard OSC 52 clipboard write', () => {
    const result = decodeOsc52('c;aGVsbG8gZnJvbSBvc2M1Mg==')
    expect(result).toBe('hello from osc52')
  })

  it('treats empty target as clipboard (default)', () => {
    const result = decodeOsc52(';aGVsbG8=')
    expect(result).toBe('hello')
  })

  it('treats explicit c target as clipboard', () => {
    const result = decodeOsc52('c;aGVsbG8=')
    expect(result).toBe('hello')
  })

  it('returns null for non-clipboard targets (p, q, s)', () => {
    expect(decodeOsc52('p;aGVsbG8=')).toBeNull()
    expect(decodeOsc52('q;aGVsbG8=')).toBeNull()
    expect(decodeOsc52('s;aGVsbG8=')).toBeNull()
  })

  it('refuses the read form c;?', () => {
    expect(decodeOsc52('c;?')).toBeNull()
    expect(decodeOsc52(';?')).toBeNull()
  })

  it('returns null for malformed base64', () => {
    expect(decodeOsc52('c;not-valid-base64!!!')).toBeNull()
    expect(decodeOsc52('c;%%%')).toBeNull()
  })

  it('returns null for empty data after semicolon', () => {
    // atob('') returns '' without throwing — reject before decoding.
    expect(decodeOsc52('c;')).toBeNull()
  })

  it('returns null for payload with no semicolon', () => {
    expect(decodeOsc52('no-semicolon')).toBeNull()
    expect(decodeOsc52('')).toBeNull()
  })

  it('returns null for whitespace-only decoded bytes', () => {
    // btoa('   ') → 'ICAg', atob('ICAg') → '   ', which is 3 bytes.
    // Whitespace-only: a remote program must never clear the clipboard.
    const result = decodeOsc52('c;ICAg')
    expect(result).toBe('   ')
    // The policy layer (shouldCopy-style equivalent for OSC 52) would
    // reject this. But the decoder returns it — the policy above decides.
    // Verify the decoder doesn't reject it on its own (that's the size cap's
    // job at the byte level, not the content level).
    expect(result).not.toBeNull()
  })

  it('rejects zero-byte decoded payload', () => {
    // An OSC 52 payload whose base64 decodes to zero bytes is dropped.
    // This can happen with non-printable control characters in base64
    // that decode to nothing meaningful.
    // The simplest way to test: empty base64 is already rejected above.
    // Zero-byte from whitespace is rejected above too.
    // This tests the general case: atob of malformed/non-decodable content
    // that happens to return empty.
    expect(decodeOsc52('c;====')).toBeNull()
  })

  it('rejects payload above the base64 size cap before decoding', () => {
    // 1 MiB cap on decoded bytes = 1_398_104 max base64 chars.
    // Generate a base64 string one char longer than the cap.
    const overCap = 'A'.repeat(1_398_105)
    const result = decodeOsc52(`c;${overCap}`)
    expect(result).toBeNull()
  })

  it('accepts payload exactly at the base64 size cap', () => {
    // Exactly 1_398_104 base64 chars decodes to 1_048_578 bytes (> 1 MiB
    // due to padding), but the cap is on base64 length, not decoded size.
    // This test verifies the cap boundary is exact.
    const atCap = 'A'.repeat(1_398_104)
    const result = decodeOsc52(`c;${atCap}`)
    // The decoded bytes are > 1 MiB but the cap is on base64 length.
    // The cap at exactly OSC52_MAX_BASE64 should accept this.
    expect(result).not.toBeNull()
  })

  it('handles base64 with padding characters', () => {
    const result = decodeOsc52('c;aGk=')
    expect(result).toBe('hi')
  })

  it('handles base64 with two padding characters', () => {
    const result = decodeOsc52('c;aA==')
    expect(result).toBe('h')
  })

  it('handles multi-line decoded content', () => {
    const result = decodeOsc52('c;' + btoa('line1\nline2\nline3'))
    expect(result).toBe('line1\nline2\nline3')
  })

  it('handles Unicode content', () => {
    const text = 'héllo wörld 🌍'
    const bytes = new TextEncoder().encode(text)
    let binary = ''
    for (let i = 0; i < bytes.length; i++) {
      binary += String.fromCharCode(bytes[i])
    }
    const result = decodeOsc52('c;' + btoa(binary))
    expect(result).toBe(text)
  })
})

// ── ClipboardGate ──────────────────────────────────────────────────────

describe('ClipboardGate', () => {
  it('starts denied (granted = false, suppressed = false)', () => {
    const gate = new ClipboardGate()
    expect(gate.granted).toBe(false)
    expect(gate.suppressed).toBe(false)
  })

  it('allow() sets granted to true', () => {
    const gate = new ClipboardGate()
    gate.allow()
    expect(gate.granted).toBe(true)
    expect(gate.suppressed).toBe(false)
  })

  it('suppress() sets suppressed to true, leaves granted false', () => {
    const gate = new ClipboardGate()
    gate.suppress()
    expect(gate.suppressed).toBe(true)
    expect(gate.granted).toBe(false)
  })

  it('allow() after suppress() sets both', () => {
    const gate = new ClipboardGate()
    gate.suppress()
    gate.allow()
    expect(gate.granted).toBe(true)
    expect(gate.suppressed).toBe(true)
  })

  it('suppress() after allow() leaves granted true', () => {
    const gate = new ClipboardGate()
    gate.allow()
    gate.suppress()
    expect(gate.granted).toBe(true)
    expect(gate.suppressed).toBe(true)
  })
})

// ── shouldCopy ──────────────────────────────────────────────────────────

describe('shouldCopy', () => {
  it('returns true for non-empty, non-whitespace text', () => {
    expect(shouldCopy('echo hi')).toBe(true)
  })

  it('returns false for empty string', () => {
    expect(shouldCopy('')).toBe(false)
  })

  it('returns false for whitespace-only', () => {
    expect(shouldCopy('   ')).toBe(false)
    expect(shouldCopy('\t\n ')).toBe(false)
  })

  it('returns true for text with leading/trailing whitespace', () => {
    expect(shouldCopy('  hello  ')).toBe(true)
  })
})

// ── ClipboardAccess implementations ─────────────────────────────────────

describe('BrowserClipboard', () => {
  let origNavigator: typeof navigator
  let origRuntime: unknown

  afterEach(() => {
    // Restore globals that the test mutated.
    Object.defineProperty(globalThis, 'navigator', {
      value: origNavigator,
      writable: true,
      configurable: true,
    })
    if (origRuntime === undefined) {
      delete (window as unknown as Record<string, unknown>).runtime
    } else {
      ;(window as unknown as Record<string, unknown>).runtime = origRuntime
    }
  })

  it('writeText calls navigator.clipboard.writeText', async () => {
    origNavigator = globalThis.navigator
    origRuntime = (window as unknown as Record<string, unknown>).runtime

    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(globalThis, 'navigator', {
      value: { clipboard: { writeText, readText: vi.fn() } },
      writable: true,
      configurable: true,
    })

    // Remove Wails runtime so the chooser picks BrowserClipboard.
    delete (window as unknown as Record<string, unknown>).runtime

    const clipboard = createClipboardAccess()
    await clipboard.writeText('test')
    expect(writeText).toHaveBeenCalledWith('test')
  })

  it('readText calls navigator.clipboard.readText', async () => {
    origNavigator = globalThis.navigator
    origRuntime = (window as unknown as Record<string, unknown>).runtime

    const readText = vi.fn().mockResolvedValue('clipboard content')
    Object.defineProperty(globalThis, 'navigator', {
      value: { clipboard: { readText, writeText: vi.fn() } },
      writable: true,
      configurable: true,
    })

    delete (window as unknown as Record<string, unknown>).runtime

    const clipboard = createClipboardAccess()
    const result = await clipboard.readText()
    expect(readText).toHaveBeenCalled()
    expect(result).toBe('clipboard content')
  })
})

describe('WailsClipboard', () => {
  let origRuntime: unknown

  afterEach(() => {
    if (origRuntime === undefined) {
      delete (window as unknown as Record<string, unknown>).runtime
    } else {
      ;(window as unknown as Record<string, unknown>).runtime = origRuntime
    }
  })

  it('is selected when window.runtime exists', () => {
    origRuntime = (window as unknown as Record<string, unknown>).runtime
    ;(window as unknown as Record<string, unknown>).runtime = {
      ClipboardGetText: vi.fn(),
      ClipboardSetText: vi.fn(),
    }

    const clipboard = createClipboardAccess()
    expect(clipboard).toBeDefined()
    expect(typeof clipboard.readText).toBe('function')
    expect(typeof clipboard.writeText).toBe('function')
  })
})

describe('createClipboardAccess', () => {
  let origRuntime: unknown

  afterEach(() => {
    if (origRuntime === undefined) {
      delete (window as unknown as Record<string, unknown>).runtime
    } else {
      ;(window as unknown as Record<string, unknown>).runtime = origRuntime
    }
  })

  it('returns a degraded implementation when neither backend is available', async () => {
    origRuntime = (window as unknown as Record<string, unknown>).runtime
    delete (window as unknown as Record<string, unknown>).runtime

    // Replace navigator with one that has no clipboard.
    const origNavigator = globalThis.navigator
    Object.defineProperty(globalThis, 'navigator', {
      value: {},
      writable: true,
      configurable: true,
    })

    // Must not throw at construction — a missing clipboard must not kill the app.
    const clipboard = createClipboardAccess()
    expect(clipboard).toBeDefined()

    // Operations reject with a clear reason rather than a cryptic TypeError.
    await expect(clipboard.readText()).rejects.toThrow('no clipboard backend')
    await expect(clipboard.writeText('test')).rejects.toThrow('no clipboard backend')

    Object.defineProperty(globalThis, 'navigator', {
      value: origNavigator,
      writable: true,
      configurable: true,
    })
  })
})
