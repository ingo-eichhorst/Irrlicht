import XCTest
@testable import Irrlicht
import Foundation

@MainActor
final class SessionManagerTests: XCTestCase {
    var sessionManager: SessionManager!
    var tempDirectory: URL!
    
    override func setUp() async throws {
        try await super.setUp()
        
        // Create temporary directory for testing
        tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("IrrlichtTests")
            .appendingPathComponent(UUID().uuidString)
        
        try FileManager.default.createDirectory(
            at: tempDirectory,
            withIntermediateDirectories: true,
            attributes: nil
        )
        
        sessionManager = SessionManager()
    }
    
    override func tearDown() async throws {
        sessionManager = nil
        
        // Clean up temporary directory
        if let tempDirectory = tempDirectory {
            try? FileManager.default.removeItem(at: tempDirectory)
        }
        
        try await super.tearDown()
    }
    
    // MARK: - JSON Parsing Tests
    
    func testValidSessionStateParsing() throws {
        let jsonData = """
        {
            "session_id": "sess_test123",
            "state": "working",
            "model": "claude-3.7-sonnet",
            "cwd": "/Users/test/projects/app",
            "transcript_path": "/Users/test/.claude/projects/app/transcript.jsonl",
            "updated_at": "2024-09-06T15:30:00.000Z",
            "event_count": 5,
            "last_event": "UserPromptSubmit"
        }
        """.data(using: .utf8)!
        
        let session = try JSONDecoder().decode(SessionState.self, from: jsonData)
        
        XCTAssertEqual(session.id, "sess_test123")
        XCTAssertEqual(session.state, .working)
        XCTAssertEqual(session.model, "claude-3.7-sonnet")
        XCTAssertEqual(session.cwd, "/Users/test/projects/app")
        XCTAssertEqual(session.transcriptPath, "/Users/test/.claude/projects/app/transcript.jsonl")
        XCTAssertEqual(session.eventCount, 5)
        XCTAssertEqual(session.lastEvent, "UserPromptSubmit")
    }
    
    func testInvalidJSONHandling() {
        let invalidJSON = """
        {
            "session_id": "sess_invalid",
            "state": "unknown_state",
            // Invalid comment in JSON
            "invalid": true
        }
        """.data(using: .utf8)!
        
        XCTAssertThrowsError(try JSONDecoder().decode(SessionState.self, from: invalidJSON))
    }
    
    func testMissingFieldsHandling() {
        let incompleteJSON = """
        {
            "session_id": "sess_incomplete",
            "state": "working"
        }
        """.data(using: .utf8)!
        
        XCTAssertThrowsError(try JSONDecoder().decode(SessionState.self, from: incompleteJSON))
    }
    
    // MARK: - State Glyph Tests
    
    func testStateGlyphs() {
        XCTAssertEqual(SessionState.State.working.glyph, "●")
        XCTAssertEqual(SessionState.State.waiting.glyph, "◔")
        XCTAssertEqual(SessionState.State.ready.glyph, "checkmark.circle.fill")
    }
    
    func testStateColors() {
        XCTAssertEqual(SessionState.State.working.color, "#8B5CF6")
        XCTAssertEqual(SessionState.State.waiting.color, "#F59E0B") 
        XCTAssertEqual(SessionState.State.ready.color, "#34C759")
    }
    
    // MARK: - Display Formatting Tests
    
    func testShortIdGeneration() {
        let session = SessionState(
            id: "sess_abc123def456ghi789",
            state: .working,
            model: "claude-3.7-sonnet",
            cwd: "/test",
            transcriptPath: "/test/transcript.jsonl",
            updatedAt: Date(),
            eventCount: 1,
            lastEvent: "SessionStart"
        )
        
        XCTAssertEqual(session.shortId, "hi789")  // Last 6 characters
    }
    
    func testTimeAgoFormatting() {
        let oneMinuteAgo = Date().addingTimeInterval(-60)
        let session = SessionState(
            id: "sess_test",
            state: .working,
            model: "claude-3.7-sonnet",
            cwd: "/test",
            transcriptPath: "/test/transcript.jsonl",
            updatedAt: oneMinuteAgo,
            eventCount: 1,
            lastEvent: "SessionStart"
        )
        
        // Should show something like "1m ago"
        XCTAssertTrue(session.timeAgo.contains("ago"))
    }
    
    // MARK: - Session Manager Tests
    
    func testEmptyGlyphStrip() {
        sessionManager.sessions = []
        XCTAssertEqual(sessionManager.glyphStrip, "○")
    }
    
    func testGlyphStripWithFewSessions() {
        sessionManager.sessions = [
            createMockSession(id: "1", state: .working),
            createMockSession(id: "2", state: .waiting),
            createMockSession(id: "3", state: .ready)
        ]
        
        XCTAssertEqual(sessionManager.glyphStrip, "● ◔ ✓")
    }
    
    func testGlyphStripWithManySessions() {
        sessionManager.sessions = Array(1...5).map { 
            createMockSession(id: "\($0)", state: .working)
        }
        
        XCTAssertEqual(sessionManager.glyphStrip, "5 sessions")
    }
    
    func testActiveSessionsDetection() {
        sessionManager.sessions = [
            createMockSession(id: "1", state: .working),
            createMockSession(id: "2", state: .ready)
        ]
        
        XCTAssertTrue(sessionManager.hasActiveSessions)
        
        sessionManager.sessions = [
            createMockSession(id: "1", state: .ready)
        ]
        
        XCTAssertFalse(sessionManager.hasActiveSessions)
    }
    
    func testSessionCountsByState() {
        sessionManager.sessions = [
            createMockSession(id: "1", state: .working),
            createMockSession(id: "2", state: .working),
            createMockSession(id: "3", state: .waiting),
            createMockSession(id: "4", state: .ready)
        ]
        
        XCTAssertEqual(sessionManager.workingSessions, 2)
        XCTAssertEqual(sessionManager.waitingSessions, 1)
        XCTAssertEqual(sessionManager.readySessions, 1)
    }
    
    // MARK: - Helper Methods
    
    private func createMockSession(id: String, state: SessionState.State) -> SessionState {
        SessionState(
            id: "sess_\(id)",
            state: state,
            model: "claude-3.7-sonnet",
            cwd: "/Users/test/projects/test",
            transcriptPath: "/Users/test/.claude/projects/test/transcript.jsonl",
            updatedAt: Date(),
            eventCount: 1,
            lastEvent: "SessionStart"
        )
    }
    
    private func createMockJSONFile(at url: URL, session: SessionState) throws {
        let data = try JSONEncoder().encode(session)
        try data.write(to: url)
    }
}