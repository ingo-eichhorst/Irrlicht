import Foundation

/// Which content the NSStatusItem icon renders (issue #909): the classic
/// per-project session-state dots, the subscription quota mini-bars, or
/// both side by side. Stored in @AppStorage("menuBarStyle"); defaults to
/// `.lights` so existing users see no visual change until they opt in.
enum MenuBarStyle: String, CaseIterable, Identifiable {
    case lights
    case usage
    case combined
    /// One aggregate dot for every project at once, and no quota bars — the
    /// only style whose width does not grow with the number of projects
    /// (issue #1845). Appended LAST on purpose: `allCases` drives the
    /// Settings segmented control's order, so a new case anywhere else would
    /// move the three segments an existing user already knows.
    case compact

    var id: String { rawValue }

    var label: String {
        switch self {
        case .lights: return "Lights"
        case .usage: return "Usage"
        case .combined: return "Combined"
        case .compact: return "Compact"
        }
    }

    // MARK: - What each style renders
    //
    // These three are `switch`es rather than the `style == .lights` /
    // `style == .combined` / `style != .usage` comparisons that used to live
    // in MenuBarImageBuilder, because a comparison silently answers for a
    // case that did not exist when it was written. Adding `.compact` (#1845)
    // had to answer three separate questions, and each one defaulted into
    // some other style's branch with nothing failing to build. A `switch`
    // over the enum is the one reader the Swift compiler forces to be
    // exhaustive — the same reasoning MenuBarStatusRenderer.segmentOrder
    // gives for deriving from `allCases` instead of hand-listing (#1797).

    /// Whether the icon carries the subscription quota bars at all.
    var showsQuotaBars: Bool {
        switch self {
        case .lights, .compact: return false
        case .usage, .combined: return true
        }
    }

    /// Whether those quota bars render in the narrow, label-less layout
    /// (`QuotaMenuBarRenderer`'s `compact:` flag — a different, older concept
    /// than the `.compact` *style*, which shows no quota bars at all).
    var usesNarrowQuotaBars: Bool {
        switch self {
        case .lights, .usage, .compact: return false
        case .combined: return true
        }
    }

    /// Whether the session dots collapse into ONE aggregate dot spanning
    /// every project, instead of one dot-group per project.
    var aggregatesSessionDots: Bool {
        switch self {
        case .lights, .usage, .combined: return false
        case .compact: return true
        }
    }

    /// Whether the dots give way to the quota bars when those are
    /// renderable. Only `.usage` does; see
    /// `MenuBarImageBuilder.shouldShowDotsInUsageStyle` for the two
    /// conditions that bring them back anyway.
    var hidesDotsWhenQuotaIsRenderable: Bool {
        switch self {
        case .lights, .combined, .compact: return false
        case .usage: return true
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
/// or `.combined`: the stacked provider-reported quota bars, or a single compact ring for the
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
