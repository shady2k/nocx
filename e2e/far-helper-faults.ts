// The far helper dies while its SSH transport lives: the fault the ssh-panels
// scenarios need, staged by the far host itself (design §5.10, lifecycle
// scenario 4; nocx-oalo1.6.3).
//
// # What the fault is
//
// A far helper is a daemon installed under the far account's home and started
// by the bridge a coordinator's exec lane runs. Its death is not the
// connection's: the lane closes, the transport stays, and the panels' helper
// axis turns `helper-lost` with a Refresh that redials — while the open shell
// is untouched. A spec that only cut the whole connection could never tell
// those two apart.
//
// # Why the far host does the killing
//
// `cmd/e2e-sshd` is the far host, and with -helper-kill-dir it watches the path
// it announces as `HELPERKILL=<path>`: each distinct content of that file is
// one cue, answered by SIGKILLing the daemon whose command is
// `<home>/.nocx/helper/<generation>/nocx-helper serve` — only that process, and
// only this account's. It then prints `HELPERKILLED=<pid> cue=<cue>` per
// process signalled and a final `HELPERKILLDONE=<n> cue=<cue>`. The fault
// belongs there, for the same reason the wire faults belong to `fault-proxy.ts`
// and not to a spec reaching into a socket: the party that owns the thing being
// faulted stages the fault, and says what it did on the observable the spec
// already reads. The flag is why a fixture started for any other scenario
// watches nothing at all.
//
// # Why a distinct content per cue
//
// An appear/disappear gate cannot be re-armed without a race: the far side
// polls, and a remove-then-write is one state change it may never see. Each
// call here writes a new nonce, so two kills in a row are two cues by
// construction.
//
// # Why the cue is published by rename, and why the answer names it
//
// The gate is a file the far side polls, so writing it in place would truncate
// it first: a poll landing in that gap reads blank content, which is not a
// request, though it was once read as one. `publishCue` writes beside the gate
// and renames onto it, so the far side sees the whole cue or none of it — and
// the far side refuses a blank gate besides.
//
// Every answer carries the cue it answers, because waiting for the COUNT of
// answers to move lets a late answer to the previous cue satisfy this call
// before its own request has been looked at. This call waits for its own nonce
// to be answered, and reads the pids from that answer alone.
import { renameSync, writeFileSync } from 'node:fs'

/** The stand's far helper as this harness needs it; `SshdFixture` satisfies it. */
export interface FarHelperHost {
  /** The gate `cmd/e2e-sshd` announced, or '' when a stale fixture printed none. */
  readonly helperKillFile: string
  /** Every machine-readable line the fixture has printed, in arrival order. */
  lines(): readonly string[]
}

/** How long one cue may take before the far host is presumed not to answer. */
const CUE_TIMEOUT_MS = 30_000
/** How often the printed lines are read while a cue is outstanding. */
const CUE_POLL_MS = 25
/** Distinguishes two cues written inside the same millisecond. */
let cueSeq = 0

/**
 * Kill the far host's helper daemon and answer the pids the far side
 * signalled.
 *
 * The transport is deliberately not touched: it is the connection that carried
 * the daemon, and the fault is the daemon's death beside it. A far host that
 * found nothing to kill is an error rather than an empty answer — a spec that
 * asked for a death and got none would go on to assert against a helper that
 * never died.
 */
export async function killFarHelper(host: FarHelperHost): Promise<number[]> {
  if (!host.helperKillFile) {
    throw new Error(
      'killFarHelper: this fixture announced no HELPERKILL path — cmd/e2e-sshd prints one only when it was started with -helper-kill-dir, so this fixture was not asked to watch for a kill',
    )
  }
  const cue = nextCue()
  publishCue(host.helperKillFile, cue)

  const deadline = Date.now() + CUE_TIMEOUT_MS
  let answer = helperKillAnswer(host.lines(), cue)
  while (!answer.done) {
    if (Date.now() > deadline) {
      throw new Error(
        `killFarHelper: the far host answered no cue ${cue} within ${CUE_TIMEOUT_MS}ms; last lines: ${host.lines().slice(-5).join(' | ')}`,
      )
    }
    await new Promise((resolve) => setTimeout(resolve, CUE_POLL_MS))
    answer = helperKillAnswer(host.lines(), cue)
  }

  if (answer.pids.length === 0) {
    throw new Error(
      `killFarHelper: the far host found no helper daemon to kill for cue ${cue}${answer.failure === null ? '' : ` (${answer.failure})`} — is the far helper installed and serving?`,
    )
  }
  return [...answer.pids]
}

/** A new cue: one line, unique across calls and processes, echoed verbatim. */
function nextCue(): string {
  return `kill-${process.pid}-${Date.now()}-${++cueSeq}`
}

/**
 * Publish one cue without ever showing the gate half-written: write it beside
 * the gate, then rename it on. The far side polls the gate, and a rename is one
 * state change — it sees the whole cue or the previous state, never the empty
 * file a truncate-then-write leaves while the write is in flight.
 */
function publishCue(gate: string, cue: string): void {
  const beside = `${gate}.${cue}.tmp`
  writeFileSync(beside, cue, { mode: 0o600 })
  renameSync(beside, gate)
}

/** The answer one cue received, once the far host has printed it. */
export interface HelperKillAnswer {
  /** Whether the far host has answered this cue at all. */
  readonly done: boolean
  /** The pids THIS cue signalled, and none of an earlier cue's. */
  readonly pids: readonly number[]
  /** Why this cue killed nothing, when the far host said. */
  readonly failure: string | null
}

/**
 * The answer the far host has printed for `cue`, if any.
 *
 * An answer is `<name>=<value> cue=<cue>`, and the cue is matched as the whole
 * trailing field — so an answer to another cue is never read as this one's,
 * however late it arrives.
 */
export function helperKillAnswer(lines: readonly string[], cue: string): HelperKillAnswer {
  const pids: number[] = []
  let announced = 0
  let done = false
  let failure: string | null = null
  for (const line of lines) {
    const killed = answerValue(line, 'HELPERKILLED=', cue)
    if (killed !== null) {
      const pid = Number(killed)
      if (Number.isInteger(pid) && pid > 0) pids.push(pid)
      continue
    }
    const failed = answerValue(line, 'HELPERKILLFAILED=', cue)
    if (failed !== null) {
      failure = failed
      continue
    }
    const count = answerValue(line, 'HELPERKILLDONE=', cue)
    if (count !== null) {
      done = true
      announced = Number(count)
    }
  }
  return { done, pids: announced > 0 ? pids.slice(-announced) : [], failure }
}

/** The value of one answer line, when that line answers `cue`. */
function answerValue(line: string, prefix: string, cue: string): string | null {
  if (!line.startsWith(prefix)) return null
  const rest = line.slice(prefix.length)
  const field = ` cue=${cue}`
  if (!rest.endsWith(field)) return null
  return rest.slice(0, rest.length - field.length)
}
