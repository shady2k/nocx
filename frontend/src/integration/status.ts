// The session integration axis, renderer side (nocx-dvql / nocx-5uu5).
//
// The backend publishes session.integrationChanged: whether this session's
// shell integration is live, and when it is not, why. This module owns three
// things and nothing else — the subscription seam, the words the product
// says, and the memory of which card the user has already seen.
//
// It owns the WORDS deliberately. A reason is a closed server vocabulary
// (ssh.RefusalReason) and every surface that shows one — the tab's tooltip,
// the card, the details dialog — must say the same thing about it, or the
// user reads two different diagnoses of one session. One table, one owner
// (AD-8).
//
// The words never name a third-party program. nocx cannot see which one took
// the shell over — AD-6 forbids reading the byte stream and the process table
// is a race — so naming one would be a guess presented as a finding. The
// remedy surface carries the observation instead, labelled as a guess.

import type { Dispatcher } from '../dispatcher'
import type { SessionIntegrationChanged } from '../generated/session.integrationChanged'

export type IntegrationReason = NonNullable<SessionIntegrationChanged['reason']>

/** One integration fact, delivered with its session id intact. */
export type IntegrationFactHandler = (fact: SessionIntegrationChanged) => void

/** Subscribe to the server-initiated session.integrationChanged notification.
 *  The wire shape is guarded at the boundary like files.changed and
 *  lifecycle.changed (the same unsolicited-notification defect class): a
 *  payload without a string sessionId and a string shell is not a fact and is
 *  not delivered. */
export function subscribeIntegrationChanged(
  dispatcher: Dispatcher,
  handler: IntegrationFactHandler,
): () => void {
  return dispatcher.subscribe('session.integrationChanged', (params: unknown) => {
    const p = params as SessionIntegrationChanged
    if (p && typeof p.sessionId === 'string' && typeof p.shell === 'string') handler(p)
  })
}

/** True while this session is in a state the product should mark. `starting`
 *  is NOT degraded: the shell has not failed, it has not finished proving
 *  itself, and marking it would put a warning on every tab for its first
 *  seconds. */
export function isDegraded(fact: SessionIntegrationChanged | null): boolean {
  return fact !== null && (fact.status === 'conventional' || fact.status === 'lost')
}

// ── which shell nocx started ──────────────────────────────────────────────

/** The shells nocx installs a launcher for, plus the honest catch-all.
 *
 *  This exists because the remedy has to name the file nocx's OWN launcher
 *  sources: `internal/shellintegration/launcher_bash.go` sources
 *  `~/.bashrc`, `launcher_zsh.go` sources `~/.zshrc`. Advice about a file
 *  nocx never reads cannot work, and advice naming a shell the session is
 *  not running is how a zsh user was handed `bash -lic …` (nocx-0mqs).
 *
 *  The renderer derives this once, here, so the fix panel and anything that
 *  comes after it cannot disagree about which shell a session is (AD-8). */
export type ShellFamily = 'bash' | 'zsh' | 'other'

/** The family of the shell the backend says it started. The wire carries a
 *  path; a login shell is conventionally argv[0]-prefixed with a dash, so
 *  that is stripped before matching. The match is on the whole basename —
 *  `bashful` is not bash, and a prefix match would put `~/.bashrc` in front
 *  of somebody who has never had one. */
export function shellFamily(shellPath: string): ShellFamily {
  const base = shellPath.replace(/\/+$/, '').split('/').pop() ?? ''
  const name = base.startsWith('-') ? base.slice(1) : base
  if (name === 'bash') return 'bash'
  if (name === 'zsh') return 'zsh'
  return 'other'
}

/** The environment variable nocx exports before the user's startup file runs
 *  (`internal/shellintegration`: `ActivationEnv`, and the `export` in
 *  `launch.go`). It is the whole reason the remedy below is specific rather
 *  than a bisect: a block that takes the shell over can test for this and
 *  stand aside. */
const ACTIVATION_ENV = 'NOCX_SHELL_INTEGRATION'

