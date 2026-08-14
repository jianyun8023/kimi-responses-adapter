use std::convert::Infallible;
use std::io::Write as _;
use std::sync::Arc;
use std::time::{Duration, Instant};

use axum::body::{Body, Bytes};
use axum::extract::{Request, State};
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode, header};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::{Router, body};
use futures_util::{StreamExt, TryStreamExt};
use serde_json::{Value, json};
use tokio::sync::mpsc;
use tokio_stream::wrappers::UnboundedReceiverStream;
use tokio_util::codec::{FramedRead, LinesCodec};
use tokio_util::io::StreamReader;
use tracing::{error, info};

use crate::adapter::config::Config;
use crate::adapter::convert::build_anthropic_request;
use crate::adapter::models::ModelRegistry;
use crate::adapter::nonstream::anthropic_to_response;
use crate::adapter::stream::{StreamTranslator, now_unix, rand_id};
use crate::adapter::types::{AnthropicError, AnthropicMessageObj, ResponsesRequest};

pub struct AppState {
    pub cfg: Config,
    // No client-level timeout: streaming responses can run for minutes.
    // Cancellation propagates from the inbound request context.
    pub client: reqwest::Client,
    pub models: ModelRegistry,
}

pub fn router(cfg: Config) -> Router {
    let state = Arc::new(AppState {
        cfg,
        client: reqwest::Client::new(),
        models: ModelRegistry::new(Duration::from_secs(600)),
    });
    Router::new()
        .route("/v1/responses", any(responses_entry))
        .route("/healthz", any(healthz_entry))
        // Everything else (e.g. /v1/messages, /v1/chat/completions,
        // /v1/models) is proxied to the Kimi upstream byte-for-byte.
        .fallback(passthrough)
        .with_state(state)
}

async fn healthz_entry(State(state): State<Arc<AppState>>, req: Request) -> Response {
    if req.method() == Method::GET || req.method() == Method::HEAD {
        return json_response(StatusCode::OK, json!({"status": "ok"}));
    }
    passthrough(State(state), req).await
}

async fn responses_entry(State(state): State<Arc<AppState>>, req: Request) -> Response {
    if req.method() != Method::POST {
        return passthrough(State(state), req).await;
    }
    let start = Instant::now();
    let (parts, body) = req.into_parts();
    let inbound = parts.headers;
    let body = match body::to_bytes(body, 64 << 20).await {
        Ok(b) => b,
        Err(_) => {
            return json_error(
                StatusCode::BAD_REQUEST,
                "invalid request body",
                "invalid_request_error",
            );
        }
    };
    let req: ResponsesRequest = match serde_json::from_slice(&body) {
        Ok(r) => r,
        Err(_) => {
            return json_error(
                StatusCode::BAD_REQUEST,
                "invalid request body",
                "invalid_request_error",
            );
        }
    };

    if req.max_output_tokens <= 0 {
        state
            .models
            .ensure_fresh(&state.cfg.kimi_base_url, &auth_headers(&inbound))
            .await;
    }
    let resolver = |model: &str| state.models.lookup(model);
    let anth = match build_anthropic_request(&state.cfg, &req, Some(&resolver)) {
        Ok(a) => a,
        Err(e) => return json_error(StatusCode::BAD_REQUEST, &e, "invalid_request_error"),
    };
    let up_body = serde_json::to_vec(&anth).expect("request serializes");

    let mut headers = auth_headers(&inbound);
    headers.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
    if !state.cfg.anthropic_beta.is_empty() {
        if let Ok(v) = HeaderValue::from_str(&state.cfg.anthropic_beta) {
            headers.insert("anthropic-beta", v);
        }
    }
    headers.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    if anth.stream {
        headers.insert(
            header::ACCEPT,
            HeaderValue::from_static("text/event-stream"),
        );
    }

    let url = format!("{}/v1/messages", state.cfg.kimi_base_url);
    let resp = match state
        .client
        .post(url)
        .headers(headers)
        .body(up_body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            return json_error(
                StatusCode::BAD_GATEWAY,
                &format!("upstream request failed: {e}"),
                "api_error",
            );
        }
    };

    let status = resp.status();
    info!(
        model = %req.model,
        upstream_model = %anth.model,
        max_tokens = anth.max_tokens,
        stream = anth.stream,
        status = %status,
        "responses"
    );

    if status != StatusCode::OK {
        let err_body = read_limited(resp, 1 << 20).await.unwrap_or_default();
        return relay_upstream_error(req.stream, &req.model, status, &err_body);
    }

    if anth.stream {
        return stream_response(state, req, resp, start);
    }

    let resp_body = match read_limited(resp, 64 << 20).await {
        Ok(b) => b,
        Err(_) => {
            return json_error(
                StatusCode::BAD_GATEWAY,
                "failed reading upstream response",
                "api_error",
            );
        }
    };
    match serde_json::from_slice::<AnthropicMessageObj>(&resp_body) {
        Ok(msg) => json_response(
            StatusCode::OK,
            anthropic_to_response(&state.cfg, &req, &msg),
        ),
        Err(_) => json_error(
            StatusCode::BAD_GATEWAY,
            "invalid upstream response",
            "api_error",
        ),
    }
}

