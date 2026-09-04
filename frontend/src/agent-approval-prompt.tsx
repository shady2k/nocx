/**
 * AgentApprovalPrompt — the renderer half of an approval question (nocx-z9hj4,
 * design §7.2/§7.3): a run suspended and a person is being asked to decide.
 * One kind of question whether the risk was an effect coming in (a policy
 * escalation) or a secret going out (an egress finding) — the wire sends
 * agent.approvalRequested either way, and this surface renders it either way.
 *
 * Since nocx-gycwo an answer has a WIDTH as well as a direction. Before it,
 * every answer was `once` and bound to the exact proposal by call id and
 * argument hash, so a person who had just allowed the assistant to read the
 * screen was asked about the SAME screen on their next question, and the only
 * place to say "stop asking me this" was a settings page in a vocabulary they
 * had never seen. A policy question offers allow and deny at once, in this
 * session and always when the backend can supply the standing binding. For a
 * command that binding is the complete canonical invocation; for a
 * non-command proposal the effect row already carried on the wire names what
 * the standing answer covers. The BACKEND applies the width: this surface
 * never edits the policy matrix, which would put a second owner on the
 * document the settings page owns (design §"Three wire changes").
 *
 * Two things it must never do. It must never derive the effect from the tool
 * name — `effect` is on the wire precisely so it does not, because a rule
 * keyed by a tool name is what ADR-0028 decision 4 forbids. And it must never
 * offer a standing answer to an EGRESS question: an egress ask means a tool
 * result contained secret-shaped material and nothing has reached the model
 * provider yet, so "always" there would mean "always send secrets to the
 * provider", which is not a decision anyone should make by clicking a button
 * sitting next to five others. Egress keeps Allow / Deny, once only.
 *
 * WHAT THE SESSION IS CALLED (nocx-vnzek). The wire carries the derived
 * resource, and for `kind: 'session'` that derivation IS the session id — an
 * internal handle that says nothing to the person being asked to decide. So
 * the surface names the pane instead, through the SAME derivation the tab
 * strip and the answer's tool-call line use (PaneManager.sessionWhere, which
 * is sessionDisplayName plus the machine), and says nothing at all when no
 * pane can be named.
 *
 * AND WHAT THE CALL DOES, IN A SENTENCE (nocx-njn8s). That was not enough.
 * A person approving `run` was shown the effect, then the raw argument blob
 * — `{"command": "df -h", "sessionId": "ab607…cf95"}` — with the id nocx-vnzek
 * had just taken off the tool-call line still inside it, and the MACHINE,
 * the fact that decides whether a destructive command lands on this laptop
 * or on a production host, never named at all. So a `run` proposal now reads
 * as a sentence — the command verbatim in its own block — with the machine
 * and the tab as rows beneath it (nocx-0mvpy.2).
 *
 * THE BLOB IS THE FALLBACK, AND IT IS NOT A LESSER ONE. That block is the
 * model's own proposal quoted verbatim, and paraphrasing it is only honest
 * while the paraphrase is EXHAUSTIVE — every argument accounted for. That
 * rule stands; what changed under it is written next.
 *
 * EXHAUSTIVE BY CONSTRUCTION, NOT PER TOOL (nocx-n7xha). njn8s satisfied
 * the rule by writing a sentence for exactly one shape — `run` with the two
 * arguments its schema declares — and leaving every other shape the blob.
 * That reading was too narrow twice over. For `readScreen` the blob's only
 * content IS the session id nocx-vnzek had just taken off the tool-call
 * line and njn8s off the run sentence, so the one surface still printing
 * the handle was the one asking a person to decide. And a third argument
 * on `run` put the whole proposal back into JSON rather than adding one
 * line. A GENERIC renderer that lists EVERY parsed argument as a named row
 * is exhaustive by construction — nothing can be dropped, because nothing
 * is selected — and it is honest for tools nobody has written a sentence
 * for yet. So the arguments are rows now, the blob is kept for the case a
 * row list cannot describe (arguments that do not parse, or that are not an
 * object), and njn8s's `run` sentence is untouched: rows sit beside it.
 *
 * EVERY FACT IS A ROW (nocx-0mvpy.2). The machine, the tab, the directory,
 * the parsed arguments and the effect are all rows of the ONE fact list, in
 * that order, so a person reads where a call lands and what it can do at a
 * glance — the lead is one sentence, and the two paragraphs below the facts
 * say only what cannot be a row: what approving covers, and how wide an
 * answer reaches. The where and the effect are rows even when the arguments
 * do not parse: the blob is the fallback for the ARGUMENTS, and where a call
 * lands does not depend on them.
 *
 * WHERE THE CALL LANDS HAS ONE OWNER ON THIS SURFACE. The machine and the
 * tab are rows, so the argument that names the session is NOT also a row
 * whenever a pane can be named — the rows state it, once. Only when no pane
 * holds the session does that argument's row appear, and it says exactly
 * that. One statement either way — two would be two surfaces owning one
 * fact, and the loser goes on advertising what it can no longer deliver.
 *
 * AND A GUESS IS NOT A FACT (AD-5). The window now says which directory the
 * call lands in, which nothing on it said before. nocx distinguishes a cwd
 * an OSC 7 report confirmed from one a session was merely opened with, and
 * an approval window that printed the second as fact would lie at the exact
 * moment lying is most expensive — so both cases have their own words, on
 * the row, through the ONE derivation PaneManager.sessionWhere already
 * exists to be (a second injected callback could disagree with it). Neither
 * wording promises more than it knows: the directory is the pane's AS OF
 * NOW, and a shell can `cd` between the question and the answer. Binding an
 * effect to its preconditions is nocx-d6gn4.1 and is not claimed here.
 *
 * AND THE SCAN'S FINDING IS EVIDENCE, DRAWN (nocx-swn1m). A skill write
 * carries the first static-scan match in the proposed body, and the skills
 * spec §6 layer 3 says what it is for: "A finding never silently downgrades
 * the result. On write it becomes evidence in the approval, naming the
 * pattern and the line." The wire has carried it since the kernel built it
 * and this surface drew nothing, so the person approving a body saw the
 * bytes without the pattern that was matched in them. It is drawn beside the
 * bytes now, and it is NOT a refusal: the write stays the person's to allow
 * or deny, which is the decision the spec made and not one this window may
 * remake. For a skill installed from a URL that reading is the whole of the
 * defence rather than a backstop — layer 2 (a drafting step kept untainted)
 * does not apply at all when a stranger wrote the body.
 *
 * AND THE SCRIPT A COMMAND NAMES IS SHOWN (nocx-872jc.3). `bash deploy.sh` is
 * eleven characters and the whole of its meaning is in a file this window said
 * nothing about, so approving it was approving a NAME. The file's current
 * contents are drawn beside the command, through the SAME viewer the Skills
 * page reads a skill file through, and labelled as what they are: a reading,
 * not the thing being approved. It changes nothing about the binding — what is
 * sent is byte for byte what it was — and it is not a scan: the bytes go to
 * the person, and reading them is theirs to do.
 *
 * What the surface must not overstate (design §7.2): approving covers the
 * call that is asking — it has NOT run, and no call after it in that response
 * will. It does NOT promise the domain is untouched: a permitted sibling
 * earlier in the same batch has already run. The sentence is on the surface,
 * where a person deciding reads it.
 */
