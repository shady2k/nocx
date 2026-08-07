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

// ── Origin (B.9) ──────────────────────────────────────────────────────────

/**
 * The machine a tab's content speaks for — the scope of origin-following
 * surfaces like the Files panel (design §5.4). Composed by TabManager, which
 * owns the tab: the content answers the capability below with everything it
 * knows about itself, and `tabId` is added by the one place that knows it —
 * a content instance is constructed before the Tab that numbers it exists.
 */
export interface ActiveOrigin {
  /** The tab that owns this origin — added by TabManager. */
  tabId: number
  /** The backend session this origin resolves to; `files.open` is the only
   *  method that takes it, so the wrong pairing stays inexpressible (§5.2). */
  sessionId: string
  /** How the session was opened: 'ssh' for a session opened through the SSH
   *  open path, 'local' otherwise. Never inferred from the cwd. */
  kind: 'local' | 'ssh'
  /** The shell's current working directory (provider syntax), or null when
   *  unknown — no session cwd yet, or inside an environment whose directory
   *  is not knowable. */
  cwd: string | null
  /** True only when `cwd` came from a verified OSC 7 report (AD-5): the one
   *  cwd a composition layer may hand to `files.open` as `rootPath` (D2). A
   *  session-open cwd is the provider's fallback question, not a claim. */
  cwdVerified: boolean
  /** Whether origin-following surfaces should follow this origin's cwd.
   *  A terminal says yes — the shell's verified OSC 7 cwd is the panel's
   *  reveal target. A viewer tab carries a FROZEN origin (the machine the
   *  file was opened from) and explicitly has NO opinion about where we
   *  are now: it says no, so activating a viewer never moves the panel —
   *  and its frozen cwd is a snapshot, never a claim, so it must not be
   *  treated as one. The store checks this before cwd, so the viewer's
   *  stale cwd value is never consulted. */
  cwdFollow: boolean
  /** Host label for the machine this origin speaks for, or null for a local
   *  session. Carried so provenance can ride a surface's output — the file
   *  viewer titles a remote file "host · name" and a local file by its
   *  basename alone, and a null host on an ssh tab would make a remote file
   *  look local (the marker asymmetry backwards). Never inferred from the
   *  cwd; it is how the session was opened. */
  host: string | null
}

// ── Host (B.4) ────────────────────────────────────────────────────────────

/**
 * Scoped to one mounted tab. All methods become inert after the tab is
 * disposed, so late async callbacks cannot mutate recycled UI (B.6).
 */
export interface TabHost {
  setTitle(title: string): void
  /** Update the tab's tooltip (ADR-0019 §3.3 — sandbox tooltip). */
  updateTooltip(tooltip: string): void
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
  /**
   * Optional capability: the machine this content speaks for, for surfaces
   * that follow the ACTIVE tab (the Files panel, design §5.4). Terminal
   * content answers from its session; viewer content answers from the
   * binding it was opened with. `tabId` is deliberately absent — the
   * content does not know its tab; TabManager adds it when it composes the
   * `ActiveOrigin` for the accessor it hands the shell.
   *
   * Returns null when there is no answer: no session yet, a closed
   * session, or an environment the content cannot speak for (a hand-typed
   * `ssh` inside a local tab — naming the local session there would show
   * one machine's files while the user acts on another's, §0).
   */
  activeOrigin?(): Omit<ActiveOrigin, 'tabId'> | null
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
