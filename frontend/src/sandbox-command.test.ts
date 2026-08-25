import { describe, expect, it } from 'vitest'
import { recognizeSandboxCommand, SANDBOX_COMMAND } from './sandbox-command'

describe('recognizeSandboxCommand — the typed `/sandbox` parser', () => {
  it.each([
    ['/sandbox', true],
    ['  /sandbox  ', true],
    ['/sandbox\n', true],
    ['/sandbox x', false],
    ['/sandbox  --help', false],
    ['/Sandbox', false],
    ['/SANDBOX', false],
    ['echo /sandbox', false],
    ['/sandbox-extra', false],
    ['//sandbox', false],
    ['sandbox', false],
    ['', false],
    ['   ', false],
  ])('maps %j to %s', (doc, expected) => {
    expect(recognizeSandboxCommand(doc)).toBe(expected)
  })

  it('recognizes exactly the exported constant', () => {
    expect(recognizeSandboxCommand(SANDBOX_COMMAND)).toBe(true)
    expect(SANDBOX_COMMAND).toBe('/sandbox')
  })
})
