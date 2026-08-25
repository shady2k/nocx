// @vitest-environment jsdom
//
// The typed internal-command seam (design §5.1): `internalCommand` runs
// BEFORE secret resolution and the atomic handoff, and its CLOSED outcome
// decides the draft and whether anything reaches the PTY/history/ledger.
import { describe, it, expect, vi } from 'vitest'
import { Extension } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { CommandEditor, type EditorActions } from './editor'
import type { InternalCommandOutcome } from './sandbox-command'

const viewOf = (ed: CommandEditor): EditorView => {
  const withView = ed as unknown as { view: EditorView }
  return withView.view
}

const setup = (actions: Partial<EditorActions> = {}) => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const submit = vi.fn()
  const cancel = vi.fn()
  const onDocCleared = vi.fn()
  const ed = new CommandEditor({ submit, cancel, onDocCleared, ...actions }, [] as Extension[])
  ed.mount(container)
  const view = viewOf(ed)
  return { ed, view, submit, cancel, onDocCleared, container }
}

const key = (view: EditorView, init: KeyboardEventInit) =>
  view.contentDOM.dispatchEvent(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )

const enter = (view: EditorView) => key(view, { key: 'Enter' })

const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

describe('CommandEditor.internalCommand', () => {
  it('notHandled falls through to the ordinary submit', async () => {
    const internalCommand = vi.fn((): InternalCommandOutcome => ({ kind: 'notHandled' }))
    const { ed, view, submit } = setup({ internalCommand })
    ed.show()
    ed.insertText('echo hi')
    enter(view)
    await flush()
    expect(internalCommand).toHaveBeenCalledWith('echo hi')
    expect(submit).toHaveBeenCalledWith('echo hi')
    expect(ed.getDoc()).toBe('')
  })

  it('consumed clears the draft and sends nothing to the PTY', async () => {
    const internalCommand = vi.fn((): InternalCommandOutcome => ({ kind: 'consumed' }))
    const { ed, view, submit, onDocCleared } = setup({ internalCommand })
    ed.show()
    ed.insertText('/sandbox')
    enter(view)
    await flush()
    expect(internalCommand).toHaveBeenCalledWith('/sandbox')
    expect(submit).not.toHaveBeenCalled()
    expect(ed.getDoc()).toBe('')
    expect(onDocCleared).toHaveBeenCalledTimes(1)
  })

  it('refused keeps the draft and sends nothing to the PTY', async () => {
    const internalCommand = vi.fn((): InternalCommandOutcome => ({
      kind: 'refused',
      reason: 'Wait for the shell',
    }))
    const { ed, view, submit, onDocCleared } = setup({ internalCommand })
    ed.show()
    ed.insertText('/sandbox')
    enter(view)
    await flush()
    expect(submit).not.toHaveBeenCalled()
    expect(ed.getDoc()).toBe('/sandbox')
    expect(onDocCleared).not.toHaveBeenCalled()
  })

  it('recognition runs before secret resolution (beforeSubmit)', async () => {
    const order: string[] = []
    const internalCommand = vi.fn((): InternalCommandOutcome => {
      order.push('internal')
      return { kind: 'consumed' }
    })
    const beforeSubmit = vi.fn(() => {
      order.push('beforeSubmit')
      return false as const
    })
    const { ed, view, submit } = setup({ internalCommand, beforeSubmit })
    ed.show()
    ed.insertText('/sandbox')
    enter(view)
    await flush()
    expect(order).toEqual(['internal'])
    expect(beforeSubmit).not.toHaveBeenCalled()
    expect(submit).not.toHaveBeenCalled()
  })
})
