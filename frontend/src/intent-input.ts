/**
 * The pane's input element: what a person DID, as session intent
 * (nocx-zg3k3.3.1, design §6.1).
 *
 * THE CLIENT DOES NOT ENCODE BYTES. What a key means depends on the modes the
 * program set — DECCKM changes what an arrow key is worth, bracketed paste
 * changes what a paste is wrapped in — and since ADR-0065/0066 the one holder
 * of those modes is the session runtime beside the pty. So a keyboard, an IME,
 * a clipboard and a pointer are read here for WHAT THEY ARE (a physical key
 * with modifiers, committed text, a paste body, a pointer event, a focus
 * change) and the wire carries that
 * (contracts/session.intent.params.schema.json). There is no encoder in this
 * file and there must never be one: the spellings below are the payload
 * vocabulary internal/sessionruntime/intent.go parses, and a name it does not
 * carry is REFUSED there rather than guessed at here.
 *
 * WHY A REAL ELEMENT AND NOT A KEY LISTENER ON THE GRID. An IME draws its
 * candidate window against a focused editable element's caret. With no such
 * element the window lands at the top-left of the page, wherever the browser's
 * fallback is, instead of under the text the person is composing. So the pane
 * keeps one textarea, empty and invisible, positioned where the terminal caret
 * is, and every event below is read from it.
 *
 * PHYSICAL KEY OR COMMITTED TEXT — the split is the whole point of the kind
 * pair, and it is decided by one rule: a keydown that produces a character on
 * this layout is NOT sent as a key (the character arrives as text, which is
 * what the person saw), and a keydown that is a chord or a control key IS sent
 * as a key, with its modifiers, because only the program can decide what
 * Ctrl+A or Shift+Enter means in its own modes. The physical key is read from
 * `code` and never from `key`: with Option held, macOS reports the composed
 * character, so a key-based match would fail on exactly the layouts that need
 * it (the same reason snippets/chord.ts matches on `code`).
 */

/** One thing a person did, in the wire's own vocabulary and with no bytes in
 *  it. The payload's shape follows from the kind: a key name with modifier
 *  prefixes, committed UTF-8 text, a paste body, a pointer event in cells, or
 *  "in"/"out". */
import { logKeydown } from './log'
import { createTerminalInput } from './ui/terminal-input-element'

export type SessionIntent =
  | { readonly kind: 'key'; readonly payload: string }
  | { readonly kind: 'text'; readonly payload: string }
  | { readonly kind: 'paste'; readonly payload: string }
  | { readonly kind: 'mouse'; readonly payload: string }
  | { readonly kind: 'focus'; readonly payload: string }

/** Where a person's actions go. One callback for every kind, because the order
 *  they were produced in is part of what they mean — a Ctrl-C typed after a
 *  command is a different act from one typed before it. */
export type IntentSink = (intent: SessionIntent) => void

// ── the key vocabulary ──────────────────────────────────────────────────────
//
// ONE VOCABULARY, keyed by the PHYSICAL key (KeyboardEvent.code), partitioned
// so named non-printing keys can be recognised before mapping a code. It is
// the whole of what the wire will accept: internal/sessionruntime/intent.go's own
// keyNames, spelled identically. The names are lower case and the backend
// matches case-insensitively, so a name this table does not carry is a key the
// runtime has no identity for — the pane sends NOTHING for it rather than
// inventing a name, because a key-encoding table that fell back to something
// is exactly the defect ADR-0066 exists against.
const NON_PRINTING_KEY_NAMES: Readonly<Record<string, string>> = {
  ArrowLeft: 'left',
  ArrowRight: 'right',
  ArrowUp: 'up',
  ArrowDown: 'down',
  Home: 'home',
  End: 'end',
  PageUp: 'pageup',
  PageDown: 'pagedown',
  Insert: 'insert',
  Delete: 'delete',
  Backspace: 'backspace',
  Enter: 'enter',
  // THE KEYPAD'S ENTER IS ITS OWN KEY, not a second spelling of Return
  // (nocx-1ebrs.32). To a program that has asked for nothing the two are the
  // same CR, and to one that has asked for the keypad's own sequences
  // (application keypad mode, DECNKM) both keys keep their identity: the keypad
  // sends SS3 M where Return still sends CR. Folding them into one name here
  // would be the CLIENT deciding bytes before the program's modes have been
  // read, which is the defect ADR-0066 exists against — so the table says which
  // key was pressed and the runtime says what it is worth.
  NumpadEnter: 'numpadenter',
  Tab: 'tab',
  Escape: 'escape',
  F1: 'f1',
  F2: 'f2',
  F3: 'f3',
  F4: 'f4',
  F5: 'f5',
  F6: 'f6',
  F7: 'f7',
  F8: 'f8',
  F9: 'f9',
  F10: 'f10',
  F11: 'f11',
  F12: 'f12',
}

