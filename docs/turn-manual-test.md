# Desktop stream test with a real TURN service

This checklist tests the desktop stream (observe and takeover) through a
real TURN relay. The automated tests use an in-process pion TURN server and
a fake Cloudflare API (`internal/guest/webrtc/turn_test.go`,
`internal/desktop/ice`). They do not cover the real Cloudflare API, a real
browser, real NATs, or a Cloudflare Tunnel.

Do the steps in the order given. Record pass or fail for each line.

## 1. Setup

1. Create a TURN key in the Cloudflare dashboard (Realtime > TURN Server).
   Keep the key ID and the API token.
2. Set up the host with a Cloudflare Tunnel and no inbound port: the web UI
   is reachable only through the tunnel hostname. Make sure that no
   firewall rule or router forward sends UDP to the host.
3. Start `lydiabox up` with:

   ```sh
   LYDIABOX_CLOUDFLARE_TURN_KEY_ID=<key id>
   LYDIABOX_CLOUDFLARE_TURN_API_TOKEN=<api token>
   LYDIABOX_WEBRTC_RELAY_ONLY=true
   LYDIABOX_WEBRTC_UDP_PORT=0
   LYDIABOX_TURN_TTL=5m
   ```

   A short TTL makes step 4 quick. Do the test with `chv` and with
   `docker` if both are available.

## 2. Stream through the relay

1. Open the tunnel hostname in Chrome on a different network (for example
   a phone hotspot) and sign in.
2. Open the Desktop page. Expect video within about 10 seconds.
3. Open `chrome://webrtc-internals`. In the peer connection, expect
   `iceTransportPolicy: relay` and Cloudflare `iceServers`. In the stats,
   expect the selected candidate pair with `candidateType` `relay` for
   the local candidate.
4. Click Takeover. Move the mouse, click, and type in the guest terminal.
   Expect the input to reach the guest.
5. Switch back to Observer and ask an agent to use the desktop. Expect
   the agent action overlay.

## 3. TCP and TLS

1. On the browser computer, block outbound UDP (for example with the
   macOS application firewall or `pf`, or on a network that blocks UDP).
2. Reload the Desktop page. Expect video. In `chrome://webrtc-internals`,
   expect the local relay candidate with `relayProtocol` `tcp` or `tls`.
3. On the host, block outbound UDP except DNS (for example
   `iptables -I FORWARD -p udp ! --dport 53 -j DROP` for `chv`, or the
   Docker `DOCKER-USER` chain for `docker`). Reload the Desktop page.
   Expect video: the guest uses TURN over TCP or TLS. Remove the rule
   afterwards.

## 4. Credential renewal and revocation

1. Keep the Desktop page open for longer than `LYDIABOX_TURN_TTL` (5
   minutes). Expect a short video pause about one minute before the TTL
   ends, then video with a new peer connection in
   `chrome://webrtc-internals`. The stream must not stop.
2. Close the page. In the Cloudflare dashboard TURN analytics, expect the
   usage to stop shortly afterwards (the host revokes the credentials).
3. Set a wrong `LYDIABOX_CLOUDFLARE_TURN_API_TOKEN` and restart. Open the
   Desktop page. Expect the error `desktop stream failed to start` with
   `HTTP 401` (relay-only mode does not fall back). The token must not
   appear in the host log.

## 5. Self-run coturn (optional)

1. Run coturn on a host with a public address with
   `use-auth-secret` and `static-auth-secret=<secret>`.
2. Start `lydiabox up` with `LYDIABOX_TURN_URLS=turn:<host>:3478?transport=udp,turns:<host>:5349?transport=tcp`,
   `LYDIABOX_TURN_SECRET=<secret>`, and `LYDIABOX_WEBRTC_RELAY_ONLY=true`.
3. Repeat section 2. Expect coturn log lines for two allocations per
   viewer (the browser and the guest).

## 6. Default configuration

1. Remove all ICE settings and set `LYDIABOX_WEBRTC_UDP_PORT=50000`.
2. Open the Desktop page from the LAN. Expect video as before, the Google
   STUN server in `chrome://webrtc-internals`, and a `host` candidate pair.
