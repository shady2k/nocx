// The composer's cursor is the TERMINAL's cursor, drawn by the same rules.
//
// One row above the composer, the painter draws a block: filled with
// `--terminal-cursor`, the glyph under it painted in
// `--terminal-cursor-accent`, and nothing at all when the surface is not
// focused. "What a cursor
// looks like here" therefore already had an owner and two tokens before this
// module existed; this is that answer extended to the prompt, not a second one
// beside it. A bar caret in the composer and a block in the scrollback were
// two vocabularies for one thing.
//
// THE BLOCK IS DRAWN TWO WAYS, and the split is the whole design:
//
//   on a character — this module's mark decoration, which paints behind the
//     glyph and recolours it. A block laid OVER the text would hide the
//     character it stands on, which a terminal never does and which costs
//     more in a box you move around in than in one you only type at the end
//     of;
//   at the end of a line — no decoration at all. CM6's own cursor element
//     (drawSelection) is restyled into a block in style.css, because there is
//     no character to mark and the alternatives are both wrong: an inline
//     widget brings back the `cm-widgetBuffer` image that made the card grow
//     by a pixel under a completion ghost, and a line-level `::after` renders
//     after everything on the line — including that ghost — so the cursor
//     would sit at the END of the suggestion rather than at the caret
//     (`unam[e]█` where a shell shows `unam█e`). CM places its cursor element
//     by coordinate, at the caret, in an absolutely positioned layer: right
//     place, and no line box to grow.
//
// So this module owns exactly one case, and style.css owns the other.
import { Extension } from '@codemirror/state'
import {
  Decoration,
  EditorView,
  ViewPlugin,
  type DecorationSet,
  type ViewUpdate,
} from '@codemirror/view'

/** The character the cursor stands on: filled behind, recoloured on top. */
export const BLOCK_CURSOR_MARK = 'nocx-block-cursor'

/**
 * The blink cycle, in milliseconds — ONE number for both halves.
 *
 * The two halves blink by two different mechanisms and neither can see the
 * other: the end-of-line block is CM6's cursor layer, which writes an inline
 * `animation-name` on itself and takes its period from `cursorBlinkRate`,
 * while the mark is a CSS animation in style.css. A cursor that changed its
 * rhythm as it stepped onto a character would read as two cursors taking
 * turns, so the number is declared here and handed to both: to CM6 through
 * drawSelection, and to the stylesheet as `--nocx-cursor-blink`.
 *
 * 1200ms is CM6's own default, kept so that nothing about the end-of-line
 * half changes when it is passed explicitly.
 */
export const CURSOR_BLINK_MS = 1200

/**
 * The rate to hand CM6: zero — its own word for "do not blink" — when the
 * viewer has asked for less motion.
 *
 * The query is read here rather than in CSS because the layer's animation is
 * inline and a stylesheet cannot reach it; style.css guards the mark's half
 * with the same query, the way every other animation in this app is guarded.
 * Read once at construction: a cursor that started or stopped blinking under
 * a person mid-session would be its own small motion.
 */
export function cursorBlinkRate(): number {
  return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 0 : CURSOR_BLINK_MS
}

// The idle bound, the phase attribute and the phase's two words are the
// shared caret vocabulary (caret-phase.ts): the grid's overlay follows the
// same rule (nocx-1ebrs.37) and reads them from the same owner.
import { CARET_IDLE_MS, CARET_PHASE_ATTR, type CaretPhase } from './caret-phase'

export { CARET_IDLE_MS, CARET_PHASE_ATTR, type CaretPhase } from './caret-phase'

/**
 * The caret's phase as the surface is rendering it.
 *
 * The state lives on the editor element and nowhere else, because the two
 * halves of the cursor are drawn by two mechanisms that share no code — a CSS
 * animation here, CM6's inline `animation-name` there — and a copy of the
 * phase in a module variable would be a second answer to "is it blinking".
 * This reads that attribute back as a type: the seam a test asks, and the same
 * one the stylesheet consumes.
 */
