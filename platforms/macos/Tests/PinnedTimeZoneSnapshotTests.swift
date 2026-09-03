import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Locks the property #1659 is about: an image snapshot renders under the time
/// zone it is ASKED for, not the one the machine happens to be in.
///
/// The obvious test — "renders 00:00" — is the trap, and it is the same one
/// `PinnedScaleSnapshotTests` is written around (as `PinnedLocaleSnapshotTests`
/// was, until #1874 retired it with its only subject).
/// `HistoryViewSnapshotTests` used to assign `NSTimeZone.default = UTC`
/// in `setUp`, so on any machine an assertion of "renders as UTC" passes
/// whether the formatter read its argument or the process default. The fix is
/// to drive BOTH zones through one view: whatever `NSTimeZone.default` is, at
/// most one arm can agree with it, so an implementation that reads the machine
/// fails the other — on this `Europe/Berlin` laptop and on a UTC runner alike.
///
/// Everything below goes through `PinnedSnapshotHost`, the type the suites
/// snapshot, rather than through a hosting view this file assembles: "the host
/// that pins" and "the host the proof was taken on" must not be two objects
/// that can disagree.
///
/// ## What this suite does NOT reach, and how that was measured
///
/// `HistoryActivityContentView.tooltipText` also renders a date, and #1659
/// threads `\.formatTimeZone` and `\.formatLocale` into it — but no assertion
/// here grades that pass-through, because neither of its two call sites
/// produces anything a test can read back. `.tooltip(…)`
/// (`SessionListView.swift`) is an `onHover`-only modifier that draws into a
/// separate `NSPanel` and adds nothing to the view tree, and `.accessibilityLabel(…)`
/// is not rasterised: a window-less `NSHostingView` over three `Text`s reports
/// `accessibilityChildren() == nil` and `accessibilityLabel() == nil` at the
/// root (measured — SwiftUI builds the AX tree lazily for a real AX client, and
/// there is none in an `xctest` process). What IS graded is the formatter it
/// calls, `HistoryFormat.fullDateTime`, directly.
@MainActor
final class PinnedTimeZoneSnapshotTests: XCTestCase {

    /// 2023-11-14 22:13:20 UTC — late enough in the UTC day that `Asia/Tokyo`
    /// (+09:00, no DST) is already on the NEXT date, so the two zones differ in
    /// the `M/d` label as well as in `HH:mm`.
    private let instant = Date(timeIntervalSince1970: 1_700_000_000)

    private var utc: TimeZone { PinnedTimeZoneSnapshot.referenceTimeZone }
    private var tokyo: TimeZone { PinnedTimeZoneSnapshot.contrastingTimeZone }

    // MARK: - The formatters honour the zone they are GIVEN

    /// Both x-axis formats, both zones. This is also the arm that would catch
    /// the specific hazard #1659 describes — a formatter cached under one zone
    /// and handed back for another — since a cache keyed only by format string
    /// would answer the second call with the first call's formatter.
    func testAxisLabelsRenderInTheZoneTheyAreGiven() {
        XCTAssertEqual(HistoryFormat.axisLabel(instant, bucketSeconds: 3_600, timeZone: utc), "22:13")
        XCTAssertEqual(HistoryFormat.axisLabel(instant, bucketSeconds: 3_600, timeZone: tokyo), "07:13")
        XCTAssertEqual(HistoryFormat.axisLabel(instant, bucketSeconds: 86_400, timeZone: utc), "11/14")
        XCTAssertEqual(HistoryFormat.axisLabel(instant, bucketSeconds: 86_400, timeZone: tokyo), "11/15")
    }

    func testClockRendersInTheZoneItIsGiven() {
        XCTAssertEqual(HistoryFormat.clock(instant, timeZone: utc), "Tue 10:13 PM")
        XCTAssertEqual(HistoryFormat.clock(instant, timeZone: tokyo), "Wed 7:13 AM")
    }

