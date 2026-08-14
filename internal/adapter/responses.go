package adapter

import "encoding/json"

// OpenAI Responses API inbound types. Only the fields the adapter needs are
// modeled; unknown fields are ignored.

type ResponsesRequest struct {
	Model             string              `json:"model"`
	Input             json.RawMessage     `json:"input"` // string or []InputItem
	Instructions      string              `json:"instructions"`
	Tools             []json.RawMessage   `json:"tools"`
	ToolChoice        json.RawMessage     `json:"tool_choice"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls"`
	Reasoning         *ResponsesReasoning `json:"reasoning"`
	MaxOutputTokens   int                 `json:"max_output_tokens"`
	Temperature       *float64            `json:"temperature"`
	TopP              *float64            `json:"top_p"`
	Stream            bool                `json:"stream"`
	Store             *bool               `json:"store"`
	Include           []string            `json:"include"`
}

type ResponsesReasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary"`
}

// InputItem is a union over the Responses input item kinds.
type InputItem struct {
	Type string `json:"type"` // message | reasoning | function_call | function_call_output | web_search_call | ...
	Role string `json:"role"` // for type=message

	Content json.RawMessage `json:"content"` // string or []ContentPart

	// reasoning
	Summary          []SummaryPart `json:"summary"`
	EncryptedContent string        `json:"encrypted_content"`

	// function_call
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`

	// function_call_output
	Output json.RawMessage `json:"output"` // string or []ContentPart
}

type SummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ContentPart struct {
	Type     string          `json:"type"` // input_text | input_image | output_text | ...
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"` // string or {"url": ...}
}
