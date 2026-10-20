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
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { ChannelsProvider } from '@/features/channels/components/channels-provider'
import { channelSchema } from '@/features/channels/types'
import type { CursorAccountStatus } from '@/features/playground/types'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CursorAccountCard } from '../components/account-card'

const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} } },
  initAsync: false,
})
const clients: QueryClient[] = []
const channel = channelSchema.parse({
  id: 1,
  type: 64,
  key: '',
  status: 1,
  name: 'Account A',
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
  models: 'composer-2.5,grok-4.7',
})

function status(): CursorAccountStatus & {
  quota: NonNullable<CursorAccountStatus['quota']>
} {
  return {
    route: '1',
    quota: {
      observed_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 30_000).toISOString(),
      windows: [
        { name: 'Cursor Models', used_percent: 20 },
        { name: 'Other Models', used_percent: 100 },
      ],
      billing_cycle_end_ms: '',
      on_demand: 'unknown',
    },
    health: {
      disabled: false,
      in_flight: 1,
      max_in_flight: 2,
      cooldowns: [
        {
          scope: 'Other Models',
          retry_at: new Date(Date.now() + 60_000).toISOString(),
          state: 'cooling',
        },
      ],
    },
  }
}

beforeEach(() => {
  localStorage.clear()
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  vi.useRealTimers()
  localStorage.clear()
  useAuthStore.getState().auth.setUser(null)
})

function mount(row = channel) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <ChannelsProvider>
          <CursorAccountCard channel={row} />
        </ChannelsProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
}

test('one exhausted pool keeps the other pool usage and concurrency visible', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: status() },
  })
  mount()
  expect(
    await screen.findByRole('progressbar', { name: 'Cursor Models' })
  ).toHaveAttribute('aria-valuenow', '20')
  expect(
    screen.getByRole('progressbar', { name: 'Other Models' })
  ).toHaveAttribute('aria-valuenow', '100')
  expect(
    screen.getByText('In-flight requests', { exact: false })
  ).toHaveTextContent('1 / 2')
  expect(
    screen.getByText('Cooling down until', { exact: false })
  ).toHaveTextContent('Other Models')
})

test('an upstream quota failure shows unknown usage rather than zero quota', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { ...status(), quota: null, error: 'Cursor quota is unavailable' },
    },
  })
  mount()
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Cursor quota is unavailable'
  )
  expect(screen.getAllByText('Unknown usage')).toHaveLength(2)
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
})

test('an expired snapshot is not displayed as current quota', async () => {
  const data = status()
  data.quota.expires_at = new Date(Date.now() - 1000).toISOString()
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data } })
  mount()
  expect(await screen.findAllByText('Unknown usage')).toHaveLength(2)
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
})

test('a status network failure remains local to its account card', async () => {
  vi.spyOn(api, 'get').mockRejectedValue(
    new Error('Account status request failed')
  )
  mount()
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Account status request failed'
  )
  expect(screen.getByRole('article', { name: 'Account A' })).toBeVisible()
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
})

test('a displayed snapshot becomes unknown when its lifetime expires', async () => {
  vi.useFakeTimers()
  const data = status()
  data.quota.expires_at = new Date(Date.now() + 1000).toISOString()
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data } })
  mount()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
  expect(
    screen.getByRole('progressbar', { name: 'Cursor Models' })
  ).toBeVisible()
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1001)
  })
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  expect(screen.getAllByText('Unknown usage')).toHaveLength(2)
})

test('an elapsed cooldown shows the recovery probe state', async () => {
  const data = status()
  data.health.cooldowns = [
    {
      scope: 'Other Models',
      retry_at: new Date(Date.now() - 1000).toISOString(),
      state: 'awaiting_probe',
    },
  ]
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data } })
  mount()
  const observations = await screen.findAllByText('Awaiting recovery probe', {
    exact: false,
  })
  for (const observation of observations) {
    expect(observation).toBeVisible()
  }
  expect(
    screen.queryByText('Cooling down until', { exact: false })
  ).not.toBeInTheDocument()
})

test('a read-only administrator can refresh without mutation controls', async () => {
  useAuthStore.getState().auth.setUser({
    id: 2,
    username: 'reader',
    role: 10,
    permissions: { admin_permissions: { channel: { read: true } } },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: status() },
  })
  mount()
  await screen.findByRole('progressbar', { name: 'Cursor Models' })
  expect(
    screen.getByRole('button', { name: 'Refresh account status' })
  ).toBeEnabled()
  for (const name of ['Edit', 'Disable', 'Sync models']) {
    expect(screen.queryByRole('button', { name })).not.toBeInTheDocument()
  }
})

test('enabling a disabled channel requires confirmation and preserves its account identity', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: status() },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const row = {
    ...channel,
    status: 2,
    name: 'Very long account name '.repeat(12).trim(),
  }
  mount(row)
  const article = screen.getByRole('article', { name: row.name })
  expect(article).toHaveClass('min-w-0')
  const user = userEvent.setup()
  await user.click(within(article).getByRole('button', { name: 'Enable' }))
  expect(post).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel/1/status',
      { status: 1 },
      expect.any(Object)
    )
  )
})

test('refresh remains disabled while the account request is pending', async () => {
  let release!: (value: unknown) => void
  vi.spyOn(api, 'get').mockImplementation(
    () =>
      new Promise((resolve) => {
        release = resolve
      })
  )
  mount()
  expect(
    screen.getByRole('button', { name: 'Refresh account status' })
  ).toBeDisabled()
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  release({ data: { success: true, data: status() } })
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Refresh account status' })
    ).toBeEnabled()
  )
})
