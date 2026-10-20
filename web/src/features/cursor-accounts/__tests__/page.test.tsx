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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import type { ComponentType } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { channelSchema } from '@/features/channels/types'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CursorAccounts } from '..'
import { CursorPoolOverviewCard } from '../components/overview-card'

const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} }, zh },
  initAsync: false,
})
const clients: QueryClient[] = []
beforeEach(async () => {
  await i18n.changeLanguage('en')
  localStorage.clear()
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  localStorage.clear()
  useAuthStore.getState().auth.setUser(null)
})

async function mount(component: ComponentType = CursorAccounts) {
  const Component = component
  const root = createRootRoute({ component: () => <Component /> })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  await router.load()
  render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </I18nextProvider>
  )
}

test('an empty account pool offers a Cursor-specific create action and quota settings', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = []
    if (url === '/api/channel') data = { items: [], total: 0 }
    else if (url === '/api/channel/default_base_urls') data = {}
    return { data: { success: true, data } }
  })
  await mount()
  await screen.findByText('No Cursor accounts')
  expect(
    screen.queryByRole('button', { name: 'Manage user quotas' })
  ).not.toBeInTheDocument()
  await userEvent
    .setup()
    .click(screen.getByRole('tab', { name: 'Gateway quota settings' }))
  expect(
    screen.getByRole('button', { name: 'Manage user quotas' })
  ).toHaveAttribute('href', '/users')
  expect(
    screen.getByRole('button', { name: 'Manage API key quotas' })
  ).toHaveAttribute('href', '/keys')
  expect(
    screen.getByRole('button', { name: 'Configure gateway pricing' })
  ).toHaveAttribute('href', '/system-settings/billing/model-pricing')
  await userEvent
    .setup()
    .click(screen.getAllByRole('button', { name: 'Add Cursor account' })[0])
  await waitFor(() => expect(screen.getByRole('dialog')).toBeVisible())
  expect(
    await screen.findByPlaceholderText('Cursor User API Key')
  ).toBeVisible()
})

test('an account-list error provides retry without a false empty-pool message', async () => {
  vi.spyOn(api, 'get').mockRejectedValue(new Error('Unavailable'))
  await mount()
  expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
  expect(screen.queryByText('No Cursor accounts')).not.toBeInTheDocument()
})

test('one failed account keeps the other account quota independently visible', async () => {
  const first = channelSchema.parse({
    id: 1,
    type: 64,
    key: '',
    status: 1,
    name: 'Primary',
    created_time: 0,
    test_time: 0,
    response_time: 0,
    balance_updated_time: 0,
    models: 'grok-4.7',
  })
  const second = { ...first, id: 2, name: 'Backup' }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = []
    if (url === '/api/channel') data = { items: [first, second], total: 2 }
    else if (url === '/api/channel/default_base_urls') data = {}
    else if (String(url).endsWith('/cursor/status')) {
      data = {
        route: 'test',
        quota:
          url === '/api/channel/1/cursor/status'
            ? {
                observed_at: new Date().toISOString(),
                expires_at: new Date(Date.now() + 30_000).toISOString(),
                windows: [{ name: 'Cursor Models', used_percent: 25 }],
                billing_cycle_end_ms: '',
                on_demand: 'unknown',
              }
            : null,
        health: {
          disabled: false,
          in_flight: 0,
          max_in_flight: 2,
          cooldowns: [],
        },
        error:
          url === '/api/channel/2/cursor/status'
            ? 'Cursor quota is unavailable'
            : undefined,
      }
    }
    return { data: { success: true, data } }
  })
  await mount()
  const primary = await screen.findByRole('article', { name: 'Primary' })
  expect(
    await within(primary).findByRole('progressbar', { name: 'Cursor Models' })
  ).toHaveAttribute('aria-valuenow', '25')
  const backup = screen.getByRole('article', { name: 'Backup' })
  expect(await within(backup).findByRole('status')).toHaveTextContent(
    'Cursor quota is unavailable'
  )
  expect(within(backup).queryByRole('progressbar')).not.toBeInTheDocument()
})

test('the overview hides the account pool from users without channel read access', async () => {
  useAuthStore.getState().auth.setUser({ id: 3, username: 'member', role: 1 })
  const get = vi.spyOn(api, 'get')
  await mount(CursorPoolOverviewCard)
  expect(screen.queryByText('Cursor account pool')).not.toBeInTheDocument()
  expect(get).not.toHaveBeenCalled()
})

test('the account page loads Chinese resources and responds to a language change', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = []
    if (url === '/api/channel') data = { items: [], total: 0 }
    else if (url === '/api/channel/default_base_urls') data = {}
    return { data: { success: true, data } }
  })
  await i18n.changeLanguage('zh')
  await mount()
  expect(
    await screen.findByRole('heading', { name: 'Cursor 账号池' })
  ).toBeVisible()
  expect(await screen.findByText('暂无 Cursor 账号')).toBeVisible()
  await act(async () => {
    await i18n.changeLanguage('en')
  })
  expect(
    screen.getByRole('heading', { name: 'Cursor account pool' })
  ).toBeVisible()
})
