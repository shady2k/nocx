// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { IntentInput, type SessionIntent } from './intent-input'
import { NATIVE_RESTORE_COMMAND } from './native-mode'

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
  Object.defineProperty(event, 'clipboardData', {
    value: { getData: () => text, types: text === '' ? [] : ['text/plain'] },
  })
  return event
}

/** A clipboard carrying an image and no text at all — the shape the browser
 *  hands a `paste` event when the person copied a screenshot (nocx-1ebrs.14). */
function imagePasteEvent(): Event {
  const event = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'clipboardData', {
    value: { getData: () => '', types: ['image/png'] },
  })
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

  // THE NUMERIC KEYPAD'S ENTER IS A KEY OF ITS OWN (nocx-1ebrs.32). A browser
  // reports it as key "Enter" with code "NumpadEnter" — measured in both
  // engines, and the reason the composer's `e.key === 'Enter'` was never the
  // broken seam. The vocabulary below had no name for that CODE, so keyIntentOf
  // returned null and the keydown was dropped here: the program received
  // nothing, which is exactly "does nothing" and not "sends the wrong byte".
  //
  // It is NAMED rather than folded into "enter" because what the two keys are
  // worth is the program's: once a program asks for the keypad's own sequences
  // (application keypad mode, DECNKM) the keypad's Enter is SS3 M and Return is
  // still CR, and that decision needs the key's identity. A client that mapped
  // it to "enter" would send CR where the program asked for the keypad's own
  // sequence (ADR-0066).
  it('names the keypad’s Enter, rather than dropping it as an unknown key', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('Enter', { key: 'Enter' }))
    input.element.dispatchEvent(keydown('NumpadEnter', { key: 'Enter' }))

    expect(intents).toEqual([
      { kind: 'key', payload: 'enter' },
      { kind: 'key', payload: 'numpadenter' },
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

describe('a wheel gesture is counted in lines, not in events (nocx-1ebrs.26)', () => {
  /** A pane whose row pitch is 17px, as a real one reports through the
   *  painter's accepted metric. */
  function wheelPane(): { surface: HTMLElement; intents: SessionIntent[] } {
    const intents: SessionIntent[] = []
    const surface = document.createElement('div')
    document.body.append(surface)
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      surface,
      cellAt: () => ({ x: 2, y: 3 }),
      mouseTracking: () => true,
      lineHeight: () => 17,
    })
    document.body.append(input.element)
    live.push(input)
    return { surface, intents }
  }

  const wheel = (deltaY: number, extra: WheelEventInit = {}): WheelEvent =>
    new WheelEvent('wheel', { deltaY, clientX: 10, clientY: 20, cancelable: true, ...extra })

  it('reports one step per line height of a stream of small pixel deltas', () => {
    // A trackpad at 4px a frame: 17 events is 68px, which is FOUR rows. One
    // report per event told the program the person scrolled seventeen times.
    const { surface, intents } = wheelPane()
    for (let i = 0; i < 17; i++) surface.dispatchEvent(wheel(4))
    expect(intents).toHaveLength(4)
    expect(intents.every((intent) => intent.payload === 'press wheel-down 2 3')).toBe(true)
  })

  it('carries the remainder into the next event rather than discarding it', () => {
    // Three deltas of 10px are 30px: one row reported, 13px carried. A second
    // 10px then crosses the line, so the pair is two reports — a fix that
    // rounded each event to zero would report one, and one that rounded up
    // would report three.
    const { surface, intents } = wheelPane()
    surface.dispatchEvent(wheel(10))
    surface.dispatchEvent(wheel(10))
    surface.dispatchEvent(wheel(10))
    expect(intents).toHaveLength(1)
    surface.dispatchEvent(wheel(10))
    expect(intents).toHaveLength(2)
  })

  it('counts a momentum tail as one gesture: many shrinking deltas, few reports', () => {
    // The tail after the fingers lift: 20px, then 12, 9, 6, 4, 2 — 53px of a
    // decaying curve, which is three rows and not six reports.
    const { surface, intents } = wheelPane()
    for (const delta of [20, 12, 9, 6, 4, 2]) surface.dispatchEvent(wheel(delta))
    expect(intents).toHaveLength(3)
  })

  it('reports both directions, and a delta of a line is already a line', () => {
    const { surface, intents } = wheelPane()
    surface.dispatchEvent(wheel(-17))
    surface.dispatchEvent(wheel(17))
    expect(intents.map((intent) => intent.payload)).toEqual([
      'press wheel-up 2 3',
      'press wheel-down 2 3',
    ])

    // DOM_DELTA_LINE: one unit IS one row, so it must not be divided by the
    // pitch again (that would report a line-mode wheel at a seventeenth of
    // its speed).
    intents.length = 0
    for (let i = 0; i < 3; i++) {
      surface.dispatchEvent(wheel(1, { deltaMode: WheelEvent.DOM_DELTA_LINE }))
    }
    expect(intents).toHaveLength(3)
  })

  it('spends no leftover remainder on the next gesture', () => {
    // 8px of a 17px row is left over when the pointer leaves. If it survived,
    // the next gesture's first 9px would report a row the person never moved.
    const { surface, intents } = wheelPane()
    surface.dispatchEvent(wheel(8))
    surface.dispatchEvent(new PointerEvent('pointerleave', {}))
    surface.dispatchEvent(wheel(8))
    expect(intents).toHaveLength(0)
  })

  it('does not divide a delta from a program that is not tracking the mouse', () => {
    const intents: SessionIntent[] = []
    const surface = document.createElement('div')
    document.body.append(surface)
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      surface,
      cellAt: () => ({ x: 2, y: 3 }),
      mouseTracking: () => false,
      lineHeight: () => 17,
    })
    document.body.append(input.element)
    live.push(input)
    surface.dispatchEvent(wheel(4))
    expect(intents).toEqual([])
  })
})

