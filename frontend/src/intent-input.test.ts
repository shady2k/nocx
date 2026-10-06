// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { IntentInput, type SessionIntent } from './intent-input'

// The pane's input element is where a person's actions become SESSION INTENT
// (nocx-zg3k3.3.1): a physical key, committed text, a paste, a pointer event, a
// focus change. Nothing here encodes bytes — what a key means against the
// program's modes is the session runtime's decision (ADR-0066) — so these tests
// assert the INTENT, and the vocabulary they assert is the one
// internal/sessionruntime/intent.go parses.

const live: IntentInput[] = []

function makeInput(): { input: IntentInput; intents: SessionIntent[] } {
  const intents: SessionIntent[] = []
  const input = new IntentInput({
    emit: (intent) => intents.push(intent),
  })
  document.body.append(input.element)
  live.push(input)
  return { input, intents }
}

afterEach(() => {
  for (const input of live.splice(0)) input.destroy()
  document.body.innerHTML = ''
})

/** A keydown as a browser delivers it. `altGraph` is passed through a stub
 *  because the modifier state is a method rather than an init field. */
function keydown(
  code: string,
  init: KeyboardEventInit & { altGraph?: boolean } = {},
): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { code, bubbles: true, cancelable: true, ...init })
  if (init.altGraph) {
    Object.defineProperty(event, 'getModifierState', {
      value: (key: string) => key === 'AltGraph',
    })
  }
  return event
}

/** An input event carrying what the browser committed. Built by hand: jsdom's
 *  InputEvent does not carry every field this module reads. */
function inputEvent(data: string, inputType: string, isComposing = false): Event {
  const event = new Event('input', { bubbles: true })
  Object.defineProperty(event, 'data', { value: data })
  Object.defineProperty(event, 'inputType', { value: inputType })
  Object.defineProperty(event, 'isComposing', { value: isComposing })
  return event
}

function pasteEvent(text: string): Event {
  const event = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'clipboardData', { value: { getData: () => text } })
  return event
}

function composition(
  type: 'compositionstart' | 'compositionupdate' | 'compositionend',
  data: string,
): Event {
  const event = new CompositionEvent(type, { data, bubbles: true })
  return event
}

describe('a physical key and committed text stay distinguishable', () => {
  // The person typed a character. What the WIRE needs is the text — the key's
  // meaning against the program's modes is the runtime's decision — and the
  // keystroke must not also go out as a key, or the program receives the
  // character twice.
  it('sends a printable key as committed text, never as a key and a text', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('KeyA', { key: 'a' }))
    input.element.dispatchEvent(inputEvent('a', 'insertText'))

    expect(intents).toEqual([{ kind: 'text', payload: 'a' }])
  })

  // Shift is a modifier on the same physical key, and what the person produced
  // is the shifted character. Sending "Shift+a" as a key would ask the runtime
  // to derive a character from a key — exactly what the key/text split exists
  // to avoid.
  it('sends Shift+A as the character it produced', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('KeyA', { key: 'A', shiftKey: true }))
    input.element.dispatchEvent(inputEvent('A', 'insertText'))

    expect(intents).toEqual([{ kind: 'text', payload: 'A' }])
  })

  // A chord is not a character: the program is owed the KEY, with its
  // modifiers, so that it can decide what Ctrl+A means in its own modes.
  it('sends a control chord as a key, and never the text it may also produce', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('KeyA', { key: 'a', ctrlKey: true }))
    // Some platforms still commit a character for the chord; it must not
    // follow the key out, or the program sees Ctrl+A and an 'a'.
    input.element.dispatchEvent(inputEvent('\u0001', 'insertText'))

    expect(intents).toEqual([{ kind: 'key', payload: 'Ctrl+a' }])
  })

  it('names the control pad, the arrow pad and the function keys', () => {
    const { input, intents } = makeInput()

    for (const [code, key] of [
      ['Enter', 'Enter'],
      ['Tab', 'Tab'],
      ['Backspace', 'Backspace'],
      ['Escape', 'Escape'],
      ['ArrowUp', 'ArrowUp'],
      ['PageDown', 'PageDown'],
      ['Home', 'Home'],
      ['Delete', 'Delete'],
      ['F5', 'F5'],
    ] as const) {
      input.element.dispatchEvent(keydown(code, { key }))
    }

    expect(intents).toEqual([
      { kind: 'key', payload: 'enter' },
      { kind: 'key', payload: 'tab' },
      { kind: 'key', payload: 'backspace' },
      { kind: 'key', payload: 'escape' },
      { kind: 'key', payload: 'up' },
      { kind: 'key', payload: 'pagedown' },
      { kind: 'key', payload: 'home' },
      { kind: 'key', payload: 'delete' },
      { kind: 'key', payload: 'f5' },
    ])
  })

  // A PUNCTUATION KEY IS TWO DIFFERENT THINGS depending on the chord. Bare, it
  // is the character the layout produced, and text carries it — sending "quote"
  // as a key would ask the runtime to derive an apostrophe from a key name it
  // would then have to have a character for. Held with Ctrl it is a chord, and
  // only the program can say what Ctrl+' means in its own modes.
  it('sends punctuation as text when bare and as a key when chorded', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('Quote', { key: "'" }))
    input.element.dispatchEvent(inputEvent("'", 'insertText'))
    input.element.dispatchEvent(keydown('Quote', { key: "'", ctrlKey: true }))
    input.element.dispatchEvent(keydown('Space', { key: ' ', ctrlKey: true }))

    expect(intents).toEqual([
      { kind: 'text', payload: "'" },
      { kind: 'key', payload: 'Ctrl+quote' },
      { kind: 'key', payload: 'Ctrl+space' },
    ])
  })

  // Shift+Enter is its own chord and the program must be able to tell it from
  // Enter (nocx-nt70). It is the MODIFIER that distinguishes them, so the name
  // carries it.
  it('carries the modifiers that distinguish a chord from its bare key', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('Enter', { key: 'Enter', shiftKey: true }))
    input.element.dispatchEvent(
      keydown('F5', { key: 'F5', ctrlKey: true, shiftKey: true, altKey: true }),
    )

    expect(intents).toEqual([
      { kind: 'key', payload: 'Shift+enter' },
      { kind: 'key', payload: 'Ctrl+Alt+Shift+f5' },
    ])
  })

  // A key the wire has no name for is NOT guessed at. The runtime refuses an
  // unknown name rather than falling back to "send the payload" (ADR-0066), and
  // a client that invented a name would be the second encoder the bead exists
  // to remove.
  it('sends nothing for a physical key the vocabulary does not carry', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('F13', { key: 'F13' }))
    input.element.dispatchEvent(keydown('IntlBackslash', { key: '\\' }))

    expect(intents).toEqual([])
  })
})

