// The disposable projections (ADR-0024 §5–§7, bead nocx-u7uh.7): the
// ledger, history and the block model consume the kernel and hold no
// lifecycle state of their own. This module pins the projection contract:
// the published attempt is the only authority — an app-owned ledger record
// binds to it and closes only on it (exit status exactly once, abandoned
// never successful), history persists ONLY completed app-owned records
// (and never the attempt's own command text — the privacy rule), and the
// block model opens on an attempt and freezes on its completion. The
import { describe, it, expect, vi } from 'vitest'
import { LifecycleKernel } from './state'
import type { ExecutionAttempt, LifecycleFact } from './state'
import { CommandLedger } from '../command-ledger'
import type { CommandRecord } from '../command-ledger'
import {
  LifecycleProjections,
  type BlockProjectionPort,
  type UnattributedCommand,
  type UnattributedPort,
} from './projections'

const LANE = 'lane-1'
const FENCE = 'a'.repeat(64)

function promptReady(domain = 'd1', epoch = 1): LifecycleFact {
  return { lane: LANE, lifecycle: 'prompt_ready', domain, epoch }
}

function running(
  domain = 'd1',
  epoch = 1,
  attempt: Partial<NonNullable<LifecycleFact['attempt']>> = {},
): LifecycleFact {
  return {
    lane: LANE,
    lifecycle: 'running',
    domain,
    epoch,
    attempt: { id: 'att-1', state: 'open', ...attempt },
  }
}

function lostF(): LifecycleFact {
  return { lane: LANE, lifecycle: 'lost' }
}

/** The block-port recorder: no DOM, just the ordered calls. */
class FakeBlocks implements BlockProjectionPort {
  readonly events: string[] = []
  bindBlock(a: ExecutionAttempt): void {
    this.events.push(`bind:${a.id}`)
  }
  openBlock(a: ExecutionAttempt): void {
    this.events.push(`open:${a.id}:${a.command ?? ''}`)
  }
  freezeBlock(a: ExecutionAttempt): void {
    this.events.push(`freeze:${a.id}:${a.exitCode ?? 'null'}`)
  }
  abandonBlock(a: ExecutionAttempt): void {
    this.events.push(`abandon:${a.id}`)
  }
  abandonPending(): void {
    this.events.push('abandon-pending')
  }
  enterBlock(): void {
    this.events.push('enter')
  }
}

function makeEnv(opts: { unattributed?: UnattributedPort } = {}) {
  const kernel = new LifecycleKernel()
  let minted = 0
  const ledger = new CommandLedger({ now: () => 1000, mintSubmitId: () => `sub-${++minted}` })
  const blocks = new FakeBlocks()
  const persist = vi.fn<(rec: CommandRecord, attempt: ExecutionAttempt) => Promise<unknown>>(() =>
    Promise.resolve(null),
  )
  const projections = new LifecycleProjections(
    kernel,
    ledger,
    blocks,
    persist,
    undefined,
    opts.unattributed,
  )
  projections.attach()
  return { kernel, ledger, blocks, persist, projections }
}

