import SwiftUI

// MARK: - The notice family (#1814)
//
// Everything this app uses to say "something needs your attention" lives here:
// three grounds, one alpha, two composed surfaces.
//
//   - `noticeGround(hue:)` — the panel-width, full-bleed ground. Used by
//     `BannerStrip` and by `SessionListView.errorView`, which sit in the same
//     slot of the panel and had independently grown the same geometry.
//   - `alertStrip(hue:)` — the inset, rounded strip drawn INSIDE a row or a
//     wizard entry: an icon plus text, tighter than a banner because it is
//     subordinate to the thing it annotates.
//   - `pill(color:hue:…)` — the text-only badge, no icon, single line by
//     default.
//
// They differ in geometry and in nothing else. Every one of them draws its
// ground with `IrrColors.noticeWash(_:)`, which is the whole point: the alpha
// is declared once, at `IrrColors.noticeWashAlpha`, and
// `NoticeWashLintTests` fails the build on a second spelling of it.
//
// That rule exists because the divergence it prevents had already happened.
// Five sites spelled the alpha by hand and three values shipped — 0.12 in the
// row, 0.08 in the permission wizard, 0.10 in the panel's error strip — while
// `TokenContrastTests` proved WCAG AA against 0.12 for all of them. For the
// two that were not 0.12 the proof was measuring a wash nothing rendered
// (#1802).

// MARK: - Grounds

/// The panel-width notice ground: full-bleed, square-cornered, the same
/// horizontal rhythm as the panel's other full-width chrome.
///
/// Square corners and edge-to-edge width are what make this read as a band
/// across the panel rather than as a card inside it — these notices are
/// separated from the list by a `Divider()`, not by a margin.
struct NoticeGround: ViewModifier {
    /// The notice's plain brand hue, NOT a pre-dimmed token — the alpha comes
    /// from `IrrColors.noticeWash`, so handing it one of the `*Dim` grounds
    /// would composite the alpha twice and render a wash of about 1.4%.
    let hue: Color

    func body(content: Content) -> some View {
        content
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, IrrSpacing.sp3)
            .padding(.vertical, 6)
            .background(IrrColors.noticeWash(hue))
    }
}

extension View {
    func noticeGround(hue: Color) -> some View { modifier(NoticeGround(hue: hue)) }
}

/// Shared chrome for the inline ALERT strips — an icon plus text on a tinted
/// wash, as opposed to `PillText`'s text-only pill.
///
/// Three users, and they had drifted: the context-pressure alert (#689), the
/// session error line (#1802) and the permission wizard's "granted, but not
/// applied" notice (#1362) hand-rolled the same modifiers with different
/// constants — the last of them under a comment claiming it matched the first.
/// One copy now, so a future fourth strip cannot introduce a fourth set.
///
/// The trailing `.padding(.top, 2)` is an outward gap, not inner padding: it
/// separates the strip from whatever it annotates, so a caller that wants a
/// larger gap adds to it rather than replacing it.
struct AlertStrip: ViewModifier {
    /// The notice's plain brand hue — see `NoticeGround.hue`.
    let hue: Color

    func body(content: Content) -> some View {
        content
            .padding(.horizontal, 4)
            .padding(.vertical, 2)
            .background(IrrColors.noticeWash(hue))
            .cornerRadius(IrrRadius.sm)
            .padding(.top, 2)
    }
}

extension View {
    func alertStrip(hue: Color) -> some View { modifier(AlertStrip(hue: hue)) }
}

/// Shared shape for the row's single-line notice pills (pending question,
/// cache-bloat badge): tinted text on a dim background, full-width,
/// truncating rather than wrapping.
///
/// `color` and `hue` are separate (issue #984): `hue` (defaulting to `color`)
/// tints the background and is kept at the plain brand colour so dots/glows
/// elsewhere stay visually consistent; `color` draws the text and can be a
/// different, per-appearance-tuned value where the brand hue itself doesn't
/// clear WCAG AA against that wash (see `IrrColors.waitingPillText`).
///
/// `lineLimit` defaults to 1 (the cache-bloat badge); the question pill
/// requests more (issue #979) since the daemon no longer pre-truncates it to
/// a single-line-sized cut.
struct PillText: ViewModifier {
    let color: Color
    let hue: Color
    var font: Font = .system(size: 10)
    var lineLimit: Int = 1

