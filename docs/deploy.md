# Deploying sloppa with previews

`compose.yaml` runs one service, **sloppa**, on two ports: the app (dashboard, API and MCP) on 4100, and previews on 4200, where `sloppa preview run` connects back and browsers reach the dev servers. Neither port is published on the host: the reverse proxy reaches both over its own network.

```text
browser ── https://sloppa.example.com ──────────▶ Traefik ──▶ sloppa:4100

browser ── https://<label>.preview.example.net ──▶ Traefik ──▶ sloppa:4200 ──┐
                                                                                  │ data WebSocket per connection
sloppa preview run ── wss://preview.example.net/_connect ──▶ Traefik ──▶ sloppa:4200 ┘
     └── 127.0.0.1:<port>  the dev server, never reachable any other way
```

## Traefik under Dokploy

Dokploy's `websecure` entrypoint defaults to the `letsencrypt` resolver, which uses HTTP-01 and so gets one certificate per hostname. Every preview's name would then land in the CT logs, where scanners pick it up within seconds. Previews need a wildcard instead, which only DNS-01 can issue.

### 1. Add a DNS-01 resolver (static config, once)

In Dokploy, open **Web Server → Traefik → Edit traefik.yml** (it's `/etc/dokploy/traefik/traefik.yml`) and add a second resolver under the existing `certificatesResolvers`:

```yaml
certificatesResolvers:
  letsencrypt:
    # ...unchanged...
  letsencrypt-dns:
    acme:
      email: you@example.com
      storage: /etc/dokploy/traefik/dynamic/acme-dns.json
      dnsChallenge:
        provider: cloudflare
        # Ask Cloudflare directly whether the TXT record is out, rather than a caching resolver.
        resolvers:
          - 1.1.1.1:53
          - 1.0.0.1:53
```

Then give Traefik the token in **Web Server → Traefik → Modify Environment**:

```env
CF_DNS_API_TOKEN=<Cloudflare token with Zone / DNS / Edit and Zone / Zone / Read, for the preview domain's zone only>
```

Reload Traefik after both changes; existing routes and certificates aren't affected.

### 2. DNS

`*.preview.example.net` already points at this host. The apex, which the CLI's tunnels connect to, needs a record of its own because a wildcard doesn't cover it:

```text
preview.example.net     A  <server IP>
*.preview.example.net   A  <server IP>   (exists)
```

Keep both DNS-only (grey cloud): Cloudflare's proxy would buffer and time out the tunnels.

### 3. The router

The `sloppa` service's labels in `compose.yaml` carry it, and they're the part to check. With two ports, each router names its service:

```yaml
- traefik.http.routers.previews.rule=Host(`preview.example.net`) || HostRegexp(`^[a-z0-9-]+\.preview\.example\.net$`)
- traefik.http.routers.previews.entrypoints=websecure
- traefik.http.routers.previews.tls.certresolver=letsencrypt-dns # overrides websecure's HTTP-01 default
- traefik.http.routers.previews.tls.domains[0].main=preview.example.net
- traefik.http.routers.previews.tls.domains[0].sans=*.preview.example.net
- traefik.http.routers.previews.service=previews
- traefik.http.services.previews.loadbalancer.server.port=4200
```

`tls.domains` is what makes Traefik request the wildcard up front. Without it, it would request a certificate for each host it sees. Once deployed, CT logs (Cert Spotter, crt.sh) should show only `*.preview.example.net` and the apex.

As a file-provider route instead (`/etc/dokploy/traefik/dynamic/previews.yml`), assuming the container is reachable as `sloppa` on `dokploy-network`:

```yaml
http:
  routers:
    previews:
      rule: Host(`preview.example.net`) || HostRegexp(`^[a-z0-9-]+\.preview\.example\.net$`)
      entryPoints: [websecure]
      service: previews
      tls:
        certResolver: letsencrypt-dns
        domains:
          - main: preview.example.net
            sans: ["*.preview.example.net"]
  services:
    previews:
      loadBalancer:
        servers:
          - url: http://sloppa:4200
```

### Timeouts, measured

`websecure` sets `respondingTimeouts.readTimeout: 300s`. Measured on Traefik v3.7.15, with the timeout scaled down to 5s:

| Through Traefik                                          | Result                                              |
| -------------------------------------------------------- | --------------------------------------------------- |
| Control socket idle 20s between pings, for over a minute | Stays up: upgraded connections are hijacked         |
| Browser WebSocket (HMR) idle for 12s                     | Stays up                                            |
| SSE stream and a slow download, 12s each                 | Complete                                            |
| A 10 MB upload at 1 MB/s                                 | `499` at exactly 5.1s; it succeeds direct to sloppa |

So only request **bodies** are bounded: in production, an upload to a preview that takes more than 5 minutes is cut. The tunnels themselves don't need the timeout raised. The server still pings every 20 seconds for proxies and NATs that do drop idle connections.

## Moving sloppa from a Dokploy application to this compose app

sloppa runs today as a Dokploy application, routed through the file provider. Both can't own `sloppa.example.com` at once, so:

1. Make the Traefik changes above.
2. Create a compose app from this repo with `compose.yaml`, and set `PREVIEW_SECRET` (`openssl rand -base64 32`) plus the old app's environment (`BOOTSTRAP_*`, `S3_*`). Passwords hashed by the Bun build still verify.
3. Before the first deploy, copy the old app's `/data` (it holds `accounts.sqlite`, and the files without S3) into the new `sloppa-data` volume.
4. Remove the old application's domain, deploy the compose app, then delete the old application.
5. Stop whatever served previews before, if anything did.

## Caddy instead

Stock Caddy has no DNS providers, so build it with one first: `xcaddy build --with github.com/caddy-dns/cloudflare`. Then:

```caddyfile
preview.example.net, *.preview.example.net {
	tls {
		dns cloudflare {env.CF_DNS_API_TOKEN}
	}
	reverse_proxy sloppa:4200
}
```

Caddy streams responses, passes WebSocket upgrades through, and sets `X-Forwarded-For` by itself. It sets no read timeout on proxied requests, so long uploads go through. Set `TRUST_PROXY=true`, as for the app.

## nginx instead

Get the wildcard from certbot's or lego's DNS-01 plugin:

```sh
certbot certonly --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
  -d preview.example.net -d '*.preview.example.net'
```

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    http2 on;
    server_name preview.example.net *.preview.example.net;

    ssl_certificate     /etc/letsencrypt/live/preview.example.net/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/preview.example.net/privkey.pem;

    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:4200;  # sloppa's preview port
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        # Replaced, not appended: with TRUST_PROXY, sloppa believes the last entry.
        proxy_set_header X-Forwarded-For $remote_addr;
        # SSE and streamed responses as they come, uploads without spooling them to disk first.
        proxy_buffering off;
        proxy_request_buffering off;
        # nginx counts idle time on upgraded connections too: HMR sockets can sit quiet a while.
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
    }
}
```

## Safety notes

- The preview port only ever dials through the tunnel a CLI opened. No header picks an upstream, and the CLI connects to `127.0.0.1` (or `::1`) alone.
- `*.preview.example.net` and `sloppa.example.com` are different registrable domains, so previews are cross-site to sloppa. The proxy also strips `Domain=` from the cookies previews set, so they stay on their own subdomain.
- Previews live in sloppa's memory, on their own port and listener, apart from the app's. A restart drops every route, and the CLIs reconnect to the same URLs on their own.
