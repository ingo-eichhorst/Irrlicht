import SwiftUI

/// The locale `FormatStyle`-based rendering resolves through (#1630).
///
/// ## Why this exists at all
///
/// `TextField(_:value:format:)` **ignores `\.locale`**. That was measured, not
/// assumed: rendering the rule editor #1874 deleted inside an `NSHostingView`
/// under `.environment(\.locale, Locale(identifier: "en_US"))` on a `de_DE` Mac
/// left its threshold field reading `150.000`, while `@Environment(\.locale)`
/// read from inside the very same subtree reported `en_US`. The environment
/// arrives; the format style does not consult it. `format: .number` is
/// `FloatingPointFormatStyle<Double>()`, whose `locale` property defaults to
/// `Locale.autoupdatingCurrent` — the *process's* locale, which no view
/// modifier can reach.
///
/// The consequence was #1630: that view's committed reference PNG was a
/// picture of the recording contributor's regional settings (`150.000`), and a
/// `macos-latest` runner — or any contributor on a `,`-grouping locale —
/// rendered `150,000` and failed with a message that said nothing about why.
/// There was no test-only fix for it, because nothing in the test's reach is
/// what the format style reads. This key is the seam.
///
/// ## What #1874 changed, and what it did not
///
/// #1874 removed that editor, and with it the only view in the app that
/// rendered a `FormatStyle` — so **no committed reference is locale-dependent
/// today, and the number path this key was built for is exercised by nothing.**
/// The key stays because its surviving consumers read it and pass it
/// explicitly (`SessionListView`, `QuotaResetFormat`, `HistoryView`), and
/// because the AppKit fact above did not stop being true. What *is* gone is
/// the lock: whoever next renders a number through a `FormatStyle` must
/// re-establish one, or the picture-of-the-machine defect returns unobserved.
/// `docs/swift-testing.md` carries that obligation in full.
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
/// `.number` already is. The evidence is that #1630 regenerated **no**
/// reference PNG, and neither did #1874 for any reference it kept: every
/// surviving committed snapshot still matches byte-for-byte on the host that
/// recorded it. (`PinnedLocaleSnapshotTests` asserted the same thing directly
/// until #1874; it drove the deleted rule editor, so it retired with it rather
/// than being re-pointed at a view invented to keep it alive — #1390's lesson.)
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
