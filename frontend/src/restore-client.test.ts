// One fetch path for one entry's artifact (nocx-v13pd), and one assembly for
// a whole turn's answer (nocx-3dteo).
//
// A block's body and ONE run of prose are the SAME two round trips —
// ledger.get for the entry, ledger.artifact for the payload — differing only
// in which artifact of the entry is asked for. A second fetch path would be
// two answers to one question, and they would agree until the day the
// artifact list changed shape.
//
// A TURN'S ANSWER IS NOT ONE ARTIFACT. Since ADR-0040 a turn owns no body:
// its prose is `text` children with seats, and the answer a person copies is
// those joined. The artifacts an ask entry does carry are the provider
// wiretap captures, which is why asking it for one drew the raw request.
import { describe, it, expect, vi } from 'vitest'
import {
  answerTextForEntry,
  answerTextForTurn,
  arrangedByCause,
  blocksForPane,
  bodyForBlock,
  restoredBody,
  type RestorableBlock,
} from './restore-client'
import type { WSClient } from './ipc'

/** A ledger that answers `ledger.get` with one entry's artifact list and
 *  `ledger.artifact` with the bytes of whichever id was asked for. */
function fakeLedger(
  artifacts: Array<{ id: string; mediaType: string; body: string }>,
  kind = 'shell',
  proseEvicted = false,
) {
  const calls: Array<{ method: string; params: unknown }> = []
  const client = {
    call: vi.fn((method: string, params: unknown) => {
      calls.push({ method, params })
      if (method === 'ledger.get') {
        return Promise.resolve({
          entry: { kind },
          artifacts: artifacts.map((a) => ({ id: a.id, mediaType: a.mediaType })),
          caused: [],
          proseEvicted,
        })
      }
      const found = artifacts.find((a) => a.id === (params as { id: string }).id)
      return Promise.resolve({ body: found?.body ?? '' })
    }),
  } as unknown as WSClient
  return { client, calls }
}

/** The SGR body, with its escapes written AS escapes: a literal control byte
 *  in a fixture is a byte the next editor silently eats. */
const SGR_BODY = `\u001b[32mok\u001b[0m`
const ANSWER_TEXT = '## Findings\n- run `ls`\n'

const BOTH = [
  { id: 'art-vt', mediaType: 'application/vt', body: SGR_BODY },
  { id: 'art-txt', mediaType: 'text/plain', body: ANSWER_TEXT },
]

describe('restore-client — one helper, two media types', () => {
  it('a block body is the SGR artifact — the colour is what a block draws', async () => {
    const { client } = fakeLedger(BOTH)
    expect(await bodyForBlock(client, 'entry-1')).toBe(SGR_BODY)
  })

  it("an answer's text is the text/plain artifact, byte for byte", async () => {
    const { client, calls } = fakeLedger(BOTH)
    expect(await answerTextForEntry(client, 'entry-1')).toBe(ANSWER_TEXT)
    // Two round trips and no more: the entry, then the one artifact.
    expect(calls.map((c) => c.method)).toEqual(['ledger.get', 'ledger.artifact'])
    expect(calls[1].params).toEqual({ id: 'art-txt' })
  })

  it('answers null when the entry has no artifact of that type — retention took it', async () => {
    const { client } = fakeLedger([BOTH[0]])
    expect(await answerTextForEntry(client, 'entry-1')).toBeNull()
    const other = fakeLedger([BOTH[1]])
    expect(await bodyForBlock(other.client, 'entry-1')).toBeNull()
  })

  it('answers null when the store cannot be reached at all', async () => {
    const client = {
      call: vi.fn(() => Promise.reject(new Error('socket closed'))),
    } as unknown as WSClient
    expect(await answerTextForEntry(client, 'entry-1')).toBeNull()
    expect(await bodyForBlock(client, 'entry-1')).toBeNull()
  })
})

