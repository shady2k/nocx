// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createPaneContext, updatePaneContext } from './pane-context'

describe('createPaneContext — the DOM contract', () => {
  it('local: folder icon, muted path, no branch when none is known', () => {
    const el = createPaneContext({ kind: 'local', path: '~/repos/nocx' })
    expect(el.className).toBe('ui-pane-context')
    expect(el.dataset.kind).toBe('local')
    expect(el.hasAttribute('data-split')).toBe(false)
    expect(el.hasAttribute('data-active')).toBe(false)
    expect(el.querySelector('svg')).not.toBeNull()
    const path = el.querySelector<HTMLElement>('.ui-pane-context__path')
    expect(path?.textContent).toBe('~/repos/nocx')
    expect(el.querySelector('.ui-pane-context__divider')).toBeNull()
    expect(el.querySelector('.ui-pane-context__branch')).toBeNull()
    // Chrome muted, not PromptContext's accent — see pane-context.ts's own
    // file-header note on why this is a local renderer rather than
    // PromptContext's chrome presentation.
    expect(path?.className).toBe('ui-pane-context__path')
  })

  it('local: a known branch adds a divider and the branch text', () => {
    const el = createPaneContext({ kind: 'local', path: '~/repos/nocx', branch: 'main' })
    expect(el.querySelector('.ui-pane-context__divider')).not.toBeNull()
    const branch = el.querySelector<HTMLElement>('.ui-pane-context__branch')
    expect(branch?.textContent).toBe('main')
  })

  it('remote: server icon, host label, path — never a branch (spec §3: remote is not walked for one)', () => {
    const el = createPaneContext({
      kind: 'remote',
      host: 'dev@staging',
      path: '/srv/nocx',
      branch: 'should-not-render',
    })
    expect(el.dataset.kind).toBe('remote')
    const host = el.querySelector<HTMLElement>('.ui-pane-context__host')
    expect(host?.textContent).toBe('dev@staging')
    const path = el.querySelector<HTMLElement>('.ui-pane-context__path')
    expect(path?.textContent).toBe('/srv/nocx')
    expect(el.querySelector('.ui-pane-context__branch')).toBeNull()
  })

  it('program: the foreground program name, "Keyboard → target", and no Session actions control absent a callback', () => {
    const el = createPaneContext({
      kind: 'program',
      program: 'nvim',
      path: '~/repos/nocx',
      keyboardTarget: 'nvim',
    })
    expect(el.dataset.kind).toBe('program')
    const program = el.querySelector<HTMLElement>('.ui-pane-context__program')
    expect(program?.textContent).toBe('nvim')
    const keyboard = el.querySelector<HTMLElement>('.ui-pane-context__keyboard')
    expect(keyboard?.textContent).toBe('Keyboard → nvim')
    expect(el.querySelector('.ui-button')).toBeNull()
  })

  it('program: Session actions renders only when the caller supplies the callback, and calling it fires the callback', () => {
    const onSessionActions = vi.fn()
    const el = createPaneContext(
      { kind: 'program', program: 'nvim', path: '~' },
      { onSessionActions },
    )
    const btn = el.querySelector<HTMLButtonElement>('.ui-button')
    expect(btn).not.toBeNull()
    expect(btn?.textContent).toBe('Session actions')
    btn?.click()
    expect(onSessionActions).toHaveBeenCalledTimes(1)
  })

  it('data-split and data-active reflect the split identity facts (spec §3: distinct from the active-TAB accent)', () => {
    const el = createPaneContext({ kind: 'local', path: '~', split: true, active: true })
    expect(el.dataset.split).toBe('true')
    expect(el.dataset.active).toBe('true')
  })

  it('updatePaneContext restates the SAME element in place, switching kind and clearing stale facts', () => {
    const el = createPaneContext({ kind: 'local', path: '~/repos/nocx', branch: 'main' })
    updatePaneContext(el, { kind: 'remote', host: 'dev@staging', path: '/srv/nocx' })
    expect(el.dataset.kind).toBe('remote')
    expect(el.querySelector('.ui-pane-context__branch')).toBeNull()
    expect(el.querySelector<HTMLElement>('.ui-pane-context__host')?.textContent).toBe('dev@staging')
  })
})
