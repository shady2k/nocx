// A block's elapsed time — the ONE wording for a header, running or finished
// (nocx-ouru5.1.7). It used to be two functions in blocks.ts
// (`formatRunningDuration` and `formatDuration`) that agreed under a minute
// and disagreed about everything a long command reaches: a 167-minute run read
// "167m 36s", and the branch that decided "minutes is the largest unit" was
// written twice — once per state, so neither could be fixed alone.
//
// Under a minute the figure keeps the shape the owner sees today: tenths of a
// second ("12.8s"), and milliseconds below one second, because "0.0s" says the
// measurement never happened (spec 2026-09-15 §1.7 round 3). Above that the
// unit follows the magnitude — minutes, hours, days — and each unit is the
// largest that still says the whole span, rounded DOWN so a figure never
// names a value the unit above it would carry.
//
// The unit WORDS are the catalogue's (locale-model): "43m 8s" and "43 мин 8 с"
// are one figure in two languages, which is why this module translates rather
// than hard-codes the suffixes. A number here is figures, not prose: it keeps
// the call site's own spelling in every language.
import { t } from '../i18n/locale-model'

const SECOND = 1_000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** How long a block has been — or was — running.
 *
 *  Called for a running block on every tenth-second tick and once for a
 *  finished one, so both states of one block read as one formatter's answer:
 *  a figure that changes at the moment a block settles would be a second
 *  wording, and the settle is exactly where "43m 8s" became "167m 36s".
 *
 *  Empty for a duration that is not one — negative, or not a number — rather
 *  than printing "NaNm". */
export function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return ''
  if (ms < SECOND) return t('scrollback.duration.ms', '{value}ms', { value: Math.round(ms) })
  if (ms < MINUTE) {
    return t('scrollback.duration.seconds', '{value}s', { value: (ms / SECOND).toFixed(1) })
  }
  const minutes = Math.floor(ms / MINUTE)
  if (ms < HOUR) {
    return t('scrollback.duration.minutes', '{minutes}m {seconds}s', {
      minutes,
      seconds: Math.floor((ms % MINUTE) / SECOND),
    })
  }
  const hours = Math.floor(ms / HOUR)
  if (ms < DAY) {
    return t('scrollback.duration.hours', '{hours}h {minutes}m', {
      hours,
      minutes: minutes % 60,
    })
  }
  return t('scrollback.duration.days', '{days}d {hours}h', {
    days: Math.floor(ms / DAY),
    hours: hours % 24,
  })
}
