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
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { getChannels } from '@/features/channels/api'
import { CHANNEL_TYPE_CURSOR } from '@/features/channels/constants'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

export function CursorPoolOverviewCard() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const canRead = useAuthStore((s) =>
    Boolean(
      s.auth.user &&
      s.auth.user.role >= ROLE.ADMIN &&
      hasPermission(
        s.auth.user,
        ADMIN_PERMISSION_RESOURCES.CHANNEL,
        ADMIN_PERMISSION_ACTIONS.READ
      )
    )
  )
  const query = useQuery({
    queryKey: ['channels', 'list', 'cursor-overview'],
    queryFn: async () =>
      requireServerSuccess(
        await getChannels({ type: CHANNEL_TYPE_CURSOR, p: 1, page_size: 1 })
      ),
    enabled: canRead,
    staleTime: 30_000,
    retry: false,
    meta: { errorToast: false },
  })
  if (!canRead) return null
  let count = t('Loading...')
  if (query.isError) count = t('Unknown')
  else if (query.data?.data) count = formatNumber(query.data.data.total, locale)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Cursor account pool')}</CardTitle>
      </CardHeader>
      <CardContent className='flex flex-wrap items-center justify-between gap-3'>
        <div className='min-w-0 space-y-1'>
          <p className='font-medium'>
            {t('Account channels')}: {count}
          </p>
          <p className='text-muted-foreground text-sm'>
            {t(
              'View per-account quota, billing, concurrency and cooldown status.'
            )}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Gateway balances are separate from Cursor plan quota. Zero gateway cost does not mean zero upstream usage.'
            )}
          </p>
        </div>
        <Button variant='outline' render={<Link to='/cursor-accounts' />}>
          {t('Manage account pool')}
        </Button>
      </CardContent>
    </Card>
  )
}
