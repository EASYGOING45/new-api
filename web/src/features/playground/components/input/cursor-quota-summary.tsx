/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { CursorAccountStatus } from '../../types'

interface CursorQuotaSummaryProps {
  account: CursorAccountStatus
  scope?: string
  accountNumber?: number
}

export function CursorQuotaSummary(props: CursorQuotaSummaryProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const health = props.account.health
  const quota = props.account.quota
  const fresh =
    !props.account.error && quota && Date.parse(quota.expires_at) > Date.now()
  const used = fresh
    ? quota.windows.find((item) => item.name === props.scope)?.used_percent
    : undefined
  const observation = health.last_requests?.find(
    (item) => item.scope === props.scope
  )
  // Only this pool and account-wide protection affect the selected model.
  const cooldown = (health.cooldowns ?? [])
    .filter((item) => item.scope === 'account' || item.scope === props.scope)
    .sort((a, b) => Date.parse(b.retry_at) - Date.parse(a.retry_at))[0]
  let status = t('Actual availability unknown')
  let warning = false
  if (health.disabled) {
    status = t('Cursor account authentication failed')
    warning = true
  } else if (cooldown) {
    status = t('Awaiting recovery probe')
    if (Date.parse(cooldown.retry_at) > Date.now()) {
      status = `${t('Cooling until')} ${new Date(cooldown.retry_at).toLocaleTimeString(locale)}`
    }
    warning = true
  } else if (
    health.max_in_flight > 0 &&
    health.in_flight >= health.max_in_flight
  ) {
    status = t('Cursor account concurrency limit reached')
    warning = true
  } else if (used !== undefined && used >= 100) {
    status = t('Included quota exhausted; availability unknown')
    if (
      quota?.on_demand_budget?.state === 'exhausted' ||
      quota?.on_demand_budget?.state === 'disabled' ||
      quota?.on_demand === 'disabled'
    ) {
      status = t('No reported budget available')
      warning = true
    } else if (
      quota?.on_demand === 'enabled' &&
      quota.on_demand_budget?.state === 'remaining'
    ) {
      status = t('Included quota exhausted; paid headroom remains')
    }
  } else if (used !== undefined && observation) {
    status = t('Last request succeeded')
    if (observation.status < 200 || observation.status >= 300) {
      status = t('Last request failed')
      warning = true
    }
  } else if (used !== undefined) {
    status = t('Included quota remains')
  }

  return (
    <div className='flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 break-words'>
      {props.accountNumber !== undefined && (
        <span className='text-muted-foreground'>
          {t('Route account')} {formatNumber(props.accountNumber, locale)}
        </span>
      )}
      <span className='font-medium'>{props.scope || t('Selected pool')}</span>
      <span className='tabular-nums'>
        {used === undefined
          ? t('Unknown usage')
          : `${t('Used')} ${formatNumber(used, locale)}%`}
      </span>
      <span className={warning ? 'text-destructive' : 'text-muted-foreground'}>
        {status}
      </span>
    </div>
  )
}
