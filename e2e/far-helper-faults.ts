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
// `cmd/e2e-sshd` is the far host, and it watches the path it announces as
// `HELPERKILL=<path>`: each distinct content of that file is one cue, answered
// by SIGKILLing the daemon whose command is
// `<home>/.nocx/helper/<generation>/nocx-helper serve` — only that process, and
// only this account's. It then prints `HELPERKILLED=<pid>` per process
// signalled and a final `HELPERKILLDONE=<n>`. The fault belongs there, for the
// same reason the wire faults belong to `fault-proxy.ts` and not to a spec
// reaching into a socket: the party that owns the thing being faulted stages
// the fault, and says what it did on the observable the spec already reads.
//
// # Why a distinct content per cue
//
// An appear/disappear gate cannot be re-armed without a race: the far side
// polls, and a remove-then-write is one state change it may never see. Each
// call here writes a new nonce and waits for the count to move, so two kills in
// a row are two cues by construction.
import { writeFileSync } from 'node:fs'

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
      'killFarHelper: this fixture announced no HELPERKILL path — cmd/e2e-sshd prints it before READY, so the binary behind this run predates the kill switch',
    )
  }
  const before = helperKillCues(host.lines())
  writeFileSync(host.helperKillFile, `kill-${Date.now()}-${++cueSeq}`)

  const deadline = Date.now() + CUE_TIMEOUT_MS
  while (helperKillCues(host.lines()) === before) {
    if (Date.now() > deadline) {
      throw new Error(
        `killFarHelper: the far host answered no kill cue within ${CUE_TIMEOUT_MS}ms; last lines: ${host.lines().slice(-5).join(' | ')}`,
      )
    }
    await new Promise((resolve) => setTimeout(resolve, CUE_POLL_MS))
  }

  const pids = helperKilledPids(host.lines())
  if (pids.length === 0) {
    const failure = lastCueFailure(host.lines())
    throw new Error(
      `killFarHelper: the far host found no helper daemon to kill${failure === null ? '' : ` (${failure})`} — is the far helper installed and serving?`,
    )
  }
  return pids
}

/** How many kill cues the far host has completed. */
export function helperKillCues(lines: readonly string[]): number {
  return lines.filter((line) => line.startsWith('HELPERKILLDONE=')).length
}

/** The pids the LAST completed cue signalled: each cue's own answer, not every
 *  pid the far host ever killed. */
export function helperKilledPids(lines: readonly string[]): number[] {
  let pids: number[] = []
  for (const line of lines) {
    if (line.startsWith('HELPERKILLED=')) {
      const pid = Number(line.slice('HELPERKILLED='.length))
      if (Number.isInteger(pid) && pid > 0) pids.push(pid)
      continue
    }
    if (line.startsWith('HELPERKILLDONE=')) {
      const announced = Number(line.slice('HELPERKILLDONE='.length))
      pids = announced > 0 ? pids.slice(-announced) : []
    }
  }
  return pids
}

/** Why the far host's last cue killed nothing, when it said: a process table
 *  it could not read. Printed before the cue's own count. */
function lastCueFailure(lines: readonly string[]): string | null {
  for (let i = lines.length - 1; i >= 0; i--) {
    if (!lines[i].startsWith('HELPERKILLDONE=')) continue
    for (let j = i - 1; j >= 0; j--) {
      if (lines[j].startsWith('HELPERKILLFAILED=')) {
        return lines[j].slice('HELPERKILLFAILED='.length)
      }
      if (lines[j].startsWith('HELPERKILLDONE=')) return null
    }
    return null
  }
  return null
}
