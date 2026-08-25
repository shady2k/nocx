// ApiStore — the one list every part of the workbench reads (the Files and
// Git store pattern): the open collections, the request in the form, and the
// runs. Plain Solid signals; nothing here renders, and no method is called
// during a render.
//
// THAT RULE STANDS, AND ONE CALL IS IN BREACH OF IT TODAY. `importCurl`
// raises the kit's `showConfirm` before it discards unsaved work
// (nocx-86wvw), so this file imports a UI module and puts a dialog on a
// screen. It is not a new rule replacing the old one, and it should not be
// read as a precedent: the surface's own grammar is the other way round —
// api-pane.tsx raises the confirmation for a delete and the store just
// deletes — and owning "is there unsaved work" does not make this file the
// owner of "ask the person". Those are two questions and the second one
// belongs where the other asks in this surface already are.
//
// It is here because the worker who fixed the defect did not own
// api-pane.tsx that wave, which is a scheduling fact and not an argument.
// The call site says what has to move and what moving it must preserve; the
// bill, meanwhile, is in api-store.test.ts, which carries a
// `vi.mock('../ui/dialog')` that no test of a store with nothing to render
// would ever need.
//
// Three rules, each with the thing it stops:
//
// 1. SEND WRITES FIRST, BECAUSE THE FILE IS WHAT GETS SENT. `api.request.send`
//    takes a handle and a path — never a request value — so what goes out is
//    what is on disk. A form able to send something the file does not contain
//    would be a second truth beside the one design §6.4 names, and the two
//    would agree until the day they did not. The write happens only when the
//    draft actually differs from what was read: pressing Send on an untouched
//    request must not rewrite the file, because a git diff a person did not
//    cause is how the "shareable through git" claim stops being true.
//    A write that FAILS stops the send: the file never held what would have
//    gone out, so sending would put the OLD request on the wire under the new
//    request's name.
//
// 2. A RUN EXISTS FROM THE MOMENT SEND IS PRESSED, and it is FILLED IN when
//    the exchange settles rather than replaced. The row used to be built
//    from the RESULT, so it could not exist before the answer did: there was
//    nothing on screen while a request was in flight, nothing to name a
//    running exchange by, and a failure was not a run at all — it arrived as
//    a JSON-RPC error. Now the same row (the same id) is appended pending
//    and settled in place, so a person watches one thing happen instead of
//    watching nothing and then seeing something appear.
//
//    A FAILED SEND IS STILL A RUN, and now it is a run with the request text,
//    the route, how far it got and which phase it stopped at — the same
//    detail an answered run gets, minus what never came back.
//
// 3. ONE LIST OF COLLECTIONS, WHICHEVER DOOR THE FOLDER CAME THROUGH.
//    `api.collections.open` answers a handle plus a collection, and so does
//    `api.collections.create`; `api.collections.list` answers rows. The
//    adopters in api-model.ts put the first two into the third's shape, so
//    nothing downstream has to ask which call produced the row it is looking
//    at — a collection a person has just made and one they opened an hour ago
//    are the same row, and only the row knows the difference.
//
// The pretty/raw choice belongs to ONE run. A single flag for the list would
// mean opening the raw text of the run you are reading also opens it for the
// nineteen above it.

import { createSignal, untrack } from 'solid-js'
import { showConfirm } from '../ui/dialog'
import type { ApiConnection, ApiWorkbenchServices, ImportSource } from './api-client'
import type { ApiImportCurlResult } from '../generated/api.import.curl'
import type { ApiRequestScopeResult } from '../generated/api.request.scope'
import type { FilesChanged } from '../generated/files.changed'
import {
  adoptCreatedCollection,
  adoptFolderCollection,
  adoptImportedRequest,
  adoptOpenedCollection,
  type ApiCertificate,
  type ApiEnvironment,
  type ApiEnvironmentRef,
  type ApiFailure,
  type ApiImportNote,
  type ApiOpenCollection,
  type ApiParam,
  type ApiRaw,
  type ApiRequest,
  type ApiResponse,
  type ApiSendResult,
  type ApiSentRoute,
  type ApiTimings,
} from './api-model'
import { proposedRequestName, slugify } from './api-paths'
// The one owner of "where does this path live" (AD-8). This file had a
// second copy of it, three lines long and identical, until a third caller
// arrived — and two derivations of one concept agree everywhere anybody
// looks until the day they do not.
import { directoryOf } from './api-tree'
import { foldQueryIntoParams } from './api-url'
import type { Unsupported as PostmanNote } from '../generated/api.import.postman'

/** WHICH PART of an exchange is being read. Three, not two: the headers
 *  were stacked above the body in one pane, so a long body pushed them off
 *  screen and a long header list pushed the body off. They are what a person
 *  looks at one at a time. */
export type ApiRunView = 'body' | 'headers' | 'raw'

/**
 * HOW A RUN ENDED, or that it has not.
 *
 * The wire's three plus two only this side can have, spelt as that union so
 * an outcome added to the schema arrives here rather than being restated.
 *
 * `pending` is the one the backend
 * cannot report, because it is true exactly while there is no answer to
 * report anything with. It is the state this whole change exists to make
 * representable — before it, a run and its answer were the same object, so
 * "in flight" had nowhere to live and the only signal was a disabled button.
 *
 * `refused` is the other one the wire does not send, and it is a different
 * fact from `failed`: the method REFUSED the ask — an unknown handle, an
 * auth variable nothing can resolve — so no exchange was ever attempted and
 * there is no request text, no route and no phase. A run that showed a
 * refusal as a failed exchange would be claiming an attempt that never
 * happened.
 */
type ApiRunOutcome = ApiSendResult['outcome'] | 'pending' | 'refused'

/**
 * One exchange, as the list holds it — from the moment Send is pressed.
 *
 * The fields are filled in as facts arrive: a pending run has the method,
 * the URL and the environment it was sent under, and everything else is null
 * or empty until the exchange settles. The row is never replaced, so its id
 * is stable from the first render to the last and nothing renumbers under a
 * person who is reading it.
 */
export interface ApiRun {
  /**
   * WHICH REQUEST this run belongs to — the collection handle and the path
   * within it, the same pair that addresses the file.
   *
   * The list used to be the workbench's, flat: every send anybody made, in
   * one column, whichever request was on screen. So opening another request
   * showed it somebody else's answers, and deleting a request left its runs
   * behind with nothing to explain what they were replies to. A run is an
   * answer to one question and it is kept beside that question.
   */
  readonly handle: string
  readonly relPath: string
  readonly id: number
  /**
   * The name this surface gave the exchange, which is what Stop names.
   *
   * It is ours rather than the JSON-RPC id: the dispatcher mints one per
   * call and never hands it to the caller, so a button that wanted to name a
   * request would have needed a second addressing scheme over the same thing
   * (api-client.ts). It stays on the row after the run settles, because the
   * row is the record of what happened and the token is part of it.
   */
  readonly token: string
  /** The method and URL as the FORM had them when Send was pressed — what
   *  the person asked for. Kept on the run so scrolling back through twenty
   *  of them does not require remembering what the form held at the time. */
  readonly method: string
  readonly url: string
  /** How it ended, or that it has not yet. */
  readonly outcome: ApiRunOutcome
  /** The NAME of the environment this exchange went out under, in the
   *  BACKEND's words — read off the send result rather than off what the
   *  form was pointed at, because the renderer names an environment by its
   *  path and the name lives inside the file. '' is a send that named none,
   *  and '' is also a run that has not come back yet: nothing has said which
   *  record answered, and the panel's own choice is not written here
   *  instead — that would be the guess this field exists to avoid.
   *
   *  A run recording what the renderer BELIEVED it asked for would be
   *  `vault.status.defaultProvider` in reverse: a value one side writes and
   *  the other never reads back. */
  readonly environment: string
  /** How it went out (§6.5) — the backend's account, never the panel's. */
  readonly route: ApiSentRoute
  /**
   * WHAT WENT OUT, segmented — present whatever the outcome, because the
   * sender composes it before it dials.
   *
   * It is null only while the run is pending and on a run that was refused
   * before it became an exchange. Before this change it was reachable only
   * through `response`, so the runs that most needed it — the ones that
   * never got a response — were exactly the ones that did not have it.
   */
  readonly request: ApiRaw | null
  /** What answered the dial, '' when nothing did or nothing has yet. */
  readonly remoteAddr: string
  /** What the resolver answered for the host, in its own order. Empty when
   *  there was no lookup to make, when it failed, and while pending. */
  readonly dnsAddresses: readonly string[]
  /** The phases as far as the attempt got, or null while pending. */
  readonly timings: ApiTimings | null
  /** The chain the server presented. Never null once the run has settled. */
  readonly certificates: readonly ApiCertificate[]
  /** What came back — null unless the outcome is `answered`. */
  readonly response: ApiResponse | null
  /** Why it ended without an answer — null while pending, on an answered
   *  run, and on a refusal, which never became an exchange. */
  readonly failure: ApiFailure | null
  /** Why the method refused the ask, for the one outcome that is not an
   *  exchange at all. '' otherwise. */
  readonly error: string
  /** The renderer's own clock at the moment Send was pressed, in
   *  milliseconds. It is what the pending row counts up from, and it is
   *  CLIENT-side deliberately: the backend reports timings for an exchange
   *  that has ended, and this is the one number that exists while it has
   *  not. */
  readonly startedAt: number
  readonly view: ApiRunView
}

/** Which request file the form is showing, or null when the draft came from
 *  an import and has no file behind it yet. */
interface ApiSelection {
  readonly handle: string
  readonly relPath: string
}

/**
 * Who answers a variable, and with what, from the backend-computed scope.
 *
 * `value` is null for the vault and for names with no answer. `from` carries
 * the folder path only for a folder answer; the renderer never derives
 * precedence or reads a second source.
 */
export interface VariableAnswer {
  readonly scope: 'request' | 'folder' | 'environment' | 'secret' | 'none' | 'unknown'
  readonly value: string | null
  readonly from: string | null
}
interface ApiFolderVariablesResult {
  readonly variables: readonly ApiParam[] | null
  readonly error: string
}

