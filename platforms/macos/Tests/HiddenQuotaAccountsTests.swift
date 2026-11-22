import AppKit
import XCTest
@testable import Irrlicht

/// #2163: the user picks which quota accounts are shown. A hidden account
/// draws no popover chip, is never picked for a provider's menu-bar slot, and
/// stays listed in Settings after it stops reporting. Same fixtures as
/// `QuotaAccountBucketTests`, whose tests stay unchanged as the locks for
/// "nothing hidden".
@MainActor
final class HiddenQuotaAccountsTests: XCTestCase {
    private let base = Date(timeIntervalSince1970: 1_800_000_000)

    private func snap(used: Double, sampledAt: Date? = nil, provider: String? = "anthropic",
                      ref: String?, planType: String? = nil) -> RateLimitInfo {
        RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: used, windowMinutes: 300,
                                          resetsAt: base.addingTimeInterval(3600))],
            planType: planType,
            sampledAt: sampledAt ?? base.addingTimeInterval(-60),
            provider: provider,
            attributionQuality: provider == nil ? nil : RateLimitInfo.attributionQualityConfirmed,
            confirmedAccountRef: ref
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

    /// Two confirmed Claude accounts: `acct-A` is the less constrained one.
    private var twoAccounts: [SessionState] {
        [
            session("a", snap(used: 20, ref: "acct-AAAA", planType: "max")),
            session("b", snap(used: 80, ref: "acct-BBBB", planType: "team")),
        ]
    }

    // MARK: - red-first: popover chips

    func testAHiddenAccountDrawsNoChip() {
        let chips = SessionListView.quotaChipData(
            sessions: twoAccounts, hiddenAccounts: ["anthropic|acct-BBBB"], now: base)
        XCTAssertEqual(chips.map(\.id), ["anthropic|acct-AAAA"],
                       "the hidden account's chip must be dropped, the other kept")
    }

    /// Showing it again is the absence of the key, read on the next render; no
    /// state survives from the hidden render.
    func testShowingAnAccountAgainBringsItsChipBack() {
        _ = SessionListView.quotaChipData(
            sessions: twoAccounts, hiddenAccounts: ["anthropic|acct-BBBB"], now: base)
        let chips = SessionListView.quotaChipData(sessions: twoAccounts, hiddenAccounts: [], now: base)
        XCTAssertEqual(chips.map(\.id), ["anthropic|acct-AAAA", "anthropic|acct-BBBB"])
    }

    func testHidingEveryAccountOfAProviderDrawsNoChipForIt() {
        let chips = SessionListView.quotaChipData(
            sessions: twoAccounts + [session("o", adapter: "codex", snap(used: 5, provider: "openai", ref: "seat-1"))],
            hiddenAccounts: ["anthropic|acct-AAAA", "anthropic|acct-BBBB"], now: base)
        XCTAssertEqual(chips.map(\.id), ["openai|seat-1"])
    }

    /// The unconfirmed bucket is a row of its own and can be hidden alone.
    func testTheUnconfirmedBucketCanBeHiddenOnItsOwn() {
        let sessions = twoAccounts + [session("u", snap(used: 50, ref: nil))]
        let chips = SessionListView.quotaChipData(
            sessions: sessions, hiddenAccounts: ["anthropic|unconfirmed"], now: base)
        XCTAssertEqual(chips.map(\.id), ["anthropic|acct-AAAA", "anthropic|acct-BBBB"])
    }

    // MARK: - red-first: menu-bar pick

    /// The hidden account is the more constrained one, which D1 (#2095) would
    /// otherwise pick.
    func testTheMenuBarSlotShowsTheVisibleAccountEvenWhenTheHiddenOneIsMoreConstrained() {
        for providerKey in ["anthropic", nil] as [String?] {
            let got = QuotaMenuBarRenderer.selectedSnapshot(
                sessions: twoAccounts, providerKey: providerKey,
                hiddenAccounts: ["anthropic|acct-BBBB"], now: base)
            XCTAssertEqual(got?.confirmedAccountRef, "acct-AAAA", "providerKey=\(providerKey ?? "Auto")")
        }
    }

    /// A selected provider whose accounts are all hidden gets no slot, and
    /// Auto skips it even though it reported most recently.
    func testAProviderWithEveryAccountHiddenGetsNoSlotAndAutoSkipsIt() {
        let codex = session("o", adapter: "codex",
                            snap(used: 5, sampledAt: base.addingTimeInterval(-600), provider: "openai", ref: "seat-1"))
        let sessions = twoAccounts + [codex]
        let hidden: Set<String> = ["anthropic|acct-AAAA", "anthropic|acct-BBBB"]
        XCTAssertNil(QuotaMenuBarRenderer.selectedSnapshot(
            sessions: sessions, providerKey: "anthropic", hiddenAccounts: hidden, now: base))
        XCTAssertEqual(QuotaMenuBarRenderer.selectedSnapshot(
            sessions: sessions, providerKey: nil, hiddenAccounts: hidden, now: base)?.provider, "openai",
            "Auto must pick the provider that still has a visible account")
    }

    // MARK: - red-first: Settings rows

    func testAHiddenAccountThatNoSessionReportsStaysListedWithItsStoredLabel() {
        let live = SessionListView.quotaChipData(sessions: [twoAccounts[0]], now: base)
        let rows = SettingsView.quotaAccountRows(
            live: live, hidden: ["anthropic|acct-BBBB": "Team", "anthropic|unconfirmed": ""])
        XCTAssertEqual(rows.map(\.key), ["anthropic|acct-AAAA", "anthropic|acct-BBBB", "anthropic|unconfirmed"])
        XCTAssertEqual(rows.map(\.label), [
            "Claude Max · Account …AAAA",
            "Team · Account …BBBB — no data",
            "Claude · Account unconfirmed — no data",
        ])
        XCTAssertEqual(rows.map(\.hasLiveData), [true, false, false])
        // What hiding a row would store: the live tier for a reporting
        // account, the stored one for the others.
        XCTAssertEqual(rows.map(\.tierLabel), ["Claude Max", "Team", ""])
    }

    /// A hidden account that is still reporting is labelled from its live
    /// snapshot, not marked as having no data.
    func testAHiddenAccountThatStillReportsIsLabelledLive() {
        let live = SessionListView.quotaChipData(sessions: twoAccounts, now: base)
        let rows = SettingsView.quotaAccountRows(live: live, hidden: ["anthropic|acct-BBBB": "stale label"])
        XCTAssertEqual(rows.map(\.label), ["Claude Max · Account …AAAA", "Team · Account …BBBB"])
        XCTAssertEqual(rows.map(\.tierLabel), ["Claude Max", "Team"],
                       "hiding a reporting account must store its live tier, not an old stored one")
    }

    func testHidingARowStoresItsTierLabelAndShowingItRemovesTheKey() {
        let row = SettingsView.QuotaAccountRow(key: "anthropic|acct-BBBB", tierLabel: "Team", hasLiveData: true)
        let hiddenRaw = SettingsView.settingQuotaAccount(row, shown: false, in: "")
        XCTAssertEqual(HiddenQuotaAccounts.decode(hiddenRaw), ["anthropic|acct-BBBB": "Team"])
        XCTAssertEqual(SettingsView.settingQuotaAccount(row, shown: true, in: hiddenRaw), "",
                       "showing the last hidden account must store the never-set value")
    }

    // MARK: - the menu-bar hand-off

    /// `combinedImage` → `iconImage` → `quotaImage` → `imageForSelectedProvider`
    /// → `selectedSnapshot` each pass the hidden set on, and each defaults it
    /// to "nothing hidden", so a dropped argument still compiles. These assert
    /// through the top two, on both of `quotaImage`'s paths (Auto and a
    /// selected provider). The first link is pinned by source in
    /// `MenuBarAppearanceTests.testImageBuilderRoutesBothHalvesThroughTheExtractedSeams`.
    ///
    /// Mutations run with `tools/mutate.sh` in #2163's review: deleting the
    /// argument between `iconImage` and `quotaImage` turned
    /// `testTheIconFallsBackToItsDotsWhenEveryAccountIsHidden` red on both
    /// paths; deleting it on `quotaImage`'s selected-provider call turned both
    /// tests below red for `["anthropic"]`; deleting `combinedImage`'s argument
    /// turned the source pin red.
    private var usage: MenuBarAppearance {
        MenuBarAppearance(style: .usage, grouping: .project, maxProjects: MenuBarMaxProjects.defaultValue)
    }

    func testTheQuotaHalfDrawsTheVisibleAccountOnBothPaths() throws {
        for providerKeys in [[], ["anthropic"]] as [[String]] {
            func quota(_ sessions: [SessionState], hidden: Set<String>) throws -> NSImage {
                try XCTUnwrap(MenuBarImageBuilder.quotaImage(
                    appearance: usage, sessions: sessions, providerKeys: providerKeys,
                    hiddenAccounts: hidden, now: base))
            }
            let filtered = try quota(twoAccounts, hidden: ["anthropic|acct-BBBB"])
            let visibleOnly = try quota([twoAccounts[0]], hidden: [])
            let unfiltered = try quota(twoAccounts, hidden: [])
            XCTAssertEqual(filtered.tiffRepresentation, visibleOnly.tiffRepresentation,
                           "providerKeys=\(providerKeys): the slot must draw the visible account")
            // Vacuity guard: unfiltered, D1 picks the hidden (more constrained)
            // account, so the two renders must differ for the line above to mean
            // anything.
            XCTAssertNotEqual(unfiltered.tiffRepresentation, visibleOnly.tiffRepresentation,
                              "providerKeys=\(providerKeys): the fixture must make the hidden account the pick")
        }
    }

    func testTheIconFallsBackToItsDotsWhenEveryAccountIsHidden() throws {
        let allHidden: Set<String> = ["anthropic|acct-AAAA", "anthropic|acct-BBBB"]
        let dots = try XCTUnwrap(MenuBarImageBuilder.dotsImage(
            appearance: usage, sessions: twoAccounts, projectGroupOrder: [], daemonLabels: [:]))
        for providerKeys in [[], ["anthropic"]] as [[String]] {
            func icon(hidden: Set<String>) throws -> NSImage {
                try XCTUnwrap(MenuBarImageBuilder.iconImage(
                    appearance: usage, sessions: twoAccounts, projectGroupOrder: [], daemonLabels: [:],
                    providerKeys: providerKeys, hiddenAccounts: hidden, now: base))
            }
            XCTAssertEqual(try icon(hidden: allHidden).size.width, dots.size.width, accuracy: 0.01,
                           "providerKeys=\(providerKeys): with no visible account .usage must compose its dots")
            XCTAssertNotEqual(try icon(hidden: []).size.width, dots.size.width,
                              "providerKeys=\(providerKeys): vacuity — with nothing hidden the quota half draws")
        }
    }

    // MARK: - storage

    func testTheStoredValueRoundTripsKeysWithAnyPunctuation() {
        let hidden = ["anthropic|a,b|c\"d": "Claude Max", "unknown:pi|unconfirmed": "", "openai|seat 1": "Plus"]
        XCTAssertEqual(HiddenQuotaAccounts.decode(HiddenQuotaAccounts.encode(hidden)), hidden)
    }

    /// Anything unreadable degrades to "nothing hidden", the default.
    func testAnUnreadableStoredValueHidesNothing() {
        for raw in ["", "not json", "[\"anthropic|acct-A\"]", "42", "{\"\": \"x\"}"] {
            XCTAssertEqual(HiddenQuotaAccounts.decode(raw), [:], "raw=\(raw)")
        }
        XCTAssertEqual(HiddenQuotaAccounts.decode("{\"anthropic|acct-A\": 7}"), ["anthropic|acct-A": ""],
                       "a non-string label keeps the key hidden with no label")
    }

    func testTheKeyIsReadFromTheStoreItIsGiven() {
        let defaults = InMemoryDefaults()
        XCTAssertEqual(HiddenQuotaAccounts.current(in: defaults), [:])
        defaults.set(HiddenQuotaAccounts.encode(["anthropic|acct-A": "Max"]), forKey: HiddenQuotaAccounts.storageKey)
        XCTAssertEqual(HiddenQuotaAccounts.current(in: defaults), ["anthropic|acct-A": "Max"])
    }
}
