import { afterEach, describe, expect, it, vi } from 'vitest'
import { decideIntegrationMethod } from './integration-method-decision'
import { HelperConsentAskQueue, type HelperConsentAskRequest } from './host-key-controller'
import { RpcError } from './dispatcher'
import { setActiveLocale } from './i18n/locale-model'

const evidence = { host: 'host.example:22', fingerprint: 'SHA256:abc', hostKey: null }
const note = `The method is in force for this host, but the host’s method was not saved. After a restart, nocx will use the previously saved method (or Auto if none) and any permission already saved for this machine.`

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
  afterEach(() => setActiveLocale('en'))

  it('explains the previously saved method and permission after restart in Russian', async () => {
    setActiveLocale('ru')
    const state = setup()
    const setIntegrationMethod = vi.fn().mockResolvedValue({
      ...evidence,
      method: 'script',
      hostRecordSaved: false,
      helperConsentSaved: false,
    })
    await decideIntegrationMethod(state.request, 'script', {
      ...state,
      profileClient: { setIntegrationMethod },
    })
    expect(await state.answer).toEqual({
      method: 'script',
      note: 'Метод действует для этой машины, но он не сохранён. После перезапуска nocx использует ранее сохранённый метод (или Auto, если его нет) и ранее сохранённое разрешение для этой машины.',
    })
  })

  for (const hostRecordSaved of [false, true]) {
    it(`settles active and queued asks when hostRecordSaved is ${hostRecordSaved}`, async () => {
      const state = setup()
      const setIntegrationMethod = vi.fn().mockResolvedValue({
        ...evidence,
        method: 'script',
        hostRecordSaved,
        helperConsentSaved: false,
      })
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

  it('reports durable helper consent separately from a mode that was not saved', async () => {
    const state = setup()
    const setIntegrationMethod = vi.fn().mockResolvedValue({
      ...evidence,
      method: 'helper',
      hostRecordSaved: false,
      helperConsentSaved: true,
    })
    await decideIntegrationMethod(state.request, 'helper', {
      ...state,
      profileClient: { setIntegrationMethod },
    })
    const expected = {
      method: 'helper',
      note: 'The helper method is in force for this host, but the host’s method was not saved. Permission to deploy the helper was saved and can be reused after a restart without asking again.',
    }
    expect(await state.answer).toEqual(expected)
    expect(await state.queuedAnswer).toEqual(expected)
    expect(state.showToast).not.toHaveBeenCalled()
  })

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
