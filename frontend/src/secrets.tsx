/**
 * SecretsSection — vault contents page, one row per stored value.
 *
 * Replaces the old CredentialsSection. Shows a permanent explanation line,
 * then the vault inventory when unsealed, or a locked state when sealed.
 *
 * The secret owns its name (ADR-0016): each row renders the vault's name for
 * the secret — never derived, never blank, never a reference — and the page
 * offers Add (the user is asked for the name) and Rename. A secret with no
 * connection using it is a row like any other; that is the point of the ADR.
 *
 * No Reveal, no Copy of a stored value. Settlement: ADR-0011 §2 and
 * vault design §3.1.
 */
import { For, Show, Switch, Match, createSignal, createEffect, onMount } from 'solid-js'
import { Button } from './ui/button'
import { IconButton } from './ui/icon-button'
import { EmptyState } from './ui/empty-state'
import { CollectionRow, CollectionView } from './ui/collection-view'
import { Badge } from './ui/badge'
import { Dialog } from './ui/dialog'
import { Stack } from './ui/stack'
import { TextField } from './ui/text-field'
import { SegmentedControl } from './ui/segmented-control'
import { createFormValidation, required } from './ui/validation'
import { KeyIcon, LockIcon, PencilIcon, ResetIcon, TrashIcon } from './ui/icons'
import {
  DEFAULT_KEY_MODE,
  KeyMaterialInput,
  KeyPassphrasePrompt,
  suppliesMaterial,
} from './key-material-input'
import type { KeyInputMode } from './key-material-input'
import { showToast } from './ui/toast'
import type { VaultClient, InventoryEntry } from './vault-client'
import type { VaultController } from './vault'
import type { DialogClient } from './dialog-client'
import type { ProfileClient } from './profiles'
import { log } from './log'
export interface SecretsSectionProps {
  vaultClient: VaultClient
  vaultController: VaultController
  /** Native dialog capability (dialog.*). Absent in the dev-web harness and
   *  in tests; the surfaces then degrade to typing paths by hand. */
  dialogClient?: DialogClient
  /** Connection usage (secrets.usage). Needed to NAME the connections a
   *  delete would break — the delete confirmation says which connections
   *  stop working before anything is deleted. */
  profileClient?: ProfileClient
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; entries: InventoryEntry[] }
  | { kind: 'empty' }
  | { kind: 'error'; message: string }

// What the material is, in words a user reads. The wire vocabulary is closed
// (registry spec §4.1) — a new kind is an addition here, never a degradation
// into the raw token. The badge is how two rows with the same name and store
// (a private key and its passphrase) stay distinguishable (nocx-mg9r).
const KIND_LABELS: Record<string, string> = {
  password: 'Password',
  'key-passphrase': 'Key passphrase',
  'private-key': 'Private key',
  'public-key': 'Public key',
  'otp-seed': 'OTP seed',
}

const STORE_LABELS: Record<string, string> = {
  system: 'System keychain',
  file: 'Encrypted nocx storage',
}
// The kinds a user can create on this page. The wire vocabulary is closed and
// wider; this page offers the three the surface names today. Private keys are
// supplied three ways (path / file / paste) through KeyMaterialInput — the
// same vocabulary the connection editor uses.
const ADD_KINDS = [
  { value: 'password', label: 'Password' },
  { value: 'key-passphrase', label: 'Key passphrase' },
  { value: 'private-key', label: 'Private key' },
] as const

type AddKind = (typeof ADD_KINDS)[number]['value']

