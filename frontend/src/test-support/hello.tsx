import { t } from '../i18n/locale-model'

export function Hello(props: { name: string }) {
  return <span>{t('testSupport.hello.greeting', 'Hello, {name}!', { name: props.name })}</span>
}
