// @vitest-environment jsdom
/**
 * AgentApprovalDialog tests — the consent surface that admits an executable
 * process tree to the tool endpoint (nocx-rowqt.12).
 *
 * What a user can do: read WHICH executable and WHICH scope they are being
 * asked about, then allow or deny. The facts are the whole point of the
 * dialog — a person who cannot read the digest cannot give the consent this
 * surface claims to collect — so they are asserted as the kit's fact rows
 * rather than as text somewhere on the page. The row is what carries
 * `overflow-wrap: anywhere`; the two hand-rolled paragraphs it replaced
 * carried nothing, and a 64-character digest left the dialog through a
 * horizontal scrollbar with its closing bracket (nocx-39x9n).
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import type { MachineFacts } from './agent-machine'
import { AgentApprovalDialog, HostKeyDialog } from './host-key-dialog'

// The shape the backend actually sends: agent_approval.go composes the path
// and the digest into one string, so the value the dialog receives is long,
// unbroken and the reason the wrapping matters.
const EXECUTABLE = '/run/current-system/sw/bin/claude'
const DIGEST = '55640c4f3b8769e625c91e6aeaac3032c713a8bd0b83e04c9265772d7cb40825'
const WORKSPACE = 'default'

// The two machines the backend derives: the coordinator's own, and one reached
// over ssh as an account whose host key it accepted.
const LOCAL_MACHINE: MachineFacts = { kind: 'local' }
const SSH_MACHINE: MachineFacts = {
  kind: 'ssh',
  host: 'build.example.com',
  account: 'deploy',
  hostKey: 'SHA256:key-a',
}

/**
 * Open the dialog as one ask carries it.
 *
 * `machine: null` is "the wire sent none" and is its OWN value rather than an
 * omitted argument: the backend sets a machine on every approval ask, so a
 * build that does not is a case the surface must state, and a default
 * PARAMETER cannot express it — JavaScript substitutes the default for an
 * explicit `undefined`, which is how this test first passed the wrong ask.
 */
function open({
  busy = false,
  machine = LOCAL_MACHINE,
}: { busy?: boolean; machine?: MachineFacts | null } = {}) {
  const onDecide = vi.fn()
  const view = render(() => (
    <AgentApprovalDialog
      executable={EXECUTABLE}
      digest={DIGEST}
      workspace={WORKSPACE}
      machine={machine === null ? undefined : machine}
      busy={busy}
      onDecide={onDecide}
    />
  ))
  return { view, onDecide }
}

/** The facts as a person reads them: name → value, paired rather than
 *  positional, because what matters is which value sits under which name. */
function facts(container: HTMLElement): Record<string, string> {
  const names = Array.from(
    container.querySelectorAll('.ui-fact-list__name'),
    (el) => el.textContent ?? '',
  )
  const values = Array.from(
    container.querySelectorAll('.ui-fact-list__value'),
    (el) => el.textContent ?? '',
  )
  return Object.fromEntries(names.map((name, i) => [name, values[i] ?? '']))
}

