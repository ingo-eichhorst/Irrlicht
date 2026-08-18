#!/usr/bin/env bash
# swift-suite.sh — run the macOS Swift test suite so that a hang or an abort is
# a bounded, legible failure instead of a silently truncated run (#1523).
#
# This file is sourced, not executed:
#
#   . "$SCRIPT_DIR/lib/swift-suite.sh"
#   swift_suite_run "$log" swift test --skip LauncherHarnessTests
#   swift_suite_verdict "$?" "$log"
#
# Shell options, both directions, because the convention above depends on them
# and used to say nothing about them (#1633):
#
#   this file  requires nothing of the caller's options and changes none of
#              them. swift_suite_run returns the command's status, or 124 for
#              a hang, whether or not `-e` is set.
#   the caller must keep `$?` reachable. Under `-e` a non-zero return aborts
#              the caller on the line above, so swift_suite_verdict never runs
#              and a hang and a failure print the same thing — write `set +e`
#              first, or `|| rc=$?` (#1629, .github/workflows/macos-swift.yml).
#
# Why any of this exists. XCTest's stall detector responds to a hung
# expectation by calling `abort()` on the whole process. The run then stops
# partway — 33 of 40 suites, in the measured case — and the aggregate total
# never prints. Every suite that DID report says "0 failures", because none of
# them failed; the ones that would have failed simply never ran. So the output
# reads as healthy, and the only signal is an exit code that is easy to
# attribute to the cosmetic-looking `error: Exited with unexpected signal code
# 6` line trailing the log. That misreading has already happened twice in this
# repo: once when a truncated run was reported as "every other Swift suite
# passes", and once in #1044 when a real appearance bug was written off as
# toolchain antialiasing.
#
# The three checks below are therefore about the *shape* of the run, not about
# whether any assertion failed:
#
#   completed  — the test bundle reported its own result, so nothing was
#                dropped after the last line we can see.
#   ran tests  — it executed at least one test, so "completed" is not vacuous.
#   exit code  — the ordinary signal, kept but no longer trusted alone.
#
# `swift_suite_completed` keys on the BUNDLE line rather than on the presence
# of an `Executed N tests` aggregate, and the difference is the whole point:
# measured against real logs, a truncated run still contains 29-33 per-suite
# `Executed N tests` lines, so a grep for that string reports a truncated run
# as complete. Only the bundle's own summary is written once, at the end.

# Seconds before a run is declared hung. Deliberately far above the observed
# runtime (~2s idle, ~30s under heavy load) — this is a containment for a
# process that will otherwise sit forever, not a performance budget. It
# mirrors macos-swift.yml's `timeout-minutes`, which exists for the same
# reason: a job stuck `in_progress` reads as "still queued". It mirrors that
# rationale, not that workflow's number (15 minutes, for a ~45s build).
#
# The number is a CEILING, and it is derived rather than picked (#1530). It was
# 600 — exactly an automated caller's whole Bash-tool budget — so the caller
# killed the gate at the same instant the gate would have started explaining
# itself, and every careful diagnosis below was unreachable for the one kind of
# caller that most needs it. A bound whose expiry nobody can observe is this
# file's own founding rule turned on itself: absence of a finding and inability
# to look produced the same output.
#
# The four numbers it has to sit under, all measured or documented elsewhere in
# the repo rather than invented here:
#
#   suite, healthy         ~5s (318 tests, this machine), ~30s under heavy load
#   cold `swift build`     ~105s measured; the swift gate builds BEFORE the suite
#   pre-push budget        540s (tools/preflight.sh --budget, #1570)
#   agent command budget   600s (one foreground Bash tool call)
#
# So the bound must satisfy `timeout + cold build < pre-push budget`, i.e.
# anything below 435. 240 takes that with room and still leaves 8× the loaded
# run. `SWIFT_SUITE_MIN_HEADROOM` is what `swift-suite_test.sh` asserts against
# — the relation is checked on every run rather than being true the day it was
# typed.
# shellcheck disable=SC2034  # both are read by swift-suite_test.sh (142-145),
# which asserts `default_timeout + COLD_BUILD < MIN_HEADROOM` on every run
# rather than leaving the relation true only on the day it was typed. They are
# deliberately declared here, beside the budget arithmetic they document.
SWIFT_SUITE_COLD_BUILD_SECONDS=105
SWIFT_SUITE_MIN_HEADROOM=540
SWIFT_SUITE_TIMEOUT="${SWIFT_SUITE_TIMEOUT:-240}"

# Every grep here passes -a. That is not defensive noise: `swift test` emits
# ANSI colour and erase sequences, and a grep that classifies the stream as
# binary on one control byte matches NOTHING and reports success. The default
# `grep` on a developer machine here is ugrep, which does exactly that, and it
# fooled an abort-check run against this very issue's output.
SWIFT_SUITE_GREP_OPTS="-a"

