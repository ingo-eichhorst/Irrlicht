import XCTest
@testable import Irrlicht

/// Coverage for two of `SessionListView`'s quota-chip helpers that had no
/// test file of their own before issue #1995: `resolveChipMode` (auto
/// subscription-vs-usage detection) and `usageCreditsLine` (the credits
/// sub-line `quotaTooltip` builds for a usage-mode chip). Neither sits
/// naturally in `QuotaChipBarColorTests` (bar-color ramp) or
/// `QuotaChipClockTests` (clock-dependent rendering), so this is a third,
/// narrowly-scoped file rather than a fourth concern bolted onto either.
///
/// `usageCreditsLine` is `static` and non-private specifically so this file
/// can reach it — `quotaTooltip` itself is `private` on a `View` struct,
/// which `@testable import` does not bypass.
final class QuotaChipModeTests: XCTestCase {

    private let now = PinnedNowSnapshot.referenceNow

    // MARK: - usageCreditsLine (issue #1995 T2: currency, and a real zero balance)

    /// Red-first for #1995: a non-USD balance renders with a hardcoded "$"
    /// today. Mirrors the web case in quotaChips.test.js exactly.
    func testDoesNotRenderADollarSignForANonDollarBalance() {
        let credits = CreditsInfo(hasCredits: true, unlimited: nil, balance: 12.34,
                                  balanceObserved: true, currency: "CNY")
        let line = SessionListView.usageCreditsLine(credits)
        XCTAssertNotNil(line)
        XCTAssertFalse(line?.contains("$") ?? true,
                       "a CNY balance must not render with a \"$\" prefix, got \(line ?? "nil")")
    }

    /// An unlabeled (legacy, or explicitly "USD") balance keeps today's "$"
    /// rendering — this is the lock half of the T2 pair.
    func testStillRendersADollarSignForAnUnlabeledOrUSDBalance() {
        let unlabeled = CreditsInfo(hasCredits: true, unlimited: nil, balance: 5, balanceObserved: true, currency: nil)
        XCTAssertEqual(SessionListView.usageCreditsLine(unlabeled), "Credits balance: $5.00")
        let usd = CreditsInfo(hasCredits: true, unlimited: nil, balance: 5, balanceObserved: true, currency: "USD")
        XCTAssertEqual(SessionListView.usageCreditsLine(usd), "Credits balance: $5.00")
    }

    /// Red-first for #1995: `balance`'s own `omitempty` on the wire drops a
    /// genuine zero exactly like an absent value (core/domain/session/
    /// rate_limit.go), so a real zero balance decodes with `balance == nil`
    /// — today that falls through to `hasCredits`/nil instead of rendering
    /// the observed zero. `balanceObserved` is the only field that survives
    /// the round trip to tell the two apart.
    func testPreservesAnObservedZeroBalance() {
        let observedZero = CreditsInfo(hasCredits: true, unlimited: nil, balance: nil,
                                       balanceObserved: true, currency: nil)
        XCTAssertEqual(SessionListView.usageCreditsLine(observedZero), "Credits balance: $0.00",
                       "an observed zero balance must render as zero, not fall through to "
                       + "\"Credits: available\" or nil")
    }

    /// A balance that was never observed at all (no `balanceObserved`, no
    /// `balance`) must NOT render as zero — that would be a guess, not a
    /// reading. Falls through to `hasCredits`.
    func testDoesNotGuessAZeroBalanceWhenNoneWasObserved() {
        let neverObserved = CreditsInfo(hasCredits: true, unlimited: nil, balance: nil,
                                        balanceObserved: nil, currency: nil)
        XCTAssertEqual(SessionListView.usageCreditsLine(neverObserved), "Credits: available")
    }

    func testUnlimitedTakesPrecedenceOverBalance() {
        let unlimited = CreditsInfo(hasCredits: true, unlimited: true, balance: 100,
                                    balanceObserved: true, currency: "CNY")
        XCTAssertEqual(SessionListView.usageCreditsLine(unlimited), "Credits: unlimited")
    }

    func testNilCreditsRenderNoLine() {
        XCTAssertNil(SessionListView.usageCreditsLine(nil))
    }

    // MARK: - resolveChipMode (issue #1995: agree with quotaChips.js's chipModeFor)

    /// This is the one case whose verdict changes: windows AND credits AND
    /// no plan_type used to resolve `.usage` (keyed on `planType` being
    /// empty); it now resolves `.subscription` (keyed on windows being
    /// present), matching `quotaChips.js`'s `chipModeFor` after #1995 —
    /// the two clients had silently diverged on exactly this shape.
    ///
    /// Mutation-proved: reverting `resolveChipMode`'s auto branch to the
    /// old `(snap.credits != nil && (snap.planType ?? "").isEmpty) ?
    /// .usage : .subscription` reddened this test alone — every other
    /// case in this file stayed green, confirming this is the one shape
    /// the change actually affects.
    func testAutoPrefersSubscriptionWhenWindowsArePresentEvenWithCredits() {
        let snap = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 10, windowMinutes: 300,
                                          resetsAt: now.addingTimeInterval(3600))],
            credits: CreditsInfo(hasCredits: true, unlimited: nil, balance: 5, balanceObserved: true, currency: nil),
            sampledAt: now
        )
        XCTAssertEqual(SessionListView.resolveChipMode(snap: snap, providerKey: "auto-test-1"), .subscription)
    }

    func testAutoStillPrefersUsageWhenOnlyCreditsArePresent() {
        let snap = RateLimitInfo(
            windows: [],
            credits: CreditsInfo(hasCredits: true, unlimited: nil, balance: 5, balanceObserved: true, currency: nil),
            sampledAt: now
        )
        XCTAssertEqual(SessionListView.resolveChipMode(snap: snap, providerKey: "auto-test-2"), .usage)
    }

    func testAutoFallsBackToSubscriptionWithNeitherWindowsNorCredits() {
        let snap = RateLimitInfo(windows: [], sampledAt: now)
        XCTAssertEqual(SessionListView.resolveChipMode(snap: snap, providerKey: "auto-test-3"), .subscription)
    }

    // Deliberately no "explicit preference overrides auto" case here:
    // ProviderModePreference.current(for:) reads UserDefaults.standard
    // directly with no injectable store, and PersistentDefaultsLintTests
    // bans a UserDefaults.standard MUTATION anywhere in the test targets —
    // exercising the .subscription/.usage preference branches would need
    // exactly that. All four cases above use a providerKey no test has ever
    // written a preference under, so `.current(for:)` reads its own default
    // (.auto) and the auto-detection branch is what's actually exercised.
}
