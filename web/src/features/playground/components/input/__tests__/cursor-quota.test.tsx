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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, expect, test, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import type { CursorAccountStatus } from '../../../types'
import { CursorQuotaPanel } from '../cursor-quota-panel'

const clients: QueryClient[] = []
afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  vi.restoreAllMocks()
})
function account(): CursorAccountStatus {
  return {
    route: '1',
    quota: {
      observed_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 30_000).toISOString(),
      windows: [
        { name: 'Cursor Models', used_percent: 2.7 },
        { name: 'Other Models', used_percent: 100 },
      ],
      billing_cycle_end_ms: '',
      on_demand: 'not_queried',
    },
    health: {
      disabled: false,
      in_flight: 0,
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
async function mount(expanded = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const view = render(
    <CursorQuotaPanel group='default' model='grok' scope='Cursor Models' />,
    {
      wrapper: ({ children }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    }
  )
  if (expanded) {
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Show quota details' }))
  }
  return view
}
test('exhausted Other Models shows its own warning while Cursor Models retains its usage', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [account()] },
  })
  await mount()
  expect(
    await screen.findByRole('progressbar', { name: 'Cursor Models' })
  ).toHaveAttribute('aria-valuenow', '2.7')
  expect(
    screen.getByRole('progressbar', { name: 'Other Models' })
  ).toHaveAttribute('aria-valuenow', '100')
  expect(screen.getByText('Included quota exhausted')).toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent(
    'Other Models: Cooling until'
  )
  expect(screen.queryByText(/Entire Cursor account/)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Refresh status' })).toBeEnabled()
})
test('failed refresh removes old percentages and manual retry recovers', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: [account()] } })
  const user = userEvent.setup()
  await mount()
  await screen.findByRole('progressbar', { name: 'Cursor Models' })
  get.mockRejectedValueOnce(new Error('network failed'))
  await user.click(screen.getByRole('button', { name: 'Refresh status' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Cursor quota is unavailable'
  )
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Refresh status' }))
  expect(
    await screen.findByRole('progressbar', { name: 'Cursor Models' })
  ).toBeInTheDocument()
})
test('missing buckets stay unknown and multiple accounts retain separate percentages', async () => {
  const first = account()
  if (first.quota) {
    first.quota.windows = [{ name: 'Cursor Models', used_percent: 20 }]
  }
  first.health.cooldowns = []
  const second = account()
  if (second.quota) {
    second.quota.windows = [{ name: 'Cursor Models', used_percent: 40 }]
  }
  second.route = '2'
  second.health.cooldowns = []
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [first, second] },
  })
  await mount()
  await screen.findAllByText('Route account 1')
  expect(screen.getAllByText('Route account 2')).toHaveLength(2)
  expect(screen.getAllByText('Unknown usage')).toHaveLength(2)
  expect(
    screen
      .getAllByRole('progressbar', { name: 'Cursor Models' })
      .map((el) => el.getAttribute('aria-valuenow'))
  ).toEqual(['20', '40'])
  expect(
    screen.queryByRole('progressbar', { name: 'Other Models' })
  ).not.toBeInTheDocument()
})
test('initial loading, empty status and expired observations never display zero usage', async () => {
  let resolve: (value: unknown) => void = () => undefined
  vi.spyOn(api, 'get').mockReturnValue(
    new Promise((done) => {
      resolve = done
    })
  )
  await mount()
  expect(screen.getByRole('status')).toHaveTextContent('Loading quota status')
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  await act(async () => resolve({ data: { success: true, data: [] } }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Cursor quota is unavailable'
  )
  const expired = account()
  if (expired.quota) expired.quota.expires_at = '2000-01-01T00:00:00Z'
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: [expired] },
  })
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Refresh status' }))
  await waitFor(() =>
    expect(screen.getAllByText('Unknown usage')).toHaveLength(3)
  )
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
})
test('quota labels update when switching to Chinese', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [account()] },
  })
  await mount()
  await screen.findByText('Included quota exhausted')
  i18next.addResourceBundle('zh', 'translation', zh.translation)
  try {
    await act(async () => {
      await i18next.changeLanguage('zh')
    })
    expect(screen.getByText('套餐额度已用尽')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '刷新状态' })).toBeInTheDocument()
  } finally {
    await act(async () => {
      await i18next.changeLanguage('en')
    })
  }
})

