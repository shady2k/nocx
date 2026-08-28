// ═══════════════════════════════════════════════════════════════════════════
// The frozen surface's half of the gesture: ⌘/⌃-click on a decorated link in
// the DOM scrollback.
//
// Modifier-gated, and not by preference. This terminal copies on select and
// selects a word on double-click, so a plain click already MEANS something
// here; making it also mean "open" would give one gesture two owners, which
// is the defect AGENTS.md names — the loser goes on advertising what it can
// no longer deliver. ⌘-click is unclaimed, and it is what the platform's
// terminals already teach.
//
// The mousedown is suppressed as well as the click. Selection begins on
// mousedown, so without it an armed click would open the file AND leave the
// word selected and on the clipboard — the copy the user was trying to stop
// doing.
// ═══════════════════════════════════════════════════════════════════════════

import type { ActiveOrigin } from '../pane-content'
import { LINK_CLASS, linkTargetOf } from './decorate'
import type { LinkOpener } from './open'
import type { ArmedTracker } from './armed'

/** Set on the attached root while the modifier is held — the CSS hook that
 *  makes links look clickable exactly when they are. */
export const ARMED_CLASS = 'links-armed'

export interface LinkSurfaceDeps {
  readonly opener: LinkOpener
  /** The origin of the tab this root belongs to, read at CLICK time: a tab's
   *  cwd moves, and a link clicked now resolves against where the shell is
   *  now, not where it was when the block froze. */
  readonly origin: () => Omit<ActiveOrigin, 'paneId'> | null
  readonly armed: ArmedTracker
}

/**
 * Attach link activation to one tab's scrollback. Returns a detach function;
 * a tab that is disposed must call it, or the listeners outlive the pane and
 * a later click resolves against an origin that is gone.
 */
export function attachLinkClicks(root: HTMLElement, deps: LinkSurfaceDeps): () => void {
  const apply = (armed: boolean): void => {
    root.classList.toggle(ARMED_CLASS, armed)
  }
  apply(deps.armed.armed())
  const unsubscribe = deps.armed.subscribe(apply)

  const linkAt = (e: MouseEvent): HTMLElement | null => {
    if (!(e.metaKey || e.ctrlKey)) return null
    const el = e.target
    if (!(el instanceof Element)) return null
    return el.closest<HTMLElement>(`.${LINK_CLASS}`)
  }

  // Capture phase, both: the block's own mousedown handler owns double-click
  // word selection and would run first otherwise.
  const onMouseDown = (e: MouseEvent): void => {
    if (e.button !== 0 || linkAt(e) === null) return
    e.preventDefault()
    e.stopPropagation()
  }

  const onClick = (e: MouseEvent): void => {
    if (e.button !== 0) return
    const el = linkAt(e)
    if (el === null) return
    const target = linkTargetOf(el)
    if (target === null) return
    e.preventDefault()
    e.stopPropagation()
    void deps.opener.open(target, deps.origin())
  }

  root.addEventListener('mousedown', onMouseDown, true)
  root.addEventListener('click', onClick, true)

  return () => {
    unsubscribe()
    root.classList.remove(ARMED_CLASS)
    root.removeEventListener('mousedown', onMouseDown, true)
    root.removeEventListener('click', onClick, true)
  }
}