/** Everything the fix text needs to know about the shell that ran. */
interface ShellFacts {
  readonly path: string
  readonly family: ShellFamily
  /** The startup file nocx's launcher sources for this shell. */
  readonly startupFile: string
}

function shellFacts(path: string): ShellFacts {
  const family = shellFamily(path)
  return {
    path,
    family,
    startupFile:
      family === 'zsh' ? '~/.zshrc' : family === 'bash' ? '~/.bashrc' : 'your shell startup file',
  }
}

/** A path is pasted into a command line, so a path with a space in it has to
 *  survive the paste. Single quotes, and only when the path needs them —
 *  quoting `/bin/zsh` would make the line look like something it is not. */
function shellQuote(path: string): string {
  if (/^[A-Za-z0-9._\-/]+$/.test(path)) return path
  return `'${path.replace(/'/g, `'\\''`)}'`
}

/** What nocx can tell the user to do, written for the shell that actually
 *  ran. Absent for every reason where the answer is not the user's to
 *  change — an empty "How to fix" that says "try again" teaches the user
 *  that the button never helps. */
interface IntegrationFix {
  /** Why this works, in prose. Names the shell and the file, never a
   *  third-party program. */
  readonly lead: string
  /** The lines to paste. Written for this shell, and for no other. */
  readonly snippet: string
}

/** What the product says about one degraded session: a headline the user can
 *  read at a glance and one sentence that is true. */
export interface IntegrationMessage {
  /** The card's title and the tab mark's label. */
  readonly title: string
  /** One sentence. No program names, no host names, no paths, no error
   *  text — a remote publish failure is an SFTP status code and a path,
   *  meaningful in a log and meaningless in a pane (nocx-viil). */
  readonly description: string
  /** What is true right now, for the details chain. */
  readonly happening: string
  /** The last step that did work, for the details chain. */
  readonly lastGoodStep: string
  /** The remedy, when nocx has one that is honest for this reason and this
   *  shell. Absent where the answer is not the user's to change. */
  readonly fix?: IntegrationFix
}

/** The one place a reason becomes English.
 *
 *  The first seven strings were agreed with the owner rather than invented in
 *  review, which is what nocx-viil requires. The rest arrived with the
 *  integration-delivery-carrier design and have NOT been through that
 *  agreement yet — they are here because the alternative is worse: every one
 *  of those reasons reached the user as "cannot say why" while the backend
 *  knew exactly which of thirty things had happened, and a soft degrade the UI
 *  contradicts is how a feature that does not exist survives a release. Each
 *  says what happened and the last step that did work, names no program and no
 *  path, and claims nothing nocx did not observe. The wording is the
 *  coordinator’s to review; the coverage is not optional.
 *
 *  `startup-did-not-return` and `handshake-timeout` are deliberately different
 *  sentences for two different amounts of knowledge, and the difference is the
 *  point. The first is emitted when the shell reported entering nocx's rcfile
 *  and never reported leaving the user's startup — nocx knows where it
 *  stopped. The second is emitted when nothing was reported at all: the shell
 *  did not answer, and the backend does NOT know that anything intercepted it.
 *  Giving the second the first's sentence would state a finding nocx does not
 *  have, which is how a zsh user was handed advice about bash. */
interface MessageTemplate extends Omit<IntegrationMessage, 'fix'> {
  /** Resolved against the shell that actually ran, which is the whole point:
   *  a fix written once, for a shell nobody named, is the defect. */
  readonly fix?: (shell: ShellFacts) => IntegrationFix
}

/** The remedy for a shell that never answered: guard the block that took it
 *  over on the variable nocx exports before the startup file runs.
 *
 *  This is nocx's own measurement, not a textbook. The activation variable is
 *  in the environment before `~/.bashrc` / `~/.zshrc` is sourced, so the
 *  guard is available to every affected user, and it is what people hit by
 *  the same class of failure in other terminals converge on. The advice it
 *  replaces — halve your rc file and put it back a piece at a time — is what
 *  you say when you know nothing, and here nocx knows something. */
function standAsideFix(shell: ShellFacts): IntegrationFix {
  const lines = [
    `# nocx exports ${ACTIVATION_ENV}=1 before ${shell.startupFile} runs.`,
    '# Wrap the block that hands your shell to another program, so it stands',
    '# aside for nocx and still runs in every other terminal:',
    '',
    `if [ -z "$${ACTIVATION_ENV}" ]; then`,
    '  # the lines that take the shell over go here',
    'fi',
  ]
  // A shell nocx has no launcher for still gets the variable — it is
  // exported for every session — but not a command line invented for it.
  if (shell.family !== 'other') {
    lines.push(
      '',
      '# To see whether the shell reaches a prompt at all:',
      `${shellQuote(shell.path)} -ic 'echo nocx-reached-a-prompt'`,
    )
  }
  return {
    lead:
      `nocx started ${shell.path} and exports ${ACTIVATION_ENV}=1 before ` +
      `${shell.startupFile} runs. A block there that hands the shell to another ` +
      `program can test for it and stand aside — nocx then reaches a prompt, and ` +
      `every other terminal you open is unchanged.`,
    snippet: lines.join('\n'),
  }
}

