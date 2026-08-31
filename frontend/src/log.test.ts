// @vitest-environment jsdom
// The logging seam's environment split, asserted where it is observable:
// a log line emitted with no backend to take it must be on the console by
// the time the call returns. Awaiting a rejected binding call to discover
// that puts every browser log line a microtask late — invisible to a
// synchronous reader (a test, a Playwright console listener, a person
// stepping through devtools) at the moment it mattered.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { setTransport } from '@wailsio/runtime'
import { log } from './log'
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
