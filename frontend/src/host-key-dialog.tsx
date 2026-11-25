import { createSignal, Show } from 'solid-js'
import { machineLabel, type MachineFacts } from './agent-machine'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { FactList } from './ui/fact-list'
import { SegmentedControl } from './ui/segmented-control'
import { Stack } from './ui/stack'
import { t } from './i18n/locale-model'

interface HostKeyDecisionEvidence {
  host: string
  changed: boolean
  fingerprint: string
  storedFingerprint?: string
}

/** The connect-time ask (ADR-0069): a machine whose connection has not
 *  answered how nocx integrates with it. Only the identity travels here —
 *  the methods offered and what each means is fixed copy in the dialog
 *  below, the same way AgentApprovalDialog states its own grant rather than
 *  taking it as a prop.
 *
 *  profileId is the saved connection the open named, and it is here because
 *  the last sentence says where the answer can be changed later: a saved
 *  connection has settings to point at, and a destination typed into ssh has
 *  none. It is read from the ask's own data rather than inferred from what
 *  the renderer holds, so the sentence cannot disagree with the request it
 *  is answering (ADR-727g63tr decision 1, mockup frame 7). */
interface HelperConsentAskEvidence {
  fingerprint: string
  profileId?: string
}

/** The last sentence of the ask, chosen by the ask's own data: a saved
 *  connection points at its settings; a typed ssh has none, so it names the
 *  one way to get some. Two sentences rather than one composed from a host,
 *  because "the connection's settings" is false for a destination that is
 *  not a connection. */
function changeMethodLater(profileId: string | undefined): string {
  return profileId
    ? t(
        'connections.helperAsk.changeLater',
        'Change your mind later from the connection’s settings.',
      )
    : t(
        'connections.helperAsk.changeLaterTyped',
        'To change it later, save a connection to this machine and open its settings.',
      )
}

/** The methods the connect-time ask offers — built from the modes the
 *  product can actually carry (ADR-0069): never 'auto', which is not an
 *  answer, and never a disabled "coming soon" entry. A mode added later
 *  (the server tier, nocx-xn63t.2.1) adds one entry here. */
export type IntegrationMethod = 'raw' | 'script' | 'helper'

interface HostKeyDialogProps {
  /** null when this ask is about the method alone — the key is already
   *  trusted, so there is nothing here to show or accept about it. */
  evidence: HostKeyDecisionEvidence | null
  /** null when this ask is about the host key alone (the ordinary
   *  probe-time and open-time case, unchanged). Present together with
   *  evidence exactly when the SAME dial discovered both: one dialog then
   *  carries both questions, each with its own action. */
  helperAsk: HelperConsentAskEvidence | null
  busy: boolean
  /** Meaningful only when evidence is non-null. Its own action, independent
   *  of the method choice (owner's decision, 2026-09-16, ADR-0069): trusting
   *  the key answers nothing about the method, and answering the method
   *  needs no trust decision made first. */
  onAcceptHostKey: () => void
  /** Meaningful only when helperAsk is non-null. Fires once the person has
   *  picked one of the three methods and pressed Continue. */
  onChooseMethod: (method: IntegrationMethod) => void
  onClose: () => void
}

/** One consent surface for probe-time and open-time host-key decisions, and
 *  the connect-time integration-method ask (ADR-0069) — which rides the SAME
 *  surface because the two questions can arrive on the very same connect: a
 *  host whose key is already trusted raises no host-key dialog at all, so
 *  hanging the method ask off that dialog alone would leave it unasked
 *  forever on an already-trusted host. */