// ── what a restored entry IS, read from its KIND (nocx-4em1z) ─────────────
//
// The restore path has to pick a block's grammar — a terminal grid must not
// re-wrap, prose must — and the fact it picks from is the entry's own kind,
// never a guess from which artifact survived.
//
// The BODY follows from that: a command draws its `application/vt` grid, with
// the plain copy marked derived from it as the fallback retention leaves. A
// turn draws NOTHING of its own — since ADR-0040 its prose is `text` children
// — so the artifacts on an ask entry are somebody else's business, and asking
// it for a body is how the provider wiretap came back as an answer
// (nocx-3dteo).
describe('restore-client — a block says what it is by its kind, and a turn owns no body', () => {
  it('a terminal body makes it a command block, and the body is the SGR one', async () => {
    const { client } = fakeLedger(BOTH)
    expect(await restoredBody(client, 'entry-1')).toEqual({
      kind: 'command',
      body: SGR_BODY,
      caused: [],
      proseEvicted: false,
    })
  })

  it('a turn draws no body of its own, whatever text/plain it carries', async () => {
    // This used to assert the opposite — that a turn is "drawn from its
    // text" — and it was written before ADR-0040 moved the stored unit into
    // `text` children. What it pinned after that was the wiretap: the only
    // text/plain an ask entry has is the raw provider request and response.
    const { client } = fakeLedger([BOTH[1]], 'ask')
    expect(await restoredBody(client, 'entry-1')).toEqual({
      kind: 'ask',
      body: null,
      caused: [],
      proseEvicted: false,
    })
  })

  // THE DEFECT THIS EPIC'S END-TO-END CHECK FOUND (nocx-dc2fr.8).
  //
  // A command the assistant ran used to be recorded as `kind=agent`, because
  // that column carried WHO submitted it as well as WHAT the row is. Once
  // ADR-0040 took the turn's own artifact away and the grammar started being
  // read from the kind, that command came back drawn as PROSE: reflowing
  // text where a terminal grid belongs, with the grid's alignment gone.
  //
  // So: `shell` decides the grammar and `assistant` says nothing about it.
  // The two columns are read independently or this comes straight back.
  //
  // Asserted HERE rather than through a rendered block, because this is the
  // seam that decides. A test that hands a builder `kind: 'command'` and then
  // reads the kind back off the DOM asserts what the test itself supplied —
  // and mutation-checking is what tells the two apart (AGENTS.md rule 1).
  it('a command the ASSISTANT ran is a command, not prose — the kind decides, never the source', async () => {
    const { client } = fakeLedger([BOTH[0]], 'shell')
    expect(await restoredBody(client, 'entry-1')).toEqual({
      kind: 'command',
      body: SGR_BODY,
      caused: [],
      proseEvicted: false,
    })
  })

  it('a whole agent turn answers with NO artifact and is still an ask (ADR-0040)', async () => {
    // Since ADR-0040 a turn carries no artifact of its own: its prose is
    // `text` children. So an empty artifact list is the ORDINARY shape of
    // a turn, never evidence it was a command — the kind is the entry's.
    const { client } = fakeLedger([], 'ask')
    expect(await restoredBody(client, 'entry-1')).toEqual({
      kind: 'ask',
      body: null,
      caused: [],
      proseEvicted: false,
    })
  })
  it('a turn whose answer is gone is still an ask — the kind does not follow the loss', async () => {
    // Retention takes bodies and leaves entries (ADR-0019 §7). A turn that
    // lost its prose says "no longer kept" through its own notice, and the
    // kind comes from the entry, never from what artifact survived.
    const { client } = fakeLedger([], 'ask', true)
    expect(await restoredBody(client, 'entry-1')).toEqual({
      kind: 'ask',
      body: null,
      caused: [],
      proseEvicted: true,
    })
  })

  it('says nothing about the kind it cannot know when the store is unreachable', async () => {
    const client = {
      call: vi.fn(() => Promise.reject(new Error('socket closed'))),
    } as unknown as WSClient
    expect(await restoredBody(client, 'entry-1')).toEqual({
      kind: 'command',
      body: null,
      caused: [],
      proseEvicted: false,
    })
  })
})

