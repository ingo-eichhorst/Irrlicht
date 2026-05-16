import SwiftUI

/// Per-row session-state glyph, unified with the web dashboard
/// (`platforms/web/index.html` `svgIcons`):
///
/// - working: large solid dot (r = 7/20 of frame), opacity-only breathe
///            between 0.55 and 1.0 over 1.4 s.
/// - waiting: two-bar pause.
/// - ready:   SF Symbol `checkmark.circle.fill` (unchanged from before).
///
/// The opacity animation is suppressed when the user has enabled Reduce
/// Motion; the dot remains at full opacity, fully visible.
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

/// Breathing solid dot — mirrors the web `.row-state-icon svg circle.core`
/// keyframes (`irrlicht-breathe`): opacity 0.55 ↔ 1.0 over 1.4 s, ease-in-out,
/// autoreversing. No scaling — the dot is always at its full r=7/20 footprint
/// (70 % of the cell) so it stays clearly visible even if the animation drops
/// frames or stops entirely. v0.4.5's halo-based glyph degraded to the tiny
/// inner dot on macOS frame-degradation and on reduced-motion; this design
/// removes that failure mode by construction.
private struct WorkingIcon: View {
    let size: CGFloat
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    private static let period: Double = 1.4
    private static let dotRatio: CGFloat = 7 / 20   // r=7 of viewBox 20
    private static let dimOpacity: Double = 0.55

    var body: some View {
        let diameter = size * Self.dotRatio * 2
        Circle()
            .fill(IrrColors.working)
            .frame(width: diameter, height: diameter)
            .opacity(reduceMotion ? 1 : (dim ? Self.dimOpacity : 1))
            .animation(
                reduceMotion ? nil :
                    .easeInOut(duration: Self.period / 2)
                        .repeatForever(autoreverses: true),
                value: dim
            )
            .frame(width: size, height: size)
            .onAppear { if !reduceMotion { dim = true } }
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
