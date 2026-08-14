package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

type emittedEvent struct {
	Type string
	Data map[string]any
}

func parseEvents(t *testing.T, sse string) []emittedEvent {
	t.Helper()
	var events []emittedEvent
	for _, chunk := range strings.Split(sse, "\n\n") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var ev emittedEvent
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(line, "data:") {
				var d map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
					t.Fatalf("bad event JSON: %v\n%s", err, line)
				}
				ev.Data = d
				ev.Type, _ = d["type"].(string)
			}
		}
		events = append(events, ev)
	}
	return events
}

func eventsOfType(events []emittedEvent, typ string) []emittedEvent {
	var out []emittedEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// Canned Kimi stream: search status text, server_tool_use, tool result,
// thinking with signature, final answer text, and a client tool call.
const kimiStream = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"k3\",\"usage\":{\"input_tokens\":120,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Search results for query: best\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" pizza places\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_1\",\"name\":\"web_search\",\"input\":{}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\": \\\"best pizza\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\" places\\\"}\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"web_search_tool_result\",\"tool_use_id\":\"srvtoolu_1\",\"content\":[{\"type\":\"web_search_result\",\"url\":\"https://example.com/pizza\",\"title\":\"Pizza\"}]}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":2}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":3,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"The user wants pizza. \"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"I found results.\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"kimi-sig-\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":3,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"xyz\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":3}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":4,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":4,\"delta\":{\"type\":\"text_delta\",\"text\":\"Here are \"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":4,\"delta\":{\"type\":\"text_delta\",\"text\":\"the best pizza places.\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":4}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":5,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"shell\",\"input\":{}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":5,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":5}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":88}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

