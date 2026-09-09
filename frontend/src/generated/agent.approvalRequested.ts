/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.approvalRequested.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the agent.approvalRequested server-to-client notification (nocx-z9hj4, design §7.2/§7.3): a question reached a person. One kind of question whether the risk was an effect coming in (a policy escalation) or a secret going out (an egress finding). Carries the full binding — run, attempt, tool, call id and the canonical-argument hash — what the person's answer on agent.approve must name, the arguments being decided about, the reason the gate asked, and the egress findings when the gate that asked was the egress gate. Findings are facts — which detector fired, the kind, where — never the secret material itself.
 */
export interface AgentApprovalRequested {
  /**
   * The backend-minted run id the question belongs to — the value agent.approve echoes back.
   */
  runId: string
  /**
   * The run's attempt — part of the binding; the answer echoes it back.
   */
  attempt: number
  /**
   * The tool the model proposed calling — one of the names internal/agenttools' declaration table declares, and nothing else. The set is CLOSED on purpose (nocx-69sew): the renderer keys two sentences on this value (the command block for the command carrier, the network row for fetch.url), and while it was a bare string a rename in the table — `run` became `session.run` — left the renderer comparing against a dead literal, which no test and no schema could see. Enumerated here, the generated renderer type is a union, so a comparison against a name the table no longer declares is a compile error rather than a branch that is quietly never taken; TestApprovalRequestedToolEnumMatchesTheTable is the other end, and fails the moment the table and this list disagree. Only a declared tool can reach this notification: the two names that are not declarations — the unknown-tool anchor and tools.search — are answered inside WrapInvokableToolCall and never reach the kernel that escalates.
   */
  tool:
    | 'files.read'
    | 'fetch.url'
    | 'session.list'
    | 'session.read'
    | 'session.run'
    | 'session.wait'
    | 'files.edit'
    | 'files.create'
    | 'git.status'
    | 'notes.search'
    | 'notes.create'
    | 'notes.update'
    | 'notes.delete'
    | 'snippets.list'
    | 'snippets.create'
    | 'snippets.update'
    | 'snippets.delete'
    | 'snippets.reorder'
    | 'skills.read'
    | 'skills.create'
    | 'skills.update'
    | 'skills.delete'
    | 'skills.resolve'
    | 'skills.install'
    | 'workers.holdings'
    | 'workers.spawn'
    | 'workers.say'
    | 'workers.wait'
    | 'workers.inbox'
    | 'workers.close'
  /**
   * The model's call id for the proposed call — part of the binding.
   */
  callId: string
  /**
   * The canonical-argument hash of the binding — what distinguishes one proposal from a changed one. The answer echoes it back; a changed argument never resumes under the old approval.
   */
  argHash: string
  /**
   * The proposed call's arguments, as the model produced them — what the person is deciding about.
   */
  arguments: string
  /**
   * Which gate asked: the policy gate (an effect coming in) or the egress gate (a secret going out).
   */
  reason: 'policy' | 'egress'
  /**
   * The effect class the policy gate decided on — the row a standing answer writes when the exact invocation rule is saved. Sent by the backend because the renderer must never derive an effect from a tool name (ADR-0028 decision 4).
   */
  effect:
    | 'observe'
    | 'mutate-reversible'
    | 'mutate-destructive'
    | 'privilege-change'
    | 'disclose'
    | 'cross-boundary'
    | 'delegate'
  /**
   * Whether the prompt can offer a standing answer. For a command proposal, rule names the exact canonical invocation the answer would save; for a non-command proposal, rule is empty and the effect field names the policy row the answer covers.
   */
  standing: {
    available: boolean
    rule: string
    reason: string
  }
  /**
   * The resource the gate matched the call against, or null when the call named none. A fact for the person reading the question; a standing answer is over the exact invocation in standing, never over this.
   */
  resource?: {
    /**
     * The resource kind, from the ledger's closed set.
     */
    kind: 'path' | 'session' | 'environment' | 'credential' | 'destination' | 'content'
    /**
     * The resource's id.
     */
    id: string
  } | null
  /**
   * Present only when a resource of the proposed call fell outside a bound (design §5.3). It is what makes the question answerable: the prompt's three widths — this call, this session, always — widen NOTHING that excluded the resource, so without this fact the next identical call asks again, for ever. cause says which bound was missed and therefore whether any answer can move it; resource is the scope the row would have to grow to cover, in the form a row states it in, so the widening is applied from the question rather than re-derived from the arguments; widening is the offer itself, sent by the backend because the backend is what applies the answer.
   */
  outOfScope?: {
    /**
     * Which bound the resource fell outside. 'row-scope' is an operator's own selector on the effect row — editable, so an answer may widen it. 'fence' is the run fence or a narrowed capability, which no answer can move.
     */
    cause: 'row-scope' | 'fence'
    /**
     * The resource that fell outside — what the row would have to grow to cover.
     */
    resource: {
      kind: 'path' | 'session' | 'environment' | 'credential' | 'destination' | 'content'
      id: string
    }
    /**
     * Whether the prompt may offer the widening answer (agent.approve scope 'expand'). Available only for cause 'row-scope': offering a question whose yes cannot be honoured is the failure this shape exists to remove, so a fence carries reason instead and no offer.
     */
    widening: {
      available: boolean
      reason: string
    }
  } | null
  /**
   * skills.create and skills.update only: the first static scan finding in the bytes being proposed, naming the file it was found in — always SKILL.md, because that is the only file those two write. A skills.install proposal leaves this ABSENT and puts its findings in `install.files[].findings` instead, attached to the file each matched in: that question carries every file's bytes, so a finding is marked on the line it sits on, and a row repeating the first of them would be a second surface owning one fact (nocx-ojfuc.2). The path is stated rather than left out so this finding is the same shape skills.audit and skills.file carry; a surface handed a finding without one has to invent a subject for it.
   */
  finding?: {
    path: string
    patternId: string
    line: string
    lineNumber: number
  } | null
  /**
   * Skill write only: the classifier verdict or bounded failure fact.
   */
  classifier?: {
    consulted: boolean
    verdict?: 'clear' | 'suspect'
    model?: string
    reason: string
  } | null
  /**
   * Egress only: the findings are in an ERROR string the tool returned rather than in its result — the surface reads the two differently.
   */
  wasError?: boolean
  /**
   * Command proposals only (nocx-4h0m7.5): what the verbatim command's variables currently read as, carried BESIDE the verbatim string and never instead of it (nocx-y47mi SETTLED 1 — the verbatim string is what runs). Absent for a non-command proposal. Every part carries one of three states, and the last two are different facts the surface must never merge: 'unsafe' means reading it would have an effect ($(cmd), backticks, <(cmd), ${VAR:=x}, ${VAR:?msg}, $((x++))) so it is left exactly as written; 'unasked' means it is a pure read and no shell could be asked — a remote host without our integration, or a shell that did not answer.
   */
  expansion?: {
    /**
     * Whether a live shell was consulted at all. False is the remote host, the un-integrated session and the shell that did not answer; reason says which.
     */
    asked: boolean
    /**
     * Why no shell was consulted, in the words the person reads. Present only when asked is false.
     */
    reason?: string
    /**
     * The expanded DISPLAY form: the verbatim command with every answered span substituted and every other span left exactly as written. It is never submitted, never re-quoted and never handed to a shell — the verbatim command in arguments is what runs.
     */
    command: string
    /**
     * Every expansion site in the verbatim command, in the order it appears.
     */
    parts?: {
      /**
       * The verbatim source text of this expansion site.
       */
      text: string
      /**
       * The parameter's name when it has one — what a refusal sentence names.
       */
      name?: string
      /**
       * Which shell construct this is.
       */
      kind:
        | 'parameter'
        | 'tilde'
        | 'glob'
        | 'brace'
        | 'arithmetic'
        | 'command-substitution'
        | 'process-substitution'
      /**
       * expanded: a live shell answered. unsafe: reading it would have an effect, so it is left as written and reason says why. unasked: a pure read no shell could be asked for.
       */
      state: 'expanded' | 'unsafe' | 'unasked'
      /**
       * What the shell said this reads as. Present only for state 'expanded'.
       */
      value?: string
      /**
       * How many paths a glob matched. A pattern can match an enormous number of them, so the count is the fact and the list is available rather than inline.
       */
      count?: number
      /**
       * Why an unsafe expansion is left exactly as written.
       */
      reason?: string
    }[]
    /**
     * The NAME=value prefixes the command applies to a command of its own — `HOME=/tmp rm -rf $HOME/x`. The PARSE's business and never the shell's: the live shell knows nothing about them, so every expansion of an assigned name is reported unsafe rather than expanded.
     */
    assignments?: {
      name: string
      value: string
    }[]
    /**
     * What a live shell says each program word actually IS. It closes, partly, the hole the command parser names in its own header: nocx does not read rc files, so an alias or a function can make `ls` mean something else.
     */
    programs?: {
      word: string
      kind: 'alias' | 'function' | 'builtin' | 'file' | 'not-found'
      /**
       * What the alias expands to, or the path of the file.
       */
      target?: string
    }[]
  } | null
  /**
   * Command proposals only (nocx-872jc.3): the whole of every file the proposed command NAMES, read at the moment the question was asked. `bash deploy.sh` is eleven characters whose meaning lives somewhere else, so a person shown only the command was approving a NAME rather than an act. Carried BESIDE the verbatim string and never instead of it, exactly as `expansion` is (nocx-4h0m7.5 settled that shape; nocx-y47mi SETTLED 1 — the verbatim command in `arguments` is what runs, byte for byte). It is a READING and not a promise: these are the file's contents NOW, nothing here is bound into the approval, and the file can change between this question and the run — the surface says so in its own words. Which files are named comes from the command parser's own resource report (the entries whose verb is execute or source), never from a second tokenizing of the command line, so the window can never show a file the policy gate did not see. ABSENT — not an empty array — whenever the parse named no such file, so a proposal with no script draws no empty affordance. EVERY named file is here or the field would lie by omission: `bash a.sh && bash b.sh` carries two, because showing the first of two looks complete while being half the act. Nothing is scanned: the bytes go to the person and reading them is theirs to do.
   */
  scripts?: {
    /**
     * The path THE COMMAND WROTE, verbatim — `deploy.sh`, not the absolute path it resolved to. It is what the person is reading on the command line above, and a second name for the same subject in the one place the two must obviously be the same thing would be this window's own ambiguity.
     */
    path: string
    /**
     * How the command names the file, in the parser's own vocabulary. `execute` is `bash x.sh`, `sh ./x.sh`, `./x.sh`; `source` is `source x.sh` and `. x.sh`, which changes the shell itself rather than running a subprocess — a difference a person deciding is owed.
     */
    verb: 'execute' | 'source'
    /**
     * The file verbatim. `""` with an empty refusal is an EMPTY FILE, which is a true thing to show; empty whenever refusal is set, because half a refused file is neither the file nor a refusal.
     */
    text: string
    /**
     * Why the bytes are not shown. Empty means nothing was refused and `text` is the file. The first three are skills.file's own values, spelled the same because they are the same sentences about the same facts and one viewer draws both. `unreadable` is this notification's own: skills.file answers a REQUEST and can fail it, while a question has nowhere to put an error, so "there was no file to read" must arrive as a fact inside the question or not at all. Never null.
     */
    refusal: '' | 'not-text' | 'too-large' | 'unreadable'
    /**
     * The read budget, in bytes, a too-large refusal was measured against. It travels so the viewer's sentence can name the limit rather than keeping a second copy of the number. The head of an over-budget file is deliberately not sent: a person who read the first 64 KiB of a script would believe they had read the script.
     */
    maxBytes: number
    /**
     * Why an unreadable file was not read, in the words the person reads — no session, no provider for that machine, a relative path with no directory to resolve it against, a file that is gone, permission refused, the read budget spent. Empty for every other refusal. It travels rather than being composed on the surface for the reason expansion.reason does: these differ in ways that matter to whoever is deciding, and a renderer writing one of its own would put our guess in front of them instead of what happened.
     */
    reason: string
  }[]
  /**
   * skills.install only (nocx-ojfuc.2, widened by nocx-295uk.3): what the proposed address RESOLVED to. The tool's arguments are an address, and an address is not something anybody can decide about — the model was asked to install a skill from a page, and what it resolved that page to is the thing the person is actually deciding. A question naming the ask rather than the answer would let somebody approve a page they read and receive a repository they never saw. So this carries the resolution: the route it travelled, and every skill the answer covers — the name and description each document gives itself, the digest each write is bound to, and EVERY file that will land, with its bytes. Absent for every other proposal. ONE QUESTION CARRIES A SET rather than one question per skill, because 'which one, or all of them?' is a real answer to a repository holding nine: nine dialogues for it are worse than one listing nine, and the person reads the same content either way. A plain URL install is that same shape with a set of one and no route. THE ROUTE FACTS — source, destination, originsDiffer, ref, commit — are shared by every skill in the set because they are properties of the journey, not of a skill, and originsDiffer is stated rather than left to be re-derived: a surface comparing two hosts itself would be a second answer to a question the server already answered. THE BYTES ARE HERE rather than behind a request, because none of these files is on disk yet and must not be until the person answers: skills.file reads an INSTALLED skill, a notification cannot answer a follow-up, and bytes fetched a second time from a mutable server slot would not be the bytes the question was built from. It is the shape `scripts` already has for the file a command names. The size is bounded upstream and not here: internal/skill refuses a bundle over 32 files, over 512 KiB of support text or with a document over 64 KiB before any question can exist.
   */
  install?: {
    /**
     * The address the person supplied before repository resolution.
     */
    source?: string
    /**
     * The canonical repository address reached by resolution.
     */
    destination?: string
    /**
     * Whether the source and destination URL hosts differ.
     */
    originsDiffer?: boolean
    /**
     * The repository ref pinned by the resolution.
     */
    ref?: string
    /**
     * The immutable repository commit pinned by the resolution.
     */
    commit?: string
    /**
     * Every skill covered by this approval. Direct installs contain one item; resolved installs contain the selected candidates.
     *
     * @minItems 1
     */
    skills: [
      {
        /**
         * The candidate path inside the resolved repository. Absent for a direct URL install.
         */
        path?: string
        /**
         * The skill name from its own frontmatter.
         */
        name: string
        /**
         * The skill description from its own frontmatter.
         */
        description: string
        /**
         * The address fetched for this skill.
         */
        url: string
        /**
         * The sha256 digest over the complete skill bundle.
         */
        digest: string
        /**
         * Every file that will land, with its exact text and findings.
         *
         * @minItems 1
         */
        files: [
          {
            path: string
            text: string
            findings: {
              path: string
              patternId: string
              line: string
              lineNumber: number
            }[]
          },
          ...{
            path: string
            text: string
            findings: {
              path: string
              patternId: string
              line: string
              lineNumber: number
            }[]
          }[],
        ]
      },
      ...{
        /**
         * The candidate path inside the resolved repository. Absent for a direct URL install.
         */
        path?: string
        /**
         * The skill name from its own frontmatter.
         */
        name: string
        /**
         * The skill description from its own frontmatter.
         */
        description: string
        /**
         * The address fetched for this skill.
         */
        url: string
        /**
         * The sha256 digest over the complete skill bundle.
         */
        digest: string
        /**
         * Every file that will land, with its exact text and findings.
         *
         * @minItems 1
         */
        files: [
          {
            path: string
            text: string
            findings: {
              path: string
              patternId: string
              line: string
              lineNumber: number
            }[]
          },
          ...{
            path: string
            text: string
            findings: {
              path: string
              patternId: string
              line: string
              lineNumber: number
            }[]
          }[],
        ]
      }[],
    ]
  } | null
  /**
   * Egress only: what was found and where. Facts, never the material.
   */
  findings?: {
    /**
     * Which detector fired: known vault material or a heuristic match.
     */
    source: 'known' | 'heuristic'
    /**
     * The recognizer's closed kind for a heuristic finding.
     */
    kind?: string
    /**
     * The vault catalogue name of the matched secret for a known finding (ADR-0016). Display metadata, never material.
     */
    secretName?: string
    /**
     * Byte offset of the match into the tool result, inclusive.
     */
    start: number
    /**
     * Byte offset of the match into the tool result, exclusive.
     */
    end: number
  }[]
}