fn stream_response(
    state: Arc<AppState>,
    req: ResponsesRequest,
    resp: reqwest::Response,
    start: Instant,
) -> Response {
    let (tx, rx) = mpsc::unbounded_channel::<String>();
    let tx_check = tx.clone();
    let cfg = state.cfg.clone();
    let model = req.model.clone();
    tokio::spawn(async move {
        let mut debug = std::env::var("KIMI_DEBUG_SSE_FILE")
            .ok()
            .filter(|p| !p.is_empty())
            .and_then(|p| {
                std::fs::OpenOptions::new()
                    .create(true)
                    .append(true)
                    .open(&p)
                    .ok()
                    .inspect(|_| info!("debug: teeing upstream SSE to {p}"))
            });
        let byte_stream = resp.bytes_stream().map_err(std::io::Error::other);
        let reader = StreamReader::new(byte_stream);
        let mut framed = FramedRead::new(reader, LinesCodec::new_with_max_length(16 * 1024 * 1024));
        let mut t = StreamTranslator::new(&cfg, &req, move |s: String| {
            let _ = tx.send(s);
        });
        while let Some(line) = framed.next().await {
            // Client disconnected: stop consuming and drop the upstream body.
            if tx_check.is_closed() {
                return;
            }
            match line {
                Ok(l) => {
                    if let Some(f) = debug.as_mut() {
                        let _ = writeln!(f, "{l}");
                    }
                    t.feed_line(&l);
                    if t.is_done() {
                        break;
                    }
                }
                Err(e) => {
                    t.finish_eof();
                    error!("stream translation error: {e}");
                    return;
                }
            }
        }
        t.finish_eof();
        info!(model = %model, elapsed = ?start.elapsed(), "responses done");
    });

    let stream = UnboundedReceiverStream::new(rx).map(|s| Ok::<Bytes, Infallible>(Bytes::from(s)));
    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "text/event-stream")
        .header(header::CACHE_CONTROL, "no-cache")
        .body(Body::from_stream(stream))
        .expect("response builds")
}

fn relay_upstream_error(stream: bool, model: &str, status: StatusCode, body: &[u8]) -> Response {
    let mut msg = String::from_utf8_lossy(body).into_owned();
    #[derive(serde::Deserialize)]
    struct ErrorBody {
        error: Option<AnthropicError>,
    }
    if let Ok(parsed) = serde_json::from_slice::<ErrorBody>(body) {
        if let Some(e) = parsed.error {
            if !e.message.is_empty() {
                msg = e.message;
            }
        }
    }
    if stream {
        let failed = json!({
            "id": rand_id("resp_"),
            "object": "response",
            "created_at": now_unix(),
            "status": "failed",
            "model": model,
            "output": [],
            "error": {"code": "upstream_error", "message": msg},
        });
        let payload = json!({
            "type": "response.failed",
            "sequence_number": 0,
            "response": failed,
        });
        let frame = format!("event: response.failed\ndata: {payload}\n\n");
        return (
            StatusCode::OK,
            [
                (header::CONTENT_TYPE, "text/event-stream"),
                (header::CACHE_CONTROL, "no-cache"),
            ],
            frame,
        )
            .into_response();
    }
    json_response(
        status,
        json!({"error": {"message": msg, "type": "upstream_error"}}),
    )
}

