/**
 * CodeBlock — preformatted, monospaced output the user reads but does not edit:
 * a JSON payload, a list of file paths, a captured error.
 *
 * The export page had this as `.st-export-backup-details`, a `<pre>` with its own
 * background, border, radius, padding, type size and scroll cap declared on the
 * surface. Every one of those is an appearance decision, and appearance decisions
 * made in a surface are how two screens end up showing the same kind of thing two
 * different ways. The next surface that has to show a payload gets this instead of
 * writing its own.
 *
 * Scrolls rather than grows: the content is machine output of unknown length, and
 * a section whose height is decided by a backend response is a section that pushes
 * everything under it off screen. The cap lives in `code-block.css` — one number,
 * decided once, not a prop each caller re-answers.
 *
 * `tabIndex={0}` because a scrollable region that only a mouse wheel can move is
 * unreachable by keyboard once the content overflows.
 *
 * WRAPPED OR SCROLLED is the caller's one piece of variance, and it is here
 * rather than in a surface stylesheet because the answer has to be the same
 * one the editors give. `wrap` defaults to true — most machine output is a
 * list of short lines and a reader should see all of it — and `wrap={false}`
 * is a block holding BYTES: the API workbench's raw request and raw response
 * are the same octets its body editor holds, and a surface that showed them
 * one way while the editor showed them the other would be two answers to one
 * question (nocx-kdawd). Long content is then reached by scrolling sideways
 * inside the block, which already has its own scroll box.
 *
 * `children` is a JSX element rather than a string, so a block may carry an
 * inline component where the machine output does: the API workbench's raw
 * request text renders `SecretChip` in place of a secret's bytes (ADR-0021 —
 * the reference is what is stored, sent and resolved, and only the RENDERING
 * is a chip). Widened rather than forked: a surface that needed a chip inside
 * preformatted output would otherwise have hand-rolled a second `<pre>` with
 * its own background, border, type size and scroll cap — which is the exact
 * defect this component was extracted to end. Plain strings are unaffected.
 */
import type { JSX } from 'solid-js'

export interface CodeBlockProps {
  children: JSX.Element
  /** Accessible name, when the block needs one beyond its surrounding label. */
  ariaLabel?: string
  /** Whether a long line wraps. Default true; see the note above for when a
   *  block says false. */
  wrap?: boolean
}

export function CodeBlock(props: CodeBlockProps) {
  return (
    <pre
      class="ui-code-block"
      data-wrap={props.wrap === false ? 'false' : undefined}
      aria-label={props.ariaLabel}
      tabIndex={0}
    >
      {props.children}
    </pre>
  )
}