describe('the layout owns the character, and the client does not', () => {
  // A chord the SURFACE owns is not input to the program: the consume
  // handler is consulted before this element reads the key at all, and a
  // consumed key opens whatever the surface opens and produces nothing here —
  // the same contract xterm's custom key handler had before it (design §10.1).
  it('consumes a key the surface owns, and emits nothing for it', () => {
    const intents: SessionIntent[] = []
    let chordHandled = false
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      consume: (e) => {
        if (e.code === 'KeyP' && e.altKey && e.metaKey) {
          chordHandled = true
          return true
        }
        return false
      },
    })
    live.push(input)

    input.element.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'p',
        code: 'KeyP',
        altKey: true,
        metaKey: true,
        bubbles: true,
        cancelable: true,
      }),
    )
    // A chord the pane owns must not also type a character: whatever the
    // browser commits for it (Option+P is 'π' on macOS) is swallowed.
    input.element.dispatchEvent(inputEvent('π', 'insertText'))

    expect(chordHandled).toBe(true)
    expect(intents).toEqual([])

    // A key the surface does not own still reaches the wire as itself.
    input.element.dispatchEvent(keydown('Enter', { key: 'Enter' }))
    expect(intents).toEqual([{ kind: 'key', payload: 'enter' }])
  })

  // The interrupt is a KEY and not a byte to be found inside a string
  // (nocx-zg3k3.3.1): the pane's held window watches for this payload, and the
  // runtime decides what Ctrl-C means to the program that is reading.
  it('sends Ctrl+C as the interrupt key the held window watches for', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('KeyC', { key: 'c', ctrlKey: true }))

    expect(intents).toEqual([{ kind: 'key', payload: 'Ctrl+c' }])
  })

  // A dead key produces no character on its own; the character arrives when the
  // next key composes it. Sending the dead key as a key would put a key the
  // program cannot see the layout for into its input.
  it('sends nothing for a dead key, and the composed character when it lands', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('Backquote', { key: 'Dead' }))
    input.element.dispatchEvent(keydown('KeyA', { key: 'à' }))
    input.element.dispatchEvent(inputEvent('à', 'insertText'))

    expect(intents).toEqual([{ kind: 'text', payload: 'à' }])
  })

  // AltGr is how a layout reaches its third level, and browsers report it as
  // ctrl+alt. Read as a chord it would swallow every AltGr character; read as
  // the layout's own key it is just text.
  it('treats an AltGr key as the character the layout produced, not as a chord', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(
      keydown('KeyQ', { key: '@', ctrlKey: true, altKey: true, altGraph: true }),
    )
    input.element.dispatchEvent(inputEvent('@', 'insertText'))

    expect(intents).toEqual([{ kind: 'text', payload: '@' }])
  })

  // Holding a key repeats it: the person is asking for it again. A control key
  // repeats as a key; a printable one repeats as text, which is the browser's
  // own repeat of the input event.
  it('repeats a held control key as further keys', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('ArrowUp', { key: 'ArrowUp' }))
    input.element.dispatchEvent(keydown('ArrowUp', { key: 'ArrowUp', repeat: true }))
    input.element.dispatchEvent(keydown('ArrowUp', { key: 'ArrowUp', repeat: true }))

    expect(intents).toEqual([
      { kind: 'key', payload: 'up' },
      { kind: 'key', payload: 'up' },
      { kind: 'key', payload: 'up' },
    ])
  })

  it('repeats a held printable key as further text', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('KeyA', { key: 'a' }))
    input.element.dispatchEvent(inputEvent('a', 'insertText'))
    input.element.dispatchEvent(keydown('KeyA', { key: 'a', repeat: true }))
    input.element.dispatchEvent(inputEvent('a', 'insertText'))

    expect(intents).toEqual([
      { kind: 'text', payload: 'a' },
      { kind: 'text', payload: 'a' },
    ])
  })
})

