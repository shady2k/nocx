/** The app owns this chord before a focused terminal can emit it. */
export function mountSettingsShortcut(openSettings: () => void): () => void {
  const onKeydown = (event: KeyboardEvent): void => {
    if ((event.ctrlKey || event.metaKey) && !event.shiftKey && !event.altKey && event.key === ',') {
      event.preventDefault()
      event.stopPropagation()
      openSettings()
    }
  }
  document.addEventListener('keydown', onKeydown, true)
  return () => document.removeEventListener('keydown', onKeydown, true)
}