export type ApiScopeVariable = ApiRequestScopeResult['variables'][number]

export interface ApiStore {
  collections(): readonly ApiOpenCollection[]
  /** The handle of the collection the workbench is pointed at — the one just
   *  made, the one just opened, or the one holding the request in the form.
   *  '' when there is none.
   *
   *  Deliberately not `selected()`, which answers a different question: that
   *  one is WHICH FILE THE FORM IS SHOWING, and a folder is not a file. One
   *  signal answering both would have to be read as "a request, unless it is
   *  a collection", and Send is gated on it. */
  activeCollection(): string
  /**
   * WHERE THE PANEL IS POINTED inside that collection — a path, '' at its
   * root. The one owner of "where is this person standing", which before it
   * had none: the plus derived it from the open request, and a curl import
   * erased it by detaching the form from its file.
   *
   * A PLACE A PERSON WALKED TO, not a fact about the form. Opening a request
   * moves it (you are where you just went), clicking a folder moves it, and
   * pointing at a collection puts it at that collection's root. What the
   * form happens to be holding does not.
   */
  activeFolder(): string
  /**
   * Where the draft in the form WILL BE WRITTEN, for a draft with no file
   * behind it — the answer the curl ask gave, '' being the collection's root.
   *
   * Deliberately not `activeFolder`: between Convert and Save a person may
   * walk somewhere else in the tree, and a pending file that followed them
   * around would be a promise the ask made and the surface broke.
   */
  draftFolder(): string
  /**
   * WHERE A COLLECTION MADE WITH NO PLACE NAMED GOES — the directory, off
   * the backend's own listing. '' on a build with no app directory, and ''
   * before the first listing answers.
   *
   * It is here rather than derived in the surface because only the backend
   * knows it: the location comes from the app's data directory, which the
   * renderer has no way to compute and must never guess at.
   */
  defaultRoot(): string
  /** The environments of the collection the workbench is pointed at, as the
   *  last listing had them. [] when that collection has none, and [] when
   *  nothing is open — a collection with no environments is a collection
   *  (§6.2), so this is an ordinary state and not a degraded one. */
  environments(): readonly ApiEnvironmentRef[]
  /** Which environment the next send from the ACTIVE collection goes out
   *  under: the environment's path within that collection, or '' for none.
   *
   *  Per collection, because it addresses one: a path from another folder is
   *  a path this handle does not own, and the backend refuses it. */
  activeEnvironment(): string
  selected(): ApiSelection | null
  draft(): ApiRequest | null
  saved(): ApiRequest | null
  /** True while the draft differs from what the file last answered. */
  dirty(): boolean
  /** The exchanges of the request that is OPEN, newest first — never the
   *  whole session's. An answer belongs beside its question. */
  runs(): readonly ApiRun[]
  notes(): readonly ApiImportNote[]
  /** The last failure, in the words the backend used, or '' when the last
   *  thing attempted worked. On the surface rather than in a log: a degrade
   *  the UI does not show is a feature that does not exist surviving a
   *  release. */
  error(): string
  loading(): boolean
  /** The run of the OPEN request that is still in flight, or null.
   *
   *  A run rather than a boolean, because the surface needs the token to
   *  stop it and the row to point at. It answers about the open request
   *  only: a send left running in another request is that request's business
   *  and its Stop lives on its own line. */
  pending(): ApiRun | null

  /**
   * Who answers a name according to the backend-computed scope rows, or
   * `unknown` while that scope has not been read.
   */
  variableAnswer(name: string): VariableAnswer

  /** The backend-computed request, folder, environment and vault rows. */
  scopeVariables(): readonly ApiScopeVariable[] | null
  /** Give a secret variable its value — the one call that carries a
   *  credential. Answers whether it landed; the value is never kept. */
  bindSecret(variable: string, value: string): Promise<boolean>

  /** The backend's reported refresh mode for the collection watch set, or
   *  null until the first `files.watch` answers — and for a build that
   *  cannot watch at all. */
  watchMode(): 'watching' | 'polling' | null
  /** Why refresh is degraded: non-null only when a LOCAL watch could not be
   *  established and the backend fell back to polling. The persistent badge
   *  renders from this; designed-mode polling carries no reason and warns
   *  about nothing. */
  watchDegradedReason(): string | null
  /** The watch could not be established, in the backend's words, or '' when
   *  the last attempt worked. Both ends of the interval: it is set from the
   *  moment a `files.open` or `files.watch` is refused until the next
   *  successful `files.watch` — which `refresh()` always sends, so the header
   *  action is the retry. */
  watchFailed(): string

  /** Subscribe to the change stream and begin watching. Called once, by the
   *  pane's mount; `dispose()` is the other end. Idempotent. */
  startWatching(): void
  /** Release the watch binding and drop the subscriptions. The collection
   *  handles are NOT released — they belong to the app's opened-folder list
   *  (design §6.1) and closing the tab must not close the user's folders. */
  dispose(): void

  refresh(): Promise<void>
  openFolder(path: string): Promise<void>
  /** Make a collection under `name` and leave it open, selected and in the
   *  list — one call, because `api.collections.create` answers the same
   *  handle-and-collection an open does. */
  createCollection(name: string): Promise<void>
  /**
   * Make ONE folder inside a collection that is open, and put the collection
   * the call answered back into the list.
   *
   * `parentRelPath` is an EXISTING folder inside it, `''` being the
   * collection's own root. Nesting is repeated calls, never a path: the
   * caller passes back the `relPath` the last one answered (api-client.ts
   * says why), and the answer to "that folder is not there" is a refusal
   * rather than a folder tree nobody asked for.
   */
  createFolder(handle: string, parentRelPath: string, name: string): Promise<void>
  closeFolder(handle: string): Promise<void>
  /** The SSH connections an environment may route through, or [] where this
   *  build offers none. Read once, when the panel first needs them: a
   *  profile list changes when a person edits their connections, and the
   *  editor re-reads it every time it opens. */
  connections(): readonly ApiConnection[]
  /** Ask for that list. The environments page calls it when it opens. */
  loadConnections(): Promise<void>
  /** Point the workbench at one open collection — what a click on its row
   *  already does, reachable for an action that must act on THAT row rather
   *  than on whatever the panel happened to be pointed at. */
  pointAt(handle: string): void
  /** Walk into a folder of one open collection: the panel is pointed at that
   *  collection and standing in that folder. `relPath` is '' for the
   *  collection's own root, which is what its row means. */
  enterFolder(handle: string, relPath: string): void
  openRequest(handle: string, relPath: string): Promise<void>
  /**
   * Take the request out of the form. The FILE is untouched — this is a
   * close, not a delete, and it is what a tab's ✕ is in every other client;
   * nocx has one form by design, so it is one act rather than a strip.
   *
   * IT DOES NOT ASK. The surface does, the way it does for a delete, with
   * `unsavedWork` and `closeQuestion` below — this file is not where this
   * product raises dialogs (see the header).
   */
  closeRequest(): Promise<void>
  /** True while the form holds work that is not on disk: a draft that has
   *  drifted from its file, or one that never had a file. The ONE owner of
   *  that question — the pane derived it a second time for Save's enabled
   *  state, and two derivations of one predicate agree until they do not. */
  unsavedWork(): boolean
  /** What closing the form would cost, named, or '' when it would cost
   *  nothing. The sentence is here because which of the two is true is read
   *  off `selected`, and that belongs to whoever holds it. */
  closeQuestion(): string
  /**
   * Write the draft into a file that does not exist yet, in the collection
   * the workbench is pointed at, and select it.
   *
   * The missing half of the curl import. `api.import.curl` fills the FORM —
   * there is no file behind it (importCurl says so), so nothing is selected
   * and Send is refused, correctly: `api.request.send` reads the file. What
   * was missing was any way to give it one, so an imported request could be
   * looked at and never sent. This is that way, and it is also how a request
   * comes to exist at all — there is no other creator.
   */
  saveDraftAs(): Promise<void>
  /** Write the draft back to the file it came from. */
  saveDraft(): Promise<void>
  /**
   * Delete one request file.
   *
   * The form is cleared only when the file DELETED is the one it is showing
   * — a request open from another collection is nobody's business here. What
   * replaces it is nothing rather than the next row: picking a person's next
   * request for them is a choice they did not make.
   */
  deleteRequest(handle: string, relPath: string): Promise<void>
  /**
   * Copy one request file, beside itself, and open the copy.
   *
   * Somebody wanting the same call with one header changed had two ways to
   * get it: retype the request, or edit the original and lose it. The parts
   * were both here — an allocator that names a file nothing occupies, and a
   * write — with no door onto them.
   *
   * THE SOURCE IS THE FILE, read under its own path rather than taken from
   * the form: the request being copied is very often not the one open, and
   * the file is the truth in any case (§6.4). What travels is everything the
   * file holds — method, URL, headers, query, variables, body and the auth
   * VARIABLE NAME — and what does not is the file's own identity, the path
   * and the id minted from it. A credential cannot travel because there is
   * nowhere in the contract it could be spelled (design §8).
   */
  duplicateRequest(handle: string, relPath: string): Promise<void>
  /**
   * Move one request file to another path inside the SAME collection.
   *
   * The file is renamed on the backend, never copied-then-deleted. The
   * OPEN request follows the file: if this is the request in the form, the
   * form ends up pointed at the new path with the moved file's contents.
   * A move of the open request with unsaved edits is REFUSED (see the
   * implementation), because moving carries the file and not the draft.
   */
  moveRequest(handle: string, fromRelPath: string, toRelPath: string): Promise<void>
  /**
   * Make a request that does not exist yet, in the collection the workbench
   * is pointed at, and open it.
   *
   * Until this there was NO WAY TO CREATE A REQUEST at all: the panel could
   * open one a file already held, convert a curl line into a form with no
   * file behind it, or import somebody's Postman export — and a person
   * starting from nothing had to write JSON into the folder by hand.
   *
   * `dir` is which folder INSIDE that collection it goes in — the same
   * parameter `freePath` has always taken for a duplicate, reached from a
   * second door: a folder with nothing in it is not a place anybody can work
   * until a request can be put in it. A door that AIMS at a row passes it,
   * and a collection row's own path is '', which is how "the root" is said
   * deliberately.
   *
   * OMITTED means "here", not "the root". The header's plus aims at nothing
   * — that is what it is for — so the only answer it can give is where the
   * person already is: the folder the open request lives in.
   */
  newRequest(dir?: string): Promise<void>
  editDraft(next: ApiRequest): void
  /** Point the active collection at one of its environments, or at none
   *  ('' ). A person's choice is remembered for as long as the workbench
   *  lives and is never overwritten by a later listing — including a choice
   *  of NONE, which is why "chosen nothing" and "chosen none" are two
   *  states here rather than one. */
  setEnvironment(relPath: string): void
  readFolderVariables(relPath: string): Promise<ApiFolderVariablesResult>
  writeFolderVariables(
    relPath: string,
    variables: readonly ApiParam[],
  ): Promise<ApiFolderVariablesResult>
  send(): Promise<void>
  /**
   * Stop the run of the open request that is in flight.
   *
   * It does NOT settle the row. The stopped exchange reports itself on the
   * send's own result, which comes back as `stopped` and fills the row the
   * same way an answer does — so there is one writer of a run's ending and
   * it is the same one for all three outcomes. A store that also wrote
   * "stopped" here would be the second account of one exchange, and the two
   * would disagree the day a stop arrived too late to take effect.
   */
  stop(): Promise<void>
  /** Read ONE environment whole, for an ask that is about to edit it. It
   *  answers null when the read failed — the reason is on `error()`, the way
   *  every other failed call reports — because there is nothing to open an
   *  editor onto. */
  readEnvironment(relPath: string): Promise<ApiEnvironment | null>
  /** Write one back, creating the file when nothing occupies the name, and
   *  re-list so the picker names what is now on disk. */
  writeEnvironment(relPath: string, environment: ApiEnvironment): Promise<void>
  /** Convert a curl command line into the form. `into` is the folder the
   *  resulting request will be SAVED to and defaults to where the person is
   *  standing; nothing is written until then (design §10). */
  importCurl(line: string, into?: string): Promise<void>
  /** Convert an export into a collection folder at `dest`. The export is an
   *  ImportSource — a path on the backend's machine, the document itself, or
   *  a URL the backend fetches over a route — because a browser drop and the
   *  kit's file input hold BYTES and no location, bytes reach a backend
   *  wherever it runs, and an export can sit behind a network only the
   *  backend is on (api-client.ts). */
  importPostman(source: ImportSource, dest: string): Promise<void>
  setRunView(id: number, view: ApiRunView): void
}

