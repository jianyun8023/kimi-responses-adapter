package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// buildAnthropicRequest converts an OpenAI Responses request into a Kimi
// Anthropic Messages request. The conversion is deterministic and stateless:
// all conversational state arrives in req.Input on every call.
//
// resolveModel optionally supplies per-model metadata (collected from the
// Kimi upstream). Default max_tokens precedence:
// client max_output_tokens > model metadata > cfg.MaxTokens.
func buildAnthropicRequest(cfg *Config, req *ResponsesRequest, resolveModel func(model string) (ModelInfo, bool)) (*AnthropicRequest, error) {
	model := req.Model
	if mapped, ok := cfg.ModelMap[model]; ok {
		model = mapped
	}

	out := &AnthropicRequest{
		Model:       model,
		MaxTokens:   req.MaxOutputTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = cfg.MaxTokens
		if resolveModel != nil {
			if info, ok := resolveModel(model); ok && info.MaxOutputTokens > 0 {
				out.MaxTokens = info.MaxOutputTokens
			}
		}
	}

	items, err := parseInput(req.Input)
	if err != nil {
		return nil, err
	}

	thinkingEnabled := configureThinking(cfg, req, out)
	if !thinkingEnabled && containsSignedReasoning(items) {
		// Signed thinking history requires thinking mode on
		// Anthropic-compatible upstreams; enable it even when the client
		// omitted reasoning.effort or asked for minimal (sub2api#5166).
		out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: cfg.ThinkingBudgets["medium"]}
		clampThinkingBudget(out)
		thinkingEnabled = true
	}
	if thinkingEnabled {
		// Anthropic rejects sampling overrides together with extended thinking.
		out.Temperature = nil
		out.TopP = nil
	}

	var systemParts []string
	if req.Instructions != "" {
		systemParts = append(systemParts, req.Instructions)
	}

	var msgs []AnthropicMessage
	push := func(role string, block AnthropicContent) {
		if len(msgs) == 0 || msgs[len(msgs)-1].Role != role {
			msgs = append(msgs, AnthropicMessage{Role: role})
		}
		msgs[len(msgs)-1].Content = append(msgs[len(msgs)-1].Content, block)
	}

	for i := range items {
		it := &items[i]
		typ := it.Type
		if typ == "" && it.Role != "" {
			typ = "message"
		}
		switch typ {
		case "message":
			switch it.Role {
			case "system", "developer":
				if s := contentText(it.Content); s != "" {
					systemParts = append(systemParts, s)
				}
			case "user":
				for _, b := range userContentBlocks(it.Content) {
					push("user", b)
				}
			case "assistant":
				for _, b := range assistantContentBlocks(it.Content) {
					push("assistant", b)
				}
			}
		case "reasoning":
			if !thinkingEnabled {
				continue
			}
			p, ok := decodeReasoning(it.EncryptedContent, summaryText(it.Summary))
			if !ok {
				continue
			}
			if p.Redacted != "" {
				push("assistant", AnthropicContent{Type: "redacted_thinking", Data: p.Redacted})
			} else {
				push("assistant", AnthropicContent{Type: "thinking", Thinking: p.Thinking, Signature: p.Signature})
			}
		case "function_call":
			input := json.RawMessage(it.Arguments)
			if len(input) == 0 || !json.Valid(input) {
				input = json.RawMessage(`{}`)
			}
			push("assistant", AnthropicContent{Type: "tool_use", ID: it.CallID, Name: it.Name, Input: input})
		case "function_call_output":
			push("user", AnthropicContent{Type: "tool_result", ToolUseID: it.CallID, Content: toolResultContent(it.Output)})
		case "web_search_call":
			// v1: server-side search history is not replayed upstream; the
			// assistant's synthesized answer remains in the transcript.
		}
	}

	if len(msgs) == 0 {
		return nil, errors.New("input produced no messages")
	}
	out.Messages = msgs
	out.System = strings.Join(systemParts, "\n\n")

	tools, err := convertTools(req.Tools)
	if err != nil {
		return nil, err
	}
	out.Tools = tools
	out.ToolChoice = convertToolChoice(req.ToolChoice, req.ParallelToolCalls)

	return out, nil
}

// configureThinking maps Responses reasoning effort onto an Anthropic
// thinking budget, clamped against max_tokens. Returns whether thinking is
// enabled.
func configureThinking(cfg *Config, req *ResponsesRequest, out *AnthropicRequest) bool {
	effort := "medium"
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		effort = strings.ToLower(req.Reasoning.Effort)
	}
	switch effort {
	case "none", "minimal":
		out.Thinking = &AnthropicThinking{Type: "disabled"}
		return false
	}
	budget, ok := cfg.ThinkingBudgets[effort]
	if !ok {
		budget = cfg.ThinkingBudgets["medium"]
	}
	out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: budget}
	clampThinkingBudget(out)
	return true
}