export function caretPhase(view: EditorView): CaretPhase {
  return view.dom.getAttribute(CARET_PHASE_ATTR) === 'solid' ? 'solid' : 'blinking'
}

/**
 * Install in a CommandEditor to hold the caret still while keys arrive.
 *
 * Both halves of the cursor blinked on their own clocks before this existed.
 * The mark half is a plain `animation: … infinite` in composer.css, which no
 * keystroke could restart; CM6's half restarts itself on every transaction
 * that carries a selection, so it was nearer the standard but on a rhythm of
 * its own. One phase, written once per key and cleared on one idle timer, is
 * what makes the two agree — and is the only way the phase can be asserted
 * without naming a class.
 *
 * NOT keyed on doc changes alone. An arrow, Home/End and a shift-selection
 * move the caret with no character in the document, and they are input: they
 * are what a person presses while LOOKING at the caret, which is the moment a
 * mid-off blink misleads most. `selectionSet` therefore resets the phase too,
 * and the standard's own answer for them is the same as for typing.
 */
export function caretBlink(): Extension {
  return ViewPlugin.fromClass(
    class {
      private idle: ReturnType<typeof setTimeout> | null = null

      constructor(private readonly view: EditorView) {
        // A caret that has been sitting there is blinking before the first
        // key of the session, which is the state every text field opens in.
        this.set('blinking')
      }

      update(update: ViewUpdate): void {
        if (!update.docChanged && !update.selectionSet) return
        this.set('solid')
        if (this.idle !== null) clearTimeout(this.idle)
        this.idle = setTimeout(() => {
          this.idle = null
          this.set('blinking')
        }, CARET_IDLE_MS)
      }

      destroy(): void {
        if (this.idle !== null) clearTimeout(this.idle)
      }

      private set(phase: CaretPhase): void {
        this.view.dom.setAttribute(CARET_PHASE_ATTR, phase)
      }
    },
  )
}

const MARK = Decoration.mark({ class: BLOCK_CURSOR_MARK })

/**
 * The character the block covers, or nothing when there is none to cover.
 *
 * Unfocused draws nothing, matching the terminal's inactive cursor style
 * stated a second time because it is the same claim: a cursor says where YOUR
 * typing goes, and in a surface that is not taking your typing it says
 * something false. Two composers are on screen whenever a pane is split.
 *
 * The head rather than the anchor, and drawn even while a selection is up:
 * the head is the end the keyboard moves, so it is the end the person is
 * watching.
 */
function blockCursorDecorations(view: EditorView): DecorationSet {
  if (!view.hasFocus) return Decoration.none
  const head = view.state.selection.main.head
  const line = view.state.doc.lineAt(head)
  if (head >= line.to) return Decoration.none // end of line: CM's own cursor
  return Decoration.set([MARK.range(head, head + 1)])
}

/**
 * Install in a CommandEditor to draw the terminal's block cursor.
 *
 * Pairs with `drawSelection()`: that is what turns the native caret
 * transparent (nocx-dvdfx) and what draws the element style.css shapes into
 * the end-of-line block, so the two halves of the cursor are never on screen
 * at once.
 */
export function blockCursor(): Extension {
  return ViewPlugin.fromClass(
    class {
      decorations: DecorationSet

      constructor(view: EditorView) {
        this.decorations = blockCursorDecorations(view)
      }

      update(update: ViewUpdate): void {
        // focusChanged is not optional here: the cursor must LEAVE when the
        // pane loses focus, and losing focus changes neither the document nor
        // the selection. Without it the split's inactive half goes on showing
        // a cursor that takes nothing.
        if (
          update.docChanged ||
          update.selectionSet ||
          update.focusChanged ||
          update.viewportChanged
        ) {
          this.decorations = blockCursorDecorations(update.view)
        }
      }
    },
    { decorations: (plugin) => plugin.decorations },
  )
}
