import { Show, createEffect, createMemo, onCleanup, untrack, type JSX } from 'solid-js'
import { Portal } from 'solid-js/web'
import {
  overlayHostBelow,
  popOverlay,
  pushOverlay,
  restoreFocus,
  topOverlay,
  type OverlayEntry,
} from './overlay/stack'

export interface PromptProps {
  open: boolean
  title?: string
  ariaLabel: string
  placement?: 'floating' | 'top-sheet'
  /**
   * Whether this question OWNS the surface under it. Default true: a scrim, a
   * click that stops at the panel, the keyboard, and Escape — which is what a
   * vault unlock or a password has always been.
   *
   * `false` is the question a person may also answer IN WORDS: the agent
   * approval's, whose composer is the other half of its answer (ADR-anntnxdj
   * decision 4 — otherwise they write "no, do it differently" and the agent
   * sits waiting on a button, which is the defect the scrim and the focus
   * guard made unavoidable). Nothing is dimmed, a press outside reaches what
   * is under it, the keyboard stays with whoever the person gives it to, and
   * Escape belongs to that surface — the panel answers its own Escape.
   */
  modal?: boolean
  /** Compact rhythm for dense decision panels; the default keeps modal spacing. */
  density?: 'default' | 'compact'
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

/** The three numbers a scroll container reports about itself. */
export interface ScrollBox {
  scrollTop: number
  clientHeight: number
  scrollHeight: number
}

/**
 * Which edges of a scroll container are cut — content hidden above the top,
 * below the bottom, or both.
 *
 * Pure, and exported, because it is the only part of the fade that can be
 * checked without a layout engine: jsdom reports zero for all three numbers,
 * so a test that renders a Prompt and looks at the result learns nothing about
 * the arithmetic. `e2e/prompt-height.spec.ts` supplies the other half.
 *
 * The one-pixel tolerance is not slack: a scroll container's numbers are
 * fractional under a non-integer device pixel ratio, and a half-pixel
 * remainder is not "there is more to read". Announcing one would be exactly
 * the lie the fade exists to avoid.
 */
export function overflowEdges(box: ScrollBox): { start: boolean; end: boolean } {
  return {
    start: box.scrollTop > 1,
    end: box.scrollTop + box.clientHeight < box.scrollHeight - 1,
  }
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
  let body: HTMLDivElement | undefined
  let entry: OverlayEntry | null = null
  let removeFocusGuard: (() => void) | null = null
  /**
   * The overlay this prompt renders INSIDE while open, or null to render in
   * its own place. A modal `<dialog>` lives in the browser's top layer, which
   * is above every z-index in the normal layer by definition — being on top of
   * a top-layer element is not a number, it is a parent. So a prompt opened
   * over something must be a DOM child of it, which is also how the connection
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
   * IT IS THE ENTRY BELOW US, NOT THE TOP ONE, and it is re-read whenever the
   * stack changes. A host is not permanent: the connection overlay is a modal
   * `<dialog>` that closes ITSELF when the socket comes up, and a prompt raised
   * while it was up is a DOM child of it — so `dialog:not([open])`'s
   * `display: none` took the prompt off the screen with the connection screen,
   * still open and still on the overlay stack. That is the vault unlock
   * question vanishing in front of the person it was asked of, on the first
   * connection after an upgrade (nocx-1ebrs.7). Reading the stack reactively
   * is what lets the prompt follow its host down: to the next overlay below,
   * and to its own place when there is none.
   */
  const host = createMemo(() => (props.open ? overlayHostBelow(entry) : null))

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
        // A prompt that does not own the surface leaves Escape to whatever the
        // person is in; its own panel answers Escape from inside (onKeyDown).
        { escape: props.modal !== false },
      )
      // Prompt is the keyboard owner while open, unless it is not modal at all
      // (see PromptProps.modal): then it must not take the keyboard from the
      // composer it is asking about. Pane activation can finish the same turn
      // that raised this prompt and focus the composer after the initial focus
      // below; reclaim that attempt before it reaches the editor's focus
      // listener.
      if (element && props.modal !== false) {
        const keepFocus = (e: FocusEvent) => {
          if (topOverlay() !== entry || element?.contains(e.target as Node)) return
          e.stopPropagation()
          focusInitial(element)
        }
        document.addEventListener('focusin', keepFocus, true)
        removeFocusGuard = () => document.removeEventListener('focusin', keepFocus, true)
        focusInitial(element)
      }
    } else if (!props.open && entry) {
      removeFocusGuard?.()
      removeFocusGuard = null
      popOverlay(entry)
      restoreFocus(entry)
      entry = null
    }
  })

  /**
   * Mark the edges where the body is cut, so a reader can tell there is more.
   *
   * The panel now stops at the bottom of the window and the body scrolls
   * inside it (prompt.css), which removes the one thing that told a reader
   * the content continues: a panel running off the screen at least looked
   * wrong. A body clipped at a hard edge looks finished. The fade painted
   * from these attributes is what puts the fact back — the same answer, and
   * the same argument, as the tab strip's horizontal fade (tab-strip.css):
   * an edge that says "there is more" when there is not cannot be checked by
   * looking, so it is MEASURED rather than painted unconditionally.
   */
  const markOverflow = () => {
    const el = body
    if (!el) return
    const { start, end } = overflowEdges(el)
    el.toggleAttribute('data-overflow-start', start)
    el.toggleAttribute('data-overflow-end', end)
  }

  /**
   * Three things change the answer, and none of them subsumes another:
   * scrolling (the reader moves within the same content), the body's own box
   * (the window is resized — the cap is expressed against the viewport), and
   * the content (a Show branch arrives, an expanded command streams in). A
   * ResizeObserver on the body cannot see the third: the body is capped, so
   * its own box does not change when what is inside it grows.
   */
  createEffect(() => {
    const el = props.open ? body : undefined
    if (!el) return
    markOverflow()
    el.addEventListener('scroll', markOverflow, { passive: true })
    const box = new ResizeObserver(markOverflow)
    box.observe(el)
    const content = new MutationObserver(markOverflow)
    content.observe(el, { childList: true, subtree: true, characterData: true })
    onCleanup(() => {
      el.removeEventListener('scroll', markOverflow)
      box.disconnect()
      content.disconnect()
    })
  })

  onCleanup(() => {
    removeFocusGuard?.()
    removeFocusGuard = null
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
    // A prompt that does not own the surface takes Escape only from INSIDE
    // itself: out in the composer it belongs to the composer (the overlay
    // stack's document handler skips this prompt entirely).
    if (e.key === 'Escape' && props.modal === false) {
      e.preventDefault()
      props.onClose()
      return
    }
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
      data-modal={props.modal === false ? 'false' : undefined}
      /*
       * Which prompt this is, on the element that swallows the click.
       *
       * The overlay is the scrim: it covers the whole host and it is what a
       * pointer hits, so when a prompt is open over something a test was
       * driving, this div is the obstacle Playwright names — and it named it
       * only as `<div class="ui-prompt-overlay" data-placement="top-sheet">`,
       * which five product surfaces render identically (vault setup and
       * unlock, the key passphrase, the connection password, the agent
       * approval). The accessible name sits on the `<section>` inside, where
       * an interception message never reaches. nocx-6wgcw was one such click,
       * reproducible once per 44-minute containerised run, and the trace could
       * not say which prompt had been in the way.
       *
       * The value is `ariaLabel` and nothing else, so the name a screen reader
       * announces and the name a trace prints cannot drift apart; the
       * alternative — a `data-name` prop each caller passes — is a second
       * string to keep in step with the first, and the callers already pass
       * this one. Blank means the prompt genuinely has no name yet, and the
       * attribute is then absent rather than empty: `data-prompt=""` in a
       * trace reads as a prompt called "", while its absence says truthfully
       * that the dialog under it is unlabelled too, which is its own defect.
       */
      data-prompt={props.ariaLabel || undefined}
      onMouseDown={(event) => {
        // A press round the panel is a refusal only while this prompt OWNS the
        // surface: otherwise the person is reaching past it for the composer,
        // and answering a question they did not mean to touch would be the
        // scrim deciding something nobody said.
        if (props.modal === false) return
        if (event.target === event.currentTarget) props.onClose()
      }}
    >
      <section
        class="ui-prompt"
        data-placement={props.placement ?? 'floating'}
        data-density={props.density === 'compact' ? 'compact' : undefined}
        role="dialog"
        aria-modal={props.modal === false ? 'false' : 'true'}
        aria-label={props.ariaLabel}
        onKeyDown={onKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <Show when={props.title}>
          <h2 class="ui-prompt__title">{props.title}</h2>
        </Show>
        <div ref={body} class="ui-prompt__body">
          {props.children}
        </div>
        <div class="ui-prompt__actions" data-layout={props.actionsLayout ?? 'row'}>
          {props.actions}
        </div>
      </section>
    </div>
  )

  return (
    <Show when={props.open}>
      <PromptSurface host={host} panel={panel} />
    </Show>
  )
}

/**
 * Where the panel is mounted, and the component that exists for exactly as long
 * as this prompt is open.
 *
 * THE PANEL IS BUILT HERE, in the render pass, and NOT as the Portal's
 * children. `Portal` defers its children to its own effect, and effects run in
 * the order they were registered — the one in [Prompt] that pushes this prompt
 * onto the overlay stack is registered first, so a panel built inside the
 * Portal would not exist yet. Pushed without its element, the entry has nothing
 * for the toast host to render into and nothing for the caret policy to focus,
 * and the panel is a stranger to the stack it is on.
 *
 * A component rather than inline JSX so the panel is built once per OPENING:
 * closing disposes it, and the next opening builds a fresh one.
 */
function PromptSurface(props: { host: () => HTMLElement | null; panel: () => JSX.Element }) {
  /** The place this prompt was declared: the mount that outlives every overlay
   *  above it, and what it renders into when there is none. */
  let home: HTMLDivElement | undefined
  // `untrack`: the panel is built ONCE per opening, and `panel` is a closure
  // over this prompt's refs rather than a value that can change under it. Read
  // tracked, the rule that wants every prop read inside a reactive scope is
  // right to complain — and the answer is to say which read this is, not to
  // move the build somewhere it would run again.
  const content = untrack(() => props.panel())
  return (
    <>
      {/* `display: contents` keeps this anchor out of layout (prompt.css). */}
      <div class="ui-prompt-home" ref={home} />
      {/* `Portal` re-parents the SAME panel when its mount changes — it removes
          its own container from the old host and inserts the same children into
          a container in the new one — so following a host down keeps the node,
          and with it whatever the person has typed. */}
      <Portal mount={props.host() ?? home}>{content}</Portal>
    </>
  )
}
