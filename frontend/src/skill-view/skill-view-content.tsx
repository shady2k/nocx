// ═══════════════════════════════════════════════════════════════════════════
// SkillViewContent — the skill tab (nocx-btg7d, nocx-4m1n1, nocx-dh14q): the
// changed warning above the split, then the identity rail beside the BODY —
// every file the bundle carries, the chosen one's bytes, and the Check group
// with what content.db knows about this skill plus the Check/Re-check button
// that spends the one model call.
//
// SkillViewHeader and SkillViewBody live in their own modules
// (skill-view-header.tsx, skill-view-body.tsx): this class owns none of
// their markup, only the lifecycle that feeds them — the store
// subscription, the resolved-skill identity, and the re-read on
// activation.
//
// A thin adapter over SolidPaneContent (extended, not re-implemented — it
// already owns the host element's lifecycle: creation as `.surface-host`,
// one Solid root, one disposal, and the `.pane:has(> .surface-host)` padding
// rule in styles/base.css that keeps this tab flush with its pane the way
// Settings and the API workbench already are). What this class adds is what
// only it knows: the store subscription, the resolved-skill identity, and
// the re-read on activation.
//
// IDENTITY IS THE RESOLVED SKILL, NEVER THE REQUESTED NAME. Two roots can
// discover a skill under one name; discovery keeps the first root's copy
// (internal/skill/discover.go), so the NAME this tab was opened with is only
// a first guess. Once the store answers, this content locks onto the
// resolved skill's PATH — unique per root — and never again matches by name
// alone: a same-named skill surfacing from a lower-precedence root after
// this one is deleted has a DIFFERENT path, and is a different skill, never
// silently adopted.
//
// THE TAB CLOSES WHEN ITS SKILL LEAVES THE LIST. The `Dialog` this surface
// replaces does exactly that (skills-section.tsx:265, "a card left open over
// a Delete closes rather than describing a skill that is not there") —
// deleting the Dialog must not delete the behaviour.
//
// THE TAB RE-READS THE STORE WHEN IT BECOMES VISIBLE. There is no change
// notification on the wire and each window builds its own SkillsStore
// (main.tsx:238); a modal was short-lived enough not to care, but a tab
// lives for days and would otherwise go on advertising a switch a second
// window flipped an hour ago.
// ═══════════════════════════════════════════════════════════════════════════

import { createSignal, Show, type JSX } from 'solid-js'
import { render } from 'solid-js/web'
import { SolidPaneContent, type PaneHost } from '../solid-pane-content'
import { showToast } from '../ui/toast'
import type { PinKind, Skill, SkillsState, SkillsStore } from '../skills-store'
import { SkillViewHeader, type ViewState } from './skill-view-header'
import { SkillViewBody } from './skill-view-body'
// Styling lives at styles/surfaces/skill-view.css, imported centrally from
// style.css (npm run lint fails on a stylesheet nothing imports) — the same
// arrangement notes.css uses, since this module's CSS is not beside it.

export interface SkillViewDeps {
  readonly store: SkillsStore
}

/**
 * The tab's whole content: the full-width warning slot always, then the body
 * once the skill has resolved. The resolved identity is rendered by the body
 * at the top of the left rail, beside Check and Files; the right side is
 * reserved for the selected document or check report.
 *
 * The body is not remounted by an unrelated store refresh (toggling the
 * switch, say): `Show`'s render function runs once per false→true
 * transition, not on every truthy update, so `SkillViewBody` keeps its
 * fetched files and its selection across a state change that leaves the
 * skill's PATH the same — only losing the skill (the tab closing) or a
 * fresh mount rebuilds it.
 */
