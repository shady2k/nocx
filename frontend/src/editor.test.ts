// @vitest-environment jsdom
//
// CommandEditor tests, rewritten for the CodeMirror 6 engine (ADR-0010, W1).
// No querySelector('textarea'), no .value/.rows/.selectionStart pokes: the
// suite asserts through the public API and observable behaviour.
//
// Honesty about the one seam: jsdom performs no layout and no native
// contenteditable editing, so a real keystroke/selection gesture is
// impossible here. Tests therefore seed selections through the CM6 view
// (`viewOf` — the same transaction a mouse drag produces) and dispatch
// keydown/mouseup on the view's contentDOM (where real events land). Almost
// every outcome is then observed through the public callbacks — submit,
// cancel, onInputChange, resized, focus,
// visibility. The document is read back directly in exactly three places
// where no public channel exists and the assertion is state integrity
// (cleared after a throwing submit; untouched by a no-op Ctrl-C).
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest'
import { Extension } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { defaultKeymap } from '@codemirror/commands'
import { CommandEditor, stripPastedIndent, type EditorActions } from './editor'
import { shellExtensions, highlightShellText, shellHighlightReady } from './shell-highlight'
import { CommandSnapshotStore } from './command-snapshot'
import { CompletionController } from './suggest/controller'
import { CompletionDropdown } from './ui/completion-dropdown'
import { commandWord, type SuggestionProvider } from './suggest/providers'
import type { Candidate } from './suggest/candidate'

/**
 * The editor's internal CM6 view. CommandEditor keeps it private; tests
 * reach it only to seed selections and to read the document where no public
 * channel exists (see file header).
 */
const viewOf = (ed: CommandEditor): EditorView => {
  const withView = ed as unknown as { view: EditorView }
  return withView.view
}

const setup = (actions: Partial<EditorActions> = {}, extensions: Extension[] = []) => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const order: string[] = []
  const submit = vi.fn((doc: string) => order.push(`submit:${doc}`))
  const cancel = vi.fn(() => {
    order.push('cancel')
    return true
  })
  const ed = new CommandEditor({ submit, cancel, ...actions }, extensions)
  ed.mount(container)
  const view = viewOf(ed)
  return { ed, view, submit, cancel, order, container }
}

/** Dispatch a keydown exactly where a user's keystroke lands. */
const key = (view: EditorView, init: KeyboardEventInit) =>
  view.contentDOM.dispatchEvent(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )

const enter = (view: EditorView, shift = false) => key(view, { key: 'Enter', shiftKey: shift })

const ctrlC = (view: EditorView) => key(view, { key: 'c', ctrlKey: true })

const escape = (view: EditorView) => key(view, { key: 'Escape' })

/** Seed a selection the way a mouse drag would (gesture stand-in). */
const select = (view: EditorView, anchor: number, head: number) =>
  view.dispatch({ selection: { anchor, head } })

