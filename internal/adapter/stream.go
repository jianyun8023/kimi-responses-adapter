package adapter

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// streamTranslator converts a Kimi Anthropic SSE stream into an OpenAI
// Responses SSE stream. It is a pure state machine over upstream events.
type streamTranslator struct {
	cfg *Config
	req *ResponsesRequest
	w   io.Writer
	seq int

	responseID       string
	createdAt        int64
	output           []any
	usageIn          int
	usageOut         int
	usageCached      int
	usageCacheCreate int
	usageThink       int
	stopReason       string

	// current content block state
	blockType     string
	outputIndex   int
	itemID        string
	callID        string
	toolName      string
	argsBuf       strings.Builder
	thinkBuf      strings.Builder
	sigBuf        strings.Builder
	redactedData  string
	textBuf       strings.Builder
	textHold      bool // buffering possible web-search status preamble
	textStarted   bool
	sources       []any
	searchQuery   string
	salvagedQuery string
	// A closed text block matching the search-status marker is held until the
	// next block starts: dropped if that block is server_tool_use(web_search),
	// emitted as normal text otherwise (mirrors sub2api#5166).
	pendingStatus      string
	pendingStatusReady bool
	openItem           bool
	openSearch         bool
	terminated         bool
}

func randID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// translateSSE reads an Anthropic SSE stream from r and writes Responses SSE
// events to w, flushing after every event.
func translateSSE(cfg *Config, req *ResponsesRequest, r io.Reader, w io.Writer, flush func()) error {
	t := &streamTranslator{cfg: cfg, req: req, w: w}
	doFlush := flush
	if doFlush == nil {
		doFlush = func() {}
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var dataLines []string
	done := false
	dispatch := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var ev AnthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return
		}
		t.handleEvent(&ev)
		doFlush()
		if ev.Type == "message_stop" {
			done = true
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			dispatch()
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if done {
			break
		}
	}
	dispatch()
	if !done {
		// Anthropic streams always end with message_stop; anything else means
		// the upstream (or the connection) died mid-flight.
		t.fail("upstream stream ended before message_stop")
		if err := scanner.Err(); err != nil {
			return err
		}
		return nil
	}
	t.finish()
	doFlush()
	return nil
}

func (t *streamTranslator) emit(eventType string, payload map[string]any) {
	payload["type"] = eventType
	payload["sequence_number"] = t.seq
	t.seq++
	b, _ := json.Marshal(payload)
	fmt.Fprintf(t.w, "event: %s\ndata: %s\n\n", eventType, b)
}

func (t *streamTranslator) responseShell(status string) map[string]any {
	out := t.output
	if out == nil {
		out = []any{}
	}
	return map[string]any{
		"id":                  t.responseID,
		"object":              "response",
		"created_at":          t.createdAt,
		"status":              status,
		"error":               nil,
		"incomplete_details":  nil,
		"model":               t.req.Model,
		"output":              out,
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
		"metadata":            map[string]any{},
	}
}

// usageMap converts Anthropic usage to Responses usage. Anthropic reports
// cached tokens separately from input_tokens, while Responses expects
// input_tokens to be the total with cached_tokens as a subset detail.
func usageMap(in, out, cacheCreate, cached, reasoning int) map[string]any {
	totalIn := in + cached + cacheCreate
	return map[string]any{
		"input_tokens":          totalIn,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens":         out,
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
		"total_tokens":          totalIn + out,
	}
}

func (t *streamTranslator) handleEvent(ev *AnthropicStreamEvent) {
	switch ev.Type {
	case "message_start":
		t.responseID = randID("resp_")
		t.createdAt = time.Now().Unix()
		if ev.Message != nil && ev.Message.Usage != nil {
			t.usageIn = ev.Message.Usage.InputTokens
			t.usageCached = ev.Message.Usage.CacheReadInputTokens
			t.usageCacheCreate = ev.Message.Usage.CacheCreationInputTokens
		}
		t.emit("response.created", map[string]any{"response": t.responseShell("in_progress")})
		t.emit("response.in_progress", map[string]any{"response": t.responseShell("in_progress")})
	case "content_block_start":
		if ev.ContentBlock != nil {
			t.blockStart(ev.Index, ev.ContentBlock)
		}
	case "content_block_delta":
		if ev.Delta != nil {
			t.blockDelta(ev.Delta)
		}
	case "content_block_stop":
		t.blockStop()
	case "message_delta":
		if ev.Usage != nil {
			t.usageOut = ev.Usage.OutputTokens
			if ev.Usage.CacheReadInputTokens > 0 {
				t.usageCached = ev.Usage.CacheReadInputTokens
			}
			if ev.Usage.CacheCreationInputTokens > 0 {
				t.usageCacheCreate = ev.Usage.CacheCreationInputTokens
			}
			if ev.Usage.OutputTokensDetails != nil {
				t.usageThink = ev.Usage.OutputTokensDetails.ThinkingTokens
			}
		}
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			t.stopReason = ev.Delta.StopReason
		}
	case "message_stop":
		t.finish()
	case "error":
		msg := "upstream error"
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		t.fail(msg)
	}
}

