# Harness and magpie formats

This document records the on-disk formats, command lines, and HTTP APIs that
Phase 3 (`internal/magpie`, `internal/harness`) depends on. Each fact has a
tag:

- **Verified**: checked on 2026-11-10 in upstream source, official
  documentation, or real files of a current release.
- **Inferred**: not found in a primary source. The code follows the most
  likely shape and the item is listed here so that a maintainer can confirm
  it against a real installation.

All paths are relative to the guest home `/home/agent`. The host reads and
writes them in `$LYDIABOX_DATA_DIR/mnt/jfs/home/agent` through
`internal/harness/safefile`, which rejects every path that resolves outside
that tree.

## magpie (`yetone/magpie` v0.1.3531, headless `magpie-cli` build)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Server command | `magpie serve --web=127.0.0.1:3426` | Verified (`internal/cli/main.go`) |
| Gateway address | `MAGPIE_ADDR=127.0.0.1:3425` (3425 is the default port; there is no `--addr` flag) | Verified (`internal/gui/port.go`, `docs/reference.md`) |
| Data directory | `$XDG_CONFIG_HOME/magpie` (no data-dir flag). LydiaBox sets `XDG_CONFIG_HOME` and `XDG_STATE_HOME` to `$DATA_DIR/state`, so the data is in `$DATA_DIR/state/magpie`; `XDG_CACHE_HOME` is `$DATA_DIR/cache` | Verified (`internal/appdir/appdir.go`) |
| Files | `settings.json`, `providers.json` (`{"providers":[…],"groups":[…]}`), `caller-keys.json`, `usage.jsonl` | Verified |
| Private `HOME` | `$DATA_DIR/state/magpie/home`, so magpie does not detect or rewire the agent configs of the host user | Inferred (magpie detects local agents through `HOME`) |
| Health | `GET /api/hello` on the gateway gives `{"name":"magpie","version":"…"}` | Verified (`docs/integrating.md`) |
| Admin API | The web page API on the `--web` address. Authentication: cookie `magpie_web_<port>` that holds `MAGPIE_WEB_KEY` (16 or more characters). Requests without `Origin` and `Sec-Fetch-Site` pass the cross-origin check | Verified (`internal/gui/web.go`) |
| Providers | `GET /api/providers` (`providers[]` with `id`, `name`, `preset`, `chat`, `responses`, `anthropic`, `gemini`, `off`, `ready`, `key.set`, `models[].id`); `POST /api/provider/{save,on,off,delete,key}` with a `provider.Provider` body plus `new` | Verified (`internal/gui/providers.go`) |
| Provider save semantics | A save without `key` is sent with the current key (read with `/api/provider/key`) | Inferred |
| Routing groups | `GET /api/groups`; `POST /api/groups/{save,delete}`; group fields `id`, `name`, `members` (`provider/model` or `group/<id>`), `routing` (`order` = fallback, `rotate` = round robin) | Verified (`internal/gui/groups.go`, `internal/provider/group.go`) |
| Gateway keys | `GET /api/caller-keys`; `POST /api/caller-keys/{add-key,remove-key,on-key,off-key}` with `{"name","secret","key"}`. A caller-provided secret must be 8 to 256 characters without spaces and must not start with `sk-magpie-` | Verified (`internal/gui/caller_keys.go`, `internal/access/access.go`) |
| Key header | `Authorization: Bearer <key>`, `x-api-key`, or `x-goog-api-key`. Loopback callers may use any token | Verified (`docs/reference.md`) |
| Model list | `GET /v1/models` on the gateway; IDs are `provider/model` | Verified |
| Context window field in `/v1/models` | `context_window`, `context_length`, or `max_context_length` (first non-zero) | Inferred |

The VM gateway key is named `lydiabox-vm`. LydiaBox generates the secret
(`lbx-gw-` + 48 hex digits), stores it AES-256-GCM encrypted in the
`magpie_secrets` table (migration `0200_magpie_secrets`), and registers it
with `add-key`. The guest only knows the placeholder `lydiabox-local`; the
host side of the vsock tunnel calls `Supervisor.InjectHeaders`, which sets
`Authorization` and `X-Api-Key` to the real key.

