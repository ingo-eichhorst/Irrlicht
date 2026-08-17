import SwiftUI

/// The quota chip's two date renderings, as pure functions of their inputs
/// (#1663).
///
/// These were `SessionListView.formatResetTime` and `.formatClockTime`, two
/// `private func`s that between them stacked four machine reads:
/// `Calendar.current`, a wall-clock `Date()`, and a `DateFormatter` with
/// neither `locale` nor `timeZone` set. Every one of them is now a **required
/// argument**, which is #1659's shape for the same problem: a call site with
/// nothing to pass is `error: missing argument for parameter 'now' in call`
/// rather than another silent read of the machine.
///
/// The views get theirs from the environment — `\.formatNow` (#1663),
/// `\.calendar` (SwiftUI's own, default `Calendar.current`), `\.formatTimeZone`
/// (#1659, default `NSTimeZone.default`) and `\.formatLocale` (#1630, default
/// `Locale.autoupdatingCurrent`). Each of those defaults is, by construction,
/// what the unpinned spelling already resolved through, so no user sees a
/// different string; `PinnedNowSnapshotTests` asserts that against a verbatim
/// copy of the code these replaced rather than against a description of it.
///
/// Deliberately **not** cached, unlike `HistoryFormat`'s formatters. The code
/// this replaced built a fresh `DateFormatter` per call, so keeping that keeps
/// the change to what #1663 is about; a cache keyed by fewer axes than the
/// function takes is also exactly the hazard #1659 records.
enum QuotaResetFormat {

    /// Short clock time — the quota tooltip's "Projected cap: …" line.
    ///
    /// Was `SessionListView.formatClockTime`, whose `DateFormatter` set
    /// neither `locale` nor `timeZone`.
    static func clock(_ date: Date, timeZone: TimeZone, locale: Locale) -> String {
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = timeZone
        f.dateStyle = .none
        f.timeStyle = .short
        return f.string(from: date)
    }

    /// Compact reset label for the chip row. A reset on the same day as `now`
    /// renders as short clock time ("11:14"); a reset on any other day renders
    /// as "EEE H:mm" ("Fri 9:00"). Mirrors mockup 1's two spellings.
    ///
    /// Was `SessionListView.formatResetTime`. `now` is the argument #1663
    /// exists for: it does not shift the rendered time, it picks which of the
    /// two format strings above is used, so a site that reads it from the wall
    /// clock cannot be photographed by a snapshot on one day and compared on
    /// the next.
    ///
    /// `calendar` contributes its identity and week rules only — its **time
    /// zone is overridden** with `timeZone`, so the day the branch is decided
    /// in and the day the string is rendered in cannot disagree. That is not a
    /// user-visible change: `Calendar.current.timeZone` and `NSTimeZone.default`
    /// (the two arguments' respective defaults) are the same zone on any
    /// process that does not assign the latter, and nothing in the app does.
    /// It is also why there is no both-sides pixel arm for the calendar in
    /// `PinnedNowSnapshotTests`: with the zone forced, the identifier does not
    /// decide the branch — which that suite asserts across Foundation's
    /// calendars rather than assuming.
    static func resetLabel(_ date: Date,
                           now: Date,
                           calendar: Calendar,
                           timeZone: TimeZone,
                           locale: Locale) -> String {
        var cal = calendar
        cal.timeZone = timeZone

        let f = DateFormatter()
        f.locale = locale
        f.timeZone = timeZone
        if cal.isDate(date, inSameDayAs: now) {
            f.dateStyle = .none
            f.timeStyle = .short
        } else {
            f.dateFormat = "EEE H:mm"
        }
        return f.string(from: date)
    }
}

/// The `"resets …"` text of one quota window row.
///
/// A view of its own, rather than a `Text` inline in
/// `SessionListView.quotaWindowRow`, for one reason: it is the only way this
/// site can be rendered by a test. `quotaWindowRow` is a method on
/// `SessionListView`, so reaching it means hosting the whole panel with its
/// three environment objects and a `SessionState` carrying a rate-limit
/// snapshot — and an `@Environment` read from a `SessionListView` value a test
/// constructed itself, outside a view update, answers the DEFAULT, which is a
/// pin that reaches nothing wearing the shape of a passing test.
///
/// Hosted directly, its four environment reads resolve the way they do in the
/// app, so `PinnedNowSnapshotTests` can drive two values through one view per
/// axis and watch the pixels move.
///
/// The modifiers are the ones this text carried inline; no geometry changed,
/// and no committed reference renders this site at all (#1663 verified that,
/// and the 53-PNG set is unchanged by this extraction).
struct QuotaResetLabel: View {
    let resetsAt: Date

    @Environment(\.formatNow) private var formatNow
    @Environment(\.calendar) private var calendar
    @Environment(\.formatTimeZone) private var formatTimeZone
    @Environment(\.formatLocale) private var formatLocale

    var body: some View {
        Text("resets \(QuotaResetFormat.resetLabel(resetsAt, now: formatNow(), calendar: calendar, timeZone: formatTimeZone, locale: formatLocale))")
            .font(.system(size: 9, design: .monospaced))
            .foregroundColor(.secondary)
            .lineLimit(1)
            .truncationMode(.tail)
    }
}
