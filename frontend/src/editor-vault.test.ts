// @vitest-environment jsdom
//
// The vault seams CommandEditor gained for "secrets in the prompt":
//   - beforeSubmit: the async planning window BEFORE the atomic handoff —
//     the host resolves references there; a veto keeps the draft, a plan
//     sends the RESOLVED line while the ledger records the reference-intact
//     one. The plain path (no hook) must stay byte-identical to the old
//     contract — those cases live in editor.test.ts and pass unchanged.
//   - Ctrl+Shift+V: the vault-secret picker's chord.
//   - The standard editing keymap (defaultKeymap) — the baseline the chip's
//     atomicity needs; the capture listener still wins for Enter/Escape/Tab.
//
// No real timers: the verdict chain is microtasks, and a handful of awaits
// drains it deterministically (an await's own continuation interleaves with
// the chain, so one await is not enough).
import { describe, it, expect, vi } from 'vitest'
import { Extension } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { CommandEditor, type EditorActions } from './editor'

const viewOf = (ed: CommandEditor): EditorView => {
  const withView = ed as unknown as { view: EditorView }
  return withView.view
}

const setup = (actions: Partial<EditorActions> = {}, extensions: Extension[] = []) => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const submit = vi.fn()
  const cancel = vi.fn()
  const ed = new CommandEditor({ submit, cancel, ...actions }, extensions)
  ed.mount(container)
  const view = viewOf(ed)
  return { ed, view, submit, cancel, container }
}

const key = (view: EditorView, init: KeyboardEventInit) =>
  view.contentDOM.dispatchEvent(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )

const enter = (view: EditorView) => key(view, { key: 'Enter' })

/** Drain the verdict chain deterministically: the editor's submit chain is
 *  several microtasks deep (hook → commit → finally), and an await's own
 *  continuation interleaves with them, so a handful of awaits lets them all
 *  run. No real timers. */
const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

/** A gate the test opens after asserting the in-flight state. The executor
 *  form is required: Promise.withResolvers needs an ES2024 lib, and the
 *  project targets ES2021. */
function gate(): { promise: Promise<void>; release: () => void } {
  let release!: () => void
  const promise = new Promise<void>((resolve) => {
    release = resolve
  })
  return { promise, release }
}