/** What is true right now for every reason but `channel-lost`: the session is
 *  a working terminal and the structure around it is off. One string rather
 *  than thirty copies of it — the sentence is the same fact each time, and a
 *  reason that needed a different one would be a reason whose session is in a
 *  different state. */
const PLAIN_TERMINAL =
  'This tab is a plain terminal: command blocks and the command editor are off.'

/** One "not integrated" template. The title is the same for every reason
 *  because the headline answers "did this work", which has one answer; the
 *  description and the last good step are what differ, and they are what the
 *  user reads to know which of thirty things happened. */
function notIntegrated(
  description: string,
  lastGoodStep: string,
  fix?: (shell: ShellFacts) => IntegrationFix,
): MessageTemplate {
  return { title: 'Not integrated', description, happening: PLAIN_TERMINAL, lastGoodStep, fix }
}

const MESSAGES: Record<IntegrationReason, MessageTemplate> = {
  'handshake-timeout': notIntegrated(
    'Your shell did not answer nocx in time, so this tab is a plain terminal.',
    'nocx started the shell and opened its channel. The shell never answered on it.',
    standAsideFix,
  ),
  // The stage, not the culprit. nocx knows its rcfile started and never got
  // control back; exec, exit, a hung attach and a crashed shell all produce
  // this identically, so the sentence names what did not happen and stops
  // there. It is the reason the owner's own wording was written for, and the
  // only one that can carry it honestly (nocx-yww2).
  'startup-did-not-return': notIntegrated(
    'Your shell startup files did not return control to nocx, so this tab is a plain terminal.',
    'nocx started the shell and its startup files began. They never handed control back.',
    standAsideFix,
  ),
  'channel-lost': {
    title: 'Integration lost',
    description: 'nocx lost its channel to this shell, so this tab is now a plain terminal.',
    happening: 'This tab is a plain terminal. Commands still run; they are no longer recorded.',
    lastGoodStep: 'The shell was integrated and answering. Its channel then ended.',
  },
  'unsupported-shell': notIntegrated(
    'nocx has no integration for this shell, so this tab is a plain terminal.',
    'The connection opened. Integration was declined before the shell started.',
  ),
  'no-secure-temp': notIntegrated(
    'nocx could not create a private temporary file for this session, so integration was not installed.',
    'The connection opened. Installing the integration stopped before the shell started.',
  ),
  'remote-command': {
    title: 'Not integrated',
    description:
      'This connection runs a configured command instead of a shell, so there is nothing to integrate.',
    happening: 'This tab runs the configured command. Command blocks do not apply to it.',
    lastGoodStep: 'The connection opened and ran what it was configured to run.',
  },

  // nocx's OWN guard, and the only reason on this list the far side had no
  // part in: the command nocx was about to run exceeded the bound it declares
  // for a remote command, so it was refused before it was sent rather than
  // truncated into something else (nocx-e4ir3). A user cannot cause this and
  // cannot fix it, so there is no remedy offered — it is a defect in nocx,
  // and the sentence says so rather than implying the host did something.
  'command-too-long': {
    title: 'Not integrated',
    description:
      'nocx refused to send its own start-up command to this host because it was longer than nocx allows, so this tab is a plain terminal.',
    happening: PLAIN_TERMINAL,
    lastGoodStep: 'The connection opened and authenticated. nocx stopped before sending anything.',
  },

  // ── the selective-refusal matrix, by real SSH channel type ─────────────
  //
  // A server or an intermediary may permit some channels and not others, and
  // each row is a different thing the user's administrator can change. Before
  // these, all five arrived as "cannot say why".
  'session-unavailable': notIntegrated(
    'This connection would not open a session of its own for nocx, so this tab is a plain terminal.',
    'The connection opened and authenticated once. Opening another session on it was refused.',
  ),
  'pty-unavailable': notIntegrated(
    'This connection will not give nocx a terminal, so there is no interactive shell here to integrate.',
    'The connection opened and nocx asked for a terminal. The request was refused.',
  ),
  'exec-refused': notIntegrated(
    'This connection would not run the line nocx starts a shell with, so it started your shell the plain way instead.',
    'The connection opened and gave nocx a terminal. Running nocx’s own line on it was refused.',
  ),
  'exec-substituted': notIntegrated(
    'This connection runs one fixed command whatever it is asked for, so there is no shell here to integrate.',
    'The connection opened and ran what it is configured to run, in place of what nocx asked for.',
  ),
  'publish-unavailable': notIntegrated(
    'nocx could not copy its shell integration to this host, so there was nothing on it to start.',
    'The connection opened and authenticated. Copying the integration across was refused.',
  ),

  // ── the bootstrap, before the shell exists ─────────────────────────────
  //
  // These are the far host answering nocx’s own setup, or failing to. None
  // of them is the user’s to change, which is why none carries a fix: an
  // empty "How to fix" that says "try again" teaches the user that the button
  // never helps.
  'loader-termios-unavailable': notIntegrated(
    'nocx could not put the terminal into the state its setup needs, so it gave the terminal back untouched.',
    'The connection opened and nocx’s line started. Preparing the terminal stopped there.',
  ),
  'bootstrap-interrupted': notIntegrated(
    'The connection ended while nocx was still setting this session up.',
    'nocx’s line started on the far host. The channel ended before the setup finished.',
  ),
  'bootstrap-protocol': notIntegrated(
    'What answered nocx during setup was not nocx’s own protocol, so nocx stopped rather than act on it.',
    'nocx’s line started on the far host. What came back where nocx’s own frame belongs was something else.',
  ),
  'bootstrap-timeout': notIntegrated(
    'The far host answered nocx once and then did not finish the setup in time.',
    'nocx’s line started and announced itself. It never reached an outcome.',
  ),
  // The one reason on this list that is worth reading twice: the far side
  // emits each of its steps exactly once, so a step answered out of turn was
  // written by something that is not nocx’s own setup. The sentence says
  // what happened and claims nothing about who did it.
  'bootstrap-out-of-order': notIntegrated(
    'An answer arrived out of turn during nocx’s setup, so nocx refused it and stopped.',
    'nocx’s line started on the far host. A step was answered that was not the next one.',
  ),
  'receiver-unready': notIntegrated(
    'Nothing on the far host answered nocx’s setup, so this tab is a plain terminal.',
    'The connection opened and nocx’s line was sent. Nothing announced itself on it.',
  ),
  'stage-too-large': notIntegrated(
    'nocx’s setup was larger than nocx allows itself to send, so none of it was sent.',
    'The connection opened. nocx refused its own oversized setup before writing a byte of it.',
  ),
  'stage-digest-unavailable': notIntegrated(
    'The far host has no way to check that nocx’s setup arrived intact, and nocx does not run setup it cannot verify.',
    'The connection opened and nocx’s line started. Nothing on the host could compute a checksum.',
  ),
  'stage-digest-mismatch': notIntegrated(
    'nocx’s setup did not arrive on the far host exactly as it was sent, so nocx did not run it.',
    'The connection opened and nocx’s setup was sent. Checking it on the far host did not match.',
  ),
  'stage-fd-unavailable': notIntegrated(
    'The far host could not hand nocx’s verified setup to the shell, so none of it was run.',
    'The connection opened, and nocx’s setup arrived and verified. Passing it to the shell failed.',
  ),
  'stage-source-failed': notIntegrated(
    'nocx’s setup ran on the far host and gave control back instead of starting your shell.',
    'The connection opened, and nocx’s setup arrived, verified and ran. It did not take the session over.',
  ),
  'secret-too-large': notIntegrated(
    'nocx’s key for this session was larger than nocx allows itself to send, so it was not sent.',
    'The connection opened and nocx’s setup ran. nocx refused its own oversized frame.',
  ),
  'secret-malformed': notIntegrated(
    'The far host did not recognise nocx’s key for this session, so the session has no channel of its own.',
    'The connection opened and nocx’s setup ran and read the key. It was not the shape a key must be.',
  ),
  'secret-not-for-this-session': notIntegrated(
    'The key that reached the far host was addressed to a different session, so the far host refused it.',
    'The connection opened and nocx’s setup ran and read a key. It named another session.',
  ),
  'capability-fd-unavailable': notIntegrated(
    'The far host could not open a private place to keep nocx’s key for this session, so nocx did not hand one over.',
    'The connection opened and nocx’s setup ran. Opening private storage for the key failed.',
  ),
  'capability-unlink-failed': notIntegrated(
    'The far host could not make nocx’s key nameless before writing it, so nocx wrote nothing at all.',
    'The connection opened and nocx’s setup ran. Removing the name failed, so no key was written.',
  ),
  'capability-write-failed': notIntegrated(
    'The far host could not store nocx’s key for this session, so the session has no channel of its own.',
    'The connection opened and nocx’s setup ran. Writing the key failed.',
  ),
  'generation-unavailable': notIntegrated(
    'This host has no copy of nocx’s shell integration to start. The next connection installs it and tries again.',
    'The connection opened and nocx’s setup ran. It found nothing installed to hand the shell to.',
  ),
  'channel-unavailable': notIntegrated(
    'This connection will not give nocx a channel of its own to the shell, so this tab is a plain terminal.',
    'The connection opened and authenticated. Opening nocx’s own channel on it was refused.',
  ),

  unknown: notIntegrated(
    'nocx could not set up shell integration for this session, and cannot say why.',
    'The session opened. Integration stopped somewhere nocx cannot name.',
  ),
}

