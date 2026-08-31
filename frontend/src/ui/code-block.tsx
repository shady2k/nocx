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
 * `variant="answer"` is the streamed assistant fence's bordered form. It
 * shares this component's appearance rather than letting the answer surface
 * repaint a second code block.
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
 * `copy` is the optional clipboard operation behind a copy control in a real
 * header strip, injected rather than reached for so the kit never names a
 * platform. It is offered only for a block whose children ARE the text: a
 * block carrying an inline component has no text to hand over, and where that
 * component is a `SecretChip` the bytes behind it are precisely what must not
 * leave through the clipboard. Callers that already own a copy affordance omit
 * the prop and render the read-only block. `label` names the fence language or
 * block kind on the left; it defaults to `Code`. An imperative surface — the
 * assistant's streamed answer, which builds its blocks by hand — passes the
 * same label through `mountCodeBlockCopyButton`, so there is one copy
 * vocabulary rather than two.
 */
import { Show, children, createSignal } from 'solid-js'
import type { JSX } from 'solid-js'
import { render } from 'solid-js/web'
import { CopyIcon } from './icons'
import { IconButton } from './icon-button'
import { showToast } from './toast'

const DEFAULT_CODE_LABEL = 'Code'

export interface CodeBlockProps {
  children: JSX.Element
  /** Accessible name, when the block needs one beyond its surrounding label. */
  ariaLabel?: string
  /** Visible kind in the copy header; defaults to `Code`. */
  label?: string
  /** Whether a long line wraps. Default true; see the note above for when a
   *  block says false. */
  wrap?: boolean
  /** Surface-specific bordered form used by streamed assistant answers. */
  variant?: 'answer'
  /** Injected platform clipboard operation. Existing callers with their own copy
   * affordance may omit this and render only the read-only block. */
  copy?: (text: string) => Promise<void>
}

export interface CodeBlockCopyButtonProps {
  /** Read at click time so an imperative streaming block copies current code. */
  getText: () => string
  /** Visible kind in the imperative copy header; defaults to `Code`. */
  label?: string
  copy: (text: string) => Promise<void>
  /** Keep the host inside an existing imperative code container. */
  keepHostInBlock?: boolean
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

function createCodeBlockHeader(label: string): HTMLDivElement {
  const header = document.createElement('div')
  header.className = 'ui-code-block__header'
  const labelEl = document.createElement('span')
  labelEl.className = 'ui-code-block__label'
  labelEl.textContent = label
  header.append(labelEl)
  return header
}

/**
 * Give an imperative block the same structure as the JSX component.
 *
 * The scrollback creates a block before it knows its rows, so the copy host
 * starts inside the block. Move it into a kit-owned header and put the existing
 * block under a kit wrapper; no surface needs to know either structure.
 */
function mountImperativeCodeBlockHeader(
  host: HTMLElement,
  label: string,
  keepHostInBlock: boolean,
): void {
  const block = host.parentElement
  if (!block?.classList.contains('ui-code-block')) {
    const header = createCodeBlockHeader(label)
    host.before(header)
    header.append(host)
    return
  }

  host.classList.add('ui-code-block__copy-host')
  if (keepHostInBlock) {
    block.classList.add('ui-code-block-wrap--copy')
    const header = createCodeBlockHeader(label)
    header.append(host)
    block.prepend(header)
    return
  }

  const wrapper = document.createElement('div')
  wrapper.className = 'ui-code-block-wrap ui-code-block-wrap--copy'
  block.replaceWith(wrapper)
  block.classList.remove('ui-code-block-wrap')

  const header = createCodeBlockHeader(label)
  header.append(host)
  wrapper.append(header, block)
}

/** Mount the CodeBlock copy control into an imperative DOM surface. */
export function mountCodeBlockCopyButton(
  host: HTMLElement,
  props: CodeBlockCopyButtonProps,
): () => void {
  const { label, keepHostInBlock, ...buttonProps } = props
  mountImperativeCodeBlockHeader(host, label ?? DEFAULT_CODE_LABEL, keepHostInBlock ?? false)
  return render(() => <CodeBlockCopyButton {...buttonProps} />, host)
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
      <Show when={props.copy && copyText() !== undefined ? props.copy : undefined}>
        {(copy) => (
          <div class="ui-code-block__header">
            <span class="ui-code-block__label">{props.label ?? DEFAULT_CODE_LABEL}</span>
            <CodeBlockCopyButton getText={() => copyText() ?? ''} copy={copy()} />
          </div>
        )}
      </Show>
      <pre
        class="ui-code-block"
        data-variant={props.variant}
        data-wrap={props.wrap === false ? 'false' : undefined}
        aria-label={props.ariaLabel}
        tabIndex={0}
      >
        {held()}
      </pre>
    </div>
  )
}