test('team balance does not hide an exhausted member limit and the reset has a timezone', async () => {
  const data = account()
  if (!data.quota) throw new Error('missing fixture')
  data.quota.on_demand = 'enabled'
  data.quota.on_demand_budget = {
    state: 'exhausted',
    limits: [
      {
        scope: 'member',
        used_usd: 1310.22,
        limit_usd: 1310,
        remaining_usd: 0,
        state: 'exhausted',
      },
      {
        scope: 'team',
        used_usd: 200,
        limit_usd: 1000,
        remaining_usd: 800,
        state: 'remaining',
      },
    ],
  }
  data.quota.resets_at = '2099-10-30T15:00:00Z'
  data.quota.reset_state = 'known'
  data.health.last_requests = [
    {
      scope: 'Cursor Models',
      status: 200,
      observed_at: new Date().toISOString(),
    },
    {
      scope: 'Other Models',
      status: 429,
      observed_at: new Date().toISOString(),
    },
  ]
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [data] },
  })
  await mount()
  await screen.findByText('On-demand spending limit reached')
  expect(screen.getByText('Member spending limit')).toBeInTheDocument()
  expect(screen.getByText('Team spending limit')).toBeInTheDocument()
  expect(screen.getByText('Used: 1,310.22 USD')).toBeInTheDocument()
  expect(screen.getByText('Remaining: 800 USD')).toBeInTheDocument()
  expect(screen.getByText(/Included quota reset:/)).toHaveTextContent('2099')
  expect(screen.getByText(/Reported billing cycle end/)).toBeInTheDocument()
  expect(
    screen.getByText(/Last request in this pool succeeded/)
  ).toBeInTheDocument()
  expect(
    screen.queryByText(/Last request in this pool failed/)
  ).not.toBeInTheDocument()
  expect(
    screen.queryByText('Cursor account authentication failed')
  ).not.toBeInTheDocument()
})

test('missing paid settings and reset remain unknown while included usage remains visible', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [account()] },
  })
  await mount()
  await screen.findByText('Paid headroom unknown')
  expect(screen.getByText(/On-demand billing:/)).toHaveTextContent('Unknown')
  expect(screen.getByText(/Included quota reset:/)).toHaveTextContent('Unknown')
  expect(screen.getByText('Actual availability unknown')).toBeInTheDocument()
  expect(
    screen.getByRole('progressbar', { name: 'Cursor Models' })
  ).toHaveAttribute('aria-valuenow', '2.7')
})

test('expired cycle is not advanced and a stale paid snapshot is not presented as current', async () => {
  const data = account()
  if (!data.quota) throw new Error('missing fixture')
  data.quota.on_demand = 'enabled'
  data.quota.on_demand_budget = { state: 'remaining', limits: [] }
  data.quota.reset_state = 'known'
  data.quota.resets_at = '2000-01-01T00:00:00Z'
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: [data] } })
  await mount()
  await screen.findByText(/Reported cycle ended; awaiting upstream refresh/)
  expect(screen.getByText(/Included quota reset:/)).toHaveTextContent('Unknown')
  expect(screen.getByText('Reported paid headroom remains')).toBeInTheDocument()
  data.quota.expires_at = '2000-01-01T00:00:00Z'
  get.mockResolvedValue({ data: { success: true, data: [data] } })
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Refresh status' }))
  await screen.findByText('Paid headroom unknown')
  expect(
    screen.queryByText('Reported paid headroom remains')
  ).not.toBeInTheDocument()
})

