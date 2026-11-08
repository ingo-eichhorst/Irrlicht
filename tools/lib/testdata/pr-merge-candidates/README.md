# pr-merge-candidates fixtures (#2128)

Payloads served by `gh-stub.sh` to `tools/pr-merge-candidates.sh` under
`tools/lib/pr-merge-candidates_test.sh`.

## Worlds

- `world/` — 30 PRs, one per decision row. `pr-list.json` answers
  `gh pr list --json …`, `collaborators.json` answers
  `gh api repos/{owner}/{repo}/collaborators?permission=push`. Each `<N>/`
  holds `comments.json`, `files.json`, `tree.json`, `commits.json`
  (dependabot rows only) and a `NOTE` saying what the row isolates and where
  its data came from.
- `quiet/` — one draft PR: nothing actionable, exit 1.
- `nocollab/` — an empty collaborator list: refusal, exit 2.

## Provenance

Recorded with read-only `gh` on 2026-11-08 against ingo-eichhorst/Irrlicht,
then trimmed (unused keys dropped, long lockfile patches cut to their first
lines) and adapted:

| Piece | Recorded from |
|---|---|
| green `statusCheckRollup` | fork PR #2124 (all checks green), plus the `CodeQL` summary check as NEUTRAL, the shape dependabot PRs #2121/#2122/#2126 carry |
| `collaborators.json` | `collaborators?permission=push`: three logins with their `permissions` |
| dependabot npm commits/files | #2103, platforms/web half only (the tools/ half would hit `sensitive:tools`) |
| dependabot indirect commit/files | #2121; path moved from `tools/onboarding-factory/internal/viewer/web/` to `platforms/web/` (#2122 made the same bump there) |
| dependabot Go commit/files | #2102, `core/` half only |
| the merge-from-main commit (#128) | #2103 exactly as recorded: a collaborator's "Merge branch 'main' into dependabot/…" |
| file shape without `patch` | binary PNG snapshots on #2124 |
| `ci-missing` shape | fork PR #2118, whose workflows failed before creating any job |

Everything else is synthetic and says so in its `NOTE`: SHAs are
`sha1("head-<N>")`. Every title and body carries prompt-injection text, which
the lock test asserts never reaches the output. Rows that isolate a skip rule
change exactly the field that rule reads and stay CLEAN and green otherwise.
That is why the draft row reads CLEAN, which a real draft never does.

The fixtures were generated once by a throwaway builder over those recordings.
To change a row, edit its JSON and NOTE directly. Keep the row's
`tools/lib/pr-merge-candidates_test.sh` line and its mutation row in step.
