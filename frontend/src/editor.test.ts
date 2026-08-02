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

  it('setCwd updates the cwd chip text', () => {
    const { ed, container } = setup()
    ed.show()
    ed.setCwd('/home/dev/projects')
    const chip = container.querySelector('.nocx-editor-cwd')
    expect(chip).not.toBeNull()
    expect(chip!.textContent).toContain('dev/projects')
  })

  it('Escape clears the textarea and resets rows, does not cancel (no shell interrupt)', () => {
    const { ed, ta, cancel } = setup()
    ed.show()
    ta.value = 'some draft'
    ta.rows = 2
    ta.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      }),
    )
    expect(ta.value).toBe('')
    expect(ta.rows).toBe(1)
    // Escape clears the draft only — it does NOT interrupt the shell.
    expect(cancel).not.toHaveBeenCalled()
  })

  it('rootContains returns true for elements inside the editor root', () => {
    const { ed, container, ta } = setup()
    ed.show()
    expect(ed.rootContains(ta)).toBe(true)
    expect(ed.rootContains(container.querySelector('.nocx-editor-cwd'))).toBe(true)
  })

  it('rootContains returns false for elements outside the editor root', () => {
    const { ed, container } = setup()
    ed.show()
    expect(ed.rootContains(document.body)).toBe(false)
    expect(ed.rootContains(container)).toBe(false) // container is the mount parent, not inside root
    expect(ed.rootContains(null)).toBe(false)
  })

  it('insertText inserts at the caret, replacing any selection', () => {
    const { ed, ta } = setup()
    ed.show()
    ta.value = 'echo XX'
    ta.selectionStart = 5
    ta.selectionEnd = 7 // select "XX"
    ed.insertText('hi')
    expect(ta.value).toBe('echo hi')
    expect(ta.selectionStart).toBe(7) // caret after the inserted text
  })
})

describe('alias hints', () => {
  const HINT_ITEMS = [
    { alias: 'prod-db', hostName: '10.0.0.1', user: 'deploy' },
    { alias: 'prod-web', hostName: 'web.example.com', port: 2222 },
    { alias: 'staging-db', hostName: 'staging.example.com' },
  ]

  const keyDown = (ta: HTMLTextAreaElement, key: string) =>
    ta.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))

  const input = (ta: HTMLTextAreaElement, value: string) => {
    ta.value = value
    ta.dispatchEvent(new Event('input', { bubbles: true }))
  }

  it('showAliasHints renders items; hideAliasHints clears them', () => {
    const { ed, container } = setup()
    const hintEl = container.querySelector('.nocx-editor-hint')!
    expect(hintEl).toBeTruthy()
    expect((hintEl as HTMLElement).style.display).toBe('none')

    ed.showAliasHints(HINT_ITEMS)
    expect((hintEl as HTMLElement).style.display).not.toBe('none')
    const items = container.querySelectorAll('.nocx-editor-hint__item')
    expect(items.length).toBe(3)

    ed.hideAliasHints()
    expect((hintEl as HTMLElement).style.display).toBe('none')
    expect(container.querySelectorAll('.nocx-editor-hint__item').length).toBe(0)
  })

  it('showAliasHints with empty list hides the dropdown', () => {
    const { ed, container } = setup()
    ed.show()
    ed.showAliasHints([])
    const hintEl = container.querySelector('.nocx-editor-hint') as HTMLElement
    expect(hintEl.style.display).toBe('none')
  })

  it('showAliasHints highlights first item by default', () => {
    const { ed, container } = setup()
    ed.show()
    ed.showAliasHints(HINT_ITEMS)
    const items = container.querySelectorAll('.nocx-editor-hint__item')
    expect(items[0].classList.contains('nocx-editor-hint__item--selected')).toBe(true)
    expect(items[1].classList.contains('nocx-editor-hint__item--selected')).toBe(false)
  })

  it('ArrowDown/ArrowUp navigates the hint list', () => {
    const { ed, ta, container } = setup()
    ed.show()
    ed.showAliasHints(HINT_ITEMS)

    const items = () => container.querySelectorAll('.nocx-editor-hint__item')

    expect(items()[0].classList.contains('nocx-editor-hint__item--selected')).toBe(true)

    keyDown(ta, 'ArrowDown')
    expect(items()[0].classList.contains('nocx-editor-hint__item--selected')).toBe(false)
    expect(items()[1].classList.contains('nocx-editor-hint__item--selected')).toBe(true)

    keyDown(ta, 'ArrowDown')
    expect(items()[0].classList.contains('nocx-editor-hint__item--selected')).toBe(false)
    expect(items()[2].classList.contains('nocx-editor-hint__item--selected')).toBe(true)

    // Wrap around
    keyDown(ta, 'ArrowDown')
    expect(items()[0].classList.contains('nocx-editor-hint__item--selected')).toBe(true)

    // Back up
    keyDown(ta, 'ArrowUp')
    expect(items()[2].classList.contains('nocx-editor-hint__item--selected')).toBe(true)
  })
  it('Enter on hint does NOT submit (atomic handoff preserved)', () => {
    const { ed, ta, submit } = setup()
    ed.show()
    input(ta, 'ssh prod')
    ed.showAliasHints(HINT_ITEMS)
    keyDown(ta, 'Enter')
    // The alias was accepted, not submitted as a command
    expect(submit).not.toHaveBeenCalled()
  })

  it('Enter on hint accepts the selected alias and replaces the ssh line', () => {
    const { ed, ta } = setup()
    ed.show()
    input(ta, 'ssh prod')
    ed.showAliasHints(HINT_ITEMS)
    keyDown(ta, 'Enter')
    expect(ta.value).toBe('ssh prod-db')
  })

  it('Escape dismisses hints without clearing the textarea', () => {
    const { ed, ta, container } = setup()
    ed.show()
    input(ta, 'ssh prod')
    ed.showAliasHints(HINT_ITEMS)
    const hintEl = container.querySelector('.nocx-editor-hint') as HTMLElement
    expect(hintEl.style.display).not.toBe('none')

    keyDown(ta, 'Escape')
    expect(hintEl.style.display).toBe('none')
    expect(ta.value).toBe('ssh prod') // textarea untouched
  })

  it('onInputChange fires on every input event with current value', () => {
    const onInputChange = vi.fn()
    const c = document.createElement('div')
    document.body.appendChild(c)
    const ed = new CommandEditor({ submit: vi.fn(), cancel: vi.fn(), onInputChange })
    ed.mount(c)
    ed.show()

    const ta = c.querySelector('textarea')!
    ta.value = 'hello'
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    expect(onInputChange).toHaveBeenCalledWith('hello')

    ta.value = 'ssh prod'
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    expect(onInputChange).toHaveBeenCalledWith('ssh prod')
    expect(onInputChange).toHaveBeenCalledTimes(2)
  })

  it('hints are hidden after hide() is called', () => {
    const { ed, container } = setup()
    ed.show()
    ed.showAliasHints(HINT_ITEMS)
    const hintEl = container.querySelector('.nocx-editor-hint') as HTMLElement
    expect(hintEl.style.display).not.toBe('none')
    ed.hide()
    expect(hintEl.style.display).toBe('none')
  })
})
