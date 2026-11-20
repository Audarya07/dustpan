# dustpan

See what your coding agents left behind on your machine, and clean it up safely.

## Why

If you use Claude Code, Codex or Cursor a lot, they leave things lying around: git worktrees nobody went back to, finished job folders, build binaries and scratch files in `/tmp`, old session transcripts. You don't notice it happening. Two problems come out of that:

1. Disk space slowly disappears and it's not obvious where it went.
2. Secrets end up in places you never look, like an `.env` copied into a forgotten worktree or cloud credentials sitting in a finished job's temp folder.

dustpan finds these leftovers, shows how much space they use, points out the ones that contain secrets, and lets you clean them up one at a time.

It also keeps receipts: a small record of which agent session created which file or folder. That way it can tell you "the session that made this ended 3 days ago and nothing has touched it since", instead of guessing from file dates.

Here's what a report looks like:

```
Your agents left 3.1 GB behind in 14 places.  2 contain secrets.

■ Secrets in leftovers (rotate these first)
  AKIA…7Q2X  AWS key  ~/.claude/jobs/7f3a91c2/tmp/acct_prod.json
  .env ×3    values   ~/repo/.claude/worktrees/x/.env
  Deleting the file doesn't undo a leak. Rotate the key.

■ Dead agent data                      3.1 GB
  Abandoned worktrees           1.6 GB   2, no unsaved work
  Old transcripts & logs        1.0 GB   older than 30d
  Finished / stale agent jobs   318 MB   4 folders
  Scratch files in /tmp         180 MB   8 files

■ Still in use, left alone
  2 worktrees on feature-1234   session active 4 min ago

→ clean   → report --details
```

## Getting started

You need macOS or Linux, and Go 1.26 or newer.

Install it:

```sh
go install github.com/Audarya07/dustpan/cmd/dustpan@latest
```

`go install` puts the binary in Go's bin folder (usually `~/go/bin`). If you get `command not found: dustpan` afterwards, that folder isn't on your `PATH` yet. Add it once (for zsh; use `~/.bashrc` for bash):

```sh
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.zshrc
source ~/.zshrc
dustpan version
```

Run a report. It only reads, and usually takes a few seconds. The very first run can take up to a minute, because it reads through your existing Claude Code transcripts once.

```sh
dustpan report
```

Add `--details` to see every item, why it's listed and which session created it.

When you want to clean up, run this in your own terminal window:

```sh
dustpan clean
```

It goes through the items one by one and asks what to do with each: move it to the Trash, keep it, or stop.

### Claude Code plugin (optional)

The plugin records receipts while your agents work, shows a short reminder at most once a day when something new turns up, and adds a `/dustpan:leftovers` command so the agent can walk you through the report. The agent can never delete anything.

Install the binary first (above), then add the plugin from your terminal:

```sh
claude plugin marketplace add Audarya07/dustpan
claude plugin install dustpan@dustpan
```

Or from inside a Claude Code session, run `/plugin marketplace add Audarya07/dustpan` and then `/plugin install dustpan@dustpan`. Restart Claude Code afterwards so the hooks load.

The hooks call the `dustpan` binary, so Claude Code has to be able to find it on its `PATH`. If you start Claude Code from a terminal where `dustpan version` works, you're set. If the binary can't be found, the hooks quietly do nothing: no errors, but no receipts or reminders either.

## What it looks for

