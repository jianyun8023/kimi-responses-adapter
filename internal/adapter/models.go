package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ModelInfo describes per-model limits collected from the Kimi Code
// upstream (or seeded from built-in knowledge).
type ModelInfo struct {
	ID              string
	ContextWindow   int
	MaxOutputTokens int
}

// builtinModels seeds the registry. Values are conservative fallbacks for
// the known Kimi Code models; a successful upstream /v1/models fetch
// overrides them.
var builtinModels = []ModelInfo{
	{ID: "k3", ContextWindow: 262144, MaxOutputTokens: 32768},
	{ID: "k3-256k", ContextWindow: 262144, MaxOutputTokens: 32768},
	{ID: "kimi-for-coding", ContextWindow: 262144, MaxOutputTokens: 32768},
	{ID: "kimi-for-coding-highspeed", ContextWindow: 262144, MaxOutputTokens: 32768},
}

// modelRegistry caches model metadata fetched from the upstream
// GET /v1/models endpoint. Fetching is lazy and single-flight; failures are
// non-fatal and simply extend the previous cache.
type modelRegistry struct {
	mu        sync.Mutex
	models    map[string]ModelInfo
	fetchedAt time.Time
	ttl       time.Duration
	client    *http.Client
}

func newModelRegistry(ttl time.Duration) *modelRegistry {
	r := &modelRegistry{
		models: map[string]ModelInfo{},
		ttl:    ttl,
		client: &http.Client{Timeout: 5 * time.Second},
	}
	for _, m := range builtinModels {
		r.models[m.ID] = m
	}
	return r
}

func (r *modelRegistry) lookup(model string) (ModelInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.models[model]
	return m, ok
}

// ensureFresh refetches /v1/models when the cache is stale. The inbound
// client credential is forwarded for authentication, exactly like the
// passthrough proxy does.
func (r *modelRegistry) ensureFresh(ctx context.Context, baseURL string, auth http.Header) {
	r.mu.Lock()
	if time.Since(r.fetchedAt) < r.ttl {
		r.mu.Unlock()
		return
	}
	// Mark the attempt now so concurrent/failing requests don't hammer upstream.
	r.fetchedAt = time.Now()
	r.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return
	}
	for k, vs := range auth {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}
	infos := parseModelList(body)
	if len(infos) == 0 {
		return
	}
	r.mu.Lock()
	for _, m := range infos {
		r.models[m.ID] = m
	}
	r.mu.Unlock()
}

// parseModelList tolerates both OpenAI-style {"data": [...]} and bare-array
// responses, and reads whichever limit fields the upstream provides.
func parseModelList(body json.RawMessage) []ModelInfo {
	var entries []map[string]any
	var wrapper struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Data != nil {
		entries = wrapper.Data
	} else if err := json.Unmarshal(body, &entries); err != nil {
		return nil
	}

	var infos []ModelInfo
	for _, e := range entries {
		id, _ := e["id"].(string)
		if id == "" {
			continue
		}
		info := ModelInfo{ID: id}
		info.ContextWindow = firstInt(e, "context_window", "context_length", "max_context_tokens", "max_input_tokens")
		info.MaxOutputTokens = firstInt(e, "max_output_tokens", "max_tokens", "output_limit", "max_completion_tokens")
		infos = append(infos, info)
	}
	return infos
}

func firstInt(e map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := e[k].(type) {
		case float64:
			if v > 0 {
				return int(v)
			}
		case string:
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
