import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

// `onExportCSV: {}, onExportJSON: {}` below are intentional no-ops — these are
// pure visual-rendering snapshots, not interaction tests, so export wiring is
// irrelevant (SonarQube swift:S1186 flags each occurrence individually).
@MainActor
final class HistoryViewSnapshotTests: XCTestCase {
    private var originalTimeZone: TimeZone!

    override func setUp() async throws {
        try await super.setUp()
        // Pin the timezone so the chart's x-axis labels (HH:mm / M/d) render
        // identically regardless of the machine running the test.
        originalTimeZone = NSTimeZone.default
        NSTimeZone.default = TimeZone(identifier: "UTC")!
    }

    override func tearDown() async throws {
        NSTimeZone.default = originalTimeZone
        try await super.tearDown()
    }

    private func host(_ view: some View, height: CGFloat) -> NSView {
        let width = SessionListView.panelWidth
        let wrapped = view
            .frame(width: width, height: height)
            .background(Color(NSColor.windowBackgroundColor))
        let hosting = NSHostingView(rootView: wrapped)
        // Pin to dark aqua so snapshots don't depend on the current system
        // appearance (matches SessionRowSnapshotTests).
        hosting.appearance = NSAppearance(named: .darkAqua)
        hosting.frame = CGRect(x: 0, y: 0, width: width, height: height)
        hosting.layoutSubtreeIfNeeded()
        return hosting
    }

    /// Four daily buckets (bucketSeconds = 86400 → M/d axis labels), three
    /// projects — exercises the stacked-area chart, the summary total, and
    /// the contributor list.
    private func populated() -> HistoryResponse {
        let day: Int64 = 86_400
        let base: Int64 = 1_700_000_000
        let buckets = (0..<8).map { base + Int64($0) * day }
        // Three projects with a value in every bucket so the stacked areas read
        // as continuous bands (a classic stacked-area chart).
        let perProject: [(String, [Double])] = [
            ("irrlicht", [0.80, 1.00, 1.20, 1.10, 1.50, 1.80, 2.00, 2.40]),
            ("dashboard", [0.30, 0.40, 0.50, 0.60, 0.50, 0.70, 0.90, 1.00]),
            ("scratch", [0.10, 0.15, 0.20, 0.10, 0.25, 0.30, 0.20, 0.35]),
        ]
        var series: [HistoryPoint] = []
        for (project, values) in perProject {
            for (i, v) in values.enumerated() {
                series.append(HistoryPoint(ts: buckets[i], project: project, value: v))
            }
        }
        let totals = perProject.map { ($0.0, $0.1.reduce(0, +)) }
        let grand = totals.reduce(0.0) { $0 + $1.1 }
        return HistoryResponse(
            range: "month",
            chart: "cost",
            group: "project",
            start: base,
            end: base + Int64(buckets.count) * day,
            bucketSeconds: day,
            bucketStarts: buckets,
            total: grand,
            series: series,
            topContributors: totals.map { HistoryContributor(label: $0.0, value: $0.1) },
            tokenSplit: nil,
            scope: nil
        )
    }

    private func empty() -> HistoryResponse {
        HistoryResponse(
            range: "day",
            chart: "cost",
            group: "project",
            start: 1_700_000_000,
            end: 1_700_086_400,
            bucketSeconds: 3_600,
            bucketStarts: [],
            total: 0,
            series: [],
            topContributors: [],
            tokenSplit: nil,
            scope: nil
        )
    }

    /// Tokens chart (#750): the side panel is the input/output/cache split.
    private func populatedTokens() -> HistoryResponse {
        let day: Int64 = 86_400
        let base: Int64 = 1_700_000_000
        let buckets = (0..<8).map { base + Int64($0) * day }
        let perKey: [(String, [Double])] = [
            ("main", [12000, 14000, 16000, 15000, 20000, 24000, 26000, 30000]),
            ("feat/x", [4000, 5000, 6000, 7000, 6000, 9000, 11000, 12000]),
        ]
        var series: [HistoryPoint] = []
        for (key, values) in perKey {
            for (i, v) in values.enumerated() {
                series.append(HistoryPoint(ts: buckets[i], project: key, value: v))
            }
        }
        let grand = perKey.reduce(0.0) { $0 + $1.1.reduce(0, +) }
        return HistoryResponse(
            range: "month", chart: "tokens", group: "branch",
            start: base, end: base + Int64(buckets.count) * day,
            bucketSeconds: day, bucketStarts: buckets, total: grand,
            series: series,
            topContributors: perKey.map { HistoryContributor(label: $0.0, value: $0.1.reduce(0, +)) },
            tokenSplit: HistoryTokenSplit(input: grand * 0.6, output: grand * 0.1, cache: grand * 0.3),
            scope: nil
        )
    }

