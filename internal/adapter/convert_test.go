package adapter

import (
	"encoding/json"
	"testing"
)

func testConfig() *Config {
	return &Config{
		ModelMap:           map[string]string{},
		MaxTokens:          32768,
		SearchStatusPrefix: "Search results for query:",
		ThinkingBudgets:    map[string]int{"low": 4096, "medium": 16384, "high": 32768},
	}
}

func mustBuild(t *testing.T, cfg *Config, reqJSON string) *AnthropicRequest {
	t.Helper()
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	out, err := buildAnthropicRequest(cfg, &req, nil)
	if err != nil {
		t.Fatalf("buildAnthropicRequest: %v", err)
	}
	return out
}

func TestReasoningRoundTrip(t *testing.T) {
	enc := encodeReasoning("let me think about this", "sig-kimi-abc123")
	reqJSON := `{
		"model": "k3-256k",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "encrypted_content": "` + enc + `",
			 "summary": [{"type": "summary_text", "text": "let me think about this"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello!"}]},
			{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "file.txt"}
		]
	}`
	out := mustBuild(t, testConfig(), reqJSON)

	if len(out.Messages) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, user), got %d: %+v", len(out.Messages), out.Messages)
	}
	assistant := out.Messages[1]
	if assistant.Role != "assistant" || len(assistant.Content) != 3 {
		t.Fatalf("assistant turn should have thinking+text+tool_use, got %+v", assistant)
	}
	th := assistant.Content[0]
	if th.Type != "thinking" || th.Thinking != "let me think about this" || th.Signature != "sig-kimi-abc123" {
		t.Fatalf("thinking block not restored: %+v", th)
	}
	if assistant.Content[1].Type != "text" || assistant.Content[1].Text != "hello!" {
		t.Fatalf("text block wrong: %+v", assistant.Content[1])
	}
	tu := assistant.Content[2]
	if tu.Type != "tool_use" || tu.ID != "call_1" || tu.Name != "shell" || string(tu.Input) != `{"cmd":"ls"}` {
		t.Fatalf("tool_use block wrong: %+v", tu)
	}
	tr := out.Messages[2].Content[0]
	if tr.Type != "tool_result" || tr.ToolUseID != "call_1" || tr.Content != "file.txt" {
		t.Fatalf("tool_result wrong: %+v", tr)
	}
}

func TestBareSignatureFallback(t *testing.T) {
	p, ok := decodeReasoning("raw-kimi-signature", "thinking text from summary")
	if !ok || p.Signature != "raw-kimi-signature" || p.Thinking != "thinking text from summary" {
		t.Fatalf("fallback decode failed: %+v ok=%v", p, ok)
	}
}

func TestForeignSignatureDropped(t *testing.T) {
	if _, ok := decodeReasoning("gAAAA-openai-blob", "summary"); ok {
		t.Fatal("OpenAI gAAAA blob must not be replayed to Anthropic upstreams")
	}
}

func TestSignedReasoningForcesThinkingOn(t *testing.T) {
	// effort=minimal would normally disable thinking, but a signed thinking
	// block in history requires thinking mode upstream (sub2api#5166).
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"reasoning": {"effort": "minimal"},
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "encrypted_content": "kimi-sig-abc",
			 "summary": [{"type": "summary_text", "text": "thought"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "again"}]}
		]
	}`)
	if out.Thinking == nil || out.Thinking.Type != "enabled" {
		t.Fatalf("signed reasoning should force thinking on: %+v", out.Thinking)
	}
	th := out.Messages[1].Content[0]
	if th.Type != "thinking" || th.Signature != "kimi-sig-abc" {
		t.Fatalf("thinking block not restored: %+v", th)
	}
}

func TestNoForceWhenNoSignedReasoning(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"reasoning": {"effort": "minimal"},
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "reasoning", "encrypted_content": "gAAAA-blob"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello"}]}
		]
	}`)
	if out.Thinking == nil || out.Thinking.Type != "disabled" {
		t.Fatalf("foreign-only reasoning should keep thinking disabled: %+v", out.Thinking)
	}
	for _, m := range out.Messages {
		for _, b := range m.Content {
			if b.Type == "thinking" {
				t.Fatalf("foreign reasoning leaked into messages: %+v", m)
			}
		}
	}
}

