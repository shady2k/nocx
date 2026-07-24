import { describe, it, expect } from 'vitest'
import { submitCommand } from './submit'

describe('submitCommand', () => {
  it('dispatches submit, refocuses the grid, then sends — in order (nocx-4ff.12)', () => {
    const calls: string[] = []
    submitCommand('echo hi', {
      dispatchSubmit: () => calls.push('dispatch'),
      focusGrid: () => calls.push('focus'),
      sendDoc: (d) => calls.push(`send:${d}`),
    })
    expect(calls).toEqual(['dispatch', 'focus', 'send:echo hi'])
  })
})