# ---------------------------------------------------------------------------
# The real-home witness (#1669, #1670; the class #1661 opened)
# ---------------------------------------------------------------------------
#
# A test run must not leave anything behind in the developer's home. Three
# instances of the same defect have now shipped: 1136 preference plists
# (#1661), a fixture plus an OVERWRITTEN session-order.json in the live
# daemon's directory (#1669), and installed audio in ~/Library/Sounds (#1670).
#
# Each was fixed at its source. This is the standing check that the class has
# not been re-entered — and it lives in the shell rather than in the suite for
# one reason: it is the only place that still runs when the suite does NOT
# finish. XCTest's stall detector `abort()`s the process (#1523),
# swift_suite_run kills the tree at SWIFT_SUITE_TIMEOUT, and
# `preflight.sh --budget` kills the gate. Those are exactly the runs where a
# `defer`-based cleanup is skipped, i.e. the runs on which #1670 actually
# leaks — so an in-process assertion is absent precisely when it is needed.
#
# It reads. It never deletes, moves, or writes anything under the home. That is
# not a stylistic preference: a sweep of "files the tests probably left" is what
# removed ~1895 files from a real ~/Library/Preferences while #1661 was being
# fixed. Nothing here has a removal path at all.
#
# WHAT IS WATCHED, and why it is not more:
#
#   Library/Preferences  #1661's directory. 126-130 entries, 3ms to list.
#   Library/Sounds       #1670's directory. 2 long-standing entries.
#
# NEITHER IS NOISE-FREE, and the honest version of the measurement matters more
# than the flattering one. Measured on the author's machine:
#
#   0 additions   across each of two ~2 minute windows bracketing a full
#                 352-test suite run (one of them also a cold `swift build`)
#   4 additions   across one 870s window of ordinary interactive use —
#                 com.apple.bluetooth, com.apple.DuetExpertCenter.MagicalMoments,
#                 com.apple.voicetrigger, com.googlecode.iterm2. All background
#                 daemons, none ours, all in Library/Preferences.
#                 Library/Sounds took 0 over the same window.
#
# So the noise is a function of the WINDOW, which is the one variable here that
# is under our control — hence tools/preflight.sh brackets the `swift test`
# invocation only, not the build before it. At the measured background rate
# (~1 new plist per 218s of active use) a ~5s healthy run carries a ~2% chance
# of naming a stray plist.
#
# That residual is accepted rather than filtered, and the reason is #1661
# itself: its 1136 leaked files were named `<uuid>.plist`, so no name-based
# filter could have distinguished them from background churn without also
# hiding them. A guard that answers "something appeared, here is its name" is
# one a reader can judge in two seconds; a guard with an allowlist is one that
# would have missed the incident it exists for.
#
# `Library/Application Support/Irrlicht` is deliberately NOT watched, and the
# reason is not noise — it measured 0 additions over the same window. It is
# that a new-entry witness CANNOT see #1669: the worst of that defect is an
# overwrite of a file that already exists, which adds no entry, and the level
# that would see the rest (`instances/`) is written continuously by the live
# daemon (measured: two files modified inside 60s with nothing of ours
# running). Watching it would report clean while the defect happened, which is
# worse than not watching it. That instance is locked on the resolved path
# instead, by `platforms/macos/Tests/RealHomeIsolationTests.swift`.
#
# The whole ~/Library was considered and rejected in #1668 with the
# measurement: 6s over 887,491 files, and 24 of 41 changed entries were
# unrelated third-party churn (Google, WhatsApp, Claude, VS Code).
SWIFT_SUITE_WITNESSED_DIRS=("Library/Preferences" "Library/Sounds")

# ---------------------------------------------------------------------------
# The domain half of the witness (#1688; the gap #1672 left open)
# ---------------------------------------------------------------------------
#
# The directory witness above watches for new FILES. #1672 was a write into an
# ALREADY-EXISTING key of an ALREADY-EXISTING domain — `NotificationEventRow`
# persisted two sound choices merely by being rendered — so no file appeared, no
# directory entry changed, and the witness was green on every run that did it.
# A new-entry check cannot see that defect in principle, not by oversight.
#
# So this half compares two named domains' KEY SETS *and* their VALUES:
#
#   com.apple.dt.xctest.tool   what UserDefaults.standard resolves to under
#                              `swift test` (18 keys on this machine, all of
#                              them the app's own — the same 18 #1690 measured).
#   io.irrlicht.app            the app's own domain, which a test constructing
#                              an app type must never reach at all.
#
# WHAT IT SEES, and this is the whole list:
#
#   a key APPEARING      the first persist of a key on a domain that lacked it
#                        — a CI runner, a new developer machine, a newly added
#                        preference key.
#   a key DISAPPEARING   a `removeObject` against a real domain. Strictly worse
#                        than an addition, and reported separately for that
#                        reason.
#   a value CHANGING     at a key that was already there. This is the case the
#                        directory witness and a growth-only key-set witness are
#                        both structurally blind to, and it is why this half
#                        exists.
#   the domain APPEARING or EMPTYING wholesale.
#
# WHAT IT CANNOT SEE, stated here because a saturated domain otherwise reads
# EXACTLY like a clean one, and the verdict prints this every time it passes:
#
#   an IDEMPOTENT write — the same value stored back at a key that already
#   holds it. That is #1672 as it actually happened. Both keys were already
#   present at the values being written, so the domain stayed byte-identical
#   and the plist's mtime never moved (`1786919878`, measured across an
#   instrumented pre-fix run that PRINTED the two writes). No comparison of a
#   domain against itself can distinguish that from no write at all. The checks
#   that do see it are in-process and live in platforms/macos:
#   `PersistentDefaultsLintTests`' app-target mutation scan and
#   `InMemoryDefaults.writtenKeys`.
#
# So this half's value is the FIRST occurrence and the CHANGED occurrence, and
# the honest framing is that it is a second, independent net rather than a
# tighter one. It runs here rather than in the suite for the reason the whole
# witness does: it is what still runs when the suite abort()s (#1523), when the
# tree is killed at SWIFT_SUITE_TIMEOUT, or when `--budget` kills the gate.
#
# IT COMPARES. It never repairs. `defaults export <domain> -` is the entire
# mechanism — there is no `defaults write`, no `defaults delete` and no removal
# path anywhere in this file, because a "clean up what the tests left" sweep is
# what removed ~1895 files from a real ~/Library/Preferences during #1661.
#
# NOISE, measured on the author's machine and stated as measured:
#
#   0 changes   across a full `swift build` + suite gate run, both domains
#   0 changes   across a second gate run
#
# Quieter than the directory half by construction: ~/Library/Preferences is
# written by every background daemon on the machine, where a preference domain
# is written only by processes that own it. The residual is `io.irrlicht.app`
# on a machine where the app is RUNNING — Sparkle rewrites `SULastCheckTime` on
# its default 86400s interval (no `SUScheduledCheckInterval` is declared in the
# bundle; checked), i.e. ~0.07% of a 60s window, and the app rewrites
# `projectGroupOrder` when the user reorders groups. Both are named in the
# failure message with the same "re-run; ours reappears" advice the directory
# half gives, and neither is filtered by name, for #1661's reason.
SWIFT_SUITE_WITNESSED_DOMAINS=("com.apple.dt.xctest.tool" "io.irrlicht.app")

