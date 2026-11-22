import XCTest
@testable import Irrlicht

/// The guard that keeps `swift test` from reconfiguring the developer's live
/// production daemon.
///
/// `DaemonManager.publishSettingsDidChange()` and `start()`'s adopt path both
/// end in a publish. With `PublishClient.apply` hard-wired there, a test
/// process sent its own (xctest-domain) publish settings — and a token read
/// from the real keychain — to `PUT http://127.0.0.1:7837/api/v1/relay/publish`,
/// i.e. to whatever daemon the developer had running. `DaemonManager` now
/// takes a `RelayPublishing`, and a default-constructed one in a test process
/// records instead of sending; this class proves the default.
///
/// **Inert by construction, and that is load-bearing.** Every arm only
/// constructs a manager and inspects its publisher. None calls `start()`,
/// `publishSettingsDidChange()` or `publish(_:)`, and there is no `setUp`: the
/// committed mutation fixture
/// (`tools/lib/keychain-test-isolation-swift-mutations_test.sh`) runs these
/// arms with the live default restored, where any of those calls would reach
/// the live daemon.
@MainActor
final class DaemonManagerPublisherGuardTests: XCTestCase {

    /// The rule. The second arm is the vacuity guard: a rule that returned the
    /// recorder unconditionally would pass the first and ship an app that
    /// never tells its daemon to publish.
    func testThePublisherRuleRecordsUnderXCTestAndSendsInTheApp() {
        XCTAssertTrue(
            DaemonManager.defaultPublisher(underXCTest: true) is RecordingRelayPublisher,
            "a test process must get the recording publisher")
        XCTAssertTrue(
            DaemonManager.defaultPublisher(underXCTest: false) is LiveRelayPublisher,
            "the shipped app must still PUT to its daemon")
    }

    /// The wiring: `DaemonManager()` — exactly the construction
    /// `DaemonManagerTests.setUp`, `SettingsViewTests` and
    /// `PinnedAppStorageSnapshotTests` use — got a publisher that sends
    /// nothing.
    func testADefaultDaemonManagerInATestProcessDoesNotUseTheLivePublisher() {
        let publisher = DaemonManager().publisher
        XCTAssertFalse(
            publisher is LiveRelayPublisher,
            "DaemonManager() in a test process would PUT to the live daemon on "
            + "\(DaemonEndpoint.httpBase)\(PublishClient.path)")
        XCTAssertTrue(
            publisher is RecordingRelayPublisher,
            "DaemonManager() in a test process selected \(type(of: publisher))")
    }
}
