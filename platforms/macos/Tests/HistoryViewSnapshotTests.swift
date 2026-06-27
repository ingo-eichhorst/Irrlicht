import XCTest
import SwiftUI
import SnapshotTesting
@testable import Irrlicht

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
    /// projects, a linear forecast — exercises the stacked-area chart, the
    /// summary total, the forecast line, and the contributor list.
    private func populated() -> HistoryResponse {
        let day: Int64 = 86_400
        let base: Int64 = 1_700_000_000
        let buckets = [base, base + day, base + 2 * day, base + 3 * day]
        let series = [
            HistoryPoint(ts: buckets[0], project: "irrlicht", value: 1.20),
            HistoryPoint(ts: buckets[1], project: "irrlicht", value: 0.80),
            HistoryPoint(ts: buckets[3], project: "irrlicht", value: 2.00),
            HistoryPoint(ts: buckets[1], project: "dashboard", value: 0.50),
            HistoryPoint(ts: buckets[2], project: "dashboard", value: 1.50),
            HistoryPoint(ts: buckets[2], project: "scratch", value: 0.30),
        ]
        let forecast = HistoryForecast(
            projected: 7.88,
            basis: "linear",
            horizonBuckets: 1,
            series: [HistoryForecastPoint(ts: base + 4 * day, value: 1.575)]
        )
        return HistoryResponse(
            range: "month",
            chart: "cost",
            group: "project",
            start: base,
            end: base + 4 * day,
            bucketSeconds: day,
            bucketStarts: buckets,
            total: 6.30,
            series: series,
            topContributors: [
                HistoryContributor(label: "irrlicht", value: 4.00),
                HistoryContributor(label: "dashboard", value: 2.00),
                HistoryContributor(label: "scratch", value: 0.30),
            ],
            forecast: forecast
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
            forecast: nil
        )
    }

    func testHistory_Populated() {
        let view = HistoryContentView(
            data: populated(),
            range: .month,
            forecastEnabled: true,
            onExportCSV: {},
            onExportJSON: {}
        )
        assertSnapshot(of: host(view, height: 460), as: .image)
    }

    func testHistory_EmptyState() {
        let view = HistoryContentView(
            data: empty(),
            range: .day,
            forecastEnabled: true,
            onExportCSV: {},
            onExportJSON: {}
        )
        assertSnapshot(of: host(view, height: 320), as: .image)
    }
}
