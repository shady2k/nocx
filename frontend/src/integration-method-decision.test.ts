import { describe, expect, it, vi } from 'vitest'
import { decideIntegrationMethod } from './integration-method-decision'
import { HelperConsentAskQueue, type HelperConsentAskRequest } from './host-key-controller'
import { RpcError } from './dispatcher'

const evidence = { host: 'host.example:22', fingerprint: 'SHA256:abc', hostKey: null }
const note = `The method applies to this connection, but the host's record was not saved, so this host will be asked again after a restart`

function setup() {
  let active: HelperConsentAskRequest | null = null
  const helperConsentAsks = new HelperConsentAskQueue((request) => {
    active = request
  })
  const answer = helperConsentAsks.request(evidence, new AbortController().signal)
  const request = active!
  const queuedAnswer = helperConsentAsks.request(evidence, new AbortController().signal)
  const setHelperConsentAskBusy = vi.fn()
  const showToast = vi.fn()
  return {
    helperConsentAsks,
    answer,
    queuedAnswer,
    request,
    setHelperConsentAskBusy,
    showToast,
    active: () => active,
  }
}

describe('decideIntegrationMethod', () => {
  for (const hostRecordSaved of [false, true]) {
    it(`settles active and queued asks when hostRecordSaved is ${hostRecordSaved}`, async () => {
      const state = setup()
      const setIntegrationMethod = vi
        .fn()
        .mockResolvedValue({ ...evidence, method: 'script', hostRecordSaved })
      await decideIntegrationMethod(state.request, 'script', {
        ...state,
        profileClient: { setIntegrationMethod },
      })
      const expected = { method: 'script', note: hostRecordSaved ? undefined : note }
      expect(await state.answer).toEqual(expected)
      expect(await state.queuedAnswer).toEqual(expected)
      expect(state.request.settled).toBe(true)
      expect(state.active()).toBeNull()
      expect(setIntegrationMethod).toHaveBeenCalledWith(
        evidence.fingerprint,
        'script',
        evidence.host,
      )
      expect(state.setHelperConsentAskBusy.mock.calls).toEqual([[true], [false]])
      expect(state.showToast).not.toHaveBeenCalled()
    })
  }

  it('leaves the ask open and clears busy on any RPC failure without exposing RPC text', async () => {
    const state = setup()
    const setIntegrationMethod = vi
      .fn()
      .mockRejectedValue(new RpcError('private RPC details', -32603, { answerApplied: true }))
    await decideIntegrationMethod(state.request, 'helper', {
      ...state,
      profileClient: { setIntegrationMethod },
    })
    expect(state.request.settled).toBe(false)
    expect(state.active()).toBe(state.request)
    expect(state.setHelperConsentAskBusy.mock.calls).toEqual([[true], [false]])
    expect(state.showToast).toHaveBeenCalledExactlyOnceWith({
      level: 'danger',
      message: `Could not apply the integration method for ${evidence.host}. The choice was not saved.`,
      duration: 0,
    })
  })
})
