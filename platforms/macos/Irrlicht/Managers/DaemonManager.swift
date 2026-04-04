import Foundation
import os

/// Manages the lifecycle of the embedded `irrlichd` daemon binary.
///
/// On launch the manager checks whether a daemon is already reachable (e.g.
/// started via LaunchAgent). If not, it spawns the copy bundled inside the
/// app and restarts it automatically if it crashes.
@MainActor
final class DaemonManager: ObservableObject {
    @Published private(set) var daemonRunning = false

    private var process: Process?
    private var healthTask: Task<Void, Never>?
    private var restartCount = 0
    private let maxRestartDelay: TimeInterval = 30
    private let daemonPort = 7837

    private let logger = Logger(subsystem: "com.anthropic.irrlicht", category: "DaemonManager")

    // MARK: - Public

    func start() {
        healthTask = Task { [weak self] in
            guard let self else { return }
            if await self.isDaemonReachable() {
                self.logger.info("External daemon already running — skipping embedded launch")
                self.daemonRunning = true
                await self.monitorExternalDaemon()
            } else {
                self.spawnDaemon()
            }
        }
    }

    func stop() {
        healthTask?.cancel()
        healthTask = nil
        terminateProcess()
    }

    // MARK: - Health check

    /// Returns true if an irrlichd instance is already listening on the expected port.
    func isDaemonReachable() async -> Bool {
        guard let url = URL(string: "http://127.0.0.1:\(daemonPort)/state") else { return false }
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

    /// Path to the `irrlichd` binary inside the app bundle.
    private var bundledDaemonURL: URL? {
        Bundle.main.url(forAuxiliaryExecutable: "irrlichd")
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

    /// When an external daemon was detected at startup, keep watching so the UI
    /// indicator stays accurate if the external daemon goes away.
    private func monitorExternalDaemon() async {
        while !Task.isCancelled {
            try? await Task.sleep(nanoseconds: 5_000_000_000) // 5 s
            guard !Task.isCancelled else { return }
            let reachable = await isDaemonReachable()
            if reachable {
                daemonRunning = true
            } else {
                logger.warning("External daemon went away — spawning embedded daemon")
                daemonRunning = false
                spawnDaemon()
                return
            }
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
