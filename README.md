# undo

**Undo anything your coding agent does to your project.**

`undo` takes a checkpoint of your project before every command your coding agent runs. When a command goes wrong — `rm -rf` in the wrong folder, `git reset --hard`, a `sed -i` across the codebase, a deleted `.git` — one command puts everything back.

```
$ undo
==> Checkpoint 41 · 12 seconds ago
    before rm -rf src .git

  restore  .git/  2,341 paths
  restore  src/   128 paths

Restore 2,469 paths? [y/N] y
✔ Restored 2,469 paths. Run undo to put them back.
```

It works with **Claude Code, Codex, Cursor, Gemini CLI and OpenCode**, and with any other tool through `undo run`. Checkpoints are copy-on-write clones, so they take about 15 ms per command and almost no disk space.

---

## Why

Coding agents run shell commands on your real machine, and sometimes those commands destroy work.

- **Built-in rewind only sees the agent's own file edits.** Claude Code's `/rewind`, Codex undo and Cursor checkpoints track edits made through their file tools. Changes made through the shell — `rm`, `mv`, `sed -i`, `git reset`, build scripts — are not tracked, and agents do more and more through the shell.
- **Command blockers can't predict every mistake.** Deny lists catch the commands someone thought of in advance and still miss the new ones.
- **Git is not a backup of your working tree.** It skips ignored and untracked files, and it can't bring back a deleted `.git`.

`undo` doesn't try to guess which commands are dangerous. It makes every command reversible.

## Install

**Homebrew** (macOS)

```sh
brew install shimlabs/tap/undo
```

**Go** (1.26 or newer; on macOS, the Xcode Command Line Tools)

```sh
go install github.com/shimlabs/undo/cmd/undo@latest
```

**Binaries** for macOS and Linux, on Intel and ARM, are on the [releases page](https://github.com/shimlabs/undo/releases).

## Set it up for your agent

```sh
undo install claude     # Claude Code
undo install codex      # Codex
undo install cursor     # Cursor
undo install gemini     # Gemini CLI
undo install opencode   # OpenCode
```

That's it. From now on, every shell command and file edit the agent makes is preceded by a checkpoint. A few agents need one more step:

| Agent | After `undo install` |
|---|---|
| Claude Code | Nothing. Checkpoints start with the next tool call. |
| Codex | Open Codex, run `/hooks` and trust the undo hook. |
| Cursor | Restart agent sessions that are already running. |
| Gemini CLI | Nothing. Gemini runs hooks only in folders you trust. |
| OpenCode | Restart OpenCode to load the plugin. |

Not using one of these? Wrap any command:

```sh
undo run -- ./scripts/migrate.sh
```

## Use it

```sh
undo              # restore the state before the last command that changed something
undo list         # show checkpoints and the command that followed each one
undo diff 38      # show what restoring checkpoint 38 would change
undo to 38        # restore the state at checkpoint 38
undo status       # show what undo protects, and how much disk it uses
```

`undo` skips commands that changed nothing, such as `ls` or `git status`, and asks before it changes anything; pass `--yes` to skip the question. **Every restore can itself be undone**: before restoring, `undo` takes a checkpoint of how things are now.

```
$ undo list
==> Checkpoints in ~/code/app
  38  4 minutes ago   npm run build
  39  3 minutes ago   Edit src/server.ts  (Claude Code)
  40  1 minute ago    sed -i 's/userId/accountId/g' src/**/*.ts  (Codex)
  41  12 seconds ago  rm -rf src .git  (Claude Code)
```

Deleted the whole project folder? Run `undo` in its parent folder, and it tells you how to bring it back.

## How it works

1. **The first checkpoint clones the whole project** into a store. On APFS (macOS) and on Btrfs or XFS (Linux), a clone shares its data with the original, so it takes almost no space and about two seconds for 100,000 files.
2. **A small background process watches the project** with FSEvents or inotify, and keeps a list of the paths that changed. It starts by itself, uses no CPU while you are idle, and exits after an hour without commands.
3. **Each later checkpoint clones only the files that changed** since the one before. Its cost depends on what changed, not on the size of the project.
4. **A restore** works out the state at the checkpoint from the base clone and the layers on top of it, and writes back only the paths that differ.

Old checkpoints are merged away after 7 days, past 2,000 checkpoints, or when the store's own data passes 5 GB. Everything stays on your machine: `undo` makes no network connections.

The store lives in `~/Library/Application Support/undo` on macOS and in `~/.local/state/undo` on Linux. When a project is on a different disk, clones can't cross disks, so its store goes into a hidden `.undo-…` folder next to the project.

## Requirements

- **macOS** with APFS, or **Linux** with Btrfs or XFS.
- Other filesystems, such as ext4, FAT and exFAT drives, or network drives, can't make clones. `undo` explains this and offers copy mode: run `undo checkpoint --copy` once, and the store keeps full copies instead, extended attributes such as Finder tags included. Copy mode takes as much disk as the project, and everything else works the same.
- Windows is not supported.

## Remove it

```sh
undo uninstall claude    # or codex, cursor, gemini, opencode
```

To delete the checkpoints too, remove the store folder that `undo status` shows.

## FAQ

**Does it slow my agent down?**
About 15 ms per command, most of it starting the process. The first checkpoint of a large project takes a few seconds, once.

**How much disk does it use?**
Clones share data with your project, so a checkpoint costs almost nothing when it is taken. Disk use grows only as the project's files are rewritten, because the store then holds the only copy of the old content. `undo status` shows the real number, and old checkpoints are merged away automatically.

**Is it a replacement for git?**
No. Checkpoints are a short-lived safety net for the last few hours or days. Keep committing.

**What happens if two agents work in the same project?**
Both are protected. Every checkpoint records the command and the agent session it came from, and `undo list` shows them in order.

**Can a restore make things worse?**
A restore first checkpoints the current state, so running `undo` again puts everything back the way it was before the restore.

## License

[MIT](LICENSE)
