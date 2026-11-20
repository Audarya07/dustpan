# dustpan

Reports what coding agents left behind on your machine, and which agent session created it.

Agents like Claude Code leave abandoned git worktrees, finished job folders, build binaries and scratch files in `/tmp`, and old transcripts. Some of it holds secrets: an `.env` copied into a forgotten worktree, cloud credentials in a job's temp folder. This tool finds it, shows how much space it takes, and tells you which leftovers contain secrets. It records receipts, so "this looks old" becomes "the session that made this ended 3 days ago and nothing has touched it since".

Codex and Cursor worktree folders and old Codex session transcripts are cleaned up under the same safety rules; in this version only Claude Code sessions get receipts (so only they show which session left something). Cursor transcripts aren't scanned.

```
Your agents left 3.1 GB behind in 14 places.  2 contain secrets.

■ Secrets in leftovers  — rotate these first
  AKIA…7Q2X  AWS key  ~/.claude/jobs/7f3a91c2/tmp/acct_prod.json
  .env ×3    values   ~/repo/.claude/worktrees/x/.env

■ Dead agent data                      3.1 GB
  Abandoned worktrees           1.6 GB   2, no unsaved work
  Old transcripts & logs        1.0 GB   older than 30d
  Finished / stale agent jobs   318 MB   4 folders
  Scratch files in /tmp         180 MB   8 files

■ Still in use — left alone
  2 worktrees on feature-1234   session active 4 min ago

→ clean   → report --details
```

## Install

macOS and Linux. Once the repo is published:

```
go install github.com/Audarya07/dustpan/cmd/dustpan@latest
```

Until then, build from a checkout and put the binary on your `PATH`:

```
go build -o dustpan ./cmd/dustpan
```

Optional Claude Code plugin (installs the hooks and the `/leftovers` command):

```
/plugin marketplace add <path-to-repo>/plugin
/plugin install dustpan@dustpan
```

The hooks do nothing if the `dustpan` binary is not on your `PATH`.

## Safety promises

- It never deletes anything by itself. Hooks and reports only read, and only write to the tool's own state folder.
- The only thing that deletes is `dustpan clean`. It needs a real terminal, asks before every item, and re-checks each item right before acting. There is no non-interactive delete command, and `/leftovers` never deletes.
- When unsure, it keeps. An item is offered only if every check passes; otherwise it is listed as "left alone" with the reason.
- Nothing in use is offered: no running process inside it, no live session, nothing changed in the last 24 hours, no git operation in progress.
- Worktrees with uncommitted work, non-regenerable ignored files (like `.env`), or commits that exist nowhere else are never offered.
- Only items with proof an agent created them are offered. Your own worktrees are not listed.
- Your code and config are never touched: main checkouts, files inside git checkouts, agent settings and memory, shell dotfiles.
- Deletions go to the Trash, so they can be undone. A worktree put back from the Trash is no longer registered with git; its branch is kept, so to work on it again check the branch out in a new worktree. For items containing secrets, permanent deletion (you type `delete`) is offered alongside a "trash anyway" choice, because a trashed secret is still readable. Where an item's disk has no Trash, permanent deletion is the only option. It never empties the Trash.
- Secrets are always masked (at most the first 4 and last 4 characters are shown). Receipts and output hold paths and timestamps only, never file contents or full secret values. Deleting a secret does not undo a leak: copies may exist in backups or provider logs.

## Commands

- `dustpan report` shows the summary. `--details` lists every item with path, size, age and which session created it. `--json` prints the same data for scripts. All read-only.
- `dustpan clean` walks through offered items one at a time: Delete / Keep / Skip the rest.
- `dustpan stats` shows local numbers (reports run, space recovered, secrets found, keep rate). `--share` prints an anonymised summary you can paste anywhere.
- `dustpan hook <event>` is the internal entry point the plugin's hooks call.
- `/leftovers` (plugin) runs the report in your Claude Code session so the agent can explain items. It ends by telling you to run `dustpan clean` in your own terminal.

## Where state lives

`$XDG_STATE_HOME/dustpan/`, by default `~/.local/state/dustpan/`: receipts, cached report, stats, nudge state, reconstruction progress, session snapshots and lock files. Folders are created with mode `0700` and files with `0600`; an existing folder's mode is not changed.

## No telemetry

It makes no network connections, ever. Nothing is sent anywhere.

## Uninstall

Remove the plugin (`/plugin uninstall dustpan@dustpan`, then `/plugin marketplace remove dustpan`), delete the binary, and delete the state folder: `rm -r ~/.local/state/dustpan`.