const KEY_NAMES: Readonly<Record<string, string>> = {
  ...NON_PRINTING_KEY_NAMES,
  Space: 'space',

  KeyA: 'a',
  KeyB: 'b',
  KeyC: 'c',
  KeyD: 'd',
  KeyE: 'e',
  KeyF: 'f',
  KeyG: 'g',
  KeyH: 'h',
  KeyI: 'i',
  KeyJ: 'j',
  KeyK: 'k',
  KeyL: 'l',
  KeyM: 'm',
  KeyN: 'n',
  KeyO: 'o',
  KeyP: 'p',
  KeyQ: 'q',
  KeyR: 'r',
  KeyS: 's',
  KeyT: 't',
  KeyU: 'u',
  KeyV: 'v',
  KeyW: 'w',
  KeyX: 'x',
  KeyY: 'y',
  KeyZ: 'z',

  Digit0: '0',
  Digit1: '1',
  Digit2: '2',
  Digit3: '3',
  Digit4: '4',
  Digit5: '5',
  Digit6: '6',
  Digit7: '7',
  Digit8: '8',
  Digit9: '9',

  Backquote: 'backquote',
  Backslash: 'backslash',
  BracketLeft: 'bracketleft',
  BracketRight: 'bracketright',
  Comma: 'comma',
  Equal: 'equal',
  Minus: 'minus',
  Period: 'period',
  Quote: 'quote',
  Semicolon: 'semicolon',
  Slash: 'slash',
}

/** The modifier prefixes, in the order this module writes them. The backend
 *  peels them off in any order, so the order is a readability choice and the
 *  spelling is not: ctrl, alt, shift, super are the four it knows. */
function modifierPrefix(e: KeyboardEvent): string {
  let prefix = ''
  if (e.ctrlKey) prefix += 'Ctrl+'
  if (e.altKey) prefix += 'Alt+'
  if (e.shiftKey) prefix += 'Shift+'
  if (e.metaKey) prefix += 'Super+'
  return prefix
}

/** Whether this keydown produces a CHARACTER on this layout, in which case the
 *  committed text carries it and no key does.
 *
 *  The test is `key.length === 1` under no chord: the browser has already
 *  resolved the layout — a dead key resolved to its composed letter, AltGr to
 *  its third-level symbol, Shift to the shifted character — and that resolved
 *  character IS what the person produced.
 *
 *  ALT IS NOT A CHORD BY ITSELF, deliberately: on macOS Option is a layout key
 *  (Option+a is 'å') and this keeps the behaviour xterm had before it, where
 *  Option composed rather than acting as Meta. AltGr is excluded earlier and
 *  separately, because browsers report it as ctrl+alt and reading that as a
 *  chord would swallow every third-level character on the layouts that have
 *  one. A "treat Option as Meta" preference would flip this predicate and
 *  belongs to whatever owns settings, not here. */
function producesCharacter(e: KeyboardEvent): boolean {
  return e.key.length === 1 && !e.ctrlKey && !e.metaKey
}