# The control probe. NEVER compared before-to-after — it is a machine domain and
# churns on its own (`ACDMonthlyAnalyticsLastPosted` moves without anyone asking
# it to). It answers exactly one question: did the reader actually read
# anything? Without it, "both watched domains hold no keys" — the state of a
# fresh CI runner or a new developer machine — is indistinguishable from a
# reader that is broken, misdirected or stubbed out, and the second of those
# would report a clean domain delta on every run forever. The directory half's
# vacuity guard cannot be copied here, because "no keys in either domain" is a
# legitimate state there where "no watched directory present" is not.
SWIFT_SUITE_WITNESS_CONTROL_DOMAIN="${SWIFT_SUITE_WITNESS_CONTROL_DOMAIN:-NSGlobalDomain}"

# The reader, injectable for ONE caller: tools/lib/swift-suite_test.sh, which
# has to drive an added key, a removed key and a changed value to prove this
# half can fail. The only way to do that against the real `defaults` is to
# `defaults write` a domain on the developer's machine — which is the incident,
# not a test of it. So the test points this at a committed stub
# (tools/lib/testdata/swift-suite/defaults-stub.sh) and everything downstream of
# the external command is production code. The two things a stub cannot pin —
# that a real `defaults export` answers a plist for a live domain and an empty
# dict for one that does not exist — are pinned by read-only arms in that same
# file against the real binary.
SWIFT_SUITE_WITNESS_DEFAULTS="${SWIFT_SUITE_WITNESS_DEFAULTS:-defaults}"

# swift_suite_witness_home — the home to watch.
#
# From the PASSWORD DATABASE, not `$HOME`. bash's `~user` expansion consults
# getpwnam, so it answers the real home even when `HOME` has been moved — and
# `HOME` being moved is not hypothetical here, since redirecting it is one of
# the things a future fix in this area might do. A witness that followed `HOME`
# would then watch an empty temp directory, find nothing, and report clean
# forever.
#
# SWIFT_SUITE_WITNESS_HOME overrides it, and exists for ONE caller:
# tools/lib/swift-suite_test.sh, which has to plant a file under a watched
# directory to prove the witness can fail. Planting one in the real home to
# test a guard against planting files in the real home is not a joke this
# repository can afford.
swift_suite_witness_home() {
  if [[ -n "${SWIFT_SUITE_WITNESS_HOME:-}" ]]; then
    printf '%s\n' "$SWIFT_SUITE_WITNESS_HOME"
    return 0
  fi
  local user home
  user=$(id -un 2>/dev/null) || return 1
  [[ -n "$user" ]] || return 1
  eval "home=~$user" 2>/dev/null || return 1
  [[ -n "$home" && "$home" != "~$user" ]] || return 1
  printf '%s\n' "$home"
}

# _swift_suite_witness_slug <relative-dir> — a filename-safe key.
_swift_suite_witness_slug() {
  printf '%s\n' "${1//\//_}"
}

# _swift_suite_witness_snapshot <statedir> <phase> — list every watched
# directory into <statedir>/<slug>.<phase>.
#
# The first line of each file is a STATE marker, and the three states are kept
# apart on purpose. "the directory is not there" and "the directory is there and
# empty" are different facts — a run that CREATES ~/Library/Sounds is #1670's
# other half — and "the directory is there and could not be read" must never
# read as either. A missing home is a failure of the snapshot itself.
_swift_suite_witness_snapshot() {
  local statedir="$1" phase="$2" home dir slug target
  home=$(swift_suite_witness_home) || {
    printf 'UNREADABLE\n' > "$statedir/HOME.$phase"
    return 1
  }
  printf '%s\n' "$home" > "$statedir/HOME.$phase"
  local entries
  for dir in "${SWIFT_SUITE_WITNESSED_DIRS[@]}"; do
    slug=$(_swift_suite_witness_slug "$dir")
    target="$home/$dir"
    if [[ ! -e "$target" ]]; then
      printf 'ABSENT\n' > "$statedir/$slug.$phase"
      continue
    fi
    # Captured rather than redirected straight to the file: `ls` failing after
    # it has already written a partial listing would otherwise leave a file
    # that looks like a successful snapshot.
    if entries=$(ls -A "$target" 2>/dev/null); then
      printf 'PRESENT\n%s' "${entries:+$entries$'\n'}" > "$statedir/$slug.$phase"
    else
      printf 'UNREADABLE\n' > "$statedir/$slug.$phase"
    fi
  done
  return 0
}

