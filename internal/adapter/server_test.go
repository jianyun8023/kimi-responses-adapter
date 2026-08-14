package adapter

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const miniUpstreamStream = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi there\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

type recordedRequest struct {
	path   string
	auth   string
	apiKey string
	body   string
}

func setupTestServers(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec *recordedRequest)) (*httptest.Server, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.path = r.URL.RequestURI()
		rec.auth = r.Header.Get("Authorization")
		rec.apiKey = r.Header.Get("x-api-key")
		rec.body = string(body)
		handler(w, r, rec)
	}))
	t.Cleanup(upstream.Close)

	cfg := testConfig()
	cfg.KimiBaseURL = upstream.URL
	adapter := httptest.NewServer(NewServer(*cfg).Handler())
	t.Cleanup(adapter.Close)
	return adapter, rec
}

func postWithKey(t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEndToEndResponsesStream(t *testing.T) {
	adapter, rec := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, miniUpstreamStream)
	})

	resp := postWithKey(t, adapter.URL+"/v1/responses", "client-kimi-key",
		`{"model":"k3","stream":true,"input":"hello"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	out := string(body)
	if !strings.Contains(out, "event: response.completed") {
		t.Fatalf("missing response.completed:\n%s", out)
	}
	if !strings.Contains(out, "hi there") {
		t.Fatalf("missing text delta:\n%s", out)
	}
	// Upstream saw the Anthropic endpoint with the client's key forwarded.
	if rec.path != "/v1/messages" {
		t.Fatalf("upstream path wrong: %s", rec.path)
	}
	if rec.auth != "Bearer client-kimi-key" {
		t.Fatalf("upstream auth wrong: %q %q", rec.auth, rec.apiKey)
	}
	if !strings.Contains(rec.body, `"thinking":{"type":"enabled"`) {
		t.Fatalf("upstream body missing thinking config: %s", rec.body)
	}
}

func TestPassthroughChatCompletions(t *testing.T) {
	adapter, rec := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[]}`)
	})

	resp := postWithKey(t, adapter.URL+"/v1/chat/completions", "client-kimi-key",
		`{"model":"k3","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "chatcmpl-1") {
		t.Fatalf("passthrough response wrong: %d %s", resp.StatusCode, body)
	}
	if rec.path != "/v1/chat/completions" {
		t.Fatalf("passthrough path wrong: %s", rec.path)
	}
	if !strings.Contains(rec.body, `"messages"`) {
		t.Fatalf("passthrough body was modified: %s", rec.body)
	}
	if rec.auth != "Bearer client-kimi-key" {
		t.Fatalf("passthrough auth wrong: %q", rec.auth)
	}
}

func TestPassthroughStreamsIncrementally(t *testing.T) {
	adapter, _ := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, miniUpstreamStream)
	})

	resp := postWithKey(t, adapter.URL+"/v1/messages", "client-kimi-key",
		`{"model":"k3","stream":true,"messages":[]}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "event: message_start") {
		t.Fatalf("passthrough SSE body wrong:\n%s", body)
	}
}

func TestXAPIKeyForwarded(t *testing.T) {
	adapter, rec := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, miniUpstreamStream)
	})

	req, _ := http.NewRequest(http.MethodPost, adapter.URL+"/v1/responses",
		strings.NewReader(`{"model":"k3","stream":true,"input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "xkimi-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rec.apiKey != "xkimi-key" {
		t.Fatalf("x-api-key not forwarded: %q", rec.apiKey)
	}
}

const miniUpstreamMessage = `{"id":"msg_1","type":"message","role":"assistant","model":"k3",` +
	`"content":[{"type":"thinking","thinking":"hmm","signature":"sig-1"},` +
	`{"type":"text","text":"Search results for query: x"},` +
	`{"type":"server_tool_use","name":"web_search"},` +
	`{"type":"web_search_tool_result","content":[]},` +
	`{"type":"text","text":"the answer"}],` +
	`"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":100,"cache_read_input_tokens":50,"output_tokens":20,"output_tokens_details":{"thinking_tokens":5}}}`

// ---- positive cases ----

func TestEndToEndResponsesNonStream(t *testing.T) {
	adapter, rec := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, miniUpstreamMessage)
	})

	resp := postWithKey(t, adapter.URL+"/v1/responses", "client-kimi-key",
		`{"model":"k3","stream":false,"input":"hello"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if out["status"] != "completed" {
		t.Fatalf("status wrong: %v", out["status"])
	}
	output := out["output"].([]any)
	// reasoning + web_search_call + message; status text suppressed.
	if len(output) != 3 {
		t.Fatalf("expected 3 output items, got %d: %s", len(output), body)
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 150 { // 100 + 50 cached
		t.Fatalf("usage wrong: %v", usage)
	}
	// Upstream must have been asked for non-streaming.
	if strings.Contains(rec.body, `"stream":true`) {
		t.Fatalf("stream flag leaked to upstream: %s", rec.body)
	}
}

func TestPassthroughPreservesQueryString(t *testing.T) {
	adapter, rec := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		_, _ = io.WriteString(w, `{}`)
	})
	resp, err := http.Get(adapter.URL + "/v1/models?limit=5&after=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rec.path != "/v1/models?limit=5&after=x" {
		t.Fatalf("query string lost: %s", rec.path)
	}
}

