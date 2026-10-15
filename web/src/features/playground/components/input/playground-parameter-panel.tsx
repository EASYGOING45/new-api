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
import { SlidersHorizontalIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { PromptInputButton } from '@/components/ai-elements/prompt-input'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { Slider } from '@/components/ui/slider'
import { Switch } from '@/components/ui/switch'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useIsMobile } from '@/hooks/use-mobile'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  getParameterControlValueText,
  getCursorModelOptions,
  isCursorParameterValueAvailable,
  normalizeParameterNumberValue,
  PLAYGROUND_PARAMETER_CONTROLS,
  PLAYGROUND_PARAMETER_PANEL_SCROLL_CLASS,
  type PlaygroundParameterKey,
} from '../../lib/parameters/playground-parameters'
import type { ParameterEnabled, PlaygroundConfig } from '../../types'

type PlaygroundParameterPanelProps = {
  config: PlaygroundConfig
  disabled?: boolean
  onConfigChange: <K extends keyof PlaygroundConfig>(
    key: K,
    value: PlaygroundConfig[K]
  ) => void
  onParameterEnabledChange: (
    key: PlaygroundParameterKey,
    value: boolean
  ) => void
  parameterEnabled: ParameterEnabled
}

type PlaygroundParameterContentProps = PlaygroundParameterPanelProps & {
  compact?: boolean
}