# _swift_suite_domain_flatten — read a `defaults export` plist on stdin, write
# one `<key>\t<flattened value>` line per TOP-LEVEL key.
#
# Top-level keys are the `<key>` elements at exactly ONE tab of indentation, and
# that is a property of the format rather than a heuristic: `plutil`/`defaults`
# indent by depth, and XML escapes `<` inside text, so no value can produce a
# line beginning with a tab followed by `<key>`. Nested dictionaries' keys are
# at two tabs or more and are folded into their parent's value, which is what we
# want — a change anywhere inside a top-level key's value is a change of that
# key. Output is one line per key so the comparison is line-based like the
# directory half, and so `cut -f1` recovers the key even for a value that itself
# contains a tab.
_swift_suite_domain_flatten() {
  awk '
    /^\t<key>/ {
      if (k != "") print k "\t" v
      s = $0
      sub(/^\t<key>/, "", s); sub(/<\/key>[ \t]*$/, "", s)
      k = s; v = ""; next
    }
    /^<\/dict>/ { if (k != "") { print k "\t" v; k = "" } next }
    {
      if (k != "") {
        s = $0
        sub(/^[ \t]+/, "", s)
        v = (v == "" ? s : v " " s)
      }
    }
    END { if (k != "") print k "\t" v }
  '
}

# _swift_suite_domain_state <domain> — the three-state snapshot of ONE domain,
# printed as a STATE marker line followed by the flattened key/value body.
#
# The three states are kept apart for the same reason the directory half keeps
# its three apart, and the mapping is measured rather than assumed:
#
#   PRESENT     the reader answered a plist holding at least one key.
#   ABSENT      the reader answered a plist holding NO keys. `defaults export`
#               answers `<dict/>` at status 0 for a domain that does not exist
#               (measured), so "no such domain" and "exists and is empty" are
#               one state here. Nothing downstream branches on the difference,
#               and merging them costs one read where distinguishing them would
#               cost two — i.e. a second, later view of the same domain.
#   UNREADABLE  the reader failed, produced nothing, or produced something that
#               is not a plist. An EMPTY answer is deliberately NOT a clean
#               empty domain: a reader that could not run at all answers empty
#               too, and that is the one failure this whole file exists to keep
#               distinguishable from a finding.
#
# Always returns 0 — the diagnosis is the verdict's job, so that one place
# decides what an unreadable domain means instead of two that can disagree.
_swift_suite_domain_state() {
  local domain="${1:-}" out body rc=0
  if [[ -z "$domain" ]]; then
    printf 'UNREADABLE\n'
    return 0
  fi
  # `|| rc=$?` rather than a bare assignment: under a CALLER's `-e` an
  # assignment from a failing command substitution aborts the caller on that
  # line, which is #1633's defect in this same file.
  # `:-defaults` as well as the declaration above: a caller that UNSETS the knob
  # (the shape a test's teardown takes) would otherwise make this line an
  # unbound-variable error under `set -u`, which is a witness that cannot look
  # failing for a reason nothing here would report.
  out=$("${SWIFT_SUITE_WITNESS_DEFAULTS:-defaults}" export "$domain" - 2>/dev/null) || rc=$?
  if (( rc != 0 )) || [[ -z "$out" || "$out" != *"<plist"* ]]; then
    printf 'UNREADABLE\n'
    return 0
  fi
  body=$(printf '%s\n' "$out" | _swift_suite_domain_flatten | sort) || body=""
  if [[ -z "$body" ]]; then
    printf 'ABSENT\n'
  else
    printf 'PRESENT\n%s\n' "$body"
  fi
  return 0
}

# _swift_suite_witness_domain_snapshot <statedir> <phase> — every watched
# domain, plus the control probe, into <statedir>/domain_<slug>.<phase>.
_swift_suite_witness_domain_snapshot() {
  local statedir="$1" phase="$2" domain slug
  _swift_suite_domain_state "$SWIFT_SUITE_WITNESS_CONTROL_DOMAIN" > "$statedir/CONTROL.$phase"
  for domain in "${SWIFT_SUITE_WITNESSED_DOMAINS[@]}"; do
    slug=$(_swift_suite_witness_slug "$domain")
    _swift_suite_domain_state "$domain" > "$statedir/domain_$slug.$phase"
  done
  return 0
}

# _swift_suite_witness_domain_report <marker> <keyfile> <bodyfile> — print each
# named key with the value it carries in <bodyfile>. Shared by the created,
# added and removed arms so the three cannot drift into printing different
# shapes. Both arguments are real FILES, never process substitutions: the body
# is re-read once per key, and a `<(…)` FIFO can only be read once — which would
# print the first key's value and empty strings after it.
_swift_suite_witness_domain_report() {
  local marker="$1" keyfile="$2" bodyfile="$3" key value
  while IFS= read -r key; do
    [[ -n "$key" ]] || continue
    value=$(awk -F'\t' -v k="$key" '$1 == k { print $2; exit }' "$bodyfile")
    printf '    %s %s = %s\n' "$marker" "$key" "$value"
  done < "$keyfile"
  return 0
}

