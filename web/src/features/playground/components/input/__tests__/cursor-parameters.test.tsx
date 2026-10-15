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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState } from 'react'
import { expect, test } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../../../constants'
import { playgroundConfigSchema } from '../../../lib/storage/storage-schema'
import { buildChatCompletionPayload } from '../../../lib/streaming/payload-builder'
import type { CursorModelCapabilities, PlaygroundConfig } from '../../../types'
import { PlaygroundParameterPanel } from '../playground-parameter-panel'

const capabilities: CursorModelCapabilities = {
  id: 'claude-fixture',
  base_id: 'claude-fixture',
  provider: 'anthropic',
  observed_at: '2026-10-15T00:00:00Z',
  default_source: 'upstream_non_max',
  defaults: { effort: 'high', fast: 'true', context: '300k' },
  parameters: [
    {
      id: 'effort',
      name: 'Effort',
      values: ['high', 'max'].map((value) => ({ value, display_name: value })),
    },
    {
      id: 'fast',
      name: 'Fast',
      values: ['false', 'true'].map((value) => ({
        value,
        display_name: value,
      })),
    },
    {
      id: 'context',
      name: 'Context',
      values: ['300k', '1m'].map((value) => ({ value, display_name: value })),
    },
  ],
  variants: ['high', 'max']
    .flatMap((effort) =>
      ['false', 'true'].flatMap((fast) =>
        ['300k', '1m'].map((context) => ({
          id: `claude-fixture-${effort}`,
          parameters: { effort, fast, context },
          max_mode: context === '1m',
        }))
      )
    )
    .filter(
      (v) => !(v.parameters.effort === 'max' && v.parameters.fast === 'true')
    ),
}
const baseConfig: PlaygroundConfig = {
  ...DEFAULT_CONFIG,
  model: capabilities.id,
  model_provider: 'anthropic',
  cursor_capabilities: capabilities,
}
const enabled = { ...DEFAULT_PARAMETER_ENABLED, temperature: true }

function Harness({ initial = baseConfig }: { initial?: PlaygroundConfig }) {
  const [config, setConfig] = useState(initial)
  return (
    <>
      <PlaygroundParameterPanel
        config={config}
        parameterEnabled={enabled}
        onConfigChange={(key, value) =>
          setConfig((previous) => ({ ...previous, [key]: value }))
        }
        onParameterEnabledChange={() => undefined}
      />
      <output data-testid='payload'>
        {JSON.stringify(buildChatCompletionPayload([], config, enabled))}
      </output>
    </>
  )
}
function payload() {
  return JSON.parse(screen.getByTestId('payload').textContent ?? '{}')
}

test('dynamic vendor and options send native effort, false and context with valid combinations', async () => {
  const user = userEvent.setup()
  render(<Harness />)
  await user.click(screen.getByRole('button', { name: 'Parameters' }))
  expect(screen.getByText('anthropic / claude-fixture')).toBeInTheDocument()
  expect(payload()).toMatchObject({
    cursor: { parameters: capabilities.defaults },
  })
  await user.click(screen.getByRole('combobox', { name: 'Reasoning effort' }))
  expect(screen.getByRole('option', { name: 'max' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('switch', { name: 'Fast mode' }))
  await user.click(screen.getByRole('combobox', { name: 'Reasoning effort' }))
  await user.click(screen.getByRole('option', { name: 'max' }))
  await user.click(screen.getByRole('combobox', { name: 'Context window' }))
  await user.click(screen.getByRole('option', { name: '1m' }))
  expect(payload()).toMatchObject({
    cursor: { parameters: { effort: 'max', fast: 'false', context: '1m' } },
  })
  expect(payload()).not.toHaveProperty('temperature')
  expect(
    playgroundConfigSchema.parse({
      ...baseConfig,
      cursor_parameters: { fast: 'false' },
    }).cursor_parameters
  ).toEqual({ fast: 'false' })
  expect(playgroundConfigSchema.parse(baseConfig)).not.toHaveProperty(
    'cursor_capabilities'
  )
})

test('model catalog with only fast does not invent effort or context controls', async () => {
  const user = userEvent.setup()
  render(
    <Harness
      initial={{
        ...baseConfig,
        model: 'composer-fixture',
        cursor_capabilities: {
          ...capabilities,
          id: 'composer-fixture',
          context_tokens: 200000,
          defaults: { fast: 'false' },
          parameters: capabilities.parameters.filter(
            (parameter) => parameter.id === 'fast'
          ),
          variants: [
            {
              id: 'composer-fixture',
              parameters: { fast: 'false' },
              max_mode: false,
            },
            {
              id: 'composer-fixture-fast',
              parameters: { fast: 'true' },
              max_mode: false,
            },
          ],
        },
      }}
    />
  )
  await user.click(screen.getByRole('button', { name: 'Parameters' }))
  expect(
    screen.queryByRole('combobox', { name: 'Reasoning effort' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('combobox', { name: 'Context window' })
  ).not.toBeInTheDocument()
  expect(payload().cursor.parameters).toEqual({ fast: 'false' })
})

test('unavailable catalog shows an error and does not restore generic sampling controls', async () => {
  const user = userEvent.setup()
  render(
    <Harness
      initial={{
        ...baseConfig,
        cursor_capabilities: { ...capabilities, error: 'unavailable' },
      }}
    />
  )
  await user.click(screen.getByRole('button', { name: 'Parameters' }))
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Cursor model options are unavailable'
  )
  expect(payload()).not.toHaveProperty('temperature')
  expect(
    screen.queryByRole('switch', { name: 'Fast mode' })
  ).not.toBeInTheDocument()
})

test('ordinary provider keeps sampling controls for the same model ID', async () => {
  const user = userEvent.setup()
  render(
    <Harness initial={{ ...baseConfig, cursor_capabilities: undefined }} />
  )
  await user.click(screen.getByRole('button', { name: 'Parameters' }))
  expect(
    screen.queryByRole('switch', { name: 'Fast mode' })
  ).not.toBeInTheDocument()
  expect(payload()).toHaveProperty('temperature', baseConfig.temperature)
  expect(payload()).not.toHaveProperty('cursor')
})

test('dynamic option labels work in the Chinese locale', async () => {
  i18next.addResourceBundle('zh', 'translation', zh.translation)
  await i18next.changeLanguage('zh')
  try {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByRole('button', { name: '参数' }))
    expect(
      screen.getByRole('combobox', { name: '推理强度' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('switch', { name: 'Fast 模式' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('combobox', { name: '上下文窗口' })
    ).toBeInTheDocument()
    expect(screen.getByText('Cursor 模型参数')).toBeInTheDocument()
  } finally {
    await i18next.changeLanguage('en')
  }
})
