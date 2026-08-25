#!/usr/bin/env bash
# state-vocabulary-lint.sh — refuse when a file hand-types an INCOMPLETE list
# of the session-state vocabulary.
#
# WHY THIS EXISTS (#1804, recommended and prototyped by #1798)
#
# The lifecycle vocabulary held three values for two years, so "name all of
# them" and "name three of them" were the same act, and dozens of sites did it
# — prose, HTML tables, CLI help strings, `case` arms, test fixtures. #1796
# added a fourth value and every one of those sites became a claim that is
# quietly false. That is worse than no claim: a stale enumeration tells the
# next reader not to look.
#
# The epic shipped FOUR separate defects of exactly this shape, each complete
# under the old vocabulary and silently incomplete under the new one:
#   - `classifyAgentDone` enumerated the values it promotes *from*, so the
#     new value could never reach `ready`;
#   - `refreshStaleSessions`' switch matched neither arm for the new value, so
#     `sessionErrorHoldTimeout` could never fire;
#   - `warnUnknownStateOnce` told the user the build "knows only %s/%s/%s";
#   - `irrlicht-ls --state` rejected a value the daemon emits.
# Review caught them one at a time, after each had shipped. This is the check
# that catches the fifth.
#
# WHAT COUNTS AS A SITE, AND WHY THE THRESHOLD IS THREE
#
# A SITE is one line that names at least STATE_VOCAB_MIN_NAMED of the
# canonical values but NOT all of them. Both halves are load-bearing.
#
#   the threshold   #1798 prototyped this check and measured the threshold
#                   against the tree of the day. At two, roughly half the hits
#                   are false positives, because naming exactly the first two
#                   values is a DELIBERATE and correct partition — "does this
#                   session occupy a concurrency slot" — spelled at seven
#                   sites (see session.HasWorkInFlight). At three that whole
#                   class drops out. The cost is stated rather than hidden:
#                   this check does NOT catch `history_tracker.statePriority`,
#                   which names two values plus a `default`, and it would have
#                   caught only two of the four defects above. It complements
#                   review; it does not replace it.
#
#   not all of them An enumeration naming the ENTIRE vocabulary is current by
#                   construction, so flagging it would be pure noise. Naming a
#                   proper subset is the defect. This is also what makes the
#                   check self-maintaining across the NEXT addition: the day a
#                   fifth value lands, every site naming today's four becomes
#                   a proper subset and lights up — which is precisely the
#                   revisit-list nobody had when the fourth one landed.
#
# THE VOCABULARY IS DERIVED, NEVER RETYPED
#
# It is read out of core/domain/session/session.go's `canonicalStates`, the
# single declaration AGENTS.md points every caller at. A linter that hardcoded
# the values would be the exact defect it exists to find and would go stale at
# the same moment as everything else. When the vocabulary cannot be parsed
# this script REFUSES rather than scanning for nothing: absence of a finding
# and inability to look must never print the same thing.
#
# WAIVERS
#
# Some sites name a proper subset on purpose. They are recorded one per line,
# with a reason, in tools/state-vocabulary-lint.waivers. A waiver is keyed on
# a path and suppresses every site in that file. Both directions fail:
#   - a flagged site with no waiver      -> FAIL (a new stale vocabulary)
#   - a waiver matching no flagged site  -> FAIL (a stale waiver)
# The second is what stops the list rotting into a permanent exemption nobody
# re-reads — the same reason tools/bash-lint.sh refuses an exclusion that
# matched nothing and tools/lib/shell-lib-suite.sh refuses a skip that names
# no file.
#
# WHERE THIS RUNS
#
# Two callers, one script, deliberately: .github/workflows/linux.yml's "Lint
# the session-state vocabulary" step and tools/preflight.sh's `tools` gate both
# invoke THIS file, so CI and the pre-push hook judge a run by one
# implementation rather than by two that can disagree — the reason test.yml and
# macos-swift.yml both give for sharing their runners. The CI step is not
# redundant with the gate's unit tests: those prove the lint WORKS, and only a
# run over the real tree proves the repo PASSES it. Until #1804 added that step
# the only thing standing between a stale enumeration and `main` was a pre-push
# hook, which `git push --no-verify` disables.
#
# awk PORTABILITY. macOS runs BWK awk; the Linux CI runner's `awk` is mawk.
# Measured on the current tree — BWK awk 20200816, gawk, and mawk 1.3.4 each
# report the same 55 sites and the same 56 `--list` lines:
#   for A in /usr/bin/awk "$(command -v gawk)" "$(command -v mawk)"; do
#     D=$(mktemp -d); ln -s "$A" "$D/awk"
#     PATH="$D:$PATH" tools/state-vocabulary-lint.sh --list | grep -c .
#   done
# A divergence could not go quiet anyway: an awk that matched LESS would retire
# waivers, and a waiver matching no site is itself an error here, so the
# two-directional waiver check doubles as a cross-awk conformance check.
#
# Usage:
#   tools/state-vocabulary-lint.sh                  # scan the repo
#   tools/state-vocabulary-lint.sh --list           # print every site, waived or not
#   tools/state-vocabulary-lint.sh --waivers PATH   # use another waiver file
#   tools/state-vocabulary-lint.sh --source PATH    # read the vocabulary elsewhere
#   tools/state-vocabulary-lint.sh -- FILE...       # scan these files only (tests)
#
# Exit 0 and print OK when every flagged site is waived and every waiver is
# live. Exit 1 and name each site when one is unwaived or a waiver is stale —
# an ordinary finding. Exit 2 (a REFUSAL, not a finding) when the check could
# not run at all: no git repository, an unparseable vocabulary, an unreadable
# waiver file, an exclusion that matched nothing, or a scan that reached zero
# files.
set -uo pipefail

