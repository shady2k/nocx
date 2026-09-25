/**
 * Whether a scroll missed frames, judged against a baseline the scroll cannot
 * set (nocx-2v80t.3.39).
 *
 * THE BUDGET IS THE DISPLAY'S OWN INTERVAL, not a rounded constant: a 60 Hz
 * headless Chromium schedules frames at ~16.7 ms and jitters above it, and
 * headless WebKit reports whole milliseconds (16 and 17), so a fixed 16.7 ms
 * budget fails on the host's own vsync rather than on a missed frame. A missed
 * frame is one interval at about twice the display's, so the criterion is the
 * scroll's median within a quarter of the display interval and its p95 within
 * one dropped-frame-scale outlier.
 *
 * THE DISPLAY INTERVAL IS MEASURED WHILE NOTHING SCROLLS. It used to be read
 * from the scroll under test (its fastest interval, then its tenth
 * percentile), and a baseline the measured run sets is one it can move: 17
 * intervals at 16.7 ms and 162 at 33.4 ms made the baseline 33.4, and a scroll
 * that missed nine frames in ten passed. The idle sample is taken in the same
 * page, just before the scroll, and its MEDIAN is the interval — one early
 * callback (WebKit now and then delivers two a few milliseconds apart) moves a
 * median not at all.
 *
 * AND THE IDLE PAGE MUST ITSELF RUN AT A REFRESH RATE. A page too busy to
 * reach its display's rate even at rest would hand a slow baseline to the
 * scroll, which is the same hole from the other side; every browser this suite
 * runs schedules 60 Hz, so an idle interval above MAX_IDLE_INTERVAL_MS (50 Hz)
 * fails on its own.
 */

export const FRAME_TOLERANCE = 1.25
export const P95_TOLERANCE = 2
export const MAX_IDLE_INTERVAL_MS = 1000 / 50

export interface FrameVerdict {
  /** The display interval: the median of the idle intervals. */
  baselineMs: number
  medianMs: number
  p95Ms: number
  /** Empty when the scroll kept the display's rate. */
  failures: string[]
}

/** The same rank every read of these samples uses: floor(n × fraction). */
export function percentile(samples: readonly number[], fraction: number): number {
  const sorted = [...samples].sort((a, b) => a - b)
  return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * fraction))] ?? 0
}

export function judgeFrames(idle: readonly number[], scroll: readonly number[]): FrameVerdict {
  const baselineMs = percentile(idle, 0.5)
  const medianMs = percentile(scroll, 0.5)
  const p95Ms = percentile(scroll, 0.95)
  const failures: string[] = []
  if (idle.length === 0 || scroll.length === 0) {
    failures.push(`no frames sampled (idle ${idle.length}, scroll ${scroll.length})`)
  } else {
    if (!(baselineMs > 0) || baselineMs > MAX_IDLE_INTERVAL_MS) {
      failures.push(
        `the idle page ran at ${baselineMs} ms per frame, slower than ${MAX_IDLE_INTERVAL_MS} ms`,
      )
    }
    if (medianMs > baselineMs * FRAME_TOLERANCE) {
      failures.push(
        `scroll median ${medianMs} ms is over ${FRAME_TOLERANCE} x the idle ${baselineMs} ms`,
      )
    }
    if (p95Ms > baselineMs * P95_TOLERANCE) {
      failures.push(`scroll p95 ${p95Ms} ms is over ${P95_TOLERANCE} x the idle ${baselineMs} ms`)
    }
  }
  return { baselineMs, medianMs, p95Ms, failures }
}
