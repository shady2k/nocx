// @vitest-environment jsdom
/**
 * AgentApprovalPrompt tests — the renderer half of an approval question
 * (nocx-z9hj4). What a user can do: see ONE kind of question whether the
 * risk was an effect coming in (a policy escalation) or a secret going out
 * (an egress finding), see the tool, the arguments and — for egress — what
 * was found and where, never the material itself; and decide.
 *
 * Since nocx-0mvpy.2 the where and the what are ROWS: machine, tab, cwd,
 * the arguments and the effect each get their own row of one fact list, in
 * that order, and the lead is one sentence. The tests assert the rows BY
 * NAME — the paragraph assertions they replaced could not see a fact stop
 * being on the window.
 *
 * Since nocx-gycwo the decision has a WIDTH as well as a direction: a policy
 * question offers allow and deny at once, in this session and always, so the
 * place a person is asked is the place they can stop being asked. An egress
 * question keeps two answers, both `once` — "always send secrets to the model
 * provider" is not a standing decision to be made by a button sitting next to
 * five others.
 *
 * What the surface must not overstate (criterion 4): it says what approval
 * covers — this call has not run, and no call after it will — and does NOT
 * claim the domain is untouched.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { createSignal } from 'solid-js'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { AgentApprovalPrompt, standingAnswerReceipt } from './agent-approval-prompt'
import { AgentInputTarget } from './agent-ask'
import { recordApprovalDecision } from './agent-approval-decision'
import { BlockManager } from './scrollback/blocks'
import { CommandSnapshotStore } from './command-snapshot'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import { EFFECT_LABEL } from './effect-labels'
import type { AgentApprove } from './generated/agent.approve'

const POLICY_ASK: AgentApprovalRequested = {
  runId: '7',
  attempt: 1,
  tool: 'files.read',
  callId: 'call_1',
  argHash: 'hash-a',
  arguments: '{"path":"/repo/a.txt"}',
  reason: 'policy',
  // The effect the gate decided on, sent by the backend (nocx-zd1vp). The
  // surface must never work it out from the tool name — that would be a rule
  // keyed by a tool name in everything but storage (ADR-0028 decision 4).
  effect: 'observe',
  standing: { available: false, rule: '', reason: '' },
  resource: { kind: 'path', id: '/repo/a.txt' },
}

const EGRESS_ASK: AgentApprovalRequested = {
  ...POLICY_ASK,
  reason: 'egress',
  wasError: false,
  findings: [
    { source: 'known', secretName: 'github-token', start: 0, end: 5 },
    { source: 'heuristic', kind: 'openai-api-key', start: 11, end: 40 },
  ],
}

const STANDING_ASK: AgentApprovalRequested = {
  ...POLICY_ASK,
  tool: 'run',
  arguments: '{"command":"df -h"}',
  standing: { available: true, rule: 'df -h', reason: '' },
}

function renderPrompt(overrides?: Partial<Parameters<typeof AgentApprovalPrompt>[0]>) {
  const props = {
    open: true,
    ask: POLICY_ASK,
    busy: false,
    onDecide: vi.fn(),
    ...overrides,
  }
  const utils = render(() => <AgentApprovalPrompt {...props} />)
  return { ...utils, props }
}

/** A recorder for the seam the surface actually owns: direction and width. */
function recordDecisions() {
  const decisions: Array<[boolean, AgentApprove['scope']]> = []
  return {
    decisions,
    onDecide: (approved: boolean, scope: AgentApprove['scope']) =>
      decisions.push([approved, scope]),
  }
}

/** Every row as `name` + `value`, in the order the window reads. The note
 *  lives inside the value cell (it qualifies that value and must not be
 *  able to drift to another row), so it is subtracted here. */
function rows(container: HTMLElement): Array<[string, string]> {
  return Array.from(container.querySelectorAll('.ui-fact-list__row')).map((r) => {
    const value = r.querySelector('.ui-fact-list__value')?.textContent ?? ''
    const note = r.querySelector('.ui-fact-list__note')?.textContent ?? ''
    return [r.querySelector('.ui-fact-list__name')?.textContent ?? '', value.replace(note, '')]
  })
}

/** Every row's name, in order — the assertion this task is about. */
function names(container: HTMLElement): string[] {
  return rows(container).map(([name]) => name)
}

const SID = '9bb9a7602c27e8ba0741972c7049b54b'

/** What a pane answers about itself. `cwd`/`cwdVerified` travel together
 *  (nocx-n7xha): a cwd an OSC 7 report confirmed is a claim, the one a
 *  session was opened with is a guess, and this window is the one place a
 *  guess printed as fact costs most. */
type Where = { tab: string; machine: string; cwd: string; cwdVerified: boolean }

/** A local pane in a directory the shell has confirmed. */
const HERE: Where = { tab: 'home/dev', machine: '', cwd: '/home/dev', cwdVerified: true }

describe('AgentApprovalPrompt — a session is named, never numbered (nocx-vnzek)', () => {
  afterEach(cleanup)

  const SESSION_ASK: AgentApprovalRequested = {
    ...POLICY_ASK,
    tool: 'readScreen',
    arguments: `{"sessionId":"${SID}"}`,
    resource: { kind: 'session', id: SID },
  }

  it("says which pane the call reaches, in the pane's own name", () => {
    const r = renderPrompt({
      ask: SESSION_ASK,
      sessionWhere: (id: string) => (id === SID ? HERE : null),
    })
    // The pane's own name, not the 32-hex handle the wire carried.
    expect(r.container.textContent ?? '').toContain('home/dev')
    expect(r.container.textContent ?? '').not.toContain(SID)
  })

  /**
   * CHANGED DELIBERATELY (nocx-0mvpy.2). The pane used to be the session
   * argument's row — `sessionId` / "home/dev on this machine". Now the
   * machine and the tab are their OWN rows, and the session argument is
   * covered by them: where a call lands has one owner on this surface, so
   * the handle appears on no row and in no blob.
   */
  it('renders the session as the pane — machine and tab rows, never the id and never a blob', () => {
    const { container } = renderPrompt({ ask: SESSION_ASK, sessionWhere: () => HERE })
    expect(container.querySelector('.ui-code-block')).toBeNull()
    expect(rows(container)).toEqual([
      ['machine', 'this machine'],
      ['tab', 'home/dev'],
      ['cwd', '/home/dev'],
      ['effect', 'read and inspect'],
    ])
    expect(container.textContent ?? '').not.toContain(SID)
  })

  it('still accounts for the session when no pane can name it — without the id', () => {
    const { container } = renderPrompt({ ask: SESSION_ASK, sessionWhere: () => null })
    // Nothing is dropped: the argument is still a row. But an id nothing on
    // screen can name is still an id, so it stays off the surface.
    expect(rows(container)).toEqual([
      ['sessionId', 'a session no tab in this window holds'],
      ['effect', 'read and inspect'],
    ])
    expect(container.textContent ?? '').not.toContain(SID)
  })

  it('derives the session row from the value, never from the resource', () => {
    // A sessionId whose resource is a PATH — a proposal the backend's
    // resource derivation does not cover — is still a fact about the
    // value: if a pane on screen holds that session, the row says so. The
    // window must not print "no tab holds it" on the strength of an
    // invariant enforced in another module (internal/agenttools/registry).
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'run',
        arguments: `{"sessionId":"${SID}"}`,
        resource: { kind: 'path', id: '/tmp' },
      },
      sessionWhere: () => HERE,
    })
    expect(rows(container)).toEqual([
      ['sessionId', 'home/dev on this machine'],
      ['effect', 'read and inspect'],
    ])
    expect(container.textContent ?? '').not.toContain(SID)
  })

  it('says nothing about a tab or a directory for a path — a path is the person’s own word', () => {
    const { container } = renderPrompt({ ask: POLICY_ASK, sessionWhere: () => HERE })
    const text = container.textContent ?? ''
    expect(text).not.toContain('home/dev')
    expect(text).not.toContain('cwd')
    // The path argument itself is still a row, named as the model named it;
    // the effect closes the list.
    expect(rows(container)).toEqual([
      ['path', '/repo/a.txt'],
      ['effect', 'read and inspect'],
    ])
  })
})