func TestInstructionsAndSystemBecomeSystem(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"instructions": "You are Codex.",
		"input": [
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "Be terse."}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}
		]
	}`)
	if out.System != "You are Codex.\n\nBe terse." {
		t.Fatalf("system wrong: %q", out.System)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Fatalf("messages wrong: %+v", out.Messages)
	}
}

func TestToolsAndWebSearch(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": "hi",
		"tools": [
			{"type": "function", "name": "shell", "description": "run", "parameters": {"type": "object"}},
			{"type": "web_search_preview"}
		],
		"tool_choice": "auto",
		"parallel_tool_calls": false
	}`)
	if len(out.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %+v", out.Tools)
	}
	if out.Tools[0].Name != "shell" || out.Tools[0].InputSchema == nil {
		t.Fatalf("function tool wrong: %+v", out.Tools[0])
	}
	if out.Tools[1].Type != "web_search_20250305" || out.Tools[1].Name != "web_search" {
		t.Fatalf("server tool wrong: %+v", out.Tools[1])
	}
	if out.ToolChoice == nil || out.ToolChoice.Type != "auto" || !out.ToolChoice.DisableParallelToolUse {
		t.Fatalf("tool_choice wrong: %+v", out.ToolChoice)
	}
}

func TestThinkingEffortMapping(t *testing.T) {
	out := mustBuild(t, testConfig(), `{"model":"k3","input":"hi","reasoning":{"effort":"high"},"max_output_tokens":65536}`)
	if out.Thinking == nil || out.Thinking.Type != "enabled" || out.Thinking.BudgetTokens != 32768 {
		t.Fatalf("high effort wrong: %+v", out.Thinking)
	}

	out = mustBuild(t, testConfig(), `{"model":"k3","input":"hi","reasoning":{"effort":"minimal"}}`)
	if out.Thinking == nil || out.Thinking.Type != "disabled" {
		t.Fatalf("minimal effort should disable thinking: %+v", out.Thinking)
	}

	out = mustBuild(t, testConfig(), `{"model":"k3","input":"hi","reasoning":{"effort":"high"},"max_output_tokens":8000}`)
	if out.Thinking == nil || out.Thinking.Type != "enabled" || out.Thinking.BudgetTokens >= 8000 {
		t.Fatalf("budget should be clamped below max_tokens: %+v", out.Thinking)
	}
}

func TestImageInput(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [{"type":"message","role":"user","content":[
			{"type":"input_text","text":"what is this?"},
			{"type":"input_image","image_url":"data:image/png;base64,aGk="}
		]}]
	}`)
	blocks := out.Messages[0].Content
	if len(blocks) != 2 || blocks[1].Type != "image" {
		t.Fatalf("blocks wrong: %+v", blocks)
	}
	if blocks[1].Source.MediaType != "image/png" || blocks[1].Source.Data != "aGk=" {
		t.Fatalf("image source wrong: %+v", blocks[1].Source)
	}
}

func TestModelMap(t *testing.T) {
	cfg := testConfig()
	cfg.ModelMap = map[string]string{"k3-256k": "kimi-for-coding-highspeed"}
	out := mustBuild(t, cfg, `{"model":"k3-256k","input":"hi"}`)
	if out.Model != "kimi-for-coding-highspeed" {
		t.Fatalf("model not mapped: %s", out.Model)
	}
}

func buildErr(t *testing.T, cfg *Config, reqJSON string) error {
	t.Helper()
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(reqJSON), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	_, err := buildAnthropicRequest(cfg, &req, nil)
	return err
}

// ---- positive cases ----

func TestMultipleToolResultsMergeIntoOneUserMessage(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"run both"}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
			{"type":"function_call","call_id":"c2","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"one"},
			{"type":"function_call_output","call_id":"c2","output":"two"}
		]
	}`)
	if len(out.Messages) != 3 {
		t.Fatalf("expected user/assistant/user, got %d messages", len(out.Messages))
	}
	last := out.Messages[2]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("tool results should merge into one user message: %+v", last)
	}
	if last.Content[0].ToolUseID != "c1" || last.Content[1].ToolUseID != "c2" {
		t.Fatalf("tool_result order wrong: %+v", last.Content)
	}
}

func TestFunctionCallInvalidArgumentsBecomeEmptyObject(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"not-json"}
		]
	}`)
	tu := out.Messages[1].Content[0]
	if string(tu.Input) != `{}` {
		t.Fatalf("invalid arguments should become {}: %s", tu.Input)
	}
}

func TestToolResultWithStructuredOutput(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":[{"type":"output_text","text":"part1"},{"type":"output_text","text":"part2"}]}
		]
	}`)
	if len(out.Messages) != 3 {
		t.Fatalf("expected user/assistant/user, got %+v", out.Messages)
	}
	tr := out.Messages[2].Content[0]
	blocks, ok := tr.Content.([]AnthropicContent)
	if !ok || len(blocks) != 2 || blocks[0].Text != "part1" || blocks[1].Text != "part2" {
		t.Fatalf("structured tool result wrong: %+v", tr.Content)
	}
}

