/**
 * Quick-connect picker — a modal dialog that lists SSH profiles (and later
 * command-palette entries) with a filter box.
 *
 * ## Provider interface
 *
 * Every source of items implements QuickConnectProvider. SSH profiles and
 * the local shell are the first implementations. A command palette adds a
 * second provider — the dialog merges every provider's items into one flat
 * list. The local shell is a provider so the interface is the same for all
 * sources.
 *
 * ## Lifecycle
 *
 * QuickConnectController owns the Solid root. mount() must be called before
 * show(). destroy() tears down the root.
 *
 * ## Keyboard
 *
 * - Typing filters the list.
 * - Up/Down move selection.
 * - Enter activates.
 * - Escape closes via the overlay stack (Dialog's built-in handler).
 * - Filter input is focused on open.
 */
import {
  For,
  Show,
  createSignal,
  createMemo,
  createEffect,
  type Component,
  type Setter,
} from 'solid-js'
import { render } from 'solid-js/web'
import type { ProfileClient } from './profiles'
import type { Tab } from './tabs'
import { Dialog } from './ui/dialog'
import { SearchField } from './ui/search-field'

// ═══════════════════════════════════════════════════════════════════════════
// Public interfaces
// ═══════════════════════════════════════════════════════════════════════════

export interface QuickConnectItem {
  /** Stable identity — used as key in lists. */
  readonly id: string
  /** Primary label, e.g. "user@host". */
  readonly label: string
  /** Additional context, e.g. profile display name. */
  readonly detail?: string
  /** Invoked when the item is activated (click or Enter). */
  readonly run: () => void
}

export interface QuickConnectProvider {
  readonly id: string
  readonly label: string
  /** Return the current list of items. Called on every open. */
  getItems(): QuickConnectItem[] | Promise<QuickConnectItem[]>
}

// ═══════════════════════════════════════════════════════════════════════════
// Local shell provider — always listed first
// ═══════════════════════════════════════════════════════════════════════════

