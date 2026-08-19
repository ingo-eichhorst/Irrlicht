import AppKit
import SwiftUI
import XCTest
@testable import Irrlicht

/// Locks the property #1630 is about: an image snapshot renders under the
/// locale it is ASKED for, not the locale the machine happens to have.
///
/// The obvious test — "renders `150.000`" — is the trap, and it is the same one
/// `PinnedScaleSnapshotTests` is written around: on the `de_DE` Mac that
/// recorded the references it passes whether the locale is pinned or inherited
/// from the process, so it would be green for the wrong reason on the only
/// machine anyone had run it on. The fix is to drive BOTH locales through one
/// view. Whatever `Locale.autoupdatingCurrent` is, at most one arm can agree
/// with it, so an implementation that inherits the process locale fails the
/// other — on a `de_DE` laptop and an `en_US` runner alike.
///
/// Everything here goes through `PinnedSnapshotHost`, the type the suites
/// snapshot, rather than through a hosting view this file assembles: "the host
/// that pins" and "the host the proof was taken on" must not be two objects
/// that can disagree.
@MainActor
final class PinnedLocaleSnapshotTests: XCTestCase {

    // MARK: - The pin reaches the pixels

    /// The threshold field as it is actually rendered, read back off the
    /// `NSTextField` SwiftUI puts in the view tree.
    ///
    /// Reading the rendered string rather than trusting that the environment
    /// "was set" is the whole point: setting a locale and then rendering a view
    /// that ignores it is precisely the defect — #1630's own suggested fix,
    /// `.environment(\.locale, …)` on its own, does exactly that and changes
    /// nothing, which is why the seam in
    /// `Irrlicht/Views/FormatLocaleEnvironment.swift` had to exist.
    private func renderedThreshold(locale: Locale, placeholder: String = "150000") -> String? {
        let model = BackchannelRulesModel()
        model.rules = [
            BackchannelRule(
                id: "r1", enabled: true, name: "Auto-compact",
                trigger: .init(event: BackchannelRule.eventContextTokens, threshold: 150_000),
                actions: [.init(kind: BackchannelRule.actionInput, preset: BackchannelRule.presetCompact)],
                adapter: nil
            )
        ]
        let host = PinnedSnapshotHost(
            BackchannelRulesView(model: model).frame(width: 304, height: 220),
            width: 304, height: 220, locale: locale)

        // The tokens field is identified by the placeholder the view gives it
        // (`BackchannelRulesView`'s `isTokens ? "150000" : "85"`), not by
        // position — a reordered card must not silently start measuring the
        // rule-name field instead.
        return PinnedLocaleSnapshot.textFields(in: host.view)
            .first { $0.placeholder == placeholder }?.value
    }

    /// The load-bearing arm. Three locales, one view; the host can be at most
    /// one of them.
    func testTheThresholdFieldRendersUnderThePinnedLocaleNotTheProcessLocale() {
        let cases: [(Locale, String)] = [
            (Locale(identifier: "en_US"), "150,000"),
            (Locale(identifier: "de_DE"), "150.000"),
            // U+202F, narrow no-break space — measured, not guessed.
            (Locale(identifier: "fr_FR"), "150\u{202F}000"),
        ]
        for (locale, expected) in cases {
            guard let rendered = renderedThreshold(locale: locale) else {
                // "could not find the field" and "the field said the right
                // thing" must never produce the same verdict.
                return XCTFail("no text field with placeholder \"150000\" in the rendered tree — "
                               + "this check cannot have run (process locale: \(Locale.autoupdatingCurrent.identifier))")
            }
            XCTAssertEqual(rendered, expected,
                           "pinned \(locale.identifier) but rendered \(rendered.debugDescription) "
                           + "— the process locale is \(Locale.autoupdatingCurrent.identifier)")
        }
    }

    /// The locale the suites actually pin renders what the committed reference
    /// shows. Stated separately from the arm above so a change to
    /// `referenceLocale` fails HERE, naming the constant, rather than as an
    /// opaque byte mismatch in `BackchannelRulesViewSnapshotTests`.
    func testReferenceLocaleRendersTheGroupingTheCommittedReferenceShows() {
        XCTAssertEqual(renderedThreshold(locale: PinnedLocaleSnapshot.referenceLocale), "150.000",
                       "the committed tokens reference PNG reads \"150.000\"")
    }

    /// …and the default a suite gets when it names no locale is that same one,
    /// so the arms above cannot pass while every real snapshot renders under
    /// something else.
    func testTheHostsDefaultLocaleIsTheReferenceLocale() {
        let model = BackchannelRulesModel()
        model.rules = [
            BackchannelRule(
                id: "r1", enabled: true, name: "Auto-compact",
                trigger: .init(event: BackchannelRule.eventContextTokens, threshold: 150_000),
                actions: [.init(kind: BackchannelRule.actionInput, preset: BackchannelRule.presetCompact)],
                adapter: nil
            )
        ]
        let defaulted = PinnedSnapshotHost(
            BackchannelRulesView(model: model).frame(width: 304, height: 220),
            width: 304, height: 220)   // no `locale:` — exactly what the suites write
        let rendered = PinnedLocaleSnapshot.textFields(in: defaulted.view)
            .first { $0.placeholder == "150000" }?.value
        XCTAssertEqual(rendered, renderedThreshold(locale: PinnedLocaleSnapshot.referenceLocale),
                       "the host's default locale is not `PinnedLocaleSnapshot.referenceLocale`")
    }

    /// …and the two constants the both-sides arms rely on really do disagree,
    /// so a future edit cannot quietly make this suite tautological by setting
    /// them to the same thing.
    func testReferenceAndContrastingLocalesGroupDifferently() {
        let a = 150_000.0.formatted(.number.locale(PinnedLocaleSnapshot.referenceLocale))
        let b = 150_000.0.formatted(.number.locale(PinnedLocaleSnapshot.contrastingLocale))
        XCTAssertNotEqual(a, b, "referenceLocale and contrastingLocale group thousands the same way (\(a)) — "
                          + "every both-sides assertion here is vacuous")
    }

    // MARK: - The seam costs the app nothing

    /// `\.formatLocale` defaults to `Locale.autoupdatingCurrent`, which is what
    /// a bare `.number` already resolves through — so the shipping app renders
    /// exactly what it rendered before #1630, on every host, for every locale.
    ///
    /// This is the weaker half of that claim (one host, five numbers). The
    /// stronger half is that #1630 regenerated no reference PNG: all 53
    /// committed snapshots still match byte-for-byte on the machine that
    /// recorded them, with the seam in place.
    func testDefaultIsIndistinguishableFromABareNumberStyle() {
        for value in [150_000.0, 85, 1234.5, -9_876_543.21, 0] {
            XCTAssertEqual(value.formatted(.number),
                           value.formatted(.number.locale(.autoupdatingCurrent)),
                           "`.number` and `.number.locale(.autoupdatingCurrent)` differ for \(value)")
        }
    }
}
