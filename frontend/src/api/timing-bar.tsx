// The exchange, drawn to scale: how the elapsed time was actually spent.
//
// It replaces one line of numbers — `dns 97.278ms · connect 0ms · tls 0ms ·
// ttfb 0ms · total 97.337ms` — which is exact and answers the wrong question.
// The question a person asks of a slow request is "WHERE did it go", and five
// numbers in a row make them do the arithmetic themselves, including the
// subtraction that is not written down: `ttfb` is measured from the start of
// the request, so the server's own thinking time is `ttfb` minus the three
// phases before it, and nothing on that line says so.
//
// So the bar is the reading and the numbers stay underneath it. Five spans,
// proportional to the total:
//
//   resolve   the name → an address
//   connect   the TCP dial
//   tls       the handshake
//   waiting   ttfb minus the three above: the far side thinking
//   download  total minus ttfb: the body coming back
//
// WHY IT IS NOT IN THE KIT. `ui/` is the product's control vocabulary — the
// things a person operates. This is one domain's reading of one exchange,
// with that domain's five phases named in it, and putting it in the kit would
// put HTTP into a module that knows nothing about protocols. If a second
// surface ever needs a segmented duration bar, this moves there and takes a
// generic segment list with it; until then it lives beside the run it draws.
//
// A ZERO PHASE IS NOT DRAWN, and that is deliberate rather than cosmetic: a
// direct-to-address request does no lookup, a plain http:// one does no
// handshake, and a hairline of colour for a phase that did not happen reads
// as a phase that was instant. The legend below only names what is on the
// bar, so the two never disagree.

import { For, Show, createEffect } from 'solid-js'
import { Caption } from '../ui/caption'
import { formatElapsed } from './api-model'
import type { ApiTimings } from './api-model'

/** One drawn phase: what it is called, how long it took, and the class that
 *  paints it. Built here from the timings the backend sent — never by a
 *  caller, so the five phases have one owner. */
interface Phase {
  id: string
  label: string
  ms: number
}

/**
 * The five phases, in the order they happen.
 *
 * `waiting` and `download` are DERIVED, and both are clamped at zero. The
 * wire carries measurements from four independent clocks' worth of hooks —
 * a lookup the route did rather than the transport, a handshake that never
 * finished, a body read that ended before the first byte was recorded — and
 * a negative width is a rendering bug where the honest answer is "that phase
 * did not happen". A clamp cannot lie about the total, which is drawn from
 * its own measurement.
 */
export function phasesOf(t: ApiTimings): Phase[] {
  const before = t.dnsMs + t.connectMs + t.tlsMs
  const waiting = Math.max(0, t.ttfbMs - before)
  const download = Math.max(0, t.totalMs - Math.max(t.ttfbMs, before))
  return [
    { id: 'resolve', label: 'resolve', ms: t.dnsMs },
    { id: 'connect', label: 'connect', ms: t.connectMs },
    { id: 'tls', label: 'tls', ms: t.tlsMs },
    { id: 'waiting', label: 'waiting', ms: waiting },
    { id: 'download', label: 'download', ms: download },
  ].filter((p) => p.ms > 0)
}

export interface TimingBarProps {
  timings: ApiTimings
}

export function TimingBar(props: TimingBarProps) {
  const phases = () => phasesOf(props.timings)
  // The DRAWN total, which is the sum of what is on the bar rather than the
  // measured total: the two differ by whatever the clamps above removed, and
  // a bar whose spans add up to something other than its own width would
  // leave a gap nobody can account for. The measured total is printed beside
  // it, so nothing is hidden — it is simply not the divisor.
  const drawn = () => phases().reduce((sum, p) => sum + p.ms, 0)
  const share = (ms: number): string => `${(drawn() === 0 ? 0 : (ms / drawn()) * 100).toFixed(3)}%`

  return (
    <Show when={phases().length > 0}>
      <div class="api-timing">
        <div
          class="api-timing__bar"
          role="img"
          aria-label={phases()
            .map((p) => `${p.label} ${formatElapsed(p.ms)}`)
            .concat(`total ${formatElapsed(props.timings.totalMs)}`)
            .join(', ')}
        >
          <For each={phases()}>
            {(p) => (
              <span
                class="api-timing__span"
                data-phase={p.id}
                // The width is DATA — this phase's share of the bar — so it
                // is set as a custom property on the element rather than as
                // an inline style prop, which the surface rules forbid and
                // which would put the paint in the markup. The stylesheet
                // still owns `width: var(--api-timing-share)`; this only
                // says what the share is (api-pane.tsx sets the tree's width
                // the same way).
                ref={(el: HTMLElement) => {
                  createEffect(() => el.style.setProperty('--api-timing-share', share(p.ms)))
                }}
                title={`${p.label} ${formatElapsed(p.ms)}`}
              />
            )}
          </For>
        </div>
        {/* The numbers stay: a bar answers "where did it go" and cannot
            answer "how long exactly", which is the question the next person
            asks when they compare two runs. */}
        <div class="api-timing__legend">
          <For each={phases()}>
            {(p) => (
              <span class="api-timing__key">
                <span class="api-timing__swatch" data-phase={p.id} aria-hidden="true" />
                <Caption>{`${p.label} ${formatElapsed(p.ms)}`}</Caption>
              </span>
            )}
          </For>
          <span class="api-timing__key" data-total="true">
            <Caption>{`total ${formatElapsed(props.timings.totalMs)}`}</Caption>
          </span>
        </div>
      </div>
    </Show>
  )
}
