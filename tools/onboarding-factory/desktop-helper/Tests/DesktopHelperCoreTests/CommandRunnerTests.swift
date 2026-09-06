import ApplicationServices
import DesktopHelperCore
import XCTest
@testable import ClaudeDesktopHelper

final class CommandRunnerTests: XCTestCase {
    func testSetValueRunsCompleteInjectedActionPathInOrder() throws {
        let application = AXUIElementCreateSystemWide()
        let targetElement = AXUIElementCreateApplication(41_001)
        let target = LiveElement(
            snapshot: ElementSnapshot(
                path: [0],
                role: "AXTextArea",
                identifier: "prompt-input"
            ),
            element: targetElement
        )
        var order: [String] = []
        var currentValue = "old"
        let dependencies = makeCommandDependencies(
            application: application,
            record: { order.append($0) },
            readTree: { _, _ in
                order.append("read")
                return LiveTree(elements: [target])
            },
            setValue: { value, element in
                order.append("set")
                XCTAssertTrue(CFEqual(element, targetElement))
                XCTAssertEqual(value, "new")
                currentValue = value
            },
            valueAttribute: { element in
                order.append("value")
                XCTAssertTrue(CFEqual(element, targetElement))
                return currentValue
            }
        )
        let response = try CommandRunner.run(HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .setValue,
            selector: ControlSelector(role: "AXTextArea", identifier: "prompt-input"),
            value: "new"
        ), dependencies: dependencies)

