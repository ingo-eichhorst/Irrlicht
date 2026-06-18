import XCTest
@testable import Irrlicht
import Foundation

/// Regression coverage for the #690 coalesced UI refresh.
///
/// Background: with 40+ working agents, each `session_updated` push used to run
/// a full `rebuildSessionsFromMap()` + `patchApiGroups()` and reassign the
/// @Published surfaces — and because every WS message resumes as its own
/// @MainActor turn, SwiftUI re-rendered the whole (eager, non-virtualized)
/// session list once per message. That O(N) work × N pushes/sec was the ~25%
/// CPU. These tests pin the fix: a burst of pure-metric pushes must collapse
/// into a single render pass, while a state transition still flushes
/// immediately so the UI stays snappy.
@MainActor
final class SessionManagerCoalescingTests: XCTestCase {
    typealias AgentGroup = SessionManager.AgentGroup

    private var sut: SessionManager!
    private var originalProjectGroupOrder: Any?

    override func setUp() async throws {
        try await super.setUp()
        // seedLocalApiGroups persists projectGroupOrder to UserDefaults; snapshot
        // + restore so the developer's real order survives the run (same pattern
        // as the apiGroups tests).
        originalProjectGroupOrder = UserDefaults.standard.object(forKey: "projectGroupOrder")
        sut = SessionManager()
        // Long window so the trailing timer never fires mid-test — flushes are
        // driven explicitly via flushPendingUIRefreshForTests().
        sut.uiRefreshInterval = 1000
    }

    override func tearDown() async throws {
        if let originalProjectGroupOrder {
            UserDefaults.standard.set(originalProjectGroupOrder, forKey: "projectGroupOrder")
        } else {
            UserDefaults.standard.removeObject(forKey: "projectGroupOrder")
        }
        sut = nil
        try await super.tearDown()
    }

    /// A storm of pure-metric updates (state unchanged) must not render once per
    /// message — they coalesce into exactly one flush, and every update lands.
    func testMetricStorm_coalescesIntoSingleFlush() {
        let ids = (0..<40).map { "s\($0)" }
        seedWorkingSessions(ids)

        for (n, id) in ids.enumerated() {
            sut.handleWsMessage(updatedEnvelope(id: id, seq: UInt64(n + 1), cost: Double(n)))
        }

        XCTAssertEqual(sut.uiRefreshFlushCount, 0,
                       "40 metric pushes must not render synchronously — they coalesce")

        sut.flushPendingUIRefreshForTests()

        XCTAssertEqual(sut.uiRefreshFlushCount, 1,
                       "the coalesced burst must apply in exactly one render pass")
        let costByID = sut.apiGroups
            .flatMap { $0.agents ?? [] }
            .reduce(into: [String: Double]()) { $0[$1.id] = $1.metrics?.estimatedCostUSD ?? -1 }
        XCTAssertEqual(costByID.count, 40, "all 40 sessions present after flush")
        XCTAssertEqual(costByID["s39"], 39, "the last update for each session is applied on flush")
    }

    /// A state transition (working → waiting) bypasses the window so the row
    /// updates immediately — transitions fire at human pace, not the metric rate.
    func testStateChange_flushesImmediately() {
        seedWorkingSessions(["a", "b"])

        sut.handleWsMessage(updatedEnvelope(id: "a", seq: 1, cost: 1, state: "waiting"))

        XCTAssertEqual(sut.uiRefreshFlushCount, 1,
                       "a state transition flushes immediately, without waiting for the window")
        let stateA = sut.apiGroups.flatMap { $0.agents ?? [] }.first { $0.id == "a" }?.state
        XCTAssertEqual(stateA, .waiting, "the transition is reflected in the list-view surface")
    }

    // MARK: - Helpers

    /// Seed `ids` as working sessions present in both surfaces so WS updates
    /// patch in place (and `groupedSessionIds` contains them).
    private func seedWorkingSessions(_ ids: [String]) {
        let agents = ids.map { decodeSession(sessionObject(id: $0, cost: 0)) }
        for s in agents { sut.sessionMap[s.id] = s }
        sut.seedLocalApiGroups([AgentGroup(name: "proj", agents: agents)])
    }

    private func decodeSession(_ json: String) -> SessionState {
        // swiftlint:disable:next force_try
        try! JSONDecoder().decode(SessionState.self, from: Data(json.utf8))
    }

    private func sessionObject(id: String, cost: Double, state: String = "working") -> String {
        """
        {"session_id":"\(id)","state":"\(state)","model":"m","cwd":"/tmp","project_name":"proj",\
        "first_seen":0,"updated_at":0,"metrics":{"elapsed_seconds":0,"total_tokens":0,"model_name":"m",\
        "context_utilization_percentage":0,"pressure_level":"safe","estimated_cost_usd":\(cost)}}
        """
    }

    private func updatedEnvelope(id: String, seq: UInt64, cost: Double, state: String = "working") -> String {
        """
        {"type":"session_updated","seq":\(seq),"session":\(sessionObject(id: id, cost: cost, state: state))}
        """
    }
}
