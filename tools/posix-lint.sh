#!/usr/bin/env bash
# posix-lint.sh — prove every `#!/bin/sh` script in this repo is really POSIX sh.
#
# WHY THIS EXISTS (#1423). `site/install.sh` is what installs and uninstalls
# irrlicht on users' machines, via `curl -fsSL https://irrlicht.io/install.sh | sh`.
# On Debian and Ubuntu that `sh` is **dash**. A bashism in it fails on a new
# user's first command, before anything is installed that could report it.
#
# The coverage that existed before this gate was `dash -n` (tools/lib/
# install-uninstall_test.sh check 9). That is a *parser* check, and a parser
# accepts most bashisms as ordinary commands — measured, one bashism per file:
#
#     bashism                     dash -n   checkbashisms   shellcheck --shell=sh
#     [[ "$x" == 1 ]]             pass      CATCH           CATCH   (SC3010/SC3014)
#     arr=(a b c)                 CATCH     CATCH           CATCH   (SC3030/SC3054)
#     ${v,,}                      pass      CATCH           CATCH   (SC3059)
#     s+=b                        pass      CATCH           CATCH   (SC3024)
#     <(cmd)                      CATCH     CATCH           CATCH   (SC3001)
#     echo -e                     pass      CATCH           CATCH   (SC3037)
#     function greet() {}         CATCH     CATCH           CATCH   (SC2112)
#     source ./other.sh           pass      CATCH           CATCH   (SC3046)
#                                 ---------------------------------------------
#                                 3 of 8    8 of 8          8 of 8
#
# So a parser check alone misses `[[ ]]`, `${v,,}`, `+=`, `echo -e` and
# `source` — five of the most common bashisms, including the one the issue
# named first. This gate therefore runs BOTH kinds of check on every file:
#
#   1. a real POSIX shell's parser (`dash -n`, or `ash -n`), and
#   2. a static bashism linter that NAMES the violation
#      (`checkbashisms` preferred; `shellcheck --shell=sh` otherwise).
#
# WHERE IT RUNS. `.github/workflows/linux.yml`'s `build-test` job, on
# ubuntu-latest — the only runner where `/bin/sh` is genuinely dash and where
# shellcheck is preinstalled (ubuntu-24.04 image ships shellcheck 0.9.0;
# the macos-15 image ships none, which is why test.yml's macOS `go-test` job
# is deliberately NOT the host for this). Mirrored locally by
# `tools/preflight.sh --only posix`.
#
# NEVER SILENTLY PASSES. This gate exists because a green check that tested
# nothing is worse than no check. Three ways out are therefore hard failures,
# not skips: no POSIX shell available, no static linter available, and an
# empty file set. Each exits 2 with a message saying what to install.
#
# Scope: every tracked file whose FIRST LINE is a POSIX-sh shebang
# (`#!/bin/sh`, `#!/usr/bin/env sh`, with or without flags). Matching line 1
# only is deliberate — `tools/lib/install-uninstall_test.sh` is a bash file
# that writes `#!/bin/sh` stubs inside a heredoc, and a content grep would try
# to lint it as POSIX sh. `testdata/` is excluded because that fixture corpus
# is deliberately full of bashisms and is driven by the unit test, not by the
# gate — the same split `tools/skill-lint.sh` draws for its own corpus.
#
# Usage:
#   tools/posix-lint.sh              # lint every tracked #!/bin/sh script
#   tools/posix-lint.sh FILE...      # lint just these files (used by tests)
set -uo pipefail

FILES=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    # Print the whole leading comment block, the same self-documenting idiom
    # tools/skill-lint.sh and tools/preflight.sh use, so --help can't drift.
    -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
    --) shift; FILES+=("$@"); break ;;
    -*) echo "unknown arg: $1" >&2; exit 2 ;;
    *) FILES+=("$1"); shift ;;
  esac
done

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# is_posix_shebang <file> — true when line 1 selects a POSIX sh interpreter.
# Reads one line rather than slurping the file: some tracked "scripts" are
# large, and only the first line can carry a shebang anyway.
is_posix_shebang() {
  local first
  IFS= read -r first < "$1" 2>/dev/null || return 1
  case "$first" in
    '#!'*/sh|'#!'*/sh\ *|'#!'*env\ sh|'#!'*env\ sh\ *) return 0 ;;
    *) return 1 ;;
  esac
}

