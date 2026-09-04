import ApplicationServices
import DesktopHelperCore
import Foundation
import XCTest
@testable import ClaudeDesktopHelper

final class DesktopHelperCoreTests: XCTestCase {
    private let fixtureDefinitions = [
        ProbeDefinition(name: "local_environment_selector", selectors: [
            ControlSelector(role: "AXButton", identifier: "environment-selector"),
        ]),
        ProbeDefinition(name: "project_selector", selectors: [
            ControlSelector(role: "AXButton", identifier: "project-selector"),
        ]),
        ProbeDefinition(name: "prompt_field", selectors: [
            ControlSelector(role: "AXTextArea", identifier: "prompt-input"),
        ]),
        ProbeDefinition(name: "send_control", selectors: [
            ControlSelector(role: "AXButton", identifier: "send-button"),
        ]),
        ProbeDefinition(name: "model_selector", selectors: [
            ControlSelector(role: "AXButton", identifier: "model-selector"),
        ]),
        ProbeDefinition(name: "mode_selector", selectors: [
            ControlSelector(role: "AXButton", identifier: "mode-selector"),
        ]),
        ProbeDefinition(name: "session_menu", selectors: [
            ControlSelector(role: "AXButton", identifier: "session-menu"),
        ]),
        ProbeDefinition(name: "stop_control", selectors: [
            ControlSelector(role: "AXButton", identifier: "stop-button"),
        ]),
    ]

    func testSelectorUsesStableAttributesAndHierarchySuffix() throws {
        let element = ElementSnapshot(
            path: [0, 2, 1],
            role: "AXButton",
            identifier: "mode-selector",
            hierarchy: ["AXApplication", "AXWindow", "AXGroup"]
        )
        XCTAssertTrue(ControlFinder.matches(
            element,
            selector: ControlSelector(
                role: "AXButton",
                identifier: "mode-selector",
                hierarchy: ["AXWindow", "AXGroup"]
            )
        ))
        XCTAssertFalse(ControlFinder.matches(
            element,
            selector: ControlSelector(role: "AXButton", identifier: "model-selector")
        ))
    }

    func testUniqueSelectorReportsMissingAndAmbiguousControls() throws {
        let button = ElementSnapshot(path: [0], role: "AXButton", identifier: "send-button")
        XCTAssertThrowsError(try ControlFinder.unique(
            in: [],
            matching: ControlSelector(role: "AXButton", identifier: "send-button")
        )) { error in
            XCTAssertEqual(error as? HelperFailure, HelperFailure(
                .controlMissing,
                "The selector did not match a visible control."
            ))
        }
        XCTAssertThrowsError(try ControlFinder.unique(
            in: [button, ElementSnapshot(path: [1], role: "AXButton", identifier: "send-button")],
            matching: ControlSelector(role: "AXButton", identifier: "send-button")
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .controlAmbiguous)
        }
    }

    func testCenterPointUsesFreshGeometry() throws {
        XCTAssertEqual(
            try ClickPlan(freshFrame: Frame(x: 10, y: 20, width: 80, height: 30)).point,
            Point(x: 50, y: 35)
        )
        XCTAssertEqual(
            try ClickPlan(freshFrame: Frame(x: 200, y: 100, width: 40, height: 100)).point,
            Point(x: 220, y: 150)
        )
    }

    func testInvalidGeometryReportsStaleControl() {
        for frame in [
            Frame(x: 0, y: 0, width: 0, height: 10),
            Frame(x: 0, y: 0, width: 10, height: -1),
            Frame(x: .nan, y: 0, width: 10, height: 10),
        ] {
            XCTAssertThrowsError(try ClickPlan(freshFrame: frame)) { error in
                XCTAssertEqual((error as? HelperFailure)?.code, .staleControl)
            }
        }
    }

    func testCompleteProbeFixture() throws {
        let matches = try ProbeValidator.validate(
            elements: try completeFixture(),
            definitions: fixtureDefinitions
        )
        XCTAssertEqual(matches.count, fixtureDefinitions.count)
        XCTAssertEqual(fixtureDefinitions.count, 8)
        XCTAssertEqual(matches.last?.identifier, "stop-button")
        XCTAssertTrue(matches.allSatisfy(\.visible))
    }

    func testProbeReportsDistinctMissingAmbiguousAndStaleReasons() throws {
        let complete = try completeFixture()
        XCTAssertThrowsError(try ProbeValidator.validate(
            elements: complete.filter { $0.identifier != "project-selector" },
            definitions: fixtureDefinitions
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .controlMissing)
        }

        let duplicate = ElementSnapshot(
            path: [9],
            role: "AXButton",
            identifier: "model-selector",
            frame: Frame(x: 1, y: 1, width: 20, height: 20)
        )
        XCTAssertThrowsError(try ProbeValidator.validate(
            elements: complete + [duplicate],
            definitions: fixtureDefinitions
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .controlAmbiguous)
        }

        let stale = complete.map { element in
            guard element.identifier == "mode-selector" else { return element }
            return ElementSnapshot(
                path: element.path,
                role: element.role,
                identifier: element.identifier,
                frame: Frame(x: 0, y: 0, width: 0, height: 20),
                hierarchy: element.hierarchy
            )
        }
        XCTAssertThrowsError(try ProbeValidator.validate(
            elements: stale,
            definitions: fixtureDefinitions
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .staleControl)
        }
    }

