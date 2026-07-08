import Foundation

/// Which content the NSStatusItem icon renders (issue #909): the classic
/// per-project session-state dots, the subscription quota mini-bars, or
/// both side by side. Stored in @AppStorage("menuBarStyle"); defaults to
/// `.lights` so existing users see no visual change until they opt in.
enum MenuBarStyle: String, CaseIterable, Identifiable {
    case lights
    case usage
    case combined

    var id: String { rawValue }

    var label: String {
        switch self {
        case .lights: return "Lights"
        case .usage: return "Usage"
        case .combined: return "Combined"
        }
    }

    static let storageKey = "menuBarStyle"

    /// Read the persisted style. Falls back to `.lights` for an unset or
    /// unparseable value, matching ProviderModePreference's fallback shape.
    static var current: MenuBarStyle {
        let raw = UserDefaults.standard.string(forKey: storageKey) ?? ""
        return MenuBarStyle(rawValue: raw) ?? .lights
    }
}

/// Which subscription provider's quota renders in the Usage/Combined menu
/// bar styles. A single fixed choice, not multi-provider — issue #909's
/// maintainer flagged that showing every subscription at once would crowd
/// an already-tight icon budget shared with the capped-at-5 dot groups.
/// Stored under @AppStorage("menuBarQuotaProvider"); empty means "not yet
/// chosen", and MenuBarImageBuilder falls back to the freshest provider
/// it finds.
enum MenuBarQuotaProvider {
    static let storageKey = "menuBarQuotaProvider"

    static var current: String? {
        let raw = UserDefaults.standard.string(forKey: storageKey) ?? ""
        return raw.isEmpty ? nil : raw
    }
}

/// How the quota portion of the icon renders when MenuBarStyle is `.usage`
/// or `.combined`: the stacked 5h/7d bars, or a single compact ring for the
/// most-imminent window (mirrors Claude Usage Tracker's "Compact" icon
/// style, requested alongside issue #909). Stored under
/// @AppStorage("menuBarQuotaVisual"); defaults to `.bars`.
enum QuotaVisualStyle: String, CaseIterable, Identifiable {
    case bars
    case circle

    var id: String { rawValue }

    var label: String {
        switch self {
        case .bars: return "Bars"
        case .circle: return "Circle"
        }
    }

    static let storageKey = "menuBarQuotaVisual"

    static var current: QuotaVisualStyle {
        let raw = UserDefaults.standard.string(forKey: storageKey) ?? ""
        return QuotaVisualStyle(rawValue: raw) ?? .bars
    }
}
