import AppKit
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

    /// The headline last announced, so a re-render does not re-announce an
    /// unchanged fault. This is the AppKit counterpart of the web banner's
    /// `el.dataset.bannerKey` reconcile-and-skip (#1801's
    /// `renderUnappliedGrantsBanner`), and it exists for the same reason:
    /// SwiftUI rebuilds this view on every `@Published` touch of the manager,
    /// and an announcement per rebuild would talk over a screen-reader user
    /// continuously while the fault stands.
    @State private var announced: String?

    var body: some View {
        BannerStrip(
            icon: "exclamationmark.triangle.fill",
            tint: IrrColors.errorPillText,
            wash: IrrColors.errorDim,
            headline: summary.text,
            rows: summary.items.map {
                BannerRow(id: $0.id, lead: $0.title, reason: $0.reason)
            }
        )
        .accessibilityIdentifier("daemon-error-banner")
        // An interruption rather than a standing report — the AppKit
        // counterpart of `role="alert"`, not `role="status"`. The grants banner
        // chose `status` because an unapplied grant is a condition you can read
        // whenever you get to it; a daemon that is not responding invalidates
        // everything else on the panel, so it is worth cutting in for.
        //
        // `BannerStrip`'s own accessibility modifiers are the `status` half —
        // they make the strip readable once focus reaches it, and announce
        // NOTHING on their own. `alert` is the posted announcement below;
        // without it the comment above would be claiming a role the code does
        // not implement, and the two banners would be indistinguishable to
        // assistive tech.
        .onAppear { announce() }
        .onChange(of: summary.text) { _ in announce() }
    }

    /// Posts a VoiceOver announcement once per distinct headline. Guarded on
    /// `announced` rather than fired unconditionally — see that property.
    private func announce() {
        guard announced != summary.text else { return }
        announced = summary.text
        NSAccessibility.post(
            element: NSApp as Any,
            notification: .announcementRequested,
            userInfo: [
                .announcement: summary.text,
                .priority: NSAccessibilityPriorityLevel.high.rawValue,
            ]
        )
    }
}
