// ═══════════════════════════════════════════════════════════════════════════
// PaneContent seam — content types implement this interface; Tab owns the
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
 * surfaces like the Files panel (design §5.4). Composed by PaneManager, which
 * owns the tab: the content answers the capability below with everything it
 * knows about itself, and `paneId` is added by the one place that knows it —
 * a content instance is constructed before the Tab that numbers it exists.
 */
export interface ActiveOrigin {
  /** The tab that owns this origin — added by PaneManager. */
  paneId: number
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
  /**
   * The machine this origin speaks for, as a person NAMES it — the string
   * `machine-name.ts` produces (`user@host`, or "This machine"), and the
   * same one the tab strip's second line shows for this tab.
   *
   * Distinct from `host`, which is the bare hostname a surface compares or
   * prefixes a filename with. This one is for telling a person which
   * machine, in a place where the tab is not in front of them: the
   * operations list is global, one list for every tab, so a row naming a
   * path and no machine answers nothing once two connections are open.
   *
   * Optional because only a LIVE terminal origin can answer it. A viewer or
   * diff tab carries a frozen origin and no live user to name, and says so
   * by carrying nothing rather than by naming a machine it is no longer on.
   */
  machine?: string
}

// ── Host (B.4) ────────────────────────────────────────────────────────────

/**
 * Scoped to one mounted tab. All methods become inert after the tab is
 * disposed, so late async callbacks cannot mutate recycled UI (B.6).
 */
export interface PaneHost {
  setTitle(title: string): void
  /** Update the pane's tooltip (ADR-0036 §3.3 — sandbox tooltip). */
  updateTooltip(tooltip: string): void
  requestAttention(): void
  requestClose(): void
  /**
   * THE OPENING IS OVER — from here, output is the user's to have missed.
   *
   * A tab lights an unread-activity mark for output that arrived while
   * nobody was looking at it, and until this is called there is no such
   * output: a session that has just opened prints a banner, its rc file's
   * chatter and a prompt, and none of it is news. The distinction cannot be
   * drawn from the bytes — they are output like any other — and it cannot be
   * drawn with a clock either: a restore opens every pane at once, so a
   * pane's own start is separated from the pane before it by however long
   * ten other shells took to start. It is drawn HERE, by the content, which
   * is the only party that knows what its own start looks like.
   *
   * Called at most once, and the host clears anything the opening
   * accumulated: the state where every tab but one carries the mark is the
   * state where the mark says nothing at all.
   */
  contentSettled(): void
}

// ── Content (B.4, B.6) ────────────────────────────────────────────────────

export interface PaneContent {
  /**
   * Called at most once per content instance. `signal` is aborted when the
   * tab is disposed during mount — the implementation MUST stop and tear
   * down partial resources (B.6).
   */
  mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void>

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
   * extending BasePaneContent get the default impl that stores _target.
   */
  setTarget(target: HTMLElement): void
  /**
   * Optional capability: the machine this content speaks for, for surfaces
   * that follow the ACTIVE tab (the Files panel, design §5.4). Terminal
   * content answers from its session; viewer content answers from the
   * binding it was opened with. `paneId` is deliberately absent — the
   * content does not know its tab; PaneManager adds it when it composes the
   * `ActiveOrigin` for the accessor it hands the shell.
   *
   * Returns null when there is no answer: no session yet, a closed
   * session, or an environment the content cannot speak for (a hand-typed
   * `ssh` inside a local tab — naming the local session there would show
   * one machine's files while the user acts on another's, §0).
   */
  activeOrigin?(): Omit<ActiveOrigin, 'paneId'> | null

  /**
   * Optional capability: the session this content holds and the session that
   * opened it, as the backend ADMITTED the edge (nocx-9hu9d). `label` is
   * deliberately absent — the content does not know what the strip calls its
   * tab; PaneManager adds it when it composes the `LineageNode`.
   *
   * Returns null when the content holds no session — a settings or viewer
   * tab, or a terminal whose open has not answered yet.
   *
   * This is NOT `activeOrigin` under another name, and the two must not be
   * merged. `activeOrigin` answers "which machine does this tab speak for",
   * and answers null in cases where the tab plainly holds a session — a
   * hand-typed `ssh` inside a local tab is the documented one. That silence
   * is right for a Files panel and wrong here: a tab left running is left
   * running whichever machine it is talking to, and a lineage question that
   * inherited that null would under-report exactly the sessions a person
   * most needs to be told about.
   *
   * PROVENANCE ONLY (ADR-0020 §5). Nothing may read this to decide what one
   * tab may do to another; see lineage.ts.
   */
  lineage?(): { sessionId: string; parentSessionId: string | null } | null

  /**
   * Optional capability: what is LIVE in this content right now — the command
   * running in the foreground, and the machine the content is talking to.
   * Read by the close prompts, which name what dies before it dies
   * (nocx-isoph.6, design D6). `label` is deliberately absent for the same
   * reason it is absent above: the content does not know what the strip calls
   * its tab, and the layer that owns the strip composes the two.
   *
   * Returns null when the content holds no live session — a settings or
   * viewer tab, a terminal whose open has not answered yet, or one whose
   * shell has exited. Both fields null is a DIFFERENT answer and an honest
   * one: a session sitting at a local prompt, which closes without losing
   * anything (live-work.ts says why that does not count as live).
   *
   * NOT `activeOrigin` and NOT `lineage`, for two different reasons.
   * `activeOrigin` answers which MACHINE a tab speaks for, for surfaces that
   * follow the active tab — so it is deliberately frozen on a viewer and
   * deliberately silent where it cannot speak for the machine in front of the
   * user, and a close prompt built on it would both name a machine nothing is
   * running on and go quiet exactly where it most needs to speak. That is the
   * same trap `lineage` refused to walk into; read its note above first.
   * `lineage` itself answers what is OPEN — it speaks for
   * a tab whose shell has already exited, because that tab is still on screen
   * and its owner is owed the truth about it — where this answers what is
   * RUNNING, and an exited shell is not.
   */
  liveWork?(): { command: string | null; host: string | null } | null
}

/**
 * Common base for DOM-based PaneContent implementations. Stores the mount
 * target and implements setVisible by toggling the 'active' class on it.
 * Override setVisible only when the implementation genuinely needs
 * different visibility semantics (e.g. a future Solid surface that manages
 * visibility through signals). Every new PaneContent must extend this or
 * provide its own setVisible.
 *
 * The mount target is set by setTarget() before mount, so setVisible is
 * meaningful from the first activation call. Implementations MUST NOT
 * store _target themselves — BasePaneContent owns it.
 */
export abstract class BasePaneContent implements PaneContent {
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

  abstract mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void>
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
 * PaneManager reads it to decide restore, attention, and default-title
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
   *  through PaneHost.setTitle. */
  readonly defaultTitle: string
}
