/**
 * Quick-connect picker — ONE surface with two presentations (nocx-4t37).
 *
 * The model is Raycast, not VS Code: one field, MIXED results, every row
 * carrying its TYPE on the right (Command / Host / Setting) and its context
 * as a subtitle. Nobody has to remember a prefix to reach anything.
 *
 * Two entry points, two presentations of the same dialog:
 *
 * - The tab-strip caret opens the PLAIN SERVER LIST (`variant: 'hosts'`):
 *   saved profiles, live aliases, the ad-hoc connect fallback — no commands,
 *   no type badges. One job, and its speed comes from that.
 * - Ctrl/Cmd+Shift+P opens the PALETTE (`variant: 'palette'`): commands and
 *   hosts mixed, each row typed on the right.
 *
 * ## Drill-in
 *
 * A command that needs a target DRILLS IN inside the same surface:
 * activating it replaces the list with the first step's choices, the chosen
 * steps accumulate as breadcrumbs in the field, and Backspace or Escape
 * walks back one step at a time. A command declares its `steps` and gets the
 * picker for free — the second command that needs a target must not
 * hand-roll its own second step.
 *
 * ## Degraded sources are typed facts
 *
 * An unavailable `ssh -G` resolver renders as a row naming the condition,
 * never as an empty list; a drill step that cannot produce choices says why.
 * A degraded source and an empty source are different facts.
 *
 * ## Provider interface
 *
 * Every source of items implements QuickConnectProvider. SSH profiles, live
 * aliases and the local shell are the first implementations; the local shell
 * is a provider so the interface is the same for all sources. Each item
 * declares its own `kind` — the palette's type vocabulary.
 *
 * ## Lifecycle
 *
 * QuickConnectController owns the Solid root. mount() must be called before
 * show(). destroy() tears down the root. show() opens the server list
 * (caret); showPalette() opens the palette (chord).
 *
 * ## Keyboard
 *
 * - Typing filters the list.
 * - Up/Down move selection.
 * - Enter activates.
 * - Escape closes via the overlay stack (Dialog's built-in handler); while a
 *   drill is in progress Escape walks back one step instead.
 * - Backspace on an empty filter walks a drill back one step.
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
import { parseQuickConnect, type ProfileClient } from './profiles'
import type { Pane } from './panes'
import { Dialog } from './ui/dialog'
import { SearchField } from './ui/search-field'
import { VAULT_OFFER_SETUP, VAULT_OFFER_UNSEAL, addSecretLabel } from './ui/secret-picker'
import { aliasRows, profileRows } from './quick-connect-assembly'

// ═══════════════════════════════════════════════════════════════════════════
// Public interfaces
// ═══════════════════════════════════════════════════════════════════════════

/** The palette's type vocabulary — rendered as the badge on the right of
 *  each row in palette mode (nocx-4t37). */
export type QuickConnectItemKind = 'command' | 'host' | 'setting' | 'secret' | 'snippet'

const KIND_LABELS: Record<QuickConnectItemKind, string> = {
  command: 'Command',
  host: 'Host',
  setting: 'Setting',
  secret: 'Secret',
  snippet: 'Snippet',
}

export interface QuickConnectItem {
  /** Stable identity — used as key in lists. */
  readonly id: string
  /** Primary label, e.g. "user@host" or the command name. */
  readonly label: string
  /** Additional context — the row's subtitle (profile name, what the
   *  command does). */
  readonly detail?: string
  /** When true, this entry comes from a system source (e.g. ~/.ssh/config),
   *  not from a user-saved connection. Visually distinguished from saved
   *  connections. */
  readonly system?: boolean
  /** Badge text for system entries; defaults to "alias". */
  readonly badge?: string
  /** The row's type, shown as a badge on the right in palette mode. */
  readonly kind: QuickConnectItemKind
  /** When true, this row may appear in the caret's plain server list even
   *  though it is not a host. Opt-in per item, never per kind: admitting
   *  every command would put "Forward a port" in front of the caret, and
   *  forwarding a port is a different job from connecting to a machine.
   *  Only "New connection" sets it — creating a connection is still about
   *  connecting, one you have not saved yet (nocx-d4us). */
  readonly allowInHosts?: boolean
  /** When present, activating this command drills into its steps inside the
   *  same surface instead of running (nocx-4t37). */
  readonly drill?: DrillCommand
  readonly disabled?: boolean
  /** Invoked when the item is activated (click or Enter). */
  readonly run: () => void
}