/**
 * A name for one exchange, minted here because the dispatcher's id is not
 * ours to use (api-client.ts).
 *
 * `crypto.randomUUID` where there is one, which is every browser this ships
 * in and the test runner; the counter is for a context that has none, and it
 * is per module rather than global time so two tokens minted in the same
 * millisecond still differ. Collision is what must not happen: the backend
 * refuses a second send under a token already in flight, and two windows
 * choosing one name would each be refused by the other's run — except that
 * the registry is keyed by connection too, so it could not happen anyway.
 */
let tokenCounter = 0
function newToken(): string {
  const c: Crypto | undefined = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  tokenCounter += 1
  return `run-${tokenCounter}`
}

/**
 * The file a request called `name` can have, given what the folder already
 * holds: `create-user.json`, then `create-user-2.json`, and so on.
 *
 * The suffix counts FILES rather than requests, because what must not
 * collide is the path — two requests may legitimately be called the same
 * thing, and the folder is what refuses that. A name that slugs to nothing
 * (punctuation, another script) falls back to `untitled`, which is a file
 * name rather than a judgement about the name.
 */
/** What a request is called before anybody — a person or the offer below —
 *  has said. It is a file name's worth of nothing on purpose: the request a
 *  person is about to type has no name yet, and asking for one puts a
 *  decision before the thing they came to do. */
const UNTITLED = 'Untitled request'

function freePath(
  open: readonly ApiOpenCollection[],
  handle: string,
  name: string,
  /** The directory INSIDE the collection the file goes in — '' for the
   *  collection's own root, which is where a request made from nothing goes.
   *  A copy goes beside its original instead, and a copy of
   *  `users/create.json` at the root is not beside anything. */
  dir = '',
): string {
  const taken = new Set(
    open.find((c) => c.handle === handle)?.collection.requests.map((r) => r.relPath) ?? [],
  )
  const stem = slugify(name) || 'untitled'
  const at = dir === '' ? stem : `${dir}/${stem}`
  return firstFree(taken, (n) => (n === 1 ? `${at}.json` : `${at}-${n}.json`))
}

/**
 * The first candidate `form` produces that `taken` does not already hold,
 * counting from one — the rule behind `create-user.json`, `create-user-2.json`
 * and behind `create copy`, `create copy 2`.
 *
 * ONE COUNTING RULE, two currencies. A file and a name are uniquified against
 * different sets and spell their suffix differently, and that is all that
 * differs; a second loop would be the same rule written twice, agreeing until
 * the day one of them learnt something the other did not.
 */
function firstFree(taken: ReadonlySet<string>, form: (n: number) => string): string {
  for (let n = 1; ; n++) {
    const candidate = form(n)
    if (!taken.has(candidate)) return candidate
  }
}

/**
 * What a copy of `name` is CALLED, given what the collection already shows.
 *
 * A name a person can tell apart is the point of the whole act: a second row
 * called exactly what the first one is called is the tree this door exists to
 * stop growing. The file could not collide either way — the allocator sees to
 * that — but two identical rows over two different files is worse than a
 * collision, because nothing on screen says which is which.
 */
