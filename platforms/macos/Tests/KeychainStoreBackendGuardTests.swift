import XCTest
@testable import Irrlicht

/// The guard that keeps `swift test` out of the user's real login keychain.
///
/// The relay token's Keychain item trusts only the signed release app, so any
/// read from a test process makes `securityd` put a password dialog on the
/// developer's screen — and the suite read it several times per run (see
/// `KeychainStore`'s doc comment). The fix is a backend chosen once per
/// process; this class proves the choice.
///
/// **Inert by construction, and that is load-bearing.** Every arm only
/// inspects which backend was selected. None calls `get`, `set` or `delete`,
/// and there is no `setUp`: the committed mutation fixture
/// (`tools/lib/keychain-test-isolation-swift-mutations_test.sh`) runs these
/// arms with the guard deliberately broken, where any of those calls would
/// reach the real keychain.
final class KeychainStoreBackendGuardTests: XCTestCase {

    /// A detector that cannot see the condition it keys on reports exactly
    /// what a healthy run reports. This arm fails loudly if
    /// `AppHome.isRunningTests` ever stops answering true in the test bundle
    /// (for instance if the suite moves to swift-testing, which does not link
    /// XCTest) — at which point every arm below would be guarding nothing.
    func testTheTestProcessDetectorFiresInThisVeryProcess() {
        XCTAssertTrue(
            AppHome.isRunningTests,
            "this assertion is running under XCTest by definition — if the detector "
            + "disagrees, KeychainStore.shared reads the user's real login keychain")
    }

    /// The rule: a test process gets the in-memory backend, the app gets the
    /// system one. The second arm is the vacuity guard — without it,
    /// `defaultBackend` returning in-memory unconditionally would pass the
    /// first and ship an app that never persists its token. Constructing a
    /// `SystemKeychainBackend` touches nothing; only its methods do.
    func testTheBackendRulePicksInMemoryUnderXCTestAndTheSystemKeychainInTheApp() {
        XCTAssertTrue(
            KeychainStore.defaultBackend(underXCTest: true) is InMemoryKeychainBackend,
            "a test process must get the in-memory backend")
        XCTAssertTrue(
            KeychainStore.defaultBackend(underXCTest: false) is SystemKeychainBackend,
            "the shipped app must still use the real keychain")
    }

    /// The wiring: the process-wide store every production call site uses
    /// (`DaemonManager`, `SessionManager+Relay`, `SettingsView`) took the
    /// in-memory backend in this process.
    func testTheSharedStoreUsesTheInMemoryBackendInATestProcess() {
        let backend = KeychainStore.shared.backend
        XCTAssertTrue(
            backend is InMemoryKeychainBackend,
            "KeychainStore.shared selected \(type(of: backend)) in a test process — "
            + "every get/set from the suite would reach the real login keychain")
    }
}
