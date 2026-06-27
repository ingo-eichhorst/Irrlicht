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
        let rate = grand / Double(buckets.count)
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
            forecast: HistoryForecast(
                projected: grand + rate,
                basis: "linear",
                horizonBuckets: 1,
                series: [HistoryForecastPoint(ts: base + Int64(buckets.count) * day, value: rate)]
            )
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
