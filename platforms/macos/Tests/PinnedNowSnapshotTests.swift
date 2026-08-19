import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Locks the property #1663 is about: the quota chip's reset label renders
/// against the clock, calendar, zone and locale it is GIVEN, not the four the
/// machine happens to be in.
///
/// The obvious test — "renders 09:00" — is the trap this whole family is
/// written around (`PinnedScaleSnapshotTests`, `PinnedLocaleSnapshotTests`,
/// `PinnedTimeZoneSnapshotTests` each say it in their own terms). Asserting one
/// rendering is green on the host that agrees with it whether the input reached
/// the view or not. So every axis here drives **two** values through one view:
/// whatever the machine is, at most one arm can agree with it.
///
/// The "now" axis is the one #1659 deliberately left open, and it is the
/// sharpest of the four because it does not shift a rendered time — it selects
/// a different FORMAT STRING. `"09:00"` before midnight, `"Fr. 9:00"` after,
/// from the same input. A reference pinned on zone and locale alone is still a
/// picture of the day it was recorded, and it fails on the recording machine
/// itself the next morning.
///
/// ## What is graded where, and why it is split
///
/// `QuotaResetFormat` is graded directly, on strings: that is where the branch
/// selection lives and a string says which branch ran. `QuotaResetLabel` is
/// graded in pixels, by rasterising it twice under different pins and refusing
/// an identical result: that is what catches a view which computes the right
/// answer from the wrong input — a `Date()` left at the call site passes every
/// string assertion in this file.
///
/// Everything goes through `PinnedSnapshotHost`, the type the snapshot suites
/// use, rather than a hosting view assembled here: "the host that pins" and
/// "the host the proof was taken on" must not be two objects that can disagree.
///
/// ## What this suite does NOT reach
///
/// The quota **tooltip**, which is `formatClockTime`'s only call site.
/// `.tooltip(…)` is an `onHover`-only AppKit bridge drawing into a separate
/// `NSPanel`; it adds nothing to the view tree, and a window-less
/// `NSHostingView` populates no accessibility tree in an `xctest` process
/// (measured by #1659 for the same reason, and by
/// `PermissionWizardEffectErrorRenderTests`, which abandoned an AX walk after
/// every query came back empty). So `QuotaResetFormat.clock` is graded on
/// strings and its pass-through from `SessionListView`'s environment is held by
/// the compiler — both arguments are required — and by nothing else. Stated
/// rather than implied.
@MainActor
final class PinnedNowSnapshotTests: XCTestCase {

    // MARK: - Fixtures

    /// 2023-11-17 09:00:00 UTC — a Friday, so the not-same-day branch renders
    /// the `"Fri 9:00"` spelling `QuotaResetFormat.resetLabel`'s own doc
    /// comment uses as its example.
    private let reset = Date(timeIntervalSince1970: 1_700_211_600)

    /// 2023-11-17 08:00:00 UTC — an hour before the reset, same UTC day.
    private let sameDayNow = Date(timeIntervalSince1970: 1_700_208_000)

    /// 2023-11-16 23:00:00 UTC — ten hours before the reset, and the PREVIOUS
    /// UTC day. In `Asia/Tokyo` (+09:00) it is 08:00 on the same local day as
    /// the reset, which is what lets one (date, now) pair drive the branch from
    /// the time-zone axis as well as from this one.
    private let otherDayNow = Date(timeIntervalSince1970: 1_700_175_600)

    private var utc: TimeZone { PinnedTimeZoneSnapshot.referenceTimeZone }
    private var tokyo: TimeZone { PinnedTimeZoneSnapshot.contrastingTimeZone }
    private var de: Locale { PinnedLocaleSnapshot.referenceLocale }
    private var en: Locale { PinnedLocaleSnapshot.contrastingLocale }
    private var gregorian: Calendar { Calendar(identifier: .gregorian) }

