/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.calibration.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of agent.calibration and of agent.calibration.answer: the panes nocx is watching, and — when one was named and is still watched — the state of that agent's calibration (nocx-etejh). Both methods answer the same shape because a write is a read of the state it produced, and a surface that had to ask again would draw a walk one step behind itself. The step list is the closed one the backend owns: what a person is asked, in what order, and which three of them a calibration cannot complete without.
 */
export interface AgentCalibration {
  /**
   * Every pane nocx is currently watching, in pane order. It rides with every answer rather than in a method of its own so the poll that refreshes the walk also refreshes the list: a pane whose observation closed leaves the list on the next answer, which is how a surface learns the agent it was calibrating is gone.
   */
  panes: {
    /**
     * The pane's server-authoritative session id (AD-7), which is what agent.calibration takes back to ask about it.
     */
    sessionId: string
    /**
     * The agent named by the enrolment act, verbatim. It is read from the act rather than taken from the caller, because a caller that named the agent would be a second owner of which rule a pane is under — and of which agent a labelled set belongs to.
     */
    agent: string
  }[]
  /**
   * The named pane's calibration. ABSENT when no pane was named, and absent when the pane named is no longer watched — a race a polling surface passes through rather than an error.
   */
  calibration?: {
    /**
     * The pane this calibration is of, echoed back so a late answer cannot be applied to a pane the person has since switched to.
     */
    sessionId: string
    /**
     * The agent being calibrated, as the enrolment act named it. A labelled set belongs to an agent rather than to a pane: the same agent calibrated in any pane answers the same rule.
     */
    agent: string
    /**
     * The closed, ordered list of what a person is asked to produce. The required three come first deliberately, so a walk abandoned part way is most likely to have produced the labels a rule cannot be verified without.
     */
    steps: {
      /**
       * The state the frame captured for this step is labelled with. exited is deliberately not among them: it is a fact about the process, and reading it off a screen would mean believing an agent that printed the word.
       */
      label: 'idle' | 'working' | 'asks-you' | 'waiting-on-child' | 'error' | 'menu-open'
      /**
       * Whether the calibration can complete without it. The three required ones are the three a person can produce on demand; the optional ones, uncalibrated, fall to unknown, which every consumer treats as busy — a refusal rather than a wrong answer.
       */
      required: boolean
      /**
       * The instruction the person reads, in the second person. It is the question the label is the answer to, which is why the label is not something the surface chooses.
       */
      ask: string
      /**
       * The driver state this screen must classify to when a rule is later verified against the set. The two vocabularies are mapped in exactly one place; a second mapping would disagree the first time a state was added.
       */
      expect: 'free_text' | 'permission_choice' | 'modal_choice' | 'working' | 'error'
    }[]
    /**
     * What this agent's rule has EARNED against the labelled set (nocx-jse6x). Typing authority is not a property of who wrote a rule: it is replayed against every labelled frame, and each must classify to the state the person was asked to produce. A rule that has not done that may still light the indicator — a wrong dot costs nothing — and may not be typed against, because a mistimed keystroke does not merely fail to arrive, it answers whatever modal is on screen. Always present: an agent with no set has an unverified verdict rather than none.
     */
    verification: {
      /**
       * Whether nocx may type into a pane running this agent. It is the whole consequence of everything else here, and a surface must state it rather than leave a person to infer it from a count.
       */
      mayType: boolean
      /**
       * How many labelled frames the set holds. A declined state contributes none, because nothing was captured for it — so this is what the rule was actually checked against rather than what it might have been.
       */
      labelled: number
      /**
       * How many of those the rule answered with their label's state. Equal to labelled exactly when the rule verified.
       */
      agreed: number
      /**
       * One entry per labelled frame the rule answered with something else, in the order the person was asked for them. Both sides travel because the point of showing them is repair: one of the two is wrong, and the person is the only one who can say which.
       */
      disagreements: {
        /**
         * The state the person was asked to produce.
         */
        label: string
        /**
         * The driver state that label must classify to. It is the same value the step carries, because the two vocabularies are mapped in exactly one place.
         */
        expected: string
        /**
         * What the rule actually answered for that frame.
         */
        got: string
      }[]
      /**
       * Why an unverified verdict is unverified, in the words a person reads. ABSENT exactly when the rule verified — an empty string would be a claim that there was a reason and it was nothing.
       */
      reason?: string
    }
    /**
     * The calibration in progress on this pane, ABSENT when there is none. A walk that has answered every step has been written out and is no longer in progress, so it is absent then too — what it produced is under stored.
     */
    walk?: {
      /**
       * The step being asked for, by its index in steps. This is the only thing that decides which label the next capture writes.
       */
      pending: number
      /**
       * One record per step answered so far, in the order they were asked.
       */
      given: AgentCalibrationRecord[]
    }
    /**
     * The labelled set on disk for this agent, ABSENT when the agent has never been calibrated. It is written only when a walk completes, so an abandoned retry never destroys the set a rule was verified against.
     */
    stored?: {
      /**
       * Whether the set carries every required label with a frame behind it. It is DERIVED from the labels present and never stored on disk: a set arrives from a file a person can edit, so a stored flag would be a claim rather than a fact.
       */
      complete: boolean
      /**
       * One record per step the person was asked, in the order they were asked.
       */
      labels: AgentCalibrationRecord[]
    }
  }
}
export interface AgentCalibrationRecord {
  /**
   * The state this record is about.
   */
  label: string
  /**
   * True for a state the person was asked for and declined. A declined step is written down rather than dropped, and the difference is load-bearing: absent means nobody was ever asked, skipped means they were asked and said no. Both are uncalibrated, and only one of them is a decision.
   */
  skipped: boolean
  /**
   * The mark in the set's capture that this label's frame replays to. ABSENT for a skipped label, because there is no frame behind it and a mark that pointed anywhere would point at another step's screen.
   */
  atMs?: number
}
