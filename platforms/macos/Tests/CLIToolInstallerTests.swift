import XCTest
@testable import Irrlicht

final class CLIToolInstallerTests: XCTestCase {
    func testChooseTargetPicksFirstWritableCandidate() {
        let target = CLIToolInstaller.chooseTarget(
            candidates: ["/nope/a", "/yes/b", "/yes/c"],  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            isWritableDir: { $0.hasPrefix("/yes") }  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        )
        XCTAssertEqual(target, "/yes/b")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
    }

    func testChooseTargetFallsBackToSecondCandidate() {
        // The /usr/local/bin → /opt/homebrew/bin ladder: first not writable.
        let target = CLIToolInstaller.chooseTarget(
            candidates: ["/usr/local/bin", "/opt/homebrew/bin"],  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            isWritableDir: { $0 == "/opt/homebrew/bin" }  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        )
        XCTAssertEqual(target, "/opt/homebrew/bin")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
    }

    func testChooseTargetReturnsNilWhenNothingWritable() {
        XCTAssertNil(CLIToolInstaller.chooseTarget(
            candidates: ["/a", "/b"],  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
            isWritableDir: { _ in false }
        ))
    }

    func testInstallRefusesToReplaceRegularFile() throws {
        // Real temp dir with a regular file squatting on the link name —
        // install() must refuse rather than delete user data. bundledLs is
        // nil in the test bundle, so exercise the guard order first: with
        // no embedded binary the failure is "not embedded".
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(atPath: dir) }
        FileManager.default.createFile(atPath: dir + "/irrlicht-ls", contents: Data("real".utf8))  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint

        let result = CLIToolInstaller.install(candidates: [dir])
        guard case .failed(let message) = result else {
            return XCTFail("install must fail in the test bundle, got \(result)")
        }
        // SwiftPM test bundles don't embed the Go binary — the unavailable
        // guard fires before any filesystem mutation.
        XCTAssertTrue(message.contains("not embedded"), "unexpected failure: \(message)")
        // The squatting file is untouched either way.
        XCTAssertEqual(
            try String(contentsOfFile: dir + "/irrlicht-ls", encoding: .utf8), "real"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        )
    }

    func testStatusIsUnavailableInTestBundle() {
        // SwiftPM test bundles carry no auxiliary executables; the Settings
        // section must land on .unavailable (section shows the dev-build
        // note instead of a dead button).
        XCTAssertEqual(CLIToolInstaller.status(), .unavailable)
    }

    func testClearLinkSiteRemovesDanglingSymlink() throws {
        // The review-found bug: fileExists() traverses symlinks, so a
        // dangling link (bundle moved/deleted) was invisible to the old
        // check and createSymbolicLink failed with "file exists".
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(atPath: dir) }
        let link = dir + "/irrlicht-ls"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        try FileManager.default.createSymbolicLink(
            atPath: link, withDestinationPath: dir + "/gone-bundle/irrlicht-ls"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        )

        XCTAssertNil(CLIToolInstaller.clearLinkSite(link), "dangling symlink must be cleared, not reported")
        XCTAssertNil(try? FileManager.default.destinationOfSymbolicLink(atPath: link), "link should be gone")
    }

    func testClearLinkSiteRemovesValidSymlink() throws {
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(atPath: dir) }
        let target = dir + "/target"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        FileManager.default.createFile(atPath: target, contents: Data("bin".utf8))
        let link = dir + "/irrlicht-ls"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        try FileManager.default.createSymbolicLink(atPath: link, withDestinationPath: target)

        XCTAssertNil(CLIToolInstaller.clearLinkSite(link))
        XCTAssertNil(try? FileManager.default.destinationOfSymbolicLink(atPath: link))
        // Removing the link never touches its target.
        XCTAssertEqual(try String(contentsOfFile: target, encoding: .utf8), "bin")
    }

    func testClearLinkSiteRefusesRegularFile() throws {
        let dir = try makeTempDir()
        defer { try? FileManager.default.removeItem(atPath: dir) }
        let link = dir + "/irrlicht-ls"  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        FileManager.default.createFile(atPath: link, contents: Data("real".utf8))

        let message = CLIToolInstaller.clearLinkSite(link)
        XCTAssertNotNil(message)
        XCTAssertTrue(message?.contains("not a symlink") ?? false)
        XCTAssertEqual(try String(contentsOfFile: link, encoding: .utf8), "real", "regular file must be untouched")
    }

    private func makeTempDir() throws -> String {
        let dir = NSTemporaryDirectory() + "cli-tool-test-" + UUID().uuidString
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        return dir
    }
}
