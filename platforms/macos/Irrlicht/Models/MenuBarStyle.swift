import Foundation

/// Which content the NSStatusItem icon renders (issue #909): the classic
/// per-project session-state dots, the subscription quota mini-bars, or
/// both side by side. Stored in @AppStorage("menuBarStyle"); defaults to
/// `.lights` so existing users see no visual change until they opt in.
///
/// **WHAT the icon shows, and nothing else.** How densely it shows it is an
/// independent choice — `MenuBarAppearance.grouping` (issue #1852, reshaped by
/// #1955). #1849 briefly made density a fourth case here, which forced the two
/// choices into one enum: "narrow" was then reachable in exactly one
/// combination (aggregate dots, no quota bars), and the crowded-menu-bar user
/// who wanted quota bars *and* a narrow icon had nothing to pick at all.
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
    /// `MenuBarAppearance.migrateLegacyCompactSetting`, which runs at launch
    /// to carry it over before anything reads this.
    ///
    /// Takes the store rather than offering a `.standard` convenience: the
    /// render path reads `MenuBarAppearance.current`, which needs both keys,
    /// and a no-argument overload here had no callers left after #1852.
    static func current(in defaults: UserDefaults) -> MenuBarStyle {
        let raw = defaults.string(forKey: storageKey) ?? ""
        return MenuBarStyle(rawValue: raw) ?? .lights
    }
}

/// How the icon BUCKETS the session dots (issue #1955) — the choice that
/// replaced #1852's `menuBarCompact` Bool.
///
/// **Grouping selects the bucket KEY, and nothing else.** One rule per case:
/// `project` keys on `projectName ?? cwd` (what the icon has always done),
/// `location` keys on the owning daemon, `combined` keys on a constant so
/// every session lands in one bucket. That is what makes the removed Compact
/// toggle a *point* in this space rather than a fourth control: combined
/// grouping with a one-slot budget IS what compact rendered.
///
/// **`location` is declared but not implemented in this build.** #1955's
/// per-daemon buckets are phase 2 of that issue and land after #1954, which is
/// what makes bucket truncation stable; until then `resolved` sends it to
/// `project`. The case is declared now rather than added later so the stored
/// format and the migration are final — a user who lands on this build from a
/// later one still parses their choice instead of silently resetting.
/// `selectableCases` is what Settings offers, so nothing unimplemented is
/// reachable from the UI.
enum MenuBarGrouping: String, CaseIterable, Identifiable {
    case project
    case location
    case combined

    var id: String { rawValue }

    var label: String {
        switch self {
        case .project: return "By project"
        case .location: return "By location"
        case .combined: return "Combined"
        }
    }

    /// Whether this build has a bucket rule for this case.
    ///
    /// An exhaustive `switch` rather than a stored list, for the reason
    /// `MenuBarStatusRenderer.segmentOrder` gives (#1797): a list is the one
    /// enum reader the compiler cannot force to be exhaustive, so a fourth
    /// grouping added next year would default to "implemented" and render as
    /// whatever `resolved` happened to fall through to.
    var isImplemented: Bool {
        switch self {
        case .project, .combined: return true
        case .location: return false
        }
    }

    /// The grouping actually used for bucketing. An unimplemented case falls
    /// back to `project`, which is the pre-#1955 behaviour and therefore the
    /// only fallback that cannot surprise anyone.
    var resolved: MenuBarGrouping { isImplemented ? self : .project }

    /// The cases Settings offers. Derived, so implementing `location` in
    /// phase 2 surfaces its segment without a second edit here.
    static var selectableCases: [MenuBarGrouping] { allCases.filter(\.isImplemented) }

    static let storageKey = "menuBarGrouping"

