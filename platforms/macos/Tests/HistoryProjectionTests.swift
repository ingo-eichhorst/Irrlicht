import XCTest
@testable import Irrlicht

/// Unit coverage for the quota burn-rate projection math (the average-pace cap
/// and end-percent), separate from the snapshot tests of the rendered chart.
final class HistoryProjectionTests: XCTestCase {
    private let start = Date(timeIntervalSince1970: 1_700_000_000)
    private let windowSeconds: Double = 5 * 3600

    private func cap(usedPercent: Double, elapsedFraction: Double) -> Date? {
        let end = start.addingTimeInterval(windowSeconds)
        let now = start.addingTimeInterval(windowSeconds * elapsedFraction)
        return QuotaWindowVM.averagePaceCap(now: now, start: start, resetsAt: end, usedPercent: usedPercent)
    }

    func testOverPace_HitsCapBeforeReset() {
        // 60% used 40% into the window → mean rate reaches 100% at ~3h20m, before reset.
        let c = cap(usedPercent: 60, elapsedFraction: 0.4)
        let expected = start.addingTimeInterval(0.4 * windowSeconds * 100.0 / 60.0)
        XCTAssertNotNil(c)
        XCTAssertEqual(c!.timeIntervalSince1970, expected.timeIntervalSince1970, accuracy: 1)
    }

    func testUnderPace_ReturnsNil() {
        // 15% used 40% in → projects past reset → won't hit the cap this window.
        XCTAssertNil(cap(usedPercent: 15, elapsedFraction: 0.4))
    }

    func testFlatUsage_ReturnsNil() {
        XCTAssertNil(cap(usedPercent: 0, elapsedFraction: 0.5))
    }

    func testAtOrOverCap_ReturnsNil() {
        XCTAssertNil(cap(usedPercent: 100, elapsedFraction: 0.5))
        XCTAssertNil(cap(usedPercent: 120, elapsedFraction: 0.5))
    }

    func testProjectedEndPercent_ClampsTo100() {
        let end = start.addingTimeInterval(windowSeconds)
        let now = start.addingTimeInterval(2 * 3600)
        let vm = QuotaWindowVM(
            label: "5h", planLabel: nil, start: start, end: end, now: now,
            usedPercent: 80, projectedCap: nil, isStale: false
        )
        // 80% at 40% through → naive end 200% → clamps to 100.
        XCTAssertEqual(vm.projectedEndPercent, 100, accuracy: 0.001)
    }
}
