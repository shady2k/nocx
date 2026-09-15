// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { markShellCommand, SHELL_COMMAND_CLASS } from './shell-command'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const CSS = resolve(dirname, '../styles/components/shell-command.css')

/** The declaration block of the first rule whose selector LIST contains
 *  `selector` exactly (comma-separated grouped selectors count). */
function ruleFor(cssText: string, selector: string): string {
  const withoutComments = cssText.replace(/\/\*[\s\S]*?\*\//g, '')
  for (const block of withoutComments.split('}')) {
    const [head, body] = block.split('{')
    if (
      head
        ?.trim()
        .split(/\s*,\s*/)
        .includes(selector) &&
      body
    )
      return body
  }
  throw new Error(`no rule for ${selector}`)
}

describe('markShellCommand — marks the host, never the content', () => {
  it('adds the identity class to an arbitrary existing element', () => {
    const el = document.createElement('span')
    el.innerHTML = '<span class="tok-command">git</span> <span class="tok-path">diff</span>'
    markShellCommand(el)
    expect(el.classList.contains(SHELL_COMMAND_CLASS)).toBe(true)
    expect(el.classList.contains('ui-shell-command')).toBe(true)
    // Content is untouched — the helper never inserts, removes or re-tokenizes.
    expect(el.innerHTML).toBe(
      '<span class="tok-command">git</span> <span class="tok-path">diff</span>',
    )
  })

  it('is idempotent — calling it twice adds the class once', () => {
    const el = document.createElement('span')
    markShellCommand(el)
    markShellCommand(el)
    expect(el.className).toBe('ui-shell-command')
  })

  it("does not remove a caller's own classes", () => {
    const el = document.createElement('span')
    el.className = 'cmd-header-command'
    markShellCommand(el)
    expect(el.classList.contains('cmd-header-command')).toBe(true)
    expect(el.classList.contains('ui-shell-command')).toBe(true)
  })
})

describe('shell-command.css — the terminal-command palette, scoped under the identity', () => {
  const css = readFileSync(CSS, 'utf8')

  it('the executable (git) reads in accent', () => {
    expect(ruleFor(css, '.ui-shell-command .tok-command')).toContain('color: var(--color-accent)')
  })

  it('ordinary arguments, flags, atoms, strings and heredocs read as body text (diff --stat)', () => {
    const rule = ruleFor(css, '.ui-shell-command .tok-path')
    expect(rule).toContain('color: var(--color-text)')
  })

  it('operators are muted', () => {
    expect(ruleFor(css, '.ui-shell-command .tok-operator')).toContain(
      'color: var(--color-text-muted)',
    )
  })

  it('comments are dim', () => {
    expect(ruleFor(css, '.ui-shell-command .tok-comment')).toContain('color: var(--color-text-dim)')
  })

  it('variables and keywords read in the secondary accent', () => {
    const rule = ruleFor(css, '.ui-shell-command .tok-variable')
    expect(rule).toContain('color: var(--color-accent-secondary)')
  })

  it('an unresolved command keeps the ordinary text colour, not accent — the underline stays global', () => {
    expect(ruleFor(css, '.ui-shell-command .tok-command.tok-unresolved')).toContain(
      'color: var(--color-text)',
    )
  })
})