# _swift_suite_witness_domain_verdict <statedir> — 0 only if every watched
# domain's key set AND values are demonstrably what they were before the run.
# Non-zero if a key appeared, a key vanished, an existing key's value moved, the
# domain appeared or emptied, or the witness could not look.
_swift_suite_witness_domain_verdict() {
  local statedir="${1:-}" ok=0 domain slug before after bstate astate cstate
  local b a bk ak chg added removed changed n ckey summary=""
  [[ -n "$statedir" && -d "$statedir" ]] || { echo "_swift_suite_witness_domain_verdict: needs an existing <statedir>" >&2; return 1; }

  cstate=$(head -1 "$statedir/CONTROL.after" 2>/dev/null) || cstate=""
  if [[ "$cstate" != "PRESENT" ]]; then
    echo "domain witness: the control read of $SWIFT_SUITE_WITNESS_CONTROL_DOMAIN answered '${cstate:-nothing at all}'." >&2
    echo "  That domain is never compared — it exists so a reader that cannot read is not" >&2
    echo "  mistaken for two domains that did not change. This run is unwitnessed there." >&2
    ok=1
  fi

  for domain in "${SWIFT_SUITE_WITNESSED_DOMAINS[@]}"; do
    slug=$(_swift_suite_witness_slug "$domain")
    before="$statedir/domain_$slug.before"
    after="$statedir/domain_$slug.after"
    if [[ ! -f "$before" || ! -f "$after" ]]; then
      echo "domain witness: $domain was never snapshotted — it is unwitnessed, not clean." >&2
      ok=1
      continue
    fi
    bstate=$(head -1 "$before" 2>/dev/null); astate=$(head -1 "$after" 2>/dev/null)
    if [[ "$bstate" == "UNREADABLE" || "$astate" == "UNREADABLE" ]]; then
      echo "domain witness: $domain could not be read — this run is unwitnessed there." >&2
      echo "  Inability to look must not print the same thing as finding nothing." >&2
      ok=1
      continue
    fi
    if [[ "$bstate" == "PRESENT" && "$astate" == "ABSENT" ]]; then
      echo "domain witness: $domain held keys before the run and holds none now." >&2
      echo "  Nothing in this repository is allowed to remove a real preference domain (#1661)." >&2
      ok=1
      continue
    fi
    if [[ "$bstate" == "ABSENT" && "$astate" == "PRESENT" ]]; then
      echo "domain witness: the run CREATED $domain, which held no keys before it:" >&2
      tail -n +2 "$after" > "$statedir/.cmp-a"
      cut -f1 "$statedir/.cmp-a" > "$statedir/.cmp-keys"
      _swift_suite_witness_domain_report + "$statedir/.cmp-keys" "$statedir/.cmp-a" >&2
      echo "  A test must not persist into a real preference domain (#1661, #1672)." >&2
      ok=1
      continue
    fi
    if [[ "$bstate" == "ABSENT" && "$astate" == "ABSENT" ]]; then
      summary+="${summary:+, }$domain (no keys)"
      continue
    fi

    b="$statedir/.cmp-b"; a="$statedir/.cmp-a"; bk="$statedir/.cmp-bk"; ak="$statedir/.cmp-ak"; chg="$statedir/.cmp-chg"
    tail -n +2 "$before" | sort > "$b"
    tail -n +2 "$after"  | sort > "$a"
    cut -f1 "$b" | sort > "$bk"
    cut -f1 "$a" | sort > "$ak"
    comm -13 "$b" "$a" | cut -f1 | sort -u > "$chg"

    added=$(comm -13 "$bk" "$ak")
    removed=$(comm -23 "$bk" "$ak")
    # A key is CHANGED when its whole line moved and the key itself is on both
    # sides — subtracting the added set is what stops a new key being counted
    # twice, once as an addition and once as a change.
    changed=$(comm -12 "$chg" "$bk")

    if [[ -n "$added" ]]; then
      n=$(printf '%s\n' "$added" | wc -l | tr -d ' ')
      echo "domain witness: the run ADDED $n key(s) to $domain:" >&2
      printf '%s\n' "$added" > "$statedir/.cmp-keys"
      _swift_suite_witness_domain_report + "$statedir/.cmp-keys" "$a" >&2
      echo "  A test must not persist into a real preference domain (#1661, #1672)." >&2
      ok=1
    fi
    if [[ -n "$removed" ]]; then
      n=$(printf '%s\n' "$removed" | wc -l | tr -d ' ')
      echo "domain witness: the run REMOVED $n key(s) from $domain:" >&2
      printf '%s\n' "$removed" > "$statedir/.cmp-keys"
      _swift_suite_witness_domain_report - "$statedir/.cmp-keys" "$b" >&2
      echo "  Nothing in this repository is allowed to delete a real preference (#1661)." >&2
      ok=1
    fi
    if [[ -n "$changed" ]]; then
      n=$(printf '%s\n' "$changed" | wc -l | tr -d ' ')
      echo "domain witness: the run CHANGED $n existing key(s) in $domain:" >&2
      printf '%s\n' "$changed" > "$statedir/.cmp-keys"
      while IFS= read -r ckey; do
        [[ -n "$ckey" ]] || continue
        printf '    ~ %s\n      before: %s\n      after:  %s\n' \
          "$ckey" \
          "$(awk -F'\t' -v k="$ckey" '$1 == k { print $2; exit }' "$b")" \
          "$(awk -F'\t' -v k="$ckey" '$1 == k { print $2; exit }' "$a")" >&2
      done < "$statedir/.cmp-keys"
      echo "  This is the shape the directory witness cannot see: a write into an existing" >&2
      echo "  key of an existing domain (#1672). If it is io.irrlicht.app, the running app" >&2
      echo "  also writes SULastCheckTime and projectGroupOrder — re-run to tell that apart;" >&2
      echo "  ours reappears every time. Nothing here rewrites anything either way." >&2
      ok=1
    fi
    if [[ -z "$added$removed$changed" ]]; then
      summary+="${summary:+, }$domain ($(wc -l < "$bk" | tr -d ' ') keys)"
    fi
  done

  if (( ok == 0 )); then
    echo "domain witness: ${summary:-no domains watched} — key sets and values unchanged."
    echo "  That is NOT proof the run wrote nothing. A write of the value a key ALREADY"
    echo "  holds changes no key and no value, which is exactly how #1672 happened; only"
    echo "  PersistentDefaultsLintTests and InMemoryDefaults.writtenKeys see that one."
  fi
  return "$ok"
}