describe('CommandEditor', () => {
  it('starts hidden; show/hide toggle isVisible', () => {
    const { ed } = setup()
    expect(ed.isVisible).toBe(false)
    ed.show()
    expect(ed.isVisible).toBe(true)
    ed.hide()
    expect(ed.isVisible).toBe(false)
  })

  it('Enter hides+clears before submit (atomic handoff)', () => {
    const { ed, view, submit, order } = setup()
    ed.show()
    ed.insertText('echo hi')
    submit.mockImplementation((d: string) => order.push(`visible@submit:${ed.isVisible}|${d}`))
    enter(view)
    expect(submit).toHaveBeenCalledWith('echo hi')
    expect(order[0]).toBe('visible@submit:false|echo hi') // hidden BEFORE submit

    // The clear half of the handoff, observed publicly: the next prompt shows
    // an empty editor — had 'echo hi' survived, this submit would carry it.
    ed.show()
    ed.insertText('fresh')
    enter(view)
    expect(submit).toHaveBeenLastCalledWith('fresh')
  })

  // An empty prompt is not a command. CommandLedger.open refuses an empty
  // string by throwing, and commit() clears and hides BEFORE it calls submit
  // — so an unguarded empty Enter threw out of the keydown with the editor
  // already hidden, and nothing ever showed it again: the prompt was gone for
  // the rest of the session (nocx-axqs, seen in CI as nocxify-journey's editor
  // never returning). The two ends are asserted, not just the first: no submit
  // AND the prompt is still there and still works.
  it('Enter on an empty prompt is not a submit, and the prompt survives it', () => {
    const { ed, view, submit } = setup()
    ed.show()
    enter(view)
    expect(submit).not.toHaveBeenCalled()
    expect(ed.isVisible).toBe(true)
    ed.insertText('echo hi')
    enter(view)
    expect(submit).toHaveBeenCalledWith('echo hi')
  })

  // Whitespace alone is the same non-command: the ledger would accept it
  // (it is truthy) and open a block for a command the user never typed.
  // A LEADING space is not this case — ` ls` is a real command, and the
  // trim decides only whether to submit, never what is sent.
  it('a whitespace-only draft is not a command either', () => {
    const { ed, view, submit } = setup()
    ed.show()
    ed.insertText('   ')
    enter(view)
    expect(submit).not.toHaveBeenCalled()
    expect(ed.isVisible).toBe(true)
  })

  // THE COMPOSER'S HALF OF nocx-1ebrs.32, and the reason the defect was never
  // here: a browser reports the keypad's Enter as key "Enter" with code
  // "NumpadEnter" (measured in Blink and WebKit), so this handler's
  // `e.key === 'Enter'` fires for it exactly as it does for Return. The test
  // states that as a result a person can reach — a draft submitted — rather
  // than as the condition the code happens to test, so a future rewrite of the
  // condition cannot quietly keep the two keys apart.
  it('submits on the numeric keypad’s Enter, exactly as Return does', () => {
    const { ed, view, submit } = setup()
    ed.show()
    ed.insertText('echo hi')
    key(view, { key: 'Enter', code: 'NumpadEnter' })
    expect(submit).toHaveBeenCalledWith('echo hi')
    expect(ed.isVisible).toBe(false)
  })

  it('submit receives the composed document byte-identical', () => {
    const { ed, view, submit } = setup()
    ed.show()
    ed.insertText('echo "a\tb"  &&\nprintf ok')
    enter(view)
    expect(submit).toHaveBeenCalledWith('echo "a\tb"  &&\nprintf ok')
  })

  // Tab is completion's key, and completion is not built yet. What it must
  // NOT be meanwhile is the browser's focus-move: measured in a real browser
  // on 2026-08-02, pressing Tab at the prompt took document.activeElement
  // from .cm-content to nothing, so the next keystroke went nowhere. The
  // keydown is cancelled and the document is untouched — pressing Tab does
  // nothing, visibly, instead of doing something invisible and wrong.
  it('Tab is swallowed at the prompt rather than moving the focus away', () => {
    const { ed, view } = setup()
    ed.show()
    ed.insertText('ec')
    const delivered = key(view, { key: 'Tab' })
    expect(delivered).toBe(false) // preventDefault: no browser focus move
    expect(view.state.doc.toString()).toBe('ec')
  })

  it('Ctrl/Cmd+Tab is left alone — it belongs to the window, not the prompt', () => {
    const { ed, view } = setup()
    ed.show()
    expect(key(view, { key: 'Tab', ctrlKey: true })).toBe(true)
    expect(key(view, { key: 'Tab', metaKey: true })).toBe(true)
  })

  it('Shift+Enter does not submit', () => {
    const { ed, view, submit } = setup()
    ed.show()
    ed.insertText('x')
    enter(view, true)
    expect(submit).not.toHaveBeenCalled()
  })

  it('Ctrl-C with no selection clears and cancels (interrupt)', () => {
    const { ed, view, cancel, submit } = setup()
    ed.show()
    ed.insertText('echo partial')
    ctrlC(view)
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(submit).not.toHaveBeenCalled()

    // Cleared, observed publicly: what the next prompt shows is only 'fresh'.
    ed.insertText('fresh')
    enter(view)
    expect(submit).toHaveBeenLastCalledWith('fresh')
  })

  it('Ctrl-C preserves the draft when the host owns an active execution', () => {
    const cancel = vi.fn(() => false)
    const { ed, view } = setup({ cancel })
    ed.show()
    ed.insertText('half-typed question')

    ctrlC(view)

    expect(cancel).toHaveBeenCalledTimes(1)
    expect(ed.getDoc()).toBe('half-typed question')
  })

  it('Ctrl-C with a selection is left alone so copy still works', () => {
    const { ed, view, cancel } = setup()
    ed.show()
    ed.insertText('echo hi')
    select(view, 0, 7) // "echo hi" fully selected
    ctrlC(view)
    expect(cancel).not.toHaveBeenCalled()
    expect(viewOf(ed).state.doc.toString()).toBe('echo hi') // draft untouched
  })

  it('Escape clears the draft, does not cancel (no shell interrupt)', () => {
    const { ed, view, cancel, submit } = setup()
    ed.show()
    ed.insertText('some draft')
    escape(view)
    expect(cancel).not.toHaveBeenCalled()

    // Cleared, observed publicly: the next submit carries only what was typed
    // after the escape.
    ed.insertText('fresh')
    enter(view)
    expect(submit).toHaveBeenLastCalledWith('fresh')
  })

  it('IME composition keys are never interpreted as editor commands', () => {
    const { ed, view, cancel, submit } = setup()
    ed.show()
    ed.insertText('ni')
    // A composition-in-progress Enter (accepting a candidate) must not submit.
    key(view, { key: 'Enter', isComposing: true })
    expect(submit).not.toHaveBeenCalled()
    // ... nor a composing Ctrl-C or Escape.
    key(view, { key: 'c', ctrlKey: true, isComposing: true })
    key(view, { key: 'Escape', isComposing: true })
    expect(cancel).not.toHaveBeenCalled()

    // Draft intact, observed publicly: 'ni' still leads the next submit.
    ed.insertText('!')
    enter(view)
    expect(submit).toHaveBeenLastCalledWith('ni!')
  })

  it('insertText inserts at the caret, replacing any selection', () => {
    const { ed, view, submit } = setup()
    ed.show()
    ed.insertText('echo XX')
    select(view, 5, 7) // select "XX"
    ed.insertText('hi')
    // Replaced, not appended: the next insertion lands after 'hi' and the
    // submitted document is exactly 'echo hi!'.
    ed.insertText('!')
    enter(view)
    expect(submit).toHaveBeenCalledWith('echo hi!')
  })

  it('insertText focuses the editor when visible', () => {
    const { ed, view } = setup()
    ed.show()
    ed.insertText('a')
    expect(document.activeElement).toBe(view.contentDOM)
  })

  it('applies the nocx-editor-input class to the input surface', () => {
    const { view } = setup()
    expect(view.contentDOM.classList.contains('nocx-editor-input')).toBe(true)
  })

  it('the field wrapper is an ancestor of CM6’s own root, never CM6’s root itself (spec 2026-09-15 §4)', () => {
    const { view, container } = setup()
    // The border-owning box is `.ui-composer-frame__field` (ui/composer-
    // frame.ts) — an element CM6 never touches — not `.cm-editor` itself
    // any more: see composer-frame.ts's file header for why (CM6 rewrites
    // `.cm-editor`'s own class attribute, which is what made a class
    // installed there fragile in round 3).
    const field = container.querySelector('.ui-composer-frame__field')!
    expect(field).not.toBeNull()
    expect(field.contains(view.dom)).toBe(true)
    expect(view.dom.classList.contains('nocx-editor-field')).toBe(false)
  })

  it('the field wrapper’s focus attribute is projected from the editor’s own focus notifications, never from CM6’s class (round 3 regression)', () => {
    // Round 3's defect was a class added to `.cm-editor` by hand
    // (`view.dom.classList.add(...)`) surviving only until CM6's own next
    // `editorAttributes` recompute — present unfocused, gone the instant
    // the field focused, because CM6 owns that attribute and rewrites it.
    // The fix moved the border to an ancestor CM6 never writes to, driven
    // by a plain DOM focusin/focusout listener instead — so this asserts
    // the SAME border-affecting state survives a CM6 update cycle, without
    // depending on CM6's own `cm-focused` class at all.
    const { view, container } = setup()
    const field = container.querySelector<HTMLElement>('.ui-composer-frame__field')!
    expect(field.dataset.focused).toBe('false')
    view.contentDOM.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    expect(field.dataset.focused).toBe('true')
    // A CM6 update cycle (the same kind that flips CM6's own `cm-focused`)
    // must not disturb this attribute — it is not derived from CM6's state.
    view.dispatch({})
    expect(field.dataset.focused).toBe('true')
    view.contentDOM.dispatchEvent(
      new FocusEvent('focusout', { bubbles: true, relatedTarget: document.body }),
    )
    expect(field.dataset.focused).toBe('false')
  })

  it('multiline: the host is told when the capped row count changes', () => {
    const resized = vi.fn()
    const { ed } = setup({ resized })
    ed.show()
    expect(resized).not.toHaveBeenCalled() // 1 line = no growth

    ed.insertText('line1\nline2\nline3')
    expect(resized).toHaveBeenCalledTimes(1) // 3 lines

    ed.insertText('\nline4')
    expect(resized).toHaveBeenCalledTimes(2) // 4 lines
  })

  it('Ctrl+Z undoes an edit — the prompt has a history', () => {
    // `@codemirror/commands` was a dependency and `history()` was installed
    // nowhere, so Ctrl+Z in the prompt did nothing. Asserted through the real
    // keystroke rather than by calling undo(), because what was missing was
    // the wiring, not the command.
    const { ed, view } = setup()
    ed.show()
    ed.insertText('git status')
    expect(ed.getDoc()).toBe('git status')
    key(view, { key: 'z', ctrlKey: true })
    expect(ed.getDoc()).toBe('')
  })

  it('a pasted command loses the indent that would hide it from shell history', () => {
    // A leading space is HISTCONTROL=ignorespace: the command runs and the
    // shell does not record it. Every docs site indents its examples, so the
    // flag would be set by accident and invisibly.
    expect(stripPastedIndent('  curl https://x \\\n    -H "A: b"', true)).toBe(
      'curl https://x \\\n    -H "A: b"',
    )
    // Only the first line: inside quotes whitespace is data.
    expect(stripPastedIndent('  a\n    b\n  c', true)).toBe('a\n    b\n  c')
    // Not at a line start: the paste is landing mid-line, where a space is
    // just a space.
    expect(stripPastedIndent('  curl', false)).toBe('  curl')
    // Nothing to strip is left alone (the handler then lets CM6 paste).
    expect(stripPastedIndent('curl', true)).toBe('curl')
  })

  it('multiline: growth reports stop at the cap', () => {
    // The cap is 30 lines and must equal the CSS max-height in
    // styles/surfaces/composer.css: past it the box no longer grows, so
    // there is nothing for the scrollback to follow. Ten lines was the
    // original cap and was raised because a pasted curl with a JSON body is
    // twenty.
    const resized = vi.fn()
    const { ed } = setup({ resized })
    ed.show()
    ed.insertText(Array(35).fill('line').join('\n'))
    expect(resized).toHaveBeenCalledTimes(1) // capped at 30, fired once

    ed.insertText('\nline36')
    expect(resized).toHaveBeenCalledTimes(1) // still 30 rows — no further report
  })

  const context = (container: ParentNode) =>
    container.querySelector<HTMLElement>('.nocx-editor-context .ui-prompt-context')!

  it('the context is one PromptContext naming the directory (spec 2026-09-15 §2, §6)', () => {
    const { ed, container } = setup()
    ed.show()
    expect(context(container).textContent).toContain('~')
    ed.setCwd('/home/dev/projects')
    expect(context(container).textContent).toContain('dev/projects')
    expect(context(container).title).toBe('/home/dev/projects')
    expect(container.textContent).not.toContain('📁')
  })

  it('an SSH prompt names the host first, in the strong emphasis (nocx-3779, spec §5.2)', () => {
    const { ed, container } = setup()
    ed.show()
    ed.setCwd('/srv/nocx')
    ed.setLocation('root@192.0.2.57')
    const strong = context(container).querySelector('[data-emphasis="strong"]')
    expect(strong?.textContent).toBe('root@192.0.2.57')
    expect(context(container).textContent).toContain('srv/nocx')
  })

  it('a local session names no host at all (nocx-3779)', () => {
    const { ed, container } = setup()
    ed.show()
    ed.setLocation('')
    expect(context(container).querySelector('[data-emphasis="strong"]')).toBeNull()
  })

  it('the context dims when focus leaves the composer and normalises when it comes back (spec §6)', () => {
    const { ed, view, container } = setup()
    ed.show()
    view.contentDOM.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    expect(context(container).dataset.tone).toBe('normal')
    view.contentDOM.dispatchEvent(
      new FocusEvent('focusout', { bubbles: true, relatedTarget: document.body }),
    )
    expect(context(container).dataset.tone).toBe('dim')
  })

  it('setWhereFacts shows the branch and re-derives the path against the reported home', () => {
    const { ed, container } = setup()
    ed.show()
    ed.setCwd('/home/dev/repos/nocx')
    ed.setWhereFacts({ home: '/home/dev', branch: 'main' })
    const text = context(container).textContent
    expect(text).toContain('~/repos/nocx')
    expect(context(container).querySelector('[data-part="branch"]')?.textContent).toBe('main')
  })

  it('setWhereFacts with no branch shows a bare path (spec §3: no source, no branch)', () => {
    const { ed, container } = setup()
    ed.show()
    ed.setWhereFacts({})
    expect(context(container).querySelector('[data-part="branch"]')).toBeNull()
  })

  it('the chrome row holds no clock (spec §5.4)', () => {
    const { ed, container } = setup()
    ed.show()
    const chrome = container.querySelector<HTMLElement>('.nocx-editor-chrome')!
    expect(chrome.textContent).not.toMatch(/\d{1,2}:\d{2}/)
    expect('setTime' in ed).toBe(false)
  })

  it('orders the context before the controls, and the controls are kit Buttons', () => {
    const { ed, container } = setup()
    ed.show()
    const row = [...container.querySelector<HTMLElement>('.nocx-editor-chrome')!.children]
    const ctx = container.querySelector('.nocx-editor-context')!
    const controls = container.querySelector('.nocx-editor-controls')!
    expect(row.indexOf(ctx)).toBeLessThan(row.indexOf(controls))
    for (const c of controls.querySelectorAll('[data-control]')) {
      expect(c.classList.contains('ui-button')).toBe(true)
      expect((c as HTMLElement).dataset.variant).toBe('ghost')
      expect((c as HTMLElement).dataset.size).toBe('sm')
    }
  })

  // ── The submit control (spec 2026-09-15 §4) ───────────────────────────
  describe('the submit control', () => {
    const submitButton = (container: ParentNode): HTMLButtonElement =>
      container.querySelector<HTMLButtonElement>('.ui-composer-frame__submit .ui-icon-button')!

    it('is disabled on an empty draft and enabled once one exists, keeping the outlined submit appearance', () => {
      const { ed, container } = setup()
      ed.show()
      const btn = submitButton(container)
      expect(btn.disabled).toBe(true)
      expect(btn.dataset.appearance).toBe('submit')

      ed.insertText('echo hi')
      expect(btn.disabled).toBe(false)
      expect(btn.dataset.appearance).toBe('submit')

      ed.clear()
      expect(btn.disabled).toBe(true)
      expect(btn.dataset.appearance).toBe('submit')
    })

    it('a whitespace-only draft leaves it disabled — matching the whitespace-only Enter rule', () => {
      const { ed, container } = setup()
      ed.show()
      ed.insertText('   ')
      expect(submitButton(container).disabled).toBe(true)
    })

    it('click calls submit() — the same atomic handoff Enter uses, and nothing else', () => {
      const { ed, container, submit, order } = setup()
      ed.show()
      ed.insertText('echo hi')
      submit.mockImplementation((d: string) => order.push(`visible@submit:${ed.isVisible}|${d}`))
      submitButton(container).click()
      expect(submit).toHaveBeenCalledWith('echo hi')
      expect(order[0]).toBe('visible@submit:false|echo hi') // hidden BEFORE submit, same as Enter
    })

    it('the accessible name follows the active target — Run command by default, Send question once handoffToShell refuses', () => {
      const { ed, container } = setup({ handoffToShell: () => false })
      ed.show()
      const btn = submitButton(container)
      expect(btn.getAttribute('aria-label')).toBe('Send question')

      // setTargetExtensions is the SAME seam terminal-content.ts already
      // calls on every switch — re-reading handoffToShell() is what keeps
      // the label in step without a second "which target" channel.
      ed.setTargetExtensions([])
      expect(btn.getAttribute('aria-label')).toBe('Send question')
    })

    it('defaults to Run command when handoffToShell is absent (every existing caller without it)', () => {
      const { ed, container } = setup()
      ed.show()
      expect(submitButton(container).getAttribute('aria-label')).toBe('Run command')
    })
  })

  it('rootContains returns true for the input surface and chrome (focus-bounce)', () => {
    const { ed, view, container } = setup()
    ed.show()
    // The focus-bounce tests `rootContains(activeElement)`; with CM6 the active
    // element is the contentDOM, so this is the contract that must hold.
    expect(ed.rootContains(view.contentDOM)).toBe(true)
    expect(ed.rootContains(context(container))).toBe(true)
  })

  it('rootContains returns false for elements outside the editor root', () => {
    const { ed, container } = setup()
    ed.show()
    expect(ed.rootContains(document.body)).toBe(false)
    expect(ed.rootContains(container)).toBe(false) // mount parent, not inside root
    expect(ed.rootContains(null)).toBe(false)
  })

  it('after hide()/show() the editor is focusable, re-measured, and functional', () => {
    const { ed, view, submit } = setup()
    const requestMeasure = vi.spyOn(view, 'requestMeasure')
    ed.show()
    ed.insertText('first')
    ed.hide()
    expect(ed.isVisible).toBe(false)
    ed.show()
    // A hidden CM6 view can cache wrong geometry; show() must re-measure.
    expect(requestMeasure).toHaveBeenCalled()
    expect(ed.isVisible).toBe(true)
    expect(document.activeElement).toBe(view.contentDOM) // focus lands
    ed.insertText(' + second')
    enter(view)
    expect(submit).toHaveBeenCalledWith('first + second') // editing still works
    requestMeasure.mockRestore()
  })

  it('onInputChange fires on a user-driven document change with the text', () => {
    const onInputChange = vi.fn()
    const { ed, view } = setup({ onInputChange })
    ed.show()
    // The transaction a keystroke produces: a plain dispatch, not the public API.
    view.dispatch({ changes: { from: 0, insert: 'hello' } })
    expect(onInputChange).toHaveBeenCalledWith('hello')
    view.dispatch({ changes: { from: 0, to: 5, insert: 'ssh prod' } })
    expect(onInputChange).toHaveBeenCalledWith('ssh prod')
    expect(onInputChange).toHaveBeenCalledTimes(2)
  })

  it('onInputChange does not fire for programmatic edits (paste/accept parity)', () => {
    const onInputChange = vi.fn()
    const { ed } = setup({ onInputChange })
    ed.show()
    ed.insertText('ssh prod')
    expect(onInputChange).not.toHaveBeenCalled()
  })

  it('clear() empties the document through the submit seam — fires no input, announces the clear (nocx-4ff.7)', () => {
    const onInputChange = vi.fn()
    const onDocCleared = vi.fn()
    const { ed, view } = setup({ onInputChange, onDocCleared })
    ed.show()
    ed.insertText('git status --short')
    ed.clear()
    expect(ed.getDoc()).toBe('')
    // Programmatic, like a submit's clear: no input event, but the
    // cleared-line announcement is the whole point of the seam — the
    // host's floating surfaces must not hold stale findings over the
    // empty line.
    expect(onInputChange).not.toHaveBeenCalled()
    expect(onDocCleared).toHaveBeenCalledTimes(1)
    // And the editor still works afterwards.
    ed.insertText('fresh')
    expect(view.state.doc.toString()).toBe('fresh')
  })

  it('constructor extensions reach the CM6 view', () => {
    const ran: string[] = []
    const ext = keymap.of([{ key: 'F8', run: () => (ran.push('F8'), true) }])
    const { ed, view } = setup({}, [ext])
    ed.show()
    key(view, { key: 'F8' })
    expect(ran).toEqual(['F8'])
  })

  it('Enter still submits even when a default-precedence keymap binds it (keymap precedence)', () => {
    // The scenario ADR-0010 §4 warns about: CM6's defaultKeymap binds Enter to
    // insertNewline. Without Prec.highest that binding would insert a newline —
    // W1's interception must win so the submit contract survives.
    const { ed, view, submit } = setup({}, [keymap.of(defaultKeymap)])
    ed.show()
    ed.insertText('abc')
    enter(view)
    expect(submit).toHaveBeenCalledWith('abc')
  })

  it('a throwing onInputChange cannot corrupt the editor (fail-open)', () => {
    const { ed, view, submit } = setup({
      onInputChange: () => {
        throw new Error('consumer bug')
      },
    })
    ed.show()
    ed.insertText('still works')
    enter(view)
    expect(submit).toHaveBeenCalledWith('still works')
  })

  it('a throwing resized cannot corrupt the editor (fail-open)', () => {
    const { ed, view, submit } = setup({
      resized: () => {
        throw new Error('consumer bug')
      },
    })
    ed.show()
    ed.insertText('a\nb')
    enter(view)
    expect(submit).toHaveBeenCalledWith('a\nb')
  })

  it('a throwing submit leaves the editor hidden and cleared (state consistent)', () => {
    const { ed, view } = setup({
      submit: (d: string) => {
        throw new Error(`submit exploded on ${d}`)
      },
    })
    ed.show()
    ed.insertText('boom')
    // jsdom swallows listener exceptions and reports them as a window 'error'
    // event, which vitest's jsdom environment forwards to
    // process 'uncaughtException' (failing the run) unless a user error
    // listener exists. So the throw cannot be observed with toThrow here; in a
    // real browser it surfaces as an uncaught error AFTER the handoff. What
    // matters is the editor's state: the handoff (clear + hide) already
    // completed before the throw.
    const swallowWindowError = () => {}
    window.addEventListener('error', swallowWindowError)
    try {
      enter(view)
    } finally {
      window.removeEventListener('error', swallowWindowError)
    }
    expect(ed.isVisible).toBe(false)
    expect(viewOf(ed).state.doc.toString()).toBe('')
  })

  it('leaves the layout whole when it hides, and takes its slot back when shown', () => {
    const { ed } = setup()
    ed.show()

    // The composer has ONE way out, and it takes its box with it: the flex
    // slot is what an inline TUI on the normal buffer needs, and the settle
    // glide — not a reserved box — is what keeps the scrollback from jumping
    // (nocx-g6hnk, reversing part of nocx-i4h04).
    ed.hide()

    expect(ed.isVisible).toBe(false)
    expect(ed.root.style.display).toBe('none')
    expect(ed.root.hasAttribute('inert')).toBe(false)

    ed.show()

    expect(ed.isVisible).toBe(true)
    expect(ed.root.style.display).toBe('')
    expect(ed.root.style.visibility).toBe('')
    expect(ed.root.dataset.suspended).toBeUndefined()
    expect(ed.root.hasAttribute('inert')).toBe(false)
  })

  it('dispose removes the root and leaves the editor inert, not broken', () => {
    const { ed, container } = setup()
    ed.show()
    ed.dispose()
    expect(container.querySelector('.nocx-editor')).toBeNull()
    expect(() => ed.hide()).not.toThrow()
    expect(ed.isVisible).toBe(false)
    expect(() => ed.insertText('x')).not.toThrow()
  })
})