/// Proxies any non-Responses endpoint to the Kimi upstream unchanged: same
/// method, path, query, body, and (streaming) response.
async fn passthrough(State(state): State<Arc<AppState>>, req: Request) -> Response {
    let (parts, body) = req.into_parts();
    let path_and_query = parts
        .uri
        .path_and_query()
        .map(|pq| pq.as_str().to_string())
        .unwrap_or_else(|| "/".to_string());
    let url = format!("{}{}", state.cfg.kimi_base_url, path_and_query);

    let mut headers = auth_headers(&parts.headers);
    headers.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
    if !state.cfg.anthropic_beta.is_empty() {
        if let Ok(v) = HeaderValue::from_str(&state.cfg.anthropic_beta) {
            headers.insert("anthropic-beta", v);
        }
    }
    copy_headers(&mut headers, &parts.headers);

    let method = parts.method.clone();
    let path_log = parts.uri.path().to_string();
    let up_body = reqwest::Body::wrap_stream(body.into_data_stream());
    let resp = match state
        .client
        .request(method.clone(), url)
        .headers(headers)
        .body(up_body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            return json_error(
                StatusCode::BAD_GATEWAY,
                &format!("upstream request failed: {e}"),
                "api_error",
            );
        }
    };

    let status = resp.status();
    let mut out_headers = HeaderMap::new();
    copy_headers(&mut out_headers, resp.headers());
    // Streaming body: chunks are written and flushed as they arrive, so SSE
    // streams reach the client incrementally.
    let stream = resp
        .bytes_stream()
        .map(|r| r.map_err(|e| Box::new(e) as Box<dyn std::error::Error + Send + Sync>));
    let mut response = Response::new(Body::from_stream(stream));
    *response.status_mut() = status;
    *response.headers_mut() = out_headers;
    info!(method = %method, path = %path_log, status = %status, "passthrough");
    response
}

/// Copies the inbound client credential (Authorization Bearer or x-api-key)
/// onto an upstream request. The adapter holds no keys of its own.
fn auth_headers(inbound: &HeaderMap) -> HeaderMap {
    let mut h = HeaderMap::new();
    if let Some(v) = inbound.get(header::AUTHORIZATION) {
        h.insert(header::AUTHORIZATION, v.clone());
    }
    if let Some(v) = inbound.get("x-api-key") {
        h.insert("x-api-key", v.clone());
    }
    h
}

const HOP_BY_HOP: [&str; 10] = [
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "content-length",
    "host",
];

fn copy_headers(dst: &mut HeaderMap, src: &HeaderMap) {
    let names: Vec<HeaderName> = src.keys().cloned().collect();
    for name in names {
        if HOP_BY_HOP.contains(&name.as_str()) {
            continue;
        }
        dst.remove(&name);
        for v in src.get_all(&name) {
            dst.append(&name, v.clone());
        }
    }
}

async fn read_limited(resp: reqwest::Response, limit: usize) -> Result<Bytes, reqwest::Error> {
    let mut stream = resp.bytes_stream();
    let mut buf = Vec::new();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk?;
        let remaining = limit.saturating_sub(buf.len());
        buf.extend_from_slice(&chunk[..chunk.len().min(remaining)]);
        if buf.len() >= limit {
            break;
        }
    }
    Ok(Bytes::from(buf))
}

fn json_error(status: StatusCode, message: &str, typ: &str) -> Response {
    json_response(status, json!({"error": {"message": message, "type": typ}}))
}

