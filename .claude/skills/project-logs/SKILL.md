---
name: project-logs
description: "kennel: where its logs live (the CLI's change log on each Mac, admin command output in dev) and the command that reads them by time. Read when kennel did something while it ran — a route that vanished, a link that appeared, a cd that got the wrong home; it reads the lines itself, never ask the user to paste them."
---

# kennel logs

kennel is a CLI that runs on the user's Macs only. It has no server and ships nothing off the
machine, so every line is read locally (over `ssh <host>` for another Mac).

## The CLI, on each Mac

- **Where:** `~/.local/state/kennel/kennel.log`, appended by `logf` in `main.go`.
- **What:** one line per change kennel makes on disk: route added, removed or expired, a
  profile home or symlink created, a share link created or removed. Each line starts with an
  RFC 3339 local time with its offset: `2026-11-12T00:02:00-05:00 route expired and dropped:
  ~/Projects/oot work 2026-11-11`. Reads (`env`, `which`, `doctor`, `share plan`) write nothing, so a wrong
  home after a `cd` leaves no line; reproduce it with `kennel which <dir>` instead.
- **Fetch a window:** `grep '^2026-11-12' ~/.local/state/kennel/kennel.log` for a day, or
  `tail -50` of the file for "just now". State the window in UTC beside the user's local time.
- **Retention:** kept forever; nothing rotates the file.

## Dev, through admin

`admin <command>` writes each run's output to `tmp/<command>~<command>.<timestamp>.log`
(`tmp/test~test.2026-10-20_180814_167518000.log`). Read the newest one for the command
(`ls -t tmp/test~*.log | head -1`), bottom 80 lines first for a failed run.

## From a line to a fix

A log line names the path and profile kennel acted on. Reproduce it in the throwaway `HOME`
of `project-verify`'s `tools/sandbox.sh`, then `diagnose`, then an `implement` pass.
