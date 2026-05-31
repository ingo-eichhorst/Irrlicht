import XCTest
@testable import Irrlicht

/// Verifies the macOS `OrchestratorState` Codable contract matches the daemon's
/// `orchestrator.State` JSON (core/domain/orchestrator/state.go) — key remapping
/// and convoy filtering in particular.
final class OrchestratorStateDecodeTests: XCTestCase {
    private func decode(_ json: String) throws -> OrchestratorState {
        try JSONDecoder().decode(OrchestratorState.self, from: Data(json.utf8))
    }

    func testDecodesFullSnapshotWithKeyRemapping() throws {
        let json = """
        {
          "adapter": "gastown",
          "running": true,
          "root": "/gt",
          "global_agents": [
            {"role": "mayor", "icon": "🎩", "description": "Coordinates", "session_id": "sess-mayor", "state": "working"},
            {"role": "deacon", "icon": "📋", "state": "idle"}
          ],
          "codebases": [
            {"name": "rig-1", "repo_url": "git@x:rig-1", "status": "operational", "worktrees": [
              {"path": "/gt/rig-1", "branch": "main", "is_main": true, "workers": [
                {"role": "polecat", "icon": "👷", "name": "pc-1", "id": "bead-1234abcd", "session_id": "sess-pc", "state": "working"}
              ]}
            ]}
          ],
          "work_units": [
            {"id": "c1", "type": "convoy", "name": "Ship it", "source": "gastown", "total": 4, "done": 4},
            {"id": "t1", "type": "task_list", "name": "Chores", "source": "gastown", "total": 3, "done": 1}
          ],
          "health": {"daemon_running": true, "pid": 4242, "heartbeat_count": 9, "boot_running": true, "boot_degraded": false, "session_alive": true},
          "updated_at": "2026-05-31T00:00:00Z"
        }
        """
        let st = try decode(json)

        XCTAssertEqual(st.adapter, "gastown")
        XCTAssertTrue(st.running)

        XCTAssertEqual(st.globalAgents?.count, 2)
        XCTAssertEqual(st.globalAgents?[0].sessionID, "sess-mayor")
        XCTAssertNil(st.globalAgents?[1].sessionID, "missing session_id decodes to nil")

        let worker = st.codebases?.first?.worktrees?.first?.workers?.first
        XCTAssertEqual(st.codebases?.first?.repoURL, "git@x:rig-1")
        XCTAssertEqual(st.codebases?.first?.worktrees?.first?.isMain, true)
        XCTAssertEqual(worker?.workerID, "bead-1234abcd", "worker `id` maps to workerID")
        XCTAssertEqual(worker?.sessionID, "sess-pc")

        // Convoys are work_units filtered to type == "convoy".
        XCTAssertEqual(st.convoys.map { $0.id }, ["c1"])
        XCTAssertTrue(st.convoys.first?.isDone == true)

        XCTAssertEqual(st.health?.pid, 4242)
        XCTAssertEqual(st.health?.heartbeatCount, 9)
    }

    func testDecodesMinimalSnapshotTolerant() throws {
        // Older / partial daemons omit everything but adapter + running.
        let st = try decode(#"{"adapter":"gastown","running":false,"updated_at":"2026-05-31T00:00:00Z"}"#)
        XCTAssertFalse(st.running)
        XCTAssertNil(st.globalAgents)
        XCTAssertNil(st.codebases)
        XCTAssertTrue(st.convoys.isEmpty)
        XCTAssertNil(st.health)
    }
}