import { For, Show } from 'solid-js'
import {
  ActionGroup,
  Badge,
  Button,
  CodeBlock,
  FactList,
  FileReadout,
  Prompt,
  Stack,
  StatusCard,
  type Fact,
  type FileReadoutOutcome,
  type StatusCardTone,
} from './ui'
import { EFFECT_LABEL } from './effect-labels'
import { scanPatternWords } from './scan-pattern-words'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import type { AgentApprove } from './generated/agent.approve'

/** How far an answer reaches — the wire's own vocabulary, not a second one. */
type ApprovalScope = AgentApprove['scope']

/**
 * The two tools this window says something EXTRA about, named once each
 * (nocx-69sew).
 *
 * The window is generic — every proposal renders from `effect`, the parsed
 * arguments and the pane rows, and nothing here derives an effect from a
 * tool name (ADR-0028 decision 4 forbids that, and the header says so). Two
 * PRESENTATION branches are keyed on the name anyway, because they are
 * statements only true of one tool: the command carrier gets njn8s's
 * sentence and its verbatim block, and fetch.url gets the row saying the
 * call leaves this machine.
 *
 * Those two literals are the whole of the coupling, and they used to be a
 * silent one. `run` was renamed `session.run` in d71263ab; `if (ask().tool
 * !== 'run')` went on compiling, went on passing ten tests written from the
 * component, and every real command proposal fell through the branch — no
 * lead sentence, no command block, no variable expansion. Nothing could see
 * it: the wire type said `string`, so 'run' was as valid a comparand as any
 * other string, and the tests carried the component's own belief.
 *
 * `AgentApprovalRequested['tool']` is now a union generated from the
 * contract's enum, which a Go test holds equal to the declaration table
 * (TestApprovalRequestedToolEnumMatchesTheTable). So `satisfies` below fails
 * to compile the moment a name here stops being a name the backend can
 * send, and the comparisons that read these constants fail with it — a
 * rename cannot leave this file comparing against a dead string and a green
 * suite.
 */
const COMMAND_TOOL = 'session.run' satisfies AgentApprovalRequested['tool']
const NETWORK_TOOL = 'fetch.url' satisfies AgentApprovalRequested['tool']

/** What the window keys its two by-name branches on, for the test that
 *  proves the chain from this file to the declaration table is unbroken. */
export const TOOLS_THIS_WINDOW_NAMES = {
  command: COMMAND_TOOL,
  network: NETWORK_TOOL,
} as const

/** What the command's variables read as, as the backend derived it. */
type ExpansionFacts = NonNullable<NonNullable<AgentApprovalRequested['expansion']>>
type ExpansionPart = NonNullable<ExpansionFacts['parts']>[number]

/** One file the proposed command names, read as the question was asked. */
type ScriptReading = NonNullable<AgentApprovalRequested['scripts']>[number]

/**
 * What the command DOES with the file, in a person's words (nocx-872jc.3).
 *
 * The two verbs are not the same act and the difference is the kind a person
 * deciding is owed: `bash x.sh` starts a subprocess that ends, while
 * `source x.sh` runs the file in the shell the person is sitting in, so
 * everything it sets outlives it. The parser already distinguishes them —
 * internal/content routes source to the privilege-change effect row for
 * exactly this reason — and this is where that distinction reaches a reader.
 */
