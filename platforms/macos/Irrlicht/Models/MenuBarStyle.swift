import Foundation

/// Which content the NSStatusItem icon renders (issue #909): the classic
/// per-project session-state dots, the subscription quota mini-bars, or
/// both side by side. Stored in @AppStorage("menuBarStyle"); defaults to
/// `.lights` so existing users see no visual change until they opt in.
///
/// **WHAT the icon shows, and nothing else.** How densely it shows it is an
/// independent choice — `MenuBarAppearance.isCompact` (issue #1852). #1849
/// briefly made density a fourth case here, which forced the two choices into
/// one enum: "narrow" was then reachable in exactly one combination
/// (aggregate dots, no quota bars), and the crowded-menu-bar user who wanted
/// quota bars *and* a narrow icon had nothing to pick at all.
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
    ///
    /// `"compact"` — the raw value #1849's fourth case persisted — is one of
    /// those unparseable values, so it would land on `.lights` with the
    /// modifier OFF and silently discard the choice. See
    /// `MenuBarAppearance.migrateLegacyCompactStyle`, which runs at launch to
    /// carry it over before anything reads this.
    ///
    /// Takes the store rather than offering a `.standard` convenience: the
    /// render path reads `MenuBarAppearance.current`, which needs both keys,
    /// and a no-argument overload here had no callers left after #1852.
    static func current(in defaults: UserDefaults) -> MenuBarStyle {
        let raw = defaults.string(forKey: storageKey) ?? ""
        return MenuBarStyle(rawValue: raw) ?? .lights
    }
}

/// What the menu bar icon renders, as the two independent choices it actually
/// is (issue #1852): `style` — WHAT is shown — and `isCompact` — HOW DENSELY.
///
/// **Why the four predicates live here rather than on `MenuBarStyle`.** What
/// the icon renders is now a function of BOTH fields. Answering half the
/// questions on the enum and half here would rebuild, one layer down, exactly
/// the conflation this issue removes. Moving all four also turns every
/// existing `style.showsQuotaBars`-shaped call site into a compile error
/// instead of a silent behaviour change — and in a refactor the risk sits at
/// the call sites, not at the definitions.
///
/// The three `style`-dependent predicates still `switch` exhaustively over
/// `style` rather than comparing against a case, which is the reasoning
/// `MenuBarStyle` carried before #1852 and `MenuBarStatusRenderer.segmentOrder`
/// gives for deriving from `allCases` (#1797): a comparison silently answers
/// for a case that did not exist when it was written. `aggregatesSessionDots`
/// is the exception and needs no switch — it IS the modifier.
struct MenuBarAppearance: Equatable {
    /// Which content the icon carries.
    var style: MenuBarStyle
    /// Whether it is drawn in the narrow, space-saving form.
    var isCompact: Bool

    // MARK: - What this appearance renders

    /// Whether the icon carries the subscription quota bars at all.
    ///
    /// A `style` question only: `isCompact` changes how densely the bars are
    /// drawn, never whether they are drawn. `lights` + compact is precisely
    /// what #1849 shipped as the Compact *style* — one aggregate dot, no
    /// quota half.
    var showsQuotaBars: Bool {
        switch style {
        case .lights: return false
        case .usage, .combined: return true
        }
    }

    /// Whether those quota bars render in the narrow, label-less layout
    /// (`QuotaMenuBarRenderer`'s `compact:` flag — an older, lower-level
    /// concept than this type's `isCompact`, and the thing `isCompact`
    /// selects).
    ///
    /// **`combined` is narrow whether or not `isCompact` is set, and that is
    /// permanent, not a temporary shim.** The reason is structural: `combined`
    /// draws two things in one menu-bar-width budget, so its quota half has
    /// been the narrow layout since #909 and would have to be narrow whatever
    /// this modifier did. Compatibility follows from that rather than causing
    /// it — making narrowness track `isCompact` would *widen* every existing
    /// Combined user's icon on the first launch after upgrading, which #1845's
    /// acceptance criterion 4 forbids and #1852 restates as its one hard
    /// constraint. So on `combined`, `isCompact` aggregates the dots and leaves
    /// the quota half alone; `usage` is where it selects the narrow layout
    /// `combined` was already using.
    var usesNarrowQuotaBars: Bool {
        switch style {
        case .lights: return false
        case .usage: return isCompact
        case .combined: return true
        }
    }

    /// Whether the session dots collapse into ONE aggregate dot spanning every
    /// project, instead of one dot-group per project. This is what `isCompact`
    /// means for the dot half, and it means the same thing on every style that
    /// draws dots — which is the whole point of #1852.
    var aggregatesSessionDots: Bool { isCompact }

    /// Whether the dots give way to the quota bars when those are renderable.
    /// Only `.usage` does; see `MenuBarImageBuilder.shouldShowDotsInUsageStyle`
    /// for the two conditions that bring them back anyway. A `style` question
    /// only — compacting the dots does not change whether they yield.
    var hidesDotsWhenQuotaIsRenderable: Bool {
        switch style {
        case .lights, .combined: return false
        case .usage: return true
        }
    }

