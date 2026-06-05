import XCTest
@testable import Irrlicht

final class CLIToolInstallerTests: XCTestCase {
    func testChooseTargetPicksFirstWritableCandidate() {
        let target = CLIToolInstaller.chooseTarget(
            candidates: ["/nope/a", "/yes/b", "/yes/c"],
            isWritableDir: { $0.hasPrefix("/yes") }
        )
        XCTAssertEqual(target, "/yes/b")
    }

    func testChooseTargetFallsBackToSecondCandidate() {
        // The /usr/local/bin → /opt/homebrew/bin ladder: first not writable.
        let target = CLIToolInstaller.chooseTarget(
            candidates: ["/usr/local/bin", "/opt/homebrew/bin"],
            isWritableDir: { $0 == "/opt/homebrew/bin" }
        )
        XCTAssertEqual(target, "/opt/homebrew/bin")
    }

    func testChooseTargetReturnsNilWhenNothingWritable() {
        XCTAssertNil(CLIToolInstaller.chooseTarget(
            candidates: ["/a", "/b"],
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
        FileManager.default.createFile(atPath: dir + "/irrlicht-ls", contents: Data("real".utf8))

        let result = CLIToolInstaller.install(candidates: [dir])
        guard case .failed(let message) = result else {
            return XCTFail("install must fail in the test bundle, got \(result)")
        }
        // SwiftPM test bundles don't embed the Go binary — the unavailable
        // guard fires before any filesystem mutation.
        XCTAssertTrue(message.contains("not embedded"), "unexpected failure: \(message)")
        // The squatting file is untouched either way.
        XCTAssertEqual(
            try String(contentsOfFile: dir + "/irrlicht-ls", encoding: .utf8), "real"
        )
    }

    func testStatusIsUnavailableInTestBundle() {
        // SwiftPM test bundles carry no auxiliary executables; the Settings
        // section must land on .unavailable (section shows the dev-build
        // note instead of a dead button).
        XCTAssertEqual(CLIToolInstaller.status(), .unavailable)
    }

    private func makeTempDir() throws -> String {
        let dir = NSTemporaryDirectory() + "cli-tool-test-" + UUID().uuidString
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        return dir
    }
}
