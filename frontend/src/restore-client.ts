// Reading a pane's past back (nocx-m3fqk, design §5 and §6).
//
// Two reads, and the split is ADR-0019 §6's: the page carries what a block is
// (command, directory, outcome) and never its bytes, and the body of each
// block is fetched one at a time by the thing that is about to draw it. A
// restore that hauled every body in the page would read a megabyte to paint
// fifty headers.
//
// WHAT THIS DOES NOT DO: decide anything. The order is the ledger's — seq
// DESC, the one total order — and this reverses the page for drawing because
// a person reads their past downwards. Nothing here filters, ranks or
// merges.
import type { WSClient } from './ipc'
import type { LedgerQuery } from './generated/ledger.query'
import type { UnreconciledCause } from './unreconciled-notice'
import type { LedgerArtifact } from './generated/ledger.artifact'
import type { Caused, LedgerGet } from './generated/ledger.get'

/** How many blocks a pane comes back with.
 *
 *  Fifty rather than everything: eight panes at fifty is four hundred blocks
 *  of DOM, and the scrollback a person actually scrolls through is the recent
 *  end of it. Older commands are not lost — they are in recall, which is
 *  where a question about last week belongs. */
const RESTORE_BLOCK_LIMIT = 50

/** One block to draw, as the store knows it. The body is fetched separately;
 *  `null` means there is none to show, which is a hole and not silence. */
export interface RestorableBlock {
  entryId: string
  command: string
  cwd: string
  /** The host the command ran on, '' for the local machine. A block keeps
   *  saying where it ran even when the pane is local again — which is what
   *  makes an inline ssh honest without any code of its own (design §7). */
  host: string
  status: 'success' | 'failure' | 'entered' | 'unknown' | 'unreconciled'
  /** WHY nobody could say whether this block's session still exists, or null
   *  when the question was settled (nocx-k6p18.5). Carried in the wire's own
   *  closed vocabulary — the sentence is chosen in unreconciled-notice.ts,
   *  once, so a backend rewording never changes what a person reads. */
  unreconciled: UnreconciledCause | null
  /** How long it took, or NULL for a time the store never recorded — which
   *  is not the same fact as zero and must not draw the same chip. A turn
   *  whose run predates the close that measures it carries null forever,
   *  and the header answers that with no duration chip at all (nocx-hoeq3).
   *  Coercing it to 0 here is what made a restored turn claim '0ms'. */
  durationMs: number | null
  exitCode: number | null
  /** WHO submitted it, in the block's display vocabulary — mapped from the
   *  entry's OWN source column (entries.source: 'user'|'assistant'), never
   *  guessed from the kind. A command the assistant ran is kind='shell'
   *  with source='assistant', and the badge must say the second half;
   *  deriving it from kind again would repaint the defect this split
   *  exists to remove (nocx-dc2fr). It is what paints the agent badge on a
   *  restored block, and dropping it is why a restored tab used to forget
   *  that the assistant had run the command (nocx-4em1z). */
  author: 'shell' | 'agent'
}

/** The block's display author, from the ledger's source vocabulary. The
 *  badge's language is the InputTarget's ('shell' is the human, 'agent' is
 *  the assistant's lane); the LEDGER's is entries.source. One mapping, at
 *  the restore boundary — the two are never conflated anywhere else. */
function authorFromSource(source: 'user' | 'assistant'): 'shell' | 'agent' {
  return source === 'assistant' ? 'agent' : 'shell'
}

/** The ledger's status vocabulary, narrowed to what a frozen block draws.
 *  A restored entry that never closed is `unknown`, which is exactly what an
 *  abandoned attempt renders as — the block says "this did not finish", and
 *  it did not.
 *
 *  UNLESS NOBODY COULD BE ASKED. Since nocx-k6p18.5 a session can outlive the
 *  coordinator that opened it, so a row still open at restore is no longer
 *  proof that it was abandoned: its host may not have been reachable since the
 *  app restarted. `unreconciled` outranks the status column for exactly that
 *  row, because the column says what was last WRITTEN and the mark says
 *  whether anybody has been able to check it since. Rendering it as `unknown`
 *  would tell a person a running build had finished, which is the lie this
 *  whole change exists to remove — and rendering it as running would be the
 *  same lie from the other end. */
function frozenStatus(
  status: string,
  unreconciled: UnreconciledCause | null,
): RestorableBlock['status'] {
  if (unreconciled !== null) return 'unreconciled'
  switch (status) {
    case 'success':
      return 'success'
    case 'failure':
      return 'failure'
    case 'interrupted':
      return 'unknown'
    default:
      return 'unknown'
  }
}