export function HostKeyDialog(props: HostKeyDialogProps) {
  // No pre-selected method (ADR-0069's own rationale): a choice names what
  // the person is choosing between, and a default picked for them is the
  // answer nobody gave. Continue stays disabled until one is picked.
  const [method, setMethod] = createSignal<IntegrationMethod | ''>('')

  const title = () => {
    if (props.evidence)
      return props.evidence.changed
        ? t('connections.hostKey.changed', 'Host key changed')
        : t('connections.hostKey.unknown', 'Unknown host key')
    return t('connections.hostKey.choose', 'Choose how nocx connects')
  }
  return (
    <Dialog
      open
      closeAria={t('ui.dialog.closeAria', 'Close dialog')}
      closeTitle={t('ui.dialog.closeTitle', 'Close')}
      onClose={props.onClose}
      title={title()}
      footer={
        <>
          {/* Its own action, always offered when there is a key to trust —
              independent of the method choice below (issue (b), ADR-0069):
              the owner reversed the earlier combined dialog's withholding of
              this button whenever a method question rode the same refusal. */}
          <Show when={props.evidence} keyed>
            {(evidence) => (
              <Button
                variant={evidence.changed ? 'danger' : 'primary'}
                disabled={props.busy}
                onClick={props.onAcceptHostKey}
              >
                {props.busy
                  ? 'Trusting…'
                  : evidence.changed
                    ? 'Trust the new key'
                    : 'Trust host key'}
              </Button>
            )}
          </Show>
          <Show when={props.helperAsk}>
            <Button
              variant="primary"
              disabled={props.busy || method() === ''}
              onClick={() => {
                const m = method()
                if (m !== '') props.onChooseMethod(m)
              }}
            >
              {props.busy ? 'Saving…' : 'Continue'}
            </Button>
          </Show>
          <Button variant="default" disabled={props.busy} onClick={props.onClose} autofocus>
            {t('hostKey.cancel', 'Cancel')}
          </Button>
        </>
      }
    >
      <Stack>
        <Show when={props.evidence} keyed>
          {(evidence) => (
            <>
              <Show
                when={evidence.changed}
                fallback={
                  <p>
                    {t(
                      'hostKey.firstTime.body',
                      'This is the first time nocx has met this host. If you trust this machine, accept its key — it will be saved to ~/.ssh/known_hosts.',
                    )}
                  </p>
                }
              >
                <p>
                  {t(
                    'hostKey.changed.body',
                    'The host key offered by this server differs from the one nocx has seen before. That can mean the host’s key was regenerated — or that someone is intercepting this connection. Do not continue unless you are sure this is the same machine. Accepting replaces the old key: it stops being trusted.',
                  )}
                </p>
              </Show>
              <p>
                {evidence.changed ? 'Stored fingerprint' : 'Offered fingerprint'}:{' '}
                <code>{evidence.changed ? evidence.storedFingerprint : evidence.fingerprint}</code>
              </p>
              <Show when={evidence.changed}>
                <p>
                  {t('hostKey.fingerprint.offered', 'Offered fingerprint')}: <code>{evidence.fingerprint}</code>
                </p>
              </Show>
            </>
          )}
        </Show>
        <Show when={props.helperAsk} keyed>
          {(ask) => (
            <>
              <p>
                {t(
                  'connections.helperAsk.lead',
                  'Choose how nocx integrates with this machine. The method is saved for the machine — every tab to it, from any saved connection or a typed ssh, opens this way — and permission to deploy nocx’s helper here is remembered with it, so this is asked once.',
                )}
              </p>
              <SegmentedControl
                ariaLabel={t('hostKey.method.aria', 'Integration method')}
                options={[
                  {
                    value: 'raw',
                    label: t('hostKey.method.raw.label', 'Raw'),
                    title: t(
                      'hostKey.method.raw.title',
                      'No integration — a plain terminal, nothing written to the host.',
                    ),
                  },
                  {
                    value: 'script',
                    label: t('hostKey.method.script.label', 'Script'),
                    title: t(
                      'hostKey.method.script.title',
                      'The shell scripts nocx ships — command blocks, completions, no compiled binary.',
                    ),
                  },
                  {
                    value: 'helper',
                    label: t('hostKey.method.helper.label', 'Helper'),
                    title: t(
                      'hostKey.method.helper.title',
                      'Deploy the nocx helper — the Git panel and everything that needs more than a shell.',
                    ),
                  },
                ]}
                value={method()}
                onChange={(v) => setMethod(v as IntegrationMethod)}
                disabled={props.busy}
              />
              <p>
                {t(
                  'connections.helperAsk.consent',
                  'Choosing Helper also allows nocx to deploy its helper binary on this machine; Raw and Script never do.',
                )}{' '}
                {changeMethodLater(ask.profileId)}
              </p>
              <p>
                {t('hostKey.fingerprint.label', 'Fingerprint')}: <code>{ask.fingerprint}</code>
              </p>
            </>
          )}
        </Show>
      </Stack>
    </Dialog>
  )
}

interface AgentApprovalDialogProps {
  executable: string
  digest: string
  workspace: string
  // The machine the answer would be given for. The wire always sends one
  // (internal/transport sets it for every approval ask); it is optional here
  // only because the capability's params are shared, and a surface must say
  // what it cannot name rather than call an unknown machine local.
  machine?: MachineFacts
  busy: boolean
  onDecide: (approved: boolean) => void
}

