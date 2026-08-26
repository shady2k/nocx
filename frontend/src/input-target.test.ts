import { describe, it, expect, vi } from 'vitest'
import { createRegistry, type InputTarget, ShellInputTarget } from './input-target'
import type { CommandAuthor } from './command-ledger'
const fake = (id: string, routesToShell = false, author: CommandAuthor = 'shell'): InputTarget => ({
  id,
  label: id,
  routesToShell,
  // The fake's author defaults to the human's shell; a test that needs an
  // agent-authored submission passes 'agent' explicitly.
  author,
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
  it('the shell IS the human — its author is the entries.kind value (nocx-iadtt)', () => {
    const t = new ShellInputTarget(vi.fn(), vi.fn())
    expect(t.author).toBe('shell')
  })
  it('delegates paste semantics to the renderer, then sends CR', async () => {
    const paste = vi.fn()
    const sendRaw = vi.fn()
    const t = new ShellInputTarget(paste, sendRaw)
    expect(t.routesToShell).toBe(true)
    await t.submit('echo hi')

    expect(paste).toHaveBeenCalledTimes(1)
    expect(paste).toHaveBeenCalledWith('echo hi')
    expect(sendRaw).toHaveBeenCalledTimes(1)
    expect(sendRaw).toHaveBeenCalledWith('\r')
    expect(paste.mock.invocationCallOrder[0]).toBeLessThan(sendRaw.mock.invocationCallOrder[0])
  })
  it('preserves \\n so every line executes as a command separator (nocx-4ff.14)', async () => {
    const paste = vi.fn()
    const sendRaw = vi.fn()
    const t = new ShellInputTarget(paste, sendRaw)
    expect(t.routesToShell).toBe(true)
    await t.submit('a\nb')
    expect(paste).toHaveBeenCalledWith('a\nb')
    expect(sendRaw).toHaveBeenCalledWith('\r')
  })
})

describe('a submit is routed by InputTargetRegistry.active() — never a panel boolean, never a second editor (nocx-x8s2.2)', () => {
  // The acceptance criterion names the MECHANISM: the ask's submit goes
  // through the registry's active target, the same registry that routes the
  // shell submit. This pins the contract the ask surface must satisfy —
  // the registry is the single router, and switching the active target
  // redirects submits to the other target and no other.
  it('a submit lands on the active target and on no other', async () => {
    const r = createRegistry()
    // The spies are held by name rather than read back off the target:
    // `InputTarget.submit` is a method signature, so asserting on
    // `target.submit` detaches a method from its object and eslint's
    // unbound-method rule refuses it — correctly, since a detached method
    // is exactly what this test must not accidentally exercise.
    const shellSubmit = vi.fn(async () => {})
    const agentSubmit = vi.fn(async () => {})
    const shell: InputTarget = {
      id: 'shell',
      label: 'shell',
      routesToShell: true,
      author: 'shell',
      submit: shellSubmit,
    }
    const agent: InputTarget = {
      id: 'agent',
      label: 'agent',
      routesToShell: false,
      author: 'agent',
      submit: agentSubmit,
    }
    r.register(shell)
    r.register(agent)

    // Agent target active: the ask routes to the agent; the shell sees
    // nothing.
    r.setActive('agent')
    await r.active().submit('what did that block print?', { targetId: 'agent' })
    expect(agentSubmit).toHaveBeenCalledTimes(1)
    expect(shellSubmit).not.toHaveBeenCalled()

    // Back to the shell target: the ordinary submit returns to the PTY.
    r.setActive('shell')
    await r.active().submit('echo hi', { targetId: 'shell' })
    expect(shellSubmit).toHaveBeenCalledTimes(1)
    expect(agentSubmit).toHaveBeenCalledTimes(1)
  })

  it('the agent target is registered under the ADR-0004 §3 id and becomes active()', () => {
    // ADR-0004 §3 names the vocabulary: 'shell' | 'agent' | …. The ask
    // surface must arrive as a registered target under 'agent' — the
    // mechanism half of "routed by InputTargetRegistry.active()".
    const r = createRegistry()
    r.register(fake('shell'))
    const agent = fake('agent')
    r.register(agent)
    r.setActive('agent')
    expect(r.active()).toBe(agent)
  })
})
