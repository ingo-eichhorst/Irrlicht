import XCTest
@testable import Irrlicht

/// #1530 blocker 2 — the suite hang.
///
/// Outside an app bundle Sparkle's `startUpdater` fails, and
/// `SPUStandardUpdaterController` answers by `dispatch_after`-ing an `NSAlert`
/// onto the main queue and running it modally. Nothing in a headless run can
/// dismiss it, so the next test to spin the main run loop blocks forever — a
/// *hang*, which off a TTY produces an empty log and names no test at all.
/// `UpdateManager.init`'s doc comment carries the full mechanism.
///
/// These arms exist because the natural mutation for the last of them — delete
/// the guard and watch it go red — cannot be run on a developer Mac: it means
/// actually arming the modal alert. So the rule is split into a pure function
/// that CAN be reddened in place, plus a detector that must be shown to fire.
@MainActor
final class UpdateManagerSparkleGuardTests: XCTestCase {

    /// The rule itself. Mutating `shouldStartUpdater` to `requested` reddens
    /// the first arm; the second is the vacuity guard, without which
    /// `return false` would satisfy the first.
    func testShouldStartUpdaterRefusesUnderXCTestAndAllowsTheApp() {
        XCTAssertFalse(
            UpdateManager.shouldStartUpdater(requested: true, underXCTest: true),
            "a test host must never start Sparkle, however loudly the caller asks")
        XCTAssertTrue(
            UpdateManager.shouldStartUpdater(requested: true, underXCTest: false),
            "the shipped app must still start its updater")
        XCTAssertFalse(
            UpdateManager.shouldStartUpdater(requested: false, underXCTest: false),
            "an explicit `startingUpdater: false` is still honoured")
    }

    /// A guard that cannot see the condition it guards against reports exactly
    /// what a healthy run reports. This arm is the one that fails loudly if
    /// `NSClassFromString("XCTestCase")` ever stops answering — for instance
    /// if the suite moves to swift-testing, which does not link XCTest.
    func testXCTestIsDetectedInThisVeryProcess() {
        XCTAssertTrue(
            UpdateManager.isRunningUnderXCTest,
            "this assertion is running under XCTest by definition — if the "
            + "detector disagrees, every UpdateManager built in this bundle "
            + "arms Sparkle's modal alert")
    }

    /// The wiring: the default-argument construction — exactly the shape that
    /// shipped in `UnappliedGrantsBannerRenderTests` — records that it did not
    /// start the updater, and `init` hands Sparkle that same value.
    ///
    /// A **lock**, deliberately. Its mutation is "remove the `underXCTest`
    /// term from `init`", which would start Sparkle outside an app bundle and
    /// put a modal alert on the machine running it; the two arms above cover
    /// the same rule without that cost. What it does add is that `init`
    /// consults the rule at all — a nil `updaterStarted` wiring shows up here
    /// and nowhere else.
    func testDefaultConstructionDoesNotStartSparkleUnderXCTest() {
        XCTAssertFalse(
            UpdateManager().updaterStarted,
            "UpdateManager() must not start Sparkle inside a test host")
    }
}
