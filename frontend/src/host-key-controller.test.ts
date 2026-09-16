import { describe, expect, it, vi } from 'vitest'
import {
  OpenHostKeyRequestQueue,
  type OpenHostKeyRequest,
  HelperConsentAskQueue,
  type HelperConsentAskRequest,
} from './host-key-controller'
import type { HelperConsentAskEvidence, HostKeyErrorEvidence } from './terminal-content'

function evidence(knownHostsHost: string, key: string): HostKeyErrorEvidence {
  return {
    host: 'db.example.com:22',
    knownHostsHost,
    changed: false,
    algorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:abc',
    key,
    profileId: 'ssh:test',
  }
}

describe('OpenHostKeyRequestQueue', () => {
  it('one accepted key resolves every queued request for the same route and key', async () => {
    const active: Array<OpenHostKeyRequest | null> = []
    const queue = new OpenHostKeyRequestQueue((request) => active.push(request))

    const first = queue.request(evidence('nocx-v1-route:22', 'a2V5'), new AbortController().signal)
    const accepted = active[active.length - 1]
    if (!accepted) throw new Error('first request did not become active')
    expect(accepted).not.toBeNull()

    const duplicate = queue.request(
      evidence('nocx-v1-route:22', 'a2V5'),
      new AbortController().signal,
    )
    const different = queue.request(
      evidence('nocx-v1-other:22', 'b3RoZXI='),
      new AbortController().signal,
    )
    const duplicateSettled = vi.fn()
    const differentSettled = vi.fn()
    void duplicate.then(duplicateSettled)
    void different.then(differentSettled)

    queue.settleMatchingQueued(accepted, true)
    await expect(duplicate).resolves.toBe(true)
    expect(differentSettled).not.toHaveBeenCalled()

    queue.settle(accepted, true)
    await expect(first).resolves.toBe(true)
    expect(active[active.length - 1]?.evidence.knownHostsHost).toBe('nocx-v1-other:22')

    const remaining = active[active.length - 1]
    if (!remaining) throw new Error('different request did not become active')
    queue.settle(remaining, false)
    await expect(different).resolves.toBe(false)
  })

  it('an abort closes only its own pending decision', async () => {
    const active: Array<OpenHostKeyRequest | null> = []
    const queue = new OpenHostKeyRequestQueue((request) => active.push(request))
    const controller = new AbortController()
    const pending = queue.request(evidence('nocx-v1-route:22', 'a2V5'), controller.signal)
    controller.abort()
    await expect(pending).resolves.toBe(false)
  })
})

function helperAskEvidence(fingerprint: string): HelperConsentAskEvidence {
  return { host: 'db.example.com:22', fingerprint, hostKey: null }
}

// The connect-time ask (ADR-0069) reuses the exact queueing mechanism the
// host-key ask above uses (AD-8) — this pins that the generalisation kept
// its own identity: two asks are "the same question" when they share a
// fingerprint (ADR-0034's own identity), not a knownHostsHost/key pair,
// which a helper-only ask does not even carry — and that a decline resolves
// null, never false, since the answer is a method or nothing.
describe('HelperConsentAskQueue', () => {
  it('one chosen method resolves every queued request for the same fingerprint', async () => {
    const active: Array<HelperConsentAskRequest | null> = []
    const queue = new HelperConsentAskQueue((request) => active.push(request))

    const first = queue.request(helperAskEvidence('SHA256:abc'), new AbortController().signal)
    const accepted = active[active.length - 1]
    if (!accepted) throw new Error('first request did not become active')

    const duplicate = queue.request(helperAskEvidence('SHA256:abc'), new AbortController().signal)
    const different = queue.request(helperAskEvidence('SHA256:other'), new AbortController().signal)
    const differentSettled = vi.fn()
    void different.then(differentSettled)

    queue.settleMatchingQueued(accepted, 'script')
    await expect(duplicate).resolves.toBe('script')
    expect(differentSettled).not.toHaveBeenCalled()

    queue.settle(accepted, 'script')
    await expect(first).resolves.toBe('script')
    expect(active[active.length - 1]?.evidence.fingerprint).toBe('SHA256:other')
  })

  it('an abort closes only its own pending decision, not a different tab asking about a different machine', async () => {
    const active: Array<HelperConsentAskRequest | null> = []
    const queue = new HelperConsentAskQueue((request) => active.push(request))
    const controller = new AbortController()

    const aborted = queue.request(helperAskEvidence('SHA256:abc'), controller.signal)
    const other = queue.request(helperAskEvidence('SHA256:other'), new AbortController().signal)
    const otherSettled = vi.fn()
    void other.then(otherSettled)

    controller.abort()
    await expect(aborted).resolves.toBeNull()
    expect(otherSettled).not.toHaveBeenCalled()
  })
})
