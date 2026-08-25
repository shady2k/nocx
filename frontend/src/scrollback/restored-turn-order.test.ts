// @vitest-environment jsdom
//
// Restore reproduces the live arrangement, and the comparison is against a
// LIVE turn's DOM (nocx-dc2fr.4, the tree arrangement's counterpart of
// nocx-9sqii criterion 9).
//
// Not against a fixture this file wrote: a fixture is what the author of the
// restore path believed the live path draws, and the two agree exactly where
// nobody looked. So both sides are built here, through their real owners —
// BlockManager for the live flow, restoredTurn plus restoredBlock and
// arrangedByCause for the restored one — and the assertion is that the two
// DOMs read the same.
//
// WHAT IS COMPARED, and it is stated because "the same" needs a definition
// (ADR-0040): the sequence of blocks in the scrollback, and inside a turn the
// sequence of its children and the TEXT each one draws — a run of prose is
// read by its rows, a tool call by its header, a command by its header. NOT
// the chrome: a restored block carries `data-restored` and offers nothing
// that needs a process (ADR-0019 §3), which is a difference the product
// REQUIRES.
//
// WHAT THE RESTORED SIDE DRAWS, AND WHY IT IS THE SAME LIST. Live, a turn is
// ONE block whose children are the causal sequence in the seats the store
// gave them. Restored, the page came back in ledger order and the relation
// placed it; the drawer seats each child where the store said it sits — both
// sides build the same list through the same builders, which is the property
// this file asserts.
import { describe, it, expect } from 'vitest'
import { BlockManager } from './blocks'
import { restoredBlock, restoredTurn } from './restored-block'
import { arrangedByCause, blocksForPane, type RestorableBlock } from '../restore-client'
import type { Caused } from '../generated/ledger.get'
import { DEFAULT_SNAPSHOT } from './serializer'
import { CommandSnapshotStore } from '../command-snapshot'

const S = DEFAULT_SNAPSHOT

/** A scrollback the manager can own, attached like the real one. */
function newManager() {
  const inner = document.createElement('div')
  document.body.appendChild(inner)
  const xtermContainer = document.createElement('div')
  inner.appendChild(xtermContainer)
  const manager = new BlockManager(inner, xtermContainer, {
    snapshotStore: new CommandSnapshotStore(),
  })
  return { inner, manager }
}

/** Everything a reader meets inside a turn, in DOM order, each as
 *  "kind:text" — the same reading the live flow's own tests take, so the
 *  two sides are read identically. */
function flowOf(el: HTMLElement): string[] {
  const out: string[] = []
  const piece = (c: Element): void => {
    if (c.classList.contains('term-line')) {
      out.push(`text:${c.textContent ?? ''}`)
      return
    }
    out.push(c.className)
  }
  const box = el.querySelector(':scope > .cmd-children')
  for (const child of Array.from(box?.children ?? [])) {
    const c = child as HTMLElement
    if (!c.classList.contains('cmd-block')) {
      piece(c)
      continue
    }
    const kind = c.dataset.blockKind ?? 'command'
    const header = c.querySelector(':scope > .cmd-header .cmd-header-text')?.textContent ?? ''
    if (kind === 'tool') {
      out.push(`call:${header}`)
      continue
    }
    if (kind !== 'text') {
      out.push(`${kind}:${header}`)
      continue
    }
    // A text child's rows: the LIVE body is under [data-answer-body], the
    // RESTORED one under .cmd-output-ask — the shared reading is the
    // term-lines themselves, whatever the box around them.
    const body = c.querySelector('[data-answer-body]') ?? c.querySelector('.cmd-output-ask')
    for (const row of Array.from(body?.children ?? [])) {
      piece(row)
    }
  }
  return out
}
/** Every TOP-LEVEL block in the scrollback, as "kind:header" in DOM order. */
function topLevel(root: HTMLElement): string[] {
  return Array.from(root.children)
    .filter((c): c is HTMLElement => c.classList.contains('cmd-block'))
    .map(
      (b) =>
        `${b.dataset.blockKind ?? 'command'}:${b.querySelector('.cmd-header-text')?.textContent ?? ''}`,
    )
}

// The turn under test, in one description both sides are built from: the
// assistant is asked something, reads a file, says what it is about to do,
// runs a command — which opens a real block — and answers from its output.
const QUESTION = 'what went wrong?'
const BEFORE = 'let me look at the file. '
const AFTER = 'line 3 is wrong'
// No concatenation of the two: the answer is not one string any more
// (ADR-0040). BEFORE and AFTER are separate `text` blocks either side of the
// command, and joining them here would be this test quietly re-inventing the
// cut it exists to prove nobody has to make.
const COMMAND = 'cat -n a.txt'

