# DevCleaner

A Go CLI for reclaiming developer disk space, designed for agents that invoke a command, read the results, and invoke another command. No terminal UI or background daemon.

It inventories **Git worktrees and the generated artifacts inside each worktree independently**. Keep a working tree with valuable changes while clearing its `node_modules` or `target`, or remove an old clean worktree altogether. Saved plans never include both a directory and its contents.

## Download

Download the signed CLI and menu-bar app from [GitHub Releases](https://github.com/semistrict/devcleaner/releases/latest). Choose an archive matching your Mac: `arm64` is Apple Silicon and `amd64` is Intel. Only architectures present in the release assets are available. The CLI archive contains the full standalone executable; the app is optional.

Download `SHA256SUMS` and the archives into the same folder, then run `shasum -a 256 -c SHA256SUMS` after downloading all listed files. See `SIGNING.txt` for that release's signing and notarization status. Signed builds that have not been notarized may be blocked by macOS Gatekeeper on first launch.

## Build

Requires macOS, Go 1.25 or newer, Git, and the Xcode Command Line Tools (a C compiler is required for SQLite and the optional menu-bar app).

```sh
make build
./bin/devcleaner help
```

The executable is `bin/devcleaner`. Standalone use requires no runtime service. Neither the CLI nor the optional app sends scan data over the network. Git commands disable optional index writes, filesystem-monitor hooks, and repository hooks.

## Optional menu-bar app and macOS permissions

```sh
make app
open bin/DevCleaner.app
```

This builds a small [Wails v3](https://v3.wails.io/features/menus/systray/) menu-bar wrapper using native menus and dialogs. There is no separate web frontend, bundled browser, Swift bridge, or C archive. The app links the same Go command and scanner packages directly into its own executable. Wails v3 is currently beta; the dependency is pinned to `v3.0.0-beta.26`, which includes the [macOS 27 tray-click fix](https://github.com/wailsapp/wails/pull/5919).

The **DevCleaner** menu can scan your home directory, choose another folder, stop scanning while keeping completed measurements, show the last scan result, reveal the app in Finder, and open disk-access settings. It never automatically applies a cleanup plan. Quitting stops ongoing work and waits for it to finish saving before the app exits.

To attach disk permissions to the app rather than iTerm:

1. Put `DevCleaner.app` in a stable location and open it through Finder or `open`, not by executing its inner binary from a shell.
2. If your chosen folders require it, add **DevCleaner.app** in **System Settings → Privacy & Security → Full Disk Access**. The menu's **Disk Access Settings…** and **Reveal DevCleaner.app** actions help with this. Quit and reopen the app after enabling access. The app does not change privacy settings itself or claim to have access before you grant it.
3. Run CLI commands with `--app` to require execution in the running app:

```sh
./bin/devcleaner scan --app
./bin/devcleaner stop --app
./bin/devcleaner plan --app --kind worktree --older-than 7d
./bin/devcleaner apply --app --plan PLAN_ID --yes --allow-review
```

`--app` fails if the app is unavailable; it never silently switches to terminal permissions. Without either routing flag, the CLI uses the app when it is running and otherwise behaves as the standalone CLI. `--standalone` explicitly runs in the terminal process. Relative paths and `DEVCLEANER_DB` are resolved by the CLI before forwarding so the app uses the caller's intended workspace and state database. Progress, English output, exit codes, and Ctrl-C cancellation are carried back to the CLI. Losing the connection cancels the request, saves partial scan work where possible, and never retries a potentially destructive request locally.

Communication uses a private, current-user Unix socket, not a TCP port. The CLI remains a separate lightweight executable; scanning, Git subprocesses, and deletion operations run inside the app's process tree when delegated. This follows macOS's [responsible-process permission model](https://developer.apple.com/forums/thread/125438); protected-directory access must still be verified after the user grants permission.

The app icon source is `packaging/icons/app-icon.png`. `scripts/generate-icons.sh` builds the macOS icon sizes and the monochrome menu-bar template from `scripts/generate-tray-icon.swift`; `make app` runs it automatically.

The bundle identifier is `dev.devcleaner.app`. Local builds are ad-hoc signed. Rebuilding or moving such a build may require granting permission again. For a consistent distributed signing identity, set `DEVCLEANER_SIGN_IDENTITY` to your own Developer ID identity when running `make app`. Signing credentials remain in your local keychain. Release signing and optional notarization are described below.

## Agent workflow

```sh
# Scan your home directory by default and follow registered worktrees,
# including linked worktrees outside the root. Repeat --root for other folders.
./bin/devcleaner scan --workers 8

# Read the saved inventory; neither command rescans the disk.
./bin/devcleaner status
./bin/devcleaner list --kind worktree --older-than 7d
./bin/devcleaner list --kind ignored --limit 100 --offset 0

# Option A: keep all working trees; clear recognized generated artifacts.
./bin/devcleaner plan --kind ignored --safety safe --older-than 7d

# Option B: remove clean working trees older than a configurable threshold.
./bin/devcleaner plan --kind worktree --older-than 7d

# Option C: a mixed plan; prefer removing an eligible old worktree, otherwise
# consider its artifacts individually. Largest candidates come first.
./bin/devcleaner plan --older-than 30d --min-bytes 104857600 --limit 20

# Inspect the returned plan ID, then explicitly execute that exact plan.
./bin/devcleaner show --plan PLAN_ID
./bin/devcleaner apply --plan PLAN_ID --yes --allow-review
```

`plan` only writes a plan to the state database. `apply` without `--yes` returns that plan with an error and makes no filesystem changes. There are no interactive prompts. An agent should obtain authorization for the specific plan under its own approval policy before passing execution flags.

Select exact candidates with repeatable `--path /absolute/candidate/path`. An absent, protected, overlapping, or filtered-out requested path produces an error, rather than silently broadening the selection. Use `--scan ID` to plan or list from a specific saved snapshot. Run `help` for all flags.

## Stop scanning and use what is ready

Press **Ctrl-C** during a scan, or have another invocation request a graceful stop:

```sh
./bin/devcleaner stop
# If the scan uses a custom database, use the same --db setting here.
./bin/devcleaner stop --db /path/to/state.db
```

The active scan stops its workers and Git commands, saves completed measurements, and exits successfully with an explicitly **partial scan**. Wait for that invocation to exit, then use `status`, `list`, and `plan` normally. Stopping a scan never removes files. The `stop` command targets the database's current scan through a private local socket, without waiting for the scan's database lock.

You can also give a scan a time budget:

```sh
./bin/devcleaner scan --time-limit 30s
./bin/devcleaner plan --kind ignored --safety safe --older-than 7d
```

Completed artifacts are kept even when traversal of their enclosing worktree is unfinished. Incomplete directories are excluded from cleanup candidates and savings totals. A very early stop may yield an empty partial scan, especially during repository discovery. Accepted cached measurements remain marked as cached. Plans disclose partial coverage and still revalidate every selected item before cleanup.

The partial scan is saved as a historical snapshot and its completed measurements merge into the SQLite inventory. An empty partial scan does not erase earlier inventory; prior snapshots remain available by ID. The next scan reuses completed measurements under the normal cache policy and discovers what remains. This is not an exact traversal-position resume. Force-killing the process cannot save its in-memory results; use `stop`, Ctrl-C, or `--time-limit` for graceful stopping. The time limit stops new work and cancels active work, but saving the partial snapshot can take additional time.

## Safety levels

`--safety` on `plan` is a ceiling: `safe`, `review` (the default), or `unsafe`.

| Level | Examples | Execution approval |
| --- | --- | --- |
| `safe` | Recognized generated artifacts and disposable tool caches | `--yes` |
| `review` | Clean named worktrees, virtual environments, local package repositories, ambiguous build outputs | `--yes --allow-review` |
| `unsafe` | Unrecognized ignored files, including local configuration and databases | `--yes --allow-unsafe` |

`--allow-unsafe` also acknowledges review items. An unsafe plan can propose permanently deleting unrecognized ignored data, but **never overrides worktree protection**. Dirty, locked, detached, submodule, or incomplete worktrees are listed with `safety: unsafe` and a `blocked_reason`; they cannot enter any executable plan. Symbolic links and nested repositories are also protected.

A removable worktree must have:

- A named branch. A local-only branch is sufficient; no remote, upstream, merge, or push is required. The branch is retained after removal.
- No staged, unstaged, or untracked files. Index entries marked assume-unchanged or skip-worktree block removal too.
- Only recognized safe generated artifacts among its ignored files. Other ignored data protects the worktree, even though ordinary `git status` hides it.
- No Git lock, detached HEAD, submodule configuration, nested repository, cross-volume mount, or incomplete measurement.

The main working copy is never a worktree-removal candidate. Its ignored artifacts can still be cleaned.

Before each action, apply checks identity, a complete metadata fingerprint, the age threshold, current Git ignore/tracking state, and current worktree safety. Changed items fail individually, with details persisted in the plan. Git removal uses `git worktree remove` without force. A crash or uncertain mutation leaves `status: running`, requiring manual inspection and a new scan/plan rather than an automatic retry. Completed items are not executed again.

Stop agents, builds, package managers, and watchers using the selected paths before applying. The CLI cannot freeze other processes: a filesystem change can race the final checks. "Safe" describes rebuildability under conventional tool use, not proof that an active process no longer needs a directory. Nothing can detect arbitrary hand-edits inside otherwise standard generated output.

## Language artifacts and caches

Run `devcleaner rules` for the machine-readable registry.

| Ecosystem | Project artifacts recognized |
| --- | --- |
| JavaScript / TypeScript | `node_modules`, `.next`, `.nuxt`, `.turbo`, `.parcel-cache`, `.svelte-kit`, `.angular`; `dist`/`build` require review |
| Python | `__pycache__`, pytest/mypy/ruff/Hypothesis caches; virtual environments, tox, and nox require review |
| Rust | `target` with `Cargo.toml` |
| Swift | `.build` with `Package.swift` |
| Maven / Gradle | Maven `target`; Gradle `build` and `.gradle`, with project markers |
| .NET | `bin` and `obj` with a C#, F#, or VB project file |
| Dart / Flutter | `.dart_tool` and `build` with `pubspec.yaml` |
| Elixir | `_build`, `deps`, `.elixir_ls` with `mix.exs` |
| Zig | `.zig-cache`, `zig-cache`, `zig-out` with `build.zig` |
| Ruby | `vendor/bundle` with `Gemfile`, requiring review |
| C / C++ | `CMakeFiles` |

Project candidates must be **ignored by Git**. Tracked outputs, arbitrary directories with similar names, Git object databases, agent conversation history, and personal folders are not inferred to be disposable. Unknown ignored paths remain visible at the unsafe level. Git may coalesce a wholly ignored parent directory into one candidate; the CLI conservatively requires review rather than treating unknown containers as generated output.

Add `scan --caches` to include conventional macOS cache locations for Xcode, Homebrew, pip, uv, Go, pnpm, Yarn, JavaScript packages, Gradle, Maven, Rust, Dart, Lima downloads, Bazel/Bazelisk, Playwright, Cypress, Electron, CocoaPods, Dotslash, gopls, TinyGo, and Zig. uv is recognized under both `Library/Caches/uv` and `.cache/uv`. Local module/package repositories use the review level because they can include locally published packages. Custom tool cache locations are not automatically discovered. No tool-managed garbage collectors or package-manager commands run.

## Persistence, parallelism, and estimates

The SQLite database defaults to `~/Library/Application Support/devcleaner/state.db`. Override it with `--db PATH` on any command or `DEVCLEANER_DB`. State files use private permissions. One invocation holds a database lock at a time; other invocations fail clearly rather than racing cleanup plans. SQLite stores current inventory, historical scan snapshots, reusable measurements, saved plans, and per-item execution journals. `status`, `list`, and `plan` use the current inventory by default without walking the filesystem. Completed cleanup and inventory updates commit in the same transaction. Deleted entries retain metadata; a recreated directory receives a new inventory identity even when its path or inode is reused. Overlapping estimates invalidated by cleanup require targeted refresh before planning.

```sh
./bin/devcleaner status
./bin/devcleaner list --older-than 7d
./bin/devcleaner refresh --path /path/to/repo/target
./bin/devcleaner history
./bin/devcleaner reset --yes
```

`refresh` measures only explicitly selected paths and checks Git metadata for repository candidates. Newly recognized cache and managed-storage locations can be added directly with `refresh --path` without a home scan. It supports bounded workers, cancellation, and `--time-limit`. `scan` remains explicit discovery for new locations. `status` and scan results prominently flag the five largest Git-ignored items of at least 1 GiB, including unclassified and protected directories. Use `list --kind ignored --min-bytes 1073741824` for all large ignored data. The artifact registry governs deletion classification, not discovery visibility. Nested Git data is included in measured size while its deletion protection remains.

Inventory is a saved estimate, not a filesystem watcher: external changes become known when refreshed or discovered, and apply still revalidates immediately before deletion.

`history` reports cumulative estimated allocated bytes removed by successful actions, without double-counting nested inventory entries. This can differ from actual free-space gains. `reset --yes` retires current inventory, clears its disposable measurement cache, and invalidates old plans. It preserves disk files, historical metadata, and cleanup history; run `scan` afterward to start over. Existing database formats are not migrated; replace an unsupported old database to start over.

`scan` defaults to your home directory. Use `--root PATH` to choose a different location. It uses a bounded worker pool (default up to 8, configurable from 1–32) for repository discovery, Git inspection, and filesystem measurement. Worktrees and their artifacts share a traversal instead of reading each large artifact twice. File traversal never follows symbolic links. Discovery stops at repository boundaries, follows registered linked worktrees, and skips common generated directories and most hidden folders. Use `--root` for otherwise undiscovered hidden directories or unregistered nested repositories. The default discovery depth is 8; truncation and access errors appear as warnings.

Measurements are reused for **24 hours** by default. Git inventories and worktree safety are still read on every scan. The per-item `cached` and `measured_at` fields expose reuse. Directory modification time alone cannot detect deep file changes, so cache reuse is explicitly an estimate—not a freshness guarantee. Use `refresh --path PATH` to update selected candidates; `scan --refresh` or `--cache-ttl 0h` explicitly remeasures during discovery. Listing and planning use stored measurements only; applying always remeasures. Gracefully stopped scans save an explicitly partial snapshot of completed measurements.

Parallelism currently spans independent repositories and measurement groups. A single large worktree is still traversed serially within its group. In a local fixture benchmark with 16,000 files and a warm OS metadata cache (median of three fresh scans), eight worktrees took 498 ms with one worker and 123 ms with eight workers. One worktree took about 173 ms regardless of worker count. These are fixture results, not cold-disk or whole-machine guarantees; further optimization should target traversal within a large tree and redundant Git queries before increasing worker counts.

`--older-than` accepts days (`7d`, `0.5d`) or Go durations (`48h`, `30m`). Age is based on the **newest filesystem modification anywhere inside a candidate**, including its root directory. It is not last access, last agent use, branch commit age, or worktree creation time. A recently modified source file protects an entire worktree from an age-filtered plan while older artifacts inside it may still qualify.

Sizes use allocated disk blocks, not logical file lengths. Hard links are deduplicated within a candidate; APFS clones and links shared across candidates can make actual savings smaller. Inventory items may overlap, but inventory summary totals and all plans eliminate parent/child double counting.

**Cleanup permanently deletes selected artifacts and caches to reclaim space.** Worktree removal permanently removes its directory through Git and leaves its branch. Nothing is moved to Trash. Plans state these actions explicitly and expire after 24 hours. Saved plans from older builds that specified Trash are rejected; create a new plan to review permanent deletion. Existing scan measurements can be reused.

State history has no automatic retention policy. Use `reset --yes` to start over while retaining history.

## Output, progress, and exit codes

Commands explain results in English: readable sizes, safety levels, reasons an item is protected, cleanup actions, and follow-up commands. Plans state how much space can be reclaimed and explicitly describe permanent deletion and worktree removal.

Every operational command shows an initial activity line on stderr, followed by an update every second until it finishes. Scan progress names the current phase and shows completed/total work where known; apply distinguishes validation from cleanup. Within a large folder, measurement and artifact deletion show its path, file count, and allocated-byte estimate. During parallel measurement, the path identifies the worker whose update is shown; those file and byte counts belong to that folder. Cleanup compares these counts with the saved measurement. Bytes removed are estimates, not measured free-space gains. Git worktree removal shows its path and elapsed time because Git does not expose per-file deletion progress. Counts describe the current phase, not an invented whole-scan percentage. These are ordinary log lines, with no terminal UI or cursor controls, so progress is also visible in agent logs. Use `--quiet` to suppress progress.

Use `--json` only when structured output is useful. This opt-in format emits one JSON object on stdout; progress stays on stderr:

```json
{
  "schema_version": 1,
  "command": "plan",
  "data": {
    "id": "…",
    "scan_id": "…",
    "maximum_safety": "review",
    "estimated_reclaimable_bytes": 123456,
    "bytes_deleted_directly": 123456,
    "bytes_removed_by_git": 0,
    "items": []
  }
}
```

In JSON mode, errors include `error.code` and `error.message`, and partial apply failures return the updated plan in `data`. In the default English output, each failed item includes its reason and saved status. Exit codes: **0** success, **1** operation or partial failure, **2** invalid arguments. `list` shows how many candidates match and the offset to request the next page; results are sorted by allocated bytes descending. Paths are quoted with control characters escaped. Treat paths as data and pass them as separate arguments, never as executable shell text.

## Validation

```sh
make test                 # race detector and isolated Git/SQLite fixtures
make check                # Go vet
go test github.com/wailsapp/wails/v3/pkg/application -run TestCoerceStatusItemEventType_MacOS27Regression
```

Tests create disposable repositories, worktrees, caches, and databases in temporary directories. Cleanup tests permanently delete only their own disposable artifacts and verify that dirty source files and retained branches remain intact.

### Managed storage

`scan --caches` also inventories Docker, Lima, Colima, and lnx storage. These findings are visible in `status` and `list --kind storage`, but cannot enter deletion plans, even with unsafe permission. Allocated VM disk size is not an estimate of reclaimable space: inspect unused images, build cache, instances, or test fixtures through the owning tool. Persistent container volumes and VM filesystems are not disposable caches.

For example, `refresh --path "$HOME/.lima/default" --path "$HOME/Library/Caches/lima"` adds or updates just those locations in SQLite, with progress and cancellation. Symbolic-link targets remain protected.

## Local signed releases

All compilation, signing, and optional notarization run on your Mac. GitHub hosts the source and finished downloads; no hosted build workflow is required.

```sh
# Commit the release sources first.
make release
# Push that commit, then upload the local archives:
make release-upload
```

`make release` requires a clean committed checkout and records `SOURCE_COMMIT` with the archives. It uses the sole available Developer ID Application identity, or `DEVCLEANER_SIGN_IDENTITY` when set. It builds the host architecture only, signs the CLI and app with hardened runtime and a secure timestamp, verifies signatures, and writes archives plus SHA-256 checksums under `dist/vVERSION/darwin-ARCH`. It does not alter the running development app. The CLI and bundle versions must match.

To notarize, set `DEVCLEANER_NOTARY_PROFILE` to an existing `notarytool` keychain profile before `make release`. The script submits both binaries to Apple and staples the app ticket. Without that profile, it explicitly marks the assets as signed but not notarized. Credentials are never stored in this repository or uploaded to GitHub.

`make release-upload` verifies checksums and a clean checkout matching GitHub's `main`, then creates a release with the local assets. Existing releases are never overwritten. Override `DEVCLEANER_GITHUB_REPO` for a fork.

## License

[MIT](LICENSE), copyright 2026 Ramon Nogueira.
