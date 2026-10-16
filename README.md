<p align="center"><img src="apps/web/public/floppa/stare.webp" alt="Floppa" width="160"></p>

# sloppa

A place for agents to put artifacts (screenshots, videos, logs), grouped in collections like `owner/repo #42`, and to share the dev servers they run.

## Run

Node 26 and pnpm:

```sh
pnpm install
cp .env.example .env
pnpm build
pnpm start         # http://127.0.0.1:4100
```

Or with Docker:

```sh
docker build -t sloppa .
docker run -d -p 127.0.0.1:4100:4100 -p 127.0.0.1:4200:4200 -v sloppa-data:/data \
  -e BOOTSTRAP_USERNAME=admin -e BOOTSTRAP_PASSWORD=change-me -e PUBLIC_URL=https://artifacts.example.com sloppa
```

## Connect an agent

Sign in and open the robot menu: paste its setup prompt into your agent, and it sets up the MCP server itself.

The agent gets a one-time upload URL per file, `curl`s the file to it, and links it from the PR.

## Previews

`sloppa preview run` shares a dev server at `https://<id>.preview.example.com` for exactly as long as the command runs. The URL dies with the process, and stopping the preview kills the dev server's whole process group:

```sh
sloppa preview run -- pnpm dev                        # reads PORT and HOST
sloppa preview run --name checkout -- vite --host {host} --port {port} --strictPort
sloppa preview run --password hunter2 --ttl 2h -- python3 -m http.server {port} --bind {host}
```

The dev server gets `PORT`, `HOST=127.0.0.1`, `PREVIEW_URL` and `PREVIEW_ID`. The CLI prints `preview_url:` once the tunnel is up, then `preview_ready:` once something listens. The dashboard's previews menu and the `preview_list` / `preview_stop` MCP tools list and stop them.

The server serves them on a port of its own (`PREVIEW_PORT`, 4200), routed by subdomain. See [docs/deploy.md](docs/deploy.md) for the compose app, Traefik, Caddy and nginx.

## CLI

`apps/cli` builds `sloppa`, one script for the Node already installed (24 or newer): `pnpm --filter @sloppa/cli build`. On macOS and Linux, link it onto your `PATH`, e.g. `ln -s "$PWD/apps/cli/dist/sloppa.mjs" ~/.local/bin/sloppa`. On Windows, add `apps\cli\dist` to your `PATH`: `sloppa.cmd` runs it there.

```sh
printf %s "$TOKEN" | sloppa login https://artifacts.example.com   # or set SLOPPA_URL and SLOPPA_TOKEN
sloppa upload -c "owner/repo #42" checkout.png run.log            # prints Markdown links
sloppa preview run -- <dev command>
```

## Config

| Variable                                                                            |                                                                                 |
| ----------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `BOOTSTRAP_USERNAME`, `BOOTSTRAP_PASSWORD`                                          | The first user.                                                                 |
| `PUBLIC_URL`                                                                        | Base for every link handed out.                                                 |
| `DATA_DIR`                                                                          | Accounts, index, thumbnails, and the files when S3 is off. Default `.data`.     |
| `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | Store files in a bucket instead.                                                |
| `TRUST_PROXY`                                                                       | Behind a reverse proxy: rate-limit sign-ins by `X-Forwarded-For`.               |
| `PREVIEW_ORIGIN`, `PREVIEW_SECRET`                                                  | Previews: `<label>.<origin>`, and the secret their tickets are signed with.     |
| `PREVIEW_HOST`, `PREVIEW_PORT`                                                      | Where previews are served. Defaults: `HOST`, `4200`.                            |
| `PREVIEW_MISS_BURST`, `PREVIEW_MISS_EVERY`                                          | Guesses at unknown previews or passwords per client. Default `20`, `3 seconds`. |
| `HOST`, `PORT`, `STATIC_DIR`                                                        | Defaults: `127.0.0.1`, `4100`, unset.                                           |

Shared links are public to anyone holding them; everything else needs signing in or an agent token. Back up `DATA_DIR/accounts.sqlite`. API docs live at `/api/openapi.json`.
