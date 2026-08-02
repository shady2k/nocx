import type { Component } from 'solid-js'
import { AsteriskIcon, KeyIcon, KeyboardIcon, LightbulbIcon, UserIcon } from './ui/icons'
import type { AuthMode } from './profiles'

export const AUTH_MODES: AuthMode[] = ['', 'password', 'publicKey', 'agent', 'keyboardInteractive']

const labels: Record<AuthMode, string> = {
  '': 'Auto',
  password: 'Password',
  publicKey: 'Public Key',
  agent: 'Agent',
  keyboardInteractive: 'Interactive',
}

const icons: Record<AuthMode, Component> = {
  '': LightbulbIcon,
  password: AsteriskIcon,
  publicKey: KeyIcon,
  agent: UserIcon,
  keyboardInteractive: KeyboardIcon,
}

export function authModeLabel(mode: AuthMode): string {
  switch (mode) {
    case '':
      return 'Auto'
    case 'password':
      return 'Password'
    case 'publicKey':
      return 'Public Key'
    case 'agent':
      return 'Agent'
    case 'keyboardInteractive':
      return 'Keyboard Interactive'
  }
}

export const AUTH_SEGMENTS = AUTH_MODES.map((mode) => ({
  value: mode,
  label: labels[mode],
  title: authModeLabel(mode),
  icon: icons[mode],
}))
