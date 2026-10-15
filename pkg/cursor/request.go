package cursor

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// This first native implementation deliberately accepts text and caller-owned
// function tools only. Unsupported controls must not silently change meaning.
type chatRequest struct {
	Model           string  `json:"model"`
	Stream          *bool   `json:"stream,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	Cursor          *struct {
		Parameters map[string]string `json:"parameters,omitempty"`
		Fast       *bool             `json:"fast,omitempty"`
		Context    *string           `json:"context,omitempty"`
	} `json:"cursor,omitempty"`
	selection modelSelection
	Messages  []struct {
		Role       string            `json:"role"`
		Content    common.RawMessage `json:"content"`
		ToolCallID string            `json:"tool_call_id"`
		ToolCalls  []toolCall        `json:"tool_calls"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Parameters  common.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type bridgeTool struct {
	Name, Description string
	InputSchema       common.RawMessage
}
type Usage struct{ Input, Output, CacheRead, CacheWrite, Reasoning int }
type Event struct {
	Kind                 int
	Status               int
	Text, ID, Name, Stop string
	ErrorCode            string
	Usage                Usage
}

const (
	KText = iota
	KThink
	KToolStart
	KToolArgs
	KUsage
	KStop
	KError
)

func parseRequest(body []byte) (*chatRequest, [][]byte, []bridgeTool, string, error) {
	var raw map[string]common.RawMessage
	var r chatRequest
	if common.Unmarshal(body, &raw) != nil || common.Unmarshal(body, &r) != nil {
		return nil, nil, nil, "", errors.New("invalid Cursor chat request")
	}
	for _, name := range []string{"temperature", "top_p", "frequency_penalty", "presence_penalty", "max_tokens", "max_completion_tokens", "stop", "seed", "response_format", "functions", "function_call", "logprobs", "top_logprobs", "fast", "context"} {
		if v, ok := raw[name]; ok && string(v) != "null" {
			return nil, nil, nil, "", errors.New("Cursor native channel does not support " + name)
		}
	}
	if v, ok := raw["n"]; ok && string(v) != "null" && string(v) != "1" {
		return nil, nil, nil, "", errors.New("Cursor native channel supports n=1 only")
	}
	if v, ok := raw["tool_choice"]; ok && string(v) != "null" && string(v) != `"auto"` {
		return nil, nil, nil, "", errors.New("Cursor native channel supports automatic tool choice only")
	}
	if options, ok := raw["cursor"]; ok && string(options) != "null" {
		var values map[string]common.RawMessage
		if common.Unmarshal(options, &values) != nil {
			return nil, nil, nil, "", errors.New("cursor must contain fast and/or context options")
		}
		for name := range values {
			if name != "fast" && name != "context" && name != "parameters" {
				return nil, nil, nil, "", errors.New("unsupported Cursor option: " + name)
			}
		}
	}
	if len(r.Messages) == 0 || len(r.Messages) > 1024 || len(r.Tools) > 128 {
		return nil, nil, nil, "", errors.New("invalid Cursor message or tool count")
	}
	var tools []bridgeTool
	for _, t := range r.Tools {
		var schema map[string]any
		if t.Type != "function" || t.Function.Name == "" || len(t.Function.Name) > 128 || common.Unmarshal(t.Function.Parameters, &schema) != nil || schema == nil {
			return nil, nil, nil, "", errors.New("invalid Cursor function tool")
		}
		tools = append(tools, bridgeTool{t.Function.Name, t.Function.Description, t.Function.Parameters})
	}
	var messages [][]byte
	add := func(v any) { b, _ := common.Marshal(v); messages = append(messages, b) }
	var system strings.Builder
	var pending []string
	var results []any
	flush := func() {
		for _, id := range pending {
			results = append(results, cursorResult(id, "Tool execution was interrupted; no result is available.", true))
		}
		if len(results) > 0 {
			add(map[string]any{"role": "tool", "content": results})
		}
		pending = nil
		results = nil
	}
	lastUser := "."
	for _, m := range r.Messages {
		var content string
		if len(m.Content) > 0 && string(m.Content) != "null" {
			if common.Unmarshal(m.Content, &content) != nil {
				var parts []struct{ Type, Text string }
				if common.Unmarshal(m.Content, &parts) != nil {
					return nil, nil, nil, "", errors.New("Cursor native channel accepts text content only")
				}
				for _, p := range parts {
					if p.Type != "text" {
						return nil, nil, nil, "", errors.New("Cursor native channel accepts text content only")
					}
					content += p.Text
				}
			}
		}
		switch m.Role {
		case "system", "developer":
			system.WriteString(content)
			system.WriteString("\n")
		case "tool":
			found := false
			for i, id := range pending {
				if id == m.ToolCallID {
					pending = append(pending[:i], pending[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				return nil, nil, nil, "", errors.New("Cursor tool result has no matching call")
			}
			results = append(results, cursorResult(m.ToolCallID, content, false))
		case "user":
			flush()
			add(map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": content}}})
			lastUser = content
		case "assistant":
			flush()
			parts := []any{}
			if content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": content})
			}
			for _, t := range m.ToolCalls {
				var args map[string]any
				if t.ID == "" || t.Type != "function" || common.UnmarshalJsonStr(t.Function.Arguments, &args) != nil || args == nil {
					return nil, nil, nil, "", errors.New("invalid Cursor tool-call history")
				}
				parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": cursorCallID(t.ID), "toolName": cursorCall, "args": map[string]any{"namespace": "magpie", "toolName": t.Function.Name, "arguments": args}})
				pending = append(pending, t.ID)
			}
			add(map[string]any{"role": "assistant", "content": parts})
		default:
			return nil, nil, nil, "", errors.New("unsupported Cursor message role")
		}
	}
	flush()
	for _, t := range tools {
		system.WriteString("\nAvailable MCP tool in namespace magpie: " + t.Name + "\n" + t.Description + "\nSchema: " + string(t.InputSchema) + "\nCall using CallDynamicTool with namespace, toolName, arguments.\n")
	}
	if system.Len() > 0 {
		b, _ := common.Marshal(map[string]any{"role": "system", "content": system.String()})
		messages = append([][]byte{b}, messages...)
	}
	return &r, messages, tools, lastUser, nil
}

func cursorCallID(id string) string {
	if strings.HasPrefix(id, "call_") {
		return strings.Replace(id, "__fc_", "\nfc_", 1)
	}
	return id
}

func cursorResult(id, text string, isError bool) map[string]any {
	var result any = text
	var parsed any
	if common.UnmarshalJsonStr(text, &parsed) == nil {
		result = parsed
	}
	return map[string]any{"type": "tool-result", "toolCallId": cursorCallID(id), "toolName": cursorCall, "result": result, "isError": isError, "experimental_content": []any{map[string]any{"type": "text", "text": text}}}
}

func cursorUsage(fields []pbField) (Usage, error) {
	// Bounds keep malformed uint64 counts from overflowing int or downstream sums.
	for _, f := range fields {
		if f.num >= 1 && f.num <= 5 && f.n > 1_000_000_000 {
			return Usage{}, errors.New("oversized token usage")
		}
	}
	in, out, cr, cw, reason := pbNum(fields, 1), pbNum(fields, 2), pbNum(fields, 3), pbNum(fields, 4), pbNum(fields, 5)
	if cr+cw > in || reason > out {
		return Usage{}, errors.New("inconsistent token usage")
	}
	return Usage{int(in - cr - cw), int(out), int(cr), int(cw), int(reason)}, nil
}
