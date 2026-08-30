import { beforeEach, describe, expect, it } from 'vitest'

import {
  applySSHReconnect,
  autoReconnectDelayMs,
  AUTO_RECONNECT_ATTEMPTS,
  SSH_RECONNECT_DEFAULT,
  sshReconnectPolicy,
} from './reconnect-setting'

describe('the ssh reconnect policy', () => {
  beforeEach(() => applySSHReconnect(SSH_RECONNECT_DEFAULT))

  // Reconnecting by itself can duplicate work still running on the far host.
  // A fetch that failed is not permission to do that.
  it('defaults to asking, and a value it does not recognise leaves that alone', () => {
    expect(sshReconnectPolicy()).toBe('ask')
    applySSHReconnect(undefined)
    expect(sshReconnectPolicy()).toBe('ask')
    applySSHReconnect('sometimes')
    expect(sshReconnectPolicy()).toBe('ask')
    applySSHReconnect(true)
    expect(sshReconnectPolicy()).toBe('ask')
  })

  it('adopts each of the three the backend can send', () => {
    for (const value of ['auto', 'never', 'ask'] as const) {
      applySSHReconnect(value)
      expect(sshReconnectPolicy()).toBe(value)
    }
  })
})

describe('automatic attempts', () => {
  // A pane that retried forever would hammer a host that is down and hide
  // from the person that anything is wrong.
  it('is bounded', () => {
    expect(AUTO_RECONNECT_ATTEMPTS).toBeGreaterThan(0)
    expect(AUTO_RECONNECT_ATTEMPTS).toBeLessThanOrEqual(5)
  })

  // Three probes in three seconds all fail for the same reason: the network
  // is not back yet.
  it('backs off, and caps so the last attempt still lands while somebody is looking', () => {
    const delays = [1, 2, 3, 4, 5].map(autoReconnectDelayMs)
    for (let i = 1; i < delays.length; i++) {
      expect(delays[i]).toBeGreaterThanOrEqual(delays[i - 1])
    }
    expect(delays[0]).toBeLessThan(delays[2])
    expect(Math.max(...delays)).toBeLessThanOrEqual(8000)
  })
})