    private func label(_ date: Date, now: Date,
                       calendar: Calendar? = nil,
                       zone: TimeZone? = nil,
                       locale: Locale? = nil) -> String {
        QuotaResetFormat.resetLabel(date, now: now,
                                    calendar: calendar ?? gregorian,
                                    timeZone: zone ?? utc,
                                    locale: locale ?? de)
    }

    // MARK: - The branch is chosen by the "now" it is given

    /// The whole point of #1663, in two lines: one reset, two clocks, two
    /// different FORMATS. Neither arm can be green on a host that reads
    /// `Date()`, because today is neither of these days.
    func testResetLabelSelectsItsFormatFromTheNowItIsGiven() {
        XCTAssertEqual(label(reset, now: sameDayNow), "09:00")
        XCTAssertEqual(label(reset, now: otherDayNow), "Fr. 9:00")
    }

    /// …and the branch selection is not a property of one locale's spelling.
    func testResetLabelSelectsItsFormatFromTheNowInEveryLocale() {
        // U+202F, narrow no-break space before the AM/PM marker — measured, not
        // guessed, and the same trap `PinnedTimeZoneSnapshotTests` records for
        // `HistoryFormat.fullDateTime`. A plain space here fails with
        // `("9:00 AM") is not equal to ("9:00 AM")`.
        XCTAssertEqual(label(reset, now: sameDayNow, locale: en), "9:00\u{202F}AM")
        XCTAssertEqual(label(reset, now: otherDayNow, locale: en), "Fri 9:00")
    }

    // MARK: - …and the zone, and the locale

    /// One (reset, now) pair, two zones, and the zone decides the BRANCH as
    /// well as the clock: in UTC the reset is tomorrow, in `Asia/Tokyo` it is
    /// later today. An implementation that compared days in one zone and
    /// rendered in another would fail here and nowhere else in this file.
    func testResetLabelRendersAndBranchesInTheZoneItIsGiven() {
        XCTAssertEqual(label(reset, now: otherDayNow, zone: utc), "Fr. 9:00")
        XCTAssertEqual(label(reset, now: otherDayNow, zone: tokyo), "18:00")
    }

    /// Both branches on the locale axis, so an arm cannot pass while the
    /// locale argument is dropped on the branch nobody looked at.
    func testResetLabelRendersInTheLocaleItIsGiven() {
        XCTAssertNotEqual(label(reset, now: sameDayNow, locale: de),
                          label(reset, now: sameDayNow, locale: en))
        XCTAssertNotEqual(label(reset, now: otherDayNow, locale: de),
                          label(reset, now: otherDayNow, locale: en))
    }

    func testClockRendersInTheZoneAndLocaleItIsGiven() {
        XCTAssertEqual(QuotaResetFormat.clock(reset, timeZone: utc, locale: de), "09:00")
        XCTAssertEqual(QuotaResetFormat.clock(reset, timeZone: tokyo, locale: de), "18:00")
        XCTAssertEqual(QuotaResetFormat.clock(reset, timeZone: utc, locale: en), "9:00\u{202F}AM")
    }

    // MARK: - The calendar argument, and why no pixel arm grades it

