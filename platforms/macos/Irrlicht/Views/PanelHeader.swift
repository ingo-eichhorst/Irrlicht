import SwiftUI

/// Shared header for every full-panel swap inside the menu-bar popover
/// (History, Settings, and future sub-panels — issue #940). A leading "‹
/// Back" affordance, a centered title, and an invisible mirror of Back on
/// the trailing side so the title stays centered regardless of how wide
/// Dynamic Type/accessibility text sizing makes the Back button — the two
/// Spacers guarantee a minimum gap on both sides.
struct PanelHeader: View {
    let title: String
    let onBack: () -> Void

    var body: some View {
        HStack {
            backButton
            Spacer(minLength: IrrSpacing.sp2)
            Text(title)
                .font(.headline)
            Spacer(minLength: IrrSpacing.sp2)
            backButton.opacity(0).allowsHitTesting(false).accessibilityHidden(true)
        }
        .padding(.horizontal, IrrSpacing.sp4)
        .padding(.vertical, IrrSpacing.sp3)
    }

    private var backButton: some View {
        Button(action: onBack) {
            HStack(spacing: 2) {
                Image(systemName: "chevron.left")
                Text("Back")
            }
            .foregroundColor(.secondary)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}
