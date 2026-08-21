// The shipped providers (design §8.4, §8.5) — command names from the
// OSC 636 snapshot, history over the control plane, local filesystem
// paths, and SSH hosts routed from the quick-connect assembly (host
// candidates are built in host-provider.ts from the shared non-UI module
// quick-connect-assembly.ts — see createShellProviders). Applicability is
// part of the
// contract: a provider declares where it applies and is not consulted
// outside it. In particular the local path provider is inactive on a
// remote session (a local path must never masquerade as a remote one) and
// in command position for a bare word (a bare first word is a command
// name, not a path) — but in argument position it answers ANY token,
// including the empty token, which lists the session cwd (`cd ` + Tab is
// the case the dropdown exists for).
import type { CommandNamesState, CommandSnapshotStore } from '../command-snapshot'
import type { HistoryQuery } from '../generated/history.query'
import type { FsComplete } from '../generated/fs.complete'
import type { ShellComplete } from '../generated/shell.complete'
import type { Candidate } from './candidate'
import type { CompletionToken, TokenPosition } from './token'
import { looksLikePath } from './token'
import { hostProvider } from './host-provider'
import { snippetProvider, type SnippetProviderDeps } from '../snippets/snippet-provider'
import type { ProfileClient } from '../profiles'

/** Per-provider cap on the candidates returned to the merge. */
export const MAX_PROVIDER_CANDIDATES = 20

/**
 * How many history rows argument position may return while the path
 * provider is in play. A history row replaces the whole line; a path
 * candidate replaces one token, and the token being typed is the more
 * specific intent — so wherever paths answer, history must never crowd
 * them out. The owner once counted four directories in his home and got
 * none, because twenty whole-line history rows buried them. Paths answer
 * only on a local session and only under a command that takes filesystem
 * arguments (the NO_FS_CANDIDATES gate fsProvider applies) — under `ssh`,
 * whose argument is a host, the path provider is silent, and history is
 * the only useful thing on offer, so it keeps the provider-wide cap.
 * Command position and remote sessions keep the provider-wide cap too.
 */
export const MAX_HISTORY_IN_ARGUMENT_POSITION = 5

/**
 * Why an applicable provider answered nothing. Zero candidates is a state
 * the product shows, never silence: the controller renders one
 * non-selectable row naming the reason when it is specific, and the generic
 * "no matches" otherwise.
 */
export type EmptyReason =
  /** Nothing matched the prefix — the default when a provider has no better
   *  explanation. */
  | { kind: 'no-match' }
  /** The directory this command completes lists nothing of the kind it
   *  takes (`cd Downloads/` where Downloads holds only files). `dir` is the
   *  display name of the directory, '' when it is the session cwd. */
  | { kind: 'dirs-only-empty'; dir: string }
  /** The directory being listed holds nothing at all. Distinct from
   *  'no-match', which says the prefix matched nothing: here there is no
   *  prefix — the token is empty or ends in `/` — so the listing succeeded
   *  and the folder is simply empty. Telling a user "no matches" when they
   *  typed no prefix blames them for the folder (nocx-azxe.5). `dir` is the
   *  display name, '' when it is the session cwd. */
  | { kind: 'empty-dir'; dir: string }
  /** Command names could not be offered, and WHICH of the five discovery
   *  states we are in is the whole reason this carries a payload. A missing
   *  snapshot used to render as "still loading" whatever had happened, which
   *  is true only while a scan is running: a user whose scan timed out,
   *  failed, or is being served a stale cache was being told to wait for
   *  something that was not coming. `ageMs` is meaningful for `stale` — a
   *  cached set offered without its age is indistinguishable from a current
   *  one — and `reason` carries the backend's own words for a failure. */
  | { kind: 'command-names'; state: CommandNamesState; ageMs: number; reason: string }
  /** The quick-connect alias resolver (`ssh -G`) could not answer — the
   *  degraded condition the picker would surface is routed through, never
   *  rebuilt: hosts cannot be offered, and naming WHY beats the generic
   *  "no matches" (an empty list would read as "you have no hosts"). */
  | { kind: 'hosts-unavailable'; reason: string; detail: string }

