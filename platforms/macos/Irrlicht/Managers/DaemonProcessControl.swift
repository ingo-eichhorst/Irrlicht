import Foundation
import os

/// What `DaemonManager` does to real processes and to the daemon's port: the
/// reachability probe, the stale-daemon kill and the spawn.
///
/// # Why this is a seam
///
/// `DaemonManager.start()` probes `GET <DaemonEndpoint>/state` with a 2 s
/// timeout and, on a miss at the default port, runs `pkill -x irrlichd` —
/// which matches every `irrlichd` on the machine by NAME, the developer's
/// production daemon included — and then spawns `core/bin/irrlichd` when a dev
/// build of it exists next to the sources. `DaemonManagerTests` calls
/// `start()`, so before this seam a test run on a loaded machine (where a 2 s
/// probe can miss) could take the developer's live daemon down and replace it
/// with a dev build. So a `DaemonManager` in a test process gets a
/// `RecordingDaemonProcessControl`, which touches no process and no socket;
/// `DaemonManagerProcessControlGuardTests` is the guard.
protocol DaemonProcessControl: AnyObject, Sendable {
    /// The `irrlichd` executable a spawn launches, or nil when none exists.
    var daemonExecutableURL: URL? { get }
    /// True if a daemon answers on `DaemonEndpoint`.
    func isDaemonReachable() async -> Bool
    /// Kill `irrlichd` processes left over from a previous launch.
    func killStaleDaemons()
    /// Start `process` (already configured by `DaemonManager`).
    func run(_ process: Process) throws
}

/// The app's process control: the code `DaemonManager` carried before the
/// seam, moved here unchanged.
final class LiveDaemonProcessControl: DaemonProcessControl, @unchecked Sendable {
    private let logger = Logger(subsystem: "io.irrlicht.app", category: "DaemonManager")

    /// Path to the `irrlichd` binary inside the app bundle, with a dev-mode fallback.
    var daemonExecutableURL: URL? {
        if let url = Bundle.main.url(forAuxiliaryExecutable: "irrlichd") {
            return url
        }
        // Dev fallback: look relative to the Swift source tree (core/bin/irrlichd)
        let devURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // Managers/
            .deletingLastPathComponent() // Irrlicht/
            .deletingLastPathComponent() // macos/
            .deletingLastPathComponent() // platforms/
            .deletingLastPathComponent() // repo root
            .appendingPathComponent("core/bin/irrlichd")
        return FileManager.default.fileExists(atPath: devURL.path) ? devURL : nil
    }

    /// Returns true if an irrlichd instance is already listening on the expected port.
    func isDaemonReachable() async -> Bool {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/state") else { return false }
        var request = URLRequest(url: url)
        request.timeoutInterval = 2
        do {
            let (_, response) = try await URLSession.shared.data(for: request)
            return (response as? HTTPURLResponse)?.statusCode == 200
        } catch {
            return false
        }
    }

    /// Kill any `irrlichd` processes left over from a previous app launch,
    /// LaunchAgent, or manual invocation so we start with a clean slate.
    func killStaleDaemons() {
        // On a custom port we're a dev instance coexisting with the production
        // daemon (port 7837). pkill matches by process name and can't target a
        // port, so a global kill here would take production down too — skip it.
        if DaemonEndpoint.isCustomPort {
            logger.info("Custom daemon port \(DaemonEndpoint.port) — skipping global pkill to leave production running")
            return
        }
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/pkill")  // NOSONAR (swift:S1075) — local filesystem/binary path, not a network endpoint
        task.arguments = ["-x", "irrlichd"]
        task.standardOutput = FileHandle.nullDevice
        task.standardError = FileHandle.nullDevice
        try? task.run()
        task.waitUntilExit()

        if task.terminationStatus == 0 {
            logger.info("Killed stale irrlichd process(es)")
            // Give the port a moment to free up
            Thread.sleep(forTimeInterval: 0.5)
        }
    }

    func run(_ process: Process) throws {
        try process.run()
    }
}

/// Records every call and touches nothing: no socket, no `pkill`, no process
/// started. The test-process default for `DaemonManager`.
///
/// `run(_:)` records the process and returns WITHOUT starting it, so
/// `DaemonManager` proceeds as if the launch succeeded (it keeps the
/// never-started `Process`, whose `isRunning` stays false). The probe answers
/// `reachable`, false by default, so `start()` walks the kill-and-spawn path —
/// the one that, live, could kill the developer's daemon.
final class RecordingDaemonProcessControl: DaemonProcessControl, @unchecked Sendable {
    enum Call: Equatable {
        case probe
        case killStaleDaemons
        case run(executable: URL?)
    }

    /// A path that names no file, so nothing could ever be launched from it.
    static let placeholderExecutable = URL(fileURLWithPath: "/nonexistent/irrlichd-recording-double")

    let daemonExecutableURL: URL?
    private let reachable: Bool
    private var recorded: [Call] = []
    private let lock = NSLock()

    init(reachable: Bool = false, daemonExecutableURL: URL? = RecordingDaemonProcessControl.placeholderExecutable) {
        self.reachable = reachable
        self.daemonExecutableURL = daemonExecutableURL
    }

    /// Every call so far, oldest first.
    var calls: [Call] {
        lock.lock()
        defer { lock.unlock() }
        return recorded
    }

    func isDaemonReachable() async -> Bool {
        record(.probe)
        return reachable
    }

    func killStaleDaemons() {
        record(.killStaleDaemons)
    }

    func run(_ process: Process) throws {
        record(.run(executable: process.executableURL))
    }

    /// Synchronous so the lock is never held across a suspension point.
    private func record(_ call: Call) {
        lock.lock()
        defer { lock.unlock() }
        recorded.append(call)
    }
}
