# Repository Guidelines

## Project Structure & Module Organization

This is a Rust (tokio/axum) service: an OpenAI Responses API adapter for the
Kimi Code Anthropic API. It is stateless and holds no credentials.

- `src/main.rs` — entrypoint; loads env config and starts the axum server.
- `src/adapter/` — all adapter logic:
  - `config.rs` env-driven `Config`; `types.rs` Responses/Anthropic API types.
  - `convert.rs` Responses → Anthropic request conversion.
  - `stream.rs` Anthropic SSE → Responses SSE state machine (core).
  - `nonstream.rs` non-streaming response conversion.
  - `reasoning.rs` thinking/signature ↔ `encrypted_content` codec.
  - `models.rs` upstream model-metadata cache (lazy, TTL, single-flight).
  - `server.rs` routing, upstream calls, raw passthrough for non-Responses paths.
- Tests live next to the code in `#[cfg(test)]` modules; there are no assets
  or fixtures directories.

## Build, Test, and Development Commands

Toolchain and tasks are managed by mise (`mise install` once):

```sh
mise run build   # cargo build
mise run test    # cargo test
mise run lint    # cargo clippy --all-targets -- -D warnings
mise run fmt     # cargo fmt
mise run ci      # fmt --check + clippy + test (what CI runs)
mise run run     # serve on :8787 (LISTEN_ADDR to override)
```

## Coding Style & Naming Conventions

- Standard Rust: `cargo fmt`-clean, `cargo clippy --all-targets -- -D warnings`
  clean (run `mise run ci` before committing).
- Names mirror the protocol they belong to: `Anthropic*` types for the Kimi
  upstream side, `Responses*`/`InputItem` for the OpenAI client side.
- Emitted SSE/JSON payloads are built with `serde_json::json!` maps (sorted
  keys, matching Go's `map[string]any` output); parsed input uses serde
  structs with `#[serde(default)]` so unknown fields are ignored. Raw JSON
  that must round-trip byte-for-byte (tool arguments, schemas) is kept as
  `Box<serde_json::value::RawValue>`.
- Comment protocol quirks (e.g. web-search status suppression) where they
  are handled.

## Testing Guidelines

- Framework: standard `#[test]` / `#[tokio::test]`; mock upstreams are axum
  servers on ephemeral localhost ports; the adapter router is driven
  in-process via `tower::ServiceExt::oneshot`.
- Name tests after behavior (e.g. `stream_search_status_suppressed`).
- Protocol changes must add a canned-SSE test in `stream.rs` tests and/or a
  request-conversion case in `convert.rs` tests; server behavior goes in
  `server.rs` tests. Run `mise run test` — all tests must pass.

## Commit & Pull Request Guidelines

Use Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`) with a scoped
summary, e.g. `fix(stream): drop truncated search-status text blocks`.
PRs should describe the protocol behavior changed, link the motivating issue,
and include a captured upstream SSE sample when touching `stream.rs`.

## Release

`mise run release <x.y.z>` bumps `Cargo.toml`, commits, tags `v<x.y.z>` and
pushes both (dist requires the tag to match the package version, so never
tag by hand). `dist` (cargo-dist, see `dist-workspace.toml`) builds binaries
+ shell/PowerShell installers and publishes GitHub Releases via the generated
`release.yml` (do not hand-edit it; change `dist-workspace.toml` and run
`dist generate`). `.github/workflows/docker.yml` independently builds and
pushes the multi-arch GHCR image from the multi-stage `Dockerfile`.

## Security & Configuration Tips

- Never add API-key handling: credentials arrive per request and are forwarded
  to Kimi unchanged. Do not log request bodies or `Authorization` headers.
- All tunables are env vars (`KIMI_*`, `LISTEN_ADDR`); document new ones in
  `README.md` and `README_CN.md`.
