import { createSignal, Show, untrack, type JSX } from 'solid-js'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Field } from './ui/field'
import { SecretValueField } from './ui/secret-value-field'
import { Select, type SelectOption } from './ui/select'
import { Stack } from './ui/stack'
import { TextField } from './ui/text-field'
import type { InventoryEntry, VaultSecretKind } from './vault-client'

/** What a door already knows when it opens the ask. */
export interface SecretCreateAsk {
  /** The proposed display name. Filled in, editable, never demanded. */
  name: string
  /** The derived kind. Chosen, editable. */
  kind: VaultSecretKind
  /** The value already typed where the person was standing, or no value. */
  value?: string
}

/** The vault seam the ask writes through. */
export interface SecretCreateVault {
  list(): Promise<InventoryEntry[]>
  createSecret(params: {
    name: string
    kind: VaultSecretKind
    value: string
    resolve?: boolean
  }): Promise<{ name: string }>
}

export interface SecretCreateDialogProps {
  /** Null means closed; the password input is absent from the DOM then. */
  ask: SecretCreateAsk | null
  vault: SecretCreateVault
  onClose: () => void
  /** The handle and name the vault actually stored. */
  onCreated: (created: { handle: string; name: string }) => void
}

const SECRET_KIND_LABELS: Record<VaultSecretKind, string> = {
  password: 'Password',
  'key-passphrase': 'Key passphrase',
  'private-key': 'Private key',
  'public-key': 'Public key',
  'otp-seed': 'OTP seed',
  'api-token': 'API token',
}

const SECRET_KIND_OPTIONS: SelectOption[] = (
  Object.keys(SECRET_KIND_LABELS) as VaultSecretKind[]
).map((value) => ({ value, label: SECRET_KIND_LABELS[value] }))

function firstFreeVariant(name: string, entries: InventoryEntry[]): string {
  const names = new Set(entries.map((entry) => entry.name))
  let suffix = 2
  let candidate = `${name} ${suffix}`
  while (names.has(candidate)) {
    suffix += 1
    candidate = `${name} ${suffix}`
  }
  return candidate
}

function SecretCreateForm(
  props: Omit<SecretCreateDialogProps, 'ask'> & { ask: SecretCreateAsk },
): JSX.Element {
  const [name, setName] = createSignal(untrack(() => props.ask.name))
  const [kind, setKind] = createSignal<VaultSecretKind>(untrack(() => props.ask.kind))
  const [nameError, setNameError] = createSignal('')

  const submit = async (value: string): Promise<void> => {
    const candidate = name().trim()
    if (candidate.startsWith('secrow:')) {
      const message = 'Secret names cannot start with "secrow:"'
      setNameError(message)
      throw new Error(message)
    }

    setNameError('')
    const entries = await props.vault.list()
    if (entries.some((entry) => entry.name === candidate)) {
      const message = `A secret named "${candidate}" is already in the vault`
      setNameError(message)
      setName(firstFreeVariant(candidate, entries))
      throw new Error(message)
    }

    const created = await props.vault.createSecret({
      name: candidate,
      kind: kind(),
      value,
      resolve: true,
    })
    const updatedEntries = await props.vault.list()
    const createdEntry = updatedEntries.find((entry) => entry.name === created.name)
    if (!createdEntry) {
      throw new Error(`Secret "${created.name}" was not found in the vault inventory`)
    }

    props.onCreated({ handle: createdEntry.id, name: created.name })
    props.onClose()
  }

  return (
    <Dialog
      open={true}
      onClose={props.onClose}
      title="Create secret"
      footer={<Button onClick={props.onClose}>Cancel</Button>}
    >
      <Stack gap="default">
        <TextField
          id="secret-create-name"
          label="Name"
          error={nameError() || undefined}
          value={name()}
          onInput={(next) => {
            setName(next)
            setNameError('')
          }}
          placeholder="e.g. prod password"
        />
        <Field for="secret-create-kind" label="Kind">
          <Select
            id="secret-create-kind"
            options={SECRET_KIND_OPTIONS}
            value={kind()}
            onChange={(next) => setKind(next as VaultSecretKind)}
          />
        </Field>
        <Field for="secret-create-value" label="Value">
          <SecretValueField
            id="secret-create-value"
            ariaLabel="Value"
            placeholder="Paste the secret value"
            actionLabel="Save to vault"
            title="Save secret to vault"
            initialValue={props.ask.value}
            onSubmit={submit}
          />
        </Field>
      </Stack>
    </Dialog>
  )
}

export function SecretCreateDialog(props: SecretCreateDialogProps): JSX.Element {
  return (
    <Show when={props.ask} keyed>
      {(ask) => <SecretCreateForm {...props} ask={ask} />}
    </Show>
  )
}
