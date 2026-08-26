/**
 * KeyMaterialInput — the three ways a private key can be supplied: a path, a
 * chosen file, or pasted material.
 *
 * Extracted from the connection editor, which offered the three-way input in
 * two places (profile editor and group defaults); the Secrets page is a third.
 * One vocabulary, three call sites — "path / choose a file / paste" being
 * three ways to supply one key, which is what the design spec means.
 *
 * Two of the three modes supply key MATERIAL; only 'path' supplies a path.
 *
 * 'file' used to be treated as a path picker, reading `File.path` — an
 * Electron extension that exists in neither a browser nor a Wails webview,
 * so the fallback fired every time and a bare filename like `id_ed25519`
 * was stored as if it were a path. Choosing a file reads its contents, which
 * is what the mode's name says and the only thing achievable without a
 * native dialog. The difference between it and 'material' is how the key is
 * supplied, not what is stored.
 *
 * Path mode is the one the native dialog belongs to: when `openFileDialog`
 * is provided, the path field gains a Browse action that fills it with a
 * real absolute path from the platform picker. The runtime is often absent —
 * the dev-web harness has no Wails at all — and the picker then rejects; the
 * hint says so and the field stays hand-typable.
 */
import { createSignal, Show } from 'solid-js'
import { SegmentedControl } from './ui/segmented-control'
import { TextField } from './ui/text-field'
import { Field } from './ui/field'
import { Select } from './ui/select'
import { Button } from './ui/button'
import { FileInput } from './ui/file-input'
import { Prompt } from './ui/prompt'
import { Stack } from './ui/stack'
import { RpcError } from './dispatcher'
import { log } from './log'
import { showToast } from './ui/toast'
import type { ProfileClient } from './profiles'
import type { InventoryEntry } from './vault-client'

export type KeyInputMode = 'path' | 'file' | 'material' | 'secret'

/** Three of the four modes supply key MATERIAL; only 'path' supplies a path.
 *  Spelling that out once beats repeating `mode === 'file' || mode ===
 *  'material'` at every save site and losing the reason. */
export const suppliesMaterial = (m: KeyInputMode) => m === 'material' || m === 'file'

const KEY_MODES: { value: KeyInputMode; label: string }[] = [
  { value: 'file', label: 'Choose file' },
  { value: 'path', label: 'Path' },
  { value: 'material', label: 'Paste key' },
  { value: 'secret', label: 'Secret' },
]

/** Select options for a vault-row picker: the inventory rows, each named by
 *  its secret's name, plus — when the bound row is missing from that
 *  inventory (the secret was deleted, the vault could not answer, the row is
 *  of another kind) — a fallback option that KEEPS the binding as its value.
 *  Dropping it would read as "None", and the next save would clear a
 *  credential nobody meant to clear.
 *
 *  Its label says the picker cannot name it. It used to be the row handle,
 *  and that is what a person saw in the endpoint editor over a locked vault:
 *  `secrow:dd39558499fe31b5ddce0f88a5d31320` where the name of their own API
 *  key belongs, with nothing on screen to say why (nocx-5ratm). The handle
 *  is a row id, not a secret reference, and it means nothing to anybody. */
const UNLISTED_SECRET_LABEL = 'Unavailable secret'

export function secretOptions(
  entries: InventoryEntry[],
  bound?: string,
): { value: string; label: string }[] {
  const opts = entries.map((entry) => ({ value: entry.id, label: entry.name }))
  if (bound && !opts.some((o) => o.value === bound)) {
    opts.push({ value: bound, label: UNLISTED_SECRET_LABEL })
  }
  return opts
}

/**
 * What the input opens on, everywhere it is used.
 *
 * Choosing a file, because it is the only one of the three that asks the user
 * for nothing they have to know: Path wants an absolute path typed from
 * memory — and the native picker that would fill it in is absent outside a
 * packaged Wails build — while Paste wants the key on the clipboard. The
 * first segment is the same one, so the selected option is also the leftmost
 * and the control reads in the order a user tries things.
 *
 * Exported rather than repeated: the mode is reset to its initial value on
 * every open and close of every editor, and four call sites spelling the
 * default themselves is four places for it to drift.
 */
export const DEFAULT_KEY_MODE: KeyInputMode = 'file'

