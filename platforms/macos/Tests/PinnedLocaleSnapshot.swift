import AppKit
import SwiftUI
@testable import Irrlicht

/// The locale every image snapshot is rendered under, so a committed reference
/// is a picture of the VIEW and not of the recording machine's regional
/// settings (#1630).
///
/// ## What went wrong
///
/// The rule editor #1874 deleted rendered its threshold field through
/// `format: .number`, which resolves via `Locale.autoupdatingCurrent`. The
/// reference host is `de_DE`, so its committed reference read `150.000`, while
/// a `macos-latest` runner at `en_US` rendered `150,000`. Every contributor
/// whose Mac groups thousands differently failed that test locally, with a
/// "Snapshot does not match reference." that named no cause. This is the
/// locale sibling of what `PinnedScaleSnapshot` is for the backing scale: a
/// value read from the MACHINE where the equivalent value was available from
/// the INPUT.
///
/// ## What is left of it after #1874
///
/// That view was the app's only `FormatStyle` render, and #1874 deleted it
/// along with the rest of the feature it belonged to — so **no surviving
/// reference is locale-dependent**, and a wrong value here would now redden
/// nothing. The pin stays anyway: it is what keeps the next locale-sensitive
/// render from silently photographing a machine, and `docs/swift-testing.md`
/// records that whoever adds one owes the family a fresh lock.
///
/// ## Why `de_DE` and not `en_US`
///
/// For the same reason `PinnedScaleSnapshot.referenceScale` is 2 rather than
/// the "nicest" scale: **it is what the committed references were recorded
/// under**, so adopting it regenerates nothing. `AGENTS.md` names re-recording
/// a reference to make a test pass as the move #1034 and #1044 both made and
/// both got wrong; pinning the recording host's own locale means #1630 never
/// has to make that call, and the untouched reference set is then itself the
/// evidence that the pin changed no rendering.
///
/// This is a statement about the PNGs on disk, not a preference. If the
/// catalog is ever re-recorded wholesale on another host, this constant moves
/// with it.
enum PinnedLocaleSnapshot {
    /// The locale every committed reference was recorded under. Measured:
    /// `defaults read -g AppleLocale` on the reference Mac → `de_DE`.
    ///
    /// Anchored on the surviving set rather than on one PNG's visible
    /// grouping, because #1874 deleted the only reference that had any: the
    /// evidence is that all 60 references under `Tests/__Snapshots__`
    /// (`find Tests/__Snapshots__ -name '*.png' | wc -l`) still match
    /// byte-for-byte on that host with this pin applied — `git status
    /// --porcelain platforms/macos/Tests/__Snapshots__` is the check, and it
    /// was empty across #1630, #1659, #1662, #1663 and #1874.
    static let referenceLocale = Locale(identifier: "de_DE")

    /// A locale that groups thousands differently from `referenceLocale`.
    /// `en_US` is what a `macos-latest` runner uses — the exact host that
    /// reported #1630.
    ///
    /// Its job is to keep `referenceLocale` from being changed into something
    /// that makes the both-sides arms tautological; the arms themselves name
    /// their locales literally, so they stay a statement about ICU rather than
    /// about whatever this file currently pins.
    static let contrastingLocale = Locale(identifier: "en_US")

    /// Every `NSTextField` in a rendered hierarchy, as
    /// `(placeholder, displayed string)`.
    ///
    /// Used to read what a SwiftUI `TextField` actually PUT ON SCREEN, which
    /// is the only way to prove a locale pin reached the format style rather
    /// than merely being set. Callers must check the field they wanted was
    /// found — "no such field" and "the field said the right thing" must never
    /// produce the same verdict.
    static func textFields(in view: NSView) -> [(placeholder: String?, value: String)] {
        var found: [(placeholder: String?, value: String)] = []
        func walk(_ v: NSView) {
            if let field = v as? NSTextField {
                found.append((field.placeholderString, field.stringValue))
            }
            v.subviews.forEach(walk)
        }
        walk(view)
        return found
    }
}

extension View {
    /// Pin the locale this subtree renders under, for a snapshot host.
    ///
    /// Sets **both** locale environments on purpose. `\.formatLocale` is the
    /// one `FormatStyle`-based rendering reads (see
    /// `Irrlicht/Views/FormatLocaleEnvironment.swift` — `TextField(value:format:)`
    /// demonstrably ignores `\.locale`), and `\.locale` is the one SwiftUI's own
    /// localized rendering reads. A snapshot host wants neither of them coming
    /// from the machine.
    ///
    /// On the reference host this is a no-op — both environments already
    /// resolve to `de_DE` there — which is why applying it regenerated no
    /// reference.
    fileprivate func pinnedLocale(_ locale: Locale) -> some View {
        environment(\.formatLocale, locale)
            .environment(\.locale, locale)
    }