describe('a restored turn reads the same as the live turn it came from', () => {
  /** The live turn, driven through the engine exactly as the ask surface
   *  drives it: the calls arrive over the wire, the command is submitted
   *  through the ordinary path, and the answer streams around both. */
  function liveTurn() {
    const { inner, manager } = newManager()
    const live = manager.addAnswerBlock(QUESTION, '/repo')
    live.el.dataset.entryId = 'turn-1'
    live.toolCall({
      callId: 'c0',
      tool: 'files.read',
      effect: 'observe',
      resource: { kind: 'path', id: '/repo/a.txt' },
      opensBlock: false,
    })
    live.append(BEFORE)
    live.toolCall({ callId: 'c1', tool: 'run', effect: 'mutate-destructive', opensBlock: true })
    manager.startBlock(COMMAND, '/repo', 0, 0, 'agent')
    live.append(AFTER)
    live.close('success')
    return inner
  }

  it('draws the same blocks in the same order — children included (ADR-0040)', () => {
    const live = liveTurn()
    const turnEl = live.querySelector<HTMLElement>('.cmd-block[data-block-kind="ask"]')!

    // RESTORED. The page comes back in ledger order — and here the command
    // the turn ran does NOT immediately follow its turn, because a person
    // typed something in this pane while the assistant was working.
    // arrangedByCause puts it back where it belongs; nothing else moves.
    const page: RestorableBlock[] = [
      block('turn-1', QUESTION, 'shell'),
      block('typed-1', 'git status', 'shell'),
      block('cmd-1', COMMAND, 'shell'),
    ]
    const causes: Caused[] = [
      {
        entryId: 'act-0',
        position: 0,
        kind: 'action',
        source: 'assistant' as const,
        intent: 'files.read',
        args: { path: '/repo/a.txt' },
        effect: 'observe',
        resource: { kind: 'path', id: '/repo/a.txt' },
        opensBlock: false,
      },
      // The prose between the calls is a `text` child in the tree; the
      // store holds it and the restore seats it by its own body.
      {
        entryId: 'txt-1',
        position: 1,
        kind: 'text',
        source: 'assistant' as const,
        intent: '',
        args: null,
        effect: null,
        resource: null,
        opensBlock: false,
      },
      {
        entryId: 'act-1',
        position: 2,
        kind: 'action',
        source: 'assistant' as const,
        intent: 'run',
        args: null,
        effect: 'mutate-destructive',
        resource: null,
        opensBlock: true,
      },
      {
        entryId: 'cmd-1',
        position: 3,
        kind: 'shell',
        source: 'assistant' as const,
        intent: COMMAND,
        args: null,
        effect: null,
        resource: null,
        opensBlock: false,
      },
      {
        entryId: 'txt-2',
        position: 4,
        kind: 'text',
        source: 'assistant' as const,
        intent: '',
        args: null,
        effect: null,
        resource: null,
        opensBlock: false,
      },
    ]
    const restored = draw(
      page,
      causes,
      new Map([
        ['txt-1', BEFORE],
        ['txt-2', AFTER],
      ]),
    )
    const restoredTurnEl = restored.querySelector<HTMLElement>('.cmd-block[data-block-kind="ask"]')!

    // ONE list, drawn top to bottom: the same children in the same seats.
    expect(flowOf(restoredTurnEl)).toEqual(flowOf(turnEl))
    expect(flowOf(turnEl)).toEqual([
      'call:files.read',
      `text:${BEFORE}`,
      `command:${COMMAND}`,
      `text:${AFTER}`,
    ])
    // The turn's own block is ONE block; the children live INSIDE it, not
    // beside it — and the command the person TYPED keeps the ledger's own
    // place, outside the turn, exactly where it was (nothing is reordered
    // that the relation does not name).
    expect(topLevel(restored)).toEqual([`ask:${QUESTION}`, 'command:git status'])
    expect(topLevel(live)).toEqual([`ask:${QUESTION}`])
  })

  it('CRITERION 1 — a command the assistant ran restores as a command with a terminal grid, not as prose (the defect)', () => {
    // The whole point of the task: a command the assistant ran is
    // kind='shell' WITH source='assistant', and the restore must draw the
    // GRAMMAR from the kind (a terminal grid, never re-wrapped) and the
    // BADGE from the source. The old code derived both from kind, so an
    // agent-run command came back as prose.
    const restored = draw(
      [
        block('turn-1', QUESTION, 'shell'),
        // The block's display author is what blocksForPane maps from
        // entries.source — 'agent' is how an assistant-run command's badge
        // reads once the source column has done its job.
        block('cmd-1', COMMAND, 'agent'),
      ],
      [
        {
          entryId: 'cmd-1',
          position: 0,
          kind: 'shell',
          source: 'assistant' as const,
          intent: COMMAND,
          args: null,
          effect: null,
          resource: null,
          opensBlock: false,
        },
      ],
      // The command's own body: a VT grid, which restoredBlock renders as
      // term-lines.
      new Map([['cmd-1', '\u001b[32mok\u001b[0m']]),
    )
    const cmd = restored.querySelector<HTMLElement>('.cmd-block[data-block-kind="command"]')
    expect(cmd).not.toBeNull()
    // A terminal grid: the VT body renders as term-lines, never as prose
    // under the answer-body renderer.
    expect(cmd!.querySelector('.term-line')).not.toBeNull()
    expect(cmd!.querySelector('[data-answer-body]')).toBeNull()
    // The badge is read from SOURCE (the block's display vocabulary maps
    // assistant → 'agent'), never guessed from the kind.
    expect(cmd!.querySelector('[data-author]')?.getAttribute('data-author')).toBe('agent')
  })

  // CRITERION 2 — THE MATRIX, IN ONE DRAW.
  //
  // Criterion 1 asserts the cell the defect lived in. This asserts that the
  // two columns are READ INDEPENDENTLY, which is the property that stops the
  // defect coming back in another cell.
  //
  // One turn with two commands under it. The commands differ ONLY in source;
  // the turn differs from both only in kind. So:
  //
  //   a reader that dropped SOURCE badges the two commands the same
  //   a reader that dropped KIND draws the turn like a command
  //
  // and each mistake is caught by a different assertion below, which is why
  // this is one draw and not three tests.
  //
  // Written by the coordinator rather than by the author of the change
  // (AGENTS.md rule 4): a test written by the implementer in the same pass
  // encodes the implementer's model, including the parts that are wrong.
  it('CRITERION 2 — grammar comes from kind and the badge from source, read independently', async () => {
    const MINE = 'ls -la'
    const THEIRS = 'df -h'
    const cause = (
      entryId: string,
      position: number,
      intent: string,
      source: 'user' | 'assistant',
    ) => ({
      entryId,
      position,
      kind: 'shell' as const,
      source,
      intent,
      args: null,
      effect: null,
      resource: null,
      opensBlock: false,
    })
    // THE PAGE COMES THROUGH THE REAL MAPPING, and that is the whole
    // difference between this test and a test of its own fixture. `block()`
    // takes the display author as an argument, so a page built with it
    // asserts what the test decided; the badge would then survive the source
    // column being dropped entirely. So the rows are built from WIRE
    // ENTRIES by blocksForPane — the same function the pane calls — and the
    // author is derived from `source` by the product, once.
    const page = await blocksForPane(
      fakeClient([
        wireEntry('turn-1', QUESTION, 'ask', 'user'),
        wireEntry('cmd-mine', MINE, 'shell', 'user'),
        wireEntry('cmd-theirs', THEIRS, 'shell', 'assistant'),
      ]),
      'pane-1',
    )
    const restored = draw(
      page,
      [cause('cmd-mine', 0, MINE, 'user'), cause('cmd-theirs', 1, THEIRS, 'assistant')],
      new Map([
        ['cmd-mine', 'a.txt'],
        ['cmd-theirs', '9.7G'],
      ]),
    )

    const of = (command: string): HTMLElement => {
      const el = Array.from(restored.querySelectorAll<HTMLElement>('.cmd-block')).find(
        (b) => b.querySelector('.cmd-header-text')?.textContent === command,
      )
      expect(el, `no block drew for ${command}`).not.toBeUndefined()
      return el!
    }
    const badgeOf = (el: HTMLElement): string | null =>
      el.querySelector('[data-author]')?.getAttribute('data-author') ?? null

    const mine = of(MINE)
    const theirs = of(THEIRS)

    // WHAT IS NOT ASSERTED HERE, AND WHERE IT IS. The grammar is decided by
    // `restoredBody`, which this harness bypasses: `blockFacts` hands the
    // builder a kind directly, so reading the kind back off the DOM here
    // would assert what the test itself supplied. Mutation-checking is what
    // told the two apart — dropping the source read fails the badge
    // assertions below, and re-pointing the GRAMMAR at the wrong column does
    // not fail anything in this file. So the grammar half lives with its
    // owner, in restore-client.test.ts ("a command the ASSISTANT ran is a
    // command, not prose"), where it is asserted at the seam that decides
    // and does fail under that mutation.
    //
    // What this test owns is the other half: SOURCE decides the badge, and
    // it is the ONLY thing these two rows differ by. Equal badges here is a
    // reader that dropped the column.
    expect(badgeOf(mine)).toBeNull()
    expect(badgeOf(theirs)).toBe('agent')
    expect(badgeOf(mine)).not.toBe(badgeOf(theirs))
  })
})

