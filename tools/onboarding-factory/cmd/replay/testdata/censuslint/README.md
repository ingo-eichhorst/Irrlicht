# Census-figure lint corpus (#1518)

One file per spelling the detector must get right, committed rather than
improvised so the mutation evidence outlives the PR that added it (AGENTS.md:
"Prefer committing that mutation to describing it").

Each case is pinned to the verdict `scanCommentsForFigures` must return, and
roughly half of them are `want:none` rows. Those carry as much of the value as
the flagged ones: a detector that reported every digit run would satisfy every
`want:one-figure` case and read as excellent coverage. That is #1450's lesson,
and it is the specific risk the issue named — the natural implementation of
this check "flags every `0`, `1` and `2` in the package".

The cases run against `corpusCensus`, a synthetic census declared in
`issue1518_census_lint_test.go`, never against
`censusOfTheCommittedCatalog`. A corpus pinned to the LIVE figures would go
stale the first time the catalog moves, which is the defect this whole lint
exists to prevent — reproduced inside its own evidence.

The `.go.txt` extension is deliberate. The files must be parseable by
`go/parser` and must NOT be built, vetted or gofmt-gated: `gofmt -l tools/`
descends into `testdata/` (verified), and these fixtures plant comment
spellings whose whole point is to be odd.