/** Every reason the wire can carry, as a runtime list.
 *
 *  It is not a copy of the contract's enum, it IS the enum: `MESSAGES` above
 *  is typed `Record<IntegrationReason, MessageTemplate>`, `IntegrationReason`
 *  is read off the GENERATED renderer type, and `npm run contracts:check` —
 *  a gate on every commit — is what proves that generated type still matches
 *  `contracts/session.integrationChanged.schema.json`. So `tsc` refuses a
 *  member with no message and refuses a message with no member, and the keys
 *  below are exactly the vocabulary the server sends.
 *
 *  That chain is why this is exported rather than the test reading the schema
 *  file: the containerized `vitest` assembles its workspace from `frontend/`
 *  alone, so a path out to `contracts/` resolves to nothing there — and it
 *  need not, because the schema has already been checked into this module's
 *  own types by the time the test runs. */
export const INTEGRATION_REASONS = Object.keys(MESSAGES) as IntegrationReason[]

/** The message for a fact, or null when there is nothing to say — which is
 *  every non-degraded status. A reason the renderer does not recognise falls
 *  back to `unknown` rather than to silence: an unrenderable reason is still
 *  a degraded session, and silence is the defect. */
export function integrationMessage(
  fact: SessionIntegrationChanged | null,
): IntegrationMessage | null {
  if (!isDegraded(fact) || !fact) return null
  const { fix, ...words } = MESSAGES[fact.reason as IntegrationReason] ?? MESSAGES.unknown
  return fix ? { ...words, fix: fix(shellFacts(fact.shell)) } : words
}