func runStream(t *testing.T, cfg *Config, upstream string) []emittedEvent {
	t.Helper()
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"k3","stream":true,"input":"hi"}`), &req); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := translateSSE(cfg, &req, strings.NewReader(upstream), &sb, nil); err != nil {
		t.Fatalf("translateSSE: %v", err)
	}
	return parseEvents(t, sb.String())
}

func TestStreamSearchStatusSuppressed(t *testing.T) {
	events := runStream(t, testConfig(), kimiStream)

	var text string
	for _, e := range eventsOfType(events, "response.output_text.delta") {
		text += e.Data["delta"].(string)
	}
	if strings.Contains(text, "Search results for query:") {
		t.Fatalf("search status text leaked into output: %q", text)
	}
	if text != "Here are the best pizza places." {
		t.Fatalf("final answer wrong: %q", text)
	}
}

func TestStreamWebSearchCall(t *testing.T) {
	events := runStream(t, testConfig(), kimiStream)

	var searchItem map[string]any
	for _, e := range eventsOfType(events, "response.output_item.done") {
		item := e.Data["item"].(map[string]any)
		if item["type"] == "web_search_call" {
			searchItem = item
		}
	}
	if searchItem == nil {
		t.Fatal("no web_search_call item emitted")
	}
	if searchItem["status"] != "completed" {
		t.Fatalf("web_search_call status wrong: %v", searchItem["status"])
	}
	action := searchItem["action"].(map[string]any)
	if action["query"] != "best pizza places" {
		t.Fatalf("search query wrong: %v", action["query"])
	}
	sources := action["sources"].([]any)
	if len(sources) != 1 || sources[0].(map[string]any)["url"] != "https://example.com/pizza" {
		t.Fatalf("sources wrong: %v", sources)
	}
}

func TestStreamReasoningEncryptedContent(t *testing.T) {
	events := runStream(t, testConfig(), kimiStream)

	var reasoning map[string]any
	for _, e := range eventsOfType(events, "response.output_item.done") {
		item := e.Data["item"].(map[string]any)
		if item["type"] == "reasoning" {
			reasoning = item
		}
	}
	if reasoning == nil {
		t.Fatal("no reasoning item emitted")
	}
	enc, _ := reasoning["encrypted_content"].(string)
	p, ok := decodeReasoning(enc, "")
	if !ok {
		t.Fatal("encrypted_content does not decode")
	}
	if p.Thinking != "The user wants pizza. I found results." {
		t.Fatalf("thinking wrong: %q", p.Thinking)
	}
	if p.Signature != "kimi-sig-xyz" {
		t.Fatalf("signature wrong: %q", p.Signature)
	}
	var summary string
	for _, e := range eventsOfType(events, "response.reasoning_summary_text.delta") {
		summary += e.Data["delta"].(string)
	}
	if summary != p.Thinking {
		t.Fatalf("summary deltas wrong: %q", summary)
	}
}

func TestStreamFunctionCall(t *testing.T) {
	events := runStream(t, testConfig(), kimiStream)

	doneArgs := eventsOfType(events, "response.function_call_arguments.done")
	if len(doneArgs) != 1 || doneArgs[0].Data["arguments"] != `{"cmd":"ls"}` {
		t.Fatalf("function call arguments wrong: %+v", doneArgs)
	}
	var fc map[string]any
	for _, e := range eventsOfType(events, "response.output_item.done") {
		item := e.Data["item"].(map[string]any)
		if item["type"] == "function_call" {
			fc = item
		}
	}
	if fc == nil || fc["call_id"] != "toolu_1" || fc["name"] != "shell" {
		t.Fatalf("function_call item wrong: %+v", fc)
	}
}

func TestStreamCompletedUsage(t *testing.T) {
	events := runStream(t, testConfig(), kimiStream)

	completed := eventsOfType(events, "response.completed")
	if len(completed) != 1 {
		t.Fatalf("expected exactly one response.completed, got %d", len(completed))
	}
	resp := completed[0].Data["response"].(map[string]any)
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 120 || usage["output_tokens"].(float64) != 88 {
		t.Fatalf("usage wrong: %v", usage)
	}
	output := resp["output"].([]any)
	if len(output) != 4 {
		t.Fatalf("expected 4 output items, got %d: %v", len(output), output)
	}
	if resp["status"] != "completed" {
		t.Fatalf("status wrong: %v", resp["status"])
	}
}

func TestStreamMaxTokensIncomplete(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":4096}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	if n := len(eventsOfType(events, "response.incomplete")); n != 1 {
		t.Fatalf("expected response.incomplete, got %d", n)
	}
	if n := len(eventsOfType(events, "response.completed")); n != 0 {
		t.Fatalf("should not emit response.completed, got %d", n)
	}
}

func TestStreamUpstreamError(t *testing.T) {
	upstream := "event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	events := runStream(t, testConfig(), upstream)
	failed := eventsOfType(events, "response.failed")
	if len(failed) != 1 {
		t.Fatalf("expected response.failed, got %d", len(failed))
	}
	resp := failed[0].Data["response"].(map[string]any)
	if resp["error"].(map[string]any)["message"] != "Overloaded" {
		t.Fatalf("error message wrong: %v", resp["error"])
	}
}

// Text matching the search-status marker but NOT followed by a
// server_tool_use block must be emitted as normal output (sub2api#5166).
func TestStreamStatusPrefixWithoutSearchIsKept(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Search results for query: test\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"second block\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)

	var text string
	for _, e := range eventsOfType(events, "response.output_text.delta") {
		text += e.Data["delta"].(string)
	}
	if !strings.Contains(text, "Search results for query: test") {
		t.Fatalf("non-search text with marker prefix was wrongly suppressed: %q", text)
	}
	if !strings.Contains(text, "second block") {
		t.Fatalf("missing second block: %q", text)
	}
}

// A stream that ends without message_stop must surface response.failed.
func TestStreamPrematureEOFFails(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	events := runStream(t, testConfig(), upstream)
	if n := len(eventsOfType(events, "response.failed")); n != 1 {
		t.Fatalf("expected one response.failed, got %d", n)
	}
	if n := len(eventsOfType(events, "response.completed")); n != 0 {
		t.Fatalf("must not emit response.completed on premature EOF, got %d", n)
	}
}

// ---- positive cases ----

func TestStreamSequenceNumbersMonotonic(t *testing.T) {
	events := runStream(t, testConfig(), kimiStream)
	for i, e := range events {
		seq, ok := e.Data["sequence_number"].(float64)
		if !ok || int(seq) != i {
			t.Fatalf("sequence_number not monotonic at %d: %v", i, e.Data["sequence_number"])
		}
	}
}

func TestStreamMarkerSplitAcrossDeltas(t *testing.T) {
	// The status marker arriving in pieces must still be recognized.
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Search resu\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lts for query: golang\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"server_tool_use\",\"name\":\"web_search\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)

	if n := len(eventsOfType(events, "response.output_text.delta")); n != 0 {
		t.Fatalf("split-marker status text leaked: %d deltas", n)
	}
	var searchItem map[string]any
	for _, e := range eventsOfType(events, "response.output_item.done") {
		item := e.Data["item"].(map[string]any)
		if item["type"] == "web_search_call" {
			searchItem = item
		}
	}
	if searchItem == nil {
		t.Fatal("web_search_call missing")
	}
	if q := searchItem["action"].(map[string]any)["query"]; q != "golang" {
		t.Fatalf("salvaged query wrong: %v", q)
	}
}

func TestStreamSearchWithoutResultClosedAtStop(t *testing.T) {
	// server_tool_use without a following web_search_tool_result must still
	// be closed at message_stop rather than leak as in_progress.
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"name\":\"web_search\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	var item map[string]any
	for _, e := range eventsOfType(events, "response.output_item.done") {
		if e.Data["item"].(map[string]any)["type"] == "web_search_call" {
			item = e.Data["item"].(map[string]any)
		}
	}
	if item == nil || item["status"] != "completed" {
		t.Fatalf("dangling web_search_call not closed: %+v", item)
	}
}

func TestStreamRedactedThinking(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"opaque-blob\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	var reasoning map[string]any
	for _, e := range eventsOfType(events, "response.output_item.done") {
		if e.Data["item"].(map[string]any)["type"] == "reasoning" {
			reasoning = e.Data["item"].(map[string]any)
		}
	}
	if reasoning == nil {
		t.Fatal("no reasoning item for redacted_thinking")
	}
	p, ok := decodeReasoning(reasoning["encrypted_content"].(string), "")
	if !ok || p.Redacted != "opaque-blob" {
		t.Fatalf("redacted payload wrong: %+v ok=%v", p, ok)
	}
}

func TestStreamToolUseEmptyArgs(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"noop\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	done := eventsOfType(events, "response.function_call_arguments.done")
	if len(done) != 1 || done[0].Data["arguments"] != "{}" {
		t.Fatalf("empty args should become {}: %+v", done)
	}
}

func TestStreamUsageCachedAndThinkingTokens(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":239,\"cache_read_input_tokens\":7680,\"output_tokens\":0}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":239,\"cache_read_input_tokens\":7680,\"output_tokens\":44,\"output_tokens_details\":{\"thinking_tokens\":28}}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	completed := eventsOfType(events, "response.completed")
	if len(completed) != 1 {
		t.Fatal("no response.completed")
	}
	usage := completed[0].Data["response"].(map[string]any)["usage"].(map[string]any)
	// Responses semantics: input_tokens includes cached tokens.
	if usage["input_tokens"].(float64) != 7919 {
		t.Fatalf("input_tokens should include cache_read: %v", usage["input_tokens"])
	}
	if usage["input_tokens_details"].(map[string]any)["cached_tokens"].(float64) != 7680 {
		t.Fatalf("cached_tokens wrong: %v", usage)
	}
	if usage["output_tokens_details"].(map[string]any)["reasoning_tokens"].(float64) != 28 {
		t.Fatalf("reasoning_tokens wrong: %v", usage)
	}
}

// ---- negative cases ----

func TestStreamMalformedDataLineSkipped(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {not valid json\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	if n := len(eventsOfType(events, "response.completed")); n != 1 {
		t.Fatalf("stream should survive a malformed data line, completed=%d", n)
	}
}

func TestStreamErrorMidStreamFails(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"boom\"}}\n\n"
	events := runStream(t, testConfig(), upstream)
	failed := eventsOfType(events, "response.failed")
	if len(failed) != 1 {
		t.Fatalf("expected one response.failed, got %d", len(failed))
	}
	if n := len(eventsOfType(events, "response.completed")); n != 0 {
		t.Fatalf("must not complete after upstream error, got %d", n)
	}
}

func TestStreamEmptyTextBlockEmitsNothing(t *testing.T) {
	upstream := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	events := runStream(t, testConfig(), upstream)
	if n := len(eventsOfType(events, "response.output_item.added")); n != 0 {
		t.Fatalf("empty text block should emit no items, got %d", n)
	}
	completed := eventsOfType(events, "response.completed")
	if len(completed) != 1 {
		t.Fatal("no response.completed")
	}
	out := completed[0].Data["response"].(map[string]any)["output"].([]any)
	if len(out) != 0 {
		t.Fatalf("output should be empty: %v", out)
	}
}
