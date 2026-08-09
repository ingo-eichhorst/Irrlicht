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
#      — EVERY one that is installed, not the first one found, because the two
#      do not agree: checkbashisms accepts `local`, `set -o pipefail` and
#      `echo -n`, which shellcheck rejects (SC3043/SC3040/SC3037). CI has
#      shellcheck only, so preferring the other one locally would let a
#      developer's preflight pass a diff that CI rejects.
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
  #
  # `-z` with `core.quotePath=off`, not a plain line read: under the default
  # `core.quotePath=true` git C-quotes any path with a non-ASCII byte, so
  # `inställ.sh` is listed as `"inst\303\244ll.sh"` — a string that is not a
  # real path. `[[ -f ]]` then rejects it and the file is dropped from the
  # walk with no message, which is a bashism-carrying script reported as
  # `ALL PASS`. The empty-set guard below cannot catch that, because it only
  # fires when EVERY file is missed. NUL delimiting also survives newlines and
  # spaces in paths (this repo tracks one with a space).
  while IFS= read -r -d '' f; do
    [[ "$f" == */testdata/* ]] && continue
    if [[ ! -f "$f" ]]; then
      # Loud, not silent: a tracked path the walk cannot open is exactly the
      # blind spot above, and the whole point of this gate is that "not
      # looked at" must never render as "clean".
      echo "posix-lint: skipping $f (tracked but not a regular file)" >&2
      continue
    fi
    is_posix_shebang "$f" && FILES+=("$f")
  done < <(git -c core.quotePath=off ls-files -z | LC_ALL=C sort -z -u)
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

# EVERY available linter runs, rather than the first one found.
#
# The tempting design — prefer one, fall back to the other — makes the gate's
# strength depend on which machine it is running on, and the two tools do NOT
# agree. checkbashisms accepts `local x=1`, `set -o pipefail` and `echo -n`,
# all of which shellcheck rejects (SC3043 / SC3040 / SC3037) and all of which
# an installer accretes. CI has shellcheck and not checkbashisms; a developer
# who has installed checkbashisms would then have had the weaker checker
# silently preferred, `tools/preflight.sh --only posix` would pass, and
# linux.yml would go red on the push — the round-trip preflight exists to
# prevent. Running both is monotone: the local result can be stricter than
# CI's, never weaker.
#
# shellcheck's output is filtered to the POSIX-compatibility codes so the gate
# cannot drag in unrelated style debt from scripts nobody has linted before.
# SC3xxx is the "In POSIX sh, X is undefined" family; SC2039 is the pre-0.7.2
# catch-all that older shellchecks emit instead; SC2112/SC2113 are the two
# `function` keyword spellings, which live outside the SC3xxx range.
BASHISM_LINTERS=()
command -v shellcheck    >/dev/null 2>&1 && BASHISM_LINTERS+=("shellcheck")
command -v checkbashisms >/dev/null 2>&1 && BASHISM_LINTERS+=("checkbashisms")
if [[ ${#BASHISM_LINTERS[@]} -eq 0 ]]; then
  echo "posix-lint: no static bashism linter found (looked for: checkbashisms, shellcheck)." >&2
  echo "  Install one:  brew install checkbashisms   |   apt-get install -y devscripts" >&2
  echo "                brew install shellcheck      |   apt-get install -y shellcheck" >&2
  echo "  A parser check alone misses [[ ]], \${v,,}, +=, echo -e and source" >&2
  echo "  (3 of 8 in the matrix at the top of this file), so refusing to pass." >&2
  exit 2
fi

POSIX_CODES='\[SC(3[0-9]{3}|2039|211[23])\]'
# SC1xxx is shellcheck's parse/IO family. SC1072/SC1073/SC1088 make it ABANDON
# analysis of the file, so it can exit 1 having emitted nothing but SC1xxx —
# and a filter that keeps only POSIX codes would then see an empty result and
# call the file clean. That is "the tool didn't look" wearing "the code is
# fine", the shape this whole gate exists to remove, so it is treated as a
# failure to check rather than filtered away.
PARSE_ABORT_CODES='\[SC1[0-9]{3}\]'

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
  local f="$1" linter rc=0
  for linter in "${BASHISM_LINTERS[@]}"; do
    lint_with "$linter" "$f" || rc=1
  done
  return "$rc"
}

# lint_with <linter> <file> — one linter's verdict on one file.
lint_with() {
  local linter="$1" f="$2" raw rc line out=""

  if [[ "$linter" == "checkbashisms" ]]; then
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
  # A parse abort must not be filtered into silence — see PARSE_ABORT_CODES.
  if [[ "$raw" =~ $PARSE_ABORT_CODES ]]; then
    printf 'shellcheck could not check this file (parse error; analysis abandoned):\n%s\n' "$raw"
    return 1
  fi
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

# Name every checker that ran. Which ones are present varies by host — CI has
# shellcheck only — so a run that cannot be read back to "what actually
# looked at this" is a run whose green means nothing in particular.
echo "posix-lint: ${#FILES[@]} file(s); parser=$POSIX_SH, bashisms=${BASHISM_LINTERS[*]}"
if [[ " ${BASHISM_LINTERS[*]} " != *" shellcheck "* ]]; then
  # Not a failure — checkbashisms alone is still far stronger than the parser
  # check that preceded this gate. But it is weaker than CI's checker, and a
  # local green that CI will contradict should say so at the time, not on the
  # push.
  echo "posix-lint: NOTE — shellcheck is absent, so this run is WEAKER than CI's." >&2
  echo "  checkbashisms accepts 'local', 'set -o pipefail' and 'echo -n'; shellcheck rejects all three." >&2
  echo "  Install it to match the gate that guards main:  brew install shellcheck" >&2
fi

rc=0
for f in "${FILES[@]}"; do
  file_rc=0

  if ! parse_out=$("$POSIX_SH" -n "$f" 2>&1); then
    echo "FAIL [$POSIX_SH -n] $f" >&2
    printf '%s\n' "$parse_out" >&2
    file_rc=1
  fi

  if ! bashism_out=$(lint_bashisms "$f"); then
    echo "FAIL [bashisms] $f" >&2
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
