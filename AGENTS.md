# Repository Guidelines

## Project Structure & Module Organization

This is a Go (stdlib-only) service: an OpenAI Responses API adapter for the
Kimi Code Anthropic API. It is stateless and holds no credentials.

- `cmd/kimi-responses-adapter/main.go` — entrypoint; loads env config and starts HTTP.
- `internal/adapter/` — all adapter logic:
  - `config.go` env-driven `Config`; `responses.go` / `anthropic.go` API types.
  - `convert.go` Responses → Anthropic request conversion.
  - `stream.go` Anthropic SSE → Responses SSE state machine (core).
  - `nonstream.go` non-streaming response conversion.
  - `reasoning.go` thinking/signature ↔ `encrypted_content` codec.
  - `server.go` routing, upstream calls, raw passthrough for non-Responses paths.
- Tests live next to the code as `*_test.go`; there are no assets or fixtures directories.

## Build, Test, and Development Commands

```sh
go build ./...     # compile all packages
go vet ./...       # static analysis
go test ./...      # run all unit + httptest-based end-to-end tests
go run ./cmd/kimi-responses-adapter   # serve on :8787 (LISTEN_ADDR to override)
```

No module downloads are needed; the module has zero external dependencies.

## Coding Style & Naming Conventions

- Standard Go: `gofmt`-clean (run `gofmt -w .` before committing), tabs for indentation.
- Names mirror the protocol they belong to: `Anthropic*` types for the Kimi
  upstream side, `Responses*`/`InputItem` for the OpenAI client side.
- Keep the adapter dependency-free; prefer `map[string]any` for emitted SSE
  payloads and structs for parsed input.
- Exported symbols need no doc comments unless non-obvious; comment protocol
  quirks (e.g. web-search status suppression) where they are handled.

## Testing Guidelines

- Framework: standard `testing` plus `net/http/httptest` for upstream mocks.
- Name tests `Test<Behavior>` (e.g. `TestStreamSearchStatusSuppressed`).
- Protocol changes must add a canned-SSE test in `stream_test.go` and/or a
  request-conversion case in `convert_test.go`; server behavior goes in
  `server_test.go`. Run `go test ./...` — all tests must pass.

## Commit & Pull Request Guidelines

The repository has no commit history yet; use Conventional Commits
(`feat:`, `fix:`, `test:`, `docs:`) with a scoped summary, e.g.
`fix(stream): drop truncated search-status text blocks`.
PRs should describe the protocol behavior changed, link the motivating issue,
and include a captured upstream SSE sample when touching `stream.go`.

## Security & Configuration Tips

- Never add API-key handling: credentials arrive per request and are forwarded
  to Kimi unchanged. Do not log request bodies or `Authorization` headers.
- All tunables are env vars (`KIMI_*`, `LISTEN_ADDR`); document new ones in
  `README.md`.
