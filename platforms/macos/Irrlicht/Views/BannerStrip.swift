import SwiftUI

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
    /// brand hue — the hue measures under AA against the 12% wash it sits on
    /// (#984 found this for the question pill; it holds for every tint here).
    let tint: Color
    /// The 12%-alpha ground. Kept as the plain brand hue so dots and glows
    /// elsewhere stay visually consistent.
    let wash: Color
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
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, IrrSpacing.sp3)
        .padding(.vertical, 6)
        .background(wash)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(headline)
    }
}

extension BannerStrip where Trailing == EmptyView {
    /// A banner with no action affordance.
    init(icon: String, tint: Color, wash: Color, headline: String, rows: [BannerRow]) {
        self.init(icon: icon, tint: tint, wash: wash, headline: headline, rows: rows) { EmptyView() }
    }
}
