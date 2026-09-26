---
name: devcleaner
description: Reclaim developer disk space on a Mac using this repository's CLI. Use for disk-usage scans, cleanup plans, removing old clean Git worktrees, clearing generated artifacts, or continuing a saved cleanup.
---

# Developer disk cleanup

Use the CLI to scan, save a concrete plan, and execute authorized cleanup. An inspection or planning request ends before deletion. This skill grants no deletion permission and cannot override an approval rejection.

## Prepare and scan

If the executable is missing, download the full CLI from [GitHub Releases](https://github.com/semistrict/devcleaner/releases/latest). This tool supports Apple Silicon (`arm64`) on macOS 12 or later. The full standalone executable is in `devcleaner_VERSION_darwin_arm64.tar.gz`. For app installation, use `DevCleaner_VERSION_arm64.dmg` when available and drag the app to Applications; the full CLI is also bundled at `/Applications/DevCleaner.app/Contents/MacOS/devcleaner`. Verify the selected download against `SHA256SUMS` and read `SIGNING.txt`. The initial v0.2.0 release was signed but unnotarized; prefer a later notarized release for installation. If only an unnotarized release is available, report that limitation instead of bypassing Gatekeeper.

When using a checkout, work from its root, read `./bin/devcleaner help` for current flags, and build with `make build` if needed. Otherwise use the installed executable in place of `./bin/devcleaner` throughout this skill. See [README.md](../../../README.md) for app setup, safety rules, and storage details.

Prefer `--app` so filesystem access belongs to the running menu-bar app. If unavailable, resolve that rather than silently switching to terminal permissions. Use `--standalone` when standalone execution is intended. Replacing an executable does not update its running process: after rebuilding, verify the app has restarted before relying on changed behavior. Preserve active scans and cleanup when arranging a restart.

Scan the **home directory by default**. Use explicit roots when requested or needed for targeted follow-up. Start follow-up cleanup with `status` and `list`: they read the persistent SQLite inventory and cleanup history without walking the disk. Default `plan` also uses this inventory. Do not rescan merely because the user asks whether more space can be freed. Keep the same `--db` selection across invocations.

Use `refresh --path PATH` for selected known candidates whose estimates or safety need updating; it rechecks only those paths and their Git metadata. Reserve `scan` for initial discovery, explicitly requested rediscovery, or locations outside the saved inventory. An empty partial scan does not replace earlier inventory. Cleanup marks deleted generations in SQLite and invalidates overlapping estimates; deleted metadata and savings history remain. A recreated path gets a new inventory identity, so never match cleanup history by path string alone.

Use `history` for cumulative estimated allocated bytes removed, not an assertion of actual free-space gains. For a requested fresh start, `reset --yes` retires current inventory and invalidates old plans while preserving disk files, deleted metadata, and cleanup history; then perform the requested discovery. Never reset just to simplify a follow-up.

When discovery is needed, use a bounded parallel scan including recognized tool caches:

```sh
run_dir=$(mktemp -d /tmp/devcleaner-run.XXXXXX)
df -k "$HOME" > "$run_dir/disk-before.txt"
./bin/devcleaner scan --app --caches --time-limit 2m > "$run_dir/scan.txt"
```

Adjust the budget as needed. Keep English output and stderr progress; capture verbose stdout because home scans can produce thousands of warnings. Read the full results yourself and summarize coverage gaps. Use `--refresh` when fresh measurements are needed; normal scans reuse cached measurements, and applying revalidates them.

When asked to make do with current results, run `stop --app` with the same database and wait for the scan to exit. Continue from completed measurements in its saved partial scan. Disclose partial coverage. Force-killing loses unsaved measurements.

## Select and inspect

Use the current inventory by default; `--scan ID` selects a historical snapshot only when that is intentional. For large inventories, read-only inspection of SQLite is acceptable; inspect active generations and exclude entries marked deleted, missing, superseded, or needing refresh. Create plans and mutate files through the CLI.

Start with the large Git-ignored findings in `status`, or `list --kind ignored --min-bytes 1073741824`. Surface substantial ignored data even when its name is outside the artifact rules or deletion is protected. Discovery priority is separate from deletion eligibility: explain what a large directory contains using project context instead of hiding it behind safety filters.

Also check `list --kind storage` for Docker, Lima, Colima, and lnx data outside Git repositories. These are observations, not direct-deletion candidates: VM disks and container volumes can contain persistent data. Use the owning tool to distinguish unused images/build cache, unused instances, and generated test fixtures. Never equate an entire VM disk’s allocated size with reclaimable space. `scan --caches` discovers these locations alongside tool caches; `refresh --path` can add a newly recognized cache/storage location directly without a home rescan. DaisyDisk’s saved results are useful discovery leads; obtain current targeted measurements before proposing savings.

For unspecified general cleanup, **7 days old and at least 100 MiB** are useful starting filters, largest first. Adjust to the user's scope. Age and size are not proof of disuse.

- Treat conventional Git-ignored build output (`target`, `dist`, `build`, framework output) as disposable generated data when its role is established by project context. The normal cost is rebuilding. Hypothetical manual edits are not a reason to demand inspection or defer cleanup.
- Keep build output, installed dependencies, and virtual environments separate in proposals. A CLI `review` label is a conservative classification, not evidence of valuable local changes. Resolve an ambiguous classification yourself using project/tool metadata; describe any actual exception precisely instead of saying “needs inspection.” Escalate concerns only for concrete evidence such as tracked source, user-created data, or a documented custom use of the directory. Unknown ignored data remains outside routine cleanup.
- Remove worktrees only when the CLI considers them removable: clean, named branch, unblocked, and completely measured. A local-only branch is sufficient; Git retains it. Keep dirty worktrees while considering their generated artifacts independently.
- Check active use: open paths inside a candidate and process working directories inside its owning repository are exclusion signals. On macOS, use `lsof -u "$USER" -Fn` and `lsof -a -u "$USER" -d cwd -Fn`. A shell in a broad parent such as home does not make every descendant repository active. Failed or incomplete inspection is uncertainty, not proof of inactivity.
- Compare canonical paths with path-component containment. Select an enclosing worktree or its artifacts, never both. Keep active workspaces, protected candidates, personal files, and agent history out of general cleanup.

Save a plan with explicit selected `--path` arguments and the chosen age/size filters against current inventory. Use the safety ceiling required by the selected items: `safe` for safe-classified artifacts, or `review` for eligible clean worktrees and identified build outputs that the registry classifies conservatively. Use exact paths so a higher ceiling does not broaden the selection. Pass paths as individual arguments, not interpolated shell code.

Inspect `show --plan ID --app` before execution. Confirm paths, actions, safety, totals, and exclusions. Estimates are not actual recovered bytes; shared blocks, hard links, and snapshots can reduce savings.

## Approve and execute

Every cleanup proposal must proactively answer “Is it safe?” before asking for approval or executing already authorized cleanup. Keep the scope and risk assessment to two or three short sentences: category counts, estimated space, a qualified risk judgment, the safeguards actually checked, and the main remaining risk or practical cost. Link the full saved plan when useful; do not dump hundreds of paths.

Base the assessment on the selected items and concrete evidence, not just CLI safety labels. Distinguish verified checks from checks that apply will perform. For ordinary build output, describe the practical cost of rebuilding; for dependencies and caches, describe reinstalling or downloading. Apply the exception criteria above rather than repeating speculative manual-change warnings. Explain branch retention and local-change protection when worktrees are selected, and report active-use problems or failed checks specifically. State that deletion is permanent. Moving to Trash does not reclaim disk space and is not this workflow.

Use existing session authorization where it covers the concrete plan. If approval is missing or a gate rejects execution, ask one short question about the selected scope and explain the stated reason. A request to shorten the explanation, create a skill, or change the UI is not deletion approval. Keep rejected plans pending; do not split them, change execution routes, or use direct deletion commands to bypass rejection.

Once authorized, apply the exact saved plan:

```sh
./bin/devcleaner apply --app --plan PLAN_ID --yes --allow-review > "$run_dir/apply.txt"
```

Include `--allow-review` only for approved review items. General cleanup does not use `--allow-unsafe`. The CLI rechecks identity, contents, age, and Git state before each action. Let changed candidates fail validation rather than bypassing checks.

If execution loses its connection, inspect the saved journal before retrying. A `running` item has an uncertain outcome: reconcile the filesystem and create a fresh plan as needed. Never automatically repeat an uncertain mutation.

## Verify and report

Read the final saved plan, confirm completed paths are gone and removed worktrees' branches remain, and record `df -k "$HOME"` again. Distinguish observed available-space change from estimated deleted bytes; other processes and snapshots affect it. Report failures, skipped items, and coverage limits briefly. Say nothing was deleted only when no mutation started; otherwise report the partial outcome.