const SCRIPT_VERB_WORDS: Record<ScriptReading['verb'], string> = {
  execute: 'the command runs this file as a script',
  source: 'the command reads this file into the shell itself, so what it sets outlives it',
}

/**
 * One reading, as FileReadout's own four-way answer to "what is on screen".
 *
 * The mapping is a `switch` over the wire's closed union with no default, so
 * a fifth refusal fails this return type rather than rendering as a viewer
 * quietly showing nothing. And the wire's vocabulary is deliberately the
 * kit's: three of the four values are `skills.file`'s, spelled the same
 * because they are the same sentences about the same facts, and FileReadout
 * is the one place those sentences are written.
 */
const scriptOutcome = (reading: ScriptReading): FileReadoutOutcome => {
  switch (reading.refusal) {
    case '':
      return { kind: 'text', text: reading.text }
    case 'not-text':
      return { kind: 'not-text' }
    case 'too-large':
      return { kind: 'too-large', maxBytes: reading.maxBytes }
    case 'unreadable':
      // The backend's own sentence, verbatim. A fallback of ours here would
      // be a second author of "why this file is not on screen", and the two
      // would disagree the first day a new refusal arrived.
      return { kind: 'unreadable', message: reading.reason }
  }
}

/**
 * What a live shell said a program word IS (nocx-4h0m7.5). nocx does not read
 * anybody's rc files, so `ls` on the command line and `ls` on the PATH are
 * not the same claim — an alias or a function can make them differ, and this
 * is the one place a person is told which they are looking at.
 */
const PROGRAM_KIND_WORDS: Record<NonNullable<ExpansionFacts['programs']>[number]['kind'], string> =
  {
    alias: 'an alias in this shell, not the program of that name',
    function: 'a shell function, not the program of that name',
    builtin: "the shell's own builtin",
    file: 'the program of that name on the PATH',
    'not-found': 'not a command this shell knows',
  }

/**
 * One expansion, in the words a person reads. The three states are three
 * different facts and the surface keeps them apart deliberately: a value we
 * READ, a value we REFUSE to read because reading it would have an effect,
 * and a value we COULD NOT read at all. Merging the last two would tell
 * somebody their shell had been consulted when it had not.
 */
const expansionRowValue = (part: ExpansionPart): string => {
  switch (part.state) {
    case 'expanded':
      // A glob can match an enormous number of paths, so the count is the
      // fact and the paths are a sample beside it — never 143 paths inline.
      return part.count ? `${part.count} paths — ${part.value}` : (part.value ?? '')
    case 'unsafe':
      return 'left exactly as written'
    case 'unasked':
      return 'not read'
  }
}

const expansionRowNote = (part: ExpansionPart, facts: ExpansionFacts): string | undefined => {
  switch (part.state) {
    case 'expanded':
      return 'as the shell reads it now'
    case 'unsafe':
      return part.reason
    case 'unasked':
      return facts.reason || 'no shell could be asked for this value'
  }
}

/** What the classifier's verdict says on the window: the state, the sentence
 *  beneath it, and the tone it is painted in. */
type ClassifierNotice = { tone: StatusCardTone; title: string; description?: string }

/**
 * The classifier's verdict in a person's words (nocx-u43bb).
 *
 * A pure function rather than four ternaries in the JSX, because the shapes
 * are four and the wire allows all four: cleared, suspect, a gate that never
 * ran, and — the schema requires only `consulted` and `reason` — a
 * consultation that came back with no verdict at all.
 *
 * THE TONE IS NEVER `danger`. Same reason as the scan finding one block
 * above: the tone a person reads is part of what the window claims, and a
 * second model's suspicion is evidence beside the bytes, not a refusal — the
 * write is still theirs to allow or refuse. And `clear` is deliberately NOT
 * `ok`: `ok` is the tone of a thing that is fine, and a model saying it
 * raises no suspicion is not a guarantee that the body is safe. It is one
 * cheap reader's opinion, which is worth stating and not worth dressing as a
 * clean bill of health — so it reads as `neutral`, a fact among the facts.
 *
 * A gate that DID NOT RUN says so, out loud. An absent gate rendered as
 * silence is indistinguishable from a clean one, which is the silent degrade
 * AGENTS.md forbids; `reason` carries why (role resolution refused, or the
 * input gate withheld the call) and is rendered with it.
 *
 * The sentence is ASSEMBLED from the parts that exist, so an absent `model`
 * removes its own clause instead of leaving "said by ." on the window. The
 * reason is copied verbatim — it is already bounded and masked by the kernel
 * and re-wording a fact would be this surface inventing one.
 */
