// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { CommandEditor } from './editor'

const setup = () => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const order: string[] = []
  const submit = vi.fn((doc: string) => order.push(`submit:${doc}`))
  const cancel = vi.fn(() => order.push('cancel'))
  const ed = new CommandEditor({ submit, cancel })
  ed.mount(container)
  const ta = container.querySelector('textarea')!
  return { ed, ta, submit, cancel, order, container }
}

const ctrlC = (ta: HTMLTextAreaElement) =>
  ta.dispatchEvent(
    new KeyboardEvent('keydown', {
      key: 'c',
      ctrlKey: true,
      bubbles: true,
      cancelable: true,
    }),
  )

const enter = (ta: HTMLTextAreaElement, shift = false) =>
  ta.dispatchEvent(
    new KeyboardEvent('keydown', {
      key: 'Enter',
      shiftKey: shift,
      bubbles: true,
      cancelable: true,
    }),
  )

describe('CommandEditor', () => {
  it('Enter hides+clears before submit (atomic handoff)', () => {
    const { ed, ta, submit, order } = setup()
    ed.show()
    ta.value = 'echo hi'
    // record visibility at submit time via a spy
    submit.mockImplementation((doc: string) => order.push(`visible@submit:${ed.isVisible}|${doc}`))
    enter(ta)
    expect(submit).toHaveBeenCalledWith('echo hi')
    expect(order[0]).toBe('visible@submit:false|echo hi') // hidden BEFORE submit
    expect(ta.value).toBe('')
  })

  it('Shift+Enter does not submit', () => {
    const { ed, ta, submit } = setup()
    ed.show()
    ta.value = 'x'
    enter(ta, true)
    expect(submit).not.toHaveBeenCalled()
  })

  it('starts hidden; show/hide toggle isVisible', () => {
    const { ed } = setup()
    expect(ed.isVisible).toBe(false)
    ed.show()
    expect(ed.isVisible).toBe(true)
    ed.hide()
    expect(ed.isVisible).toBe(false)
  })

  it('Ctrl-C with no selection clears and cancels (interrupt)', () => {
    const { ed, ta, cancel, submit } = setup()
    ed.show()
    ta.value = 'echo partial'
    ta.selectionStart = ta.selectionEnd = ta.value.length
    ctrlC(ta)
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(submit).not.toHaveBeenCalled()
    expect(ta.value).toBe('')
  })

  it('Ctrl-C with a selection is left alone so copy still works', () => {
    const { ed, ta, cancel } = setup()
    ed.show()
    ta.value = 'echo hi'
    ta.selectionStart = 0
    ta.selectionEnd = ta.value.length
    ctrlC(ta)
    expect(cancel).not.toHaveBeenCalled()
    expect(ta.value).toBe('echo hi')
  })

  it('applies the nocx-editor-input class (mono font via CSS)', () => {
    const { ta } = setup()
    expect(ta.className).toContain('nocx-editor-input')
  })

  it('multiline: grows rows as lines are added, resets to 1 on submit', () => {
    const { ed, ta } = setup()
    ed.show()
    // 3 lines → rows should be 3
    ta.value = 'line1\nline2\nline3'
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    expect(ta.rows).toBe(3)

    // submit resets to 1
    ta.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Enter',
        shiftKey: false,
        bubbles: true,
        cancelable: true,
      }),
    )
    expect(ta.rows).toBe(1)
  })

  it('multiline: caps rows at MAX_ROWS (10)', () => {
    const { ed, ta } = setup()
    ed.show()
    ta.value = Array(15).fill('line').join('\n') // 15 lines
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    expect(ta.rows).toBe(10)
  })

  it('clicking the submit button submits and clears (same as Enter)', () => {
    const { ed, container, submit } = setup()
    ed.show()
    const ta = container.querySelector('textarea')!
    ta.value = 'echo hi'

    const btn = container.querySelector('.nocx-editor-submit') as HTMLButtonElement
    expect(btn).not.toBeNull()
    btn.click()

    expect(submit).toHaveBeenCalledWith('echo hi')
    expect(ta.value).toBe('')
    expect(ed.isVisible).toBe(false)
  })

  it('setCwd updates the cwd chip text', () => {
    const { ed, container } = setup()
    ed.show()
    ed.setCwd('/home/dev/projects')
    const chip = container.querySelector('.nocx-editor-cwd')
    expect(chip).not.toBeNull()
    expect(chip!.textContent).toContain('dev/projects')
  })
})
