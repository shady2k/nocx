/**
 * A PERSON'S OWN RULE FOR AN AGENT (nocx-y6w66;
 * contracts/agent.rules.schema.json).
 *
 * nocx reads an agent pane's state from a rule. This is where the person who
 * runs an agent nocx reads wrongly gets to write one, switch detection off for
 * it, or go back to the rule nocx ships — without waiting for a release.
 *
 * Four methods and one shape. All three writes answer with the state they
 * produced, so the page never draws a rule one round trip behind the person's
 * own edit; and the read carries the directory, because editing the file by
 * hand is half of what the feature is for.
 *
 * A PULL, like the calibration beside it: the request is the looking.
 */
import type { Dispatcher } from './dispatcher'
import type { AgentRule, AgentRules as AgentRulesResult } from './generated/agent.rules'

/** Every agent this build ships a rule for, and where the person's own
 *  documents live. */
export type AgentRules = AgentRulesResult

/** One agent's detection: where the rule reading its pane comes from, the
 *  document as it stands, and why it could not be used if it could not be. */
export type AgentRuleRow = AgentRule

/** Where the rule reading an agent's pane comes from. Closed: `shipped` and
 *  `user` read the pane, `disabled` and `unreadable` do not. */
export type AgentRuleState = AgentRule['state']

export class AgentRulesClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  /** Every agent nocx ships a rule for, with the person's half beside it. */
  read(): Promise<AgentRules> {
    return this.dispatcher.call<AgentRules>('agent.rules', {})
  }

  /**
   * Record the person's own rule for an agent, replacing whatever they had.
   * The document is text, and the backend compiles it before it is stored: a
   * document that could never answer is refused here, while the person is
   * looking at it.
   */
  set(agent: string, document: string): Promise<AgentRules> {
    return this.dispatcher.call<AgentRules>('agent.rules.set', { agent, document })
  }

  /**
   * Switch detection for an agent. Off is a state and not a deletion: their
   * document stays where it is, and switching back on returns exactly it.
   */
  setEnabled(agent: string, enabled: boolean): Promise<AgentRules> {
    return this.dispatcher.call<AgentRules>('agent.rules.setEnabled', { agent, enabled })
  }

  /**
   * Remove the person's own rule, which restores the one nocx ships — with
   * detection on. This is the only operation that puts the shipped rule back.
   */
  remove(agent: string): Promise<AgentRules> {
    return this.dispatcher.call<AgentRules>('agent.rules.delete', { agent })
  }
}
