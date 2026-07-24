// Native-mode escape (ADR-0004 §1, nocx-4ff.9). A tab latched into native mode
// never shows the editor again this session, no matter what markers arrive —
// the state-independent guarantee that the user is never trapped.
export const NATIVE_RESTORE = '__nocx_native_mode\r'

export function shouldShowEditor(owned: boolean, nativeMode: boolean): boolean {
  return owned && !nativeMode
}
