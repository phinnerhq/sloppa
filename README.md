<p align="center"><img src="apps/web/public/floppa/stare.webp" alt="Floppa" width="160"></p>

# sloppa

A simple platform for helping with agentic workflows. Features:

- artifacts storage
- preview urls

## Run

Node 26 and pnpm:

```sh
pnpm install --frozen-lockfile
cp .env.example .env
pnpm build
pnpm start
```

Open `http://127.0.0.1:4100`; the API schema is at `/api/openapi.json`. See [.env.example](.env.example) for configuration and [Deploy with previews](docs/deploy.md) for Docker and reverse proxies.

The dashboard's robot menu currently provides a setup prompt for the MCP server.

## CLI

Build with `pnpm --filter @sloppa/cli build`.

- macOS and Linux: run `mkdir -p ~/.local/bin && ln -s "$PWD/apps/cli/dist/sloppa.mjs" ~/.local/bin/sloppa` and ensure `~/.local/bin` is on your `PATH`.
- Windows: add `apps\cli\dist` to your `PATH`; `sloppa.cmd` runs the CLI.

Create an agent token in the dashboard's robot menu. Pipe it into login, or set `SLOPPA_URL` and `SLOPPA_TOKEN`:

```sh
printf %s "$TOKEN" | sloppa login https://sloppa.example.com
sloppa artifact upload -c "owner/repo #42" checkout.png run.log
sloppa preview run -- pnpm dev
```

Run `sloppa --help` or `sloppa <command> --help` for the rest. Agents can use [skills/sloppa/SKILL.md](skills/sloppa/SKILL.md).
