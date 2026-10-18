---
name: sloppa
description: Attach screenshots, recordings, logs or reports to a PR or issue as links, or share a running dev server at a public URL, with the `sloppa` CLI.
---

# sloppa

`sloppa` stores files as **artifacts** filed in **collections**, and shares dev servers as **previews**. Every artifact link is public to anyone holding it: upload what you'd paste in the PR itself.

## Ready

`sloppa whoami` prints the user name when the CLI is installed and signed in.

- Command not found: ask the user to install it (the sloppa repo's README, under CLI).
- An error about signing in: ask the user for the server URL and an agent token (the dashboard's robot menu), then run `printf %s "$TOKEN" | sloppa login <url>`, keeping the token out of chat and tool output.

## Attach files to a PR

1. Name the collection after the PR: `owner/repo #42`. Reuse that exact label for every file of the PR.
2. Upload: `sloppa artifact upload -c "owner/repo #42" checkout.png run.log`. For command output, pipe it in: `pnpm test 2>&1 | sloppa artifact upload -c "owner/repo #42" --name test-run.log -`.
3. Paste the printed Markdown into the PR description or a comment: images embed, everything else links.

Done when the PR shows the links. A file in the wrong collection: `sloppa artifact move <id> -c "owner/repo #43"`; its links stay the same.

## Share a dev server

1. Start it in the background: `sloppa preview run -- pnpm dev`. The dev server gets `PORT` and `HOST`; for one that only takes flags, use the placeholders: `sloppa preview run -- vite --host {host} --port {port} --strictPort`.
2. Wait for `preview_ready: <url>` on stderr, then give out that URL.
3. The preview lives exactly as long as the command: stop the command, or `sloppa preview stop <id>`, when done. `sloppa: tunnel lost (…), reconnecting` means the server blipped or restarted: the command reconnects to the same URL by itself, so leave it running.

`--name checkout` gives a stable subdomain, `--password` puts it behind one, `--ttl 2h` bounds it.

## Everything else

`sloppa <command> --help` lists each command's flags with examples: `artifact list|get|delete`, `collection list`, `preview list`. Lists print tab-separated lines; add `--json` for the API's JSON. `artifact delete` breaks links already posted, so it asks for `--yes`; check with `--dry-run` first.
