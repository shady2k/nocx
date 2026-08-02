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
  /** When true, this entry comes from a system source (e.g. ~/.ssh/config),
   *  not from a user-saved connection. Visually distinguished from saved
   *  connections. */
  readonly system?: boolean
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
// Actions provider — the things that are not a connection, always listed first
//
// One provider rather than two, because the group is what draws the separator:
// splitting "Local shell" and "New connection" into separate providers would
// put a line between them.
// ═══════════════════════════════════════════════════════════════════════════

export class ActionsQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'actions'
  readonly label = 'Actions'

  constructor(
    private newTab: () => Tab,
    private newConnection: () => void,
  ) {}

  getItems(): QuickConnectItem[] {
    return [
      {
        id: '__local__',
        label: 'Local shell',
        detail: 'Open a new local terminal tab',
        run: () => void this.newTab(),
      },
      {
        id: '__new_connection__',
        label: 'New connection',
        detail: 'Define an SSH connection in Settings',
        run: () => this.newConnection(),
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
    // A profile saved before its host was filled in is not a connection: opening
    // it hands the backend an empty address and the tab comes up on "Terminal
    // failed to start". It also rendered as a row with an empty primary label —
    // a stray indent rather than a line. The palette lists what can be
    // connected to; finishing such a profile is what the New-connection action
    // above is for.
    return profiles
      .filter((p) => p.options.host != null && p.options.host.trim() !== '')
      .map((p) => {
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
// SSH alias provider — live, read-only aliases from ~/.ssh/config
// ═══════════════════════════════════════════════════════════════════════════

export class SSHAliasQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'ssh-aliases'
  readonly label = 'SSH Aliases'

  constructor(
    private profileClient: ProfileClient,
    private newTabByHost: (host: string, user?: string, port?: number) => Tab,
  ) {}

  async getItems(): Promise<QuickConnectItem[]> {
    const response = await this.profileClient.listSSHAliases()

    // Degraded resolver: surface the condition rather than hiding it.
    if (response.unavailable != null) {
      return [
        {
          id: '__ssh_aliases_unavailable__',
          label: `SSH config: ${response.unavailable.reason}`,
          detail: response.unavailable.detail,
          system: true,
          run: () => {},
        },
      ]
    }

    if (response.aliases.length === 0) {
      return []
    }

    // Get saved profiles for deduplication: an alias already targeted by a
    // saved profile is suppressed (priority is ours).
    const profiles = await this.profileClient.listProfiles()
    const coveredAliases = new Set(
      profiles
        .filter((p) => p.options.host != null && p.options.host.trim() !== '')
        .map((p) => p.options.host),
    )

    return response.aliases
      .filter((a) => !coveredAliases.has(a.alias))
      .map((a) => {
        const label = a.user ? `${a.user}@${a.alias}` : a.alias
        return {
          id: `__ssh_alias:${a.alias}`,
          label,
          detail: a.hostName !== a.alias ? a.hostName : undefined,
          system: true,
          run: () => void this.newTabByHost(a.alias, a.user, a.port),
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

/**
 * An item plus the provider it came from. The provider is what the separator
 * below is drawn from: a rule that put a line after the actions would draw one
 * with nothing under it when no connection is defined, and this way the line
 * only exists where two populated groups meet — including while a filter is
 * emptying one of them.
 */
interface GroupedItem extends QuickConnectItem {
  readonly providerId: string
}

const QuickConnectDialog: Component<QuickConnectDialogProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  let gen = 0
  const [query, setQuery] = createSignal('')
  const [items, setItems] = createSignal<GroupedItem[]>([])
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
    const all: GroupedItem[] = []
    for (const provider of providers) {
      try {
        const providerItems = await provider.getItems()
        if (currentGen !== gen) return
        all.push(...providerItems.map((item) => ({ ...item, providerId: provider.id })))
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
    <Dialog open={props.open} onClose={props.onClose} size="lg">
      <div class="quick-connect" ref={panelRef} onKeyDown={onKeyDown}>
        <div class="quick-connect__search">
          <SearchField
            value={query()}
            onInput={setQuery}
            placeholder="Type to filter…"
            ariaLabel="Quick connect filter"
          />
        </div>
        {/* No listbox at all when nothing matches: the empty notice takes the
            list's place in the layout rather than sitting under a list box that
            stretches to the bottom of the panel, and an empty `role="listbox"`
            is not something to announce either. */}
        <Show
          when={filteredItems().length > 0}
          fallback={<div class="quick-connect__empty">No matches</div>}
        >
          <div class="quick-connect__list" role="listbox" aria-label="Connection list">
            <For each={filteredItems()}>
              {(item, index) => (
                <>
                  <Show
                    when={
                      index() > 0 && filteredItems()[index() - 1]?.providerId !== item.providerId
                    }
                  >
                    <div class="quick-connect__separator" role="presentation" />
                  </Show>
                  <div
                    class="quick-connect__item"
                    classList={{
                      'quick-connect__item--selected': selectedIndex() === index(),
                      'quick-connect__item--system': item.system === true,
                    }}
                    role="option"
                    aria-selected={selectedIndex() === index()}
                    onClick={() => activate(index())}
                    onMouseEnter={() => setSelectedIndex(index())}
                  >
                    <span class="quick-connect__item-label">{item.label}</span>
                    <Show when={item.system === true}>
                      <span class="quick-connect__item-badge">alias</span>
                    </Show>
                    <Show when={item.detail !== undefined}>
                      <span class="quick-connect__item-detail">{item.detail}</span>
                    </Show>
                  </div>
                </>
              )}
            </For>
          </div>
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