/**
 * What the person is actually deciding, in a sentence (nocx-njn8s).
 *
 * The prompt used to print `{"command": "df -h", "sessionId": "ab607…cf95"}`
 * and leave the person to parse it by eye — with the session id nocx-vnzek
 * took off the tool-call line still sitting inside the blob, and the MACHINE,
 * the fact that decides whether a destructive command lands on this laptop or
 * on a production host, never named at all.
 *
 * Since nocx-0mvpy.2 the sentence is ONE sentence and the machine and the
 * tab are rows beneath it — a person reads where the call lands at a glance,
 * not by parsing the lead.
 */
describe('AgentApprovalPrompt — what the call does, where (nocx-njn8s, nocx-0mvpy.2)', () => {
  afterEach(cleanup)

  const RUN_ASK: AgentApprovalRequested = {
    ...POLICY_ASK,
    tool: 'run',
    effect: 'mutate-destructive',
    arguments: `{"command":"df -h","sessionId":"${SID}"}`,
    resource: { kind: 'session', id: SID },
  }

  const LOCAL: Where = HERE

  it('states machine, tab, cwd, the arguments and the effect each as its own row, in that order', () => {
    // The acceptance: every fact a person decides on is a row, read at a
    // glance. machine, tab, cwd, then the arguments the window does not
    // already state, then the effect — one fact list.
    const { container } = renderPrompt({
      ask: {
        ...RUN_ASK,
        arguments: `{"command":"df -h","sessionId":"${SID}","timeoutMs":5000}`,
      },
      sessionWhere: () => LOCAL,
    })
    expect(names(container)).toEqual(['machine', 'tab', 'cwd', 'timeoutMs', 'effect'])
    expect(rows(container)[0]).toEqual(['machine', 'this machine'])
    expect(rows(container)[1]).toEqual(['tab', 'home/dev'])
    expect(rows(container)[2]).toEqual(['cwd', '/home/dev'])
    expect(rows(container)[3]).toEqual(['timeoutMs', '5000'])
    expect(rows(container)[4]).toEqual(['effect', 'make changes that cannot be undone'])
  })

  it('leads with ONE sentence — the machine and the tab are rows, not prose', () => {
    const { container } = renderPrompt({ ask: RUN_ASK, sessionWhere: () => LOCAL })
    const text = container.textContent ?? ''
    expect(text).toContain('The assistant wants to run this command:')
    // The two facts that used to hang off the sentence are rows now.
    expect(text).not.toContain('on this machine')
    expect(text).not.toContain('in the tab')
    // The command itself, verbatim and alone — not wrapped in JSON.
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('df -h')
    expect(rows(container)).toEqual([
      ['machine', 'this machine'],
      ['tab', 'home/dev'],
      ['cwd', '/home/dev'],
      ['effect', 'make changes that cannot be undone'],
    ])
  })

  it('never prints the session id back, on any surface of the question', () => {
    const { container } = renderPrompt({ ask: RUN_ASK, sessionWhere: () => LOCAL })
    expect(container.textContent ?? '').not.toContain(SID)
  })

  it('names the machine the pane is actually talking to', () => {
    const { container } = renderPrompt({
      ask: RUN_ASK,
      sessionWhere: () => ({
        tab: 'srv-01',
        machine: 'deploy@srv-01.example.com',
        cwd: '/srv/app',
        cwdVerified: true,
      }),
    })
    expect(rows(container)[0]).toEqual(['machine', 'deploy@srv-01.example.com'])
    const text = container.textContent ?? ''
    // "this machine" would be a lie about where the command lands.
    expect(text).not.toContain('this machine')
  })

  it('says nothing about where when no pane holds the session, and still shows the command', () => {
    const { container } = renderPrompt({ ask: RUN_ASK, sessionWhere: () => null })
    const text = container.textContent ?? ''
    expect(text).toContain('run this command')
    expect(text).not.toContain('in the tab')
    expect(text).not.toContain('this machine')
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('df -h')
  })

  /**
   * CHANGED DELIBERATELY (nocx-n7xha). njn8s put the blob back whenever
   * `run` carried a third argument, because the sentence had words for two
   * and dropping the third silently would have been worse than a blob. The
   * sentence is now accompanied by a row per argument it does not itself
   * state, so the third argument is SHOWN rather than dropped — which is
   * what the fallback was protecting, obtained without giving up the
   * sentence and without putting the session id back on screen.
   */
  it('shows an argument it has no words for as a row, and keeps the sentence', () => {
    const args = `{"command":"df -h","sessionId":"${SID}","timeoutMs":5000}`
    const { container } = renderPrompt({
      ask: { ...RUN_ASK, arguments: args },
      sessionWhere: () => LOCAL,
    })
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('df -h')
    const rows_ = Array.from(container.querySelectorAll('.ui-fact-list__row')).map(
      (r) => r.textContent,
    )
    expect(rows_).toContain('timeoutMs5000')
    expect(container.textContent ?? '').not.toContain(SID)
    expect(container.textContent ?? '').not.toContain('with these arguments')
  })

  it('falls back to the verbatim blob when the arguments are not an object at all', () => {
    const { container } = renderPrompt({
      ask: { ...RUN_ASK, arguments: 'not json' },
      sessionWhere: () => LOCAL,
    })
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('not json')
    // Where a call lands does not depend on its arguments parsing
    // (nocx-0mvpy.2): the pane's rows and the effect render beside the blob.
    expect(names(container)).toEqual(['machine', 'tab', 'cwd', 'effect'])
  })

  it('names the machine for every other tool too — as its own row (nocx-0mvpy.2)', () => {
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'readScreen',
        arguments: `{"sessionId":"${SID}"}`,
        resource: { kind: 'session', id: SID },
      },
      sessionWhere: () => ({
        tab: 'srv-01',
        machine: 'deploy@srv-01.example.com',
        cwd: '/srv/app',
        cwdVerified: true,
      }),
    })
    // Where the call lands is stated ONCE: the machine and tab rows cover
    // the session argument, so no sessionId row repeats it.
    expect(names(container)).toEqual(['machine', 'tab', 'cwd', 'effect'])
    expect(rows(container)[0]).toEqual(['machine', 'deploy@srv-01.example.com'])
  })

  it('does not say the assistant WANTS to run a command that has already run', () => {
    // The egress gate screens a tool RESULT, so by the time this question is
    // asked the command is behind us and what is being decided is whether
    // what it printed may leave for the provider. "wants to run" there would
    // misreport what has already happened to the machine.
    const { container } = renderPrompt({
      ask: { ...RUN_ASK, reason: 'egress', wasError: false, findings: [] },
      sessionWhere: () => LOCAL,
    })
    const text = container.textContent ?? ''
    expect(text).not.toContain('wants to run')
    expect(text).toContain('The command that produced it ran')
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('df -h')
    // The effect row is a POLICY fact: an egress question is about what a
    // result may do, not about the call that already happened.
    expect(names(container)).not.toContain('effect')
  })

  it('states that fetch.url reaches the network from this machine', () => {
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'fetch.url',
        effect: 'cross-boundary',
        arguments: '{"url":"https://example.test/page"}',
        resource: { kind: 'destination', id: 'https://example.test/page' },
      },
      sessionWhere: () => null,
    })
    expect(rows(container)).toEqual([
      ['url', 'https://example.test/page'],
      ['network', 'reaches the network from this machine'],
      ['effect', EFFECT_LABEL['cross-boundary']],
    ])
  })

  it('says what the call can do, in the effect vocabulary and never from the tool name', () => {
    const { container } = renderPrompt({ ask: RUN_ASK, sessionWhere: () => LOCAL })
    expect(rows(container).find(([name]) => name === 'effect')).toEqual([
      'effect',
      'make changes that cannot be undone',
    ])
  })

  it('names execution as handing work to another agent', () => {
    const { container } = renderPrompt({
      ask: { ...RUN_ASK, effect: 'delegate' },
      sessionWhere: () => LOCAL,
    })
    expect(rows(container).find(([name]) => name === 'effect')).toEqual([
      'effect',
      'hand work to another agent',
    ])
  })
})

