import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Locks #1675: the quota chip's three remaining wall-clock reads answer from
/// the clock they are GIVEN, not the one the machine happens to be in.
///
/// This is #1663's suite (`PinnedNowSnapshotTests`) one read further on, and it
/// keeps that file's shape deliberately: every axis drives **two** values
/// through one view, because asserting a single rendering is green on the host
/// that agrees with it whether the input reached the view or not.
///
/// ## The three reads, and what each one costs
///
/// | Read | Moves | Kind |
/// |---|---|---|
/// | `quotaPacePercent` — the pace marker's x | pixels | **continuous** |
/// | `snapshotIsStale` — the chip's opacity | pixels | **discrete** |
/// | `QuotaResetFormat.timeUntil` — "resets in 4h 12m" | tooltip only | continuous |
///
/// The discrete one is the worse hazard and is why this suite exists rather
/// than a pin being enough: a continuous read makes a reference *wrong*, which
/// somebody notices, while a discrete one makes a reference *correct until a
/// wall-clock instant passes* and then permanently wrong — the same shape
/// #1663 met in `formatResetTime`'s same-day branch, now on an `.opacity`.
///
/// ## What is graded where, and why it is split
///
/// The pure functions are graded on values, because a value says which branch
/// ran. The two views (`QuotaWindowRow`, `QuotaStaleDimmed`) are graded in
/// **pixels**, by rasterising each twice under different pins and refusing an
/// identical result. That split is not stylistic: #1676 measured that putting
/// `Date()` back at a call site leaves every value-level assertion green and
/// reddens only the two-clock byte comparison. A suite of value tests over
/// `snapshotIsStale` would have graded the arithmetic and missed the defect.
///
/// Each pixel arm is paired with a **must-not-differ** arm on a fixture whose
/// verdict is the same under both clocks. Without those, "the bytes differ" is
/// satisfied by a view that varies with the clock for any reason at all,
/// including one unrelated to the property under test.
///
/// Everything goes through `PinnedSnapshotHost`, the type the snapshot suites
/// use, so "the host that pins" and "the host the proof was taken on" cannot be
/// two objects that disagree. Nothing here calls `as: .pinnedImage`, so there is
/// no committed reference and `ImageSnapshotCIScopeTests` does not skip this
/// suite — it is graded on a CI runner (#1615).
@MainActor
final class QuotaChipClockTests: XCTestCase {

    // MARK: - Fixtures

    /// The two instants #1663 established, 48 hours apart — a different
    /// calendar day in every time zone.
    private var early: Date { PinnedNowSnapshot.referenceNow }
    private var late: Date { PinnedNowSnapshot.contrastingNow }

    /// A 5h window resetting one hour after `early`, i.e. **between** the two
    /// clocks: four of its five hours elapsed under one (pace 80%) and long
    /// rolled over under the other. That single placement carries both flips —
    /// continuous for the pace marker, discrete for the staleness — and every
    /// premise assertion below re-derives it rather than trusting this comment.
    private var flipping: RateLimitInfo {
        info(resetsAt: early.addingTimeInterval(3_600), usedPercent: 42)
    }

    /// A window already rolled over at `early`, so it is stale under BOTH
    /// clocks — the must-not-differ fixture for the dimming arm.
    private var alreadyStale: RateLimitInfo {
        info(resetsAt: early.addingTimeInterval(-3_600), usedPercent: 42)
    }

    /// A window with the "no expiry data" sentinel, so `quotaPacePercent`
    /// returns nil under every clock — the must-not-differ fixture for the row
    /// arm.
    private var unpaceable: RateLimitInfo {
        info(resetsAt: Date(timeIntervalSince1970: 0), usedPercent: 42)
    }

