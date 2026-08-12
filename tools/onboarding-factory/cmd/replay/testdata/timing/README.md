# Transition-timing corpus (#1480)

One file per timing shape the measurement must get right, committed rather than
improvised so the evidence outlives the PR that added it (AGENTS.md: "Prefer
committing that mutation to describing it").

Each case supplies the two slices `compareOrdered` takes — already
filtered, exactly as `runExtendedCheck` hands them over: `recorded` has passed
`filterStateTransitions` (primary session, non-empty `prev_state`) and
`replayed` has passed `dropInitTransitions` (no synthetic init row).

`want_deltas_ns` is the full expected result, in order. A case whose
`want_first_drift` is `null` asserts that nothing exceeded `driftThreshold` —
those are the vacuity guards, and without them a detector that flagged
everything would look identical to one that flagged correctly.

The numbers in `first-transition-31s-early.json` are the real ones from
`mistral-vibe/scenarios/2-12_context-compaction`, the worst case #1476
documented. It is the case that must go red if the measurement stops measuring.
