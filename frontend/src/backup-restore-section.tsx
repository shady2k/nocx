/**
 * BackupRestoreSection — Solid component for Backup & Restore (ADR-0027).
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
import { Button, FileInput, Radio, MarkerList, PageSection } from './ui'
import { showToast } from './ui/toast'
import { showConfirm } from './ui/dialog'
import { isNotStartedAnswer } from './dispatcher'
import { t } from './i18n/locale-model'

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
  fileInputResetKey: number
  /** Why the last preview did not run, when the control plane refused it
   *  before it started: the refusal's own sentence, shown here rather than
   *  in a toast, with the retry beneath it (ADR-8eej3e66 stage 1). */
  previewRefused: string | null
  /** Bumped when the person asks for a refused preview again. */
  previewAttempt: number
}

/** A rejected call's own sentence, as it will be shown. */
function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/** A file the person picked that `readBackupText` could not read, in their words. */
function readBackupFileSentence(err: unknown): string {
  const raw = err instanceof Error ? err.message : String(err)
  // `readBackupText` throws exactly one error of its own: the size cap. Name
  // the limit in a unit a person reads — not the byte count the wire carries.
  if (raw.startsWith('File exceeds ')) {
    return `The file is larger than ${MAX_BACKUP_BYTES / (1024 * 1024)} MiB`
  }
  // Everything else is `file.text()` rejecting — a read the page cannot name
  // more precisely than the file being unreadable.
  return 'the file could not be read'
}

/**
 * The backend's exact preview refusal, in a person's words.
 *
 * Readable backup bytes still get refused by the backend (a wrong format, an
 * unsupported version, a rule the document violates), and the wire carries
 * the reason verbatim — `invalid backup document: …`. A person who chose a
 * file wants to know the file is wrong, not that a package refused it.
 */
function restorePreviewSentence(err: unknown): string {
  const raw = err instanceof Error ? err.message : String(err)
  const invalid = /^invalid backup document: (.+)$/.exec(raw)
  if (invalid) return invalid[1]
  return 'the backup could not be previewed'
}

