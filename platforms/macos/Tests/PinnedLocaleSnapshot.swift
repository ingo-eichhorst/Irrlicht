import AppKit
import SwiftUI
@testable import Irrlicht

/// The locale every image snapshot is rendered under, so a committed reference
/// is a picture of the VIEW and not of the recording machine's regional
/// settings (#1630).
///
/// ## What went wrong
///
/// `BackchannelRulesView`'s threshold field renders through `format: .number`,
/// which resolves via `Locale.autoupdatingCurrent`. The reference host is
/// `de_DE`, so
/// `__Snapshots__/BackchannelRulesViewSnapshotTests/testBackchannelRuleContextTokens.1.png`
/// reads `150.000`; a `macos-latest` runner is `en_US` and renders `150,000`.
/// Every contributor whose Mac groups thousands differently fails that test
/// locally, with a "Snapshot does not match reference." that names no cause.
/// This is the locale sibling of what `PinnedScaleSnapshot` is for the backing
/// scale: a value read from the MACHINE where the equivalent value was
/// available from the INPUT.
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
    /// `defaults read -g AppleLocale` on the reference Mac → `de_DE`, and the
    /// tokens reference shows `150.000`, which is `de_DE` grouping.
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
/// `PinnedLocaleSnapshotTests` grades this object and not one it assembled
/// itself, so "the host the suites use" and "the host the pin was proven on"
/// cannot be two things that disagree.
///
/// The remaining hole is the one a type cannot close: a suite that stops using
/// `.pinnedImage` altogether. `ImageSnapshotCIScopeTests` already derives its CI
/// classification from that same string, so such a suite loses the scale pin,
/// the locale pin and its CI classification together, as one visible failure
/// there.
struct PinnedSnapshotHost {
    /// The laid-out hosting view. Read it to inspect what was rendered; pass
    /// the `PinnedSnapshotHost` itself to `assertSnapshot`.
    let view: NSView

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
    init(_ content: some View,
         width: CGFloat,
         height: CGFloat,
         appearance: NSAppearance.Name = .darkAqua,
         locale: Locale = PinnedLocaleSnapshot.referenceLocale) {
        let hosting = NSHostingView(rootView: content.pinnedLocale(locale))
        hosting.appearance = NSAppearance(named: appearance)
        hosting.frame = CGRect(x: 0, y: 0, width: width, height: height)
        hosting.layoutSubtreeIfNeeded()
        self.view = hosting
    }
}
