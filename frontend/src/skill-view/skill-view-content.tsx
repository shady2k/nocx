// ═══════════════════════════════════════════════════════════════════════════
// SkillViewContent — the HEADER of the skill tab (nocx-btg7d). The two-pane
// body (a file list and a file view) and the check panel are later tasks;
// this content renders only what a person needs to answer "which skill is
// this, and is it on" — name, provenance, path, the enable switch, and a
// Check/Re-check button that is not wired yet (Task 10 spends the model
// call).
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
import { Badge, Button, Checkbox, FactList, Stack, StatusCard } from '../ui'
import { showToast } from '../ui/toast'
import { provenanceTone } from '../skills-presentation'
import type { Skill, SkillsState, SkillsStore } from '../skills-store'
// Styling lives at styles/surfaces/skill-view.css, imported centrally from
// style.css (npm run lint fails on a stylesheet nothing imports) — the same
// arrangement notes.css uses, since this module's CSS is not beside it.

export interface SkillViewDeps {
  readonly store: SkillsStore
}

type ViewState =
  { kind: 'loading' } | { kind: 'unavailable'; message: string } | { kind: 'ready'; skill: Skill }

interface SkillViewHeaderProps {
  name: string
  state: ViewState
  busy: boolean
  onToggle: (enabled: boolean) => void
}

function SkillViewHeader(props: SkillViewHeaderProps): JSX.Element {
  const readySkill = (): Skill | null => (props.state.kind === 'ready' ? props.state.skill : null)
  const unavailableMessage = (): string =>
    props.state.kind === 'unavailable' ? props.state.message : ''

  return (
    <div class="skill-view__header">
      <Show when={props.state.kind === 'loading'}>
        <StatusCard
          tone="neutral"
          title="Loading this skill"
          description={`Reading “${props.name}” from the discovered skills.`}
        />
      </Show>
      <Show when={unavailableMessage()}>
        <StatusCard
          tone="danger"
          title="Skills could not be read"
          description={unavailableMessage()}
        />
      </Show>
      <Show when={readySkill()}>
        {(skill) => (
          <Stack gap="loose">
            <div class="skill-view__title">
              <h1 class="skill-view__name">{skill().name}</h1>
              <Badge tone={provenanceTone(skill().provenance)}>{skill().provenance}</Badge>
            </div>
            <FactList
              facts={[{ name: 'Where it is', value: skill().path }]}
              ariaLabel="Where this skill lives"
            />
            <Checkbox
              variant="switch"
              label="Offer this skill to the assistant"
              checked={skill().enabled}
              disabled={props.busy}
              onChange={(enabled) => props.onToggle(enabled)}
            />
            {/* Not wired: a model call belongs to Task 10's check panel, and
                internal/profile/role.go refuses to spend one silently. This
                button exists so the header names the action before the panel
                that performs it exists — disabled, rather than wired to
                nothing, so pressing it cannot look like it did something. */}
            <Show when={skill().provenance !== 'builtin'}>
              <Button
                disabled
                title="Reading this skill with a model is not wired up yet"
                onClick={() => {}}
              >
                {skill().check ? 'Re-check' : 'Check this skill'}
              </Button>
            </Show>
          </Stack>
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
  }

  // ── SolidPaneContent ──────────────────────────────────────────────────

  renderContent(root: HTMLElement): () => void {
    return render(
      () => (
        <SkillViewHeader
          name={this.name}
          state={this.viewState()}
          busy={this.busy()}
          onToggle={(enabled) => void this.toggle(enabled)}
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

  // viewportChanged and focus are inherited as no-ops: the header lays
  // itself out in flow, and there is nothing to focus yet — the next task's
  // file list is where a deliberate focus target belongs.

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
}
