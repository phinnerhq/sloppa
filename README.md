<p align="center"><img src="apps/web/public/floppa/stare.webp" alt="Floppa" width="160"></p>

# sloppa

A simple platform for helping with agentic workflows. Features:

- artifacts storage
- preview urls
- agent instructions and skills, synced to every machine

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

Each machine the CLI runs on signs in with a token of its own. The dashboard's robot menu gives a setup prompt to paste into an agent, which installs the CLI and registers its machine with a single-use setup link; running `sloppa setup` again on the same machine re-pairs it under a new token. To do it by hand, create a machine there and pipe its token into login, or set `SLOPPA_URL` and `SLOPPA_TOKEN`:

```sh
sloppa setup 'https://sloppa.example.com/setup/…'
printf %s "$TOKEN" | sloppa login https://sloppa.example.com
sloppa artifact upload -c "owner/repo #42" checkout.png run.log
sloppa preview run -- pnpm dev
```

### Agent instructions and skills

The dashboard's Agents page keeps one set of instructions and skills for all your machines: instructions shared by all, plus extra ones per machine, and skills imported from GitHub repositories (and updated from them later) or uploaded as folders, each turned on or off per machine. Each machine pulls them with:

```sh
sloppa agents sync --dry-run   # what would change
sloppa agents sync
```

It writes `~/.agents/AGENTS.md` and links it as `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md` and `~/.config/opencode/AGENTS.md`, for the harnesses whose folder exists. Skills go to `~/.agents/skills/<name>`, which Codex and opencode read, and are linked into `~/.claude/skills`. Whatever a sync replaces or removes is moved to `~/.agents/.sloppa-backups/` first, and only skills sloppa installed are ever removed. Each sync reports back, so the page shows which machines are behind or had problems. Run it from cron or a shell profile to keep machines current. Only the dashboard can edit, so a leaked machine token can't change what every machine's agents read. Set `GITHUB_TOKEN` on the server to import from private repositories.

Run `sloppa --help` or `sloppa <command> --help` for the rest. Agents can use [skills/sloppa/SKILL.md](skills/sloppa/SKILL.md).
