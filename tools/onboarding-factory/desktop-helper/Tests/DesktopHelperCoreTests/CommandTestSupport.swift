import ApplicationServices
import DesktopHelperCore
import XCTest
@testable import ClaudeDesktopHelper

func makeCommandDependencies(
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
