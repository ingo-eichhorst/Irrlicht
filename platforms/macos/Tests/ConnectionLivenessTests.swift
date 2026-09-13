import XCTest
@testable import Irrlicht
import Foundation

/// `ConnectionLiveness.isStale` is a guard this change ADDS, so it has no
/// "before the fix" to run red. Its evidence is the mutant table below instead:
/// a committed set of wrong implementations, each of which the case table must
/// be able to tell apart from the real one.
///
/// That is the difference between a case table that happens to pass and one
/// that constrains anything. `testEveryMutantIsCaught` re-runs it on every
/// build, so a later edit that drops the case distinguishing a mutant fails
/// here rather than quietly reducing the suite to decoration.
@MainActor
final class ConnectionLivenessTests: XCTestCase {

    /// One case: the inputs, what the real predicate must answer, and why the
    /// row exists.
    private struct Case {
        let name: String
        let lastFrameAt: Date?
        let now: Date
        let deadline: TimeInterval
        let wantStale: Bool
    }

    private static let epoch = Date(timeIntervalSince1970: 1_700_000_000)

    /// Every edge named in `ConnectionLiveness.isStale`'s doc comment, plus the
    /// two ordinary ones on either side of the deadline.
    private static let cases: [Case] = [
        Case(name: "fresh — one second of silence, sixty tolerated",
             lastFrameAt: epoch, now: epoch.addingTimeInterval(1), deadline: 60, wantStale: false),
        Case(name: "one tick short of the deadline",
             lastFrameAt: epoch, now: epoch.addingTimeInterval(59.9), deadline: 60, wantStale: false),
        Case(name: "exactly at the deadline is the longest tolerated silence",
             lastFrameAt: epoch, now: epoch.addingTimeInterval(60), deadline: 60, wantStale: false),
        Case(name: "one tick past the deadline",
             lastFrameAt: epoch, now: epoch.addingTimeInterval(60.1), deadline: 60, wantStale: true),
        Case(name: "stale — a whole standby's worth of silence",
             lastFrameAt: epoch, now: epoch.addingTimeInterval(3600), deadline: 60, wantStale: true),
        Case(name: "no clock yet — there is no elapsed time to measure",
             lastFrameAt: nil, now: epoch.addingTimeInterval(3600), deadline: 60, wantStale: false),
        Case(name: "clock corrected backwards over the stamp",
             lastFrameAt: epoch, now: epoch.addingTimeInterval(-3600), deadline: 60, wantStale: false),
    ]

    /// The real predicate answers every case as specified.
    func testEveryCase() {
        XCTAssertFalse(Self.cases.isEmpty, "the case table is empty, so this test checked nothing")
        for c in Self.cases {
            XCTAssertEqual(
                ConnectionLiveness.isStale(lastFrameAt: c.lastFrameAt, now: c.now, deadline: c.deadline),
                c.wantStale,
                c.name)
        }
    }

    // MARK: - Committed mutation fixture

    /// A wrong implementation the case table above must be able to reject.
    private struct Mutant {
        let name: String
        /// What the mutation is, in the terms someone would apply it in.
        let edit: String
        let isStale: (Date?, Date, TimeInterval) -> Bool
    }

    private static let mutants: [Mutant] = [
        Mutant(name: "neverStale",
               edit: "delete the whole predicate and return false — the pre-#1953 behavior, "
                   + "where nothing ever judged a link silent",
               isStale: { _, _, _ in false }),
        Mutant(name: "infiniteDeadline",
               edit: "widen the deadline the caller passes to .infinity, the mutation named in "
                   + "the #1953 triage plan",
               isStale: { last, now, _ in
                   ConnectionLiveness.isStale(lastFrameAt: last, now: now, deadline: .infinity)
               }),
        Mutant(name: "atDeadlineIsStale",
               edit: "relax the comparison from `>` to `>=`, so the deadline becomes the "
                   + "shortest fatal silence instead of the longest tolerated one",
               isStale: { last, now, deadline in
                   guard let last else { return false }
                   return now.timeIntervalSince(last) >= deadline
               }),
        Mutant(name: "nilIsStale",
               edit: "treat a link with no clock as stale, which would have the tick reap "
                   + "each replacement socket before its first tick",
               isStale: { last, now, deadline in
                   guard let last else { return true }
                   return now.timeIntervalSince(last) > deadline
               }),
        Mutant(name: "absoluteElapsed",
               edit: "take `abs()` of the elapsed time, so a wall clock corrected backwards "
                   + "over the stamp reads as silence",
               isStale: { last, now, deadline in
                   guard let last else { return false }
                   return abs(now.timeIntervalSince(last)) > deadline
               }),
        Mutant(name: "alwaysStale",
               edit: "return true unconditionally, which would recycle every link on every tick",
               isStale: { _, _, _ in true }),
    ]

    /// Every mutant disagrees with `wantStale` on at least one committed case —
    /// i.e. applying that edit to `ConnectionLiveness.isStale` turns
    /// `testEveryCase` red.
    ///
    /// Not only asserted here: all six edits below were also applied to
    /// `ConnectionLiveness.isStale` itself and `testEveryCase` was run against
    /// each, going red every time — `neverStale` and `infiniteDeadline` on the
    /// two past-the-deadline cases, `atDeadlineIsStale` on the boundary case,
    /// `nilIsStale` on the never-confirmed case, `absoluteElapsed` on the
    /// backwards-clock case, and `alwaysStale` on all five not-stale cases.
    /// This table is what keeps that true without anyone repeating it.
    func testEveryMutantIsCaught() {
        XCTAssertFalse(Self.mutants.isEmpty, "the mutant table is empty, so this test checked nothing")
        XCTAssertFalse(Self.cases.isEmpty, "the case table is empty, so no mutant could be caught")

        for mutant in Self.mutants {
            let caughtBy = Self.cases.filter { c in
                mutant.isStale(c.lastFrameAt, c.now, c.deadline) != c.wantStale
            }
            XCTAssertFalse(
                caughtBy.isEmpty,
                "mutant '\(mutant.name)' (\(mutant.edit)) survives every case in the table — "
                    + "the table no longer constrains that behavior, so add a case that does")
        }
    }
}