// ── who wrote it, and what is a block at all (nocx-4em1z) ─────────────────
describe('restore-client — the pane read', () => {
  /** A ledger.query answering with one page of entries. */
  function fakeQuery(entries: Array<Record<string, unknown>>) {
    const client = {
      call: vi.fn(() => Promise.resolve({ entries })),
    } as unknown as WSClient
    return client
  }

  const entry = (over: Record<string, unknown>) => ({
    id: 'e1',
    intent: 'ls',
    cwd: '/repo',
    host: null,
    kind: 'shell',
    source: 'user' as const,
    status: 'success',
    durationMs: 10,
    exitCode: 0,
    ...over,
  })
  it("carries the entry's source, so a command the assistant ran keeps its badge", async () => {
    // The defect this split exists to remove: an agent-run command is
    // kind='shell' AND source='assistant', and the badge must read the
    // second half — never be derived from the kind again.
    const client = fakeQuery([
      entry({ id: 'agent-cmd', kind: 'shell', source: 'assistant', intent: 'go test ./...' }),
    ])
    const [block] = await blocksForPane(client, 'pane-1')
    expect(block.author).toBe('agent')
    expect(block.command).toBe('go test ./...')
  })

  it('carries a duration the store never recorded as NULL, never as zero', async () => {
    // `durationMs` is nullable on the wire and null is what an entry nobody
    // timed carries. Zero is a different fact — it is a claim — and the
    // header draws a chip for it. Coercing here is what made a restored turn
    // say '0ms' out loud (nocx-hoeq3).
    const client = fakeQuery([entry({ durationMs: null }), entry({ id: 'e2', durationMs: 0 })])
    const blocks = await blocksForPane(client, 'pane-1')
    expect(blocks.map((b) => b.durationMs)).toEqual([0, null])
  })

  it('a command a person typed is authored by the shell', async () => {
    const client = fakeQuery([entry({})])
    const [block] = await blocksForPane(client, 'pane-1')
    expect(block.author).toBe('shell')
  })

  it('an action is not a block and never becomes one', async () => {
    // A tool call is recorded, and it is an element of a turn's flow rather
    // than a block in the ledger's own words: 'an action has no block and no
    // command line' (command-ledger.ts). Restoring one as a top-level block
    // would put a second owner beside the tool-call line the answer already
    // draws.
    const client = fakeQuery([
      entry({ id: 'act', kind: 'action', intent: 'readScreen' }),
      entry({ id: 'cmd' }),
    ])
    const blocks = await blocksForPane(client, 'pane-1')
    expect(blocks.map((b) => b.entryId)).toEqual(['cmd'])
  })
})

