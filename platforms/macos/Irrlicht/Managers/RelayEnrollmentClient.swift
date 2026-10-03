import Foundation

/// Redeems a relay enrollment URL (`https://<origin>/enroll/XXXX-XXXX`,
/// minted by `irrlichtrelay enroll new`, #1963) for an ordinary relay token,
/// so a desktop joins a relay by pasting one URL instead of a URL and a
/// bearer token (#1965). Shaped like `ElfdansPairingClient`: pure parse,
/// request and decode steps plus one thin async call.
enum RelayEnrollmentClient {
    static let redeemPath = "/api/v1/enroll/redeem"  // NOSONAR (swift:S1075) — fixed relay API path

    /// Mirrors the relay's `onetimecode.Alphabet`: the ambiguous I/L/O/U/0/1
    /// are dropped (core/pkg/onetimecode/onetimecode.go).
    static let codeAlphabet = Set("ABCDEFGHJKMNPQRSTVWXYZ23456789")

    enum Failure: LocalizedError, Equatable {
        /// 401. The relay answers unknown, expired and already-used codes
        /// identically (`enrollCodeInvalidMsg`), so this must not guess which.
        case codeRefused
        /// 403. The relay runs with `--auth off` and has no token store.
        case authOff
        /// 429. Too many failed redemptions in the relay's window.
        case rateLimited
        case invalidResponse
        case keychainWriteFailed
        /// The request never got an HTTP answer; carries the host.
        case unreachable(String)
        /// Any other status, with the relay's own `error` text when it sent one.
        case rejected(Int, String?)

        var errorDescription: String? {
            switch self {
            case .codeRefused:
                return "That enrollment code is invalid, expired or already used. Mint a fresh one with `irrlichtrelay enroll new`."
            case .authOff:
                return "This relay runs with --auth off and cannot enroll desktops. Start it with --auth tokens-file."
            case .rateLimited:
                return "Too many failed enrollment attempts. Wait a minute, then try again."
            case .invalidResponse:
                return "The relay returned an invalid enrollment response."
            case .keychainWriteFailed:
                return "Joined the relay, but the token could not be saved to the Keychain. Mint a fresh code and try again."
            case .unreachable(let host):
                return "Could not reach \(host). Check the address and your network."
            case .rejected(let status, let message?):
                return "The relay refused the enrollment (HTTP \(status)): \(message)"
            case .rejected(let status, nil):
                return "The relay refused the enrollment (HTTP \(status))."
            }
        }
    }

    /// Splits an enrollment URL into the relay origin and the presented code.
    /// The relay prints `<https origin>/enroll/<code>` when it has a
    /// `--public-url` (validated as an HTTPS origin without a path), and
    /// otherwise only the bare code, saying it "still works when typed or
    /// pasted into the desktop app by hand" (core/cmd/irrlichtrelay/
    /// pairing_handoff.go). A bare code is therefore sent to the relay already
    /// configured in `configuredRelayURL`, but only when that is `https`/`wss`.
    /// Anything else is refused here, before any network call.
    static func parse(_ raw: String, configuredRelayURL: String = "") -> (origin: URL, code: String)? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if isPresentedCode(trimmed.uppercased()) {
            guard let origin = secureOrigin(configuredRelayURL, schemes: ["https", "wss"]) else { return nil }
            return (origin, trimmed.uppercased())
        }
        guard let parts = URLComponents(string: trimmed),
              let origin = secureOrigin(trimmed, schemes: ["https"]) else { return nil }

        var path = parts.path
        if path.hasSuffix("/") { path.removeLast() }
        let prefix = "/enroll/"
        guard path.hasPrefix(prefix) else { return nil }
        let code = path.dropFirst(prefix.count).uppercased()
        guard isPresentedCode(code) else { return nil }
        return (origin, code)
    }

    /// The `https://host[:port]` origin of `raw`, or nil unless its scheme is
    /// one of `schemes` and it carries a host and no credentials, query or
    /// fragment. The path is ignored; the callers check it themselves.
    private static func secureOrigin(_ raw: String, schemes: Set<String>) -> URL? {
        guard let parts = URLComponents(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = parts.scheme?.lowercased(), schemes.contains(scheme),
              let host = parts.host, !host.isEmpty,
              parts.user == nil, parts.password == nil,
              parts.query == nil, parts.fragment == nil else { return nil }
        var origin = URLComponents()
        origin.scheme = "https"
        origin.host = host.lowercased()
        origin.port = parts.port
        return origin.url
    }

    /// The relay's `IsPresentedCode`: four alphabet characters, a dash, four more.
    static func isPresentedCode(_ code: String) -> Bool {
        let chars = Array(code)
        guard chars.count == 9, chars[4] == "-" else { return false }
        return chars.enumerated().allSatisfy { $0.offset == 4 || codeAlphabet.contains($0.element) }
    }

    /// The redeem route carries no bearer: the code is the credential.
    static func makeRequest(origin: URL, code: String, label: String) -> URLRequest {
        var request = URLRequest(url: origin.appendingPathComponent(String(redeemPath.dropFirst())),
                                 timeoutInterval: 8)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONEncoder().encode(["code": code, "label": label])
        return request
    }

    /// Returns the issued token, or the `Failure` naming why there is none.
    static func decode(_ data: Data, status: Int) throws -> String {
        struct Redeemed: Decodable { let token: String }
        struct RelayError: Decodable { let error: String }
        switch status {
        case 200:
            guard data.count <= 64_000,
                  let token = try? JSONDecoder().decode(Redeemed.self, from: data).token,
                  !token.isEmpty else { throw Failure.invalidResponse }
            return token
        case 401: throw Failure.codeRefused
        case 403: throw Failure.authOff
        case 429: throw Failure.rateLimited
        default:
            let message = data.count <= 64_000
                ? (try? JSONDecoder().decode(RelayError.self, from: data))?.error
                : nil
            throw Failure.rejected(status, message?.isEmpty == false ? message : nil)
        }
    }

    /// Stores the redeemed credential where the manual Sources fields keep
    /// theirs: the token under the `relayToken` Keychain account, the origin
    /// as `relayServerURL` (the app's `relayStreamURL` and the daemon's
    /// `normalizeRelayURL` both rewrite `https://` to the `wss://` stream
    /// URL), and publishing switched on. A refused Keychain write throws
    /// before any setting changes, so publishing never starts without a token.
    /// The caller then nudges the running subscribe link and daemon, exactly
    /// as the token field does.
    static func apply(token: String, origin: URL, defaults: UserDefaults = .standard,
                      setToken: (String, String) -> Bool = { KeychainStore.set($0, account: $1) }) throws {
        guard setToken(token, "relayToken") else { throw Failure.keychainWriteFailed }
        defaults.set(origin.absoluteString, forKey: "relayServerURL")
        defaults.set(true, forKey: "publishToRelay")
    }

    static func redeem(origin: URL, code: String, label: String) async throws -> String {
        let request = makeRequest(origin: origin, code: code, label: label)
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await URLSession.shared.data(for: request)
        } catch is URLError {
            throw Failure.unreachable(origin.host ?? origin.absoluteString)
        }
        guard let http = response as? HTTPURLResponse else { throw Failure.invalidResponse }
        return try decode(data, status: http.statusCode)
    }
}
