import XCTest
@testable import Irrlicht

@MainActor
final class AgentRegistryTests: XCTestCase {

    override func tearDown() async throws {
        // The registry is process-global; reset between tests so populated
        // state from one test doesn't leak into another's empty-registry
        // expectation.
        AgentRegistry.byName = [:]
        try await super.tearDown()
    }

    // MARK: - adapterName

    func testAdapterName_emptyRegistry_returnsRawAdapterKey() throws {
        let session = try makeSession(adapter: "fake-agent")
        XCTAssertEqual(session.adapterName, "fake-agent",
                       "missing registry entry should fall through to the raw adapter key")
    }

    func testAdapterName_emptyAdapter_returnsUnknownPlaceholder() throws {
        let session = try makeSession(adapter: nil)
        XCTAssertEqual(session.adapterName, "Unknown",
                       "no adapter field should resolve to a stable 'Unknown' label")
    }

    func testAdapterName_populatedRegistry_returnsDisplayName() throws {
        AgentRegistry.byName["claude-code"] = AgentBranding(
            name: "claude-code",
            displayName: "Claude Code",
            iconSVGLight: "<svg/>",
            iconSVGDark: "<svg/>"
        )
        let session = try makeSession(adapter: "claude-code")
        XCTAssertEqual(session.adapterName, "Claude Code")
    }

    // MARK: - adapterIcon

    func testAdapterIcon_emptyRegistry_returnsGenericIcon() throws {
        let session = try makeSession(adapter: "fake-agent")
        XCTAssertNotNil(session.adapterIcon,
                        "registry miss should still render the neutral generic icon, not nil")
    }

    func testAdapterIcon_populatedRegistry_returnsBrandedIcon() throws {
        AgentRegistry.byName["claude-code"] = AgentBranding(
            name: "claude-code",
            displayName: "Claude Code",
            iconSVGLight: """
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 56 56">\
            <rect x="0" y="0" width="56" height="56" fill="#D97757"/>\
            </svg>
            """,
            iconSVGDark: """
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 56 56">\
            <rect x="0" y="0" width="56" height="56" fill="#D97757"/>\
            </svg>
            """
        )
        let session = try makeSession(adapter: "claude-code")
        XCTAssertNotNil(session.adapterIcon)
    }

    // MARK: - Helpers

    /// Decodes a minimally-valid SessionState payload with the given adapter
    /// field. Mirrors the irrlichd JSON shape used by SessionManagerTests.
    private func makeSession(adapter: String?) throws -> SessionState {
        let adapterField: String
        if let a = adapter {
            adapterField = "\"adapter\": \"\(a)\","
        } else {
            adapterField = ""
        }
        let json = """
        {
            "session_id": "sess_test",
            "state": "ready",
            "model": "test-model",
            "cwd": "/tmp",
            "first_seen": "2024-01-01T00:00:00.000Z",
            "updated_at": "2024-01-01T00:00:00.000Z",
            \(adapterField)
            "transcript_path": "/tmp/t.jsonl"
        }
        """.data(using: .utf8)!
        return try JSONDecoder().decode(SessionState.self, from: json)
    }
}
