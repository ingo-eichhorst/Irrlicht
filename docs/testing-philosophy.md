# Testing Philosophy — House Style for What Counts as Evidence

Referenced from [AGENTS.md](../AGENTS.md)'s Testing section. These are the
repo's general rules for what makes a test (or any other verification
mechanism) trustworthy — they apply everywhere, not just to one gate or one
package. The `ir:exec` skill (`.claude/skills/ir:exec/SKILL.md`, "Prove and
verify") enforces the red-first and mutation rules mechanically; this file is
where the full rationale and incident history live.

**A test earns its place by having been seen fail.** A green that was never red is a
claim, not evidence. How you obtain that red depends on what the test is for.

**A defect test proves nothing until it has been seen red.** Run it before the fix
exists, confirm it fails, paste the failure. A test that passes on `main` means either
the diagnosis is wrong or the test doesn't reach the defect (a stub blind to the
asserted field is the classic) — stop and report rather than shipping the green. Locks
— tests pinning behavior that must *not* change — pass by construction; say which ones
those are. `ir:exec` enforces this in "Prove and verify"; it binds outside `ir:exec` too.

**Anything a change *adds* has no "before the fix" to run against, and owes a
deliberate mutation instead.** A new guard, a static `architecture_test.go` rule, a
linter or registry tripwire, a derived count or score, a schema constraint, a data
migration, a config rewriter, a contract assertion — every one of them passes the
moment it is written, which is exactly the condition the red-first rule exists to
prevent. So break the thing being protected — violate the invariant, perturb the
derived number, corrupt a migrated value — and confirm the new check goes red. If
nothing does, it does not reach what it claims to cover, and that is the same
stop-and-report as a defect test that passes on `main`. This includes a **lock the
change itself adds**: "passes by construction" is the reason the mutation is needed,
not an exemption from it — the Locks sentence above is about locks over behavior that
predates the change, where there is nothing new to prove reaches anything. Four agents
in one fleet run arrived at this gap from four directions — a new guard (#1366),
derived numbers (#1363), a data migration (#1367), a layering rule (#1391) — each
because a reviewer, or the agent itself, ran a mutation nobody had asked for.

**Prefer committing that mutation to describing it.** `tools/lib/testdata/posix-lint/`'s
deliberately-broken fixtures are the shape ("committed rather than improvised so the
mutation evidence outlives the PR"), and `TestSourceScanCatchesEveryKnownShape`
(`core/application/services/construction_test.go`) is the same idea as a corpus.
Evidence living only in a merged PR body is re-run by nothing; for the
`contracttesting` families #1479 committed it beside each assertion, in
`core/internal/contracttesting/<family>_selftest_test.go` — the paragraph
closing the contract-family bullets below carries the shape and its limits. And a guard that *rewrites* an existing one owes
its predecessor's cases as locks on top of its own — "Guarded construction" below
carries that rule and the incident that earned it.

**A verification mechanism must fail loudly when it cannot run.** Absence of a finding
and inability to look must never produce the same output: "the thing under test never
executed" is the most expensive way to fail, because it is indistinguishable from
success. Wherever a check greps, matches, mutates, shells out or waits on a readiness
signal, assert that the operation actually happened — not merely that it reported
nothing. The guard is one line each time. Three of them caught something real the
moment they were added: `posix-lint.sh` refusing rather than skipping when it finds no
POSIX shell, no static linter, or no files, after its first draft printed `ALL PASS`
over an installer carrying a deliberate `[[ ]]` (below); a mutation harness asserting
its mutation changed the file, which then caught two more stale mutations (#1390); and
an e2e test waiting on a signal narrower than "the daemon published its addr file",
which fires *before* the consent effects under test run, so a deliberately-broken
binary came back green (#1449; `ir:exec`'s "Prove and verify" section carries the recipe). Two more
were added before they could catch anything and carry the weaker evidence that they
*can* fire: the architecture corpus asserting every case still contains the construct
it plants (below), and the harness built on #1390's lesson from the start, carrying a
deliberate no-match row that must report `STALE` (#1450).

**A figure that documents behaviour states the command that produces it, or it
is marked as an estimate.** This is the counterpart to the rule above, from
the other direction: that one is about a check going silently blind, this one
is about a *number* silently drifting away from what it once measured — typed
once, then repeated by hand until it no longer describes anything. First
named for the replay tree's own catalog counts ("Replay's measured figures"
below, where `knownFirstTransitionDrift` and `censusOfTheCommittedCatalog` are
the machine-generated shape to copy), it is not scoped to replay and was
violated twice in one day outside it (#1726). PR #1724's version-floor
rationale claimed two source-read versions were *"seven months apart with
zero drift"* — in `hookinstaller.go`, in `monitoring-surface.md`, and in the
PR body — where `npm view @google/gemini-cli time` puts them **twelve days**
apart (2026-08-07 to 2026-08-19); the error ran in the direction that
overstates the evidence, since a
twelve-day window is far weaker support for "byte-identical" than seven
months. The review that caught it then wrote its own summary claiming
subagents "stalled seven times"; the real count, from the transcript, is
five. Neither figure had a command behind it — both were typed from memory
under the same pressure the rule exists to catch. The fix is the same shape
either way: derive the number in code and print the literal, or say in the
same sentence that the figure is an estimate and how it was arrived at.

**A fixture that waits by SLEEPING has not observed what it waits for, and the
assertion after the sleep is not evidence that it has.** Poll the condition to a
generous deadline and fail with the elapsed time: that turns "the machine was busy"
into a slower pass while a thing that genuinely never happens still fails loudly —
the property a longer sleep weakens. #1586's tmux fixture is the shape, and the
reason to MEASURE rather than reason about which condition is pending: it slept a
fixed 120ms and then hard-failed unless the helper had been reparented to init, and
those were **not the same condition**. Measured over 40 runs, the reparenting was
already complete on the first `ps` every time, while the env the test actually reads
was readable on the first sysctl *none* of the time (~1.2ms p50) — it only lands with
the exec, since until then the pid is still the Apple-signed, env-stripped `/bin/sh`.
So the sleep was covering a condition nothing checked, the hard-fail was checking one
that never failed for its stated reason, and polling only the checked one would have
replaced a 120ms margin with the duration of one `ps`. The poll is
`awaitFixtureCondition` (`processlifecycle/osutil_darwin_test.go`); each caller carries
a vacuity guard, because a fixture handed nothing to wait for reports ready having read
nothing. This is a rule about the shape, not a known-flake register: after the fix that
test is not expected to flake, and recording it as one would be a dismissal that stops
the next agent looking.

**And a fixture must observe the SUBJECT, never a side effect the subject produces on
its way out.** Polling is the second half of the rule above and not the whole of it:
#1616 fell through the gap. `gate-budget_test.sh` asked whether a killed process tree
had survived by looking for a *marker file* its innermost `sleep 30; echo … >marker`
would write — so "it was interrupted" and "it survived" could write the same evidence,
and an ordinary preemption between the two `kill`s of a depth-first walk let the shell
reap its dead `sleep` and run the `echo`. Someone following the paragraph above would
have polled the marker and kept an ambiguous fixture. Two measurements make the point
sharper than the flake did. The marker assertion could only fire at t+30 while the case
ended at t+3, so it never carried the property at all — the survivor count did. And
sparing the deepest process left the old case passing all three of its assertions while
a `sleep 30` genuinely outlived its bound. The fix is structural rather than a longer
wait: the fixture's leaf `exec`s its sleep, so there is no next command and no
mid-transition state, and survival is read as "does this pid exist" and polled to a
deadline that fails with the surviving pids. Reproduced 1-in-600 naturally under load,
and deterministically at 100% by injecting ~400µs at the identified point.

**A validator that cannot parse its input checks MORE, never less.** An input it
cannot read with confidence is neither a quiet pass nor a skip: it is the case where
the validator has the least idea what it is looking at, so it is the last place to
drop checks. `skill-lint.sh`'s fence and frontmatter checks exist for exactly that
reason (below) — skipping is how it tells "documents a marker" from "has one", and an
unbalanced delimiter would otherwise silence every check after it.

**Code that emits bytes from a structural diff gets a property test.** Anything that
computes an edit and writes the result — config rewriters, formatting-preserving
serializers, patchers, migrators — is tested by generating random inputs and random
mutations and asserting the output round-trips, not only by hand-written cases, which
encode what the author already thought of and are therefore the same set they got
right. `hookjson`'s splicer shipped with seven green round-trip tests and a defect
writing `,,` into ~11% of randomly shaped documents, because all seven removed the
*tail* of a container — the one position where the arithmetic was correct.
`TestSplice_PropertyRandomMutations`
(`core/adapters/inbound/agents/hookjson/jsonc_test.go`) is the shape to copy: a fixed
seed so a failure reproduces, the document *and* the mutation printed in the failure
message, and a committed iteration count small enough to stay in the suite (2000,
0.07s) with a much larger sweep across several seeds run locally before landing. Such
a PR says two things out loud. **Which structural axes the generator varies** — one
that varies only the axis you thought of is the same vacuous green wearing a different
hat: that test's first draft mutated only object members, so it never produced a
removal run longer than one item and never touched an array, while multi-item removals
inside an array are exactly what the production uninstall path performs
(`hooks[event]`, seven events removed in one pass). A fourth defect survived until the
generator was widened. And **which properties survive which mutation** — "every
comment is preserved" is false for a deletion, since the deleted subtree's comments
go with it, and asserting it anyway produces false failures that erode the test.

**Varying an axis means varying the SPELLING the code classifies on, not the axis's
name — and the generator itself needs a vacuity guard.** Both halves were earned in
one run, on `hooktoml`'s splicer (#1753), which had to be widened to model the
multi-line arrays mistral-vibe's own writer emits. The generator was extended to
place multi-line arrays in five positions with an adversarial element pool spelling
`[[hooks]]`, `[table]`, `#` and `]` — a widening that reads as thorough and produced
arrays in 1539 of 2000 documents. Then deleting the one line that makes the widening
safe (the guard that classifies NOTHING inside an array as a header, a comment or a
key) left the whole suite, those 2000 documents included, **green**: every
adversarial element was a *quoted string*, and `"[[hooks]]"` starts with a quote and
is header-shaped to nobody. The shape that bites is a bare nested array written as
the last element with no trailing comma — its line reads `[1, 2]`, which is exactly
what the header matcher matches. The generator now emits that too and the same
mutation reddens three tests. So state the axis in the vocabulary of the code under
test (what the scanner *matches on*), not of the format. And because a generator can
also stop emitting the construct entirely — one character in an `rng.Intn` threshold
does it — count what was actually produced and fail when it drops, the same way any
other mechanism must fail loudly when it cannot run: `assertArrayAxisWasExercised`
(`core/adapters/inbound/agents/hooktoml/property_test.go`) prints the census on every
run and is itself seen red by disabling the four call sites.
