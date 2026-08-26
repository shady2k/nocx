// @vitest-environment jsdom
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { grantBlockFromElement } from './ask-entry'
import { GrantController, type GrantBlock } from './grant'

const SRC_ROOT = resolve(import.meta.dirname ?? new URL('.', import.meta.url).pathname)
const SOURCE_EXTENSIONS: Record<string, true> = {
  '.js': true,
  '.jsx': true,
  '.ts': true,
  '.tsx': true,
  '.mjs': true,
  '.cjs': true,
}
const GRANT_LABEL = 'marked for the question'

function productionModules(dir: string): string[] {
  const modules: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = resolve(dir, entry.name)
    if (entry.isDirectory()) {
      modules.push(...productionModules(path))
      continue
    }
    if (
      SOURCE_EXTENSIONS[path.slice(path.lastIndexOf('.'))] === true &&
      !/(?:\.test|\.spec|\.bench)\./.test(entry.name)
    ) {
      modules.push(path)
    }
  }
  return modules
}

it('keeps the grant chip label in one production module', () => {
  const owners = productionModules(SRC_ROOT).filter((file) =>
    readFileSync(file, 'utf8').includes(GRANT_LABEL),
  )
  expect(owners).toEqual([resolve(SRC_ROOT, 'grant.ts')])
})

const block = (
  itemId: string,
  command: string,
  running = false,
  window?: { start: number; count: number },
): GrantBlock => {
  const blockEl = document.createElement('div')
  blockEl.className = running ? 'cmd-block cmd-block-running' : 'cmd-block'
  blockEl.dataset.entryId = itemId
  const header = document.createElement('span')
  header.className = 'cmd-header-text'
  header.textContent = command
  const output = document.createElement('div')
  output.className = 'cmd-output'
  for (const text of ['first', 'second', 'third']) {
    const row = document.createElement('span')
    row.className = 'term-line'
    row.textContent = text
    output.appendChild(row)
  }
  blockEl.append(header, output)
  document.body.appendChild(blockEl)
  return { itemId, blockEl, command, state: running ? 'running' : 'exited', ...window }
}

afterEach(() => {
  document.body.replaceChildren()
})