/** The key intent a keydown means, or null when it means none: a character
 *  (text carries it), a composition in progress (the IME owns it), an AltGr key
 *  (the layout's own), or a physical key the vocabulary has no name for. */
function keyIntentOf(e: KeyboardEvent): SessionIntent | null {
  if (e.isComposing) return null
  // AltGr arrives as ctrl+alt on every platform that has it; the layout's
  // character follows as text, exactly as a plain letter's does.
  if (e.getModifierState?.('AltGraph')) return null
  if (producesCharacter(e)) return null
  // A physical code does not prove a character was resolved. After a layout
  // switch WebKit can report key='Unidentified' with code='KeyT' before the
  // real keydown and text arrive (nocx-hss79). Only a named non-printing key
  // or a Ctrl/Meta chord may use its code as a key intent.
  if (
    !e.ctrlKey &&
    !e.metaKey &&
    !Object.prototype.hasOwnProperty.call(NON_PRINTING_KEY_NAMES, e.key)
  ) {
    return null
  }
  const name = KEY_NAMES[e.code]
  if (!name) return null
  return { kind: 'key', payload: modifierPrefix(e) + name }
}

/** A dead key is not a key the program should hear about: it produced nothing
 *  on its own, and the character it takes part in arrives as committed text. */
function isDeadKey(e: KeyboardEvent): boolean {
  return e.key === 'Dead'
}

/** The text a keydown would have inserted, for a key the browser delivered to
 *  nobody: `producesCharacter`'s answer, plus the AltGr character the layout
 *  resolves — that one is reported as ctrl+alt and is TEXT rather than a
 *  chord, the same reading keyIntentOf makes of it (nocx-1ebrs.66.1.19).
 *  Null when the key inserts nothing on its own: a composition in progress, a
 *  dead key, or a non-printing key. */
function textOfUnclaimedKey(e: KeyboardEvent): string | null {
  if (e.isComposing || isDeadKey(e)) return null
  if (e.getModifierState?.('AltGraph')) return e.key.length === 1 ? e.key : null
  return producesCharacter(e) ? e.key : null
}

export interface IntentInputOptions {
  /** Where intents go, in the order they were produced. */
  emit: IntentSink
  /** The surface's own chords, consulted before this element reads a key at
   *  all. Returning true CONSUMES the key: nothing is emitted and the
   *  browser's default is prevented — a chord the pane owns is not input to
   *  the program, and that is the same contract xterm's custom key handler
   *  had before it (design §10.1). The pane answers "is this the snippet
   *  chord" here because a chord is a product rule and this element is the
   *  kit's, and the kit does not know the product's shortcuts (AD-8). */
  consume?: (e: KeyboardEvent) => boolean
  /** The element pointer events are read from — the painted grid. Absent
   *  leaves the pointer alone, which is what a surface with no grid wants. */
  surface?: HTMLElement | null
  /** The CELL a pointer event is over, or null when it is outside the grid.
   *  Supplied by the caller because the cell geometry is the painter's, and a
   *  second derivation of it here would be a second answer to "which cell is
   *  under this point" (AD-8). */
  cellAt?: (clientX: number, clientY: number) => { x: number; y: number } | null
  /** Whether the accepted backend screen frame says the program is tracking
   *  the pointer. Ordinary clicks and wheel events belong to selection and
   *  scrolling, not to a program that has not enabled mouse reporting. */
  mouseTracking?: () => boolean
  /** The pane's row pitch in CSS pixels — the painter's accepted cell height,
   *  read here so a pixel-delta wheel can be counted in LINES (nocx-1ebrs.26).
   *  The pointer's geometry is the painter's (see `cellAt`), and a value of
   *  zero or less is read as "no pitch yet": one event then reports one step,
   *  which is the behaviour before the accumulator and never the intended
   *  one. */
  lineHeight?: () => number
  /** Whether an UNCLAIMED keyboard is this element's to read
   *  (nocx-1ebrs.66.1.19).
   *
   *  A keydown delivered while nothing in the document holds the keyboard is
   *  dispatched at the body, and no element hears it: there is nothing for
   *  the character that follows it to be inserted into. Whether that
   *  keystroke BELONGS to the program is a product question — is this the
   *  active pane, is an overlay up, does the program own the keyboard right
   *  now — and the product answers it here. What the key MEANS stays this
   *  element's answer, exactly as for a keydown that lands on it (AD-8: one
   *  owner per input, and this element is the owner of the reading).
   *
   *  Absent means the element never adopts: a surface that is not the
   *  keyboard's fallback must not become one. */
  mayAdoptUnclaimedKey?: (e: KeyboardEvent) => boolean
}

