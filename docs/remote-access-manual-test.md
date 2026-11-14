# Remote access test with a real Cloudflare account

This checklist tests remote access through Cloudflare Zero Trust against a
real Cloudflare account: `lydiabox cloudflare setup`, the tunnel that
`lydiabox up` runs, the Access login in the browser, non-browser clients,
and `lydiabox cloudflare teardown`. The automated tests use a fake
Cloudflare API (`internal/cloudflare/cloudflaretest`), a fake JWKS
(`internal/cloudflare/accessjwt/accessjwttest`), and a fake `cloudflared`;
they cover the same code paths, but not the real API answers, the
dashboard, the Access login pages, or the Cloudflare edge. See
[conformance.md](conformance.md#41-configuration-and-single-binary-startup).

Do the steps in the order given. Record pass or fail for each line.

## 1. Before you start

1. A Cloudflare account with a domain (a zone) on it, for example
   `example.com`. This document uses the hostname `agent.example.com`,
   called `$HOST`. The hostname must not have a DNS record yet.
2. Zero Trust is enabled: in the Cloudflare dashboard, open Zero Trust and
   select a team name and a plan (the Free plan is sufficient). The API
   cannot do this step. The team domain is `<team>.cloudflareaccess.com`.
3. An API token (My Profile, API Tokens, Create Token, Custom token) with
   these permissions. The names are the ones in the dashboard on
   2026-11-14; Cloudflare renames permission groups from time to time.

   | Scope | Permission | Used for |
   | :--- | :--- | :--- |
   | Account | Cloudflare Tunnel: Edit | Create the tunnel, its ingress, and its token |
   | Account | Access: Apps and Policies: Edit | Access applications and reusable policies |
   | Account | Access: Organizations, Identity Providers, and Groups: Edit | Read the team domain, add the one-time PIN login |
   | Account | Access: Service Tokens: Edit | The service token for non-browser clients |
   | Account | Cloudflare Realtime: Edit (the API reference calls it `Calls Write`) | Create and delete the TURN key of the desktop stream; not needed with `-turn=false` |
   | Zone (`example.com`) | DNS: Edit | The CNAME record of `$HOST` |
   | Zone (`example.com`) | Zone: Read | Find the zone of `$HOST` |

4. The account ID (dashboard, the domain's Overview page, right column).
5. A host with LydiaBox and a working `lydiabox up` (any VM driver;
   `docker` is sufficient). Keep the admin token at hand.

```sh
export LYDIABOX_CLOUDFLARE_API_TOKEN=<token> LYDIABOX_CLOUDFLARE_ACCOUNT_ID=<account id>
export HOST=agent.example.com ME=<your email>
```

## 2. Setup

1. Run `lydiabox cloudflare setup -hostname $HOST -email $ME`.
   - The output lists `created tunnel`, `routed`, `created DNS record`,
     `enabled the one-time PIN login method` (only when the account had
     none), `created Access policy`, `created service token`, `created
     Access application`, and `created TURN key`. The last line says that
     the desktop stream goes through Cloudflare TURN.
   - It prints `CF-Access-Client-Id` and `CF-Access-Client-Secret`. Save
     them as `$CF_ID` and `$CF_SECRET`.
   - It installs `cloudflared` into `$LYDIABOX_DATA_DIR/runtime/bin`.
2. Check the dashboard:
   - Zero Trust, Networks, Tunnels: a tunnel `lydiabox-agent-example-com`,
     status Inactive. Its public hostname is `$HOST` to
     `http://127.0.0.1:8080`.
   - DNS, Records: `$HOST` CNAME `<tunnel id>.cfargotunnel.com`, Proxied.
   - Zero Trust, Access, Applications: `LydiaBox ($HOST)`, self-hosted,
     domain `$HOST`, two policies: `LydiaBox owner ($HOST)` (Allow, your
     email) and `LydiaBox service token ($HOST)` (Service Auth).
   - Zero Trust, Settings, Authentication: One-time PIN is in the list.
   - Zero Trust, Access, Service Auth: `LydiaBox ($HOST)`.
   - Realtime, TURN Server: a key `LydiaBox ($HOST)`.
3. `lydiabox cloudflare status` shows the hostname, the team domain, the
   audience tag (the same value as the AUD tag on the application's
   Overview page), the TURN key ID (the same as in the dashboard), and no
   secret.
4. `sqlite3 $LYDIABOX_DATA_DIR/state/lydiabox.db "select secrets_enc from remote_access"`
   shows binary data, not the tunnel token.

## 3. Tunnel

1. Start `lydiabox up` without `LYDIABOX_LISTEN_ADDR`, without
   `LYDIABOX_WEBRTC_UDP_PORT`, and without any `LYDIABOX_STUN_*` or
   `LYDIABOX_*TURN*` variable in the environment (comment them out in
   `.env`).
   - The log shows `the desktop stream uses the stored Cloudflare TURN key`
     with `relayOnly=true`.
   - The log shows `Cloudflare Access login is on`, `the tunnel is on;
     listening on loopback only`, `cloudflared started`, and then
     `Cloudflare Tunnel connected`.
   - The listen line is `http://127.0.0.1:8080`.
2. From another machine on the LAN, `curl http://<host-ip>:8080/` fails
   (connection refused).
3. The dashboard shows the tunnel as Healthy.
4. `curl -H "Authorization: Bearer $LYDIABOX_AUTH_TOKEN" http://127.0.0.1:8080/api/v1/remote-access`
   on the host shows `"configured": true`, `"turnInUse": true`, and
   `"tunnel": {"state": "connected", ...}`.
5. Kill the `cloudflared` child (`pkill -f "cloudflared tunnel"`). Within
   a few seconds, the log shows `cloudflared stopped; restarting`, and the
   state goes back to `connected` with `restarts` 1.

## 4. Browser login

1. In a private window, open `https://$HOST`. Cloudflare shows its Access
   login page for the team.
2. Enter `$ME`. An email with a code arrives. Enter the code.
3. LydiaBox opens without the admin token prompt. The status bar shows
   `$ME` next to the sign-out button.
4. `https://$HOST/api/v1/auth/session` in the same window shows
   `"method": "access"` and `"email": "<your email>"`.
5. Open the Studio and start a session: the terminal works (WebSocket
   through the tunnel). Open the Desktop page from another network (for
   example a phone hotspot): video starts within about 10 seconds, with no
   extra setting. `chrome://webrtc-internals` shows `iceTransportPolicy:
   relay` and a `relay` candidate pair on a Cloudflare TURN address. Take
   over and type in the desktop: the input works.
6. Restart `lydiabox up` with `LYDIABOX_STUN_URLS=stun:stun.l.google.com:19302`.
   The log says that the stored TURN key is not used, `/api/v1/remote-access`
   shows `"turnInUse": false`, and the video through the tunnel does not
   start (no relay), unless the browser can reach UDP port 50000 of the
   host. Restart without the variable.
7. Open the Files page, upload a file, and download it again.
8. Select Sign out. The browser goes to the Access logout page. Open
   `https://$HOST` again: Cloudflare asks for a new code.
9. In a new private window, enter another email that is not on the list.
   Cloudflare does not let it in (no code arrives, or Access shows that
   you do not have access).

## 5. Allow list in LydiaBox

1. Stop `lydiabox up`. Start it with `LYDIABOX_ACCESS_EMAILS=nobody@example.com`.
2. Sign in at `https://$HOST` as `$ME`. Access lets you in, but LydiaBox
   shows the token form with the notice that `$ME` is not on the allow
   list. Sign in with the admin token: the app opens.
3. Stop `lydiabox up` and remove `LYDIABOX_ACCESS_EMAILS`.

## 6. Non-browser clients

1. Without the service token, Access blocks the request:
   `curl -i -H "Authorization: Bearer $LYDIABOX_AUTH_TOKEN" https://$HOST/api/v1/status`
   answers with a redirect to the Access login (302) or 403.
2. With the service token and the admin token, it works:

   ```sh
   curl -H "CF-Access-Client-Id: $CF_ID" -H "CF-Access-Client-Secret: $CF_SECRET" \
     -H "Authorization: Bearer $LYDIABOX_AUTH_TOKEN" https://$HOST/api/v1/status
   ```

3. With the service token but a wrong admin token: `401` from LydiaBox.
4. WebDAV through rclone:

   ```sh
   rclone lsd :webdav: --webdav-url=https://$HOST/webdav/ --webdav-user=lydiabox \
     --webdav-pass="$(rclone obscure "$LYDIABOX_AUTH_TOKEN")" \
     --header "CF-Access-Client-Id: $CF_ID" --header "CF-Access-Client-Secret: $CF_SECRET"
   ```

   lists `home` and `workspace`.
5. S3 through rclone, with the same two `--header` flags:

   ```sh
   rclone lsd :s3:lydiabox --s3-provider=Minio --s3-force-path-style \
     --s3-endpoint=https://$HOST/s3 --s3-access-key-id=lydiabox \
     --s3-secret-access-key="$LYDIABOX_AUTH_TOKEN" \
     --header "CF-Access-Client-Id: $CF_ID" --header "CF-Access-Client-Secret: $CF_SECRET"
   ```

## 7. Bypass paths

1. Run `lydiabox cloudflare setup -hostname $HOST -email $ME -bypass /webdav,/s3`.
   The output lists two new applications `$HOST/s3` and `$HOST/webdav` and
   `Secret: unchanged`.
2. macOS Finder, Go, Connect to Server, `https://$HOST/webdav/`, user
   `lydiabox`, password = admin token: the volume mounts.
3. `aws --endpoint-url https://$HOST/s3 s3 ls s3://lydiabox/` with
   `AWS_ACCESS_KEY_ID=lydiabox` and `AWS_SECRET_ACCESS_KEY=$LYDIABOX_AUTH_TOKEN`
   lists `home/` and `workspace/`.
4. `curl -i https://$HOST/webdav/` without credentials answers `401` from
   LydiaBox (not the Access login).
5. `https://$HOST/` still needs the Access login.
6. Run setup again without `-bypass`. The two bypass applications are
   deleted, and `https://$HOST/webdav/` needs Access again.

## 8. Repeated setup

1. Run `lydiabox cloudflare setup -hostname $HOST -email $ME -email <second email>`.
   The output shows `reused tunnel`, `reused DNS record`, `updated Access
   policy`, `reused service token`, `updated Access application`, and
   `reused TURN key`.
2. The dashboard has one tunnel, one DNS record, one application, and the
   owner policy lists both emails.
3. Restart `lydiabox up`. The second email can sign in.
4. Run setup again with `-turn=false`: the output shows `deleted TURN key`,
   the key is gone from the dashboard, and after a restart of
   `lydiabox up` the Desktop page has no video through the tunnel. Run
   setup once more without `-turn=false`: a new key is created.
5. Run `lydiabox cloudflare setup -hostname other.example.com -email $ME`:
   it stops with a message to run teardown first, and changes nothing.

## 9. Teardown

1. Stop `lydiabox up` (a running `cloudflared` reconnects and can block
   the tunnel delete). Run `lydiabox cloudflare teardown`. It ends with
   `Remote access is removed.` If a step fails, the command says that the
   stored setup is kept; run it again. Record such a failure.
2. The dashboard has no tunnel, no DNS record for `$HOST`, no LydiaBox
   application, policy, service token, or TURN key. The One-time PIN login
   method stays.
3. `lydiabox cloudflare status` says that remote access is not set up.
   `lydiabox up` listens on `0.0.0.0:8080` again and does not start
   `cloudflared`.

## 10. Record the result

Add a section "Remote access run (Cloudflare, real account)" to
[conformance.md](conformance.md) with the date, the `cloudflared` and
LydiaBox versions, and pass or fail for each step. Report each difference
between the real API and the fake (`internal/cloudflare/cloudflaretest`)
as a bug, and fix the fake together with the code.
