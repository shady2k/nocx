import { describe, expect, it } from 'vitest'
import { RpcError } from './dispatcher'
import { integrationMethodAnswerApplied } from './integration-method-failure'

describe('integration method RPC failures', () => {
  it('distinguishes an answer applied in memory from a failure before applying it', () => {
    expect(
      integrationMethodAnswerApplied(new RpcError('not saved', -32603, { answerApplied: true })),
    ).toBe(true)
    expect(
      integrationMethodAnswerApplied(
        new RpcError('consent failed', -32603, { answerApplied: false }),
      ),
    ).toBe(false)
    expect(integrationMethodAnswerApplied(new RpcError('unrelated', -32603))).toBe(false)
    expect(integrationMethodAnswerApplied(new Error('not RPC'))).toBe(false)
  })
})