## Claude Code (`claude`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Transcript | `.claude/projects/<encoded cwd>/<session uuid>.jsonl`; the encoded cwd replaces every character that is not a letter or digit with `-` (`/workspace/lydiabox` gives `-workspace-lydiabox`) | Verified (docs and real files) |
| Records | `type` `user`/`assistant`/`system`/`summary`/`custom-title`/`file-history-snapshot`/…; `uuid`, `parentUuid`, `sessionId`, `cwd`, `timestamp`, `isSidechain`, `isMeta`, `message.{role,content,model,usage}`; content blocks `text`, `thinking`, `tool_use`, `tool_result` (`is_error`) | Verified |
| Usage | One API message is written as several lines with the same `message.id`; usage is counted once per ID. Input tokens = `input_tokens` + `cache_creation_input_tokens` + `cache_read_input_tokens` | Verified |
| Start / resume | `claude --session-id <uuid>`, `claude --resume <id> [prompt]`, `--model` | Verified |
| Config | `.claude/settings.json` `env.ANTHROPIC_BASE_URL`, `env.ANTHROPIC_AUTH_TOKEN`, `model`; user-scope MCP servers in `.claude.json` `mcpServers.<name> = {"type":"http","url":…}` | Verified |

## Codex CLI (`codex`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Rollout | `.codex/sessions/YYYY/MM/DD/rollout-<local time>-<uuid>.jsonl`; new IDs are UUIDv7 | Verified |
| Current lines | `{timestamp, type, payload}`; `session_meta` (`id`, `cwd`), `turn_context` (`model`, `cwd`), `response_item` (`message` with `input_text`/`output_text`, `function_call`, `function_call_output`, `custom_tool_call` such as `apply_patch`, `custom_tool_call_output`, `reasoning`), `event_msg` (`token_count` with `info.total_token_usage`) | Verified |
| Old lines (2025) | First line `{id, timestamp, instructions, git}`, then `{"record_type":"state"}` and bare items | Verified |
| Titles | `.codex/session_index.jsonl` (`id`, `thread_name`) | Verified |
| Start / resume | `codex [--model m]`, `codex resume <id> [prompt]` | Verified |
| Config | `model`, `model_provider`, `[model_providers.lydiabox]` (`name`, `base_url`, `env_key`, `wire_api = "responses"`), `[mcp_servers.<name>] url` | Verified (`wire_api` accepts only `responses` now) |

## dsh (DeepSeek Harness, `@deepseek-ai/dsh`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Home | `$DSH_HOME` or `.dsh` | Verified (`docs/config-catalog.md`) |
| Sessions | `.dsh/sessions/<projectKey>/<encodedId>/session[.vN].jsonl[.zstd]`, zstd by default; `projectKey` is `--` + cwd with `/`, `\`, `:` as `-` and other unsafe characters as `~XXXX` + `--` | Verified (`docs/persistence-catalog.md`, `session-persistence-jsonl/src/format.ts`) |
| Lines | Header `{type:"session", version, id, createdAt, cwd, isSeeded, delegationDepth, origin?}`; events `{type, seq, time, data, ignorable?}`; `user/message` (`source.kind` is `user` for human prompts), `assistant/message` (`message.content[]` with `text` and `tool-call` blocks, `message.source.{model,provider}`, `usage.{inputTokens,outputTokens}`), `tool/result` (`message.toolCallId`, `isError`) | Verified |
| Resume | `dsh tui --resume <id>`; headless `--session-id` | Verified (`docs/cli-help.md`) |
| `encodedId` for IDs with only safe characters | The ID itself | Inferred |
| `.dsh/.env` and `DSH_MODEL` | dsh has no documented `.env` support. The launcher runs dsh through `sh -c 'set -a; . ~/.dsh/.env; …'`, so `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `DEEPSEEK_BASE_URL`, the `*_API_KEY` placeholders, and `DSH_MODEL` reach the process | Inferred |

