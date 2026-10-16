# @sloppa/preview

What `sloppa preview run` (apps/cli) and the server's preview listener (apps/server/src/preview) share: the tickets, the protocol, and the tunnel stream. Deployment is in [docs/deploy.md](../../docs/deploy.md).

## The tunnel

1. `sloppa preview run` gets a ticket from the server: `base64url(json).base64url(HMAC-SHA256)` of `{id, account, name?, passwordMac?, exp}`, keyed with `PREVIEW_SECRET`. The label is `name ?? id`, and `id` is 8 random base36 characters.
2. It opens the **control socket**, `GET /_connect` on the preview apex with `Authorization: Bearer <ticket>` and subprotocol `sloppa-preview.v1`, then sends `{"type":"hello","port":…}`. The server answers `ready` with the URL. **The route exists exactly as long as this socket does.** A reconnect with the same ticket replaces the old socket; another ticket gets `4002` for a name in use.
3. For each connection the proxy needs, the server sends `{"type":"open","conn":<id>}`. The CLI connects to its dev server and opens the **data socket** `/_data/<id>`, or answers `refuse` if nothing listens there (the browser gets a 502 saying so).
4. Data sockets carry raw bytes in binary frames, and the text frame `eof` half-closes. Backpressure is TCP's own: a full read buffer pauses the WebSocket, which stops reading its socket, so a slow dev server holds an upload back all the way to the browser.
5. On the server, a data socket is a connection in a per-preview `http.Agent` pool, under node:http's client. That handles keep-alive, upgrades (Vite's HMR) and streaming; compression passes through untouched.

Control close codes: `4001` bad or expired ticket, `4002` name taken, `4003` stopped from sloppa, `4004` expired, `4005` replaced. These are final, and the CLI stops its dev server. Anything else, such as `1001` when the server restarts, makes the CLI reconnect.

## What the proxy changes

- `Host` becomes `127.0.0.1:<port>`, which passes Vite's `allowedHosts`. `X-Forwarded-Host`, `-Proto` and `-For` describe the real request.
- `Location` headers pointing at the dev server's own address are pointed back at the preview.
- `Set-Cookie` loses any `Domain=` other than the preview's own host, so no preview can set cookies for its neighbours.
- Basic auth for password-protected previews (any user name) is checked against `passwordMac` and stripped before forwarding.
- `X-Robots-Tag: noindex, nofollow`, unless the dev server set one.
