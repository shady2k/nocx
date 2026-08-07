import './style.css'
import { GetWSPort, GetWSToken, CheckForUpdate, ReportHealthy } from '../wailsjs/go/main/WailsApp'
import { render } from 'solid-js/web'
import { Show, createSignal } from 'solid-js'
import App from './App'
import { log } from './log'
import { WSClient, type SandboxStatus } from './ipc'
import { TabManager } from './tabs'
import { mountSidebar, type SidebarViewDescriptor } from './sidebar'
import { createClipboardAccess, ClipboardGate } from './clipboard'
import { ClipboardBannerImpl } from './banner'
import { ProfileClient } from './profiles'
import { VaultClient } from './vault-client'
import { DialogClient } from './dialog-client'
import { createVaultState, SetupDialog, UnlockDialog } from './vault'
import { ConnectionPasswordPrompt } from './connection-password-prompt'
import type { ConnectionsPasswordRequest } from './generated/connections.passwordRequest'
import { VaultObserver } from './vault-observer'
import { Dispatcher } from './dispatcher'
import { SettingsContent, SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'
import { FootprintClient } from './footprint-client'
import { HorizontalTabStrip, VerticalTabStrip } from './tab-strip'
import { SurfaceRegistry, SURFACE_ID_SETTINGS } from './surface-registry'
import { mountUpdateNotice } from './update-notice'
import { IconButton } from './ui/icon-button'
import { PlugIcon, RefreshIcon, SettingsIcon } from './ui/icons'
import { SettingsObserver } from './settings-observer'
import { bootstrapTheme, reconcileThemeFromGo } from './renderers/theme-bootstrap'
import { bootstrapPlatform } from './platform'
import {
  QuickConnectController,
  ActionsQuickConnectProvider,
  AdHocQuickConnectProvider,
  SSHQuickConnectProvider,
  SSHAliasQuickConnectProvider,
  type DrillCommand,
  type QuickConnectProvider,
} from './quick-connect'
import { PortsPanel, createPortsPanelServices, createPortsPauseControl } from './ports'
import { profileRows } from './quick-connect-assembly'
import { showToast } from './ui/toast'
import { registerFileViewerSurface, openFileViewer } from './file-viewer'
import { createFilesView, FILES_VIEW_ID } from './files/files-view'
import { createFilesPanelServices, type FilesPanelServices } from './files/files-client'
import type { ActiveOrigin } from './tab-content'
import type { TunnelOpenResult } from './generated/tunnel.open'
import { HostKeyDialog } from './host-key-dialog'
import { OpenHostKeyRequestQueue, type OpenHostKeyRequest } from './host-key-controller'

async function main() {
  log.info('nocx: main() called')

  // Single Solid root owns the shell. App renders the skeleton with empty
  // hosts (#tabbar, #activitybar, #sidebar, #panes) that imperative code
  // mounts into. Everything below is the composition root — no more DOM
  // construction, no hand-wired layout.
  // Bootstrap the theme before any render. Applies data-theme, validates
  // terminal tokens, and sets the module-level current theme so every
  // XtermRenderer mount() reads the correct palette from the first frame.
  // ADR-0013 §8, §8.1; design spec §5.4.
  const appliedThemeId = bootstrapTheme()
  // Publishes data-platform for the chrome that differs by host — currently the
  // macOS traffic-light reservation in the tab bar. Not awaited: the attribute
  // only adds a notch, so a frame without it is correct on every platform that
  // does not need one, and blocking first paint on an IPC round trip is not.
  void bootstrapPlatform()
  render(() => <App />, document.getElementById('app')!)
  const bar = document.getElementById('tabbar')!
  const verticalStripHost = document.getElementById('vertical-tabstrip')!
  const panes = document.getElementById('panes')!
  const activityBar = document.getElementById('activitybar')!
  const sidebarPanel = document.getElementById('sidebar')!

  // Update notice — renders inline in the tab bar, right-aligned.
  const notice = mountUpdateNotice(bar)

  const clipboard = createClipboardAccess()
  const gate = new ClipboardGate()
  const banner = new ClipboardBannerImpl()

  // Bound Go method — no startup-event race. Guarded so the renderers still
  // mount without a Wails runtime (plain browser), where GetWSPort throws.
  // In that dev path the backend lives on the page's own host (e.g. a remote
  // dev VM), not necessarily on loopback.
  let port = 9876
  let token = ''
  let host: string | undefined
  try {
    port = await GetWSPort()
    token = await GetWSToken()
  } catch {
    host = location.hostname
    console.warn('nocx: no Wails runtime, using fallback WS port', port)
  }
  const dispatcher = new Dispatcher()
  const client = new WSClient(dispatcher)
  await client.connect(port, host, token)
  const profileClient = new ProfileClient(dispatcher)
  const vaultClient = new VaultClient(dispatcher)
  const dialogClient = new DialogClient(dispatcher)
  const footprintClient = new FootprintClient(dispatcher)
  const vaultObserver = new VaultObserver(dispatcher)
  const vaultController = createVaultState(vaultClient)
  vaultObserver.start(() => {
    void vaultController.refresh()
  })
  void vaultController.refresh()

  // ── Backend-initiated unlock requests ──────────────────────────────
  // The dispatcher's onVaultSealed handles renderer-initiated calls.
  // This subscription handles the OTHER direction: the backend sends a
  // vault.unlockRequest notification when it needs the vault open (e.g.
  // to load the ContentDB key at startup). The same dialog, same code.
  let pendingBackendUnlock: string | null = null
  dispatcher.subscribe('vault.unlockRequest', (params) => {
    const p = params as { requestId: string; reason: string }
    if (!p || !p.requestId) return
    pendingBackendUnlock = p.requestId
    vaultController.openUnlock(p.reason || 'The vault is locked.')
  })

  // ── Backend-initiated connection-password asks ─────────────────────
  // The OTHER backend→renderer ask (same shape as the vault unlock above,
  // different meaning): the auth ladder raised a prompt-password rung and
  // the renderer must supply the connection password, naming which
  // connection and account it is asking about (nocx-s8jn). One ask at a
  // time — the ladder blocks until this is answered.
  const [pendingConnectionPassword, setPendingConnectionPassword] =
    createSignal<ConnectionsPasswordRequest | null>(null)
  dispatcher.subscribe('connections.passwordRequest', (params) => {
    const p = params as ConnectionsPasswordRequest
    if (!p || !p.requestId) return
    setPendingConnectionPassword(p)
  })

  // Open-time host-key decisions share the same consent surface as
  // Connections → Test. Requests are queued because restored tabs can fail
  // concurrently; no tab may steal another tab's decision.
  const [pendingOpenHostKey, setPendingOpenHostKey] = createSignal<OpenHostKeyRequest | null>(null)
  const [openHostKeyBusy, setOpenHostKeyBusy] = createSignal(false)
  const openHostKeys = new OpenHostKeyRequestQueue((request) => setPendingOpenHostKey(request))

  const acceptOpenHostKey = async (request: OpenHostKeyRequest) => {
    setOpenHostKeyBusy(true)
    try {
      await profileClient.trustHostKey(request.evidence.knownHostsHost, request.evidence.key)
      openHostKeys.settleMatchingQueued(request)
      openHostKeys.settle(request, true)
    } catch (err) {
      showToast({
        level: 'danger',
        message: `Could not trust the host key: ${err instanceof Error ? err.message : String(err)}`,
        duration: 0,
      })
    } finally {
      setOpenHostKeyBusy(false)
    }
  }

  // ── Vault activity signal (nocx-eg80) ──────────────────────────────
  // Throttled: at most one call every 3 seconds. Reports user activity
  // (keyboard, mouse, UI actions) so the vault can reset its idle timer.
  // Terminal output, background jobs, and WebSocket messages do NOT fire
  // this — see the e2e test that verifies this distinction.
  let lastActivity = 0
  const ACTIVITY_THROTTLE_MS = 3000
  const reportActivity = () => {
    const now = Date.now()
    if (now - lastActivity < ACTIVITY_THROTTLE_MS) return
    lastActivity = now
    vaultClient.activity().catch(() => {
      // Fire-and-forget: a failed activity call is never actionable.
    })
  }

  document.addEventListener('keydown', reportActivity, true)
  document.addEventListener('mousedown', reportActivity, true)

  // The generated-screen invariant says no setting key appears in the frontend,
  // and it is about the SCREEN: settings.ts and settings-content.ts render from
  // declarations so a new setting costs one MustRegister* call in Go and zero
  // frontend changes. The composition root is a different thing — a CONSUMER
  // that acts on one specific setting — and a consumer has to name what it
  // consumes. So the key is named here, deliberately, and only here.
  //
  // The alternative was tried and rejected: identifying the declaration by
  // section "Interface" plus control "select" reads as key-free but is a latent
  // bug, because it silently resolves to whichever select comes first in
  // declaration order. nocx-8yg.6 (colour schemes) is already filed and would
  // add exactly such a select to Interface, at which point tab placement would
  // stop working with nothing on screen to say why.
  const PLACEMENT_KEY = 'tab.placement'
  const THEME_KEY = 'ui.theme'

  let placement: unknown = 'horizontal'
  try {
    const snap = await profileClient.getSnapshot()
    placement = snap.values[PLACEMENT_KEY] ?? 'horizontal'
    // Reconcile the Go theme setting against the bootstrap cache. Go is
    // authoritative (ADR-0013 §8.1): the bootstrap cache covers the first
    // frame, but the persisted Go value wins on snapshot arrival.
    reconcileThemeFromGo(snap.values[THEME_KEY] as string | undefined, appliedThemeId)
  } catch {
    // Backend may not be ready yet — safe fallback.
  }
  const tabStrip = placement === 'vertical' ? new VerticalTabStrip() : new HorizontalTabStrip()

  const tm = new TabManager(
    bar,
    verticalStripHost,
    panes,
    client,
    clipboard,
    gate,
    banner,
    profileClient,
    tabStrip,
  )
  tm.onVaultSealed = () => vaultController.openUnlock('open this connection')
  tm.onHostKeyError = (evidence, signal) => openHostKeys.request(evidence, signal)
  tm.onSetupVault = () => vaultController.openSetup()
  tm.onCreateSecret = (name) => openSettingsTab().startNewSecret(name)
  tm.onActivity = reportActivity

  // Surface registry — surfaces declared once, every entry point resolves
  // through the registry rather than rebuilding the descriptor. (AD-8)
  const registry = new SurfaceRegistry()
  registry.register(SURFACE_ID_SETTINGS, {
    surfaceType: SURFACE_SETTINGS,
    singletonKey: SINGLETON_SETTINGS,
    factory: () => {
      const content = new SettingsContent(
        profileClient,
        undefined,
        vaultController,
        vaultClient,
        dialogClient,
        footprintClient,
      )
      content.onConnect = (profile) => {
        log.info('nocx: connect from Settings', { profileId: profile.id })
        // Vault preflight: if sealed, ensureBeforeSave shows UnlockDialog
        // and defers newSSHTab until after unseal.
        vaultController.ensureBeforeSave(() => {
          void tm.newSSHTab(
            profile.id,
            profile.options.host,
            profile.options.user,
            profile.options.port,
            profile.name,
          )
          return Promise.resolve()
        }, 'open this connection')
      }
      return content
    },
    descriptor: {
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Settings',
    },
  })

  // Ports (nocx-wzc4.7): a SIDEBAR VIEW, not a tab. The owner's reference
  // (Orca's PORTS panel) sits beside the terminal so a port can be watched
  // while the command that opens it is being typed; a tab replaces the
  // terminal and cannot do that. The view follows the ACTIVE tab: the
  // target accessor below is a Solid signal fed by TabManager's
  // onActiveTabChange, so switching SSH tabs re-scopes the panel, a local
  // tab scopes to the reserved "local" target and shows THIS machine's
  // listeners, and a tab with no ports scope (alias, Settings) shows the
  // no-connection state instead of a stale host's ports (nocx-wzc4.8).
  const portsServices = createPortsPanelServices(dispatcher)
  const [portsTargetId, setPortsTargetId] = createSignal<string | null>(tm.portsTargetId())
  // Why the target is null, when it is null because the pane walked into an
  // environment we cannot enumerate (a hand-typed ssh has no managed
  // connection, so no second exec channel) — the panel says which host
  // rather than showing this machine's listeners under its tab (nocx-695k.3).
  const [portsUnavailable, setPortsUnavailable] = createSignal<string>(tm.portsUnavailableReason())
  // The ACTIVE tab's origin for origin-following surfaces (the Files panel,
  // design §5.4): a signal fed on tab change exactly like portsTargetId —
  // TabManager.activeOrigin() reads a plain field, so the accessor must be
  // this signal, never a direct call.
  const [activeOrigin, setActiveOrigin] = createSignal<ActiveOrigin | null>(tm.activeOrigin())
  tm.onActiveTabChange = () => {
    setPortsTargetId(tm.portsTargetId())
    setPortsUnavailable(tm.portsUnavailableReason())
    setActiveOrigin(tm.activeOrigin())
  }
  // ── Files panel (fm-w10) and its viewer (fm-w7) ──────────────────────
  // The panel's backend surface, wrapped so the composition root owns the
  // binding lifecycle: a binding is live from the files.open that created
  // it until files.close releases it, and the viewer (FileViewerDeps)
  // asks this registry whether a binding may still be read. The wrapper
  // marks dead the moment the store releases the binding, before the wire
  // close resolves — the store issues no further reads either way, so the
  // registry and the store cannot disagree about when it died.
  const filesServices = createFilesPanelServices(dispatcher)
  const filesBindings = new Map<string, Set<(live: boolean) => void>>()
  const liveFilesBindings = new Set<string>()
  const filesServicesTracked: FilesPanelServices = {
    // Spread, then intercept: open and close are the two methods the
    // composition root must watch (the binding-liveness registry), and
    // everything else — list, read, watch, reveal, the change
    // subscription — forwards by CONSTRUCTION. Do not turn this back
    // into an enumeration: an object literal that lists the methods it
    // forwards is a seam where the next method added to FilesClient
    // disappears silently, in production only, while every test stays
    // green because the tests substitute their own services object.
    ...filesServices,
    open: async (sessionId, rootPath) => {
      const res = await filesServices.open(sessionId, rootPath)
      liveFilesBindings.add(res.bindingId)
      return res
    },
    close: async (bindingId) => {
      liveFilesBindings.delete(bindingId)
      const cbs = filesBindings.get(bindingId)
      filesBindings.delete(bindingId)
      if (cbs !== undefined) for (const cb of [...cbs]) cb(false)
      return filesServices.close(bindingId)
    },
  }
  const onFilesBindingLiveness = (bindingId: string, cb: (live: boolean) => void): (() => void) => {
    const cbs = filesBindings.get(bindingId) ?? new Set()
    cbs.add(cb)
    filesBindings.set(bindingId, cbs)
    // Synchronous first call with the current verdict — the viewer's mount
    // decides whether to read at all from it (fm-w7).
    cb(liveFilesBindings.has(bindingId))
    return () => {
      cbs.delete(cb)
      if (cbs.size === 0) filesBindings.delete(bindingId)
    }
  }
  registerFileViewerSurface(registry, tm, {
    readFile: (params) => filesServicesTracked.read(params.bindingId, params.path, 0),
    onBindingLiveness: onFilesBindingLiveness,
  })
  const filesView = createFilesView({
    services: filesServicesTracked,
    opener: { open: openFileViewer },
    clipboard,
    activeOrigin,
  })
  /**
   * Open (or focus) the Settings tab and hand back the instance that is
   * actually on screen.
   *
   * `openTab` deduplicates on the singleton key, so when Settings is already
   * open the content just built is discarded and the live instance is the
   * existing tab's. Talking to the one we built would have addressed a surface
   * nobody can see — silently, and only on the second invocation.
   */
  function openSettingsTab(): SettingsContent {
    const { content, descriptor } = registry.build(SURFACE_ID_SETTINGS)
    const live = tm.openTab(content, descriptor).content
    if (!(live instanceof SettingsContent)) {
      throw new Error('nocx: the Settings singleton is not a SettingsContent')
    }
    return live
  }

  // Live application through SettingsObserver: when any setting
  // changes, refetch the snapshot and act on relevant keys.
  const observer = new SettingsObserver(dispatcher)
  observer.setRevision(0)
  observer.start(() => {
    void (async () => {
      try {
        const snap = await profileClient.getSnapshot()
        observer.setRevision(snap.revision)
        const next = snap.values[PLACEMENT_KEY] ?? 'horizontal'
        if (next !== placement) {
          placement = next
          const newStrip = next === 'vertical' ? new VerticalTabStrip() : new HorizontalTabStrip()
          wireQuickConnect(newStrip)
          tm.replaceStrip(newStrip)
        }
        // Theme setting changed — reconcile against Go's value (ADR-0013 §8.1).
        reconcileThemeFromGo(snap.values[THEME_KEY] as string | undefined)
      } catch {
        // Silently ignore — a settings fetch failure is not actionable here.
      }
    })()
  })
  // App-shell sidebar (nocx-82l9.6) — VS Code-style activity bar plus a
  // collapsible panel.  Views and actions are two separate zones:
  //
  // - Top zone: views (Ports is the first real one, nocx-wzc4.7; Explorer,
  //   Git, and Servers are future beads).
  // - Bottom zone: global actions (currently only the Settings gear).
  //
  // Connections has been removed from the activity bar — it is not a view
  // and not an action (see .internal/specs §2.4).  It is now a Settings
  // sub-page reachable from the Settings rail.
  // Pause is a HEADER action, not body chrome (nocx-wzc4.9): one shared
  // controller feeds both the header toggle and the panel's status merges,
  // so the two can never disagree about the backend's flag.
  const portsPause = createPortsPauseControl()
  const PORTS_VIEW: SidebarViewDescriptor = {
    id: 'ports',
    title: 'Ports',
    icon: PlugIcon,
    // Refresh, not Pause (nocx-wzc4.11). One sample costs ~12ms, so there is
    // nothing to protect a host from; what a user actually wants is to ask
    // again after starting something.
    actions: () => (
      <IconButton
        data-testid="ports-refresh"
        size="sm"
        ariaLabel="Refresh ports"
        title="Refresh ports"
        disabled={portsTargetId() === null}
        onClick={() => void portsServices.sample(portsTargetId() as string)}
      >
        <RefreshIcon />
      </IconButton>
    ),
    // The view receives the shell's view props: visible gates sampling,
    // activeProfileId re-scopes the panel to the tab in front.
    view: (props) => (
      <PortsPanel
        profileId={props.activeProfileId}
        unavailableIn={portsUnavailable}
        services={portsServices}
        visible={props.visible}
        pause={portsPause}
      />
    ),
    order: 0,
  }
  // The sidebar renders array order, so the views reach mountSidebar sorted
  // by their order field — the field then means what it says, and a future
  // view cannot slip in front by registration order. Files registers below
  // Ports (FILES_VIEW_ORDER < 0) and must be the FIRST icon in the view
  // zone — an owner requirement, asserted here and in files-view.test.tsx.
  const sidebarViews = [filesView, PORTS_VIEW].sort((a, b) => a.order - b.order)
  if (sidebarViews[0]?.id !== FILES_VIEW_ID) {
    throw new Error('nocx: Files must be the first activity-bar view')
  }
  const sidebar = mountSidebar(
    activityBar,
    sidebarPanel,
    sidebarViews,
    /* actions */ [
      {
        id: 'settings',
        title: 'Settings',
        icon: SettingsIcon,
        onActivate: () => {
          log.info('nocx: opening Settings tab')
          openSettingsTab()
        },
      },
    ],
    undefined,
    /* eslint-disable solid/reactivity -- mountSidebar consumes these
       accessors reactively (SidebarViewProps.activeProfileId and
       .activeOrigin, fed with the ports target and the Files origin); the
       reads happen inside the views' tracked scopes, and the gate cannot
       see across the function boundary. */
    () => portsTargetId(),
    () => activeOrigin(),
  )

  // Cmd/Ctrl+, opens or focuses the Settings tab.
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key === ',') {
      e.preventDefault()
      openSettingsTab()
    }
  })

  // ── Quick-connect picker (nocx-imkb.7) ──────────────────────────────
  // Both the initial tab strip AND replacement strips (via replaceStrip)
  // need onQuickConnect wired — the helper ensures no strip is missed.

  const qcContainer = document.createElement('div')
  document.body.append(qcContainer)

  const sshProvider = new SSHQuickConnectProvider(profileClient, (id, host, user) =>
    tm.newSSHTab(id, host, user),
  )
  // "Forward a port" (nocx-4t37): a command that needs a target drills in
  // INSIDE the palette — server, then port — instead of dead-ending or
  // opening a second dialog. The forward is the same tunnel.open the ports
  // panel drives; only the owner label differs (no owning tab, so the scope
  // names the palette).
  const openForward = async (
    profileId: string,
    destination: string,
    port: number,
  ): Promise<void> => {
    const call = <T,>(method: string, params: unknown): Promise<T> =>
      dispatcher.call<T>(method, params)
    const announce = (rec: TunnelOpenResult): void => {
      showToast({
        level: 'success',
        message: `Forwarding ${rec.destination} on ${rec.actualBind.host}:${rec.actualBind.port}`,
      })
    }
    const open = (bindPort: number): Promise<TunnelOpenResult> =>
      call<TunnelOpenResult>('tunnel.open', {
        profileId,
        port: bindPort,
        destination,
        scope: `palette:${profileId}`,
      })
    try {
      announce(await open(port))
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      if (!/address already in use|EADDRINUSE/i.test(msg)) {
        showToast({ level: 'danger', message: msg })
        return
      }
      try {
        announce(await open(0))
      } catch (e2) {
        showToast({ level: 'danger', message: e2 instanceof Error ? e2.message : String(e2) })
      }
    }
  }

  const forwardPortCommand: DrillCommand = {
    id: '__forward_port__',
    label: 'Forward a port',
    detail: 'Expose one of the server\u2019s ports on this machine',
    steps: [
      {
        name: 'server',
        fetch: async () => {
          // Saved profiles only: a forward is profile-owned (tunnel.open
          // resolves the profile's config and dials its own connection), so
          // an alias, which has no profile, cannot be a forward target.
          const profiles = await profileClient.listProfiles()
          return profileRows(profiles).map((p) => ({
            id: p.id,
            label: p.label,
            detail: p.detail,
          }))
        },
      },
      {
        name: 'port',
        fetch: async (selections) => {
          const server = selections[0]
          const st = await portsServices.sample(server.item.id)
          if (st.discovery.state === 'available' || st.discovery.state === 'available-limited') {
            if (st.discovery.listeners.length === 0) {
              return [
                {
                  id: '__no_listeners__',
                  label: 'No listening ports',
                  detail: 'This server has nothing listening right now',
                  system: true,
                },
              ]
            }
            return st.discovery.listeners.map((l) => {
              const wildcard = l.address === '0.0.0.0' || l.address === '::' || l.address === ''
              const destination =
                wildcard && st.host !== '' ? `${st.host}:${l.port}` : `${l.address}:${l.port}`
              const process =
                l.process.evidence === 'known' ? ` — ${l.process.name} (pid ${l.process.pid})` : ''
              return {
                id: `__fwd_port:${l.port}`,
                label: `:${l.port}`,
                detail: `${destination}${process}`,
                value: destination,
              }
            })
          }
          // Typed condition — never an empty list: a degraded source and an
          // empty source are different facts.
          return [
            {
              id: `__ports_${st.discovery.state}__`,
              label: `Ports: ${st.discovery.state}`,
              detail:
                st.discovery.classification ||
                st.discovery.stderr ||
                'No sample yet — open this server in a tab, then try again',
              system: true,
            },
          ]
        },
      },
    ],
    run: (selections) => {
      const server = selections[0]
      const portChoice = selections[1]
      const destination = portChoice.item.value ?? ''
      const localPort = Number(portChoice.item.label.replace(/^:/, '')) || 0
      void openForward(server.item.id, destination, localPort)
    },
  }

  const qcProviders: QuickConnectProvider[] = [
    new ActionsQuickConnectProvider(
      () => tm.newTab(),
      () => openSettingsTab().startNewConnection(),
      // "Integrate this shell" (nocx-ynsx): route to the ACTIVE tab's
      // terminal content — the shell at the current prompt. The content
      // itself owns the PROMPT_READY && trusted && owned gate and refuses
      // with a stated reason outside it.
      () => void tm.activeTerminalContent()?.integrateShell(),
      forwardPortCommand,
      // Sandboxed shell… action (ADR-0019 §3.2): live flag + backend status
      // on every open; picker → new sandboxed tab. Cancellation is a no-op.
      {
        state: async () => {
          const snap = await profileClient.getSnapshot()
          const enabled = snap.values['sandbox.enabled'] === true
          let status: SandboxStatus | null = null
          if (enabled) {
            try {
              status = await client.sandboxStatus()
            } catch {
              status = null
            }
          }
          return { enabled, status }
        },
        open: () => {
          void (async () => {
            const picked = await dialogClient.openDirectoryDialog()
            if (!picked.path) return // cancelled: no-op
            tm.newSandboxedTab(picked.path)
          })().catch((err: unknown) => {
            const message = err instanceof Error ? err.message : String(err)
            showToast({
              level: 'danger',
              message: `Could not open the directory picker: ${message}`,
            })
          })
        },
      },
    ),
    sshProvider,
    new SSHAliasQuickConnectProvider(profileClient, (host, user, port) =>
      tm.newSSHTab('', host, user, port),
    ),
    // Free-form fallback: "Connect to <host>" when the typed query matches
    // neither a saved profile nor an alias. Same host path as aliases — the
    // dialog only reaches it after every real match missed.
    new AdHocQuickConnectProvider((host, user, port) => tm.newSSHTab('', host, user, port)),
  ]

  const qc = new QuickConnectController()
  qc.mount(qcContainer, qcProviders)

  function wireQuickConnect(strip: typeof tabStrip) {
    strip.onQuickConnect = () => qc.show()
  }
  wireQuickConnect(tabStrip)

  // Cmd/Ctrl+Shift+P opens the PALETTE (nocx-4t37): commands and hosts
  // mixed, each row typed on the right; target-needing commands drill in.
  // Chosen to match the command-palette convention. The tab-strip caret
  // (wireQuickConnect above) stays the plain server list. Does not collide
  // with TabManager (Ctrl+T/W/1-9), the terminal (single keystrokes), or
  // CodeMirror (which does not register this binding in its keymap).
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key === 'P') {
      e.preventDefault()
      qc.showPalette()
    }
  })

  // Ctrl/Cmd+Shift+I — "Integrate this shell" (nocx-ynsx). The same entry
  // the quick-connect palette lists, reachable without opening the picker.
  // The gate (PROMPT_READY && trusted && owned) lives in
  // TerminalContent.integrateShell and refuses with a stated reason outside
  // it. Intercepted only while the ACTIVE tab is a terminal, so the chord
  // stays free elsewhere (it collides with WebKit's devtools shortcut, and
  // in a release build there is no inspector to open; the tradeoff is
  // named here deliberately).
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key === 'I') {
      if (tm.activeTerminalContent() === null) return
      e.preventDefault()
      tm.activeTerminalContent()?.integrateShell()
    }
  })

  // Ctrl/Cmd+Shift+O — reveal-or-focus the Ports sidebar view (nocx-wzc4.7).
  // Ports is no longer a tab or a palette item — it is a surface you keep
  // open beside the terminal — so the chord brings the view to the front
  // (or focuses it when it is already there) instead of opening another
  // anything.
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key.toLowerCase() === 'o') {
      e.preventDefault()
      sidebar.revealView('ports')
    }
  })

  void tm.openInitialTab()

  // --- Auto-update: check on start, then every 24 h ---

  // Report healthy once the initial tab's renderer mounted and PTY opened.
  tm.initialTabReady.then(
    () => {
      ReportHealthy().catch((err) => console.warn('nocx: ReportHealthy failed', err))
    },
    () => {
      console.warn('nocx: initial tab failed — not reporting healthy')
    },
  )

  // Check for updates. Failures are silent (airplane mode, DNS hiccup, etc.).
  try {
    const info = await CheckForUpdate()
    if (info) {
      notice.showAvailable(info.Version, info.NotesURL)
    }
  } catch {
    // Silent — automatic check failures are not surfaced to the user.
  }

  // Re-check every 24 hours.
  const DAY_MS = 24 * 60 * 60 * 1000
  setInterval(() => {
    void (async () => {
      try {
        const info = await CheckForUpdate()
        if (info) {
          notice.showAvailable(info.Version, info.NotesURL)
        }
      } catch {
        // Silent.
      }
    })()
  }, DAY_MS)

  // ── Vault dialogs ────────────────────────────────────────────────────
  // Mounted at the app level so they float over every page, not just Settings.
  const vaultRoot = document.createElement('div')
  document.body.append(vaultRoot)
  render(
    () => (
      <>
        <Show when={vaultController.showSetup()}>
          <SetupDialog
            open={vaultController.showSetup()}
            onClose={() => vaultController.closeSetup()}
            onSetupComplete={() => vaultController.onSetupDone()}
            vaultClient={vaultClient}
          />
        </Show>
        <Show when={vaultController.showUnlock()}>
          <UnlockDialog
            open={vaultController.showUnlock()}
            onClose={() => {
              // Report cancellation for backend-initiated requests.
              if (pendingBackendUnlock) {
                vaultClient
                  .unlockResolved({ requestId: pendingBackendUnlock, outcome: 'cancelled' })
                  .catch(() => {})
                pendingBackendUnlock = null
              }
              vaultController.closeUnlock()
            }}
            onUnsealed={() => {
              // Report success for backend-initiated requests.
              if (pendingBackendUnlock) {
                vaultClient
                  .unlockResolved({ requestId: pendingBackendUnlock, outcome: 'unsealed' })
                  .catch(() => {})
                pendingBackendUnlock = null
              }
              vaultController.onUnsealDone()
            }}
            vaultClient={vaultClient}
            vaultStatus={vaultController.status()}
            reason={vaultController.unlockReason()}
          />
        </Show>
        <Show when={pendingConnectionPassword()}>
          {(ask) => (
            <ConnectionPasswordPrompt
              open
              ask={ask()}
              client={profileClient}
              onDone={() => {
                setPendingConnectionPassword(null)
              }}
            />
          )}
        </Show>
        <Show when={pendingOpenHostKey()} keyed>
          {(request) => (
            <HostKeyDialog
              evidence={request.evidence}
              busy={openHostKeyBusy()}
              onAccept={() => void acceptOpenHostKey(request)}
              onClose={() => openHostKeys.settle(request, false)}
            />
          )}
        </Show>
      </>
    ),
    vaultRoot,
  )
}

main().catch((err) => log.error('nocx: main error', { message: (err as Error).message }))
