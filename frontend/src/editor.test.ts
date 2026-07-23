// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { CommandEditor } from './editor'

const setup = () => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const order: string[] = []
  const submit = vi.fn((doc: string) => order.push(`submit:${doc}`))
  const ed = new CommandEditor({ submit })
  ed.mount(container)
  const ta = container.querySelector('textarea')!
  return { ed, ta, submit, order, container }
}

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
    submit.mockImplementation((doc: string) =>
      order.push(`visible@submit:${ed.isVisible}|${doc}`),
    )
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
})
