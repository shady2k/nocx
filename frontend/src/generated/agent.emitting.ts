/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.emitting.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of agent.emitting: the panes nocx is watching, and — when one was named and is still watched — that pane's current frame together with THE RULE'S OWN READING OF IT (nocx-02uci). Both halves are required and the second is the one that is easy to forget: a view that shows only the screen is a screen, and the person already has one of those. What makes the reading trustworthy is that it is a RECORDING of the same evaluation the product acts on rather than a second evaluation written beside it — the branch named here is the branch that decided the pane's state, and its predicates are reported exactly as the walk answered them. Nothing here decides anything: the AD-6 amendment grants an enrolled pane's grid exactly two powers, whether nocx may write into the pane and what its indicator shows, and this answers neither. It is a read-out handed to the pane's own operator, of a screen they own and are already looking at, and it creates no enrolment.
 */
export interface AgentEmitting {
  /**
   * Every pane nocx is currently watching, in pane order. It rides with every answer rather than in a method of its own so that the poll that refreshes the view also refreshes the list: a pane whose observation closed leaves the list on the next answer, which is how the surface learns it has nothing left to show. Empty is the ordinary case — almost no pane in the product is enrolled — and it is not a degraded reading. A pane whose agent has EXITED is still listed: its last screen is exactly what the person working out what happened needs.
   */
  panes: {
    /**
     * The pane's server-authoritative session id (AD-7), which is what agent.emitting takes back to ask for its reading.
     */
    sessionId: string
    /**
     * The agent named by the enrolment act, verbatim. It is read from the act rather than taken from the caller, because a caller that named the agent would be a second owner of which rule a pane is under.
     */
    agent: string
  }[]
  /**
   * The named pane's frame and what its rule sees on it. ABSENT when no pane was named, and absent when the pane named is no longer watched — which is a race the surface polls into rather than an error, because an observation closes when its session ends and the surface finds out one answer later.
   */
  reading?: {
    /**
     * The pane this reading is of, echoed back so a late answer cannot be applied to a pane the person has since switched to.
     */
    sessionId: string
    /**
     * The backend instance that minted the session (AD-7), same vocabulary as session.observationChanged: a reading out of a previous backend instance is refused rather than drawn over the current pane.
     */
    instanceId: string
    /**
     * The session's epoch within its backend instance, as minted at open. With instanceId this binds the reading to an incarnation.
     */
    sessionEpoch: number
    /**
     * The agent whose rule read this frame, from the enrolment act.
     */
    agent: string
    /**
     * Whether nocx has a rule for this agent at all. It is the distinction the surface rests on: 'this agent's rule could not read the screen' and 'nocx has no rule for this agent' both answer unknown, and only the second is permanent. False means the frame below is still legible and there is simply nothing yet reading it — which is the state a person WRITING a rule starts in.
     */
    hasRule: boolean
    /**
     * What the rule answered for this frame — exactly the value session.observationChanged would carry, from the same evaluation. The closed set and the meaning of each member are stated once, in contracts/session.observationChanged.schema.json.
     */
    state:
      | 'free_text'
      | 'permission_choice'
      | 'modal_choice'
      | 'working'
      | 'error'
      | 'unknown'
      | 'exited'
    /**
     * The answer the rule falls to when no branch matched — the document's own default. It is carried so a person can see that an answer came from the fall-through rather than from a branch, which is a different repair.
     */
    fallback:
      | 'free_text'
      | 'permission_choice'
      | 'modal_choice'
      | 'working'
      | 'error'
      | 'unknown'
      | 'exited'
    /**
     * Index into `branches` of the branch that produced `state`. ABSENT when no branch matched, in which case `state` is `fallback` — and absent is the honest form, because branch zero is a real branch and pointing at it would send a person to repair one that did not run.
     */
    matchedBranch?: number
    /**
     * The pane's screen as the rule sees it — the same panegrid.Frame the classification was decided from, never the renderer's own VT state and never persisted as the person's history.
     */
    frame: {
      /**
       * The grid's width in columns.
       */
      cols: number
      /**
       * The grid's height in rows.
       */
      rows: number
      /**
       * The cursor's column. It is carried because the cursor is the one thing an agent cannot forge — printed text cannot take it — so several predicates are about nothing else, and a person cannot check them without seeing where it is.
       */
      cursorX: number
      /**
       * The cursor's row.
       */
      cursorY: number
      /**
       * Whether the pane is in the alternate screen. An observation like any other; it decides nothing.
       */
      altScreen: boolean
      /**
       * One entry per row, in row order.
       */
      lines: {
        /**
         * One entry per COLUMN, so an index into this array is a column index. That exactness is the point rather than a convenience: ADR-0041 pins the emulator for its column geometry, both of the amendment's powers are positional, and a double-width character occupies two columns — the grapheme sits at the first and the second is an empty string, the continuation cell. A reader that joined these into a string would lose the alignment the cursor position is stated in.
         */
        cells: string[]
        /**
         * The repeated cell, when this row is nothing but one cell edge to edge. ABSENT for every other row. It is here because the input box is FOUND by its two full-width rules, so which rows are rules is the first thing a person comparing a screen against a rule reads off the screen.
         */
        rule?: string
        /**
         * The first non-blank column of the row. ABSENT for a blank row, deliberately: column zero is a real column, and a blank row drawn as opening there looks like chrome. 'Opens with' is what separates a chrome marker from the same glyph wrapped into a transcript line, so this is the other fact a person reads first.
         */
        opensAt?: number
      }[]
    }
    /**
     * Where each of the rule's anchors bound, in document order — a derived anchor listed beneath the one it is computed from. The anchors are the arithmetic, and they are where a rule breaks when a TUI moves its chrome, so a row number is what makes 'the box did not bind' repairable.
     */
    anchors: {
      /**
       * The anchor's name in the document.
       */
      name: string
      /**
       * How it binds: searchUp, offset or firstNonBlankBelow.
       */
      kind: string
      /**
       * The anchor this one is computed from. Absent for one computed from the frame's own bottom edge.
       */
      from?: string
      /**
       * False when the chrome this anchor names was not on the screen. An unbound anchor is ABSENT rather than bound at row zero, so `row` is carried only when this is true.
       */
      bound: boolean
      /**
       * The row it bound to. Present only when bound.
       */
      row?: number
    }[]
    /**
     * The ordered branch walk, in document order — which is the order it was evaluated in, and that order is a safety property of the document rather than a presentation choice: the dialog branches come before the free-text branch so a dialog can never be masked by an input box drawn beneath it.
     */
    branches: {
      /**
       * What this branch answers. For the three-valued `below` branch it is the value its verdict actually produced, and it is ABSENT when the branch produced none.
       */
      state?:
        | 'free_text'
        | 'permission_choice'
        | 'modal_choice'
        | 'working'
        | 'error'
        | 'unknown'
        | 'exited'
      /**
       * False for every branch after the one that matched. A person who cannot see that a later branch was never asked will 'fix' one that could not have run.
       */
      reached: boolean
      /**
       * Whether this branch produced the answer.
       */
      matched: boolean
      /**
       * The conjunction, in document order. Empty for a `below` branch, which carries `below` instead.
       */
      predicates: {
        /**
         * Which predicate of the closed set this is.
         */
        kind: string
        /**
         * The anchor it names. Absent for one that reads the cursor.
         */
        anchor?: string
        /**
         * The predicate's own arguments, rendered — so a person reads what was looked for and not only whether it was found. Only the arguments the kind actually reads: a view that printed every field would invite a repair to one the predicate ignores.
         */
        detail?: string
        /**
         * False for every predicate after the one that failed. THIS IS WHERE THE BRANCH STOPPED, and it is the acceptance criterion's own sentence: a conjunction short-circuits, and reporting the rest as merely 'not held' would send a person to the wrong line.
         */
        evaluated: boolean
        /**
         * Whether it held. Meaningful only when evaluated.
         */
        held: boolean
        /**
         * The row span this predicate was PERMITTED to search, when it searches one and its anchor bound. The cap is the engine's, not the document's — it is what stops an agent whose printed lines abut the chrome from extending the area a forged marker would be looked for in — so seeing it is how a person tells 'the rule looked in the wrong place' from 'the rule was not allowed to look that far'.
         */
        region?: {
          /**
           * First row of the span, inclusive.
           */
          from: number
          /**
           * Last row of the span, inclusive.
           */
          to: number
        }
      }[]
      /**
       * The three-valued branch's answer, kept three-valued. All rows recognised, a counterexample, or nothing drawn there at all — and folding the middle into the last is exactly the collapse that turns a refusal into free_text, which is a keystroke into whatever appears next.
       */
      below?: {
        /**
         * The anchor it reads below.
         */
        anchor: string
        /**
         * The cells a row down there may open with. Anything else is chrome the rule has not seen.
         */
        glyphs: string[]
        /**
         * False when the anchor was absent, in which case the verdict decided nothing at all. Carried separately from the verdict because a view showing only the verdict would look like it had.
         */
        anchorBound: boolean
        /**
         * `allMatched` — every row down there opened with a recognised glyph; `counterexample` — one did not; `nothing` — nothing is drawn there, which decides nothing and falls through to the branches after it.
         */
        verdict: 'nothing' | 'allMatched' | 'counterexample'
      }
    }[]
    /**
     * The value-reading half of the rule, in document order: for each extractor the rows it was PERMITTED to read and the rows it actually read. Both halves, because an extractor that read nothing is a different repair depending on which of the two was wrong. An extractor's yield reaches nothing that decides — it is evaluated after the verdict and from a value no branch can see — so a rule that reads MORE off a screen can never answer that screen differently.
     */
    extractors: {
      /**
       * The name the document gave the extractor.
       */
      name: string
      /**
       * The anchor its region is computed from.
       */
      anchor: string
      /**
       * The row span the engine allowed. ABSENT when the anchor did not bind, in which case the extractor never ran at all — which is a different thing from running and matching nothing.
       */
      region?: {
        /**
         * First row of the span, inclusive.
         */
        from: number
        /**
         * Last row of the span, inclusive.
         */
        to: number
      }
      /**
       * One entry per row it matched, in the order the screen drew them. Empty when it ran and read nothing.
       */
      rows: {
        /**
         * The named capture groups that participated, sorted by name so the order is the same on every poll. A group that did not participate contributes NO entry rather than an empty one, because absent and empty are different claims and only one of them is true.
         */
        fields: {
          /**
           * The capture group's name.
           */
          name: string
          /**
           * What it captured.
           */
          value: string
        }[]
      }[]
    }[]
  }
}
