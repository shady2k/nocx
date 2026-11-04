// @vitest-environment jsdom
// The logging seam's environment split, asserted where it is observable:
// a log line emitted with no backend to take it must be on the console by
// the time the call returns. Awaiting a rejected binding call to discover
// that puts every browser log line a microtask late — invisible to a
// synchronous reader (a test, a Playwright console listener, a person
// stepping through devtools) at the moment it mattered.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { setTransport } from '@wailsio/runtime'
import { log, logKeydown, setDecisionTracing } from './log'
import { installBrowserTransport } from './wails-runtime'

type BridgeWindow = typeof globalThis & { go?: unknown }

afterEach(() => {
  delete (globalThis as BridgeWindow).go
  setTransport(null)
  vi.restoreAllMocks()
})

describe('log — no Wails runtime (plain browser, jsdom)', () => {
  it('writes to the console synchronously, not one rejected promise later', () => {
    const info = vi.spyOn(console, 'log').mockImplementation(() => {})
    log.info('nocx: hello')
    // No await anywhere above: the assertion runs in the same tick.
    expect(info).toHaveBeenCalledWith('nocx: hello')
  })

  it('routes each level to its own console sink', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})
    log.warn('w')
    log.error('e')
    log.debug('d')
    expect(warn).toHaveBeenCalledWith('w')
    expect(error).toHaveBeenCalledWith('e')
    expect(debug).toHaveBeenCalledWith('d')
  })

  it('appends serialised fields to the message', () => {
    const info = vi.spyOn(console, 'log').mockImplementation(() => {})
    log.info('msg', { a: 1 })
    expect(info).toHaveBeenCalledWith('msg {"a":1}')
  })
})

// The instrument nocx-1ebrs.32 is settled with, asserted where a person
// actually reads it: a key a surface never sees and a key it sees and drops look
// identical from outside, and the four values the browser reported are what
// separates them. Off unless somebody asked for it — the trace is per
// keystroke.
describe('log — a keydown, as the browser reported it (nocx-1ebrs.32)', () => {
  const keyEvent = (init: KeyboardEventInit) =>
    new KeyboardEvent('keydown', { key: 'Enter', ...init })

  afterEach(() => setDecisionTracing(false))

  it('writes nothing until decision tracing is asked for', () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})
    logKeydown('grid', keyEvent({ code: 'NumpadEnter', keyCode: 13, location: 3 }))
    expect(debug).not.toHaveBeenCalled()
  })

  it('names the surface and the key class, and never a typed character', () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})
    setDecisionTracing(true)
    logKeydown('grid', keyEvent({ code: 'NumpadEnter', keyCode: 13, location: 3 }))
    expect(debug).toHaveBeenCalledTimes(1)
    const line = debug.mock.calls[0][0] as string
    expect(line).toContain('nocx:decide keydown')
    // A NAMED KEY IS NOT A CHARACTER (nocx-atpw5.1.39): `NumpadEnter` stays,
    // because it is the macOS defect's own discriminator and carries nothing
    // a person typed.
    expect(line).toContain(JSON.stringify({ where: 'grid', class: 'named', key: 'Enter' }))
    expect(line).not.toContain('NumpadEnter')
    expect(line).not.toContain('keyCode')
  })

  it('says char and nothing else for a character typed into the pane', () => {
    const debug = vi.spyOn(console, 'debug').mockImplementation(() => {})
    setDecisionTracing(true)
    // A password is letters too: this is the line that must never carry one.
    for (const key of ['h', 'Z', '7', ' ', 'é', '🙂']) {
      logKeydown('composer', keyEvent({ key }))
    }
    const lines = debug.mock.calls.map((call) => String(call[0]))
    expect(lines).toHaveLength(6)
    // EXACT, because the point is that the line has nothing else in it: a
    // `toContain` on the fields would still pass with the character beside
    // them.
    for (const line of lines) {
      expect(line).toBe('nocx:decide keydown {"where":"composer","class":"char"}')
    }
  })
})

describe('log — shimmed browser (dev-web, headless e2e)', () => {
  it('still uses the console: the window.go shim carries no Log', () => {
    ;(globalThis as BridgeWindow).go = {
      main: { WailsApp: { CheckForUpdate: () => Promise.resolve(null) } },
    }
    installBrowserTransport()
    const info = vi.spyOn(console, 'log').mockImplementation(() => {})
    log.info('browser line')
    expect(info).toHaveBeenCalledWith('browser line')
  })

  it('hands the line to the backend when the bridge does carry Log', () => {
    const backendLog = vi.fn(() => Promise.resolve())
    ;(globalThis as BridgeWindow).go = { main: { WailsApp: { Log: backendLog } } }
    installBrowserTransport()
    const info = vi.spyOn(console, 'log').mockImplementation(() => {})
    log.info('backend line')
    expect(backendLog).toHaveBeenCalledWith('backend line')
    expect(info).not.toHaveBeenCalled()
  })
})
