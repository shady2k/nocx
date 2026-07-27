/**
 * Export/backup/import section — Solid component mounted inside the settings
 * content pane.  Replaces the imperative DOM-building predecessor (519 → 0).
 * Four modes (ADR-0011 §7), each stating what it carries and omits.
 * The portable encrypted export prompts for a new passphrase with
 * confirmation via inline TextFields (not a Modal primitive, not prompt()).
 *
 * State: local createStores per sub-component. Nothing here is shared state —
 * these are per-operation busy flags and form drafts (nocx-imkb.5).
 */

import { For, Show } from 'solid-js'
import { createStore } from 'solid-js/store'
import { render } from 'solid-js/web'
import type { ProfileClient, ExportManifest, ConfigExport } from './profiles'
import { downloadJSON, downloadBinary } from './export-utils'
import { PageSection } from './ui/page-section'
import { Button } from './ui/button'
import { TextField } from './ui/text-field'
import { Checkbox } from './ui/checkbox'

// ── Mode definitions ────────────────────────────────────────────────────

type Mode = 'config-export' | 'portable-encrypted' | 'same-machine-backup' | 'import'

interface ModeDef {
  mode: Mode
  label: string
  summary: string
}

const MODES: ModeDef[] = [
  {
    mode: 'config-export',
    label: 'Configuration Export',
    summary: 'Profiles, groups, credential metadata, and settings',
  },
  {
    mode: 'portable-encrypted',
    label: 'Portable Encrypted Export',
    summary: 'Configuration encrypted under a new passphrase',
  },
  {
    mode: 'same-machine-backup',
    label: 'Same-Machine Backup',
    summary: 'File paths to copy; secrets stay in the OS keychain',
  },
  {
    mode: 'import',
    label: 'Import',
    summary: 'Restore a configuration export into this machine',
  },
]

// ── Manifest display ────────────────────────────────────────────────────

function ManifestDisplay(props: { manifest: ExportManifest }) {
  return (
    <ul class="st-export-manifest">
      <For each={props.manifest.carries}>
        {(item) => (
          <li class="st-export-carries">
            <span class="st-export-check">+</span> {item}
          </li>
        )}
      </For>
      <For each={props.manifest.omits}>
        {(item) => (
          <li class="st-export-omits">
            <span class="st-export-cross">−</span> {item}
          </li>
        )}
      </For>
      <Show when={props.manifest.notes}>
        <For each={props.manifest.notes}>{(note) => <li class="st-export-note">{note}</li>}</For>
      </Show>
    </ul>
  )
}

// ── Status line helper ─────────────────────────────────────────────────

function StatusLine(props: { message: string }) {
  return <div class="st-export-status">{props.message}</div>
}

// ── Config export actions ───────────────────────────────────────────────

function ConfigExportActions(props: { profileClient: ProfileClient }) {
  const [state, setState] = createStore({ status: '', busy: false })

  const handleClick = () => {
    setState('busy', true)
    setState('status', 'Exporting…')
    props.profileClient
      .configExport()
      .then(
        (result) => {
          downloadJSON('nocx-config-export.json', result)
          setState('status', 'Exported — file downloaded.')
        },
        (e) => {
          setState('status', `Export failed: ${String(e)}`)
        },
      )
      .finally(() => {
        setState('busy', false)
      })
  }

  return (
    <>
      <Button class="ui-export-btn" disabled={state.busy} onClick={handleClick}>
        Export Configuration
      </Button>
      <StatusLine message={state.status} />
    </>
  )
}

// ── Portable encrypted export actions ─────────────────────────────────

