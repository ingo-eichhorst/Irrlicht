import Foundation
import Combine

/// Provides Gas Town state via WebSocket push (primary) with REST polling fallback.
/// Subscribes to the same WebSocket stream as SessionManager and handles
/// `gastown_state` messages alongside session messages.
@MainActor
class GasTownProvider: ObservableObject {
    @Published var state: GasTownState?
    @Published var isAvailable: Bool = false

    // REST polling fallback
    private var pollTimer: Timer?
    private let pollInterval: TimeInterval = 5.0
    private let endpoint = URL(string: "http://localhost:7837/api/v1/gastown")!

    init() {
        Task { await fetchOnce() }
        startPolling()
    }

    deinit {
        pollTimer?.invalidate()
    }

    private func startPolling() {
        pollTimer = Timer.scheduledTimer(withTimeInterval: pollInterval, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                await self?.fetchOnce()
            }
        }
    }

    private func fetchOnce() async {
        do {
            let (data, response) = try await URLSession.shared.data(from: endpoint)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else {
                isAvailable = false
                return
            }
            let decoded = try JSONDecoder().decode(GasTownState.self, from: data)
            state = decoded
            isAvailable = decoded.running
        } catch {
            // Daemon not running or endpoint not available yet — silent.
            isAvailable = false
        }
    }

    /// Called by SessionManager when a gastown_state WebSocket message arrives.
    func handleWebSocketUpdate(_ newState: GasTownState) {
        state = newState
        isAvailable = newState.running
    }

    // MARK: - Convenience accessors

    var isDaemonRunning: Bool { state?.running ?? false }

    var globalAgents: [GlobalAgent] { state?.safeGlobalAgents ?? [] }
    var codebases: [GasTownCodebase] { state?.safeCodebases ?? [] }
    var workUnits: [WorkUnit] { state?.safeWorkUnits ?? [] }
    var convoys: [WorkUnit] { state?.convoys ?? [] }
    var activeConvoys: [WorkUnit] { state?.activeConvoys ?? [] }
    var completedConvoys: [WorkUnit] { state?.completedConvoys ?? [] }
    var activeRigCount: Int { state?.activeRigCount ?? 0 }

    /// Mayor agent (if present).
    var mayor: GlobalAgent? { globalAgents.first { $0.isMayor } }

    /// Deacon agent (if present).
    var deacon: GlobalAgent? { globalAgents.first { $0.isDeacon } }
}