export interface QuickConnectProvider {
  readonly id: string
  readonly label: string
  /**
   * The kinds this provider can produce — declared, so a variant that shows
   * none of them is never CONSULTED (nocx-8rtr, owner review).
   *
   * Applicability has to be part of the contract, exactly as it is for the
   * completion providers: getItems is an RPC for most of these, and the
   * vault's is one that can raise the unlock prompt. Without this gate,
   * opening the snippet list — or the plain server list — asked the vault
   * for its inventory, which is a question nobody at that surface asked.
   */
  readonly kinds: readonly QuickConnectItemKind[]
  /** Return the current list of items. Called on every open, and only when
   *  the open variant shows one of `kinds`. */
  getItems(): QuickConnectItem[] | Promise<QuickConnectItem[]>
  /**
   * Optional query-dependent items. Consulted only when nothing from getItems
   * matched the filter — the free-form "connect to the typed host" fallback.
   * Runs on every keystroke, so it must stay synchronous.
   */
  getQueryItems?(query: string): QuickConnectItem[]
  /**
   * Optional rows that always sit LAST, whether or not anything matched — the
   * "the one you want is not here" offer, which is precisely the answer to an
   * empty list and must therefore survive one. Distinct from getQueryItems,
   * which is a fallback: a fallback that only appears when nothing matched
   * cannot offer to create the name you typed while a near-match is still on
   * screen. Runs on every keystroke, so it must stay synchronous.
   */
  getTrailingItems?(query: string): QuickConnectItem[]
}

// ── Drill-in (nocx-4t37) ────────────────────────────────────────────────
// A command that needs a target declares its steps; the surface walks them.
// Each step's choices are fetched with the selections so far, the trail
// renders as breadcrumbs, and the completed selection goes to `run`.

/** One choice made during a drill-in: which step and which item. */
export interface DrillSelection {
  readonly stepName: string
  readonly item: DrillChoice
}

/** One selectable row in a drill step. */
export interface DrillChoice {
  readonly id: string
  readonly label: string
  readonly detail?: string
  readonly system?: boolean
  readonly badge?: string
  /** Machine payload the command's `run` reads (e.g. the destination of a
   *  forward). The surface never displays it. */
  readonly value?: string
}

/** One drill step: a named picker over `fetch`'s choices. A step that needs
 *  a value nobody can offer choices for — a snippet's `{{ask:…}}` field —
 *  is NOT this: it belongs in a form that asks for every field at once, and
 *  the palette closes to show it. A step that looked like a filter and was
 *  in fact an input read as a filter, which is exactly how it was used
 *  (owner review). */
export interface DrillStepSpec {
  /** Breadcrumb name for this step, e.g. "server" or "port". */
  readonly name: string
  /** Fetch this step's choices given the selections so far. A step that
   *  cannot produce choices returns a typed condition row — never an empty
   *  list. */
  fetch(selections: readonly DrillSelection[]): Promise<DrillChoice[]>
}

/** A command that needs targets. Activating it enters the drill: the list
 *  becomes each step's choices in turn, the trail accumulates in the field,
 *  and Backspace/Escape walks back out one step at a time. */
export interface DrillCommand {
  readonly id: string
  readonly label: string
  readonly detail: string
  /** Steps after the command itself, walked in order. */
  readonly steps: readonly DrillStepSpec[]
  /** Runs with the completed selection. */
  run(selections: readonly DrillSelection[]): void
}

// ═══════════════════════════════════════════════════════════════════════════
// Commands provider — the things that are not a connection
//
// One provider rather than two, because the group is what draws the separator:
// splitting the commands into separate providers would put a line between
// them. Every item here is a Command — it lives in the palette and never in
// the caret's plain server list (nocx-4t37).
// ═══════════════════════════════════════════════════════════════════════════

/** An item for a target-needing command: activating it drills instead of
 *  running. */
function drillItem(cmd: DrillCommand): QuickConnectItem {
  return {
    id: cmd.id,
    kind: 'command',
    label: cmd.label,
    detail: cmd.detail,
    drill: cmd,
    run: () => {},
  }
}

