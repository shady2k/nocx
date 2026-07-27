// ═══════════════════════════════════════════════════════════════════════════
// TabContent seam — content types implement this interface; Tab owns the
// chrome and delegates lifecycle to the content. Polymorphism lives in the
// content, not in Tab subclasses (B.2).
// ═══════════════════════════════════════════════════════════════════════════

// ── Branded identity types (B.7) ──────────────────────────────────────────

/** Serializable content type, used in restore descriptors and deep links.
 *  Registered at the composition root, stable across releases. */
export type SurfaceType = string & { readonly __brand: 'SurfaceType' }

/** Optional deduplication key for singleton content (Connections, Settings).
 *  Registered at the composition root, stable across releases. */
export type SingletonKey = string & { readonly __brand: 'SingletonKey' }

// ── Registered surface constants ──────────────────────────────────────────

export const SURFACE_TERMINAL: SurfaceType = 'nocx.terminal' as SurfaceType

// ── Geometry (B.5) ────────────────────────────────────────────────────────

/** CSS-pixel viewport delivered from the presentation layer to the content.
 *  Content MUST NOT interpret container geometry itself. */
export interface ContentViewport {
  width: number
  height: number
  devicePixelRatio: number
}

// ── Host (B.4) ────────────────────────────────────────────────────────────

/**
 * Scoped to one mounted tab. All methods become inert after the tab is
 * disposed, so late async callbacks cannot mutate recycled UI (B.6).
 */
export interface TabHost {
  setTitle(title: string): void
  requestAttention(): void
  requestClose(): void
}

// ── Content (B.4, B.6) ────────────────────────────────────────────────────

export interface TabContent {
  /**
   * Called at most once per content instance. `signal` is aborted when the
   * tab is disposed during mount — the implementation MUST stop and tear
   * down partial resources (B.6).
   */
  mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void>

  /** Delivered by the presentation layer after layout measurement.
   *  Never called before mount starts; suppressed after disposal. */
  viewportChanged(viewport: ContentViewport): void

  /** Focus the content's primary input element.
   *  Only called after a successful mount, or safely queued (B.6). */
  focus(): void

  /** Tear down all resources. Idempotent. Cancels an in-flight mount
   *  through the AbortSignal passed to mount() (B.6). */
  dispose(): void

  /** Show or hide the content without remounting it.
   *  Never triggers a remount, WebGL context churn, or session teardown. */
  setVisible(visible: boolean): void

  /**
   * Pre-set the mount target element so setVisible is meaningful before
   * mount. Called by Tab constructor before any activation. Implementations
   * that don't need a DOM target (e.g. a future Solid surface managing
   * visibility through signals) MAY implement this as a no-op. Contents
   * extending BaseTabContent get the default impl that stores _target.
   */
  setTarget(target: HTMLElement): void
}

/**
 * Common base for DOM-based TabContent implementations. Stores the mount
 * target and implements setVisible by toggling the 'active' class on it.
 * Override setVisible only when the implementation genuinely needs
 * different visibility semantics (e.g. a future Solid surface that manages
 * visibility through signals). Every new TabContent must extend this or
 * provide its own setVisible.
 *
 * The mount target is set by setTarget() before mount, so setVisible is
 * meaningful from the first activation call. Implementations MUST NOT
 * store _target themselves — BaseTabContent owns it.
 */
export abstract class BaseTabContent implements TabContent {
  protected _target: HTMLElement | null = null

  /**
   * Whether this content is the active tab's, as the chrome told us.
   *
   * The authoritative answer, and the reason it exists: setVisible toggles an
   * 'active' CSS class for presentation, and code that needs to know whether it
   * is active was reading that class back out of the DOM (nocx-fttm). A class is
   * a rendering detail — anything may add or remove it — so asking the DOM what
   * the application state is inverts the direction the information travels.
   */
  protected _active = false

  /**
   * Pre-set the mount target so setVisible is meaningful before mount.
   * Called by Tab constructor. Idempotent — subsequent calls after the
   * first are no-ops. Implementations override this only when they need
   * to intercept target assignment.
   */
  setTarget(target: HTMLElement): void {
    if (this._target) return
    this._target = target
  }

  abstract mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void>
  abstract viewportChanged(viewport: ContentViewport): void
  abstract focus(): void
  abstract dispose(): void

  setVisible(visible: boolean): void {
    this._active = visible
    if (this._target) {
      this._target.classList.toggle('active', visible)
    }
  }
}

// ── Model state (B.8) ─────────────────────────────────────────────────────

/**
 * Policies that replace `kind` tests. Every tab carries a descriptor;
 * TabManager reads it to decide restore, attention, and default-title
 * behaviour without asking what kind of content is inside.
 */
export interface ContentDescriptor {
  /** Stable identity for restore descriptors and deep links. */
  readonly surfaceType: SurfaceType

  /** If set, only one tab with this key may be open at a time. */
  readonly singletonKey: SingletonKey | null

  readonly restoreDescriptor: unknown

  /** Whether this content can produce attention-worthy events (bell, exit). */
  readonly supportsAttention: boolean

  /** Fixed title for view tabs; ignored when the title is set dynamically
   *  through TabHost.setTitle. */
  readonly defaultTitle: string
}
