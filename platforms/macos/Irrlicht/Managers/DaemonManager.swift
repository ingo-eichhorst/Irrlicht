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
    /// Bounded "PUT publish config once the daemon answers" task (issue #722),
    /// tracked so it can be cancelled on stop and never overlaps itself.
    private var publishSyncTask: Task<Void, Never>?
    private var restartCount = 0
    private let maxRestartDelay: TimeInterval = 30

    private let logger = Logger(subsystem: "io.irrlicht.app", category: "DaemonManager")

    /// Test-only view of the app-owned daemon process (nil when none), so tests
    /// can assert the POST-based publish reconfigure never spawns or relaunches
    /// a daemon (issue #722). Internal: visible only via `@testable import`.
    var currentProcessForTesting: Process? { process }

    // MARK: - Relay publish settings

    /// Relay-publish settings (issue #718). URL + enabled flag live in
    /// UserDefaults (shared with the relay *subscribe* direction); the token
    /// lives in the Keychain — same store the subscribe path uses.
    private var publishEnabled: Bool { UserDefaults.standard.bool(forKey: "publishToRelay") }
    private var relayURL: String {
        (UserDefaults.standard.string(forKey: "relayServerURL") ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Builds the daemon's launch environment from a base (the app's own
    /// environment). Only `IRRLICHT_BIND_ADDR` is layered on, so the daemon
    /// listens on the port the app connects to. Relay publishing is no longer
    /// passed as launch env — the app drives it live over the daemon's loopback
    /// `PUT /api/v1/relay/publish` instead (issue #722).
    static func buildDaemonEnv(base: [String: String], bindAddr: String) -> [String: String] {
        var env = base
        env["IRRLICHT_BIND_ADDR"] = bindAddr
        return env
    }

    /// Called by Settings when a publish-relevant setting changes (the toggle,
    /// the relay URL, or the Keychain token — the last fires no UserDefaults
    /// notification, so Settings nudges us explicitly). POSTs the new config to
    /// the running daemon so it reconfigures its forwarder live — no relaunch,
    /// and it works whether this app spawned the daemon or adopted an
    /// already-running one (issue #722).
    func publishSettingsDidChange() {
        pushPublishConfig()
    }

    /// PUT the current publish settings to the daemon now (fire-and-forget).
    /// Used when the daemon is already known to be reachable (the adopt path and
    /// settings nudges). The daemon's Apply is idempotent, so re-POSTing the
    /// config already in effect is a cheap no-op.
    ///
    /// The UserDefaults flags are read on the main actor (cheap), but the
    /// Keychain token read and the network call run on a detached task: Keychain
    /// access can be slow, and blocking the main actor on it would stutter the
    /// UI (and stall anything awaiting the run loop).
    private func pushPublishConfig() {
        let enabled = publishEnabled
        let url = relayURL
        Task.detached {
            let token = KeychainStore.get(account: "relayToken")
                .trimmingCharacters(in: .whitespacesAndNewlines)
            await PublishClient.apply(enabled: enabled, url: url, token: token)
        }
    }

    /// After a spawn/restart, wait (bounded) for the daemon to answer, then PUT
    /// the current publish settings. A freshly-spawned daemon starts with
    /// publishing off (the app no longer passes the relay env, issue #722), so
    /// this brings it in line with the app's settings without a manual toggle.
    private func syncPublishWhenReachable() {
        publishSyncTask?.cancel()
        publishSyncTask = Task { [weak self] in
            for _ in 0..<25 { // up to ~5s (25 × 200ms)
                guard let self, !Task.isCancelled else { return }
                if await self.isDaemonReachable() {
                    self.pushPublishConfig()
                    return
                }
                try? await Task.sleep(nanoseconds: 200_000_000)
            }
        }
    }

    // MARK: - Public

    func start() {
        guard healthTask == nil else { return }
        healthTask = Task { [weak self] in
            guard let self else { return }
            if await self.isDaemonReachable() {
                self.daemonRunning = true
                self.logger.info("External daemon already reachable — skipping spawn")
                // Adopt path: bring the already-running daemon in line with our
                // publish settings (it may have been started without them).
                self.pushPublishConfig()
                return
            }
            self.killStaleDaemons()
            self.spawnDaemon()
        }
    }

    func stop() {
        healthTask?.cancel()
        healthTask = nil
        publishSyncTask?.cancel()
        publishSyncTask = nil
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
        // Relay publishing is applied live over loopback after launch, not via
        // env (issue #722) — see syncPublishWhenReachable below.
        proc.environment = Self.buildDaemonEnv(
            base: ProcessInfo.processInfo.environment,
            bindAddr: "127.0.0.1:\(DaemonEndpoint.port)"
        )

        proc.terminationHandler = { [weak self] terminated in
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.daemonRunning = false

                // An intentional stop or relaunch clears or replaces `process`
                // before the handler runs, so treat this as a crash worth
                // restarting only when the terminated process is still current.
                guard self.process === terminated else { return }
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
            // The spawned daemon starts with publishing off; once it answers,
            // POST the app's publish settings so it matches without a relaunch.
            syncPublishWhenReachable()
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