/**
 * The window states FACTS, not JSON (nocx-n7xha).
 *
 * What a person saw: a 32-hex session id printed back inside a JSON blob,
 * above a sentence that named the tab the blob was identifying; no word
 * anywhere about which directory the call lands in; and two of five
 * paragraphs spent explaining policy.
 *
 * Four properties are asserted here and they are the whole bead:
 *
 *  - EXHAUSTIVE BY CONSTRUCTION. Every parsed argument is a named row, for
 *    every tool, including one this surface has never heard of. That is
 *    what makes dropping the blob honest — njn8s's rule survives, it is
 *    just no longer satisfied per-tool.
 *  - THE ID IS GONE. A value naming a resource the window has already named
 *    renders as the product's name for it, and the handle appears on no
 *    surface of the window.
 *  - A GUESS IS NOT A FACT. The working directory is named, and a cwd no
 *    OSC 7 report confirmed says so (AD-5). An approval window that printed
 *    a guess as fact would lie at the moment lying is most expensive.
 *  - EVERY FACT IS A ROW (nocx-0mvpy.2). The machine, the tab, the
 *    directory, the arguments and the effect are each a row of the one
 *    list, in that order, so the where and the what are read at a glance —
 *    the lead is one sentence, and the paragraphs below say only what
 *    cannot be a row.
 */
describe('AgentApprovalPrompt — the facts, not the JSON (nocx-n7xha)', () => {
  afterEach(cleanup)

  const SESSION_ASK: AgentApprovalRequested = {
    ...POLICY_ASK,
    tool: 'readScreen',
    arguments: `{"sessionId":"${SID}"}`,
    resource: { kind: 'session', id: SID },
  }

  it('shows every parsed argument as a named row, including ones it has no words for', () => {
    const { container } = renderPrompt({
      ask: {
        ...SESSION_ASK,
        arguments: `{"sessionId":"${SID}","region":{"start":0,"end":24},"why":"because"}`,
      },
      sessionWhere: () => HERE,
    })
    // machine, tab, cwd and effect are rows too (nocx-0mvpy.2); the parsed
    // arguments sit between the directory and the effect, in the model's
    // own order, and the session argument is covered by the pane's rows.
    expect(names(container)).toEqual(['machine', 'tab', 'cwd', 'region', 'why', 'effect'])
    expect(rows(container)[3]).toEqual(['region', '{"start":0,"end":24}'])
    expect(rows(container)[4]).toEqual(['why', 'because'])
  })

  it('does the same for a tool nobody has written a sentence for', () => {
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'someone.elses.tool',
        arguments: '{"target":"prod","force":true,"retries":3}',
        resource: null,
      },
    })
    expect(rows(container)).toEqual([
      ['target', 'prod'],
      ['force', 'true'],
      ['retries', '3'],
      ['effect', 'read and inspect'],
    ])
    expect(container.textContent ?? '').toContain('someone.elses.tool')
  })

  it('keeps the verbatim blob when the arguments are not an object — that fallback stays', () => {
    const { container } = renderPrompt({
      ask: { ...SESSION_ASK, arguments: '[1,2,3]' },
      sessionWhere: () => HERE,
    })
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('[1,2,3]')
    // The blob is the fallback for the ARGUMENTS; the where and the effect
    // are rows in both branches, because where a call lands does not depend
    // on the arguments parsing.
    expect(names(container)).toEqual(['machine', 'tab', 'cwd', 'effect'])
    expect(container.textContent ?? '').toContain('with these arguments')
  })

  it('names the working directory the shell confirmed, and says the shell confirmed it', () => {
    const { container } = renderPrompt({ ask: SESSION_ASK, sessionWhere: () => HERE })
    const row = rows(container).find(([name]) => name === 'cwd')
    expect(row?.[1]).toContain('/home/dev')
    const note = container.querySelector('.ui-fact-list__note')?.textContent ?? ''
    expect(note).toContain('reported by the shell')
    // It is the pane's directory AS OF NOW. Binding the effect to the
    // precondition is a different bead (nocx-d6gn4.1) and this window must
    // not read as though it had already happened.
    expect(note).toContain('as of now')
  })

  it('says so when the working directory is a guess the shell never confirmed', () => {
    const { container } = renderPrompt({
      ask: SESSION_ASK,
      sessionWhere: () => ({ ...HERE, cwd: '~/Documents', cwdVerified: false }),
    })
    const row = rows(container).find(([name]) => name === 'cwd')
    expect(row?.[1]).toContain('~/Documents')
    const note = container.querySelector('.ui-fact-list__note')?.textContent ?? ''
    expect(note).toContain('has not confirmed')
    expect(note).toContain('as of now')
  })

  it('says nothing about a directory when the pane has none to report', () => {
    const { container } = renderPrompt({
      ask: SESSION_ASK,
      sessionWhere: () => ({ ...HERE, cwd: '', cwdVerified: false }),
    })
    expect(names(container)).toEqual(['machine', 'tab', 'effect'])
  })

  it("names run's directory too, in the same list as the machine and the tab", () => {
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'run',
        effect: 'mutate-destructive',
        arguments: `{"command":"rm -rf build","sessionId":"${SID}"}`,
        resource: { kind: 'session', id: SID },
      },
      sessionWhere: () => HERE,
    })
    const text = container.textContent ?? ''
    // The command keeps its own block, and the lead names it in one
    // sentence; the where is rows beneath.
    expect(text).toContain('The assistant wants to run this command:')
    expect(container.querySelector('.ui-code-block')?.textContent).toBe('rm -rf build')
    // The two arguments the window already states — command in the block,
    // sessionId as the pane's rows — are not repeated: where a call lands
    // has one owner on this surface, not two.
    expect(rows(container)).toEqual([
      ['machine', 'this machine'],
      ['tab', 'home/dev'],
      ['cwd', '/home/dev'],
      ['effect', 'make changes that cannot be undone'],
    ])
  })

  it('puts the decision facts before the policy prose', () => {
    const { container } = renderPrompt({
      ask: { ...SESSION_ASK, standing: STANDING_ASK.standing },
      sessionWhere: () => HERE,
    })
    const text = container.textContent ?? ''
    const facts = text.indexOf('effect')
    const covers = text.indexOf('Approving covers this call')
    const lasts = text.indexOf('An answer in this session lasts')
    expect(facts).toBeGreaterThan(-1)
    expect(covers).toBeGreaterThan(facts)
    expect(lasts).toBeGreaterThan(covers)
  })

  it('names the effect row a standing answer covers in product words', () => {
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        standing: { available: true, rule: '', reason: '' },
      },
    })
    expect(container.textContent ?? '').toContain(
      `is a standing answer for ${EFFECT_LABEL.observe}`,
    )
  })
})

