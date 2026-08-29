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
import { BLOCK_CURSOR_MARK, CURSOR_BLINK_MS } from './block-cursor'

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
    const css = readFileSync(resolve(import.meta.dirname ?? '.', 'style.css'), 'utf8')
    const rule = /\.nocx-editor \.nocx-block-cursor \{[^}]*animation:([^;]*);/.exec(css)
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
