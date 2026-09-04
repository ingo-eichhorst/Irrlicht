import DesktopHelperCore
import Foundation
import XCTest
@testable import ClaudeDesktopHelper

final class ActionSafetyTests: XCTestCase {
    func testActionRequiresFalseToTruePostconditionTransition() throws {
        var actionRan = false
        var verifyRan = false
        XCTAssertThrowsError(try ActionTransition.perform(
            condition: .enabled,
            observe: { true },
            action: { actionRan = true },
            verify: { verifyRan = true }
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .postconditionFailed)
        }
        XCTAssertFalse(actionRan)
        XCTAssertFalse(verifyRan)

        var order: [String] = []
        try ActionTransition.perform(
            condition: .enabled,
            observe: {
                order.append("observe")
                return false
            },
            action: { order.append("action") },
            verify: { order.append("verify") }
        )
        XCTAssertEqual(order, ["observe", "action", "verify"])
    }

    func testKeyboardEventBoundaryChecksFrontmostAndFocusedImmediatelyBeforeEmission() throws {
        var order: [String] = []
        try KeyboardEventBoundary.emit(
            requireFrontmost: { order.append("frontmost") },
            isTargetFocused: {
                order.append("focused")
                return true
            },
            postEvent: { order.append("event") }
        )
        XCTAssertEqual(order, ["frontmost", "focused", "event"])

        var eventRan = false
        XCTAssertThrowsError(try KeyboardEventBoundary.emit(
            requireFrontmost: {},
            isTargetFocused: { false },
            postEvent: { eventRan = true }
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .actionFailed)
        }
        XCTAssertFalse(eventRan)
    }

    func testAmbiguousPostconditionFailsImmediately() {
        let selector = ControlSelector(role: "AXButton", identifier: "result")
        let postcondition = Postcondition(selector: selector, condition: .enabled)
        let matches = [
            ElementSnapshot(path: [0], role: "AXButton", identifier: "result", enabled: true),
            ElementSnapshot(path: [1], role: "AXButton", identifier: "result", enabled: false),
        ]
        XCTAssertThrowsError(try PostconditionObserver.holds(
            postcondition,
            in: matches
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .controlAmbiguous)
        }
    }

    func testResponseEncodingFailureReturnsClassifiedJSONAndNonzeroExit() throws {
        let response = HelperResponse(
            ok: true,
            command: .inspect,
            elements: [ElementSnapshot(
                path: [0],
                role: "AXButton",
                frame: Frame(x: .infinity, y: 0, width: 10, height: 10)
            )]
        )
        let encoded = ResponseEncoder.encode(ProcessResult(response: response, exitCode: 0))
        XCTAssertEqual(encoded.exitCode, HelperFailure(.actionFailed, "").exitCode)
        let decoded = try JSONDecoder().decode(HelperResponse.self, from: encoded.data)
        XCTAssertFalse(decoded.ok)
        XCTAssertEqual(decoded.error?.code, .actionFailed)
        XCTAssertEqual(
            decoded.error?.message,
            "The helper could not encode its JSON response."
        )
    }
}
