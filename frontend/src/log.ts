/**
 * Logging seam — the single owner of the Wails logging FFI.
 *
 * ADR-0011 design intent:
 * The `fields` parameter is where structured data lives, and once the
 * Secret type lands (bead nocx-l7o, PR11-T7) the fields type must reject
 * it — secrets must never reach the serialised log string. That check is
 * NOT implemented here yet; this seam is shaped so it can be added.
 */

import { Log } from '../bindings/github.com/shady2k/nocx/wailsapp'
import { bindingReachable } from './wails-runtime'

/** The generated bindings are named, so this is the key both the browser
 *  shim and the reachability probe split. */
const LOG_BINDING = 'main.WailsApp.Log'

/** Devtools flip for decision tracing: `window.nocxDebug = true`. Read
 *  live so it takes effect without a reload. */
interface WailsWindow extends Window {
  nocxDebug?: unknown
}

type LogLevel = 'info' | 'warn' | 'error' | 'debug'

/** Structured fields for a log entry — must reject Secret when that type lands. */
type LogFields = Record<string, unknown>

function write(level: LogLevel, msg: string, fields?: LogFields): void {
  const parts = [msg]
  if (fields && Object.keys(fields).length > 0) {
    try {
      parts.push(JSON.stringify(fields))
    } catch {
      parts.push('[unserializable fields]')
    }
  }
  const full = parts.join(' ')

  // Which side takes the line is decided synchronously, before any call.
  // v2 probed window.go for the Log method; v3 asks the same question of
  // the runtime it actually has (shim bridge, or the webview's invoke
  // bridge). Deciding it by catching the call's rejection instead would
  // put every browser log line a microtask after the event that caused it
  // — invisible to a synchronous reader, and to a test.
  if (!bindingReachable(LOG_BINDING)) {
    consoleFor(level)(full)
    return
  }

  // Backend path. A rejection here is the soft degrade — the line still
  // reaches the console rather than vanishing — and callers never see a
  // floating promise.
  Log(full).catch(() => {
    consoleFor(level)(full)
  })
}

/** The console sink for a level. */
function consoleFor(level: LogLevel): (msg: string) => void {
  switch (level) {
    case 'error':
      return console.error
    case 'warn':
      return console.warn
    case 'debug':
      return console.debug
    default:
      return console.log
  }
}

/**
 * Decision tracing (the `nocx:decide` stream) is OFF by default. The
 * arbiter-grant and ghost-refusal logs are per-keystroke when enabled, so
 * nothing is emitted — and no fields are built — until a person opts in.
 * Flip it from the devtools console with `window.nocxDebug = true`; the
 * flag is read live, so it takes effect on the next keystroke.
 */
let decisionTracingEnabled = false

/** Programmatic switch (tests, a future settings surface). */
export function setDecisionTracing(enabled: boolean): void {
  decisionTracingEnabled = enabled
}

/** Whether decision tracing is on: the programmatic flag OR the devtools
 *  `window.nocxDebug` flag. Cheap enough to call on the hot path — it is
 *  the gate that keeps per-keystroke tracing off when nobody asked for it. */
export function isDecisionTracing(): boolean {
  if (decisionTracingEnabled) return true
  if (typeof window !== 'undefined' && (window as WailsWindow).nocxDebug === true) return true
  return false
}

/** The stable prefix every decision trace carries — filter the devtools
 *  console on exactly this string to see the whole decision stream. */
const DECISION_PREFIX = 'nocx:decide'

/** Emit one decision trace through the existing seam (Wails FFI, or the
 *  console when no runtime). No-op without even building the message while
 *  decision tracing is off — the hot-path guarantee. */
export function logDecision(msg: string, fields?: LogFields): void {
  if (!isDecisionTracing()) return
  write('debug', `${DECISION_PREFIX} ${msg}`, fields)
}

/** What the browser reported for one keydown, on a surface that reads keys
 *  (nocx-1ebrs.32).
 *
 *  WHY THIS EXISTS. A key a surface never sees and a key it sees and drops are
 *  the same thing from the outside — nothing happens — and the one signal that
 *  separates them is the four values a KeyboardEvent carries. The defect that
 *  bought this line is macOS-only (the numeric keypad's Enter): the engines CI
 *  runs cannot reproduce the shipped WebView's report, so the person on that
 *  machine reads it here. `where` names the surface, which is the whole of the
 *  discriminator — the composer's handler and the grid's input element each
 *  report, and whichever one has no line for the key is the one that dropped
 *  it.
 *
 *  OFF UNLESS ASKED FOR: it is the existing decision trace, so it is silent in
 *  a shipped build and behind `window.nocxDebug = true` in the devtools that
 *  NOCX_DEVTOOLS=1 opens. The four values are the DOM's own fields, not names
 *  of ours: `keyCode` and `location` are legacy and deprecated, and they are
 *  exactly what a report has to quote for a defect that only one engine has. */
export function logKeydown(where: 'composer' | 'grid', e: KeyboardEvent): void {
  logDecision('keydown', { where, ...keyShape(e) })
}

/** What a keydown may be written down as: the surface that saw it, and the
 *  key's CLASS — never the character (nocx-atpw5.1.39).
 *
 *  WHY. This trace exists to answer "which surface saw the key", and it was
 *  written when only somebody in devtools with `window.nocxDebug` could read
 *  it. It is now on in the e2e stand, whose console goes into the failure
 *  block and from there into CI's log and its artifacts — a file that leaves
 *  the machine. `e.key` is the CHARACTER for every printable key, so the old
 *  fields would have written down exactly what a person typed, which is the
 *  one thing AGENTS.md keeps out of every log: a password typed into a
 *  running ssh arrives here as an ordinary letter.
 *
 *  A NAMED KEY IS NOT A CHARACTER. `Enter`, `Tab`, `ArrowLeft`, `Escape`,
 *  `Backspace`, `F5` are control names from the DOM's fixed vocabulary and
 *  carry nothing a person typed, so they stay — the numeric keypad's `Enter`
 *  (the macOS defect this trace was written for, nocx-1ebrs.32) is exactly
 *  such a name, and a report that could not tell `Enter` from `NumpadEnter`
 *  would have lost the instrument for that bead. Everything with a
 *  one-character `key` reports `char` and nothing else. */
function keyShape(e: KeyboardEvent): { class: 'char' | 'named'; key?: string } {
  // NOT `length === 1` ALONE: a lone surrogate half or a dead key's own name
  // would slip through as neither. `Array.from` counts code points, so an
  // astral character is one character and still `char`.
  const characters = Array.from(e.key)
  if (characters.length === 1) return { class: 'char' }
  if (e.key === '') return { class: 'named', key: '(none)' }
  return { class: 'named', key: e.key }
}

export const log = {
  info(msg: string, fields?: LogFields): void {
    write('info', msg, fields)
  },
  warn(msg: string, fields?: LogFields): void {
    write('warn', msg, fields)
  },
  error(msg: string, fields?: LogFields): void {
    write('error', msg, fields)
  },
  debug(msg: string, fields?: LogFields): void {
    write('debug', msg, fields)
  },
}
