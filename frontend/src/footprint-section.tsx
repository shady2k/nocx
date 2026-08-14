/**
 * FootprintSection — the visible, removable footprint of nocx's silent
 * install (nocx-mlm7 P10, delivery-modes design §4.1/§9): what nocx wrote on
 * which host, where, and when it was last seen — and the uninstall action.
 *
 * The data comes from shell.footprint.status, which is fact-backed and NEVER
 * connects: lastObservedAt is "when nocx last SAW this bundle", not a claim
 * about what is on the host right now, and a host wiped since then is
 * described as last seen, never as installed.
 *
 * Uninstall is offered exactly where removableProfileId is present — a saved
 * connection resolves to the destination, so the backend owns credentials
 * and the dial. Absence of that field IS the explanation: the row states
 * plainly that removal needs a saved connection and names ~/.nocx as the
 * path to remove by hand. An action is never offered that would fail at
 * click time (AGENTS.md rule 1).
 *
 * Kit contract: rows are the kit's CollectionRow (info + actions); the name
 * and meta lines reuse the connections page's cm-item-* surface classes;
 * every control is a kit component, never repainted here.
 */
import { For, Show, createSignal, onMount } from 'solid-js'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { CollectionRow } from './ui/collection-view'
import { EmptyState } from './ui/empty-state'
import { PageSection } from './ui/page-section'
import { Spinner } from './ui/spinner'
import { Stack } from './ui/stack'
import { showConfirm } from './ui/dialog'
import { showToast } from './ui/toast'
import { RefreshIcon, TrashIcon } from './ui/icons'
import type { FootprintClient } from './footprint-client'
import type { ShellFootprintStatusResult } from './generated/shell.footprint.status'

/** One destination's footprint row — the generated result's element type. */
type FootprintDestination = ShellFootprintStatusResult['destinations'][number]

/** One helper install row — the generated result's element type. */
type HelperInstall = NonNullable<ShellFootprintStatusResult['helpers']>[number]

export interface FootprintSectionProps {
  /** Absent in the dev-web harness and in surfaces that predate the RPC;
   *  without a client the section shows nothing rather than offering an
   *  action that cannot run. */
  client?: FootprintClient
}

/** "last seen" is an observation, never "installed now" (this surface does
 *  not connect): the label says so in the row itself. */
function formatLastSeen(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

export function FootprintSection(props: FootprintSectionProps) {
  /** null = loading; [] = loaded, nothing installed. */
  const [destinations, setDestinations] = createSignal<FootprintDestination[] | null>(null)
  /** The observed helper footprint (remote-helper design D8); null until
   *  the first load answers, [] once it has. */
  const [helpers, setHelpers] = createSignal<HelperInstall[] | null>(null)
  /** The identity currently being uninstalled, to disable its row. */
  const [busy, setBusy] = createSignal<string | null>(null)

  const load = async (): Promise<void> => {
    if (!props.client) return
    try {
      const res = await props.client.status()
      setDestinations(res.destinations)
      setHelpers(res.helpers ?? [])
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      showToast({ level: 'danger', message: `Could not read the remote footprint: ${msg}` })
      setDestinations([])
      setHelpers([])
    }
  }

  onMount(() => {
    void load()
  })

  const uninstall = async (d: FootprintDestination): Promise<void> => {
    if (!props.client || !d.removableProfileId) return
    const ok = await showConfirm(
      `Remove nocx's shell integration from ${d.identity}? Only manifest-owned, unmodified files are removed; anything you changed stays and is reported.`,
      'Uninstall',
      'Cancel',
    )
    if (!ok) return
    setBusy(d.identity)
    try {
      const res = await props.client.uninstall(d.removableProfileId)
      showToast({
        level: 'success',
        message: `Removed ${res.removed.length} file(s) from ${d.identity}`,
      })
      if (res.conflicts.length > 0) {
        showToast({
          level: 'warning',
          message: `${res.conflicts.length} modified file(s) kept: ${res.conflicts.join(', ')}`,
        })
      }
      await load()
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      showToast({ level: 'danger', message: `Uninstall failed: ${msg}` })
    } finally {
      setBusy(null)
    }
  }

  const refresh = (): void => {
    setDestinations(null)
    void load()
  }

  // Without a client (dev-web harness, tests, surfaces that predate the RPC)
  // the section renders nothing — never a spinner that spins forever or an
  // action that cannot run. The condition lives inside JSX rather than as an
  // early return: a Solid component body runs once, so returning early there
  // would freeze the decision at first render.
  return (
    <Show when={props.client}>
      <PageSection
        id="remote-footprint"
        title="Remote footprint"
        description="nocx installs shell integration on a remote host automatically. This is what it wrote, where, and when it was last seen — no connection is made to show it. Only a saved connection to the host can remove it."
      >
        <Show when={destinations() === null}>
          <Spinner label="Loading the remote footprint" size="sm" />
        </Show>
        <Show when={destinations() !== null && destinations()!.length === 0}>
          <EmptyState
            title="Nothing installed"
            description="nocx has not left shell integration on any host. A host appears here the first time its integration was observed."
            action={
              <Button variant="ghost" size="sm" onClick={refresh}>
                <RefreshIcon />
                Refresh
              </Button>
            }
          />
        </Show>
        <Show when={destinations() !== null && destinations()!.length > 0}>
          <Stack divided dense>
            <For each={destinations()}>
              {(d) => (
                <CollectionRow
                  info={
                    <>
                      <div class="cm-item-name">{d.identity}</div>
                      <div class="cm-item-meta">
                        <Badge tone="info">{d.generation}</Badge>
                        <span>{d.path}</span>
                        <span>
                          protocol {d.protocolVersion} &middot; scripts {d.scriptVersion}
                        </span>
                        <span>last seen {formatLastSeen(d.lastObservedAt)}</span>
                      </div>
                    </>
                  }
                  actions={
                    d.removableProfileId !== null ? (
                      <Button
                        variant="danger"
                        size="sm"
                        disabled={busy() !== null}
                        onClick={() => void uninstall(d)}
                        ariaLabel={`Uninstall from ${d.identity}`}
                      >
                        <TrashIcon />
                        {busy() === d.identity ? 'Removing…' : 'Uninstall'}
                      </Button>
                    ) : (
                      <span class="cm-item-meta">
                        Removal needs a saved connection &mdash; remove {d.path} by hand
                      </span>
                    )
                  }
                />
              )}
            </For>
          </Stack>
        </Show>
        <Show when={helpers() !== null && helpers()!.length > 0}>
          <div class="cm-item-meta" role="heading" aria-level={3}>
            Remote helper
          </div>
          <Stack divided dense>
            <For each={helpers()}>
              {(h) => (
                <CollectionRow
                  info={
                    <>
                      <div class="cm-item-name">{h.identity}</div>
                      <div class="cm-item-meta">
                        <Badge tone="info">helper</Badge>
                        <span>{h.path}</span>
                        <span>hash {h.hash.slice(0, 12)}…</span>
                        <span>installed {formatLastSeen(h.installedAt)}</span>
                      </div>
                    </>
                  }
                  actions={
                    <span class="cm-item-meta">
                      The helper serves git and other remote features on this machine
                    </span>
                  }
                />
              )}
            </For>
          </Stack>
        </Show>
      </PageSection>
    </Show>
  )
}