describe('ssh key ownership: the completion dropdown owns the keys (nocx-fijh)', () => {
  /** A host-shaped provider for the ssh argument position — the dropdown's
   *  rows in the state the user is in (`ssh ` + Tab). */
  const sshHostProvider = (hosts: string[]): SuggestionProvider => ({
    id: 'host',
    targetId: 'shell',
    applicable: (c) => c.position === 'argument' && commandWord(c) === 'ssh',
    suggest: () =>
      Promise.resolve({
        candidates: hosts.map((h): Candidate => ({
          id: `host:${h}`,
          targetId: 'shell',
          providerId: 'host',
          displayText: h,
          insertText: h,
          replacement: { from: 4, to: 4 },
          matchRanges: [{ from: 0, to: 0 }],
          source: 'host',
          eligibleForGhostText: true,
        })),
      }),
  })

  it('under ssh the completion dropdown gets the keys its footer advertises: Tab opens it, ArrowDown moves its selection, Enter accepts, Escape dismisses', async () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    const controller = new CompletionController({
      providers: [sshHostProvider(['alpha', 'beta'])],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      latencyBudgetMs: 0,
      now: () => 1_750_000_000_000,
    })
    const submit = vi.fn()
    const ed = new CommandEditor(
      { submit, cancel: vi.fn(), onTab: () => controller.open() },
      controller.extensions(),
    )
    ed.mount(container)
    controller.attach(ed, container)
    // The composition-root chain shape (terminal-content.ts): completion is
    // the arbiter's last link, the editor's own handling is the tail.
    ed.setKeyArbiter((e) => controller.handleKey(e))
    ed.show()
    ed.insertText('ssh ')
    const view = viewOf(ed)
    // The user's path to the surface: Tab opens the dropdown with hosts.
    key(view, { key: 'Tab' })
    // Flush the provider's already-resolved promise through the controller's
    // .then chain — microtasks only, never a wall-clock timer.
    await Promise.resolve()
    await Promise.resolve()
    expect(dropdown.isOpen).toBe(true)
    const selected = () =>
      dropdown.root.querySelector('.ui-floating-panel__row[data-selected="true"]')
    expect(selected()?.textContent).toContain('alpha')

    // ArrowDown travels the editor seam and moves the DROPDOWN selection —
    // no other surface exists to claim the key. The selection moved: that
    // is the assertion, not that a handler was called.
    key(view, { key: 'ArrowDown' })
    expect(selected()?.textContent).toContain('beta')

    // Enter accepts the dropdown's selection. Nothing is submitted.
    key(view, { key: 'Enter' })
    expect(ed.getDoc()).toBe('ssh beta')
    expect(dropdown.isOpen).toBe(false)
    expect(submit).not.toHaveBeenCalled()

    // Escape with the dropdown open closes exactly the dropdown.
    key(view, { key: 'Tab' })
    await Promise.resolve()
    await Promise.resolve()
    expect(dropdown.isOpen).toBe(true)
    key(view, { key: 'Escape' })
    expect(dropdown.isOpen).toBe(false)
    expect(ed.getDoc()).toBe('ssh beta')
  })

  it('at bare `ssh` with no space typed, Tab opens the completion dropdown with history rows and inserts nothing (nocx-v03i)', async () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const dropdown = new CompletionDropdown({ onHover: () => {}, onPick: () => {} })
    // The shipped history provider's applicability: any non-empty line, and
    // a row whose command starts with the line replaces the whole line.
    const historyProvider: SuggestionProvider = {
      id: 'history',
      targetId: 'shell',
      applicable: (c) => c.doc.trim() !== '',
      suggest: (ctx) =>
        Promise.resolve({
          candidates: ['ssh pi@192.0.2.93', 'ssh pi@192.0.2.93 -p 22', 'ssh prod'].map(
            (r): Candidate => ({
              id: `hist:${r}`,
              targetId: 'shell',
              providerId: 'history',
              displayText: r,
              insertText: r,
              replacement: { from: 0, to: ctx.doc.length },
              matchRanges: [{ from: 0, to: ctx.doc.length }],
              source: 'history',
              scope: 'directory',
              eligibleForGhostText: true,
            }),
          ),
        }),
    }
    const controller = new CompletionController({
      // The host provider is wired too. At bare `ssh` the token sits in
      // COMMAND position, where the host provider is inapplicable — the
      // composition must surface HISTORY here, not hosts.
      providers: [sshHostProvider(['root@192.0.2.57']), historyProvider],
      dropdown,
      env: () => ({ isLocal: true, cwd: '/repo', host: '' }),
      latencyBudgetMs: 0,
      now: () => 1_750_000_000_000,
    })
    const submit = vi.fn()
    const ed = new CommandEditor(
      { submit, cancel: vi.fn(), onTab: () => controller.open() },
      controller.extensions(),
    )
    ed.mount(container)
    controller.attach(ed, container)
    ed.setKeyArbiter((e) => controller.handleKey(e))
    ed.show()
    ed.insertText('ssh') // caret right after 'ssh' — no space typed yet
    const view = viewOf(ed)

    key(view, { key: 'Tab' })
    await Promise.resolve()
    await Promise.resolve()
    expect(dropdown.isOpen).toBe(true)
    // Tab itself inserted nothing — the document is byte-identical.
    expect(ed.getDoc()).toBe('ssh')
    const rows = () =>
      [...dropdown.root.querySelectorAll<HTMLElement>('.ui-floating-panel__row')].map(
        (r) => r.textContent ?? '',
      )
    expect(rows().some((t) => t.includes('ssh pi@192.0.2.93'))).toBe(true)
    // The host provider was not consulted: its row is nowhere on the list.
    expect(rows().some((t) => t.includes('root@192.0.2.57'))).toBe(false)
    expect(submit).not.toHaveBeenCalled()
  })
})

