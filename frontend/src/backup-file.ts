export const MAX_BACKUP_BYTES = 8 * 1024 * 1024 // 8 MiB

export async function readBackupText(file: File): Promise<string> {
  if (file.size > MAX_BACKUP_BYTES) {
    throw new Error(`File exceeds ${MAX_BACKUP_BYTES} bytes limit`)
  }
  return file.text()
}

export function downloadText(
  fileName: string,
  contents: string,
  mimeType = 'application/json',
): void {
  const blob = new Blob([contents], { type: mimeType })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName
  a.click()
  URL.revokeObjectURL(url)
}