    /// The tooltip formatter, which is the one that varies on BOTH axes: it is
    /// deliberately localised, so it is graded against a locale it is given as
    /// well as a zone.
    func testFullDateTimeRendersInTheZoneAndLocaleItIsGiven() {
        let en = Locale(identifier: "en_US")
        // U+202F, narrow no-break space before the AM/PM marker — measured, not
        // guessed. `clock` above has an ordinary space because it is
        // `en_US_POSIX`; this formatter is localised and ICU is not.
        XCTAssertEqual(HistoryFormat.fullDateTime(instant, timeZone: utc, locale: en),
                       "Nov 14, 2023 at 10:13\u{202F}PM")
        XCTAssertEqual(HistoryFormat.fullDateTime(instant, timeZone: tokyo, locale: en),
                       "Nov 15, 2023 at 7:13\u{202F}AM")
        // Same instant, same zone, a locale that spells the date differently —
        // so this arm cannot pass while the locale argument is being dropped.
        XCTAssertNotEqual(HistoryFormat.fullDateTime(instant, timeZone: utc, locale: en),
                          HistoryFormat.fullDateTime(instant, timeZone: utc,
                                                     locale: Locale(identifier: "de_DE")))
    }

    /// The two constants the both-sides arms rely on really do disagree at the
    /// instant those arms use, so a future edit cannot quietly make this suite
    /// tautological by pointing them at the same offset.
    func testReferenceAndContrastingTimeZonesRenderDifferently() {
        let a = HistoryFormat.clock(instant, timeZone: PinnedTimeZoneSnapshot.referenceTimeZone)
        let b = HistoryFormat.clock(instant, timeZone: PinnedTimeZoneSnapshot.contrastingTimeZone)
        XCTAssertNotEqual(a, b, "referenceTimeZone and contrastingTimeZone render the same clock (\(a)) — "
                          + "every both-sides assertion here is vacuous")
    }

    // MARK: - The pin reaches the pixels

    /// A forecast strip whose one window has a non-nil `projectedCap`, so it
    /// renders `Text("▲ cap \(HistoryFormat.clock(cap)))` — the same call site
    /// `testQuotaForecastSingleProvider`'s committed reference contains.
    private func forecastView() -> some View {
        let start = instant
        let window = QuotaWindowVM(
            label: "5h",
            planLabel: "Claude Max",
            start: start,
            end: start.addingTimeInterval(5 * 3_600),
            now: start.addingTimeInterval(3_600),
            usedPercent: 42,
            projectedCap: start.addingTimeInterval(3 * 3_600),
            isStale: false
        )
        return HistoryQuotaForecastView(providers: [
            QuotaProviderVM(id: "anthropic", iconKey: "anthropic",
                            planLabel: "Claude Max", windows: [window])
        ])
    }

    /// Rasterise the forecast strip through the host the suites use, at the
    /// scale the committed references were recorded at.
    ///
    /// `timeZone: nil` means "pass no argument", i.e. exactly what every real
    /// suite writes.
    private func rasterizedForecast(timeZone: TimeZone?) -> Data {
        let host = timeZone.map {
            PinnedSnapshotHost(forecastView().frame(width: 320, height: 220),
                               width: 320, height: 220, timeZone: $0)
        } ?? PinnedSnapshotHost(forecastView().frame(width: 320, height: 220),
                                width: 320, height: 220)
        let image = PinnedScaleSnapshot.rasterize(host.view, scale: PinnedScaleSnapshot.referenceScale)
        // "could not rasterise" and "rasterised to the same thing" must never
        // produce the same verdict.
        guard let data = image.tiffRepresentation, !data.isEmpty else {
            XCTFail("the forecast strip rasterised to nothing — this check cannot have run")
            return Data()
        }
        return data
    }

