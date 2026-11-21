import XCTest
@testable import Irrlicht

/// The guard that keeps a rendered Settings panel from talking to the
/// developer's live daemon about task-eta activation.
///
/// `SettingsView`'s task-eta toggle runs `ActivationClient.status()` from
/// `.onAppear` whenever Advanced is expanded, and its `.onChange` can POST or
/// DELETE (`DELETE` uninstalls the managed `~/.claude/CLAUDE.md` block).
/// `SettingsRelayTokenWriteTests` renders exactly that section, so in a test
/// process the endpoint is nil and both calls answer nil without a socket.
///
/// **Inert by construction:** every arm only builds or reads the URL; none
/// calls `status()` or `set(enabled:)`, and there is no `setUp`. The committed
/// mutation fixture runs these arms with the gate removed.
final class ActivationClientGuardTests: XCTestCase {

    /// The rule. The second arm is the vacuity guard: an endpoint that was nil
    /// unconditionally would pass the first and ship an app whose task-eta
    /// toggle can never reach its daemon.
    func testTheEndpointRuleIsNilUnderXCTestAndTheDaemonInTheApp() {
        XCTAssertNil(ActivationClient.endpoint(underXCTest: true),
                     "a test process must have no task-eta endpoint")
        XCTAssertEqual(ActivationClient.endpoint(underXCTest: false)?.path,
                       "/api/v1/activation/task-eta",
                       "the shipped app must still reach its daemon's task-eta endpoint")
    }

    /// The wiring: the endpoint `status()`/`set(enabled:)` actually use is nil
    /// in this process.
    func testTheEndpointIsNilInThisTestProcess() {
        XCTAssertNil(ActivationClient.endpoint,
                     "ActivationClient would send task-eta requests to the live daemon from a test process")
    }
}