function SkillView(props: {
  name: string
  state: ViewState
  busy: boolean
  onToggle: (enabled: boolean) => void | Promise<void>
  onPin: (pin: PinKind, on: boolean) => void | Promise<void>
  onApprove: () => void
  deps: SkillViewDeps
  refreshToken: number
}): JSX.Element {
  // The whole resolved skill, not only its name: the body's Check group
  // (nocx-dh14q) gates on `provenance` — a builtin offers no check at all —
  // and the identity rail reads the same resolved object the warning does.
  const readySkill = (): Skill | null => (props.state.kind === 'ready' ? props.state.skill : null)
  return (
    <div class="skill-view__header">
      <SkillViewHeader
        name={props.name}
        state={props.state}
        busy={props.busy}
        onApprove={props.onApprove}
      />
      <Show when={readySkill()}>
        {(skill) => (
          <SkillViewBody
            name={skill().name}
            skill={skill()}
            busy={props.busy}
            onPin={props.onPin}
            onToggle={props.onToggle}
            provenance={skill().provenance}
            store={props.deps.store}
            refreshToken={props.refreshToken}
          />
        )}
      </Show>
    </div>
  )
}

export class SkillViewContent extends SolidPaneContent {
  private paneHost: PaneHost | null = null
  private unsubscribeStore: (() => void) | null = null

  /** The resolved skill's PATH, once the store has answered for this name at
   *  least once — see the module comment. Null until then, which means "any
   *  skill of this name will do to resolve onto", never "nothing is open". */
  private resolvedPath: string | null = null

  private readonly viewState: () => ViewState
  private readonly setViewState: (state: ViewState) => void
  private readonly busy: () => boolean
  private readonly setBusy: (busy: boolean) => void
  /** Bumped on every `setVisible(true)` — SkillViewBody's own re-read
   *  effect is keyed on it (see its module comment). A plain counter
   *  rather than a boolean toggle: a signal only re-fires an effect when
   *  its VALUE changes, and two consecutive activations must both be seen
   *  even though "visible" is true both times. */
  private readonly visibleGeneration: () => number
  private readonly setVisibleGeneration: (updater: (g: number) => number) => number

  constructor(
    private readonly name: string,
    private readonly deps: SkillViewDeps,
  ) {
    super()
    const [viewState, setViewState] = createSignal<ViewState>({ kind: 'loading' })
    this.viewState = viewState
    this.setViewState = setViewState
    const [busy, setBusy] = createSignal(false)
    this.busy = busy
    this.setBusy = setBusy
    const [visibleGeneration, setVisibleGeneration] = createSignal(0)
    this.visibleGeneration = visibleGeneration
    this.setVisibleGeneration = setVisibleGeneration
  }

  // ── SolidPaneContent ──────────────────────────────────────────────────

  renderContent(root: HTMLElement): () => void {
    return render(
      () => (
        <SkillView
          name={this.name}
          state={this.viewState()}
          busy={this.busy()}
          onPin={(pin, on) => this.setPin(pin, on)}
          onToggle={(enabled) => this.toggle(enabled)}
          onApprove={() => void this.approve()}
          deps={this.deps}
          refreshToken={this.visibleGeneration()}
        />
      ),
      root,
    )
  }

