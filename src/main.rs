mod adapter;

use tracing::info;

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .with_max_level(tracing::Level::INFO)
        .init();

    let cfg = adapter::config::Config::load();
    info!("kimi-responses-adapter listening on {}", cfg.listen_addr);
    info!(
        "upstream: {} (client credentials are forwarded; no keys held locally)",
        cfg.kimi_base_url
    );

    let app = adapter::server::router(cfg.clone());
    let addr = normalize_listen_addr(&cfg.listen_addr);
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .unwrap_or_else(|e| panic!("bind {addr}: {e}"));
    if let Err(e) = axum::serve(listener, app).await {
        tracing::error!("server error: {e}");
        std::process::exit(1);
    }
}

/// Go-style ":8787" binds all interfaces; std/axum need an explicit host.
fn normalize_listen_addr(addr: &str) -> String {
    match addr.strip_prefix(':') {
        Some(port) => format!("0.0.0.0:{port}"),
        None => addr.to_string(),
    }
}
