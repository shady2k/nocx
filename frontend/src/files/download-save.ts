import type { FilesDownloadResult } from '../generated/files.download'

export type DownloadSaveOutcome =
  'handed-off' | 'saved' | 'cancelled' | 'source-failed' | 'destination-failed'

export interface PreparedDownloadSave {
  destination: 'browser' | 'native'
  save(result: FilesDownloadResult, url: string | null): Promise<DownloadSaveOutcome>
  cancel(): void
  dispose(): void
}

export interface DownloadSaver {
  prepare(suggestedName: string): Promise<PreparedDownloadSave | null>
}

/** Browser downloads stay owned by the browser and stream directly to disk. */
export function createBrowserDownloadSaver(doc: Document = document): DownloadSaver {
  return {
    prepare(): Promise<PreparedDownloadSave> {
      return Promise.resolve({
        destination: 'browser',
        save(_result, url): Promise<DownloadSaveOutcome> {
          if (url === null) return Promise.resolve('destination-failed')
          const a = doc.createElement('a')
          a.href = url
          a.download = ''
          a.rel = 'noopener'
          a.click()
          return Promise.resolve('handed-off')
        },
        cancel() {},
        dispose() {},
      })
    },
  }
}

export interface NativeDownloadBindings {
  prepare(name: string): Promise<string>
  save(handle: string, ticket: string, size: number): Promise<{ outcome: string }>
  discard(handle: string): Promise<void>
}

/** Native adapter keeps only an opaque one-shot handle in the renderer. */
export function createNativeDownloadSaver(bindings: NativeDownloadBindings): DownloadSaver {
  return {
    async prepare(suggestedName): Promise<PreparedDownloadSave | null> {
      const handle = await bindings.prepare(suggestedName)
      if (!handle) return null
      let disposed = false
      let consumed = false
      const discard = (): void => {
        if (disposed) return
        disposed = true
        try {
          void bindings.discard(handle).catch(() => {})
        } catch {
          // A stale opaque handle is already unusable; cleanup is best-effort.
        }
      }
      return {
        destination: 'native',
        async save(result): Promise<DownloadSaveOutcome> {
          if (disposed || consumed) return 'destination-failed'
          consumed = true
          try {
            const outcome = (await bindings.save(handle, result.ticket, result.size)).outcome
            if (
              outcome === 'saved' ||
              outcome === 'cancelled' ||
              outcome === 'source-failed' ||
              outcome === 'destination-failed'
            ) {
              return outcome
            }
            return 'destination-failed'
          } finally {
            discard()
          }
        },
        cancel: discard,
        dispose: discard,
      }
    },
  }
}