describe('CommandEditor.beforeSubmit', () => {
  it('a veto (false) keeps the draft visible and submits nothing', async () => {
    const beforeSubmit = vi.fn(() => false as const)
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('curl {{secret:openai-key}}')
    enter(view)
    await flush()
    expect(beforeSubmit).toHaveBeenCalledWith('curl {{secret:openai-key}}')
    expect(submit).not.toHaveBeenCalled()
    expect(ed.isVisible).toBe(true)
    expect(ed.getDoc()).toBe('curl {{secret:openai-key}}')
  })

  it('a plan submits the RESOLVED line and hands the plan to the host', async () => {
    const plan = {
      sendLine: 'curl -H "Authorization: Bearer sk-live-abc"',
      recordLine: 'curl -H "Authorization: Bearer {{secret:openai-key}}"',
      refs: [{ name: 'openai-key', resolved: true }],
    }
    const beforeSubmit = vi.fn(() => plan)
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('curl -H "Authorization: Bearer {{secret:openai-key}}"')
    enter(view)
    await flush()
    expect(submit).toHaveBeenCalledWith('curl -H "Authorization: Bearer sk-live-abc"', plan)
    // The atomic handoff still holds: hidden and cleared.
    expect(ed.isVisible).toBe(false)
    expect(ed.getDoc()).toBe('')
  })

  it('an async plan keeps the atomic order — the handoff runs after the verdict', async () => {
    const g = gate()
    const beforeSubmit = vi.fn(async () => {
      await g.promise
      return { sendLine: 'echo resolved', recordLine: 'echo resolved', refs: [] }
    })
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('echo {{secret:x}}')
    enter(view)
    await flush()
    // In flight: the draft is still visible — nothing was cleared or hidden.
    expect(ed.isVisible).toBe(true)
    expect(ed.getDoc()).toBe('echo {{secret:x}}')
    expect(submit).not.toHaveBeenCalled()
    g.release()
    await flush()
    await flush()
    expect(submit).toHaveBeenCalledWith('echo resolved', expect.anything())
    expect(ed.isVisible).toBe(false)
    expect(ed.getDoc()).toBe('')
  })

  // nocx-evj4b.1.6: the host may TAKE a line and deliver it later (a submit
  // held while an ssh session proves its shell's integration). That verdict is
  // neither a veto (which keeps the draft for the person) nor a plan (which
  // sends now): the line left the composer, so the draft empties and the next
  // Enter starts a second line. Without it every Enter after the first was
  // swallowed as a duplicate and only one concatenated line ever ran.
  it('a held verdict empties the draft and lets the next Enter hold its own line', async () => {
    const held: string[] = []
    const beforeSubmit = vi.fn((doc: string) => {
      held.push(doc)
      return 'held' as const
    })
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('first')
    enter(view)
    await flush()
    expect(held).toEqual(['first'])
    expect(ed.getDoc()).toBe('')
    expect(ed.isVisible).toBe(true)
    expect(submit).not.toHaveBeenCalled()

    ed.insertText('second')
    enter(view)
    await flush()
    expect(held).toEqual(['first', 'second'])
    expect(ed.getDoc()).toBe('')
    expect(submit).not.toHaveBeenCalled()
  })

  it("an async plan's held verdict also empties the draft and submits nothing", async () => {
    const beforeSubmit = vi.fn(() => Promise.resolve('held' as const))
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('echo {{secret:x}}')
    enter(view)
    await flush()
    expect(beforeSubmit).toHaveBeenCalledTimes(1)
    expect(submit).not.toHaveBeenCalled()
    expect(ed.isVisible).toBe(true)
    expect(ed.getDoc()).toBe('')
  })

  it('a second Enter while the verdict is in flight is swallowed', async () => {
    const g = gate()
    const beforeSubmit = vi.fn(async () => {
      await g.promise
      return { sendLine: 'x', recordLine: 'x', refs: [] }
    })
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('echo {{secret:x}}')
    enter(view)
    await flush()
    enter(view) // second Enter during flight
    await flush()
    g.release()
    await flush()
    await flush()
    expect(beforeSubmit).toHaveBeenCalledTimes(1)
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it('an edit to the draft during flight drops the stale plan', async () => {
    const g = gate()
    const beforeSubmit = vi.fn(async () => {
      await g.promise
      return { sendLine: 'stale', recordLine: 'stale', refs: [] }
    })
    const { ed, view, submit } = setup({ beforeSubmit })
    ed.show()
    ed.insertText('echo {{secret:x}}')
    enter(view)
    await flush()
    ed.insertText('!') // the draft moved on
    g.release()
    await flush()
    await flush()
    expect(submit).not.toHaveBeenCalled()
    // The user's new text is the draft.
    expect(ed.getDoc()).toBe('echo {{secret:x}}!')
  })

  it('a throwing planner keeps the draft and never corrupts the editor', async () => {
    const swallowWindowError = () => {}
    window.addEventListener('error', swallowWindowError)
    try {
      const beforeSubmit = vi
        .fn()
        .mockRejectedValueOnce(new Error('planner bug'))
        .mockResolvedValueOnce({ sendLine: 'echo hi!', recordLine: 'echo hi!', refs: [] })
      const { ed, view, submit } = setup({ beforeSubmit })
      ed.show()
      ed.insertText('echo hi')
      enter(view)
      await flush()
      await flush()
      expect(submit).not.toHaveBeenCalled()
      expect(ed.isVisible).toBe(true)
      expect(ed.getDoc()).toBe('echo hi')
      // The editor still works after the failure — the second Enter resolves.
      ed.insertText('!')
      enter(view)
      await flush()
      await flush()
      expect(submit).toHaveBeenCalledWith('echo hi!', expect.anything())
    } finally {
      window.removeEventListener('error', swallowWindowError)
    }
  })
})

describe('CommandEditor: the @ trigger (the reference picker)', () => {
  it('@ after whitespace fires onSecretPicker with the trigger position', () => {
    const onSecretPicker = vi.fn()
    const { ed, view } = setup({ onSecretPicker })
    ed.show()
    ed.insertText('echo ') // the caret sits right after the space
    key(view, { key: '@', shiftKey: true })
    expect(onSecretPicker).toHaveBeenCalledTimes(1)
    expect(onSecretPicker).toHaveBeenCalledWith(5)
  })

  it('@ at line start fires onSecretPicker', () => {
    const onSecretPicker = vi.fn()
    const { ed, view } = setup({ onSecretPicker })
    ed.show()
    key(view, { key: '@', shiftKey: true })
    expect(onSecretPicker).toHaveBeenCalledWith(0)
  })

  it('@ inside a word never fires — user@host, emails, git@github.com', () => {
    const onSecretPicker = vi.fn()
    const { ed, view } = setup({ onSecretPicker })
    ed.show()
    ed.insertText('git clone ssh://dev@github.com/o/r')
    view.dispatch({ selection: { anchor: ed.getDoc().length } })
    key(view, { key: '@', shiftKey: true })
    expect(onSecretPicker).not.toHaveBeenCalled()
  })

  it('the @ itself is not consumed — it lands in the document', () => {
    const onSecretPicker = vi.fn()
    const { ed, view } = setup({ onSecretPicker })
    ed.show()
    ed.insertText('echo ')
    // Not canceled: dispatchEvent resolves true, so the browser would insert
    // the '@' (jsdom has no native contenteditable input to model that). The
    // picker replaces the trigger word when the user picks; Esc leaves the
    // literal '@' the user typed.
    expect(key(view, { key: '@', shiftKey: true })).toBe(true)
    expect(onSecretPicker).toHaveBeenCalledTimes(1)
  })
  it('typing more after @ is ordinary text — the picker filters, never traps', () => {
    const onSecretPicker = vi.fn()
    const { ed, view } = setup({ onSecretPicker })
    ed.show()
    ed.insertText('echo ')
    key(view, { key: '@', shiftKey: true })
    expect(onSecretPicker).toHaveBeenCalledTimes(1)
    // The next keystrokes continue into the LINE at the caret — not into a
    // captured filter. The picker watches the document and filters by the
    // trigger word's continuation.
    ed.insertText('ope')
    expect(ed.getDoc()).toBe('echo ope')
  })
})

describe('CommandEditor: nested form controls own their keys', () => {
  it('Enter inside the offer name field never submits the draft', () => {
    const { ed, submit, container } = setup()
    ed.show()
    ed.insertText('curl https://x')
    const input = document.createElement('input')
    input.className = 'nested-control-input'
    container.appendChild(input)
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }),
    )
    expect(submit).not.toHaveBeenCalled()
    expect(ed.getDoc()).toBe('curl https://x')
  })

  it('Escape inside a nested control does not clear the draft', () => {
    const { ed, container } = setup()
    ed.show()
    ed.insertText('curl https://x')
    const input = document.createElement('input')
    container.appendChild(input)
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    expect(ed.getDoc()).toBe('curl https://x')
  })
})

describe('CommandEditor: the standard editing keymap is the production baseline', () => {
  it('Backspace and arrow keys work WITHOUT any caller-installed keymap', () => {
    // The chip's atomicity ("the caret steps over it as one unit and
    // Backspace removes the whole reference") is a real-user behavior; this
    // pins the baseline the editor now installs itself.
    const { ed, view } = setup()
    ed.show()
    ed.insertText('abc')
    view.dispatch({ selection: { anchor: 3 } })
    key(view, { key: 'Backspace' })
    expect(ed.getDoc()).toBe('ab')
    view.dispatch({ selection: { anchor: 2 } })
    key(view, { key: 'ArrowLeft' })
    expect(view.state.selection.main.head).toBe(1)
    key(view, { key: 'Backspace' })
    expect(ed.getDoc()).toBe('b')
  })
})
