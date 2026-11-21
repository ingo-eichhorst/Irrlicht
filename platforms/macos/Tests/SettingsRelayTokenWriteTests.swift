import XCTest
import SwiftUI
@testable import Irrlicht

/// Opening Settings must not rewrite the relay token.
///
/// `.onAppear` loads the stored token into the field's draft, and the field's
/// `.onChange(of:)` fired for that assignment exactly as for a keystroke — so
/// every Settings open re-saved the token it had just read (on the system
/// backend a delete-then-add of the item, re-created with an ACL trusting only
/// the writing binary) and nudged the relay link and the daemon as if the user
/// had changed it.
@MainActor
final class SettingsRelayTokenWriteTests: XCTestCase {

    private let account = "relayToken"

    /// The commit rule on its own: an edit that trims to the stored token
    /// writes nothing and reports no change, so the caller skips the nudges.
    func testCommittingTheStoredTokenWritesNothingAndReportsNoChange() {
        let backend = CountingKeychainBackend(items: [account: "tok"])
        let store = KeychainSecrets(backend: backend)

        XCTAssertFalse(SettingsView.commitRelayToken(" tok ", to: store),
                       "an unchanged token must not trigger the relay/daemon nudges")
        XCTAssertEqual(backend.writes, 0, "an unchanged token must not be written")
    }

    /// LOCK, not red-first: a real edit still writes once and reports a change.
    func testCommittingANewTokenWritesItAndReportsAChange() {
        let backend = CountingKeychainBackend(items: [account: "tok"])
        let store = KeychainSecrets(backend: backend)

        XCTAssertTrue(SettingsView.commitRelayToken(" new ", to: store))
        XCTAssertEqual(backend.writes, 1)
        XCTAssertEqual(store.get(account: account), "new")
    }

    /// The defect as the user hits it: render Settings with the relay section
    /// visible and a token already stored, and count backend writes.
    ///
    /// The vacuity guard is `backend.reads`: the field's `.onAppear` is the
    /// only reader, so a read proves the section appeared and loaded the token
    /// into the draft — without it, a render that never reached the relay
    /// section would also write nothing. After that, the run loop is spun for
    /// a bounded settle window in which, before the fix, the write landed.
    func testOpeningSettingsWithAStoredTokenWritesNothing() {
        let backend = CountingKeychainBackend(items: [account: "tok"])
        let store = KeychainSecrets(backend: backend)
        let defaults = InMemoryDefaults()
        defaults.set(true, forKey: "advancedSettingsExpanded")
        // Subscribe ON so the URL + token fields render. relayServerURL stays
        // empty, so SessionManager.relayTokenDidChange() returns before any
        // connect; publish stays OFF so no publish-status poll starts.
        defaults.set(true, forKey: "useRelayServer")

        let view = SettingsView(isPresented: .constant(true),
                                showPermissionsReview: .constant(false),
                                sessionManager: SessionManager(defaults: defaults),
                                keychain: store)
            .environmentObject(UpdateManager(startingUpdater: false))
            .environmentObject(DaemonManager())
        let host = PinnedSnapshotHost(view, width: SessionListView.panelWidth, height: 900, defaults: defaults)

        let begin = Date()
        while backend.reads == 0, Date().timeIntervalSince(begin) < 5 {
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
            host.view.layoutSubtreeIfNeeded()
        }
        let appeared = Date().timeIntervalSince(begin)
        XCTAssertEqual(backend.reads, 1,
                       "the relay section never loaded the token (\(String(format: "%.2f", appeared))s) — "
                       + "a zero write count below would prove nothing")

        let settle = Date()
        while backend.writes == 0, Date().timeIntervalSince(settle) < 1 {
            RunLoop.main.run(until: Date().addingTimeInterval(0.01))
            host.view.layoutSubtreeIfNeeded()
        }
        XCTAssertEqual(backend.writes, 0,
                       "opening Settings rewrote the stored token "
                       + "(write seen \(String(format: "%.2f", Date().timeIntervalSince(settle)))s after the load)")
    }
}
