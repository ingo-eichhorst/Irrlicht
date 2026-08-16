import Foundation
import Sparkle
import os

/// Manages Sparkle's auto-update lifecycle.
///
/// Wraps `SPUStandardUpdaterController` so SwiftUI views can bind to update
/// state through `@Published` properties instead of importing Sparkle directly.
/// Sparkle's standard UI handles the find/download/install/relaunch flow,
/// including the "You're up to date" sheet, so this class only exposes the
/// preferences toggle, the last-checked timestamp, and a manual trigger.
///
/// `lastUpdateCheckDate` is refreshed lazily: on demand via `refresh()` from
/// Settings.onAppear and 1s after a manual `checkForUpdates()` call. There's
/// no background polling — auto-checks run ~daily, and the only place the
/// timestamp is visible is the Settings panel.
@MainActor
final class UpdateManager: ObservableObject {
    @Published var automaticallyChecksForUpdates: Bool {
        didSet {
            controller.updater.automaticallyChecksForUpdates = automaticallyChecksForUpdates
        }
    }

    @Published private(set) var lastUpdateCheckDate: Date?

    private let controller: SPUStandardUpdaterController
    private let logger = Logger(subsystem: "io.irrlicht.app", category: "UpdateManager")

    /// Did this instance actually start Sparkle's update cycle? Exactly the
    /// value handed to `SPUStandardUpdaterController` — see `init`.
    private(set) var updaterStarted: Bool

    /// Is XCTest loaded into this process? True in every test host and false
    /// in the shipped app, where the framework is never linked.
    ///
    /// Keyed on the framework's own class rather than on `XCTestConfigurationFilePath`,
    /// which `swift test` and `xcodebuild` set but a bare `xctest` invocation
    /// need not.
    static var isRunningUnderXCTest: Bool {
        NSClassFromString("XCTestCase") != nil
    }

    /// Should `init` start Sparkle's update cycle?
    ///
    /// Split out as a pure function so the rule can be asserted without
    /// constructing the thing it protects against — reddening it in place
    /// would mean actually arming the modal alert described below.
    static func shouldStartUpdater(requested: Bool, underXCTest: Bool) -> Bool {
        requested && !underXCTest
    }

    /// `startingUpdater: false` skips starting Sparkle's update cycle — for
    /// tests, which run outside an app bundle where Sparkle's start would
    /// fail its bundle checks.
    ///
    /// Under XCTest that request is overridden to `false` no matter what the
    /// caller asked for, and this is the whole of #1530's blocker 2. Outside
    /// an app bundle `-[SPUUpdater startUpdater:]` fails, and
    /// `SPUStandardUpdaterController.startUpdater` answers a failure by
    /// `dispatch_after`-ing an `NSAlert` onto the **main queue** one second
    /// later and calling `-[NSAlert runModal]` on it
    /// (`Sparkle/SPUStandardUpdaterController.m`, "Delay the alert a bit to
    /// allow other start-up actions"). In a headless test run nobody can
    /// dismiss a modal alert, so the modal loop never returns.
    ///
    /// The consequences are what made it hard to attribute. The block is
    /// drained by whichever test happens to spin the main run loop a second
    /// later — any suite using `override func tearDown() async throws`, which
    /// XCTest wraps in an `XCTWaiter` — so the hang is reported against a test
    /// that has nothing to do with the updater, and against a *different* one
    /// on each run: `SessionRowSnapshotTests.testFixtureAntigravityGhost` on a
    /// GitHub runner, `testRelayCloudOnline` and `testRoleOrchestratorRow`
    /// locally. It is positional, not per-test. And because it is a hang
    /// rather than a failure, off a TTY it produces an *empty* log.
    ///
    /// The offending call site was `UnappliedGrantsBannerRenderTests`, which
    /// took the `true` default. Fixing that one line would have worked and
    /// would have been forgettable; the guard here is what makes the next one
    /// safe by existing.
    init(startingUpdater requested: Bool = true) {
        // One value, used twice: what Sparkle is told and what `updaterStarted`
        // reports are the same expression, so the two cannot disagree.
        let start = UpdateManager.shouldStartUpdater(
            requested: requested,
            underXCTest: UpdateManager.isRunningUnderXCTest
        )
        updaterStarted = start
        controller = SPUStandardUpdaterController(
            startingUpdater: start,
            updaterDelegate: nil,
            userDriverDelegate: nil
        )
        automaticallyChecksForUpdates = controller.updater.automaticallyChecksForUpdates
        lastUpdateCheckDate = controller.updater.lastUpdateCheckDate
        if start {
            logger.info("Sparkle updater started (auto checks: \(self.automaticallyChecksForUpdates, privacy: .public))")
        }
    }

    func checkForUpdates() {
        controller.checkForUpdates(nil)
        // Sparkle writes lastUpdateCheckDate asynchronously after the check
        // completes. Re-read it shortly after so Settings shows a fresh value
        // when the user dismisses the "no updates" sheet.
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 1_000_000_000)
            self?.refresh()
        }
    }

    /// Pull the current `lastUpdateCheckDate` from Sparkle. Call from
    /// `SettingsView.onAppear` so the displayed value is fresh whenever
    /// the user opens the panel.
    func refresh() {
        let current = controller.updater.lastUpdateCheckDate
        if current != lastUpdateCheckDate {
            lastUpdateCheckDate = current
        }
    }
}
