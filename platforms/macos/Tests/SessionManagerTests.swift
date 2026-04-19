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
    
    func testMissingFieldsHandling() throws {
        // Only session_id and state are required; every other field is
        // optional via decodeIfPresent (see SessionState.init(from:)).
        // Decoding must succeed and fill missing fields with defaults.
        let incompleteJSON = """
        {
            "session_id": "sess_incomplete",
            "state": "working"
        }
        """.data(using: .utf8)!

        let session = try JSONDecoder().decode(SessionState.self, from: incompleteJSON)
        XCTAssertEqual(session.id, "sess_incomplete")
        XCTAssertEqual(session.state, .working)
        XCTAssertEqual(session.model, "unknown")
        XCTAssertEqual(session.cwd, "")
        XCTAssertNil(session.transcriptPath)
        XCTAssertNil(session.eventCount)
        XCTAssertNil(session.metrics)
    }
    
    // MARK: - State Glyph Tests

    func testStateGlyphs() {
        XCTAssertEqual(SessionState.State.working.glyph, "hammer.fill")
        XCTAssertEqual(SessionState.State.waiting.glyph, "hourglass")
        XCTAssertEqual(SessionState.State.ready.glyph, "checkmark.circle.fill")
    }

    func testStateColors() {
        XCTAssertEqual(SessionState.State.working.color, "#8B5CF6")
        XCTAssertEqual(SessionState.State.waiting.color, "#FF9500")
        XCTAssertEqual(SessionState.State.ready.color, "#34C759")
    }

    func testUnknownStateFallsBackToReady() throws {
        let jsonData = """
        {
            "session_id": "sess_unknown123",
            "state": "cancelled_by_user",
            "model": "claude-3.7-sonnet",
            "cwd": "/Users/test/projects/app",
            "updated_at": 1234567890,
            "first_seen": 1234567800,
            "event_count": 3,
            "last_event": "SessionEnd"
        }
        """.data(using: .utf8)!

        let session = try JSONDecoder().decode(SessionState.self, from: jsonData)

        XCTAssertEqual(session.id, "sess_unknown123")
        XCTAssertEqual(session.state, .ready)
    }
    
    // MARK: - Display Formatting Tests
    
    func testShortIdGeneration() {
        let session = SessionState(
            id: "sess_abc123def456ghi789",
            state: .working,
            model: "claude-3.7-sonnet",
            cwd: "/test",
            transcriptPath: "/test/transcript.jsonl",
            firstSeen: Date(),
            updatedAt: Date(),
            eventCount: 1,
            lastEvent: "SessionStart"
        )
        
        XCTAssertEqual(session.shortId, "ghi789")  // Last 6 characters
    }
    
    func testTimeAgoFormatting() {
        let oneMinuteAgo = Date().addingTimeInterval(-60)
        let session = SessionState(
            id: "sess_test",
            state: .working,
            model: "claude-3.7-sonnet",
            cwd: "/test",
            transcriptPath: "/test/transcript.jsonl",
            firstSeen: oneMinuteAgo,
            updatedAt: oneMinuteAgo,
            eventCount: 1,
            lastEvent: "SessionStart"
        )

        // RelativeDateTimeFormatter with .abbreviated style is locale-dependent
        // (e.g. "1m ago", "1 min. ago", "1m" — varies by OS/locale). Just verify
        // we got a non-empty string that mentions the one-minute delta.
        let s = session.timeAgo
        XCTAssertFalse(s.isEmpty)
        XCTAssertTrue(s.contains("1"), "timeAgo = \(s), expected to contain '1'")
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

        XCTAssertEqual(sessionManager.glyphStrip, "hammer.fill hourglass checkmark.circle.fill")
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
            firstSeen: Date(),
            updatedAt: Date(),
            eventCount: 1,
            lastEvent: "SessionStart"
        )
    }
    
    private func createMockJSONFile(at url: URL, session: SessionState) throws {
        let data = try JSONEncoder().encode(session)
        try data.write(to: url)
    }

    // MARK: - Launcher

    func testLauncherDecodes() throws {
        let jsonData = """
        {
            "session_id": "sess_l",
            "state": "working",
            "model": "claude-opus-4-7",
            "cwd": "/Users/test/projects/app",
            "updated_at": 1700000000,
            "launcher": {
                "term_program": "iTerm.app",
                "iterm_session_id": "w0t0p0-ABC"
            }
        }
        """.data(using: .utf8)!

        let session = try JSONDecoder().decode(SessionState.self, from: jsonData)
        XCTAssertNotNil(session.launcher)
        XCTAssertEqual(session.launcher?.termProgram, "iTerm.app")
        XCTAssertEqual(session.launcher?.itermSessionID, "w0t0p0-ABC")
        XCTAssertNil(session.launcher?.tmuxPane)
    }

    func testLauncherMissingIsNil() throws {
        // Session JSON without a launcher key must still decode cleanly for
        // backwards compatibility with older daemon builds.
        let jsonData = """
        {
            "session_id": "sess_legacy",
            "state": "ready",
            "model": "claude-opus-4-7",
            "cwd": "/tmp",
            "updated_at": 1700000000
        }
        """.data(using: .utf8)!
        let session = try JSONDecoder().decode(SessionState.self, from: jsonData)
        XCTAssertNil(session.launcher)
    }

    // MARK: - SessionLauncher helpers

    func testSessionLauncherBundleIDDerivation() {
        XCTAssertEqual(SessionLauncher.bundleID(for: "iTerm.app"), "com.googlecode.iterm2")
        XCTAssertEqual(SessionLauncher.bundleID(for: "Apple_Terminal"), "com.apple.Terminal")
        XCTAssertEqual(SessionLauncher.bundleID(for: "vscode"), "com.microsoft.VSCode")
        XCTAssertEqual(SessionLauncher.bundleID(for: "ghostty"), "com.mitchellh.ghostty")
        XCTAssertNil(SessionLauncher.bundleID(for: "tmux"))
        XCTAssertNil(SessionLauncher.bundleID(for: nil))
        XCTAssertNil(SessionLauncher.bundleID(for: "unknown-terminal"))
    }

    func testTitleMatchScore() {
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"

        // 3: full absolute cwd in title (iTerm2/Terminal tab title style).
        XCTAssertEqual(
            SessionLauncher.titleMatchScore(
                title: "ingo@mac: /Users/ingo/projects/irrlicht/.claude/worktrees/170 — zsh",
                cwd: cwd),
            3)

        // 2: last two components match (VS Code shows "file — worktrees/170" rarely
        //    but terminals do: "~/…/worktrees/170").
        XCTAssertEqual(
            SessionLauncher.titleMatchScore(
                title: "Edit.swift — worktrees/170",
                cwd: cwd),
            2)

        // 1: basename only — weakest, still wins when unique.
        XCTAssertEqual(
            SessionLauncher.titleMatchScore(
                title: "SessionLauncher.swift — 170",
                cwd: cwd),
            1)

        // 0: unrelated title.
        XCTAssertEqual(
            SessionLauncher.titleMatchScore(
                title: "main.swift — irrlicht",
                cwd: cwd),
            0)

        // Empty inputs are safe.
        XCTAssertEqual(SessionLauncher.titleMatchScore(title: "", cwd: cwd), 0)
        XCTAssertEqual(SessionLauncher.titleMatchScore(title: "anything", cwd: ""), 0)
    }

    func testBestMatchIndexPicksHighestScoring() {
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"
        let titles = [
            "main.swift — irrlicht",             // 0: main repo window
            "SessionLauncher.swift — 170",       // 1: basename-only match
            "Edit.swift — worktrees/170",        // 2: last-two match, should win
        ]
        XCTAssertEqual(SessionLauncher.bestMatchIndex(titles: titles, cwd: cwd), 2)
    }

    func testBestMatchIndexDisambiguatesWorktreeVsMainRepo() {
        // Realistic collision: both windows have "irrlicht" in the title. The
        // worktree session's cwd has its own basename ("170"), so only the
        // worktree window should match.
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"
        let titles = [
            "README.md — irrlicht",                     // main repo (basename=irrlicht)
            "SessionLauncher.swift — 170",              // worktree (basename=170)
        ]
        XCTAssertEqual(SessionLauncher.bestMatchIndex(titles: titles, cwd: cwd), 1)
    }

    func testBestMatchIndexReturnsNilWhenNoMatch() {
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"
        let titles = ["README.md — some-other-project", "", "main.swift — another"]
        XCTAssertNil(SessionLauncher.bestMatchIndex(titles: titles, cwd: cwd))
    }

    func testBestMatchIndexTiesBreakByFirstOccurrence() {
        // AX returns windows in z-order (frontmost first). On equal scores we
        // prefer the frontmost, which is the first element.
        let cwd = "/tmp/foo"
        let titles = ["edit foo — bar", "view foo — baz"]
        XCTAssertEqual(SessionLauncher.bestMatchIndex(titles: titles, cwd: cwd), 0)
    }
}