describe('composition commits the text exactly once', () => {
  // What the IME draws while it is composing is PROVISIONAL: the candidate
  // window's text is not what the person committed, and sending it would type
  // what they were still choosing. Only the committed text goes out, and once —
  // never the keystrokes and the final text both.
  it('sends nothing while composing and the committed text once', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(composition('compositionstart', ''))
    input.element.dispatchEvent(keydown('KeyN', { key: 'n', isComposing: true }))
    input.element.dispatchEvent(inputEvent('に', 'insertCompositionText', true))
    input.element.dispatchEvent(composition('compositionupdate', 'に'))
    input.element.dispatchEvent(inputEvent('日本', 'insertCompositionText', true))
    input.element.dispatchEvent(composition('compositionend', '日本'))
    input.element.dispatchEvent(inputEvent('日本', 'insertFromComposition'))

    expect(intents).toEqual([{ kind: 'text', payload: '日本' }])
  })

  // Escape during composition cancels it: the IME's own text was never the
  // person's, and an empty commit must produce no intent at all.
  it('sends nothing when a composition is cancelled', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(composition('compositionstart', ''))
    input.element.dispatchEvent(inputEvent('に', 'insertCompositionText', true))
    input.element.dispatchEvent(composition('compositionend', ''))

    expect(intents).toEqual([])
  })
})

describe('paste, focus and the element itself', () => {
  // A paste is its own kind and not text: the runtime wraps it for bracketed
  // paste when the program asked for that (mode 2004), and the body arrives
  // verbatim (ADR-0065). The client decides neither.
  it('sends a paste as a paste, and keeps the body out of the element', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(pasteEvent('one\ntwo'))

    expect(intents).toEqual([{ kind: 'paste', payload: 'one\ntwo' }])
    expect(input.element.value).toBe('')
  })

  it('reports focus gained and lost', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(new FocusEvent('focus'))
    input.element.dispatchEvent(new FocusEvent('blur'))

    expect(intents).toEqual([
      { kind: 'focus', payload: 'in' },
      { kind: 'focus', payload: 'out' },
    ])
  })

  // The element is an IME anchor, not a buffer: whatever the browser put in it
  // has already left as an intent, and a value that accumulated would move the
  // candidate window away from the caret it is anchored to.
  it('never accumulates what the browser put in it', () => {
    const { input, intents } = makeInput()

    input.element.value = 'xx'
    input.element.dispatchEvent(inputEvent('x', 'insertText'))

    expect(intents).toEqual([{ kind: 'text', payload: 'x' }])
    expect(input.element.value).toBe('')
  })

  it('stops listening once it is destroyed', () => {
    const { input, intents } = makeInput()
    input.destroy()

    input.element.dispatchEvent(keydown('Enter', { key: 'Enter' }))
    input.element.dispatchEvent(inputEvent('a', 'insertText'))

    expect(intents).toEqual([])
  })
})
