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

interface CursorBillingStatusProps {
  account: CursorAccountStatus
  fresh: boolean
  scope?: string
}

export function CursorBillingStatus(props: CursorBillingStatusProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const quota = props.fresh ? props.account.quota : null
  const billing = quota?.on_demand_budget
  let enabled = t('Unknown')
  if (quota?.on_demand === 'enabled') enabled = t('Enabled')
  if (quota?.on_demand === 'disabled') enabled = t('Disabled')
  let budget = t('Paid headroom unknown')
  if (billing?.state === 'remaining') {
    budget = t('Reported paid headroom remains')
  }
  if (billing?.state === 'exhausted') {
    budget = t('On-demand spending limit reached')
  }
  if (billing?.state === 'disabled') budget = t('On-demand billing disabled')
  const reset = quota?.resets_at ? Date.parse(quota.resets_at) : Number.NaN
  const resetKnown =
    Number.isFinite(reset) &&
    quota?.reset_state === 'known' &&
    reset > Date.now()
  const resetElapsed = Number.isFinite(reset) && reset <= Date.now()
  const observation = props.account.health.last_requests?.find(
    (item) => item.scope === props.scope
  )
  let requestResult = t('Actual availability unknown')
  if (observation) {
    requestResult =
      observation.status >= 200 && observation.status < 300
        ? t('Last request in this pool succeeded')
        : t('Last request in this pool failed')
  }
  return (
    <div className='space-y-2 border-t pt-2'>
      <div className='flex flex-wrap gap-x-4 gap-y-1'>
        <p>
          {t('On-demand billing')}:{' '}
          <span className='font-medium'>{enabled}</span>
        </p>
        <p
          className={
            billing?.state === 'exhausted' ? 'text-destructive' : undefined
          }
        >
          {budget}
        </p>
      </div>
      {billing?.limits.map((limit) => {
        let label = t('Account spending limit')
        if (limit.scope === 'member') label = t('Member spending limit')
        if (limit.scope === 'team') label = t('Team spending limit')
        // Upstream USD must not be converted to new-api display currency/tokens.
        const money = (value: number | null) =>
          value !== null && Number.isFinite(value)
            ? `${formatNumber(value, locale)} USD`
            : t('Unknown')
        return (
          <div key={limit.scope} className='flex flex-wrap gap-x-3 gap-y-1'>
            <span className='font-medium'>{label}</span>
            <span>
              {t('Used')}: {money(limit.used_usd)}
            </span>
            <span>
              {t('Limit')}: {money(limit.limit_usd)}
            </span>
            <span>
              {t('Remaining')}: {money(limit.remaining_usd)}
            </span>
          </div>
        )
      })}
      <p>
        {t('Included quota reset')}:{' '}
        {resetKnown
          ? new Date(reset).toLocaleString(locale, {
              year: 'numeric',
              month: '2-digit',
              day: '2-digit',
              hour: '2-digit',
              minute: '2-digit',
              timeZoneName: 'short',
            })
          : t('Unknown')}
        {resetKnown && (
          <span className='text-muted-foreground'>
            {' '}
            · {t('Reported billing cycle end')}
          </span>
        )}
        {resetElapsed && (
          <span className='text-muted-foreground'>
            {' '}
            · {t('Reported cycle ended; awaiting upstream refresh')}
          </span>
        )}
      </p>
      <p>
        {requestResult}
        {observation && (
          <span className='text-muted-foreground'>
            {' '}
            · {formatNumber(observation.status, locale)} ·{' '}
            {new Date(observation.observed_at).toLocaleString(locale)}
          </span>
        )}
      </p>
    </div>
  )
}