const MOUSE_BUTTONS: Readonly<Record<number, string>> = {
  0: 'left',
  1: 'middle',
  2: 'right',
}

/** The pane's input element and every listener that reads a person out of it.
 *
 *  It owns ONE textarea and nothing else: no rendering, no encoding, no
 *  knowledge of the session. Whatever it reads is reported through `emit`. */
export class IntentInput {
  readonly element: HTMLTextAreaElement

  private readonly emit: IntentSink
  private readonly consume: ((e: KeyboardEvent) => boolean) | null
  private readonly surface: HTMLElement | null
  private readonly cellAt:
    ((clientX: number, clientY: number) => { x: number; y: number } | null) | null
  private readonly mouseTracking: (() => boolean) | null
  private readonly lineHeight: (() => number) | null
  private readonly mayAdoptUnclaimedKey: ((e: KeyboardEvent) => boolean) | null
  private readonly listeners: Array<() => void> = []

  /** The wheel's running total per axis, in CSS pixels, and the tracking state
   *  it was accumulated under (nocx-1ebrs.26). A macOS trackpad emits dozens
   *  of small deltas per gesture and keeps emitting through momentum after the
   *  fingers lift, so one report per event told the program the person scrolled
   *  dozens of times when they scrolled once. Reports are emitted per LINE of
   *  movement, and the remainder is carried into the next event. */
  private wheelPx = { x: 0, y: 0 }
  private wheelTracking: boolean | null = null

  /** Set while the keydown that produced the text in flight went out as a KEY.
   *  Some platforms commit a character for a chord as well (Ctrl+A on some
   *  layouts, AltGr's own reporting), and without this the program would
   *  receive the chord AND the character. */
  private suppressText = false
  /** True from compositionstart until compositionend: what the IME draws while
   *  it is composing is provisional and must not reach the program. */
  private composing = false
  /** The text a composition just committed, while the engine may still be
   *  delivering it a second time (nocx-1ebrs.38). WebKit-family engines end
   *  the composition FIRST and then follow it with a plain `input` event —
   *  inputType `insertText`, isComposing already false, data the commit
   *  itself — a shape no composition filter names. The commit goes out from
   *  compositionend; an input carrying the same text before any fresh
   *  keystroke could have produced it is that echo, not the person's.
   *  Null whenever no commit is un-accounted-for. */
  private committedEcho: string | null = null
  /** Whether a fresh — non-composing — keystroke has happened since that
   *  commit. The first one re-arms text: whatever it types is the person's,
   *  even the same letter again. */
  private freshKeySinceCommit = false
  private destroyed = false
  /** The pointer button held during a motion, so a drag can be reported as a
   *  drag rather than as a bare motion (DEC 1002's own distinction). */
  private heldButton: string | null = null

