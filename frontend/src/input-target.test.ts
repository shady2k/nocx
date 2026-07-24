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
  it('sends the doc as one bracketed paste followed by CR', async () => {
    const sendRaw = vi.fn()
    const t = new ShellInputTarget(sendRaw)
    await t.submit('echo hi')
    expect(sendRaw).toHaveBeenCalledTimes(1)
    expect(sendRaw).toHaveBeenCalledWith('\x1b[200~echo hi\x1b[201~\r')
  })
  it('preserves \\n so every line executes as a command separator (nocx-4ff.14)', async () => {
    const sendRaw = vi.fn()
    await new ShellInputTarget(sendRaw).submit('a\nb')
    expect(sendRaw).toHaveBeenCalledWith('\x1b[200~a\nb\x1b[201~\r')
  })
})