    /// `resetLabel` overrides its calendar's time zone with the one it renders
    /// in, so what the calendar argument still contributes is its IDENTITY and
    /// week rules. This measures that the identity cannot change the verdict —
    /// which is the evidence for there being no both-sides pixel arm for the
    /// calendar below, rather than an assumption that there needn't be one.
    ///
    /// It is also the reason the argument is `\.calendar` (default
    /// `Calendar.current`) and not a forced `.gregorian`: a user on a Japanese
    /// or Buddhist calendar keeps theirs, and it demonstrably costs nothing.
    func testTheCalendarIdentityDoesNotDecideTheBranch() {
        let identifiers: [Calendar.Identifier] = [
            .gregorian, .buddhist, .chinese, .coptic, .ethiopicAmeteMihret,
            .ethiopicAmeteAlem, .hebrew, .iso8601, .indian, .islamic,
            .islamicCivil, .islamicTabular, .islamicUmmAlQura, .japanese,
            .persian, .republicOfChina,
        ]
        let pairs = [(reset, sameDayNow), (reset, otherDayNow)]

        // The premise: these pairs really do exercise both branches, or every
        // comparison below is one verdict compared against itself.
        let gregorianVerdicts = Set(pairs.map { pair -> String in
            label(pair.0, now: pair.1)
        })
        XCTAssertEqual(gregorianVerdicts.count, 2,
                       "the fixture pairs no longer render both branches — "
                       + "this sweep is comparing one verdict against itself")

        for identifier in identifiers {
            for (date, now) in pairs {
                XCTAssertEqual(label(date, now: now, calendar: Calendar(identifier: identifier)),
                               label(date, now: now, calendar: gregorian),
                               "calendar `\(identifier)` decides the same-day branch differently "
                               + "from `.gregorian` at a fixed zone — `QuotaResetFormat`'s doc "
                               + "comment, and the missing pixel arm it justifies, are wrong")
            }
        }
    }

    // MARK: - The constants the both-sides arms rest on