# swift_suite_witness_before <statedir> — snapshot ahead of the run.
#
# Always returns 0. A snapshot that could not be taken is recorded rather than
# raised, so the caller runs the suite either way and the diagnosis lands in
# swift_suite_witness_verdict — one place that decides, instead of two that can
# disagree about what an unreadable directory means.
swift_suite_witness_before() {
  local statedir="${1:-}"
  [[ -n "$statedir" && -d "$statedir" ]] || { echo "swift_suite_witness_before: needs an existing <statedir>" >&2; return 1; }
  _swift_suite_witness_snapshot "$statedir" before || :
  # The domain half does not depend on the home resolving, so it is taken even
  # when the directory half could not be: two independent nets, and a failure of
  # one must not silently take the other with it.
  _swift_suite_witness_domain_snapshot "$statedir" before || :
  return 0
}

# swift_suite_witness_verdict <statedir> — 0 only if the run demonstrably added
# nothing to any watched directory AND left every watched domain's key set and
# values as it found them. Non-zero if it added something, if something
# vanished, if a preference value moved, or if either half could not look.
swift_suite_witness_verdict() {
  local statedir="${1:-}" ok=0 looked=0 dir slug before after added removed bstate astate
  [[ -n "$statedir" && -d "$statedir" ]] || { echo "swift_suite_witness_verdict: needs an existing <statedir>" >&2; return 1; }

  if [[ ! -f "$statedir/HOME.before" ]]; then
    echo "real-home witness: no 'before' snapshot exists — the run was NOT witnessed." >&2
    echo "  A clean report here would mean 'nothing was measured', not 'nothing was written'." >&2
    return 1
  fi
  _swift_suite_witness_snapshot "$statedir" after || :
  _swift_suite_witness_domain_snapshot "$statedir" after || :

  for dir in "${SWIFT_SUITE_WITNESSED_DIRS[@]}"; do
    slug=$(_swift_suite_witness_slug "$dir")
    before="$statedir/$slug.before"
    after="$statedir/$slug.after"
    if [[ ! -f "$before" || ! -f "$after" ]]; then
      echo "real-home witness: ~/$dir was never snapshotted — it is unwitnessed, not clean." >&2
      ok=1
      continue
    fi
    bstate=$(head -1 "$before" 2>/dev/null); astate=$(head -1 "$after" 2>/dev/null)
    if [[ "$bstate" == "UNREADABLE" || "$astate" == "UNREADABLE" ]]; then
      echo "real-home witness: ~/$dir could not be listed — this run is unwitnessed there." >&2
      echo "  Inability to look must not print the same thing as finding nothing." >&2
      ok=1
      continue
    fi
    if [[ "$bstate" == "PRESENT" && "$astate" == "ABSENT" ]]; then
      echo "real-home witness: ~/$dir EXISTED before the run and is gone now." >&2
      echo "  Something in the suite removed a directory from the developer's home (#1661)." >&2
      ok=1
      continue
    fi
    # A directory that appeared is itself the finding — #1670 creates
    # ~/Library/Sounds on a machine that never installed a custom sound.
    if [[ "$bstate" == "ABSENT" && "$astate" == "PRESENT" ]]; then
      echo "real-home witness: the run CREATED ~/$dir, which did not exist before it." >&2
      ok=1
    fi
    # An `if`, not `[[ … ]] && looked=1`: the `&&` form returns non-zero when
    # the condition is false, which under a CALLER's `set -e` aborts the
    # function mid-loop — the failure mode #1633 documents for this same file.
    if [[ "$bstate" == "PRESENT" || "$astate" == "PRESENT" ]]; then
      looked=1
    fi
    added=$(comm -13 <(tail -n +2 "$before" | sort) <(tail -n +2 "$after" | sort))
    removed=$(comm -23 <(tail -n +2 "$before" | sort) <(tail -n +2 "$after" | sort))
    if [[ -n "$added" ]]; then
      echo "real-home witness: the run left $(printf '%s\n' "$added" | wc -l | tr -d ' ') new entr(y/ies) in ~/$dir:" >&2
      printf '%s\n' "$added" | sed 's/^/    + /' >&2
      echo "  A test must leave nothing in the developer's home (#1661, #1669, #1670)." >&2
      echo "  Background daemons also write here — a com.apple.* or third-party plist" >&2
      echo "  appearing during the run is churn, measured at roughly one per 218s of" >&2
      echo "  active use. Re-run to tell that apart from something this repo wrote;" >&2
      echo "  ours will reappear every time. Nothing here deletes anything either way." >&2
      ok=1
    fi
    if [[ -n "$removed" ]]; then
      echo "real-home witness: the run REMOVED $(printf '%s\n' "$removed" | wc -l | tr -d ' ') entr(y/ies) from ~/$dir:" >&2
      printf '%s\n' "$removed" | sed 's/^/    - /' >&2
      echo "  Nothing in this repository is allowed to delete from the developer's home." >&2
      ok=1
    fi
  done

  # The vacuity guard. Every watched directory absent or empty is what a
  # witness pointed at the wrong home looks like, and it would report a clean
  # delta on every run forever.
  if (( looked == 0 )); then
    echo "real-home witness: not one watched directory was present with anything in it." >&2
    echo "  Home read as: $(cat "$statedir/HOME.before" 2>/dev/null). Watched: ${SWIFT_SUITE_WITNESSED_DIRS[*]}" >&2
    echo "  That is what a witness looking at the wrong place reports, so it is a failure." >&2
    ok=1
  fi

  if (( ok == 0 )); then
    echo "real-home witness: no new entries in ${SWIFT_SUITE_WITNESSED_DIRS[*]} under $(cat "$statedir/HOME.before")"
  fi

  # ALWAYS run, and never short-circuited by the directory half's verdict: the
  # two nets see different things (#1672 moves no directory entry, #1661 moved
  # no preference value), so a run that trips one still has to be told about the
  # other. `|| drc=$?` for #1633's reason — a bare call under a caller's `-e`
  # would abort here and the combined status would never be returned.
  local drc=0
  _swift_suite_witness_domain_verdict "$statedir" || drc=$?
  (( drc == 0 )) || ok=1

  return "$ok"
}

