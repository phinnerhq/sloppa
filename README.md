<p align="center"><img src="apps/web/public/floppa/stare.webp" alt="Floppa" width="160"></p>

# sloppa

A simple platform for helping with agentic workflows. Features:

- artifacts storage
- preview urls

## Run

Go 1.26, Node 26 and pnpm:

```sh
pnpm install --frozen-lockfile
cp .env.example .env
pnpm build
./dist/sloppa serve
```

`pnpm build` compiles the dashboard into one static binary, `dist/sloppa`, which is both the server and the CLI. Open `http://127.0.0.1:4100`; the API schema is at `/api/openapi.json`. See [.env.example](.env.example) for configuration and [Deploy with previews](docs/deploy.md) for Docker and reverse proxies.

For development, run `pnpm dev:server` and `pnpm dev` side by side: Vite serves the dashboard on `http://127.0.0.1:5173` and proxies the API.

After changing the API, `pnpm gen` rewrites `openapi.json` from the server, then the dashboard's client from it. The client isn't committed: install, `dev`, `build` and `check` generate it.

## CLI

Put `dist/sloppa` on your `PATH`, or `go install github.com/phinnerhq/sloppa/cmd/sloppa@latest` (without the dashboard, which only `serve` needs).

The dashboard's robot menu gives a setup prompt to paste into an agent, which installs and signs in the CLI by itself. To do it by hand, create an agent token there and pipe it into login, or set `SLOPPA_URL` and `SLOPPA_TOKEN`:

```sh
printf %s "$TOKEN" | sloppa login https://sloppa.example.com
sloppa artifact upload -c "owner/repo #42" checkout.png run.log
sloppa preview run -- pnpm dev
```

Run `sloppa --help` or `sloppa <command> --help` for the rest. Agents can use [skills/sloppa/SKILL.md](skills/sloppa/SKILL.md).
