import ApplicationServices
import DesktopHelperCore
import XCTest
@testable import ClaudeDesktopHelper

// type_text exists because `keyboard` posts a PHYSICAL key code, which macOS
// maps through the active layout. Measured 2026-09-07: the driver's US table
// typed "-" for every "/" on a German layout, three times, and only the
// value_equals postcondition caught it. A character carried in the event has no
// layout to be wrong about.
final class TypeTextTests: XCTestCase {
    private let application = AXUIElementCreateSystemWide()

    private func promptTarget(_ pid: pid_t) -> LiveElement {
        LiveElement(
            snapshot: ElementSnapshot(path: [0], role: "AXTextArea", identifier: "prompt-input"),
            element: AXUIElementCreateApplication(pid)
        )
    }

    private var promptSelector: ControlSelector {
        ControlSelector(role: "AXTextArea", identifier: "prompt-input")
    }

    private func typeRequest(_ text: String, expecting expected: String) -> HelperRequest {
        HelperRequest(
            protocolVersion: desktopHelperProtocolVersion,
            command: .typeText,
            selector: promptSelector,
            value: text,
            postcondition: Postcondition(
                selector: promptSelector,
                condition: .valueEquals,
                value: expected
            )
        )
    }

    // Behaviour: one event per character, in order, and the postcondition is
    // what decides success.
    func testTypeTextPostsOneEventPerCharacterInOrder() throws {
        let target = promptTarget(42_001)
        var typed = ""
        let dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in LiveTree(elements: [target]) },
            focus: { _ in },
            isFocused: { _ in true },
            valueAttribute: { _ in typed },
            postTextEvent: { character in typed += character }
        )

        let response = try CommandRunner.run(
            typeRequest("/compact", expecting: "/compact"),
            dependencies: dependencies
        )

        XCTAssertTrue(response.ok)
        XCTAssertEqual(typed, "/compact")
        XCTAssertEqual(response.action?.postcondition, .valueEquals)
        XCTAssertEqual(response.action?.valueRedacted, true, "the helper must never echo typed text")
    }

    // RED-FIRST shape: with the per-character KeyboardEventBoundary removed —
    // one check before the loop instead of one inside it — this test fails with
    // typed == "/com…" fully written, because nothing re-reads focus. The guard
    // is the whole reason type_text is safe to use for a long argument.
    func testTypeTextStopsAtTheCharacterWhereFocusIsLost() throws {
        let target = promptTarget(42_002)
        var typed = ""
        var focusReads = 0
        let dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in LiveTree(elements: [target]) },
            focus: { _ in },
            isFocused: { _ in
                focusReads += 1
                return focusReads <= 3
            },
            valueAttribute: { _ in typed },
            postTextEvent: { character in typed += character }
        )

        XCTAssertThrowsError(try CommandRunner.run(
            typeRequest("/compact", expecting: "/compact"),
            dependencies: dependencies
        )) { error in
            guard let failure = error as? HelperFailure else {
                return XCTFail("expected a HelperFailure, got \(error)")
            }
            XCTAssertEqual(failure.code, .actionFailed)
        }
        XCTAssertEqual(typed, "/co", "typing must stop at the character where focus was lost")
    }

    // A window that is no longer frontmost is the other way a long argument
    // lands somewhere it was never meant to go.
    func testTypeTextStopsWhenTheApplicationStopsBeingFrontmost() throws {
        let target = promptTarget(42_003)
        var typed = ""
        var frontmostChecks = 0
        var dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in LiveTree(elements: [target]) },
            focus: { _ in },
            isFocused: { _ in true },
            valueAttribute: { _ in typed },
            postTextEvent: { character in typed += character }
        )
        let original = dependencies.loadContext
        dependencies = CommandDependencies(
            loadContext: {
                let context = try original()
                return CommandContext(
                    status: context.status,
                    application: context.application,
                    requireFrontmost: {
                        frontmostChecks += 1
                        if frontmostChecks > 3 {
                            throw HelperFailure(.actionFailed, "Claude is no longer frontmost.")
                        }
                    }
                )
            },
            exposeAccessibility: dependencies.exposeAccessibility,
            readTree: dependencies.readTree,
            setValue: dependencies.setValue,
            focus: dependencies.focus,
            isFocused: dependencies.isFocused,
            valueAttribute: dependencies.valueAttribute,
            snapshot: dependencies.snapshot,
            requireHitTarget: dependencies.requireHitTarget,
            postKeyboardEvent: dependencies.postKeyboardEvent,
            postTextEvent: dependencies.postTextEvent,
            physicalClick: dependencies.physicalClick
        )

        XCTAssertThrowsError(try CommandRunner.run(
            typeRequest("/compact", expecting: "/compact"),
            dependencies: dependencies
        ))
        XCTAssertLessThan(typed.count, 8, "typing must stop once the app is no longer frontmost")
    }

    // The false-to-true rule applies here like it does to every other action:
    // typing a string the composer already holds is a no-op that must not
    // report success.
    func testTypeTextRefusesAPostconditionThatIsAlreadyTrue() throws {
        let target = promptTarget(42_004)
        let dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in LiveTree(elements: [target]) },
            focus: { _ in },
            valueAttribute: { _ in "/compact" }
        )

        XCTAssertThrowsError(try CommandRunner.run(
            typeRequest("/compact", expecting: "/compact"),
            dependencies: dependencies
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .postconditionFailed)
        }
    }

    func testTypeTextRefusesAnEmptyValue() throws {
        let target = promptTarget(42_005)
        let dependencies = makeCommandDependencies(
            application: application,
            record: { _ in },
            readTree: { _, _ in LiveTree(elements: [target]) }
        )

        XCTAssertThrowsError(try CommandRunner.run(
            typeRequest("", expecting: "anything"),
            dependencies: dependencies
        )) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
        }
    }

    // Return means "accept the highlighted slash command" in the Desktop
    // composer, and a bare newline in a prompt submits the turn. Either is an
    // action the caller did not ask for, so the request layer refuses control
    // characters outright and makes the caller name a keyCode instead.
    func testRequestLayerRefusesControlCharactersInTypedText() throws {
        for text in ["one\ntwo", "tab\there", "carriage\rreturn"] {
            let json = """
            {"protocolVersion":\(desktopHelperProtocolVersion),"command":"type_text",
             "selector":{"role":"AXTextArea","identifier":"prompt-input"},
             "value":\(String(data: try JSONEncoder().encode(text), encoding: .utf8)!),
             "postcondition":{"selector":{"role":"AXTextArea","identifier":"prompt-input"},
                              "condition":"value_equals","value":"x","timeoutMilliseconds":1000}}
            """
            XCTAssertThrowsError(
                try StrictRequestDecoder.decode(Data(json.utf8)),
                "control characters must be refused: \(text.debugDescription)"
            ) { error in
                XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
            }
        }
    }

    func testRequestLayerRefusesTypedTextWithoutAPostcondition() throws {
        let json = """
        {"protocolVersion":\(desktopHelperProtocolVersion),"command":"type_text",
         "selector":{"role":"AXTextArea","identifier":"prompt-input"},"value":"hello"}
        """
        XCTAssertThrowsError(try StrictRequestDecoder.decode(Data(json.utf8))) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
        }
    }

    func testRequestLayerRefusesAKeyCodeOnTypedText() throws {
        let json = """
        {"protocolVersion":\(desktopHelperProtocolVersion),"command":"type_text",
         "selector":{"role":"AXTextArea","identifier":"prompt-input"},"value":"hello","keyCode":36,
         "postcondition":{"selector":{"role":"AXTextArea","identifier":"prompt-input"},
                          "condition":"value_equals","value":"hello","timeoutMilliseconds":1000}}
        """
        XCTAssertThrowsError(try StrictRequestDecoder.decode(Data(json.utf8))) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
        }
    }
}
