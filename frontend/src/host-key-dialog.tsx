import { Show } from 'solid-js'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Stack } from './ui/stack'

interface HostKeyDecisionEvidence {
  host: string
  changed: boolean
  fingerprint: string
  storedFingerprint?: string
}

interface HostKeyDialogProps {
  evidence: HostKeyDecisionEvidence
  busy: boolean
  onAccept: () => void
  onClose: () => void
}

/** One consent surface for probe-time and open-time host-key decisions. */
export function HostKeyDialog(props: HostKeyDialogProps) {
  return (
    <Dialog
      open
      onClose={props.onClose}
      title={props.evidence.changed ? 'Host key changed' : 'Unknown host key'}
      footer={
        <>
          <Button
            variant={props.evidence.changed ? 'danger' : 'primary'}
            disabled={props.busy}
            onClick={props.onAccept}
          >
            {props.busy
              ? 'Trusting…'
              : props.evidence.changed
                ? 'Trust the new key'
                : 'Trust host key'}
          </Button>
          <Button variant="default" disabled={props.busy} onClick={props.onClose} autofocus>
            Cancel
          </Button>
        </>
      }
    >
      <Stack>
        <Show
          when={props.evidence.changed}
          fallback={
            <p>
              This is the first time nocx has met this host. If you trust this machine, accept its
              key — it will be saved to ~/.ssh/known_hosts.
            </p>
          }
        >
          <p>
            The host key offered by this server differs from the one nocx has seen before. That can
            mean the host&rsquo;s key was regenerated — or that someone is intercepting this
            connection. Do not continue unless you are sure this is the same machine. Accepting
            replaces the old key: it stops being trusted.
          </p>
        </Show>
        <p>
          {props.evidence.changed ? 'Stored fingerprint' : 'Offered fingerprint'}:{' '}
          <code>
            {props.evidence.changed ? props.evidence.storedFingerprint : props.evidence.fingerprint}
          </code>
        </p>
        <Show when={props.evidence.changed}>
          <p>
            Offered fingerprint: <code>{props.evidence.fingerprint}</code>
          </p>
        </Show>
      </Stack>
    </Dialog>
  )
}
