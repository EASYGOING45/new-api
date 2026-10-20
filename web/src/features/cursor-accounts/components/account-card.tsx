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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Progress } from '@/components/ui/progress'
import { updateChannelStatus } from '@/features/channels/api'
import { useChannels } from '@/features/channels/components/channels-provider'
import {
  CHANNEL_STATUS,
  CHANNEL_STATUS_LABELS,
} from '@/features/channels/constants'
import type { Channel } from '@/features/channels/types'
import { CursorBillingStatus } from '@/features/playground/components/input/cursor-billing-status'
import { CursorQuotaSummary } from '@/features/playground/components/input/cursor-quota-summary'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatNumber } from '@/lib/format'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { cursorAccountQueryKey, getCursorAccountStatus } from '../api'

export function CursorAccountCard(props: {
  channel: Channel
  revision?: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const channels = useChannels()
  const user = useAuthStore((s) => s.auth.user)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const canEdit = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.WRITE
  )
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const enabled = props.channel.status === CHANNEL_STATUS.ENABLED
  const status = useQuery({
    queryKey: [...cursorAccountQueryKey, props.channel.id, props.revision],
    queryFn: ({ signal }) => getCursorAccountStatus(props.channel.id, signal),
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: false,
    meta: { errorToast: false },
  })
  const toggle = useMutation({
    mutationFn: async () =>
      requireServerSuccess(
        await updateChannelStatus(
          props.channel.id,
          enabled ? CHANNEL_STATUS.MANUAL_DISABLED : CHANNEL_STATUS.ENABLED
        )
      ),
    onSuccess: async () => {
      setConfirmOpen(false)
      await queryClient.invalidateQueries({ queryKey: ['channels'] })
    },
  })
  const account = status.data
  const [expiredSnapshot, setExpiredSnapshot] = useState<string>()
  const expiresAt = account?.quota?.expires_at
  useEffect(() => {
    if (!expiresAt) return
    const delay = Date.parse(expiresAt) - Date.now()
    if (!Number.isFinite(delay) || delay <= 0) return
    const timer = setTimeout(() => setExpiredSnapshot(expiresAt), delay + 1)
    return () => clearTimeout(timer)
  }, [expiresAt])
  const fresh = Boolean(
    !status.isError &&
    !account?.error &&
    account?.quota &&
    expiredSnapshot !== account.quota.expires_at &&
    Date.parse(account.quota.expires_at) > Date.now()
  )
  let error = account?.error
  if (status.isError) {
    error =
      getServerErrorMessage(status.error) || t('Cursor quota is unavailable')
  }
  return (
    <article aria-label={props.channel.name} className='min-w-0'>
      <Card className='h-full'>
        <CardHeader>
          <div className='flex min-w-0 items-start justify-between gap-2'>
            <CardTitle className='min-w-0 break-words'>
              {props.channel.name}
            </CardTitle>
            <Badge variant={enabled ? 'secondary' : 'outline'}>
              {t(
                CHANNEL_STATUS_LABELS[
                  props.channel.status as keyof typeof CHANNEL_STATUS_LABELS
                ] || 'Unknown'
              )}
            </Badge>
          </div>
          <p className='text-muted-foreground text-xs break-words'>
            {t('Channel')} #{formatNumber(props.channel.id, locale)} ·{' '}
            {props.channel.group} · {t('Priority')}:{' '}
            {formatNumber(props.channel.priority || 0, locale)} · {t('Weight')}:{' '}
            {formatNumber(props.channel.weight || 0, locale)}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t('Models')}:{' '}
            {formatNumber(
              props.channel.models.split(',').filter(Boolean).length,
              locale
            )}
          </p>
        </CardHeader>
        <CardContent className='space-y-4'>
          {status.isPending && (
            <LoadingState inline message={t('Loading...')} />
          )}
          {error && (
            <p role='status' className='text-destructive text-sm'>
              {t(error)}
            </p>
          )}
          {account && (
            <>
              {['Cursor Models', 'Other Models'].map((scope) => {
                const window = fresh
                  ? account.quota?.windows.find((item) => item.name === scope)
                  : undefined
                return (
                  <div key={scope} className='space-y-2'>
                    <CursorQuotaSummary account={account} scope={scope} />
                    {window && (
                      <Progress
                        aria-label={scope}
                        value={Math.max(0, Math.min(100, window.used_percent))}
                      />
                    )}
                  </div>
                )
              })}
              <div className='border-t pt-2 text-sm'>
                <p>
                  {t('In-flight requests')}:{' '}
                  {formatNumber(account.health.in_flight, locale)} /{' '}
                  {account.health.max_in_flight > 0
                    ? formatNumber(account.health.max_in_flight, locale)
                    : t('Unknown')}
                </p>
                {account.health.cooldowns.map((cooldown) => (
                  <p
                    key={cooldown.scope}
                    className='text-destructive break-words'
                  >
                    {cooldown.scope === 'account'
                      ? t('Entire Cursor account')
                      : cooldown.scope}{' '}
                    ·{' '}
                    {cooldown.state === 'awaiting_probe'
                      ? t('Awaiting recovery probe')
                      : `${t('Cooling down until')} ${new Date(cooldown.retry_at).toLocaleString(locale)}`}
                  </p>
                ))}
              </div>
              <Collapsible>
                <CollapsibleTrigger className='cursor-pointer text-sm font-medium'>
                  {t('Billing and reset details')}
                </CollapsibleTrigger>
                <CollapsibleContent className='mt-2 text-xs'>
                  <CursorBillingStatus account={account} fresh={fresh} />
                </CollapsibleContent>
              </Collapsible>
            </>
          )}
          <div className='flex flex-wrap gap-2 border-t pt-3'>
            <Button
              variant='outline'
              size='sm'
              disabled={status.isFetching}
              onClick={() => void status.refetch()}
              aria-label={t('Refresh account status')}
            >
              <RefreshCw className={status.isFetching ? 'animate-spin' : ''} />
              {t('Refresh')}
            </Button>
            {canEdit && (
              <Button
                variant='outline'
                size='sm'
                onClick={() => {
                  channels.setCurrentRow(props.channel)
                  channels.setOpen('update-channel')
                }}
              >
                {t('Edit')}
              </Button>
            )}
            {canOperate && (
              <Button
                variant='outline'
                size='sm'
                onClick={() => {
                  channels.setCurrentRow(props.channel)
                  channels.setOpen('fetch-models')
                }}
              >
                {t('Sync models')}
              </Button>
            )}
            {canOperate && (
              <Button
                variant='outline'
                size='sm'
                onClick={() => setConfirmOpen(true)}
              >
                {enabled ? t('Disable') : t('Enable')}
              </Button>
            )}
          </div>
        </CardContent>
      </Card>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={
          enabled ? t('Disable account channel') : t('Enable account channel')
        }
        desc={props.channel.name}
        isLoading={toggle.isPending}
        handleConfirm={() => toggle.mutate()}
      />
    </article>
  )
}
