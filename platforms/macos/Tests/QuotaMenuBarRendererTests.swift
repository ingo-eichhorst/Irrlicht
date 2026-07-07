import XCTest
@testable import Irrlicht

/// Coverage for the menu-bar quota icon (issue #909): the stacked 5h/7d
/// bars, the single-window compact ring, the pace marker both share, and
/// the freshest-non-stale snapshot selection across live sessions.
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

    func testBuildSVGColorThresholds() {
        // Mirrors QuotaChipBarColorTests' boundary pinning, for the bare-hex
        // (no '#') ramp this renderer's SVG fill attributes use.
        let cases: [(name: String, used: Double, wantHex: String)] = [
            ("low",      30, "34C759"),
            ("medium",   60, "FF9500"),
            ("high",     85, "FF3B30"),
            ("critical", 97, "D70015"),
        ]
        for c in cases {
            let info = makeInfo(fiveHour: c.used, sevenDay: nil)
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
    /// it's reading.
    func testBuildCircleSVGPrefersFiveHourOverSevenDayEvenWhenLessDepleted() {
        let info = makeInfo(fiveHour: 20, sevenDay: 80)
        let result = QuotaMenuBarRenderer.buildCircleSVG(for: info)
        XCTAssertNotNil(result)
        XCTAssertTrue(result!.svg.contains("#34C759"), "should use the 5h window's low-usage green")
        XCTAssertFalse(result!.svg.contains("#FF3B30"), "must not leak the 7d window's high-usage color")
    }

    func testBuildCircleSVGFallsBackToSevenDayWhenFiveHourAbsent() {
        let info = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: 60, windowMinutes: 10080, resetsAt: Date().addingTimeInterval(3600))],
            sampledAt: Date()
        )
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

    func testSelectedSnapshotPicksFreshestNonStaleAcrossSessions() {
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

    func testSelectedSnapshotSkipsStaleSnapshots() {
        let stale = makeSession(id: "1", adapter: "claude-code", usedPercent: 10, sampledSecondsAgo: 5, resetsInPast: true)
        let got = QuotaMenuBarRenderer.selectedSnapshot(sessions: [stale], providerKey: nil)
        XCTAssertNil(got)
    }

    func testSelectedSnapshotReturnsNilWhenNoSessionCarriesRateLimit() {
        let plain = SessionState(
            id: "sess_1", state: .working, model: "claude-sonnet", cwd: "/tmp",
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

    private func makeSession(
        id: String,
        adapter: String,
        usedPercent: Double,
        sampledSecondsAgo: TimeInterval,
        resetsInPast: Bool = false
    ) -> SessionState {
        let resetsAt = resetsInPast ? Date().addingTimeInterval(-3600) : Date().addingTimeInterval(3600)
        let rateLimit = RateLimitInfo(
            windows: [RateLimitWindowInfo(usedPercent: usedPercent, windowMinutes: 300, resetsAt: resetsAt)],
            sampledAt: Date().addingTimeInterval(-sampledSecondsAgo)
        )
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
            cwd: "/tmp",
            firstSeen: Date(),
            updatedAt: Date(),
            metrics: metrics,
            adapter: adapter
        )
    }
}
