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
| Start / resume | `claude --dangerously-skip-permissions --session-id <uuid>`, `claude --dangerously-skip-permissions --resume <id> [prompt]`, `--model`. The flag turns off all permission prompts; Claude Code refuses it as root (the guest runs harnesses as UID 1000). The Bash sandbox of Claude Code is off unless `sandbox.enabled` is set; LydiaBox does not set it, so commands have network access | Verified (Claude Code 2.1.328) |
| Config | `.claude/settings.json` `env.ANTHROPIC_BASE_URL`, `env.ANTHROPIC_AUTH_TOKEN`, `model`, `skipDangerousModePermissionPrompt: true` (without it, `--dangerously-skip-permissions` stops at a "No, exit / Yes, I accept" warning); user-scope MCP servers in `.claude.json` `mcpServers.<name> = {"type":"http","url":…}` | Verified (Claude Code 2.1.328 in tmux) |

## Codex CLI (`codex`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Rollout | `.codex/sessions/YYYY/MM/DD/rollout-<local time>-<uuid>.jsonl`; new IDs are UUIDv7 | Verified |
| Current lines | `{timestamp, type, payload}`; `session_meta` (`id`, `cwd`), `turn_context` (`model`, `cwd`), `response_item` (`message` with `input_text`/`output_text`, `function_call`, `function_call_output`, `custom_tool_call` such as `apply_patch`, `custom_tool_call_output`, `reasoning`), `event_msg` (`token_count` with `info.total_token_usage`) | Verified |
| Old lines (2025) | First line `{id, timestamp, instructions, git}`, then `{"record_type":"state"}` and bare items | Verified |
| Titles | `.codex/session_index.jsonl` (`id`, `thread_name`) | Verified |
| Start / resume | `codex --dangerously-bypass-approvals-and-sandbox [--model m]`, `codex resume <id> --dangerously-bypass-approvals-and-sandbox [prompt]`. The flag sets sandbox `danger-full-access` and approval `never` ("YOLO mode"); MCP tool calls are approved too (`mcp_permission_prompt_is_auto_approved`). Shell commands run without the sandbox (network policy enabled, sandbox type none), so they reach `127.0.0.1` (CDP `9222`, gateway `3425`, MCP `8910`/`8911`) and the internet; the default sandbox blocks them | Verified (Codex 0.169.0 source, TUI, and `codex exec` against a fake Responses server: a shell `curl http://127.0.0.1:…` got no connection in the default mode and an answer with the flag) |
| Config | `model`, `model_provider`, `[model_providers.lydiabox]` (`name`, `base_url`, `env_key`, `wire_api = "responses"`), `[mcp_servers.<name>] url` and `default_tools_approval_mode = "approve"` | Verified (`wire_api` accepts only `responses` now; `codex mcp get` 0.169.0 reads the approval key) |
| bubblewrap | Codex's background app server (`codex app-server --managed-daemon`) checks for `bwrap` on PATH at each start, also in bypass mode, and shows a startup warning without it; the image installs `bubblewrap` | Verified (Codex 0.169.0) |

