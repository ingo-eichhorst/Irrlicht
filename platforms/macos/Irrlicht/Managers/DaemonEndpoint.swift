import Foundation

/// Centralizes where the dashboard talks to `irrlichd`.
///
/// The port defaults to 7837 but can be overridden with the
/// `IRRLICHT_DAEMON_PORT` environment variable. This lets a dev build connect
/// to an isolated daemon (e.g. port 7838, started with its own `IRRLICHT_HOME`)
/// while the production app keeps talking to 7837 — so `/ir:test-mac` can run a
/// dev UI alongside production without taking it down. The matching daemon-side
/// knobs are `IRRLICHT_BIND_ADDR` (port) and `IRRLICHT_HOME` (state dir).
enum DaemonEndpoint {
    /// The port the production daemon binds by default.
    static let defaultPort = 7837

    /// The resolved daemon port: `IRRLICHT_DAEMON_PORT` if set to a positive
    /// integer, otherwise the default. Whitespace is trimmed first — Swift's
    /// `Int("7838\n")` returns nil, and silently falling back to 7837 would
    /// flip `isCustomPort` to false and re-enable `DaemonManager`'s global
    /// `pkill`, which could take the production daemon down.
    static let port: Int = {
        guard let raw = ProcessInfo.processInfo.environment["IRRLICHT_DAEMON_PORT"]?
            .trimmingCharacters(in: .whitespacesAndNewlines), !raw.isEmpty
        else {
            return defaultPort
        }
        guard let p = Int(raw), p > 0 else {
            FileHandle.standardError.write(Data(
                "irrlicht: IRRLICHT_DAEMON_PORT=\"\(raw)\" is not a valid port; using default \(defaultPort)\n".utf8))
            return defaultPort
        }
        return p
    }()

    /// True when an explicit non-default port was requested — i.e. we're a dev
    /// instance coexisting with production and must not disturb it.
    static var isCustomPort: Bool { port != defaultPort }

    /// Base URL for HTTP requests, e.g. `http://127.0.0.1:7837`.
    static var httpBase: String { "http://127.0.0.1:\(port)" }

    /// Base URL for WebSocket requests, e.g. `ws://127.0.0.1:7837`.
    static var wsBase: String { "ws://127.0.0.1:\(port)" }
}
