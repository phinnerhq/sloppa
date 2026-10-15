<p align="center"><img src="apps/web/public/floppa/stare.webp" alt="Floppa" width="160"></p>

# sloppa-artifacts

A place for agents to put PR artifacts (screenshots, videos, logs) instead of pushing orphan branches to GitHub.

## Run

```sh
bun install
cp .env.example .env
bun run build
bun run start      # http://127.0.0.1:4100
```

Or with Docker:

```sh
docker build -t sloppa-artifacts .
docker run -d -p 127.0.0.1:4100:4100 -v sloppa-artifacts-data:/data \
  -e BOOTSTRAP_USERNAME=admin -e BOOTSTRAP_PASSWORD=change-me -e PUBLIC_URL=https://artifacts.example.com sloppa-artifacts
```

## Connect an agent

Sign in, open the robot menu and make a setup prompt. Paste it into Claude Code, Codex or any agent with a shell: it trades the link for a token of its own, installs the MCP server, then asks whether to revoke the link. Links expire after 30 minutes; until then the same prompt sets up more agents. Revoke an agent's token from the same menu.

The agent gets a one-time upload URL per file, `curl`s the file to it, and pastes the returned markdown into the PR.

## Config

| Variable                                                                            |                                                                             |
| ----------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| `BOOTSTRAP_USERNAME`, `BOOTSTRAP_PASSWORD`                                          | The first user. Required until one exists; a new password resets theirs.    |
| `PUBLIC_URL`                                                                        | Base for every link handed out.                                             |
| `DATA_DIR`                                                                          | Accounts, index, thumbnails, and the files when S3 is off. Default `.data`. |
| `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | Store files in a bucket instead.                                            |
| `TRUST_PROXY`                                                                       | Behind a reverse proxy: rate-limit sign-ins by `X-Forwarded-For`.           |
| `HOST`, `PORT`, `STATIC_DIR`                                                        | Defaults: `127.0.0.1`, `4100`, unset.                                       |

Shared links are public to anyone holding them; everything else needs signing in, or an agent token. Keep `DATA_DIR/accounts.sqlite`: the rest of `DATA_DIR` rebuilds from storage. API docs live at `/api/openapi.json`.