test('disabled on-demand billing does not imply account authentication failure', async () => {
  const data = account()
  if (!data.quota) throw new Error('missing fixture')
  data.quota.on_demand = 'disabled'
  data.quota.on_demand_budget = { state: 'disabled', limits: [] }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [data] },
  })
  await mount()
  await screen.findByText('On-demand billing disabled')
  expect(
    screen.queryByText('Cursor account authentication failed')
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Refresh status' })).toBeEnabled()
})

test('quota panel starts collapsed and keeps only the selected pool usage and status visible', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [account()] },
  })
  await mount(false)
  const summary = within(screen.getByRole('group', { name: 'Selected pool' }))
  expect(await summary.findByText('Used 2.7%')).toBeInTheDocument()
  expect(summary.getByText('Included quota remains')).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Show quota details' })
  ).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByText('Other Models')).not.toBeInTheDocument()
  expect(screen.queryByText(/Cooling until/)).not.toBeInTheDocument()
  expect(screen.queryByText(/On-demand billing/)).not.toBeInTheDocument()
  expect(screen.queryByText(/Included quota reset/)).not.toBeInTheDocument()
  expect(
    screen.queryByRole('region', { name: 'Quota details' })
  ).not.toBeInTheDocument()
})

test('keyboard expands and collapses bounded quota details without losing trigger focus', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [account()] },
  })
  await mount(false)
  const user = userEvent.setup()
  const trigger = screen.getByRole('button', { name: 'Show quota details' })
  trigger.focus()
  await user.keyboard('{Enter}')
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  const details = screen.getByRole('region', { name: 'Quota details' })
  expect(details).toHaveClass('overflow-y-auto', 'max-h-[min(36dvh,18rem)]')
  expect(details).toHaveAttribute('tabindex', '0')
  expect(
    await within(details).findByText(/Included quota reset/)
  ).toBeInTheDocument()
  expect(
    within(details).getByRole('progressbar', { name: 'Other Models' })
  ).toHaveAttribute('aria-valuenow', '100')
  await user.keyboard(' ')
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  expect(trigger).toHaveFocus()
  expect(
    screen.queryByRole('region', { name: 'Quota details' })
  ).not.toBeInTheDocument()
})

test('switching the model updates collapsed usage and scoped cooldown without retaining the previous pool', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [account()] },
  })
  const view = await mount(false)
  await screen.findByText('Used 2.7%')
  view.rerender(
    <CursorQuotaPanel group='default' model='claude' scope='Other Models' />
  )
  expect(await screen.findByText('Used 100%')).toBeInTheDocument()
  expect(screen.getByText(/Cooling until/)).toBeInTheDocument()
  expect(screen.queryByText('Cursor Models')).not.toBeInTheDocument()
  expect(screen.queryByText('Included quota remains')).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Show quota details' })
  ).toHaveAttribute('aria-expanded', 'false')
})