function freeCopyName(open: readonly ApiOpenCollection[], handle: string, name: string): string {
  const taken = new Set(
    open.find((c) => c.handle === handle)?.collection.requests.map((r) => r.name) ?? [],
  )
  return firstFree(taken, (n) => (n === 1 ? `${name} copy` : `${name} copy ${n}`))
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/** How long after the last edit the draft is written to its file. Long
 *  enough that a sentence typed into a URL is one write, short enough that
 *  closing the lid a second later keeps it. The same rhythm and the same
 *  argument as the note tab (note-content.ts), which is where this product
 *  already decided what "saves by itself" means. */
const SAVE_IDLE_MS = 500

export interface ApiStoreOptions {
  /** Injected by tests, which cannot wait half a second per case and must
   *  not depend on a real clock (AGENTS.md: a test waits on a state, never
   *  on a duration). */
  idleMs?: number
}

export function createApiStore(
  services: ApiWorkbenchServices,
  options: ApiStoreOptions = {},
): ApiStore {
  const [collections, setCollections] = createSignal<readonly ApiOpenCollection[]>([])
  const [activeCollection, setActiveCollection] = createSignal('')
  // WHERE THE PERSON IS STANDING inside it. A place, not a fact about the
  // form: see the interface. Every writer of `activeCollection` is also a
  // writer of this — arriving in a collection is arriving at its root — and
  // the one exception is `openRequest`, which lands in the request's folder.
  const [activeFolder, setActiveFolder] = createSignal('')
  // Where a draft with no file behind it will be written. Only the curl
  // import mints such a draft, and the ask is what answers this.
  const [draftFolder, setDraftFolder] = createSignal('')
  // ── Which environment a send goes out under (nocx-pnvnn) ────────────────
  //
  // Keyed by the collection HANDLE, and the absence of a key is a state of
  // its own: "nobody has chosen for this folder yet" is not "None was
  // chosen", and only the first may be filled in by a default. Collapsing
  // them would make a person's deliberate None revert to an environment on
  // the next listing — a control whose answer the panel quietly replaces.
  //
  // The default is applied ONLY when a collection has exactly ONE
  // environment. That is the whole rule, and both halves are bought:
  //
  //  * With one, there is no choice to make. Nearly every Postman export is
  //    this case, and starting on None would make its first Send fail on
  //    `{{baseUrl}}` — a variable the folder can answer — which is the
  //    product contradicting itself.
  //  * With several, the first in a list is not a choice anybody made, and
  //    one of them is usually production. A request fired at a live system
  //    because a panel picked alphabetically is not a mistake a message can
  //    undo, so the panel picks nothing and says so.
  const [chosenEnvironments, setChosenEnvironments] = createSignal<ReadonlyMap<string, string>>(
    new Map(),
  )
  const [selected, setSelected] = createSignal<ApiSelection | null>(null)
  const [draft, setDraft] = createSignal<ApiRequest | null>(null)
  const [saved, setSaved] = createSignal<ApiRequest | null>(null)
  const [runs, setRuns] = createSignal<readonly ApiRun[]>([])
  const [notes, setNotes] = createSignal<readonly ApiImportNote[]>([])
  const [error, setError] = createSignal('')
  const [loading, setLoading] = createSignal(false)
  const [defaultRoot, setDefaultRoot] = createSignal('')

  // Run ids come from a counter rather than a clock: `Date.now()` gives two
  // runs fired in the same millisecond one id, and a list keyed by a
  // duplicate id renders one row for two exchanges.
  let nextRunId = 1

  // ── Watching (nocx-19rcp) ───────────────────────────────────────────────
  //
  // A collection is a folder on disk, and it changes underneath us. The
  // product already answers "how does a surface learn a directory changed" —
  // files.watch plus the files.changed invalidation — so this uses that and
  // does not invent a second answer inside api.* (AD-8; AGENTS.md's "look for
  // the existing answer before you write a second one").
  //
  // Three properties come from the contract and are kept HERE rather than
  // hoped for:
  //
  //  * files.watch REPLACES the set. So the set is derived from what the
  //    panel renders and re-published whenever that changes — a collection
  //    that has been closed leaves the set by construction, and cannot leak a
  //    watch because nothing has to remember to remove it.
  //  * The published set is recorded at the moment the paths are READ and is
  //    NOT rolled back when the call fails. A rejected watch is a sticky
  //    failure the user retries through the header's Refresh; re-sending the
  //    identical set on the next listing would erase the message it is meant
  //    to leave up. refresh() forgets the record first, which is what makes
  //    that action the retry.
  //  * A newly added path that fails to establish must not take the healthy
  //    watches down. Nothing here tears anything down on failure: the binding
  //    stays, the subscription stays, and a change on a folder that IS still
  //    watched still re-lists it.
  const watcher = services.watchCollections
  const [watchMode, setWatchMode] = createSignal<'watching' | 'polling' | null>(null)
  const [watchDegradedReason, setWatchDegradedReason] = createSignal<string | null>(null)
  const [watchFailed, setWatchFailed] = createSignal('')

  /** The binding every watch call carries, or null while there is none. */
  let bindingId: string | null = null
  /** The set as last handed to files.watch, or null when the backend's set
   *  must be treated as unknown (never sent; dropped by a reconnect; a
   *  refresh deliberately forcing a re-send). */
  let publishedPaths: readonly string[] | null = null
  /** The change subscription and the reconnect hook, so dispose has both
   *  ends of the interval it closes. */
  let unsubscribes: (() => void)[] = []
  /** True from dispose() onwards: a response that lands afterwards must not
   *  paint, and must not re-open a binding nobody is holding. */
  let disposed = false

  /**
   * The collection roots the panel currently renders.
   *
   * A row minted by `api.collections.create` carries NO path — §13.1 leaves
   * the location to the backend and the result does not spell it — so it is
   * not in the set until the next listing fills it in. That is a real gap of
   * one round trip, and it closes itself: createCollection is followed by the
   * user's next refresh or by any change on a folder that IS watched.
   *
   * Deduplicated, because a set is what the wire wants and two rows can name
   * one folder; ordered by the list, because comparing against the published
   * record has to be stable.
   */
  const watchPaths = (): string[] =>
    // Untracked, and it has to be: every caller is an async continuation or a
    // notification handler, never a tracked scope. A subscription taken here
    // would belong to whatever computation happened to be running when the
    // promise resolved — which is nothing at all, so the reads would simply
    // be ignored while looking like they were watched.
    untrack(() => {
      const out: string[] = []
      for (const c of collections()) {
        if (c.path !== '' && !out.includes(c.path)) out.push(c.path)
      }
      return out
    })

  /** Open the binding the watch set is carried on, or answer null when there
   *  is nothing to open one against.
   *
   *  ROOTED AT '/', and it is a carrier rather than a view: the panel never
   *  lists through it, and a collection folder can sit anywhere. `files.open`
   *  needs a session the connection owns and builds THAT session's provider,
   *  so the port hands us a LOCAL one — a collection is backend-local (§13.1)
   *  and an SSH session's binding would watch the wrong machine. */
  const openBinding = async (): Promise<string | null> => {
    if (watcher === undefined || disposed) return null
    if (bindingId !== null) return bindingId
    const sessionId = watcher.localSession()
    if (sessionId === null) return null
    try {
      const res = await watcher.open(sessionId, '/')
      if (disposed) {
        // Disposed while the open was in flight: releasing it here is the
        // only chance anybody gets — nothing else holds the id.
        void watcher.close(res.bindingId).catch(() => undefined)
        return null
      }
      bindingId = res.bindingId
      return bindingId
    } catch (err) {
      if (!disposed) setWatchFailed(message(err))
      return null
    }
  }

  /** Publish the watch set iff what the panel renders has drifted from what
   *  the backend was last told. Every seam that can change the set calls
   *  this and nothing calls files.watch directly, so a set cannot be sent
   *  twice for one change and cannot be missed by a path that forgot. */
  const syncWatchSet = async (): Promise<void> => {
    if (watcher === undefined || disposed) return
    const want = watchPaths()
    if (
      publishedPaths !== null &&
      publishedPaths.length === want.length &&
      publishedPaths.every((path, i) => path === want[i])
    ) {
      return
    }
    // Nothing open and no binding yet: there is nothing to watch, so no
    // binding is minted for an empty set. The record still moves, so the
    // first collection to arrive is a drift and publishes.
    if (want.length === 0 && bindingId === null) {
      publishedPaths = want
      return
    }
    const id = await openBinding()
    if (id === null || disposed) return
    publishedPaths = want
    try {
      const res = await watcher.watch(id, want)
      if (disposed) return
      setWatchFailed('')
      setWatchMode(res.mode)
      setWatchDegradedReason(res.degradedReason ?? null)
    } catch (err) {
      if (disposed) return
      setWatchFailed(message(err))
    }
  }

  /**
   * The server-initiated invalidation: one dirty path, no entries, so exactly
   * one code path re-reads a collection.
   *
   * Two filters, each for a different defect. A change for a binding this
   * store does not follow is not its business — the Files panel's binding,
   * or one from a previous connection. And a path outside every collection
   * root cannot be a collection of ours changing; the watch set is what
   * decides, not a guess.
   */
  const onCollectionChanged = (p: FilesChanged): void => {
    if (disposed || bindingId === null || p.bindingId !== bindingId) return
    const affected = watchPaths().some((root) => p.path === root || p.path.startsWith(`${root}/`))
    if (!affected) return
    relist()
  }

  /** Re-read the open folders because the DISK said so, not because a person
   *  did. It is the same listing the header's action issues — one code path
   *  renders a collection — and it does not force the watch set, so a change
   *  cannot erase a sticky watch failure the user has not retried.
   *
   *  Serialised, and at most one queued: a burst on one folder must not put
   *  five listings on the wire whose responses can land out of order and
   *  paint an older tree over a newer one. */
  let listingChain: Promise<void> = Promise.resolve()
  let relistQueued = false
  const relist = (): void => {
    if (relistQueued) return
    relistQueued = true
    listingChain = listingChain.then(async () => {
      relistQueued = false
      if (disposed) return
      await readCollections()
      await syncWatchSet()
    })
  }

  /** The environments of whatever collection the workbench is pointed at.
   *  Reading through the ONE list rather than caching a second copy: the
   *  listing is what a change on disk re-reads, so a picker drawn from it
   *  gains an environment a colleague added without anything being told. */
  const environmentsOf = (handle: string): readonly ApiEnvironmentRef[] =>
    collections().find((c) => c.handle === handle)?.collection.environments ?? []

  /** Which environment a send from ONE collection goes out under. The
   *  picker and the send read the same function, so the control cannot say
   *  one thing while the wire carries another. */
  const environmentFor = (handle: string): string => {
    const chosen = chosenEnvironments().get(handle)
    if (chosen !== undefined) return chosen
    const envs = environmentsOf(handle)
    return envs.length === 1 ? envs[0].relPath : ''
  }

  /** The runs of the request that is OPEN, newest first. The list holds
   *  every exchange this session made; what a person is shown is the answers
   *  to the question in front of them. */
  const visibleRuns = (): readonly ApiRun[] => {
    const target = selected()
    if (target === null) return []
    return runs().filter((r) => r.handle === target.handle && r.relPath === target.relPath)
  }

  /** The in-flight run of the OPEN request, or null. There is at most one:
   *  the line offers Stop rather than Send while it lasts, so the button
   *  cannot start a second. */
  const pending = (): ApiRun | null => visibleRuns().find((r) => r.outcome === 'pending') ?? null

  const environments = (): readonly ApiEnvironmentRef[] => environmentsOf(activeCollection())
  const activeEnvironment = (): string => environmentFor(activeCollection())

  /**
   * The backend-computed scope rows for the OPEN request, or null while no
   * current scope answer exists.
   *
   * NULL IS A THIRD STATE... A surface marking an unanswered variable must
   * not do it from an empty set it got because a read is in flight: that
   * paints every reference as broken while the panel is starting.
   * An empty array means the backend answered that nothing resolved.
   *
   * Secret rows carry their NAME and no value; the renderer never receives
   * the vault's bytes (ADR-0021).
   */
  const [scopeVariables, setScopeVariables] = createSignal<readonly ApiScopeVariable[] | null>(null)
  let scopeRevision = 0

  /**
   * WHO ANSWERS A NAME, and with what — the one accessor for it.
   *
   * The backend scope rows are the only precedence decision here. They are
   * already ordered winner first, so this function only selects the row and
   * maps its wire vocabulary to the address field's vocabulary.
   *
   * `unknown` is not a hedge and not the same as `unbound`: until a scope
   * refresh answers, painting a name as unanswered would cry wolf while the
   * answer is in flight.
   */
  const variableAnswer = (name: string): VariableAnswer => {
    const rows = scopeVariables()
    if (rows === null) return { scope: 'unknown', value: null, from: null }

    const row = rows.find((candidate) => candidate.name === name)
    if (row === undefined) return { scope: 'none', value: null, from: null }

    if (row.scope === 'vault') return { scope: 'secret', value: null, from: null }
    return {
      scope: row.scope,
      value: row.value,
      from: row.scope === 'folder' ? row.from : null,
    }
  }

  const refreshScope = async (): Promise<void> => {
    const revision = ++scopeRevision
    setScopeVariables(null)
    const target = untrack(selected)
    if (target === null) return
    const envRelPath = untrack(() => environmentFor(target.handle))
    const variables = untrack(draft)?.variables ?? []
    const isCurrent = (): boolean => {
      const current = untrack(selected)
      return (
        scopeRevision === revision &&
        current?.handle === target.handle &&
        current.relPath === target.relPath &&
        untrack(() => environmentFor(target.handle)) === envRelPath
      )
    }
    try {
      const result = await services.requestScope(
        target.handle,
        target.relPath,
        envRelPath,
        variables,
      )
      if (isCurrent()) setScopeVariables(result.variables)
    } catch (err) {
      if (isCurrent()) {
        setScopeVariables(null)
        setError(message(err))
      }
    }
  }

  /** Refresh the complete backend-computed scope after a source change. */
  const refreshVariables = async (): Promise<void> => {
    await refreshScope()
  }

  const setEnvironment = (relPath: string): void => {
    const handle = untrack(activeCollection)
    if (handle === '') return
    setChosenEnvironments((prev) => new Map(prev).set(handle, relPath))
    void refreshVariables()
  }

  /** The draft differs from what the file last answered. Compared by value —
   *  the form replaces the object on every keystroke, so identity would say
   *  "dirty" for a field typed into and typed back. */
  const dirty = (): boolean => {
    const d = draft()
    const s = saved()
    if (d === null || s === null) return false
    return JSON.stringify(d) !== JSON.stringify(s)
  }

  /** WHAT WOULD BE LOST IF THE FORM WERE REPLACED — the condition that
   *  separates "ask first" from "there was nothing to lose".
   *
   *  Two ways a draft holds work that is not on disk, and `dirty` sees only
   *  the first: it differs from the file it was read from, or there is NO
   *  file behind it at all. A converted curl line is the second — `saved` is
   *  null, so `dirty` is false while every field in the form is unsaved —
   *  and it is the case a rule written on `dirty` alone would drop on the
   *  floor exactly when a person imports twice.
   *
   *  api-pane.tsx derives the same predicate for Save's enabled state
   *  (`draft() !== null && (dirty() || selected() === null)`). This is its
   *  owner and that expression should read it; the file belongs to another
   *  worker this wave, so the duplicate is named here rather than left to be
   *  found. Two derivations of one question agree until they do not.
   */
  const unsavedWork = (): boolean => draft() !== null && (dirty() || selected() === null)

  /** Re-read the open folders. The one call that renders the list, whoever
   *  asked for it — a person, an import, or the disk. */
  const readCollections = async (): Promise<void> => {
    setLoading(true)
    try {
      const result = await services.listCollections()
      setCollections(result.collections)
      // WHERE A NEW COLLECTION WOULD GO. It rides the listing because it is
      // a fact about the build rather than about any folder, so there is no
      // second call to make and nothing to keep in step: every refresh
      // re-reads it beside the rows.
      setDefaultRoot(result.defaultRoot)
      // THE WORKBENCH POINTS AT SOMETHING WHENEVER SOMETHING IS OPEN, and
      // the interval closes when the last folder does. Without this a pane
      // mounted onto folders opened in an earlier session was pointed at
      // nothing until the person clicked a row — and everything hanging off
      // the pointer, the environment picker included, reported the state of
      // no collection at all while a collection was plainly on screen.
      //
      // It also covers the folder that LEAVES underneath the pointer: a
      // handle that is no longer listed cannot be re-validated, so pointing
      // at it is pointing at nothing.
      const listed = result.collections
      if (!listed.some((c) => c.handle === untrack(activeCollection))) {
        setActiveCollection(listed.length > 0 ? listed[0].handle : '')
        setActiveFolder('')
      }
      // AND THE FOLDER UNDERNEATH THE PERSON, by the same argument one step
      // down: a colleague's `git pull` can take a folder away while somebody
      // is standing in it, and a place that is not there is not a place. The
      // set consulted is `folders`, which is the ONE answer to what folders
      // exist (the tree reads the same one).
      const here = untrack(activeFolder)
      if (here !== '') {
        const holder = listed.find((c) => c.handle === untrack(activeCollection))
        if (holder === undefined || !holder.collection.folders.includes(here)) setActiveFolder('')
      }
      setError('')
    } catch (err) {
      setError(message(err))
    } finally {
      setLoading(false)
    }
  }

  /** The header's action: re-read the folders AND re-establish the watch,
   *  which is what makes it the retry for a watch that failed. Forgetting the
   *  published record first is the whole of that — an unchanged set would
   *  otherwise be suppressed as "already sent", and the sticky failure would
   *  have no way back. */
  const refresh = async (): Promise<void> => {
    await readCollections()
    publishedPaths = null
    await syncWatchSet()
    // The listing may have changed a folder variable, so refresh the
    // backend-computed scope after it completes.
    await refreshVariables()
  }

  const openFolder = async (path: string): Promise<void> => {
    try {
      const result = await services.openCollection(path)
      const row: ApiOpenCollection = {
        handle: result.handle,
        path,
        error: '',
        collection: adoptOpenedCollection(result.collection),
      }
      // The handle identifies the row, not the path: re-opening the folder
      // the user already has open must not put a second copy of it in the
      // tree with a stale listing beside a fresh one.
      setCollections((prev) => [...prev.filter((c) => c.handle !== row.handle), row])
      setActiveCollection(row.handle)
      // A collection that has just been opened is entered at its root: there
      // is nowhere else in it a person could be said to be standing.
      setActiveFolder('')
      setError('')
    } catch (err) {
      setError(message(err))
    }
    // Outside the try: a folder that joined the list is watched whether or
    // not something else in this call went wrong, and a watch that is refused
    // is reported through watchFailed rather than as the open's failure.
    await syncWatchSet()
  }

  /**
   * Make one, and adopt what came back.
   *
   * ONE CALL, AND THAT IS THE WHOLE POINT OF THE CONTRACT'S SHAPE.
   * `api.collections.create` answers the same `{handle, collection}` an open
   * does — the schema says it is "api.collections.open's on purpose … so the
   * renderer has one thing to do afterwards rather than two, and there is no
   * moment at which a freshly made collection is not addressable". So this
   * neither re-opens the folder nor re-lists: it puts the result straight
   * into the one list, which is what makes the new collection visible and
   * pointed at before any further round trip.
   *
   * There is NO PATH on the row, because the result carries none: §13.1
   * leaves the location to the backend, so the renderer cannot spell where
   * the folder went and does not pretend to. The next listing fills it in.
   */
  const createCollection = async (name: string): Promise<void> => {
    try {
      const result = await services.createCollection(name)
      const row: ApiOpenCollection = {
        handle: result.handle,
        path: '',
        error: '',
        collection: adoptCreatedCollection(result.collection),
      }
      setCollections((prev) => [...prev.filter((c) => c.handle !== row.handle), row])
      setActiveCollection(row.handle)
      setActiveFolder('')
      setError('')
    } catch (err) {
      // A refused name — blank, a path separator in it, `.`, `..`, a folder
      // already there — is the backend's sentence, and it goes where every
      // other failure goes so the surface can render it. Swallowing it here
      // is what makes a refusal look like a button that does nothing.
      setError(message(err))
    }
    // A created row carries no path (§13.1), so this publishes nothing new
    // today — it is here because the SET is derived from the list and every
    // seam that changes the list republishes it. A seam that decided for
    // itself whether its change could matter is how a path stops being
    // watched without anybody noticing.
    await syncWatchSet()
  }

  /**
   * Give an open collection a folder, and redraw from what the call answered.
   *
   * ONE CALL, for the reason `createCollection` above is one:
   * `api.collections.createFolder` answers the collection AS IT IS NOW, "so
   * the caller's next move is to draw the tree" — a listing fetched in a
   * second round trip would be a second account of one folder taken at a
   * second moment, and a folder with nothing in it yet is exactly the thing
   * that account could disagree about.
   *
   * A REFUSAL CHANGES NOTHING. An existing folder is refused rather than
   * merged — Mkdir's own EEXIST, which is the rule the import follows for its
   * destination — and a refused name is the backend's sentence about what was
   * typed. Both go where every other failure goes, so the ask that is holding
   * the name can show the reason under the field rather than closing over it.
   *
   * The collection is replaced in place rather than appended: the handle is
   * the row's identity, and the row keeps its path and its own listing error,
   * neither of which this call is about.
   */
  const createFolder = async (
    handle: string,
    parentRelPath: string,
    name: string,
  ): Promise<void> => {
    try {
      const result = await services.createFolder(handle, parentRelPath, name)
      const collection = adoptFolderCollection(result.collection)
      setCollections((prev) =>
        prev.map((c) => (c.handle === handle ? { ...c, collection, error: '' } : c)),
      )
      setError('')
    } catch (err) {
      setError(message(err))
    }
    // No watch to republish: the SET is the open collections' roots, and a
    // folder made inside one changes none of them. Said here rather than
    // left to be inferred, because every other seam that touches the list
    // does republish and the difference is the reason.
  }

  const closeFolder = async (handle: string): Promise<void> => {
    try {
      await services.closeCollection(handle)
      setCollections((prev) => prev.filter((c) => c.handle !== handle))
      // Nothing is pointed at a folder that has left — and the environment
      // chosen for it goes with it, which is the closing end of that
      // choice's interval. A handle is minted fresh every open, so a
      // remembered row could never be read again; it would only be a map
      // that grows for the life of the window.
      setChosenEnvironments((prev) => {
        const next = new Map(prev)
        next.delete(handle)
        return next
      })
      if (selected()?.handle === handle) cancelPendingSave()
      if (activeCollection() === handle) {
        setActiveCollection('')
        // The folder a person was standing in belonged to the collection
        // that just left the list.
        setActiveFolder('')
      }
      // The form was showing a request in the folder that just left. Keeping
      // it would leave a Send pointed at a handle that no longer resolves.
      if (selected()?.handle === handle) {
        setSelected(null)
        setDraft(null)
        setSaved(null)
      }
      setError('')
    } catch (err) {
      setError(message(err))
    }
    // The folder has left the list, so it leaves the watch set — the whole
    // set is re-sent without it, which is what the contract's REPLACE
    // semantics turn into "closing a collection cannot leak a watch".
    await syncWatchSet()
  }

  const openRequest = async (handle: string, relPath: string): Promise<void> => {
    // THE FORM IS ABOUT TO HOLD SOMETHING ELSE. Whatever the last edit was,
    // it belongs in the file it was an edit to — and the timer that would
    // have written it is about to be pointed at a different request.
    await flushDraft()
    try {
      const result = await services.readRequest(handle, relPath)
      setSelected({ handle, relPath })
      setActiveCollection(handle)
      // OPENING SOMETHING IS GOING THERE. The one writer of the pointer that
      // does not land at a root: a person who opened `users/create` is
      // standing in `users`, and the plus beside them means that folder.
      setActiveFolder(directoryOf(relPath))
      setDraftFolder(directoryOf(relPath))
      // FOLDED ONCE, into both. A file may carry its query in the URL, in
      // the rows, or in both — the sender concatenates them (§6.4) — and the
      // panel shows one of the two. Folding here makes the rows the one
      // owner from the moment the file is opened; doing it to the saved
      // snapshot as well is what keeps `dirty` false, so opening a request
      // does not report itself as edited.
      const adopted = foldQueryIntoParams(result.request)
      // WHOSE NAME IS IN THE FORM NOW. A file still called `Untitled
      // request` has been named by nobody, so the offer is live on it — that
      // is the request `newRequest` has just written and reopened. Every
      // other name on disk is one somebody gave, including one this offer
      // gave earlier, and it is never taken away again.
      offered = adopted.name === UNTITLED ? UNTITLED : ''
      setDraft(adopted)
      setSaved(adopted)
      setError('')
      await refreshVariables()
    } catch (err) {
      // The previous request stays in the form. Clearing it would make one
      // unreadable file look like the whole collection went away.
      setError(message(err))
    }
  }

  // ── A request names itself, until somebody names it (nocx-lpo2m) ──────
  //
  // WHAT THE STORE IS STILL FREE TO REPLACE: the name it last put in the
  // form, and '' once a person has taken the name over. That one string is
  // the whole interval, and both its ends are named — it opens when a
  // request arrives unnamed and it closes, for good, the moment `editDraft`
  // is handed a different name than the one in the draft. Only the header's
  // rename field can do that: everything else edits the address, the rows or
  // the body, and hands the name straight back.
  //
  // It is here rather than in the form because the DRAFT is here. A surface
  // deriving the name from the URL it is rendering would have to remember
  // what the name was a moment ago and who changed it, which is exactly what
  // this variable is, in a component that is rebuilt whenever the pane is.
  let offered = ''

  // ── SAVING IS NOT A GESTURE ───────────────────────────────────────────
  //
  // There was a Save button, and it was pressed for insurance rather than
  // for a decision: Send already wrote the file before sending, so the only
  // thing the button bought was not losing an experiment on the way to it.
  // The file is written when typing stops instead, and every act that would
  // REPLACE or REMOVE the draft flushes or cancels first — a write that
  // landed after a delete would put the file back, and one that landed after
  // a move would put the old path back.
  let saveTimer: ReturnType<typeof setTimeout> | null = null

  const cancelPendingSave = (): void => {
    if (saveTimer === null) return
    clearTimeout(saveTimer)
    saveTimer = null
  }

  /** Write the draft to its file NOW, if it has drifted from it. Awaited by
   *  everything that is about to take the draft away, so the last thing a
   *  person typed is in the file before it goes. */
  const flushDraft = async (): Promise<void> => {
    cancelPendingSave()
    if (untrack(selected) === null || untrack(draft) === null || !untrack(dirty)) return
    await saveDraft()
  }

  const scheduleSave = (): void => {
    cancelPendingSave()
    saveTimer = setTimeout(() => {
      saveTimer = null
      void flushDraft()
    }, options.idleMs ?? SAVE_IDLE_MS)
  }

  /** What an edit DOES to the draft — the naming rule, unchanged. `editDraft`
   *  is this plus the save it schedules, so there is no edit anywhere that
   *  does not reach the file. */
  const applyDraft = (next: ApiRequest): void => {
    const current = untrack(draft)
    if (current === null || next.name !== current.name) {
      // A PERSON NAMED IT. Not when the URL changes, not ever: the offer is
      // spent, and it stays spent across a save and a reopen, because what
      // the file then holds is a name somebody gave.
      offered = ''
      setDraft(next)
      return
    }
    if (offered === '' || next.name !== offered) {
      setDraft(next)
      return
    }
    // AN OFFER, NOT A DERIVATION (api-paths.ts). '' is "there is nothing in
    // this address to take a name from" — a bare host, an address that is
    // all references — and then the request keeps the name it has and the
    // offer stays live, because absent is not spent.
    const name = proposedRequestName(next.method, next.url)
    if (name === '') {
      setDraft(next)
      return
    }
    offered = name
    setDraft({ ...next, name })
  }

  const editDraft = (next: ApiRequest): void => {
    applyDraft(next)
    void refreshScope()
    scheduleSave()
  }

  const saveDraftAs = async (): Promise<void> => {
    const handle = untrack(activeCollection)
    const request = untrack(draft)
    if (handle === '' || request === null) return
    // NO ASK. The request already HAS a name — the curl importer takes one
    // from the URL, and a person who wants a different one renames it in the
    // header where the name is shown. Asking for a file name at Save was
    // asking a second time for something already answered, in the currency
    // of paths rather than of names, at the moment somebody was trying to
    // press Send.
    //
    // THE FOLDER, THOUGH, WAS ANSWERED — in the import ask, which is the one
    // moment this request's destination is on screen (nocx-8aczn.10). Before
    // that this call passed no directory at all, so every curl line ever
    // imported landed at the collection's root and had to be moved by hand.
    const relPath = freePath(untrack(collections), handle, request.name, untrack(draftFolder))
    try {
      await services.writeRequest(handle, relPath, request)
      // Selected the moment it exists: what makes Send legal is that there
      // is a file, and this is where that becomes true.
      setSelected({ handle, relPath })
      setSaved(request)
      setError('')
      // The tree does not know about the new file until the folder is
      // re-read; the disk is the truth, so the row arrives the same way a
      // colleague's would.
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }

  const [connections, setConnections] = createSignal<readonly ApiConnection[]>([])

  const loadConnections = async (): Promise<void> => {
    // ABSENT is a build with no profile store, and the editor then offers no
    // route through a connection at all — optionality is the capability
    // (api-client.ts). A FAILURE is different: it leaves the list as it was
    // and says so, because an empty picker that quietly replaced a full one
    // would tell a person their connections are gone.
    if (!services.listConnections) return
    try {
      setConnections(await services.listConnections())
      setError('')
    } catch (err) {
      setError(message(err))
    }
  }

  const pointAt = (handle: string): void => {
    setActiveCollection(handle)
    // Pointing at a collection is standing at ITS root — never in whatever
    // folder of some other collection the person was in a moment ago.
    setActiveFolder('')
  }

  /** Walk into a folder. `relPath` is '' for the collection's own root, so
   *  this is also what a click on a collection row means, and `pointAt` is
   *  that call with the folder left out. */
  const enterFolder = (handle: string, relPath: string): void => {
    setActiveCollection(handle)
    setActiveFolder(relPath)
  }

  const newRequest = async (dir?: string): Promise<void> => {
    const handle = untrack(activeCollection)
    if (handle === '') return
    // WHERE A PERSON IS is where the next request goes, when no door named
    // a folder. The header's plus needs no aiming and so names none, and
    // sending it to the collection's root put the file somewhere the crumb
    // trail directly above that control did not say — `Playground › iaam ›
    // GET tokens`, and the new request under `Playground` (nocx-8aczn.6).
    //
    // It read that folder off the open REQUEST at first, which was a second
    // answer to "where is this person" and one the curl import erased by
    // detaching the form from its file. `activeFolder` is the owner now
    // (nocx-8aczn.7), and it belongs to the collection the pointer is on, so
    // there is no foreign path to guard against here.
    const into = dir ?? untrack(activeFolder)
    // NO ASK. A person pressing "new request" has already said what they
    // want, and answering with a dialog puts a naming decision before the
    // thing they came to do — which is type a URL. The request arrives
    // named "Untitled request", NAMES ITSELF from the address as that is
    // typed (editDraft), and is renamed in the header, in place, the moment
    // the person knows better (api-pane.tsx).
    const name = UNTITLED
    const relPath = freePath(untrack(collections), handle, name, into)
    try {
      // A GET at no address, which is the request a person is about to type
      // rather than a template with opinions in it. The id is the file's
      // stem, so two machines seeding the same name produce the same file.
      await services.writeRequest(handle, relPath, {
        id: relPath.replace(/\.json$/, ''),
        name,
        method: 'GET',
        url: '',
        headers: [],
        query: [],
        variables: [],
        body: { kind: 'none', text: '', fileRef: '' },
        auth: { kind: 'none', token: '', password: '', user: '' },
      })
      setError('')
      await refresh()
      // Opened through the ordinary path, so what lands in the form is what
      // the FILE says — never the object we just sent.
      await openRequest(handle, relPath)
    } catch (err) {
      setError(message(err))
    }
  }

  const duplicateRequest = async (handle: string, relPath: string): Promise<void> => {
    try {
      // WHAT A PERSON SEES IS WHAT THEY COPY.
      //
      // The source is the FORM while the form is showing this request, and
      // the file for every other row — and the second half is not a
      // preference, it is the only thing there is: a row a person
      // right-clicked without opening has no draft anywhere to copy.
      //
      // It read the file in both cases first, on the argument that the file
      // is the truth (§6.4). That argument is about what gets SENT, and even
      // there the store does not apply it this way — `send` writes the dirty
      // draft to the file BEFORE sending, precisely so that what goes is what
      // the person is looking at. Copying past their edits would have been
      // this surface stating the rule at them instead of applying it for
      // them, and worse than that it lost the edits: the copy is opened, and
      // the draft it replaces lives only in a signal (nocx-2aunx).
      //
      // The original's FILE is left exactly as it was. Duplicating is not a
      // save, and a copy that quietly wrote the original too would be a
      // second act nobody asked for; the edits are not lost by that, because
      // they are what the copy now holds and the copy is what the person is
      // now in.
      const open = untrack(selected)
      const inTheForm = open !== null && open.handle === handle && open.relPath === relPath
      // A source that will not read stops the whole act: a copy written from
      // a request nobody could read would be a file whose contents nothing
      // accounts for.
      const source = inTheForm
        ? untrack(draft)
        : (await services.readRequest(handle, relPath)).request
      if (source === null) return
      const name = freeCopyName(untrack(collections), handle, source.name)
      // BESIDE THE ORIGINAL — the same directory inside the collection, so
      // the copy of `users/create.json` lands under `users/` where a person
      // is already looking.
      const target = freePath(untrack(collections), handle, name, directoryOf(relPath))
      await services.writeRequest(handle, target, {
        ...source,
        // The id is the file's stem, the rule `newRequest` follows: two
        // machines copying the same request produce the same file, and the
        // copy does not arrive carrying the original's identity.
        id: target.replace(/\.json$/, ''),
        name,
      })
      setError('')
      // The tree learns about the copy the way it learns about a colleague's
      // — from the folder, re-read — and the form opens it through the
      // ordinary path, so what lands there is what the FILE says.
      await refresh()
      await openRequest(handle, target)
    } catch (err) {
      setError(message(err))
    }
  }

  const saveDraft = async (): Promise<void> => {
    const target = untrack(selected)
    const request = untrack(draft)
    if (target === null || request === null) return
    try {
      await services.writeRequest(target.handle, target.relPath, request)
      setSaved(request)
      setError('')
    } catch (err) {
      setError(message(err))
    }
  }

  const deleteRequest = async (handle: string, relPath: string): Promise<void> => {
    try {
      await services.deleteRequest(handle, relPath)
      const open = untrack(selected)
      if (open !== null && open.handle === handle && open.relPath === relPath) {
        // CANCELLED, NOT FLUSHED. A scheduled write for the file that was
        // just deleted would put it back — the delete would look like it
        // worked and the row would return on the next listing.
        cancelPendingSave()
        setSelected(null)
        setDraft(null)
        setSaved(null)
      }
      // The answers go with the question. Keeping them would leave a column
      // of exchanges belonging to a file that no longer exists, under a
      // panel that can no longer say what they were replies to.
      setRuns((prev) => prev.filter((r) => !(r.handle === handle && r.relPath === relPath)))
      setError('')
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }

  const moveRequest = async (
    handle: string,
    fromRelPath: string,
    toRelPath: string,
  ): Promise<void> => {
    // WRITTEN TO THE OLD PATH FIRST, which is the path the edits were edits
    // to. A scheduled write landing after the rename would recreate the file
    // where it used to be, and the collection would hold two.
    if (untrack(selected)?.relPath === fromRelPath) await flushDraft()
    const open = untrack(selected)
    const movingTheOpenOne = open !== null && open.handle === handle && open.relPath === fromRelPath
    // THE UNSAVED-DRAFT RULE, stated (nocx-8aczn.2): a move does not carry
    // edits. The file is the truth (§6.4) — api.request.move renames the
    // FILE, and nothing this side may smuggle the draft into the moved
    // file or silently discard it. DeleteRequest clears the form when the
    // deleted file was the open one; a move that did the same would lose
    // unsaved edits, and a move that wrote the draft to the destination
    // first would be a second act (a save) performed by somebody who asked
    // for a different one. So when the request being moved is the one open
    // AND its draft differs from the file, the move is refused with the
    // remedy named: save, then move. The whole of the choice lives here so
    // every door that reaches the move gets the same answer.
    if (movingTheOpenOne && untrack(dirty)) {
      setError(
        'Save the request first — moving it to a new path would carry the unsaved edits with it.',
      )
      return
    }
    try {
      const result = await services.moveRequest(handle, fromRelPath, toRelPath)
      // The OPEN request stays open, pointed at the new path — the result
      // carries it, never derived (api-client.ts). The form is re-pointed
      // and the file re-read, so what a person is looking at is the moved
      // file with its saved contents, exactly as if they had closed it and
      // opened the new path. Nothing else about the form changes: the runs
      // stay, because they are answers to THIS question and the question did
      // not move.
      if (open !== null && open.handle === handle && open.relPath === fromRelPath) {
        setSelected({ handle, relPath: result.relPath })
        setSaved(null)
        setDraft(null)
        await openRequest(handle, result.relPath)
      }
      setError('')
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }

  const send = async (): Promise<void> => {
    let request = untrack(draft)
    if (request === null) return
    // This path writes the file itself, below, and a timer firing beside it
    // would be a second writer of the same bytes.
    cancelPendingSave()
    // A REQUEST WITH NO FILE GETS ONE, HERE. `api.request.send` sends the
    // FILE — the backend snapshots it off disk, which is what makes the run
    // and the folder agree (§6.4) — so a converted curl line could not be
    // sent until somebody saved it. Refusing Send for that reason was the
    // rule stated at the person instead of applied for them: this path
    // ALREADY writes the draft before sending when it is dirty, and "there
    // is no file yet" is the same sentence with the same answer.
    if (untrack(selected) === null) {
      await saveDraftAs()
      if (untrack(error) !== '') return
      request = untrack(draft)
    }
    const target = untrack(selected)
    if (target === null || request === null) return

    if (untrack(dirty)) {
      // Rule 1: the file is what gets sent, so it holds the draft before
      // the exchange — and a refused write stops here, never sending the
      // request the file still contains under the draft's name.
      //
      // NO RUN IS APPENDED FOR THIS. The write is the step that failed:
      // nothing went out, so there is no exchange to record — only a reason
      // the file could not be saved. A row here would say a request was
      // attempted when none was.
      try {
        await services.writeRequest(target.handle, target.relPath, request)
        setSaved(request)
      } catch (err) {
        setError(message(err))
        return
      }
    }

    // THE ROW EXISTS FROM HERE. Everything below fills it in; nothing below
    // replaces it, so the id a person's eye landed on is the id that carries
    // the answer.
    const id = nextRunId++
    const token = newToken()
    const sent = request
    setRuns((prev) => [
      {
        id,
        token,
        handle: target.handle,
        relPath: target.relPath,
        method: sent.method,
        url: sent.url,
        outcome: 'pending',
        // Nothing has said which record answered or how it went out, and
        // the panel's own choice is not written here instead: that would be
        // the guess these fields exist to avoid. They are filled from the
        // result, which is the backend's account.
        environment: '',
        route: { kind: 'direct', profileId: '', insecureTls: false },
        request: null,
        remoteAddr: '',
        dnsAddresses: [],
        timings: null,
        certificates: [],
        response: null,
        failure: null,
        error: '',
        startedAt: Date.now(),
        view: 'body',
      },
      ...prev,
    ])

    try {
      // The environment of the collection the FILE is in, not of whatever
      // the tree happens to be pointed at: `activeCollection` follows a
      // request the moment it is opened (openRequest sets it), so the two
      // agree — but naming the target's handle here is what keeps them
      // agreeing if that ever stops being true, because an environment path
      // is only meaningful inside the collection that owns it.
      const result = await services.sendRequest(
        target.handle,
        target.relPath,
        environmentFor(target.handle),
        token,
      )
      fillRun(id, (run) => settledRun(run, result))
      setError('')
    } catch (err) {
      // NOT AN EXCHANGE. What still arrives as a JSON-RPC error is what
      // never became one: an unknown handle, a request file that will not
      // read, an auth variable nothing can resolve. The row says so in its
      // own outcome rather than borrowing a phase from an attempt that was
      // never made.
      const reason = message(err)
      fillRun(id, (run) => ({ ...run, outcome: 'refused', error: reason }))
    }
  }

  /** Fill one run in place. The row is found by id and rewritten; a run that
   *  is no longer in the list — its request was deleted while the exchange
   *  was in flight — is simply not there, and the answer goes nowhere, which
   *  is right: the question it belonged to is gone. */
  const fillRun = (id: number, fill: (run: ApiRun) => ApiRun): void => {
    setRuns((prev) => prev.map((r) => (r.id === id ? fill(r) : r)))
  }

  /** What the row becomes when the exchange settles — every field the
   *  backend's account of the attempt has, whichever way it ended. */
  const settledRun = (run: ApiRun, result: ApiSendResult): ApiRun => ({
    ...run,
    outcome: result.outcome,
    // The backend's account of which record answered, never an echo of the
    // path we sent.
    environment: result.environment,
    // WHERE IT WENT OUT FROM, in the backend's account. The panel knows
    // which route it configured; what it must show is the one the send
    // actually took, read off the same record the address came from.
    route: result.route,
    request: result.request,
    remoteAddr: result.remoteAddr,
    dnsAddresses: result.dnsAddresses,
    timings: result.timings,
    certificates: result.certificates,
    response: result.response,
    failure: result.failure,
  })

  const stop = async (): Promise<void> => {
    const run = untrack(pending)
    if (run === null) return
    try {
      await services.cancelRequest(run.token)
    } catch (err) {
      // The Stop was refused — the exchange settled between the click and
      // the call, which is a race a person cannot see and does not need to.
      // The reason goes to the surface's one error line rather than onto the
      // run: the run's own ending is whatever its send answers, and this
      // must not overwrite it.
      setError(message(err))
    }
  }

  /**
   * Give a secret variable its value.
   *
   * THE ONE CALL IN THIS STORE THAT CARRIES A CREDENTIAL, and it carries it
   * one way: the value goes out and nothing about it comes back. Nothing here
   * keeps it, no signal holds it, and the environment the editor is showing
   * is re-read afterwards from the FILE — which holds the name and never the
   * value (§6.3), so the re-read cannot bring it back either.
   *
   * The refusal is the store's ordinary one (`error`), because that is what
   * every surface in this panel already reads. A sealed vault is not refused
   * here at all: it travels as the canonical sealed error and the dispatcher
   * raises the unlock, exactly as a send does (nocx-pgp9c.7).
   */
  const bindSecret = async (variable: string, value: string): Promise<boolean> => {
    const handle = untrack(activeCollection)
    const relPath = untrack(() => environmentFor(handle))
    if (handle === '' || relPath === '' || variable === '') return false
    try {
      await services.bindSecret(handle, relPath, variable, value)
      setError('')
      // The backend scope answer has changed, so a reference to it stops
      // being unanswered in the address field.
      await refreshVariables()
      return true
    } catch (err) {
      setError(message(err))
      return false
    }
  }

  const readEnvironment = async (relPath: string): Promise<ApiEnvironment | null> => {
    const handle = untrack(activeCollection)
    if (handle === '') return null
    try {
      const result = await services.readEnvironment(handle, relPath)
      setError('')
      return result.environment
    } catch (err) {
      setError(message(err))
      return null
    }
  }

  const writeEnvironment = async (relPath: string, environment: ApiEnvironment): Promise<void> => {
    const handle = untrack(activeCollection)
    if (handle === '') return
    try {
      await services.writeEnvironment(handle, relPath, environment)
      setError('')
      // The file is the truth (§6.4), so what the picker offers next comes
      // from a re-read rather than from what was just sent: a new
      // environment appears because the folder now has it, which is the same
      // route a colleague's `git pull` takes.
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }
  const readFolderVariables = async (relPath: string): Promise<ApiFolderVariablesResult> => {
    const handle = untrack(activeCollection)
    if (handle === '') return { variables: null, error: '' }
    try {
      const result = await services.readFolder(handle, relPath)
      return { variables: result.variables, error: '' }
    } catch (err) {
      return { variables: null, error: message(err) }
    }
  }

  const writeFolderVariables = async (
    relPath: string,
    variables: readonly ApiParam[],
  ): Promise<ApiFolderVariablesResult> => {
    const handle = untrack(activeCollection)
    if (handle === '') return { variables: null, error: '' }
    try {
      const result = await services.writeFolder(handle, relPath, [...variables])
      await refresh()
      return { variables: result.variables, error: '' }
    } catch (err) {
      return { variables: null, error: message(err) }
    }
  }

  /** The question asked before an import takes somebody's unsaved work with
   *  it. It NAMES the request, because "are you sure" is a question about
   *  nothing — the person has to recognise what is on the table before they
   *  can answer for it. The two sentences are the two ways the form holds
   *  work that is not on disk (`unsavedWork`), said in the words that are
   *  true of each: one has a file it has drifted from, the other has never
   *  had one. */
  const unsavedQuestion = (request: ApiRequest, act: 'import' | 'close'): string => {
    const named = request.name.trim() === '' ? 'The request in the form' : `"${request.name}"`
    const never = selected() === null
    if (act === 'import') {
      return never
        ? `${named} has never been saved. Importing this curl line replaces it, and it is gone.`
        : `${named} has unsaved changes. Importing this curl line replaces it, and they are gone.`
    }
    return never
      ? `${named} has never been saved. Closing it now discards it.`
      : `${named} has unsaved changes. Closing it now discards them.`
  }

  /**
   * What closing would cost, or '' when it would cost nothing.
   *
   * A draft with a FILE behind it costs nothing: closing writes the last
   * edit before it lets go. The only work a close can take is a draft that
   * has nowhere to be written — a curl line converted with no collection
   * open — so that is the only state with a question in it. The pane raises
   * the ask; this is the sentence and the fact behind it.
   */
  const closeQuestion = (): string => {
    const open = draft()
    return open === null || selected() !== null ? '' : unsavedQuestion(open, 'close')
  }

  /**
   * Take the request out of the form.
   *
   * THE FILE IS NOT TOUCHED. What goes is the form's attachment to it: the
   * draft, the snapshot it is compared against, and the selection the tree
   * marks. `activeFolder` stays — closing what was in a folder is not
   * leaving the folder, and the plus beside the person still means the
   * place they are still standing in.
   *
   * It does not ask. Whoever closes it has already been told what it costs
   * (`closeQuestion`), the way the delete on this surface works.
   */
  const closeRequest = async (): Promise<void> => {
    await flushDraft()
    setSelected(null)
    setDraft(null)
    setSaved(null)
    setDraftFolder('')
    setNotes([])
    setError('')
  }

  const importCurl = async (line: string, into?: string): Promise<void> => {
    let result: ApiImportCurlResult
    try {
      result = await services.importCurl(line)
    } catch (err) {
      setError(message(err))
      return
    }
    // THE ASK COMES AFTER THE PARSE, AND THAT ORDER IS THE POINT. The
    // conversion writes nothing — it is a VALUE and not a file (design §10)
    // — so running it first costs a round trip and buys the right question:
    // a line that is not a curl command is refused on its own terms, and
    // nobody is asked to discard their work for an import that was never
    // going to happen.
    //
    // WHY AN ASK RATHER THAN A SECOND UNTITLED REQUEST: there is one form,
    // one draft and one `selected` in this store, so "leave the old request
    // where it was" can only mean its FILE, never its edits — the edits live
    // nowhere else. An answer that could not carry them is not an answer to
    // "unsaved work is destroyed", so the choice is put to the person whose
    // work it is, in the kit's own `showConfirm`, which is where "are you
    // sure" lives in this product.
    //
    // AND THIS RAISE DOES NOT BELONG IN THIS FILE — see the header. It goes
    // beside the delete's confirmation in api-pane.tsx, which is where this
    // surface asks. Whoever owns that file moves it, and it is not a lift:
    // two properties have to survive the move, and neither is obvious from
    // the lines being moved.
    //
    //  1. THE ASK COMES AFTER THE PARSE, and `store.importCurl` IS the
    //     parse. A pane that asks before calling it asks somebody to discard
    //     their work for a line that is then refused as not a curl command
    //     at all. `a line that is not a curl command is refused without
    //     asking anybody to discard anything` is the test that fails when
    //     that is got wrong.
    //  2. THE CONDITION AND THE SENTENCE STAY HERE. `unsavedWork` is a fact
    //     about the form, and which of the two sentences is true is read off
    //     `selected` — both belong to whoever holds `draft`, `saved` and
    //     `selected`, which is this file and not a component.
    //
    // So the move needs a seam, and there are two that work: split this into
    // a parse and an apply, with the pane asking between them; or give
    // `importCurl` the ask as a parameter, so the store keeps the order and
    // the sentence while the pane keeps the modal. The second is smaller and
    // leaves every test here standing. The choice is the pane owner's.
    // WHATEVER WAS IN THE FORM IS IN ITS FILE — BEFORE THE QUESTION, which
    // is what decides whether there is one. Edits reach the file on their
    // own now, so the only work an import can still take is a draft with
    // nowhere to be written: a curl line converted with no collection open.
    // Asking first would name edits this line is about to save.
    await flushDraft()
    const open = draft()
    if (open !== null && unsavedWork()) {
      const proceed = await showConfirm(
        unsavedQuestion(open, 'import'),
        'Discard and import',
        'Cancel',
      )
      // NOTHING MOVES ON A NO — not the draft, not the notes, not the error.
      // The import did not happen, so the form is exactly as it was found.
      if (!proceed) return
    }
    // No file behind it yet, so nothing is selected — Send stays refused
    // until the request is saved into a collection, which is honest: there
    // is nothing on disk for api.request.send to send.
    setSelected(null)
    setSaved(null)
    // WHERE IT WILL GO WHEN IT DOES. The ask answered this, and its answer
    // is kept here rather than re-read at Save from wherever the person has
    // wandered to since: `undefined` is "the ask named none", and only then
    // does the folder they are standing in decide. `activeFolder` itself is
    // deliberately NOT moved — the import detaches the form from its file,
    // which is a fact about the form and not about where anybody is.
    setDraftFolder(into ?? untrack(activeFolder))
    // THE IMPORTER NAMED IT, off the line the person pasted, so there is
    // no offer to make: a curl line converted into the form arrives called
    // something, and rewriting that from the address would be this store
    // arguing with the importer about one request's name.
    offered = ''
    setDraft(foldQueryIntoParams(adoptImportedRequest(result.request)))
    setNotes(result.unsupported)
    setError('')
    // AND IT GETS ITS FILE NOW. Nothing is saved by a gesture on this
    // surface any more, so "nothing is written until the request is saved"
    // (design §10) resolves to this line: the request appears in the tree,
    // in the folder the ask named, and Send is legal because there is
    // something on disk for it to send. With no collection open there is
    // nowhere to write it and the draft stays fileless, which is the honest
    // state and the one `unsavedWork` still describes.
    await saveDraftAs()
  }

  const importPostman = async (source: ImportSource, dest: string): Promise<void> => {
    try {
      const result = await services.importPostman(source, dest)
      // Both importers' "what did not come across" is one vocabulary — a
      // feature named, and why — so the surface holds one list of them.
      const carried: ApiImportNote[] = result.unsupported satisfies PostmanNote[]
      setNotes(carried)
      setError('')
      // The folder is on disk now; the listing is what puts it in the tree.
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }

  const setRunView = (id: number, view: ApiRunView): void => {
    setRuns((prev) => prev.map((r) => (r.id === id ? { ...r, view } : r)))
  }

  const startWatching = (): void => {
    if (watcher === undefined || disposed || unsubscribes.length > 0) return
    unsubscribes.push(watcher.subscribeChanged(onCollectionChanged))
    unsubscribes.push(
      watcher.onConnect(() => {
        // A reconnect is a NEW connection, and a binding is bounded by the
        // connection that minted it — so the id we hold addresses nothing and
        // the set the backend was told about is gone with it. Both records
        // are dropped, which makes the next sync re-open and re-send rather
        // than suppress an unchanged set and leave the panel detached from
        // the change stream (AD-9).
        bindingId = null
        publishedPaths = null
        void syncWatchSet()
      }),
    )
    // No sync here: the set is empty until the first listing says what the
    // open folders are, and files.watch with an empty set is a round trip
    // that establishes nothing. refresh() — which the pane's mount issues
    // next — is what publishes.
  }

  const dispose = (): void => {
    disposed = true
    for (const off of unsubscribes) off()
    unsubscribes = []
    const id = bindingId
    bindingId = null
    publishedPaths = null
    if (id !== null && watcher !== undefined) {
      // Its watches go with it (files.close tears them down), so there is no
      // watch-with-an-empty-set first: one call, and a refusal has nobody
      // left to tell.
      void watcher.close(id).catch(() => undefined)
    }
  }

  return {
    collections,
    activeCollection,
    environments,
    activeEnvironment,
    selected,
    draft,
    saved,
    dirty,
    runs: visibleRuns,
    notes,
    error,
    loading,
    scopeVariables,
    pending,
    defaultRoot,
    watchMode,
    watchDegradedReason,
    readFolderVariables,
    writeFolderVariables,
    watchFailed,
    variableAnswer,
    bindSecret,
    startWatching,
    dispose,
    refresh,
    openFolder,
    createCollection,
    createFolder,
    closeFolder,
    connections,
    loadConnections,
    pointAt,
    enterFolder,
    activeFolder,
    draftFolder,
    closeRequest,
    unsavedWork,
    closeQuestion,
    openRequest,
    saveDraftAs,
    saveDraft,
    deleteRequest,
    newRequest,
    duplicateRequest,
    moveRequest,
    editDraft,
    setEnvironment,
    send,
    stop,
    readEnvironment,
    writeEnvironment,
    importCurl,
    importPostman,
    setRunView,
  }
}
