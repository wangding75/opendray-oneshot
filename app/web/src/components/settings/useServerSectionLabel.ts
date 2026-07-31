import { useTranslation } from 'react-i18next'
import type { ServerSectionId } from './serverSettingsTypes'

export function useServerSectionLabel() {
  const { t } = useTranslation()
  return (id: ServerSectionId) => ({
    title: t(`web.serverSettings.sections.${id}.title`),
    desc: t(`web.serverSettings.sections.${id}.desc`),
  })
}
