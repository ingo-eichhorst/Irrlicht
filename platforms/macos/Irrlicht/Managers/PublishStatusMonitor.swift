import Foundation
import SwiftUI

/// Polls the local daemon's `GET /api/v1/relay/publish` endpoint so Settings can
/// show whether this Mac's sessions are actually reaching the relay (issue
/// #718). The forwarder lives inside `irrlichd`, so its true link state — most
/// importantly an auth failure from a bad token — is only observable via this
/// endpoint, not from anything the app holds locally.
@MainActor
final class PublishStatusMonitor: ObservableObject {
    /// UI-facing publish link state. Mirrors the daemon's forwarder states plus
    /// `off` (publishing disabled) and `unknown` (daemon unreachable / response
    /// unparseable, e.g. during the brief relaunch gap).
    enum State: Equatable {
        case off
        case connecting
        case connected
        case authFailed
        case disconnected
        case unknown

        /// Maps the endpoint's `{enabled, state}` fields to a UI state. Pure, so
        /// it is unit-tested without a live daemon.
        static func from(enabled: Bool, state: String?) -> State {
            guard enabled else { return .off }
            switch state {
            case "connected":    return .connected
            case "connecting":   return .connecting
            case "auth_failed":  return .authFailed
            case "disconnected": return .disconnected
            default:             return .unknown
            }
        }

        var dotColor: Color {
            switch self {
            case .connected:              return .green
            case .connecting:             return .yellow
            case .authFailed:             return .red
            case .disconnected, .unknown: return .yellow
            case .off:                    return Color(.tertiaryLabelColor)
            }
        }

        /// Compact one-word label for the "Publishing — …" status line.
        var label: String {
            switch self {
            case .off:          return "off"
            case .connecting:   return "connecting"
            case .connected:    return "connected"
            case .authFailed:   return "token rejected"
            case .disconnected: return "disconnected"
            case .unknown:      return "unknown"
            }
        }
    }

    @Published private(set) var state: State = .off

    private var pollTask: Task<Void, Never>?
    private let pollInterval: TimeInterval = 3

    /// Begins polling (idempotent). Stop with `stop()` when Settings closes.
    func start() {
        guard pollTask == nil else { return }
        let interval = pollInterval
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.poll()
                try? await Task.sleep(nanoseconds: UInt64(interval * 1_000_000_000))
            }
        }
    }

    func stop() {
        pollTask?.cancel()
        pollTask = nil
        state = .off
    }

    private func poll() async {
        guard let url = URL(string: "\(DaemonEndpoint.httpBase)/api/v1/relay/publish") else { return }
        var req = URLRequest(url: url)
        req.timeoutInterval = 2
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            guard (resp as? HTTPURLResponse)?.statusCode == 200 else {
                state = .unknown
                return
            }
            let decoded = try JSONDecoder().decode(PublishStatusResponse.self, from: data)
            state = State.from(enabled: decoded.enabled, state: decoded.state)
        } catch {
            state = .unknown
        }
    }
}

private struct PublishStatusResponse: Decodable {
    let enabled: Bool
    let state: String?
}
