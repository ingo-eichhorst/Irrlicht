import Foundation

/// Derives Gas Town availability from session group types.
/// Updated by SessionManager when it hydrates sessions from the REST API.
@MainActor
class GasTownProvider: ObservableObject {
    @Published var isAvailable: Bool = false

    /// Called by SessionManager after hydration to indicate whether any
    /// groups have type == "gastown".
    func updateAvailability(_ available: Bool) {
        isAvailable = available
    }

    var isDaemonRunning: Bool { isAvailable }

    /// Whether a session belongs to Gas Town (has a role assigned by the backend).
    func ownsSession(_ session: SessionState) -> Bool {
        if let role = session.role, !role.isEmpty {
            return true
        }
        return false
    }

    /// Count of Gas Town agents from live sessions.
    func agentCount(from sessions: [SessionState]) -> Int {
        sessions.filter { ownsSession($0) }.count
    }
}
