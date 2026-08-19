# Transition-pairing corpus (#1707)

One file per reason-difference shape `timeDelta.CrossMechanism` must get right,
committed rather than improvised so the evidence outlives the PR that added it
(AGENTS.md: "Prefer committing that mutation to describing it").

Each case supplies the two slices `compareOrdered` takes — already filtered,
exactly as `runExtendedCheck` hands them over: `recorded` has passed
`filterStateTransitions` (primary session, non-empty `prev_state`) and
`replayed` has passed `dropInitTransitions` (no synthetic init row). Same shape
as `testdata/timing/`, one field different.

`want_cross_mechanism` lists the pair indices the predicate must report, and
`want_unreported` says whether the resulting check would leave such a pair
INVISIBLE — cross-mechanism in a sequence that does not otherwise diverge. That
second verdict is the one the catalog gate turns on, and it needs both answers
present: `catchup-against-classified.json` is the shape that must fail the
catalog gate, `catchup-behind-a-state-differs.json` is the committed catalog's
actual shape (copilot `1-4`, where the same pair sits behind a `state_differs`
and `Diverges` already reports it), and if only one of those existed a gate that
flagged every cross-mechanism pair and one that flagged correctly would be
indistinguishable.

Four cases must stay SILENT and they carry as much of the argument as the two
that fire. `mechanism-name-split.json` is the catalog's dominant reason
difference — 45 of the 49 measured at #1707 — and a predicate that reported it
would turn one transition under two mechanism names into 45 findings.
`reason-string-rename.json` is the population the issue predicted would grow,
and it grows for a reason nobody will fix: a frozen sidecar keeps the wording
that was current when it was recorded. `same-synthesizer-both-sides.json` pins
that the predicate keys on the ASYMMETRY rather than on the presence of the word
synthetic. `identical-reasons.json` is the ordinary vacuity guard.