// ── what a turn caused, and where it goes (nocx-h1l4o) ────────────────────
//
// ADR-0039 made a turn one entry and left this: the things a turn caused are
// separate entries, and until the `caused-by` relation existed a restored tab
// had nothing to join them with. `ingest_seq` is commit order and explicitly
// NOT causality (ADR-0019 §2), so the arrangement is read from the relation
// or it is not read at all.
describe('restore-client — the causal flow of a restored turn', () => {
  /** A ledger.get answering with an entry's artifacts AND its causal flow. */
  function fakeGet(artifacts: Array<{ id: string; mediaType: string }>, caused: unknown[]) {
    return {
      call: vi.fn((method: string) => {
        if (method === 'ledger.get')
          return Promise.resolve({
            entry: { kind: 'ask' },
            artifacts,
            caused,
            proseEvicted: false,
          })
        return Promise.resolve({ body: 'the answer' })
      }),
    } as unknown as WSClient
  }

  const CALL = {
    entryId: 'act-1',
    position: 0,
    kind: 'action',
    intent: 'files.read',
    effect: 'observe',
    resource: { kind: 'path', id: '/repo/a.txt' },
  }

  it('comes back with the calls the turn made, as the ledger ordered them', async () => {
    const client = fakeGet(
      [{ id: 'art-txt', mediaType: 'text/plain' }],
      [
        { ...CALL, entryId: 'act-2', position: 1, intent: 'run', effect: 'mutate-destructive' },
        CALL,
      ],
    )
    const restored = await restoredBody(client, 'turn-1')
    expect(restored.kind).toBe('ask')
    // The ORDER is the store's — the wire already sorted by the stored
    // position, and nothing here re-sorts or re-ranks it.
    expect(restored.caused.map((c) => c.intent)).toEqual(['run', 'files.read'])
    expect(restored.caused[1].resource).toEqual({ kind: 'path', id: '/repo/a.txt' })
  })

  it('a turn that caused nothing comes back with an empty flow, not a missing one', async () => {
    const client = fakeGet([{ id: 'art-txt', mediaType: 'text/plain' }], [])
    expect((await restoredBody(client, 'turn-1')).caused).toEqual([])
  })

  it('a store that cannot be asked degrades to no relation at all', async () => {
    const client = {
      call: vi.fn(() => Promise.reject(new Error('socket closed'))),
    } as unknown as WSClient
    expect(await restoredBody(client, 'turn-1')).toEqual({
      kind: 'command',
      body: null,
      caused: [],
      proseEvicted: false,
    })
  })

  it('a wire that carries no causal flow at all is read as none, never as a guess', async () => {
    // The field is required by the contract, so this is the defensive read
    // for a backend that is older than the renderer — and the answer is the
    // degrade, never an invented parent.
    const client = {
      call: vi.fn((method: string) => {
        if (method === 'ledger.get')
          return Promise.resolve({ artifacts: [{ id: 'a', mediaType: 'text/plain' }] })
        return Promise.resolve({ body: 'x' })
      }),
    } as unknown as WSClient
    expect((await restoredBody(client, 'turn-1')).caused).toEqual([])
  })
})

