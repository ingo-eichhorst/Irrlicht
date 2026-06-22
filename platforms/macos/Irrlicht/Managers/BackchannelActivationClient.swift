import Foundation

/// Thin client for the daemon's backchannel master-toggle (issue #724). The
/// daemon owns the toggle state; this client only flips and mirrors it, so the
/// Settings toggle never claims control is on when it isn't. Same nil-means-
/// unreachable contract as ActivationClient: nil must NOT be treated as "off".
enum BackchannelActivationClient {
    private static var endpoint: URL? {
        URL(string: "\(DaemonEndpoint.httpBase)/api/v1/activation/backchannel")
    }

    private struct State: Codable {
        let backchannelEnabled: Bool
        enum CodingKeys: String, CodingKey { case backchannelEnabled = "backchannel_enabled" }
    }

    /// Current toggle state, or nil when the daemon is unreachable.
    static func status() async -> Bool? {
        guard let url = endpoint else { return nil }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = "GET"
        return await send(req)
    }

    /// Flip the toggle (POST = enable, DELETE = disable). Returns the daemon's
    /// resulting state, or nil when unreachable.
    static func set(enabled: Bool) async -> Bool? {
        guard let url = endpoint else { return nil }
        var req = URLRequest(url: url, timeoutInterval: 2)
        req.httpMethod = enabled ? "POST" : "DELETE"
        return await send(req)
    }

    private static func send(_ req: URLRequest) async -> Bool? {
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else { return nil }
            return try JSONDecoder().decode(State.self, from: data).backchannelEnabled
        } catch {
            return nil
        }
    }
}
