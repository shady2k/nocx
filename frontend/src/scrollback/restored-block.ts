// A block built from the STORE rather than from a terminal buffer
// (nocx-cjct0, design §6).
//
// The live path builds a block from IBufferLines it can still read. A
// restored block has no buffer — the process that drew it is gone (D5) — so
// it is built from the entry's facts and the body the capture stored, and it
// goes through the SAME createCommandBlock the live path ends at. One owner
// of what a block looks like; only where the rows come from differs.
//
// ADR-0019 §3: nothing in the UI may imply live resumption. Every block this
// makes carries `data-restored`, which is what the surrounding CSS and any
// action gate read — a restored block offers nothing that needs a process.
import {
  blockKindRules,
  copyToClipboard,
  createCommandBlock,
  type BlockKind,
  type FrozenStatus,
} from './blocks'
import { createAnswerBody } from './answer-body'
import type { CommandAuthor } from '../command-ledger'
import type { CommandSnapshotStore } from '../command-snapshot'
import { attrsToStyle, type TerminalSnapshot } from './serializer'
import { runsFromSGR } from './sgr-read'

/** What a restored block is made of: the entry's facts, and its body. */
export interface RestoredBlockFacts {
  /** The renderer-local block id, minted by the caller like a live one. */
  id: number
  command: string
  cwd: string
  /** `user@host` for a block that ran on a far host, '' for a local one.
   *  Restored from the entry's own environment: a block keeps saying where it
   *  ran even when the pane it is in is local again (design §7). */
  location: string
  /** How long it took, or NULL for no duration chip at all. */
  durationMs: number | null
  exitCode: number | null
  /** Where the block ended — or `settled`, which is finished with no outcome
   *  of its own (nocx-hoeq3). */
  status: FrozenStatus | 'settled'
  /**
   * The stored SGR body, or NULL when there is none to show.
   *
   * Null is a real state and it is not "printed nothing": retention evicts
   * bodies while their entries stay (ADR-0019 §7), so a block whose artifact
   * is gone must say the output is gone. An empty STRING is the other thing —
   * a command that printed nothing — and the two must not render alike.
   */
  body: string | null
  /** WHO submitted it. Restored from the entry's own kind, which is what
   *  that column means — and dropping it is the whole of the badge half of
   *  nocx-4em1z: this call omitted the argument, the parameter defaulted to
   *  'shell', and every restored tab forgot that the assistant had run the
   *  command. The parameter is required now, so the omission cannot come
   *  back silently. */
  author: CommandAuthor
  /** What the block IS, read from the body the entry actually has
   *  (restore-client.ts restoredBody): a terminal body is a command, a
   *  text/plain original with no terminal body beside it is an assistant
   *  turn. It decides the grammar — a grid must not re-wrap and prose must
   *  (nocx-ex636) — and it is the half of nocx-4em1z that brings dialogues
   *  back as the blocks they were. */
  kind: BlockKind
  /** The ledger entry this was built from. Carried into the DOM because the
   *  copy path reads the STORED answer by it (nocx-v13pd) rather than
   *  scraping the painted rows, and a restored turn must copy exactly as a
   *  live one does. */
  entryId?: string
  /**
   * Whether the prose of THIS RUN is no longer kept (ADR-0040's retention
   * rule, ADR-0019 §7). The TURN's notice is gated on this FACT, never on
   * `body === null` alone: a turn that never streamed a word has an empty
   * body and everything is fine (nothing was lost), while a turn whose
   * prose retention took has the same empty body and one sentence to say.
   * Meaningless on a non-ask block.
   */
  proseEvicted?: boolean
}

/** The sentence a block shows where its output used to be. */
const EVICTED = 'Output is no longer kept'

/** Render a stored body as the rows the live path produces: one term-line
 *  per logical row, its runs styled through the serializer's own mapping. */
export function bodyToHTML(snapshot: TerminalSnapshot, body: string): string {
  if (body === '') return ''
  return body
    .split('\n')
    .map((row) => {
      const inner = runsFromSGR(snapshot, row)
        .map((run) => {
          const style = attrsToStyle(snapshot, run.attrs)
          return style ? `<span style="${style}">${run.chars}</span>` : run.chars
        })
        .join('')
      return `<span class="term-line">${inner}</span>`
    })
    .join('')
}

/**
 * Build one restored block.
 *
 * The snapshot is the CURRENT theme's, not one stored with the block: that is
 * the whole reason the durable body keeps colour as SGR (nocx-2f0f). A
 * restored block repaints with everything else when the theme changes.
 */
