// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { parseOsc7, parseOsc133, XtermRenderer } from './xterm'
import type { CommandMarkerEvent } from './types'

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

describe('onCommandMarker fan-out', () => {
  it('fans out one enriched event per marker to every subscriber', async () => {
    // jsdom lacks matchMedia and ResizeObserver, which xterm.js / our mount
    // code uses during init. Stub them so the terminal can initialise.
    const origMatchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    })) as typeof window.matchMedia
    const OrigResizeObserver = (globalThis as Record<string, unknown>).ResizeObserver
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
    const b = vi.fn((_ev: CommandMarkerEvent) => resolveDone())
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
    window.matchMedia = origMatchMedia
  })
})
