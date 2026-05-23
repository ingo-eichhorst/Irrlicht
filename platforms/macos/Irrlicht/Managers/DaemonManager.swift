import Foundation
import os

/// Manages the lifecycle of the embedded `irrlichd` daemon binary.
///
/// On launch the manager kills any stale daemon processes from previous runs
/// or LaunchAgent, then spawns a fresh copy from the app bundle. If the
/// daemon crashes, it restarts automatically with exponential backoff.
@MainActor
final class DaemonManager: ObservableObject {
    @Published private(set) var daemonRunning = false

    private var process: Process?
    private var healthTask: Task<Void, Never>?
    private var restartCount = 0
    private let maxRestartDelay: TimeInterval = 30

    private let logger = Logger(subsystem: "io.irrlicht.app", category: "DaemonManager")

    // MARK: - Public

    func start() {
        guard healthTask == nil else { return }
        healthTask = Task { [weak self] in
            guard let self else { return }
            if await self.isDaemonReachable() {
                self.daemonRunning = true
                self.logger.info("External daemon already reachable — skipping spawn")
                return
            }
            self.killStaleDaemons()
            self.spawnDaemon()
        }
    }

    func stop() {
        healthTask?.cancel()
        healthTask = nil
        terminateProcess()
    }

    // MARK: - Stale process cleanup

    /// Kill any `irrlichd` processes left over from a previous app launch,
    /// LaunchAgent, or manual invocation so we start with a clean slate.
    private func killStaleDaemons() {
        // On a custom port we're a dev instance coexisting with the production
        // daemon (port 7837). pkill matches by process name and can't target a
        // port, so a global kill here would take production down too — skip it.
        if DaemonEndpoint.isCustomPort {
            logger.info("Custom daemon port \(DaemonEndpoint.port) — skipping global pkill to leave production running")
            return
        }
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/pkill")
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

    // MARK: - Health check

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

    // MARK: - Embedded daemon

    /// Path to the `irrlichd` binary inside the app bundle, with a dev-mode fallback.
    private var bundledDaemonURL: URL? {
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

    private func spawnDaemon() {
        guard let daemonURL = bundledDaemonURL else {
            logger.error("irrlichd binary not found in app bundle")
            return
        }

        let proc = Process()
        proc.executableURL = daemonURL
        proc.standardOutput = FileHandle.nullDevice
        proc.standardError = FileHandle.nullDevice
        // Bind the same port the app connects to. Inherits the app's
        // environment so IRRLICHT_HOME (if set for a dev instance) propagates.
        var env = ProcessInfo.processInfo.environment
        env["IRRLICHT_BIND_ADDR"] = "127.0.0.1:\(DaemonEndpoint.port)"
        proc.environment = env

        proc.terminationHandler = { [weak self] terminated in
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.daemonRunning = false

                // Only restart if we haven't been told to stop.
                guard self.healthTask != nil, !Task.isCancelled else { return }

                let reason = terminated.terminationReason
                let status = terminated.terminationStatus
                self.logger.warning("Daemon exited (reason=\(String(describing: reason)), status=\(status)) — scheduling restart")
                self.scheduleRestart()
            }
        }

        do {
            try proc.run()
            process = proc
            daemonRunning = true
            restartCount = 0
            logger.info("Spawned embedded daemon (pid \(proc.processIdentifier))")
        } catch {
            logger.error("Failed to launch daemon: \(error.localizedDescription)")
            scheduleRestart()
        }
    }

    private func scheduleRestart() {
        restartCount += 1
        let delay = min(pow(2.0, Double(restartCount - 1)), maxRestartDelay)

        logger.info("Restart #\(self.restartCount) in \(delay)s")

        healthTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !Task.isCancelled else { return }
            self.spawnDaemon()
        }
    }

    private func terminateProcess() {
        guard let proc = process, proc.isRunning else {
            process = nil
            return
        }
        proc.terminate() // SIGTERM — daemon handles graceful shutdown
        process = nil
        daemonRunning = false
        logger.info("Terminated embedded daemon")
    }
}
