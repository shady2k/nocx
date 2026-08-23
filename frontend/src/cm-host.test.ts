// @vitest-environment jsdom
//
// CM host tests — the ownership contract from the git-manager design §5.4:
// read-only enforcement, caller extensions appended after the host's, setDoc
// as the one document writer, and disposal on both paths (explicit dispose()
// and AbortSignal). Everything is asserted through the DOM and the public
// methods — the underlying EditorView is the host's private property.
//
// The host has two modes and one lifecycle (nocx-gjnr): ReadOnlyHost is the
// file viewer's and the diff's, EditableHost is the snippet body editor's.
// The read-only tests below are the reason the modes are one module — an
// editable sibling built beside it would have been a second construction of
// the same view, free to drift on the theme, the disposal and the facets.
import { afterEach, describe, expect, it } from 'vitest'
import { EditorState, type Extension } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { EditableHost, ReadOnlyHost } from './cm-host'

// CM6 renders each line as a div.cm-line (no newline text nodes), so a raw
// textContent read collapses lines. Joining the line divs reconstructs the
// document exactly, including a trailing empty line for a final newline.
const docText = (parent: HTMLElement): string =>
  Array.from(parent.querySelectorAll('.cm-line'))
    .map((el) => el.textContent ?? '')
    .join('\n')

interface Mounted {
  host: ReadOnlyHost
  parent: HTMLElement
  controller: AbortController
}

function mountHost(extensions: Extension[] = []): Mounted {
  const host = new ReadOnlyHost()
  const parent = document.createElement('div')
  document.body.append(parent)
  const controller = new AbortController()
  host.mount(parent, controller.signal, extensions)
  return { host, parent, controller }
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ReadOnlyHost — read-only enforcement', () => {
  it('no keystroke can reach the document, even through caller extensions that try to re-enable editing', () => {
    const { host, parent } = mountHost([
      // A hostile caller extension attempting to defeat the host: the host's
      // facets come first in the extension array and CM6 resolves them by
      // precedence with the first value winning, so this cannot win.
      EditorState.readOnly.of(false),
      EditorView.editable.of(true),
    ])
    host.setDoc('frozen\ncontent\n')

    const contentEl = parent.querySelector('.cm-content') as HTMLElement
    // The structural guarantees: not an editable region, declared read-only.
    expect(contentEl.getAttribute('contenteditable')).toBe('false')
    expect(contentEl.getAttribute('aria-readonly')).toBe('true')

    const key = (init: KeyboardEventInit): void => {
      contentEl.dispatchEvent(
        new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
      )
    }
    key({ key: 'a' })
    key({ key: 'Enter' })
    key({ key: 'Backspace' })
    key({ key: 'x', ctrlKey: true })

    expect(docText(parent)).toBe('frozen\ncontent\n')
    host.dispose()
  })

  it('caller extensions are appended after the host and take effect', () => {
    const { host, parent } = mountHost([
      EditorView.editorAttributes.of({ class: 'caller-extension-marker' }),
    ])
    host.setDoc('text')

    expect(parent.querySelector('.cm-editor')?.classList.contains('caller-extension-marker')).toBe(
      true,
    )
    expect(docText(parent)).toBe('text')
    host.dispose()
  })
})

describe('ReadOnlyHost — document replacement', () => {
  it('setDoc replaces the whole document, never appending', () => {
    const { host, parent } = mountHost()
    host.setDoc('first')
    host.setDoc('second')

    expect(docText(parent)).toBe('second')
    host.dispose()
  })
})

describe('ReadOnlyHost — disposal', () => {
  it('dispose() destroys the view and is idempotent', () => {
    const { host, parent } = mountHost()
    host.setDoc('text')
    expect(parent.querySelector('.cm-editor')).not.toBeNull()

    host.dispose()
    // EditorView.destroy removes its element from the document.
    expect(parent.querySelector('.cm-editor')).toBeNull()

    // A second dispose, and post-dispose setDoc/focus, are inert.
    host.dispose()
    host.setDoc('late')
    host.focus()
    expect(docText(parent)).toBe('')
  })

  it('aborting the signal destroys the view', () => {
    const { host, parent, controller } = mountHost()
    host.setDoc('text')
    expect(parent.querySelector('.cm-editor')).not.toBeNull()

    controller.abort()
    expect(parent.querySelector('.cm-editor')).toBeNull()

    // A second abort and an explicit dispose afterwards are no-ops.
    controller.abort()
    host.dispose()
  })

  it('a signal already aborted at mount mounts nothing and leaks no view', () => {
    const host = new ReadOnlyHost()
    const parent = document.createElement('div')
    document.body.append(parent)
    const controller = new AbortController()
    controller.abort()

    host.mount(parent, controller.signal)
    expect(parent.querySelector('.cm-editor')).toBeNull()
    host.dispose()
  })
})