/** What one provider answers to one query: candidates plus — when it
 *  answered nothing — the specific reason, so "no matches" is never
 *  indistinguishable from a broken feature. */
export interface SuggestBatch {
  readonly candidates: Candidate[]
  /** Why this provider contributed nothing, when it has a specific reason
   *  to name. Absent = the generic "no matches". */
  readonly emptyReason?: EmptyReason | null
}

/** Everything a provider needs to answer one query. */
export interface SuggestContext {
  /** The whole document line. */
  readonly doc: string
  /** The word being completed ('' at a boundary). */
  readonly token: CompletionToken
  /** Where the token sits — command vs argument position. */
  readonly position: 'command' | 'argument'
  /** The tab's session is a local shell (the path provider's hard gate). */
  readonly isLocal: boolean
  /** The session's working directory, '' when unknown (no OSC 7 yet). */
  readonly cwd: string
  /** The session host, '' for the local machine (matches history rows). */
  readonly host: string
}

export interface SuggestionProvider {
  readonly id: string
  readonly targetId: string
  /** Declared applicability: not consulted outside it (design §8.5). */
  applicable(ctx: SuggestContext): boolean
  /** May resolve synchronously — the in-memory command provider has nothing
   *  to await, and a microtask per keystroke is a waste. */
  suggest(ctx: SuggestContext, signal: AbortSignal): Promise<SuggestBatch> | SuggestBatch
}

// ── command: the OSC 636 snapshot ────────────────────────────────────────
//
// The snapshot is the running shell's own answer (command-snapshot.ts), so it
// is correct on a remote host too — which is exactly what the path provider
// is not. Applicable only in command position, and only for a bare word: a
// token containing a slash is a path invocation (`./run.sh`), which the path
// provider owns.
export function commandProvider(store: CommandSnapshotStore): SuggestionProvider {
  return {
    id: 'command',
    targetId: 'shell',
    applicable: (ctx) => ctx.position === 'command' && !ctx.token.text.includes('/'),
    suggest(ctx) {
      const q = ctx.token.text
      if (q === '') return { candidates: [] }
      // Offer whatever has arrived, and name the shared half's state when
      // there is nothing to offer. The two halves land by different routes —
      // the shell's own tables over OSC 636, the target's PATH over
      // shell.commandNames — so "no candidates" has five different causes and
      // exactly one of them is "wait a moment". Reporting the wrong one is
      // the defect this replaces.
      const names = store.matching(q).slice(0, MAX_PROVIDER_CANDIDATES)
      if (names.length === 0) {
        return {
          candidates: [],
          emptyReason: {
            kind: 'command-names',
            state: store.commandNamesState,
            ageMs: store.commandNamesAgeMs,
            reason: store.commandNamesReason,
          },
        }
      }
      return {
        candidates: names.map((name): Candidate => ({
          id: `cmd:${name}`,
          targetId: 'shell',
          providerId: 'command',
          displayText: name,
          insertText: name,
          replacement: { from: ctx.token.from, to: ctx.token.to },
          matchRanges: [{ from: 0, to: q.length }],
          source: 'command',
          eligibleForGhostText: true,
        })),
      }
    },
  }
}

// ── history: the control plane's history.query ───────────────────────────
//
// Completes the whole line (history-beginning-search semantics): a history
// entry whose command starts with the line replaces the entire line. Rows are
// environment-scoped by the store (the directory rung); the same command
// arriving twice dedups by id, keeping the newest.
/** The last whitespace-delimited word of a whole-line candidate — the
 *  argument the command acts on. '' when the line ends in whitespace. */
function trailingToken(line: string): string {
  const m = line.trimEnd().match(/(\S+)$/)
  return m ? m[1] : ''
}

/** Whether a history row's trailing token is worth an existence check: in
 *  argument position any non-option word is the file the command acts on
 *  (the surface already treats argument words as paths — the fs provider
 *  answers ANY argument token); in command position only a path form
 *  (`./run.sh`, `~/x`, `/etc/hosts`). '' means no check. */
function checkableTrailingToken(line: string, position: TokenPosition): string {
  const t = trailingToken(line)
  if (t === '' || t.startsWith('-')) return ''
  if (position === 'argument') return t
  return looksLikePath(t) ? t : ''
}

