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
 * AND A FOURTH ANSWER, WHICH IS NOT A FOURTH WIDTH (nocx-4yjwk.1, design
 * §5.3). The three widths all answer "how long"; none of them moves the bound
 * that excluded a resource. So a call refused because a path fell outside a
 * row's scopes could be allowed `once` and would ask again on the next
 * identical call, for ever, because nothing widened what excluded it. When
 * the wire carries `outOfScope`, the question is that one, and `expand` is
 * the answer that settles it: it widens the effect row's scopes to cover the
 * resource that fell outside AND approves this call, as ONE act.
 *
 * The OFFER is read off the wire, never re-derived from `outOfScope.cause`.
 * The backend applies a widening, so the backend is what says whether it can
 * be given — the same rule that keeps the effect off this surface. Where the
 * bound is an immutable fence the backend sends no offer and a reason
 * instead, and the reason is on screen: a window that hit a bound and said
 * nothing about it would leave a person answering a question whose yes the
 * layer below refuses.
 *
 * What the surface must not overstate (design §7.2): approving covers the
 * call that is asking — it has NOT run, and no call after it in that response
 * will. It does NOT promise the domain is untouched: a permitted sibling
 * earlier in the same batch has already run. The sentence is on the surface,
 * where a person deciding reads it.
 */
import { For, Show } from 'solid-js'
import { ActionGroup, Badge, Button, CodeBlock, FactList, Prompt, Stack, type Fact } from './ui'
import { EFFECT_LABEL } from './effect-labels'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import type { AgentApprove } from './generated/agent.approve'

/** How far an answer reaches — the wire's own vocabulary, not a second one. */
type ApprovalScope = AgentApprove['scope']

/** What the command's variables read as, as the backend derived it. */
type ExpansionFacts = NonNullable<NonNullable<AgentApprovalRequested['expansion']>>
type ExpansionPart = NonNullable<ExpansionFacts['parts']>[number]

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
    case 'expand':
      // The widening does not save an invocation rule, so it does NOT read
      // `standing.rule`: what it edits is the effect ROW's scopes, and the
      // row is named by its effect. The resource it grows to cover is named
      // by the label beside this sentence, so "it" here is that resource and
      // never anything the surface derived for itself.
      return `${effectLabel} may then reach it, in every session, from now on`
  }
}

/**
 * The verb an answer is given with. Two spellings of "Allow" would be two
 * vocabularies for one act — the buttons say it, the aria labels repeat it,
 * and the receipt afterwards has to say the same word or the person cannot
 * tell that it is reporting what they clicked.
 */
const approvalAnswerVerb = (approved: boolean) => (approved ? 'Allow' : 'Deny')

/**
 * One label builder for every answer, because two would be two spellings of
 * one concept. `expand` is not a fourth WIDTH — the three widths answer "how
 * long", and this one answers "over what" — so its words are built from the
 * resource that fell outside rather than looked up in SCOPES.
 */
const approvalAnswerLabel = (verb: string, scope: ApprovalScope, wideningResource = '') =>
  scope === 'expand'
    ? `${verb} and widen to ${wideningResource}`
    : `${verb} ${SCOPES.find((candidate) => candidate.scope === scope)?.label ?? scope}`

/**
 * The WHOLE of one answer in one sentence: the direction and how far it
 * reaches. It is what the buttons' aria labels read, and — with "Saved:" in
 * front of it — what the receipt afterwards says, so the sentence a person is
 * told they configured is character for character the sentence they chose.
 */
const approvalAnswerSentence = (
  verb: string,
  scope: ApprovalScope,
  standing: AgentApprovalRequested['standing'],
  effectLabel: string,
  wideningResource = '',
) =>
  `${approvalAnswerLabel(verb, scope, wideningResource)} — ${approvalScopeCoverage(scope, standing, effectLabel)}`

/**
 * What a SAVED standing answer says it did (nocx-2019q). No second
 * sentence-builder: the whole line is the answer's own sentence with one word
 * in front of it, so it cannot drift from the button that produced it — and
 * when the button's words change, this changes with them.
 *
 * `Saved`, not `Allowed`: what the line reports is not that the call went
 * through — the person watched that — but that something was written down
 * which will govern calls nobody is looking at yet.
 */