describe('the projections consume the kernel (ADR-0024, bead nocx-u7uh.7)', () => {
  it('an app submit binds to the published attempt and completes on its authenticated completion — once', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    // The editor submit: app-owned text, before any pty bytes.
    ledger.open('make {{secret:ci}}', '/repo', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    // The shell start attaches: the pending record binds to the attempt.
    kernel.applyFact(
      running('d1', 1, { id: 'att-1', origin: 'app', submitId: 'sub-1', command: 'make sk-live' }),
    )
    const rec = ledger.recordForAttempt('att-1')
    expect(rec).not.toBeUndefined()
    expect(rec?.command).toBe('make {{secret:ci}}') // app text, never the wire line
    expect(blocks.events).toEqual(['bind:att-1'])

    // The authenticated completion: exit status exactly once.
    kernel.applyFact(
      running('d1', 1, {
        id: 'att-1',
        state: 'completed',
        exitCode: 0,
        fence: FENCE,
        completedAt: '2026-08-08T12:00:02Z',
      }),
    )
    expect(rec?.status).toBe('success')
    expect(rec?.exitCode).toBe(0)
    expect(rec?.endedAt).toBe(1000)
    expect(persist).toHaveBeenCalledTimes(1)
    // The persisted record carries the app-owned text — never the attempt's.
    const [persisted] = persist.mock.calls[0]
    expect(persisted.command).toBe('make {{secret:ci}}')
    expect(persisted.status).toBe('success')
    expect(blocks.events).toEqual(['bind:att-1', 'freeze:att-1:0'])

    // A later change must not re-complete the same attempt.
    kernel.applyFact(nativeFact())
    expect(persist).toHaveBeenCalledTimes(1)
    expect(rec?.status).toBe('success')
  })

  it('an abandoned attempt is unknown, never successful, and persists nothing', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    ledger.open('sleep 100', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    kernel.applyFact(running('d1', 1, { id: 'att-1', origin: 'app', submitId: 'sub-1' }))
    kernel.applyFact(running('d1', 1, { id: 'att-1', state: 'unknown' }))
    const rec = ledger.recordForAttempt('att-1')
    expect(rec?.status).toBe('unknown')
    expect(rec?.exitCode).toBeNull()
    expect(rec?.endedAt).toBe(1000)
    expect(persist).not.toHaveBeenCalled()
    expect(blocks.events).toEqual(['bind:att-1', 'abandon:att-1'])
  })

  it('a shell-originated attempt opens a block but no ledger record and persists nothing', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    // No submit: the user typed at the native prompt. The shell's line may
    // carry a literal password — it opens no record and persists nowhere
    // (the command-text decision this bead owns).
    kernel.applyFact(promptReady())
    kernel.applyFact(running('d1', 1, { id: 'att-9', origin: 'shell', command: 'ssh pi@host' }))
    expect(ledger.records()).toHaveLength(0)
    expect(blocks.events).toEqual(['open:att-9:ssh pi@host'])

    kernel.applyFact(
      running('d1', 1, { id: 'att-9', state: 'completed', exitCode: 1, fence: FENCE }),
    )
    expect(ledger.records()).toHaveLength(0)
    expect(persist).not.toHaveBeenCalled()
    expect(blocks.events).toEqual(['open:att-9:ssh pi@host', 'freeze:att-9:1'])
  })

  it('a completed fact with no prior open fact still completes the pending app record (reconnect replay)', () => {
    const { kernel, ledger, persist } = makeEnv()
    ledger.open('make', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    // The open fact was lost (reattach replay of the completed state): the
    // authenticated completion resolves the single pending app record.
    kernel.applyFact(
      running('d1', 1, { id: 'att-1', state: 'completed', exitCode: 2, fence: FENCE }),
    )
    const rec = ledger.records()[0]
    expect(rec.status).toBe('failure')
    expect(rec.exitCode).toBe(2)
    expect(persist).toHaveBeenCalledTimes(1)
  })

  it('logical completion does not wait for the render fence — ledger and history land on the event alone (u7uh.8)', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    ledger.open('make', '/repo', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    kernel.applyFact(
      running('d1', 1, { id: 'att-1', origin: 'app', submitId: 'sub-1', command: 'make' }),
    )
    blocks.events.length = 0
    persist.mockClear()

    // The completion event arrives with its fence still in flight on the
    // pty. The exit status is recorded and history is written NOW — nothing
    // in this module waits for the fence bytes. The block port is asked to
    // freeze on the same event; the rendezvous (u7uh.8) defers the VISUAL
    // freeze until the fence lands, which is the block manager's concern,
    // never the ledger's or the store's.
    kernel.applyFact(
      running('d1', 1, {
        id: 'att-1',
        state: 'completed',
        exitCode: 0,
        fence: FENCE,
        completedAt: '2026-08-08T12:00:02Z',
      }),
    )
    const rec = ledger.recordForAttempt('att-1')
    expect(rec?.status).toBe('success')
    expect(rec?.exitCode).toBe(0)
    expect(persist).toHaveBeenCalledTimes(1)
    // The freeze port call is the LAST projection on the event — after the
    // status and the history write, never before them.
    expect(blocks.events).toEqual(['freeze:att-1:0'])
  })

  it('lane loss abandons the bound projections — unknown, never success, nothing persisted', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    ledger.open('make', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    kernel.applyFact(running('d1', 1, { id: 'att-1', origin: 'app', submitId: 'sub-1' }))
    kernel.applyFact(lostF())
    const rec = ledger.recordForAttempt('att-1')
    expect(rec?.status).toBe('unknown')
    expect(rec?.exitCode).toBeNull()
    expect(persist).not.toHaveBeenCalled()
    expect(blocks.events).toEqual(['bind:att-1', 'abandon:att-1'])
  })

  it('a nested session that ends freezes the block it left behind, not only the lane it hands back (nocx-mlyu)', () => {
    const { kernel, blocks } = makeEnv()
    // The whole nested-ssh cycle: parent at a prompt, the ssh attempt opens,
    // the parent suspends, the child runs `exit`, the child's domain closes,
    // the parent reclaims the lane and its own attempt completes.
    kernel.applyFact(promptReady('parent', 1))
    kernel.applyFact(
      running('parent', 1, { id: 'att-ssh', origin: 'shell', command: 'ssh pi@far' }),
    )
    kernel.applyFact(nativeFact()) // parent suspends: the handshake is a conventional terminal
    kernel.applyFact(promptReady('child', 2))
    kernel.applyFact(running('child', 2, { id: 'att-exit', origin: 'shell', command: 'exit' }))
    kernel.applyFact(nativeFact()) // the child's domain closed
    kernel.applyFact(
      running('parent', 1, { id: 'att-ssh', origin: 'shell', command: 'ssh pi@far' }),
    )

    // The kernel has already concluded it: the shell that would have sent
    // `exit`'s completion is the one the command destroyed, so the attempt
    // is unknown. The block must follow — a block whose domain is gone can
    // never complete, and a running dot that can never stop is a lie.
    expect(kernel.attempt('att-exit')?.state).toBe('unknown')
    expect(blocks.events).toContain('abandon:att-exit')

    kernel.applyFact(
      running('parent', 1, {
        id: 'att-ssh',
        state: 'completed',
        exitCode: 0,
        fence: FENCE,
      }),
    )
    // 'enter' sits between them: the ssh block ended when the far session
    // began (nocx-95kt), which is also what freed the running slot for the
    // far host's own blocks.
    expect(blocks.events).toEqual([
      'open:att-ssh:ssh pi@far',
      'enter',
      'open:att-exit:exit',
      'abandon:att-exit',
      'freeze:att-ssh:0',
    ])
  })

  // The measured case (nocx-mlyu): against a real sshd the far shell's start
  // frame for `exit` never reaches the kernel at all — the command destroys
  // the shell that would have sent it, and the transport dies first. So the
  // block has NO attempt to go unknown with: it was opened at the app-owned
  // submit and nothing downstream can ever finish it. That is the block the
  // owner watched climb to 1m1s.
  it('a submit whose domain ends before any attempt arrives is abandoned, not left running', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    kernel.applyFact(promptReady('parent', 1))
    kernel.applyFact(nativeFact()) // the parent suspends for the handshake
    kernel.applyFact(promptReady('child', 2))

    // The user submits `exit` in the editor: a record and a running block
    // open at the submit, before any byte goes out (ADR-0024 §5).
    const rec = ledger.open('exit', '/home/pi', 'far-host', () => undefined, 'shell')
    expect(rec.status).toBe('running')

    kernel.applyFact(nativeFact()) // the child's domain closed
    kernel.applyFact(promptReady('parent', 1)) // the parent reclaims the lane

    expect(rec.status).toBe('unknown')
    expect(rec.exitCode).toBeNull()
    // 'enter' is the child taking the lane; 'abandon-pending' is the submit
    // it left unfinishable when it closed again.
    expect(blocks.events).toEqual(['enter', 'abandon-pending'])
    expect(persist).not.toHaveBeenCalled() // an abandoned record persists nothing
  })

  it('a domain that merely suspends abandons nothing — the parent block outlives the nested session', () => {
    const { kernel, ledger, blocks } = makeEnv()
    kernel.applyFact(promptReady('parent', 1))
    const rec = ledger.open('ssh pi@far', '/home/me', '', () => undefined, 'shell')
    kernel.applyFact(running('parent', 1, { id: 'att-ssh', origin: 'app', submitId: 'sub-1' }))
    kernel.applyFact(nativeFact()) // suspended, NOT closed
    kernel.applyFact(promptReady('child', 2))

    expect(rec.status).toBe('running')
    // The RECORD stays running — the ssh process is alive and reports its
    // own status at the local D — while the BLOCK ends at entry. Those are
    // two different things, and this is the pair nocx-y5v5 named.
    expect(blocks.events).toEqual(['bind:att-ssh', 'enter'])
  })

  // nocx-z5k9, reproduced by the owner 2026-08-10: after `exit` the `exit`
  // blocks froze correctly but the parent's `ssh pi@…` blocks kept the
  // running dot and a climbing timer (1m18s, 9s). The block manager has
  // always known how to freeze the ssh block as `entered` when the remote
  // session begins — nocx-95kt designed it, freezeEntered implements it —
  // and it had NO caller outside its own file. The epic's own rule: a
  // reachable read path hid an unreachable write path.
  it('the ssh block freezes as entered when a child domain takes the lane', () => {
    const { kernel, blocks } = makeEnv()
    kernel.applyFact(promptReady('parent', 1))
    kernel.applyFact(
      running('parent', 1, { id: 'att-ssh', origin: 'shell', command: 'ssh pi@far' }),
    )
    expect(blocks.events).toEqual(['open:att-ssh:ssh pi@far'])

    kernel.applyFact(nativeFact()) // the parent suspends for the handshake
    // Suspension alone is not entry: the far session has not begun, and the
    // ssh block must not freeze on a handshake that may yet fail.
    expect(blocks.events).toEqual(['open:att-ssh:ssh pi@far'])

    // The child establishes: the remote session HAS begun, so the local ssh
    // block ends here and frees the running slot for the far host's blocks.
    kernel.applyFact(promptReady('child', 2))
    expect(blocks.events).toEqual(['open:att-ssh:ssh pi@far', 'enter'])
  })

  it('a parent reclaiming the lane is not an entry — nothing freezes a second time', () => {
    const { kernel, blocks } = makeEnv()
    kernel.applyFact(promptReady('parent', 1))
    kernel.applyFact(
      running('parent', 1, { id: 'att-ssh', origin: 'shell', command: 'ssh pi@far' }),
    )
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('child', 2))
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('parent', 1)) // back home: the stack shrank
    expect(blocks.events.filter((e) => e === 'enter')).toHaveLength(1)
  })

  it('the kernel exposes its attempts read-only — the projection lookup after loss', () => {
    const { kernel } = makeEnv()
    kernel.applyFact(promptReady())
    kernel.applyFact(running('d1', 1, { id: 'att-1' }))
    kernel.applyFact(lostF())
    const abandoned = kernel.attempt('att-1')
    expect(abandoned).not.toBeUndefined()
    expect(abandoned?.state).toBe('unknown')
    expect(kernel.attempt('nope')).toBeUndefined()
  })
  it('resets projection guards before a new session can reuse attempt identity', () => {
    const { kernel, ledger, blocks, projections } = makeEnv()
    const first = ledger.open('first', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    kernel.applyFact(
      running('d1', 1, { id: 'att-reused', origin: 'app', submitId: 'sub-1', command: 'first' }),
    )
    const pending = ledger.open('pending', '/', '', () => undefined, 'shell')
    expect(blocks.events).toEqual(['bind:att-reused'])

    projections.reset()
    expect(first.status).toBe('unknown')
    expect(pending.status).toBe('unknown')
    expect(blocks.events).toEqual(['bind:att-reused', 'abandon:att-reused', 'abandon-pending'])

    kernel.reset()
    ledger.open('second', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady('d2', 1))
    kernel.applyFact(
      running('d2', 1, { id: 'att-reused', origin: 'app', submitId: 'sub-1', command: 'second' }),
    )

    expect(blocks.events).toEqual([
      'bind:att-reused',
      'abandon:att-reused',
      'abandon-pending',
      'bind:att-reused',
    ])
  })
  it('does not abandon pending work in a fresh session after a previous domain ended', () => {
    const { kernel, ledger, blocks, projections } = makeEnv()
    ledger.open('first session', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady('first-domain'))
    kernel.applyFact(lostF())
    kernel.applyFact(nativeFact())
    projections.reset()

    // A replacement session uses a fresh domain id. The old session's ended
    // count must not be mistaken for an ending in this session.
    kernel.reset()
    ledger.open('second session', '/', '', () => undefined, 'shell')
    kernel.applyFact(promptReady('second-domain'))

    expect(
      blocks.events,
      'a fresh session must not abandon its pending command because an old domain ended',
    ).toEqual(['abandon-pending'])
  })
})