  constructor(options: IntentInputOptions) {
    this.emit = options.emit
    this.consume = options.consume ?? null
    this.surface = options.surface ?? null
    this.cellAt = options.cellAt ?? null
    this.mouseTracking = options.mouseTracking ?? null
    this.lineHeight = options.lineHeight ?? null
    this.mayAdoptUnclaimedKey = options.mayAdoptUnclaimedKey ?? null

    const element = createTerminalInput()
    this.element = element

    this.listen(element, 'keydown', (e) => this.onKeydown(e as KeyboardEvent))
    this.listen(element, 'keyup', () => {
      this.suppressText = false
    })
    this.listen(element, 'input', (e) => this.onInput(e))
    this.listen(element, 'paste', (e) => this.onPaste(e as ClipboardEvent))
    this.listen(element, 'compositionstart', () => {
      this.composing = true
      // A new composition invalidates any commit whose echo has not been seen
      // yet: only the echo of ITS OWN end may be swallowed (nocx-1ebrs.38).
      this.committedEcho = null
    })
    this.listen(element, 'compositionend', (e) => this.onCompositionEnd(e as CompositionEvent))
    this.listen(element, 'focus', () => this.emit({ kind: 'focus', payload: 'in' }))
    this.listen(element, 'blur', () => this.emit({ kind: 'focus', payload: 'out' }))
    // A keystroke can be delivered while the keyboard has no owner at all;
    // see mayAdoptUnclaimedKey and onUnclaimedKeydown. Registered only when
    // the product has said such a keyboard can be this element's.
    if (this.mayAdoptUnclaimedKey !== null) {
      this.listen(document, 'keydown', (e) => this.onUnclaimedKeydown(e as KeyboardEvent))
    }
    this.listenPointer()
  }

  /** Move the keyboard's attention here. The caller decides WHEN — a pane that
   *  does not have the keyboard must not take it. */
  focus(): void {
    this.element.focus({ preventScroll: true })
  }

  /** Stop listening and take the element out of the document. Idempotent: a
   *  pane can be closed twice (a close and a teardown). */
  destroy(): void {
    if (this.destroyed) return
    this.destroyed = true
    for (const off of this.listeners.splice(0)) off()
    this.element.remove()
  }

  /** Place the element at the caret the painter drew, in the surface's own
   *  coordinates. The surface positions it — this element never repaints
   *  anything — and the position is what makes an IME's candidate window land
   *  under the text being composed rather than at the page's fallback corner. */
  placeAt(left: number, top: number, height: number): void {
    this.element.style.left = `${left}px`
    this.element.style.top = `${top}px`
    this.element.style.height = `${height}px`
  }

  private listen(target: EventTarget, type: string, handler: (event: Event) => void): void {
    const wrapped = (event: Event): void => {
      if (this.destroyed) return
      handler(event)
    }
    target.addEventListener(type, wrapped)
    this.listeners.push(() => target.removeEventListener(type, wrapped))
  }

  private listenPointer(): void {
    const surface = this.surface
    if (!surface) return
    this.listen(surface, 'pointerdown', (e) => this.onPointer(e as PointerEvent, 'press'))
    this.listen(surface, 'pointerup', (e) => this.onPointer(e as PointerEvent, 'release'))
    this.listen(surface, 'pointermove', (e) => this.onPointer(e as PointerEvent, 'motion'))
    this.listen(surface, 'wheel', (e) => this.onWheel(e as WheelEvent))
    // A gesture ENDS when the pointer leaves: a remainder left over from it
    // must not be spent as the first line of the next one (nocx-1ebrs.26).
    this.listen(surface, 'pointerleave', () => this.resetWheel())
  }

  /** Forget the wheel's running total. Called on leaving the pane and on any
   *  change of the tracking mode, because the remainder belongs to the gesture
   *  that produced it — not to the next one, and not to the other owner. */
  private resetWheel(): void {
    this.wheelPx = { x: 0, y: 0 }
  }