    func body(content: Content) -> some View {
        content
            .font(font)
            .foregroundColor(color)
            .lineLimit(lineLimit)
            .truncationMode(.tail)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 5)
            .padding(.vertical, 3)
            .background(IrrColors.noticeWash(hue))
            .cornerRadius(IrrRadius.sm)
    }
}

extension View {
    func pill(color: Color, hue: Color? = nil, font: Font = .system(size: 10), lineLimit: Int = 1) -> some View {
        modifier(PillText(color: color, hue: hue ?? color, font: font, lineLimit: lineLimit))
    }
}

// MARK: - Banners

/// One row of a `BannerStrip` — a cause, stated as a bold lead-in plus its
/// reason in body text.
///
/// The reason is carried and rendered VERBATIM rather than summarised: a
/// banner's headline collapses its causes into a count, so this text is the
/// only thing left that tells two causes with the same headline apart. That
/// argument is why `UnappliedGrantsBanner` renders each grant's reason (#1385)
/// and why `DaemonErrorBanner` renders each fault's (#1802).
struct BannerRow: Identifiable {
    let id: String
    /// Bold lead-in, drawn in the banner's tint.
    let lead: String
    /// The explanation, drawn in native primary — it is the longest text on
    /// the strip and has to stay the most readable thing on it.
    let reason: String
}

/// Shared chrome for the panel's full-width banner strips.
///
/// Two banners were ~30 near-identical lines apart, differing only in two
/// colour tokens and a glyph: `UnappliedGrantsBanner` (#1385, orange, "N
/// permissions are granted but not applied") and `DaemonErrorBanner` (#1802,
/// red, "Irrlicht has N problems"). One copy of the layout now; each banner
/// keeps its own type, its own copy, and its own decision about when to exist.
///
/// What deliberately stays OUT of here, because it is where the two genuinely
/// differ:
///
///   - **The show/hide decision.** Each banner's summary type is failable on
///     an empty item list, the parent unwraps an `Optional`, and this view
///     always renders. That split is what lets "healthy produces no banner" be
///     asserted as a plain `XCTAssertNil` rather than by photographing the
///     absence of a strip.
///   - **Announcement.** The grants banner is a standing report (`role=
///     "status"`); the daemon-error banner interrupts (`role="alert"`). Only
///     the latter posts a VoiceOver announcement, so that stays with it.
///   - **Trailing content.** The grants banner offers a "Review permissions"
///     button; a stalled daemon has no equivalent one-click route, so it
///     passes nothing. Hence the `@ViewBuilder` trailing closure rather than
///     an optional title/action pair.
struct BannerStrip<Trailing: View>: View {
    /// SF Symbol drawn beside the headline.
    let icon: String
    /// Headline and lead-in colour. A per-appearance WCAG retune, not the raw
    /// brand hue — the hue measures under AA against the wash it sits on
    /// (#984 found this for the question pill; it holds for every tint here).
    let tint: Color
    /// The ground's plain brand hue — see `NoticeGround.hue`. Kept raw so dots
    /// and glows elsewhere stay visually consistent; `noticeGround` supplies
    /// the shared alpha.
    let hue: Color
    let headline: String
    let rows: [BannerRow]
    @ViewBuilder let trailing: () -> Trailing

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            HStack(spacing: IrrSpacing.sp2) {
                Image(systemName: icon)
                    .foregroundColor(tint)
                Text(headline)
                    .font(.caption)
                    .foregroundColor(tint)
                Spacer()
            }
            ForEach(rows) { row in
                (Text("\(row.lead): ")
                    .foregroundColor(tint)
                 + Text(row.reason)
                    .foregroundColor(.primary))
                    .font(.caption2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            trailing()
        }
        .noticeGround(hue: hue)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(headline)
    }
}

extension BannerStrip where Trailing == EmptyView {
    /// A banner with no action affordance.
    init(icon: String, tint: Color, hue: Color, headline: String, rows: [BannerRow]) {
        self.init(icon: icon, tint: tint, hue: hue, headline: headline, rows: rows) { EmptyView() }
    }
}
