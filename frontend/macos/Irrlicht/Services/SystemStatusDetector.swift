import Foundation

// MARK: - Status Enums

/// Whether the `claude` CLI binary is available on the system.
enum ClaudeCodeStatus: Equatable {
    case notInstalled
    case installed
}

/// Whether `irrlicht-hook` is installed and wired into Claude Code's settings.json.
enum HookStatus: Equatable {
    /// `irrlicht-hook` binary not found in PATH.
    case binaryMissing
    /// Binary present but no irrlicht-hook entries in settings.json.
    case notConfigured
    /// Binary present and some — but not all — required events are wired.
    case partiallyConfigured(missing: [String])
    /// Binary present and all required events are wired.
    case fullyConfigured
}

// MARK: - SystemStatusDetector

/// Detects installation status of Claude Code and the irrlicht-hook integration.
///
/// Designed for testability: the detection logic is parameterised so callers can
/// inject fakes for the PATH probe and settings.json content in unit tests.
struct SystemStatusDetector {

    // The canonical set of hook events that irrlicht-hook must be registered for.
    static let requiredEvents: [String] = [
        "SessionStart",
        "UserPromptSubmit",
        "PreToolUse",
        "PostToolUse",
        "PreCompact",
        "Notification",
        "Stop",
        "SubagentStop",
        "SessionEnd",
    ]

    // MARK: - Public API

    /// Returns whether the `claude` binary is reachable on PATH.
    static func detectClaudeCodeStatus() -> ClaudeCodeStatus {
        isBinaryInPath("claude") ? .installed : .notInstalled
    }

    /// Returns the hook-configuration status.
    ///
    /// - If `irrlicht-hook` is absent from PATH → `.binaryMissing`
    /// - If settings.json is missing or unparseable → `.notConfigured`
    /// - If some required events are unwired → `.partiallyConfigured(missing:)`
    /// - If every required event is wired → `.fullyConfigured`
    static func detectHookStatus() -> HookStatus {
        let settingsURL = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".claude")
            .appendingPathComponent("settings.json")
        let settingsData = try? Data(contentsOf: settingsURL)
        return detectHookStatus(irrlichtHookInPath: isBinaryInPath("irrlicht-hook"),
                                settingsData: settingsData)
    }

    // MARK: - Testable core

    /// Parameterised variant used by unit tests.
    ///
    /// - Parameters:
    ///   - irrlichtHookInPath: whether `irrlicht-hook` was found in PATH.
    ///   - settingsData: raw bytes of `~/.claude/settings.json`, or `nil` if absent.
    static func detectHookStatus(irrlichtHookInPath: Bool, settingsData: Data?) -> HookStatus {
        guard irrlichtHookInPath else {
            return .binaryMissing
        }

        guard
            let data = settingsData,
            let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
            let hooks = json["hooks"] as? [String: Any]
        else {
            return .notConfigured
        }

        let missing = requiredEvents.filter { event in
            !isIrrlichtHookConfigured(in: hooks, for: event)
        }

        switch missing.count {
        case 0:
            return .fullyConfigured
        case requiredEvents.count:
            return .notConfigured
        default:
            return .partiallyConfigured(missing: missing)
        }
    }

    // MARK: - Private helpers

    /// Returns `true` when `which <name>` exits 0.
    private static func isBinaryInPath(_ name: String) -> Bool {
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        task.arguments = [name]
        task.standardOutput = FileHandle.nullDevice
        task.standardError = FileHandle.nullDevice
        do {
            try task.run()
            task.waitUntilExit()
            return task.terminationStatus == 0
        } catch {
            return false
        }
    }

    /// Returns `true` when `irrlicht-hook` appears as a command in the hooks
    /// array for the given event name.
    ///
    /// The settings.json schema for a single event looks like:
    /// ```json
    /// "SessionStart": [
    ///   { "hooks": [{ "type": "command", "command": "irrlicht-hook" }] }
    /// ]
    /// ```
    private static func isIrrlichtHookConfigured(in hooks: [String: Any], for event: String) -> Bool {
        guard let eventConfigs = hooks[event] as? [[String: Any]] else {
            return false
        }
        for config in eventConfigs {
            guard let hookList = config["hooks"] as? [[String: Any]] else { continue }
            for hook in hookList {
                if let command = hook["command"] as? String, command == "irrlicht-hook" {
                    return true
                }
            }
        }
        return false
    }
}