const classifierNotice = (
  classifier: NonNullable<AgentApprovalRequested['classifier']>,
): ClassifierNotice => {
  // The model clause is BRANCH-AWARE, because "verdict from X" is itself a
  // claim that a verdict exists. Two of these four shapes have none — the
  // gate that did not run, and the consultation that came back empty — so on
  // those the same field can only say which model was asked. Naming a verdict
  // where there is none is the defect this bead is about, one field over
  // again. Today's kernel sends no model on either of those paths
  // (kernel.go:2016-2027 fills Reason alone), so this is unreachable from our
  // own backend; the schema permits it, and a surface may not state a
  // falsehood merely because the current producer never provokes it.
  const say = (modelClause: (model: string) => string): string | undefined => {
    const parts: string[] = []
    if (classifier.reason) parts.push(endSentence(classifier.reason))
    if (classifier.model) parts.push(modelClause(classifier.model))
    return parts.length > 0 ? parts.join(' ') : undefined
  }
  const verdictFrom = (model: string): string => `Verdict from ${model}.`
  const askedOf = (model: string): string => `Asked of ${model}.`

  if (!classifier.consulted) {
    return {
      tone: 'warning',
      title: 'The classifier gate did not run.',
      description: say(askedOf),
    }
  }
  switch (classifier.verdict) {
    case 'clear':
      return {
        tone: 'neutral',
        title: 'A second model read this proposal and raised no suspicion.',
        description: say(verdictFrom),
      }
    case 'suspect':
      return {
        tone: 'warning',
        title: 'A second model read this proposal and judged it suspect.',
        description: say(verdictFrom),
      }
    default:
      // Consulted, and no verdict came back. The wire permits it and
      // inventing either verdict for it would be worse than saying so.
      return {
        tone: 'warning',
        title: 'A second model was consulted and returned no verdict.',
        description: say(askedOf),
      }
  }
}

/** A fact that has to sit next to another one, given its full stop — the
 *  kernel's reason is a bounded sentence and may arrive without one. */
const endSentence = (text: string): string => (/[.!?…]$/.test(text) ? text : `${text}.`)

/** One argument the window states as machine output rather than as a row:
 *  the model's own key, and the value it rendered to. */
type ArgumentBlock = { name: string; value: string }

/**
 * Whether a rendered argument value is machine text rather than a fact.
 *
 * A newline is the whole of the test, and it is asked of the VALUE because
 * that is where the property lives. The reason a skill body cannot be a row
 * is not that it is called `body`; it is that it is several lines of machine
 * text, and a fact list states a value as one line beside its name, so the
 * instructions a person is being asked to adopt arrive looking like a caption
 * on `name` and `description`. FactList's own header names the carve-out this
 * implements: a value that needs a code block is not a fact in a list, it is
 * a CodeBlock beside one.
 *
 * A list of known keys was the obvious alternative and it is wrong twice
 * over. It says nothing about the next tool's multi-line argument — the
 * second one would read as a caption until somebody noticed and added its
 * name here — and it would put this surface back in the business of knowing
 * what each tool's arguments mean, which is exactly what the exhaustive loop
 * below was built to stop doing.
 */
const isMachineText = (value: string): boolean => value.includes('\n')

export interface AgentApprovalPromptProps {
  open: boolean
  /** The question as the backend sent it — the full binding plus the ask. */
  ask: AgentApprovalRequested
  /** The decision is in flight; the buttons are disabled. */
  busy: boolean
  /**
   * The person's answer: a direction and a width, in one act. One callback
   * rather than six, because the caller's job is to put both on the wire and
   * a surface that split them would invite a call site that forgot one.
   */
  onDecide: (approved: boolean, scope: ApprovalScope) => void
  /** Where a session IS, to a person: the pane's own display title, the
   *  machine its active domain is talking to (`user@host`, or '' for a local
   *  shell — the words for "here" are this surface's, not the pane layer's),
   *  and the directory it is in ('' when it has none to report).
   *  `cwdVerified` says whether an OSC 7 report confirmed that directory or
   *  it is only the one the session was opened with (AD-5); the two travel
   *  together because this surface must word them differently.
   *  Null when no pane in this window holds it, and then the prompt names
   *  the fact rather than printing the id back. Absent in a bare-bones
   *  embedding, which is the same case. */
  sessionWhere?: (
    sessionId: string,
  ) => { tab: string; machine: string; cwd: string; cwdVerified: boolean } | null
  /** Clipboard operation injected by the application composition root. */
  copy?: (text: string) => Promise<void>
}

const TITLE: Record<AgentApprovalRequested['reason'], string> = {
  policy: 'This action needs your approval',
  egress: 'A tool result contained secret-shaped material',
}

/**
 * The three widths, narrowest first, in the order both groups read.
 *
 * `once` leads because it is the answer a hurried person should land on: it
 * decides this proposal and commits to nothing. It is also what the prompt
 * focuses on open, since Prompt puts the caret on the first enabled button.
 */
const SCOPES: ReadonlyArray<{ scope: ApprovalScope; label: string }> = [
  { scope: 'once', label: 'once' },
  // "in this session", never "in this pane": the permission binds to the
  // terminal session, so restarting the shell is a new session and the
  // question comes back. Naming the pane would promise a lifetime it has not.
  { scope: 'session', label: 'in this session' },
  { scope: 'always', label: 'always' },
]

/**
 * The narrow answer covers this proposal. A command's standing answer carries
 * the backend's exact canonical invocation; a non-command answer covers the
 * effect row already sent on the wire. The surface never derives an effect
 * from arguments or the tool name.
 */
const approvalScopeCoverage = (
  scope: ApprovalScope,
  standing: AgentApprovalRequested['standing'],
  effectLabel: string,
) => {
  const covered = standing.rule || effectLabel
  switch (scope) {
    case 'once':
      return 'this proposal only'
    case 'session':
      return `${covered} — until this terminal session ends`
    case 'always':
      return `${covered} — in every session, from now on`
  }
}