/** How much of an executable name the process table keeps, in bytes.
 *
 *  It is the kernel's number, not a display choice: `observedProcess` is
 *  darwin's `p_comm` (`internal/procwatch`, `commLen` — MAXCOMLEN), a
 *  fixed-width field. That width is the whole reason the observation can be
 *  shown at all: a field this size structurally cannot carry a path, an
 *  argument or a command line, so nothing of the user's own text can ride
 *  into a surface that is not theirs. The renderer states the number again
 *  because the wire does not carry "this one was cut" — the contract
 *  describes the fact, and a flag on it is a change to make deliberately
 *  rather than inside a wording fix (nocx-3f6a). */
const COMM_FIELD_BYTES = 16

/** The kernel truncates by bytes, so that is what is counted here: a
 *  fifteen-character name of two-byte characters was already cut when it
 *  reached the wire. */
function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

/** What nocx observed running where it expected the shell, as one sentence,
 *  in one place.
 *
 *  Every surface that shows the observation reads this function, so none of
 *  them can claim it more strongly than another (AD-8). It is labelled a
 *  guess IN THE SENTENCE rather than by placement: it comes from the process
 *  table, which can be raced, and never from the byte stream, which AD-6
 *  forbids the backend to interpret, so a reader who sees only this line
 *  still knows what it is worth. Null when the backend observed nothing — an
 *  absent observation is silence, never a hedge.
 *
 *  A name that fills the field is quoted with an ellipsis and explained
 *  (nocx-aimo). The owner read `zsh (kiro-cli-te` on the installed build: a
 *  word stopped mid-syllable, which reads as a defect in nocx and is
 *  actually the field doing its job. Nothing downstream can tell a name that
 *  was cut from one that happens to be exactly this long — the wire carries
 *  the string and no flag — so the sentence says "may be cut short", which
 *  is the only claim that is true either way. The hedge is spent only where
 *  it is needed: on every observation it would teach the reader to skip it. */
