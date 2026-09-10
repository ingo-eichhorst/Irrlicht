import AppKit
import Foundation

struct ElfdansPairing: Decodable, Equatable {
    let code: String
    let expiresIn: Int
    let pairingURL: String?
    let pairingQR: String?
    let pairingURLReason: String?

    enum CodingKeys: String, CodingKey {
        case code
        case expiresIn = "expires_in"
        case pairingURL = "pairing_url"
        case pairingQR = "pairing_qr"
        case pairingURLReason = "pairing_url_reason"
    }

    var qrImage: NSImage? {
        guard let encoded = pairingQR,
              encoded.hasPrefix("data:image/png;base64,") else { return nil }
        let payload = String(encoded.dropFirst("data:image/png;base64,".count))
        guard payload.count <= 1_500_000,
              let data = Data(base64Encoded: payload) else { return nil }
        return NSImage(data: data)
    }
}

enum ElfdansPairingClient {
    static let path = "/api/v1/push/pairings"  // NOSONAR (swift:S1075) — fixed relay API path

    enum Failure: LocalizedError {
        case invalidRelayURL
        case rejected(Int)
        case invalidResponse

        var errorDescription: String? {
            switch self {
            case .invalidRelayURL:
                return "Enter a valid relay URL first."
            case .rejected(let status):
                return "The relay could not create a pairing code (HTTP \(status))."
            case .invalidResponse:
                return "The relay returned an invalid pairing response."
            }
        }
    }

    static func endpointURL(_ raw: String) -> URL? {
        var value = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return nil }
        if !value.contains("://") { value = "ws://" + value }
        guard var parts = URLComponents(string: value),
              parts.user == nil, parts.password == nil,
              parts.query == nil, parts.fragment == nil else { return nil }
        switch parts.scheme?.lowercased() {
        case "ws": parts.scheme = "http"
        case "wss": parts.scheme = "https"
        case "http", "https": break
        default: return nil
        }
        let streamPath = "/api/v1/sessions/stream"
        if parts.path.hasSuffix(streamPath) {
            parts.path.removeLast(streamPath.count)
        }
        while parts.path.hasSuffix("/") { parts.path.removeLast() }
        parts.path += path
        return parts.url
    }

    static func makeRequest(relayURL: String, token: String) -> URLRequest? {
        guard !token.isEmpty, let endpoint = endpointURL(relayURL) else { return nil }
        var request = URLRequest(url: endpoint, timeoutInterval: 8)
        request.httpMethod = "POST"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        return request
    }

    static func decode(_ data: Data, status: Int) throws -> ElfdansPairing {
        guard status == 201 else { throw Failure.rejected(status) }
        guard data.count <= 2_000_000,
              let pairing = try? JSONDecoder().decode(ElfdansPairing.self, from: data),
              !pairing.code.isEmpty else { throw Failure.invalidResponse }
        return pairing
    }

    static func mint(relayURL: String, token: String) async throws -> ElfdansPairing {
        guard let request = makeRequest(relayURL: relayURL, token: token) else {
            throw Failure.invalidRelayURL
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw Failure.invalidResponse }
        return try decode(data, status: http.statusCode)
    }
}
