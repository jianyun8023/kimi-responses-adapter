pub mod config;
pub mod convert;
pub mod models;
pub mod nonstream;
pub mod reasoning;
pub mod server;
pub mod stream;
pub mod types;

#[cfg(test)]
pub(crate) fn test_config() -> config::Config {
    config::Config {
        listen_addr: String::new(),
        kimi_base_url: String::new(),
        anthropic_beta: String::new(),
        model_map: Default::default(),
        models: vec![],
        max_tokens: 32768,
        thinking_budgets: [
            ("low".to_string(), 4096),
            ("medium".to_string(), 16384),
            ("high".to_string(), 32768),
        ]
        .into_iter()
        .collect(),
        search_status_prefix: "Search results for query:".to_string(),
    }
}