export function observationSentence(fact: SessionIntegrationChanged): string | null {
  const observed = fact.detail?.observedProcess
  if (!observed) return null
  const filled = byteLength(observed) >= COMM_FIELD_BYTES
  const name = filled ? `${observed}…` : observed
  const sentence = `Best guess, not a finding: nocx saw "${name}" running where it expected the shell.`
  if (!filled) return sentence
  return (
    `${sentence} The system keeps only the first ${COMM_FIELD_BYTES} characters of a ` +
    `process name and nocx reads no further — never the command line — so this one may ` +
    `be cut short.`
  )
}

/** What shell integration is, carried by the build rather than linked to
 *  (nocx-qs68).
 *
 *  It used to be a GitHub blob URL. That link needs three things the shipped
 *  app cannot promise — a network, a system browser (`shell.openUrl` answers
 *  -32601 where there is no native runtime) and a path that survives a
 *  rename or a default-branch change — to explain a misconfiguration on the
 *  machine the user is already sitting at. Prose in the bundle needs none of
 *  them, and it is versioned with the build that shows it.
 *
 *  It lives beside the reason table because it is the same voice answering
 *  the same question one level up, and a second copy of these words in a
 *  document nothing links to is how the first one came to say `bash -lic` to
 *  zsh users for a month. */
export const INTEGRATION_EXPLANATION: readonly string[] = [
  'An integrated tab knows where each command starts and ends, so nocx can draw command blocks, record what you ran, and offer the command editor.',
  'A plain terminal runs your shell and its own prompt. Everything still works; what is missing is the structure around it — no command blocks, no command editor, and nothing recorded.',
  'nocx says this only when it tried to integrate a session and did not manage it. A tab that was never meant to be integrated says nothing at all, so this is always about something on the machine that can be changed.',
  'The mark on the tab stays for as long as the session is degraded. "Don\'t show again for this shell" stops the card for this shell on this machine; it leaves the mark alone, because the mark is the honest state of the session.',
]

// ── which shells the user has silenced ────────────────────────────────────

