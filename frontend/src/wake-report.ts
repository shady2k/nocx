// What the pane looked like when the machine woke up.
//
// THIS IS A DIAGNOSTIC, NOT A FEATURE, and it is here because one symptom in
// the panes-lifecycle investigation was never explained. The backend defect
// was found and fixed — nothing noticed a silently dead SSH transport, so no
// exit was ever reported — but that explains SILENCE, not the other half of
// the report: after a suspend, the owner's ssh panes came back with no
// content at all, no scrollback and no editor, in the same process.
//
// Nothing in the code read so far erases a pane's content in place. A
// contrast experiment (an SSH server killed under a live app) produced the
// designed behaviour — the tab kept its mark, its scrollback and its editor —
// so whatever empties the pane is specific to the wake, and the wake is the
// one moment nobody could observe. Guessing produced three plausible
// mechanisms and no way to choose between them.
//
// So this measures instead. It writes ONE line per wake, to the log the
// backend already keeps, naming the things the three hypotheses disagree
// about:
//
//   - the renderer's grid: is there anything in the buffer at all, and does
//     the renderer still hold a live drawing context (a WebGL context lost to
//     a sleeping GPU renders nothing and would explain an empty canvas)?
//   - the pane's own geometry: a pane measured at zero renders as blank
//     whatever it holds, and WebKit measures a hidden pane at ~0 (AD-6's
//     UI-layer corollary records that hazard from the other side);
//   - the editor: present in the DOM, or gone;
//   - the session: still bound, and what the backend last said about it.
//
// It is deliberately cheap and deliberately silent: one line, only on a wake,
// only for a pane that is mounted. When the blank-pane defect is understood
// and fixed, this should be deleted along with the bead it belongs to — a
// diagnostic that outlives its question becomes noise nobody dares remove.

import { log } from './log'

/** What one pane can be asked about itself at wake. Every field is a
 *  MEASUREMENT the pane already holds; nothing here computes or repairs. */
export interface WakeObservation {
  /** The pane's own id (the frontend-minted uuid the backend knows it by),
   *  so a line can be matched against the backend's log for the same pane. */
  paneId: string
  sessionId: string | null
  /** Rows the renderer currently holds, or null when there is no renderer. */
  bufferRows: number | null
  /** Whether the renderer believes it can still draw (its context is live). */
  rendererLive: boolean | null
  /** The pane element's measured box. Zero is the finding, not an error. */
  width: number
  height: number
  /** Whether the command editor is in the DOM at all. */
  editorPresent: boolean
  /** Whether the editor is currently shown — hidden is NORMAL for a
   *  conventional session and must not be read as the defect. */
  editorVisible: boolean
  /** The pane's own connection condition at that moment. */
  connection: string
}

/** A source of wake events. The DOM's own; injected so a test can drive it. */
export interface WakeSource {
  subscribe(onWake: () => void): () => void
}

/** The browser's account of coming back: the document becoming visible again,
 *  and the window regaining focus. Both fire on a lid opening and neither is
 *  reliable alone, so the reporter debounces rather than choosing one. */
const documentWakeSource: WakeSource = {
  subscribe(onWake: () => void): () => void {
    const onVisibility = () => {
      if (document.visibilityState === 'visible') onWake()
    }
    document.addEventListener('visibilitychange', onVisibility)
    window.addEventListener('focus', onWake)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('focus', onWake)
    }
  },
}

/** How long two wake signals are treated as one event. A lid opening delivers
 *  visibilitychange and focus within a frame or two of each other, and two
 *  identical log lines would make the record harder to read, not richer. */
const COALESCE_MS = 500

/**
 * Watch for wakes and report what the panes looked like.
 *
 * The observer is supplied by the caller, so this module knows nothing about
 * panes and cannot be tempted to fix anything it sees.
 */
export function startWakeReporter(
  observe: () => WakeObservation[],
  source: WakeSource = documentWakeSource,
  now: () => number = () => Date.now(),
): () => void {
  // Negative infinity, not zero: with zero the FIRST wake is compared against
  // a timestamp that looks like a wake half a second ago, and a clock reading
  // near zero — which is what a test's injected clock reads, and what a
  // freshly-started page's performance-derived clock could read — swallows it.
  // The first wake is never a duplicate of anything.
  let last = Number.NEGATIVE_INFINITY
  return source.subscribe(() => {
    const at = now()
    if (at - last < COALESCE_MS) return
    last = at
    const panes = observe()
    if (panes.length === 0) return
    // One line, with every pane on it: a reader comparing a blank ssh pane
    // against a healthy local one in the same instant is exactly the
    // comparison this exists to make, and two log lines cannot be trusted to
    // describe the same moment.
    log.info('nocx: panes at wake', { panes })
  })
}
