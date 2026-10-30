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
  type AnswerTextSource,
  type BlockKind,
  type DumpSource,
  type FrozenStatus,
  type RunningBlockActions,
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
   *  of its own (nocx-hoeq3), or `unreconciled`, which is NOT an ending at all
   *  (nocx-k6p18.5): the block's session was carried over from a previous
   *  coordinator and nobody could be asked whether it still exists, so the
   *  block is neither running nor finished and the pane's notice says why.
   *  Only a RESTORE can produce that one — the live path has a process to
   *  watch and never needs to wonder. */
  status: FrozenStatus | 'settled' | 'unreconciled'
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
   * body and everything is fine (nothing was lost), while a turn whose prose
   * retention took has the same empty body and one sentence to say.
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
  menuActions?: RunningBlockActions,
  dump?: DumpSource,
  answerText?: AnswerTextSource,
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
    answerText,
    menuActions,
    facts.entryId,
    dump,
  )
  el.dataset.restored = 'true'
  if (facts.kind === 'ask') {
    // Restored turns are already terminal ledger facts. The live close path
    // writes this marker when the stream ends; restore must write the same
    // marker so the finished turn exposes the dump affordance.
    el.dataset.turnState = facts.status === 'settled' ? 'success' : facts.status
  }
  if (facts.entryId) el.dataset.entryId = facts.entryId
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
