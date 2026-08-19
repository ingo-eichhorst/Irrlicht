import SwiftUI

/// The locale `FormatStyle`-based rendering resolves through (#1630).
///
/// ## Why this exists at all
///
/// `TextField(_:value:format:)` **ignores `\.locale`**. That is measured, not
/// assumed: rendering `BackchannelRulesView` inside an `NSHostingView` under
/// `.environment(\.locale, Locale(identifier: "en_US"))` on a `de_DE` Mac
/// leaves the threshold field reading `150.000`, while `@Environment(\.locale)`
/// read from inside the very same subtree reports `en_US`. The environment
/// arrives; the format style does not consult it. `format: .number` is
/// `FloatingPointFormatStyle<Double>()`, whose `locale` property defaults to
/// `Locale.autoupdatingCurrent` — the *process's* locale, which no view
/// modifier can reach.
///
/// The consequence was #1630: the committed reference PNG for
/// `BackchannelRulesViewSnapshotTests.testBackchannelRuleContextTokens` is a
/// picture of the recording contributor's regional settings (`150.000`), and a
/// `macos-latest` runner — or any contributor on a `,`-grouping locale —
/// renders `150,000` and fails with a message that says nothing about why.
/// There is no test-only fix for that, because there is nothing in the test's
/// reach that the format style reads. This key is the seam.
///
/// ## Why not `\.locale`
///
/// Because adopting `\.locale` here would be a **user-visible change of
/// unknown sign**, which is exactly what #1630 must not trade for snapshot
/// stability. SwiftUI derives `\.locale` from the bundle's localizations, not
/// from `Locale.autoupdatingCurrent`, so the two can disagree for a user whose
/// language and region differ — and the separator genuinely varies with that
/// resolution (measured on this host: `en_US` → `150,000`, `de_DE`/`en_DE` →
/// `150.000`, `fr_FR`/`en_FR` → `150 000`, `de_CH` → `150'000`, `hi_IN` →
/// `1,50,000`).
///
/// This key's default is `Locale.autoupdatingCurrent`, so
/// `.number.locale(formatLocale)` is *by construction* the same style a bare
/// `.number` already is — asserted in
/// `PinnedLocaleSnapshotTests.testDefaultIsIndistinguishableFromABareNumberStyle`,
/// and evidenced more strongly by the fact that #1630 regenerated **no**
/// reference PNG: every committed snapshot still matches byte-for-byte on the
/// host that recorded it.
///
/// Making this view honour `\.locale` may well be the right localisation
/// decision later. It is a separate decision, with a separate blast radius,
/// and it is not made here.
private struct FormatLocaleKey: EnvironmentKey {
    /// The locale a bare `format: .number` / `.percent` / `.currency` already
    /// resolves through. Overriding this key with anything else is a
    /// deliberate act; nothing in the app does it, and the snapshot suites do.
    static let defaultValue: Locale = .autoupdatingCurrent
}

extension EnvironmentValues {
    /// Locale for `FormatStyle`-based rendering in this subtree.
    ///
    /// Read it and pass it to the style (`.number.locale(formatLocale)`) —
    /// unlike `\.locale`, nothing consults this implicitly.
    var formatLocale: Locale {
        get { self[FormatLocaleKey.self] }
        set { self[FormatLocaleKey.self] = newValue }
    }
}
