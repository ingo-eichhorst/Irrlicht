import Foundation

/// The instant every image snapshot renders "now" as, so a committed reference
/// is a picture of the VIEW and not of the DAY it was recorded (#1663).
///
/// ## What this is protecting against
///
/// `SessionListView.formatResetTime` picked its format string from `Date()`:
/// the same reset renders `"9:00"` before midnight and `"Fri 9:00"` after. The
/// scale (#1530), the locale (#1630), the time zone (#1659) and the preference
/// store (#1662) were all values read from the MACHINE where they were
/// available from the INPUT; this is the same shape with a sharper edge,
/// because it fails on the recording machine itself the next morning rather
/// than only on somebody else's.
///
/// ## Why the value is free to choose, unlike the other four
///
/// `referenceScale`, `referenceLocale` and `referenceTimeZone` are all "what
/// the committed references were recorded under", because adopting a different
/// value would have re-recorded a PNG — the move `AGENTS.md` names as the one
/// #1034 and #1044 both got wrong. Nothing constrains this one, because **no
/// committed reference contains a wall-clock-dependent rendering**: every site
/// that reads one is behind `guard let snap = session.metrics?.rateLimit`
/// (`SessionListView.swift`, and since #1675 `QuotaChipParts.swift`) and no
/// fixture under `Tests/Fixtures` or `Tests/MockInstanceFiles` carries a
/// rate-limit window (#1663 verified that by grep; re-measured by #1675, still
/// zero matches). So this pin changed none of the 53 references, and that
/// untouched set is the evidence.
///
/// #1675 asked whether to seed one — a committed PNG of the chip — now that the
/// clock is an input. The answer was no, for a reason that has nothing to do
/// with the clock: per #1615 every committed-reference image suite currently
/// fails on a CI runner over rasterisation and would have to be classified as
/// skipped in `ImageSnapshotCIScopeTests`, so the fixture is seeded as
/// two-render-in-memory arms (`QuotaChipClockTests`) that run everywhere
/// instead.
///
/// The polarity is #1662's: a suite that names no instant gets a FIXED one, not
/// the machine's, so the first fixture to seed a rate-limit window cannot
/// photograph the recording day even by forgetting.
enum PinnedNowSnapshot {
    /// 2023-11-14 22:13:20 UTC. The same instant `PinnedTimeZoneSnapshotTests`
    /// drives, so the two families' fixtures line up: it is late enough in the
    /// UTC day that `Asia/Tokyo` (+09:00) is already on the next date, which is
    /// what lets a single fixture vary the DAY on the time-zone axis as well as
    /// on this one.
    static let referenceNow = Date(timeIntervalSince1970: 1_700_000_000)

    /// An instant on a different calendar day from `referenceNow` **in every
    /// time zone**, so a both-sides arm cannot be quietly made tautological by
    /// changing the zone it runs under.
    ///
    /// Exactly 48 hours later: the widest UTC offset in use is ±14:00, so two
    /// instants two days apart cannot land on one local day anywhere. A smaller
    /// gap would be sound for UTC and wrong for `Pacific/Kiritimati`.
    static let contrastingNow = referenceNow.addingTimeInterval(48 * 3_600)
}
