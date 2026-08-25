# CI Gates — What Runs, Why It's Trustworthy, and How to Reproduce It Locally

Referenced from [AGENTS.md](../AGENTS.md)'s Testing section. This file
carries the detailed write-up for every gate that isn't a permission/hook
contract family (those live in
[testing-contracts.md](testing-contracts.md)) or a replay/Swift-specific
suite (those live in [replay-testing.md](replay-testing.md) and
[swift-testing.md](swift-testing.md)): the core `go test` + architecture +
ARS score + CodeScene gates; the skill-file, POSIX-shell, and bash linters and
the recording-driver teardown tripwire and the sourced-shell-library and
shell-lib-suite-runner tripwires; the
extract-and-execute harnesses over the ARS badge job, the two other gist
badge jobs, the replaydata deletion guard, the Swift snapshot-evidence copy
step, and the Swift test-harness source step; the "which bash a workflow step
gets" derivation; the web test suites; and `tools/preflight.sh`'s full Local
CI parity section (chunking, the budget, and what it makes visible).

- Unit + e2e: `go test ./core/... -race -count=1` (includes the headless
  daemon startup smoke test — boots a real `irrlichd` on an ephemeral port
  under `t.TempDir()`, so it never touches the production daemon).
- Architecture: `core/architecture_test.go` (runs automatically as part of
  `go test ./core/...`) statically enforces the hexagonal import direction
  from Key Conventions — `domain/` and `ports/` packages may not import
  outward into `adapters/` or `application/`, `application/services/`
  may only reach `adapters/inbound/` through `ports/`, and `pkg/` — the
  shared leaf layer depended on from domain, adapters, application and
  cmd alike — may not import `adapters/` or `application/` at all. It checks
  **direct** imports only, so a rule constrains the edges a package declares,
  not what those edges drag in transitively. `pkg/` was unbound until #1391,
  where the natural fix for a decode shared between `pkg/tailer` and the
  `hookjson` adapter was an import that no rule in the table forbade; the
  shared code went to a new leaf (`core/pkg/jsonc`) and the missing rule was
  added with it.
  A second architecture rule lives beside it in
  `core/architecture_hookbody_test.go` (#1389) and is deliberately a separate
  file: the layering table asks a question about the IMPORT GRAPH, while that
  one asks which EXPRESSIONS a function may contain, and needs
  `NeedSyntax|NeedTypes|NeedTypesInfo` over a narrow pattern rather than
  `NeedName|NeedImports` over the module. It enforces that inside
  `core/adapters/inbound/agents/...` an inbound `*http.Request`'s body may be
  read only by `hookjson.readBody`, the single decode both of that package's
  entry points (`DecodeConfined`, `DecodeSealed`) funnel through — see "Hook
  path confinement" below for why. Its corpus is `core/architecture_hookbody_shapes_test.go`: one file
  per spelling (decoder in a variable, `io.ReadAll`, an aliased body, a helper
  in another file, `r.FormValue`, a request stashed in a struct field) pinned
  to the verdict the detector must return, plus two `want:false` cases —
  `*http.Response.Body` and `r.Method` — that pin the false positives a
  name-based rule would produce. Every case asserts the construct it plants is
  actually present in its own source before any verdict is checked, because a
  corpus that quietly stops containing its own test cases reads as a pass.
- Architecture score: `tools/ars-gate.sh` flags it when the Agent Readiness
  Score (composite or any category) regresses vs `origin/main` — advisory,
  not a merge gate: it runs as a PR check (`.github/workflows/ars-gate.yml`,
  not required by branch protection) and is mirrored locally by
  `tools/preflight.sh`'s `arch` gate (see "Local CI parity" below). A
  red result is a prompt to look closer, not a block — use judgment on
  whether the regression is worth addressing before merging. Deterministic
  and workflow-agnostic: it fires on any push, not tied to a specific agent
  skill.
  **What IS required is stated here positively**, because "not required by
  branch protection" appears twice in this section and nothing said what is —
  the sentence #1432 filed as the one creating the false impression. Measured
  with `gh api repos/ingo-eichhorst/Irrlicht/rulesets/15993081`: the "Protect
  Main" ruleset is active on `main` with **zero** bypass actors and carries
  `pull_request`, `required_linear_history`, `deletion`, `non_fast_forward`
  and `required_status_checks`, whose required contexts are exactly **`go-test`
  and `build-test`**. Classic branch protection on `main` is empty
  (`.required_status_checks.contexts` is `[]`), so the ruleset is the whole of
  it. When #1432 was filed (2026-08-09) that ruleset carried no
  `required_status_checks` rule at all, so this is a behaviour CHANGE, not a
  restatement. Its consequence is #1432's own scope item 3: a **CONFLICTING**
  PR dispatches no `pull_request` workflows, so those two checks come back
  ABSENT rather than red — and an absent required check reads as "waiting for
  status to be reported", i.e. indistinguishable from still-queued. That is
  correct behaviour with a confusing symptom; rebase the PR and the checks
  appear.
  The ARS score is also what the README's ARS badge shows, and since #1654 it
  is published the way the CodeScene one is: `.github/workflows/ars.yml`
  PATCHes a shields.io endpoint payload into the badges gist on every push to
  `main` and writes nothing to the repository. It used to commit a rewritten
  README.md and push it to `main` — which the `pull_request` rule above refused
  on every run from 2026-04-26 onward, silently until #1647.
- Code health: CodeScene posts a "CodeScene Code Health Review" check on every
  PR automatically (via the CodeScene GitHub App, configured on codescene.io
  project 82148 — not a workflow in this repo). Like the ARS score, it's
  advisory, not a merge gate: neither branch protection nor the "Protect
  Main" ruleset requires it to pass — that ruleset's required contexts are
  `go-test` and `build-test` and nothing else (bullet above). A red result is
  a prompt to look closer, not something to chase to green before merging or
  releasing. The
  README's CodeScene badge shows the live score, auto-refreshed on every
  push to `main` by `.github/workflows/codescene-badge.yml`. For concrete,
  file:line-level findings (rule, message, fix effort) rather than a
  hotspot/trend view, run `/ir:sonarqube-report`, which reads SonarQube
  Cloud's issue list via `tools/sonarqube-report.sh` (needs `SONAR_TOKEN`
  in a local `.env` — see `.env.example`).

- Skill files: `tools/skill-lint.sh` reads every `.md` under
  `.claude/skills/` plus any other tracked `SKILL.md` (there is one under
  `tools/irrlicht-design-system/`) — the files that tell agents how to triage,
  plan, implement and review, and which had no mechanical coverage at all
  until #1209 (PR #1204 changed two of them, `preflight.sh --changed` skipped
  all ten gates, and thirteen PR checks went green having read nothing that
  changed). Unresolved conflict markers, leftover `{{TOKEN}}` / `REPEAT:` /
  `OPTIONAL:` template scaffolding and an unbalanced code fence are hard
  failures; the internal-reference, list-count and frontmatter checks are
  heuristics and only warn until their noise floor is known (`--strict`
  promotes them, which is how one gets hardened). The fence and frontmatter
  checks exist because skipping is how the linter tells "documents a marker"
  from "has one" — so an unbalanced delimiter would otherwise silence the rest
  of the file. That is the parse-failure rule at the top of this section in its
  local form. Runs as
  its own `skill-file lint` gate in `tools/preflight.sh` (scoped to skill
  markdown plus the linter itself) and unscoped as test.yml's "Lint skill
  files" step — first in the job, before `setup-go`. Its own tests are
  `tools/lib/skill-lint_test.sh`, over the fixture corpus under
  `tools/lib/testdata/skill-lint/` — so the assertions never move when a real
  skill file is edited, and `testdata/` is excluded from the gate's own walk
  because those fixtures are deliberately corrupt.
