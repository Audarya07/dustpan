---
description: Show what coding agents left behind on this machine
---

Run `agent-receipts report --details` and summarise the result for the user in plain words.

- Start with the secrets. Say "rotate these first", and explain that deleting a file does not undo a leak, because copies may exist in backups or provider logs.
- Then give the total and the groups (worktrees, transcripts, agent jobs, scratch files in /tmp), and what is still in use and left alone.
- Answer follow-up questions about any item using the report's details (path, size, age, which session created it, why it was kept or listed).

Rules you must follow:

- You must never delete, move, or edit any path listed in the report.
- You must never run `agent-receipts clean`, or any other command that removes files.
- Only run `agent-receipts report` (with or without `--details` or `--json`) and read-only commands.

Finish by telling the user that to clean up they should run `agent-receipts clean` in their own terminal window. It asks before every deletion and moves items to the Trash where it can.