export interface KeyMaterialInputProps {
  /** Element-id prefix: `<id>-path` and `<id>-text`. Call sites pass e.g.
   *  `profile-key` or `group-default-key` so the emitted ids stay
   *  `profile-key-path` / `profile-key-text` — the ids the connection
   *  editor's tests and selectors already use. */
  id: string
  mode: KeyInputMode
  onModeChange: (mode: KeyInputMode) => void
  /** Path mode. */
  pathValue: string
  onPathChange: (path: string) => void
  pathPlaceholder?: string
  /** Material mode. */
  materialValue: string
  onMaterialChange: (value: string) => void
  /** Parent-side error (e.g. invalid key material), shown under the material
   *  field. File-read failures are the component's own. */
  error?: string
  /** Fingerprint caption under a pasted key. */
  fingerprint?: string
  /** Native file picker (dialog.openFile). When present, Path mode gets a
   *  Browse action that fills the path with a real absolute path. */
  openFileDialog?: () => Promise<{ path: string }>
  /** Secret mode: the vault's private-key rows, with the bound one as the
   *  current value (ADR-0017 §1, b5bu). Empty when the vault is locked. The
   *  props are optional because call sites that only create material (the
   *  Secrets page) never offer an existing secret. */
  secrets?: InventoryEntry[]
  secretValue?: string
  onSecretChange?: (value: string | undefined) => void
}

/**
 * The one mistake worth catching before the round trip: uploading `id_x.pub`
 * instead of `id_x`.
 *
 * Deliberately narrow. This does not attempt to validate a private key —
 * that is the backend's job, it has the parser, and a second opinion in the
 * renderer would be a second source of truth about what a key is. This only
 * recognises an OpenSSH PUBLIC key, which is a single line beginning with its
 * algorithm name, and says so in the words the user needs.
 */
export function publicKeyMistake(text: string): string | undefined {
  const first = text.trim().split('\n', 1)[0] ?? ''
  if (/^(ssh-(rsa|dss|ed25519)|ecdsa-sha2-|sk-(ssh|ecdsa))/.test(first)) {
    return 'That is a public key. nocx needs the private key — the file without the .pub suffix.'
  }
  return undefined
}

export function KeyMaterialInput(props: KeyMaterialInputProps) {
  const [fileError, setFileError] = createSignal<string | undefined>(undefined)

  const changeMode = (value: string) => {
    setFileError(undefined)
    props.onModeChange(value as KeyInputMode)
  }

  // Prop reads happen in the handler bodies (event-handler scope, which
  // Solid tracks); the promise callbacks only touch values captured there.
  const browse = () => {
    if (!props.openFileDialog) return
    const open = props.openFileDialog
    const changePath = props.onPathChange
    void open().then(
      (result) => {
        if (result.path) changePath(result.path)
      },
      () => {
        // The native picker's absence is the outcome of pressing Browse, not
        // a standing property of the field — the toast carries it, and it
        // tells the user what to do instead.
        showToast({
          level: 'danger',
          message: 'The native file picker is not available here. Type the path by hand.',
        })
      },
    )
  }

  return (
    <>
      <SegmentedControl
        options={KEY_MODES}
        value={props.mode}
        onChange={changeMode}
        ariaLabel="Key input mode"
      />
      <Show when={props.mode === 'path'}>
        <div class="km-path-row">
          <TextField
            id={`${props.id}-path`}
            label="Private Key Path"
            value={props.pathValue}
            onInput={(value) => props.onPathChange(value)}
            placeholder={props.pathPlaceholder ?? '~/.ssh/id_ed25519'}
            error={props.error}
          />
          <Show when={props.openFileDialog}>
            <Button
              variant="default"
              onClick={browse}
              ariaLabel="Browse for a private key file"
              title="Choose a file with the system picker"
            >
              Browse…
            </Button>
          </Show>
        </div>
      </Show>
      <Show when={props.mode === 'file'}>
        <Field for={`${props.id}-file`} error={fileError() ?? props.error}>
          <FileInput
            id={`${props.id}-file`}
            accept="*"
            onChange={(file) => {
              if (!file) return
              const change = props.onMaterialChange
              setFileError(undefined)
              void file.text().then(
                (text) => {
                  setFileError(publicKeyMistake(text))
                  change(text)
                },
                () => {
                  setFileError(undefined)
                  // A file that cannot be read is the outcome of the read,
                  // not a fact about the field — the toast carries it. The
                  // field error stays for what is known at the moment of choosing.
                  showToast({
                    level: 'danger',
                    message: 'Could not read that file. Choose another, or paste the key.',
                  })
                },
              )
            }}
            ariaLabel="Choose private key file"
            buttonLabel="Choose file…"
          />
        </Field>
      </Show>
      <Show when={props.mode === 'material'}>
        <TextField
          multiline
          id={`${props.id}-text`}
          label="Private Key"
          value={props.materialValue}
          onInput={(value) => {
            setFileError(publicKeyMistake(value))
            props.onMaterialChange(value)
          }}
          placeholder="Paste the private key content here"
          error={fileError() ?? props.error}
        />
        <Show when={props.fingerprint}>
          <span class="cm-key-fingerprint">Fingerprint: {props.fingerprint}</span>
        </Show>
      </Show>
      <Show when={props.mode === 'secret'}>
        <Field for={`${props.id}-secret`} label="Private Key Secret" error={props.error}>
          <Select
            value={props.secretValue ?? ''}
            onChange={(value) => props.onSecretChange?.(value || undefined)}
            options={secretOptions(props.secrets ?? [], props.secretValue)}
            placeholder={'\u2014 None \u2014'}
          />
        </Field>
      </Show>
    </>
  )
}

