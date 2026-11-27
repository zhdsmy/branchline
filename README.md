# Branchline

Branchline for Git is a macOS Git client written in Go with [MyGo](https://github.com/egoist/mygo). It works on repositories that live on SSH servers directly. Git runs on the server through your own `ssh`, so you don't clone the repository first and you don't install anything on the server besides Git.

> **Alpha.** `v0.2.0-alpha.1` is the first public build. It has been tested on one Mac and a few Linux servers. Please report problems in [Issues](https://github.com/zhdsmy/branchline/issues).

## Features

### Repositories and SSH

- Single window with the **Repository Switcher** for local and SSH repositories (**Add Local Repository…**, **Add SSH Repository…**), plus **Back**/**Forward** navigation (`⌘[` / `⌘]`).
- **Test Connection** checks an SSH address, optional identity file, and server Git version before adding. Unknown or changed server host keys are never auto-accepted; **Copy SSH Command** and **Retry** guide verification in Terminal.
- Toolbar **Fetch**, **Pull**, and **Push** with an in-memory credential dialog for SSH passwords, key passphrases, and remote login prompts. Answers are used for one command only and are never saved.
- Checks the Git version when a repository opens (locally or on the SSH server), warns when Git is older than 2.25, and disables options that need a newer Git.

### History and commit actions

- **History** shows the commit graph, branch/remote/tag/`HEAD` badges, commit metadata, changed files, and **Load More** pagination (loads 500 commits initially locally or 1,000 over SSH).
- Shared file diff viewer with **Split** and **Unified** layouts, line counts, and a **`-w`** ignore-whitespace toggle.
- **Search Commits** (**Search** or `⌘F`) searches by **Message**, **Author**, **Hash**, **Content**, or **File**, with **Match case**, **Regex**, **Current branch**, **Added or removed**, path filtering (**In path**), **More Results**, and **Show in History**.
- Right-click any changed file in **History** or tracked file in **Local Changes** for **File History…** (per-commit diffs with **Load More File History**) and line-by-line **Blame…**.
- Right-click a commit in **History** to **Checkout Commit…** (detached `HEAD`), **Reset '\<branch\>' to Here…** (**Soft**, **Mixed**, **Hard** with explicit confirmation, or **Keep**), **Cherry-pick Commit…** (with **Commit the changes**, **Append origin commit SHA (-x)**, and **Mainline parent** for merge commits), **Revert Commit…**, **Copy Commit SHA**, or **Copy Commit Info**.

### Local Changes, stashes, and conflicts

- **Local Changes** (**Changes** in the sidebar) lists **Unstaged** and **Staged** files with **Stage**, **Stage All**, **Unstage**, **Unstage All**, **Discard**, and **Discard All…** (with a confirmation before discarding changes or deleting untracked files).
- Commit box with **Amend**, live commit-message length warnings, optional `Signed-off-by`, and `⌘↩` to **Commit**.
- **Stashes** in the sidebar: **Save Stash** (optional message and **Include untracked files**), **Apply…** (with **Restore staged changes (--index)** and **Delete stash after applying**), **Pop**, and **Drop…** with confirmation.
- **Conflicted** section lists unmerged files with conflict types and quick actions (**Use Ours**, **Use Theirs**, **Accept Deletion**, and **Mark Resolved**). Selecting a conflicted file opens the built-in conflict inspector with **Overview** (side-by-side **Ours** and **Theirs**) and **Merge Hunks** (**Open Hunk Resolver**) for hunk-by-hunk selection (**Previous**, **Next**, **All Ours**, **All Theirs**, **Column layout**), direct editing of **Result**, and **Save & Mark Resolved**.
- Operation-in-progress banner across **Changes** and **History** during a merge, rebase, cherry-pick, or revert, with **Continue**, **Skip** (where Git supports it), and **Abort**.

### Branches, remotes, and tags

- **Branches**: **New Branch** in the sidebar (or **New Branch…** on the **Branches** header, a branch, a remote branch, or a tag) with **Checkout after create**, double-click to check out, and **Delete…** with a merged check and **Force Delete** confirmation for unmerged branches.
- Local branch context menu: **Rename…**, **Copy Branch Name**, **Fast-Forward to '\<upstream\>'**, **Tracking** (**Remove tracking reference** or pick a remote branch), **Pull '\<upstream\>'**, and **Push to '\<upstream\>'…** (or **Push to**).
- **Remotes**: **New Remote** (or **Add New Remote…** on the **Remotes** header) with **Fetch after adding**; right-click a remote for **Fetch '\<remote\>'**, **Edit '\<remote\>'…**, **Delete '\<remote\>'…**, and **New Remote…**; right-click a remote branch for **Checkout…**, **Delete '\<remote/branch\>'…**, **New Branch…**, **New Tag…**, and **Copy Branch Name**.
- Hosted repository links for GitHub, GitLab, Bitbucket, and Gitea (including Codeberg): **View on '\<service\>'** on remotes and remote branches, and **Create Pull Request on '\<service\>'** or **Push and Create Pull Request…** on local branches.
- **Tags**: **New Tag** (lightweight or annotated, with optional **Push to remote**) and **Push All Tags…**; right-click a tag for **Checkout Commit…**, **Show Tag Details…**, **Push…** (with guarded **Force Push Tag** confirmation), **Delete…**, **Delete from Remote…**, **New Branch…**, **New Tag…**, and **Copy Tag Name**.

### Merge and rebase

- **Merge into '\<branch\>'…** from local branches, remote branches, tags, and commits: **Fast-forward if possible**, **No Fast-Forward**, **Squash**, and **Don't Commit**, with conflict prediction when supported by Git.
- **Rebase '\<branch\>' on '\<target\>'…** (from branches, remote branches, and tags) and **Rebase '\<branch\>' to Here…** (from commits): **Stash and reapply local changes**, **Update dependent branches** (Git 2.38+), and conflict prediction.
- **Interactive Rebase** (**Interactively Rebase '\<branch\>' to Here…** on commits, **Interactively Rebase on '\<target\>'…** on branches, remote branches, and tags): reorder commits, choose **Pick**, **Reword**, **Edit**, **Squash**, **Fixup**, or **Drop**, edit full multi-line commit messages via **Edit Message**, and optionally **Create backup branch** or **Update dependent branches**.
- **Quick Rebase Actions** submenu on commits in **History** for single-commit rewrites: **Reword Message…**, **Edit…**, **Squash into Parent…**, **Fixup into Parent…**, and **Drop…**.

### Settings

- Open **Branchline → Settings…** (`⌘,`) for four tabs:
  - **General**: **Appearance** (**System**, **Light**, **Dark**), **Commit Order** (**Topologically** or **By date**), and **Fetch all tags**.
  - **Diff**: **Font Size** (applied to diffs and **Blame**), **Tab Width** (`2`, `4`, `8`), **Show +/- change marks**, and **Ignore whitespace changes**.
  - **Commit**: **Subject Soft Limit**, **Subject Hard Limit**, **Page Guide** (**Page guide at column**), and **Add Signed-off-by to commits**.
  - **Git**: **Global User Information** (**User Name**, **User Email**), **Git Toolchain** (**Git Instance** with detected binaries or **Custom…**, and **ENV PATH**: **Process + Homebrew**, **Process PATH**, or **Login shell** for local repositories; SSH repositories keep using Git on the server), and **Verbose Git Trace** (**Enable verbose Git output**).

## Known limitations

- **Whole-file staging only:** **Local Changes** stages, unstages, and discards whole files. Hunk- and line-level staging are not implemented yet.
- **Inline diff and conflict limits:** Diffs and conflicted files larger than 2 MB, binary files, and submodule conflicts are not rendered inline (whole-side conflict actions and **Mark Resolved** still work).
- **Git version gates:** Core features require Git 2.25 or later (local or on the SSH server). **Update dependent branches** (`--update-refs`) and merge/rebase conflict prediction require Git 2.38 or later.
- **Not yet implemented:** Multi-select actions, Git submodules, worktrees, Git LFS, Git Flow, automatic background fetch, and in-app updates are not in this alpha.
- **SSH host keys and credentials:** New or changed SSH host keys must be verified in Terminal first, and passwords/passphrases are never stored.

## Requirements

- A Mac with Apple silicon. Alpha builds are tested on macOS 27 only.
- Git on the Mac. Branchline looks in `/opt/homebrew/bin`, `/usr/local/bin` and `/usr/bin`, so Homebrew Git or the Xcode Command Line Tools both work.
- For SSH repositories: an `ssh` setup that can reach the server, and Git on the server. Branchline uses your `~/.ssh/config`, keys and ssh-agent as they are. The server needs Git 2.25 or later. Tests run on Ubuntu 20.04, Ubuntu 24.04 and Alpine Linux.

## Install

1. Download the zip from [Releases](https://github.com/zhdsmy/branchline/releases), unzip it, and move `Branchline.app` to `/Applications`.
2. Open Branchline. The app is signed with a self-signed certificate and is not notarized by Apple, so macOS blocks the first launch.
3. Open System Settings → Privacy & Security, find the message about Branchline, and click **Open Anyway**. You only do this once.

## SSH addresses

Branchline accepts `user@host:/path`, `host:relative/path` and `ssh://user@host:port/path`. Add a server key to `known_hosts` in Terminal before you connect to a new server: Branchline does not accept unknown host keys for you.

## Build from source

You need macOS and Go 1.27.3 or later.

```sh
go build ./...
go tool mygo vet .
go test ./...
```

`sh scripts/macos.sh build` builds a signed `Branchline.app`. It needs a code-signing identity in your keychain and never falls back to ad-hoc signing. See [docs/macos-development-signing.md](docs/macos-development-signing.md).

## License

[MIT](LICENSE). Branchline depends on MyGo (MIT) and other Go modules listed in `go.mod`, each under its own license. The release zip includes their license notices.
