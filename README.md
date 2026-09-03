# vercel-tui

A keyboard-driven terminal UI for Vercel: deployments across every
project and team at a glance, live build logs, and the management
actions people actually use. Follows the k9s / lazygit lineage — a
single static binary, no browser tab required.

## Why

The official `vercel` CLI tails the logs of one deployment and deploys
code, but it has no overview: no way to see deploy state across many
projects and teams, or to jump between deployments without typing URLs.
That surface lives in the web dashboard today. `vercel-tui` owns it from
the terminal.

## Install

Requires Go 1.27+.

```sh
go install github.com/shehjaddev/vercel-tui/cmd/vtui@latest
```

Run it:

```sh
vtui
```

## Setup

No separate login step. On first start `vtui` resolves a token in this
order:

1. `--token` flag
2. `VERCEL_TOKEN` environment variable
3. a token previously saved by `vtui` itself (`~/.config/vtui/token`)
4. a token you've stored with the official CLI (`~/.local/share/com.vercel.cli/auth.json`)

If none exist, the app shows a login screen: press `o` to open
vercel.com/account/tokens, paste a token, and it is validated and stored
under `~/.config/vtui/`.

CLI OAuth tokens expire roughly every few hours; `vtui` refreshes them
transparently on the first rejected request, so the tool keeps working
without restarting the official CLI.

If you work inside a linked directory, `vtui` reads `.vercel/project.json`
(the same file the official CLI writes) on launch to scope the view to that
project and team. `L` writes that file for the selected project, and
`--dir` overrides where it is read from / written to.

## Usage

`vtui` opens directly into the deployments view — one list of every
deployment across the active team, grouped by project. `t` switches the
active team and re-scopes the view. Every list auto-refreshes (5s by
default, 2s while a build is running).

### Deployments

| Key | Action |
|---|---|
| `j` `k` `g` `G` | move cursor / jump to first / last |
| `enter` | open the actions menu (logs, redeploy, rollback, copy, open, delete) |
| `e` | environment variables for the selected project |
| `E` | expand / collapse the selected project's deployments |
| `a` | toggle grouped-by-project vs all deployments |
| `l` | live logs of the selected deployment (scroll, `/` search, `n` next match) |
| `/` | filter list by project, branch, commit, author, url |
| `s` | cycle state filter (building / ready / error / canceled / queued) |
| `x` | cancel a building deployment |
| `R` | redeploy the same commit |
| `B` | instant rollback to production (ready production deployments only) |
| `D` | delete a deployment (type the project name to confirm) |
| `c` | copy the deployment URL |
| `o` | open the deployment in your browser |
| `L` | link the selected project to `./.vercel/project.json` (persists scope) |
| `U` | unlink: drop `.vercel/project.json` and clear the project filter |
| `?` | toggle this help |

The selected deployment's details show in a pinned block above the list:
project, state, branch, commit, target, repo (`owner/repo`), commit
message, author, timestamps, URL, and the project's domains. The list
columns are PROJECT, STATE, BRANCH, COMMIT, AGE, and ACTIVITY (last
status change).

### Project scoping

By default the list shows every project in the active team. Press `L` on a
project to write `.vercel/project.json` (scoping every future launch to
that project), and `U` to clear it. This is the only way to narrow the
view to a single project.

### Environment variables

`e` opens the selected project's environment variables: create, edit
value, change targets, and delete. Sensitive variables are write-only
through the API; the UI says so instead of pretending to read them.

## Flags

```
--token <token>   token (overrides all other resolution)
--target <name>   filter deployments by target (production, preview)
--branch <name>   filter deployments by git branch
--refresh <dur>   poll interval, e.g. 10s; 0 disables (default 5s)
--dir <path>      directory holding .vercel/project.json (default ".")
```

## Development

```sh
go build ./cmd/vtui && ./vtui
go test ./...
```

## Notes

- Owns the overview; it does not race the official CLI at deploying from
  disk or tailing a single deployment's logs — the roadmap deliberately
  keeps out of that.
- Scope is read + deployment management (redeploy, rollback, cancel,
  delete, env vars). Nothing else.
- The API client is a thin boundary with tolerant JSON decoding, so
  additive API changes don't break the binary overnight.
