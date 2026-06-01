import Foundation
import ServiceManagement
import os

/// Thin wrapper around `SMAppService.mainApp` for registering Irrlicht as a
/// login item. The app opts the user in on first launch and, on every launch
/// thereafter, reconciles the system's real registration state against the
/// user's stored preference — so a registration that failed or got stranded on
/// a translocated path heals itself once the app runs from a stable location.
enum LoginItemManager {
    private static let logger = Logger(subsystem: "io.irrlicht.app", category: "LoginItemManager")

    private static let launchAtLoginKey = "launchAtLogin"
    private static let didApplyDefaultKey = "didApplyDefaultLoginItem"

    /// The system's ground-truth registration state, for the UI to reflect.
    static var status: SMAppService.Status { SMAppService.mainApp.status }

    /// Open System Settings → General → Login Items so the user can approve a
    /// `.requiresApproval` item (only the user can clear that state).
    static func openLoginItemsSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }

    /// What `reconcileOnLaunch` should do given the user's stored preference
    /// and the system's current status. Pure so it can be unit-tested without
    /// touching launchd.
    ///
    /// `.requiresApproval` with the preference on still maps to `.register`:
    /// it's harmless (re-registering a pending item is a no-op the system
    /// tolerates) and documents the intent that we keep trying to be enabled;
    /// the UI is what nudges the user to approve it.
    enum Action { case register, unregister, none }

    static func reconcileAction(prefersOn: Bool, status: SMAppService.Status) -> Action {
        if prefersOn {
            return status == .enabled ? .none : .register
        } else {
            return status == .enabled ? .unregister : .none
        }
    }

    /// Called once during `applicationDidFinishLaunching`. On the first ever
    /// launch we leave the seeded `launchAtLogin = true` in place (opt-in by
    /// default) and flip the gate so we never re-assert that default over a
    /// later opt-out. On every launch we then reconcile the live system status
    /// against the stored preference, re-registering if a previous attempt
    /// failed or registered a now-stale (e.g. translocated) path.
    static func reconcileOnLaunch() {
        let defaults = UserDefaults.standard
        if !defaults.bool(forKey: didApplyDefaultKey) {
            defaults.set(true, forKey: didApplyDefaultKey)
        }

        let prefersOn = defaults.bool(forKey: launchAtLoginKey)
        switch reconcileAction(prefersOn: prefersOn, status: SMAppService.mainApp.status) {
        case .register:
            apply(register: true, context: "launch reconcile")
        case .unregister:
            apply(register: false, context: "launch reconcile")
        case .none:
            break
        }
    }

    /// Bound to the Preferences toggle. The `SMAppService` call talks to
    /// launchd over XPC, so run it off the main actor — keeps the switch
    /// animation smooth on slower Macs. Errors (typically unsigned debug
    /// builds) are logged, never surfaced in the UI.
    static func setEnabled(_ enabled: Bool) {
        Task.detached(priority: .userInitiated) {
            apply(register: enabled, context: "toggle")
        }
    }

    private static func apply(register: Bool, context: String) {
        do {
            if register {
                try SMAppService.mainApp.register()
                logger.info("Registered as login item (\(context, privacy: .public))")
            } else {
                try SMAppService.mainApp.unregister()
                logger.info("Unregistered as login item (\(context, privacy: .public))")
            }
        } catch {
            logger.error("\(context, privacy: .public) \(register ? "register" : "unregister", privacy: .public)() failed: \(error.localizedDescription, privacy: .public)")
        }
    }
}