# The threshold, in a named variable rather than buried in the awk program, so
# the header above and the code below cannot disagree about the number.
STATE_VOCAB_MIN_NAMED=3

# The file that declares the vocabulary, relative to the repo root.
STATE_VOCAB_SOURCE='core/domain/session/session.go'

# Path-prefix exclusions, applied before anything else, each with the reason
# it is here. These are NOT waivers: a waiver is a judgement about a site that
# was examined, while these are trees the check has no business reading.
# Every one is existence-checked below — an exclusion that stopped excluding
# reads from the log exactly like coverage.
STATE_VOCAB_EXCLUDE=(
  'replaydata/'
  'frozen agent recordings — every transcript quotes source that was correct when it was recorded, and CI guards their deletion'
  'CHANGELOG.md'
  'historical release notes — a shipped entry describing the vocabulary of its own release is a true statement about that release'
  'site/docs/changelog.html'
  'the rendered form of the same release history'
  'site/docs/roadmap.html'
  'dated roadmap entries, each describing what a release was aiming at when it was written — the same "do not correct history" rule as the changelog'
  'tools/lib/testdata/'
  "this gate's own fixtures deliberately enumerate; they are driven by tools/lib/state-vocabulary-lint_test.sh, the same split every other linter here draws"
  'tools/state-vocabulary-lint.waivers'
  "this gate's own bookkeeping — a waiver's REASON routinely has to quote the subset it is excusing, and scanning it would make every explanation a finding (three did, on the first run)"
)

# Binary and asset extensions: grepping these produces noise, not findings.
STATE_VOCAB_BINARY_EXT='png|jpg|jpeg|gif|icns|pdf|webp|ico|woff|woff2|ttf|otf|zip|mp4|mov|wav|aiff'

