import XCTest
@testable import Irrlicht

final class ContextPressureThresholdTests: XCTestCase {

    private func metrics(utilization: Double, tokens: Int64) -> SessionMetrics {
        SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: tokens,
            modelName: "test",
            contextWindow: nil,
            contextUtilization: utilization,
            pressureLevel: "unknown",
            contextWindowUnknown: nil,
            estimatedCostUSD: nil,
            lastAssistantText: nil,
            tasks: nil
        )
    }

    // MARK: - Percent mode

    func testPercentFiresAtAndAboveThreshold() {
        let threshold = ContextPressureThreshold(value: 80, unit: .percent)
        XCTAssertTrue(threshold.isExceeded(by: metrics(utilization: 80, tokens: 1000)))   // == fires
        XCTAssertTrue(threshold.isExceeded(by: metrics(utilization: 92.5, tokens: 1000))) // above fires
    }

    func testPercentBelowThresholdDoesNotFire() {
        let threshold = ContextPressureThreshold(value: 80, unit: .percent)
        XCTAssertFalse(threshold.isExceeded(by: metrics(utilization: 79.9, tokens: 1000)))
    }

    func testPercentZeroUtilizationNeverFires() {
        // An unknown context window leaves utilization at 0 — percent mode must
        // not fire even when the raw token count is large.
        let threshold = ContextPressureThreshold(value: 80, unit: .percent)
        XCTAssertFalse(threshold.isExceeded(by: metrics(utilization: 0, tokens: 500_000)))
    }

    // MARK: - Tokens mode

    func testTokensFiresAtAndAboveThreshold() {
        let threshold = ContextPressureThreshold(value: 150_000, unit: .tokens)
        // == fires, and works even when the window (percentage) is unknown.
        XCTAssertTrue(threshold.isExceeded(by: metrics(utilization: 0, tokens: 150_000)))
        XCTAssertTrue(threshold.isExceeded(by: metrics(utilization: 10, tokens: 200_000)))
    }

    func testTokensBelowThresholdDoesNotFire() {
        let threshold = ContextPressureThreshold(value: 150_000, unit: .tokens)
        XCTAssertFalse(threshold.isExceeded(by: metrics(utilization: 99, tokens: 149_999)))
    }

    func testTokensZeroNeverFires() {
        let threshold = ContextPressureThreshold(value: 150_000, unit: .tokens)
        XCTAssertFalse(threshold.isExceeded(by: metrics(utilization: 0, tokens: 0)))
    }

    // MARK: - Defaults

    func testDefaultsPerUnit() {
        XCTAssertEqual(ContextPressureThreshold.defaultValue(for: .percent), 80)
        XCTAssertEqual(ContextPressureThreshold.defaultValue(for: .tokens), 150_000)
        XCTAssertEqual(ContextPressureThreshold.defaultValue, 80)
        XCTAssertEqual(ContextPressureThreshold.defaultUnit, .percent)
    }
}