describe('AgentApprovalDialog', () => {
  afterEach(cleanup)

  it('names the agent and its fingerprint as fact rows a long value can wrap in', () => {
    const { view } = open()
    // toContain, not toBe, for the fingerprint: a row's value element carries
    // the note beside the value, which is the point of the note — it cannot
    // drift from what it qualifies.
    const rows = facts(view.container)
    expect(rows['Agent']).toBe(EXECUTABLE)
    expect(rows['Fingerprint']).toContain(DIGEST)
    const names = Array.from(
      view.container.querySelectorAll('.ui-fact-list__name'),
      (el) => el.textContent,
    )
    expect(names).toEqual(['Agent', 'Machine', 'Fingerprint', 'Applies to', 'Lasts'])
  })

  // The four questions a person has, and the dialog used to answer none of
  // them (nocx-fu18z). Asserted as text a person can read rather than as the
  // presence of a row, because the row was never the missing part.
  it('says what a yes allows, how far it reaches, how long it lasts and what a no costs', () => {
    const { view } = open()
    const text = view.container.textContent ?? ''
    expect(text).toContain('start other agents in new tabs')
    expect(text).toContain(`Every tab in the ${WORKSPACE} workspace`)
    expect(text).toContain('the agent still runs')
  })

  // The dialog said the answer could not be withdrawn, which stopped being
  // true when the Settings page landed (nocx-6jbad). A person deciding is
  // entitled to know where to change their mind, and a surface that denies the
  // way back sends them to edit JSON by hand for no reason.
  it('says where the answer can be undone, and never claims it cannot be', () => {
    const { view } = open()
    const text = view.container.textContent ?? ''
    expect(text).toContain('Settings → Agent access')
    expect(text).not.toContain('no way to undo')
    expect(facts(view.container)['Lasts']).toContain('Settings → Agent access')
  })

  // THE MACHINE (nocx-50w7p.16). A yes admits an agent on ONE machine, so the
  // dialog has to say which — a decision about a place nobody named is not a
  // decision a person can make.
  it('names the machine the answer would be given for', () => {
    const local = open()
    expect(facts(local.view.container)['Machine']).toContain('this machine')

    const ssh = open({ machine: SSH_MACHINE })
    const sshMachine = facts(ssh.view.container)['Machine'] ?? ''
    expect(sshMachine).toContain('deploy@build.example.com')
    // And it says the answer reaches that machine alone, which is what the
    // person is trading away by answering yes.
    expect(sshMachine).toContain('another host')
  })

  // An ask whose machine did not arrive is SAID to be unnamed rather than
  // called local: the two are different facts, and guessing the comfortable
  // one is how a surface promises something the wire did not carry.
  it('says a machine it cannot name rather than calling it local', () => {
    const { view } = open({ machine: null })
    const machine = facts(view.container)['Machine'] ?? ''
    expect(machine).toContain('could not name')
    expect(machine).not.toContain('this machine')
  })

  // Vocabulary nobody outside this repository has met. It named the internal
  // surface and never what the surface lets an agent do.
  it('never calls it "the tool endpoint"', () => {
    const { view } = open()
    expect(view.container.textContent ?? '').not.toContain('tool endpoint')
  })

  // D14's wording, asserted because it is the part that tells a person the
  // grant reaches past the process they typed.
  it('says the grant covers the commands the agent launches', () => {
    const { view } = open()
    expect(view.container.textContent).toContain('and commands it launches')
  })

  it('reports allow and deny as the answer the person gave', () => {
    const { view, onDecide } = open()
    fireEvent.click(view.getByText('Allow'))
    expect(onDecide).toHaveBeenCalledWith(true)
    fireEvent.click(view.getByText('Deny'))
    expect(onDecide).toHaveBeenCalledWith(false)
  })

  // A decision in flight must not be given twice: the store write is what
  // takes the time, and a second click would ask a second question.
  it('refuses a second answer while the first is being recorded', () => {
    const { view, onDecide } = open({ busy: true })
    fireEvent.click(view.getByText('Saving…'))
    fireEvent.click(view.getByText('Deny'))
    expect(onDecide).not.toHaveBeenCalled()
  })
})

/**
 * HostKeyDialog tests — the one consent surface for probe-time and open-time
 * host-key decisions AND the connect-time helper ask (ADR-0068).
 *
 * What a user can do, in each of the three shapes the wire can send: an
 * ordinary host-key ask (evidence only — unchanged by this bead), a
 * helper-only ask (a machine whose key is already trusted), and the
 * combined ask a single unknown-or-changed-key auto connect raises. Each
 * button is asserted from the state the button starts enabled in: present,
 * enabled, and its click reaching the callback the caller acts on.
 */
const HOST_KEY_UNKNOWN = {
  host: 'db.example.com:22',
  changed: false,
  fingerprint: 'SHA256:offered',
}
const HOST_KEY_CHANGED = {
  host: 'db.example.com:22',
  changed: true,
  fingerprint: 'SHA256:new',
  storedFingerprint: 'SHA256:old',
}

