import XCTest
@testable import Irrlicht

/// #1362 — a failed consent effect must be decoded, surfaced, and
/// retryable in the macOS wizard, without ever rolling the grant back.
final class AgentPermissionsEffectErrorTests: XCTestCase {

    private let reason = "settings.json is malformed: invalid character '}'"

    private func decodeSnapshot(_ json: String) throws -> PermissionsSnapshot {
        try JSONDecoder().decode(PermissionsSnapshot.self, from: Data(json.utf8))
    }

    private func item(state: String, effectError: String?) throws -> PermissionItem {
        let errField = effectError.map { ", \"effect_error\": \"\($0)\"" } ?? ""
        let json = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"\(state)",
        "title":"Install hooks","feature_unlocked":"waiting detection","touches":"settings.json",
        "detail":"adds hook entries"\(errField)}]}]}
        """
        return try XCTUnwrap(decodeSnapshot(json).agents.first?.permissions.first)
    }

    // MARK: - Decoding

    /// The daemon omits `effect_error` when the effect succeeded
    /// (omitempty), so the absent key must decode, not throw.
    func testAbsentEffectErrorDecodesAsNil() throws {
        let perm = try item(state: "granted", effectError: nil)
        XCTAssertNil(perm.effectError)
        XCTAssertNil(perm.effectNotice)
        XCTAssertEqual(perm.state, .granted)
    }

    func testEffectErrorDecodesFromWireFormat() throws {
        let perm = try item(state: "granted", effectError: reason)
        XCTAssertEqual(perm.effectError, reason)
    }

    // MARK: - The notice

    func testGrantedButNotAppliedNotice() throws {
        let notice = try XCTUnwrap(item(state: "granted", effectError: reason).effectNotice)
        XCTAssertEqual(notice.label, "Granted, but not applied")
        XCTAssertEqual(notice.reason, reason)
        XCTAssertEqual(notice.retryLabel, "Retry")
        // Retry re-submits the CURRENT decision — never flips consent off.
        XCTAssertTrue(notice.grant)
    }

    func testRevokedButNotUndoneNotice() throws {
        let notice = try XCTUnwrap(item(state: "denied", effectError: reason).effectNotice)
        XCTAssertEqual(notice.label, "Revoked, but not undone")
        XCTAssertFalse(notice.grant)
    }

    func testHealthyAndPendingProduceNoNotice() throws {
        XCTAssertNil(try item(state: "granted", effectError: nil).effectNotice)
        XCTAssertNil(try item(state: "granted", effectError: "").effectNotice)
        // Pending never runs an effect; a stray error is not rendered.
        XCTAssertNil(try item(state: "pending", effectError: reason).effectNotice)
    }

    /// The grant is NOT rolled back by a failed effect — the single most
    /// important constraint in #1362. A LOCK.
    func testFailedApplyStillReadsAsGranted() throws {
        let perm = try item(state: "granted", effectError: reason)
        XCTAssertEqual(perm.state, .granted, "a failed Apply must not revoke consent")
        XCTAssertNotNil(perm.effectNotice, "...but it must be visible")
    }

    // MARK: - Apply batch (retryability)

    func testUnchangedFailedPermissionIsResubmittedSoApplyRetries() throws {
        let failed = try item(state: "granted", effectError: reason)
        // Toggle unchanged (still on) — pre-#1362 this was diffed away and
        // the retry never reached the daemon.
        XCTAssertTrue(failed.shouldSubmit(grant: true))
    }

    func testUnchangedHealthyPermissionIsStillSkipped() throws {
        let healthy = try item(state: "granted", effectError: nil)
        XCTAssertFalse(healthy.shouldSubmit(grant: true))
        XCTAssertTrue(healthy.shouldSubmit(grant: false), "a real change is submitted")
    }

    func testPendingIsAlwaysSubmitted() throws {
        let pending = try item(state: "pending", effectError: nil)
        XCTAssertTrue(pending.shouldSubmit(grant: true))
        XCTAssertTrue(pending.shouldSubmit(grant: false))
    }

    func testFailedRemoveIsResubmittedAsDeny() throws {
        let failed = try item(state: "denied", effectError: reason)
        XCTAssertTrue(failed.shouldSubmit(grant: false))
    }

    // MARK: - Auto-wizard lifecycle

    /// Dismissal must not treat "granted but broken" as resolved, or the
    /// wizard closes on a success that installed nothing.
    func testFailedEffectKeepsTheAutoWizardOpen() throws {
        let json = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"granted",
        "title":"Install hooks","feature_unlocked":"","touches":"","detail":"",
        "effect_error":"\(reason)"}]}]}
        """
        let agent = try XCTUnwrap(decodeSnapshot(json).agents.first)
        XCTAssertTrue(agent.hasUnresolvedPermissions)
        // ...but it must NOT re-present the wizard on its own, or a
        // permanent failure would loop forever.
        XCTAssertFalse(agent.needsWizard)
    }

    func testFullyAnsweredHealthyAgentDismissesTheWizard() throws {
        let json = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"granted",
        "title":"Install hooks","feature_unlocked":"","touches":"","detail":""}]}]}
        """
        let agent = try XCTUnwrap(decodeSnapshot(json).agents.first)
        XCTAssertFalse(agent.hasUnresolvedPermissions)
        XCTAssertFalse(agent.needsWizard)
    }

    /// The review wizard closes on Apply. The daemon answers 200 whether
    /// or not the closure succeeded, so closing on `ok` alone would hide
    /// the failure on the one surface built to show it.
    func testHasFailedEffectDistinguishesBrokenFromMerelyAnswered() throws {
        let broken = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"granted",
        "title":"Install hooks","feature_unlocked":"","touches":"","detail":"",
        "effect_error":"\(reason)"}]}]}
        """
        let fine = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"granted",
        "title":"Install hooks","feature_unlocked":"","touches":"","detail":""}]}]}
        """
        XCTAssertTrue(try XCTUnwrap(decodeSnapshot(broken).agents.first).hasFailedEffect)
        XCTAssertFalse(try XCTUnwrap(decodeSnapshot(fine).agents.first).hasFailedEffect)
        // A pending permission has not failed — it simply hasn't run.
        let stillPending = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"pending",
        "title":"Install hooks","feature_unlocked":"","touches":"","detail":""}]}]}
        """
        let agent = try XCTUnwrap(decodeSnapshot(stillPending).agents.first)
        XCTAssertFalse(agent.hasFailedEffect)
        XCTAssertTrue(agent.hasUnresolvedPermissions)
    }

    func testPendingAgentStillNeedsAndHoldsTheWizard() throws {
        let json = """
        {"mode":"ask","agents":[{"name":"claude-code","display_name":"Claude Code",
        "detected":true,"permissions":[{"key":"hooks","kind":"modify","state":"pending",
        "title":"Install hooks","feature_unlocked":"","touches":"","detail":""}]}]}
        """
        let agent = try XCTUnwrap(decodeSnapshot(json).agents.first)
        XCTAssertTrue(agent.hasUnresolvedPermissions)
        XCTAssertTrue(agent.needsWizard)
    }
}