// ── the restored side, through the same seam the pane reaches ────────────

/** Draw the restored page the way terminal-content.restorePast does: the
 *  relation places the blocks, a turn becomes one block carrying its
 *  children in seat order, and a block the turn caused is not drawn twice.
 *  Text children are drawn from their own stored bodies. */
function draw(
  page: RestorableBlock[],
  causes: Caused[],
  childBodies: Map<string, string>,
): HTMLElement {
  const root = document.createElement('div')
  const store = new CommandSnapshotStore()
  const container = () => document.createElement('div')
  const byID = new Map(page.map((b) => [b.entryId, b]))
  const causesOf = (id: string) => (id === 'turn-1' ? causes : [])
  const placed = new Set<string>()
  for (const b of arrangedByCause(page, causesOf)) {
    if (placed.has(b.entryId)) continue
    placed.add(b.entryId)
    if (b.entryId !== 'turn-1') {
      root.appendChild(restoredBlock(blockFacts(b, 'out'), S, container, () => {}, store))
      continue
    }
    let id = 100
    for (const el of restoredTurn(
      {
        command: QUESTION,
        cwd: '/repo',
        location: '',
        durationMs: 0,
        exitCode: null,
        status: 'success',
        body: null,
        author: 'agent',
        kind: 'ask',
        entryId: 'turn-1',
        causes,
        proseEvicted: false,
      },
      S,
      () => id++,
      container,
      () => {},
      store,
      (cause) => {
        if (cause.kind === 'text') {
          const body = childBodies.get(cause.entryId)
          return restoredBlock(
            {
              id: id++,
              command: '',
              cwd: '',
              location: '',
              kind: 'text',
              body: body ?? '',
              author: cause.source === 'assistant' ? ('agent' as const) : ('shell' as const),
              status: 'success',
              durationMs: null,
              exitCode: null,
              entryId: cause.entryId,
            },
            S,
            container,
            () => {},
            store,
          )
        }
        if (cause.kind === 'action') {
          // A call that OPENED A BLOCK draws no child line of its own: the
          // command block it opened IS the account of that call, exactly as
          // the live flow places it (ADR-0040).
          if (cause.opensBlock) return null
          return restoredBlock(
            {
              id: id++,
              command: cause.intent ?? '',
              cwd: '',
              location: '',
              kind: 'tool',
              body: null,
              author: cause.source === 'assistant' ? ('agent' as const) : ('shell' as const),
              status: 'success',
              durationMs: null,
              exitCode: null,
              entryId: cause.entryId,
            },
            S,
            container,
            () => {},
            store,
          )
        }
        const row = byID.get(cause.entryId)
        if (!row || placed.has(cause.entryId)) return null
        placed.add(cause.entryId)
        return restoredBlock(blockFacts(row, 'out'), S, container, () => {}, store)
      },
    )) {
      root.appendChild(el)
    }
  }
  return root
}