# swift_suite_completed <log> — did the test bundle report its own result?
swift_suite_completed() {
  grep $SWIFT_SUITE_GREP_OPTS -qE "Test Suite '.*\.xctest' (passed|failed) at" "$1"
}

# swift_suite_ran_tests <log> — did it execute at least one test? The vacuity
# guard: a bundle that reports "Executed 0 tests" is a gate that checked
# nothing, which this repo treats as a failure rather than a pass.
swift_suite_ran_tests() {
  grep $SWIFT_SUITE_GREP_OPTS -qE 'Executed [1-9][0-9]* test' "$1"
}

# swift_suite_bundle_failed <log> — did the bundle report its own result as
# FAILED? Read independently of the exit code, which is the only other source
# of "did it pass" and reaches us through script(1)'s status propagation. That
# propagation is a single point of failure and is spelled differently on Linux
# (util-linux script needs -e to return the child's status at all), so a port
# that got it wrong would otherwise read every failing suite as a pass.
swift_suite_bundle_failed() {
  grep $SWIFT_SUITE_GREP_OPTS -qE "Test Suite '.*\.xctest' failed at" "$1"
}

# swift_suite_last_test <log> — the last test to *start*, which for a hang or
# an abort is the one that caused it. Only usable because swift_suite_run
# allocates a pty: XCTest block-buffers when stdout is not a TTY, so a hung run
# otherwise produces an empty log and cannot be attributed at all.
swift_suite_last_test() {
  local last
  last=$(grep $SWIFT_SUITE_GREP_OPTS "' started" "$1" 2>/dev/null | tail -1)
  [[ -n "$last" ]] && echo "  last test to start: ${last#Test Case }"
  return 0
}

# _swift_suite_hung_headline — written from two branches (a hang that printed
# something, and one that printed nothing), which is exactly the kind of pair
# that drifts apart.
_swift_suite_hung_headline() {
  echo "swift test HUNG: no exit within ${SWIFT_SUITE_TIMEOUT}s; the process tree was killed." >&2
}

# swift_suite_verdict <exit-code> <log> — 0 only if the run both passed and
# demonstrably finished. Prints a diagnosis naming which of the two failed.
swift_suite_verdict() {
  local rc="${1:-}" log="${2:-}" ok=0

  if [[ -z "$rc" || -z "$log" ]]; then
    echo "swift_suite_verdict: needs <exit-code> <log>" >&2
    return 1
  fi
  # Numeric, checked rather than assumed: bash's arithmetic context resolves a
  # bare word as a variable name, so `[[ abc -eq 124 ]]` compares 0 and a
  # garbled exit code would silently take the success path.
  if [[ ! "$rc" =~ ^[0-9]+$ ]]; then
    echo "swift_suite_verdict: exit code '$rc' is not a number — refusing to judge the run." >&2
    return 1
  fi
  if [[ ! -f "$log" ]]; then
    echo "swift test produced no log at '$log' — treating the run as failed." >&2
    return 1
  fi
  # An EMPTY log is a different diagnosis from a truncated one and deserves its
  # own line: nothing started, rather than something starting and stopping
  # partway. Reporting it as "truncated" sends the reader looking for a test
  # that never ran.
  #
  # The hang case is checked FIRST inside this branch, and the order is the
  # decision: a run killed at the timeout having printed nothing is still a
  # hang, and calling it "never started" would send the reader to the pty and
  # the toolchain when the process was in fact alive the whole time.
  if [[ ! -s "$log" ]]; then
    if [[ "$rc" -eq 124 ]]; then
      _swift_suite_hung_headline
      echo "  It produced no output at all, so there is no test name to attribute it to —" >&2
      echo "  the hang is before or during startup rather than inside a test." >&2
    else
      echo "swift test produced NO output at all — the run never started." >&2
      echo "  A pty that could not be allocated looks exactly like this; so does a" >&2
      echo "  toolchain that failed to launch. Re-run, and check \`script -q /dev/null true\`." >&2
    fi
    return 1
  fi

  if [[ "$rc" -eq 124 ]]; then
    _swift_suite_hung_headline
    swift_suite_last_test "$log" >&2
    ok=1
  elif [[ "$rc" -ne 0 ]]; then
    echo "swift test exited $rc." >&2
    if grep $SWIFT_SUITE_GREP_OPTS -q 'unexpected signal code 6' "$log"; then
      echo "  signal 6: XCTest aborted the process on a stalled expectation, so the" >&2
      echo "  run stopped where it stood — see #1523." >&2
    fi
    swift_suite_last_test "$log" >&2
    ok=1
  fi

  if ! swift_suite_completed "$log"; then
    echo "swift test did not finish: the test bundle never reported its own result," >&2
    echo "  so the run was TRUNCATED and the suites after the last one shown never ran." >&2
    echo "  They report no failures because they did not execute, not because they passed." >&2
    swift_suite_last_test "$log" >&2
    ok=1
  elif ! swift_suite_ran_tests "$log"; then
    echo "swift test finished but executed 0 tests — the gate checked nothing." >&2
    ok=1
  fi

  # Asked separately from the exit code, on purpose — see swift_suite_bundle_failed.
  if swift_suite_bundle_failed "$log"; then
    echo "swift test: the test bundle reported its own result as FAILED." >&2
    ok=1
  fi

  return "$ok"
}