function PortableEncryptedActions(props: { profileClient: ProfileClient }) {
  const [state, setState] = createStore({
    passphrase: '',
    confirm: '',
    showPasswords: false,
    includePrivate: false,
    status: '',
    busy: false,
  })

  const handleEncrypt = () => {
    const pass = state.passphrase
    const conf = state.confirm
    if (!pass) {
      setState('status', 'Passphrase is required.')
      return
    }
    if (pass !== conf) {
      setState('status', 'Passphrases do not match.')
      return
    }
    setState('busy', true)
    setState('status', 'Encrypting…')
    props.profileClient
      .portableEncryptedExport(pass, state.includePrivate)
      .then(
        (result) => {
          downloadBinary('nocx-portable-export.enc', result.payload)
          setState('status', 'Exported — file downloaded. Keep the passphrase safe.')
          setState('passphrase', '')
          setState('confirm', '')
        },
        (e) => {
          setState('status', `Export failed: ${String(e)}`)
        },
      )
      .finally(() => {
        setState('busy', false)
      })
  }

  const inputType = () => (state.showPasswords ? 'text' : 'password')

  return (
    <>
      <div class="st-export-passphrase-form">
        <TextField
          label="New passphrase"
          type={inputType()}
          placeholder="Choose a strong passphrase"
          value={state.passphrase}
          onInput={(v) => setState('passphrase', v)}
        />
        <TextField
          label="Confirm passphrase"
          type={inputType()}
          placeholder="Re-enter the passphrase"
          value={state.confirm}
          onInput={(v) => setState('confirm', v)}
        />
        <Checkbox
          checked={state.showPasswords}
          onChange={(v) => setState('showPasswords', v)}
          label="Show passphrase"
        />
        <Checkbox
          checked={state.includePrivate}
          onChange={(v) => setState('includePrivate', v)}
          label="Include private content (conversations, command history)"
        />
      </div>
      <div class="st-export-btn-row">
        <Button
          class="ui-export-btn ui-export-btn-primary"
          variant="primary"
          disabled={state.busy}
          onClick={handleEncrypt}
        >
          Encrypt and Export
        </Button>
        <StatusLine message={state.status} />
      </div>
    </>
  )
}

// ── Backup actions ──────────────────────────────────────────────────────

function BackupActions(props: { profileClient: ProfileClient }) {
  const [state, setState] = createStore({
    status: '',
    busy: false,
    paths: '',
    pathsVisible: false,
  })

  const handleShow = () => {
    setState('busy', true)
    setState('status', 'Checking…')
    props.profileClient
      .backup()
      .then(
        (result) => {
          setState('paths', JSON.stringify(result, null, 2))
          setState('pathsVisible', true)
          setState('status', '')
        },
        (e) => {
          setState('status', `Backup check failed: ${String(e)}`)
          setState('pathsVisible', false)
        },
      )
      .finally(() => {
        setState('busy', false)
      })
  }

  return (
    <>
      <Button class="ui-export-btn" disabled={state.busy} onClick={handleShow}>
        Show Backup Paths
      </Button>
      <StatusLine message={state.status} />
      <Show when={state.pathsVisible}>
        <pre class="st-export-backup-details">{state.paths}</pre>
      </Show>
    </>
  )
}

// ── Import actions ──────────────────────────────────────────────────────

function ImportActions(props: { profileClient: ProfileClient }) {
  const [state, setState] = createStore({
    configFile: null as File | null,
    configStatus: '',
    configBusy: false,
    encFile: null as File | null,
    portablePass: '',
    portableStatus: '',
    portableBusy: false,
  })

  const handleConfigImport = () => {
    const file = state.configFile
    if (!file) return
    const pc = props.profileClient
    setState('configBusy', true)
    setState('configStatus', 'Importing…')
    file
      .text()
      .then((text) => {
        const data = JSON.parse(text) as ConfigExport
        return pc.importConfig(data)
      })
      .then((result) => {
        const parts: string[] = [
          `Imported ${result.profilesImported} profiles,`,
          `${result.groupsImported} groups,`,
          `${result.credentialsImported} credentials.`,
        ]
        if (result.unresolvedCredentials?.length) {
          parts.push(` ${result.unresolvedCredentials.length} credentials need secret mapping.`)
        }
        setState('configStatus', parts.join(' '))
      })
      .catch((e) => {
        setState('configStatus', `Import failed: ${String(e)}`)
      })
      .finally(() => {
        setState('configBusy', false)
      })
  }

  const handlePortableImport = () => {
    const file = state.encFile
    if (!file) return
    const pass = state.portablePass
    const pc = props.profileClient
    setState('portableBusy', true)
    setState('portableStatus', 'Decrypting and importing…')
    file
      .arrayBuffer()
      .then((buf) => {
        const base64 = btoa(Array.from(new Uint8Array(buf), (b) => String.fromCharCode(b)).join(''))
        return pc.importPortable(base64, pass)
      })
      .then((result) => {
        const parts: string[] = [
          `Imported ${result.profilesImported} profiles,`,
          `${result.groupsImported} groups,`,
          `${result.credentialsImported} credentials.`,
        ]
        if (result.unresolvedCredentials?.length) {
          parts.push(` ${result.unresolvedCredentials.length} credentials need secret mapping.`)
        }
        setState('portableStatus', parts.join(' '))
        setState('encFile', null)
        setState('portablePass', '')
      })
      .catch((e) => {
        setState('portableStatus', `Import failed: ${String(e)}`)
      })
      .finally(() => {
        setState('portableBusy', false)
      })
  }

  return (
    <>
      <div class="st-export-import-section">
        <label class="st-export-import-label">Import from configuration export (.json)</label>
        <input
          type="file"
          accept=".json"
          class="st-export-file-input"
          onChange={(e) => setState('configFile', e.currentTarget.files?.[0] ?? null)}
        />
        <Button
          class="ui-export-btn"
          disabled={state.configBusy || !state.configFile}
          onClick={handleConfigImport}
        >
          Import
        </Button>
        <StatusLine message={state.configStatus} />
      </div>
      <div class="st-export-import-section">
        <label class="st-export-import-label">Import from portable encrypted export (.enc)</label>
        <input
          type="file"
          accept=".enc"
          class="st-export-file-input"
          onChange={(e) => setState('encFile', e.currentTarget.files?.[0] ?? null)}
        />
        <TextField
          type="password"
          placeholder="Passphrase used during export"
          value={state.portablePass}
          onInput={(v) => setState('portablePass', v)}
        />
        <Button
          class="ui-export-btn"
          disabled={state.portableBusy || !state.encFile || !state.portablePass}
          onClick={handlePortableImport}
        >
          Decrypt and Import
        </Button>
        <StatusLine message={state.portableStatus} />
      </div>
    </>
  )
}