export function AgentApprovalPrompt(props: AgentApprovalPromptProps) {
  const ask = () => props.ask
  const effectLabel = () => EFFECT_LABEL[ask().effect]

  /** What a pane says about a session, through the ONE derivation the tab
   *  strip and the tool-call line read. Null when nothing on screen holds
   *  it — never the id, which is what this exists to keep off the surface. */
  const paneOf = (sessionId: string) => props.sessionWhere?.(sessionId) ?? null

  /** Where this call lands, when the resource is a session and a pane can
   *  say. Null otherwise — for a path, whose id is already the person's own
   *  word, and for a session nothing on screen holds. */
  const where = () => {
    const res = ask().resource
    if (!res || res.kind !== 'session') return null
    return paneOf(res.id)
  }

  /** The product's words for the machine a landed call touches. A local
   *  shell has no host, and '' is that fact — "this machine" is what a
   *  person calls it, and saying nothing there would leave the one question
   *  the sentence exists to answer unanswered. */
  const machineWords = (machine: string) => machine || 'this machine'

  /**
   * How the command is introduced, which is a matter of TENSE and not of
   * taste. A policy question is asked BEFORE the call: the command has not
   * run, and "wants to run" is the whole point of the prompt. An egress
   * question is asked AFTER it: the gate screens a tool RESULT, so the
   * command is already behind us and the thing being decided is whether what
   * it printed may leave for the provider. Saying "wants to run" there would
   * misreport what has already happened to the machine.
   */
  const runLead = () => {
    if (ask().reason !== 'egress') return 'The assistant wants to run this command'
    // "ran", unconditionally: the where used to hang off this sentence and
    // gave "ran" nowhere to land without a pane to name; the machine and the
    // tab are rows now, so the sentence stands alone either way.
    return 'The command that produced it ran'
  }

  /**
   * The model's proposal as an object, or null when it is not one — it did
   * not parse, or it parsed to an array or a scalar. Null means the
   * ARGUMENTS cannot be described as rows, and that is where the verbatim
   * blob survives: quoting what we cannot restate is honest, and inventing
   * rows for it would not be. The pane's rows and the effect do not depend
   * on this — they render beside the blob either way.
   */
  const parsedArguments = (): Record<string, unknown> | null => {
    let parsed: unknown
    try {
      parsed = JSON.parse(ask().arguments)
    } catch {
      return null
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
    return parsed as Record<string, unknown>
  }

  /**
   * The command a `run` proposal proposes — njn8s's sentence, kept.
   *
   * The key-count gate is gone (nocx-n7xha): it existed so a third argument
   * could not vanish from the question, and the rows below now guarantee
   * that directly for every argument the sentence does not itself state. An
   * empty or non-string command is still no sentence — there is nothing to
   * put in the block — and then the command is simply one of the rows.
   */
  const proposedCommand = () => {
    if (ask().tool !== COMMAND_TOOL) return null
    const command = parsedArguments()?.command
    return typeof command === 'string' && command !== '' ? command : null
  }

  /**
   * What the command's variables read as — present only for a command
   * proposal the backend derived it for. It sits BESIDE the verbatim
   * command and never replaces it: the string in `arguments` is what runs
   * (nocx-y47mi SETTLED 1), and this window is the one place a person can
   * see what `$HOME` currently is before staking an `rm -rf` on it.
   */
  const expansion = (): ExpansionFacts | null => ask().expansion ?? null

  /**
   * The expanded form, shown only when it says something the verbatim line
   * does not. A command whose variables were all refused or unreadable
   * expands to itself, and a second identical code block would read as a
   * claim that something was resolved when nothing was.
   */
  const expandedCommand = (): string | null => {
    const facts = expansion()
    if (!facts || facts.command === '' || facts.command === proposedCommand()) return null
    return facts.command
  }

  /**
   * Every file the command names, read now (nocx-872jc.3). Empty is the
   * ordinary case and draws NOTHING — not a heading, not an empty viewer:
   * an affordance beside a command that names no file would read as "we
   * looked and there was nothing to show", which is a claim, and a command
   * that names none is not a command anything was looked for in.
   */
  const scripts = (): readonly ScriptReading[] => ask().scripts ?? []

  /**
   * The arguments the window has ALREADY stated, which are therefore not
   * repeated as rows. Derived from what is actually rendered rather than
   * from the tool name: `run`'s block states `command`, and the machine and
   * tab rows state the session whenever a pane can be named — with no pane
   * to name, the session argument still owes the person a row.
   */
  const statedInTheWindow = (): ReadonlySet<string> => {
    const keys = new Set<string>()
    if (proposedCommand() !== null) keys.add('command')
    if (where() !== null) keys.add('sessionId')
    return keys
  }

  /** One argument's value, in the product's words where the product has
   *  them. A `sessionId` that reaches the rows is one the machine and tab
   *  rows did not state — almost always a pane no one can name, but the row
   *  says what it finds either way, derived from the value and never
   *  assumed from where()'s resource. Everything else is the model's own
   *  value, a string as itself and anything else as the JSON it arrived as,
   *  which is exact and never a paraphrase. */
  const argumentValue = (key: string, value: unknown): string => {
    if (key === 'sessionId' && typeof value === 'string') {
      const pane = paneOf(value)
      return pane
        ? `${pane.tab} on ${machineWords(pane.machine)}`
        : 'a session no tab in this window holds'
    }
    return typeof value === 'string' ? value : (JSON.stringify(value) ?? String(value))
  }

  /**
   * The parsed arguments the window does not already state, split into the
   * two ways it can state one: a row of the fact list, or a block beside it
   * (nocx-m40iw). Both halves keep the model's own order and the model's own
   * key.
   *
   * ONE walk and ONE partition. Two derivations — a rows function and a
   * blocks function, each re-walking `Object.entries` and each re-applying
   * the predicate — would be two owners of a single rule, and the first day
   * they disagreed an argument would be stated twice or vanish from the
   * question altogether. Exhaustiveness is what the arguments are owed, and
   * a partition computed once is what keeps it true by construction: every
   * argument lands in exactly one half.
   */
  const partitionedArguments = (): { rows: Fact[]; blocks: ArgumentBlock[] } => {
    const rows: Fact[] = []
    const blocks: ArgumentBlock[] = []
    const args = parsedArguments()
    if (args === null) return { rows, blocks }
    const stated = statedInTheWindow()
    for (const [key, value] of Object.entries(args)) {
      if (stated.has(key)) continue
      const rendered = argumentValue(key, value)
      if (isMachineText(rendered)) blocks.push({ name: key, value: rendered })
      else rows.push({ name: key, value: rendered })
    }
    return { rows, blocks }
  }

  /**
   * The facts of this call, each its own row (nocx-0mvpy.2): where it lands
   * — the pane's machine, tab and directory — then every parsed argument
   * the window does not already state, in the model's own order and under
   * the model's own names, then what the call can do.
   *
   * Exhaustive by construction — the partition above selects nothing, it only
   * decides WHERE each argument is stated, so nothing can be dropped. An
   * argument this surface has never heard of is a row with the key the model
   * used, which is the honest name for it, unless its value is machine text,
   * in which case it is a block under this list under the same key.
   */
  const facts = (): Fact[] => {
    const rows: Fact[] = []
    const pane = where()
    if (pane !== null) {
      rows.push({
        // One mono key per fact, like every row in this column (nocx-n7xha):
        // a spaced English phrase beside the model's own argument keys would
        // read as a different kind of thing. `machine` is the pane's word
        // for where the call lands (`user@host`, or the product's "this
        // machine" for a local shell), `tab` the pane's display title, and
        // `effect` the wire's own field name.
        name: 'machine',
        value: machineWords(pane.machine),
      })
      rows.push({
        name: 'tab',
        value: pane.tab,
      })
      if (pane.cwd !== '') {
        rows.push({
          // `cwd`, not "working directory": this column is ONE vocabulary or
          // it is none. Every other row is an argument under the model's own
          // key — `sessionId`, `path` — in mono, and a spaced English phrase
          // beside them reads as a different kind of thing and pulls the eye
          // off the values, which are what a person is deciding on. `cwd` is
          // also the word nocx already uses for this fact everywhere else:
          // the block header's chip, the ledger's own column, SubmitEntry.
          // (nocx-n7xha follow-up.)
          name: 'cwd',
          value: pane.cwd,
          // Two wordings, because nocx knows the difference and this window
          // is where pretending otherwise costs most (AD-5). Both say "as of
          // now": the pane's directory is what it is when the question is
          // asked, and nothing here binds it to the moment of the answer.
          note: pane.cwdVerified
            ? "the tab's directory as of now, reported by the shell"
            : "the tab's directory as of now, and the shell has not confirmed it",
        })
      }
    }
    // Only the ARGUMENTS are partitioned, and only they should be. The pane's
    // rows above and the expansion's rows below are single-line by
    // construction — a machine name, a tab title, a directory, one word of a
    // command and what it resolves to — so asking them whether they are
    // machine text would be answering a question none of them can raise.
    rows.push(...partitionedArguments().rows)
    // What the command's variables read as (nocx-4h0m7.5), as rows of the
    // ONE fact list — the assignments the command makes to itself first,
    // because they change what every expansion below them means, then each
    // expansion, then what each program word actually is.
    const exp = expansion()
    if (exp !== null) {
      for (const assignment of exp.assignments ?? []) {
        rows.push({
          name: assignment.name,
          value: assignment.value,
          note: 'the command sets this itself for this command, so the shell’s value is not what it reads',
        })
      }
      for (const part of exp.parts ?? []) {
        rows.push({
          name: part.text,
          value: expansionRowValue(part),
          note: expansionRowNote(part, exp),
        })
      }
      for (const program of exp.programs ?? []) {
        rows.push({
          name: program.word,
          value: PROGRAM_KIND_WORDS[program.kind],
          note: program.target || undefined,
        })
      }
    }
    if (ask().reason === 'policy' && ask().tool === NETWORK_TOOL) {
      rows.push({
        name: 'network',
        value: 'reaches the network from this machine',
      })
    }
    if (ask().reason === 'policy' && !ask().standing.available && ask().standing.reason !== '') {
      rows.push({
        name: 'standing',
        value: ask().standing.reason,
      })
    }
    if (ask().reason === 'policy') {
      rows.push({
        // The effect is a row only for a policy question: an egress ask is
        // about what a result may do, not about the call that already
        // happened. The value is EFFECT_LABEL's — the ONE owner of the words.
        name: 'effect',
        value: effectLabel(),
      })
    }
    return rows
  }

  const egressIntro = () => {
    if (ask().reason !== 'egress') return ''
    return ask().wasError
      ? 'The tool failed, and its error mentioned secret-shaped material. Nothing was sent to the model provider.'
      : 'A tool result contained secret-shaped material. Nothing was sent to the model provider.'
  }

  /**
   * A standing answer is offered only when the backend supplied a binding it
   * can save: a canonical invocation for a command or an effect row for a
   * non-command proposal. The button repeats that binding's coverage without
   * reconstructing one from arguments.
   */
  const offeredScopes = () =>
    SCOPES.filter(({ scope }) => scope === 'once' || ask().standing.available)

  const answerLabel = (verb: string, scope: ApprovalScope) =>
    `${verb} ${SCOPES.find((candidate) => candidate.scope === scope)?.label ?? scope}`

  const answerAriaLabel = (verb: string, scope: ApprovalScope) =>
    `${answerLabel(verb, scope)} — ${approvalScopeCoverage(scope, ask().standing, effectLabel())}`

  const group = (approved: boolean, verb: string, variant: 'primary' | 'danger') => (
    <ActionGroup ariaLabel={approved ? 'Allow this action' : 'Refuse this action'}>
      <For each={offeredScopes()}>
        {({ scope }) => (
          <Button
            variant={scope === 'once' ? variant : 'default'}
            secondary={`— ${approvalScopeCoverage(scope, ask().standing, effectLabel())}`}
            disabled={props.busy}
            ariaLabel={answerAriaLabel(verb, scope)}
            onClick={() => props.onDecide(approved, scope)}
          >
            {answerLabel(verb, scope)}
          </Button>
        )}
      </For>
    </ActionGroup>
  )

  return (
    <Prompt
      open={props.open}
      // Escape and a click on the scrim are the NARROWEST refusal. Dismissing
      // a question is not answering it for every call to come.
      onClose={() => props.onDecide(false, 'once')}
      ariaLabel={TITLE[ask().reason]}
      placement="top-sheet"
      title={TITLE[ask().reason]}
      density="compact"
      actionsLayout={ask().reason === 'egress' ? 'row' : 'stacked'}
      actions={
        <Show
          when={ask().reason === 'policy'}
          fallback={
            <>
              <Button
                variant="primary"
                secondary={`— ${approvalScopeCoverage('once', ask().standing, effectLabel())}`}
                disabled={props.busy}
                ariaLabel={answerAriaLabel('Allow', 'once')}
                onClick={() => props.onDecide(true, 'once')}
              >
                Allow once
              </Button>
              <Button
                variant="danger"
                secondary={`— ${approvalScopeCoverage('once', ask().standing, effectLabel())}`}
                disabled={props.busy}
                ariaLabel={answerAriaLabel('Deny', 'once')}
                onClick={() => props.onDecide(false, 'once')}
              >
                Deny once
              </Button>
            </>
          }
        >
          <>
            {group(true, 'Allow', 'primary')}
            {group(false, 'Deny', 'danger')}
          </>
        </Show>
      }
    >
      <Stack>
        <Show when={ask().reason === 'egress'}>
          <p>{egressIntro()}</p>
        </Show>
        <Show
          when={parsedArguments()}
          fallback={
            <>
              <p>
                The assistant is asking to call <strong>{ask().tool}</strong> with these arguments:
              </p>
              <CodeBlock copy={props.copy} ariaLabel={`Arguments of ${ask().tool}`}>
                {ask().arguments}
              </CodeBlock>
            </>
          }
        >
          <Show
            when={proposedCommand()}
            fallback={
              <p>
                The assistant is asking to call <strong>{ask().tool}</strong>.
              </p>
            }
          >
            {(command) => (
              <>
                <p>{runLead()}:</p>
                <CodeBlock copy={props.copy} ariaLabel="The command this question is about">
                  {command()}
                </CodeBlock>
                {/*
                  The expanded form sits BESIDE the verbatim one and is
                  labelled as a reading, never as the thing that runs
                  (nocx-4h0m7.5). Substituting it into the submission was
                  considered and refused: a rewritten command can behave
                  differently through our own fault — re-quoting a value
                  that contains quotes or newlines, or a command that meant
                  to be re-evaluated later — so what is sent is byte-for-byte
                  the block above.
                */}
                <Show when={expandedCommand()}>
                  {(expanded) => (
                    <>
                      <p>
                        With its variables as the shell reads them now. This is what the command
                        means, not what is sent — the line above is what runs, exactly as written:
                      </p>
                      <CodeBlock
                        copy={props.copy}
                        ariaLabel="The same command with its variables read as the shell reads them now"
                      >
                        {expanded()}
                      </CodeBlock>
                    </>
                  )}
                </Show>
                <Show when={expansion()?.asked === false && (expansion()?.reason ?? '') !== ''}>
                  <p>{expansion()?.reason}</p>
                </Show>
              </>
            )}
          </Show>
        </Show>
        <FactList facts={facts()} ariaLabel={`What ${ask().tool} would do`} />
        {/*
          The arguments the fact list could not state, in the model's order,
          BESIDE the rows rather than inside them (nocx-m40iw). A skill body
          is the clearest case: it is the thing the person is actually being
          asked to adopt, and a fact value renders it as a caption on `name`
          and `description`. `label` is the argument's own key, the same
          vocabulary the rows use, and `copy` is passed because a body is
          precisely the thing somebody may want on their clipboard before
          deciding — unlike the finding's quoted line below it.
        */}
        <For each={partitionedArguments().blocks}>
          {(block) => (
            <CodeBlock
              copy={props.copy}
              label={block.name}
              ariaLabel={`The ${block.name} argument of ${ask().tool}`}
            >
              {block.value}
            </CodeBlock>
          )}
        </For>
        {/*
          The file the command NAMES, after the facts, because a person reads
          it once they have decided what the command IS (nocx-872jc.3). The
          lead is said ONCE above the whole set rather than on each file: it
          is one sentence about what these bytes are, and repeating it per
          file would turn a disclaimer into a lecture.

          It is the same viewer the Skills page reads a SKILL.md through, and
          that is the point of the epic it belongs to — one capability in
          three places, not three viewers. Nothing is scanned here, so no
          `marks`: running the skill scanner over an arbitrary shell script
          would be a new advisory surface on every command approval, and an
          empty findings affordance would read as an all-clear nobody gave.
        */}
        <Show when={scripts().length > 0}>
          <p>
            And what those files hold right now. This is a reading, not what is sent — the command
            above is what runs, exactly as written, and a file can change between this question and
            the moment it runs.
          </p>
          <For each={scripts()}>
            {(reading) => (
              <FileReadout
                // The path is the one the COMMAND wrote, which is the one
                // on the line above; `verb` says what the command does with
                // it, which is not the same act for `bash` and for `source`.
                // Mono keys, like every other row in this window.
                facts={[
                  { name: 'path', value: reading.path },
                  { name: 'verb', value: SCRIPT_VERB_WORDS[reading.verb] },
                ]}
                ariaLabel={`What ${reading.path} holds right now`}
                outcome={scriptOutcome(reading)}
              />
            )}
          </For>
        </Show>
        {/*
          The scan's finding sits AFTER the facts, which is where the body it
          is about has just been stated, and it is a StatusCard rather than a
          paragraph because it is a condition and not body copy — the same
          reason the Vault page stopped writing its state as prose. `warning`,
          never `danger`: the tone a person reads is part of what the window
          claims, and the spec is explicit that a finding does not make the
          result unreadable. The line goes in a CodeBlock because it is
          verbatim evidence — bytes from the proposed body, quoted, not
          restated — and no `copy`, because there is nothing here a person
          wants on their clipboard.
        */}
        <Show when={ask().finding}>
          {(finding) => (
            <Stack>
              <StatusCard
                tone="warning"
                title={scanPatternWords(finding().patternId)}
                description={`Line ${finding().lineNumber} of ${finding().path} matched the static scan. It is evidence to read beside the bytes, not a refusal — the write is still yours to allow or refuse.`}
              />
              <CodeBlock
                ariaLabel={`Line ${finding().lineNumber} of ${finding().path}, which the static scan matched`}
              >
                {finding().line}
              </CodeBlock>
            </Stack>
          )}
        </Show>
        {/*
          The classifier's verdict is the finding's sibling and reads like it:
          a condition stated as a card, after the body it judged. What it does
          NOT have is a CodeBlock, and the asymmetry is deliberate — the
          finding's `line` is verbatim bytes quoted out of the proposed body,
          which is why it earns a block, while the classifier's `reason` is a
          masked, bounded sentence a person reads. Prose belongs in the card's
          description; putting it in a block would dress a sentence as machine
          output. Absent entirely, this renders nothing at all: an ordinary
          policy approval carries no classifier and must not grow an empty
          slot for one.
        */}
        <Show when={ask().classifier}>
          {(classifier) => {
            const notice = () => classifierNotice(classifier())
            return (
              <StatusCard
                tone={notice().tone}
                title={notice().title}
                description={notice().description}
              />
            )
          }}
        </Show>
        <Show when={(ask().findings?.length ?? 0) > 0}>
          <Stack gap="loose">
            <p>What was found, and where — never the material itself:</p>
            <For each={ask().findings}>
              {(f) => (
                <p>
                  <Badge tone={f.source === 'known' ? 'warning' : 'info'}>
                    {f.source === 'known' ? 'Known vault material' : 'Heuristic match'}
                  </Badge>{' '}
                  {f.source === 'known' ? f.secretName : f.kind}
                  {' — bytes '}
                  {f.start}–{f.end}
                </p>
              )}
            </For>
          </Stack>
        </Show>
        <p>
          Approving covers this call: it has not run, and no call after it in this response will. It
          does not promise the terminal is untouched — a permitted call earlier in this batch may
          already have run.
        </p>
        <Show when={ask().reason === 'policy' && ask().standing.available}>
          <p>
            An answer in this session lasts until this terminal session ends; restarting the shell
            starts a new one and the question comes back. An answer of always is a standing answer
            for {ask().standing.rule || effectLabel()}, in every session, from now on, which you can
            change on the Agent policy page.
          </p>
        </Show>
      </Stack>
    </Prompt>
  )
}
