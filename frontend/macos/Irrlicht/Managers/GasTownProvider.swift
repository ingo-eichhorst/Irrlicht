import Foundation
import Combine

/// Polls the irrlichtd /api/v1/gastown endpoint for Gas Town state.
@MainActor
class GasTownProvider: ObservableObject {
    @Published var snapshot: GasTownSnapshot?
    @Published var isAvailable: Bool = false

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
            let snap = try JSONDecoder().decode(GasTownSnapshot.self, from: data)
            snapshot = snap
            isAvailable = snap.detected
        } catch {
            // Daemon not running or endpoint not available yet — silent.
            isAvailable = false
        }
    }

    // MARK: - Convenience accessors

    var rigs: [RigState] { snapshot?.rigs ?? [] }
    var polecats: [PolecatState] { snapshot?.polecats ?? [] }
    var isDaemonRunning: Bool { snapshot?.daemon?.running ?? false }

    /// Polecats grouped by rig name.
    var polecatsByRig: [String: [PolecatState]] {
        Dictionary(grouping: polecats, by: \.rig)
    }
}
