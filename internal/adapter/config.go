package adapter

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// Config holds all adapter configuration. Everything is driven by
// environment variables so the binary stays a thin, stateless proxy.
type Config struct {
	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string
	// KimiBaseURL is the base URL of the Kimi Code Anthropic-compatible API.
	KimiBaseURL string
	// AnthropicBeta is sent as the anthropic-beta header when non-empty.
	AnthropicBeta string
	// ModelMap maps incoming Responses model names to upstream Kimi models.
	// Unmapped names are passed through unchanged.
	ModelMap map[string]string
	// Models is the list returned by GET /v1/models.
	Models []string
	// MaxTokens is the default Anthropic max_tokens when the client does not
	// specify max_output_tokens.
	MaxTokens int
	// ThinkingBudgets maps reasoning effort to Anthropic thinking budget.
	ThinkingBudgets map[string]int
	// SearchStatusPrefix marks Kimi's web-search status text blocks
	// (e.g. "Search results for query: ...") that must be suppressed.
	SearchStatusPrefix string
}

func LoadConfig() Config {
	cfg := Config{
		ListenAddr:         envOr("LISTEN_ADDR", ":8787"),
		KimiBaseURL:        strings.TrimRight(envOr("KIMI_BASE_URL", "https://api.kimi.com/coding"), "/"),
		AnthropicBeta:      os.Getenv("KIMI_ANTHROPIC_BETA"),
		ModelMap:           map[string]string{},
		Models:             []string{"k3", "k3-256k", "kimi-for-coding", "kimi-for-coding-highspeed"},
		MaxTokens:          envInt("KIMI_MAX_TOKENS", 32768),
		SearchStatusPrefix: envOr("KIMI_SEARCH_STATUS_PREFIX", "Search results for query:"),
		ThinkingBudgets: map[string]int{
			"low":    4096,
			"medium": 16384,
			"high":   32768,
		},
	}
	if v := os.Getenv("KIMI_MODEL_MAP"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg.ModelMap)
	}
	if v := os.Getenv("KIMI_MODELS"); v != "" {
		cfg.Models = nil
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				cfg.Models = append(cfg.Models, m)
			}
		}
	}
	if v := os.Getenv("KIMI_THINKING_BUDGETS"); v != "" {
		_ = json.Unmarshal([]byte(v), &cfg.ThinkingBudgets)
	}
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