fn json_response(status: StatusCode, v: Value) -> Response {
    let mut s = serde_json::to_string(&v).unwrap_or_else(|_| "{}".to_string());
    s.push('\n');
    (status, [(header::CONTENT_TYPE, "application/json")], s).into_response()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::adapter::test_config;
    use http_body_util::BodyExt;
    use std::sync::Mutex;
    use tower::ServiceExt;

    const MINI_UPSTREAM_STREAM: &str = concat!(
        "event: message_start\n",
        "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
        "event: content_block_start\n",
        "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
        "event: content_block_delta\n",
        "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi there\"}}\n\n",
        "event: content_block_stop\n",
        "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
        "event: message_delta\n",
        "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n",
        "event: message_stop\n",
        "data: {\"type\":\"message_stop\"}\n\n",
    );

    const MINI_UPSTREAM_MESSAGE: &str = concat!(
        r#"{"id":"msg_1","type":"message","role":"assistant","model":"k3","#,
        r#""content":[{"type":"thinking","thinking":"hmm","signature":"sig-1"},"#,
        r#"{"type":"text","text":"Search results for query: x"},"#,
        r#"{"type":"server_tool_use","name":"web_search"},"#,
        r#"{"type":"web_search_tool_result","content":[]},"#,
        r#"{"type":"text","text":"the answer"}],"#,
        r#""stop_reason":"end_turn","#,
        r#""usage":{"input_tokens":100,"cache_read_input_tokens":50,"output_tokens":20,"output_tokens_details":{"thinking_tokens":5}}}"#,
    );

    #[derive(Default)]
    struct RecordedRequest {
        path: String,
        auth: String,
        api_key: String,
        body: String,
    }

    type SharedRec = Arc<Mutex<RecordedRequest>>;

    struct UpstreamCall {
        path: String,
        headers: HeaderMap,
        body: String,
    }

    /// Starts a mock upstream on an ephemeral localhost port and returns its
    /// base URL plus the shared request recorder.
    async fn spawn_upstream<F>(respond: F) -> (String, SharedRec)
    where
        F: Fn(&UpstreamCall) -> Response + Send + Sync + 'static,
    {
        let rec: SharedRec = Arc::new(Mutex::new(RecordedRequest::default()));
        let respond = Arc::new(respond);
        let app = Router::new().fallback({
            let rec = rec.clone();
            move |req: Request| {
                let rec = rec.clone();
                let respond = respond.clone();
                async move {
                    let (parts, body) = req.into_parts();
                    let bytes = body::to_bytes(body, usize::MAX).await.unwrap_or_default();
                    let call = UpstreamCall {
                        path: parts
                            .uri
                            .path_and_query()
                            .map(|pq| pq.as_str().to_string())
                            .unwrap_or_default(),
                        headers: parts.headers,
                        body: String::from_utf8_lossy(&bytes).into_owned(),
                    };
                    {
                        let mut r = rec.lock().unwrap();
                        r.path = call.path.clone();
                        r.auth = header_str(&call.headers, "authorization");
                        r.api_key = header_str(&call.headers, "x-api-key");
                        r.body = call.body.clone();
                    }
                    respond(&call)
                }
            }
        });
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        (format!("http://{addr}"), rec)
    }

    fn header_str(headers: &HeaderMap, name: &str) -> String {
        headers
            .get(name)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("")
            .to_string()
    }

    fn sse_response(body: &'static str) -> Response {
        Response::builder()
            .header(header::CONTENT_TYPE, "text/event-stream")
            .body(Body::from(body))
            .unwrap()
    }

    fn adapter_app(base_url: &str) -> Router {
        let mut cfg = test_config();
        cfg.kimi_base_url = base_url.to_string();
        router(cfg)
    }

    async fn post(app: &Router, path: &str, key: &str, body: &str) -> Response {
        let mut b = Request::builder()
            .method(Method::POST)
            .uri(path)
            .header(header::CONTENT_TYPE, "application/json");
        if !key.is_empty() {
            b = b.header(header::AUTHORIZATION, format!("Bearer {key}"));
        }
        app.clone()
            .oneshot(b.body(Body::from(body.to_string())).unwrap())
            .await
            .unwrap()
    }

    async fn body_string(resp: Response) -> (StatusCode, String) {
        let status = resp.status();
        let bytes = resp.into_body().collect().await.unwrap().to_bytes();
        (status, String::from_utf8_lossy(&bytes).into_owned())
    }

    #[tokio::test]
    async fn end_to_end_responses_stream() {
        let (base, rec) = spawn_upstream(|_| sse_response(MINI_UPSTREAM_STREAM)).await;
        let app = adapter_app(&base);

        let resp = post(
            &app,
            "/v1/responses",
            "client-kimi-key",
            r#"{"model":"k3","stream":true,"input":"hello"}"#,
        )
        .await;
        let (status, out) = body_string(resp).await;
        assert_eq!(status, StatusCode::OK, "body: {out}");
        assert!(
            out.contains("event: response.completed"),
            "missing response.completed:\n{out}"
        );
        assert!(out.contains("hi there"), "missing text delta:\n{out}");

        let rec = rec.lock().unwrap();
        assert_eq!(rec.path, "/v1/messages");
        assert_eq!(rec.auth, "Bearer client-kimi-key");
        assert!(
            rec.body.contains(r#""thinking":{"type":"enabled""#),
            "upstream body missing thinking config: {}",
            rec.body
        );
    }

    #[tokio::test]
    async fn passthrough_chat_completions() {
        let (base, rec) = spawn_upstream(|_| {
            Response::builder()
                .header(header::CONTENT_TYPE, "application/json")
                .body(Body::from(
                    r#"{"id":"chatcmpl-1","object":"chat.completion","choices":[]}"#,
                ))
                .unwrap()
        })
        .await;
        let app = adapter_app(&base);

        let resp = post(
            &app,
            "/v1/chat/completions",
            "client-kimi-key",
            r#"{"model":"k3","messages":[{"role":"user","content":"hi"}]}"#,
        )
        .await;
        let (status, body) = body_string(resp).await;
        assert_eq!(status, StatusCode::OK);
        assert!(
            body.contains("chatcmpl-1"),
            "passthrough response wrong: {body}"
        );

        let rec = rec.lock().unwrap();
        assert_eq!(rec.path, "/v1/chat/completions");
        assert!(
            rec.body.contains(r#""messages""#),
            "passthrough body modified: {}",
            rec.body
        );
        assert_eq!(rec.auth, "Bearer client-kimi-key");
    }

    #[tokio::test]
    async fn passthrough_streams_incrementally() {
        let (base, _rec) = spawn_upstream(|_| sse_response(MINI_UPSTREAM_STREAM)).await;
        let app = adapter_app(&base);

        let resp = post(
            &app,
            "/v1/messages",
            "client-kimi-key",
            r#"{"model":"k3","stream":true,"messages":[]}"#,
        )
        .await;
        let (_status, body) = body_string(resp).await;
        assert!(
            body.contains("event: message_start"),
            "passthrough SSE body wrong:\n{body}"
        );
    }

    #[tokio::test]
    async fn x_api_key_forwarded() {
        let (base, rec) = spawn_upstream(|_| sse_response(MINI_UPSTREAM_STREAM)).await;
        let app = adapter_app(&base);

        let req = Request::builder()
            .method(Method::POST)
            .uri("/v1/responses")
            .header(header::CONTENT_TYPE, "application/json")
            .header("x-api-key", "xkimi-key")
            .body(Body::from(r#"{"model":"k3","stream":true,"input":"hi"}"#))
            .unwrap();
        let resp = app.clone().oneshot(req).await.unwrap();
        let _ = body_string(resp).await;
        assert_eq!(rec.lock().unwrap().api_key, "xkimi-key");
    }

    // ---- positive cases ----

    #[tokio::test]
    async fn end_to_end_responses_non_stream() {
        let (base, rec) = spawn_upstream(|_| {
            Response::builder()
                .header(header::CONTENT_TYPE, "application/json")
                .body(Body::from(MINI_UPSTREAM_MESSAGE))
                .unwrap()
        })
        .await;
        let app = adapter_app(&base);

        let resp = post(
            &app,
            "/v1/responses",
            "client-kimi-key",
            r#"{"model":"k3","stream":false,"input":"hello"}"#,
        )
        .await;
        let (status, body) = body_string(resp).await;
        assert_eq!(status, StatusCode::OK, "body: {body}");
        let out: Value = serde_json::from_str(&body).expect("bad JSON");
        assert_eq!(out["status"], "completed");
        let output = out["output"].as_array().unwrap();
        // reasoning + web_search_call + message; status text suppressed.
        assert_eq!(output.len(), 3, "expected 3 output items: {body}");
        let usage = &out["usage"];
        assert_eq!(usage["input_tokens"], 150, "100 + 50 cached");

        let rec = rec.lock().unwrap();
        assert!(
            !rec.body.contains(r#""stream":true"#),
            "stream flag leaked to upstream: {}",
            rec.body
        );
    }

    #[tokio::test]
    async fn passthrough_preserves_query_string() {
        let (base, rec) = spawn_upstream(|_| Response::new(Body::from("{}"))).await;
        let app = adapter_app(&base);

        let req = Request::builder()
            .uri("/v1/models?limit=5&after=x")
            .body(Body::empty())
            .unwrap();
        let resp = app.clone().oneshot(req).await.unwrap();
        let _ = body_string(resp).await;
        assert_eq!(rec.lock().unwrap().path, "/v1/models?limit=5&after=x");
    }

    #[tokio::test]
    async fn anthropic_headers_set() {
        let seen: Arc<Mutex<(String, String)>> =
            Arc::new(Mutex::new((String::new(), String::new())));
        let (base, _rec) = spawn_upstream({
            let seen = seen.clone();
            move |call: &UpstreamCall| {
                let mut s = seen.lock().unwrap();
                s.0 = header_str(&call.headers, "anthropic-version");
                s.1 = header_str(&call.headers, "anthropic-beta");
                sse_response(MINI_UPSTREAM_STREAM)
            }
        })
        .await;
        let mut cfg = test_config();
        cfg.kimi_base_url = base;
        cfg.anthropic_beta = "interleaved-thinking-2025-05-14".to_string();
        let app = router(cfg);

        let resp = post(
            &app,
            "/v1/responses",
            "k",
            r#"{"model":"k3","stream":true,"input":"hi"}"#,
        )
        .await;
        let _ = body_string(resp).await;
        let s = seen.lock().unwrap();
        assert_eq!(s.0, "2023-06-01");
        assert_eq!(s.1, "interleaved-thinking-2025-05-14");
    }

    #[tokio::test]
    async fn model_metadata_used_for_max_tokens() {
        let (base, rec) = spawn_upstream(|call: &UpstreamCall| {
            if call.path == "/v1/models" {
                return Response::new(Body::from(
                    r#"{"data":[{"id":"k3","max_output_tokens":65536}]}"#,
                ));
            }
            sse_response(MINI_UPSTREAM_STREAM)
        })
        .await;
        let app = adapter_app(&base);

        let resp = post(
            &app,
            "/v1/responses",
            "k",
            r#"{"model":"k3","stream":true,"input":"hi"}"#,
        )
        .await;
        let _ = body_string(resp).await;
        assert!(
            rec.lock().unwrap().body.contains(r#""max_tokens":65536"#),
            "model metadata max_tokens not used"
        );
    }

    // ---- negative cases ----

    #[tokio::test]
    async fn invalid_body_rejected() {
        let (base, _rec) =
            spawn_upstream(|_| panic!("upstream must not be called on invalid body")).await;
        let app = adapter_app(&base);
        let resp = post(&app, "/v1/responses", "k", "{not json").await;
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn upstream_error_relayed_non_stream() {
        let (base, _rec) = spawn_upstream(|_| {
            Response::builder()
                .status(StatusCode::UNAUTHORIZED)
                .header(header::CONTENT_TYPE, "application/json")
                .body(Body::from(
                    r#"{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}"#,
                ))
                .unwrap()
        })
        .await;
        let app = adapter_app(&base);
        let resp = post(
            &app,
            "/v1/responses",
            "bad-key",
            r#"{"model":"k3","stream":false,"input":"hi"}"#,
        )
        .await;
        let (status, body) = body_string(resp).await;
        assert_eq!(
            status,
            StatusCode::UNAUTHORIZED,
            "upstream status should be relayed"
        );
        assert!(
            body.contains("invalid api key"),
            "upstream error message lost: {body}"
        );
    }

    #[tokio::test]
    async fn upstream_error_relayed_stream() {
        let (base, _rec) = spawn_upstream(|_| {
            Response::builder()
                .status(529) // Anthropic overloaded
                .header(header::CONTENT_TYPE, "application/json")
                .body(Body::from(
                    r#"{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}"#,
                ))
                .unwrap()
        })
        .await;
        let app = adapter_app(&base);
        let resp = post(
            &app,
            "/v1/responses",
            "k",
            r#"{"model":"k3","stream":true,"input":"hi"}"#,
        )
        .await;
        let (status, body) = body_string(resp).await;
        // Stream clients always get 200 + a terminal response.failed event.
        assert_eq!(
            status,
            StatusCode::OK,
            "stream errors should surface as SSE"
        );
        assert!(
            body.contains("event: response.failed"),
            "response.failed missing:\n{body}"
        );
        assert!(
            body.contains("Overloaded"),
            "error message missing:\n{body}"
        );
    }

    #[tokio::test]
    async fn upstream_unreachable() {
        let app = adapter_app("http://127.0.0.1:1"); // nothing listening
        let resp = post(
            &app,
            "/v1/responses",
            "k",
            r#"{"model":"k3","stream":false,"input":"hi"}"#,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::BAD_GATEWAY);
    }
}
