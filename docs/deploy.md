# Deploy with previews

Build the [Dockerfile](../Dockerfile) and keep `/data` on a volume. The app and its previews share port 4100, told apart by host, so route both domains there:

- `PUBLIC_URL=https://sloppa.example.com`
- `PREVIEW_ORIGIN=https://preview.example.net`
- `PREVIEW_SECRET`, from `openssl rand -base64 32`
- `TRUST_PROXY=true`, so rate limits use the proxy's client address
- `BOOTSTRAP_USERNAME` and `BOOTSTRAP_PASSWORD` for the first account, and `S3_*` if artifacts live in a bucket

See [.env.example](../.env.example) for the rest. Password hashes from the Bun build still work.

## Configure Traefik

HTTP-01 issues a certificate for each preview hostname, exposing its name in Certificate Transparency logs. Use a DNS-01 wildcard certificate to keep individual preview names out of those logs.

In Dokploy, open **Web Server → Traefik → Edit traefik.yml**. Add this resolver alongside the existing one:

```yaml
certificatesResolvers:
  letsencrypt-dns:
    acme:
      email: you@example.com
      storage: /etc/dokploy/traefik/dynamic/acme-dns.json
      dnsChallenge:
        provider: cloudflare
        # Ask Cloudflare itself whether the TXT record is out, not a caching resolver.
        resolvers: ["1.1.1.1:53", "1.0.0.1:53"]
```

In **Modify Environment**, set `CF_DNS_API_TOKEN` to a token scoped to the preview domain's zone, with **Zone / DNS / Edit** and **Zone / Zone / Read**. Reload Traefik after both changes.

Create both DNS records. The CLI connects to the apex, which a wildcard does not cover:

```text
preview.example.net     A  <server IP>
*.preview.example.net   A  <server IP>
```

Keep both records DNS-only. Cloudflare's proxy can buffer or time out tunnels.

Give the preview domain its own router, with `letsencrypt-dns` and the apex and wildcard in `tls.domains`. Keep these settings together to avoid per-preview certificates:

```text
traefik.http.routers.previews.rule=Host(`preview.example.net`) || HostRegexp(`^[a-z0-9-]+\.preview\.example\.net$`)
traefik.http.routers.previews.tls.certresolver=letsencrypt-dns
traefik.http.routers.previews.tls.domains[0].main=preview.example.net
traefik.http.routers.previews.tls.domains[0].sans=*.preview.example.net
```

### Request timeouts

With Traefik v3.7.15 and `respondingTimeouts.readTimeout` reduced to `5s`, only request bodies hit the limit. A 10 MB upload at 1 MB/s returned `499` after 5.1 seconds, but succeeded directly. Control sockets stayed up for over a minute with 20-second pings. Idle browser WebSockets, SSE, and slow downloads also survived the limit.

At `300s`, uploads taking more than five minutes are cut off. The tunnels do not need a longer read timeout.

## Use Caddy or nginx

These examples route the preview domain; the app's own domain goes to the same port. A proxy in a container reaches `sloppa:4100` over a shared network; a proxy on the host needs the port published on loopback.

For Caddy, build the DNS provider with `xcaddy build --with github.com/caddy-dns/cloudflare`:

```caddyfile
preview.example.net, *.preview.example.net {
	tls {
		dns cloudflare {env.CF_DNS_API_TOKEN}
	}
	reverse_proxy sloppa:4100
}
```

For nginx, obtain the apex and wildcard certificate with certbot's DNS-01 plugin:

```sh
certbot certonly --dns-cloudflare --dns-cloudflare-credentials /etc/letsencrypt/cloudflare.ini \
	-d preview.example.net -d '*.preview.example.net'
```

This nginx example assumes the port is published on loopback. Replace `X-Forwarded-For` rather than appending, since sloppa trusts its last entry. Disable buffering for streams and uploads. Allow idle WebSocket connections for HMR:

```nginx
map $http_upgrade $connection_upgrade {
	default upgrade;
	''      close;
}

server {
	listen 443 ssl;
	server_name preview.example.net *.preview.example.net;
	ssl_certificate     /etc/letsencrypt/live/preview.example.net/fullchain.pem;
	ssl_certificate_key /etc/letsencrypt/live/preview.example.net/privkey.pem;
	client_max_body_size 0;

	location / {
		proxy_pass http://127.0.0.1:4100;
		proxy_http_version 1.1;
		proxy_set_header Host $host;
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection $connection_upgrade;
		proxy_set_header X-Forwarded-For $remote_addr;
		proxy_buffering off;
		proxy_request_buffering off;
		proxy_read_timeout 1h;
		proxy_send_timeout 1h;
	}
}
```

## Safety notes

- Anyone holding a shared artifact link can read it.
- Back up `DATA_DIR/accounts.sqlite`; it holds accounts and credentials. Without S3, back up the artifact files in `DATA_DIR` too.
- Keep the app and previews on different registrable domains so previews remain cross-site to the app. The proxy removes cookie domains other than the preview's own host.
- Previews go only through CLI tunnels. Request headers cannot select another upstream, and the CLI connects only to `127.0.0.1` or `::1`.
- Preview routes live in memory. A restart drops them; CLIs reconnect to the same URLs while their tickets remain valid.
