import { RpcError } from './dispatcher'

/** Whether this RPC failure happened after the chosen method was applied. */
export function integrationMethodAnswerApplied(error: unknown): boolean {
  if (!(error instanceof RpcError) || typeof error.data !== 'object' || error.data === null) {
    return false
  }
  return 'answerApplied' in error.data && error.data.answerApplied === true
}