if [[ ${#FILES[@]} -eq 0 ]]; then
  cd "$ROOT_DIR" || exit 2
  # `git ls-files`, not `find`: `find .` descends into `.claude/worktrees/`,
  # which holds entire checkouts of this repo, and would lint every other
  # branch's scripts as if they were this one's. Staged files are index
  # entries, so a newly added script is covered on the push that adds it.
  while IFS= read -r f; do
    [[ "$f" == */testdata/* ]] && continue
    [[ -f "$f" ]] || continue
    is_posix_shebang "$f" && FILES+=("$f")
  done < <(git ls-files | LC_ALL=C sort -u)
fi

if [[ ${#FILES[@]} -eq 0 ]]; then
  # Not a pass. An empty set means the discovery and the repo disagree about
  # where POSIX scripts live — the same silent-green shape this gate is for.
  echo "posix-lint: no #!/bin/sh scripts found — nothing was checked" >&2
  exit 2
fi

# ─── Tool discovery ─────────────────────────────────────────────────────────
# Both kinds are mandatory. A missing tool is a hard failure with an install
# hint, never a skip: "the linter wasn't there" must not read as "the code is
# clean". This is the exact failure mode #1423 is about.

POSIX_SH=""
for candidate in dash ash; do
  if command -v "$candidate" >/dev/null 2>&1; then POSIX_SH="$candidate"; break; fi
done
if [[ -z "$POSIX_SH" ]]; then
  echo "posix-lint: no real POSIX shell found (looked for: dash, ash)." >&2
  echo "  Install one:  brew install dash   |   apt-get install -y dash" >&2
  echo "  Refusing to pass a run that could only have used bash-as-sh." >&2
  exit 2
fi

# checkbashisms is preferred over shellcheck because it reports bashisms and
# nothing else, so the gate's scope is fixed by the tool rather than by a
# filter this script would have to keep in sync. shellcheck is the fallback
# because it is preinstalled on GitHub's ubuntu runners, which makes the CI
# path free; there its output is filtered to the POSIX-compatibility codes so
# the gate cannot drag in unrelated style debt from scripts nobody has linted
# before. SC3xxx is the "In POSIX sh, X is undefined" family; SC2039 is the
# pre-0.7.2 catch-all that older shellchecks emit instead; SC2112 is the
# `function` keyword, which lives outside the SC3xxx range.
BASHISM_LINTER=""
if command -v checkbashisms >/dev/null 2>&1; then
  BASHISM_LINTER="checkbashisms"
elif command -v shellcheck >/dev/null 2>&1; then
  BASHISM_LINTER="shellcheck"
else
  echo "posix-lint: no static bashism linter found (looked for: checkbashisms, shellcheck)." >&2
  echo "  Install one:  brew install checkbashisms   |   apt-get install -y devscripts" >&2
  echo "                brew install shellcheck      |   apt-get install -y shellcheck" >&2
  echo "  A parser check alone misses [[ ]], \${v,,}, +=, echo -e and source" >&2
  echo "  (3 of 8 in the matrix at the top of this file), so refusing to pass." >&2
  exit 2
fi

POSIX_CODES='\[SC(3[0-9]{3}|2039|2112)\]'

# lint_bashisms <file> — print findings to stdout, return 1 when any were
# found (or when the linter could not be trusted to have looked).
#
# EVERY BRANCH DISTINGUISHES "clean" FROM "did not run". Writing this the
# obvious way — `out=$(linter "$f" | grep -E "$CODES")` and then testing
# `-z "$out"` — reproduces #1423 inside the gate built to fix it: if the
# linter or the filter fails to execute, the capture is empty, empty reads as
# clean, and the file is reported `ok`. That was not hypothetical; the first
# draft of this script did exactly that and printed `ALL PASS` over an
# installer carrying a deliberate `[[ ]]`. So the exit status of each tool is
# checked explicitly, and the filtering is done in bash rather than by piping
# into grep, which removes the second process that could fail unnoticed.
lint_bashisms() {
  local f="$1" raw rc line out=""

  if [[ "$BASHISM_LINTER" == "checkbashisms" ]]; then
    # -f forces the check regardless of what checkbashisms makes of the
    # shebang. It changes nothing for a plain `#!/bin/sh` (verified: those are
    # checked either way) — it is here so that THIS script's line-1 discovery
    # is the single thing deciding the gate's scope, rather than two
    # heuristics that can disagree about an unusual interpreter line.
    raw=$(checkbashisms -f "$f" 2>&1); rc=$?
    # 0 = clean, 1 = bashisms found. Anything else is checkbashisms failing.
    if [[ "$rc" -gt 1 ]]; then
      printf 'checkbashisms could not check this file (exit %s):\n%s\n' "$rc" "$raw"
      return 1
    fi
    [[ "$rc" -eq 0 ]] && return 0
    printf '%s\n' "$raw"
    return 1
  fi

  raw=$(shellcheck --shell=sh --format=gcc "$f" 2>&1); rc=$?
  # shellcheck: 0 = clean, 1 = findings. 2+ means it could not parse or run,
  # which must never be reported as a clean file.
  if [[ "$rc" -gt 1 ]]; then
    printf 'shellcheck could not check this file (exit %s):\n%s\n' "$rc" "$raw"
    return 1
  fi
  [[ "$rc" -eq 0 ]] && return 0
  # Filter to the POSIX-compatibility findings in bash — no pipe, so there is
  # no second process whose failure could look like "nothing matched".
  while IFS= read -r line; do
    [[ "$line" =~ $POSIX_CODES ]] && out+="$line"$'\n'
  done <<< "$raw"
  # rc was 1, so shellcheck DID find something; if none of it was a POSIX
  # violation the file is clean for this gate's purposes (general style debt
  # is deliberately out of scope — see the note on POSIX_CODES above).
  [[ -z "$out" ]] && return 0
  printf '%s' "$out"
  return 1
}

# ─── Run ────────────────────────────────────────────────────────────────────

echo "posix-lint: ${#FILES[@]} file(s); parser=$POSIX_SH, bashisms=$BASHISM_LINTER"

rc=0
for f in "${FILES[@]}"; do
  file_rc=0

  if ! parse_out=$("$POSIX_SH" -n "$f" 2>&1); then
    echo "FAIL [$POSIX_SH -n] $f" >&2
    printf '%s\n' "$parse_out" >&2
    file_rc=1
  fi

  if ! bashism_out=$(lint_bashisms "$f"); then
    echo "FAIL [$BASHISM_LINTER] $f" >&2
    printf '%s\n' "$bashism_out" >&2
    file_rc=1
  fi

  if [[ "$file_rc" -eq 0 ]]; then
    echo "  ok  $f"
  else
    rc=1
  fi
done

if [[ "$rc" -ne 0 ]]; then
  echo >&2
  echo "posix-lint: FAILED — the above are bashisms in a #!/bin/sh script." >&2
  echo "  These ship to users running dash (Debian/Ubuntu /bin/sh)." >&2
  exit 1
fi

echo "posix-lint: ALL PASS"