/**
 * The blocks this pane had, oldest first — the order they are drawn in.
 *
 * THROWS when the store could not be asked, and that is the whole point of
 * not catching here. "This pane had no blocks" and "nobody could tell me"
 * are different answers, and the caller has to act differently on them: the
 * first is final, the second must be tried again when the socket is back
 * (AD-9 — a reconnect is ordinary, and a pane that restored during one would
 * otherwise show an empty past for the rest of the session).
 */
// An ACTION is not a block and never becomes one: the ledger's own words are
// "a third kind and can never be an author: an action has no block and no
// command line" (command-ledger.ts). A tool call is drawn as a line inside
// the turn's flow (nocx-shxv0), so restoring one as a top-level block would
// be a second owner of the same fact.
export async function blocksForPane(client: WSClient, paneId: string): Promise<RestorableBlock[]> {
  const page = await client.call<LedgerQuery>('ledger.query', {
    scope: 'everywhere',
    paneId,
    limit: RESTORE_BLOCK_LIMIT,
  })
  return page.entries
    .filter((e) => e.kind !== 'action')
    .map((e) => ({
      entryId: e.id,
      command: e.intent,
      cwd: e.cwd,
      host: e.host ?? '',
      status: frozenStatus(e.status, e.unreconciled ?? null),
      unreconciled: e.unreconciled ?? null,
      durationMs: e.durationMs,
      exitCode: e.exitCode,
      author: authorFromSource(e.source),
    }))
    .reverse()
}

/**
 * ONE entry's artifact of a given media type, or null when there is none.
 *
 * Two round trips, always the same two: `ledger.get` for the entry (which
 * lists its artifacts) and `ledger.artifact` for the bytes. The media type is
 * the ONLY thing that varies between the two callers below, which is exactly
 * why they share this and do not each own a fetch: a second path would agree
 * with this one until the day the artifact list changed shape.
 *
 * NULL IS TWO DIFFERENT FACTS: retention evicted the artifact, or the store
 * could not be reached. They are collapsed here on purpose — every caller
 * has to say the same thing for both ("this is not here"), and a caller that
 * could tell them apart would still have nothing different to do.
 */
async function artifactBody(
  client: WSClient,
  entryId: string,
  mediaType: string,
): Promise<string | null> {
  try {
    const entry = await client.call<LedgerGet>('ledger.get', { id: entryId })
    const artifact = entry.artifacts.find((a) => a.mediaType === mediaType)
    if (!artifact) return null
    const body = await client.call<LedgerArtifact>('ledger.artifact', { id: artifact.id })
    return body.body
  } catch {
    // Quiet by design: a pane restoring fifty blocks would otherwise log
    // fifty times for one store that is down, and the caller already says
    // once, in the product, that history is unavailable.
    return null
  }
}

/**
 * The body one block printed, or null when there is none to show.
 *
 * The SGR body is what a block DRAWS. The derived text/plain artifact beside
 * it is for search and copy, and drawing that one would silently throw the
 * colour away.
 *
 * Null is rendered as "the output is not here", never as an empty block that
 * reads as a command which printed nothing.
 */
export async function bodyForBlock(client: WSClient, entryId: string): Promise<string | null> {
  return artifactBody(client, entryId, 'application/vt')
}

/**
 * The prose ONE `text` child recorded, or null when it is no longer stored.
 *
 * This is the artifact OpenProse wrote for one run of prose, so it is what the
 * model actually said — not a rendering of it. Reading the record rather than
 * the DOM is nocx-v13pd: the flow consumes the markdown markers it paints, so
 * the DOM is no longer the answer, and text scraped from it would quietly
 * differ from what was stored.
 *
 * A `text` CHILD is the whole domain of this: since ADR-0040 a turn owns no
 * prose artifact, so calling it with a turn's id asks the wrong entry — see
 * answerTextForTurn, which is what a whole answer is assembled by.
 *
 * Null is a refusal the caller must SAY. Falling back to the painted text
 * would be the defect this exists to close, silently.
 */
export async function answerTextForEntry(
  client: WSClient,
  entryId: string,
): Promise<string | null> {
  return artifactBody(client, entryId, 'text/plain')
}

/**
 * A whole turn's answer, assembled from the `text` children its prose lives
 * in — or null when there is none to hand back.
 *
 * ADR-0040 moved the stored unit: a turn is an entry with no body of its own,
 * and each run of prose between two calls is a `text` child with a seat. So
 * the answer a person copies is the concatenation of those children in the
 * causal order the ledger assigned, and there is no single artifact to read.
 *
 * ASKING THE TURN FOR ONE ARTIFACT IS WHAT THIS REPLACES (nocx-3dteo). The
 * only `text/plain` artifacts an ask entry has are the provider wiretap
 * captures — the raw chat-completions request and response, hung on the turn
 * by the wire recorder — so a lookup by media type put the system prompt and
 * every tool schema on the clipboard, and drew them as the answer on restore.
 *
 * Null is the same refusal answerTextForEntry makes, for the same reason: a
 * turn whose prose retention took, and a store that cannot be asked, are one
 * fact to a person — it is not here.
 */
