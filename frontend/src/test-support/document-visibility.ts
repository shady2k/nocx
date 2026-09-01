/** Override jsdom's document visibility and return the exact descriptor restore. */
export function setDocumentHidden(hidden: boolean): () => void {
  const previous = Object.getOwnPropertyDescriptor(document, 'hidden')
  Object.defineProperty(document, 'hidden', { configurable: true, value: hidden })
  return () => {
    if (previous) Object.defineProperty(document, 'hidden', previous)
    else Reflect.deleteProperty(document, 'hidden')
  }
}
