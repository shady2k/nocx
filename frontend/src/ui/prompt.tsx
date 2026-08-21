import { Show, createEffect, createMemo, onCleanup, untrack, type JSX } from 'solid-js'
import { Portal } from 'solid-js/web'
import {
  popOverlay,
  pushOverlay,
  restoreFocus,
  topOverlayElement,
  type OverlayEntry,
} from './overlay/stack'

export interface PromptProps {
  open: boolean
  title?: string
  ariaLabel: string
  placement?: 'floating' | 'top-sheet'
  onClose: () => void
  /**
   * The prompt's affirmative action, fired by Enter in a single-line field.
   *
   * Opt-in, with the same contract as Dialog's `onSubmit`: a caller that
   * passes this is saying "this prompt has one obvious yes". Textareas and
   * buttons own their own Enter; an IME's Enter accepts a candidate.
   */
  onSubmit?: () => void
  children: JSX.Element
  actions: JSX.Element
  /**
   * How the action slot lays its children out.
   *
   * `row` — the default and what every prompt had before: one right-aligned
   * line, because one question with one yes and one no fits on one.
   *
   * `stacked` — a column of lines. Added for the approval prompt (nocx-gycwo),
   * whose answers stopped being two: allow and deny, each at three widths, is
   * two groups of three, and six buttons on one line is a wall rather than a
   * choice. The variance is typed and lives here rather than being painted
   * from the surface — a surface may place a kit component and may never
   * repaint it (§3.6).
   */
  actionsLayout?: 'row' | 'stacked'
}

/**
 * Put the caret where the user is about to type — the same preference order
 * a native `<dialog>`'s showModal gives: an explicit autofocus, then the
 * first real field, then the first button. A Prompt is a plain div, so it
 * must do this itself.
 */
function focusInitial(panel: HTMLElement): void {
  const enabled = ':not([disabled]):not([tabindex="-1"])'
  const target =
    panel.querySelector<HTMLElement>('[autofocus]' + enabled) ??
    panel.querySelector<HTMLElement>(
      `input:not([type="hidden"])${enabled}, select${enabled}, textarea${enabled}`,
    ) ??
    panel.querySelector<HTMLElement>('button' + enabled)
  target?.focus()
}

export function Prompt(props: PromptProps) {
  let element: HTMLDivElement | undefined
  let entry: OverlayEntry | null = null
  /**
   * The overlay this prompt renders INSIDE while open, or null to render in
   * place. A modal `<dialog>` lives in the browser's top layer, which is above
   * every z-index in the normal layer by definition — being on top of a
   * top-layer element is not a number, it is a parent. So a prompt opened over
   * something must be a DOM child of it, which is also how the connection
   * editor's own password prompt has always appeared above its dialog.
   *
   * A `Portal`, not `host.appendChild(element)`. Moving the node by hand put
   * it where Solid does not expect it: closing the prompt by unmounting its
   * owner — which is what `<Show when={unlockOpen()}>` does — removed nothing,
   * because Solid removes from the parent it inserted into. The overlay entry
   * popped and the panel stayed on screen with the caret still in it, so the
   * first Escape appeared to do nothing and the second closed the panel *and*
   * the dialog under it, the panel having been a child of that dialog all
   * along. Measured in a browser; the entry was gone from the stack while the
   * element was still in the DOM.
   *
   * `untrack` is what makes this the overlay we opened OVER: read reactively,
   * pushing ourselves would immediately re-point it at this prompt. It is
   * recomputed only when `open` flips, and computed during render — before the
   * effect below pushes.
   */
  const host = createMemo(() => (props.open ? untrack(topOverlayElement) : null))

  createEffect(() => {
    if (props.open && !entry) {
      const onClose = props.onClose
      entry = pushOverlay(
        () => {
          onClose()
          return true
        },
        undefined,
        element,
      )
      // Escape is supplied by the overlay stack's document-level handler —
      // it closes the topmost overlay, which is this prompt.
      if (element) focusInitial(element)
    } else if (!props.open && entry) {
      popOverlay(entry)
      restoreFocus(entry)
      entry = null
    }
  })

  onCleanup(() => {
    if (entry) {
      popOverlay(entry)
      restoreFocus(entry)
    }
  })

  /**
   * Enter in a single-line field means "the obvious yes", the same as it
   * means in Dialog and in every other form. Guarded three ways, mirroring
   * Dialog: only when the caller declared an action, only from a real input
   * (a textarea owns Enter, a button already has its own), and not
   * mid-composition — an IME uses Enter to accept a candidate.
   */
  const onKeyDown = (e: KeyboardEvent) => {
    if (!props.onSubmit) return
    if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return
    const target = e.target as HTMLElement | null
    if (!target || target.tagName !== 'INPUT') return
    if ((target as HTMLInputElement).type === 'button') return
    e.preventDefault()
    props.onSubmit()
  }

  const panel = () => (
    <div
      ref={element}
      class="ui-prompt-overlay"
      data-placement={props.placement ?? 'floating'}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) props.onClose()
      }}
    >
      <section
        class="ui-prompt"
        data-placement={props.placement ?? 'floating'}
        role="dialog"
        aria-modal="true"
        aria-label={props.ariaLabel}
        onKeyDown={onKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <Show when={props.title}>
          <h2 class="ui-prompt__title">{props.title}</h2>
        </Show>
        <div class="ui-prompt__body">{props.children}</div>
        <div class="ui-prompt__actions" data-layout={props.actionsLayout ?? 'row'}>
          {props.actions}
        </div>
      </section>
    </div>
  )

  return (
    <Show when={props.open}>
      {/* `keyed` because Portal reads `mount` once — the same reason ToastHost
          keys its own host. `host` only changes when the prompt opens, so this
          never re-creates a panel the user is typing into. */}
      <Show when={host()} keyed fallback={panel()}>
        {(el) => <Portal mount={el}>{panel()}</Portal>}
      </Show>
    </Show>
  )
}