describe('tab completion wiring', () => {
  it('Tab fires onTab and is consumed (never submits, never moves focus)', () => {
    const onTab = vi.fn()
    const { view, submit } = setup({ onTab })
    // dispatchEvent resolves false when the event was canceled — the Tab was
    // swallowed, so it can never reach the browser's focus-move default.
    expect(key(view, { key: 'Tab' })).toBe(false)
    expect(onTab).toHaveBeenCalledTimes(1)
    expect(submit).not.toHaveBeenCalled()
  })

  it('Ctrl-Tab is not completion — it falls through untouched', () => {
    const onTab = vi.fn()
    const { view } = setup({ onTab })
    expect(key(view, { key: 'Tab', ctrlKey: true })).toBe(true)
    expect(onTab).not.toHaveBeenCalled()
  })

  it('with no onTab action, Tab is still swallowed (focus never leaves)', () => {
    const { view } = setup()
    expect(key(view, { key: 'Tab' })).toBe(false)
  })

  it('Shift+Tab with the dropdown closed is swallowed and opens nothing (Tab opens; Shift+Tab cycles back once open)', () => {
    const onTab = vi.fn()
    const { ed, view } = setup({ onTab })
    ed.show()
    ed.insertText('ec')
    expect(key(view, { key: 'Tab', shiftKey: true })).toBe(false)
    expect(onTab).not.toHaveBeenCalled()
    // The doc is untouched and the key never reached the browser's
    // focus-move default.
    expect(view.state.doc.toString()).toBe('ec')
  })
})