describe('GrantController', () => {
  it('fills a whole-block mark but paints only selected rows for a row mark', () => {
    const controller = new GrantController()
    const whole = block('whole', 'git status')
    const rows = block('rows', 'npm test', false, { start: 1, count: 1 })
    controller.setBlocks([whole])
    expect(whole.blockEl.dataset.granted).toBe('true')
    expect(whole.blockEl.querySelectorAll('.term-line[data-granted]')).toHaveLength(0)

    controller.setBlocks([rows])
    expect(rows.blockEl.dataset.granted).toBeUndefined()
    expect(rows.blockEl.querySelectorAll('.term-line[data-granted]')).toHaveLength(1)
    expect(rows.blockEl.querySelectorAll('.term-line[data-granted]')[0]?.textContent).toBe('second')
    expect(rows.blockEl.querySelectorAll('.cmd-block[data-granted]')).toHaveLength(0)
    controller.destroy()
  })

  it('does not paint a composer rail on either block or row marks', () => {
    const controller = new GrantController()
    const marked = block('item-rail', 'printf hi', false, { start: 0, count: 2 })
    controller.setBlocks([marked])

    expect(marked.blockEl.dataset.granted).toBeUndefined()
    expect(marked.blockEl.querySelector('.term-line[data-granted]')).not.toBeNull()
    expect(marked.blockEl.querySelector('.term-line[data-granted]')?.className).toBe('term-line')
    controller.destroy()
  })
  it('uses a visible fill without a left rail for block and row marks', () => {
    const css = readFileSync(resolve(SRC_ROOT, 'style.css'), 'utf8')
    expect(css).toMatch(
      /\.cmd-block\[data-granted\]\s*\{[^}]*background:\s*color-mix\(in srgb, var\(--color-accent\), transparent (?:8[0-9]|7[0-9])%\)/s,
    )
    expect(css).toMatch(/\.cmd-block \.term-line\[data-granted\]\s*\{[^}]*background:/s)
    expect(css).not.toMatch(/\.cmd-block\[data-granted\][^{]*\{[^}]*box-shadow:/s)
    expect(css).not.toMatch(/\.cmd-block\[data-granted\]::before/)
  })
  it('uses compact menu metrics for the grant panel variant', () => {
    const css = readFileSync(resolve(SRC_ROOT, 'styles/components/floating-panel.css'), 'utf8')
    expect(css).toMatch(
      /\.ui-floating-panel\[data-variant='grant'\]\s+\.ui-floating-panel__list\s*\{[^}]*padding:\s*0/s,
    )
    expect(css).toMatch(
      /\.ui-floating-panel\[data-variant='grant'\]\s+\.ui-floating-panel__row\s*\{[^}]*padding:\s*6px 12px/s,
    )
    expect(css).toMatch(
      /\.ui-floating-panel\[data-variant='grant'\]\s+\.ui-floating-panel__footer\s*\{[^}]*padding:\s*6px 12px/s,
    )
  })

  it('shows the default grant as a chip and changes it when a person marks a block', () => {
    const controller = new GrantController()
    controller.mount(document.body)
    const one = block('item-1', 'git status')

    expect(controller.chip.classList.contains('nocx-chip')).toBe(true)
    expect(controller.chip.dataset.state).toBe('default')
    expect(controller.chip.textContent).toContain('marked for the question')
    expect(controller.chip.textContent).toContain('0')

    controller.setBlocks([one])

    expect(controller.chip.dataset.state).toBe('chosen')
    expect(controller.chip.textContent).toContain('1')
    expect(one.blockEl.dataset.granted).toBe('true')
    controller.destroy()
  })
  it('does not open the panel when no blocks are marked', () => {
    const controller = new GrantController()
    controller.mount(document.body)

    controller.chip.click()

    expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
    expect(controller.chip.title).toBe('Mark blocks to include them in a question')
    controller.destroy()
  })

  it('closes an open panel when its last mark is removed', () => {
    const controller = new GrantController()
    const marked = block('removed-mark', 'git status')
    controller.setBlocks([marked])
    controller.mount(document.body)
    controller.chip.click()

    controller.setBlocks([])

    expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
    expect(controller.current).toEqual([])
    controller.destroy()
  })

  it('opens an ask mark with the question as its row label', () => {
    const controller = new GrantController()
    const answer = block('answer-1', 'what does this do?')
    answer.blockEl.dataset.blockKind = 'ask'
    answer.blockEl.dataset.recordedCommand = ''
    const marked = grantBlockFromElement(answer.blockEl)
    expect(marked?.command).toBe('what does this do?')

    controller.setBlocks(marked ? [marked] : [])
    controller.mount(document.body)
    controller.chip.click()

    const row = document.querySelector<HTMLElement>('.ui-floating-panel__row')
    expect(row?.textContent).toContain('what does this do?')
    expect(row?.textContent?.trim()).not.toBe('')
    controller.destroy()
  })

  it('opens an empty command mark with an explicit row label', () => {
    const controller = new GrantController()
    const empty = block('empty-1', '')
    empty.blockEl.dataset.recordedCommand = ''
    const marked = grantBlockFromElement(empty.blockEl)

    controller.setBlocks(marked ? [marked] : [])
    controller.mount(document.body)
    controller.chip.click()

    expect(document.querySelector<HTMLElement>('.ui-floating-panel__row')?.textContent).toContain(
      '(empty command)',
    )
    controller.destroy()
  })
  it('closes on Escape without clearing marks', () => {
    const controller = new GrantController()
    const marked = block('escape-mark', 'git status')
    controller.setBlocks([marked])
    controller.mount(document.body)
    controller.chip.click()

    const escape = new KeyboardEvent('keydown', {
      key: 'Escape',
      bubbles: true,
      cancelable: true,
    })
    document.dispatchEvent(escape)

    expect(escape.defaultPrevented).toBe(true)

    expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
    expect(controller.current).toEqual([marked])
    controller.destroy()
  })

  it('closes on an outside pointer press without clearing marks', () => {
    const controller = new GrantController()
    const marked = block('outside-mark', 'git status')
    controller.setBlocks([marked])
    controller.mount(document.body)
    controller.chip.click()

    document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))

    expect(document.querySelector('.ui-floating-panel[data-open="true"]')).toBeNull()
    expect(controller.current).toEqual([marked])
    controller.destroy()
  })

  it('opens a block list, dismisses one row, and dismisses all', () => {
    const onChange = vi.fn()
    const controller = new GrantController({ onChange })
    const first = block('item-1', 'git status')
    const second = block('item-2', 'npm test', true)
    controller.setBlocks([first, second])
    controller.mount(document.body)

    controller.chip.click()
    const panel = document.querySelector<HTMLElement>('.ui-floating-panel[data-variant="grant"]')
    expect(panel?.dataset.open).toBe('true')
    expect(panel?.textContent).toContain('git status')
    expect(panel?.textContent).toContain('npm test')

    const dismiss = panel?.querySelector<HTMLButtonElement>('[data-action="dismiss-grant"]')
    expect(dismiss).not.toBeNull()
    dismiss?.click()
    expect(onChange).toHaveBeenCalledWith([second])
    expect(first.blockEl.dataset.granted).toBeUndefined()
    expect(second.blockEl.dataset.granted).toBe('true')
    expect(panel?.textContent).not.toContain('git status')
    expect(panel?.textContent).toContain('npm test')

    panel?.querySelector<HTMLButtonElement>('[data-action="dismiss-all-grants"]')?.click()
    expect(onChange).toHaveBeenLastCalledWith([])
    expect(controller.chip.dataset.state).toBe('default')
    controller.destroy()
  })

  it('clicking a row scrolls to it and flashes the whole block mark', () => {
    const controller = new GrantController()
    controller.mount(document.body)
    const target = block('item-1', 'git status')
    const scrollIntoView = vi.fn()
    target.blockEl.scrollIntoView = scrollIntoView
    controller.setBlocks([target])

    controller.chip.click()
    const row = document.querySelector<HTMLElement>('.ui-floating-panel__row')
    row?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))

    expect(scrollIntoView).toHaveBeenCalled()
    expect(target.blockEl.classList.contains('cmd-block-grant-flash')).toBe(true)
    controller.destroy()
  })

  it('does not clear another pane’s mark when this pane changes grants', () => {
    const firstController = new GrantController()
    const secondController = new GrantController()
    firstController.mount(document.body)
    secondController.mount(document.body)
    const first = block('item-1', 'git status')
    const second = block('item-2', 'npm test')

    firstController.setBlocks([first])
    secondController.setBlocks([second])
    firstController.setBlocks([])

    expect(first.blockEl.dataset.granted).toBeUndefined()
    expect(second.blockEl.dataset.granted).toBe('true')
    firstController.destroy()
    secondController.destroy()
  })
})
