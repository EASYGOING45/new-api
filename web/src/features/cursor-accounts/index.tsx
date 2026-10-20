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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getCoreRowModel,
  useReactTable,
  type PaginationState,
} from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTableCardGrid, DataTablePagination } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { getChannels } from '@/features/channels/api'
import { ChannelsDialogs } from '@/features/channels/components/channels-dialogs'
import {
  ChannelsProvider,
  useChannels,
} from '@/features/channels/components/channels-provider'
import { CHANNEL_TYPE_CURSOR } from '@/features/channels/constants'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { cursorAccountQueryKey } from './api'
import { CursorAccountCard } from './components/account-card'
import { CursorQuotaSettings } from './components/quota-settings'

const columns = [{ accessorKey: 'name' }]

export function CursorAccounts() {
  return (
    <ChannelsProvider>
      <CursorAccountsContent />
      <ChannelsDialogs initialChannelType={CHANNEL_TYPE_CURSOR} />
    </ChannelsProvider>
  )
}

function CursorAccountsContent() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const channels = useChannels()
  const user = useAuthStore((s) => s.auth.user)
  const canCreate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 10,
  })
  const query = useQuery({
    queryKey: ['channels', 'list', 'cursor-accounts', pagination],
    queryFn: async () =>
      requireServerSuccess(
        await getChannels({
          type: CHANNEL_TYPE_CURSOR,
          p: pagination.pageIndex + 1,
          page_size: pagination.pageSize,
          id_sort: true,
        })
      ),
  })
  const table = useReactTable({
    data: query.data?.data?.items || [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    rowCount: query.data?.data?.total || 0,
    state: { pagination },
    onPaginationChange: setPagination,
  })
  const add = (
    <Button
      onClick={() => {
        channels.setCurrentRow(null)
        channels.setOpen('create-channel')
      }}
    >
      {t('Add Cursor account')}
    </Button>
  )
  let countLabel = t('Unknown')
  if (query.isPending) countLabel = t('Loading...')
  else if (!query.isError && query.data?.data) {
    countLabel = formatNumber(query.data.data.total, locale)
  }
  let content
  if (query.isError) {
    content = <ErrorState onRetry={() => void query.refetch()} />
  } else if (!query.isPending && table.getRowModel().rows.length === 0) {
    content = (
      <EmptyState
        title={t('No Cursor accounts')}
        description={t('Add a Cursor channel to view its quota and health.')}
        action={canCreate ? add : undefined}
      />
    )
  } else {
    content = (
      <DataTableCardGrid
        table={table}
        isLoading={query.isPending}
        gridClassName='grid grid-cols-1 gap-4 xl:grid-cols-2'
        getRowClassName={() => 'border-0 bg-transparent p-0'}
        renderCard={(row) => (
          <CursorAccountCard
            channel={row.original}
            revision={query.dataUpdatedAt}
          />
        )}
      />
    )
  }
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Cursor account pool')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          disabled={query.isFetching}
          onClick={() => {
            void query.refetch()
            void queryClient.invalidateQueries({
              queryKey: cursorAccountQueryKey,
            })
          }}
        >
          {t('Refresh status')}
        </Button>
        {canCreate && add}
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-5'>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Use one Cursor account per channel. Health and concurrency are shared by the same key; quota percentages must not be added together.'
            )}
          </p>
          <Tabs defaultValue='accounts'>
            <TabsList>
              <TabsTrigger value='accounts'>
                {t('Account channels')}
              </TabsTrigger>
              <TabsTrigger value='quota'>
                {t('Gateway quota settings')}
              </TabsTrigger>
            </TabsList>
            <TabsContent value='accounts' className='space-y-4 pt-3'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <h2 className='font-semibold'>
                  {t('Account channels')} ({countLabel})
                </h2>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Account health is local to this gateway instance. Status refreshes every 30 seconds while this page is active.'
                  )}
                </p>
              </div>
              {content}
              {!query.isError && <DataTablePagination table={table} compact />}
            </TabsContent>
            <TabsContent value='quota' className='pt-3'>
              <CursorQuotaSettings />
            </TabsContent>
          </Tabs>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
