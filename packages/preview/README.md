# @sloppa/preview

Shared tickets, protocol schemas, and tunnel streams for the CLI and server. See [deployment](../../docs/deploy.md) for proxy setup and safety notes.

## Tunnel design

The CLI opens a control WebSocket at `/_connect` with a signed ticket. The route exists only while that socket stays open. Each `open` message asks it to connect to the dev server and open a data WebSocket at `/_data/<conn>`. If nothing listens, it sends `refuse` and the browser gets a 502.

Each data socket becomes a connection in a per-preview `http.Agent` pool. Node handles keep-alive, upgrades, and streaming. [TunnelStream](src/Tunnel.ts) carries raw bytes with half-close support and TCP backpressure, so slow readers do not cause unbounded buffering.

Use [Protocol.ts](src/Protocol.ts) for messages and final close codes, and [Ticket.ts](src/Ticket.ts) for ticket encoding. A reconnect with the same ticket replaces the old control socket; a different ticket cannot claim an occupied name.

## Proxy changes

The server's [proxy](../../apps/server/src/preview/Proxy.ts) rewrites `Host` to the dev server's loopback address so Vite accepts it. It replaces forwarding headers with the client's address and the public preview host and scheme. Redirects to the dev server point back at the preview. The proxy strips preview Basic auth before forwarding and supplies `X-Robots-Tag: noindex, nofollow` unless the dev server sets one.
