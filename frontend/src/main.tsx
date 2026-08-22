import './style.css'
import {
  GetWSPort,
  GetWSToken,
  CheckForUpdate,
  ReportHealthy,
} from '../bindings/github.com/shady2k/nocx/wailsapp'
import { render } from 'solid-js/web'
import { Show, createSignal } from 'solid-js'
import App from './App'
import { log } from './log'
import { installBrowserTransport } from './wails-runtime'
import { WSClient, type SandboxStatus } from './ipc'
import { openSandboxedShell } from './sandbox-open'
import { shieldState } from './sandbox-shield'
import { showSandboxPermissions } from './sandbox-permissions-dialog'
import { LayoutStore } from './layout/layout-store'
import { LayoutClient } from './layout/layout-client'
import { PaneManager } from './panes'
import { mountSidebar, type SidebarViewDescriptor } from './sidebar'
import { AboutClient } from './about-client'
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
import { HistoryStatusStore } from './history-status'
import { FootprintClient } from './footprint-client'
import { EndpointClient } from './endpoints'
import { AgentClient } from './agent'
import { HorizontalTabStrip, VerticalTabStrip } from './tab-strip'
import { SurfaceRegistry, SURFACE_ID_SETTINGS } from './surface-registry'
import { mountUpdateNotice } from './update-notice'
import { mountConnectionNotice } from './connection-notice'
import { IconButton } from './ui/icon-button'
import { PlugIcon, RefreshIcon, SettingsIcon, ShieldIcon, TextQuoteIcon } from './ui/icons'
import { SettingsObserver } from './settings-observer'
import { bootstrapTheme, reconcileThemeFromGo } from './renderers/theme-bootstrap'
import { bootstrapPlatform } from './platform'
import {
  QuickConnectController,
  ActionsQuickConnectProvider,
  AdHocQuickConnectProvider,
  SSHQuickConnectProvider,
  SSHAliasQuickConnectProvider,
  SecretsQuickConnectProvider,
  type DrillCommand,
  type QuickConnectProvider,
} from './quick-connect'
import { PortsPanel, createPortsPanelServices, createPortsPauseControl } from './ports'
import { profileRows } from './quick-connect-assembly'
import { showToast } from './ui/toast'
import { registerFileViewerSurface, openFileViewer } from './file-viewer'
import { createFilesView, FILES_VIEW_ID } from './files/files-view'
import { createFilesPanelServices, type FilesPanelServices } from './files/files-client'
import { createGitView } from './git/git-view'
import { createGitPanelServices, type GitPanelServices } from './git/git-client'
import { createGitStore } from './git/git-store'
import { registerGitDiffSurface } from './git/git-diff/open-git-diff'
import type { ActiveOrigin, SurfaceType } from './pane-content'
import {
  clampSidebarWidth,
  createSidebarWidthController,
  persistSidebarWidth,
} from './sidebar-width'
import { UIStateClient } from './uistate-client'
import { OUTPUT_WRAP_DEFAULT, OUTPUT_WRAP_KEY, applyOutputWrap } from './output-wrap'
import { applyOutputCap, OUTPUT_CAP_KEY } from './output-cap'
import { applyRestoreOnStartup, RESTORE_ON_STARTUP_KEY } from './restore-setting'
import type { TunnelOpenResult } from './generated/tunnel.open'
import { HostKeyDialog } from './host-key-dialog'
import { OpenHostKeyRequestQueue, type OpenHostKeyRequest } from './host-key-controller'
import { SnippetsClient } from './snippets/snippets-client'
import { SnippetsStore, type Snippet } from './snippets/snippets-store'
import { createSessionFactsProvider } from './snippets/session-facts'
import { createSnippetFireAdapter } from './snippets/fire'
import { SnippetsQuickConnectProvider } from './snippets/snippets-quick-connect'
import { mountSnippetAskDialog } from './snippets/snippet-ask-dialog'
import { NotesClient } from './notes/notes-client'
import { NotesStore } from './notes/notes-store'
import { NotesPanel } from './notes/notes-panel'
import { registerNotesSurface, openNote, createAndOpenNote } from './notes'
import { isNoteChord } from './notes/chord'
import { createOverviewController } from './overview/overview-controller'
import { askFields } from './snippets/resolve'