export function historyProvider(opts: {
  query: (cwd: string, host: string) => Promise<HistoryQuery>
  /** The fs.complete seam — the same backend call the path provider makes.
   *  Checks whether a history row's trailing token still exists, so a row
   *  whose file is gone is DEMOTED — never hidden (re-running a command to
   *  see it fail is legitimate). Absent on a remote session: the backend's
   *  filesystem is not the session's, and no call may be made. */
  completeFs?: (text: string, cwd: string) => Promise<FsComplete>
}): SuggestionProvider {
  // The existence cache: one fs.complete call per (cwd, trailing token),
  // for the life of the open list. A query whose document extends the
  // previous query's is the same interaction (the user typed more); a
  // document that does not is a new interaction, and the cache resets so a
  // file created or deleted since can change the verdict.
  const exists = new Map<string, boolean>()
  let lastDoc: string | null = null
  return {
    id: 'history',
    targetId: 'shell',
    // Applicable whenever there is a line to complete, even with a trailing
    // space (`git ` + Tab can complete the line to `git status`).
    applicable: (ctx) => ctx.doc.trim() !== '',
    async suggest(ctx, signal) {
      const line = ctx.doc
      if (line === '') return { candidates: [] }
      // In argument position where the path provider answers — a local
      // session under a command that takes filesystem arguments, the same
      // NO_FS_CANDIDATES gate fsProvider applies — history is a sidebar,
      // capped so a flood of whole-line rows can never bury the paths.
      // Where paths do not answer (command position, remote sessions, and
      // a local `ssh`, whose argument is a host, not a path) history keeps
      // the provider-wide cap: capping it there would be losing rows, not
      // making room.
      const cap =
        ctx.position === 'argument' && ctx.isLocal && !NO_FS_CANDIDATES[commandWord(ctx)]
          ? MAX_HISTORY_IN_ARGUMENT_POSITION
          : MAX_PROVIDER_CANDIDATES
      const result = await opts.query(ctx.cwd, ctx.host)
      if (signal.aborted) return { candidates: [] }
      const seen = new Set<string>()
      const out: Candidate[] = []
      for (const e of result.entries) {
        // Newest first on the wire; keep the first (newest) of a duplicate.
        if (!e.command.startsWith(line) || seen.has(e.command)) continue
        seen.add(e.command)
        out.push({
          id: `hist:${e.command}`,
          targetId: 'shell',
          providerId: 'history',
          displayText: e.command,
          insertText: e.command,
          replacement: { from: 0, to: line.length },
          matchRanges: [{ from: 0, to: line.length }],
          source: 'history',
          scope: 'directory',
          freshness: e.endedAt ?? undefined,
          // A row still running has no final outcome — never invent one.
          outcome: e.status === 'running' ? undefined : { status: e.status },
          environment: { cwd: e.cwd, host: e.host, confidence: 'asserted' },
          eligibleForGhostText: true,
        })
        if (out.length >= cap) break
      }
      // A history row whose trailing token is a path that no longer exists
      // is demoted — never dropped — so it ranks last (the owner's "I
      // deleted the file and this suggestion looks strange"). The check is
      // the fs.complete seam (one call per token, cached for the life of
      // the open list) and is skipped entirely on a remote session, where
      // the backend's filesystem is not the session's and "exists" cannot
      // be known. An exact entry-name match is existence; the backend
      // answers soft-empty for a missing directory, so any other answer
      // demotes.
      if (ctx.isLocal && opts.completeFs) {
        if (lastDoc !== null && !ctx.doc.startsWith(lastDoc)) exists.clear()
        lastDoc = ctx.doc
        for (const c of out) {
          const token = checkableTrailingToken(c.insertText, ctx.position)
          if (token === '') continue
          const key = `${ctx.cwd}\u0000${token}`
          let missing = exists.get(key)
          if (missing === undefined) {
            const result = await opts.completeFs(token, ctx.cwd)
            missing = !result.entries.some((e) => e.name === token)
            exists.set(key, missing)
          }
          if (missing) c.stalePath = true
        }
      }
      return { candidates: out }
    },
  }
}

