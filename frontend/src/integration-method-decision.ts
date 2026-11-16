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
      note = `The method applies to this connection, but the host's record was not saved, so this host will be asked again after a restart`
    }
  } catch {
    showToast({
      level: 'danger',
      message: `Could not apply the integration method for ${request.evidence.host}. The choice was not saved.`,
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
