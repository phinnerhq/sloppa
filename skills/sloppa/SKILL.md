---
name: sloppa
description: Attach screenshots, recordings, logs or reports to a PR or issue as links, share a running dev server at a public URL, or sync this machine's agent instructions and skills, with the `sloppa` CLI.
---

# sloppa

`sloppa` stores files as **artifacts** filed in **collections**, shares dev servers as **previews**, and keeps the user's **agent instructions and skills** for every machine. Every artifact link is public to anyone holding it: upload what you'd paste in the PR itself.

## Ready

`sloppa whoami` prints the user name, and the machine, when the CLI is installed and signed in.

- Command not found: ask the user to install it (the sloppa repo's README, under CLI).
- An error about signing in: ask the user for a setup link (the dashboard's robot menu makes a setup prompt with one), then run `sloppa setup '<link>'`. It registers this machine, or re-pairs it if it was set up before, and saves its token without showing it.

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

## Sync agent instructions and skills

The user edits them on the dashboard's Agents page; machines pull them.

1. `sloppa agents sync --dry-run` to see what would change, then `sloppa agents sync`.
2. It writes `~/.agents/AGENTS.md` and skills into `~/.agents/skills`, links them where Claude Code, Codex and opencode read them, and moves anything it replaces to `~/.agents/.sloppa-backups/`. Only skills it installed are ever removed.
3. Done when it exits 0. A nonzero exit lists what failed; the dashboard shows the same.

Editing instructions or skills takes the dashboard: a machine's token can only read them. Restart the agent session to pick up new instructions.

## Everything else

`sloppa <command> --help` lists each command's flags with examples: `artifact list|get|delete`, `collection list`, `preview list`. Lists print tab-separated lines; add `--json` for the API's JSON. `artifact delete` breaks links already posted, so it asks for `--yes`; check with `--dry-run` first.