describe('AgentApprovalPrompt', () => {
  afterEach(cleanup)

  it('names the tool, the arguments and the reason — the question a person decides', () => {
    const { container } = renderPrompt()
    expect(container.textContent).toContain('files.read')
    // The argument, as a named row. It used to be the JSON blob
    // `{"path":"/repo/a.txt"}`; a person deciding is owed the fact, not
    // the encoding (nocx-n7xha).
    expect(container.querySelector('.ui-fact-list__name')?.textContent).toBe('path')
    expect(container.querySelector('.ui-fact-list__value')?.textContent).toBe('/repo/a.txt')
    expect(container.querySelector('.ui-prompt[data-placement="top-sheet"]')).toBeTruthy()
  })

  it('states what approval covers and does not claim the domain is untouched (criterion 4)', () => {
    const { container } = renderPrompt()
    const text = container.textContent ?? ''
    expect(text).toContain('it has not run')
    expect(text).toContain('no call after it in this response will')
    expect(text).toContain('does not promise the terminal is untouched')
  })

  it('renders egress findings — facts, never the material — and distinguishes the sources', () => {
    const { container } = renderPrompt({ ask: EGRESS_ASK })
    const text = container.textContent ?? ''
    expect(text).toContain('Nothing was sent to the model provider')
    expect(text).toContain('Known vault material')
    expect(text).toContain('github-token')
    expect(text).toContain('Heuristic match')
    expect(text).toContain('openai-api-key')
    // The secret VALUE is never on the surface: only facts about it.
    expect(text).not.toContain('sk-')
  })

  it('says when the findings are in an ERROR string rather than a result', () => {
    const { container } = renderPrompt({ ask: { ...EGRESS_ASK, wasError: true } })
    expect(container.textContent).toContain('The tool failed')
  })

  it('a policy question offers three allowances and three refusals, each with its scope', () => {
    const { decisions, onDecide } = recordDecisions()
    const ui = renderPrompt({ ask: STANDING_ASK, onDecide })

    for (const name of [
      'Allow once — this proposal only',
      'Allow in this session — df -h — until this terminal session ends',
      'Allow always — df -h — in every session, from now on',
      'Deny once — this proposal only',
      'Deny in this session — df -h — until this terminal session ends',
      'Deny always — df -h — in every session, from now on',
    ]) {
      fireEvent.click(ui.getByRole('button', { name }))
    }

    expect(decisions).toEqual([
      [true, 'once'],
      [true, 'session'],
      [true, 'always'],
      [false, 'once'],
      [false, 'session'],
      [false, 'always'],
    ])
  })
  it('keeps the scope on the primary line and carries coverage on a readable second line', () => {
    const { container } = renderPrompt({ ask: STANDING_ASK })
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('.ui-button'))
    expect(buttons.map((button) => button.textContent)).toEqual([
      'Allow once — this proposal only',
      'Allow in this session — df -h — until this terminal session ends',
      'Allow always — df -h — in every session, from now on',
      'Deny once — this proposal only',
      'Deny in this session — df -h — until this terminal session ends',
      'Deny always — df -h — in every session, from now on',
    ])
    expect(
      buttons.map((button) => [
        button.querySelector('.ui-button__label')?.textContent,
        button.querySelector('.ui-button__secondary')?.textContent,
      ]),
    ).toEqual([
      ['Allow once', '— this proposal only'],
      ['Allow in this session', '— df -h — until this terminal session ends'],
      ['Allow always', '— df -h — in every session, from now on'],
      ['Deny once', '— this proposal only'],
      ['Deny in this session', '— df -h — until this terminal session ends'],
      ['Deny always', '— df -h — in every session, from now on'],
    ])
    expect(buttons.every((button) => button.querySelector('.ui-button__secondary'))).toBe(true)
    expect(buttons[0]?.getAttribute('aria-label')).toBe('Allow once — this proposal only')
    expect(buttons[1]?.getAttribute('aria-label')).toBe(
      'Allow in this session — df -h — until this terminal session ends',
    )
    expect(buttons[2]?.getAttribute('aria-label')).toBe(
      'Allow always — df -h — in every session, from now on',
    )
  })

  it('groups allowances and refusals into two rows of three', () => {
    const { container } = renderPrompt({ ask: STANDING_ASK })
    const groups = Array.from(container.querySelectorAll('.ui-action-group'))
    expect(groups).toHaveLength(2)
    const names = groups.map((g) =>
      Array.from(g.querySelectorAll('.ui-button')).map((b) => b.textContent),
    )
    expect(names).toEqual([
      [
        'Allow once — this proposal only',
        'Allow in this session — df -h — until this terminal session ends',
        'Allow always — df -h — in every session, from now on',
      ],
      [
        'Deny once — this proposal only',
        'Deny in this session — df -h — until this terminal session ends',
        'Deny always — df -h — in every session, from now on',
      ],
    ])
  })

  it("names the effect in the product's words, and reads it from effect and never from tool", () => {
    const { container } = renderPrompt()
    expect(container.textContent).toContain('read and inspect')

    cleanup()
    // Same tool name, a different effect: the words must follow `effect`.
    const { container: other } = renderPrompt({
      ask: { ...POLICY_ASK, effect: 'mutate-destructive' },
    })
    expect(other.textContent).toContain('make changes that cannot be undone')
    expect(other.textContent).not.toContain('read and inspect')
  })

  it('says how long a session answer lasts, and never promises the pane', () => {
    const { container } = renderPrompt({ ask: STANDING_ASK })
    const text = container.textContent ?? ''
    expect(text).toContain('in this session')
    // The permission binds to the terminal SESSION: restarting the shell in
    // the same pane asks again, so naming the pane would promise a lifetime
    // the answer does not have.
    expect(text).not.toContain('in this pane')
    expect(text).toContain('Agent policy page')
  })

  it('an egress question offers two answers, and both are once', () => {
    const { decisions, onDecide } = recordDecisions()
    const ui = renderPrompt({ ask: EGRESS_ASK, onDecide })

    expect(
      ui.queryByRole('button', {
        name: 'Allow always — every read and inspect call, in every session, from now on',
      }),
    ).toBeNull()
    expect(
      ui.queryByRole('button', {
        name: 'Allow in this session — every read and inspect call in this session',
      }),
    ).toBeNull()
    expect(
      ui.queryByRole('button', {
        name: 'Deny always — every read and inspect call, in every session, from now on',
      }),
    ).toBeNull()
    expect(ui.container.querySelectorAll('.ui-button')).toHaveLength(2)

    fireEvent.click(ui.getByRole('button', { name: 'Allow once — this proposal only' }))
    fireEvent.click(ui.getByRole('button', { name: 'Deny once — this proposal only' }))
    expect(decisions).toEqual([
      [true, 'once'],
      [false, 'once'],
    ])
  })

  it('dismissing the prompt is the NARROWEST refusal, never a standing one', () => {
    const { decisions, onDecide } = recordDecisions()
    const { container } = renderPrompt({ onDecide })
    const overlay = container.querySelector('.ui-prompt-overlay')!
    fireEvent.mouseDown(overlay)
    expect(decisions).toEqual([[false, 'once']])
  })

  it('disables every answer while the decision is in flight', () => {
    const { container } = renderPrompt({ ask: STANDING_ASK, busy: true })
    const buttons = Array.from(container.querySelectorAll('.ui-button'))
    expect(buttons).toHaveLength(6)
    for (const b of buttons) expect((b as HTMLButtonElement).disabled).toBe(true)
  })

  it('states the scope and carried rule in each answer control', () => {
    const effect = 'mutate-destructive'
    const view = renderPrompt({
      ask: { ...STANDING_ASK, effect },
    })
    const coverage = [
      'Allow once — this proposal only',
      'Allow in this session — df -h — until this terminal session ends',
      'Allow always — df -h — in every session, from now on',
      'Deny once — this proposal only',
      'Deny in this session — df -h — until this terminal session ends',
      'Deny always — df -h — in every session, from now on',
    ]
    for (const name of coverage) expect(view.getByRole('button', { name })).toBeTruthy()

    const egress = renderPrompt({ ask: { ...EGRESS_ASK, effect } })
    expect(egress.queryByRole('button', { name: /in this session/ })).toBeNull()
    expect(egress.queryByRole('button', { name: /always/ })).toBeNull()
    expect(view.queryByRole('button', { name: 'Allow once' })).toBeNull()
    expect(view.container.textContent).toContain(EFFECT_LABEL[effect])
  })
})

