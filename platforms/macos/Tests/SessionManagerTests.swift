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
    
    // MARK: - SessionMetrics.formattedCost

    private func makeMetrics(cost: Double?) -> SessionMetrics {
        SessionMetrics(
            elapsedSeconds: 0,
            totalTokens: 0,
            modelName: "",
            contextWindow: nil,
            contextUtilization: 0,
            pressureLevel: "unknown",
            estimatedCostUSD: cost,
            lastAssistantText: nil,
            tasks: nil
        )
    }

    func testFormattedCost_DropsCentsAt100AndAbove() {
        // Below $100: keep two decimals. The prior bug hid cents at $10+.
        XCTAssertEqual(makeMetrics(cost: 9.99).formattedCost, "$9.99")
        XCTAssertEqual(makeMetrics(cost: 12.5).formattedCost, "$12.50")
        XCTAssertEqual(makeMetrics(cost: 99.99).formattedCost, "$99.99")
        // $100+: drop cents so the value fits the 36pt cost column behind the
        // context bar (issue #214).
        XCTAssertEqual(makeMetrics(cost: 100.0).formattedCost, "$100")
        XCTAssertEqual(makeMetrics(cost: 105.0).formattedCost, "$105")
        XCTAssertEqual(makeMetrics(cost: 106.99).formattedCost, "$107")
    }

    func testFormattedCost_SmallAndZero() {
        XCTAssertNil(makeMetrics(cost: nil).formattedCost)
        XCTAssertNil(makeMetrics(cost: 0).formattedCost)
        XCTAssertEqual(makeMetrics(cost: 0.005).formattedCost, "<$0.01")
        XCTAssertEqual(makeMetrics(cost: 0.01).formattedCost, "$0.01")
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
        // JetBrainsActivator uses dynamic bundle resolution; bundleID field is ""
        // but the activator IS registered (non-nil return).
        XCTAssertEqual(SessionLauncher.bundleID(for: "jetbrains"), "")
        // Kitty: KittyActivator (socket-based, bundle ID is still registered)
        XCTAssertEqual(SessionLauncher.bundleID(for: "kitty"),     "net.kovidgoyal.kitty")
        // New title-match hosts
        XCTAssertEqual(SessionLauncher.bundleID(for: "zed"),       "dev.zed.Zed")
        XCTAssertEqual(SessionLauncher.bundleID(for: "rio"),       "com.raphaelamorim.rio")
        XCTAssertEqual(SessionLauncher.bundleID(for: "tabby"),     "org.tabby")
        XCTAssertEqual(SessionLauncher.bundleID(for: "waveterm"),  "dev.commandline.waveterm")
        XCTAssertEqual(SessionLauncher.bundleID(for: "alacritty"), "org.alacritty")
        XCTAssertEqual(SessionLauncher.bundleID(for: "nova"),      "com.panic.Nova")
        XCTAssertEqual(SessionLauncher.bundleID(for: "cmux"),      "com.cmuxterm.app")
        // tmux is a decorator, not a standalone term_program — no registry entry.
        XCTAssertNil(SessionLauncher.bundleID(for: "tmux"))
        XCTAssertNil(SessionLauncher.bundleID(for: nil))
        XCTAssertNil(SessionLauncher.bundleID(for: "unknown-terminal"))
    }

    func testJetBrainsActivatorRunningBundleIDIsNilOrKnown() {
        let bid = JetBrainsActivator.runningBundleID()
        guard let bid else { return } // no IDE running — nothing to assert
        let known: Set<String> = [
            "com.jetbrains.goland", "com.jetbrains.intellij", "com.jetbrains.intellij.ce",
            "com.jetbrains.pycharm", "com.jetbrains.pycharm.ce", "com.jetbrains.WebStorm",
            "com.jetbrains.rider", "com.jetbrains.CLion", "com.jetbrains.rustrover",
        ]
        XCTAssertTrue(known.contains(bid), "unexpected JetBrains bundle ID: \(bid)")
    }

    func testTitleMatchScoreFullCwdDominates() {
        // Full cwd in title (iTerm2/Terminal tab title style) dominates any
        // ancestor match.
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"
        XCTAssertEqual(
            AXTitleMatchActivator.titleMatchScore(
                title: "ingo@mac: /Users/ingo/projects/irrlicht/.claude/worktrees/170 — zsh",
                cwd: cwd),
            1_000)
    }

    func testTitleMatchScoreDeepestAncestorWins() {
        // cwd is several levels below the VS Code workspace root.
        // VS Code's window title shows only the workspace folder name
        // ("irrlicht"). The matcher must still find that as an ancestor.
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"

        // parts index: Users(0) ingo(1) projects(2) irrlicht(3) .claude(4) worktrees(5) 170(6)
        //   Basename "170" — score 7.
        XCTAssertEqual(
            AXTitleMatchActivator.titleMatchScore(title: "SessionLauncher.swift — 170", cwd: cwd),
            7)

        //   "worktrees" at depth 5 → score 6 (basename "170" missing).
        XCTAssertEqual(
            AXTitleMatchActivator.titleMatchScore(title: "Edit.swift — worktrees", cwd: cwd),
            6)

        //   VS Code workspace is the repo root: "irrlicht" at depth 3 → score 4.
        XCTAssertEqual(
            AXTitleMatchActivator.titleMatchScore(title: "2.1.114 — irrlicht", cwd: cwd),
            4)
    }

    func testTitleMatchScoreSkipsGenericTopsAndHomeBasename() {
        // "Users" and the user's home basename must never match alone —
        // otherwise every title string containing "ingo" would win.
        let cwd = "/Users/ingo/projects/irrlicht"
        // Title matches "ingo" only — must score 0 (skipped).
        XCTAssertEqual(
            AXTitleMatchActivator.titleMatchScore(title: "ingo@mac: ~ — zsh", cwd: cwd),
            0)
        // Title matches "Users" only — must score 0.
        XCTAssertEqual(
            AXTitleMatchActivator.titleMatchScore(title: "Users directory", cwd: cwd),
            0)
    }

    func testTitleMatchScoreEmptyInputs() {
        let cwd = "/Users/ingo/projects/irrlicht"
        XCTAssertEqual(AXTitleMatchActivator.titleMatchScore(title: "", cwd: cwd), 0)
        XCTAssertEqual(AXTitleMatchActivator.titleMatchScore(title: "anything", cwd: ""), 0)
    }

    func testBestMatchIndexPicksDeepestAncestor() {
        // Worktree session, three VS Code windows open. Only one window
        // (the main repo) is an ancestor of the cwd — that one wins, even
        // though the cwd basename itself doesn't appear anywhere.
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"
        let titles = [
            "2.1.114 — irrlicht",                // 0: main repo, ancestor depth 3 → score 4
            "index.html — opencode-test",        // 1: unrelated
            "benchmark.md — agent-readyness",    // 2: unrelated
        ]
        XCTAssertEqual(AXTitleMatchActivator.bestMatchIndex(titles: titles, cwd: cwd), 0)
    }

    func testBestMatchIndexPrefersDeeperMatchWhenBothPresent() {
        // If both a deeper subfolder window ("core") and the repo root ("irrlicht")
        // are open, a cwd inside core should pick the core window.
        let cwd = "/Users/ingo/projects/irrlicht/core"
        let titles = [
            "README.md — irrlicht",     // ancestor at depth 3 → score 4
            "main.go — core",           // basename at depth 4 → score 5 (wins)
        ]
        XCTAssertEqual(AXTitleMatchActivator.bestMatchIndex(titles: titles, cwd: cwd), 1)
    }

    func testBestMatchIndexReturnsNilWhenNoMatch() {
        let cwd = "/Users/ingo/projects/irrlicht/.claude/worktrees/170"
        let titles = ["README.md — some-other-project", "", "main.swift — another"]
        XCTAssertNil(AXTitleMatchActivator.bestMatchIndex(titles: titles, cwd: cwd))
    }

    func testBestMatchIndexTieBreakByCWDPrefix() {
        // Two windows whose titles both contain "irrlicht" (same leaf depth = tie
        // on primary score). The second window also contains "a", the grandparent
        // segment of the actual cwd, so it should win the tie-break.
        let cwd = "/Users/ingo/a/irrlicht"
        let titles = [
            "README.md — irrlicht",   // 0: leaf match, prefix count 1 (irrlicht)
            "main.go — irrlicht — a", // 1: leaf match, prefix count 2 (irrlicht + a)
        ]
        XCTAssertEqual(AXTitleMatchActivator.bestMatchIndex(titles: titles, cwd: cwd), 1)
    }

    func testBestMatchIndexTmuxStyleTitleParsed() {
        // tmux window titles often embed full paths like "[0] 1:zsh /Users/.../irrlicht".
        // The Tier A score (1000) fires because the full cwd substring is present.
        let cwd = "/Users/ingo/projects/irrlicht"
        let titles = [
            "README.md — other-project",
            "[0] 1:zsh /Users/ingo/projects/irrlicht",  // full cwd → Tier A
        ]
        XCTAssertEqual(AXTitleMatchActivator.bestMatchIndex(titles: titles, cwd: cwd), 1)
    }

    func testCWDSegmentMatchCountBasic() {
        let cwd = "/Users/ingo/a/irrlicht"
        // Title contains both "irrlicht" and "a"
        XCTAssertEqual(
            AXTitleMatchActivator.cwdSegmentMatchCount(title: "main.go — irrlicht — a", cwd: cwd),
            2)
        // Title contains only "irrlicht"
        XCTAssertEqual(
            AXTitleMatchActivator.cwdSegmentMatchCount(title: "README.md — irrlicht", cwd: cwd),
            1)
        // Title contains neither — score 0
        XCTAssertEqual(
            AXTitleMatchActivator.cwdSegmentMatchCount(title: "index.html — other", cwd: cwd),
            0)
    }

    func testCWDSegmentMatchCountSkipsGenericSegments() {
        // "Users" and "home" are generic and must not inflate the count.
        let cwd = "/Users/ingo/irrlicht"
        XCTAssertEqual(
            AXTitleMatchActivator.cwdSegmentMatchCount(title: "Users — home — irrlicht", cwd: cwd),
            1) // only "irrlicht" counts; Users/ingo filtered out
    }

    // MARK: - iTerm UUID extraction

    func testITermUUIDExtractsFromLegacyFormat() {
        // Older iTerm2 ITERM_SESSION_ID: "w0t0p0:UUID"
        XCTAssertEqual(
            ITermActivator.iTermUUID(from: "w4t0p0:1FFEA6B4-1EA4-4A3C-86B6-B80027B5690F"),
            "1FFEA6B4-1EA4-4A3C-86B6-B80027B5690F")
    }

    func testITermUUIDExtractsFromCurrentFormat() {
        // Newer format: "w0:t0:p0:UUID"
        XCTAssertEqual(
            ITermActivator.iTermUUID(from: "w0:t0:p0:ABCD-1234"),
            "ABCD-1234")
    }

    func testITermUUIDWithoutColonReturnsInput() {
        // If the env value has no colon at all, treat the whole thing as a
        // UUID — best-effort, still lets the AppleScript try a match.
        XCTAssertEqual(ITermActivator.iTermUUID(from: "BARE-UUID"), "BARE-UUID")
    }

    func testITermUUIDEmptyOrNil() {
        XCTAssertNil(ITermActivator.iTermUUID(from: nil))
        XCTAssertNil(ITermActivator.iTermUUID(from: ""))
        // Trailing colon → empty tail → nil (no usable UUID to target).
        XCTAssertNil(ITermActivator.iTermUUID(from: "w0t0p0:"))
    }
}