describe('EditableHost — the editable mode of the same host', () => {
  it('takes typed input the read-only mode refuses, and reports the document', () => {
    const host = new EditableHost()
    const parent = document.createElement('div')
    document.body.append(parent)
    const controller = new AbortController()
    host.mount(parent, controller.signal)
    host.setDoc('start')

    const contentEl = parent.querySelector('.cm-content') as HTMLElement
    expect(contentEl.getAttribute('contenteditable')).toBe('true')
    expect(contentEl.getAttribute('aria-readonly')).toBe(null)

    // What the read-only mode's own test proves cannot happen: keystrokes
    // reach an editable region. jsdom does not emulate contenteditable
    // input, so the assertion is the structural one above plus the
    // document seam the surface actually saves from.
    expect(host.doc()).toBe('start')
    expect(docText(parent)).toBe('start')

    host.dispose()
  })

  it('reports every document change to the caller, so a draft never lags the field', () => {
    const seen: string[] = []
    const host = new EditableHost()
    const parent = document.createElement('div')
    document.body.append(parent)
    const controller = new AbortController()
    host.mount(parent, controller.signal, [], (text) => seen.push(text))

    host.setDoc('one')
    host.setDoc('two')

    // setDoc is a document change like any other, and the listener is how
    // the surface's draft signal follows the field: a surface that only
    // read doc() on Save would write what it last set rather than what the
    // person typed.
    expect(seen).toEqual(['one', 'two'])
    host.dispose()
  })

  it('doc() is empty before mount and after dispose, never a stale document', () => {
    const host = new EditableHost()
    expect(host.doc()).toBe('')
    const parent = document.createElement('div')
    document.body.append(parent)
    const controller = new AbortController()
    host.mount(parent, controller.signal)
    host.setDoc('text')
    host.dispose()
    expect(host.doc()).toBe('')
  })
})

describe('EditableHost — what an editable field must already have', () => {
  it('binds Enter, undo and redo: a field where Return does nothing is not a field', () => {
    // The read-only modes need no keymap at all, so the editable one has to
    // bring the editing essentials itself. Enter is the one that bites: with
    // no binding, a Return inside a dialog falls through to the dialog's own
    // submit and the person's newline saves the form instead.
    const host = new EditableHost()
    const parent = document.createElement('div')
    document.body.append(parent)
    const controller = new AbortController()
    host.mount(parent, controller.signal)

    const bound = (key: string): boolean => {
      const contentEl = parent.querySelector('.cm-content') as HTMLElement
      const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
      contentEl.dispatchEvent(event)
      return event.defaultPrevented
    }
    expect(bound('Enter')).toBe(true)
    host.dispose()
  })
})

describe('wrapping — a line that outruns the box, in both modes', () => {
  const mount = (host: EditableHost | ReadOnlyHost): HTMLElement => {
    const parent = document.createElement('div')
    document.body.append(parent)
    host.mount(parent, new AbortController().signal)
    return parent
  }

  // A FIELD SOMEBODY TYPES PROSE INTO WRAPS. The snippet body and a note are
  // sentences, and CM6 does not wrap unless it is told to — so a pasted
  // paragraph became one line running off the right edge of the dialog, with
  // the text clipped at the frame and a horizontal scrollbar under it
  // (nocx-dn33v). Asserted through the class `EditorView.lineWrapping`
  // installs, which is the thing that carries `white-space: pre-wrap` into the
  // content: jsdom does no layout, so a width measurement here would assert
  // nothing at all.
  it('an editable host holding prose wraps', () => {
    const host = new EditableHost()
    const parent = mount(host)
    expect(parent.querySelector('.cm-content')?.classList.contains('cm-lineWrapping')).toBe(true)
    host.dispose()
  })

  // AND AN EDITABLE HOST HOLDING CODE DOES NOT — which is the half that was
  // missing, because wrapping was decided by the MODE and every editable
  // surface got the prose answer. The API request body is a document: one long
  // value came back as a stack of rows whose continuations sat against the line
  // numbers, and nothing inside the editor could be moved sideways, so the pane
  // around it was the only scrollbar and it moved the whole surface
  // (nocx-kdawd). `code` is the same answer the read-only modes already give.
  it('an editable host holding code does not, so a long line is scrolled instead', () => {
    const host = new EditableHost('code')
    const parent = mount(host)
    expect(parent.querySelector('.cm-content')?.classList.contains('cm-lineWrapping')).toBe(false)
    host.dispose()
  })

  // The keymap is not the wrapping decision's to lose. A host asked for `code`
  // is still a FIELD — Enter must break the line rather than reach the dialog
  // behind it — and this is the pairing the two halves of the constructor could
  // silently stop honouring.
  it('an editable host holding code is still editable and still binds Enter', () => {
    const host = new EditableHost('code')
    const parent = mount(host)
    const contentEl = parent.querySelector('.cm-content') as HTMLElement
    expect(contentEl.getAttribute('contenteditable')).toBe('true')
    const event = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    contentEl.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    host.dispose()
  })

  // AND A READ-ONLY ONE DOES NOT, deliberately. Its two surfaces are the file
  // viewer and the git diff, where a line is a LINE: wrapping one silently
  // renumbers what the reader is looking at against what the file says, and a
  // diff whose rows no longer align row-for-row has stopped being a diff. Same
  // reasoning the terminal's frozen output is held to (nocx-juau) — long
  // content is reached by scrolling sideways.
  it('a read-only host does not', () => {
    const host = new ReadOnlyHost()
    const parent = mount(host)
    expect(parent.querySelector('.cm-content')?.classList.contains('cm-lineWrapping')).toBe(false)
    host.dispose()
  })
})