    /// `referenceNow` and `contrastingNow` really are on different days, in
    /// both pinned zones, so a future edit cannot quietly make the pixel arms
    /// tautological by moving them closer together.
    func testReferenceAndContrastingNowsSelectDifferentBranches() {
        for zone in [utc, tokyo] {
            let a = label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.referenceNow, zone: zone)
            let b = label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.contrastingNow, zone: zone)
            XCTAssertNotEqual(a, b,
                              "PinnedNowSnapshot.referenceNow and .contrastingNow render the same "
                              + "label (\(a)) in \(zone.identifier) — every both-sides arm that "
                              + "uses them is vacuous")
        }
    }

    // MARK: - The seam costs the app nothing

    /// The environment defaults render exactly what the code they replaced
    /// rendered, on every host.
    ///
    /// Both old spellings are copied **verbatim** from `SessionListView` as it
    /// stood before #1663, so this is a comparison against the old code and not
    /// against a description of it.
    func testTheDefaultsAreIndistinguishableFromTheCodeTheyReplaced() {
        let env = EnvironmentValues()

        // --- formatClockTime, verbatim ---
        let oldClock = DateFormatter()
        oldClock.dateStyle = .none
        oldClock.timeStyle = .short
        XCTAssertEqual(QuotaResetFormat.clock(reset, timeZone: env.formatTimeZone, locale: env.formatLocale),
                       oldClock.string(from: reset),
                       "`\\.formatTimeZone` / `\\.formatLocale` defaults render a different clock "
                       + "than the unset DateFormatter they replaced")

        // --- formatResetTime, verbatim, on both branches ---
        // The wall clock is read on both sides, microseconds apart. The one way
        // that is unsound is a day boundary falling between the two reads, so
        // it is checked rather than hoped for.
        func oldResetTime(_ date: Date) -> String {
            let cal = Calendar.current
            let now = Date()
            let f = DateFormatter()
            if cal.isDate(date, inSameDayAs: now) {
                f.dateStyle = .none
                f.timeStyle = .short
            } else {
                f.dateFormat = "EEE H:mm"
            }
            return f.string(from: date)
        }

        let openedAt = Date()
        for date in [Date(), Date().addingTimeInterval(3 * 86_400)] {
            XCTAssertEqual(
                QuotaResetFormat.resetLabel(date, now: env.formatNow(),
                                            calendar: env.calendar,
                                            timeZone: env.formatTimeZone,
                                            locale: env.formatLocale),
                oldResetTime(date),
                "the environment defaults render a different reset label than the code they "
                + "replaced")
        }
        XCTAssertTrue(Calendar.current.isDate(openedAt, inSameDayAs: Date()),
                      "a calendar day turned over while this test ran, so the two sides read "
                      + "different days for a reason unrelated to the seam — re-run it")
    }

    /// …and the default clock is the wall clock read AT EACH CALL, not an
    /// instant captured when the key was defined.
    ///
    /// This is the arm that separates `FormatNow.wallClock` from a stopped
    /// clock that happens to be close to now, and it is the counterpart of
    /// `PinnedTimeZoneSnapshotTests.testTheDefaultFollowsNSTimeZoneDefaultAndNotAutoupdatingCurrent`.
    func testTheDefaultIsTheWallClockReadAtEachCall() {
        let before = Date()
        let read = EnvironmentValues().formatNow()
        let after = Date()

        XCTAssertGreaterThanOrEqual(read, before,
                                    "`\\.formatNow`'s default answered with an instant from before "
                                    + "it was called — it is captured, not read")
        XCTAssertLessThanOrEqual(read, after,
                                 "`\\.formatNow`'s default answered with an instant from the "
                                 + "future — it is not reading the wall clock")
        // Two reads of the default advance; two reads of a FIXED clock do not.
        // Without this, a `FormatNow(fixed: Date())` default would pass both
        // assertions above.
        XCTAssertNotEqual(EnvironmentValues().formatNow(), read,
                          "two reads of `\\.formatNow`'s default answered the same instant — "
                          + "the default is stopped, not running")
    }

    /// …and a pinned clock is stopped: every read in a subtree answers the same
    /// instant, which is what makes a rendering a function of its inputs.
    func testAPinnedClockIsStopped() {
        let stopped = FormatNow(fixed: PinnedNowSnapshot.referenceNow)
        XCTAssertEqual(stopped(), PinnedNowSnapshot.referenceNow)
        XCTAssertEqual(stopped(), stopped())
    }

    // MARK: - The pin reaches the pixels

    /// The real `QuotaResetLabel` — the view `SessionListView.quotaWindowRow`
    /// renders — hosted through the type the snapshot suites use.
    private func rasterizedLabel(resetsAt: Date,
                                 now: Date = PinnedNowSnapshot.referenceNow,
                                 timeZone: TimeZone? = nil,
                                 locale: Locale? = nil) -> Data {
        let content = QuotaResetLabel(resetsAt: resetsAt).frame(width: 200, height: 24)
        let host = PinnedSnapshotHost(content,
                                      width: 200, height: 24,
                                      locale: locale ?? PinnedLocaleSnapshot.referenceLocale,
                                      timeZone: timeZone ?? PinnedTimeZoneSnapshot.referenceTimeZone,
                                      now: now)
        let image = PinnedScaleSnapshot.rasterize(host.view, scale: PinnedScaleSnapshot.referenceScale)
        // "could not rasterise" and "rasterised to the same thing" must never
        // produce the same verdict.
        guard let data = image.tiffRepresentation, !data.isEmpty else {
            XCTFail("the reset label rasterised to nothing — this check cannot have run")
            return Data()
        }
        return data
    }

    /// The load-bearing arm, and the one that catches the mutation this whole
    /// change exists to make catchable: put `Date()` back at the call site in
    /// `QuotaResetLabel` and both renders become today's, identically — which
    /// is what this refuses. Every string assertion above stays green under
    /// that mutation.
    func testTheSameLabelRendersDifferentlyUnderTwoPinnedNows() {
        // The premise, loudly first: these two clocks must produce different
        // text for this fixture, or the byte comparison measures nothing.
        XCTAssertNotEqual(
            label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.referenceNow),
            label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.contrastingNow),
            "the fixture reads the same under both pinned clocks — the pixel comparison below "
            + "cannot fail for the right reason")

        // …and rendering is deterministic, or "they differ" proves nothing.
        XCTAssertEqual(rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow),
                       rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow),
                       "the same label rasterised twice under the same clock differs — this "
                       + "suite's both-sides arms are not measuring the clock")

        XCTAssertNotEqual(rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow,
                                          now: PinnedNowSnapshot.referenceNow),
                          rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow,
                                          now: PinnedNowSnapshot.contrastingNow),
                          "the reset label rendered identically under two pinned clocks — "
                          + "`\\.formatNow` is reaching nothing and the branch is coming from "
                          + "the machine's wall clock")
    }

    /// The zone reaches the pixels of THIS view too — `PinnedTimeZoneSnapshotTests`
    /// proves it for the History panel, which shares no call site with this one.
    func testTheSameLabelRendersDifferentlyUnderTwoPinnedTimeZones() {
        XCTAssertNotEqual(label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.referenceNow, zone: utc),
                          label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.referenceNow, zone: tokyo),
                          "the fixture reads the same in both pinned zones — the pixel comparison "
                          + "below cannot fail for the right reason")
        XCTAssertNotEqual(rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow, timeZone: utc),
                          rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow, timeZone: tokyo),
                          "the reset label rendered identically under UTC and Asia/Tokyo — "
                          + "`\\.formatTimeZone` is reaching nothing and the render is coming "
                          + "from NSTimeZone.default (\(NSTimeZone.default.identifier))")
    }

    /// …and the locale.
    func testTheSameLabelRendersDifferentlyUnderTwoPinnedLocales() {
        XCTAssertNotEqual(label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.referenceNow, locale: de),
                          label(PinnedNowSnapshot.referenceNow, now: PinnedNowSnapshot.referenceNow, locale: en),
                          "the fixture reads the same in both pinned locales — the pixel "
                          + "comparison below cannot fail for the right reason")
        XCTAssertNotEqual(rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow, locale: de),
                          rasterizedLabel(resetsAt: PinnedNowSnapshot.referenceNow, locale: en),
                          "the reset label rendered identically under de_DE and en_US — "
                          + "`\\.formatLocale` is reaching nothing")
    }

    // MARK: - The host stops the clock, and stops it at the reference instant

    /// Collects what a hosted subtree actually sees. A class so the SwiftUI
    /// value type can write into it.
    private final class EnvironmentReport {
        var formatNow: FormatNow?
    }

    private struct EnvironmentProbe: View {
        @Environment(\.formatNow) private var formatNow
        let report: EnvironmentReport

        var body: some View {
            report.formatNow = formatNow
            return Color.clear
        }
    }

    func testTheHostStopsTheClockItIsGiven() {
        let report = EnvironmentReport()
        _ = PinnedSnapshotHost(EnvironmentProbe(report: report),
                               width: 40, height: 40,
                               now: PinnedNowSnapshot.contrastingNow)

        // "the probe never rendered" and "the probe saw the right thing" must
        // not produce the same verdict.
        guard let seen = report.formatNow else {
            return XCTFail("the probe's body never evaluated — this check cannot have run")
        }
        XCTAssertEqual(seen(), PinnedNowSnapshot.contrastingNow,
                       "`\\.formatNow` is not pinned by the host")
        XCTAssertEqual(seen(), seen(),
                       "the host's clock is still running — a subtree that reads it twice "
                       + "renders two different instants")
    }

    /// …and the instant a suite gets when it names none is
    /// `PinnedNowSnapshot.referenceNow`, so the arms above cannot pass while
    /// every real snapshot renders against the machine's clock.
    ///
    /// Stated separately so a change to `referenceNow` fails HERE, naming the
    /// constant, rather than as an opaque byte mismatch somewhere else.
    func testTheHostsDefaultNowIsTheReferenceNow() {
        let report = EnvironmentReport()
        _ = PinnedSnapshotHost(EnvironmentProbe(report: report), width: 40, height: 40)
        guard let seen = report.formatNow else {
            return XCTFail("the probe's body never evaluated — this check cannot have run")
        }
        XCTAssertEqual(seen(), PinnedNowSnapshot.referenceNow,
                       "the host's default clock is not `PinnedNowSnapshot.referenceNow`")
    }
}