function blockFacts(b: RestorableBlock, body = 'out') {
  return {
    id: 0,
    command: b.command,
    cwd: b.cwd,
    location: b.host,
    durationMs: b.durationMs,
    exitCode: b.exitCode,
    status: b.status,
    body,
    author: b.author,
    kind: 'command' as const,
    entryId: b.entryId,
  }
}

/** One `ledger.query` entry as the wire really shapes it, narrowed to what
 *  blocksForPane reads. `kind` and `source` are given SEPARATELY on purpose:
 *  a fixture that derived one from the other would hide the very
 *  independence the matrix test asserts. */
function wireEntry(
  id: string,
  intent: string,
  kind: 'shell' | 'ask',
  source: 'user' | 'assistant',
): Record<string, unknown> {
  return {
    id,
    intent,
    kind,
    source,
    cwd: '/repo',
    host: '',
    status: 'success',
    durationMs: 0,
    exitCode: 0,
  }
}

/** A client that answers `ledger.query` with the page it was built from, so
 *  blocksForPane runs its real mapping over real wire shapes. Newest-first,
 *  because that is the order the method documents and blocksForPane
 *  reverses. */
function fakeClient(entries: Record<string, unknown>[]): Parameters<typeof blocksForPane>[0] {
  return {
    call: () =>
      Promise.resolve({
        entries: [...entries].reverse(),
        scope: 'everywhere',
        exhausted: true,
        hasRows: true,
        coverage: null,
      }),
  } as unknown as Parameters<typeof blocksForPane>[0]
}

function block(entryId: string, command: string, author: 'shell' | 'agent'): RestorableBlock {
  return {
    entryId,
    command,
    cwd: '/repo',
    host: '',
    status: 'success',
    durationMs: 0,
    exitCode: 0,
    author,
  }
}
