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
    private var pollTask: Task<Void, Never>?

    init() {
        controller = SPUStandardUpdaterController(
            startingUpdater: true,
            updaterDelegate: nil,
            userDriverDelegate: nil
        )
        automaticallyChecksForUpdates = controller.updater.automaticallyChecksForUpdates
        lastUpdateCheckDate = controller.updater.lastUpdateCheckDate
        logger.info("Sparkle updater started (auto checks: \(self.automaticallyChecksForUpdates, privacy: .public))")
        startPollingLastCheckDate()
    }

    deinit {
        pollTask?.cancel()
    }

    func checkForUpdates() {
        controller.checkForUpdates(nil)
    }

    /// Sparkle does not publish KVO notifications for `lastUpdateCheckDate`,
    /// so refresh the mirror periodically to keep the Settings UI in sync.
    private func startPollingLastCheckDate() {
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self else { return }
                let current = self.controller.updater.lastUpdateCheckDate
                if current != self.lastUpdateCheckDate {
                    self.lastUpdateCheckDate = current
                }
            }
        }
    }
}
