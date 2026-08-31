import { beforeEach, describe, expect, it, vi } from 'vitest'

const resolveBackend = vi.hoisted(() => vi.fn())

vi.mock('../bindings/github.com/shady2k/nocx/wailsapp', () => ({
  ResolveBackend: resolveBackend,
}))

import { createDesktopEndpointProvider } from './endpoint-desktop'

describe('createDesktopEndpointProvider', () => {
  beforeEach(() => {
    resolveBackend.mockReset()
  })

  it('maps a successful binding result to an endpoint', async () => {
    resolveBackend.mockResolvedValue({
      ok: true,
      host: '127.0.0.1',
      port: 4321,
      token: 'capability-token',
      kind: '',
      message: '',
      remedy: '',
    })

    await expect(createDesktopEndpointProvider().resolve()).resolves.toEqual({
      ok: true,
      endpoint: { host: '127.0.0.1', port: 4321, token: 'capability-token' },
    })
    expect(resolveBackend).toHaveBeenCalledOnce()
  })

  it('maps a classified launch failure without losing its message or remedy', async () => {
    resolveBackend.mockResolvedValue({
      ok: false,
      host: '',
      port: 0,
      token: '',
      kind: 'not-ready',
      message: 'The backend is not ready.',
      remedy: 'Retry the launch.',
    })

    await expect(createDesktopEndpointProvider().resolve()).resolves.toEqual({
      ok: false,
      failure: {
        kind: 'not-ready',
        message: 'The backend is not ready.',
        remedy: 'Retry the launch.',
      },
    })
  })

  it('turns a missing Wails runtime into a no-server failure instead of throwing', async () => {
    resolveBackend.mockRejectedValue(new Error('Wails runtime is unavailable'))

    await expect(createDesktopEndpointProvider().resolve()).resolves.toMatchObject({
      ok: false,
      failure: {
        kind: 'no-server',
      },
    })
  })
})
