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
  -e UPLOAD_TOKEN=change-me -e PUBLIC_URL=https://artifacts.example.com sloppa-artifacts
```

## Connect an agent

```sh
claude mcp add --transport http sloppa-artifacts http://127.0.0.1:4100/mcp \
  --header "Authorization: Bearer $UPLOAD_TOKEN"
```

The agent gets a one-time upload URL per file, `curl`s the file to it, and pastes the returned markdown into the PR.

## Config

| Variable                                                                            |                                                                  |
| ----------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `UPLOAD_TOKEN`                                                                      | Required. Guards the dashboard, API and MCP.                     |
| `PUBLIC_URL`                                                                        | Base for every link handed out.                                  |
| `DATA_DIR`                                                                          | SQLite index, and the files when S3 is off. Defaults to `.data`. |
| `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | Store files in a bucket instead.                                 |
| `HOST`, `PORT`, `STATIC_DIR`                                                        | Defaults: `127.0.0.1`, `4100`, unset.                            |

Shared links are public to anyone holding them; everything else needs the token. API docs live at `/api/openapi.json`.
