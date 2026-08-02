/**
 * BackupRestoreSection — Solid component for Backup & Restore (ADR-0018).
 */
import { createEffect, on, Show, For } from 'solid-js'
import { createStore } from 'solid-js/store'
import type {
  ProfileClient,
  RestoreStrategy,
  BackupCreateResult,
  RestorePreview,
  RestoreResult,
} from './profiles'
import { readBackupText, MAX_BACKUP_BYTES, downloadText } from './backup-file'
import { Button, FileInput, Radio, MarkerList } from './ui'
import { showToast } from './ui/toast'
import { showConfirm } from './ui/dialog'

interface Props {
  profileClient: ProfileClient
}

interface State {
  creating: boolean
  previewing: boolean
  restoring: boolean
  contents: string | null
  strategy: RestoreStrategy
  preview: RestorePreview | null
  previewError: string | null
  fileInputResetKey: number
}

const PLAINTEXT_WARNING = `The backup file is plaintext JSON. All settings values, hostnames, connection names, inline usernames, and auth modes are stored without encryption. Credential secrets (passwords, key passphrases) are never included, but the connection metadata may still be sensitive.`

export function BackupRestoreSection(props: Props) {
  const [state, setState] = createStore<State>({
    creating: false,
    previewing: false,
    restoring: false,
    contents: null,
    strategy: 'merge',
    preview: null,
    previewError: null,
    fileInputResetKey: 0,
  })

  let previewGen = 0
  const busy = () => state.creating || state.previewing || state.restoring

  const handleCreate = async () => {
    if (state.creating) return
    setState('creating', true)
    try {
      const result: BackupCreateResult = await props.profileClient.createBackup()

      // Try native save dialog first; fall back to browser download.
      let saved = false
      try {
        const saveResult = await props.profileClient.saveBackupToFile(
          result.fileName,
          result.contents,
        )
        if (saveResult !== null) {
          showToast({ message: `Backup saved to ${saveResult.path}`, level: 'success' })
          saved = true
        }
        // null = user cancelled; fall through to download fallback.
      } catch {
        // Native dialog failed (e.g. zenity not installed); fall through.
      }

      if (!saved) {
        downloadText(result.fileName, result.contents)
        const parts: string[] = [
          `Backup created: ${result.summary.settings} settings, ${result.summary.connections} connections, ${result.summary.groups} groups.`,
        ]
        if (result.summary.credentialBindingsRemoved > 0)
          parts.push(`${result.summary.credentialBindingsRemoved} credential binding(s) removed.`)
        if (result.summary.groupCredentialBindingsRemoved > 0)
          parts.push(`${result.summary.groupCredentialBindingsRemoved} group binding(s) removed.`)
        if (result.summary.groupDefaultKeysOmitted > 0)
          parts.push(`${result.summary.groupDefaultKeysOmitted} group key(s) omitted.`)
        showToast({ message: parts.join(' '), level: 'success' })
      }
    } catch (err) {
      showToast({
        message: `Backup failed: ${err instanceof Error ? err.message : String(err)}`,
        level: 'danger',
      })
    } finally {
      setState('creating', false)
    }
  }

  const loadPreview = async (file: File | null) => {
    if (!file) {
      setState({ contents: null, preview: null, previewError: null })
      return
    }
    setState({ previewing: true, previewError: null, preview: null })
    try {
      const text = await readBackupText(file)
      setState('contents', text)
      // The createEffect below reacts to contents change and calls previewBackupRestore.
    } catch (err) {
      setState({
        preview: null,
        previewError: (err as Error).message,
        fileInputResetKey: state.fileInputResetKey + 1,
        previewing: false,
      })
    }
  }

  // Single preview path: reacts to file selection (via contents) and strategy changes.
  createEffect(
    on(
      () => [state.strategy, state.contents] as const,
      async ([strat, contents]) => {
        if (!contents) return
        setState({ previewing: true, previewError: null, preview: null })
        previewGen++
        const gen = previewGen
        try {
          const result = await props.profileClient.previewBackupRestore(contents, strat)
          if (gen !== previewGen) return
          setState({ preview: result, previewError: null })
        } catch (err) {
          if (gen !== previewGen) return
          setState({ preview: null, previewError: (err as Error).message })
        } finally {
          if (gen === previewGen) setState('previewing', false)
        }
      },
      { defer: true },
    ),
  )

  const handleRestore = async () => {
    if (!state.contents || !state.preview) return
    const isReplace = state.strategy === 'replace'
    const msg = isReplace
      ? `Replace will reset all connections and settings to match the backup exactly. Extra connections and settings overrides will be removed. Credential metadata and keychain entries are not affected. Continue?`
      : `Merge will apply backup settings and connections on top of your current configuration. Existing items not in the backup are kept. Continue?`
    const confirmed = await showConfirm(msg, isReplace ? 'Replace' : 'Merge', 'Cancel')
    if (!confirmed) return

    setState('restoring', true)
    try {
      const result: RestoreResult = await props.profileClient.restoreBackup(
        state.contents,
        state.strategy,
        state.preview.previewToken,
      )
      const parts: string[] = [`Restore complete (${result.strategy}).`]
      if (result.settingsChanged > 0) parts.push(`${result.settingsChanged} settings changed.`)
      if (result.settingsReset > 0) parts.push(`${result.settingsReset} settings reset.`)
      if (result.connectionsAdded > 0) parts.push(`${result.connectionsAdded} connections added.`)
      if (result.connectionsUpdated > 0)
        parts.push(`${result.connectionsUpdated} connections updated.`)
      if (result.connectionsRemoved > 0)
        parts.push(`${result.connectionsRemoved} connections removed.`)
      if (result.groupsAdded > 0) parts.push(`${result.groupsAdded} groups added.`)
      if (result.groupsUpdated > 0) parts.push(`${result.groupsUpdated} groups updated.`)
      if (result.groupsRemoved > 0) parts.push(`${result.groupsRemoved} groups removed.`)
      if (result.groupCredentialBindingsRemoved > 0)
        parts.push(`${result.groupCredentialBindingsRemoved} group binding(s) removed.`)
      if (result.connectionsRequiringCredential.length > 0) {
        const names = result.connectionsRequiringCredential.map((c) => c.name).join(', ')
        parts.push(`Connections needing credential reassignment: ${names}.`)
      }
      showToast({ message: parts.join(' '), level: 'success' })
      setState({
        contents: null,
        preview: null,
        previewError: null,
        fileInputResetKey: state.fileInputResetKey + 1,
      })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      if (msg.includes('preview is stale')) {
        // Auto re-preview: trigger a fresh preview with retained contents and strategy.
        setState({ preview: null, previewError: null })
        // Bump the generation to discard any in-flight preview from the old effect.
        previewGen++
        // The createEffect watches strategy+contents; since neither changed, force a
        // re-fetch by nulling and restoring contents in the next microtask.
        const saved = state.contents
        setState('contents', null)
        queueMicrotask(() => {
          if (saved) setState('contents', saved)
        })
      }
      showToast({ message: `Restore failed: ${msg}`, level: 'danger' })
    } finally {
      setState('restoring', false)
    }
  }

  const preview = () => state.preview

  return (
    <div class="backup-restore">
      <section class="backup-restore__section">
        <h3>Create backup</h3>
        <p>
          Download a versioned backup file containing your non-secret settings, SSH connections, and
          connection groups. Credential records and secrets are never included.
        </p>
        <MarkerList
          items={[
            {
              text: 'Settings overrides (non-secret), SSH connections (without credential IDs), and connection groups (typed defaults subset without credential bindings).',
              tone: 'included',
            },
            {
              text: 'Credential records (УЗ), secret references (SecretID, PassphraseSecretID), OS keychain material, ContentDB (conversations, command history), declared defaults.',
              tone: 'excluded',
            },
          ]}
        />
        <div class="backup-restore__warning">
          <strong>Plaintext warning:</strong> {PLAINTEXT_WARNING}
        </div>
        <Button
          variant="primary"
          disabled={busy()}
          onClick={() => {
            void handleCreate()
          }}
        >
          {state.creating ? 'Creating…' : 'Create backup'}
        </Button>
      </section>

      <section class="backup-restore__section">
        <h3>Restore backup</h3>
        <p>
          Select a <code>nocx-backup</code> JSON file to preview and restore. Maximum file size:{' '}
          {MAX_BACKUP_BYTES / 1024 / 1024} MiB.
        </p>
        <FileInput
          accept=".json"
          disabled={busy()}
          resetKey={state.fileInputResetKey}
          buttonLabel="Choose backup file…"
          onChange={(f) => {
            void loadPreview(f)
          }}
        />

        <Show when={state.contents}>
          <div class="backup-restore__strategy">
            <label>Restore strategy</label>
            <Radio
              name="backup-strategy"
              value="merge"
              checked={state.strategy === 'merge'}
              onChange={(val) => setState('strategy', val as RestoreStrategy)}
              disabled={busy()}
              label="Merge — apply backup on top of current configuration (default)"
            />
            <Radio
              name="backup-strategy"
              value="replace"
              checked={state.strategy === 'replace'}
              onChange={(val) => setState('strategy', val as RestoreStrategy)}
              disabled={busy()}
              label="Replace — reset to exact backup snapshot (destroys extra connections/settings)"
            />
          </div>
        </Show>

        <Show when={state.previewing}>
          <p class="backup-restore__status">Generating preview…</p>
        </Show>

        <Show when={state.previewError}>
          <div class="backup-restore__error">{state.previewError}</div>
        </Show>

        <Show when={preview()}>
          {(p) => (
            <div class="backup-restore__preview">
              <h4>Preview — {p().strategy}</h4>
              <p>Backup created: {p().createdAt}</p>
              <table class="backup-restore__counts">
                <thead>
                  <tr>
                    <th />
                    <th>Settings</th>
                    <th>Connections</th>
                    <th>Groups</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td>Included</td>
                    <td>{p().settings.included}</td>
                    <td>{p().connections.included}</td>
                    <td>{p().groups.included}</td>
                  </tr>
                  <tr>
                    <td>Added</td>
                    <td>—</td>
                    <td>{p().connections.added}</td>
                    <td>{p().groups.added}</td>
                  </tr>
                  <tr>
                    <td>Changed/Updated</td>
                    <td>{p().settings.changed}</td>
                    <td>{p().connections.updated}</td>
                    <td>{p().groups.updated}</td>
                  </tr>
                  <tr>
                    <td>Removed/Reset</td>
                    <td>{p().settings.reset}</td>
                    <td>{p().connections.removed}</td>
                    <td>{p().groups.removed}</td>
                  </tr>
                </tbody>
              </table>

              <Show when={p().connectionsRequiringCredential.length > 0}>
                <div class="backup-restore__warning">
                  <strong>Connections requiring credential reassignment:</strong>
                  <ul>
                    <For each={p().connectionsRequiringCredential}>{(c) => <li>{c.name}</li>}</For>
                  </ul>
                </div>
              </Show>

              <Show
                when={
                  p().omissions.credentialBindingsRemoved +
                    p().omissions.groupCredentialBindingsRemoved +
                    p().omissions.groupDefaultKeysOmitted >
                  0
                }
              >
                <div class="backup-restore__warning">
                  <strong>Omissions:</strong>
                  <ul>
                    <Show when={p().omissions.credentialBindingsRemoved > 0}>
                      <li>
                        {p().omissions.credentialBindingsRemoved} connection credential binding(s)
                        removed
                      </li>
                    </Show>
                    <Show when={p().omissions.groupCredentialBindingsRemoved > 0}>
                      <li>
                        {p().omissions.groupCredentialBindingsRemoved} group credential binding(s)
                        removed
                      </li>
                    </Show>
                    <Show when={p().omissions.groupDefaultKeysOmitted > 0}>
                      <li>
                        {p().omissions.groupDefaultKeysOmitted} group default key(s) omitted from
                        backup
                      </li>
                    </Show>
                  </ul>
                </div>
              </Show>

              <Button
                variant={state.strategy === 'replace' ? 'danger' : 'primary'}
                disabled={busy()}
                onClick={() => {
                  void handleRestore()
                }}
              >
                {state.restoring
                  ? 'Restoring…'
                  : state.strategy === 'replace'
                    ? 'Replace configuration'
                    : 'Merge backup'}
              </Button>
            </div>
          )}
        </Show>
      </section>
    </div>
  )
}