  private onKeydown(e: KeyboardEvent): void {
    // BEFORE ANY DECISION, what the browser actually reported (nocx-1ebrs.32):
    // a key the pane never sees and a key it sees and drops look identical from
    // outside — "does nothing" — and only this line tells them apart. It is the
    // existing decision trace, so it is silent unless somebody asked for it.
    logKeydown('grid', e)
    if (this.consume?.(e)) {
      // The pane's own chord: consumed here, prevented from reaching the
      // browser AND from becoming an intent — the caller just opened
      // whatever the chord opens, and zero bytes belong to the program.
      e.preventDefault()
      // The browser may still commit a character for the chord (Option+P is
      // 'π' on macOS); it belongs to the pane's own gesture, not to the
      // program, so the next input event is swallowed — and the next keydown,
      // or a keyup where nothing else happened, clears the suppression.
      this.suppressText = true
      return
    }
    // Let the browser own its physical paste chord. Preventing this keydown
    // would also suppress the native paste event, which is the only source of
    // clipboard contents; the paste listener below then emits the paste intent.
    // Keep Shift+V here too: browsers use it for unformatted paste.
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.code === 'KeyV') {
      this.suppressText = false
      return
    }
    if (isDeadKey(e)) {
      // Nothing on its own; whatever it composes arrives as text.
      this.suppressText = false
      return
    }
    const intent = keyIntentOf(e)
    if (!intent) {
      // A character, an AltGr key or a composing key: the text (if any) is what
      // carries it, so any chord suppression from the previous key ends here.
      this.suppressText = false
      // A keystroke the person made outside any composition re-arms text after
      // a commit: its own input event — even the same letter — is theirs, never
      // the echo of a composition that already ended (nocx-1ebrs.38).
      if (!e.isComposing) this.freshKeySinceCommit = true
      return
    }
    this.suppressText = true
    // The browser must not act on a key the program is being told about: an
    // Enter would otherwise put a newline in this element, and a Tab would move
    // the focus out of the pane.
    e.preventDefault()
    this.emit(intent)
  }

  /** A keydown the DOCUMENT delivered to nobody: no element held the
   *  keyboard, so the browser dispatched it at the body and there is no
   *  target for the character that follows (nocx-1ebrs.66.1.19).
   *
   *  BUBBLE PHASE, and registered after the pane's own document listeners on
   *  purpose: a chord one of them took (Ctrl+C addressed to the running block,
   *  the copy chord, the target chord) is already prevented, and a key another
   *  surface owns must not be read a second time here.
   *
   *  The three conditions are the ones the product's predicate cannot see:
   *  that the event was not already claimed, that it did not land on this
   *  element, and that the keyboard really is unclaimed. */
  private onUnclaimedKeydown(e: KeyboardEvent): void {
    if (e.defaultPrevented) return
    if (e.target === this.element) return
    const holder = document.activeElement
    if (holder !== null && holder !== document.body && holder !== document.documentElement) return
    if (this.mayAdoptUnclaimedKey?.(e) !== true) return
    this.adoptUnclaimedKey(e)
  }

  /** Read a keydown that reached no element as if it had reached this one.
   *
   *  THE KEYBOARD COMES WITH THE KEY: one focus, and every keystroke after
   *  this one is read by the element's own listener, so nothing is adopted
   *  twice and nothing has to be adopted again. */
  private adoptUnclaimedKey(e: KeyboardEvent): void {
    this.focus()
    const text = textOfUnclaimedKey(e)
    if (text !== null) {
      // Nothing follows this keydown with an `input` event: it was routed at
      // the body, so the character it would have inserted has nowhere to
      // land. Emitting it here is the text intent the input event would have
      // produced — and the default is prevented so a second copy cannot land
      // in the element that was just focused.
      e.preventDefault()
      this.suppressText = false
      this.freshKeySinceCommit = true
      this.emit({ kind: 'text', payload: text })
      return
    }
    // Everything else means what it means ON the element: a named key or a
    // Ctrl/Meta chord becomes a key intent (and is prevented), the physical
    // paste chord is left to the browser — whose default now pastes into the
    // element this key focused — and a dead key or a composition is left
    // alone.
    this.onKeydown(e)
  }

  private onInput(e: Event): void {
    const element = this.element
    // Read and clear in one step: whatever the browser put here has already
    // been reported (or is being reported below), and a value that accumulated
    // would drag the IME's candidate window away from the caret the pane
    // anchors it to.
    element.value = ''
    if (this.suppressText) return
    const data = (e as InputEvent).data
    const inputType = (e as InputEvent).inputType
    const composing = (e as InputEvent).isComposing === true || this.composing
    if (composing) return
    if (inputType === 'insertCompositionText' || inputType === 'insertFromComposition') {
      // A composition's own text never travels as it is drawn, and its commit
      // has already gone out from compositionend: sending it here would be the
      // same text twice, which is the defect the bead names.
      return
    }
    if (this.committedEcho !== null && !this.freshKeySinceCommit && data === this.committedEcho) {
      // THE COMMIT'S OWN ECHO (nocx-1ebrs.38). WebKit-family engines end the
      // composition first and then deliver the committed text AGAIN, as a
      // plain `input` — insertText, isComposing already false — a shape the
      // inputType filter above does not name. It is recognisable by what it
      // is: the commit text, arriving before any fresh keystroke could have
      // produced it. The commit went out from compositionend, so this is
      // silence; the window stays open for an engine that delivers the echo
      // more than once, and anything else closes it.
      return
    }
    this.committedEcho = null
    if (data === null || data === undefined || data === '') return
    this.emit({ kind: 'text', payload: data })
  }

  private onCompositionEnd(e: CompositionEvent): void {
    this.composing = false
    const data = e.data ?? ''
    // The commit, exactly once. An empty commit is a CANCELLED composition
    // (Escape), and the text the IME was showing was never the person's.
    if (data === '') {
      this.committedEcho = null
      return
    }
    // WebKit-family engines follow this end with the same text as a plain
    // `input` (nocx-1ebrs.38): open the echo window, and close it against
    // anything the person does fresh — the committing keystroke itself may
    // already have reported as a plain key before this end arrived, so the
    // mark it left is cleared here and re-armed by the NEXT keystroke.
    this.committedEcho = data
    this.freshKeySinceCommit = false
    this.emit({ kind: 'text', payload: data })
  }

  private onPaste(e: ClipboardEvent): void {
    // The element is not a buffer and the client does not wrap: a paste is its
    // own kind, and bracketed-paste wrapping is the program's mode and the
    // runtime's decision (ADR-0065).
    e.preventDefault()
    const data = e.clipboardData
    const text = data?.getData('text') ?? ''
    if (text !== '') {
      this.emit({ kind: 'paste', payload: text })
      return
    }
    // AN IMAGE-ONLY CLIPBOARD IS STILL A PASTE, AND THE PROGRAM IS OWED IT
    // (nocx-1ebrs.14). There is no text to forward — the terminal cannot turn a
    // screenshot into cells — but a full-screen program reads the clipboard
    // over its own path and only knows to look because a paste arrived. This
    // dropped the gesture on the floor, so the program showed nothing while the
    // same gesture in another terminal showed the image it had fetched.
    //
    // What is emitted is an EMPTY paste, not an invented body: the runtime
    // wraps it in the program's own bracketed-paste mode, so a program that
    // asked for 2004 receives the bracket pair with nothing between it, and a
    // program that did not receives nothing at all (there is no way to say
    // "empty" outside that mode). A clipboard holding NO data is the other
    // empty case and stays silent: nothing was copied, so there is no paste.
    const carriesOtherData = (data?.types ?? []).some((type) => !type.startsWith('text/'))
    if (carriesOtherData) this.emit({ kind: 'paste', payload: '' })
  }

  private onPointer(e: PointerEvent, action: 'press' | 'release' | 'motion'): void {
    if (!(this.mouseTracking?.() ?? false)) {
      // A release can arrive after the program disabled reporting mid-drag.
      // Clear local drag state, but leave the event available to selection.
      if (action === 'release') this.heldButton = null
      return
    }
    if (action === 'motion' && e.buttons === 0 && this.heldButton === null) {
      // A bare motion is a motion: the program is told the pointer moved with
      // no button down, which is what DEC 1003 asks for and what a program
      // tracking a drag (1002) ignores.
      this.emitMouse(action, 'none', e)
      return
    }
    const button = MOUSE_BUTTONS[e.button] ?? null
    if (action === 'press') {
      if (!button) return
      this.heldButton = button
      this.emitMouse('press', button, e)
      return
    }
    if (action === 'release') {
      // A release names the button that went up, which is the one that was
      // down when the press was reported.
      const released = button ?? this.heldButton
      this.heldButton = null
      if (!released) return
      this.emitMouse('release', released, e)
      return
    }
    this.emitMouse('motion', this.heldButton ?? button ?? 'none', e)
  }

  /** One wheel REPORT per line of movement, not per event (nocx-1ebrs.26).
   *
   *  A wheel event is a press and a release of one step in a single event, so
   *  it is reported as the press the program's tracking mode is looking for —
   *  and the count is the point. A high-resolution trackpad sends tens of
   *  sub-line deltas per flick and keeps going through momentum, so reporting
   *  each one told the program the person scrolled dozens of times when they
   *  scrolled once. That is not a speed preference: it is a false statement
   *  about the input, and the momentum tail arrives after the gesture ended.
   *
   *  The deltas are converted to pixels first, as scrollback/live-history.ts
   *  does for the scroller (`_translateWheel`), so the three `deltaMode`s mean
   *  the same thing here: a line is a line and must not be divided again, a
   *  page is a viewport, and only a pixel delta is accumulated. */
  private onWheel(e: WheelEvent): void {
    const tracking = this.mouseTracking?.() ?? false
    if (!tracking) {
      this.wheelTracking = false
      this.resetWheel()
      return
    }
    // A NEW OWNER STARTS A NEW GESTURE: a remainder accumulated while the
    // program was reporting says nothing about what the person is doing now.
    if (this.wheelTracking !== true) {
      this.wheelTracking = true
      this.resetWheel()
    }
    const line = this.lineHeight?.() ?? 0
    if (!(line > 0)) {
      // No row pitch to count in (the pane has no committed metric yet). One
      // event is one step, which is what this did before and is the degrade
      // rather than a second rule.
      const button = e.deltaY < 0 ? 'wheel-up' : e.deltaY > 0 ? 'wheel-down' : null
      if (button !== null && this.emitMouse('press', button, e)) e.preventDefault()
      return
    }
    const scale =
      e.deltaMode === WheelEvent.DOM_DELTA_LINE
        ? line
        : e.deltaMode === WheelEvent.DOM_DELTA_PAGE
          ? Math.max(line, this.surface?.clientHeight ?? line)
          : 1
    this.wheelPx.x += e.deltaX * scale
    this.wheelPx.y += e.deltaY * scale
    let reported = false
    while (this.wheelPx.y <= -line) {
      if (!this.emitMouse('press', 'wheel-up', e)) break
      this.wheelPx.y += line
      reported = true
    }
    while (this.wheelPx.y >= line) {
      if (!this.emitMouse('press', 'wheel-down', e)) break
      this.wheelPx.y -= line
      reported = true
    }
    while (this.wheelPx.x <= -line) {
      if (!this.emitMouse('press', 'wheel-left', e)) break
      this.wheelPx.x += line
      reported = true
    }
    while (this.wheelPx.x >= line) {
      if (!this.emitMouse('press', 'wheel-right', e)) break
      this.wheelPx.x -= line
      reported = true
    }
    if (reported) e.preventDefault()
  }

  private emitMouse(
    action: string,
    button: string,
    e: { clientX: number; clientY: number },
  ): boolean {
    const cell = this.cellAt?.(e.clientX, e.clientY) ?? null
    if (!cell) return false
    this.emit({ kind: 'mouse', payload: `${action} ${button} ${cell.x} ${cell.y}` })
    return true
  }
}
