// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createPaneContext, updatePaneContext } from './pane-context'

describe('createPaneContext — the DOM contract', () => {
  it("local: folder icon, then PromptContext's chrome presentation carrying the path, no branch when none is known", () => {
    const el = createPaneContext({ kind: 'local', path: '~/repos/nocx' })
    expect(el.className).toBe('ui-pane-context')
    expect(el.dataset.kind).toBe('local')
    expect(el.hasAttribute('data-split')).toBe(false)
    expect(el.hasAttribute('data-active')).toBe(false)
    // The leading identity icon — PaneContext's own, not PromptContext's.
    expect(el.querySelector('svg')).not.toBeNull()
    // The where-line itself is PromptContext, in its `chrome` presentation
    // (round 2, nocx-9bpeq.23): PaneContext no longer paints path/branch
    // text of its own — see pane-context.ts's file header.
    const prompt = el.querySelector<HTMLElement>('.ui-prompt-context')
    expect(prompt).not.toBeNull()
    expect(prompt?.dataset.presentation).toBe('chrome')
    const path = prompt?.querySelector<HTMLElement>('[data-part="path"]')
    expect(path?.textContent).toBe('~/repos/nocx')
    expect(prompt?.querySelector('[data-part="host"]')).toBeNull()
    expect(prompt?.querySelector('[data-part="branch"]')).toBeNull()
  })

  it('local: a known branch renders through PromptContext\'s own "on" connector and branch icon', () => {
    const el = createPaneContext({ kind: 'local', path: '~/repos/nocx', branch: 'main' })
    const prompt = el.querySelector<HTMLElement>('.ui-prompt-context')
    expect(prompt?.querySelector('[data-part="on"]')).not.toBeNull()
    // Scoped to the where-line, not the whole strip: PaneContext's own
    // leading identity icon is a separate `<svg>` sibling, and PromptContext
    // draws a second one (GitBranchIcon) for the branch part.
    expect(prompt?.querySelector('svg')).not.toBeNull()
    const branch = prompt?.querySelector<HTMLElement>('[data-part="branch"]')
    expect(branch?.textContent).toBe('main')
  })

  it("remote: server icon, then PromptContext's host:path — never a branch (spec §3: remote is not walked for one)", () => {
    const el = createPaneContext({
      kind: 'remote',
      host: 'dev@staging',
      path: '/srv/nocx',
      branch: 'should-not-render',
    })
    expect(el.dataset.kind).toBe('remote')
    expect(el.querySelector('svg')).not.toBeNull()
    const prompt = el.querySelector<HTMLElement>('.ui-prompt-context')
    const host = prompt?.querySelector<HTMLElement>('[data-part="host"]')
    expect(host?.textContent).toBe('dev@staging')
    const path = prompt?.querySelector<HTMLElement>('[data-part="path"]')
    expect(path?.textContent).toBe('/srv/nocx')
    expect(prompt?.querySelector('[data-part="branch"]')).toBeNull()
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
    const prompt = el.querySelector<HTMLElement>('.ui-prompt-context')
    expect(prompt?.querySelector('[data-part="branch"]')).toBeNull()
    expect(prompt?.querySelector<HTMLElement>('[data-part="host"]')?.textContent).toBe(
      'dev@staging',
    )
  })
})