/** The narrow storage seam, injectable so the store is testable without a
 *  browser and so a locked-down webview cannot throw on construction. */
export interface IntegrationSilenceStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

/** The record is a JSON array of `${shell} ${reason}` lines, where a reason
 *  of `*` means every reason for that shell — and `*` is the only reason
 *  ever written now (see the store below).
 *
 *  The key still says `seen` because that is where the record already is
 *  (nocx-wfxz). Renaming it would throw away the silences users chose on the
 *  shipped build — the one thing in there that IS a decision — to tidy a
 *  string nobody sees. The per-card lines that build also wrote are read as
 *  nothing and drop out on the next write. */
const SILENCE_KEY = 'nocx.integration.seen.v1'
const ALL_REASONS = '*'

/** Remembers which shells the user has told nocx to stop raising cards for.
 *
 *  Exactly one thing writes here, and it is the user pressing "Don't show
 *  again for this shell" (nocx-wfxz). Drawing a card writes nothing, closing
 *  one writes nothing, and a session that ends with the card still up writes
 *  nothing — so a card closed before the user had worked out what it meant
 *  comes back with the next session that hits the same thing. The rule this
 *  replaces recorded the (shell, reason) pair the moment the card was DRAWN,
 *  which spent the card on a glance and never said so on the surface.
 *
 *  What is left is the standing decision, and it is per shell rather than per
 *  (shell, reason): the user answered about the shell, not about one way it
 *  failed. Deliberately not global either — a user who has accepted that
 *  their login shell is not integrated has said nothing about the next host
 *  they connect to.
 *
 *  WHY localStorage, and why here rather than in the backend: this is
 *  renderer presentation state — "has this person answered for this shell" —
 *  not backend authority, and putting it behind an RPC would make the card's
 *  first paint wait on a round trip. It must survive a restart, which rules
 *  out the run-scoped suppression the clipboard banner uses: a decision the
 *  user made on Monday must not be asked again every morning. And it must be
 *  per machine, which localStorage is and a synced setting would not be — the
 *  same profile on a second machine has a different shell and deserves the
 *  card. */
export class IntegrationSilenceStore {
  constructor(private storage: IntegrationSilenceStorage | null) {}

  /** Has the user silenced this shell? */
  isSilenced(shell: string): boolean {
    return this.read().has(shell)
  }

  /** Silence every card for this shell — the "Don't show again for this
   *  shell" action, and the only writer there is. */
  silenceShell(shell: string): void {
    if (!this.storage) return
    const silenced = this.read()
    if (silenced.has(shell)) return
    silenced.add(shell)
    try {
      this.storage.setItem(
        SILENCE_KEY,
        JSON.stringify([...silenced].map((s) => `${s} ${ALL_REASONS}`)),
      )
    } catch {
      // Storage full or denied. The card will be offered again, which is
      // the safe direction.
    }
  }

  /** The silenced shells, as shells. A line that does not end in the
   *  all-reasons marker is not a decision the user made and reads as nothing
   *  at all — which is what retires the per-card lines the previous build
   *  wrote. The shell is everything before the marker, so a path with a
   *  space in it survives the round trip. */
  private read(): Set<string> {
    if (!this.storage) return new Set()
    try {
      const raw = this.storage.getItem(SILENCE_KEY)
      if (!raw) return new Set()
      const parsed: unknown = JSON.parse(raw)
      if (!Array.isArray(parsed)) return new Set()
      const suffix = ` ${ALL_REASONS}`
      return new Set(
        parsed
          .filter((v): v is string => typeof v === 'string' && v.endsWith(suffix))
          .map((v) => v.slice(0, -suffix.length)),
      )
    } catch {
      // A corrupt or unreadable record reads as "nothing silenced": showing
      // a card the user silenced is a nuisance, never showing one is the
      // defect.
      return new Set()
    }
  }
}

/** localStorage when the webview allows it, null when it does not. A
 *  denied storage must not take the tab down on construction. */
export function safeSilenceStorage(): IntegrationSilenceStorage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}
