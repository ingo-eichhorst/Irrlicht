import XCTest
@testable import Irrlicht

/// The behavioural half of #1669 / #1670: no path this app writes to resolves
/// inside the developer's real home while the suite is running.
///
/// # Why this is a path lock rather than a filesystem delta
///
/// `tools/lib/swift-suite.sh`'s witness watches `~/Library/Preferences` and
/// `~/Library/Sounds` for entries that appear across a run, and that shape
/// catches #1670 exactly. It cannot catch #1669, for two reasons that are worth
/// stating rather than leaving to be rediscovered:
///
/// * the worst of #1669 is an **overwrite** — `session-order.json` already
///   exists, so replacing its contents with test data adds no entry to any
///   directory and a name-delta witness reports clean.
/// * the level that WOULD see the other half is
///   `~/Library/Application Support/Irrlicht/instances/`, which the live daemon
///   writes to continuously (measured: two files modified inside 60s with
///   nothing of ours running), so watching it is a false-positive machine.
///
/// So that instance is locked here instead, on the resolved path, where there
/// is no noise at all.
///
/// # Order is load-bearing
///
/// The `AppHome` assertions come first and `return` on failure, before anything
/// that could *create* a directory. `SoundPlayer.soundsDirectory()` creates
/// `<home>/Library/Sounds` when it is absent, so a version of this test that
/// called it first would, on a machine where the redirect is broken AND that
/// directory does not exist, produce the very write it exists to forbid. A test
/// for a leak must not be able to leak while failing.
@MainActor
final class RealHomeIsolationTests: XCTestCase {

    /// The home `cfprefsd` and the user's shell both mean — read from the
    /// password database, never from `HOME` or `NSHomeDirectory()`, both of
    /// which this process is entitled to have moved. Shared with #1661's
    /// witness rather than spelled a second way.
    private var realHome: URL { PreferencesDirectoryWitness.passwordDatabaseHome }

    // MARK: - The witness's own premises

    /// Without this, every assertion below is vacuous in the direction that
    /// matters: if `realHome` degenerated to something no path can be inside,
    /// "outside the real home" would be true of the real home itself.
    func testTheRealHomeIsAPlausibleDirectory() {
        var isDirectory: ObjCBool = false
        XCTAssertTrue(
            FileManager.default.fileExists(atPath: realHome.path, isDirectory: &isDirectory),
            "the password-database home \(realHome.path) does not exist — this test cannot tell "
            + "'outside the real home' from 'the real home could not be found'"
        )
        XCTAssertTrue(isDirectory.boolValue, "\(realHome.path) is not a directory")
        XCTAssertGreaterThan(
            realHome.pathComponents.count, 1,
            "the password-database home resolved to \(realHome.path) — a root-like answer makes "
            + "containment trivially true and every assertion here meaningless"
        )
        // Deliberately NOT cross-checked against Foundation's own home. That
        // would read as a stronger premise and is in fact a fragile one: a
        // developer running the suite under `CFFIXED_USER_HOME` (which does
        // move Foundation's answer, measured) would fail it for a reason
        // unrelated to this lock, and `getpwuid` is the authority here anyway —
        // it is what `cfprefsd` and the shell's `~user` both mean.
    }

    func testAppHomeReportsThisProcessAsATestBundle() {
        // If this is ever false in a test run, the redirect is off and every
        // path below is the real one — so it is asserted rather than assumed.
        XCTAssertTrue(AppHome.isRunningTests,
                      "AppHome does not recognise this process as a test bundle")
    }

    // MARK: - The lock

    func testNothingThisAppWritesToResolvesInsideTheRealHome() throws {
        // Cheapest and side-effect-free first — see the note on ordering above.
        try assertOutsideRealHome(AppHome.url, "AppHome.url")
        try assertOutsideRealHome(AppHome.library, "AppHome.library")

        let manager = SessionManager()
        try assertOutsideRealHome(manager.instancesPath, "SessionManager.instancesPath")
        try assertOutsideRealHome(manager.orderFilePath, "SessionManager.orderFilePath")

        // Creates the directory it returns, which is why it is last.
        try assertOutsideRealHome(try SoundPlayer.soundsDirectory(), "SoundPlayer.soundsDirectory()")

        let preview = SoundChoice.custom(installedFilename: "IrrlichtCustom-ready.aiff",
                                         displayPath: "/dev/null").previewURL
        try assertOutsideRealHome(try XCTUnwrap(preview), "SoundChoice.previewURL")
    }

    /// The test home is a real, writable directory — otherwise "outside the
    /// real home" would also be satisfied by a path that resolves nowhere, and
    /// the suite's own writes would fail for a reason unrelated to the lock.
    func testTheTestHomeExistsAndIsWritable() throws {
        var isDirectory: ObjCBool = false
        XCTAssertTrue(FileManager.default.fileExists(atPath: AppHome.url.path, isDirectory: &isDirectory),
                      "AppHome.url \(AppHome.url.path) does not exist")
        XCTAssertTrue(isDirectory.boolValue, "AppHome.url is not a directory")
        XCTAssertTrue(FileManager.default.isWritableFile(atPath: AppHome.url.path),
                      "AppHome.url is not writable — the suite's writes would fail here, not be redirected")
    }

    // MARK: - Containment

    /// Component-wise, not `hasPrefix`: `/Users/ingo2` starts with the string
    /// `/Users/ingo` and is a different home.
    private static func contains(_ parent: URL, _ child: URL) -> Bool {
        let p = parent.standardizedFileURL.resolvingSymlinksInPath().pathComponents
        let c = child.standardizedFileURL.resolvingSymlinksInPath().pathComponents
        guard c.count >= p.count else { return false }
        return Array(c.prefix(p.count)) == p
    }

    /// `throws` rather than `XCTAssert`, so a failure stops the caller before
    /// the next resolution — which for `soundsDirectory()` is also a creation.
    private func assertOutsideRealHome(_ url: URL, _ label: String,
                                       file: StaticString = #filePath, line: UInt = #line) throws {
        guard !Self.contains(realHome, url) else {
            let message = """
                \(label) resolved to \(url.path), inside the real home \(realHome.path).
                Under `swift test` this app must not write there: #1669 overwrote the developer's \
                own session-order.json with test data and dropped fixtures into the live daemon's \
                instances/, and #1670 installed audio into ~/Library/Sounds behind a `defer` that \
                aborts skip. Resolve through `AppHome`.
                """
            XCTFail(message, file: file, line: line)
            throw NotIsolated(message: message)
        }
    }

    private struct NotIsolated: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }
}