    func testResponsesDoNotContainTextValues() throws {
        let secret = "do-not-leak-this-prompt"
        let request = HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .setValue,
            selector: ControlSelector(role: "AXTextArea"),
            value: secret
        )
        XCTAssertTrue(String(decoding: try JSONEncoder().encode(request), as: UTF8.self).contains(secret))

        let response = HelperResponse(
            ok: true,
            command: .inspect,
            elements: [ElementSnapshot(
                path: [0],
                role: "AXTextArea",
                identifier: "prompt-input",
                valueRedacted: true
            )]
        )
        let encoded = String(decoding: try JSONEncoder().encode(response), as: UTF8.self)
        XCTAssertFalse(encoded.contains(secret))
        XCTAssertFalse(encoded.contains("\"value\":"))
        XCTAssertTrue(encoded.contains("\"valueRedacted\":true"))
    }

    func testProtocolAndTraversalValidation() throws {
        let data = Data(#"{"protocolVersion":1,"command":"inspect"}"#.utf8)
        XCTAssertEqual(try JSONDecoder().decode(HelperRequest.self, from: data).command, .inspect)
        XCTAssertThrowsError(try TraversalLimits(maxDepth: 0, maxNodes: 5_000).validated()) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
        }
    }

    func testActionRequestsRequirePostconditionsBeforeLiveAccess() {
        let request = HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .physicalClick,
            selector: ControlSelector(role: "AXButton")
        )
        XCTAssertThrowsError(try RequestValidator.validate(request)) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
            XCTAssertEqual((error as? HelperFailure)?.message, "This action requires a postcondition.")
        }
    }

    func testProbeRequiresUniqueNamesAndNonEmptySelectors() {
        let duplicate = ProbeDefinition(
            name: "prompt",
            selectors: [ControlSelector(role: "AXTextArea")]
        )
        let request = HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .probe,
            probes: [duplicate, duplicate]
        )
        XCTAssertThrowsError(try RequestValidator.validate(request)) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
        }
    }

    func testMissingAccessibilityGrantHasStableFailure() {
        XCTAssertThrowsError(try PreflightGate.validate(
            installed: true,
            running: true,
            accessibilityTrusted: false
        )) { error in
            let failure = error as? HelperFailure
            XCTAssertEqual(failure?.code, .accessibilityDenied)
            XCTAssertEqual(failure?.exitCode, 12)
            XCTAssertEqual(
                failure?.message,
                "macOS Accessibility access is not granted to claude-desktop-helper."
            )
        }
    }

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
        let dependencies = makeDependencies(
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
        let dependencies = makeDependencies(
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
        let dependencies = makeDependencies(
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

    func testCommandRunnerPropagatesTreeReadFailureBeforeAction() {
        let application = AXUIElementCreateSystemWide()
        let injected = HelperFailure(.actionFailed, "injected tree read failure")
        var order: [String] = []
        var clicked = false
        let dependencies = makeDependencies(
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

    func testStrictDecoderRejectsUnknownAndCommandInapplicableFieldsBeforeRun() throws {
        let unknownCoordinate = Data(
            #"{"protocolVersion":1,"command":"preflight","x":42}"#.utf8
        )
        let inapplicableSelector = Data(
            #"{"protocolVersion":1,"command":"preflight","selector":{"role":"AXButton"}}"#.utf8
        )
        let nestedUnknown = Data(
            #"{"protocolVersion":1,"command":"inspect","limits":{"maxDepth":4,"maxNodes":20,"extra":true}}"#.utf8
        )
        for input in [unknownCoordinate, inapplicableSelector, nestedUnknown] {
            var runnerCalled = false
            let result = RequestProcessor.process(input) { request in
                runnerCalled = true
                return HelperResponse(ok: true, command: request.command)
            }
            XCTAssertFalse(runnerCalled)
            XCTAssertEqual(result.exitCode, 2)
            XCTAssertEqual(result.response.error?.code, .invalidRequest)
        }

        let valid = Data(#"{"protocolVersion":1,"command":"preflight"}"#.utf8)
        let decoded = try StrictRequestDecoder.decode(valid)
        XCTAssertEqual(decoded.command, .preflight)
    }

    func testInjectedDesktopProcessBoundaryClassifiesMissingStates() {
        let notInstalled = DesktopEnvironment(
            applicationURL: { nil },
            runningApplications: { [] },
            accessibilityTrusted: { true },
            desktopVersion: { _ in XCTFail("version must not be read"); return nil },
            bundledClaudeCodeVersion: {
                XCTFail("bundled metadata must not be read")
                return ""
            }
        )
        XCTAssertThrowsError(try ClaudeDesktopContext.load(environment: notInstalled)) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .appNotInstalled)
        }

        let notRunning = DesktopEnvironment(
            applicationURL: { URL(fileURLWithPath: "/Applications/Claude.app") },
            runningApplications: { [] },
            accessibilityTrusted: { true },
            desktopVersion: { _ in XCTFail("version must not be read"); return nil },
            bundledClaudeCodeVersion: {
                XCTFail("bundled metadata must not be read")
                return ""
            }
        )
        XCTAssertThrowsError(try ClaudeDesktopContext.load(environment: notRunning)) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .appNotRunning)
        }
    }

    func testInjectedFilesystemSelectsLatestVerifiedBundledMetadata() throws {
        let support = URL(fileURLWithPath: "/virtual/Application Support", isDirectory: true)
        let versions = ["2.1.9", "2.1.10", "9.9.9"].map {
            support.appendingPathComponent("Claude/claude-code/\($0)", isDirectory: true)
        }
        let filesystem = ClaudeCodeFilesystem(
            applicationSupportDirectory: { support },
            directories: { _ in versions },
            fileExists: { verified in
                verified.deletingLastPathComponent().lastPathComponent != "9.9.9"
            },
            bundleMetadata: { app in
                let version = app.deletingLastPathComponent().lastPathComponent
                return BundleMetadata(
                    bundleIdentifier: ClaudeDesktopContext.claudeCodeBundleIdentifier,
                    version: version
                )
            }
        )
        XCTAssertEqual(
            try ClaudeDesktopContext.latestBundledClaudeCodeVersion(filesystem: filesystem),
            "2.1.10"
        )
    }

    func testSemanticVersionOrdersNumericComponents() {
        XCTAssertLessThan(SemanticVersion("2.1.99")!, SemanticVersion("2.1.260")!)
        XCTAssertLessThan(SemanticVersion("2.1.260")!, SemanticVersion("2.2.0")!)
        XCTAssertNil(SemanticVersion("latest"))
    }

    private func completeFixture() throws -> [ElementSnapshot] {
        let url = try XCTUnwrap(Bundle.module.url(
            forResource: "complete-tree",
            withExtension: "json",
            subdirectory: "Fixtures"
        ))
        return try JSONDecoder().decode([ElementSnapshot].self, from: Data(contentsOf: url))
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

    private func makeDependencies(
        application: AXUIElement,
        record: @escaping (String) -> Void,
        readTree: @escaping (AXUIElement, TraversalLimits) throws -> LiveTree,
        setValue: @escaping (String, AXUIElement) throws -> Void = { _, _ in
            XCTFail("Unexpected setValue boundary call.")
        },
        focus: @escaping (AXUIElement) throws -> Void = { _ in
            XCTFail("Unexpected focus boundary call.")
        },
        isFocused: @escaping (AXUIElement) throws -> Bool = { _ in
            XCTFail("Unexpected isFocused boundary call.")
            return false
        },
        valueAttribute: @escaping (AXUIElement) throws -> String? = { _ in
            XCTFail("Unexpected valueAttribute boundary call.")
            return nil
        },
        snapshot: @escaping (AXUIElement, [Int], [String]) throws -> ElementSnapshot = { _, _, _ in
            XCTFail("Unexpected snapshot boundary call.")
            throw HelperFailure(.actionFailed, "Unexpected snapshot boundary call.")
        },
        requireHitTarget: @escaping (AXUIElement, Point) throws -> Void = { _, _ in
            XCTFail("Unexpected requireHitTarget boundary call.")
        },
        postKeyboardEvent: @escaping (UInt16, [String]) throws -> Void = { _, _ in
            XCTFail("Unexpected postKeyboardEvent boundary call.")
        },
        physicalClick: @escaping (Point) throws -> Void = { _ in
            XCTFail("Unexpected physicalClick boundary call.")
        }
    ) -> CommandDependencies {
        CommandDependencies(
            loadContext: {
                record("load")
                return CommandContext(
                    status: AppStatus(
                        bundleIdentifier: "com.anthropic.claudefordesktop",
                        installed: true,
                        running: true,
                        accessibilityTrusted: true,
                        desktopVersion: "test",
                        bundledClaudeCodeVersion: "test"
                    ),
                    application: application,
                    requireFrontmost: { record("frontmost") }
                )
            },
            exposeAccessibility: { element in
                record("expose")
                XCTAssertTrue(CFEqual(element, application))
            },
            readTree: readTree,
            setValue: setValue,
            focus: focus,
            isFocused: isFocused,
            valueAttribute: valueAttribute,
            snapshot: snapshot,
            requireHitTarget: requireHitTarget,
            postKeyboardEvent: postKeyboardEvent,
            physicalClick: physicalClick
        )
    }
}