// ── fs: local filesystem paths ───────────────────────────────────────────
//
// The backend (fs.complete) resolves the partial path and lists the directory
// it points at. Applicable only when the session is local — the backend's
// filesystem IS the local machine's, and inside an SSH session that answer
// would be a local path masquerading as a remote one. In argument position it
// answers ANY token, including the empty token (which lists the session cwd);
// in command position only a path invocation (`./run.sh`) — a bare first word
// is a command name and the command provider owns it.

/**
 * Commands whose argument is a directory, not a file — their path candidates
 * are filtered to directories only. The default for anything not listed
 * here, including a command this table has never heard of, is "both": the
 * rule is a promise about the command's argument, and for an unknown command
 * we can promise nothing. Grows by addition.
 */
const DIRECTORIES_ONLY: Record<string, true> = { cd: true, pushd: true, rmdir: true }

/**
 * Commands that take NO filesystem candidates at all — the argument is a
 * destination in another namespace, and offering the local tree would be a
 * category error. `ssh`'s argument is a host (the host provider owns it);
 * offering Downloads/ above the host row is the bug this table kills. Grows
 * by addition, like DIRECTORIES_ONLY — the default for an unknown command
 * stays "both kinds".
 */
const NO_FS_CANDIDATES: Record<string, true> = { ssh: true }

/** The command word the token completes under ('' in command position — a
 *  path invocation like `./run.sh` is not a command the table knows, so the
 *  default "both" applies). Also gates the host provider: hosts are offered
 *  only under the `ssh` command, and this is the same derivation the path
 *  provider uses for its dirs-only table. */
export function commandWord(ctx: SuggestContext): string {
  if (ctx.position === 'command') return ''
  return ctx.doc.slice(0, ctx.token.from).trim().split(/\s+/)[0] ?? ''
}

/** The display name of the directory a token's path prefix lists — '' when
 *  the listing is the session cwd itself (an empty token, `./` or `../`),
 *  which the empty row then names as "this folder". */
function listedDirName(ctx: SuggestContext): string {
  const t = ctx.token.text
  const prefix = t.slice(0, t.lastIndexOf('/') + 1)
  const name = prefix.replace(/\/+$/, '').split('/').pop() ?? ''
  return name === '.' || name === '..' || name === '' ? '' : name
}

