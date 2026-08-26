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
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { AgentApprovalPrompt } from './agent-approval-prompt'
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

  it('says what the call can do, in the effect vocabulary and never from the tool name', () => {
    const { container } = renderPrompt({ ask: RUN_ASK, sessionWhere: () => LOCAL })
    expect(rows(container).find(([name]) => name === 'effect')).toEqual([
      'effect',
      'make changes that cannot be undone',
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
    const { container } = renderPrompt({ ask: SESSION_ASK, sessionWhere: () => HERE })
    const text = container.textContent ?? ''
    const facts = text.indexOf('effect')
    const covers = text.indexOf('Approving covers this call')
    const lasts = text.indexOf('An answer in this session lasts')
    expect(facts).toBeGreaterThan(-1)
    expect(covers).toBeGreaterThan(facts)
    expect(lasts).toBeGreaterThan(covers)
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
    const ui = renderPrompt({ onDecide })

    for (const name of [
      'Allow once — this proposal only',
      'Allow in this session — every read and inspect call in this session',
      'Allow always — every read and inspect call, in every session, from now on',
      'Deny once — this proposal only',
      'Deny in this session — every read and inspect call in this session',
      'Deny always — every read and inspect call, in every session, from now on',
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
  it('each answer says its own scope, and carries no second line', () => {
    // The owner's correction, 2026-08-26: the scope belongs IN the button's
    // text. What came off is the per-button description line, not the word a
    // person needs in order to tell the six answers apart — a column heading
    // is one more thing to look up while deciding.
    const { container } = renderPrompt()
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('.ui-button'))
    expect(buttons.map((button) => button.textContent)).toEqual([
      'Allow once',
      'Allow in this session',
      'Allow always',
      'Deny once',
      'Deny in this session',
      'Deny always',
    ])
    expect(buttons.every((button) => !button.querySelector('.ui-button__secondary'))).toBe(true)
    expect(buttons[0]?.getAttribute('aria-label')).toBe('Allow once — this proposal only')
    expect(buttons[2]?.getAttribute('aria-label')).toBe(
      'Allow always — every read and inspect call, in every session, from now on',
    )
  })

  it('groups allowances and refusals into two rows of three', () => {
    const { container } = renderPrompt()
    const groups = Array.from(container.querySelectorAll('.ui-action-group'))
    expect(groups).toHaveLength(2)
    const names = groups.map((g) =>
      Array.from(g.querySelectorAll('.ui-button')).map((b) => b.textContent),
    )
    expect(names).toEqual([
      ['Allow once', 'Allow in this session', 'Allow always'],
      ['Deny once', 'Deny in this session', 'Deny always'],
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
    const { container } = renderPrompt()
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
    const { container } = renderPrompt({ busy: true })
    const buttons = Array.from(container.querySelectorAll('.ui-button'))
    expect(buttons).toHaveLength(6)
    for (const b of buttons) expect((b as HTMLButtonElement).disabled).toBe(true)
  })

  it('states each answer coverage by the control name, using the effect vocabulary', () => {
    const effect = 'mutate-destructive'
    const view = renderPrompt({
      ask: { ...POLICY_ASK, effect },
    })
    const coverage = [
      `Allow once — this proposal only`,
      `Allow in this session — every ${EFFECT_LABEL[effect]} call in this session`,
      `Allow always — every ${EFFECT_LABEL[effect]} call, in every session, from now on`,
      `Deny once — this proposal only`,
      `Deny in this session — every ${EFFECT_LABEL[effect]} call in this session`,
      `Deny always — every ${EFFECT_LABEL[effect]} call, in every session, from now on`,
    ]
    for (const name of coverage) expect(view.getByRole('button', { name })).toBeTruthy()

    const egress = renderPrompt({ ask: { ...EGRESS_ASK, effect } })
    expect(egress.queryByRole('button', { name: /every .* call in this session/ })).toBeNull()
    expect(
      egress.queryByRole('button', { name: /every .* call, in every session, from now on/ }),
    ).toBeNull()
    expect(view.queryByRole('button', { name: 'Allow once' })).toBeNull()
    expect(view.container.textContent).toContain(EFFECT_LABEL[effect])
  })
})