// ── the arrangement, which is the relation's and nothing else's ───────────
describe('restore-client — blocks arranged by the relation', () => {
  const block = (entryId: string) =>
    ({
      entryId,
      command: entryId,
      cwd: '/',
      host: '',
      status: 'success' as const,
      durationMs: 0,
      exitCode: 0,
      author: 'shell' as const,
    }) satisfies RestorableBlock

  const cause = (entryId: string, position: number) => ({
    entryId,
    // The child's seat among its siblings (ADR-0040). It replaces the `at`
    // this fixture used to carry — how far the turn's prose had got when the
    // cause happened — which existed only while a turn's prose was one string
    // to be cut. Irrelevant to the arrangement asserted here either way: this
    // module places a caused block next to its turn.
    position,
    kind: 'shell' as const,
    // A page row here is a command a person ran; the source column is what
    // the fixture must carry now (nocx-dc2fr).
    source: 'user' as const,
    intent: entryId,
    // An ACTION's facts, and null on a shell child: a command a turn ran is
    // not a tool call and asked for nothing.
    args: null,
    effect: null,
    resource: null,
    opensBlock: false,
  })

  it('places a turn’s commands after it, in the causal order it assigned', () => {
    // Ledger order interleaves the two turns' commands; the relation says
    // which belongs to which, and that is the only thing consulted.
    const blocks = [block('turn-a'), block('cmd-b1'), block('turn-b'), block('cmd-a1')]
    const arranged = arrangedByCause(blocks, (id) => {
      if (id === 'turn-a') return [cause('cmd-a1', 0)]
      if (id === 'turn-b') return [cause('cmd-b1', 0)]
      return []
    })
    expect(arranged.map((b) => b.entryId)).toEqual(['turn-a', 'cmd-a1', 'turn-b', 'cmd-b1'])
  })

  it('keeps the causal order of several commands one turn ran', () => {
    const blocks = [block('turn'), block('second'), block('first')]
    const arranged = arrangedByCause(blocks, (id) =>
      id === 'turn' ? [cause('first', 0), cause('second', 1)] : [],
    )
    expect(arranged.map((b) => b.entryId)).toEqual(['turn', 'first', 'second'])
  })

  // ── criterion 4: the three ways the relation is not there ──────────────

  it('with NO relation, the order is plain ledger order and nothing is attached', () => {
    const blocks = [block('turn'), block('cmd')]
    const arranged = arrangedByCause(blocks, () => [])
    expect(arranged.map((b) => b.entryId)).toEqual(['turn', 'cmd'])
  })

  it('an UNREADABLE relation is the same answer as none — never a guessed parent', () => {
    // The read failed, so every entry answers with no causes. The command
    // must NOT be attached to the turn that happens to sit above it.
    const blocks = [block('turn'), block('cmd')]
    const arranged = arrangedByCause(blocks, () => [])
    expect(arranged.map((b) => b.entryId)).toEqual(['turn', 'cmd'])
  })

  it('a DANGLING cause names an entry this page does not hold, and is skipped', () => {
    // Retention evicted it, or it is older than the page limit. The turn
    // keeps its other causes and the page keeps everything it does hold.
    const blocks = [block('turn'), block('kept')]
    const arranged = arrangedByCause(blocks, (id) =>
      id === 'turn' ? [cause('evicted', 0), cause('kept', 1)] : [],
    )
    expect(arranged.map((b) => b.entryId)).toEqual(['turn', 'kept'])
  })

  it('a relation that points in a circle still emits every block exactly once', () => {
    // Not reachable through the store — AddCause assigns one direction —
    // but a reader that could loop on a malformed page would hang the tab,
    // and a hung tab is a worse answer than an odd order.
    const blocks = [block('a'), block('b')]
    const arranged = arrangedByCause(blocks, (id) =>
      id === 'a' ? [cause('b', 0)] : [cause('a', 0)],
    )
    expect(arranged.map((b) => b.entryId).sort()).toEqual(['a', 'b'])
    expect(arranged).toHaveLength(2)
  })
})

// ── a turn owns no body, and the wiretap is not one (nocx-3dteo) ──────────
//
// The provider wiretap (internal/app/assistant_wire_capture.go) hangs the RAW
// chat-completions request and response on the TURN entry as `text/plain`
// artifacts with `captureMethod: 'raw-output'` and no execution. Since
// ADR-0040 the turn owns no body of its own — its prose is `text` children —
// so those captures are the ONLY `text/plain` artifacts an ask entry has, and
// a body picked by media type alone is the system prompt and every tool
// schema, drawn as the answer.
//
// The store orders an entry's own artifacts by artifact id, so WHICH capture
// wins is arbitrary; the fixture carries both for that reason.

const WIRE_REQUEST =
  '{"model":"gpt-5","messages":[{"role":"system","content":"You are the assistant built into nocx"}],' +
  '"tools":[{"type":"function","function":{"name":"files.edit"}}],"stream_options":{"include_usage":true}}'
const WIRE_RESPONSE = 'data: {"choices":[{"delta":{"content":"Вот мои инструменты"}}]}'

const WIRETAP = [
  { id: 'art-wire-1', mediaType: 'text/plain', captureMethod: 'raw-output', body: WIRE_REQUEST },
  { id: 'art-wire-2', mediaType: 'text/plain', captureMethod: 'raw-output', body: WIRE_RESPONSE },
]

/** One prose run of a turn: a `text` child owning its own artifact. */
type ProseChild = { entryId: string; artifactId: string; text: string }

/** A ledger holding ONE turn — its own artifacts, and the `text` children its
 *  prose actually lives in. `ledger.get` answers for the turn and for every
 *  child; `ledger.artifact` answers for any artifact either holds. */
