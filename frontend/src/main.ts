import './style.css'
import {
  GetWSPort,
  CheckForUpdate,
  ApplyUpdate,
  ReportHealthy,
} from '../wailsjs/go/main/WailsApp'
import { WSClient } from './ipc'
import { TabManager } from './tabs'
import { createClipboardAccess, ClipboardGate } from './clipboard'
import { ClipboardBannerImpl } from './banner'

/**
 * Renders the auto-update notice in the tab bar. The notice is a small,
 * non-modal element that shows update availability, download progress,
 * and pending-restart state. It renders from state — bound Go calls are
 * idempotent.
 */
class UpdateNotice {
  private readonly el: HTMLDivElement

  constructor(private bar: HTMLElement) {
    this.el = document.createElement('div')
    this.el.className = 'update-notice'
    this.el.style.display = 'none'
    this.bar.append(this.el)
  }

  /** Show an update is available with a link to release notes. */
  showAvailable(version: string, notesUrl: string): void {
    this.el.style.display = 'flex'
    this.el.innerHTML = ''
    const span = document.createElement('span')
    span.textContent = `nocx ${version} available`
    const link = document.createElement('a')
    link.href = notesUrl
    link.target = '_blank'
    link.rel = 'noopener'
    link.textContent = 'release notes'
    link.className = 'update-notes-link'
    const btn = document.createElement('button')
    btn.textContent = 'Update'
    btn.className = 'update-apply-btn'
    btn.addEventListener('click', () => {
      void this.apply()
    })
    this.el.append(span, ' · ', link, ' ', btn)
  }

  /** Show the busy/downloading state. */
  showDownloading(): void {
    this.el.style.display = 'flex'
    this.el.innerHTML = ''
    this.el.textContent = 'Downloading update…'
    this.el.className = 'update-notice downloading'
  }

  /** Show pending restart state after a successful apply. */
  showPendingRestart(version: string): void {
    this.el.style.display = 'flex'
    this.el.innerHTML = ''
    this.el.textContent = `nocx ${version} installed — restart to apply`
    this.el.className = 'update-notice pending'
  }

  /** Show an error message. */
  showError(msg: string): void {
    this.el.style.display = 'flex'
    this.el.innerHTML = ''
    this.el.textContent = `Update failed: ${msg}`
    this.el.className = 'update-notice error'
  }

  hide(): void {
    this.el.style.display = 'none'
  }

  private async apply(): Promise<void> {
    this.showDownloading()
    try {
      await ApplyUpdate()
      // After a successful apply, show pending restart.
      this.showPendingRestart('') // version unknown here; Go can enrich later
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      this.showError(msg)
    }
  }
}

async function main() {
  const bar = document.getElementById('tabbar')
  const panes = document.getElementById('panes')
  if (!bar || !panes) throw new Error('#tabbar / #panes not found')

  // Update notice — renders inline in the tab bar, right-aligned.
  const notice = new UpdateNotice(bar)

  // Clipboard access is constructed at the composition root and injected
  // down (AD-8). No consumer calls the factory — a test can inject a fake.
  const clipboard = createClipboardAccess()

  // OSC 52 gate — pure state, no DOM. Denied by default (Warp default).
  const gate = new ClipboardGate()

  // Banner — raised once on the first blocked OSC 52 write. The real
  // implementation manipulates the DOM; tests inject a fake.
  const banner = new ClipboardBannerImpl()

  // Bound Go method — no startup-event race. Guarded so the renderers still
  // mount without a Wails runtime (plain browser), where GetWSPort throws.
  let port = 9876
  try {
    port = await GetWSPort()
  } catch {
    console.warn('nocx: no Wails runtime, using fallback WS port', port)
  }

  const client = new WSClient()
  await client.connect(port)

  // TabManager opens the first tab and activates it in the constructor.
  // The renderer is selected via ?r=xterm|wterm inside TabManager.
  const tm = new TabManager(bar, panes, client, clipboard, gate, banner)

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

main().catch(console.error)