test.each([
  [
    'authentication failure',
    (data: CursorAccountStatus) => {
      data.health.disabled = true
    },
    'Cursor account authentication failed',
  ],
  [
    'account-wide cooldown',
    (data: CursorAccountStatus) => {
      data.health.cooldowns[0].scope = 'account'
    },
    'Cooling until',
  ],
  [
    'recovery awaiting a probe',
    (data: CursorAccountStatus) => {
      data.health.cooldowns = [
        {
          scope: 'Cursor Models',
          retry_at: '2000-01-01T00:00:00Z',
          state: 'half_open',
        },
      ]
    },
    'Awaiting recovery probe',
  ],
  [
    'concurrency at capacity',
    (data: CursorAccountStatus) => {
      data.health.in_flight = 2
    },
    'Cursor account concurrency limit reached',
  ],
  [
    'recent failure in the selected pool',
    (data: CursorAccountStatus) => {
      data.health.last_requests = [
        {
          scope: 'Cursor Models',
          status: 502,
          observed_at: new Date().toISOString(),
        },
      ]
    },
    'Last request failed',
  ],
  [
    'recent success in the selected pool',
    (data: CursorAccountStatus) => {
      data.health.last_requests = [
        {
          scope: 'Cursor Models',
          status: 200,
          observed_at: new Date().toISOString(),
        },
      ]
    },
    'Last request succeeded',
  ],
  [
    'expired quota with an old success',
    (data: CursorAccountStatus) => {
      if (data.quota) data.quota.expires_at = '2000-01-01T00:00:00Z'
      data.health.last_requests = [
        {
          scope: 'Cursor Models',
          status: 200,
          observed_at: '2000-01-01T00:00:00Z',
        },
      ]
    },
    'Actual availability unknown',
  ],
] as const)(
  'collapsed summary exposes %s without opening details',
  async (_name, change, expected) => {
    const data = account()
    change(data)
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: [data] },
    })
    await mount(false)
    expect(await screen.findByText(new RegExp(expected))).toBeInTheDocument()
    expect(screen.queryByText('Included quota remains')).not.toBeInTheDocument()
  }
)

test.each([
  ['remaining', 'enabled', 'Included quota exhausted; paid headroom remains'],
  ['exhausted', 'enabled', 'No reported budget available'],
  ['disabled', 'disabled', 'No reported budget available'],
  ['unknown', 'not_queried', 'Included quota exhausted; availability unknown'],
])(
  'exhausted selected pool with %s paid budget shows a qualified availability status',
  async (state, setting, expected) => {
    const data = account()
    if (!data.quota) throw new Error('missing quota fixture')
    data.quota.windows[0].used_percent = 100
    data.quota.on_demand = setting
    data.quota.on_demand_budget = { state, limits: [] }
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: [data] },
    })
    await mount(false)
    expect(await screen.findByText(expected)).toBeInTheDocument()
    expect(screen.getByText('Used 100%')).toBeInTheDocument()
    expect(
      screen.queryByText('Cursor account authentication failed')
    ).not.toBeInTheDocument()
  }
)

test('collapsed refresh failure discards old usage and retry restores it without expanding', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: [account()] } })
  await mount(false)
  await screen.findByText('Used 2.7%')
  get.mockRejectedValueOnce(new Error('network failed'))
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Refresh status' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Actual availability unknown'
  )
  expect(screen.queryByText('Used 2.7%')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Refresh status' }))
  expect(await screen.findByText('Used 2.7%')).toBeInTheDocument()
  expect(
    screen.queryByRole('region', { name: 'Quota details' })
  ).not.toBeInTheDocument()
})

test('multiple collapsed accounts keep their own usage and scoped health instead of an aggregate', async () => {
  const first = account()
  const second = account()
  second.route = '2'
  if (second.quota) second.quota.windows[0].used_percent = 40
  second.health.disabled = true
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [first, second] },
  })
  await mount(false)
  expect(await screen.findByText('Route account 1')).toBeInTheDocument()
  expect(screen.getByText('Route account 2')).toBeInTheDocument()
  expect(screen.getByText('Used 2.7%')).toBeInTheDocument()
  expect(screen.getByText('Used 40%')).toBeInTheDocument()
  expect(
    screen.getByText('Cursor account authentication failed')
  ).toBeInTheDocument()
})

test('missing quota and cooldown data remain unknown in a collapsed summary', async () => {
  const data = account()
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [
        {
          ...data,
          quota: null,
          error: 'unavailable',
          health: { ...data.health, cooldowns: null },
        },
      ],
    },
  })
  await mount(false)
  expect(await screen.findByText('Unknown usage')).toBeInTheDocument()
  expect(screen.getByText('Actual availability unknown')).toBeInTheDocument()
  expect(screen.queryByText('Included quota remains')).not.toBeInTheDocument()
})