describe('AgentApprovalPrompt — standing answers name the carried rule (nocx-takqr.2)', () => {
  afterEach(cleanup)

  it('puts the backend rule sentence in each standing control', () => {
    const rule = 'df -h'
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'run',
        arguments: '{"command":"df -h"}',
        standing: { available: true, rule, reason: '' },
      },
    })
    expect(container.querySelector('.ui-button:nth-of-type(2)')?.textContent).toContain(rule)
    expect(container.textContent).toContain(`Allow in this session — ${rule}`)
    expect(container.textContent).toContain(`Allow always — ${rule}`)
    expect(container.textContent).toContain(`Deny in this session — ${rule}`)
    expect(container.textContent).toContain(`Deny always — ${rule}`)
  })

  it('offers only once when the backend says the rule cannot be shown', () => {
    const reason = 'the command uses an indirect wrapper'
    const { container } = renderPrompt({
      ask: {
        ...POLICY_ASK,
        tool: 'run',
        arguments: '{"command":"sudo df -h"}',
        standing: { available: false, rule: '', reason },
      },
    })
    expect(container.querySelectorAll('.ui-button')).toHaveLength(2)
    expect(container.textContent).toContain(reason)
    expect(names(container)).toContain('standing')
  })
})

/**
 * The expanded form beside the verbatim one (nocx-4h0m7.5).
 *
 * The incident: a script referred to `$HOME/xxx`, something upstream had been
 * rewriting `$HOME`, the rewrite silently stopped, and files in the real home
 * were deleted. Nothing that reads command TEXT could have caught it — the
 * text was correct. So the window shows what the variables READ AS, beside
 * the verbatim command and never instead of it.
 *
 * What a user can do that they could not before: see what `$HOME` currently
 * is before approving `rm -rf $HOME/x`, see which parts nocx refused to read
 * and why, and see when nocx could not read anything at all — three
 * different facts, three different words.
 */