// ── Shell syntax highlighting (shell-highlight.ts) ─────────────────────

/** Read the live line's token spans as [class, text] pairs, in DOM order. */
function liveTokens(doc: string, store?: CommandSnapshotStore): Array<[string, string]> {
  const { ed, view } = setup({}, shellExtensions(store ?? new CommandSnapshotStore()))
  ed.show()
  ed.insertText(doc)
  return [...view.contentDOM.querySelectorAll<HTMLElement>('[class^="tok-"]')].map((span) => [
    span.className,
    span.textContent ?? '',
  ])
}

/** A fresh, empty store — "no snapshot" verdicts for the pure-syntax tests. */
const freshStore = (): CommandSnapshotStore => new CommandSnapshotStore()

/** Read the static pass's token spans the same way, from a template element. */
function staticTokens(html: string): Array<[string, string]> {
  const root = document.createElement('div')
  root.innerHTML = html
  return [...root.querySelectorAll<HTMLElement>('[class^="tok-"]')].map((span) => [
    span.className,
    span.textContent ?? '',
  ])
}

describe('shell syntax highlighting', () => {
  // The grammar loads asynchronously at module init; every test below needs
  // the tokenizer ready so live and frozen classes are deterministic.
  beforeAll(async () => {
    await shellHighlightReady
  })

  it('command name, flag, pipe and redirect target are distinguishable token classes', () => {
    const byClass = new Map<string, string[]>()
    for (const [classes, text] of liveTokens('ls -la | grep foo > out.txt')) {
      for (const cls of classes.split(/\s+/)) {
        byClass.set(cls, [...(byClass.get(cls) ?? []), text])
      }
    }
    expect(byClass.get('tok-command')).toEqual(['ls', 'grep'])
    expect(byClass.get('tok-flag')).toEqual(['-la'])
    expect(byClass.get('tok-operator')).toEqual(['|', '>'])
    // The VS Code grammar styles every bare word after the command as an
    // unquoted argument, so the redirect target and the plain argument `foo`
    // share the path role.
    expect(byClass.get('tok-path')).toEqual(['foo', 'out.txt'])
  })

  it('a quoted string containing a pipe is one string token', () => {
    const tokens = liveTokens('echo "a|b"')
    expect(tokens.filter(([cls]) => cls === 'tok-string')).toEqual([['tok-string', '"a|b"']])
    expect(tokens.some(([cls, text]) => cls === 'tok-operator' && text === '|')).toBe(false)
  })

  it('a pipe inside a comment is not an operator', () => {
    const tokens = liveTokens('# ls | grep foo')
    expect(tokens).toContainEqual(['tok-comment', '# ls | grep foo'])
    expect(tokens.some(([cls, text]) => cls === 'tok-operator' && text === '|')).toBe(false)
  })

  it('highlighting is off when no shell language is installed (non-shell target)', () => {
    const { ed, view } = setup() // extensions default to []
    ed.show()
    ed.insertText('ls -la | grep foo > out.txt')
    expect(view.contentDOM.querySelectorAll('[class^="tok-"]').length).toBe(0)
  })

  it('the frozen-header pass emits the same classes as the live line for the same text', () => {
    const doc = 'ls -la | grep foo > out.txt'
    expect(staticTokens(highlightShellText(doc, freshStore()))).toEqual(liveTokens(doc))
  })

  it('the static pass escapes the command text (no markup injection)', () => {
    const html = highlightShellText('echo "<script>alert(1)</script>"', freshStore())
    expect(html).not.toContain('<script>')
    const root = document.createElement('div')
    root.innerHTML = html
    expect(root.textContent).toBe('echo "<script>alert(1)</script>"')
  })

  it('the corpus: every command word, real or invented, lands in the same class', () => {
    const cases: Array<[string, string]> = [
      ['ls -la', 'ls'],
      ['pwd', 'pwd'],
      ['cargo test', 'cargo'],
      ['kubectl get pods', 'kubectl'],
      ['docker compose up', 'docker'],
      ['./scripts/release', './scripts/release'],
      ['sdf dffd', 'sdf'],
    ]
    for (const [line, word] of cases) {
      const classes = liveTokens(line)
        .filter(([, text]) => text === word)
        .map(([cls]) => cls)
      // Exactly the command class and nothing else — `sdf` is styled like any
      // other command word and gets no diagnostic (no existence claim).
      expect(classes).toEqual(['tok-command'])
    }
  })

  it('the corpus: live and frozen produce identical classes for identical text', () => {
    const corpus = [
      'ls -la',
      'pwd',
      'cargo test',
      'kubectl get pods',
      'docker compose up',
      './scripts/release',
      'FOO=bar make build',
      'sudo -u deploy make release',
      'echo "a|b"',
      '# ls | grep foo',
      'sdf dffd',
    ]
    for (const line of corpus) {
      expect(staticTokens(highlightShellText(line, freshStore()))).toEqual(liveTokens(line))
    }
  })

  it('an assignment keeps its variable and operator roles distinct from the command', () => {
    const tokens = liveTokens('FOO=bar make build')
    expect(tokens).toContainEqual(['tok-variable', 'FOO'])
    expect(tokens).toContainEqual(['tok-operator', '='])
    expect(tokens).toContainEqual(['tok-command', 'make'])
    // `build` is make's argument, so the grammar styles it as an unquoted
    // argument, not a command — only the command-position word is special.
    expect(tokens).toContainEqual(['tok-path', 'build'])
  })

  it('a multiline document colours every line (document-wide token offsets)', () => {
    const tokens = liveTokens('kubectl get pods\nls -la')
    expect(tokens).toContainEqual(['tok-command', 'kubectl'])
    expect(tokens).toContainEqual(['tok-command', 'ls'])
    expect(tokens).toContainEqual(['tok-flag', '-la'])
  })

  it('a heredoc body across lines keeps the heredoc role', () => {
    const tokens = staticTokens(highlightShellText('cat <<EOF\nhello world\nEOF', freshStore()))
    expect(tokens).toContainEqual(['tok-heredoc', 'hello world'])
    expect(tokens).toContainEqual(['tok-heredoc', 'EOF'])
  })
})

