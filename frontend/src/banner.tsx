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
 *
 * SolidJS implementation (nocx-njrx.6): renders via solid-js/web render()
 * into #panes, cleans up with the returned dispose function.
 */
import { render } from 'solid-js/web'
import { Button } from './ui/button'

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
   * user's choice once they interact. While the banner is on screen,
   * further calls resolve immediately with 'dismiss' so a loop cannot
   * stack multiple banners.
   */
  show(): Promise<BannerChoice>
}

/**
 * Solid component that renders the clipboard banner UI.
 * Receives a callback for each of the three choices.
 */
function ClipboardBannerComponent(props: { onChoice: (choice: BannerChoice) => void }) {
  return (
    <div class="clipboard-banner">
      <span class="clipboard-banner-message">
        A terminal program tried to write to your clipboard. This is disabled by default for
        security reasons, to protect against malicious software.
      </span>
      <div class="clipboard-banner-actions">
        <Button
          class="clipboard-banner-btn clipboard-banner-allow"
          variant="primary"
          onClick={() => props.onChoice('allow')}
        >
          Allow clipboard writes
        </Button>
        <Button
          class="clipboard-banner-btn clipboard-banner-suppress"
          onClick={() => props.onChoice('suppress')}
        >
          Don't show again
        </Button>
        <Button
          class="clipboard-banner-btn clipboard-banner-dismiss"
          variant="close"
          ariaLabel="Dismiss"
          onClick={() => props.onChoice('dismiss')}
        >
          ✕
        </Button>
      </div>
    </div>
  )
}

/**
 * Real banner implementation — renders a Solid component into #panes
 * and self-disposes on choice, matching the imperative predecessor's
 * DOM classes and behaviour exactly.
 */
export class ClipboardBannerImpl implements ClipboardBanner {
  private _shown = false
  private _dispose: (() => void) | null = null
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
      const container = document.getElementById('panes')
      if (!container) {
        // No #panes element — banner cannot render. Resolve immediately
        // and reset state so the next show() can try again.
        this._shown = false
        this._resolve = null
        resolve('dismiss')
        return
      }
      this._dispose = render(
        () => <ClipboardBannerComponent onChoice={(choice) => this._decide(choice)} />,
        container,
      )
    })
  }

  private _decide(choice: BannerChoice): void {
    if (this._dispose) {
      this._dispose()
      this._dispose = null
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
