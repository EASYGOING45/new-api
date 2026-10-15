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
// Message types
export type MessageRole = 'user' | 'assistant' | 'system'

export type MessageStatus = 'loading' | 'streaming' | 'complete' | 'error'

export type PlaygroundMessageLayoutMode = 'alternating' | 'left'

export interface MessageVersion {
  id: string
  content: string
}

export interface Message {
  key: string
  from: MessageRole
  versions: MessageVersion[]
  createdAt?: number
  startedAt?: number
  completedAt?: number
  durationMs?: number
  sources?: { href: string; title: string }[]
  reasoning?: {
    content: string
    duration: number
    startedAt?: number
    completedAt?: number
    durationMs?: number
  }
  isReasoningStreaming?: boolean
  isReasoningComplete?: boolean
  isContentComplete?: boolean
  status?: MessageStatus
  errorCode?: string | null
}

// API payload types
export interface ChatCompletionMessage {
  role: MessageRole
  content: string | ContentPart[]
}

export interface ContentPart {
  type: 'text' | 'image_url'
  text?: string
  image_url?: {
    url: string
  }
}

export interface ChatCompletionRequest {
  model: string
  group?: string
  messages: ChatCompletionMessage[]
  stream: boolean
  temperature?: number
  top_p?: number
  max_tokens?: number
  frequency_penalty?: number
  presence_penalty?: number
  seed?: number
  reasoning_effort?: string
  cursor?: { parameters: Record<string, string> }
}

export interface ChatCompletionChunk {
  id: string
  object: string
  created: number
  model: string
  choices: Array<{
    index: number
    delta: {
      role?: MessageRole
      content?: string
      reasoning_content?: string
    }
    finish_reason: string | null
  }>
}

export interface ChatCompletionResponse {
  id: string
  object: string
  created: number
  model: string
  choices: Array<{
    index: number
    message: {
      role: MessageRole
      content: string
      reasoning_content?: string
    }
    finish_reason: string
  }>
  usage?: {
    prompt_tokens: number
    completion_tokens: number
    total_tokens: number
  }
}

// Configuration types
export interface PlaygroundConfig {
  model: string
  group: string
  temperature: number
  top_p: number
  max_tokens: number
  frequency_penalty: number
  presence_penalty: number
  seed: number | null
  stream: boolean
  model_provider?: string
  cursor_capabilities?: CursorModelCapabilities
  cursor_parameters?: Record<string, string>
}

export interface ParameterEnabled {
  temperature: boolean
  top_p: boolean
  max_tokens: boolean
  frequency_penalty: boolean
  presence_penalty: boolean
  seed: boolean
}

export interface CursorModelCapabilities {
  budget_scope?: string
  id: string
  provider: string
  base_id: string
  parameters: Array<{
    id: string
    name: string
    values: Array<{
      value: string
      display_name: string
      increases_cost?: boolean
    }>
  }>
  variants: Array<{
    id: string
    parameters: Record<string, string>
    max_mode: boolean
  }>
  defaults: Record<string, string>
  default_source: string
  context_tokens?: number
  observed_at: string
  error?: string
}

// Model and group options
export interface ModelOption {
  label: string
  value: string
  provider?: string
  cursor?: CursorModelCapabilities
}

export interface GroupOption {
  label: string
  value: string
  ratio: number | string
  desc?: string
}

export interface CursorAccountStatus {
  route: string
  quota: {
    observed_at: string
    expires_at: string
    windows: { name: string; used_percent: number }[]
    billing_cycle_end_ms: string
    on_demand: string
    on_demand_budget?: {
      state: string
      limits: {
        scope: string
        used_usd: number | null
        limit_usd: number | null
        remaining_usd: number | null
        state: string
      }[]
    }
    resets_at?: string | null
    reset_state?: string
  } | null
  health: {
    disabled: boolean
    in_flight: number
    max_in_flight: number
    cooldowns: { scope: string; retry_at: string; state: string }[]
    last_requests?:
      | { scope: string; observed_at: string; status: number }[]
      | null
  }
  error?: string
}