# state_vocab_read <path-to-session.go>
# Print the canonical values, one per line, in declaration order.
# Returns 2 (refusal) when the vocabulary cannot be established.
#
# Two steps, because the slice literal names CONSTANTS and this script needs
# their string values: read `canonicalStates = []string{...}` for the constant
# identifiers, then resolve each against its own declaration in the same file.
# Resolving rather than pattern-matching `State[A-Z]` directly is deliberate —
# that pattern also matches the substring `StateNotCompacting` inside
# `CompactionStateNotCompacting`, which is not a lifecycle value at all.
state_vocab_read() {
  local src="$1"

  if [[ ! -r "$src" ]]; then
    echo "REFUSE: state-vocabulary-lint — cannot read $src, so the vocabulary is unknown." >&2
    return 2
  fi

  local literal
  literal=$(LC_ALL=C sed -n 's/.*canonicalStates *= *\[\]string{\([^}]*\)}.*/\1/p' "$src" | head -1)
  if [[ -z "$literal" ]]; then
    echo "REFUSE: state-vocabulary-lint — no \`canonicalStates = []string{...}\` literal in $src." \
         "The declaration moved; point this script at its new home rather than letting it guess." >&2
    return 2
  fi

  local ident value count=0 out=""
  for ident in $(printf '%s' "$literal" | tr ',' '\n' | tr -d ' \t'); do
    [[ -n "$ident" ]] || continue
    value=$(LC_ALL=C sed -n "s/^[[:space:]]*${ident}[[:space:]]*=[[:space:]]*\"\([a-z_]*\)\".*/\1/p" "$src" | head -1)
    if [[ -z "$value" ]]; then
      echo "REFUSE: state-vocabulary-lint — $ident is in canonicalStates but has no \`$ident = \"...\"\` declaration in $src." >&2
      return 2
    fi
    out+="$value"$'\n'
    count=$((count + 1))
  done

  # At or below the threshold the "proper subset of size >= N" rule is
  # vacuous — nothing could ever qualify — so a truncated parse would scan the
  # whole repo and report a serene zero. Refuse instead.
  if (( count <= STATE_VOCAB_MIN_NAMED )); then
    echo "REFUSE: state-vocabulary-lint — parsed only $count value(s) from $src;" \
         "the \"names >= $STATE_VOCAB_MIN_NAMED but not all\" rule cannot match anything at that size." >&2
    return 2
  fi

  printf '%s' "$out"
  return 0
}

