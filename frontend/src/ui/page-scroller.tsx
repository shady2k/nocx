/**
 * PageScroller — the content scroll owner (§6.2). Every page has exactly
 * one. Exposes `scrollToElement(el)` for deep-link navigation, replacing
 * `scrollIntoView()` which would scroll every scrollable ancestor.
 *
 * Renders `.ui-page__scroll`.
 *
 * NOTE: The prop is named `handle` (not `ref`) because Solid JSX
 * intercepts `ref` as a reserved attribute on components and never
 * passes it as a prop. Using `handle` avoids this conflict.
 *
 * The scroller element ref is captured via Solid's callback ref pattern
 * (`ref={(el) => { scrollEl = el }}`), NOT pre-declared let-binding,
 * because Solid does not assign to a pre-existing variable.
 */

import type { JSX } from 'solid-js'
import { createEffect } from 'solid-js'

export interface PageScrollerHandle {
  /** Scrolls the scroller so `el` is visible, using scroller-relative
   *  positioning. Unlike `scrollIntoView()`, this does NOT scroll any
   *  ancestor — only the owned scroll container moves. */
  scrollToElement(el: HTMLElement): void
}

export interface PageScrollerProps {
  /** Functional or object handle receiving the PageScrollerHandle. */
  handle?: PageScrollerHandle | ((h: PageScrollerHandle) => void)
  children: JSX.Element
}

export function PageScroller(props: PageScrollerProps) {
  let scrollEl: HTMLDivElement | undefined

  const handle: PageScrollerHandle = {
    scrollToElement(el: HTMLElement) {
      if (!scrollEl) return
      const top =
        el.getBoundingClientRect().top - scrollEl.getBoundingClientRect().top + scrollEl.scrollTop
      scrollEl.scrollTo({ top, behavior: 'smooth' })
    },
  }

  createEffect(() => {
    if (props.handle) {
      if (typeof props.handle === 'function') {
        props.handle(handle)
      } else {
        Object.assign(props.handle, handle)
      }
    }
  })

  return (
    <div
      ref={(el) => {
        scrollEl = el
      }}
      class="ui-page__scroll"
    >
      {props.children}
    </div>
  )
}