export interface KeyPassphrasePromptProps {
  open: boolean
  /** The key this passphrase belongs to — the prompt names it, so a vault
   *  passphrase can never be typed into it by mistake (nocx-s8jn). */
  keyName: string
  /** Row handle of the key secret whose passphrase is wanted (ADR-0017). */
  keyRow: string
  /** The display name the minted passphrase secret owns (ADR-0016) — the
   *  key's own name ("Key for root@host") is not it: the prompt stores a
   *  passphrase, and says so ("Passphrase for root@host"). */
  passphraseName: string
  client: ProfileClient
  /** 'saved' when a VERIFIED passphrase was stored — the minted passphrase
   *  row rides along so the caller can bind it; 'skipped' when declined
   *  (the key stays stored, and the connection asks at connect time). */
  onResult: (outcome: 'saved' | 'skipped', row?: string) => void
}

/**
 * KeyPassphrasePrompt — asked when a passphrase-protected private key has
 * been saved (saveKeyMaterial reported passphraseWanted). The backend verifies
 * the passphrase against the stored key material before storing it: a wrong
 * one is refused there and then, at the moment the user can fix it, instead of
 * surfacing at connect time where nothing can be done about it (nocx-dze3).
 */
export function KeyPassphrasePrompt(props: KeyPassphrasePromptProps) {
  const [passphrase, setPassphrase] = createSignal('')
  const [error, setError] = createSignal('')
  const [saving, setSaving] = createSignal(false)

  const save = async () => {
    if (!passphrase()) {
      setError('Enter the key passphrase')
      return
    }
    setError('')
    setSaving(true)
    try {
      // The prompt is asked about the KEY; the secret it stores is the
      // passphrase, and it says so. The stored name is the caller's
      // passphraseName — never the key's own name ("Key for root@host").
      const result = await props.client.saveKeyPassphrase(
        props.keyRow,
        passphrase(),
        props.passphraseName,
      )
      showToast({ level: 'success', message: 'Key passphrase stored.' })
      props.onResult('saved', result.row)
    } catch (e) {
      setSaving(false)
      if (
        e instanceof RpcError &&
        typeof e.data === 'object' &&
        e.data &&
        'reason' in e.data &&
        e.data.reason === 'invalid-key-passphrase'
      ) {
        // The backend's own sentence: it distinguishes a wrong passphrase
        // from a key that cannot be verified at all.
        setError((e as Error).message)
        return
      }
      const message = (e as Error).message
      log.error('Failed to store key passphrase', { message })
      showToast({ level: 'danger', message: `Could not save the key passphrase: ${message}` })
    }
  }

  /** Declining is allowed: the key stays stored, and the interface says the
   *  connection will ask for the passphrase when it connects. */
  const decline = () => {
    showToast({
      level: 'info',
      message: 'No passphrase stored — the connection will ask for it when it connects.',
    })
    props.onResult('skipped')
  }

  return (
    <Prompt
      open={props.open}
      onClose={decline}
      ariaLabel={`Passphrase for ${props.keyName}`}
      placement="top-sheet"
      title={`Passphrase for ${props.keyName}`}
      onSubmit={() => {
        if (saving()) return
        void save()
      }}
      actions={
        <>
          <Button variant="primary" disabled={saving()} onClick={() => void save()}>
            {saving() ? 'Saving…' : 'Save passphrase'}
          </Button>
          <Button variant="default" disabled={saving()} onClick={decline}>
            Not now
          </Button>
        </>
      }
    >
      <Stack>
        <p class="ui-vault-desc-text">
          This key is encrypted. The passphrase you save here opens it — not the vault, not a
          connection password.
        </p>
        <TextField
          id="key-passphrase"
          label="Key passphrase"
          placeholder="The passphrase that protects this key"
          type="password"
          value={passphrase()}
          onInput={(v) => {
            setPassphrase(v)
            setError('')
          }}
          error={error()}
          autoFocus
        />
      </Stack>
    </Prompt>
  )
}