  async mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this._disposed || this._hostElement) return
    if (signal.aborted) return
    this.paneHost = host
    await super.mount(target, host, signal)
    // Aborted while the root was opening: the base class has already
    // returned without mounting, so there is nothing to subscribe for.
    if (this._disposed || this._hostElement === null) return
    // Synchronous first call with whatever the store currently holds
    // (SkillsStore.subscribe), then every later change — including the one
    // `setVisible(true)` below asks for.
    this.unsubscribeStore = this.deps.store.subscribe((state) => this.onStoreState(state))
  }

  setVisible(visible: boolean): void {
    super.setVisible(visible)
    if (!visible) return
    // There is no change notification on the wire and each window builds its
    // own store (main.tsx:238) — a re-read on every activation is how a
    // long-lived tab stops advertising a switch a second window already
    // flipped.
    void this.deps.store.refresh()
    // The same re-read, for the body: bumping this is what tells
    // SkillViewBody to re-fetch the manifest (and whichever file is on
    // screen) rather than going on showing what they were the day the tab
    // opened.
    this.setVisibleGeneration((g) => g + 1)
  }

  /**
   * The tab closed. Unsubscribe first: the base class disposes the Solid
   * root and removes the host element, and a store notification arriving
   * after that would be writing into signals nothing reads any more.
   */
  dispose(): void {
    this.unsubscribeStore?.()
    this.unsubscribeStore = null
    super.dispose()
  }

  // viewportChanged is inherited as a no-op: the header and the body both
  // lay themselves out in flow/grid and answer their own scrolling. focus()
  // is also inherited — a tab activation has no single control to seize;
  // SkillViewBody's own file list and view own their internal focus moves
  // (↑/↓, Enter) once a person is inside them.

  // ── Store subscription ───────────────────────────────────────────────

  private onStoreState(state: SkillsState): void {
    if (this._disposed) return
    if (state.kind === 'loading') {
      this.setViewState({ kind: 'loading' })
      return
    }
    if (state.kind === 'unavailable') {
      this.setViewState({ kind: 'unavailable', message: state.message })
      return
    }
    const found = state.skills.find(
      (skill) =>
        skill.name === this.name &&
        (this.resolvedPath === null || skill.path === this.resolvedPath),
    )
    if (!found) {
      // Gone — deleted, or a same-named skill from a lower-precedence root
      // took the name over, which is a DIFFERENT skill (see module comment).
      // Either way this tab is not about anything any more: it closes
      // rather than describing nothing, the way the Dialog it replaces did.
      this.paneHost?.requestClose()
      return
    }
    this.resolvedPath = found.path
    this.paneHost?.setTitle(found.name)
    this.setViewState({ kind: 'ready', skill: found })
  }

  // ── The switch ────────────────────────────────────────────────────────

  private async toggle(enabled: boolean): Promise<void> {
    const state = this.viewState()
    if (state.kind !== 'ready' || this.busy()) return
    this.setBusy(true)
    try {
      await this.deps.store.setEnabled(state.skill.name, enabled)
    } catch (err) {
      // A late failure must not paint a disposed tab (B.6): nobody is
      // looking at a toast for a tab that no longer exists, and writing to
      // its signals afterward is exactly the "late async callback" case
      // PaneHost's methods are documented to go inert for.
      if (this._disposed) return
      showToast({ level: 'danger', message: err instanceof Error ? err.message : String(err) })
    } finally {
      if (!this._disposed) this.setBusy(false)
    }
  }

  private async setPin(pin: PinKind, on: boolean): Promise<void> {
    const state = this.viewState()
    if (state.kind !== 'ready' || this.busy()) return
    this.setBusy(true)
    try {
      await this.deps.store.setPin(state.skill.name, pin, on)
    } catch (err) {
      if (this._disposed) return
      showToast({ level: 'danger', message: err instanceof Error ? err.message : String(err) })
    } finally {
      if (!this._disposed) this.setBusy(false)
    }
  }

  // ── Re-approve (nocx-54a2c review) ───────────────────────────────────
  //
  // The row's own Re-approve icon does this same write; the header carries
  // it too because a person now DECIDES about a skill from this tab, and
  // "the bytes changed" is a danger card here with nowhere else to send the
  // press it names (see SkillViewHeader's own comment on the card).

  private async approve(): Promise<void> {
    const state = this.viewState()
    if (state.kind !== 'ready' || this.busy()) return
    this.setBusy(true)
    try {
      await this.deps.store.approve(state.skill.name)
    } catch (err) {
      // Same B.6 guard toggle() uses: a disposed tab paints nothing.
      if (this._disposed) return
      showToast({ level: 'danger', message: err instanceof Error ? err.message : String(err) })
    } finally {
      if (!this._disposed) this.setBusy(false)
    }
  }
}