function PlaygroundParameterContent({
  compact = false,
  config,
  disabled,
  onConfigChange,
  onParameterEnabledChange,
  parameterEnabled,
}: PlaygroundParameterContentProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const cursor = getCursorModelOptions(config)

  const updateParameterConfig = (
    key: PlaygroundParameterKey,
    value: number | null
  ) => {
    if (key === 'seed') {
      onConfigChange('seed', value)
      return
    }

    onConfigChange(key, value ?? 0)
  }

  return (
    <div
      className={cn(
        'grid gap-3',
        PLAYGROUND_PARAMETER_PANEL_SCROLL_CLASS,
        compact ? 'px-4 pb-4' : 'p-1'
      )}
    >
      {cursor ? (
        <>
          <div className='space-y-1'>
            <p className='text-sm font-medium'>
              {config.model_provider} / {config.model}
            </p>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Options come from the current Cursor account catalog. Some options use more allowance.'
              )}
            </p>
            {cursor.capabilities.error ? (
              <p role='alert' className='text-destructive text-xs'>
                {t(
                  'Cursor model options are unavailable. Refresh the catalog or check the channel.'
                )}
              </p>
            ) : (
              <p className='text-muted-foreground text-xs'>
                {t('Catalog updated')}:{' '}
                {new Date(cursor.capabilities.observed_at).toLocaleString(
                  locale
                )}
              </p>
            )}
            {!cursor.capabilities.error &&
              !cursor.capabilities.parameters.some(
                (parameter) => parameter.id === 'context'
              ) &&
              Boolean(cursor.capabilities.context_tokens) && (
                <p className='text-muted-foreground text-xs'>
                  {t('Context window')}:{' '}
                  {formatNumber(
                    cursor.capabilities.context_tokens ?? 0,
                    locale
                  )}{' '}
                  tokens
                </p>
              )}
          </div>
          {!cursor.capabilities.error &&
            cursor.capabilities.parameters.map((parameter) => {
              const controlId = `cursor-${parameter.id}`
              let label = parameter.name || parameter.id
              if (parameter.id === 'fast') {
                label = t('Fast mode')
              } else if (parameter.id === 'context') {
                label = t('Context window')
              } else if (
                ['effort', 'reasoning', 'reasoning_effort'].includes(
                  parameter.id
                )
              ) {
                label = t('Reasoning effort')
              }
              const isBoolean =
                parameter.values.length === 2 &&
                parameter.values.every((value) =>
                  ['true', 'false'].includes(value.value)
                )
              const selected = cursor.parameters[parameter.id] ?? ''
              const update = (value: string) =>
                onConfigChange('cursor_parameters', {
                  ...cursor.parameters,
                  [parameter.id]: value,
                })
              return (
                <div
                  key={parameter.id}
                  className={
                    isBoolean
                      ? 'flex items-center justify-between gap-3'
                      : 'grid gap-2'
                  }
                >
                  <label htmlFor={controlId}>{label}</label>
                  {isBoolean ? (
                    <Switch
                      id={controlId}
                      checked={selected === 'true'}
                      disabled={
                        disabled ||
                        !isCursorParameterValueAvailable(
                          config,
                          parameter.id,
                          selected === 'true' ? 'false' : 'true'
                        )
                      }
                      onCheckedChange={(value) => update(String(value))}
                    />
                  ) : (
                    <Select
                      value={selected}
                      disabled={disabled || parameter.values.length < 2}
                      onValueChange={(value) => {
                        if (value !== null) update(value)
                      }}
                    >
                      <SelectTrigger id={controlId} className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {parameter.values.map((value) => (
                          <SelectItem
                            key={value.value}
                            value={value.value}
                            disabled={
                              !isCursorParameterValueAvailable(
                                config,
                                parameter.id,
                                value.value
                              )
                            }
                          >
                            {value.display_name || value.value}
                            {value.increases_cost ? ' ↑' : ''}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </div>
              )
            })}
        </>
      ) : (
        PLAYGROUND_PARAMETER_CONTROLS.map((control) => {
          const enabled = parameterEnabled[control.key]
          const value = config[control.key]
          const controlId = `playground-${control.key}`

          return (
            <div
              className={cn(
                'border-border/70 bg-background/60 grid gap-2 rounded-lg border p-3 transition-opacity',
                (!enabled || disabled) && 'opacity-55'
              )}
              key={control.key}
            >
              <div className='flex items-start justify-between gap-3'>
                <div className='min-w-0 space-y-1'>
                  <div className='flex min-w-0 items-center gap-2'>
                    <label
                      className='truncate text-sm leading-5 font-medium'
                      htmlFor={controlId}
                    >
                      {t(control.labelKey)}
                    </label>
                    <Badge
                      className='h-5 max-w-24 shrink-0 px-1.5 font-mono text-[11px]'
                      variant='outline'
                    >
                      {t(getParameterControlValueText(control.key, value))}
                    </Badge>
                  </div>
                  <p className='text-muted-foreground text-xs leading-4'>
                    {t(control.descriptionKey)}
                  </p>
                </div>

                <Switch
                  aria-label={t('Enable {{parameter}}', {
                    parameter: t(control.labelKey),
                  })}
                  checked={enabled}
                  disabled={disabled}
                  onCheckedChange={(checked) =>
                    onParameterEnabledChange(control.key, checked)
                  }
                  size='sm'
                />
              </div>

              {control.valueType === 'slider' ? (
                <Slider
                  className='py-1.5'
                  disabled={disabled || !enabled}
                  id={controlId}
                  max={control.max}
                  min={control.min}
                  onValueChange={(nextValue) => {
                    const firstValue = Array.isArray(nextValue)
                      ? nextValue[0]
                      : nextValue
                    updateParameterConfig(
                      control.key,
                      normalizeParameterNumberValue(control.key, firstValue)
                    )
                  }}
                  step={control.step}
                  value={[Number(value)]}
                />
              ) : (
                <Input
                  disabled={disabled || !enabled}
                  id={controlId}
                  inputMode='numeric'
                  max={control.max}
                  min={control.min}
                  onChange={(event) => {
                    updateParameterConfig(
                      control.key,
                      normalizeParameterNumberValue(
                        control.key,
                        event.target.value
                      )
                    )
                  }}
                  step={control.step}
                  type='number'
                  value={value ?? ''}
                />
              )}
            </div>
          )
        })
      )}
    </div>
  )
}

export function PlaygroundParameterPanel(props: PlaygroundParameterPanelProps) {
  const { t } = useTranslation()
  const isMobile = useIsMobile()
  const activeCount = getCursorModelOptions(props.config)
    ? 3
    : PLAYGROUND_PARAMETER_CONTROLS.filter(
        (control) => props.parameterEnabled[control.key]
      ).length

  const trigger = (
    <PromptInputButton
      aria-label={t('Parameters')}
      className='text-muted-foreground hover:text-foreground hover:bg-muted/70 relative font-medium'
      disabled={props.disabled}
      variant='ghost'
    >
      <SlidersHorizontalIcon size={16} />
      <span className='bg-primary text-primary-foreground absolute -top-1 -right-1 flex h-3.5 min-w-3.5 items-center justify-center rounded-full px-1 text-[9px] leading-none font-semibold'>
        {activeCount}
      </span>
    </PromptInputButton>
  )

  if (isMobile) {
    return (
      <Sheet>
        <Tooltip>
          <TooltipTrigger render={<SheetTrigger render={trigger} />} />
          <TooltipContent>
            <p>{t('Parameters')}</p>
          </TooltipContent>
        </Tooltip>
        <SheetContent
          className='max-h-[85vh] overflow-hidden rounded-t-xl'
          side='bottom'
        >
          <SheetHeader>
            <SheetTitle>{t('Parameter settings')}</SheetTitle>
          </SheetHeader>
          <PlaygroundParameterContent {...props} compact />
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <Popover>
      <Tooltip>
        <TooltipTrigger render={<PopoverTrigger render={trigger} />} />
        <TooltipContent>
          <p>{t('Parameters')}</p>
        </TooltipContent>
      </Tooltip>
      <PopoverContent
        align='start'
        className='w-[22rem] max-w-[calc(100vw-2rem)] gap-3 p-3'
        collisionPadding={8}
        side='top'
        sideOffset={8}
      >
        <div className='space-y-1 px-1'>
          <div className='text-sm font-semibold'>{t('Parameter settings')}</div>
          <div className='text-muted-foreground text-xs leading-4'>
            {getCursorModelOptions(props.config)
              ? t('Cursor model parameters')
              : t('Only enabled parameters are sent with the request.')}
          </div>
        </div>
        <PlaygroundParameterContent {...props} />
      </PopoverContent>
    </Popover>
  )
}