## dsh (DeepSeek Harness, `@deepseek-ai/dsh`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Home | `$DSH_HOME` or `.dsh` | Verified (`docs/config-catalog.md`) |
| Sessions | `.dsh/sessions/<projectKey>/<encodedId>/session[.vN].jsonl[.zstd]`, zstd by default; `projectKey` is `--` + cwd with `/`, `\`, `:` as `-` and other unsafe characters as `~XXXX` + `--` | Verified (`docs/persistence-catalog.md`, `session-persistence-jsonl/src/format.ts`) |
| Lines | Header `{type:"session", version, id, createdAt, cwd, isSeeded, delegationDepth, origin?}`; events `{type, seq, time, data, ignorable?}`; `user/message` (`source.kind` is `user` for human prompts), `assistant/message` (`message.content[]` with `text` and `tool-call` blocks, `message.source.{model,provider}`, `usage.{inputTokens,outputTokens}`), `tool/result` (`message.toolCallId`, `isError`) | Verified |
| Resume | `dsh tui --resume <id>`; headless `--session-id`. dsh 0.2.2-rc.5 ships no `tui` profile (`profile "tui" does not exist` on a fresh install); its shipped profiles are `web`, `headless`, `sdk`, `sdk-minimal`, and `acp` | Verified (`docs/cli-help.md`; dsh 0.2.2-rc.5, 2026-11-20) |
| Permissions | No CLI flag. The `@deepseek-ai/dsh-base` profile reads `DSH_PERMISSION_MODE`; the launcher sets `danger-full-access` (sandbox off, approval policy `never`). In that mode dsh runs commands as they are, without its sandbox, so they have network access | Verified (`dsh-base/cordis.patch.yml`, `docs/subsystems/sandbox.md`, dsh 0.2.2-rc.5) |
| `encodedId` for IDs with only safe characters | The ID itself | Inferred |
| `.dsh/lydiabox.env` and `DSH_MODEL` | The wirer writes `OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `DEEPSEEK_BASE_URL`, the `*_API_KEY` placeholders, and `DSH_MODEL` to `~/.dsh/lydiabox.env`; the launcher runs dsh through `sh -c 'set -a; . ~/.dsh/lydiabox.env; …'`, so they reach the process. A session model replaces `DSH_MODEL` after the file is read. With no model bound and none in the file, the wirer writes the first model of the gateway catalog (`GET /v1/models`). dsh 0.2.2-rc.1 reads `~/.dsh/.env` itself and exits when that file sets a base URL ("only the launching environment may set" it), so the wirer removes the managed keys from `~/.dsh/.env` | Verified (dsh 0.2.2-rc.1, 2026-11-19) |
| Model (`.dsh/lydiabox.patch.yml`) | dsh 0.2.2 reads no model variable (`DSH_MODEL` is only in its Python SDK example). A profile takes the model of a new agent from a profile row: `acp` in the `acp` profile, `agent-default-model` in the others; both ship `deepseek-official` with `deepseek-v4-flash` (sent to `/v1/messages`). The wirer writes the overlay `~/.dsh/lydiabox.patch.yml`: a pi-ai route `lydiabox` (`@deepseek-ai/dsh-llm-pi-ai`: `api: openai-completions`, `baseURL` = gateway `/v1`, `apiKeyEnv: OPENAI_API_KEY`, `defaultInput: [text, image]`, `compat.supportsStore: false`, `models: [{id: process.env.DSH_MODEL}]`) and `{provider: lydiabox, model: process.env.DSH_MODEL}` for both rows (`!!js` values). When `DSH_MODEL` is set, the launcher runs `dsh <profile> --patch ~/.dsh/lydiabox.patch.yml <app arguments>` (launcher flags come before the app arguments). A row that the profile does not have is ignored. dsh records the route in the transcript (`source.provider: "lydiabox"`); the session list shows the model without that prefix (`codex/gpt-6.1-sol`) | Verified (dsh 0.2.2-rc.5 against a fake gateway, 2026-11-20: `acp` and `headless` send the session model to `/v1/chat/completions`; without the overlay `acp` sends `deepseek-v4-flash` to `/v1/messages`) |

