import Foundation

/// Pushes the app's relay-publish settings to the running daemon's loopback
/// `PUT /api/v1/relay/publish` so toggling "Publish to relay" — or editing the
/// URL/token — reconfigures the live forwarder without a daemon relaunch
/// (issue #722). The daemon owns the forwarder's lifecycle; this client only
/// tells it the desired `{enabled, url, token}`, which works whether the app
/// spawned the daemon or adopted an already-running one.
enum PublishClient {
    static let path = "/api/v1/relay/publish"  // NOSONAR (swift:S1075) — fixed route on the daemon's own loopback API, not an external URI

    /// Wire shape of the PUT body. The relay token travels in this loopback
    /// body rather than a spawn-time env var (issue #722); the daemon binds
    /// 127.0.0.1 only, so this is the same trust boundary as every other
    /// daemon endpoint.
    struct Config: Codable, Equatable {
        let enabled: Bool
        let url: String
        let token: String
    }

    /// Builds the PUT request. Internal (not private) so tests can assert the
    /// wire shape without a live daemon. Returns nil only if the endpoint URL
    /// can't be formed.
    static func makeRequest(enabled: Bool, url: String, token: String) -> URLRequest? {
        guard let endpoint = URL(string: "\(DaemonEndpoint.httpBase)\(path)") else { return nil }
        var req = URLRequest(url: endpoint, timeoutInterval: 2)
        req.httpMethod = "PUT"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONEncoder().encode(Config(enabled: enabled, url: url, token: token))
        return req
    }

    /// Send the desired publish config to the daemon. Best-effort: a failure
    /// (daemon not yet reachable) is swallowed — the caller re-syncs once the
    /// daemon answers, and the Settings poll reflects the true state regardless.
    /// Returns true on a 200.
    @discardableResult
    static func apply(enabled: Bool, url: String, token: String) async -> Bool {
        guard let req = makeRequest(enabled: enabled, url: url, token: token) else { return false }
        do {
            let (_, resp) = try await URLSession.shared.data(for: req)
            return (resp as? HTTPURLResponse)?.statusCode == 200
        } catch {
            return false
        }
    }
}

/// Something that can deliver a publish config to a daemon. `DaemonManager`
/// takes one so a test process never reaches `PublishClient.apply`'s live
/// loopback endpoint (see `DaemonManager.publisher`).
protocol RelayPublishing: Sendable {
    /// Delivers `config`. True when the daemon accepted it.
    func publish(_ config: PublishClient.Config) async -> Bool
}

/// The app's publisher: `PublishClient.apply`, i.e. a PUT to
/// `DaemonEndpoint.httpBase` + `PublishClient.path`.
struct LiveRelayPublisher: RelayPublishing {
    func publish(_ config: PublishClient.Config) async -> Bool {
        await PublishClient.apply(enabled: config.enabled, url: config.url, token: config.token)
    }
}

/// Records every config it is handed and reports success, sending nothing.
/// The test-process default for `DaemonManager`, and what a test reads to
/// assert on what would have been published.
final class RecordingRelayPublisher: RelayPublishing, @unchecked Sendable {
    private var configs: [PublishClient.Config] = []
    private let lock = NSLock()

    /// Every config published so far, oldest first.
    var published: [PublishClient.Config] {
        lock.lock()
        defer { lock.unlock() }
        return configs
    }

    func publish(_ config: PublishClient.Config) async -> Bool {
        record(config)
        return true
    }

    /// Synchronous so the lock is never held across a suspension point.
    private func record(_ config: PublishClient.Config) {
        lock.lock()
        defer { lock.unlock() }
        configs.append(config)
    }
}
