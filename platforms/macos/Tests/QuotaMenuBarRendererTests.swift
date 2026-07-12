import XCTest
@testable import Irrlicht

/// Coverage for the menu-bar quota icon (issue #909): the stacked 5h/7d
/// bars, the single-window compact ring, the pace marker both share, the
/// pace-aware color ramp (must mirror SessionListView.barColor exactly —
/// see QuotaChipBarColorTests for the popover-side pin of the same table),
/// and the freshest-renderable snapshot selection across live sessions.
@MainActor
final class QuotaMenuBarRendererTests: XCTestCase {

    // MARK: - buildSVG (bars)

    func testBuildSVGReturnsNilWhenNoWindows() {
        let info = RateLimitInfo(windows: [], sampledAt: Date())
        XCTAssertNil(QuotaMenuBarRenderer.buildSVG(for: info))
    }

    func testBuildSVGIncludesBothRowsWhenBothWindowsPresent() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let result = QuotaMenuBarRenderer.buildSVG(for: info)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains(">5h<"))
        XCTAssertTrue(result!.svg.contains(">7d<"))
    }

    func testBuildSVGOmitsMissingWindowRow() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 40, windowMinutes: 10080, resetsAt: Date().addingTimeInterval(3600))],
            sampledAt: Date()
        )
        let result = QuotaMenuBarRenderer.buildSVG(for: info)
        XCTAssertNotNil(result)
        XCTAssertFalse(result!.svg.contains(">5h<"))
        XCTAssertTrue(result!.svg.contains(">7d<"))
    }

    // MARK: - buildSVG compact mode (Combined style: no labels, narrower bars)

    func testBuildSVGCompactOmitsWindowLabels() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let result = QuotaMenuBarRenderer.buildSVG(for: info, compact: true)
        XCTAssertNotNil(result)
        XCTAssertFalse(result!.svg.contains(">5h<"))
        XCTAssertFalse(result!.svg.contains(">7d<"))
    }

    func testBuildSVGCompactIsNarrowerThanDefault() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let normal = QuotaMenuBarRenderer.buildSVG(for: info, compact: false)
        let compact = QuotaMenuBarRenderer.buildSVG(for: info, compact: true)
        XCTAssertNotNil(normal)
        XCTAssertNotNil(compact)
        XCTAssertLessThan(compact!.width, normal!.width)
        // compact's total width IS the bar width (no label/gap column), so
        // it can be compared directly against the known 32pt default bar
        // width: the requested range is 30-40% narrower, i.e. 60-70% of 32.
        XCTAssertGreaterThanOrEqual(compact!.width, 32 * 0.60)
        XCTAssertLessThanOrEqual(compact!.width, 32 * 0.70)
    }

    func testBuildSVGCompactDefaultsToFalseWhenOmitted() {
        let info = makeInfo(fiveHour: 20, sevenDay: 40)
        let omitted = QuotaMenuBarRenderer.buildSVG(for: info)
        let explicitFalse = QuotaMenuBarRenderer.buildSVG(for: info, compact: false)
        XCTAssertEqual(omitted?.width, explicitFalse?.width)
        XCTAssertTrue(omitted!.svg.contains(">5h<"))
    }

    /// Same ramp QuotaChipBarColorTests pins for SessionListView.barColor
    /// — this renderer must reach the identical verdict (as a bare hex
    /// instead of a SwiftUI Color) so the same window can't read green in
    /// the icon while the popover shows it orange. Cases stay off the exact
    /// threshold boundary on purpose: `windowWithPace` derives `pace` from
    /// `Date()` at construction time and `buildSVG` re-evaluates it a moment
    /// later, so an exact-boundary delta (e.g. precisely 5 or 15) can tip
    /// either way on sub-millisecond wall-clock drift. Exact-boundary
    /// pinning already lives in QuotaChipBarColorTests, which calls
    /// `barColor(used:pace:)` directly with fixed Doubles and has none of
    /// that drift.
    func testBuildSVGColorThresholdsMirrorPopoverPaceRamp() {
        let cases: [(name: String, used: Double, pace: Double, wantHex: String)] = [
            ("on pace",                30, 30, "34C759"),
            ("clearly still green",    33, 30, "34C759"),
            ("clearly yellow",         36, 30, "FFCC00"),
            ("clearly orange",         46, 30, "FF9500"),
            ("far ahead",              70, 30, "FF9500"),
            ("behind pace",            20, 40, "34C759"),
            ("at cap (85%) overrides pace", 85, 50, "FF9500"),
        ]
        for c in cases {
            let window = windowWithPace(usedPercent: c.used, pace: c.pace)
            let info = RateLimitInfo(windows: [window], sampledAt: Date())
            let result = QuotaMenuBarRenderer.buildSVG(for: info)
            XCTAssertTrue(
                result?.svg.contains("#\(c.wantHex)") ?? false,
                "\(c.name): expected #\(c.wantHex) in \(result?.svg ?? "nil")"
            )
        }
    }

    /// pace nil (no resetsAt) falls back to the absolute-only ramp, same as
    /// SessionListView.barColor's nil-pace branch.
    func testBuildSVGColorThresholdsFallBackToAbsoluteRampWhenUnpaceable() {
        let cases: [(name: String, used: Double, wantHex: String)] = [
            ("nil pace, low usage",    30, "34C759"),
            ("nil pace, 50% — yellow", 50, "FFCC00"),
            ("nil pace, 70% — orange", 70, "FF9500"),
        ]
        for c in cases {
            let info = RateLimitInfo(
                windows: [RateLimitWindowInfo(usedPercent: c.used, windowMinutes: 300, resetsAt: Date(timeIntervalSince1970: 0))],
                sampledAt: Date()
            )
            let result = QuotaMenuBarRenderer.buildSVG(for: info)
            XCTAssertTrue(
                result?.svg.contains("#\(c.wantHex)") ?? false,
                "\(c.name): expected #\(c.wantHex) in \(result?.svg ?? "nil")"
            )
        }
    }

    // MARK: - buildCircleSVG

    func testBuildCircleSVGReturnsNilWhenNoWindows() {
        let info = RateLimitInfo(windows: [], sampledAt: Date())
        XCTAssertNil(QuotaMenuBarRenderer.buildCircleSVG(for: info))
    }

    /// Regression pin: the circle must stay pinned to the 5h window even
    /// when the 7d window is more depleted — RateLimitInfo.imminentWindow
    /// would pick 7d here (80 > 20), which is deliberately *not* what the
    /// circle shows. A glance-value shouldn't silently swap which window
    /// it's reading. Colors chosen so a wrong-window regression is
    /// unambiguous: the 5h reading (20% used, 80% pace — well behind pace)
    /// is green, while the 7d reading (80% used, ~57% pace — ahead of pace)
    /// would be orange.
    func testBuildCircleSVGPrefersFiveHourOverSevenDayEvenWhenLessDepleted() {
        let info = makeInfo(fiveHour: 20, sevenDay: 80, fiveHourResetsIn: 3600)
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains("#34C759"), "should use the 5h window's green (behind pace)")
        XCTAssertFalse(result!.svg.contains("#FF9500"), "must not leak the 7d window's orange (ahead of pace)")
    }

    func testBuildCircleSVGFallsBackToSevenDayWhenFiveHourAbsent() {
        // Fresh 7d window (pace ≈ 0) so 60% used is unambiguously ahead of
        // pace → orange, isolating "did it use the 7d window at all" from
        // the color-ramp tests above.
        let window = windowWithPace(usedPercent: 60, pace: 0, windowMinutes: 10080)
        let info = RateLimitInfo(windows: [window], sampledAt: Date())
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains("#FF9500"))
    }

    // MARK: - pace marker (mirrors SessionListView.quotaPacePercent)

    func testPaceMarkerRenderedOnBarsWhenWindowHasFutureReset() {
        let info = makeInfo(fiveHour: 20, sevenDay: nil, fiveHourResetsIn: 3600)
        let result = QuotaMenuBarRenderer.buildSVG(for: info)
        XCTAssertTrue(result!.svg.contains("fill=\"red\""))
    }

    func testPaceMarkerAbsentOnBarsWhenResetIsUnset() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 20, windowMinutes: 300, resetsAt: Date(timeIntervalSince1970: 0))],
            sampledAt: Date()
        )
        let result = QuotaMenuBarRenderer.buildSVG(for: info)
        XCTAssertFalse(result!.svg.contains("fill=\"red\""))
    }

    func testPaceMarkerStillRendersClampedWhenWindowAlreadyExpired() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 20, windowMinutes: 300, resetsAt: Date().addingTimeInterval(-60))],
            sampledAt: Date()
        )
        let result = QuotaMenuBarRenderer.buildSVG(for: info)
        XCTAssertTrue(result!.svg.contains("fill=\"red\""))
    }

    func testPaceMarkerRenderedOnCircleAsRedStroke() {
        let info = makeInfo(fiveHour: 20, sevenDay: nil, fiveHourResetsIn: 3600)
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info)
        XCTAssertTrue(result!.svg.contains("stroke=\"red\""))
    }

    // MARK: - selectedSnapshot

    func testSelectedSnapshotPicksFreshestAcrossSessions() {
        let older = makeSession(id: "1", adapter: "claude-code", usedPercent: 10, sampledSecondsAgo: 120)
        let newer = makeSession(id: "2", adapter: "claude-code", usedPercent: 90, sampledSecondsAgo: 5)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [older, newer], providerKey: nil)
        XCTAssertEqual(got?.windows.first?.usedPercent, 90)
    }

    func testSelectedSnapshotFiltersByProviderKey() {
        let claude = makeSession(id: "1", adapter: "claude-code", usedPercent: 10, sampledSecondsAgo: 5)
        let codex = makeSession(id: "2", adapter: "codex", usedPercent: 90, sampledSecondsAgo: 5)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [claude, codex], providerKey: "anthropic")
        XCTAssertEqual(got?.windows.first?.usedPercent, 10)
    }

    /// The popover (SessionListView.mergeIntoBuckets) keeps a stale
    /// snapshot and dims the chip rather than dropping it — the icon has
    /// no room to dim, but dropping entirely made an active session look
    /// idle (it collapsed the whole quota display, which for `.usage`
    /// style meant falling back to the "no sessions" icon). Keeping it
    /// matches the popover's intent: show the last-known reading until the
    /// next statusline tick refreshes it.
    func testSelectedSnapshotKeepsStaleSnapshotsRatherThanDroppingThem() {
        let staleRateLimit = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 10, windowMinutes: 300, resetsAt: Date().addingTimeInterval(-3600))],
            sampledAt: Date().addingTimeInterval(-5)
        )
        let stale = sessionState(id: "1", adapter: "claude-code", rateLimit: staleRateLimit)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [stale], providerKey: nil)
        XCTAssertEqual(got?.windows.first?.usedPercent, 10)
    }

    /// A snapshot with no windows (the credits/usage-only path) can never
    /// render — it must not win the freshest-wins race over an older
    /// snapshot that actually has data.
    func testSelectedSnapshotSkipsSnapshotWithEmptyWindows() {
        let unrenderable = sessionState(
            id: "unrenderable", adapter: "claude-code",
            rateLimit: RateLimitInfo(windows: [], sampledAt: Date()) // freshest, but empty
        )
        let renderable = makeSession(id: "2", adapter: "claude-code", usedPercent: 42, sampledSecondsAgo: 120)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [unrenderable, renderable], providerKey: nil)
        XCTAssertEqual(got?.windows.first?.usedPercent, 42)
    }

    func testSelectedSnapshotReturnsNilWhenNoSessionCarriesRateLimit() {
        let plain = SessionState(
            id: "sess_1", state: .working, model: "claude-sonnet", cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: Date(), updatedAt: Date()
        )
        XCTAssertNil(QuotaMenuBarRenderer.selectedSnapshot(sessions: [plain], providerKey: nil))
    }

    // MARK: - helpers

    private func makeInfo(fiveHour: Double?, sevenDay: Double?, fiveHourResetsIn: TimeInterval = 3600) -> RateLimitInfo {
        var windows: [RateLimitWindowInfo] = []
        if let fiveHour {
            windows.append(RateLimitWindowInfo(usedPercent: fiveHour, windowMinutes: 300, resetsAt: Date().addingTimeInterval(fiveHourResetsIn)))
        }
        if let sevenDay {
            windows.append(RateLimitWindowInfo(usedPercent: sevenDay, windowMinutes: 10080, resetsAt: Date().addingTimeInterval(3 * 86400)))
        }
        return RateLimitInfo(windows: windows, sampledAt: Date())
    }

    /// Builds a window whose `resetsAt` implies exactly `pace` percent
    /// elapsed, so color-ramp tests can target specific (used, pace) pairs
    /// instead of whatever a fixed resetsAt happens to imply.
    private func windowWithPace(usedPercent: Double, pace: Double, windowMinutes: Int = 300) -> RateLimitWindowInfo {
        let windowSeconds = Double(windowMinutes) * 60
        let elapsed = (pace / 100) * windowSeconds
        let resetsAt = Date().addingTimeInterval(windowSeconds - elapsed)
        return RateLimitWindowInfo(usedPercent: usedPercent, windowMinutes: windowMinutes, resetsAt: resetsAt)
    }

    /// Common case: a session with a fresh (future-resetting) rate-limit
    /// window. Tests that need an already-expired window build a
    /// RateLimitInfo directly and go through `sessionState` instead, so
    /// this stays at 4 arguments rather than growing a resetsInPast flag
    /// nobody but one test needed (CodeScene: excess function arguments).
    private func makeSession(
        id: String,
        adapter: String,
        usedPercent: Double,
        sampledSecondsAgo: TimeInterval
    ) -> SessionState {
        let rateLimit = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: usedPercent, windowMinutes: 300, resetsAt: Date().addingTimeInterval(3600))],
            sampledAt: Date().addingTimeInterval(-sampledSecondsAgo)
        )
        return sessionState(id: id, adapter: adapter, rateLimit: rateLimit)
    }

    private func sessionState(id: String, adapter: String, rateLimit: RateLimitInfo) -> SessionState {
        let metrics = SessionMetrics(
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
        )
        return SessionState(
            id: "sess_\(id)",
            state: .working,
            model: "claude-sonnet",
            cwd: "/tmp",  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            firstSeen: Date(),
            updatedAt: Date(),
            metrics: metrics,
            adapter: adapter
        )
    }
}
