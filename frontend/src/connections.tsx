/**
 * ConnectionsView — Solid component for the connections manager.
 *
 * Replaces the imperative ConnectionManagerViewImpl (deleted in the same
 * commit). Uses the UI kit (Button, TextField, Checkbox, Select, Toolbar)
 * and Solid reactive state instead of hand-rolled DOM and private fields.
 *
 * Behaviour that must match the predecessor:
 * - Header with title and action buttons
 * - Profile list on the left, form panel on the right
 * - Grouped profile display using buildGroupTree
 * - Full SSH profile editing including credential selector, auth radio,
 *   advanced settings, jump host selector
 * - Credential CRUD with host-binding validation
 * - Quick connect via dblclick or SSH button
 * - Import from Tabby
 * - onConnect callback
 */
import { For, Show, createSignal, createMemo, onMount } from 'solid-js'
import { render } from 'solid-js/web'
import { Button } from './ui/button'
import { TextField } from './ui/text-field'
import { Checkbox } from './ui/checkbox'
import { Select, type SelectOption } from './ui/select'
import { Toolbar } from './ui/toolbar'
import { Section } from './ui/section'
import type { SSHProfile, ProfileGroup, Credential, AuthMode, TreeNode } from './profiles'
import { ProfileClient, buildGroupTree, newProfileID } from './profiles'
import { log } from './log'

// ── Helpers ─────────────────────────────────────────────────────────────────

function authModeLabel(mode: AuthMode): string {
  switch (mode) {
    case '':
      return 'Auto'
    case 'password':
      return 'Password'
    case 'publicKey':
      return 'Public Key'
    case 'agent':
      return 'Agent'
    case 'keyboardInteractive':
      return 'Keyboard Interactive'
  }
}

const AUTH_MODES: AuthMode[] = ['', 'password', 'publicKey', 'agent', 'keyboardInteractive']
const CRED_AUTH_MODES: AuthMode[] = ['password', 'publicKey', 'agent']

// ── Props ────────────────────────────────────────────────────────────────────

export interface ConnectionsViewProps {
  client: ProfileClient
  onConnect?: (profile: SSHProfile) => void
}

// ── Component ────────────────────────────────────────────────────────────────

