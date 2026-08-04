import './style.css'
import { GetWSPort, GetWSToken, CheckForUpdate, ReportHealthy } from '../wailsjs/go/main/WailsApp'
import { render } from 'solid-js/web'
import { Show } from 'solid-js'
import App from './App'
import { log } from './log'
import { WSClient } from './ipc'
import { TabManager } from './tabs'
import { mountSidebar } from './sidebar'
import { createClipboardAccess, ClipboardGate } from './clipboard'
import { ClipboardBannerImpl } from './banner'
import { ProfileClient } from './profiles'
import { VaultClient } from './vault-client'
import { DialogClient } from './dialog-client'
import { createVaultState, SetupDialog, UnlockDialog } from './vault'
import { VaultObserver } from './vault-observer'
import { Dispatcher } from './dispatcher'
import { SettingsContent, SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'
import { HorizontalTabStrip, VerticalTabStrip } from './tab-strip'
import { SurfaceRegistry, SURFACE_ID_SETTINGS } from './surface-registry'
import { mountUpdateNotice } from './update-notice'
import { SettingsIcon } from './ui/icons'
import { SettingsObserver } from './settings-observer'
import { bootstrapTheme, reconcileThemeFromGo } from './renderers/theme-bootstrap'
import { bootstrapPlatform } from './platform'
import { showToast } from './ui/toast'
import {
  QuickConnectController,
  ActionsQuickConnectProvider,
  SSHQuickConnectProvider,
  SSHAliasQuickConnectProvider,
  type QuickConnectProvider,
} from './quick-connect'

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
  const vaultObserver = new VaultObserver(dispatcher)
  const vaultController = createVaultState(vaultClient)
  vaultObserver.start(() => {
    void vaultController.refresh()
  })
  void vaultController.refresh()

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
  // - Top zone: views from the registry (currently empty; Explorer, Git,
  //   and Servers are future beads).
  // - Bottom zone: global actions (currently only the Settings gear).
  //
  // Connections has been removed from the activity bar — it is not a view
  // and not an action (see .internal/specs §2.4).  It is now a Settings
  // sub-page reachable from the Settings rail.
  mountSidebar(
    activityBar,
    sidebarPanel,
    /* views — empty until nocx-708q */ [],
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
  const qcProviders: QuickConnectProvider[] = [
    new ActionsQuickConnectProvider(
      () => tm.newTab(),
      () => openSettingsTab().startNewConnection(),
      // Sandboxed shell… action (ADR-0019 §3.2): live flag + backend status
      // on every open; picker → new sandboxed tab. Cancellation is a no-op.
      {
        state: async () => {
          const snap = await profileClient.getSnapshot()
          const enabled = snap.values['sandbox.enabled'] === true
          let status: Awaited<ReturnType<WSClient['sandboxStatus']>> | null = null
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
  ]

  const qc = new QuickConnectController()
  qc.mount(qcContainer, qcProviders)

  function wireQuickConnect(strip: typeof tabStrip) {
    strip.onQuickConnect = () => qc.show()
  }
  wireQuickConnect(tabStrip)

  // Cmd/Ctrl+Shift+P opens the quick-connect picker.
  // Chosen to match VS Code's command-palette convention. Does not collide
  // with TabManager (Ctrl+T/W/1-9), the terminal (single keystrokes), or
  // CodeMirror (which does not register this binding in its keymap).
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key === 'P') {
      e.preventDefault()
      qc.show()
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
            onClose={() => vaultController.closeUnlock()}
            onUnsealed={() => vaultController.onUnsealDone()}
            vaultClient={vaultClient}
            vaultStatus={vaultController.status()}
            reason={vaultController.unlockReason()}
          />
        </Show>
      </>
    ),
    vaultRoot,
  )
}

main().catch((err) => log.error('nocx: main error', { message: (err as Error).message }))