    func testHistoryPopulated() {
        let view = HistoryContentView(
            data: populated(),
            range: .month,
            onExportCSV: { /* unused in this snapshot */ },
            onExportJSON: { /* unused in this snapshot */ }
        )
        assertSnapshot(of: host(view, height: 460), as: .image)
    }

    func testHistoryTokens() {
        let view = HistoryContentView(
            data: populatedTokens(),
            range: .month,
            chart: .tokens,
            group: .branch,
            scope: nil,
            onExportCSV: { /* unused in this snapshot */ },
            onExportJSON: { /* unused in this snapshot */ }
        )
        assertSnapshot(of: host(view, height: 460), as: .image)
    }

    /// #1029: the CO2 chart is the only one with a methodology-link overlay
    /// (top-trailing on the chart) — this pins its presence so a future
    /// regression (e.g. accidentally scoping it to another chart, or losing
    /// it entirely) shows up as a snapshot diff.
    private func populatedCO2() -> HistoryResponse {
        let day: Int64 = 86_400
        let base: Int64 = 1_700_000_000
        let buckets = (0..<8).map { base + Int64($0) * day }
        let perKey: [(String, [Double])] = [
            ("main", [120, 140, 160, 150, 200, 240, 260, 300]),
            ("feat/x", [40, 50, 60, 70, 60, 90, 110, 120]),
        ]
        var series: [HistoryPoint] = []
        for (key, values) in perKey {
            for (i, v) in values.enumerated() {
                series.append(HistoryPoint(ts: buckets[i], project: key, value: v))
            }
        }
        let grand = perKey.reduce(0.0) { $0 + $1.1.reduce(0, +) }
        return HistoryResponse(
            range: "month", chart: "co2", group: "branch",
            start: base, end: base + Int64(buckets.count) * day,
            bucketSeconds: day, bucketStarts: buckets, total: grand,
            series: series,
            topContributors: perKey.map { HistoryContributor(label: $0.0, value: $0.1.reduce(0, +)) },
            tokenSplit: nil,
            scope: nil
        )
    }

    func testHistoryCO2() {
        let view = HistoryContentView(
            data: populatedCO2(),
            range: .month,
            chart: .co2,
            group: .branch,
            scope: nil,
            onExportCSV: { /* unused in this snapshot */ },
            onExportJSON: { /* unused in this snapshot */ }
        )
        assertSnapshot(of: host(view, height: 460), as: .image)
    }

    func testHistoryDrilldown() {
        let view = HistoryContentView(
            data: populated(),
            range: .month,
            chart: .cost,
            group: .branch,
            scope: HistoryScope(field: .project, value: "irrlicht"),
            onExportCSV: { /* unused in this snapshot */ },
            onExportJSON: { /* unused in this snapshot */ }
        )
        assertSnapshot(of: host(view, height: 500), as: .image)
    }

    func testHistoryEmptyState() {
        let view = HistoryContentView(
            data: empty(),
            range: .day,
            onExportCSV: { /* unused in this snapshot */ },
            onExportJSON: { /* unused in this snapshot */ }
        )
        assertSnapshot(of: host(view, height: 320), as: .image)
    }

    // MARK: Yield (#373)

    /// Three projects: two with a productive/reverted split, one unknown-only
    /// (non-git, no attributable spend). Exercises the headline ratio, the
    /// unattributed line, the ↩ counts, and the split bars.
    private func yieldFixture() -> HistoryYieldResponse {
        HistoryYieldResponse(
            range: "month",
            productiveCost: 12.50,
            revertedCost: 3.50,
            unknownCost: 1.25,
            totalCost: 16.00,
            yieldRatio: 12.50 / 16.00,
            projects: [
                HistoryYieldProject(project: "irrlicht", productiveCost: 8.0, revertedCost: 2.0, unknownCost: 0, totalCost: 10.0, yieldRatio: 0.80, revertedCount: 2),
                HistoryYieldProject(project: "dashboard", productiveCost: 4.5, revertedCost: 1.5, unknownCost: 0, totalCost: 6.0, yieldRatio: 0.75, revertedCount: 1),
                HistoryYieldProject(project: "scratch", productiveCost: 0, revertedCost: 0, unknownCost: 1.25, totalCost: 0, yieldRatio: 0, revertedCount: 0),
            ]
        )
    }

    func testYieldPopulated() {
        let view = HistoryYieldContentView(data: yieldFixture(), range: .month)
        assertSnapshot(of: host(view, height: 360), as: .image)
    }

    // MARK: DORA (#951)