# state_vocab_files <vocab-space-separated>
# Print the tracked+untracked corpus, one path per line, after exclusions.
# Returns 2 when the corpus is empty or an exclusion matched nothing.
#
# `git ls-files`, not `find`: `find .` descends into `.claude/worktrees/`,
# which holds entire checkouts of this repo, and would lint every other
# branch's files as if they were this one's (the reason tools/posix-lint.sh
# gives for the same choice). Untracked files are included because a file that
# newly enumerates the vocabulary is exactly the one this gate most needs to
# see on the commit that adds it.
state_vocab_files() {
  local all kept i pat before after
  all=$( { git -c core.quotePath=off ls-files
           git -c core.quotePath=off ls-files --others --exclude-standard --full-name -- :/
         } 2>/dev/null | LC_ALL=C sort -u )
  if [[ -z "$all" ]]; then
    echo "REFUSE: state-vocabulary-lint — the file corpus is empty, so the scan would have looked at nothing." >&2
    return 2
  fi

  kept="$all"
  for ((i = 0; i < ${#STATE_VOCAB_EXCLUDE[@]}; i += 2)); do
    pat="${STATE_VOCAB_EXCLUDE[i]}"
    before=$(printf '%s\n' "$kept" | grep -c .)
    kept=$(printf '%s\n' "$kept" | LC_ALL=C grep -vF -- "$pat")
    after=$(printf '%s\n' "$kept" | grep -c .)
    if (( before == after )); then
      echo "REFUSE: state-vocabulary-lint — the exclusion '$pat' matched no file." >&2
      echo "  Its stated reason was: ${STATE_VOCAB_EXCLUDE[i + 1]}" >&2
      echo "  Either that tree moved or it is gone; an exclusion that stopped excluding must not read as coverage." >&2
      return 2
    fi
  done

  kept=$(printf '%s\n' "$kept" | LC_ALL=C grep -Eiv "\\.($STATE_VOCAB_BINARY_EXT)\$")

  # `git grep`, NOT `grep`. The choice is load-bearing, not stylistic. It does
  # two jobs here.
  #
  # (1) BINARY REJECTION, because the extension list above cannot be complete —
  # this repo tracks four extensionless compiled artefacts (core/replay,
  # split-shards, tools/seed-demo-sessions/, tools/wsload/), and awk ABORTS the
  # whole batch on one rather than skipping it. That surfaces as a refusal (no
  # verdict at all), so it has to be filtered rather than tolerated.
  #
  # (2) NARROWING, because a file containing none of the words cannot hold a
  # site and the awk pass is the expensive half. How much it removes is NOT
  # restated here — the gate prints the surviving count in its own summary line
  # every run ("scanned N file(s)"), so the number a reader sees is always the
  # measured one. An earlier draft of this comment claimed a ~250-file survivor
  # set; the real figure was four times that, because `error` is a
  # near-ubiquitous English word and the narrowing is far weaker than it looks.
  #
  # WHY NOT `grep -I`. Every implementation's "is this text" heuristic is its
  # own, and they disagree about a file this repo actually tracks:
  # platforms/web/irrlicht.js holds a literal NUL at offset 67966 (a `'\0'`
  # string separator). GNU grep 3.12 reads far enough to see it and calls the
  # file binary — in EVERY locale, measured — while the ugrep that shadows
  # `grep` on the author's machine does not. So the corpus differed by one real
  # source file between a laptop and the Linux CI runner, with the LOCAL run
  # the permissive one: the site was flagged and waived locally and silently
  # absent in CI. Caught on this gate's first CI run, and only because the
  # waiver for that file then matched nothing — the two-directional waiver
  # check converted a silent under-scan into a loud failure, which is the whole
  # reason it fails in both directions.
  #
  # git's heuristic is git's OWN code (NUL within the first 8000 bytes), so it
  # is identical on every platform and locale, and git is already a hard
  # dependency here. Measured on this tree: it keeps irrlicht.js and rejects
  # all four compiled artefacts (each has a NUL by offset 5). Exactly one
  # tracked text file has a NUL past the first 1 KiB and every binary has one
  # inside the first 100 bytes, so the classes separate with room to spare.
  #
  # The pattern is built from the vocabulary rather than typed, so it cannot
  # narrow away a file that mentions only a NEWLY added state.
  local anyword
  anyword=$(printf '%s\n' "$1" | tr ' ' '|' | sed 's/|$//')
  kept=$(printf '%s\n' "$kept" | tr '\n' '\0' \
    | xargs -0 -n 200 git grep -I -l --untracked -E "$anyword" -- 2>/dev/null)

  if [[ -z "$kept" ]]; then
    echo "REFUSE: state-vocabulary-lint — every file was excluded; nothing was scanned." >&2
    return 2
  fi
  printf '%s\n' "$kept"
  return 0
}

# state_vocab_sites <vocab-space-separated> <file>...
# Print one `path:line:text` record per flagged site; nothing when clean.
#
# The vocabulary crosses into awk SPACE-separated, not newline-separated: the
# awk shipped with macOS rejects a newline inside a `-v` assignment outright
# ("awk: newline in string"), and it does so on stderr while still exiting in a
# way a careless caller reads as a clean scan. Measured here during
# development — the run printed "0 site(s)" over a tree with dozens of them.
# `xargs` propagates a non-zero status from the command it ran, so the caller
# checks it (see state_vocab_lint) rather than trusting empty output.
state_vocab_sites() {
  local vocab="$1"; shift
  [[ $# -gt 0 ]] || return 0
  printf '%s\0' "$@" | xargs -0 -n 150 awk -v VOCAB="$vocab" -v MIN="$STATE_VOCAB_MIN_NAMED" '
    function cap(s) { return toupper(substr(s, 1, 1)) substr(s, 2) }

    BEGIN {
      N = split(VOCAB, V, " ")
      while (N > 0 && V[N] == "") N--          # split() leaves a trailing empty field
      if (N < 2) { print "state-vocabulary-lint: awk received an unusable vocabulary" > "/dev/stderr"; exit 2 }
      ANY = "("
      for (i = 1; i <= N; i++) ANY = ANY (i > 1 ? "|" : "") V[i]
      ANY = ANY ")"
      # The CONTENTS of a bracket expression, not a bracket expression: these
      # are interpolated as [W] and [^W], and nesting one inside the other
      # ("[^[A-Za-z0-9_]]") silently becomes "any non-word char followed by a
      # literal ]", which matches almost nothing. Measured: with the nested
      # spelling the prose-run arm never fired at all, and the scan reported
      # 27 sites where it should have reported far more.
      W = "A-Za-z0-9_"
      # A separator that joins two names into ONE enumeration: a comma, slash
      # or pipe, or a conjunction. Anything else between them is prose that
      # merely happens to contain two of the words.
      SEP = "([ \t]*[,/|][ \t]*|[ \t]*,?[ \t]+(and|or)[ \t]+)"
    }

    # A code TOKEN: the value as a string literal, a markup element body, a
    # Go/Swift constant, or an identifier component (badge-working, .ready).
    function token(line, v,   C) {
      C = cap(v)
      if (line ~ ("[\"'\''`]" v "[\"'\''`]"))             return 1
      if (line ~ (">" v "<"))                             return 1
      if (line ~ ("(^|[^" W "])State" C "($|[^" W "])"))  return 1
      if (line ~ ("[.:_-]" v "($|[^" W "])"))             return 1
      return 0
    }

    # A prose RUN: the value adjacent to another value with only a list
    # separator between them, in either direction.
    function run(line, v) {
      if (line ~ ("(^|[^" W "])" v SEP ANY "($|[^" W "])")) return 1
      if (line ~ ("(^|[^" W "])" ANY SEP v "($|[^" W "])")) return 1
      return 0
    }

    {
      # Cheap gate first: a line cannot name MIN distinct values without
      # containing MIN of them as substrings, and index() is far cheaper than
      # the ~7 regexes per value below. Measured on this repo: 14.0s -> 1.6s.
      #
      # CASE-FOLDED, and that is not a nicety. The `StateWaiting` constant form
      # contains "waiting" only after folding, so a case-SENSITIVE pre-check
      # skips every Go/Swift constant site — and it does so silently, changing
      # the verdict while still exiting 0. It did exactly that when this
      # optimisation was first written: the repo scan dropped
      # session_detector_activity.go and reported its waiver as stale, which is
      # the only reason the bug was noticed at all. A fixture pins it now
      # (three-of-four-constants.go).
      lower = tolower($0)
      cheap = 0
      for (i = 1; i <= N; i++) if (index(lower, V[i])) cheap++
      if (cheap < MIN) next

      c = 0
      for (i = 1; i <= N; i++) if (token($0, V[i]) || run($0, V[i])) c++
      if (c >= MIN && c < N) printf "%s:%d:%s\n", FILENAME, FNR, $0
    }
  '
}

# state_vocab_lint <waiver-file> <mode> [file...]
# The whole check. 0 clean / 1 findings / 2 refusal.
state_vocab_lint() {
  local waiver_file="$1" mode="$2"; shift 2
  local vocab files sites rc=0 shown

  vocab=$(state_vocab_read "${STATE_VOCAB_SOURCE_PATH:-$STATE_VOCAB_SOURCE}") || return 2
  vocab=$(printf '%s' "$vocab" | tr '\n' ' ' | sed 's/ *$//')
  shown="$vocab"

  if [[ $# -gt 0 ]]; then
    files=$(printf '%s\n' "$@")
  else
    files=$(state_vocab_files "$vocab") || return 2
  fi

  local count
  count=$(printf '%s\n' "$files" | grep -c .)
  if (( count == 0 )); then
    echo "REFUSE: state-vocabulary-lint — no files to scan." >&2
    return 2
  fi

  local scan_rc=0
  # shellcheck disable=SC2046  # Splitting is the point: `files` is one path per line, and IFS is narrowed to a newline first, so a path containing a space still arrives as ONE argument.
  sites=$(IFS=$'\n'; state_vocab_sites "$vocab" $(printf '%s\n' "$files")) || scan_rc=$?
  if (( scan_rc != 0 )); then
    # An awk that died and an awk that found nothing both produce empty
    # output. Only the status tells them apart, so it is checked rather than
    # assumed — this exact case (macOS awk rejecting the vocabulary) reported
    # a clean tree during development.
    echo "REFUSE: state-vocabulary-lint — the scan itself failed (status $scan_rc); no verdict was reached." >&2
    return 2
  fi

  if [[ "$mode" == "list" ]]; then
    [[ -n "$sites" ]] && printf '%s\n' "$sites"
    echo "state-vocabulary-lint: vocabulary [$shown]; scanned $count file(s); $(printf '%s' "$sites" | grep -c .) site(s)"
    return 0
  fi

  # Waivers: `path` then whitespace then a reason. `#` comments and blanks out.
  local waived=""
  if [[ ! -r "$waiver_file" ]]; then
    echo "REFUSE: state-vocabulary-lint — cannot read the waiver file $waiver_file." \
         "Without it every site reads as unwaived, which is a different answer from \"clean\"." >&2
    return 2
  fi
  waived=$(LC_ALL=C sed -e 's/#.*//' -e 's/[[:space:]].*$//' "$waiver_file" | grep -v '^[[:space:]]*$')

  # The set of paths that actually have a flagged site, derived ONCE so both
  # directions below decide membership by the same rule. They did not always:
  # direction 1 compared whole lines while direction 2 grepped for `path:` as a
  # substring, so a waiver could stay "live" because its path was a suffix of a
  # different flagged path, or because some flagged line's TEXT happened to
  # contain it. Two directions of one question answered by two matchers is the
  # shape that lets a waiver rot while reading green.
  local flagged_paths="" site path
  while IFS= read -r site; do
    [[ -n "$site" ]] || continue
    flagged_paths+="${site%%:*}"$'\n'
  done <<<"$sites"
  flagged_paths=$(printf '%s' "$flagged_paths" | LC_ALL=C sort -u)

  # Direction 1 — a flagged site with no waiver.
  local unwaived=""
  while IFS= read -r site; do
    [[ -n "$site" ]] || continue
    path="${site%%:*}"
    printf '%s\n' "$waived" | grep -qxF -- "$path" || unwaived+="$site"$'\n'
  done <<<"$sites"

  if [[ -n "$unwaived" ]]; then
    rc=1
    echo "FAIL: state-vocabulary-lint — $(printf '%s' "$unwaived" | grep -c .) site(s) hand-type an INCOMPLETE state vocabulary:" >&2
    printf '%s' "$unwaived" | cut -c1-200 | sed 's/^/  /' >&2
    echo "Each names >= $STATE_VOCAB_MIN_NAMED of [$shown] but not all of them." \
         "Complete the list, derive it from session.CanonicalStates(), or record the site in $waiver_file with a reason." >&2
  fi

  # Direction 2 — a waiver that matches no flagged site.
  local stale="" wp
  while IFS= read -r wp; do
    [[ -n "$wp" ]] || continue
    printf '%s\n' "$flagged_paths" | grep -qxF -- "$wp" || stale+="$wp"$'\n'
  done <<<"$waived"

  if [[ -n "$stale" ]]; then
    rc=1
    echo "FAIL: state-vocabulary-lint — $(printf '%s' "$stale" | grep -c .) waiver(s) match no flagged site any more:" >&2
    printf '%s' "$stale" | sed 's/^/  /' >&2
    echo "The site was fixed or the file moved — drop the entry." \
         "A waiver that stopped matching and a clean run must not look the same." >&2
  fi

  (( rc == 0 )) && echo "OK: state-vocabulary-lint — vocabulary [$shown]; scanned $count file(s); $(printf '%s' "$sites" | grep -c .) site(s), all waived."
  return "$rc"
}

# Only run the CLI form when executed directly — tools/lib/*_test.sh sources
# this file to drive the functions against fixtures.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
    echo "REFUSE: state-vocabulary-lint — not inside a git repository" >&2
    exit 2
  }
  cd "$REPO_ROOT" || { echo "REFUSE: state-vocabulary-lint — cannot cd to $REPO_ROOT" >&2; exit 2; }
  WAIVERS=tools/state-vocabulary-lint.waivers
  MODE=check
  EXPLICIT=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      # Re-print the header block, the self-documenting idiom tools/bash-lint.sh
      # and tools/posix-lint.sh use, so --help cannot drift from the contract.
      -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
      --list)    MODE=list; shift ;;
      --waivers) shift; WAIVERS="${1:-}"; shift ;;
      --source)  shift; STATE_VOCAB_SOURCE_PATH="${1:-}"; shift ;;
      --)        shift; EXPLICIT+=("$@"); break ;;
      -*)        echo "REFUSE: state-vocabulary-lint — unknown argument $1" >&2; exit 2 ;;
      *)         EXPLICIT+=("$1"); shift ;;
    esac
  done
  state_vocab_lint "$WAIVERS" "$MODE" ${EXPLICIT+"${EXPLICIT[@]}"}
  exit $?
fi