    /// The load-bearing arm, and the one that would have caught #1630's
    /// mutation B in this family: revert the product seam so the formatters
    /// read `NSTimeZone.default` again and both renders become the machine's
    /// clock — identical — which is what this refuses.
    func testTheSameViewRendersDifferentlyUnderTwoPinnedTimeZones() {
        // The premise, stated loudly first: these two zones must actually
        // produce different text for this fixture, or a byte comparison below
        // would be measuring nothing.
        let cap = instant.addingTimeInterval(3 * 3_600)
        XCTAssertNotEqual(HistoryFormat.clock(cap, timeZone: utc),
                          HistoryFormat.clock(cap, timeZone: tokyo),
                          "the fixture's projected cap reads the same in both zones — "
                          + "the pixel comparison below cannot fail for the right reason")

        // …and rendering is deterministic, or "they differ" proves nothing.
        XCTAssertEqual(rasterizedForecast(timeZone: utc), rasterizedForecast(timeZone: utc),
                       "the same view rasterised twice under the same zone differs — "
                       + "this suite's both-sides arms are not measuring the time zone")

        XCTAssertNotEqual(rasterizedForecast(timeZone: utc), rasterizedForecast(timeZone: tokyo),
                          "the forecast strip rendered identically under UTC and Asia/Tokyo — "
                          + "the pinned zone is reaching nothing, and the render is coming from "
                          + "NSTimeZone.default (\(NSTimeZone.default.identifier))")
    }

    /// …and the zone a suite gets when it names none is the one the committed
    /// references were recorded under, so the arms above cannot pass while every
    /// real snapshot renders under something else.
    ///
    /// Stated separately from the arms above so a change to `referenceTimeZone`
    /// fails HERE, naming the constant, rather than as eight opaque byte
    /// mismatches in `HistoryViewSnapshotTests`.
    func testTheHostsDefaultTimeZoneIsTheReferenceTimeZone() {
        XCTAssertEqual(rasterizedForecast(timeZone: nil),
                       rasterizedForecast(timeZone: PinnedTimeZoneSnapshot.referenceTimeZone),
                       "the host's default time zone is not `PinnedTimeZoneSnapshot.referenceTimeZone`")
        XCTAssertNotEqual(rasterizedForecast(timeZone: nil),
                          rasterizedForecast(timeZone: PinnedTimeZoneSnapshot.contrastingTimeZone),
                          "the host renders the same under both zones — the equality above is vacuous")
    }

    // MARK: - Every time-zone-carrying environment is pinned, not just the seam

    /// Collects what a hosted subtree actually sees. A class so the SwiftUI
    /// value type can write into it.
    private final class EnvironmentReport {
        var formatTimeZone: TimeZone?
        var calendar: Calendar?
        var timeZone: TimeZone?
    }

    private struct EnvironmentProbe: View {
        @Environment(\.formatTimeZone) private var formatTimeZone
        @Environment(\.calendar) private var calendar
        @Environment(\.timeZone) private var timeZone
        let report: EnvironmentReport

        var body: some View {
            report.formatTimeZone = formatTimeZone
            report.calendar = calendar
            report.timeZone = timeZone
            return Color.clear
        }
    }

