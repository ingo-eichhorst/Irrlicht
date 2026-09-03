# Replay & Onboarding-Factory Testing

Referenced from [AGENTS.md](../AGENTS.md)'s Testing section. Covers the
onboarding-factory's `of validate` and Go test suites, `tools/replay-fixtures.sh`,
the replay transition-timing ratchet, the machine-generated catalog census
("Replay's measured figures" — the worked example this repo's general
"a figure states the command that produces it" rule is named for; see
[testing-philosophy.md](testing-philosophy.md)), the read-boundary
reconstruction, the hook-channel grading path, how to regenerate goldens, and
the `replaydata/agents/adapters.json` maturity + capability model.

- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh` — gated in CI by linux.yml, and run
  natively as `tools/preflight.sh`'s `replay fixtures` gate, so golden drift
  surfaces without Docker. Takes ~3 minutes unscoped; under `--changed` it
  runs only when the diff touches `replaydata/`, the factory, or the
  `core/` parsers and tailer a golden is derived from.
- Replay transition timing: a golden records a `virtual_time` on every
  transition, and until #1480 **nothing compared it to anything** — the
  goldens pin it only against their own previous value, `compareOrdered` walks
  `prev_state`/`new_state` index-by-index and never reads the time, and the
  "N of M recordings diverge" headline every replay PR quotes is counts and
  kinds only (that headline is `extendedCheck.Diverges` counted over the
  catalog — see the next bullet for why it has exactly one definition). So a transition reproduced at the right position in the ORDER but
  31 seconds from when the daemon made it was a full pass, and the golden then
  pinned it as correct. `compareOrdered`
  (`tools/onboarding-factory/cmd/replay/extended_check.go`) now returns the
  MATCHED pairs rather than a count of them, each carrying how far apart in time
  the two sides fired — so the ordered-divergence figure and the timing figure
  are one traversal and cannot describe different transitions. The first draft
  had a second, identical loop plus three comments and a runtime assertion
  saying the two must agree; one loop makes that structural instead.
  Only KIND-MATCHED pairs carry a delta — where `compareOrdered` reports
  `state_differs` the two sides are not the same transition, so their timestamp
  difference means nothing and counting it would report one sequence divergence
  twice. The neighbouring question — whether a differing `reason` should
  demote a pair the same way — was measured in #1707 and answered NO: a reason
  names the MECHANISM, both mechanisms exist on both sides, and the catalog's
  dominant shape is a session's first `ready→working` reached through the
  classifier default on one side and the force bounce on the other, with those
  pairs sitting CLOSER in time than the ones that agree. What does survive the
  argument is narrower and cannot be produced by renaming a string — exactly one
  side SYNTHESIZED, the other classified — and it is `timeDelta.CrossMechanism`
  plus `TestCrossMechanismPairsAreAlreadyReported`, which is a report rather than
  a demotion: it asserts that every such pair in the catalog is already visible
  to an existing mechanism, and fails the day one is not. The figures are in
  `compareOrdered`'s doc comment as of that measurement, not restated here. The reporting side (`timing_drift.go`) buckets and ranks those deltas;
  it reuses `core/domain/stats.Percentile` rather than carrying its own.
  It is a **ratchet, not a tolerance gate**, and that is the deliberate
  shape: roughly a quarter of the catalog's kind-matched pairs are still more
  than 1s from their daemon, so a gate failing on all of them would protect
  nothing. `TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog` walks every
  sidecar-driven recording, prints the distribution, and fails when a
  recording NEWLY drifts, when a pinned entry stops drifting and is left to rot,
  or when the aggregate counts grow — the same idiom `knownZeroTransition`
  uses. The 1s threshold is read off the measured distribution rather than
  picked: |delta| is sharply bimodal (74.4% under 100ms, 24.3% over 1s) with a
  near-empty decade between, so the cut lands where ~1% of the population
  lives; `driftThreshold`'s doc comment carries the histogram. That trough is
  the same 10 pairs before and after #1478 reshaped both modes around it, which
  is stronger evidence for the cut than the original measurement was. The
  goldens #1476 documented as its accepted cost are enumerated by name in
  `knownFirstTransitionDrift` rather than living in a paragraph of a doc
  comment, which is what #1480 was filed about. Its mutation evidence is
  committed, not described: `cmd/replay/testdata/timing/` holds one file per
  timing shape, carrying both verdicts on purpose — a detector that flags
  everything and one that flags correctly are indistinguishable without the
  cases that must stay silent. `tools/replay-fixtures.sh` reports the same
  figure beside the divergence figure, because a `go test` that passes prints
  nothing without `-v` and an unread measurement is the failure mode this
  closes.
- Replay's measured figures: the worked example for the general rule above —
  "a figure that documents behaviour states the command that produces it, or
  it is marked as an estimate," including the evidence it was violated
  outside the replay tree (#1726) — rather than repeating either here. The
  replay tree carried one example of each outcome:
  `knownFirstTransitionDrift` is machine-generated and stayed right across a
  change that reshaped the distribution it describes, while the
  `zero`/`fabricated`/`divergent` counts were typed by hand and went wrong twice
  in two PRs — #1478 had to correct copies in five places, and one it missed
  left `tools/replay-fixtures.sh` claiming 198 divergent recordings while the Go
  side measured 140. Two mechanisms close it (#1503). **One named predicate**
  decides each population — `extendedCheck.Diverges`, `.ReproducesNothing`,
  `.Fabricates` — and every counter derives from it, including `main.go`'s exit
  code, which had its own wider spelling (`ordered || missing kinds || extra
  kinds`); those disjuncts are structurally unreachable and the census asserts
  the two spellings agree on every recording it walks rather than trusting that
  argument. `Diverges` is `len(OrderedMismatches) > 0`, and the near-misses are
  the point: "the counts or the kind sets differ" reads as a restatement and is
  one low, because one committed recording replays the same two kinds and the
  same four transitions in the wrong ORDER — that is the recording #1478's table
  lost, and it is pinned by name in
  `TestDivergesCatchesTheOrderSwapThatCountsAndKindsMiss`. **The counts are
  machine-generated**: `censusOfTheCommittedCatalog` is the pasteable literal
  `TestCatalogCensusMatchesTheCommittedFigures` prints on every run, the same
  idiom `knownFirstTransitionDrift` uses, and no doc comment restates a figure
  it carries. Two guards run before the equality check, because "paste the
  measured literal" is the wrong advice when the measurement itself broke: a
  walk that reached fewer recordings than the committed census, and a catalog
  where nothing diverges at all. Its mutation evidence is a committed corpus
  (`TestCensusDiffNamesEveryStaleShape`) — one deliberately-stale literal per
  shape, including #1478's exact one-low, plus the identical-census row that
  stops a diff which reports everything from looking correct.
  Three of the census's figures are not defect counts, and each exists because
  a prose sentence was carrying the number. `DivergentByCountsAndKinds` is the
  near-miss spelling itself, carried beside `Divergent` so that "they differ by
  exactly one recording" is re-derived every run instead of being true once.
  `UnpairedSidecars` and `PairedButUngraded` are the denominator's honesty:
  every sidecar on disk is either graded, unpairable, or paired-but-ungradeable,
  and the three figures sum to the catalog. Nothing is unpairable today — the
  Go walk and `tools/replay-fixtures.sh` select recordings by the same two
  transcript names, from the one declaration `replay.TranscriptNames`, with
  `TestSweepAndGatesWalkTheSameTranscriptNames` pinning the shell's own `find`
  list to it. They do not walk one identical *set* — the sweep enumerates
  transcripts, the gates enumerate sidecars — and the NAMES are what had
  drifted. The figure stays in the census at zero because it
  is what would report the blindness returning: before #1517 it counted every
  aider recording — graded by the sweep, by no Go gate, so
  `knownZeroTransition`, `knownFabricated` and #1480's ratchets described the
  catalog *that walk could see* rather than the catalog, and #1342's stated
  goal of catalog-wide coverage was silently false for a whole adapter. The
  remaining `PairedButUngraded` recordings produce no extended check at all —
  the process-owned-store adapters, plus the aider recordings whose sidecar
  names no real session and is not drivable.
- Replay read boundary: the sidecar records `file_size` from the fswatcher's
  stat at fire time but stamps `ts` at the daemon's **dequeue** time, and the
  watcher's stat time is not a field of `lifecycle.Event` at all — so "where
  had the daemon's read reached" is reconstructed, never read off. `#1342`'s
  `readBoundaryFor` widens a provably-manufactured pass by one recorded stat;
  `#1478` adds `readBoundaryClusterWindow` (10ms), which additionally takes
  every later stat dequeued within that window, on the inference that a serial
  detector loop dequeuing an event microseconds later had it queued while the
  read ran. **Additive, never narrowing** — that is what keeps it off the 35
  gemini-cli recordings a *replacement* time bound broke during #1476's review.
  The constant is CALIBRATED, not derived, and both walls are measured facts
  about the committed catalog: below 3ms the rescues are incomplete, at 28ms
  replay fabricates in `codex/2-1_basic-turn` and at 52ms in
  `codex/1-1_session-start`. Those two are exactly the goldens #1342's rejected
  guard-narrowing broke — two unrelated mechanisms hitting the same wall, which
  is why it is treated as a property of the catalog. `TestReadBoundaryClusterWindow_BothWallsAreMeasured`
  drives the window past each wall and is the calibration's committed mutation
  evidence; a window justified only from below could be raised to 1s with
  nothing objecting. One recording remains in `knownZeroTransition` because
  reaching it needs 69ms, i.e. it can only be bought by making two goldens
  assert something false — the trade this whole line of work exists to refuse.
