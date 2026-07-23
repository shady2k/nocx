import { describe, it, expect, vi } from 'vitest'
import { createRegistry, type InputTarget, ShellInputTarget } from './input-target'

const fake = (id: string): InputTarget => ({
  id, label: id, submit: vi.fn(async () => {}),
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
  it('submits one bracketed paste + CR', async () => {
    const send = vi.fn()
    const t = new ShellInputTarget(send)
    await t.submit('echo hi', { targetId: 'shell' })
    expect(send).toHaveBeenCalledTimes(1)
    expect(send).toHaveBeenCalledWith('\x1b[200~echo hi\x1b[201~\r')
  })
  it('preserves a multi-line document inside the paste', async () => {
    const send = vi.fn()
    await new ShellInputTarget(send).submit('a\nb', { targetId: 'shell' })
    expect(send).toHaveBeenCalledWith('\x1b[200~a\nb\x1b[201~\r')
  })
})
