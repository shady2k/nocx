/**
 * Clipboard banner — raised on a blocked OSC 52 write, offering
 * allow / don't-show-again / dismiss. Visibility comes from announcing the
 * block, exactly as Warp does: the remedy sits in the banner rather than in
 * a settings hunt.
 *
 * The three outcomes are deliberately different, and conflating them was a
 * real bug: dismiss means "not now", so the next blocked write asks again;
 * only "don't show again" is permanent, and only for this run. While the
 * banner is on screen, further blocked writes are dropped without stacking
 * a second one.
 *
 * It overlays the top of the terminal area, not the window — the tab strip
 * is the title bar, so an overlay there hides the tabs and the traffic
 * lights — and it never takes layout space, because shrinking the terminal
 * reflows the grid down to the PTY.
 */

/** The three banner outcomes the caller acts on. */
export type BannerChoice = 'allow' | 'suppress' | 'dismiss'

/**
 * Injectable banner interface — the real implementation manipulates the
 * DOM; tests inject a fake to avoid jsdom layout and to control the
 * outcome directly.
 */
export interface ClipboardBanner {
  /** True once the banner has been shown in this run. */
  readonly shown: boolean

  /**
   * Raise the banner across the top of the window. Resolves with the
   * user's choice when one of the three affordances is clicked. The
   * banner self-removes after the choice.
   *
   * If the banner is already showing, the call is a no-op that resolves
   * immediately with 'dismiss' — a program emitting the sequence in a
   * loop must not stack banners.
   */
  show(): Promise<BannerChoice>
}

/**
 * Real banner implementation — creates, shows and self-removes a DOM
 * element at the top of the window. Matches the existing CSS idiom
 * (tabbar colours, font stack, corner-radius).
 */
export class ClipboardBannerImpl implements ClipboardBanner {
  private _shown = false
  private _el: HTMLElement | null = null
  private _resolve: ((choice: BannerChoice) => void) | null = null

  get shown(): boolean {
    return this._shown
  }

  show(): Promise<BannerChoice> {
    // Already showing — a second blocked write must not stack a second
    // banner. Return a no-op that resolves immediately so the caller does
    // not block on a promise that will never settle.
    if (this._shown) {
      return Promise.resolve('dismiss')
    }

    this._shown = true

    return new Promise<BannerChoice>((resolve) => {
      this._resolve = resolve
      this._el = this._create()
      // Overlay inside #panes, which is position:relative. Two constraints
      // meet here and only this satisfies both:
      //   - not over the top of the window: the tab strip IS the title bar
      //     (nocx-cpp), so an overlay there hides the tabs and the traffic
      //     lights along with them.
      //   - not in the layout flow either: taking vertical space resizes the
      //     terminal grid, which reflows the PTY and repaints whatever
      //     full-screen program is running. A notice must not resize the
      //     terminal underneath it.
      document.getElementById('panes')?.append(this._el)
    })
  }

  private _create(): HTMLElement {
    const banner = document.createElement('div')
    banner.className = 'clipboard-banner'

    const msg = document.createElement('span')
    msg.className = 'clipboard-banner-message'
    msg.textContent =
      'A terminal program tried to write to your clipboard. ' +
      'This is disabled by default for security reasons, to protect against malicious software.'

    const actions = document.createElement('div')
    actions.className = 'clipboard-banner-actions'

    const allow = document.createElement('button')
    allow.className = 'clipboard-banner-btn clipboard-banner-allow'
    allow.textContent = 'Allow clipboard writes'
    allow.addEventListener('click', () => this._decide('allow'))

    const suppress = document.createElement('button')
    suppress.className = 'clipboard-banner-btn clipboard-banner-suppress'
    suppress.textContent = "Don't show again"
    suppress.addEventListener('click', () => this._decide('suppress'))

    const dismiss = document.createElement('button')
    dismiss.className = 'clipboard-banner-btn clipboard-banner-dismiss'
    dismiss.textContent = '✕'
    dismiss.setAttribute('aria-label', 'Dismiss')
    dismiss.addEventListener('click', () => this._decide('dismiss'))

    actions.append(allow, suppress, dismiss)
    banner.append(msg, actions)
    return banner
  }

  private _decide(choice: BannerChoice): void {
    if (this._el) {
      this._el.remove()
      this._el = null
    }
    // Lower the flag: it means "a banner is on screen right now", nothing
    // more. Leaving it raised turned dismiss into a permanent silence —
    // every later blocked write was dropped without ever asking again, so
    // ✕ and "don't show again" became the same button and the user could
    // never reach the allow action afterwards. Only `suppressed` on the
    // gate is permanent, and only the user can set it.
    //
    // A program hammering OSC 52 in a loop can therefore re-raise the
    // banner as fast as it is dismissed. That is what "don't show again"
    // is for; a cooldown would be guessing at a duration nobody has
    // measured.
    this._shown = false
    if (this._resolve) {
      this._resolve(choice)
      this._resolve = null
    }
  }
}
