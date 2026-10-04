import XCTest
@testable import Irrlicht

/// #737 Phase 3 — the macOS half of the low execution-confidence chip.
///
/// The daemon owns the threshold and the tooltip (`core/domain/session/
/// metrics.go`, `ExecutionConfidence*`); this client decodes three fields and
/// renders what it is told. These tests pin both halves: the decode (the
/// keys are the daemon's spelling, and absence is tolerated) and the
/// presentation rule `ExecutionConfidenceChip.from`, which mirrors
/// `executionConfidenceChip` in platforms/web/formatters.js.
final class ExecutionConfidenceTests: XCTestCase {

    private func decodeMetrics(_ fields: String) throws -> SessionMetrics {
        let sep = fields.isEmpty ? "" : ","
        let json = #"{"elapsed_seconds":1,"total_tokens":10"# + sep + fields + "}"
        return try JSONDecoder().decode(SessionMetrics.self, from: Data(json.utf8))
    }

    // MARK: - Decode

    func testDecodesAllThreeFields() throws {
        let m = try decodeMetrics(
            #""execution_confidence":42,"execution_confidence_low":true,"execution_confidence_tooltip":"Execution confidence 42/100""#)
        XCTAssertEqual(m.executionConfidence, 42)
        XCTAssertEqual(m.executionConfidenceLow, true)
        XCTAssertEqual(m.executionConfidenceTooltip, "Execution confidence 42/100")
    }

    func testAbsentFieldsDecodeToNilWithoutFailingTheMetrics() throws {
        let m = try decodeMetrics("")
        XCTAssertNil(m.executionConfidence)
        XCTAssertNil(m.executionConfidenceLow)
        XCTAssertNil(m.executionConfidenceTooltip)
        XCTAssertEqual(m.totalTokens, 10, "the rest of the metrics must still decode")
    }

    /// A real 0 is a score, not "missing" — the daemon sends it through a
    /// pointer for exactly this reason.
    func testScoreZeroDecodesAsZeroNotNil() throws {
        let m = try decodeMetrics(#""execution_confidence":0,"execution_confidence_low":true"#)
        XCTAssertEqual(m.executionConfidence, 0)
        XCTAssertEqual(m.executionConfidenceLow, true)
    }

    func testRoundTripsThroughEncode() throws {
        let m = try decodeMetrics(
            #""execution_confidence":7,"execution_confidence_low":true,"execution_confidence_tooltip":"t""#)
        let again = try JSONDecoder().decode(SessionMetrics.self, from: JSONEncoder().encode(m))
        XCTAssertEqual(again.executionConfidence, 7)
        XCTAssertEqual(again.executionConfidenceLow, true)
        XCTAssertEqual(again.executionConfidenceTooltip, "t")
    }

    /// The CodingKeys above are only right if they are the daemon's keys. Read
    /// the Go struct tags themselves rather than trusting a copy, so a rename
    /// on either side fails here instead of decoding nothing forever.
    func testCodingKeysAreTheDaemonsJSONTags() throws {
        let metricsGo = URL(fileURLWithPath: #filePath)  // …/platforms/macos/Tests/<this file>
            .deletingLastPathComponent()                 // …/platforms/macos/Tests
            .deletingLastPathComponent()                 // …/platforms/macos
            .deletingLastPathComponent()                 // …/platforms
            .deletingLastPathComponent()                 // repo root
            .appendingPathComponent("core/domain/session/metrics.go")
        let source = try String(contentsOf: metricsGo, encoding: .utf8)
        let keys: [SessionMetrics.CodingKeys] = [
            .executionConfidence, .executionConfidenceLow, .executionConfidenceTooltip,
        ]
        for key in keys {
            XCTAssertTrue(source.contains(#"json:"\#(key.rawValue),omitempty""#),
                          "metrics.go has no json tag \(key.rawValue) — the daemon renamed it")
        }
    }

    // MARK: - Presentation

    private func metrics(score: Int?, low: Bool?, tooltip: String? = nil) -> SessionMetrics {
        SessionMetrics(
            elapsedSeconds: 0, totalTokens: 0, modelName: "m", contextWindow: nil,
            contextUtilization: 0, pressureLevel: "unknown", contextWindowUnknown: nil,
            estimatedCostUSD: nil, lastAssistantText: nil, tasks: nil,
            executionConfidence: score, executionConfidenceLow: low,
            executionConfidenceTooltip: tooltip)
    }

    func testHiddenWithoutMetrics() {
        XCTAssertNil(ExecutionConfidenceChip.from(nil))
    }

    func testHiddenWhenTheLowFlagIsAbsent() {
        // Even with a score that would be "low" by any threshold: the client
        // never applies one.
        XCTAssertNil(ExecutionConfidenceChip.from(metrics(score: 10, low: nil, tooltip: "t")))
    }

    func testHiddenWhenTheLowFlagIsExplicitlyFalse() {
        XCTAssertNil(ExecutionConfidenceChip.from(metrics(score: 10, low: false, tooltip: "t")))
    }

    func testShownWhenLow() {
        let chip = ExecutionConfidenceChip.from(metrics(score: 31, low: true, tooltip: "x"))
        XCTAssertEqual(chip?.text, "? 31")
    }

    /// The gate is the daemon's flag, never the score's truthiness: 0 is the
    /// least confident a session can be and must show.
    func testShownWhenLowWithScoreZero() {
        let chip = ExecutionConfidenceChip.from(metrics(score: 0, low: true, tooltip: "x"))
        XCTAssertEqual(chip?.text, "? 0")
    }

    func testShownWithDashWhenLowButScoreMissing() {
        let chip = ExecutionConfidenceChip.from(metrics(score: nil, low: true))
        XCTAssertEqual(chip?.text, "? \u{2014}")
        XCTAssertEqual(chip?.tooltip, "", "absent tooltip renders as no tooltip, not a placeholder")
    }

    func testTooltipIsTheDaemonStringVerbatim() {
        // The string executionConfidenceTooltip (core/domain/session/
        // execution_confidence.go) composes for a score of 23.
        let daemon = "Execution confidence 23/100 — scored from hedging and uncertainty language in the agent's recent messages, latest weighted most (100 = decisive; below 50 is low)."
        let chip = ExecutionConfidenceChip.from(metrics(score: 23, low: true, tooltip: daemon))
        XCTAssertEqual(chip?.tooltip, daemon)
    }
}