export async function answerTextForTurn(client: WSClient, entryId: string): Promise<string | null> {
  try {
    const entry = await client.call<LedgerGet>('ledger.get', { id: entryId })
    const prose = (entry.caused ?? []).filter((c) => c.kind === 'text')
    if (prose.length === 0) return null
    // The children are independent reads, and the order that matters is the
    // ledger's, not the network's — so they are joined by their position in
    // `caused`, whatever order the answers arrive in.
    const runs = await Promise.all(prose.map((c) => answerTextForEntry(client, c.entryId)))
    const kept = runs.filter((r): r is string => r !== null)
    return kept.length === 0 ? null : kept.join('')
  } catch {
    // Quiet for the reason artifactBody is quiet: the surface that reports a
    // degraded history says it once, in the product.
    return null
  }
}

/** One entry a turn caused, at its position inside the turn — the ledger's
 *  `caused-by` relation, resolved and ordered by the backend (nocx-h1l4o).
 *
 *  This is the generated wire type, re-exported under the name the renderer
 *  uses. Nothing here is derived: the position, the order, the effect and the
 *  resource are all the store's, because a reader that re-derived any of them
 *  would be a second owner of a relation the ledger already owns (AD-8). */
export type RestoredCause = Caused

/** What a restored entry turns out to be, and the body to draw it with.
 *
 *  `kind` is the block's GRAMMAR (blocks.ts BlockKind): a terminal grid must
 *  keep the line breaks the serializer gave it, prose must wrap at the
 *  block's width, and the header of a command is a command line while the
 *  header of a turn is a question. */
export interface RestoredBody {
  kind: 'command' | 'ask'
  body: string | null
  /**
   * Whether the prose of THIS RUN is no longer kept (ADR-0040's retention
   * rule, ADR-0019 §7): retention took the bodies of the turn's `text`
   * children as a unit. It is the ONE place a reader asks that question —
   * the same receipt the store's own LedgerEntry.ProseEvicted reads — and
   * it rides ledger.get because a restored turn has to be able to say the
   * text is gone exactly once, whatever the run was cut into.
   *
   * Meaningful only when `kind` is 'ask'. A command whose own body was
   * evicted says its own sentence on its own block and never sets this.
   */
  proseEvicted: boolean
  /**
   * What this entry CAUSED, in the causal order the turn assigned.
   *
   * EMPTY IS THE DEGRADE AND IT IS THE ONLY ONE: an entry that caused
   * nothing, a store that could not be asked, and a backend that sent no
   * flow at all all answer `[]`, and every one of them draws plain ledger
   * order with the command as an independent agent block. None of them
   * guesses a parent — there is nothing in the page that could be used to
   * guess one that would not be wrong exactly when two things happened at
   * once (ADR-0019 §2).
   */
  caused: RestoredCause[]
}

/**
 * One entry's body, and what that body says the block IS (nocx-4em1z).
 *
 * THE KIND IS READ, NOT INFERRED. Since ADR-0040 the entry's OWN kind is
 * the durable fact: an `agent` entry is a TURN (ask grammar — prose, wrapped
 * by the answer body), a `shell` entry is a COMMAND (a terminal grid, which
 * must not re-wrap). The artifact list decides only what BODY to draw with
 * it: a command's drawn body is the `application/vt` SGR grid (the
 * `text/plain` beside it is openly derived from that one), and a turn's
 * prose lives in its `text` children, so a whole turn answers with NO
 * artifact of its own — an empty list is the ordinary shape, not a guess
 * that it was a command.
 *
 * An entry whose artifact is GONE — retention took it (ADR-0019 §7), or the
 * store could not be asked — keeps its kind and answers `body: null`: the
 * sentence "this output is no longer kept" is the block's, and a turn says
 * it for its prose once, never per hole.
 *
 * ONE round trip pair, shared with bodyForBlock above: the entry, then its
 * artifact. The kind and the causal flow fall out of the first call — which
 * is why the relation cost this path no round trip at all.
 */
