import AppKit
import Foundation
import OSLog

/// Activator for JetBrains IDEs (GoLand, IntelliJ IDEA, PyCharm, WebStorm,
/// Rider, CLion, RustRover). All embed JediTerm, which sets
/// `TERMINAL_EMULATOR=JetBrains-JediTerm` instead of `$TERM_PROGRAM`, so the
/// Go detection layer maps all of them to `termProgram = "jetbrains"`.
///
/// Activation uses AX title-match (same strategy as VS Code / Cursor): window
/// titles show the project folder name, which is sufficient for CWD-segment
/// scoring. We fan out over all known bundle IDs and pick the first one that
/// is currently running, so the activator works regardless of which JetBrains
/// product the user has open.
struct JetBrainsActivator: HostActivator {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "JetBrainsActivator")

    let termProgram = "jetbrains"

    // Not a single bundle ID — resolved at activation time from `bundleIDs`.
    let bundleID = ""

    private static let bundleIDs: [String] = [
        "com.jetbrains.goland",
        "com.jetbrains.intellij",
        "com.jetbrains.intellij.ce",
        "com.jetbrains.pycharm",
        "com.jetbrains.pycharm.ce",
        "com.jetbrains.WebStorm",
        "com.jetbrains.rider",
        "com.jetbrains.CLion",
        "com.jetbrains.rustrover",
    ]

    func activate(_ session: SessionState) -> Bool {
        let cwd = session.cwd
        guard !cwd.isEmpty else {
            Self.logger.info("no cwd for session \(session.id, privacy: .public)")
            return false
        }
        guard let bid = Self.runningBundleID() else {
            Self.logger.info("no running JetBrains IDE found")
            return false
        }
        let ok = AppActivator.activate(bundleID: bid) { activated in
            guard activated else { return }
            AXTitleMatchActivator.raiseMatchingWindow(bundleID: bid, cwd: cwd)
        }
        return ok
    }

    /// Returns the bundle ID of the first currently-running JetBrains IDE, or
    /// nil when none is open. Exposed as `internal` so unit tests can verify
    /// the fan-out logic without launching real apps.
    static func runningBundleID() -> String? {
        bundleIDs.first { !NSRunningApplication.runningApplications(withBundleIdentifier: $0).isEmpty }
    }
}
