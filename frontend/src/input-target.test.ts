import { describe, it, expect, vi } from 'vitest'
import { createRegistry, type InputTarget, ShellInputTarget } from './input-target'

const fake = (id: string): InputTarget => ({
  id,
  label: id,
  submit: vi.fn(async () => {}),
})

describe('InputTargetRegistry', () => {
  it('first registered target is active by default', () => {
    const r = createRegistry()
    r.register(fake('shell'))
    expect(r.active().id).toBe('shell')
  })
  it('setActive switches; unknown id throws', () => {
    const r = createRegistry()
    r.register(fake('shell'))
    r.register(fake('agent'))
    r.setActive('agent')
    expect(r.active().id).toBe('agent')
    expect(() => r.setActive('nope')).toThrow()
  })
  it('active() with no targets throws', () => {
    expect(() => createRegistry().active()).toThrow()
  })
})

describe('ShellInputTarget', () => {
  it('pastes the doc (engine owns bracketed-paste wrapping) then sends CR', async () => {
    const paste = vi.fn()
    const sendRaw = vi.fn()
    const t = new ShellInputTarget(paste, sendRaw)
    await t.submit('echo hi')
    expect(paste).toHaveBeenCalledTimes(1)
    expect(paste).toHaveBeenCalledWith('echo hi')
    expect(sendRaw).toHaveBeenCalledWith('\r')
  })
  it('never hand-rolls ESC[200~ wrappers on the raw channel', async () => {
    const paste = vi.fn()
    const sendRaw = vi.fn()
    await new ShellInputTarget(paste, sendRaw).submit('a\nb')
    expect(paste).toHaveBeenCalledWith('a\nb')
    for (const [arg] of sendRaw.mock.calls) expect(arg).not.toContain('200~')
  })
})
