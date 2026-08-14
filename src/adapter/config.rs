use std::collections::HashMap;

/// Config holds all adapter configuration. Everything is driven by
/// environment variables so the binary stays a thin, stateless proxy.
#[derive(Clone, Debug)]
pub struct Config {
    /// Address the HTTP server binds to.
    pub listen_addr: String,
    /// Base URL of the Kimi Code Anthropic-compatible API.
    pub kimi_base_url: String,
    /// Sent as the anthropic-beta header when non-empty.
    pub anthropic_beta: String,
    /// Maps incoming Responses model names to upstream Kimi models.
    /// Unmapped names are passed through unchanged.
    pub model_map: HashMap<String, String>,
    /// The list returned by GET /v1/models.
    pub models: Vec<String>,
    /// Default Anthropic max_tokens when the client does not specify
    /// max_output_tokens.
    pub max_tokens: i64,
    /// Maps reasoning effort to Anthropic thinking budget.
    pub thinking_budgets: HashMap<String, i64>,
    /// Marks Kimi's web-search status text blocks
    /// (e.g. "Search results for query: ...") that must be suppressed.
    pub search_status_prefix: String,
}

impl Config {
    pub fn load() -> Config {
        let mut cfg = Config {
            listen_addr: env_or("LISTEN_ADDR", ":8787"),
            kimi_base_url: env_or("KIMI_BASE_URL", "https://api.kimi.com/coding")
                .trim_end_matches('/')
                .to_string(),
            anthropic_beta: std::env::var("KIMI_ANTHROPIC_BETA").unwrap_or_default(),
            model_map: HashMap::new(),
            models: vec![
                "k3".to_string(),
                "k3-256k".to_string(),
                "kimi-for-coding".to_string(),
                "kimi-for-coding-highspeed".to_string(),
            ],
            max_tokens: env_int("KIMI_MAX_TOKENS", 32768),
            search_status_prefix: env_or("KIMI_SEARCH_STATUS_PREFIX", "Search results for query:"),
            thinking_budgets: [
                ("low".to_string(), 4096),
                ("medium".to_string(), 16384),
                ("high".to_string(), 32768),
            ]
            .into_iter()
            .collect(),
        };
        if let Ok(v) = std::env::var("KIMI_MODEL_MAP") {
            if !v.is_empty() {
                if let Ok(m) = serde_json::from_str(&v) {
                    cfg.model_map = m;
                }
            }
        }
        if let Ok(v) = std::env::var("KIMI_MODELS") {
            if !v.is_empty() {
                cfg.models = v
                    .split(',')
                    .map(str::trim)
                    .filter(|m| !m.is_empty())
                    .map(str::to_string)
                    .collect();
            }
        }
        if let Ok(v) = std::env::var("KIMI_THINKING_BUDGETS") {
            if !v.is_empty() {
                if let Ok(b) = serde_json::from_str(&v) {
                    cfg.thinking_budgets = b;
                }
            }
        }
        cfg
    }
}

fn env_or(key: &str, def: &str) -> String {
    match std::env::var(key) {
        Ok(v) if !v.is_empty() => v,
        _ => def.to_string(),
    }
}

fn env_int(key: &str, def: i64) -> i64 {
    match std::env::var(key) {
        Ok(v) if !v.is_empty() => v.parse().unwrap_or(def),
        _ => def,
    }
}
