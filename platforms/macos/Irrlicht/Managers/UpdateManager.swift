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

    init() {
        controller = SPUStandardUpdaterController(
            startingUpdater: true,
            updaterDelegate: nil,
            userDriverDelegate: nil
        )
        automaticallyChecksForUpdates = controller.updater.automaticallyChecksForUpdates
        lastUpdateCheckDate = controller.updater.lastUpdateCheckDate
        logger.info("Sparkle updater started (auto checks: \(self.automaticallyChecksForUpdates, privacy: .public))")
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
