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
 */
export interface CodeBlockProps {
  children: string
  /** Accessible name, when the block needs one beyond its surrounding label. */
  ariaLabel?: string
}

export function CodeBlock(props: CodeBlockProps) {
  return (
    <pre class="ui-code-block" aria-label={props.ariaLabel} tabIndex={0}>
      {props.children}
    </pre>
  )
}
