import './style.css'
import { GetWSPort, GetWSToken, CheckForUpdate, ReportHealthy } from '../wailsjs/go/main/WailsApp'
import { render } from 'solid-js/web'
import App from './App'
import { log } from './log'
import { WSClient } from './ipc'
import { TabManager } from './tabs'
import { mountSidebar } from './sidebar'
import { createClipboardAccess, ClipboardGate } from './clipboard'
import { ClipboardBannerImpl } from './banner'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import { SettingsContent, SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'
import { HorizontalTabStrip, VerticalTabStrip } from './tab-strip'
import { SurfaceRegistry, SURFACE_ID_SETTINGS } from './surface-registry'
import { mountUpdateNotice } from './update-notice'
import { SettingsIcon } from './ui/icons'
import { SettingsObserver } from './settings-observer'
import { bootstrapTheme, reconcileThemeFromGo } from './renderers/theme-bootstrap'
import {
  QuickConnectController,
  LocalShellQuickConnectProvider,
  SSHQuickConnectProvider,
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

  // Surface registry — surfaces declared once, every entry point resolves
  // through the registry rather than rebuilding the descriptor. (AD-8)
  const registry = new SurfaceRegistry()
  registry.register(SURFACE_ID_SETTINGS, {
    surfaceType: SURFACE_SETTINGS,
    singletonKey: SINGLETON_SETTINGS,
    // Settings hosts the Connections page (nocx-imkb.3), so it needs the same
    // connect callback the standalone surface has. Assigned inside the factory,
    // before mount: the factory builds a fresh SettingsContent each time it is
    // opened, and a setter applied afterwards would leave the first connect
    // click of a freshly opened tab with nothing to call.
    factory: () => {
      const content = new SettingsContent(profileClient)
      content.onConnect = (profile) => {
        log.info('nocx: connect from Settings', { profileId: profile.id })
        tm.newSSHTab(profile.id, profile.options.host, profile.options.user)
      }
      return content
    },
    descriptor: {
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Settings',
    },
  })

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
          const { content, descriptor } = registry.build(SURFACE_ID_SETTINGS)
          tm.openTab(content, descriptor)
        },
      },
    ],
  )

  // Cmd/Ctrl+, opens or focuses the Settings tab.
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key === ',') {
      e.preventDefault()
      const { content, descriptor } = registry.build(SURFACE_ID_SETTINGS)
      tm.openTab(content, descriptor)
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
    new LocalShellQuickConnectProvider(() => tm.newTab()),
    sshProvider,
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
}

main().catch((err) => log.error('nocx: main error', { message: (err as Error).message }))