export function ConnectionsView(props: ConnectionsViewProps) {
  // ── Data state ──────────────────────────────────────────────────────────
  const [profiles, setProfiles] = createSignal<SSHProfile[]>([])
  const [groups, setGroups] = createSignal<ProfileGroup[]>([])
  const [credentials, setCredentials] = createSignal<Credential[]>([])

  // ── Selection state ─────────────────────────────────────────────────────
  const [selectedID, setSelectedID] = createSignal('')
  const [editing, setEditing] = createSignal<SSHProfile | null>(null)
  const [editingCredential, setEditingCredential] = createSignal<Credential | null>(null)

  // ── Password input ref for credential form ──────────────────────────────
  let passwordInputRef: HTMLInputElement | undefined

  // ── Data loading ────────────────────────────────────────────────────────
  async function loadAll() {
    try {
      const [p, g, c] = await Promise.all([
        props.client.listProfiles(),
        props.client.listGroups(),
        props.client.listCredentials(),
      ])
      setProfiles(p ?? [])
      setGroups(g ?? [])
      setCredentials(c ?? [])
    } catch {
      // Keep current state on error.
    }
  }

  // Initial load on mount.
  onMount(() => {
    void loadAll()
  })

  // ── Derived ─────────────────────────────────────────────────────────────
  const jumpServerProfiles = createMemo(() => profiles().filter((p) => p.options.canBeJumpServer))

  const isNewProfile = createMemo(() => {
    const p = editing()
    return p !== null && (!p.id || !profiles().some((x) => x.id === p.id))
  })

  // ── Actions ─────────────────────────────────────────────────────────────

  function handleProfileClick(p: SSHProfile) {
    setSelectedID(p.id)
    setEditing(null)
    setEditingCredential(null)
  }

  function handleProfileDblClick(p: SSHProfile) {
    props.onConnect?.(p)
  }

  function handleQuickConnect(p: SSHProfile) {
    props.onConnect?.(p)
  }

  function startNewProfile() {
    const profile: SSHProfile = {
      id: '',
      type: 'ssh',
      name: 'New connection',
      options: { host: '', port: 22, user: '', auth: '' },
    }
    setSelectedID('')
    setEditing(profile)
    setEditingCredential(null)
  }

  function showCredentialsPanel() {
    setSelectedID('')
    setEditing(null)
    setEditingCredential({
      id: '',
      name: '',
      username: '',
      auth: '',
    })
  }

  function editCredential(cred: Credential) {
    setSelectedID('')
    setEditing(null)
    setEditingCredential({ ...cred })
  }

  function cancelCredential() {
    setEditingCredential(null)
  }

  async function saveProfile(profile: SSHProfile) {
    if (!profile.id) {
      profile.id = newProfileID('ssh', profile.name)
    }
    try {
      await props.client.createProfile(profile)
      setSelectedID(profile.id)
      setEditing(null)
      await loadAll()
    } catch (err) {
      log.error('Failed to save', { message: (err as Error).message })
    }
  }

  async function deleteProfile(profile: SSHProfile) {
    if (!confirm(`Delete "${profile.name}"?`)) return
    try {
      await props.client.deleteProfile(profile.id)
      setSelectedID('')
      setEditing(null)
      await loadAll()
    } catch {
      // Silent fail
    }
  }

  async function deleteCredential(credential: Credential) {
    if (!confirm(`Delete credential "${credential.name}"?`)) return
    try {
      await props.client.deleteCredential(credential.id)
      setEditingCredential(null)
      await loadAll()
    } catch {
      // Silent fail
    }
  }

  function handleImport() {
    const client = props.client
    const doImport = (text: string) => {
      /* eslint-disable solid/reactivity */
      client
        .importTabby(text)
        .then((count) => {
          log.info('Imported SSH profiles from Tabby config', { count })
          void loadAll()
        })
        .catch((err: unknown) => {
          log.error('Import failed', { message: (err as Error).message })
        })
      /* eslint-enable solid/reactivity */
    }
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = '.yml,.yaml'
    input.addEventListener('change', () => {
      const file = input.files?.[0]
      if (!file) return
      void file.text().then(doImport)
    })
    input.click()
  }
  // ── Render helpers ──────────────────────────────────────────────────────

  function renderCredentialListItem(cred: Credential) {
    const isSelected = editingCredential()?.id === cred.id
    return (
      <div
        classList={{ 'cm-item': true, 'cm-selected': isSelected }}
        onClick={() => editCredential(cred)}
      >
        <div class="cm-item-info">
          <div class="cm-item-name">{cred.name}</div>
          <div class="cm-item-meta">
            {cred.username} &bull; {authModeLabel(cred.auth)}
          </div>
        </div>
      </div>
    )
  }

  function renderGroupSection(node: TreeNode) {
    const groupProfiles = profiles().filter((p) => p.group === node.id)
    return (
      <>
        <div class="cm-group-header">{node.name}</div>
        <For each={groupProfiles}>{(p) => renderListItem(p)}</For>
        <For each={node.children}>{(child) => renderGroupSection(child)}</For>
      </>
    )
  }

  function renderListItem(p: SSHProfile) {
    const isSelected = p.id === selectedID()
    return (
      <div
        classList={{ 'cm-item': true, 'cm-selected': isSelected }}
        onClick={() => handleProfileClick(p)}
        onDblClick={() => handleProfileDblClick(p)}
      >
        <div class="cm-item-info">
          <div class="cm-item-name">{p.name}</div>
          <div class="cm-item-meta">
            {p.options.user || '?'}@{p.options.host}:{p.options.port || 22}
          </div>
        </div>
        <button
          class="cm-quick-connect"
          title="Quick connect"
          onClick={(e) => {
            e.stopPropagation()
            handleQuickConnect(p)
          }}
        >
          SSH
        </button>
      </div>
    )
  }

  function renderEmpty() {
    return (
      <div style={{ color: '#565f89', 'font-size': '13px', padding: '32px' }}>
        Select a connection to edit, or click &quot;+ New connection&quot; to create one.
      </div>
    )
  }

  // ── Profile form ────────────────────────────────────────────────────────

  function renderProfileForm(profile: SSHProfile) {
    const isNew = isNewProfile()

    function setOption(key: keyof SSHProfile['options'], value: unknown) {
      const updated = { ...profile, options: { ...profile.options, [key]: value } }
      setEditing(updated)
    }

    function onNameChange(v: string) {
      const updated = { ...profile, name: v }
      if (!profile.id) updated.id = newProfileID('ssh', v)
      setEditing(updated)
    }

    const credOptions = createMemo((): SelectOption[] =>
      credentials().map((c) => ({
        value: c.id,
        label: `${c.name} (${c.username})`,
      })),
    )

    const jumpOptions = createMemo((): SelectOption[] =>
      jumpServerProfiles().map((p) => ({
        value: p.id,
        label: p.name,
      })),
    )

    return (
      <div class="cm-form">
        <Section title="Basic">
          <TextField label="Name" value={profile.name} onInput={onNameChange} />
          <TextField
            label="Host"
            value={profile.options.host}
            onInput={(v) => setOption('host', v)}
          />
          <TextField
            label="Port"
            value={profile.options.port || 22}
            type="number"
            onInput={(v) => {
              const n = parseInt(v, 10)
              setOption('port', isNaN(n) ? 0 : n)
            }}
          />

          <div class="cm-field">
            <label>Credential ({'\u0423\u0417'})</label>
            <Select
              value={profile.options.credentialId ?? ''}
              onChange={(v) => setOption('credentialId', v || undefined)}
              options={credOptions()}
              placeholder="— None (specify below) —"
            />
          </div>

          <Show when={!profile.options.credentialId}>
            <TextField
              label="User"
              value={profile.options.user || ''}
              onInput={(v) => setOption('user', v)}
            />
          </Show>
        </Section>

        <Show when={!profile.options.credentialId}>
          <div class="cm-form-section">
            <h2>Authentication (override)</h2>
            <div style={{ color: '#565f89', 'font-size': '12px', 'margin-bottom': '12px' }}>
              Tip: Create a Credential above to reuse auth settings across connections.
            </div>
            <div class="cm-field">
              <label>Method</label>
              <div class="cm-radio-group">
                <For each={AUTH_MODES}>
                  {(mode) => (
                    <label>
                      <input
                        type="radio"
                        name="auth-mode"
                        value={mode}
                        checked={(profile.options.auth ?? '') === mode}
                        onChange={() => setOption('auth', mode)}
                      />
                      {authModeLabel(mode)}
                    </label>
                  )}
                </For>
              </div>
            </div>
          </div>
        </Show>

        <Show when={!!profile.options.credentialId}>
          <div class="cm-form-section">
            <div
              style={{
                padding: '12px',
                background: 'rgba(122, 162, 247, 0.1)',
                'border-radius': '6px',
                color: '#c0caf5',
              }}
            >
              <strong>Using Credential: </strong>
              <span>
                {(() => {
                  const cred = credentials().find((c) => c.id === profile.options.credentialId)
                  if (!cred) return 'Unknown'
                  return cred.name
                })()}
              </span>
              <br />
              <small>
                {(() => {
                  const cred = credentials().find((c) => c.id === profile.options.credentialId)
                  if (!cred) return ''
                  return `Username: ${cred.username} | Auth: ${authModeLabel(cred.auth)}`
                })()}
              </small>
            </div>
          </div>
        </Show>

        <div class="cm-form-section">
          <h2>Advanced</h2>
          <TextField
            label="Keepalive interval (ms)"
            value={profile.options.keepaliveInterval || 0}
            type="number"
            onInput={(v) => {
              const n = parseInt(v, 10)
              setOption('keepaliveInterval', isNaN(n) ? 0 : n)
            }}
          />
          <TextField
            label="Keepalive count max"
            value={profile.options.keepaliveCountMax || 0}
            type="number"
            onInput={(v) => {
              const n = parseInt(v, 10)
              setOption('keepaliveCountMax', isNaN(n) ? 0 : n)
            }}
          />
          <TextField
            label="Ready timeout (ms)"
            value={profile.options.readyTimeout || 0}
            type="number"
            onInput={(v) => {
              const n = parseInt(v, 10)
              setOption('readyTimeout', isNaN(n) ? 0 : n)
            }}
          />

          <div class="cm-field">
            <label>Jump server</label>
            <Select
              value={profile.options.jumpHost ?? ''}
              onChange={(v) => setOption('jumpHost', v || undefined)}
              options={jumpOptions()}
              placeholder="— None —"
            />
          </div>

          <Checkbox
            label="Agent forward"
            checked={profile.options.agentForward ?? false}
            onChange={(v) => setOption('agentForward', v)}
          />
          <Checkbox
            label="Can be used as jump server"
            checked={profile.options.canBeJumpServer ?? false}
            onChange={(v) => setOption('canBeJumpServer', v)}
          />
        </div>

        <div class="cm-form-actions">
          <button class="cm-connect" onClick={() => props.onConnect?.(profile)}>
            Connect
          </button>
          <button class="cm-save" onClick={() => void saveProfile(profile)}>
            {isNew ? 'Create' : 'Save'}
          </button>
          <Show when={!isNew}>
            <button class="cm-danger" onClick={() => void deleteProfile(profile)}>
              Delete
            </button>
          </Show>
        </div>
      </div>
    )
  }

  // ── Credential form ────────────────────────────────────────────────────

  function renderCredentialForm(credential: Credential) {
    const isNew = !credential.id

    function updateField(key: keyof Credential, value: string) {
      const updated = { ...credential, [key]: value }
      if (key === 'name' && !credential.id) updated.id = `cred:${value}:${Date.now()}`
      setEditingCredential(updated)
    }

    const [formError, setFormError] = createSignal('')

    async function saveCred() {
      if (!credential.name || !credential.username) {
        setFormError('Name and username are required.')
        return
      }
      setFormError('')
      try {
        // ADR-0013: Send only identity payload, exclude backend-owned fields
        const identityPayload = {
          id: credential.id,
          name: credential.name,
          username: credential.username,
          auth: credential.auth,
          keyPath: credential.keyPath,
        }
        
        // Use updateCredential for existing, createCredential for new
        const saved = isNew
          ? await props.client.createCredential(identityPayload as Credential)
          : await props.client.updateCredential(identityPayload as Credential)
        
        // Use returned ID for password save (server may have assigned different ID)
        const savedId = saved.id || credential.id
        if (credential.auth === 'password' && passwordInputRef?.value) {
          await props.client.savePassword(savedId, passwordInputRef.value)
        }
        setEditingCredential(null)
        await loadAll()
      } catch (err) {
        const message = (err as Error).message
        log.error('Failed to save', { message })
        setFormError(message)
      }
    }

    return (
      <div class="cm-form">
        <Section title={isNew ? 'New Credential (\u0423\u0417)' : 'Edit Credential'}>
          <TextField label="Name" value={credential.name} onInput={(v) => updateField('name', v)} />
          <TextField
            label="Username"
            value={credential.username}
            onInput={(v) => updateField('username', v)}
          />

          <div class="cm-field">
            <label>Authentication Method</label>
            <div class="cm-radio-group">
              <For each={CRED_AUTH_MODES}>
                {(mode) => (
                  <label>
                    <input
                      type="radio"
                      name="cred-auth-mode"
                      value={mode}
                      checked={credential.auth === mode}
                      onChange={() => updateField('auth', mode)}
                    />
                    {authModeLabel(mode)}
                  </label>
                )}
              </For>
            </div>
          </div>

          <Show when={credential.auth === 'password'}>
            <div class="cm-field">
              <label>Password (stored in OS keychain)</label>
              <input
                ref={passwordInputRef}
                type="password"
                placeholder={credential.id ? 'Leave empty to keep current' : 'Enter password'}
              />
            </div>
          </Show>

          <Show when={credential.auth === 'publicKey'}>
            <TextField
              label="Private Key Path"
              value={credential.keyPath || ''}
              onInput={(v) => updateField('keyPath', v)}
            />
          </Show>
        </Section>

        <Show when={formError()}>
          <div class="cm-form-error" role="alert">
            {formError()}
          </div>
        </Show>

        <div class="cm-form-actions">
          <button class="cm-save" onClick={() => void saveCred()}>
            {isNew ? 'Create Credential' : 'Save Credential'}
          </button>
          <Show when={!isNew}>
            <button class="cm-danger" onClick={() => void deleteCredential(credential)}>
              Delete Credential
            </button>
          </Show>
          <button onClick={cancelCredential}>Cancel</button>
        </div>
      </div>
    )
  }

  // ── Form panel ─────────────────────────────────────────────────────────

  const formPanelContent = createMemo(() => {
    const cred = editingCredential()
    if (cred) return renderCredentialForm(cred)

    const ed = editing()
    if (ed) return renderProfileForm(ed)

    const selId = selectedID()
    if (selId) {
      const p = profiles().find((x) => x.id === selId)
      if (p) return renderProfileForm(p)
    }

    return renderEmpty()
  })

  // ── Main render ────────────────────────────────────────────────────────

  const tree = createMemo(() => buildGroupTree(groups()))
  const ungrouped = createMemo(() =>
    profiles().filter((p) => !p.group || !groups().some((g) => g.id === p.group)),
  )
  const hasCredentials = createMemo(() => credentials().length > 0)

  return (
    <>
      <Toolbar>
        <h1>Connections</h1>
        <Button onClick={handleImport} title="Import SSH profiles from a Tabby config.yml">
          Import from Tabby
        </Button>
        <Button onClick={showCredentialsPanel} title="Manage saved passwords (keychain)">
          Saved credentials
        </Button>
        <Button class="cm-primary" onClick={startNewProfile}>
          + New connection
        </Button>
      </Toolbar>
      <div class="cm-body">
        <div class="cm-list">
          <Show when={hasCredentials()}>
            <div class="cm-group-header">Saved Credentials</div>
            <For each={credentials()}>{(cred) => renderCredentialListItem(cred)}</For>
          </Show>

          <Show
            when={profiles().length > 0 || credentials().length > 0}
            fallback={
              <div class="cm-list-empty">
                No connections yet.
                {'\n'}
                Click &quot;+ New connection&quot; to add one.
              </div>
            }
          >
            <For each={tree()}>{(node) => renderGroupSection(node)}</For>
            <Show when={ungrouped().length > 0}>
              <div class="cm-group-header">Connections</div>
              <For each={ungrouped()}>{(p) => renderListItem(p)}</For>
            </Show>
          </Show>
        </div>
        <div class="cm-form-panel">{formPanelContent()}</div>
      </div>
    </>
  )
}

// ── Mounting helper for ConnectionsContent ──────────────────────────────────

/**
 * Render the ConnectionsView Solid component into a target element.
 * Called by ConnectionsContent.mount() to bridge the TabContent seam.
 * Returns a dispose function for cleanup.
 */
export function mountConnectionsView(
  target: HTMLElement,
  client: ProfileClient,
  onConnect?: (profile: SSHProfile) => void,
): () => void {
  return render(() => <ConnectionsView client={client} onConnect={onConnect} />, target)
}