export async function restoredBody(client: WSClient, entryId: string): Promise<RestoredBody> {
  try {
    const entry = await client.call<LedgerGet>('ledger.get', { id: entryId })
    const caused = entry.caused ?? []
    const vt = entry.artifacts.find((a) => a.mediaType === 'application/vt')
    const text = entry.artifacts.find((a) => a.mediaType === 'text/plain')
    // What the block DRAWS with: a command's grid is the vt, and its plain
    // copy beside it is the fallback when retention took the grid.
    //
    // A TURN DRAWS WITH NOTHING OF ITS OWN. Since ADR-0040 its prose is
    // `text` children with seats, so an ask entry has no body artifact —
    // and the artifacts it DOES carry are the provider wiretap captures
    // (internal/app/assistant_wire_capture.go: the raw request and the raw
    // response, both text/plain). Picking a turn's body by media type
    // therefore drew the chat-completions request — system prompt, every
    // tool schema, stream_options — above the real prose, on restore only,
    // because the live path never reads artifacts (nocx-3dteo).
    //
    // Not filtered by capture method, but not chosen at all: a filter would
    // still be asking an entry for a body it does not have, and the next
    // artifact hung on a turn would arrive as the next wrong answer.
    const chosen = entry.entry.kind === 'ask' ? undefined : (vt ?? text)
    if (!chosen) {
      // The BLOCK'S KIND is the ENTRY's kind, never a guess from which
      // artifact survived: since ADR-0040 a turn carries NO artifact of its
      // own (its prose is a `text` child), so an empty artifact list is the
      // ordinary shape of a whole turn, not evidence it was a command.
      return {
        kind: entry.entry.kind === 'ask' ? 'ask' : 'command',
        body: null,
        caused,
        proseEvicted: !!entry.proseEvicted,
      }
    }
    const body = await client.call<LedgerArtifact>('ledger.artifact', { id: chosen.id })
    // The kind is the ENTRY's, whatever artifact survived: an ask entry
    // is a turn, everything else is a command. The VT choice above decided
    // which artifact is the BODY to draw with, not what the block is.
    return {
      kind: entry.entry.kind === 'ask' ? 'ask' : 'command',
      body: body.body,
      caused,
      proseEvicted: !!entry.proseEvicted,
    }
  } catch {
    // Quiet for the same reason bodyForBlock is: fifty restoring blocks
    // would otherwise log fifty times for one dead socket, and the pane
    // already says its past could not be read.
    return { kind: 'command', body: null, caused: [], proseEvicted: false }
  }
}

/**
 * The page's blocks in the order the RELATION puts them: each turn followed
 * by the blocks it caused, in the causal order it assigned (nocx-h1l4o).
 *
 * WHAT IT DOES NOT DECIDE, and where that decision lives. This puts a turn's
 * caused blocks next to it, in causal order. WHERE inside the turn each one
 * goes is the drawer's, and since ADR-0040 there is nothing left to work out:
 * a turn CARRIES its children in the seats the store gave them, so the
 * drawer places what it is handed. The drawer consumes the blocks a turn
 * placed rather than drawing them again at their own row.
 *
 * THIS DECIDES NOTHING, which is the same promise the module header makes.
 * The causal order is the store's — `caused` arrives sorted by the position
 * the turn assigned — and everything else keeps the ledger order the page
 * came in. What this does is PLACE: it moves a caused block out of its
 * `ingest_seq` position and next to the turn that caused it, because commit
 * order is explicitly not causality (ADR-0019 §2) and the two disagree
 * precisely when two things happen at once.
 *
 * THE THREE WAYS THE RELATION IS NOT THERE all land here, and all of them
 * produce plain ledger order with the block left where the page put it:
 *
 *  - no relation — `causesOf` answers `[]` for every entry;
 *  - an unreadable one — the read failed, so it answers `[]` too;
 *  - a dangling one — a cause names an entry this page does not hold (older
 *    than the page limit, or evicted by retention), and it is skipped while
 *    the turn keeps every cause that IS here.
 *
 * None of them attaches a block to the turn that happens to sit above it. A
 * guessed parent is wrong exactly when a person is confused and looking.
 */
export function arrangedByCause<T extends { entryId: string }>(
  blocks: T[],
  causesOf: (entryId: string) => RestoredCause[],
): T[] {
  const byID = new Map(blocks.map((b) => [b.entryId, b]))
  // Which blocks are somebody's cause: they are drawn after their turn, so
  // they do not also come out at their own ledger position.
  const claimed = new Set<string>()
  for (const b of blocks) {
    for (const c of causesOf(b.entryId)) {
      if (byID.has(c.entryId) && c.entryId !== b.entryId) claimed.add(c.entryId)
    }
  }
  const out: T[] = []
  const drawn = new Set<string>()
  // `drawn` is what stops a malformed relation from looping. The store
  // cannot produce a cycle — AddCause records one direction and refuses an
  // entry that is not there — but a reader that could hang the tab on one is
  // a worse answer than an odd order.
  const emit = (b: T): void => {
    if (drawn.has(b.entryId)) return
    drawn.add(b.entryId)
    out.push(b)
    for (const c of causesOf(b.entryId)) {
      const caused = byID.get(c.entryId)
      if (caused) emit(caused)
    }
  }
  for (const b of blocks) {
    if (!claimed.has(b.entryId)) emit(b)
  }
  // A block claimed by a turn that is itself NOT in this page would
  // otherwise be dropped from the tab entirely — the relation may cost a
  // block its placement, never its existence.
  for (const b of blocks) emit(b)
  return out
}