function nativeFact(): LifecycleFact {
  return { lane: LANE, lifecycle: 'native' }
}

// ── Binding by position, not by identity (nocx-td6d4.10) ────────────────
//
// "Is this published attempt the one my submit created" has an authoritative
// answer and a guessed one, and the codebase uses both. The assistant lane
// asks by identity: lifecycle.submitAttempt carries a client-minted
// requestId and the backend binds the run to the attempt by equality
// (ws_lifecycle.go). A person's Enter carries nothing, so the projection
// re-derives the pairing here — first unbound record whose status is
// running — and reads "found nothing" as "the human typed this straight at
// the shell", the one case that deliberately persists nothing because the
// line may carry a literal password.
//
// The two answers agree until a record is left behind. A submit whose
// lifecycle.submitAttempt was REFUSED is exactly that: the refusal is
// fail-open by design — "the bytes still go out and the session stays
// conventional" (terminal-content.ts) — so the command runs, the record
// stays running, and no attempt ever claims it. The next command's attempt
// then finds that stale record first and binds to it, and the outcome of one
// command is recorded under the text of another.
describe('an attempt binds the record its own submit opened', () => {
  it('does not bind a later attempt to the record a refused submit left behind', () => {
    const { kernel, ledger, persist } = makeEnv()
    kernel.applyFact(promptReady())

    // Submit one, refused: no attempt is ever published for it. The command
    // still ran — that is what fail-open means — so its record is left open.
    ledger.open('deploy staging', '/repo', '', () => undefined, 'shell')

    // Submit two, on a domain that is live this time.
    ledger.open('echo second', '/repo', '', () => undefined, 'shell')
    kernel.applyFact(
      running('d1', 1, { id: 'att-2', origin: 'app', submitId: 'sub-2', command: 'echo second' }),
    )
    kernel.applyFact(
      running('d1', 1, { id: 'att-2', state: 'completed', exitCode: 0, fence: FENCE }),
    )

    // The attempt belongs to the second submit, so the second submit's record
    // is what completes and what history is handed.
    expect(persist).toHaveBeenCalledTimes(1)
    const [recorded] = persist.mock.calls[0]
    expect(recorded.command).toBe('echo second')
    expect(recorded.exitCode).toBe(0)

    // And the abandoned first record is untouched: it was never this
    // attempt's, and closing it under another command's exit status would be
    // the same defect wearing the other face.
    const stale = ledger.records().find((r) => r.command === 'deploy staging')
    expect(stale?.status).toBe('running')
    expect(stale?.exitCode).toBeNull()
  })
})