| Leftover | Where | Counted as leftover when |
|---|---|---|
| Abandoned worktrees | `<repo>/.claude/worktrees`, `~/.codex/worktrees`, `~/.cursor/worktrees`, plus worktrees an agent created elsewhere | it has no commits, or its work is merged or pushed, and nothing is unsaved |
| Finished or stale agent jobs | `~/.claude/jobs` | the job is done, or has been idle for 14 days or more |
| Scratch files | `/tmp` and `$TMPDIR` | an agent created it and that session has ended |
| Old transcripts | `~/.claude/projects`, `~/.codex/sessions` | it's past the agent's retention period (30 days unless you changed Claude's `cleanupPeriodDays`) |

Everything it finds is also checked for secrets using the [gitleaks](https://github.com/gitleaks/gitleaks) rules.

For now only Claude Code sessions get receipts, so only those show which session left something behind. Codex and Cursor worktrees and old Codex transcripts are still found and cleaned under the same rules. Cursor transcripts aren't scanned yet.

## How it keeps your stuff safe

The rule throughout is simple: if dustpan isn't sure, it leaves the item alone and tells you why.

- Nothing is deleted on its own. `report`, the hooks and `/dustpan:leftovers` only read. The one command that deletes is `dustpan clean`, and it needs a real terminal and your answer for each item.
- Anything still in use is skipped. That covers files a running process has open, sessions that are still going, anything changed in the last 24 hours, and repos in the middle of a rebase or merge. Each item is checked again right before anything happens to it.
- Unsaved work is never offered. Worktrees with uncommitted changes, ignored files you can't regenerate (like `.env`), or commits that don't exist anywhere else are kept. So is anything that contains a git checkout or sits inside one.
- Only things an agent made are offered. If there's no evidence an agent created something, it isn't touched. Your own checkouts, agent settings and memory, and shell dotfiles are always off limits.
- You can undo a cleanup. Items go to the Trash, and dustpan never empties it for you. One catch: a worktree you restore from the Trash is no longer registered with git. Its branch is still there, so you can check it out into a new worktree.
- Secrets get extra handling. A secret in the Trash is still readable, so for those items dustpan also offers permanent deletion, which you confirm by typing `delete`. Keep in mind that deleting a leaked key doesn't make it safe again. Rotate it.
- Secrets are never printed in full. You'll see at most the first 4 and last 4 characters. dustpan only stores file paths and timestamps, never file contents.

## Does it slow anything down?

Not noticeably. These numbers are from an Apple Silicon MacBook:

| What | When it runs | Cost |
|---|---|---|
| Hook after a tool call | each time the agent runs a shell command or writes a file | under 10 ms |
| Hook at session start | once per session | about 50 ms, a few hundred ms on the very first run |
| Background refresh | once when a session ends | about 3.5 s at low CPU priority, about 55 MB of memory |
| `dustpan report` | when you run it | 1 to 4 s |
| Files on disk | | a few hundred KB |

The hooks give up after 450 ms and always exit cleanly, so they can't hold up or break an agent session. The reminder shows at most once a day, only when there's something new (a new secret, or more than 1 GB of new leftovers), and backs off to once a week if you keep ignoring it.

## Commands

| Command | What it does |
|---|---|
| `dustpan report` | Shows the summary. `--details` lists every item, `--json` gives machine-readable output. Read only. |
| `dustpan clean` | Goes through cleanable items one at a time and asks before each. |
| `dustpan stats` | Your own numbers: space recovered, secrets found, how often you chose to keep things. `--share` prints a version with no paths or names that you can paste anywhere. |
| `/dustpan:leftovers` | Plugin command. Shows the report inside Claude Code and explains the items. Never deletes. |

## Privacy

There's no telemetry and dustpan never connects to the network. Everything it keeps lives in `~/.local/state/dustpan/` (or `$XDG_STATE_HOME/dustpan/`), readable only by your user.

## Known limitations

- macOS and Linux only.
- On first run it rebuilds receipts from your existing Claude Code transcripts. If the transcript for a leftover is already gone, dustpan can't tell who made it, so it won't offer to clean it.
- Worktrees an agent created with a path like `git worktree add ../wt` aren't attributed yet.

## Uninstall

If you installed the plugin, remove it first. This removes the marketplace and uninstalls the plugin in one go:

```sh
claude plugin marketplace remove dustpan
```

(Inside a Claude Code session the same thing is `/plugin marketplace remove dustpan`.)

Then remove the binary from Go's bin folder, and dustpan's own state and config folders:

```sh
GOBIN_DIR="$(go env GOBIN)"; rm -f "${GOBIN_DIR:-$(go env GOPATH)/bin}/dustpan"
rm -rf "${XDG_STATE_HOME:-$HOME/.local/state}/dustpan" "${XDG_CONFIG_HOME:-$HOME/.config}/dustpan"
```

This doesn't touch anything dustpan moved to your Trash; those items stay there until you empty it.

## License

MIT, see [LICENSE](LICENSE).
