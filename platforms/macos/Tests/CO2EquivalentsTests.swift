import XCTest
@testable import Irrlicht

// Pure-logic coverage for the History CO2 chart's reference-line picker
// (issue #952) — mirrors platforms/web/irrlicht.test.js's "CO2 equivalents"
// suite structurally. No snapshots — deterministic and host-free.
final class CO2EquivalentsTests: XCTestCase {
    func testEquivalentsAscendingWithNoDuplicateIdsAndPositiveGrams() {
        let all = CO2Equivalents.all
        for i in 1..<all.count {
            XCTAssertGreaterThan(all[i].grams, all[i - 1].grams)
        }
        XCTAssertEqual(Set(all.map(\.id)).count, all.count)
        XCTAssertTrue(all.allSatisfy { $0.grams > 0 })
    }

    func testTheListIsDenseFrom100gToRoughly100Tonnes() {
        XCTAssertGreaterThanOrEqual(CO2Equivalents.all.count, 17)
        XCTAssertGreaterThanOrEqual(CO2Equivalents.all.last?.grams ?? 0, 100_000_000)
    }

    func testPickReturnsNothingForZeroOrNegativeAxis() {
        XCTAssertEqual(CO2Equivalents.pick(maxY: 0), [])
        XCTAssertEqual(CO2Equivalents.pick(maxY: -5), [])
    }

    func testTinyAxisBelowSmallestEquivalentDrawsNoLines() {
        XCTAssertEqual(CO2Equivalents.pick(maxY: 0.05), [])
    }

    func testEveryPickSitsUnderAxisCeilingAndNoneRepeat() {
        let picks = CO2Equivalents.pick(maxY: 2_000_000)
        XCTAssertGreaterThan(picks.count, 0)
        for eq in picks { XCTAssertLessThan(eq.grams, 2_000_000 * 0.98) }
        XCTAssertEqual(Set(picks.map(\.id)).count, picks.count)
    }

    func testPicksAreSortedAscendingByGrams() {
        let picks = CO2Equivalents.pick(maxY: 1_000_000)
        for i in 1..<picks.count { XCTAssertGreaterThan(picks[i].grams, picks[i - 1].grams) }
    }

    func testSmallAxisSurfacesOnlySmallScaleEquivalents() {
        let picks = CO2Equivalents.pick(maxY: 100)
        XCTAssertTrue(picks.allSatisfy { $0.grams < 100 })
        XCTAssertFalse(picks.contains { $0.id.hasPrefix("flight") })
    }

    func testLargeAxisIsCappedAtThreeReferenceLines() {
        XCTAssertLessThanOrEqual(CO2Equivalents.pick(maxY: 5_000_000).count, 3)
    }

    // Matches the real-world example reported against the web implementation:
    // a 50kg axis should pick roughly 1.5kg/8.9kg/43.8kg, not values clustered
    // together or a mismatched jump to a much larger candidate.
    func test50kgAxisPicksMatchTheReportedRealWorldExample() {
        let picks = CO2Equivalents.pick(maxY: 50_000)
        XCTAssertEqual(picks.map(\.id), ["laundry", "gasoline-gallon", "flight-short"])
        XCTAssertEqual(picks.map(\.grams), [1_500, 8_900, 43_800])
    }

    // Lockstep guard: if `co2EquivalentTargets` is re-tuned in
    // platforms/web/historyTab.js, this fails until CO2Equivalents.targets is
    // updated to match — intentionally brittle so the two never silently
    // drift apart.
    func testTargetsMatchTheWebFractions() {
        XCTAssertEqual(CO2Equivalents.targets(candidateCount: 3), [0.04, 0.2, 0.8])
        XCTAssertEqual(CO2Equivalents.targets(candidateCount: 2), [0.1, 0.7])
        XCTAssertEqual(CO2Equivalents.targets(candidateCount: 1), [0.4])
        XCTAssertEqual(CO2Equivalents.targets(candidateCount: 0), [0.4])
    }
}