    private func doraFixture() -> HistoryDoraResponse {
        HistoryDoraResponse(
            range: "month",
            project: "irrlicht",
            start: 1_700_000_000,
            end: 1_702_678_400,
            available: true,
            message: nil,
            deploymentFrequency: DoraMetric(value: 2.55, unit: "per_week", sampleSize: 35, available: true, message: nil),
            leadTime: DoraMetric(value: 22.8, unit: "hours", sampleSize: 935, available: true, message: nil),
            changeFailureRate: DoraMetric(value: 42.9, unit: "percent", sampleSize: 35, available: true, message: "15 of 35 releases flagged"),
            mttr: DoraMetric(value: 8.5, unit: "hours", sampleSize: 15, available: true, message: nil)
        )
    }

    private func doraUnavailableFixture() -> HistoryDoraResponse {
        HistoryDoraResponse(
            range: "month",
            project: "scratch",
            start: 1_700_000_000,
            end: 1_702_678_400,
            available: false,
            message: "no releases found for this project",
            deploymentFrequency: DoraMetric(value: 0, unit: "per_week", sampleSize: 0, available: false, message: nil),
            leadTime: DoraMetric(value: 0, unit: "hours", sampleSize: 0, available: false, message: nil),
            changeFailureRate: DoraMetric(value: 0, unit: "percent", sampleSize: 0, available: false, message: nil),
            mttr: DoraMetric(value: 0, unit: "hours", sampleSize: 0, available: false, message: nil)
        )
    }

    func testDoraPopulated() {
        let view = HistoryDoraContentView(data: doraFixture())
        assertSnapshot(of: host(view, height: 260), as: .image)
    }

    func testDoraUnavailable() {
        let view = HistoryDoraContentView(data: doraUnavailableFixture())
        assertSnapshot(of: host(view, height: 200), as: .image)
    }

    // MARK: Quota projection

    /// 5h window, 60% used 2h in → over pace, projected to hit the cap before
    /// reset (orange trajectory + projected-cap label).
    private func fiveHourHitsCap() -> QuotaWindowVM {
        let base = Date(timeIntervalSince1970: 1_700_000_000)
        return QuotaWindowVM(
            label: "5h",
            planLabel: "Claude Max",
            start: base,
            end: base.addingTimeInterval(5 * 3600),
            now: base.addingTimeInterval(2 * 3600),
            usedPercent: 60,
            projectedCap: base.addingTimeInterval(2 * 3600 * 100.0 / 60.0),
            isStale: false
        )
    }

    /// 7d window, 15% used 2 days in → under pace, won't hit the cap this window
    /// (green trajectory, no projected cap).
    private func sevenDayUnderPace() -> QuotaWindowVM {
        let base = Date(timeIntervalSince1970: 1_700_000_000)
        return QuotaWindowVM(
            label: "7d",
            planLabel: "Claude Max",
            start: base,
            end: base.addingTimeInterval(7 * 86_400),
            now: base.addingTimeInterval(2 * 86_400),
            usedPercent: 15,
            projectedCap: nil,
            isStale: false
        )
    }

    /// 5h OpenAI window, 30% used 1h in → under pace (green, no cap).
    private func openaiFiveHourUnderPace() -> QuotaWindowVM {
        let base = Date(timeIntervalSince1970: 1_700_000_000)
        return QuotaWindowVM(
            label: "5h",
            planLabel: "ChatGPT Plus",
            start: base,
            end: base.addingTimeInterval(5 * 3600),
            now: base.addingTimeInterval(1 * 3600),
            usedPercent: 30,
            projectedCap: nil,
            isStale: false
        )
    }

    private func anthropicProvider() -> QuotaProviderVM {
        QuotaProviderVM(id: "anthropic", iconKey: "anthropic", planLabel: "Claude Max",
                        windows: [fiveHourHitsCap(), sevenDayUnderPace()])
    }

    private func openaiProvider() -> QuotaProviderVM {
        QuotaProviderVM(id: "openai", iconKey: "openai", planLabel: "ChatGPT Plus",
                        windows: [openaiFiveHourUnderPace()])
    }

    /// Single provider, both windows side-by-side — the common case (one Claude
    /// subscription): exercises the 5h cap trajectory + 7d on-pace footer.
    func testQuotaForecastSingleProvider() {
        let view = HistoryQuotaForecastView(providers: [anthropicProvider()])
        assertSnapshot(of: host(view, height: 320), as: .image)
    }

    /// Two active providers stacked — Anthropic (5h hits cap, 7d on pace) +
    /// OpenAI (5h on pace). Exercises the per-provider grid, both brand icons,
    /// and both footer states.
    func testQuotaForecastMultiProvider() {
        let view = HistoryQuotaForecastView(providers: [anthropicProvider(), openaiProvider()])
        assertSnapshot(of: host(view, height: 460), as: .image)
    }
}