async function main() {
  // The browser transport must be installed before any binding call: in the
  // dev-web and headless-e2e browsers the window.go shim is the only thing
  // that can answer GetWSPort/GetWSToken. A no-op in the packaged webview.
  installBrowserTransport()

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
  const aboutClient = new AboutClient(dispatcher)
  const client = new WSClient(dispatcher)
  // Connection notice — the transport's condition, stated where a person is
  // already looking (the tab bar). A dropped connection is a persistent
  // condition: not a toast that fades if it has not come back. This is the
  // ONE subscriber to the dispatcher's disconnect lifecycle (nocx-gbhwh);
  // the return value is for tests, the wiring here ignores it.
  mountConnectionNotice(bar, dispatcher, client)
  await client.connect(port, host, token)
  const profileClient = new ProfileClient(dispatcher)
  const vaultClient = new VaultClient(dispatcher)
  const dialogClient = new DialogClient(dispatcher)
  const footprintClient = new FootprintClient(dispatcher)
  const endpointsClient = new EndpointClient(dispatcher)
  const agentClient = new AgentClient(dispatcher)
  // The UI-state document (ADR-0033): what the app remembers without being
  // asked — the sidebar's collapse, its view, its width. Not the settings
  // registry, which holds what a user deliberately chose, and not
  // localStorage, which may not carry facts.
  const uiStateClient = new UIStateClient(dispatcher)
  // The snippet library: ONE store, read by the palette, the settings page
  // and every later surface (design §6 — no change notification on the
  // wire, a writer re-reads). Constructed with the other clients because
  // the Settings tab's factory below closes over it.
  const snippetsStore = new SnippetsStore(new SnippetsClient(dispatcher))
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
  const SANDBOX_ENABLED_KEY = 'sandbox.enabled'

  // The declared default, painted BEFORE the snapshot arrives: the first
  // frame must not show the opposite of what the backend is about to say.
  applyOutputWrap(OUTPUT_WRAP_DEFAULT)

  let placement: unknown = 'horizontal'
  const [sandboxEnabledLive, setSandboxEnabledLive] = createSignal(false)
  const [sandboxStatusAvailable, setSandboxStatusAvailable] = createSignal<boolean | null>(null)
  const refreshSandboxAvailability = async (enabled: boolean): Promise<void> => {
    if (!enabled) {
      setSandboxStatusAvailable(null)
      return
    }
    const sandboxClient = client
    if (!sandboxClient) {
      setSandboxStatusAvailable(null)
      return
    }
    try {
      setSandboxStatusAvailable((await sandboxClient.sandboxStatus())?.available ?? null)
    } catch {
      setSandboxStatusAvailable(null)
    }
  }
  // The sidebar's remembered state, from the UI-state document rather than
  // the settings snapshot below — a drag is not a decision (ADR-0033). A
  // failure falls back to the declared defaults, which is also what the CSS
  // paints before this bootstrap runs (style.css #sidebar); load() never
  // throws for exactly that reason.
  const uiState = await uiStateClient.load()
  const sidebarWidth = clampSidebarWidth(uiState.sidebar.width)
  try {
    const snap = await profileClient.getSnapshot()
    placement = snap.values[PLACEMENT_KEY] ?? 'horizontal'
    const enabled = snap.values[SANDBOX_ENABLED_KEY] === true
    setSandboxEnabledLive(enabled)
    void refreshSandboxAvailability(enabled)
    // Reconcile the Go theme setting against the bootstrap cache. Go is
    // authoritative (ADR-0013 §8.1): the bootstrap cache covers the first
    // frame, but the persisted Go value wins on snapshot arrival.
    reconcileThemeFromGo(snap.values[THEME_KEY] as string | undefined, appliedThemeId)
    // The default wrap for a command block's output — one attribute on the
    // root, read by the CSS; the per-block ⋮ override is not touched by it.
    applyOutputWrap(snap.values[OUTPUT_WRAP_KEY])
    // How much of one command's output is kept. Applied here beside the wrap
    // for the same reason: the renderer is where it is enforced, so it has to
    // know before the first block freezes.
    applyOutputCap(snap.values[OUTPUT_CAP_KEY])
    // Whether boot reopens what was left. Read HERE, before the pane manager
    // exists, because it decides what the first frame contains — and read
    // once: flipping it mid-session must not make tabs appear or vanish
    // under the person.
    applyRestoreOnStartup(snap.values[RESTORE_ON_STARTUP_KEY])
  } catch {
    // Backend may not be ready yet — safe fallback.
  }
  const tabStrip = placement === 'vertical' ? new VerticalTabStrip() : new HorizontalTabStrip()

  // The layout chain, which the backend owns and the renderer renders
  // (nocx-isoph.4). Constructed here, beside every other wire client, and
  // read by PaneManager on boot — before it opens anything, so a reload finds
  // the tabs it left rather than deciding what the window looks like and
  // asking afterwards.
  const layout = new LayoutStore(new LayoutClient(dispatcher))

  const tm = new PaneManager(
    bar,
    verticalStripHost,
    panes,
    client,
    clipboard,
    gate,
    banner,
    profileClient,
    tabStrip,
    layout,
    // Which tab was in front: the UI-state document holds it, and the mirror
    // is already warm — `load()` above ran before this line (ADR-0033).
    uiStateClient,
  )
  tm.onVaultSealed = () => vaultController.openUnlock('open this connection')
  tm.onHostKeyError = (evidence, signal) => openHostKeys.request(evidence, signal)
  tm.onSetupVault = () => vaultController.openSetup()
  tm.onCreateSecret = (name) => openSettingsPane().startNewSecret(name)
  // A question refused for want of an endpoint: the toast names the
  // problem, this opens where it is fixed — Settings → Endpoints with the
  // editor already up on a blank one.
  tm.onCreateEndpoint = () => openSettingsPane().startNewEndpoint()
  tm.onActivity = reportActivity

  // ── The workspace overview (nocx-edhcu) ──────────────────────────────
  // Every workspace and every pane at once, as text cards. The controller
  // installs its own ⌥⌘O listener on the document — like the other two ⌥⌘
  // surfaces, because which pane you are in is not the question it answers —
  // and the strip's head button opens the same one rather than a second.
  const overview = createOverviewController(tm.overviewPort())
  tm.onOpenOverview = () => overview.open()

  const observer = new SettingsObserver(dispatcher)
  // Durable-history availability (nocx-rtg0.15). Started here rather than
  // inside Settings so the status is already known when the tab opens, and
  // so a degrade raised while the app runs (nocx-rtg0.10 will raise through
  // the same surface) reaches a screen that is already on the section.
  const historyStatusStore = new HistoryStatusStore(client)
  historyStatusStore.start()

  // Surface registry — surfaces declared once, every entry point resolves
  // through the registry rather than rebuilding the descriptor. (AD-8)
  const registry = new SurfaceRegistry()
  registry.register(SURFACE_ID_SETTINGS, {
    surfaceType: SURFACE_SETTINGS,
    singletonKey: SINGLETON_SETTINGS,
    factory: () => {
      const content = new SettingsContent(
        profileClient,
        observer,
        vaultController,
        vaultClient,
        dialogClient,
        footprintClient,
        endpointsClient,
        agentClient,
        snippetsStore,
        historyStatusStore,
        client,
        aboutClient,
        clipboard,
      )
      content.onConnect = (profile) => {
        log.info('nocx: connect from Settings', { profileId: profile.id })
        // Vault preflight: if sealed, ensureBeforeSave shows UnlockDialog
        // and defers newSSHPane until after unseal.
        vaultController.ensureBeforeSave(() => {
          void tm.newSSHPane(
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
  // target accessor below is a Solid signal fed by PaneManager's
  // onActivePaneChange, so switching SSH tabs re-scopes the panel, a local
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
  // PaneManager.activeOrigin() reads a plain field, so the accessor must be
  // this signal, never a direct call.
  const [activeOrigin, setActiveOrigin] = createSignal<ActiveOrigin | null>(tm.activeOrigin())
  const [activeSandboxed, setActiveSandboxed] = createSignal(tm.activePaneSandboxed())
  const [activeSurfaceType, setActiveSurfaceType] = createSignal<SurfaceType | null>(
    tm.activeSurfaceType(),
  )
  tm.onActivePaneChange = () => {
    setActiveSurfaceType(tm.activeSurfaceType())
    setPortsTargetId(tm.portsTargetId())
    setPortsUnavailable(tm.portsUnavailableReason())
    setActiveOrigin(tm.activeOrigin())
    setActiveSandboxed(tm.activePaneSandboxed())
  }
  let convertActiveTabToSandboxed: () => Promise<void> = async () => {}
  const currentShieldState = () =>
    shieldState({
      enabled: sandboxEnabledLive(),
      statusAvailable: sandboxStatusAvailable(),
      origin: activeOrigin(),
      sandboxed: activeSandboxed(),
    })
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

  // ── Git panel (design §5.4) and its diff surface (worker G) ───────────
  // The panel's backend surface, wrapped so the composition root owns the
  // binding-liveness registry the diff surface reads: a binding is live
  // from the git.open that created it until git.close releases it, and a
  // session close is marked dead by the git.changed notification — no
  // close crosses the seam on that path, so the notification is the only
  // writer (worker G's GitDiffDeps.onBindingLiveness).
  const gitServices = createGitPanelServices(dispatcher)
  const gitBindings = new Map<string, Set<(live: boolean) => void>>()
  const liveGitBindings = new Set<string>()
  const gitServicesTracked: GitPanelServices = {
    // Spread, then intercept: open and close are the two methods the
    // composition root must watch (the binding-liveness registry), and
    // everything else forwards by CONSTRUCTION — the same rule as the
    // files wrapper above.
    ...gitServices,
    open: async (sessionId, cwd) => {
      const res = await gitServices.open(sessionId, cwd)
      if (res.state === 'ok' && res.bindingId !== undefined) {
        liveGitBindings.add(res.bindingId)
      }
      return res
    },
    close: async (bindingId) => {
      liveGitBindings.delete(bindingId)
      const cbs = gitBindings.get(bindingId)
      gitBindings.delete(bindingId)
      if (cbs !== undefined) for (const cb of [...cbs]) cb(false)
      return gitServices.close(bindingId)
    },
  }
  const onGitBindingLiveness = (bindingId: string, cb: (live: boolean) => void): (() => void) => {
    const cbs = gitBindings.get(bindingId) ?? new Set()
    cbs.add(cb)
    gitBindings.set(bindingId, cbs)
    // Synchronous first call with the current verdict — the diff content's
    // mount decides whether to read at all from it.
    cb(liveGitBindings.has(bindingId))
    return () => {
      cbs.delete(cb)
      if (cbs.size === 0) gitBindings.delete(bindingId)
    }
  }
  gitServices.subscribeGitChanged((p) => {
    liveGitBindings.delete(p.bindingId)
    const cbs = gitBindings.get(p.bindingId)
    gitBindings.delete(p.bindingId)
    if (cbs !== undefined) for (const cb of [...cbs]) cb(false)
  })
  // The store is the panel's and the diff surface's shared state: the
  // surface's Reload offer (onDiffStale) is fed by the store's poll.
  const gitStore = createGitStore(gitServicesTracked)
  registerGitDiffSurface(registry, tm, {
    diff: (params) => gitServices.diff(params.bindingId, params.path, params.side, params.maxBytes),
    onBindingLiveness: onGitBindingLiveness,
    onDiffStale: (bindingId, path, side, cb) => gitStore.onDiffStale(bindingId, path, side, cb),
  })
  const gitView = createGitView({
    services: gitServicesTracked,
    store: gitStore,
    activeOrigin,
  })

  // ── Snippets: the palette (design §10.1) ─────────────────────────────
  // The palette is the keyboard surface that answers when there is no
  // command editor; the fire adapter is where the design's fire-time rules
  // live. Both read the one store built above.
  // The pane's facts are read AT FIRE TIME, never when the palette opened
  // (design §8, bead nocx-jj77): the provider reads the ACTIVE pane on
  // every call, so a tab switch between choosing a snippet and confirming
  // targets the pane in front (design §9.5).
  const snippetFacts = createSessionFactsProvider(
    {
      paneFacts: () => {
        const content = tm.activeTerminalContent()
        if (content === null) return null
        const env = content.snippetEnv()
        if (env === null) return null
        return {
          cwd: env.cwd,
          host: env.host,
          user: env.user,
          // The git panel's live binding for the active origin — the only
          // owner of a binding; the provider never invents an id to ask
          // with (session-facts.ts).
          gitBindingId: gitStore.binding()?.bindingId ?? null,
        }
      },
    },
    { status: (bindingId) => gitServices.status(bindingId) },
  )
  const snippetFire = createSnippetFireAdapter({
    facts: () => snippetFacts.facts(),
    activeInsert: () => {
      const content = tm.activeTerminalContent()
      return content === null ? null : { insertSnippet: (t) => content.insertSnippet(t) }
    },
    clipboard: { writeText: (text) => clipboard.writeText(text) },
  })
  // Where the keyboard goes after a snippet lands: back to the pane it was
  // fired into. TerminalContent decides which half owns it — the command
  // editor when it is visible, the grid otherwise — so this asks the pane
  // rather than choosing (AD-8).
  function returnKeyboardToThePane(): void {
    tm.activeTerminalContent()?.focus()
  }

  // Snippets ride the quick-connect palette — the surface the server list,
  // the command palette and the secret picker already are. The provider is
  // registered with the others below; this reference is what the chord, the
  // strip's menu and the completion dropdown all fire through, so there is
  // ONE accept path (AD-8).
  const snippetsProvider = new SnippetsQuickConnectProvider({
    store: snippetsStore,
    fire: snippetFire,
    // A refusal re-opens the list with the reason on it: the surface it is
    // about is still there, and a toast would take the explanation away
    // from it (design §11).
    onRefused: (message) => qc.showSnippets(message),
    onManage: () => openSettingsPane().openPage('snippets'),
    // A body with {{ask:…}} fields: the palette closes and the form asks
    // for all of them at once (owner review — a step that filters a list
    // cannot also be where a value is typed).
    onAsk: (snippet) => snippetAsk.ask(snippet),
    onDelivered: returnKeyboardToThePane,
  })
  // The form the fields are answered in. It reports its own refusals, so a
  // person who mistyped an answer sees why beside what they typed.
  const snippetAsk = mountSnippetAskDialog(document.body, {
    fire: (snippet, answers) => snippetsProvider.fireReporting(snippet, answers),
    onDelivered: returnKeyboardToThePane,
  })
  /**
   * Open (or focus) the Settings tab and hand back the instance that is
   * actually on screen.
   *
   * `openPane` deduplicates on the singleton key, so when Settings is already
   * open the content just built is discarded and the live instance is the
   * existing tab's. Talking to the one we built would have addressed a surface
   * nobody can see — silently, and only on the second invocation.
   */
  function openSettingsPane(): SettingsContent {
    const { content, descriptor } = registry.build(SURFACE_ID_SETTINGS)
    const live = tm.openPane(content, descriptor).content
    if (!(live instanceof SettingsContent)) {
      throw new Error('nocx: the Settings singleton is not a SettingsContent')
    }
    return live
  }

  // The sidebar width controller (nocx-qmcu): the single owner of the
  // panel's width between the drag handle, the settings observer and the
  // persistence seam. Created here at the composition root because all
  // three consume it — the bootstrap value above, the observer below, and
  // mountSidebar, which binds the kit ResizeHandle to it.
  const sidebarWidthCtrl = createSidebarWidthController(sidebarPanel, sidebarWidth, (width) => {
    // The persistence seam (persistSidebarWidth): fire-and-forget, and a
    // failed write surfaces a warning instead of reverting the width or
    // wedging the handle — the value stays applied, and the next commit
    // retries. `save` is a method, hence the closure.
    persistSidebarWidth(
      (px) => uiStateClient.save({ sidebar: { width: px } }),
      (message) => showToast({ level: 'warning', message }),
      width,
    )
  })

  // Live application through SettingsObserver: when any setting
  // changes, refetch the snapshot and act on relevant keys.
  observer.setRevision(0)
  observer.start(() => {
    void (async () => {
      try {
        const snap = await profileClient.getSnapshot()
        observer.setRevision(snap.revision)
        const enabled = snap.values[SANDBOX_ENABLED_KEY] === true
        setSandboxEnabledLive(enabled)
        void refreshSandboxAvailability(enabled)
        const next = snap.values[PLACEMENT_KEY] ?? 'horizontal'
        if (next !== placement) {
          placement = next
          const newStrip = next === 'vertical' ? new VerticalTabStrip() : new HorizontalTabStrip()
          wireQuickConnect(newStrip)
          tm.replaceStrip(newStrip)
        }
        // Theme setting changed — reconcile against Go's value (ADR-0013 §8.1).
        reconcileThemeFromGo(snap.values[THEME_KEY] as string | undefined)
        // The wrap default is live: flipping it repaints every block nobody
        // has overridden, in place, with no restart and no reflow of the
        // blocks that carry their own answer.
        applyOutputWrap(snap.values[OUTPUT_WRAP_KEY])
        // The cap is live too: a block frozen after the change is captured
        // under the new number, and blocks already stored keep what they got.
        applyOutputCap(snap.values[OUTPUT_CAP_KEY])
        // The sidebar width is deliberately absent from this loop now. It
        // is not a setting, so no settings revision can carry it, and one
        // window is the only thing that changes it — re-reading it here
        // would be the app telling itself what it just did (ADR-0033 §7).
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
        onOpenAsConnection={(host, user) => tm.openAsConnection(host, user)}
        unavailableIn={portsUnavailable}
        services={portsServices}
        visible={props.visible}
        pause={portsPause}
      />
    ),
    order: 0,
  }
  // ── Notes (nocx-z56hq) ───────────────────────────────────────────────
  // One store, every notes surface reads it. The panel FINDS a note and the
  // tab writes it; the chord skips the panel entirely, which is the whole
  // point of the feature (design §6.3).
  const notesStore = new NotesStore(new NotesClient(dispatcher))
  registerNotesSurface(tm, notesStore)
  const NOTES_VIEW: SidebarViewDescriptor = {
    id: 'notes',
    title: 'Notes',
    icon: TextQuoteIcon,
    view: (props) => (
      <NotesPanel
        store={notesStore}
        visible={props.visible()}
        onOpen={(id) => openNote(id, '')}
        onCreate={() => void createAndOpenNote()}
      />
    ),
    order: 1,
  }

  const sidebarViews = [filesView, PORTS_VIEW, gitView, NOTES_VIEW].sort(
    (a, b) => a.order - b.order,
  )
  if (sidebarViews[0]?.id !== FILES_VIEW_ID) {
    throw new Error('nocx: Files must be the first activity-bar view')
  }
  // The sidebar renders array order, so the views reach mountSidebar sorted
  // by their order field — the field then means what it says, and a future
  // view cannot slip in front by registration order. Files registers below
  // Ports (FILES_VIEW_ORDER < 0) and must be the FIRST icon in the view
  // zone — an owner requirement, asserted here and in files-view.test.tsx.
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
          openSettingsPane()
        },
      },
    ],
    {
      collapsed: uiState.sidebar.collapsed,
      activeViewId: uiState.sidebar.activeViewId,
      save: (next) => {
        // Fire-and-forget, and silent: a collapse that fails to persist
        // costs the next launch's starting state and nothing a user could
        // act on now. The width's seam warns because a drag is a
        // deliberate act with an expectation attached; toggling a panel
        // twenty times a session is not.
        void uiStateClient.save({ sidebar: next }).catch(() => {})
      },
    },
    /* eslint-disable solid/reactivity -- mountSidebar consumes these
       accessors reactively (SidebarViewProps.activeProfileId and
       .activeOrigin, fed with the ports target and the Files origin, plus
       the settings-mode accessor below); the reads happen inside the
       views' tracked scopes, and the gate cannot see across the function
       boundary. */
    () => portsTargetId(),
    () => activeOrigin(),
    sidebarWidthCtrl,
    () => activeSurfaceType() === SURFACE_SETTINGS,
    [
      {
        id: 'sandbox-shield',
        title: () => {
          const state = currentShieldState()
          return state.kind === 'ready'
            ? state.action === 'remove'
              ? `Remove sandbox from this tab (${state.workspace})`
              : `Convert this tab to a sandboxed shell (${state.workspace})`
            : state.kind === 'disabled'
              ? state.reason
              : 'Convert active tab to a sandboxed shell'
        },
        icon: ShieldIcon,
        selected: () => activeSandboxed(),
        hidden: () => currentShieldState().kind === 'hidden',
        disabled: () => currentShieldState().kind !== 'ready',
        onActivate: () => void convertActiveTabToSandboxed(),
      },
    ],
  )

  // Cmd/Ctrl+, opens or focuses the Settings tab.
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key === ',') {
      e.preventDefault()
      openSettingsPane()
    }
  })

  // ── Quick-connect picker (nocx-imkb.7) ──────────────────────────────
  // Both the initial tab strip AND replacement strips (via replaceStrip)
  // need onQuickConnect wired — the helper ensures no strip is missed.

  const qcContainer = document.createElement('div')
  document.body.append(qcContainer)

  const sshProvider = new SSHQuickConnectProvider(profileClient, (id, host, user) =>
    tm.newSSHPane(id, host, user),
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

  const getSandboxState = async (): Promise<{
    enabled: boolean
    status: SandboxStatus | null
  }> => {
    const snap = await profileClient.getSnapshot()
    const enabled = snap.values[SANDBOX_ENABLED_KEY] === true
    let status: SandboxStatus | null = null
    if (enabled) {
      try {
        status = await client.sandboxStatus()
      } catch {
        status = null
      }
    }
    return { enabled, status }
  }
  const reportSandboxOpenError = (message: string): void => {
    showToast({ level: 'danger', message: `Could not open a sandboxed shell: ${message}` })
  }

  convertActiveTabToSandboxed = async (): Promise<void> => {
    const ready = currentShieldState()
    if (ready.kind !== 'ready') return
    const source = tm.activeOrigin()
    const oldPane = source ? tm.paneOf(source.paneId) : undefined
    if (!source || !oldPane || !tm.tabOf(source.paneId)) return
    if (ready.action === 'remove') {
      const transcript = tm.captureConversionTranscript(oldPane.id)
      const made = tm.newLocalPaneAt(ready.workspace)
      if (
        (await made.created) &&
        (await tm.installConversionTranscript(
          made.pane.id,
          transcript,
          'Sandbox removed — new shell',
        ))
      ) {
        tm.replaceTabPosition(oldPane.id, made.pane.id)
        void tm.closePane(oldPane)
      }
      return
    }

    let state: { enabled: boolean; status: SandboxStatus | null }
    try {
      state = await getSandboxState()
    } catch (err) {
      reportSandboxOpenError(err instanceof Error ? err.message : 'sandbox status unavailable')
      return
    }
    if (!state.enabled) return
    if (!state.status?.available) {
      reportSandboxOpenError(
        `Sandbox unavailable (${state.status?.reason || 'status-unavailable'})`,
      )
      return
    }
    await openSandboxedShell(
      {
        getSnapshot: () => profileClient.getSnapshot(),
        openDirectory: () => dialogClient.openDirectoryDialog(),
        showPermissions: showSandboxPermissions,
        newSandboxedTab: (_workspace, launch) => {
          const transcript = tm.captureConversionTranscript(oldPane.id)
          const made = tm.newSandboxedPane(ready.workspace, launch)
          void (async () => {
            if (
              (await made.created) &&
              (await tm.installConversionTranscript(
                made.pane.id,
                transcript,
                'Sandbox enabled — new shell',
              ))
            ) {
              tm.replaceTabPosition(oldPane.id, made.pane.id)
              void tm.closePane(oldPane)
            }
          })()
        },
        reportError: reportSandboxOpenError,
      },
      { workspace: ready.workspace },
    )
  }

  const qcProviders: QuickConnectProvider[] = [
    new ActionsQuickConnectProvider(
      () => tm.newPane(),
      () => openSettingsPane().startNewConnection(),
      forwardPortCommand,
    ),
    sshProvider,
    // Snippets: one kind set of its own (the 'snippets' variant), so its
    // Enter — which types a saved phrase into the pane in front — can never
    // sit in the server list or the command palette.
    snippetsProvider,
    new SSHAliasQuickConnectProvider(profileClient, (host, user, port) =>
      tm.newSSHPane('', host, user, port),
    ),
    // Free-form fallback: "Connect to <host>" when the typed query matches
    // neither a saved profile nor an alias. Same host path as aliases — the
    // dialog only reaches it after every real match missed.
    new AdHocQuickConnectProvider((host, user, port) => tm.newSSHPane('', host, user, port)),
    // The vault half (nocx-fk32). It contributes only to the 'secrets'
    // variant — the dialog admits one kind set per variant — so these rows
    // never appear in the server list or the palette.
    new SecretsQuickConnectProvider({
      status: () => vaultClient.status(),
      inventory: () => vaultClient.inventory(),
      insert: (name) => void insertSecretIntoActivePane(name),
      // The create dialog is the vault's own, and it is the SAME one the
      // prompt's '@' picker opens (nocx-fk32.1): a secret needs a name and a
      // value, and neither picker is where a value gets typed.
      create: (name) => openSettingsPane().startNewSecret(name),
      // The vault layer owns both prompts (nocx-fk32.3); the picker only
      // says which one this state calls for.
      requestUnseal: () => vaultController.openUnlock('use its secrets'),
      requestSetup: () => vaultController.openSetup(),
    }),
  ]

  /** Insert a saved secret where the user is typing in the pane in front
   *  (nocx-fk32). The pane decides WHAT is inserted by asking who owns
   *  input — the reference into the editor's draft, the resolved value into
   *  the pty — because only there is the answer known. This composition
   *  root only routes the name and reports the outcome, and the outcome
   *  needs reporting: a password prompt echoes nothing, so an insert the
   *  user cannot see is an insert they will do twice. */
  async function insertSecretIntoActivePane(name: string): Promise<void> {
    const content = tm.activeTerminalContent()
    if (content === null) {
      showToast({ message: 'Open a terminal to insert a secret into', level: 'warning' })
      return
    }
    try {
      const where = await content.insertSecret(name)
      // The newline goes with the value (fc189136), so the toast reports a
      // completed send rather than asking for the keystroke that is no
      // longer needed. It still has to say something: a password prompt
      // echoes nothing, and an answer the user cannot see is an answer they
      // will give twice.
      if (where === 'value') showToast({ message: 'Secret sent — the prompt echoes nothing' })
      else if (where === 'unavailable')
        showToast({ message: `"${name}" could not be inserted`, level: 'danger' })
    } catch (err) {
      log.warn('nocx: inserting a secret failed', { error: err })
      showToast({ message: `"${name}" could not be inserted`, level: 'danger' })
    }
  }

  const qc = new QuickConnectController()
  qc.mount(qcContainer, qcProviders)

  // Firing a snippet somebody already chose — the ONE path every surface
  // that is not the palette's own list goes through: the toolbar menu's
  // rows and the completion dropdown's acceptance both land here, and the
  // palette owns what happens next (the ask form, the refusal sentences,
  // the focus return). A second copy of any of that is a second owner of
  // one behaviour (AD-8).
  function fireSnippet(snippet: Snippet): void {
    // One path for every surface that hands over a chosen snippet (the
    // completion dropdown today): a body that asks for values opens the
    // form; a plain one fires.
    if (askFields(snippet.body).length > 0) {
      snippetAsk.ask(snippet)
      return
    }
    void snippetsProvider.fire(snippet, new Map())
  }
  function fireSnippetById(id: string): void {
    const state = snippetsStore.state()
    if (state.kind !== 'ready') return
    const snippet = state.snippets.find((s) => s.id === id)
    if (snippet === undefined) return
    fireSnippet(snippet)
  }

  // The completion dropdown's snippet rows (design §10.2): the library the
  // provider reads and the acceptance it delegates. Set on the PaneManager,
  // so every pane built afterwards carries them.
  tm.snippets = {
    snippets: () => {
      const state = snippetsStore.state()
      return state.kind === 'ready' ? state.snippets : []
    },
    // Asked on a keystroke, so it must not BE a wire call per keystroke —
    // the store fires one read for an unread library and no more.
    ensureLoaded: () => snippetsStore.ensureLoaded(),
  }
  tm.onSnippetAccepted = (id) => fireSnippetById(id)

  function wireQuickConnect(strip: typeof tabStrip) {
    strip.onQuickConnect = () => qc.show()
    strip.onInsertSecret = () => qc.showSecrets()
    // The snippets action (design §10.3): the library without knowing the
    // chord — the SAME palette the chord opens, exactly as the key icon
    // beside it opens the secret list. Wired here, in the helper every
    // strip construction and replacement goes through: a callback set only
    // on the first strip works until the orientation changes and then
    // silently stops.
    strip.onSnippets = () => qc.showSnippets()
  }
  wireQuickConnect(tabStrip)

  // The snippet palette chord (⌥⌘P, snippets/chord.ts) — ONE opener, wired
  // here, reached from both keyboard boundaries (the xterm custom key
  // handler and the editor's arbiter chain), which both delegate through
  // the pane's TerminalContent (AD-8). The chord is consumed at the xterm
  // boundary, so zero bytes reach the pty (design §10.1, bead nocx-jj77).
  tm.onSnippetChord = () => qc.showSnippets()

  // ⌥⌘N: one keystroke from the impulse to the first character (design
  // §6.3). Document-level, like the palette's chord: a note is not the
  // pane's business, and the pane's own boundaries have nothing to add to
  // this one.
  document.addEventListener('keydown', (e) => {
    if (isNoteChord(e)) {
      e.preventDefault()
      void createAndOpenNote()
    }
  })

  // Cmd/Ctrl+Shift+P opens the PALETTE (nocx-4t37): commands and hosts
  // mixed, each row typed on the right; target-needing commands drill in.
  // Chosen to match the command-palette convention. The tab-strip caret
  // (wireQuickConnect above) stays the plain server list. Does not collide
  // with PaneManager (Ctrl+T/W/1-9), the terminal (single keystrokes), or
  // CodeMirror (which does not register this binding in its keymap).
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.code === 'KeyP') {
      e.preventDefault()
      qc.showPalette()
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

  void tm.openInitialPane()

  // --- Auto-update: check on start, then every 24 h ---

  // Report healthy once the initial tab's renderer mounted and PTY opened.
  tm.initialPaneReady.then(
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
