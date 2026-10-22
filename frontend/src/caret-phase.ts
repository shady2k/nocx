// The caret's blink phase — ONE vocabulary for every surface that draws a
// terminal cursor.
//
// Two surfaces draw one cursor in this app: the composer's (block-cursor.ts,
// over the CM6 editor) and the live grid's overlay (painter/painter.ts, over
// the painted cells). Neither can see the other, and each draws by a
// different mechanism — a CSS animation here, CM6's inline `animation-name`
// there, the grid's overlay animation in style.css — so the rule they share
// lives here and nowhere else, in the words the stylesheet reads.
//
// The standard, not a taste: macOS text fields, VS Code and iTerm all keep
// the caret SOLID while input arrives and let the blink return on a pause of
// about half a second. A caret caught mid-off by a key reads as a dropped
// key, which is the one thing a caret must never look like.
//
// A hold, not a second period: the blink's rhythm stays each surface's own.

/** How long a cursor holds still after the last key — or after the program's
 *  own write at the cursor (nocx-1ebrs.37) — before the blink resumes. */
export const CARET_IDLE_MS = 500

/** The attribute the phase is written on, which is what the stylesheets read. */
export const CARET_PHASE_ATTR = 'data-caret'

/** Solid while keys are arriving; blinking once they have stopped. */
export type CaretPhase = 'solid' | 'blinking'
