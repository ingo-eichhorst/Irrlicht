import SwiftUI

/// The wall clock date-dependent rendering resolves "now" through (#1663).
///
/// ## Why this exists at all
///
/// It is the third member of `SessionListView.formatResetTime`'s machine
/// reads, and the only one where pinning the other two would have made the
/// site *look* covered:
///
///     let cal = Calendar.current          // machine
///     let now = Date()                    // wall clock  ← this key
///     let f = DateFormatter()             // locale + timeZone both unpinned
///     if cal.isDate(date, inSameDayAs: now) { … } else { f.dateFormat = "EEE H:mm" }
///
/// `\.formatTimeZone` (#1659) and `\.formatLocale` (#1630) reach the formatter.
/// They do not reach the `if`. `Date()` there does not merely shift the
/// rendered time — it selects a different **format string**, so one input
/// renders `"9:00"` before midnight and `"Fri 9:00"` after. A reference PNG
/// pinned on zone and locale alone is still a picture of the day it was
/// recorded, and it fails for everybody — including the machine that recorded
/// it — the next morning. That is why #1659 left this site whole rather than
/// half-covering it.
///
/// ## Why the default costs the app nothing
///
/// `FormatNow.wallClock` calls `Date()` at the point of use, inside `body`,
/// which is where the call sites called it before. So the shipping app reads
/// the same clock at the same moment; the seam is invisible until something
/// sets the key, and nothing in the app does.
/// `PinnedNowSnapshotTests.testTheDefaultIsIndistinguishableFromTheWallClockItReplaced`
/// asserts that rather than leaving it as a claim.
///
/// ## Why a closure and not a `Date`
///
/// Two reasons, and the second is the load-bearing one.
///
/// A `Date` whose `EnvironmentKey.defaultValue` is the computed `Date()` is a
/// value that is **never equal to itself**. That is the obvious mirror of
/// `\.formatTimeZone`'s computed `NSTimeZone.default` default and it is not the
/// same thing: that one is stable between assignments, this one changes at
/// every read, and SwiftUI compares environment values it has handed out.
/// `FormatNow.wallClock` is a single stored value, so the environment a view
/// sees is byte-identical from render to render exactly as it was before this
/// key existed.
///
/// And a fixture wants ONE instant, not a fresh one per read: two reads of a
/// computed-`Date` default inside one `body` disagree by microseconds, which is
/// the hazard `SessionListView.quotaWindowRow` already documents for
/// `quotaPacePercent`. `FormatNow(fixed:)` returns the same instant however
/// many times the subtree asks, which is what makes the same-day branch
/// decidable from a fixture at all.
///
/// ## Blast radius — deliberately two call sites, not an app-wide clock
///
/// #1663 says an injectable clock across every wall-clock read in the app is a
/// separate, wider change, and this is not it. Exactly two call sites read this
/// key: `QuotaResetLabel` (the `"resets …"` text) and `SessionListView`'s quota
/// tooltip, i.e. the two functions #1663 names. Everything else in
/// `SessionListView` that touches the wall clock still touches it directly —
/// `quotaPacePercent`, `formatTimeUntil`, and `mergeIntoBuckets`' staleness
/// test — because none of those SELECTS A FORMAT, which is the property that
/// made this one impossible to close from the test side. Two of the three are
/// pixel-visible — the pace marker's position and the stale chip's opacity — so
/// they are a real obstacle to the rate-limit snapshot fixture #1663
/// anticipates, and they are filed as #1675 rather than folded in here.
struct FormatNow {
    private let read: () -> Date

    /// A clock that answers with whatever `read` returns.
    init(_ read: @escaping () -> Date) { self.read = read }

    /// A clock stopped at `instant` — the fixture form. Every read in the
    /// subtree answers the same instant, so a rendering is a function of its
    /// inputs.
    init(fixed instant: Date) { self.init { instant } }

    /// Read the clock. Spelled as a call (`formatNow()`) rather than a
    /// property so the wall-clock read is visible at the site that performs
    /// it, the way `Date()` was.
    func callAsFunction() -> Date { read() }

    /// The system wall clock: `Date()`, evaluated per read. By construction
    /// what the call sites this key replaced already did.
    static let wallClock = FormatNow { Date() }
}

private struct FormatNowKey: EnvironmentKey {
    /// The clock the code this key replaced already read. Overriding it is a
    /// deliberate act; nothing in the app does it, and the snapshot host does.
    static let defaultValue = FormatNow.wallClock
}

extension EnvironmentValues {
    /// The clock date rendering in this subtree resolves "now" from.
    ///
    /// Read it and call it (`QuotaResetFormat.resetLabel(d, now: formatNow(), …)`) —
    /// like `\.formatTimeZone` and `\.formatLocale`, nothing consults this
    /// implicitly.
    var formatNow: FormatNow {
        get { self[FormatNowKey.self] }
        set { self[FormatNowKey.self] = newValue }
    }
}
