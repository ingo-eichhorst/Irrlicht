import SwiftUI

/// The passive "N permissions are granted but not applied" strip (#1385).
///
/// #1362 made a failed consent effect visible per permission, inside the
/// wizard — so a re-apply failure at `Start`, when every granted effect is
/// re-run at once, reached only a user who opened Settings. This is the
/// surface that closes that gap on macOS, and it is deliberately the
/// quietest thing that can do so. Three things it does NOT do:
///
///   - It never presents the wizard by itself. That is the
///     fail → wizard → retry → fail loop #1362 avoided on purpose; the
///     route on is the button below, which the user chooses to press.
///   - It never nags. No modal, no repetition, no badge on the menu-bar
///     icon — `MenuBarImageBuilder`'s `.attention` state stays reserved for
///     PENDING consent, which is the one case where the user must act now.
///   - It offers no dismiss control. "Dismissible by fixing" is the whole
///     contract: a hide button would let a live fault be silenced while
///     still broken, which is the defect rather than the cure.
///
/// It renders each cause verbatim rather than only the count, because the
/// aggregate must not collapse the diagnoses: an install that FAILED
/// (#1362) and a refusal because the CLI is below its declared version
/// floor (#1365) both arrive here, and the reason text is what tells them
/// apart — the refusal's even carries its own fix.
///
/// Mirrors `renderUnappliedGrantsBanner` in platforms/web/permissionsWizard.js.
struct UnappliedGrantsBanner: View {
    let summary: UnappliedGrantSummary
    let onReview: () -> Void

    var body: some View {
        BannerStrip(
            icon: "exclamationmark.triangle",
            // waitingPillText, not the raw `waiting` hue: this text sits on the
            // same 12% wash the question pill does, where the brand hue
            // measures ~2:1 and fails WCAG AA (#984).
            tint: IrrColors.waitingPillText,
            wash: IrrColors.waitingDim,
            headline: summary.text,
            rows: summary.items.map {
                BannerRow(id: $0.id, lead: "\($0.agentDisplayName) — \($0.title)", reason: $0.reason)
            }
        ) {
            // A standing condition being reported, not an interruption — the
            // AppKit counterpart of the web banner's role="status", so no
            // announcement is posted. The route on is this button, which the
            // user chooses to press.
            Button("Review permissions", action: onReview)
                .font(.caption)
                .padding(.top, 2)
        }
    }
}