export class ActionsQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'actions'
  readonly label = 'Commands'
  readonly kinds = ['command', 'host'] as const

  constructor(
    private newPane: () => Pane,
    private newConnection: () => void,
    /** Optional target-needing command ("Forward a port"): activating it
     *  drills into its steps inside the palette. */
    private drillCommand?: DrillCommand,
  ) {}

  getItems(): QuickConnectItem[] {
    const items: QuickConnectItem[] = [
      {
        id: '__local__',
        kind: 'command',
        label: 'Local shell',
        detail: 'Open a new local terminal tab',
        run: () => void this.newPane(),
      },
      {
        // The one command row the caret admits (nocx-d4us): creating a
        // connection is still connecting to a machine — one not saved yet —
        // the same need the ad-hoc "Connect to <host>" serves from the other
        // end. Admitted by this flag, never by kind, so "Forward a port" and
        // any future command stay out of the caret.
        allowInHosts: true,
        id: '__new_connection__',
        kind: 'command',
        label: 'New connection',
        detail: 'Define an SSH connection in Settings',
        run: () => this.newConnection(),
      },
    ]
    // The target-needing command comes LAST: the first row is what Enter
    // activates on open, and that stays the muscle-memory "Local shell".
    // The drill is one typed word away ("forward").
    if (this.drillCommand) {
      items.push(drillItem(this.drillCommand))
    }
    return items
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// SSH provider — first consumer of the interface
// ═══════════════════════════════════════════════════════════════════════════

export class SSHQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'ssh'
  readonly label = 'SSH Connections'
  readonly kinds = ['host'] as const

  constructor(
    private profileClient: ProfileClient,
    private newSSHPane: (profileId: string, host: string, user?: string) => Pane,
  ) {}

  async getItems(): Promise<QuickConnectItem[]> {
    // The saved-profile half of the shared host assembly
    // (quick-connect-assembly.ts): the picker and completion list the same
    // rows — only the run callback is this provider's. A profile saved
    // before its host was filled in is filtered out there, not here.
    const profiles = profileRows(await this.profileClient.listProfiles())
    return profiles.map((p) => ({
      id: p.id,
      kind: 'host' as const,
      label: p.label,
      detail: p.detail,
      run: () => void this.newSSHPane(p.id, p.host, p.user),
    }))
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// SSH alias provider — live, read-only aliases from ~/.ssh/config
// ═══════════════════════════════════════════════════════════════════════════

export class SSHAliasQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'ssh-aliases'
  readonly label = 'SSH Aliases'
  readonly kinds = ['host'] as const

  constructor(
    private profileClient: ProfileClient,
    private newPaneByHost: (host: string, user?: string, port?: number) => Pane,
  ) {}

  async getItems(): Promise<QuickConnectItem[]> {
    const response = await this.profileClient.listSSHAliases()

    // Degraded resolver: surface the condition rather than hiding it. The
    // row is rendered from the typed condition (quick-connect-assembly.ts
    // carries it as data, never as a label) — an empty list would read as
    // "you have no hosts".
    if (response.unavailable != null) {
      return [
        {
          id: '__ssh_aliases_unavailable__',
          kind: 'host' as const,
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

    // The live half of the shared host assembly: aliases, deduped against
    // the saved profiles (an alias covered by a profile is suppressed — the
    // profile is ours and wins). Only the run callback is this provider's.
    const { aliases } = aliasRows({
      profiles: await this.profileClient.listProfiles(),
      aliases: response.aliases,
      unavailable: null,
    })
    return aliases.map((a) => ({
      id: a.id,
      kind: 'host' as const,
      label: a.label,
      detail: a.detail,
      system: true,
      run: () => void this.newPaneByHost(a.alias, a.user, a.port),
    }))
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Secrets provider — insert a saved secret into the pane in front (nocx-fk32)
//
// Names only. vault.inventory is the list a person can read and the only
// identifier a reference can carry (ADR-0016); no material passes through
// this provider, and the row's run hands the NAME to the insert seam, which
// decides what actually happens by asking who owns input.
//
// Nothing here infers anything about what is on screen. nocx cannot know
// that a program is reading stdin (ADR-0004) and does not try: this is a
// surface the user reaches when they want it, which is why it is a picker
// beside quick connect rather than an offer that appears at a guessed
// moment.
// ═══════════════════════════════════════════════════════════════════════════

export interface SecretsProviderDeps {
  /** The vault's lifecycle state. Read BEFORE the inventory, because
   *  "there is no vault" and "the vault holds nothing" are different facts
   *  and only one of them is an empty list. */
  status(): Promise<{ state: 'uninitialized' | 'sealed' | 'unsealed' }>
  inventory(): Promise<{ entries: { id: string; name: string; kind: string }[] }>
  /** Insert the named secret where the user is typing. The provider never
   *  sees the value — resolution and the pty write belong to the pane. */
  insert(name: string): void
  /** "Add a secret…" was activated: the host opens the vault's own create
   *  dialog, which owns the surface from there. The SAME seam the prompt's
   *  '@' picker creates through (secret-picker.ts requestCreate) — a second
   *  way to make a secret would be a second set of rules about names, kinds
   *  and collisions. */
  create(name: string): void
  /** The inventory could not be read: raise the unlock dialog. */
  requestUnseal(): void
  /** No vault yet: raise the vault's own setup dialog. */
  requestSetup(): void
}

export class SecretsQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'secrets'
  readonly label = 'Secrets'
  readonly kinds = ['secret'] as const

  /** Whether the last getItems saw a vault that can hold a new secret. The
   *  create row is offered from getTrailingItems, which is synchronous and
   *  runs on every keystroke, so the asynchronous answer is carried here
   *  rather than re-asked. False until the inventory has actually been
   *  read: offering to add a secret to a vault that does not exist, or to
   *  one still locked, is an offer that cannot be kept. */
  private canHoldSecrets = false

  constructor(private deps: SecretsProviderDeps) {}

  async getItems(): Promise<QuickConnectItem[]> {
    this.canHoldSecrets = false
    // A vault that was never set up has no inventory to fail to read — say
    // that, and offer the one thing that changes it.
    const state = await this.deps.status().then(
      (s) => s.state,
      () => null,
    )
    if (state === 'uninitialized') {
      return [
        {
          id: SETUP_VAULT_ROW_ID,
          kind: 'secret',
          label: VAULT_OFFER_SETUP,
          run: () => this.deps.requestSetup(),
        },
      ]
    }
    // Sealed is deliberately NOT an offer row here: the dispatcher's global
    // seam raises the unlock prompt for any RPC that lands on a sealed
    // vault and retries the call, so the ordinary path through a locked
    // vault ends with the list. The catch below is where that path did not
    // end there — a dismissed prompt, or any other failure — and an empty
    // list would report it as "you have no secrets".
    try {
      const inv = await this.deps.inventory()
      this.canHoldSecrets = true
      return inv.entries.map((e) => ({
        id: e.id,
        kind: 'secret' as const,
        label: e.name,
        detail: SECRET_KIND_DETAIL[e.kind] ?? e.kind,
        run: () => this.deps.insert(e.name),
      }))
    } catch {
      return [
        {
          id: UNSEAL_VAULT_ROW_ID,
          kind: 'secret',
          label: VAULT_OFFER_UNSEAL,
          run: () => this.deps.requestUnseal(),
        },
      ]
    }
  }

  /** The offer that outlives an empty list: the secret the vault does not
   *  hold yet is the one the person came here to type. It carries the typed
   *  filter as the name — the same judgement, and the same words, as the
   *  prompt picker's create row. Absent while the vault cannot hold one;
   *  the offer row above is the whole answer in that state. */
  getTrailingItems(query: string): QuickConnectItem[] {
    if (!this.canHoldSecrets) return []
    const typed = query.trim()
    return [
      {
        id: CREATE_SECRET_ROW_ID,
        kind: 'secret',
        label: addSecretLabel(typed),
        run: () => this.deps.create(typed),
      },
    ]
  }
}

/** Rows that are not vault entries, so they are addressed by reserved ids
 *  rather than by an inventory handle. */
const CREATE_SECRET_ROW_ID = '__create_secret__'
const SETUP_VAULT_ROW_ID = '__setup_vault__'
const UNSEAL_VAULT_ROW_ID = '__unseal_vault__'

/** What each vault kind is, in the words a person picking one would use.
 *  The vocabulary is the registry's closed set (contracts/vault.inventory). */
const SECRET_KIND_DETAIL: Record<string, string> = {
  password: 'Password',
  'key-passphrase': 'Key passphrase',
  'private-key': 'Private key',
  'public-key': 'Public key',
  'otp-seed': 'One-time-password seed',
}

// ═══════════════════════════════════════════════════════════════════════════
// Free-form connect provider — "I know this host and you do not"
//
// Contributes nothing to the static list. It answers the dialog's
// query-dependent fallback: when the typed query parses as a host, offer a
// Connect entry that opens the same newPaneByHost path the alias provider
// uses — nothing is persisted. The dialog only consults it when no saved
// profile or alias matched, so a mistyped alias can never silently become an
// ad-hoc connection to a host that merely shares its name.
// ═══════════════════════════════════════════════════════════════════════════

export class AdHocQuickConnectProvider implements QuickConnectProvider {
  readonly id = 'ad-hoc'
  readonly label = 'Quick Connect'
  readonly kinds = ['host'] as const

  constructor(private newPaneByHost: (host: string, user?: string, port?: number) => Pane) {}

  getItems(): QuickConnectItem[] {
    return []
  }

  getQueryItems(query: string): QuickConnectItem[] {
    const trimmed = query.trim()
    if (!trimmed) return []

    const parsed = parseQuickConnect(trimmed)
    const host = (parsed.options.host ?? '').trim()
    // Same judgement as connections.tsx's quick-connect handler: input that
    // contained '@' or ':' yet parsed to an empty host was malformed and must
    // not connect to whatever fell out of the parser — the dialog explains why.
    if (!host) return []

    const user = parsed.options.user
    const port = parsed.options.port
    return [
      {
        id: '__ad_hoc_connect__',
        kind: 'host',
        label: `Connect to ${user ? `${user}@` : ''}${host}`,
        detail: port != null && port !== 22 ? `port ${port}` : undefined,
        system: true,
        badge: 'ad-hoc',
        run: () => void this.newPaneByHost(host, user, port),
      },
    ]
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// QuickConnect dialog component
// ═══════════════════════════════════════════════════════════════════════════

/** Which presentation of the one surface is showing. */
export type PaletteVariant = 'hosts' | 'palette' | 'secrets' | 'snippets'

/** Which kinds each variant shows — ONE table, read by both the row filter
 *  and the provider gate below, so a variant cannot admit a kind whose
 *  provider it never consulted (or consult one whose rows it then hides).
 *  'hosts' also admits a per-item opt-in ("New connection"), which is why
 *  its row filter is a shade wider than this table. */
const VARIANT_KINDS: Record<PaletteVariant, readonly QuickConnectItemKind[]> = {
  hosts: ['host', 'command'],
  palette: ['command', 'host', 'setting'],
  secrets: ['secret'],
  snippets: ['snippet'],
}

interface QuickConnectDialogProps {
  open: boolean
  onClose: () => void
  providers: QuickConnectProvider[]
  /** 'hosts' (the tab-strip caret): the plain server list — no commands, no
   *  type badges. 'palette' (the chord): commands and hosts mixed, typed.
   *  'secrets' (the tab-strip key icon, nocx-fk32): vault names only,
   *  inserted into whatever owns input in the pane in front. Secrets are
   *  deliberately absent from the palette — a list that mixes 'open a
   *  connection' with 'type a password into what is in front of you' would
   *  make one Enter mean two very different things. 'snippets' (⌥⌘P and the
   *  strip's snippets action) is the same shape for the same reason: its
   *  Enter fires a saved phrase into the pane in front. */
  variant: PaletteVariant
  /** A sentence to show above the list — the refusal a fire came back with
   *  (design §11): it renders here and STAYS, because the surface it is
   *  about is still on screen and a toast would take the explanation away
   *  from it. */
  notice?: string
  /** The person moved on — picked a row, entered a drill, walked back. The
   *  notice is about the choice they just made, so it dies with it: a
   *  refusal still on screen inside the NEXT row's questions is a sentence
   *  about something that is no longer in front of anybody. */
  onNoticeDone?: () => void
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

/** The in-flight drill: the command and the steps chosen so far. */
interface DrillState {
  readonly command: DrillCommand
  readonly selections: DrillSelection[]
}

/** Group id for drill rows — one group, so no separator can appear. */
const DRILL_GROUP_ID = '__drill__'

/** The label/detail substring match both modes filter with. */
function matchesText(label: string, detail: string | undefined, q: string): boolean {
  return (
    label.toLowerCase().includes(q) || (detail !== undefined && detail.toLowerCase().includes(q))
  )
}

const QuickConnectDialog: Component<QuickConnectDialogProps> = (props) => {
  let panelRef: HTMLDivElement | undefined
  let gen = 0
  const [query, setQuery] = createSignal('')
  const [items, setItems] = createSignal<GroupedItem[]>([])
  const [selectedIndex, setSelectedIndex] = createSignal(0)
  const [drill, setDrill] = createSignal<DrillState | null>(null)
  /** Choices per drill depth, so walking back restores a step without
   *  re-fetching it (the port step would otherwise re-sample). */
  const [stepCache, setStepCache] = createSignal<Record<number, DrillChoice[]>>({})

  /** The depth of the drill (0 = first step) and its current step. */
  const stepIndex = () => drill()?.selections.length ?? 0
  const currentStep = () => drill()?.command.steps[stepIndex()] ?? null

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
    const shown = VARIANT_KINDS[props.variant]
    for (const provider of providers) {
      // Not applicable to this variant: not consulted. getItems is a wire
      // call for most providers and a vault read for one of them.
      if (!provider.kinds.some((k) => shown.includes(k))) continue
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

  /** Fetch one drill step's choices and cache them at its depth. A thrown
   *  fetch becomes a typed condition row — never a silent empty list. */
  const loadStep = async (state: DrillState, currentGen: number): Promise<void> => {
    const step = state.command.steps[state.selections.length]
    if (!step) return
    let choices: DrillChoice[]
    try {
      choices = await step.fetch(state.selections)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      choices = [
        {
          id: '__step_error__',
          label: `Could not load ${step.name}s`,
          detail: msg,
          system: true,
        },
      ]
    }
    if (currentGen !== gen) return
    setStepCache((prev) => ({ ...prev, [state.selections.length]: choices }))
  }

  // Reload items and reset every transient state when the dialog opens.
  createEffect(() => {
    gen++
    const currentGen = gen

    if (!props.open) return

    // Read the providers inside the tracked scope, so a change to them
    // re-runs this effect; everything after the handoff is untracked.
    const providers = props.providers

    setQuery('')
    setSelectedIndex(0)
    setDrill(null)
    setStepCache({})

    void loadItems(providers, currentGen)
  })

  /** Enter a command's drill: step 0 replaces the list. */
  /** Put the keyboard back in the filter field. The field is the ONLY place
   *  this surface takes input, so anything that keeps the dialog open —
   *  entering a drill step, walking back — has to leave the caret there.
   *  A typed step made this visible: its row says "type to replace it" and
   *  a person who reached it by clicking had nowhere for the typing to go
   *  (owner review). rAF for the same reason the open path uses one: the
   *  list is re-rendering. */
  const focusSearch = (): void => {
    requestAnimationFrame(() => {
      panelRef?.querySelector<HTMLElement>('.quick-connect__search input')?.focus()
    })
  }

  const enterDrill = (command: DrillCommand): void => {
    const state: DrillState = { command, selections: [] }
    setDrill(state)
    setQuery('')
    setSelectedIndex(0)
    void loadStep(state, gen)
    focusSearch()
  }

  /** Choose the current step's item: advance a step, or run the command
   *  when the selection is complete. */
  const chooseStep = (choice: DrillChoice): void => {
    const state = drill()
    if (!state) return
    const step = state.command.steps[state.selections.length]
    if (!step) return
    const selections = [...state.selections, { stepName: step.name, item: choice }]
    if (selections.length >= state.command.steps.length) {
      state.command.run(selections)
      setDrill(null)
      props.onClose()
      return
    }
    const next: DrillState = { command: state.command, selections }
    setDrill(next)
    setQuery('')
    setSelectedIndex(0)
    void loadStep(next, gen)
    focusSearch()
  }

  /** Walk the drill back one step; at its root, back to the palette. */
  const walkBack = (): void => {
    props.onNoticeDone?.()
    const state = drill()
    if (!state) return
    setQuery('')
    setSelectedIndex(0)
    focusSearch()
    if (state.selections.length === 0) {
      setDrill(null)
      return
    }
    setDrill({ command: state.command, selections: state.selections.slice(0, -1) })
  }

  /** The current step's choices, filtered by the query. */
  const drillFiltered = createMemo<DrillChoice[]>(() => {
    const q = query().trim().toLowerCase()
    return (
      stepCache()[stepIndex()]?.filter((c) => q === '' || matchesText(c.label, c.detail, q)) ?? []
    )
  })

  // Filtered list — derived from query, variant and drill state.
  const filteredItems = createMemo<GroupedItem[]>(() => {
    if (drill()) {
      // Drill step: this step's choices as rows of one group.
      return drillFiltered().map((c) => ({
        ...c,
        providerId: DRILL_GROUP_ID,
        kind: 'host' as const,
        run: () => {},
      }))
    }

    const q = query().trim().toLowerCase()
    // Each variant admits exactly one kind set, and 'secret' is in exactly
    // one of them: the palette must not offer a row whose Enter types into
    // the pane in front. The caret's one exception is per-item, not per-kind:
    // "New connection" (allowInHosts) is about connecting to a machine — one
    // not saved yet — so it belongs to the caret's single job, while "Forward
    // a port" and every other command stay out (nocx-d4us).
    const admits = (item: QuickConnectItem): boolean => {
      // The caret's one exception is per ITEM, not per kind: "New
      // connection" is about connecting to a machine, one not saved yet.
      if (props.variant === 'hosts') return item.kind === 'host' || item.allowInHosts === true
      return VARIANT_KINDS[props.variant].includes(item.kind)
    }
    // Rows that always sit last — the vault's "Add a secret…". Admitted by
    // the same kind rule as everything else, so the offer to create a secret
    // cannot appear in the palette or the server list.
    const trailing: GroupedItem[] = []
    for (const provider of props.providers) {
      const providerItems = (provider.getTrailingItems?.(query()) ?? []).filter((it) => admits(it))
      trailing.push(...providerItems.map((item) => ({ ...item, providerId: provider.id })))
    }

    const matched = items().filter(
      (it) => admits(it) && (q === '' || matchesText(it.label, it.detail, q)),
    )
    if (matched.length > 0) {
      if (props.variant === 'hosts') {
        // Hosts keep their order — Enter on an empty query still activates
        // the first saved host — and the admitted command row ("New
        // connection") follows them instead of jumping ahead of them (the
        // actions provider is first in the app's provider list).
        const hostRows = matched.filter((it) => it.kind === 'host')
        const caretRows = matched.filter((it) => it.kind !== 'host')
        return [...hostRows, ...caretRows, ...trailing]
      }
      return [...matched, ...trailing]
    }

    // Nothing static matched — consult the query-dependent providers (the
    // ad-hoc "Connect to <host>" fallback). Only reached when every real
    // match missed, so the free-form entry can never outrank a saved profile
    // or an alias; a mistyped alias cannot silently become an ad-hoc
    // connection to a host that merely shares its name.
    const queryItems: GroupedItem[] = []
    for (const provider of props.providers) {
      const providerItems = provider.getQueryItems?.(query()) ?? []
      queryItems.push(...providerItems.map((item) => ({ ...item, providerId: provider.id })))
    }
    return [...queryItems, ...trailing]
  })

  // Parse-failure notice for the empty state. Reuses the connections.tsx
  // quick-connect judgement: input that contained '@' or ':' yet parsed to an
  // empty host was malformed — say why rather than showing a bare "No
  // matches" (or worse, connecting to whatever fell out of the parser).
  // Inside a drill there is no host parsing to fail.
  const parseFailureMessage = createMemo(() => {
    if (drill()) return null
    const q = query().trim()
    if (!q || filteredItems().length > 0) return null
    const hadAtOrColon = q.includes('@') || q.includes(':')
    if (!hadAtOrColon) return null
    const parsed = parseQuickConnect(q)
    if (parsed.options.host != null && parsed.options.host.trim() !== '') return null
    return `Could not parse "${q}": try "user@host:port" or "ssh://user@host:port"`
  })

  // Clamp selected index when the filtered list changes.
  createEffect(() => {
    const len = filteredItems().length
    const cur = selectedIndex()
    if (cur >= len && len > 0) setSelectedIndex(len - 1)
    else if (len === 0) setSelectedIndex(0)
  })

  function activate(index: number) {
    if (drill()) {
      const choice = drillFiltered()[index]
      if (!choice) return
      props.onNoticeDone?.()
      chooseStep(choice)
      return
    }
    const item = filteredItems()[index]
    if (!item || item.disabled) return
    props.onNoticeDone?.()
    if (item.drill) {
      // A command that needs a target drills in — no second dialog, no
      // dead end (nocx-4t37).
      enterDrill(item.drill)
      return
    }
    // Activate the item, then close. The item's run() may open a tab.
    item.run()
    props.onClose()
  }

  /** Escape while a drill is in progress walks back one step instead of
   *  closing. The Dialog consults this in its close path (the overlay
   *  stack's Escape handler and the native cancel), so the dialog stays
   *  open until the drill is walked back to the palette root. */
  const onDialogEscape = (): boolean => {
    if (!drill()) return false
    walkBack()
    return true
  }

  function onKeyDown(e: KeyboardEvent) {
    // Backspace on an empty filter walks a drill back one step. Escape is
    // handled through the Dialog's onEscape veto, not here — the overlay
    // stack's document-capture handler owns Escape and would close the
    // dialog before this bubble-phase handler could walk back.
    if (e.key === 'Backspace' && query() === '' && drill()) {
      e.preventDefault()
      walkBack()
      return
    }
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

  const emptyMessage = createMemo(() => {
    if (parseFailureMessage() != null) return parseFailureMessage()
    if (drill()) return `No matching ${currentStep()?.name}s`
    return 'No matches'
  })

  return (
    <Dialog open={props.open} onClose={props.onClose} onEscape={onDialogEscape} size="lg">
      <div class="quick-connect" data-variant={props.variant} ref={panelRef} onKeyDown={onKeyDown}>
        {/* Drill-in breadcrumbs: the command's path, walked back one step at
            a time with Backspace or Escape. */}
        <Show when={drill()}>
          <div class="quick-connect__drill" role="presentation" aria-label="Drill-in path">
            <span class="quick-connect__drill-step">{drill()!.command.label}</span>
            <For each={drill()!.selections}>
              {(sel) => (
                <>
                  <span class="quick-connect__drill-sep" aria-hidden="true">
                    ›
                  </span>
                  <span class="quick-connect__drill-step">{sel.item.label}</span>
                </>
              )}
            </For>
            <span class="quick-connect__drill-sep" aria-hidden="true">
              ›
            </span>
            <span class="quick-connect__drill-step quick-connect__drill-step--current">
              {currentStep()?.name}
            </span>
          </div>
        </Show>
        <div class="quick-connect__search">
          <SearchField
            value={query()}
            onInput={setQuery}
            placeholder={
              drill()
                ? `Filter ${currentStep()?.name}s…`
                : props.variant === 'hosts'
                  ? 'Type to filter…'
                  : props.variant === 'secrets'
                    ? 'Type to filter secrets…'
                    : props.variant === 'snippets'
                      ? 'Type to filter snippets…'
                      : 'Search commands and hosts…'
            }
            ariaLabel={
              drill()
                ? `Filter ${currentStep()?.name}s`
                : props.variant === 'hosts'
                  ? 'Quick connect filter'
                  : props.variant === 'secrets'
                    ? 'Secret filter'
                    : props.variant === 'snippets'
                      ? 'Snippet filter'
                      : 'Command palette filter'
            }
          />
        </div>
        {/* What a refused fire came back with, kept on the surface it is
            about (design §11): the list stays underneath, so the person can
            pick something else without reopening anything. */}
        <Show when={props.notice !== undefined && props.notice !== ''}>
          <div class="quick-connect__notice" role="alert">
            {props.notice}
          </div>
        </Show>
        {/* No listbox at all when nothing matches: the empty notice takes the
            list's place in the layout rather than sitting under a list box that
            stretches to the bottom of the panel, and an empty `role="listbox"`
            is not something to announce either. */}
        <Show
          when={filteredItems().length > 0}
          fallback={<div class="quick-connect__empty">{emptyMessage()}</div>}
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
                      'quick-connect__item--disabled': item.disabled === true,
                    }}
                    role="option"
                    aria-selected={selectedIndex() === index()}
                    aria-disabled={item.disabled === true}
                    // The row is not a focus target: the field is. Without
                    // this the mouse silently moves the caret out of the
                    // one place this surface takes input, and the next
                    // keystroke goes nowhere.
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => activate(index())}
                    onMouseEnter={() => setSelectedIndex(index())}
                  >
                    <span class="quick-connect__item-label">{item.label}</span>
                    <Show when={item.system === true}>
                      <span class="quick-connect__item-badge">{item.badge ?? 'alias'}</span>
                    </Show>
                    <Show when={item.detail !== undefined}>
                      <span class="quick-connect__item-detail">{item.detail}</span>
                    </Show>
                    <Show when={props.variant === 'palette' && drill() === null}>
                      <span class="quick-connect__item-kind">{KIND_LABELS[item.kind]}</span>
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
  private _setVariant: Setter<PaletteVariant> | null = null
  private _setNotice: Setter<string | undefined> | null = null
  private _mounted = false

  mount(container: HTMLElement, providers: QuickConnectProvider[]): void {
    if (this._mounted) return
    this._mounted = true

    const [open, setOpen] = createSignal(false)
    const [variant, setVariant] = createSignal<PaletteVariant>('hosts')
    const [notice, setNotice] = createSignal<string | undefined>(undefined)
    this._setOpen = setOpen
    this._setVariant = setVariant
    this._setNotice = setNotice

    this.dispose = render(
      () => (
        <QuickConnectDialog
          open={open()}
          onClose={() => setOpen(false)}
          providers={providers}
          variant={variant()}
          notice={notice()}
          onNoticeDone={() => setNotice(undefined)}
        />
      ),
      container,
    )
  }

  /** Open the plain server list — the tab-strip caret's fast path: hosts
   *  only, no commands, no type badges (nocx-4t37). */
  show(): void {
    this._setNotice?.(undefined)
    this._setVariant?.('hosts')
    this._setOpen?.(true)
  }

  /** Open the palette — Ctrl/Cmd+Shift+P: commands and hosts mixed, each
   *  row typed, target-needing commands drilling in (nocx-4t37). */
  showPalette(): void {
    this._setNotice?.(undefined)
    this._setVariant?.('palette')
    this._setOpen?.(true)
  }

  /** Open the snippet list — ⌥⌘P and the strip's snippets action: the saved
   *  phrases, fired into whatever owns input in the pane in front. Same
   *  surface as every other list this palette shows; `notice` carries a
   *  refusal from the last fire, which is why reopening with one is how a
   *  refusal stays on screen (design §11). */
  showSnippets(notice?: string): void {
    this._setNotice?.(notice)
    this._setVariant?.('snippets')
    this._setOpen?.(true)
  }

  /** Open the secret list — the tab-strip key icon (nocx-fk32): vault names
   *  only, inserted into whatever owns input in the pane in front. */
  showSecrets(): void {
    this._setNotice?.(undefined)
    this._setVariant?.('secrets')
    this._setOpen?.(true)
  }

  destroy(): void {
    this.dispose?.()
    this.dispose = null
    this._setOpen = null
    this._setVariant = null
    this._mounted = false
  }
}