func TestToolChoiceVariants(t *testing.T) {
	out := mustBuild(t, testConfig(), `{"model":"k3","input":"hi","tool_choice":"required"}`)
	if out.ToolChoice == nil || out.ToolChoice.Type != "any" {
		t.Fatalf("required should map to any: %+v", out.ToolChoice)
	}
	out = mustBuild(t, testConfig(), `{"model":"k3","input":"hi","tool_choice":"none"}`)
	if out.ToolChoice == nil || out.ToolChoice.Type != "none" {
		t.Fatalf("none wrong: %+v", out.ToolChoice)
	}
	out = mustBuild(t, testConfig(), `{"model":"k3","input":"hi","tool_choice":{"type":"function","name":"shell"}}`)
	if out.ToolChoice == nil || out.ToolChoice.Type != "tool" || out.ToolChoice.Name != "shell" {
		t.Fatalf("function choice wrong: %+v", out.ToolChoice)
	}
}

func TestImageURLSource(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [{"type":"message","role":"user","content":[
			{"type":"input_image","image_url":{"url":"https://example.com/x.png"}}
		]}]
	}`)
	b := out.Messages[0].Content[0]
	if b.Type != "image" || b.Source.Type != "url" || b.Source.URL != "https://example.com/x.png" {
		t.Fatalf("url image wrong: %+v", b)
	}
}

func TestRedactedThinkingRoundTrip(t *testing.T) {
	enc := encodeRedactedReasoning("opaque-redacted-data")
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","encrypted_content":"`+enc+`"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}
		]
	}`)
	th := out.Messages[1].Content[0]
	if th.Type != "redacted_thinking" || th.Data != "opaque-redacted-data" {
		t.Fatalf("redacted thinking not restored: %+v", th)
	}
}

func TestSamplingDroppedOnlyWhenThinkingEnabled(t *testing.T) {
	out := mustBuild(t, testConfig(), `{"model":"k3","input":"hi","temperature":0.5,"top_p":0.9,"reasoning":{"effort":"medium"}}`)
	if out.Temperature != nil || out.TopP != nil {
		t.Fatal("sampling must be dropped when thinking is enabled")
	}
	out = mustBuild(t, testConfig(), `{"model":"k3","input":"hi","temperature":0.5,"top_p":0.9,"reasoning":{"effort":"minimal"}}`)
	if out.Temperature == nil || *out.Temperature != 0.5 || out.TopP == nil || *out.TopP != 0.9 {
		t.Fatal("sampling should pass through when thinking is disabled")
	}
}

func TestTinyMaxTokensDisablesThinking(t *testing.T) {
	out := mustBuild(t, testConfig(), `{"model":"k3","input":"hi","max_output_tokens":1024}`)
	if out.Thinking == nil || out.Thinking.Type != "disabled" {
		t.Fatalf("max_tokens<=2048 should disable thinking: %+v", out.Thinking)
	}
}

func TestWebSearchCallHistorySkipped(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"x"}},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
		]
	}`)
	for _, m := range out.Messages {
		for _, b := range m.Content {
			if b.Type == "server_tool_use" || b.Type == "web_search_tool_result" {
				t.Fatalf("web_search_call history should be skipped in v1: %+v", m)
			}
		}
	}
}

func TestStringMessageContent(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [{"type":"message","role":"user","content":"plain string"}]
	}`)
	if out.Messages[0].Content[0].Text != "plain string" {
		t.Fatalf("string content wrong: %+v", out.Messages[0])
	}
}

// ---- negative cases ----

func TestMissingInputRejected(t *testing.T) {
	if err := buildErr(t, testConfig(), `{"model":"k3"}`); err == nil {
		t.Fatal("missing input must be rejected")
	}
}

func TestInvalidInputTypeRejected(t *testing.T) {
	if err := buildErr(t, testConfig(), `{"model":"k3","input":42}`); err == nil {
		t.Fatal("non-string non-array input must be rejected")
	}
}

func TestSystemOnlyInputProducesNoMessages(t *testing.T) {
	err := buildErr(t, testConfig(), `{
		"model": "k3",
		"input": [{"type":"message","role":"developer","content":[{"type":"input_text","text":"be nice"}]}]
	}`)
	if err == nil {
		t.Fatal("input without any user/assistant message must be rejected")
	}
}

func TestUnknownItemsSkipped(t *testing.T) {
	out := mustBuild(t, testConfig(), `{
		"model": "k3",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"computer_call","id":"x"},
			{"type":"local_shell_call","id":"y"}
		]
	}`)
	if len(out.Messages) != 1 {
		t.Fatalf("unknown item types should be skipped: %+v", out.Messages)
	}
}