describe('AgentApprovalPrompt — what the command’s variables read as (nocx-4h0m7.5)', () => {
  afterEach(cleanup)

  const EXPANSION_ASK: AgentApprovalRequested = {
    ...POLICY_ASK,
    tool: 'run',
    effect: 'mutate-destructive',
    arguments: '{"command":"rm -rf $HOME/x $(id -u)"}',
    resource: null,
    expansion: {
      asked: true,
      command: 'rm -rf /home/dev/x $(id -u)',
      parts: [
        { text: '$HOME', name: 'HOME', kind: 'parameter', state: 'expanded', value: '/home/dev' },
        {
          text: '$(id -u)',
          kind: 'command-substitution',
          state: 'unsafe',
          reason: 'it runs a command to produce its value',
        },
      ],
    },
  }

  it('shows the expanded form beside the verbatim command, never instead of it', () => {
    const { container } = renderPrompt({ ask: EXPANSION_ASK })
    const blocks = Array.from(container.querySelectorAll('.ui-code-block')).map(
      (b) => b.textContent,
    )
    expect(blocks).toContain('rm -rf $HOME/x $(id -u)')
    expect(blocks).toContain('rm -rf /home/dev/x $(id -u)')
    expect(container.textContent).toContain('the line above is what runs, exactly as written')
  })

  it('names each variable and what it reads as, on its own row', () => {
    const { container } = renderPrompt({ ask: EXPANSION_ASK })
    expect(rows(container)).toContainEqual(['$HOME', '/home/dev'])
  })

  it('leaves an unsafe expansion as written and says why', () => {
    const { container } = renderPrompt({ ask: EXPANSION_ASK })
    expect(rows(container)).toContainEqual(['$(id -u)', 'left exactly as written'])
    expect(container.textContent).toContain('it runs a command to produce its value')
  })

  it('says a value was NOT READ rather than implying nocx refused to read it', () => {
    const { container } = renderPrompt({
      ask: {
        ...EXPANSION_ASK,
        expansion: {
          asked: false,
          reason: 'nocx’s shell integration is not live in this session',
          command: 'rm -rf $HOME/x $(id -u)',
          parts: [
            { text: '$HOME', name: 'HOME', kind: 'parameter', state: 'unasked' },
            {
              text: '$(id -u)',
              kind: 'command-substitution',
              state: 'unsafe',
              reason: 'it runs a command to produce its value',
            },
          ],
        },
      },
    })
    expect(rows(container)).toContainEqual(['$HOME', 'not read'])
    expect(rows(container)).toContainEqual(['$(id -u)', 'left exactly as written'])
    expect(container.textContent).toContain('nocx’s shell integration is not live in this session')
    // Nothing was read, so there is no second block claiming otherwise.
    const blocks = Array.from(container.querySelectorAll('.ui-code-block')).map(
      (b) => b.textContent,
    )
    expect(blocks.filter((b) => b === 'rm -rf $HOME/x $(id -u)')).toHaveLength(1)
  })

  it('states an assignment the command makes to itself', () => {
    const { container } = renderPrompt({
      ask: {
        ...EXPANSION_ASK,
        arguments: '{"command":"HOME=/tmp rm -rf $HOME/x"}',
        expansion: {
          asked: true,
          command: 'HOME=/tmp rm -rf $HOME/x',
          assignments: [{ name: 'HOME', value: '/tmp' }],
          parts: [
            {
              text: '$HOME',
              name: 'HOME',
              kind: 'parameter',
              state: 'unsafe',
              reason:
                'the command sets HOME itself for this command, so the shell’s value is not the one it will be read with',
            },
          ],
        },
      },
    })
    expect(rows(container)).toContainEqual(['HOME', '/tmp'])
    expect(container.textContent).toContain('the command sets HOME itself for this command')
  })

  it('says what a program word actually is, because nocx does not read rc files', () => {
    const { container } = renderPrompt({
      ask: {
        ...EXPANSION_ASK,
        expansion: {
          asked: true,
          command: 'rm -rf /home/dev/x $(id -u)',
          parts: [],
          programs: [{ word: 'rm', kind: 'alias', target: 'rm -i' }],
        },
      },
    })
    expect(rows(container)).toContainEqual([
      'rm',
      'an alias in this shell, not the program of that name',
    ])
  })

  it('says nothing about expansion when the backend sent none', () => {
    const { container } = renderPrompt({
      ask: { ...POLICY_ASK, tool: 'run', arguments: '{"command":"df -h"}' },
    })
    expect(container.querySelectorAll('.ui-code-block')).toHaveLength(1)
    expect(names(container)).not.toContain('$HOME')
  })
})

/**
 * The fourth answer (nocx-4yjwk.1, design §5.3). A call refused because a
 * resource fell outside a row's scopes cannot be settled by any of the three
 * WIDTHS: `once`, `session` and `always` all answer "how long", and none of
 * them moves the bound that excluded the resource — so the same call asks
 * again on the next turn, for ever. `expand` is the answer that does, and it
 * widens and approves as one act.
 *
 * Everything here drives the BUTTON. A test that called `onDecide` directly
 * would pass against a prompt that renders no fourth answer at all, which is
 * the entire defect (AGENTS.md testing rule 1).
 */
describe('AgentApprovalPrompt — the widening answer (nocx-4yjwk.1)', () => {
  afterEach(cleanup)

  /** A resource that fell outside an operator's own row selector, which is
   *  editable — so the backend says the widening may be offered. */
  const WIDENABLE_ASK: AgentApprovalRequested = {
    ...POLICY_ASK,
    arguments: '{"path":"/repo/secrets/b.txt"}',
    resource: { kind: 'path', id: '/repo/secrets/b.txt' },
    outOfScope: {
      cause: 'row-scope',
      resource: { kind: 'path', id: '/repo/secrets/b.txt' },
      widening: { available: true, reason: '' },
    },
  }

  /** The same shape outside an immutable fence: no answer can move it, so
   *  the backend offers nothing and says why instead. */
  const FENCED_ASK: AgentApprovalRequested = {
    ...WIDENABLE_ASK,
    outOfScope: {
      cause: 'fence',
      resource: { kind: 'path', id: '/repo/secrets/b.txt' },
      widening: {
        available: false,
        reason:
          'this path is outside the run fence, which no answer here can move — start a run with wider bounds',
      },
    },
  }

  const widenButton = (container: HTMLElement): HTMLButtonElement | null =>
    (Array.from(container.querySelectorAll('.ui-button')).find((b) =>
      (b.textContent ?? '').startsWith('Allow and widen to'),
    ) as HTMLButtonElement | undefined) ?? null

  it('offers a fourth answer when the backend says the widening is available', () => {
    const { container } = renderPrompt({ ask: WIDENABLE_ASK })
    const widen = widenButton(container)
    expect(widen).not.toBeNull()
    // Enabled from the state a person OPENS the prompt in — an answer that
    // needs another click first is not an answer they can give.
    expect(widen!.disabled).toBe(false)
  })

  it('answers with the direction AND the widening scope, in one act', () => {
    const { decisions, onDecide } = recordDecisions()
    const { container } = renderPrompt({ ask: WIDENABLE_ASK, onDecide })
    fireEvent.click(widenButton(container)!)
    expect(decisions).toEqual([[true, 'expand']])
  })

  it('names the resource that fell outside, in the backend’s own words', () => {
    const { container } = renderPrompt({ ask: WIDENABLE_ASK })
    expect(widenButton(container)!.textContent).toContain('/repo/secrets/b.txt')
    // …and the accessible name says what widening costs, not only what it does.
    expect(widenButton(container)!.getAttribute('aria-label')).toBe(
      'Allow and widen to /repo/secrets/b.txt — read and inspect may then reach it, in every session, from now on',
    )
  })

  /**
   * The offer is READ off the wire and never re-derived from `cause`: the
   * backend applies the answer, so the backend is what says whether it can be
   * given (ADR-0028 decision 4). A prompt that inferred "row-scope means
   * offer" would offer a yes the layer below refuses.
   */
  it('offers nothing when the widening is unavailable, and says why instead', () => {
    const { container } = renderPrompt({ ask: FENCED_ASK })
    expect(widenButton(container)).toBeNull()
    expect(container.textContent).toContain(
      'this path is outside the run fence, which no answer here can move',
    )
  })

  it('does not re-derive the offer from the cause', () => {
    // cause 'row-scope' — the editable one — with the offer withheld. The
    // surface must follow `available`, not the cause it usually accompanies.
    const { container } = renderPrompt({
      ask: {
        ...WIDENABLE_ASK,
        outOfScope: {
          cause: 'row-scope',
          resource: { kind: 'path', id: '/repo/secrets/b.txt' },
          widening: { available: false, reason: 'this row is managed and cannot be widened here' },
        },
      },
    })
    expect(widenButton(container)).toBeNull()
    expect(container.textContent).toContain('this row is managed and cannot be widened here')
  })

  it('leaves an ordinary policy question exactly as it was', () => {
    const { container } = renderPrompt({ ask: STANDING_ASK })
    expect(container.querySelectorAll('.ui-button')).toHaveLength(6)
    expect(widenButton(container)).toBeNull()
    expect(container.textContent).not.toContain('widen')
  })

  it('leaves an egress question exactly as it was', () => {
    const { container } = renderPrompt({ ask: EGRESS_ASK })
    expect(container.querySelectorAll('.ui-button')).toHaveLength(2)
    expect(widenButton(container)).toBeNull()
    expect(container.textContent).not.toContain('widen')
  })

  /**
   * A widening is administrative and reaches further than any of the three
   * widths in the axis it moves. It must never be what a hurried person lands
   * on: Prompt puts the caret on the first enabled button, and that is still
   * `Allow once`.
   */
  it('is not what the prompt focuses on open', () => {
    const { container } = renderPrompt({ ask: WIDENABLE_ASK })
    const widen = widenButton(container)
    // Assert the answer is THERE before asserting where the caret is not:
    // a prompt that renders no fourth answer would satisfy the second half
    // of this test for the wrong reason.
    expect(widen).not.toBeNull()
    expect(document.activeElement).not.toBe(widen)
    expect((document.activeElement as HTMLElement)?.textContent).toContain('Allow once')
  })

  /**
   * A widening that did not stick must not leave the call resumed, so
   * `agent.approve` with scope `expand` can come back an RPC ERROR rather
   * than a warning. The caller's failure path (main.tsx) keeps the question
   * queued and lowers `busy`; what this surface owes is that the question is
   * then ANSWERABLE AGAIN — still open, every answer enabled.
   */
  it('a failed decide leaves the question answerable', async () => {
    const [busy, setBusy] = createSignal(false)
    let rejected: (() => void) | null = null
    const onDecide = () => {
      setBusy(true)
      // What decideApproval does with a refusal: the question is NOT
      // dequeued, the toast reports it, and busy falls again.
      rejected = () => setBusy(false)
    }
    const { container } = render(() => (
      <AgentApprovalPrompt open ask={WIDENABLE_ASK} busy={busy()} onDecide={onDecide} />
    ))
    fireEvent.click(widenButton(container)!)
    expect(
      Array.from(container.querySelectorAll('.ui-button')).every(
        (b) => (b as HTMLButtonElement).disabled,
      ),
    ).toBe(true)

    rejected!()
    await Promise.resolve()

    // Still open, and every answer — the widening included — can be given again.
    expect(container.querySelector('.ui-prompt')).not.toBeNull()
    expect(widenButton(container)).not.toBeNull()
    expect(
      Array.from(container.querySelectorAll('.ui-button')).some(
        (b) => (b as HTMLButtonElement).disabled,
      ),
    ).toBe(false)
  })
})