export class LocalShellQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'local'
  readonly label = 'Local Shell'

  constructor(private newTab: () => Tab) {}

  getItems(): QuickConnectItem[] {
    return [
      {
        id: '__local__',
        label: 'Local shell',
        detail: 'Open a new local terminal tab',
        run: () => void this.newTab(),
      },
    ]
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// SSH provider — first consumer of the interface
// ═══════════════════════════════════════════════════════════════════════════

export class SSHQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'ssh'
  readonly label = 'SSH Connections'

  constructor(
    private profileClient: ProfileClient,
    private newSSHTab: (profileId: string, host: string, user?: string) => Tab,
  ) {}

  async getItems(): Promise<QuickConnectItem[]> {
    const profiles = await this.profileClient.listProfiles()
    return profiles.map((p) => {
      const user = p.options.user
      const host = p.options.host
      const label = user ? `${user}@${host}` : host
      return {
        id: p.id,
        label,
        detail: p.name,
        run: () => void this.newSSHTab(p.id, host, user),
      }
    })
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// QuickConnect dialog component
// ═══════════════════════════════════════════════════════════════════════════

interface QuickConnectDialogProps {
  open: boolean
  onClose: () => void
  providers: QuickConnectProvider[]
}

const QuickConnectDialog: Component<QuickConnectDialogProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  let gen = 0
  const [query, setQuery] = createSignal('')
  const [items, setItems] = createSignal<QuickConnectItem[]>([])
  const [selectedIndex, setSelectedIndex] = createSignal(0)

  /**
   * Load items from every provider once the dialog opens.
   *
   * Deliberately outside the tracked scope. An async createEffect stops
   * tracking at its first await, so half the reads in a body like this one are
   * reactive and half are not — with nothing in the code to say which. Reading
   * the props synchronously and handing the values to a plain async function
   * makes that boundary explicit instead of implicit.
   *
   * The generation counter guards a close/reopen race: a slow provider from a
   * previous open must not overwrite a later open's items.
   */
  const loadItems = async (
    providers: readonly QuickConnectProvider[],
    currentGen: number,
  ): Promise<void> => {
    const all: QuickConnectItem[] = []
    for (const provider of providers) {
      try {
        const providerItems = await provider.getItems()
        if (currentGen !== gen) return
        all.push(...providerItems)
      } catch {
        // Provider failure is not user-visible — skip silently.
      }
    }
    if (currentGen !== gen) return
    setItems(all)

    // Focus the search input. requestAnimationFrame ensures the dialog's
    // showModal animation has completed before we focus.
    requestAnimationFrame(() => {
      if (currentGen !== gen) return
      panelRef?.querySelector<HTMLElement>('.quick-connect__search input')?.focus()
    })
  }

  // Reload items when the dialog opens.
  createEffect(() => {
    gen++
    const currentGen = gen

    if (!props.open) return

    // Read the providers inside the tracked scope, so a change to them
    // re-runs this effect; everything after the handoff is untracked.
    const providers = props.providers

    setQuery('')
    setSelectedIndex(0)

    void loadItems(providers, currentGen)
  })

  // Filtered list — derived from query.
  const filteredItems = createMemo(() => {
    const q = query().toLowerCase().trim()
    if (!q) return items()
    return items().filter(
      (it) =>
        it.label.toLowerCase().includes(q) ||
        (it.detail !== undefined && it.detail.toLowerCase().includes(q)),
    )
  })

  // Clamp selected index when the filtered list changes.
  createEffect(() => {
    const len = filteredItems().length
    const cur = selectedIndex()
    if (cur >= len && len > 0) setSelectedIndex(len - 1)
    else if (len === 0) setSelectedIndex(0)
  })

  function activate(index: number) {
    const list = filteredItems()
    const item = list[index]
    if (!item) return
    // Activate the item, then close. The item's run() may open a tab.
    item.run()
    props.onClose()
  }

  function onKeyDown(e: KeyboardEvent) {
    const list = filteredItems()
    if (list.length === 0) return

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setSelectedIndex((prev: number) => (prev + 1) % list.length)
        break
      case 'ArrowUp':
        e.preventDefault()
        setSelectedIndex((prev: number) => (prev - 1 + list.length) % list.length)
        break
      case 'Enter':
        e.preventDefault()
        activate(selectedIndex())
        break
      // Escape is handled natively by the dialog's cancel event.
    }
  }

  return (
    <Dialog open={props.open} onClose={props.onClose}>
      <div class="quick-connect" ref={panelRef} onKeyDown={onKeyDown}>
        <div class="quick-connect__search">
          <SearchField
            value={query()}
            onInput={setQuery}
            placeholder="Type to filter…"
            ariaLabel="Quick connect filter"
          />
        </div>
        <div class="quick-connect__list" role="listbox" aria-label="Connection list">
          <For each={filteredItems()}>
            {(item, index) => (
              <div
                class="quick-connect__item"
                classList={{
                  'quick-connect__item--selected': selectedIndex() === index(),
                }}
                role="option"
                aria-selected={selectedIndex() === index()}
                onClick={() => activate(index())}
                onMouseEnter={() => setSelectedIndex(index())}
              >
                <span class="quick-connect__item-label">{item.label}</span>
                <Show when={item.detail !== undefined}>
                  <span class="quick-connect__item-detail">{item.detail}</span>
                </Show>
              </div>
            )}
          </For>
        </div>
        <Show when={filteredItems().length === 0}>
          <div class="quick-connect__empty">No matches</div>
        </Show>
      </div>
    </Dialog>
  )
}

// ═══════════════════════════════════════════════════════════════════════════
// Controller — owned by the composition root
// ═══════════════════════════════════════════════════════════════════════════

export class QuickConnectController {
  private dispose: (() => void) | null = null
  private _setOpen: Setter<boolean> | null = null
  private _mounted = false

  mount(container: HTMLElement, providers: QuickConnectProvider[]): void {
    if (this._mounted) return
    this._mounted = true

    const [open, setOpen] = createSignal(false)
    this._setOpen = setOpen

    this.dispose = render(
      () => (
        <QuickConnectDialog open={open()} onClose={() => setOpen(false)} providers={providers} />
      ),
      container,
    )
  }

  /** Open the picker. No-op before mount or after destroy. */
  show(): void {
    this._setOpen?.(true)
  }

  destroy(): void {
    this.dispose?.()
    this.dispose = null
    this._setOpen = null
    this._mounted = false
  }
}