    /// The host pins **all three** environments a date can be read from, not
    /// only the seam this issue is named after.
    ///
    /// This arm exists because the first draft of #1659 pinned only
    /// `\.formatTimeZone` and six of the fourteen `HistoryViewSnapshotTests`
    /// references still reddened — Swift Charts resolves
    /// `AxisMarks(values: .automatic(…))` through `\.calendar`, whose default
    /// `Calendar.current` follows `NSTimeZone.default`, so the deleted `setUp`
    /// had been holding tick and gridline POSITIONS as well as label text. A
    /// pixel comparison cannot separate those two; this can.
    func testTheHostPinsEveryTimeZoneCarryingEnvironment() {
        let report = EnvironmentReport()
        _ = PinnedSnapshotHost(EnvironmentProbe(report: report),
                               width: 40, height: 40, timeZone: tokyo)

        // "the probe never rendered" and "the probe saw the right thing" must
        // not produce the same verdict.
        guard let seen = report.formatTimeZone else {
            return XCTFail("the probe's body never evaluated — this check cannot have run")
        }
        XCTAssertEqual(seen, tokyo, "`\\.formatTimeZone` is not pinned by the host")
        XCTAssertEqual(report.calendar?.timeZone, tokyo,
                       "`\\.calendar` is not pinned by the host — Swift Charts picks its date "
                       + "ticks through this, so the reference positions come from the machine")
        XCTAssertEqual(report.timeZone, tokyo, "`\\.timeZone` is not pinned by the host")
        // …and the calendar's identity and week rules, which also decide tick
        // boundaries, are not the host's either.
        XCTAssertEqual(report.calendar?.identifier, .gregorian,
                       "the host's calendar identity is not pinned")
        // NOTE when this one fails: `Locale` prints its identifier and nothing
        // else, so an autoupdating `de_DE` and a fixed `de_DE` produce a
        // baffling "(de_DE) is not equal to (de_DE)". They are genuinely
        // different — the autoupdating one is the MACHINE — and telling them
        // apart is the whole point, so the strict comparison stays.
        XCTAssertEqual(report.calendar?.locale, PinnedLocaleSnapshot.referenceLocale,
                       "the host's calendar locale is not `PinnedLocaleSnapshot.referenceLocale` "
                       + "(if both sides print the same identifier, one of them is autoupdating)")
    }

    // MARK: - The seam costs the app nothing

    /// `\.formatTimeZone` defaults to `NSTimeZone.default`, which is what an
    /// unset `DateFormatter.timeZone` already resolved through — so the shipping
    /// app renders exactly what it rendered before #1659, on every host. A user
    /// in Berlin still sees Berlin times.
    ///
    /// The unpinned formatter here is spelled the way `HistoryFormat.posix` was
    /// before #1659, verbatim, so this is a comparison against the old code
    /// rather than against a description of it.
    func testTheDefaultIsIndistinguishableFromTheUnpinnedFormatterItReplaced() {
        let before = DateFormatter()
        before.locale = Locale(identifier: "en_US_POSIX")
        before.dateFormat = "HH:mm"            // `timeZone` deliberately never set

        XCTAssertEqual(
            HistoryFormat.axisLabel(instant, bucketSeconds: 3_600,
                                    timeZone: EnvironmentValues().formatTimeZone),
            before.string(from: instant),
            "the environment default renders a different wall clock than the formatter it replaced")
    }

    /// …and the default is `NSTimeZone.default` specifically, read per access —
    /// not `TimeZone.autoupdatingCurrent`, and not a value captured once.
    ///
    /// This is the only assertion that can tell those apart, because they agree
    /// on every host until something assigns `NSTimeZone.default`. Measured on
    /// this machine: after `NSTimeZone.default = UTC`, an unset `DateFormatter`
    /// renders UTC while `TimeZone.autoupdatingCurrent` and `TimeZone.current`
    /// both still report `Europe/Berlin` — so either of those two as the default
    /// would have been a real behaviour change for exactly the processes that
    /// assign it, which is what `HistoryViewSnapshotTests` used to be.
    ///
    /// It is the one place left in this target that assigns `NSTimeZone.default`;
    /// it restores it from a teardown block, which XCTest runs on failure too.
    func testTheDefaultFollowsNSTimeZoneDefaultAndNotAutoupdatingCurrent() {
        // Pick a zone the host is not already in, so "it followed" and "it was
        // already that" cannot look the same.
        let target = NSTimeZone.default == tokyo ? utc : tokyo
        let original = NSTimeZone.default
        addTeardownBlock { NSTimeZone.default = original }
        NSTimeZone.default = target

        XCTAssertEqual(EnvironmentValues().formatTimeZone, target,
                       "`\\.formatTimeZone`'s default does not follow NSTimeZone.default — "
                       + "either it is captured rather than computed, or it reads something else")
        XCTAssertNotEqual(TimeZone.autoupdatingCurrent, target,
                          "TimeZone.autoupdatingCurrent followed NSTimeZone.default on this host — "
                          + "the measurement this default rests on no longer holds; re-check the "
                          + "choice in FormatTimeZoneEnvironment.swift")
    }
}
