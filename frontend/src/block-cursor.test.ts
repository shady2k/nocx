// @vitest-environment jsdom
// The composer's cursor, asserted where a person sees it: in the rendered DOM
// of a mounted CommandEditor, not in the decoration set that produced it.
//
// ONE HALF OF THE CURSOR IS ASSERTED HERE AND THE OTHER CANNOT BE. The block
// over a character is a decoration, so it is in the DOM and these cases read
// it. The block at the end of a line is CM6's own cursor element, and CM6
// places that by measuring coordinates — jsdom computes no layout, so the
// cursor layer renders empty here whatever the code does. Asserting its
// absence would therefore pass against any implementation, including a broken
// one, so this file asserts only that no MARK is drawn there and says out
// loud where the rest is checked: e2e/command-editor.spec.ts, in a browser.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { EditorView } from '@codemirror/view'
import { CommandEditor } from './editor'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import {
  BLOCK_CURSOR_MARK,
  CARET_IDLE_MS,
  CARET_PHASE_ATTR,
  CURSOR_BLINK_MS,
  caretPhase,
} from './block-cursor'
import { cursorCharLeft } from '@codemirror/commands'

const viewOf = (ed: CommandEditor): EditorView => (ed as unknown as { view: EditorView }).view

/** The character the block is painted on, or null if no block is drawn. */
const blockOn = (view: EditorView): string | null =>
  view.contentDOM.querySelector(`.${BLOCK_CURSOR_MARK}`)?.textContent ?? null

describe('the composer draws the terminal block cursor', () => {
  let teardown: (() => void)[] = []

  beforeEach(() => {
    teardown = []
  })

  afterEach(() => {
    for (const t of teardown) t()
    document.body.innerHTML = ''
  })

  const mount = () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const ed = new CommandEditor({ submit: vi.fn(), cancel: vi.fn() })
    ed.mount(container)
    ed.show() // focuses the input, which is the state a prompt is in
    teardown.push(() => container.remove())
    return { ed, view: viewOf(ed) }
  }

  it('paints the character the caret stands on, so the character is still read', () => {
    const { ed, view } = mount()
    ed.replaceDoc('echo hello')
    view.dispatch({ selection: { anchor: 6 } }) // on the 'e' of hello

    expect(blockOn(view)).toBe('e')
  })

  it('moves with the caret', () => {
    const { ed, view } = mount()
    ed.replaceDoc('echo hello')

    view.dispatch({ selection: { anchor: 0 } })
    expect(blockOn(view)).toBe('e') // 'echo'
    view.dispatch({ selection: { anchor: 4 } })
    expect(blockOn(view)).toBe(' ')
  })

  it('marks no character at the end of a line, where the cursor element is the block instead', () => {
    const { ed, view } = mount()
    ed.replaceDoc('echo hello')
    view.dispatch({ selection: { anchor: 10 } })

    expect(blockOn(view)).toBeNull()
  })

  it('marks no character on an empty document either — the state the prompt starts in', () => {
    const { view } = mount()

    expect(view.state.doc.length).toBe(0)
    expect(blockOn(view)).toBeNull()
  })

  it('with a selection up, the block marks the head — the end the keyboard moves', () => {
    const { ed, view } = mount()
    ed.replaceDoc('echo hello')
    view.dispatch({ selection: { anchor: 9, head: 5 } })

    expect(blockOn(view)).toBe('h')
  })

  it('the mark blinks on the period the other half was given, not on a copy of it', () => {
    // The two halves blink by two mechanisms that cannot see each other — a
    // CSS animation here, CM6's inline `animation-name` there — so the number
    // has one declaration (CURSOR_BLINK_MS) handed to both. A period that
    // drifted from it would show as a cursor changing its rhythm as it steps
    // onto a character.
    //
    // Read from the stylesheet because jsdom loads no CSS and computes no
    // animation: the file is the only place this fact exists.
    const css = readFileSync(
      resolve(import.meta.dirname ?? '.', 'styles/surfaces/composer.css'),
      'utf8',
    )
    // `[^{]*` accepts the rest of the selector list before the brace: the
    // departure ghost (nocx-1ebrs.40) is listed beside the composer in one
    // rule, and a selector list applies the body to every name in it, so the
    // declaration check below still guards the composer's cursor exactly.
    const rule = /\.nocx-editor \.nocx-block-cursor[^{]*\{[^}]*animation:([^;]*);/.exec(css)
    expect(rule).not.toBeNull()
    // The period comes from the property, and the fallback beside it — which
    // the CSS-integrity gate requires of a property set from outside the
    // cascade — is the same number written a second time. Checked, not
    // trusted: this is what makes the duplicate safe.
    const fallback = /var\(--nocx-cursor-blink,\s*(\d+)ms\)/.exec(rule![1])
    expect(fallback).not.toBeNull()
    expect(Number(fallback![1])).toBe(CURSOR_BLINK_MS)
  })

  it('a pane that is not focused shows no cursor at all, the way an inactive terminal does', async () => {
    const { ed, view } = mount()
    ed.replaceDoc('echo hello')
    view.dispatch({ selection: { anchor: 6 } })
    expect(blockOn(view)).toBe('e')

    view.contentDOM.blur()

    // Waited on the state, never on a clock: focus reaches the view through a
    // notification CM6 delivers itself, so the assertion is "the surface
    // arrives in this state", not "this many milliseconds later".
    await vi.waitFor(() => expect(blockOn(view)).toBeNull())
  })
})

