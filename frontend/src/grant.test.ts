// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GrantController, type GrantBlock } from './grant'

const block = (itemId: string, command: string, running = false): GrantBlock => {
  const blockEl = document.createElement('div')
  blockEl.className = running ? 'cmd-block cmd-block-running' : 'cmd-block'
  blockEl.dataset.entryId = itemId
  const header = document.createElement('span')
  header.className = 'cmd-header-text'
  header.textContent = command
  blockEl.appendChild(header)
  document.body.appendChild(blockEl)
  return { itemId, blockEl, command, state: running ? 'running' : 'exited' }
}

afterEach(() => {
  document.body.replaceChildren()
})

describe('GrantController', () => {
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