export function BackupRestoreSection(props: Props) {
  const [state, setState] = createStore<State>({
    creating: false,
    previewing: false,
    restoring: false,
    contents: null,
    strategy: 'merge',
    preview: null,
    fileInputResetKey: 0,
    previewRefused: null,
    previewAttempt: 0,
  })

  let previewGen = 0
  let fileGen = 0
  const busy = () => state.creating || state.previewing || state.restoring

  const handleCreate = async () => {
    if (state.creating) return
    setState('creating', true)
    try {
      const result: BackupCreateResult = await props.profileClient.createBackup()

      // Three outcomes, and only one of them is a download. `backup.saveToFile`
      // resolves to a path when the user picked one and to `null` when they
      // cancelled the dialog (contracts/backup.saveToFile.schema.json models
      // both). It rejects when there is no dialog to open at all — no saver
      // wired, or no zenity/osascript on the machine. Cancelling is the user
      // declining the file; falling back to a download there hands them the
      // very thing they just refused, and puts an unasked-for plaintext copy
      // of their configuration in Downloads.
      let dialogAvailable = true
      let savedPath: string | null = null
      try {
        const saveResult = await props.profileClient.saveBackupToFile(
          result.fileName,
          result.contents,
        )
        savedPath = saveResult === null ? null : saveResult.path
      } catch {
        dialogAvailable = false
      }

      if (dialogAvailable && savedPath === null) {
        showToast({
          message: t('backup.discarded', 'Backup discarded — save was cancelled.'),
          level: 'info',
        })
      } else {
        // What was left behind is the part worth reading, and it is the same
        // whether the file went through the dialog or the download — the two
        // used to say different things, so a user who saved never learned that
        // a credential binding had been dropped.
        const parts = [
          savedPath !== null ? `Backup saved to ${savedPath}.` : 'Backup downloaded.',
          `${result.summary.settings} settings, ${result.summary.connections} connections, ${result.summary.groups} groups.`,
        ]
        if (result.summary.credentialBindingsRemoved > 0)
          parts.push(`${result.summary.credentialBindingsRemoved} credential binding(s) removed.`)
        if (result.summary.groupCredentialBindingsRemoved > 0)
          parts.push(`${result.summary.groupCredentialBindingsRemoved} group binding(s) removed.`)
        if (result.summary.groupDefaultKeysOmitted > 0)
          parts.push(`${result.summary.groupDefaultKeysOmitted} group key(s) omitted.`)
        if (savedPath === null) downloadText(result.fileName, result.contents)
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
    const gen = ++fileGen
    previewGen++
    if (!file) {
      setState({ contents: null, preview: null, previewing: false, previewRefused: null })
      return
    }
    setState({ previewing: true, preview: null, previewRefused: null })
    try {
      const text = await readBackupText(file)
      if (gen !== fileGen) return
      setState('contents', text)
    } catch (err) {
      if (gen !== fileGen) return
      setState({ preview: null, fileInputResetKey: state.fileInputResetKey + 1, previewing: false })
      showToast({
        message: `Could not read the backup file: ${readBackupFileSentence(err)}`,
        level: 'danger',
      })
    }
  }

  createEffect(
    on(
      () => [state.strategy, state.contents, state.previewAttempt] as const,
      async ([strat, contents]) => {
        if (!contents) return
        setState({ previewing: true, preview: null, previewRefused: null })
        previewGen++
        const gen = previewGen
        try {
          const result = await props.profileClient.previewBackupRestore(contents, strat)
          if (gen !== previewGen) return
          setState({ preview: result })
        } catch (err) {
          if (gen !== previewGen) return
          // A call that did not start is not the backup's fault and not
          // final: the file is still chosen, nothing ran, and nothing is
          // wrong with it. Choosing the same file again fires no change
          // event, so without a way to ask again the section is a dead end
          // (nocx-9ox05). The sentence is the wire's, shown HERE next to the
          // remedy instead of in a toast over the whole app (ADR-8eej3e66
          // §8).
          if (isNotStartedAnswer(err)) {
            setState({ preview: null, previewRefused: messageOf(err) })
            return
          }
          setState({ preview: null })
          showToast({
            message: `Could not preview the backup: ${restorePreviewSentence(err)}`,
            level: 'danger',
          })
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
      ? 'Replace will reset all connections and settings to match the backup exactly. Extra connections and settings overrides will be removed. Credential metadata and keychain entries are not affected. Continue?'
      : 'Merge will apply backup settings and connections on top of your current configuration. Existing items not in the backup are kept. Continue?'
    const confirmed = await showConfirm(msg, isReplace ? 'Replace' : 'Merge', 'Cancel')
    if (!confirmed) return

    setState('restoring', true)
    try {
      const result: RestoreResult = await props.profileClient.restoreBackup(
        state.contents,
        state.strategy,
        state.preview.previewToken,
      )
      const parts = [`Restore complete (${result.strategy}).`]
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
        fileInputResetKey: state.fileInputResetKey + 1,
      })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      if (msg.includes('preview is stale')) {
        setState({ preview: null })
        previewGen++
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
      <PageSection
        title={t('backup.create.title', 'Create backup')}
        description={t(
          'backup.create.desc',
          'Download a versioned backup file containing your non-secret settings, SSH connections, and connection groups. Credential records and secrets are never included.',
        )}
      >
        <MarkerList
          items={[
            {
              text: t(
                'backup.markers.settingsIn',
                'Settings overrides (non-secret), SSH connections (without credential IDs), and connection groups (typed defaults subset without credential bindings).',
              ),
              tone: 'included',
            },
            {
              text: t(
                'backup.markers.secretsOut',
                'Credential records, secret references, OS keychain material, ContentDB, and declared defaults.',
              ),
              tone: 'excluded',
            },
          ]}
        />
        <div class="backup-restore__warning">
          <strong>
            {t('backup.plaintextWarning', 'Plaintext warning:')}{' '}
            {t(
              'backup.plaintext.body',
              'The backup file is plaintext JSON. All settings values, hostnames, connection names, inline usernames, and auth modes are stored without encryption. Credential secrets (passwords, key passphrases) are never included, but the connection metadata may still be sensitive.',
            )}
          </strong>
        </div>
        <Button
          variant="primary"
          disabled={busy()}
          onClick={() => {
            void handleCreate()
          }}
        >
          {state.creating ? 'Creating…' : t('backup.create.title', 'Create backup')}
        </Button>
      </PageSection>

      <PageSection
        title={t('backup.restore.title', 'Restore backup')}
        description={
          t('backup.restore.desc', 'Select a nocx-backup JSON file to preview and restore.') +
          ` Maximum file size: ${MAX_BACKUP_BYTES / 1024 / 1024} MiB.`
        }
      >
        <FileInput
          accept=".json"
          disabled={busy()}
          resetKey={state.fileInputResetKey}
          buttonLabel={t('backup.choose.buttonLabel', 'Choose backup file…')}
          onChange={(f) => {
            void loadPreview(f)
          }}
        />

        <Show when={state.contents}>
          <div class="backup-restore__strategy">
            <label>{t('backup.strategy', 'Restore strategy')}</label>
            <Radio
              name="backup-strategy"
              value="merge"
              checked={state.strategy === 'merge'}
              onChange={(val) => setState('strategy', val as RestoreStrategy)}
              disabled={busy()}
              label={t(
                'backup.strategy.merge',
                'Merge — apply backup on top of current configuration (default)',
              )}
            />
            <Radio
              name="backup-strategy"
              value="replace"
              checked={state.strategy === 'replace'}
              onChange={(val) => setState('strategy', val as RestoreStrategy)}
              disabled={busy()}
              label={t(
                'backup.strategy.replace',
                'Replace — reset to exact backup snapshot (destroys extra connections/settings)',
              )}
            />
          </div>
        </Show>

        <Show when={state.previewing}>
          <p class="backup-restore__status">
            {t('backup.preview.generating', 'Generating preview…')}
          </p>
        </Show>

        <Show when={state.previewRefused !== null && !state.previewing}>
          <p class="backup-restore__status" data-testid="backup-preview-refused">
            {t('backup.preview.refusedHead', 'Could not build the preview.')} {state.previewRefused}
          </p>
          <Button
            disabled={busy()}
            onClick={() => setState('previewAttempt', state.previewAttempt + 1)}
          >
            {t('backup.preview.again', 'Preview again')}
          </Button>
        </Show>

        <Show when={preview()}>
          {(p) => (
            <div class="backup-restore__preview">
              <h4>
                {t('backup.preview.head', 'Preview —')} {p().strategy}
              </h4>
              <p>
                {t('backup.createdHead', 'Backup created:')} {p().createdAt}
              </p>
              <table class="backup-restore__counts">
                <thead>
                  <tr>
                    <th />
                    <th>{t('backup.cat.settings', 'Settings')}</th>
                    <th>{t('backup.cat.connections', 'Connections')}</th>
                    <th>{t('backup.cat.groups', 'Groups')}</th>
                    <th>{t('backup.cat.snippets', 'Snippets')}</th>
                    <th>{t('backup.cat.notes', 'Notes')}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td>{t('backup.diff.included', 'Included')}</td>
                    <td>{p().settings.included}</td>
                    <td>{p().connections.included}</td>
                    <td>{p().groups.included}</td>
                    <td>{p().snippets.included}</td>
                    <td>{p().notes.included}</td>
                  </tr>
                  <tr>
                    <td>{t('backup.diff.added', 'Added')}</td>
                    <td>—</td>
                    <td>{p().connections.added}</td>
                    <td>{p().groups.added}</td>
                    <td>—</td>
                    <td>—</td>
                  </tr>
                  <tr>
                    <td>{t('backup.diff.changed', 'Changed/Updated')}</td>
                    <td>{p().settings.changed}</td>
                    <td>{p().connections.updated}</td>
                    <td>{p().groups.updated}</td>
                    <td>—</td>
                    <td>—</td>
                  </tr>
                  <tr>
                    <td>{t('backup.diff.removed', 'Removed/Reset')}</td>
                    <td>{p().settings.reset}</td>
                    <td>{p().connections.removed}</td>
                    <td>{p().groups.removed}</td>
                    <td>—</td>
                    <td>—</td>
                  </tr>
                </tbody>
              </table>

              <Show when={p().connectionsRequiringCredential.length > 0}>
                <div class="backup-restore__warning">
                  <strong>
                    {t('backup.reassign.head', 'Connections requiring credential reassignment:')}
                  </strong>
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
                  <strong>{t('backup.omissions.head', 'Omissions:')}</strong>
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
      </PageSection>
    </div>
  )
}
