import DesktopHelperCore
import Foundation
import XCTest
@testable import ClaudeDesktopHelper

final class ExecutableBoundaryTests: XCTestCase {
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
}