// clampThinkingBudget keeps the thinking budget strictly below max_tokens.
func clampThinkingBudget(out *AnthropicRequest) {
	if out.Thinking == nil || out.Thinking.Type != "enabled" {
		return
	}
	budget := out.Thinking.BudgetTokens
	if out.MaxTokens <= 2048 {
		out.Thinking = &AnthropicThinking{Type: "disabled"}
		return
	}
	if budget >= out.MaxTokens {
		budget = out.MaxTokens / 2
		if budget < 1024 {
			budget = 1024
		}
	}
	out.Thinking.BudgetTokens = budget
}

// containsSignedReasoning reports whether any input item carries a
// replayable thinking signature.
func containsSignedReasoning(items []InputItem) bool {
	for i := range items {
		if items[i].Type != "reasoning" {
			continue
		}
		if _, ok := decodeReasoning(items[i].EncryptedContent, summaryText(items[i].Summary)); ok {
			return true
		}
	}
	return false
}

func parseInput(raw json.RawMessage) ([]InputItem, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing input")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		part, _ := json.Marshal([]ContentPart{{Type: "input_text", Text: s}})
		return []InputItem{{Type: "message", Role: "user", Content: part}}, nil
	}
	var items []InputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	return items, nil
}

func parseContentParts(raw json.RawMessage) []ContentPart {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ContentPart{{Type: "input_text", Text: s}}
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	return parts
}

func contentText(raw json.RawMessage) string {
	var sb strings.Builder
	for _, p := range parseContentParts(raw) {
		if p.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func userContentBlocks(raw json.RawMessage) []AnthropicContent {
	var blocks []AnthropicContent
	for _, p := range parseContentParts(raw) {
		switch p.Type {
		case "input_text", "output_text", "text":
			if p.Text != "" {
				blocks = append(blocks, AnthropicContent{Type: "text", Text: p.Text})
			}
		case "input_image":
			if b := imageBlock(p.ImageURL); b != nil {
				blocks = append(blocks, *b)
			}
		}
	}
	return blocks
}

func assistantContentBlocks(raw json.RawMessage) []AnthropicContent {
	var blocks []AnthropicContent
	for _, p := range parseContentParts(raw) {
		if (p.Type == "output_text" || p.Type == "text" || p.Type == "input_text") && p.Text != "" {
			blocks = append(blocks, AnthropicContent{Type: "text", Text: p.Text})
		}
	}
	return blocks
}

func imageBlock(raw json.RawMessage) *AnthropicContent {
	if len(raw) == 0 {
		return nil
	}
	url := ""
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		url = s
	} else {
		var obj struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &obj); err == nil {
			url = obj.URL
		}
	}
	if url == "" {
		return nil
	}
	if mt, data, ok := parseDataURI(url); ok {
		return &AnthropicContent{Type: "image", Source: &ImageSource{Type: "base64", MediaType: mt, Data: data}}
	}
	return &AnthropicContent{Type: "image", Source: &ImageSource{Type: "url", URL: url}}
}

func parseDataURI(u string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(u, "data:") {
		return "", "", false
	}
	rest := u[len("data:"):]
	i := strings.IndexByte(rest, ',')
	if i < 0 {
		return "", "", false
	}
	meta := rest[:i]
	data = rest[i+1:]
	mediaType = strings.SplitN(meta, ";", 2)[0]
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return mediaType, data, true
}

func toolResultContent(raw json.RawMessage) any {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var blocks []AnthropicContent
		for _, p := range parts {
			if p.Text != "" {
				blocks = append(blocks, AnthropicContent{Type: "text", Text: p.Text})
			}
		}
		if len(blocks) > 0 {
			return blocks
		}
	}
	return string(raw)
}

func convertTools(raw []json.RawMessage) ([]AnthropicTool, error) {
	var tools []AnthropicTool
	for _, rt := range raw {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rt, &probe); err != nil {
			continue
		}
		switch probe.Type {
		case "function":
			var fn struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}
			if err := json.Unmarshal(rt, &fn); err != nil {
				return nil, fmt.Errorf("invalid function tool: %w", err)
			}
			if len(fn.Parameters) == 0 {
				fn.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, AnthropicTool{Name: fn.Name, Description: fn.Description, InputSchema: fn.Parameters})
		case "web_search", "web_search_preview", "web_search_preview_2025_03_11":
			tools = append(tools, AnthropicTool{Type: "web_search_20250305", Name: "web_search"})
		}
	}
	return tools, nil
}

func convertToolChoice(raw json.RawMessage, parallel *bool) *AnthropicToolChoice {
	var choice *AnthropicToolChoice
	if len(raw) > 0 {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			switch s {
			case "auto":
				choice = &AnthropicToolChoice{Type: "auto"}
			case "required":
				choice = &AnthropicToolChoice{Type: "any"}
			case "none":
				choice = &AnthropicToolChoice{Type: "none"}
			}
		} else {
			var obj struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &obj); err == nil && (obj.Type == "function" || obj.Type == "tool") && obj.Name != "" {
				choice = &AnthropicToolChoice{Type: "tool", Name: obj.Name}
			}
		}
	}
	if parallel != nil && !*parallel {
		if choice == nil {
			choice = &AnthropicToolChoice{Type: "auto"}
		}
		if choice.Type == "auto" || choice.Type == "any" {
			choice.DisableParallelToolUse = true
		}
	}
	return choice
}

func summaryText(parts []SummaryPart) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}