export function restoredBlock(
  facts: RestoredBlockFacts,
  snapshot: TerminalSnapshot,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
  store: CommandSnapshotStore,
): HTMLElement {
  // A TURN's body is prose and is drawn by the answer body's own renderer —
  // the one the live stream draws through (nocx-4em1z). A `text` CHILD's
  // body is prose too, and it is drawn by the SAME owner, exactly as the
  // live path streams a run of prose through createAnswerBody — one owner
  // of what prose looks like, whichever path produced the block. A
  // COMMAND's body is an SGR grid and is rendered here, from the bytes the
  // capture stored.
  //
  // The EVICTED SENTENCE is the TURN's alone: a run whose prose is gone is
  // reported once, on the turn that owns it (ADR-0040's retention rule) —
  // never one sentence per hole. A text child whose body is null therefore
  // draws an empty block where its rows used to be, and a command whose
  // terminal body was evicted says its own sentence on its own block.
  const proseKind = facts.kind === 'ask' || facts.kind === 'text'
  // A COMMAND's body is an SGR grid — rendered here from the stored bytes,
  // exactly as the live path paints a frozen block (the theme is current,
  // which is why the durable body keeps SGR). A TURN or `text` child has no
  // grid: its prose goes through the answer body's own renderer below.
  const html =
    facts.kind === 'command'
      ? facts.body === null
        ? `<span class="term-line cmd-output-evicted">${EVICTED}</span>`
        : bodyToHTML(snapshot, facts.body)
      : ''
  const el = createCommandBlock(
    facts.kind,
    facts.id,
    facts.command,
    facts.cwd,
    facts.location,
    html,
    facts.durationMs,
    facts.exitCode,
    facts.status,
    getContainer,
    onSelect,
    store,
    facts.author,
  )
  if (facts.entryId !== undefined) el.dataset.entryId = facts.entryId
  el.dataset.restored = 'true'
  // The raw body rides along so a SECOND capture (sandbox conversion
  // re-capture) reads the stored SGR, not the text the paint already
  // stripped of colour. Null means evicted: nothing to carry.
  if (facts.body !== null) el.dataset.sgr = facts.body
  // The EVICTED SENTENCE. A COMMAND says it whenever its body is gone; a
  // TURN says it only when the RUN's prose fact says so — never from
  // `body === null` alone, because a turn that never streamed a word has
  // an empty body and nothing was lost. A `text` child stays silent either
  // way: the run is the unit and the turn says it for all of them.
  const evicted =
    facts.kind === 'ask'
      ? facts.proseEvicted === true
      : facts.body === null && facts.kind !== 'text'
  if (evicted) {
    el.dataset.outputEvicted = 'true'
    const notice = document.createElement('div')
    notice.className =
      facts.kind === 'ask'
        ? 'cmd-output cmd-output-ask cmd-output-evicted'
        : 'cmd-output cmd-output-evicted'
    if (facts.kind === 'ask') notice.dataset.proseEvicted = 'true'
    notice.textContent = EVICTED
    el.appendChild(notice)
  } else if (proseKind && facts.body !== null) {
    // The same body element the live answer builds — the class comes from
    // the kind's rules, which own the wrap policy, so a restored answer
    // or a restored prose child wraps exactly as the live one does.
    const outputEl = document.createElement('div')
    outputEl.className = blockKindRules(facts.kind).outputClass!
    outputEl.dataset.answerBody = ''
    el.appendChild(outputEl)
    const body = createAnswerBody(outputEl, { store, copy: copyToClipboard })
    // The whole chunk in one call: the renderer takes chunks, and a caller
    // that has all of it is simply a caller with one chunk.
    body.append(facts.body)
    body.finish()
  }
  return el
}

/** One child of a restored turn, as the ledger returns it (`ledger.get`'s
 *  `caused` row). This is the WIRE shape narrowed to what placing needs:
 *  the seat order is the store's, and the intent/args/effect/resource
 *  fields are what a child's own block is built from. */
export interface RestoredCause {
  entryId: string
  /** What the child row IS — the ledger's own vocabulary ('ask' is a TURN,
   *  never a child of a turn, and the member is here because the enum
   *  mirrors the store; 'text' is a run of the assistant's prose). */
  kind: 'shell' | 'ask' | 'action' | 'text'
  /** Who submitted the child's content, in the ledger's entries.source
   *  vocabulary — the badge of a child never guesses it from the kind. */
  source: 'user' | 'assistant'
  intent?: string
  args?: Record<string, unknown> | null
  effect?: string | null
  resource?: { kind: string; id: string } | null
  opensBlock?: boolean
}

/** What a restored TURN is made of: the block's own facts, plus the children
 *  the ledger holds for it. `proseEvicted` is inherited from the block
 *  facts — the turn's own notice is the block's, gated on that fact. */
export interface RestoredTurnFacts extends Omit<RestoredBlockFacts, 'id'> {
  /** The turn's children, in the seat order the ledger stored (ADR-0040). */
  causes?: RestoredCause[]
}

/**
 * Build one restored TURN: its block, then the blocks it caused, drawn
 * INSIDE the turn exactly as the live path draws its children — one
 * `.cmd-children` box under the header, each child at its seat (ADR-0040).
 * The turn and its children are ONE block with ONE question.
 *
 * WHAT THE RESTORED TURN DRAWS, AND WHY IT IS THE SAME LIST. Live, the
 * scrollback holds one turn block whose children are the causal sequence —
 * prose, a tool call, a command, prose — in the seats the store gave them.
 * Restored, the page arrived in ledger order and the relation placed it;
 * the drawer places what it is handed, so a restored turn's children are
 * exactly the seats the store recorded. Two paths, one list.
 * `drawCaused` turns one CAUSE into the block to seat. NULL is the
 * DANGLING case and it is deliberate: the entry is older than the page
 * limit, or retention took it. Never a placeholder — a block that is not
 * there is not drawn, and the cost is that child and nothing else.
 */
export function restoredTurn(
  facts: RestoredTurnFacts,
  snapshot: TerminalSnapshot,
  nextId: () => number,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
  store: CommandSnapshotStore,
  drawCaused: (cause: RestoredCause) => HTMLElement | null,
): HTMLElement[] {
  const el = restoredBlock({ ...facts, id: nextId() }, snapshot, getContainer, onSelect, store)
  // The turn's prose, when the run still had it, is drawn by the block
  // builder's own answer-body path; when it is GONE the block says the
  // sentence once, in the same words a command says for its output — the
  // run-level marker (data-prose-evicted) rides that notice, not a second
  // one appended here.
  const children = document.createElement('div')
  children.className = 'cmd-children'
  el.appendChild(children)
  for (const cause of facts.causes ?? []) {
    const child = drawCaused(cause)
    if (child) children.appendChild(child)
  }
  return [el]
}