export function fsProvider(opts: {
  complete: (text: string, cwd: string) => Promise<FsComplete>
}): SuggestionProvider {
  return {
    id: 'fs',
    targetId: 'shell',
    applicable: (ctx) => {
      if (!ctx.isLocal) return false
      if (ctx.position === 'argument') return !NO_FS_CANDIDATES[commandWord(ctx)]
      // Command position: only a path invocation (`./run.sh`) — never a
      // bare word.
      return looksLikePath(ctx.token.text)
    },
    async suggest(ctx, signal) {
      // The wire refuses empty text (the handler treats it as "no
      // completion"), so an empty token asks for the session cwd by name:
      // `./` lists it, and the display below keys off the REAL token, so
      // rows show bare names — never a `./` the user did not type.
      const q = ctx.token.text === '' ? './' : ctx.token.text
      const result = await opts.complete(q, ctx.cwd)
      if (signal.aborted) return { candidates: [] }
      // The part of the token the user has already typed up to the last
      // slash — display and insert both carry it, so accepting a candidate
      // never loses the directory the user already wrote.
      const t = ctx.token.text
      const lastSlash = t.lastIndexOf('/')
      const tokenPrefix = t.slice(0, lastSlash + 1)
      // `cd`, `pushd` and `rmdir` take directories only; the default for
      // everything else (including unknown commands) is both kinds.
      const directoriesOnly = DIRECTORIES_ONLY[commandWord(ctx)] === true
      const rows = result.entries.filter((e) => !directoriesOnly || e.isDir)
      if (rows.length === 0) {
        // The query answered nothing — but WHY matters: a directory that
        // holds only files is a different message from "nothing matches",
        // and a dirs-only command is the case the dropdown exists for.
        if (directoriesOnly && result.entries.length > 0) {
          return {
            candidates: [],
            emptyReason: { kind: 'dirs-only-empty', dir: listedDirName(ctx) },
          }
        }
        // Nothing in the directory at all. Whether that is the user's doing
        // depends on whether they typed a prefix: with one, nothing matched
        // it; without one — an empty token, or a token ending in `/` — the
        // listing succeeded and the folder is empty. Tab completing into an
        // empty folder is the common way to arrive here, and "No matches"
        // reads there as if the completion had failed when it had just
        // succeeded (nocx-azxe.5).
        const t = ctx.token.text
        const typedPrefix = t.slice(t.lastIndexOf('/') + 1)
        if (typedPrefix === '') {
          return {
            candidates: [],
            emptyReason: { kind: 'empty-dir', dir: listedDirName(ctx) },
          }
        }
        return { candidates: [] }
      }
      return {
        candidates: rows.slice(0, MAX_PROVIDER_CANDIDATES).map((e): Candidate => {
          // Report 3 — the display shows the LAST SEGMENT only: the typed
          // prefix is already in the line, and repeating it in every row is
          // noise that also forces the panel wider than it needs to be.
          // `insertText` and the replacement range are UNCHANGED — this is
          // displayText only, which is exactly why it is a separate field.
          //
          // Unambiguity: every row of one listing shares one parent, and
          // that parent is what the user already wrote in the line; the
          // candidate id is the FULL resolved path (`fs:<path>`), so two
          // rows from different parents can never read alike — a merge
          // dedups by id, and a history row keeps its whole-line display.
          const display = e.name + (e.isDir ? '/' : '')
          // The typed part of the SEGMENT (after the last slash) is what
          // the match marks inside the segment; a trailing slash means the
          // segment is complete and nothing is highlighted.
          const segPrefix = t.slice(lastSlash + 1)
          const matchTo = Math.min(segPrefix.length, e.name.length)
          return {
            id: `fs:${e.path}`,
            targetId: 'shell',
            providerId: 'fs',
            displayText: display,
            // insertText is UNCHANGED by report 3: what a pick inserts is
            // the full path the user wrote (the typed prefix + the segment).
            insertText: tokenPrefix + display,
            replacement: { from: ctx.token.from, to: ctx.token.to },
            matchRanges: matchTo > 0 ? [{ from: 0, to: matchTo }] : [],
            source: 'path',
            kind: e.isDir ? 'directory' : 'file',
            environment: { cwd: ctx.cwd, confidence: 'asserted' },
            eligibleForGhostText: true,
          }
        }),
      }
    },
  }
}

// ── shell: the remote completion adapter (nocx-w7h.15) ────────────────────
//
// The adapter asks the remote shell's own completion machinery — compgen for
// paths and commands, bash completion functions for command-specific answers
// like `git checkout`. This is what makes the mechanism worth building:
// paths alone could have been faked. The candidate's source names the
// adapter so the UI can distinguish an adapter answer from a local guess.
//
// Applicable only on a remote session — on a local session the fs provider
// answers identically from the backend's own filesystem. The adapter REPLACES
// the DIRECTORIES_ONLY table: the shell's compgen -d / compgen -f already
// answer the right kind, so the table is not consulted and must not be
// re-added.
export function shellCompleteProvider(opts: {
  complete: (params: {
    sessionId: string
    cwd: string
    line: string
    pos: number
  }) => Promise<ShellComplete>
  /** The session ID of the tab this provider answers for. */
  sessionId: () => string
}): SuggestionProvider {
  return {
    id: 'shell',
    targetId: 'shell',
    applicable: (ctx) => {
      // The adapter is active on remote sessions only — on a local
      // session the fs provider answers identically.
      if (ctx.isLocal) return false
      // Also active in command position for a bare word: the shell's
      // compgen -c answers command names on the remote host.
      if (ctx.position === 'command') return true
      // In argument position: always active — the shell answers paths
      // and command-specific completions.
      return true
    },
    async suggest(ctx, signal) {
      const sid = opts.sessionId()
      if (!sid) return { candidates: [] }

      const pos = ctx.token.to // caret position

      const result = await opts.complete({
        sessionId: sid,
        cwd: ctx.cwd,
        line: ctx.doc,
        pos,
      })
      if (signal.aborted) return { candidates: [] }

      if (result.entries.length === 0) {
        // The adapter named a specific reason — surface it rather than
        // the generic "no matches".
        if (result.reason) {
          // Map specific adapter reasons to the EmptyReason vocabulary.
          if (result.reason === 'cancelled') return { candidates: [] }
          // Surface the adapter's reason as the honest empty message.
          return {
            candidates: [],
            emptyReason: {
              kind: 'hosts-unavailable',
              reason: 'shell completion',
              detail: result.reason,
            },
          }
        }
        return { candidates: [] }
      }

      // The part of the token the user has already typed up to the last
      // slash — same display logic as fsProvider.
      const t = ctx.token.text
      const lastSlash = t.lastIndexOf('/')
      const tokenPrefix = t.slice(0, lastSlash + 1)

      return {
        candidates: result.entries.slice(0, MAX_PROVIDER_CANDIDATES).map((e): Candidate => {
          const display = e.isDir ? e.name + '/' : e.name
          const segPrefix = t.slice(lastSlash + 1)
          const matchTo = Math.min(segPrefix.length, e.name.length)
          return {
            id: `${e.source}:${e.path ?? e.name}`,
            targetId: 'shell',
            providerId: 'shell',
            displayText: display,
            insertText: e.source === 'path' ? tokenPrefix + display : display,
            replacement: { from: ctx.token.from, to: ctx.token.to },
            matchRanges: matchTo > 0 ? [{ from: 0, to: matchTo }] : [],
            source: e.source === 'command' ? 'command' : 'path',
            kind: e.isDir ? 'directory' : undefined,
            environment: { cwd: ctx.cwd, host: ctx.host, confidence: 'asserted' },
            eligibleForGhostText: true,
          }
        }),
      }
    },
  }
}

