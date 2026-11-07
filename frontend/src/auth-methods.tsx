import type { Component } from 'solid-js'
import { AsteriskIcon, KeyIcon, KeyboardIcon, LightbulbIcon, UserIcon } from './ui/icons'
import type { AuthMode } from './profiles'
import { t } from './i18n/locale-model'

export const AUTH_MODES: AuthMode[] = ['', 'password', 'publicKey', 'agent', 'keyboardInteractive']

const icons: Record<AuthMode, Component> = {
  '': LightbulbIcon,
  password: AsteriskIcon,
  publicKey: KeyIcon,
  agent: UserIcon,
  keyboardInteractive: KeyboardIcon,
}

/** One word per mode, resolved at READ: a module-level table of labels was a
 *  second owner of these words (and a snapshot of the language the window
 *  started in) beside this function, which is the one that knows what each
 *  mode is called (nocx-dej6.3.8's review). */
export function authModeLabel(mode: AuthMode): string {
  switch (mode) {
    case '':
      return t('connections.auth.mode.auto', 'Auto')
    case 'password':
      return t('connections.auth.mode.password', 'Password')
    case 'publicKey':
      return t('connections.auth.mode.publicKey', 'Public Key')
    case 'agent':
      return t('connections.auth.mode.agent', 'Agent')
    case 'keyboardInteractive':
      return t('connections.auth.mode.keyboardInteractive', 'Keyboard Interactive')
  }
}

/** The segmented control's options, built per read for the same reason. */
export const authSegments = () =>
  AUTH_MODES.map((mode) => ({
    value: mode,
    label: authModeLabel(mode),
    title: authModeLabel(mode),
    icon: icons[mode],
  }))