describe('the wheel has one owner before any delta is accumulated (nocx-tdncs)', () => {
  function wheelPane(localScrolling: boolean | 'native' = true) {
    const intents: SessionIntent[] = []
    const localScrolls: Array<[number, number]> = []
    const surface = document.createElement('div')
    const grid = document.createElement('div')
    const history = document.createElement('div')
    surface.append(grid, history)
    document.body.append(surface)
    Object.defineProperty(surface, 'clientHeight', { value: 170 })
    const state = {
      tracking: true,
      buffer: 'normal' as 'normal' | 'alternate',
      hit: true,
      lineHeight: 17,
    }
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      surface,
      cellAt: () => (state.hit ? { x: 2, y: 3 } : null),
      mouseTracking: () => state.tracking,
      bufferKind: () => state.buffer,
      isLiveTarget: (target) => target instanceof Node && grid.contains(target),
      scrollLocal: localScrolling
        ? (x, y, overLive) => {
            if (localScrolling === 'native') return false
            localScrolls.push([x, y])
            const scroller = overLive ? grid : history
            scroller.scrollLeft += x
            scroller.scrollTop += y
            return true
          }
        : undefined,
      lineHeight: () => state.lineHeight,
    })
    document.body.append(input.element)
    live.push(input)
    function wheel(deltaY: number, init: WheelEventInit = {}, target = grid): WheelEvent {
      const event = new WheelEvent('wheel', {
        deltaY,
        clientX: 10,
        clientY: 20,
        bubbles: true,
        cancelable: true,
        ...init,
      })
      target.dispatchEvent(event)
      return event
    }
    return { input, surface, grid, history, intents, localScrolls, state, wheel }
  }

  it('retires fractions when the accepted input binding changes between wheel events', () => {
    const { input, wheel, intents } = wheelPane()
    wheel(10)
    input.resetWheel()
    wheel(7)
    expect(intents).toEqual([])
    wheel(10)
    expect(intents).toEqual([{ kind: 'mouse', payload: 'press wheel-down 2 3' }])
  })

  it('lets a local owner scroll background without treating it as a reporting grid', () => {
    const surface = document.createElement('div')
    const grid = document.createElement('div')
    const archive = document.createElement('div')
    surface.append(grid, archive)
    document.body.append(surface)
    surface.scrollTop = 100
    const intents: SessionIntent[] = []
    const input = new IntentInput({
      surface,
      emit: (intent) => intents.push(intent),
      cellAt: () => ({ x: 2, y: 3 }),
      mouseTracking: () => true,
      isLiveTarget: (target) => target === grid,
      scrollLocal: (_x, y, overLive, target) => {
        if (overLive || target !== surface) return false
        surface.scrollTop += y
        return true
      },
    })
    live.push(input)
    const backgroundWheel = new WheelEvent('wheel', {
      deltaY: -40,
      bubbles: true,
      cancelable: true,
    })
    surface.dispatchEvent(backgroundWheel)
    expect(surface.scrollTop).toBe(60)
    expect(backgroundWheel.defaultPrevented).toBe(true)
    const archiveWheel = new WheelEvent('wheel', {
      deltaY: -40,
      bubbles: true,
      cancelable: true,
    })
    archive.dispatchEvent(archiveWheel)
    expect(surface.scrollTop).toBe(60)
    expect(archiveWheel.defaultPrevented).toBe(false)
    expect(intents).toEqual([])
  })

  it('leaves nested archived scrolling native when the local owner declines the event', () => {
    const { wheel, history, intents } = wheelPane('native')
    const event = wheel(40, {}, history)
    expect(event.defaultPrevented).toBe(false)
    expect(intents).toEqual([])
    expect(history.scrollTop).toBe(0)
  })

  // Reproduced failures: Shift and historical targets reached the program,
  // and sub-line captured deltas still reached the browser's scroller.
  it.each(['normal', 'alternate'] as const)(
    'scrolls Shift-wheel locally exactly once on the %s buffer, never as app input',
    (buffer) => {
      const { state, wheel, grid, history, intents, localScrolls } = wheelPane()
      state.buffer = buffer
      const event = wheel(-2, {
        deltaX: 1,
        deltaMode: WheelEvent.DOM_DELTA_LINE,
        shiftKey: true,
        ctrlKey: true,
      })
      expect(localScrolls).toEqual([[17, -34]])
      expect(grid.scrollLeft).toBe(17)
      expect(grid.scrollTop).toBe(-34)
      expect(history.scrollTop).toBe(0)
      expect(intents).toEqual([])
      expect(event.defaultPrevented).toBe(true)
    },
  )

  it('keeps historical deltas local and clears app residuals on both target transitions', () => {
    const { wheel, grid, history, intents, localScrolls } = wheelPane()
    wheel(10, { deltaX: 10 })
    const historical = wheel(100, { deltaX: 100 }, history)
    wheel(7, { deltaX: 7 })
    expect(localScrolls).toEqual([[100, 100]])
    expect(history.scrollLeft).toBe(100)
    expect(history.scrollTop).toBe(100)
    expect(grid.scrollTop).toBe(0)
    expect(intents).toEqual([])
    expect(historical.defaultPrevented).toBe(true)
    wheel(10, { deltaX: 10 })
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press wheel-down 2 3' },
      { kind: 'mouse', payload: 'press wheel-right 2 3' },
    ])
  })

  it('captures every fractional app event immediately and counts both axes independently', () => {
    const { wheel, intents, localScrolls } = wheelPane()
    expect(wheel(10, { deltaX: -8 }).defaultPrevented).toBe(true)
    expect(intents).toEqual([])
    expect(wheel(7, { deltaX: -8 }).defaultPrevented).toBe(true)
    expect(intents).toEqual([{ kind: 'mouse', payload: 'press wheel-down 2 3' }])
    expect(wheel(0, { deltaX: -1 }).defaultPrevented).toBe(true)
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press wheel-down 2 3' },
      { kind: 'mouse', payload: 'press wheel-left 2 3' },
    ])
    expect(localScrolls).toEqual([])
  })

  it('does not accumulate outside the live cell grid and forgets both old residuals', () => {
    const { state, wheel, intents } = wheelPane()
    wheel(10, { deltaX: 10 })
    state.hit = false
    wheel(100, { deltaX: 100 })
    state.hit = true
    wheel(7, { deltaX: 7 })
    expect(intents).toEqual([])
    wheel(10, { deltaX: 10 })
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press wheel-down 2 3' },
      { kind: 'mouse', payload: 'press wheel-right 2 3' },
    ])
  })

  it('leaves primary non-reporting wheel local but forwards alternate wheel for mode 1007', () => {
    const { state, wheel, intents, localScrolls } = wheelPane()
    state.tracking = false
    expect(wheel(17).defaultPrevented).toBe(true)
    expect(localScrolls).toEqual([[0, 17]])
    expect(intents).toEqual([])
    state.buffer = 'alternate'
    expect(wheel(-17, { deltaX: 17 }).defaultPrevented).toBe(true)
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press wheel-up 2 3' },
      { kind: 'mouse', payload: 'press wheel-right 2 3' },
    ])
    expect(localScrolls).toEqual([[0, 17]])
  })

  it('preserves native local scrolling when no local collaborator is supplied', () => {
    const { state, wheel, history, intents, localScrolls } = wheelPane(false)
    expect(wheel(17, { shiftKey: true }).defaultPrevented).toBe(false)
    expect(wheel(17, {}, history).defaultPrevented).toBe(false)
    state.tracking = false
    expect(wheel(17).defaultPrevented).toBe(false)
    expect(localScrolls).toEqual([])
    expect(intents).toEqual([])
  })

  it('normalizes page and line units once for each owner', () => {
    const { wheel, intents, localScrolls } = wheelPane()
    wheel(-0.2, { deltaX: 0.1, deltaMode: WheelEvent.DOM_DELTA_PAGE })
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press wheel-up 2 3' },
      { kind: 'mouse', payload: 'press wheel-up 2 3' },
      { kind: 'mouse', payload: 'press wheel-right 2 3' },
    ])
    wheel(-0.2, { deltaX: 0.1, deltaMode: WheelEvent.DOM_DELTA_PAGE, shiftKey: true })
    wheel(2, { deltaX: -1, deltaMode: WheelEvent.DOM_DELTA_LINE, shiftKey: true })
    expect(localScrolls).toEqual([
      [17, -34],
      [-17, 34],
    ])
  })

  it.each(['tracking', 'buffer', 'modifiers', 'delta mode', 'local owner', 'pointerleave'])(
    'discards both residual axes when %s changes',
    (transition) => {
      const { state, surface, wheel, intents } = wheelPane()
      wheel(10, { deltaX: 10 })
      let init: WheelEventInit = { deltaX: 7 }
      if (transition === 'tracking') {
        state.buffer = 'alternate'
        wheel(0) // Establish alternate/reporting ownership before the transition.
        wheel(10, { deltaX: 10 })
        state.tracking = false
      } else if (transition === 'buffer') {
        state.buffer = 'alternate'
      } else if (transition === 'modifiers') {
        init = { ...init, altKey: true }
      } else if (transition === 'delta mode') {
        init = { deltaX: 7 / 17, deltaMode: WheelEvent.DOM_DELTA_LINE }
      } else if (transition === 'local owner') {
        wheel(1, { shiftKey: true })
      } else {
        surface.dispatchEvent(new PointerEvent('pointerleave'))
      }
      wheel(transition === 'delta mode' ? 7 / 17 : 7, init)
      expect(intents).toEqual([])
      wheel(
        transition === 'delta mode' ? 10 / 17 : 10,
        transition === 'delta mode' ? { ...init, deltaX: 10 / 17 } : { ...init, deltaX: 10 },
      )
      const prefix = transition === 'modifiers' ? 'Alt+' : ''
      expect(intents).toEqual([
        { kind: 'mouse', payload: `${prefix}press wheel-down 2 3` },
        { kind: 'mouse', payload: `${prefix}press wheel-right 2 3` },
      ])
    },
  )

  it('preserves non-Shift wheel modifiers in the existing key prefix order', () => {
    const { wheel, intents } = wheelPane()
    wheel(-17, { deltaX: -17, ctrlKey: true, altKey: true, metaKey: true })
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'Ctrl+Alt+Super+press wheel-up 2 3' },
      { kind: 'mouse', payload: 'Ctrl+Alt+Super+press wheel-left 2 3' },
    ])
  })

  it('preserves horizontal input without a committed row pitch', () => {
    const { state, wheel, intents } = wheelPane()
    state.lineHeight = 0
    expect(wheel(-1, { deltaX: 1 }).defaultPrevented).toBe(true)
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press wheel-up 2 3' },
      { kind: 'mouse', payload: 'press wheel-right 2 3' },
    ])
  })
})

