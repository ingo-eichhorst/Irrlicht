import XCTest
@testable import Irrlicht

/// The guard that keeps `swift test` from probing, killing or spawning real
/// daemons.
///
/// `DaemonManager.start()` probes the daemon port with a 2 s timeout and, on a
/// miss at the default port, runs `pkill -x irrlichd` — every `irrlichd` on
/// the machine, the developer's production daemon included — then spawns
/// `core/bin/irrlichd` if a dev build exists. `DaemonManager` now takes a
/// `DaemonProcessControl`, and a default-constructed one in a test process
/// records instead of acting; this class proves the default.
///
/// **Inert by construction, and that is load-bearing.** Every arm only
/// constructs a manager or a control and inspects its type. None calls
/// `start()`, `isDaemonReachable()`, `killStaleDaemons()`, `run(_:)` or even
/// `daemonExecutableURL`, and there is no `setUp`: the committed mutation
/// fixture (`tools/lib/keychain-test-isolation-swift-mutations_test.sh`) runs
/// these arms with the live default restored, where any of those calls would
/// reach the real port or the real process table.
@MainActor
final class DaemonManagerProcessControlGuardTests: XCTestCase {

    /// The rule. The second arm is the vacuity guard: a rule that returned the
    /// recorder unconditionally would pass the first and ship an app that
    /// never starts its daemon.
    func testTheProcessControlRuleRecordsUnderXCTestAndActsInTheApp() {
        XCTAssertTrue(
            DaemonManager.defaultProcessControl(underXCTest: true) is RecordingDaemonProcessControl,
            "a test process must get the recording process control")
        XCTAssertTrue(
            DaemonManager.defaultProcessControl(underXCTest: false) is LiveDaemonProcessControl,
            "the shipped app must still probe, clean up and spawn its daemon")
    }

    /// The wiring: `DaemonManager()` — the construction `DaemonManagerTests`,
    /// `SettingsViewTests` and `PinnedAppStorageSnapshotTests` use — got a
    /// process control that touches nothing.
    func testADefaultDaemonManagerInATestProcessDoesNotUseTheLiveProcessControl() {
        let control = DaemonManager().processControl
        XCTAssertFalse(
            control is LiveDaemonProcessControl,
            "DaemonManager() in a test process would probe \(DaemonEndpoint.httpBase) and, "
            + "on a miss, pkill every irrlichd on the machine")
        XCTAssertTrue(
            control is RecordingDaemonProcessControl,
            "DaemonManager() in a test process selected \(type(of: control))")
    }
}