/**
 * THE RECEIPT (nocx-2019q) — a standing answer says so where it was given.
 *
 * Driven through the BUTTON, not the callback, and through the real pieces
 * between it and the screen: the prompt's own answer goes on a fake wire, the
 * backend's receipt notification comes back on it, and the REAL
 * AgentInputTarget draws into a REAL scrollback block. A test that called the
 * receipt drawer directly would prove the drawer works and nothing about
 * whether clicking Allow always reaches it.
 */
describe('AgentApprovalPrompt — the receipt a standing answer earns', () => {
  afterEach(cleanup)

  const RUN_ID = 7
  const ENTRY_ID = 'answer-1'

  /** What the backend says when the standing half was written. */
  type Saved = {
    runId: string
    entryId: string
    approved: boolean
    scope: string
    rule: string
    effect: string
    ruleId: string
  }

  /** The wire, as far as this exchange is concerned. `approve` is what the
   *  test scripts: whether the answer is accepted, and what receipt (if any)
   *  the backend then sends. */
  class Wire {
    calls: { method: string; params: unknown }[] = []
    private subs = new Map<string, (params: unknown) => void>()
    /** The receipt this exchange's agent.approve produces, or null. */
    receipt: Saved | null = null
    /** The warning agent.approve answers with — the standing half that did
     *  not stick. */
    warning = ''
    forgetRuleAnswers: () => Promise<{ removed: boolean }> = () =>
      Promise.resolve({ removed: true })

    call<T = unknown>(method: string, params: unknown): Promise<T> {
      this.calls.push({ method, params })
      if (method === 'agent.ask') {
        return Promise.resolve({
          runId: RUN_ID,
          entryId: ENTRY_ID,
          state: 'prepared',
          ingestSeq: 1,
          replayed: false,
          model: 'qwen3',
        }) as Promise<T>
      }
      if (method === 'agent.approve') {
        // The backend writes the standing answer and announces it BEFORE it
        // answers the call, exactly as the transport does.
        if (this.receipt) this.emit('agent.standingAnswerSaved', this.receipt)
        return Promise.resolve({ state: 'streaming', warning: this.warning }) as Promise<T>
      }
      if (method === 'policy.forgetRule') {
        return this.forgetRuleAnswers() as Promise<T>
      }
      return Promise.reject(new Error(`unexpected call ${method}`))
    }

    subscribe(method: string, handler: (params: unknown) => void): () => void {
      this.subs.set(method, handler)
      return () => this.subs.delete(method)
    }

    emit(method: string, params: unknown): void {
      this.subs.get(method)?.(params)
    }
  }

  /** A pane with a real scrollback and a real agent target, with one turn
   *  open on it — the state a person is in when a question arrives. */
  async function paneWithAnOpenTurn(wire: Wire, openPermissions?: () => void) {
    const inner = document.createElement('div')
    document.body.appendChild(inner)
    const xtermContainer = document.createElement('div')
    inner.appendChild(xtermContainer)
    const manager = new BlockManager(inner, xtermContainer, {
      snapshotStore: new CommandSnapshotStore(),
    })
    const target = new AgentInputTarget({
      dispatcher: wire as never,
      cancel: vi.fn(() =>
        Promise.resolve({ runId: 0, state: 'cancelled' as const, cancelled: true as const }),
      ),
      sessionId: () => 'session-a',
      cwd: () => '/repo',
      grants: () => [],
      openAnswer: (question, cwd, running) => manager.addAnswerBlock(question, cwd, running),
      onRefusal: vi.fn(),
      openPermissions,
    })
    await target.submit('please read it')
    return { inner, manager, target }
  }

  const receiptLine = (root: HTMLElement) => root.querySelector<HTMLElement>('.ui-block-notice')
  const receiptText = (root: HTMLElement) =>
    receiptLine(root)?.querySelector('.ui-block-notice__text')?.textContent ?? ''
  const receiptActions = (root: HTMLElement) =>
    Array.from(
      receiptLine(root)?.querySelectorAll<HTMLButtonElement>(
        '.ui-block-notice__actions .ui-button',
      ) ?? [],
    )

  /** Click the answer whose label starts with these words. */
  function answer(container: HTMLElement, label: string) {
    const button = Array.from(container.querySelectorAll<HTMLButtonElement>('.ui-button')).find(
      (b) => (b.textContent ?? '').startsWith(label),
    )
    if (!button) throw new Error(`no answer labelled ${label}`)
    fireEvent.click(button)
  }

  /** Render the real prompt with the real decision path behind its buttons. */
  function promptOver(wire: Wire, ask: AgentApprovalRequested) {
    return render(() => (
      <AgentApprovalPrompt
        open
        ask={ask}
        busy={false}
        onDecide={(approved, scope) => {
          void recordApprovalDecision(ask, approved, scope, {
            dispatcher: wire as never,
            onWarning: () => {},
            onError: () => {},
          })
        }}
      />
    ))
  }

  const RUN_ASK: AgentApprovalRequested = { ...STANDING_ASK, runId: String(RUN_ID) }

  const SAVED: Saved = {
    runId: String(RUN_ID),
    entryId: ENTRY_ID,
    approved: true,
    scope: 'always',
    rule: 'df -h',
    effect: 'observe',
    ruleId: 'rule-42',
  }

  /** Criterion 1. */
  it('says what was saved, in the words of the button that saved it', async () => {
    const wire = new Wire()
    wire.receipt = SAVED
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = promptOver(wire, RUN_ASK)

    answer(container, 'Allow always')
    await Promise.resolve()

    expect(receiptText(inner)).toBe(
      standingAnswerReceipt(true, 'always', 'df -h', EFFECT_LABEL[RUN_ASK.effect]),
    )
    // The person's own words: the sentence the button carried, unaltered.
    expect(receiptText(inner)).toContain('Allow always')
    expect(receiptText(inner)).toContain('df -h')
    expect(receiptText(inner)).toContain('in every session, from now on')
    expect(receiptActions(inner).map((b) => b.textContent)).toEqual(['Undo', 'Manage permissions'])
  })

  /** Criterion 2, first half: "once" saves nothing, so it reports nothing. */
  it('draws nothing for an answer that saved nothing', async () => {
    const wire = new Wire()
    wire.receipt = null
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = promptOver(wire, RUN_ASK)

    answer(container, 'Allow once')
    await Promise.resolve()

    expect(wire.calls.some((c) => c.method === 'agent.approve')).toBe(true)
    expect(receiptLine(inner)).toBeNull()
  })

  /** Criterion 2, second half — true for a DIFFERENT reason: an egress
   *  question offers no width at all, so the only answer it can be given
   *  saves nothing anywhere. */
  it('draws nothing for an egress answer, which is never a standing one', async () => {
    const wire = new Wire()
    wire.receipt = null
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const egress: AgentApprovalRequested = { ...EGRESS_ASK, runId: String(RUN_ID) }
    const { container } = promptOver(wire, egress)

    // The surface offers exactly two answers here, both `once`.
    expect(
      Array.from(container.querySelectorAll('.ui-button')).map((b) => b.textContent),
    ).toHaveLength(2)
    answer(container, 'Allow once')
    await Promise.resolve()

    expect(receiptLine(inner)).toBeNull()
  })

  /** Criterion 5: a failed save must not put "Saved" on the screen. */
  it('draws nothing when the save failed, and the warning is what is reported', async () => {
    const wire = new Wire()
    wire.receipt = null
    wire.warning = 'the decision was applied to this call, but could not be saved: disk is full'
    const warned: string[] = []
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = render(() => (
      <AgentApprovalPrompt
        open
        ask={RUN_ASK}
        busy={false}
        onDecide={(approved, scope) => {
          void recordApprovalDecision(RUN_ASK, approved, scope, {
            dispatcher: wire as never,
            onWarning: (sentence) => warned.push(sentence),
            onError: () => {},
          })
        }}
      />
    ))

    answer(container, 'Allow always')
    await Promise.resolve()
    await Promise.resolve()

    expect(receiptLine(inner)).toBeNull()
    expect(warned).toEqual([wire.warning])
  })

  /** Criterion 3: Undo forgets THAT rule, named by its id. */
  it('undoes by id, and only that id', async () => {
    const wire = new Wire()
    wire.receipt = SAVED
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = promptOver(wire, RUN_ASK)
    answer(container, 'Allow always')
    await Promise.resolve()

    fireEvent.click(receiptActions(inner)[0])
    await Promise.resolve()
    await Promise.resolve()

    const forgets = wire.calls.filter((c) => c.method === 'policy.forgetRule')
    expect(forgets).toHaveLength(1)
    expect(forgets[0].params).toEqual({ id: 'rule-42' })
    // Never a whole-document write: a snapshot restore would discard an
    // answer given between the save and the undo.
    expect(wire.calls.some((c) => c.method === 'policy.set')).toBe(false)
    expect(receiptText(inner)).toBe('Undone — that answer is no longer saved.')
    expect(receiptActions(inner).map((b) => b.textContent)).toEqual(['Manage permissions'])
  })

  /** Criterion 4: undoing something already gone is a success, and says so. */
  it('reports what is true when the rule was already gone', async () => {
    const wire = new Wire()
    wire.receipt = SAVED
    wire.forgetRuleAnswers = () => Promise.resolve({ removed: false })
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = promptOver(wire, RUN_ASK)
    answer(container, 'Allow always')
    await Promise.resolve()

    fireEvent.click(receiptActions(inner)[0])
    await Promise.resolve()
    await Promise.resolve()

    expect(receiptText(inner)).toBe('That answer was already gone.')
    expect(receiptText(inner)).not.toContain('could not')
    expect(receiptActions(inner).map((b) => b.textContent)).toEqual(['Manage permissions'])
  })

  it('keeps Undo offered when the undo itself failed', async () => {
    const wire = new Wire()
    wire.receipt = SAVED
    wire.forgetRuleAnswers = () => Promise.reject(new Error('the transport is closed'))
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = promptOver(wire, RUN_ASK)
    answer(container, 'Allow always')
    await Promise.resolve()

    fireEvent.click(receiptActions(inner)[0])
    await Promise.resolve()
    await Promise.resolve()

    expect(receiptText(inner)).toContain('the transport is closed')
    expect(receiptLine(inner)?.dataset.tone).toBe('warning')
    expect(receiptActions(inner).map((b) => b.textContent)).toEqual(['Undo', 'Manage permissions'])
  })

  it('offers no Undo for an answer no id can name', async () => {
    const wire = new Wire()
    wire.receipt = { ...SAVED, scope: 'session', ruleId: '' }
    const { inner } = await paneWithAnOpenTurn(wire, vi.fn())
    const { container } = promptOver(wire, RUN_ASK)

    answer(container, 'Allow in this session')
    await Promise.resolve()

    expect(receiptText(inner)).toContain('until this terminal session ends')
    expect(receiptActions(inner).map((b) => b.textContent)).toEqual(['Manage permissions'])
  })

  it('opens the page that manages standing answers', async () => {
    const wire = new Wire()
    wire.receipt = SAVED
    const openPermissions = vi.fn()
    const { inner } = await paneWithAnOpenTurn(wire, openPermissions)
    const { container } = promptOver(wire, RUN_ASK)
    answer(container, 'Allow always')
    await Promise.resolve()

    fireEvent.click(receiptActions(inner)[1])
    expect(openPermissions).toHaveBeenCalledTimes(1)
  })
})