describe('a command whose outcome is authenticated and recorded nowhere leaves a trace', () => {
  // nocx-2vb9y. The loss is silent in both halves today: the block freezes
  // with its exit status while nothing persists, and the backend never hears
  // of the command at all — no request, no error, no log. Two occurrences
  // cost a whole session and still did not settle which route they took, so
  // the routes are reported apart.

  it('reports a completed attempt that carried app authority and bound nothing', () => {
    const seen: UnattributedCommand[] = []
    const { kernel } = makeEnv({ unattributed: (r) => seen.push(r) })
    kernel.applyFact(promptReady())
    // The backend minted a submit token for this attempt — an app-owned
    // submit really happened — and the ledger holds no record carrying it.
    kernel.applyFact(
      running('d1', 1, { id: 'att-7', origin: 'app', submitId: 'sub-gone', command: 'make' }),
    )
    kernel.applyFact(
      running('d1', 1, { id: 'att-7', state: 'completed', exitCode: 1, fence: FENCE }),
    )

    expect(seen.map((r) => r.at)).toEqual(['bind', 'complete'])
    expect(seen[1]).toMatchObject({
      at: 'complete',
      attemptId: 'att-7',
      hadSubmitId: true,
      origin: 'app',
      exitCode: 1,
    })
  })

  it('reports the other door: a submitted record abandoned with the domain', () => {
    // The completion that arrives AFTER this door closes carries no app
    // authority — the shell's own start has no token — so the two branches
    // above would stay silent about a command that was submitted, ran, and
    // reached neither history nor the bell. This is the report that names it.
    const seen: UnattributedCommand[] = []
    const { kernel, ledger } = makeEnv({ unattributed: (r) => seen.push(r) })
    kernel.applyFact(promptReady('parent', 1))
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('child', 2))
    const rec = ledger.open('exit', '/home/pi', 'far-host', () => undefined, 'shell')

    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('parent', 1))

    expect(rec.status).toBe('unknown')
    expect(seen).toEqual([
      {
        at: 'abandon',
        attemptId: '',
        hadSubmitId: false,
        origin: 'app',
        exitCode: null,
        runningRecords: 0,
      },
    ])
  })

  it('says nothing about a genuinely shell-originated command', () => {
    const seen: UnattributedCommand[] = []
    const { kernel, persist } = makeEnv({ unattributed: (r) => seen.push(r) })
    // A line typed at the native prompt: no submit, no token, no record —
    // and persisting nothing is the command-text decision, not a loss.
    kernel.applyFact(promptReady())
    kernel.applyFact(running('d1', 1, { id: 'att-8', origin: 'shell', command: 'ls' }))
    kernel.applyFact(
      running('d1', 1, { id: 'att-8', state: 'completed', exitCode: 0, fence: FENCE }),
    )

    expect(persist).not.toHaveBeenCalled()
    expect(seen).toEqual([])
  })
})

