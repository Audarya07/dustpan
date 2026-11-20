# dustpan

**See what your coding agents left behind — and safely sweep it up.**

Coding agents (Claude Code, Codex, Cursor) leave things on your machine: abandoned git worktrees, finished job folders, build binaries and scratch files in `/tmp`, old session transcripts. Nobody notices, so two things happen quietly:

- **Disk space disappears**, and it isn't obvious where it went.
- **Secrets pile up in places you never look**: an `.env` copied into a forgotten worktree, cloud credentials in a finished job's temp folder.

dustpan finds those leftovers, tells you how much space they take, flags the ones that contain secrets, and lets you clean them up one item at a time. It also keeps **receipts** — a record of which agent session created what — so instead of guessing "this looks old", it can say "the session that made this ended 3 days ago and nothing has touched it since".

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

## Quick start

Requires macOS or Linux and Go 1.25+.

1. **Install the binary**

   ```sh
   go install github.com/Audarya07/dustpan/cmd/dustpan@latest   # once published
   # or, from a checkout:
   go build -o dustpan ./cmd/dustpan && mv dustpan ~/go/bin/      # any folder on your PATH
   ```

2. **See what's there** (read-only, takes a few seconds):

   ```sh
   dustpan report            # the summary above
   dustpan report --details  # every item, why it's listed, which session made it
   ```

3. **Clean up** — in your own terminal window:

   ```sh
   dustpan clean
   ```

   It shows one item at a time and asks: delete (to the Trash), keep, or skip the rest.

4. **Optional: the Claude Code plugin.** Adds hooks that record receipts as your agents work, a once-a-day reminder when something new turns up, and a `/leftovers` command that lets the agent explain the report to you (it can never delete anything).

   ```
   /plugin marketplace add <path-to-dustpan-repo>/plugin
   /plugin install dustpan@dustpan
   ```

   The hooks need the `dustpan` binary on your `PATH`; without it they silently do nothing.

## What it looks at

| Leftover | Where | Considered dead when |
|---|---|---|
| Abandoned worktrees | `<repo>/.claude/worktrees`, `~/.codex/worktrees`, `~/.cursor/worktrees`, and worktrees an agent created elsewhere | no commits, or merged/pushed — and nothing unsaved |
| Finished or stale agent jobs | `~/.claude/jobs` | done, or idle for 14+ days |
| Scratch files | `/tmp` and `$TMPDIR` | an agent created it and that session has ended |
| Old transcripts | `~/.claude/projects`, `~/.codex/sessions` | past the agent's retention (Claude's `cleanupPeriodDays`, default 30 days) |

Each of these is also checked for secrets using the [gitleaks](https://github.com/gitleaks/gitleaks) rule set.

Only Claude Code sessions get receipts in this version, so only they show which session left something. Codex and Cursor worktrees and old Codex transcripts are still cleaned up under the same safety rules. Cursor transcripts aren't scanned.

## Safety promises

dustpan is built around one rule: **when unsure, keep it.**

- **It never deletes on its own.** Reports, hooks and `/leftovers` only read. The only thing that deletes is `dustpan clean`, which needs a real terminal and your answer for every single item.
- **Nothing in use is offered.** Not if a process has files open there, its agent session is still running, it changed in the last 24 hours, or a git operation is in progress. Each item is re-checked right before acting.
- **Nothing unsaved is offered.** Worktrees with uncommitted changes, ignored files that aren't rebuildable (like `.env`), or commits that exist nowhere else are kept. So is anything that contains or sits inside a git checkout.
- **Your own things are left alone.** Only items with evidence that an agent created them are offered. Your main checkouts, files inside git checkouts, agent settings and memory, and shell dotfiles are never touched.
- **Deleting can be undone.** Items go to the Trash. (A worktree restored from the Trash is no longer registered with git; its branch is kept, so check it out again in a new worktree.) dustpan never empties the Trash for you.
- **Secrets get extra care.** Because a trashed secret is still readable, items containing secrets also offer permanent deletion — you must type `delete` to confirm. Deleting does not undo a leak: if a key was exposed, rotate it.
- **Secrets are never shown in full.** At most the first 4 and last 4 characters appear. dustpan stores paths and timestamps only, never file contents.

## Will it slow me down?

No — it's designed to stay out of the way. Measured on a MacBook (Apple Silicon):

| What | When | Cost |
|---|---|---|
| Hook after a tool call | every Bash/Write call by the agent | under 10 ms |
| Hook at session start | once per session | ~50 ms (a few hundred ms on the very first run after install) |
| Background refresh | once when a session ends | ~3.5 s, at low CPU priority, ~55 MB memory |
| `dustpan report` | when you run it | ~1–4 s |
| State on disk | — | a few hundred KB |

Hooks have a hard 450 ms limit and always exit cleanly, so they can't block or break an agent session. The reminder appears at most once a day, and only when something new turns up (a new secret, or more than 1 GB of new leftovers); if you ignore it a few times, it drops to weekly.

## Commands

| Command | What it does |
|---|---|
| `dustpan report` | Summary of leftovers. `--details` lists every item; `--json` for scripts. Read-only. |
| `dustpan clean` | Walks through cleanable items one at a time: delete / keep / skip the rest. |
| `dustpan stats` | Your local numbers: space recovered, secrets found, how often you kept items. `--share` prints an anonymised summary you can paste anywhere. |
| `/leftovers` | (plugin) Shows the report inside Claude Code and explains items. Never deletes. |

## Privacy

No telemetry. dustpan makes no network connections, ever. Everything stays on your machine in `~/.local/state/dustpan/` (or `$XDG_STATE_HOME/dustpan/`), created private to your user (`0700` folders, `0600` files).

## Known limits

- macOS and Linux only.
- On the first run, receipts are rebuilt from existing Claude Code transcripts. Leftovers whose transcripts are already gone can't be attributed, so they aren't offered.
- Worktrees an agent created with a relative path that goes up a level (`git worktree add ../wt`) aren't attributed yet.

## Uninstall

```
/plugin uninstall dustpan@dustpan
/plugin marketplace remove dustpan
rm "$(command -v dustpan)"
rm -r ~/.local/state/dustpan
```