    /// Read the persisted grouping. Falls back to `project` for an unset or
    /// unparseable value, matching `MenuBarStyle.current`'s fallback shape —
    /// which is also the pre-#1955 rendering, so an install that never opts in
    /// is untouched.
    static func current(in defaults: UserDefaults) -> MenuBarGrouping {
        let raw = defaults.string(forKey: storageKey) ?? ""
        return MenuBarGrouping(rawValue: raw) ?? .project
    }
}

/// How many dot-groups the icon draws before the rest collapse into one `…`
/// (issue #1955) — the number that replaced the hardcoded
/// `MenuBarStatusRenderer.maxVisibleGroups = 5`.
///
/// **A whole-icon slot budget, not a per-bucket one**, and bounded 1–10 with a
/// default of 5. 5 is what shipped hardcoded, so the default renders
/// byte-identically. The ceiling exists because the setting can otherwise
/// produce an icon the menu bar silently truncates: the widest a slot gets is
/// `aggregateRender`'s dot-plus-count, so the worst case grows linearly in the
/// budget. `MenuBarAppearanceTests.testTheSlotBudgetStaysInsideTheIconsWidth`
/// computes that worst case by READING `MenuBarStatusRenderer`'s own constants
/// and fails if raising this ceiling pushes it past the stated budget — the
/// mutants that prove it fires are committed alongside it.
enum MenuBarMaxProjects {
    static let storageKey = "menuBarMaxProjects"

    static let minimum = 1
    static let maximum = 10
    /// What `MenuBarStatusRenderer` hardcoded before #1955.
    static let defaultValue = 5

    static func clamp(_ value: Int) -> Int {
        Swift.min(Swift.max(value, minimum), maximum)
    }

    /// Read the persisted budget, clamped.
    ///
    /// Absence is checked with `object(forKey:)` rather than treating
    /// `integer(forKey:)`'s `0` as "unset": `0` is also what a corrupted or
    /// hand-edited plist stores, and the two must not answer differently from
    /// each other by accident. Both land on `defaultValue` here, but they land
    /// there for stated reasons — absent means "never chosen", `0` is out of
    /// range and gets clamped to `minimum`.
    static func current(in defaults: UserDefaults) -> Int {
        guard defaults.object(forKey: storageKey) != nil else { return defaultValue }
        return clamp(defaults.integer(forKey: storageKey))
    }
}

/// What the menu bar icon renders, as the choices it actually is (#1852,
/// reshaped by #1955): `style` — WHAT is shown — plus `grouping` and
/// `maxProjects`, which together say HOW DENSELY.
///
/// **Why the four predicates live here rather than on `MenuBarStyle`.** What
/// the icon renders is now a function of every field. Answering half the
/// questions on the enum and half here would rebuild, one layer down, exactly
/// the conflation #1852 removed. Moving all four also turns every existing
/// `style.showsQuotaBars`-shaped call site into a compile error instead of a
/// silent behaviour change — and in a refactor the risk sits at the call
/// sites, not at the definitions.
///
/// Every predicate `switch`es exhaustively rather than comparing against a
/// case, which is the reasoning `MenuBarStyle` carried before #1852 and
/// `MenuBarStatusRenderer.segmentOrder` gives for deriving from `allCases`
/// (#1797): a comparison silently answers for a case that did not exist when
/// it was written. Since #1955 that applies to `aggregatesSessionDots` too —
/// it used to BE the `isCompact` Bool and needed no switch; it is now a
/// question about the grouping, and a fourth grouping must not be able to
/// answer it by falling through.
struct MenuBarAppearance: Equatable {
    /// Which content the icon carries.
    var style: MenuBarStyle
    /// How its session dots are bucketed.
    var grouping: MenuBarGrouping
    /// How many buckets it draws before the rest collapse into one `…`.
    /// Always inside `MenuBarMaxProjects`' bounds — the initializer clamps, so
    /// an out-of-range budget is unrepresentable rather than merely unwritten.
    private(set) var maxProjects: Int