## Antigravity (`agy`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Conversations | `.gemini/antigravity/conversations/<uuid>.db`: SQLite, table `steps(idx, step_type, status, step_payload BLOB, metadata BLOB)`, payloads are protobuf without a public schema | Verified (real files) |
| Artifacts | `.gemini/antigravity/brain/<uuid>/` (`task.md` and others), `annotations/<uuid>.pbtxt` (text protobuf with `title`) | Verified (layout); `title` field Inferred |
| Step types | 14 user input, 15 planner response, 5 code action, 8 view file | Inferred |
| Payload decoding | Schema-free protobuf walk; the longest string of a user or planner step is its text; paths are strings that look like file paths | Inferred |
| CLI | `agy [--model m] [--conversation <id>] [-i/--prompt-interactive <text>]`; `-p/--print`, `--settings`, `agy mcp list`, `agy models` | Verified (`agy --help`, agy 1.5.4) |
| Config (design path) | `.antigravity/settings.json`: `endpoint`, `apiKey`, `model`, `env.GOOGLE_GEMINI_BASE_URL`, `env.GEMINI_API_KEY`. The CLI does not read this file; the wirer writes it because design section 4.4.1 names it, and `CurrentModel` reads `model` from it | Design path |
| Config (CLI) | `.gemini/antigravity-cli/settings.json` (schema `https://antigravity.google/schemas/v1/settings.json`): `"modelProvider": "gemini"` and `customModelsConfig.customModels.<model> = {"modelName": "gemini-api://<model>", "label": "<model>"}`. With `GEMINI_API_KEY` and `GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425` in the environment (wirer `Env` and `ptymanager.DefaultEnv`), `agy --model <model>` sends `POST /v1beta/models/<model>:streamGenerateContent?alt=sse` with `X-Goog-Api-Key` to the gateway. The CLI `model` key is not written: agy 1.5.4 resolves a saved custom-model label to an empty model in print mode (`tModel: model is empty`), and without `GEMINI_API_KEY` it refuses to start when `modelProvider` is `gemini` | Verified (schema, docs, and agy 1.5.4 against a capture server) |
| MCP | `.gemini/config/mcp_config.json` (documented global path; `agy mcp list` shows both servers) and `.gemini/antigravity/mcp_config.json` (design path), as `mcpServers.<name>.serverUrl` | Verified (docs, agy 1.5.4) |

## opencode (`sst/opencode`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Store (1.x) | `.local/share/opencode/opencode.db` (SQLite): `session` (`id`, `parent_id`, `directory`, `title`, `time_created`, `time_updated`, `time_archived`), `message` (`id`, `session_id`, `data` JSON with `role`, `modelID`, `providerID`, `cost`, `tokens`), `part` (`id`, `message_id`, `session_id`, `data` JSON: `text`, `tool` with `state.{status,input,output,error}`, `patch` with `files`) | Verified (real database of opencode 1.18) |
| Store (legacy) | `.local/share/opencode/storage/{session/<project>/<id>.json, message/<sessionID>/<id>.json, part/<messageID>/<id>.json}` | Verified (older releases) |
| IDs | `ses_`, `msg_`, `prt_` + time-ordered suffix | Verified |
| Start / resume | `opencode [--session id] [--model provider/model] [--prompt text]`; `opencode import <file>` takes `opencode export` JSON | Verified |
| Config | `.config/opencode/opencode.json`: `provider.lydiabox` (`npm: "@ai-sdk/openai-compatible"`, `options.baseURL`, `options.apiKey`, `models`), `model: "lydiabox/<provider>/<model>"`, `mcp.<name> = {"type":"remote","url","enabled":true}`. When the file already uses the newer `providers` or `mcp.servers` keys, those are written instead | Verified (old form, still accepted); new form Inferred |

## Common rules

- Discovery skips sidechain and subagent sessions, archived opencode
  sessions, and files without a human prompt.