    /// Pin the time zone this subtree renders dates in, for a snapshot host
    /// (#1659). Scoped to the subtree, unlike the `NSTimeZone.default`
    /// assignment `HistoryViewSnapshotTests` used to make, so it cannot leak
    /// past a test that aborts before its `tearDown` (#1523).
    ///
    /// Sets **three** environments, and the second one is the one nobody would
    /// predict. Measured, by deleting that suite's `setUp` and keeping only
    /// `\.formatTimeZone`: six of its fourteen references still reddened, and
    /// they all went green again under `TZ=UTC`. So the string formatting was
    /// not the only machine read.
    ///
    /// - `\.formatTimeZone` is the seam the `DateFormatter`s consult
    ///   (`Irrlicht/Views/FormatTimeZoneEnvironment.swift`). It decides what a
    ///   tick LABEL says.
    /// - `\.calendar` is what **Swift Charts** resolves `AxisMarks(values:
    ///   .automatic(…))` through, so it decides WHERE the ticks and gridlines
    ///   are — which is why `testQuotaForecast*` moved even though its chart
    ///   is `compact` and draws no axis labels at all. Its default is
    ///   `Calendar.current`, whose `timeZone` DOES follow `NSTimeZone.default`
    ///   (measured; `TimeZone.current` notably does not), which is how the old
    ///   `setUp` reached it by accident.
    /// - `\.timeZone` is SwiftUI's own. Nothing in this app is known to read
    ///   it, and its default did NOT follow `NSTimeZone.default`, so it was
    ///   `Europe/Berlin` while every committed reference was recorded — but
    ///   pinning it changes no reference (measured: all 14 still match), and a
    ///   host that pins two of the three time zones in its environment would be
    ///   an odd thing to leave behind.
    ///
    /// The calendar is rebuilt rather than copied from `Calendar.current`,
    /// which carries the host's calendar identity and week rules as well as its
    /// zone. `.gregorian` + the pinned locale reproduces the recording host
    /// exactly (all 14 references still match) while no longer depending on it.
    fileprivate func pinnedTimeZone(_ timeZone: TimeZone, locale: Locale) -> some View {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = timeZone
        calendar.locale = locale
        return environment(\.formatTimeZone, timeZone)
            .environment(\.calendar, calendar)
            .environment(\.timeZone, timeZone)
    }

    /// Stop the clock this subtree renders dates against, for a snapshot host
    /// (#1663).
    ///
    /// The fourth value in this family that a reference was reading off the
    /// machine, and the one whose failure is loudest: `Date()` inside
    /// `SessionListView.formatResetTime` did not shift a rendered time, it
    /// chose between `"9:00"` and `"Fri 9:00"`, so a reference recorded on one
    /// side of midnight fails on the other — on the recording machine too.
    ///
    /// One environment, not three: unlike the time zone, nothing else in
    /// SwiftUI carries a "now" a view could read instead. `\.formatNow` is the
    /// only key `QuotaResetFormat`'s callers consult, and
    /// `PinnedNowSnapshotTests.testTheHostStopsTheClockItIsGiven` grades that
    /// on this object rather than on a hierarchy it assembled itself.
    fileprivate func pinnedNow(_ instant: Date) -> some View {
        environment(\.formatNow, FormatNow(fixed: instant))
    }
}