/**
 * The shell target's provider set, wired at the composition root. The host
 * provider is built HERE: it needs a ProfileClient, and the assembly it
 * routes (quick-connect-assembly.ts) is plain non-UI code, so providers.ts
 * can import it — the DOM-bound quick-connect module never enters this
 * chain. Registered ABOVE history: in `ssh` argument position hosts must
 * outrank whole-line history rows (the rank rung enforces it; registration
 * order only breaks the exact ties).
 */
export function createShellProviders(opts: {
  store: CommandSnapshotStore
  queryHistory: (cwd: string, host: string) => Promise<HistoryQuery>
  completeFs: (text: string, cwd: string) => Promise<FsComplete>
  /** The shell.complete seam — the remote completion adapter (nocx-w7h.15).
   *  Absent when no adapter is wired (tests, raw contexts). */
  completeShell?: (params: {
    sessionId: string
    cwd: string
    line: string
    pos: number
  }) => Promise<ShellComplete>
  /** The session ID of the tab — only needed when completeShell is present. */
  sessionId?: () => string
  /** Present when a ProfileClient is wired (the app); absent in tests and
   *  raw-mode contexts where no connection manager exists — a provider that
   *  can never answer is not registered. */
  profileClient?: ProfileClient
  /** The snippet library seam (design §10.2). Absent where no snippets
   *  service is wired — a provider that can never answer is not
   *  registered, the same rule the host provider follows. */
  snippets?: SnippetProviderDeps
}): SuggestionProvider[] {
  return [
    commandProvider(opts.store),
    // Snippets answer in command position beside command names, ranked
    // below them: a saved phrase never takes an executable's row (§10.2).
    ...(opts.snippets ? [snippetProvider(opts.snippets)] : []),
    ...(opts.profileClient ? [hostProvider({ profileClient: opts.profileClient })] : []),
    // The adapter (nocx-w7h.15) answers remote paths and command-specific
    // completions — registered ABOVE fsProvider so it outranks the local
    // path provider on remote sessions (its applicability gate ensures it
    // is only consulted there). The stale-path check in historyProvider
    // is skipped on remote sessions (ctx.isLocal gate), so no double-call.
    ...(opts.completeShell && opts.sessionId
      ? [shellCompleteProvider({ complete: opts.completeShell, sessionId: opts.sessionId })]
      : []),
    // The fs.complete seam travels to history too: the stale-path demotion
    // (a history row whose trailing token no longer exists ranks last)
    // uses the same backend call the path provider makes.
    historyProvider({ query: opts.queryHistory, completeFs: opts.completeFs }),
    fsProvider({ complete: opts.completeFs }),
  ]
}