        XCTAssertTrue(response.ok)
        XCTAssertEqual(response.action?.postcondition, .valueEquals)
        XCTAssertEqual(
            order,
            ["load", "frontmost", "expose", "read", "read", "value", "set", "read", "value"]
        )
    }

    func testKeyboardRunsCompleteInjectedActionPathInOrder() throws {
        let application = AXUIElementCreateSystemWide()
        let targetElement = AXUIElementCreateApplication(41_002)
        var order: [String] = []
        var enabled = true
        let dependencies = makeCommandDependencies(
            application: application,
            record: { order.append($0) },
            readTree: { _, _ in
                order.append("read")
                return LiveTree(elements: [LiveElement(
                    snapshot: ElementSnapshot(
                        path: [0],
                        role: "AXButton",
                        identifier: "send-button",
                        enabled: enabled
                    ),
                    element: targetElement
                )])
            },
            focus: { element in
                order.append("focus")
                XCTAssertTrue(CFEqual(element, targetElement))
            },
            isFocused: { element in
                order.append("focused")
                XCTAssertTrue(CFEqual(element, targetElement))
                return true
            },
            postKeyboardEvent: { keyCode, modifiers in
                order.append("keyboard")
                XCTAssertEqual(keyCode, 36)
                XCTAssertEqual(modifiers, ["command"])
                enabled = false
            }
        )
        let selector = ControlSelector(role: "AXButton", identifier: "send-button")
        let response = try CommandRunner.run(HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .keyboard,
            selector: selector,
            keyCode: 36,
            modifiers: ["command"],
            postcondition: Postcondition(selector: selector, condition: .disabled)
        ), dependencies: dependencies)

        XCTAssertTrue(response.ok)
        XCTAssertEqual(
            order,
            [
                "load", "frontmost", "expose", "read", "focus", "read",
                "frontmost", "focused", "keyboard", "read",
            ]
        )
    }

    func testPhysicalClickRunsCompleteInjectedActionPathWithFreshCenter() throws {
        let application = AXUIElementCreateSystemWide()
        let targetElement = AXUIElementCreateApplication(41_003)
        let selector = ControlSelector(role: "AXButton", identifier: "session-menu")
        var order: [String] = []
        var visible = true
        var clickedPoint: Point?
        let dependencies = makeCommandDependencies(
            application: application,
            record: { order.append($0) },
            readTree: { _, _ in
                order.append("read")
                let elements = visible ? [LiveElement(
                    snapshot: ElementSnapshot(
                        path: [0],
                        role: "AXButton",
                        identifier: "session-menu",
                        frame: Frame(x: 1, y: 2, width: 3, height: 4)
                    ),
                    element: targetElement
                )] : []
                return LiveTree(elements: elements)
            },
            snapshot: { element, path, hierarchy in
                order.append("refresh")
                XCTAssertTrue(CFEqual(element, targetElement))
                XCTAssertEqual(path, [0])
                XCTAssertTrue(hierarchy.isEmpty)
                return ElementSnapshot(
                    path: path,
                    role: "AXButton",
                    identifier: "session-menu",
                    frame: Frame(x: 100, y: 200, width: 20, height: 40)
                )
            },
            requireHitTarget: { element, point in
                order.append("hit")
                XCTAssertTrue(CFEqual(element, targetElement))
                XCTAssertEqual(point, Point(x: 110, y: 220))
            },
            physicalClick: { point in
                order.append("click")
                clickedPoint = point
                visible = false
            }
        )
        let response = try CommandRunner.run(HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .physicalClick,
            selector: selector,
            postcondition: Postcondition(selector: selector, condition: .absent)
        ), dependencies: dependencies)

        XCTAssertTrue(response.ok)
        XCTAssertEqual(clickedPoint, Point(x: 110, y: 220))
        XCTAssertEqual(
            order,
            [
                "load", "frontmost", "expose", "read", "read", "refresh",
                "hit", "frontmost", "click", "read",
            ]
        )
    }

    // A read that fails AFTER the click is not the same as one that fails
    // before it, and the two must never leave the helper looking alike.
    //
    // RED-FIRST: before awaitPostcondition retried its own reads, the first
    // post-click read threw straight out of the command, and the Go driver —
    // which classifies a bare `AX error -25202` as a PRE-click refusal — went
    // on to click Send a second time. Measured live on 1.46388.4: clicking
    // Send swaps the composer for the in-flight view, and that is precisely
    // when a tree read fails this way.
    func testClickPostconditionSurvivesATransientReadFailure() throws {
        let application = AXUIElementCreateSystemWide()
        let targetElement = AXUIElementCreateApplication(41_004)
        let selector = ControlSelector(role: "AXButton", identifier: "send")
        var clicks = 0
        var readsAfterClick = 0
        let dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in
                guard clicks > 0 else {
                    return LiveTree(elements: [LiveElement(
                        snapshot: ElementSnapshot(
                            path: [0],
                            role: "AXButton",
                            identifier: "send",
                            frame: Frame(x: 0, y: 0, width: 10, height: 10)
                        ),
                        element: targetElement
                    )])
                }
                readsAfterClick += 1
                // The renderer is mid-swap on the first look, settled after.
                if readsAfterClick == 1 {
                    throw HelperFailure(.actionFailed, "AX error -25202")
                }
                return LiveTree(elements: [])
            },
            snapshot: { element, path, _ in
                ElementSnapshot(
                    path: path,
                    role: "AXButton",
                    identifier: "send",
                    frame: Frame(x: 0, y: 0, width: 10, height: 10)
                )
                    .self
            },
            requireHitTarget: { _, _ in },
            physicalClick: { _ in clicks += 1 }
        )
        let response = try CommandRunner.run(HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .physicalClick,
            selector: selector,
            postcondition: Postcondition(selector: selector, condition: .absent)
        ), dependencies: dependencies)

        XCTAssertTrue(response.ok)
        XCTAssertEqual(clicks, 1, "the click must be posted exactly once")
        XCTAssertGreaterThan(readsAfterClick, 1, "the failed read must be retried, not propagated")
    }

    // And when the tree can never be read, absence of a finding and inability
    // to look must not produce the same message.
    //
    // RED-FIRST: this used to throw the injected accessibility error itself,
    // which the driver reads as "nothing happened" and retries.
    func testClickReportsAnUnverifiablePostconditionRatherThanTheReadFailure() {
        let application = AXUIElementCreateSystemWide()
        let targetElement = AXUIElementCreateApplication(41_005)
        let selector = ControlSelector(role: "AXButton", identifier: "send")
        var clicks = 0
        let dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in
                guard clicks > 0 else {
                    return LiveTree(elements: [LiveElement(
                        snapshot: ElementSnapshot(
                            path: [0],
                            role: "AXButton",
                            identifier: "send",
                            frame: Frame(x: 0, y: 0, width: 10, height: 10)
                        ),
                        element: targetElement
                    )])
                }
                throw HelperFailure(.actionFailed, "AX error -25202")
            },
            snapshot: { element, path, _ in
                ElementSnapshot(
                    path: path,
                    role: "AXButton",
                    identifier: "send",
                    frame: Frame(x: 0, y: 0, width: 10, height: 10)
                )
            },
            requireHitTarget: { _, _ in },
            physicalClick: { _ in clicks += 1 }
        )
        XCTAssertThrowsError(try CommandRunner.run(HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .physicalClick,
            selector: selector,
            postcondition: Postcondition(
                selector: selector,
                condition: .absent,
                timeoutMilliseconds: 100
            )
        ), dependencies: dependencies)) { error in
            guard let failure = error as? HelperFailure else {
                return XCTFail("error = \(error), want a HelperFailure")
            }
            XCTAssertEqual(failure.code, .postconditionFailed)
            XCTAssertTrue(
                failure.message.contains("could not be verified"),
                "message = \(failure.message)"
            )
            XCTAssertTrue(
                failure.message.contains("-25202"),
                "the message must name what stopped it: \(failure.message)"
            )
        }
        XCTAssertEqual(clicks, 1, "the click must be posted exactly once")
    }

    func testCommandRunnerPropagatesTreeReadFailureBeforeAction() {
        let application = AXUIElementCreateSystemWide()
        let injected = HelperFailure(.actionFailed, "injected tree read failure")
        var order: [String] = []
        var clicked = false
        let dependencies = makeCommandDependencies(
            application: application,
            record: { order.append($0) },
            readTree: { _, _ in
                order.append("read")
                throw injected
            },
            physicalClick: { _ in clicked = true }
        )
        let selector = ControlSelector(role: "AXButton", identifier: "session-menu")
        XCTAssertThrowsError(try CommandRunner.run(HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .physicalClick,
            selector: selector,
            postcondition: Postcondition(selector: selector, condition: .absent)
        ), dependencies: dependencies)) { error in
            XCTAssertEqual(error as? HelperFailure, injected)
        }
        XCTAssertFalse(clicked)
        XCTAssertEqual(order, ["load", "frontmost", "expose", "read"])
    }
}
