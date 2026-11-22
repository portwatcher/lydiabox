# Subscription sign-in: findings and real-account checklist

LydiaBox lets a user sign in to a Claude subscription (for Claude Code), a
ChatGPT subscription (for Codex), or a Google account with Antigravity (for
Antigravity) on the Models page. The sign-in runs in
magpie on the host. This file records what the pinned magpie supports, where
the tokens live, the provider terms that limit this use, and the checklist
for a check with real accounts. The routes are in
[api.md section 10.1](api.md#101-subscriptions).

The automated tests use a fake magpie (`internal/magpie/magpietest`) and
response bodies recorded from magpie v0.1.3531 (offline, with fake
accounts). Nobody has signed in to a real Claude, ChatGPT, or Antigravity
account through LydiaBox yet. The Antigravity flow is from the magpie
v0.1.3531 source (`internal/provider/signin.go`, `google.go`); its fake
responses are not recorded from magpie.

## What magpie v0.1.3531 supports

magpie (`internal/bootstrap/manifest.json`, `magpie-cli` v0.1.3531) signs in
to Claude, ChatGPT, Google (Gemini CLI, Antigravity), Copilot, and other
subscriptions, and serves each one as a provider (`claude`, `codex`,
`antigravity`, ...)
with the models `<provider>/<model>`. LydiaBox uses these routes of its web
API (`magpie serve --web`, loopback only, random key):

| magpie route | Use |
| :--- | :--- |
| `POST /api/signin` `{agent, site}` | Starts a sign-in: `{id, agent, state, url, code, pasteCode, pasteCallback, installing}` |
| `GET /api/signin/{id}` | State: `starting`, `installing`, `waiting`, `done` (`user`), `failed` (`error`), `canceled` |
| `POST /api/signin/{id}/callback` `{url}` | The code or the address that the vendor page shows at the end |
| `POST /api/signin/{id}/cancel` | Gives up the sign-in |
| `POST /api/signin/claude-token` `{token}` | Adds a Claude account from `claude setup-token` |
| `POST /api/login/forget` `{agent, user}` | Removes an account and its stored sign-in |
| `GET /api/providers` | A subscription provider has `account: {agent, logins: [{user, plan, active, on, paused, lapsed}]}` |

Flows:

- **ChatGPT (Codex)**: device code. magpie asks
  `auth.openai.com/api/accounts/deviceauth/usercode` for a code, the user
  enters it on the ChatGPT page, and magpie polls until it gets the tokens
  (up to 15 minutes). magpie also has a browser flow with a callback on
  `localhost:1455`; LydiaBox does not use it, because the browser of the
  user is not on the host.
- **Claude (Claude Code)**: magpie serves a Claude subscription through
  Anthropic's own Claude Code. The first sign-in installs Claude Code on
  the host from `downloads.claude.ai` (state `installing`), then runs its
  sign-in. When the page shows a code to copy, magpie sets `pasteCode` and
  LydiaBox asks for it. The other way is a token from `claude setup-token`
  (made on any computer with a browser), pasted into the dialog.

- **Antigravity**: Google sign-in under Antigravity's own OAuth client
  (agent `antigravity`). magpie listens for the callback on a `localhost`
  port of the host and sets `pasteCallback`. The browser of the user is not
  on the host, so the last page (`http://localhost:<port>/oauth-callback?...`)
  does not load; the user pastes its address into the dialog and LydiaBox
  sends it to `POST /api/signin/{id}/callback`. LydiaBox does not offer the
  Gemini CLI sign-in (agent `gemini`).

Storage and refresh (all on the host, below `$LYDIABOX_DATA_DIR/state/magpie`,
which is outside the JuiceFS mount that holds `/home/agent`):

- `logins.json` (mode `0600`) holds every account with its tokens.
- The private magpie `home` holds the vendor CLI files:
  `home/.claude/.credentials.json` (mode `0600`) and `home/.codex/auth.json`.
- magpie refreshes the sign-ins (ChatGPT near expiry, all of them every 8
  days, `magpie accounts refresh` on demand). Refresh tokens are single
  use, so a sign-in that magpie refreshes stops working in any other tool
  that had a copy.
- These files are plain JSON, protected by file mode only, like the API keys
  that magpie already keeps. They are not AES-256-GCM encrypted by
  LydiaBox.

`TestSubscriptionStoreStaysOnHost` checks that magpie runs with `HOME` and
`XDG_*` below the host data directory and outside the guest mount.
`TestSubscriptionRoutes` checks that no token reaches an API response or a
file below the guest home.

## Provider terms

Read again on 2026-11-23. "Unclear" means that the terms do not say it.

| Use | Anthropic (Claude Code) | OpenAI (Codex) | Google (Antigravity) |
| :--- | :--- | :--- | :--- |
| The user signs in to the unmodified CLI in the user's own hosted VM | Permitted, if the operator accepts the Commercial Terms | Permitted (device code for remote machines) | Unclear |
| The service holds or shares the sign-in | Forbidden | Forbidden | Forbidden |
| The service routes subscription traffic | Forbidden | Unclear, probably forbidden | Forbidden |
| The user's API key in the VM | Permitted | Permitted | Permitted (AI Studio or Gemini Enterprise key) |
| The user's API key through a gateway of the service | Unclear | Unclear; transfer of a key to a third party is forbidden | Unclear |

- **Anthropic**: <https://code.claude.com/docs/en/legal-and-compliance>
  permits Claude Code in hosted agent infrastructure when the binary is
  unmodified and each end user signs in with an own subscription or key.
  Third parties may not "collect, store, or intermediate Claude.ai
  credentials" or route requests through Free, Pro, Max, or Ultra plan
  credentials. The Consumer Terms
  (<https://www.anthropic.com/legal/consumer-terms>) forbid sharing the
  login. The Commercial Terms
  (<https://www.anthropic.com/legal/commercial-terms>) apply to the
  operator. opencode removed its Claude subscription sign-in after a
  request from Anthropic (<https://github.com/anomalyco/opencode/pull/18186>):
  use Claude in opencode with an API key only.
- **OpenAI**: the Terms of Use (<https://openai.com/policies/row-terms-of-use/>)
  forbid sharing account credentials or making an account available to
  others. The Services Agreement (<https://openai.com/policies/services-agreement/>,
  3.3(g)) forbids transferring API keys to a third party. The Codex
  authentication page (<https://learn.chatgpt.com/docs/auth>) recommends
  the device code sign-in on a remote or headless machine and says to keep
  `~/.codex/auth.json` like a password.
- **Google**: the Antigravity terms (<https://antigravity.google/terms>,
  section 6) call access to the service through third-party software a
  breach. The FAQ (<https://antigravity.google/docs/faq>) does not support
  wrapping `agy` in third-party applications and recommends an AI Studio or
  Gemini Enterprise key for them. Whether LydiaBox, which starts `agy` in
  the user's VM, is such an application is not clear.

Decision for LydiaBox Cloud: guest-native mode
([design.md section 4.4.2](design.md#442-guest-native-mode-hosted-cells)).
The harness signs in inside the user's VM, the tokens stay on the VM disk,
and the cell neither reads them nor routes model traffic. The self-hosted
version keeps the host sign-in below: one user, an own account, an own host.

## Real-account checklist

Use a test account that you can lose. Run on a host with network access.

1. `lydiabox up`, open the Models page. The Subscriptions panel shows
   Claude, ChatGPT, and Antigravity with **Sign in**.
2. **ChatGPT**: select **Sign in** on the ChatGPT card, then **Sign in**.
   The dialog shows a code and **Open sign-in page**. Open the page in any
   browser, sign in, and enter the code.
   - Expect: the dialog closes within a few seconds, the card lists the
     account with its plan, and the Available models list has `codex/...`
     models.
3. **Claude, browser**: select **Sign in** on the Claude card, keep
   **Browser sign-in**, select **Sign in**.
   - Expect: "Installing Claude Code on the host…" (first time), then
     **Open sign-in page**. If the page shows a code, paste it and select
     **Finish sign-in**. If the dialog shows no code field, note what the
     page showed and what the dialog did: this path is not verified.
   - Expect: the card lists the account and `claude/...` models.
4. **Claude, setup token**: on another computer run `claude setup-token`,
   copy the token, select **Add account** on the Claude card, choose
   **Setup token**, and paste it. Expect a new account named
   "Claude setup-token …".
   **Antigravity**: select **Sign in** on the Antigravity card, then
   **Sign in**, then **Open sign-in page**. Sign in to Google. The browser
   then opens a `localhost` page that does not load. Copy its address,
   paste it into **Address**, and select **Finish sign-in**.
   - Expect: the card lists the Google account and `antigravity/...`
     models. Start an Antigravity session with one of these models and send
     one prompt. This path is not verified.
5. **Harness**: in the Studio, start a Claude Code session with a
   `claude/...` model and a Codex session with a `codex/...` model, and send
   one prompt in each. Expect an answer.
6. **No token in the guest**: in the VM, run
   `grep -rl -e sk-ant-o -e refresh_token /home/agent 2>/dev/null` and
   check `~/.claude/settings.json` and `~/.codex/config.toml`. Expect no
   match and the placeholder token `lydiabox-local` only.
7. **On the host**: expect the accounts in
   `$LYDIABOX_DATA_DIR/state/magpie/logins.json` (mode `0600`).
8. **Off and remove**: clear **Route requests to this subscription**,
   expect its models to leave the list; set it again. Remove the ChatGPT
   account: expect it to go from the card and from `logins.json`. Remove the
   setup-token Claude account: expect the same. Remove the last browser
   Claude account: expect the refusal "Claude Code is signed in to …".
9. **Restart**: restart `lydiabox up` and expect the accounts to stay.

Record the date, the magpie version, and the result of each step in
[conformance.md](conformance.md).

## Open questions for the owner

1. Terms: the cloud version uses each user's own harness sign-in in the
   user's own VM (guest-native mode, decided 2026-11-23). Open: should the
   cloud offer the Antigravity Google sign-in, or only a Gemini API key in
   the VM?
2. The last browser-signed-in Claude account cannot be removed through
   magpie's API (magpie does not sign its Claude Code copy out). Accept
   "turn the subscription off", or should LydiaBox delete the magpie
   Claude Code credentials file itself (this depends on magpie internals)?
3. Should LydiaBox encrypt the magpie data directory at rest (for example,
   on an encrypted volume), now that it holds refresh tokens?
4. magpie on a macOS host stores Claude Code credentials in the login
   Keychain (the `security` tool asked for permission in a local test).
   Is a macOS host a supported target for subscriptions?