function openHostKeyDialog(props: {
  evidence?: typeof HOST_KEY_UNKNOWN | null
  helperAsk?: { fingerprint: string } | null
  busy?: boolean
}) {
  const onAcceptHostKey = vi.fn()
  const onDecideHelper = vi.fn()
  const onClose = vi.fn()
  const view = render(() => (
    <HostKeyDialog
      evidence={props.evidence ?? null}
      helperAsk={props.helperAsk ?? null}
      busy={props.busy ?? false}
      onAcceptHostKey={onAcceptHostKey}
      onDecideHelper={onDecideHelper}
      onClose={onClose}
    />
  ))
  return { view, onAcceptHostKey, onDecideHelper, onClose }
}

describe('HostKeyDialog — host-key-only ask (unchanged by ADR-0068)', () => {
  afterEach(cleanup)

  it('offers Trust host key and reaches onAcceptHostKey, not the helper callback', () => {
    const { view, onAcceptHostKey, onDecideHelper } = openHostKeyDialog({
      evidence: HOST_KEY_UNKNOWN,
    })
    expect(view.queryByText('Use the helper')).toBeNull()
    fireEvent.click(view.getByText('Trust host key'))
    expect(onAcceptHostKey).toHaveBeenCalledTimes(1)
    expect(onDecideHelper).not.toHaveBeenCalled()
  })

  it('names a changed key as danger, distinctly from an unknown one', () => {
    const { view } = openHostKeyDialog({ evidence: HOST_KEY_CHANGED })
    expect(view.getByText('Trust the new key')).toBeTruthy()
    expect(view.container.textContent).toContain('SHA256:old')
    expect(view.container.textContent).toContain('SHA256:new')
  })

  it('cancelling reaches onClose without recording anything', () => {
    const { view, onAcceptHostKey, onClose } = openHostKeyDialog({ evidence: HOST_KEY_UNKNOWN })
    fireEvent.click(view.getByText('Cancel'))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onAcceptHostKey).not.toHaveBeenCalled()
  })
})

describe('HostKeyDialog — connect-time helper ask (ADR-0068)', () => {
  afterEach(cleanup)

  it('on an already-trusted machine asks about the helper alone, with no host-key action', () => {
    const { view, onDecideHelper } = openHostKeyDialog({
      helperAsk: { fingerprint: 'SHA256:trusted' },
    })
    expect(view.queryByText('Trust host key')).toBeNull()
    expect(view.queryByText('Trust the new key')).toBeNull()
    expect(view.getByText('Use the helper')).toBeTruthy()
    expect(view.getByText('Not now')).toBeTruthy()
    expect(view.container.textContent).toContain('SHA256:trusted')

    fireEvent.click(view.getByText('Use the helper'))
    expect(onDecideHelper).toHaveBeenCalledWith(true)
  })

  it('declining reaches onDecideHelper(false) — a recorded answer, not a cancel', () => {
    const { view, onDecideHelper, onClose } = openHostKeyDialog({
      helperAsk: { fingerprint: 'SHA256:trusted' },
    })
    fireEvent.click(view.getByText('Not now'))
    expect(onDecideHelper).toHaveBeenCalledWith(false)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('when the key is ALSO unknown, one dialog shows both the host-key evidence and the helper question, with no standalone host-key action', () => {
    const { view, onAcceptHostKey, onDecideHelper } = openHostKeyDialog({
      evidence: HOST_KEY_UNKNOWN,
      helperAsk: { fingerprint: HOST_KEY_UNKNOWN.fingerprint },
    })
    // The host-key evidence is shown (so the person can judge it)...
    expect(view.container.textContent).toContain(HOST_KEY_UNKNOWN.fingerprint)
    // ...but its own accept button is not offered: trusting the key is a
    // precondition of either helper answer below, never a third click.
    expect(view.queryByText('Trust host key')).toBeNull()
    expect(onAcceptHostKey).not.toHaveBeenCalled()

    fireEvent.click(view.getByText('Use the helper'))
    expect(onDecideHelper).toHaveBeenCalledWith(true)
  })

  it('busy disables every action, including the helper buttons', () => {
    const { view, onDecideHelper } = openHostKeyDialog({
      helperAsk: { fingerprint: 'SHA256:trusted' },
      busy: true,
    })
    fireEvent.click(view.getByText('Saving…'))
    expect(onDecideHelper).not.toHaveBeenCalled()
    fireEvent.click(view.getByText('Not now'))
    expect(onDecideHelper).not.toHaveBeenCalled()
  })
})
