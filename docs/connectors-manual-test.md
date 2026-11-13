# Connector test with real OAuth providers

This checklist tests the four OAuth connectors (GitHub, Google Drive,
Notion, Slack) against the real providers. It needs an OAuth application
at each provider and an account to sign in with. The automated tests use a
fake provider (`internal/connector/connectortest`); they cover the same
code paths, but not the provider consoles, consent screens, or real API
answers. See [conformance.md](conformance.md#46-zero-trust-connectors).

Do the steps in the order given. Record pass or fail for each line.

## 1. Host

1. Run `lydiabox up` with any VM driver (`docker` is sufficient). Do not
   set a new `LYDIABOX_MASTER_KEY`. On the first start `lydiabox up` writes
   a key to `$LYDIABOX_DATA_DIR/state/master.key` and uses it after each
   restart, so the stored tokens stay readable. A different key in the
   environment overrides the file, and on a data directory with stored
   secrets `lydiabox up` then stops with `decryption failed`.

2. Select the origin that the browser uses. The redirect URI of each
   provider is `<origin>/api/v1/connectors/<provider>/callback`. The
   origin must be the same in the provider console and in the browser
   address bar.
   - GitHub and Google accept `http://localhost:8080`.
   - Slack accepts only `https://` redirect URLs. Notion can also refuse
     `http://`. For these, put an HTTPS reverse proxy (for example Caddy
     with a real certificate) in front of `lydiabox` and use its origin.
3. Make a note of the origin. This document calls it `$ORIGIN`.

## 2. Provider applications

| Provider | Console | Application settings | Variables |
| :--- | :--- | :--- | :--- |
| GitHub | Settings, Developer settings, OAuth Apps, New OAuth App | Callback URL `$ORIGIN/api/v1/connectors/github/callback` | `LYDIABOX_GITHUB_CLIENT_ID`, `LYDIABOX_GITHUB_CLIENT_SECRET` |
| Google Drive | Google Cloud console: enable the Google Drive API; OAuth consent screen (External, add your account as a test user, scopes `drive.file` and `drive.readonly`); Credentials, OAuth client ID, type Web application | Authorized redirect URI `$ORIGIN/api/v1/connectors/gdrive/callback` | `LYDIABOX_GDRIVE_CLIENT_ID`, `LYDIABOX_GDRIVE_CLIENT_SECRET` |
| Notion | notion.so/profile/integrations, New integration, type Public | Redirect URI `$ORIGIN/api/v1/connectors/notion/callback`; capabilities: read content, insert content | `LYDIABOX_NOTION_CLIENT_ID`, `LYDIABOX_NOTION_CLIENT_SECRET` |
| Slack | api.slack.com/apps, Create New App, From scratch | OAuth & Permissions: redirect URL `$ORIGIN/api/v1/connectors/slack/callback`; User Token Scopes `channels:read`, `groups:read`, `chat:write`, `search:read`, `users:read` | `LYDIABOX_SLACK_CLIENT_ID`, `LYDIABOX_SLACK_CLIENT_SECRET` |

Set the variables and restart `lydiabox up`. As an alternative, enter the
client ID and secret on the Connectors page (`PUT
/api/v1/connectors/{provider}/client`); environment values have priority.

Check: `GET /api/v1/connectors` shows `"oauthConfigured": true` and
`"oauthSource": "env"` (or `"stored"`) for each configured provider.

## 3. Authorize and callback

Do these steps for each provider, in the web UI at `$ORIGIN/connectors`:

1. Sign in to the web UI with the admin token.
2. Click Connect on the provider card. The browser goes to the provider
   consent page. The URL has `code_challenge_method=S256`, `state`, and
   the redirect URI from step 2.
3. Accept the consent.
   - Google: select the test user account. Google shows that the app is
     not verified; continue.
   - Notion: select the pages that the integration can read.
   - Slack: select the workspace.
4. The browser comes back to `/connectors?connected=<provider>`. The
   provider card shows one account with status `ok`. The account label is
   the provider user name (GitHub login, Google e-mail, Notion workspace
   or user, Slack user).
5. `GET /api/v1/connectors/audit?provider=<provider>` shows a
   `connector.connected` entry with `authKind` `oauth2`.
6. Check that no token is in the API answers: `GET
   /api/v1/connectors/<provider>` has no `token`, `accessToken`, or
   `refreshToken` field.

Negative checks (one provider is sufficient):

- Click Connect, then click Cancel (or Deny) on the consent page. The
  browser comes back to `/connectors?error=access_denied&provider=...`.
- Open the callback URL a second time (browser Back, then reload). The
  answer is `?error=invalid_state`.

## 4. Refresh (Google Drive)

Google access tokens expire after one hour.

1. Wait more than one hour after the authorization.
2. Call `gdrive_search` (step 6). It succeeds. The audit log shows a
   `connector.token.refreshed` entry. The account `expiresAt` is about one
   hour later.

## 5. git credentials in the guest (GitHub)

Open a shell session (Studio page, or `POST /api/v1/sessions`
`{"harness":"shell"}`) and run:

```sh
printf 'protocol=https\nhost=github.com\n\n' | lydiabox-guest git-cred get
git ls-remote https://github.com/<owner>/<private-repo>.git
grep -r gho_ ~ /workspace 2>/dev/null | head -1     # no output
```

Expected: `git-cred get` prints `username=` and `password=` lines; the
`ls-remote` of a private repository lists refs without a prompt; no token
is in a file in the guest. The audit log shows `connector.token.issued`.

## 6. Connector MCP in the guest

In the same shell:

```sh
mcp() { curl -s -X POST http://127.0.0.1:8911/mcp -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"; echo; }
curl -s -X POST http://127.0.0.1:8911/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq -r '.result.tools[].name'
```

| Provider | Call | Expected |
| :--- | :--- | :--- |
| GitHub | `mcp github_list_repos '{}'` | your repositories |
| GitHub | `mcp github_list_issues '{"owner":"<owner>","repo":"<repo>"}'` | the open issues |
| GitHub | `mcp github_get_file_contents '{"owner":"<owner>","repo":"<repo>","path":"README.md"}'` | the file content |
| GitHub | `mcp github_create_issue '{"owner":"<owner>","repo":"<test-repo>","title":"lydiabox test"}'` | an issue URL; close the issue after the test |
| Google Drive | `mcp gdrive_search '{"query":"<file name>"}'` | the file with its ID |
| Google Drive | `mcp gdrive_download_to_workspace '{"file_id":"<id>","path":"drive/test"}'` | the file is in `/workspace/drive/` |
| Google Drive | `mcp gdrive_upload_from_workspace '{"path":"drive/test"}'` | a new Drive file ID |
| Google Drive | `mcp gdrive_download_to_workspace '{"file_id":"<id>","path":"../../etc/x"}'` | an error; no file outside `/workspace` |
| Notion | `mcp notion_search '{"query":"<page title>"}'` | the page with its ID |
| Notion | `mcp notion_read_page '{"page_id":"<id>"}'` | the page text |
| Notion | `mcp notion_append_to_page '{"page_id":"<id>","text":"lydiabox test"}'` | the text is on the page |
| Slack | `mcp slack_list_channels '{}'` | the channels |
| Slack | `mcp slack_post_message '{"channel":"<test channel id>","text":"lydiabox test"}'` | the message is in the channel |
| Slack | `mcp slack_search_messages '{"query":"lydiabox test"}'` | the message from the previous line |

The same calls work on the host at `POST $ORIGIN/mcp/connectors` with the
bearer token.

## 7. Disconnect

For each provider, click Disconnect on the Connectors page (`DELETE
/api/v1/connectors/<provider>?account=<label>`). Expected: `204`, the card
has no account, and the provider shows that the access is revoked (GitHub:
Settings, Applications, Authorized OAuth Apps; Google: myaccount.google.com,
Security, third-party access; Notion: Settings, Connections; Slack: the
app is not in the workspace apps list for the user). After this,
`git-cred get` prints nothing and the MCP calls answer with a "no account"
error.
