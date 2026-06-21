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

    /// Last publish config the running daemon was launched with, so a settings
    /// nudge relaunches only when the effective config actually changed.
    private var lastPublishConfig = ""

    // MARK: - Relay publish settings

    /// Relay-publish settings (issue #718). URL + enabled flag live in
    /// UserDefaults (shared with the relay *subscribe* direction); the token
    /// lives in the Keychain — same store the subscribe path uses.
    private var publishEnabled: Bool { UserDefaults.standard.bool(forKey: "publishToRelay") }
    // Trimmed at the source so the change-detection signature and the launched
    // env derive from identical values (buildDaemonEnv trims again defensively
    // for its standalone callers/tests).
    private var relayURL: String {
        (UserDefaults.standard.string(forKey: "relayServerURL") ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
    private var relayToken: String {
        KeychainStore.get(account: "relayToken").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Signature of the publish-relevant settings. The token is reduced to a
    /// hash so the plaintext secret isn't retained, while a token change still
    /// shows up as a different signature.
    private var publishConfigSignature: String {
        let token = relayToken
        return "\(publishEnabled)|\(relayURL)|\(token.isEmpty ? "none" : String(token.hashValue))"
    }

    /// Builds the daemon's launch environment from a base (the app's own
    /// environment) plus the relay-publish settings. When publishing is on with
    /// a non-empty URL it sets `IRRLICHT_RELAY_URL` (which activates the daemon's
    /// outbound forwarder) and, when a token is present, `IRRLICHT_RELAY_TOKEN`.
    /// When publishing is off it strips both, so a value inherited from the
    /// app's own environment can't silently keep forwarding on. `bindAddr` is
    /// always set so the daemon listens on the port the app connects to.
    static func buildDaemonEnv(
        base: [String: String],
        bindAddr: String,
        publishEnabled: Bool,
        relayURL: String,
        relayToken: String
    ) -> [String: String] {
        var env = base
        env["IRRLICHT_BIND_ADDR"] = bindAddr
        let url = relayURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let token = relayToken.trimmingCharacters(in: .whitespacesAndNewlines)
        if publishEnabled && !url.isEmpty {
            env["IRRLICHT_RELAY_URL"] = url
            if token.isEmpty {
                env.removeValue(forKey: "IRRLICHT_RELAY_TOKEN")
            } else {
                env["IRRLICHT_RELAY_TOKEN"] = token
            }
        } else {
            env.removeValue(forKey: "IRRLICHT_RELAY_URL")
            env.removeValue(forKey: "IRRLICHT_RELAY_TOKEN")
        }
        return env
    }

    /// Called by Settings when a publish-relevant setting changes (the toggle,
    /// the relay URL, or the Keychain token — the last fires no UserDefaults
    /// notification, so Settings nudges us explicitly). Relaunches the embedded
    /// daemon so its startup-wired forwarder picks up the new env, but only when
    /// the effective config actually changed.
    func publishSettingsDidChange() {
        let sig = publishConfigSignature
        guard sig != lastPublishConfig else { return }
        lastPublishConfig = sig
        relaunch()
    }

    /// Relaunches the embedded daemon to apply changed launch settings. Only a
    /// daemon this app spawned is relaunched; an external or custom-port dev
    /// daemon the app didn't start is left untouched (its env is owned wherever
    /// it was launched). The crash-restart path is not triggered:
    /// terminateProcess() clears `process`, and the terminationHandler restarts
    /// only when the terminated process is still the current one.
    private func relaunch() {
        guard process != nil else {
            logger.info("Publish settings changed but no app-owned daemon to relaunch")
            return
        }
        logger.info("Relaunching embedded daemon to apply relay-publish settings")
        terminateProcess()
        healthTask?.cancel()
        healthTask = Task { [weak self] in
            // Brief grace so the listening socket frees before the new daemon binds.
            try? await Task.sleep(nanoseconds: 700_000_000)
            guard let self, !Task.isCancelled else { return }
            self.restartCount = 0
            self.spawnDaemon()
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
        // environment so IRRLICHT_HOME (if set for a dev instance) propagates,
        // then layers on the relay-publish env from Settings (issue #718).
        proc.environment = Self.buildDaemonEnv(
            base: ProcessInfo.processInfo.environment,
            bindAddr: "127.0.0.1:\(DaemonEndpoint.port)",
            publishEnabled: publishEnabled,
            relayURL: relayURL,
            relayToken: relayToken
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
            // Record the publish config this daemon launched with, so a later
            // settings nudge relaunches only on an actual change.
            lastPublishConfig = publishConfigSignature
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
