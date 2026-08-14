package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseModelListOpenAIStyle(t *testing.T) {
	body := json.RawMessage(`{"object":"list","data":[
		{"id":"k3-256k","object":"model","context_window":262144,"max_output_tokens":65536},
		{"id":"k3","object":"model","context_length":131072,"max_tokens":16384}
	]}`)
	infos := parseModelList(body)
	if len(infos) != 2 {
		t.Fatalf("expected 2 models, got %d", len(infos))
	}
	if infos[0].ContextWindow != 262144 || infos[0].MaxOutputTokens != 65536 {
		t.Fatalf("k3-256k info wrong: %+v", infos[0])
	}
	if infos[1].ContextWindow != 131072 || infos[1].MaxOutputTokens != 16384 {
		t.Fatalf("k3 info wrong: %+v", infos[1])
	}
}

func TestParseModelListBareArray(t *testing.T) {
	infos := parseModelList(json.RawMessage(`[{"id":"kimi-for-coding","max_output_tokens":32768}]`))
	if len(infos) != 1 || infos[0].MaxOutputTokens != 32768 {
		t.Fatalf("bare array parse wrong: %+v", infos)
	}
}

func TestRegistryRefreshOverridesBuiltins(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer client-key" {
			t.Errorf("credential not forwarded: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"k3-256k","context_window":262144,"max_output_tokens":65536}]}`))
	}))
	defer upstream.Close()

	reg := newModelRegistry(time.Minute)
	if info, _ := reg.lookup("k3-256k"); info.MaxOutputTokens != 32768 {
		t.Fatalf("builtin seed wrong: %+v", info)
	}

	auth := http.Header{"Authorization": []string{"Bearer client-key"}}
	reg.ensureFresh(context.Background(), upstream.URL, auth)

	info, ok := reg.lookup("k3-256k")
	if !ok || info.MaxOutputTokens != 65536 {
		t.Fatalf("refresh did not override builtin: %+v", info)
	}
}

func TestRegistryFailureKeepsCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	reg := newModelRegistry(time.Minute)
	reg.ensureFresh(context.Background(), upstream.URL, nil)
	if info, ok := reg.lookup("k3"); !ok || info.MaxOutputTokens != 32768 {
		t.Fatalf("builtin should survive failed refresh: %+v ok=%v", info, ok)
	}
}

func TestMaxTokensPrecedence(t *testing.T) {
	cfg := testConfig()
	resolver := func(model string) (ModelInfo, bool) {
		return ModelInfo{ID: model, MaxOutputTokens: 65536}, true
	}

	// Client value wins over model metadata.
	var req ResponsesRequest
	_ = json.Unmarshal([]byte(`{"model":"k3","input":"hi","max_output_tokens":4096}`), &req)
	out, err := buildAnthropicRequest(cfg, &req, resolver)
	if err != nil || out.MaxTokens != 4096 {
		t.Fatalf("client max_output_tokens should win: %d err=%v", out.MaxTokens, err)
	}

	// Model metadata wins over the global default.
	req = ResponsesRequest{}
	_ = json.Unmarshal([]byte(`{"model":"k3","input":"hi"}`), &req)
	out, err = buildAnthropicRequest(cfg, &req, resolver)
	if err != nil || out.MaxTokens != 65536 {
		t.Fatalf("model metadata should win over KIMI_MAX_TOKENS: %d err=%v", out.MaxTokens, err)
	}

	// Global default applies when no metadata exists.
	out, err = buildAnthropicRequest(cfg, &req, nil)
	if err != nil || out.MaxTokens != cfg.MaxTokens {
		t.Fatalf("global default wrong: %d err=%v", out.MaxTokens, err)
	}
}
