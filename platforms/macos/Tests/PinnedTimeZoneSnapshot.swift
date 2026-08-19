import Foundation

/// The time zone every image snapshot is rendered under, so a committed
/// reference is a picture of the VIEW and not of the recording machine's clock
/// (#1659).
///
/// ## What was wrong
///
/// `HistoryFormat`'s cached formatters pinned the locale and never set
/// `timeZone`, so `HH:mm` / `M/d` / `EEE h:mm a` all came out of
/// `NSTimeZone.default`. Eight of the fourteen committed
/// `HistoryViewSnapshotTests` references contain that output, and the only
/// thing keeping them right was that suite's own `setUp`:
///
///     originalTimeZone = NSTimeZone.default
///     NSTimeZone.default = TimeZone(identifier: "UTC")!
///
/// That is the third value these references were reading off the MACHINE where
/// it was available from the INPUT — after the backing scale
/// (`PinnedScaleSnapshot`, #1530) and the locale (`PinnedLocaleSnapshot`,
/// #1630) — and it was the one still held by seven lines a new suite has to
/// remember to copy. It is now held by `PinnedSnapshotHost`, whose only
/// initializer applies it, so a suite cannot host a view without it.
///
/// ## Why `UTC`
///
/// For the same reason `PinnedLocaleSnapshot.referenceLocale` is `de_DE` and
/// `PinnedScaleSnapshot.referenceScale` is `2`: **it is what the committed
/// references were recorded under**, so adopting it regenerates nothing.
/// `AGENTS.md` names re-recording a reference to make a test pass as the move
/// #1034 and #1044 both made and both got wrong.
///
/// This one is a stronger statement than the locale's, because it is not the
/// recording host's own zone (that is `Europe/Berlin`) — it is the value that
/// suite deliberately assigned. Moving the pin here is a straight transfer of
/// an existing decision from one suite's `setUp` to the type every suite goes
/// through.
enum PinnedTimeZoneSnapshot {
    /// The time zone every committed reference was recorded under — read off
    /// `HistoryViewSnapshotTests.setUp` as it stood before #1659, which is now
    /// deleted because this is where that pin lives.
    static let referenceTimeZone = TimeZone(identifier: "UTC")!

    /// A zone whose wall clock differs from `referenceTimeZone` at every
    /// instant (fixed +09:00, no DST).
    ///
    /// Its job is the same as `PinnedLocaleSnapshot.contrastingLocale`'s: keep
    /// `referenceTimeZone` from being changed into something that makes the
    /// both-sides arms tautological. `Asia/Tokyo` also differs from the
    /// recording host's `Europe/Berlin`, so neither arm of a two-zone test can
    /// agree with the machine — which is what makes such a test able to fail
    /// on this laptop and on a UTC runner alike.
    static let contrastingTimeZone = TimeZone(identifier: "Asia/Tokyo")!
}