- POSIX shell scripts: `tools/posix-lint.sh` checks every file git knows
  about — tracked, plus **untracked and not gitignored** (#1611) — whose
  **first line** is a `#!/bin/sh` shebang; today `site/install.sh`,
  `tools/linux-replay-entrypoint.sh` and `tools/git-hooks/shim` (#1591 brought
  the third into scope, which is the whole reason that file was written in
  POSIX sh rather than bash). Line 1 only, because
  `tools/lib/install-uninstall_test.sh` is a bash file that writes `#!/bin/sh`
  stubs inside a heredoc, and a content grep would try to lint it as POSIX sh.
  The untracked half is #1611 and it is #1591's own consequence one layer
  down: once `changed_files_vs_origin_main` counted untracked files, a brand
  new `#!/bin/sh` script DID put this gate in scope, and a gate walking
  `git ls-files` then could not see the file that summoned it — `ALL PASS`
  over a file it never read, this gate's founding incident arriving through
  file selection. Untracked paths join the SAME stream and the SAME loop as
  tracked ones rather than getting a second walk, so the `testdata/` exclusion
  and the line-1 rule cannot apply to only half the set; the mis-implementation
  is not hypothetical — measured, a second walk lints the deliberately-corrupt
  fixture corpus and the linter's own bash source.
  It runs two different kinds of check on each file: a real POSIX shell's
  parser (`dash -n`) and **every** static bashism linter installed —
  `shellcheck --shell=sh` filtered to its POSIX-compatibility codes
  (`SC3xxx`, plus `SC2039` and `SC2112`/`SC2113`, so general style debt stays
  out of scope) and `checkbashisms`. Both kinds, because the parser alone is
  far weaker than it looks: measured one bashism per file, `dash -n` catches
  **3 of 8** — it flags arrays, process substitution and the `function`
  keyword, and accepts `[[ ]]`, `${v,,}`, `+=`, `echo -e` and `source`, where
  either static linter catches all eight of those. *Every* installed linter
  rather than the first one found, because the two disagree beyond that
  sample: `checkbashisms` accepts `local`, `set -o pipefail` and `echo -n`,
  which `shellcheck` rejects (SC3043/SC3040/SC3037) and which an installer
  accretes. CI has shellcheck and not checkbashisms, so preferring the other
  one locally would let a developer's preflight pass a diff CI rejects —
  running both is monotone, and a run without shellcheck says out loud that
  it is weaker than CI. That gap is #1423: `site/install.sh` reaches users as
  `curl … | sh`, which on Debian and Ubuntu is dash, so a bashism lands on a
  new user's first command before anything is installed that could report it.
  The gate lives in **`linux.yml`**, not test.yml, and the placement is the
  decision rather than an accident — ubuntu-latest is the only runner where
  `/bin/sh` is genuinely dash *and* the image ships shellcheck (0.9.0); the
  macos image ships none, and test.yml's `go-test` job is pinned to macOS for
  the runtime paths in `go test ./core/...`. Mirrored locally by
  `tools/preflight.sh --only posix`. **Three ways out are hard failures, not
  skips** — no POSIX shell, no static linter, and an empty file set — because
  a gate whose absence reads as a pass is the exact defect it was built to
  remove. Its tests are `tools/lib/posix-lint_test.sh` over the corpus under
  `tools/lib/testdata/posix-lint/`: one deliberately-broken fixture per
  bashism class, committed rather than improvised so the mutation evidence
  outlives the PR, plus a clean `good-clean.sh` as the vacuity guard,
  `noisy-but-posix.sh` (POSIX-clean but SC2086-noisy) pinning the severity
  filter in the one direction the `bad-*` files cannot reach, and two cases
  pinning the refusals. That suite runs in `linux.yml`, **not** in test.yml's
  `tools/lib/*_test.sh` loop — it needs a linter the macOS image lacks, and
  the loop skips it by name for that reason. One of those cases exists because the first
  draft of the linter reproduced #1423 inside itself — it piped into `grep`
  and tested the capture for emptiness, so a linter that failed to run came
  back empty, empty read as clean, and it printed `ALL PASS` over an installer
  carrying a deliberate `[[ ]]`. `testdata/` is excluded from the gate's own
  walk, the same split `skill-lint.sh` draws. Separately,
  `install-uninstall_test.sh` now *executes* the installer under `dash` rather
  than `sh` (macOS ships `/bin/dash`), which is the runtime half — it reaches
  only the lines a case runs, where the linter reads every line.
- Bash scripts: `tools/bash-lint.sh` runs `shellcheck --shell=bash
  --severity=warning` over every file git knows about — tracked, plus untracked
  and not gitignored — whose **first line** is a bash shebang. 118 files
  (83 at #1684, plus the 34 #1687 brought in, plus one since — the count is
  hand-kept and moves with any added script), and until #1684 **no static
  linter read one of them**: the bullet
  above selects on `#!/bin/sh` line 1, deliberately (#1611), so every bash file
  fell through — including the bounded gate runner (`gate-budget.sh`), the
  pre-push hook's own scoping rules (`changed-files.sh`), the shared suite
  runner and every extract-and-execute workflow lock, i.e. the machinery
  deciding whether the other gates pass. Same file-selection blindness class as
  #1423 and #1611. It found 26 findings, all fixed or annotated.
  Four decisions are load-bearing and each was measured rather than preferred.
  - **DEFAULT-IN, opt-out with a reason.** The scope is not a prefix list — it
    is everything, minus the declared `EXCLUDE` globs each carrying its
    justification, and an exclusion that matches NOTHING is a hard refusal
    (exit 2) rather than a no-op, the same both-directions existence check
    `TW_EXEMPT_KEYS` and `nilTolerant` get. So a bash file added anywhere is
    covered by existing. Since #1687 **exactly one** family is out — the
    deliberately-corrupt `testdata/` corpora — which puts more weight on that
    refusal than it carried before: it is now the only proof the existence
    check works, so a second exclusion owes its own arm in the suite.
    #1687 brought in the other two, the recording rig's per-agent drivers
    (`replaydata/**`, 33 files) and the `.tmpl` they are generated from: 89
    findings, all fixed or annotated, and **byte-identical on 0.9.0 and
    0.11.0**, which extends the version-agreement measurement below to that
    corpus rather than assuming it carries. Why they were worth it beyond the
    count: #1388/#1694 found codex's driver had ROTTED against codex-cli
    0.147.0 and produced a healthy-looking fixture — `driver.exit-reason: ok`,
    zero rollouts — from a run that did nothing. Ten drivers steer live TUIs by
    grepping literal strings, and no static analyser had read one of them.
    Of the 78 SC2034s, 77 were verified cross-file seams and annotated per
    site; the one with no consumer anywhere (`SKILL_DIR` in the gastown driver)
    was deleted rather than annotated, which is the distinction the rule turns
    on.
  - **A severity FLOOR, not posix-lint's named code set**, and the reason the
    precedent was not followed is that SC3xxx is a closed family by definition
    while "bugs in bash" is not: an opt-in code list is not enforced on a code
    shellcheck adds later. The floor is `warning` because that is where the
    VERSION SPLIT vanishes. Measured per-file over the whole corpus with 0.9.0,
    0.10.0 and 0.11.0 binaries side by side: at `warning` all three are
    **byte-identical** (26 findings, same line:col); at `style` they are not,
    and the asymmetry runs in BOTH directions with the damage on the CI side —
    138 findings CI's 0.9.0 reports which a local 0.11.0 cannot produce
    (137 × SC2317) against 25 the other way (SC2329). A `style` floor would
    make `preflight --only bash` pass a diff linux.yml rejects, the exact
    round-trip posix-lint's monotonicity argument exists to prevent. `-x`
    (external sources) is off, also measured: these libraries source through
    variable paths, so it changed the count by zero.
  - **ONE FILE PER INVOCATION.** Measured: shellcheck given several files at
    once suppresses SC2034 for a name used in ANY of them — one file alone
    reports 2 findings, the same file beside `await-gone.sh` reports 0. A
    multi-file gate's verdict would depend on which other files shared the
    command line, which `--changed` scoping makes differ between a push and
    CI's full run. It is also why the count is 26 where a single multi-file run
    over the same tree says 15.
  - **A comment line whose FIRST word is the linter's name is a DIRECTIVE**, and
    an unparseable one makes shellcheck ABANDON the file — every later finding
    silently disappears. That is inside the floor (SC1072/SC1073 are `error`)
    for the same reason `posix-lint.sh` refuses to filter its parse-abort codes
    into silence, and it is the gate's most valuable rule: it caught the
    construct **four times in this PR's own new prose**, and
    `replaydata/_lib/drive/contracts.sh` carried it from #508 until #1687 —
    for that whole time shellcheck's ONLY output for that file was the two
    parse errors. **No extra guard was added for it in #1687, because the floor
    already refuses it**: with the file in scope the gate goes red on
    SC1073/SC1072, measured on 0.9.0 and 0.11.0 alike. Note the correction to
    #1687's own framing while quoting it — "rewording that one line surfaces an
    SC2005" is true at shellcheck's DEFAULT severity and false at this gate's,
    since SC2005 is `style`; at `--severity=warning` the reworded file reports
    nothing. What the floor buys is not the hidden finding but the end of the
    abandonment, i.e. that any finding a future edit introduces is visible at
    all. The
    sibling SC1125 is the `# shellcheck disable=SCxxxx — <prose>` spelling; the
    disable IS still honoured (measured on both versions), but the reason must
    go behind a second `#`.
  The sanctioned escape hatch is a per-SITE `# shellcheck disable=SCxxxx  #
  <reason>` naming its consumer's `file:line` — all 94 SC2034s are that (17 at
  #1684, 77 at #1687), each verified to have a real reader — and never a code
  removed from the gate.
  A directive covers only the NEXT command, so four adjacent knob assignments
  need four. Two consequences #1687 measured and had to work around: a `;`-
  joined declaration line (`A=(); B=(); C=()`) takes a directive only on its
  FIRST assignment, so such lines are split one per line rather than left with
  a directive that appears to cover three and covers one; and a directive
  placed before the file's first COMMAND applies file-wide, which is a blanket
  disable wearing a per-site shape and is not used.
  It lives in **`linux.yml`** beside `posix-lint.sh`, but for a DIFFERENT
  reason, and the difference is the decision: posix-lint needs ubuntu because
  `/bin/sh` must genuinely BE dash there — a property of the runtime — whereas
  shellcheck is a static analyser that reads no interpreter, so neither the OS
  nor the arch enters the verdict (the 0.9.0 measurement above was taken from an
  x86_64 binary under Rosetta on arm64 macOS and agrees byte-for-byte with a
  native arm64 0.11.0). What picks the host is only that the ubuntu image ships
  shellcheck and macos-latest ships none. Mirrored locally by
  `tools/preflight.sh --only bash`. Its tests are `tools/lib/bash-lint_test.sh`
  over the committed corpus under `tools/lib/testdata/bash-lint/` — one
  deliberately-broken fixture per rule class, `good-clean.sh` as the vacuity
  guard, `style-noisy-but-warning-clean.sh` pinning the floor in the one
  direction the `bad-*` files cannot reach, and the abandonment fixture proved
  by REWORDING its directive line and asserting the hidden SC2115 appears.
  That suite runs in `linux.yml` for the same reason posix-lint's does
  (above) — it is the loop's SECOND skip
  argument. `shell-lib-suite_test.sh` now derives that list from the workflow
  step and existence-checks every name, because the one-file check it had would
  have gone on passing while saying nothing about the new entry.

- Recording-driver teardown: a Go tripwire,
  `tools/onboarding-factory/internal/driverteardown`, over every
  `replaydata/agents/*/driver-interactive.sh` enumerated from disk, so a new
  adapter is covered the day it lands rather than the day someone remembers it.
  It grades four invariants — a `tmux kill-session` in the EXIT-trap handler or
  at top level is never gated on a liveness flag (INV-1), a driver that launches
  tmux installs a `trap … EXIT` whose handler tears the session down (INV-2),
  every session name it mints carries the driver's own `$$` as a `-`-delimited
  field (INV-3), and a handler that writes `driver.exit-reason` cannot write the
  verdict variable's INITIAL value on an abort path (INV-4).
  INV-4 is the one guarding a regression the #1825 fix would otherwise have
  shipped, so it is the one most easily weakened by accident: before that fix an
  aborting driver wrote no `driver.exit-reason` at all and `run-cell.sh` read the
  absence as `unknown`, but a trap writing `"$EXIT_REASON"` unconditionally turns
  that absence into the initialiser — `ok` — reporting SUCCESS for a run that
  never formed a verdict. Nine drivers had that hole, six of them before the trap
  work started. A handler writing a literal rather than a variable has no
  initial-value hazard and is exempt BY DERIVATION, not by an allowlist
  (opencode, whose inline trap writes no verdict at all).
  INV-1 says "at top level", not "the end-of-run sweep", and the two are not the
  same set: a driver whose step dispatch is a top-level `while … case` — copilot
  today — has its per-step kills classified as teardown. They are ungated, so it
  passes; the note matters because a kiro-cli-shaped step guard would read as a
  violation there.
  It is the STATIC half of a pair: the runtime half is
  `tools/onboarding-factory/scripts/lib/tmux-teardown-check.sh`, which
  `run-cell.sh` calls after the driver returns to assert no session carrying
  that run's `driver.pid` survived. That runtime check matches on the pid
  alone, so it is only sound while INV-3 holds — which is exactly why INV-3 is
  enforced rather than assumed, and why the two halves are named in each
  other's headers.
  Structure, not text, separates a teardown site from a legitimate step-level
  guard: both spellings of the gate are byte-identical, so INV-1 grades a
  `kill-session` inside the EXIT-trap handler or at top level, and leaves one
  inside an ordinary function alone (kiro-cli's `step_exit_clean` /
  `step_sigkill` entry guards are correct — `reset_session` aliases a retired
  slot number onto a live slot's pane). The accepted blind spot is a driver
  that moves its end-of-run sweep into a helper function; INV-2 still binds
  there. Every structural assumption is a refusal rather than a best-effort
  parse — an unclosed function, an unbalanced `if`/`fi`, a `tmux new-session`
  with no `-s`, an empty file, or a driver where INV-1 graded nothing all
  return errors, so "found no violation" and "could not look" never produce
  the same output. #1825 is the incident: `step_exit_clean` signalled a
  graceful exit, slept a second, and marked the slot dead without observing
  it; the teardown loop was gated on that flag and skipped the only session
  that needed killing. Nine recordings in one morning each leaked a live
  `claude` and its tmux session.

- Sourced shell libraries: not a contract family — a tripwire,
  `tools/lib/shell-lib-errexit_test.sh`, over every `tools/lib/*.sh` that is
  not a `*_test.sh`. Each function it can drive must, under a caller's
  `set -e`, return what its library DOCUMENTS rather than aborting the caller,
  and must leave the caller's shell options byte-identical. It exists because
  three issues in a row were the same defect in a different file — #1629 (a
  workflow step reading `$?` on a line GitHub's implicit `-e` never reached),
  #1633 (`swift_suite_run`'s post-timeout kill sequence aborting before
  `return 124`), #1635 (`budget_run`'s backgrounded child inheriting errexit
  and dying before writing the status file that is its "it finished" signal,
  so a gate that failed instantly was reported as a TIMEOUT that burned the
  whole budget) — and each sibling was found only because someone happened to
  look. These files are SOURCED, so they run with whatever options their caller
  has, and none of them said anything about that. Four things are load-bearing:
  - **Bare statement position is the whole point.** Every other calling shape —
    `if f`, `f || x`, `x=$(f)` — makes bash ignore errexit for the whole
    function body, and (measured on 3.2.57) for a subshell that body
    backgrounds as well. The very call that came back 1 instead of 3 against
    the unfixed `gate-budget.sh` returns 3 written as `budget_run … || rc=$?`,
    so a lock written that way is green against a broken library.
  - **`$(set +o)` cannot capture the options.** bash 3.2 reports errexit and
    nounset as OFF inside a command substitution regardless of the parent —
    measured, same shell, same instant: `$( )` gives `set +o errexit` where a
    redirect to a file gives `set -o errexit`. A probe built the obvious way is
    byte-identical before and after any leak and can never fail. The redirect
    spelling is used, and `tw_fixture_leaks` is its committed guard.
  - **No function is blind-called.** Each has a named setup+call recipe;
    expected statuses are chosen DISTINCTIVE (0, 2, 3, 124) because a documented
    1 is indistinguishable from an errexit abort, which also exits 1. Anything
    undrivable is named in `TW_EXEMPT_KEYS` with its reason — today one entry,
    `swift_suite_run` on a non-Darwin host, and it is inactive on macOS where
    the suite actually runs. Recipe keys and exemption keys are existence-
    checked against the walk in BOTH directions, so a library that stopped
    being walked surfaces as its recipes naming nothing rather than as silence.
  - Its own mutation evidence is committed (`tw_fixture_correct` /
    `tw_fixture_aborts` / `tw_fixture_leaks`, four synthetic bijection cases,
    and a real walk over a directory holding three of the libraries,
    swift-suite.sh deliberately not among them — phrased that way rather than
    as "three of the four" because #1639 added a fifth and made that count
    stale). The
    fixtures matter in both directions: a leaked `set +e` is a *working* fix for
    the return value, so obligation (a) passes it and only (b) objects.
  What it buys over a hand-written lock was measured on this repo: deleting
  `_budget_kill_tree`'s own guard reddens the tripwire and leaves
  `gate-budget_test.sh`'s whole `-e` block green, because that helper is only
  ever reached through a region `budget_run` guards separately.
- Two defaults for shell logic, both learned the hard way and both re-broken
  inside the very PR that documented them (#1825, caught at review):
  - **A suite under a `lib/` directory is DISCOVERED, never listed.**
    `shell_lib_suite_run <dir>` globs `*_test.sh`, so a hand-written
    "run this suite" block beside it does not add coverage — it re-runs a suite
    the glob already found, and it re-creates the membership-by-hand problem the
    glob exists to remove. #1803 added two suites that a hand-typed list did not
    notice; #1825 then added two blocks four lines below the glob that had
    already picked them up, and each of those suites ran twice.
  - **Shell logic that needs a RUNTIME test gets marker-extracted, not
    re-implemented in the test.** Wrap the block in `# BEGIN <name>` /
    `# END <name>`, have the test extract and `eval` it against stubs, and make a
    missing marker a loud REFUSAL rather than an empty pass — a test that
    reimplements the code under test passes while the real code is broken, which
    is the same "graded nothing" failure a vacuity guard exists to catch.
    `tools/onboarding-factory/scripts/lib/run-cell-multi-teardown_test.sh` is the
    reference; the drivers' `cleanup()` handlers use it to prove INV-4's guard
    actually rewrites an aborted verdict, which the static checker cannot see.

- The shell-lib suite runner: `tools/lib/shell-lib-suite.sh` is the ONE
  implementation behind test.yml's "Test the shared shell libs" step and
  `tools/preflight.sh`'s `tools` gate. Before #1639 those were two copies of
  the same loop that disagreed about the only thing that matters: preflight's
  collected every file's status, CI's had no `|| rc=1` and — test.yml declaring
  no `shell:` and no `defaults:`, so GitHub's `bash -e {0}` applies (errexit
  only; see "Which bash a workflow step gets" below) — aborted on the FIRST
  failing file with every later file
  in glob order never run and nothing in the log saying so. One round trip per
  red file, on suites that take seconds each. The sharing follows
  `macos-swift.yml`'s own reason for sharing `swift-suite.sh`: CI and the
  pre-push hook judge a run by the same rules rather than by two
  implementations that can disagree. Three things about it are load-bearing.
  It prints a **census** — found / skipped / ran / failed — because "they all
  passed" and "the loop stopped early" had no distinguishing line in either
  predecessor. An **empty corpus is a named refusal** (status 2, distinct from
  1) rather than either predecessor's answer, both measured: CI's loop iterated
  once with the literal unexpanded pattern (nullglob is off by default and
  neither invocation changes it) and died with `No such file or directory`,
  while preflight's `[[ -e "$t" ]] || continue` filtered that out and returned
  0, passing silently. And the two callers' one remaining scope difference —
  CI skips `posix-lint_test.sh`, which needs a linter the macos image lacks —
  is an **argument** that is validated against the corpus before anything runs,
  so a skip that stops matching a real file is a refusal instead of a `case`
  pattern quietly matching nothing. Its tests are
  `tools/lib/shell-lib-suite_test.sh`, which grades the runner in BARE
  STATEMENT position as well as under `|| rc=$?`: bash suppresses errexit for a
  function's whole body when the call sits in a `||` position, so a runner that
  ran its files as bare `bash "$f"` statements is invisible from the `||` shape
  and from the CI step itself. Both predecessors are emitted verbatim there and
  re-measured on every run, which doubles as the vacuity guard — if bash ever
  stopped aborting, the fix would be protecting nothing and would pass for the
  wrong reason.
- The ARS badge job: `tools/lib/ars-badge-push_test.sh` EXTRACTS each of
  `.github/workflows/ars.yml`'s three `run:` steps out of the workflow
  file and EXECUTES it against a stub, under the invocation that step
  actually gets — DERIVED from the workflow, today `bash -e` (see "Which bash a
  workflow step gets" below; this bullet claimed
  `bash --noprofile --norc -e -o pipefail` until #1650). Behavioural rather
  than a text scan, because a scan pins one spelling of a guard where running
  the block pins the property. The FILENAME now describes nothing in the file
  and is kept deliberately: it was written for a "Commit badge update" step
  (#1641), and #1654 deleted that step, so there is no push left anywhere in
  this workflow. Its header is the description instead.
  **The job could never do what it was built to do, and was green about it for
  four months** (#1654). It pushed the badge commit straight to `main`, which
  the "Protect Main" ruleset refuses — `GH013: Repository rule violations
  found` — and a rule violation is not a transient condition, so none of the
  five retries could ever clear it. Every run since 2026-04-26 computed a
  score, rewrote README.md on the runner and threw it away: README read
  `8.1/10` while the scan returned `7.9/10`. Before #1647 made the failure
  loud, it did that behind a green check. The fix is codescene-badge.yml's
  shape — the extract step builds a shields.io ENDPOINT payload, a new "Update
  Gist with ARS score" step PATCHes it into the same badges gist coverage.yml
  and codescene-badge.yml already write to (no new secret: `GIST_SECRET` and
  `COVERAGE_GIST_ID` were already configured), and README's badge became an
  `img.shields.io/endpoint?url=…` pointing at `ars.json` in that gist. Nothing
  is committed, so nothing can be refused, and the job dropped from
  `contents: write` to `contents: read` — a real reduction, since that grant
  existed only for the push and this job runs a third-party CLI (pinned by
  commit, but still) next to the workflow token.
  **What that deleted is a real loss and is named rather than glossed**:
  #1641's four obligations (the pre-fix retry loop replayed verbatim, five
  failed pushes exiting 0; the exhausted case failing loudly; the
  third-attempt arm proving the retry is still a retry; the clean paths) and
  #1655's six (the same loop against REAL git in a throwaway repo — the
  mid-rebase wreckage, the ordinary rejected push, the committed rejected
  `git rebase --abort` spelling, the unabortable-rebase refusal). "A rewritten
  guard replays its predecessor's cases" is about a guard being REWRITTEN;
  here the mechanism was REMOVED, and contorting those obligations into
  passing against a step that does not exist is the vacuous green this whole
  family is about. The retry-loop defect they fixed is now covered nowhere,
  because no retry loop remains in this repo's workflows — if one is written
  again, they are in git history at `ae85182f`. `workflow-step_test.sh` moved
  its real-workflow row and its `shell:` mutation target from
  'Commit badge update' to 'Run ARS scan' for the same reason.
  **The two steps that PRODUCE the badge are #1644's and they survived the
  deletion**: `ars scan … || true` followed by a `cat` that succeeds, then an
  extract step whose `if [ -n "$ARS_BADGE" ]` skipped in silence — three green
  steps and a frozen badge. The `|| true` was **not** protecting a legitimate
  low-score exit, and that had to be checked rather than assumed because the
  issue proposed leaving it in place: pinned v0.0.9 returns `ExitError{Code: 2}`
  only under `if p.threshold > 0`, this invocation passes no `--threshold`, and
  there is no `.arsrc.yml` to supply one — measured, the pinned binary scoring
  `./core` at 7.9/10 exits 0. So the scan's status is read (`|| scan_status=$?`,
  never a bare `; rc=$?`, the line #1629's implicit `-e` never reaches) and
  three outcomes are distinguished where there were two. A failed scan **fails
  the job**, because this workflow gates nothing: it runs post-merge on `main`,
  no PR and no merge depends on it, and its own "Install ARS CLI" step already
  fails the job for the likeliest transient cause (a `go install` outage) — a
  scan that could not run was the one failure here that was silent.
  Each refusal asserts EVERY other refusal's WORDING is ABSENT — nine of them
  now, across three steps, driven from one table rather than pairwise by hand —
  because a shared non-zero is satisfied by refusals that all fire together.
  Re-measured against the new extract step: deleting the missing-badge refusal
  leaves it exiting 1 via the URL refusal, so every status arm stays green and
  only the wording arms go red, exactly as #1644 measured on its predecessor.
  **The gist step is entirely new, so every one of its obligations passes the
  moment it is written**, and the two mutations worth carrying are the ones
  that do NOT simply flip a status. Dropping `|| curl_status=$?` makes a
  transport failure abort the step under the implicit `-e` with an EMPTY log —
  no annotation, no diagnosis, "could not be attempted" going silent — while
  the status arm stays green. And replacing the numeric-shape check on
  `%{http_code}` with a bare `[ -z … ]` lets a non-numeric code print
  `integer expression expected` twice, evaluate FALSE, and reach the SUCCESS
  line: a failed publish reported as a publish. Both spellings were
  codescene-badge.yml's and coverage.yml's, noted here rather than changed
  there — which is what #1710 was filed about and has since fixed; the bullet
  below is where those two now live.
- The other two gist badge jobs: `tools/lib/gist-badge-guards_test.sh` is the
  same extract-and-execute treatment for `coverage.yml`'s "Update Gist with
  coverage" and `codescene-badge.yml`'s "Update Gist with code health score"
  (#1710) — the two steps the bullet above found the defects in and left
  alone. **Both were LIVE, on the two badges that were working.** A transport
  failure never reached its own refusal (errexit aborts at an assignment from
  a failing command substitution, so `http_code=$(curl …)` ended the step
  before the `echo` — measured, exit 6 with no `HTTP …` line and no
  annotation), and a non-numeric `%{http_code}` reported SUCCESS (`[ "" -lt
  200 ]` errors and evaluates FALSE, both disjuncts do, and errexit exempts a
  failing command in an `if` condition — measured, exit 0 with the badge
  reported published on a run where the gist was never written). The second is
  #1654's own failure class, a silently frozen badge behind a green job, one
  workflow over. ars.yml's guards were ported rather than redesigned, so the
  three gist-writing workflows carry ONE shape.
  Four things are worth knowing. **It is one file for TWO workflows**, which
  looks like a violation of ars-badge-push_test.sh's "one harness per
  workflow" and is the same rule applied: what these two share is not a
  workflow file but the gist-write SHAPE — one step each, identical apart from
  the badge name, the gist filename and the rendered variable — plus the curl
  stub, the refusal vocabulary and one preflight trigger, and two files would
  duplicate all four and let the copies disagree. **The refusal table spans
  both workflows**, six needles rather than three, so a step that grew the
  other badge's sentence is caught — that copy-paste is where this whole class
  came from. **The `000` comment was DELETED, not reworded**: it documented a
  branch errexit made unreachable, and a comment describing a branch that
  cannot execute is the reason nobody re-checks it. And both steps moved from
  `/tmp/gist-*.json` to workspace-relative paths, matching ars.yml — a shared
  absolute path is not isolable per case, so two arms would read each other's
  response file.
  Mutation evidence is seven deliberate mutations, each seen red and each
  reverted: the capture guard and the shape check removed from EACH workflow
  (four), coverage's refusal given codescene's sentence, its success line
  deleted, and the preflight trigger narrowed back. The first pair is the one
  to remember — dropping `|| curl_status=$?` leaves every STATUS arm green,
  because the step still exits non-zero (7, from the abort); only the wording
  arms and the "reaching the line the abort used to skip" arm go red. That is
  #1644's measurement, reproduced two families on.
  The two pre-fix bodies are committed verbatim and re-measured on every run
  rather than quoted in the PR, which is the permanent vacuity guard: if
  errexit ever stopped aborting on a failed capture, or `[` stopped evaluating
  a bad integer as false, the guards would be protecting nothing and would
  pass for the wrong reason. `preflight.sh`'s `tools` trigger gains both
  workflows, its sixth widening for this reason (#1591, #1629, #1639, #1641,
  #1645).
  **The decode is the guard that is easy to leave out**, and it is the same
  class of defect one level down. shields.io's STATIC-badge path is escaped
  (`--` is a literal dash, `_` and `%20` are spaces, `__` is a literal
  underscore, `%2F` is a slash) while the ENDPOINT schema takes plain text, so
  publishing the path's bytes renders a badge reading
  `Agent--Assisted%207.9%2F10` — wrong, permanently, and green about it. An
  escape the step does not model is a refusal rather than a guess. It needs TWO
  mutations, not one: skipping the decode entirely is caught by the
  percent-escape refusal acting as a second line of defence, so the arm that
  pins the decode itself is only discriminating against a decode wrong in one
  way the `%` guard cannot see — dropping just the `--` → `-` restore, which
  reddens exactly the payload-equality arms and nothing else.
  The **`contents: read`** claim is a lock, so it is driven against two
  deliberately mutated copies as well as the real file, and the real file is
  mutated too: a predicate that had stopped matching would read identically to
  a clean workflow.
  **No linter was built, and the measurement is the reason.** Over the
  multi-line `run:` blocks in `.github/workflows/`, a rule keyed on "the
  block's last statement is `echo`/`sleep`/`cat`/`printf`" flagged 6 and
  **missed the #1641 defect** — that loop was nested inside an `if`/`else`, so
  the block's last line was `fi` — while 4 of the 6 it did flag were correct
  code; a rule keyed on `|| true` flagged 2 and also missed it; "the block
  contains a loop" flagged 3, of which 1 was the defect. That subject is gone
  with the push step, but the conclusion is not: no candidate rule both catches
  its subject and stays quiet on the correct blocks, so a green would claim
  coverage it does not have — the same conclusion #1629 reached by the same
  measurement about a `$?`-keyed rule, and #1639 about the sibling family.
  `preflight.sh`'s `tools` trigger already carries `ars.yml` (#1641's widening,
  the fourth for this reason — #1591, #1629, #1639), so #1654 needed no sixth:
  the new assertions simply join the gate that already covers the file.
- The replaydata deletion guard:
  `tools/lib/replaydata-deletion-guard_test.sh` does to
  `.github/workflows/replaydata-deletion-guard.yml`'s "Detect deletions of
  load-bearing replaydata" step what the bullet above does to ars.yml —
  extracts it and EXECUTES it against a git stub, under the same derived
  invocation (`bash -e` for that step, which is why the step supplies its own
  `set -euo pipefail` on line 1). It is the same family's most
  expensive member, because that workflow is a **merge gate**: before #1645 its
  diff was captured as `deletions=$(git diff … || true)`, and an empty
  `$deletions` is the gate's SUCCESS condition — so a git exiting 128 produced
  no violations, printed `OK: no disallowed deletions` and exited 0, permitting
  exactly the #268 deletion the gate exists to refuse. **Where** the defect sat
  is the part worth carrying: this block was spot-checked and cleared TWICE
  during #1639 and #1641, both times on the strength of the classification
  LOOP, which is genuinely fine. The statement that decides the step's status
  is the loop's INPUT, one line above it — a `case` walk over `$deletions` can
  only ever be as good as `$deletions`, and nothing in the loop can see that it
  was handed an empty string by a failure rather than by a clean PR.
  Three outcomes now, never two: deletions found and disallowed (fail), no
  deletions (pass), and **could not determine** (fail, naming why). Three
  refusals implement the third, and the second of them is a SECOND measured
  hazard rather than defensive padding — `on: workflow_dispatch` carries no
  `github.event.pull_request`, so both `${{ }}` expressions expand to the empty
  string and `git diff "..."` is `HEAD...HEAD`: exit 0, no output, PASS. That is
  measured against real git in the lock, not stubbed, since a claim about how
  git parses `...` has to be made by git. Note honestly what each refusal
  independently buys: deleting the empty-context one still leaves the run
  failing, because an empty sha does not rev-parse either — what it uniquely
  buys is the DIAGNOSIS ("no pull_request context" rather than "the base commit
  is not present in this checkout", which points at the checkout instead of the
  trigger). The commit-presence refusal is the one that is defensive: with
  `fetch-depth: 0` no ordinary condition was found that makes `base.sha`
  unreachable, so unlike the other two it is not backed by a reproduction, only
  by the observation that it is one edit to the checkout step away.
  Two more things about the lock. `cell_is_live` reads the WORKING TREE, so
  "live cell" and "orphan cell" are properties of the directory a body runs
  in — every arm executes against a throwaway tree under `$TMP`, and the real
  deletion-guarded catalog is never touched. And what the stub CANNOT grade is
  said out loud: `git mv` is permitted because git's own rename detection
  reports an R that `--diff-filter=D` drops, not because of anything the step
  does, and detection is ON by default in modern git — so `--find-renames=50%`
  pins the threshold, not the behaviour, and that arm grades the invocation the
  step makes rather than claiming the step implements renaming.
  `preflight.sh`'s `tools` trigger gains this workflow, its fifth widening for
  this reason (#1591, #1629, #1639, #1641).
- The snapshot-evidence copy:
  `tools/lib/swift-snapshot-evidence_test.sh` is the same treatment for
  `macos-swift.yml`'s "Collect the skipped suites' pixels" step (#1646), and
  it is the family's cheapest member — a `cp -R` of the reference snapshots
  into the artifact whose status was read by NOTHING. That step opens with
  `set +e` for a good reason (its whole purpose is to run assertions that
  fail), so the failed copy neither aborted nor reached `bad`: green job,
  uploaded artifact missing the `__References__` tree, and therefore failure
  images with nothing to compare against. Three outcomes now — copied /
  nothing to copy / could not copy — plus a fourth no exit status can see, an
  empty tree, which `cp -R` copies with a cheerful 0. **#1646 is where
  extract-and-execute stopped being blocked by a real `swift test`**: the
  fixture checkout's `tools/lib/swift-suite.sh` SOURCES the repo's own library
  by absolute path and overrides only `swift_suite_run`, so the two predicates
  the body consults are production code reading a committed log fixture, and
  every outcome of a 20-minute macOS job is reachable in a second. That is the
  shape to copy for any step whose blocker is one expensive command rather than
  the body. Two arms carry more than they look: the references must be copied
  even when the RUN is judged bad (this job exists to publish a failed run, so
  a copy on the happy path only would ship nothing on exactly the runs the
  artifact is for), and they must not be counted as failure images — 53
  references would otherwise satisfy the "not one of the suites produced a
  failure image" guard forever. The re-audit is in that file's header rather
  than here: `exit "$bad"` decides, six guards write `bad`, and of the four
  statements it cannot see, one degrades loudly (a failed `mkdir -p` reaches
  two guards, not the one the issue claimed) and one is silent and cosmetic
  (`>> "$GITHUB_STEP_SUMMARY"`, whose text is already on stdout).
  This workflow was in `preflight.sh`'s `tools` trigger since #1629, so the
  trigger needed no sixth widening — the assertion simply joins the gate that
  already covers it.
  **The fourth is the one worth carrying, because #1677 pinned it WRONG on
  purpose and #1678 had to correct it.** `. tools/lib/swift-suite.sh` was also
  read by nothing, and that failure was never silent — every `swift_suite_*`
  call becomes "command not found", so the step exits 1. What it got wrong was
  the DIAGNOSIS, and by more than #1646 recorded: measured, **four** headlines
  fire at once, opening with `TRUNCATED` (which sends a reader to XCTest's
  stall detector, #1523) and closing with "not one of the five suites produced
  a failure image … #1615 has moved" (which accuses this workflow's own suite
  classification). #1677 recorded that as an audit finding and committed an arm
  asserting the wrong headline — loud, and honest about being wrong, but a test
  pinning incorrect behaviour reads as coverage, which is why it was filed
  rather than left as a note. Three outcomes now, on #1645's discipline and on
  the same shape as the copy: **loaded** / **could not load** (the source's
  status, now read) / **loaded and defines nothing** (`command -v`, because
  `. an-empty-file` exits 0 — the outcome no status check can see, exactly as
  an empty `cp -R` is). Both refuse before the run rather than adding to
  `bad`, since `swift_suite_run` produces everything the six guards judge.
  Two things the mutation runs settle. The wording arms are what
  discriminate and the status arms are not: dropping the source status check
  leaves the step still exiting 1 via the OTHER refusal, so only "naming the
  harness it could not load" and the mutual-absence arm go red — #1644's
  measurement, reproduced one family on, and the reason each refusal asserts
  every other refusal's wording is absent. And keeping both refusals but
  replacing `exit 1` with a continue leaves both NAMING arms green while the
  four headline-absent arms go red, so those arms hold the exit placement
  rather than the message. The sibling `swift-test` step does **not** have this
  defect and needed no change under that ticket: measured the same way,
  `swift_suite_verdict` is itself among the missing functions, so no headline
  prints at all and the step exits 127 with three `command not found` lines —
  loud, undiagnosed, but never wrong. Weaker, not broken; closed by the bullet
  below (#1702).
- The swift-test harness source: `tools/lib/swift-test-step_test.sh` is the
  same treatment for the OTHER `shell: bash` step of `macos-swift.yml`, "Test
  (bounded, streamed under a pty)" (#1702) — the same unread
  `. tools/lib/swift-suite.sh`, refused with the same two checks, for the same
  reason (`. an-empty-file` exits 0, so a source that RETURNS 0 having defined
  nothing reads exactly like one that worked). Four things differ from the
  sibling and each is the reason this was a separate ticket rather than a
  copy.
  **The names are not the sibling's.** That step consults its predicates
  individually (`swift_suite_completed`, `swift_suite_ran_tests`); this one
  consults only `swift_suite_verdict`, which is precisely the name whose
  absence causes the whole defect and is on neither of the sibling's lists. A
  list copied across is green against a library missing exactly the verdict —
  measured, and the reason the lock drives the required set BOTH ways, one
  library per name with the REAL library minus one function.
  **The status is a decision, stated in the step.** `swift_suite_verdict` is
  the step's last command and returns 0 or 1 and nothing else, so 1 is what
  this step already means by "failed"; 127 was the shell reporting the last
  "command not found", not the workflow reporting anything. Replacing both
  `exit 1`s with `exit 127` reddens only the status arms and leaves every
  wording arm green, which is #1644's measurement run backwards.
  **What the pre-fix step did is committed and re-measured**, not quoted: the
  old two-line spelling runs on every pass with the library absent and must
  still exit 127 having printed no `::error::` at all. That is the vacuity
  guard for the whole fix.
  **And one arm is honestly non-discriminating**, said in the file rather than
  glossed: with `swift_suite_run` gone the production verdict still runs, finds
  no log and fails at 1 — the same 1 a correct refusal produces — so only the
  wording tells them apart. Dropping the source check is the sharper case: the
  `command -v` refusal then fires and reports an ABSENT file as "read but
  defines no swift_suite_run", so the step exits 1 with a wrong diagnosis and
  every status arm stays green.
- Which bash a workflow step gets: **there are two invocations and this repo
  conflated them** for the whole of the two bullets above (#1650). A step
  DECLARING `shell: bash` runs as `bash --noprofile --norc -e -o pipefail {0}`;
  a step declaring no `shell:` and no `defaults:` runs as **`bash -e {0}`** —
  errexit only, no `--noprofile`, no `--norc` and **no pipefail**. That is
  measured off a runner rather than read off the docs: run 31960152598's own
  group header for `replaydata-deletion-guard.yml`'s step reads
  `shell: /usr/bin/bash -e {0}`. Of this repo's workflows only
  `macos-swift.yml` declares `shell: bash` (on two steps; its job-level
  `defaults:` sets `working-directory` and no shell), so every other step is on
  the `bash -e` side.
  **The direction of the error is what made it worth a library rather than a
  comment fix.** Four harnesses extracted a step body and ran it under the
  pipefail spelling, each saying in its own header that running it under
  anything else "would grade a different program" — which was true, and was
  what they were doing. A body whose correctness depends on pipefail
  (`x=$(thing | grep -v skip)`) is graded SAFE by a harness that supplies it and
  swallows the failure in production: a false green, the same "absence of a
  finding and inability to look produce the same output" shape as the rest of
  this section. (Under `shell: bash` the error reverses and is loud.)
  So the invocation is **derived, not typed**: `tools/lib/workflow-step.sh`
  answers `workflow_step_shell <workflow> <step>` and `workflow_step_body`
  off the same one-pass scan, so a harness cannot grade one step's body under
  another step's shell, and a step that later gains `shell: bash` — or whose
  job or workflow gains a `defaults: { run: { shell: … } }` — moves its harness
  with it. It REFUSES (status 2, naming what it could not do) for an unreadable
  file, an absent step, a DUPLICATE step name, an unmodelled `shell:` value and
  a step with no `run: |` block, and never falls back to a default: a harness
  handed a plausible `bash -e` for a step that no longer exists would grade an
  empty body, which exits 0 and reads as a clean run. Its tests are
  `tools/lib/workflow-step_test.sh` over the committed corpus
  `tools/lib/testdata/workflow-step/` (one fixture per declaration shape, plus
  the refusals), and three of its obligations are the ones to keep: the two
  invocations are shown to grade the SAME body differently (without that,
  deriving is ceremony and a derivation stuck on one answer looks correct); a
  copy of a real workflow is mutated BOTH ways, since a derivation hard-wired
  to either answer passes one direction; and the five real harnessed steps are
  resolved through the same code, which is what stops
  `swift-suite_test.sh`'s hand-written `shell: bash` spelling — correct, and
  left alone — from silently stopping to match.
  Verified while fixing it, per "dismissals carry evidence": **no live pipefail
  dependency existed** anywhere in `.github/workflows/`. All 16 pipeline-
  carrying lines were read — ars.yml's two are `$(… | head -1 || echo "")` and
  its `sed "s|…|…|"` was a delimiter not a pipe (that `sed` went with the
  README rewrite in #1654, which added no pipeline: its `jq` calls read and
  write files rather than piping, and its `curl` status is captured with
  `|| curl_status=$?`); the deletion guard sets its own
  `set -euo pipefail`; macos-swift.yml is on the `shell: bash` side already;
  test.yml's is inside an `echo` message; and codescene-badge.yml's
  `SCORE=$(… | jq -r …)` and coverage.yml's `pct=$(…)` are each followed
  immediately by an explicit emptiness guard that does the work pipefail would.

- Web (only when touching a `web/` tree): `npm test` in that tree. There are
  two independent suites, each with its own `node_modules`:
  - `platforms/web/` — the dashboard.
  - `tools/onboarding-factory/internal/viewer/web/` — the onboarding viewer.

  `npm test` runs `vitest run` (single CI-shaped pass, no watch).

  `node_modules/` is gitignored, so a fresh clone — or any new
  `git worktree add` — starts without dependencies. No manual install step is
  needed: each tree's `pretest` script runs `npm ci --ignore-scripts` when
  they're missing, so `npm test` self-heals on its first run (slow once,
  instant afterwards). To get it out of the way up front, run `npm ci` in the
  tree yourself. **Never `npm install`** — it re-resolves the dependency graph
  and rewrites `package-lock.json`; on an npm older than the one that wrote the
  lockfile it silently strips the `libc` fields from the
  `@rolldown/binding-linux-*` entries, and that churn then rides along in an
  unrelated PR. `npm ci` installs *from* the lockfile and never writes it.

  Because the two trees resolve independently, they can drift onto different
  versions of the same transitive package — which is how one ended up carrying
  a vulnerable `postcss` while the other was patched (#1225). Both are kept
  current by weekly dependabot updates configured in `.github/dependabot.yml`,
  which covers the Go modules and GitHub Actions on the same schedule; a bump
  landing in only one tree is a signal something is wrong with that config, not
  normal.

### Local CI parity — catch failures before pushing

`tools/preflight.sh` runs every PR-gating check (test.yml + web-test.yml +
ars-gate.yml + linux.yml's replay-fixtures step natively, plus the full Linux
build+test gate via Docker under `--linux`) locally and prints a pass/fail
summary instead of stopping at the first failure — so before opening a PR, run
it once instead of round-tripping through GitHub Actions per fix. Gates run
**cheapest first**, in two phases, not in "CI's order": there is no single CI
order to mirror, since those are separate workflows GitHub runs concurrently,
and the order is load-bearing under `--budget` (below) because it decides which
gates survive a squeeze. `skill-file lint`, `POSIX sh lint` and `bash lint` are the
only coverage their file families have and cost a second or two each, so they
run before four minutes of `go test` — which is the argument test.yml already
makes in-file for running the skill lint before `setup-go`.

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the test.yml-equivalent gates
tools/preflight.sh --only arch    # just the ARS architecture gate
tools/preflight.sh --only skills  # just the .claude/skills/**/*.md linter
tools/preflight.sh --only bash    # just the shellcheck lint over bash scripts
tools/preflight.sh --only swift   # just the macOS Swift build + test suite
tools/preflight.sh --budget 540   # bound the whole run; see "The budget" below
```

**For an automated caller (an agent), `--only` chunking is the recipe, not a
debugging convenience — the unscoped run does not reliably fit a foreground
`Bash` call's 600s budget** (it reliably exceeds it on this machine; the long
pole is the `go` group's core suite + replay fixtures). Run each group as its
own **foreground** invocation instead of the single unscoped command:
`tools/preflight.sh --only go|web|arch|tools|skills|posix|bash|security|swift` (see
`tools/preflight.sh --help` for the current group list; `linux` stays opt-in
and needs Docker). Every gate still runs — chunking only changes how many
invocations it takes. **Do not background the unscoped run to make it fit**:
a subagent is not woken by its own background job, so the run stalls silently
with the work committed but never pushed
(`.claude/skills/ir:exec/SKILL.md` Phase 4 step 11 has the incident and the
same recipe). The same shape recurs for any subagent driving an interactive
process, not just preflight — one Bash call per step, every wait a bounded
polling loop, `timeout N` on anything that can hang, and findings
checkpointed to a file before they're used, so a stall costs a turn instead
of the work — see the two bullets that skill's Notes section carries beside
that same incident (#1726). Chunking is still the recipe for a *manual* unscoped run;
`--budget` is what covers the `--changed` run the hook performs, and the two
compose — `--only <group> --budget <n>` bounds one group.

Also read a push's exit status directly, never through a pipe: `git push … |
tail` reports `tail`'s status, so a push the hook refused looks like a success
to the caller. Assert afterwards that `git status -sb` shows a tracking branch —
this is a plausible cause of the "committed but never pushed" incident recorded
in `ir:exec` Phase 4 (#1570).

**Not `PIPESTATUS`**, which is what this paragraph advised until #1559's agent
tried it: this repo's shell is zsh, where the array is spelled `$pipestatus` and
indexed from **1**, so the bash spelling `${PIPESTATUS[0]}` expands to the empty
string and the check reports nothing at all. Advice for reading a status that
silently yields no status is this section's own subject arriving in its own
prose, which is why the fix is to name the portable check rather than to correct
the spelling — `git status -sb` works in either shell and asserts the thing
actually wanted (the branch is tracking), where a pipe status only asserts that
one command in a pipeline exited zero.

`tools/install-git-hooks.sh` (run once per clone; worktrees share the parent
repo's hooks automatically) wires `tools/preflight.sh`'s fast gates as a
pre-push hook, so a push that would fail CI is rejected locally instead. What
it installs into the shared `.git/hooks/<name>` is neither the hook script nor
a symlink to it, but a copy of `tools/git-hooks/shim`, which resolves the
**pushing** working tree at run time and execs *that* tree's
`tools/git-hooks/<name>` (#1591). Before that, the installed hook was a symlink
into the MAIN checkout, so every worktree's push ran the main checkout's
script — meaning a hook change in a worktree did not govern that worktree's own
push, and anything under `tools/git-hooks/` was untestable from the branch that
changed it. PR #1590 rewrote the hook to bound its own runtime and its own push
ran the old unbounded one, hitting the exact defect it was fixing. Three
consequences worth knowing:

- **The shim is now the one link a `git pull` cannot update.** Changing
  `tools/git-hooks/shim` means re-running the installer; changing
  `tools/git-hooks/pre-push` does not. That is the right way round — the shim
  resolves a path and execs, and has no reason to change. The installer
  overwrites whatever it finds (an older symlink install, a hand-edited copy,
  a stale shim), so re-running it is always safe and a second run in a row
  installs nothing.
- **A revision that genuinely carries no hook passes, loudly**, on stderr —
  a bisect, or a branch predating the file, has no gate there to skip, and
  refusing would only make `git bisect` hostile.
- **A hook missing from the tree while `HEAD` still carries it refuses**, as
  does one present but not executable. That is a broken working tree, not a
  revision without the hook, and a gate skipped because a file was invisible is
  this repo's most-repeated failure shape.

`tools/lib/git-hooks_test.sh` covers both halves in throwaway repos (bare
origin + main checkout + linked worktree, pushing over a filesystem path), and
carries the mutation beside the assertion: one case installs the pre-#1591
symlink and pins the OPPOSITE outcome from the identical rig, so an assertion
that the worktree's refusing hook ran cannot be satisfied by a rig where
nothing ran at all.

The hook runs `tools/preflight.sh --changed --budget 540`, which scopes every gate
to the packages and web trees the push's diff actually touches (vs
`origin/main`), so a typical push finishes in seconds rather than re-running
the whole suite. A large or cross-cutting diff (or a `go.mod`/`go.sum` change,
which falls back to the full core suite) can still take a few minutes. Skip
once with `git push --no-verify`; run `tools/preflight.sh` manually (no
`--changed`) for the unscoped full gate.

**The budget is the part not to remove** (#1570). Scoping alone did not make
the hook fit: on a one-file diff under `core/adapters/inbound/agents/` the run
measured **621s** — go 250s, arch 16s, security 355s — against an automated
caller's 600s command budget, so the *caller* killed the tool call. That is the
worst available failure: no summary, no gate name, no exit code, the commit
already made and the push not sent, and the documented recovery (`--no-verify`)
then skips the sub-second gates nobody ran. Six of thirteen PRs in one day went
out that way. `--budget <seconds>` makes the run bound itself: each gate is
given whatever is left, a gate that outlives it is **killed and reported
`TIMEOUT` by name**, every gate behind it is reported **`NOT RUN`**, and both
exit non-zero. Neither is a `SKIP` — `SKIP` means "this diff cannot break it",
which is a finished answer; these two are the absence of one, and the closing
block lists them again after the summary. `PREPUSH_BUDGET` overrides the hook's
540s (`0` = unbounded, exactly the old behaviour); an unflagged
`tools/preflight.sh` is unbounded and unchanged. The bounded runner is
`tools/lib/gate-budget.sh` — pure bash 3.2, because `timeout(1)` is not on a
stock macOS and a gate that stops being bounded on the machines missing an
optional dependency is the same defect wearing a different hat. Its unit tests
plus the end-to-end mutation (a copy of `preflight.sh` with one gate replaced
by a `sleep`) are `tools/lib/gate-budget_test.sh`, in the `tools` gate.

Two things the budget makes visible that were previously invisible, both
measured while #1570 was being fixed:
- **`gosec` was running twice per module.** `-severity`/`-confidence` filter
  the *report*, not the analysis, so `security-scan.sh`'s informational pass
  and its gate pass were the same 172s scan of the same 263 files, twice. One
  `-fmt=json` run now answers both (`tools/lib/gosec-report.sh`), which took
  the security gate from **355s to 186s** with identical coverage and verdict.
  Nothing was narrowed — deduplicating a scan is not scanning less, which is
  why gosec was *not* scoped to changed packages instead. A report that will
  not parse, or whose own `.Stats.files` is 0, is refused rather than read as
  clean: a scan that read nothing produces "no High/High findings" too.
- **The `swift` gate can consume the whole budget on its own.** Its trigger
  includes `tools/preflight.sh` itself, and `SWIFT_SUITE_TIMEOUT` defaults to
  600s — equal to an automated caller's entire command budget, so the gate's
  own careful HUNG diagnosis could never print before the caller killed
  everything. Measured on this machine at `origin/main`, the suite reaches
  `SessionRowSnapshotTests.testRelayCloudOnline` and stops there (twice, at the
  identical point; that test passes in 0.157s in isolation) — #1523/#1530
  territory. Under `--budget` the outer bound fires first and names the gate,
  and the tree kill reaches through `script -q`'s separate session, leaving no
  orphaned `xctest` (measured at `--budget 45`).

The security gate is scoped twice over: its trigger regex decides whether the
scan runs at all, and `tools/security-scan.sh --changed` then picks which Go
modules and web trees to scan, matching each scanner against the files it
actually reads. Without that second layer a pure-Go push paid for an `npm
audit` of both web trees and was rejected by a pre-existing advisory it could
not have caused (#1213) — forcing `--no-verify`, which disables every other
gate too. Both layers read the same changed set, from
`tools/lib/changed-files.sh`; its unit tests run in the `tools` gate. That set
counts **untracked, non-ignored** files as well as committed, staged and
unstaged ones (#1591). It did not until then: `git diff` cannot see a file
that was never added and `--cached` only catches it once staged, so a
newly written script selected no gates at all while the function's own doc
said uncommitted work counted. Invisible to the pre-push hook — a file has to
be committed to be pushed — and wrong for every manual `--changed` run.

Two of the failure modes it won't catch: environment-specific timing flakes
that only manifest on loaded Linux CI runners (not this machine), and true
Linux-only bugs unless you pass `--linux`.

