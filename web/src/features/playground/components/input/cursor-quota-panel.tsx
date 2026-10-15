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
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { getCursorStatus } from '../../api'
import { CursorBillingStatus } from './cursor-billing-status'
import { CursorQuotaSummary } from './cursor-quota-summary'

interface CursorQuotaPanelProps {
  group: string
  model: string
  scope?: string
  isGenerating?: boolean
}

export function CursorQuotaPanel(props: CursorQuotaPanelProps) {
  const { t, i18n } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const query = useQuery({
    queryKey: ['playground-cursor-status', props.group, props.model],
    queryFn: () => getCursorStatus(props.group, props.model),
    enabled: Boolean(props.group && props.model),
    staleTime: 0,
    refetchInterval: 10_000,
    retry: false,
    meta: { errorToast: false },
  })
  const refetch = query.refetch
  useEffect(() => {
    if (!props.isGenerating) void refetch()
  }, [props.isGenerating, refetch])

  return (
    <section
      aria-label={t('Cursor quota status')}
      className='bg-muted/20 rounded-xl border px-3 py-2 text-xs'
    >
      <Collapsible open={expanded} onOpenChange={setExpanded}>
        <div className='flex flex-wrap items-start gap-x-2 gap-y-1'>
          <div
            role='group'
            aria-label={t('Selected pool')}
            className='min-w-0 flex-1 basis-48 space-y-1 py-1'
          >
            {query.isPending && (
              <p role='status'>{t('Loading quota status')}</p>
            )}
            {(query.isError || (!query.isPending && !query.data?.length)) && (
              <p role='alert'>
                {t('Cursor quota is unavailable')} ·{' '}
                {t('Actual availability unknown')}
              </p>
            )}
            {!query.isError &&
              query.data?.map((account, index) => (
                <CursorQuotaSummary
                  key={account.route}
                  account={account}
                  scope={props.scope}
                  accountNumber={query.data.length > 1 ? index + 1 : undefined}
                />
              ))}
          </div>
          <div className='ml-auto flex shrink-0 items-center gap-1'>
            <Button
              type='button'
              size='icon-xs'
              variant='ghost'
              aria-label={t('Refresh status')}
              title={t('Refresh status')}
              disabled={query.isFetching}
              onClick={() => void refetch()}
            >
              <RefreshCw
                aria-hidden='true'
                className={
                  query.isFetching ? 'motion-safe:animate-spin' : undefined
                }
              />
            </Button>
            <CollapsibleTrigger
              render={<Button type='button' size='xs' variant='ghost' />}
            >
              {expanded ? t('Hide quota details') : t('Show quota details')}
              <ChevronDown
                aria-hidden='true'
                className={expanded ? 'rotate-180' : undefined}
              />
            </CollapsibleTrigger>
          </div>
        </div>
        <CollapsibleContent>
          <div
            role='region'
            aria-label={t('Quota details')}
            tabIndex={0}
            className='focus-visible:outline-ring mt-2 max-h-[min(36dvh,18rem)] space-y-2 overflow-y-auto overscroll-contain border-t pt-2 focus-visible:outline'
          >
            {!query.isError &&
              query.data?.map((account, index) => {
                const fresh =
                  !account.error &&
                  account.quota &&
                  Date.parse(account.quota.expires_at) > Date.now()
                return (
                  <div
                    key={account.route}
                    className='space-y-2 border-t pt-2 first:border-0'
                  >
                    {(query.data?.length ?? 0) > 1 && (
                      <p>
                        {t('Route account')} {formatNumber(index + 1, locale)}
                      </p>
                    )}
                    {(account.error || !fresh) && (
                      <p role='status'>{t('Cursor quota is unavailable')}</p>
                    )}
                    <div className='grid gap-3 sm:grid-cols-2'>
                      {['Cursor Models', 'Other Models'].map((name) => {
                        const window = fresh
                          ? account.quota?.windows.find(
                              (item) => item.name === name
                            )
                          : undefined
                        const used = window?.used_percent
                        const exhausted = used !== undefined && used >= 100
                        return (
                          <div key={name}>
                            <div className='mb-1 flex flex-wrap justify-between gap-x-2'>
                              <span className='font-medium'>{name}</span>
                              <span>
                                {used === undefined
                                  ? t('Unknown usage')
                                  : `${t('Used')} ${formatNumber(used, locale)}%`}
                              </span>
                            </div>
                            {used !== undefined && (
                              <Progress
                                aria-label={name}
                                value={Math.min(100, used)}
                                className={
                                  exhausted
                                    ? '[&_[data-slot=progress-indicator]]:bg-destructive'
                                    : undefined
                                }
                              />
                            )}
                            {exhausted && (
                              <p className='text-destructive mt-1 font-medium'>
                                {t('Included quota exhausted')}
                              </p>
                            )}
                          </div>
                        )
                      })}
                    </div>
                    <CursorBillingStatus
                      account={account}
                      fresh={Boolean(fresh)}
                      scope={props.scope}
                    />
                    {account.health.disabled && (
                      <Alert variant='destructive'>
                        <AlertDescription>
                          {t('Cursor account authentication failed')}
                        </AlertDescription>
                      </Alert>
                    )}
                    {account.health.in_flight >= account.health.max_in_flight &&
                      account.health.max_in_flight > 0 && (
                        <p role='status'>
                          {t('Cursor account concurrency limit reached')}
                        </p>
                      )}
                    {(account.health.cooldowns ?? []).map((cooldown) => (
                      <p
                        key={cooldown.scope}
                        role='status'
                        className='text-destructive'
                      >
                        {cooldown.scope === 'account'
                          ? t('Entire Cursor account')
                          : cooldown.scope}
                        :{' '}
                        {Date.parse(cooldown.retry_at) > Date.now()
                          ? t('Cooling until')
                          : t('Awaiting recovery probe')}{' '}
                        {new Date(cooldown.retry_at).toLocaleTimeString(locale)}
                      </p>
                    ))}
                    {account.quota && (
                      <p className='text-muted-foreground'>
                        {t('Observed at')}{' '}
                        {new Date(account.quota.observed_at).toLocaleTimeString(
                          locale
                        )}
                      </p>
                    )}
                  </div>
                )
              })}
            <p className='text-muted-foreground mt-2'>
              {t(
                'Included quota exhaustion does not disable the account. Paid headroom is not a guarantee that a request will succeed.'
              )}
            </p>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </section>
  )
}
