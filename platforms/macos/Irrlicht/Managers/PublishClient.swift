import Foundation

/// Pushes the app's relay-publish settings to the running daemon's loopback
/// `PUT /api/v1/relay/publish` so toggling "Publish to relay" — or editing the
/// URL/token — reconfigures the live forwarder without a daemon relaunch
/// (issue #722). The daemon owns the forwarder's lifecycle; this client only
/// tells it the desired `{enabled, url, token}`, which works whether the app
/// spawned the daemon or adopted an already-running one.
enum PublishClient {
    static let path = "/api/v1/relay/publish"

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