describe('the caret holds still while keys are arriving', () => {
  // The standard, not a preference: macOS text fields, VS Code and iTerm all
  // keep the caret SOLID while input arrives and let the blink resume on a
  // pause. A caret caught mid-off by a key looks like the key was dropped.
  //
  // The phase is asserted through the module's own answer (`caretPhase`),
  // never through a class name: what the CSS does with it is a stylesheet
  // fact and is checked as one, below.
  let teardown: (() => void)[] = []

  beforeEach(() => {
    teardown = []
  })

  afterEach(() => {
    for (const t of teardown) t()
    document.body.innerHTML = ''
  })

  const mount = (): { ed: CommandEditor; view: EditorView } => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const ed = new CommandEditor({ submit: vi.fn(), cancel: vi.fn() })
    ed.mount(container)
    ed.show()
    // Only the idle timer is faked. CM6 schedules its own measuring on
    // requestAnimationFrame and the clock stays real, so nothing but the
    // caret's own idle wait is under this file's control.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    teardown.push(() => {
      vi.useRealTimers()
      container.remove()
    })
    return { ed, view: viewOf(ed) }
  }

  /** A prompt that has been quiet long enough to be blinking: the baseline. */
  const quiet = (ed: CommandEditor, view: EditorView): void => {
    ed.replaceDoc('echo hello')
    view.dispatch({ selection: { anchor: 6 } })
    vi.advanceTimersByTime(CARET_IDLE_MS)
    expect(caretPhase(view)).toBe('blinking')
  }

  it('a typed character leaves the caret solid, and the blink resumes after the idle bound', () => {
    const { ed, view } = mount()
    quiet(ed, view)

    // The shape CM6's own input handling produces for a key: a change AND the
    // selection that follows it.
    view.dispatch(view.state.replaceSelection('x'))

    expect(caretPhase(view)).toBe('solid')
    // The blink must not come back WHILE keys are arriving: one millisecond
    // inside the idle bound it is still solid.
    vi.advanceTimersByTime(CARET_IDLE_MS - 1)
    expect(caretPhase(view)).toBe('solid')
    vi.advanceTimersByTime(1)
    expect(caretPhase(view)).toBe('blinking')
  })

  it('a key that only moves the caret is input too, and restarts the idle wait', () => {
    // The decision this file records: an arrow, Home/End and a shift-selection
    // count exactly as a character does. They are what a person presses while
    // LOOKING at the caret, which is the moment a mid-off blink misleads most,
    // and the standard treats them as input.
    const { ed, view } = mount()
    quiet(ed, view)

    cursorCharLeft(view)
    expect(caretPhase(view)).toBe('solid')

    view.dispatch({ selection: { anchor: 2, head: 5 } })
    expect(caretPhase(view)).toBe('solid')
    vi.advanceTimersByTime(CARET_IDLE_MS)
    expect(caretPhase(view)).toBe('blinking')
  })

  it('stays solid for as long as keys keep arriving, however long the typing lasts', () => {
    const { ed, view } = mount()
    quiet(ed, view)

    // Four keys, each inside the idle bound of the one before: a person typing
    // continuously must never see the caret flicker between them.
    for (const ch of 'abcd') {
      view.dispatch(view.state.replaceSelection(ch))
      vi.advanceTimersByTime(CARET_IDLE_MS - 100)
      expect(caretPhase(view)).toBe('solid')
    }
    vi.advanceTimersByTime(CARET_IDLE_MS)
    expect(caretPhase(view)).toBe('blinking')
  })

  it('both halves of the cursor read that phase, so neither blinks on its own clock', () => {
    // The two halves are drawn by two mechanisms — a decoration's CSS
    // animation and CM6's own cursor layer, whose animation name is written
    // INLINE and so outranks any ordinary rule. jsdom runs no animation, so
    // the stylesheet is the only place this fact exists (the same reason the
    // period is read from it above).
    const css = readFileSync(
      resolve(import.meta.dirname ?? '.', 'styles/surfaces/composer.css'),
      'utf8',
    )
    // The attribute name comes from the module, so a rename on the TypeScript
    // side fails here instead of silently un-suppressing the blink in a
    // browser that no test is watching.
    const solid =
      String.raw`\.nocx-editor \.cm-editor\[` + CARET_PHASE_ATTR + String.raw`='solid'\] `
    // The same selector-list acceptance as the period test above: the ghost
    // is listed beside the composer, and the body reaches both.
    expect(
      new RegExp(solid + String.raw`\.nocx-block-cursor[^{]*\{[^}]*animation:\s*none`).test(css),
    ).toBe(true)
    expect(
      new RegExp(
        solid + String.raw`\.cm-cursorLayer[^{]*\{[^}]*animation-name:\s*none !important`,
      ).test(css),
    ).toBe(true)
  })
})
