import SwiftUI

/// The daemon-wide error strip (#1802).
///
/// A session that fails turns red on its own row. A failure with no session
/// behind it — the daemon is not answering, hooks are not installed, no
/// adapters loaded — has no row to turn red, so it needs a surface of its own.
/// This is that surface, and it is a SECOND mechanism deliberately: it reuses
/// none of the session-state plumbing, because there is no session to hang it
/// on. That cost is recorded on #1796.
///
/// Modelled on `UnappliedGrantsBanner` down to the structure — a non-optional
/// already-computed summary in, no store access, no `@State`, the show/hide
/// decision made outside by the parent unwrapping an `Optional`. Three things
/// it keeps from that precedent on purpose:
///
///   - It never nags. No modal, no repetition, and no promotion of the
///     menu-bar icon to `.attention`, which stays reserved for pending consent
///     — the one case where the user must act right now.
///   - It offers no dismiss control. "Dismissible by fixing" is the whole
///     contract: a hide button would let a live fault be silenced while still
///     broken, which is the defect rather than the cure.
///   - It renders each cause verbatim rather than only the count, because the
///     aggregate must not collapse the diagnoses — two faults that share a
///     headline are told apart only by their reason text.
///
/// It differs from that precedent in two ways, both deliberate. It is RED
/// rather than orange-on-`waitingDim`: orange is this app's "an agent is
/// waiting on you" hue, and a daemon fault is not a question anyone can
/// answer. And it carries no action button — the grants banner has a wizard to
/// send you to, and a stalled daemon has no equivalent one-click route, so
/// offering a button that did nothing would be worse than offering none.
struct DaemonErrorBanner: View {
    let summary: DaemonErrorSummary

    var body: some View {
        VStack(alignment: .leading, spacing: IrrSpacing.sp1) {
            HStack(spacing: IrrSpacing.sp2) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundColor(IrrColors.errorPillText)
                Text(summary.text)
                    .font(.caption)
                    .foregroundColor(IrrColors.errorPillText)
                Spacer()
            }
            ForEach(summary.items) { fault in
                // errorPillText, not the raw `error` hue: this text sits on a
                // 12% wash of that same hue, where the brand red measures under
                // WCAG AA in both appearances — the finding #984 made about the
                // question pill, one colour over. The reason itself uses native
                // primary: it is the longest text here and has to stay the most
                // readable thing on the strip.
                (Text("\(fault.title): ")
                    .foregroundColor(IrrColors.errorPillText)
                 + Text(fault.reason)
                    .foregroundColor(.primary))
                    .font(.caption2)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, IrrSpacing.sp3)
        .padding(.vertical, 6)
        .background(IrrColors.errorDim)
        // An interruption rather than a standing report — the AppKit
        // counterpart of `role="alert"`, not `role="status"`. The grants banner
        // chose `status` because an unapplied grant is a condition you can read
        // whenever you get to it; a daemon that is not responding invalidates
        // everything else on the panel, so it is worth cutting in for.
        .accessibilityElement(children: .contain)
        .accessibilityLabel(summary.text)
        .accessibilityAddTraits(.isStaticText)
        .accessibilityIdentifier("daemon-error-banner")
    }
}