describe('mouse intent follows the accepted backend tracking mode', () => {
  it('leaves ordinary clicks and wheel events to selection and scrolling', () => {
    const intents: SessionIntent[] = []
    const surface = document.createElement('div')
    document.body.append(surface)
    let tracking = false
    let selectionSawPress = false
    surface.addEventListener('pointerdown', () => {
      selectionSawPress = true
    })
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      surface,
      cellAt: () => ({ x: 2, y: 3 }),
      mouseTracking: () => tracking,
    })
    document.body.append(input.element)
    live.push(input)

    surface.dispatchEvent(
      new MouseEvent('pointerdown', { button: 0, buttons: 1, clientX: 10, clientY: 20 }),
    )
    const wheel = new WheelEvent('wheel', { deltaY: 1, clientX: 10, clientY: 20, cancelable: true })
    surface.dispatchEvent(wheel)

    expect(intents).toEqual([])
    expect(selectionSawPress).toBe(true)
    expect(wheel.defaultPrevented).toBe(false)

    tracking = true
    surface.dispatchEvent(
      new MouseEvent('pointerdown', { button: 0, buttons: 1, clientX: 10, clientY: 20 }),
    )
    surface.dispatchEvent(
      new MouseEvent('pointerup', { button: 0, buttons: 0, clientX: 10, clientY: 20 }),
    )
    const trackedWheel = new WheelEvent('wheel', {
      deltaY: -1,
      clientX: 10,
      clientY: 20,
      cancelable: true,
    })
    surface.dispatchEvent(trackedWheel)

    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press left 2 3' },
      { kind: 'mouse', payload: 'release left 2 3' },
      { kind: 'mouse', payload: 'press wheel-up 2 3' },
    ])
    expect(trackedWheel.defaultPrevented).toBe(true)
  })

  it('reports modified pointer actions without taking selection away from the DOM', () => {
    const intents: SessionIntent[] = []
    const surface = document.createElement('div')
    document.body.append(surface)
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      surface,
      mouseTracking: () => true,
      cellAt: () => ({ x: 2, y: 3 }),
    })
    live.push(input)
    const events = ['pointerdown', 'pointermove', 'pointerup'].map(
      (type) =>
        new MouseEvent(type, {
          button: 0,
          buttons: type === 'pointerup' ? 0 : 1,
          ctrlKey: true,
          altKey: true,
          shiftKey: true,
          metaKey: true,
          cancelable: true,
        }),
    )
    const selected: string[] = []
    for (const event of events) {
      surface.addEventListener(event.type, (seen) => {
        if (!seen.defaultPrevented) selected.push(seen.type)
      })
      surface.dispatchEvent(event)
    }
    expect(intents).toEqual([
      { kind: 'mouse', payload: 'Ctrl+Alt+Shift+Super+press left 2 3' },
      { kind: 'mouse', payload: 'Ctrl+Alt+Shift+Super+motion left 2 3' },
      { kind: 'mouse', payload: 'Ctrl+Alt+Shift+Super+release left 2 3' },
    ])
    expect(selected).toEqual(['pointerdown', 'pointermove', 'pointerup'])
    expect(events.every((event) => !event.defaultPrevented)).toBe(true)
  })

  it('clears a held drag when tracking turns off before release', () => {
    const intents: SessionIntent[] = []
    const surface = document.createElement('div')
    document.body.append(surface)
    let tracking = true
    const input = new IntentInput({
      emit: (intent) => intents.push(intent),
      surface,
      cellAt: () => ({ x: 2, y: 3 }),
      mouseTracking: () => tracking,
    })
    document.body.append(input.element)
    live.push(input)

    surface.dispatchEvent(
      new MouseEvent('pointerdown', { button: 0, buttons: 1, clientX: 10, clientY: 20 }),
    )
    tracking = false
    surface.dispatchEvent(
      new MouseEvent('pointerup', { button: 0, buttons: 0, clientX: 10, clientY: 20 }),
    )
    tracking = true
    surface.dispatchEvent(
      new MouseEvent('pointermove', { button: 0, buttons: 0, clientX: 10, clientY: 20 }),
    )

    expect(intents).toEqual([
      { kind: 'mouse', payload: 'press left 2 3' },
      { kind: 'mouse', payload: 'motion none 2 3' },
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

  // THE SHIPPED WEBVIEW'S COMMIT (nocx-1ebrs.38): WebKit-family engines end
  // the composition FIRST and then deliver the committed text a SECOND time,
  // as a plain `input` — inputType `insertText`, isComposing already false,
  // data the commit itself. No inputType filter names that shape, so before
  // the fix the pane sent the commit twice and the TUI echoed the double.
  // One commit is one text intent, whatever order the engine delivers its
  // events in.
  it('sends the commit once when its echo follows compositionend as plain insertText', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(composition('compositionstart', ''))
    input.element.dispatchEvent(keydown('KeyF', { key: 'ф', isComposing: true }))
    input.element.dispatchEvent(inputEvent('ф', 'insertCompositionText', true))
    input.element.dispatchEvent(composition('compositionend', 'ф'))
    // The echo: no isComposing, no composition inputType — only the text.
    input.element.dispatchEvent(inputEvent('ф', 'insertText', false))

    expect(intents).toEqual([{ kind: 'text', payload: 'ф' }])
  })

  // An input method that commits BETWEEN the keydown and the end reports the
  // committing keydown as a plain character (isComposing false on both
  // sides of the wire it sees). The keydown is still the composition's, and
  // the text that follows it is still the commit's echo — the commit goes
  // out once, from compositionend.
  it('sends the commit once when the committing keydown reports no isComposing', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(composition('compositionstart', ''))
    input.element.dispatchEvent(keydown('KeyF', { key: 'ф' }))
    input.element.dispatchEvent(composition('compositionend', 'ф'))
    input.element.dispatchEvent(inputEvent('ф', 'insertText', false))

    expect(intents).toEqual([{ kind: 'text', payload: 'ф' }])
  })

  // The echo window is not a blanket: the first real keystroke after a
  // commit produces text of its own, even the same letter again. A person
  // who commits ф and then types ф on the plain layout gets both.
  it('still sends a plain keystroke that repeats the committed letter', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(composition('compositionstart', ''))
    input.element.dispatchEvent(composition('compositionend', 'ф'))
    input.element.dispatchEvent(inputEvent('ф', 'insertText', false)) // the echo
    input.element.dispatchEvent(keydown('KeyF', { key: 'ф' }))
    input.element.dispatchEvent(inputEvent('ф', 'insertText', false)) // the person's own ф

    expect(intents).toEqual([
      { kind: 'text', payload: 'ф' },
      { kind: 'text', payload: 'ф' },
    ])
  })

  // A PLAIN CYRILLIC KEYSTROKE (nocx-1ebrs.38's own shape, typed on the
  // layout rather than through an input method): the keydown resolves to a
  // character, so the TEXT carries it and no key intent exists to travel
  // beside it. The pty receives these bytes once, from one intent.
  it('sends a plain Cyrillic keystroke as text, never as a key and a text', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(keydown('KeyA', { key: 'ф' }))
    input.element.dispatchEvent(inputEvent('ф', 'insertText'))

    expect(intents).toEqual([{ kind: 'text', payload: 'ф' }])
  })
})