// ── Command-existence verdicts from backend snapshots ────────────────────

describe('command existence verdicts', () => {
  // The tokenizer must be ready so the verdict classes are deterministic.
  beforeAll(async () => {
    await shellHighlightReady
  })

  /** A fresh tab store, seeded with the given names (or left empty). */
  const makeStore = (names?: string[]): CommandSnapshotStore => {
    const store = freshStore()
    if (names) {
      store.applyBackendSnapshot(names)
      // An empty shared PATH set is present, so the store can judge names
      // absent from the session-local snapshot.
      store.applySharedNames({ state: 'ready', names: [], ageMs: 0, reason: '', truncated: false })
    }
    return store
  }

  it('pwd — a builtin, not a file — renders resolved with no underline', () => {
    expect(liveTokens('pwd', makeStore(['pwd', 'ls']))).toEqual([['tok-command', 'pwd']])
  })

  it('sdfsdf renders unresolved: the command class plus the underline class, not a red foreground', () => {
    expect(liveTokens('sdfsdf', makeStore(['pwd', 'ls']))).toEqual([
      ['tok-command tok-unresolved', 'sdfsdf'],
    ])
  })

  it('$TOOL build renders indeterminate — nothing is underlined', () => {
    expect(
      liveTokens('$TOOL build', makeStore(['pwd', 'ls'])).some(([cls]) =>
        cls.includes('tok-unresolved'),
      ),
    ).toBe(false)
  })

  it('"$(pick)" arg renders indeterminate — the name inside a substitution is not checked', () => {
    expect(
      liveTokens('"$(pick)" arg', makeStore(['pwd', 'ls'])).some(([cls]) =>
        cls.includes('tok-unresolved'),
      ),
    ).toBe(false)
  })

  it('a command inside $( ) renders indeterminate even when it exists', () => {
    expect(
      liveTokens('x=$(ls)', makeStore(['pwd', 'ls'])).some(([cls]) =>
        cls.includes('tok-unresolved'),
      ),
    ).toBe(false)
  })

  it('with no snapshot everything is unavailable and nothing is underlined', () => {
    const tokens = liveTokens('sdfsdf && pwd', makeStore())
    expect(tokens.some(([cls]) => cls.includes('tok-unresolved'))).toBe(false)
    expect(tokens.filter(([cls]) => cls === 'tok-command')).toEqual([
      ['tok-command', 'sdfsdf'],
      ['tok-command', 'pwd'],
    ])
  })

  it('the live line re-decorates when the snapshot arrives mid-typing', () => {
    const store = freshStore()
    const { ed, view } = setup({}, shellExtensions(store))
    ed.show()
    ed.insertText('sdfsdf')
    expect(view.contentDOM.querySelectorAll('.tok-unresolved').length).toBe(0)
    store.applyBackendSnapshot(['pwd'])
    store.applySharedNames({ state: 'ready', names: [], ageMs: 0, reason: '', truncated: false })
    expect(view.contentDOM.querySelectorAll('.tok-unresolved').length).toBe(1)
  })

  it('the frozen-header pass carries the same verdict classes', () => {
    const store = makeStore(['pwd'])
    expect(staticTokens(highlightShellText('pwd', store))).toEqual([['tok-command', 'pwd']])
    expect(staticTokens(highlightShellText('sdfsdf', store))).toEqual([
      ['tok-command tok-unresolved', 'sdfsdf'],
    ])
  })

  it('a tab whose session never sent a snapshot reports unavailable even when another tab has one', () => {
    const other = makeStore(['pwd']) // this tab is ready…
    const mine = makeStore() // …this tab never received a snapshot
    expect(other.status).toBe('ready')
    expect(mine.status).toBe('unavailable')
    // No underline in the snapshot-less tab — unavailable never collapses
    // into unresolved, even while a sibling tab is fully seeded.
    expect(liveTokens('sdfsdf', mine)).toEqual([['tok-command', 'sdfsdf']])
    expect(liveTokens('sdfsdf', other)).toEqual([['tok-command tok-unresolved', 'sdfsdf']])
  })
})

