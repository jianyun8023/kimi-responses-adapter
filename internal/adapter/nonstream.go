package adapter

import (
	"encoding/json"
	"strings"
	"time"
)

// anthropicToResponse converts a non-streaming Anthropic message into an
// OpenAI Responses response object.
func anthropicToResponse(cfg *Config, req *ResponsesRequest, msg *AnthropicMessageObj) map[string]any {
	var output []any
	var pendingSearchID string
	var pendingQuery string

	flushSearch := func(sources []any) {
		if pendingSearchID == "" {
			return
		}
		action := map[string]any{"type": "search"}
		if pendingQuery != "" {
			action["query"] = pendingQuery
		}
		if len(sources) > 0 {
			action["sources"] = sources
		}
		output = append(output, map[string]any{
			"id": pendingSearchID, "type": "web_search_call", "status": "completed", "action": action,
		})
		pendingSearchID = ""
		pendingQuery = ""
	}

	for i := range msg.Content {
		b := &msg.Content[i]
		switch b.Type {
		case "text":
			// Suppress Kimi's web-search status preamble, same as streaming.
			if strings.HasPrefix(b.Text, cfg.SearchStatusPrefix) {
				continue
			}
			output = append(output, map[string]any{
				"id": randID("msg_"), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": b.Text, "annotations": []any{}}},
			})
		case "thinking":
			output = append(output, map[string]any{
				"id": randID("rs_"), "type": "reasoning",
				"summary":           []any{map[string]any{"type": "summary_text", "text": b.Thinking}},
				"encrypted_content": encodeReasoning(b.Thinking, b.Signature),
			})
		case "redacted_thinking":
			output = append(output, map[string]any{
				"id": randID("rs_"), "type": "reasoning", "summary": []any{},
				"encrypted_content": encodeRedactedReasoning(b.Data),
			})
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 && json.Valid(b.Input) {
				args = string(b.Input)
			}
			output = append(output, map[string]any{
				"id": randID("fc_"), "type": "function_call", "call_id": b.ID,
				"name": b.Name, "arguments": args, "status": "completed",
			})
		case "server_tool_use":
			pendingSearchID = randID("ws_")
			var input struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(b.Input, &input)
			pendingQuery = input.Query
		case "web_search_tool_result":
			flushSearch(extractSources(b.Content))
		}
	}
	flushSearch(nil)

	if output == nil {
		output = []any{}
	}

	status := "completed"
	resp := map[string]any{
		"id":                  randID("resp_"),
		"object":              "response",
		"created_at":          time.Now().Unix(),
		"status":              status,
		"error":               nil,
		"incomplete_details":  nil,
		"model":               req.Model,
		"output":              output,
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
		"metadata":            map[string]any{},
	}
	if msg.StopReason == "max_tokens" {
		resp["status"] = "incomplete"
		resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	if msg.Usage != nil {
		thinking := 0
		if msg.Usage.OutputTokensDetails != nil {
			thinking = msg.Usage.OutputTokensDetails.ThinkingTokens
		}
		resp["usage"] = usageMap(msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheCreationInputTokens, msg.Usage.CacheReadInputTokens, thinking)
	}
	return resp
}
