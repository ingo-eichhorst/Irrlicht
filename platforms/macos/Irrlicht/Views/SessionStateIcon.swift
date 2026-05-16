import SwiftUI

/// Per-row session-state glyph, unified with the web dashboard
/// (`platforms/web/index.html` `svgIcons`):
///
/// - working: solid inner dot + animated halo ring (expands and fades).
/// - waiting: two-bar pause.
/// - ready:   SF Symbol `checkmark.circle.fill` (unchanged from before).
///
/// The halo animation is suppressed when the user has enabled Reduce Motion;
/// only the steady inner dot remains.
struct SessionStateIcon: View {
    let state: SessionState.State
    let size: CGFloat

    var body: some View {
        switch state {
        case .working:
            WorkingIcon(size: size)
        case .waiting:
            WaitingIcon(size: size)
        case .ready:
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: size))
                .foregroundColor(IrrColors.ready)
        }
    }
}

/// Heartbeat halo — mirrors the SMIL `<animate>` in the web SVG:
/// halo scales r 3→9 (i.e. 1.0×→3.0× starting size), opacity 0.85→0,
/// stroke 1.5→0.4 over 1.6s, repeating forever.
private struct WorkingIcon: View {
    let size: CGFloat
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var animating = false

    private static let period: Double = 1.6
    private static let coreRatio: CGFloat = 2.6 / 20   // SVG dot radius / viewBox
    private static let haloStartRatio: CGFloat = 3 / 20
    private static let haloEndScale: CGFloat = 9 / 3   // r=3 → r=9

    var body: some View {
        ZStack {
            // Steady inner dot
            Circle()
                .fill(IrrColors.working)
                .frame(width: size * Self.coreRatio * 2,
                       height: size * Self.coreRatio * 2)

            if !reduceMotion {
                let haloDiameter = size * Self.haloStartRatio * 2
                Circle()
                    .stroke(IrrColors.working,
                            lineWidth: animating ? 0.4 : 1.5)
                    .frame(width: haloDiameter, height: haloDiameter)
                    .scaleEffect(animating ? Self.haloEndScale : 1.0)
                    .opacity(animating ? 0 : 0.85)
                    .animation(
                        .linear(duration: Self.period)
                            .repeatForever(autoreverses: false),
                        value: animating
                    )
            }
        }
        .frame(width: size, height: size)
        .onAppear { animating = true }
    }
}

/// Two-bar pause — mirrors the existing web waiting SVG
/// (`viewBox="0 0 16 16"`, rects 2.5×10 at x=4 and x=9.5).
private struct WaitingIcon: View {
    let size: CGFloat

    private static let barWidthRatio: CGFloat = 2.5 / 16
    private static let barHeightRatio: CGFloat = 10 / 16
    private static let gapRatio: CGFloat = 3 / 16   // 9.5 - 4 - 2.5 = 3
    private static let cornerRatio: CGFloat = 1 / 16

    var body: some View {
        let barWidth = size * Self.barWidthRatio
        let barHeight = size * Self.barHeightRatio
        let gap = size * Self.gapRatio
        let corner = size * Self.cornerRatio
        HStack(spacing: gap) {
            RoundedRectangle(cornerRadius: corner)
                .fill(IrrColors.waiting)
                .frame(width: barWidth, height: barHeight)
            RoundedRectangle(cornerRadius: corner)
                .fill(IrrColors.waiting)
                .frame(width: barWidth, height: barHeight)
        }
        .frame(width: size, height: size)
    }
}