// ───────────────────────────────────────────────────────────────────────────
// The model chip in the chrome row (nocx-rikz5). The chip names the model
// that will answer and IS the way to change it. Two claims the surface
// tests cannot make: the listeners are installed ONCE and read a slot (a
// listener per state change is how one click fires three times), and a
// rung with nowhere to go is not a control.
//
// The height claim — a chip appearing must not move the chrome row —
// deliberately lives in the Playwright spec, not here: jsdom computes no
// layout, so getBoundingClientRect returns zeroes and such an assertion
// passes while meaning nothing.
describe('the model chip (nocx-rikz5)', () => {
  const chipsOf = (ed: CommandEditor): HTMLElement[] =>
    Array.from(ed.root.querySelectorAll<HTMLElement>('[data-control^="model"]')).filter(
      (el) => el.style.display !== 'none',
    )

  const textsOf = (ed: CommandEditor): string[] => chipsOf(ed).map((el) => el.textContent ?? '')

  it('shows nothing until a state arrives — the row grows no chip on its own', () => {
    const { ed } = setup()
    expect(textsOf(ed)).toEqual([])
    ed.setModelChip(null)
    expect(textsOf(ed)).toEqual([])
  })

  it('names the endpoint and the model, each with the full value as its title and accessible name', () => {
    const { ed } = setup()
    const model = 'deepseek/deepseek-v4-flash-0731-preview'
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model })
    expect(textsOf(ed)).toEqual(['openrouter', model])
    const [endpointChip, modelChip] = chipsOf(ed)
    expect(endpointChip.title).toBe('openrouter')
    expect(endpointChip.getAttribute('aria-label')).toBe('Answers with openrouter. Open Endpoints.')
    expect(modelChip.title).toBe(model)
    expect(modelChip.getAttribute('aria-label')).toBe(
      `Answers with the model ${model}. Open Roles.`,
    )
  })

  it('opens Endpoints from the endpoint and Roles from the model', () => {
    const { ed } = setup()
    const opened: string[] = []
    ed.onModelChipClick((page) => opened.push(page))
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    const [endpointChip, modelChip] = chipsOf(ed)
    endpointChip.click()
    modelChip.click()
    expect(opened).toEqual(['endpoints', 'roles'])
  })

  it('carries one rung sentence when the role does not resolve, and opens the page it names', () => {
    const { ed } = setup()
    const opened: string[] = []
    ed.onModelChipClick((page) => opened.push(page))
    ed.setModelChip({ kind: 'action', text: 'Add an endpoint first', page: 'endpoints' })
    expect(textsOf(ed)).toEqual(['Add an endpoint first'])
    chipsOf(ed)[0].click()
    expect(opened).toEqual(['endpoints'])
  })

  it('a rung no page repairs is not a control — the chip is disabled and clicking it does nothing', () => {
    const { ed } = setup()
    const opened: string[] = []
    ed.onModelChipClick((page) => opened.push(page))
    ed.setModelChip({
      kind: 'action',
      text: 'Settings could not be read — the assistant is unavailable',
      page: null,
    })
    const [chip] = chipsOf(ed)
    expect((chip as HTMLButtonElement).disabled).toBe(true)
    // A disabled button swallows the click in a real engine; jsdom's
    // .click() does not, so the SLOT is what has to be empty.
    chip.click()
    expect(opened).toEqual([])
  })

  it('becomes a control again when the next state has somewhere to go', () => {
    const { ed } = setup()
    const opened: string[] = []
    ed.onModelChipClick((page) => opened.push(page))
    ed.setModelChip({ kind: 'action', text: 'unavailable', page: null })
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    const [endpointChip, modelChip] = chipsOf(ed)
    expect((endpointChip as HTMLButtonElement).disabled).toBe(false)
    expect((modelChip as HTMLButtonElement).disabled).toBe(false)
    modelChip.click()
    expect(opened).toEqual(['roles'])
  })

  it('one click is one call however many states the chip has worn', () => {
    // The listener is installed once in the constructor and reads a
    // mutable slot (recoveryChip's precedent). Re-adding it per state is
    // how one click ends up firing three times.
    const { ed } = setup()
    const opened: string[] = []
    ed.onModelChipClick((page) => opened.push(page))
    ed.setModelChip({ kind: 'action', text: 'Choose a model', page: 'roles' })
    ed.setModelChip({ kind: 'action', text: 'Add an endpoint first', page: 'endpoints' })
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    chipsOf(ed)[1].click()
    expect(opened).toEqual(['roles'])
  })

  it('null takes both chips away again — a Run target has no model to name', () => {
    const { ed } = setup()
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    expect(textsOf(ed)).toHaveLength(2)
    ed.setModelChip(null)
    expect(textsOf(ed)).toEqual([])
  })

  it('is a kit ghost Button that truncates, in the controls group (spec §5.2)', () => {
    const { ed } = setup()
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    for (const chip of chipsOf(ed)) {
      expect(chip.classList.contains('ui-button')).toBe(true)
      expect(chip.dataset.truncate).toBe('true')
      expect(chip.closest('.nocx-editor-controls')).not.toBeNull()
    }
  })
})
describe('the grant chip (nocx-wcswn)', () => {
  // The editor owns the chip's ELEMENT and its place in the row; GrantController
  // owns everything it SAYS (nocx-5u3oz.13). So this asserts identity and
  // position only — the label, the aria-label, the title and the typed state
  // are grant.test.ts's, and asserting them here is what made two owners.
  it('is the last control in the row, and is a kit Button', () => {
    const { ed } = setup()
    const controls = ed.root.querySelector('.nocx-editor-controls')!
    const grant = controls.querySelector<HTMLElement>('[data-control="grant"]')!
    expect(grant.classList.contains('ui-button')).toBe(true)
    expect([...controls.children].indexOf(grant)).toBe(controls.children.length - 1)
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    expect([...controls.children].indexOf(grant)).toBe(controls.children.length - 1)
  })

  it('clicking the chip reaches the host without changing the input target', () => {
    const { ed } = setup()
    const opened = vi.fn()
    ed.onGrantChipClick(opened)
    ed.root.querySelector<HTMLElement>('[data-control="grant"]')!.click()
    expect(opened).toHaveBeenCalledTimes(1)
  })
})

