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
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export function CursorQuotaSettings() {
  const { t } = useTranslation()
  const isRoot = useAuthStore((s) => s.auth.user?.role === ROLE.SUPER_ADMIN)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Gateway quota settings')}</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Gateway balances and API key quotas are internal allocations. Cursor usage and spending limits are reported by the upstream account.'
          )}
        </p>
        <div className='grid gap-3 md:grid-cols-3'>
          <div className='space-y-2 rounded-lg border p-3'>
            <p className='font-medium'>{t('User quota')}</p>
            <p className='text-muted-foreground text-xs'>
              {t('Adjust a gateway user balance from the user quota action.')}
            </p>
            <Button variant='outline' size='sm' render={<Link to='/users' />}>
              {t('Manage user quotas')}
            </Button>
          </div>
          <div className='space-y-2 rounded-lg border p-3'>
            <p className='font-medium'>{t('API key quota')}</p>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Set remaining quota, expiration, model permissions and IP restrictions for your API keys.'
              )}
            </p>
            <Button variant='outline' size='sm' render={<Link to='/keys' />}>
              {t('Manage API key quotas')}
            </Button>
          </div>
          <div className='space-y-2 rounded-lg border p-3'>
            <p className='font-medium'>{t('Model pricing')}</p>
            <p className='text-muted-foreground text-xs'>
              {t(
                'A zero gateway price does not limit Cursor spending. Configure pricing before relying on gateway quota controls.'
              )}
            </p>
            {isRoot && (
              <Button
                variant='outline'
                size='sm'
                render={
                  <Link
                    to='/system-settings/billing/$section'
                    params={{ section: 'model-pricing' }}
                  />
                }
              >
                {t('Configure gateway pricing')}
              </Button>
            )}
          </div>
        </div>
        <p className='text-muted-foreground text-xs'>
          {t(
            'Cursor plan quota, on-demand billing and account or team spending limits are read-only here. Manage them in Cursor.'
          )}
        </p>
      </CardContent>
    </Card>
  )
}
