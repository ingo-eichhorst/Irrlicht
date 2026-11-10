import XCTest
@testable import Irrlicht

/// #2095 — one quota chip per account. Covers the macOS side of the shared
/// bucketing contract: `mergeIntoBuckets` (popover) and
/// `QuotaMenuBarRenderer.selectedSnapshot` (menu bar) both key by
/// `RateLimitInfo.accountBucketKey`.
///
/// #2136 revised #2095's D3 for the unconfirmed case: every session of one
/// provider whose snapshot has no confirmed account shares ONE bucket
/// (`<provider>|unconfirmed`) instead of getting a bucket each. A confirmed
/// account never merges with that bucket or with another account. The #2136
/// tests below assert through `mergeIntoBuckets` / `selectedSnapshot` rather
/// than calling `accountBucketKey` directly, so they compile unchanged on
/// both sides of the fix.
@MainActor
final class QuotaAccountBucketTests: XCTestCase {
    private let base = Date(timeIntervalSince1970: 1_800_000_000)
    /// Pinned clock for the menu-bar selection tests (no wall-clock reads).
    private lazy var wall: Date = base

    private func snap(used: Double, sampledAt: Date, resetsAt: Date, provider: String? = "anthropic",
                       ref: String?, product: String? = nil) -> RateLimitInfo {
        RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: used, windowMinutes: 300, resetsAt: resetsAt)],
            sampledAt: sampledAt,
            provider: provider,
            attributionQuality: provider == nil ? nil : RateLimitInfo.attributionQualityConfirmed,
            confirmedAccountRef: ref, product: product
        )
    }

    private func session(_ id: String, adapter: String = "claude-code", _ rl: RateLimitInfo) -> SessionState {
        SessionState(
            id: "sess_\(id)", state: .working, model: "claude-sonnet",
            cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: base, updatedAt: base,
            metrics: SessionMetrics(
                elapsedSeconds: 0, totalTokens: 0, modelName: "claude-sonnet", contextWindow: nil,
                contextUtilization: 0, pressureLevel: "safe", contextWindowUnknown: nil,
                estimatedCostUSD: nil, lastAssistantText: nil, tasks: nil, rateLimit: rl),
            adapter: adapter)
    }

    private func fold(_ sessions: [SessionState], now: Date) -> [String: SessionListView.ChipBucket] {
        var b: [String: SessionListView.ChipBucket] = [:]
        for s in sessions { SessionListView.mergeIntoBuckets(session: s, into: &b, now: now) }
        return b
    }

    // MARK: - red-first

    /// D-a: two confirmed accounts, interleaved sampling over several folds.
    func testTwoConfirmedAccountsNeverShareABucket() {
        let reset = base.addingTimeInterval(3600)
        var sessions: [SessionState] = []
        for i in 0..<6 {
            let a = snap(used: 20, sampledAt: base.addingTimeInterval(Double(i * 2)), resetsAt: reset, ref: "acct-A")
            let b = snap(used: 80, sampledAt: base.addingTimeInterval(Double(i * 2 + 1)), resetsAt: reset, ref: "acct-B")
            sessions += [session("a\(i)", a), session("b\(i)", b)]
        }
        let buckets = fold(sessions, now: base)
        XCTAssertEqual(buckets.count, 2, "two accounts must be two buckets")
        XCTAssertEqual(buckets["anthropic|acct-A"]?.snapshot.windows.first?.usedPercent, 20)
        XCTAssertEqual(buckets["anthropic|acct-B"]?.snapshot.windows.first?.usedPercent, 80)
    }

    /// D-b, flipped by #2136 (was `testUnconfirmedSessionsNeverFoldTogether`):
    /// an empty and a missing ref are both unconfirmed, so one provider's
    /// sessions share its one unconfirmed bucket and the newer reading wins.
    /// An unknown provider's sessions are unconfirmed too, so one adapter's
    /// share one bucket, as they did in v0.6.7.
    func testUnconfirmedSessionsOfOneProviderShareOneBucket() {
        let reset = base.addingTimeInterval(3600)
        let confirmedNoRef = fold([
            session("1", snap(used: 10, sampledAt: base, resetsAt: reset, ref: "")),
            session("2", snap(used: 30, sampledAt: base.addingTimeInterval(1), resetsAt: reset, ref: nil)),
        ], now: base)
        XCTAssertEqual(Array(confirmedNoRef.keys), ["anthropic|unconfirmed"],
                       "confirmed provider with no ref: one bucket for the provider")
        XCTAssertEqual(confirmedNoRef["anthropic|unconfirmed"]?.snapshot.windows.first?.usedPercent, 30)
        let unknown = fold([
            session("1", adapter: "pi", snap(used: 10, sampledAt: base, resetsAt: reset, provider: nil, ref: nil)),
            session("2", adapter: "pi", snap(used: 30, sampledAt: base.addingTimeInterval(1), resetsAt: reset, provider: nil, ref: nil)),
        ], now: base)
        XCTAssertEqual(Array(unknown.keys), ["unknown:pi|unconfirmed"],
                       "unknown provider of one adapter: one bucket for the adapter")
        XCTAssertEqual(unknown["unknown:pi|unconfirmed"]?.snapshot.windows.first?.usedPercent, 30)
    }

    /// D-c: the FRESHER snapshot is the LESS constrained account; the menu bar
    /// must show the more constrained one regardless of sampling order.
    func testSelectedSnapshotIsDeterministicAcrossInterleavedTicks() {
        let reset = wall.addingTimeInterval(3600)
        for aNewer in [true, false] {
            let a = snap(used: 20, sampledAt: wall.addingTimeInterval(aNewer ? -5 : -50), resetsAt: reset, ref: "acct-A")
            let b = snap(used: 80, sampledAt: wall.addingTimeInterval(aNewer ? -50 : -5), resetsAt: reset, ref: "acct-B")
            for order in [[session("a", a), session("b", b)], [session("b", b), session("a", a)]] {
                let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: order, providerKey: "anthropic", now: wall)
                XCTAssertEqual(got?.windows.first?.usedPercent, 80, "aNewer=\(aNewer)")
            }
        }
    }

    // MARK: - #2136: one unconfirmed bucket per provider

    /// Red on main: two buckets (`anthropic|session:sess_older`,
    /// `anthropic|session:sess_newer`). One subscription, two Claude Code
    /// sessions whose account is unconfirmed (consent pending, or running
    /// before the account was first seen): one chip, the newer reading.
    func testTwoUnconfirmedSessionsOfOneProviderShareOneBucketWithTheNewerReading() {
        let reset = base.addingTimeInterval(7200)
        let older = session("older", snap(used: 6, sampledAt: base.addingTimeInterval(-1800), resetsAt: reset, ref: nil))
        let newer = session("newer", snap(used: 22, sampledAt: base.addingTimeInterval(-120), resetsAt: reset, ref: nil))
        for order in [[older, newer], [newer, older]] {
            let buckets = fold(order, now: base)
            XCTAssertEqual(Array(buckets.keys), ["anthropic|unconfirmed"])
            let bucket = buckets["anthropic|unconfirmed"]
            XCTAssertEqual(bucket?.snapshot.windows.first?.usedPercent, 22)
            XCTAssertEqual(bucket?.session.id, "sess_newer")
            XCTAssertEqual(bucket?.accountRef, "")
            let chips = SessionListView.quotaChipData(sessions: order, now: base)
            XCTAssertEqual(chips.count, 1)
            XCTAssertEqual(chips.first.map { RateLimitInfo.accountLine(ref: $0.accountRef) }, "Account unconfirmed",
                           "the shared chip keeps its \"Account unconfirmed\" tooltip line")
        }
    }

    /// Lock (passes on main too): a confirmed account never absorbs the
    /// provider's unconfirmed bucket. Asserted through `accountRef`, not the
    /// bucket key, so it reads the same on both sides of the key change.
    func testAConfirmedAndAnUnconfirmedSessionOfOneProviderStayTwoBuckets() {
        let reset = base.addingTimeInterval(7200)
        let confirmed = session("acct", snap(used: 10, sampledAt: base.addingTimeInterval(-1800), resetsAt: reset, ref: "acct-A"))
        let unconfirmed = session("unc", snap(used: 30, sampledAt: base.addingTimeInterval(-120), resetsAt: reset, ref: nil))
        for order in [[confirmed, unconfirmed], [unconfirmed, confirmed]] {
            let chips = SessionListView.quotaChipData(sessions: order, now: base)
            XCTAssertEqual(chips.count, 2)
            XCTAssertEqual(chips.first { $0.accountRef == "acct-A" }?.snapshot.windows.first?.usedPercent, 10)
            XCTAssertEqual(chips.first { $0.accountRef.isEmpty }?.snapshot.windows.first?.usedPercent, 30)
        }
    }

    /// Lock (passes on main too): two confirmed accounts keep their own
    /// buckets, keyed `<provider>|<ref>` on both sides of the fix.
    func testTwoDifferentConfirmedRefsStayTwoBuckets() {
        let reset = base.addingTimeInterval(7200)
        let buckets = fold([
            session("b", snap(used: 80, sampledAt: base.addingTimeInterval(-120), resetsAt: reset, ref: "acct-B")),
            session("a", snap(used: 10, sampledAt: base.addingTimeInterval(-1800), resetsAt: reset, ref: "acct-A")),
        ], now: base)
        XCTAssertEqual(buckets.keys.sorted(), ["anthropic|acct-A", "anthropic|acct-B"])
        XCTAssertEqual(buckets["anthropic|acct-A"]?.snapshot.windows.first?.usedPercent, 10)
        XCTAssertEqual(buckets["anthropic|acct-B"]?.snapshot.windows.first?.usedPercent, 80)
    }

    /// Red on main: three buckets. The Muse account-API sweep stamps the same
    /// Meta snapshot onto every live top-level Muse session, and its ref is
    /// always empty (`museaccountapi.accountRef()`), so N Muse sessions must
    /// give one Meta chip, as in v0.6.7.
    func testMuseSessionsCarryingTheSameMetaSnapshotShareOneBucket() {
        let meta = RateLimitInfo(
            windows: [
                RateLimitWindowInfo(usedPercent: 12, windowMinutes: 300, resetsAt: base.addingTimeInterval(7200)),
                RateLimitWindowInfo(usedPercent: 35, windowMinutes: 10080, resetsAt: base.addingTimeInterval(4 * 86400)),
            ],
            sampledAt: base.addingTimeInterval(-30),
            provider: "meta",
            attributionQuality: RateLimitInfo.attributionQualityConfirmed,
            attributionEvidence: "muse_account_api", observationSource: "account_api",
            confirmedAccountRef: nil, product: "Muse Code Everyday Usage"
        )
        let buckets = fold([
            session("m1", adapter: "muse", meta),
            session("m2", adapter: "muse", meta),
            session("m3", adapter: "muse", meta),
        ], now: base)
        XCTAssertEqual(Array(buckets.keys), ["meta|unconfirmed"])
        XCTAssertEqual(buckets["meta|unconfirmed"]?.providerKey, "meta")
    }

    /// Red on main: the menu bar treated each unconfirmed session as its own
    /// account and showed the most constrained (80%, the OLDER reading).
    /// Sharing one bucket, the in-account rule applies: fresh over stale, then
    /// newest `sampledAt` (20%). The older reading is deliberately the more
    /// used one so the two rules disagree.
    func testSelectedSnapshotTreatsUnconfirmedSessionsOfOneProviderAsOneAccount() {
        let reset = wall.addingTimeInterval(3600)
        let older = session("older", snap(used: 80, sampledAt: wall.addingTimeInterval(-50), resetsAt: reset, ref: nil))
        let newer = session("newer", snap(used: 20, sampledAt: wall.addingTimeInterval(-5), resetsAt: reset, ref: nil))
        for providerKey in ["anthropic", nil] as [String?] {
            for order in [[older, newer], [newer, older]] {
                let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: order, providerKey: providerKey, now: wall)
                XCTAssertEqual(got?.windows.first?.usedPercent, 20, "providerKey=\(providerKey ?? "auto")")
            }
        }
    }

    // MARK: - extra coverage

    func testFreshBeatsStaleAcrossAccountsAndStaleStillReturnedAlone() {
        let fresh = snap(used: 10, sampledAt: wall.addingTimeInterval(-9), resetsAt: wall.addingTimeInterval(3600), ref: "acct-A")
        let stale = snap(used: 90, sampledAt: wall.addingTimeInterval(-5), resetsAt: wall.addingTimeInterval(-3600), ref: "acct-B")
        let both = QuotaMenuBarRenderer.selectedSnapshot(sessions: [session("a", fresh), session("b", stale)], providerKey: nil, now: wall)
        XCTAssertEqual(both?.windows.first?.usedPercent, 10)
        let only = QuotaMenuBarRenderer.selectedSnapshot(sessions: [session("b", stale)], providerKey: nil, now: wall)
        XCTAssertEqual(only?.windows.first?.usedPercent, 90)
    }

    func testTieBreaksByBucketKeyAscending() {
        let reset = wall.addingTimeInterval(3600)
        let a = snap(used: 50, sampledAt: wall.addingTimeInterval(-50), resetsAt: reset, ref: "acct-A", product: "claude_max")
        let b = snap(used: 50, sampledAt: wall.addingTimeInterval(-5), resetsAt: reset, ref: "acct-B", product: "claude_pro")
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [session("b", b), session("a", a)], providerKey: nil, now: wall)
        XCTAssertEqual(got?.confirmedAccountRef, "acct-A")
    }

    /// Lock: the mode preference / cost lookups key on the bare provider, not
    /// the bucket key (existing `providerMode_<key>` settings must keep matching).
    func testBucketCarriesBareProviderKeyAndAccountRef() {
        let reset = base.addingTimeInterval(3600)
        let chips = SessionListView.quotaChipData(sessions: [
            session("a", snap(used: 20, sampledAt: base, resetsAt: reset, ref: "acct-A")),
            session("u", adapter: "pi", snap(used: 5, sampledAt: base, resetsAt: reset, provider: nil, ref: nil)),
        ], now: base)
        XCTAssertEqual(chips.map(\.providerKey), ["anthropic", "unknown:pi"])
        XCTAssertEqual(chips.map(\.accountRef), ["acct-A", ""])
        XCTAssertEqual(ProviderModePreference.storageKey(providerKey: chips[0].providerKey), "providerMode_anthropic")
    }

    func testProductLabelAndAccountSuffix() {
        let r = { (p: String?) in self.snap(used: 1, sampledAt: self.base, resetsAt: self.base, ref: "x", product: p) }
        XCTAssertEqual(r("claude_team").productLabel, "Claude Team")
        XCTAssertEqual(r("some_new_tier").productLabel, "Some New Tier")
        XCTAssertNil(r(nil).productLabel)
        XCTAssertEqual(RateLimitInfo.accountLine(ref: "acct-1234abcd"), "Account …abcd")
        XCTAssertEqual(RateLimitInfo.accountLine(ref: ""), "Account unconfirmed")
    }

    func testWireRoundTrip() throws {
        let json = #"{"windows":[],"sampled_at":1,"confirmed_account_ref":"acct-9","product":"claude_pro"}"#
        let d = try JSONDecoder().decode(RateLimitInfo.self, from: Data(json.utf8))
        XCTAssertEqual(d.confirmedAccountRef, "acct-9")
        XCTAssertEqual(d.product, "claude_pro")
        let back = try JSONDecoder().decode(RateLimitInfo.self, from: JSONEncoder().encode(d))
        XCTAssertEqual(back.confirmedAccountRef, "acct-9")
    }

    /// Auto mode (no provider picked) keeps its documented meaning: the
    /// provider that reported most recently, NOT the most constrained provider.
    func testAutoModePicksTheMostRecentlyReportingProviderNotTheMostConstrained() {
        let reset = wall.addingTimeInterval(3600)
        let anthropic = snap(used: 30, sampledAt: wall.addingTimeInterval(-5), resetsAt: reset, ref: "acct-A")
        let openai = snap(used: 70, sampledAt: wall.addingTimeInterval(-3600), resetsAt: reset, provider: "openai", ref: "acct-O")
        let got = QuotaMenuBarRenderer.selectedSnapshot(
            sessions: [session("o", adapter: "codex", openai), session("a", anthropic)], providerKey: nil, now: wall)
        XCTAssertEqual(got?.windows.first?.usedPercent, 30)
    }

    /// Auto mode, then D1 inside the chosen provider: the two anthropic accounts
    /// are the most recent, so the more constrained of them wins.
    func testAutoModeAppliesD1WithinTheMostRecentProvider() {
        let reset = wall.addingTimeInterval(3600)
        let a = snap(used: 20, sampledAt: wall.addingTimeInterval(-5), resetsAt: reset, ref: "acct-A")
        let b = snap(used: 80, sampledAt: wall.addingTimeInterval(-50), resetsAt: reset, ref: "acct-B")
        let openai = snap(used: 95, sampledAt: wall.addingTimeInterval(-3600), resetsAt: reset, provider: "openai", ref: "acct-O")
        let got = QuotaMenuBarRenderer.selectedSnapshot(
            sessions: [session("o", adapter: "codex", openai), session("a", a), session("b", b)], providerKey: nil, now: wall)
        XCTAssertEqual(got?.windows.first?.usedPercent, 80)
    }
}
