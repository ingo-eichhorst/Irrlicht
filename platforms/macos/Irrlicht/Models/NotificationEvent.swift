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

    /// Filename used when a custom audio file is installed into
    /// `~/Library/Sounds/` — UNNotificationSound resolves by basename there.
    var customFilename: String {
        "IrrlichtCustom-\(rawValue).caf"
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
