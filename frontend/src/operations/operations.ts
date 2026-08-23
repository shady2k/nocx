// Operations — everything nocx is doing on somebody's behalf right now,
// in one list, in one order, with one count.
//
// ## Why this exists
//
// An upload survives a WebSocket drop and runs on its own SSH lease, and
// for a while the only place it could be SEEN was the Files panel. Switch
// sidebar view, or collapse the panel with Cmd+B, and a 2 GB transfer was
// invisible and uncancellable while it went on running. The activity bar's
// operations indicator is the place that is always on screen; this module
// is what it reads.
//
// ## It is not an upload list, and it is not a framework either
//
// Download (nocx-9le.8) has no panel of its own — it has nowhere else to
// appear — so the item type is shaped so a second kind of work is an
// ADDITION: a source function that yields `Operation`s. What is
// deliberately absent is everything a framework would have and no operation
// needs: no priorities, no queue, no retry policy, no registry.
//
// The one thing an item carries that is not data is `cancel`. That is
// deliberate: whether stopping an operation still means anything is a
// judgement about THAT operation, made by whoever owns it, and a surface
// that had to switch on `kind` to work it out would be a second owner of
// the question the moment a second kind existed.
//
// ## What this module does NOT own
//
// How long a finished operation is remembered. A store is what remembers,
// so each source bounds its own retention (`FINISHED_TRANSFERS_RETAINED`
// in files/upload-store.ts); this module only orders what it is given.

import { isTerminalPhase, type OperationKind, type OperationPhase } from '../ui/operation'

export interface Operation {
  /** Stable identity, unique across sources — the transfer id today. */
  id: string
  kind: OperationKind
  /** What the operation is about, as the person named it: the file. */
  title: string
  /** Where it is going. Empty where it is not known — an operation adopted
   *  from a retained outcome after a reload knows its name and not its
   *  destination, and says so by carrying nothing rather than a guess. */
  destination: string
  /** WHICH MACHINE `destination` is on, as a person names it
   *  (machine-name.ts). Empty only where it is genuinely unknown.
   *
   *  It is on the ITEM and not derived by the surface, and that is the
   *  whole point: this list is GLOBAL — one list for every tab — so by the
   *  time a row is drawn there is no tab to ask. A source records it when
   *  the operation STARTS, which is the only moment anybody knows. */
  machine: string
  phase: OperationPhase
  /** Bytes confirmed so far, or null while nothing has been OBSERVED. Not
   *  zero: zero is a measurement and this is its absence. */
  done: number | null
  /** The declared size. Zero is legitimate — an empty file is a file — and
   *  null is the absence of a declaration, which is what an operation
   *  adopted from a retained outcome has. */
  total: number | null
  speedBytesPerSecond: number | null
  /** Why it says what it says; null when there is nothing to say. */
  error: string | null
  /** When it started, on its source's clock; null where the source never
   *  saw it start. The opening end of the duration a finished row reports. */
  startedAt: number | null
  /** When it reached a terminal phase, on its source's clock; null while
   *  it is live. The finished half of the list is ordered by it. */
  endedAt: number | null
  /** Stop it, or null where stopping would mean nothing any more. */
  cancel: (() => void) | null
}

export interface OperationsModel {
  /** Everything worth showing: the live ones first, in the order they
   *  started, then the finished ones with the most recent directly under
   *  them. */
  operations(): Operation[]
  /** How many are still live — the badge's number, and absent at zero. A
   *  finished operation leaves this AT ONCE and stays in the list: success
   *  does not shout, and does not vanish without trace either. */
  activeCount(): number
  /** Aggregate progress over the live operations, or null when nothing is
   *  running — which is how the bar knows not to be there.
   *
   *  A record rather than a bare `number | null`, because zero is a real
   *  fraction: a surface handed `number | null` would write `progress() ?? 0`
   *  at the render site, which paints a default the model never produced
   *  and cannot see. The absence is the null; the number inside is always
   *  a measurement.
   *
   *  Determinate, never a spinner: a 20-minute upload must not put
   *  permanent motion in somebody's peripheral vision. Sizes that are all
   *  zero yield 0 rather than NaN — an empty file is a file, and it is not
   *  finished the instant it starts. */
  progress(): OperationsProgress | null
}

/** The aggregate, as a record — see `OperationsModel.progress`. Not
 *  exported: a surface reads the fraction off what it is handed and never
 *  names the type, the way the upload flow's collision ask is not named
 *  either. */
interface OperationsProgress {
  /** 0..1, clamped. */
  fraction: number
}

/** A source of operations: a reactive accessor, read inside the model's
 *  own derivations so a surface re-renders when any source moves. */
export type OperationSource = () => Operation[]

export function createOperationsModel(sources: readonly OperationSource[]): OperationsModel {
  // Plain functions rather than createMemo: every one of these is a cheap
  // derivation over a list a person is looking at, and a memo here would
  // add a node to dispose for no measurable saving. The reads inside stay
  // tracked either way, which is the part that matters.
  const all = (): Operation[] => sources.flatMap((source) => source())

  const live = (): Operation[] => all().filter((o) => !isTerminalPhase(o.phase))

  const operations = (): Operation[] => {
    const list = all()
    const finished = list
      .filter((o) => isTerminalPhase(o.phase))
      // Most recently finished first, so the top of the finished section
      // is stable as older ones fall off the end. `sort` is on a fresh
      // array — `filter` already made one — and never on a source's.
      .sort((a, b) => (b.endedAt ?? 0) - (a.endedAt ?? 0))
    return [...list.filter((o) => !isTerminalPhase(o.phase)), ...finished]
  }

  const activeCount = (): number => live().length

  const progress = (): OperationsProgress | null => {
    const running = live()
    if (running.length === 0) return null
    let done = 0
    let total = 0
    for (const o of running) {
      done += o.done ?? 0
      // A live operation with no declared size contributes nothing to the
      // denominator rather than poisoning the aggregate: the fraction is
      // clamped below, so the worst case is a bar that reads full early,
      // never a NaN that reads as no bar at all.
      total += o.total ?? 0
    }
    if (total <= 0) return { fraction: 0 }
    return { fraction: Math.min(1, done / total) }
  }

  return { operations, activeCount, progress }
}