    // MARK: - Persistence

    static let compactStorageKey = "menuBarCompact"

    static var current: MenuBarAppearance {
        current(in: .standard)
    }

    static func current(in defaults: UserDefaults) -> MenuBarAppearance {
        MenuBarAppearance(
            style: MenuBarStyle.current(in: defaults),
            isCompact: defaults.bool(forKey: compactStorageKey)
        )
    }

    // MARK: - Migration off #1849's fourth case

    /// The raw `menuBarStyle` value #1849 persisted for its Compact style.
    static let legacyCompactStyleRawValue = "compact"

    /// Carry a user who picked #1849's Compact *style* over to the equivalent
    /// style-plus-modifier, once.
    ///
    /// Without it `MenuBarStyle.current` sees a raw value this build cannot
    /// parse, falls back to `.lights` with the modifier OFF, and silently
    /// widens the icon of the one group of users who had explicitly asked for
    /// a narrow one.
    ///
    /// **The carry-over is exact, not approximate.** `lights` + compact answers
    /// all four predicates above the way `.compact` did at #1849's merge —
    /// `showsQuotaBars` false, `usesNarrowQuotaBars` false,
    /// `aggregatesSessionDots` true, `hidesDotsWhenQuotaIsRenderable` false
    /// (`MenuBarStyle.swift:44/54/64/74` on that commit) — so the icon renders
    /// identically across the upgrade rather than merely similarly. That claim
    /// is re-run rather than only asserted here:
    /// `testMigratedCompactStyleUsersKeepTheirRendering` drives this migration
    /// and renders what falls out.
    ///
    /// Idempotent by construction: it fires only while the persisted style
    /// still reads `"compact"`, and rewriting that value is what stops it
    /// firing again. It therefore needs none of
    /// `migrateLegacyPreferredPosition`'s "has the new key already been
    /// written" guard — there is no second key whose absence means "not yet
    /// migrated".
    ///
    /// **Unlike that sibling, this one is destructive, and deliberately so.**
    /// `migrateLegacyPreferredPosition` leaves its legacy key in place so a
    /// downgrade still finds the position it expects. Here the legacy value
    /// lives in the *same* key the new value must occupy, so carrying it over
    /// means overwriting it. The consequence, traced: a user who upgrades and
    /// then downgrades to a `.compact`-era build lands on Lights with a wide
    /// icon, because that build cannot read `menuBarCompact`. Re-upgrading
    /// restores the compact rendering by itself — `menuBarCompact` is still
    /// `true` and the old build never touched it — so the choice is
    /// temporarily invisible on the older build rather than lost.
    ///
    /// Takes the store for the same reason
    /// `MenuBarStatusItemIdentity.migrateLegacyPreferredPosition` does.
    ///
    /// - Returns: whether a choice was actually carried over, so a caller (or
    ///   a test) can tell "migrated" apart from "nothing to migrate".
    @discardableResult
    static func migrateLegacyCompactStyle(in defaults: UserDefaults) -> Bool {
        guard defaults.string(forKey: MenuBarStyle.storageKey)
            == legacyCompactStyleRawValue else { return false }

        defaults.set(MenuBarStyle.lights.rawValue, forKey: MenuBarStyle.storageKey)
        defaults.set(true, forKey: compactStorageKey)
        return true
    }
}

/// Every stored default the menu bar icon's appearance depends on, read in one
/// place and compared as one value.
///
/// `MenuBarController` repaints the icon when one of these changes, and
/// `UserDefaults.didChangeNotification` fires for *any* write — so something
/// has to answer "did a menu-bar setting actually change". Before #1852 that
/// was three hand-listed reads compared against three hand-listed last-seen
/// fields, so adding a fourth setting meant editing four separate places and
/// forgetting any one of them left the icon stale until an unrelated event
/// repainted it, with nothing failing. Synthesised `Equatable` over one value
/// makes the comparison total by construction: a field added here is compared
/// whether or not anyone remembered to compare it.
///
/// Its limit, stated rather than implied: this is total over the fields it
/// HOLDS, not over every default the icon can depend on. `QuotaVisualStyle` is
/// read again deeper in the render path (`QuotaMenuBarRenderer`), and is
/// mirrored here by hand — so a fifth key introduced down there would still
/// need adding. The forget-to-add failure moved from four sites to one; it did
/// not disappear.
struct MenuBarIconSettings: Equatable {
    var appearance: MenuBarAppearance
    var quotaProvider: String
    var quotaVisual: String

    static func current(in defaults: UserDefaults) -> MenuBarIconSettings {
        MenuBarIconSettings(
            appearance: MenuBarAppearance.current(in: defaults),
            quotaProvider: defaults.string(forKey: MenuBarQuotaProvider.storageKey) ?? "",
            quotaVisual: defaults.string(forKey: QuotaVisualStyle.storageKey) ?? ""
        )
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
