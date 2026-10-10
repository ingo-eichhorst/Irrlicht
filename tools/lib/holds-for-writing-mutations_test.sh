#!/usr/bin/env bash
# holds-for-writing-mutations_test.sh — committed mutation fixture for issue
# #2079's per-pid writer probe, processlifecycle.HoldsForWriting
# (core/adapters/inbound/agents/processlifecycle/process_darwin.go and
# process_linux.go).
#
# WHY THIS FILE EXISTS. #2079 adds a probe the codex, dsh and muse
# SharedPIDOwner probes now ask instead of the whole-process-table
# transcript-writer scan. Its unit tests (holdsforwriting_test.go and
# holdsforwriting_darwin_test.go) first failed to build, because the method did
# not exist. Their rows pin properties a working probe has by construction, so
# each one's red evidence is a mutation of the line it protects:
#
#   1. the darwin mode rule (lsofFD.Writes, shared with WriterOf) is ignored,
#      so any lsof FD mode counts → the read-only row goes red: a pid holding
#      the file read-only "holds it for writing";
#   2. lsof's -a is dropped → the unheld-file row goes red: lsof ORs "-p pid"
#      with the path and lists every file the pid has open, so the holder's
#      other writers answer for a file it never opened;
#   3. the darwin non-positive-pid guard is dropped → the no-child rows go red:
#      `lsof -a -p -1` is a usage error that exits 1, which reads as "nothing to
#      report", and a child is started for a pid that names nothing;
#   4. the measured figure loses its regenerate: line → the probe-cost anchor
#      #2079 added goes red, so that anchor really reaches the doc comment;
#   5. (linux) the mode rule is ignored → the read-only row goes red;
#   6. (linux) an fdinfo with no flags line reads as "not held" → the
#      could-not-look row for it goes red;
#   7. (linux) an unreadable /proc/<pid>/fd reads as "not held" → the EACCES
#      row goes red (it needs a non-root user, and skips by name as root).
#
# 1-4 need darwin (holdsForWritingVia is darwin-only, and only darwin starts an
# lsof child). 5-7 need linux. test.yml runs this file on macos, so 5-7 re-run
# only on a linux host, e.g. a golang:1.26-bookworm container as a non-root
# user; on darwin they are reported as not run, by name.
#
# tools/mutate.sh owns the mechanics this file must not re-improvise: the
# stale/ambiguous-anchor guards, and the byte-for-byte restore that never
# touches git state. Modeled on codex-shared-pid-owner-mutations_test.sh.

set -uo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../.." && pwd)"
MUTATE_SH="$REPO_ROOT/tools/mutate.sh"

need() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "FAIL: holds-for-writing-mutations — $tool not found" >&2
    exit 1
  fi
  return 0
}
quote_output() { sed 's/^/      | /'; return 0; }
need go
need git

if [[ ! -x "$MUTATE_SH" ]]; then
  echo "FAIL: holds-for-writing-mutations — $MUTATE_SH is missing or not executable" >&2
  exit 1
fi

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  echo "holds-for-writing-mutations: CANNOT RUN — the worktree is dirty, and mutate.sh needs a" >&2
  echo "  clean tree for its post-restore check to mean anything. Commit or clean up, then:" >&2
  echo "    bash tools/lib/holds-for-writing-mutations_test.sh" >&2
  if [[ -n "${CI:-}" || -n "${MUTATION_FIXTURES_STRICT:-}" ]]; then
    echo "  (failing rather than skipping: CI/strict mode, where a skip is indistinguishable" >&2
    echo "   from a pass and this gate is the only thing re-running this mutation)" >&2
    exit 1
  fi
  echo "  (skipped locally; set MUTATION_FIXTURES_STRICT=1 to make this a failure)" >&2
  exit 0
fi

fails=0

assert_go_test_goes_red() {
  local label="$1" file="$2" anchor="$3" replacement="$4" pkg="$5" run="$6" want="$7"
  local out rc

  out="$(cd "$REPO_ROOT" && "$MUTATE_SH" "$file" "$anchor" "$replacement" \
    bash -c "go test $pkg -run '$run' -race -count=1 -v 2>&1; echo GO_TEST_RC=\$?" 2>&1)"
  rc=$?

  if [[ $rc -ne 0 ]]; then
    echo "FAIL: $label — mutate.sh refused (exit $rc). A STALE or ambiguous anchor means the"
    echo "      guard's source moved and this fixture needs updating — it does NOT mean the"
    echo "      guard is fine."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -q 'GO_TEST_RC=0' <<<"$out"; then
    echo "FAIL: $label — the test stayed GREEN under the mutation, so the guard does not reach"
    echo "      what it claims to protect."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if grep -qE '^# |build failed|cannot use|undefined:' <<<"$out"; then
    echo "FAIL: $label — the mutation broke the BUILD rather than the guard. A fixture that"
    echo "      cannot compile proves nothing about the behavior it is meant to exercise."
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  if ! grep -qF -- "$want" <<<"$out"; then
    echo "FAIL: $label — the test failed, but not with the expected message."
    echo "      wanted to find: $want"
    echo "$out" | quote_output
    fails=$((fails + 1))
    return
  fi
  echo "ok  $label"
}