/**
 * The consent surface that admits an agent's process tree to nocx's tools.
 *
 * IT SAYS WHAT A YES ALLOWS (nocx-fu18z). It used to state six facts and
 * answer none of the questions a person actually has. "the tool endpoint" is
 * vocabulary nobody outside this repository has met; the scope arrived as the
 * durable key it is part of ("tool-endpoint:workspace:default"); the digest
 * sat there with nothing to check it against and no reason given; and what
 * would BECOME POSSIBLE, which is the whole of what consent is for, appeared
 * in no word of it. So the lead is one sentence naming the three powers the
 * caller's grant actually carries — Delegate, Observe and MutateDestructive
 * over its own session and environment (internal/app/worker_auth.go
 * callerGrant) — and the rows say how far the answer reaches and how long it
 * lasts.
 *
 * D14's words are kept verbatim in the lead: the approval must read "allow
 * this agent and commands it launches", and it is the sentence that tells a
 * person the grant does not stop at the process they typed.
 *
 * IT ALSO SAYS WHAT A NO COSTS. Without that line Deny reads as the button
 * that breaks something: the agent still runs, it simply runs without nocx's
 * tools, and the pane says so.
 *
 * AND IT NAMES THE MACHINE THE ANSWER IS FOR (nocx-50w7p.16). A person's yes
 * admits an agent ON ONE MACHINE: the same executable on a host, or as another
 * account on one, is asked about again. A dialog that did not say where would
 * be collecting a decision about a place nobody named.
 *
 * AND IT SAYS WHERE THE ANSWER CAN BE UNDONE. It used to admit that no surface
 * could withdraw it, which was true when it was written and stopped being true
 * when the Settings page landed (Settings → Agent access, internal/transport's
 * agentAccess.forget, nocx-6jbad): a person deciding is entitled to know the
 * decision is revisitable, and a dialog that said otherwise would send them to
 * edit agent-approvals.json by hand for no reason.
 */
export function AgentApprovalDialog(props: AgentApprovalDialogProps) {
  return (
    <Dialog
      open
      closeAria={t('ui.dialog.closeAria', 'Close dialog')}
      closeTitle={t('ui.dialog.closeTitle', 'Close')}
      onClose={() => props.onDecide(false)}
      title={t('hostKey.agent.title', "Allow this agent to use nocx's tools?")}
      footer={
        <>
          <Button variant="primary" disabled={props.busy} onClick={() => props.onDecide(true)}>
            {props.busy ? 'Saving…' : 'Allow'}
          </Button>
          <Button
            variant="default"
            disabled={props.busy}
            onClick={() => props.onDecide(false)}
            autofocus
          >
            {t('hostKey.agent.deny', 'Deny')}
          </Button>
        </>
      }
    >
      <Stack>
        <p>
          {t('hostKey.agent.lead', 'If you allow it, this agent')}{' '}
          <strong>{t('hostKey.agent.launches', 'and commands it launches')}</strong>{' '}
          {t(
            'hostKey.agent.powers',
            'can start other agents in new tabs, watch what they are doing, and stop them. They cannot read your secrets, reach another machine, or change anything else on this one.',
          )}
        </p>
        <FactList
          ariaLabel={t('hostKey.agent.allowed.aria', 'What is being allowed')}
          facts={[
            { name: t('hostKey.fact.agent', 'Agent'), value: props.executable },
            {
              name: t('hostKey.fact.machine', 'Machine'),
              value: props.machine ? machineLabel(props.machine) : t('hostKey.fact.noMachine', 'a machine nocx could not name'),
              // THREE cases and not two: an ask with no machine is not an ask
              // about this machine. A note that fell through to the local
              // wording would describe a place the wire never named, which is
              // how a surface promises something nobody asked for.
              note: !props.machine
                ? 'nocx could not name the machine this question is about, so nothing here says where the agent would run.'
                : props.machine.kind === 'ssh'
                  ? 'The agent runs on that host, as that account. Your answer is remembered for that machine alone, so the same agent on another host — or as another account — is asked about again.'
                  : 'The agent runs on this machine. Your answer is remembered for this machine alone.',
            },
            {
              name: t('hostKey.fact.fingerprint', 'Fingerprint'),
              value: props.digest,
              note: 'The answer is remembered against these bytes, so a replaced program is asked about again.',
            },
            {
              name: t('hostKey.fact.appliesTo', 'Applies to'),
              value: `Every tab in the ${props.workspace} workspace`,
              note: 'Not only this tab: the same agent started anywhere in this workspace is allowed without asking again.',
            },
            {
              name: t('hostKey.fact.lasts', 'Lasts'),
              value: 'Until you undo it in Settings → Agent access',
              note: 'The answer survives a restart. Undoing it there means the next start of this agent asks again.',
            },
          ]}
        />
        <p>
          {t(
            'hostKey.agent.denyBody',
            "If you deny, the agent still runs — without nocx's tools, and its pane says so. The answer is remembered for this agent alone.",
          )}
        </p>
      </Stack>
    </Dialog>
  )
}