describe('native-mode restore remains structured input', () => {
  it('keeps the restore command free of encoded Enter bytes', () => {
    expect(NATIVE_RESTORE_COMMAND).toBe('__nocx_native_mode')
    expect(NATIVE_RESTORE_COMMAND).not.toMatch(/[\r\n]$/)
  })
})

describe('paste, focus and the element itself', () => {
  // AN IMAGE-ONLY CLIPBOARD IS STILL A PASTE THE PROGRAM IS OWED
  // (nocx-1ebrs.14). The terminal can forward no text, and the program has its
  // own way of reading the clipboard — what it cannot do is learn that the
  // person pasted at all. Every terminal that makes this work tells the program
  // SOMETHING: the paste arrives with an empty body, and a program in bracketed
  // paste mode receives the bracket pair with nothing between them. The client
  // still decodes nothing and encodes nothing (ADR-0065): it reports that a
  // paste happened.
  it('sends an empty paste when the clipboard carries an image and no text', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(imagePasteEvent())

    expect(intents).toEqual([{ kind: 'paste', payload: '' }])
  })

  it('sends nothing at all for an empty text clipboard', () => {
    // The other empty case, and it is a different fact: nothing was copied, so
    // there is no paste to announce and an empty bracket pair on every stray
    // Ctrl+V would be input the person did not ask for.
    const { input, intents } = makeInput()

    input.element.dispatchEvent(pasteEvent(''))

    expect(intents).toEqual([])
  })

  // A paste is its own kind and not text: the runtime wraps it for bracketed
  // paste when the program asked for that (mode 2004), and the body arrives
  // verbatim (ADR-0065). The client decides neither.
  it('sends a paste as a paste, and keeps the body out of the element', () => {
    const { input, intents } = makeInput()

    input.element.dispatchEvent(pasteEvent('one\ntwo'))

    expect(intents).toEqual([{ kind: 'paste', payload: 'one\ntwo' }])
    expect(input.element.value).toBe('')
  })

  it.each([
    ['Ctrl+V', { ctrlKey: true }],
    ['Meta+V', { metaKey: true }],
  ])('lets %s reach the browser paste event instead of the terminal', (_chord, modifiers) => {
    const { input, intents } = makeInput()

    const key = keydown('KeyV', { key: 'v', ...modifiers })
    input.element.dispatchEvent(key)

    expect(key.defaultPrevented).toBe(false)
    expect(intents).toEqual([])

    input.element.dispatchEvent(pasteEvent('clipboard text'))

    expect(intents).toEqual([{ kind: 'paste', payload: 'clipboard text' }])
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