func (t *streamTranslator) blockStart(index int, cb *AnthropicContent) {
	// Arbitrate any held search-status text against the new block.
	if t.pendingStatusReady {
		if cb.Type == "server_tool_use" && cb.Name == "web_search" {
			// Kimi does not stream server_tool_use input, so the status text
			// is the only place the query appears: salvage it before dropping.
			if strings.HasPrefix(t.pendingStatus, t.cfg.SearchStatusPrefix) {
				t.salvagedQuery = strings.TrimSpace(strings.TrimPrefix(t.pendingStatus, t.cfg.SearchStatusPrefix))
			}
			t.pendingStatus = ""
			t.pendingStatusReady = false
		} else {
			t.flushPendingStatusText()
		}
	}

	t.blockType = cb.Type
	t.argsBuf.Reset()
	t.thinkBuf.Reset()
	t.sigBuf.Reset()
	t.textBuf.Reset()
	t.textHold = false
	t.textStarted = false
	t.redactedData = ""
	t.outputIndex = len(t.output)

	switch cb.Type {
	case "text":
		// Hold deltas until we know whether this block is a Kimi web-search
		// status preamble ("Search results for query: ...").
		t.textHold = true
	case "thinking":
		t.sigBuf.WriteString(cb.Signature)
		t.itemID = randID("rs_")
		t.openItem = true
		t.emit("response.output_item.added", map[string]any{
			"output_index": t.outputIndex,
			"item":         map[string]any{"id": t.itemID, "type": "reasoning", "summary": []any{}},
		})
		t.emit("response.reasoning_summary_part.added", map[string]any{
			"item_id": t.itemID, "output_index": t.outputIndex, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": ""},
		})
	case "redacted_thinking":
		t.itemID = randID("rs_")
		t.redactedData = cb.Data
		t.openItem = true
		t.emit("response.output_item.added", map[string]any{
			"output_index": t.outputIndex,
			"item":         map[string]any{"id": t.itemID, "type": "reasoning", "summary": []any{}},
		})
	case "tool_use":
		t.itemID = randID("fc_")
		t.callID = cb.ID
		t.toolName = cb.Name
		t.openItem = true
		t.emit("response.output_item.added", map[string]any{
			"output_index": t.outputIndex,
			"item": map[string]any{
				"id": t.itemID, "type": "function_call", "call_id": t.callID,
				"name": t.toolName, "arguments": "", "status": "in_progress",
			},
		})
	case "server_tool_use":
		t.itemID = randID("ws_")
		t.openItem = true
		t.openSearch = true
		t.searchQuery = ""
		t.sources = nil
		t.emit("response.output_item.added", map[string]any{
			"output_index": t.outputIndex,
			"item":         map[string]any{"id": t.itemID, "type": "web_search_call", "status": "in_progress"},
		})
		t.emit("response.web_search_call.in_progress", map[string]any{
			"output_index": t.outputIndex, "item_id": t.itemID,
		})
	case "web_search_tool_result":
		t.sources = extractSources(cb.Content)
	}
}

func (t *streamTranslator) blockDelta(d *AnthropicDelta) {
	switch d.Type {
	case "text_delta":
		t.textDelta(d.Text)
	case "thinking_delta":
		t.thinkBuf.WriteString(d.Thinking)
		t.emit("response.reasoning_summary_text.delta", map[string]any{
			"item_id": t.itemID, "output_index": t.outputIndex, "summary_index": 0,
			"delta": d.Thinking,
		})
	case "signature_delta":
		t.sigBuf.WriteString(d.Signature)
	case "input_json_delta":
		t.argsBuf.WriteString(d.PartialJSON)
		if t.blockType == "tool_use" {
			t.emit("response.function_call_arguments.delta", map[string]any{
				"item_id": t.itemID, "output_index": t.outputIndex, "delta": d.PartialJSON,
			})
		}
	}
}