export const standingAnswerReceipt = (
  approved: boolean,
  scope: ApprovalScope,
  /** The canonical invocation the answer covers, exactly as the question
   *  offered it; empty for a non-command answer, whose coverage is the row. */
  rule: string,
  effectLabel: string,
) =>
  `Saved: ${approvalAnswerSentence(
    approvalAnswerVerb(approved),
    scope,
    // The coverage builder reads a standing OFFER, because that is the shape
    // the question arrives in and the shape the buttons hold. A receipt has
    // only the two facts inside it, so it says so here rather than making
    // every caller assemble an offer for an answer already given.
    { available: true, rule, reason: '' },
    effectLabel,
  )}`

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
    if (ask().tool !== 'run') return null
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
   * The facts of this call, each its own row (nocx-0mvpy.2): where it lands
   * — the pane's machine, tab and directory — then every parsed argument
   * the window does not already state, in the model's own order and under
   * the model's own names, then what the call can do.
   *
   * Exhaustive by construction — the loop selects nothing, so nothing can be
   * dropped. An argument this surface has never heard of is a row with the
   * key the model used, which is the honest name for it.
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
    const args = parsedArguments()
    if (args !== null) {
      const stated = statedInTheWindow()
      for (const [key, value] of Object.entries(args)) {
        if (stated.has(key)) continue
        rows.push({ name: key, value: argumentValue(key, value) })
      }
    }
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
    if (ask().reason === 'policy' && ask().tool === 'fetch.url') {
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

  /**
   * The fact that a resource fell outside a bound, or null when nothing did
   * — the ordinary question, which this whole section leaves untouched.
   */
  const outOfScope = () => ask().outOfScope ?? null

  /**
   * Whether the widening answer may be OFFERED, read off the wire and never
   * re-derived from `cause`. The backend is what applies a widening, so the
   * backend is what says whether it can be given: a surface that inferred
   * "row-scope means offer" would put a yes on screen that the layer below
   * refuses, which is the failure this whole shape exists to remove (design
   * §5.3, ADR-0028 decision 4 — the effect never comes from this side).
   */
  const wideningOffered = () => outOfScope()?.widening.available === true

  /**
   * Why no widening can be offered, when the backend said so — an immutable
   * fence, most often. A person who can see that a bound was hit is owed the
   * fact that this window cannot move it; the alternative is a question they
   * answer and a call that fails anyway.
   */
  const wideningRefused = () => {
    const fact = outOfScope()
    if (fact === null || fact.widening.available) return ''
    return fact.widening.reason
  }

  /** The resource the row would have to grow to cover, in the backend's own
   *  words. The surface prints the id it was given and derives nothing from
   *  the arguments — the widening is applied from the QUESTION. */
  const wideningResource = () => outOfScope()?.resource.id ?? ''

  /** The kit builders above, bound to this question's own facts. */
  const answerLabel = (verb: string, scope: ApprovalScope) =>
    approvalAnswerLabel(verb, scope, wideningResource())

  const answerAriaLabel = (verb: string, scope: ApprovalScope) =>
    approvalAnswerSentence(verb, scope, ask().standing, effectLabel(), wideningResource())

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
            {group(true, approvalAnswerVerb(true), 'primary')}
            {group(false, approvalAnswerVerb(false), 'danger')}
            {/*
              The widening answer sits LAST, in a group of its own, and that
              placement is the argument. It belongs to neither row: it is
              broader than `always` in the axis it moves — it edits the effect
              row's own scopes, which is administration and not a width — and
              narrower in another, since it grows the row by exactly the one
              resource that fell outside. Putting it in the Allow row would
              read as a fourth width and would sit where a hurried person's
              eye already is; last and named is where a deliberate answer
              belongs. It is never focused on open either way, because Prompt
              puts the caret on the first enabled button and that is still
              `Allow once` (the reason SCOPES leads with it).

              There is no Deny twin: a widening is an approval, and the wire
              refuses `expand` on a decline. Denying is what the Deny row
              already does.
            */}
            <Show when={wideningOffered()}>
              <ActionGroup ariaLabel="Allow this action and widen what it may reach">
                <Button
                  variant="default"
                  secondary={`— ${approvalScopeCoverage('expand', ask().standing, effectLabel())}`}
                  disabled={props.busy}
                  ariaLabel={answerAriaLabel('Allow', 'expand')}
                  onClick={() => props.onDecide(true, 'expand')}
                >
                  {answerLabel('Allow', 'expand')}
                </Button>
              </ActionGroup>
            </Show>
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
        <Show when={wideningRefused() !== ''}>
          {/*
            A bound was hit and no answer here can move it. The sentence sits
            with the answers rather than among the facts because it is about
            what this window can and cannot do — a person who reads the row
            and then finds no widening answer must be told which bounds are
            immovable from here, or they will read the absence as an omission.
          */}
          <p>{wideningRefused()}</p>
        </Show>
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
