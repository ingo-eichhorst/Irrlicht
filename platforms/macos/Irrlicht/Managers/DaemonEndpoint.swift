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
    /// integer, otherwise the default.
    static let port: Int = {
        if let v = ProcessInfo.processInfo.environment["IRRLICHT_DAEMON_PORT"],
           let p = Int(v), p > 0 {
            return p
        }
        return defaultPort
    }()

    /// True when an explicit non-default port was requested — i.e. we're a dev
    /// instance coexisting with production and must not disturb it.
    static var isCustomPort: Bool { port != defaultPort }

    /// Base URL for HTTP requests, e.g. `http://127.0.0.1:7837`.
    static var httpBase: String { "http://127.0.0.1:\(port)" }

    /// Base URL for WebSocket requests, e.g. `ws://127.0.0.1:7837`.
    static var wsBase: String { "ws://127.0.0.1:\(port)" }
}
