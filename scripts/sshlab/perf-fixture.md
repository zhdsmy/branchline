# SSH performance fixture protocol v1

This protocol defines a deterministic synthetic fixture for SSH performance
measurements, not a performance result. It creates synthetic linear history,
not a copy of git/git. No SSH, container, GUI, network, or benchmark operation
is performed by the generator.

## Local commands

Requires local Python 3.8+ and Git. No remote Python is required for `emit`.

```sh
python3 scripts/sshlab/perf-fixture.py create --commits 1000
python3 scripts/sshlab/perf-fixture.py create --commits 100000
python3 scripts/sshlab/perf-fixture.py check /tmp/branchline-ssh-perf-<created-suffix>
python3 scripts/sshlab/perf-fixture.py emit --commits 1000 > /tmp/perf-1000.fi
python3 scripts/sshlab/test-perf-fixture.py
```

`create` always allocates a fresh `/tmp/branchline-ssh-perf-*` directory, with
`repo/` and `manifest.json`. It prints one JSON object. `root` is the directory;
`root_commit` is the first commit OID. On macOS `/tmp` may resolve to
`/private/tmp`. There is no arbitrary create destination or cleanup/reset API.
On failure, an incomplete fixture is retained and its path is printed to stderr.
The owner removes only exact generated directories after retaining evidence.

`check` is read-only: it verifies the manifest format, exact history count,
single root/no merges, the complete refs set being exactly `refs/heads/main`
pointing to HEAD (including rejecting extra tags/remote refs), 1000 tracked paths, index equal to HEAD,
exactly 1000 unstaged modifications and no untracked entries, object integrity,
and recorded content/index/status digests. Mismatch exits 1; invalid CLI arguments
exit 2. It does not reset, repair or refresh the index. A manifest is a baseline
record, not an authenticity signature; independent Git assertions are still
required at measurement time.

## Data contract

- Format: `branchline.ssh-performance-fixture.v1`; scales exactly 1000 or 100000.
- SHA-1, one `refs/heads/main`, no merges, commits numbered 1 through N.
- Author/committer identity is `Branchline SSH Performance <perf@example.invalid>`;
  Git derives the author from the explicit committer. UTC commit timestamps are
  `1700000000 + number`. Messages are
  `SSH performance commit NNNNNN\n\nDeterministic fixture.\n`.
- Root creates 1000 files at `tracked/groupGG/fileIIII.txt`, where
  `GG = floor(index / 100)` and `IIII = index`, zero padded to 2/4 digits.
  Each subsequent commit `number` replaces file `(number - 2) % 1000`.
- Committed content: `file IIII version VVVVVV\n`. All root versions are 1.
  At HEAD, file i has version `2+i+1000*floor((N-2-i)/1000)` when N >= 2+i,
  otherwise 1. All modes are 100644.
- After checkout, append `working tree change\n` to every tracked file without
  staging. Thus both sizes have exactly 1000 unstaged modifications.
- Manifest records count, local Git version, fast-import stream SHA256, HEAD,
  root commit, the complete refs map, HEAD tree, index/status/worktree SHA256. Worktree digest is sorted
  path bytes and body bytes, each prefixed with its unsigned 8-byte big-endian
  length. Index/status digests use raw `ls-files --stage -z` and
  `status --porcelain=v1 -z --untracked-files=all` output.

The stream uses explicit main refs, marks, raw dates and inline blobs, without
`git init -b` or newer import features. It targets Git 2.25+. For Linux setup,
import `emit` into a new task-owned repository, select main, checkout and append
the dirty suffix using the contract above. Do not transfer the caller's Git
config, keys or hooks. Fixture preparation and independent checks are outside
all timing windows; benchmark setup owns its isolated remote paths and cleanup.