// ── Mode card ────────────────────────────────────────────────────────────

function ModeCard(props: { def: ModeDef; profileClient: ProfileClient }) {
  const [state, setState] = createStore({
    expanded: false,
    loaded: false,
    loading: false,
    manifest: null as ExportManifest | null,
    error: null as string | null,
  })

  const loadManifest = () => {
    setState('loading', true)
    props.profileClient
      .exportManifest(props.def.mode)
      .then(
        (m) => {
          setState('manifest', m)
          setState('loaded', true)
        },
        (e) => {
          setState('error', `Failed to load: ${String(e)}`)
        },
      )
      .finally(() => {
        setState('loading', false)
      })
  }

  const handleToggle = () => {
    const now = state.expanded
    setState('expanded', !now)
    if (!now && !state.loaded && !state.loading) {
      loadManifest()
    }
  }

  return (
    <div class="st-export-card" classList={{ 'st-export-card-expanded': state.expanded }}>
      <div class="st-export-card-header">
        <span class="st-export-card-label">{props.def.label}</span>
        <span class="st-export-card-summary">{props.def.summary}</span>
        <Button class="st-export-card-toggle" onClick={handleToggle}>
          {state.expanded ? 'Hide details' : 'Show details'}
        </Button>
      </div>
      <div class="st-export-card-body">
        <Show when={state.loading}>
          <div class="st-export-loading">Loading mode details…</div>
        </Show>
        <Show when={state.error !== null && !state.loading}>
          <div class="st-export-error">{state.error}</div>
        </Show>
        <Show when={state.manifest !== null && !state.loading}>
          <ManifestDisplay manifest={state.manifest!} />
          <div class="st-export-actions">
            <Show when={props.def.mode === 'config-export'}>
              <ConfigExportActions profileClient={props.profileClient} />
            </Show>
            <Show when={props.def.mode === 'portable-encrypted'}>
              <PortableEncryptedActions profileClient={props.profileClient} />
            </Show>
            <Show when={props.def.mode === 'same-machine-backup'}>
              <BackupActions profileClient={props.profileClient} />
            </Show>
            <Show when={props.def.mode === 'import'}>
              <ImportActions profileClient={props.profileClient} />
            </Show>
          </div>
        </Show>
      </div>
    </div>
  )
}

// ── Root component ───────────────────────────────────────────────────────

export function ExportSection(props: { profileClient: ProfileClient }) {
  return (
    <PageSection title="Export / Backup / Import" class="ui-export">
      <p class="ui-export-desc">
        Each mode states what it carries and what it omits. Private content and secrets are never
        included without an explicit choice.
      </p>
      <div class="ui-export-grid">
        <For each={MODES}>
          {(def) => <ModeCard def={def} profileClient={props.profileClient} />}
        </For>
      </div>
    </PageSection>
  )
}

// ── Island mount, for imperative callers only ───────────────────────────
// The settings surface is still imperative, so it cannot place <ExportSection/>
// as a child; it has to open a Solid root inside one of its elements. That is
// what this is — a mounting boundary, not a compatibility shim, and it goes
// when the settings surface migrates and renders the component directly.
//
// It returns the disposer deliberately. render() hands back the only way to
// tear the root down, and dropping it leaves effects alive on nodes the caller
// has already removed from the document.

export function mountExportSection(
  container: HTMLElement,
  profileClient: ProfileClient,
): () => void {
  return render(() => <ExportSection profileClient={profileClient} />, container)
}
