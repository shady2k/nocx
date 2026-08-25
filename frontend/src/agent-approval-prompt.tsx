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
 * had never seen. A policy question now offers allow and deny at once, in this
 * session and always — the logic every other assistant has — and the BACKEND
 * applies the width: this surface never edits the policy matrix, which would
 * put a second owner on the document the settings page owns (design §"Three
 * wire changes").
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
 * The coverage sentence belongs to the answer control, not to the explanatory
 * prose below the call. The effect is interpolated from the wire's class so a
 * standing answer cannot silently widen to every effect.
 */
const approvalScopeCoverage = (scope: ApprovalScope, effect: AgentApprovalRequested['effect']) => {
  switch (scope) {
    case 'once':
      return 'this proposal only'
    case 'session':
      return `every ${EFFECT_LABEL[effect]} call in this session`
    case 'always':
      return `every ${EFFECT_LABEL[effect]} call, in every session, from now on`
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
    if (ask().tool !== 'run') return null
    const command = parsedArguments()?.command
    return typeof command === 'string' && command !== '' ? command : null
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
   * One group of three. `variant` is spent on the narrowest answer in each
   * group and nowhere else: the two once-scoped buttons are the immediate
   * reply to the question being asked, and the standing answers are
   * deliberately quieter, because a decision that outlives the question
   * should be a deliberate act rather than the thing the eye lands on.
   */
  const group = (approved: boolean, verb: string, variant: 'primary' | 'danger') => (
    <ActionGroup ariaLabel={approved ? 'Allow this action' : 'Refuse this action'}>
      <For each={SCOPES}>
        {({ scope, label }) => (
          <Button
            variant={scope === 'once' ? variant : 'default'}
            disabled={props.busy}
            secondary={`— ${approvalScopeCoverage(scope, ask().effect)}`}
            onClick={() => props.onDecide(approved, scope)}
          >
            {`${verb} ${label}`}
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
      actionsLayout={ask().reason === 'egress' ? 'row' : 'stacked'}
      actions={
        <Show
          when={ask().reason === 'policy'}
          fallback={
            <>
              <Button
                variant="primary"
                disabled={props.busy}
                secondary={`— ${approvalScopeCoverage('once', ask().effect)}`}
                onClick={() => props.onDecide(true, 'once')}
              >
                Allow once
              </Button>
              <Button
                variant="danger"
                disabled={props.busy}
                secondary={`— ${approvalScopeCoverage('once', ask().effect)}`}
                onClick={() => props.onDecide(false, 'once')}
              >
                Deny once
              </Button>
            </>
          }
        >
          {group(true, 'Allow', 'primary')}
          {group(false, 'Deny', 'danger')}
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
        <Show when={ask().reason === 'policy'}>
          <p>
            An answer in this session lasts until this terminal session ends; restarting the shell
            starts a new one and the question comes back. An answer of always is a standing answer
            for <strong>{effectLabel()}</strong>, which you can change on the Agent policy page.
          </p>
        </Show>
      </Stack>
    </Prompt>
  )
}
