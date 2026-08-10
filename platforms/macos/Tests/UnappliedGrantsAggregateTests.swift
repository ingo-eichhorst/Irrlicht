import XCTest
@testable import Irrlicht

/// #1385 — the aggregate "N permissions are granted but not applied".
///
/// #1362 made a failed consent effect visible per permission, inside the
/// wizard. A startup re-apply failure therefore reached only a user who
/// opened Settings. These pin the daemon-computed aggregate the macOS app
/// renders passively instead, and the constraints it inherits: it must not
/// nag, must not auto-open the wizard, and must not change consent.
final class UnappliedGrantsAggregateTests: XCTestCase {

    private let installFailed = "settings.json is malformed: invalid character '}'"
    private let versionFloor = "claude 1.2.0 is below the required 2.0.0; upgrade and grant again"

    private func decode(_ json: String) throws -> PermissionsSnapshot {
        try JSONDecoder().decode(PermissionsSnapshot.self, from: Data(json.utf8))
    }

    /// A snapshot whose `hooks` grant is in force — the healthy baseline.
    private func healthyJSON() -> String {
        """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"granted",
        "title":"Install hooks","feature_unlocked":"Waiting detection",
        "touches":"~/.claude/settings.json","detail":"adds hook entries"}]}]}
        """
    }

    /// The same snapshot plus the daemon's aggregate, carrying both
    /// diagnoses that ride the effect-error path.
    private func unappliedJSON() -> String {
        """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"granted",
        "title":"Install hooks","feature_unlocked":"Waiting detection",
        "touches":"~/.claude/settings.json","detail":"adds hook entries",
        "effect_error":"\(installFailed)"}]}],
        "unapplied_grants":[
        {"agent":"claude-code","agent_display_name":"Claude Code","key":"hooks",
        "title":"Install hooks","reason":"\(installFailed)"},
        {"agent":"codex","agent_display_name":"Codex","key":"hooks",
        "title":"Install hooks","reason":"\(versionFloor)"}]}
        """
    }

    // MARK: - Decoding

    /// The daemon omits the field entirely when nothing is wrong
    /// (omitempty), so the absent key must decode to empty, not throw.
    func testAbsentAggregateDecodesAsEmpty() throws {
        let snap = try decode(healthyJSON())
        XCTAssertTrue(snap.unappliedGrants.isEmpty)
        XCTAssertNil(snap.unappliedGrantSummary)
    }

    func testAggregateDecodesFromWireFormat() throws {
        let snap = try decode(unappliedJSON())
        XCTAssertEqual(snap.unappliedGrants.count, 2)
        XCTAssertEqual(snap.unappliedGrants.first?.agentDisplayName, "Claude Code")
        XCTAssertEqual(snap.unappliedGrants.first?.title, "Install hooks")
        XCTAssertEqual(snap.unappliedGrants.first?.reason, installFailed)
    }

    // MARK: - The headline

    func testHeadlineCountsAndReadsNaturallyAtOne() throws {
        let two = try XCTUnwrap(decode(unappliedJSON()).unappliedGrantSummary)
        XCTAssertEqual(two.count, 2)
        XCTAssertEqual(two.text, "2 permissions are granted but not applied")

        let oneJSON = unappliedJSON().replacingOccurrences(
            of: """
            ,
            {"agent":"codex","agent_display_name":"Codex","key":"hooks",
            "title":"Install hooks","reason":"\(versionFloor)"}
            """, with: "")
        let one = try XCTUnwrap(decode(oneJSON).unappliedGrantSummary)
        XCTAssertEqual(one.count, 1)
        XCTAssertEqual(one.text, "1 permission is granted but not applied")
    }

    /// The headline is one number; the detail must still say WHICH and WHY,
    /// or an install that FAILED (#1362) and a refusal below the CLI
    /// version floor (#1365) collapse into the same undiagnosable warning.
    func testTheTwoDiagnosesStayDistinguishable() throws {
        let summary = try XCTUnwrap(decode(unappliedJSON()).unappliedGrantSummary)
        XCTAssertEqual(summary.items.map(\.reason), [installFailed, versionFloor])
        XCTAssertEqual(summary.items.map { "\($0.agentDisplayName): \($0.title)" },
                       ["Claude Code: Install hooks", "Codex: Install hooks"])
    }

    // MARK: - Locks (green by construction; they exist to catch a later change)

    /// #1385 constraint 2: the aggregate must not widen the auto wizard.
    /// `needsWizard` is what pops it open, and #1362 deliberately left it
    /// driven by PENDING items only — an unapplied grant is answered.
    func testAggregateDoesNotWidenNeedsWizard() throws {
        let snap = try decode(unappliedJSON())
        let agent = try XCTUnwrap(snap.agents.first)
        XCTAssertFalse(agent.hasPending)
        XCTAssertFalse(agent.needsWizard,
                       "an unapplied grant must not auto-present the wizard (#1362's loop)")
    }

    /// #1385 constraint 3: surfacing a failure is not a re-prompt. Nothing
    /// about the recorded consent moves.
    func testAggregateDoesNotChangeConsent() throws {
        let snap = try decode(unappliedJSON())
        let perm = try XCTUnwrap(snap.agents.first?.permissions.first)
        XCTAssertEqual(perm.state, .granted)
    }
}