    init(
        style: MenuBarStyle,
        grouping: MenuBarGrouping,
        maxProjects: Int = MenuBarMaxProjects.defaultValue
    ) {
        self.style = style
        self.grouping = grouping
        self.maxProjects = MenuBarMaxProjects.clamp(maxProjects)
    }

    // MARK: - What this appearance renders

    /// Whether the icon carries the subscription quota bars at all.
    ///
    /// A `style` question only: the grouping changes how densely the bars are
    /// drawn, never whether they are drawn. `lights` + `combined` is precisely
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
    /// concept than this type's density, and the thing the grouping selects).
    ///
    /// **`combined` STYLE is narrow whatever the grouping is, and that is
    /// permanent, not a temporary shim.** The reason is structural: it draws
    /// two things in one menu-bar-width budget, so its quota half has been the
    /// narrow layout since #909 and would have to be narrow whatever the
    /// density said. Compatibility follows from that rather than causing it —
    /// making narrowness track density would *widen* every existing Combined
    /// user's icon on the first launch after upgrading, which #1845's
    /// acceptance criterion 4 forbids and #1852 restates as its one hard
    /// constraint. So on the `combined` STYLE the grouping aggregates the dots
    /// and leaves the quota half alone; `usage` is where it selects the narrow
    /// layout `combined` was already using.
    ///
    /// The `.usage` arm returns `aggregatesSessionDots` rather than repeating
    /// its switch: at #1852 both arms read the same `isCompact` Bool, and
    /// #1955 replaced that Bool with a derivation — writing the derivation
    /// twice is how the two would drift.
    var usesNarrowQuotaBars: Bool {
        switch style {
        case .lights: return false
        case .usage: return aggregatesSessionDots
        case .combined: return true
        }
    }

    /// Whether the session dots collapse into ONE aggregate dot spanning every
    /// project, instead of one dot-group per bucket. This is the density
    /// signal #1955 derives from the grouping, and it means the same thing on
    /// every style that draws dots — which is the point #1852 established and
    /// #1955 keeps.
    var aggregatesSessionDots: Bool {
        switch grouping.resolved {
        case .project, .location: return false
        case .combined: return true
        }
    }

    /// Whether the dots give way to the quota bars when those are renderable.
    /// Only `.usage` does; see `MenuBarImageBuilder.shouldShowDotsInUsageStyle`
    /// for the two conditions that bring them back anyway. A `style` question
    /// only — how the dots are bucketed does not change whether they yield.
    var hidesDotsWhenQuotaIsRenderable: Bool {
        switch style {
        case .lights, .combined: return false
        case .usage: return true
        }
    }

    // MARK: - Persistence

    /// #1852's density Bool. Removed as a *setting* by #1955 — the grouping
    /// and the slot budget replaced it — and kept here only as the key
    /// `migrateLegacyCompactSetting` reads. Nothing renders from it.
    static let legacyCompactStorageKey = "menuBarCompact"

    static var current: MenuBarAppearance {
        current(in: .standard)
    }

    static func current(in defaults: UserDefaults) -> MenuBarAppearance {
        MenuBarAppearance(
            style: MenuBarStyle.current(in: defaults),
            grouping: MenuBarGrouping.current(in: defaults),
            maxProjects: MenuBarMaxProjects.current(in: defaults)
        )
    }

    // MARK: - Migration off the two shapes "compact" has had

    /// The raw `menuBarStyle` value #1849 persisted for its Compact style.
    static let legacyCompactStyleRawValue = "compact"

