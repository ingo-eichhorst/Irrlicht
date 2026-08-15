package services

import "context"

// noGitBudget is the context every git read that deliberately has NO aggregate
// deadline runs under. Naming it is the whole point: #1563 gave an aggregate to
// the two operations that stack an UNBOUNDED number of git calls
// (ComputeDoraMetrics, YieldSweeper.Sweep) and left these without one, and a
// named helper makes that a reviewable absence rather than a bare context whose
// meaning the next reader has to infer. It is processlifecycle's
// noAggregateBudget (#1529) one layer over, down to the reasoning.
//
// Its production call sites are the enricher's: adoptGitMetadata (2 git calls),
// RefreshOnActivity (up to 3), CaptureYieldOnReady (1) and backfillOne (up to
// 3). Three reasons they get none, and the third is the one that would actually
// change an answer:
//
//   - The stack is bounded by a CONSTANT, not by the input. #1563's subject is
//     `1 + len(in-window tags) + len(revertCandidates)` and `1 per project
//     root` — counts a repository or a user's session list can grow without
//     limit. Two and three cannot.
//   - Their ceiling is already the SHORT one. Every read on these paths is
//     fixedCost (gitTimeout, 5s), because none of them walks commit messages,
//     so the worst case is 15s rather than the 30s-per-call the history walks
//     can reach.
//   - They run inside the detector loop, where a non-answer is what #1485/#1543
//     spent six issues making safe: it declines to overwrite what the session
//     already holds and leaves backfillOne willing to retry. An aggregate here
//     would make those non-answers MORE frequent for no bounded gain — the loop
//     has no deadline of its own to protect, and each read that goes unanswered
//     is one more field left to a later pass.
//
// It does not violate core/architecture_shellout_test.go, and the reason is
// what is DONE with it rather than what it is: that rule resolves a
// package-local helper whose body is a bare root context and reports it exactly
// like a literal context.Background(), so what keeps this legal is that nothing
// here passes it to exec.CommandContext. Every git call downstream still
// derives its own gitTimeout/gitHistoryTimeout from it, so each CHILD keeps a
// ceiling. What is absent is only the ceiling ACROSS children.
func noGitBudget() context.Context { return context.Background() }

// budgetSpent reports whether an operation's aggregate budget is gone, and it
// is the predicate every loop in this package that stacks git calls consults
// BEFORE starting the next one.
//
// One named spelling rather than `ctx.Err() != nil` at five call sites, for the
// reason shellout.Answered is one predicate rather than five: what it means is
// not obvious from the expression. `ctx.Err() != nil` reads as an error check,
// and the thing being decided is "have I run out of permission to look" — whose
// correct handling (poison the answer, do not report the remaining work as
// looked-at-and-empty) is the whole of #1529 and #1543.
func budgetSpent(ctx context.Context) bool { return ctx.Err() != nil }