function fakeTurn(
  own: Array<{ id: string; mediaType: string; captureMethod?: string; body: string }>,
  children: ProseChild[] = [],
) {
  const calls: Array<{ method: string; params: unknown }> = []
  const bodies = new Map<string, string>([
    ...own.map((a) => [a.id, a.body] as const),
    ...children.map((c) => [c.artifactId, c.text] as const),
  ])
  const client = {
    call: vi.fn((method: string, params: unknown) => {
      calls.push({ method, params })
      if (method === 'ledger.get') {
        const id = (params as { id: string }).id
        const child = children.find((c) => c.entryId === id)
        if (child) {
          return Promise.resolve({
            entry: { kind: 'text' },
            artifacts: [{ id: child.artifactId, mediaType: 'text/plain', captureMethod: 'none' }],
            caused: [],
            proseEvicted: false,
          })
        }
        return Promise.resolve({
          entry: { kind: 'ask' },
          artifacts: own.map((a) => ({
            id: a.id,
            mediaType: a.mediaType,
            captureMethod: a.captureMethod ?? 'none',
          })),
          caused: children.map((c, i) => ({
            entryId: c.entryId,
            position: i,
            kind: 'text',
            source: 'assistant',
            intent: '',
          })),
          proseEvicted: false,
        })
      }
      return Promise.resolve({ body: bodies.get((params as { id: string }).id) ?? '' })
    }),
  } as unknown as WSClient
  return { client, calls }
}

describe('restore-client — a restored turn draws its children, never the provider wire', () => {
  it('a turn whose only artifacts are wiretap captures restores with NO body', async () => {
    const { client, calls } = fakeTurn(WIRETAP)
    const restored = await restoredBody(client, 'turn-1')
    expect(restored.kind).toBe('ask')
    expect(restored.body).toBeNull()
    // And it did not even fetch one: a turn has no body to ask for.
    expect(calls.map((c) => c.method)).toEqual(['ledger.get'])
  })

  it('a turn that also has prose children still draws none of its own', async () => {
    const { client } = fakeTurn(WIRETAP, [
      { entryId: 'prose-1', artifactId: 'art-p1', text: 'Вот мои инструменты:\n' },
    ])
    expect((await restoredBody(client, 'turn-1')).body).toBeNull()
  })
})

describe('restore-client — copying an answer yields the answer', () => {
  it('assembles the turn’s prose from its text children, in causal order', async () => {
    const { client } = fakeTurn(WIRETAP, [
      { entryId: 'prose-1', artifactId: 'art-p1', text: '## Findings\n' },
      { entryId: 'prose-2', artifactId: 'art-p2', text: '- run `ls`\n' },
    ])
    expect(await answerTextForTurn(client, 'turn-1')).toBe('## Findings\n- run `ls`\n')
  })

  it('never hands back a wiretap capture — the request is not the answer', async () => {
    const { client } = fakeTurn(WIRETAP, [
      { entryId: 'prose-1', artifactId: 'art-p1', text: 'Вот мои инструменты:\n' },
    ])
    const copied = await answerTextForTurn(client, 'turn-1')
    expect(copied).toBe('Вот мои инструменты:\n')
    expect(copied).not.toContain('stream_options')
    expect(copied).not.toContain('You are the assistant built into nocx')
  })

  it('a turn with nothing but wiretap captures refuses, rather than copying them', async () => {
    // Nothing kept means nothing to copy, which the caller SAYS rather than
    // papering over with the wire — or with the painted text.
    const { client } = fakeTurn(WIRETAP)
    expect(await answerTextForTurn(client, 'turn-1')).toBeNull()
  })

  it('a store that cannot be asked refuses rather than inventing a body', async () => {
    const client = {
      call: vi.fn(() => Promise.reject(new Error('socket closed'))),
    } as unknown as WSClient
    expect(await answerTextForTurn(client, 'turn-1')).toBeNull()
  })
})