func TestAnthropicHeadersSet(t *testing.T) {
	var version, beta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version = r.Header.Get("anthropic-version")
		beta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, miniUpstreamStream)
	}))
	defer upstream.Close()

	cfg := testConfig()
	cfg.KimiBaseURL = upstream.URL
	cfg.AnthropicBeta = "interleaved-thinking-2025-05-14"
	adapter := httptest.NewServer(NewServer(*cfg).Handler())
	defer adapter.Close()

	resp := postWithKey(t, adapter.URL+"/v1/responses", "k",
		`{"model":"k3","stream":true,"input":"hi"}`)
	resp.Body.Close()
	if version != "2023-06-01" {
		t.Fatalf("anthropic-version wrong: %q", version)
	}
	if beta != "interleaved-thinking-2025-05-14" {
		t.Fatalf("anthropic-beta not forwarded: %q", beta)
	}
}

func TestModelMetadataUsedForMaxTokens(t *testing.T) {
	rec := &recordedRequest{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"k3","max_output_tokens":65536}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		rec.body = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, miniUpstreamStream)
	}))
	defer upstream.Close()

	cfg := testConfig()
	cfg.KimiBaseURL = upstream.URL
	adapter := httptest.NewServer(NewServer(*cfg).Handler())
	defer adapter.Close()

	resp := postWithKey(t, adapter.URL+"/v1/responses", "k",
		`{"model":"k3","stream":true,"input":"hi"}`)
	resp.Body.Close()
	if !strings.Contains(rec.body, `"max_tokens":65536`) {
		t.Fatalf("model metadata max_tokens not used: %s", rec.body)
	}
}

// ---- negative cases ----

func TestInvalidBodyRejected(t *testing.T) {
	adapter, _ := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		t.Error("upstream must not be called on invalid body")
	})
	resp := postWithKey(t, adapter.URL+"/v1/responses", "k", `{not json`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUpstreamErrorRelayedNonStream(t *testing.T) {
	adapter, _ := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`)
	})
	resp := postWithKey(t, adapter.URL+"/v1/responses", "bad-key",
		`{"model":"k3","stream":false,"input":"hi"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upstream status should be relayed, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "invalid api key") {
		t.Fatalf("upstream error message lost: %s", body)
	}
}

func TestUpstreamErrorRelayedStream(t *testing.T) {
	adapter, _ := setupTestServers(t, func(w http.ResponseWriter, r *http.Request, _ *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529) // Anthropic overloaded
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	})
	resp := postWithKey(t, adapter.URL+"/v1/responses", "k",
		`{"model":"k3","stream":true,"input":"hi"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// Stream clients always get 200 + a terminal response.failed event.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream errors should surface as SSE, got HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "event: response.failed") || !strings.Contains(string(body), "Overloaded") {
		t.Fatalf("response.failed missing or wrong:\n%s", body)
	}
}

func TestUpstreamUnreachable(t *testing.T) {
	cfg := testConfig()
	cfg.KimiBaseURL = "http://127.0.0.1:1" // nothing listening
	adapter := httptest.NewServer(NewServer(*cfg).Handler())
	defer adapter.Close()

	resp := postWithKey(t, adapter.URL+"/v1/responses", "k",
		`{"model":"k3","stream":false,"input":"hi"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}