export function SecretsSection(props: SecretsSectionProps) {
  const [loadState, setLoadState] = createSignal<LoadState>({ kind: 'loading' })

  // Add-secret dialog state. The value input depends on the kind: passwords
  // and passphrases take a single field; a private key takes the three-way
  // input (path / file / paste) shared with the connection editor.
  const [addOpen, setAddOpen] = createSignal(false)
  const [addName, setAddName] = createSignal('')
  const [addKind, setAddKind] = createSignal<AddKind>('password')
  const [addValue, setAddValue] = createSignal('')
  const [keyPassphraseAsk, setKeyPassphraseAsk] = createSignal<{
    keyRow: string
    keyName: string
    passphraseName: string
    resolve: (outcome: { saved: boolean; row?: string }) => void
  } | null>(null)
  const askKeyPassphrase = (
    keyRow: string,
    keyName: string,
    passphraseName: string,
  ): Promise<{ saved: boolean; row?: string }> =>
    new Promise((resolve) => setKeyPassphraseAsk({ keyRow, keyName, passphraseName, resolve }))
  const [addKeyMode, setAddKeyMode] = createSignal<KeyInputMode>(DEFAULT_KEY_MODE)
  const [addKeyMaterial, setAddKeyMaterial] = createSignal('')
  const [addKeyPath, setAddKeyPath] = createSignal('')
  const [addBusy, setAddBusy] = createSignal(false)
  const addValidation = createFormValidation({
    name: () => required('Name')(addName()),
    value: () =>
      addKind() === 'private-key'
        ? suppliesMaterial(addKeyMode())
          ? required('Key')(addKeyMaterial())
          : required('Path')(addKeyPath())
        : required('Value')(addValue()),
  })

  // Replace-value dialog state — the row being replaced, addressed by its
  // opaque handle, never by a secret reference (nocx-jb20.1). The value
  // field is EMPTY on open and labelled as a replacement: the vault never
  // hands the old value back (ADR-0011 §2), so this is "write a new one",
  // not "edit the value".
  const [replaceTarget, setReplaceTarget] = createSignal<InventoryEntry | null>(null)
  const [replaceValue, setReplaceValue] = createSignal('')
  const [replaceKeyMode, setReplaceKeyMode] = createSignal<KeyInputMode>(DEFAULT_KEY_MODE)
  const [replaceKeyMaterial, setReplaceKeyMaterial] = createSignal('')
  const [replaceKeyPath, setReplaceKeyPath] = createSignal('')
  const [replaceBusy, setReplaceBusy] = createSignal(false)
  const replaceValidation = createFormValidation({
    value: () => {
      const entry = replaceTarget()
      if (!entry) return undefined
      if (entry.kind === 'private-key') {
        return suppliesMaterial(replaceKeyMode())
          ? required('New key')(replaceKeyMaterial())
          : required('Path')(replaceKeyPath())
      }
      return required('New value')(replaceValue())
    },
  })

  // Rename dialog state — the row being renamed, addressed by its opaque
  // handle, never by a secret reference (nocx-jb20.1).
  const [renameTarget, setRenameTarget] = createSignal<InventoryEntry | null>(null)
  const [renameName, setRenameName] = createSignal('')

  // Delete-secret dialog state — the row being deleted, addressed by its
  // opaque handle (nocx-jb20.1). A secret that connections still use names
  // them before anything can be deleted; a secret nothing uses goes with a
  // plain confirmation. The delete button is never the dialog's default
  // action — Cancel is (ui/README: "Enter must not fire a destructive
  // confirmation").
  const [deleteTarget, setDeleteTarget] = createSignal<InventoryEntry | null>(null)
  const [deleteBusy, setDeleteBusy] = createSignal(false)
  // Connection names for the target secret. null while loading,
  // [] once loaded with nothing to name. Only fetched for rows in use — a
  // plain confirmation for a secret nothing uses.
  const [deleteNames, setDeleteNames] = createSignal<string[] | null>(null)
  const [deleteNamesError, setDeleteNamesError] = createSignal<string | null>(null)
  const [renameBusy, setRenameBusy] = createSignal(false)
  const renameValidation = createFormValidation({
    name: () => required('Name')(renameName()),
  })

  const [filter, setFilter] = createSignal('')

  /** The rows the filter leaves. Matching on the name alone: it is the thing
   *  the user typed and the only field they chose. */
  const visibleEntries = () => {
    const st = loadState()
    if (st.kind !== 'loaded') return [] as InventoryEntry[]
    const q = filter().trim().toLowerCase()
    if (!q) return st.entries
    return st.entries.filter((e) => e.name.toLowerCase().includes(q))
  }

  const status = () => props.vaultController.status()

  async function load(): Promise<void> {
    setLoadState({ kind: 'loading' })
    try {
      const inv = await props.vaultClient.inventory()
      if (inv.entries.length === 0) {
        setLoadState({ kind: 'empty' })
      } else {
        setLoadState({ kind: 'loaded', entries: inv.entries })
      }
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to load secrets', { message })
      showToast({ level: 'danger', message: 'Could not load secrets: ' + message })
      setLoadState({ kind: 'error', message })
    }
  }

  // Refresh status on mount. The createEffect on status() below handles
  // loading inventory once the vault state is known.
  onMount(() => {
    void props.vaultController.refresh()
  })

  // Load inventory when the vault transitions to unsealed.
  createEffect(() => {
    const s = status()
    if (s && s.state === 'unsealed') {
      void load()
    }
  })

  function openAdd(): void {
    addValidation.reset()
    setAddName('')
    setAddKind('password')
    setAddValue('')
    setAddKeyMode(DEFAULT_KEY_MODE)
    setAddKeyMaterial('')
    setAddKeyPath('')
    setAddOpen(true)
  }

  function closeAdd(): void {
    if (addBusy()) return
    setAddOpen(false)
  }

  async function submitAdd(): Promise<void> {
    if (!addValidation.valid()) {
      addValidation.revealAll()
      return
    }
    setAddBusy(true)
    try {
      // A private key in path mode is dereferenced by the BACKEND at save.
      const params: {
        name: string
        kind: 'password' | 'key-passphrase' | 'private-key'
        value?: string
        path?: string
      } = {
        name: addName().trim(),
        kind: addKind(),
      }
      if (addKind() === 'private-key' && !suppliesMaterial(addKeyMode())) {
        params.path = addKeyPath().trim()
      } else if (addKind() === 'private-key') {
        params.value = addKeyMaterial()
      } else {
        params.value = addValue()
      }
      // An encrypted private key needs its passphrase stored with it: mint
      // through the same verified path the connection editor uses, and ask
      // for the passphrase when the backend reports the key is locked. A
      // cancelled or wrong passphrase aborts the add — the key is not
      if (addKind() === 'private-key' && suppliesMaterial(addKeyMode())) {
        const pc = props.profileClient
        if (!pc) {
          setAddOpen(false)
          showToast({ level: 'danger', message: 'This window cannot add a private key.' })
          void load()
          return
        }
        const minted = await pc.saveKeyMaterial(addKeyMaterial(), addName().trim())
        if (minted.passphraseWanted) {
          const outcome = await askKeyPassphrase(
            minted.row,
            addName().trim(),
            `Passphrase for ${addName().trim()}`,
          )
          if (!outcome.saved) {
            setAddOpen(false)
            showToast({
              level: 'info',
              message: 'Key added without a passphrase — a connection using it will ask for one.',
            })
            void load()
            return
          }
        }
      } else {
        await props.vaultClient.createSecret(params)
      }
      setAddOpen(false)
      showToast({ level: 'success', message: `Added "${addName().trim()}"` })
      void load()
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to add secret', { message })
      showToast({ level: 'danger', message: `Could not add secret: ${message}` })
    } finally {
      setAddBusy(false)
    }
  }

  function openReplace(entry: InventoryEntry): void {
    replaceValidation.reset()
    setReplaceTarget(entry)
    setReplaceValue('')
    setReplaceKeyMode(DEFAULT_KEY_MODE)
    setReplaceKeyMaterial('')
    setReplaceKeyPath('')
  }

  function closeReplace(): void {
    if (replaceBusy()) return
    setReplaceTarget(null)
  }

  async function submitReplace(): Promise<void> {
    const entry = replaceTarget()
    if (!entry) return
    if (!replaceValidation.valid()) {
      replaceValidation.revealAll()
      return
    }
    setReplaceBusy(true)
    try {
      // The reference does not change: replaceSecret writes the new material
      // under the same SecretID, so every connection using the secret keeps
      // working (the backend guarantees it; the renderer never sees the id).
      const params: { id: string; value?: string; path?: string } = { id: entry.id }
      if (entry.kind === 'private-key' && !suppliesMaterial(replaceKeyMode())) {
        params.path = replaceKeyPath().trim()
      } else if (entry.kind === 'private-key') {
        params.value = replaceKeyMaterial()
      } else {
        params.value = replaceValue()
      }
      await props.vaultClient.replaceSecret(params)
      setReplaceTarget(null)
      showToast({ level: 'success', message: `Replaced the value of "${entry.name}"` })
      void load()
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to replace secret', { message })
      showToast({ level: 'danger', message: `Could not replace the value: ${message}` })
    } finally {
      setReplaceBusy(false)
    }
  }

  function openRename(entry: InventoryEntry): void {
    renameValidation.reset()
    setRenameTarget(entry)
    setRenameName(entry.name)
  }

  function closeRename(): void {
    if (renameBusy()) return
    setRenameTarget(null)
  }

  async function submitRename(): Promise<void> {
    const entry = renameTarget()
    if (!entry) return
    if (!renameValidation.valid()) {
      renameValidation.revealAll()
      return
    }
    setRenameBusy(true)
    try {
      await props.vaultClient.renameSecret({ id: entry.id, name: renameName().trim() })
      setRenameTarget(null)
      showToast({ level: 'success', message: `Renamed to "${renameName().trim()}"` })
      void load()
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to rename secret', { message })
      showToast({ level: 'danger', message: `Could not rename secret: ${message}` })
    } finally {
      setRenameBusy(false)
    }
  }

  function openDelete(entry: InventoryEntry): void {
    setDeleteTarget(entry)
    setDeleteBusy(false)
    setDeleteNamesError(null)
    if (entry.usedBy > 0) {
      // The confirmation must NAME the connections this breaks before
      // anything can be deleted — the count alone is not the feature.
      setDeleteNames(null)
      void loadDeleteNames(entry)
    } else {
      setDeleteNames([])
    }
  }

  async function loadDeleteNames(entry: InventoryEntry): Promise<void> {
    setDeleteNames(null)
    setDeleteNamesError(null)
    const pc = props.profileClient
    if (!pc) {
      setDeleteNamesError('this window cannot check which connections use the secret')
      return
    }
    try {
      const { profiles: refs } = await pc.secretUsage(entry.id)
      setDeleteNames(refs.map((r) => r.profileName).filter(Boolean))
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to load connection usage for the delete confirmation', { message })
      setDeleteNamesError(message)
    }
  }

  function closeDelete(): void {
    if (deleteBusy()) return
    setDeleteTarget(null)
  }

  async function submitDelete(): Promise<void> {
    const entry = deleteTarget()
    if (!entry) return
    setDeleteBusy(true)
    try {
      await props.vaultClient.deleteSecret({ id: entry.id })
      setDeleteTarget(null)
      showToast({ level: 'success', message: `Deleted "${entry.name}"` })
      // The row is gone from the list without a manual refresh.
      void load()
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to delete secret', { message })
      showToast({ level: 'danger', message: `Could not delete secret: ${message}` })
    } finally {
      setDeleteBusy(false)
    }
  }

  return (
    <div class="sr-root">
      {/* A Switch, not nested Shows. The nested form made a missing case
          invisible: `uninitialized` fell through every branch and the page
          rendered nothing at all — a blank panel, no heading, no plate — and
          so did `loading` and `error`. Only `sealed`, `empty` and `loaded`
          were ever named, because before the vault could be reset there was
          no easy way to reach the others with this page open. A Switch makes
          the set of states something you have to look at. */}
      <Switch fallback={<EmptyState icon={<KeyIcon />} title="Loading secrets…" />}>
        <Match when={status()?.state === 'uninitialized'}>
          <EmptyState
            icon={<LockIcon />}
            title="Protection is not set up yet"
            description="nocx has nowhere to keep passwords until protection is set up. Nothing is stored, and nothing is lost."
            action={
              <Button variant="primary" onClick={() => props.vaultController.openSetup()}>
                Set up protection
              </Button>
            }
          />
        </Match>

        <Match when={status()?.state === 'sealed'}>
          <EmptyState
            icon={<LockIcon />}
            title="Vault is locked"
            description="Unlock the vault to see what secrets it holds."
            action={
              <Button
                variant="primary"
                onClick={() => props.vaultController.openUnlock('view your secrets')}
              >
                Unlock vault
              </Button>
            }
          />
        </Match>

        <Match when={loadState().kind === 'error'}>
          <EmptyState
            icon={<KeyIcon />}
            title="Could not load secrets"
            description={(loadState() as Extract<LoadState, { kind: 'error' }>).message}
            action={
              <Button variant="default" onClick={() => void load()}>
                Try again
              </Button>
            }
          />
        </Match>

        {/* Unsealed, with or without contents: the same surface either way.
            The toolbar has to exist before there is anything to filter, or
            "Add a secret" would appear only once a secret already existed —
            which is the shape of defect this repo has shipped before, where
            the only way to create the first thing was to already have one. */}
        <Match when={loadState().kind === 'empty' || loadState().kind === 'loaded'}>
          <CollectionView
            searchValue={filter()}
            onSearch={setFilter}
            searchPlaceholder="Filter secrets"
            searchLabel="Filter secrets"
            actions={
              <Button variant="primary" onClick={openAdd}>
                + Add a secret
              </Button>
            }
            hasItems={visibleEntries().length > 0}
            empty={
              <Show
                when={loadState().kind === 'loaded'}
                fallback={
                  <EmptyState
                    icon={<KeyIcon />}
                    title="Vault is empty"
                    description="There are no secrets in the vault yet. Add one here, or save a password on a connection and it appears. Whatever is stored is encrypted and never shown back to you."
                  />
                }
              >
                {/* Contents exist; the filter is what hid them. Saying "vault
                    is empty" here would be a lie the user can disprove by
                    clearing the box. */}
                <EmptyState
                  icon={<KeyIcon />}
                  title="No secret matches that"
                  description="Nothing in the vault matches the filter."
                />
              </Show>
            }
          >
            <For each={visibleEntries()}>
              {(entry) => (
                <CollectionRow
                  info={
                    <div class="sr-row-info">
                      <span class="sr-row-icon">
                        <KeyIcon />
                      </span>
                      <div class="sr-row-body">
                        <span class="sr-row-label">{entry.name}</span>
                        <span class="sr-row-usage">
                          {entry.usedBy} connection{entry.usedBy === 1 ? '' : 's'}
                        </span>
                      </div>
                    </div>
                  }
                  actions={
                    <>
                      {/* What the material IS, before which store holds it:
                          two rows with the same generated name (a key and its
                          passphrase) are told apart by this badge (nocx-mg9r).
                          The icon alone cannot carry that — both are keys. */}
                      <Badge tone="info">{KIND_LABELS[entry.kind] ?? entry.kind}</Badge>
                      {/* Which store holds it is a status, and a status is a
                          Badge — the same vocabulary the diagnostics and the
                          unreachable marker beside it already use. As bare
                          text it read as part of the name and ran into the
                          row's right edge. */}
                      <Badge tone="neutral">{STORE_LABELS[entry.provider] ?? entry.provider}</Badge>
                      <Show when={!entry.reachable}>
                        <Badge tone="danger">Store unreachable</Badge>
                      </Show>
                      <IconButton
                        size="md"
                        ariaLabel={`Replace value of ${entry.name}`}
                        title="Replace value"
                        onClick={() => openReplace(entry)}
                      >
                        <ResetIcon />
                      </IconButton>
                      <IconButton
                        size="md"
                        ariaLabel={`Rename ${entry.name}`}
                        title="Rename secret"
                        onClick={() => openRename(entry)}
                      >
                        <PencilIcon />
                      </IconButton>
                      <IconButton
                        size="md"
                        ariaLabel={`Delete ${entry.name}`}
                        title="Delete secret"
                        onClick={() => openDelete(entry)}
                      >
                        <TrashIcon />
                      </IconButton>
                    </>
                  }
                />
              )}
            </For>
          </CollectionView>
        </Match>
      </Switch>

      {/* Add dialog — the user was asked for the name and the kind, because
          they set out to create a secret (ADR-0016). Mounted only while open:
          the value field is a password input and must not sit in the DOM (or
          the accessibility tree) of a closed page. */}
      <Show when={addOpen()}>
        <Dialog
          open={addOpen()}
          onClose={closeAdd}
          title="Add secret"
          onSubmit={() => void submitAdd()}
          footer={
            <>
              <Button variant="primary" onClick={() => void submitAdd()} disabled={addBusy()}>
                Add secret
              </Button>
              <Button variant="default" onClick={closeAdd} disabled={addBusy()}>
                Cancel
              </Button>
            </>
          }
        >
          <Stack gap="default">
            <TextField
              id="sr-add-name"
              label="Name"
              placeholder="e.g. prod password"
              value={addName()}
              onInput={setAddName}
              onBlur={() => addValidation.touch('name')}
              error={addValidation.error('name')}
              required
            />
            <SegmentedControl
              options={ADD_KINDS as unknown as { value: string; label: string }[]}
              value={addKind()}
              onChange={(v) => setAddKind(v as AddKind)}
              ariaLabel="Kind"
            />
            <Show when={addKind() !== 'private-key'}>
              <TextField
                id="sr-add-value"
                label="Value"
                type="password"
                value={addValue()}
                onInput={setAddValue}
                onBlur={() => addValidation.touch('value')}
                error={addValidation.error('value')}
                required
              />
            </Show>
            <Show when={addKind() === 'private-key'}>
              {/* The three-way input, the same vocabulary the connection
                  editor offers. A key in path mode is dereferenced by the
                  backend at save time; file and paste supply material. */}
              <KeyMaterialInput
                id="sr-add-key"
                mode={addKeyMode()}
                onModeChange={setAddKeyMode}
                pathValue={addKeyPath()}
                onPathChange={(v) => {
                  setAddKeyPath(v)
                  addValidation.touch('value')
                }}
                materialValue={addKeyMaterial()}
                onMaterialChange={setAddKeyMaterial}
                error={addValidation.error('value')}
                openFileDialog={props.dialogClient?.openFileDialog.bind(props.dialogClient)}
              />
            </Show>
          </Stack>
        </Dialog>
      </Show>

      {/* Key-passphrase ask — an encrypted private key was just minted; the
          passphrase is asked for right there, verified against the key. */}
      <Show when={keyPassphraseAsk()}>
        {(ask) => (
          <Show when={props.profileClient}>
            <KeyPassphrasePrompt
              open
              keyName={ask().keyName}
              keyRow={ask().keyRow}
              passphraseName={ask().passphraseName}
              client={props.profileClient!}
              onResult={(outcome, row) => {
                const resolve = ask().resolve
                setKeyPassphraseAsk(null)
                resolve(outcome === 'saved' ? { saved: true, row } : { saved: false })
              }}
            />
          </Show>
        )}
      </Show>

      {/* Replace-value dialog — the field is EMPTY and labelled as a
          replacement: the vault never hands the old value back (ADR-0011
          §2), so "edit the value" would be a lie the interface cannot keep.
          Addressed by the row's opaque handle. */}
      <Show when={replaceTarget()}>
        {(target) => (
          <Dialog
            open={true}
            onClose={closeReplace}
            title={`Replace value of "${target().name}"`}
            onSubmit={() => void submitReplace()}
            footer={
              <>
                <Button
                  variant="primary"
                  onClick={() => void submitReplace()}
                  disabled={replaceBusy()}
                >
                  Replace value
                </Button>
                <Button variant="default" onClick={closeReplace} disabled={replaceBusy()}>
                  Cancel
                </Button>
              </>
            }
          >
            <Stack gap="default">
              <p class="sr-replace-hint">
                Write a new value. The stored value is never shown and stays in place until you
                save. Every connection using this secret keeps working.
              </p>
              <Show when={target().kind !== 'private-key'}>
                <TextField
                  id="sr-replace-value"
                  label="New value"
                  type="password"
                  value={replaceValue()}
                  onInput={(v) => {
                    setReplaceValue(v)
                    replaceValidation.touch('value')
                  }}
                  onBlur={() => replaceValidation.touch('value')}
                  error={replaceValidation.error('value')}
                  required
                />
              </Show>
              <Show when={target().kind === 'private-key'}>
                <KeyMaterialInput
                  id="sr-replace-key"
                  mode={replaceKeyMode()}
                  onModeChange={setReplaceKeyMode}
                  pathValue={replaceKeyPath()}
                  onPathChange={(v) => {
                    setReplaceKeyPath(v)
                    replaceValidation.touch('value')
                  }}
                  materialValue={replaceKeyMaterial()}
                  onMaterialChange={setReplaceKeyMaterial}
                  error={replaceValidation.error('value')}
                  openFileDialog={props.dialogClient?.openFileDialog.bind(props.dialogClient)}
                />
              </Show>
            </Stack>
          </Dialog>
        )}
      </Show>

      {/* Rename dialog — addressed by the row's opaque handle. */}
      <Show when={renameTarget()}>
        {(target) => (
          <Dialog
            open={true}
            onClose={closeRename}
            title={`Rename "${target().name}"`}
            onSubmit={() => void submitRename()}
            footer={
              <>
                <Button
                  variant="primary"
                  onClick={() => void submitRename()}
                  disabled={renameBusy()}
                >
                  Rename
                </Button>
                <Button variant="default" onClick={closeRename} disabled={renameBusy()}>
                  Cancel
                </Button>
              </>
            }
          >
            <Stack gap="default">
              <TextField
                id="sr-rename-name"
                label="Name"
                value={renameName()}
                onInput={setRenameName}
                onBlur={() => renameValidation.touch('name')}
                error={renameValidation.error('name')}
                required
              />
            </Stack>
          </Dialog>
        )}
      </Show>

      {/* Delete confirmation — addressed by the row's opaque handle. A secret
          that connections still use NAMES them before it can be deleted, and
          the delete button is never the default action: Cancel is autofocused
          so a stray Enter cannot destroy anything (ui/README: "Enter must not
          fire a destructive confirmation"). A secret nothing uses goes with a
          plain confirmation. */}
      <Show when={deleteTarget()}>
        {(target) => (
          <Dialog
            open={true}
            onClose={closeDelete}
            title={`Delete "${target().name}"?`}
            footer={
              <>
                <Button
                  variant="danger"
                  disabled={deleteBusy() || deleteNames() === null || deleteNamesError() !== null}
                  onClick={() => void submitDelete()}
                >
                  {deleteBusy() ? 'Deleting…' : 'Delete secret'}
                </Button>
                <Button variant="default" onClick={closeDelete} disabled={deleteBusy()} autofocus>
                  Cancel
                </Button>
              </>
            }
          >
            <Stack gap="default">
              <Show when={target().usedBy > 0}>
                <Show when={deleteNames() === null && deleteNamesError() === null}>
                  <p class="sr-delete-hint">Checking which connections use this secret…</p>
                </Show>
                <Show when={deleteNamesError() !== null}>
                  <p class="sr-delete-hint" role="alert">
                    Could not check which connections use this secret: {deleteNamesError()}
                  </p>
                  <Button
                    variant="default"
                    disabled={deleteBusy()}
                    onClick={() => void loadDeleteNames(target())}
                  >
                    Try again
                  </Button>
                </Show>
                <Show when={deleteNames() !== null && deleteNamesError() === null}>
                  <p class="sr-delete-hint">
                    These connections use this secret and will stop using it:
                  </p>
                  <Show when={deleteNames()!.length > 0}>
                    <ul class="sr-delete-connections">
                      <For each={deleteNames()}>{(name) => <li>{name}</li>}</For>
                    </ul>
                  </Show>
                  <Show when={deleteNames()!.length === 0}>
                    <p class="sr-delete-hint">
                      {target().usedBy} connection{target().usedBy === 1 ? '' : 's'} use it, but the
                      connection name could not be resolved.
                    </p>
                  </Show>
                </Show>
              </Show>
              <Show when={target().usedBy === 0}>
                <p class="sr-delete-hint">
                  No connection uses this secret. The stored value is destroyed and cannot be
                  recovered.
                </p>
              </Show>
              <p class="sr-delete-hint">This cannot be undone.</p>
            </Stack>
          </Dialog>
        )}
      </Show>
    </div>
  )
}
