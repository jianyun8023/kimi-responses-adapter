package adapter

import "encoding/json"

// Anthropic-side types (Kimi Code Messages API).

type AnthropicRequest struct {
	Model       string               `json:"model"`
	MaxTokens   int                  `json:"max_tokens"`
	System      string               `json:"system,omitempty"`
	Messages    []AnthropicMessage   `json:"messages"`
	Tools       []AnthropicTool      `json:"tools,omitempty"`
	ToolChoice  *AnthropicToolChoice `json:"tool_choice,omitempty"`
	Thinking    *AnthropicThinking   `json:"thinking,omitempty"`
	Temperature *float64             `json:"temperature,omitempty"`
	TopP        *float64             `json:"top_p,omitempty"`
	Stream      bool                 `json:"stream,omitempty"`
}

type AnthropicMessage struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"content"`
}

// AnthropicContent is a union of all content block kinds used in both
// directions: text, image, thinking, redacted_thinking, tool_use,
// tool_result, server_tool_use, web_search_tool_result.
type AnthropicContent struct {
	Type string `json:"type"`

	Text      string       `json:"text,omitempty"`
	Source    *ImageSource `json:"source,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Signature string       `json:"signature,omitempty"`
	Data      string       `json:"data,omitempty"` // redacted_thinking payload

	ID        string          `json:"id,omitempty"`    // tool_use / server_tool_use
	Name      string          `json:"name,omitempty"`  // tool_use / server_tool_use
	Input     json.RawMessage `json:"input,omitempty"` // tool_use / server_tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   any             `json:"content,omitempty"` // tool_result / web_search_tool_result
}

type ImageSource struct {
	Type      string `json:"type"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type AnthropicTool struct {
	Type        string          `json:"type,omitempty"` // set for server tools, e.g. web_search_20250305
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	MaxUses     int             `json:"max_uses,omitempty"`
}

type AnthropicToolChoice struct {
	Type                   string `json:"type"` // auto | any | none | tool
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type AnthropicThinking struct {
	Type         string `json:"type"` // enabled | disabled
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type AnthropicUsage struct {
	InputTokens              int                          `json:"input_tokens"`
	OutputTokens             int                          `json:"output_tokens"`
	CacheCreationInputTokens int                          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int                          `json:"cache_read_input_tokens"`
	OutputTokensDetails      *AnthropicOutputTokenDetails `json:"output_tokens_details"`
}

type AnthropicOutputTokenDetails struct {
	ThinkingTokens int `json:"thinking_tokens"`
}

// AnthropicMessageObj is the non-streaming response / message_start payload.
type AnthropicMessageObj struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []AnthropicContent `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      *AnthropicUsage    `json:"usage"`
}

// AnthropicStreamEvent is the envelope for every SSE event from upstream.
type AnthropicStreamEvent struct {
	Type         string               `json:"type"`
	Index        int                  `json:"index"`
	Message      *AnthropicMessageObj `json:"message,omitempty"`
	ContentBlock *AnthropicContent    `json:"content_block,omitempty"`
	Delta        *AnthropicDelta      `json:"delta,omitempty"`
	Usage        *AnthropicUsage      `json:"usage,omitempty"`
	Error        *AnthropicError      `json:"error,omitempty"`
}

type AnthropicDelta struct {
	Type        string `json:"type"` // text_delta | thinking_delta | signature_delta | input_json_delta
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type AnthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
