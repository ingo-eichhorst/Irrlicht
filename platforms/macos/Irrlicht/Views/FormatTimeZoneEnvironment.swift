import SwiftUI

/// The time zone `DateFormatter`-based rendering resolves through (#1659).
///
/// ## Why this exists at all
///
/// `HistoryFormat`'s cached formatters pinned `locale` to `en_US_POSIX` and
/// never set `timeZone`, so every date string the History panel draws came out
/// of `NSTimeZone.default` — the machine. Eight of the fourteen committed
/// `HistoryViewSnapshotTests` references contain that output, and they were
/// correct only because that one suite's `setUp` assigned
/// `NSTimeZone.default = UTC` and put it back in `tearDown`. A new suite hosting
/// `HistoryContentView` / `HistoryActivityContentView` /
/// `HistoryQuotaForecastView` without copying those seven lines would have
/// recorded the developer's own time zone into a reference.
///
/// This is the time-zone sibling of `\.formatLocale` (#1630), and it exists for
/// the same reason: the value was being read from the MACHINE where the
/// equivalent value was available from the INPUT. It is a separate key rather
/// than a second use of that one because the two are separately overridable —
/// a host may want POSIX-stable numbers and local wall-clock times, or the
/// reverse — and because they are read by different machinery (`FormatStyle`
/// vs `DateFormatter`).
///
/// ## Why the environment could NOT reach the formatters as they stood
///
/// `\.formatLocale` worked for the rule editor #1874 deleted because a
/// `FormatStyle` is *constructed per render*, inside `body`, so the view can
/// hand it a value it read from the environment. `HistoryFormat`'s formatters
/// were file-scope
/// `private static let DateFormatter`s: one object per process, built on first
/// touch, reachable from no view. Setting this key alone would therefore have
/// changed nothing — the vacuous-green shape #1630 measured for
/// `.environment(\.locale, …)`.
///
/// So the product change is not "add a key". It is that `HistoryFormat`'s
/// entry points now **require** a `TimeZone` argument, and the views that call
/// them read it from here. A new call site that has no zone to pass is a
/// compile error rather than a silent read of `NSTimeZone.default`.
///
/// ## Why the default is `NSTimeZone.default` and not `.autoupdatingCurrent`
///
/// Because `NSTimeZone.default` is, *by construction*, the value an unset
/// `DateFormatter.timeZone` already resolved through — the same
/// "by construction identical" bar `\.formatLocale`'s
/// `Locale.autoupdatingCurrent` default met. A user in Berlin still sees Berlin
/// times.
///
/// `TimeZone.autoupdatingCurrent` and `TimeZone.current` would NOT have met it.
/// Measured on this host (`Europe/Berlin`), after `NSTimeZone.default = UTC`:
///
///     TimeZone.current               = Europe/Berlin   (does NOT follow it)
///     TimeZone.autoupdatingCurrent   = Europe/Berlin   (does NOT follow it)
///     unset DateFormatter            = 22:13 = UTC     (DOES follow it)
///
/// so either of those two would have been a real behaviour change for any
/// process that assigns `NSTimeZone.default` — which is precisely the History
/// snapshot suite, and would have quietly broken the eight references this
/// change exists to protect.
///
/// `defaultValue` is a **computed** property for the same reason: read per
/// access, so it tracks a later assignment exactly the way the unset formatter
/// did (also measured — an unset `DateFormatter` created *before* the
/// assignment still rendered UTC after it, so "captured at first use" is not
/// the semantics these formatters had).
private struct FormatTimeZoneKey: EnvironmentKey {
    /// The time zone an unset `DateFormatter.timeZone` already resolves
    /// through. Overriding this key with anything else is a deliberate act;
    /// nothing in the app does it, and the snapshot hosts do.
    static var defaultValue: TimeZone { NSTimeZone.default }
}

extension EnvironmentValues {
    /// Time zone for `DateFormatter`-based rendering in this subtree.
    ///
    /// Read it and pass it to the formatter (`HistoryFormat.axisLabel(d,
    /// bucketSeconds: s, timeZone: formatTimeZone)`) — like `\.formatLocale`
    /// and unlike `\.locale`, nothing consults this implicitly.
    ///
    /// Hoist it into a local before using it inside an escaping builder
    /// closure (Swift Charts' `AxisValueLabel`, `ForEach`'s content): reading
    /// a property wrapper from a captured `self` outside `body` evaluation is
    /// what SwiftUI warns about, and the local is free.
    var formatTimeZone: TimeZone {
        get { self[FormatTimeZoneKey.self] }
        set { self[FormatTimeZoneKey.self] = newValue }
    }
}