# _swift_suite_descendants <pid> — <pid> and every descendant, deepest first.
#
# Collected BEFORE anything is signalled, which is the whole trick: once the
# wrapper dies its children reparent to launchd and the ppid chain that finds
# them is gone. Deepest-first so a parent cannot spawn a replacement between
# two kills.
_swift_suite_descendants() {
  local kid
  for kid in $(pgrep -P "$1" 2>/dev/null); do
    _swift_suite_descendants "$kid"
  done
  echo "$1"
}

# swift_suite_run <log> <cmd...> — run <cmd...> under a pty, streaming its
# output AND capturing it to <log>, and kill it if it outlives
# SWIFT_SUITE_TIMEOUT. Returns the command's exit status, or 124 if it had to
# be killed.
#
# `script -q "$log"` is the pty. macOS-only spelling, which is fine because the
# only caller is a gate already guarded on Darwin, and it buys three things:
# XCTest line-buffers instead of block-buffering, so a hung run's log names the
# test it hung in rather than being empty; the output still reaches the
# terminal live, so a gate that is going to hang for minutes is not also
# silent; and the exit status is still the child's (verified — an aborting run
# comes back 1, a clean one 0, a SIGKILLed one 9).
swift_suite_run() {
  local log="${1:-}"; shift || true
  if [[ -z "$log" || $# -eq 0 ]]; then
    echo "swift_suite_run: needs <log> <cmd...>" >&2
    return 1
  fi
  : > "$log" || return 1

  # stdin from /dev/null is load-bearing, not tidiness. BSD script(1) calls
  # tcgetattr on its OWN stdin before allocating the pty, and fails outright
  # ("tcgetattr/ioctl: Operation not supported on socket") whenever stdin is
  # not a terminal — which is every environment this gate actually runs in: a
  # CI step, a pre-push hook, an agent session. Without this the gate does not
  # merely lose the pty, it never starts the suite at all.
  script -q "$log" "$@" < /dev/null 2>&1 &
  local child=$!

  local waited=0
  while kill -0 "$child" 2>/dev/null && (( waited < SWIFT_SUITE_TIMEOUT )); do
    sleep 1
    waited=$(( waited + 1 ))
  done

  if kill -0 "$child" 2>/dev/null; then
    # Kill the tree by explicit pid, NOT by process group. script(1) calls
    # login_tty, so `swift test` and the `xctest` it forks live in a different
    # SESSION and different process groups from the wrapper: a negative-PID
    # kill on the wrapper's group reaches the wrapper alone. That looks like it
    # works, because closing the pty master takes a *chatty* child down via
    # SIGPIPE — and a genuinely hung one writes nothing, so it survives, keeps
    # the pty and the SwiftPM build lock, and accumulates across runs. Measured
    # both ways; tools/lib/swift-suite_test.sh pins it with a fixture that is
    # silent and in its own process group.
    local victims
    victims=$(_swift_suite_descendants "$child")
    # The braces-with-stderr-suppressed wrapper is for legibility, not safety:
    # the shell reports a killed background job as
    # "swift-suite.sh: line NN: 1234 Terminated: 15  script -q ..." when it
    # reaps it, which lands immediately before the hang diagnosis and reads as
    # an error *in this file* at exactly the moment the gate is trying to
    # explain itself. The notice is written when `wait` reaps, so the
    # redirection has to cover the wait, not just the kills.
    #
    # The trailing `|| :` is the opposite — safety, not legibility — and it is
    # not about the group's status, which `return 124` discards anyway. Every
    # command in here is EXPECTED to fail: after SIGTERM plus the grace the
    # victims are gone, so `kill -KILL` exits non-zero and `wait` reports the
    # signal. Under a CALLER's `-e` the shell then dies on the first of them
    # and `return 124` is never reached, so a hang comes back as an ordinary
    # failure and swift_suite_verdict — which keys its entire hang diagnosis on
    # that 124 — diagnoses the wrong thing (#1633). A compound command that is
    # the left operand of `||` runs with errexit ignored throughout, so this
    # one token covers the whole block, including whatever is added to it
    # later. Guarding the commands individually is NOT equivalent, and the
    # near-miss is why this spelling was chosen: `|| :` on the two kills alone
    # moves the abort onto `wait` and returns 143 — as plausible a wrong answer
    # as the 1 it replaces. Measured all three ways in #1633.
    {
      # Unquoted on purpose: a whitespace-separated pid list.
      # shellcheck disable=SC2086
      kill -TERM $victims 2>/dev/null
      sleep 2
      # shellcheck disable=SC2086
      kill -KILL $victims 2>/dev/null
      wait "$child"
    } 2>/dev/null || :
    return 124
  fi

  wait "$child"
}
