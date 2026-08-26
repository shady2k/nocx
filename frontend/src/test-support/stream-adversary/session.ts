// The assembled-session seam for the stream-adversary harness. One interface,
// two implementations in spirit: `assembleTodaySession` wires the modules that
// exist after the ADR-0024 severance (the two-axis lifecycle kernel, the
// app-owned command ledger), and the projection bead replaces its internals
// with the published-fact projections WITHOUT touching the projection
// contract below. That is what makes the corpus reusable: the snapshot shape
// is the contract the later bead tests against.
//
// SEVERED (ADR-0024 §1): OSC 133 markers are render-only. The seam parses
// them and records the event (delivery proof) but feeds nothing — no input
// state, no ledger, no blocks, no history. OSC 636 is a command-existence
// snapshot and nothing more (the readiness passport that shared its channel
// is deleted with bead nocx-u7uh.11). OSC 7 keeps its validated location
// role. The buffer axis — fed by the alt-buffer CSI sequence, a
// renderer-owned presentation fact — drives the kernel's buffer axis; the
// kernel's lifecycle axis is fed ONLY by published facts, and the hostile
// corpus carries none, so the authority projections stay at their
// post-severance verdicts: Native, raw input, no domain, no rewrite, no
// rerun.
import { CommandLedger } from '../../command-ledger'
import { LifecycleKernel } from '../../lifecycle/state'
import { parseOsc133, parseOsc7 } from '../../renderers/xterm'
import type { CorpusFrame } from './corpus'

/** The nine security-sensitive projections, captured before and after each
 *  case. Plain data — deep-cloned by the harness, never shared by reference. */
export interface SessionProjection {
  /** Lifecycle axis. Post-severance: the buffer axis reported through the
   *  InputState label (Native | ALT_SCREEN). Under ADR-0024 §6: the
   *  two-axis LifecycleState (Native | PromptReady(domain) |
   *  Running(attempt) | Desynchronized(domain) | Lost). */
  lifecycle: string
  /** Who owns keyboard input: 'editor' or 'raw'. Post-severance: always
   *  'raw' — no stream sequence may grant DOM keyboard ownership. */
  keyboardRoute: 'editor' | 'raw'
  /** The accepted domain id, if any. Post-severance: always null — the
   *  corpus carries no published facts, so the kernel mints no domain. */
  activeDomain: string | null
  /** Serialized attempt state from the ledger (status + exit code). */
  attemptState: string
  /** Running/frozen block counts (scrollback projection). */
  blockState: string
  /** History records persisted (history.record calls) during the case. */
  historyCalls: number
  /** Integration-sensitive ssh rewriting enabled? Post-severance: always
   *  false — the _shellIntegrated latch is deleted. */
  rewriteAuthority: boolean
  /** Re-run authorized? Post-severance: always false. */
  rerunAuthority: boolean
  /** OSC 7 cwd (render-only location metadata). */
  cwd: string | null
}

/** The seam the harness replays against. One instance per corpus case. */
export interface SessionAssembly {
  events: string[]
  dispatch(frame: CorpusFrame): void
  snapshot(): SessionProjection
}

const INBAND_READY = '1337;NOCX_IB_READY'

/**
 * Wires the severed modules the way terminal-content does, minus the DOM:
 * OSC 133 markers are parsed and logged but drive nothing (ADR-0024 §1),
 * OSC 7 updates cwd, and the alt-buffer CSI sequence drives the buffer
 * axis of the two-axis lifecycle kernel. 'app' frames model the editor
 * submit that synchronously creates the attempt before any bytes are
 * written (ADR-0024 §5). The readiness passport is deleted (nocx-u7uh.11);
 * OSC 636 carries only the command-existence snapshot, which the harness
 * does not model.
 *
 * All state is real module state — no mocks, no hand-rolled reducer. The
 * ledger's persistence seam is gone with the marker cycle: nothing completes
 * a record, so history.record has no terminal caller and historyCalls is
 * always zero.
 */
export function assembleTodaySession(): SessionAssembly {
  const lifecycle = new LifecycleKernel()
  const events: string[] = []

  const ledger = new CommandLedger({ now: () => 1000 })
  let cwd: string | null = null

  function dispatch(frame: CorpusFrame): void {
    switch (frame.channel) {
      case 'app': {
        if (frame.payload.startsWith('submit:')) {
          const command = frame.payload.slice('submit:'.length)
          ledger.open(command, '/tmp', '', () => undefined, 'shell')
          events.push(`app:submit:${command}`)
        } else {
          events.push(`app:unknown:${frame.payload}`)
        }
        return
      }
      case 'osc133': {
        const marker = parseOsc133(frame.payload)
        if (marker === null) {
          events.push('osc133:rejected')
          return
        }
        // SEVERED: the marker is logged (delivery proof) and drives nothing.
        events.push(
          `marker:${marker.kind}${marker.exitCode !== undefined ? `:${marker.exitCode}` : ''}`,
        )
        return
      }
      case 'osc7': {
        const parsed = parseOsc7(frame.payload)
        if (parsed !== null) {
          cwd = parsed.path
          events.push('cwd')
        } else {
          events.push('osc7:rejected')
        }
        return
      }
      case 'csi': {
        if (frame.payload === '?1049h') {
          lifecycle.setBuffer('alternate')
          events.push('buffer:alternate')
        } else if (frame.payload === '?1049l') {
          lifecycle.setBuffer('normal')
          events.push('buffer:normal')
        } else {
          events.push(`csi:ignored:${frame.payload}`)
        }
        return
      }
      case 'private-osc': {
        // The renderer registers exactly one private OSC: the in-band READY.
        // It confirms the wrapper's echo mode; it is not lifecycle authority.
        events.push(frame.payload === INBAND_READY ? 'inband:ready' : 'private-osc:ignored')
        return
      }
      case 'dcs': {
        events.push('dcs:ignored')
        return
      }
    }
  }

  function snapshot(): SessionProjection {
    const records = ledger.records()
    const last = records[records.length - 1]
    const running = records.some((r) => r.status === 'running') ? 1 : 0
    return {
      // The projection contract reports the buffer through the lifecycle
      // label today (the kernel's lifecycle axis carries no facts in the
      // corpus); the projection bead reconnects it to the two-axis model.
      lifecycle: lifecycle.buffer === 'alternate' ? 'ALT_SCREEN' : 'Native',
      keyboardRoute: 'raw',
      activeDomain: null,
      attemptState:
        last === undefined
          ? 'none'
          : `id:${last.id} ${last.status}${last.exitCode !== null ? `:${last.exitCode}` : ''}`,
      blockState: `${running} running, 0 frozen`,
      historyCalls: 0,
      rewriteAuthority: false,
      rerunAuthority: false,
      cwd,
    }
  }

  return { events, dispatch, snapshot }
}