    /// Carry a user who asked for a compact icon — in EITHER of the two shapes
    /// that choice has been stored in — over to the grouping-plus-budget pair
    /// that replaced it, once.
    ///
    /// Two sources, because there are two eras of install:
    /// - `menuBarStyle == "compact"` — #1849's fourth style, on a machine that
    ///   never launched a #1852 build. That raw value does not parse here, so
    ///   without this `MenuBarStyle.current` falls back to `.lights` with the
    ///   default budget and silently widens the icon of the one group of users
    ///   who had explicitly asked for a narrow one. It is rewritten to
    ///   `lights`, which is destructive and has to be: the legacy value lives
    ///   in the same key the new value must occupy.
    /// - `menuBarCompact == true` — #1852's density Bool, which #1955 removes
    ///   as a setting. That key lives in a DIFFERENT key from the ones written
    ///   here, so nothing forces an overwrite and it is left in place —
    ///   `MenuBarStatusItemIdentity.migrateLegacyPreferredPosition`'s
    ///   non-destructive shape. A downgrade to a #1852-era build then still
    ///   finds the compact flag it expects.
    ///
    /// **The carry-over is exact, not approximate.** `combined` grouping with
    /// a one-slot budget answers all four predicates above the way `.compact`
    /// did at #1849's merge and the way `lights` + `isCompact` did at #1852's,
    /// AND routes through the same `aggregateRender` path — which matters for
    /// a group of 3 or fewer sessions, where `renderGroup` would have drawn
    /// overlapping plain dots instead of the pie-plus-count. That claim is
    /// re-run rather than only asserted here:
    /// `MenuBarAppearanceTests.testMigratedCompactUsersKeepTheirRendering`
    /// drives this migration and renders what falls out, at 2 and 5 sessions.
    ///
    /// Guarded on "has the new grouping key been written yet", so it fires at
    /// most once and never overwrites a grouping the user chose. That is the
    /// guard shape rather than #1852's "does the legacy value still read
    /// compact", because the source and target keys are now different: with
    /// two sources and two targets there is no single write whose absence
    /// means "not yet migrated".
    ///
    /// Takes the store for the same reason
    /// `MenuBarStatusItemIdentity.migrateLegacyPreferredPosition` does.
    ///
    /// - Returns: whether a choice was actually carried over, so a caller (or
    ///   a test) can tell "migrated" apart from "nothing to migrate".
    @discardableResult
    static func migrateLegacyCompactSetting(in defaults: UserDefaults) -> Bool {
        guard defaults.object(forKey: MenuBarGrouping.storageKey) == nil else { return false }

        let hadCompactStyle = defaults.string(forKey: MenuBarStyle.storageKey)
            == legacyCompactStyleRawValue
        let hadCompactModifier = defaults.bool(forKey: legacyCompactStorageKey)
        guard hadCompactStyle || hadCompactModifier else { return false }

        if hadCompactStyle {
            defaults.set(MenuBarStyle.lights.rawValue, forKey: MenuBarStyle.storageKey)
        }
        defaults.set(MenuBarGrouping.combined.rawValue, forKey: MenuBarGrouping.storageKey)
        defaults.set(MenuBarMaxProjects.minimum, forKey: MenuBarMaxProjects.storageKey)
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
    var quotaProviders: String
    var quotaVisual: String

    static func current(in defaults: UserDefaults) -> MenuBarIconSettings {
        MenuBarIconSettings(
            appearance: MenuBarAppearance.current(in: defaults),
            quotaProviders: defaults.string(forKey: MenuBarQuotaProviders.storageKey) ?? "",
            quotaVisual: defaults.string(forKey: QuotaVisualStyle.storageKey) ?? ""
        )
    }
}

/// #909's single quota provider. Removed as a *setting* by #1955 — the ordered
/// list below replaced it — and kept here only as the key
/// `MenuBarQuotaProviders.migrateLegacySingleProvider` reads. Nothing renders
/// from it.
enum MenuBarQuotaProvider {
    static let storageKey = "menuBarQuotaProvider"
}

/// Which subscription providers' quotas render in the Usage/Combined menu bar
/// styles, in the order the user put them (issue #1955). One quota slot per
/// entry; an empty list keeps #909's behaviour, where the freshest snapshot
/// across every provider wins.
///
/// **Stored as a delimited `String`, not a `[String]`, and that is forced
/// rather than chosen.** `[String]` is not an `@AppStorage` type, and
/// `PersistentDefaultsLintTests` forbids a `UserDefaults.standard` receiver
/// anywhere under `Irrlicht/Views/` — so the Settings multi-select has no way
/// to reach an array-shaped default.
///
/// The delimiter is a comma, and the limit that choice carries is stated
/// rather than implied: a provider key containing a comma would split into
/// two. Read at `a680d5ea`, the keys this list can hold are
/// `RateLimitInfo.providerKey(adapter:)`'s `"anthropic"` / `"openai"` and
/// `SettingsView.knownQuotaProviderKeys`' `"unknown:<adapter>"` fallback,
/// whose tail is a daemon-supplied adapter name — so the *shipped* keys carry
/// no comma, but a future adapter name is not something this type can enforce.
/// What is enforced is that everything else about the encoding round-trips:
/// `MenuBarAppearanceTests.testProviderKeysSurviveAnEncodeDecodeRoundTrip`
/// generates keys over the punctuation adapter names actually use and pins
/// both the round trip and the degradation of a malformed raw value.
///
/// **The quota half needs no numeric cap**, unlike the dot half's slot budget:
/// it can only grow by the user adding an entry to a list whose candidates are
/// the providers that actually have live snapshots
/// (`SettingsView.knownQuotaProviderKeys`), rather than growing on its own as
/// projects accumulate.
enum MenuBarQuotaProviders {
    static let storageKey = "menuBarQuotaProviders"