describe('the box transition (nocx-1ebrs.40)', () => {
  // jsdom performs no layout and ships no Web Animations API, so the animated
  // path needs both named by the test: `rect` stands in for the box's
  // measured rectangle, and Element.animate is stubbed for EVERY element so
  // the fade's two surfaces — the box (its return) and the ghost (the
  // departure) — can be told apart by the element that was asked to move.
  const rect = (
    ed: CommandEditor,
    box: { x: number; y: number; left: number; top: number; width: number; height: number },
  ): void => {
    vi.spyOn(ed.root, 'getBoundingClientRect').mockReturnValue(box as DOMRect)
  }
  const animateAny = () => {
    const calls: {
      el: Element
      keyframes: Keyframe[]
      options: number | KeyframeAnimationOptions
    }[] = []
    const anims: {
      el: Element
      finished: Promise<void>
      resolve: () => void
      cancel: ReturnType<typeof vi.fn>
    }[] = []
    const proto = Element.prototype as unknown as { animate?: unknown }
    const orig = proto.animate
    Object.defineProperty(proto, 'animate', {
      configurable: true,
      value: function (keyframes: Keyframe[], options: number | KeyframeAnimationOptions) {
        calls.push({ el: this as Element, keyframes, options })
        // One promise, settled by resolve() or rejected by cancel() — the
        // real WAAPI settles `finished` in rejection on cancel, and the
        // implementation's rejection handler is what removes the ghost, so
        // the stand-in has to too, or a cancelled ghost outlives the test.
        let resolve!: () => void
        let reject!: (e: Error) => void
        const finished = new Promise<void>((res, rej) => {
          resolve = res
          reject = rej
        })
        const cancel = vi.fn(() => {
          reject(new Error('cancelled'))
        })
        anims.push({ el: this as Element, finished, resolve, cancel })
        return { finished, cancel }
      },
    })
    const restore = (): void => {
      if (orig === undefined) delete proto.animate
      else proto.animate = orig
    }
    return { calls, anims, restore }
  }
  const BOX = { x: 8, y: 9, left: 8, top: 9, width: 300, height: 130 }
  const ghostOf = (): HTMLElement | null =>
    document.querySelector<HTMLElement>('[data-nocx-motion-ghost]')
  // A ghost whose stand-in promise never settled would leak into the next
  // test's document-wide queries; each test owns its ghosts only.
  afterEach(() => {
    document.querySelectorAll('[data-nocx-motion-ghost]').forEach((g) => g.remove())
  })

  it('without the Web Animations API the box leaves in one frame, as before', () => {
    // jsdom is that build: no `animate` on the element, so hide() is the
    // instant removal every existing test and every reduced-motion user gets.
    const { ed } = setup()
    ed.show()
    ed.hide()
    expect(ed.root.style.display).toBe('none')
    expect(ed.isVisible).toBe(false)
    expect(ghostOf()).toBeNull()
  })

  it('hide() takes the box out of the flow in one step; a ghost fades where it was', () => {
    const { ed } = setup()
    ed.show()
    rect(ed, BOX)
    const { calls, restore } = animateAny()
    try {
      ed.hide()
      // Input ownership has already moved — isVisible is false the instant
      // hide() ran — and the box is out of the layout in the SAME frame as
      // the blur: every consumer that waits for the composer to be visible
      // must wait for the box that owns input, never catch a dying one
      // (a submit typed into that dying window went raw to the pty and no
      // block was ever recorded — the journey spec caught it).
      expect(ed.isVisible).toBe(false)
      expect(ed.root.style.display).toBe('none')
      expect(ed.root.style.position).toBe('')
      expect(ed.root.getAttribute('inert')).toBeNull()
      // The ink fades on the ghost: the box's frozen copy, fixed at the
      // rectangle the box occupied, owning paint only.
      const ghost = ghostOf()
      expect(ghost).not.toBeNull()
      const call = calls.find((c) => c.el === ghost)
      expect(call, 'the ghost is what animates').toBeTruthy()
      expect(call!.keyframes).toEqual([{ opacity: '1' }, { opacity: '0' }])
      expect(ghost!.style.position).toBe('fixed')
      expect(ghost!.style.left).toBe('8px')
      expect(ghost!.style.top).toBe('9px')
      expect(ghost!.style.width).toBe('300px')
      expect(ghost!.style.height).toBe('130px')
      expect(ghost!.style.pointerEvents).toBe('none')
      expect(ghost!.getAttribute('inert')).toBe('')
      expect(ghost!.getAttribute('aria-hidden')).toBe('true')
    } finally {
      restore()
    }
  })

  it('the departure wears no composer identity: a document-wide query still answers one composer', () => {
    // The suite's promise (e2e/harness.ts): a bare `.nocx-editor` /
    // `.nocx-editor-input` locator resolves to ONE element per tab, and
    // Playwright's strict mode reads two as a product bug. Bought on CI run
    // 39602953353: both identity classes rode the clone, so for the 140ms of
    // every submit two elements answered, and the first strict poll after
    // Enter — command-editor.spec.ts:246, enhanced-input.spec.ts:21 — failed
    // on all four e2e shards. The departure carries its own name; the ink
    // classes stay, because they are the ink.
    // This file's earlier tests leave their roots in the document, so a
    // whole-document count is the FILE's, not the product's: the assertion is
    // this test's own surfaces — the box is the only composer its container
    // holds, and the ghost, the one other element hide() puts in the
    // document, answers neither name. The document-wide strictness itself is
    // owned by the two e2e specs that bought it.
    const { ed, view, container } = setup()
    ed.show()
    rect(ed, BOX)
    const { restore } = animateAny()
    try {
      ed.hide()
      const editors = [...document.querySelectorAll('.nocx-editor')].filter((el) =>
        container.contains(el),
      )
      expect(editors).toHaveLength(1)
      expect(editors[0]).toBe(ed.root)
      const inputs = [...document.querySelectorAll('.nocx-editor-input')].filter((el) =>
        container.contains(el),
      )
      expect(inputs).toHaveLength(1)
      expect(inputs[0]).toBe(view.contentDOM)
      // The ghost keeps its own names, and the ink classes with them.
      const ghost = ghostOf()!
      expect(ghost.matches('.nocx-editor')).toBe(false)
      expect(ghost.closest('.nocx-editor')).toBeNull()
      expect(ghost.querySelector('.nocx-editor-input')).toBeNull()
      expect(ghost.classList.contains('nocx-editor-departure')).toBe(true)
      const ghostInput = ghost.querySelector<HTMLElement>('.nocx-editor-departure-input')!
      expect(ghostInput).not.toBeNull()
      expect(ghostInput.classList.contains('cm-content')).toBe(true)
      expect(ghostInput.classList.contains('nocx-editor-input')).toBe(false)
      expect(ghost.querySelector('.cm-editor')).not.toBeNull()
    } finally {
      restore()
    }
  })

  it('the ghost is removed when its fade ends, and when dispose() runs', async () => {
    const { ed } = setup()
    ed.show()
    rect(ed, BOX)
    const { anims, restore } = animateAny()
    try {
      ed.hide()
      const ghost = ghostOf()!
      const anim = anims.find((x) => x.el === ghost)!
      anim.resolve()
      await anim.finished
      expect(document.contains(ghost)).toBe(false)
      // And a ghost in flight does not survive a dispose.
      ed.show()
      rect(ed, BOX)
      ed.hide()
      const second = ghostOf()!
      expect(second).not.toBeNull()
      ed.dispose()
      expect(document.contains(second)).toBe(false)
    } finally {
      restore()
    }
  })

  it('show() while a departure fades: the ghost goes, the box returns from nothing', async () => {
    const { ed } = setup()
    ed.show()
    rect(ed, BOX)
    const { calls, anims, restore } = animateAny()
    try {
      ed.hide()
      const ghost = ghostOf()!
      const ghostAnim = anims.find((x) => x.el === ghost)!
      ed.show()
      // The box is back; the ghost's half-faded ink over it would read as a
      // second flicker, so the return cancels the departure outright.
      expect(ghostAnim.cancel).toHaveBeenCalled()
      // The removal rides the rejection the cancel settled; one microtask.
      await Promise.resolve()
      expect(document.contains(ghost)).toBe(false)
      expect(ed.root.style.display).toBe('')
      expect(ed.isVisible).toBe(true)
      const backCall = calls.filter((c) => c.el === ed.root)
      expect(backCall).toHaveLength(1)
      // Opacity only, from nothing: the box's layout height never animates,
      // so no fit number moves mid-return.
      expect(backCall[0].keyframes).toEqual([{ opacity: '0' }, { opacity: '1' }])
    } finally {
      restore()
    }
  })

  it('hide() mid-return hands the reached ink to the ghost', () => {
    const { ed } = setup()
    ed.show()
    rect(ed, BOX)
    const first = animateAny()
    try {
      ed.show()
      // The return is mid-fade at 0.4 ink when the box is asked to leave:
      // the ghost starts from the ink the person is actually looking at.
      ed.root.style.opacity = '0.4'
      ed.hide()
      expect(ed.root.style.display).toBe('none')
      const ghost = ghostOf()!
      const call = first.calls.find((c) => c.el === ghost)
      expect(call).toBeTruthy()
      expect(call!.keyframes).toEqual([{ opacity: '0.4' }, { opacity: '0' }])
    } finally {
      first.restore()
    }
  })

  it('a reduced-motion preference keeps the old instant box', () => {
    const { ed } = setup()
    ed.show()
    rect(ed, BOX)
    // jsdom defines no matchMedia at all; motion.ts reads it optionally, so
    // the stub is the whole answer.
    vi.stubGlobal('matchMedia', () => ({ matches: true }))
    try {
      const { calls, restore } = animateAny()
      try {
        ed.hide()
        expect(ed.root.style.display).toBe('none')
        expect(calls).toHaveLength(0)
        expect(ghostOf()).toBeNull()
      } finally {
        restore()
      }
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
