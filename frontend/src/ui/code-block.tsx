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
 *
 * `copy` is the optional clipboard operation behind a copy control in the
 * block's corner, injected rather than reached for so the kit never names a
 * platform. It is offered only for a block whose children ARE the text: a
 * block carrying an inline component has no text to hand over, and where that
 * component is a `SecretChip` the bytes behind it are precisely what must not
 * leave through the clipboard. Callers that already own a copy affordance omit
 * the prop and render the read-only block, and an imperative surface — the
 * assistant's streamed answer, which builds its blocks by hand — mounts the
 * same control through `mountCodeBlockCopyButton` so there is one copy
 * vocabulary rather than two.
 */
import { Show, children, createSignal } from 'solid-js'
import type { JSX } from 'solid-js'
import { render } from 'solid-js/web'
import { CopyIcon } from './icons'
import { IconButton } from './icon-button'
import { showToast } from './toast'

export interface CodeBlockProps {
  children: JSX.Element
  /** Accessible name, when the block needs one beyond its surrounding label. */
  ariaLabel?: string
  /** Whether a long line wraps. Default true; see the note above for when a
   *  block says false. */
  wrap?: boolean
  /** Injected platform clipboard operation. Existing callers with their own copy
   * affordance may omit this and render only the read-only block. */
  copy?: (text: string) => Promise<void>
}

export interface CodeBlockCopyButtonProps {
  /** Read at click time so an imperative streaming block copies current code. */
  getText: () => string
  copy: (text: string) => Promise<void>
}

function CodeBlockCopyButton(props: CodeBlockCopyButtonProps) {
  const [copied, setCopied] = createSignal(false)

  const copy = async (): Promise<void> => {
    try {
      await props.copy(props.getText())
      setCopied(true)
      showToast({ level: 'success', message: 'Code copied' })
    } catch {
      setCopied(false)
      showToast({ level: 'danger', message: 'Could not copy code' })
    }
  }

  return (
    <IconButton
      ariaLabel={copied() ? 'Copied' : 'Copy code'}
      title={copied() ? 'Copied' : 'Copy code'}
      size="sm"
      onClick={() => void copy()}
    >
      <CopyIcon />
    </IconButton>
  )
}

/** Mount the CodeBlock copy control into an imperative DOM surface. */
export function mountCodeBlockCopyButton(
  host: HTMLElement,
  props: CodeBlockCopyButtonProps,
): () => void {
  return render(() => <CodeBlockCopyButton {...props} />, host)
}

export function CodeBlock(props: CodeBlockProps) {
  // RESOLVED ONCE, because three things now ask what the children are: the
  // `<pre>` renders them, and the copy control's presence and its payload
  // both depend on whether they are text. `props.children` is a getter that
  // re-runs the caller's expression on every read, so a block carrying a
  // component (`SecretChip` in the API workbench's raw request) would build a
  // fresh instance per question and throw two of them away. `children()` is
  // the kit's existing answer to this — Section and Tabs resolve their
  // `actions` the same way.
  const held = children(() => props.children)

  // The text a copy control would hand over, and `undefined` when there is
  // none: children are a JSX element so that a block CAN carry a component,
  // and an element is not text.
  const copyText = (): string | undefined => {
    const value = held()
    return typeof value === 'string' ? value : undefined
  }

  return (
    <div
      class="ui-code-block-wrap"
      classList={{ 'ui-code-block-wrap--copy': Boolean(props.copy) && copyText() !== undefined }}
    >
      <pre
        class="ui-code-block"
        data-wrap={props.wrap === false ? 'false' : undefined}
        aria-label={props.ariaLabel}
        tabIndex={0}
      >
        {held()}
      </pre>
      <Show when={props.copy && copyText() !== undefined ? props.copy : undefined}>
        {(copy) => <CodeBlockCopyButton getText={() => copyText() ?? ''} copy={copy()} />}
      </Show>
    </div>
  )
}