describe('a record orphaned by a fail-open submit is still the command that ran', () => {
  // The submit path is fail-open by design: a refused lifecycle.submitAttempt
  // must never swallow the command, so the bytes go out anyway. The record it
  // opened then carries a token no attempt will ever echo. Before this, the
  // ledger refused to bind it (rightly — guessing is what nocx-td6d4.10 took
  // out) and the block model opened a SECOND block for the shell-originated
  // attempt, while complete()'s fallback quietly closed the first record. The
  // two halves disagreed about which block the outcome belonged to.

  it('binds the next unattributed attempt, so one block carries the outcome', () => {
    const { kernel, ledger, blocks, persist } = makeEnv()
    const rec = ledger.open('make test', '/repo', '', () => undefined, 'shell')
    // The submit was refused: nothing will ever carry this record's token.
    ledger.orphan(rec.id)
    kernel.applyFact(promptReady())
    kernel.applyFact(running('d1', 1, { id: 'att-3', origin: 'shell', command: 'make test' }))

    // Bound, not opened: the block that already exists is this command's.
    expect(blocks.events).toEqual(['bind:att-3'])
    expect(ledger.recordForAttempt('att-3')?.id).toBe(rec.id)

    kernel.applyFact(
      running('d1', 1, { id: 'att-3', state: 'completed', exitCode: 2, fence: FENCE }),
    )
    expect(persist).toHaveBeenCalledTimes(1)
    // The record keeps the APP-OWNED text, never the attempt's wire line.
    expect(persist.mock.calls[0][0].command).toBe('make test')
    expect(ledger.records()[0].exitCode).toBe(2)
  })

  it('and a record nobody orphaned is still never claimed by a tokenless attempt', () => {
    const { kernel, ledger, blocks } = makeEnv()
    ledger.open('make test', '/repo', '', () => undefined, 'shell')
    kernel.applyFact(promptReady())
    // No orphan mark: this is the guess nocx-td6d4.10 removed, and it stays
    // removed — a live submit's record is not up for grabs.
    kernel.applyFact(running('d1', 1, { id: 'att-4', origin: 'shell', command: 'ls' }))

    expect(blocks.events).toEqual(['open:att-4:ls'])
    expect(ledger.recordForAttempt('att-4')).toBeUndefined()
  })
})
