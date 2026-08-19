import Foundation

/// The one place this app resolves the user's home directory.
///
/// # What #1669 and #1670 are
///
/// Five call sites resolved the real home directly and wrote under it. Three of
/// them are reached from the test suite, so `swift test` on a developer machine
/// wrote into the *live* directories the running daemon and app own:
///
/// ```
///   ~/Library/Application Support/Irrlicht/instances/<uuid>.json   fixture, `defer`-cleaned
///   ~/Library/Application Support/Irrlicht/session-order.json      OVERWRITTEN, no cleanup at all
///   ~/Library/Sounds/IrrlichtCustom-<event>.<ext>                  installed, `defer`-cleaned
///   ~/Library/Sounds/                                              created if absent
/// ```
///
/// The `defer`s hold on the ordinary path and are skipped on exactly the runs
/// that fail: XCTest's stall detector answers a hung expectation by `abort()`ing
/// the process (#1523), `tools/lib/swift-suite.sh` kills the tree at 240s, and
/// `--budget` kills the gate. `session-order.json` had no cleanup in any case —
/// a suite run replaced the developer's real session ordering with test data
/// (`{"order":["b","a"],"version":1}`, measured).
///
/// # Why this shape
///
/// **Not a `HOME` redirect.** Measured on this machine with a standalone binary:
///
/// ```
///   HOME=<tmp>                          NSHomeDirectory = /Users/ingo   (unchanged)
///   HOME=<tmp> CFFIXED_USER_HOME=<tmp>  NSHomeDirectory = <tmp>         (redirected)
/// ```
///
/// `HOME` alone moves nothing in Foundation, so a fix built on it would look
/// like a fix and change nothing. `CFFIXED_USER_HOME` does work — the whole
/// suite passes under it, 352 tests, and the redirected tree is where the four
/// paths above land — but it only applies when the suite is launched through a
/// script that sets it. A developer typing `swift test`, or running the suite
/// from Xcode, is exactly who got hit, and would still be.
///
/// **Not a cleanup sweep.** A function whose job is to delete files from a
/// directory it did not create, in the developer's home, is what removed ~1895
/// files from a real `~/Library/Preferences` while #1661 was being fixed.
/// Nothing here deletes anything, ever. The test home is created under
/// `NSTemporaryDirectory()`, which the OS reclaims; no code of ours removes it.
///
/// So the redirect happens **inside the process**, keyed on the same signal
/// `SessionManager.isRunningUnitTests` already uses to keep unit tests off the
/// developer's live daemon socket (#832 — "so unit tests never race against
/// whatever daemon happens to be reachable on the machine"). That decision was
/// already made for the network; this is the same decision for the filesystem,
/// and it holds however the suite is launched.
///
/// `RealHomeIsolationTests` is the behavioural half — it fails if any of the
/// resolved paths lands inside the password-database home — and
/// `RealHomePathLintTests` is the structural half: it fails the build on a
/// home-resolving construct anywhere in the app or test targets except the two
/// files that are allowed one. Together they are what makes a *future* call
/// site inherit this rather than have to remember it.
enum AppHome {

    /// True when this process is a test bundle.
    ///
    /// The same expression `SessionManager.isRunningUnitTests` uses, and
    /// deliberately not a copy of that property: it is `@MainActor`-isolated on
    /// an `ObservableObject`, and this type is consulted from `nonisolated`
    /// static context (`SoundChoice.previewURL`, `notificationSound(for:)`).
    ///
    /// It answers for `swift test` and for Xcode alike. `XCTestConfigurationFilePath`
    /// would not: only Xcode's runner sets it, and this project's actual test
    /// command is `swift test`.
    static var isRunningTests: Bool { NSClassFromString("XCTestCase") != nil }

    /// The home directory this process may write into: the user's real home in
    /// the app, a per-process temporary directory under test.
    static var url: URL { isRunningTests ? testHome : realHome }

    /// `<home>/Library`. Equivalent to
    /// `FileManager.url(for: .libraryDirectory, in: .userDomainMask)` for a
    /// non-sandboxed app — that API resolves the same `<home>/Library` — with
    /// the difference that this one is redirected under test and that one is
    /// not.
    static var library: URL { url.appendingPathComponent("Library", isDirectory: true) }

    private static var realHome: URL { FileManager.default.homeDirectoryForCurrentUser }

    /// Created once per process, named after the pid so two concurrent test
    /// processes cannot share one.
    ///
    /// `fatalError` rather than a fallback, and that is the load-bearing line:
    /// the only fallback available is the real home, which is the outcome this
    /// type exists to make impossible. A trap is a legible red; a silent
    /// fallback is #1669 and #1670 coming back with a mechanism that claims to
    /// have fixed them.
    private static let testHome: URL = {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory(), isDirectory: true)
            .appendingPathComponent("irrlicht-test-home-\(ProcessInfo.processInfo.processIdentifier)",
                                    isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        } catch {
            fatalError("""
                AppHome could not create the test home at \(dir.path): \(error).
                Refusing to fall back to the real home — that is #1669/#1670.
                """)
        }
        return dir
    }()
}