/// The rasterisable host a `.pinnedImage` snapshot is taken of — and the only
/// way to build one.
///
/// ## Why this is a type and not a modifier every suite remembers to apply
///
/// The first draft of #1630 applied `.pinnedLocale()` by hand in each of the
/// seven host helpers and added a source scan asserting that every file taking
/// an image snapshot mentioned it. That is a guard policing an API the same
/// change invented, which this repo treats as a signal to remove the API
/// (`AGENTS.md`, #1390) — and the scan was weak for the usual reason: it could
/// not tell a file that *has* the call from one that merely *documents* it, and
/// it flagged `ImageSnapshotCIScopeTests` and `RasterPrimitiveEvidenceTests` on
/// its first run for saying the words.
///
/// So `Snapshotting.pinnedImage` is declared over **this type** rather than over
/// `NSView`. A suite cannot hand it an unpinned hierarchy, because the only
/// initializer applies the pin itself: forgetting is a compile error, not a
/// reference PNG that quietly photographs someone's regional settings.
/// `PinnedLocaleSnapshotTests` graded this object and not one it assembled
/// itself, so "the host the suites use" and "the host the pin was proven on"
/// could not be two things that disagree. It retired with its only subject in
/// #1874; `PinnedTimeZoneSnapshotTests` and `PinnedNowSnapshotTests` still
/// grade this object the same way, and they read `referenceLocale` from here,
/// so the constant is not unobserved — only its number-grouping arm is gone.
///
/// #1659 moved the **time zone** pin here for the same reason, out of
/// `HistoryViewSnapshotTests`' `setUp`. That one had a second problem a
/// subtree environment does not have: it assigned `NSTimeZone.default`, which
/// is process-wide, so it depended on `tearDown` running to put the process
/// back — and per #1523 this suite intermittently aborts mid-run.
/// `PinnedTimeZoneSnapshotTests` grades it here, on this object.
///
/// The remaining hole is the one a type cannot close: a suite that stops using
/// `.pinnedImage` altogether. `ImageSnapshotCIScopeTests` already derives its CI
/// classification from that same string, so such a suite loses the scale pin,
/// the locale pin and its CI classification together, as one visible failure
/// there.
/// #1662 moved the **preferences** pin here for the third time, and it is the
/// one where "a picture of the machine" was most literally true: `@AppStorage`
/// resolves `UserDefaults.standard`, which under `swift test` is the
/// `com.apple.dt.xctest.tool` domain — a real, persistent plist that every
/// previous run of this suite wrote into. `GroupViewSnapshotTests` and
/// `SessionRowSnapshotTests` each held eight of those keys open by hand in
/// `setUp` and put them back in `tearDown`, which fails in three ways at once:
/// it is per-suite rather than a property of the snapshot strategy, it pins a
/// SMALLER set than the views read (nine more keys sat in that domain
/// unpinned, one of them holding the developer's real project names), and a
/// `tearDown` does not run for the runs that need it most — #1523 aborts the
/// process, `swift-suite.sh` kills the tree at 240s and `--budget` kills the
/// gate. `PinnedAppStorageSnapshotTests` grades this object.
struct PinnedSnapshotHost {
    /// The laid-out hosting view. Read it to inspect what was rendered; pass
    /// the `PinnedSnapshotHost` itself to `assertSnapshot`.
    let view: NSView

    /// The store every `@AppStorage` in the hosted subtree resolved through.
    /// Read it to assert what a render was driven by; write to it BEFORE
    /// constructing the host to drive a render.
    let defaults: InMemoryDefaults

    /// Host `content` at a fixed size, appearance and locale.
    ///
    /// - Parameters:
    ///   - width/height: the hosting view's frame. Callers keep their own
    ///     `.frame`/`.background` modifiers on `content` — this changes no
    ///     geometry, which is why adopting it regenerated no reference.
    ///   - appearance: pinned for the same reason the locale is; every suite
    ///     already did this by hand (`SessionRowSnapshotTests`' light-mode pill
    ///     tests are the one caller that passes `.aqua`).
    ///   - locale: defaults to the locale every committed reference was
    ///     recorded under. Only the tests that PROVE the pin pass anything else.
    ///   - timeZone: same, for date rendering (#1659). It was
    ///     `HistoryViewSnapshotTests`' own `setUp` until this parameter
    ///     existed, which is exactly the "one suite remembers" shape the
    ///     locale pin was moved here to stop.
    ///   - defaults: the preference store `@AppStorage` resolves through
    ///     (#1662). Typed `InMemoryDefaults`, **not** `UserDefaults`, so
    ///     `.standard` is not expressible here: a `.pinnedImage` snapshot
    ///     cannot photograph the machine's preferences even by passing the
    ///     wrong argument. The default is a fresh, empty store, so an
    ///     unspecified key renders at the `@AppStorage` declaration's own
    ///     default — which is what the committed references were recorded
    ///     under, so adopting this regenerated none of them. A suite that
    ///     forgets to pass its store gets ISOLATION and a visible failure
    ///     ("my pinned value did not reach the view"), never a reference that
    ///     silently encodes someone's Settings.
    ///   - now: the instant `\.formatNow` answers with (#1663). Its default is
    ///     a fixed one rather than the wall clock, for #1662's polarity
    ///     reason: a suite that names none gets DETERMINISM, never a reference
    ///     that photographs the day it was recorded. Unconstrained by the
    ///     committed set — no reference renders a wall-clock-dependent site
    ///     today — so adopting it regenerated none of them.
    init(_ content: some View,
         width: CGFloat,
         height: CGFloat,
         appearance: NSAppearance.Name = .darkAqua,
         locale: Locale = PinnedLocaleSnapshot.referenceLocale,
         timeZone: TimeZone = PinnedTimeZoneSnapshot.referenceTimeZone,
         defaults: InMemoryDefaults = InMemoryDefaults(),
         now: Date = PinnedNowSnapshot.referenceNow) {
        let hosting = NSHostingView(
            rootView: content
                .pinnedLocale(locale)
                .pinnedTimeZone(timeZone, locale: locale)
                .defaultAppStorage(defaults)
                .pinnedNow(now))
        hosting.appearance = NSAppearance(named: appearance)
        hosting.frame = CGRect(x: 0, y: 0, width: width, height: height)
        hosting.layoutSubtreeIfNeeded()
        self.view = hosting
        self.defaults = defaults
    }
}