## Antigravity (`agy`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Conversations | `.gemini/antigravity/conversations/<uuid>.db`: SQLite, table `steps(idx, step_type, status, step_payload BLOB, metadata BLOB)`, payloads are protobuf without a public schema | Verified (real files) |
| Artifacts | `.gemini/antigravity/brain/<uuid>/` (`task.md` and others), `annotations/<uuid>.pbtxt` (text protobuf with `title`) | Verified (layout); `title` field Inferred |
| Step types | 14 user input, 15 planner response, 5 code action, 8 view file | Inferred |
| Payload decoding | Schema-free protobuf walk; the longest string of a user or planner step is its text; paths are strings that look like file paths | Inferred |
| CLI | `agy [--model m] [--conversation <id>] [-i/--prompt-interactive <text>]`; `-p/--print`, `--settings`, `agy mcp list`, `agy models`. The launcher adds `--dangerously-skip-permissions` (approves every tool permission request). agy's terminal sandbox is the opt-in `--sandbox` flag, which the launcher does not pass | Verified (`agy --help`, agy 1.5.4 and 1.5.12) |
| Config (design path) | `.antigravity/settings.json`: `endpoint`, `apiKey`, `model`, `env.GOOGLE_GEMINI_BASE_URL`, `env.GEMINI_API_KEY`. The CLI does not read this file; the wirer writes it because design section 4.4.1 names it, and `CurrentModel` reads `model` from it | Design path |
| Config (CLI) | `.gemini/antigravity-cli/settings.json` (schema `https://antigravity.google/schemas/v1/settings.json`): `"modelProvider": "gemini"`, `"colorScheme": "dark"` when the user has no color scheme (the first start of agy then skips its color step; LydiaBox never accepts the terms, which stay the user's decision), and `customModelsConfig.customModels.<model> = {"modelName": "gemini-api://<model>", "label": "<model>"}`. With `GEMINI_API_KEY` and `GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425` in the environment (wirer `Env` and `ptymanager.DefaultEnv`), `agy --model <model>` sends `POST /v1beta/models/<model>:streamGenerateContent?alt=sse` with `X-Goog-Api-Key` to the gateway. The CLI `model` key is not written: agy 1.5.4 resolves a saved custom-model label to an empty model in print mode (`tModel: model is empty`), and without `GEMINI_API_KEY` it refuses to start when `modelProvider` is `gemini` | Verified (schema, docs, and agy 1.5.4 against a capture server) |
| MCP | `.gemini/config/mcp_config.json` (documented global path; `agy mcp list` shows both servers) and `.gemini/antigravity/mcp_config.json` (design path), as `mcpServers.<name>.serverUrl` | Verified (docs, agy 1.5.4) |

## opencode (`sst/opencode`)

| Item | Value | Tag |
| :--- | :--- | :--- |
| Store (1.x) | `.local/share/opencode/opencode.db` (SQLite): `session` (`id`, `parent_id`, `directory`, `title`, `time_created`, `time_updated`, `time_archived`), `message` (`id`, `session_id`, `data` JSON with `role`, `modelID`, `providerID`, `cost`, `tokens`), `part` (`id`, `message_id`, `session_id`, `data` JSON: `text`, `tool` with `state.{status,input,output,error}`, `patch` with `files`) | Verified (real database of opencode 1.18) |
| Store (legacy) | `.local/share/opencode/storage/{session/<project>/<id>.json, message/<sessionID>/<id>.json, part/<messageID>/<id>.json}` | Verified (older releases) |
| IDs | `ses_`, `msg_`, `prt_` + time-ordered suffix | Verified |
| Start / resume | `opencode [--session id] [--model provider/model] [--prompt text]`; `opencode import <file>` takes `opencode export` JSON. The launcher adds `--auto` (approves permission requests that the config does not deny). `opencode --help` has no sandbox option | Verified (`opencode --help`, opencode 1.19.6) |
| Config | `.config/opencode/opencode.json`: `provider.lydiabox` (`npm: "@ai-sdk/openai-compatible"`, `options.baseURL`, `options.apiKey`, `models`), `model: "lydiabox/<provider>/<model>"`, `mcp.<name> = {"type":"remote","url","enabled":true}`. When the file already uses the newer `providers` or `mcp.servers` keys, those are written instead | Verified (old form, still accepted); new form Inferred |
| Images | opencode gives a model of a custom provider image input only from `modalities.input` (`capabilities.input.image`); without it, an image in a tool result (`lydiabox_computer_screenshot`) becomes the text "Cannot read image". The gateway catalog has no modalities, so the wirer gives every `provider.lydiabox.models` entry without `modalities` the value `{"input":["text","image"],"output":["text"]}` and `attachment: true`, as Claude Code and Codex send images for every model; an entry with `modalities` is kept | Verified (opencode 1.18.17 against a fake gateway and MCP server, 2026-11-20: the tool-result image reaches `/v1/chat/completions` as `image_url` only with the modalities) |
| Session model | The session list shows the model of the last assistant message. For the provider `lydiabox` it is the gateway model ID (`codex/gpt-6.1-sol`), as for the other harnesses; another provider shows as `<providerID>/<modelID>`. The host also removes a `lydiabox/` prefix from discovered and stored models (a guest binary or a discovery cache of an older version) | Verified (unit tests) |

## ACP agents

The ACP engine (`docs/api.md` section 8, "ACP engine") runs the bridge
`lydiabox-guest acp` (`internal/acp`) as a guest terminal in raw mode. The
bridge is an ACP client (protocol version 1; a small JSON-RPC client that
decodes the agent messages leniently, because the five agents follow
different schema versions). It starts the agent below with the same
environment as the terminal launch (gateway keys from the wirers), passes
the MCP servers `lydiabox` and `lydiabox-connectors` in `mcpServers`, and
selects the full-access mode. Checked on 2026-11-19 in the guest image
(`lydiabox/guest:instabox-base`, amd64, as UID 1000, configs written by
the wirers, a stub MCP server, and a fake model gateway on port 3425 that
streams one reply for every model API; Claude Code and Codex again in the
InstaBox image after the prune, with the installed CLIs):

| Harness | Agent | Version | Session | Full access | Result |
| :--- | :--- | :--- | :--- | :--- | :--- |
| Claude Code (2.1.328) | `claude-agent-acp` (npm `@agentclientprotocol/claude-agent-acp`, Zed/ACP adapter), with the installed `claude` (`CLAUDE_CODE_EXECUTABLE`; the MCP servers then go to `claude` as `--mcp-config` argv) | 0.108.1 (pinned in `install-harnesses.sh`) | `session/new`, `session/load` | mode `bypassPermissions` (offered to a user other than root) | Verified: full turn through the gateway (`/v1/messages`, with the tools of both MCP servers), usage with context window and cost, resume with replay |
| Codex (0.170.0) | `codex-acp` (npm `@agentclientprotocol/codex-acp`), with the installed `codex` (`CODEX_PATH`; it drives `codex app-server`, so the rollouts are the TUI's) | 2.7.0 (pinned) | `session/new`, `session/load` | mode `agent-full-access` | Verified: full turn (`/v1/responses`), usage with context window, resume with replay; no login needed with the `lydiabox` model provider; it reports MCP servers that the config already defines as `mcp_startup.*` tool calls, which the bridge drops. Codex adds a "Model metadata … not found" warning to the reply for a gateway model, as in its TUI |
| opencode (1.19.5) | `opencode acp` | the installed CLI | `session/new`, `session/load` | no mode; `OPENCODE_CONFIG_CONTENT` allows every tool, as `--auto` does | Verified: full turn (`/v1/chat/completions`), usage, resume with replay |
| dsh (0.2.2-rc.1; model 0.2.2-rc.5) | `dsh acp` (profile bundle `@deepseek-ai/dsh-acp-app`, through the launcher wrapper with the overlay `~/.dsh/lydiabox.patch.yml`, see dsh above) | the installed CLI | `session/new`, `session/resume` (no `session/load`: the bridge sends the transcript history first) | `DSH_PERMISSION_MODE=danger-full-access` | Verified: full turn, context size and window, resume with the transcript history; dsh is not in the ACP registry. 0.2.2-rc.5: the session model reaches `/v1/chat/completions` (fake gateway); model options are JSON `["<provider>","<model>"]` values in provider groups, which the bridge matches |
| Antigravity (1.5.9) | `agy acp --dangerously-skip-permissions` | the installed CLI | `session/new`, `session/load` | the flag approves every tool (its modes are `default`, `accept-edits`, `plan`) | Not used: the session, the context window (1,048,576), and resume work, but the ACP server offers only Google's built-in models as the model option, has no `--model` flag, and ignores the CLI settings `model` key, so a prompt goes to `gemini-3.1-pro-preview` instead of the LydiaBox model. Antigravity keeps the terminal engine and the transcript chat view; the API refuses `engine: "acp"` for it |

Image size: both npm packages bundle a full copy of their CLI (355 MB of
Claude, 436 MB of Codex). The bridge sets `CLAUDE_CODE_EXECUTABLE` and
`CODEX_PATH` to the installed programs, and `install-harnesses.sh`
(`prune_node_modules`) removes the bundled copies, opencode's platform
package (its postinstall copied the binary to `opencode-ai/bin`), native
prebuilds for other platforms, source maps, and type declarations (not in
`claude-agent-acp`, whose SDK names `.d.ts` files at run time). The
InstaBox import lays the pulled image out on its 16 GiB root disk next to
the image itself, so every MB of image costs about 2 MB there
([instabox.md](instabox.md)).

The ACP registry (`https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json`)
lists the Claude, Codex, OpenCode, and Antigravity agents (the registry's
Antigravity entry is a separate `agy_acp_server` binary; the installed
`agy` has the same server as `agy acp`). Not checked: tool calls and
permission requests of a real agent (the fake model replies with text
only), MCP tool calls through ACP, and a real model through magpie.

The bridge maps `tool_call` and `tool_call_update` to one tool event
(`internal/acp/events.go`). Tool call formats that the live check on
2026-11-19 (Codex, Claude Code, opencode with a real model) showed, and the
codex-acp 2.7.0 source:

- Output: `content` text, then `rawOutput` text. codex-acp sends the output
  of a command only in `_meta.terminal_output_delta.data` (Zed:
  `_meta.terminal_output`), as chunks while the command runs or once with its
  end, because the bridge declares no terminal capability. The bridge appends
  the chunks, up to the output limit. The content item `terminal` and the
  `terminal_exit` metadata have no output.
- Codex MCP call: title `mcp.<server>.<tool>`, `rawInput`
  `{"server","tool","arguments"}`, `rawOutput` `{"result","error"}`. The
  bridge sets the name to `mcp__<server>__<tool>` (the form of Claude Code),
  the summary to the tool and the summary of the arguments (`browser_open
  https://www.iana.org`), and the input to the arguments.
- Kind: a computer, browser, plan, agent, or MCP tool name sets the kind. A
  name with spaces is a title (codex-acp uses the command line), so a known
  ACP kind sets the kind for it. A shell call of `lbx browser` or `lbx
  computer` gets the kind `browser` or `computer` (`harness.ShellKind`). An
  update leaves out the fields that did not change: claude-agent-acp sends a
  Bash call first with the name `Bash`, the title `Terminal`, the kind
  `execute`, and an empty input, then the command without the name and the
  kind. The bridge keeps the ACP kind and the `name` of each call, and sets
  the kind again on each update. A title does not replace a `name`.
- Images: an image content block and the images of `rawOutput` (Claude Code
  and opencode send a screenshot in both). Each data URL is added once.
- Duration: from the first report of the call to its end. A call that the
  agent replays (`session/load`), or whose first report is its end, has no
  duration.

## Session events

`internal/harness/adapters` (`EventReader`) turns each native transcript
into the typed events of `GET /api/v1/sessions/{id}/events` (`docs/api.md`
section 8). It keeps the parse state of the last 16 sessions: an appended
JSONL file is parsed from the last complete line; a file that changed
otherwise (shorter, or another first 256 bytes) is parsed again; the
SQLite stores are read again when the database or its WAL file changed. The
same code runs on the host (`juicefs` mode) and in the guest daemon
(`harness.events`, `provider` mode).

| Harness | User, assistant | Reasoning | Tool call, result, duration | Plan | Usage, context | Turn end, errors |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| Claude Code | `text` blocks (noise such as `<command-name>` left out) | `thinking` blocks | `tool_use` and `tool_result` (`is_error`, image blocks); duration from the timestamps | `TodoWrite` input | `message.usage` once per `message.id`; context = input + cache creation + cache read; window not recorded | `stop_reason` `end_turn`; `[Request interrupted` is an interrupted end; `isApiErrorMessage` is an error |
| Codex | `response_item` messages (context such as `<environment_context>` left out) | `reasoning` summary | `function_call`, `custom_tool_call` (`apply_patch` gives the diff), `local_shell_call`, `web_search_call` and their outputs; `metadata.exit_code` or `Exit code:` gives the error, `metadata.duration_seconds` the duration | `update_plan` input | `event_msg` `token_count` `last_token_usage`; context = `input_tokens`; window = `model_context_window` | `task_complete`, `turn_aborted`, `error` |
| dsh | `user/message` (`source.kind` `user`), `assistant/message` `text` parts | `reasoning` and `thinking` parts (Inferred) | `tool-call` parts and `tool/result` (`isError`); duration from the times | to-do tool input (Inferred) | `usage` of each assistant message; window not recorded | an assistant message without tool calls ends the turn (Inferred); `*error*` events |
| opencode | `text` parts (synthetic parts left out) | `reasoning` parts | `tool` parts: one event with the call and its result (`state.status`, `output`, `error`, `attachments`, `time`) | `todowrite` input | `step-finish` `tokens` and `cost`; window not recorded | assistant `time.completed` with `finish` other than `tool-calls`; message `error` |
| Antigravity | user input and planner steps (longest string) | not available | code action and view file steps give an edit or a read of the file they name; no output, no duration | `brain/<id>/task.md` checklist (`[ ]`, `[/]`, `[x]`) | not available | not available |

Gaps: the Antigravity step payloads have no public schema, so its events
have no tool output, reasoning, or usage. Claude Code, dsh, and opencode do
not record the context window; the web UI then takes it from the model list
(`contextWindow` of `GET /api/v1/models`). Tool kinds come from the tool
names (`harness.ToolKind`); a name that contains `browser`, `playwright`,
or `puppeteer` is browser use (the guest `browser_*` tools), one that
contains `computer` or `screenshot` is computer use, and an `mcp__` name or
a name with `__` is an MCP tool. A shell call of `lbx browser …` or
`lbx computer …` is browser or computer use (`harness.ShellKind`).

The agent state of the event stream comes from the terminal, the same for
all five harnesses: output in the last 2 seconds is `working` (the TUIs
animate while they work), and a quiet screen whose last 2000 characters
match an approval prompt ("Do you want to proceed?", "Would you like to run
…?", "Allow once", "Permission required", "(y/n)", …) is `attention`.
Before the first event, a first-run screen is `setup` (the Antigravity
onboarding: "Welcome to Antigravity CLI!", "Choose your color scheme:",
"Terms of Service & Data Use", and any step with the footer "↑/↓ Navigate ·
enter Confirm"; "Do you trust the files in this folder?", "Do you
trust the contents of this directory?", "Do you trust this folder?", "Yes,
I trust this folder", "How would you like to authenticate", "Select login
method", "Sign in with ChatGPT", "Choose the text style"), and the input
endpoint refuses prompts with `409`. Only the output after the last input
counts for `setup`: when the user answers the screen in the terminal, the
program draws other output, and the old text in the tail does not count. The
room also tracks bracketed paste mode (`ESC [?2004h` / `l`) for the input
endpoint.

## Common rules

- Discovery skips sidechain and subagent sessions, archived opencode
  sessions, and files without a human prompt.
