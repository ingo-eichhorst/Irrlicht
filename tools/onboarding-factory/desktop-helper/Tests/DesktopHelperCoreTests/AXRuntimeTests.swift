import ApplicationServices
import DesktopHelperCore
import XCTest
@testable import ClaudeDesktopHelper

final class AXRuntimeTests: XCTestCase {
    func testAXChildrenTreatsOnlyUnsupportedOrValidEmptyArrayAsLeaf() throws {
        XCTAssertEqual(
            try AXRuntime.decodeChildren(status: .attributeUnsupported, value: nil).count,
            0
        )
        let emptyChildren: [AXUIElement] = []
        XCTAssertEqual(
            try AXRuntime.decodeChildren(status: .success, value: emptyChildren as CFArray).count,
            0
        )
        XCTAssertThrowsError(try AXRuntime.decodeChildren(
            status: .cannotComplete,
            value: nil
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .actionFailed)
        }
        XCTAssertThrowsError(try AXRuntime.decodeChildren(
            status: .success,
            value: ["not-an-element"] as CFArray
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .actionFailed)
        }

        let injected = HelperFailure(.actionFailed, "injected AXChildren failure")
        XCTAssertThrowsError(try AXRuntime.readTree(
            application: AXUIElementCreateSystemWide(),
            limits: TraversalLimits(maxDepth: 2, maxNodes: 2),
            childrenReader: { _ in throw injected }
        )) { error in
            XCTAssertEqual(error as? HelperFailure, injected)
        }
    }

    func testAXAttributeFailuresPropagateThroughSnapshotAndValueReaders() throws {
        let element = AXUIElementCreateSystemWide()
        let injected = HelperFailure(.actionFailed, "injected AX attribute failure")
        XCTAssertNil(try AXRuntime.decodeAttribute(
            status: .attributeUnsupported,
            value: nil,
            attribute: kAXTitleAttribute
        ))
        XCTAssertNil(try AXRuntime.decodeAttribute(
            status: .noValue,
            value: nil,
            attribute: kAXTitleAttribute
        ))
        XCTAssertThrowsError(try AXRuntime.decodeAttribute(
            status: .cannotComplete,
            value: nil,
            attribute: kAXTitleAttribute
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .actionFailed)
        }

        let snapshotAttributes = [
            kAXRoleAttribute,
            kAXIdentifierAttribute,
            kAXEnabledAttribute,
            kAXFocusedAttribute,
            kAXPositionAttribute,
            kAXSizeAttribute,
        ]
        for failingAttribute in snapshotAttributes {
            XCTAssertThrowsError(try AXRuntime.snapshot(
                element,
                path: [0],
                hierarchy: [],
                attributeReader: { _, attribute in
                    if attribute == failingAttribute { throw injected }
                    return self.validAXValue(for: attribute)
                }
            )) { error in
                XCTAssertEqual(error as? HelperFailure, injected)
            }
        }

        let valueCondition = Postcondition(
            selector: ControlSelector(role: "AXTextArea", identifier: "prompt-input"),
            condition: .valueEquals,
            value: "expected"
        )
        XCTAssertThrowsError(try PostconditionObserver.holds(
            valueCondition,
            in: [ElementSnapshot(
                path: [0],
                role: "AXTextArea",
                identifier: "prompt-input"
            )],
            valueAtPath: { _ in throw injected }
        )) { error in
            XCTAssertEqual(error as? HelperFailure, injected)
        }
    }

    private func validAXValue(for attribute: String) -> CFTypeRef? {
        switch attribute {
        case kAXRoleAttribute:
            return kAXButtonRole as CFString
        case kAXEnabledAttribute, kAXFocusedAttribute:
            return kCFBooleanTrue
        case kAXPositionAttribute:
            var point = CGPoint(x: 1, y: 2)
            return AXValueCreate(.cgPoint, &point)
        case kAXSizeAttribute:
            var size = CGSize(width: 3, height: 4)
            return AXValueCreate(.cgSize, &size)
        default:
            return nil
        }
    }
}
