import { t } from './i18n/locale-model'
import type { HelperConsentAskRequest, HelperConsentAskQueue } from './host-key-controller'
import type { IntegrationMethod } from './host-key-dialog'
import type { ProfileClient } from './profiles'
import type { showToast as toast } from './ui/toast'

interface IntegrationMethodDecisionDependencies {
  profileClient: Pick<ProfileClient, 'setIntegrationMethod'>
  helperConsentAsks: HelperConsentAskQueue
  setHelperConsentAskBusy: (busy: boolean) => void
  showToast: typeof toast
}

/**
 * The connect-time ask's answer (ADR-0069): the chosen method is recorded for
 * the HOST under the fingerprint the ask named (ADR-727g63tr decision 1),
 * plus the machine's helper grant when the method is helper. THE ANSWER
 * STANDS WHETHER OR NOT IT COULD BE SAVED, because the open that asked goes
 * on with it — a write the backend could not persist comes back as a result
 * with hostRecordSaved false, and the pane head says the host will be asked
 * again after a restart. Any other failure never applied the answer: the ask
 * stays open for another try and a toast reports it without backend text.
 */
export async function decideIntegrationMethod(
  request: HelperConsentAskRequest,
  method: IntegrationMethod,
  {
    profileClient,
    helperConsentAsks,
    setHelperConsentAskBusy,
    showToast,
  }: IntegrationMethodDecisionDependencies,
) {
  setHelperConsentAskBusy(true)
  let note: string | undefined
  try {
    const result = await profileClient.setIntegrationMethod(
      request.evidence.fingerprint,
      method,
      request.evidence.host,
    )
    if (result.hostRecordSaved === false) {
      note = t(
        'connections.helperAsk.notSaved',
        'The method is in force for this host, but the host’s record was not saved, so nocx will ask again after a restart.',
      )
    }
  } catch {
    showToast({
      level: 'danger',
      message: t(
        'connections.helperAsk.applyFailed',
        'Could not apply the integration method for {host}. The choice was not saved.',
        { host: request.evidence.host },
      ),
      duration: 0,
    })
    return
  } finally {
    setHelperConsentAskBusy(false)
  }
  const answer = { method, note }
  helperConsentAsks.settleMatchingQueued(request, answer)
  helperConsentAsks.settle(request, answer)
}