func (t *streamTranslator) textDelta(s string) {
	t.textBuf.WriteString(s)
	if t.textHold {
		cur := t.textBuf.String()
		marker := t.cfg.SearchStatusPrefix
		if len(cur) < len(marker) && strings.HasPrefix(marker, cur) {
			return // still ambiguous, keep buffering
		}
		if strings.HasPrefix(cur, marker) {
			return // status preamble confirmed, hold until block stop then drop
		}
		// Not a status preamble: flush the buffer and stream live from here.
		t.textHold = false
		t.startTextItem()
		t.emit("response.output_text.delta", map[string]any{
			"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0, "delta": cur,
		})
		return
	}
	if !t.textStarted {
		t.startTextItem()
	}
	t.emit("response.output_text.delta", map[string]any{
		"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0, "delta": s,
	})
}

func (t *streamTranslator) startTextItem() {
	t.itemID = randID("msg_")
	t.textStarted = true
	t.openItem = true
	t.emit("response.output_item.added", map[string]any{
		"output_index": t.outputIndex,
		"item": map[string]any{
			"id": t.itemID, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []any{},
		},
	})
	t.emit("response.content_part.added", map[string]any{
		"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

func (t *streamTranslator) blockStop() {
	switch t.blockType {
	case "text":
		t.textStop()
	case "thinking":
		full := t.thinkBuf.String()
		t.emit("response.reasoning_summary_part.done", map[string]any{
			"item_id": t.itemID, "output_index": t.outputIndex, "summary_index": 0,
			"part": map[string]any{"type": "summary_text", "text": full},
		})
		item := map[string]any{
			"id": t.itemID, "type": "reasoning",
			"summary":           []any{map[string]any{"type": "summary_text", "text": full}},
			"encrypted_content": encodeReasoning(full, t.sigBuf.String()),
		}
		t.emit("response.output_item.done", map[string]any{"output_index": t.outputIndex, "item": item})
		t.output = append(t.output, item)
		t.openItem = false
	case "redacted_thinking":
		item := map[string]any{
			"id": t.itemID, "type": "reasoning", "summary": []any{},
			"encrypted_content": encodeRedactedReasoning(t.redactedData),
		}
		t.emit("response.output_item.done", map[string]any{"output_index": t.outputIndex, "item": item})
		t.output = append(t.output, item)
		t.openItem = false
	case "tool_use":
		args := t.argsBuf.String()
		if args == "" || !json.Valid([]byte(args)) {
			args = "{}"
		}
		t.emit("response.function_call_arguments.done", map[string]any{
			"item_id": t.itemID, "output_index": t.outputIndex, "arguments": args,
		})
		item := map[string]any{
			"id": t.itemID, "type": "function_call", "call_id": t.callID,
			"name": t.toolName, "arguments": args, "status": "completed",
		}
		t.emit("response.output_item.done", map[string]any{"output_index": t.outputIndex, "item": item})
		t.output = append(t.output, item)
		t.openItem = false
	case "server_tool_use":
		var input struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal([]byte(t.argsBuf.String()), &input)
		t.searchQuery = input.Query
		t.emit("response.web_search_call.searching", map[string]any{
			"output_index": t.outputIndex, "item_id": t.itemID,
		})
	case "web_search_tool_result":
		t.closeSearchItem("completed")
	}
	t.blockType = ""
}

func (t *streamTranslator) textStop() {
	if t.textHold {
		cur := t.textBuf.String()
		t.textHold = false
		marker := t.cfg.SearchStatusPrefix
		if cur == "" {
			return
		}
		// Blocks matching the marker (or a strict prefix of it, which can only
		// be a truncated status text) are held until the next block decides
		// whether this was a web-search preamble.
		if strings.HasPrefix(cur, marker) || strings.HasPrefix(marker, cur) {
			t.pendingStatus = cur
			t.pendingStatusReady = true
			return
		}
		// Ambiguous buffer that turned out not to be a preamble: emit whole.
		t.startTextItem()
		t.emit("response.output_text.delta", map[string]any{
			"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0, "delta": cur,
		})
	}
	if !t.textStarted {
		return
	}
	full := t.textBuf.String()
	t.emit("response.content_part.done", map[string]any{
		"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": full, "annotations": []any{}},
	})
	item := map[string]any{
		"id": t.itemID, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": full, "annotations": []any{}}},
	}
	t.emit("response.output_item.done", map[string]any{"output_index": t.outputIndex, "item": item})
	t.output = append(t.output, item)
	t.openItem = false
}

// flushPendingStatusText emits a held status-candidate block as a normal
// (complete) assistant message item.
func (t *streamTranslator) flushPendingStatusText() {
	text := t.pendingStatus
	t.pendingStatus = ""
	t.pendingStatusReady = false
	if text == "" {
		return
	}
	t.outputIndex = len(t.output)
	t.startTextItem()
	t.emit("response.output_text.delta", map[string]any{
		"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0, "delta": text,
	})
	t.emit("response.content_part.done", map[string]any{
		"item_id": t.itemID, "output_index": t.outputIndex, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
	})
	item := map[string]any{
		"id": t.itemID, "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
	t.emit("response.output_item.done", map[string]any{"output_index": t.outputIndex, "item": item})
	t.output = append(t.output, item)
	t.openItem = false
}

func (t *streamTranslator) closeSearchItem(status string) {
	if !t.openSearch {
		return
	}
	t.openSearch = false
	t.openItem = false
	t.emit("response.web_search_call.completed", map[string]any{
		"output_index": t.outputIndex, "item_id": t.itemID,
	})
	action := map[string]any{"type": "search"}
	query := t.searchQuery
	if query == "" {
		query = t.salvagedQuery
	}
	if query != "" {
		action["query"] = query
	}
	if len(t.sources) > 0 {
		action["sources"] = t.sources
	}
	item := map[string]any{
		"id": t.itemID, "type": "web_search_call", "status": status, "action": action,
	}
	t.emit("response.output_item.done", map[string]any{"output_index": t.outputIndex, "item": item})
	t.output = append(t.output, item)
}

// finish closes any dangling items. Called on message_stop and at stream end.
func (t *streamTranslator) finish() {
	if t.responseID == "" {
		return
	}
	if t.pendingStatusReady {
		t.flushPendingStatusText()
	}
	if t.openSearch {
		t.closeSearchItem("completed")
	}
	if t.openItem {
		// Upstream ended mid-block; close the item with what we have.
		switch t.blockType {
		case "text":
			t.textStop()
		case "thinking", "redacted_thinking", "tool_use":
			t.blockStop()
		}
		t.openItem = false
	}
	status := "completed"
	var resp map[string]any
	if t.stopReason == "max_tokens" {
		resp = t.responseShell("incomplete")
		resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	} else {
		resp = t.responseShell(status)
	}
	resp["usage"] = usageMap(t.usageIn, t.usageOut, t.usageCacheCreate, t.usageCached, t.usageThink)
	if t.stopReason == "max_tokens" {
		t.emit("response.incomplete", map[string]any{"response": resp})
	} else {
		t.emit("response.completed", map[string]any{"response": resp})
	}
	t.responseID = "" // prevent a second terminal event
}

func (t *streamTranslator) fail(message string) {
	if t.terminated {
		return
	}
	t.terminated = true
	if t.responseID == "" {
		t.responseID = randID("resp_")
		t.createdAt = time.Now().Unix()
	}
	resp := t.responseShell("failed")
	resp["error"] = map[string]any{"code": "upstream_error", "message": message}
	resp["usage"] = usageMap(t.usageIn, t.usageOut, t.usageCacheCreate, t.usageCached, t.usageThink)
	t.emit("response.failed", map[string]any{"response": resp})
	t.responseID = ""
}

// extractSources pulls URLs out of a web_search_tool_result content array.
func extractSources(content any) []any {
	arr, ok := content.([]any)
	if !ok {
		return nil
	}
	var sources []any
	for _, c := range arr {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "web_search_result" {
			if u, ok := m["url"].(string); ok && u != "" {
				sources = append(sources, map[string]any{"type": "url", "url": u})
			}
		}
	}
	return sources
}
