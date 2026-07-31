import Foundation

/// Categories of desktop notifications the app can fire. Each event has its
/// own enable toggle and its own sound choice stored in `UserDefaults`.
enum NotificationEvent: String, CaseIterable {
    case ready
    case waiting
    case contextPressure

    var displayName: String {
        switch self {
        case .ready: return "Agent ready"
        case .waiting: return "Agent waiting for input"
        case .contextPressure: return "Context pressure alert"
        }
    }

    var enabledKey: String {
        switch self {
        case .ready: return "notifyOnReady"
        case .waiting: return "notifyOnWaiting"
        case .contextPressure: return "notifyOnContextPressure"
        }
    }

    var soundKey: String {
        switch self {
        case .ready: return "soundOnReady"
        case .waiting: return "soundOnWaiting"
        case .contextPressure: return "soundOnContextPressure"
        }
    }

    /// Out-of-the-box sound for each event. New installs pick these up via
    /// `UserDefaults.register(defaults:)`; existing users keep whatever they
    /// already chose.
    var defaultSound: SoundChoice {
        switch self {
        case .ready: return .funk
        case .waiting: return .ping
        case .contextPressure: return .sosumi
        }
    }
}

/// The master "Enable notifications" gate (#940), made authoritative over the
/// firing path in #1183. Owns the key and the single rule for reading it, so
/// the Settings toggle and the code that decides whether to fire can't drift.
enum NotificationSettings {
    /// Deliberately NOT part of `SessionManager`'s `register(defaults:)` seed:
    /// a registered value would make `object(forKey:)` non-nil forever, which
    /// silently disables the upgrade fallback below.
    static let masterEnabledKey = "notificationsEnabled"

    /// Pure decision: is the master gate on? `master` is `nil` when the key has
    /// never been written — an install that predates the toggle and hasn't
    /// opened Settings since. That case falls back to the per-event OR, so an
    /// already-configured setup keeps firing instead of going quiet on upgrade.
    static func masterEnabled(master: Bool?, anyEventEnabled: Bool) -> Bool {
        master ?? anyEventEnabled
    }

    /// `UserDefaults`-reading wrapper, used by the firing path and by
    /// `SettingsView`'s one-time reconcile. Reads through `object(forKey:)`
    /// rather than `bool(forKey:)` — the latter flattens "absent" to `false`
    /// and would silence exactly the installs the fallback exists for.
    static func masterEnabled(defaults: UserDefaults = .standard) -> Bool {
        masterEnabled(
            master: defaults.object(forKey: masterEnabledKey) as? Bool,
            anyEventEnabled: NotificationEvent.allCases.contains { defaults.bool(forKey: $0.enabledKey) }
        )
    }
}
