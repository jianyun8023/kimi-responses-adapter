# kimi-responses-adapter

[English](README.md) | 中文

面向 Kimi Code Anthropic API 的 OpenAI Responses API 兼容层。

一个刻意保持轻薄、无状态、单协议方向的适配器：

```text
Codex
  │ Responses API
  ▼
kimi-responses-adapter       （只做协议适配）
  │ Anthropic Messages API
  ▼
api.kimi.com/coding
```

本适配器只负责把 `POST /v1/responses` 翻译成 Kimi 的 Anthropic Messages API
并转回，避免通用 Responses↔Anthropic 桥带来的信息损失：

- **Thinking 闭环**：Kimi 的 `thinking` + `signature` 块被装进 Responses 的
  `reasoning.encrypted_content`（自包含 payload），下一轮请求逐字节还原。
  回传时也兼容「裸 signature + summary 文本」的简化形式。
- **联网搜索**：Kimi 的 `web_search_20250305` 服务端工具转为 Responses 的
  `web_search_call` item（含 query 和 sources）；Kimi 的
  `Search results for query: ...` 状态文本会被抑制，不会泄漏进
  `output_text`。
- **函数调用**：`function_call` / `function_call_output` ↔ `tool_use` /
  `tool_result`，包含增量式的 `function_call_arguments.delta`。
- **其余接口原样透传**：任何非 Responses 路径（`/v1/messages`、
  `/v1/chat/completions`、`/v1/models` 等）字节级转发到 Kimi 上游，
  包括流式响应。

适配器不保存任何状态、不持有任何凭证：会话历史随每次请求到达并被确定性地
重建为 Anthropic messages；客户端的 API key（`Authorization: Bearer ...` 或
`x-api-key`）逐请求原样转发给 Kimi。认证方面没有任何需要配置的东西。

## 端点

| 端点                  | 行为                                  |
| --------------------- | ------------------------------------- |
| `POST /v1/responses`  | Responses ↔ Anthropic 协议适配        |
| `GET /healthz`        | 健康检查                              |
| 其余所有路径          | 原样透传到 Kimi 上游                  |

## 支持范围

- `stream=true` 与 `stream=false`
- `input_text`、`input_image`（data URI 或 URL）
- function tools、`function_call`、`function_call_output`
- `reasoning` effort → thinking budget；`encrypted_content` ↔ Kimi signature
- `web_search` / `web_search_preview` → `web_search_20250305`
- usage 映射（`input_tokens`、`output_tokens`、cached tokens）
- `max_tokens` 截断 → `response.incomplete`

有意不支持：Chat Completions 适配、对外 Anthropic 协议、Gemini、OAuth、
多厂商、用户/计费。已完成的 `web_search_call` 会回放到上游上下文，避免
Agent 后续轮次重复执行同一次服务端搜索。

## 配置

全部通过环境变量配置：

| 变量                        | 默认值                                     | 说明 |
| --------------------------- | ------------------------------------------ | ---- |
| `LISTEN_ADDR`               | `:8787`                                    | 监听地址 |
| `KIMI_BASE_URL`             | `https://api.kimi.com/coding`              | 上游 base URL |
| `KIMI_ANTHROPIC_BETA`       | （空）                                     | 可选的上游 `anthropic-beta` 头 |
| `KIMI_MODEL_MAP`            | `{"codex-auto-review":"kimi-for-coding-highspeed"}` | Responses 模型映射；环境变量在默认值上合并，同名键可覆盖默认值 |
| `KIMI_CLIENT_SOURCE`        | （空）                                     | 覆盖上游 User-Agent；为空时透传客户端 User-Agent |
| `KIMI_MAX_TOKENS`           | `32768`                                    | 兜底 `max_tokens`，仅当客户端和模型元数据都未提供时使用 |
| `KIMI_THINKING_BUDGETS`     | `{"low":4096,"medium":16384,"high":32768}` | effort → budget；`minimal`/`none` 关闭 thinking |
| `KIMI_SEARCH_STATUS_PREFIX` | `Search results for query:`                | 需要抑制的状态文本前缀 |
| `KIMI_DEBUG_SSE_FILE`       | （空）                                     | 设置后把上游原始 SSE 转储到该文件，用于调试 |

thinking budget 会被钳制在 `max_tokens` 之下。开启 thinking 时，按
Anthropic 的要求丢弃采样参数（`temperature`、`top_p`）。

## 模型元数据

模型的上下文窗口、最大输出 token 数等限制会从 Kimi Code 上游自动收集，
而不是写死在代码里：适配器惰性调用 `GET {KIMI_BASE_URL}/v1/models`
（转发当前请求的凭证），兼容 `{"data": [...]}` 和裸数组两种返回结构，
结果缓存 10 分钟。已知 Kimi Code 模型带有内置默认值，因此拉取失败或上游
不返回元数据也不影响运行。

`max_tokens` 取值优先级：客户端 `max_output_tokens` → 上游模型元数据 →
`KIMI_MAX_TOKENS`。每个请求的实际取值会写入日志。

## 运行

```sh
cargo build --release
./target/release/kimi-responses-adapter
```

或使用已发布的容器镜像（多架构：`linux/amd64`、`linux/arm64`）：

```sh
docker run --rm -p 8787:8787 ghcr.io/jianyun8023/kimi-responses-adapter:latest
```

本适配器是纯粹的协议转换器和代理：它不做认证终结。请部署在可信网段，
不要直接暴露在公网。

## 发布

推送 semver tag 即可触发发布。[dist](https://opensource.axo.dev/cargo-dist/)（见
[dist-workspace.toml](dist-workspace.toml)）会为 Linux/macOS（amd64 + arm64，
Linux 使用 musl 静态链接）和 Windows（amd64）构建二进制，把归档、
shell/PowerShell 安装器和校验和发布到 GitHub Releases；另有一个独立工作流
向 GHCR 推送多架构镜像：

```sh
mise run release 0.1.0
```

该任务会 bump `Cargo.toml` 中的 `version`、提交、打 `v0.1.0` tag 并一并推送
—— dist 要求 tag 与包版本一致，手工发版（只 `git tag` 不 bump 版本）会导致
Release 工作流失败。

## 开发

工具链与任务由 [mise](https://mise.jdx.dev) 管理（先执行一次
`mise install`，然后）：

```sh
mise run build   # cargo build
mise run test    # cargo test
mise run lint    # cargo clippy --all-targets -- -D warnings
mise run fmt     # cargo fmt
mise run ci      # fmt --check + clippy + test（与 CI 一致）
```