- Replay's hook channel: replay grades a recorded hook through exactly two
  shared things — `session.hookSignalEffects`, which decides whether a hook
  NAME does anything at all, and `applyHookEvent`, which decides whether the
  effect reaches the classifier. Both had to be right before any hook was
  gradeable, and #1695 is the run where only the first was looked at.
  **A hook absent from the table is silently dropped**, which is #1320's
  lesson; `HookStop` was absent on the stated grounds that its effect "carries
  a payload a name-keyed lookup cannot supply". True about the payload, false
  about the consequence: `signalPolicies`' turn-done `apply` guards BOTH
  payload fields, so a payload-free hold still asserts `HookTurnDone` and the
  degradation is one-directional — it can add to the cue verdict and can never
  clear one the transcript found. That row is now present, and
  `TestPayloadFreeTurnDoneNeverClearsATranscriptCue` is the property rather
  than the paragraph. What the row does NOT replace is
  `SessionDetector.HandleStopHook`, which has the payload and must keep it:
  dropping the payload while keeping the hold reddens exactly one arm of
  `TestSessionDetector_StopHook_DrivesTheTurnDoneTransition`, the turn that
  ended on a question, and that arm is the whole argument for the two
  entry points coexisting.
  **The second half is the one that reads as health.** With the row added and
  nothing else, every golden stayed byte-identical — `applyHookEvent` copied
  `r.lastMetrics`, whose `NoSubstantiveActivity` belongs to the last TRANSCRIPT
  batch, and `runClassifier`'s mirror of #329's short-circuit then discarded a
  classification that had already answered `ready`. The flag is PER PASS and a
  hook pass is a different pass; the daemon never had the problem because its
  synthetic activity event goes through `RefreshOnActivity`, which recomputes
  it. So a hook table row is worth checking end-to-end against a recording, not
  just against the table's own test.
  The payoff is that `cause` in a golden finally means something: a
  hook-produced `ready` (`cause: "hook"`, at the hook's own timestamp) and a
  transcript-produced one (`cause: "debounce_coalesce"`) are different bytes,
  so the provenance question needed no new field. codex's
  `2-13_turn-end-terminal-text` went from 2 of 4 recorded transitions
  reproduced to 4 of 4 with zero mismatches, and #1388's two ">1s drift"
  entries left that population — those were the short-circuit, not the
  "structural" debounce lag they were recorded as.
  Which recordings can grade a Stop is a register that is machine-generated
  (`stopHookCensus`, `TestStopHookIsGradedByTheCommittedCatalog`) because a
  hand-written one is what #1695 had to correct: at #1695 exactly ONE could,
  codex's, and `replaydata/agents/claudecode/` carried no Stop-bearing sidecar
  at all — the catalog's other two Stops belong to co-resident claude-code
  sessions inside another adapter's multi-agent recording, one naming a session
  the replay does not drive, one on a sidecar that cannot drive a replay. A
  catalog with no Stop at all is a REFUSAL there, not a pass.
  **#1699 is the second, and the point is that it is not a duplicate of the
  first.** Claude Code is the adapter the Stop channel was built for
  (`session.HookStop`'s doc comment names it), so a claudecode-specific Stop
  regression had no golden that would notice; the only fix was to RECORD one,
  since those two zero rows carry a real, correctly-handled claudecode Stop and
  are ungradeable for reasons no harness change can reach. What the recording
  then showed is the sentence above with the sides swapped. In codex's
  recording the daemon flipped 0.8ms after the POST; in claudecode's it flipped
  **2.1s** later, at the next debounce boundary — still the hook's decision
  (`decided_by_tier: "hook"`, `hook_turn_done: true` in the sidecar), because
  `dispatchHookActivity` pushes its synthetic event onto the DEBOUNCED channel
  and a transcript write 97ms after the POST re-opened the 2s window. So which
  side of the debounce a Stop lands on is a property of what the CLI wrote in
  the 2s after its own Stop, not of the adapter, and replay — which applies the
  hook at its own timestamp — is now the EARLY side. That is a real replay/daemon
  fidelity gap and it is carried as a `knownFirstTransitionDrift`-style ratchet
  entry rather than tuned away; #1388's paragraph in `issue1480_timing_test.go`
  is where the three readings of it live side by side.

- Replay goldens (when a recording or replay-output change is in play):
  regenerate with `UPDATE_REPLAY_GOLDENS=1 go test
  ./tools/onboarding-factory/cmd/replay/... -count=1` (the `-count=1`
  matters — without it the cached test result skips the write). Commit only
  the goldens of the adapter you touched.
- replaydata integrity (the onboarding recording/fixture catalog): `go run ./tools/onboarding-factory/cmd/of validate`
  (schema + referential integrity over the catalog + cells — a CI gate). When a
  `web/` or recording-rig change is in play, also `bash
  tools/onboarding-factory/scripts/smoke-test.sh` (the rig's `bash -n` + lib
  unit tests).
- Onboarding maturity + capability model (#1369): `replaydata/agents/adapters.json`
  is the COLUMN file of the matrix, symmetric to `scenarios.json`'s rows. It
  holds each adapter's claimed maturity (`planned`/`alpha`/`beta`/`stable`) and
  the traits it lacks. `of validate` gates three things over it, and all three
  are scoped to a catalog that actually carries the core set, so a partial
  fixture tree is unaffected:
  - **The core twelve.** Only 12 of the 48 scenarios gate a promotion; the
    other 36 are optional and block nothing. The set, and one line of
    justification per scenario, is in `internal/matrix/vocabulary.go` — in code
    rather than in data, so weakening it is a reviewable diff. `alpha` requires
    four state scenarios reachable from hooks alone, `beta` all nine
    state-core, `stable` all twelve including the three metrics ones. That
    split is what `alpha` MEANS: state only, no metrics.
  - **The capability model.** Traits are a closed set in
    `internal/matrix/capability.go`; each adapter's value for one is
    three-valued — `absent` (derives `n/a`), `untraced` (has the feature, it
    reaches no Source the adapter reads → derives `unobservable`), or the
    default `traced`. A boolean cannot express this and the two-valued version
    was pruned in #529 after producing false positives. A declared dead pair
    **needs no cell directory** — the matrix synthesizes it — which is the
    part that shortens onboarding. Agreement is checked both ways: a
    declaration must match its cell, and a structurally dead cell must be
    explained by a declaration or by a documented `record_blocked` reason.
  - **Maturity claims.** Declaring more than the core standing earns is a
    failure; declaring less never is (the 30-day / adoption criteria live in
    `site/docs/adapters.html` and no gate can see them). `of status --summary`
    shows claimed vs earned side by side.

## The one-scenario trait rule

`internal/matrix/capability.go` declares every trait against exactly **one**
scenario, and `Trait.Scenario` is a single string rather than a slice so that
widening one is a deliberate type change rather than an edit to a literal. That
shape is not an aesthetic choice — it is what two withdrawn multi-scenario
traits cost, and the rule is only legible through the two cases that produced
it. Both are recorded here because the second one's traits have since been
removed along with the feature they described (#1846 / #1876), and the rule has
to outlive them.

**Case 1 — `architect_editor`, withdrawn for asserting something false.**
`architect-editor-pair` was very nearly folded into `plan_mode`: five adapters'
5.4 assessments say in as many words that it is "the SAME architectural
blocker" as their 2.18. It has two instantiations and only one goes through a
plan gate — (b) is the plan→implement mode pair, but (a) is a genuine two-model
handoff, and the acceptance criteria are written for (a): two model
contributions in one turn, each with its own `ModelName`. The bundled version
was written and then measured. It synthesized an `unobservable`
architect-editor-pair cell for **aider** — the one adapter whose signature
feature *is* architect/editor mode — out of a `plan_mode` value that says only
that aider's `/ask` gate is not persisted. A false claim, in a cell nobody had
assessed. Splitting the trait cost the model its second predicting trait and
was worth it. `architect_editor` is still in `Traits` today, and its comment
there is the original write-up of this case, kept in place because the trait it
explains is still there to explain; the paragraph above restates it so both
cases can be read side by side.

**Case 2 — `backchannel_control` / `backchannel_observe`, withdrawn for making
the validator unsatisfiable.** A single `backchannel` trait covering both
`backchannel-control` and `backchannel-observe` was tried. mistral-vibe was
`observed` on control and `blocked-daemon` on observe, and that observe cell's
own notes said "RE-ASSESS after Control fix" — so the very next edit to it
would have made `of validate` unsatisfiable: `traced` fails the reverse arm on
observe, `untraced`/`absent` fail the forward arm on control, and the only
escape is writing a `record_blocked` reason that would mean the wrong thing.
Two traits, two truthful values, no escape needed. This is the sharper of the
two cases: a trait spanning two scenarios **whose cells disagree** has no
truthful value at all, where case 1 merely had a wrong one.

**The rule that survives them.** A trait may span several scenarios only while
those scenarios are guaranteed to move together for **every** adapter. Nothing
in this matrix guarantees that, and both attempts to assume it were wrong
within one ticket. So every trait covers exactly one scenario, and the cost of
that is one JSON line in `adapters.json` per cell rather than per feature. The
same rule is why the three #1803 error traits (`overload_retry`,
`overload_terminal`, `auth_refusal`) are not folded into `error_epilogue`, and
why `subscription_signal` is split from `burndown_progression` — those splits
carry their own measured evidence in `capability.go`.
