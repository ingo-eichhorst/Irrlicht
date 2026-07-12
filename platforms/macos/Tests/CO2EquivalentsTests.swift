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

    // A 50kg axis should pick well-spread equivalents, not clustered ones.
    func test50kgAxisPicksAreWellSpread() {
        let picks = CO2Equivalents.pick(maxY: 50_000)
        XCTAssertEqual(picks.map(\.id), ["petrol-liter", "running-shoes", "flight-short"])
        XCTAssertEqual(picks.map(\.grams), [2_350, 9_500, 43_800])
    }

    // Regression test for issue #980: at this axis scale the original sparse
    // table picked grid-kwh (460g) and laundry (1500g) together, only ~9% of
    // the axis height apart — enough for their labels to overlap.
    func test10kgAxisNoLongerClustersTwoPicksTogether() {
        let picks = CO2Equivalents.pick(maxY: 11_000)
        XCTAssertEqual(picks.map(\.id), ["grid-kwh", "petrol-liter", "running-shoes"])
        for i in 1..<picks.count {
            let fractionalGap = (picks[i].grams - picks[i - 1].grams) / 11_000
            XCTAssertGreaterThan(fractionalGap, 0.15)
        }
    }

    // Sweeps maxY across ~9 orders of magnitude (log-spaced) so a future edit
    // that reintroduces a sparse region gets caught here instead of shipping
    // as another overlapping-label bug — mirrors the same sweep in
    // platforms/web/irrlicht.test.js.
    func testNoTwoAdjacentPicksClusterAcrossTheFullRange() {
        var exp = 0.0
        while exp <= 8.2 {
            let maxY = pow(10.0, exp)
            let picks = CO2Equivalents.pick(maxY: maxY)
            for i in 1..<picks.count {
                let fractionalGap = (picks[i].grams - picks[i - 1].grams) / maxY
                XCTAssertGreaterThan(fractionalGap, 0.04, "maxY=\(maxY) picks=\(picks.map(\.id))")
            }
            exp += 0.05
        }
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