PKG="./core/adapters/inbound/agents/processlifecycle/"
DARWIN_FILE="core/adapters/inbound/agents/processlifecycle/process_darwin.go"
LINUX_FILE="core/adapters/inbound/agents/processlifecycle/process_linux.go"

case "$(uname -s)" in
Darwin)
  # ── 1. the mode rule is ignored: a read-only holder "writes" ──
  assert_go_test_goes_red \
    "darwin HoldsForWriting counting a read-only descriptor as a writer" \
    "$DARWIN_FILE" \
    $'\treturn m == \'w\' || m == \'u\'' \
    $'\treturn m != 0' \
    "$PKG" \
    "^TestHoldsForWriting\$/^held_read-only_by_the_asked_pid\$" \
    "= true, want false"

  # ── 2. -a is dropped: the pid's other writers answer for any path ──
  assert_go_test_goes_red \
    "darwin HoldsForWriting without lsof's -a" \
    "$DARWIN_FILE" \
    'lsofPath, "-a", "-p", strconv.Itoa(pid), "--", path)' \
    'lsofPath, "-p", strconv.Itoa(pid), "--", path)' \
    "$PKG" \
    "^TestHoldsForWriting\$/^a_file_the_asked_pid_does_not_hold\$" \
    "= true, want false"

  # ── 3. the non-positive-pid guard is dropped: lsof runs for no process ──
  # One run per row, so each pid has to go red on its own.
  for row in zero_pid negative_pid; do
    assert_go_test_goes_red \
      "darwin HoldsForWriting starting lsof for a non-positive pid ($row)" \
      "$DARWIN_FILE" \
      $'\tif pid <= 0 || path == "" {' \
      $'\tif path == "" {' \
      "$PKG" \
      "^TestHoldsForWritingNamesNothingStartsNoChild\$/^${row}\$" \
      "a call that names nothing must start no lsof child"
  done

  # ── 4. the figure loses its generator: the cost anchor goes red ──
  assert_go_test_goes_red \
    "HoldsForWriting's measured figure without its regenerate: line" \
    "$DARWIN_FILE" \
    $'// regenerate: IRRLICHT_MEASURE_PROBE_COSTS=1 go test\n// ./core/adapters/inbound/agents/processlifecycle/ -run TestMeasureProbeCosts -v\nfunc (darwinObserver) HoldsForWriting' \
    $'func (darwinObserver) HoldsForWriting' \
    "$PKG" \
    "^TestEveryMeasuredFigureNamesItsGenerator\$" \
    "quotes a measured figure and does not carry"

  echo "not run: linux HoldsForWriting mutations (5-7) — need a linux host; this is $(uname -s)"
  ;;
Linux)
  # ── 5. the linux mode rule is ignored: a read-only holder "writes" ──
  assert_go_test_goes_red \
    "linux HoldsForWriting counting a read-only descriptor as a writer" \
    "$LINUX_FILE" \
    $'\t\treturn flags&3 != 0, nil' \
    $'\t\treturn flags >= 0, nil' \
    "$PKG" \
    "^TestHoldsForWriting\$/^held_read-only_by_the_asked_pid\$" \
    "= true, want false"

  # ── 6. a flags-less fdinfo collapses to "not held" ──
  assert_go_test_goes_red \
    "linux HoldsForWriting reading an fdinfo with no flags line as not held" \
    "$LINUX_FILE" \
    'return false, fmt.Errorf("%s has no flags line", fdinfo)' \
    'return false, nil' \
    "$PKG" \
    "^TestHoldsForWritingProcCouldNotLook\$/^fdinfo_without_a_flags_line\$" \
    "want an error"

  # ── 7. an unreadable fd directory collapses to "not held" ──
  if [[ "$(id -u)" == 0 ]]; then
    echo "not run: linux HoldsForWriting EACCES mutation (7) — root reads a mode-000 directory anyway"
  else
    assert_go_test_goes_red \
      "linux HoldsForWriting reading an unreadable fd directory as not held" \
      "$LINUX_FILE" \
      'return false, fmt.Errorf("read %s: %w", fdDir, err)' \
      'return false, nil' \
      "$PKG" \
      "^TestHoldsForWritingProcCouldNotLook\$/^an_fd_directory_this_user_may_not_read\$" \
      "on an unreadable fd directory, want an error"
  fi

  echo "not run: darwin HoldsForWriting mutations (1-4) — need a darwin host; this is Linux"
  ;;
*)
  echo "FAIL: holds-for-writing-mutations — no mutation here runs on $(uname -s); HoldsForWriting has"
  echo "      only the darwin and linux implementations this file mutates."
  exit 1
  ;;
esac

if [[ $fails -gt 0 ]]; then
  echo "holds-for-writing-mutations: $fails FAILED"
  exit 1
fi
echo "holds-for-writing-mutations: ALL PASS"