    private func info(resetsAt: Date, usedPercent: Double, windowMinutes: Int = 300) -> RateLimitInfo {
        RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: usedPercent,
                                          windowMinutes: windowMinutes,
                                          resetsAt: resetsAt)],
            planType: "max",
            sampledAt: early
        )
    }

    private func session(id: String, adapter: String = "claude-code", rateLimit: RateLimitInfo) -> SessionState {
        SessionState(
            id: "sess_\(id)",
            state: .working,
            model: "claude-sonnet",
            cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: early,
            updatedAt: early,
            metrics: SessionMetrics(
                elapsedSeconds: 0,
                totalTokens: 0,
                modelName: "claude-sonnet",
                contextWindow: nil,
                contextUtilization: 0,
                pressureLevel: "safe",
                contextWindowUnknown: nil,
                estimatedCostUSD: nil,
                lastAssistantText: nil,
                tasks: nil,
                rateLimit: rateLimit
            ),
            adapter: adapter
        )
    }

    // MARK: - Read 1: the pace marker is placed by the `now` it is given

    /// One window, two clocks, two positions. Neither arm can be green on a
    /// host reading `Date()`, because today is neither of these instants.
    func testPacePercentIsPlacedByTheNowItIsGiven() {
        let window = flipping.windows[0]
        // The window opened 4h before `early` and closes 1h after it, so 4 of
        // its 5 hours have elapsed: 80%. `late` is 48h further on, far past the
        // reset, so the marker clamps to the bar's right edge.
        XCTAssertEqual(SessionListView.quotaPacePercent(window, now: early), 80)
        XCTAssertEqual(SessionListView.quotaPacePercent(window, now: late), 100)
    }

    /// The two nil branches keep answering nil whatever the clock says — a
    /// window that cannot be paced must not acquire a marker from the clock.
    func testPacePercentStaysUnpaceableUnderEveryClock() {
        for now in [early, late] {
            XCTAssertNil(SessionListView.quotaPacePercent(unpaceable.windows[0], now: now),
                         "the resetsAt sentinel became paceable at \(now)")
            let zeroLength = RateLimitWindowInfo(usedPercent: 42, windowMinutes: 0, resetsAt: late)
            XCTAssertNil(SessionListView.quotaPacePercent(zeroLength, now: now),
                         "a zero-length window became paceable at \(now)")
        }
    }

    // MARK: - Read 2: the staleness flip is decided by the `now` it is given

    /// The discrete flip, in both directions from one fixture — which is what
    /// separates this from a value that merely shifts. An implementation that
    /// hard-coded either verdict passes one of these and fails the other.
    func testStalenessFlipsWithTheNowItIsGiven() {
        XCTAssertFalse(SessionListView.snapshotIsStale(flipping, now: early),
                       "a window resetting an hour from now is not stale yet")
        XCTAssertTrue(SessionListView.snapshotIsStale(flipping, now: late),
                      "a window that reset 47h ago is stale")
    }

    /// The boundary is inclusive (`resetsAt <= now`), which is the daemon-side
    /// rule this mirrors. Pinned because `<` and `<=` are indistinguishable on
    /// every fixture that is not exactly on the instant.
    func testStalenessIsInclusiveAtTheResetInstant() {
        let boundary = info(resetsAt: early, usedPercent: 42)
        XCTAssertTrue(SessionListView.snapshotIsStale(boundary, now: early),
                      "a window resetting exactly now is stale")
        XCTAssertFalse(SessionListView.snapshotIsStale(boundary, now: early.addingTimeInterval(-1)),
                       "a window resetting one second from now is not")
    }

    /// …and the flip survives the fold: the same two clocks through the whole
    /// `sessions → chips` derivation, which is the call `SessionListView.body`
    /// makes.
    func testQuotaChipDataDrivesItsStalenessFromTheNowItIsGiven() {
        let sessions = [session(id: "1", rateLimit: flipping)]
        for (now, wantStale) in [(early, false), (late, true)] {
            let chips = SessionListView.quotaChipData(sessions: sessions, now: now)
            guard let chip = chips.first else {
                return XCTFail("the derivation produced no chip at \(now) — this check cannot have run")
            }
            XCTAssertEqual(SessionListView.snapshotIsStale(chip.snapshot, now: now), wantStale,
                           "the chip derived at \(now) reports the wrong staleness")
        }
    }

    /// The lock that makes dropping `QuotaWidgetData.isStale` safe rather than
    /// assumed: for **every** path through `mergeIntoBuckets`, the verdict the
    /// merge computed and kept equals the verdict re-derived from the snapshot
    /// it kept. If that ever stops holding, `QuotaStaleDimmed` and the
    /// tooltip's staleness note are reporting something the merge disagrees
    /// with.
    ///
    /// A lock over behaviour that predates #1675 — it passes by construction
    /// against the current merge — so its evidence is a mutation of the merge,
    /// not a red run before the fix (see the PR body's M5).
    func testTheMergeNeverKeepsASnapshotWhoseStalenessDisagreesWithIt() {
        let fresh = flipping
        let stale = alreadyStale
        // Each row is one bucket-collision shape the merge branches on.
        let cases: [(name: String, sessions: [SessionState])] = [
            ("single fresh", [session(id: "1", rateLimit: fresh)]),
            ("single stale", [session(id: "1", rateLimit: stale)]),
            ("stale first, then fresh", [session(id: "1", rateLimit: stale), session(id: "2", rateLimit: fresh)]),
            ("fresh first, then stale", [session(id: "1", rateLimit: fresh), session(id: "2", rateLimit: stale)]),
            ("two stale, second sampled later", [
                session(id: "1", rateLimit: stale),
                session(id: "2", rateLimit: RateLimitInfo(windows: stale.windows, planType: "max",
                                                          sampledAt: early.addingTimeInterval(60))),
            ]),
        ]
        var bucketsSeen = 0
        for row in cases {
            for now in [early, late] {
                var buckets: [String: SessionListView.ChipBucket] = [:]
                for s in row.sessions {
                    SessionListView.mergeIntoBuckets(session: s, into: &buckets, now: now)
                }
                XCTAssertFalse(buckets.isEmpty, "\(row.name) at \(now): the fold produced no bucket")
                for (key, bucket) in buckets {
                    bucketsSeen += 1
                    XCTAssertEqual(
                        bucket.isStale,
                        SessionListView.snapshotIsStale(bucket.snapshot, now: now),
                        "\(row.name) at \(now), bucket \(key): the merge kept a staleness verdict "
                        + "that disagrees with the snapshot it kept — the chip's dimming and the "
                        + "merge's own bucketing are now two facts that can differ")
                }
            }
        }
        XCTAssertEqual(bucketsSeen, cases.count * 2,
                       "the sweep did not reach one bucket per case per clock — it checked less "
                       + "than it reports")
    }

    /// The merge prefers a fresh snapshot over a stale one regardless of
    /// `sampledAt`, which is the rule the stored verdict exists for. Untestable
    /// before #1675 made the fold pure — `mergeIntoBuckets` read the clock and
    /// was a private method on a view — so this is new coverage of old
    /// behaviour, and a lock rather than a defect test.
    func testTheMergePrefersAFreshSnapshotOverAStaleOneEvenWhenTheStaleOneIsNewer() {
        // The stale snapshot is sampled LATER, so a sampledAt-only rule picks it.
        let newerButStale = RateLimitInfo(windows: alreadyStale.windows, planType: "max",
                                          sampledAt: early.addingTimeInterval(600))
        var buckets: [String: SessionListView.ChipBucket] = [:]
        for s in [session(id: "1", rateLimit: newerButStale), session(id: "2", rateLimit: flipping)] {
            SessionListView.mergeIntoBuckets(session: s, into: &buckets, now: early)
        }
        guard let bucket = buckets.values.first else {
            return XCTFail("the fold produced no bucket — this check cannot have run")
        }
        XCTAssertFalse(bucket.isStale,
                       "the fold kept the newer-but-stale snapshot over the fresh one")
        XCTAssertEqual(bucket.snapshot.sampledAt, flipping.sampledAt,
                       "the fold reports fresh but kept the stale snapshot's data")
    }

    // MARK: - Read 3: "resets in …" counts from the `now` it is given

    func testTimeUntilCountsFromTheNowItIsGiven() {
        let reset = early.addingTimeInterval(24 * 3_600 + 12 * 60)
        // The day branch drops minutes, so 24h12m reads "1d" — pre-existing
        // behaviour, pinned here because nothing pinned it before.
        XCTAssertEqual(QuotaResetFormat.timeUntil(reset, now: early), "1d")
        XCTAssertEqual(QuotaResetFormat.timeUntil(reset, now: early.addingTimeInterval(20 * 3_600)), "4h 12m")
        XCTAssertEqual(QuotaResetFormat.timeUntil(reset, now: early.addingTimeInterval(24 * 3_600)), "12m")
    }

    /// A reset already past reads `"0m"` rather than a negative duration —
    /// which is exactly the state `snapshotIsStale` is simultaneously reporting.
    func testTimeUntilNeverCountsBackwards() {
        XCTAssertEqual(QuotaResetFormat.timeUntil(early, now: late), "0m")
    }

    /// The day/hour spelling drops the hour component when it is zero, which is
    /// the one branch a "4h 12m"-shaped fixture never reaches.
    func testTimeUntilDropsAZeroHourComponent() {
        XCTAssertEqual(QuotaResetFormat.timeUntil(early.addingTimeInterval(3 * 86_400), now: early), "3d")
        XCTAssertEqual(QuotaResetFormat.timeUntil(early.addingTimeInterval(3 * 86_400 + 7 * 3_600), now: early), "3d 7h")
    }

    // MARK: - The pins reach the pixels

    /// The real `QuotaWindowRow` — the view `SessionListView.quotaChipView`
    /// renders — hosted through the type the snapshot suites use.
    private func rasterizedRow(_ snapshot: RateLimitInfo, now: Date, compact: Bool = false) -> Data {
        rasterize(QuotaWindowRow(window: snapshot.windows[0], compact: compact), now: now,
                  width: 220, height: 24, what: "the quota window row")
    }

    /// The real `QuotaStaleDimmed`, over content whose own rendering carries no
    /// clock — so the only thing that can move these bytes is the opacity.
    private func rasterizedDimmedChip(_ snapshot: RateLimitInfo, now: Date) -> Data {
        rasterize(QuotaStaleDimmed(snapshot: snapshot) {
            Color.white.frame(width: 60, height: 20)
        }, now: now, width: 60, height: 20, what: "the stale-dimmed chip")
    }

    private func rasterize(_ content: some View, now: Date,
                           width: CGFloat, height: CGFloat, what: String) -> Data {
        let host = PinnedSnapshotHost(content.frame(width: width, height: height),
                                      width: width, height: height,
                                      now: now)
        let image = PinnedScaleSnapshot.rasterize(host.view, scale: PinnedScaleSnapshot.referenceScale)
        // "could not rasterise" and "rasterised to the same thing" must never
        // produce the same verdict.
        guard let data = image.tiffRepresentation, !data.isEmpty else {
            XCTFail("\(what) rasterised to nothing — this check cannot have run")
            return Data()
        }
        return data
    }

    /// The load-bearing arm for read 1, and the one that catches the mutation
    /// this change exists to make catchable: put `Date()` back inside
    /// `QuotaWindowRow` and both renders become today's, identically. Every
    /// value assertion above stays green under that mutation.
    ///
    /// Rendered in **compact** mode deliberately: that drops the reset label,
    /// whose own clock read #1663 already covers, so the pace marker's x is the
    /// only clock-dependent pixel left in the row and this arm reddens for
    /// read 1 alone. The must-not-differ arm below is the same view in the same
    /// mode with the marker removed, so the two differ in exactly one thing.
    func testTheSameRowRendersDifferentlyUnderTwoPinnedNows() {
        // The premise, loudly first: the pace must actually differ for this
        // fixture, or the byte comparison measures nothing.
        XCTAssertNotEqual(SessionListView.quotaPacePercent(flipping.windows[0], now: early),
                          SessionListView.quotaPacePercent(flipping.windows[0], now: late),
                          "the fixture paces identically under both pinned clocks — the pixel "
                          + "comparison below cannot fail for the right reason")
        // …and rendering is deterministic, or "they differ" proves nothing.
        XCTAssertEqual(rasterizedRow(flipping, now: early, compact: true),
                       rasterizedRow(flipping, now: early, compact: true),
                       "the same row rasterised twice under one clock differs — this suite's "
                       + "both-sides arms are not measuring the clock")

        XCTAssertNotEqual(rasterizedRow(flipping, now: early, compact: true),
                          rasterizedRow(flipping, now: late, compact: true),
                          "the quota window row's pace marker sits at the same x under two pinned "
                          + "clocks — `\\.formatNow` is reaching nothing and the marker's "
                          + "position is coming from the machine's wall clock")
    }

    /// …and the must-not-differ half: a window the clock cannot pace, in
    /// compact mode so the reset label (which #1663 already covers) is dropped
    /// and the marker is the only clock-dependent pixel left. Two clocks, one
    /// rendering. Without this, the arm above is satisfied by a row that varies
    /// with the clock for any reason at all.
    func testARowTheClockCannotPaceRendersIdenticallyUnderBothClocks() {
        XCTAssertNil(SessionListView.quotaPacePercent(unpaceable.windows[0], now: early),
                     "the premise of this arm is that this fixture has no pace marker")
        XCTAssertEqual(rasterizedRow(unpaceable, now: early, compact: true),
                       rasterizedRow(unpaceable, now: late, compact: true),
                       "a row with no pace marker and no reset label still rendered differently "
                       + "under two clocks — something else in this row reads the wall clock")
    }

    /// The load-bearing arm for read 2 — the discrete one. The content is a
    /// plain white rectangle, so the only thing that can move a byte is the
    /// `.opacity` the staleness verdict picks.
    func testTheSameChipDimsUnderOneClockAndNotTheOther() {
        XCTAssertNotEqual(SessionListView.snapshotIsStale(flipping, now: early),
                          SessionListView.snapshotIsStale(flipping, now: late),
                          "the fixture's staleness does not flip between the pinned clocks — the "
                          + "pixel comparison below cannot fail for the right reason")
        XCTAssertEqual(rasterizedDimmedChip(flipping, now: early),
                       rasterizedDimmedChip(flipping, now: early),
                       "the same chip rasterised twice under one clock differs")

        XCTAssertNotEqual(rasterizedDimmedChip(flipping, now: early),
                          rasterizedDimmedChip(flipping, now: late),
                          "the chip rendered at the same opacity under a clock before and a clock "
                          + "after its window reset — `\\.formatNow` is reaching nothing and the "
                          + "stale dimming is coming from the machine's wall clock")
    }

    /// …and the must-not-differ half: a snapshot stale under BOTH clocks
    /// renders identically. This is what makes the arm above a measurement of
    /// the *flip* rather than of the clock leaking into the render some other
    /// way.
    func testAChipStaleUnderBothClocksRendersIdentically() {
        XCTAssertTrue(SessionListView.snapshotIsStale(alreadyStale, now: early)
                      && SessionListView.snapshotIsStale(alreadyStale, now: late),
                      "the premise of this arm is that this fixture is stale under both clocks")
        XCTAssertEqual(rasterizedDimmedChip(alreadyStale, now: early),
                       rasterizedDimmedChip(alreadyStale, now: late),
                       "a chip whose staleness verdict is identical under both clocks rendered "
                       + "differently — the dimming is varying with something other than the "
                       + "verdict")
    }

    /// The dimming really is the 50% the chip's doc comment claims, asserted
    /// against the production constant rather than a copy of it — so an arm
    /// above cannot be passing on a difference nobody can see.
    func testTheStaleOpacityIsHalf() {
        XCTAssertEqual(QuotaStaleDimmed<EmptyView>.staleOpacity, 0.5)
    }
}