    static let separator = ","

    static func encode(_ keys: [String]) -> String {
        keys.joined(separator: separator)
    }

    /// Empty and whitespace-only components are dropped, and repeats are
    /// collapsed to their FIRST occurrence — so a trailing delimiter or a
    /// hand-edited plist cannot produce a slot that asks
    /// `QuotaMenuBarRenderer` for the provider named "", nor two slots drawing
    /// the same subscription twice.
    ///
    /// **The de-duplication is not defensive tidying, it is what makes the
    /// Settings list renderable.** `SettingsView.quotaProviderRows` feeds this
    /// list straight into a `ForEach(id: \.self)`, and SwiftUI's contract
    /// there is that ids are unique — a repeat gives "undefined results" and a
    /// logged warning rather than a second row. The write path cannot produce
    /// one (`quotaProviderSelection` removes before it appends), so the only
    /// source is a store edited from outside the app, which is exactly the
    /// case the empty-component filter above already exists for.
    static func decode(_ raw: String) -> [String] {
        var seen = Set<String>()
        return raw.split(separator: Character(separator))
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && seen.insert($0).inserted }
    }

    static func current(in defaults: UserDefaults) -> [String] {
        decode(defaults.string(forKey: storageKey) ?? "")
    }

    /// Carry #909's single provider choice into the ordered list, once.
    ///
    /// Non-destructive: the legacy key is a DIFFERENT key from the new one, so
    /// nothing forces an overwrite and it is left in place — the same
    /// structural reason `MenuBarStatusItemIdentity.migrateLegacyPreferredPosition`
    /// leaves its legacy position readable, and a downgrade to a #909-era
    /// build still finds the single provider it expects.
    ///
    /// The guard is "has the new key been written yet", not "is the list
    /// empty": an empty list is a legitimate choice (it means Auto), and a
    /// guard on emptiness would silently re-add the legacy provider every
    /// launch after the user removed it.
    ///
    /// - Returns: whether a choice was actually carried over.
    @discardableResult
    static func migrateLegacySingleProvider(in defaults: UserDefaults) -> Bool {
        guard defaults.object(forKey: storageKey) == nil else { return false }
        let legacy = (defaults.string(forKey: MenuBarQuotaProvider.storageKey) ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard !legacy.isEmpty else { return false }

        defaults.set(encode([legacy]), forKey: storageKey)
        return true
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
